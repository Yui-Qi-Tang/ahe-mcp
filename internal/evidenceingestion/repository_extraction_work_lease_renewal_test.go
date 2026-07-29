package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkLeaseRenewalInput(t *testing.T) {
	valid := RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "renew-1",
		WorkItemID:                "repo-work:1",
		ClaimID:                   "work-claim:1",
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 60_000,
	}
	got, duration, err := validateRepositoryExtractionWorkLeaseRenewalInput(valid)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkLeaseRenewalInput() error = %v", err)
	}
	if got != valid || duration != time.Minute {
		t.Fatalf("validated input/duration = %+v/%s, want %+v/1m", got, duration, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkLeaseRenewalInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withLeaseRenewalRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withLeaseRenewalWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withLeaseRenewalClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing worker", input: withLeaseRenewalWorkerID(valid, ""), kind: ErrorInvalidInput},
		{name: "long worker", input: withLeaseRenewalWorkerID(valid, strings.Repeat("w", 201)), kind: ErrorInvalidInput},
		{name: "zero duration", input: withLeaseRenewalDuration(valid, 0), kind: ErrorInvalidInput},
		{name: "long duration", input: withLeaseRenewalDuration(valid, RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()+1), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := validateRepositoryExtractionWorkLeaseRenewalInput(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkLeaseRenewalPayloadBindsClaimWorkerAndDuration(t *testing.T) {
	base := RepositoryExtractionWorkLeaseRenewalInput{
		WorkItemID:                "repo-work:1",
		ClaimID:                   "work-claim:1",
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 60_000,
	}
	baseHash, err := repositoryExtractionWorkLeaseRenewalPayloadHash(base)
	if err != nil {
		t.Fatalf("repositoryExtractionWorkLeaseRenewalPayloadHash(base) error = %v", err)
	}
	for _, changed := range []RepositoryExtractionWorkLeaseRenewalInput{
		withLeaseRenewalWorkItemID(base, "repo-work:2"),
		withLeaseRenewalClaimID(base, "work-claim:2"),
		withLeaseRenewalWorkerID(base, "worker-2"),
		withLeaseRenewalDuration(base, 120_000),
	} {
		changedHash, err := repositoryExtractionWorkLeaseRenewalPayloadHash(changed)
		if err != nil {
			t.Fatalf("repositoryExtractionWorkLeaseRenewalPayloadHash(changed) error = %v", err)
		}
		if changedHash == baseHash {
			t.Fatalf("changed renewal payload hash = base hash %s", baseHash)
		}
	}
}

func TestRenewRepositoryExtractionWorkLeaseValidatesPoolAndTime(t *testing.T) {
	_, err := RenewRepositoryExtractionWorkLease(t.Context(), nil, RepositoryExtractionWorkLeaseRenewalInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = renewRepositoryExtractionWorkLease(t.Context(), nil, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "renew-1",
		WorkItemID:                "repo-work:1",
		ClaimID:                   "work-claim:1",
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 60_000,
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func withLeaseRenewalRequestID(input RepositoryExtractionWorkLeaseRenewalInput, value string) RepositoryExtractionWorkLeaseRenewalInput {
	input.RequestID = value
	return input
}

func withLeaseRenewalWorkItemID(input RepositoryExtractionWorkLeaseRenewalInput, value string) RepositoryExtractionWorkLeaseRenewalInput {
	input.WorkItemID = value
	return input
}

func withLeaseRenewalClaimID(input RepositoryExtractionWorkLeaseRenewalInput, value string) RepositoryExtractionWorkLeaseRenewalInput {
	input.ClaimID = value
	return input
}

func withLeaseRenewalWorkerID(input RepositoryExtractionWorkLeaseRenewalInput, value string) RepositoryExtractionWorkLeaseRenewalInput {
	input.WorkerID = value
	return input
}

func withLeaseRenewalDuration(input RepositoryExtractionWorkLeaseRenewalInput, value int64) RepositoryExtractionWorkLeaseRenewalInput {
	input.LeaseDurationMilliseconds = value
	return input
}
