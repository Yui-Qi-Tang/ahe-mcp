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
	// RepositoryExtractionWorkFailurePolicyV1 is the immutable initial retry policy contract.
	RepositoryExtractionWorkFailurePolicyV1 = "repository-retry-policy-v1"

	// RepositoryExtractionWorkFailureDecisionRetry marks a claim retry-eligible after a bounded delay.
	RepositoryExtractionWorkFailureDecisionRetry = "retry"
	// RepositoryExtractionWorkFailureDecisionStop rejects policy retry for a deterministic or authority failure.
	RepositoryExtractionWorkFailureDecisionStop = "stop"
	// RepositoryExtractionWorkFailureDecisionManualReview requires an operator to classify or repair the failure.
	RepositoryExtractionWorkFailureDecisionManualReview = "manual_review"
	// RepositoryExtractionWorkFailureDecisionExhausted means the retryable failure reached the attempt limit.
	RepositoryExtractionWorkFailureDecisionExhausted = "exhausted"

	// RepositoryExtractionWorkFailurePolicyMaxAttempts bounds retry eligibility under policy v1.
	RepositoryExtractionWorkFailurePolicyMaxAttempts = 3

	repositoryExtractionWorkFailurePolicyBaseBackoff = 5 * time.Second
)

const (
	// RepositoryExtractionWorkFailureReasonRetryable identifies a retryable runner invocation failure.
	RepositoryExtractionWorkFailureReasonRetryable = "retryable_runner_failure"
	// RepositoryExtractionWorkFailureReasonLimitReached identifies exhausted retry eligibility.
	RepositoryExtractionWorkFailureReasonLimitReached = "retry_limit_reached"
	// RepositoryExtractionWorkFailureReasonDeterministic identifies a deterministic or authority failure.
	RepositoryExtractionWorkFailureReasonDeterministic = "deterministic_or_authority_failure"
	// RepositoryExtractionWorkFailureReasonSeparateRepair identifies a distinct recovery or inspection path.
	RepositoryExtractionWorkFailureReasonSeparateRepair = "separate_recovery_or_cause_inspection_required"
	// RepositoryExtractionWorkFailureReasonUnclassified identifies an unknown failure class.
	RepositoryExtractionWorkFailureReasonUnclassified = "unclassified_failure"
)

// RepositoryExtractionWorkFailurePolicyInput identifies one terminal failed claim for classification.
type RepositoryExtractionWorkFailurePolicyInput struct {
	RequestID       string `json:"request_id"`
	WorkItemID      string `json:"work_item_id"`
	ClaimID         string `json:"claim_id"`
	DecisionActorID string `json:"decision_actor_id"`
}

// RepositoryExtractionWorkFailurePolicyResult reports one immutable policy decision.
type RepositoryExtractionWorkFailurePolicyResult struct {
	RequestID           string                   `json:"request_id"`
	Work                RepositoryExtractionWork `json:"work"`
	ClaimID             string                   `json:"claim_id"`
	AttemptNumber       int                      `json:"attempt_number"`
	WorkerID            string                   `json:"worker_id"`
	FinishedAt          time.Time                `json:"finished_at"`
	FailureClass        string                   `json:"failure_class"`
	FailureMessage      string                   `json:"failure_message"`
	PolicyVersion       string                   `json:"policy_version"`
	Decision            string                   `json:"decision"`
	DecisionReason      string                   `json:"decision_reason"`
	MaxAttempts         int                      `json:"max_attempts"`
	BackoffMilliseconds int64                    `json:"backoff_milliseconds"`
	RetryNotBefore      *time.Time               `json:"retry_not_before,omitempty"`
	DecidedAt           time.Time                `json:"decided_at"`
	DecisionActorID     string                   `json:"decision_actor_id"`
	Replayed            bool                     `json:"replayed"`
}

type repositoryExtractionWorkFailurePolicyDecision struct {
	decision            string
	reason              string
	backoffMilliseconds int64
	retryNotBefore      *time.Time
}

type persistedRepositoryExtractionWorkFailurePolicyDecision struct {
	result             RepositoryExtractionWorkFailurePolicyResult
	requestPayloadHash string
}

// ClassifyFailedRepositoryExtractionWork records policy v1 for one exact terminal failed claim.
func ClassifyFailedRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkFailurePolicyInput) (RepositoryExtractionWorkFailurePolicyResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkFailurePolicyResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func classifyFailedRepositoryExtractionWork(ctx context.Context, db sqlDB, input RepositoryExtractionWorkFailurePolicyInput, decidedAt time.Time) (RepositoryExtractionWorkFailurePolicyResult, error) {
	input, err := validateRepositoryExtractionWorkFailurePolicyInput(input)
	if err != nil {
		return RepositoryExtractionWorkFailurePolicyResult{}, err
	}
	if decidedAt.IsZero() {
		return RepositoryExtractionWorkFailurePolicyResult{}, newDomainError(ErrorInvalidInput, "decided_at is required")
	}
	decidedAt = decidedAt.UTC()
	payloadHash, err := repositoryExtractionWorkFailurePolicyPayloadHash(input)
	if err != nil {
		return RepositoryExtractionWorkFailurePolicyResult{}, err
	}

	var result RepositoryExtractionWorkFailurePolicyResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-failure-policy", input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkFailurePolicyDecision(ctx, tx, input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != payloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work failure policy payload", input.RequestID)
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
		if work.status != RepositoryExtractionWorkOutcomeFailed || work.claimID != input.ClaimID {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not failed claim %s", input.WorkItemID, input.ClaimID)
		}
		if work.claimedBy == "" || work.claimedAt == nil || work.finishedAt == nil || work.failureClass == "" || work.failureMessage == "" {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s has incomplete failed state", input.WorkItemID)
		}
		if decidedAt.Before(work.finishedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s policy decision time precedes its failure", input.WorkItemID)
		}

		attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, input.ClaimID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", input.ClaimID)
		}
		if err != nil {
			return err
		}
		if attempt.workItemID != input.WorkItemID ||
			attempt.workerID != work.claimedBy ||
			attempt.status != RepositoryExtractionWorkOutcomeFailed ||
			attempt.finishedAt == nil ||
			attempt.expiredAt != nil ||
			!attempt.claimedAt.Equal(work.claimedAt.UTC()) ||
			!attempt.finishedAt.Equal(work.finishedAt.UTC()) ||
			attempt.failureClass != work.failureClass ||
			attempt.failureMessage != work.failureMessage {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s does not match failed work", input.ClaimID)
		}
		if existingRequestID, ok, err := readRepositoryExtractionWorkFailurePolicyRequestIDByClaim(ctx, tx, input.ClaimID); err != nil {
			return err
		} else if ok {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim %s already has failure policy request %s", input.ClaimID, existingRequestID)
		}

		policy := decideRepositoryExtractionWorkFailurePolicy(attempt.failureClass, attempt.attemptNumber, decidedAt)
		result = RepositoryExtractionWorkFailurePolicyResult{
			RequestID:           input.RequestID,
			Work:                work.work,
			ClaimID:             input.ClaimID,
			AttemptNumber:       attempt.attemptNumber,
			WorkerID:            attempt.workerID,
			FinishedAt:          attempt.finishedAt.UTC(),
			FailureClass:        attempt.failureClass,
			FailureMessage:      attempt.failureMessage,
			PolicyVersion:       RepositoryExtractionWorkFailurePolicyV1,
			Decision:            policy.decision,
			DecisionReason:      policy.reason,
			MaxAttempts:         RepositoryExtractionWorkFailurePolicyMaxAttempts,
			BackoffMilliseconds: policy.backoffMilliseconds,
			RetryNotBefore:      policy.retryNotBefore,
			DecidedAt:           decidedAt,
			DecisionActorID:     input.DecisionActorID,
		}
		return persistRepositoryExtractionWorkFailurePolicyDecision(ctx, tx, result, payloadHash)
	})
	if err != nil {
		return RepositoryExtractionWorkFailurePolicyResult{}, err
	}
	return result, nil
}

func decideRepositoryExtractionWorkFailurePolicy(failureClass string, attemptNumber int, decidedAt time.Time) repositoryExtractionWorkFailurePolicyDecision {
	switch ErrorKind(failureClass) {
	case ErrorRunnerInvocationFailed, ErrorRunnerInvocationTimeout, ErrorRunnerInvocationCancelled:
		if attemptNumber >= RepositoryExtractionWorkFailurePolicyMaxAttempts {
			return repositoryExtractionWorkFailurePolicyDecision{
				decision: RepositoryExtractionWorkFailureDecisionExhausted,
				reason:   RepositoryExtractionWorkFailureReasonLimitReached,
			}
		}
		backoff := repositoryExtractionWorkFailurePolicyBaseBackoff * time.Duration(1<<(attemptNumber-1))
		retryNotBefore := decidedAt.Add(backoff).UTC()
		return repositoryExtractionWorkFailurePolicyDecision{
			decision:            RepositoryExtractionWorkFailureDecisionRetry,
			reason:              RepositoryExtractionWorkFailureReasonRetryable,
			backoffMilliseconds: backoff.Milliseconds(),
			retryNotBefore:      &retryNotBefore,
		}
	case ErrorPersistedAttemptFailed, ErrorRepositoryWorkLeaseExpired:
		return repositoryExtractionWorkFailurePolicyDecision{
			decision: RepositoryExtractionWorkFailureDecisionManualReview,
			reason:   RepositoryExtractionWorkFailureReasonSeparateRepair,
		}
	case ErrorInvalidUTF8,
		ErrorUnknownSpan,
		ErrorSpanOutOfBounds,
		ErrorQuotedHashMismatch,
		ErrorDuplicateProposalLocalID,
		ErrorMissingSourceViewAttempt,
		ErrorOccurrenceConflict,
		ErrorInvalidInput,
		ErrorInvalidRecordID,
		ErrorIdempotencyKeyReused,
		ErrorRepositoryRevisionMismatch,
		ErrorRepositorySnapshotIntegrity,
		ErrorInvalidExtractorOutput,
		ErrorUnsupportedAdmission,
		ErrorAdmissionStateConflict,
		ErrorSourceGenerationConflict,
		ErrorChangeObservationConflict,
		ErrorRepositoryWorkConflict:
		return repositoryExtractionWorkFailurePolicyDecision{
			decision: RepositoryExtractionWorkFailureDecisionStop,
			reason:   RepositoryExtractionWorkFailureReasonDeterministic,
		}
	default:
		return repositoryExtractionWorkFailurePolicyDecision{
			decision: RepositoryExtractionWorkFailureDecisionManualReview,
			reason:   RepositoryExtractionWorkFailureReasonUnclassified,
		}
	}
}

func validateRepositoryExtractionWorkFailurePolicyInput(input RepositoryExtractionWorkFailurePolicyInput) (RepositoryExtractionWorkFailurePolicyInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.DecisionActorID = strings.TrimSpace(input.DecisionActorID)
	if input.RequestID == "" {
		return RepositoryExtractionWorkFailurePolicyInput{}, newDomainError(ErrorInvalidInput, "repository extraction work failure policy request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkFailurePolicyInput{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkFailurePolicyInput{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.DecisionActorID == "" || len(input.DecisionActorID) > 200 {
		return RepositoryExtractionWorkFailurePolicyInput{}, newDomainError(ErrorInvalidInput, "repository extraction work decision_actor_id must contain 1 to 200 bytes")
	}
	return input, nil
}

func repositoryExtractionWorkFailurePolicyPayloadHash(input RepositoryExtractionWorkFailurePolicyInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkItemID      string `json:"work_item_id"`
		ClaimID         string `json:"claim_id"`
		DecisionActorID string `json:"decision_actor_id"`
	}{
		WorkItemID:      input.WorkItemID,
		ClaimID:         input.ClaimID,
		DecisionActorID: input.DecisionActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func persistRepositoryExtractionWorkFailurePolicyDecision(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkFailurePolicyResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_failure_policy_decisions (
			request_id,
			work_item_id,
			claim_id,
			decision_actor_id,
			policy_version,
			failure_class,
			attempt_number,
			decision,
			decision_reason,
			max_attempts,
			backoff_milliseconds,
			retry_not_before,
			decided_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, result.RequestID, result.Work.WorkItemID, result.ClaimID, result.DecisionActorID, result.PolicyVersion, result.FailureClass, result.AttemptNumber, result.Decision, result.DecisionReason, result.MaxAttempts, result.BackoffMilliseconds, result.RetryNotBefore, result.DecidedAt, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work failure policy decision: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkFailurePolicyDecision(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkFailurePolicyDecision, bool, error) {
	var persisted persistedRepositoryExtractionWorkFailurePolicyDecision
	var workItemID string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			work_item_id,
			claim_id,
			decision_actor_id,
			policy_version,
			failure_class,
			attempt_number,
			decision,
			decision_reason,
			max_attempts,
			backoff_milliseconds,
			retry_not_before,
			decided_at,
			request_payload_hash
		FROM repository_extraction_work_failure_policy_decisions
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.DecisionActorID,
		&persisted.result.PolicyVersion,
		&persisted.result.FailureClass,
		&persisted.result.AttemptNumber,
		&persisted.result.Decision,
		&persisted.result.DecisionReason,
		&persisted.result.MaxAttempts,
		&persisted.result.BackoffMilliseconds,
		&persisted.result.RetryNotBefore,
		&persisted.result.DecidedAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkFailurePolicyDecision{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkFailurePolicyDecision{}, false, fmt.Errorf("reading repository extraction work failure policy decision: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if err != nil {
		return persistedRepositoryExtractionWorkFailurePolicyDecision{}, false, err
	}
	attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, persisted.result.ClaimID)
	if err != nil {
		return persistedRepositoryExtractionWorkFailurePolicyDecision{}, false, err
	}
	if attempt.workItemID != workItemID ||
		attempt.status != RepositoryExtractionWorkOutcomeFailed ||
		attempt.finishedAt == nil ||
		attempt.expiredAt != nil ||
		attempt.attemptNumber != persisted.result.AttemptNumber ||
		attempt.failureClass != persisted.result.FailureClass ||
		persisted.result.DecidedAt.Before(attempt.finishedAt.UTC()) {
		return persistedRepositoryExtractionWorkFailurePolicyDecision{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s does not match failed claim attempt", requestID)
	}
	expected := decideRepositoryExtractionWorkFailurePolicy(attempt.failureClass, attempt.attemptNumber, persisted.result.DecidedAt.UTC())
	if persisted.result.PolicyVersion != RepositoryExtractionWorkFailurePolicyV1 ||
		persisted.result.Decision != expected.decision ||
		persisted.result.DecisionReason != expected.reason ||
		persisted.result.MaxAttempts != RepositoryExtractionWorkFailurePolicyMaxAttempts ||
		persisted.result.BackoffMilliseconds != expected.backoffMilliseconds ||
		!sameOptionalTime(persisted.result.RetryNotBefore, expected.retryNotBefore) {
		return persistedRepositoryExtractionWorkFailurePolicyDecision{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s has inconsistent durable decision", requestID)
	}
	persisted.result.Work = work.work
	persisted.result.WorkerID = attempt.workerID
	persisted.result.FinishedAt = attempt.finishedAt.UTC()
	persisted.result.FailureMessage = attempt.failureMessage
	persisted.result.DecidedAt = persisted.result.DecidedAt.UTC()
	if persisted.result.RetryNotBefore != nil {
		retryNotBefore := persisted.result.RetryNotBefore.UTC()
		persisted.result.RetryNotBefore = &retryNotBefore
	}
	return persisted, true, nil
}

func readRepositoryExtractionWorkFailurePolicyRequestIDByClaim(ctx context.Context, tx sqlTx, claimID string) (string, bool, error) {
	var requestID string
	err := tx.queryRow(ctx, `
		SELECT request_id
		FROM repository_extraction_work_failure_policy_decisions
		WHERE claim_id = $1
	`, claimID).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading repository extraction work failure policy decision by claim: %w", err)
	}
	return requestID, true, nil
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
