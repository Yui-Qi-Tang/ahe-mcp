package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrToolContract identifies an advertised MCP tool that contradicts its pinned call contract.
var ErrToolContract = errors.New("mcp tool contract violation")

const (
	clientCallTimeout      = 30 * time.Second
	maxClientResponseBytes = 2 << 20
	maxClientArguments     = 64
	maxClientArgumentBytes = 64 << 10
	maxClientEnvironment   = 128
	maxClientValueBytes    = 8 << 10
	maxClientStderrBytes   = 64 << 10
)

// CommandConfig identifies one explicitly selected local MCP stdio process.
// Environment is not inherited; callers must provide every required variable.
type CommandConfig struct {
	Path        string
	Args        []string
	Directory   string
	Environment []string
}

// ToolCallRequest selects one exact tool and JSON object argument.
type ToolCallRequest struct {
	Name                    string
	ExpectedInputSchemaHash string
	Arguments               json.RawMessage
}

// ToolCallResponse returns the advertised tool contract and exact structured result.
type ToolCallResponse struct {
	ProtocolVersion   string
	ServerName        string
	ServerVersion     string
	Tool              Tool
	StructuredContent json.RawMessage
}

// NormalizeCommandConfig validates and canonicalizes one closed local process
// command without starting it.
func NormalizeCommandConfig(config CommandConfig) (CommandConfig, error) {
	return prepareCommandConfig(config)
}

type clientRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type clientCallToolResult struct {
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// CallTool starts one bounded MCP process, validates its advertised tool, and calls it once.
func CallTool(
	ctx context.Context,
	command CommandConfig,
	request ToolCallRequest,
) (ToolCallResponse, error) {
	if ctx == nil {
		return ToolCallResponse{}, errors.New("mcp client context is required")
	}
	command, err := prepareCommandConfig(command)
	if err != nil {
		return ToolCallResponse{}, err
	}
	request, err = prepareToolCallRequest(request)
	if err != nil {
		return ToolCallResponse{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, clientCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, command.Path, command.Args...)
	cmd.Dir = command.Directory
	cmd.Env = append([]string(nil), command.Environment...)
	cmd.WaitDelay = time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ToolCallResponse{}, fmt.Errorf("opening mcp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ToolCallResponse{}, fmt.Errorf("opening mcp stdout: %w", err)
	}
	var stderr boundedClientBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return ToolCallResponse{}, fmt.Errorf("starting mcp process: %w", err)
	}
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxClientResponseBytes)
	encoder := json.NewEncoder(stdin)

	initialize, err := clientRequest[initializeResult](scanner, encoder, 1, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": implementation{
			Name:    "ahe-mcp",
			Version: "v1",
		},
	})
	if err != nil {
		return ToolCallResponse{}, commandError(err, stderr.String())
	}
	if initialize.ProtocolVersion != ProtocolVersion {
		return ToolCallResponse{}, fmt.Errorf(
			"mcp server selected unsupported protocol version %q",
			initialize.ProtocolVersion,
		)
	}

	listed, err := clientRequest[listToolsResult](scanner, encoder, 2, "tools/list", map[string]any{})
	if err != nil {
		return ToolCallResponse{}, commandError(err, stderr.String())
	}
	tool, err := exactListedTool(listed.Tools, request.Name)
	if err != nil {
		return ToolCallResponse{}, err
	}
	if tool.Annotations.ReadOnlyHint != nil && !*tool.Annotations.ReadOnlyHint {
		return ToolCallResponse{}, fmt.Errorf("%w: tool %q declares readOnlyHint=false", ErrToolContract, request.Name)
	}
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		return ToolCallResponse{}, fmt.Errorf("%w: tool %q declares destructiveHint=true", ErrToolContract, request.Name)
	}
	schemaHash, err := ToolInputSchemaHash(tool.InputSchema)
	if err != nil {
		return ToolCallResponse{}, err
	}
	if schemaHash != request.ExpectedInputSchemaHash {
		return ToolCallResponse{}, fmt.Errorf(
			"%w: tool %q input schema hash %s does not match pinned %s",
			ErrToolContract,
			request.Name,
			schemaHash,
			request.ExpectedInputSchemaHash,
		)
	}
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}); err != nil {
		return ToolCallResponse{}, fmt.Errorf("writing initialized notification: %w", err)
	}

	called, err := clientRequest[clientCallToolResult](scanner, encoder, 3, "tools/call", map[string]any{
		"name":      request.Name,
		"arguments": request.Arguments,
	})
	if err != nil {
		return ToolCallResponse{}, commandError(err, stderr.String())
	}
	if called.IsError {
		return ToolCallResponse{}, errors.New("mcp tool returned isError")
	}
	if len(called.StructuredContent) == 0 || !json.Valid(called.StructuredContent) {
		return ToolCallResponse{}, errors.New("mcp tool returned invalid structuredContent")
	}

	if err := stdin.Close(); err != nil {
		return ToolCallResponse{}, fmt.Errorf("closing mcp stdin: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		waited = true
		return ToolCallResponse{}, commandError(fmt.Errorf("waiting for mcp process: %w", err), stderr.String())
	}
	waited = true
	return ToolCallResponse{
		ProtocolVersion:   initialize.ProtocolVersion,
		ServerName:        initialize.ServerInfo.Name,
		ServerVersion:     initialize.ServerInfo.Version,
		Tool:              tool,
		StructuredContent: append(json.RawMessage(nil), called.StructuredContent...),
	}, nil
}

func prepareCommandConfig(config CommandConfig) (CommandConfig, error) {
	if !filepath.IsAbs(config.Path) {
		return CommandConfig{}, errors.New("mcp command path must be absolute")
	}
	if !filepath.IsAbs(config.Directory) {
		return CommandConfig{}, errors.New("mcp command directory must be absolute")
	}
	args, err := boundedClientValues(config.Args, "argument", maxClientArguments)
	if err != nil {
		return CommandConfig{}, err
	}
	environment, err := boundedClientValues(config.Environment, "environment entry", maxClientEnvironment)
	if err != nil {
		return CommandConfig{}, err
	}
	environmentNames := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" {
			return CommandConfig{}, fmt.Errorf("mcp environment entry %q is invalid", entry)
		}
		if _, exists := environmentNames[name]; exists {
			return CommandConfig{}, fmt.Errorf("mcp environment variable %q is duplicated", name)
		}
		environmentNames[name] = struct{}{}
	}
	return CommandConfig{
		Path:        filepath.Clean(config.Path),
		Args:        args,
		Directory:   filepath.Clean(config.Directory),
		Environment: environment,
	}, nil
}

func prepareToolCallRequest(request ToolCallRequest) (ToolCallRequest, error) {
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 200 {
		return ToolCallRequest{}, errors.New("mcp tool name must contain 1 to 200 bytes")
	}
	if !validClientSHA256(request.ExpectedInputSchemaHash) {
		return ToolCallRequest{}, errors.New("mcp expected input schema hash is invalid")
	}
	if len(request.Arguments) == 0 {
		request.Arguments = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(request.Arguments, &object); err != nil || object == nil {
		return ToolCallRequest{}, errors.New("mcp tool arguments must be a JSON object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return ToolCallRequest{}, fmt.Errorf("canonicalizing mcp tool arguments: %w", err)
	}
	if len(canonical) > maxClientArgumentBytes {
		return ToolCallRequest{}, fmt.Errorf("mcp tool arguments exceed %d bytes", maxClientArgumentBytes)
	}
	return ToolCallRequest{
		Name:                    request.Name,
		ExpectedInputSchemaHash: request.ExpectedInputSchemaHash,
		Arguments:               canonical,
	}, nil
}

func boundedClientValues(values []string, field string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("mcp command has more than %d %ss", limit, field)
	}
	result := make([]string, len(values))
	for index, value := range values {
		if strings.IndexByte(value, 0) >= 0 || len(value) > maxClientValueBytes {
			return nil, fmt.Errorf("mcp command %s %d is invalid", field, index)
		}
		result[index] = value
	}
	return result, nil
}

func clientRequest[T any](
	scanner *bufio.Scanner,
	encoder *json.Encoder,
	id int,
	method string,
	params any,
) (T, error) {
	var zero T
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return zero, fmt.Errorf("writing mcp %s request: %w", method, err)
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return zero, fmt.Errorf("reading mcp %s response: %w", method, err)
		}
		return zero, fmt.Errorf("mcp process ended before %s response", method)
	}
	var response clientRPCResponse
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return zero, fmt.Errorf("decoding mcp %s response: %w", method, err)
	}
	if response.JSONRPC != "2.0" || string(response.ID) != fmt.Sprint(id) {
		return zero, fmt.Errorf("mcp %s response has invalid JSON-RPC identity", method)
	}
	if response.Error != nil {
		return zero, fmt.Errorf("mcp %s error %d: %s", method, response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 {
		return zero, fmt.Errorf("mcp %s response omitted result", method)
	}
	var result T
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return zero, fmt.Errorf("decoding mcp %s result: %w", method, err)
	}
	return result, nil
}

func exactListedTool(tools []Tool, name string) (Tool, error) {
	var matched *Tool
	for index := range tools {
		if tools[index].Name != name {
			continue
		}
		if matched != nil {
			return Tool{}, fmt.Errorf("%w: server advertised duplicate tool %q", ErrToolContract, name)
		}
		copy := tools[index]
		matched = &copy
	}
	if matched == nil {
		return Tool{}, fmt.Errorf("%w: server did not advertise tool %q", ErrToolContract, name)
	}
	if matched.InputSchema == nil {
		return Tool{}, fmt.Errorf("%w: tool %q omitted inputSchema", ErrToolContract, name)
	}
	return *matched, nil
}

// ToolInputSchemaHash returns the canonical SHA-256 identity of one advertised input schema.
func ToolInputSchemaHash(schema map[string]any) (string, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("%w: encoding tool input schema: %v", ErrToolContract, err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func validClientSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func commandError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: stderr: %s", err, stderr)
}

type boundedClientBuffer struct {
	data      bytes.Buffer
	truncated bool
}

func (b *boundedClientBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := maxClientStderrBytes - b.data.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.data.Write(data)
	return original, nil
}

func (b *boundedClientBuffer) String() string {
	value := strings.ToValidUTF8(b.data.String(), "?")
	value = strings.Join(strings.Fields(value), " ")
	if b.truncated {
		value += " [truncated]"
	}
	return value
}
