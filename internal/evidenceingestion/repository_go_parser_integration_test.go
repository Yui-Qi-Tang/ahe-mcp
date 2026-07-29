//go:build integration

package evidenceingestion

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestIntegrationRunRepositoryGoParserPersistsOneMultiFileBatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/repository-parser\n\ngo 1.22\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Main() {}\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "worker", "worker.go"), []byte("package worker\n\ntype Worker struct{}\n\nfunc (Worker) Run() {}\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	commitSHA := gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")

	snapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "repository-parser",
		CommitSHA:     commitSHA,
		RequestID:     "repository-parser-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	request := RepositoryGoParserRequest{
		RequestID:            "repository-parser-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
	}
	result, err := RunRepositoryGoParserExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor() error = %v", err)
	}
	if result.Replayed || result.ProposalCount != 3 || len(result.ProposalOccurrenceIDs) != 3 {
		t.Fatalf("repository result = %+v", result)
	}
	if result.SourceGenerationReplayed || result.SourceGeneration.ID == "" || result.SourceGeneration.Number != 1 || result.SourceGeneration.RepositorySnapshotID != result.RepositorySnapshotID || result.SourceGeneration.ExtractionAttemptID != result.ExtractionAttemptID || result.SourceGeneration.ProposalBatchID != result.ProposalBatchID || result.SourceGeneration.ExtractorOutputHash != result.OutputHash || result.SourceGeneration.ProposalCount != result.ProposalCount {
		t.Fatalf("automatic repository generation = %+v, result = %+v", result.SourceGeneration, result)
	}
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 3)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)

	var repositorySnapshotID *string
	var sourceSnapshotID, extractionViewID *string
	if err := pool.QueryRow(ctx, `
		SELECT repository_snapshot_id, source_snapshot_id, extraction_view_id
		FROM extraction_runs
		WHERE extraction_run_id = $1
	`, result.ExtractionRunID).Scan(&repositorySnapshotID, &sourceSnapshotID, &extractionViewID); err != nil {
		t.Fatalf("reading repository extraction run: %v", err)
	}
	if repositorySnapshotID == nil || *repositorySnapshotID != snapshot.RepositorySnapshot.ID || sourceSnapshotID != nil || extractionViewID != nil {
		t.Fatalf("repository run binding = repository %v source %v view %v", repositorySnapshotID, sourceSnapshotID, extractionViewID)
	}

	var sourceRefsData []byte
	if err := pool.QueryRow(ctx, `
		SELECT source_refs
		FROM proposal_occurrences
		WHERE extraction_attempt_id = $1
		ORDER BY proposal_local_id
		LIMIT 1
	`, result.ExtractionAttemptID).Scan(&sourceRefsData); err != nil {
		t.Fatalf("reading repository source refs: %v", err)
	}
	var refs []ResolvedSourceRef
	if err := json.Unmarshal(sourceRefsData, &refs); err != nil {
		t.Fatalf("decoding repository source refs: %v", err)
	}
	if len(refs) == 0 || refs[0].TargetKind != "file_snapshot" || refs[0].RepositorySnapshotID != snapshot.RepositorySnapshot.ID || refs[0].FileSnapshotID == "" || refs[0].ExtractionViewID != "" {
		t.Fatalf("repository source refs = %+v", refs)
	}

	proposal, err := GetProposalByOccurrenceID(ctx, pool, result.ProposalOccurrenceIDs[0])
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID() error = %v", err)
	}
	if proposal.SourceBindingKind != ProposalSourceBindingRepositorySnapshot || proposal.RepositorySnapshot == nil {
		t.Fatalf("repository proposal binding = %+v", proposal)
	}
	if proposal.SourceGeneration == nil || *proposal.SourceGeneration != result.SourceGeneration || proposal.SourceGenerationActive {
		t.Fatalf("repository proposal generation = %+v active %t, want inactive %+v", proposal.SourceGeneration, proposal.SourceGenerationActive, result.SourceGeneration)
	}
	if *proposal.RepositorySnapshot != snapshot.RepositorySnapshot {
		t.Fatalf("query repository snapshot = %+v, want %+v", proposal.RepositorySnapshot, snapshot.RepositorySnapshot)
	}
	if proposal.SourceSnapshotID != "" || proposal.ExtractionViewID != "" || proposal.RendererName != "" {
		t.Fatalf("repository proposal fabricated source/view fields: %+v", proposal)
	}
	if proposal.CodeFact == nil || len(proposal.SourceRefs) == 0 || proposal.SourceRefs[0].FileSnapshotID == "" {
		t.Fatalf("repository proposal facts/refs = fact %+v refs %+v", proposal.CodeFact, proposal.SourceRefs)
	}

	listed, err := ListProposalRecords(ctx, pool, ProposalListInput{
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		SourceGenerationID:   result.SourceGeneration.ID,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("ListProposalRecords(repository) error = %v", err)
	}
	if len(listed) != result.ProposalCount {
		t.Fatalf("repository proposal list count = %d, want %d", len(listed), result.ProposalCount)
	}
	for _, record := range listed {
		if record.SourceGeneration == nil || *record.SourceGeneration != result.SourceGeneration || record.SourceGenerationActive {
			t.Fatalf("historical repository record generation = %+v active %t", record.SourceGeneration, record.SourceGenerationActive)
		}
	}
	legacyOnly, err := ListProposalRecords(ctx, pool, ProposalListInput{SourceSnapshotID: "srcsnap:missing", Limit: 10})
	if err != nil {
		t.Fatalf("ListProposalRecords(source snapshot) error = %v", err)
	}
	if len(legacyOnly) != 0 {
		t.Fatalf("source snapshot filter returned repository proposals: %+v", legacyOnly)
	}

	trace, err := TraceProposalProvenance(ctx, pool, proposal.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if trace.RepositorySnapshot == nil || trace.RepositorySnapshot.ID != snapshot.RepositorySnapshot.ID {
		t.Fatalf("repository trace = %+v", trace)
	}

	admission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           "repository-integration-test",
		DecisionReason:       "verified repository declaration accepted",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal() error = %v", err)
	}
	if admission.AdmissionOutcome != admissionOutcomeAdmitted || admission.CanonicalRef == "" || len(admission.RawEvidenceNodeIDs) == 0 {
		t.Fatalf("repository admission = %+v", admission)
	}
	canonical, err := GetCanonicalEvidenceByID(ctx, pool, admission.CanonicalRef)
	if err != nil {
		t.Fatalf("GetCanonicalEvidenceByID() error = %v", err)
	}
	if canonical.Payload.SourceType != SourceSystemCodeRepository || canonical.Provenance.OriginGroupID != snapshot.RepositorySnapshot.ID {
		t.Fatalf("repository canonical source/provenance = %+v / %+v", canonical.Payload, canonical.Provenance)
	}
	wantRef := proposal.SourceRefs[0]
	if !slices.Contains(canonical.Provenance.OriginRefs, snapshot.RepositorySnapshot.ID) || !slices.Contains(canonical.Provenance.OriginRefs, wantRef.FileSnapshotID) || !slices.Contains(canonical.Provenance.OriginRefs, spanLocator(wantRef)) {
		t.Fatalf("repository canonical origin refs = %v", canonical.Provenance.OriginRefs)
	}
	if canonical.OriginProposal.RepositorySnapshot == nil || canonical.OriginProposal.RepositorySnapshot.ID != snapshot.RepositorySnapshot.ID || canonical.OriginProposal.AdmissionOutcome != admissionOutcomeAdmitted {
		t.Fatalf("repository canonical origin proposal = %+v", canonical.OriginProposal)
	}

	replay, err := RunRepositoryGoParserExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("replay RunRepositoryGoParserExtractor() error = %v", err)
	}
	if !replay.Replayed || !replay.SourceGenerationReplayed || replay.ExtractionAttemptID != result.ExtractionAttemptID || replay.ProposalBatchID != result.ProposalBatchID || replay.ProposalCount != result.ProposalCount || replay.SourceGeneration != result.SourceGeneration {
		t.Fatalf("replay = %+v, want identifiers from %+v", replay, result)
	}
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 3)
	assertTableCount(t, ctx, pool, "repository_extraction_run_requests", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)

	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Next() {}\n"))
	runGitSnapshotTestCommand(t, root, "add", "main.go")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "next")
	nextSnapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "repository-parser",
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     "repository-parser-snapshot-next",
	})
	if err != nil {
		t.Fatalf("next CaptureGitRepositorySnapshot() error = %v", err)
	}
	_, err = RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            request.RequestID,
		RepositorySnapshotID: nextSnapshot.RepositorySnapshot.ID,
	})
	assertKind(t, err, ErrorIdempotencyKeyReused)
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_run_requests", 1)
}

func TestIntegrationRunRepositoryGoParserCreatesEmptyCandidateGeneration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/repository-parser-empty\n\ngo 1.22\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")

	snapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "repository-parser-empty",
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     "repository-parser-empty-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	if snapshot.RepositorySnapshot.SelectedFileCount != 0 {
		t.Fatalf("selected file count = %d, want 0", snapshot.RepositorySnapshot.SelectedFileCount)
	}

	request := RepositoryGoParserRequest{
		RequestID:            "repository-parser-empty-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
	}
	result, err := RunRepositoryGoParserExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor() error = %v", err)
	}
	if result.Replayed || result.ProposalCount != 0 || len(result.ProposalOccurrenceIDs) != 0 {
		t.Fatalf("empty repository result = %+v", result)
	}
	if result.SourceGenerationReplayed || result.SourceGeneration.ID == "" || result.SourceGeneration.Number != 1 || result.SourceGeneration.RepositorySnapshotID != result.RepositorySnapshotID || result.SourceGeneration.ExtractionAttemptID != result.ExtractionAttemptID || result.SourceGeneration.ProposalBatchID != result.ProposalBatchID || result.SourceGeneration.ExtractorOutputHash != result.OutputHash || result.SourceGeneration.ProposalCount != 0 {
		t.Fatalf("empty repository generation = %+v, result = %+v", result.SourceGeneration, result)
	}
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)

	replay, err := RunRepositoryGoParserExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("replay RunRepositoryGoParserExtractor() error = %v", err)
	}
	if !replay.Replayed || !replay.SourceGenerationReplayed || replay.SourceGeneration != result.SourceGeneration {
		t.Fatalf("empty repository replay = %+v, want generation from %+v", replay, result)
	}
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
}

func TestIntegrationRunRepositoryGoParserPersistsParseFailureWithoutBatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "broken.go"), []byte("package broken\n\nfunc Broken( {\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "broken")
	commitSHA := gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")
	snapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "repository-parser-broken",
		CommitSHA:     commitSHA,
		RequestID:     "repository-parser-broken-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	_, err = RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            "repository-parser-broken-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
	})
	assertKind(t, err, ErrorInvalidInput)
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertTableCount(t, ctx, pool, "repository_source_generations", 0)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	var status, failureClass string
	if err := pool.QueryRow(ctx, `SELECT status, failure_class FROM extraction_attempts`).Scan(&status, &failureClass); err != nil {
		t.Fatalf("reading failed repository attempt: %v", err)
	}
	if status != attemptStatusFailed || failureClass != string(ErrorInvalidInput) {
		t.Fatalf("failed attempt status/class = %s/%s", status, failureClass)
	}
}
