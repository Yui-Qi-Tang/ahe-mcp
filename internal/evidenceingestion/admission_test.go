package evidenceingestion

import (
	"context"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestMockSQLAdmitPendingProposalCreatesCanonicalGraph(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("mock-admission"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}

	result, err := admitPendingProposal(ctx, db, AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		DecisionBy:           "unit-test",
		DecisionReason:       "fixture statement accepted",
	})
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}
	if result.AdmissionOutcome != admissionOutcomeAdmitted {
		t.Fatalf("outcome = %q, want admitted", result.AdmissionOutcome)
	}
	if result.CanonicalRef == "" {
		t.Fatalf("canonical ref is empty")
	}
	if len(result.RawEvidenceNodeIDs) != 1 || len(result.CanonicalEdgeIDs) != 1 {
		t.Fatalf("graph IDs = raw %v edges %v, want one raw node and one edge", result.RawEvidenceNodeIDs, result.CanonicalEdgeIDs)
	}
	if len(db.canonicalGraphNodes) != 2 {
		t.Fatalf("canonical node count = %d, want 2", len(db.canonicalGraphNodes))
	}
	if len(db.canonicalGraphEdges) != 1 {
		t.Fatalf("canonical edge count = %d, want 1", len(db.canonicalGraphEdges))
	}
	claim := db.canonicalGraphNodes[result.CanonicalRef]
	if claim.Kind != evidencegraph.CanonicalSourceClaim {
		t.Fatalf("canonical ref kind = %q, want source_claim", claim.Kind)
	}
	if claim.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("claim payload = %q", claim.Payload.Claim)
	}
	raw := db.canonicalGraphNodes[result.RawEvidenceNodeIDs[0]]
	if raw.Kind != evidencegraph.CanonicalRawEvidence || raw.Payload.Span != "Refunds must be completed within 7 days." {
		t.Fatalf("raw evidence node = %+v", raw)
	}
	edge := db.canonicalGraphEdges[result.CanonicalEdgeIDs[0]]
	if edge.From != raw.ID || edge.To != claim.ID || edge.Relation != evidencegraph.CanonicalSupportsClaim {
		t.Fatalf("canonical edge = %+v, want raw supports_claim claim", edge)
	}

	got, err := traceProposalProvenance(ctx, db, ingested.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("traceProposalProvenance() error = %v", err)
	}
	if got.AdmissionOutcome != admissionOutcomeAdmitted || got.CanonicalRef != result.CanonicalRef {
		t.Fatalf("proposal state = %s/%s, want admitted/%s", got.AdmissionOutcome, got.CanonicalRef, result.CanonicalRef)
	}
}

func TestMockSQLGetCanonicalEvidenceByID(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("mock-canonical-query"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	admission, err := admitPendingProposal(ctx, db, AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
	})
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}

	got, err := getCanonicalEvidenceByID(ctx, db, admission.CanonicalRef)
	if err != nil {
		t.Fatalf("getCanonicalEvidenceByID() error = %v", err)
	}
	if got.CanonicalID != admission.CanonicalRef || got.NodeKind != evidencegraph.CanonicalSourceClaim {
		t.Fatalf("canonical record = %s/%s, want source claim %s", got.CanonicalID, got.NodeKind, admission.CanonicalRef)
	}
	if got.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("claim = %q", got.Payload.Claim)
	}
	if got.OriginProposalOccurrenceID != ingested.ProposalOccurrenceID {
		t.Fatalf("origin proposal = %q, want %q", got.OriginProposalOccurrenceID, ingested.ProposalOccurrenceID)
	}
	if got.OriginProposal.ProposalOccurrenceID != ingested.ProposalOccurrenceID || got.OriginProposal.AdmissionOutcome != admissionOutcomeAdmitted {
		t.Fatalf("origin proposal trace = %+v", got.OriginProposal)
	}
	if len(got.OriginProposal.SourceRefs) != 1 || got.OriginProposal.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("origin source refs = %+v, want span:S1", got.OriginProposal.SourceRefs)
	}
}

func TestMockSQLAdmitPendingProposalReplay(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("mock-admission-replay"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	first, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	second, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("second admitPendingProposal() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second Replayed = false, want true")
	}
	if second.AdmissionDecisionID != first.AdmissionDecisionID || second.CanonicalRef != first.CanonicalRef {
		t.Fatalf("replay = %+v, want decision/ref from %+v", second, first)
	}
	if len(db.admissionDecisions) != 1 || len(db.canonicalGraphNodes) != 2 || len(db.canonicalGraphEdges) != 1 {
		t.Fatalf("counts = decisions %d nodes %d edges %d, want 1/2/1", len(db.admissionDecisions), len(db.canonicalGraphNodes), len(db.canonicalGraphEdges))
	}
}

func TestMockSQLAdmitPendingProposalRejectsNonPendingState(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("mock-admission-state"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	occurrence := db.proposalOccurrences[ingested.ProposalOccurrenceID]
	occurrence.AdmissionOutcome = admissionOutcomeRejected
	db.proposalOccurrences[ingested.ProposalOccurrenceID] = occurrence

	_, err = admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	assertKind(t, err, ErrorAdmissionStateConflict)
	if len(db.canonicalGraphNodes) != 0 || len(db.canonicalGraphEdges) != 0 || len(db.admissionDecisions) != 0 {
		t.Fatalf("non-pending admission mutated graph: nodes %d edges %d decisions %d", len(db.canonicalGraphNodes), len(db.canonicalGraphEdges), len(db.admissionDecisions))
	}
}

func TestCanonicalAdmissionMutationReusesCanonicalIdentityForExactDuplicate(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-admission-duplicate")
	first, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("first ingestManualText() error = %v", err)
	}
	input.RequestID = "mock-admission-duplicate-second-run"
	second, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("second ingestManualText() error = %v", err)
	}
	if first.ProposalOccurrenceID == second.ProposalOccurrenceID || first.ProposalFingerprint != second.ProposalFingerprint {
		t.Fatalf("fixture did not create distinct occurrence with same fingerprint: first %+v second %+v", first, second)
	}

	firstAdmission, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("first admitPendingProposal() error = %v", err)
	}
	secondAdmission, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: second.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("second admitPendingProposal() error = %v", err)
	}
	if secondAdmission.CanonicalRef != firstAdmission.CanonicalRef {
		t.Fatalf("second canonical ref = %s, want %s", secondAdmission.CanonicalRef, firstAdmission.CanonicalRef)
	}
	if len(db.admissionDecisions) != 2 || len(db.canonicalGraphNodes) != 2 || len(db.canonicalGraphEdges) != 1 {
		t.Fatalf("counts = decisions %d nodes %d edges %d, want 2/2/1", len(db.admissionDecisions), len(db.canonicalGraphNodes), len(db.canonicalGraphEdges))
	}
}

func TestMockSQLAdmitPendingProposalPersistsDerivation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	parent := admitCanonicalReadFixture(t, ctx, db, "mock-derived-parent")
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-derived-proposal"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}

	input := AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{parent.CanonicalRef},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:derived-admission",
		},
	}
	result, err := admitPendingProposal(ctx, db, input)
	if err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}
	if result.DerivationID == "" || result.CanonicalRef == "" {
		t.Fatalf("derived admission IDs are empty: %+v", result)
	}
	if len(result.RawEvidenceNodeIDs) != 0 || len(result.ParentNodeIDs) != 1 || result.ParentNodeIDs[0] != parent.CanonicalRef {
		t.Fatalf("derived admission result = %+v", result)
	}
	derived := db.canonicalGraphNodes[result.CanonicalRef]
	if derived.Kind != evidencegraph.CanonicalDerivedClaim || derived.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("derived node = %+v", derived)
	}
	derivation := db.canonicalDerivations[result.DerivationID]
	if derivation.NodeID != result.CanonicalRef || len(derivation.Parents) != 1 || derivation.Parents[0] != parent.CanonicalRef {
		t.Fatalf("persisted derivation = %+v", derivation)
	}
	edge := db.canonicalGraphEdges[result.CanonicalEdgeIDs[0]]
	if edge.From != parent.CanonicalRef || edge.To != result.CanonicalRef || edge.Relation != evidencegraph.CanonicalDerivedFrom {
		t.Fatalf("derived edge = %+v", edge)
	}

	replay, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("replay admitPendingProposal() error = %v", err)
	}
	if !replay.Replayed || replay.DerivationID != result.DerivationID || len(replay.ParentNodeIDs) != 1 || replay.ParentNodeIDs[0] != parent.CanonicalRef {
		t.Fatalf("derived replay = %+v, want %+v", replay, result)
	}
}

func TestMockSQLDerivedAdmissionRejectsMissingParentWithoutMutation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	proposal, err := ingestManualText(ctx, db, testManualInput("mock-derived-missing-parent"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}

	_, err = admitPendingProposal(ctx, db, AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		Derivation: &DerivationAdmissionInput{
			ParentNodeIDs: []string{"canon-node:missing"},
			Method:        "policy_summary",
			Producer:      "unit-test",
			TraceRef:      "trace:missing-parent",
		},
	})
	assertKind(t, err, ErrorDerivationInvariant)
	if len(db.canonicalGraphNodes) != 0 || len(db.canonicalGraphEdges) != 0 || len(db.canonicalDerivations) != 0 || len(db.admissionDecisions) != 0 {
		t.Fatalf(
			"failed derived admission mutated authority: nodes %d edges %d derivations %d decisions %d",
			len(db.canonicalGraphNodes),
			len(db.canonicalGraphEdges),
			len(db.canonicalDerivations),
			len(db.admissionDecisions),
		)
	}
}
