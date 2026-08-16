package evidenceingestion

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestReadCanonicalGraphViewLoadsAdmittedNeighborhood(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	admission := admitCanonicalReadFixture(t, ctx, db, "canonical-read-view")
	input := CanonicalReadInput{
		RootNodeIDs: []string{" " + admission.CanonicalRef + " ", admission.CanonicalRef},
		MaxDepth:    1,
		MaxNodes:    8,
		MaxEdges:    1,
	}

	first, err := readCanonicalGraphView(ctx, db, input)
	if err != nil {
		t.Fatalf("readCanonicalGraphView() error = %v", err)
	}
	second, err := readCanonicalGraphView(ctx, db, input)
	if err != nil {
		t.Fatalf("second readCanonicalGraphView() error = %v", err)
	}

	if first.Truncated {
		t.Fatalf("Truncated = true, want false")
	}
	if len(first.RootNodeIDs) != 1 || first.RootNodeIDs[0] != admission.CanonicalRef {
		t.Fatalf("root IDs = %v, want %s", first.RootNodeIDs, admission.CanonicalRef)
	}
	if first.MaxDepth != 1 || first.MaxNodes != 8 || first.MaxEdges != 1 {
		t.Fatalf("read scope = depth %d nodes %d edges %d, want 1/8/1", first.MaxDepth, first.MaxNodes, first.MaxEdges)
	}
	if len(first.Artifact.Nodes) != 2 || len(first.Artifact.Edges) != 1 {
		t.Fatalf("artifact topology = %d nodes/%d edges, want 2/1", len(first.Artifact.Nodes), len(first.Artifact.Edges))
	}
	if !strings.HasPrefix(first.Artifact.SnapshotID, "postgres-read-view:") {
		t.Fatalf("snapshot ID = %q, want postgres-read-view prefix", first.Artifact.SnapshotID)
	}
	if err := first.Artifact.Validate(); err != nil {
		t.Fatalf("artifact.Validate() error = %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("repeatable read differs:\nfirst  %+v\nsecond %+v", first, second)
	}
}

func TestReadCanonicalGraphViewReportsNodeBudgetTruncation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	admission := admitCanonicalReadFixture(t, ctx, db, "canonical-read-truncated")

	view, err := readCanonicalGraphView(ctx, db, CanonicalReadInput{
		RootNodeIDs: []string{admission.CanonicalRef},
		MaxDepth:    1,
		MaxNodes:    1,
		MaxEdges:    8,
	})
	if err != nil {
		t.Fatalf("readCanonicalGraphView() error = %v", err)
	}

	if !view.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if len(view.Artifact.Nodes) != 1 || view.Artifact.Nodes[0].ID != admission.CanonicalRef {
		t.Fatalf("nodes = %+v, want only root %s", view.Artifact.Nodes, admission.CanonicalRef)
	}
	if len(view.Artifact.Edges) != 0 {
		t.Fatalf("edges = %+v, want none without both bounded endpoints", view.Artifact.Edges)
	}
}

func TestReadCanonicalGraphViewAppliesRelationFilter(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	admission := admitCanonicalReadFixture(t, ctx, db, "canonical-read-filter")

	view, err := readCanonicalGraphView(ctx, db, CanonicalReadInput{
		RootNodeIDs: []string{admission.CanonicalRef},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalContradicts},
		MaxDepth:    1,
		MaxNodes:    8,
		MaxEdges:    8,
	})
	if err != nil {
		t.Fatalf("readCanonicalGraphView() error = %v", err)
	}

	if view.Truncated || len(view.Artifact.Nodes) != 1 || len(view.Artifact.Edges) != 0 {
		t.Fatalf("filtered view = truncated %t, %d nodes/%d edges, want false, 1/0", view.Truncated, len(view.Artifact.Nodes), len(view.Artifact.Edges))
	}
}

func TestReadCanonicalGraphViewRejectsInvalidScope(t *testing.T) {
	valid := CanonicalReadInput{
		RootNodeIDs: []string{"canon-node:root"},
		MaxDepth:    1,
		MaxNodes:    8,
		MaxEdges:    8,
	}
	for _, tc := range []struct {
		name  string
		input CanonicalReadInput
		kind  ErrorKind
	}{
		{name: "missing roots", input: withCanonicalReadRoots(valid, nil), kind: ErrorInvalidInput},
		{name: "bad root ID", input: withCanonicalReadRoots(valid, []string{"node:root"}), kind: ErrorInvalidRecordID},
		{name: "negative depth", input: withCanonicalReadDepth(valid, -1), kind: ErrorInvalidInput},
		{name: "depth too large", input: withCanonicalReadDepth(valid, canonicalReadMaxDepth+1), kind: ErrorInvalidInput},
		{name: "zero nodes", input: withCanonicalReadNodes(valid, 0), kind: ErrorInvalidInput},
		{name: "zero edges with traversal", input: withCanonicalReadEdges(valid, 0), kind: ErrorInvalidInput},
		{name: "unknown relation", input: withCanonicalReadRelations(valid, []evidencegraph.CanonicalEdgeRelation{"unknown"}), kind: ErrorInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readCanonicalGraphView(context.Background(), newMockSQLDB(), tc.input)
			assertKind(t, err, tc.kind)
		})
	}
}

func TestReadCanonicalGraphViewFailsClosedWithoutDerivationRows(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	admission := admitCanonicalReadFixture(t, ctx, db, "canonical-read-derived")
	claim := db.canonicalGraphNodes[admission.CanonicalRef]
	claim.Kind = evidencegraph.CanonicalDerivedClaim
	db.canonicalGraphNodes[admission.CanonicalRef] = claim

	_, err := readCanonicalGraphView(ctx, db, CanonicalReadInput{
		RootNodeIDs: []string{admission.CanonicalRef},
		MaxDepth:    0,
		MaxNodes:    1,
		MaxEdges:    0,
	})
	if err == nil || !strings.Contains(err.Error(), "requires derivation data") {
		t.Fatalf("error = %v, want missing derivation failure", err)
	}
}

func TestReadCanonicalGraphViewRestoresPersistedDerivation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	parent := admitCanonicalReadFixture(t, ctx, db, "canonical-read-derived-parent")
	proposal, err := ingestManualText(ctx, db, testManualInput("canonical-read-derived-proposal"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	derived, err := admitPendingProposal(ctx, db, AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parent.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:canonical-read-derived",
		},
	})
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}

	view, err := readCanonicalGraphView(ctx, db, CanonicalReadInput{
		RootNodeIDs: []string{derived.CanonicalRef},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom},
		MaxDepth:    1,
		MaxNodes:    2,
		MaxEdges:    1,
	})
	if err != nil {
		t.Fatalf("readCanonicalGraphView() error = %v", err)
	}
	if view.Truncated || len(view.Artifact.Nodes) != 2 || len(view.Artifact.Edges) != 1 || len(view.Artifact.Derivations) != 1 {
		t.Fatalf("derived view = truncated %t, %d nodes/%d edges/%d derivations", view.Truncated, len(view.Artifact.Nodes), len(view.Artifact.Edges), len(view.Artifact.Derivations))
	}
	derivation := view.Artifact.Derivations[0]
	if derivation.ID != derived.DerivationID || derivation.NodeID != derived.CanonicalRef || !reflect.DeepEqual(derivation.Parents, []string{parent.CanonicalRef}) {
		t.Fatalf("restored derivation = %+v", derivation)
	}
	if err := view.Artifact.Validate(); err != nil {
		t.Fatalf("artifact.Validate() error = %v", err)
	}
}

func TestReadCanonicalGraphViewFailsClosedOnIncompleteDerivationScope(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	parent := admitCanonicalReadFixture(t, ctx, db, "canonical-read-incomplete-parent")
	proposal, err := ingestManualText(ctx, db, testManualInput("canonical-read-incomplete-proposal"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	derived, err := admitPendingProposal(ctx, db, AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parent.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:incomplete-derived",
		},
	})
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}

	_, err = readCanonicalGraphView(ctx, db, CanonicalReadInput{
		RootNodeIDs: []string{derived.CanonicalRef},
		MaxDepth:    0,
		MaxNodes:    1,
		MaxEdges:    0,
	})
	if err == nil || !strings.Contains(err.Error(), "outside the bounded PostgreSQL view") {
		t.Fatalf("error = %v, want incomplete derivation scope failure", err)
	}
}

func admitCanonicalReadFixture(t *testing.T, ctx context.Context, db *mockSQLDB, sourceID string) AdmissionResult {
	t.Helper()
	ingested, err := ingestManualText(ctx, db, testManualInput(sourceID), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	admission, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}
	return admission
}

func withCanonicalReadRoots(input CanonicalReadInput, roots []string) CanonicalReadInput {
	input.RootNodeIDs = roots
	return input
}

func withCanonicalReadRelations(input CanonicalReadInput, relations []evidencegraph.CanonicalEdgeRelation) CanonicalReadInput {
	input.Relations = relations
	return input
}

func withCanonicalReadDepth(input CanonicalReadInput, depth int) CanonicalReadInput {
	input.MaxDepth = depth
	return input
}

func withCanonicalReadNodes(input CanonicalReadInput, nodes int) CanonicalReadInput {
	input.MaxNodes = nodes
	return input
}

func withCanonicalReadEdges(input CanonicalReadInput, edges int) CanonicalReadInput {
	input.MaxEdges = edges
	return input
}
