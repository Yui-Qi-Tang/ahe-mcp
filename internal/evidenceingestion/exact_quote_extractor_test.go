package evidenceingestion

import "testing"

func TestDecodeBoundedExactQuoteExtractorOutput(t *testing.T) {
	input := ExtractorInput{Spans: []ExtractorInputSpan{{
		SpanID: "span:selected",
		Text:   "Refunds must be completed within 7 days. Contact support for exceptions.",
	}}}

	output, err := DecodeBoundedExactQuoteExtractorOutput([]byte(
		`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:selected"]}]}`,
	), input, 2)
	if err != nil {
		t.Fatalf("DecodeBoundedExactQuoteExtractorOutput() error = %v", err)
	}
	if len(output.Proposals) != 1 {
		t.Fatalf("proposal count = %d, want 1", len(output.Proposals))
	}

	abstained, err := DecodeBoundedExactQuoteExtractorOutput([]byte(`{"proposals":[]}`), input, 2)
	if err != nil {
		t.Fatalf("abstention error = %v", err)
	}
	if len(abstained.Proposals) != 0 {
		t.Fatalf("abstention proposal count = %d, want 0", len(abstained.Proposals))
	}
}

func TestDecodeBoundedExactQuoteExtractorOutputRejectsUngroundedMaterial(t *testing.T) {
	input := ExtractorInput{Spans: []ExtractorInputSpan{{
		SpanID: "span:selected",
		Text:   "Refunds must be completed within 7 days.",
	}}}
	cases := []struct {
		name string
		data string
		kind ErrorKind
	}{
		{
			name: "paraphrase",
			data: `{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds take one week.","evidence_refs":["span:selected"]}]}`,
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "unknown-span",
			data: `{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:other"]}]}`,
			kind: ErrorUnknownSpan,
		},
		{
			name: "too-many",
			data: `{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds","evidence_refs":["span:selected"]},{"proposal_local_id":"stmt-2","statement_text":"7 days","evidence_refs":["span:selected"]}]}`,
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "model-supplied-section-coverage",
			data: `{"proposals":[],"document_section_coverage":{"schema_version":"invented"}}`,
			kind: ErrorInvalidExtractorOutput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeBoundedExactQuoteExtractorOutput([]byte(tc.data), input, 1)
			assertKind(t, err, tc.kind)
		})
	}
}
