package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkExecutionRepairInput(t *testing.T) {
	valid := RepositoryExtractionWorkExecutionRepairInput{
		RequestID:          "repair-1",
		ExecutionRequestID: "execution-1",
		WorkItemID:         "repo-work:1",
		ClaimID:            "work-claim:1",
		WorkerID:           "worker-1",
		RepairActorID:      "controller-1",
	}
	got, err := validateRepositoryExtractionWorkExecutionRepairInput(valid)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkExecutionRepairInput() error = %v", err)
	}
	if got != valid {
		t.Fatalf("validated input = %+v, want %+v", got, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkExecutionRepairInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withExecutionRepairRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "missing execution request", input: withExecutionRepairExecutionRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withExecutionRepairWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withExecutionRepairClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing worker", input: withExecutionRepairWorkerID(valid, ""), kind: ErrorInvalidInput},
		{name: "long worker", input: withExecutionRepairWorkerID(valid, strings.Repeat("w", 201)), kind: ErrorInvalidInput},
		{name: "missing actor", input: withExecutionRepairActorID(valid, ""), kind: ErrorInvalidInput},
		{name: "long actor", input: withExecutionRepairActorID(valid, strings.Repeat("a", 201)), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkExecutionRepairInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkExecutionRepairPayloadBindsAuthorityAndActor(t *testing.T) {
	base := RepositoryExtractionWorkExecutionRepairInput{
		ExecutionRequestID: "execution-1",
		WorkItemID:         "repo-work:1",
		ClaimID:            "work-claim:1",
		WorkerID:           "worker-1",
		RepairActorID:      "controller-1",
	}
	baseHash, err := repositoryExtractionWorkExecutionRepairPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkExecutionRepairPayloadHash(base) error = %v", err)
	}
	for _, changed := range []RepositoryExtractionWorkExecutionRepairInput{
		withExecutionRepairExecutionRequestID(base, "execution-2"),
		withExecutionRepairWorkItemID(base, "repo-work:2"),
		withExecutionRepairClaimID(base, "work-claim:2"),
		withExecutionRepairWorkerID(base, "worker-2"),
		withExecutionRepairActorID(base, "controller-2"),
	} {
		changedHash, err := repositoryExtractionWorkExecutionRepairPayloadHash(changed)
		if err != nil {
			t.Fatalf("repositoryExtractionWorkExecutionRepairPayloadHash(changed) error = %v", err)
		}
		if changedHash == baseHash {
			t.Fatalf("changed execution repair payload hash = base hash %s", baseHash)
		}
	}
}

func TestRepairExpiredRepositoryExtractionWorkExecutionValidatesPoolAndTime(t *testing.T) {
	_, err := RepairExpiredRepositoryExtractionWorkExecution(t.Context(), nil, RepositoryExtractionWorkExecutionRepairInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = repairExpiredRepositoryExtractionWorkExecution(t.Context(), nil, RepositoryExtractionWorkExecutionRepairInput{
		RequestID:          "repair-1",
		ExecutionRequestID: "execution-1",
		WorkItemID:         "repo-work:1",
		ClaimID:            "work-claim:1",
		WorkerID:           "worker-1",
		RepairActorID:      "controller-1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func withExecutionRepairRequestID(input RepositoryExtractionWorkExecutionRepairInput, value string) RepositoryExtractionWorkExecutionRepairInput {
	input.RequestID = value
	return input
}

func withExecutionRepairExecutionRequestID(input RepositoryExtractionWorkExecutionRepairInput, value string) RepositoryExtractionWorkExecutionRepairInput {
	input.ExecutionRequestID = value
	return input
}

func withExecutionRepairWorkItemID(input RepositoryExtractionWorkExecutionRepairInput, value string) RepositoryExtractionWorkExecutionRepairInput {
	input.WorkItemID = value
	return input
}

func withExecutionRepairClaimID(input RepositoryExtractionWorkExecutionRepairInput, value string) RepositoryExtractionWorkExecutionRepairInput {
	input.ClaimID = value
	return input
}

func withExecutionRepairWorkerID(input RepositoryExtractionWorkExecutionRepairInput, value string) RepositoryExtractionWorkExecutionRepairInput {
	input.WorkerID = value
	return input
}

func withExecutionRepairActorID(input RepositoryExtractionWorkExecutionRepairInput, value string) RepositoryExtractionWorkExecutionRepairInput {
	input.RepairActorID = value
	return input
}
