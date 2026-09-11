package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

const eventBaseline = "../../build/context-retention-20260910.HIkZNW"
const eventBaselineSHA = "fafa071ca30e206227860ec9a8c0f4deef3ef16e3002434f17899aa67ed3f2fa"

type eventPlan struct {
	contextPlan
	EventPromptVersion string         `json:"event_prompt_version"`
	EventSchema        map[string]any `json:"event_schema"`
}

func eventRoot(t *testing.T, phase string) *os.Root {
	t.Helper()
	if os.Getenv("DETECTIVE_EVENT_PHASE") != phase {
		t.Skip("explicit event-overview experiment phase required")
	}
	dir := os.Getenv("DETECTIVE_EVENT_DIR")
	info, err := os.Lstat(dir)
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "event-overview-20260910.") || err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("explicit existing private 0700 event-overview directory required")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func eventMakePlan(t *testing.T, root *os.Root) eventPlan {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"baseline.PLAN.json", "baseline.assessment.plan.md", "github.raw.json", "github.capture.json", "assessment.plan.md"} {
		files[name] = pilotDigest(pilotRead(t, root, name))
	}
	var old contextPlan
	if files["baseline.PLAN.json"] != eventBaselineSHA || json.Unmarshal(pilotRead(t, root, "baseline.PLAN.json"), &old) != nil || len(old.Cases) != 5 || len(old.Inputs) != 5 || len(old.InputSHA256) != 5 || old.Instructions["A"] != sourcepilot.SegmentInstruction || old.FileSHA256["github.raw.json"] != files["github.raw.json"] || old.FileSHA256["github.capture.json"] != files["github.capture.json"] || old.FileSHA256["assessment.plan.md"] != files["baseline.assessment.plan.md"] {
		t.Fatal("frozen baseline, source capture or assessment changed")
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
	for _, path := range []string{"event_overview_experiment_test.go", "worldmonitor_sim_test.go", "public_source_context_test.go", "public_source_segments_test.go", "public_source_pilot_test.go", "model_test.go", "../sourcepilot/event_overview_test.go", "../../../../go.mod", "../../../../go.sum"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		code[path] = pilotDigest(raw)
	}
	plan := eventPlan{
		contextPlan:        contextPlan{Version: "detective-event-overview-experiment/v1", Model: pilotModel, Endpoint: pilotEndpoint, MaxGenerations: 10, MaxOutputTokens: 768, ReasoningEffort: "none", Order: []string{"AE", "EA", "AE", "EA", "AE"}, Instructions: map[string]string{"A": sourcepilot.SegmentInstruction, "E": sourcepilot.EventInstruction}, Schema: sourcepilot.SegmentSummarySchema(), FileSHA256: files, CodeSHA256: code, Cases: old.Cases},
		EventPromptVersion: sourcepilot.EventPromptVersion,
		EventSchema:        sourcepilot.EventOverviewSchema(),
	}
	for i, c := range plan.Cases {
		input := segmentsPilotInput(t, c.Body, c.Projection)
		if !wmSameInput(input, old.Inputs[i], old.InputSHA256[i]) {
			t.Fatal("complete baseline input changed")
		}
		plan.Inputs = append(plan.Inputs, json.RawMessage(input))
		plan.InputSHA256 = append(plan.InputSHA256, pilotDigest([]byte(input)))
	}
	return plan
}

// Freeze has no inventory, model or source request. All assessment data stays local.
func TestEventOverviewFreeze(t *testing.T) {
	root := eventRoot(t, "freeze")
	entries, err := os.ReadDir(root.Name())
	if err != nil || len(entries) != 1 || entries[0].Name() != "assessment.plan.md" {
		t.Fatal("fresh event directory must contain only assessment.plan.md")
	}
	_ = pilotRead(t, root, "assessment.plan.md")
	pilotSave(t, root, "freeze.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "max_generations": 10, "new_source_requests": 0}))
	baseline, err := os.OpenRoot(eventBaseline)
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
	pilotSave(t, root, "PLAN.json", pilotJSON(t, eventMakePlan(t, root)))
	t.Log("frozen five existing full inputs, two arms; model calls=0 source requests=0")
}

// Only the E instruction and JSON schema differ. The shared response reader,
// generation limits, transport, source bytes and caller request remain unchanged.
type eventModel struct {
	model.LLM
	arm string
	err error
}

func (m *eventModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	copyRequest := *req
	if m.arm == "E" {
		config := *req.Config
		config.SystemInstruction = genai.NewContentFromText(sourcepilot.EventInstruction, genai.RoleUser)
		config.ResponseJsonSchema = sourcepilot.EventOverviewSchema()
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

func eventGenerate(ctx context.Context, llm model.LLM, arm, input string) (string, string, error) {
	if arm != "A" && arm != "E" {
		return "", "invalid_arm", errors.New("invalid event experiment arm")
	}
	observed := &eventModel{LLM: llm, arm: arm}
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

func eventParse(c segmentsPilotCase, arm, raw string) (any, []sourcepilot.Segment, int, error) {
	citations := []sourcepilot.Segment{}
	if arm == "E" {
		result, err := sourcepilot.ParseEventOverview(raw, c.Body, c.Projection)
		if err != nil {
			return nil, citations, 0, err
		}
		for _, number := range result.Segments {
			citations = append(citations, c.Projection.Segments[number-1])
		}
		return result, citations, utf8.RuneCountInString(result.Overview), nil
	}
	result, err := sourcepilot.ParseSegmentSummary(raw, c.Body, c.Projection)
	if err != nil {
		return nil, citations, 0, err
	}
	characters := 0
	for _, item := range result.Items {
		citations = append(citations, c.Projection.Segments[item.Segment-1])
		characters += utf8.RuneCountInString(item.Summary)
	}
	return result, citations, characters, nil
}

func TestEventOverviewRun(t *testing.T) {
	root := eventRoot(t, "run")
	plan := eventMakePlan(t, root)
	planRaw := pilotRead(t, root, "PLAN.json")
	if !bytes.Equal(planRaw, pilotJSON(t, plan)) {
		t.Fatal("frozen plan, input, code, prompt, schema or assessment changed")
	}
	pilotSave(t, root, "run.started.json", pilotJSON(t, map[string]any{"started_at": time.Now().UTC(), "plan_sha256": pilotDigest(planRaw), "max_generations": 10, "authority_effect": "none"}))
	setupCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	llm, err := localModel(setupCtx, Settings{Mode: "local", Model: plan.Model, BaseURL: plan.Endpoint})
	cancel()
	if err != nil {
		pilotSave(t, root, "run.stopped.json", pilotJSON(t, map[string]any{"error_class": "model_inventory", "attempted_generations": 0, "authority_effect": "none"}))
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
			raw, errorClass, err := eventGenerate(ctx, llm, arm, input)
			cancel()
			var result any
			citations, characters := []sourcepilot.Segment{}, 0
			if err == nil {
				result, citations, characters, err = eventParse(c, arm, raw)
				if err != nil {
					errorClass = "output_contract"
				}
			}
			errorText := ""
			if err != nil {
				errorText = err.Error()
			}
			pilotSave(t, root, prefix+".result.json", pilotJSON(t, map[string]any{"case": c.Name, "role": c.Role, "arm": arm, "body_sha256": c.Projection.BodySHA256, "input_sha256": plan.InputSHA256[i], "instruction_sha256": pilotDigest([]byte(plan.Instructions[arm])), "raw_model_text": raw, "raw_characters": utf8.RuneCountInString(raw), "summary_characters": characters, "result": result, "citations": citations, "elapsed_ms": time.Since(start).Milliseconds(), "available": err == nil, "error_class": errorClass, "error": errorText, "human_review": "not_reviewed", "semantic_quality": "requires_review", "authority_effect": "none"}))
			t.Logf("case=%d arm=%s characters=%d error_class=%s", i+1, arm, characters, errorClass)
			if err != nil {
				pilotSave(t, root, "run.stopped.json", pilotJSON(t, map[string]any{"stopped_at": time.Now().UTC(), "attempted_generations": attempts, "error_class": errorClass, "new_source_requests": 0, "retries": 0, "human_review": "not_reviewed", "authority_effect": "none"}))
				t.Fatal("model or output contract failure; remaining attempts not run")
			}
		}
	}
	pilotSave(t, root, "run.completed.json", pilotJSON(t, map[string]any{"completed_at": time.Now().UTC(), "attempted_generations": attempts, "model_inventory_requests": 1, "new_source_requests": 0, "retries": 0, "human_review": "not_reviewed", "semantic_quality": "requires_review", "authority_effect": "none"}))
}

func TestEventOverviewFakeWire(t *testing.T) {
	var rawRequests [][]byte
	var requests []map[string]any
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		var request map[string]any
		if err != nil || json.Unmarshal(raw, &request) != nil {
			t.Error("invalid fake request")
		}
		rawRequests = append(rawRequests, raw)
		requests = append(requests, request)
		writeModelText(w, "Synthetic complete response.")
	})
	llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	input := `{"segments":[{"number":1,"text":"Atlas API reported errors."}]}`
	if _, err := segmentsPilotGenerate(t.Context(), llm, input); err != nil {
		t.Fatal(err)
	}
	for _, arm := range []string{"A", "E"} {
		if _, _, err := eventGenerate(t.Context(), llm, arm, input); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 3 || !bytes.Equal(rawRequests[0], rawRequests[1]) || requests[0]["instructions"] != sourcepilot.SegmentInstruction || requests[2]["instructions"] != sourcepilot.EventInstruction {
		t.Fatal("A baseline bytes or E instruction changed")
	}
	for i, schema := range []map[string]any{sourcepilot.SegmentSummarySchema(), sourcepilot.SegmentSummarySchema(), sourcepilot.EventOverviewSchema()} {
		// The existing ADK adapter sorts required field names on the wire.
		sort.Strings(schema["required"].([]string))
		text, _ := requests[i]["text"].(map[string]any)
		format, _ := text["format"].(map[string]any)
		reasoning, _ := requests[i]["reasoning"].(map[string]any)
		if !bytes.Equal(pilotJSON(t, schema), pilotJSON(t, format["schema"])) || requests[i]["temperature"] != float64(0) || requests[i]["max_output_tokens"] != float64(768) || reasoning["effort"] != "none" || requests[i]["tools"] != nil {
			t.Fatalf("wire schema, sampling, limit, thinking or tools changed: %s", rawRequests[i])
		}
	}
	baselineText := requests[0]["text"].(map[string]any)
	eventText := requests[2]["text"].(map[string]any)
	eventText["format"].(map[string]any)["schema"] = baselineText["format"].(map[string]any)["schema"]
	requests[2]["instructions"] = requests[0]["instructions"]
	if !reflect.DeepEqual(requests[0], requests[2]) {
		t.Fatal("E changed fields beyond instruction and JSON schema")
	}
}

func TestEventOverviewFakeFailuresNoRetry(t *testing.T) {
	for _, failure := range []string{"http", "incomplete", "invalid_json", "invalid_selection"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				switch failure {
				case "http":
					http.Error(w, "synthetic failure", http.StatusInternalServerError)
				case "incomplete":
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "incomplete", "model": pilotModel, "output": []any{}})
				case "invalid_json":
					writeModelText(w, `{"overview":`)
				case "invalid_selection":
					writeModelText(w, `{"overview":"Atlas API 錯誤增加。","segments":[2],"reason":""}`)
				}
			})
			llm, err := localModel(t.Context(), Settings{Mode: "local", Model: pilotModel, BaseURL: server.URL + "/v1"})
			if err != nil {
				t.Fatal(err)
			}
			c := segmentsPilotCase{Body: "Atlas API reported errors."}
			c.Projection, err = sourcepilot.SegmentBody(c.Body)
			if err != nil {
				t.Fatal(err)
			}
			raw, class, err := eventGenerate(t.Context(), llm, "E", segmentsPilotInput(t, c.Body, c.Projection))
			if failure == "invalid_json" || failure == "invalid_selection" {
				if err != nil || raw == "" {
					t.Fatal("invalid output raw text was not preserved")
				}
				_, _, _, err = eventParse(c, "E", raw)
			} else if raw != "" || class != "model_"+failure {
				t.Fatalf("transport failure was not rejected: class=%s", class)
			}
			if err == nil || calls != 1 {
				t.Fatalf("failure accepted or retried: calls=%d err=%v", calls, err)
			}
		})
	}
}
