// Package mcpsupersession exposes exact-reviewed fresh-v2 replacements only.
package mcpsupersession

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

const ToolReview = "get_supersession_review"
const ToolAdmit = "admit_reviewed_supersession"

type Backend struct {
	pool      *pgxpool.Pool
	principal runtimeauth.Principal
}

func NewBackend(pool *pgxpool.Pool, principal runtimeauth.Principal) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("supersession reviewer requires PostgreSQL")
	}
	p, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	return &Backend{pool: pool, principal: p}, nil
}
func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("supersession reviewer unavailable")
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
		var req struct {
			ProposalOccurrenceID string                                     `json:"proposal_occurrence_id"`
			Basis                evidenceingestion.SupersessionLineageBasis `json:"basis"`
			TargetNodeIDs        []string                                   `json:"target_node_ids"`
		}
		if err = decodeRequest(args, &req); err == nil {
			value, err = evidenceingestion.LoadSupersessionReview(ctx, b.pool, evidenceingestion.SupersessionReviewRequest{ProposalOccurrenceID: req.ProposalOccurrenceID, Basis: req.Basis, TargetNodeIDs: req.TargetNodeIDs})
		}
	case ToolAdmit:
		var req evidenceingestion.ReviewedSupersessionInput
		if err = decodeRequest(args, &req); err == nil {
			req.ReviewerID = b.principal.ID
			value, err = evidenceingestion.AdmitReviewedSupersession(ctx, b.pool, req)
		}
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in supersession profile")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func object(fields map[string]any, optional ...string) map[string]any {
	keys := []string{}
	for key := range fields {
		if !slices.Contains(optional, key) {
			keys = append(keys, key)
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
	basis := object(map[string]any{"source_system": text(64), "source_namespace": text(200), "object_type": text(100), "object_id": text(500), "slot_kind": text(100), "slot_id": text(500)})
	targets := map[string]any{"type": "array", "items": text(200), "minItems": 1, "maxItems": 64, "uniqueItems": true}
	selection := object(map[string]any{"proposal_occurrence_id": text(200), "basis": basis, "target_node_ids": targets})
	review := object(map[string]any{"proposal_occurrence_id": text(200), "basis": basis, "target_node_ids": targets, "expected_revision": map[string]any{"type": "integer", "minimum": 0}, "expected_head_event_id": text(200)}, "expected_head_event_id")
	ro, no, yes := true, false, true
	return []mcpstdio.Tool{
		{Name: ToolReview, Title: "Review Fresh Supersession", Description: "Read one complete native fresh-v2 external source_claim replacement review: pending new proposal, all older admitted targets and their exact sources/revisions, six-field source-object/slot basis, current CAS head and immutable subject. Requires an exact source snapshot with an externally assigned revision, the same source-object identity and a complete semantically justified target set; a new revision alone does not justify replacement; an ordinarily admitted replacement cannot be retrofitted. Stop if these prerequisites are unavailable; do not fabricate identities or slot assignments, or treat a newly assigned revision as clearing a reason code. Show the entire display and lifecycle before asking for human approval; compare retained old/new source material and disclose comparison limits. An unchanged same-C occurrence cannot self-target; a singleton unclassified source object may qualify for separately authorized initial-lineage review. No repository_code or derived endpoint supersession. exact_replay_only is historical review, not new approval. No writes or truth/freshness determination. See docs/CURRENT_USE_GUIDANCE.md.", InputSchema: selection, Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: ToolAdmit, Title: "Admit Reviewed Supersession", Description: "Only after explicit human approval of the complete unchanged get_supersession_review display. Submit display.request as review, subject as expected_subject, decision=approved, human reason and stable request_id. Launcher binds reviewer. Atomically admit the fresh source claim, exact new-to-old replacement edges, lineage/event/head and independent receipt. Preserve every input for retries; changed review/head needs new review and approval. Old targets become superseded, not current. Success does not guarantee source-object closure, a unique frontier or current-use eligibility: read currentness and reassess the complete bases under the same policy. Missing review/decision or unresolved prerequisites stop dependent use; see docs/CURRENT_USE_GUIDANCE.md. Does not enable generic relations, repository or derived replacement.", InputSchema: object(map[string]any{"request_id": text(200), "review": review, "expected_subject": text(200), "decision": map[string]any{"type": "string", "enum": []string{"approved"}}, "decision_reason": text(2000)}), Annotations: mcpstdio.Annotations{ReadOnlyHint: &no, DestructiveHint: &no, IdempotentHint: &yes}},
	}
}
