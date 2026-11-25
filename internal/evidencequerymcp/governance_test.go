package evidencequerymcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

func TestCurrentGovernanceToolsRequireExplicitKnownPolicy(t *testing.T) {
	s := newServer(&fakeQueryCore{})
	for _, name := range []string{ToolGetEvidenceUseState, ToolCheckCurrentPlan} {
		for _, policy := range []string{"", "historical", "grounded-current-action/v999"} {
			req := map[string]any{"policy": policy}
			if name == ToolGetEvidenceUseState {
				req["canonical_ids"] = []string{"canon-node:synthetic"}
			} else {
				req["actions"] = []any{map[string]any{"action_id": "act", "basis_ids": []string{"canon-node:synthetic"}}}
			}
			payload, _ := json.Marshal(req)
			if _, err := s.CallTool(context.Background(), name, payload); err == nil || !strings.Contains(err.Error(), "policy") {
				t.Fatal("missing/unknown policy reached a DB clearance", name, policy, err)
			}
		}
	}
}

func TestAdmittedOccurrenceKeepsGovernanceProjection(t *testing.T) {
	state := &evidenceingestion.EvidenceUseProjection{Snapshot: evidenceuse.Snapshot{Version: evidenceuse.ProjectionV1}}
	p := evidenceingestion.ProposalQueryResult{ProposalOccurrenceID: "occ:synthetic", AdmissionOutcome: "admitted", CanonicalRef: "canon-node:synthetic", UseState: state,
		SourceSystem: evidenceingestion.SourceSystemExternalDocument, OriginMetadata: map[string]string{"external_content_fidelity": "verbatim", "external_coverage": "exact_excerpt"}}
	s := newServer(&fakeQueryCore{result: p})
	out, err := s.GetEvidenceRecord(context.Background(), GetEvidenceRecordRequest{ProposalOccurrenceID: p.ProposalOccurrenceID})
	if err != nil {
		t.Fatal(err)
	}
	if out.UseState != state || out.RecordRef.Kind != "proposal" || out.AdmissionOutcome != "admitted" || out.Source.ExternalSource.AssessmentProjection != "evidence-use-projection/v1" || out.Source.ExternalSource.ContentFidelity != "verbatim" {
		t.Fatal("occurrence lost state or original provenance", out)
	}
}

func TestSourceAssessmentLabelMatchesProjectedVersion(t *testing.T) {
	for _, version := range []string{evidenceuse.ProjectionV1, evidenceuse.ProjectionV2} {
		t.Run(version, func(t *testing.T) {
			state := &evidenceingestion.EvidenceUseProjection{Snapshot: evidenceuse.Snapshot{Version: version}}
			origin := evidenceingestion.ProposalQueryResult{SourceSystem: evidenceingestion.SourceSystemExternalDocument, UseState: state}
			for _, out := range []GetEvidenceRecordResponse{
				mapProposalResult(origin),
				mapCanonicalResult(evidenceingestion.CanonicalQueryResult{OriginProposal: origin, UseState: state}),
			} {
				if out.UseState != state || out.Source.ExternalSource == nil || out.Source.ExternalSource.AssessmentProjection != version {
					t.Fatalf("%s readback mislabeled its native %s projection", out.RecordRef.Kind, version)
				}
			}
		})
	}
	origin := evidenceingestion.ProposalQueryResult{SourceSystem: evidenceingestion.SourceSystemExternalDocument}
	for _, out := range []GetEvidenceRecordResponse{mapProposalResult(origin), mapCanonicalResult(evidenceingestion.CanonicalQueryResult{OriginProposal: origin})} {
		if out.Source.ExternalSource.AssessmentProjection != "not_projected" {
			t.Fatal("absent assessment was advertised as projected")
		}
	}
}

func TestGovernanceToolsAcceptAtMost64EvidenceIDs(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("canon-node:synthetic-%03d", i)
		}
		return out
	}
	for _, tool := range GovernanceTools() {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		var schema map[string]any
		switch tool.Name {
		case ToolGetEvidenceUseState:
			schema, _ = props["canonical_ids"].(map[string]any)
		case ToolCheckCurrentPlan:
			actions, _ := props["actions"].(map[string]any)
			item, _ := actions["items"].(map[string]any)
			itemProps, _ := item["properties"].(map[string]any)
			schema, _ = itemProps["basis_ids"].(map[string]any)
		default:
			continue
		}
		if schema["maxItems"] != 64 {
			t.Fatalf("%s ID schema maxItems = %#v, want 64", tool.Name, schema["maxItems"])
		}
	}
	// The server has no records pool: an accepted count fails later on the pool,
	// a rejected count fails on the limit first.
	s := newServer(&fakeQueryCore{})
	for _, n := range []int{64, 65} {
		requests := map[string]map[string]any{
			ToolGetEvidenceUseState: {"policy": string(evidenceuse.CurrentActionV1), "canonical_ids": ids(n)},
			ToolCheckCurrentPlan:    {"policy": string(evidenceuse.CurrentActionV1), "actions": []any{map[string]any{"action_id": "act", "basis_ids": ids(n)}}},
		}
		for name, req := range requests {
			payload, _ := json.Marshal(req)
			_, err := s.CallTool(context.Background(), name, payload)
			limited := err != nil && strings.Contains(err.Error(), "1..64")
			if (n == 64) == limited {
				t.Fatalf("%s with %d IDs: err = %v", name, n, err)
			}
		}
	}
}
