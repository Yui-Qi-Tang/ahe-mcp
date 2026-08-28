package mcpadmin

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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
	coreTools := new(evidenceingestionmcp.Server).Tools()
	if len(tools) != len(coreTools) {
		t.Fatalf("admin tools count = %d, core tools count = %d", len(tools), len(coreTools))
	}
	for _, tool := range coreTools {
		if _, ok := names[tool.Name]; !ok {
			t.Fatalf("admin tools do not expose core tool %q", tool.Name)
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
	extractorDefinition := outputProperties["extractor_definition"].(map[string]any)
	extractorProperties := extractorDefinition["properties"].(map[string]any)
	if extractorProperties["name"].(map[string]any)["minLength"] != 1 ||
		extractorProperties["name"].(map[string]any)["maxLength"] != evidenceingestion.ExtractorDefinitionNameMaxBytes ||
		extractorProperties["version"].(map[string]any)["minLength"] != 1 ||
		extractorProperties["version"].(map[string]any)["maxLength"] != evidenceingestion.ExtractorDefinitionVersionMaxBytes {
		t.Fatalf("extractor identity schema = %+v", extractorProperties)
	}
	config := extractorProperties["config"].(map[string]any)
	if config["maxProperties"] != evidenceingestion.ExtractorDefinitionConfigMaxEntries ||
		config["propertyNames"].(map[string]any)["maxLength"] != evidenceingestion.ExtractorDefinitionConfigKeyMaxBytes ||
		config["additionalProperties"].(map[string]any)["maxLength"] != evidenceingestion.ExtractorDefinitionConfigValueMaxBytes {
		t.Fatalf("extractor config schema = %+v", config)
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

func TestAdmissionToolMetadataStatesHumanReviewAndTerminalEffects(t *testing.T) {
	tools := make(map[string]mcpToolMetadata)
	for _, tool := range ingestionTools() {
		tools[tool.Name] = mcpToolMetadata{
			description: tool.Description,
			destructive: tool.Annotations.DestructiveHint,
		}
	}
	coreDescriptions := make(map[string]string)
	for _, tool := range new(evidenceingestionmcp.Server).Tools() {
		coreDescriptions[tool.Name] = tool.Description
	}

	for _, name := range []string{
		evidenceingestionmcp.ToolAdmitPendingProposal,
		evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		evidenceingestionmcp.ToolAdmitPendingCanonicalContradiction,
		evidenceingestionmcp.ToolRecordPendingCanonicalContradictionDisposition,
		evidenceingestionmcp.ToolAdmitPendingSupersession,
	} {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q is missing", name)
		}
		if tool.destructive == nil || !*tool.destructive {
			t.Errorf("tool %q destructiveHint = %v, want true for a terminal lifecycle transition", name, tool.destructive)
		}
		if tool.description != coreDescriptions[name] {
			t.Errorf("tool %q descriptions drifted: backend=%q core=%q", name, tool.description, coreDescriptions[name])
		}
		for _, want := range []string{"explicit human", "immediately", "terminal", "does not prove"} {
			if !strings.Contains(tool.description, want) {
				t.Errorf("tool %q description %q does not contain %q", name, tool.description, want)
			}
		}
	}
}

func TestCanonicalContradictionToolMetadataStatesNodeAndPairContract(t *testing.T) {
	backendTools := make(map[string]string)
	for _, tool := range ingestionTools() {
		backendTools[tool.Name] = tool.Description
	}
	coreTools := make(map[string]string)
	for _, tool := range new(evidenceingestionmcp.Server).Tools() {
		coreTools[tool.Name] = tool.Description
	}

	wants := map[string][]string{
		evidenceingestionmcp.ToolSubmitCanonicalContradictionProposal: {
			"canonical nodes",
			"unordered node pair",
			"single-use",
			"does not create",
			"does not infer or validate",
		},
		evidenceingestionmcp.ToolAdmitPendingCanonicalContradiction: {
			"canonical nodes",
			"explicit human approval",
			"immediately",
			"terminal",
		},
		evidenceingestionmcp.ToolRecordPendingCanonicalContradictionDisposition: {
			"explicit human",
			"single-use",
			"immediately",
			"terminal",
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

func TestCanonicalSupersessionToolMetadataStatesFreshAdmissionContract(t *testing.T) {
	backendTools := make(map[string]mcpToolMetadata)
	for _, tool := range ingestionTools() {
		backendTools[tool.Name] = mcpToolMetadata{
			description: tool.Description,
			destructive: tool.Annotations.DestructiveHint,
		}
	}
	coreTools := make(map[string]string)
	for _, tool := range new(evidenceingestionmcp.Server).Tools() {
		coreTools[tool.Name] = tool.Description
	}

	name := evidenceingestionmcp.ToolAdmitPendingSupersession
	tool, ok := backendTools[name]
	if !ok {
		t.Fatalf("backend tool %q is missing", name)
	}
	if tool.description != coreTools[name] {
		t.Errorf("tool %q descriptions drifted: backend=%q core=%q", name, tool.description, coreTools[name])
	}
	if tool.destructive == nil || !*tool.destructive {
		t.Errorf("tool %q destructiveHint = %v, want terminal mutation", name, tool.destructive)
	}
	for _, fragment := range []string{
		"fresh pending proposal",
		"exact source quotes",
		"source title and location",
		"provider revision",
		"complete set",
		"coverage/limitations",
		"reviewed six-field",
		"head coordinate (revision and event ID)",
		"explicit human approval",
		"immediately and terminally",
		"new-to-old",
		"first four basis fields",
		"slot_kind and slot_id",
		"does not prove",
		"record_pending_proposal_disposition",
	} {
		if !strings.Contains(tool.description, fragment) {
			t.Errorf("tool %q description %q does not contain %q", name, tool.description, fragment)
		}
	}

	var admissionSchema map[string]any
	for _, tool := range ingestionTools() {
		if tool.Name == name {
			admissionSchema = tool.InputSchema
			break
		}
	}
	if admissionSchema == nil {
		t.Fatal("supersession admission schema is missing")
	}
	properties := admissionSchema["properties"].(map[string]any)
	for _, forbidden := range []string{"lineage_key", "complete", "current", "winner", "status", "revision"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("supersession schema exposes caller authority field %q", forbidden)
		}
	}
	targets := properties["target_node_ids"].(map[string]any)
	if targets["minItems"] != 1 || targets["maxItems"] != 64 || targets["uniqueItems"] != true {
		t.Fatalf("supersession target schema = %+v", targets)
	}
	basis := properties["basis"].(map[string]any)
	wantBasis := []string{"source_system", "source_namespace", "object_type", "object_id", "slot_kind", "slot_id"}
	if !reflect.DeepEqual(basis["required"], wantBasis) || basis["additionalProperties"] != false {
		t.Fatalf("supersession basis schema = %+v", basis)
	}
	coordinateBranches, ok := admissionSchema["oneOf"].([]map[string]any)
	if !ok || len(coordinateBranches) != 2 {
		t.Fatalf("supersession coordinate oneOf = %#v, want zero and positive revision branches", admissionSchema["oneOf"])
	}
	zeroProperties := coordinateBranches[0]["properties"].(map[string]any)
	zeroRevision := zeroProperties["expected_revision"].(map[string]any)
	zeroHead := zeroProperties["expected_head_event_id"].(map[string]any)
	if zeroRevision["const"] != int64(0) || zeroHead["maxLength"] != int64(0) {
		t.Fatalf("zero-head coordinate schema = %#v", coordinateBranches[0])
	}
	positiveProperties := coordinateBranches[1]["properties"].(map[string]any)
	positiveRevision := positiveProperties["expected_revision"].(map[string]any)
	positiveHead := positiveProperties["expected_head_event_id"].(map[string]any)
	if positiveRevision["minimum"] != int64(1) || positiveHead["minLength"] != int64(1) ||
		!reflect.DeepEqual(coordinateBranches[1]["required"], []string{"expected_head_event_id"}) {
		t.Fatalf("positive-head coordinate schema = %#v", coordinateBranches[1])
	}
}

type mcpToolMetadata struct {
	description string
	destructive *bool
}
