package evidenceingestion

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

func buildManualSource(input ManualTextInput) (SourceSnapshot, ExtractionView, []SpanEntry, error) {
	if input.SourceID == "" {
		return SourceSnapshot{}, ExtractionView{}, nil, newDomainError(ErrorInvalidInput, "source_id is required")
	}
	if input.SourceVersion == "" {
		return SourceSnapshot{}, ExtractionView{}, nil, newDomainError(ErrorInvalidInput, "source_version is required")
	}
	contract, err := renderingContract(input.SourceSystem)
	if err != nil {
		return SourceSnapshot{}, ExtractionView{}, nil, err
	}
	if !utf8.Valid(input.Raw) {
		return SourceSnapshot{}, ExtractionView{}, nil, newDomainError(ErrorInvalidUTF8, "%s source is not valid UTF-8", contract.SourceSystem)
	}

	rawHash := contentHash(input.Raw)
	snapshotID, err := sourceSnapshotID(contract.SourceSystem, input.SourceID, input.SourceVersion, rawHash)
	if err != nil {
		return SourceSnapshot{}, ExtractionView{}, nil, err
	}
	snapshot := SourceSnapshot{
		ID:             snapshotID,
		SourceSystem:   contract.SourceSystem,
		SourceID:       input.SourceID,
		SourceVersion:  input.SourceVersion,
		RawContentHash: rawHash,
		OriginMetadata: cloneStringMap(input.OriginMetadata),
	}

	rendered := append([]byte(nil), input.Raw...)
	renderedHash := contentHash(rendered)
	viewID, err := stableID("view:", "extraction_view", struct {
		SourceSnapshotID    string `json:"source_snapshot_id"`
		RendererName        string `json:"renderer_name"`
		RendererVersion     string `json:"renderer_version"`
		RenderedContentHash string `json:"rendered_content_hash"`
	}{
		SourceSnapshotID:    snapshot.ID,
		RendererName:        contract.RendererName,
		RendererVersion:     contract.RendererVersion,
		RenderedContentHash: renderedHash,
	})
	if err != nil {
		return SourceSnapshot{}, ExtractionView{}, nil, err
	}
	view := ExtractionView{
		ID:                  viewID,
		SourceSnapshotID:    snapshot.ID,
		RendererName:        contract.RendererName,
		RendererVersion:     contract.RendererVersion,
		Rendered:            rendered,
		RenderedContentHash: renderedHash,
	}

	spans, err := buildLineSpanCatalog(view, contract.SpanCatalogVersion)
	if err != nil {
		return SourceSnapshot{}, ExtractionView{}, nil, err
	}
	return snapshot, view, spans, nil
}

func sourceSnapshotID(sourceSystem, sourceID, sourceVersion, rawHash string) (string, error) {
	if sourceSystem == SourceSystemExternalDocument {
		return stableID("srcsnap:", "external_source_snapshot", struct {
			SourceSystem  string `json:"source_system"`
			SourceID      string `json:"source_id"`
			SourceVersion string `json:"source_version"`
		}{
			SourceSystem:  sourceSystem,
			SourceID:      sourceID,
			SourceVersion: sourceVersion,
		})
	}
	return stableID("srcsnap:", "source_snapshot", struct {
		SourceSystem   string `json:"source_system"`
		SourceID       string `json:"source_id"`
		SourceVersion  string `json:"source_version"`
		RawContentHash string `json:"raw_content_hash"`
	}{
		SourceSystem:   sourceSystem,
		SourceID:       sourceID,
		SourceVersion:  sourceVersion,
		RawContentHash: rawHash,
	})
}

func buildLineSpanCatalog(view ExtractionView, spanCatalogVersion string) ([]SpanEntry, error) {
	if !utf8.Valid(view.Rendered) {
		return nil, newDomainError(ErrorInvalidUTF8, "rendered view is not valid UTF-8")
	}
	var spans []SpanEntry
	lineStart := 0
	displayLine := 1
	spanNumber := 1
	for lineStart <= len(view.Rendered) {
		newline := bytes.IndexByte(view.Rendered[lineStart:], '\n')
		lineEnd := len(view.Rendered)
		nextLineStart := len(view.Rendered) + 1
		if newline >= 0 {
			lineEnd = lineStart + newline
			nextLineStart = lineEnd + 1
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && view.Rendered[contentEnd-1] == '\r' {
			contentEnd--
		}
		if contentEnd > lineStart {
			quoted := append([]byte(nil), view.Rendered[lineStart:contentEnd]...)
			spans = append(spans, SpanEntry{
				ExtractionViewID:   view.ID,
				SpanID:             fmt.Sprintf("span:S%d", spanNumber),
				SpanCatalogVersion: spanCatalogVersion,
				StartByte:          lineStart,
				EndByte:            contentEnd,
				DisplayLine:        displayLine,
				QuotedTextHash:     contentHash(quoted),
				QuotedText:         string(quoted),
			})
			spanNumber++
		}
		if newline < 0 {
			break
		}
		lineStart = nextLineStart
		displayLine++
	}
	return spans, nil
}

type sourceRenderingContract struct {
	SourceSystem       string
	RendererName       string
	RendererVersion    string
	SpanCatalogVersion string
}

func renderingContract(sourceSystem string) (sourceRenderingContract, error) {
	switch sourceSystem {
	case "", SourceSystemManualText:
		return sourceRenderingContract{
			SourceSystem:       SourceSystemManualText,
			RendererName:       RendererManualTextIdentity,
			RendererVersion:    RendererManualTextIdentityVersion,
			SpanCatalogVersion: SpanCatalogManualLineV1,
		}, nil
	case SourceSystemCodeFile:
		return sourceRenderingContract{
			SourceSystem:       SourceSystemCodeFile,
			RendererName:       RendererCodeIdentity,
			RendererVersion:    RendererCodeIdentityVersion,
			SpanCatalogVersion: SpanCatalogCodeLineV1,
		}, nil
	case SourceSystemConnectorText:
		return sourceRenderingContract{
			SourceSystem:       SourceSystemConnectorText,
			RendererName:       RendererConnectorTextIdentity,
			RendererVersion:    RendererConnectorTextIdentityVersion,
			SpanCatalogVersion: SpanCatalogConnectorTextLineV1,
		}, nil
	case SourceSystemMCPReadDocument:
		return sourceRenderingContract{
			SourceSystem:       SourceSystemMCPReadDocument,
			RendererName:       RendererMCPReadDocumentIdentity,
			RendererVersion:    RendererMCPReadDocumentIdentityVersion,
			SpanCatalogVersion: SpanCatalogMCPReadDocumentLineV1,
		}, nil
	case SourceSystemExternalDocument:
		return sourceRenderingContract{
			SourceSystem:       SourceSystemExternalDocument,
			RendererName:       RendererExternalDocumentIdentity,
			RendererVersion:    RendererExternalDocumentIdentityVersion,
			SpanCatalogVersion: SpanCatalogExternalDocumentLineV1,
		}, nil
	default:
		return sourceRenderingContract{}, newDomainError(ErrorInvalidInput, "source system %q is not supported", sourceSystem)
	}
}

func validateSpanBounds(view ExtractionView, span SpanEntry) error {
	if span.ExtractionViewID != view.ID {
		return newDomainError(ErrorUnknownSpan, "span %s belongs to view %s, want %s", span.SpanID, span.ExtractionViewID, view.ID)
	}
	if span.StartByte < 0 || span.EndByte < span.StartByte || span.EndByte > len(view.Rendered) {
		return newDomainError(ErrorSpanOutOfBounds, "span %s [%d,%d) outside rendered bytes length %d", span.SpanID, span.StartByte, span.EndByte, len(view.Rendered))
	}
	quoted := view.Rendered[span.StartByte:span.EndByte]
	if got := contentHash(quoted); got != span.QuotedTextHash {
		return newDomainError(ErrorQuotedHashMismatch, "span %s quoted hash %s, want %s", span.SpanID, span.QuotedTextHash, got)
	}
	if string(quoted) != span.QuotedText {
		return newDomainError(ErrorQuotedHashMismatch, "span %s quoted text does not match rendered bytes", span.SpanID)
	}
	return nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
