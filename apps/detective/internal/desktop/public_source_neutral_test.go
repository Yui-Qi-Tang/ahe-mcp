package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const neutralBaselineVersion = "detective-neutral-baseline/v1"
const neutralRunVersion = "detective-neutral-run/v1"
const neutralAssessmentSHA = "3fa3e30e67b811441532591ec5253aab63521aaa6af68ca61642d42f650d923b"

type neutralCase struct {
	Name       string                    `json:"name"`
	Role       string                    `json:"role"`
	OriginRole string                    `json:"origin_role"`
	Body       string                    `json:"body"`
	Input      json.RawMessage           `json:"input"`
	Expected   string                    `json:"expected"`
	Projection sourcepilot.SegmentedBody `json:"projection"`
}

// This portable task artifact contains no execution model, endpoint, code hash
// or generated answer. Expected outcomes and provenance are not model inputs.
type neutralBaseline struct {
	Version           string                       `json:"version"`
	TaskVersion       string                       `json:"task_version"`
	SystemInstruction string                       `json:"system_instruction"`
	PromptSHA256      string                       `json:"prompt_sha256"`
	Schema            json.RawMessage              `json:"schema"`
	SchemaSHA256      string                       `json:"schema_sha256"`
	SourceSHA256      string                       `json:"source_sha256"`
	Capture           pilotCapture                 `json:"source_capture"`
	Cases             []neutralCase                `json:"cases"`
	Updates           []sourcepilot.IncidentUpdate `json:"selected_updates"`
}

type neutralProfile struct {
	Name               string  `json:"name"`
	Model              string  `json:"model"`
	Endpoint           string  `json:"endpoint"`
	ReasoningEffort    string  `json:"reasoning_effort"`
	Temperature        float64 `json:"temperature"`
	MaxOutputTokens    int     `json:"max_output_tokens"`
	CaseTimeoutSeconds int     `json:"case_timeout_seconds"`
	MaxGenerations     int     `json:"max_generations"`
}

type neutralPlan struct {
	Version          string            `json:"version"`
	BaselineSHA256   string            `json:"baseline_sha256"`
	PromptSHA256     string            `json:"prompt_sha256"`
	SchemaSHA256     string            `json:"schema_sha256"`
	CasesSHA256      string            `json:"cases_sha256"`
	SourceSHA256     string            `json:"source_sha256"`
	AssessmentSHA256 string            `json:"assessment_sha256"`
	CodeSHA256       map[string]string `json:"code_sha256"`
	Profile          neutralProfile    `json:"profile"`
}

func neutralMakeBaseline(t *testing.T, prior segmentsPilotPlan, capture pilotCapture) neutralBaseline {
	t.Helper()
	if len(prior.Cases) != 5 || len(prior.Updates) != 2 {
		t.Fatal("exactly five already-seen cases and two source updates required")
	}
	schema, err := json.Marshal(sourcepilot.SegmentSummarySchema())
	if err != nil {
		t.Fatal(err)
	}
	cases := make([]neutralCase, len(prior.Cases))
	for i, c := range prior.Cases {
		cases[i] = neutralCase{Name: c.Name, Role: "already_seen", OriginRole: c.Role, Body: c.Body, Input: json.RawMessage(segmentsPilotInput(t, c.Body, c.Projection)), Expected: c.Expected, Projection: c.Projection}
	}
	return neutralBaseline{Version: neutralBaselineVersion, TaskVersion: sourcepilot.NeutralPromptVersion, SystemInstruction: sourcepilot.NeutralInstruction, PromptSHA256: pilotDigest([]byte(sourcepilot.NeutralInstruction)), Schema: schema, SchemaSHA256: pilotDigest(schema), SourceSHA256: prior.SourceSHA256, Capture: capture, Cases: cases, Updates: prior.Updates}
}

func neutralArtifacts(t *testing.T, raw, captureRaw []byte) (neutralBaseline, neutralPlan) {
	t.Helper()
	prior := segmentsPilotMakePlan(t, raw)
	var capture pilotCapture
	if json.Unmarshal(captureRaw, &capture) != nil || capture.SHA256 != lifecycleSourceSHA || capture.URL != pilotSources["github"] || capture.Status != http.StatusOK || capture.Bytes != len(raw) || capture.Error != "" {
		t.Fatal("frozen source capture does not match the bounded snapshot")
	}
	if _, err := time.Parse(time.RFC3339Nano, capture.ObservedAt); err != nil {
		t.Fatal("source observation time unavailable")
	}
	baseline := neutralMakeBaseline(t, prior, capture)
	code := prior.CodeSHA256
	for _, path := range []string{"public_source_neutral_test.go", "../sourcepilot/neutral.go", "../sourcepilot/neutral_test.go"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		code[path] = pilotDigest(content)
	}
	profile := neutralProfile{Name: "explicit-local-e4b/v1", Model: pilotModel, Endpoint: pilotEndpoint, ReasoningEffort: "none", Temperature: 0, MaxOutputTokens: 768, CaseTimeoutSeconds: 120, MaxGenerations: 5}
	plan := neutralPlan{Version: neutralRunVersion, BaselineSHA256: pilotDigest(pilotJSON(t, baseline)), PromptSHA256: baseline.PromptSHA256, SchemaSHA256: baseline.SchemaSHA256, CasesSHA256: pilotDigest(pilotJSON(t, baseline.Cases)), SourceSHA256: lifecycleSourceSHA, AssessmentSHA256: neutralAssessmentSHA, CodeSHA256: code, Profile: profile}
	return baseline, plan
}

func neutralRoot(t *testing.T, phase string) *os.Root {
	t.Helper()
	if os.Getenv("DETECTIVE_SOURCE_NEUTRAL") != phase {
		t.Skip("explicit neutral-baseline phase required; no model or source call")
	}
	dir := os.Getenv("DETECTIVE_NEUTRAL_DIR")
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "source-neutral-20260910.") {
		t.Fatal("explicit fresh source-neutral directory required")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("source-neutral directory must be an existing private 0700 directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if phase == "freeze" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("freeze requires an empty directory; partial attempts are not reusable")
		}
	}
	return root
}

// Markers are exclusive and durable even when a later step fails. A partial run
// is retained for inspection, never resumed or retried in the same directory.
func neutralStart(root *os.Root, name string, raw []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func TestSourceNeutralFreeze(t *testing.T) {
	root := neutralRoot(t, "freeze")
	if err := neutralStart(root, "freeze.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "planned_generations": 5, "new_source_requests": 0})); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(lifecycleBaseline)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	raw, captureRaw := pilotRead(t, source, "github.raw.json"), pilotRead(t, source, "github.capture.json")
	baseline, plan := neutralArtifacts(t, raw, captureRaw)
	assessment, err := os.ReadFile("../../docs/PUBLIC_SOURCE_NEUTRAL_20260910.md")
	if err != nil || pilotDigest(assessment) != neutralAssessmentSHA {
		t.Fatal("pre-generation assessment plan changed")
	}
	pilotSave(t, root, "github.raw.json", raw)
	pilotSave(t, root, "github.capture.json", captureRaw)
	pilotSave(t, root, "assessment.plan.md", assessment)
	pilotSave(t, root, "baseline.json", pilotJSON(t, baseline))
	pilotSave(t, root, "PLAN.json", pilotJSON(t, plan))
	t.Log("frozen already_seen=5; source requests=0 model calls=0; baseline separate from explicit E4B execution profile")
}

// Keep historical generators unchanged. This is the same sealed-output request
// and response contract, with only the opening persona removed from instruction.
func neutralGenerate(ctx context.Context, llm model.LLM, input string) (string, error) {
	temperature := float32(0)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(input, genai.RoleUser)}, Config: &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(sourcepilot.NeutralInstruction, genai.RoleUser), Temperature: &temperature, MaxOutputTokens: 768, ResponseMIMEType: "application/json", ResponseJsonSchema: sourcepilot.SegmentSummarySchema(),
	}}
	count := 0
	var text strings.Builder
	for response, err := range llm.GenerateContent(ctx, req, false) {
		count++
		if count != 1 || err != nil || response == nil || response.Partial || response.Interrupted || response.ErrorCode != "" || response.ErrorMessage != "" || response.FinishReason != genai.FinishReasonStop || response.Content == nil || response.Content.Role != genai.RoleModel {
			return "", errors.New("model response incomplete or unsupported")
		}
		for _, part := range response.Content.Parts {
			if part == nil || part.Thought || part.FunctionCall != nil || part.FunctionResponse != nil || part.ToolCall != nil || part.ToolResponse != nil || part.InlineData != nil || part.FileData != nil || part.ExecutableCode != nil || part.CodeExecutionResult != nil {
				return "", errors.New("model response is not plain text")
			}
			if text.Len()+len(part.Text) > 16<<10 {
				return "", errors.New("model text exceeds bound")
			}
			text.WriteString(part.Text)
		}
	}
	if count != 1 || ctx.Err() != nil {
		return "", errors.New("model returned no complete response")
	}
	return text.String(), nil
}

func TestSourceNeutralRun(t *testing.T) {
	root := neutralRoot(t, "run")
	baseline, plan := neutralArtifacts(t, pilotRead(t, root, "github.raw.json"), pilotRead(t, root, "github.capture.json"))
	baselineRaw, planRaw := pilotRead(t, root, "baseline.json"), pilotRead(t, root, "PLAN.json")
	if !bytes.Equal(baselineRaw, pilotJSON(t, baseline)) || !bytes.Equal(planRaw, pilotJSON(t, plan)) || pilotDigest(pilotRead(t, root, "assessment.plan.md")) != neutralAssessmentSHA {
		t.Fatal("frozen baseline, source, assessment, code or execution profile changed")
	}
	if err := neutralStart(root, "run.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "plan_sha256": pilotDigest(planRaw), "baseline_sha256": plan.BaselineSHA256, "max_generations": 5, "new_source_requests": 0, "human_review": "not_reviewed", "authority_effect": "none"})); err != nil {
		t.Fatal(err)
	}
	setupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	llm, err := localModel(setupCtx, Settings{Mode: "local", Model: plan.Profile.Model, BaseURL: plan.Profile.Endpoint})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	for i, c := range baseline.Cases {
		t.Run(c.Name, func(t *testing.T) {
			input := segmentsPilotInput(t, c.Body, c.Projection)
			binding := map[string]any{"baseline_sha256": plan.BaselineSHA256, "plan_sha256": pilotDigest(planRaw), "body_sha256": c.Projection.BodySHA256, "input_sha256": pilotDigest([]byte(input))}
			if i >= 3 {
				update := baseline.Updates[i-3]
				binding["source_sha256"], binding["incident_id"], binding["update_id"] = baseline.SourceSHA256, update.IncidentID, update.ID
			}
			if err := neutralStart(root, fmt.Sprintf("case-%02d.started.json", i+1), pilotJSON(t, map[string]any{"case": c.Name, "role": c.Role, "origin_role": c.OriginRole, "input": json.RawMessage(input), "binding": binding, "started_at": time.Now().UTC()})); err != nil {
				t.Fatal(err)
			}
			if attempts >= plan.Profile.MaxGenerations {
				t.Fatal("generation budget exhausted")
			}
			start := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), time.Duration(plan.Profile.CaseTimeoutSeconds)*time.Second)
			attempts++
			raw, err := neutralGenerate(ctx, llm, input)
			cancel()
			elapsed := time.Since(start).Milliseconds()
			var result sourcepilot.SegmentSummary
			errorClass := ""
			if err != nil {
				errorClass = "model_response"
			} else if result, err = sourcepilot.ParseSegmentSummary(raw, c.Body, c.Projection); err != nil {
				errorClass = "output_contract"
			}
			outcome, structural, languageCheck, qualityExpectation := "invalid", "failed", "not_run", "not_run"
			citations, language := []sourcepilot.Segment{}, []string{}
			if err == nil {
				structural, outcome, qualityExpectation = "passed", "extracted", "requires_review"
				if len(result.Items) == 0 {
					outcome = "abstained"
				}
				language = segmentsPilotLanguage(result, c.Projection)
				languageCheck = "passed"
				if len(language) != 0 {
					languageCheck = "failed"
				}
				for _, item := range result.Items {
					citations = append(citations, c.Projection.Segments[item.Segment-1])
				}
				if c.Expected != "inspect" {
					qualityExpectation = "passed"
					if outcome != c.Expected {
						qualityExpectation, errorClass, err = "failed", "quality_expectation", errors.New("synthetic outcome mismatch")
					}
				}
				if err == nil && languageCheck == "failed" {
					errorClass, err = "language_check", errors.New("finite language checks failed")
				}
			}
			errorText := ""
			if err != nil {
				errorText = err.Error()
			}
			report := map[string]any{"case": c.Name, "role": c.Role, "origin_role": c.OriginRole, "expected": c.Expected, "outcome": outcome, "structural_check": structural, "quality_expectation": qualityExpectation, "elapsed_ms": elapsed, "raw_model_text": raw, "result": result, "citations": citations, "binding": binding, "finite_language_check": languageCheck, "language_issues": language, "error_class": errorClass, "error": errorText, "human_review": "not_reviewed", "semantic_quality": "not_automatically_proven", "authority_effect": "none", "comparison": "historical_descriptive_not_causal"}
			pilotSave(t, root, fmt.Sprintf("case-%02d.result.json", i+1), pilotJSON(t, report))
			t.Logf("case=%s role=%s outcome=%s structural=%s quality_expectation=%s language=%s elapsed_ms=%d", c.Name, c.Role, outcome, structural, qualityExpectation, languageCheck, elapsed)
			if err != nil {
				t.Error(errorText)
			}
		})
	}
	if attempts != 5 {
		t.Error("not all five frozen cases were attempted")
	}
	pilotSave(t, root, "run.completed.json", pilotJSON(t, map[string]any{"completed_at": time.Now().UTC(), "baseline_sha256": plan.BaselineSHA256, "planned_generations": 5, "attempted_generations": attempts, "automated_checks_passed": !t.Failed(), "semantic_quality": "requires_review", "human_review": "not_reviewed", "authority_effect": "none", "comparison": "historical_descriptive_not_causal"}))
}

func TestSourceNeutralPortableBaseline(t *testing.T) {
	updates, err := segmentsPilotSelect(segmentsPilotFixture())
	if err != nil {
		t.Fatal(err)
	}
	cases, err := segmentsPilotCases(updates)
	if err != nil {
		t.Fatal(err)
	}
	prior := segmentsPilotPlan{SourceSHA256: lifecycleSourceSHA, Cases: cases, Updates: updates, Model: "execution-model-must-not-leak", Endpoint: "execution-endpoint-must-not-leak", CodeSHA256: map[string]string{"execution-code-must-not-leak": "hash"}}
	baseline := neutralMakeBaseline(t, prior, pilotCapture{SHA256: lifecycleSourceSHA})
	raw := pilotJSON(t, baseline)
	var roundtrip neutralBaseline
	if json.Unmarshal(raw, &roundtrip) != nil || len(roundtrip.Cases) != 5 || len(roundtrip.Updates) != 2 || roundtrip.SystemInstruction != strings.TrimPrefix(sourcepilot.SegmentInstruction, "你是來源閱讀助手，") {
		t.Fatal("portable neutral task is incomplete or prompt changed beyond persona")
	}
	for _, forbidden := range []string{"execution-model-must-not-leak", "execution-endpoint-must-not-leak", "execution-code-must-not-leak", `"code_sha256"`, `"raw_model_text"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("execution state leaked into portable baseline")
		}
	}
	for i, c := range roundtrip.Cases {
		if c.Role != "already_seen" || c.OriginRole != cases[i].Role || c.Body != cases[i].Body || c.Expected != cases[i].Expected || !reflect.DeepEqual(c.Projection, cases[i].Projection) {
			t.Fatal("case text, order, expectation, exposure or projection changed")
		}
		var compact bytes.Buffer
		if json.Compact(&compact, c.Input) != nil || compact.String() != segmentsPilotInput(t, c.Body, c.Projection) {
			t.Fatal("portable input is not the exact body-only request")
		}
	}
}

func TestSourceNeutralFakeWireOnlyRemovesPersona(t *testing.T) {
	var received []map[string]any
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid fake request")
			return
		}
		received = append(received, request)
		writeModelText(w, `{"items":[],"reason":"合成測試回覆"}`)
	})
	llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := segmentsPilotSelect(segmentsPilotFixture())
	if err != nil {
		t.Fatal(err)
	}
	cases, err := segmentsPilotCases(updates)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		input := segmentsPilotInput(t, c.Body, c.Projection)
		if _, err := segmentsPilotGenerate(t.Context(), llm, input); err != nil {
			t.Fatal(err)
		}
		if _, err := neutralGenerate(t.Context(), llm, input); err != nil {
			t.Fatal(err)
		}
		if len(received) != (i+1)*2 {
			t.Fatal("each fake case must have exactly one request per generator")
		}
		old, current := received[i*2], received[i*2+1]
		if old["instructions"] != sourcepilot.SegmentInstruction || current["instructions"] != sourcepilot.NeutralInstruction || current["model"] != pilotModel || current["temperature"] != float64(0) || current["max_output_tokens"] != float64(768) {
			t.Fatal("neutral wire instruction or fixed generation profile changed")
		}
		old["instructions"] = sourcepilot.NeutralInstruction
		if !reflect.DeepEqual(old, current) {
			t.Fatal("wire request differs beyond exact opening-persona removal")
		}
		requestRaw := pilotJSON(t, current)
		var request struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Tools []json.RawMessage `json:"tools"`
			Text  struct {
				Format struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"format"`
			} `json:"text"`
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if json.Unmarshal(requestRaw, &request) != nil || request.Reasoning.Effort != "none" || len(request.Tools) != 0 || len(request.Input) != 1 || len(request.Input[0].Content) != 1 || request.Input[0].Content[0].Text != input {
			t.Fatal("reasoning, tools or exact body-only input changed")
		}
		var actualSchema, expectedSchema any
		if json.Unmarshal(request.Text.Format.Schema, &actualSchema) != nil || json.Unmarshal(pilotJSON(t, sourcepilot.SegmentSummarySchema()), &expectedSchema) != nil || !reflect.DeepEqual(actualSchema, expectedSchema) {
			t.Fatal("closed segment schema changed on the wire")
		}
	}
}

func TestSourceNeutralFakeRejectsWithoutRetry(t *testing.T) {
	for _, mode := range []string{"error", "tool", "multiple_finals"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				switch mode {
				case "error":
					http.Error(w, "synthetic failure", http.StatusInternalServerError)
				case "tool":
					_, _ = io.WriteString(w, `{"model":"gemma4:e4b-it-qat","status":"completed","output":[{"type":"function_call","name":"writer"}]}`)
				case "multiple_finals":
					message := map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "{}"}}}
					_ = json.NewEncoder(w).Encode(map[string]any{"model": pilotModel, "status": "completed", "output": []any{message, message}})
				}
			})
			llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := neutralGenerate(t.Context(), llm, `{"segments":[{"number":1,"text":"Synthetic source."}]}`)
			if err == nil || raw != "" || calls != 1 {
				t.Fatalf("unsupported response accepted or retried: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestSourceNeutralMarkerRefusesReplay(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"freeze.started.json", "run.started.json", "case-01.started.json"} {
		original := []byte("{\"attempt\":1}\n")
		if err := neutralStart(root, name, original); err != nil {
			t.Fatal(err)
		}
		if err := neutralStart(root, name, []byte("{\"attempt\":2}\n")); !errors.Is(err, os.ErrExist) {
			t.Fatal("marker did not reject replay with an existing-file error")
		}
		if !bytes.Equal(pilotRead(t, root, name), original) {
			t.Fatal("replay modified the original private marker")
		}
	}
}
