package evidenceingestion

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPrepareRepositoryExtractionWorkExecution(t *testing.T) {
	root := t.TempDir()
	input := RepositoryExtractionWorkExecutionInput{
		RequestID:     " execute-1 ",
		WorkItemID:    " repo-work:1 ",
		ClaimID:       " work-claim:1 ",
		WorkerID:      " worker-1 ",
		WorkspaceRoot: " " + root + " ",
	}
	got, err := prepareRepositoryExtractionWorkExecution(input)
	if err != nil {
		t.Fatalf("prepareRepositoryExtractionWorkExecution() error = %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", root, err)
	}
	if got.input.RequestID != "execute-1" || got.input.WorkItemID != "repo-work:1" || got.input.ClaimID != "work-claim:1" || got.input.WorkerID != "worker-1" || got.input.WorkspaceRoot != wantRoot {
		t.Fatalf("prepared input = %+v", got.input)
	}
	if got.repositorySnapshotRequestID == "" || got.extractorRequestID == "" || got.finishRequestID == "" || got.requestPayloadHash == "" {
		t.Fatalf("prepared request identities = %+v", got)
	}
	if got.repositorySnapshotRequestID == got.extractorRequestID || got.repositorySnapshotRequestID == got.finishRequestID || got.extractorRequestID == got.finishRequestID {
		t.Fatalf("child request IDs are not distinct: %+v", got)
	}
	replay, err := prepareRepositoryExtractionWorkExecution(got.input)
	if err != nil {
		t.Fatalf("replay prepareRepositoryExtractionWorkExecution() error = %v", err)
	}
	if replay.repositorySnapshotRequestID != got.repositorySnapshotRequestID || replay.extractorRequestID != got.extractorRequestID || replay.finishRequestID != got.finishRequestID || replay.requestPayloadHash != got.requestPayloadHash {
		t.Fatalf("replayed prepared request = %+v, want %+v", replay, got)
	}
}

func TestPrepareRepositoryExtractionWorkExecutionValidatesAuthority(t *testing.T) {
	valid := RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-1",
		WorkItemID:    "repo-work:1",
		ClaimID:       "work-claim:1",
		WorkerID:      "worker-1",
		WorkspaceRoot: t.TempDir(),
	}
	tests := []struct {
		name  string
		input RepositoryExtractionWorkExecutionInput
		kind  ErrorKind
	}{
		{name: "missing request", input: withExecutionRequestID(valid, ""), kind: ErrorInvalidInput},
		{name: "bad work ID", input: withExecutionWorkItemID(valid, "work:1"), kind: ErrorInvalidRecordID},
		{name: "bad claim ID", input: withExecutionClaimID(valid, "claim:1"), kind: ErrorInvalidRecordID},
		{name: "missing worker", input: withExecutionWorkerID(valid, ""), kind: ErrorInvalidInput},
		{name: "long worker", input: withExecutionWorkerID(valid, strings.Repeat("w", 201)), kind: ErrorInvalidInput},
		{name: "missing workspace", input: withExecutionWorkspaceRoot(valid, ""), kind: ErrorInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := prepareRepositoryExtractionWorkExecution(tt.input)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestRepositoryExtractionWorkExecutionPayloadBindsClaimWorkerAndWorkspace(t *testing.T) {
	base, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-1",
		WorkItemID:    "repo-work:1",
		ClaimID:       "work-claim:1",
		WorkerID:      "worker-1",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare base execution request: %v", err)
	}
	for _, changedInput := range []RepositoryExtractionWorkExecutionInput{
		withExecutionWorkItemID(base.input, "repo-work:2"),
		withExecutionClaimID(base.input, "work-claim:2"),
		withExecutionWorkerID(base.input, "worker-2"),
		withExecutionWorkspaceRoot(base.input, t.TempDir()),
	} {
		changed, err := prepareRepositoryExtractionWorkExecution(changedInput)
		if err != nil {
			t.Fatalf("prepare changed execution request: %v", err)
		}
		if changed.requestPayloadHash == base.requestPayloadHash {
			t.Fatalf("changed execution payload hash = base hash %s", base.requestPayloadHash)
		}
		if changed.repositorySnapshotRequestID != base.repositorySnapshotRequestID || changed.extractorRequestID != base.extractorRequestID || changed.finishRequestID != base.finishRequestID {
			t.Fatalf("same parent request produced different child IDs: changed %+v base %+v", changed, base)
		}
	}
}

func TestExecuteClaimedRepositoryExtractionWorkValidatesPoolAndStartTime(t *testing.T) {
	_, err := ExecuteClaimedRepositoryExtractionWork(t.Context(), nil, RepositoryExtractionWorkExecutionInput{})
	assertKind(t, err, ErrorInvalidInput)

	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-1",
		WorkItemID:    "repo-work:1",
		ClaimID:       "work-claim:1",
		WorkerID:      "worker-1",
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare execution request: %v", err)
	}
	_, _, err = reserveRepositoryExtractionWorkExecution(t.Context(), nil, prepared, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}

func TestRepositoryExtractionWorkExecutionFailureMaterial(t *testing.T) {
	domainCause := fmt.Errorf(
		"capturing repository snapshot: %w",
		newDomainError(ErrorInvalidInput, "workspace does not contain the claimed revision"),
	)
	failureClass, failureMessage := repositoryExtractionWorkExecutionFailureMaterial(domainCause)
	if failureClass != string(ErrorInvalidInput) ||
		failureMessage != "workspace does not contain the claimed revision" {
		t.Fatalf("domain failure material = %q/%q", failureClass, failureMessage)
	}

	failureClass, failureMessage = repositoryExtractionWorkExecutionFailureMaterial(errors.New("storage unavailable"))
	if failureClass != string(ErrorRepositoryWorkExecutionFailed) ||
		failureMessage != "storage unavailable" {
		t.Fatalf("unclassified failure material = %q/%q", failureClass, failureMessage)
	}

	_, failureMessage = repositoryExtractionWorkExecutionFailureMaterial(
		errors.New(strings.Repeat("\u754c", repositoryExtractionWorkMaxFailureMessageBytes)),
	)
	if len(failureMessage) > repositoryExtractionWorkMaxFailureMessageBytes ||
		!utf8.ValidString(failureMessage) {
		t.Fatalf(
			"bounded failure message length/UTF-8 = %d/%t",
			len(failureMessage),
			utf8.ValidString(failureMessage),
		)
	}
}

func withExecutionRequestID(input RepositoryExtractionWorkExecutionInput, value string) RepositoryExtractionWorkExecutionInput {
	input.RequestID = value
	return input
}

func withExecutionWorkItemID(input RepositoryExtractionWorkExecutionInput, value string) RepositoryExtractionWorkExecutionInput {
	input.WorkItemID = value
	return input
}

func withExecutionClaimID(input RepositoryExtractionWorkExecutionInput, value string) RepositoryExtractionWorkExecutionInput {
	input.ClaimID = value
	return input
}

func withExecutionWorkerID(input RepositoryExtractionWorkExecutionInput, value string) RepositoryExtractionWorkExecutionInput {
	input.WorkerID = value
	return input
}

func withExecutionWorkspaceRoot(input RepositoryExtractionWorkExecutionInput, value string) RepositoryExtractionWorkExecutionInput {
	input.WorkspaceRoot = value
	return input
}
