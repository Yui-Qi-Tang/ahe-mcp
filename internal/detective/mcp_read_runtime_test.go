package detective

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func TestPrepareMCPReadSourcePinsLoopbackInvocation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS loopback-only command is required")
	}
	command, err := mcpstdio.MacOSLoopbackOnlyCommand(mcpstdio.CommandConfig{
		Path:      "/fixture/ahe-mcp-adapter",
		Args:      []string{"serve"},
		Directory: "/fixture",
	})
	if err != nil {
		t.Fatalf("MacOSLoopbackOnlyCommand() error = %v", err)
	}
	sourceBindingID := "workspace-source:" + strings.Repeat("a", 64)
	config := MCPReadSourceConfig{
		ConnectorID:                 " atlassian-read ",
		Provider:                    " atlassian ",
		LogicalCapability:           " read_document ",
		AdapterName:                 " ahe-mcp-atlassian-adapter ",
		AdapterVersion:              " v1 ",
		ProviderToolName:            " read_atlassian_document ",
		ProviderToolInputSchemaHash: "sha256:" + strings.Repeat("b", 64),
		Arguments:                   json.RawMessage(`{"object_id":"AHE-42","product":"jira"}`),
		Command:                     command,
	}
	first, err := prepareMCPReadSource(" workspace:mcp ", " "+sourceBindingID+" ", config)
	if err != nil {
		t.Fatalf("prepareMCPReadSource() error = %v", err)
	}
	second, err := prepareMCPReadSource("workspace:mcp", sourceBindingID, config)
	if err != nil {
		t.Fatalf("second prepareMCPReadSource() error = %v", err)
	}
	if first.workspaceID != "workspace:mcp" ||
		first.sourceBindingID != sourceBindingID ||
		first.config.ConnectorID != "atlassian-read" ||
		first.bindingHash != second.bindingHash ||
		first.commandHash != second.commandHash {
		t.Fatalf("prepared MCP read source = %+v / %+v", first, second)
	}
	cycleID := mcpReadCollectionCycleID(first.workspaceID, first.sourceBindingID, 1, first.bindingHash)
	requestID := mcpReadCollectionRequestID(first.workspaceID, first.sourceBindingID, 1, first.bindingHash)
	if !strings.HasPrefix(cycleID, "detective-mcp-read-cycle:") ||
		!strings.HasPrefix(requestID, "detective-mcp-read-collect:") ||
		cycleID == mcpReadCollectionCycleID(first.workspaceID, first.sourceBindingID, 2, first.bindingHash) ||
		requestID == mcpReadCollectionRequestID(first.workspaceID, first.sourceBindingID, 2, first.bindingHash) {
		t.Fatalf("MCP read cycle identities = %q / %q", cycleID, requestID)
	}
}

func TestPrepareMCPReadSourceRejectsUnconfinedCommand(t *testing.T) {
	_, err := prepareMCPReadSource(
		"workspace:mcp",
		"workspace-source:"+strings.Repeat("a", 64),
		MCPReadSourceConfig{
			ConnectorID:                 "atlassian-read",
			Provider:                    "atlassian",
			LogicalCapability:           "read_document",
			AdapterName:                 "adapter",
			AdapterVersion:              "v1",
			ProviderToolName:            "read_atlassian_document",
			ProviderToolInputSchemaHash: "sha256:" + strings.Repeat("b", 64),
			Arguments:                   json.RawMessage(`{"object_id":"AHE-42"}`),
			Command: mcpstdio.CommandConfig{
				Path:      "/fixture/adapter",
				Directory: "/fixture",
			},
		},
	)
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestMCPReadRuntimeRequiresPostgresPool(t *testing.T) {
	_, _, err := RegisterMCPReadSourceBinding(t.Context(), nil, MCPReadSourceBindingInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
	_, err = RunMCPReadSourceTick(t.Context(), nil, MCPReadSourceTickInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}
