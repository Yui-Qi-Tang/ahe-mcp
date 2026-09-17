package mcprelations

import (
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

// Tools returns a new closed schema for each call; mutating discovery metadata
// cannot change the server's dispatch or authority.
func (b *Backend) Tools() []mcpstdio.Tool {
	ro, write, no, terminal, yes := true, false, false, true, true
	tools := []mcpstdio.Tool{
		{Name: ToolGetImplementsReview, Title: "Review Implements", Description: "Read a complete bounded derived-specification ancestor AND closure and versioned Go code evidence. Supply one explicit rule per derived layer and the proposed mapping; these assertions do not grant authority. Show the complete display (including exact source quotations, code revision/path, all rules, coverage and limitations), retain the returned review and subject unchanged, then ask for explicit relation approval. Only derived_claim to repository-backed source_claim is supported. No write or approval occurs.",
			InputSchema: implementsSchema(), Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: ToolAdmitReviewedImplements, Title: "Admit Reviewed Implements", Description: "Only after explicit approval of the complete unchanged implements review, append one directed navigation edge and its independent receipt. Copy review and expected_subject from get_implements_review. Reuse request_id exactly for retries. Node admission is not relation approval. Reviewer identity is launcher-bound. Does not create nodes or prove semantic correctness, test coverage, transitivity or currentness.",
			InputSchema: admissionSchema(implementsSchema(), object(map[string]any{"admitted_cut_id": text(512), "report_id": text(512), "candidate_id": text(512), "basis_id": text(512), "review_package_id": text(512), "display_id": text(512)})), Annotations: mcpstdio.Annotations{ReadOnlyHint: &write, DestructiveHint: &terminal, IdempotentHint: &yes}},
		{Name: ToolGetReferencesReview, Title: "Review References", Description: "Read exact original reference-token and anchor evidence from two admitted qualified external source claims. V1 accepts only literal [[ahe-ref:#anchor]] or snapshot-pinned [[ahe-ref:srcsnap:<hash>#anchor]] markers already in original bytes; arbitrary URLs and inferred mentions are unsupported. Never edit original text to manufacture a marker. Show the complete display, retain review and subject, and obtain separate relation approval. Unique target is an observation, not permanent truth. No write occurs.",
			InputSchema: referencesSchema(), Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: ToolAdmitReviewedReferences, Title: "Admit Reviewed References", Description: "After explicit approval of the unchanged complete references display, append one independently receipted structural navigation edge. Copy review and expected_subject; reuse request_id on exact retry. Fresh admission verifies the current observed target set. Historical retries use their retained cut. No support, dependency, supersession or truth assertion is inferred; caller reviewer/receipt/graph fields are forbidden.",
			InputSchema: admissionSchema(referencesSchema(), object(map[string]any{"review_subject_id": text(200), "cut_id": text(200)})), Annotations: mcpstdio.Annotations{ReadOnlyHint: &write, DestructiveHint: &terminal, IdempotentHint: &yes}},
	}
	return tools
}
func object(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	// Stable ordering is necessary for schema hashes in tool discovery.
	for k := range properties {
		required = append(required, k)
	}
	slices.Sort(required)
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func text(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
func list(item map[string]any, min, max int) map[string]any {
	return map[string]any{"type": "array", "items": item, "minItems": min, "maxItems": max}
}
func choices(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func admissionSchema(review, subject map[string]any) map[string]any {
	return object(map[string]any{"request_id": text(200), "review": review, "expected_subject": subject, "decision": choices("approved"), "decision_reason": text(2000)})
}
func implementsSchema() map[string]any {
	return object(map[string]any{
		"specification_node_id": text(512), "implementation_node_id": text(512),
		"rules": list(object(map[string]any{"node_id": text(512), "derivation_id": text(512), "rule_statement": text(4000)}), 1, 64),
		"mapping": object(map[string]any{
			"proposal_sentence": text(4000), "coverage": text(4000), "limitations": list(text(2000), 0, evidenceimplements.MaxLimitations),
			"witnesses": list(object(map[string]any{"kind": choices(string(evidenceimplements.ExplicitSourceMapping), string(evidenceimplements.LanguageContract), string(evidenceimplements.ReviewedBehavior)), "endpoint_node_ids": list(text(512), 2, 2), "source_title": text(evidenceimplements.MaxTitleBytes), "source_location": text(evidenceimplements.MaxLocationBytes), "exact_excerpt": text(evidenceimplements.MaxExcerptBytes), "excerpt_hash": text(100)}), 1, evidenceimplements.MaxWitnesses),
		}),
	})
}
func referencesSchema() map[string]any {
	return object(map[string]any{
		"resolver_profile": choices("same-snapshot-anchor/v1", "native-snapshot-anchor/v1"),
		"from_node_id":     text(512), "to_node_id": text(512),
		"reference": object(map[string]any{"source_snapshot_id": text(200), "extraction_view_id": text(200), "span_id": text(200), "start_byte": map[string]any{"type": "integer", "minimum": 0}, "end_byte": map[string]any{"type": "integer", "minimum": 1}, "token": text(256), "token_hash": text(100)}),
		"target":    object(map[string]any{"source_snapshot_id": text(200), "extraction_view_id": text(200), "span_id": text(200), "anchor_id": text(256), "span_hash": text(100)}),
	})
}
