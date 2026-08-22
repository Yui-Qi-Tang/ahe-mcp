package mcpadmin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
)

func TestBackendExposesAdminToolsAndDelegatesCalls(t *testing.T) {
	tools := ingestionTools()
	if len(tools) == 0 {
		t.Fatal("ingestionTools() returned no tools")
	}

	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if _, exists := names[tool.Name]; exists {
			t.Fatalf("duplicate admin tool %q", tool.Name)
		}
		names[tool.Name] = struct{}{}
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("admin tool %q schema type = %v, want object", tool.Name, tool.InputSchema["type"])
		}
		if tool.Annotations.ReadOnlyHint == nil ||
			tool.Annotations.DestructiveHint == nil ||
			tool.Annotations.IdempotentHint == nil {
			t.Fatalf("admin tool %q has incomplete annotations", tool.Name)
		}
	}
	if _, ok := names[evidenceingestionmcp.ToolSubmitTextSource]; !ok {
		t.Fatalf("admin tools do not contain %q", evidenceingestionmcp.ToolSubmitTextSource)
	}
	externalTool := tools[len(tools)-1]
	if externalTool.Name != evidenceingestionmcp.ToolSubmitExternalSource {
		t.Fatalf("last admin tool = %q, want %q", externalTool.Name, evidenceingestionmcp.ToolSubmitExternalSource)
	}
	externalProperties := externalTool.InputSchema["properties"].(map[string]any)
	if got := externalProperties["content_fidelity"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []string{evidenceingestion.ExternalSourceContentFidelityVerbatim}) {
		t.Fatalf("external content fidelity enum = %#v", got)
	}
	if got := externalProperties["coverage"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []string{
		evidenceingestion.ExternalSourceCoverageFullDocument,
		evidenceingestion.ExternalSourceCoverageExactExcerpt,
		evidenceingestion.ExternalSourceCoverageTruncatedDocument,
	}) {
		t.Fatalf("external coverage enum = %#v", got)
	}

	outputProperties := tools[2].InputSchema["properties"].(map[string]any)
	producerSessionRef := outputProperties["producer_session_ref"].(map[string]any)
	if producerSessionRef["maxLength"] != evidenceingestion.ProducerSessionRefMaxBytes {
		t.Fatalf("producer session ref schema = %+v", producerSessionRef)
	}
	for _, queryTool := range []string{
		"get_evidence_record",
		"get_grounded_evidence_brief",
		"get_mcp_read_source_states",
		"get_mcp_read_source_transition",
	} {
		if _, ok := names[queryTool]; ok {
			t.Fatalf("admin backend exposes query tool %q", queryTool)
		}
	}

	var calledName string
	backend := &Backend{
		tools: tools,
		call: func(_ context.Context, name string, arguments []byte) ([]byte, error) {
			calledName = name
			return arguments, nil
		},
	}
	listed := backend.Tools()
	listed[0].Name = "mutated"
	if backend.Tools()[0].Name == "mutated" {
		t.Fatal("Tools() exposed the backend slice")
	}

	payload := json.RawMessage(`{"request_id":"request-1"}`)
	got, err := backend.CallTool(context.Background(), evidenceingestionmcp.ToolSubmitTextSource, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if calledName != evidenceingestionmcp.ToolSubmitTextSource || string(got) != string(payload) {
		t.Fatalf("CallTool() = name %q payload %s", calledName, got)
	}
}
