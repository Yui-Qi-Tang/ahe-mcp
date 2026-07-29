package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkRetryDecisionConsumptionInput(t *testing.T) {
	valid := RepositoryExtractionWorkRetryDecisionConsumptionInput{
		RequestID:              "consume-1",
		FailurePolicyRequestID: "failure-policy-1",
		WorkItemID:             "repo-work:1",
		ClaimID:                "work-claim:1",
		ConsumerActorID:        "retry-controller-1",
	}
	got, err := validateRepositoryExtractionWorkRetryDecisionConsumptionInput(valid)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkRetryDecisionConsumptionInput() error = %v", err)
	}
	if got != valid {
		t.Fatalf("validated input = %+v, want %+v", got, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkRetryDecisionConsumptionInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withRetryDecisionConsumptionRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "missing policy request", input: withRetryDecisionConsumptionPolicyRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withRetryDecisionConsumptionWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withRetryDecisionConsumptionClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing actor", input: withRetryDecisionConsumptionActorID(valid, ""), kind: ErrorInvalidInput},
		{name: "long actor", input: withRetryDecisionConsumptionActorID(valid, strings.Repeat("a", 201)), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkRetryDecisionConsumptionInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkRetryDecisionConsumptionPayloadBindsAuthority(t *testing.T) {
	base := RepositoryExtractionWorkRetryDecisionConsumptionInput{
		FailurePolicyRequestID: "failure-policy-1",
		WorkItemID:             "repo-work:1",
		ClaimID:                "work-claim:1",
		ConsumerActorID:        "retry-controller-1",
	}
	baseHash, err := repositoryExtractionWorkRetryDecisionConsumptionPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkRetryDecisionConsumptionPayloadHash(base) error = %v", err)
	}
	for _, changed := range []RepositoryExtractionWorkRetryDecisionConsumptionInput{
		withRetryDecisionConsumptionPolicyRequestID(base, "failure-policy-2"),
		withRetryDecisionConsumptionWorkItemID(base, "repo-work:2"),
		withRetryDecisionConsumptionClaimID(base, "work-claim:2"),
		withRetryDecisionConsumptionActorID(base, "retry-controller-2"),
	} {
		changedHash, err := repositoryExtractionWorkRetryDecisionConsumptionPayloadHash(changed)
		if err != nil {
			t.Fatalf("repositoryExtractionWorkRetryDecisionConsumptionPayloadHash(changed) error = %v", err)
		}
		if changedHash == baseHash {
			t.Fatalf("changed consumption payload hash = base hash %s", baseHash)
		}
	}
}

func TestConsumeDueRepositoryExtractionWorkRetryDecisionValidatesPoolAndTime(t *testing.T) {
	_, err := ConsumeDueRepositoryExtractionWorkRetryDecision(t.Context(), nil, RepositoryExtractionWorkRetryDecisionConsumptionInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = consumeDueRepositoryExtractionWorkRetryDecision(t.Context(), nil, RepositoryExtractionWorkRetryDecisionConsumptionInput{
		RequestID:              "consume-1",
		FailurePolicyRequestID: "failure-policy-1",
		WorkItemID:             "repo-work:1",
		ClaimID:                "work-claim:1",
		ConsumerActorID:        "retry-controller-1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func withRetryDecisionConsumptionRequestID(input RepositoryExtractionWorkRetryDecisionConsumptionInput, value string) RepositoryExtractionWorkRetryDecisionConsumptionInput {
	input.RequestID = value
	return input
}

func withRetryDecisionConsumptionPolicyRequestID(input RepositoryExtractionWorkRetryDecisionConsumptionInput, value string) RepositoryExtractionWorkRetryDecisionConsumptionInput {
	input.FailurePolicyRequestID = value
	return input
}

func withRetryDecisionConsumptionWorkItemID(input RepositoryExtractionWorkRetryDecisionConsumptionInput, value string) RepositoryExtractionWorkRetryDecisionConsumptionInput {
	input.WorkItemID = value
	return input
}

func withRetryDecisionConsumptionClaimID(input RepositoryExtractionWorkRetryDecisionConsumptionInput, value string) RepositoryExtractionWorkRetryDecisionConsumptionInput {
	input.ClaimID = value
	return input
}

func withRetryDecisionConsumptionActorID(input RepositoryExtractionWorkRetryDecisionConsumptionInput, value string) RepositoryExtractionWorkRetryDecisionConsumptionInput {
	input.ConsumerActorID = value
	return input
}
