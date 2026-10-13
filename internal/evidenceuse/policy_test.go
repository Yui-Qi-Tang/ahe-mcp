package evidenceuse

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
)

var testTime = time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)

func fixture() Snapshot {
	s := Snapshot{Version: ProjectionV1, StoreID: "synthetic-store", CutID: "cut:1", Nodes: map[string]Node{}, Sources: map[string]Source{}}
	for _, id := range []string{"a", "b", "c"} {
		sourceID := "source:" + id
		s.Nodes[id] = Node{
			Kind: evidencegraph.CanonicalSourceClaim, Admission: "admitted",
			Withdrawal:      Withdrawal{Status: WithdrawalNone, Complete: true},
			Currentness:     Currentness{Complete: true},
			Temporal:        evidencegraph.TemporalRecord{Status: evidencegraph.TemporalCurrent},
			ParentsComplete: true, SourcesComplete: true, ContradictionsComplete: true,
			SourceIDs: []string{sourceID},
		}
		s.Sources[sourceID] = Source{SnapshotID: sourceID, ContentHash: "sha256:" + id, ProviderRevision: "r1", DeclaredFidelity: "verbatim", DeclaredCoverage: "exact_excerpt", AssessmentRevision: 1, AssessmentsComplete: true,
			Checks: []SourceCheck{
				{AssessmentID: "bytes:" + id, Criterion: RetentionV1, Result: CheckPass, MaterialRef: "material:retained"},
				{AssessmentID: "correspondence:" + id, Criterion: CorrespondenceV1, Result: CheckPass, ComparedRevision: "r1", MaterialRef: "material:provider-r1"},
				{AssessmentID: "coverage:" + id, Criterion: CoverageV1, Result: CheckPass, ComparedRevision: "r1", MaterialRef: "material:provider-r1"},
			},
		}
		x := s.Sources[sourceID]
		for i := range x.Checks {
			x.Checks[i].SourceSnapshotID = sourceID
			x.Checks[i].ContentHash = x.ContentHash
			x.Checks[i].Scope = RetainedSnapshotScope
		}
		s.Sources[sourceID] = x
	}
	return s
}

func derive(s *Snapshot, id string, parents ...string) {
	n := s.Nodes["a"]
	n.Kind, n.SourceIDs, n.Parents = evidencegraph.CanonicalDerivedClaim, nil, parents
	s.Nodes[id] = n
}

func change(s *Snapshot, id string, f func(*Node)) {
	n := s.Nodes[id]
	f(&n)
	s.Nodes[id] = n
}

func withdraw(s *Snapshot, id string) {
	change(s, id, func(n *Node) {
		n.Withdrawal = Withdrawal{Status: WithdrawalWithdrawn, Complete: true, Revision: 1, EventID: "withdraw:" + id, Reason: "TEST fixture stop-use", CorrectionIDs: []string{"c"}}
	})
}

func assess(t *testing.T, s Snapshot, ids ...string) Assessment {
	t.Helper()
	a, err := Assess(s, GroundedActionV1, ids, testTime)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func outcome(t *testing.T, a Assessment, id string) Outcome {
	t.Helper()
	for _, n := range a.Nodes {
		if n.NodeID == id {
			return n.Outcome
		}
	}
	t.Fatalf("missing assessment for %s", id)
	return ""
}

func TestWithdrawalPropagatesThroughAllANDAncestors(t *testing.T) {
	s := fixture()
	derive(&s, "d", "a", "b")
	derive(&s, "e", "d", "c")
	if got := outcome(t, assess(t, s, "e"), "e"); got != Eligible {
		t.Fatal(got)
	}
	withdraw(&s, "a")
	before, _ := json.Marshal(s)
	a := assess(t, s, "e")
	for _, id := range []string{"a", "d", "e"} {
		if got := outcome(t, a, id); got != Excluded {
			t.Errorf("%s = %s", id, got)
		}
	}
	if outcome(t, a, "c") != Eligible {
		t.Fatal("independent correction was excluded")
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("assessment rewrote evidence or AND parents")
	}
	if !slices.Equal(s.Nodes["d"].Parents, []string{"a", "b"}) {
		t.Fatal("correction replaced a parent")
	}
}

func TestRestoreDoesNotClearSupersessionAndWithdrawalDoesNotReviveOldNode(t *testing.T) {
	s := fixture()
	change(&s, "a", func(n *Node) {
		n.Currentness = Currentness{LineageKey: "lineage:1", Status: evidencesupersession.CurrentnessSuperseded, Complete: true}
		n.Withdrawal = Withdrawal{Status: WithdrawalRestored, Complete: true, Revision: 2, EventID: "restore:a"}
	})
	withdraw(&s, "b") // The replacement's disposition cannot undo a's incoming edge.
	a := assess(t, s, "a", "b")
	if outcome(t, a, "a") != Excluded || outcome(t, a, "b") != Excluded {
		t.Fatal(a)
	}
}

func TestContradictionIsStableAndOnlyUsesExplicitPairs(t *testing.T) {
	s := fixture()
	s.Contradictions = []Contradiction{
		{ID: "ab", Left: "a", Right: "b", ReviewRef: "review:ab", Scope: "synthetic same scope"},
		{ID: "bc", Left: "b", Right: "c", ReviewRef: "review:bc", Scope: "synthetic same scope"},
	}
	first := assess(t, s, "a", "b", "c")
	for _, id := range []string{"a", "b", "c"} {
		if outcome(t, first, id) != Blocked {
			t.Fatal(first)
		}
	}
	if !reflect.DeepEqual(first, assess(t, s, "a", "b", "c")) {
		t.Fatal("conflict eligibility oscillates")
	}
	withdraw(&s, "b")
	a := assess(t, s, "a", "b", "c")
	if outcome(t, a, "a") != Eligible || outcome(t, a, "c") != Eligible || outcome(t, a, "b") != Excluded {
		t.Fatal(a)
	}
	if len(s.Contradictions) != 2 {
		t.Fatal("history was rewritten")
	}
}

func TestUnknownConflictPeerCannotBeTreatedAsHistorical(t *testing.T) {
	for _, kind := range []string{"missing", "withdrawal", "currentness", "ambiguous", "unclassified", "admission"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture()
			s.Contradictions = []Contradiction{{ID: "ab", Left: "a", Right: "b", ReviewRef: "review:ab"}}
			change(&s, "b", func(n *Node) {
				switch kind {
				case "withdrawal":
					n.Withdrawal.Complete = false
				case "currentness":
					n.Currentness.Complete = false
				case "ambiguous":
					n.Currentness = Currentness{Complete: true, LineageKey: "l", Status: evidencesupersession.CurrentnessAmbiguous}
				case "unclassified":
					n.Temporal.Status = evidencegraph.TemporalUnknown
				case "admission":
					n.Admission = ""
				}
			})
			if kind == "missing" {
				delete(s.Nodes, "b")
			}
			if got := outcome(t, assess(t, s, "a"), "a"); got != Blocked {
				t.Fatal(got)
			}
		})
	}
}

func TestSourceFindingFollowsSnapshotBindingsNotClaimCorrectness(t *testing.T) {
	s := fixture()
	change(&s, "b", func(n *Node) { n.SourceIDs = []string{"source:a"} })
	source := s.Sources["source:a"]
	source.Checks[1].Result = CheckFail
	// A pass about statement support must not clear a provider-correspondence failure.
	source.Checks = append(source.Checks, SourceCheck{AssessmentID: "statement", Criterion: "statement-support/v1", Result: CheckPass, ComparedRevision: "current", MaterialRef: "today"})
	s.Sources["source:a"] = source
	a := assess(t, s, "a", "b", "c")
	if outcome(t, a, "a") != Excluded || outcome(t, a, "b") != Excluded || outcome(t, a, "c") != Eligible {
		t.Fatal(a)
	}
	// Optional positive verification cannot erase a known negative finding.
	plain, err := Assess(s, CurrentActionV1, []string{"a"}, testTime)
	if err != nil || outcome(t, plain, "a") != Excluded {
		t.Fatal(plain, err)
	}
}

func TestOptionalVerificationDoesNotMeanUnknownGovernanceIsClear(t *testing.T) {
	s := fixture()
	x := s.Sources["source:a"]
	x.Checks = nil
	x.AssessmentRevision = 0
	s.Sources["source:a"] = x
	plain, err := Assess(s, CurrentActionV1, []string{"a"}, testTime)
	if err != nil || outcome(t, plain, "a") != Eligible {
		t.Fatal(plain, err)
	}
	if outcome(t, assess(t, s, "a"), "a") != Blocked {
		t.Fatal("declaration became verified")
	}
	x.AssessmentsComplete = false
	s.Sources["source:a"] = x
	plain, err = Assess(s, CurrentActionV1, []string{"a"}, testTime)
	if err != nil || outcome(t, plain, "a") != Blocked {
		t.Fatal("unread findings treated as absent", plain, err)
	}
}

func TestDeclaredLabelsAndWrongRevisionNeverSupplyMissingChecks(t *testing.T) {
	for _, kind := range []string{"none", "wrong_revision", "wrong_snapshot", "wrong_hash", "partial_scope", "no_provider_revision", "inconclusive", "disputed", "incomplete", "no_material", "statement_only"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture()
			x := s.Sources["source:a"]
			switch kind {
			case "none":
				x.Checks = nil
			case "wrong_revision":
				x.Checks[1].ComparedRevision = "today"
			case "wrong_snapshot":
				x.Checks[1].SourceSnapshotID = "source:c"
			case "wrong_hash":
				x.Checks[1].ContentHash = "sha256:other"
			case "partial_scope":
				x.Checks[1].Scope = "one-paragraph"
			case "no_provider_revision":
				x.ProviderRevision = ""
			case "inconclusive":
				x.Checks[1].Result = CheckInconclusive
			case "disputed":
				y := x.Checks[1]
				y.AssessmentID = "other-review"
				y.Result = CheckFail
				x.Checks = append(x.Checks, y)
			case "incomplete":
				x.AssessmentsComplete = false
			case "no_material":
				x.Checks[1].MaterialRef = ""
			case "statement_only":
				x.Checks = []SourceCheck{{AssessmentID: "statement", Criterion: "statement-support/v1", Result: CheckPass, MaterialRef: "today"}}
			}
			s.Sources["source:a"] = x
			if got := outcome(t, assess(t, s, "a"), "a"); got != Blocked {
				t.Fatal(got)
			}
		})
	}
}

func TestMissingGovernanceOrANDDataFailsClosed(t *testing.T) {
	for _, kind := range []string{"withdrawal", "currentness", "incident_edges", "source_bindings", "source", "parent_manifest", "parent_node", "empty_parents"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture()
			derive(&s, "d", "a", "b")
			change(&s, "d", func(n *Node) {
				switch kind {
				case "withdrawal":
					n.Withdrawal = Withdrawal{}
				case "currentness":
					n.Currentness = Currentness{}
				case "incident_edges":
					n.ContradictionsComplete = false
				case "source_bindings":
					n.SourcesComplete = false
				case "parent_manifest":
					n.ParentsComplete = false
				case "empty_parents":
					n.Parents = nil
				}
			})
			if kind == "parent_node" {
				delete(s.Nodes, "b")
			}
			if kind == "source" {
				delete(s.Sources, "source:a")
			}
			if got := outcome(t, assess(t, s, "d"), "d"); got != Blocked {
				t.Fatal(got)
			}
		})
	}
}

func TestANDCombinationIsOrderIndependentAndCyclesAreRejected(t *testing.T) {
	s := fixture()
	derive(&s, "d", "a", "b")
	withdraw(&s, "a")
	change(&s, "b", func(n *Node) { n.ContradictionsComplete = false })
	one := assess(t, s, "d")
	change(&s, "d", func(n *Node) { n.Parents = []string{"b", "a"} })
	two := assess(t, s, "d")
	if !reflect.DeepEqual(one, two) || outcome(t, two, "d") != Excluded {
		t.Fatal(one, two)
	}
	derive(&s, "e", "d")
	change(&s, "d", func(n *Node) { n.Parents = []string{"e"} })
	if _, err := Assess(s, GroundedActionV1, []string{"e"}, testTime); !errors.Is(err, ErrInvalidProjection) {
		t.Fatal(err)
	}
}

func TestAcceptanceReevaluatesTimeAndGovernance(t *testing.T) {
	s := fixture()
	change(&s, "a", func(n *Node) { n.Temporal.ValidTo = testTime.Add(time.Minute).Format(time.RFC3339Nano) })
	plan := []ActionBasis{{ActionID: "act", BasisIDs: []string{"a"}}}
	first, err := CheckPlan(s, GroundedActionV1, plan, testTime)
	if err != nil || !first.Accepted {
		t.Fatal(first, err)
	}
	atEnd, err := CheckPlan(s, GroundedActionV1, plan, testTime.Add(time.Minute))
	if err != nil || atEnd.Accepted || outcome(t, atEnd.Assessment, "a") != Excluded {
		t.Fatal(atEnd, err)
	}
	if first.Assessment.StateDigest != atEnd.Assessment.StateDigest {
		t.Fatal("time test changed the snapshot")
	}
	withdraw(&s, "a")
	last, err := CheckPlan(s, GroundedActionV1, plan, testTime)
	if err != nil || last.Accepted || last.Assessment.StateDigest == first.Assessment.StateDigest {
		t.Fatal(last, err)
	}
	// The historical successful result stays inspectable without granting reuse.
	if !first.Accepted {
		t.Fatal("historical result mutated")
	}
}

func TestDigestBindsSourceAssessmentsWithoutGraphChanges(t *testing.T) {
	s := fixture()
	before := assess(t, s, "a")
	x := s.Sources["source:a"]
	x.AssessmentRevision++
	x.Checks[1].Result = CheckFail
	s.Sources["source:a"] = x
	after := assess(t, s, "a")
	if before.StateDigest == after.StateDigest || outcome(t, after, "a") != Excluded {
		t.Fatal(after)
	}
}

func TestUnknownContractAndMalformedManifestCannotClearPlan(t *testing.T) {
	s := fixture()
	for _, actions := range [][]ActionBasis{nil, {{ActionID: "act"}}, {{ActionID: "act", BasisIDs: []string{"a", "a"}}}, {{ActionID: "act", BasisIDs: []string{"a"}}, {ActionID: "act", BasisIDs: []string{"b"}}}} {
		if _, err := CheckPlan(s, GroundedActionV1, actions, testTime); !errors.Is(err, ErrInvalidProjection) {
			t.Fatalf("%+v: %v", actions, err)
		}
	}
	if _, err := Assess(s, "historical", []string{"a"}, testTime); !errors.Is(err, ErrInvalidProjection) {
		t.Fatal(err)
	}
	s.Version = "old-client/v0"
	if _, err := Assess(s, CurrentActionV1, []string{"a"}, testTime); !errors.Is(err, ErrInvalidProjection) {
		t.Fatal(err)
	}
}

func TestValidityAndCurrentnessBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Node)
		want Outcome
	}{
		{"at start", func(n *Node) { n.Temporal.ValidFrom = testTime.Format(time.RFC3339Nano) }, Eligible},
		{"before start", func(n *Node) { n.Temporal.ValidFrom = testTime.Add(time.Second).Format(time.RFC3339Nano) }, Excluded},
		{"invalid end", func(n *Node) { n.Temporal.ValidTo = "not-a-time" }, Blocked},
		{"reversed window", func(n *Node) {
			n.Temporal.ValidFrom = testTime.Format(time.RFC3339Nano)
			n.Temporal.ValidTo = testTime.Add(-time.Second).Format(time.RFC3339Nano)
		}, Blocked},
		{"no lineage is not current", func(n *Node) { n.Temporal.Status = evidencegraph.TemporalUnknown }, Blocked},
		{"lineage lacks witness", func(n *Node) {
			n.Currentness = Currentness{Complete: true, LineageKey: "l", Status: evidencesupersession.CurrentnessCurrent}
		}, Blocked},
		{"invalid restore", func(n *Node) { n.Withdrawal.Status = WithdrawalRestored }, Blocked},
		{"restore retains conflict", func(n *Node) {
			n.Withdrawal = Withdrawal{Status: WithdrawalRestored, Complete: true, Revision: 2, EventID: "restore:a"}
		}, Blocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			change(&s, "a", tc.edit)
			if tc.name == "restore retains conflict" {
				s.Contradictions = []Contradiction{{ID: "ab", Left: "a", Right: "b", ReviewRef: "review:ab"}}
			}
			if got := outcome(t, assess(t, s, "a"), "a"); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSnapshotSetOrderAndRepeatedReadsAreStable(t *testing.T) {
	s := fixture()
	derive(&s, "d", "b", "a")
	s.Contradictions = []Contradiction{{ID: "ab", Left: "b", Right: "a", ReviewRef: "review:ab"}, {ID: "bc", Left: "c", Right: "b", ReviewRef: "review:bc"}}
	before, _ := json.Marshal(s)
	want := assess(t, s, "d")
	for range 8 {
		t.Run("read", func(t *testing.T) {
			t.Parallel()
			if got := assess(t, s, "d"); !reflect.DeepEqual(got, want) {
				t.Fatal("unstable assessment", got)
			}
			if after, _ := json.Marshal(s); string(before) != string(after) {
				t.Fatal("input was mutated")
			}
		})
	}
	reordered := fixture()
	derive(&reordered, "d", "a", "b")
	reordered.Contradictions = []Contradiction{{ID: "bc", Left: "b", Right: "c", ReviewRef: "review:bc"}, {ID: "ab", Left: "a", Right: "b", ReviewRef: "review:ab"}}
	x := reordered.Sources["source:a"]
	slices.Reverse(x.Checks)
	reordered.Sources["source:a"] = x
	if got := assess(t, reordered, "d"); !reflect.DeepEqual(got, want) {
		t.Fatal("set ordering changed assessment", got, want)
	}
}
