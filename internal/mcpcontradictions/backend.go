// Package mcpcontradictions exposes independent exact-reviewed pair-v1 decisions.
package mcpcontradictions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ToolReview = "get_contradiction_review"
const ToolApply = "apply_reviewed_contradiction"

type Backend struct {
	pool      *pgxpool.Pool
	principal runtimeauth.Principal
}

func NewBackend(pool *pgxpool.Pool, principal runtimeauth.Principal) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("contradiction reviewer requires PostgreSQL")
	}
	p, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	return &Backend{pool: pool, principal: p}, nil
}
func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("contradiction reviewer unavailable")
	}
	if ctx == nil {
		return nil, errors.New("context required")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var value any
	var err error
	switch name {
	case ToolReview:
		var req evidenceingestion.ContradictionReviewRequest
		if err = decodeRequest(args, &req); err == nil {
			value, err = evidenceingestion.LoadContradictionReview(ctx, b.pool, req)
		}
	case ToolApply:
		var req evidenceingestion.ReviewedContradictionInput
		if err = decodeRequest(args, &req); err == nil {
			req.ReviewerID = b.principal.ID
			value, err = evidenceingestion.ApplyReviewedContradiction(ctx, b.pool, req)
		}
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in contradiction profile")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func object(fields map[string]any, optional ...string) map[string]any {
	keys := []string{}
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
	review := object(map[string]any{"proposal_request_id": text(200), "node_a_id": text(200), "node_b_id": text(200), "rationale": text(4000), "scope": text(4000), "conditions": map[string]any{"type": "array", "items": text(2000), "maxItems": 32}, "producer_name": text(200), "producer_version": text(200), "producer_session_ref": text(500), "source_proposal_occurrence_id": text(512), "relation": map[string]any{"type": "string", "enum": []string{"contradicts"}}}, "producer_session_ref", "source_proposal_occurrence_id")
	ro, no, yes := true, false, true
	return []mcpstdio.Tool{
		{Name: ToolReview, Title: "Review Contradiction", Description: "Read the complete native pair-v1 review: both exact admitted endpoints and available source evidence, agent rationale, stated scope/conditions, requested symmetric contradiction effect, store/revision and subject. Show the whole display before an independent human relation decision. Node approval is insufficient. Getter is read-only and does not reserve the pair. Terminal native decisions return exact_replay_only historical material; other terminal pairs cannot reopen. No truth choice, inferred clique, withdrawal or supersession.", InputSchema: review, Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: ToolApply, Title: "Apply Reviewed Contradiction", Description: "After explicit independent human approved/reject/audit_only and reason, submit the unchanged display.request as review and subject as expected_subject with a stable request_id. Launcher binds reviewer. Atomically persist pair-v1 proposal, terminal decision, exact review receipt and only on approval the single symmetric edge. Preserve all inputs for retries; changed subjects need fresh review. No other node or relation writes, terminal reopening or erroneous-relation withdrawal.", InputSchema: object(map[string]any{"request_id": text(200), "review": review, "expected_subject": text(200), "decision": map[string]any{"type": "string", "enum": []string{"approved", "reject", "audit_only"}}, "decision_reason": text(2000)}), Annotations: mcpstdio.Annotations{ReadOnlyHint: &no, DestructiveHint: &no, IdempotentHint: &yes}},
	}
}
