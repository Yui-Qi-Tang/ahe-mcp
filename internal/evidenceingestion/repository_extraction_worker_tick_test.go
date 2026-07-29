package evidenceingestion

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareRepositoryExtractionWorkerTick(t *testing.T) {
	root := t.TempDir()
	input := RepositoryExtractionWorkerTickInput{
		RequestID:                 " tick-1 ",
		WorkspaceRoot:             " " + root + " ",
		RepoID:                    " ahe-wrap ",
		ExtractorName:             " " + ExtractorRepositoryGoParserCodeFact + " ",
		WorkerID:                  " worker-1 ",
		LeaseDurationMilliseconds: 60_000,
	}
	got, err := prepareRepositoryExtractionWorkerTick(input)
	if err != nil {
		t.Fatalf("prepareRepositoryExtractionWorkerTick() error = %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks(%q) error = %v", root, err)
	}
	if got.input.RequestID != "tick-1" || got.input.WorkspaceRoot != wantRoot || got.input.RepoID != "ahe-wrap" || got.input.ExtractorName != ExtractorRepositoryGoParserCodeFact || got.input.WorkerID != "worker-1" || got.input.LeaseDurationMilliseconds != 60_000 {
		t.Fatalf("prepared tick input = %+v", got.input)
	}
	if got.claimRequestID == "" || got.executionRequestID == "" || got.claimRequestID == got.executionRequestID || got.requestPayloadHash == "" {
		t.Fatalf("prepared tick identities = %+v", got)
	}
	replay, err := prepareRepositoryExtractionWorkerTick(got.input)
	if err != nil {
		t.Fatalf("replay prepareRepositoryExtractionWorkerTick() error = %v", err)
	}
	if replay.claimRequestID != got.claimRequestID || replay.executionRequestID != got.executionRequestID || replay.requestPayloadHash != got.requestPayloadHash {
		t.Fatalf("replayed prepared tick = %+v, want %+v", replay, got)
	}
}

func TestPrepareRepositoryExtractionWorkerTickValidatesBoundedInput(t *testing.T) {
	valid := RepositoryExtractionWorkerTickInput{
		RequestID:                 "tick-1",
		WorkspaceRoot:             t.TempDir(),
		RepoID:                    "ahe-wrap",
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 60_000,
	}
	tests := []struct {
		name  string
		input RepositoryExtractionWorkerTickInput
	}{
		{name: "missing request", input: withWorkerTickRequestID(valid, "")},
		{name: "missing workspace", input: withWorkerTickWorkspaceRoot(valid, "")},
		{name: "missing repository", input: withWorkerTickRepoID(valid, "")},
		{name: "unsupported extractor", input: withWorkerTickExtractorName(valid, "ollama-local")},
		{name: "missing worker", input: withWorkerTickWorkerID(valid, "")},
		{name: "long worker", input: withWorkerTickWorkerID(valid, strings.Repeat("w", 201))},
		{name: "short lease", input: withWorkerTickLeaseDuration(valid, 0)},
		{name: "long lease", input: withWorkerTickLeaseDuration(valid, RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := prepareRepositoryExtractionWorkerTick(tt.input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestRepositoryExtractionWorkerTickPayloadBindsWorkspaceAndStream(t *testing.T) {
	base, err := prepareRepositoryExtractionWorkerTick(RepositoryExtractionWorkerTickInput{
		RequestID:                 "tick-1",
		WorkspaceRoot:             t.TempDir(),
		RepoID:                    "ahe-wrap",
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 60_000,
	})
	if err != nil {
		t.Fatalf("prepare base worker tick: %v", err)
	}
	for _, changedInput := range []RepositoryExtractionWorkerTickInput{
		withWorkerTickWorkspaceRoot(base.input, t.TempDir()),
		withWorkerTickRepoID(base.input, "other-repo"),
		withWorkerTickExtractorName(base.input, ExtractorRepositoryGoplsCodeFact),
		withWorkerTickWorkerID(base.input, "worker-2"),
		withWorkerTickLeaseDuration(base.input, 120_000),
	} {
		changed, err := prepareRepositoryExtractionWorkerTick(changedInput)
		if err != nil {
			t.Fatalf("prepare changed worker tick: %v", err)
		}
		if changed.requestPayloadHash == base.requestPayloadHash {
			t.Fatalf("changed worker tick payload hash = base hash %s", base.requestPayloadHash)
		}
		if changed.claimRequestID != base.claimRequestID || changed.executionRequestID != base.executionRequestID {
			t.Fatalf("same parent request produced different child IDs: changed %+v base %+v", changed, base)
		}
	}
}

func TestValidateRepositoryExtractionWorkerTickClaim(t *testing.T) {
	request, err := prepareRepositoryExtractionWorkerTick(RepositoryExtractionWorkerTickInput{
		RequestID:                 "tick-1",
		WorkspaceRoot:             t.TempDir(),
		RepoID:                    "ahe-wrap",
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 60_000,
	})
	if err != nil {
		t.Fatalf("prepare worker tick: %v", err)
	}
	if err := validateRepositoryExtractionWorkerTickClaim(request, RepositoryExtractionWorkClaimResult{
		RequestID:     request.claimRequestID,
		RepoID:        request.input.RepoID,
		ExtractorName: request.input.ExtractorName,
		WorkerID:      request.input.WorkerID,
	}); err != nil {
		t.Fatalf("validate no-work claim: %v", err)
	}

	claimedAt := time.Now().UTC()
	leaseExpiresAt := claimedAt.Add(time.Minute)
	work := RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        request.input.RepoID,
		ExtractorName: request.input.ExtractorName,
	}
	claim := RepositoryExtractionWorkClaimResult{
		RequestID:      request.claimRequestID,
		RepoID:         request.input.RepoID,
		ExtractorName:  request.input.ExtractorName,
		WorkerID:       request.input.WorkerID,
		Claimed:        true,
		Work:           &work,
		ClaimID:        "work-claim:1",
		AttemptNumber:  1,
		ClaimedAt:      &claimedAt,
		LeaseExpiresAt: &leaseExpiresAt,
	}
	if err := validateRepositoryExtractionWorkerTickClaim(request, claim); err != nil {
		t.Fatalf("validate claimed work: %v", err)
	}
	claim.AttemptNumber = 0
	assertKind(t, validateRepositoryExtractionWorkerTickClaim(request, claim), ErrorRepositoryWorkConflict)
}

func TestRunRepositoryExtractionWorkerTickRequiresPool(t *testing.T) {
	_, err := RunRepositoryExtractionWorkerTick(t.Context(), nil, RepositoryExtractionWorkerTickInput{})
	assertKind(t, err, ErrorInvalidInput)
}

func withWorkerTickRequestID(input RepositoryExtractionWorkerTickInput, value string) RepositoryExtractionWorkerTickInput {
	input.RequestID = value
	return input
}

func withWorkerTickWorkspaceRoot(input RepositoryExtractionWorkerTickInput, value string) RepositoryExtractionWorkerTickInput {
	input.WorkspaceRoot = value
	return input
}

func withWorkerTickRepoID(input RepositoryExtractionWorkerTickInput, value string) RepositoryExtractionWorkerTickInput {
	input.RepoID = value
	return input
}

func withWorkerTickExtractorName(input RepositoryExtractionWorkerTickInput, value string) RepositoryExtractionWorkerTickInput {
	input.ExtractorName = value
	return input
}

func withWorkerTickWorkerID(input RepositoryExtractionWorkerTickInput, value string) RepositoryExtractionWorkerTickInput {
	input.WorkerID = value
	return input
}

func withWorkerTickLeaseDuration(input RepositoryExtractionWorkerTickInput, value int64) RepositoryExtractionWorkerTickInput {
	input.LeaseDurationMilliseconds = value
	return input
}
