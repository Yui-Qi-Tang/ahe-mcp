package evidencequerymcp

import (
	"context"
	"encoding/json"
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
