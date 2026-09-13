package desktop

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func TestNewWorkClearsOnlyActiveWork(t *testing.T) {
	s := newTestService(t)
	settings := atlassianSettings(s)
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	const saved = "previously saved source and task data"
	if err := s.writePrivate("saved-work.json", []byte(saved), false); err != nil {
		t.Fatal(err)
	}
	settingsBefore, err := os.ReadFile(filepath.Join(s.dataDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	entriesBefore, err := os.ReadDir(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	session := &sourcemcp.OAuthSession{}
	s.oauthSessions = map[string]*sourcemcp.OAuthSession{"atlassian": session}
	s.state.SourceAuth = map[string]SourceAuthView{"atlassian": {Status: "connected"}}
	s.state.Tools = []Tool{{ConnectionID: "atlassian", Name: "getJiraIssue", Digest: "saved-inventory"}}
	s.state.ToolAdvice = &ToolAdvice{ID: "old-advice"}
	s.state.Source = &SourceView{ID: "old-source", RawText: saved}
	s.state.Task = &TaskWork{InputID: "old-task", Status: "selected"}
	s.state.Brief = &BriefWork{}
	s.state.Candidates = []CandidateView{{Ordinal: 1, State: "pending"}}
	s.state.Extraction = &labstatus.RowBatch{}
	s.state.BatchPath = "saved-batch.json"
	s.state.BatchDigest = "old-batch"
	s.state.BatchResult = &pending.BatchResult{}
	s.state.Messages = []Message{{Role: "user", Text: "old objective"}}
	s.state.Search = &EvidenceSearchView{}
	s.state.Error = "old failure"
	before := s.Snapshot()

	state, err := s.NewWork()
	if err != nil {
		t.Fatal(err)
	}
	if state.Source != nil || state.Task != nil || state.Brief != nil || len(state.Candidates) != 0 || state.Extraction != nil || state.BatchPath != "" || state.BatchDigest != "" || state.BatchResult != nil || len(state.Messages) != 0 || state.Search != nil {
		t.Fatal("active work survived reset")
	}
	if state.Busy || state.Operation != "" || state.Error != "" || state.ToolAdvice != nil {
		t.Fatal("reset retained operation, error, or stale advice")
	}
	if !reflect.DeepEqual(state.Settings, before.Settings) || !reflect.DeepEqual(state.Tools, before.Tools) || !reflect.DeepEqual(state.SourceAuth, before.SourceAuth) || s.sourceLogin("atlassian") != session {
		t.Fatal("empty work changed settings, tools, or login")
	}
	if state.DataDir != before.DataDir || state.WorkspaceID != before.WorkspaceID || len(state.Events) != len(before.Events)+2 {
		t.Fatal("reset replaced the workspace or its operational history")
	}
	entriesAfter, err := os.ReadDir(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatal("reset created or deleted workspace files")
	}
	for i := range entriesBefore {
		if entriesBefore[i].Name() != entriesAfter[i].Name() {
			t.Fatal("reset replaced a workspace file")
		}
	}
	settingsAfter, err := os.ReadFile(filepath.Join(s.dataDir, "settings.json"))
	if err != nil || string(settingsAfter) != string(settingsBefore) {
		t.Fatal("saved settings changed")
	}
	body, err := os.ReadFile(filepath.Join(s.dataDir, "saved-work.json"))
	if err != nil || string(body) != saved {
		t.Fatal("saved work changed")
	}
}

func TestNewWorkRejectsBusyOrClosedService(t *testing.T) {
	for _, blocked := range []string{"busy", "closed"} {
		t.Run(blocked, func(t *testing.T) {
			s := newTestService(t)
			s.state.Source = &SourceView{ID: "retained-source"}
			if blocked == "closed" {
				s.Close()
			} else {
				_, done, err := s.begin(t.Context(), "existing_work")
				if err != nil {
					t.Fatal(err)
				}
				defer done()
			}
			before := s.Snapshot()
			state, err := s.NewWork()
			if err == nil || !reflect.DeepEqual(state, before) {
				t.Fatal("blocked reset changed active work")
			}
		})
	}
}
