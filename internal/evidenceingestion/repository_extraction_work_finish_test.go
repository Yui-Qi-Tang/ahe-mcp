package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkFinishInput(t *testing.T) {
	success := RepositoryExtractionWorkFinishInput{
		RequestID:          "finish-1",
		WorkItemID:         "repo-work:1",
		ClaimID:            "work-claim:1",
		WorkerID:           "worker-1",
		Outcome:            RepositoryExtractionWorkOutcomeSucceeded,
		SourceGenerationID: "generation:1",
	}
	failure := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-2",
		WorkItemID:     "repo-work:2",
		ClaimID:        "work-claim:2",
		WorkerID:       "worker-2",
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "gopls exited before initialization",
	}

	for _, input := range []RepositoryExtractionWorkFinishInput{success, failure} {
		got, err := validateRepositoryExtractionWorkFinishInput(input)
		if err != nil {
			t.Fatalf("validateRepositoryExtractionWorkFinishInput(%s) error = %v", input.Outcome, err)
		}
		if got != input {
			t.Fatalf("validated input = %+v, want %+v", got, input)
		}
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkFinishInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withFinishRequestID(success, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withFinishWorkItemID(success, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withFinishClaimID(success, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing worker", input: withFinishWorkerID(success, ""), kind: ErrorInvalidInput},
		{name: "long worker", input: withFinishWorkerID(success, strings.Repeat("w", 201)), kind: ErrorInvalidInput},
		{name: "unknown outcome", input: withFinishOutcome(success, "cancelled"), kind: ErrorInvalidInput},
		{name: "success without generation", input: withFinishGenerationID(success, ""), kind: ErrorInvalidInput},
		{name: "success with bad generation", input: withFinishGenerationID(success, "source-generation:1"), kind: ErrorInvalidRecordID},
		{name: "success with failure class", input: withFinishFailure(success, "failed", "message"), kind: ErrorInvalidInput},
		{name: "failure with generation", input: withFinishGenerationID(failure, "generation:1"), kind: ErrorInvalidInput},
		{name: "failure without class", input: withFinishFailure(failure, "", failure.FailureMessage), kind: ErrorInvalidInput},
		{name: "failure without message", input: withFinishFailure(failure, failure.FailureClass, ""), kind: ErrorInvalidInput},
		{name: "long failure class", input: withFinishFailure(failure, strings.Repeat("f", repositoryExtractionWorkMaxFailureClassBytes+1), failure.FailureMessage), kind: ErrorInvalidInput},
		{name: "long failure message", input: withFinishFailure(failure, failure.FailureClass, strings.Repeat("m", repositoryExtractionWorkMaxFailureMessageBytes+1)), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkFinishInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkFinishPayloadBindsOutcomeAndDiagnostics(t *testing.T) {
	base := RepositoryExtractionWorkFinishInput{
		WorkItemID:     "repo-work:1",
		ClaimID:        "work-claim:1",
		WorkerID:       "worker-1",
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "first failure",
	}
	first, err := repositoryExtractionWorkFinishPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkFinishPayloadHash(first) error = %v", err)
	}
	second, err := repositoryExtractionWorkFinishPayloadHash(withFinishFailure(base, base.FailureClass, "second failure"))
	if err != nil {
		t.Fatalf("repositoryExtractionWorkFinishPayloadHash(second) error = %v", err)
	}
	if first == second {
		t.Fatalf("finish payload hashes are equal: %s", first)
	}
}

func TestFinishRepositoryExtractionWorkValidatesPoolAndTime(t *testing.T) {
	_, err := FinishRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkFinishInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = finishRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkFinishInput{
		RequestID:          "finish-1",
		WorkItemID:         "repo-work:1",
		ClaimID:            "work-claim:1",
		WorkerID:           "worker-1",
		Outcome:            RepositoryExtractionWorkOutcomeSucceeded,
		SourceGenerationID: "generation:1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func withFinishRequestID(input RepositoryExtractionWorkFinishInput, value string) RepositoryExtractionWorkFinishInput {
	input.RequestID = value
	return input
}

func withFinishWorkItemID(input RepositoryExtractionWorkFinishInput, value string) RepositoryExtractionWorkFinishInput {
	input.WorkItemID = value
	return input
}

func withFinishClaimID(input RepositoryExtractionWorkFinishInput, value string) RepositoryExtractionWorkFinishInput {
	input.ClaimID = value
	return input
}

func withFinishWorkerID(input RepositoryExtractionWorkFinishInput, value string) RepositoryExtractionWorkFinishInput {
	input.WorkerID = value
	return input
}

func withFinishOutcome(input RepositoryExtractionWorkFinishInput, value string) RepositoryExtractionWorkFinishInput {
	input.Outcome = value
	return input
}

func withFinishGenerationID(input RepositoryExtractionWorkFinishInput, value string) RepositoryExtractionWorkFinishInput {
	input.SourceGenerationID = value
	return input
}

func withFinishFailure(input RepositoryExtractionWorkFinishInput, class, message string) RepositoryExtractionWorkFinishInput {
	input.FailureClass = class
	input.FailureMessage = message
	return input
}
