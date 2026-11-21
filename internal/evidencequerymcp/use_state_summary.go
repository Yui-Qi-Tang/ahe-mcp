package evidencequerymcp

import (
	"encoding/json"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

// UseStateMode selects transport detail, never an evaluation policy.
type UseStateMode string

const (
	// UseStateFull retains the existing complete governance snapshot response.
	UseStateFull UseStateMode = "full"
	// UseStateAssessmentOnly omits the snapshot while retaining its assessment.
	UseStateAssessmentOnly UseStateMode = "assessment_only"
)

func (m UseStateMode) validate() error {
	if m != "" && m != UseStateFull && m != UseStateAssessmentOnly {
		return invalidUseStateMode()
	}
	return nil
}

func invalidUseStateMode() error {
	return &ToolError{Code: toolErrorInvalidRequest, Message: "use_state_mode must be full or assessment_only"}
}

// UnmarshalJSON rejects explicit null as well as unknown transport modes.
func (m *UseStateMode) UnmarshalJSON(raw []byte) error {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return invalidUseStateMode()
	}
	mode := UseStateMode(*value)
	if mode == "" || mode.validate() != nil {
		return invalidUseStateMode()
	}
	*m = mode
	return nil
}

// UseStateModeSchema is shared by the query tool registries.
func UseStateModeSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{string(UseStateFull), string(UseStateAssessmentOnly)},
		"description": "Optional transport mode. full (default) returns the complete snapshot. assessment_only returns the unchanged assessment and explicitly omitted-snapshot context; its full-state digest cannot be recomputed from that response. No policy or current-use gate changes."}
}

// UseStateSummary carries an assessment without pretending to be a Snapshot.
type UseStateSummary struct {
	Contract        string                  `json:"contract"`
	Assessment      evidenceuse.Assessment  `json:"assessment"`
	SnapshotContext UseStateSnapshotContext `json:"snapshot_context"`
	Limitations     []string                `json:"limitations"`
}

// UseStateSnapshotContext identifies the omitted complete input to evaluation.
type UseStateSnapshotContext struct {
	ProjectionVersion              string `json:"projection_version"`
	StoreID                        string `json:"store_id"`
	CutID                          string `json:"cut_id"`
	NodeCount                      int    `json:"node_count"`
	SourceCount                    int    `json:"source_count"`
	ContradictionCount             int    `json:"contradiction_count"`
	SnapshotOmitted                bool   `json:"snapshot_omitted"`
	DigestScope                    string `json:"digest_scope"`
	DigestRecomputableFromResponse bool   `json:"digest_recomputable_from_response"`
	GlobalAbsenceInferenceAllowed  bool   `json:"global_absence_inference_allowed"`
}

func summarizeUseState(p *evidenceingestion.EvidenceUseProjection) *UseStateSummary {
	if p == nil {
		return nil
	}
	// Detach slices so a typed consumer cannot mutate a cached/native assessment.
	a := p.Assessment
	a.Roots = slices.Clone(a.Roots)
	a.Nodes = slices.Clone(a.Nodes)
	for i := range a.Nodes {
		a.Nodes[i].Reasons = slices.Clone(a.Nodes[i].Reasons)
	}
	return &UseStateSummary{Contract: "evidence-use-summary/v1", Assessment: a,
		SnapshotContext: UseStateSnapshotContext{
			ProjectionVersion: p.Snapshot.Version, StoreID: p.Snapshot.StoreID, CutID: p.Snapshot.CutID,
			NodeCount: len(p.Snapshot.Nodes), SourceCount: len(p.Snapshot.Sources), ContradictionCount: len(p.Snapshot.Contradictions),
			SnapshotOmitted: true, DigestScope: "complete_snapshot", DigestRecomputableFromResponse: false, GlobalAbsenceInferenceAllowed: false,
		},
		Limitations: []string{
			"The unchanged assessment and state_digest were computed from the complete native snapshot. This response omits that snapshot; it is not a partial Snapshot and cannot independently reproduce or verify its digest. Request full mode for that material.",
			"Assessment nodes retain all evaluated roots and AND ancestry. This is a historical assessment at evaluated_at, not source truth, action support or an execution lease. Recheck the complete declared bases with check_current_evidence_plan before current use.",
			"Transport omission does not reduce native evaluation scope or its resource limits. Separate reads may observe different cuts.",
		}}
}

func formatRecordUseState(response GetEvidenceRecordResponse, mode UseStateMode) GetEvidenceRecordResponse {
	if mode == UseStateAssessmentOnly {
		response.UseStateSummary = summarizeUseState(response.UseState)
		response.UseState = nil
	}
	return response
}
