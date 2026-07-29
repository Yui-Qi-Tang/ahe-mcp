//go:build integration

package evidenceingestion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRepositoryGoParserDeltaFallsBackWithoutBaseAndConverges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := repositoryDeltaGitFixture(t, map[string]string{
		"main.go": "package sample\n\nfunc Main() {}\n",
	})
	snapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "delta-no-base", "delta-no-base-snapshot")
	request := RepositoryGoParserRequest{
		RequestID:            "delta-no-base-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
	}

	const concurrentExecutions = 16
	results := make([]RepositoryIngestResult, concurrentExecutions)
	errs := make([]error, concurrentExecutions)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = RunRepositoryGoParserDeltaExtractor(ctx, pool, request)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("RunRepositoryGoParserDeltaExtractor() result %d error = %v", i, err)
		}
	}
	for i, result := range results {
		if result.DeltaExtraction == nil || result.DeltaExtraction.Decision != RepositoryDeltaDecisionFullFallback || result.DeltaExtraction.FallbackReason != RepositoryDeltaFallbackNoBaseGeneration {
			t.Fatalf("result %d delta audit = %+v", i, result.DeltaExtraction)
		}
		if result.DeltaExtraction.FullVerifiedFileCount != 1 || result.DeltaExtraction.AddedFileCount != 1 || result.DeltaExtraction.DeltaParsedFileCount != 0 {
			t.Fatalf("result %d delta counts = %+v", i, result.DeltaExtraction)
		}
	}
	if results[0].ExtractionAttemptID != results[1].ExtractionAttemptID || results[0].ProposalBatchID != results[1].ProposalBatchID || results[0].SourceGeneration.ID != results[1].SourceGeneration.ID || results[0].DeltaExtraction.ID != results[1].DeltaExtraction.ID {
		t.Fatalf("overlapping delta execution did not converge: first %+v second %+v", results[0], results[1])
	}
	assertTableCount(t, ctx, pool, "repository_delta_extractions", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)

	replay, err := RunRepositoryGoParserDeltaExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("replay RunRepositoryGoParserDeltaExtractor() error = %v", err)
	}
	if !replay.Replayed || replay.DeltaExtraction == nil || !replay.DeltaExtraction.Replayed || replay.DeltaExtraction.ID != results[0].DeltaExtraction.ID {
		t.Fatalf("delta replay = %+v", replay)
	}
}

func TestIntegrationRepositoryGoParserDeltaVerifiesAddedModifiedDeletedAndUnchangedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := repositoryDeltaGitFixture(t, map[string]string{
		"deleted.go":  "package sample\n\nfunc Deleted() {}\n",
		"modified.go": "package sample\n\nfunc Before() {}\n",
		"same.go":     "package sample\n\ntype Stable struct{}\n",
	})
	baseSnapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "delta-verified", "delta-verified-base-snapshot")
	base, err := RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            "delta-verified-base-run",
		RepositorySnapshotID: baseSnapshot.RepositorySnapshot.ID,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor(base) error = %v", err)
	}

	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatalf("remove deleted.go: %v", err)
	}
	writeGitSnapshotTestFile(t, filepath.Join(root, "modified.go"), []byte("package sample\n\nfunc After() {}\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "added.go"), []byte("package sample\n\nfunc Added() {}\n"))
	runGitSnapshotTestCommand(t, root, "add", "-A")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "delta")
	currentSnapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "delta-verified", "delta-verified-current-snapshot")
	request := RepositoryGoParserRequest{
		RequestID:            "delta-verified-current-run",
		RepositorySnapshotID: currentSnapshot.RepositorySnapshot.ID,
	}
	result, err := RunRepositoryGoParserDeltaExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("RunRepositoryGoParserDeltaExtractor() error = %v", err)
	}
	delta := result.DeltaExtraction
	if delta == nil || delta.Decision != RepositoryDeltaDecisionVerified || delta.FallbackReason != "" || delta.BaseSourceGenerationID != base.SourceGeneration.ID || delta.BaseRepositorySnapshotID != baseSnapshot.RepositorySnapshot.ID {
		t.Fatalf("verified delta audit = %+v, base = %+v", delta, base.SourceGeneration)
	}
	if delta.AddedFileCount != 1 || delta.ModifiedFileCount != 1 || delta.DeletedFileCount != 1 || delta.UnchangedFileCount != 1 || delta.DeltaParsedFileCount != 2 || delta.FullVerifiedFileCount != 3 {
		t.Fatalf("verified delta counts = %+v", delta)
	}
	if delta.CandidateOutputHash != result.OutputHash || delta.FullOutputHash != result.OutputHash || delta.SelectedOutputHash != result.OutputHash {
		t.Fatalf("verified delta hashes = %+v, result output = %s", delta, result.OutputHash)
	}
	currentInput, err := BuildRepositoryExtractorInput(ctx, pool, currentSnapshot.RepositorySnapshot.ID)
	if err != nil {
		t.Fatalf("BuildRepositoryExtractorInput() error = %v", err)
	}
	fullOutput, err := extractRepositoryGoParser(ctx, currentInput)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser(full verification) error = %v", err)
	}
	fullHash, err := extractorOutputHash(fullOutput)
	if err != nil {
		t.Fatalf("extractorOutputHash(full verification) error = %v", err)
	}
	if result.OutputHash != fullHash {
		t.Fatalf("selected output hash = %s, want full %s", result.OutputHash, fullHash)
	}
	assertTableCount(t, ctx, pool, "repository_delta_extractions", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 2)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	assertTableCount(t, ctx, pool, "admission_decisions", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)

	replay, err := RunRepositoryGoParserDeltaExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("replay RunRepositoryGoParserDeltaExtractor() error = %v", err)
	}
	if !replay.Replayed || replay.DeltaExtraction == nil || !replay.DeltaExtraction.Replayed {
		t.Fatalf("verified delta replay = %+v, want %+v", replay.DeltaExtraction, delta)
	}
	replayedDelta := *replay.DeltaExtraction
	replayedDelta.Replayed = false
	if replayedDelta != *delta {
		t.Fatalf("verified delta replay = %+v, want %+v", replay.DeltaExtraction, delta)
	}
}

func TestIntegrationRepositoryGoParserDeltaFallsBackFromTamperedBaseAndRejectsAuditTamper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := repositoryDeltaGitFixture(t, map[string]string{
		"main.go": "package sample\n\nfunc Main() {}\n",
	})
	baseSnapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "delta-tamper", "delta-tamper-base-snapshot")
	base, err := RunRepositoryGoParserExtractor(ctx, pool, RepositoryGoParserRequest{
		RequestID:            "delta-tamper-base-run",
		RepositorySnapshotID: baseSnapshot.RepositorySnapshot.ID,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor(base) error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extraction_attempts
		SET fixture_output = jsonb_set(
			fixture_output,
			'{proposals,0,statement_text}',
			to_jsonb('tampered base statement'::text)
		)
		WHERE extraction_attempt_id = $1
	`, base.ExtractionAttemptID); err != nil {
		t.Fatalf("tamper base fixture output: %v", err)
	}

	writeGitSnapshotTestFile(t, filepath.Join(root, "README.md"), []byte("new manifest entry\n"))
	runGitSnapshotTestCommand(t, root, "add", "README.md")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "manifest-only")
	currentSnapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "delta-tamper", "delta-tamper-current-snapshot")
	request := RepositoryGoParserRequest{
		RequestID:            "delta-tamper-current-run",
		RepositorySnapshotID: currentSnapshot.RepositorySnapshot.ID,
	}
	result, err := RunRepositoryGoParserDeltaExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("RunRepositoryGoParserDeltaExtractor() error = %v", err)
	}
	if result.DeltaExtraction == nil || result.DeltaExtraction.Decision != RepositoryDeltaDecisionFullFallback || result.DeltaExtraction.FallbackReason != RepositoryDeltaFallbackBaseAuthorityInvalid || result.DeltaExtraction.BaseSourceGenerationID != base.SourceGeneration.ID {
		t.Fatalf("tampered-base fallback = %+v", result.DeltaExtraction)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE repository_delta_extractions
		SET decision = 'delta_verified',
			fallback_reason = NULL,
			candidate_output_hash = full_output_hash
		WHERE delta_extraction_id = $1
	`, result.DeltaExtraction.ID); err == nil {
		t.Fatal("migration accepted verified delta without base authority")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE repository_delta_extractions
		SET base_source_generation_id = $2,
			base_repository_snapshot_id = $3
		WHERE delta_extraction_id = $1
	`, result.DeltaExtraction.ID, result.SourceGeneration.ID, currentSnapshot.RepositorySnapshot.ID); err == nil {
		t.Fatal("migration accepted the current snapshot as its own delta base")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE repository_delta_extractions
		SET revision_proof_hash = $2
		WHERE delta_extraction_id = $1
	`, result.DeltaExtraction.ID, contentHash([]byte("tampered proof"))); err != nil {
		t.Fatalf("tamper delta proof hash: %v", err)
	}
	_, err = RunRepositoryGoParserDeltaExtractor(ctx, pool, request)
	assertKind(t, err, ErrorRepositorySnapshotIntegrity)
}

func TestIntegrationRepositoryWorkExecutionUsesVerifiedParserDelta(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := repositoryDeltaGitFixture(t, map[string]string{
		"main.go": "package sample\n\nfunc Before() {}\n",
		"same.go": "package sample\n\ntype Stable struct{}\n",
	})
	repoID := "delta-work-execution"
	baseCommit := gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")
	_, baseObservation := persistStableWorkObservation(t, ctx, pool, repoID, baseCommit, "delta-work-base", time.Now().UTC().Add(-time.Second), false)
	baseScheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "delta-work-base-schedule", baseObservation.RequestID, ExtractorRepositoryGoParserCodeFact)
	baseClaim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "delta-work-base-claim", repoID, ExtractorRepositoryGoParserCodeFact, "delta-work-worker")
	base, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkExecutionInput{
		RequestID:     "delta-work-base-execute",
		WorkItemID:    baseScheduled.Work.WorkItemID,
		ClaimID:       baseClaim.ClaimID,
		WorkerID:      baseClaim.WorkerID,
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("ExecuteClaimedRepositoryExtractionWork(base) error = %v", err)
	}
	if base.Extraction.DeltaExtraction == nil || base.Extraction.DeltaExtraction.Decision != RepositoryDeltaDecisionFullFallback {
		t.Fatalf("base work delta audit = %+v", base.Extraction.DeltaExtraction)
	}

	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc After() {}\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "added.go"), []byte("package sample\n\nfunc Added() {}\n"))
	runGitSnapshotTestCommand(t, root, "add", "-A")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "delta-work")
	currentCommit := gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")
	_, currentObservation := persistStableWorkObservation(t, ctx, pool, repoID, currentCommit, "delta-work-current", time.Now().UTC().Add(-time.Second), false)
	currentScheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "delta-work-current-schedule", currentObservation.RequestID, ExtractorRepositoryGoParserCodeFact)
	currentClaim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "delta-work-current-claim", repoID, ExtractorRepositoryGoParserCodeFact, "delta-work-worker")
	current, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkExecutionInput{
		RequestID:     "delta-work-current-execute",
		WorkItemID:    currentScheduled.Work.WorkItemID,
		ClaimID:       currentClaim.ClaimID,
		WorkerID:      currentClaim.WorkerID,
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("ExecuteClaimedRepositoryExtractionWork(current) error = %v", err)
	}
	delta := current.Extraction.DeltaExtraction
	if delta == nil || delta.Decision != RepositoryDeltaDecisionVerified || delta.BaseSourceGenerationID != base.Extraction.SourceGeneration.ID || delta.AddedFileCount != 1 || delta.ModifiedFileCount != 1 || delta.UnchangedFileCount != 1 || delta.DeletedFileCount != 0 || delta.DeltaParsedFileCount != 2 || delta.FullVerifiedFileCount != 3 {
		t.Fatalf("current work delta audit = %+v, base generation = %s", delta, base.Extraction.SourceGeneration.ID)
	}
	if current.Finish.SourceGenerationID != current.Extraction.SourceGeneration.ID || current.Finish.Outcome != RepositoryExtractionWorkOutcomeSucceeded {
		t.Fatalf("current work finish = %+v, extraction = %+v", current.Finish, current.Extraction)
	}
	assertTableCount(t, ctx, pool, "repository_delta_extractions", 2)
	assertTableCount(t, ctx, pool, "repository_source_generations", 2)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
}

func repositoryDeltaGitFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/repository-delta\n\ngo 1.24\n"))
	for path, content := range files {
		writeGitSnapshotTestFile(t, filepath.Join(root, path), []byte(content))
	}
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	return root
}

func captureRepositoryDeltaSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, root, repoID, requestID string) RepositorySnapshotCaptureResult {
	t.Helper()
	result, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        repoID,
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     requestID,
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	return result
}
