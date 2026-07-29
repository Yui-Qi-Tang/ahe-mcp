package evidenceingestion

import (
	"context"
	"fmt"
)

const (
	// GroundedEvidenceSourceContextContractV1 identifies post-retrieval source hydration.
	GroundedEvidenceSourceContextContractV1 = "post-retrieval-source-context-v1"

	// GroundedEvidenceSourceContextStatusAvailable means exact bounded context was derived.
	GroundedEvidenceSourceContextStatusAvailable GroundedEvidenceSourceContextStatus = "available"
	// GroundedEvidenceSourceContextStatusNotApplicable means the matched source surface is unsupported.
	GroundedEvidenceSourceContextStatusNotApplicable GroundedEvidenceSourceContextStatus = "not_applicable"
	// GroundedEvidenceSourceContextStatusSourceViewOverBudget means source payloads were withheld.
	GroundedEvidenceSourceContextStatusSourceViewOverBudget GroundedEvidenceSourceContextStatus = "source_view_over_budget"
)

const (
	sourceContextLimitationPostRetrievalOnly    = "context_does_not_participate_in_candidate_selection_or_ranking"
	sourceContextLimitationStructuralNotClaim   = "structural_units_are_source_context_not_evidence_claims"
	sourceContextLimitationManualSourceOnly     = "context_hydration_supports_manual_source_snapshots_only"
	sourceContextLimitationMissingExactRefs     = "search_core_has_no_exact_source_refs"
	sourceContextLimitationCrossesUnits         = "search_core_crosses_deterministic_source_units"
	sourceContextLimitationSourceViewOverBudget = "source_view_exceeds_bounded_hydration_budget"
)

// GroundedEvidenceSourceContextStatus is a closed post-retrieval hydration outcome.
type GroundedEvidenceSourceContextStatus string

// GroundedEvidenceSourceView reports the authoritative bounded loader outcome.
type GroundedEvidenceSourceView struct {
	Contract    string                       `json:"contract"`
	Status      BoundedSourceViewStatus      `json:"status"`
	LimitReason BoundedSourceViewLimitReason `json:"limit_reason,omitempty"`
	Observation BoundedSourceViewObservation `json:"observation"`
}

// GroundedEvidenceSourceUnit is one exact deterministic source container.
type GroundedEvidenceSourceUnit struct {
	UnitID     string                   `json:"unit_id"`
	UnitKind   SourceStructureUnitKind  `json:"unit_kind"`
	UnitClass  SourceStructureUnitClass `json:"unit_class"`
	ExactText  string                   `json:"exact_text"`
	SourceRefs []ResolvedSourceRef      `json:"source_refs"`
}

// GroundedEvidenceSourceContextRef resolves one compiler context ref to exact bytes.
type GroundedEvidenceSourceContextRef struct {
	Role      SourceStructureContextRole `json:"role"`
	SourceRef ResolvedSourceRef          `json:"source_ref"`
}

// GroundedEvidenceSourceContextEnvelope is exact context outside retrieval and ranking.
type GroundedEvidenceSourceContextEnvelope struct {
	Contract            string                             `json:"contract"`
	Refs                []GroundedEvidenceSourceContextRef `json:"refs"`
	ReferencedBytes     int                                `json:"referenced_bytes"`
	BudgetTruncated     bool                               `json:"budget_truncated"`
	OmittedOptionalRefs int                                `json:"omitted_optional_refs"`
}

// GroundedEvidenceSourceContext is a self-contained post-retrieval context package.
type GroundedEvidenceSourceContext struct {
	ProposalOccurrenceID string                                 `json:"-"`
	Contract             string                                 `json:"contract"`
	Status               GroundedEvidenceSourceContextStatus    `json:"status"`
	SearchParticipation  bool                                   `json:"search_participation"`
	SearchCore           []ResolvedSourceRef                    `json:"search_core"`
	SourceView           *GroundedEvidenceSourceView            `json:"source_view,omitempty"`
	AtomicContainer      *GroundedEvidenceSourceUnit            `json:"atomic_container,omitempty"`
	ContextEnvelope      *GroundedEvidenceSourceContextEnvelope `json:"context_envelope,omitempty"`
	Limitations          []string                               `json:"limitations"`
}

type groundedEvidenceSourceViewCache struct {
	loaded    BoundedSourceViewResult
	structure SourceStructure
}

func loadGroundedEvidenceSourceContexts(
	ctx context.Context,
	db sqlQueryer,
	matches []ProposalSearchResult,
) ([]GroundedEvidenceSourceContext, error) {
	contexts := make([]GroundedEvidenceSourceContext, 0, len(matches))
	cache := make(map[string]groundedEvidenceSourceViewCache)
	for _, match := range matches {
		record := match.Record
		sourceContext := GroundedEvidenceSourceContext{
			ProposalOccurrenceID: record.ProposalOccurrenceID,
			Contract:             GroundedEvidenceSourceContextContractV1,
			SearchParticipation:  false,
			SearchCore:           append([]ResolvedSourceRef(nil), record.SourceRefs...),
			Limitations:          groundedEvidenceSourceContextLimitations(),
		}
		if record.SourceBindingKind != ProposalSourceBindingSourceSnapshot ||
			record.SourceSystem != SourceSystemManualText {
			sourceContext.Status = GroundedEvidenceSourceContextStatusNotApplicable
			sourceContext.Limitations = append(
				sourceContext.Limitations,
				sourceContextLimitationManualSourceOnly,
			)
			contexts = append(contexts, sourceContext)
			continue
		}
		if len(record.SourceRefs) == 0 {
			sourceContext.Status = GroundedEvidenceSourceContextStatusNotApplicable
			sourceContext.Limitations = append(
				sourceContext.Limitations,
				sourceContextLimitationMissingExactRefs,
			)
			contexts = append(contexts, sourceContext)
			continue
		}
		if record.SourceSnapshotID == "" || record.ExtractionViewID == "" {
			return nil, newDomainError(
				ErrorOccurrenceConflict,
				"proposal occurrence %s has incomplete manual source identity",
				record.ProposalOccurrenceID,
			)
		}

		cacheKey := record.SourceSnapshotID + "\x00" + record.ExtractionViewID
		cached, ok := cache[cacheKey]
		if !ok {
			loaded, err := loadBoundedSourceViewFromQueryer(
				ctx,
				db,
				record.SourceSnapshotID,
				record.ExtractionViewID,
			)
			if err != nil {
				return nil, err
			}
			cached.loaded = loaded
			if loaded.Status == BoundedSourceViewStatusAvailable {
				if loaded.Input == nil {
					return nil, newDomainError(
						ErrorOccurrenceConflict,
						"available source view %s has no materialized input",
						record.ExtractionViewID,
					)
				}
				structure, err := CompileSourceStructure(*loaded.Input)
				if err != nil {
					return nil, err
				}
				cached.structure = structure
			}
			cache[cacheKey] = cached
		}

		sourceContext.SourceView = groundedEvidenceSourceView(cached.loaded)
		if cached.loaded.Status == BoundedSourceViewStatusOverBudget {
			sourceContext.Status = GroundedEvidenceSourceContextStatusSourceViewOverBudget
			sourceContext.Limitations = append(
				sourceContext.Limitations,
				sourceContextLimitationSourceViewOverBudget,
			)
			contexts = append(contexts, sourceContext)
			continue
		}
		if cached.loaded.Input == nil {
			return nil, newDomainError(
				ErrorOccurrenceConflict,
				"source view %s has no materialized input",
				record.ExtractionViewID,
			)
		}

		hydrated, applicable, err := hydrateGroundedEvidenceSourceContext(
			sourceContext,
			*cached.loaded.Input,
			cached.structure,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"hydrating proposal occurrence %s: %w",
				record.ProposalOccurrenceID,
				err,
			)
		}
		if !applicable {
			hydrated.Status = GroundedEvidenceSourceContextStatusNotApplicable
			hydrated.Limitations = append(
				hydrated.Limitations,
				sourceContextLimitationCrossesUnits,
			)
		}
		contexts = append(contexts, hydrated)
	}
	return contexts, nil
}

func hydrateGroundedEvidenceSourceContext(
	sourceContext GroundedEvidenceSourceContext,
	input ExtractorInput,
	structure SourceStructure,
) (GroundedEvidenceSourceContext, bool, error) {
	spansByID := make(map[string]ExtractorInputSpan, len(input.Spans))
	for _, span := range input.Spans {
		spansByID[span.SpanID] = span
	}
	var selected SourceStructureUnit
	selectedSet := false
	seenSearchCore := make(map[string]struct{}, len(sourceContext.SearchCore))
	for _, ref := range sourceContext.SearchCore {
		if _, duplicate := seenSearchCore[ref.SpanID]; duplicate {
			return GroundedEvidenceSourceContext{}, false, newDomainError(
				ErrorOccurrenceConflict,
				"search core repeats span %s",
				ref.SpanID,
			)
		}
		seenSearchCore[ref.SpanID] = struct{}{}
		span, ok := spansByID[ref.SpanID]
		if !ok {
			return GroundedEvidenceSourceContext{}, false, newDomainError(
				ErrorOccurrenceConflict,
				"search core references unknown span %s",
				ref.SpanID,
			)
		}
		if ref != resolvedExtractorInputSpan(input.ExtractionViewID, span) {
			return GroundedEvidenceSourceContext{}, false, newDomainError(
				ErrorOccurrenceConflict,
				"search core span %s differs from the persisted source view",
				ref.SpanID,
			)
		}
		unit, ok := structure.UnitForSpan(ref.SpanID)
		if !ok {
			return GroundedEvidenceSourceContext{}, false, newDomainError(
				ErrorOccurrenceConflict,
				"search core span %s has no deterministic source unit",
				ref.SpanID,
			)
		}
		if !selectedSet {
			selected = unit
			selectedSet = true
			continue
		}
		if selected.ID != unit.ID {
			return sourceContext, false, nil
		}
	}
	if !selectedSet {
		return sourceContext, false, nil
	}

	containerRefs, err := resolveSourceStructureSpanIDs(
		input.ExtractionViewID,
		spansByID,
		selected.CoreSpanIDs,
	)
	if err != nil {
		return GroundedEvidenceSourceContext{}, false, err
	}
	contextRefs := make([]GroundedEvidenceSourceContextRef, 0, len(selected.Context.Refs))
	for _, ref := range selected.Context.Refs {
		span, ok := spansByID[ref.SpanID]
		if !ok {
			return GroundedEvidenceSourceContext{}, false, newDomainError(
				ErrorOccurrenceConflict,
				"source unit %s context references unknown span %s",
				selected.ID,
				ref.SpanID,
			)
		}
		contextRefs = append(contextRefs, GroundedEvidenceSourceContextRef{
			Role:      ref.Role,
			SourceRef: resolvedExtractorInputSpan(input.ExtractionViewID, span),
		})
	}
	sourceContext.Status = GroundedEvidenceSourceContextStatusAvailable
	sourceContext.AtomicContainer = &GroundedEvidenceSourceUnit{
		UnitID:     selected.ID,
		UnitKind:   selected.Kind,
		UnitClass:  selected.Class,
		ExactText:  selected.ExactText,
		SourceRefs: containerRefs,
	}
	sourceContext.ContextEnvelope = &GroundedEvidenceSourceContextEnvelope{
		Contract:            selected.Context.Contract,
		Refs:                contextRefs,
		ReferencedBytes:     selected.Context.ReferencedBytes,
		BudgetTruncated:     selected.Context.BudgetTruncated,
		OmittedOptionalRefs: selected.Context.OmittedOptionalRefs,
	}
	return sourceContext, true, nil
}

func resolveSourceStructureSpanIDs(
	extractionViewID string,
	spansByID map[string]ExtractorInputSpan,
	spanIDs []string,
) ([]ResolvedSourceRef, error) {
	refs := make([]ResolvedSourceRef, 0, len(spanIDs))
	for _, spanID := range spanIDs {
		span, ok := spansByID[spanID]
		if !ok {
			return nil, newDomainError(
				ErrorOccurrenceConflict,
				"source unit references unknown span %s",
				spanID,
			)
		}
		refs = append(refs, resolvedExtractorInputSpan(extractionViewID, span))
	}
	return refs, nil
}

func resolvedExtractorInputSpan(
	extractionViewID string,
	span ExtractorInputSpan,
) ResolvedSourceRef {
	return ResolvedSourceRef{
		ExtractionViewID: extractionViewID,
		SpanID:           span.SpanID,
		StartByte:        span.StartByte,
		EndByte:          span.EndByte,
		QuotedTextHash:   span.QuotedTextHash,
		QuotedText:       span.Text,
	}
}

func groundedEvidenceSourceView(
	result BoundedSourceViewResult,
) *GroundedEvidenceSourceView {
	return &GroundedEvidenceSourceView{
		Contract:    result.Contract,
		Status:      result.Status,
		LimitReason: result.LimitReason,
		Observation: result.Observation,
	}
}

func groundedEvidenceSourceContextLimitations() []string {
	return []string{
		sourceContextLimitationPostRetrievalOnly,
		sourceContextLimitationStructuralNotClaim,
	}
}
