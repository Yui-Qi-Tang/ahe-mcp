package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func briefModelSource() sourcepilot.BriefSource {
	return sourcepilot.BriefSource{Version: sourcepilot.BriefSourceVersion, SourceKind: "public_event",
		SourceID: "private-brief-source", SourceRevision: "private-brief-revision",
		SourceURL: "https://example.invalid/private-brief-url", ObservedAt: "2026-09-10T01:00:00Z",
		Coverage: "full_document", Limitations: []string{},
		Body: strings.Repeat("The service reported increased errors. ", 20) + "<br />The cause remains unconfirmed. END-OF-SOURCE"}
}

func TestNewBriefExtractorUsesOneFullBodyPlainTextRequest(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_ADMIN_KEY", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID"} {
		t.Setenv(key, "synthetic-brief-secret")
	}
	t.Setenv("OPENAI_CUSTOM_HEADERS", "Authorization: Bearer synthetic-brief-secret\nX-Private: synthetic-brief-secret")
	t.Setenv("OPENAI_BASE_URL", "https://must-not-be-used.invalid/v1")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	source := briefModelSource()
	answer := "服務錯誤增加，原因尚未確認。"
	var inventories, generations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, values := range r.Header {
			if strings.Contains(strings.Join(values, " "), "synthetic-brief-secret") {
				t.Error("ambient credential reached model")
			}
		}
		switch r.URL.Path {
		case "/v1/models":
			inventories.Add(1)
			_, _ = io.WriteString(w, `{"data":[{"id":"gemma4:e4b-it-qat"}]}`)
		case "/v1/responses":
			generations.Add(1)
			var request map[string]any
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid request")
				return
			}
			reasoning, _ := request["reasoning"].(map[string]any)
			temperature, _ := request["temperature"].(float64)
			topP, _ := request["top_p"].(float64)
			if request["model"] != "gemma4:e4b-it-qat" || request["instructions"] != sourcepilot.BriefInstruction ||
				request["max_output_tokens"] != float64(768) || temperature < 0.2999 || temperature > 0.3001 || topP < 0.8999 || topP > 0.9001 ||
				reasoning["effort"] != "none" || request["tools"] != nil || request["stream"] == true || strings.Contains(fmt.Sprint(request["text"]), "json_schema") {
				t.Error("brief lost its bounded, tool-free plain-text request")
			}
			inputs, _ := request["input"].([]any)
			if len(inputs) != 1 {
				t.Error("unexpected conversation history")
				return
			}
			message, _ := inputs[0].(map[string]any)
			parts, _ := message["content"].([]any)
			if len(parts) != 1 {
				t.Error("unexpected content parts")
				return
			}
			part, _ := parts[0].(map[string]any)
			text, _ := part["text"].(string)
			var body map[string]string
			if json.Unmarshal([]byte(text), &body) != nil || len(body) != 1 || body["body"] != source.Body {
				t.Error("input was truncated, enriched or not exactly the source body")
			}
			for _, excluded := range []string{source.SourceID, source.SourceRevision, source.SourceURL, "synthetic-brief-secret"} {
				if strings.Contains(fmt.Sprint(request), excluded) {
					t.Errorf("metadata entered model input: %q", excluded)
				}
			}
			writeModelText(w, answer)
		default:
			t.Error("unexpected source or writer request")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	extractor, err := NewBriefExtractor(t.Context(), "gemma4:e4b-it-qat", server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	report, err := extractor.Extract(t.Context(), source)
	if err != nil || report.Stage != "complete" || report.Text != answer || report.RawText != answer || report.Source.Body != source.Body || report.HumanReview != "not_reviewed" || report.AuthorityEffect != "none" || report.ReferenceScope != "complete_input_not_claim_support" || inventories.Load() != 1 || generations.Load() != 1 {
		t.Fatalf("brief result or operation count changed: err=%v inventories=%d generations=%d", err, inventories.Load(), generations.Load())
	}
}

func TestNewBriefExtractorRejectsInvalidSettingsBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		ctx            context.Context
		name, endpoint string
	}{
		{nil, "gemma4:e4b-it-qat", server.URL + "/v1"},
		{cancelled, "gemma4:e4b-it-qat", server.URL + "/v1"},
		{t.Context(), "", server.URL + "/v1"},
		{t.Context(), " model", server.URL + "/v1"},
		{t.Context(), "model\nname", server.URL + "/v1"},
		{t.Context(), "gemma4:e4b-it-qat", server.URL + "/v1?token=synthetic-secret"},
		{t.Context(), "gemma4:e4b-it-qat", "https://example.invalid/v1"},
	} {
		extractor, err := NewBriefExtractor(test.ctx, test.name, test.endpoint)
		if err == nil || extractor != nil || calls.Load() != 0 || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("invalid settings caused a request or leaked a credential")
		}
	}
}

func TestNewBriefExtractorRejectsTransportFailuresWithoutRetry(t *testing.T) {
	for _, mode := range []string{"http", "incomplete", "tool", "wrong_model"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "http":
					http.Error(w, "synthetic-private-diagnostic", http.StatusTooManyRequests)
				case "incomplete":
					_, _ = io.WriteString(w, `{"model":"gemma4:e4b-it-qat","status":"incomplete","output":[]}`)
				case "tool":
					_, _ = io.WriteString(w, `{"model":"gemma4:e4b-it-qat","status":"completed","output":[{"type":"function_call","name":"writer"}]}`)
				case "wrong_model":
					_, _ = io.WriteString(w, `{"model":"wrong","status":"completed","output":[]}`)
				}
			})
			extractor, err := NewBriefExtractor(t.Context(), "gemma4:e4b-it-qat", server.URL+"/v1")
			if err != nil {
				t.Fatal(err)
			}
			report, err := extractor.Extract(t.Context(), briefModelSource())
			if err == nil || calls.Load() != 1 || report.Stage == "complete" || report.Text != "" || report.RawText != "" {
				t.Fatal("failed transport was retried or accepted")
			}
			raw, marshalErr := json.Marshal(report)
			if marshalErr != nil || strings.Contains(string(raw)+err.Error(), "synthetic-private-diagnostic") {
				t.Fatal("upstream diagnostic escaped")
			}
		})
	}
}
