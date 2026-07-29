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
	// RepositoryExtractionWorkRecoveryDispositionRequeued means the expired work became pending again.
	RepositoryExtractionWorkRecoveryDispositionRequeued = "requeued"
	// RepositoryExtractionWorkRecoveryDispositionSuperseded means newer pending work replaced the expired work.
	RepositoryExtractionWorkRecoveryDispositionSuperseded = "superseded"
)

// RepositoryExtractionWorkRecoveryInput identifies one expired claim for explicit recovery.
type RepositoryExtractionWorkRecoveryInput struct {
	RequestID       string `json:"request_id"`
	WorkItemID      string `json:"work_item_id"`
	ClaimID         string `json:"claim_id"`
	RecoveryActorID string `json:"recovery_actor_id"`
}

// RepositoryExtractionWorkRecoveryResult reports the durable expired-claim transition.
type RepositoryExtractionWorkRecoveryResult struct {
	RequestID              string                   `json:"request_id"`
	Work                   RepositoryExtractionWork `json:"work"`
	ClaimID                string                   `json:"claim_id"`
	AttemptNumber          int                      `json:"attempt_number"`
	WorkerID               string                   `json:"worker_id"`
	LeaseExpiresAt         time.Time                `json:"lease_expires_at"`
	RecoveredAt            time.Time                `json:"recovered_at"`
	RecoveryActorID        string                   `json:"recovery_actor_id"`
	Disposition            string                   `json:"disposition"`
	SupersededByWorkItemID string                   `json:"superseded_by_work_item_id,omitempty"`
	Replayed               bool                     `json:"replayed"`
}

type persistedRepositoryExtractionWorkRecoveryRequest struct {
	result             RepositoryExtractionWorkRecoveryResult
	requestPayloadHash string
}

type repositoryExtractionWorkRecoveryRequest struct {
	input              RepositoryExtractionWorkRecoveryInput
	recoveredAt        time.Time
	requestPayloadHash string
}

// RecoverExpiredRepositoryExtractionWork releases one exact claim after its lease expires.
func RecoverExpiredRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkRecoveryInput) (RepositoryExtractionWorkRecoveryResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func recoverExpiredRepositoryExtractionWork(ctx context.Context, db sqlDB, input RepositoryExtractionWorkRecoveryInput, recoveredAt time.Time) (RepositoryExtractionWorkRecoveryResult, error) {
	request, err := prepareRepositoryExtractionWorkRecovery(input, recoveredAt)
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}

	var result RepositoryExtractionWorkRecoveryResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		var err error
		result, err = recoverExpiredRepositoryExtractionWorkTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	return result, nil
}

func prepareRepositoryExtractionWorkRecovery(input RepositoryExtractionWorkRecoveryInput, recoveredAt time.Time) (repositoryExtractionWorkRecoveryRequest, error) {
	input, err := validateRepositoryExtractionWorkRecoveryInput(input)
	if err != nil {
		return repositoryExtractionWorkRecoveryRequest{}, err
	}
	if recoveredAt.IsZero() {
		return repositoryExtractionWorkRecoveryRequest{}, newDomainError(ErrorInvalidInput, "recovered_at is required")
	}
	payloadHash, err := repositoryExtractionWorkRecoveryPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkRecoveryRequest{}, err
	}
	return repositoryExtractionWorkRecoveryRequest{
		input:              input,
		recoveredAt:        recoveredAt.UTC(),
		requestPayloadHash: payloadHash,
	}, nil
}

func recoverExpiredRepositoryExtractionWorkTx(ctx context.Context, tx sqlTx, request repositoryExtractionWorkRecoveryRequest) (RepositoryExtractionWorkRecoveryResult, error) {
	input := request.input
	if result, ok, err := replayRepositoryExtractionWorkRecoveryIfPresent(ctx, tx, input.RequestID, request.requestPayloadHash); err != nil || ok {
		return result, err
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, input.WorkItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work %s was not found", input.WorkItemID)
	}
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	if err := lockRepositorySourceStream(ctx, tx, work.work.RepoID, work.work.ExtractorName); err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-recovery", input.RequestID); err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	if result, ok, err := replayRepositoryExtractionWorkRecoveryIfPresent(ctx, tx, input.RequestID, request.requestPayloadHash); err != nil || ok {
		return result, err
	}
	work, err = readRepositoryExtractionWorkByIDForUpdate(ctx, tx, input.WorkItemID)
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	result, err := recoverExpiredRepositoryExtractionWorkLocked(ctx, tx, work, input.ClaimID, "", request.recoveredAt)
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	result.RequestID = input.RequestID
	result.RecoveryActorID = input.RecoveryActorID
	if err := persistRepositoryExtractionWorkRecoveryRequest(ctx, tx, result, request.requestPayloadHash); err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	return result, nil
}

func replayRepositoryExtractionWorkRecoveryIfPresent(ctx context.Context, tx sqlTx, requestID, requestPayloadHash string) (RepositoryExtractionWorkRecoveryResult, bool, error) {
	persisted, ok, err := readRepositoryExtractionWorkRecoveryRequest(ctx, tx, requestID)
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, false, err
	}
	if !ok {
		return RepositoryExtractionWorkRecoveryResult{}, false, nil
	}
	if persisted.requestPayloadHash != requestPayloadHash {
		return RepositoryExtractionWorkRecoveryResult{}, false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work recovery payload", requestID)
	}
	result := persisted.result
	result.Replayed = true
	return result, true, nil
}

func recoverExpiredRepositoryExtractionWorkLocked(ctx context.Context, tx sqlTx, work repositoryExtractionWorkRecord, claimID, expectedWorkerID string, recoveredAt time.Time) (RepositoryExtractionWorkRecoveryResult, error) {
	if work.status != repositoryExtractionWorkRunning || work.claimID != claimID {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not running claim %s", work.work.WorkItemID, claimID)
	}
	if expectedWorkerID != "" && work.claimedBy != expectedWorkerID {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not running claim %s for worker %s", work.work.WorkItemID, claimID, expectedWorkerID)
	}

	attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, claimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", claimID)
	}
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	if attempt.workItemID != work.work.WorkItemID || attempt.workerID != work.claimedBy || attempt.status != repositoryExtractionWorkRunning || work.claimedAt == nil || !attempt.claimedAt.Equal(work.claimedAt.UTC()) {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s does not match running work", claimID)
	}
	if recoveredAt.Before(attempt.leaseExpiresAt) {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim %s remains leased until %s", claimID, attempt.leaseExpiresAt.UTC().Format(time.RFC3339Nano))
	}

	pending, hasPending, err := readPendingRepositoryExtractionWork(ctx, tx, work.work.RepoID, work.work.ExtractorName)
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
	}
	result := RepositoryExtractionWorkRecoveryResult{
		Work:           work.work,
		ClaimID:        claimID,
		AttemptNumber:  attempt.attemptNumber,
		WorkerID:       attempt.workerID,
		LeaseExpiresAt: attempt.leaseExpiresAt.UTC(),
		RecoveredAt:    recoveredAt,
		Disposition:    RepositoryExtractionWorkRecoveryDispositionRequeued,
	}
	status := repositoryExtractionWorkPending
	var supersededBy any
	if hasPending {
		if pending.work.ObservationNumber <= work.work.ObservationNumber {
			return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorRepositoryWorkConflict, "pending repository extraction work %s is not newer than expired work %s", pending.work.WorkItemID, work.work.WorkItemID)
		}
		status = repositoryExtractionWorkSuperseded
		supersededBy = pending.work.WorkItemID
		result.Disposition = RepositoryExtractionWorkRecoveryDispositionSuperseded
		result.SupersededByWorkItemID = pending.work.WorkItemID
	}

	if err := expireRepositoryExtractionWorkClaimAttempt(ctx, tx, attempt, recoveredAt); err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, err
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
	`, work.work.WorkItemID, claimID, status, supersededBy, recoveredAt, repositoryExtractionWorkRunning)
	if err != nil {
		return RepositoryExtractionWorkRecoveryResult{}, fmt.Errorf("recovering expired repository extraction work: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return RepositoryExtractionWorkRecoveryResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s changed during recovery", work.work.WorkItemID)
	}
	return result, nil
}

func validateRepositoryExtractionWorkRecoveryInput(input RepositoryExtractionWorkRecoveryInput) (RepositoryExtractionWorkRecoveryInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.RecoveryActorID = strings.TrimSpace(input.RecoveryActorID)
	if input.RequestID == "" {
		return RepositoryExtractionWorkRecoveryInput{}, newDomainError(ErrorInvalidInput, "repository extraction work recovery request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkRecoveryInput{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkRecoveryInput{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.RecoveryActorID == "" || len(input.RecoveryActorID) > 200 {
		return RepositoryExtractionWorkRecoveryInput{}, newDomainError(ErrorInvalidInput, "repository extraction work recovery_actor_id must contain 1 to 200 bytes")
	}
	return input, nil
}

func repositoryExtractionWorkRecoveryPayloadHash(input RepositoryExtractionWorkRecoveryInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkItemID      string `json:"work_item_id"`
		ClaimID         string `json:"claim_id"`
		RecoveryActorID string `json:"recovery_actor_id"`
	}{
		WorkItemID:      input.WorkItemID,
		ClaimID:         input.ClaimID,
		RecoveryActorID: input.RecoveryActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func persistRepositoryExtractionWorkRecoveryRequest(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkRecoveryResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_recovery_requests (
			request_id,
			work_item_id,
			claim_id,
			recovery_actor_id,
			disposition,
			superseded_by_work_item_id,
			recovered_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, result.RequestID, result.Work.WorkItemID, result.ClaimID, result.RecoveryActorID, result.Disposition, nullableString(result.SupersededByWorkItemID), result.RecoveredAt, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work recovery request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkRecoveryRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkRecoveryRequest, bool, error) {
	var persisted persistedRepositoryExtractionWorkRecoveryRequest
	var workItemID string
	var supersededBy *string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			work_item_id,
			claim_id,
			recovery_actor_id,
			disposition,
			superseded_by_work_item_id,
			recovered_at,
			request_payload_hash
		FROM repository_extraction_work_recovery_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.RecoveryActorID,
		&persisted.result.Disposition,
		&supersededBy,
		&persisted.result.RecoveredAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkRecoveryRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkRecoveryRequest{}, false, fmt.Errorf("reading repository extraction work recovery request: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if err != nil {
		return persistedRepositoryExtractionWorkRecoveryRequest{}, false, err
	}
	attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, persisted.result.ClaimID)
	if err != nil {
		return persistedRepositoryExtractionWorkRecoveryRequest{}, false, err
	}
	if attempt.workItemID != workItemID || attempt.status != repositoryExtractionWorkExpired || attempt.expiredAt == nil || !attempt.expiredAt.Equal(persisted.result.RecoveredAt.UTC()) {
		return persistedRepositoryExtractionWorkRecoveryRequest{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work recovery request %s does not match expired claim attempt", requestID)
	}
	persisted.result.Work = work.work
	persisted.result.AttemptNumber = attempt.attemptNumber
	persisted.result.WorkerID = attempt.workerID
	persisted.result.LeaseExpiresAt = attempt.leaseExpiresAt.UTC()
	persisted.result.RecoveredAt = persisted.result.RecoveredAt.UTC()
	if supersededBy != nil {
		persisted.result.SupersededByWorkItemID = *supersededBy
	}
	return persisted, true, nil
}
