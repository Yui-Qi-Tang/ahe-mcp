package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const savedSourceLimit = 256 << 10 // Same per-file bound as captureSource.

var errSourceInspection = errors.New("saved source receipt or capture could not be verified; no connection or write was made")

// InspectSource verifies an existing v1 source capture without opening a Service,
// loading settings, contacting the source, or writing files. The result describes
// consistency with a local receipt, not authenticity or present upstream state.
func InspectSource(ctx context.Context, receiptPath string) (SourceView, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(receiptPath) || filepath.Clean(receiptPath) != receiptPath || !safeText(receiptPath, 4096) {
		return SourceView{}, errSourceInspection
	}
	directory := filepath.Dir(receiptPath)
	if checkDirectoryPath(directory, false) != nil {
		return SourceView{}, errSourceInspection
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedSourceFile(info) {
		return SourceView{}, errSourceInspection
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return SourceView{}, errSourceInspection
	}
	defer root.Close()
	var opened []os.FileInfo
	// Every reference must remain an exact sibling of the receipt. A missing
	// capture is never searched for elsewhere or redirected to an original URL.
	read := func(path string) ([]byte, error) {
		if ctx.Err() != nil || filepath.Dir(path) != directory || filepath.Clean(path) != path || !safeText(path, 4096) || checkSourceRoot(root, directory, info) != nil {
			return nil, errSourceInspection
		}
		before, err := root.Lstat(filepath.Base(path))
		if err != nil || !privateSourceFile(before) || before.Size() > savedSourceLimit {
			return nil, errSourceInspection
		}
		for _, other := range opened {
			if os.SameFile(before, other) {
				return nil, errSourceInspection
			}
		}
		file, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, errSourceInspection
		}
		defer file.Close()
		after, err := file.Stat()
		if err != nil || !privateSourceFile(after) || !os.SameFile(before, after) {
			return nil, errSourceInspection
		}
		body, err := io.ReadAll(io.LimitReader(file, savedSourceLimit+1))
		current, statErr := root.Lstat(filepath.Base(path))
		if err != nil || statErr != nil || len(body) > savedSourceLimit || !privateSourceFile(current) || !os.SameFile(after, current) || ctx.Err() != nil || checkSourceRoot(root, directory, info) != nil {
			return nil, errSourceInspection
		}
		opened = append(opened, after)
		return body, nil
	}
	body, err := read(receiptPath)
	if err != nil || sourcemcp.ValidateRecordedJSON(string(body)) != nil {
		return SourceView{}, errSourceInspection
	}
	var receipt sourceReceipt
	if !closedReceiptFields(body) {
		return SourceView{}, errSourceInspection
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF || receipt.SchemaVersion != "detective-source-receipt/v1" || receipt.Revision != "unknown" || receipt.RawResult.Path == receipt.Text.Path || receipt.RawResult.Path == receiptPath || receipt.Text.Path == receiptPath {
		return SourceView{}, errSourceInspection
	}
	observed, err := time.Parse(time.RFC3339Nano, receipt.CapturedAt)
	if err != nil || observed.IsZero() {
		return SourceView{}, errSourceInspection
	}
	raw, err := read(receipt.RawResult.Path)
	if err != nil || !matchesSourceArtifact(raw, receipt.RawResult) {
		return SourceView{}, errSourceInspection
	}
	text, err := read(receipt.Text.Path)
	if err != nil || !matchesSourceArtifact(text, receipt.Text) || !recordedCodeCitation(receipt.CodeCitation, receipt.Config, receipt.Tool.Name, string(text)) || sourcemcp.VerifyRecordedSource(receipt.Config, receipt.Tool, receipt.ArgumentsJSON, string(raw), string(text)) != nil {
		return SourceView{}, errSourceInspection
	}
	document, err := labstatus.RestoreDocument(receipt.Text.Path, string(text))
	if err != nil || ctx.Err() != nil || checkSourceRoot(root, directory, info) != nil {
		return SourceView{}, errSourceInspection
	}
	coordinate := document.Source()
	return SourceView{
		ID: "sha256:" + coordinate.SHA256, Title: receipt.Config.Name + " / " + receipt.Tool.Name + " — 保存的觀測",
		Path: receipt.Text.Path, Kind: "mcp", RawText: document.RawText(), SHA256: coordinate.SHA256, Bytes: coordinate.Bytes, Rows: sourceRows(document),
		Note: "檔案與已保存收據一致；沒有重新取材。這不是來源真實性、最新狀態或真人核准的證明；歷史完整工具清單未重新驗證，來源 revision 仍未知。",
		Capture: &SourceCapture{
			CapturedAt: receipt.CapturedAt, Revision: receipt.Revision,
			CodeCitation: receipt.CodeCitation,
			RawResult:    SourceArtifact{Path: receipt.RawResult.Path, SHA256: receipt.RawResult.SHA256, Bytes: receipt.RawResult.Bytes},
			Receipt:      sourceArtifact(receiptPath, body),
			Inspection:   &SourceInspection{VerifiedAt: time.Now().UTC().Format(time.RFC3339Nano), Config: receipt.Config, Tool: receipt.Tool, ArgumentsJSON: receipt.ArgumentsJSON, RawResultJSON: string(raw)},
		},
	}, nil
}

// encoding/json treats field names case-insensitively. Exact persisted keys
// prevent aliases from silently overwriting a second value of the same field.
func closedReceiptFields(body []byte) bool {
	fields, ok := savedSourceOptionalFields(body, []string{"schema_version", "config", "tool", "args_json", "captured_at", "raw_result", "text", "source_revision"}, "code_citation")
	if !ok {
		return false
	}
	if _, ok := savedSourceOptionalFields(fields["config"], []string{"id", "name", "transport", "command", "url", "allowed_tools"}, "args", "directory", "codebase_cache"); !ok {
		return false
	}
	if _, ok := savedSourceFields(fields["tool"], "name", "description", "input_schema_json", "schema_sha256", "inventory_sha256", "config_sha256"); !ok {
		return false
	}
	for _, name := range []string{"raw_result", "text"} {
		if _, ok := savedSourceFields(fields[name], "path", "sha256", "bytes"); !ok {
			return false
		}
	}
	if citation, exists := fields["code_citation"]; exists {
		if _, ok := savedSourceFields(citation, "filePath", "startLine", "endLine", "fileSHA256", "exactQuote"); !ok {
			return false
		}
	}
	return true
}

func savedSourceOptionalFields(body []byte, required []string, optional ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, false
	}
	names := append([]string(nil), required...)
	for _, name := range optional {
		if _, ok := fields[name]; ok {
			names = append(names, name)
		}
	}
	return savedSourceFields(body, names...)
}

func savedSourceFields(body []byte, names ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != len(names) {
		return nil, false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return nil, false
		}
	}
	return fields, true
}

func ownedSourceFile(info os.FileInfo) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Getuid()
}

func privateSourceFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && ownedSourceFile(info)
}

func checkSourceRoot(root *os.Root, path string, original os.FileInfo) error {
	current, err := os.Lstat(path)
	pinned, rootErr := root.Stat(".")
	if err != nil || rootErr != nil || !current.IsDir() || current.Mode().Perm() != 0o700 || !ownedSourceFile(current) || !os.SameFile(original, current) || !os.SameFile(original, pinned) || checkDirectoryPath(path, false) != nil {
		return errSourceInspection
	}
	return nil
}

func sourceArtifact(path string, body []byte) SourceArtifact {
	hash := sha256.Sum256(body)
	return SourceArtifact{Path: path, SHA256: hex.EncodeToString(hash[:]), Bytes: len(body)}
}

func matchesSourceArtifact(body []byte, expected sourceFile) bool {
	actual := sourceArtifact(expected.Path, body)
	return actual.SHA256 == expected.SHA256 && actual.Bytes == expected.Bytes
}

// OpenSourceReceipt changes only the source inspection and in-memory event log.
// Recorded settings and tools remain data; they never become execution policy.
func (s *Service) OpenSourceReceipt(ctx context.Context, path string) (State, error) {
	ctx, done, err := s.begin(ctx, "開啟來源收據")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.BatchPath != "" || state.BatchDigest != "" {
		return s.finish(done, errors.New("active batch"), "已有候選批次，不能替換其來源；請先開始新工作。")
	}
	source, err := InspectSource(ctx, path)
	if err != nil {
		return s.finish(done, err, "來源收據或原始檔案未通過離線核對；保留原本來源，未建立連線或修改檔案。")
	}
	s.mu.Lock()
	if ctx.Err() != nil || s.state.BatchPath != "" || s.state.BatchDigest != "" {
		s.mu.Unlock()
		return s.finish(done, errors.New("source inspection cancelled or batch active"), "來源重開已取消或有既有批次；保留原本來源，沒有修改檔案。")
	}
	s.state.Source = &source
	s.state.Task = nil
	s.state.Brief = nil
	s.state.Candidates = []CandidateView{}
	s.state.Extraction = nil
	s.state.BatchResult = nil
	s.state.Tools = []Tool{}
	s.mu.Unlock()
	return s.finish(done, nil, "已離線重開保存來源；檔案與收據一致，不是來源真實性、最新狀態或人工核准。")
}
