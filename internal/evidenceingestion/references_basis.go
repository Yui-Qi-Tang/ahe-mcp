package evidenceingestion

// These values are internal inputs to pure relation validation. They are not
// accepted by any MCP tool; a future loader must reconstruct them from native
// admission records before they can carry runtime authority.
// ReferencesMaxTargetCandidates bounds the complete observed overlap set.
const ReferencesMaxTargetCandidates = 64

// ReferencesNativeEndpoint describes the source/admission material that a native
// loader must reconstruct. Constructing this value does not prove persistence
// or semantic correctness; that loader has not been ported yet.
type ReferencesNativeEndpoint struct {
	Node           CanonicalQueryResult
	ReviewSnapshot ReviewableSourceClaimReviewSnapshot
	RawText        string
	RenderedText   string
	Spans          []SpanEntry
}

// ReferencesNativeBasis binds two original endpoints and a complete observed
// target claim set. A nil set cannot establish a resolved target.
type ReferencesNativeBasis struct {
	From               ReferencesNativeEndpoint
	To                 ReferencesNativeEndpoint
	TargetCandidateIDs []string
}
