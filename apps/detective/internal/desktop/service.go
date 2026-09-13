package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const operationTimeout = 2 * time.Minute

var errConnectionID = errors.New("來源連線 ID 須為 1–128 個 ASCII 英文字母、數字、_、. 或 -，且不可重複；設定未儲存。")

// ErrWorkspaceInUse means a cooperating process already holds this workspace.
var ErrWorkspaceInUse = errors.New("desktop workspace is already in use")

// Service owns one explicitly started desktop operation and its private files.
// The UI projection, messages and local settings never grant AHE authority.
type Service struct {
	oauthSessions  map[string]*sourcemcp.OAuthSession
	beginAtlassian func(context.Context) (*sourcemcp.AtlassianAuthorization, error)
	mu             sync.Mutex
	state          State
	dataDir        string
	root           *os.Root
	directory      *os.File
	cancel         context.CancelFunc
	opDone         chan struct{}
	closed         bool
}

// New opens a private local workspace without connecting to a model or MCP.
// Persisted local mode is deliberately reset to demo on every application start.
func New(dataDir string) (*Service, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir || dataDir == string(filepath.Separator) || !safeText(dataDir, 4096) {
		return nil, errors.New("desktop data directory must be a clean absolute path")
	}
	if err := checkDirectoryPath(dataDir, true); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, errors.New("cannot create private desktop directory")
	}
	if err := checkDirectoryPath(dataDir, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("desktop data directory requires mode 0700")
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, errors.New("cannot open private desktop directory")
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, errors.New("cannot pin desktop directory")
	}
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = directory.Close()
		_ = root.Close()
		return nil, errors.New("desktop directory changed while opening")
	}
	// The lock belongs to the pinned directory handle until Close finishes.
	// It coordinates cooperating versions only, not arbitrary filesystem writers.
	if err := unix.Flock(int(directory.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = directory.Close()
		_ = root.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrWorkspaceInUse
		}
		return nil, errors.New("cannot lock private desktop directory")
	}
	s := &Service{dataDir: dataDir, root: root, directory: directory, beginAtlassian: sourcemcp.BeginAtlassianOAuth}
	workspaceDigest := sha256.Sum256([]byte(dataDir))
	s.state = State{Version: Version, DataDir: dataDir, WorkspaceID: hex.EncodeToString(workspaceDigest[:6]), Settings: initialSettings(), Messages: []Message{}, Events: []Event{}, Candidates: []CandidateView{}, Tools: []Tool{}}
	if body, err := s.readPrivate("settings.json", 64<<10); err == nil {
		var settings Settings
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&settings) != nil || decoder.Decode(new(any)) != io.EOF {
			s.Close()
			return nil, errors.New("saved desktop settings are invalid")
		}
		// Only loading may retain an ID accepted by the earlier desktop rule.
		// All other checks remain in force, and this never rewrites the file.
		settings, err = validateSettings(settings, true)
		if err != nil {
			s.Close()
			return nil, errors.New("saved desktop settings require correction")
		}
		settings.Mode = "demo"
		s.state.Settings = settings
		for _, connection := range settings.Connections {
			if !sourcemcp.ValidConnectionID(connection.ID) {
				s.state.Error = "舊來源連線 ID 已保留於離線模式；請更正為 1–128 個 ASCII 英文字母、數字、_、. 或 -。未改寫設定檔，也未建立連線。"
				break
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.Close()
		return nil, errors.New("cannot safely read desktop settings")
	}
	return s, nil
}

func initialSettings() Settings {
	return Settings{Mode: "demo", Model: "gemma4:e4b-it-qat", BaseURL: "http://127.0.0.1:11434/v1", SourceID: "desktop-status", Connections: []Connection{}}
}

// Snapshot returns a deep detached copy suitable for an untrusted UI renderer.
func (s *Service) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state)
}

func cloneState(state State) State {
	// State contains only JSON-compatible values; this also detaches nested
	// extraction and partial-recovery slices from renderer-owned objects.
	body, _ := json.Marshal(state)
	var detached State
	_ = json.Unmarshal(body, &detached)
	return detached
}

func (s *Service) begin(ctx context.Context, action string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("desktop service is closed")
	}
	if s.state.Busy {
		return nil, nil, errors.New("another desktop operation is still running")
	}
	if ctx == nil || ctx.Err() != nil {
		return nil, nil, errors.New("desktop operation requires an active context")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	s.cancel = cancel
	s.opDone = make(chan struct{})
	operationDone := s.opDone
	s.state.Busy = true
	s.state.Operation = action
	s.state.Error = ""
	switch action {
	case "evidence_search", "evidence_search_demo", "settings", "demo":
		// Search belongs to this request and saved launcher, never the next one.
		s.state.Search = nil
	}
	// Advice belongs to one observed inventory and operation only. A refresh,
	// setting change, source operation or chat cannot silently reuse it.
	s.state.ToolAdvice = nil
	if s.state.SourceChat != nil && s.state.SourceChat.Status == "awaiting_confirmation" && action != "codebase_chat_confirm" {
		s.state.SourceChat.Status = "superseded"
	}
	s.eventLocked(action, "running", "操作已開始；尚未確認完成。")
	var once sync.Once
	done := func() {
		once.Do(func() {
			cancel()
			s.mu.Lock()
			defer s.mu.Unlock()
			s.state.Busy = false
			s.state.Operation = ""
			s.cancel = nil
			s.opDone = nil
			close(operationDone)
		})
	}
	return ctx, done, nil
}

// finish deliberately does not expose raw upstream errors or diagnostics.
func (s *Service) finish(done func(), err error, detail string) (State, error) {
	s.mu.Lock()
	status := "completed"
	if err != nil {
		status = "failed"
		s.state.Error = detail
	}
	s.eventLocked(s.state.Operation, status, detail)
	s.mu.Unlock()
	done()
	if err != nil {
		return s.Snapshot(), errors.New(detail)
	}
	return s.Snapshot(), nil
}

func (s *Service) eventLocked(action, status, detail string) {
	s.state.Events = append(s.state.Events, Event{ID: newID(), Action: action, Status: status, Detail: detail, Time: time.Now().UTC().Format(time.RFC3339Nano)})
	if len(s.state.Events) > 100 {
		s.state.Events = append([]Event(nil), s.state.Events[len(s.state.Events)-100:]...)
	}
}

func (s *Service) messageLocked(role, text, kind string) {
	s.state.Messages = append(s.state.Messages, Message{ID: newID(), Role: role, Text: text, Kind: kind, Time: time.Now().UTC().Format(time.RFC3339Nano)})
	if len(s.state.Messages) > 40 {
		s.state.Messages = append([]Message(nil), s.state.Messages[len(s.state.Messages)-40:]...)
	}
}

// Cancel requests cancellation without pretending the operation or DB rollback
// is complete. Only the running operation clears Busy after its cleanup.
func (s *Service) Cancel() State {
	s.mu.Lock()
	if s.state.Operation == "task_run" && s.state.Task != nil {
		switch s.state.Task.Status {
		case "selected", "abstained", "failed":
			// Record publication has finished; do not imply a completed local
			// selection will be rewritten into a cancellation failure.
			s.mu.Unlock()
			return s.Snapshot()
		}
	}
	if s.cancel != nil {
		s.cancel()
		detail := "已要求取消；先前提交可能已生效，請保留同一批次並查詢或恢復。"
		if s.state.Operation == "evidence_search" || s.state.Operation == "evidence_search_demo" {
			detail = "已要求取消唯讀查詢；請等待程序結束。本操作未發出入庫或審查操作。"
		}
		if s.state.Operation == "task_run" {
			detail = "已要求取消選段；請等待失敗紀錄保存。本操作不會送入 pending 或寫入 AHE。"
		}
		s.eventLocked(s.state.Operation, "cancel_requested", detail)
	}
	s.mu.Unlock()
	return s.Snapshot()
}

// Close cancels and waits for the owned operation before releasing file handles.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	done := s.opDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.mu.Lock()
	s.forgetAllSourceLoginsLocked()
	s.mu.Unlock()
	_ = s.directory.Close()
	_ = s.root.Close()
}

// SaveSettings saves only explicit noncredential coordinates, never connecting.
func (s *Service) SaveSettings(settings Settings) (State, error) {
	for _, c := range settings.Connections {
		if c.CodebaseCache != "" {
			if err := s.validateCodebaseConnection(c); err != nil {
				return s.Snapshot(), errors.New("Codebase 預設不可改成其他程式、參數或資料夾；請移除設定後重新選擇 repository。")
			}
		}
	}
	settings, err := validateSettings(settings, false)
	if err != nil {
		// Validation is side-effect free, including the existing UI projection.
		if errors.Is(err, errConnectionID) {
			return s.Snapshot(), errConnectionID
		}
		return s.Snapshot(), errors.New("設定不符合本機模式、來源或 launcher 的限制；未儲存。")
	}
	_, done, err := s.begin(context.Background(), "settings")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	body, err := json.Marshal(settings)
	if err == nil {
		err = s.writePrivate("settings.json", body, true)
	}
	if err != nil {
		return s.finish(done, err, "設定無法安全保存；未建立連線。")
	}
	s.mu.Lock()
	s.state.Settings = settings
	s.state.Task = nil
	s.state.SourceChat = nil
	s.forgetAllSourceLoginsLocked()
	s.state.Tools = []Tool{}
	s.mu.Unlock()
	return s.finish(done, nil, "設定已保存；沒有自動連線，也沒有授予工具或寫入權限。")
}

func validateSettings(settings Settings, allowLegacyIDs bool) (Settings, error) {
	if settings.Mode != "demo" && settings.Mode != "local" {
		return Settings{}, errors.New("unsupported desktop mode")
	}
	if !safeText(settings.Model, 128) || !safeText(settings.SourceID, 256) {
		return Settings{}, errors.New("invalid model or source identity")
	}
	endpoint, err := localEndpoint(settings.BaseURL)
	if err != nil {
		return Settings{}, err
	}
	settings.BaseURL = endpoint
	for _, command := range []string{settings.IntakeLauncher, settings.QueryLauncher, settings.ReviewLauncher} {
		if command != "" && (!safeText(command, 4096) || !filepath.IsAbs(command) || filepath.Clean(command) != command) {
			return Settings{}, errors.New("launcher must be one clean absolute executable path")
		}
	}
	if len(settings.Connections) > 16 {
		return Settings{}, errors.New("too many source connections")
	}
	settings.Connections = append([]Connection{}, settings.Connections...)
	seen := map[string]bool{}
	for i := range settings.Connections {
		c := &settings.Connections[i]
		if err := sourcemcp.ValidateProcessOptions(sourceConfig(*c)); err != nil {
			return Settings{}, err
		}
		c.Args = append([]string(nil), c.Args...)
		validID := sourcemcp.ValidConnectionID(c.ID)
		if allowLegacyIDs && !validID {
			validID = safeText(c.ID, 128)
		}
		if !validID || seen[c.ID] {
			return Settings{}, errConnectionID
		}
		if !safeText(c.Name, 256) || len(c.AllowedTools) > 32 {
			return Settings{}, errors.New("invalid source connection")
		}
		seen[c.ID] = true
		if c.Transport == "stdio" {
			if !safeText(c.Command, 4096) || !filepath.IsAbs(c.Command) || filepath.Clean(c.Command) != c.Command || c.URL != "" {
				return Settings{}, errors.New("invalid source launcher")
			}
		} else if c.Transport == "atlassian-oauth" {
			if err := sourcemcp.Validate(sourceConfig(*c)); err != nil {
				return Settings{}, err
			}
		} else if c.Transport == "streamable-http" {
			if c.Command != "" || !safeLocalSourceURL(c.URL) {
				return Settings{}, errors.New("source HTTP requires a credential-free loopback URL")
			}
		} else {
			return Settings{}, errors.New("unsupported source transport")
		}
		for _, tool := range c.AllowedTools {
			if !safeText(tool, 128) {
				return Settings{}, errors.New("invalid allowed source tool")
			}
		}
		c.AllowedTools = append([]string{}, c.AllowedTools...)
	}
	return settings, nil
}

func safeText(text string, limit int) bool {
	if strings.TrimSpace(text) == "" || len(text) > limit || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func newID() string {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(nonce[:])
}

func checkDirectoryPath(path string, allowMissing bool) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			if !(allowMissing && errors.Is(err, os.ErrNotExist)) {
				return errors.New("desktop directory path is unavailable")
			}
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("desktop directory path must not contain symlinks")
		}
		if current == string(filepath.Separator) {
			break
		}
	}
	return nil
}

func (s *Service) privateDirectory() error {
	info, err := s.directory.Stat()
	current, currentErr := os.Lstat(s.dataDir)
	if err != nil || currentErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !os.SameFile(info, current) {
		return errors.New("desktop directory is no longer private")
	}
	return checkDirectoryPath(s.dataDir, false)
}

func (s *Service) readPrivate(name string, limit int64) ([]byte, error) {
	if err := s.privateDirectory(); err != nil {
		return nil, err
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("desktop file is not private and regular")
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("desktop file changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("desktop file exceeds its bound")
	}
	return body, nil
}

func (s *Service) writePrivate(name string, body []byte, replace bool) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("invalid desktop filename")
	}
	if err := s.privateDirectory(); err != nil {
		return err
	}
	if info, err := s.root.Lstat(name); err == nil {
		if !replace || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("desktop output already exists or is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect desktop output")
	}
	temporary := ".desktop-" + newID() + ".tmp"
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("cannot create desktop output")
	}
	defer s.root.Remove(temporary) // Only this operation's new private temporary name.
	if _, err = file.Write(body); err != nil {
		_ = file.Close()
		return errors.New("cannot write desktop output")
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("cannot sync desktop output")
	}
	if err = file.Close(); err != nil {
		return errors.New("cannot close desktop output")
	}
	if replace {
		err = s.root.Rename(temporary, name)
	} else {
		err = s.root.Link(temporary, name)
	}
	if err != nil {
		return errors.New("cannot publish desktop output without replacement")
	}
	if err = s.directory.Sync(); err != nil {
		return errors.New("desktop output durability is uncertain; retain the same file")
	}
	return nil
}

// captureSource persists exact bounded bytes before model access. It does not
// replace the active source or modify any existing batch or review projection.
func (s *Service) captureSource(title, kind, raw string) (SourceView, error) {
	if kind != "local" && kind != "mcp" && kind != "demo" && kind != "mcp-result" && kind != "mcp-provenance" && kind != "local-provenance" {
		return SourceView{}, errors.New("unsupported source kind")
	}
	name := "source-" + newID() + ".md"
	path := filepath.Join(s.dataDir, name)
	document, err := labstatus.RestoreDocument(path, raw)
	if err != nil {
		return SourceView{}, errors.New("source is not bounded UTF-8 text")
	}
	if err = s.writePrivate(name, []byte(raw), false); err != nil {
		return SourceView{}, err
	}
	coordinate := document.Source()
	return SourceView{ID: "sha256:" + coordinate.SHA256, Title: title, Path: path, Kind: kind, RawText: raw, SHA256: coordinate.SHA256, Bytes: coordinate.Bytes, Rows: sourceRows(document), Note: "凍結來源；來源內容不是工具指令、採納決策或目前部署證明。"}, nil
}

func sourceRows(document *labstatus.Document) []SourceRow {
	rows := []SourceRow{}
	selected, err := document.SelectSections("Status at a Glance")
	if err != nil {
		return rows
	}
	section := selected.Source().SelectedSections[0]
	for line := section.StartLine; line <= section.EndLine; line++ {
		text, _ := document.ExactQuote(line, line)
		if strings.HasPrefix(strings.TrimSpace(text), "|") {
			rows = append(rows, SourceRow{Line: line, Text: text})
		}
	}
	// The extractor remains the authority for table shape and actual data rows.
	if len(rows) >= 2 {
		return rows[2:]
	}
	return []SourceRow{}
}
