//go:build integration

package evidenceingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationListExpiredRepositoryExtractionWorkExecutionsFindsExactOwnersWithoutMutation(t *testing.T) {
	ctx, pool := integrationPool(t)
	db := pgxDB{pool: pool}
	base := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)

	renewedWork, renewedClaim, renewedExecution := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "z-renewed-execution", "a", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(10*time.Millisecond), 10, true,
	)
	renewed, err := renewRepositoryExtractionWorkLease(ctx, db, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "renew-z-renewed-execution",
		WorkItemID:                renewedWork.Work.WorkItemID,
		ClaimID:                   renewedClaim.ClaimID,
		WorkerID:                  renewedClaim.WorkerID,
		LeaseDurationMilliseconds: 40,
	}, base.Add(15*time.Millisecond))
	if err != nil {
		t.Fatalf("renew execution-owned claim: %v", err)
	}

	expiredWork, expiredClaim, expiredExecution := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "a-expired-execution", "b", ExtractorRepositoryGoplsCodeFact,
		base, base.Add(25*time.Millisecond), 10, true,
	)
	createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "0-expired-without-owner", "c", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(5*time.Millisecond), 5, false,
	)
	terminalWork, terminalClaim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "b-terminal-execution", "d", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(30*time.Millisecond), 100, true,
	)
	_, err = finishRepositoryExtractionWork(ctx, db, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-b-terminal-execution",
		WorkItemID:     terminalWork.Work.WorkItemID,
		ClaimID:        terminalClaim.ClaimID,
		WorkerID:       terminalClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "terminal execution must not remain discoverable",
	}, base.Add(32*time.Millisecond))
	if err != nil {
		t.Fatalf("finish terminal execution fixture: %v", err)
	}

	input := RepositoryExtractionWorkExpiredExecutionListInput{Limit: 10}
	executions, err := listExpiredRepositoryExtractionWorkExecutions(ctx, db, input, *expiredClaim.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("list at exact execution expiry: %v", err)
	}
	assertExpiredRepositoryExtractionWorkExecution(
		t,
		executions,
		expiredExecution.input.RequestID,
		expiredWork.Work,
		expiredClaim,
		*expiredClaim.LeaseExpiresAt,
		10,
	)

	executions, err = listExpiredRepositoryExtractionWorkExecutions(ctx, db, input, renewed.LeaseExpiresAt.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("list before renewed expiry: %v", err)
	}
	if len(executions) != 1 || executions[0].ExecutionRequestID != expiredExecution.input.RequestID {
		t.Fatalf("executions before renewed expiry = %+v, want only %q", executions, expiredExecution.input.RequestID)
	}

	executions, err = listExpiredRepositoryExtractionWorkExecutions(ctx, db, RepositoryExtractionWorkExpiredExecutionListInput{Limit: 1}, renewed.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("list bounded executions: %v", err)
	}
	if len(executions) != 1 || executions[0].ExecutionRequestID != expiredExecution.input.RequestID {
		t.Fatalf("bounded executions = %+v, want earliest %q", executions, expiredExecution.input.RequestID)
	}

	executions, err = listExpiredRepositoryExtractionWorkExecutions(ctx, db, input, renewed.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("list at renewed expiry: %v", err)
	}
	if len(executions) != 2 {
		t.Fatalf("executions at renewed expiry = %+v, want two", executions)
	}
	if executions[0].ExecutionRequestID != expiredExecution.input.RequestID || executions[1].ExecutionRequestID != renewedExecution.input.RequestID {
		t.Fatalf("execution order = %+v, want %q then %q", executions, expiredExecution.input.RequestID, renewedExecution.input.RequestID)
	}
	assertExpiredExecutionDiscoveryTableCounts(t, ctx, pool, 4, 4, 3, 1, 1, 0)

	_, err = repairExpiredRepositoryExtractionWorkExecution(ctx, db, RepositoryExtractionWorkExecutionRepairInput{
		RequestID:          "repair-a-expired-execution",
		ExecutionRequestID: expiredExecution.input.RequestID,
		WorkItemID:         expiredWork.Work.WorkItemID,
		ClaimID:            expiredClaim.ClaimID,
		WorkerID:           expiredClaim.WorkerID,
		RepairActorID:      "controller-expired-execution-discovery",
	}, renewed.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("repair discovered execution: %v", err)
	}
	executions, err = listExpiredRepositoryExtractionWorkExecutions(ctx, db, input, renewed.LeaseExpiresAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("list after exact repair: %v", err)
	}
	assertExpiredRepositoryExtractionWorkExecution(
		t,
		executions,
		renewedExecution.input.RequestID,
		renewedWork.Work,
		renewedClaim,
		renewed.LeaseExpiresAt,
		10,
	)
	assertExpiredExecutionDiscoveryTableCounts(t, ctx, pool, 4, 4, 3, 1, 1, 1)
}

func createExpiredExecutionDiscoveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoID, commitCharacter, extractorName string, observedAt, claimedAt time.Time, leaseMilliseconds int64, reserveExecution bool) (RepositoryExtractionWorkScheduleResult, RepositoryExtractionWorkClaimResult, repositoryExtractionWorkExecutionRequest) {
	t.Helper()
	_, stable := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat(commitCharacter, 40), repoID, observedAt, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-"+repoID, stable.RequestID, extractorName)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-" + repoID,
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-" + repoID,
		LeaseDurationMilliseconds: leaseMilliseconds,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim discovery fixture %q: %v", repoID, err)
	}
	if !reserveExecution {
		return scheduled, claim, repositoryExtractionWorkExecutionRequest{}
	}
	execution := reserveRepositoryExtractionWorkExecutionForRepairTest(t, ctx, pool, "execute-"+repoID, scheduled.Work, claim, claimedAt.Add(time.Millisecond))
	return scheduled, claim, execution
}

func assertExpiredRepositoryExtractionWorkExecution(t *testing.T, executions []RepositoryExtractionWorkExpiredExecution, executionRequestID string, work RepositoryExtractionWork, claim RepositoryExtractionWorkClaimResult, leaseExpiresAt time.Time, heartbeatLeaseMilliseconds int64) {
	t.Helper()
	if len(executions) != 1 {
		t.Fatalf("expired executions = %+v, want one", executions)
	}
	got := executions[0]
	if got.ExecutionRequestID != executionRequestID || got.Work != work || got.ClaimID != claim.ClaimID || got.AttemptNumber != claim.AttemptNumber || got.WorkerID != claim.WorkerID || claim.ClaimedAt == nil || !got.ClaimedAt.Equal(*claim.ClaimedAt) || !got.LeaseExpiresAt.Equal(leaseExpiresAt) || got.HeartbeatLeaseDurationMilliseconds != heartbeatLeaseMilliseconds || got.HeartbeatCount != 0 {
		t.Fatalf("expired execution = %+v, want execution/work/claim/lease %q/%+v/%+v/%s", got, executionRequestID, work, claim, leaseExpiresAt)
	}
}

func assertExpiredExecutionDiscoveryTableCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItems, attempts, executions, renewals, finishes, repairs int) {
	t.Helper()
	assertTableCount(t, ctx, pool, "repository_extraction_work_items", workItems)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", attempts)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", executions)
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", renewals)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", finishes)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", repairs)
}
