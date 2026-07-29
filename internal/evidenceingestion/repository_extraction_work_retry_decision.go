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

// RepositoryExtractionWorkRetryDecisionConsumptionInput identifies one exact persisted retry decision.
type RepositoryExtractionWorkRetryDecisionConsumptionInput struct {
	RequestID              string `json:"request_id"`
	FailurePolicyRequestID string `json:"failure_policy_request_id"`
	WorkItemID             string `json:"work_item_id"`
	ClaimID                string `json:"claim_id"`
	ConsumerActorID        string `json:"consumer_actor_id"`
}

// RepositoryExtractionWorkRetryDecisionConsumptionResult reports one durable policy-authorized retry transition.
type RepositoryExtractionWorkRetryDecisionConsumptionResult struct {
	RequestID              string                   `json:"request_id"`
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
	BackoffMilliseconds    int64                    `json:"backoff_milliseconds"`
	RetryNotBefore         time.Time                `json:"retry_not_before"`
	DecidedAt              time.Time                `json:"decided_at"`
	ConsumedAt             time.Time                `json:"consumed_at"`
	ConsumerActorID        string                   `json:"consumer_actor_id"`
	Disposition            string                   `json:"disposition"`
	SupersededByWorkItemID string                   `json:"superseded_by_work_item_id,omitempty"`
	Replayed               bool                     `json:"replayed"`
}

type persistedRepositoryExtractionWorkRetryDecisionConsumption struct {
	result             RepositoryExtractionWorkRetryDecisionConsumptionResult
	requestPayloadHash string
}

type repositoryExtractionWorkRetryDecisionConsumptionRequest struct {
	input              RepositoryExtractionWorkRetryDecisionConsumptionInput
	consumedAt         time.Time
	requestPayloadHash string
}

// ConsumeDueRepositoryExtractionWorkRetryDecision applies one due persisted retry decision.
func ConsumeDueRepositoryExtractionWorkRetryDecision(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkRetryDecisionConsumptionInput) (RepositoryExtractionWorkRetryDecisionConsumptionResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func consumeDueRepositoryExtractionWorkRetryDecision(ctx context.Context, db sqlDB, input RepositoryExtractionWorkRetryDecisionConsumptionInput, consumedAt time.Time) (RepositoryExtractionWorkRetryDecisionConsumptionResult, error) {
	request, err := prepareRepositoryExtractionWorkRetryDecisionConsumption(input, consumedAt)
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}

	var result RepositoryExtractionWorkRetryDecisionConsumptionResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		var err error
		result, err = consumeDueRepositoryExtractionWorkRetryDecisionTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	return result, nil
}

func prepareRepositoryExtractionWorkRetryDecisionConsumption(input RepositoryExtractionWorkRetryDecisionConsumptionInput, consumedAt time.Time) (repositoryExtractionWorkRetryDecisionConsumptionRequest, error) {
	input, err := validateRepositoryExtractionWorkRetryDecisionConsumptionInput(input)
	if err != nil {
		return repositoryExtractionWorkRetryDecisionConsumptionRequest{}, err
	}
	if consumedAt.IsZero() {
		return repositoryExtractionWorkRetryDecisionConsumptionRequest{}, newDomainError(ErrorInvalidInput, "consumed_at is required")
	}
	consumedAt = consumedAt.UTC()
	payloadHash, err := repositoryExtractionWorkRetryDecisionConsumptionPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkRetryDecisionConsumptionRequest{}, err
	}
	return repositoryExtractionWorkRetryDecisionConsumptionRequest{
		input:              input,
		consumedAt:         consumedAt,
		requestPayloadHash: payloadHash,
	}, nil
}

func consumeDueRepositoryExtractionWorkRetryDecisionTx(ctx context.Context, tx sqlTx, request repositoryExtractionWorkRetryDecisionConsumptionRequest) (RepositoryExtractionWorkRetryDecisionConsumptionResult, error) {
	input := request.input
	consumedAt := request.consumedAt
	if result, ok, err := replayRepositoryExtractionWorkRetryDecisionConsumptionIfPresent(ctx, tx, input.RequestID, request.requestPayloadHash); err != nil || ok {
		return result, err
	}
	_, err := lockRepositorySourceStreamForWork(ctx, tx, input.WorkItemID)
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-consume-retry-decision", input.RequestID); err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	if result, ok, err := replayRepositoryExtractionWorkRetryDecisionConsumptionIfPresent(ctx, tx, input.RequestID, request.requestPayloadHash); err != nil || ok {
		return result, err
	}

	policy, ok, err := readRepositoryExtractionWorkFailurePolicyDecision(ctx, tx, input.FailurePolicyRequestID)
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	if !ok {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work failure policy request %s was not found", input.FailurePolicyRequestID)
	}
	if policy.result.Work.WorkItemID != input.WorkItemID || policy.result.ClaimID != input.ClaimID {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s does not identify work %s claim %s", input.FailurePolicyRequestID, input.WorkItemID, input.ClaimID)
	}
	if policy.result.Decision != RepositoryExtractionWorkFailureDecisionRetry || policy.result.RetryNotBefore == nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s is not a retry decision", input.FailurePolicyRequestID)
	}
	retryNotBefore := policy.result.RetryNotBefore.UTC()
	if consumedAt.Before(retryNotBefore) {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s is not due before %s", input.FailurePolicyRequestID, retryNotBefore.Format(time.RFC3339Nano))
	}

	work, err := readRepositoryExtractionWorkByIDForUpdate(ctx, tx, input.WorkItemID)
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	if existingRequestID, ok, err := readRepositoryExtractionWorkRetryDecisionConsumptionRequestID(ctx, tx, input.FailurePolicyRequestID); err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	} else if ok {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work failure policy request %s was already consumed by request %s", input.FailurePolicyRequestID, existingRequestID)
	}

	transition, err := retryFailedRepositoryExtractionWorkLocked(ctx, tx, work, input.ClaimID, consumedAt)
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	result := RepositoryExtractionWorkRetryDecisionConsumptionResult{
		RequestID:              input.RequestID,
		FailurePolicyRequestID: input.FailurePolicyRequestID,
		Work:                   transition.Work,
		ClaimID:                transition.ClaimID,
		AttemptNumber:          transition.AttemptNumber,
		WorkerID:               transition.WorkerID,
		FinishedAt:             transition.FinishedAt,
		FailureClass:           transition.FailureClass,
		FailureMessage:         transition.FailureMessage,
		PolicyVersion:          policy.result.PolicyVersion,
		Decision:               policy.result.Decision,
		DecisionReason:         policy.result.DecisionReason,
		BackoffMilliseconds:    policy.result.BackoffMilliseconds,
		RetryNotBefore:         retryNotBefore,
		DecidedAt:              policy.result.DecidedAt,
		ConsumedAt:             consumedAt,
		ConsumerActorID:        input.ConsumerActorID,
		Disposition:            transition.Disposition,
		SupersededByWorkItemID: transition.SupersededByWorkItemID,
	}
	if err := persistRepositoryExtractionWorkRetryDecisionConsumption(ctx, tx, result, request.requestPayloadHash); err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, err
	}
	return result, nil
}

func replayRepositoryExtractionWorkRetryDecisionConsumptionIfPresent(ctx context.Context, tx sqlTx, requestID, requestPayloadHash string) (RepositoryExtractionWorkRetryDecisionConsumptionResult, bool, error) {
	persisted, ok, err := readRepositoryExtractionWorkRetryDecisionConsumption(ctx, tx, requestID)
	if err != nil {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, false, err
	}
	if !ok {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, false, nil
	}
	if persisted.requestPayloadHash != requestPayloadHash {
		return RepositoryExtractionWorkRetryDecisionConsumptionResult{}, false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work retry decision consumption payload", requestID)
	}
	result := persisted.result
	result.Replayed = true
	return result, true, nil
}

func validateRepositoryExtractionWorkRetryDecisionConsumptionInput(input RepositoryExtractionWorkRetryDecisionConsumptionInput) (RepositoryExtractionWorkRetryDecisionConsumptionInput, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.FailurePolicyRequestID = strings.TrimSpace(input.FailurePolicyRequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.ConsumerActorID = strings.TrimSpace(input.ConsumerActorID)
	if input.RequestID == "" {
		return RepositoryExtractionWorkRetryDecisionConsumptionInput{}, newDomainError(ErrorInvalidInput, "repository extraction work retry decision consumption request_id is required")
	}
	if input.FailurePolicyRequestID == "" {
		return RepositoryExtractionWorkRetryDecisionConsumptionInput{}, newDomainError(ErrorInvalidInput, "failure_policy_request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return RepositoryExtractionWorkRetryDecisionConsumptionInput{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return RepositoryExtractionWorkRetryDecisionConsumptionInput{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.ConsumerActorID == "" || len(input.ConsumerActorID) > 200 {
		return RepositoryExtractionWorkRetryDecisionConsumptionInput{}, newDomainError(ErrorInvalidInput, "repository extraction work consumer_actor_id must contain 1 to 200 bytes")
	}
	return input, nil
}

func repositoryExtractionWorkRetryDecisionConsumptionPayloadHash(input RepositoryExtractionWorkRetryDecisionConsumptionInput) (string, error) {
	payload, err := deterministicJSON(struct {
		FailurePolicyRequestID string `json:"failure_policy_request_id"`
		WorkItemID             string `json:"work_item_id"`
		ClaimID                string `json:"claim_id"`
		ConsumerActorID        string `json:"consumer_actor_id"`
	}{
		FailurePolicyRequestID: input.FailurePolicyRequestID,
		WorkItemID:             input.WorkItemID,
		ClaimID:                input.ClaimID,
		ConsumerActorID:        input.ConsumerActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func persistRepositoryExtractionWorkRetryDecisionConsumption(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkRetryDecisionConsumptionResult, payloadHash string) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_retry_decision_consumptions (
			request_id,
			failure_policy_request_id,
			work_item_id,
			claim_id,
			consumer_actor_id,
			policy_decision,
			retry_not_before,
			disposition,
			superseded_by_work_item_id,
			consumed_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, result.RequestID, result.FailurePolicyRequestID, result.Work.WorkItemID, result.ClaimID, result.ConsumerActorID, result.Decision, result.RetryNotBefore, result.Disposition, nullableString(result.SupersededByWorkItemID), result.ConsumedAt, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work retry decision consumption: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkRetryDecisionConsumption(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkRetryDecisionConsumption, bool, error) {
	var persisted persistedRepositoryExtractionWorkRetryDecisionConsumption
	var workItemID, policyDecision string
	var supersededBy *string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			failure_policy_request_id,
			work_item_id,
			claim_id,
			consumer_actor_id,
			policy_decision,
			retry_not_before,
			disposition,
			superseded_by_work_item_id,
			consumed_at,
			request_payload_hash
		FROM repository_extraction_work_retry_decision_consumptions
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&persisted.result.FailurePolicyRequestID,
		&workItemID,
		&persisted.result.ClaimID,
		&persisted.result.ConsumerActorID,
		&policyDecision,
		&persisted.result.RetryNotBefore,
		&persisted.result.Disposition,
		&supersededBy,
		&persisted.result.ConsumedAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkRetryDecisionConsumption{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkRetryDecisionConsumption{}, false, fmt.Errorf("reading repository extraction work retry decision consumption: %w", err)
	}

	policy, ok, err := readRepositoryExtractionWorkFailurePolicyDecision(ctx, tx, persisted.result.FailurePolicyRequestID)
	if err != nil {
		return persistedRepositoryExtractionWorkRetryDecisionConsumption{}, false, err
	}
	if !ok ||
		policy.result.Work.WorkItemID != workItemID ||
		policy.result.ClaimID != persisted.result.ClaimID ||
		policyDecision != RepositoryExtractionWorkFailureDecisionRetry ||
		policy.result.Decision != policyDecision ||
		policy.result.RetryNotBefore == nil ||
		!persisted.result.RetryNotBefore.Equal(*policy.result.RetryNotBefore) ||
		persisted.result.ConsumedAt.Before(persisted.result.RetryNotBefore) {
		return persistedRepositoryExtractionWorkRetryDecisionConsumption{}, false, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry decision consumption request %s has inconsistent durable authority", requestID)
	}
	persisted.result.Work = policy.result.Work
	persisted.result.AttemptNumber = policy.result.AttemptNumber
	persisted.result.WorkerID = policy.result.WorkerID
	persisted.result.FinishedAt = policy.result.FinishedAt
	persisted.result.FailureClass = policy.result.FailureClass
	persisted.result.FailureMessage = policy.result.FailureMessage
	persisted.result.PolicyVersion = policy.result.PolicyVersion
	persisted.result.Decision = policy.result.Decision
	persisted.result.DecisionReason = policy.result.DecisionReason
	persisted.result.BackoffMilliseconds = policy.result.BackoffMilliseconds
	persisted.result.DecidedAt = policy.result.DecidedAt
	persisted.result.RetryNotBefore = persisted.result.RetryNotBefore.UTC()
	persisted.result.ConsumedAt = persisted.result.ConsumedAt.UTC()
	if supersededBy != nil {
		persisted.result.SupersededByWorkItemID = *supersededBy
	}
	return persisted, true, nil
}

func readRepositoryExtractionWorkRetryDecisionConsumptionRequestID(ctx context.Context, tx sqlTx, failurePolicyRequestID string) (string, bool, error) {
	var requestID string
	err := tx.queryRow(ctx, `
		SELECT request_id
		FROM repository_extraction_work_retry_decision_consumptions
		WHERE failure_policy_request_id = $1
	`, failurePolicyRequestID).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading repository extraction work retry decision consumption by policy request: %w", err)
	}
	return requestID, true, nil
}
