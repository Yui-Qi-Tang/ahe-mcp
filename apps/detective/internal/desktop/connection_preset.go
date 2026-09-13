package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

var codebaseReadTools = []string{"list_projects", "index_status", "get_graph_schema", "search_graph", "search_code", "get_code_snippet", "trace_path", "get_architecture", "check_index_coverage"}

func codebaseID(root string) string {
	hash := sha256.Sum256([]byte(root))
	return "codebase-" + hex.EncodeToString(hash[:6])
}

// CodebasePreset prepares a connection draft only. It never starts a process,
// indexes a repository, saves settings, or obtains evidence writer authority.
func (s *Service) CodebasePreset(repository string) (Connection, error) {
	if runtime.GOOS != "darwin" || !filepath.IsAbs(repository) || !safeText(repository, 4096) {
		return Connection{}, errors.New("離線 Codebase 預設目前限 macOS，請選擇本機 repository 資料夾")
	}
	root, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return Connection{}, errors.New("無法確認所選資料夾")
	}
	home, err := os.UserHomeDir()
	if err != nil || root == "/" || root == home {
		return Connection{}, errors.New("請選擇單一 repository，不要選家目錄或磁碟根目錄")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Connection{}, errors.New("所選路徑不是資料夾")
	}
	id := codebaseID(root)
	c := Connection{ID: id, Name: "Codebase · " + filepath.Base(root), Transport: "stdio", Command: filepath.Join(home, ".local", "bin", "codebase-memory-mcp"), Directory: root, CodebaseCache: filepath.Join(s.dataDir, id), AllowedTools: append([]string(nil), codebaseReadTools...)}
	if err := sourcemcp.Validate(sourceConfig(c)); err != nil {
		return Connection{}, errors.New("找不到已安裝且由目前使用者持有的 codebase-memory-mcp；沒有下載或啟動程式")
	}
	return c, nil
}

// codebaseConnection is the backend chat boundary, not a UI visibility test.
// Generic stdio, HTTP, OAuth, editable argv, and non-preset cache paths fail.
func (s *Service) codebaseConnection(id string) (sourcemcp.Config, error) {
	state := s.Snapshot()
	if state.Settings.Mode != "local" {
		return sourcemcp.Config{}, errors.New("codebase requires explicit local mode")
	}
	for _, c := range state.Settings.Connections {
		if c.ID != id {
			continue
		}
		if err := s.validateCodebaseConnection(c); err != nil {
			return sourcemcp.Config{}, err
		}
		if err := s.root.Mkdir(c.ID, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return sourcemcp.Config{}, err
		}
		if err := checkDirectoryPath(c.CodebaseCache, false); err != nil {
			return sourcemcp.Config{}, err
		}
		info, err := os.Lstat(c.CodebaseCache)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return sourcemcp.Config{}, errors.New("codebase cache must be private")
		}
		return sourceConfig(c), nil
	}
	return sourcemcp.Config{}, errors.New("codebase preset not found")
}

func (s *Service) validateCodebaseConnection(c Connection) error {
	home, err := os.UserHomeDir()
	if err != nil || runtime.GOOS != "darwin" || c.Transport != "stdio" || c.URL != "" || len(c.Args) != 0 || c.Directory == "" || c.ID != codebaseID(c.Directory) || c.Command != filepath.Join(home, ".local", "bin", "codebase-memory-mcp") || c.CodebaseCache != filepath.Join(s.dataDir, c.ID) || !slices.Equal(c.AllowedTools, codebaseReadTools) {
		return errors.New("chat accepts only the unchanged offline codebase preset")
	}
	root, err := filepath.EvalSymlinks(c.Directory)
	if err != nil || root != c.Directory || root == "/" || root == home || strings.HasPrefix(c.CodebaseCache, root+string(filepath.Separator)) {
		return errors.New("invalid codebase repository boundary")
	}
	return sourcemcp.Validate(sourceConfig(c))
}

// IndexCodebase is a separate explicit action; no model, intake or review runs.
func (s *Service) IndexCodebase(parent context.Context, id string) (State, error) {
	ctx, done, err := s.begin(parent, "codebase_index")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	config, err := s.codebaseConnection(id)
	if err != nil {
		return s.finish(done, err, "只接受未變更的離線 Codebase 預設；沒有啟動索引。")
	}
	config.AllowedTools = []string{"index_repository"}
	tools, err := sourcemcp.Discover(ctx, config)
	if err != nil || len(tools) != 1 {
		return s.finish(done, errors.New("index tool unavailable"), "離線索引工具不可用；請確認程式版本、快取衝突或網路封鎖支援。")
	}
	args, err := json.Marshal(map[string]any{"repo_path": config.Directory, "persistence": false})
	if err != nil {
		return s.finish(done, err, "無法建立索引參數。")
	}
	result, err := sourcemcp.Call(ctx, config, tools[0], string(args))
	if err != nil {
		return s.finish(done, err, "索引未確認完成；沒有自行重試、啟用監看或修改 repository。")
	}
	_, err = s.captureToolResult(ctx, config, tools[0], string(args), result)
	if err != nil {
		return s.finish(done, err, "索引回覆未能保存；請另查索引狀態。")
	}
	return s.finish(done, nil, "索引工具已回覆並保存紀錄；請取得工具清單後在聊天室查 index_status。這不是證據入庫。")
}
