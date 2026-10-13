package evidencequerymcp

import (
	"encoding/json"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestExternalDeclarationProjectionDoesNotInventVerification(t *testing.T) {
	for _, fidelity := range []string{"verbatim", "agent_condensed", ""} {
		t.Run(fidelity, func(t *testing.T) {
			original := evidenceingestion.ProposalQueryResult{
				SourceSystem:     evidenceingestion.SourceSystemExternalDocument,
				SourceSnapshotID: "srcsnap:historical", RawContentHash: "sha256:retained",
				OriginMetadata: map[string]string{
					"external_content_fidelity": fidelity, "external_coverage": "exact_excerpt",
					"external_object_id": "unknown", "external_limitations_json": `["retained legacy declaration"]`,
					"declaration_authority": "independently_verified", "assessment_projection": "all_passed",
				},
			}
			before, _ := json.Marshal(original)
			source := mapSourceInfo(original)
			if source.ExternalSource.ContentFidelity != fidelity || source.ExternalSource.Coverage != "exact_excerpt" || source.SourceSnapshotID != original.SourceSnapshotID || source.RawContentHash != original.RawContentHash {
				t.Fatal("original declarations changed", source)
			}
			raw, err := json.Marshal(source.ExternalSource)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["declaration_authority"] != "original_intake_declaration" || payload["assessment_projection"] != "not_projected" {
				t.Fatal(payload)
			}
			if after, _ := json.Marshal(original); string(before) != string(after) {
				t.Fatal("source was rewritten")
			}
		})
	}
}
