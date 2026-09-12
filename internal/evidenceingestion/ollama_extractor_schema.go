package evidenceingestion

// ollamaExtractorOutputSchema constrains shape and reference selection only.
// Trusted decoding and grounding checks remain authoritative after generation.
func ollamaExtractorOutputSchema(input ExtractorInput, maxProposals int) (map[string]any, error) {
	spanIDs := make([]string, 0, len(input.Spans))
	seen := make(map[string]bool, len(input.Spans))
	for _, span := range input.Spans {
		if span.SpanID == "" || seen[span.SpanID] {
			return nil, newDomainError(ErrorInvalidInput, "extractor input requires non-empty, unique span IDs")
		}
		seen[span.SpanID] = true
		spanIDs = append(spanIDs, span.SpanID)
	}
	reference := map[string]any{"type": "string"}
	if len(spanIDs) > 0 {
		reference["enum"] = spanIDs
	}
	proposals := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"proposal_local_id", "statement_text", "evidence_refs"},
			"properties": map[string]any{
				"proposal_local_id": map[string]any{"type": "string", "minLength": 1},
				"statement_text":    map[string]any{"type": "string", "minLength": 1},
				"evidence_refs": map[string]any{
					"type": "array", "minItems": 1, "items": reference,
				},
			},
		},
	}
	if len(spanIDs) == 0 {
		// An empty catalogue can only abstain; avoid an invalid empty enum.
		proposals["maxItems"] = 0
	} else if maxProposals > 0 {
		proposals["maxItems"] = maxProposals
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"proposals"},
		"properties":           map[string]any{"proposals": proposals},
	}, nil
}
