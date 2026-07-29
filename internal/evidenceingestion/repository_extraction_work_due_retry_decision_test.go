package evidenceingestion

import (
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkDueRetryDecisionListInput(t *testing.T) {
	for _, limit := range []int{1, RepositoryExtractionWorkDueRetryDecisionMaxLimit} {
		if err := validateRepositoryExtractionWorkDueRetryDecisionListInput(RepositoryExtractionWorkDueRetryDecisionListInput{Limit: limit}); err != nil {
			t.Fatalf("validate limit %d: %v", limit, err)
		}
	}

	for _, limit := range []int{0, RepositoryExtractionWorkDueRetryDecisionMaxLimit + 1} {
		err := validateRepositoryExtractionWorkDueRetryDecisionListInput(RepositoryExtractionWorkDueRetryDecisionListInput{Limit: limit})
		assertKind(t, err, ErrorInvalidInput)
	}
}

func TestListDueRepositoryExtractionWorkRetryDecisionsValidatesPoolAndTime(t *testing.T) {
	_, err := ListDueRepositoryExtractionWorkRetryDecisions(t.Context(), nil, RepositoryExtractionWorkDueRetryDecisionListInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = listDueRepositoryExtractionWorkRetryDecisions(t.Context(), nil, RepositoryExtractionWorkDueRetryDecisionListInput{Limit: 1}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func TestValidatePersistedRepositoryExtractionWorkDueRetryDecision(t *testing.T) {
	finishedAt := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	decidedAt := finishedAt.Add(time.Millisecond)
	decision := RepositoryExtractionWorkDueRetryDecision{
		FailurePolicyRequestID: "failure-policy:1",
		AttemptNumber:          1,
		FinishedAt:             finishedAt,
		FailureClass:           string(ErrorRunnerInvocationFailed),
		PolicyVersion:          RepositoryExtractionWorkFailurePolicyV1,
		Decision:               RepositoryExtractionWorkFailureDecisionRetry,
		DecisionReason:         RepositoryExtractionWorkFailureReasonRetryable,
		MaxAttempts:            RepositoryExtractionWorkFailurePolicyMaxAttempts,
		BackoffMilliseconds:    5000,
		RetryNotBefore:         decidedAt.Add(5 * time.Second),
		DecidedAt:              decidedAt,
	}
	if err := validatePersistedRepositoryExtractionWorkDueRetryDecision(decision, 1, decision.FailureClass); err != nil {
		t.Fatalf("validate persisted due retry decision: %v", err)
	}

	decision.BackoffMilliseconds++
	err := validatePersistedRepositoryExtractionWorkDueRetryDecision(decision, 1, decision.FailureClass)
	assertKind(t, err, ErrorRepositoryWorkConflict)
}
