package evidenceingestion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOllamaExtractorRunnerCallsGenerateEndpoint(t *testing.T) {
	responseJSON := `{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"]}]}`
	var captured ollamaExtractorGenerateRequest
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.String() != "http://127.0.0.1:11434/api/generate" {
			t.Fatalf("url = %s, want loopback generate endpoint", r.URL.String())
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonHTTPResponse(ollamaExtractorGenerateResponse{Response: responseJSON}), nil
	})}

	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		BaseURL:    "http://127.0.0.1:11434",
		Model:      "gemma4:12b",
		NumPredict: 256,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	got, err := runner.Run(context.Background(), testOllamaExtractorInput(t))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if string(got) != responseJSON {
		t.Fatalf("response bytes = %q, want %q", string(got), responseJSON)
	}
	if _, err := decodeTrustedExtractorOutput(got); err != nil {
		t.Fatalf("provider response should satisfy strict output shape: %v", err)
	}
	if captured.Model != "gemma4:12b" {
		t.Fatalf("model = %q", captured.Model)
	}
	if captured.Stream {
		t.Fatalf("stream = true, want false")
	}
	if captured.Format != "json" {
		t.Fatalf("format = %#v, want json", captured.Format)
	}
	if captured.Think {
		t.Fatalf("think = true, want false")
	}
	if got := numericOption(t, captured.Options, "temperature"); got != 0 {
		t.Fatalf("temperature = %v, want 0", got)
	}
	if got := numericOption(t, captured.Options, "num_predict"); got != 256 {
		t.Fatalf("num_predict = %v, want 256", got)
	}
	if !strings.Contains(captured.Prompt, `"span_id":"span:S1"`) {
		t.Fatalf("prompt did not include grounded span ID: %s", captured.Prompt)
	}
	if !strings.Contains(captured.Prompt, "Refunds must be completed within 7 days.") {
		t.Fatalf("prompt did not include rendered source text: %s", captured.Prompt)
	}
	if strings.Contains(captured.Prompt, "canonical_ref") {
		t.Fatalf("prompt should not ask the model for canonical fields: %s", captured.Prompt)
	}

	definition := runner.ExtractorDefinition()
	if definition.Name != ExtractorOllamaLocal || definition.Version != ExtractorOllamaLocalVersion {
		t.Fatalf("definition = %+v, want ollama-local/v1", definition)
	}
	if definition.Config["model"] != "gemma4:12b" || definition.Config["prompt_version"] != ollamaExtractorPromptVersion {
		t.Fatalf("definition config = %+v", definition.Config)
	}
}

func TestOllamaExtractorRunnerNormalizesGenerateEndpoint(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.String() != "http://[::1]:11434/api/generate" {
			t.Fatalf("url = %s, want IPv6 loopback generate endpoint", r.URL.String())
		}
		return jsonHTTPResponse(ollamaExtractorGenerateResponse{Response: `{"proposals":[]}`}), nil
	})}

	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		BaseURL:    "http://[::1]:11434/api/generate",
		Model:      "gemma4:12b",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	if _, err := runner.Run(context.Background(), testOllamaExtractorInput(t)); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatalf("server was not called")
	}
}

func TestOllamaExtractorRunnerUsesBoundedExactQuotePrompt(t *testing.T) {
	var captured ollamaExtractorGenerateRequest
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return jsonHTTPResponse(ollamaExtractorGenerateResponse{Response: `{"proposals":[]}`}), nil
	})}
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		BaseURL:      "http://localhost:11434",
		Model:        "model-under-test",
		HTTPClient:   client,
		PromptMode:   OllamaExtractorPromptBoundedExactQuote,
		MaxProposals: 3,
	})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	if _, err := runner.Run(context.Background(), testOllamaExtractorInput(t)); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, required := range []string{
		"Return at most 3 proposals",
		`Return {"proposals":[]} when no objective statement is present`,
		"Copy statement_text exactly",
		"Do not answer questions",
	} {
		if !strings.Contains(captured.Prompt, required) {
			t.Fatalf("bounded prompt missing %q: %s", required, captured.Prompt)
		}
	}
	definition := runner.ExtractorDefinition()
	if definition.Config["prompt_version"] != ollamaExactQuotePromptVersion ||
		definition.Config["output_contract"] != OllamaExtractorPromptBoundedExactQuote ||
		definition.Config["max_proposals"] != "3" {
		t.Fatalf("bounded extractor definition = %+v", definition)
	}
}

func TestOllamaExtractorRunnerProviderErrors(t *testing.T) {
	input := testOllamaExtractorInput(t)
	cases := []struct {
		name      string
		status    int
		body      string
		wantError string
	}{
		{
			name:      "http-error",
			status:    http.StatusInternalServerError,
			body:      `provider unavailable`,
			wantError: "HTTP 500",
		},
		{
			name:      "provider-error-field",
			status:    http.StatusOK,
			body:      `{"error":"model missing"}`,
			wantError: "model missing",
		},
		{
			name:      "malformed-provider-json",
			status:    http.StatusOK,
			body:      `{`,
			wantError: "parsing ollama extractor response",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return textHTTPResponse(tc.status, tc.body), nil
			})}
			runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
				BaseURL:    "http://127.0.0.1:11434",
				Model:      "gemma4:12b",
				HTTPClient: client,
			})
			if err != nil {
				t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
			}
			_, err = runner.Run(context.Background(), input)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Run() error = %v, want containing %q", err, tc.wantError)
			}
		})
	}
}

func TestOllamaExtractorRunnerDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Body:       io.NopCloser(strings.NewReader("redirect rejected")),
			Header: http.Header{
				"Location": []string{"http://192.0.2.1/collect"},
			},
			Request: request,
		}, nil
	})}
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		BaseURL:    "http://127.0.0.1:11434",
		Model:      "gemma4:12b",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	_, err = runner.Run(context.Background(), testOllamaExtractorInput(t))
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("Run() error = %v, want bounded redirect rejection", err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want 1", calls)
	}
}

func TestOllamaExtractorRunnerConfigValidation(t *testing.T) {
	_, err := NewOllamaExtractorRunner(OllamaExtractorConfig{})
	assertKind(t, err, ErrorInvalidInput)

	for _, baseURL := range []string{
		"https://127.0.0.1:11434",
		"http://ollama.example.test:11434",
		"http://user@127.0.0.1:11434",
		"http://127.0.0.1:11434?token=secret",
		"http://127.0.0.1:11434#fragment",
		"http://127.0.0.1:11434/api/chat",
	} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
				BaseURL: baseURL,
				Model:   "gemma4:12b",
			})
			assertKind(t, err, ErrorInvalidInput)
		})
	}

	_, err = NewOllamaExtractorRunner(OllamaExtractorConfig{Model: "gemma4:12b", NumPredict: -1})
	assertKind(t, err, ErrorInvalidInput)

	_, err = NewOllamaExtractorRunner(OllamaExtractorConfig{
		Model: "gemma4:12b", PromptMode: OllamaExtractorPromptBoundedExactQuote,
	})
	assertKind(t, err, ErrorInvalidInput)
}

func TestMockSQLRunTrustedExtractorWithOllamaDefinition(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-ollama-runner-source"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	outputData := mustJSONBytes(t, testFixture())
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{Model: "gemma4:12b"})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:           "mock-ollama-runner",
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: runner.ExtractorDefinition(),
	}

	result, err := runTrustedExtractor(ctx, db, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return outputData, nil
	})
	if err != nil {
		t.Fatalf("runTrustedExtractor() error = %v", err)
	}
	got, err := traceProposalProvenance(ctx, db, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("traceProposalProvenance() error = %v", err)
	}
	if got.ExtractorName != ExtractorOllamaLocal || got.ExtractorVersion != ExtractorOllamaLocalVersion {
		t.Fatalf("extractor provenance = %s/%s, want ollama-local/v1", got.ExtractorName, got.ExtractorVersion)
	}
	if len(db.extractorDefinitions) != 1 {
		t.Fatalf("extractor definition count = %d, want 1", len(db.extractorDefinitions))
	}
}

func numericOption(t *testing.T, values map[string]any, key string) float64 {
	t.Helper()
	got, ok := values[key]
	if !ok {
		t.Fatalf("missing option %q in %+v", key, values)
	}
	switch value := got.(type) {
	case int:
		return float64(value)
	case float64:
		return value
	default:
		t.Fatalf("option %q type = %T, want numeric", key, got)
		return 0
	}
}

func testOllamaExtractorInput(t *testing.T) ExtractorInput {
	t.Helper()
	sourceCtx, err := buildManualSourceContext(testManualInput("ollama-extractor-input"))
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	return sourceCtx.extractorInput()
}

func TestOllamaExtractorRunnerLeavesInvalidModelJSONToStrictDecode(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(ollamaExtractorGenerateResponse{
			Response: "```json\n{\"proposals\":[]}\n```",
		}), nil
	})}
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		BaseURL:    "http://127.0.0.1:11434",
		Model:      "gemma4:12b",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	got, err := runner.Run(context.Background(), testOllamaExtractorInput(t))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(got) != "```json\n{\"proposals\":[]}\n```" {
		t.Fatalf("runner normalized invalid output = %q", string(got))
	}
	_, err = decodeTrustedExtractorOutput(got)
	assertKind(t, err, ErrorInvalidExtractorOutput)
	if definition := runner.ExtractorDefinition(); definition.Name != ExtractorOllamaLocal {
		t.Fatalf("runner definition = %+v", definition)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonHTTPResponse(value any) *http.Response {
	var body strings.Builder
	_ = json.NewEncoder(&body).Encode(value)
	return textHTTPResponse(http.StatusOK, body.String())
}

func textHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
