package evidenceingestion

import "testing"

func TestBuildMCPReadDocumentSourceUsesDistinctAuthorityContract(t *testing.T) {
	raw := []byte("Refunds must be completed within 7 days.\n")
	snapshot, view, spans, err := buildManualSource(ManualTextInput{
		SourceSystem:  SourceSystemMCPReadDocument,
		SourceID:      "mcp-read-source:fixture",
		SourceVersion: "revision-42",
		Raw:           raw,
		OriginMetadata: map[string]string{
			"mcp_provider":  "fixture",
			"mcp_object_id": "AHE-42",
		},
	})
	if err != nil {
		t.Fatalf("buildManualSource(mcp_read_document) error = %v", err)
	}
	if snapshot.SourceSystem != SourceSystemMCPReadDocument ||
		snapshot.SourceID != "mcp-read-source:fixture" ||
		snapshot.RawContentHash != contentHash(raw) {
		t.Fatalf("mcp read snapshot = %+v", snapshot)
	}
	if view.RendererName != RendererMCPReadDocumentIdentity ||
		view.RendererVersion != RendererMCPReadDocumentIdentityVersion ||
		view.RenderedContentHash != contentHash(raw) {
		t.Fatalf("mcp read view = %+v", view)
	}
	if len(spans) != 1 ||
		spans[0].SpanCatalogVersion != SpanCatalogMCPReadDocumentLineV1 ||
		spans[0].QuotedText != "Refunds must be completed within 7 days." {
		t.Fatalf("mcp read spans = %+v", spans)
	}
}
