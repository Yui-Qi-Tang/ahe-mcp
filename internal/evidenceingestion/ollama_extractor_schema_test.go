package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestOllamaExtractorWireSchema(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(fmt.Sprintf("bounded=%t", bounded), func(t *testing.T) {
			var schema *jsonschema.Resolved
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var request struct {
					Format json.RawMessage `json:"format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				var format jsonschema.Schema
				if err := json.Unmarshal(request.Format, &format); err != nil {
					t.Fatal(err)
				}
				var err error
				schema, err = format.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				return jsonHTTPResponse(ollamaExtractorGenerateResponse{Response: `{"proposals":[]}`}), nil
			})}
			config := OllamaExtractorConfig{Model: "synthetic-model", HTTPClient: client}
			if bounded {
				config.PromptMode = OllamaExtractorPromptBoundedExactQuote
				config.MaxProposals = 1
			}
			runner, err := NewOllamaExtractorRunner(config)
			if err != nil {
				t.Fatal(err)
			}
			input := testOllamaExtractorInput(t)
			// Reuse the runner to detect accidentally cached span enums, including IDs
			// that do not resemble the example ID printed in the prompt.
			for _, spanID := range []string{"span:S1", "document:section-42"} {
				input.Spans = []ExtractorInputSpan{{SpanID: spanID, Text: "Synthetic source statement."}}
				if _, err := runner.Run(context.Background(), input); err != nil {
					t.Fatal(err)
				}
				proposal := fmt.Sprintf(`{"proposal_local_id":"p1","statement_text":"Synthetic source statement.","evidence_refs":[%q]}`, spanID)
				cases := []struct {
					name, output string
					valid        bool
				}{
					{"abstain", `{"proposals":[]}`, true},
					{"grounded shape", `{"proposals":[` + proposal + `]}`, true},
					{"count bound", `{"proposals":[` + proposal + `,` + proposal + `]}`, !bounded},
					{"span shaped regression", `{"text":"Synthetic source statement.","start_byte":0,"end_byte":27,"display_line":1,"span_id":"span:S1"}`, false},
					{"missing root", `{}`, false},
					{"extra root", `{"proposals":[],"canonical_id":"x"}`, false},
					{"null proposals", `{"proposals":null}`, false},
					{"missing field", `{"proposals":[{"proposal_local_id":"p1","statement_text":"x"}]}`, false},
					{"extra proposal field", `{"proposals":[{"proposal_local_id":"p1","statement_text":"x","evidence_refs":["` + spanID + `"],"canonical_id":"x"}]}`, false},
					{"unknown ref", `{"proposals":[{"proposal_local_id":"p1","statement_text":"x","evidence_refs":["missing"]}]}`, false},
					{"empty refs", `{"proposals":[{"proposal_local_id":"p1","statement_text":"x","evidence_refs":[]}]}`, false},
					{"empty text", `{"proposals":[{"proposal_local_id":"p1","statement_text":"","evidence_refs":["` + spanID + `"]}]}`, false},
					{"empty id", `{"proposals":[{"proposal_local_id":"","statement_text":"x","evidence_refs":["` + spanID + `"]}]}`, false},
				}
				for _, tc := range cases {
					var value any
					if err := json.Unmarshal([]byte(tc.output), &value); err != nil {
						t.Fatal(err)
					}
					if err := schema.Validate(value); (err == nil) != tc.valid {
						t.Errorf("%s/%s: valid=%t, error=%v", spanID, tc.name, tc.valid, err)
					}
				}
				if spanID != "span:S1" {
					stale := map[string]any{"proposals": []any{map[string]any{
						"proposal_local_id": "p1", "statement_text": "Synthetic source statement.", "evidence_refs": []any{"span:S1"},
					}}}
					if err := schema.Validate(stale); err == nil {
						t.Fatal("schema accepted a span from the previous run")
					}
				}
				// A schema-valid paraphrase still fails the authoritative quote check.
				paraphrase := []byte(fmt.Sprintf(`{"proposals":[{"proposal_local_id":"p1","statement_text":"Unsupported paraphrase.","evidence_refs":[%q]}]}`, spanID))
				var value any
				if err := json.Unmarshal(paraphrase, &value); err != nil {
					t.Fatal(err)
				}
				if err := schema.Validate(value); err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeBoundedExactQuoteExtractorOutput(paraphrase, input, 1); err == nil {
					t.Fatal("quote validator accepted paraphrase")
				}
			}
			input.Spans = nil
			if _, err := runner.Run(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(map[string]any{"proposals": []any{}}); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(map[string]any{"proposals": []any{map[string]any{"proposal_local_id": "p1", "statement_text": "x", "evidence_refs": []any{"span:S1"}}}}); err == nil {
				t.Fatal("empty catalogue accepted proposal")
			}
		})
	}
}

func TestOllamaExtractorRejectsInvalidSpanCatalogueBeforeHTTP(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached provider")
		return nil, nil
	})}
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{Model: "synthetic-model", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	for _, spans := range [][]ExtractorInputSpan{
		{{SpanID: ""}}, {{SpanID: "duplicate"}, {SpanID: "duplicate"}},
	} {
		if _, err := runner.Run(context.Background(), ExtractorInput{Spans: spans}); err == nil {
			t.Fatal("invalid catalogue accepted")
		}
	}
}
