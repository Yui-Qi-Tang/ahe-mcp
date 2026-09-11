package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reasoningMarker = "synthetic-private-reasoning-not-for-display"

func modelMessage(text string) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}}
}

func modelReasoning() map[string]any {
	return map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": reasoningMarker}}, "encrypted_content": reasoningMarker}
}

func writeModelItems(w http.ResponseWriter, items ...any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "gemma4:e4b-it-qat", "status": "completed", "output": items})
}

func TestDesktopResponsesReasoningIsNotFinalContent(t *testing.T) {
	for _, operation := range []string{"chat", "extract"} {
		t.Run(operation, func(t *testing.T) {
			s := newTestService(t)
			if operation == "extract" {
				importTestSource(t, s)
			}
			server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Reasoning struct{ Effort string } `json:"reasoning"`
					Think     json.RawMessage         `json:"think"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Reasoning.Effort != "none" || request.Think != nil {
					t.Error("request must use Responses reasoning.effort=none, not native think")
				}
				final := "這是最終的來源閱讀說明。"
				if operation == "extract" {
					final = extractedCandidate
				}
				writeModelItems(w, modelReasoning(), modelMessage(final))
			})
			localSettings(t, s, server)
			var state State
			var err error
			if operation == "extract" {
				state, err = s.Extract(context.Background(), 4)
			} else {
				state, err = s.SendMessage(context.Background(), "請閱讀來源")
			}
			if err != nil {
				t.Fatalf("valid final content rejected: %v", err)
			}
			body, _ := json.Marshal(state)
			if strings.Contains(string(body), reasoningMarker) {
				t.Fatal("reasoning reached UI state")
			}
			if operation == "extract" && (len(state.Candidates) != 1 || state.BatchResult == nil || state.BatchResult.Summary.SubmissionAttempts != 0) {
				t.Fatal("extraction did not remain a local candidate")
			}
			files, err := os.ReadDir(s.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				body, err := os.ReadFile(filepath.Join(s.dataDir, file.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(body), reasoningMarker) {
					t.Fatal("reasoning was saved")
				}
			}
		})
	}
}

func TestDesktopModelBodyCancellationKeepsErrorIdentity(t *testing.T) {
	started := make(chan struct{})
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":`))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := &http.Transport{}
	t.Cleanup(base.CloseIdleConnections)
	transport := &modelTransport{base: base, host: endpoint.Host, model: "gemma4:e4b-it-qat"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		response, err := transport.RoundTrip(request)
		if response != nil {
			response.Body.Close()
		}
		finished <- err
	}()
	<-started
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("body cancellation misclassified: %v", err)
	}
}

func TestDesktopResponsesRejectUnsafeOrMissingFinal(t *testing.T) {
	for _, mode := range []string{"reasoning_only", "reasoning_tool", "reasoning_unknown", "reasoning_refusal", "empty_text", "incomplete_message", "wrong_role"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestService(t)
			server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				message := modelMessage("not accepted")
				items := []any{modelReasoning(), message}
				switch mode {
				case "reasoning_only":
					items = items[:1]
				case "reasoning_tool":
					items = append(items, map[string]any{"type": "function_call", "name": "writer"})
				case "reasoning_unknown":
					items = append(items, map[string]any{"type": "unknown"})
				case "reasoning_refusal":
					message["content"] = []any{map[string]any{"type": "refusal", "refusal": reasoningMarker}}
				case "empty_text":
					items[1] = modelMessage(" \n")
				case "incomplete_message":
					message["status"] = "incomplete"
				case "wrong_role":
					message["role"] = "user"
				}
				writeModelItems(w, items...)
			})
			localSettings(t, s, server)
			state, err := s.SendMessage(context.Background(), "請閱讀來源")
			if err == nil || len(state.Messages) != 1 {
				t.Fatal("unsafe final accepted")
			}
		})
	}
}
