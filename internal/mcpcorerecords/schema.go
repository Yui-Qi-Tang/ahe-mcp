package mcpcorerecords

import (
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func object(fields map[string]any, optional ...string) map[string]any {
	required := []string{}
	for k := range fields {
		if !slices.Contains(optional, k) {
			required = append(required, k)
		}
	}
	slices.Sort(required)
	return map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}
}
func text(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
func integer(min, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max}
}
func array(items any, max int) map[string]any {
	return map[string]any{"type": "array", "items": items, "maxItems": max}
}
func keySchema() map[string]any {
	return object(map[string]any{"namespace": text(512), "local_id": text(512), "scope_ref": text(512), "revision": text(512)})
}
func subjectSchema() map[string]any {
	p := map[string]any{}
	for _, k := range []string{"proposal_occurrence_id", "source_snapshot_id", "extraction_view_id", "source_version", "raw_content_hash", "rendered_content_hash", "proposal_fingerprint"} {
		p[k] = text(1024)
	}
	return object(p)
}
func tool(name, desc string, schema map[string]any, read bool) mcpstdio.Tool {
	no, yes := false, true
	return mcpstdio.Tool{Name: name, Description: desc, InputSchema: schema, Annotations: mcpstdio.Annotations{ReadOnlyHint: &read, DestructiveHint: &no, IdempotentHint: &yes}}
}

// ReadTools returns the closed read-only Core record surface.
func ReadTools() []mcpstdio.Tool {
	return []mcpstdio.Tool{
		tool("get_proposition_members", "Read current externally assigned proposition members and bounded original graph from one snapshot. Does not establish equivalence or merge derivations. Graph neighbours are not additional members; inspect truncation.", object(map[string]any{"key": keySchema(), "limit": integer(1, 32)}), true),
		tool("get_proposition_binding_history", "Read an explicit binding revision (-1=current, 0=initial). A historical receipt is not current validity. This is not whole-graph time travel; original evidence and parents never change with a binding correction.", object(map[string]any{"node_id": text(512), "revision": integer(-1, 9007199254740991), "limit": integer(1, 100)}), true),
		tool("get_external_check_subject", "Read immutable subject coordinates of a successful source-backed text proposal. No approval or freshness assertion.", object(map[string]any{"proposal_occurrence_id": text(1024)}), true),
		tool("get_external_check", "Read one historical external report, including unchecked dimensions and limitations. No aggregate truth verdict or proof the checker ran.", object(map[string]any{"check_id": text(128)}), true),
		tool("list_external_checks", "List bounded historical reports for one proposal. No latest winner is selected; inspect truncation.", object(map[string]any{"proposal_occurrence_id": text(1024), "limit": integer(1, 100)}), true),
		tool("get_external_representation", "Read one exact external JSON declaration and its declared missing or supplied dependencies. Supplied means linked bytes, not sufficient evidence.", object(map[string]any{"representation_id": text(128)}), true),
		tool("get_external_representation_material", "Return the complete stored declaration as checker material. Use this exact content in an external check; it includes dependency states and versions, not only the condition tree.", object(map[string]any{"representation_id": text(128), "input_id": text(128)}), true),
		tool("get_external_dependency_users", "Read registered representations using one exact four-field dependency identity and explicitly linked checker inputs. Links cover only returned representations; inspect both truncation flags. No automatic invalidation or recomputation. Bounded output does not bound total scan cost.", object(map[string]any{"key": keySchema(), "limit": integer(1, 100)}), true),
	}
}

// Tools returns separately authorized record writes; none admit evidence.
func (b *Backend) Tools() []mcpstdio.Tool {
	if b == nil {
		return nil
	}
	material := object(map[string]any{"id": text(128), "role": text(16), "format": text(256), "version": text(256), "content": text(262144), "mapping_claim": text(8192)})
	finding := object(map[string]any{"dimension": text(256), "criterion": text(8192), "input_ids": array(text(128), 32), "result": map[string]any{"type": "string", "enum": []string{"pass", "fail", "inconclusive", "not_checked", "unsupported"}}, "detail": text(8192)})
	dep := object(map[string]any{"key": keySchema(), "status": map[string]any{"type": "string", "enum": []string{"missing", "supplied"}}, "pointers": array(text(2048), 32), "reason": text(8192), "source_snapshot_id": text(1024), "extraction_view_id": text(1024), "rendered_content_hash": text(1024)}, "source_snapshot_id", "extraction_view_id", "rendered_content_hash")
	return []mcpstdio.Tool{
		tool("bind_canonical_proposition", "Record an explicitly authorized external identity decision for an admitted source or derived claim. Show the original claim, four-field identity, definition and reason before requesting a decision. Launcher binds the recorder; no semantic equivalence is proved. Exact retry returns historical initial receipt, never current validity.", object(map[string]any{"request_id": text(512), "key": keySchema(), "definition": text(8192), "node_id": text(512), "decision_reason": text(2048)}), false),
		tool("change_proposition_binding", "Record an explicitly authorized correction, withdrawal or restoration using the exact prior revision/reference/from_id read from history. Do not infer approval. Withdrawal requires empty target/definition; restoration uses only the last withdrawn target. Stale concurrent changes fail. Original evidence and parents remain immutable.", object(map[string]any{"request_id": text(512), "node_id": text(512), "expected_revision": integer(0, 9007199254740991), "previous_ref": text(1024), "from_id": text(128), "operation": map[string]any{"type": "string", "enum": []string{"correct", "withdraw", "restore"}}, "target": keySchema(), "definition": text(8192), "reason": text(2048), "evidence_ref": text(2048)}, "target", "definition"), false),
		tool("record_external_check", "Append an externally supplied report; contract=external-check/v1. Launcher records recorder identity separately from claimed checker. Verbatim materials must equal stored source/candidate bytes. Explicitly retain not_checked, unsupported and limitations. No admission, aggregate pass, currentness, or automatic invalidation.", object(map[string]any{"contract": text(128), "request_id": text(512), "subject": subjectSchema(), "checker_name": text(1024), "checker_version": text(1024), "checker_configuration": text(8192), "run_ref": text(1024), "materials": array(material, 32), "findings": array(finding, 128), "limitations": array(text(8192), 32), "revises_id": text(128), "revision_reason": text(8192)}, "revises_id", "revision_reason"), false),
		tool("record_external_representation", "Append externally authored JSON text; contract=external-representation/v1. Preserve exact numeric literals and text. Launcher binds recorded_by separately from claimed producer. Declare dependencies with exactly four identity fields and exact JSON pointers. Supplied means linked bytes. No extraction, rule execution, proof, admission or branch winner.", object(map[string]any{"contract": text(128), "request_id": text(512), "subject": subjectSchema(), "name": text(1024), "version": text(1024), "role": text(16), "format": text(1024), "format_version": text(1024), "content": text(262144), "producer": text(1024), "mapping_claim": text(8192), "dependencies": array(dep, 64), "previous_id": text(128), "revision_reason": text(8192), "canonical_node_id": text(512), "derivation_id": text(512)}, "previous_id", "revision_reason", "canonical_node_id", "derivation_id"), false),
		tool("link_external_check_representation", "Index an already recorded check input that exactly equals the complete representation envelope. Separate transaction from record creation; no implicit link. Cannot attach an old check to supplemented material or change any result.", object(map[string]any{"check_id": text(128), "input_id": text(128), "representation_id": text(128)}), false),
	}
}
