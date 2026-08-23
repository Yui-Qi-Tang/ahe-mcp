//go:build integration

package evidenceingestion

import (
	"context"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceprojection"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCanonicalContradictionAdmissionRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	nodeA := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-a", "contradiction-source-a")
	nodeB := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-b", "contradiction-source-b")
	if nodeA == nodeB {
		t.Fatalf("fixture canonical nodes are equal: %s", nodeA)
	}

	input := CanonicalContradictionProposalInput{
		RequestID:          "contradiction-request-1",
		NodeAID:            nodeB,
		NodeBID:            nodeA,
		Rationale:          "The two admitted claims were reviewed as mutually incompatible.",
		ProducerName:       "integration-agent",
		ProducerVersion:    "workflow-v1",
		ProducerSessionRef: "session:integration-1",
	}
	proposal, err := SubmitCanonicalContradictionProposal(ctx, pool, input)
	if err != nil {
		t.Fatalf("SubmitCanonicalContradictionProposal() error = %v", err)
	}
	if proposal.Proposal.NodeAID >= proposal.Proposal.NodeBID || proposal.Proposal.AdmissionOutcome != admissionOutcomePending || proposal.Replayed {
		t.Fatalf("proposal = %+v", proposal)
	}
	reverseInput := input
	reverseInput.NodeAID, reverseInput.NodeBID = input.NodeBID, input.NodeAID
	replay, err := SubmitCanonicalContradictionProposal(ctx, pool, reverseInput)
	if err != nil {
		t.Fatalf("replay proposal: %v", err)
	}
	if !replay.Replayed || replay.Proposal.ID != proposal.Proposal.ID {
		t.Fatalf("proposal replay = %+v", replay)
	}
	conflictInput := input
	conflictInput.Rationale = "changed rationale must not overwrite the first proposal"
	_, err = SubmitCanonicalContradictionProposal(ctx, pool, conflictInput)
	assertKind(t, err, ErrorOccurrenceConflict)

	pending, err := GetCanonicalContradictionProposal(ctx, pool, proposal.Proposal.ID)
	if err != nil {
		t.Fatalf("GetCanonicalContradictionProposal(pending): %v", err)
	}
	if pending.Decision != nil || pending.NodeA.CanonicalID != proposal.Proposal.NodeAID || pending.NodeB.CanonicalID != proposal.Proposal.NodeBID {
		t.Fatalf("pending review card = %+v", pending)
	}
	if len(pending.NodeA.OriginProposal.SourceRefs) == 0 || len(pending.NodeB.OriginProposal.SourceRefs) == 0 {
		t.Fatalf("review card lacks source excerpts: A=%+v B=%+v", pending.NodeA.OriginProposal.SourceRefs, pending.NodeB.OriginProposal.SourceRefs)
	}

	decision, err := AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{
		ProposalID:     proposal.Proposal.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "reviewed both grounded claims and accepted the conflict",
	})
	if err != nil {
		t.Fatalf("AdmitPendingCanonicalContradiction() error = %v", err)
	}
	if decision.Decision.Outcome != admissionOutcomeAdmitted || decision.Decision.CanonicalEdgeID == "" || decision.Replayed {
		t.Fatalf("decision = %+v", decision)
	}
	decisionReplay, err := AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{
		ProposalID:     proposal.Proposal.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "reviewed both grounded claims and accepted the conflict",
	})
	if err != nil {
		t.Fatalf("replay admission: %v", err)
	}
	if !decisionReplay.Replayed || decisionReplay.Decision.ID != decision.Decision.ID {
		t.Fatalf("decision replay = %+v", decisionReplay)
	}
	_, err = AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{
		ProposalID:     proposal.Proposal.ID,
		DecisionBy:     "different-reviewer",
		DecisionReason: "must not replace the first admission audit",
	})
	assertKind(t, err, ErrorAdmissionStateConflict)

	admitted, err := GetCanonicalContradictionProposal(ctx, pool, proposal.Proposal.ID)
	if err != nil {
		t.Fatalf("GetCanonicalContradictionProposal(admitted): %v", err)
	}
	if admitted.Decision == nil || admitted.Decision.CanonicalEdgeID != decision.Decision.CanonicalEdgeID || admitted.Proposal.AdmissionOutcome != admissionOutcomeAdmitted {
		t.Fatalf("admitted review card = %+v", admitted)
	}
	relation, err := GetCanonicalRelationByID(ctx, pool, decision.Decision.CanonicalEdgeID)
	if err != nil {
		t.Fatalf("GetCanonicalRelationByID() error = %v", err)
	}
	if relation.Edge.Relation != evidencegraph.CanonicalContradicts || relation.OriginProposal != nil || relation.OriginContradictionProposal == nil {
		t.Fatalf("relation provenance = %+v", relation)
	}
	if relation.OriginContradictionProposal.Proposal.ID != proposal.Proposal.ID {
		t.Fatalf("relation proposal origin = %+v", relation.OriginContradictionProposal.Proposal)
	}

	view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{
		RootNodeIDs: []string{nodeA, nodeB},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalContradicts},
		MaxDepth:    1,
		MaxNodes:    4,
		MaxEdges:    4,
	})
	if err != nil {
		t.Fatalf("ReadCanonicalGraphView() error = %v", err)
	}
	if len(view.Artifact.Edges) != 1 || view.Artifact.Edges[0].ID != decision.Decision.CanonicalEdgeID {
		t.Fatalf("contradiction read view = %+v", view.Artifact.Edges)
	}
	topology, err := evidenceprojection.PrepareTopology(view.Artifact)
	if err != nil {
		t.Fatalf("PrepareTopology() error = %v", err)
	}
	clusters := topology.ConflictClusters()
	if len(clusters) != 1 || len(clusters[0].NodeIDs) != 2 {
		t.Fatalf("contradiction diagnostics = %+v", clusters)
	}
}

func TestIntegrationCanonicalContradictionDispositionDoesNotWriteEdge(t *testing.T) {
	ctx, pool := integrationPool(t)
	nodeA := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-reject-a", "contradiction-reject-source-a")
	nodeB := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-reject-b", "contradiction-reject-source-b")
	proposal, err := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{
		RequestID:       "contradiction-reject-request",
		NodeAID:         nodeA,
		NodeBID:         nodeB,
		Rationale:       "Candidate conflict retained for explicit rejection coverage.",
		ProducerName:    "integration-agent",
		ProducerVersion: "workflow-v1",
	})
	if err != nil {
		t.Fatalf("SubmitCanonicalContradictionProposal() error = %v", err)
	}
	decision, err := RecordPendingCanonicalContradictionDisposition(ctx, pool, CanonicalContradictionDispositionInput{
		ProposalID:     proposal.Proposal.ID,
		Outcome:        ProposalDispositionRejected,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "the claim scopes are compatible after review",
	})
	if err != nil {
		t.Fatalf("RecordPendingCanonicalContradictionDisposition() error = %v", err)
	}
	if decision.Decision.CanonicalEdgeID != "" || decision.Decision.Outcome != ProposalDispositionRejected {
		t.Fatalf("disposition = %+v", decision)
	}
	_, err = RecordPendingCanonicalContradictionDisposition(ctx, pool, CanonicalContradictionDispositionInput{
		ProposalID:     proposal.Proposal.ID,
		Outcome:        ProposalDispositionRejected,
		DecisionBy:     "different-reviewer",
		DecisionReason: "must not replace the first disposition audit",
	})
	assertKind(t, err, ErrorAdmissionStateConflict)
	assertTableCount(t, ctx, pool, "canonical_contradiction_proposals", 1)
	assertTableCount(t, ctx, pool, "canonical_contradiction_admission_decisions", 1)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 2)
}

func TestIntegrationCanonicalContradictionDatabaseRejectsInvalidOriginEdges(t *testing.T) {
	ctx, pool := integrationPool(t)
	nodeA := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-db-a", "contradiction-db-source-a")
	nodeB := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-db-b", "contradiction-db-source-b")
	proposal, err := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{
		RequestID:       "contradiction-db-request",
		NodeAID:         nodeA,
		NodeBID:         nodeB,
		Rationale:       "Exercise database-level contradiction edge invariants.",
		ProducerName:    "integration-agent",
		ProducerVersion: "workflow-v1",
	})
	if err != nil {
		t.Fatalf("SubmitCanonicalContradictionProposal() error = %v", err)
	}
	pending := proposal.Proposal
	var statementProposalID string
	if err := pool.QueryRow(ctx, `
		SELECT origin_proposal_occurrence_id
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`, pending.NodeAID).Scan(&statementProposalID); err != nil {
		t.Fatalf("load canonical statement origin: %v", err)
	}
	assertDirectStatementOriginContradictionEdgeRejected(
		t,
		ctx,
		pool,
		"canon-edge:direct-statement-origin",
		pending.NodeAID,
		pending.NodeBID,
		statementProposalID,
	)

	assertDirectContradictionEdgeRejected(
		t,
		ctx,
		pool,
		"canon-edge:direct-reversed",
		pending.NodeBID,
		pending.NodeAID,
		evidencegraph.CanonicalContradicts,
		pending.ID,
	)
	assertDirectContradictionEdgeRejected(
		t,
		ctx,
		pool,
		"canon-edge:direct-wrong-relation",
		pending.NodeAID,
		pending.NodeBID,
		evidencegraph.CanonicalReferences,
		pending.ID,
	)
	assertDirectContradictionEdgeRejected(
		t,
		ctx,
		pool,
		"canon-edge:direct-pending",
		pending.NodeAID,
		pending.NodeBID,
		evidencegraph.CanonicalContradicts,
		pending.ID,
	)

	decision, err := AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{
		ProposalID:     pending.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "database invariant fixture approved through the governed path",
	})
	if err != nil {
		t.Fatalf("AdmitPendingCanonicalContradiction() error = %v", err)
	}
	if decision.Decision.CanonicalEdgeID == "" {
		t.Fatalf("decision lacks canonical edge: %+v", decision)
	}
}

func assertDirectContradictionEdgeRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	edgeID, fromNodeID, toNodeID string,
	relation evidencegraph.CanonicalEdgeRelation,
	proposalID string,
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
			origin_canonical_contradiction_proposal_id
		)
		VALUES ($1, $2, $3, $4, '{}'::jsonb, NULL, $5)
	`, edgeID, fromNodeID, toNodeID, string(relation), proposalID)
	if err != nil {
		return
	}
	if _, cleanupErr := pool.Exec(ctx, `DELETE FROM canonical_graph_edges WHERE canonical_edge_id = $1`, edgeID); cleanupErr != nil {
		t.Fatalf("invalid edge %s was accepted and cleanup failed: %v", edgeID, cleanupErr)
	}
	t.Fatalf("database accepted invalid contradiction edge %s", edgeID)
}

func assertDirectStatementOriginContradictionEdgeRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	edgeID, fromNodeID, toNodeID, statementProposalID string,
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
			origin_canonical_contradiction_proposal_id
		)
		VALUES ($1, $2, $3, 'contradicts', '{}'::jsonb, $4, NULL)
	`, edgeID, fromNodeID, toNodeID, statementProposalID)
	if err != nil {
		return
	}
	if _, cleanupErr := pool.Exec(ctx, `DELETE FROM canonical_graph_edges WHERE canonical_edge_id = $1`, edgeID); cleanupErr != nil {
		t.Fatalf("invalid edge %s was accepted and cleanup failed: %v", edgeID, cleanupErr)
	}
	t.Fatalf("database accepted statement-origin contradiction edge %s", edgeID)
}

func ingestAndAdmitContradictionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID, sourceID string) string {
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
		DecisionReason:       "create contradiction endpoint",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(%s) error = %v", requestID, err)
	}
	return admission.CanonicalRef
}
