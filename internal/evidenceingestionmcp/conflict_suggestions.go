package evidenceingestionmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

const ToolSuggestIntakeConflicts = "suggest_intake_conflicts"
const SuggestIntakeConflictsDescription = "Read bounded conflict candidates for one persisted grounded statement proposal, from exact external object/declared slot identity, actual direct contradiction/supersession edges and lexical overlap. Returns candidate claim/source coordinates, matching basis, scan and return limits, truncation and snapshot identity. Optional basis is a caller-declared retrieval hint, never a binding or admission. No suggestions does not prove no conflict; overlap is not a contradiction verdict. Read-only: never admit, withdraw, supersede or choose truth."

type conflictSuggestionCore interface {
	SuggestIntakeConflicts(context.Context, evidenceingestion.ConflictSuggestionRequest) (evidenceingestion.ConflictSuggestionResult, error)
}

func (p postgresCore) SuggestIntakeConflicts(ctx context.Context, r evidenceingestion.ConflictSuggestionRequest) (evidenceingestion.ConflictSuggestionResult, error) {
	return evidenceingestion.SuggestIntakeConflicts(ctx, p.pool, r)
}
func (s *Server) suggestIntakeConflicts(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if s == nil || s.conflictSuggestions == nil {
		return nil, errors.New("conflict suggestion reader unavailable")
	}
	var req evidenceingestion.ConflictSuggestionRequest
	if err := decodeBoundedReviewRequest(args, &req, "intake conflict suggestions"); err != nil {
		return nil, err
	}
	value, err := s.conflictSuggestions.SuggestIntakeConflicts(ctx, req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func ConflictSuggestionSchema() map[string]any {
	text := func(n int) map[string]any { return map[string]any{"type": "string", "minLength": 1, "maxLength": n} }
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"proposal_occurrence_id"}, "properties": map[string]any{
		"proposal_occurrence_id": text(512), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "scan_limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
		"basis": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"source_system", "source_namespace", "object_type", "object_id", "slot_kind", "slot_id"}, "properties": map[string]any{"source_system": text(64), "source_namespace": text(200), "object_type": text(100), "object_id": text(500), "slot_kind": text(100), "slot_id": text(500)}},
	}}
}
