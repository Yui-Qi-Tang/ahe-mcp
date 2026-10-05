package evidenceingestion

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

func consistencyLiteral(subject, property, context string, negated bool) ConsistencyLiteral {
	return ConsistencyLiteral{Atom: ConsistencyAtom{subject, property, context}, Negated: negated}
}

func TestConsistencyEncoding(t *testing.T) {
	p := consistencyLiteral("door", "open", "noon", false)
	np := p
	np.Negated = true
	q := consistencyLiteral("door", "locked", "noon", false)
	nq := q
	nq.Negated = true
	tests := []struct {
		name    string
		clauses [][]ConsistencyLiteral
		want    bool
	}{
		{"opposites", [][]ConsistencyLiteral{{p}, {np}}, false},
		{"three_way", [][]ConsistencyLiteral{{p}, {q}, {np, nq}}, false},
		{"different_subjects", [][]ConsistencyLiteral{{p}, {consistencyLiteral("window", "open", "noon", true)}}, true},
		{"different_contexts", [][]ConsistencyLiteral{{p}, {consistencyLiteral("door", "open", "night", true)}}, true},
		{"tuple_boundaries", [][]ConsistencyLiteral{{consistencyLiteral("a:b", "c", "d", false)}, {consistencyLiteral("a", "b:c", "d", true)}}, true},
		{"empty_clause", [][]ConsistencyLiteral{{}}, false},
		{"tautology", [][]ConsistencyLiteral{{p, np}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := ConsistencyView{Members: []ConsistencyMember{{NodeID: "evidence"}}}
			_, cnf, err := compileConsistency(view, ConsistencyRequest{Evidence: []ConsistencyCondition{{ID: "evidence", Clauses: tc.clauses}}})
			if err != nil {
				t.Fatal(err)
			}
			if got := consistencyTruthTable(cnf); got != tc.want {
				t.Fatalf("satisfiable=%v, want %v", got, tc.want)
			}
			for _, origin := range cnf.Origins {
				if !slices.Equal(origin, []string{"node:evidence"}) {
					t.Fatal(origin)
				}
			}
		})
	}
}

// Deliberately independent from CheckAssignment and the external solver.
func consistencyTruthTable(cnf logicresolver.CNF) bool {
	for assignment := 0; assignment < 1<<len(cnf.Variables); assignment++ {
		all := true
		for _, clause := range cnf.Clauses {
			any := false
			for _, lit := range clause {
				id := lit
				if id < 0 {
					id = -id
				}
				value := assignment&(1<<(id-1)) != 0
				if (lit > 0 && value) || (lit < 0 && !value) {
					any = true
				}
			}
			if !any {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func TestConsistencyRejectsIncompleteNormalization(t *testing.T) {
	good := ConsistencyCondition{ID: "a", Clauses: [][]ConsistencyLiteral{{consistencyLiteral("s", "p", "c", false)}}}
	view := ConsistencyView{Members: []ConsistencyMember{{NodeID: "a"}, {NodeID: "b"}}}
	for _, tc := range []struct {
		name     string
		evidence []ConsistencyCondition
	}{
		{"missing", []ConsistencyCondition{good}},
		{"duplicate", []ConsistencyCondition{good, good}},
		{"extra", []ConsistencyCondition{good, {ID: "outside", Clauses: good.Clauses}}},
		{"no_clauses", []ConsistencyCondition{good, {ID: "b"}}},
		{"invalid_atom", []ConsistencyCondition{good, {ID: "b", Clauses: [][]ConsistencyLiteral{{{}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := compileConsistency(view, ConsistencyRequest{Evidence: tc.evidence}); !errors.Is(err, logicresolver.ErrInput) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestConsistencyConditionOrdering(t *testing.T) {
	view := ConsistencyView{Members: []ConsistencyMember{{NodeID: "a"}, {NodeID: "b"}}}
	p, q := consistencyLiteral("s", "p", "c", false), consistencyLiteral("s", "q", "c", true)
	a := ConsistencyCondition{ID: "a", Clauses: [][]ConsistencyLiteral{{p, q}}}
	b := ConsistencyCondition{ID: "b", Clauses: [][]ConsistencyLiteral{{p}}}
	_, first, err := compileConsistency(view, ConsistencyRequest{Evidence: []ConsistencyCondition{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	a.Clauses = [][]ConsistencyLiteral{{q, p}}
	_, second, err := compileConsistency(view, ConsistencyRequest{Evidence: []ConsistencyCondition{b, a}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("condition/literal ordering changed encoding")
	}
}

func TestConsistencyScopeValidation(t *testing.T) {
	for _, s := range []ConsistencyScope{{}, {Namespace: "a", ScopeRef: " ", MaxNodes: 1}, {Namespace: "a", ScopeRef: "b", MaxNodes: 257}, {Namespace: "a", ScopeRef: "b", MaxEdges: -1}} {
		if _, err := s.normalized(); !errors.Is(err, logicresolver.ErrInput) {
			t.Fatalf("accepted %+v", s)
		}
	}
	view := ConsistencyView{Profile: ConsistencyProfile, Members: []ConsistencyMember{{NodeID: "a", HeadRef: "membership:initial"}}}
	a, _ := consistencyHash(view)
	view.Snapshot = "another transaction"
	b, _ := consistencyHash(view)
	if a != b {
		t.Fatal("transaction receipt changed content identity")
	}
	view.Members[0].HeadRef = "event:corrected"
	b, _ = consistencyHash(view)
	if a == b {
		t.Fatal("binding head not hashed")
	}
}
