//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRepositoryExtractionWorkFinishLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/work-finish\n\ngo 1.22\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc First() {}\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "first")
	_, firstRun := captureAndRunRepositoryGenerationFixture(t, ctx, pool, root, "work-finish-first")

	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Second() {}\n"))
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "second")
	_, secondRun := captureAndRunRepositoryGenerationFixture(t, ctx, pool, root, "work-finish-second")

	base := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	_, stableFirst := persistStableWorkObservation(t, ctx, pool, firstRun.SourceGeneration.RepoID, firstRun.SourceGeneration.CommitSHA, "finish-first", base, false)
	scheduledFirst := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-finish-first", stableFirst.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimFirstAt := base.Add(10 * time.Millisecond)
	claimedFirst, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-finish-first",
		RepoID:                    firstRun.SourceGeneration.RepoID,
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-first",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimFirstAt)
	if err != nil {
		t.Fatalf("claim first work: %v", err)
	}

	successInput := RepositoryExtractionWorkFinishInput{
		RequestID:          "finish-success",
		WorkItemID:         scheduledFirst.Work.WorkItemID,
		ClaimID:            claimedFirst.ClaimID,
		WorkerID:           claimedFirst.WorkerID,
		Outcome:            RepositoryExtractionWorkOutcomeSucceeded,
		SourceGenerationID: firstRun.SourceGeneration.ID,
	}
	wrongWorker := successInput
	wrongWorker.RequestID = "finish-wrong-worker"
	wrongWorker.WorkerID = "other-worker"
	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, wrongWorker, claimFirstAt.Add(time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	wrongGeneration := successInput
	wrongGeneration.RequestID = "finish-wrong-generation"
	wrongGeneration.SourceGenerationID = secondRun.SourceGeneration.ID
	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, wrongGeneration, claimFirstAt.Add(2*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	finishedFirstAt := claimFirstAt.Add(3 * time.Millisecond)
	finishedFirst, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, successInput, finishedFirstAt)
	if err != nil {
		t.Fatalf("finish first work: %v", err)
	}
	if finishedFirst.Replayed || finishedFirst.Outcome != RepositoryExtractionWorkOutcomeSucceeded || finishedFirst.Work != scheduledFirst.Work || finishedFirst.SourceGenerationID != firstRun.SourceGeneration.ID || finishedFirst.AttemptNumber != claimedFirst.AttemptNumber || !finishedFirst.LeaseExpiresAt.Equal(*claimedFirst.LeaseExpiresAt) || !finishedFirst.FinishedAt.Equal(finishedFirstAt) {
		t.Fatalf("finished first work = %+v", finishedFirst)
	}
	finishedFirstReplay, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, successInput, finishedFirstAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay first finish: %v", err)
	}
	if !finishedFirstReplay.Replayed || !sameRepositoryExtractionFinishResult(finishedFirstReplay, finishedFirst) {
		t.Fatalf("finished first replay = %+v, first = %+v", finishedFirstReplay, finishedFirst)
	}
	reusedRequest := successInput
	reusedRequest.Outcome = RepositoryExtractionWorkOutcomeFailed
	reusedRequest.SourceGenerationID = ""
	reusedRequest.FailureClass = "runner_invocation_failed"
	reusedRequest.FailureMessage = "late failure"
	_, err = FinishRepositoryExtractionWork(ctx, pool, reusedRequest)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	secondFinish := successInput
	secondFinish.RequestID = "finish-success-again"
	_, err = FinishRepositoryExtractionWork(ctx, pool, secondFinish)
	assertKind(t, err, ErrorRepositoryWorkConflict)

	_, stableSecond := persistStableWorkObservation(t, ctx, pool, secondRun.SourceGeneration.RepoID, secondRun.SourceGeneration.CommitSHA, "finish-second", base.Add(20*time.Millisecond), false)
	scheduledSecond := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-finish-second", stableSecond.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimSecondAt := base.Add(30 * time.Millisecond)
	claimedSecond, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-finish-second",
		RepoID:                    secondRun.SourceGeneration.RepoID,
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-second",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimSecondAt)
	if err != nil {
		t.Fatalf("claim second work: %v", err)
	}
	failureInput := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-failure",
		WorkItemID:     scheduledSecond.Work.WorkItemID,
		ClaimID:        claimedSecond.ClaimID,
		WorkerID:       claimedSecond.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "gopls exited before initialization",
	}
	finishedSecondAt := claimSecondAt.Add(time.Millisecond)
	finishedSecond, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, failureInput, finishedSecondAt)
	if err != nil {
		t.Fatalf("finish second work: %v", err)
	}
	if finishedSecond.Outcome != RepositoryExtractionWorkOutcomeFailed || finishedSecond.Work != scheduledSecond.Work || finishedSecond.SourceGenerationID != "" || finishedSecond.FailureClass != failureInput.FailureClass || finishedSecond.FailureMessage != failureInput.FailureMessage || finishedSecond.AttemptNumber != claimedSecond.AttemptNumber || !finishedSecond.LeaseExpiresAt.Equal(*claimedSecond.LeaseExpiresAt) || !finishedSecond.FinishedAt.Equal(finishedSecondAt) {
		t.Fatalf("finished second work = %+v", finishedSecond)
	}
	finishedSecondReplay, err := FinishRepositoryExtractionWork(ctx, pool, failureInput)
	if err != nil {
		t.Fatalf("replay second finish: %v", err)
	}
	if !finishedSecondReplay.Replayed || !sameRepositoryExtractionFinishResult(finishedSecondReplay, finishedSecond) {
		t.Fatalf("finished second replay = %+v, first = %+v", finishedSecondReplay, finishedSecond)
	}

	coalescedFailed := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-failed-again", stableSecond.RequestID, ExtractorRepositoryGoParserCodeFact)
	if !coalescedFailed.Coalesced || coalescedFailed.Created || coalescedFailed.Work != scheduledSecond.Work {
		t.Fatalf("failed work reschedule = %+v", coalescedFailed)
	}
	noWork := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-after-terminal", secondRun.SourceGeneration.RepoID, ExtractorRepositoryGoParserCodeFact, "worker-third")
	if noWork.Claimed {
		t.Fatalf("claim after terminal work = %+v", noWork)
	}

	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, scheduledFirst.Work.WorkItemID, RepositoryExtractionWorkOutcomeSucceeded, firstRun.SourceGeneration.ID, "", "")
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, scheduledSecond.Work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", failureInput.FailureClass, failureInput.FailureMessage)
	assertTableCount(t, ctx, pool, "repository_extraction_work_items", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_schedule_requests", 3)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 3)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 2)
	assertTableCount(t, ctx, pool, "repository_source_generations", 2)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
}

func TestIntegrationRepositoryExtractionWorkFinishRejectsExpiredLease(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "finish-expired", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "finish-expired", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-finish-expired", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claimed, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-finish-expired",
		RepoID:                    "finish-expired",
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-expired",
		LeaseDurationMilliseconds: RepositoryExtractionWorkMinLeaseDuration.Milliseconds(),
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim expiring work: %v", err)
	}
	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-expired",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        claimed.ClaimID,
		WorkerID:       claimed.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_timeout",
		FailureMessage: "worker returned at the lease boundary",
	}, claimedAt.Add(RepositoryExtractionWorkMinLeaseDuration))
	assertKind(t, err, ErrorRepositoryWorkLeaseExpired)

	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claimed)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 0)
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status
		FROM repository_extraction_work_items
		WHERE work_item_id = $1
	`, scheduled.Work.WorkItemID).Scan(&status); err != nil {
		t.Fatalf("reading expired work state: %v", err)
	}
	if status != repositoryExtractionWorkRunning {
		t.Fatalf("expired work status = %s, want running", status)
	}
}

func TestIntegrationRepositoryExtractionWorkConcurrentFinishHasOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	_, stable := persistStableWorkObservation(t, ctx, pool, "finish-concurrent", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "finish-concurrent", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-finish-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimed, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-finish-concurrent",
		RepoID:                    "finish-concurrent",
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-concurrent",
		LeaseDurationMilliseconds: RepositoryExtractionWorkMaxLeaseDuration.Milliseconds(),
	}, base.Add(10*time.Millisecond))
	if err != nil {
		t.Fatalf("claim concurrent finish work: %v", err)
	}

	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(index int) {
			_, err := FinishRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkFinishInput{
				RequestID:      fmt.Sprintf("finish-concurrent-%d", index),
				WorkItemID:     scheduled.Work.WorkItemID,
				ClaimID:        claimed.ClaimID,
				WorkerID:       claimed.WorkerID,
				Outcome:        RepositoryExtractionWorkOutcomeFailed,
				FailureClass:   "runner_invocation_failed",
				FailureMessage: fmt.Sprintf("failure %d", index),
			})
			errs <- err
		}(i)
	}
	succeeded := 0
	conflicted := 0
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case isKind(err, ErrorRepositoryWorkConflict):
			conflicted++
		default:
			t.Fatalf("concurrent finish error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent finish succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
}

func sameRepositoryExtractionFinishResult(left, right RepositoryExtractionWorkFinishResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left == right
}

func assertRepositoryExtractionWorkTerminalState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItemID, outcome, generationID, failureClass, failureMessage string) {
	t.Helper()
	var gotOutcome, gotGenerationID, gotFailureClass, gotFailureMessage string
	var finishedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status,
			COALESCE(source_generation_id, ''),
			COALESCE(failure_class, ''),
			COALESCE(failure_message, ''),
			finished_at
		FROM repository_extraction_work_items
		WHERE work_item_id = $1
	`, workItemID).Scan(&gotOutcome, &gotGenerationID, &gotFailureClass, &gotFailureMessage, &finishedAt); err != nil {
		t.Fatalf("reading terminal work %s: %v", workItemID, err)
	}
	if gotOutcome != outcome || gotGenerationID != generationID || gotFailureClass != failureClass || gotFailureMessage != failureMessage || finishedAt == nil {
		t.Fatalf("terminal work %s = %s/%s/%s/%s/%v, want %s/%s/%s/%s/non-nil", workItemID, gotOutcome, gotGenerationID, gotFailureClass, gotFailureMessage, finishedAt, outcome, generationID, failureClass, failureMessage)
	}
	var attemptOutcome, attemptGenerationID, attemptFailureClass, attemptFailureMessage string
	var attemptFinishedAt, leaseExpiresAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT a.status,
			COALESCE(a.source_generation_id, ''),
			COALESCE(a.failure_class, ''),
			COALESCE(a.failure_message, ''),
			a.finished_at,
			a.lease_expires_at
		FROM repository_extraction_work_claim_attempts a
		JOIN repository_extraction_work_items w ON w.claim_id = a.claim_id
		WHERE w.work_item_id = $1
	`, workItemID).Scan(&attemptOutcome, &attemptGenerationID, &attemptFailureClass, &attemptFailureMessage, &attemptFinishedAt, &leaseExpiresAt); err != nil {
		t.Fatalf("reading terminal claim attempt for %s: %v", workItemID, err)
	}
	if attemptOutcome != outcome || attemptGenerationID != generationID || attemptFailureClass != failureClass || attemptFailureMessage != failureMessage || !attemptFinishedAt.Equal(*finishedAt) || !attemptFinishedAt.Before(leaseExpiresAt) {
		t.Fatalf("terminal claim attempt %s = %s/%s/%s/%s/%s/%s", workItemID, attemptOutcome, attemptGenerationID, attemptFailureClass, attemptFailureMessage, attemptFinishedAt, leaseExpiresAt)
	}
}

func isKind(err error, want ErrorKind) bool {
	got, ok := KindOf(err)
	return ok && got == want
}
