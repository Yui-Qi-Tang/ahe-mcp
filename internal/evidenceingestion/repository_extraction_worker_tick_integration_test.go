//go:build integration

package evidenceingestion

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRunRepositoryExtractionWorkerTickSucceedsReplaysAndRecordsNoWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/worker-tick", "package sample\n\nfunc WorkerTick() {}\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "worker-tick", commitSHA, "worker-tick", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-worker-tick", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	input := RepositoryExtractionWorkerTickInput{
		RequestID:                 "worker-tick-1",
		WorkspaceRoot:             root,
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-tick-1",
		LeaseDurationMilliseconds: 60_000,
	}
	prepared, err := prepareRepositoryExtractionWorkerTick(input)
	if err != nil {
		t.Fatalf("prepare worker tick: %v", err)
	}

	first, err := RunRepositoryExtractionWorkerTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("RunRepositoryExtractionWorkerTick() error = %v", err)
	}
	if first.Replayed || first.RequestID != input.RequestID || !first.Claim.Claimed || first.Claim.Replayed || first.Claim.Work == nil || *first.Claim.Work != scheduled.Work || first.Claim.AttemptNumber != 1 {
		t.Fatalf("first worker tick claim = %+v, schedule = %+v", first, scheduled)
	}
	if first.Claim.RequestID != prepared.claimRequestID || first.Execution == nil || first.Execution.RequestID != prepared.executionRequestID || first.Execution.Replayed || first.Execution.Work != scheduled.Work || first.Execution.ClaimID != first.Claim.ClaimID || first.Execution.Finish.Outcome != RepositoryExtractionWorkOutcomeSucceeded {
		t.Fatalf("first worker tick execution = %+v, prepared = %+v", first.Execution, prepared)
	}
	assertRepositoryExtractionWorkerTickRequest(t, ctx, pool, prepared, first.Claim)

	replay, err := RunRepositoryExtractionWorkerTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay RunRepositoryExtractionWorkerTick() error = %v", err)
	}
	if !replay.Replayed || !replay.Claim.Replayed || replay.Execution == nil || !replay.Execution.Replayed || !replay.Execution.Extraction.Replayed || !replay.Execution.Finish.Replayed {
		t.Fatalf("replayed worker tick = %+v", replay)
	}
	if replay.Claim.ClaimID != first.Claim.ClaimID || replay.Execution.Extraction.SourceGeneration.ID != first.Execution.Extraction.SourceGeneration.ID || !replay.Execution.Finish.FinishedAt.Equal(first.Execution.Finish.FinishedAt) {
		t.Fatalf("replayed worker tick identity = %+v, first = %+v", replay, first)
	}

	reused := input
	reused.WorkspaceRoot = t.TempDir()
	_, err = RunRepositoryExtractionWorkerTick(ctx, pool, reused)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	noWorkInput := input
	noWorkInput.RequestID = "worker-tick-no-work"
	noWorkInput.WorkerID = "worker-tick-idle"
	noWork, err := RunRepositoryExtractionWorkerTick(ctx, pool, noWorkInput)
	if err != nil {
		t.Fatalf("no-work RunRepositoryExtractionWorkerTick() error = %v", err)
	}
	if noWork.Replayed || noWork.Claim.Claimed || noWork.Claim.Replayed || noWork.Claim.Work != nil || noWork.Execution != nil {
		t.Fatalf("no-work worker tick = %+v", noWork)
	}
	noWorkReplay, err := RunRepositoryExtractionWorkerTick(ctx, pool, noWorkInput)
	if err != nil {
		t.Fatalf("replay no-work RunRepositoryExtractionWorkerTick() error = %v", err)
	}
	if !noWorkReplay.Replayed || !noWorkReplay.Claim.Replayed || noWorkReplay.Claim.Claimed || noWorkReplay.Execution != nil {
		t.Fatalf("replayed no-work worker tick = %+v", noWorkReplay)
	}

	assertTableCount(t, ctx, pool, "repository_extraction_worker_tick_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	assertTableCount(t, ctx, pool, "repository_generation_activation_requests", 0)
}

func TestIntegrationRunRepositoryExtractionWorkerTickLeavesFailedExecutionRunning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/worker-tick-authority", "package authority\n\nfunc Authority() {}\n")
	wrongRoot, _ := repositoryWorkExecutionGitFixture(t, "example.com/worker-tick-wrong", "package wrong\n\nfunc Wrong() {}\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "worker-tick-failure", commitSHA, "worker-tick-failure", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-worker-tick-failure", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	input := RepositoryExtractionWorkerTickInput{
		RequestID:                 "worker-tick-failure",
		WorkspaceRoot:             wrongRoot,
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-tick-failure",
		LeaseDurationMilliseconds: 60_000,
	}
	prepared, err := prepareRepositoryExtractionWorkerTick(input)
	if err != nil {
		t.Fatalf("prepare failing worker tick: %v", err)
	}

	_, err = RunRepositoryExtractionWorkerTick(ctx, pool, input)
	assertKind(t, err, ErrorInvalidInput)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, prepared.claimRequestID, input.RepoID, input.ExtractorName, input.WorkerID)
	if !claim.Replayed || !claim.Claimed || claim.Work == nil || *claim.Work != scheduled.Work {
		t.Fatalf("failed worker tick claim replay = %+v, schedule = %+v", claim, scheduled)
	}
	assertRepositoryExtractionWorkerTickRequest(t, ctx, pool, prepared, claim)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	_, err = RunRepositoryExtractionWorkerTick(ctx, pool, input)
	assertKind(t, err, ErrorInvalidInput)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	reused := input
	reused.WorkspaceRoot = root
	_, err = RunRepositoryExtractionWorkerTick(ctx, pool, reused)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	blocked := input
	blocked.RequestID = "worker-tick-running-stream"
	blocked.WorkspaceRoot = root
	blocked.WorkerID = "worker-tick-observer"
	noWork, err := RunRepositoryExtractionWorkerTick(ctx, pool, blocked)
	if err != nil {
		t.Fatalf("running-stream worker tick error = %v", err)
	}
	if noWork.Claim.Claimed || noWork.Claim.Work != nil || noWork.Execution != nil {
		t.Fatalf("running-stream worker tick = %+v", noWork)
	}
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	assertTableCount(t, ctx, pool, "repository_extraction_worker_tick_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 0)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 0)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 0)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	assertTableCount(t, ctx, pool, "repository_generation_activation_requests", 0)
}

func TestIntegrationRunRepositoryExtractionWorkerTickConcurrentReplayCommitsOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/worker-tick-concurrent", "package sample\n\nfunc ConcurrentTick() {}\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "worker-tick-concurrent", commitSHA, "worker-tick-concurrent", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-worker-tick-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	input := RepositoryExtractionWorkerTickInput{
		RequestID:                 "worker-tick-concurrent",
		WorkspaceRoot:             root,
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-tick-concurrent",
		LeaseDurationMilliseconds: 60_000,
	}

	const concurrentTicks = 16
	start := make(chan struct{})
	results := make(chan RepositoryExtractionWorkerTickResult, concurrentTicks)
	errors := make(chan error, concurrentTicks)
	for range concurrentTicks {
		go func() {
			<-start
			result, err := RunRepositoryExtractionWorkerTick(ctx, pool, input)
			results <- result
			errors <- err
		}()
	}
	close(start)

	replayed := 0
	claimReplayed := 0
	executionReplayed := 0
	for range concurrentTicks {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent worker tick error = %v", err)
		}
		result := <-results
		if !result.Claim.Claimed || result.Claim.Work == nil || *result.Claim.Work != scheduled.Work || result.Execution == nil || result.Execution.Finish.Outcome != RepositoryExtractionWorkOutcomeSucceeded {
			t.Fatalf("concurrent worker tick result = %+v", result)
		}
		if result.Replayed {
			replayed++
		}
		if result.Claim.Replayed {
			claimReplayed++
		}
		if result.Execution.Replayed {
			executionReplayed++
		}
	}
	if replayed != concurrentTicks-1 ||
		claimReplayed != concurrentTicks-1 ||
		executionReplayed != concurrentTicks-1 {
		t.Fatalf(
			"concurrent replay counts tick/claim/execution = %d/%d/%d, want %d/%d/%d",
			replayed,
			claimReplayed,
			executionReplayed,
			concurrentTicks-1,
			concurrentTicks-1,
			concurrentTicks-1,
		)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_worker_tick_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
}

func assertRepositoryExtractionWorkerTickRequest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, request repositoryExtractionWorkerTickRequest, claim RepositoryExtractionWorkClaimResult) {
	t.Helper()
	var repoID, extractorName, workerID, claimRequestID, executionRequestID, payloadHash string
	var leaseDurationMilliseconds int64
	var claimed bool
	var workItemID, claimID *string
	var attemptNumber *int
	if err := pool.QueryRow(ctx, `
		SELECT
			repo_id,
			extractor_name,
			worker_id,
			lease_duration_milliseconds,
			claim_request_id,
			execution_request_id,
			claimed,
			work_item_id,
			claim_id,
			attempt_number,
			request_payload_hash
		FROM repository_extraction_worker_tick_requests
		WHERE request_id = $1
	`, request.input.RequestID).Scan(&repoID, &extractorName, &workerID, &leaseDurationMilliseconds, &claimRequestID, &executionRequestID, &claimed, &workItemID, &claimID, &attemptNumber, &payloadHash); err != nil {
		t.Fatalf("read worker tick request %s: %v", request.input.RequestID, err)
	}
	if repoID != request.input.RepoID || extractorName != request.input.ExtractorName || workerID != request.input.WorkerID || leaseDurationMilliseconds != request.input.LeaseDurationMilliseconds || claimRequestID != request.claimRequestID || executionRequestID != request.executionRequestID || payloadHash != request.requestPayloadHash || claimed != claim.Claimed {
		t.Fatalf("worker tick request = %s/%s/%s/%d/%s/%s/%t/%s, want %+v claim %+v", repoID, extractorName, workerID, leaseDurationMilliseconds, claimRequestID, executionRequestID, claimed, payloadHash, request, claim)
	}
	if !claim.Claimed {
		if workItemID != nil || claimID != nil || attemptNumber != nil {
			t.Fatalf("no-work worker tick durable claim = %v/%v/%v", workItemID, claimID, attemptNumber)
		}
		return
	}
	if workItemID == nil || *workItemID != claim.Work.WorkItemID || claimID == nil || *claimID != claim.ClaimID || attemptNumber == nil || *attemptNumber != claim.AttemptNumber {
		t.Fatalf("claimed worker tick durable claim = %v/%v/%v, want %s/%s/%d", workItemID, claimID, attemptNumber, claim.Work.WorkItemID, claim.ClaimID, claim.AttemptNumber)
	}
}
