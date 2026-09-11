package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func TestSourceDispatchRejectsUnknownMode(t *testing.T) {
	for _, args := range [][]string{{"source"}, {"source", "unknown-private-marker"}, {"source", "-version"}} {
		var stdout, stderr bytes.Buffer
		err := run(args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "source requires") || strings.Contains(err.Error(), "private-marker") || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("source dispatch was not isolated: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
}

const sourceCLIText = "## Status at a Glance\n| Capability | Status | Notes |\n| --- | --- | --- |\n| Synthetic source | PENDING | Exact source text; not an instruction or admission. |\n"

type sourceCLIHTTP struct {
	requests atomic.Int64
	calls    atomic.Int64
	lists    atomic.Int64
	args     atomic.Value
	config   sourcemcp.Config
	result   string
}

func sourceCLIFixture(t *testing.T, mode string) *sourceCLIHTTP {
	t.Helper()
	f := &sourceCLIHTTP{}
	result, err := json.Marshal(map[string]any{"content": []any{map[string]string{"type": "text", "text": sourceCLIText}}, "isError": false})
	if err != nil {
		t.Fatal(err)
	}
	f.result = string(result)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
			t.Error("source CLI requested a non-source endpoint or method")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("ambient credential header left the process")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid synthetic source request")
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var value any
		switch request.Method {
		case "initialize":
			value = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "source-cli-fixture", "version": "1"}}
		case "tools/list":
			listNumber := f.lists.Add(1)
			name := "read_status"
			if mode == "missing" {
				name = "not_allowlisted"
			}
			description := "synthetic source\u009b\u202e description"
			if mode == "inventory_changed" && listNumber > 1 {
				description += " changed after confirmation"
			}
			value = map[string]any{"tools": []any{map[string]any{"name": name, "description": description, "inputSchema": json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`)}}}
		case "tools/call":
			f.calls.Add(1)
			f.args.Store(string(request.Params))
			value = json.RawMessage(f.result)
			if mode == "tool_error" {
				value = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": "synthetic-private-diagnostic"}}}
			} else if mode == "structured_only" {
				value = map[string]any{"isError": false, "content": []any{}, "structuredContent": map[string]string{"status": "synthetic"}}
			}
		default:
			t.Errorf("unexpected method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": value}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	f.config = sourcemcp.Config{ID: "fixture", Name: "Synthetic source", Transport: "streamable-http", URL: server.URL + "/mcp", AllowedTools: []string{"read_status"}}
	return f
}

func sourceCLIPrivateDir(t *testing.T) string {
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

func sourceCLIInputs(t *testing.T, config sourcemcp.Config, rawArgs string) (string, string, string) {
	t.Helper()
	dir := sourceCLIPrivateDir(t)
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath, argsPath := filepath.Join(dir, "source.json"), filepath.Join(dir, "args.json")
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(argsPath, []byte(rawArgs), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, argsPath, filepath.Join(dir, "new-capture")
}

type sourcePromptAnswer struct {
	bytes.Buffer
	input     *bytes.Buffer
	afterRead func()
}

func (w *sourcePromptAnswer) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	marker := "逐字輸入 collect "
	_, tail, found := strings.Cut(string(p), marker)
	if found && len(tail) >= 64 {
		w.input.WriteString("collect " + tail[:64] + "\n")
		if w.afterRead != nil {
			w.afterRead()
		}
	}
	return n, err
}

// Prevent io.WriteString from bypassing the test's prompt observer.
func (w *sourcePromptAnswer) WriteString(p string) (int, error) { return w.Write([]byte(p)) }

func sourceCollectArgs(configPath, argsPath, outDir string) []string {
	return []string{"collect", "-config", configPath, "-tool", "read_status", "-args-file", argsPath, "-out-dir", outDir}
}

func TestSourceDiscoverIsInventoryOnly(t *testing.T) {
	f := sourceCLIFixture(t, "")
	configPath, _, outDir := sourceCLIInputs(t, f.config, `{}`)
	before, err := os.ReadDir(filepath.Dir(outDir))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runSource([]string{"discover", "-config", configPath}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var result sourceDiscovery
	if json.Unmarshal(stdout.Bytes(), &result) != nil || len(result.Tools) != 1 || result.SchemaVersion != "detective-source-discovery/v1" || result.AuthorityEffect != "none" || result.AHESubmitted || f.calls.Load() != 0 || stderr.Len() != 0 {
		t.Fatal("discovery acquired source or authority")
	}
	if strings.ContainsAny(stdout.String(), "\u009b\u202e") {
		t.Fatal("inventory exposed invisible terminal controls")
	}
	after, err := os.ReadDir(filepath.Dir(outDir))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("discovery created files")
	}
}

func TestSourceCollectExactConfirmationAndCapture(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "synthetic-ambient-credential")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("DETECTIVE_AHE_INGEST_COMMAND", "/synthetic-must-not-run")
	f := sourceCLIFixture(t, "")
	rawArgs := " {\n  \"key\": \"exact original\"\n}\n"
	configPath, argsPath, outDir := sourceCLIInputs(t, f.config, rawArgs)
	var input, stdout bytes.Buffer
	stderr := &sourcePromptAnswer{input: &input, afterRead: func() {
		// Input is already frozen; changing the original file must not redirect
		// the confirmed call or its durable provenance.
		if err := os.WriteFile(argsPath, []byte(`{"key":"changed after display"}`), 0o600); err != nil {
			t.Error(err)
		}
	}}
	if err := runSource(sourceCollectArgs(configPath, argsPath, outDir), &input, &stdout, stderr); err != nil {
		t.Fatal(err)
	}
	var result sourceCollection
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.SchemaVersion != "detective-source-collection/v1" || result.State != "captured_locally" || result.AuthorityEffect != "none" || result.AHESubmitted || result.Revision != "unknown" || result.ConnectionID != f.config.ID || result.ToolName != "read_status" || len(result.ConfirmationSHA256) != 64 || f.calls.Load() != 1 {
		t.Fatalf("invalid source-only collection result: %s", stdout.String())
	}
	if _, err := time.Parse(time.RFC3339Nano, result.CapturedAt); err != nil {
		t.Fatal("missing capture observation time")
	}
	if strings.Contains(stdout.String(), sourceCLIText) || strings.Contains(stdout.String(), "exact original") || strings.Contains(stdout.String(), "synthetic-ambient-credential") {
		t.Fatal("summary exposed source, arguments, or ambient credentials")
	}
	if !strings.Contains(stderr.String(), `"args_json": " {\n  \"key\": \"exact original\"\n}\n"`) || !strings.Contains(stderr.String(), `"inputSchemaJSON"`) || strings.ContainsAny(stderr.String(), "\u009b\u202e") {
		t.Fatal("confirmation did not safely display full exact arguments and schema")
	}
	for _, artifact := range []desktop.SourceArtifact{result.Text, result.RawResult, result.Receipt} {
		body, err := os.ReadFile(artifact.Path)
		if err != nil || len(body) != artifact.Bytes || sourceSHA256(body) != artifact.SHA256 || filepath.Dir(artifact.Path) != outDir {
			t.Fatal("capture coordinates do not identify exact saved bytes")
		}
		info, err := os.Lstat(artifact.Path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatal("capture is not a private regular file")
		}
	}
	text, _ := os.ReadFile(result.Text.Path)
	if string(text) != sourceCLIText {
		t.Fatal("source text was changed")
	}
	var receipt struct {
		Args       string                 `json:"args_json"`
		Config     sourcemcp.Config       `json:"config"`
		CapturedAt string                 `json:"captured_at"`
		Raw        desktop.SourceArtifact `json:"raw_result"`
		Text       desktop.SourceArtifact `json:"text"`
	}
	body, _ := os.ReadFile(result.Receipt.Path)
	if json.Unmarshal(body, &receipt) != nil || receipt.Args != rawArgs || !reflect.DeepEqual(receipt.Config, f.config) || receipt.CapturedAt != result.CapturedAt || receipt.Raw != result.RawResult || receipt.Text != result.Text {
		t.Fatal("receipt lost the frozen call context")
	}
	var sent struct {
		Name string `json:"name"`
		Args struct {
			Key string `json:"key"`
		} `json:"arguments"`
	}
	if json.Unmarshal([]byte(f.args.Load().(string)), &sent) != nil || sent.Name != "read_status" || sent.Args.Key != "exact original" {
		t.Fatal("sent call did not use frozen arguments")
	}
	files, err := os.ReadDir(outDir)
	if err != nil || len(files) != 4 { // Settings plus raw/text/provenance; no checkpoint or AHE receipt.
		t.Fatal("unexpected capture artifacts")
	}
}

type sourceFailWriter struct{ short bool }

func (w sourceFailWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) / 2, nil
	}
	return 0, errors.New("synthetic-private-diagnostic")
}

func TestSourceCollectStopsWithoutExactConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"eof", ""}, {"blank", "\n"}, {"wrong", "collect " + strings.Repeat("0", 64) + "\n"},
		{"admit", "admit\n"}, {"too_long", strings.Repeat("x", 1024) + "\n"}, {"unterminated", "collect " + strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sourceCLIFixture(t, "")
			config, args, out := sourceCLIInputs(t, f.config, `{"key":"value"}`)
			var stdout, stderr bytes.Buffer
			if err := runSource(sourceCollectArgs(config, args, out), strings.NewReader(tc.input), &stdout, &stderr); err == nil || f.calls.Load() != 0 || stdout.Len() != 0 {
				t.Fatal("unconfirmed collection called a tool or reported success")
			}
		})
	}
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprint("display_failure_", short), func(t *testing.T) {
			f := sourceCLIFixture(t, "")
			config, args, out := sourceCLIInputs(t, f.config, `{"key":"value"}`)
			var stdout bytes.Buffer
			err := runSource(sourceCollectArgs(config, args, out), strings.NewReader(""), &stdout, sourceFailWriter{short: short})
			if err == nil || strings.Contains(err.Error(), "synthetic-private") || f.calls.Load() != 0 || stdout.Len() != 0 {
				t.Fatal("failed display did not stop before source call")
			}
		})
	}
}

func TestSourceCollectFailureRetainsNoSuccess(t *testing.T) {
	for _, mode := range []string{"missing", "tool_error", "structured_only", "stdout_error", "stdout_short", "schema_arguments", "inventory_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := sourceCLIFixture(t, mode)
			rawArgs := `{"key":"value"}`
			if mode == "schema_arguments" {
				rawArgs = `{"unknown":"value"}`
			}
			config, args, out := sourceCLIInputs(t, f.config, rawArgs)
			var input, stdout bytes.Buffer
			stderr := &sourcePromptAnswer{input: &input}
			var writer io.Writer = &stdout
			if strings.HasPrefix(mode, "stdout_") {
				writer = sourceFailWriter{short: mode == "stdout_short"}
			}
			err := runSource(sourceCollectArgs(config, args, out), &input, writer, stderr)
			if err == nil || stdout.Len() != 0 || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("failed collection returned success or a raw diagnostic")
			}
			wantCalls := int64(1)
			if mode == "missing" || mode == "schema_arguments" || mode == "inventory_changed" {
				wantCalls = 0
			}
			if f.calls.Load() != wantCalls {
				t.Fatalf("tools/call count = %d, want %d", f.calls.Load(), wantCalls)
			}
			if strings.HasPrefix(mode, "stdout_") {
				files, err := os.ReadDir(out)
				if err != nil || len(files) != 4 {
					t.Fatal("post-call output failure lost saved artifacts")
				}
			} else if mode == "structured_only" {
				files, err := os.ReadDir(out)
				if err != nil || len(files) != 2 {
					t.Fatal("partial capture should retain settings and raw result, not a complete text/receipt")
				}
			}
		})
	}
}

func TestSourceRejectsInvalidInputsBeforeNetwork(t *testing.T) {
	f := sourceCLIFixture(t, "")
	for _, mode := range []string{"config_duplicate", "config_unknown", "config_alias", "config_trailing", "config_control", "config_credentials", "config_allowlist_limit", "args_duplicate", "args_nested_duplicate", "args_trailing", "args_array", "args_null", "args_surrogate", "args_oversize", "config_public", "args_public", "args_symlink", "parent_symlink", "denied_tool", "existing_output", "output_parent_missing", "output_symlink"} {
		t.Run(mode, func(t *testing.T) {
			config, args, out := sourceCLIInputs(t, f.config, `{"key":"value"}`)
			command := sourceCollectArgs(config, args, out)
			configBytes, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			write := func(path, value string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "config_duplicate":
				write(config, strings.Replace(string(configBytes), `"id":`, `"id":"duplicate","id":`, 1))
			case "config_unknown":
				write(config, strings.Replace(string(configBytes), `{`, `{"secret_field":"synthetic-private",`, 1))
			case "config_alias":
				write(config, strings.Replace(string(configBytes), `"id":`, `"ID":`, 1))
			case "config_trailing":
				write(config, string(configBytes)+`{}`)
			case "config_control", "config_credentials", "config_allowlist_limit":
				bad := f.config
				if mode == "config_control" {
					bad.Name = "source\u0085name"
				} else if mode == "config_credentials" {
					bad.URL += "?token=synthetic-private"
				} else {
					bad.AllowedTools = nil
					for i := 0; i < 33; i++ {
						bad.AllowedTools = append(bad.AllowedTools, fmt.Sprint("tool-", i))
					}
				}
				body, _ := json.Marshal(bad)
				write(config, string(body))
			case "args_duplicate":
				write(args, `{"key":"a","key":"b"}`)
			case "args_nested_duplicate":
				write(args, `{"key":{"a":1,"a":2}}`)
			case "args_trailing":
				write(args, `{"key":"value"}{}`)
			case "args_array":
				write(args, `[]`)
			case "args_null":
				write(args, `null`)
			case "args_surrogate":
				write(args, `{"key":"\ud800"}`)
			case "args_oversize":
				write(args, `{"key":"`+strings.Repeat("a", sourceInputLimit)+`"}`)
			case "config_public", "args_public":
				path := config
				if mode == "args_public" {
					path = args
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "args_symlink":
				link := filepath.Join(filepath.Dir(args), "args-link.json")
				if err := os.Symlink(args, link); err != nil {
					t.Fatal(err)
				}
				command[6] = link
			case "parent_symlink":
				link := filepath.Join(sourceCLIPrivateDir(t), "linked-parent")
				if err := os.Symlink(filepath.Dir(config), link); err != nil {
					t.Fatal(err)
				}
				command[2] = filepath.Join(link, filepath.Base(config))
			case "denied_tool":
				command[4] = "not_allowlisted"
			case "existing_output":
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			case "output_parent_missing":
				command[8] = filepath.Join(out, "not-created")
			case "output_symlink":
				if err := os.Symlink(filepath.Dir(out), out); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			before := f.requests.Load()
			err = runSource(command, strings.NewReader(""), &stdout, &stderr)
			if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || f.requests.Load() != before || strings.Contains(err.Error(), "synthetic-private") {
				t.Fatalf("invalid input reached network or exposed detail: %v", err)
			}
			if strings.HasPrefix(mode, "config_") {
				err = runSource([]string{"discover", "-config", config}, strings.NewReader(""), &stdout, &stderr)
				if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || f.requests.Load() != before {
					t.Fatal("discovery accepted a config that collect rejects")
				}
			}
		})
	}
}

func TestSourceClosedFlagsAndTimeout(t *testing.T) {
	t.Setenv("DETECTIVE_BASE_URL", "http://127.0.0.1:1/v1")
	for _, args := range [][]string{
		{"discover"}, {"collect"}, {"discover", "-config", "/private-config", "-config", "/other"},
		{"discover", "-config", "/private-config", "-tool", "read_status"},
		{"collect", "-yes"}, {"collect", "-model", "synthetic-private"},
		{"collect", "-ahe-submit-pending=true"}, {"collect", "--", "private-positional"},
		{"collect", "-timeout", "0s"}, {"collect", "-timeout", "121s"}, {"collect", "-timeout", "synthetic-private"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runSource(args, strings.NewReader(""), &stdout, &stderr); err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatalf("closed source flags were not rejected safely: %v", err)
		}
	}
}

func TestSourceStaticHelpNeedsNoConfiguration(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"discover", "-h"}, {"collect", "--help"}} {
		var stdout, stderr bytes.Buffer
		if err := runSource(args, strings.NewReader(""), &stdout, &stderr); err != nil || stdout.String() != sourceUsage || stderr.Len() != 0 {
			t.Fatalf("source help was not static: %v", err)
		}
	}
}

func sourceFDFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var controlErr error
	if err := raw.Control(func(fd uintptr) { flags, controlErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || controlErr != nil {
		t.Fatalf("cannot inspect test descriptor: %v %v", err, controlErr)
	}
	return flags
}

func TestSourceConfirmationFileRestoresDescriptor(t *testing.T) {
	for _, mode := range []string{"confirm", "timeout", "cancel", "eof"} {
		t.Run(mode, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			before := sourceFDFlags(t, reader)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			expected := "collect " + strings.Repeat("a", 64)
			if mode == "confirm" {
				if _, err := io.WriteString(writer, expected+"\n"); err != nil {
					t.Fatal(err)
				}
			} else if mode == "cancel" {
				cancel()
			} else if mode == "eof" {
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			got, err := readSourceConfirmation(ctx, reader)
			if mode == "confirm" && (err != nil || got != expected) {
				t.Fatalf("real pipe confirmation failed: %q %v", got, err)
			}
			if mode != "confirm" && (err == nil || got != "") {
				t.Fatal("unfinished file confirmation was accepted")
			}
			if time.Since(start) > time.Second || sourceFDFlags(t, reader) != before {
				t.Fatal("confirmation failed to bound waiting or restore caller descriptor flags")
			}
			if _, err := reader.Stat(); err != nil {
				t.Fatal("confirmation closed the caller's descriptor")
			}
		})
	}
}
