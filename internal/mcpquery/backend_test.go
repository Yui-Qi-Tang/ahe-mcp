package mcpquery

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
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
		evidencequerymcp.ToolOpenCanonicalReadView,
		evidencequerymcp.ToolFindCanonicalPath,
		evidencequerymcp.ToolGetCanonicalTopologyDiagnostics,
		evidencequerymcp.ToolGetCanonicalContradictionProposal,
		evidencequerymcp.ToolGetCanonicalSupersessionHead,
		evidencequerymcp.ToolGetCanonicalSupersessionCurrentness,
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

func TestCanonicalTopologyToolMetadataStatesBoundedStructuralSemantics(t *testing.T) {
	backendTools := make(map[string]string)
	for _, tool := range queryTools() {
		backendTools[tool.Name] = tool.Description
	}
	coreTools := make(map[string]string)
	for _, tool := range new(evidencequerymcp.Server).Tools() {
		coreTools[tool.Name] = tool.Description
	}

	wants := map[string][]string{
		evidencequerymcp.ToolOpenCanonicalReadView: {
			"bounded",
			"instance-local",
			"evicted",
		},
		evidencequerymcp.ToolFindCanonicalPath: {
			"found_in_view=false",
			"not global absence",
			"contradicts",
			"both directions",
			"stored direction",
		},
		evidencequerymcp.ToolGetCanonicalTopologyDiagnostics: {
			"connected components",
			"does not imply",
		},
		evidencequerymcp.ToolGetCanonicalContradictionProposal: {
			"canonical nodes",
			"pending or terminal",
		},
		evidencequerymcp.ToolGetCanonicalSupersessionHead: {
			"compare-and-swap coordinate",
			"subsequent governed write",
			"does not identify a current node",
			"prove source freshness",
		},
		evidencequerymcp.ToolGetCanonicalSupersessionCurrentness: {
			"snapshot-bound",
			"current, superseded, ambiguous, or unknown",
			"authoritative repeatable-read cut",
			"callers supply only lineage_key",
		},
	}
	for name, fragments := range wants {
		description, ok := backendTools[name]
		if !ok {
			t.Fatalf("backend tool %q is missing", name)
		}
		if description != coreTools[name] {
			t.Errorf("tool %q descriptions drifted: backend=%q core=%q", name, description, coreTools[name])
		}
		for _, fragment := range fragments {
			if !strings.Contains(description, fragment) {
				t.Errorf("tool %q description %q does not contain %q", name, description, fragment)
			}
		}
	}
}

func TestCanonicalTopologyToolSchemasExposeBoundedContracts(t *testing.T) {
	toolsByName := make(map[string]map[string]any)
	for _, tool := range queryTools() {
		toolsByName[tool.Name] = tool.InputSchema
	}

	assertRequiredFields(t, toolsByName[evidencequerymcp.ToolOpenCanonicalReadView],
		"root_node_ids", "max_depth", "max_nodes", "max_edges",
	)
	assertRequiredFields(t, toolsByName[evidencequerymcp.ToolFindCanonicalPath],
		"handle", "from_node_id", "to_node_id", "relations",
	)
	assertRequiredFields(t, toolsByName[evidencequerymcp.ToolGetCanonicalTopologyDiagnostics], "handle")

	headSchema := toolsByName[evidencequerymcp.ToolGetCanonicalSupersessionHead]
	if headSchema["additionalProperties"] != false {
		t.Fatalf("supersession head additionalProperties = %#v, want false", headSchema["additionalProperties"])
	}
	if _, ok := headSchema["properties"]; ok {
		t.Fatalf("supersession head schema accepts caller properties: %#v", headSchema["properties"])
	}
	if _, ok := headSchema["required"]; ok {
		t.Fatalf("supersession head schema requires caller fields: %#v", headSchema["required"])
	}

	currentnessSchema := toolsByName[evidencequerymcp.ToolGetCanonicalSupersessionCurrentness]
	assertRequiredFields(t, currentnessSchema, "lineage_key")
	currentnessProperties := schemaProperties(t, currentnessSchema)
	if len(currentnessProperties) != 1 {
		t.Fatalf("supersession currentness properties = %#v, want only lineage_key", currentnessProperties)
	}
	if _, ok := currentnessProperties["lineage_key"]; !ok {
		t.Fatalf("supersession currentness schema lacks lineage_key: %#v", currentnessProperties)
	}
	for _, forbidden := range []string{"complete", "current", "members", "winner", "status", "head", "hash"} {
		if _, ok := currentnessProperties[forbidden]; ok {
			t.Errorf("supersession currentness schema exposes caller authority field %q", forbidden)
		}
	}

	openProperties := schemaProperties(t, toolsByName[evidencequerymcp.ToolOpenCanonicalReadView])
	assertIntegerBounds(t, openProperties, "max_depth", 0, 8)
	assertIntegerBounds(t, openProperties, "max_nodes", 1, 1024)
	assertIntegerBounds(t, openProperties, "max_edges", 0, 4096)

	wantRelations := make([]string, 0, len(evidencegraph.CanonicalRelations()))
	for _, relation := range evidencegraph.CanonicalRelations() {
		wantRelations = append(wantRelations, string(relation))
	}
	for _, toolName := range []string{
		evidencequerymcp.ToolOpenCanonicalReadView,
		evidencequerymcp.ToolFindCanonicalPath,
	} {
		properties := schemaProperties(t, toolsByName[toolName])
		relations, ok := properties["relations"].(map[string]any)
		if !ok {
			t.Fatalf("%s relations schema = %#v", toolName, properties["relations"])
		}
		items, ok := relations["items"].(map[string]any)
		if !ok || !reflect.DeepEqual(items["enum"], wantRelations) {
			t.Fatalf("%s relation enum = %#v, want %v", toolName, items["enum"], wantRelations)
		}
	}
}

func assertRequiredFields(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	if schema == nil {
		t.Fatal("tool schema is missing")
	}
	got, ok := schema["required"].([]string)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("required fields = %#v, want %v", schema["required"], want)
	}
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	return properties
}

func assertIntegerBounds(t *testing.T, properties map[string]any, name string, minimum, maximum int64) {
	t.Helper()
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("%s schema = %#v", name, properties[name])
	}
	if property["type"] != "integer" || property["minimum"] != minimum || property["maximum"] != maximum {
		t.Fatalf("%s bounds = %#v, want %d..%d", name, property, minimum, maximum)
	}
}
