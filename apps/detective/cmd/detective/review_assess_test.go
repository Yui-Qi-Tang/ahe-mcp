package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func assessCLIArguments() []string {
	return []string{"-decision", "/missing/decision", "-base-url", "http://127.0.0.1:1", "-model", "fixture:local", "-out", "/missing/advisory"}
}

func TestReviewAssessRequiresExplicitClosedFlags(t *testing.T) {
	t.Setenv("DETECTIVE_MODEL", "private-environment-marker")
	t.Setenv("OLLAMA_HOST", "https://private-environment-marker.invalid")
	for _, extra := range [][]string{
		{"-review-command", "/private-marker"}, {"-query-command", "/private-marker"},
		{"-input", "/private-marker"}, {"-version"}, {"-ahe-submit-pending"},
		{"-confirm-display", "private-marker"}, {"extra-private-marker"}, {"--", "private-marker"},
		{"-model", "private-marker"}, {"-timeout", "0"}, {"-timeout", "-1s"},
		{"-timeout", "6m"}, {"-timeout", "private-marker"},
		{"-think=private-marker"}, {"-think=true", "-think=false"},
		{"-think=false", "--think=true"}, {"-think"}, {"-think", "true"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runReviewAssess(append(assessCLIArguments(), extra...), &stdout, &stderr)
			if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("invalid assess flags reached I/O or exposed input: %v", err)
			}
		})
	}
	for i := 0; i < len(assessCLIArguments()); i += 2 {
		for _, value := range []string{"", " "} {
			args := assessCLIArguments()
			args[i+1] = value
			var stdout, stderr bytes.Buffer
			if err := runReviewAssess(args, &stdout, &stderr); err == nil || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("missing explicit assess input reached I/O")
			}
		}
	}
	for _, args := range [][]string{{}, {"-assessment", "relative"}, {"-assessment", "/missing", "-model", "private-marker"}, {"-assessment", "/missing", "extra"}, {"-assessment", "/one", "-assessment", "/two"}, {"-assessment", "/missing", "-think=true"}, {"-assessment", "/missing", "-think=false"}} {
		var out bytes.Buffer
		if err := runReviewQuestions(args, &out); err == nil || out.Len() != 0 || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("questions accepted an unsupported input or exposed input")
		}
	}
}

func TestReviewFlagsBooleanRequiresEqualsBeforeParsing(t *testing.T) {
	for _, name := range []string{"think", "another-mode"} {
		for _, tt := range []struct {
			name string
			args []string
			ok   bool
			on   bool
		}{
			{name: "explicit_true", args: []string{"-" + name + "=true"}, ok: true, on: true},
			{name: "explicit_false", args: []string{"--" + name + "=false"}, ok: true},
			{name: "bare", args: []string{"-" + name}},
			{name: "separate_value", args: []string{"-" + name, "true"}},
			{name: "hidden_bool", args: []string{"-" + name, "-" + name + "=false"}},
			{name: "hidden_model", args: []string{"-" + name, "-model=private-marker"}},
			{name: "hidden_endpoint", args: []string{"-" + name, "--base-url=http://private-marker.invalid"}},
			{name: "hidden_output", args: []string{"-" + name, "-out=/private-marker"}},
			{name: "hidden_new_flag", args: []string{"-" + name, "-timeout=1s"}},
		} {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				flags := flag.NewFlagSet("review", flag.ContinueOnError)
				on := flags.Bool(name, false, "")
				model := flags.String("model", "unchanged-model", "")
				endpoint := flags.String("base-url", "unchanged-endpoint", "")
				out := flags.String("out", "unchanged-output", "")
				flags.String("timeout", "", "")
				args := append([]string{"-model", "fixture:local", "-base-url=http://127.0.0.1:1", "-out", "/fixture/output"}, tt.args...)
				err := parseReviewFlags(flags, args)
				if tt.ok {
					if err != nil || *on != tt.on || *model != "fixture:local" || *endpoint != "http://127.0.0.1:1" || *out != "/fixture/output" {
						t.Fatalf("explicit boolean changed existing flag parsing: %v", err)
					}
					return
				}
				if err == nil || strings.Contains(err.Error(), "private-marker") || *on || *model != "unchanged-model" || *endpoint != "unchanged-endpoint" || *out != "unchanged-output" {
					t.Fatalf("bare boolean reached flag.Parse or hid a following flag: %v", err)
				}
			})
		}
	}
}

func TestReviewAssessThinkingIsExplicitAndNeverRetainsRawThinking(t *testing.T) {
	t.Setenv("DETECTIVE_THINK", "true")
	t.Setenv("OLLAMA_THINK", "true")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/must-not-start")
	t.Setenv("DETECTIVE_AHE_QUERY_COMMAND", "/must-not-start")
	t.Setenv("DETECTIVE_AHE_REVIEW_COMMAND", "/must-not-start")
	const privateThinking = "private-thinking-marker：這是測試用的合成文字。\n不得保存原文。"
	for _, tt := range []struct {
		name     string
		args     []string
		think    bool
		thinking string
		fail     bool
	}{
		{name: "default ignores environment"},
		{name: "explicit false ignores environment", args: []string{"-think=false"}},
		{name: "explicit true", args: []string{"-think=true"}, think: true, thinking: privateThinking},
		{name: "double dash explicit true", args: []string{"--think=true"}, think: true, thinking: privateThinking},
		{name: "default rejects nonempty thinking", thinking: privateThinking, fail: true},
		{name: "explicit false rejects nonempty thinking", args: []string{"-think=false"}, thinking: privateThinking, fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decisionPath, decision := assessCLIFixture(t)
			before, err := os.ReadFile(decisionPath)
			if err != nil {
				t.Fatal(err)
			}
			var tags, calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "" {
					t.Error("unexpected credentials")
				}
				switch r.URL.Path {
				case "/api/tags":
					tags.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "fixture:local", "model": "fixture:local", "size": 1, "digest": strings.Repeat("a", 64), "details": map[string]any{"format": "gguf"}}}})
				case "/api/chat":
					calls.Add(1)
					var request map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error("invalid request JSON")
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					wantThink := "false"
					if tt.think {
						wantThink = "true"
					}
					if len(request) != 8 || string(request["think"]) != wantThink || request["tools"] != nil || request["tool_choice"] != nil {
						t.Error("CLI changed thinking mode or introduced tool authority")
					}
					for _, field := range []string{"shift", "stream", "truncate"} {
						if string(request[field]) != "false" {
							t.Errorf("unrelated request field %s changed", field)
						}
					}
					var options map[string]int
					if err := json.Unmarshal(request["options"], &options); err != nil || len(options) != 2 || options["num_predict"] != 2048 || options["temperature"] != 0 {
						t.Error("thinking changed generation options")
					}
					message := map[string]any{"role": "assistant", "content": `{"verdict":"no_specific_concern","summary":"此合成回覆只測試保存邊界，不代表語意驗收。","concerns":[]}`}
					if tt.thinking != "" {
						message["thinking"] = tt.thinking
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:local", "done": true, "done_reason": "stop", "message": message})
				default:
					t.Error("unexpected endpoint, including possible MCP call")
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			outPath := filepath.Join(filepath.Dir(decisionPath), "advisory.json")
			args := append([]string{"-decision", decisionPath, "-base-url", server.URL, "-model", "fixture:local", "-out", outPath}, tt.args...)
			var stdout, stderr bytes.Buffer
			err = runReviewAssess(args, &stdout, &stderr)
			if tags.Load() != 1 || calls.Load() != 1 {
				t.Fatalf("want one inventory and one chat without retries, got %d and %d; error: %v", tags.Load(), calls.Load(), err)
			}
			after, readErr := os.ReadFile(decisionPath)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("thinking assessment changed the original decision")
			}
			if strings.Contains(stdout.String(), "private-thinking-marker") || strings.Contains(stderr.String(), "private-thinking-marker") {
				t.Fatal("raw thinking leaked to CLI output")
			}
			if tt.fail {
				if err == nil || !strings.Contains(err.Error(), "completion_envelope") || strings.Contains(err.Error(), "private-thinking-marker") || stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("off mode did not fail safely: %v", err)
				}
				if _, err := os.Stat(outPath); !os.IsNotExist(err) {
					t.Fatal("off mode saved a response with nonempty thinking")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var receipt map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || len(receipt) != 6 || string(receipt["schema_version"]) != `"detective-reason-assessment-saved/v1"` || string(receipt["state"]) != `"advisory_saved"` || string(receipt["authority_effect"]) != `"none"` || string(receipt["decision_digest"]) != `"`+decision.Digest+`"` {
				t.Fatal("thinking changed the narrow v1 stdout receipt")
			}
			saved, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(saved, []byte("private-thinking-marker")) || bytes.Contains(saved, []byte(`"message"`)) || bytes.Contains(saved, []byte(`"done_reason"`)) {
				t.Fatal("raw thinking or raw completion body was retained")
			}
			var metadata struct {
				SchemaVersion string `json:"schema_version"`
				PromptVersion string `json:"prompt_version"`
				Thinking      *struct {
					Mode    string `json:"mode"`
					Present bool   `json:"present"`
					Bytes   int    `json:"bytes"`
					SHA256  string `json:"sha256"`
				} `json:"thinking"`
			}
			if err := json.Unmarshal(saved, &metadata); err != nil || metadata.SchemaVersion != "detective-reason-assessment/v2" || metadata.PromptVersion != "detective-reason-adviser/v6" || metadata.Thinking == nil {
				t.Fatal("new assessment lacks versioned thinking metadata")
			}
			wantMode, wantHash := "off", ""
			if tt.think {
				wantMode = "on"
				sum := sha256.Sum256([]byte(tt.thinking))
				wantHash = hex.EncodeToString(sum[:])
			}
			if metadata.Thinking.Mode != wantMode || metadata.Thinking.Present != (tt.thinking != "") || metadata.Thinking.Bytes != len(tt.thinking) || metadata.Thinking.SHA256 != wantHash {
				t.Fatal("thinking metadata differs from explicit mode and exact response bytes")
			}
			assessment, err := pending.LoadReasonAssessment(outPath)
			if err != nil || assessment.DecisionDigest != decision.Digest || assessment.AuthorityEffect != "none" {
				t.Fatalf("new assessment cannot be reopened: %v", err)
			}
			server.Close()
			var questions bytes.Buffer
			if err := runReviewQuestions([]string{"-assessment", outPath}, &questions); err != nil || questions.String() != stderr.String() || tags.Load() != 1 || calls.Load() != 1 {
				t.Fatalf("offline questions changed display or reran a request: %v", err)
			}
			if strings.Contains(questions.String(), "private-thinking-marker") {
				t.Fatal("offline questions exposed raw thinking")
			}
		})
	}
}

func assessCLIFixture(t *testing.T) (string, pending.ReviewDecision) {
	t.Helper()
	reviewPath, bundle := reviewCLIFileFixture(t)
	decisionPath := filepath.Join(filepath.Dir(reviewPath), "decision.json")
	decision, err := pending.RecordReviewDecision(bundle, "admit", "私有理由：很厲害", bundle.Review.Display.ID, decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	return decisionPath, decision
}

func assessCLIServer(t *testing.T, content string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "fixture:local", "model": "fixture:local", "size": 1, "digest": strings.Repeat("a", 64), "details": map[string]any{"format": "gguf"}}}})
		case "/api/chat":
			calls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:local", "done": true, "done_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}})
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestReviewAssessPrivateSaveNarrowSummaryAndOfflineQuestions(t *testing.T) {
	decisionPath, decision := assessCLIFixture(t)
	before, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatal(err)
	}
	server, calls := assessCLIServer(t, `{"verdict":"follow_up_needed","summary":"需要補充理由。","concerns":[{"kind":"missing_justification","explanation":"理由只有讚嘆，未說明採納範圍。","question":"候選與引用如何支持你的採納範圍？\u001b[31m","anchor_ids":["a0000","a0001"]}]}`)
	outPath := filepath.Join(filepath.Dir(decisionPath), "advisory.json")
	args := []string{"-decision", decisionPath, "-base-url", server.URL, "-model", "fixture:local", "-out", outPath}
	var stdout, stderr bytes.Buffer
	if err := runReviewAssess(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil || len(summary) != 6 || string(summary["state"]) != `"advisory_saved"` || string(summary["authority_effect"]) != `"none"` || string(summary["decision_digest"]) != `"`+decision.Digest+`"` || string(summary["follow_up_count"]) != "1" {
		t.Fatalf("unexpected success summary: %s", stdout.Bytes())
	}
	for _, private := range []string{decision.Reason, "需要補充理由", "候選與引用", "exact_quote", "review_decision"} {
		if strings.Contains(stdout.String(), private) {
			t.Fatal("stdout included private advisory content")
		}
	}
	if strings.ContainsRune(stderr.String(), '\x1b') || !strings.Contains(stderr.String(), `\x1b`) || !strings.Contains(stderr.String(), "候選與引用") {
		t.Fatal("advisory display lost or failed to escape a question")
	}
	assessment, err := pending.LoadReasonAssessment(outPath)
	if err != nil || assessment.DecisionDigest != decision.Digest {
		t.Fatalf("private advisory not reloadable: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("advisory is not private")
	}
	server.Close()
	t.Setenv("OLLAMA_HOST", "https://must-not-contact.invalid")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/must-not-start")
	var questions bytes.Buffer
	if err := runReviewQuestions([]string{"-assessment", outPath}, &questions); err != nil || questions.String() != stderr.String() || calls.Load() != 1 {
		t.Fatalf("offline questions reran model or changed display: %v", err)
	}
	after, err := os.ReadFile(decisionPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reason assessment changed the original decision")
	}
	if err := runReviewQuestions([]string{"-assessment", decisionPath}, &questions); err == nil {
		t.Fatal("a decision was accepted as an advisory")
	}
}

func TestReviewAssessFailureHasSafeStageAndNoSuccess(t *testing.T) {
	for _, tt := range []struct{ name, content, stage string }{
		{"anchor", `{"verdict":"follow_up_needed","summary":"private-model-marker","concerns":[{"kind":"missing_justification","explanation":"private-model-marker","question":"private-model-marker","anchor_ids":["invented","a0001"]}]}`, "quote_grounding"},
		{"verdict", `{"verdict":"no_specific_concern","summary":"private-model-marker","concerns":[{"kind":"missing_justification","explanation":"private-model-marker","question":"private-model-marker","anchor_ids":["a0000","a0001"]}]}`, "advisory_contract"},
		{"text", `{"verdict":"follow_up_needed","summary":"private-model-marker","concerns":[{"kind":"missing_justification","explanation":"private-model-marker","question":"private-model-marker\n","anchor_ids":["a0000","a0001"]}]}`, "advisory_contract"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decisionPath, _ := assessCLIFixture(t)
			before, err := os.ReadFile(decisionPath)
			if err != nil {
				t.Fatal(err)
			}
			server, calls := assessCLIServer(t, tt.content)
			outPath := filepath.Join(filepath.Dir(decisionPath), "advisory.json")
			var stdout, stderr bytes.Buffer
			err = runReviewAssess([]string{"-decision", decisionPath, "-base-url", server.URL, "-model", "fixture:local", "-out", outPath}, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.stage) || strings.Contains(err.Error(), "private-model-marker") || stdout.Len() != 0 || stderr.Len() != 0 || calls.Load() != 1 {
				t.Fatalf("assessment failure was not safely diagnosed without retry: %v", err)
			}
			if _, err := os.Stat(outPath); !os.IsNotExist(err) {
				t.Fatal("failed model response was saved as a valid advisory")
			}
			after, err := os.ReadFile(decisionPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed advisory changed the exact decision")
			}
		})
	}
}
