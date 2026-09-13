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

func ollamaWholeSpanSelectionSchema(input ExtractorInput, maxProposals int) (map[string]any, error) {
	units, err := wholeSpanExactQuoteUnits(input)
	if err != nil {
		return nil, err
	}
	schema, err := ollamaExtractorOutputSchema(input, maxProposals)
	if err != nil {
		return nil, err
	}
	if len(units) == 0 {
		return schema, nil
	}
	choices := make([]any, 0, len(units))
	for _, unit := range units {
		choices = append(choices, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"proposal_local_id", "statement_text", "evidence_refs"},
			"properties": map[string]any{
				"proposal_local_id": map[string]any{"type": "string", "minLength": 1},
				"statement_text":    map[string]any{"type": "string", "enum": []string{unit.StatementText}},
				"evidence_refs": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 1,
					"items": map[string]any{"type": "string", "enum": unit.EvidenceRefs},
				},
			},
		})
	}
	proposals := schema["properties"].(map[string]any)["proposals"].(map[string]any)
	proposals["items"] = map[string]any{"oneOf": choices}
	return schema, nil
}
