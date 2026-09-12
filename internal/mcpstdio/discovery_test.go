package mcpstdio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscoverToolsDoesNotCallTool(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverTools(context.Background(), CommandConfig{
		Path: executable, Args: []string{"-test.run=TestStdioClientHelperProcess"}, Directory: t.TempDir(), Environment: []string{"AHE_MCP_CLIENT_HELPER=discovery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServerName != "loopback-mcp" || len(result.Tools) != 1 || result.Tools[0].Name != "read_document" || result.Tools[0].InputSchemaHash != clientHelperSchemaHash(t) {
		t.Fatalf("unexpected discovery: %+v", result)
	}
}

func TestDiscoverToolsCancelsUnresponsiveProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = DiscoverTools(ctx, CommandConfig{Path: executable, Args: []string{"-test.run=TestDiscoveryUnresponsiveHelper"}, Directory: t.TempDir(), Environment: []string{"AHE_DISCOVERY_HANG=1"}})
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestDiscoveryUnresponsiveHelper(t *testing.T) {
	if os.Getenv("AHE_DISCOVERY_HANG") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestDiscoveryPaginationAndInvalidContracts(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pages", "duplicate", "cursor", "schema", "protocol"} {
		t.Run(mode, func(t *testing.T) {
			result, err := DiscoverTools(context.Background(), CommandConfig{Path: executable, Args: []string{"-test.run=TestDiscoveryPaginationHelper"}, Directory: t.TempDir(), Environment: []string{"AHE_DISCOVERY_PAGES=" + mode}})
			if mode == "pages" {
				if err != nil || len(result.Tools) != 2 {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

func TestDiscoveryPaginationHelper(t *testing.T) {
	mode := os.Getenv("AHE_DISCOVERY_PAGES")
	if mode == "" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	page := 0
	initialized := false
	for {
		var request struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			if err == io.EOF {
				os.Exit(0)
			}
			os.Exit(90)
		}
		var result any
		switch request.Method {
		case "initialize":
			protocol := ProtocolVersion
			if mode == "protocol" {
				protocol = "unsupported"
			}
			result = map[string]any{"protocolVersion": protocol, "serverInfo": map[string]any{"name": "paged", "version": "v1"}}
		case "notifications/initialized":
			initialized = true
			continue
		case "tools/list":
			if !initialized {
				os.Exit(91)
			}
			name := "first"
			if page > 0 {
				if request.Params["cursor"] != "next" {
					os.Exit(92)
				}
				if mode != "duplicate" {
					name = "second"
				}
			}
			tool := map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}}
			if mode == "schema" {
				delete(tool, "inputSchema")
			}
			data := map[string]any{"tools": []any{tool}}
			if page == 0 || mode == "cursor" {
				data["nextCursor"] = "next"
			}
			result = data
			page++
		default:
			os.Exit(93)
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(94)
		}
	}
}

func TestDiscoveredPinStillRejectsSchemaDriftBeforeCall(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := CommandConfig{Path: executable, Args: []string{"-test.run=TestStdioClientHelperProcess"}, Directory: t.TempDir(), Environment: []string{"AHE_MCP_CLIENT_HELPER=discovery"}}
	result, err := DiscoverTools(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	pin := result.Tools[0].InputSchemaHash
	replacement := "0"
	if strings.HasSuffix(pin, "0") {
		replacement = "1"
	}
	pin = pin[:len(pin)-1] + replacement
	_, err = CallTool(context.Background(), command, ToolCallRequest{Name: result.Tools[0].Name, ExpectedInputSchemaHash: pin})
	if !errors.Is(err, ErrToolContract) {
		t.Fatalf("expected schema contract rejection, got %v", err)
	}
}
