package desktop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestWorkspaceIdentityIsStablePathLabel(t *testing.T) {
	base := serviceDirectory(t)
	identifiers := make(map[string]bool)
	for _, parent := range []string{"甲", "乙"} {
		dir := filepath.Join(base, parent, "same-basename")
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		body, err := json.Marshal(s.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(dir))
		want := hex.EncodeToString(digest[:6])
		if wire["workspaceID"] != want || wire["dataDir"] != dir {
			t.Fatalf("workspace identity = %v, want %q with exact data directory", wire["workspaceID"], want)
		}
		if identifiers[want] {
			t.Fatal("different paths with the same basename share an identifier")
		}
		identifiers[want] = true
		s.Close()
		reopened, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		if got := reopened.Snapshot(); got.WorkspaceID != want || got.DataDir != dir {
			t.Fatal("reopening the same path changed workspace identity")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("workspace identification created a file: %v", err)
		}
	}
}

func TestWorkspaceIdentitySurvivesSettingsAndDemoWithoutIdentityFiles(t *testing.T) {
	s := newTestService(t)
	initial := s.Snapshot()
	// Coordinates deliberately cannot start a real source, model or AHE service.
	settings := twoSourceSettings(s)
	state, err := s.SaveSettings(settings)
	if err != nil || state.WorkspaceID != initial.WorkspaceID || state.DataDir != initial.DataDir {
		t.Fatalf("saving settings changed workspace identity: %v", err)
	}
	settingsPath := filepath.Join(s.dataDir, "settings.json")
	saved, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Settings
	if err := json.Unmarshal(saved, &decoded); err != nil || !reflect.DeepEqual(decoded, settings) {
		t.Fatalf("identity changed settings content: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(saved, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["workspaceID"]; exists {
		t.Fatal("workspace identity was persisted into settings")
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil || !reflect.DeepEqual(chatFileNames(entries), []string{"settings.json"}) {
		t.Fatalf("saving settings created an identity file: %v", err)
	}
	state, err = s.LoadDemo()
	if err != nil || state.WorkspaceID != initial.WorkspaceID || state.DataDir != initial.DataDir {
		t.Fatalf("new offline work changed workspace identity: %v", err)
	}
	if state.Source == nil || state.Source.Kind != "demo" || state.Settings.Mode != "demo" || len(state.Tools) != 0 || state.BatchPath != "" {
		t.Fatal("workspace identity changed the offline demonstration boundary")
	}
	for range 3 {
		detached := s.Snapshot()
		if detached.WorkspaceID != initial.WorkspaceID {
			t.Fatal("reading a snapshot changed workspace identity")
		}
		detached.WorkspaceID = "renderer-owned-change"
	}
	entries, err = os.ReadDir(s.dataDir)
	wantNames := []string{"settings.json", filepath.Base(state.Source.Path)}
	slices.Sort(wantNames)
	if err != nil || !reflect.DeepEqual(chatFileNames(entries), wantNames) {
		t.Fatalf("demo or snapshots created files beyond existing settings and source: %v", err)
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(saved, after) {
		t.Fatalf("demo or snapshots rewrote settings: %v", err)
	}
}
