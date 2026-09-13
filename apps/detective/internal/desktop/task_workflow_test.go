package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

const desktopTaskText = "The owner may approve deployment only after smoke tests pass.\r\nA green dashboard alone is not approval.\r\n\r\nThe operator must verify the package checksum; that check is not complete.\r\n"

func importTaskFixture(t *testing.T, s *Service, text string) State {
	t.Helper()
	path := filepath.Join(serviceDirectory(t), "ticket.md")
	writeSavedFixture(t, path, []byte(text))
	state, err := s.ImportSource(path)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func prepareTaskFixture(t *testing.T, s *Service, purpose string) State {
	t.Helper()
	source := s.Snapshot().Source
	state, err := s.PrepareTask(TaskDraftRequest{Objective: purpose, SourcePath: source.Path, SourceSHA256: source.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func taskServer(t *testing.T, reply string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"gemma4:e4b-it-qat"}]}`)
		case "/v1/responses":
			count.Add(1)
			envelope := taskModelEnvelope()
			envelope["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] = reply
			_ = json.NewEncoder(w).Encode(envelope)
		default:
			t.Error("unexpected tool, source or writer call")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &count
}

func localTaskSettings(t *testing.T, s *Service, endpoint string) {
	t.Helper()
	settings := s.Snapshot().Settings
	settings.Mode, settings.BaseURL = "local", endpoint+"/v1"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopTaskPreparePreservesSourceAndVersionsPurpose(t *testing.T) {
	s := newTestService(t)
	source := importTaskFixture(t, s, desktopTaskText).Source
	state := prepareTaskFixture(t, s, "採集上線條件。")
	work := state.Task
	if work.Status != "prepared" || work.Record != nil || work.Path != "" || work.Input.Source.Revision != "unknown" ||
		work.Input.Source.Coverage != "exact_excerpt" || work.Input.Source.Location != source.Path ||
		!reflect.DeepEqual(work.Input.Task.Parts, []string{"body"}) || work.Input.Source.Parts[0].Text != desktopTaskText || len(work.Units) != 2 ||
		work.Scope.FactCompletenessAssessed || len(work.Scope.NotCollectedParts) != 0 {
		t.Fatalf("task invented scope or rewrote source: %+v", work)
	}
	if !strings.Contains(strings.Join(work.Input.Source.Limitations, "\n"), "comments or history") || !strings.Contains(strings.Join(work.Input.Source.Limitations, "\n"), source.Note) {
		t.Fatal("generic-source limits or local provenance missing")
	}
	changed := prepareTaskFixture(t, s, "採集回復方案。")
	if changed.Task.Input.Task.ID != work.Input.Task.ID || changed.Task.Input.Task.Revision != 2 || changed.Task.InputID == work.InputID {
		t.Fatal("purpose update reused execution identity")
	}
	changed.Task.Input.Source.Parts[0].Text = "renderer mutation"
	if s.Snapshot().Task.Input.Source.Parts[0].Text != desktopTaskText {
		t.Fatal("task state aliases renderer")
	}
	importTaskFixture(t, s, desktopTaskText)
	if s.Snapshot().Task != nil {
		t.Fatal("new capture retained old task")
	}
	next := prepareTaskFixture(t, s, "採集上線條件。")
	if next.Task.Input.Task.ID == work.Input.Task.ID || next.Task.Input.Task.Revision != 1 {
		t.Fatal("different capture reused task identity")
	}
}

func TestDesktopTaskPrepareSavedMCPUsesOneToolReturn(t *testing.T) {
	s := newTestService(t)
	path, receipt := savedSourceFixture(t)
	if _, err := s.OpenSourceReceipt(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	state := prepareTaskFixture(t, s, "採集合成來源中的狀態。")
	source := state.Task.Input.Source
	if len(source.Parts) != 1 || source.Parts[0].Name != "tool_return" || source.Parts[0].Text != demoSource || source.Revision != "unknown" || source.Coverage != "exact_excerpt" {
		t.Fatal("MCP text became provider fields or complete ticket")
	}
	metadata := strings.Join(source.Limitations, "\n")
	for _, want := range []string{path, receipt.RawResult.Path, receipt.RawResult.SHA256, receipt.CapturedAt} {
		if !strings.Contains(metadata, want) {
			t.Errorf("capture reference omitted: %s", want)
		}
	}
	if _, err := os.Stat(receipt.Config.Command); !os.IsNotExist(err) {
		t.Fatal("fixture unexpectedly contains a runnable source launcher")
	}
}

func TestDesktopTaskRejectsInvalidAndStalePreparation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*TaskDraftRequest, *Service)
	}{
		{"path", func(d *TaskDraftRequest, _ *Service) { d.SourcePath += "-changed" }},
		{"digest", func(d *TaskDraftRequest, _ *Service) { d.SourceSHA256 = strings.Repeat("0", 64) }},
		{"empty purpose", func(d *TaskDraftRequest, _ *Service) { d.Objective = " " }},
		{"changed source", func(_ *TaskDraftRequest, s *Service) {
			writeSavedFixture(t, s.Snapshot().Source.Path, []byte("changed"))
		}},
		{"oversize", func(d *TaskDraftRequest, s *Service) {
			state := importTaskFixture(t, s, strings.Repeat("a", taskextract.MaxProvidedBytes+1))
			d.SourcePath, d.SourceSHA256 = state.Source.Path, state.Source.SHA256
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestService(t)
			importTaskFixture(t, s, desktopTaskText)
			prepareTaskFixture(t, s, "採集條件。")
			source := s.Snapshot().Source
			draft := TaskDraftRequest{Objective: "新目的", SourcePath: source.Path, SourceSHA256: source.SHA256}
			test.edit(&draft, s)
			state, err := s.PrepareTask(draft)
			if err == nil || state.Task != nil || state.Busy || len(state.Candidates) != 0 {
				t.Fatal("invalid preparation retained executable selection")
			}
			if test.name == "oversize" && !strings.Contains(state.Error, "32 KiB") {
				t.Fatal("oversize refusal omitted actionable limit")
			}
		})
	}
}

func TestDesktopTaskRunAndOfflineReopenRetainExactRecord(t *testing.T) {
	s := newTestService(t)
	server, calls := taskServer(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":"Untrusted annotation; not a human decision."}`)
	localTaskSettings(t, s, server.URL)
	source := importTaskFixture(t, s, desktopTaskText).Source
	prepared := prepareTaskFixture(t, s, "採集上線條件。")
	state, err := s.RunTask(context.Background(), prepared.Task.InputID)
	if err != nil {
		t.Fatal(err)
	}
	work := state.Task
	if calls.Load() != 1 || work.Status != "selected" || work.Record == nil || len(work.Record.Result.Candidates) != 1 ||
		len(state.Candidates) != 0 || state.BatchPath != "" || state.Brief != nil || state.Busy {
		t.Fatal("task did not complete once within its local lane")
	}
	candidate := work.Record.Result.Candidates[0]
	if candidate.Text != strings.TrimSuffix(desktopTaskText, "\r\n") || strings.Contains(candidate.Text, "Untrusted annotation") {
		t.Fatal("candidate lost source bytes or incorporated model annotation")
	}
	if _, err := s.RunTask(context.Background(), prepared.Task.InputID); err == nil || calls.Load() != 1 {
		t.Fatal("completed task was silently rerun")
	}
	raw, err := os.ReadFile(work.Path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(work.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("task record not private")
	}
	if _, err := taskextract.ParseRecord(raw); err != nil {
		t.Fatal(err)
	}
	changed := prepareTaskFixture(t, s, "採集尚未完成的檢查。")
	if changed.Task.Record != nil || changed.Task.InputID == work.InputID {
		t.Fatal("new purpose kept previous candidates")
	}
	if unchanged, err := os.ReadFile(work.Path); err != nil || string(unchanged) != string(raw) {
		t.Fatal("new purpose overwrote previous record")
	}
	s.Close()
	server.Close()
	if err := os.Remove(source.Path); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	state, err = reopened.OpenTaskRecord(work.Path)
	if err != nil || state.Task == nil || !reflect.DeepEqual(state.Task.Record, work.Record) || state.Task.BaseURL != "" || state.Source != nil || state.Settings.Mode != "demo" {
		t.Fatalf("offline reopen depended on original source/settings/model: %v", err)
	}
	if _, err := reopened.RunTask(context.Background(), work.InputID); err == nil || calls.Load() != 1 {
		t.Fatal("offline record acquired execution authority")
	}
}

func TestDesktopTaskFailedAndAbstainedRecords(t *testing.T) {
	for _, test := range []struct{ name, reply, status string }{
		{"abstained", `{"outcome":"abstained","ranges":[],"reason":"No relevant paragraph."}`, "abstained"},
		{"incomplete", `{"outcome":"incomplete","ranges":[],"reason":"Selection incomplete."}`, "failed"},
		{"invalid reference", `{"outcome":"selected","ranges":[{"start_id":"u999","end_id":"u999"}],"reason":""}`, "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestService(t)
			server, calls := taskServer(t, test.reply)
			localTaskSettings(t, s, server.URL)
			importTaskFixture(t, s, desktopTaskText)
			prepared := prepareTaskFixture(t, s, "採集條件。")
			state, err := s.RunTask(context.Background(), prepared.Task.InputID)
			if (err != nil) != (test.status == "failed") || calls.Load() != 1 || state.Task == nil || state.Task.Status != test.status || state.Task.Record == nil || len(state.Candidates) != 0 {
				t.Fatalf("wrong completion/failure state: %v", err)
			}
			if test.status == "failed" && state.Task.Record.Result != nil {
				t.Fatal("failure retained candidates")
			}
			opened, err := s.OpenTaskRecord(state.Task.Path)
			if err != nil || opened.Task.Status != test.status || calls.Load() != 1 {
				t.Fatal("offline record reclassified or retried failure")
			}
		})
	}
}

func TestDesktopTaskCancellationSavesFailureAndBlocksChanges(t *testing.T) {
	s := newTestService(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"gemma4:e4b-it-qat"}]}`)
			return
		}
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	defer close(release)
	localTaskSettings(t, s, server.URL)
	importTaskFixture(t, s, desktopTaskText)
	prepared := prepareTaskFixture(t, s, "採集條件。")
	finished := make(chan error, 1)
	go func() {
		_, err := s.RunTask(context.Background(), prepared.Task.InputID)
		finished <- err
	}()
	<-entered
	if state := s.Snapshot(); !state.Busy || state.Task.Status != "running" || state.Task.Record != nil {
		t.Fatal("running task exposed stale candidates")
	}
	if _, err := s.SaveSettings(s.Snapshot().Settings); err == nil {
		t.Fatal("running task settings changed")
	}
	s.Cancel()
	if err := <-finished; err == nil {
		t.Fatal("cancelled selection reported success")
	}
	state := s.Snapshot()
	if state.Busy || state.Task.Status != "failed" || state.Task.Record == nil || state.Task.Record.ErrorCode != "cancelled" || state.Task.Record.Result != nil || calls.Load() != 1 {
		t.Fatal("cancellation lost durable failure or retained candidates")
	}
	if _, err := s.OpenTaskRecord(state.Task.Path); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopTaskSettingsAndLegacyLaneIsolation(t *testing.T) {
	s := newTestService(t)
	server, calls := taskServer(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`)
	localTaskSettings(t, s, server.URL)
	importTaskFixture(t, s, desktopTaskText)
	prepared := prepareTaskFixture(t, s, "採集條件。")
	for _, operation := range []func() (State, error){
		func() (State, error) { return s.Extract(context.Background(), 4) },
		func() (State, error) { return s.SubmitPending(context.Background(), "admit") },
		func() (State, error) { return s.ExtractBrief(context.Background()) },
		func() (State, error) { return s.SubmitBriefPending(context.Background(), "admit") },
		func() (State, error) { return s.PrepareBriefCandidate(BriefCandidateRequest{}) },
	} {
		if _, err := operation(); err == nil {
			t.Fatal("task leaked into a legacy extraction/writer lane")
		}
	}
	if calls.Load() != 0 || s.Snapshot().Task.InputID != prepared.Task.InputID {
		t.Fatal("legacy operation invoked selector or replaced task")
	}
	settings := s.Snapshot().Settings
	settings.Model = "different-model"
	if state, err := s.SaveSettings(settings); err != nil || state.Task != nil {
		t.Fatal("settings save retained prepared task")
	}
	if _, err := s.RunTask(context.Background(), prepared.Task.InputID); err == nil || calls.Load() != 0 {
		t.Fatal("old confirmation ran after settings change")
	}
	prepareTaskFixture(t, s, "新目的。")
	if state, err := s.LoadDemo(); err != nil || state.Task != nil {
		t.Fatal("new demo retained task state")
	}
}

func TestDesktopTaskRunRejectsPostPrepareChanges(t *testing.T) {
	for _, name := range []string{"input ID", "saved bytes", "source metadata", "model", "endpoint"} {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t)
			server, calls := taskServer(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`)
			localTaskSettings(t, s, server.URL)
			importTaskFixture(t, s, desktopTaskText)
			state := prepareTaskFixture(t, s, "採集條件。")
			inputID := state.Task.InputID
			switch name {
			case "input ID":
				inputID += "changed"
			case "saved bytes":
				writeSavedFixture(t, state.Source.Path, []byte("changed after preparation"))
			default:
				s.mu.Lock()
				switch name {
				case "source metadata":
					s.state.Source.Title = "different observation"
				case "model":
					s.state.Settings.Model = "different-model"
				case "endpoint":
					s.state.Settings.BaseURL = "http://127.0.0.1:1/v1"
				}
				s.mu.Unlock()
			}
			state, err := s.RunTask(context.Background(), inputID)
			if err == nil || calls.Load() != 0 || state.Busy || (state.Task != nil && state.Task.Record != nil) {
				t.Fatal("changed source, confirmation or settings reached model")
			}
		})
	}
}

func TestDesktopTaskSaveFailureDoesNotPublishCandidates(t *testing.T) {
	s := newTestService(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"gemma4:e4b-it-qat"}]}`)
			return
		}
		calls.Add(1)
		if err := os.Chmod(s.dataDir, 0o755); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(taskModelEnvelope())
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if err := os.Chmod(s.dataDir, 0o700); err != nil {
			t.Error(err)
		}
	})
	localTaskSettings(t, s, server.URL)
	importTaskFixture(t, s, desktopTaskText)
	prepared := prepareTaskFixture(t, s, "採集條件。")
	state, err := s.RunTask(context.Background(), prepared.Task.InputID)
	if err == nil || calls.Load() != 1 || state.Task == nil || state.Task.Status != "failed" || state.Task.Record != nil || state.Task.Path == "" || len(state.Candidates) != 0 || state.Busy {
		t.Fatal("failed persistence published candidates or lost recovery path")
	}
	if _, err := s.RunTask(context.Background(), prepared.Task.InputID); err == nil || calls.Load() != 1 {
		t.Fatal("failed persistence silently retried model")
	}
}

func TestDesktopTaskCancelAfterPublicationDoesNotChangeOutcome(t *testing.T) {
	for _, status := range []string{"selected", "abstained", "failed"} {
		t.Run(status, func(t *testing.T) {
			s := newTestService(t)
			ctx, done, err := s.begin(context.Background(), "task_run")
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			s.mu.Lock()
			s.state.Task = &TaskWork{Status: status}
			events := len(s.state.Events)
			s.mu.Unlock()
			state := s.Cancel()
			if ctx.Err() != nil || len(state.Events) != events || state.Task.Status != status {
				t.Fatal("late cancel misreported a completed record as a pending failure")
			}
		})
	}
}
