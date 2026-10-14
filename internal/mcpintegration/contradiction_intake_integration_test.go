//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpcontradictions"
)

// TEST APPROVAL STUB: synthetic evidence in a disposable DB, through real
// compiled stdio processes and independently installed/qualified DB roles.
func TestIntegrationContradictionIntakeStandardMCP(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileContradictionReviewer)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.intake, f.schema, "intake")
	reviewer := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.reviewer, f.schema, "source-claim-reviewer")
	contradiction := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.contradiction, f.schema, "contradiction-reviewer")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "query")
	contradiction.assertTools(t, []string{mcpcontradictions.ToolReview, mcpcontradictions.ToolApply})
	for _, p := range []*authorityProcess{intake, reviewer, query} {
		p.assertDenied(t, mcpcontradictions.ToolApply, map[string]any{})
	}
	for _, name := range []string{"admit_pending_canonical_contradiction", "admit_pending_proposal", "admit_reviewed_source_claim", "admit_reviewed_implements", "admit_reviewed_supersession"} {
		contradiction.assertDenied(t, name, map[string]any{})
	}
	submit := func(label, statement string) evidenceingestionmcp.SubmitExtractorOutputResponse {
		src := relationTool[evidenceingestionmcp.SubmitExternalSourceResponse](t, intake, "submit_external_source", evidenceingestion.ExternalSourceEnvelopeV1{SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "source-" + label, SourceSystem: "lab", SourceNamespace: "contradiction-synthetic", ObjectType: "document", ObjectID: "policy", Revision: label, SourceLocation: "https://example.invalid/policy", Title: "Synthetic policy", ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim, Content: statement + "\n", Coverage: evidenceingestion.ExternalSourceCoverageFullDocument, Limitations: []string{}, CollectorID: "synthetic-collector", ConnectorID: "synthetic-connector", ObservedAt: "2026-10-14T00:00:00Z"})
		return relationTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{RequestID: "extract-" + label, SourceSnapshotID: src.SourceSnapshotID, ExtractionViewID: src.ExtractionViewID, ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "synthetic", Version: "v1"}, ExtractorOutput: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{src.Spans[0].SpanID}}}}})
	}
	admit := func(p evidenceingestionmcp.SubmitExtractorOutputResponse) string {
		r := relationTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", map[string]any{"extraction_attempt_id": p.ExtractionAttemptID, "proposal_occurrence_id": p.ProposalOccurrenceID})
		return relationTool[evidenceingestionmcp.AdmitPendingProposalResponse](t, reviewer, "admit_reviewed_source_claim", map[string]any{"extraction_attempt_id": p.ExtractionAttemptID, "expected_subject": r.Subject, "decision": "approved", "decision_reason": "TEST APPROVAL STUB: synthetic source review"}).CanonicalRef
	}
	a := submit("a", "The synthetic policy permits operation.")
	aID := admit(a)
	b := submit("b", "The synthetic policy forbids operation.")
	suggestions := relationTool[evidenceingestion.ConflictSuggestionResult](t, intake, evidenceingestionmcp.ToolSuggestIntakeConflicts, evidenceingestion.ConflictSuggestionRequest{ProposalOccurrenceID: b.ProposalOccurrenceID})
	if len(suggestions.Suggestions) != 1 || suggestions.Suggestions[0].CanonicalID != aID || suggestions.Coverage.RelationAnchorAvailable {
		t.Fatal("intake suggestion lost matching basis")
	}
	bID := admit(b)
	r := relationTool[evidenceingestion.ContradictionReview](t, contradiction, mcpcontradictions.ToolReview, evidenceingestion.ContradictionReviewRequest{ProposalRequestID: "pair", NodeAID: bID, NodeBID: aID, Rationale: "TEST APPROVAL STUB: synthetic incompatible policy", Scope: "same synthetic release", Conditions: []string{"same operation"}, ProducerName: "synthetic", ProducerVersion: "v1", Relation: "contradicts"})
	in := evidenceingestion.ReviewedContradictionInput{RequestID: "decision", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: independent relation approval"}
	first := relationTool[evidenceingestion.ContradictionReviewReceipt](t, contradiction, mcpcontradictions.ToolApply, in)
	if first.Decision.DecisionBy == "" || first.Decision.CanonicalEdgeID == "" {
		t.Fatal("missing launcher identity or edge")
	}
	retry := relationTool[evidenceingestion.ContradictionReviewReceipt](t, contradiction, mcpcontradictions.ToolApply, in)
	if !retry.Replayed || retry.ID != first.ID {
		t.Fatal("exact retry changed decision")
	}
	native := relationTool[evidencequerymcp.CanonicalContradictionProposalResponse](t, query, "get_canonical_contradiction_proposal", map[string]any{"canonical_contradiction_proposal_id": first.Decision.ProposalID})
	if native.ReviewReceipt == nil || native.ReviewReceipt.ID != first.ID || native.Proposal.NodeAID != r.Display.Request.NodeAID || native.Proposal.NodeBID != r.Display.Request.NodeBID {
		t.Fatal("Query lost exact pair provenance")
	}
	// Tool decoder must reject injected launcher-owned authority before mutation.
	raw, _ := json.Marshal(in)
	var injected map[string]any
	json.Unmarshal(raw, &injected)
	injected["reviewer_id"] = "forged"
	response := contradiction.request(t, "tools/call", map[string]any{"name": mcpcontradictions.ToolApply, "arguments": injected})
	var envelope struct {
		IsError bool `json:"isError"`
	}
	if response.Error == nil && (json.Unmarshal(response.Result, &envelope) != nil || !envelope.IsError) {
		t.Fatal("caller reviewer authority accepted")
	}
	// Direct DB writes are unavailable even though the profile can admit its pair.
	p := lifecyclePool(t, ctx, f, f.contradiction)
	for _, sql := range []string{`INSERT INTO canonical_graph_nodes SELECT * FROM canonical_graph_nodes WHERE false`, `INSERT INTO canonical_graph_edges SELECT * FROM canonical_graph_edges WHERE false`, `UPDATE canonical_contradiction_proposals SET rationale=rationale WHERE false`} {
		if _, err := p.Exec(ctx, sql); err == nil {
			t.Fatal("native profile authority broadened")
		}
	}
}
