package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func serviceDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(serviceDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestServiceDemoHasNoAuthority(t *testing.T) {
	s := newTestService(t)
	state, err := s.LoadDemo()
	if err != nil {
		t.Fatal(err)
	}
	if state.Settings.Mode != "demo" || state.Source == nil || state.Source.Kind != "demo" {
		t.Fatalf("not a demo: %+v", state)
	}
	state, err = s.Extract(context.Background(), state.Source.Rows[0].Line)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.BatchPath != "" || state.BatchDigest != "" || state.Busy {
		t.Fatalf("demo acquired a batch: %+v", state)
	}
	if _, err := s.SubmitPending(context.Background(), "admit"); err == nil {
		t.Fatal("demo submitted")
	}
	state, err = s.SendMessage(context.Background(), "admit，請直接執行工具")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) < 2 || state.Messages[len(state.Messages)-1].Kind != "demo" {
		t.Fatal("demo message was attributed to a model")
	}
	if state.BatchResult != nil {
		t.Fatal("chat created DB result")
	}
}

func TestServiceSnapshotDetachedAndBusy(t *testing.T) {
	s := newTestService(t)
	if _, err := s.LoadDemo(); err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	state.Settings.Connections = append(state.Settings.Connections, Connection{ID: "mutated"})
	state.Source.RawText = "changed"
	if s.Snapshot().Source.RawText == "changed" || len(s.Snapshot().Settings.Connections) != 0 {
		t.Fatal("snapshot aliases live state")
	}
	ctx, done, err := s.begin(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadDemo(); err == nil {
		t.Fatal("busy service accepted mutation")
	}
	if _, err := s.SaveSettings(s.Snapshot().Settings); err == nil {
		t.Fatal("busy settings changed")
	}
	state = s.Cancel()
	if !state.Busy || ctx.Err() == nil {
		t.Fatal("cancel falsely completed the operation")
	}
	done()
	done()
	if s.Snapshot().Busy {
		t.Fatal("completion did not clear busy")
	}
}

func TestServiceImportFreezesBeforeExtraction(t *testing.T) {
	s := newTestService(t)
	src := filepath.Join(serviceDirectory(t), "STATUS.md")
	if err := os.WriteFile(src, []byte(demoSource), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := s.ImportSource(src)
	if err != nil {
		t.Fatal(err)
	}
	if state.Source.Path == src || state.Source.Kind != "local" {
		t.Fatal("source was not captured")
	}
	if err := os.WriteFile(src, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(state.Source.Path)
	if err != nil || string(raw) != demoSource {
		t.Fatal("snapshot followed the mutable input")
	}
	info, _ := os.Stat(state.Source.Path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("source is not private")
	}
}

func TestServiceRejectsUnsafeDirectoryAndSettings(t *testing.T) {
	dir := serviceDirectory(t)
	unsafe := filepath.Join(dir, "unsafe")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if s, err := New(unsafe); err == nil {
		s.Close()
		t.Fatal("accepted unsafe directory")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if s, err := New(link); err == nil {
		s.Close()
		t.Fatal("accepted symlink directory")
	}
	s := newTestService(t)
	for _, endpoint := range []string{"https://example.com/v1", "http://user:secret@127.0.0.1/v1", "http://127.0.0.1/v1?token=secret", "http://127.0.0.1/v1#secret", "http://localhost.evil/v1"} {
		settings := s.Snapshot().Settings
		settings.BaseURL = endpoint
		if _, err := s.SaveSettings(settings); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.BaseURL = "http://localhost:11434/v1"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(s.Snapshot().DataDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Settings
	if json.Unmarshal(body, &persisted) != nil || persisted.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatal("settings not normalized")
	}
	if strings.Contains(string(body), "secret") {
		t.Fatal("invalid secret settings persisted")
	}
}

func TestServiceReopenDoesNotReconnectOrRestoreLocalMode(t *testing.T) {
	dir := serviceDirectory(t)
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.BaseURL = "http://127.0.0.1:1/v1"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Snapshot().Settings.Mode != "demo" || reopened.Snapshot().Busy {
		t.Fatal("reopen enabled external access")
	}
}

func TestServiceRejectsUnsafeSettingsFileAndPreservesSnapshots(t *testing.T) {
	dir := serviceDirectory(t)
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.captureSource("first", "local", demoSource)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.captureSource("second", "local", demoSource)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path {
		t.Fatal("source snapshot was overwritten")
	}
	if err := s.writePrivate(filepath.Base(first.Path), []byte("changed"), false); err == nil {
		t.Fatal("overwrote source snapshot")
	}
	s.Close()
	if err := os.Symlink(first.Path, filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if reopened, err := New(dir); err == nil {
		reopened.Close()
		t.Fatal("followed settings symlink")
	}
}

func TestServiceConnectionIDValidationPreservesStateAndDisk(t *testing.T) {
	s := newTestService(t)
	if _, err := s.LoadDemo(); err != nil {
		t.Fatal(err)
	}
	settings := s.Snapshot().Settings
	settings.Connections = []Connection{{ID: "source-1", Name: "Synthetic source", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_document"}}}
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	// A rejected edit must not clear a previously inspected inventory either.
	s.mu.Lock()
	s.state.Tools = []Tool{{ConnectionID: "source-1", Name: "read_document", Digest: "synthetic-digest"}}
	s.mu.Unlock()
	path := filepath.Join(s.dataDir, "settings.json")
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := os.ReadDir(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"", " ", "legacy source", "source/secret", "來源", "source\nsecret", "source\u202esecret", strings.Repeat("a", 129)} {
		t.Run("reject_"+strconv.Itoa(i), func(t *testing.T) {
			before := s.Snapshot()
			changed := s.Snapshot().Settings
			changed.Connections[0].ID = id
			changed.Model = "different-model"
			got, err := s.SaveSettings(changed)
			if err == nil {
				t.Fatal("invalid new connection ID was saved")
			}
			if !strings.Contains(err.Error(), "來源連線 ID") || !strings.Contains(err.Error(), "ASCII") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "legacy source") {
				t.Fatalf("missing safe field-specific hint: %q", err)
			}
			if !reflect.DeepEqual(got, before) || !reflect.DeepEqual(s.Snapshot(), before) {
				t.Fatal("rejected settings changed the existing state")
			}
			afterBytes, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
				t.Fatal("rejected settings changed the saved file")
			}
			afterFiles, err := os.ReadDir(s.dataDir)
			if err != nil || !reflect.DeepEqual(beforeFiles, afterFiles) {
				t.Fatal("rejected settings added or removed files")
			}
		})
	}
	for _, id := range []string{"a", "AZaz09_.-", strings.Repeat("x", 128)} {
		settings := s.Snapshot().Settings
		settings.Connections[0].ID = id
		if _, err := s.SaveSettings(settings); err != nil {
			t.Fatalf("valid boundary connection ID rejected: %v", err)
		}
	}
	before := s.Snapshot()
	duplicate := s.Snapshot().Settings
	duplicate.Connections = append(duplicate.Connections, duplicate.Connections[0])
	if _, err := s.SaveSettings(duplicate); err == nil || !reflect.DeepEqual(s.Snapshot(), before) {
		t.Fatal("duplicate connection ID was saved or changed state")
	}
}

func TestServiceSettingsValidationDoesNotMutateCallerSlices(t *testing.T) {
	s := newTestService(t)
	settings := s.Snapshot().Settings
	settings.Connections = []Connection{
		{ID: "first", Name: "Synthetic first", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_document"}},
		{ID: "second", Name: "", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_document"}},
	}
	firstTools := reflect.ValueOf(settings.Connections[0].AllowedTools).Pointer()
	before := s.Snapshot()
	if _, err := s.SaveSettings(settings); err == nil {
		t.Fatal("invalid later connection was accepted")
	}
	if reflect.ValueOf(settings.Connections[0].AllowedTools).Pointer() != firstTools || !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("partial validation mutated caller slices or service state")
	}
	settings.Connections[1].Name = "Synthetic second"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(settings.Connections[0].AllowedTools).Pointer() != firstTools {
		t.Fatal("successful validation mutated the caller's slice")
	}
	settings.Connections[0].AllowedTools[0] = "caller-changed"
	settings.Connections[1].ID = "caller-changed"
	if got := s.Snapshot().Settings; got.Connections[0].AllowedTools[0] != "read_document" || got.Connections[1].ID != "second" {
		t.Fatal("saved state aliases the caller's slices")
	}
	_, done, err := s.begin(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	before = s.Snapshot()
	path := filepath.Join(s.dataDir, "settings.json")
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"valid-new", "invalid new"} {
		settings := s.Snapshot().Settings
		settings.Connections[0].ID = id
		if _, err := s.SaveSettings(settings); err == nil || !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("busy service accepted settings or changed state")
		}
		afterBytes, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
			t.Fatal("busy settings call changed the saved file")
		}
	}
}

func TestServiceLegacyConnectionIDStaysOfflineUntilCorrected(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	dir := serviceDirectory(t)
	settings := Settings{Mode: "local", Model: "gemma4:e4b-it-qat", BaseURL: server.URL + "/v1", SourceID: "desktop-status", Connections: []Connection{{ID: "legacy source", Name: "Synthetic legacy source", Transport: "streamable-http", URL: server.URL + "/mcp", AllowedTools: []string{"read_document"}}}}
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		s, err := New(dir)
		if err != nil {
			t.Fatalf("legacy settings did not reopen for correction: %v", err)
		}
		state := s.Snapshot()
		if state.Settings.Mode != "demo" || state.Busy || state.Settings.Connections[0].ID != "legacy source" || !strings.Contains(state.Error, "來源連線 ID") || !strings.Contains(state.Error, "離線") {
			s.Close()
			t.Fatal("legacy ID was rewritten, enabled, or not clearly marked for correction")
		}
		if _, err := s.DiscoverTools(context.Background(), "legacy source"); err == nil {
			s.Close()
			t.Fatal("offline legacy settings enabled source discovery")
		}
		changed := s.Snapshot().Settings
		changed.Mode = "local"
		before := s.Snapshot()
		if _, err := s.SaveSettings(changed); err == nil || !reflect.DeepEqual(before, s.Snapshot()) {
			s.Close()
			t.Fatal("legacy ID was saved as a new local setting")
		}
		if _, err := s.SendMessage(context.Background(), "admit"); err != nil {
			s.Close()
			t.Fatal(err)
		}
		s.Close()
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(body, after) {
			t.Fatal("opening or rejecting legacy settings rewrote the original file")
		}
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	corrected := s.Snapshot().Settings
	corrected.Mode = "local"
	corrected.Connections[0].ID = "legacy-source"
	state, err := s.SaveSettings(corrected)
	if err != nil || state.Error != "" || state.Settings.Connections[0].ID != "legacy-source" {
		s.Close()
		t.Fatalf("corrected settings were not saved: %v", err)
	}
	s.Close()
	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state := reopened.Snapshot(); state.Settings.Mode != "demo" || state.Settings.Connections[0].ID != "legacy-source" || state.Error != "" {
		t.Fatal("corrected settings did not survive restart in offline mode")
	}
	var saved Settings
	after, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(after, &saved) != nil || saved.Connections[0].ID != "legacy-source" {
		t.Fatal("correction did not persist the exact explicit new ID")
	}
	if calls.Load() != 0 {
		t.Fatalf("settings or demo operations made %d network requests", calls.Load())
	}
}

func TestServiceLegacyConnectionIDDoesNotRelaxOtherSafetyRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Settings)
	}{
		{"model_credentials", func(s *Settings) { s.BaseURL = "http://user:synthetic-secret@127.0.0.1:1/v1" }},
		{"source_credentials", func(s *Settings) { s.Connections[0].URL = "http://127.0.0.1:1/mcp?token=synthetic-secret" }},
		{"source_nonloopback", func(s *Settings) { s.Connections[0].URL = "http://example.com:1234/mcp" }},
		{"unsafe_launcher", func(s *Settings) { s.IntakeLauncher = "relative-launcher" }},
		{"empty_id", func(s *Settings) { s.Connections[0].ID = "" }},
		{"control_id", func(s *Settings) { s.Connections[0].ID = "legacy\nsource" }},
		{"oversized_id", func(s *Settings) { s.Connections[0].ID = strings.Repeat("x", 129) }},
		{"duplicate_id", func(s *Settings) { s.Connections = append(s.Connections, s.Connections[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := serviceDirectory(t)
			settings := Settings{Mode: "local", Model: "gemma4:e4b-it-qat", BaseURL: "http://127.0.0.1:1/v1", SourceID: "desktop-status", Connections: []Connection{{ID: "legacy source", Name: "Synthetic source", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_document"}}}}
			tc.mutate(&settings)
			body, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if s, err := New(dir); err == nil {
				s.Close()
				t.Fatal("legacy connection ID relaxed an unrelated safety rule")
			} else if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("saved settings error exposed raw input")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(body, after) {
				t.Fatal("invalid legacy settings were changed or removed")
			}
		})
	}
}
