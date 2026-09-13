package desktop

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

const taskTicketHash = "d2b109ee6749e434b55a8f5aace7f84444bc1ee09e8978e6963133f151310d6b"

func taskTicket(t *testing.T, name string) (taskextract.Task, taskextract.Source, []string) {
	t.Helper()
	raw, err := os.ReadFile("../taskextract/testdata/ticket.json")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(body))) != taskTicketHash {
		t.Fatal("frozen synthetic ticket bytes changed")
	}
	task := taskextract.Task{
		ID: "task-test-7", Revision: 1, SourceID: "ticket-test-7",
		Parts: []string{"description", "comments"},
	}
	var expected []string
	switch name {
	case "release":
		task.Objective = "採集這張 ticket 的上線條件，包含尚未完成的條件。"
		expected = []string{"u001", "u002"}
	case "recovery":
		task.Objective = "採集這張 ticket 的回復方案、適用例外與失敗紀錄。"
		expected = []string{"u003", "u004", "u005"}
	default:
		t.Fatal("select exactly one synthetic task: release or recovery")
	}
	source := taskextract.Source{
		ID: "ticket-test-7", Revision: "revision-1", Title: "Synthetic release ticket",
		Location: "https://example.invalid/tickets/TEST-7", Coverage: "full_document",
		Limitations: []string{},
		Parts: []taskextract.SourcePart{
			{Name: "description", Text: body},
			{Name: "history", Text: "Unrequested private-history marker: HISTORY_NOT_FOR_MODEL."},
		},
	}
	return task, source, expected
}

// This ordinary test checks the shared fixture, not model quality.
func TestTaskLocalFrozenTicket(t *testing.T) {
	var previousID string
	for _, name := range []string{"release", "recovery"} {
		task, source, expected := taskTicket(t, name)
		request, err := taskextract.Prepare(task, source, "synthetic-selector")
		if err != nil || len(request.Units()) != 6 || len(expected) == 0 ||
			!slices.Equal(request.Scope().NotCollectedParts, []string{"comments"}) ||
			!slices.Equal(request.Scope().NotProvidedParts, []string{"history"}) ||
			request.InputID() == previousID {
			t.Fatalf("frozen task scope changed: %v", err)
		}
		previousID = request.InputID()
	}
}

// Explicit opt-in: exactly one synthetic task, no Desktop workspace, source
// MCP, DB, pending, review, fallback model or retry. The expected paragraph
// catalog is controller-side test data and is never given to the model.
func TestTaskLocalModelSyntheticTicket(t *testing.T) {
	if os.Getenv("DETECTIVE_TASK_MODEL_SMOKE") != "1" {
		t.Skip("requires explicit approval for a single local-model synthetic task")
	}
	name := os.Getenv("DETECTIVE_TASK_MODEL_CASE")
	modelName := os.Getenv("DETECTIVE_TASK_MODEL_NAME")
	endpoint := os.Getenv("DETECTIVE_TASK_MODEL_ENDPOINT")
	if modelName == "" || endpoint == "" {
		t.Fatal("select an explicit model name and loopback /v1 endpoint")
	}
	task, source, expected := taskTicket(t, name)
	request, err := taskextract.Prepare(task, source, modelName)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	result, extractErr := ExtractTask(t.Context(), task, source, modelName, endpoint)
	elapsed := time.Since(started)
	selected := []string{}
	exact := result.Status == "selected" && len(result.Candidates) > 0
	for _, candidate := range result.Candidates {
		if candidate.Part != "description" || candidate.StartByte < 0 ||
			candidate.EndByte > len(source.Parts[0].Text) || candidate.EndByte <= candidate.StartByte ||
			candidate.Text != source.Parts[0].Text[candidate.StartByte:candidate.EndByte] {
			exact = false
		}
		for _, id := range candidate.UnitIDs {
			if !slices.Contains(selected, id) {
				selected = append(selected, id)
			}
		}
	}
	missing, extra := []string{}, []string{}
	for _, id := range expected {
		if !slices.Contains(selected, id) {
			missing = append(missing, id)
		}
	}
	for _, id := range selected {
		if !slices.Contains(expected, id) {
			extra = append(extra, id)
		}
	}
	replayOK := false
	if extractErr == nil {
		replayed, replayErr := taskextract.Replay(request, result)
		replayOK = replayErr == nil && reflect.DeepEqual(replayed, result)
	}
	errorText := ""
	if extractErr != nil {
		errorText = extractErr.Error()
	}
	report := struct {
		Case              string             `json:"case"`
		StartedAt         time.Time          `json:"started_at"`
		ElapsedMS         int64              `json:"elapsed_ms"`
		SourceSHA256      string             `json:"source_sha256"`
		PromptVersion     string             `json:"prompt_version"`
		ModelInputVersion string             `json:"model_input_version"`
		InputID           string             `json:"input_id"`
		Model             string             `json:"model"`
		ReasoningEffort   string             `json:"reasoning_effort"`
		Task              taskextract.Task   `json:"task"`
		Source            taskextract.Source `json:"source"`
		Units             []taskextract.Unit `json:"units"`
		Result            taskextract.Result `json:"result"`
		Error             string             `json:"error"`
		Assessment        string             `json:"assessment"`
		ExpectedUnitIDs   []string           `json:"expected_unit_ids"`
		SelectedUnitIDs   []string           `json:"selected_unit_ids"`
		MissingUnitIDs    []string           `json:"missing_unit_ids"`
		ExtraUnitIDs      []string           `json:"extra_unit_ids"`
		Verbatim          bool               `json:"verbatim"`
		LocalReplayOK     bool               `json:"local_replay_ok"`
	}{
		Case: name, StartedAt: started, ElapsedMS: elapsed.Milliseconds(),
		SourceSHA256: taskTicketHash, PromptVersion: taskextract.PromptVersion,
		ModelInputVersion: taskextract.ModelInputVersion,
		InputID:           request.InputID(), Model: modelName, ReasoningEffort: "none",
		Task: task, Source: source, Units: request.Units(), Result: result, Error: errorText,
		Assessment:      "synthetic_expected_paragraphs_not_human_approval",
		ExpectedUnitIDs: expected, SelectedUnitIDs: selected, MissingUnitIDs: missing, ExtraUnitIDs: extra,
		Verbatim: exact, LocalReplayOK: replayOK,
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	// Final text is inside result.raw_text; no reasoning/provider packets are
	// logged. Keep this run output outside the public repository.
	t.Log("TASK_LOCAL_REPORT " + string(raw))
	if extractErr != nil || !exact || !replayOK || len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("synthetic task failed: status=%s missing=%v extra=%v verbatim=%t replay=%t err=%v",
			result.Status, missing, extra, exact, replayOK, extractErr)
	}
}
