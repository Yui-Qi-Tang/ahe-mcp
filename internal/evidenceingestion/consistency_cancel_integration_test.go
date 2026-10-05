//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntegrationConsistencyCancelledConfiguration(t *testing.T) {
	ctx, pool := integrationPool(t)
	in := ConsistencyWatchInput{WatchID: "cancelled-config", RequestID: "first", EngineID: "fixed-engine", Request: ConsistencyRequest{Scope: ConsistencyScope{Namespace: "fixed", ScopeRef: "scope"}, RuleVersion: "1"}}
	if _, err := RegisterConsistencyWatch(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock(`+consistencyJournalLock+`)`, in.WatchID); err != nil {
		t.Fatal(err)
	}
	in.ExpectedRevision = 1
	in.RequestID = "retry-after-cancellation"
	in.Paused = true
	waiting, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := RegisterConsistencyWatch(waiting, pool, in); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation while journal lock held: %v", err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := ReadConsistencyWatch(ctx, pool, in.WatchID, -1)
	if err != nil || before.CurrentRevision != 1 || before.Configuration.Paused {
		t.Fatal("cancelled request committed", before, err)
	}
	events, err := ReadConsistencyEvents(ctx, pool, in.WatchID, 0, 10)
	if err != nil || len(events.Events) != 1 {
		t.Fatal("cancelled request appended notification", events, err)
	}
	receipt, err := RegisterConsistencyWatch(ctx, pool, in)
	if err != nil || receipt.Revision != 2 || receipt.Replayed {
		t.Fatal("exact retry did not commit once", receipt, err)
	}
	replay, err := RegisterConsistencyWatch(ctx, pool, in)
	if err != nil || !replay.Replayed || replay.Revision != 2 {
		t.Fatal("historical receipt missing", replay, err)
	}
	after, err := ReadConsistencyWatch(ctx, pool, in.WatchID, -1)
	if err != nil || !after.Configuration.Paused {
		t.Fatal("pause missing after retry", after, err)
	}
}
