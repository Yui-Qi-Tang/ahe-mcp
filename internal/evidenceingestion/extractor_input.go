package evidenceingestion

func (ctx manualSourceContext) extractorInput() ExtractorInput {
	spans := make([]ExtractorInputSpan, 0, len(ctx.Spans))
	for _, span := range ctx.Spans {
		spans = append(spans, ExtractorInputSpan{
			SpanID:         span.SpanID,
			Text:           span.QuotedText,
			StartByte:      span.StartByte,
			EndByte:        span.EndByte,
			DisplayLine:    span.DisplayLine,
			QuotedTextHash: span.QuotedTextHash,
		})
	}
	return ExtractorInput{
		SourceSnapshotID:    ctx.SourceSnapshot.ID,
		ExtractionViewID:    ctx.ExtractionView.ID,
		SourceSystem:        ctx.SourceSnapshot.SourceSystem,
		SourceID:            ctx.SourceSnapshot.SourceID,
		SourceVersion:       ctx.SourceSnapshot.SourceVersion,
		RawContentHash:      ctx.SourceSnapshot.RawContentHash,
		Renderer:            RendererRef{Name: ctx.ExtractionView.RendererName, Version: ctx.ExtractionView.RendererVersion},
		RenderedContentHash: ctx.ExtractionView.RenderedContentHash,
		RenderedText:        string(ctx.ExtractionView.Rendered),
		SpanCatalogVersion:  ctx.SpanCatalogVersion,
		Spans:               spans,
	}
}
