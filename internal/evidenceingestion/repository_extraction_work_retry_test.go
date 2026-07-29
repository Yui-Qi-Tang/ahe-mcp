package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkRetryInput(t *testing.T) {
	valid := RepositoryExtractionWorkRetryInput{
		RequestID:    "retry-1",
		WorkItemID:   "repo-work:1",
		ClaimID:      "work-claim:1",
		RetryActorID: "operator-1",
	}
	got, err := validateRepositoryExtractionWorkRetryInput(valid)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkRetryInput() error = %v", err)
	}
	if got != valid {
		t.Fatalf("validated input = %+v, want %+v", got, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkRetryInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withRetryRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withRetryWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withRetryClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing actor", input: withRetryActorID(valid, ""), kind: ErrorInvalidInput},
		{name: "long actor", input: withRetryActorID(valid, strings.Repeat("a", 201)), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkRetryInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkRetryPayloadBindsClaimAndActor(t *testing.T) {
	base := RepositoryExtractionWorkRetryInput{
		WorkItemID:   "repo-work:1",
		ClaimID:      "work-claim:1",
		RetryActorID: "operator-1",
	}
	baseHash, err := repositoryExtractionWorkRetryPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkRetryPayloadHash(base) error = %v", err)
	}
	for _, changed := range []RepositoryExtractionWorkRetryInput{
		withRetryWorkItemID(base, "repo-work:2"),
		withRetryClaimID(base, "work-claim:2"),
		withRetryActorID(base, "operator-2"),
	} {
		changedHash, err := repositoryExtractionWorkRetryPayloadHash(changed)
		if err != nil {
			t.Fatalf("repositoryExtractionWorkRetryPayloadHash(changed) error = %v", err)
		}
		if changedHash == baseHash {
			t.Fatalf("changed retry payload hash = base hash %s", baseHash)
		}
	}
}

func TestRetryFailedRepositoryExtractionWorkValidatesPoolAndTime(t *testing.T) {
	_, err := RetryFailedRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkRetryInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = retryFailedRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkRetryInput{
		RequestID:    "retry-1",
		WorkItemID:   "repo-work:1",
		ClaimID:      "work-claim:1",
		RetryActorID: "operator-1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func withRetryRequestID(input RepositoryExtractionWorkRetryInput, value string) RepositoryExtractionWorkRetryInput {
	input.RequestID = value
	return input
}

func withRetryWorkItemID(input RepositoryExtractionWorkRetryInput, value string) RepositoryExtractionWorkRetryInput {
	input.WorkItemID = value
	return input
}

func withRetryClaimID(input RepositoryExtractionWorkRetryInput, value string) RepositoryExtractionWorkRetryInput {
	input.ClaimID = value
	return input
}

func withRetryActorID(input RepositoryExtractionWorkRetryInput, value string) RepositoryExtractionWorkRetryInput {
	input.RetryActorID = value
	return input
}
