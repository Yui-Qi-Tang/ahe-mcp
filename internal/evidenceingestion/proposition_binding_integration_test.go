//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func propositionTestClaim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, parents ...string) string {
	t.Helper()
	statement := "Externally declared evidence " + name
	got, err := IngestManualText(ctx, pool, ManualTextInput{SourceID: name, SourceVersion: "v1", Raw: []byte(statement + "\n"), RequestID: name, AttemptNumber: 1}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	input := AdmissionInput{ProposalOccurrenceID: got.ProposalOccurrenceID, DecisionBy: "fixture", DecisionReason: "external decision"}
	if len(parents) > 0 {
		input.Derivation = &DerivationAdmissionInput{ParentNodeIDs: parents, Method: "declared-rule", Producer: "fixture", TraceRef: "trace:" + name}
	}
	admitted, err := AdmitPendingProposal(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	return admitted.CanonicalRef
}

func propositionTestKey(name string) PropositionKey {
	return PropositionKey{Namespace: "identity-test", LocalID: name, ScopeRef: "release-1", Revision: "definition-1"}
}
func propositionTestBind(t *testing.T, ctx context.Context, pool *pgxpool.Pool, node, request string, key PropositionKey) PropositionBindingInput {
	t.Helper()
	in := PropositionBindingInput{RequestID: request, Key: key, Definition: "Externally reviewed definition", NodeID: node, DecisionBy: "reviewer", DecisionReason: "evidence examined"}
	if _, err := BindCanonicalProposition(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	return in
}
func propositionTestChange(node, id string, rev int64, ref, from, op string, target PropositionKey) PropositionBindingChange {
	in := PropositionBindingChange{RequestID: id, NodeID: node, ExpectedRevision: rev, PreviousRef: ref, FromID: from, Operation: op, Target: target, DecisionBy: "reviewer", Reason: "external correction", EvidenceRef: "source:review-note"}
	if op != "withdraw" {
		in.Definition = "Externally reviewed definition"
	}
	return in
}

func TestIntegrationPropositionBindingLifecycle(t *testing.T) {
	ctx, pool := integrationPool(t)
	a := propositionTestClaim(t, ctx, pool, "parent-a")
	b := propositionTestClaim(t, ctx, pool, "parent-b")
	c := propositionTestClaim(t, ctx, pool, "parent-c")
	ab := propositionTestClaim(t, ctx, pool, "derived-ab", a, b)
	qc := propositionTestClaim(t, ctx, pool, "derived-c", c)
	original, correct := propositionTestKey("original"), propositionTestKey("corrected")
	initial := propositionTestBind(t, ctx, pool, ab, "initial-ab", original)
	propositionTestBind(t, ctx, pool, qc, "initial-c", original)
	read := func(node string, rev int64, limit int) PropositionBindingHistory {
		t.Helper()
		v, e := ReadPropositionBindingHistory(ctx, pool, node, rev, limit)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	members := func(k PropositionKey, limit int) PropositionMembers {
		t.Helper()
		v, e := ReadPropositionMembers(ctx, pool, k, limit)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	apply := func(in PropositionBindingChange) PropositionChangeReceipt {
		t.Helper()
		v, e := ChangePropositionBinding(ctx, pool, in)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	before := members(original, 32)
	t.Run("alternative_derivations_keep_distinct_nodes_and_parents", func(t *testing.T) {
		if len(before.Nodes) != 2 || len(before.Graph.Derivations) != 2 {
			t.Fatalf("members/derivations=%d/%d", len(before.Nodes), len(before.Graph.Derivations))
		}
		parentCounts := []int{}
		for _, d := range before.Graph.Derivations {
			parentCounts = append(parentCounts, len(d.Parents))
		}
		slices.Sort(parentCounts)
		if !slices.Equal(parentCounts, []int{1, 2}) {
			t.Fatalf("AND parents=%v", parentCounts)
		}
		if v := members(original, 1); !v.Truncated || len(v.Nodes) != 1 {
			t.Fatalf("unbounded inventory: %+v", v)
		}
	})
	change := propositionTestChange(ab, "correct-ab", 0, "membership:initial-ab", original.ID(), "correct", correct)
	corrected := apply(change)
	t.Run("correction_moves_only_one_binding", func(t *testing.T) {
		if !slices.Equal(members(original, 32).Nodes, []string{qc}) || !slices.Equal(members(correct, 32).Nodes, []string{ab}) {
			t.Fatal("correction moved wrong members")
		}
		if h := read(ab, 0, 100); h.PropositionID != original.ID() || h.HeadRevision != 1 {
			t.Fatalf("history=%+v", h)
		}
	})
	withdrawal := propositionTestChange(ab, "withdraw-ab", 1, "event:correct-ab", correct.ID(), "withdraw", PropositionKey{})
	apply(withdrawal)
	t.Run("withdrawn_binding_does_not_fall_back_to_initial", func(t *testing.T) {
		if h := read(ab, -1, 1); h.Active || h.PropositionID != "" || !h.HistoryTruncated || h.HeadRevision != 2 {
			t.Fatalf("withdrawn=%+v", h)
		}
		if len(members(correct, 32).Nodes) != 0 || slices.Contains(members(original, 32).Nodes, ab) {
			t.Fatal("withdrawal resurrected")
		}
	})
	t.Run("old_change_and_initial_replays_are_historical_only", func(t *testing.T) {
		replay := apply(change)
		if !replay.Replayed || !reflect.DeepEqual(replay.Event, corrected.Event) {
			t.Fatal("changed historical receipt")
		}
		receipt, e := BindCanonicalProposition(ctx, pool, initial)
		if e != nil || !receipt.Replayed || receipt.Initial != initial {
			t.Fatalf("initial replay=%+v %v", receipt, e)
		}
		if h := read(ab, -1, 100); h.Active || h.HeadRevision != 2 {
			t.Fatal("old receipt changed current state")
		}
		drift := change
		drift.Reason = "different reason"
		var domain *DomainError
		if _, e := ChangePropositionBinding(ctx, pool, drift); !errors.As(e, &domain) || domain.Kind != ErrorIdempotencyKeyReused {
			t.Fatalf("changed replay: %v", e)
		}
	})
	t.Run("stale_full_predecessor_and_invalid_transition_rejected", func(t *testing.T) {
		for i, in := range []PropositionBindingChange{
			propositionTestChange(ab, "stale", 0, "membership:initial-ab", original.ID(), "correct", correct),
			propositionTestChange(ab, "bad-ref", 2, "event:correct-ab", "", "restore", correct),
			propositionTestChange(ab, "bad-from", 2, "event:withdraw-ab", original.ID(), "restore", correct),
			propositionTestChange(ab, "bad-restore", 2, "event:withdraw-ab", "", "restore", original),
			propositionTestChange(ab, "double-withdraw", 2, "event:withdraw-ab", "", "withdraw", PropositionKey{}),
		} {
			if _, e := ChangePropositionBinding(ctx, pool, in); !errors.Is(e, ErrPropositionBindingConflict) {
				t.Fatalf("case %d: %v", i, e)
			}
		}
	})
	t.Run("failed_new_definition_rolls_back", func(t *testing.T) {
		fresh := propositionTestKey("must-not-survive")
		in := propositionTestChange(ab, "rollback", 0, "membership:initial-ab", original.ID(), "correct", fresh)
		if _, e := ChangePropositionBinding(ctx, pool, in); e == nil {
			t.Fatal("stale change accepted")
		}
		var n int
		if e := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_propositions WHERE proposition_id=$1`, fresh.ID()).Scan(&n); e != nil || n != 0 {
			t.Fatalf("orphan definition=%d %v", n, e)
		}
	})
	t.Run("explicit_restore_then_ABA_does_not_accept_stale_input", func(t *testing.T) {
		apply(propositionTestChange(ab, "restore", 2, "event:withdraw-ab", "", "restore", correct))
		apply(propositionTestChange(ab, "back-original", 3, "event:restore", correct.ID(), "correct", original))
		if _, e := ChangePropositionBinding(ctx, pool, propositionTestChange(ab, "ABA-stale", 0, "membership:initial-ab", original.ID(), "withdraw", PropositionKey{})); !errors.Is(e, ErrPropositionBindingConflict) {
			t.Fatalf("ABA: %v", e)
		}
		if h := read(ab, -1, 100); !h.Active || h.HeadRevision != 4 || len(h.Events) != 4 {
			t.Fatalf("restored=%+v", h)
		}
	})
	t.Run("native_graph_unchanged", func(t *testing.T) {
		if after := members(original, 32); !reflect.DeepEqual(before.Graph, after.Graph) {
			t.Fatal("binding operations changed native graph or provenance")
		}
	})
	t.Run("reconnect_preserves_history", func(t *testing.T) {
		reopened, e := pgxpool.NewWithConfig(ctx, pool.Config())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		if h, e := ReadPropositionBindingHistory(ctx, reopened, ab, -1, 100); e != nil || h.HeadRevision != 4 {
			t.Fatalf("reconnect=%+v %v", h, e)
		}
	})
	t.Run("definition_drift_and_second_initial_binding_rejected", func(t *testing.T) {
		in := initial
		in.RequestID = "second-initial"
		if _, e := BindCanonicalProposition(ctx, pool, in); !errors.Is(e, ErrPropositionBindingConflict) {
			t.Fatalf("second initial: %v", e)
		}
		in.NodeID = c
		in.Definition = "different"
		if _, e := BindCanonicalProposition(ctx, pool, in); !errors.Is(e, ErrPropositionBindingConflict) {
			t.Fatalf("definition drift: %v", e)
		}
	})
}

func TestIntegrationPropositionBindingConcurrencyAndSnapshot(t *testing.T) {
	ctx, pool := integrationPool(t)
	key := propositionTestKey("base")
	node := propositionTestClaim(t, ctx, pool, "race-claim")
	initial := PropositionBindingInput{RequestID: "initial-race", NodeID: node, Key: key, Definition: "Externally reviewed definition", DecisionBy: "reviewer", DecisionReason: "reviewed"}
	t.Run("concurrent_exact_initial_requests_share_receipt", func(t *testing.T) {
		const n = 8
		ch := make(chan error, n)
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() { _, e := BindCanonicalProposition(ctx, pool, initial); ch <- e })
		}
		wg.Wait()
		close(ch)
		for e := range ch {
			if e != nil {
				t.Fatal(e)
			}
		}
	})
	snapshot, e := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	defer snapshot.Rollback(ctx)
	if _, e := readPropositionMembersTx(ctx, snapshot, key, 32); e != nil {
		t.Fatal(e)
	}
	t.Run("competing_changes_have_one_winner_and_no_lost_update", func(t *testing.T) {
		const n = 12
		start := make(chan struct{})
		ch := make(chan error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				<-start
				_, e := ChangePropositionBinding(ctx, pool, propositionTestChange(node, fmt.Sprintf("change-%d", i), 0, "membership:initial-race", key.ID(), "correct", propositionTestKey(fmt.Sprintf("target-%d", i))))
				ch <- e
			})
		}
		close(start)
		wg.Wait()
		close(ch)
		wins := 0
		for e := range ch {
			if e == nil {
				wins++
			} else if !errors.Is(e, ErrPropositionBindingConflict) {
				t.Fatal(e)
			}
		}
		if wins != 1 {
			t.Fatalf("winners=%d", wins)
		}
		var defs, events int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_propositions`).Scan(&defs); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_proposition_binding_events`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if defs != 2 || events != 1 {
			t.Fatalf("partial writes: definitions=%d events=%d", defs, events)
		}
	})
	t.Run("repeatable_read_retains_coherent_old_binding_and_graph", func(t *testing.T) {
		old, e := readPropositionMembersTx(ctx, snapshot, key, 32)
		if e != nil || !slices.Equal(old.Nodes, []string{node}) {
			t.Fatalf("snapshot=%+v %v", old, e)
		}
		now, e := ReadPropositionMembers(ctx, pool, key, 32)
		if e != nil || len(now.Nodes) != 0 {
			t.Fatalf("current=%+v %v", now, e)
		}
	})
	t.Run("concurrent_exact_changes_apply_once", func(t *testing.T) {
		h, e := ReadPropositionBindingHistory(ctx, pool, node, -1, 100)
		if e != nil {
			t.Fatal(e)
		}
		in := propositionTestChange(node, "same-withdraw", 1, h.HeadRef, h.PropositionID, "withdraw", PropositionKey{})
		const n = 8
		ch := make(chan PropositionChangeReceipt, n)
		errs := make(chan error, n)
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() { r, e := ChangePropositionBinding(ctx, pool, in); ch <- r; errs <- e })
		}
		wg.Wait()
		close(ch)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		fresh := 0
		for r := range ch {
			if !r.Replayed {
				fresh++
			}
			if r.Event.Revision != 2 {
				t.Fatal("extra event")
			}
		}
		if fresh != 1 {
			t.Fatalf("applied=%d", fresh)
		}
	})
}

func TestIntegrationPropositionBindingDatabaseGuardsAndRoles(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	node := propositionTestClaim(t, ctx, pool, "role-claim")
	key := propositionTestKey("role-base")
	var schema string
	if e := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	rolePool := func(profile dbrole.Profile) *pgxpool.Pool {
		t.Helper()
		role := "proposition_role_" + randomHex(t, 8)
		id := pgx.Identifier{role}.Sanitize()
		if _, e := pool.Exec(ctx, `CREATE ROLE `+id+` NOLOGIN`); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DROP OWNED BY `+id)
			_, _ = pool.Exec(context.Background(), `DROP ROLE `+id)
		})
		if _, e := pool.Exec(ctx, `GRANT USAGE ON SCHEMA `+pgx.Identifier{schema}.Sanitize()+` TO `+id); e != nil {
			t.Fatal(e)
		}
		manifest, e := dbrole.BuildManifest(profile)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range manifest.Tables {
			privs := []string{}
			for _, p := range r.Privileges {
				privs = append(privs, string(p))
			}
			if _, e := pool.Exec(ctx, `GRANT `+strings.Join(privs, ",")+` ON TABLE `+pgx.Identifier{schema, r.Table}.Sanitize()+` TO `+id); e != nil {
				t.Fatal(e)
			}
		}
		cfg := pool.Config()
		cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, e := c.Exec(ctx, `SET ROLE `+id); return e }
		result, e := pgxpool.NewWithConfig(ctx, cfg)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(result.Close)
		return result
	}
	writer, reader := rolePool(dbrole.ProfileCoreRecords), rolePool(dbrole.ProfileQuery)
	initial := propositionTestBind(t, ctx, writer, node, "initial-role", key)
	t.Run("query_role_cannot_write", func(t *testing.T) {
		in := initial
		in.RequestID = "query-write"
		if _, e := BindCanonicalProposition(ctx, reader, in); e == nil {
			t.Fatal("query wrote binding")
		}
	})
	change := propositionTestChange(node, "role-withdraw", 0, "membership:initial-role", key.ID(), "withdraw", PropositionKey{})
	if _, e := ChangePropositionBinding(ctx, writer, change); e != nil {
		t.Fatal(e)
	}
	t.Run("query_role_sees_withdrawal", func(t *testing.T) {
		v, e := ReadPropositionMembers(ctx, reader, key, 32)
		if e != nil || len(v.Nodes) != 0 {
			t.Fatalf("query=%+v %v", v, e)
		}
	})
	t.Run("direct_mutations_and_invalid_events_cannot_rewrite_history", func(t *testing.T) {
		for _, table := range []string{"canonical_propositions", "canonical_proposition_bindings", "canonical_proposition_binding_events"} {
			for _, sql := range []string{"DELETE FROM " + table, "TRUNCATE " + table} {
				if _, e := writer.Exec(ctx, sql); e == nil {
					t.Fatalf("accepted %s", sql)
				}
			}
		}
		if _, e := writer.Exec(ctx, `UPDATE canonical_proposition_bindings SET decision_reason='rewrite'`); e == nil {
			t.Fatal("initial receipt rewritten")
		}
		if _, e := writer.Exec(ctx, `INSERT INTO canonical_proposition_binding_events(request_id,canonical_node_id,revision,previous_ref,from_id,target_id,operation,decision_by,decision_reason,evidence_ref) VALUES ('forged',$1,2,'event:role-withdraw',NULL,$2,'correct','actor','reason','source')`, node, key.ID()); e == nil {
			t.Fatal("direct SQL bypassed transition")
		}
	})
	t.Run("RLS_hidden_withdrawal_fails_instead_of_resurrecting", func(t *testing.T) {
		if _, e := pool.Exec(ctx, `ALTER TABLE canonical_proposition_binding_events ENABLE ROW LEVEL SECURITY`); e != nil {
			t.Fatal(e)
		}
		defer pool.Exec(ctx, `ALTER TABLE canonical_proposition_binding_events DISABLE ROW LEVEL SECURITY`)
		if _, e := ReadPropositionMembers(ctx, reader, key, 32); e == nil {
			t.Fatal("RLS hidden head read as initial")
		}
	})
	t.Run("rollback_cannot_erase_recorded_history", func(t *testing.T) {
		down, err := os.ReadFile("../../migrations/000050_evidence_ingestion_proposition_bindings.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(down)); err == nil {
			t.Fatal("rollback erased history")
		}
		h, err := ReadPropositionBindingHistory(ctx, pool, node, -1, 100)
		if err != nil || h.HeadRevision != 1 || h.Active {
			t.Fatalf("history after rejected rollback=%+v %v", h, err)
		}
	})

	t.Run("readiness_rejects_missing_transition_guard", func(t *testing.T) {
		if _, e := pool.Exec(ctx, `ALTER TABLE canonical_proposition_binding_events DISABLE TRIGGER proposition_binding_transition`); e != nil {
			t.Fatal(e)
		}
		if _, e := migrations.VerifyCurrent(ctx, pool); e == nil {
			t.Fatal("missing guard passed readiness")
		}
	})
}

func integrationPoolWithMigrations(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_ingestion_test_" + randomHex(t, 8)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return ctx, pool
}
