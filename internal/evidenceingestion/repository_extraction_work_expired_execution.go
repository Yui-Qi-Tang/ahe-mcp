package evidenceingestion

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositoryExtractionWorkExpiredExecutionMaxLimit bounds one cross-stream discovery query.
	RepositoryExtractionWorkExpiredExecutionMaxLimit = 100
)

// RepositoryExtractionWorkExpiredExecutionListInput bounds one cross-stream discovery query.
type RepositoryExtractionWorkExpiredExecutionListInput struct {
	Limit int `json:"limit"`
}

// RepositoryExtractionWorkExpiredExecution is one execution-owned running claim whose lease elapsed.
type RepositoryExtractionWorkExpiredExecution struct {
	ExecutionRequestID                 string                   `json:"execution_request_id"`
	Work                               RepositoryExtractionWork `json:"work"`
	ClaimID                            string                   `json:"claim_id"`
	AttemptNumber                      int                      `json:"attempt_number"`
	WorkerID                           string                   `json:"worker_id"`
	ClaimedAt                          time.Time                `json:"claimed_at"`
	LeaseExpiresAt                     time.Time                `json:"lease_expires_at"`
	HeartbeatLeaseDurationMilliseconds int64                    `json:"heartbeat_lease_duration_milliseconds"`
	HeartbeatCount                     int                      `json:"heartbeat_count"`
}

// ListExpiredRepositoryExtractionWorkExecutions returns bounded repair candidates without changing work state.
func ListExpiredRepositoryExtractionWorkExecutions(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkExpiredExecutionListInput) ([]RepositoryExtractionWorkExpiredExecution, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listExpiredRepositoryExtractionWorkExecutions(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func listExpiredRepositoryExtractionWorkExecutions(ctx context.Context, db sqlDB, input RepositoryExtractionWorkExpiredExecutionListInput, asOf time.Time) ([]RepositoryExtractionWorkExpiredExecution, error) {
	if err := validateRepositoryExtractionWorkExpiredExecutionListInput(input); err != nil {
		return nil, err
	}
	if asOf.IsZero() {
		return nil, newDomainError(ErrorInvalidInput, "expired repository extraction work execution discovery time is required")
	}
	asOf = asOf.UTC()

	rows, err := db.query(ctx, `
		SELECT
			e.request_id,
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
			a.lease_expires_at,
			e.heartbeat_lease_duration_milliseconds,
			e.heartbeat_count
		FROM repository_extraction_work_claim_attempts a
		JOIN repository_extraction_work_execution_requests e
		  ON e.work_item_id = a.work_item_id
		 AND e.claim_id = a.claim_id
		 AND e.worker_id = a.worker_id
		JOIN repository_extraction_work_items w
		  ON w.work_item_id = a.work_item_id
		 AND w.claim_id = a.claim_id
		 AND w.claimed_by = a.worker_id
		 AND w.claimed_at = a.claimed_at
		JOIN repository_change_observations o
		  ON o.change_observation_id = w.change_observation_id
		WHERE a.status = 'running'
		  AND a.finished_at IS NULL
		  AND a.expired_at IS NULL
		  AND a.lease_expires_at <= $1
		  AND w.status = 'running'
		  AND w.finished_at IS NULL
		  AND w.source_generation_id IS NULL
		  AND w.failure_class IS NULL
		  AND w.failure_message IS NULL
		ORDER BY
			a.lease_expires_at,
			w.repo_id,
			w.extractor_name,
			w.work_item_id,
			a.claim_id,
			e.request_id
		LIMIT $2
	`, asOf, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing expired repository extraction work executions: %w", err)
	}
	defer rows.Close()

	executions := make([]RepositoryExtractionWorkExpiredExecution, 0, input.Limit)
	for rows.Next() {
		var execution RepositoryExtractionWorkExpiredExecution
		if err := rows.Scan(
			&execution.ExecutionRequestID,
			&execution.Work.WorkItemID,
			&execution.Work.RepoID,
			&execution.Work.ExtractorName,
			&execution.Work.ObservationID,
			&execution.Work.ObservationNumber,
			&execution.Work.ChangeToken,
			&execution.Work.HeadCommitSHA,
			&execution.ClaimID,
			&execution.AttemptNumber,
			&execution.WorkerID,
			&execution.ClaimedAt,
			&execution.LeaseExpiresAt,
			&execution.HeartbeatLeaseDurationMilliseconds,
			&execution.HeartbeatCount,
		); err != nil {
			return nil, fmt.Errorf("scanning expired repository extraction work execution: %w", err)
		}
		execution.ClaimedAt = execution.ClaimedAt.UTC()
		execution.LeaseExpiresAt = execution.LeaseExpiresAt.UTC()
		executions = append(executions, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating expired repository extraction work executions: %w", err)
	}
	return executions, nil
}

func validateRepositoryExtractionWorkExpiredExecutionListInput(input RepositoryExtractionWorkExpiredExecutionListInput) error {
	if input.Limit < 1 || input.Limit > RepositoryExtractionWorkExpiredExecutionMaxLimit {
		return newDomainError(
			ErrorInvalidInput,
			"expired repository extraction work execution limit must be between 1 and %d",
			RepositoryExtractionWorkExpiredExecutionMaxLimit,
		)
	}
	return nil
}
