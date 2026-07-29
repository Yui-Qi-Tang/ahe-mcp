// Package mcpstdio implements the minimal MCP stdio JSON-RPC transport used by AHE tools.
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// ProtocolVersion is the MCP schema revision this stdio adapter targets.
	ProtocolVersion = "2025-06-18"

	maxMessageBytes = 16 << 20
)

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Backend is the domain-facing surface used by the stdio transport.
type Backend interface {
	Tools() []Tool
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error)
}

// Tool describes one MCP tool exposed by a Backend.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations Annotations    `json:"annotations,omitempty"`
}

// Annotations carries MCP tool behavior hints.
type Annotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

// Server serves one MCP stdio endpoint over newline-delimited JSON-RPC.
type Server struct {
	name    string
	version string
	backend Backend
}

// NewServer constructs a stdio server over an already-wired Backend.
func NewServer(name, version string, backend Backend) (*Server, error) {
	if name == "" {
		return nil, errors.New("server name is required")
	}
	if version == "" {
		return nil, errors.New("server version is required")
	}
	if backend == nil {
		return nil, errors.New("backend is required")
	}
	return &Server{name: name, version: version, backend: backend}, nil
}

// Serve reads newline-delimited JSON-RPC messages from in and writes responses to out.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageBytes)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		response, ok := s.handleMessage(ctx, line)
		if !ok {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("writing mcp response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading mcp request: %w", err)
	}
	return nil
}

func (s *Server) handleMessage(ctx context.Context, data []byte) (rpcResponse, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return errorResponse(json.RawMessage("null"), codeParseError, "parse error"), true
	}
	id, hasID := raw["id"]
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	methodData, ok := raw["method"]
	if !ok {
		if !hasID {
			return rpcResponse{}, false
		}
		return errorResponse(id, codeInvalidRequest, "method is required"), true
	}
	var method string
	if err := json.Unmarshal(methodData, &method); err != nil || method == "" {
		if !hasID {
			return rpcResponse{}, false
		}
		return errorResponse(id, codeInvalidRequest, "method must be a non-empty string"), true
	}
	if !hasID {
		return rpcResponse{}, false
	}

	switch method {
	case "initialize":
		return resultResponse(id, initializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities: serverCapabilities{
				Tools: toolsCapability{ListChanged: false},
			},
			ServerInfo: implementation{Name: s.name, Version: s.version},
		}), true
	case "tools/list":
		return resultResponse(id, listToolsResult{Tools: s.backend.Tools()}), true
	case "tools/call":
		result, err := s.callTool(ctx, raw["params"])
		if err != nil {
			return errorResponse(id, codeInvalidParams, err.Error()), true
		}
		return resultResponse(id, result), true
	default:
		return errorResponse(id, codeMethodNotFound, fmt.Sprintf("method %q not found", method)), true
	}
}

func (s *Server) callTool(ctx context.Context, paramsData json.RawMessage) (callToolResult, error) {
	var params callToolParams
	if len(paramsData) > 0 {
		if err := json.Unmarshal(paramsData, &params); err != nil {
			return callToolResult{}, fmt.Errorf("invalid tools/call params: %w", err)
		}
	}
	if params.Name == "" {
		return callToolResult{}, errors.New("tools/call params.name is required")
	}
	if !s.hasTool(params.Name) {
		return callToolResult{}, fmt.Errorf("unknown tool %q", params.Name)
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}
	structured, err := s.backend.CallTool(ctx, params.Name, params.Arguments)
	if err != nil {
		errorPayload := toolErrorPayload(err)
		return callToolResult{
			Content:           []contentBlock{{Type: "text", Text: string(errorPayload)}},
			StructuredContent: errorPayload,
			IsError:           true,
		}, nil
	}
	return callToolResult{
		Content:           []contentBlock{{Type: "text", Text: string(structured)}},
		StructuredContent: structured,
		IsError:           false,
	}, nil
}

func (s *Server) hasTool(name string) bool {
	for _, tool := range s.backend.Tools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func toolErrorPayload(err error) json.RawMessage {
	data, marshalErr := json.Marshal(err)
	if marshalErr == nil && string(data) != "{}" {
		return data
	}
	data, _ = json.Marshal(map[string]string{
		"code":    "tool_error",
		"message": err.Error(),
	})
	return data
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func resultResponse(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	}
}

type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      implementation     `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type callToolResult struct {
	Content           []contentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
