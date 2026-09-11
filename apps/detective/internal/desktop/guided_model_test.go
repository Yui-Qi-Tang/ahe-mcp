package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func guidedModelSource() sourcepilot.GuidedSource {
	return sourcepilot.GuidedSource{Version: "detective-summary-source/v1", SourceID: "private-source-coordinate", SourceRevision: "private-revision",
		SourceURL: "https://example.invalid/private-source-url", ObservedAt: "2026-09-10T01:00:00Z", SummaryOrigin: "synthetic external summary",
		Coverage: "full_document", Limitations: []string{}, Summary: "Summary-only marker: Payment API recovered.",
		Body: "Payment API recovered at 14:20 UTC.<br />Refund API still had delays."}
}

func TestNewGuidedReviewerUsesThreeToolFreeLocalRequests(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_ADMIN_KEY", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID"} {
		t.Setenv(key, "synthetic-guided-secret")
	}
	t.Setenv("OPENAI_CUSTOM_HEADERS", "Authorization: Bearer synthetic-guided-secret\nX-Guided-Secret: synthetic-guided-secret")
	t.Setenv("OPENAI_BASE_URL", "https://must-not-be-used.invalid/v1")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	answers := []string{
		`{"claims":[{"text":"付款 API 已恢復。","subject":"付款 API","predicate":"已恢復","object":""}],"reason":""}`,
		`{"assessments":[{"claim":1,"relation":"supported","segments":[1],"explanation":"原文明確記載付款 API 已恢復。"}]}`,
		`{"observations":[{"statement":{"text":"退款 API 仍有延遲。","subject":"退款 API","predicate":"仍有","object":"延遲"},"segments":[2]}],"reason":""}`,
	}
	var inventories, generations atomic.Int64
	inputs := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, values := range r.Header {
			if strings.Contains(strings.Join(values, " "), "synthetic-guided-secret") {
				t.Errorf("ambient secret header %s reached model transport", key)
			}
		}
		switch r.URL.Path {
		case "/v1/models":
			inventories.Add(1)
			_, _ = io.WriteString(w, `{"data":[{"id":"gemma4:e4b-it-qat"}]}`)
		case "/v1/responses":
			index := int(generations.Add(1)) - 1
			if index >= len(answers) {
				t.Error("unexpected extra generation")
				http.Error(w, "unexpected extra generation", http.StatusBadRequest)
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			var request struct {
				Model string `json:"model"`
				Input []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
				Tools           []json.RawMessage `json:"tools"`
				Stream          bool              `json:"stream"`
				Temperature     *float64          `json:"temperature"`
				MaxOutputTokens int               `json:"max_output_tokens"`
				Reasoning       struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
				Text struct {
					Format struct {
						Type   string `json:"type"`
						Strict bool   `json:"strict"`
					} `json:"format"`
				} `json:"text"`
			}
			if json.Unmarshal(raw, &request) != nil || request.Model != "gemma4:e4b-it-qat" || len(request.Tools) != 0 || request.Stream || request.Temperature == nil || *request.Temperature != 0 || request.MaxOutputTokens != 1536 || request.Reasoning.Effort != "none" || request.Text.Format.Type != "json_schema" || !request.Text.Format.Strict || len(request.Input) != 1 || len(request.Input[0].Content) != 1 {
				t.Error("request lost bounded, explicit, tool-free structured generation")
				return
			}
			for _, excluded := range []string{"private-source-coordinate", "private-revision", "example.invalid/private-source-url", "synthetic-guided-secret"} {
				if strings.Contains(string(raw), excluded) {
					t.Errorf("non-content metadata %q entered generation", excluded)
				}
			}
			inputs <- request.Input[0].Content[0].Text
			writeModelText(w, answers[index])
		default:
			t.Error("unexpected source or writer request")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	reviewer, err := NewGuidedReviewer(t.Context(), "gemma4:e4b-it-qat", server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	report, err := reviewer.Review(t.Context(), guidedModelSource())
	if err != nil {
		t.Fatal(err)
	}
	if inventories.Load() != 1 || generations.Load() != 3 || report.Stage != "complete" || report.AuthorityEffect != "none" || report.HumanReview != "not_reviewed" {
		t.Fatal("wrong attempt count or authority boundary")
	}
	first, second, third := <-inputs, <-inputs, <-inputs
	if !strings.Contains(first, "Summary-only marker") || strings.Contains(first, "Refund API") || !strings.Contains(second, "付款 API") || !strings.Contains(second, "Refund API still had delays.") || !strings.Contains(third, "Refund API still had delays.") || strings.Contains(third, "Summary-only marker") || strings.Contains(third, "付款 API") {
		t.Fatal("three independent input stages leaked summary into the blind body reading")
	}
}

func TestNewGuidedReviewerRejectsInvalidSettingsBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name           string
		ctx            context.Context
		model, baseURL string
	}{
		{"nil-context", nil, "gemma4:e4b-it-qat", server.URL + "/v1"},
		{"cancelled", cancelled, "gemma4:e4b-it-qat", server.URL + "/v1"},
		{"missing-model", t.Context(), "", server.URL + "/v1"},
		{"space-model", t.Context(), " gemma4:e4b-it-qat", server.URL + "/v1"},
		{"control-model", t.Context(), "model\nname", server.URL + "/v1"},
		{"wrong-path", t.Context(), "gemma4:e4b-it-qat", server.URL + "/api"},
		{"query", t.Context(), "gemma4:e4b-it-qat", server.URL + "/v1?token=synthetic-secret"},
		{"credentials", t.Context(), "gemma4:e4b-it-qat", strings.Replace(server.URL, "http://", "http://user:synthetic-secret@", 1) + "/v1"},
		{"external", t.Context(), "gemma4:e4b-it-qat", "https://example.invalid/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reviewer, err := NewGuidedReviewer(test.ctx, test.model, test.baseURL)
			if err == nil || reviewer != nil || calls.Load() != 0 || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("invalid model settings caused network or leaked details")
			}
		})
	}
}

func TestNewGuidedReviewerDoesNotRetryLaterTransportFailure(t *testing.T) {
	var generations atomic.Int64
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		if generations.Add(1) == 1 {
			writeModelText(w, `{"claims":[{"text":"付款 API 已恢復。","subject":"付款 API","predicate":"已恢復","object":""}],"reason":""}`)
			return
		}
		http.Error(w, "synthetic-private-upstream-error", http.StatusTooManyRequests)
	})
	reviewer, err := NewGuidedReviewer(t.Context(), "gemma4:e4b-it-qat", server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	report, err := reviewer.Review(t.Context(), guidedModelSource())
	if err == nil || generations.Load() != 2 || report.Stage != "source_check" || len(report.Claims) != 1 || len(report.Assessments) != 0 || len(report.Attempts) != 2 || report.Attempts[1].RawText != "" {
		t.Fatal("transport failure was retried or discarded completed claims")
	}
	raw, marshalErr := json.Marshal(report)
	if marshalErr != nil || strings.Contains(string(raw)+err.Error(), "synthetic-private-upstream-error") {
		t.Fatal("upstream HTTP diagnostic escaped into the partial report")
	}
}
