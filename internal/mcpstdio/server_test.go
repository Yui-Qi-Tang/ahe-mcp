package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestServeInitializeAndListTools(t *testing.T) {
	backend := &fakeBackend{tools: []Tool{{
		Name:        "get_evidence_record",
		Description: "Read one evidence record.",
		InputSchema: objectSchema(map[string]any{
			"proposal_occurrence_id": map[string]any{"type": "string"},
		}, nil),
		Annotations: Annotations{ReadOnlyHint: boolPtr(true)},
	}}}
	server := newTestServer(t, backend)
	var out bytes.Buffer
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		"",
	}, "\n")

	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	responses := decodeResponses(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2: %s", len(responses), out.String())
	}
	if got := responses[0]["result"].(map[string]any)["protocolVersion"]; got != ProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", got, ProtocolVersion)
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "get_evidence_record" {
		t.Fatalf("tools/list result = %+v", tools)
	}
}

func TestServeCallToolReturnsStructuredContent(t *testing.T) {
	backend := &fakeBackend{
		tools:      []Tool{{Name: "echo", Description: "Echo.", InputSchema: objectSchema(nil, nil)}},
		callResult: json.RawMessage(`{"ok":true,"value":"hello"}`),
	}
	server := newTestServer(t, backend)
	var out bytes.Buffer

	err := server.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"echo","arguments":{"value":"hello"}}}`+"\n"), &out)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if backend.callName != "echo" || string(backend.callArguments) != `{"value":"hello"}` {
		t.Fatalf("call forwarded = %q/%s", backend.callName, backend.callArguments)
	}
	responses := decodeResponses(t, out.String())
	result := responses[0]["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("isError = %v, want false", result["isError"])
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["ok"] != true || structured["value"] != "hello" {
		t.Fatalf("structuredContent = %+v", structured)
	}
}

func TestServeCallToolDomainErrorUsesToolResult(t *testing.T) {
	backend := &fakeBackend{
		tools:   []Tool{{Name: "submit", Description: "Submit.", InputSchema: objectSchema(nil, nil)}},
		callErr: errors.New("invalid span"),
	}
	server := newTestServer(t, backend)
	var out bytes.Buffer

	err := server.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit"}}`+"\n"), &out)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	responses := decodeResponses(t, out.String())
	result := responses[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("isError = %v, want true", result["isError"])
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["message"] != "invalid span" {
		t.Fatalf("structuredContent = %+v", structured)
	}
}

func TestServeErrorsAndNotifications(t *testing.T) {
	backend := &fakeBackend{
		tools: []Tool{{Name: "known", Description: "Known.", InputSchema: objectSchema(nil, nil)}},
	}
	server := newTestServer(t, backend)
	var out bytes.Buffer
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"unknown"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"missing"}}`,
		"",
	}, "\n")

	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	responses := decodeResponses(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("responses = %d, want 2: %s", len(responses), out.String())
	}
	assertRPCError(t, responses[0], codeMethodNotFound)
	assertRPCError(t, responses[1], codeInvalidParams)
}

func newTestServer(t *testing.T, backend Backend) *Server {
	t.Helper()
	server, err := NewServer("test-server", "v1", backend)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func decodeResponses(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("Unmarshal response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

func assertRPCError(t *testing.T, response map[string]any, wantCode int) {
	t.Helper()
	errObj := response["error"].(map[string]any)
	if int(errObj["code"].(float64)) != wantCode {
		t.Fatalf("error code = %v, want %d", errObj["code"], wantCode)
	}
}

type fakeBackend struct {
	tools         []Tool
	callName      string
	callArguments json.RawMessage
	callResult    json.RawMessage
	callErr       error
}

func (b *fakeBackend) Tools() []Tool {
	return append([]Tool(nil), b.tools...)
}

func (b *fakeBackend) CallTool(_ context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	b.callName = name
	b.callArguments = append(json.RawMessage(nil), arguments...)
	if b.callErr != nil {
		return nil, b.callErr
	}
	return b.callResult, nil
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	if properties != nil {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func boolPtr(value bool) *bool {
	return &value
}
