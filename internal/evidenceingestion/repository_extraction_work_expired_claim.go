package evidenceingestion

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositoryExtractionWorkExpiredClaimMaxLimit bounds one discovery query.
	RepositoryExtractionWorkExpiredClaimMaxLimit = 100
)

// RepositoryExtractionWorkExpiredClaimListInput selects one extractor stream to inspect.
type RepositoryExtractionWorkExpiredClaimListInput struct {
	RepoID        string `json:"repo_id"`
	ExtractorName string `json:"extractor_name"`
	Limit         int    `json:"limit"`
}

// RepositoryExtractionWorkExpiredClaim is one still-running claim whose current lease has elapsed.
type RepositoryExtractionWorkExpiredClaim struct {
	Work           RepositoryExtractionWork `json:"work"`
	ClaimID        string                   `json:"claim_id"`
	AttemptNumber  int                      `json:"attempt_number"`
	WorkerID       string                   `json:"worker_id"`
	ClaimedAt      time.Time                `json:"claimed_at"`
	LeaseExpiresAt time.Time                `json:"lease_expires_at"`
}

// ListExpiredRepositoryExtractionWorkClaims returns bounded recovery candidates without changing work state.
func ListExpiredRepositoryExtractionWorkClaims(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkExpiredClaimListInput) ([]RepositoryExtractionWorkExpiredClaim, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func listExpiredRepositoryExtractionWorkClaims(ctx context.Context, db sqlDB, input RepositoryExtractionWorkExpiredClaimListInput, asOf time.Time) ([]RepositoryExtractionWorkExpiredClaim, error) {
	input, err := validateRepositoryExtractionWorkExpiredClaimListInput(input)
	if err != nil {
		return nil, err
	}
	if asOf.IsZero() {
		return nil, newDomainError(ErrorInvalidInput, "expired repository extraction work claim discovery time is required")
	}
	asOf = asOf.UTC()

	rows, err := db.query(ctx, `
		SELECT
			w.work_item_id,
			w.repo_id,
			w.extractor_name,
			w.change_observation_id,
			o.observation_number,
			o.change_token,
			o.head_commit_sha,
			a.claim_id,
			a.attempt_number,
			a.worker_id,
			a.claimed_at,
			a.lease_expires_at
		FROM repository_extraction_work_items w
		JOIN repository_change_observations o
		  ON o.change_observation_id = w.change_observation_id
		JOIN repository_extraction_work_claim_attempts a
		  ON a.work_item_id = w.work_item_id
		 AND a.claim_id = w.claim_id
		 AND a.worker_id = w.claimed_by
		 AND a.claimed_at = w.claimed_at
		WHERE w.repo_id = $1
		  AND w.extractor_name = $2
		  AND w.status = $3
		  AND w.finished_at IS NULL
		  AND a.status = $3
		  AND a.finished_at IS NULL
		  AND a.expired_at IS NULL
		  AND a.lease_expires_at <= $4
		ORDER BY a.lease_expires_at, w.work_item_id, a.claim_id
		LIMIT $5
	`, input.RepoID, input.ExtractorName, repositoryExtractionWorkRunning, asOf, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing expired repository extraction work claims: %w", err)
	}
	defer rows.Close()

	claims := make([]RepositoryExtractionWorkExpiredClaim, 0, input.Limit)
	for rows.Next() {
		var claim RepositoryExtractionWorkExpiredClaim
		if err := rows.Scan(
			&claim.Work.WorkItemID,
			&claim.Work.RepoID,
			&claim.Work.ExtractorName,
			&claim.Work.ObservationID,
			&claim.Work.ObservationNumber,
			&claim.Work.ChangeToken,
			&claim.Work.HeadCommitSHA,
			&claim.ClaimID,
			&claim.AttemptNumber,
			&claim.WorkerID,
			&claim.ClaimedAt,
			&claim.LeaseExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scanning expired repository extraction work claim: %w", err)
		}
		claim.ClaimedAt = claim.ClaimedAt.UTC()
		claim.LeaseExpiresAt = claim.LeaseExpiresAt.UTC()
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating expired repository extraction work claims: %w", err)
	}
	return claims, nil
}

func validateRepositoryExtractionWorkExpiredClaimListInput(input RepositoryExtractionWorkExpiredClaimListInput) (RepositoryExtractionWorkExpiredClaimListInput, error) {
	input.RepoID = strings.TrimSpace(input.RepoID)
	if input.RepoID == "" {
		return RepositoryExtractionWorkExpiredClaimListInput{}, newDomainError(ErrorInvalidInput, "expired repository extraction work claim repo_id is required")
	}
	extractorName, err := validateRepositoryExtractionWorkExtractor(input.ExtractorName)
	if err != nil {
		return RepositoryExtractionWorkExpiredClaimListInput{}, err
	}
	input.ExtractorName = extractorName
	if input.Limit < 1 || input.Limit > RepositoryExtractionWorkExpiredClaimMaxLimit {
		return RepositoryExtractionWorkExpiredClaimListInput{}, newDomainError(
			ErrorInvalidInput,
			"expired repository extraction work claim limit must be between 1 and %d",
			RepositoryExtractionWorkExpiredClaimMaxLimit,
		)
	}
	return input, nil
}
