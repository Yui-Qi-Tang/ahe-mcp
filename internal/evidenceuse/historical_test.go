package evidenceuse

import (
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestHistoricalPurposeIsPermanentAndDoesNotEraseConflict(t *testing.T) {
	for _, policy := range []Policy{CurrentActionV1, GroundedActionV1} {
		s := fixture()
		derive(&s, "historical", "a", "b")
		derive(&s, "child", "historical")
		before, err := s.Digest()
		if err != nil {
			t.Fatal(err)
		}
		change(&s, "historical", func(n *Node) { n.UseClass = evidencegraph.DerivationHistoricalOnly })
		after, err := s.Digest()
		if err != nil || before == after {
			t.Fatal("purpose absent from digest", err)
		}
		a, err := Assess(s, policy, []string{"child"}, testTime)
		if err != nil {
			t.Fatal(err)
		}
		if outcome(t, a, "historical") != Excluded || outcome(t, a, "child") != Excluded {
			t.Fatal("historical AND cleared", a)
		}
		s.Contradictions = []Contradiction{{ID: "conflict", Left: "c", Right: "historical", ReviewRef: "review", Scope: "synthetic"}}
		a, err = Assess(s, policy, []string{"c"}, testTime)
		if err != nil || outcome(t, a, "c") != Blocked {
			t.Fatal("historical purpose erased conflict", err)
		}
		if !slices.ContainsFunc(a.Nodes[0].Reasons, func(r Reason) bool { return r.Code == "active_contradiction" }) {
			t.Fatal("conflict reason missing", a)
		}
		change(&s, "historical", func(n *Node) { n.UseClass = "unknown" })
		a, err = Assess(s, policy, []string{"child"}, testTime)
		if err != nil || outcome(t, a, "child") == Eligible {
			t.Fatal("unknown use class cleared", err)
		}
	}
}
