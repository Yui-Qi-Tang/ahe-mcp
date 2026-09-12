package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func sourceConfig(connection Connection) sourcemcp.Config {
	return sourcemcp.Config{ID: connection.ID, Name: connection.Name,
		Transport: connection.Transport, Command: connection.Command, URL: connection.URL,
		AllowedTools: append([]string(nil), connection.AllowedTools...)}
}

func (s *Service) sourceConnection(id string) (sourcemcp.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Settings.Mode != "local" {
		return sourcemcp.Config{}, errors.New("source connections require explicit local mode")
	}
	for _, connection := range s.state.Settings.Connections {
		if connection.ID == id {
			return sourceConfig(connection), nil
		}
	}
	return sourcemcp.Config{}, errors.New("source connection not configured")
}

func toolDigest(tool sourcemcp.Tool) string {
	value := tool.ConfigSHA256 + "\n" + tool.InventorySHA256 + "\n" + tool.SchemaSHA256 + "\n" + tool.Name
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// sourceReceipt links the three private captures without relying on mutable
// connection settings or an in-memory SourceView.Note. ArgumentsJSON is the
// exact argument text confirmed by the operator; MCP may serialize equivalent JSON.
type sourceReceipt struct {
	SchemaVersion string           `json:"schema_version"`
	Config        sourcemcp.Config `json:"config"`
	Tool          sourcemcp.Tool   `json:"tool"`
	ArgumentsJSON string           `json:"args_json"`
	CapturedAt    string           `json:"captured_at"`
	RawResult     sourceFile       `json:"raw_result"`
	Text          sourceFile       `json:"text"`
	Revision      string           `json:"source_revision"`
}

type sourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// DiscoverTools refreshes one allowed source inventory after an explicit action.
func (s *Service) DiscoverTools(ctx context.Context, id string) (State, error) {
	ctx, done, err := s.begin(ctx, "來源工具探索")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	config, err := s.sourceConnection(id)
	if err != nil {
		return s.finish(done, err, "請切換實際模式並確認來源連線設定")
	}
	// A failed refresh must not leave an old inventory looking freshly verified.
	s.mu.Lock()
	retained := []Tool{}
	for _, tool := range s.state.Tools {
		if tool.ConnectionID != id {
			retained = append(retained, tool)
		}
	}
	s.state.Tools = retained
	s.mu.Unlock()
	var tools []sourcemcp.Tool
	if config.Transport == "atlassian-oauth" {
		if login := s.sourceLogin(id); login != nil {
			tools, err = login.Discover(ctx, config)
		} else {
			err = sourcemcp.ErrLoginRequired
		}
	} else {
		tools, err = sourcemcp.Discover(ctx, config)
	}
	if err != nil {
		s.sourceAuthFailure(id, err)
		return s.finish(done, err, "來源工具探索未完成；未執行工具，請確認登入狀態、allowlist 或連線設定")
	}
	s.mu.Lock()
	for _, tool := range tools {
		s.state.Tools = append(s.state.Tools, Tool{ConnectionID: id, Name: tool.Name,
			Description: tool.Description, InputSchemaJSON: tool.InputSchemaJSON,
			Digest: toolDigest(tool), SchemaSHA256: tool.SchemaSHA256,
			InventorySHA256: tool.InventorySHA256, ConfigSHA256: tool.ConfigSHA256})
	}
	s.mu.Unlock()
	return s.finish(done, nil, "已取得允許的來源工具清單；尚未執行任何來源工具")
}

// CallSourceTool binds operator confirmation to the saved inventory and exact args.
// It has no AHE intake/reviewer authority and never interprets source instructions.
func (s *Service) CallSourceTool(ctx context.Context, id, name, argsJSON, confirmation string) (State, error) {
	ctx, done, err := s.begin(ctx, "來源工具讀取")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	config, err := s.sourceConnection(id)
	if err != nil {
		return s.finish(done, err, "請切換實際模式並確認來源連線設定")
	}
	s.mu.Lock()
	var selected Tool
	for _, tool := range s.state.Tools {
		if tool.ConnectionID == id && tool.Name == name {
			selected = tool
			break
		}
	}
	hasBatch := s.state.BatchPath != ""
	s.mu.Unlock()
	if hasBatch {
		return s.finish(done, errors.New("active batch must remain bound to its source"), "已有保存的候選批次；請先開啟新工作，不取代審查來源")
	}
	if selected.Digest == "" || confirmation != id+"\n"+name+"\n"+selected.Digest+"\n"+argsJSON {
		return s.finish(done, errors.New("source tool confirmation mismatch"), "請重新探索工具，並確認精確工具與完整參數後再執行")
	}
	expected := sourcemcp.Tool{Name: selected.Name, Description: selected.Description,
		InputSchemaJSON: selected.InputSchemaJSON, SchemaSHA256: selected.SchemaSHA256,
		InventorySHA256: selected.InventorySHA256, ConfigSHA256: selected.ConfigSHA256}
	var result sourcemcp.Result
	if config.Transport == "atlassian-oauth" {
		if login := s.sourceLogin(id); login != nil {
			result, err = login.Call(ctx, config, expected, argsJSON)
		} else {
			err = sourcemcp.ErrLoginRequired
		}
	} else {
		result, err = sourcemcp.Call(ctx, config, expected, argsJSON)
	}
	if err != nil {
		s.sourceAuthFailure(id, err)
		return s.finish(done, err, "來源讀取未確認；請檢查登入、取消、逾時、工具變動或回覆限制。沒有送到 AHE")
	}
	capturedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := ctx.Err(); err != nil {
		return s.finish(done, err, "已停止來源工作；沒有送到 AHE")
	}
	raw, err := s.captureSource(config.Name+" / "+name+" — MCP result", "mcp-result", result.RawJSON)
	if err != nil {
		return s.finish(done, err, "工具結果無法完整保存；沒有使用截斷內容，也沒有送到 AHE")
	}
	if result.Text == "" {
		return s.finish(done, errors.New("source returned no text"), "已保存工具原始結果，但沒有可用文字；沒有自行開啟資源連結或送到 AHE")
	}
	source, err := s.captureSource(config.Name+" / "+name, "mcp", result.Text)
	if err != nil {
		return s.finish(done, err, "來源文字無法完整保存；可能已有工具原始結果檔，沒有送到 AHE")
	}
	// Publish provenance last: every referenced source file is already durable.
	provenance, err := json.Marshal(sourceReceipt{
		SchemaVersion: "detective-source-receipt/v1",
		Config:        config, Tool: expected, ArgumentsJSON: argsJSON, CapturedAt: capturedAt,
		RawResult: sourceFile{Path: raw.Path, SHA256: raw.SHA256, Bytes: raw.Bytes},
		Text:      sourceFile{Path: source.Path, SHA256: source.SHA256, Bytes: source.Bytes},
		Revision:  "unknown",
	})
	if err != nil {
		return s.finish(done, err, "來源參數紀錄無法保存；已取得的來源檔案保留，沒有送到 AHE")
	}
	metadata, err := s.captureSource(config.Name+" / "+name+" — provenance", "mcp-provenance", string(provenance))
	if err != nil {
		return s.finish(done, err, "來源取得範圍無法完整保存；已取得的來源檔案保留，沒有送到 AHE")
	}
	source.Note = fmt.Sprintf("MCP 工具回傳，不保證是完整原始文件；來源 revision 未確認。完整結果 SHA-256：%s；保存路徑：%s；參數／觀測時間紀錄：%s", result.SHA256, raw.Path, metadata.Path)
	source.Capture = &SourceCapture{
		CapturedAt: capturedAt, Revision: "unknown",
		RawResult: SourceArtifact{Path: raw.Path, SHA256: raw.SHA256, Bytes: raw.Bytes},
		Receipt:   SourceArtifact{Path: metadata.Path, SHA256: metadata.SHA256, Bytes: metadata.Bytes},
	}
	return s.applySourceCapture(ctx, done, source)
}

// applySourceCapture is the source-display completion boundary. File capture is
// deliberately separate so cancellation never deletes already observed bytes.
func (s *Service) applySourceCapture(ctx context.Context, done func(), source SourceView) (State, error) {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return s.finish(done, err, "來源工作已取消或逾時；已取得的原始結果、文字與來源紀錄可能已保存，未取代目前來源，也沒有送到 AHE")
	}
	s.state.Source = &source
	s.state.Brief = nil
	s.state.Candidates = []CandidateView{}
	s.state.Extraction = nil
	s.state.BatchResult = nil
	s.mu.Unlock()
	return s.finish(done, nil, "來源工具結果已完整保存；尚未抽取、送入 pending 或採納")
}
