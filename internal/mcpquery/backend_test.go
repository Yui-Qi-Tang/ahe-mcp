package mcpquery

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
)

func TestBackendExposesOnlyShippingQueryToolsAndDelegatesCalls(t *testing.T) {
	tools := queryTools()
	gotNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		gotNames = append(gotNames, tool.Name)
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("query tool %q schema type = %v, want object", tool.Name, tool.InputSchema["type"])
		}
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Fatalf("query tool %q is not annotated read-only", tool.Name)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("query tool %q is not annotated non-destructive", tool.Name)
		}
		if tool.Annotations.IdempotentHint == nil || !*tool.Annotations.IdempotentHint {
			t.Fatalf("query tool %q is not annotated idempotent", tool.Name)
		}
	}
	wantNames := []string{
		evidencequerymcp.ToolGetEvidenceRecord,
		evidencequerymcp.ToolListEvidenceRecords,
		evidencequerymcp.ToolSearchEvidenceRecords,
		evidencequerymcp.ToolGetGroundedEvidenceBrief,
		evidencequerymcp.ToolListEvidenceNeighbors,
		evidencequerymcp.ToolGetRelationProvenance,
		evidencequerymcp.ToolGetMCPReadSourceStates,
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("query tools = %v, want %v", gotNames, wantNames)
	}
	for _, forbidden := range []string{
		"get_mcp_read_source_transition",
		"submit_text_source",
		"admit_pending_proposal",
	} {
		for _, name := range gotNames {
			if name == forbidden {
				t.Fatalf("query backend exposes non-shipping tool %q", forbidden)
			}
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

	payload := json.RawMessage(`{"canonical_id":"canon-node:1"}`)
	got, err := backend.CallTool(context.Background(), evidencequerymcp.ToolGetEvidenceRecord, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if calledName != evidencequerymcp.ToolGetEvidenceRecord || string(got) != string(payload) {
		t.Fatalf("CallTool() = name %q payload %s", calledName, got)
	}
}
