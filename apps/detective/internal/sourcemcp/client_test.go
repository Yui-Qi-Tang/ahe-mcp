package sourcemcp

import (
	"bufio"
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
	"sync"
	"testing"
	"time"
)

const fixtureSchema = `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`

func TestValidateArgumentsJSON(t *testing.T) {
	for _, test := range []struct {
		name  string
		raw   string
		valid bool
	}{
		{"empty-object", "{}", true},
		{"preserved-whitespace", " {\n\t\"id\": \"value\"\n} \n", true},
		{"paired-surrogate", `{"text":"\ud83d\ude00"}`, true},
		{"null", "null", false},
		{"array", "[]", false},
		{"trailing-value", "{} {}", false},
		{"duplicate-key", `{"id":"one","id":"two"}`, false},
		{"nested-duplicate", `{"nested":{"id":1,"id":2}}`, false},
		{"surrogate", `{"text":"\ud800"}`, false},
		{"invalid-utf8", "{\"text\":\"\xff\"}", false},
		{"oversize", "{\"text\":\"" + strings.Repeat("x", 64<<10) + "\"}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidateArgumentsJSON(test.raw) == nil; got != test.valid {
				t.Errorf("valid = %t, want %t", got, test.valid)
			}
		})
	}
}

func TestValidConnectionIDMatchesClientValidation(t *testing.T) {
	check := func(id string, want bool) {
		t.Helper()
		if got := ValidConnectionID(id); got != want {
			t.Errorf("ValidConnectionID(%q) = %t, want %t", id, got, want)
		}
		config := Config{ID: id, Name: "Synthetic source", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", AllowedTools: []string{"read_document"}}
		if got := Validate(config) == nil; got != want {
			t.Errorf("client configuration disagrees on ID %q", id)
		}
	}
	for char := byte(0); char < 128; char++ {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '.' || char == '-'
		check("a"+string(char)+"z", allowed)
	}
	for _, id := range []string{"a", "_.-", "AZaz09_.-", strings.Repeat("x", 128)} {
		check(id, true)
	}
	for _, id := range []string{"", strings.Repeat("x", 129), "來源", "a\u202eb", "a\xffb"} {
		check(id, false)
	}
}

type sourceFixture struct {
	mu      sync.Mutex
	mode    string
	methods []string
	calls   int
	args    json.RawMessage
	started chan struct{}
	once    sync.Once
	page    int
}

func fixtureReply(method, mode string) any {
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "synthetic-source", "version": "1"}}
	case "tools/list":
		schema := fixtureSchema
		if mode == "schema-drift" {
			schema = `{"type":"object","properties":{"changed":{"type":"string"}}}`
		}
		tools := []any{
			map[string]any{"name": "read_document", "description": "Read synthetic document.\nNo company service.", "inputSchema": json.RawMessage(schema), "annotations": map[string]any{"readOnlyHint": false}},
			map[string]any{"name": "delete_document", "description": "Not allowlisted", "inputSchema": json.RawMessage(`{"type":"object"}`), "annotations": map[string]any{"readOnlyHint": true}},
		}
		if mode == "inventory-drift" {
			tools = tools[:1]
		}
		if mode == "duplicate-tool" {
			tools = append(tools, tools[0])
		}
		result := map[string]any{"tools": tools}
		if mode == "pagination-loop" {
			result["nextCursor"] = "same"
		}
		return result
	case "tools/call":
		if mode == "tool-error" {
			return map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": "synthetic-secret"}}}
		}
		if mode == "image" {
			return map[string]any{"content": []any{map[string]string{"type": "image", "data": "not-read"}}}
		}
		return map[string]any{"content": []any{map[string]string{"type": "text", "text": "合成來源第一段"}, map[string]string{"type": "text", "text": "第二段；不採納任何主張"}}, "structuredContent": map[string]any{"synthetic": true}, "isError": false}
	}
	return nil
}

func httpFixture(t *testing.T, mode string) (Config, *sourceFixture) {
	t.Helper()
	f := &sourceFixture{mode: mode, started: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("ambient credential header was forwarded")
		}
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.methods = append(f.methods, "DELETE")
			f.mu.Unlock()
			if r.Header.Get("Mcp-Session-Id") != "fixture-session" {
				t.Error("missing session on close")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			t.Error("unexpected HTTP method")
			w.WriteHeader(405)
			return
		}
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxRPCBytes+1)).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		mode := f.mode
		f.methods = append(f.methods, request.Method)
		if request.Method == "initialize" {
			f.page = 0
		}
		if request.Method == "tools/list" {
			f.page++
		}
		page := f.page
		if request.Method == "tools/call" {
			f.calls++
			f.args = append([]byte(nil), request.Params...)
		}
		f.mu.Unlock()
		if request.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "fixture-session" {
			t.Error("session ID not forwarded")
		}
		if r.Header.Get("MCP-Protocol-Version") != protocolVersion || r.Header.Get("Accept") != "application/json, text/event-stream" {
			t.Error("missing MCP transport headers")
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		if mode == "cancel" {
			f.once.Do(func() { close(f.started) })
			<-r.Context().Done()
			return
		}
		if mode == "redirect" {
			http.Redirect(w, r, "http://127.0.0.1:1/never", http.StatusTemporaryRedirect)
			return
		}
		if mode == "http-error" {
			http.Error(w, "synthetic-secret", 503)
			return
		}
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "fixture-session")
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == "oversize" {
			_, _ = io.WriteString(w, strings.Repeat("x", maxRPCBytes+1))
			return
		}
		if mode == "malformed" {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":null}`)
			return
		}
		result := fixtureReply(request.Method, mode)
		if request.Method == "tools/list" && (mode == "pages" || mode == "too-many-pages") {
			name := fmt.Sprintf("hidden_%d", page)
			if page == 1 {
				name = "read_document"
			}
			list := map[string]any{"tools": []any{map[string]any{"name": name, "inputSchema": json.RawMessage(fixtureSchema)}}}
			if page < 3 || mode == "too-many-pages" {
				list["nextCursor"] = fmt.Sprintf("page-%d", page+1)
			}
			var params map[string]string
			if json.Unmarshal(request.Params, &params) != nil {
				t.Error("invalid pagination params")
			}
			if page > 1 && params["cursor"] != fmt.Sprintf("page-%d", page) {
				t.Error("pagination cursor not passed exactly")
			}
			result = list
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		if mode == "rpc-error" {
			delete(response, "result")
			response["error"] = map[string]any{"code": -1, "message": "synthetic-secret"}
		}
		if mode == "wrong-id" {
			response["id"] = request.ID + 1
		}
		if mode == "notification" {
			response = map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"}
		}
		if mode == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, ": heartbeat\n\nevent: message\ndata: %s\n\n", mustJSON(response))
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(srv.Close)
	return Config{ID: "mock-source", Name: "模擬來源", Transport: "streamable-http", URL: srv.URL + "/mcp", AllowedTools: []string{"read_document"}}, f
}

func (f *sourceFixture) change(mode string) { f.mu.Lock(); defer f.mu.Unlock(); f.mode = mode }
func (f *sourceFixture) snapshot() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...), f.calls
}

func TestHTTPDiscoverAndCall(t *testing.T) {
	for _, mode := range []string{"json", "sse"} {
		t.Run(mode, func(t *testing.T) {
			cfg, f := httpFixture(t, mode)
			t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
			t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
			t.Setenv("NO_PROXY", "")
			tools, err := Discover(context.Background(), cfg)
			if err != nil || len(tools) != 1 {
				t.Fatalf("Discover = %v, %v", tools, err)
			}
			tool := tools[0]
			if tool.Name != "read_document" || !validDigest(tool.SchemaSHA256) || !validDigest(tool.InventorySHA256) || tool.ConfigSHA256 != configDigest(cfg) {
				t.Fatalf("bad discovery: %+v", tool)
			}
			if _, calls := f.snapshot(); calls != 0 {
				t.Fatal("discovery invoked a source tool")
			}
			result, err := Call(context.Background(), cfg, tool, `{"id":"fixture-1"}`)
			if err != nil {
				t.Fatal(err)
			}
			if result.SHA256 != digest([]byte(result.RawJSON)) || !strings.Contains(result.RawJSON, `"synthetic":true`) || result.Text != "[MCP text block 1]\n合成來源第一段\n\n[MCP text block 2]\n第二段；不採納任何主張" {
				t.Fatalf("bad result: %+v", result)
			}
			methods, calls := f.snapshot()
			want := []string{"initialize", "notifications/initialized", "tools/list", "DELETE", "initialize", "notifications/initialized", "tools/list", "tools/call", "DELETE"}
			if calls != 1 || !reflect.DeepEqual(methods, want) {
				t.Fatalf("methods=%v calls=%d", methods, calls)
			}
		})
	}
}

func TestHTTPStopsInventoryDriftWithoutSourceCall(t *testing.T) {
	for _, mode := range []string{"schema-drift", "inventory-drift", "notification"} {
		t.Run(mode, func(t *testing.T) {
			cfg, f := httpFixture(t, "json")
			tools, err := Discover(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			f.change(mode)
			_, err = Call(context.Background(), cfg, tools[0], `{"id":"fixture"}`)
			if !errors.Is(err, ErrInventoryChanged) {
				t.Fatalf("error=%v", err)
			}
			if _, calls := f.snapshot(); calls != 0 {
				t.Fatal("changed inventory reached source tool")
			}
		})
	}
}

func TestCallRejectsUntrustedSelectionBeforeSession(t *testing.T) {
	cfg, f := httpFixture(t, "json")
	tools, err := Discover(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.snapshot()
	for _, change := range []string{"forbidden", "schema", "config", "arguments", "duplicate-arguments", "null-arguments", "oversize-arguments", "missing-required", "wrong-type", "extra-property"} {
		t.Run(change, func(t *testing.T) {
			c, tool, args := cfg, tools[0], `{"id":"valid"}`
			switch change {
			case "forbidden":
				tool.Name = "delete_document"
			case "schema":
				tool.InputSchemaJSON = `{"type":"object"}`
			case "config":
				c.Name = "another identity"
			case "arguments":
				args = `[]`
			case "duplicate-arguments":
				args = `{"x":1,"x":2}`
			case "null-arguments":
				args = `null`
			case "oversize-arguments":
				args = `{"x":"` + strings.Repeat("x", maxArgsBytes) + `"}`
			case "missing-required":
				args = `{}`
			case "wrong-type":
				args = `{"id":123}`
			case "extra-property":
				args = `{"id":"valid","extra":"synthetic-secret"}`
			}
			if _, err := Call(context.Background(), c, tool, args); err == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
	after, calls := f.snapshot()
	if calls != 0 || !reflect.DeepEqual(before, after) {
		t.Fatal("invalid selection started a session")
	}
}

func TestHTTPFailuresDoNotRetryOrLeak(t *testing.T) {
	for _, mode := range []string{"http-error", "redirect", "rpc-error", "wrong-id", "malformed", "oversize", "duplicate-tool", "pagination-loop"} {
		t.Run(mode, func(t *testing.T) {
			cfg, f := httpFixture(t, mode)
			if tools, err := Discover(context.Background(), cfg); err == nil || tools != nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("Discover=%v,%v", tools, err)
			}
			methods, calls := f.snapshot()
			initializes := 0
			for _, m := range methods {
				if m == "initialize" {
					initializes++
				}
			}
			if calls != 0 || initializes != 1 {
				t.Fatalf("unexpected retry/tool calls: %v", methods)
			}
		})
	}
	for _, mode := range []string{"tool-error", "image"} {
		t.Run(mode, func(t *testing.T) {
			cfg, f := httpFixture(t, "json")
			tools, err := Discover(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			f.change(mode)
			result, err := Call(context.Background(), cfg, tools[0], `{"id":"fixture"}`)
			if err == nil || result != (Result{}) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("Call=%+v,%v", result, err)
			}
			if _, calls := f.snapshot(); calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestHTTPCancellation(t *testing.T) {
	cfg, f := httpFixture(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Discover(ctx, cfg); done <- err }()
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("mock request not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not return")
	}
	if methods, calls := f.snapshot(); calls != 0 || len(methods) != 1 {
		t.Fatalf("unexpected operations: %v", methods)
	}
}

func TestValidateRejectsRemoteOrCredentialEndpoints(t *testing.T) {
	base := Config{ID: "source", Name: "source", Transport: "streamable-http", AllowedTools: []string{"read_document"}}
	for _, endpoint := range []string{"https://example.com/mcp", "http://example.com:80/mcp", "http://localhost:80/mcp", "http://127.0.0.1/mcp", "http://127.0.0.1:80/mcp?token=private", "http://user:pass@127.0.0.1:80/mcp", "http://127.0.0.1:80/mcp#x", "http://127.0.0.2:80/mcp", "http://0.0.0.0:80/mcp", "http://[::ffff:127.0.0.1]:80/mcp"} {
		c := base
		c.URL = endpoint
		if Validate(c) == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:8123/mcp", "http://[::1]:8123/mcp"} {
		c := base
		c.URL = endpoint
		if err := Validate(c); err != nil {
			t.Fatal(err)
		}
	}
}

func stdioFixture(t *testing.T, mode string) Config {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(t.TempDir(), "launcher")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	contents := "#!/bin/sh\nexec " + quote(binary) + " -test.run=^TestSourceMCPHelperProcess$ -- source-mcp-fixture " + quote(mode) + "\n"
	if err := os.WriteFile(launcher, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{ID: "stdio-source", Name: "local synthetic tool", Transport: "stdio", Command: launcher, AllowedTools: []string{"read_document"}}
}

func TestStdioSourceDiscoveryAndCall(t *testing.T) {
	for _, key := range []string{"DATABASE_DSN", "PGPASSWORD", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "HTTP_PROXY", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD", "HOME"} {
		t.Setenv(key, "synthetic-secret")
	}
	cfg := stdioFixture(t, "valid")
	tools, err := Discover(context.Background(), cfg)
	if err != nil || len(tools) != 1 {
		t.Fatalf("Discover=%v,%v", tools, err)
	}
	result, err := Call(context.Background(), cfg, tools[0], `{"id":"local"}`)
	if err != nil || !strings.Contains(result.Text, "合成來源") || result.SHA256 != digest([]byte(result.RawJSON)) {
		t.Fatalf("Call=%+v,%v", result, err)
	}
}

func TestStdioFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"malformed", "oversize", "rpc-error", "nonzero", "stall"} {
		t.Run(mode, func(t *testing.T) {
			cfg := stdioFixture(t, mode)
			timeout := 3 * time.Second
			if mode == "stall" {
				timeout = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			tools, err := Discover(ctx, cfg)
			if err == nil || tools != nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("Discover=%v,%v", tools, err)
			}
			if mode == "stall" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline error=%v", err)
			}
			if mode == "nonzero" && err.Error() != "source MCP launcher exited unsuccessfully" {
				t.Fatalf("nonzero exit was not independently verified: %v", err)
			}
		})
	}
}

func TestProtectedLauncher(t *testing.T) {
	cfg := stdioFixture(t, "valid")
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.Command, 0o722); err != nil {
		t.Fatal(err)
	}
	if Validate(cfg) == nil {
		t.Fatal("writable launcher accepted")
	}
	if err := os.Chmod(cfg.Command, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(cfg.Command, link); err != nil {
		t.Fatal(err)
	}
	cfg.Command = link
	if Validate(cfg) == nil {
		t.Fatal("symlink accepted")
	}
	for _, command := range []string{"launcher", "SECRET=value /bin/tool", "/bin/tool --arg"} {
		cfg.Command = command
		if Validate(cfg) == nil {
			t.Fatal("non-launcher accepted")
		}
	}
}

func TestSourceMCPHelperProcess(t *testing.T) {
	pos := -1
	for i, arg := range os.Args {
		if arg == "source-mcp-fixture" {
			pos = i
			break
		}
	}
	if pos < 0 {
		return
	}
	mode := os.Args[pos+1]
	for _, key := range []string{"DATABASE_DSN", "PGPASSWORD", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "HTTP_PROXY", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD", "HOME"} {
		if os.Getenv(key) != "" {
			os.Exit(20)
		}
	}
	_, _ = fmt.Fprintln(os.Stderr, "synthetic-secret must never be surfaced")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request rpcRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(21)
		}
		if request.ID == 0 {
			continue
		}
		switch mode {
		case "malformed":
			_, _ = fmt.Fprintln(os.Stdout, "synthetic-secret not-json")
			continue
		case "oversize":
			_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", maxRPCBytes+1))
			continue
		case "stall":
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		case "rpc-error":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -1, "message": "synthetic-secret"}})
			continue
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": fixtureReply(request.Method, "valid")})
	}
	if mode == "nonzero" {
		os.Exit(23)
	}
	os.Exit(0)
}

func TestJSONRejectsDuplicateKeysAndDepth(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{"x":{"n":1,"\u006e":2}}`, `{} {}`, strings.Repeat("[", 66) + strings.Repeat("]", 66), "{\"x\":\"\xff\"}"} {
		var v any
		if decodeJSON([]byte(raw), &v) == nil {
			t.Fatalf("accepted invalid JSON")
		}
	}
}

func TestSSEBoundsAndUnfinishedResponse(t *testing.T) {
	for _, raw := range []string{"data: {}\n", "event: endpoint\ndata: {}\n\n", strings.Repeat(":x\n", maxRPCBytes/2), "data: " + strings.Repeat("x", maxRPCBytes) + "\n\n"} {
		if _, err := readSSE(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted malformed/unbounded SSE")
		}
	}
}

func TestArgumentsValidatedLocallyWithoutDefaultsOrRemoteReferences(t *testing.T) {
	for _, args := range []map[string]any{{}, {"id": 4}, {"id": "ok", "extra": "synthetic-secret"}} {
		if err := validateArguments(fixtureSchema, args); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("invalid arguments accepted or leaked: %v", err)
		}
	}
	args := map[string]any{"id": "exact value"}
	if err := validateArguments(fixtureSchema, args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, map[string]any{"id": "exact value"}) {
		t.Fatal("arguments mutated")
	}
	for _, schema := range []string{
		`{"type":"object","$ref":"http://127.0.0.1:1/private"}`,
		`{"type":"object","$ref":"#"}`,
		`{"type":"object","properties":{"id":{"$dynamicRef":"#node"}}}`,
	} {
		if err := validateArguments(schema, args); err == nil {
			t.Fatal("reference-bearing schema accepted")
		}
	}
	if err := validateArguments(`{"type":"object","properties":{"id":{"type":"integer"}}}`, map[string]any{"id": json.Number("123")}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"9007199254740992", "1e999999", "1e-99999", "1.0000000000000000000000000001"} {
		if err := validateArguments(`{"type":"object","properties":{"id":{"type":"integer"}}}`, map[string]any{"id": json.Number(value)}); err == nil {
			t.Fatal("unsafe numeric projection accepted")
		}
	}
	if err := validateArguments(`{"type":"object","properties":{"id":{"type":"number"}}}`, map[string]any{"id": json.Number("0.1")}); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryPaginationIsBounded(t *testing.T) {
	for _, mode := range []string{"pages", "too-many-pages"} {
		t.Run(mode, func(t *testing.T) {
			cfg, f := httpFixture(t, mode)
			tools, err := Discover(context.Background(), cfg)
			if mode == "pages" {
				if err != nil || len(tools) != 1 {
					t.Fatalf("Discover=%v,%v", tools, err)
				}
				if _, err := Call(context.Background(), cfg, tools[0], `{"id":"paged"}`); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || tools != nil {
				t.Fatalf("page bound not enforced: %v,%v", tools, err)
			}
			methods, calls := f.snapshot()
			lists := 0
			for _, method := range methods {
				if method == "tools/list" {
					lists++
				}
			}
			if mode == "pages" && (lists != 6 || calls != 1) {
				t.Fatalf("operations=%v", methods)
			}
			if mode != "pages" && (lists != maxPages || calls != 0) {
				t.Fatalf("operations=%v", methods)
			}
		})
	}
}

func TestSourceJSONUnicodeAndErrorStatus(t *testing.T) {
	for _, raw := range []string{`{"content":[{"type":"text","text":"\ud800"}]}`, `{"content":[{"type":"text","text":"\udc00"}]}`, `{"content":[{"type":"text","text":"\u0000"}]}`, `{"content":[{"type":"text","text":"ok"}],"isError":null}`} {
		if result, err := sourceResult([]byte(raw)); err == nil || result != (Result{}) {
			t.Fatal("invalid source result accepted")
		}
	}
	for _, raw := range []string{`{"content":[{"type":"text","text":"\ud83d\ude00"}]}`, `{"content":[{"type":"text","text":"\ufffd"}]}`, `{"content":[{"type":"text","text":"\\ud800"}]}`} {
		if result, err := sourceResult([]byte(raw)); err != nil || result.RawJSON != raw {
			t.Fatalf("valid source changed/rejected: %v", err)
		}
	}
}
