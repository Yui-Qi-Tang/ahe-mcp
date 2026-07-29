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
	repositoryExtractionWorkMaxFailureClassBytes   = 100
	repositoryExtractionWorkMaxFailureMessageBytes = 2000
)

// RepositoryExtractionWorkFinishInput records one claimed work item's terminal outcome.
type RepositoryExtractionWorkFinishInput struct {
	RequestID          string `json:"request_id"`
	WorkItemID         string `json:"work_item_id"`
	ClaimID            string `json:"claim_id"`
	WorkerID           string `json:"worker_id"`
	Outcome            string `json:"outcome"`
	SourceGenerationID string `json:"source_generation_id,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
	FailureMessage     string `json:"failure_message,omitempty"`
}

// RepositoryExtractionWorkFinishResult reports one verified terminal work transition.
type RepositoryExtractionWorkFinishResult struct {
	RequestID          string                   `json:"request_id"`
	Work               RepositoryExtractionWork `json:"work"`
	ClaimID            string                   `json:"claim_id"`
	WorkerID           string                   `json:"worker_id"`
	Outcome            string                   `json:"outcome"`
	SourceGenerationID string                   `json:"source_generation_id,omitempty"`
	FailureClass       string                   `json:"failure_class,omitempty"`
	FailureMessage     string                   `json:"failure_message,omitempty"`
	AttemptNumber      int                      `json:"attempt_number"`
	LeaseExpiresAt     time.Time                `json:"lease_expires_at"`
	FinishedAt         time.Time                `json:"finished_at"`
	Replayed           bool                     `json:"replayed"`
}

type persistedRepositoryExtractionWorkFinishRequest struct {
	result             RepositoryExtractionWorkFinishResult
	requestPayloadHash string
}

// FinishRepositoryExtractionWork atomically records a verified success or bounded failure.
func FinishRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkFinishInput) (RepositoryExtractionWorkFinishResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkFinishResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func finishRepositoryExtractionWork(ctx context.Context, db sqlDB, input RepositoryExtractionWorkFinishInput, finishedAt time.Time) (RepositoryExtractionWorkFinishResult, error) {
	input, err := validateRepositoryExtractionWorkFinishInput(input)
	if err != nil {
		return RepositoryExtractionWorkFinishResult{}, err
	}
	if finishedAt.IsZero() {
		return RepositoryExtractionWorkFinishResult{}, newDomainError(ErrorInvalidInput, "finished_at is required")
	}
	finishedAt = finishedAt.UTC()
	payloadHash, err := repositoryExtractionWorkFinishPayloadHash(input)
	if err != nil {
		return RepositoryExtractionWorkFinishResult{}, err
	}

	var result RepositoryExtractionWorkFinishResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-finish", input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkFinishRequest(ctx, tx, input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != payloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work finish payload", input.RequestID)
			}
			result = persisted.result
			result.Replayed = true
			return nil
		}

		work, err := readRepositoryExtractionWorkByID(ctx, tx, input.WorkItemID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work %s was not found", input.WorkItemID)
		}
		if err != nil {
			return err
		}
		if err := lockRepositorySourceStream(ctx, tx, work.work.RepoID, work.work.ExtractorName); err != nil {
			return err
		}
		work, err = readRepositoryExtractionWorkByIDForUpdate(ctx, tx, input.WorkItemID)
		if err != nil {
			return err
		}
		if work.status != repositoryExtractionWorkRunning {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s has status %s, want running", input.WorkItemID, work.status)
		}
		if work.claimID != input.ClaimID || work.claimedBy != input.WorkerID {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not claimed by worker %s with claim %s", input.WorkItemID, input.WorkerID, input.ClaimID)
		}
		if work.claimedAt == nil || finishedAt.Before(work.claimedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s finish time precedes its claim", input.WorkItemID)
		}
		attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, input.ClaimID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", input.ClaimID)
		}
		if err != nil {
			return err
		}
		if attempt.workItemID != input.WorkItemID || attempt.workerID != input.WorkerID || attempt.status != repositoryExtractionWorkRunning || !attempt.claimedAt.Equal(work.claimedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s does not match running work", input.ClaimID)
		}
		if !finishedAt.Before(attempt.leaseExpiresAt) {
			return newDomainError(ErrorRepositoryWorkLeaseExpired, "repository extraction work claim %s expired at %s", input.ClaimID, attempt.leaseExpiresAt.UTC().Format(time.RFC3339Nano))
		}

		switch input.Outcome {
		case RepositoryExtractionWorkOutcomeSucceeded:
			generation, err := readRepositorySourceGenerationByID(ctx, tx, input.SourceGenerationID)
			if errors.Is(err, pgx.ErrNoRows) {
				return newDomainError(ErrorMissingSourceViewAttempt, "repository source generation %s was not found", input.SourceGenerationID)
			}
			if err != nil {
				return fmt.Errorf("reading repository source generation: %w", err)
			}
			if generation.RepoID != work.work.RepoID || generation.ExtractorName != work.work.ExtractorName || generation.CommitSHA != work.work.HeadCommitSHA {
				return newDomainError(ErrorRepositoryWorkConflict, "repository source generation %s does not match work %s repository, extractor, and commit", generation.ID, work.work.WorkItemID)
			}
		case RepositoryExtractionWorkOutcomeFailed:
		default:
			return newDomainError(ErrorInvalidInput, "unsupported repository extraction work outcome %q", input.Outcome)
		}

		tag, err := tx.exec(ctx, `
			UPDATE repository_extraction_work_items
			SET status = $4,
				source_generation_id = $5,
				finished_at = $6,
				failure_class = $7,
				failure_message = $8,
				updated_at = $6
			WHERE work_item_id = $1 AND claim_id = $2 AND claimed_by = $3 AND status = $9
		`, input.WorkItemID, input.ClaimID, input.WorkerID, input.Outcome, nullableString(input.SourceGenerationID), finishedAt, nullableString(input.FailureClass), nullableString(input.FailureMessage), repositoryExtractionWorkRunning)
		if err != nil {
			return fmt.Errorf("finishing repository extraction work: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s changed during finish", input.WorkItemID)
		}
		if err := finishRepositoryExtractionWorkClaimAttempt(ctx, tx, attempt, input, finishedAt); err != nil {
			return err
		}
		result = RepositoryExtractionWorkFinishResult{
			RequestID:          input.RequestID,
			Work:               work.work,
			ClaimID:            input.ClaimID,
			WorkerID:           input.WorkerID,
			Outcome:            input.Outcome,
			SourceGenerationID: input.SourceGenerationID,
			FailureClass:       input.FailureClass,
			FailureMessage:     input.FailureMessage,
			AttemptNumber:      attempt.attemptNumber,
			LeaseExpiresAt:     attempt.leaseExpiresAt.UTC(),
			FinishedAt:         finishedAt,
		}
		return persistRepositoryExtractionWorkFinishRequest(ctx, tx, result, payloadHash)
	})
	if err != nil {
		return RepositoryExtractionWorkFinishResult{}, err
	}
	return result, nil
}

func validateRepositoryExtractionWorkFinishInput(input RepositoryExtractionWorkFinishInput) (RepositoryExtractionWorkFinishInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.SourceGenerationID = strings.TrimSpace(input.SourceGenerationID)
	input.FailureClass = strings.TrimSpace(input.FailureClass)
	input.FailureMessage = strings.TrimSpace(input.FailureMessage)
	if input.RequestID == "" {
		return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "repository extraction work finish request_id is required")
	}
	if input.WorkItemID == "" || input.ClaimID == "" {
		return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "repository extraction work finish work_item_id and claim_id are required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.WorkerID == "" || len(input.WorkerID) > 200 {
		return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "repository extraction work worker_id must contain 1 to 200 bytes")
	}
	switch input.Outcome {
	case RepositoryExtractionWorkOutcomeSucceeded:
		if input.SourceGenerationID == "" {
			return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "source_generation_id is required for succeeded repository extraction work")
		}
		if !strings.HasPrefix(input.SourceGenerationID, "generation:") {
			return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidRecordID, "source_generation_id %q must start with generation:", input.SourceGenerationID)
		}
		if input.FailureClass != "" || input.FailureMessage != "" {
			return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "succeeded repository extraction work cannot include failure fields")
		}
	case RepositoryExtractionWorkOutcomeFailed:
		if input.SourceGenerationID != "" {
			return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "failed repository extraction work cannot include source_generation_id")
		}
		if input.FailureClass == "" || len(input.FailureClass) > repositoryExtractionWorkMaxFailureClassBytes {
			return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "repository extraction work failure_class must contain 1 to %d bytes", repositoryExtractionWorkMaxFailureClassBytes)
		}
		if input.FailureMessage == "" || len(input.FailureMessage) > repositoryExtractionWorkMaxFailureMessageBytes {
			return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "repository extraction work failure_message must contain 1 to %d bytes", repositoryExtractionWorkMaxFailureMessageBytes)
		}
	default:
		return RepositoryExtractionWorkFinishInput{}, newDomainError(ErrorInvalidInput, "unsupported repository extraction work outcome %q", input.Outcome)
	}
	return input, nil
}

func repositoryExtractionWorkFinishPayloadHash(input RepositoryExtractionWorkFinishInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkItemID         string `json:"work_item_id"`
		ClaimID            string `json:"claim_id"`
		WorkerID           string `json:"worker_id"`
		Outcome            string `json:"outcome"`
		SourceGenerationID string `json:"source_generation_id,omitempty"`
		FailureClass       string `json:"failure_class,omitempty"`
		FailureMessage     string `json:"failure_message,omitempty"`
	}{
		WorkItemID:         input.WorkItemID,
		ClaimID:            input.ClaimID,
		WorkerID:           input.WorkerID,
		Outcome:            input.Outcome,
		SourceGenerationID: input.SourceGenerationID,
		FailureClass:       input.FailureClass,
		FailureMessage:     input.FailureMessage,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func readRepositoryExtractionWorkByIDForUpdate(ctx context.Context, tx sqlTx, workItemID string) (repositoryExtractionWorkRecord, error) {
	record, err := scanRepositoryExtractionWork(tx.queryRow(ctx, repositoryExtractionWorkSelect+`
		WHERE w.work_item_id = $1
		FOR UPDATE OF w
	`, workItemID))
	if err != nil {
		return repositoryExtractionWorkRecord{}, fmt.Errorf("locking repository extraction work %s: %w", workItemID, err)
	}
	return record, nil
}

func persistRepositoryExtractionWorkFinishRequest(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkFinishResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_finish_requests (
			request_id, work_item_id, claim_id, worker_id, outcome, request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, result.RequestID, result.Work.WorkItemID, result.ClaimID, result.WorkerID, result.Outcome, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work finish request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkFinishRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkFinishRequest, bool, error) {
	var persisted persistedRepositoryExtractionWorkFinishRequest
	var workItemID string
	err := tx.queryRow(ctx, `
		SELECT request_id, work_item_id, claim_id, worker_id, outcome, request_payload_hash
		FROM repository_extraction_work_finish_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.WorkerID,
		&persisted.result.Outcome,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkFinishRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkFinishRequest{}, false, fmt.Errorf("reading repository extraction work finish request: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if err != nil {
		return persistedRepositoryExtractionWorkFinishRequest{}, false, err
	}
	attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, persisted.result.ClaimID)
	if err != nil {
		return persistedRepositoryExtractionWorkFinishRequest{}, false, err
	}
	if attempt.workItemID != workItemID ||
		attempt.workerID != persisted.result.WorkerID ||
		attempt.status != persisted.result.Outcome ||
		attempt.finishedAt == nil ||
		attempt.expiredAt != nil {
		return persistedRepositoryExtractionWorkFinishRequest{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work finish request %s does not match terminal claim attempt", requestID)
	}
	persisted.result.Work = work.work
	persisted.result.SourceGenerationID = attempt.sourceGenerationID
	persisted.result.FailureClass = attempt.failureClass
	persisted.result.FailureMessage = attempt.failureMessage
	persisted.result.AttemptNumber = attempt.attemptNumber
	persisted.result.LeaseExpiresAt = attempt.leaseExpiresAt.UTC()
	persisted.result.FinishedAt = attempt.finishedAt.UTC()
	return persisted, true, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
