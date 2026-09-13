package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func TestCodebasePlannerCancellationHasNoSourceCall(t *testing.T) {
	s := newTestService(t)
	c, err := s.CodebasePreset(t.TempDir())
	if err != nil {
		t.Skip("installed macOS Codebase preset required")
	}
	started := make(chan struct{})
	server := modelServer(t, func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "recent-question") || strings.Contains(string(body), "older-question") {
			t.Error("clarification history was absent or unbounded")
		}
		close(started)
		<-r.Context().Done()
	})
	settings := s.Snapshot().Settings
	settings.Connections = []Connection{c}
	settings.Mode = "local"
	settings.BaseURL = server.URL + "/v1"
	if _, err = s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	cfg := sourceConfig(c)
	sort.Strings(cfg.AllowedTools)
	body, _ := json.Marshal(cfg)
	schema := `{"type":"object","properties":{}}`
	tool := sourcemcp.Tool{Name: "list_projects", InputSchemaJSON: schema, SchemaSHA256: sourceArtifact("", []byte(schema)).SHA256, ConfigSHA256: sourceArtifact("", body).SHA256, InventorySHA256: strings.Repeat("a", 64)}
	s.state.Tools = []Tool{{ConnectionID: c.ID, Name: tool.Name, InputSchemaJSON: schema, SchemaSHA256: tool.SchemaSHA256, ConfigSHA256: tool.ConfigSHA256, InventorySHA256: tool.InventorySHA256, Digest: toolDigest(tool)}}
	s.state.SourceChat = &SourceChat{ConnectionID: c.ID, Status: "needs_input"}
	s.state.Messages = []Message{{Role: "user", Text: "older-question"}, {Role: "user", Text: "recent-question"}, {Role: "assistant", Text: "which project?"}, {Role: "user", Text: "this project"}, {Role: "assistant", Text: "which function?"}}
	finished := make(chan error, 1)
	go func() { _, err := s.StartSourceChat(context.Background(), c.ID, "read"); finished <- err }()
	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("planner did not start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("planner timeout")
	}
	s.Cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled planner succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation timeout")
	}
	state := s.Snapshot()
	if state.Busy || state.SourceChat != nil || state.Source != nil || state.BatchPath != "" {
		t.Fatal("cancelled planner published read or intake")
	}
	files, err := os.ReadDir(c.CodebaseCache)
	if err != nil || len(files) != 0 {
		t.Fatal("planner executed Codebase")
	}
}

func TestSourceChatRejectsOtherMCPBeforeModelOrSource(t *testing.T) {
	var posts atomic.Int64
	s, calls, requests := adviceService(t, func(w http.ResponseWriter, _ *http.Request) { posts.Add(1); writeModelText(w, suggestedSourceTool) })
	baseline := requests.Load()
	for _, transport := range []string{"streamable-http", "stdio", "atlassian-oauth"} {
		s.mu.Lock()
		s.state.Settings.Connections[0].Transport = transport
		s.mu.Unlock()
		if _, err := s.StartSourceChat(context.Background(), "fixture", "read"); err == nil {
			t.Fatal("generic connection reached planner")
		}
		s.mu.Lock()
		s.state.SourceChat = &SourceChat{ID: "read", ConnectionID: "fixture", Status: "awaiting_confirmation", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano), Advice: &ToolAdvice{Outcome: "suggested", Tool: &Tool{Name: "list_projects", ConnectionID: "fixture"}, ArgumentsJSON: "{}"}}
		s.mu.Unlock()
		if _, err := s.ConfirmSourceChat(context.Background(), "read"); err == nil {
			t.Fatal("generic connection reached executor")
		}
	}
	if posts.Load() != 0 || calls.Load() != 0 || requests.Load() != baseline {
		t.Fatal("rejected connection caused I/O")
	}
}

func TestChatReadConfirmationBoundary(t *testing.T) {
	now := time.Now()
	base := State{SourceChat: &SourceChat{ID: "read", ConnectionID: "codebase", Status: "awaiting_confirmation", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), Advice: &ToolAdvice{Outcome: "suggested", Tool: &Tool{Name: "list_projects", ConnectionID: "codebase"}, ArgumentsJSON: "{}"}}}
	if _, err := confirmedChatRead(base, "read", now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*State){
		"expired":          func(s *State) { s.SourceChat.ExpiresAt = now.Format(time.RFC3339Nano) },
		"consumed":         func(s *State) { s.SourceChat.Status = "failed" },
		"wrong connection": func(s *State) { s.SourceChat.Advice.Tool.ConnectionID = "other" },
		"index":            func(s *State) { s.SourceChat.Advice.Tool.Name = "index_repository" },
		"writer":           func(s *State) { s.SourceChat.Advice.Tool.Name = "admit_pending_proposal" },
		"abstain":          func(s *State) { s.SourceChat.Advice.Outcome = "abstain" },
		"batch":            func(s *State) { s.BatchPath = "saved-batch" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var state State
			_ = json.Unmarshal(raw, &state)
			change(&state)
			if _, err := confirmedChatRead(state, "read", now); err == nil {
				t.Fatal("invalid card accepted")
			}
		})
	}
	s := newTestService(t)
	s.state.SourceChat = base.SourceChat
	if _, err := s.NewWork(); err != nil || s.Snapshot().SourceChat != nil {
		t.Fatal("new work retained card")
	}
}

func TestCodeCitationPreservesOriginalAndRejectsMismatch(t *testing.T) {
	dir := t.TempDir()
	body := []byte("package sample\n\nfunc Build() {}\n")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), body, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := sourcemcp.Config{Directory: dir, CodebaseCache: "private-cache"}
	snippet := codeSnippet{FilePath: "main.go", StartLine: 3, EndLine: 3, Source: "func Build() {}\n"}
	raw, _ := json.Marshal(snippet)
	citation, err := codebaseCitation(cfg, "get_code_snippet", string(raw))
	if err != nil || citation == nil {
		t.Fatalf("citation: %v", err)
	}
	if citation.ExactQuote != snippet.Source || citation.FileSHA256 != sourceArtifact("", body).SHA256 || !recordedCodeCitation(citation, cfg, "get_code_snippet", string(raw)) {
		t.Fatal("citation changed original")
	}
	for _, name := range []string{"../outside.go", "/outside.go"} {
		snippet.FilePath = name
		raw, _ = json.Marshal(snippet)
		if _, err := codebaseCitation(cfg, "get_code_snippet", string(raw)); err == nil {
			t.Fatal("path escape accepted")
		}
	}
	snippet.FilePath = "main.go"
	snippet.Source = "invented"
	raw, _ = json.Marshal(snippet)
	if _, err := codebaseCitation(cfg, "get_code_snippet", string(raw)); err == nil {
		t.Fatal("invented code accepted")
	}
	if c, err := codebaseCitation(cfg, "search_graph", `{"source":"invented"}`); err != nil || c != nil {
		t.Fatal("graph relation became citation")
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Fatal(err)
	}
	snippet.FilePath = "link.go"
	raw, _ = json.Marshal(snippet)
	if _, err := codebaseCitation(cfg, "get_code_snippet", string(raw)); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestPresetForgeryRejected(t *testing.T) {
	s := newTestService(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := Connection{ID: codebaseID(dir), Name: "Codebase", Transport: "stdio", Command: filepath.Join(home, ".local", "bin", "codebase-memory-mcp"), Directory: dir, CodebaseCache: filepath.Join(s.dataDir, codebaseID(dir)), AllowedTools: append([]string(nil), codebaseReadTools...)}
	for _, field := range []string{"command", "args", "cache", "tools", "url", "root"} {
		t.Run(field, func(t *testing.T) {
			c := base
			switch field {
			case "command":
				c.Command = "/operator/mcp"
			case "args":
				c.Args = []string{"--remote"}
			case "cache":
				c.CodebaseCache = "/other-cache"
			case "tools":
				c.AllowedTools = []string{"index_repository"}
			case "url":
				c.URL = "https://remote.invalid"
			case "root":
				c.Directory = strings.TrimSuffix(dir, "/") + "/.."
			}
			if s.validateCodebaseConnection(c) == nil {
				t.Fatal("forged preset accepted")
			}
		})
	}
}
