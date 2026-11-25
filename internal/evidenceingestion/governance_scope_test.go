package evidenceingestion

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

var scopeTestTime = time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)

func scopeNodeID(i int) string { return fmt.Sprintf("canon-node:%05d", i) }

// scopeTestStore builds a synthetic store with raw evidence, withdrawn and
// lineage-bound source claims, failed source checks, multi-parent derived
// claims and contradictions, some of which point at absent nodes.
func scopeTestStore(n int) evidenceuse.Snapshot {
	s := evidenceuse.Snapshot{Version: evidenceuse.ProjectionV2, StoreID: "synthetic-store", CutID: "governance:1", Nodes: map[string]evidenceuse.Node{}, Sources: map[string]evidenceuse.Source{}, Contradictions: []evidenceuse.Contradiction{}}
	for i := range n {
		id := scopeNodeID(i)
		node := evidenceuse.Node{
			Kind: evidencegraph.CanonicalSourceClaim, Admission: "admitted",
			Withdrawal:      evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalNone, Complete: true},
			Currentness:     evidenceuse.Currentness{Complete: true},
			Temporal:        evidencegraph.TemporalRecord{Status: evidencegraph.TemporalCurrent},
			Parents:         []string{},
			ParentsComplete: true, SourcesComplete: true, ContradictionsComplete: true,
		}
		switch {
		case i%10 == 0:
			node.Kind = evidencegraph.CanonicalRawEvidence
		case i >= 40 && i%3 == 0:
			node.Kind, node.UseClass = evidencegraph.CanonicalDerivedClaim, evidencegraph.DerivationOrdinary
			node.Temporal.Status = evidencegraph.TemporalUnknown
			node.Parents = []string{scopeNodeID(i - 7), scopeNodeID(i - 13), scopeNodeID(i / 2)}
		}
		if node.Kind != evidencegraph.CanonicalDerivedClaim {
			source := fmt.Sprintf("source:%05d", i)
			node.SourceIDs = []string{source}
			check := evidenceuse.SourceCheck{AssessmentID: "check:" + source, SourceSnapshotID: source, ContentHash: "sha256:" + source, Scope: evidenceuse.RetainedSnapshotScope, Criterion: evidenceuse.RetentionV1, Result: evidenceuse.CheckPass, MaterialRef: "material:retained"}
			if i%13 == 0 {
				check.Result = evidenceuse.CheckFail
			}
			s.Sources[source] = evidenceuse.Source{SnapshotID: source, ContentHash: "sha256:" + source, AssessmentRevision: 1, AssessmentsComplete: true, Checks: []evidenceuse.SourceCheck{check}}
		}
		if i%17 == 0 {
			node.Withdrawal = evidenceuse.Withdrawal{Status: evidenceuse.WithdrawalWithdrawn, Complete: true, Revision: 1, EventID: "withdraw:" + id, Reason: "synthetic stop-use"}
		}
		if i%11 == 0 {
			status := evidencesupersession.CurrentnessCurrent
			if i%22 == 0 {
				status = evidencesupersession.CurrentnessSuperseded
			}
			node.Currentness = evidenceuse.Currentness{LineageKey: fmt.Sprintf("lineage:%d", i%5), Status: status, Complete: true, WitnessID: "witness:1"}
		}
		s.Nodes[id] = node
	}
	for i := 0; i < n; i += 19 {
		if peer := (i * 7) % n; peer != i {
			s.Contradictions = append(s.Contradictions, evidenceuse.Contradiction{ID: fmt.Sprintf("canon-edge:contradicts:%05d", i), Left: scopeNodeID(i), Right: scopeNodeID(peer), ReviewRef: "review:1", Scope: "recorded_explicit_pair"})
		}
	}
	s.Contradictions = append(s.Contradictions, evidenceuse.Contradiction{ID: "canon-edge:contradicts:absent", Left: scopeNodeID(47), Right: "canon-node:absent", ReviewRef: "review:1", Scope: "recorded_explicit_pair"})
	return s
}

// scopedFromStore runs the production scope collection against fetchers backed
// by store and returns the scoped snapshot and the largest single fetch.
func scopedFromStore(t *testing.T, store evidenceuse.Snapshot, roots []string) (evidenceuse.Snapshot, int, error) {
	t.Helper()
	largest := 0
	fetchNodes := func(_ context.Context, ids []string) (map[string]evidenceuse.Node, error) {
		largest = max(largest, len(ids))
		out := map[string]evidenceuse.Node{}
		for _, id := range ids {
			if n, ok := store.Nodes[id]; ok {
				out[id] = n
			}
		}
		return out, nil
	}
	fetchContradictions := func(_ context.Context, ids []string) ([]evidenceuse.Contradiction, error) {
		in := map[string]bool{}
		for _, id := range ids {
			in[id] = true
		}
		out := []evidenceuse.Contradiction{}
		for _, e := range store.Contradictions {
			if in[e.Left] || in[e.Right] {
				out = append(out, e)
			}
		}
		return out, nil
	}
	nodes, contradictions, err := collectEvidenceUseScope(context.Background(), roots, fetchNodes, fetchContradictions)
	if err != nil {
		return evidenceuse.Snapshot{}, largest, err
	}
	s := evidenceuse.Snapshot{Version: store.Version, StoreID: store.StoreID, CutID: store.CutID, Nodes: nodes, Sources: map[string]evidenceuse.Source{}, Contradictions: contradictions}
	for _, n := range nodes {
		for _, source := range n.SourceIDs {
			s.Sources[source] = store.Sources[source]
		}
	}
	return s, largest, nil
}

func TestScopedGovernanceAssessmentMatchesWholeStore(t *testing.T) {
	store := scopeTestStore(400)
	var rootSets [][]string
	for i := range 400 {
		rootSets = append(rootSets, []string{scopeNodeID(i)}, []string{scopeNodeID(i), scopeNodeID((i*31 + 5) % 400)})
	}
	rootSets = append(rootSets, []string{"canon-node:absent"}, []string{scopeNodeID(399), "canon-node:absent", scopeNodeID(3)})
	for _, policy := range []evidenceuse.Policy{evidenceuse.CurrentActionV1, evidenceuse.GroundedActionV1} {
		for _, roots := range rootSets {
			want, err := evidenceuse.Assess(store, policy, roots, scopeTestTime)
			if err != nil {
				t.Fatalf("whole-store Assess(%v): %v", roots, err)
			}
			scoped, _, err := scopedFromStore(t, store, roots)
			if err != nil {
				t.Fatalf("scope(%v): %v", roots, err)
			}
			got, err := evidenceuse.Assess(scoped, policy, roots, scopeTestTime)
			if err != nil {
				t.Fatalf("scoped Assess(%v): %v", roots, err)
			}
			if !reflect.DeepEqual(got.Nodes, want.Nodes) {
				t.Fatalf("%s roots %v: scoped assessment differs\n got %+v\nwant %+v", policy, roots, got.Nodes, want.Nodes)
			}
			plan := []evidenceuse.ActionBasis{{ActionID: "act", BasisIDs: roots}}
			wantPlan, err := evidenceuse.CheckPlan(store, policy, plan, scopeTestTime)
			if err != nil {
				t.Fatal(err)
			}
			gotPlan, err := evidenceuse.CheckPlan(scoped, policy, plan, scopeTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if gotPlan.Accepted != wantPlan.Accepted || !reflect.DeepEqual(gotPlan.Assessment.Nodes, wantPlan.Assessment.Nodes) {
				t.Fatalf("%s roots %v: scoped plan check differs", policy, roots)
			}
		}
	}
}

func TestScopedGovernanceLoadsOnlyTheEvaluatedScope(t *testing.T) {
	store := scopeTestStore(400)
	// 1 is a source claim with no parents and no contradictions.
	scoped, _, err := scopedFromStore(t, store, []string{scopeNodeID(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Nodes) != 1 || len(scoped.Contradictions) != 0 || len(scoped.Sources) != 1 {
		t.Fatalf("one-root scope = %d nodes, %d contradictions, %d sources; want 1, 0, 1", len(scoped.Nodes), len(scoped.Contradictions), len(scoped.Sources))
	}
	// 57 contradicts 399; the peer's own parents are not part of the scope.
	scoped, _, err = scopedFromStore(t, store, []string{scopeNodeID(57)})
	if err != nil {
		t.Fatal(err)
	}
	peer := scopeNodeID(399)
	if _, ok := scoped.Nodes[peer]; !ok || len(store.Nodes[peer].Parents) == 0 {
		t.Fatalf("test setup: expected derived conflict peer %s in scope", peer)
	}
	for _, parent := range store.Nodes[peer].Parents {
		if _, ok := scoped.Nodes[parent]; ok {
			t.Fatalf("peer parent %s was loaded", parent)
		}
	}
}

func TestScopedGovernanceServesOneRootAboveTheOldStoreLimit(t *testing.T) {
	store := scopeTestStore(12000)
	root := []string{scopeNodeID(11999)}
	if _, err := evidenceuse.Assess(store, evidenceuse.CurrentActionV1, root, scopeTestTime); err == nil {
		t.Fatal("test setup: whole-store assessment should exceed its node bound")
	}
	scoped, largest, err := scopedFromStore(t, store, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Nodes) >= 1000 || largest >= 1000 {
		t.Fatalf("scope loaded %d nodes, largest fetch %d; want a small closure", len(scoped.Nodes), largest)
	}
	if _, err := evidenceuse.Assess(scoped, evidenceuse.CurrentActionV1, root, scopeTestTime); err != nil {
		t.Fatalf("scoped assessment: %v", err)
	}
}

func TestScopedGovernanceFailsClosedAboveTheScopeLimit(t *testing.T) {
	store := evidenceuse.Snapshot{Nodes: map[string]evidenceuse.Node{}}
	const depth = governanceScopeMaxNodes + 1
	for i := range depth {
		n := evidenceuse.Node{Kind: evidencegraph.CanonicalDerivedClaim, Parents: []string{scopeNodeID(i + 1)}}
		if i == depth-1 {
			n.Kind, n.Parents = evidencegraph.CanonicalSourceClaim, nil
		}
		store.Nodes[scopeNodeID(i)] = n
	}
	_, _, err := scopedFromStore(t, store, []string{scopeNodeID(0)})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("more than %d nodes", governanceScopeMaxNodes)) {
		t.Fatalf("deep ancestry error = %v, want scope node limit", err)
	}
}

func TestEndpointUseCutRejectsADifferentScope(t *testing.T) {
	state := scopeTestStore(3)
	cut := &endpointUseCut{roots: []string{scopeNodeID(1), scopeNodeID(2)}, state: &state}
	if _, err := cut.snapshot(context.Background(), []string{scopeNodeID(2), scopeNodeID(1), scopeNodeID(1)}); err != nil {
		t.Fatalf("same parent set in another order: %v", err)
	}
	if _, err := cut.snapshot(context.Background(), []string{scopeNodeID(1)}); err == nil {
		t.Fatal("a different parent set reused the reviewed scope")
	}
}
