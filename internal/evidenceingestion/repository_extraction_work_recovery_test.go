package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkRecoveryInput(t *testing.T) {
	valid := RepositoryExtractionWorkRecoveryInput{
		RequestID:       "recover-1",
		WorkItemID:      "repo-work:1",
		ClaimID:         "work-claim:1",
		RecoveryActorID: "operator-1",
	}
	got, err := validateRepositoryExtractionWorkRecoveryInput(valid)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkRecoveryInput() error = %v", err)
	}
	if got != valid {
		t.Fatalf("validated input = %+v, want %+v", got, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkRecoveryInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withRecoveryRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withRecoveryWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withRecoveryClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing actor", input: withRecoveryActorID(valid, ""), kind: ErrorInvalidInput},
		{name: "long actor", input: withRecoveryActorID(valid, strings.Repeat("a", 201)), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkRecoveryInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkRecoveryPayloadBindsClaimAndActor(t *testing.T) {
	base := RepositoryExtractionWorkRecoveryInput{
		WorkItemID:      "repo-work:1",
		ClaimID:         "work-claim:1",
		RecoveryActorID: "operator-1",
	}
	baseHash, err := repositoryExtractionWorkRecoveryPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkRecoveryPayloadHash(base) error = %v", err)
	}
	for _, changed := range []RepositoryExtractionWorkRecoveryInput{
		withRecoveryWorkItemID(base, "repo-work:2"),
		withRecoveryClaimID(base, "work-claim:2"),
		withRecoveryActorID(base, "operator-2"),
	} {
		changedHash, err := repositoryExtractionWorkRecoveryPayloadHash(changed)
		if err != nil {
			t.Fatalf("repositoryExtractionWorkRecoveryPayloadHash(changed) error = %v", err)
		}
		if changedHash == baseHash {
			t.Fatalf("changed recovery payload hash = base hash %s", baseHash)
		}
	}
}

func TestRecoverExpiredRepositoryExtractionWorkValidatesPoolAndTime(t *testing.T) {
	_, err := RecoverExpiredRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkRecoveryInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = recoverExpiredRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkRecoveryInput{
		RequestID:       "recover-1",
		WorkItemID:      "repo-work:1",
		ClaimID:         "work-claim:1",
		RecoveryActorID: "operator-1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func withRecoveryRequestID(input RepositoryExtractionWorkRecoveryInput, value string) RepositoryExtractionWorkRecoveryInput {
	input.RequestID = value
	return input
}

func withRecoveryWorkItemID(input RepositoryExtractionWorkRecoveryInput, value string) RepositoryExtractionWorkRecoveryInput {
	input.WorkItemID = value
	return input
}

func withRecoveryClaimID(input RepositoryExtractionWorkRecoveryInput, value string) RepositoryExtractionWorkRecoveryInput {
	input.ClaimID = value
	return input
}

func withRecoveryActorID(input RepositoryExtractionWorkRecoveryInput, value string) RepositoryExtractionWorkRecoveryInput {
	input.RecoveryActorID = value
	return input
}
