package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const wmBaseline = "../../build/context-retention-20260910.HIkZNW"
const wmBaselineSHA = "fafa071ca30e206227860ec9a8c0f4deef3ef16e3002434f17899aa67ed3f2fa"

type wmMethod struct {
	Version         string             `json:"version"`
	UpstreamCommit  string             `json:"upstream_commit"`
	PromptDate      string             `json:"prompt_date"`
	Instruction     string             `json:"instruction"`
	MaxOutputTokens map[string]int     `json:"max_output_tokens"`
	Temperature     map[string]float64 `json:"temperature"`
	TopP            map[string]float64 `json:"top_p"`
}

type wmProjection struct {
	Headline string `json:"synthetic_headline"`
	Snippet  string `json:"context_snippet"`
	Input    string `json:"input"`
}

type wmPlan struct {
	Version        string              `json:"version"`
	Model          string              `json:"model"`
	Endpoint       string              `json:"endpoint"`
	MaxGenerations int                 `json:"max_generations"`
	Reasoning      string              `json:"reasoning_effort"`
	Retries        int                 `json:"retries"`
	Method         wmMethod            `json:"method"`
	Order          [][]string          `json:"order"`
	Cases          []segmentsPilotCase `json:"cases"`
	AInputs        []json.RawMessage   `json:"a_inputs"`
	WInputs        []wmProjection      `json:"w_inputs"`
	FileSHA256     map[string]string   `json:"file_sha256"`
	CodeSHA256     map[string]string   `json:"code_sha256"`
}

// This is only an adapter for the five frozen bodies, not an RSS/HTML parser.
// A split UTF-16 surrogate normalizes to U+FFFD; the frozen corpus has no pairs.
func wmSlice(text string, limit int) string {
	units := utf16.Encode([]rune(text))
	if len(units) > limit {
		units = units[:limit]
	}
	return string(utf16.Decode(units))
}

func wmProject(body string) wmProjection {
	clean := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " ")
	clean = strings.Join(strings.Fields(html.UnescapeString(clean)), " ")
	headline := clean
	if end := strings.Index(clean, ". "); end >= 0 {
		headline = clean[:end+1]
	}
	headline = wmSlice(headline, 500)
	snippet := wmSlice(clean, 400)
	if len(utf16.Encode([]rune(snippet))) < 40 || clean == headline {
		snippet = ""
	}
	input := "Choose one story and write its brief:\n1. " + headline
	if snippet != "" {
		input += "\n    Context: " + snippet
	}
	return wmProjection{Headline: headline, Snippet: snippet, Input: input}
}

func wmRoot(t *testing.T, phase string) *os.Root {
	t.Helper()
	if os.Getenv("DETECTIVE_WM_PHASE") != phase {
		t.Skip("explicit WorldMonitor simulation phase required")
	}
	dir := os.Getenv("DETECTIVE_WM_DIR")
	info, err := os.Lstat(dir)
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "worldmonitor-sim-20260910.") || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("explicit existing private 0700 WorldMonitor simulation directory required")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func wmMakePlan(t *testing.T, root *os.Root) wmPlan {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"baseline.PLAN.json", "baseline.assessment.plan.md", "github.raw.json", "github.capture.json", "assessment.plan.md", "method.plan.json"} {
		files[name] = pilotDigest(pilotRead(t, root, name))
	}
	var old contextPlan
	var method wmMethod
	if files["baseline.PLAN.json"] != wmBaselineSHA || json.Unmarshal(pilotRead(t, root, "baseline.PLAN.json"), &old) != nil || len(old.Cases) != 5 || old.Instructions["A"] != sourcepilot.SegmentInstruction || old.FileSHA256["github.raw.json"] != files["github.raw.json"] || old.FileSHA256["github.capture.json"] != files["github.capture.json"] || old.FileSHA256["assessment.plan.md"] != files["baseline.assessment.plan.md"] {
		t.Fatal("frozen context baseline or source changed")
	}
	if json.Unmarshal(pilotRead(t, root, "method.plan.json"), &method) != nil || method.Version == "" || method.UpstreamCommit != "af4e6da5642f0fe6ddba62fd52ebe6dbcc341ef5" || method.PromptDate != "2026-09-10" || strings.TrimSpace(method.Instruction) == "" || !reflect.DeepEqual(method.MaxOutputTokens, map[string]int{"A": 768, "W100": 100, "W768": 768}) || !reflect.DeepEqual(method.Temperature, map[string]float64{"A": 0, "W100": 0.3, "W768": 0.3}) || !reflect.DeepEqual(method.TopP, map[string]float64{"W100": 0.9, "W768": 0.9}) {
		t.Fatal("explicit pinned method, prompt and arm parameters required")
	}
	code := map[string]string{}
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			code[path] = pilotDigest(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"worldmonitor_sim_test.go", "public_source_context_test.go", "public_source_segments_test.go", "public_source_pilot_test.go", "model_test.go", "../../../../go.mod", "../../../../go.sum"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		code[path] = pilotDigest(raw)
	}
	plan := wmPlan{Version: "detective-worldmonitor-handler-simulation/v1", Model: pilotModel, Endpoint: pilotEndpoint, MaxGenerations: 15, Reasoning: "none", Method: method, Order: [][]string{{"A", "W100", "W768"}, {"W100", "W768", "A"}, {"W768", "A", "W100"}, {"A", "W768", "W100"}, {"W100", "A", "W768"}}, Cases: old.Cases, FileSHA256: files, CodeSHA256: code}
	for i, c := range plan.Cases {
		input := segmentsPilotInput(t, c.Body, c.Projection)
		if !wmSameInput(input, old.Inputs[i], old.InputSHA256[i]) {
			t.Fatal("baseline A projection changed")
		}
		plan.AInputs = append(plan.AInputs, json.RawMessage(input))
		plan.WInputs = append(plan.WInputs, wmProject(c.Body))
	}
	return plan
}

// PLAN.json pretty-prints embedded JSON; compare its compact representation
// while still binding the exact original request bytes through their digest.
func wmSameInput(input string, frozen json.RawMessage, digest string) bool {
	var compact bytes.Buffer
	return json.Compact(&compact, frozen) == nil && compact.String() == input && pilotDigest([]byte(input)) == digest
}

func TestWorldMonitorSimFrozenInputFormatting(t *testing.T) {
	input := `{"segments":[{"number":1,"text":"Atlas errors increased."}]}`
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(input), "", "  "); err != nil {
		t.Fatal(err)
	}
	if !wmSameInput(input, indented.Bytes(), pilotDigest([]byte(input))) || wmSameInput(strings.ReplaceAll(input, "increased", "decreased"), indented.Bytes(), pilotDigest([]byte(input))) || wmSameInput(input, indented.Bytes(), "wrong") {
		t.Fatal("formatting or exact-input digest binding changed")
	}
}

func TestWorldMonitorSimFreeze(t *testing.T) {
	root := wmRoot(t, "freeze")
	entries, err := os.ReadDir(root.Name())
	if err != nil || len(entries) != 2 || entries[0].Name() != "assessment.plan.md" || entries[1].Name() != "method.plan.json" {
		t.Fatal("fresh simulation directory must contain only assessment.plan.md and method.plan.json")
	}
	_ = pilotRead(t, root, "assessment.plan.md")
	_ = pilotRead(t, root, "method.plan.json")
	pilotSave(t, root, "freeze.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "max_generations": 15, "new_source_requests": 0}))
	baseline, err := os.OpenRoot(wmBaseline)
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	for _, name := range []string{"PLAN.json", "assessment.plan.md", "github.raw.json", "github.capture.json"} {
		target := name
		if name == "PLAN.json" || name == "assessment.plan.md" {
			target = "baseline." + name
		}
		pilotSave(t, root, target, pilotRead(t, baseline, name))
	}
	pilotSave(t, root, "PLAN.json", pilotJSON(t, wmMakePlan(t, root)))
	t.Log("frozen five existing cases, three arms; model calls=0 source requests=0")
}

// Preserve the underlying error while reusing the unchanged A response reader.
// Only W changes request fields; this wrapper never mutates the original request.
type wmModel struct {
	model.LLM
	method wmMethod
	arm    string
	err    error
}

func (m *wmModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	copyRequest := *req
	if m.arm != "A" {
		config := *req.Config
		temperature, topP := float32(m.method.Temperature[m.arm]), float32(m.method.TopP[m.arm])
		config.SystemInstruction = genai.NewContentFromText(m.method.Instruction, genai.RoleUser)
		config.Temperature, config.TopP = &temperature, &topP
		config.MaxOutputTokens = int32(m.method.MaxOutputTokens[m.arm])
		config.ResponseMIMEType, config.ResponseJsonSchema, config.ResponseSchema = "text/plain", nil, nil
		copyRequest.Config = &config
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range m.LLM.GenerateContent(ctx, &copyRequest, stream) {
			if err != nil {
				m.err = err
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func wmGenerate(ctx context.Context, llm model.LLM, method wmMethod, arm, input string) (string, string, error) {
	observed := &wmModel{LLM: llm, method: method, arm: arm}
	raw, err := segmentsPilotGenerate(ctx, observed, input)
	if err == nil {
		return raw, "", nil
	}
	var failure modelFailure
	if errors.As(observed.err, &failure) {
		return raw, string(failure), err
	}
	return raw, "model_response", err
}

func TestWorldMonitorSimRun(t *testing.T) {
	root := wmRoot(t, "run")
	plan := wmMakePlan(t, root)
	planRaw := pilotRead(t, root, "PLAN.json")
	if !bytes.Equal(planRaw, pilotJSON(t, plan)) {
		t.Fatal("frozen plan, inputs, code, assessment or method changed")
	}
	pilotSave(t, root, "run.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "plan_sha256": pilotDigest(planRaw), "max_generations": 15, "authority_effect": "none"}))
	setupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	llm, err := localModel(setupCtx, Settings{Mode: "local", Model: plan.Model, BaseURL: plan.Endpoint})
	cancel()
	if err != nil {
		pilotSave(t, root, "run.stopped.json", pilotJSON(t, map[string]any{"error_class": "model_inventory", "attempted_generations": 0, "authority_effect": "none"}))
		t.Fatal(err)
	}
	attempts, unavailable := 0, 0
	for i, c := range plan.Cases {
		for _, arm := range plan.Order[i] {
			input, instruction := plan.WInputs[i].Input, plan.Method.Instruction
			if arm == "A" {
				input, instruction = string(plan.AInputs[i]), sourcepilot.SegmentInstruction
			}
			prefix := fmt.Sprintf("case-%02d-%s", i+1, arm)
			start := time.Now()
			pilotSave(t, root, prefix+".started.json", pilotJSON(t, map[string]any{"case": c.Name, "arm": arm, "input": input, "input_sha256": pilotDigest([]byte(input)), "started_at": start.UTC(), "attempt": attempts + 1}))
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			attempts++
			raw, errorClass, err := wmGenerate(ctx, llm, plan.Method, arm, input)
			cancel()
			var result sourcepilot.SegmentSummary
			citations := []sourcepilot.Segment{}
			characters := utf8.RuneCountInString(raw)
			if err == nil && arm == "A" {
				result, err = sourcepilot.ParseSegmentSummary(raw, c.Body, c.Projection)
				if err != nil {
					errorClass = "output_contract"
				} else {
					characters = 0
					for _, item := range result.Items {
						citations = append(citations, c.Projection.Segments[item.Segment-1])
						characters += utf8.RuneCountInString(item.Summary)
					}
				}
			}
			errorText := ""
			if err != nil {
				unavailable++
				errorText = err.Error()
			}
			pilotSave(t, root, prefix+".result.json", pilotJSON(t, map[string]any{"case": c.Name, "role": c.Role, "arm": arm, "body_sha256": c.Projection.BodySHA256, "input_sha256": pilotDigest([]byte(input)), "instruction_sha256": pilotDigest([]byte(instruction)), "raw_model_text": raw, "summary_characters": characters, "wm_native_min_length_met": arm != "A" && len(utf16.Encode([]rune(strings.TrimSpace(raw)))) >= 20, "result": result, "citations": citations, "elapsed_ms": time.Since(start).Milliseconds(), "available": err == nil, "error_class": errorClass, "error": errorText, "human_review": "not_reviewed", "semantic_quality": "requires_review", "authority_effect": "none"}))
			t.Logf("case=%d arm=%s error_class=%s", i+1, arm, errorClass)
			if err != nil && errorClass != "model_incomplete" && errorClass != "output_contract" {
				pilotSave(t, root, "run.stopped.json", pilotJSON(t, map[string]any{"stopped_at": time.Now().UTC(), "attempted_generations": attempts, "error_class": errorClass, "authority_effect": "none"}))
				t.Fatal("unexpected model failure; remaining attempts not run")
			}
		}
	}
	pilotSave(t, root, "run.completed.json", pilotJSON(t, map[string]any{"completed_at": time.Now().UTC(), "attempted_generations": attempts, "unavailable": unavailable, "model_inventory_requests": 1, "new_source_requests": 0, "retries": 0, "human_review": "not_reviewed", "semantic_quality": "requires_review", "authority_effect": "none"}))
}

func TestWorldMonitorSimProjection(t *testing.T) {
	version := wmProject("GPT gpt-5.2, gpt-5.3-codex and gpt-5.4 failed. Other models worked.")
	if version.Headline != "GPT gpt-5.2, gpt-5.3-codex and gpt-5.4 failed." || version.Snippet == "" {
		t.Fatal("model version was mistaken for sentence boundary")
	}
	navigation := wmProject("View history<br/>Notification preferences<br />Support contact")
	if navigation.Headline != "View history Notification preferences Support contact" || navigation.Snippet != "" || strings.Contains(navigation.Input, "Context:") {
		t.Fatal("equal-title navigation must have no body snippet")
	}
	if wmProject("A &amp; B.<br> Short.").Headline != "A & B." || wmProject("A &amp; B.<br> Short.").Snippet != "" {
		t.Fatal("bounded cleaning or short-body filter changed")
	}
	if got := wmSlice(strings.Repeat("a", 398)+"🌍suffix", 400); got != strings.Repeat("a", 398)+"🌍" {
		t.Fatal("snippet does not use UTF-16 units")
	}
	if got := wmSlice(strings.Repeat("a", 399)+"🌍suffix", 400); got != strings.Repeat("a", 399)+"�" {
		t.Fatal("split surrogate normalization changed")
	}
}

func wmFakeMethod() wmMethod {
	return wmMethod{Instruction: "Synthetic brief instruction.", MaxOutputTokens: map[string]int{"A": 768, "W100": 100, "W768": 768}, Temperature: map[string]float64{"A": 0, "W100": 0.3, "W768": 0.3}, TopP: map[string]float64{"W100": 0.9, "W768": 0.9}}
}

func TestWorldMonitorSimFakeWire(t *testing.T) {
	var requests []map[string]any
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid fake request")
		}
		requests = append(requests, request)
		writeModelText(w, "Synthetic complete response.")
	})
	llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	input := `{"segments":[{"number":1,"text":"Atlas API reported errors."}]}`
	if _, err := segmentsPilotGenerate(t.Context(), contextPromptModel{LLM: llm, instruction: sourcepilot.SegmentInstruction}, input); err != nil {
		t.Fatal(err)
	}
	for _, arm := range []string{"A", "W100", "W768"} {
		if _, _, err := wmGenerate(t.Context(), llm, wmFakeMethod(), arm, input); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 4 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("A baseline wire changed")
	}
	for i, limit := range []float64{100, 768} {
		request := requests[i+2]
		reasoning, _ := request["reasoning"].(map[string]any)
		if request["instructions"] != wmFakeMethod().Instruction || request["max_output_tokens"] != limit || reasoning["effort"] != "none" || request["tools"] != nil {
			t.Fatalf("W controlled wire mismatch: %v", request)
		}
		temperature, _ := request["temperature"].(float64)
		topP, _ := request["top_p"].(float64)
		if temperature < 0.2999 || temperature > 0.3001 || topP < 0.8999 || topP > 0.9001 || strings.Contains(fmt.Sprint(request["text"]), "json_schema") {
			t.Fatal("W sampling or plain-text schema mismatch")
		}
	}
	delete(requests[2], "max_output_tokens")
	delete(requests[3], "max_output_tokens")
	if !reflect.DeepEqual(requests[2], requests[3]) {
		t.Fatal("W arms differ beyond output ceiling")
	}
}

func TestWorldMonitorSimFakeFailureNoRetry(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			calls := 0
			server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if incomplete {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "incomplete", "model": pilotModel, "output": []any{}})
				} else {
					http.Error(w, "synthetic failure", http.StatusInternalServerError)
				}
			})
			llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
			if err != nil {
				t.Fatal(err)
			}
			raw, class, err := wmGenerate(t.Context(), llm, wmFakeMethod(), "W100", "synthetic input")
			want := "model_http"
			if incomplete {
				want = "model_incomplete"
			}
			if err == nil || raw != "" || calls != 1 || class != want {
				t.Fatalf("failure accepted, unclassified or retried: calls=%d class=%s err=%v", calls, class, err)
			}
		})
	}
}
