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

func TestIntegrationRunExpiredRepositoryExtractionWorkMaintenanceTickAppliesBoundedCrossStreamBatchAndReplays(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	ownerlessWork, ownerlessClaim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "a-expired-maintenance-ownerless", "a", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(5*time.Millisecond), 5, false,
	)
	ownedWork, ownedClaim, ownedExecution := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "b-expired-maintenance-owned", "b", ExtractorRepositoryGoplsCodeFact,
		base, base.Add(10*time.Millisecond), 5, true,
	)
	remainingWork, remainingClaim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "c-expired-maintenance-remaining", "c", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(15*time.Millisecond), 5, false,
	)
	discoveredAt := base.Add(30 * time.Millisecond)
	input := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "expired-maintenance-bounded",
		Limit:              2,
		MaintenanceActorID: "expired-maintenance-controller",
	}

	first, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, input, discoveredAt)
	if err != nil {
		t.Fatalf("run bounded expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, first, input, discoveredAt, false, []expiredMaintenanceTransitionExpectation{
		{kind: RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery, work: ownerlessWork.Work, claim: ownerlessClaim},
		{kind: RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair, work: ownedWork.Work, claim: ownedClaim, executionRequestID: ownedExecution.input.RequestID},
	})
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, ownerlessClaim.ClaimID, discoveredAt)
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, ownedClaim.ClaimID, discoveredAt)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, remainingClaim)
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 1, 2, 1, 1)

	replay, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, input, discoveredAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay bounded expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, replay, input, discoveredAt, true, []expiredMaintenanceTransitionExpectation{
		{kind: RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery, work: ownerlessWork.Work, claim: ownerlessClaim},
		{kind: RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair, work: ownedWork.Work, claim: ownedClaim, executionRequestID: ownedExecution.input.RequestID},
	})
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, remainingClaim)
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 1, 2, 1, 1)

	reused := input
	reused.MaintenanceActorID = "different-expired-maintenance-controller"
	_, err = runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, reused, discoveredAt)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	secondInput := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "expired-maintenance-remaining",
		Limit:              10,
		MaintenanceActorID: input.MaintenanceActorID,
	}
	second, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, secondInput, discoveredAt)
	if err != nil {
		t.Fatalf("run remaining expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, second, secondInput, discoveredAt, false, []expiredMaintenanceTransitionExpectation{
		{kind: RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery, work: remainingWork.Work, claim: remainingClaim},
	})
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 2, 3, 2, 1)

	emptyInput := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "expired-maintenance-empty",
		Limit:              10,
		MaintenanceActorID: input.MaintenanceActorID,
	}
	empty, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, emptyInput, discoveredAt)
	if err != nil {
		t.Fatalf("run empty expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, empty, emptyInput, discoveredAt, false, nil)
	emptyReplay, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, emptyInput, discoveredAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay empty expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, emptyReplay, emptyInput, discoveredAt, true, nil)
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 3, 3, 2, 1)
}

func TestIntegrationRunExpiredRepositoryExtractionWorkMaintenanceTickRollsBackAndRestartsWholeBatch(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	_, ownerlessClaim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "a-expired-maintenance-rollback-ownerless", "d", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(5*time.Millisecond), 5, false,
	)
	_, ownedClaim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "b-expired-maintenance-rollback-owned", "e", ExtractorRepositoryGoplsCodeFact,
		base, base.Add(10*time.Millisecond), 5, true,
	)
	discoveredAt := base.Add(30 * time.Millisecond)
	input := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "expired-maintenance-rollback-restart",
		Limit:              2,
		MaintenanceActorID: "expired-maintenance-rollback-controller",
	}

	_, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_second_expired_maintenance_item() RETURNS trigger AS $$
		BEGIN
			IF NEW.item_ordinal = 2 THEN
				RAISE EXCEPTION 'injected expired maintenance item failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER reject_second_expired_maintenance_item
		BEFORE INSERT ON repository_extraction_work_expired_maintenance_tick_items
		FOR EACH ROW EXECUTE FUNCTION reject_second_expired_maintenance_item();
	`)
	if err != nil {
		t.Fatalf("install expired maintenance fault injection: %v", err)
	}

	_, err = runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, input, discoveredAt)
	if err == nil || !strings.Contains(err.Error(), "injected expired maintenance item failure") {
		t.Fatalf("fault-injected expired maintenance tick error = %v", err)
	}
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, ownerlessClaim)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, ownedClaim)
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 0, 0, 0, 0)

	_, err = pool.Exec(ctx, `
		DROP TRIGGER reject_second_expired_maintenance_item ON repository_extraction_work_expired_maintenance_tick_items;
		DROP FUNCTION reject_second_expired_maintenance_item();
	`)
	if err != nil {
		t.Fatalf("remove expired maintenance fault injection: %v", err)
	}

	restarted, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, input, discoveredAt)
	if err != nil {
		t.Fatalf("restart expired maintenance tick: %v", err)
	}
	if restarted.Replayed || len(restarted.Transitions) != 2 {
		t.Fatalf("restarted expired maintenance tick = %+v", restarted)
	}
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, ownerlessClaim.ClaimID, discoveredAt)
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, ownedClaim.ClaimID, discoveredAt)
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 1, 2, 1, 1)
}

func TestIntegrationRunExpiredRepositoryExtractionWorkMaintenanceTicksSkipLockedCandidates(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "concurrent-expired-maintenance", "1", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(5*time.Millisecond), 5, false,
	)

	results := make(chan RepositoryExtractionWorkExpiredMaintenanceTickResult, 2)
	errs := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		go func(index int) {
			result, err := RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pool, RepositoryExtractionWorkExpiredMaintenanceTickInput{
				RequestID:          fmt.Sprintf("concurrent-expired-maintenance-%d", index),
				Limit:              1,
				MaintenanceActorID: fmt.Sprintf("concurrent-expired-maintenance-controller-%d", index),
			})
			results <- result
			errs <- err
		}(index)
	}
	transitionCount := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent expired maintenance tick: %v", err)
		}
		transitionCount += len((<-results).Transitions)
	}
	if transitionCount != 1 {
		t.Fatalf("concurrent expired maintenance transition count = %d, want 1", transitionCount)
	}
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 2, 1, 1, 0)
}

func TestIntegrationRunExpiredRepositoryExtractionWorkMaintenanceTickSkipsContendedSourceStreamRow(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 22, 16, 30, 0, 0, time.UTC)
	work, claim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "contended-expired-maintenance", "2", ExtractorRepositoryGoParserCodeFact,
		base, base.Add(5*time.Millisecond), 5, false,
	)
	discoveredAt := base.Add(20 * time.Millisecond)

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin contended source stream row lock: %v", err)
	}
	t.Cleanup(func() {
		_ = lockTx.Rollback(context.Background())
	})
	if _, err := lockTx.Exec(ctx, `SELECT 1 FROM repository_source_streams WHERE repo_id = $1 AND extractor_name = $2 FOR UPDATE`, work.Work.RepoID, work.Work.ExtractorName); err != nil {
		t.Fatalf("hold contended source stream row: %v", err)
	}

	contendedInput := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "contended-expired-maintenance-skip",
		Limit:              1,
		MaintenanceActorID: "contended-expired-maintenance-controller",
	}
	contended, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, contendedInput, discoveredAt)
	if err != nil {
		t.Fatalf("run contended expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, contended, contendedInput, discoveredAt, false, nil)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release contended source stream row lock: %v", err)
	}
	uncontendedInput := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "contended-expired-maintenance-retry",
		Limit:              1,
		MaintenanceActorID: contendedInput.MaintenanceActorID,
	}
	uncontended, err := runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, uncontendedInput, discoveredAt)
	if err != nil {
		t.Fatalf("run uncontended expired maintenance tick: %v", err)
	}
	assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t, uncontended, uncontendedInput, discoveredAt, false, []expiredMaintenanceTransitionExpectation{
		{kind: RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery, work: work.Work, claim: claim},
	})
	assertExpiredMaintenanceTickTableCounts(t, ctx, pool, 2, 1, 1, 0)
}

func TestIntegrationRetryAndExpiredMaintenanceControllersRunConcurrently(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	due := createDueRetryDecisionFixture(t, ctx, pool, "mixed-controller-retry", string(ErrorRunnerInvocationFailed), base)
	expiredWork, expiredClaim, _ := createExpiredExecutionDiscoveryFixture(
		t, ctx, pool, "mixed-controller-expired", "f", ExtractorRepositoryGoplsCodeFact,
		base, base.Add(5*time.Millisecond), 5, false,
	)

	start := make(chan struct{})
	errCh := make(chan error, 2)
	go func() {
		<-start
		result, err := RunRepositoryExtractionWorkRetryControllerTick(ctx, pool, RepositoryExtractionWorkRetryControllerTickInput{
			RequestID:       "mixed-controller-retry-tick",
			Limit:           1,
			ConsumerActorID: "mixed-controller-retry",
		})
		if err == nil && len(result.Consumptions) != 1 {
			err = fmt.Errorf("retry controller consumptions = %d, want 1", len(result.Consumptions))
		}
		errCh <- err
	}()
	go func() {
		<-start
		result, err := RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pool, RepositoryExtractionWorkExpiredMaintenanceTickInput{
			RequestID:          "mixed-controller-expired-tick",
			Limit:              1,
			MaintenanceActorID: "mixed-controller-expired",
		})
		if err == nil && len(result.Transitions) != 1 {
			err = fmt.Errorf("expired maintenance transitions = %d, want 1", len(result.Transitions))
		}
		errCh <- err
	}()
	close(start)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("mixed controller concurrency: %v", err)
		}
	}

	assertRepositoryExtractionWorkPendingWithoutClaim(t, ctx, pool, due.work.WorkItemID)
	assertExpiredRepositoryExtractionWorkClaimAttemptAtOrAfterLease(t, ctx, pool, expiredClaim)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_controller_tick_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 1)
	if expiredWork.Work.WorkItemID == due.work.WorkItemID {
		t.Fatal("mixed controller fixtures unexpectedly share work identity")
	}
}

type expiredMaintenanceTransitionExpectation struct {
	kind               string
	work               RepositoryExtractionWork
	claim              RepositoryExtractionWorkClaimResult
	executionRequestID string
}

func assertRepositoryExtractionWorkExpiredMaintenanceTickResult(t *testing.T, got RepositoryExtractionWorkExpiredMaintenanceTickResult, input RepositoryExtractionWorkExpiredMaintenanceTickInput, discoveredAt time.Time, replayed bool, wants []expiredMaintenanceTransitionExpectation) {
	t.Helper()
	if got.RequestID != input.RequestID || got.Limit != input.Limit || got.MaintenanceActorID != input.MaintenanceActorID || !got.DiscoveredAt.Equal(discoveredAt) || got.Replayed != replayed || len(got.Transitions) != len(wants) {
		t.Fatalf("expired maintenance tick = %+v, want input %+v at %s with %d transitions replayed=%t", got, input, discoveredAt, len(wants), replayed)
	}
	for index, want := range wants {
		transition := got.Transitions[index]
		if transition.TransitionKind != want.kind {
			t.Fatalf("expired maintenance transition[%d] kind = %q, want %q", index, transition.TransitionKind, want.kind)
		}
		switch want.kind {
		case RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery:
			if transition.Recovery == nil || transition.ExecutionRepair != nil || transition.Recovery.Work != want.work || transition.Recovery.ClaimID != want.claim.ClaimID || transition.Recovery.RecoveryActorID != input.MaintenanceActorID || !transition.Recovery.RecoveredAt.Equal(discoveredAt) || transition.Recovery.Replayed != replayed {
				t.Fatalf("expired maintenance recovery[%d] = %+v, want %+v", index, transition, want)
			}
		case RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair:
			if transition.ExecutionRepair == nil || transition.Recovery != nil || transition.ExecutionRepair.ExecutionRequestID != want.executionRequestID || transition.ExecutionRepair.Work != want.work || transition.ExecutionRepair.ClaimID != want.claim.ClaimID || transition.ExecutionRepair.RepairActorID != input.MaintenanceActorID || !transition.ExecutionRepair.RepairedAt.Equal(discoveredAt) || transition.ExecutionRepair.Replayed != replayed {
				t.Fatalf("expired maintenance execution repair[%d] = %+v, want %+v", index, transition, want)
			}
		default:
			t.Fatalf("unsupported expired maintenance transition expectation %q", want.kind)
		}
	}
}

func assertExpiredMaintenanceTickTableCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticks, items, recoveries, repairs int) {
	t.Helper()
	assertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_requests", ticks)
	assertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_items", items)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", recoveries)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", repairs)
}

func assertExpiredRepositoryExtractionWorkClaimAttemptAtOrAfterLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim RepositoryExtractionWorkClaimResult) {
	t.Helper()
	var status string
	var expiredAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, expired_at
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, claim.ClaimID).Scan(&status, &expiredAt); err != nil {
		t.Fatalf("read mixed-controller expired claim: %v", err)
	}
	if claim.LeaseExpiresAt == nil || status != repositoryExtractionWorkExpired || expiredAt.Before(*claim.LeaseExpiresAt) {
		t.Fatalf("mixed-controller expired claim = %s/%s, lease = %v", status, expiredAt, claim.LeaseExpiresAt)
	}
}

func assertRepositoryExtractionWorkPendingWithoutClaim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItemID string) {
	t.Helper()
	var status string
	var claimID *string
	if err := pool.QueryRow(ctx, `
		SELECT status, claim_id
		FROM repository_extraction_work_items
		WHERE work_item_id = $1
	`, workItemID).Scan(&status, &claimID); err != nil {
		t.Fatalf("read mixed-controller retried work: %v", err)
	}
	if status != repositoryExtractionWorkPending || claimID != nil {
		t.Fatalf("mixed-controller retried work = %s/%v, want pending without claim", status, claimID)
	}
}
