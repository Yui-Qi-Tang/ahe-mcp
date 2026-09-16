package evidenceingestion

// CanonicalReferencesLimitationsV1 returns the fixed structural review boundary.
// Each caller owns its copy; neither an agent nor a caller can widen the effect.
func CanonicalReferencesLimitationsV1() []string {
	return []string{"observed_target_uniqueness_not_permanent_or_commit_latest",
		"no_truth_support_dependency_status_supersession_or_currentness_effect",
		"reviewer_and_display_are_asserted_not_authenticated"}
}

// CanonicalReferencesReviewRequest supplies assertions that must match independently loaded facts.
type CanonicalReferencesReviewRequest struct {
	ResolverProfile string                              `json:"resolver_profile"`
	FromNodeID      string                              `json:"from_node_id"`
	ToNodeID        string                              `json:"to_node_id"`
	Reference       CanonicalReferencesReferenceWitness `json:"reference"`
	Target          CanonicalReferencesTargetWitness    `json:"target"`
}

// CanonicalReferencesReferenceWitness identifies the token within A's exact evidence span.
type CanonicalReferencesReferenceWitness struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	ExtractionViewID string `json:"extraction_view_id"`
	SpanID           string `json:"span_id"`
	StartByte        int    `json:"start_byte"`
	EndByte          int    `json:"end_byte"`
	Token            string `json:"token"`
	TokenHash        string `json:"token_hash"`
}

// CanonicalReferencesTargetWitness identifies the complete line named by an explicit anchor.
type CanonicalReferencesTargetWitness struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	ExtractionViewID string `json:"extraction_view_id"`
	SpanID           string `json:"span_id"`
	AnchorID         string `json:"anchor_id"`
	SpanHash         string `json:"span_hash"`
}

// CanonicalReferencesTargetCut retains the complete overlapping claim IDs at one native observation.
// Later admissions do not change this historical observation or move its edge.
type CanonicalReferencesTargetCut struct {
	ContractVersion  string   `json:"contract_version"`
	SourceSnapshotID string   `json:"source_snapshot_id"`
	ExtractionViewID string   `json:"extraction_view_id"`
	SpanID           string   `json:"span_id"`
	StartByte        int      `json:"start_byte"`
	EndByte          int      `json:"end_byte"`
	SpanHash         string   `json:"span_hash"`
	CandidateIDs     []string `json:"candidate_ids"`
}

// CanonicalReferencesResolution retains exact coordinates and the observed unique target cut.
type CanonicalReferencesResolution struct {
	ContractVersion string                              `json:"contract_version"`
	ResolverProfile string                              `json:"resolver_profile"`
	FromNodeID      string                              `json:"from_node_id"`
	ToNodeID        string                              `json:"to_node_id"`
	Reference       CanonicalReferencesReferenceWitness `json:"reference"`
	Target          CanonicalReferencesTargetWitness    `json:"target"`
	TargetCut       CanonicalReferencesTargetCut        `json:"target_cut"`
}

// CanonicalReferencesEndpointCard displays original admitted evidence and its qualified source.
type CanonicalReferencesEndpointCard struct {
	NodeID                     string              `json:"node_id"`
	OriginProposalOccurrenceID string              `json:"origin_proposal_occurrence_id"`
	Claim                      string              `json:"claim"`
	SourceSnapshotID           string              `json:"source_snapshot_id"`
	ExtractionViewID           string              `json:"extraction_view_id"`
	SourceSystem               string              `json:"source_system"`
	SourceID                   string              `json:"source_id"`
	SourceVersion              string              `json:"source_version"`
	RawContentHash             string              `json:"raw_content_hash"`
	RenderedContentHash        string              `json:"rendered_content_hash"`
	Title                      string              `json:"title"`
	Location                   string              `json:"location"`
	Coverage                   string              `json:"coverage"`
	Limitations                []string            `json:"limitations"`
	SourceRefs                 []ResolvedSourceRef `json:"source_refs"`
}

// CanonicalReferencesReviewBody is the complete exact display, with no approval authority.
type CanonicalReferencesReviewBody struct {
	ContractVersion string                           `json:"contract_version"`
	Request         CanonicalReferencesReviewRequest `json:"request"`
	Resolution      CanonicalReferencesResolution    `json:"resolution"`
	From            CanonicalReferencesEndpointCard  `json:"from"`
	To              CanonicalReferencesEndpointCard  `json:"to"`
	Relation        string                           `json:"relation"`
	Effect          string                           `json:"effect"`
	Limitations     []string                         `json:"limitations"`
}
