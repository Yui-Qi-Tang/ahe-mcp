package desktop

import (
	"context"
	"encoding/json"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// This opt-in smoke uses the installed binary, an explicitly selected repository,
// and a retained private workspace. It never enables an external model or DB.
func TestCodebaseOfflineLive(t *testing.T) {
	work, repo := os.Getenv("AHE_CODEBASE_TEST_WORK"), os.Getenv("AHE_CODEBASE_TEST_REPO")
	if work == "" || repo == "" {
		t.Skip("requires explicit local test workspace and repository")
	}
	if !filepath.IsAbs(work) || !filepath.IsAbs(repo) {
		t.Fatal("absolute paths required")
	}
	s, err := New(work)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.CodebasePreset(repo)
	if err != nil {
		t.Fatal(err)
	}
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.Connections = []Connection{c}
	if _, err = s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	state, err := s.DiscoverTools(context.Background(), c.ID)
	if err != nil {
		cfg, e := s.codebaseConnection(c.ID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = sourcemcp.Discover(context.Background(), cfg)
		t.Fatalf("discover: %v", e)
	}
	if len(state.Tools) == 0 {
		t.Fatal("no read tools")
	}
	t.Logf("discovered %d offline read tools", len(state.Tools))
	if _, err = s.IndexCodebase(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int64
	model := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		if posts.Add(1) == 1 {
			writeModelText(w, `{"outcome":"suggested","tool_name":"list_projects","arguments":{},"reason":"先核對目前索引專案。"}`)
		} else {
			writeModelText(w, "這是合成模型回答，用於驗證一次本機工具讀取；不是模型品質評估。")
		}
	})
	localSettings(t, s, model)
	if _, err = s.DiscoverTools(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	state, err = s.StartSourceChat(context.Background(), c.ID, "目前有哪些索引專案？")
	if err != nil || state.SourceChat == nil {
		t.Fatalf("plan: %v", err)
	}
	if posts.Load() != 1 || state.SourceChat.Source != nil {
		t.Fatal("planning executed a read")
	}
	id := state.SourceChat.ID
	state, err = s.ConfirmSourceChat(context.Background(), id)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if state.SourceChat.Status != "completed" || state.Source == nil || state.Source.Capture == nil || posts.Load() != 2 || state.BatchPath != "" {
		t.Fatal("read did not complete separately from intake")
	}
	if _, err = s.ConfirmSourceChat(context.Background(), id); err == nil || posts.Load() != 2 {
		t.Fatal("confirmation was replayed")
	}
	t.Log("offline index, one confirmed chat read, retained receipt, replay rejection: PASS")
	var projects struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}
	if json.Unmarshal([]byte(state.Source.RawText), &projects) != nil || len(projects.Projects) != 1 {
		t.Fatal("expected exactly one indexed project")
	}
	cfg, err := s.codebaseConnection(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range state.Tools {
		if tool.Name == "get_code_snippet" {
			args, _ := json.Marshal(map[string]string{"project": projects.Projects[0].Name, "qualified_name": "Build"})
			result, e := sourcemcp.Call(context.Background(), cfg, recordedTool(tool), string(args))
			if e != nil {
				t.Fatal(e)
			}
			source, e := s.captureToolResult(context.Background(), cfg, recordedTool(tool), string(args), result)
			if e != nil {
				t.Fatal(e)
			}
			if source.Capture.CodeCitation == nil {
				t.Fatal("no file-verified citation")
			}
			reopened, e := InspectSource(context.Background(), source.Capture.Receipt.Path)
			if e != nil || reopened.Capture.CodeCitation == nil {
				t.Fatalf("offline code receipt: %v", e)
			}
			t.Log("snippet matches local file, content version retained, offline receipt reopened: PASS")
		}
	}
}
