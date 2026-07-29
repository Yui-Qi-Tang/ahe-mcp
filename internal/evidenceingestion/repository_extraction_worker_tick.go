package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RepositoryExtractionWorkerTickInput selects one extractor stream for one bounded worker tick.
type RepositoryExtractionWorkerTickInput struct {
	RequestID                 string `json:"request_id"`
	WorkspaceRoot             string `json:"workspace_root"`
	RepoID                    string `json:"repo_id"`
	ExtractorName             string `json:"extractor_name"`
	WorkerID                  string `json:"worker_id"`
	LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
}

// RepositoryExtractionWorkerTickResult reports the durable claim decision and optional execution.
type RepositoryExtractionWorkerTickResult struct {
	RequestID string                                   `json:"request_id"`
	Claim     RepositoryExtractionWorkClaimResult      `json:"claim"`
	Execution *RepositoryExtractionWorkExecutionResult `json:"execution,omitempty"`
	Replayed  bool                                     `json:"replayed"`
}

type repositoryExtractionWorkerTickRequest struct {
	input              RepositoryExtractionWorkerTickInput
	claimRequestID     string
	executionRequestID string
	requestPayloadHash string
}

type persistedRepositoryExtractionWorkerTickRequest struct {
	input              RepositoryExtractionWorkerTickInput
	claimRequestID     string
	executionRequestID string
	claimed            bool
	workItemID         string
	claimID            string
	attemptNumber      int
	requestPayloadHash string
}

// RunRepositoryExtractionWorkerTick claims and, when available, executes one work item.
func RunRepositoryExtractionWorkerTick(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkerTickInput) (RepositoryExtractionWorkerTickResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkerTickResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := prepareRepositoryExtractionWorkerTick(input)
	if err != nil {
		return RepositoryExtractionWorkerTickResult{}, err
	}

	claim, err := ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
		RequestID:                 request.claimRequestID,
		RepoID:                    request.input.RepoID,
		ExtractorName:             request.input.ExtractorName,
		WorkerID:                  request.input.WorkerID,
		LeaseDurationMilliseconds: request.input.LeaseDurationMilliseconds,
	})
	if err != nil {
		return RepositoryExtractionWorkerTickResult{}, err
	}
	replayed, err := reserveRepositoryExtractionWorkerTick(ctx, pgxDB{pool: pool}, request, claim)
	if err != nil {
		return RepositoryExtractionWorkerTickResult{}, err
	}
	result := RepositoryExtractionWorkerTickResult{
		RequestID: request.input.RequestID,
		Claim:     claim,
		Replayed:  replayed,
	}
	if !claim.Claimed {
		return result, nil
	}

	execution, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkExecutionInput{
		RequestID:     request.executionRequestID,
		WorkspaceRoot: request.input.WorkspaceRoot,
		WorkItemID:    claim.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
	})
	if err != nil {
		return RepositoryExtractionWorkerTickResult{}, err
	}
	result.Execution = &execution
	return result, nil
}

func prepareRepositoryExtractionWorkerTick(input RepositoryExtractionWorkerTickInput) (repositoryExtractionWorkerTickRequest, error) {
	requestID, repoID, extractorName, workerID, _, err := validateRepositoryExtractionWorkClaimInput(RepositoryExtractionWorkClaimInput{
		RequestID:                 input.RequestID,
		RepoID:                    input.RepoID,
		ExtractorName:             input.ExtractorName,
		WorkerID:                  input.WorkerID,
		LeaseDurationMilliseconds: input.LeaseDurationMilliseconds,
	})
	if err != nil {
		return repositoryExtractionWorkerTickRequest{}, err
	}
	workspaceRoot, err := canonicalDirectory(input.WorkspaceRoot)
	if err != nil {
		return repositoryExtractionWorkerTickRequest{}, err
	}
	input.RequestID = requestID
	input.WorkspaceRoot = workspaceRoot
	input.RepoID = repoID
	input.ExtractorName = extractorName
	input.WorkerID = workerID

	claimRequestID, err := repositoryExtractionWorkerTickChildRequestID("work-tick-claim:", "repository_extraction_worker_tick_claim", requestID)
	if err != nil {
		return repositoryExtractionWorkerTickRequest{}, err
	}
	executionRequestID, err := repositoryExtractionWorkerTickChildRequestID("work-tick-execute:", "repository_extraction_worker_tick_execution", requestID)
	if err != nil {
		return repositoryExtractionWorkerTickRequest{}, err
	}
	payloadHash, err := repositoryExtractionWorkerTickPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkerTickRequest{}, err
	}
	return repositoryExtractionWorkerTickRequest{
		input:              input,
		claimRequestID:     claimRequestID,
		executionRequestID: executionRequestID,
		requestPayloadHash: payloadHash,
	}, nil
}

func repositoryExtractionWorkerTickChildRequestID(prefix, kind, requestID string) (string, error) {
	return stableID(prefix, kind, struct {
		TickRequestID string `json:"tick_request_id"`
	}{TickRequestID: requestID})
}

func repositoryExtractionWorkerTickPayloadHash(input RepositoryExtractionWorkerTickInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkspaceRoot             string `json:"workspace_root"`
		RepoID                    string `json:"repo_id"`
		ExtractorName             string `json:"extractor_name"`
		WorkerID                  string `json:"worker_id"`
		LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
	}{
		WorkspaceRoot:             input.WorkspaceRoot,
		RepoID:                    input.RepoID,
		ExtractorName:             input.ExtractorName,
		WorkerID:                  input.WorkerID,
		LeaseDurationMilliseconds: input.LeaseDurationMilliseconds,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func reserveRepositoryExtractionWorkerTick(ctx context.Context, db sqlDB, request repositoryExtractionWorkerTickRequest, claim RepositoryExtractionWorkClaimResult) (bool, error) {
	if err := validateRepositoryExtractionWorkerTickClaim(request, claim); err != nil {
		return false, err
	}
	var replayed bool
	err := withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-worker-tick", request.input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkerTickRequest(ctx, tx, request.input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != request.requestPayloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction worker tick payload", request.input.RequestID)
			}
			if persisted.input.RepoID != request.input.RepoID ||
				persisted.input.ExtractorName != request.input.ExtractorName ||
				persisted.input.WorkerID != request.input.WorkerID ||
				persisted.input.LeaseDurationMilliseconds != request.input.LeaseDurationMilliseconds ||
				persisted.claimRequestID != request.claimRequestID ||
				persisted.executionRequestID != request.executionRequestID ||
				!repositoryExtractionWorkerTickClaimIdentityMatches(persisted, claim) {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction worker tick request %s has inconsistent durable identity", request.input.RequestID)
			}
			replayed = true
			return nil
		}
		return persistRepositoryExtractionWorkerTickRequest(ctx, tx, request, claim)
	})
	if err != nil {
		return false, err
	}
	return replayed, nil
}

func validateRepositoryExtractionWorkerTickClaim(request repositoryExtractionWorkerTickRequest, claim RepositoryExtractionWorkClaimResult) error {
	if claim.RequestID != request.claimRequestID ||
		claim.RepoID != request.input.RepoID ||
		claim.ExtractorName != request.input.ExtractorName ||
		claim.WorkerID != request.input.WorkerID {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction worker tick request %s received an inconsistent claim decision", request.input.RequestID)
	}
	if !claim.Claimed {
		if claim.Work != nil || claim.ClaimID != "" || claim.AttemptNumber != 0 || claim.ClaimedAt != nil || claim.LeaseExpiresAt != nil {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction worker tick request %s received an incomplete no-work decision", request.input.RequestID)
		}
		return nil
	}
	if claim.Work == nil ||
		claim.Work.RepoID != request.input.RepoID ||
		claim.Work.ExtractorName != request.input.ExtractorName ||
		!strings.HasPrefix(claim.Work.WorkItemID, "repo-work:") ||
		!strings.HasPrefix(claim.ClaimID, "work-claim:") ||
		claim.AttemptNumber < 1 ||
		claim.ClaimedAt == nil ||
		claim.LeaseExpiresAt == nil ||
		!claim.LeaseExpiresAt.After(*claim.ClaimedAt) {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction worker tick request %s received an incomplete claimed-work decision", request.input.RequestID)
	}
	return nil
}

func repositoryExtractionWorkerTickClaimIdentityMatches(persisted persistedRepositoryExtractionWorkerTickRequest, claim RepositoryExtractionWorkClaimResult) bool {
	if persisted.claimed != claim.Claimed {
		return false
	}
	if !claim.Claimed {
		return persisted.workItemID == "" && persisted.claimID == "" && persisted.attemptNumber == 0
	}
	return claim.Work != nil &&
		persisted.workItemID == claim.Work.WorkItemID &&
		persisted.claimID == claim.ClaimID &&
		persisted.attemptNumber == claim.AttemptNumber
}

func persistRepositoryExtractionWorkerTickRequest(ctx context.Context, tx sqlTx, request repositoryExtractionWorkerTickRequest, claim RepositoryExtractionWorkClaimResult) error {
	var workItemID, claimID, attemptNumber any
	if claim.Claimed {
		workItemID = claim.Work.WorkItemID
		claimID = claim.ClaimID
		attemptNumber = claim.AttemptNumber
	}
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_worker_tick_requests (
			request_id,
			repo_id,
			extractor_name,
			worker_id,
			lease_duration_milliseconds,
			claim_request_id,
			execution_request_id,
			claimed,
			work_item_id,
			claim_id,
			attempt_number,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, request.input.RequestID, request.input.RepoID, request.input.ExtractorName, request.input.WorkerID, request.input.LeaseDurationMilliseconds, request.claimRequestID, request.executionRequestID, claim.Claimed, workItemID, claimID, attemptNumber, request.requestPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction worker tick request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkerTickRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkerTickRequest, bool, error) {
	var request persistedRepositoryExtractionWorkerTickRequest
	var workItemID, claimID *string
	var attemptNumber *int
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			repo_id,
			extractor_name,
			worker_id,
			lease_duration_milliseconds,
			claim_request_id,
			execution_request_id,
			claimed,
			work_item_id,
			claim_id,
			attempt_number,
			request_payload_hash
		FROM repository_extraction_worker_tick_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&request.input.RequestID,
		&request.input.RepoID,
		&request.input.ExtractorName,
		&request.input.WorkerID,
		&request.input.LeaseDurationMilliseconds,
		&request.claimRequestID,
		&request.executionRequestID,
		&request.claimed,
		&workItemID,
		&claimID,
		&attemptNumber,
		&request.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkerTickRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkerTickRequest{}, false, fmt.Errorf("reading repository extraction worker tick request: %w", err)
	}
	if workItemID != nil {
		request.workItemID = *workItemID
	}
	if claimID != nil {
		request.claimID = *claimID
	}
	if attemptNumber != nil {
		request.attemptNumber = *attemptNumber
	}
	return request, true, nil
}
