package ahemcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTransportRequiresAbsoluteExecutable(t *testing.T) {
	for _, command := range []string{"", "ahe-ingest-mcp", "DATABASE_DNS=secret /bin/cat", "/bin/cat --secret", "/bin/../bin/cat", t.TempDir()} {
		t.Run(command, func(t *testing.T) {
			if c, err := start(context.Background(), command); err == nil || c != nil {
				t.Fatalf("start() = %v, %v; want rejection", c, err)
			}
		})
	}
}

func TestTransportSanitizesEnvironmentAndExitsOnEOF(t *testing.T) {
	for _, name := range []string{"PGPASSWORD", "PGSERVICE", "DATABASE_DNS", "AHE_RUNTIME_PROFILE", "AHE_DATABASE_ROLE", "OPENAI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD", "GODEBUG"} {
		t.Setenv(name, "synthetic-secret")
	}
	t.Setenv("PATH", "/synthetic/unsafe")
	launcher, _ := transportLauncher(t, "environment")
	c, err := start(context.Background(), launcher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.initialize(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		EnvironmentOK bool `json:"environment_ok"`
	}
	if err := c.call("get_extractor_input", map[string]string{}, &result); err != nil || !result.EnvironmentOK {
		t.Fatalf("call() = %+v, %v", result, err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	assertTransportJoined(t, c)
}

func TestTransportRejectsCapabilityDriftBeforeSourceCall(t *testing.T) {
	for _, mode := range []string{"wrong-protocol", "query-server", "reviewer-tools", "extra-writer", "missing-tool", "duplicate-tool", "changing-tools", "extra-capability", "pagination", "missing-schema"} {
		t.Run(mode, func(t *testing.T) {
			launcher, marker := transportLauncher(t, mode)
			c, err := start(context.Background(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if err := c.initialize(); err == nil {
				t.Fatal("initialize() succeeded for a non-intake peer")
			}
			if err := c.call("submit_text_source", map[string]string{}, &struct{}{}); err == nil {
				t.Fatal("source call accepted after rejected initialization")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source/tool call reached peer: %v", err)
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestTransportRejectsMalformedRPCAndRedactsDiagnostics(t *testing.T) {
	for _, mode := range []string{"malformed", "wrong-id", "string-id", "wrong-jsonrpc", "duplicate-key", "escaped-duplicate", "nested-duplicate", "both-result-error", "null-result", "unknown-envelope", "rpc-error", "oversized", "early-eof", "notification"} {
		t.Run(mode, func(t *testing.T) {
			launcher, _ := transportLauncher(t, mode)
			c, err := start(context.Background(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			err = c.initialize()
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("initialize() error = %v", err)
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestTransportRejectsInvalidToolResults(t *testing.T) {
	for _, mode := range []string{"tool-error", "tool-invalid-json", "tool-duplicate", "tool-mismatch", "tool-extra-block", "tool-unknown-envelope"} {
		t.Run(mode, func(t *testing.T) {
			launcher, _ := transportLauncher(t, mode)
			c, err := start(context.Background(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if err := c.initialize(); err != nil {
				t.Fatal(err)
			}
			err = c.call("get_extractor_input", map[string]string{}, &struct{}{})
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("call() error = %v", err)
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestTransportRejectsNonPendingTools(t *testing.T) {
	for _, tool := range []string{"submit_manual_evidence", "submit_external_source", "admit_reviewed_source_claim", "admit_pending_proposal", "get_evidence_record"} {
		t.Run(tool, func(t *testing.T) {
			launcher, marker := transportLauncher(t, "valid")
			c, err := start(context.Background(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if err := c.initialize(); err != nil {
				t.Fatal(err)
			}
			if err := c.call(tool, map[string]string{}, &struct{}{}); err == nil {
				t.Fatal("non-pending tool accepted")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied tool reached peer: %v", err)
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestTransportCancellationInterruptsBlockedResponseAndJoins(t *testing.T) {
	launcher, marker := transportLauncher(t, "hang-call")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := start(ctx, launcher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.initialize(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- c.call("get_extractor_input", map[string]string{}, &struct{}{}) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			cancel()
			<-result
			t.Fatal("peer did not receive the call")
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call() = %v, want cancellation", err)
		}
	case <-deadline.C:
		t.Fatal("canceled call did not finish")
	}
	assertTransportJoined(t, c)
}

func TestTransportCloseRejectsAbnormalOrMissingEOFExit(t *testing.T) {
	for _, mode := range []string{"abnormal-eof", "hang-eof"} {
		t.Run(mode, func(t *testing.T) {
			launcher, _ := transportLauncher(t, mode)
			c, err := start(context.Background(), launcher)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if err := c.initialize(); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("Close() = %v", err)
			}
			assertTransportJoined(t, c)
		})
	}
}

func TestTransportJSONLimits(t *testing.T) {
	for _, data := range []string{`{"x":1,"x":2}`, `{"x":1,"\u0078":2}`, `{"a":[{"x":1,"x":2}]}`, `{} {}`, "{\"x\":\"\xff\"}", strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66), strings.Repeat(" ", maxRPCBytes)} {
		var output any
		if err := decodeRPCJSON([]byte(data), &output, false); err == nil {
			t.Fatalf("invalid JSON accepted (%d bytes)", len(data))
		}
	}
	if sameJSON([]byte(`{"value":9007199254740992}`), []byte(`{"value":9007199254740993}`)) {
		t.Fatal("large distinct JSON numbers collapsed")
	}
	if !sameJSON([]byte(`{"a":1,"b":2}`), []byte(`{ "b": 2, "a": 1 }`)) {
		t.Fatal("equivalent JSON objects differ")
	}
}

func assertTransportJoined(t *testing.T, c *client) {
	t.Helper()
	select {
	case <-c.waited:
	default:
		t.Fatal("child Wait was not joined")
	}
	if c.command.ProcessState == nil {
		t.Fatal("child process state is unavailable")
	}
}

func transportLauncher(t *testing.T, mode string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "tool-called")
	launcher := filepath.Join(directory, "intake-launcher")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport DETECTIVE_TRANSPORT_HELPER=" + quote(mode) + "\nexport DETECTIVE_TRANSPORT_MARKER=" + quote(marker) + "\nexec " + quote(os.Args[0]) + " -test.run='^TestTransportHelper$'\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return launcher, marker
}

// TestTransportHelper is an actual child process, selected only by the trusted
// synthetic wrapper after the production transport has sanitized its environment.
func TestTransportHelper(t *testing.T) {
	mode := os.Getenv("DETECTIVE_TRANSPORT_HELPER")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "query-readback-") {
		os.Exit(serveQueryHelper(mode))
	}
	if strings.HasPrefix(mode, "review-workflow-") {
		os.Exit(serveReviewHelper(strings.TrimPrefix(mode, "review-workflow-")))
	}
	os.Exit(serveTransportHelper(mode))
}

func serveTransportHelper(mode string) int {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxRPCBytes)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return 2
		}
		if request.Method == "notifications/initialized" {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			if raw := malformedTransportResponse(mode); raw != "" {
				_, _ = fmt.Fprintln(os.Stderr, "synthetic-secret on stderr")
				_, _ = fmt.Fprintln(os.Stdout, raw)
				_, _ = io.Copy(io.Discard, os.Stdin)
				return 0
			}
			if mode == "early-eof" {
				return 3
			}
			result = transportInitializeResult(mode)
		case "tools/list":
			result = transportToolsResult(mode)
		case "tools/call":
			if err := os.WriteFile(os.Getenv("DETECTIVE_TRANSPORT_MARKER"), []byte("called"), 0o600); err != nil {
				return 4
			}
			if mode == "hang-call" {
				_, _ = io.Copy(io.Discard, os.Stdin)
				return 0
			}
			result = transportToolResult(mode)
		default:
			return 5
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return 6
		}
	}
	if mode == "abnormal-eof" {
		_, _ = fmt.Fprintln(os.Stderr, "synthetic-secret after EOF")
		return 7
	}
	if mode == "hang-eof" {
		time.Sleep(time.Minute)
	}
	return 0
}

func transportInitializeResult(mode string) map[string]any {
	name, version := "ahe-ingest-mcp", protocolVersion
	if mode == "query-server" {
		name = "ahe-query-mcp"
	}
	if mode == "wrong-protocol" {
		version = "1900-01-01"
	}
	capabilities := map[string]any{"tools": map[string]bool{"listChanged": mode == "changing-tools"}}
	if mode == "extra-capability" {
		capabilities["sampling"] = map[string]any{}
	}
	return map[string]any{"protocolVersion": version, "serverInfo": map[string]string{"name": name, "version": "synthetic"}, "capabilities": capabilities}
}

func transportToolsResult(mode string) map[string]any {
	names := []string{"submit_manual_evidence", "submit_text_source", "submit_external_source", "submit_extractor_output", "get_extractor_input"}
	switch mode {
	case "reviewer-tools":
		names = []string{"get_source_claim_review", "admit_reviewed_source_claim"}
	case "extra-writer":
		names = append(names, "admit_pending_proposal")
	case "missing-tool":
		names = names[:4]
	case "duplicate-tool":
		names[4] = names[0]
	}
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tool := map[string]any{"name": name, "inputSchema": map[string]string{"type": "object"}}
		if mode == "missing-schema" {
			delete(tool, "inputSchema")
		}
		tools = append(tools, tool)
	}
	result := map[string]any{"tools": tools}
	if mode == "pagination" {
		result["nextCursor"] = "more"
	}
	return result
}

func transportToolResult(mode string) map[string]any {
	text := `{"environment_ok":true}`
	if mode == "environment" {
		for _, entry := range os.Environ() {
			if strings.Contains(entry, "synthetic-secret") || strings.HasPrefix(entry, "PATH=") && entry != "PATH=/usr/bin:/bin:/usr/sbin:/sbin" {
				text = `{"environment_ok":false}`
			}
		}
	}
	switch mode {
	case "tool-error":
		text = `{"message":"synthetic-secret"}`
	case "tool-invalid-json":
		text = `synthetic-secret`
	case "tool-duplicate":
		text = `{"a":1,"a":2}`
	}
	blocks := []map[string]string{{"type": "text", "text": text}}
	if mode == "tool-extra-block" {
		blocks = append(blocks, blocks[0])
	}
	result := map[string]any{"content": blocks, "isError": mode == "tool-error"}
	if mode == "tool-mismatch" {
		result["structuredContent"] = map[string]bool{"environment_ok": false}
	}
	if mode == "tool-unknown-envelope" {
		result["untrusted"] = "synthetic-secret"
	}
	return result
}

func malformedTransportResponse(mode string) string {
	switch mode {
	case "malformed":
		return `synthetic-secret`
	case "wrong-id":
		return `{"jsonrpc":"2.0","id":8,"result":{}}`
	case "string-id":
		return `{"jsonrpc":"2.0","id":"1","result":{}}`
	case "wrong-jsonrpc":
		return `{"jsonrpc":"1.0","id":1,"result":{}}`
	case "duplicate-key":
		return `{"jsonrpc":"2.0","id":1,"id":1,"result":{}}`
	case "escaped-duplicate":
		return `{"jsonrpc":"2.0","id":1,"\u0069d":1,"result":{}}`
	case "nested-duplicate":
		return `{"jsonrpc":"2.0","id":1,"result":{"a":1,"a":2}}`
	case "both-result-error":
		return `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"synthetic-secret"}}`
	case "null-result":
		return `{"jsonrpc":"2.0","id":1,"result":null}`
	case "unknown-envelope":
		return `{"jsonrpc":"2.0","id":1,"result":{},"message":"synthetic-secret"}`
	case "rpc-error":
		return `{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"synthetic-secret"}}`
	case "oversized":
		return strings.Repeat("x", maxRPCBytes+1)
	case "notification":
		return `{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"synthetic-secret"}}`
	}
	return ""
}
