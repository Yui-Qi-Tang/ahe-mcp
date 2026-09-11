package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const contextBaseline = "../../build/source-segments-20260910.ttgU4v"
const contextBaselineSHA = "a63f240b07bc039af99e75def736cdd261fd23955ff2c6083e5ab6732ae6d46c"
const contextAddition = "短摘要不是只保留片段的第一個事件。請在同一句中保留該片段明說且會改變主要脈絡的處置結果、影響邊界及原因已知或未知；可省略道歉、完整列舉等次要細節，不把先後改成因果。只翻譯導覽標籤不算事件資訊。若原文沒有明說，不補寫。"

// This test-only wrapper changes one request field without mutating its caller.
// The runtime model, source parser, persona and output limits stay unchanged.
type contextPromptModel struct {
	model.LLM
	instruction string
}

func (m contextPromptModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	copyRequest := *req
	copyConfig := *req.Config
	copyConfig.SystemInstruction = genai.NewContentFromText(m.instruction, genai.RoleUser)
	copyRequest.Config = &copyConfig
	return m.LLM.GenerateContent(ctx, &copyRequest, stream)
}

type contextPlan struct {
	Version         string              `json:"version"`
	Model           string              `json:"model"`
	Endpoint        string              `json:"endpoint"`
	MaxGenerations  int                 `json:"max_generations"`
	MaxOutputTokens int                 `json:"max_output_tokens"`
	Temperature     int                 `json:"temperature"`
	ReasoningEffort string              `json:"reasoning_effort"`
	RetryCount      int                 `json:"retry_count"`
	Order           []string            `json:"order"`
	Instructions    map[string]string   `json:"instructions"`
	Schema          map[string]any      `json:"schema"`
	FileSHA256      map[string]string   `json:"file_sha256"`
	CodeSHA256      map[string]string   `json:"code_sha256"`
	Cases           []segmentsPilotCase `json:"cases"`
	Inputs          []json.RawMessage   `json:"inputs"`
	InputSHA256     []string            `json:"input_sha256"`
}

func contextRoot(t *testing.T, phase string) *os.Root {
	t.Helper()
	if os.Getenv("DETECTIVE_CONTEXT_PHASE") != phase {
		t.Skip("explicit context-retention phase required")
	}
	dir := os.Getenv("DETECTIVE_CONTEXT_DIR")
	info, err := os.Lstat(dir)
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "context-retention-20260910.") || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("explicit existing private 0700 context-retention directory required")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func contextMakePlan(t *testing.T, root *os.Root) contextPlan {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"baseline.PLAN.json", "github.raw.json", "github.capture.json", "assessment.plan.md"} {
		files[name] = pilotDigest(pilotRead(t, root, name))
	}
	var old segmentsPilotPlan
	var capture pilotCapture
	raw := pilotRead(t, root, "github.raw.json")
	if files["baseline.PLAN.json"] != contextBaselineSHA || json.Unmarshal(pilotRead(t, root, "baseline.PLAN.json"), &old) != nil || len(old.Cases) != 5 || old.SourceSHA256 != pilotDigest(raw) || json.Unmarshal(pilotRead(t, root, "github.capture.json"), &capture) != nil || capture.SHA256 != old.SourceSHA256 || capture.Bytes != len(raw) || capture.Status != 200 || capture.Error != "" {
		t.Fatal("frozen baseline or source capture changed")
	}
	cases := []segmentsPilotCase{old.Cases[3], old.Cases[4],
		{Name: "synthetic_region_recovery_unknown", Role: "new_synthetic", Body: "Atlas API reported increased errors. The team reverted a configuration change, and requests outside the north region returned to normal.\nAtlas API requests in the north region remain delayed. The cause remains unconfirmed."},
		{Name: "synthetic_scope_unrecovered_navigation", Role: "new_synthetic", Body: "Orion Search is experiencing degraded performance. Other Orion services are not affected.\nThe team is investigating; no recovery has been reported.\nView history. Notification preferences. Contact support."},
		old.Cases[2],
	}
	cases[0].Role, cases[1].Role, cases[4].Role = "seen_public", "seen_public", "seen_synthetic"
	plan := contextPlan{Version: "detective-context-retention/v1", Model: pilotModel, Endpoint: pilotEndpoint, MaxGenerations: 10, MaxOutputTokens: 768, ReasoningEffort: "none", Order: []string{"AB", "BA", "AB", "BA", "AB"}, Instructions: map[string]string{"A": sourcepilot.SegmentInstruction, "B": sourcepilot.SegmentInstruction + "\n" + contextAddition}, Schema: sourcepilot.SegmentSummarySchema(), FileSHA256: files, CodeSHA256: segmentsPilotCode(t), Cases: cases}
	for _, path := range []string{"public_source_context_test.go", "../../../../go.mod", "../../../../go.sum"} {
		code, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		plan.CodeSHA256[path] = pilotDigest(code)
	}
	for i := range plan.Cases {
		c := &plan.Cases[i]
		c.Expected = "human_review_only"
		projection, err := sourcepilot.SegmentBody(c.Body)
		if err != nil {
			t.Fatal(err)
		}
		c.Projection = projection
		input := segmentsPilotInput(t, c.Body, projection)
		plan.Inputs = append(plan.Inputs, json.RawMessage(input))
		plan.InputSHA256 = append(plan.InputSHA256, pilotDigest([]byte(input)))
	}
	return plan
}

// Freeze copies local artifacts only; the assessment is never model input.
func TestContextRetentionFreeze(t *testing.T) {
	root := contextRoot(t, "freeze")
	entries, err := os.ReadDir(root.Name())
	if err != nil || len(entries) != 1 || entries[0].Name() != "assessment.plan.md" {
		t.Fatal("fresh context directory must contain only assessment.plan.md")
	}
	_ = pilotRead(t, root, "assessment.plan.md")
	pilotSave(t, root, "freeze.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "max_generations": 10, "new_source_requests": 0}))
	baseline, err := os.OpenRoot(contextBaseline)
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	for _, name := range []string{"PLAN.json", "github.raw.json", "github.capture.json"} {
		target := name
		if name == "PLAN.json" {
			target = "baseline.PLAN.json"
		}
		pilotSave(t, root, target, pilotRead(t, baseline, name))
	}
	pilotSave(t, root, "PLAN.json", pilotJSON(t, contextMakePlan(t, root)))
	t.Log("frozen five cases, two instructions; model calls=0 new source requests=0")
}

func TestContextRetentionRun(t *testing.T) {
	root := contextRoot(t, "run")
	plan := contextMakePlan(t, root)
	planRaw := pilotRead(t, root, "PLAN.json")
	if !bytes.Equal(planRaw, pilotJSON(t, plan)) {
		t.Fatal("frozen complete plan, source, assessment or code changed")
	}
	pilotSave(t, root, "run.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "plan_sha256": pilotDigest(planRaw), "max_generations": 10, "new_source_requests": 0, "authority_effect": "none"}))
	setupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	llm, err := localModel(setupCtx, Settings{Mode: "local", Model: plan.Model, BaseURL: plan.Endpoint})
	cancel()
	if err != nil {
		pilotSave(t, root, "run.stopped.json", pilotJSON(t, map[string]any{"error_class": "model_inventory", "error": err.Error(), "attempted_generations": 0, "authority_effect": "none"}))
		t.Fatal(err)
	}
	attempts := 0
	for i, c := range plan.Cases {
		for _, arm := range strings.Split(plan.Order[i], "") {
			input := segmentsPilotInput(t, c.Body, c.Projection)
			prefix := fmt.Sprintf("case-%02d-%s", i+1, arm)
			start := time.Now()
			pilotSave(t, root, prefix+".started.json", pilotJSON(t, map[string]any{"case": c.Name, "arm": arm, "input": json.RawMessage(input), "input_sha256": plan.InputSHA256[i], "started_at": start.UTC(), "attempt": attempts + 1}))
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			attempts++
			raw, err := segmentsPilotGenerate(ctx, contextPromptModel{LLM: llm, instruction: plan.Instructions[arm]}, input)
			cancel()
			elapsed := time.Since(start).Milliseconds()
			var result sourcepilot.SegmentSummary
			errorClass, errorText := "", ""
			if err != nil {
				errorClass = "model_response"
			} else {
				result, err = sourcepilot.ParseSegmentSummary(raw, c.Body, c.Projection)
				if err != nil {
					errorClass = "output_contract"
				}
			}
			citations := []sourcepilot.Segment{}
			characters := 0
			if err == nil {
				for _, item := range result.Items {
					citations = append(citations, c.Projection.Segments[item.Segment-1])
					characters += utf8.RuneCountInString(item.Summary)
				}
			} else {
				errorText = err.Error()
			}
			pilotSave(t, root, prefix+".result.json", pilotJSON(t, map[string]any{"case": c.Name, "role": c.Role, "arm": arm, "body_sha256": c.Projection.BodySHA256, "input_sha256": plan.InputSHA256[i], "instruction_sha256": pilotDigest([]byte(plan.Instructions[arm])), "raw_model_text": raw, "raw_characters": utf8.RuneCountInString(raw), "summary_characters": characters, "result": result, "citations": citations, "elapsed_ms": elapsed, "error_class": errorClass, "error": errorText, "human_review": "not_reviewed", "semantic_quality": "requires_review", "authority_effect": "none"}))
			t.Logf("case=%d arm=%s items=%d elapsed_ms=%d error_class=%s", i+1, arm, len(result.Items), elapsed, errorClass)
			if err != nil {
				pilotSave(t, root, "run.stopped.json", pilotJSON(t, map[string]any{"stopped_at": time.Now().UTC(), "attempted_generations": attempts, "error_class": errorClass, "human_review": "not_reviewed", "authority_effect": "none"}))
				t.Fatal(errorText)
			}
		}
	}
	pilotSave(t, root, "run.completed.json", pilotJSON(t, map[string]any{"completed_at": time.Now().UTC(), "attempted_generations": attempts, "model_inventory_requests": 1, "new_source_requests": 0, "retries": 0, "semantic_quality": "requires_review", "human_review": "not_reviewed", "authority_effect": "none"}))
}

func TestContextRetentionFakeWire(t *testing.T) {
	var requests []map[string]any
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid fake request")
		}
		requests = append(requests, request)
		writeModelText(w, `{"items":[{"segment":1,"summary":"Atlas API 錯誤增加。"}],"reason":""}`)
	})
	llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []model.LLM{llm, contextPromptModel{LLM: llm, instruction: sourcepilot.SegmentInstruction}, contextPromptModel{LLM: llm, instruction: sourcepilot.SegmentInstruction + "\n" + contextAddition}} {
		if _, err := segmentsPilotGenerate(t.Context(), candidate, `{"segments":[{"number":1,"text":"Atlas API reported increased errors."}]}`); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 3 || !reflect.DeepEqual(requests[0], requests[1]) || requests[0]["instructions"] != sourcepilot.SegmentInstruction || requests[2]["instructions"] != sourcepilot.SegmentInstruction+"\n"+contextAddition {
		t.Fatal("baseline wire or instruction mismatch")
	}
	delete(requests[0], "instructions")
	delete(requests[2], "instructions")
	if !reflect.DeepEqual(requests[0], requests[2]) {
		t.Fatal("context instruction changed other wire fields")
	}
}

func TestContextRetentionFakeErrorNoRetry(t *testing.T) {
	calls := 0
	server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "synthetic failure", http.StatusInternalServerError)
	})
	llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := segmentsPilotGenerate(t.Context(), contextPromptModel{LLM: llm, instruction: sourcepilot.SegmentInstruction + "\n" + contextAddition}, "synthetic input")
	if err == nil || raw != "" || calls != 1 {
		t.Fatalf("failed response accepted or retried: calls=%d err=%v", calls, err)
	}
}

func TestContextRetentionParserStillRejectsInvalidSelections(t *testing.T) {
	body := "Atlas API reported increased errors."
	projection, err := sourcepilot.SegmentBody(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"items":[{"segment":1,"summary":"A"},{"segment":1,"summary":"B"}],"reason":""}`, `{"items":[{"segment":2,"summary":"A"}],"reason":""}`} {
		if _, err := sourcepilot.ParseSegmentSummary(raw, body, projection); err == nil {
			t.Fatal("invalid segment selection accepted")
		}
	}
}
