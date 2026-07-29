//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRunRepositoryExtractionWorkRetryControllerTickConsumesBoundedBatchAndReplays(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 22, 14, 0, 0, 0, time.UTC)
	dueA := createDueRetryDecisionFixture(t, ctx, pool, "a-retry-controller-tick", string(ErrorRunnerInvocationFailed), base)
	dueB := createDueRetryDecisionFixture(t, ctx, pool, "b-retry-controller-tick", string(ErrorRunnerInvocationTimeout), base)
	dueC := createDueRetryDecisionFixture(t, ctx, pool, "c-retry-controller-tick", string(ErrorRunnerInvocationCancelled), base)
	future := createDueRetryDecisionFixture(t, ctx, pool, "future-retry-controller-tick", string(ErrorRunnerInvocationFailed), base.Add(time.Minute))
	discoveredAt := *dueA.policy.RetryNotBefore
	input := RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "retry-controller-tick-bounded",
		Limit:           2,
		ConsumerActorID: "retry-controller-bounded",
	}

	first, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, input, discoveredAt)
	if err != nil {
		t.Fatalf("run bounded retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, first, input, discoveredAt, []dueRetryDecisionFixture{dueA, dueB}, false)
	assertRepositoryExtractionWorkPendingAfterRetry(t, ctx, pool, dueA.work.WorkItemID, discoveredAt)
	assertRepositoryExtractionWorkPendingAfterRetry(t, ctx, pool, dueB.work.WorkItemID, discoveredAt)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, dueC.work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", dueC.finish.FailureClass, dueC.finish.FailureMessage)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, future.work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", future.finish.FailureClass, future.finish.FailureMessage)
	assertRetryControllerTickTableCounts(t, ctx, pool, 1, 2, 2)

	replay, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, input, discoveredAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay bounded retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, replay, input, discoveredAt, []dueRetryDecisionFixture{dueA, dueB}, true)
	assertRetryControllerTickTableCounts(t, ctx, pool, 1, 2, 2)
	remaining, err := listDueRepositoryExtractionWorkRetryDecisions(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkDueRetryDecisionListInput{Limit: 10}, discoveredAt)
	if err != nil {
		t.Fatalf("list remaining due retry decisions: %v", err)
	}
	assertDueRepositoryExtractionWorkRetryDecision(t, remaining, dueC)

	reused := input
	reused.ConsumerActorID = "another-retry-controller"
	_, err = runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, reused, discoveredAt)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	secondInput := RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "retry-controller-tick-second",
		Limit:           10,
		ConsumerActorID: input.ConsumerActorID,
	}
	second, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, secondInput, discoveredAt)
	if err != nil {
		t.Fatalf("run second retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, second, secondInput, discoveredAt, []dueRetryDecisionFixture{dueC}, false)
	assertRetryControllerTickTableCounts(t, ctx, pool, 2, 3, 3)

	emptyInput := RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "retry-controller-tick-empty",
		Limit:           10,
		ConsumerActorID: input.ConsumerActorID,
	}
	empty, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, emptyInput, discoveredAt)
	if err != nil {
		t.Fatalf("run empty retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, empty, emptyInput, discoveredAt, nil, false)
	emptyReplay, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, emptyInput, future.policy.RetryNotBefore.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay empty retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, emptyReplay, emptyInput, discoveredAt, nil, true)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, future.work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", future.finish.FailureClass, future.finish.FailureMessage)
	assertRetryControllerTickTableCounts(t, ctx, pool, 3, 3, 3)
}

func TestIntegrationRunRepositoryExtractionWorkRetryControllerTicksSkipLockedCandidates(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-10 * time.Second)
	createDueRetryDecisionFixture(t, ctx, pool, "concurrent-retry-controller-tick", string(ErrorRunnerInvocationFailed), base)

	results := make(chan RepositoryExtractionWorkRetryControllerTickResult, 2)
	errs := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		go func(index int) {
			result, err := RunRepositoryExtractionWorkRetryControllerTick(ctx, pool, RepositoryExtractionWorkRetryControllerTickInput{
				RequestID:       fmt.Sprintf("concurrent-retry-controller-tick-%d", index),
				Limit:           1,
				ConsumerActorID: fmt.Sprintf("concurrent-retry-controller-%d", index),
			})
			results <- result
			errs <- err
		}(index)
	}
	consumptionCount := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent retry controller tick: %v", err)
		}
		consumptionCount += len((<-results).Consumptions)
	}
	if consumptionCount != 1 {
		t.Fatalf("concurrent retry controller consumption count = %d, want 1", consumptionCount)
	}
	assertRetryControllerTickTableCounts(t, ctx, pool, 2, 1, 1)
}

func TestIntegrationRunRepositoryExtractionWorkRetryControllerTickSkipsContendedSourceStreamRow(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC)
	due := createDueRetryDecisionFixture(t, ctx, pool, "contended-retry-controller-tick", string(ErrorRunnerInvocationFailed), base)
	discoveredAt := *due.policy.RetryNotBefore

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin contended retry source stream row lock: %v", err)
	}
	t.Cleanup(func() {
		_ = lockTx.Rollback(context.Background())
	})
	if _, err := lockTx.Exec(ctx, `SELECT 1 FROM repository_source_streams WHERE repo_id = $1 AND extractor_name = $2 FOR UPDATE`, due.work.RepoID, due.work.ExtractorName); err != nil {
		t.Fatalf("hold contended retry source stream row: %v", err)
	}

	contendedInput := RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "contended-retry-controller-tick-skip",
		Limit:           1,
		ConsumerActorID: "contended-retry-controller",
	}
	contended, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, contendedInput, discoveredAt)
	if err != nil {
		t.Fatalf("run contended retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, contended, contendedInput, discoveredAt, nil, false)

	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release contended retry source stream row lock: %v", err)
	}
	uncontendedInput := RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "contended-retry-controller-tick-retry",
		Limit:           1,
		ConsumerActorID: contendedInput.ConsumerActorID,
	}
	uncontended, err := runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, uncontendedInput, discoveredAt)
	if err != nil {
		t.Fatalf("run uncontended retry controller tick: %v", err)
	}
	assertRepositoryExtractionWorkRetryControllerTickResult(t, uncontended, uncontendedInput, discoveredAt, []dueRetryDecisionFixture{due}, false)
	assertRetryControllerTickTableCounts(t, ctx, pool, 2, 1, 1)
}

func assertRepositoryExtractionWorkRetryControllerTickResult(t *testing.T, got RepositoryExtractionWorkRetryControllerTickResult, input RepositoryExtractionWorkRetryControllerTickInput, discoveredAt time.Time, wants []dueRetryDecisionFixture, replayed bool) {
	t.Helper()
	if got.RequestID != input.RequestID ||
		got.Limit != input.Limit ||
		got.ConsumerActorID != input.ConsumerActorID ||
		!got.DiscoveredAt.Equal(discoveredAt) ||
		got.Replayed != replayed ||
		len(got.Consumptions) != len(wants) {
		t.Fatalf("retry controller tick = %+v, want input %+v at %s with %d consumptions replayed=%t", got, input, discoveredAt, len(wants), replayed)
	}
	for index, want := range wants {
		consumption := got.Consumptions[index]
		expectedRequestID, err := repositoryExtractionWorkRetryControllerTickChildRequestID(input.RequestID, want.policy.RequestID)
		if err != nil {
			t.Fatalf("build expected retry controller child request ID: %v", err)
		}
		if consumption.RequestID != expectedRequestID ||
			consumption.FailurePolicyRequestID != want.policy.RequestID ||
			consumption.Work != want.work ||
			consumption.ClaimID != want.claim.ClaimID ||
			consumption.ConsumerActorID != input.ConsumerActorID ||
			!consumption.ConsumedAt.Equal(discoveredAt) ||
			consumption.Disposition != RepositoryExtractionWorkRetryDispositionRequeued ||
			consumption.Replayed != replayed {
			t.Fatalf("retry controller consumption[%d] = %+v, want fixture %+v replayed=%t", index, consumption, want, replayed)
		}
	}
}

func assertRetryControllerTickTableCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticks, items, consumptions int) {
	t.Helper()
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_controller_tick_requests", ticks)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_controller_tick_items", items)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", consumptions)
}
