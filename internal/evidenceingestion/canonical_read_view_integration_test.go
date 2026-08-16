//go:build integration

package evidenceingestion

import (
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestIntegrationReadCanonicalGraphViewReturnsBoundedArtifact(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "canonical-read-view")
	ingested, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("IngestManualText() error = %v", err)
	}
	admission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "exercise PostgreSQL canonical read view",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal() error = %v", err)
	}

	view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{
		RootNodeIDs: []string{admission.CanonicalRef},
		MaxDepth:    1,
		MaxNodes:    8,
		MaxEdges:    8,
	})
	if err != nil {
		t.Fatalf("ReadCanonicalGraphView() error = %v", err)
	}
	if view.Truncated || len(view.Artifact.Nodes) != 2 || len(view.Artifact.Edges) != 1 {
		t.Fatalf("canonical read view = truncated %t, %d nodes/%d edges, want false, 2/1", view.Truncated, len(view.Artifact.Nodes), len(view.Artifact.Edges))
	}
}

func TestIntegrationDerivedAdmissionRoundTripsThroughPostgres(t *testing.T) {
	ctx, pool := integrationPool(t)
	parentInputA, parentFixtureA := integrationInputFixture(t, "derived-parent-a")
	parentInputA.SourceID = "fixture-refund-policy-a"
	parentIngestA, err := IngestManualText(ctx, pool, parentInputA, parentFixtureA)
	if err != nil {
		t.Fatalf("IngestManualText(parent A) error = %v", err)
	}
	parentA, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: parentIngestA.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(parent A) error = %v", err)
	}

	parentInputB, parentFixtureB := integrationInputFixture(t, "derived-parent-b")
	parentInputB.SourceID = "fixture-refund-policy-b"
	parentIngestB, err := IngestManualText(ctx, pool, parentInputB, parentFixtureB)
	if err != nil {
		t.Fatalf("IngestManualText(parent B) error = %v", err)
	}
	parentB, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: parentIngestB.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(parent B) error = %v", err)
	}

	derivedInput, derivedFixture := integrationInputFixture(t, "derived-target")
	derivedInput.SourceID = "fixture-refund-policy-derived-proposal"
	derivedIngest, err := IngestManualText(ctx, pool, derivedInput, derivedFixture)
	if err != nil {
		t.Fatalf("IngestManualText(derived) error = %v", err)
	}
	derived, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: derivedIngest.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "persist exact AND derivation parents",
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parentB.CanonicalRef, parentA.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "integration-test",
			TraceRef:      "trace:postgres-derived-admission",
		},
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(derived) error = %v", err)
	}
	wantParents := []string{parentA.CanonicalRef, parentB.CanonicalRef}
	slices.Sort(wantParents)
	if derived.DerivationID == "" || !slices.Equal(derived.ParentNodeIDs, wantParents) || len(derived.CanonicalEdgeIDs) != 2 || len(derived.RawEvidenceNodeIDs) != 0 {
		t.Fatalf("derived admission = %+v, want two normalized parents and no raw evidence nodes", derived)
	}

	view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{
		RootNodeIDs: []string{derived.CanonicalRef},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalDerivedFrom},
		MaxDepth:    1,
		MaxNodes:    3,
		MaxEdges:    2,
	})
	if err != nil {
		t.Fatalf("ReadCanonicalGraphView(derived) error = %v", err)
	}
	if view.Truncated || len(view.Artifact.Nodes) != 3 || len(view.Artifact.Edges) != 2 || len(view.Artifact.Derivations) != 1 {
		t.Fatalf("derived read view = truncated %t, %d nodes/%d edges/%d derivations", view.Truncated, len(view.Artifact.Nodes), len(view.Artifact.Edges), len(view.Artifact.Derivations))
	}
	if !slices.Equal(view.Artifact.Derivations[0].Parents, wantParents) {
		t.Fatalf("persisted derivation parents = %v, want %v", view.Artifact.Derivations[0].Parents, wantParents)
	}

	replay, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: derivedIngest.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("replay AdmitPendingProposal(derived) error = %v", err)
	}
	if !replay.Replayed || replay.DerivationID != derived.DerivationID || !slices.Equal(replay.ParentNodeIDs, wantParents) {
		t.Fatalf("derived replay = %+v, want persisted derivation %+v", replay, derived)
	}
	assertTableCount(t, ctx, pool, "canonical_derivations", 1)
	assertTableCount(t, ctx, pool, "canonical_derivation_parents", 2)
}

func TestIntegrationDerivedAdmissionRejectsMissingParentAtomically(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "derived-missing-parent")
	input.SourceID = "fixture-derived-missing-parent"
	ingested, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("IngestManualText() error = %v", err)
	}

	_, err = AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{"canon-node:missing"},
			Method:        "policy_summary",
			Producer:      "integration-test",
			TraceRef:      "trace:missing-parent",
		},
	})
	assertKind(t, err, ErrorDerivationInvariant)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
	assertTableCount(t, ctx, pool, "canonical_derivations", 0)
	assertTableCount(t, ctx, pool, "canonical_derivation_parents", 0)
	assertTableCount(t, ctx, pool, "admission_decisions", 0)
	proposal, err := TraceProposalProvenance(ctx, pool, ingested.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("failed derived proposal state = %s/%s, want pending/empty", proposal.AdmissionOutcome, proposal.CanonicalRef)
	}
}
