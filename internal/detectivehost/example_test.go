package detectivehost

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/atlassianmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func TestMCPExtractionExample(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("MCP read host requires macOS")
	}
	config, err := LoadConfig("../../configs/detective.mcp-extraction.example.json")
	if err != nil {
		t.Fatal(err)
	}
	source := config.MCPReadSources[0]
	hash, err := mcpstdio.ToolInputSchemaHash(atlassianmcp.ReadDocumentInputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if source.ProviderToolName != atlassianmcp.ToolReadDocument || source.ProviderToolInputSchemaHash != hash || source.AdapterVersion != atlassianmcp.AdapterVersion || source.ProposalConversion {
		t.Fatalf("example does not match adapter: %+v", source)
	}
	extractor, err := newMCPProposalExtractor(source.ProposalExtraction)
	if err != nil || extractor == nil {
		t.Fatalf("extractor = %v, error = %v", extractor, err)
	}
	raw, err := os.ReadFile("../../configs/atlassian-adapter.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var adapter atlassianmcp.Config
	if err := json.Unmarshal(raw, &adapter); err != nil {
		t.Fatal(err)
	}
	if _, err := atlassianmcp.NewBackend(adapter); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../configs/detective.mcp-command.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var command mcpstdio.CommandConfig
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	wrapped, err := mcpstdio.MacOSLoopbackOnlyCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(wrapped)
	b, _ := json.Marshal(source.Command)
	if string(a) != string(b) {
		t.Fatal("discovery and host commands differ")
	}
}
