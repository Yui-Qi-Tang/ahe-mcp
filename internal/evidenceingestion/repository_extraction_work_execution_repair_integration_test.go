//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRepositoryExtractionWorkExecutionRepairRequeuesExactExpiredOwner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 23, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "execution-repair-requeue", strings.Repeat("e", 40), "execution-repair-requeue", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-execution-repair-requeue", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-execution-repair-requeue",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-execution-repair",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim execution repair work: %v", err)
	}
	execution := reserveRepositoryExtractionWorkExecutionForRepairTest(t, ctx, pool, "execute-for-repair", scheduled.Work, claim, claimedAt.Add(time.Millisecond))
	input := RepositoryExtractionWorkExecutionRepairInput{
		RequestID:          "repair-execution-requeue",
		ExecutionRequestID: execution.input.RequestID,
		WorkItemID:         scheduled.Work.WorkItemID,
		ClaimID:            claim.ClaimID,
		WorkerID:           claim.WorkerID,
		RepairActorID:      "controller-execution-repair",
	}

	_, err = repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, input, claim.LeaseExpiresAt.Add(-time.Nanosecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 0)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	wrongOwner := input
	wrongOwner.RequestID = "repair-wrong-execution-owner"
	wrongOwner.ExecutionRequestID = "missing-execution-owner"
	_, err = repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, wrongOwner, *claim.LeaseExpiresAt)
	assertKind(t, err, ErrorMissingSourceViewAttempt)

	wrongWorker := input
	wrongWorker.RequestID = "repair-wrong-execution-worker"
	wrongWorker.WorkerID = "different-worker"
	_, err = repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, wrongWorker, *claim.LeaseExpiresAt)
	assertKind(t, err, ErrorRepositoryWorkConflict)

	repairedAt := *claim.LeaseExpiresAt
	repaired, err := repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, input, repairedAt)
	if err != nil {
		t.Fatalf("repair expired execution: %v", err)
	}
	if repaired.Replayed || repaired.RequestID != input.RequestID || repaired.ExecutionRequestID != input.ExecutionRequestID || repaired.Work != scheduled.Work || repaired.ClaimID != claim.ClaimID || repaired.AttemptNumber != 1 || repaired.WorkerID != claim.WorkerID || repaired.RepairActorID != input.RepairActorID || repaired.RepairReason != RepositoryExtractionWorkExecutionRepairReasonLeaseExpired || !repaired.LeaseExpiresAt.Equal(*claim.LeaseExpiresAt) || !repaired.RepairedAt.Equal(repairedAt) || repaired.Disposition != RepositoryExtractionWorkRecoveryDispositionRequeued || repaired.SupersededByWorkItemID != "" {
		t.Fatalf("execution repair result = %+v", repaired)
	}
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, claim.ClaimID, repairedAt)

	reclaimed, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-after-execution-repair",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-after-execution-repair",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, repairedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("claim after execution repair: %v", err)
	}
	if !reclaimed.Claimed || reclaimed.Work == nil || *reclaimed.Work != scheduled.Work || reclaimed.AttemptNumber != 2 || reclaimed.ClaimID == claim.ClaimID {
		t.Fatalf("claim after execution repair = %+v", reclaimed)
	}

	replay, err := repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, input, repairedAt.Add(2*time.Millisecond))
	if err != nil {
		t.Fatalf("replay execution repair: %v", err)
	}
	if !replay.Replayed || !sameRepositoryExtractionWorkExecutionRepairResult(replay, repaired) {
		t.Fatalf("execution repair replay = %+v, first = %+v", replay, repaired)
	}
	reused := input
	reused.RepairActorID = "different-controller"
	_, err = RepairExpiredRepositoryExtractionWorkExecution(ctx, pool, reused)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	_, _, err = reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, execution, repairedAt.Add(3*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 0)
}

func TestIntegrationRepositoryExtractionWorkExecutionRepairSupersedesBehindNewerPendingWork(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 23, 30, 0, 0, time.UTC)
	repoID := "execution-repair-supersede"
	extractor := ExtractorRepositoryGoParserCodeFact
	_, firstStable := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("f", 40), "execution-repair-first", base, false)
	first := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-execution-repair-first", firstStable.RequestID, extractor)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-execution-repair-first",
		RepoID:                    repoID,
		ExtractorName:             extractor,
		WorkerID:                  "worker-execution-repair-first",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim first execution repair work: %v", err)
	}
	execution := reserveRepositoryExtractionWorkExecutionForRepairTest(t, ctx, pool, "execute-execution-repair-first", first.Work, claim, claimedAt.Add(time.Millisecond))

	_, secondStable := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("0", 40), "execution-repair-second", base.Add(20*time.Millisecond), false)
	second := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-execution-repair-second", secondStable.RequestID, extractor)
	repairedAt := base.Add(30 * time.Millisecond)
	repaired, err := repairExpiredRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkExecutionRepairInput{
		RequestID:          "repair-execution-supersede",
		ExecutionRequestID: execution.input.RequestID,
		WorkItemID:         first.Work.WorkItemID,
		ClaimID:            claim.ClaimID,
		WorkerID:           claim.WorkerID,
		RepairActorID:      "controller-execution-supersede",
	}, repairedAt)
	if err != nil {
		t.Fatalf("repair superseded execution: %v", err)
	}
	if repaired.Disposition != RepositoryExtractionWorkRecoveryDispositionSuperseded || repaired.SupersededByWorkItemID != second.Work.WorkItemID {
		t.Fatalf("superseding execution repair = %+v, newer work = %+v", repaired, second)
	}
	assertRepositoryExtractionWorkSupersededBy(t, ctx, pool, first.Work.WorkItemID, second.Work.WorkItemID)
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, claim.ClaimID, repairedAt)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 1)
}

func TestIntegrationRepositoryExtractionWorkConcurrentExecutionRepairHasOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	_, stable := persistStableWorkObservation(t, ctx, pool, "execution-repair-concurrent", strings.Repeat("1", 40), "execution-repair-concurrent", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-execution-repair-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-execution-repair-concurrent",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-execution-repair-concurrent",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim concurrent execution repair work: %v", err)
	}
	execution := reserveRepositoryExtractionWorkExecutionForRepairTest(t, ctx, pool, "execute-execution-repair-concurrent", scheduled.Work, claim, claimedAt.Add(time.Millisecond))

	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(index int) {
			_, err := RepairExpiredRepositoryExtractionWorkExecution(ctx, pool, RepositoryExtractionWorkExecutionRepairInput{
				RequestID:          fmt.Sprintf("repair-execution-concurrent-%d", index),
				ExecutionRequestID: execution.input.RequestID,
				WorkItemID:         scheduled.Work.WorkItemID,
				ClaimID:            claim.ClaimID,
				WorkerID:           claim.WorkerID,
				RepairActorID:      fmt.Sprintf("controller-execution-concurrent-%d", index),
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
			t.Fatalf("concurrent execution repair error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent execution repair succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
}

func reserveRepositoryExtractionWorkExecutionForRepairTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID string, work RepositoryExtractionWork, claim RepositoryExtractionWorkClaimResult, startedAt time.Time) repositoryExtractionWorkExecutionRequest {
	t.Helper()
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     requestID,
		WorkItemID:    work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare execution for repair: %v", err)
	}
	reserved, replayed, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, startedAt)
	if err != nil {
		t.Fatalf("reserve execution for repair: %v", err)
	}
	if replayed {
		t.Fatal("first execution reservation for repair reported replay")
	}
	return reserved
}

func sameRepositoryExtractionWorkExecutionRepairResult(left, right RepositoryExtractionWorkExecutionRepairResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left == right
}
