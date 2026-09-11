package labstatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

func TestExtractorRequiresNormalExplicitFinishReason(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		reason    genai.FinishReason
		wantError bool
	}{
		{name: "empty adapter reason"},
		{name: "normal stop", reason: genai.FinishReasonStop},
		{name: "token limit", reason: genai.FinishReasonMaxTokens, wantError: true},
		{name: "safety", reason: genai.FinishReasonSafety, wantError: true},
		{name: "other", reason: genai.FinishReasonOther, wantError: true},
		{name: "explicit unspecified", reason: genai.FinishReasonUnspecified, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
			candidates := validCandidates()
			candidates.Records[0].Citation.ExactQuote = ""
			body, err := json.Marshal(candidates)
			if err != nil {
				t.Fatal(err)
			}
			extractor, err := NewExtractor(&failureLLM{response: &model.LLMResponse{
				Content:      genai.NewContentFromText(string(body), genai.RoleModel),
				FinishReason: test.reason,
			}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := extractor.Extract(t.Context(), document)
			if test.wantError {
				if !errors.Is(err, ErrModelResponse) || result.Result.Outcome != "" {
					t.Fatalf("finish reason %q: outcome = %q, error = %v; want model response error without candidates", test.reason, result.Result.Outcome, err)
				}
				return
			}
			if err != nil || result.Result.Outcome != "extracted" || len(result.Result.Records) != 1 {
				t.Fatalf("finish reason %q: outcome = %q, records = %d, error = %v", test.reason, result.Result.Outcome, len(result.Result.Records), err)
			}
		})
	}
}

func TestExtractorRejectsIncompleteResponsesWithValidJSON(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		status     string
		incomplete string
		wantError  bool
	}{
		{name: "completed control", status: "completed"},
		{name: "token limit", status: "incomplete", incomplete: "max_output_tokens", wantError: true},
		{name: "content filter", status: "incomplete", incomplete: "content_filter", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
			candidates := validCandidates()
			candidates.Records[0].Citation.ExactQuote = ""
			candidateJSON, err := json.Marshal(candidates)
			if err != nil {
				t.Fatal(err)
			}
			response := map[string]any{
				"id": "response-synthetic", "object": "response", "model": "completion-review-model",
				"status": test.status,
				"output": []any{map[string]any{
					"id": "message-synthetic", "type": "message", "role": "assistant", "status": test.status,
					"content": []any{map[string]any{"type": "output_text", "text": string(candidateJSON)}},
				}},
			}
			if test.incomplete != "" {
				response["incomplete_details"] = map[string]string{"reason": test.incomplete}
			}
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			// Exercise the pinned ADK adapter without listening on a port or
			// contacting any model service. It retains the finish reason even
			// when a truncated or filtered response happens to contain valid JSON.
			client := &http.Client{Transport: completionReviewTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
					Request:    r,
				}, nil
			})}
			llm, err := openaimodel.NewModel(t.Context(), "completion-review-model", &openaimodel.ClientConfig{
				BaseURL: "http://synthetic.invalid/v1", HTTPClient: client,
				Options: []option.RequestOption{option.WithAPIKey(""), option.WithMaxRetries(0)},
			})
			if err != nil {
				t.Fatal(err)
			}
			extractor, err := NewExtractor(llm)
			if err != nil {
				t.Fatal(err)
			}
			result, err := extractor.Extract(t.Context(), document)
			if calls != 1 {
				t.Fatalf("model adapter requests = %d, want 1", calls)
			}
			if test.wantError {
				if !errors.Is(err, ErrModelResponse) || result.Result.Outcome != "" {
					t.Fatalf("incomplete response: outcome = %q, records = %d, error = %v; want model response error without candidates", result.Result.Outcome, len(result.Result.Records), err)
				}
				return
			}
			if err != nil || result.Result.Outcome != "extracted" || len(result.Result.Records) != 1 {
				t.Fatalf("completed control: outcome = %q, records = %d, error = %v", result.Result.Outcome, len(result.Result.Records), err)
			}
		})
	}
}

type completionReviewTransport func(*http.Request) (*http.Response, error)

func (f completionReviewTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
