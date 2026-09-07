//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

// Three actual compiled stdio processes and restricted logins witness the
// public route. Synthetic approval is not authenticated human review.
func TestIntegrationSourceClaimReviewSubprocessExactAdmission(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	fixture := newAuthorityProcessFixture(t, ctx, databaseURL, dbrole.ProfileSourceClaimReviewer)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", fixture.intake, fixture.schema, "intake")
	reviewer := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", fixture.reviewer, fixture.schema, "source-claim-reviewer")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", fixture.query, fixture.schema, "")
	reviewer.assertTools(t, []string{"get_source_claim_review", "admit_reviewed_source_claim"})
	intake.assertTools(t, []string{"submit_manual_evidence", "submit_text_source", "submit_external_source", "submit_extractor_output", "get_extractor_input"})
	query.assertTools(t, []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"})
	const statement = "合成退款應於七日內完成，並保留原始證據。"
	source := authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", map[string]any{
		"request_id": "review-source", "source_id": "mock:review-refunds", "source_version": "1", "raw_text": statement,
	})
	input := authorityProcessTool[evidenceingestionmcp.GetExtractorInputResponse](t, intake, "get_extractor_input", map[string]any{"extraction_view_id": source.ExtractionViewID})
	if input.RenderedText != statement || len(input.Spans) != 1 || input.Spans[0].Text != statement || input.Spans[0].QuotedTextHash != stdioContentHash([]byte(statement)) {
		t.Fatal("intake lost exact synthetic bytes")
	}
	proposal := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", map[string]any{
		"request_id": "review-output", "source_snapshot_id": source.SourceSnapshotID, "extraction_view_id": source.ExtractionViewID,
		"extractor_definition": map[string]any{"name": "mock-review-extractor", "version": "1", "config": map[string]string{}},
		"extractor_output":     map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "claim-1", "statement_text": statement, "evidence_refs": []string{input.Spans[0].SpanID}}}},
	})
	if proposal.Status != "pending" || proposal.ProposalCount != 1 {
		t.Fatal("intake must stop pending")
	}
	getArgs := evidenceingestionmcp.GetSourceClaimReviewRequest{ExtractionAttemptID: proposal.ExtractionAttemptID, ProposalOccurrenceID: proposal.ProposalOccurrenceID}
	review := authorityProcessTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", getArgs)
	if review.ProposalManifest.ProposalCount != 1 || len(review.ProposalManifest.Entries) != 1 || review.Subject.ReviewSubject.ProposalOccurrenceID != proposal.ProposalOccurrenceID || review.Subject.ReviewDisplayArtifactID != review.Display.ID || !strings.Contains(review.Display.PayloadUTF8, statement) {
		t.Fatal("review must retain complete exact subject and display")
	}
	var display evidenceingestion.ReviewPackage
	if json.Unmarshal([]byte(review.Display.PayloadUTF8), &display) != nil || display.ProposalBasis.StatementText != statement {
		t.Fatal("review display does not contain exact statement")
	}
	if display.ProposalBasis.SourceTitle != "" || display.ProposalBasis.SourceLocation != "" || display.ProposalBasis.SourceCoverage != "" {
		t.Fatal("plain manual input acquired invented qualification")
	}
	approved := evidenceingestionmcp.AdmitReviewedSourceClaimRequest{ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "合成流程核准，非真人身份驗證"}
	for _, process := range []*authorityProcess{query, intake} {
		process.assertDenied(t, "get_source_claim_review", getArgs)
		process.assertDenied(t, "admit_reviewed_source_claim", approved)
	}
	for _, tool := range []string{"submit_text_source", "submit_extractor_output", "admit_pending_proposal", "record_pending_proposal_disposition", "submit_canonical_contradiction_proposal", "admit_pending_supersession", "activate_repository_source_generation"} {
		reviewer.assertDenied(t, tool, approved)
	}
	for index, change := range []func(*evidenceingestionmcp.AdmitReviewedSourceClaimRequest){
		func(r *evidenceingestionmcp.AdmitReviewedSourceClaimRequest) {
			r.ExpectedSubject.ReviewDisplayArtifactID = "review-display:v1:sha256:" + strings.Repeat("a", 64)
		},
		func(r *evidenceingestionmcp.AdmitReviewedSourceClaimRequest) { r.Decision = "rejected" },
	} {
		rejected := approved
		change(&rejected)
		wantCode := "review_contract_conflict"
		if index == 1 {
			wantCode = "invalid_request"
		}
		assertReviewProcessToolError(t, reviewer, rejected, wantCode)
	}
	encoded, _ := json.Marshal(approved)
	for _, suffix := range []string{`,"decision_by":"spoof"}`, `,"decision":"approved"}`} {
		forged := append(append(json.RawMessage(nil), encoded[:len(encoded)-1]...), suffix...)
		assertReviewProcessToolError(t, reviewer, forged, "invalid_request")
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_ordinary_admission_manifests", "canonical_source_claim_review_bindings"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
	admitted := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", approved)
	if admitted.Replayed || admitted.AdmissionOutcome != "admitted" || admitted.CanonicalRef == "" || len(admitted.RawEvidenceNodeIDs) != 1 || len(admitted.CanonicalEdgeIDs) != 1 {
		t.Fatal("reviewed admission did not create one grounded canonical claim")
	}
	replay := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", approved)
	if !replay.Replayed {
		t.Fatal("exact reviewed retry was not marked replayed")
	}
	replay.Replayed = false
	if !reflect.DeepEqual(replay, admitted) {
		t.Fatal("exact retry changed admission identity")
	}
	changedReason := approved
	changedReason.DecisionReason = "changed"
	assertReviewProcessToolError(t, reviewer, changedReason, "admission_replay_conflict")
	changedSubject := approved
	changedSubject.ExpectedSubject.ReviewDisplayArtifactID = "review-display:v1:sha256:" + strings.Repeat("b", 64)
	assertReviewProcessToolError(t, reviewer, changedSubject, "admission_replay_conflict")
	record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": proposal.ProposalOccurrenceID})
	if record.AdmissionOutcome != "admitted" || record.CanonicalRef == nil || *record.CanonicalRef != admitted.CanonicalRef || record.StatementText != statement || len(record.SourceRefs) != 1 || record.SourceRefs[0].QuotedText != statement || record.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(statement)) {
		t.Fatal("Query lost admitted lifecycle or exact provenance")
	}
	canonical := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admitted.CanonicalRef})
	if canonical.RecordRef.Kind != "canonical_evidence" || canonical.RecordRef.ID != admitted.CanonicalRef || canonical.Canonical == nil || canonical.ProposalOriginRef == nil || canonical.ProposalOriginRef.ID != proposal.ProposalOccurrenceID || canonical.StatementText != statement || len(canonical.SourceRefs) != 1 || canonical.SourceRefs[0].QuotedText != statement {
		t.Fatal("canonical Query readback lost the exact claim, origin or quotation")
	}
	// Existing Query tools do not expose the review binding receipt. Check that
	// separately by acceptance SELECT, without claiming a new Query capability.
	var payload, artifact, reviewerID, mode string
	if err := fixture.pool.QueryRow(ctx, `SELECT b.review_display_payload_utf8, b.review_display_artifact_id, d.decision_by, d.review_binding_contract_version FROM canonical_source_claim_review_bindings b JOIN admission_decisions d USING (admission_decision_id) WHERE b.admission_decision_id=$1`, admitted.AdmissionDecisionID).Scan(&payload, &artifact, &reviewerID, &mode); err != nil {
		t.Fatal(err)
	}
	if payload != review.Display.PayloadUTF8 || artifact != review.Subject.ReviewDisplayArtifactID || reviewerID != "mock:ahe-ingest-mcp" || mode != evidenceingestion.ReviewedSourceClaimAdmissionV1 {
		t.Fatal("persisted exact review or launcher identity drifted")
	}
	for table, count := range map[string]int{"source_snapshots": 1, "extraction_attempts": 1, "proposal_batches": 1, "proposal_occurrences": 1, "canonical_graph_nodes": 2, "canonical_graph_edges": 1, "admission_decisions": 1, "canonical_ordinary_admission_manifests": 1, "canonical_ordinary_admission_node_bindings": 2, "canonical_ordinary_admission_edge_bindings": 1, "canonical_source_claim_review_bindings": 1, "canonical_contradiction_proposals": 0, "canonical_supersession_admission_events": 0, "repository_generation_activation_requests": 0} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, count)
	}
	query.finish(t)
	reviewer.finish(t)
	intake.finish(t)
}

func assertReviewProcessToolError(t *testing.T, process *authorityProcess, arguments any, wantCode string) {
	t.Helper()
	response := process.request(t, "tools/call", map[string]any{"name": "admit_reviewed_source_claim", "arguments": arguments})
	var envelope struct {
		IsError           bool `json:"isError"`
		StructuredContent struct {
			Code string `json:"code"`
		} `json:"structuredContent"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &envelope) != nil || !envelope.IsError || envelope.StructuredContent.Code != wantCode {
		t.Fatalf("invalid or changed reviewed approval did not return the expected structured code %s (payload suppressed)", wantCode)
	}
}
