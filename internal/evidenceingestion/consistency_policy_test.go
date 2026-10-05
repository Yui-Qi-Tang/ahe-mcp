package evidenceingestion

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
)

func TestConsistencyEligibility(t *testing.T) {
	a := evidencegraph.CanonicalArtifact{}
	for _, id := range []string{"old", "current", "branch", "unknown", "derived-old", "derived-current", "derived-unknown"} {
		kind := evidencegraph.CanonicalSourceClaim
		if len(id) > 8 && id[:8] == "derived-" {
			kind = evidencegraph.CanonicalDerivedClaim
		}
		a.Nodes = append(a.Nodes, evidencegraph.CanonicalNode{ID: id, Kind: kind, TemporalRef: id})
		a.Temporal = append(a.Temporal, evidencegraph.TemporalRecord{ID: id, Status: evidencegraph.TemporalUnknown})
	}
	a.Derivations = []evidencegraph.DerivationRecord{{NodeID: "derived-old", Parents: []string{"old", "current"}}, {NodeID: "derived-current", Parents: []string{"current", "branch"}}, {NodeID: "derived-unknown", Parents: []string{"current", "unknown"}}}
	status := map[string]evidencesupersession.CurrentnessStatus{"old": evidencesupersession.CurrentnessSuperseded, "current": evidencesupersession.CurrentnessCurrent, "branch": evidencesupersession.CurrentnessAmbiguous}
	got, err := consistencyEligibility(a, nil, status, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"old": "excluded", "current": "included", "branch": "included", "unknown": "blocked", "derived-old": "excluded", "derived-current": "included", "derived-unknown": "blocked"}
	for _, s := range got {
		if s.State != want[s.NodeID] {
			t.Fatalf("%s = %s", s.NodeID, s.State)
		}
	}
	for _, tc := range []struct{ name, from, to, state string }{
		{"expired", "", "2026-10-05T00:00:00Z", "excluded"},
		{"future", "2026-10-06T00:00:00Z", "", "excluded"},
		{"reversed_window", "2026-10-06T00:00:00Z", "2026-10-04T00:00:00Z", "blocked"},
		{"empty_window", "2026-10-05T00:00:00Z", "2026-10-05T00:00:00Z", "blocked"},
		{"bad_time", "tomorrow", "", "blocked"},
		{"window", "2026-10-04T00:00:00Z", "2026-10-06T00:00:00Z", "included"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			one := evidencegraph.CanonicalArtifact{Nodes: a.Nodes[1:2], Temporal: []evidencegraph.TemporalRecord{{ID: "current", ValidFrom: tc.from, ValidTo: tc.to}}}
			got, err := consistencyEligibility(one, nil, status, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
			if err != nil || got[0].State != tc.state {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	a.Derivations[0].Parents = []string{"absent"}
	if _, err := consistencyEligibility(a, nil, status, time.Now()); !errors.Is(err, logicresolver.ErrInput) {
		t.Fatal("missing parent", err)
	}
}

func TestConsistencyLocalizationTruthTable(t *testing.T) {
	// Evaluate each returned subset independently; no production solver/oracle reuse.
	cnf := logicresolver.CNF{Variables: []logicresolver.Variable{{ID: 1}, {ID: 2}, {ID: 3}}, Clauses: [][]int{{1}, {2}, {-1, -2}, {3}}, Origins: [][]string{{"node:a"}, {"node:b"}, {"rule:a"}, {"node:unrelated"}}}
	checked := func(c logicresolver.CNF) cadical.Result {
		r := cadical.Result{Result: logicresolver.Result{InputSHA256: "input", FormulaSHA256: "formula", Status: logicresolver.UNSAT, Proof: logicresolver.Validation{Present: true, Checked: true, TargetSHA256: "formula"}}}
		if consistencyTruthTable(c) {
			r.Status = logicresolver.SAT
			r.Model = logicresolver.Validation{Present: true, Decoded: true, Checked: true, TargetSHA256: "input"}
		}
		return r
	}
	for _, tc := range []struct {
		name    string
		budget  int
		failure bool
		minimal bool
	}{{"minimal", 64, false, true}, {"budget", 1, false, false}, {"solver_failure", 64, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			out := localizeConsistency(context.Background(), cnf, checked(cnf), tc.budget, func(_ context.Context, _ int, c logicresolver.CNF) (cadical.Result, error) {
				if tc.failure {
					return cadical.Result{}, logicresolver.ErrProtocol
				}
				return checked(c), nil
			})
			if out.Minimal != tc.minimal {
				t.Fatalf("%+v", out)
			}
			if out.Minimal && !slices.Equal(out.Conditions, []string{"node:a", "node:b", "rule:a"}) {
				t.Fatal(out.Conditions)
			}
			selected := func(remove string) logicresolver.CNF {
				c := logicresolver.CNF{Variables: cnf.Variables}
				for i, o := range cnf.Origins {
					if slices.Contains(out.Conditions, o[0]) && o[0] != remove {
						c.Clauses = append(c.Clauses, cnf.Clauses[i])
					}
				}
				return c
			}
			if consistencyTruthTable(selected("")) {
				t.Fatal("returned subset is satisfiable")
			}
			if out.Minimal {
				for _, id := range out.Conditions {
					if !consistencyTruthTable(selected(id)) {
						t.Fatal("not minimal", id)
					}
				}
			}
		})
	}
}

func TestConsistencyLocalizationUnknownAndCancellation(t *testing.T) {
	cnf := logicresolver.CNF{Variables: []logicresolver.Variable{{ID: 1}}, Clauses: [][]int{{1}, {-1}}, Origins: [][]string{{"node:a"}, {"node:b"}}}
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		out := localizeConsistency(ctx, cnf, cadical.Result{}, 64, func(context.Context, int, logicresolver.CNF) (cadical.Result, error) {
			return cadical.Result{Result: logicresolver.Result{Status: logicresolver.Unknown}}, nil
		})
		cancel()
		if out.Minimal || !slices.Equal(out.Conditions, []string{"node:a", "node:b"}) {
			t.Fatal(out)
		}
		want := "resolver_failed"
		if cancelled {
			want = "cancelled"
		}
		if out.Reason != want {
			t.Fatal(out.Reason)
		}
	}
}
