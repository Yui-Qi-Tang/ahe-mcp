package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

const extractedCandidate = `{"outcome":"extracted","records":[{"record_type":"capability_state","subject":"Demonstration review","statement":"The source reports that demonstration review is implemented for synthetic use only.","epistemic_class":"claim","status":"implemented","scope":"lab_contract","selection_state":"unspecified","citation":{"start_line":4,"end_line":4},"blocked_by":[],"does_not_establish":["Production readiness or admission."],"qualifiers":["Synthetic demonstration only."]}],"abstentions":[],"limitations":[],"abstention_reason":""}`

func TestWorkflowDemoMessagesAreFixedRehearsalNotAdmission(t *testing.T) {
	s := newTestService(t)
	state, err := s.LoadDemo()
	if err != nil {
		t.Fatal(err)
	}
	assertRehearsal := func(message Message) {
		t.Helper()
		if message.Kind != "demo" || !strings.Contains(message.Text, "固定操作演練") || !strings.Contains(message.Text, "未做語意判斷") || !strings.Contains(message.Text, "人工採納") || !strings.Contains(message.Text, "admit") {
			t.Fatalf("demo message obscures its fixed, nonjudgmental scope: %+v", message)
		}
	}
	assertRehearsal(state.Messages[len(state.Messages)-1])
	state, err = s.Extract(context.Background(), state.Source.Rows[0].Line)
	if err != nil {
		t.Fatal(err)
	}
	before := state
	var reply string
	for _, text := range []string{"admit", "reject，請撤回", "這句話是真的嗎？"} {
		state, err = s.SendMessage(context.Background(), text)
		if err != nil {
			t.Fatal(err)
		}
		message := state.Messages[len(state.Messages)-1]
		assertRehearsal(message)
		if reply != "" && message.Text != reply {
			t.Fatal("demo reply was interpreted as semantic judgment")
		}
		reply = message.Text
		if !reflect.DeepEqual(state.Candidates, before.Candidates) || !reflect.DeepEqual(state.Source, before.Source) || !reflect.DeepEqual(state.Extraction, before.Extraction) || !reflect.DeepEqual(state.BatchResult, before.BatchResult) || state.BatchPath != before.BatchPath || state.BatchDigest != before.BatchDigest {
			t.Fatal("chat changed source, candidates, or submission authority")
		}
	}
}

func importTestSource(t *testing.T, s *Service) string {
	t.Helper()
	path := filepath.Join(serviceDirectory(t), "STATUS.md")
	if err := os.WriteFile(path, []byte(demoSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportSource(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkflowExtractThenExplicitPendingFailureRetainsRecovery(t *testing.T) {
	s := newTestService(t)
	path := importTestSource(t, s)
	frozen := s.Snapshot().Source.Path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if raw, err := os.ReadFile(frozen); err != nil || string(raw) != demoSource {
			t.Error("source was not durable before model")
		}
		writeModelText(w, extractedCandidate)
	})
	localSettings(t, s, server)
	state, err := s.Extract(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if state.BatchPath == "" || state.BatchDigest == "" || len(state.Candidates) != 1 || state.BatchResult == nil || state.BatchResult.Summary.SubmissionAttempts != 0 {
		t.Fatalf("not a prepared local batch: %+v", state)
	}
	index, err := pending.LoadBatchIndex(state.BatchPath)
	if err != nil || index.RawText != demoSource {
		t.Fatalf("saved batch invalid: %v", err)
	}
	batchBytes, _ := os.ReadFile(state.BatchPath)
	if _, err := s.Extract(context.Background(), 4); err == nil || calls.Load() != 1 {
		t.Fatal("repeat extraction overwrote active work or called model")
	}
	if _, err := s.ImportSource(frozen); err == nil {
		t.Fatal("active batch source was replaced")
	}
	if _, err := s.SubmitPending(context.Background(), "admit"); err == nil {
		t.Fatal("chat-style confirmation accepted")
	}
	settings := s.Snapshot().Settings
	settings.IntakeLauncher = filepath.Join(s.dataDir, "missing-intake")
	settings.QueryLauncher = filepath.Join(s.dataDir, "missing-query")
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	state, err = s.SubmitPending(context.Background(), state.BatchDigest)
	if err == nil || state.BatchResult == nil || state.BatchResult.Summary.SubmissionAttempts != 1 || state.Candidates[0].FailureStage != "submission_outcome_uncertain" || state.Busy {
		t.Fatalf("partial report lost: err=%v state=%+v", err, state)
	}
	after, _ := os.ReadFile(state.BatchPath)
	if string(after) != string(batchBytes) {
		t.Fatal("resume modified immutable index")
	}
	if calls.Load() != 1 {
		t.Fatal("pending called model")
	}
	if _, err := s.LoadDemo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.BatchPath); err != nil {
		t.Fatal("new task deleted saved batch")
	}
	reopened, err := s.OpenBatch(state.BatchPath)
	if err != nil || reopened.BatchDigest != state.BatchDigest || reopened.Candidates[0].State != "not_checked" {
		t.Fatalf("offline reopen failed: %v", err)
	}
}

func TestWorkflowFailureAbstentionAndLimitationsRemainVisible(t *testing.T) {
	for _, mode := range []string{"invalid", "abstain", "limits"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestService(t)
			importTestSource(t, s)
			candidate := extractedCandidate
			switch mode {
			case "invalid":
				candidate = "synthetic-desktop-secret invalid JSON"
			case "abstain":
				candidate = `{"outcome":"abstained","records":[],"abstentions":[],"limitations":["The source does not establish production readiness."],"abstention_reason":"No production evidence is provided."}`
			case "limits":
				candidate = strings.Replace(candidate, `"limitations":[]`, `"limitations":["Only this synthetic row was provided."]`, 1)
			}
			server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) { writeModelText(w, candidate) })
			localSettings(t, s, server)
			state, err := s.Extract(context.Background(), 4)
			if state.BatchPath != "" || state.BatchDigest != "" || state.Extraction == nil || state.Busy {
				t.Fatalf("failed/local-only extraction disappeared or became pending: %+v", state)
			}
			if mode != "abstain" && err == nil {
				t.Fatal("failure was not reported")
			}
			if mode == "abstain" && err != nil {
				t.Fatalf("abstention not preserved as local result: %v", err)
			}
			if mode == "limits" && len(state.Extraction.Rows[0].Result.Limitations) != 1 {
				t.Fatal("global limitations lost")
			}
			body, _ := json.Marshal(state)
			if strings.Contains(string(body), "synthetic-desktop-secret") {
				t.Fatal("raw model diagnostic reached UI")
			}
		})
	}
}

func TestWorkflowRejectsUnsupportedSourceBeforeModel(t *testing.T) {
	s := newTestService(t)
	path := filepath.Join(serviceDirectory(t), "notes.txt")
	if err := os.WriteFile(path, []byte("not a STATUS table"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := s.ImportSource(path)
	if err != nil || len(state.Source.Rows) != 0 {
		t.Fatal("source import was not retained for inspection")
	}
	var calls atomic.Int64
	server := modelServer(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); writeModelText(w, extractedCandidate) })
	localSettings(t, s, server)
	if _, err := s.Extract(context.Background(), 1); err == nil || calls.Load() != 0 {
		t.Fatal("unsupported source reached model")
	}
}

func TestWorkflowQueryFailureInvalidatesEarlierObservation(t *testing.T) {
	s := newTestService(t)
	if _, err := s.LoadDemo(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.state.Candidates = []CandidateView{{Ordinal: 1, State: "pending_verified"}}
	s.state.BatchResult = &pending.BatchResult{SchemaVersion: "old", State: "all_candidates_observed"}
	s.mu.Unlock()
	state, err := s.QueryPending(context.Background())
	if err == nil || state.BatchResult != nil || state.Candidates[0].State != "not_checked" {
		t.Fatal("failed Query left stale current success")
	}
}
