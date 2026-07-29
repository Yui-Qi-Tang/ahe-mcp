package evidenceingestion

import "testing"

func TestBuildConnectorTextSourceUsesDistinctAuthorityContract(t *testing.T) {
	raw := []byte("first connector line\n\nsecond connector line\r\n")
	snapshot, view, spans, err := buildManualSource(ManualTextInput{
		SourceSystem:  SourceSystemConnectorText,
		SourceID:      "connector-text-source:fixture",
		SourceVersion: "sha256:fixture",
		Raw:           raw,
		OriginMetadata: map[string]string{
			"connector_delivery_id": "detective-connector-delivery:fixture",
		},
	})
	if err != nil {
		t.Fatalf("buildManualSource(connector_text) error = %v", err)
	}
	if snapshot.SourceSystem != SourceSystemConnectorText || snapshot.SourceID != "connector-text-source:fixture" || snapshot.RawContentHash != contentHash(raw) {
		t.Fatalf("connector text snapshot = %+v", snapshot)
	}
	if view.RendererName != RendererConnectorTextIdentity ||
		view.RendererVersion != RendererConnectorTextIdentityVersion ||
		view.RenderedContentHash != contentHash(raw) {
		t.Fatalf("connector text view = %+v", view)
	}
	if len(spans) != 2 ||
		spans[0].SpanCatalogVersion != SpanCatalogConnectorTextLineV1 ||
		spans[0].QuotedText != "first connector line" ||
		spans[1].QuotedText != "second connector line" {
		t.Fatalf("connector text spans = %+v", spans)
	}
}
