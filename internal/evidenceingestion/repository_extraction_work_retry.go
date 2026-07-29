package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositoryExtractionWorkRetryDispositionRequeued means the failed work became pending again.
	RepositoryExtractionWorkRetryDispositionRequeued = "requeued"
	// RepositoryExtractionWorkRetryDispositionSuperseded means newer pending work replaced the failed work.
	RepositoryExtractionWorkRetryDispositionSuperseded = "superseded"
)

// RepositoryExtractionWorkRetryInput identifies one failed claim for explicit retry.
type RepositoryExtractionWorkRetryInput struct {
	RequestID    string `json:"request_id"`
	WorkItemID   string `json:"work_item_id"`
	ClaimID      string `json:"claim_id"`
	RetryActorID string `json:"retry_actor_id"`
}

// RepositoryExtractionWorkRetryResult reports the durable failed-work transition.
type RepositoryExtractionWorkRetryResult struct {
	RequestID              string                   `json:"request_id"`
	Work                   RepositoryExtractionWork `json:"work"`
	ClaimID                string                   `json:"claim_id"`
	AttemptNumber          int                      `json:"attempt_number"`
	WorkerID               string                   `json:"worker_id"`
	FinishedAt             time.Time                `json:"finished_at"`
	FailureClass           string                   `json:"failure_class"`
	FailureMessage         string                   `json:"failure_message"`
	RetriedAt              time.Time                `json:"retried_at"`
	RetryActorID           string                   `json:"retry_actor_id"`
	Disposition            string                   `json:"disposition"`
	SupersededByWorkItemID string                   `json:"superseded_by_work_item_id,omitempty"`
	Replayed               bool                     `json:"replayed"`
}

type persistedRepositoryExtractionWorkRetryRequest struct {
	result             RepositoryExtractionWorkRetryResult
	requestPayloadHash string
}

// RetryFailedRepositoryExtractionWork releases one exact terminal failed claim for another attempt.
func RetryFailedRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkRetryInput) (RepositoryExtractionWorkRetryResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return retryFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func retryFailedRepositoryExtractionWork(ctx context.Context, db sqlDB, input RepositoryExtractionWorkRetryInput, retriedAt time.Time) (RepositoryExtractionWorkRetryResult, error) {
	input, err := validateRepositoryExtractionWorkRetryInput(input)
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, err
	}
	if retriedAt.IsZero() {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorInvalidInput, "retried_at is required")
	}
	retriedAt = retriedAt.UTC()
	payloadHash, err := repositoryExtractionWorkRetryPayloadHash(input)
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, err
	}

	var result RepositoryExtractionWorkRetryResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if replay, ok, err := replayRepositoryExtractionWorkRetryIfPresent(ctx, tx, input.RequestID, payloadHash); err != nil || ok {
			result = replay
			return err
		}
		work, err := lockRepositorySourceStreamForWork(ctx, tx, input.WorkItemID)
		if err != nil {
			return err
		}
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-retry", input.RequestID); err != nil {
			return err
		}
		if replay, ok, err := replayRepositoryExtractionWorkRetryIfPresent(ctx, tx, input.RequestID, payloadHash); err != nil || ok {
			result = replay
			return err
		}
		work, err = readRepositoryExtractionWorkByIDForUpdate(ctx, tx, input.WorkItemID)
		if err != nil {
			return err
		}
		result, err = retryFailedRepositoryExtractionWorkLocked(ctx, tx, work, input.ClaimID, retriedAt)
		if err != nil {
			return err
		}
		result.RequestID = input.RequestID
		result.RetryActorID = input.RetryActorID
		return persistRepositoryExtractionWorkRetryRequest(ctx, tx, result, payloadHash)
	})
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, err
	}
	return result, nil
}

func replayRepositoryExtractionWorkRetryIfPresent(ctx context.Context, tx sqlTx, requestID, requestPayloadHash string) (RepositoryExtractionWorkRetryResult, bool, error) {
	persisted, ok, err := readRepositoryExtractionWorkRetryRequest(ctx, tx, requestID)
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, false, err
	}
	if !ok {
		return RepositoryExtractionWorkRetryResult{}, false, nil
	}
	if persisted.requestPayloadHash != requestPayloadHash {
		return RepositoryExtractionWorkRetryResult{}, false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work retry payload", requestID)
	}
	result := persisted.result
	result.Replayed = true
	return result, true, nil
}

func retryFailedRepositoryExtractionWorkLocked(ctx context.Context, tx sqlTx, work repositoryExtractionWorkRecord, claimID string, retriedAt time.Time) (RepositoryExtractionWorkRetryResult, error) {
	if work.status != RepositoryExtractionWorkOutcomeFailed || work.claimID != claimID {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not failed claim %s", work.work.WorkItemID, claimID)
	}
	if work.claimedBy == "" || work.claimedAt == nil || work.finishedAt == nil || work.failureClass == "" || work.failureMessage == "" {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s has incomplete failed state", work.work.WorkItemID)
	}
	if retriedAt.Before(work.finishedAt.UTC()) {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s retry time precedes its failure", work.work.WorkItemID)
	}

	attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, claimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", claimID)
	}
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, err
	}
	if attempt.workItemID != work.work.WorkItemID ||
		attempt.workerID != work.claimedBy ||
		attempt.status != RepositoryExtractionWorkOutcomeFailed ||
		attempt.finishedAt == nil ||
		attempt.expiredAt != nil ||
		!attempt.claimedAt.Equal(work.claimedAt.UTC()) ||
		!attempt.finishedAt.Equal(work.finishedAt.UTC()) ||
		attempt.failureClass != work.failureClass ||
		attempt.failureMessage != work.failureMessage {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s does not match failed work", claimID)
	}

	pending, hasPending, err := readPendingRepositoryExtractionWork(ctx, tx, work.work.RepoID, work.work.ExtractorName)
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, err
	}
	result := RepositoryExtractionWorkRetryResult{
		Work:           work.work,
		ClaimID:        claimID,
		AttemptNumber:  attempt.attemptNumber,
		WorkerID:       attempt.workerID,
		FinishedAt:     attempt.finishedAt.UTC(),
		FailureClass:   attempt.failureClass,
		FailureMessage: attempt.failureMessage,
		RetriedAt:      retriedAt,
		Disposition:    RepositoryExtractionWorkRetryDispositionRequeued,
	}
	status := repositoryExtractionWorkPending
	var supersededBy any
	if hasPending {
		if pending.work.ObservationNumber <= work.work.ObservationNumber {
			return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorRepositoryWorkConflict, "pending repository extraction work %s is not newer than failed work %s", pending.work.WorkItemID, work.work.WorkItemID)
		}
		status = repositoryExtractionWorkSuperseded
		supersededBy = pending.work.WorkItemID
		result.Disposition = RepositoryExtractionWorkRetryDispositionSuperseded
		result.SupersededByWorkItemID = pending.work.WorkItemID
	}

	tag, err := tx.exec(ctx, `
		UPDATE repository_extraction_work_items
		SET status = $3,
			superseded_by_work_item_id = $4,
			claim_id = NULL,
			claimed_by = NULL,
			claimed_at = NULL,
			source_generation_id = NULL,
			finished_at = NULL,
			failure_class = NULL,
			failure_message = NULL,
			updated_at = $5
		WHERE work_item_id = $1 AND claim_id = $2 AND status = $6
	`, work.work.WorkItemID, claimID, status, supersededBy, retriedAt, RepositoryExtractionWorkOutcomeFailed)
	if err != nil {
		return RepositoryExtractionWorkRetryResult{}, fmt.Errorf("retrying failed repository extraction work: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return RepositoryExtractionWorkRetryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s changed during retry", work.work.WorkItemID)
	}
	return result, nil
}

func validateRepositoryExtractionWorkRetryInput(input RepositoryExtractionWorkRetryInput) (RepositoryExtractionWorkRetryInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.RetryActorID = strings.TrimSpace(input.RetryActorID)
	if input.RequestID == "" {
		return RepositoryExtractionWorkRetryInput{}, newDomainError(ErrorInvalidInput, "repository extraction work retry request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkRetryInput{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkRetryInput{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.RetryActorID == "" || len(input.RetryActorID) > 200 {
		return RepositoryExtractionWorkRetryInput{}, newDomainError(ErrorInvalidInput, "repository extraction work retry_actor_id must contain 1 to 200 bytes")
	}
	return input, nil
}

func repositoryExtractionWorkRetryPayloadHash(input RepositoryExtractionWorkRetryInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkItemID   string `json:"work_item_id"`
		ClaimID      string `json:"claim_id"`
		RetryActorID string `json:"retry_actor_id"`
	}{
		WorkItemID:   input.WorkItemID,
		ClaimID:      input.ClaimID,
		RetryActorID: input.RetryActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func persistRepositoryExtractionWorkRetryRequest(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkRetryResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_retry_requests (
			request_id,
			work_item_id,
			claim_id,
			retry_actor_id,
			disposition,
			superseded_by_work_item_id,
			retried_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, result.RequestID, result.Work.WorkItemID, result.ClaimID, result.RetryActorID, result.Disposition, nullableString(result.SupersededByWorkItemID), result.RetriedAt, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work retry request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkRetryRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkRetryRequest, bool, error) {
	var persisted persistedRepositoryExtractionWorkRetryRequest
	var workItemID string
	var supersededBy *string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			work_item_id,
			claim_id,
			retry_actor_id,
			disposition,
			superseded_by_work_item_id,
			retried_at,
			request_payload_hash
		FROM repository_extraction_work_retry_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.RetryActorID,
		&persisted.result.Disposition,
		&supersededBy,
		&persisted.result.RetriedAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkRetryRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkRetryRequest{}, false, fmt.Errorf("reading repository extraction work retry request: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if err != nil {
		return persistedRepositoryExtractionWorkRetryRequest{}, false, err
	}
	attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, persisted.result.ClaimID)
	if err != nil {
		return persistedRepositoryExtractionWorkRetryRequest{}, false, err
	}
	if attempt.workItemID != workItemID ||
		attempt.status != RepositoryExtractionWorkOutcomeFailed ||
		attempt.finishedAt == nil ||
		attempt.expiredAt != nil ||
		persisted.result.RetriedAt.Before(attempt.finishedAt.UTC()) {
		return persistedRepositoryExtractionWorkRetryRequest{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry request %s does not match failed claim attempt", requestID)
	}
	persisted.result.Work = work.work
	persisted.result.AttemptNumber = attempt.attemptNumber
	persisted.result.WorkerID = attempt.workerID
	persisted.result.FinishedAt = attempt.finishedAt.UTC()
	persisted.result.FailureClass = attempt.failureClass
	persisted.result.FailureMessage = attempt.failureMessage
	persisted.result.RetriedAt = persisted.result.RetriedAt.UTC()
	if supersededBy != nil {
		persisted.result.SupersededByWorkItemID = *supersededBy
	}
	return persisted, true, nil
}
