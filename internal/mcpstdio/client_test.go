package mcpstdio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCallToolRunsExactLoopbackContract(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	response, err := CallTool(context.Background(), CommandConfig{
		Path:        executable,
		Args:        []string{"-test.run=TestStdioClientHelperProcess"},
		Directory:   t.TempDir(),
		Environment: []string{"AHE_MCP_CLIENT_HELPER=success"},
	}, ToolCallRequest{
		Name:                    "read_document",
		ExpectedInputSchemaHash: clientHelperSchemaHash(t),
		Arguments:               json.RawMessage(`{"object_id":"AHE-42"}`),
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if response.ProtocolVersion != ProtocolVersion ||
		response.ServerName != "loopback-mcp" ||
		response.ServerVersion != "v1" ||
		response.Tool.Name != "read_document" {
		t.Fatalf("CallTool() response = %+v", response)
	}
	var structured map[string]any
	if err := json.Unmarshal(response.StructuredContent, &structured); err != nil {
		t.Fatalf("structured content error = %v", err)
	}
	if structured["object_id"] != "AHE-42" {
		t.Fatalf("structured content = %s", response.StructuredContent)
	}
}

func TestCallToolFailsClosedOnToolError(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	_, err = CallTool(context.Background(), CommandConfig{
		Path:        executable,
		Args:        []string{"-test.run=TestStdioClientHelperProcess"},
		Directory:   t.TempDir(),
		Environment: []string{"AHE_MCP_CLIENT_HELPER=tool-error"},
	}, ToolCallRequest{
		Name:                    "read_document",
		ExpectedInputSchemaHash: clientHelperSchemaHash(t),
	})
	if err == nil || !strings.Contains(err.Error(), "isError") {
		t.Fatalf("CallTool() error = %v, want isError", err)
	}
}

func TestCallToolRejectsAdvertisedWriteBeforeInvocation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	_, err = CallTool(context.Background(), CommandConfig{
		Path:        executable,
		Args:        []string{"-test.run=TestStdioClientHelperProcess"},
		Directory:   t.TempDir(),
		Environment: []string{"AHE_MCP_CLIENT_HELPER=destructive"},
	}, ToolCallRequest{
		Name:                    "read_document",
		ExpectedInputSchemaHash: clientHelperSchemaHash(t),
	})
	if err == nil || !strings.Contains(err.Error(), "destructiveHint=true") {
		t.Fatalf("CallTool() error = %v, want destructive rejection", err)
	}
}

func TestPrepareCommandConfigRequiresClosedLocalExecution(t *testing.T) {
	_, err := prepareCommandConfig(CommandConfig{Path: "mcp-server", Directory: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("prepareCommandConfig(relative) error = %v", err)
	}
	_, err = prepareCommandConfig(CommandConfig{Path: "/mcp-server"})
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("prepareCommandConfig(empty directory) error = %v", err)
	}
	_, err = prepareCommandConfig(CommandConfig{
		Path:        "/mcp-server",
		Directory:   t.TempDir(),
		Environment: []string{"INVALID"},
	})
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("prepareCommandConfig(environment) error = %v", err)
	}
	_, err = prepareCommandConfig(CommandConfig{
		Path:        "/mcp-server",
		Directory:   t.TempDir(),
		Environment: []string{"HTTP_PROXY=", "HTTP_PROXY=http://unexpected.invalid"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("prepareCommandConfig(duplicate environment) error = %v", err)
	}
}

func TestPrepareToolCallRequestCanonicalizesObject(t *testing.T) {
	request, err := prepareToolCallRequest(ToolCallRequest{
		Name:                    " read_document ",
		ExpectedInputSchemaHash: clientHelperSchemaHash(t),
		Arguments:               json.RawMessage(`{"z":1,"a":2}`),
	})
	if err != nil {
		t.Fatalf("prepareToolCallRequest() error = %v", err)
	}
	if request.Name != "read_document" || string(request.Arguments) != `{"a":2,"z":1}` {
		t.Fatalf("prepared request = %+v", request)
	}
	_, err = prepareToolCallRequest(ToolCallRequest{
		Name:                    "read_document",
		ExpectedInputSchemaHash: clientHelperSchemaHash(t),
		Arguments:               json.RawMessage(`[]`),
	})
	if err == nil {
		t.Fatal("prepareToolCallRequest(array) error = nil")
	}
	_, err = prepareToolCallRequest(ToolCallRequest{
		Name:                    "read_document",
		ExpectedInputSchemaHash: clientHelperSchemaHash(t),
		Arguments: json.RawMessage(`{"value":"` +
			strings.Repeat("x", maxClientArgumentBytes) +
			`"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("prepareToolCallRequest(oversized) error = %v", err)
	}
}

func TestStdioClientHelperProcess(t *testing.T) {
	mode := os.Getenv("AHE_MCP_CLIENT_HELPER")
	if mode == "" {
		return
	}
	backend := &clientHelperBackend{
		toolError:   mode == "tool-error",
		destructive: mode == "destructive",
	}
	server, err := NewServer("loopback-mcp", "v1", backend)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

type clientHelperBackend struct {
	toolError   bool
	destructive bool
}

func (b *clientHelperBackend) Tools() []Tool {
	readOnly := true
	destructive := b.destructive
	return []Tool{{
		Name:        "read_document",
		Description: "Returns one loopback document.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"object_id": map[string]any{"type": "string"},
			},
		},
		Annotations: Annotations{
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
		},
	}}
}

func clientHelperSchemaHash(t *testing.T) string {
	t.Helper()
	hash, err := ToolInputSchemaHash((&clientHelperBackend{}).Tools()[0].InputSchema)
	if err != nil {
		t.Fatalf("ToolInputSchemaHash() error = %v", err)
	}
	return hash
}

func (b *clientHelperBackend) CallTool(
	_ context.Context,
	_ string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if b.toolError {
		return nil, errors.New("injected tool failure")
	}
	var input struct {
		ObjectID string `json:"object_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"object_id": input.ObjectID,
	})
}
