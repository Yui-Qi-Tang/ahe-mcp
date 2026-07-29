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
	// RepositoryExtractionWorkExecutionRepairReasonLeaseExpired identifies the only repair authority in this slice.
	RepositoryExtractionWorkExecutionRepairReasonLeaseExpired = "lease_expired"
)

// RepositoryExtractionWorkExecutionRepairInput identifies one expired execution-owned claim.
type RepositoryExtractionWorkExecutionRepairInput struct {
	RequestID          string `json:"request_id"`
	ExecutionRequestID string `json:"execution_request_id"`
	WorkItemID         string `json:"work_item_id"`
	ClaimID            string `json:"claim_id"`
	WorkerID           string `json:"worker_id"`
	RepairActorID      string `json:"repair_actor_id"`
}

// RepositoryExtractionWorkExecutionRepairResult reports one durable execution repair transition.
type RepositoryExtractionWorkExecutionRepairResult struct {
	RequestID              string                   `json:"request_id"`
	ExecutionRequestID     string                   `json:"execution_request_id"`
	Work                   RepositoryExtractionWork `json:"work"`
	ClaimID                string                   `json:"claim_id"`
	AttemptNumber          int                      `json:"attempt_number"`
	WorkerID               string                   `json:"worker_id"`
	RepairActorID          string                   `json:"repair_actor_id"`
	RepairReason           string                   `json:"repair_reason"`
	LeaseExpiresAt         time.Time                `json:"lease_expires_at"`
	RepairedAt             time.Time                `json:"repaired_at"`
	Disposition            string                   `json:"disposition"`
	SupersededByWorkItemID string                   `json:"superseded_by_work_item_id,omitempty"`
	Replayed               bool                     `json:"replayed"`
}

type persistedRepositoryExtractionWorkExecutionRepair struct {
	result             RepositoryExtractionWorkExecutionRepairResult
	requestPayloadHash string
}

type repositoryExtractionWorkExecutionRepairRequest struct {
	input              RepositoryExtractionWorkExecutionRepairInput
	repairedAt         time.Time
	requestPayloadHash string
}

// RepairExpiredRepositoryExtractionWorkExecution releases one execution-owned claim after its lease expires.
func RepairExpiredRepositoryExtractionWorkExecution(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkExecutionRepairInput) (RepositoryExtractionWorkExecutionRepairResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func repairExpiredRepositoryExtractionWorkExecution(ctx context.Context, db sqlDB, input RepositoryExtractionWorkExecutionRepairInput, repairedAt time.Time) (RepositoryExtractionWorkExecutionRepairResult, error) {
	request, err := prepareRepositoryExtractionWorkExecutionRepair(input, repairedAt)
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}

	var result RepositoryExtractionWorkExecutionRepairResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		var err error
		result, err = repairExpiredRepositoryExtractionWorkExecutionTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	return result, nil
}

func prepareRepositoryExtractionWorkExecutionRepair(input RepositoryExtractionWorkExecutionRepairInput, repairedAt time.Time) (repositoryExtractionWorkExecutionRepairRequest, error) {
	input, err := validateRepositoryExtractionWorkExecutionRepairInput(input)
	if err != nil {
		return repositoryExtractionWorkExecutionRepairRequest{}, err
	}
	if repairedAt.IsZero() {
		return repositoryExtractionWorkExecutionRepairRequest{}, newDomainError(ErrorInvalidInput, "repaired_at is required")
	}
	payloadHash, err := repositoryExtractionWorkExecutionRepairPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkExecutionRepairRequest{}, err
	}
	return repositoryExtractionWorkExecutionRepairRequest{
		input:              input,
		repairedAt:         repairedAt.UTC(),
		requestPayloadHash: payloadHash,
	}, nil
}

func repairExpiredRepositoryExtractionWorkExecutionTx(ctx context.Context, tx sqlTx, request repositoryExtractionWorkExecutionRepairRequest) (RepositoryExtractionWorkExecutionRepairResult, error) {
	input := request.input
	if result, ok, err := replayRepositoryExtractionWorkExecutionRepairIfPresent(ctx, tx, input.RequestID, request.requestPayloadHash); err != nil || ok {
		return result, err
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, input.WorkItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryExtractionWorkExecutionRepairResult{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work %s was not found", input.WorkItemID)
	}
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	if err := lockRepositorySourceStream(ctx, tx, work.work.RepoID, work.work.ExtractorName); err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-execution-repair", input.RequestID); err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	if result, ok, err := replayRepositoryExtractionWorkExecutionRepairIfPresent(ctx, tx, input.RequestID, request.requestPayloadHash); err != nil || ok {
		return result, err
	}

	execution, ok, err := readRepositoryExtractionWorkExecutionRequest(ctx, tx, input.ExecutionRequestID)
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	if !ok {
		return RepositoryExtractionWorkExecutionRepairResult{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work execution request %s was not found", input.ExecutionRequestID)
	}
	if execution.input.WorkItemID != input.WorkItemID || execution.input.ClaimID != input.ClaimID || execution.input.WorkerID != input.WorkerID {
		return RepositoryExtractionWorkExecutionRepairResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s does not own work %s claim %s for worker %s", input.ExecutionRequestID, input.WorkItemID, input.ClaimID, input.WorkerID)
	}

	work, err = readRepositoryExtractionWorkByIDForUpdate(ctx, tx, input.WorkItemID)
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	transition, err := recoverExpiredRepositoryExtractionWorkLocked(ctx, tx, work, input.ClaimID, input.WorkerID, request.repairedAt)
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	result := RepositoryExtractionWorkExecutionRepairResult{
		RequestID:              input.RequestID,
		ExecutionRequestID:     input.ExecutionRequestID,
		Work:                   transition.Work,
		ClaimID:                transition.ClaimID,
		AttemptNumber:          transition.AttemptNumber,
		WorkerID:               transition.WorkerID,
		RepairActorID:          input.RepairActorID,
		RepairReason:           RepositoryExtractionWorkExecutionRepairReasonLeaseExpired,
		LeaseExpiresAt:         transition.LeaseExpiresAt,
		RepairedAt:             transition.RecoveredAt,
		Disposition:            transition.Disposition,
		SupersededByWorkItemID: transition.SupersededByWorkItemID,
	}
	if err := persistRepositoryExtractionWorkExecutionRepair(ctx, tx, result, request.requestPayloadHash); err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, err
	}
	return result, nil
}

func replayRepositoryExtractionWorkExecutionRepairIfPresent(ctx context.Context, tx sqlTx, requestID, requestPayloadHash string) (RepositoryExtractionWorkExecutionRepairResult, bool, error) {
	persisted, ok, err := readRepositoryExtractionWorkExecutionRepair(ctx, tx, requestID)
	if err != nil {
		return RepositoryExtractionWorkExecutionRepairResult{}, false, err
	}
	if !ok {
		return RepositoryExtractionWorkExecutionRepairResult{}, false, nil
	}
	if persisted.requestPayloadHash != requestPayloadHash {
		return RepositoryExtractionWorkExecutionRepairResult{}, false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work execution repair payload", requestID)
	}
	result := persisted.result
	result.Replayed = true
	return result, true, nil
}

func validateRepositoryExtractionWorkExecutionRepairInput(input RepositoryExtractionWorkExecutionRepairInput) (RepositoryExtractionWorkExecutionRepairInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.ExecutionRequestID = strings.TrimSpace(input.ExecutionRequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.RepairActorID = strings.TrimSpace(input.RepairActorID)
	if input.RequestID == "" {
		return RepositoryExtractionWorkExecutionRepairInput{}, newDomainError(ErrorInvalidInput, "repository extraction work execution repair request_id is required")
	}
	if input.ExecutionRequestID == "" {
		return RepositoryExtractionWorkExecutionRepairInput{}, newDomainError(ErrorInvalidInput, "repository extraction work execution repair execution_request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkExecutionRepairInput{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkExecutionRepairInput{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.WorkerID == "" || len(input.WorkerID) > 200 {
		return RepositoryExtractionWorkExecutionRepairInput{}, newDomainError(ErrorInvalidInput, "repository extraction work execution repair worker_id must contain 1 to 200 bytes")
	}
	if input.RepairActorID == "" || len(input.RepairActorID) > 200 {
		return RepositoryExtractionWorkExecutionRepairInput{}, newDomainError(ErrorInvalidInput, "repository extraction work execution repair repair_actor_id must contain 1 to 200 bytes")
	}
	return input, nil
}

func repositoryExtractionWorkExecutionRepairPayloadHash(input RepositoryExtractionWorkExecutionRepairInput) (string, error) {
	payload, err := deterministicJSON(struct {
		ExecutionRequestID string `json:"execution_request_id"`
		WorkItemID         string `json:"work_item_id"`
		ClaimID            string `json:"claim_id"`
		WorkerID           string `json:"worker_id"`
		RepairActorID      string `json:"repair_actor_id"`
	}{
		ExecutionRequestID: input.ExecutionRequestID,
		WorkItemID:         input.WorkItemID,
		ClaimID:            input.ClaimID,
		WorkerID:           input.WorkerID,
		RepairActorID:      input.RepairActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func persistRepositoryExtractionWorkExecutionRepair(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkExecutionRepairResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_execution_repairs (
			request_id,
			execution_request_id,
			work_item_id,
			claim_id,
			worker_id,
			repair_actor_id,
			repair_reason,
			lease_expires_at,
			disposition,
			superseded_by_work_item_id,
			repaired_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, result.RequestID, result.ExecutionRequestID, result.Work.WorkItemID, result.ClaimID, result.WorkerID, result.RepairActorID, result.RepairReason, result.LeaseExpiresAt, result.Disposition, nullableString(result.SupersededByWorkItemID), result.RepairedAt, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work execution repair: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkExecutionRepair(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkExecutionRepair, bool, error) {
	var persisted persistedRepositoryExtractionWorkExecutionRepair
	var workItemID string
	var supersededBy *string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			execution_request_id,
			work_item_id,
			claim_id,
			worker_id,
			repair_actor_id,
			repair_reason,
			lease_expires_at,
			disposition,
			superseded_by_work_item_id,
			repaired_at,
			request_payload_hash
		FROM repository_extraction_work_execution_repairs
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&persisted.result.ExecutionRequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.WorkerID,
		&persisted.result.RepairActorID,
		&persisted.result.RepairReason,
		&persisted.result.LeaseExpiresAt,
		&persisted.result.Disposition,
		&supersededBy,
		&persisted.result.RepairedAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkExecutionRepair{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkExecutionRepair{}, false, fmt.Errorf("reading repository extraction work execution repair: %w", err)
	}
	execution, ok, err := readRepositoryExtractionWorkExecutionRequest(ctx, tx, persisted.result.ExecutionRequestID)
	if err != nil {
		return persistedRepositoryExtractionWorkExecutionRepair{}, false, err
	}
	if !ok {
		return persistedRepositoryExtractionWorkExecutionRepair{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution repair %s has no execution authority", requestID)
	}
	attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, persisted.result.ClaimID)
	if err != nil {
		return persistedRepositoryExtractionWorkExecutionRepair{}, false, err
	}
	if execution.input.WorkItemID != workItemID ||
		execution.input.ClaimID != persisted.result.ClaimID ||
		execution.input.WorkerID != persisted.result.WorkerID ||
		attempt.workItemID != workItemID ||
		attempt.workerID != persisted.result.WorkerID ||
		attempt.status != repositoryExtractionWorkExpired ||
		attempt.expiredAt == nil ||
		!attempt.expiredAt.Equal(persisted.result.RepairedAt.UTC()) ||
		!attempt.leaseExpiresAt.Equal(persisted.result.LeaseExpiresAt.UTC()) ||
		persisted.result.RepairedAt.Before(persisted.result.LeaseExpiresAt) {
		return persistedRepositoryExtractionWorkExecutionRepair{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution repair %s has inconsistent durable authority", requestID)
	}
	persisted.result.Work = execution.work
	persisted.result.AttemptNumber = attempt.attemptNumber
	persisted.result.LeaseExpiresAt = persisted.result.LeaseExpiresAt.UTC()
	persisted.result.RepairedAt = persisted.result.RepairedAt.UTC()
	if supersededBy != nil {
		persisted.result.SupersededByWorkItemID = *supersededBy
	}
	return persisted, true, nil
}
