package evidencequerymcp

import (
	"context"
	"encoding/json"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

const ToolGetEvidenceUseState = "get_evidence_use_state"
const ToolGetGovernanceHistory = "get_evidence_governance_history"
const ToolCheckCurrentPlan = "check_current_evidence_plan"

func GovernanceTools() []mcpstdio.Tool {
	ro, no, yes := true, false, true
	annotations := mcpstdio.Annotations{ReadOnlyHint: &ro, DestructiveHint: &no, IdempotentHint: &yes}
	ids := map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}}
	policy := map[string]any{"type": "string", "enum": []string{string(evidenceuse.CurrentActionV1), string(evidenceuse.GroundedActionV1)}}
	object := func(fields map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}
	}
	return []mcpstdio.Tool{
		{Name: ToolGetEvidenceUseState, Description: "Read one authoritative RR cut of admission, withdrawal, source assessments, full AND ancestry and actual incident contradictions, including peers outside displayed graphs. The explicitly selected policy assessment is historical at evaluated_at; use check_current_evidence_plan again at final action acceptance. Limits or RLS fail closed.", InputSchema: object(map[string]any{"canonical_ids": ids, "policy": policy}, "canonical_ids", "policy"), Annotations: annotations},
		{Name: ToolGetGovernanceHistory, Description: "Read complete bounded append-only native governance receipts for one exact target. Original request, subject/display, reviewer/reason and per-target revision remain inspectable; no new-use permission.", InputSchema: object(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"withdrawal", "source_assessment"}}, "target_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}}, "kind", "target_id"), Annotations: annotations},
		{Name: ToolCheckCurrentPlan, Description: "Mandatory final gate for current actions: supply every action and its declared canonical basis IDs, after planning/SAT. Rereads authoritative full governance and AND ancestry under the explicitly selected policy; grounded-current-action/v1 requires positive source integrity, correspondence and coverage checks. accepted=false is governance rejection, never UNSAT; accepted=true does not prove entailment or authorize later external execution. Never reuse a historical pass.", InputSchema: object(map[string]any{"policy": policy, "actions": map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": object(map[string]any{"action_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "basis_ids": ids}, "action_id", "basis_ids")}}, "actions", "policy"), Annotations: annotations},
	}
}
func (s *Server) callGovernance(ctx context.Context, name string, payload []byte) ([]byte, error) {
	var out any
	var err error
	switch name {
	case ToolGetEvidenceUseState:
		var req struct {
			IDs    []string           `json:"canonical_ids"`
			Policy evidenceuse.Policy `json:"policy"`
		}
		if err = decodeStrict(payload, &req); err == nil {
			if len(req.IDs) < 1 || len(req.IDs) > 32 {
				return nil, &ToolError{Code: toolErrorInvalidRequest, Message: "1..32 canonical IDs required"}
			}
			if err := evidenceuse.ValidatePolicy(req.Policy); err != nil {
				return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error()}
			}
			out, err = evidenceingestion.ReadEvidenceUseProjectionForPolicy(ctx, s.recordsPool, req.Policy, req.IDs)
		}
	case ToolGetGovernanceHistory:
		var req struct {
			Kind   string `json:"kind"`
			Target string `json:"target_id"`
		}
		if err = decodeStrict(payload, &req); err == nil {
			out, err = evidenceingestion.ReadGovernanceHistory(ctx, s.recordsPool, req.Kind, req.Target)
		}
	case ToolCheckCurrentPlan:
		var req struct {
			Actions []evidenceuse.ActionBasis `json:"actions"`
			Policy  evidenceuse.Policy        `json:"policy"`
		}
		if err = decodeStrict(payload, &req); err == nil {
			if len(req.Actions) < 1 || len(req.Actions) > 128 {
				return nil, &ToolError{Code: toolErrorInvalidRequest, Message: "1..128 actions required"}
			}
			if err := evidenceuse.ValidatePolicy(req.Policy); err != nil {
				return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error()}
			}
			out, err = evidenceingestion.CheckCurrentEvidencePlan(ctx, s.recordsPool, req.Policy, req.Actions)
		}
	}
	if err != nil {
		return nil, mapToolError(err)
	}
	return json.Marshal(out)
}
