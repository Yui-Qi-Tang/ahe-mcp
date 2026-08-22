package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ExtractorRepositoryGoParserCodeFact identifies repository-wide deterministic declaration extraction.
	ExtractorRepositoryGoParserCodeFact = "repository-go-parser-code-fact"
	// ExtractorRepositoryGoParserCodeFactVersion is the first repository batch contract.
	ExtractorRepositoryGoParserCodeFactVersion = "v1"
)

type repositoryAttemptContext struct {
	input               RepositoryExtractorInput
	extractorDefinition ExtractorDefinition
	extractionRun       ExtractionRun
	extractionAttempt   ExtractionAttempt
	proposalBatch       ProposalBatch
}

// RunRepositoryGoParserExtractor extracts and persists declaration proposals from one repository snapshot.
func RunRepositoryGoParserExtractor(ctx context.Context, pool *pgxpool.Pool, request RepositoryGoParserRequest) (RepositoryIngestResult, error) {
	if pool == nil {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return runRepositoryGoParserExtractor(ctx, pgxDB{pool: pool}, request)
}

// RunRepositoryGoParserDeltaExtractor runs the allowlisted parser delta adapter with full-snapshot verification.
func RunRepositoryGoParserDeltaExtractor(ctx context.Context, pool *pgxpool.Pool, request RepositoryGoParserRequest) (RepositoryIngestResult, error) {
	if pool == nil {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return runRepositoryGoParserDeltaExtractor(ctx, pgxDB{pool: pool}, request)
}

func runRepositoryGoParserExtractor(ctx context.Context, db sqlDB, request RepositoryGoParserRequest) (RepositoryIngestResult, error) {
	if request.RequestID == "" {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	input, err := buildRepositoryExtractorInput(ctx, db, request.RepositorySnapshotID)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	return runRepositoryExtractor(
		ctx,
		db,
		input,
		request.RequestID,
		request.RetryFailedAttempt,
		repositoryGoParserExtractorDefinition(),
		extractRepositoryGoParser,
	)
}

type repositoryExtractor func(context.Context, RepositoryExtractorInput) (FrozenExtractorOutput, error)

type repositoryExtractorExecution struct {
	output          FrozenExtractorOutput
	deltaExtraction *RepositoryDeltaExtraction
}

type repositoryExtractorExecutionFunc func(context.Context, RepositoryExtractorInput) (repositoryExtractorExecution, error)

type repositoryBatchMaterializationHook func(
	repositoryAttemptContext,
	FrozenExtractorOutput,
	MaterializedBatch,
) error

func runRepositoryExtractor(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	requestID string,
	retryFailedAttempt bool,
	definitionInput ExtractorDefinitionInput,
	extract repositoryExtractor,
) (RepositoryIngestResult, error) {
	return runRepositoryExtractorObserved(
		ctx,
		db,
		input,
		requestID,
		retryFailedAttempt,
		definitionInput,
		extract,
		nil,
	)
}

func runRepositoryExtractorObserved(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	requestID string,
	retryFailedAttempt bool,
	definitionInput ExtractorDefinitionInput,
	extract repositoryExtractor,
	observer repositoryExtractionPhaseObserver,
) (RepositoryIngestResult, error) {
	return runRepositoryExtractorProfiled(
		ctx,
		db,
		input,
		requestID,
		retryFailedAttempt,
		definitionInput,
		extract,
		observer,
		nil,
	)
}

func runRepositoryExtractorProfiled(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	requestID string,
	retryFailedAttempt bool,
	definitionInput ExtractorDefinitionInput,
	extract repositoryExtractor,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
) (RepositoryIngestResult, error) {
	return runRepositoryExtractorOccurrenceProfiled(
		ctx,
		db,
		input,
		requestID,
		retryFailedAttempt,
		definitionInput,
		extract,
		observer,
		subphaseObserver,
		nil,
	)
}

func runRepositoryExtractorOccurrenceProfiled(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	requestID string,
	retryFailedAttempt bool,
	definitionInput ExtractorDefinitionInput,
	extract repositoryExtractor,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
) (RepositoryIngestResult, error) {
	return runRepositoryExtractorMaterializationCompared(
		ctx,
		db,
		input,
		requestID,
		retryFailedAttempt,
		definitionInput,
		extract,
		observer,
		subphaseObserver,
		occurrenceObserver,
		nil,
	)
}

func runRepositoryExtractorMaterializationCompared(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	requestID string,
	retryFailedAttempt bool,
	definitionInput ExtractorDefinitionInput,
	extract repositoryExtractor,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
	batchHook repositoryBatchMaterializationHook,
) (RepositoryIngestResult, error) {
	if extract == nil {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "repository extractor is required")
	}
	return runRepositoryExtractorExecution(
		ctx,
		db,
		input,
		requestID,
		retryFailedAttempt,
		definitionInput,
		false,
		observer,
		subphaseObserver,
		occurrenceObserver,
		batchHook,
		func(ctx context.Context, input RepositoryExtractorInput) (repositoryExtractorExecution, error) {
			output, err := extract(ctx, input)
			return repositoryExtractorExecution{output: output}, err
		},
	)
}

func runRepositoryExtractorExecution(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	requestID string,
	retryFailedAttempt bool,
	definitionInput ExtractorDefinitionInput,
	loadDeltaExtraction bool,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
	batchHook repositoryBatchMaterializationHook,
	extract repositoryExtractorExecutionFunc,
) (RepositoryIngestResult, error) {
	if extract == nil {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "repository extractor is required")
	}
	started := startRepositoryExtractionPhase(observer)
	runResult, err := createRepositoryExtractionRun(ctx, db, RepositoryExtractionRunRequest{
		RequestID:            requestID,
		RepositorySnapshotID: input.RepositorySnapshot.ID,
		ExtractorDefinition:  definitionInput,
	})
	if err != nil {
		finishRepositoryExtractionPhase(observer, repositoryExtractionPhasePrepareAttempt, started, err)
		return RepositoryIngestResult{}, err
	}
	definition, err := buildExtractorDefinition(definitionInput)
	if err != nil {
		finishRepositoryExtractionPhase(observer, repositoryExtractionPhasePrepareAttempt, started, err)
		return RepositoryIngestResult{}, err
	}
	attemptCtx, status, err := startRepositoryAttempt(ctx, db, input, definition, runResult.ExtractionRun, retryFailedAttempt)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhasePrepareAttempt, started, err)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	switch status {
	case attemptStatusSucceeded:
		return replayCompletedRepositoryAttempt(ctx, db, attemptCtx, loadDeltaExtraction)
	case attemptStatusFailed:
		return RepositoryIngestResult{}, newDomainError(ErrorPersistedAttemptFailed, "attempt %s already failed", attemptCtx.extractionAttempt.ID)
	}

	execution, err := extract(ctx, input)
	output := execution.output
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = classifyRunnerError(ctx, err)
		}
		return RepositoryIngestResult{}, failRepositoryAttempt(ctx, db, attemptCtx, output, err)
	}
	started = startRepositoryExtractionPhase(observer)
	batch, err := materializeRepositoryBatchOccurrenceProfiled(
		attemptCtx,
		output,
		subphaseObserver,
		occurrenceObserver,
	)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhaseBatchMaterialization, started, err)
	if err != nil {
		return RepositoryIngestResult{}, failRepositoryAttempt(ctx, db, attemptCtx, output, err)
	}
	if batchHook != nil {
		if err := batchHook(attemptCtx, output, batch); err != nil {
			return RepositoryIngestResult{}, failRepositoryAttempt(ctx, db, attemptCtx, output, err)
		}
	}
	var hook proposalSuccessHook
	if execution.deltaExtraction != nil {
		delta, err := bindRepositoryDeltaExtraction(attemptCtx, batch, *execution.deltaExtraction)
		if err != nil {
			return RepositoryIngestResult{}, failRepositoryAttempt(ctx, db, attemptCtx, output, err)
		}
		execution.deltaExtraction = &delta
		hook = func(ctx context.Context, tx sqlTx) error {
			return persistRepositoryDeltaExtraction(ctx, tx, delta)
		}
	}
	started = startRepositoryExtractionPhase(observer)
	replayed, err := persistProposalSuccessWithHook(ctx, db, batch, output, hook)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhasePersistSuccess, started, err)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	if replayed {
		return replayCompletedRepositoryAttempt(ctx, db, attemptCtx, loadDeltaExtraction)
	}
	result := repositoryIngestResult(batch, false)
	result.DeltaExtraction = execution.deltaExtraction
	started = startRepositoryExtractionPhase(observer)
	result, err = attachRepositorySourceGeneration(ctx, db, result)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhaseCreateGeneration, started, err)
	return result, err
}

func replayCompletedRepositoryAttempt(ctx context.Context, db sqlDB, attemptCtx repositoryAttemptContext, loadDeltaExtraction bool) (RepositoryIngestResult, error) {
	result, err := replayRepositoryAttempt(ctx, db, attemptCtx)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	if loadDeltaExtraction {
		delta, found, err := readRepositoryDeltaExtraction(ctx, db, attemptCtx.extractionAttempt.ID)
		if err != nil {
			return RepositoryIngestResult{}, err
		}
		if found {
			if err := validateRepositoryDeltaExtraction(attemptCtx, result, delta); err != nil {
				return RepositoryIngestResult{}, err
			}
			delta.Replayed = true
			result.DeltaExtraction = &delta
		}
	}
	result.Replayed = true
	return attachRepositorySourceGeneration(ctx, db, result)
}

func attachRepositorySourceGeneration(ctx context.Context, db sqlDB, result RepositoryIngestResult) (RepositoryIngestResult, error) {
	generation, err := createRepositorySourceGeneration(ctx, db, RepositorySourceGenerationCreateInput{ProposalBatchID: result.ProposalBatchID})
	if err != nil {
		return RepositoryIngestResult{}, fmt.Errorf("creating repository source generation: %w", err)
	}
	result.SourceGeneration = generation.Generation
	result.SourceGenerationReplayed = generation.Replayed
	return result, nil
}

func repositoryGoParserExtractorDefinition() ExtractorDefinitionInput {
	return ExtractorDefinitionInput{
		Name:    ExtractorRepositoryGoParserCodeFact,
		Version: ExtractorRepositoryGoParserCodeFactVersion,
		Config: map[string]string{
			"input_contract": "repository-snapshot-tracked-go-files-v1",
			"output_schema":  CodeFactSchemaV1,
			"proposal_kind":  ProposalKindStatement,
			"span_contract":  SpanCatalogCodeLineV1,
			"topology_scope": "declarations_only",
		},
	}
}

func startRepositoryAttempt(
	ctx context.Context,
	db sqlDB,
	input RepositoryExtractorInput,
	definition ExtractorDefinition,
	run ExtractionRun,
	retryFailed bool,
) (repositoryAttemptContext, string, error) {
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		return repositoryAttemptContext{}, "", err
	}
	status, err := persistRepositoryAttemptStart(ctx, db, attemptCtx)
	if err != nil {
		return repositoryAttemptContext{}, "", err
	}
	if status != attemptStatusFailed {
		return attemptCtx, status, nil
	}
	succeededNumber, ok, err := succeededAttemptNumber(ctx, db, run.ID)
	if err != nil {
		return repositoryAttemptContext{}, "", err
	}
	if ok {
		attemptCtx, err = buildRepositoryAttemptContext(input, definition, run, succeededNumber)
		return attemptCtx, attemptStatusSucceeded, err
	}
	if !retryFailed {
		return attemptCtx, status, nil
	}
	nextNumber, err := nextAttemptNumber(ctx, db, run.ID)
	if err != nil {
		return repositoryAttemptContext{}, "", err
	}
	attemptCtx, err = buildRepositoryAttemptContext(input, definition, run, nextNumber)
	if err != nil {
		return repositoryAttemptContext{}, "", err
	}
	status, err = persistRepositoryAttemptStart(ctx, db, attemptCtx)
	return attemptCtx, status, err
}

func buildRepositoryAttemptContext(input RepositoryExtractorInput, definition ExtractorDefinition, run ExtractionRun, attemptNumber int) (repositoryAttemptContext, error) {
	if attemptNumber <= 0 {
		return repositoryAttemptContext{}, newDomainError(ErrorInvalidInput, "attempt_number must be positive")
	}
	if run.RepositorySnapshotID != input.RepositorySnapshot.ID || run.ExtractorDefinitionID != definition.ID {
		return repositoryAttemptContext{}, newDomainError(ErrorRepositorySnapshotIntegrity, "extraction run %s does not match repository input and extractor definition", run.ID)
	}
	attemptID, err := stableID("attempt:", "extraction_attempt", struct {
		ExtractionRunID string `json:"extraction_run_id"`
		AttemptNumber   int    `json:"attempt_number"`
	}{ExtractionRunID: run.ID, AttemptNumber: attemptNumber})
	if err != nil {
		return repositoryAttemptContext{}, err
	}
	batchID, err := stableID("batch:", "proposal_batch", struct {
		ExtractionAttemptID string `json:"extraction_attempt_id"`
		BatchIndex          int    `json:"batch_index"`
	}{ExtractionAttemptID: attemptID, BatchIndex: 1})
	if err != nil {
		return repositoryAttemptContext{}, err
	}
	return repositoryAttemptContext{
		input:               input,
		extractorDefinition: definition,
		extractionRun:       run,
		extractionAttempt: ExtractionAttempt{
			ID:     attemptID,
			RunID:  run.ID,
			Number: attemptNumber,
			Status: attemptStatusStarted,
		},
		proposalBatch: ProposalBatch{ID: batchID, ExtractionAttemptID: attemptID, Status: batchStatusCompleted},
	}, nil
}

func persistRepositoryAttemptStart(ctx context.Context, db sqlDB, attemptCtx repositoryAttemptContext) (string, error) {
	err := withTx(ctx, db, func(tx sqlTx) error {
		_, err := tx.exec(ctx, `
			INSERT INTO extraction_attempts (
				extraction_attempt_id, extraction_run_id, attempt_number, status
			)
			VALUES ($1, $2, $3, 'started')
			ON CONFLICT DO NOTHING
		`, attemptCtx.extractionAttempt.ID, attemptCtx.extractionRun.ID, attemptCtx.extractionAttempt.Number)
		if err != nil {
			return fmt.Errorf("upserting repository extraction attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	var status string
	err = db.queryRow(ctx, `SELECT status FROM extraction_attempts WHERE extraction_attempt_id = $1`, attemptCtx.extractionAttempt.ID).Scan(&status)
	if err != nil {
		return "", fmt.Errorf("reading repository extraction attempt: %w", err)
	}
	return status, nil
}

func extractRepositoryGoParser(ctx context.Context, input RepositoryExtractorInput) (FrozenExtractorOutput, error) {
	proposals := make([]ExtractorProposalOutput, 0)
	for _, file := range input.Files {
		if err := ctx.Err(); err != nil {
			return FrozenExtractorOutput{}, err
		}
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      file.FileSnapshot.Path,
			Source:    file.Content,
		})
		if err != nil {
			return FrozenExtractorOutput{}, err
		}
		for _, proposal := range extraction.Output.Proposals {
			qualified, err := qualifyRepositoryProposalLocalID(file.FileSnapshot.Path, proposal)
			if err != nil {
				return FrozenExtractorOutput{}, err
			}
			proposals = append(proposals, qualified)
		}
	}
	return FrozenExtractorOutput{Proposals: proposals}, nil
}

func qualifyRepositoryProposalLocalID(path string, proposal ExtractorProposalOutput) (ExtractorProposalOutput, error) {
	localID, err := stableID("local:", "repository_proposal_local", struct {
		Path            string `json:"path"`
		ProposalLocalID string `json:"proposal_local_id"`
	}{Path: path, ProposalLocalID: proposal.ProposalLocalID})
	if err != nil {
		return ExtractorProposalOutput{}, err
	}
	proposal.ProposalLocalID = localID
	return proposal, nil
}

func materializeRepositoryBatch(ctx repositoryAttemptContext, output FrozenExtractorOutput) (MaterializedBatch, error) {
	return materializeRepositoryBatchProfiled(ctx, output, nil)
}

func materializeRepositoryBatchProfiled(
	ctx repositoryAttemptContext,
	output FrozenExtractorOutput,
	observer repositoryExtractionSubphaseObserver,
) (MaterializedBatch, error) {
	return materializeRepositoryBatchOccurrenceProfiled(ctx, output, observer, nil)
}

func materializeRepositoryBatchOccurrenceProfiled(
	ctx repositoryAttemptContext,
	output FrozenExtractorOutput,
	observer repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
) (MaterializedBatch, error) {
	return materializeRepositoryBatchGrounded(
		ctx,
		output,
		observer,
		occurrenceObserver,
		true,
	)
}

func materializeRepositoryBatchLegacy(
	ctx repositoryAttemptContext,
	output FrozenExtractorOutput,
) (MaterializedBatch, error) {
	return materializeRepositoryBatchGrounded(ctx, output, nil, nil, false)
}

func materializeRepositoryBatchGrounded(
	ctx repositoryAttemptContext,
	output FrozenExtractorOutput,
	observer repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
	useGroundingIndex bool,
) (MaterializedBatch, error) {
	started := startRepositoryExtractionSubphase(observer)
	coverage, err := resolveRepositoryGoplsCoverage(ctx.input, ctx.extractorDefinition, output)
	if err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseBatchCoverageIndex, started, err)
		return MaterializedBatch{}, err
	}
	filesByPath := make(map[string]RepositoryExtractorFile, len(ctx.input.Files))
	for _, file := range ctx.input.Files {
		filesByPath[file.FileSnapshot.Path] = file
	}
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseBatchCoverageIndex, started, nil)
	started = startRepositoryExtractionSubphase(observer)
	var groundingIndex *repositoryGroundingIndex
	if useGroundingIndex {
		groundingIndex = newRepositoryGroundingIndex(ctx.input, filesByPath)
	}
	occurrences, err := materializeRepositoryOccurrencesGrounded(
		ctx,
		output.Proposals,
		filesByPath,
		occurrenceObserver,
		groundingIndex,
	)
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseBatchOccurrences, started, err)
	if err != nil {
		return MaterializedBatch{}, err
	}
	started = startRepositoryExtractionSubphase(observer)
	outputHash, err := extractorOutputHash(output)
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseBatchOutputHash, started, err)
	if err != nil {
		return MaterializedBatch{}, err
	}
	return MaterializedBatch{
		ExtractorDefinition:     ctx.extractorDefinition,
		ExtractionRun:           ctx.extractionRun,
		ExtractionAttempt:       ctx.extractionAttempt,
		ProposalBatch:           ctx.proposalBatch,
		Occurrences:             occurrences,
		RepositoryGoplsCoverage: coverage,
		FixtureOutputHash:       outputHash,
	}, nil
}

func materializeRepositoryOccurrences(
	ctx repositoryAttemptContext,
	proposals []ExtractorProposalOutput,
	filesByPath map[string]RepositoryExtractorFile,
) ([]ProposalOccurrence, error) {
	return materializeRepositoryOccurrencesProfiled(ctx, proposals, filesByPath, nil)
}

func materializeRepositoryOccurrencesProfiled(
	ctx repositoryAttemptContext,
	proposals []ExtractorProposalOutput,
	filesByPath map[string]RepositoryExtractorFile,
	observer repositoryOccurrenceComponentObserver,
) ([]ProposalOccurrence, error) {
	return materializeRepositoryOccurrencesGrounded(
		ctx,
		proposals,
		filesByPath,
		observer,
		newRepositoryGroundingIndex(ctx.input, filesByPath),
	)
}

func materializeRepositoryOccurrencesGrounded(
	ctx repositoryAttemptContext,
	proposals []ExtractorProposalOutput,
	filesByPath map[string]RepositoryExtractorFile,
	observer repositoryOccurrenceComponentObserver,
	groundingIndex *repositoryGroundingIndex,
) ([]ProposalOccurrence, error) {
	profile := newRepositoryOccurrenceProfile(observer)
	seenLocalIDs := make(map[string]bool, len(proposals))
	occurrences := make([]ProposalOccurrence, 0, len(proposals))
	for _, proposal := range proposals {
		if proposal.ProposalLocalID == "" || seenLocalIDs[proposal.ProposalLocalID] {
			return nil, newDomainError(ErrorDuplicateProposalLocalID, "repository proposal_local_id %q is empty or duplicated", proposal.ProposalLocalID)
		}
		seenLocalIDs[proposal.ProposalLocalID] = true
		hasCodeFact := proposal.CodeFact != nil
		hasCodeRelation := proposal.CodeRelation != nil
		if hasCodeFact == hasCodeRelation {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "repository proposal %s requires exactly one of code_fact or code_relation", proposal.ProposalLocalID)
		}

		var (
			sourceRefs         []ResolvedSourceRef
			codeFact           *ResolvedCodeFact
			codeRelation       *ResolvedCodeRelation
			fingerprint        string
			fingerprintVersion string
			err                error
		)
		resolutionStarted := profile.start()
		if hasCodeFact {
			file, ok := filesByPath[proposal.CodeFact.Path]
			if !ok {
				return nil, newDomainError(ErrorInvalidExtractorOutput, "repository proposal %s references unknown path %q", proposal.ProposalLocalID, proposal.CodeFact.Path)
			}
			var fileCtx attemptContext
			sourceRefs, fileCtx, err = repositoryProposalSourceRefsIndexed(
				ctx.input.RepositorySnapshot,
				file,
				proposal,
				groundingIndex,
			)
			if err != nil {
				return nil, err
			}
			codeFact, err = resolveCodeFact(fileCtx, proposal, sourceRefs)
			if err != nil {
				return nil, err
			}
			var codeFacts map[codeFactSpan]CodeFactOutput
			if groundingIndex == nil {
				codeFacts, err = goCodeFactIndex(fileCtx)
			} else {
				codeFacts, err = groundingIndex.codeFacts(file)
			}
			if err != nil {
				return nil, err
			}
			if err := verifyGoCodeFact(codeFacts, *codeFact); err != nil {
				return nil, err
			}
			fingerprintVersion = ProposalFingerprintCodeFactV1
			profile.add(repositoryOccurrenceComponentFactResolution, resolutionStarted)
		} else {
			sourceRefs, err = repositoryCodeRelationSourceRefsIndexed(
				ctx.input,
				*proposal.CodeRelation,
				groundingIndex,
			)
			if err != nil {
				return nil, err
			}
			codeRelation, err = resolveRepositoryCodeRelationIndexed(
				ctx.input,
				proposal,
				sourceRefs,
				groundingIndex,
			)
			if err != nil {
				return nil, err
			}
			fingerprintVersion, err = codeRelationFingerprintVersion(*codeRelation)
			if err != nil {
				return nil, err
			}
			profile.add(repositoryOccurrenceComponentRelationResolution, resolutionStarted)
		}
		fingerprintStarted := profile.start()
		if hasCodeFact {
			fingerprint, err = repositoryCodeFactProposalFingerprint(proposal.StatementText, sourceRefs, *codeFact)
		} else {
			fingerprint, err = repositoryCodeRelationProposalFingerprint(proposal.StatementText, sourceRefs, *codeRelation)
		}
		profile.add(repositoryOccurrenceComponentFingerprint, fingerprintStarted)
		if err != nil {
			return nil, err
		}
		assemblyStarted := profile.start()
		occurrenceID, err := stableID("occ:", "proposal_occurrence", struct {
			ExtractionAttemptID string `json:"extraction_attempt_id"`
			ProposalBatchID     string `json:"proposal_batch_id"`
			ProposalLocalID     string `json:"proposal_local_id"`
		}{ExtractionAttemptID: ctx.extractionAttempt.ID, ProposalBatchID: ctx.proposalBatch.ID, ProposalLocalID: proposal.ProposalLocalID})
		if err != nil {
			return nil, err
		}
		proposedPayload := map[string]any{
			"proposal_local_id": proposal.ProposalLocalID,
			"statement_text":    proposal.StatementText,
			"evidence_refs":     append([]string(nil), proposal.EvidenceRefs...),
		}
		if codeFact != nil {
			proposedPayload["code_fact"] = *codeFact
		}
		if codeRelation != nil {
			proposedPayload["code_relation"] = *codeRelation
		}
		occurrences = append(occurrences, ProposalOccurrence{
			ID:                         occurrenceID,
			BatchID:                    ctx.proposalBatch.ID,
			ExtractionAttemptID:        ctx.extractionAttempt.ID,
			ProposalLocalID:            proposal.ProposalLocalID,
			ProposalKind:               ProposalKindStatement,
			StatementText:              proposal.StatementText,
			ProposalFingerprint:        fingerprint,
			ProposalFingerprintVersion: fingerprintVersion,
			AdmissionOutcome:           admissionOutcomePending,
			SourceRefs:                 sourceRefs,
			CodeFact:                   codeFact,
			CodeRelation:               codeRelation,
			ProposedPayload:            proposedPayload,
		})
		profile.add(repositoryOccurrenceComponentAssembly, assemblyStarted)
	}
	profile.emit()
	return occurrences, nil
}

type repositoryOccurrenceProfile struct {
	observer                   repositoryOccurrenceComponentObserver
	factResolutionDuration     time.Duration
	relationResolutionDuration time.Duration
	fingerprintDuration        time.Duration
	assemblyDuration           time.Duration
	factCount                  int
	relationCount              int
	fingerprintCount           int
	assemblyCount              int
}

func newRepositoryOccurrenceProfile(observer repositoryOccurrenceComponentObserver) *repositoryOccurrenceProfile {
	if observer == nil {
		return nil
	}
	return &repositoryOccurrenceProfile{observer: observer}
}

func (p *repositoryOccurrenceProfile) start() time.Time {
	if p == nil {
		return time.Time{}
	}
	return time.Now()
}

func (p *repositoryOccurrenceProfile) add(component repositoryOccurrenceComponent, started time.Time) {
	if p == nil {
		return
	}
	duration := time.Since(started)
	switch component {
	case repositoryOccurrenceComponentFactResolution:
		p.factResolutionDuration += duration
		p.factCount++
	case repositoryOccurrenceComponentRelationResolution:
		p.relationResolutionDuration += duration
		p.relationCount++
	case repositoryOccurrenceComponentFingerprint:
		p.fingerprintDuration += duration
		p.fingerprintCount++
	case repositoryOccurrenceComponentAssembly:
		p.assemblyDuration += duration
		p.assemblyCount++
	}
}

func (p *repositoryOccurrenceProfile) emit() {
	if p == nil {
		return
	}
	p.observer(repositoryOccurrenceComponentFactResolution, p.factResolutionDuration, p.factCount, true)
	p.observer(repositoryOccurrenceComponentRelationResolution, p.relationResolutionDuration, p.relationCount, true)
	p.observer(repositoryOccurrenceComponentFingerprint, p.fingerprintDuration, p.fingerprintCount, true)
	p.observer(repositoryOccurrenceComponentAssembly, p.assemblyDuration, p.assemblyCount, true)
}

func repositoryProposalSourceRefsIndexed(
	snapshot RepositorySnapshot,
	file RepositoryExtractorFile,
	proposal ExtractorProposalOutput,
	index *repositoryGroundingIndex,
) ([]ResolvedSourceRef, attemptContext, error) {
	var (
		spansByID map[string]SpanEntry
		err       error
	)
	if index == nil {
		view := ExtractionView{ID: file.FileSnapshot.ID, Rendered: file.Content, RenderedContentHash: file.FileSnapshot.BlobHash}
		var spans []SpanEntry
		spans, err = buildLineSpanCatalog(view, SpanCatalogCodeLineV1)
		spansByID = make(map[string]SpanEntry, len(spans))
		for _, span := range spans {
			spansByID[span.SpanID] = span
		}
	} else {
		var spans repositoryGroundingLineSpans
		spans, err = index.lineSpans(file)
		spansByID = spans.byID
	}
	if err != nil {
		return nil, attemptContext{}, err
	}
	refs := make([]ResolvedSourceRef, 0, len(proposal.EvidenceRefs))
	for _, spanID := range proposal.EvidenceRefs {
		span, ok := spansByID[spanID]
		if !ok {
			return nil, attemptContext{}, newDomainError(ErrorUnknownSpan, "repository proposal %s references unknown span %s in %s", proposal.ProposalLocalID, spanID, file.FileSnapshot.Path)
		}
		refs = append(refs, ResolvedSourceRef{
			TargetKind:           "file_snapshot",
			RepositorySnapshotID: snapshot.ID,
			FileSnapshotID:       file.FileSnapshot.ID,
			RepoID:               snapshot.RepoID,
			CommitSHA:            snapshot.CommitSHA,
			Path:                 file.FileSnapshot.Path,
			SpanID:               span.SpanID,
			StartByte:            span.StartByte,
			EndByte:              span.EndByte,
			QuotedTextHash:       span.QuotedTextHash,
			QuotedText:           span.QuotedText,
		})
	}
	sortRepositorySourceRefs(refs)
	return refs, repositoryFileAttemptContext(snapshot, file), nil
}

func repositoryFileAttemptContext(snapshot RepositorySnapshot, file RepositoryExtractorFile) attemptContext {
	view := ExtractionView{ID: file.FileSnapshot.ID, Rendered: file.Content, RenderedContentHash: file.FileSnapshot.BlobHash}
	return attemptContext{manualSourceContext: manualSourceContext{
		SourceSnapshot: SourceSnapshot{
			SourceSystem:   SourceSystemCodeFile,
			SourceVersion:  snapshot.CommitSHA,
			RawContentHash: file.FileSnapshot.BlobHash,
			OriginMetadata: map[string]string{"repo_id": snapshot.RepoID, "commit_sha": snapshot.CommitSHA, "path": file.FileSnapshot.Path},
		},
		ExtractionView: view,
	}}
}

func repositoryCodeFactProposalFingerprint(statementText string, refs []ResolvedSourceRef, codeFact ResolvedCodeFact) (string, error) {
	type fingerprintSourceRef struct {
		RepositorySnapshotID string `json:"repository_snapshot_id"`
		FileSnapshotID       string `json:"file_snapshot_id"`
		StartByte            int    `json:"start_byte"`
		EndByte              int    `json:"end_byte"`
		QuotedTextHash       string `json:"quoted_text_hash"`
	}
	fingerprintRefs := make([]fingerprintSourceRef, 0, len(refs))
	for _, ref := range refs {
		fingerprintRefs = append(fingerprintRefs, fingerprintSourceRef{
			RepositorySnapshotID: ref.RepositorySnapshotID,
			FileSnapshotID:       ref.FileSnapshotID,
			StartByte:            ref.StartByte,
			EndByte:              ref.EndByte,
			QuotedTextHash:       ref.QuotedTextHash,
		})
	}
	return stableID("fp:", "repository_code_fact_proposal_fingerprint", struct {
		FingerprintVersion string                 `json:"fingerprint_version"`
		ProposalKind       string                 `json:"proposal_kind"`
		StatementText      string                 `json:"statement_text"`
		SourceRefs         []fingerprintSourceRef `json:"source_refs"`
		CodeFact           ResolvedCodeFact       `json:"code_fact"`
	}{
		FingerprintVersion: ProposalFingerprintCodeFactV1,
		ProposalKind:       ProposalKindStatement,
		StatementText:      statementText,
		SourceRefs:         fingerprintRefs,
		CodeFact:           codeFact,
	})
}

func sortRepositorySourceRefs(refs []ResolvedSourceRef) {
	slices.SortFunc(refs, func(a, b ResolvedSourceRef) int {
		switch {
		case a.FileSnapshotID < b.FileSnapshotID:
			return -1
		case a.FileSnapshotID > b.FileSnapshotID:
			return 1
		case a.StartByte < b.StartByte:
			return -1
		case a.StartByte > b.StartByte:
			return 1
		case a.EndByte < b.EndByte:
			return -1
		case a.EndByte > b.EndByte:
			return 1
		default:
			return 0
		}
	})
}

func failRepositoryAttempt(ctx context.Context, db sqlDB, repositoryCtx repositoryAttemptContext, output FrozenExtractorOutput, cause error) error {
	failureCtx, cancel := detachedFailureContext(ctx)
	defer cancel()
	attemptCtx := attemptContext{ExtractionAttempt: repositoryCtx.extractionAttempt}
	if len(output.Proposals) == 0 && output.RepositoryGoplsCoverage == nil {
		if err := persistAttemptFailureBytes(failureCtx, db, attemptCtx, nil, cause); err != nil {
			return fmt.Errorf("persisting repository attempt failure after %w: %v", cause, err)
		}
		return cause
	}
	if err := persistAttemptFailure(failureCtx, db, attemptCtx, output, cause); err != nil {
		return fmt.Errorf("persisting repository attempt failure after %w: %v", cause, err)
	}
	return cause
}

func repositoryIngestResult(batch MaterializedBatch, replayed bool) RepositoryIngestResult {
	occurrenceIDs := make([]string, 0, len(batch.Occurrences))
	for _, occurrence := range batch.Occurrences {
		occurrenceIDs = append(occurrenceIDs, occurrence.ID)
	}
	return RepositoryIngestResult{
		RepositorySnapshotID:    batch.ExtractionRun.RepositorySnapshotID,
		ExtractionRunID:         batch.ExtractionRun.ID,
		ExtractionAttemptID:     batch.ExtractionAttempt.ID,
		ProposalBatchID:         batch.ProposalBatch.ID,
		ProposalOccurrenceIDs:   occurrenceIDs,
		ProposalCount:           len(occurrenceIDs),
		OutputHash:              batch.FixtureOutputHash,
		RepositoryGoplsCoverage: batch.RepositoryGoplsCoverage,
		Replayed:                replayed,
	}
}

func replayRepositoryAttempt(ctx context.Context, db sqlDB, attemptCtx repositoryAttemptContext) (RepositoryIngestResult, error) {
	result := RepositoryIngestResult{
		RepositorySnapshotID:  attemptCtx.input.RepositorySnapshot.ID,
		ExtractionRunID:       attemptCtx.extractionRun.ID,
		ExtractionAttemptID:   attemptCtx.extractionAttempt.ID,
		ProposalOccurrenceIDs: []string{},
	}
	var fixtureOutput []byte
	err := db.queryRow(ctx, `
		SELECT pb.proposal_batch_id, pb.proposal_count, ea.output_hash, ea.fixture_output
		FROM proposal_batches pb
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = pb.extraction_attempt_id
		WHERE pb.extraction_attempt_id = $1 AND pb.status = 'completed'
	`, attemptCtx.extractionAttempt.ID).Scan(&result.ProposalBatchID, &result.ProposalCount, &result.OutputHash, &fixtureOutput)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RepositoryIngestResult{}, newDomainError(ErrorMissingSourceViewAttempt, "succeeded repository attempt %s has no completed proposal batch", attemptCtx.extractionAttempt.ID)
		}
		return RepositoryIngestResult{}, fmt.Errorf("reading replayed repository batch: %w", err)
	}
	var output FrozenExtractorOutput
	if err := json.Unmarshal(fixtureOutput, &output); err != nil {
		return RepositoryIngestResult{}, fmt.Errorf("decoding replayed repository extractor output: %w", err)
	}
	coverage, err := resolveRepositoryGoplsCoverage(attemptCtx.input, attemptCtx.extractorDefinition, output)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	result.RepositoryGoplsCoverage = coverage
	rows, err := db.query(ctx, `
		SELECT proposal_occurrence_id
		FROM proposal_occurrences
		WHERE extraction_attempt_id = $1 AND proposal_batch_id = $2
		ORDER BY proposal_local_id
	`, attemptCtx.extractionAttempt.ID, result.ProposalBatchID)
	if err != nil {
		return RepositoryIngestResult{}, fmt.Errorf("reading replayed repository occurrences: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var occurrenceID string
		if err := rows.Scan(&occurrenceID); err != nil {
			return RepositoryIngestResult{}, fmt.Errorf("scanning replayed repository occurrence: %w", err)
		}
		result.ProposalOccurrenceIDs = append(result.ProposalOccurrenceIDs, occurrenceID)
	}
	if err := rows.Err(); err != nil {
		return RepositoryIngestResult{}, fmt.Errorf("iterating replayed repository occurrences: %w", err)
	}
	if len(result.ProposalOccurrenceIDs) != result.ProposalCount {
		return RepositoryIngestResult{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository batch %s proposal count is %d but %d occurrences were loaded", result.ProposalBatchID, result.ProposalCount, len(result.ProposalOccurrenceIDs))
	}
	return result, nil
}
