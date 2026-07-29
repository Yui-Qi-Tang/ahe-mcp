package evidenceingestion

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestLoadGroundedEvidenceSourceContextsHydratesExactUnitsAndCachesView(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "query-source-context",
		SourceVersion: "v1",
		Raw: []byte(
			"# Runtime\n\n" +
				"Failed work remains fenced.\n" +
				"An explicit retry creates a new attempt.\n\n" +
				"- Preserve the failed attempt.\n",
		),
		RequestID: "query-source-context",
	})
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	firstRef := resolvedSpanEntry(source.Spans[1])
	secondRef := resolvedSpanEntry(source.Spans[2])
	matches := []ProposalSearchResult{
		{Record: sourceContextQueryRecord("occ:first", source, []ResolvedSourceRef{firstRef})},
		{Record: sourceContextQueryRecord("occ:second", source, []ResolvedSourceRef{secondRef})},
	}

	contexts, err := loadGroundedEvidenceSourceContexts(ctx, db, matches)
	if err != nil {
		t.Fatalf("loadGroundedEvidenceSourceContexts() error = %v", err)
	}
	if len(contexts) != 2 {
		t.Fatalf("context count = %d, want 2", len(contexts))
	}
	for index, sourceContext := range contexts {
		if sourceContext.Status != GroundedEvidenceSourceContextStatusAvailable ||
			sourceContext.SearchParticipation ||
			sourceContext.SourceView == nil ||
			sourceContext.SourceView.Status != BoundedSourceViewStatusAvailable ||
			sourceContext.AtomicContainer == nil ||
			sourceContext.ContextEnvelope == nil {
			t.Fatalf("context %d = %+v", index, sourceContext)
		}
		if sourceContext.AtomicContainer.UnitKind != SourceStructureUnitParagraph ||
			sourceContext.AtomicContainer.ExactText !=
				"Failed work remains fenced.\nAn explicit retry creates a new attempt." ||
			len(sourceContext.AtomicContainer.SourceRefs) != 2 {
			t.Fatalf("context %d atomic container = %+v", index, sourceContext.AtomicContainer)
		}
		if !sourceContextEnvelopeHasRole(
			*sourceContext.ContextEnvelope,
			SourceStructureContextHeading,
		) {
			t.Fatalf("context %d envelope = %+v", index, sourceContext.ContextEnvelope)
		}
		if !reflect.DeepEqual(sourceContext.SearchCore, matches[index].Record.SourceRefs) {
			t.Fatalf(
				"context %d search core = %+v, want %+v",
				index,
				sourceContext.SearchCore,
				matches[index].Record.SourceRefs,
			)
		}
	}
	if contexts[0].AtomicContainer.UnitID != contexts[1].AtomicContainer.UnitID {
		t.Fatalf(
			"paragraph unit IDs differ: %s / %s",
			contexts[0].AtomicContainer.UnitID,
			contexts[1].AtomicContainer.UnitID,
		)
	}
	if db.boundedViewPreflights != 1 || db.sourceViewLoads != 1 {
		t.Fatalf(
			"source view queries = preflight %d/full %d, want 1/1",
			db.boundedViewPreflights,
			db.sourceViewLoads,
		)
	}
}

func TestLoadGroundedEvidenceSourceContextsMarksCrossUnitCoreNotApplicable(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "query-source-context-cross-unit",
		SourceVersion: "v1",
		Raw: []byte(
			"# Runtime\n\n" +
				"Paragraph evidence.\n\n" +
				"- Separate list evidence.\n",
		),
		RequestID: "query-source-context-cross-unit",
	})
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	record := sourceContextQueryRecord(
		"occ:cross-unit",
		source,
		[]ResolvedSourceRef{
			resolvedSpanEntry(source.Spans[1]),
			resolvedSpanEntry(source.Spans[2]),
		},
	)

	contexts, err := loadGroundedEvidenceSourceContexts(
		ctx,
		db,
		[]ProposalSearchResult{{Record: record}},
	)
	if err != nil {
		t.Fatalf("loadGroundedEvidenceSourceContexts() error = %v", err)
	}
	if len(contexts) != 1 ||
		contexts[0].Status != GroundedEvidenceSourceContextStatusNotApplicable ||
		contexts[0].SourceView == nil ||
		contexts[0].AtomicContainer != nil ||
		contexts[0].ContextEnvelope != nil ||
		!sourceContextHasLimitation(contexts[0], sourceContextLimitationCrossesUnits) {
		t.Fatalf("cross-unit context = %+v", contexts)
	}
}

func TestLoadGroundedEvidenceSourceContextsWithholdsOverBudgetView(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "query-source-context-over-budget",
		SourceVersion: "v1",
		Raw: []byte(
			strings.Repeat("x", int(BoundedSourceViewMaxSpanBytesV1)+1) + "\n",
		),
		RequestID: "query-source-context-over-budget",
	})
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	record := sourceContextQueryRecord(
		"occ:over-budget",
		source,
		[]ResolvedSourceRef{resolvedSpanEntry(source.Spans[0])},
	)

	contexts, err := loadGroundedEvidenceSourceContexts(
		ctx,
		db,
		[]ProposalSearchResult{{Record: record}},
	)
	if err != nil {
		t.Fatalf("loadGroundedEvidenceSourceContexts() error = %v", err)
	}
	if len(contexts) != 1 ||
		contexts[0].Status != GroundedEvidenceSourceContextStatusSourceViewOverBudget ||
		contexts[0].SourceView == nil ||
		contexts[0].SourceView.LimitReason != BoundedSourceViewLimitAtomicSpan ||
		contexts[0].AtomicContainer != nil ||
		contexts[0].ContextEnvelope != nil ||
		!sourceContextHasLimitation(
			contexts[0],
			sourceContextLimitationSourceViewOverBudget,
		) {
		t.Fatalf("over-budget context = %+v", contexts)
	}
	if db.boundedViewPreflights != 1 || db.sourceViewLoads != 0 {
		t.Fatalf(
			"source view queries = preflight %d/full %d, want 1/0",
			db.boundedViewPreflights,
			db.sourceViewLoads,
		)
	}
}

func TestLoadGroundedEvidenceSourceContextsMarksUnsupportedSurfaceNotApplicable(t *testing.T) {
	db := newMockSQLDB()
	contexts, err := loadGroundedEvidenceSourceContexts(
		context.Background(),
		db,
		[]ProposalSearchResult{{Record: ProposalQueryResult{
			ProposalOccurrenceID: "occ:repository",
			SourceBindingKind:    ProposalSourceBindingRepositorySnapshot,
			SourceSystem:         SourceSystemCodeFile,
			SourceRefs: []ResolvedSourceRef{{
				RepositorySnapshotID: "repo-snapshot:test",
				FileSnapshotID:       "filesnap:test",
				SpanID:               "span:S1",
			}},
		}}},
	)
	if err != nil {
		t.Fatalf("loadGroundedEvidenceSourceContexts() error = %v", err)
	}
	if len(contexts) != 1 ||
		contexts[0].Status != GroundedEvidenceSourceContextStatusNotApplicable ||
		contexts[0].SourceView != nil ||
		!sourceContextHasLimitation(contexts[0], sourceContextLimitationManualSourceOnly) {
		t.Fatalf("unsupported context = %+v", contexts)
	}
	if db.boundedViewPreflights != 0 || db.sourceViewLoads != 0 {
		t.Fatalf(
			"unsupported surface queried source view: preflight %d/full %d",
			db.boundedViewPreflights,
			db.sourceViewLoads,
		)
	}
}

func TestLoadGroundedEvidenceSourceContextsFailsClosedOnSourceRefDrift(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("query-source-context-drift"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	ref := resolvedSpanEntry(source.Spans[0])
	ref.QuotedText = "drifted"
	record := sourceContextQueryRecord(
		"occ:drift",
		source,
		[]ResolvedSourceRef{ref},
	)

	_, err = loadGroundedEvidenceSourceContexts(
		ctx,
		db,
		[]ProposalSearchResult{{Record: record}},
	)
	assertKind(t, err, ErrorOccurrenceConflict)
}

func sourceContextQueryRecord(
	occurrenceID string,
	source SourceIntakeResult,
	refs []ResolvedSourceRef,
) ProposalQueryResult {
	return ProposalQueryResult{
		ProposalOccurrenceID: occurrenceID,
		SourceBindingKind:    ProposalSourceBindingSourceSnapshot,
		ExtractionViewID:     source.ExtractionViewID,
		RendererName:         source.RendererName,
		RendererVersion:      source.RendererVersion,
		RenderedContentHash:  source.RenderedContentHash,
		SourceSnapshotID:     source.SourceSnapshotID,
		SourceSystem:         source.SourceSystem,
		SourceID:             source.SourceID,
		SourceVersion:        source.SourceVersion,
		RawContentHash:       source.RawContentHash,
		SourceRefs:           refs,
	}
}

func resolvedSpanEntry(span SpanEntry) ResolvedSourceRef {
	return ResolvedSourceRef{
		ExtractionViewID: span.ExtractionViewID,
		SpanID:           span.SpanID,
		StartByte:        span.StartByte,
		EndByte:          span.EndByte,
		QuotedTextHash:   span.QuotedTextHash,
		QuotedText:       span.QuotedText,
	}
}

func sourceContextEnvelopeHasRole(
	envelope GroundedEvidenceSourceContextEnvelope,
	role SourceStructureContextRole,
) bool {
	for _, ref := range envelope.Refs {
		if ref.Role == role {
			return true
		}
	}
	return false
}

func sourceContextHasLimitation(
	sourceContext GroundedEvidenceSourceContext,
	limitation string,
) bool {
	for _, item := range sourceContext.Limitations {
		if item == limitation {
			return true
		}
	}
	return false
}
