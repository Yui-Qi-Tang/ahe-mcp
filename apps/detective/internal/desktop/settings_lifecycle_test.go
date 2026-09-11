package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

func twoSourceSettings(s *Service) Settings {
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.BaseURL = "http://127.0.0.1:1/v1"
	// These paths deliberately do not exist: saving coordinates must not launch them.
	settings.IntakeLauncher = filepath.Join(s.dataDir, "missing-intake-launcher")
	settings.QueryLauncher = filepath.Join(s.dataDir, "missing-query-launcher")
	settings.Connections = []Connection{
		{ID: "source-a", Name: "Synthetic source A", Transport: "stdio", Command: filepath.Join(s.dataDir, "missing-source-a"), AllowedTools: []string{"read_status"}},
		{ID: "source-b", Name: "Synthetic source B", Transport: "stdio", Command: filepath.Join(s.dataDir, "missing-source-b"), AllowedTools: []string{"read_document", "list_documents"}},
	}
	return settings
}

func TestServiceTwoSourceSettingsReopenOfflineWithoutRestoringSession(t *testing.T) {
	s := newTestService(t)
	state, err := s.LoadDemo()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Extract(context.Background(), state.Source.Rows[0].Line); err != nil {
		t.Fatal(err)
	}
	settings := twoSourceSettings(s)
	if state, err := s.SaveSettings(settings); err != nil || !reflect.DeepEqual(state.Settings, settings) {
		t.Fatalf("two-source settings were not saved exactly: %v", err)
	}
	// Seed only synthetic in-memory projections; no model, MCP or DB is involved.
	s.mu.Lock()
	s.state.Tools = []Tool{{ConnectionID: "source-b", Name: "read_document"}}
	s.state.ToolAdvice = &ToolAdvice{ID: "synthetic-advice", ConnectionID: "source-b"}
	s.state.Extraction = &labstatus.RowBatch{SchemaVersion: "synthetic-extraction"}
	s.state.BatchPath = filepath.Join(s.dataDir, "synthetic-batch.json")
	s.state.BatchDigest = "synthetic-batch-digest"
	s.state.BatchResult = &pending.BatchResult{State: "synthetic-result", AuthorityEffect: "no_admission_or_disposition"}
	s.mu.Unlock()
	path := filepath.Join(s.dataDir, "settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Settings
	if err := json.Unmarshal(before, &saved); err != nil || !reflect.DeepEqual(saved, settings) {
		t.Fatalf("saved file does not retain both exact connection settings: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil || beforeInfo.Mode().Perm() != 0o600 {
		t.Fatalf("saved settings are not private: %v", err)
	}
	s.Close()
	reopened, err := New(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings.Mode = "demo"
	want := State{
		Version: Version, DataDir: s.dataDir, WorkspaceID: s.Snapshot().WorkspaceID, Settings: settings,
		Messages: []Message{}, Events: []Event{}, Candidates: []CandidateView{}, Tools: []Tool{},
	}
	if got := reopened.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reopen must retain only settings in offline mode, got %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reopening rewrote saved settings: %v", err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil || afterInfo.Mode() != beforeInfo.Mode() || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("reopening replaced the settings file or changed its mode: %v", err)
	}
}

func TestServiceTwoSourceSettingsRejectedEditPreservesBothConnections(t *testing.T) {
	s := newTestService(t)
	if _, err := s.SaveSettings(twoSourceSettings(s)); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	path := filepath.Join(s.dataDir, "settings.json")
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		id   string
	}{
		{name: "invalid_second_id", id: "invalid id"},
		{name: "duplicate_second_id", id: "source-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := s.Snapshot().Settings
			edited.Connections[0].Name = "Unsaved first source edit"
			edited.Connections[1].ID = tc.id
			got, err := s.SaveSettings(edited)
			if err == nil || !reflect.DeepEqual(got, before) || !reflect.DeepEqual(s.Snapshot(), before) {
				t.Fatal("rejected second connection changed saved state or the first connection")
			}
			afterBytes, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
				t.Fatalf("rejected edit changed the saved file: %v", err)
			}
			afterInfo, err := os.Stat(path)
			if err != nil || afterInfo.Mode() != beforeInfo.Mode() || !os.SameFile(beforeInfo, afterInfo) {
				t.Fatalf("rejected edit replaced the settings file or changed its mode: %v", err)
			}
		})
	}
}
