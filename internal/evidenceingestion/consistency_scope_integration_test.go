//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Synthetic admission only. These fixtures do not stand in for human review.
func consistencyClaim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scope ConsistencyScope, name string, parents ...string) (string, PropositionKey) {
	t.Helper()
	node := propositionTestClaim(t, ctx, pool, "TEST-APPROVAL-STUB-"+name, parents...)
	key := PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: name, Revision: "v1"}
	_, err := BindCanonicalProposition(ctx, pool, PropositionBindingInput{RequestID: name, NodeID: node, Key: key, Definition: "Synthetic declaration " + name, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "fixture only"})
	if err != nil {
		t.Fatal(err)
	}
	return node, key
}

func consistencyRead(t *testing.T, ctx context.Context, pool *pgxpool.Pool, s ConsistencyScope) ConsistencyView {
	t.Helper()
	v, err := ReadConsistencyScope(ctx, pool, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIntegrationConsistencyScope(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	s := ConsistencyScope{Namespace: "consistency", ScopeRef: "declarations"}
	a, ka := consistencyClaim(t, ctx, pool, s, "a")
	b, _ := consistencyClaim(t, ctx, pool, s, "b")
	outside := s
	outside.ScopeRef = "elsewhere"
	consistencyClaim(t, ctx, pool, outside, "outside")
	before := consistencyRead(t, ctx, pool, s)
	t.Run("disconnected_members_and_scope_boundary", func(t *testing.T) {
		if len(before.Members) != 2 || len(before.Artifact.Nodes) != 2 || len(before.Artifact.Edges) != 0 {
			t.Fatalf("nodes=%d members=%d edges=%d", len(before.Artifact.Nodes), len(before.Members), len(before.Artifact.Edges))
		}
		if consistencyRead(t, ctx, pool, s).ID != before.ID {
			t.Fatal("same data changed view identity")
		}
	})
	t.Run("node_limit_and_empty_scope", func(t *testing.T) {
		limit := s
		limit.MaxNodes = 1
		if _, err := ReadConsistencyScope(ctx, pool, limit); !errors.Is(err, logicresolver.ErrLimit) {
			t.Fatalf("limit err=%v", err)
		}
		empty := s
		empty.ScopeRef = "absent"
		if len(consistencyRead(t, ctx, pool, empty).Members) != 0 {
			t.Fatal("scope leak")
		}
	})
	t.Run("cross_scope_correction_and_repeatable_snapshot", func(t *testing.T) {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
			t.Fatal(err)
		}
		normalized, _ := s.normalized()
		old, err := readConsistencyScopeTx(ctx, tx, normalized)
		if err != nil {
			t.Fatal(err)
		}
		target := ka
		target.ScopeRef = outside.ScopeRef
		change := propositionTestChange(a, "move-a", 0, "membership:a", ka.ID(), "correct", target)
		if _, err := ChangePropositionBinding(ctx, pool, change); err != nil {
			t.Fatal(err)
		}
		same, err := readConsistencyScopeTx(ctx, tx, normalized)
		if err != nil {
			t.Fatal(err)
		}
		if old.ID != same.ID || old.Snapshot != same.Snapshot {
			t.Fatal("transaction mixed binding snapshots")
		}
		now := consistencyRead(t, ctx, pool, s)
		if now.ID == old.ID || len(now.Members) != 1 || now.Members[0].NodeID != b {
			t.Fatal("old scope resurrected moved member")
		}
		moved := consistencyRead(t, ctx, pool, outside)
		if len(moved.Members) != 2 {
			t.Fatal("target scope missing corrected member")
		}
		if _, err := ChangePropositionBinding(ctx, pool, propositionTestChange(a, "withdraw-a", 1, "event:move-a", target.ID(), "withdraw", PropositionKey{})); err != nil {
			t.Fatal(err)
		}
		if v := consistencyRead(t, ctx, pool, outside); len(v.Members) != 1 || v.ID == moved.ID {
			t.Fatal("withdrawal resurrected or identity unchanged")
		}
	})
	t.Run("alternative_derivations_and_all_parents", func(t *testing.T) {
		p1, _ := consistencyClaim(t, ctx, pool, s, "parent-1")
		p2, _ := consistencyClaim(t, ctx, pool, s, "parent-2")
		d1, k1 := consistencyClaim(t, ctx, pool, s, "derived-1", p1, p2)
		d2, k2 := consistencyClaim(t, ctx, pool, s, "derived-2", p1)
		change := propositionTestChange(d2, "same-proposition", 0, "membership:derived-2", k2.ID(), "correct", k1)
		change.Definition = "Synthetic declaration derived-1"
		if _, err := ChangePropositionBinding(ctx, pool, change); err != nil {
			t.Fatal(err)
		}
		v := consistencyRead(t, ctx, pool, s)
		if len(v.Artifact.Derivations) != 2 {
			t.Fatal("collapsed alternative derivations")
		}
		for _, d := range v.Artifact.Derivations {
			if d.NodeID == d1 && (!slices.Contains(d.Parents, p1) || !slices.Contains(d.Parents, p2) || len(d.Parents) != 2) {
				t.Fatal("lost AND parents")
			}
			if d.NodeID == d2 && (!slices.Equal(d.Parents, []string{p1})) {
				t.Fatal("wrong alternative parents")
			}
		}
		limit := s
		limit.MaxEdges = 1
		if _, err := ReadConsistencyScope(ctx, pool, limit); !errors.Is(err, logicresolver.ErrLimit) {
			t.Fatalf("edge limit=%v", err)
		}
	})
	t.Run("missing_parent_fails", func(t *testing.T) {
		missing := ConsistencyScope{Namespace: s.Namespace, ScopeRef: "missing-parent"}
		consistencyClaim(t, ctx, pool, missing, "orphan-derived", b)
		if _, err := ReadConsistencyScope(ctx, pool, missing); err == nil {
			t.Fatal("missing AND parent accepted")
		}
	})
}

func TestIntegrationConsistencyRLS(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	s := ConsistencyScope{Namespace: "consistency", ScopeRef: "rls"}
	consistencyClaim(t, ctx, pool, s, "visible")
	role := "consistency_reader_" + randomHex(t, 6)
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	qr, qs := pgx.Identifier{role}.Sanitize(), pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE ROLE "+qr+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP OWNED BY "+qr)
		_, _ = pool.Exec(context.Background(), "DROP ROLE "+qr)
	})
	if _, err := pool.Exec(ctx, "GRANT USAGE ON SCHEMA "+qs+" TO "+qr+"; GRANT SELECT ON ALL TABLES IN SCHEMA "+qs+" TO "+qr); err != nil {
		t.Fatal(err)
	}
	cfg := pool.Config()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, err := c.Exec(ctx, "SET ROLE "+qr); return err }
	reader, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	if len(consistencyRead(t, ctx, reader, s).Members) != 1 {
		t.Fatal("ordinary unfiltered reader failed")
	}
	for _, table := range []string{"canonical_proposition_bindings", "canonical_proposition_binding_events", "canonical_propositions", "canonical_graph_nodes", "canonical_graph_edges", "canonical_derivations", "canonical_derivation_parents"} {
		t.Run(table, func(t *testing.T) {
			qt := pgx.Identifier{table}.Sanitize()
			if _, err := pool.Exec(ctx, "ALTER TABLE "+qt+" ENABLE ROW LEVEL SECURITY"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := pool.Exec(ctx, "ALTER TABLE "+qt+" DISABLE ROW LEVEL SECURITY"); err != nil {
					t.Error(err)
				}
			}()
			_, err := ReadConsistencyScope(ctx, reader, s)
			// Derivation tables are only read for derived members, so add one
			// before these cases rather than pretending an unqueried table matters.
			if table == "canonical_derivations" || table == "canonical_derivation_parents" {
				if _, e := pool.Exec(ctx, "ALTER TABLE "+qt+" DISABLE ROW LEVEL SECURITY"); e != nil {
					t.Fatal(e)
				}
				v := consistencyRead(t, ctx, pool, s)
				consistencyClaim(t, ctx, pool, s, "derived-"+table, v.Members[0].NodeID)
				if _, e := pool.Exec(ctx, "ALTER TABLE "+qt+" ENABLE ROW LEVEL SECURITY"); e != nil {
					t.Fatal(e)
				}
				_, err = ReadConsistencyScope(ctx, reader, s)
			}
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
				t.Fatal(fmt.Sprintf("RLS must fail closed, got %v", err))
			}
		})
	}
}
