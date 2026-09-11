package evidencequerymcp

import (
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

// GroundedEvidenceMultisurfaceExecution separates baseline observations from the expanded plan.
type GroundedEvidenceMultisurfaceExecution struct {
	Baseline             GroundedEvidenceQueryExecution `json:"baseline"`
	NormalizedQueryTerms []string                       `json:"normalized_query_terms"`
	SearchedSourceViews  int                            `json:"searched_source_views"`
	ExcludedSourceViews  int                            `json:"excluded_source_views"`
}

// GroundedEvidencePracticalRecovery distinguishes configuration from the effective min(n, threshold).
type GroundedEvidencePracticalRecovery struct {
	FallbackAttempted             bool                                   `json:"fallback_attempted"`
	ConfiguredMinimumEnglishTerms int                                    `json:"configured_minimum_english_terms"`
	HanTermPolicy                 GroundedEvidencePracticalHanTermPolicy `json:"han_term_policy"`
}

// GroundedEvidencePracticalHanTermPolicy distinguishes eligibility from complete query and match terms.
type GroundedEvidencePracticalHanTermPolicy struct {
	Version                     string   `json:"version"`
	AuxiliaryTerms              []string `json:"auxiliary_terms"`
	EligibleAnchorTerms         []string `json:"eligible_anchor_terms"`
	AuxiliaryOnlyQueryPreserved bool     `json:"auxiliary_only_query_preserved"`
}

func mapGroundedEvidencePracticalRecovery(input *evidenceingestion.EvidencePracticalRecovery) *GroundedEvidencePracticalRecovery {
	if input == nil {
		return nil
	}
	return &GroundedEvidencePracticalRecovery{
		FallbackAttempted: input.FallbackAttempted, ConfiguredMinimumEnglishTerms: input.ConfiguredMinimumEnglishTerms,
		HanTermPolicy: GroundedEvidencePracticalHanTermPolicy{
			Version:                     input.HanTermPolicy.Version,
			AuxiliaryTerms:              append([]string{}, input.HanTermPolicy.AuxiliaryTerms...),
			EligibleAnchorTerms:         append([]string{}, input.HanTermPolicy.EligibleAnchorTerms...),
			AuxiliaryOnlyQueryPreserved: input.HanTermPolicy.AuxiliaryOnlyQueryPreserved,
		},
	}
}

// GroundedEvidenceRetrievalBasis describes retrieval, never semantic support or a new citation.
type GroundedEvidenceRetrievalBasis struct {
	NormalizedQueryTerms []string                           `json:"normalized_query_terms"`
	BaselineMatched      bool                               `json:"baseline_matched"`
	Surfaces             []GroundedEvidenceRetrievalSurface `json:"surfaces"`
}

// GroundedEvidenceRetrievalSurface keeps statement and source text matches distinguishable.
type GroundedEvidenceRetrievalSurface struct {
	Surface             string                          `json:"surface"`
	Status              string                          `json:"status"`
	MatchedTerms        []string                        `json:"matched_terms"`
	MissingTerms        []string                        `json:"missing_terms"`
	SourceSnapshotID    string                          `json:"source_snapshot_id,omitempty"`
	ExtractionViewID    string                          `json:"extraction_view_id,omitempty"`
	RawContentHash      string                          `json:"raw_content_hash,omitempty"`
	RenderedContentHash string                          `json:"rendered_content_hash,omitempty"`
	MatchedSpans        []GroundedEvidenceRetrievalSpan `json:"matched_spans"`
	SpansTruncated      bool                            `json:"spans_truncated"`
}

// GroundedEvidenceRetrievalSpan is a source excerpt located by search, not a proposal ref amendment.
type GroundedEvidenceRetrievalSpan struct {
	SourceRef                evidenceingestion.ResolvedSourceRef `json:"source_ref"`
	MatchedTerms             []string                            `json:"matched_terms"`
	WithinProposalSourceRefs bool                                `json:"within_proposal_source_refs"`
}

func validateMultisurfaceBriefExecution(result evidenceingestion.GroundedEvidenceBriefQueryResult, schema string) error {
	execution := result.Execution
	experimental := execution.QueryMode == evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1
	practical := execution.QueryMode == evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1
	expanded := experimental || practical
	if experimental != (schema == GroundedEvidenceBriefSchemaV6) || practical != (schema == GroundedEvidenceBriefSchemaV7) ||
		expanded != (execution.Multisurface != nil) || practical != (execution.PracticalRecovery != nil) {
		return fmt.Errorf("grounded brief multisurface execution and v6 contract do not match")
	}
	if expanded {
		meta := execution.Multisurface
		if meta.Baseline.Multisurface != nil || meta.Baseline.PracticalRecovery != nil || meta.SearchedSourceViews < 0 || meta.ExcludedSourceViews < 0 ||
			(meta.ExcludedSourceViews > 0 && execution.SearchCompleteWithinSurface) || execution.GlobalAbsenceInferenceAllowed {
			return fmt.Errorf("grounded brief multisurface completeness metadata is inconsistent")
		}
	}
	if practical {
		recovery := execution.PracticalRecovery
		if recovery.HanTermPolicy.Version != evidenceingestion.EvidencePracticalHanTermPolicyVersionV1 ||
			recovery.HanTermPolicy.AuxiliaryTerms == nil || recovery.HanTermPolicy.EligibleAnchorTerms == nil {
			return fmt.Errorf("practical Han term policy is missing its version or term arrays")
		}
		want := 2
		if recovery.FallbackAttempted {
			want = 1
		}
		if recovery.ConfiguredMinimumEnglishTerms != want {
			return fmt.Errorf("practical recovery threshold does not match its attempt state")
		}
		if recovery.FallbackAttempted && (execution.Multisurface.Baseline.Truncated || execution.Multisurface.ExcludedSourceViews != 0) {
			return fmt.Errorf("practical recovery ran after an incomplete first search")
		}
	}
	for _, match := range result.Matches {
		if expanded != (match.RetrievalBasis != nil) {
			return fmt.Errorf("grounded brief retrieval basis and query mode do not match")
		}
		if expanded && !slices.Equal(match.RetrievalBasis.NormalizedQueryTerms, execution.Multisurface.NormalizedQueryTerms) {
			return fmt.Errorf("grounded brief retrieval basis query terms do not match execution")
		}
	}
	return nil
}

func mapGroundedEvidenceMultisurfaceExecution(input *evidenceingestion.EvidenceMultisurfaceExecution) *GroundedEvidenceMultisurfaceExecution {
	if input == nil {
		return nil
	}
	return &GroundedEvidenceMultisurfaceExecution{
		Baseline:             mapGroundedEvidenceQueryExecution(input.Baseline),
		NormalizedQueryTerms: append([]string{}, input.NormalizedQueryTerms...),
		SearchedSourceViews:  input.SearchedSourceViews,
		ExcludedSourceViews:  input.ExcludedSourceViews,
	}
}

func mapGroundedEvidenceRetrievalBasis(record evidenceingestion.ProposalQueryResult, basis *evidenceingestion.EvidenceRetrievalBasis) (*GroundedEvidenceRetrievalBasis, error) {
	if basis == nil {
		return nil, fmt.Errorf("multisurface result has no retrieval basis")
	}
	result := &GroundedEvidenceRetrievalBasis{
		NormalizedQueryTerms: append([]string{}, basis.NormalizedQueryTerms...),
		BaselineMatched:      basis.BaselineMatched,
		Surfaces:             make([]GroundedEvidenceRetrievalSurface, 0, len(basis.Surfaces)),
	}
	seen := map[string]bool{}
	for _, surface := range basis.Surfaces {
		if seen[surface.Surface] || !slices.Contains([]string{"searched", "over_budget", "not_applicable"}, surface.Status) {
			return nil, fmt.Errorf("multisurface result has duplicate surface or unknown status")
		}
		seen[surface.Surface] = true
		if err := validateRetrievalTermPartition(basis.NormalizedQueryTerms, surface.MatchedTerms, surface.MissingTerms); err != nil {
			return nil, err
		}
		if surface.Status != "searched" && (len(surface.MatchedTerms) != 0 || len(surface.MatchedSpans) != 0 || surface.SpansTruncated) {
			return nil, fmt.Errorf("unsearched surface reports matches")
		}
		switch surface.Surface {
		case evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement:
			if surface.Status != "searched" || surface.SourceSnapshotID != "" || surface.ExtractionViewID != "" ||
				surface.RawContentHash != "" || surface.RenderedContentHash != "" || len(surface.MatchedSpans) != 0 || surface.SpansTruncated {
				return nil, fmt.Errorf("statement retrieval basis includes source-only metadata")
			}
		case "extraction_views.rendered_content":
			if surface.Status == "searched" && (record.SourceSystem != evidenceingestion.SourceSystemManualText ||
				surface.SourceSnapshotID != record.SourceSnapshotID || surface.ExtractionViewID != record.ExtractionViewID ||
				surface.RawContentHash != record.RawContentHash || surface.RenderedContentHash != record.RenderedContentHash ||
				surface.RawContentHash == "" || surface.RawContentHash != surface.RenderedContentHash) {
				return nil, fmt.Errorf("source retrieval basis differs from the identity view")
			}
		default:
			return nil, fmt.Errorf("multisurface result contains an unsupported surface")
		}
		if len(surface.MatchedSpans) > 8 {
			return nil, fmt.Errorf("source retrieval excerpts exceed the v1 span limit")
		}
		mapped := GroundedEvidenceRetrievalSurface{
			Surface: surface.Surface, Status: surface.Status,
			MatchedTerms: append([]string{}, surface.MatchedTerms...), MissingTerms: append([]string{}, surface.MissingTerms...),
			SourceSnapshotID: surface.SourceSnapshotID, ExtractionViewID: surface.ExtractionViewID,
			RawContentHash: surface.RawContentHash, RenderedContentHash: surface.RenderedContentHash,
			MatchedSpans: []GroundedEvidenceRetrievalSpan{}, SpansTruncated: surface.SpansTruncated,
		}
		for _, span := range surface.MatchedSpans {
			ref := span.SourceRef
			if ref.ExtractionViewID != surface.ExtractionViewID || ref.SpanID == "" || ref.StartByte < 0 || ref.EndByte <= ref.StartByte ||
				int64(ref.EndByte) > evidenceingestion.BoundedSourceViewMaxRenderedBytesV1 || !utf8.ValidString(ref.QuotedText) ||
				len(ref.QuotedText) != ref.EndByte-ref.StartByte || int64(len(ref.QuotedText)) > evidenceingestion.BoundedSourceViewMaxSpanBytesV1 ||
				ref.QuotedTextHash != groundedEvidenceRepositoryTextHash(ref.QuotedText) {
				return nil, fmt.Errorf("source retrieval excerpt is not an exact bounded span")
			}
			if err := validateRetrievalMatchedTerms(surface.MatchedTerms, span.MatchedTerms); err != nil {
				return nil, err
			}
			within := false
			for _, original := range record.SourceRefs {
				if original.ExtractionViewID == ref.ExtractionViewID && original.StartByte <= ref.StartByte && original.EndByte >= ref.EndByte {
					within = true
					break
				}
			}
			if within != span.WithinProposalSourceRefs {
				return nil, fmt.Errorf("source retrieval excerpt incorrectly labels original citation membership")
			}
			mapped.MatchedSpans = append(mapped.MatchedSpans, GroundedEvidenceRetrievalSpan{
				SourceRef: ref, MatchedTerms: append([]string{}, span.MatchedTerms...), WithinProposalSourceRefs: within,
			})
		}
		result.Surfaces = append(result.Surfaces, mapped)
	}
	if !seen[evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement] || !seen["extraction_views.rendered_content"] {
		return nil, fmt.Errorf("multisurface result does not account for both search surfaces")
	}
	return result, nil
}

func validateRetrievalTermPartition(query, matched, missing []string) error {
	if err := validateRetrievalMatchedTerms(query, matched); err != nil {
		return err
	}
	if err := validateRetrievalMatchedTerms(query, missing); err != nil {
		return err
	}
	if len(matched)+len(missing) != len(query) {
		return fmt.Errorf("retrieval terms do not cover the normalized query")
	}
	for _, term := range matched {
		if slices.Contains(missing, term) {
			return fmt.Errorf("retrieval term is both matched and missing")
		}
	}
	return nil
}

func validateRetrievalMatchedTerms(query, matched []string) error {
	seen := map[string]bool{}
	for _, term := range matched {
		if term == "" || seen[term] || !slices.Contains(query, term) {
			return fmt.Errorf("retrieval terms contain an unknown or repeated term")
		}
		seen[term] = true
	}
	return nil
}
