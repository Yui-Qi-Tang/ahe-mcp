// Package mcpinitiallineage exposes independently reviewed first memberships.
package mcpinitiallineage

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

const ToolReview = "get_initial_lineage_registration_review"
const ToolAdmit = "apply_reviewed_initial_lineage_registration"

type Backend struct {
	pool      *pgxpool.Pool
	principal runtimeauth.Principal
}

func NewBackend(pool *pgxpool.Pool, principal runtimeauth.Principal) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("initial lineage reviewer requires PostgreSQL")
	}
	p, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	return &Backend{pool: pool, principal: p}, nil
}
func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("initial lineage reviewer unavailable")
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
		var req evidenceingestion.InitialLineageRegistrationRequest
		if err = decodeRequest(args, &req); err == nil {
			value, err = evidenceingestion.LoadInitialLineageRegistrationReview(ctx, b.pool, req)
		}
	case ToolAdmit:
		var req struct {
			RequestID       string                                              `json:"request_id"`
			Review          evidenceingestion.InitialLineageRegistrationRequest `json:"review"`
			ExpectedSubject string                                              `json:"expected_subject"`
			Decision        string                                              `json:"decision"`
			DecisionReason  string                                              `json:"decision_reason"`
		}
		if err = decodeRequest(args, &req); err == nil {
			value, err = evidenceingestion.ApplyInitialLineageRegistration(ctx, b.pool, evidenceingestion.InitialLineageRegistrationDecision{
				RequestID: req.RequestID, Review: req.Review, ExpectedSubject: req.ExpectedSubject, Decision: req.Decision, Reason: req.DecisionReason, ReviewerID: b.principal.ID,
			})
		}
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in initial lineage profile")
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
	selection := object(map[string]any{"claim_id": text(200), "observation_receipt_id": text(200), "basis": basis})
	ro, no, yes := true, false, true
	return []mcpstdio.Tool{
		{Name: ToolReview, Title: "Review Initial Lineage Registration", Description: "Read one complete native review for an admitted external source claim's first lineage membership. Requires a singleton admitted source object, an unclassified claim, a fresh six-field lineage and a persisted observation of the original snapshot/view. Display the entire grounding, observation, effect and limitations for independent human review. Receipt IDs and timestamps do not prove provider recontact, freshness or truth. This read grants no membership or current-use permission.", InputSchema: selection, Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: ToolAdmit, Title: "Apply Reviewed Initial Lineage Registration", Description: "Only after explicit independent human approval of the complete unchanged review. Copy request into review and subject into expected_subject; supply decision=approved, a human decision_reason and stable request_id. Launcher binds reviewer. Atomically append registration and first membership; preserve the original claim, source, admission, temporal status and derived parents. The v2 head is unchanged. Exact retry returns historical authority, not current eligibility. Re-read currentness and complete current-use policy. No relocation, deregistration, duplicate retirement or freshness guarantee.", InputSchema: object(map[string]any{"request_id": text(200), "review": selection, "expected_subject": text(200), "decision": map[string]any{"type": "string", "enum": []string{"approved"}}, "decision_reason": text(2000)}), Annotations: mcpstdio.Annotations{ReadOnlyHint: &no, DestructiveHint: &no, IdempotentHint: &yes}},
	}
}
