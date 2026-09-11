package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const suggestedSourceTool = `{"outcome":"suggested","tool_name":"read_status","arguments":{ },"reason":"僅建議讀取合成 STATUS，仍需人檢查。"}`

func adviceService(t *testing.T, respond func(http.ResponseWriter, *http.Request)) (*Service, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	s := sourceService(t)
	var sourceCalls, sourceRequests atomic.Int64
	source := sourceFixture(t, &sourceCalls)
	counted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequests.Add(1)
		source.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(counted.Close)
	configureSource(t, s, counted.URL)
	server := modelServer(t, respond)
	localSettings(t, s, server)
	if _, err := s.DiscoverTools(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	return s, &sourceCalls, &sourceRequests
}

func TestToolAdviceNeedsSeparateManualSourceConfirmation(t *testing.T) {
	var posts atomic.Int64
	s, sourceCalls, sourceRequests := adviceService(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		body, _ := io.ReadAll(r.Body)
		for _, private := range []string{"private-source-marker", "private-chat-marker", "private-launcher-marker", "private-receipt-marker", "private-other-tool"} {
			if strings.Contains(string(body), private) {
				t.Errorf("private context reached advice model: %s", private)
			}
		}
		var request map[string]any
		if json.Unmarshal(body, &request) != nil {
			t.Error("invalid request")
		}
		reasoning, _ := request["reasoning"].(map[string]any)
		if reasoning["effort"] != "none" || request["think"] != nil {
			t.Error("Responses thinking-off option not set")
		}
		if tools, ok := request["tools"].([]any); ok && len(tools) != 0 {
			t.Error("model was given tool execution capability")
		}
		if !strings.Contains(string(body), "read_status") || !strings.Contains(string(body), "合成來源問題") {
			t.Error("question or tool schema missing")
		}
		writeModelText(w, suggestedSourceTool)
	})
	s.mu.Lock()
	s.state.Source = &SourceView{RawText: "private-source-marker", Capture: &SourceCapture{Inspection: &SourceInspection{ArgumentsJSON: "private-receipt-marker"}}}
	s.state.Messages = []Message{{Text: "private-chat-marker"}}
	s.state.Settings.QueryLauncher = "/private-launcher-marker"
	s.state.Tools = append(s.state.Tools, Tool{ConnectionID: "other", Name: "private-other-tool"})
	s.mu.Unlock()
	before := s.Snapshot()
	filesBefore, err := os.ReadDir(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	requestsBefore := sourceRequests.Load()
	state, err := s.SuggestSourceTool(context.Background(), "fixture", "合成來源問題")
	if err != nil || state.ToolAdvice == nil || posts.Load() != 1 || state.Busy {
		t.Fatalf("advice not completed: %v", err)
	}
	advice := state.ToolAdvice
	if advice.ID == "" || advice.Outcome != "suggested" || advice.Tool == nil || advice.ArgumentsJSON != "{ }" || advice.PromptVersion != sourceToolAdvicePrompt || advice.Question != "合成來源問題" || advice.CreatedAt == "" {
		t.Fatalf("invalid advice projection: %+v", advice)
	}
	if !reflect.DeepEqual(state.Source, before.Source) || !reflect.DeepEqual(state.Settings, before.Settings) || !reflect.DeepEqual(state.Messages, before.Messages) || !reflect.DeepEqual(state.Tools, before.Tools) || state.BatchPath != "" {
		t.Fatal("advice changed source, settings, chat, tool inventory or batch")
	}
	filesAfter, err := os.ReadDir(s.dataDir)
	if err != nil || !reflect.DeepEqual(filesBefore, filesAfter) {
		t.Fatal("advice wrote local files")
	}
	if sourceCalls.Load() != 0 || sourceRequests.Load() != requestsBefore {
		t.Fatal("advice discovered or called the source")
	}
	// The UI projection is detached and cannot change the controller's advice.
	advice.Tool.Name = "forged"
	if s.Snapshot().ToolAdvice.Tool.Name != "read_status" {
		t.Fatal("advice projection aliases the service")
	}
	tool := before.Tools[0]
	if _, err := s.CallSourceTool(context.Background(), "fixture", tool.Name, "{ }", "admit"); err == nil || sourceCalls.Load() != 0 {
		t.Fatal("model/chat advice became execution authority")
	}
	if s.Snapshot().ToolAdvice != nil {
		t.Fatal("new operation retained the previous advice")
	}
	confirmation := "fixture\n" + tool.Name + "\n" + tool.Digest + "\n{ }"
	state, err = s.CallSourceTool(context.Background(), "fixture", tool.Name, "{ }", confirmation)
	if err != nil || sourceCalls.Load() != 1 || state.Source.Capture == nil || state.BatchPath != "" {
		t.Fatalf("separate exact source call failed: %v", err)
	}
}

func TestToolAdviceRejectsInvalidOutputsWithoutRetry(t *testing.T) {
	outputs := map[string]string{
		"fenced":            "```json\n" + suggestedSourceTool + "\n```",
		"unknown tool":      strings.Replace(suggestedSourceTool, "read_status", "writer", 1),
		"wrong arguments":   strings.Replace(suggestedSourceTool, "{ }", `{"id":"invented"}`, 1),
		"duplicate":         strings.Replace(suggestedSourceTool, `"outcome":`, `"outcome":"abstain","outcome":`, 1),
		"case alias":        strings.Replace(suggestedSourceTool, `"reason":`, `"Reason":`, 1),
		"extra":             strings.TrimSuffix(suggestedSourceTool, "}") + `,"approved":true}`,
		"trailing":          suggestedSourceTool + "{}",
		"null reason":       `{"outcome":"suggested","tool_name":"read_status","arguments":{},"reason":null}`,
		"null arguments":    `{"outcome":"suggested","tool_name":"read_status","arguments":null,"reason":"reason"}`,
		"unpaired unicode":  `{"outcome":"abstain","tool_name":"","arguments":{},"reason":"\ud800"}`,
		"abstain tool":      `{"outcome":"abstain","tool_name":"read_status","arguments":{},"reason":"reason"}`,
		"abstain arguments": `{"outcome":"abstain","tool_name":"","arguments":{"id":1},"reason":"reason"}`,
		"large reason":      `{"outcome":"abstain","tool_name":"","arguments":{},"reason":"` + strings.Repeat("x", 4097) + `"}`,
	}
	for name, output := range outputs {
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int64
			s, calls, requests := adviceService(t, func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				writeModelText(w, output)
			})
			before := requests.Load()
			s.mu.Lock()
			s.state.ToolAdvice = &ToolAdvice{ID: "old"}
			s.mu.Unlock()
			state, err := s.SuggestSourceTool(context.Background(), "fixture", "合成測試")
			if err == nil || state.ToolAdvice != nil || state.Busy || posts.Load() != 1 || calls.Load() != 0 || requests.Load() != before {
				t.Fatalf("invalid advice was repaired/retried/executed: %v", err)
			}
		})
	}
}

func TestToolAdviceAbstentionAndInvalidation(t *testing.T) {
	s, calls, requests := adviceService(t, func(w http.ResponseWriter, _ *http.Request) {
		writeModelText(w, `{"outcome":"abstain","tool_name":"","arguments":{ },"reason":"需要先提供時間範圍。"}`)
	})
	before := requests.Load()
	state, err := s.SuggestSourceTool(context.Background(), "fixture", "未提供時間範圍")
	if err != nil || state.ToolAdvice == nil || state.ToolAdvice.Outcome != "abstain" || state.ToolAdvice.Tool != nil || state.ToolAdvice.ArgumentsJSON != "" || calls.Load() != 0 || requests.Load() != before {
		t.Fatalf("abstention failed: %v", err)
	}
	state, err = s.SaveSettings(state.Settings)
	if err != nil || state.ToolAdvice != nil || len(state.Tools) != 0 {
		t.Fatal("settings did not invalidate suggestion and inventory")
	}
}

func TestToolAdvicePrerequisitesRejectBeforeModel(t *testing.T) {
	for _, failure := range []string{"demo", "batch path", "batch digest", "empty question", "large question", "no tools", "digest", "config", "duplicate", "schema", "disallowed", "unknown connection", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			var posts atomic.Int64
			s, calls, requests := adviceService(t, func(w http.ResponseWriter, _ *http.Request) { posts.Add(1); writeModelText(w, suggestedSourceTool) })
			before := requests.Load()
			id, question := "fixture", "合成問題"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.mu.Lock()
			switch failure {
			case "demo":
				s.state.Settings.Mode = "demo"
			case "batch path":
				s.state.BatchPath = "/private/batch.json"
			case "batch digest":
				s.state.BatchDigest = "saved"
			case "empty question":
				question = " "
			case "large question":
				question = strings.Repeat("x", 4097)
			case "no tools":
				s.state.Tools = nil
			case "digest":
				s.state.Tools[0].Digest = "forged"
			case "config":
				s.state.Settings.Connections[0].Name = "changed"
			case "duplicate":
				s.state.Tools = append(s.state.Tools, s.state.Tools[0])
			case "schema":
				s.state.Tools[0].InputSchemaJSON = "{}"
			case "disallowed":
				s.state.Settings.Connections[0].AllowedTools = []string{"other"}
			case "unknown connection":
				id = "unknown"
			case "cancelled":
				cancel()
			}
			s.mu.Unlock()
			state, err := s.SuggestSourceTool(ctx, id, question)
			if err == nil || state.ToolAdvice != nil || state.Busy || posts.Load() != 0 || requests.Load() != before || calls.Load() != 0 {
				t.Fatalf("failed prerequisite reached model/source: %v", err)
			}
		})
	}
}

func TestToolAdviceCancellation(t *testing.T) {
	started := make(chan struct{})
	s, calls, requests := adviceService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	before := requests.Load()
	finished := make(chan State, 1)
	go func() {
		state, _ := s.SuggestSourceTool(context.Background(), "fixture", "取消測試")
		finished <- state
	}()
	<-started
	if _, err := s.SuggestSourceTool(context.Background(), "fixture", "並行測試"); err == nil {
		t.Fatal("parallel operation was accepted")
	}
	s.Cancel()
	state := <-finished
	if state.ToolAdvice != nil || state.Busy || state.Error == "" || calls.Load() != 0 || requests.Load() != before {
		t.Fatal("cancelled advice published a successful suggestion")
	}
}

func TestToolAdviceCancellationBeforePublication(t *testing.T) {
	s := sourceService(t)
	ctx, done, err := s.begin(context.Background(), "模型來源工具建議")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	s.Cancel()
	state, err := s.applyToolAdvice(ctx, done, ToolAdvice{ID: "completed-model-reply", Outcome: "abstain"})
	if err == nil || state.ToolAdvice != nil || state.Busy {
		t.Fatal("late cancelled result was published")
	}
}

func TestToolAdviceRejectsFailedModelEnvelope(t *testing.T) {
	for _, mode := range []string{"http error", "incomplete", "tool", "wrong model", "empty"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int64
			s, calls, requests := adviceService(t, func(w http.ResponseWriter, _ *http.Request) {
				posts.Add(1)
				switch mode {
				case "http error":
					http.Error(w, "synthetic-advice-secret", 500)
				case "incomplete":
					_, _ = w.Write([]byte(`{"model":"gemma4:e4b-it-qat","status":"incomplete","output":[]}`))
				case "tool":
					_, _ = w.Write([]byte(`{"model":"gemma4:e4b-it-qat","status":"completed","output":[{"type":"function_call","name":"synthetic-advice-secret"}]}`))
				case "wrong model":
					_, _ = w.Write([]byte(`{"model":"synthetic-advice-secret","status":"completed","output":[]}`))
				case "empty":
					writeModelText(w, "")
				}
			})
			before := requests.Load()
			state, err := s.SuggestSourceTool(context.Background(), "fixture", "合成失敗測試")
			body, _ := json.Marshal(state)
			if err == nil || state.ToolAdvice != nil || state.Busy || posts.Load() != 1 || requests.Load() != before || calls.Load() != 0 || strings.Contains(string(body), "synthetic-advice-secret") {
				t.Fatalf("model failure escaped as advice or retried: %v", err)
			}
		})
	}
}

func TestToolAdviceInputExcludesOtherServersAndRejectsMixedInventory(t *testing.T) {
	s, _, _ := adviceService(t, func(w http.ResponseWriter, _ *http.Request) { writeModelText(w, suggestedSourceTool) })
	state := s.Snapshot()
	config := sourceConfig(state.Settings.Connections[0])
	tool := state.Tools[0]
	other := tool
	other.ConnectionID = "other"
	other.Name = "other-server-tool"
	tools, input, err := toolAdviceInput(config, []Tool{tool, other}, "question")
	if err != nil || len(tools) != 1 || strings.Contains(input, "other-server-tool") || strings.Contains(input, config.URL) {
		t.Fatalf("model input did not isolate the chosen inventory: %v", err)
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &wire) != nil || len(wire) != 2 || wire["question"] == nil || wire["tools"] == nil {
		t.Fatal("model input fields expanded")
	}
	config.AllowedTools = append(config.AllowedTools, "second")
	// Config hash validation alone rejects mixing a new connection configuration
	// with its old saved tool, without any model or network work.
	if _, _, err := toolAdviceInput(config, []Tool{tool}, "question"); err == nil {
		t.Fatal("changed configuration accepted")
	}
	if err := sourcemcp.ValidateArgumentsJSON(`{"x":1,"x":2}`); err == nil {
		t.Fatal("duplicate argument checker unavailable")
	}
}
