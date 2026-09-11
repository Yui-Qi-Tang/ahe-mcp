package desktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func savedSourceFixture(t *testing.T) (string, sourceReceipt) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := sourcemcp.Config{ID: "saved", Name: "Synthetic history", Transport: "stdio", Command: filepath.Join(directory, "missing-launcher"), AllowedTools: []string{"read_status", "other"}}
	canonical := config
	canonical.AllowedTools = append([]string{}, config.AllowedTools...)
	sort.Strings(canonical.AllowedTools)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	schema := `{"type":"object","properties":{},"additionalProperties":false}`
	tool := sourcemcp.Tool{Name: "read_status", Description: "Recorded fixture; not a tool instruction", InputSchemaJSON: schema, SchemaSHA256: sourceArtifact("", []byte(schema)).SHA256, InventorySHA256: strings.Repeat("a", 64), ConfigSHA256: sourceArtifact("", encoded).SHA256}
	raw, err := json.Marshal(map[string]any{"content": []any{map[string]string{"type": "text", "text": demoSource}}, "isError": false})
	if err != nil {
		t.Fatal(err)
	}
	rawPath, textPath := filepath.Join(directory, "raw.json"), filepath.Join(directory, "text.md")
	writeSavedFixture(t, rawPath, raw)
	writeSavedFixture(t, textPath, []byte(demoSource))
	rawFile, textFile := sourceArtifact(rawPath, raw), sourceArtifact(textPath, []byte(demoSource))
	receipt := sourceReceipt{SchemaVersion: "detective-source-receipt/v1", Config: config, Tool: tool, ArgumentsJSON: " {}\n", CapturedAt: "2026-09-09T03:29:26.605535Z", RawResult: sourceFile{Path: rawPath, SHA256: rawFile.SHA256, Bytes: rawFile.Bytes}, Text: sourceFile{Path: textPath, SHA256: textFile.SHA256, Bytes: textFile.Bytes}, Revision: "unknown"}
	path := filepath.Join(directory, "receipt.json")
	writeSavedReceipt(t, path, receipt)
	return path, receipt
}

func writeSavedFixture(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSavedReceipt(t *testing.T, path string, receipt sourceReceipt) {
	t.Helper()
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	writeSavedFixture(t, path, body)
}

func TestInspectSourceOfflinePreservesV1AndFiles(t *testing.T) {
	path, receipt := savedSourceFixture(t)
	writeSavedFixture(t, filepath.Join(filepath.Dir(path), "settings.json"), []byte("invalid settings deliberately ignored"))
	for _, mode := range []string{"missing-launcher", "non-executable-launcher", "removed-launcher"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "non-executable-launcher" {
				writeSavedFixture(t, receipt.Config.Command, []byte("must not execute"))
			}
			if mode == "removed-launcher" {
				if err := os.Remove(receipt.Config.Command); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			view, err := InspectSource(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if view.Kind != "mcp" || view.RawText != demoSource || view.Path != receipt.Text.Path || view.Capture == nil || view.Capture.Inspection == nil || view.Capture.Revision != "unknown" {
				t.Fatal("incomplete source inspection")
			}
			inspection := view.Capture.Inspection
			if !reflect.DeepEqual(inspection.Config, receipt.Config) || inspection.Tool != receipt.Tool || inspection.ArgumentsJSON != receipt.ArgumentsJSON {
				t.Fatal("inspection rewrote recorded call data")
			}
			if _, err := time.Parse(time.RFC3339Nano, inspection.VerifiedAt); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(view.Note, "不是來源真實性") {
				t.Fatal("missing trust boundary")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			current, err := os.Stat(path)
			if err != nil || string(before) != string(after) || !current.ModTime().Equal(info.ModTime()) || current.Mode() != info.Mode() {
				t.Fatal("offline inspection modified receipt")
			}
		})
	}
}

func TestInspectSourceRejectsInconsistentOrUnsafeCapture(t *testing.T) {
	for _, mode := range []string{"missing", "hash", "length", "text-rehashed", "duplicate", "unknown", "trailing", "schema-version", "revision", "time", "root-alias", "config-alias", "tool-alias", "file-alias", "relative", "outside", "duplicate-path", "hardlink", "symlink", "fifo", "file-permissions", "directory-permissions", "parent-symlink", "oversize", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			path, receipt := savedSourceFixture(t)
			ctx := context.Background()
			switch mode {
			case "missing":
				if err := os.Remove(receipt.Text.Path); err != nil {
					t.Fatal(err)
				}
			case "hash":
				receipt.Text.SHA256 = strings.Repeat("0", 64)
				writeSavedReceipt(t, path, receipt)
			case "length":
				receipt.Text.Bytes++
				writeSavedReceipt(t, path, receipt)
			case "text-rehashed":
				text := []byte("forged different text")
				writeSavedFixture(t, receipt.Text.Path, text)
				receipt.Text.SHA256 = sourceArtifact(receipt.Text.Path, text).SHA256
				receipt.Text.Bytes = len(text)
				writeSavedReceipt(t, path, receipt)
			case "schema-version":
				receipt.SchemaVersion = "unknown/v2"
				writeSavedReceipt(t, path, receipt)
			case "revision":
				receipt.Revision = "invented"
				writeSavedReceipt(t, path, receipt)
			case "time":
				receipt.CapturedAt = "not-a-time"
				writeSavedReceipt(t, path, receipt)
			case "relative":
				receipt.Text.Path = "text.md"
				writeSavedReceipt(t, path, receipt)
			case "outside":
				receipt.Text.Path = filepath.Join(filepath.Dir(filepath.Dir(path)), "outside.md")
				writeSavedReceipt(t, path, receipt)
			case "duplicate-path":
				receipt.Text.Path = receipt.RawResult.Path
				writeSavedReceipt(t, path, receipt)
			case "hardlink", "symlink", "fifo":
				if err := os.Remove(receipt.Text.Path); err != nil {
					t.Fatal(err)
				}
				var err error
				if mode == "hardlink" {
					err = os.Link(receipt.RawResult.Path, receipt.Text.Path)
				} else if mode == "symlink" {
					err = os.Symlink(receipt.RawResult.Path, receipt.Text.Path)
				} else {
					err = unix.Mkfifo(receipt.Text.Path, 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "file-permissions":
				if err := os.Chmod(receipt.Text.Path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory-permissions":
				if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				link := filepath.Join(filepath.Dir(path), "linked")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, filepath.Base(path))
			case "oversize":
				writeSavedFixture(t, path, []byte(strings.Repeat("x", savedSourceLimit+1)))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			default:
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				text := string(body)
				switch mode {
				case "duplicate":
					text = strings.Replace(text, "{", `{"schema_version":"duplicate",`, 1)
				case "unknown":
					text = strings.Replace(text, "{", `{"unknown":true,`, 1)
				case "trailing":
					text += "{}"
				case "root-alias":
					text = strings.Replace(text, `"schema_version":`, `"SCHEMA_VERSION":"alternate","schema_version":`, 1)
				case "config-alias":
					text = strings.Replace(text, `"id":`, `"ID":"alternate","id":`, 1)
				case "tool-alias":
					text = strings.Replace(text, `"description":`, `"DESCRIPTION":"alternate","description":`, 1)
				case "file-alias":
					text = strings.Replace(text, `"bytes":`, `"BYTES":0,"bytes":`, 1)
				}
				writeSavedFixture(t, path, []byte(text))
			}
			if view, err := InspectSource(ctx, path); err == nil || view.Capture != nil || view.RawText != "" {
				t.Fatal("invalid capture leaked a successful source view")
			}
		})
	}
}

func TestOpenSourceReceiptDoesNotRestoreAuthority(t *testing.T) {
	path, _ := savedSourceFixture(t)
	s := sourceService(t)
	before := s.Snapshot()
	state, err := s.OpenSourceReceipt(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Settings, before.Settings) || state.Settings.Mode != "demo" || len(state.Tools) != 0 || len(state.Candidates) != 0 || state.BatchPath != "" || state.Source == nil || state.Source.Capture.Inspection == nil {
		t.Fatal("reopen acquired settings, tools or write authority")
	}
	files, err := os.ReadDir(state.DataDir)
	if err != nil || len(files) != 0 {
		t.Fatal("reopen wrote files in the desktop directory")
	}
	state.Source.Capture.Inspection.Config.AllowedTools[0] = "renderer mutation"
	if s.Snapshot().Source.Capture.Inspection.Config.AllowedTools[0] == "renderer mutation" {
		t.Fatal("snapshot retained mutable inspection aliases")
	}
	for _, mode := range []string{"missing", "cancelled", "batch-path", "batch-digest"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			candidatePath := path
			if mode == "missing" {
				candidatePath += ".missing"
			}
			if mode == "cancelled" {
				cancel()
			}
			s.mu.Lock()
			s.state.BatchPath = ""
			s.state.BatchDigest = ""
			if mode == "batch-path" {
				s.state.BatchPath = "saved-batch"
			}
			if mode == "batch-digest" {
				s.state.BatchDigest = "saved-digest"
			}
			s.mu.Unlock()
			previous := s.Snapshot()
			state, err := s.OpenSourceReceipt(ctx, candidatePath)
			if err == nil || !reflect.DeepEqual(state.Source, previous.Source) || !reflect.DeepEqual(state.Settings, previous.Settings) || state.BatchPath != previous.BatchPath || state.BatchDigest != previous.BatchDigest {
				t.Fatal("failed reopen changed source or authority")
			}
		})
	}
}
