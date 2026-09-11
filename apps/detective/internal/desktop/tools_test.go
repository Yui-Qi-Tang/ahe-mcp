package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sourceService(t *testing.T) *Service {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func sourceFixture(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
			w.WriteHeader(400)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "desktop-test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "read_status", "description": "synthetic read only", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": demoSource}}, "isError": false}
		default:
			t.Errorf("unexpected method %s", req.Method)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func configureSource(t *testing.T, s *Service, url string) {
	t.Helper()
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.Connections = []Connection{{ID: "fixture", Name: "Synthetic source", Transport: "streamable-http", URL: url, AllowedTools: []string{"read_status"}}}
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
}

func TestSourceConfirmationAndSnapshot(t *testing.T) {
	s := sourceService(t)
	var calls atomic.Int64
	server := sourceFixture(t, &calls)
	configureSource(t, s, server.URL)
	state, err := s.DiscoverTools(context.Background(), "fixture")
	if err != nil || len(state.Tools) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	tool := state.Tools[0]
	if _, err := s.CallSourceTool(context.Background(), "fixture", tool.Name, "{}", "admit"); err == nil {
		t.Fatal("chat-like approval accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("tool called before exact confirmation")
	}
	confirmation := "fixture\n" + tool.Name + "\n" + tool.Digest + "\n{}"
	if _, err := s.CallSourceTool(context.Background(), "fixture", tool.Name, "{\"changed\":true}", confirmation); err == nil {
		t.Fatal("changed arguments accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("changed arguments called tool")
	}
	argsJSON := " {\n\t } \n"
	confirmation = "fixture\n" + tool.Name + "\n" + tool.Digest + "\n" + argsJSON
	startedAt := time.Now()
	state, err = s.CallSourceTool(context.Background(), "fixture", tool.Name, argsJSON, confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || state.Source == nil || state.Source.RawText != demoSource || state.Source.Kind != "mcp" || state.BatchPath != "" || len(state.Candidates) != 0 {
		t.Fatal("incorrect source-only projection")
	}
	if !strings.Contains(state.Source.Note, "revision 未確認") || !strings.Contains(state.Source.Note, "參數／觀測時間紀錄") {
		t.Fatal("source provenance boundary missing")
	}
	files, err := filepath.Glob(filepath.Join(state.DataDir, "source-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("want raw result, provenance, and text; got %d", len(files))
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("source artifact not private")
		}
	}
	var receipt sourceReceipt
	var receiptPath string
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var candidate sourceReceipt
		if json.Unmarshal(body, &candidate) == nil && candidate.SchemaVersion == "detective-source-receipt/v1" {
			receipt, receiptPath = candidate, path
		}
	}
	if receiptPath == "" {
		t.Fatal("complete source receipt was not saved")
	}
	if state.Source.Capture == nil {
		t.Fatal("complete source capture has no typed artifacts")
	}
	capture := state.Source.Capture
	if capture.RawResult != (SourceArtifact{Path: receipt.RawResult.Path, SHA256: receipt.RawResult.SHA256, Bytes: receipt.RawResult.Bytes}) || capture.Receipt.Path != receiptPath || capture.Revision != "unknown" || capture.CapturedAt != receipt.CapturedAt {
		t.Fatal("typed artifacts do not describe the exact saved capture")
	}
	receiptBytes, err := os.ReadFile(capture.Receipt.Path)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash := sha256.Sum256(receiptBytes)
	if capture.Receipt.SHA256 != hex.EncodeToString(receiptHash[:]) || capture.Receipt.Bytes != len(receiptBytes) {
		t.Fatal("typed receipt hash or size mismatch")
	}
	capture.Receipt.Path = "renderer-owned mutation"
	if s.Snapshot().Source.Capture.Receipt.Path != receiptPath {
		t.Fatal("typed capture was not detached from renderer state")
	}
	if !reflect.DeepEqual(receipt.Config, sourceConfig(state.Settings.Connections[0])) || receipt.ArgumentsJSON != argsJSON || receipt.Revision != "unknown" {
		t.Fatal("receipt lost exact confirmed arguments or connection coordinates")
	}
	if toolDigest(receipt.Tool) != tool.Digest || receipt.Tool.SchemaSHA256 != tool.SchemaSHA256 || receipt.Tool.InventorySHA256 != tool.InventorySHA256 || receipt.Tool.ConfigSHA256 != tool.ConfigSHA256 || receipt.Tool.InputSchemaJSON != tool.InputSchemaJSON || receipt.Tool.Description != tool.Description {
		t.Fatal("receipt lost the confirmed tool contract")
	}
	if receipt.RawResult.Path == receipt.Text.Path || receipt.Text.Path != state.Source.Path || receipt.Text.SHA256 != state.Source.SHA256 {
		t.Fatal("receipt does not distinguish/link raw result and text")
	}
	for _, saved := range []sourceFile{receipt.RawResult, receipt.Text} {
		body, err := os.ReadFile(saved.Path)
		if err != nil {
			t.Fatal("receipt references a missing file")
		}
		hash := sha256.Sum256(body)
		if len(body) != saved.Bytes || hex.EncodeToString(hash[:]) != saved.SHA256 {
			t.Fatal("receipt source hash/length mismatch")
		}
	}
	capturedAt, err := time.Parse(time.RFC3339Nano, receipt.CapturedAt)
	if err != nil || capturedAt.Before(startedAt) || capturedAt.After(time.Now()) {
		t.Fatal("invalid capture time")
	}
	retained, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	settings := state.Settings
	settings.Connections[0].URL = "http://127.0.0.1:1/different-gateway"
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := New(state.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := os.ReadFile(receiptPath)
	if err != nil || string(after) != string(retained) || receipt.Config.URL != server.URL {
		t.Fatal("mutable settings/restart changed source provenance")
	}
}

func TestSourceCancellationAfterCapturePreservesFilesAndPreviousDisplay(t *testing.T) {
	s := sourceService(t)
	oldState, err := s.LoadDemo()
	if err != nil {
		t.Fatal(err)
	}
	ctx, done, err := s.begin(context.Background(), "來源工具讀取")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	// Exercise the real private completion boundary after durable captures, not
	// a timing-dependent race or a production-only test hook.
	var paths []string
	var source SourceView
	for _, item := range []struct{ kind, raw string }{
		{"mcp-result", `{"content":[{"type":"text","text":"synthetic"}]}`},
		{"mcp", "synthetic captured source"},
		{"mcp-provenance", `{"schema_version":"test-cancel-capture"}`},
	} {
		captured, err := s.captureSource("synthetic capture", item.kind, item.raw)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, captured.Path)
		if item.kind == "mcp" {
			source = captured
		}
	}
	canceled := s.Cancel()
	if !canceled.Busy || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancellation did not preserve owned cleanup")
	}
	state, err := s.applySourceCapture(ctx, done, source)
	if err == nil || state.Busy || !reflect.DeepEqual(state.Source, oldState.Source) || !reflect.DeepEqual(state.Candidates, oldState.Candidates) {
		t.Fatal("canceled capture replaced the display or appeared successful")
	}
	last := state.Events[len(state.Events)-1]
	if last.Status != "failed" || !strings.Contains(last.Detail, "可能已保存") || !strings.Contains(last.Detail, "沒有送到 AHE") {
		t.Fatal("canceled capture did not explain retained files/authority boundary")
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("cancellation removed or changed a private capture")
		}
	}
}

func TestSourceModeAndActiveBatchGuards(t *testing.T) {
	s := sourceService(t)
	if _, err := s.DiscoverTools(context.Background(), "fixture"); err == nil {
		t.Fatal("demo attempted source discovery")
	}
	var calls atomic.Int64
	server := sourceFixture(t, &calls)
	configureSource(t, s, server.URL)
	state, err := s.DiscoverTools(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	tool := state.Tools[0]
	s.mu.Lock()
	s.state.BatchPath = "existing-batch-must-not-change"
	s.mu.Unlock()
	confirmation := "fixture\n" + tool.Name + "\n" + tool.Digest + "\n{}"
	if _, err := s.CallSourceTool(context.Background(), "fixture", tool.Name, "{}", confirmation); err == nil {
		t.Fatal("active batch source replaced")
	}
	if calls.Load() != 0 {
		t.Fatal("tool executed while source bound to batch")
	}
	if s.Snapshot().BatchPath != "existing-batch-must-not-change" {
		t.Fatal("active batch changed")
	}
}
