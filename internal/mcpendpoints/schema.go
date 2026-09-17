package mcpendpoints

import (
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"slices"
)

func object(fields map[string]any, optional ...string) map[string]any {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if !slices.Contains(optional, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return map[string]any{"type": "object", "properties": fields, "required": keys, "additionalProperties": false}
}
func text(max int) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
}
func (b *Backend) Tools() []mcpstdio.Tool {
	if b == nil {
		return nil
	}
	ro, no, yes := true, false, true
	annotation := func(read bool) mcpstdio.Annotations {
		return mcpstdio.Annotations{ReadOnlyHint: &read, DestructiveHint: &no, IdempotentHint: &yes}
	}
	if b.profile == ProfileRepositoryIntake {
		return []mcpstdio.Tool{
			{Name: ToolCapture, Title: "Capture Local Git Snapshot", Description: "Persist exact tracked regular Go file bytes from a full immutable Git commit in the launcher-approved local repository. No network protocols, checkout, build, model, canonical writes, branch inference or generation activation. Snapshot limits: 10,000 Go files, 16 MiB/file and 256 MiB total. Explicit source capture permission required.", InputSchema: object(map[string]any{"request_id": text(200), "commit_sha": text(64)}), Annotations: annotation(false)},
			{Name: ToolExtract, Title: "Extract Go Code Proposals", Description: "Run the deterministic Go parser against a persisted snapshot of the launcher repository. At most 256 files and 8 MiB. Create pending proposals and an inactive generation only; never execute source code, fetch dependencies, retry failed extraction automatically or admit evidence. Read exact proposal IDs with Query (lifecycle_scope=all); generation is not activated.", InputSchema: object(map[string]any{"request_id": text(200), "repository_snapshot_id": text(200)}), Annotations: annotation(false)},
		}
	}
	review := object(map[string]any{
		"kind":                   map[string]any{"type": "string", "enum": []string{"repository_code", "derived_spec"}},
		"proposal_occurrence_id": text(200),
		"derivation": object(map[string]any{
			"parent_node_ids": map[string]any{"type": "array", "items": text(200), "minItems": 1, "maxItems": 8, "uniqueItems": true},
			"method":          text(4000), "producer": text(200), "trace_ref": text(500),
		}),
	}, "derivation")
	return []mcpstdio.Tool{
		{Name: ToolReview, Title: "Review Evidence Endpoint", Description: "Reconstruct one endpoint from native DB authority. repository_code requires a parser-grounded repository proposal and forbids derivation; derived_spec requires a source-backed statement proposal plus one to eight sorted admitted AND parent IDs and explicit derivation method, producer, trace_ref. Show lifecycle together with the complete display, source context, immutable code and all ancestor leaves. lifecycle reports current admission state: pending_admission permits a new review; exact_replay_only identifies an admitted endpoint with its original receipt. display is the immutable pre-admission snapshot, not current status. An admitted proposal without a matching endpoint receipt is refused. The nested source-basis effect is context, not approval. No writes; does not prove entailment, behavior or currentness.", InputSchema: review, Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: ToolAdmit, Title: "Admit Reviewed Endpoint", Description: "Only after explicit human approval of the complete unchanged get_endpoint_review display, submit its exact request and subject with decision=approved and the human reason. Atomically create the source-backed code or derived claim, native supporting/AND edges and independent endpoint review receipt. Reviewer is launcher-bound. Reuse request_id exactly for retries. Does not grant implements/references approval, source collection or generic graph writes.", InputSchema: object(map[string]any{
			"request_id": text(200), "review": review, "expected_subject": text(200),
			"decision": map[string]any{"type": "string", "enum": []string{"approved"}}, "decision_reason": text(2000),
		}), Annotations: annotation(false)},
	}
}
