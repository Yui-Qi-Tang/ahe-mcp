package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func importBriefFixture(t *testing.T, s *Service) State {
	t.Helper()
	source := briefModelSource()
	source.Body = "The synthetic service reported increased errors.\nThe cause remains unconfirmed.\nOther services remain unaffected."
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(serviceDirectory(t), "brief-source.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := s.ImportBriefSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return state
}

func completeBriefFixture(t *testing.T, s *Service) State {
	t.Helper()
	importBriefFixture(t, s)
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeModelText(w, "The synthetic service reported increased errors; the cause remains unconfirmed.")
	})
	localSettings(t, s, server)
	state, err := s.ExtractBrief(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestBriefDesktopFrozenSourceOneCallAndIndependentSelection(t *testing.T) {
	s := newTestService(t)
	initial := importBriefFixture(t, s)
	if initial.Brief == nil || initial.Brief.Report.Stage != "inspected" || initial.Brief.Submission != nil || initial.Brief.Handoff != nil {
		t.Fatal("import gained model or DB authority")
	}
	before, err := os.ReadFile(initial.Brief.Path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["instructions"] != sourcepilot.BriefInstruction || request["tools"] != nil {
			t.Error("wrong Brief model contract")
		}
		writeModelText(w, "The synthetic service reported increased errors; the cause remains unconfirmed.")
	})
	localSettings(t, s, server)
	state, err := s.ExtractBrief(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || state.Brief.Handoff != nil || state.Brief.Report.Source.Body != initial.Brief.Report.Source.Body || state.Brief.Report.HumanReview != "not_reviewed" {
		t.Fatal("extraction changed source or authority")
	}
	if _, err := s.ExtractBrief(t.Context()); err == nil || calls.Load() != 1 {
		t.Fatal("repeated model call")
	}
	if _, err := s.PrepareBriefCandidate(BriefCandidateRequest{Statement: state.Brief.Report.Text, StartLine: 1, EndLine: 2, SourceSHA256: "wrong"}); err == nil {
		t.Fatal("stale selection accepted")
	}
	state, err = s.PrepareBriefCandidate(BriefCandidateRequest{Statement: state.Brief.Report.Text, StartLine: 1, EndLine: 2, SourceSHA256: state.Brief.Report.BodySHA256})
	if err != nil {
		t.Fatal(err)
	}
	if state.Brief.Submission.Citation.ExactQuote != "The synthetic service reported increased errors.\nThe cause remains unconfirmed." || state.Brief.Handoff != nil || state.BatchPath != "" || len(state.Candidates) != 0 {
		t.Fatal("Brief fabricated STATUS batch or submitted")
	}
	if _, err := s.PrepareBriefCandidate(BriefCandidateRequest{}); err == nil {
		t.Fatal("frozen selection replaced")
	}
	after, err := os.ReadFile(initial.Brief.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("historical input changed")
	}
	detached := s.Snapshot()
	detached.Brief.Submission.Statement = "changed"
	if s.Snapshot().Brief.Submission.Statement == "changed" {
		t.Fatal("renderer aliases Brief state")
	}
}

func TestLegacyBriefReopensWithoutReclassificationAndStopsBeforeModelInventory(t *testing.T) {
	s := newTestService(t)
	state := importBriefFixture(t, s)
	work := *state.Brief
	work.Report.Source.Version = "detective-brief-source/v1"
	work.Report.Source.SourceKind = ""
	if err := s.saveBrief(work); err != nil {
		t.Fatal(err)
	}
	path := s.Snapshot().Brief.Path
	before, err := os.ReadFile(path)
	if err != nil || bytes.Contains(before, []byte(`"source_kind"`)) {
		t.Fatal("historical work acquired a source kind")
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	localSettings(t, s, server)
	if _, err := s.OpenBriefWork(path); err != nil {
		t.Fatalf("legacy work cannot reopen: %v", err)
	}
	if _, err := s.ExtractBrief(t.Context()); err == nil || calls.Load() != 0 {
		t.Fatal("legacy work reached model inventory or generation")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || s.Snapshot().Brief.Report.Source.SourceKind != "" {
		t.Fatal("legacy work was rewritten or reclassified")
	}
}

func TestBriefImportDisplaysSpecificSafeInputBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"body", strings.Repeat("x", sourcepilot.BriefBodyLimit+1), "body_bytes"},
		{"paragraphs", strings.Repeat("private-source-line\n", 65), "nonempty_segments"},
		{"JSON", strings.Repeat("x", (256<<10)+1), "source_json_bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t)
			source := briefModelSource()
			source.Body = tc.body
			raw, _ := json.Marshal(source)
			path := filepath.Join(serviceDirectory(t), "private-filename.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			state, err := s.ImportBriefSource(path)
			var limit *sourcepilot.InputLimitError
			if !errors.As(err, &limit) || limit.Resource != tc.want || !strings.Contains(state.Error, tc.want) || strings.Contains(state.Error, "private-source-line") || strings.Contains(state.Error, path) || state.Brief != nil || state.Source != nil {
				t.Fatalf("import lost safe exact diagnostic or created work: %v %s", err, state.Error)
			}
		})
	}
}

func TestBriefDesktopPreservesExcerptCoordinatesWithoutReopeningParent(t *testing.T) {
	s := newTestService(t)
	parent := briefModelSource()
	parent.Body = strings.Repeat("Original parent line.\n", 80) + "The service remains delayed."
	start := strings.LastIndex(parent.Body, "The service")
	source, err := sourcepilot.SelectBriefExcerpt(parent, start, len(parent.Body), "Read only the named service status.")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	path := filepath.Join(serviceDirectory(t), "excerpt.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := s.ImportBriefSource(path)
	if err != nil || state.Brief == nil || !reflect.DeepEqual(state.Brief.Report.Source.Excerpt, source.Excerpt) {
		t.Fatalf("excerpt import lost parent coordinates: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	state, err = s.OpenBriefWork(state.Brief.Path)
	if err != nil || !reflect.DeepEqual(state.Brief.Report.Source.Excerpt, source.Excerpt) || state.Brief.Report.Source.Body != source.Body {
		t.Fatalf("offline reopening required source file or changed excerpt: %v", err)
	}
	var out bytes.Buffer
	if err := sourcepilot.WriteBriefText(&out, state.Brief.Report); err != nil || !strings.Contains(out.String(), "尚未重新比對父原文") {
		t.Fatal("saved excerpt falsely implied parent reverification", err)
	}
}

func TestBriefDesktopOfflineGuardsReopenAndTampering(t *testing.T) {
	s := newTestService(t)
	state := completeBriefFixture(t, s)
	state, err := s.PrepareBriefCandidate(BriefCandidateRequest{Statement: state.Brief.Report.Text, StartLine: 1, EndLine: 2, SourceSHA256: state.Brief.Report.BodySHA256})
	if err != nil {
		t.Fatal(err)
	}
	saved := state.Brief.Path
	settings := state.Settings
	settings.Mode = "demo"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitBriefPending(t.Context(), state.Brief.Submission.Digest); err == nil {
		t.Fatal("offline submit accepted")
	}
	if _, err := s.QueryBriefPending(t.Context()); err == nil {
		t.Fatal("missing receipt queried")
	}
	if _, err := s.PrepareBriefReview(t.Context(), state.Brief.Submission.Digest); err == nil {
		t.Fatal("missing receipt reviewed")
	}
	if _, err := s.ApplyBriefReview(t.Context(), BriefReviewRequest{Outcome: "admit", Reason: "old approval"}); err == nil {
		t.Fatal("old admit became approval")
	}
	before := s.Snapshot().Brief
	if _, err := s.SendMessage(t.Context(), "admit"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.Snapshot().Brief) {
		t.Fatal("chat changed Brief")
	}
	if _, err := s.LoadDemo(); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Brief != nil {
		t.Fatal("new work retained active Brief")
	}
	state, err = s.OpenBriefWork(saved)
	if err != nil || state.Brief.FailureStage != "historical_not_rechecked" || state.Settings.Mode != "demo" {
		t.Fatalf("offline reopen: %v", err)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{strings.Replace(string(raw), "The synthetic service reported", "The synthetic service caused", 1), `{"version":"ignored",` + string(raw[1:])} {
		if err := os.WriteFile(saved, []byte(change), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenBriefWork(saved); err == nil {
			t.Fatal("changed or duplicate field work accepted")
		}
	}
	if err := os.WriteFile(saved, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenBriefWork(filepath.Join(serviceDirectory(t), filepath.Base(saved))); err == nil {
		t.Fatal("foreign workspace accepted")
	}
}

func TestBriefDesktopUncertainSubmissionPersistsExactRetry(t *testing.T) {
	s := newTestService(t)
	state := completeBriefFixture(t, s)
	state, err := s.PrepareBriefCandidate(BriefCandidateRequest{Statement: state.Brief.Report.Text, StartLine: 1, EndLine: 2, SourceSHA256: state.Brief.Report.BodySHA256})
	if err != nil {
		t.Fatal(err)
	}
	settings := state.Settings
	settings.IntakeLauncher, settings.QueryLauncher = filepath.Join(s.dataDir, "missing-intake"), filepath.Join(s.dataDir, "missing-query")
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitBriefPending(t.Context(), "admit"); err == nil {
		t.Fatal("non-digest submission")
	}
	state, err = s.SubmitBriefPending(t.Context(), state.Brief.Submission.Digest)
	if err == nil || state.Brief.FailureStage != "intake" || state.Brief.Outcome != "submission_started" || state.Brief.IntakeLauncher != settings.IntakeLauncher {
		t.Fatal("uncertain submission lost recovery")
	}
	if _, err := os.Stat(state.Brief.Path); err != nil {
		t.Fatal("pre-write material not durable")
	}
	settings.IntakeLauncher += "-other"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot().Brief.Path
	if _, err := s.SubmitBriefPending(t.Context(), state.Brief.Submission.Digest); err == nil || s.Snapshot().Brief.Path != before {
		t.Fatal("uncertain retry changed launcher")
	}
}

func TestBriefDesktopCancelledAndInvalidInputsHaveNoAuthority(t *testing.T) {
	s := newTestService(t)
	if _, err := s.ExtractBrief(nil); err == nil {
		t.Fatal("nil context")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SubmitBriefPending(ctx, "anything"); err == nil {
		t.Fatal("cancelled write")
	}
	for _, command := range []string{"relative", "/tmp/../tmp/writer", "/tmp/writer\n"} {
		settings := s.Snapshot().Settings
		settings.ReviewLauncher = command
		if _, err := s.SaveSettings(settings); err == nil {
			t.Fatal("unsafe reviewer path")
		}
	}
	for _, request := range []BriefReviewRequest{{}, {Outcome: "admit", Reason: ""}, {Outcome: "approved", Reason: "x"}, {Outcome: "reject", Reason: " x"}} {
		if _, err := s.ApplyBriefReview(t.Context(), request); err == nil {
			t.Fatal("invalid review accepted")
		}
	}
}

func TestBriefDesktopCannotMixSTATUSOrMisreportFailedPersistence(t *testing.T) {
	s := newTestService(t)
	state := completeBriefFixture(t, s)
	if _, err := s.Extract(t.Context(), 1); err == nil {
		t.Fatal("STATUS extraction accepted Brief work")
	}
	state, err := s.PrepareBriefCandidate(BriefCandidateRequest{Statement: state.Brief.Report.Text, StartLine: 1, EndLine: 2, SourceSHA256: state.Brief.Report.BodySHA256})
	if err != nil {
		t.Fatal(err)
	}
	mixed := state
	mixed.BatchPath = "/other/batch"
	if s.briefOnline(mixed, state.Brief.Submission.Digest) == nil {
		t.Fatal("Brief accepted a mixed STATUS batch")
	}
	changed := cloneState(state)
	changed.Source.RawText = "different"
	if s.briefOnline(changed, state.Brief.Submission.Digest) == nil {
		t.Fatal("Brief accepted a different displayed source")
	}
	_, done, err := s.begin(t.Context(), "brief_test_save")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if err := os.Chmod(s.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dataDir, 0o700) })
	failed, err := s.finishBrief(done, *state.Brief, "success-marker")
	if err == nil || !strings.Contains(failed.Error, "保存未確認") || strings.Contains(failed.Error, "success-marker") {
		t.Fatal("save failure reported success")
	}
}
