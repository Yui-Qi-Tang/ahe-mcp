package mcpstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DiscoveredTool exposes configuration pins from the server's advertised schema.
// Annotations are provider hints, not verification of implementation behavior.
type DiscoveredTool struct {
	Name            string      `json:"provider_tool_name"`
	InputSchemaHash string      `json:"provider_tool_input_schema_hash"`
	Annotations     Annotations `json:"annotations"`
}

// DiscoveryResult identifies the server and every tool it advertised.
type DiscoveryResult struct {
	ServerName    string           `json:"server_name"`
	ServerVersion string           `json:"server_version"`
	Tools         []DiscoveredTool `json:"tools"`
}

// DiscoverTools starts the selected process and lists tools without calling any.
// It uses the same schema hash as CallTool and a bounded process lifetime.
// Child stderr is deliberately not returned because it may contain credentials.
func DiscoverTools(ctx context.Context, command CommandConfig) (DiscoveryResult, error) {
	if ctx == nil {
		return DiscoveryResult{}, errors.New("mcp client context is required")
	}
	command, err := prepareCommandConfig(command)
	if err != nil {
		return DiscoveryResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, clientCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Directory
	cmd.Env = append([]string(nil), command.Environment...)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return DiscoveryResult{}, fmt.Errorf("opening mcp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return DiscoveryResult{}, fmt.Errorf("opening mcp stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return DiscoveryResult{}, fmt.Errorf("starting mcp process: %w", err)
	}
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxClientResponseBytes)
	encoder := json.NewEncoder(stdin)
	initialized, err := clientRequest[initializeResult](scanner, encoder, 1, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": implementation{Name: "ahe-mcp", Version: "v1"},
	})
	if err != nil {
		return DiscoveryResult{}, err
	}
	if initialized.ProtocolVersion != ProtocolVersion {
		return DiscoveryResult{}, fmt.Errorf("unsupported mcp protocol version %q", initialized.ProtocolVersion)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return DiscoveryResult{}, fmt.Errorf("writing initialized notification: %w", err)
	}
	result := DiscoveryResult{ServerName: initialized.ServerInfo.Name, ServerVersion: initialized.ServerInfo.Version, Tools: []DiscoveredTool{}}
	seenNames := make(map[string]bool)
	seenCursors := make(map[string]bool)
	params := map[string]any{}
	for page := 0; ; page++ {
		if page >= 100 {
			return DiscoveryResult{}, errors.New("mcp tool discovery exceeded 100 pages")
		}
		listed, err := clientRequest[struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}](scanner, encoder, page+2, "tools/list", params)
		if err != nil {
			return DiscoveryResult{}, err
		}
		for _, tool := range listed.Tools {
			if strings.TrimSpace(tool.Name) == "" || seenNames[tool.Name] || tool.InputSchema == nil {
				return DiscoveryResult{}, fmt.Errorf("%w: missing schema, empty or duplicate tool name", ErrToolContract)
			}
			seenNames[tool.Name] = true
			hash, err := ToolInputSchemaHash(tool.InputSchema)
			if err != nil {
				return DiscoveryResult{}, err
			}
			result.Tools = append(result.Tools, DiscoveredTool{Name: tool.Name, InputSchemaHash: hash, Annotations: tool.Annotations})
		}
		if listed.NextCursor == "" {
			break
		}
		if seenCursors[listed.NextCursor] {
			return DiscoveryResult{}, errors.New("mcp tool discovery repeated a pagination cursor")
		}
		seenCursors[listed.NextCursor] = true
		params = map[string]any{"cursor": listed.NextCursor}
	}
	if err := stdin.Close(); err != nil {
		return DiscoveryResult{}, fmt.Errorf("closing mcp stdin: %w", err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		return DiscoveryResult{}, fmt.Errorf("waiting for mcp process: %w", err)
	}
	return result, nil
}
