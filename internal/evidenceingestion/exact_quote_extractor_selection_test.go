package evidenceingestion

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestWholeSpanSelectionPreservesCompleteUnits(t *testing.T) {
	for _, source := range []string{
		"Requests must have a request ID.\n\nUnless recovery is approved, failed jobs remain pending.",
		"# Limits\r\n\r\n| Mode | Limit |\r\n| --- | --- |\r\n| Online | 30 seconds |\r\n| Approved offline | 60 seconds |\r\n",
		"  逾時上限為 30 秒；\n例外：核准的離線工作可延長至 60 秒。\n",
	} {
		input := ExtractorInput{Spans: []ExtractorInputSpan{{SpanID: "span:unit", Text: source}}}
		units, err := wholeSpanExactQuoteUnits(input)
		if err != nil || len(units) != 1 || units[0].StatementText != source || input.Spans[0].Text != source {
			t.Fatalf("catalogue changed source: units=%+v, error=%v", units, err)
		}
		data := selectionOutputJSON(t, ExtractorProposalOutput{
			ProposalLocalID: "p1", StatementText: source, EvidenceRefs: []string{"span:unit"},
		})
		decoded, err := DecodeWholeSpanExactQuoteSelectionOutput(data, input, 1)
		if err != nil || len(decoded.Proposals) != 1 || decoded.Proposals[0].StatementText != source {
			t.Fatalf("complete unit changed: output=%+v, error=%v", decoded, err)
		}
	}
}

func TestWholeSpanSelectionRejectsModelFragmentsAndInvalidChoices(t *testing.T) {
	source := "Requests require approval.\n\nUnless explicitly exempted, all requests remain pending."
	input := ExtractorInput{Spans: []ExtractorInputSpan{
		{SpanID: "span:a", Text: source},
		{SpanID: "span:b", Text: "Requests require approval."},
	}}
	proposal := ExtractorProposalOutput{ProposalLocalID: "p1", StatementText: source, EvidenceRefs: []string{"span:a"}}
	tests := []struct {
		name string
		data string
	}{
		{"substring drops exception", `{"proposals":[{"proposal_local_id":"p1","statement_text":"Requests require approval.","evidence_refs":["span:a"]}]}`},
		{"paraphrase", `{"proposals":[{"proposal_local_id":"p1","statement_text":"Approval is necessary.","evidence_refs":["span:a"]}]}`},
		{"unknown span", `{"proposals":[{"proposal_local_id":"p1","statement_text":"Requests require approval.","evidence_refs":["span:missing"]}]}`},
		{"multiple refs", `{"proposals":[{"proposal_local_id":"p1","statement_text":"Requests require approval.","evidence_refs":["span:a","span:b"]}]}`},
		{"missing proposals", `{}`},
		{"null proposals", `{"proposals":null}`},
		{"unknown field", `{"proposals":[],"coverage_complete":true}`},
		{"too many", string(selectionOutputJSON(t, proposal, proposal, proposal))},
		{"duplicate unit", string(selectionOutputJSON(t, proposal, ExtractorProposalOutput{
			ProposalLocalID: "p2", StatementText: source, EvidenceRefs: []string{"span:a"},
		}))},
		{"wrong valid ref", string(selectionOutputJSON(t, ExtractorProposalOutput{
			ProposalLocalID: "p1", StatementText: source, EvidenceRefs: []string{"span:b"},
		}))},
		{"duplicate local ID", string(selectionOutputJSON(t, proposal, ExtractorProposalOutput{
			ProposalLocalID: "p1", StatementText: input.Spans[1].Text, EvidenceRefs: []string{"span:b"},
		}))},
		{"empty local ID", string(selectionOutputJSON(t, ExtractorProposalOutput{
			StatementText: source, EvidenceRefs: []string{"span:a"},
		}))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeWholeSpanExactQuoteSelectionOutput([]byte(test.data), input, 2); err == nil {
				t.Fatal("invalid model selection accepted without provider schema enforcement")
			}
		})
	}
	if _, err := DecodeWholeSpanExactQuoteSelectionOutput([]byte(`{"proposals":[]}`), input, 2); err != nil {
		t.Fatalf("abstention rejected: %v", err)
	}
}

func TestWholeSpanSelectionWireSchemaBindsFreshUnitsAndPreservesRawResponse(t *testing.T) {
	var schema *jsonschema.Resolved
	var prompt string
	rawResponse := "  {\"proposals\":[]} \n"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body ollamaExtractorGenerateRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		prompt = body.Prompt
		data, err := json.Marshal(body.Format)
		if err != nil {
			t.Fatal(err)
		}
		var format jsonschema.Schema
		if err := json.Unmarshal(data, &format); err != nil {
			t.Fatal(err)
		}
		schema, err = format.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return jsonHTTPResponse(ollamaExtractorGenerateResponse{Response: rawResponse, Done: true, DoneReason: "stop"}), nil
	})}
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		Model: "synthetic-selector", HTTPClient: client,
		PromptMode: OllamaExtractorPromptWholeSpanExactQuote, MaxProposals: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"If approved, retry.\nOtherwise remain pending.", "| Mode | Limit |\n| Online | 30s |\n| Offline | 60s |"} {
		input := ExtractorInput{Spans: []ExtractorInputSpan{
			{SpanID: "span:a", Text: source}, {SpanID: "span:b", Text: "Unselected source unit."},
		}}
		output, err := runner.Run(context.Background(), input)
		if err != nil || string(output) != rawResponse {
			t.Fatalf("raw response changed: %q, error=%v", output, err)
		}
		for _, required := range []string{"Select complete source units", "Select at most 1 units", "do not summarize", "Unselected units and limits are omissions"} {
			if !strings.Contains(prompt, required) {
				t.Errorf("selection prompt omits %q", required)
			}
		}
		for _, test := range []struct {
			text, ref string
			valid     bool
		}{
			{source, "span:a", true},
			{strings.Split(source, "\n")[0], "span:a", false},
			{source, "span:b", false},
			{source, "span:missing", false},
			{"Unselected source unit.", "span:b", true},
		} {
			data := selectionOutputJSON(t, ExtractorProposalOutput{ProposalLocalID: "p1", StatementText: test.text, EvidenceRefs: []string{test.ref}})
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != test.valid {
				t.Errorf("schema validity=%t for %q/%s: %v", test.valid, test.text, test.ref, err)
			}
		}
		// Malformed or schema-violating model output stays raw for controller audit.
		rawResponse = "```json\n{\"proposals\":[]}\n```"
	}
	definition := runner.ExtractorDefinition()
	if definition.Config["output_contract"] != OllamaExtractorPromptWholeSpanExactQuote || definition.Config["prompt_version"] != ollamaWholeSpanPromptVersion || definition.Config["max_unit_bytes"] != "65536" || definition.Config["selection_unit"] != "whole_current_span" || definition.Config["completion_contract"] != "done-true-stop-v1" {
		t.Fatalf("selection provenance = %+v", definition)
	}
}

func TestWholeSpanSelectionRejectsOversizedOrInvalidUnitsBeforeHTTP(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid whole unit reached provider")
		return nil, nil
	})}
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		Model: "synthetic-selector", HTTPClient: client,
		PromptMode: OllamaExtractorPromptWholeSpanExactQuote, MaxProposals: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, spans := range [][]ExtractorInputSpan{
		{{SpanID: "span:huge", Text: strings.Repeat("界", maxExactQuoteStatementBytes/3+1)}},
		{{SpanID: "span:empty", Text: " \n"}},
		{{SpanID: "duplicate", Text: "A"}, {SpanID: "duplicate", Text: "B"}},
	} {
		input := ExtractorInput{Spans: spans}
		if _, err := runner.Run(context.Background(), input); err == nil {
			t.Fatal("invalid unit was accepted, truncated, or silently omitted")
		}
		if _, err := DecodeWholeSpanExactQuoteSelectionOutput([]byte(`{"proposals":[]}`), input, 1); err == nil {
			t.Fatal("controller accepted invalid unit catalogue")
		}
	}
}

func TestWholeSpanSelectionRequiresProviderCompletion(t *testing.T) {
	input := ExtractorInput{Spans: []ExtractorInputSpan{{SpanID: "span:a", Text: "Retry only after approval."}}}
	completeUnit := string(selectionOutputJSON(t, ExtractorProposalOutput{
		ProposalLocalID: "p1", StatementText: input.Spans[0].Text, EvidenceRefs: []string{"span:a"},
	}))
	for _, response := range []string{"  {\"proposals\":[]}\n", " \n" + completeUnit + "\n"} {
		for _, test := range []struct {
			name     string
			metadata map[string]any
			valid    bool
		}{
			{"completed", map[string]any{"done": true, "done_reason": "stop"}, true},
			{"not done", map[string]any{"done": false, "done_reason": "stop"}, false},
			{"length", map[string]any{"done": true, "done_reason": "length"}, false},
			{"missing done", map[string]any{"done_reason": "stop"}, false},
			{"missing reason", map[string]any{"done": true}, false},
			{"empty reason", map[string]any{"done": true, "done_reason": ""}, false},
			{"missing both", nil, false},
			{"load only", map[string]any{"done": true, "done_reason": "load"}, false},
			{"untrusted reason", map[string]any{"done": true, "done_reason": "provider-private-payload"}, false},
		} {
			for _, mode := range []string{"", OllamaExtractorPromptBoundedExactQuote, OllamaExtractorPromptWholeSpanExactQuote} {
				t.Run(test.name+"/"+mode+"/"+response, func(t *testing.T) {
					client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						body := map[string]any{"response": response}
						for key, value := range test.metadata {
							body[key] = value
						}
						return jsonHTTPResponse(body), nil
					})}
					config := OllamaExtractorConfig{Model: "synthetic-selector", HTTPClient: client, PromptMode: mode}
					if mode != "" {
						config.MaxProposals = 1
					}
					runner, err := NewOllamaExtractorRunner(config)
					if err != nil {
						t.Fatal(err)
					}
					output, err := runner.Run(context.Background(), input)
					wantSuccess := mode != OllamaExtractorPromptWholeSpanExactQuote || test.valid
					if (err == nil) != wantSuccess {
						t.Fatalf("provider completion success=%t, error=%v", wantSuccess, err)
					}
					if string(output) != response {
						t.Fatalf("provider completion check changed raw response: %q", output)
					}
					if err != nil && err.Error() != "ollama selector requires done=true and done_reason=stop" {
						t.Fatalf("completion failure lacks required metadata contract: %v", err)
					}
					// These are valid complete-unit/abstention envelopes, so JSON or
					// quote validity alone must not establish provider completion.
					if _, err := DecodeWholeSpanExactQuoteSelectionOutput(output, input, 1); err != nil {
						t.Fatalf("test requires independently valid selection JSON: %v", err)
					}
				})
			}
		}
	}
}

func selectionOutputJSON(t *testing.T, proposals ...ExtractorProposalOutput) []byte {
	t.Helper()
	data, err := json.Marshal(FrozenExtractorOutput{Proposals: proposals})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
