package evidenceingestion

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositoryExtractionWorkDueRetryDecisionMaxLimit bounds one cross-stream discovery query.
	RepositoryExtractionWorkDueRetryDecisionMaxLimit = 100
)

// RepositoryExtractionWorkDueRetryDecisionListInput bounds one cross-stream discovery query.
type RepositoryExtractionWorkDueRetryDecisionListInput struct {
	Limit int `json:"limit"`
}

// RepositoryExtractionWorkDueRetryDecision is one unconsumed retry policy whose deadline elapsed.
type RepositoryExtractionWorkDueRetryDecision struct {
	FailurePolicyRequestID string                   `json:"failure_policy_request_id"`
	Work                   RepositoryExtractionWork `json:"work"`
	ClaimID                string                   `json:"claim_id"`
	AttemptNumber          int                      `json:"attempt_number"`
	WorkerID               string                   `json:"worker_id"`
	FinishedAt             time.Time                `json:"finished_at"`
	FailureClass           string                   `json:"failure_class"`
	FailureMessage         string                   `json:"failure_message"`
	PolicyVersion          string                   `json:"policy_version"`
	Decision               string                   `json:"decision"`
	DecisionReason         string                   `json:"decision_reason"`
	MaxAttempts            int                      `json:"max_attempts"`
	BackoffMilliseconds    int64                    `json:"backoff_milliseconds"`
	RetryNotBefore         time.Time                `json:"retry_not_before"`
	DecidedAt              time.Time                `json:"decided_at"`
	DecisionActorID        string                   `json:"decision_actor_id"`
}

type repositoryExtractionWorkDueRetryDecisionQuerier interface {
	query(ctx context.Context, sql string, args ...any) (sqlRows, error)
}

// ListDueRepositoryExtractionWorkRetryDecisions returns bounded consumption candidates without changing work state.
func ListDueRepositoryExtractionWorkRetryDecisions(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkDueRetryDecisionListInput) ([]RepositoryExtractionWorkDueRetryDecision, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listDueRepositoryExtractionWorkRetryDecisions(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func listDueRepositoryExtractionWorkRetryDecisions(ctx context.Context, db sqlDB, input RepositoryExtractionWorkDueRetryDecisionListInput, asOf time.Time) ([]RepositoryExtractionWorkDueRetryDecision, error) {
	return queryDueRepositoryExtractionWorkRetryDecisions(ctx, db, input, asOf, false)
}

func lockDueRepositoryExtractionWorkRetryDecisions(ctx context.Context, tx sqlTx, input RepositoryExtractionWorkDueRetryDecisionListInput, asOf time.Time) ([]RepositoryExtractionWorkDueRetryDecision, error) {
	return queryDueRepositoryExtractionWorkRetryDecisions(ctx, tx, input, asOf, true)
}

func queryDueRepositoryExtractionWorkRetryDecisions(ctx context.Context, db repositoryExtractionWorkDueRetryDecisionQuerier, input RepositoryExtractionWorkDueRetryDecisionListInput, asOf time.Time, lockWork bool) ([]RepositoryExtractionWorkDueRetryDecision, error) {
	if err := validateRepositoryExtractionWorkDueRetryDecisionListInput(input); err != nil {
		return nil, err
	}
	if asOf.IsZero() {
		return nil, newDomainError(ErrorInvalidInput, "repository extraction work due retry decision discovery time is required")
	}
	asOf = asOf.UTC()

	query := `
		SELECT
			p.request_id,
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
			a.finished_at,
			a.failure_class,
			a.failure_message,
			p.attempt_number,
			p.failure_class,
			p.policy_version,
			p.decision,
			p.decision_reason,
			p.max_attempts,
			p.backoff_milliseconds,
			p.retry_not_before,
			p.decided_at,
			p.decision_actor_id
		FROM repository_extraction_work_failure_policy_decisions p
		JOIN repository_extraction_work_claim_attempts a
		  ON a.work_item_id = p.work_item_id
		 AND a.claim_id = p.claim_id
		JOIN repository_extraction_work_items w
		  ON w.work_item_id = a.work_item_id
		 AND w.claim_id = a.claim_id
		 AND w.claimed_by = a.worker_id
		 AND w.claimed_at = a.claimed_at
		 AND w.finished_at = a.finished_at
		 AND w.failure_class = a.failure_class
		 AND w.failure_message = a.failure_message
		JOIN repository_source_streams s
		  ON s.repo_id = w.repo_id
		 AND s.extractor_name = w.extractor_name
		JOIN repository_change_observations o
		  ON o.change_observation_id = w.change_observation_id
		LEFT JOIN repository_extraction_work_retry_decision_consumptions c
		  ON c.failure_policy_request_id = p.request_id
		WHERE p.decision = 'retry'
		  AND p.retry_not_before <= $1
		  AND c.failure_policy_request_id IS NULL
		  AND a.status = 'failed'
		  AND a.finished_at IS NOT NULL
		  AND a.expired_at IS NULL
		  AND w.status = 'failed'
		  AND w.source_generation_id IS NULL
		ORDER BY
			p.retry_not_before,
			w.repo_id,
			w.extractor_name,
			w.work_item_id,
			a.claim_id,
			p.request_id
		LIMIT $2
	`
	if lockWork {
		query += "\t\tFOR UPDATE OF s SKIP LOCKED\n"
	}
	rows, err := db.query(ctx, query, asOf, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing due repository extraction work retry decisions: %w", err)
	}
	defer rows.Close()

	decisions := make([]RepositoryExtractionWorkDueRetryDecision, 0, input.Limit)
	for rows.Next() {
		var decision RepositoryExtractionWorkDueRetryDecision
		var policyAttemptNumber int
		var policyFailureClass string
		if err := rows.Scan(
			&decision.FailurePolicyRequestID,
			&decision.Work.WorkItemID,
			&decision.Work.RepoID,
			&decision.Work.ExtractorName,
			&decision.Work.ObservationID,
			&decision.Work.ObservationNumber,
			&decision.Work.ChangeToken,
			&decision.Work.HeadCommitSHA,
			&decision.ClaimID,
			&decision.AttemptNumber,
			&decision.WorkerID,
			&decision.FinishedAt,
			&decision.FailureClass,
			&decision.FailureMessage,
			&policyAttemptNumber,
			&policyFailureClass,
			&decision.PolicyVersion,
			&decision.Decision,
			&decision.DecisionReason,
			&decision.MaxAttempts,
			&decision.BackoffMilliseconds,
			&decision.RetryNotBefore,
			&decision.DecidedAt,
			&decision.DecisionActorID,
		); err != nil {
			return nil, fmt.Errorf("scanning due repository extraction work retry decision: %w", err)
		}
		decision.FinishedAt = decision.FinishedAt.UTC()
		decision.RetryNotBefore = decision.RetryNotBefore.UTC()
		decision.DecidedAt = decision.DecidedAt.UTC()
		if err := validatePersistedRepositoryExtractionWorkDueRetryDecision(decision, policyAttemptNumber, policyFailureClass); err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating due repository extraction work retry decisions: %w", err)
	}
	return decisions, nil
}

func validateRepositoryExtractionWorkDueRetryDecisionListInput(input RepositoryExtractionWorkDueRetryDecisionListInput) error {
	if input.Limit < 1 || input.Limit > RepositoryExtractionWorkDueRetryDecisionMaxLimit {
		return newDomainError(
			ErrorInvalidInput,
			"repository extraction work due retry decision limit must be between 1 and %d",
			RepositoryExtractionWorkDueRetryDecisionMaxLimit,
		)
	}
	return nil
}

func validatePersistedRepositoryExtractionWorkDueRetryDecision(decision RepositoryExtractionWorkDueRetryDecision, policyAttemptNumber int, policyFailureClass string) error {
	expected := decideRepositoryExtractionWorkFailurePolicy(decision.FailureClass, decision.AttemptNumber, decision.DecidedAt)
	if policyAttemptNumber != decision.AttemptNumber ||
		policyFailureClass != decision.FailureClass ||
		decision.PolicyVersion != RepositoryExtractionWorkFailurePolicyV1 ||
		expected.decision != RepositoryExtractionWorkFailureDecisionRetry ||
		decision.Decision != expected.decision ||
		decision.DecisionReason != expected.reason ||
		decision.MaxAttempts != RepositoryExtractionWorkFailurePolicyMaxAttempts ||
		decision.BackoffMilliseconds != expected.backoffMilliseconds ||
		expected.retryNotBefore == nil ||
		!decision.RetryNotBefore.Equal(*expected.retryNotBefore) ||
		decision.DecidedAt.Before(decision.FinishedAt) {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s has inconsistent durable retry decision", decision.FailurePolicyRequestID)
	}
	return nil
}
