//go:build integration

package evidenceingestion

import (
	"context"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCanonicalSupersessionAdmissionRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	currentNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-current", "supersession-current-source")
	replacedNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-replaced", "supersession-replaced-source")

	input := canonicalSupersessionIntegrationInput("supersession-request-1", currentNodeID, replacedNodeID)
	proposal, err := SubmitCanonicalSupersessionProposal(ctx, pool, input)
	if err != nil {
		t.Fatalf("SubmitCanonicalSupersessionProposal() error = %v", err)
	}
	if proposal.Proposal.FromNodeID != currentNodeID || proposal.Proposal.ToNodeID != replacedNodeID || proposal.Proposal.AdmissionOutcome != admissionOutcomePending || proposal.Replayed {
		t.Fatalf("proposal = %+v", proposal)
	}
	replay, err := SubmitCanonicalSupersessionProposal(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay proposal: %v", err)
	}
	if !replay.Replayed || replay.Proposal.ID != proposal.Proposal.ID {
		t.Fatalf("proposal replay = %+v", replay)
	}
	conflictInput := input
	conflictInput.Rationale = "changed rationale must not overwrite the first proposal"
	_, err = SubmitCanonicalSupersessionProposal(ctx, pool, conflictInput)
	assertKind(t, err, ErrorOccurrenceConflict)

	reverseInput := canonicalSupersessionIntegrationInput("supersession-request-reverse", replacedNodeID, currentNodeID)
	reverse, err := SubmitCanonicalSupersessionProposal(ctx, pool, reverseInput)
	if err != nil {
		t.Fatalf("submit reverse proposal: %v", err)
	}
	if reverse.Replayed || reverse.Proposal.ID == proposal.Proposal.ID || reverse.Proposal.FromNodeID != replacedNodeID || reverse.Proposal.ToNodeID != currentNodeID {
		t.Fatalf("directed reverse proposal = %+v", reverse)
	}

	pending, err := GetCanonicalSupersessionProposal(ctx, pool, proposal.Proposal.ID)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionProposal(pending): %v", err)
	}
	if pending.Decision != nil || pending.From.CanonicalID != currentNodeID || pending.To.CanonicalID != replacedNodeID {
		t.Fatalf("pending review card = %+v", pending)
	}
	if len(pending.From.OriginProposal.SourceRefs) == 0 || len(pending.To.OriginProposal.SourceRefs) == 0 {
		t.Fatalf("review card lacks source excerpts: from=%+v to=%+v", pending.From.OriginProposal.SourceRefs, pending.To.OriginProposal.SourceRefs)
	}
	if len(pending.Proposal.Limitations) != 1 || pending.Proposal.ProposalSentence == "" || pending.Proposal.VersionDifference == "" {
		t.Fatalf("review metadata = %+v", pending.Proposal)
	}

	decisionInput := CanonicalSupersessionAdmissionInput{
		ProposalID:     proposal.Proposal.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "reviewed the current and replaced source-backed claims",
	}
	decision, err := AdmitPendingCanonicalSupersession(ctx, pool, decisionInput)
	if err != nil {
		t.Fatalf("AdmitPendingCanonicalSupersession() error = %v", err)
	}
	if decision.Decision.Outcome != admissionOutcomeAdmitted || decision.Decision.CanonicalEdgeID == "" || decision.Replayed {
		t.Fatalf("decision = %+v", decision)
	}
	decisionReplay, err := AdmitPendingCanonicalSupersession(ctx, pool, decisionInput)
	if err != nil {
		t.Fatalf("replay admission: %v", err)
	}
	if !decisionReplay.Replayed || decisionReplay.Decision.ID != decision.Decision.ID {
		t.Fatalf("decision replay = %+v", decisionReplay)
	}
	changedDecision := decisionInput
	changedDecision.DecisionReason = "must not replace the first admission audit"
	_, err = AdmitPendingCanonicalSupersession(ctx, pool, changedDecision)
	assertKind(t, err, ErrorAdmissionStateConflict)

	admitted, err := GetCanonicalSupersessionProposal(ctx, pool, proposal.Proposal.ID)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionProposal(admitted): %v", err)
	}
	if admitted.Decision == nil || admitted.Decision.CanonicalEdgeID != decision.Decision.CanonicalEdgeID || admitted.Proposal.AdmissionOutcome != admissionOutcomeAdmitted {
		t.Fatalf("admitted review card = %+v", admitted)
	}
	relation, err := GetCanonicalRelationByID(ctx, pool, decision.Decision.CanonicalEdgeID)
	if err != nil {
		t.Fatalf("GetCanonicalRelationByID() error = %v", err)
	}
	if relation.Edge.From != currentNodeID || relation.Edge.To != replacedNodeID || relation.Edge.Relation != evidencegraph.CanonicalSupersedes {
		t.Fatalf("directed relation = %+v", relation.Edge)
	}
	if relation.OriginProposal != nil || relation.OriginContradictionProposal != nil || relation.OriginSupersessionProposal == nil {
		t.Fatalf("relation provenance = %+v", relation)
	}
	if relation.OriginSupersessionProposal.Proposal.ID != proposal.Proposal.ID {
		t.Fatalf("relation proposal origin = %+v", relation.OriginSupersessionProposal.Proposal)
	}

	view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{
		RootNodeIDs: []string{currentNodeID},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupersedes},
		MaxDepth:    1,
		MaxNodes:    4,
		MaxEdges:    4,
	})
	if err != nil {
		t.Fatalf("ReadCanonicalGraphView() error = %v", err)
	}
	if len(view.Artifact.Edges) != 1 || view.Artifact.Edges[0].ID != decision.Decision.CanonicalEdgeID {
		t.Fatalf("supersession read view = %+v", view.Artifact.Edges)
	}
}

func TestIntegrationCanonicalSupersessionDispositionDoesNotWriteEdge(t *testing.T) {
	for _, outcome := range []string{ProposalDispositionRejected, ProposalDispositionAuditOnly} {
		t.Run(outcome, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			currentNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-"+outcome+"-current", "supersession-"+outcome+"-current-source")
			replacedNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-"+outcome+"-old", "supersession-"+outcome+"-old-source")
			proposal, err := SubmitCanonicalSupersessionProposal(
				ctx,
				pool,
				canonicalSupersessionIntegrationInput("supersession-"+outcome+"-request", currentNodeID, replacedNodeID),
			)
			if err != nil {
				t.Fatalf("SubmitCanonicalSupersessionProposal() error = %v", err)
			}
			input := CanonicalSupersessionDispositionInput{
				ProposalID:     proposal.Proposal.ID,
				Outcome:        outcome,
				DecisionBy:     "integration-reviewer",
				DecisionReason: "the supplied records did not prove an admitted replacement",
			}
			decision, err := RecordPendingCanonicalSupersessionDisposition(ctx, pool, input)
			if err != nil {
				t.Fatalf("RecordPendingCanonicalSupersessionDisposition() error = %v", err)
			}
			if decision.Decision.CanonicalEdgeID != "" || decision.Decision.Outcome != outcome || decision.Replayed {
				t.Fatalf("disposition = %+v", decision)
			}
			replay, err := RecordPendingCanonicalSupersessionDisposition(ctx, pool, input)
			if err != nil {
				t.Fatalf("replay disposition: %v", err)
			}
			if !replay.Replayed || replay.Decision.ID != decision.Decision.ID {
				t.Fatalf("disposition replay = %+v", replay)
			}
			changedInput := input
			changedInput.DecisionBy = "different-reviewer"
			changedInput.DecisionReason = "must not replace the first disposition audit"
			_, err = RecordPendingCanonicalSupersessionDisposition(ctx, pool, changedInput)
			assertKind(t, err, ErrorAdmissionStateConflict)
			assertTableCount(t, ctx, pool, "canonical_supersession_proposals", 1)
			assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 1)
			assertTableCount(t, ctx, pool, "canonical_graph_edges", 2)
		})
	}
}

func TestIntegrationCanonicalSupersessionAdmissionRejectsCycle(t *testing.T) {
	ctx, pool := integrationPool(t)
	v1 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-cycle-v1", "supersession-cycle-source-v1")
	v2 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-cycle-v2", "supersession-cycle-source-v2")
	v3 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-cycle-v3", "supersession-cycle-source-v3")

	for _, relation := range []struct {
		requestID string
		from      string
		to        string
	}{
		{requestID: "supersession-cycle-v2-v1", from: v2, to: v1},
		{requestID: "supersession-cycle-v3-v2", from: v3, to: v2},
	} {
		proposal, err := SubmitCanonicalSupersessionProposal(
			ctx,
			pool,
			canonicalSupersessionIntegrationInput(relation.requestID, relation.from, relation.to),
		)
		if err != nil {
			t.Fatalf("submit %s: %v", relation.requestID, err)
		}
		if _, err := AdmitPendingCanonicalSupersession(ctx, pool, CanonicalSupersessionAdmissionInput{
			ProposalID:     proposal.Proposal.ID,
			DecisionBy:     "integration-reviewer",
			DecisionReason: "establish an acyclic supersession chain",
		}); err != nil {
			t.Fatalf("admit %s: %v", relation.requestID, err)
		}
	}

	cycleProposal, err := SubmitCanonicalSupersessionProposal(
		ctx,
		pool,
		canonicalSupersessionIntegrationInput("supersession-cycle-v1-v3", v1, v3),
	)
	if err != nil {
		t.Fatalf("submit cycle candidate: %v", err)
	}
	_, err = AdmitPendingCanonicalSupersession(ctx, pool, CanonicalSupersessionAdmissionInput{
		ProposalID:     cycleProposal.Proposal.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "this decision must be rejected by the cycle invariant",
	})
	assertKind(t, err, ErrorSupersessionInvariant)

	pending, err := GetCanonicalSupersessionProposal(ctx, pool, cycleProposal.Proposal.ID)
	if err != nil {
		t.Fatalf("get rejected cycle proposal: %v", err)
	}
	if pending.Proposal.AdmissionOutcome != admissionOutcomePending || pending.Decision != nil || pending.Proposal.CanonicalEdgeID != "" {
		t.Fatalf("cycle proposal was partially admitted: %+v", pending)
	}
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 2)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 5)
}

func TestIntegrationCanonicalSupersessionDatabaseRejectsUngovernedEdges(t *testing.T) {
	ctx, pool := integrationPool(t)
	currentNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-db-current", "supersession-db-current-source")
	replacedNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-db-old", "supersession-db-old-source")
	proposal, err := SubmitCanonicalSupersessionProposal(
		ctx,
		pool,
		canonicalSupersessionIntegrationInput("supersession-db-request", currentNodeID, replacedNodeID),
	)
	if err != nil {
		t.Fatalf("SubmitCanonicalSupersessionProposal() error = %v", err)
	}

	var statementProposalID string
	if err := pool.QueryRow(ctx, `
		SELECT origin_proposal_occurrence_id
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`, currentNodeID).Scan(&statementProposalID); err != nil {
		t.Fatalf("load canonical statement origin: %v", err)
	}
	assertDirectStatementOriginSupersessionEdgeRejected(t, ctx, pool, currentNodeID, replacedNodeID, statementProposalID)
	assertDirectPendingSupersessionEdgeRejected(t, ctx, pool, proposal.Proposal)
}

func assertDirectStatementOriginSupersessionEdgeRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fromNodeID, toNodeID, statementProposalID string,
) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO canonical_graph_edges (
			canonical_edge_id,
			from_node_id,
			to_node_id,
			relation,
			provenance,
			origin_proposal_occurrence_id,
			origin_canonical_contradiction_proposal_id,
			origin_canonical_supersession_proposal_id
		)
		VALUES ('canon-edge:direct-statement-supersession', $1, $2, 'supersedes', '{}'::jsonb, $3, NULL, NULL)
	`, fromNodeID, toNodeID, statementProposalID)
	if err == nil {
		t.Fatal("database accepted a statement-origin supersedes edge")
	}
}

func assertDirectPendingSupersessionEdgeRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proposal CanonicalSupersessionProposal,
) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO canonical_graph_edges (
			canonical_edge_id,
			from_node_id,
			to_node_id,
			relation,
			provenance,
			origin_proposal_occurrence_id,
			origin_canonical_contradiction_proposal_id,
			origin_canonical_supersession_proposal_id
		)
		VALUES ('canon-edge:direct-pending-supersession', $1, $2, 'supersedes', '{}'::jsonb, NULL, NULL, $3)
	`, proposal.FromNodeID, proposal.ToNodeID, proposal.ID)
	if err == nil {
		t.Fatal("database accepted a supersedes edge without its admission decision")
	}
}

func canonicalSupersessionIntegrationInput(requestID, fromNodeID, toNodeID string) CanonicalSupersessionProposalInput {
	return CanonicalSupersessionProposalInput{
		RequestID:          requestID,
		FromNodeID:         fromNodeID,
		ToNodeID:           toNodeID,
		ProposalSentence:   "The current timeout claim supersedes the previous timeout claim.",
		Rationale:          "The supplied source records explicitly establish replacement for the same scope.",
		VersionDifference:  "The current revision changes the timeout from 60 seconds to 30 seconds.",
		Limitations:        []string{"Only the supplied policy scope was compared."},
		ProducerName:       "integration-agent",
		ProducerVersion:    "workflow-v1",
		ProducerSessionRef: "session:integration-1",
	}
}

func ingestAndAdmitSupersessionFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	requestID, sourceID string,
) string {
	t.Helper()
	input, fixture := integrationInputFixture(t, requestID)
	input.SourceID = sourceID
	ingested, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("IngestManualText(%s) error = %v", requestID, err)
	}
	admission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		DecisionBy:           "integration-reviewer",
		DecisionReason:       "create canonical supersession endpoint",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(%s) error = %v", requestID, err)
	}
	return admission.CanonicalRef
}
