// Package mcpquery exposes read-only AHE query tools over MCP.
package mcpquery

import (
	"context"
	"encoding/json"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

// Backend adapts existing MCP-shaped packages to the generic stdio transport.
type Backend struct {
	tools []mcpstdio.Tool
	call  func(context.Context, string, []byte) ([]byte, error)
}

// NewBackend exposes the external read-only evidence query tools.
func NewBackend(server *evidencequerymcp.Server) *Backend {
	return &Backend{
		tools: queryTools(),
		call:  server.CallTool,
	}
}

// Tools returns the deterministic MCP tool list.
func (b *Backend) Tools() []mcpstdio.Tool {
	return append([]mcpstdio.Tool(nil), b.tools...)
}

// CallTool forwards one JSON tool payload to the underlying evidence adapter.
func (b *Backend) CallTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	data, err := b.call(ctx, name, []byte(arguments))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func queryTools() []mcpstdio.Tool {
	readOnly := true
	destructive := false
	idempotent := true
	return []mcpstdio.Tool{
		{
			Name:        evidencequerymcp.ToolGetEvidenceRecord,
			Title:       "Get Evidence Record",
			Description: "Read one proposal or admitted canonical evidence record by exact ID.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"proposal_occurrence_id": stringSchema("Proposal occurrence ID with occ: prefix."),
					"canonical_id":           stringSchema("Canonical evidence node ID with canon-node: prefix."),
				},
				"oneOf": []map[string]any{
					{"required": []string{"proposal_occurrence_id"}},
					{"required": []string{"canonical_id"}},
				},
			},
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidencequerymcp.ToolListEvidenceRecords,
			Title:       "List Evidence Records",
			Description: "List bounded proposal evidence records by exact source and lifecycle filters.",
			InputSchema: listEvidenceRecordsSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidencequerymcp.ToolSearchEvidenceRecords,
			Title:       "Search Evidence Records",
			Description: "Run one exact bounded lexical lookup over persisted proposal statements and return complete grounded records.",
			InputSchema: searchEvidenceRecordsSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidencequerymcp.ToolGetGroundedEvidenceBrief,
			Title:       "Get Grounded Evidence Brief",
			Description: "Compile one information need through bounded deterministic lexical recovery and return a self-contained evidence-only brief with authoritative query provenance.",
			InputSchema: groundedEvidenceBriefSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidencequerymcp.ToolListEvidenceNeighbors,
			Title:       "List Evidence Neighbors",
			Description: "List one bounded hop around an exact canonical node or repository symbol.",
			InputSchema: listEvidenceNeighborsSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidencequerymcp.ToolGetRelationProvenance,
			Title:       "Get Relation Provenance",
			Description: "Read one exact repository typed relation or canonical edge with provenance.",
			InputSchema: getRelationProvenanceSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidencequerymcp.ToolGetMCPReadSourceStates,
			Title:       "Get MCP Read Source States",
			Description: "Read latest-observed, exact-revision, history, or explicit compare metadata for one MCP source binding without joining evidence across revisions.",
			InputSchema: getMCPReadSourceStatesSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
	}
}

func getMCPReadSourceStatesSchema() map[string]any {
	return objectSchema(map[string]any{
		"source_binding_id": stringSchema("Exact MCP source binding ID with workspace-source: prefix."),
		"mode": enumStringSchema(
			"Required source-state selection mode.",
			evidenceingestion.MCPReadSourceStateModeLatestObserved,
			evidenceingestion.MCPReadSourceStateModeExactRevision,
			evidenceingestion.MCPReadSourceStateModeHistory,
			evidenceingestion.MCPReadSourceStateModeCompare,
		),
		"revision":      stringSchema("Exact provider-owned revision, allowed only in exact_revision mode."),
		"from_revision": stringSchema("Exact earlier provider-owned revision, allowed only in compare mode."),
		"to_revision":   stringSchema("Exact later provider-owned revision, allowed only in compare mode."),
		"limit": boundedIntegerSchema(
			"Maximum distinct immutable states returned by history mode.",
			1,
			evidenceingestion.MCPReadSourceStateMaxLimit,
		),
	}, []string{"source_binding_id", "mode"})
}

func listEvidenceRecordsSchema() map[string]any {
	schema := objectSchema(map[string]any{
		"source_snapshot_id":     stringSchema("Optional source snapshot ID with srcsnap: prefix."),
		"repository_snapshot_id": stringSchema("Optional repository snapshot ID with repo-snapshot: prefix."),
		"source_generation_id":   stringSchema("Optional exact repository source generation ID with generation: prefix."),
		"source_id":              stringSchema("Optional exact source ID for source-snapshot records."),
		"source_version":         stringSchema("Optional exact source version for source-snapshot records."),
		"admission_outcome":      stringSchema("Optional lifecycle filter: pending, rejected, audit_only, or admitted."),
		"lifecycle_scope":        enumStringSchema("Optional repository lifecycle scope. Defaults to active, or all with an exact source generation.", "active", "historical", "all"),
		"limit":                  integerSchema("Optional result limit. Defaults to 20; maximum 100."),
	}, nil)
	schema["not"] = map[string]any{
		"required": []string{"source_snapshot_id", "repository_snapshot_id"},
	}
	return schema
}

func searchEvidenceRecordsSchema() map[string]any {
	properties := listEvidenceRecordsSchema()["properties"].(map[string]any)
	properties["query"] = stringSchema("Required exact lexical query over persisted proposal statement text; at most 256 characters and 16 terms.")
	schema := objectSchema(properties, []string{"query"})
	schema["not"] = map[string]any{"required": []string{"source_snapshot_id", "repository_snapshot_id"}}
	return schema
}

func groundedEvidenceBriefSchema() map[string]any {
	properties := listEvidenceRecordsSchema()["properties"].(map[string]any)
	properties["query"] = stringSchema("Required information need; Query Core applies the selected deterministic lexical plan. At most 256 characters and 16 terms.")
	properties["query_mode"] = enumStringSchema(
		"Optional query plan. Defaults to deterministic_lexical_recovery; exact_lexical preserves the low-level all-term mode.",
		evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
		evidenceingestion.EvidenceQueryModeExactLexical,
	)
	properties["response_schema"] = enumStringSchema(
		"Optional response contract. Defaults to grounded-evidence-brief-v2; v3 adds bounded post-retrieval manual source context; v4 adds typed repository context while retaining manual source context; v5 adds compact per-match authority, lifecycle, and source revision without hydrated context.",
		evidencequerymcp.GroundedEvidenceBriefSchemaV2,
		evidencequerymcp.GroundedEvidenceBriefSchemaV3,
		evidencequerymcp.GroundedEvidenceBriefSchemaV4,
		evidencequerymcp.GroundedEvidenceBriefSchemaV5,
	)
	schema := objectSchema(properties, []string{"query"})
	schema["not"] = map[string]any{"required": []string{"source_snapshot_id", "repository_snapshot_id"}}
	return schema
}

func listEvidenceNeighborsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"canonical_id":         stringSchema("Canonical evidence node ID with canon-node: prefix."),
			"symbol_ref":           stringSchema("Exact repository symbol identity with symbol: prefix."),
			"direction":            enumStringSchema("Optional relation direction. Defaults to both.", "incoming", "outgoing", "both"),
			"relation":             stringSchema("Optional exact canonical relation or repository relation kind."),
			"lifecycle_scope":      enumStringSchema("Optional repository lifecycle scope.", "active", "historical", "all"),
			"source_generation_id": stringSchema("Optional exact repository source generation ID with generation: prefix."),
			"limit":                integerSchema("Optional result limit. Defaults to 20; maximum 100."),
		},
		"oneOf": []map[string]any{
			{"required": []string{"canonical_id"}},
			{"required": []string{"symbol_ref"}},
		},
	}
}

func getRelationProvenanceSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"proposal_occurrence_id": stringSchema("Typed repository relation proposal ID with occ: prefix."),
			"canonical_edge_id":      stringSchema("Canonical relation ID with canon-edge: prefix."),
		},
		"oneOf": []map[string]any{
			{"required": []string{"proposal_occurrence_id"}},
			{"required": []string{"canonical_edge_id"}},
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func enumStringSchema(description string, values ...string) map[string]any {
	schema := stringSchema(description)
	schema["enum"] = append([]string(nil), values...)
	return schema
}

func integerSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

func boundedIntegerSchema(description string, minimum, maximum int64) map[string]any {
	schema := integerSchema(description)
	schema["minimum"] = minimum
	schema["maximum"] = maximum
	return schema
}
