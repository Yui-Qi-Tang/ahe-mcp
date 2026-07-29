package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestDecideRepositoryExtractionWorkFailurePolicy(t *testing.T) {
	decidedAt := time.Date(2026, 7, 21, 21, 0, 0, 0, time.UTC)
	tests := []struct {
		name                string
		failureClass        string
		attemptNumber       int
		decision            string
		reason              string
		backoffMilliseconds int64
		retryNotBefore      *time.Time
	}{
		{
			name:                "first runner failure retries after five seconds",
			failureClass:        string(ErrorRunnerInvocationFailed),
			attemptNumber:       1,
			decision:            RepositoryExtractionWorkFailureDecisionRetry,
			reason:              RepositoryExtractionWorkFailureReasonRetryable,
			backoffMilliseconds: 5000,
			retryNotBefore:      timePointer(decidedAt.Add(5 * time.Second)),
		},
		{
			name:                "second timeout retries after ten seconds",
			failureClass:        string(ErrorRunnerInvocationTimeout),
			attemptNumber:       2,
			decision:            RepositoryExtractionWorkFailureDecisionRetry,
			reason:              RepositoryExtractionWorkFailureReasonRetryable,
			backoffMilliseconds: 10000,
			retryNotBefore:      timePointer(decidedAt.Add(10 * time.Second)),
		},
		{
			name:          "third cancellation exhausts retry limit",
			failureClass:  string(ErrorRunnerInvocationCancelled),
			attemptNumber: 3,
			decision:      RepositoryExtractionWorkFailureDecisionExhausted,
			reason:        RepositoryExtractionWorkFailureReasonLimitReached,
		},
		{
			name:          "invalid extractor output stops",
			failureClass:  string(ErrorInvalidExtractorOutput),
			attemptNumber: 1,
			decision:      RepositoryExtractionWorkFailureDecisionStop,
			reason:        RepositoryExtractionWorkFailureReasonDeterministic,
		},
		{
			name:          "lease expiry needs separate recovery",
			failureClass:  string(ErrorRepositoryWorkLeaseExpired),
			attemptNumber: 1,
			decision:      RepositoryExtractionWorkFailureDecisionManualReview,
			reason:        RepositoryExtractionWorkFailureReasonSeparateRepair,
		},
		{
			name:          "unknown failure needs manual review",
			failureClass:  "future_failure_class",
			attemptNumber: 1,
			decision:      RepositoryExtractionWorkFailureDecisionManualReview,
			reason:        RepositoryExtractionWorkFailureReasonUnclassified,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideRepositoryExtractionWorkFailurePolicy(tt.failureClass, tt.attemptNumber, decidedAt)
			if got.decision != tt.decision || got.reason != tt.reason || got.backoffMilliseconds != tt.backoffMilliseconds || !sameOptionalTime(got.retryNotBefore, tt.retryNotBefore) {
				t.Fatalf("decision = %+v, want decision=%q reason=%q backoff=%d retry_not_before=%v", got, tt.decision, tt.reason, tt.backoffMilliseconds, tt.retryNotBefore)
			}
		})
	}
}

func TestValidateRepositoryExtractionWorkFailurePolicyInput(t *testing.T) {
	valid := RepositoryExtractionWorkFailurePolicyInput{
		RequestID:       "policy-1",
		WorkItemID:      "repo-work:1",
		ClaimID:         "work-claim:1",
		DecisionActorID: "controller-1",
	}
	got, err := validateRepositoryExtractionWorkFailurePolicyInput(valid)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkFailurePolicyInput() error = %v", err)
	}
	if got != valid {
		t.Fatalf("validated input = %+v, want %+v", got, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkFailurePolicyInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withFailurePolicyRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withFailurePolicyWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withFailurePolicyClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing actor", input: withFailurePolicyActorID(valid, ""), kind: ErrorInvalidInput},
		{name: "long actor", input: withFailurePolicyActorID(valid, strings.Repeat("a", 201)), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkFailurePolicyInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkFailurePolicyPayloadBindsClaimAndActor(t *testing.T) {
	base := RepositoryExtractionWorkFailurePolicyInput{
		WorkItemID:      "repo-work:1",
		ClaimID:         "work-claim:1",
		DecisionActorID: "controller-1",
	}
	baseHash, err := repositoryExtractionWorkFailurePolicyPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkFailurePolicyPayloadHash(base) error = %v", err)
	}
	for _, changed := range []RepositoryExtractionWorkFailurePolicyInput{
		withFailurePolicyWorkItemID(base, "repo-work:2"),
		withFailurePolicyClaimID(base, "work-claim:2"),
		withFailurePolicyActorID(base, "controller-2"),
	} {
		changedHash, err := repositoryExtractionWorkFailurePolicyPayloadHash(changed)
		if err != nil {
			t.Fatalf("repositoryExtractionWorkFailurePolicyPayloadHash(changed) error = %v", err)
		}
		if changedHash == baseHash {
			t.Fatalf("changed failure policy payload hash = base hash %s", baseHash)
		}
	}
}

func TestClassifyFailedRepositoryExtractionWorkValidatesPoolAndTime(t *testing.T) {
	_, err := ClassifyFailedRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkFailurePolicyInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = classifyFailedRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkFailurePolicyInput{
		RequestID:       "policy-1",
		WorkItemID:      "repo-work:1",
		ClaimID:         "work-claim:1",
		DecisionActorID: "controller-1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func withFailurePolicyRequestID(input RepositoryExtractionWorkFailurePolicyInput, value string) RepositoryExtractionWorkFailurePolicyInput {
	input.RequestID = value
	return input
}

func withFailurePolicyWorkItemID(input RepositoryExtractionWorkFailurePolicyInput, value string) RepositoryExtractionWorkFailurePolicyInput {
	input.WorkItemID = value
	return input
}

func withFailurePolicyClaimID(input RepositoryExtractionWorkFailurePolicyInput, value string) RepositoryExtractionWorkFailurePolicyInput {
	input.ClaimID = value
	return input
}

func withFailurePolicyActorID(input RepositoryExtractionWorkFailurePolicyInput, value string) RepositoryExtractionWorkFailurePolicyInput {
	input.DecisionActorID = value
	return input
}
