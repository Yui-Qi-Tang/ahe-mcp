//go:build integration

package mcpintegration

import (
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

func TestIntegrationAssessmentOnlyStandardMCP(t *testing.T) {
	f := newHistoricalProcessFixture(t)
	_, parents := historicalProcessSources(t, f)
	query := f.processes["query"]
	before := historicalProcessState(t, f)
	summary := relationTool[evidencequerymcp.UseStateSummary](t, query, "get_evidence_use_state", map[string]any{
		"canonical_ids": []string{parents[0]}, "policy": "current-action/v1", "use_state_mode": "assessment_only",
	})
	if summary.Contract != "evidence-use-summary/v1" || !summary.SnapshotContext.SnapshotOmitted || summary.SnapshotContext.DigestRecomputableFromResponse || summary.Assessment.StateDigest == "" {
		t.Fatal("standard MCP summary contract missing")
	}
	record := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{
		"canonical_id": parents[0], "use_state_mode": "assessment_only",
	})
	if record.UseState != nil || record.UseStateSummary == nil || record.UseStateSummary.Assessment.StateDigest != summary.Assessment.StateDigest || record.Canonical == nil {
		t.Fatal("standard MCP record summary lost state/record")
	}
	view := relationTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, query, "open_canonical_read_view", map[string]any{
		"root_node_ids": []string{parents[0]}, "max_depth": 0, "max_nodes": 1, "max_edges": 0, "use_state_mode": "assessment_only",
	})
	if view.UseState != nil || view.UseStateSummary == nil || view.View.Handle == "" || len(view.Artifact.Nodes) != 1 {
		t.Fatal("standard MCP bounded artifact missing")
	}
	full := relationTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": parents[0], "use_state_mode": "full"})
	if full.UseState == nil || full.UseStateSummary != nil || full.UseState.Assessment.StateDigest != summary.Assessment.StateDigest {
		t.Fatal("standard MCP explicit full changed")
	}
	after := historicalProcessState(t, f)
	if len(before) != len(after) {
		t.Fatal("query changed fixture relations")
	}
	for name, hash := range before {
		if after[name] != hash {
			t.Fatal("query changed table/sequence", name)
		}
	}
}
