//go:build integration && consistencylab

package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
)

// This explicit acceptance target fails, rather than skips, when prerequisites
// are absent. Ordinary integration tests exercise the reader without a solver.
func TestIntegrationConsistencySolverLab(t *testing.T) {
	root := os.Getenv("AHE_CONSISTENCY_REPORT_DIR")
	if root == "" || os.Getenv("DATABASE_DSN") == "" {
		t.Fatal("explicit report directory and disposable DATABASE_DSN required")
	}
	cfg := cadical.Config{SolverPath: os.Getenv("AHE_CONSISTENCY_CADICAL"), CheckerPath: os.Getenv("AHE_CONSISTENCY_DRAT"),
		SolverSHA256:  "338a9bec6d24cdd04b117b63504e804f7ce4c0605370fe21cc5b8a98c25073df",
		CheckerSHA256: "6dfac4c795691b620c0d8adf359f67232e65e27782c9cd88e4d4d4d28342dc62", ArtifactDir: filepath.Join(root, "solver")}
	runner, err := cadical.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, pool := integrationPoolWithMigrations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p := consistencyLiteral("door", "open", "noon", false)
	np := p
	np.Negated = true
	q := consistencyLiteral("door", "locked", "noon", false)
	nq := q
	nq.Negated = true
	tests := []struct {
		name   string
		claims [][][]ConsistencyLiteral
		rules  []ConsistencyCondition
		want   ConsistencyOutcome
	}{
		{"disconnected_opposites", [][][]ConsistencyLiteral{{{p}}, {{np}}}, nil, ConsistencyConflict},
		{"three_way_conflict", [][][]ConsistencyLiteral{{{p}}, {{q}}, {{np, nq}}}, nil, ConsistencyConflict},
		{"three_way_pair_1", [][][]ConsistencyLiteral{{{p}}, {{q}}}, nil, ConsistencyCompatible},
		{"three_way_pair_2", [][][]ConsistencyLiteral{{{p}}, {{np, nq}}}, nil, ConsistencyCompatible},
		{"three_way_pair_3", [][][]ConsistencyLiteral{{{q}}, {{np, nq}}}, nil, ConsistencyCompatible},
		{"different_subjects", [][][]ConsistencyLiteral{{{p}}, {{consistencyLiteral("window", "open", "noon", true)}}}, nil, ConsistencyCompatible},
		{"different_contexts", [][][]ConsistencyLiteral{{{p}}, {{consistencyLiteral("door", "open", "night", true)}}}, nil, ConsistencyCompatible},
		{"rule_and_evidence_conflict", [][][]ConsistencyLiteral{{{p}}, {{q}}}, []ConsistencyCondition{{ID: "not-both", Clauses: [][]ConsistencyLiteral{{np, nq}}}}, ConsistencyConflict},
		{"rules_already_conflict", [][][]ConsistencyLiteral{{{q}}}, []ConsistencyCondition{{ID: "bad-rules", Clauses: [][]ConsistencyLiteral{{p}, {np}}}}, ConsistencyInvalidRules},
	}
	save := func(t *testing.T, name string, in ConsistencyRequest, out ConsistencyResult, err error) {
		t.Helper()
		record := struct {
			Input  ConsistencyRequest
			Result ConsistencyResult
			Error  string
		}{Input: in, Result: out}
		if err != nil {
			record.Error = err.Error()
		}
		data, e := json.MarshalIndent(record, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		f, e := os.OpenFile(filepath.Join(root, name+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(data); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := ConsistencyScope{Namespace: "consistency", ScopeRef: tc.name}
			in := ConsistencyRequest{Scope: s, RuleVersion: "fixture/v1", Rules: tc.rules}
			for i, clauses := range tc.claims {
				node, _ := consistencyClaim(t, ctx, pool, s, fmt.Sprintf("%s-%d", tc.name, i))
				in.Evidence = append(in.Evidence, ConsistencyCondition{ID: node, Clauses: clauses})
			}
			v := consistencyRead(t, ctx, pool, s)
			in.ExpectedViewID = v.ID
			rules, all, e := compileConsistency(v, in)
			if e != nil {
				t.Fatal(e)
			}
			oracle := ConsistencyCompatible
			if !consistencyTruthTable(rules) {
				oracle = ConsistencyInvalidRules
			} else if !consistencyTruthTable(all) {
				oracle = ConsistencyConflict
			}
			if oracle != tc.want {
				t.Fatal("fixture and independent truth table disagree")
			}
			out, e := CheckCanonicalConsistency(ctx, pool, runner, tc.name, in)
			save(t, tc.name, in, out, e)
			if e != nil || out.Outcome != tc.want {
				t.Fatalf("outcome=%s want=%s err=%v", out.Outcome, tc.want, e)
			}
			if tc.want == ConsistencyInvalidRules {
				if out.Declarations.InputSHA256 != "" {
					t.Fatal("blamed evidence despite invalid rules")
				}
			} else if out.Declarations.Status == logicresolver.UNSAT && !out.Declarations.Proof.Checked {
				t.Fatal("unverified conflict")
			}
		})
	}
	t.Run("stale_view_and_exact_coverage", func(t *testing.T) {
		s := ConsistencyScope{Namespace: "consistency", ScopeRef: "lifecycle"}
		a, ka := consistencyClaim(t, ctx, pool, s, "lifecycle-a")
		in := ConsistencyRequest{Scope: s, ExpectedViewID: consistencyRead(t, ctx, pool, s).ID, RuleVersion: "v1", Evidence: []ConsistencyCondition{{ID: a, Clauses: [][]ConsistencyLiteral{{p}}}}}
		initial, e := CheckCanonicalConsistency(ctx, pool, runner, "initial", in)
		save(t, "initial", in, initial, e)
		if e != nil || initial.Outcome != ConsistencyCompatible {
			t.Fatal(e, initial.Outcome)
		}
		b, kb := consistencyClaim(t, ctx, pool, s, "lifecycle-b")
		stale, e := CheckCanonicalConsistency(ctx, pool, runner, "appended-stale", in)
		save(t, "appended-stale", in, stale, e)
		if e != nil || stale.Outcome != ConsistencyInconclusive || stale.Reason != "stale_view" {
			t.Fatal(e, stale.Outcome, stale.Reason)
		}
		in.ExpectedViewID = consistencyRead(t, ctx, pool, s).ID
		missing, e := CheckCanonicalConsistency(ctx, pool, runner, "missing", in)
		save(t, "missing", in, missing, e)
		if !errors.Is(e, logicresolver.ErrInput) || missing.Outcome != ConsistencyInconclusive {
			t.Fatal("missing annotation accepted")
		}
		in.Evidence = append(in.Evidence, ConsistencyCondition{ID: b, Clauses: [][]ConsistencyLiteral{{np}}})
		conflict, e := CheckCanonicalConsistency(ctx, pool, runner, "lifecycle-conflict", in)
		save(t, "lifecycle-conflict", in, conflict, e)
		if e != nil || conflict.Outcome != ConsistencyConflict {
			t.Fatal(e, conflict.Outcome)
		}
		changed := in
		changed.RuleVersion = "v2"
		version, e := CheckCanonicalConsistency(ctx, pool, runner, "rule-version", changed)
		save(t, "rule-version", changed, version, e)
		if e != nil || version.InputID == conflict.InputID {
			t.Fatal("rule version lost")
		}
		changed = in
		changed.Rules = []ConsistencyCondition{{ID: "explicit-rule", Clauses: [][]ConsistencyLiteral{{q, nq}}}}
		content, e := CheckCanonicalConsistency(ctx, pool, runner, "rule-content", changed)
		save(t, "rule-content", changed, content, e)
		if e != nil || content.InputID == conflict.InputID {
			t.Fatal("rule content lost")
		}
		if _, e := ChangePropositionBinding(ctx, pool, propositionTestChange(b, "lifecycle-withdraw", 0, "membership:lifecycle-b", kb.ID(), "withdraw", PropositionKey{})); e != nil {
			t.Fatal(e)
		}
		withdrawn, e := CheckCanonicalConsistency(ctx, pool, runner, "withdrawn-stale", in)
		save(t, "withdrawn-stale", in, withdrawn, e)
		if e != nil || withdrawn.Reason != "stale_view" {
			t.Fatal("withdrawal did not invalidate")
		}
		in.ExpectedViewID = consistencyRead(t, ctx, pool, s).ID
		in.Evidence = in.Evidence[:1]
		fresh, e := CheckCanonicalConsistency(ctx, pool, runner, "withdrawn-fresh", in)
		save(t, "withdrawn-fresh", in, fresh, e)
		if e != nil || fresh.Outcome != ConsistencyCompatible {
			t.Fatal(e, fresh.Outcome)
		}
		target := ka
		target.ScopeRef = "moved"
		if _, e := ChangePropositionBinding(ctx, pool, propositionTestChange(a, "lifecycle-move", 0, "membership:lifecycle-a", ka.ID(), "correct", target)); e != nil {
			t.Fatal(e)
		}
		moved, e := CheckCanonicalConsistency(ctx, pool, runner, "moved-stale", in)
		save(t, "moved-stale", in, moved, e)
		if e != nil || moved.Reason != "stale_view" {
			t.Fatal("correction did not invalidate")
		}
		in.ExpectedViewID = consistencyRead(t, ctx, pool, s).ID
		in.Evidence = nil
		empty, e := CheckCanonicalConsistency(ctx, pool, runner, "empty", in)
		save(t, "empty", in, empty, e)
		if e != nil || empty.Outcome != ConsistencyInconclusive || empty.Reason != "empty_scope" {
			t.Fatal("empty scope accepted")
		}
	})
	t.Run("cancel_and_solver_failure", func(t *testing.T) {
		s := ConsistencyScope{Namespace: "consistency", ScopeRef: "negative"}
		a, _ := consistencyClaim(t, ctx, pool, s, "negative-a")
		in := ConsistencyRequest{Scope: s, ExpectedViewID: consistencyRead(t, ctx, pool, s).ID, RuleVersion: "v1", Evidence: []ConsistencyCondition{{ID: a, Clauses: [][]ConsistencyLiteral{{p}, {np}}}}}
		stopped, cancel := context.WithCancel(ctx)
		cancel()
		out, e := CheckCanonicalConsistency(stopped, pool, runner, "cancel", in)
		save(t, "cancel", in, out, e)
		if !errors.Is(e, context.Canceled) || out.Outcome != ConsistencyInconclusive {
			t.Fatal("cancel accepted")
		}
		out, e = CheckCanonicalConsistency(ctx, pool, runner, "negative", in)
		save(t, "negative", in, out, e)
		if e != nil || out.Outcome != ConsistencyConflict {
			t.Fatal(e, out.Outcome)
		}
		out, e = CheckCanonicalConsistency(ctx, pool, runner, "negative", in)
		save(t, "duplicate-query", in, out, e)
		if e == nil || out.Outcome != ConsistencyInconclusive {
			t.Fatal("solver failure accepted")
		}
		badChecker := filepath.Join(root, "bad-checker.sh")
		script := []byte("#!/bin/sh\necho 'proof rejected'\nexit 1\n")
		if e := os.WriteFile(badChecker, script, 0700); e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(script)
		bad := cfg
		bad.CheckerPath = badChecker
		bad.CheckerSHA256 = hex.EncodeToString(sum[:])
		bad.ArtifactDir = filepath.Join(root, "bad-checker-run")
		badRunner, e := cadical.New(bad)
		if e != nil {
			t.Fatal(e)
		}
		out, e = CheckCanonicalConsistency(ctx, pool, badRunner, "unverified", in)
		save(t, "unverified", in, out, e)
		if e == nil || out.Outcome != ConsistencyInconclusive || out.Declarations.Proof.Checked {
			t.Fatal("unchecked proof accepted")
		}
	})
}
