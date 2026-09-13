package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

const taskCLIModel = "synthetic-task-selector"
const taskCLISelection = `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":"Preserve both conditions."}`

func taskCLIInput(t *testing.T) (string, taskextract.Input) {
	t.Helper()
	input := taskextract.Input{
		Version: taskextract.InputVersion,
		Task: taskextract.Task{ID: "synthetic-task", Revision: 1, Objective: "找出上線條件與未完成事項。",
			SourceID: "synthetic-ticket", Parts: []string{"description", "comments"}},
		Source: taskextract.Source{ID: "synthetic-ticket", Revision: "unknown", Title: "Synthetic engineering ticket",
			Location: "https://example.invalid/tickets/TEST-7", Coverage: "exact_excerpt", Limitations: []string{"Comments were not collected."},
			Parts: []taskextract.SourcePart{
				{Name: "description", Text: "The owner may approve deployment only after smoke tests pass.\r\nA green dashboard alone is not approval.\r\n\r\nThe operator must verify the checksum. This has not been completed.\r\n\r\nThe team will select a banner color later."},
				{Name: "history", Text: "HISTORY_NOT_FOR_MODEL: unrequested collected history."},
			}},
	}
	return briefPrivateJSON(t, input), input
}

func taskCLIArgs(input, out string) []string {
	return []string{"run", "-input", input, "-model", taskCLIModel, "-base-url", "http://127.0.0.1:11434/v1", "-out", out}
}

type taskCLILLM struct{ answer string }

func (m taskCLILLM) Name() string { return taskCLIModel }

func (m taskCLILLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.answer, genai.RoleModel), FinishReason: genai.FinishReasonStop}, nil)
	}
}

func taskScriptedRunner(answer string, calls *int) taskRunner {
	return func(ctx context.Context, task taskextract.Task, source taskextract.Source, modelName, _ string) (taskextract.Result, error) {
		*calls++
		request, err := taskextract.Prepare(task, source, modelName)
		if err != nil {
			return taskextract.Result{}, err
		}
		return taskextract.Extract(ctx, taskCLILLM{answer: answer}, request)
	}
}

func TestTaskCLIInspectAndHelpAreOffline(t *testing.T) {
	path, input := taskCLIInput(t)
	for _, format := range []string{"text", "json"} {
		var stdout, stderr bytes.Buffer
		if err := runTaskWithContext(t.Context(), []string{"inspect", "-input", path, "-model", taskCLIModel, "-format", format}, &stdout, &stderr, nil); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 {
			t.Fatal("offline inspection emitted diagnostics")
		}
		if format == "json" {
			var got taskInspection
			if json.Unmarshal(stdout.Bytes(), &got) != nil || !reflect.DeepEqual(got.Task, input.Task) || !reflect.DeepEqual(got.Source, input.Source) ||
				got.HumanReview != "not_reviewed" || got.AuthorityEffect != "none" || got.Scope.FactCompletenessAssessed || len(got.Units) != 3 {
				t.Fatal("inspection lost source or claimed extraction/review authority")
			}
		} else {
			for _, wanted := range []string{input.Task.Objective, input.Source.Title, input.Source.Location, "unknown", "comments", "history", "HISTORY_NOT_FOR_MODEL"} {
				if !strings.Contains(stdout.String(), wanted) {
					t.Fatalf("inspection omitted context %q", wanted)
				}
			}
		}
	}
	for _, option := range []string{"--help", "-h", "--version"} {
		var stdout, stderr bytes.Buffer
		if err := run([]string{"task", option}, &stdout, &stderr); err != nil || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatal("task help/version dispatch failed", err)
		}
	}
}

func TestTaskCLIRunThenReadWithoutOriginalSourceOrModel(t *testing.T) {
	path, input := taskCLIInput(t)
	out := filepath.Join(sourceCLIPrivateDir(t), "task-run.json")
	var stdout, stderr bytes.Buffer
	calls := 0
	if err := runTaskWithContext(t.Context(), append(taskCLIArgs(path, out), "-format", "json"), &stdout, &stderr, taskScriptedRunner(taskCLISelection, &calls)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	record, err := taskextract.ParseRecord(raw)
	if err != nil || calls != 1 || record.Status != "selected" || record.Result == nil || len(record.Result.Candidates) != 1 ||
		!reflect.DeepEqual(record.Input, input) || record.Result.HumanReview != "not_reviewed" || record.Result.AuthorityEffect != "none" {
		t.Fatalf("run did not save a complete unreviewed record: %v", err)
	}
	candidate := record.Result.Candidates[0]
	if candidate.Text != input.Source.Parts[0].Text[candidate.StartByte:candidate.EndByte] || !strings.Contains(candidate.Text, "\r\n") || strings.Contains(candidate.Text, record.Result.Reason) {
		t.Fatal("original candidate text changed or included a model annotation")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		stdout.Reset()
		if err := runTaskWithContext(t.Context(), []string{"read", "-input", out, "-format", format}, &stdout, &stderr, nil); err != nil {
			t.Fatal("offline read needed original source or model", err)
		}
		if format == "text" && (!strings.Contains(stdout.String(), "checksum") || !strings.Contains(stdout.String(), "banner color") || !strings.Contains(stdout.String(), "HISTORY_NOT_FOR_MODEL")) {
			t.Fatal("read omitted candidate, unselected source or unprovided source context")
		}
	}
	if calls != 1 || stderr.Len() != 0 {
		t.Fatal("offline read invoked model or emitted raw diagnostics")
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("saved run is not private")
	}
}

func TestTaskCLIPreflightStopsBeforeRunner(t *testing.T) {
	t.Setenv("DETECTIVE_MODEL", taskCLIModel)
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:11434/v1")
	for _, mode := range []string{"missing_model", "missing_endpoint", "missing_out", "duplicate", "writer", "positional", "timeout_zero", "timeout_large", "cloud", "credentials", "query_url", "model_control", "nil_context", "cancelled", "nil_runner", "relative", "input_permissions", "input_symlink", "invalid_json", "oversized", "no_readable", "existing_output", "output_permissions", "output_symlink"} {
		t.Run(mode, func(t *testing.T) {
			path, input := taskCLIInput(t)
			out := filepath.Join(sourceCLIPrivateDir(t), "task-run.json")
			args := taskCLIArgs(path, out)
			ctx := t.Context()
			calls := 0
			runner := taskScriptedRunner(taskCLISelection, &calls)
			switch mode {
			case "missing_model":
				args[4] = ""
			case "missing_endpoint":
				args[6] = ""
			case "missing_out":
				args[8] = ""
			case "duplicate":
				args = append(args, "-model", taskCLIModel)
			case "writer":
				args = append(args, "-ahe-ingest-command", "private-marker")
			case "positional":
				args = append(args, "private-marker")
			case "timeout_zero":
				args = append(args, "-timeout", "0")
			case "timeout_large":
				args = append(args, "-timeout", "121s")
			case "cloud":
				args[6] = "https://example.invalid/v1"
			case "credentials":
				args[6] = "http://operator:private-marker@127.0.0.1:11434/v1"
			case "query_url":
				args[6] += "?secret=private-marker"
			case "model_control":
				args[4] += "\nprivate-marker"
			case "nil_context":
				ctx = nil
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "nil_runner":
				runner = nil
			case "relative":
				args[2] = "private-marker.json"
			case "input_permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "input_symlink":
				link := filepath.Join(sourceCLIPrivateDir(t), "link.json")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				args[2] = link
			case "invalid_json":
				if err := os.WriteFile(path, []byte(`{"task":"private-marker"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "no_readable":
				input.Task.Parts = []string{"comments"}
				args[2] = briefPrivateJSON(t, input)
			case "existing_output":
				if err := os.WriteFile(out, []byte("retain-existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "output_permissions":
				if err := os.Chmod(filepath.Dir(out), 0o755); err != nil {
					t.Fatal(err)
				}
			case "output_symlink":
				if err := os.Symlink(path, out); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			err := runTaskWithContext(ctx, args, &stdout, &stderr, runner)
			if err == nil || calls != 0 || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("preflight leaked or reached model: err=%v calls=%d", err, calls)
			}
			if mode == "existing_output" {
				raw, err := os.ReadFile(out)
				if err != nil || string(raw) != "retain-existing" {
					t.Fatal("existing output changed")
				}
			} else if mode != "output_symlink" {
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("preflight reserved an output")
				}
			}
		})
	}
}

func TestTaskCLIFailureAndAbstentionStayDistinctOnRead(t *testing.T) {
	for _, mode := range []string{"abstained", "incomplete", "bad_reference", "unavailable", "cancelled", "unchecked_success"} {
		t.Run(mode, func(t *testing.T) {
			path, _ := taskCLIInput(t)
			out := filepath.Join(sourceCLIPrivateDir(t), "task-run.json")
			calls := 0
			answer := `{"outcome":"abstained","ranges":[],"reason":"No relevant supplied passage."}`
			if mode == "incomplete" {
				answer = `{"outcome":"incomplete","ranges":[],"reason":"Selection is not complete."}`
			}
			if mode == "bad_reference" {
				answer = strings.ReplaceAll(taskCLISelection, "u002", "u999")
			}
			runner := taskScriptedRunner(answer, &calls)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "unavailable" || mode == "cancelled" || mode == "unchecked_success" {
				runner = func(context.Context, taskextract.Task, taskextract.Source, string, string) (taskextract.Result, error) {
					calls++
					if mode == "cancelled" {
						cancel()
					}
					if mode == "unchecked_success" {
						return taskextract.Result{}, nil
					}
					return taskextract.Result{}, errors.New("private-marker")
				}
			}
			var stdout, stderr bytes.Buffer
			err := runTaskWithContext(ctx, taskCLIArgs(path, out), &stdout, &stderr, runner)
			if (err == nil) != (mode == "abstained") || calls != 1 || stderr.Len() != 0 || strings.Contains(stdout.String(), "private-marker") {
				t.Fatalf("failed run was retried or misclassified: %v calls=%d", err, calls)
			}
			stdout.Reset()
			if err := runTaskWithContext(t.Context(), []string{"read", "-input", out, "-format", "json"}, &stdout, &stderr, nil); err != nil {
				t.Fatal(err)
			}
			var record taskextract.Record
			if json.Unmarshal(stdout.Bytes(), &record) != nil {
				t.Fatal("invalid saved JSON")
			}
			if mode == "abstained" {
				if record.Status != "abstained" || record.Result == nil || len(record.Result.Candidates) != 0 {
					t.Fatal("lost successful abstention")
				}
			} else if record.Status != "failed" || record.ErrorCode == "" || record.Result != nil {
				t.Fatal("saved failure became candidates or a successful abstention")
			}
		})
	}
}

type taskBrokenWriter struct{}

func (taskBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTaskCLIKeepsSavedRecordWhenDisplayFails(t *testing.T) {
	path, _ := taskCLIInput(t)
	out := filepath.Join(sourceCLIPrivateDir(t), "task-run.json")
	calls := 0
	err := runTaskWithContext(t.Context(), taskCLIArgs(path, out), taskBrokenWriter{}, io.Discard, taskScriptedRunner(taskCLISelection, &calls))
	if err == nil || !strings.Contains(err.Error(), "record is saved") || calls != 1 {
		t.Fatal("display failure lost saved outcome", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskextract.ParseRecord(raw); err != nil {
		t.Fatal("saved record invalid after display failure", err)
	}
}

func TestTaskCLIDispatchUsesExistingLocalTransportOnly(t *testing.T) {
	path, _ := taskCLIInput(t)
	out := filepath.Join(sourceCLIPrivateDir(t), "task-run.json")
	var inventory, generations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			inventory.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": taskCLIModel}}})
		case "/v1/responses":
			generations.Add(1)
			raw, err := io.ReadAll(r.Body)
			if err != nil || strings.Contains(string(raw), "HISTORY_NOT_FOR_MODEL") || strings.Contains(string(raw), "private-marker") {
				t.Error("unprovided text or ambient credentials reached model")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "synthetic-task-response", "model": taskCLIModel, "status": "completed", "output": []any{map[string]any{
				"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": taskCLISelection, "annotations": []any{}}},
			}}})
		default:
			t.Error("unexpected source or writer request")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENAI_API_KEY", "private-marker")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "private-marker")
	args := taskCLIArgs(path, out)
	args[6] = server.URL + "/v1"
	var stdout, stderr bytes.Buffer
	if err := run(append([]string{"task"}, args...), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if inventory.Load() != 1 || generations.Load() != 1 || stderr.Len() != 0 {
		t.Fatal("unexpected local transport calls")
	}
	server.Close()
	if err := run([]string{"task", "read", "-input", out}, &stdout, &stderr); err != nil {
		t.Fatal("offline read depended on server", err)
	}
}
