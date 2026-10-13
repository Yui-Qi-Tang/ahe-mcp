// Package mcpgovernance exposes two separately provisioned exact-review writers.
package mcpgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Backend struct {
	pool      *pgxpool.Pool
	principal runtimeauth.Principal
	kind      string
}

func NewBackend(pool *pgxpool.Pool, p runtimeauth.Principal, profile string) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("governance reviewer requires PostgreSQL")
	}
	principal, err := runtimeauth.NewPrincipal(p.ID)
	if err != nil {
		return nil, err
	}
	kind := ""
	switch profile {
	case "withdrawal-reviewer":
		kind = "withdrawal"
	case "source-assessment-reviewer":
		kind = "source_assessment"
	default:
		return nil, errors.New("unsupported governance reviewer profile")
	}
	return &Backend{pool: pool, principal: principal, kind: kind}, nil
}

func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("governance reviewer unavailable")
	}
	if ctx == nil {
		return nil, errors.New("context required")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var out any
	var err error
	switch name {
	case "get_" + b.kind + "_review":
		var req evidenceingestion.GovernanceRequest
		if err = decodeRequest(args, &req); err != nil {
			return nil, err
		}
		if req.Kind != b.kind {
			return nil, runtimeauth.NewUnauthorizedError("wrong governance capability")
		}
		out, err = evidenceingestion.LoadGovernanceReview(ctx, b.pool, req)
	case "apply_reviewed_" + b.kind:
		var req evidenceingestion.GovernanceDecision
		if err = decodeRequest(args, &req); err != nil {
			return nil, err
		}
		if req.Review.Kind != b.kind {
			return nil, runtimeauth.NewUnauthorizedError("wrong governance capability")
		}
		req.ReviewerID = b.principal.ID
		out, err = evidenceingestion.ApplyGovernanceDecision(ctx, b.pool, req)
	case "prepare_" + b.kind + "_batch":
		var req struct {
			Items []evidenceingestion.GovernanceBatchRequest `json:"items"`
		}
		if err = decodeRequest(args, &req); err != nil {
			return nil, err
		}
		for _, item := range req.Items {
			r := item.Record
			if r.Kind != "verification" && r.Native.Kind != b.kind {
				return nil, runtimeauth.NewUnauthorizedError("wrong governance capability")
			}
		}
		out, err = evidenceingestion.PrepareGovernanceBatch(ctx, b.pool, req.Items)
	case "apply_reviewed_" + b.kind + "_batch":
		var req evidenceingestion.GovernanceBatchApproval
		if err = decodeRequest(args, &req); err != nil {
			return nil, err
		}
		for _, r := range req.ApprovedItems {
			if r.Decision.Review.Kind != b.kind {
				return nil, runtimeauth.NewUnauthorizedError("wrong governance capability")
			}
		}
		out, err = evidenceingestion.ApplyGovernanceBatch(ctx, b.pool, req, b.principal.ID)
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation unavailable in governance profile")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
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
func text(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
func texts(max int) map[string]any {
	return map[string]any{"type": "array", "maxItems": max, "items": text(2000), "uniqueItems": true}
}
func (b *Backend) Tools() []mcpstdio.Tool {
	if b == nil {
		return nil
	}
	check := object(map[string]any{"assessed_field": text(200), "corrected_value": text(512), "assessment_id": text(200), "source_snapshot_id": text(200), "content_hash": text(200), "scope": text(200), "criterion": text(200), "result": map[string]any{"type": "string", "enum": []string{"pass", "fail", "inconclusive", "unknown"}}, "compared_revision": text(512), "material_ref": text(200), "limitations": texts(32)}, "assessment_id", "assessed_field", "corrected_value")
	request := object(map[string]any{"migration_ref": text(512), "kind": map[string]any{"type": "string", "enum": []string{b.kind}}, "target_id": text(200), "operation": text(20), "expected_revision": map[string]any{"type": "integer", "minimum": 0}, "previous_event_id": text(200), "correction_ids": texts(32), "checks": map[string]any{"type": "array", "maxItems": 32, "items": check}, "material_ids": texts(32), "resolves_event_ids": texts(32), "method": text(512), "checker_name": text(200), "checker_version": text(200), "checked_at": text(64), "limitations": texts(32)}, "migration_ref", "previous_event_id", "method", "checker_name", "checker_version", "checked_at")
	decision := object(map[string]any{"request_id": text(200), "review": request, "expected_subject": text(200), "decision": map[string]any{"type": "string", "enum": []string{"approved"}}, "reason": text(2000)})
	material := object(map[string]any{"identity": text(512), "revision": text(512), "content_hash": text(200)})
	materials := map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": material}
	recordFields := map[string]any{"revision": map[string]any{"type": "integer", "minimum": 1}, "original_materials": materials, "checked_materials": map[string]any{"type": "array", "maxItems": 128, "items": material}, "limitations": texts(32), "missing_fields": texts(32), "native": request}
	for _, f := range []string{"version", "record_id", "previous_record_hash", "store_id", "kind", "canonical_id", "claim_hash", "occurrence_id", "source_snapshot_id", "source_content_hash", "purpose", "scope", "reason", "outcome", "historical_reviewer", "historical_reviewed_at", "checker_name", "checker_version", "checked_at", "criterion", "criterion_version", "freshness_rule"} {
		recordFields[f] = text(512)
	}
	subject := object(map[string]any{"store_id": text(200), "canonical_id": text(200), "claim_hash": text(200), "criterion": text(200), "criterion_version": text(200), "scope": text(512)})
	recordFields["verification"] = object(map[string]any{"version": text(200), "subject": subject, "materials": materials, "materials_complete": map[string]any{"type": "boolean"}, "result": text(50), "checked_at": text(64)})
	record := object(recordFields, "native", "previous_record_hash", "canonical_id", "claim_hash", "occurrence_id", "source_snapshot_id", "source_content_hash", "verification", "checker_name", "checker_version", "checked_at", "criterion", "criterion_version", "freshness_rule", "checked_materials", "limitations")
	// Returned history keeps its existing JSON and fingerprints: omitted slices
	// encode as null, and an omitted native operation encodes as the zero request.
	// Only this historical record accepts those forms; executable requests retain
	// the strict profile schema and still require an exact native approval.
	manifestRecordFields := maps.Clone(recordFields)
	for _, field := range []string{"checked_materials", "limitations"} {
		manifestRecordFields[field] = map[string]any{"anyOf": []any{recordFields[field], map[string]any{"type": "null"}}}
	}
	manifestRecordFields["native"] = map[string]any{"anyOf": []any{request, map[string]any{"const": evidenceingestion.GovernanceRequest{}}}}
	manifestRecord := maps.Clone(record)
	manifestRecord["properties"] = manifestRecordFields
	manifestItem := object(map[string]any{"record": manifestRecord, "record_hash": text(200), "status": text(100), "error": text(2000), "reason": text(2000), "native_request_id": text(200), "native_subject": text(200), "native_request": request}, "error", "native_request_id", "native_subject", "native_request")
	manifest := object(map[string]any{"contract": text(100), "store_id": text(200), "items": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": manifestItem}})

	ro, no, yes := true, false, true
	return []mcpstdio.Tool{
		{Name: "prepare_" + b.kind + "_batch", Description: "Read-only preparation of versioned interim records. Verify store, target/hash/occurrence, historical review/checker separation, duplicates and explicit missing fields. Returns the exact item-set manifest, its subject, proposed per-item reasons and native displays. Show the historical records and unchanged native displays together. Checker fields are inapplicable for a purely human exclusion; unavailable history remains explicit missing fields. Verification-only records never create an exclusion or trusted truth label. Local historical approval does not approve this new display.", InputSchema: object(map[string]any{"items": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": object(map[string]any{"record": record, "reason": text(2000)})}}), Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: "apply_reviewed_" + b.kind + "_batch", Description: "Apply only the explicitly human-approved subset, against the unchanged manifest and expected_manifest_subject, preserving each prepared native request, subject and proposed human reason. The receipt retains batch_subject. Changed subjects require new review; an unchanged original manifest can retry individual committed or unaffected items. Each item is its own transaction; mixed success/replay/conflict/failure is possible. Returns durable local-to-native mapping and history readback. Keep local enforcement until Query and the real current-use consumer are verified. Retry uncertain outcomes with identical arguments.", InputSchema: object(map[string]any{"manifest": manifest, "expected_manifest_subject": text(200), "approved_items": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": object(map[string]any{"record_id": text(200), "decision": decision})}}), Annotations: mcpstdio.Annotations{ReadOnlyHint: &no, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: "get_" + b.kind + "_review", Description: "Read the exact native governance display, original retained bytes/declarations, history, explicit effect and per-target CAS. Show the complete unchanged subject to the human. Missing provenance is a limitation, not a withdrawal precondition. No writes.", InputSchema: request, Annotations: mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}},
		{Name: "apply_reviewed_" + b.kind, Description: "Apply only an explicit human approval of the unchanged native display. Submit exact display.request, expected_subject, stable request_id, decision=approved and human reason. Launcher binds reviewer. Preserve exact arguments for uncertain retries. History is retained; no admission/source/parent/replacement rewrite. Historical receipt is not a new-use clearance.", InputSchema: object(map[string]any{"request_id": text(200), "review": request, "expected_subject": text(200), "decision": map[string]any{"type": "string", "enum": []string{"approved"}}, "reason": text(2000)}), Annotations: mcpstdio.Annotations{ReadOnlyHint: &no, DestructiveHint: &no, IdempotentHint: &yes}},
	}
}
