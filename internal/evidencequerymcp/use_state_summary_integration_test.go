//go:build integration

package evidencequerymcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
)

func TestIntegrationAssessmentOnlyQueryReadbacks(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingest, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatal(err)
	}
	p := callSubmitManualEvidence(t, ctx, ingest, integrationSubmitRequest(t, "assessment-only"))
	a := callAdmitPendingProposal(t, ctx, ingest, evidenceingestionmcp.AdmitPendingProposalRequest{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "TEST APPROVAL STUB"})
	s, err := NewServer(pool)
	if err != nil {
		t.Fatal(err)
	}
	before := allTableCountDigest(t, ctx, pool)
	requests := []struct {
		tool  string
		input map[string]any
	}{
		{ToolGetEvidenceUseState, map[string]any{"canonical_ids": []string{a.CanonicalRef}, "policy": "current-action/v1"}},
		{ToolGetEvidenceRecord, map[string]any{"canonical_id": a.CanonicalRef}},
		{ToolGetEvidenceRecord, map[string]any{"proposal_occurrence_id": p.ProposalOccurrenceID}},
		{ToolOpenCanonicalReadView, map[string]any{"root_node_ids": []string{a.CanonicalRef}, "max_depth": 1, "max_nodes": 10, "max_edges": 20}},
	}
	for _, r := range requests {
		raw, _ := json.Marshal(r.input)
		full, err := s.CallTool(queryOwnerContext(t, "integration-summary"), r.tool, raw)
		if err != nil {
			t.Fatal(r.tool, err)
		}
		r.input["use_state_mode"] = "assessment_only"
		raw, _ = json.Marshal(r.input)
		compact, err := s.CallTool(queryOwnerContext(t, "integration-summary"), r.tool, raw)
		if err != nil {
			t.Fatal(r.tool, err)
		}
		var f, c map[string]json.RawMessage
		if json.Unmarshal(full, &f) != nil || json.Unmarshal(compact, &c) != nil {
			t.Fatal("invalid response")
		}
		var projection evidenceingestion.EvidenceUseProjection
		var summary UseStateSummary
		if r.tool == ToolGetEvidenceUseState {
			if json.Unmarshal(full, &projection) != nil || json.Unmarshal(compact, &summary) != nil || c["snapshot"] != nil {
				t.Fatal("use-state transport")
			}
		} else {
			if json.Unmarshal(f["use_state"], &projection) != nil || json.Unmarshal(c["use_state_summary"], &summary) != nil || c["use_state"] != nil {
				t.Fatal("nested transport", r.tool)
			}
			delete(f, "use_state")
			delete(c, "use_state_summary")
			if !reflect.DeepEqual(f, c) {
				t.Fatal("record/artifact/view changed with transport mode", r.tool)
			}
		}
		// Independent read calls have distinct evaluation times, but the unchanged
		// store must yield the same full-state digest and complete node decisions.
		projection.Assessment.EvaluatedAt = summary.Assessment.EvaluatedAt
		if !reflect.DeepEqual(projection.Assessment, summary.Assessment) || !summary.SnapshotContext.SnapshotOmitted {
			t.Fatal("assessment semantics changed", r.tool)
		}
	}
	if after := allTableCountDigest(t, ctx, pool); after != before {
		t.Fatal("query changed persistent table counts")
	}
}
