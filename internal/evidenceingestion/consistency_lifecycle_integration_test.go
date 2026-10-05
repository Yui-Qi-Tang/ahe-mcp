//go:build integration && consistencylab

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
	"github.com/jackc/pgx/v5/pgxpool"
)

func consistencyLifecycleRunner(t *testing.T) *cadical.Runner {
	t.Helper()
	root := os.Getenv("AHE_CONSISTENCY_REPORT_DIR")
	if root == "" || os.Getenv("DATABASE_DSN") == "" {
		t.Fatal("explicit report directory and disposable DATABASE_DSN required")
	}
	r, err := cadical.New(cadical.Config{SolverPath: os.Getenv("AHE_CONSISTENCY_CADICAL"), CheckerPath: os.Getenv("AHE_CONSISTENCY_DRAT"), SolverSHA256: "338a9bec6d24cdd04b117b63504e804f7ce4c0605370fe21cc5b8a98c25073df", CheckerSHA256: "6dfac4c795691b620c0d8adf359f67232e65e27782c9cd88e4d4d4d28342dc62", ArtifactDir: filepath.Join(root, "lifecycle-"+randomHex(t, 8))})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func consistencyLifecycleRecord(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("AHE_CONSISTENCY_REPORT_DIR"), name+"-"+randomHex(t, 8)+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func consistencyLifecycleChain(t *testing.T) (context.Context, *pgxpool.Pool, ConsistencyScope, string, SupersessionAdmissionResult) {
	t.Helper()
	ctx, pool := integrationPoolWithMigrations(t)
	scope := ConsistencyScope{Namespace: "lifecycle", ScopeRef: "release", Policy: ConsistencyFrontierPolicy}
	p := createSupersessionExternalProposal(t, ctx, pool, "lifecycle-old", "1", "Door is closed.")
	old, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic old declaration"})
	if err != nil {
		t.Fatal(err)
	}
	p = createSupersessionExternalProposal(t, ctx, pool, "lifecycle-new", "2", "Door is open.")
	current, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic replacement", Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{old.CanonicalRef}})
	if err != nil {
		t.Fatal(err)
	}
	for i, node := range []string{old.CanonicalRef, current.CanonicalRef} {
		key := PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: []string{"old", "new"}[i], Revision: "1"}
		propositionTestBind(t, ctx, pool, node, "bind-"+key.LocalID, key)
	}
	return ctx, pool, scope, old.CanonicalRef, current
}

func TestIntegrationConsistencyFrontier(t *testing.T) {
	t.Run("superseded_opponent_and_distinct_derivations", func(t *testing.T) {
		ctx, pool, scope, old, current := consistencyLifecycleChain(t)
		oldDerived, oldKey := consistencyClaim(t, ctx, pool, scope, "old-derived", old)
		newDerived, newKey := consistencyClaim(t, ctx, pool, scope, "new-derived", current.CanonicalRef)
		correction := propositionTestChange(newDerived, "shared-proposition", 0, "membership:new-derived", newKey.ID(), "correct", oldKey)
		correction.Definition = "Synthetic declaration old-derived"
		if _, err := ChangePropositionBinding(ctx, pool, correction); err != nil {
			t.Fatal(err)
		}
		view := consistencyRead(t, ctx, pool, scope)
		want := map[string]string{old: "excluded", current.CanonicalRef: "included", oldDerived: "excluded", newDerived: "included"}
		for _, s := range view.Selection {
			if s.State != want[s.NodeID] {
				t.Fatal(s)
			}
		}
		p := consistencyLiteral("door", "open", "prod", false)
		in := ConsistencyRequest{Scope: scope, ExpectedViewID: view.ID, RuleVersion: "1", Evidence: []ConsistencyCondition{{ID: current.CanonicalRef, Clauses: [][]ConsistencyLiteral{{p}}}, {ID: newDerived, Clauses: [][]ConsistencyLiteral{{p}}}}}
		out, err := DiagnoseCanonicalConsistency(ctx, pool, consistencyLifecycleRunner(t), "current", in, 0)
		if err != nil || out.Outcome != ConsistencyCompatible {
			t.Fatal(out.Outcome, err)
		}
		consistencyLifecycleRecord(t, "same-proposition-currentness", out)
		if len(out.View.Artifact.Derivations) != 2 {
			t.Fatal("lost excluded derivation history")
		}
	})
	t.Run("branch_merge_and_unclassified_object", func(t *testing.T) {
		ctx, pool, scope, old, current := consistencyLifecycleChain(t)
		p := createSupersessionExternalProposal(t, ctx, pool, "lifecycle-branch", "3", "Door is closed in another branch.")
		branch, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic alternative", Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{old}, ExpectedRevision: current.EventRevision, ExpectedHeadEventID: current.AdmissionEventID})
		if err != nil {
			t.Fatal(err)
		}
		key := PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: "branch", Revision: "1"}
		propositionTestBind(t, ctx, pool, branch.CanonicalRef, "bind-branch", key)
		view := consistencyRead(t, ctx, pool, scope)
		for _, s := range view.Selection {
			if s.NodeID != old && (s.State != "included" || s.Reason != "ambiguous_frontier") {
				t.Fatal(s)
			}
		}
		lit := consistencyLiteral("door", "open", "prod", false)
		neg := lit
		neg.Negated = true
		in := ConsistencyRequest{Scope: scope, ExpectedViewID: view.ID, RuleVersion: "1", Evidence: []ConsistencyCondition{{ID: current.CanonicalRef, Clauses: [][]ConsistencyLiteral{{lit}}}, {ID: branch.CanonicalRef, Clauses: [][]ConsistencyLiteral{{neg}}}}}
		out, err := DiagnoseCanonicalConsistency(ctx, pool, consistencyLifecycleRunner(t), "branch", in, 0)
		if err != nil || out.Outcome != ConsistencyConflict || !out.Localization.Minimal || len(out.Localization.Conditions) != 2 {
			t.Fatal(out, err)
		}

		snapshotTx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer snapshotTx.Rollback(context.Background())
		normalized, _ := scope.normalized()
		beforeMerge, err := readConsistencyScopeTx(ctx, snapshotTx, normalized)
		if err != nil {
			t.Fatal(err)
		}
		// Merging both frontiers excludes them without deleting their history.
		p = createSupersessionExternalProposal(t, ctx, pool, "lifecycle-merged", "4", "Door policy is merged.")
		merged, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic merge", Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{current.CanonicalRef, branch.CanonicalRef}, ExpectedRevision: branch.EventRevision, ExpectedHeadEventID: branch.AdmissionEventID})
		if err != nil {
			t.Fatal(err)
		}
		stillBefore, err := readConsistencyScopeTx(ctx, snapshotTx, normalized)
		if err != nil || stillBefore.ID != beforeMerge.ID {
			t.Fatal("currentness mixed snapshots", err)
		}
		if err := snapshotTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		// The replacement is initially outside the proposition scope.
		outside := consistencyRead(t, ctx, pool, scope)
		if outside.ID == view.ID {
			t.Fatal("out-of-scope replacement not fingerprinted")
		}
		for _, s := range outside.Selection {
			if s.State != "excluded" {
				t.Fatal(s)
			}
		}
		key.LocalID = "merged"
		propositionTestBind(t, ctx, pool, merged.CanonicalRef, "bind-merged", key)
		view = consistencyRead(t, ctx, pool, scope)
		in.ExpectedViewID = view.ID
		in.Evidence = []ConsistencyCondition{{ID: merged.CanonicalRef, Clauses: [][]ConsistencyLiteral{{lit}}}}
		out, err = DiagnoseCanonicalConsistency(ctx, pool, consistencyLifecycleRunner(t), "merged", in, 0)
		if err != nil || out.Outcome != ConsistencyCompatible {
			t.Fatal(out.Outcome, err)
		}
		// A newly admitted unclassified claim is outside the bound scope, but
		// must still invalidate the source-object completeness witness.
		p = createSupersessionExternalProposal(t, ctx, pool, "lifecycle-unclassified", "5", "Additional unclassified statement.")
		_, err = AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic"})
		if err != nil {
			t.Fatal(err)
		}
		later := consistencyRead(t, ctx, pool, scope)
		if later.ID == view.ID {
			t.Fatal("out-of-scope classification change did not invalidate view")
		}
		in.ExpectedViewID = later.ID
		out, err = DiagnoseCanonicalConsistency(ctx, pool, consistencyLifecycleRunner(t), "unknown", in, 0)
		if err != nil || out.Outcome != ConsistencyInconclusive || out.Reason != "unknown_currentness" {
			t.Fatal(out.Outcome, out.Reason, err)
		}
	})
}

func TestIntegrationConsistencyLocalization(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	scope := ConsistencyScope{Namespace: "localization", ScopeRef: "three-way"}
	p := consistencyLiteral("door", "open", "prod", false)
	q := consistencyLiteral("door", "locked", "prod", false)
	np, nq := p, q
	np.Negated = true
	nq.Negated = true
	conditions := [][][]ConsistencyLiteral{{{p}}, {{q}}, {{np, nq}}, {{consistencyLiteral("window", "open", "prod", false)}}}
	in := ConsistencyRequest{Scope: scope, RuleVersion: "1"}
	var want []string
	for i, name := range []string{"open", "locked", "not-both", "unrelated"} {
		node, _ := consistencyClaim(t, ctx, pool, scope, name)
		in.Evidence = append(in.Evidence, ConsistencyCondition{ID: node, Clauses: conditions[i]})
		if i < 3 {
			want = append(want, "node:"+node)
		}
	}
	in.ExpectedViewID = consistencyRead(t, ctx, pool, scope).ID
	r := consistencyLifecycleRunner(t)
	out, err := DiagnoseCanonicalConsistency(ctx, pool, r, "three-way", in, 0)
	slices.Sort(want)
	if err != nil || out.Outcome != ConsistencyConflict || !out.Localization.Minimal || !slices.Equal(want, out.Localization.Conditions) {
		t.Fatal(out.Localization, err)
	}
	consistencyLifecycleRecord(t, "three-way-localization", out)
	limited, err := DiagnoseCanonicalConsistency(ctx, pool, r, "limited", in, 1)
	if err != nil || limited.Outcome != ConsistencyConflict || limited.Localization.Minimal || limited.Localization.Reason != "budget_exhausted" {
		t.Fatal(limited.Localization, err)
	}
	consistencyLifecycleRecord(t, "budget-limited-localization", limited)
	in.Rules = []ConsistencyCondition{{ID: "bad-rule", Clauses: [][]ConsistencyLiteral{{p}, {np}}}}
	bad, err := DiagnoseCanonicalConsistency(ctx, pool, r, "rules", in, 0)
	if err != nil || bad.Outcome != ConsistencyInvalidRules || !bad.Localization.Minimal || !slices.Equal(bad.Localization.Conditions, []string{"rule:bad-rule"}) {
		t.Fatal(bad.Localization, err)
	}
}

func TestIntegrationConsistencyPolicyRLS(t *testing.T) {
	ctx, pool, scope, _, _ := consistencyLifecycleChain(t)
	role := "consistency_policy_reader_" + randomHex(t, 6)
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
	consistencyRead(t, ctx, reader, scope)
	for _, table := range []string{"canonical_supersession_lineages", "canonical_supersession_members", "canonical_supersession_admission_events", "canonical_supersession_replacement_targets", "source_snapshots"} {
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
			_, err := ReadConsistencyScope(ctx, reader, scope)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
				t.Fatal("partial currentness accepted", err)
			}
		})
	}
}
