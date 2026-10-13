//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/google/jsonschema-go/jsonschema"
)

// TEST APPROVAL STUB: all material and decisions belong to disposable fixtures.
func TestIntegrationGovernanceStandardMCP(t *testing.T) {
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("dedicated acceptance database required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileWithdrawalReviewer, dbrole.ProfileSourceAssessmentReviewer)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.intake, f.schema, "intake")
	claims := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.reviewer, f.schema, "source-claim-reviewer")
	withdrawals := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.withdrawal, f.schema, "withdrawal-reviewer")
	assessments := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", f.sourceAssessment, f.schema, "source-assessment-reviewer")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", f.query, f.schema, "")
	withdrawals.assertTools(t, []string{"get_withdrawal_review", "apply_reviewed_withdrawal", "prepare_withdrawal_batch", "apply_reviewed_withdrawal_batch"})
	assessments.assertTools(t, []string{"get_source_assessment_review", "apply_reviewed_source_assessment", "prepare_source_assessment_batch", "apply_reviewed_source_assessment_batch"})
	const statement = "Synthetic evidence whose current use can be withdrawn."
	src := authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", map[string]any{"request_id": "gov-source", "source_id": "synthetic:governance", "source_version": "v1", "raw_text": statement})
	proposal := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", map[string]any{"request_id": "gov-extract", "source_snapshot_id": src.SourceSnapshotID, "extraction_view_id": src.ExtractionViewID, "extractor_definition": map[string]any{"name": "TEST APPROVAL STUB", "version": "v1"}, "extractor_output": map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "claim", "statement_text": statement, "evidence_refs": []string{"span:S1"}}}}})
	review := authorityProcessTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, claims, "get_source_claim_review", map[string]any{"extraction_attempt_id": proposal.ExtractionAttemptID, "proposal_occurrence_id": proposal.ProposalOccurrenceID})
	admitted := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, claims, "admit_reviewed_source_claim", map[string]any{"extraction_attempt_id": proposal.ExtractionAttemptID, "expected_subject": review.Subject, "decision": "approved", "decision_reason": "TEST APPROVAL STUB"})
	open := map[string]any{"root_node_ids": []string{admitted.CanonicalRef}, "max_depth": 0, "max_nodes": 1, "max_edges": 0}
	before := authorityProcessTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, query, "open_canonical_read_view", open)
	req := evidenceingestion.GovernanceRequest{Kind: "withdrawal", TargetID: admitted.CanonicalRef, Operation: "withdraw"}
	withdrawalReview := authorityProcessTool[evidenceingestion.GovernanceReview](t, withdrawals, "get_withdrawal_review", req)
	decision := evidenceingestion.GovernanceDecision{RequestID: "gov-withdraw", Review: withdrawalReview.Display.Request, ExpectedSubject: withdrawalReview.Subject, Decision: "approved", Reason: "TEST APPROVAL STUB: known wrong claim"}
	query.assertDenied(t, "apply_reviewed_withdrawal", decision)
	assessments.assertDenied(t, "apply_reviewed_withdrawal", decision)
	receipt := authorityProcessTool[evidenceingestion.GovernanceReceipt](t, withdrawals, "apply_reviewed_withdrawal", decision)
	replay := authorityProcessTool[evidenceingestion.GovernanceReceipt](t, withdrawals, "apply_reviewed_withdrawal", decision)
	if !replay.Replayed || replay.EventID != receipt.EventID || receipt.ReviewerID != "mock:ahe-ingest-mcp" {
		t.Fatal("native receipt/reviewer/replay mismatch")
	}
	after := authorityProcessTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, query, "open_canonical_read_view", open)
	if before.Artifact.SnapshotID != after.Artifact.SnapshotID || before.View.Handle == after.View.Handle || before.View.GovernanceDigest == after.View.GovernanceDigest {
		t.Fatal("governance-only change did not invalidate new view identity")
	}
	record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admitted.CanonicalRef})
	if record.UseState == nil || record.UseState.Snapshot.Nodes[admitted.CanonicalRef].Withdrawal.EventID != receipt.EventID || record.AdmissionOutcome != "admitted" {
		t.Fatal("Query mixed withdrawal and admission")
	}
	occurrence := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": proposal.ProposalOccurrenceID})
	if occurrence.UseState == nil || occurrence.UseState.Assessment.StateDigest != record.UseState.Assessment.StateDigest || occurrence.RecordRef.Kind != "proposal" || occurrence.AdmissionOutcome != "admitted" {
		t.Fatal("occurrence and canonical record governance differ at unchanged cut")
	}
	relationRejected(t, query, "check_current_evidence_plan", map[string]any{"actions": []evidenceuse.ActionBasis{{ActionID: "missing-purpose", BasisIDs: []string{admitted.CanonicalRef}}}})
	grounded := authorityProcessTool[evidenceingestion.EvidenceUseProjection](t, query, "get_evidence_use_state", map[string]any{"canonical_ids": []string{admitted.CanonicalRef}, "policy": evidenceuse.GroundedActionV1})
	if grounded.Assessment.Policy != evidenceuse.GroundedActionV1 {
		t.Fatal("Query did not honor selected grounded purpose")
	}
	plan := authorityProcessTool[evidenceuse.PlanAssessment](t, query, "check_current_evidence_plan", map[string]any{"policy": "current-action/v1", "actions": []evidenceuse.ActionBasis{{ActionID: "synthetic-action", BasisIDs: []string{admitted.CanonicalRef}}}})
	if plan.Accepted {
		t.Fatal("action gate reused prior context")
	}
	source := record.UseState.Snapshot.Sources[src.SourceSnapshotID]
	sr := evidenceingestion.GovernanceRequest{Kind: "source_assessment", TargetID: src.SourceSnapshotID, Operation: "assess", MaterialIDs: []string{src.SourceSnapshotID}, Method: "synthetic retrospective review", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "v1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"historical comparison not available"}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: src.SourceSnapshotID, ContentHash: source.ContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: evidenceuse.CorrespondenceV1, Result: evidenceuse.CheckInconclusive, MaterialRef: src.SourceSnapshotID}}}
	sourceReview := authorityProcessTool[evidenceingestion.GovernanceReview](t, assessments, "get_source_assessment_review", sr)
	authorityProcessTool[evidenceingestion.GovernanceReceipt](t, assessments, "apply_reviewed_source_assessment", evidenceingestion.GovernanceDecision{RequestID: "gov-assess", Review: sourceReview.Display.Request, ExpectedSubject: sourceReview.Subject, Decision: "approved", Reason: "TEST APPROVAL STUB: retain uncertainty"})
	history := authorityProcessTool[[]evidenceingestion.GovernanceReceipt](t, query, "get_evidence_governance_history", map[string]any{"kind": "source_assessment", "target_id": src.SourceSnapshotID})
	if len(history) != 1 || history[0].Display.Source.Snapshot.ID != src.SourceSnapshotID {
		t.Fatal("source review history missing exact bytes/declarations")
	}
	sourceView := authorityProcessTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, query, "open_canonical_read_view", open)
	if sourceView.Artifact.SnapshotID != after.Artifact.SnapshotID || sourceView.View.Handle == after.View.Handle || sourceView.UseState.Snapshot.Sources[src.SourceSnapshotID].AssessmentRevision != 1 {
		t.Fatal("source-only assessment change did not update view governance identity")
	}
	sourceRecord := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admitted.CanonicalRef})
	if sourceRecord.UseState.Assessment.StateDigest != sourceView.UseState.Assessment.StateDigest {
		t.Fatal("record and graph source projections disagree on an unchanged cut")
	}
	sourceOccurrence := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": proposal.ProposalOccurrenceID})
	if sourceOccurrence.UseState == nil || sourceOccurrence.UseState.Assessment.StateDigest != sourceView.UseState.Assessment.StateDigest || sourceOccurrence.UseState.Snapshot.Sources[src.SourceSnapshotID].AssessmentRevision != 1 {
		t.Fatal("source assessment hidden by occurrence lookup")
	}
	// A purely human historical exclusion has no fabricated checker/cache fields.
	canonical, err := evidenceingestion.GetCanonicalEvidenceByID(ctx, f.pool, admitted.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(canonical.Payload)
	if err != nil {
		t.Fatal(err)
	}
	native := withdrawalReview.Display.Request
	native.ExpectedRevision, native.PreviousEventID = receipt.Revision, receipt.EventID
	local := evidenceingestion.InterimGovernanceRecord{Version: evidenceingestion.InterimGovernanceV1,
		RecordID: "local:mcp-manual", Revision: 1, StoreID: sourceRecord.UseState.Snapshot.StoreID,
		Kind: "exclusion", CanonicalID: admitted.CanonicalRef, ClaimHash: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)),
		OccurrenceID: proposal.ProposalOccurrenceID, Purpose: "current-action", Scope: "claim",
		Reason: "TEST APPROVAL STUB: original human reason", Outcome: "exclude",
		HistoricalReviewer: "TEST APPROVAL STUB: historical human", HistoricalReviewedAt: time.Now().UTC(),
		OriginalMaterials: []evidenceuse.Material{{Identity: src.SourceSnapshotID, Revision: "v1", ContentHash: source.ContentHash}},
		MissingFields:     []string{}, Native: native}
	batch, manifest := governancePrepareMixedBatch(t, withdrawals, "withdrawal", local, local)
	item := batch.Manifest.Items[0]
	if item.Status != "requires_exact_native_approval" || item.NativeRequest == nil || item.Record.CheckerName != "" || item.Record.Reason == item.Reason {
		t.Fatal("MCP lost historical human record or native manifest", item.Status, item.Error)
	}
	approval := evidenceingestion.GovernanceBatchApproval{Manifest: batch.Manifest, ExpectedManifestSubject: batch.Subject,
		ApprovedItems: []evidenceingestion.GovernanceBatchDecision{{RecordID: local.RecordID, Decision: evidenceingestion.GovernanceDecision{
			RequestID: item.NativeRequestID, Review: *item.NativeRequest, ExpectedSubject: item.NativeSubject, Decision: "approved", Reason: item.Reason}}}}
	relationRejected(t, assessments, "apply_reviewed_source_assessment_batch", approval)
	query.assertDenied(t, "apply_reviewed_withdrawal_batch", approval)
	changed := approval
	changed.ExpectedManifestSubject = "governance-batch:sha256:changed"
	relationRejected(t, withdrawals, "apply_reviewed_withdrawal_batch", changed)
	governanceRejectNonNativeBatchItems(t, withdrawals, "withdrawal", approval, manifest)
	wireApproval := governanceBatchWireApproval(t, withdrawals, "withdrawal", approval, manifest)
	for _, want := range []string{"applied", "replayed"} {
		results := authorityProcessTool[[]evidenceingestion.GovernanceBatchResult](t, withdrawals, "apply_reviewed_withdrawal_batch", wireApproval)
		if len(results) != 1 || results[0].Status != want || results[0].Receipt == nil || results[0].Receipt.BatchSubject != batch.Subject || len(results[0].Readback) != 2 {
			t.Fatal("batch MCP receipt/readback/replay mismatch", results)
		}
	}
	withdrawalHistory := authorityProcessTool[[]evidenceingestion.GovernanceReceipt](t, query, "get_evidence_governance_history", map[string]any{"kind": "withdrawal", "target_id": admitted.CanonicalRef})
	if len(withdrawalHistory) != 2 || withdrawalHistory[1].BatchSubject != batch.Subject {
		t.Fatal("Query did not retain exactly the approved native migration")
	}
	// Source-findings migration uses the same manifest contract through its
	// separately qualified writer, preserving uncertainty and exact bindings.
	sr = sourceReview.Display.Request
	sr.ExpectedRevision, sr.PreviousEventID = history[0].Revision, history[0].EventID
	checkedAt, err := time.Parse(time.RFC3339Nano, sr.CheckedAt)
	if err != nil {
		t.Fatal(err)
	}
	sourceLocal := evidenceingestion.InterimGovernanceRecord{Version: evidenceingestion.InterimGovernanceV1,
		RecordID: "local:mcp-source-finding", Revision: 1, StoreID: local.StoreID, Kind: "source_assessment",
		SourceSnapshotID: src.SourceSnapshotID, SourceContentHash: source.ContentHash,
		Purpose: "grounded-current-action", Scope: evidenceuse.RetainedSnapshotScope,
		Reason: "TEST APPROVAL STUB: historical comparison unavailable", Outcome: "inconclusive",
		HistoricalReviewer: local.HistoricalReviewer, HistoricalReviewedAt: local.HistoricalReviewedAt,
		CheckerName: sr.CheckerName, CheckerVersion: sr.CheckerVersion, CheckedAt: checkedAt,
		Criterion: evidenceuse.CorrespondenceV1, CriterionVersion: "v1", OriginalMaterials: local.OriginalMaterials,
		CheckedMaterials: local.OriginalMaterials, MissingFields: []string{}, Limitations: sr.Limitations, Native: sr}
	sourceBatch, sourceManifest := governancePrepareMixedBatch(t, assessments, "source_assessment", sourceLocal, local)
	sourceItem := sourceBatch.Manifest.Items[0]
	if sourceItem.Status != "requires_exact_native_approval" || sourceItem.NativeRequest == nil {
		t.Fatal("source migration lost native review", sourceItem.Status, sourceItem.Error)
	}
	sourceApproval := evidenceingestion.GovernanceBatchApproval{Manifest: sourceBatch.Manifest, ExpectedManifestSubject: sourceBatch.Subject,
		ApprovedItems: []evidenceingestion.GovernanceBatchDecision{{RecordID: sourceLocal.RecordID, Decision: evidenceingestion.GovernanceDecision{
			RequestID: sourceItem.NativeRequestID, Review: *sourceItem.NativeRequest, ExpectedSubject: sourceItem.NativeSubject, Decision: "approved", Reason: sourceItem.Reason}}}}
	relationRejected(t, withdrawals, "apply_reviewed_withdrawal_batch", sourceApproval)
	governanceRejectNonNativeBatchItems(t, assessments, "source_assessment", sourceApproval, sourceManifest)
	sourceWireApproval := governanceBatchWireApproval(t, assessments, "source_assessment", sourceApproval, sourceManifest)
	for _, want := range []string{"applied", "replayed"} {
		results := authorityProcessTool[[]evidenceingestion.GovernanceBatchResult](t, assessments, "apply_reviewed_source_assessment_batch", sourceWireApproval)
		if len(results) != 1 || results[0].Status != want || results[0].Receipt == nil || results[0].Receipt.BatchSubject != sourceBatch.Subject || len(results[0].Readback) != 2 {
			t.Fatal("source batch MCP receipt/readback/replay mismatch", results)
		}
	}
	sourceHistory := authorityProcessTool[[]evidenceingestion.GovernanceReceipt](t, query, "get_evidence_governance_history", map[string]any{"kind": "source_assessment", "target_id": src.SourceSnapshotID})
	if len(sourceHistory) != 2 || sourceHistory[1].BatchSubject != sourceBatch.Subject {
		t.Fatal("Query did not retain exactly the approved source migration")
	}
}

// governancePrepareMixedBatch validates the advertised prepare schema before
// calling the real subprocess. Only fixture inputs are constructed here; the
// returned manifest is captured as raw JSON and never repaired or rebuilt.
func governancePrepareMixedBatch(t *testing.T, process *authorityProcess, kind string, record, claim evidenceingestion.InterimGovernanceRecord) (evidenceingestion.PreparedGovernanceBatch, json.RawMessage) {
	t.Helper()
	verification := claim
	verification.RecordID = "local:" + kind + ":verification"
	verification.Kind, verification.Outcome = "verification", "pass"
	verification.Native = evidenceingestion.GovernanceRequest{}
	verification.Criterion, verification.CriterionVersion = "synthetic-statement-support", "v1"
	verification.CheckedAt = verification.HistoricalReviewedAt
	verification.CheckedMaterials = verification.OriginalMaterials
	verification.FreshnessRule = "live exact revision and content comparison"
	verification.Verification = &evidenceuse.VerificationReport{Version: evidenceuse.VerificationV1,
		Subject: evidenceuse.VerificationSubject{StoreID: verification.StoreID, CanonicalID: verification.CanonicalID, ClaimHash: verification.ClaimHash,
			Criterion: verification.Criterion, CriterionVersion: verification.CriterionVersion, Scope: verification.Scope},
		Materials: verification.CheckedMaterials, MaterialsComplete: true, Result: evidenceuse.CheckPass, CheckedAt: verification.CheckedAt}
	blocked := record
	blocked.RecordID = "local:" + kind + ":missing"
	blocked.HistoricalReviewer = ""
	blocked.MissingFields = []string{"historical_reviewer"}
	items := []any{}
	for _, original := range []evidenceingestion.InterimGovernanceRecord{record, verification, blocked} {
		input := governanceWireJSON(t, original).(map[string]any)
		// Exercise valid omission, not a Go struct's explicit null values.
		for _, field := range []string{"checked_materials", "limitations"} {
			if input[field] == nil {
				delete(input, field)
			}
		}
		if original.Kind == "verification" {
			delete(input, "native")
		}
		items = append(items, map[string]any{"record": input, "reason": "TEST APPROVAL STUB: new native migration reason"})
	}
	args := map[string]any{"items": items}
	governanceAssertWireSchema(t, process, "prepare_"+kind+"_batch", args)
	batch := authorityProcessTool[evidenceingestion.PreparedGovernanceBatch](t, process, "prepare_"+kind+"_batch", args)
	var wire struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(process.lastToolContent, &wire); err != nil {
		t.Fatal(err)
	}
	if len(batch.Manifest.Items) != 3 {
		t.Fatal("manifest lost original items")
	}
	for i, status := range []string{"requires_exact_native_approval", "verification_only_no_native_effect", "blocked"} {
		item := batch.Manifest.Items[i]
		if item.Status != status || i > 0 && (item.NativeRequest != nil || item.NativeSubject != "" || item.NativeRequestID != "") {
			t.Fatal("incorrect mixed manifest classification", i, item.Status, item.Error)
		}
		raw, err := json.Marshal(item.Record)
		if err != nil {
			t.Fatal(err)
		}
		if item.RecordHash != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
			t.Fatal("historical record fingerprint changed")
		}
	}
	return batch, wire.Manifest
}

func governanceBatchWireApproval(t *testing.T, process *authorityProcess, kind string, approval evidenceingestion.GovernanceBatchApproval, manifest json.RawMessage) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"manifest": manifest, "expected_manifest_subject": approval.ExpectedManifestSubject, "approved_items": approval.ApprovedItems})
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifest, sent.Manifest) {
		t.Fatal("approval did not preserve the exact returned manifest JSON")
	}
	governanceAssertWireSchema(t, process, "apply_reviewed_"+kind+"_batch", json.RawMessage(raw))
	return raw
}

func governanceRejectNonNativeBatchItems(t *testing.T, process *authorityProcess, kind string, approval evidenceingestion.GovernanceBatchApproval, manifest json.RawMessage) {
	t.Helper()
	for _, item := range approval.Manifest.Items[1:] {
		selected := approval.ApprovedItems[0]
		selected.RecordID = item.Record.RecordID
		attempt := approval
		attempt.ApprovedItems = []evidenceingestion.GovernanceBatchDecision{selected}
		wire := governanceBatchWireApproval(t, process, kind, attempt, manifest)
		results := authorityProcessTool[[]evidenceingestion.GovernanceBatchResult](t, process, "apply_reviewed_"+kind+"_batch", wire)
		if len(results) != 1 || results[0].Status != "failed" || results[0].Receipt != nil {
			t.Fatal("non-native item acquired write authority", item.Status)
		}
	}
}

func governanceAssertWireSchema(t *testing.T, process *authorityProcess, name string, args any) {
	t.Helper()
	response := process.request(t, "tools/list", map[string]any{})
	var listed struct {
		Tools []mcpstdio.Tool `json:"tools"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &listed) != nil {
		t.Fatal("cannot read published schema")
	}
	for _, tool := range listed.Tools {
		if tool.Name != name {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(governanceWireJSON(t, args)); err != nil {
			t.Fatalf("%s wire input violates advertised schema: %v", name, err)
		}
		return
	}
	t.Fatal("missing tool schema", name)
}

func governanceWireJSON(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
