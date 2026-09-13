package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

const taskTestModel = "gemma4:e4b-it-qat"

func taskModelInput() (taskextract.Task, taskextract.Source) {
	return taskextract.Task{
		ID: "synthetic-release", Revision: 1, Objective: "採集上線條件及尚未完成的限制。",
		SourceID: "synthetic-ticket", Parts: []string{"description", "comments"},
	}, taskextract.Source{
		ID: "synthetic-ticket", Revision: "synthetic-v1", Title: "Synthetic release ticket",
		Location: "https://example.invalid/ticket", Coverage: "exact_excerpt",
		Limitations: []string{"Only description and history are supplied; comments were not collected."},
		Parts: []taskextract.SourcePart{
			{Name: "description", Text: "The owner may approve deployment only after smoke tests pass.\r\n\r\nThe operator must verify the package checksum. This check is not complete."},
			{Name: "history", Text: "UNREQUESTED_HISTORY_NOT_FOR_MODEL"},
		},
	}
}

func taskModelEnvelope() map[string]any {
	return map[string]any{
		"id": "synthetic-task-response", "model": taskTestModel, "status": "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "annotations": []any{},
				"text": `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`,
			}},
		}},
	}
}

func TestExtractTaskUsesBoundedCredentialFreeStructuredRequest(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_ADMIN_KEY", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID"} {
		t.Setenv(key, "synthetic-task-secret")
	}
	t.Setenv("OPENAI_CUSTOM_HEADERS", "Authorization: Bearer synthetic-task-secret\nX-Private: synthetic-task-secret")
	t.Setenv("OPENAI_BASE_URL", "https://must-not-be-used.invalid/v1")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	task, source := taskModelInput()
	prepared, err := taskextract.Prepare(task, source, taskTestModel)
	if err != nil {
		t.Fatal(err)
	}
	var inventories, generations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, values := range r.Header {
			if strings.Contains(strings.Join(values, " "), "synthetic-task-secret") {
				t.Error("ambient credential reached the local model")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			inventories.Add(1)
			if r.Method != http.MethodGet {
				t.Error("inventory was not read-only")
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"gemma4:e4b-it-qat"}]}`)
		case "/v1/responses":
			generations.Add(1)
			var request struct {
				Model           string          `json:"model"`
				Instructions    string          `json:"instructions"`
				MaxOutputTokens int             `json:"max_output_tokens"`
				Temperature     float64         `json:"temperature"`
				Stream          bool            `json:"stream"`
				Tools           json.RawMessage `json:"tools"`
				Reasoning       struct{ Effort string }
				Input           []struct {
					Role    string
					Content []struct{ Type, Text string }
				}
				Text struct {
					Format struct {
						Type   string
						Strict bool
						Schema struct {
							Properties struct {
								Ranges struct {
									Items struct {
										Properties map[string]struct{ Enum []string }
									}
								}
							}
						}
					}
				}
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid model request")
				return
			}
			if r.Method != http.MethodPost || request.Model != taskTestModel || request.Instructions == "" ||
				request.MaxOutputTokens != 2048 || request.Temperature != 0 || request.Stream ||
				len(request.Tools) != 0 || request.Reasoning.Effort != "none" ||
				request.Text.Format.Type != "json_schema" || !request.Text.Format.Strict {
				t.Error("task selection lost its tool-free, bounded structured model contract")
			}
			for _, key := range []string{"start_id", "end_id"} {
				if !slices.Equal(request.Text.Format.Schema.Properties.Ranges.Items.Properties[key].Enum, []string{"u001", "u002"}) {
					t.Errorf("%s did not carry the frozen unit ID enum", key)
				}
			}
			if len(request.Input) != 1 || request.Input[0].Role != "user" || len(request.Input[0].Content) != 1 {
				t.Error("unexpected conversation history or model input parts")
				return
			}
			var input struct {
				Version     string             `json:"version"`
				Objective   string             `json:"objective"`
				SourceTitle string             `json:"source_title"`
				Units       []taskextract.Unit `json:"units"`
			}
			text := request.Input[0].Content[0].Text
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(text), &fields) != nil || len(fields) != 4 {
				t.Error("model input did not contain exactly four projection fields")
			}
			for _, key := range []string{"version", "objective", "source_title", "units"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("model input omitted projection field %q", key)
				}
			}
			if request.Input[0].Content[0].Type != "input_text" || json.Unmarshal([]byte(text), &input) != nil ||
				input.Version != taskextract.ModelInputVersion || input.Objective != task.Objective ||
				input.SourceTitle != source.Title || !reflect.DeepEqual(input.Units, prepared.Units()) {
				t.Error("model input changed the version, objective, title or exact source units")
			}
			for _, excluded := range []string{task.ID, source.ID, source.Revision, source.Location, source.Coverage,
				source.Limitations[0], source.Parts[1].Text, "synthetic-task-secret"} {
				if strings.Contains(text, excluded) {
					t.Errorf("collection metadata or unrequested content entered model input: %q", excluded)
				}
			}
			envelope := taskModelEnvelope()
			envelope["output"] = append([]any{map[string]any{
				"type": "reasoning", "content": []any{map[string]any{"text": "PRIVATE_REASONING_NOT_CANDIDATE"}},
			}}, envelope["output"].([]any)...)
			_ = json.NewEncoder(w).Encode(envelope)
		default:
			t.Error("unexpected source or writer operation")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	result, err := ExtractTask(t.Context(), task, source, taskTestModel, server.URL+"/v1")
	if err != nil || inventories.Load() != 1 || generations.Load() != 1 || result.Status != "selected" ||
		len(result.Candidates) != 1 || result.Candidates[0].Text != source.Parts[0].Text ||
		result.InputID != prepared.InputID() || result.HumanReview != "not_reviewed" || result.AuthorityEffect != "none" ||
		result.Scope.FactCompletenessAssessed || !slices.Equal(result.Scope.NotCollectedParts, []string{"comments"}) ||
		!slices.Equal(result.Scope.NotProvidedParts, []string{"history"}) {
		t.Fatalf("unexpected task selection: err=%v inventories=%d generations=%d result=%+v", err, inventories.Load(), generations.Load(), result)
	}
	raw, err := json.Marshal(result)
	if err != nil || strings.Contains(string(raw), "PRIVATE_REASONING_NOT_CANDIDATE") || strings.Contains(string(raw), "synthetic-task-secret") {
		t.Fatal("reasoning or credentials escaped into the candidate result")
	}
	if _, err := taskextract.Replay(prepared, result); err != nil {
		t.Fatalf("transport result cannot be checked against the frozen input: %v", err)
	}
}

func TestExtractTaskRejectsInvalidInputBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	for _, mode := range []string{"nil_context", "cancelled", "empty_model", "model_whitespace", "model_control", "remote_endpoint", "credential_endpoint", "invalid_task", "oversized_source", "oversized_scope", "no_readable", "missing_parts"} {
		t.Run(mode, func(t *testing.T) {
			task, source := taskModelInput()
			ctx := t.Context()
			name, endpoint := taskTestModel, server.URL+"/v1"
			switch mode {
			case "nil_context":
				ctx = nil
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "empty_model":
				name = ""
			case "model_whitespace":
				name = " " + name
			case "model_control":
				name += "\nother"
			case "remote_endpoint":
				endpoint = "https://example.invalid/v1"
			case "credential_endpoint":
				endpoint += "?token=synthetic-task-secret"
			case "invalid_task":
				task.Objective = ""
			case "oversized_source":
				source.Parts[0].Text = strings.Repeat("x", taskextract.MaxSourceBytes+1)
			case "oversized_scope":
				source.Parts[0].Text = strings.Repeat("x", taskextract.MaxProvidedBytes+1)
			case "no_readable":
				source.Parts[0].Text = " \r\n\r\n\t"
			case "missing_parts":
				task.Parts = []string{"comments"}
			}
			result, err := ExtractTask(ctx, task, source, name, endpoint)
			if err == nil || calls.Load() != 0 || !reflect.DeepEqual(result, taskextract.Result{}) || strings.Contains(err.Error(), "synthetic-task-secret") {
				t.Fatalf("invalid input caused I/O, success or raw diagnostics: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestExtractTaskRejectsModelFailuresWithoutRetry(t *testing.T) {
	for _, mode := range []string{"http", "incomplete", "token_limit", "wrong_model", "missing_model", "tool", "empty_response_id", "empty_final", "multiple_messages"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "http" {
					http.Error(w, "synthetic-task-private-diagnostic", http.StatusTooManyRequests)
					return
				}
				envelope := taskModelEnvelope()
				switch mode {
				case "incomplete":
					envelope["status"] = "incomplete"
				case "token_limit":
					envelope["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				case "wrong_model":
					envelope["model"] = "another-model"
				case "missing_model":
					delete(envelope, "model")
				case "tool":
					envelope["output"] = []any{map[string]any{"type": "function_call", "name": "writer"}}
				case "empty_response_id":
					envelope["id"] = ""
				case "empty_final":
					envelope["output"] = []any{}
				case "multiple_messages":
					output := envelope["output"].([]any)
					envelope["output"] = append(output, output[0])
				}
				_ = json.NewEncoder(w).Encode(envelope)
			})
			task, source := taskModelInput()
			result, err := ExtractTask(t.Context(), task, source, taskTestModel, server.URL+"/v1")
			if err == nil || calls.Load() != 1 || result.Status != "failed" || result.InputID == "" ||
				result.ErrorCode == "" || len(result.Candidates) != 0 || result.RawText != "" ||
				result.HumanReview != "not_reviewed" || result.AuthorityEffect != "none" {
				t.Fatalf("failed model output was accepted, retried or lost its binding: err=%v calls=%d result=%+v", err, calls.Load(), result)
			}
			raw, marshalErr := json.Marshal(result)
			if marshalErr != nil || strings.Contains(string(raw)+err.Error(), "synthetic-task-private-diagnostic") {
				t.Fatal("upstream diagnostic escaped")
			}
		})
	}
}
