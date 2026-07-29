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

// RepositoryExtractionWorkLeaseRenewalInput identifies one running claim to extend.
type RepositoryExtractionWorkLeaseRenewalInput struct {
	RequestID                 string `json:"request_id"`
	WorkItemID                string `json:"work_item_id"`
	ClaimID                   string `json:"claim_id"`
	WorkerID                  string `json:"worker_id"`
	LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
}

// RepositoryExtractionWorkLeaseRenewalResult reports one durable lease extension.
type RepositoryExtractionWorkLeaseRenewalResult struct {
	RequestID                 string                   `json:"request_id"`
	Work                      RepositoryExtractionWork `json:"work"`
	ClaimID                   string                   `json:"claim_id"`
	AttemptNumber             int                      `json:"attempt_number"`
	WorkerID                  string                   `json:"worker_id"`
	LeaseDurationMilliseconds int64                    `json:"lease_duration_milliseconds"`
	PriorLeaseExpiresAt       time.Time                `json:"prior_lease_expires_at"`
	RenewedAt                 time.Time                `json:"renewed_at"`
	LeaseExpiresAt            time.Time                `json:"lease_expires_at"`
	Replayed                  bool                     `json:"replayed"`
}

type persistedRepositoryExtractionWorkLeaseRenewalRequest struct {
	result             RepositoryExtractionWorkLeaseRenewalResult
	requestPayloadHash string
}

// RenewRepositoryExtractionWorkLease extends one exact running claim before expiry.
func RenewRepositoryExtractionWorkLease(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkLeaseRenewalInput) (RepositoryExtractionWorkLeaseRenewalResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func renewRepositoryExtractionWorkLease(ctx context.Context, db sqlDB, input RepositoryExtractionWorkLeaseRenewalInput, renewedAt time.Time) (RepositoryExtractionWorkLeaseRenewalResult, error) {
	input, leaseDuration, err := validateRepositoryExtractionWorkLeaseRenewalInput(input)
	if err != nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, err
	}
	if renewedAt.IsZero() {
		return RepositoryExtractionWorkLeaseRenewalResult{}, newDomainError(ErrorInvalidInput, "renewed_at is required")
	}
	renewedAt = renewedAt.UTC()
	leaseExpiresAt := renewedAt.Add(leaseDuration)
	payloadHash, err := repositoryExtractionWorkLeaseRenewalPayloadHash(input)
	if err != nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, err
	}

	var result RepositoryExtractionWorkLeaseRenewalResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-renew-lease", input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkLeaseRenewalRequest(ctx, tx, input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != payloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work lease renewal payload", input.RequestID)
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
		if work.status != repositoryExtractionWorkRunning ||
			work.claimID != input.ClaimID ||
			work.claimedBy != input.WorkerID ||
			work.claimedAt == nil ||
			work.finishedAt != nil ||
			work.sourceGenerationID != "" ||
			work.failureClass != "" ||
			work.failureMessage != "" {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not running claim %s for worker %s", input.WorkItemID, input.ClaimID, input.WorkerID)
		}

		attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, input.ClaimID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", input.ClaimID)
		}
		if err != nil {
			return err
		}
		if attempt.workItemID != input.WorkItemID ||
			attempt.workerID != input.WorkerID ||
			attempt.status != repositoryExtractionWorkRunning ||
			attempt.finishedAt != nil ||
			attempt.expiredAt != nil ||
			attempt.sourceGenerationID != "" ||
			attempt.failureClass != "" ||
			attempt.failureMessage != "" ||
			!attempt.claimedAt.Equal(work.claimedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s does not match running work", input.ClaimID)
		}
		if !renewedAt.Before(attempt.leaseExpiresAt) {
			return newDomainError(ErrorRepositoryWorkLeaseExpired, "repository extraction work claim %s expired at %s", input.ClaimID, attempt.leaseExpiresAt.UTC().Format(time.RFC3339Nano))
		}
		if !leaseExpiresAt.After(attempt.leaseExpiresAt) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim %s renewal does not extend its lease", input.ClaimID)
		}

		priorLeaseExpiresAt := attempt.leaseExpiresAt.UTC()
		tag, err := tx.exec(ctx, `
			UPDATE repository_extraction_work_claim_attempts
			SET lease_expires_at = $5
			WHERE claim_id = $1
				AND work_item_id = $2
				AND worker_id = $3
				AND status = $4
				AND lease_expires_at = $6
		`, input.ClaimID, input.WorkItemID, input.WorkerID, repositoryExtractionWorkRunning, leaseExpiresAt, priorLeaseExpiresAt)
		if err != nil {
			return fmt.Errorf("renewing repository extraction work claim lease: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim %s changed during lease renewal", input.ClaimID)
		}

		result = RepositoryExtractionWorkLeaseRenewalResult{
			RequestID:                 input.RequestID,
			Work:                      work.work,
			ClaimID:                   input.ClaimID,
			AttemptNumber:             attempt.attemptNumber,
			WorkerID:                  input.WorkerID,
			LeaseDurationMilliseconds: input.LeaseDurationMilliseconds,
			PriorLeaseExpiresAt:       priorLeaseExpiresAt,
			RenewedAt:                 renewedAt,
			LeaseExpiresAt:            leaseExpiresAt,
		}
		return persistRepositoryExtractionWorkLeaseRenewalRequest(ctx, tx, result, payloadHash)
	})
	if err != nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, err
	}
	return result, nil
}

func validateRepositoryExtractionWorkLeaseRenewalInput(input RepositoryExtractionWorkLeaseRenewalInput) (RepositoryExtractionWorkLeaseRenewalInput, time.Duration, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.RequestID == "" {
		return RepositoryExtractionWorkLeaseRenewalInput{}, 0, newDomainError(ErrorInvalidInput, "repository extraction work lease renewal request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkLeaseRenewalInput{}, 0, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkLeaseRenewalInput{}, 0, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.WorkerID == "" || len(input.WorkerID) > 200 {
		return RepositoryExtractionWorkLeaseRenewalInput{}, 0, newDomainError(ErrorInvalidInput, "repository extraction work worker_id must contain 1 to 200 bytes")
	}
	minLeaseMilliseconds := RepositoryExtractionWorkMinLeaseDuration.Milliseconds()
	maxLeaseMilliseconds := RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()
	if input.LeaseDurationMilliseconds < minLeaseMilliseconds || input.LeaseDurationMilliseconds > maxLeaseMilliseconds {
		return RepositoryExtractionWorkLeaseRenewalInput{}, 0, newDomainError(
			ErrorInvalidInput,
			"repository extraction work lease renewal duration must be between %d and %d milliseconds",
			minLeaseMilliseconds,
			maxLeaseMilliseconds,
		)
	}
	return input, time.Duration(input.LeaseDurationMilliseconds) * time.Millisecond, nil
}

func repositoryExtractionWorkLeaseRenewalPayloadHash(input RepositoryExtractionWorkLeaseRenewalInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkItemID                string `json:"work_item_id"`
		ClaimID                   string `json:"claim_id"`
		WorkerID                  string `json:"worker_id"`
		LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
	}{
		WorkItemID:                input.WorkItemID,
		ClaimID:                   input.ClaimID,
		WorkerID:                  input.WorkerID,
		LeaseDurationMilliseconds: input.LeaseDurationMilliseconds,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func persistRepositoryExtractionWorkLeaseRenewalRequest(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkLeaseRenewalResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_lease_renewal_requests (
			request_id,
			work_item_id,
			claim_id,
			worker_id,
			lease_duration_milliseconds,
			prior_lease_expires_at,
			renewed_at,
			lease_expires_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, result.RequestID, result.Work.WorkItemID, result.ClaimID, result.WorkerID, result.LeaseDurationMilliseconds, result.PriorLeaseExpiresAt, result.RenewedAt, result.LeaseExpiresAt, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work lease renewal request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkLeaseRenewalRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkLeaseRenewalRequest, bool, error) {
	var persisted persistedRepositoryExtractionWorkLeaseRenewalRequest
	var workItemID string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			work_item_id,
			claim_id,
			worker_id,
			lease_duration_milliseconds,
			prior_lease_expires_at,
			renewed_at,
			lease_expires_at,
			request_payload_hash
		FROM repository_extraction_work_lease_renewal_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.WorkerID,
		&persisted.result.LeaseDurationMilliseconds,
		&persisted.result.PriorLeaseExpiresAt,
		&persisted.result.RenewedAt,
		&persisted.result.LeaseExpiresAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkLeaseRenewalRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkLeaseRenewalRequest{}, false, fmt.Errorf("reading repository extraction work lease renewal request: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if err != nil {
		return persistedRepositoryExtractionWorkLeaseRenewalRequest{}, false, err
	}
	attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, persisted.result.ClaimID)
	if err != nil {
		return persistedRepositoryExtractionWorkLeaseRenewalRequest{}, false, err
	}
	persisted.result.PriorLeaseExpiresAt = persisted.result.PriorLeaseExpiresAt.UTC()
	persisted.result.RenewedAt = persisted.result.RenewedAt.UTC()
	persisted.result.LeaseExpiresAt = persisted.result.LeaseExpiresAt.UTC()
	wantLeaseExpiresAt := persisted.result.RenewedAt.Add(time.Duration(persisted.result.LeaseDurationMilliseconds) * time.Millisecond)
	if attempt.workItemID != workItemID ||
		attempt.workerID != persisted.result.WorkerID ||
		attempt.leaseExpiresAt.Before(persisted.result.LeaseExpiresAt) ||
		!persisted.result.RenewedAt.Before(persisted.result.PriorLeaseExpiresAt) ||
		!persisted.result.LeaseExpiresAt.After(persisted.result.PriorLeaseExpiresAt) ||
		!persisted.result.LeaseExpiresAt.Equal(wantLeaseExpiresAt) {
		return persistedRepositoryExtractionWorkLeaseRenewalRequest{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work lease renewal request %s does not match claim attempt", requestID)
	}
	persisted.result.Work = work.work
	persisted.result.AttemptNumber = attempt.attemptNumber
	return persisted, true, nil
}
