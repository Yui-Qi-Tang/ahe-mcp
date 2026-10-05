//go:build integration && consistencylab

package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestIntegrationConsistencyWatchLifecycle(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	runner := consistencyLifecycleRunner(t)
	scope := ConsistencyScope{Namespace: "watch", ScopeRef: "production"}
	a, _ := consistencyClaim(t, ctx, pool, scope, "watch-a")
	b, kb := consistencyClaim(t, ctx, pool, scope, "watch-b")
	p := consistencyLiteral("door", "open", "prod", false)
	np := p
	np.Negated = true
	in := ConsistencyWatchInput{WatchID: "door-policy", RequestID: "configuration-1", EngineID: runner.Identity(), Request: ConsistencyRequest{Scope: scope, RuleVersion: "1", Evidence: []ConsistencyCondition{{ID: a, Clauses: [][]ConsistencyLiteral{{p}}}, {ID: b, Clauses: [][]ConsistencyLiteral{{np}}}}}}
	r, err := RegisterConsistencyWatch(ctx, pool, in)
	if err != nil || r.Revision != 1 {
		t.Fatal(r, err)
	}
	replay, err := RegisterConsistencyWatch(ctx, pool, in)
	if err != nil || !replay.Replayed || replay.Hash != r.Hash {
		t.Fatal(replay, err)
	}
	changed := in
	changed.Paused = true
	_, err = RegisterConsistencyWatch(ctx, pool, changed)
	if kind, ok := KindOf(err); !ok || kind != ErrorIdempotencyKeyReused {
		t.Fatal("changed replay", err)
	}
	w := ConsistencyWorker{Pool: pool, Runner: runner, PollInterval: 10 * time.Millisecond}
	first, err := w.Tick(ctx)
	if err != nil || len(first) != 1 || first[0].State != "computed" {
		t.Fatal(first, err)
	}
	initial, err := ReadConsistencyRun(ctx, pool, first[0].RunID)
	if err != nil || initial.Run.Diagnosis.Outcome != ConsistencyConflict || initial.Freshness != "matches_snapshot" || !initial.Run.Diagnosis.Localization.Minimal {
		t.Fatal(initial, err)
	}
	consistencyLifecycleRecord(t, "persisted-conflict", initial)
	if len(initial.Run.Artifacts) == 0 {
		t.Fatal("no durable artifacts")
	}
	for _, a := range initial.Run.Artifacts {
		raw, err := ReadConsistencyArtifact(ctx, pool, initial.ID, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			t.Fatal("artifact changed")
		}
	}

	// PostgreSQL owns the durable bytes even after the runner's source files
	// disappear. Discover the exact task-created query directory from an ID.
	root := os.Getenv("AHE_CONSISTENCY_REPORT_DIR")
	matches, err := filepath.Glob(filepath.Join(root, "lifecycle-*", filepath.FromSlash(initial.Run.Artifacts[0].ID)))
	if err != nil || len(matches) != 1 {
		t.Fatal("artifact fixture", matches, err)
	}
	if err := os.Remove(matches[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConsistencyArtifact(ctx, pool, initial.ID, initial.Run.Artifacts[0].ID); err != nil {
		t.Fatal("durable artifact depended on runner files", err)
	}

	// A result and its promised artifacts are atomic, including direct SQL.
	for _, duplicate := range []bool{false, true} {
		run := initial.Run
		run.TargetID = "malformed-manifest"
		if duplicate {
			run.TargetID = "duplicate-manifest"
		}
		artifact := logicresolver.Artifact{ID: "a", SHA256: hex.EncodeToString(sha256.New().Sum(nil)), Bytes: 0}
		run.Artifacts = []logicresolver.Artifact{artifact}
		if duplicate {
			run.Artifacts = append(run.Artifacts, artifact)
		}
		data, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		id := hex.EncodeToString(sum[:])
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO consistency_runs(run_id,watch_id,revision,target_id,body) VALUES($1,$2,$3,$4,$5)`, id, run.WatchID, run.Revision, run.TargetID, string(data)); err != nil {
			t.Fatal(err)
		}
		if duplicate {
			// Extra unlisted b must not compensate for duplicate a in manifest.
			for _, key := range []string{"a", "b"} {
				if _, err = tx.Exec(ctx, `INSERT INTO consistency_run_artifacts(run_id,artifact_id,digest,content) VALUES($1,$2,$3,$4)`, id, key, artifact.SHA256, []byte{}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = tx.Commit(ctx); err == nil {
			t.Fatal("incomplete/duplicate artifact manifest committed")
		}
	}
	again, err := w.Tick(ctx)
	if err != nil || again[0].State != "unchanged" || again[0].RunID != first[0].RunID {
		t.Fatal(again, err)
	}
	_, err = ChangePropositionBinding(ctx, pool, propositionTestChange(b, "withdraw-watch-b", 0, "membership:watch-b", kb.ID(), "withdraw", PropositionKey{}))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ReadConsistencyRun(ctx, pool, initial.ID)
	if err != nil || stale.Freshness != "stale" {
		t.Fatal(stale.Freshness, err)
	}
	second, err := w.Tick(ctx)
	if err != nil || second[0].State != "computed" {
		t.Fatal(second, err)
	}
	current, err := ReadConsistencyRun(ctx, pool, second[0].RunID)
	if err != nil || current.Run.Diagnosis.Outcome != ConsistencyCompatible {
		t.Fatal(current.Run.Diagnosis, err)
	}
	consistencyLifecycleRecord(t, "stale-prior-run", stale)
	consistencyLifecycleRecord(t, "recomputed-compatible", current)
	if !reflect.DeepEqual(stale.Run, initial.Run) {
		t.Fatal("old result mutated")
	}
	// New evidence is a real source change, but normalization remains external.
	c, _ := consistencyClaim(t, ctx, pool, scope, "watch-new")
	third, err := w.Tick(ctx)
	if err != nil || third[0].State != "blocked" {
		t.Fatal(third, err)
	}
	blocked, err := ReadConsistencyRun(ctx, pool, third[0].RunID)
	if err != nil || blocked.Run.Diagnosis.Reason != "missing_normalization" {
		t.Fatal(blocked, err)
	}
	in.ExpectedRevision = 1
	in.RequestID = "configuration-2"
	in.Request.Evidence = append(in.Request.Evidence, ConsistencyCondition{ID: c, Clauses: [][]ConsistencyLiteral{{np}}})
	if _, err := RegisterConsistencyWatch(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	// A new worker instance recovers entirely from PostgreSQL.
	w = ConsistencyWorker{Pool: pool, Runner: runner, PollInterval: 10 * time.Millisecond}
	fourth, err := w.Tick(ctx)
	if err != nil || fourth[0].State != "computed" {
		t.Fatal(fourth, err)
	}
	latest, err := ReadConsistencyRun(ctx, pool, fourth[0].RunID)
	if err != nil || latest.Run.Diagnosis.Outcome != ConsistencyConflict || latest.Run.Revision != 2 {
		t.Fatal(latest, err)
	}
	page, err := ReadConsistencyEvents(ctx, pool, in.WatchID, 0, 2)
	if err != nil || !page.Truncated || page.NextCursor != 2 {
		t.Fatal(page, err)
	}
	all, err := ReadConsistencyEvents(ctx, pool, in.WatchID, 0, 256)
	if err != nil {
		t.Fatal(err)
	}
	consistencyLifecycleRecord(t, "durable-events", all)
	for i, e := range all.Events {
		if e.Cursor != int64(i+1) {
			t.Fatal("cursor gap", all)
		}
	}
	for _, table := range []string{"consistency_watches", "consistency_watch_versions", "consistency_runs", "consistency_run_artifacts", "consistency_events"} {
		for _, verb := range []string{"DELETE FROM ", "TRUNCATE "} {
			if _, err := pool.Exec(ctx, verb+table); err == nil {
				t.Fatal("mutable diagnostic history", table, verb)
			}
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE consistency_watches SET watch_id=watch_id`); err == nil {
		t.Fatal("watch update allowed")
	}
	// A known old configured result cannot become current after a new revision.
	in.RequestID = "configuration-3"
	in.ExpectedRevision = 2
	in.Paused = true
	if _, err := RegisterConsistencyWatch(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	paused, err := w.Tick(ctx)
	if err != nil || paused[0].State != "paused" {
		t.Fatal(paused, err)
	}
	if r, err := ReadConsistencyRun(ctx, pool, latest.ID); err != nil || r.Freshness != "stale" {
		t.Fatal(r.Freshness, err)
	}
}

func TestIntegrationConsistencyWorkerRestartAndContention(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	runner := consistencyLifecycleRunner(t)
	scope := ConsistencyScope{Namespace: "restart", ScopeRef: "scope"}
	node, _ := consistencyClaim(t, ctx, pool, scope, "only")
	in := ConsistencyWatchInput{WatchID: "restart", RequestID: "restart-1", EngineID: runner.Identity(), Request: ConsistencyRequest{Scope: scope, RuleVersion: "1", Evidence: []ConsistencyCondition{{ID: node, Clauses: [][]ConsistencyLiteral{{consistencyLiteral("s", "p", "c", false)}}}}}}
	receipt, err := RegisterConsistencyWatch(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	view := consistencyRead(t, ctx, pool, scope)
	target, _ := consistencyTarget(receipt.Hash, view.ID, runner.Identity())
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = appendConsistencyEvent(ctx, tx, in.WatchID, 1, target, "invalidated", ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The persisted state is exactly the crash boundary after invalidation and
	// before computing. Two replacement workers must finish it only once.
	w := ConsistencyWorker{Pool: pool, Runner: runner}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { <-start; _, err := w.Tick(ctx); errs <- err })
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM consistency_runs WHERE watch_id=$1`, in.WatchID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	events, err := ReadConsistencyEvents(ctx, pool, in.WatchID, 0, 256)
	if err != nil || len(events.Events) != 3 {
		t.Fatal(events, err)
	}
	// Identical rules/annotations can be revised explicitly; it is a new config.
	in.ExpectedRevision = 1
	in.RequestID = "restart-2"
	if _, err = RegisterConsistencyWatch(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	w.PollInterval = 10 * time.Millisecond
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for count < 2 {
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("automatic recompute did not finish")
		case <-tick.C:
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM consistency_runs WHERE watch_id=$1`, in.WatchID).Scan(&count); err != nil {
				cancel()
				t.Fatal(err)
			}
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestIntegrationConsistencyEventCommitOrder(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	runner := consistencyLifecycleRunner(t)
	in := ConsistencyWatchInput{WatchID: "cursor", RequestID: "cursor-1", EngineID: runner.Identity(), Request: ConsistencyRequest{Scope: ConsistencyScope{Namespace: "cursor", ScopeRef: "scope"}, RuleVersion: "1"}}
	if _, err := RegisterConsistencyWatch(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(context.Background())
	if err = appendConsistencyEvent(ctx, first, in.WatchID, 1, "target-a", "invalidated", ""); err != nil {
		t.Fatal(err)
	}
	var firstPID int
	if err = first.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&firstPID); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback(context.Background())
	var secondPID int
	if err = second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		err := appendConsistencyEvent(ctx, second, in.WatchID, 1, "target-b", "invalidated", "")
		if err == nil {
			err = second.Commit(ctx)
		}
		done <- err
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	blocked := false
	for !blocked {
		select {
		case err := <-done:
			t.Fatalf("second committed ahead of first: %v", err)
		case <-deadline.C:
			t.Fatal("did not observe row-lock ordering")
		case <-ticker.C:
			if err = pool.QueryRow(ctx, `SELECT $1=ANY(pg_blocking_pids($2))`, firstPID, secondPID).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := ReadConsistencyEvents(ctx, pool, in.WatchID, 0, 256)
	if err != nil || before.NextCursor != 1 {
		t.Fatal(before, err)
	}
	if err = first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	after, err := ReadConsistencyEvents(ctx, pool, in.WatchID, before.NextCursor, 256)
	if err != nil || len(after.Events) != 2 || after.Events[0].TargetID != "target-a" || after.Events[1].TargetID != "target-b" {
		t.Fatal(after, err)
	}
	// Direct SQL cannot jump a cursor or attach another target's result.
	_, err = pool.Exec(ctx, `INSERT INTO consistency_events(watch_id,cursor,revision,target_id,kind) VALUES($1,99,1,'x','invalidated')`, in.WatchID)
	if err == nil {
		t.Fatal("cursor jump accepted")
	}
}

func TestIntegrationConsistencyPolicyWatchLifecycle(t *testing.T) {
	ctx, pool, scope, old, current := consistencyLifecycleChain(t)
	runner := consistencyLifecycleRunner(t)
	p := consistencyLiteral("door", "open", "prod", false)
	np := p
	np.Negated = true
	input := ConsistencyWatchInput{WatchID: "frontier-watch", RequestID: "frontier-config-1", EngineID: runner.Identity(), Request: ConsistencyRequest{Scope: scope, RuleVersion: "1", Evidence: []ConsistencyCondition{{ID: old, Clauses: [][]ConsistencyLiteral{{np}}}, {ID: current.CanonicalRef, Clauses: [][]ConsistencyLiteral{{p}}}}}}
	if _, err := RegisterConsistencyWatch(ctx, pool, input); err != nil {
		t.Fatal(err)
	}
	worker := ConsistencyWorker{Pool: pool, Runner: runner}
	first, err := worker.Tick(ctx)
	if err != nil || first[0].State != "computed" {
		t.Fatal(first, err)
	}
	original, err := ReadConsistencyRun(ctx, pool, first[0].RunID)
	if err != nil || original.Run.Diagnosis.Outcome != ConsistencyCompatible || len(original.Run.Request.Evidence) != 1 {
		t.Fatal(original, err)
	}
	proposal := createSupersessionExternalProposal(t, ctx, pool, "watched-replacement", "3", "Door is closed after replacement.")
	next, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic watched replacement", Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{current.CanonicalRef}, ExpectedRevision: current.EventRevision, ExpectedHeadEventID: current.AdmissionEventID})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ReadConsistencyRun(ctx, pool, original.ID)
	if err != nil || stale.Freshness != "stale" {
		t.Fatal(stale.Freshness, err)
	}
	second, err := worker.Tick(ctx)
	if err != nil || second[0].State != "blocked" {
		t.Fatal(second, err)
	}
	empty, err := ReadConsistencyRun(ctx, pool, second[0].RunID)
	if err != nil || empty.Run.Diagnosis.Reason != "empty_scope" {
		t.Fatal(empty.Run.Diagnosis.Reason, err)
	}
	key := PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: "watched-replacement", Revision: "1"}
	propositionTestBind(t, ctx, pool, next.CanonicalRef, "watched-replacement-binding", key)
	third, err := worker.Tick(ctx)
	if err != nil || third[0].State != "blocked" {
		t.Fatal(third, err)
	}
	missing, err := ReadConsistencyRun(ctx, pool, third[0].RunID)
	if err != nil || missing.Run.Diagnosis.Reason != "missing_normalization" {
		t.Fatal(missing.Run.Diagnosis.Reason, err)
	}
	input.RequestID = "frontier-config-2"
	input.ExpectedRevision = 1
	input.Request.Evidence = append(input.Request.Evidence, ConsistencyCondition{ID: next.CanonicalRef, Clauses: [][]ConsistencyLiteral{{np}}})
	if _, err := RegisterConsistencyWatch(ctx, pool, input); err != nil {
		t.Fatal(err)
	}
	fourth, err := worker.Tick(ctx)
	if err != nil || fourth[0].State != "computed" {
		t.Fatal(fourth, err)
	}
	result, err := ReadConsistencyRun(ctx, pool, fourth[0].RunID)
	if err != nil || result.Freshness != "matches_snapshot" || result.Run.Diagnosis.Outcome != ConsistencyCompatible || len(result.Run.Request.Evidence) != 1 || result.Run.Request.Evidence[0].ID != next.CanonicalRef {
		t.Fatal(result, err)
	}
	events, err := ReadConsistencyEvents(ctx, pool, input.WatchID, 0, 256)
	if err != nil || len(events.Events) != 10 {
		t.Fatal(events, err)
	}
	consistencyLifecycleRecord(t, "policy-watch-recomputed", result)
}
