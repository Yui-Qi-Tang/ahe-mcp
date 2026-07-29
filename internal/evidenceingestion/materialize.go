package evidenceingestion

import (
	"fmt"
	"slices"
)

type attemptContext struct {
	manualSourceContext
	ExtractorDefinition ExtractorDefinition
	ExtractionRun       ExtractionRun
	ExtractionAttempt   ExtractionAttempt
	ProposalBatch       ProposalBatch
}

type manualSourceContext struct {
	RequestID          string
	RequestPayloadHash string
	Raw                []byte
	SourceSnapshot     SourceSnapshot
	ExtractionView     ExtractionView
	SpanCatalogVersion string
	Spans              []SpanEntry
}

func buildManualSourceContext(input ManualTextInput) (manualSourceContext, error) {
	snapshot, view, spans, err := buildManualSource(input)
	if err != nil {
		return manualSourceContext{}, err
	}
	contract, err := renderingContract(snapshot.SourceSystem)
	if err != nil {
		return manualSourceContext{}, err
	}
	payloadHash, err := sourceIntakePayloadHash(input, snapshot, view)
	if err != nil {
		return manualSourceContext{}, err
	}
	return manualSourceContext{
		RequestID:          input.RequestID,
		RequestPayloadHash: payloadHash,
		Raw:                append([]byte(nil), input.Raw...),
		SourceSnapshot:     snapshot,
		ExtractionView:     view,
		SpanCatalogVersion: contract.SpanCatalogVersion,
		Spans:              spans,
	}, nil
}

func (ctx manualSourceContext) result(replayed bool) SourceIntakeResult {
	spans := append([]SpanEntry(nil), ctx.Spans...)
	return SourceIntakeResult{
		SourceSnapshotID:    ctx.SourceSnapshot.ID,
		ExtractionViewID:    ctx.ExtractionView.ID,
		RawContentHash:      ctx.SourceSnapshot.RawContentHash,
		RenderedContentHash: ctx.ExtractionView.RenderedContentHash,
		SourceSystem:        ctx.SourceSnapshot.SourceSystem,
		SourceID:            ctx.SourceSnapshot.SourceID,
		SourceVersion:       ctx.SourceSnapshot.SourceVersion,
		RendererName:        ctx.ExtractionView.RendererName,
		RendererVersion:     ctx.ExtractionView.RendererVersion,
		SpanCatalogVersion:  ctx.SpanCatalogVersion,
		Spans:               spans,
		Replayed:            replayed,
	}
}

func sourceIntakePayloadHash(input ManualTextInput, snapshot SourceSnapshot, view ExtractionView) (string, error) {
	contract, err := renderingContract(snapshot.SourceSystem)
	if err != nil {
		return "", err
	}
	data, err := deterministicJSON(struct {
		SourceSystem        string            `json:"source_system"`
		SourceID            string            `json:"source_id"`
		SourceVersion       string            `json:"source_version"`
		RawContentHash      string            `json:"raw_content_hash"`
		OriginMetadata      map[string]string `json:"origin_metadata"`
		RendererName        string            `json:"renderer_name"`
		RendererVersion     string            `json:"renderer_version"`
		RenderedContentHash string            `json:"rendered_content_hash"`
		SpanCatalogVersion  string            `json:"span_catalog_version"`
	}{
		SourceSystem:        snapshot.SourceSystem,
		SourceID:            input.SourceID,
		SourceVersion:       input.SourceVersion,
		RawContentHash:      snapshot.RawContentHash,
		OriginMetadata:      cloneStringMap(input.OriginMetadata),
		RendererName:        view.RendererName,
		RendererVersion:     view.RendererVersion,
		RenderedContentHash: view.RenderedContentHash,
		SpanCatalogVersion:  contract.SpanCatalogVersion,
	})
	if err != nil {
		return "", fmt.Errorf("serializing source intake payload identity: %w", err)
	}
	return contentHash(data), nil
}

func buildAttemptContext(input ManualTextInput) (attemptContext, error) {
	sourceCtx, err := buildManualSourceContext(input)
	if err != nil {
		return attemptContext{}, err
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID = sourceCtx.SourceSnapshot.ID
	}
	return buildAttemptContextFromSource(sourceCtx, requestID, input.AttemptNumber)
}

func buildAttemptContextFromSource(sourceCtx manualSourceContext, requestID string, attemptNumber int) (attemptContext, error) {
	return buildAttemptContextFromSourceWithDefinition(sourceCtx, requestID, attemptNumber, ExtractorDefinitionInput{})
}

func buildAttemptContextFromSourceWithDefinition(sourceCtx manualSourceContext, requestID string, attemptNumber int, definitionInput ExtractorDefinitionInput) (attemptContext, error) {
	definition, err := buildExtractorDefinition(definitionInput)
	if err != nil {
		return attemptContext{}, err
	}
	if requestID == "" {
		return attemptContext{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	if attemptNumber == 0 {
		attemptNumber = 1
	}
	if attemptNumber < 0 {
		return attemptContext{}, newDomainError(ErrorInvalidInput, "attempt_number must be positive")
	}
	runID, err := stableID("run:", "extraction_run", struct {
		RequestID             string `json:"request_id"`
		ExtractorDefinitionID string `json:"extractor_definition_id"`
		SourceSnapshotID      string `json:"source_snapshot_id"`
		ExtractionViewID      string `json:"extraction_view_id"`
	}{
		RequestID:             requestID,
		ExtractorDefinitionID: definition.ID,
		SourceSnapshotID:      sourceCtx.SourceSnapshot.ID,
		ExtractionViewID:      sourceCtx.ExtractionView.ID,
	})
	if err != nil {
		return attemptContext{}, err
	}
	run := ExtractionRun{
		ID:                    runID,
		RequestID:             requestID,
		ExtractorDefinitionID: definition.ID,
		SourceSnapshotID:      sourceCtx.SourceSnapshot.ID,
		ExtractionViewID:      sourceCtx.ExtractionView.ID,
	}
	attemptID, err := stableID("attempt:", "extraction_attempt", struct {
		ExtractionRunID string `json:"extraction_run_id"`
		AttemptNumber   int    `json:"attempt_number"`
	}{
		ExtractionRunID: run.ID,
		AttemptNumber:   attemptNumber,
	})
	if err != nil {
		return attemptContext{}, err
	}
	attempt := ExtractionAttempt{
		ID:     attemptID,
		RunID:  run.ID,
		Number: attemptNumber,
		Status: attemptStatusStarted,
	}
	batchID, err := stableID("batch:", "proposal_batch", struct {
		ExtractionAttemptID string `json:"extraction_attempt_id"`
		BatchIndex          int    `json:"batch_index"`
	}{
		ExtractionAttemptID: attempt.ID,
		BatchIndex:          1,
	})
	if err != nil {
		return attemptContext{}, err
	}
	return attemptContext{
		manualSourceContext: sourceCtx,
		ExtractorDefinition: definition,
		ExtractionRun:       run,
		ExtractionAttempt:   attempt,
		ProposalBatch: ProposalBatch{
			ID:                  batchID,
			ExtractionAttemptID: attempt.ID,
			Status:              batchStatusCompleted,
		},
	}, nil
}

func frozenExtractorDefinition() (ExtractorDefinition, error) {
	return buildExtractorDefinition(defaultFrozenExtractorDefinitionInput())
}

func defaultFrozenExtractorDefinitionInput() ExtractorDefinitionInput {
	return ExtractorDefinitionInput{
		Name:    ExtractorFrozenManualFixture,
		Version: ExtractorFrozenManualFixtureVersion,
		Config: map[string]string{
			"fixture_schema": "manual-text-proposal-output",
			"schema_version": "v1",
		},
	}
}

func buildExtractorDefinition(input ExtractorDefinitionInput) (ExtractorDefinition, error) {
	if input.Name == "" && input.Version == "" && len(input.Config) == 0 {
		input = defaultFrozenExtractorDefinitionInput()
	}
	if input.Name == "" {
		return ExtractorDefinition{}, newDomainError(ErrorInvalidInput, "extractor definition name is required")
	}
	if input.Version == "" {
		return ExtractorDefinition{}, newDomainError(ErrorInvalidInput, "extractor definition version is required")
	}
	config := cloneStringMap(input.Config)
	configData, err := deterministicJSON(config)
	if err != nil {
		return ExtractorDefinition{}, err
	}
	configHash := contentHash(configData)
	id, err := stableID("extractor:", "extractor_definition", struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		ConfigHash string `json:"config_hash"`
	}{
		Name:       input.Name,
		Version:    input.Version,
		ConfigHash: configHash,
	})
	if err != nil {
		return ExtractorDefinition{}, err
	}
	return ExtractorDefinition{
		ID:         id,
		Name:       input.Name,
		Version:    input.Version,
		ConfigHash: configHash,
		Config:     config,
	}, nil
}

func materializeBatch(ctx attemptContext, fixture FrozenExtractorOutput) (MaterializedBatch, error) {
	if fixture.RepositoryGoplsCoverage != nil {
		return MaterializedBatch{}, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage is not supported for source-view extraction")
	}
	if err := validateDocumentSectionCoverage(ctx, fixture); err != nil {
		return MaterializedBatch{}, err
	}
	spansByID := make(map[string]SpanEntry, len(ctx.Spans))
	for _, span := range ctx.Spans {
		if err := validateSpanBounds(ctx.ExtractionView, span); err != nil {
			return MaterializedBatch{}, err
		}
		spansByID[span.SpanID] = span
	}

	seenLocalIDs := map[string]bool{}
	occurrences := make([]ProposalOccurrence, 0, len(fixture.Proposals))
	var codeFacts map[codeFactSpan]CodeFactOutput
	codeFactsLoaded := false
	for _, proposal := range fixture.Proposals {
		if proposal.ProposalLocalID == "" {
			return MaterializedBatch{}, newDomainError(ErrorInvalidInput, "proposal_local_id is required")
		}
		if seenLocalIDs[proposal.ProposalLocalID] {
			return MaterializedBatch{}, newDomainError(ErrorDuplicateProposalLocalID, "duplicate proposal_local_id %q", proposal.ProposalLocalID)
		}
		seenLocalIDs[proposal.ProposalLocalID] = true
		if proposal.StatementText == "" {
			return MaterializedBatch{}, newDomainError(ErrorInvalidInput, "statement_text is required for %s", proposal.ProposalLocalID)
		}
		if proposal.CodeRelation != nil {
			return MaterializedBatch{}, newDomainError(ErrorInvalidExtractorOutput, "code_relation requires a repository snapshot input")
		}
		if len(proposal.EvidenceRefs) == 0 {
			return MaterializedBatch{}, newDomainError(ErrorUnknownSpan, "proposal %s requires at least one evidence ref", proposal.ProposalLocalID)
		}
		sourceRefs := make([]ResolvedSourceRef, 0, len(proposal.EvidenceRefs))
		for _, spanID := range proposal.EvidenceRefs {
			span, ok := spansByID[spanID]
			if !ok {
				return MaterializedBatch{}, newDomainError(ErrorUnknownSpan, "unknown span %q", spanID)
			}
			sourceRefs = append(sourceRefs, ResolvedSourceRef{
				ExtractionViewID: span.ExtractionViewID,
				SpanID:           span.SpanID,
				StartByte:        span.StartByte,
				EndByte:          span.EndByte,
				QuotedTextHash:   span.QuotedTextHash,
				QuotedText:       span.QuotedText,
			})
		}
		sortSourceRefs(sourceRefs)
		codeFact, err := resolveCodeFact(ctx, proposal, sourceRefs)
		if err != nil {
			return MaterializedBatch{}, err
		}
		if codeFact != nil {
			if !codeFactsLoaded {
				codeFacts, err = goCodeFactIndex(ctx)
				if err != nil {
					return MaterializedBatch{}, err
				}
				codeFactsLoaded = true
			}
			if err := verifyGoCodeFact(codeFacts, *codeFact); err != nil {
				return MaterializedBatch{}, err
			}
		}
		fingerprintVersion := ProposalFingerprintStatementV1
		fingerprint, err := proposalFingerprint(proposal.StatementText, sourceRefs)
		if codeFact != nil {
			fingerprintVersion = ProposalFingerprintCodeFactV1
			fingerprint, err = codeFactProposalFingerprint(proposal.StatementText, sourceRefs, *codeFact)
		}
		if err != nil {
			return MaterializedBatch{}, err
		}
		occurrenceID, err := stableID("occ:", "proposal_occurrence", struct {
			ExtractionAttemptID string `json:"extraction_attempt_id"`
			ProposalBatchID     string `json:"proposal_batch_id"`
			ProposalLocalID     string `json:"proposal_local_id"`
		}{
			ExtractionAttemptID: ctx.ExtractionAttempt.ID,
			ProposalBatchID:     ctx.ProposalBatch.ID,
			ProposalLocalID:     proposal.ProposalLocalID,
		})
		if err != nil {
			return MaterializedBatch{}, err
		}
		proposedPayload := map[string]any{
			"proposal_local_id": proposal.ProposalLocalID,
			"statement_text":    proposal.StatementText,
			"evidence_refs":     append([]string(nil), proposal.EvidenceRefs...),
		}
		if codeFact != nil {
			proposedPayload["code_fact"] = *codeFact
		}
		occurrences = append(occurrences, ProposalOccurrence{
			ID:                         occurrenceID,
			BatchID:                    ctx.ProposalBatch.ID,
			ExtractionAttemptID:        ctx.ExtractionAttempt.ID,
			ProposalLocalID:            proposal.ProposalLocalID,
			ProposalKind:               ProposalKindStatement,
			StatementText:              proposal.StatementText,
			ProposalFingerprint:        fingerprint,
			ProposalFingerprintVersion: fingerprintVersion,
			AdmissionOutcome:           admissionOutcomePending,
			SourceRefs:                 sourceRefs,
			CodeFact:                   codeFact,
			ProposedPayload:            proposedPayload,
		})
	}
	outputHash, err := extractorOutputHash(fixture)
	if err != nil {
		return MaterializedBatch{}, err
	}
	return MaterializedBatch{
		SourceSnapshot:      ctx.SourceSnapshot,
		ExtractionView:      ctx.ExtractionView,
		Spans:               ctx.Spans,
		ExtractorDefinition: ctx.ExtractorDefinition,
		ExtractionRun:       ctx.ExtractionRun,
		ExtractionAttempt:   ctx.ExtractionAttempt,
		ProposalBatch:       ctx.ProposalBatch,
		Occurrences:         occurrences,
		FixtureOutputHash:   outputHash,
	}, nil
}

func extractorOutputHash(fixture FrozenExtractorOutput) (string, error) {
	outputData, err := deterministicJSON(fixture)
	if err != nil {
		return "", fmt.Errorf("serializing fixture output: %w", err)
	}
	return contentHash(outputData), nil
}

func proposalFingerprint(statementText string, sourceRefs []ResolvedSourceRef) (string, error) {
	type fingerprintSourceRef struct {
		ExtractionViewID string `json:"extraction_view_id"`
		StartByte        int    `json:"start_byte"`
		EndByte          int    `json:"end_byte"`
		QuotedTextHash   string `json:"quoted_text_hash"`
	}
	refs := make([]fingerprintSourceRef, 0, len(sourceRefs))
	for _, ref := range sourceRefs {
		refs = append(refs, fingerprintSourceRef{
			ExtractionViewID: ref.ExtractionViewID,
			StartByte:        ref.StartByte,
			EndByte:          ref.EndByte,
			QuotedTextHash:   ref.QuotedTextHash,
		})
	}
	return stableID("fp:", "proposal_fingerprint", struct {
		FingerprintVersion string                 `json:"fingerprint_version"`
		ProposalKind       string                 `json:"proposal_kind"`
		StatementText      string                 `json:"statement_text"`
		SourceRefs         []fingerprintSourceRef `json:"source_refs"`
	}{
		FingerprintVersion: ProposalFingerprintStatementV1,
		ProposalKind:       ProposalKindStatement,
		StatementText:      statementText,
		SourceRefs:         refs,
	})
}

func codeFactProposalFingerprint(statementText string, sourceRefs []ResolvedSourceRef, codeFact ResolvedCodeFact) (string, error) {
	type fingerprintSourceRef struct {
		ExtractionViewID string `json:"extraction_view_id"`
		StartByte        int    `json:"start_byte"`
		EndByte          int    `json:"end_byte"`
		QuotedTextHash   string `json:"quoted_text_hash"`
	}
	refs := make([]fingerprintSourceRef, 0, len(sourceRefs))
	for _, ref := range sourceRefs {
		refs = append(refs, fingerprintSourceRef{
			ExtractionViewID: ref.ExtractionViewID,
			StartByte:        ref.StartByte,
			EndByte:          ref.EndByte,
			QuotedTextHash:   ref.QuotedTextHash,
		})
	}
	return stableID("fp:", "proposal_fingerprint", struct {
		FingerprintVersion string                 `json:"fingerprint_version"`
		ProposalKind       string                 `json:"proposal_kind"`
		StatementText      string                 `json:"statement_text"`
		SourceRefs         []fingerprintSourceRef `json:"source_refs"`
		CodeFact           ResolvedCodeFact       `json:"code_fact"`
	}{
		FingerprintVersion: ProposalFingerprintCodeFactV1,
		ProposalKind:       ProposalKindStatement,
		StatementText:      statementText,
		SourceRefs:         refs,
		CodeFact:           codeFact,
	})
}

func sortSourceRefs(refs []ResolvedSourceRef) {
	slices.SortFunc(refs, func(a, b ResolvedSourceRef) int {
		left := fmt.Sprintf("%s\x00%010d\x00%010d\x00%s", a.ExtractionViewID, a.StartByte, a.EndByte, a.QuotedTextHash)
		right := fmt.Sprintf("%s\x00%010d\x00%010d\x00%s", b.ExtractionViewID, b.StartByte, b.EndByte, b.QuotedTextHash)
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		default:
			return 0
		}
	})
}
