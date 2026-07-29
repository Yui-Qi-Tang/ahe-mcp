package evidenceingestion

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ExtractorRepositoryGoplsCodeFact identifies repository-wide real-LSP declaration extraction.
	ExtractorRepositoryGoplsCodeFact = "repository-gopls-code-fact"
	// ExtractorRepositoryGoplsCodeFactVersion adds ambiguous-target diagnostics.
	ExtractorRepositoryGoplsCodeFactVersion = "v10"

	defaultRepositoryGoplsTimeout = 2 * time.Minute
)

type repositoryGoplsRunner struct {
	binaryPath    string
	workspaceRoot string
	repoID        string
	commitSHA     string
	timeout       time.Duration
	goplsVersion  string
}

type repositoryGoplsMaterializationHook func(
	input RepositoryExtractorInput,
	analysis repositoryGoplsAnalysis,
	indexed FrozenExtractorOutput,
) error

// RunRepositoryGoplsExtractor persists grounded package clauses, declarations, typed relations, and bounded semantic coverage.
func RunRepositoryGoplsExtractor(ctx context.Context, pool *pgxpool.Pool, request RepositoryGoplsRequest) (RepositoryIngestResult, error) {
	if pool == nil {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return runRepositoryGoplsExtractor(ctx, pgxDB{pool: pool}, request)
}

func runRepositoryGoplsExtractor(ctx context.Context, db sqlDB, request RepositoryGoplsRequest) (RepositoryIngestResult, error) {
	return runRepositoryGoplsExtractorObserved(ctx, db, request, nil)
}

func runRepositoryGoplsExtractorObserved(
	ctx context.Context,
	db sqlDB,
	request RepositoryGoplsRequest,
	observer repositoryExtractionPhaseObserver,
) (RepositoryIngestResult, error) {
	return runRepositoryGoplsExtractorProfiled(ctx, db, request, observer, nil)
}

func runRepositoryGoplsExtractorProfiled(
	ctx context.Context,
	db sqlDB,
	request RepositoryGoplsRequest,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
) (RepositoryIngestResult, error) {
	return runRepositoryGoplsExtractorOccurrenceProfiled(
		ctx,
		db,
		request,
		observer,
		subphaseObserver,
		nil,
	)
}

func runRepositoryGoplsExtractorOccurrenceProfiled(
	ctx context.Context,
	db sqlDB,
	request RepositoryGoplsRequest,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
) (RepositoryIngestResult, error) {
	return runRepositoryGoplsExtractorMaterializationCompared(
		ctx,
		db,
		request,
		observer,
		subphaseObserver,
		occurrenceObserver,
		nil,
	)
}

func runRepositoryGoplsExtractorMaterializationCompared(
	ctx context.Context,
	db sqlDB,
	request RepositoryGoplsRequest,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
	batchHook repositoryBatchMaterializationHook,
) (RepositoryIngestResult, error) {
	return runRepositoryGoplsExtractorCompared(
		ctx,
		db,
		request,
		observer,
		subphaseObserver,
		occurrenceObserver,
		nil,
		batchHook,
	)
}

func runRepositoryGoplsExtractorCompared(
	ctx context.Context,
	db sqlDB,
	request RepositoryGoplsRequest,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
	materializationHook repositoryGoplsMaterializationHook,
	batchHook repositoryBatchMaterializationHook,
) (RepositoryIngestResult, error) {
	return runRepositoryGoplsExtractorInstrumented(
		ctx,
		db,
		request,
		observer,
		subphaseObserver,
		occurrenceObserver,
		nil,
		nil,
		nil,
		materializationHook,
		batchHook,
	)
}

func runRepositoryGoplsExtractorInstrumented(
	ctx context.Context,
	db sqlDB,
	request RepositoryGoplsRequest,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	occurrenceObserver repositoryOccurrenceComponentObserver,
	analysisStageObserver repositoryGoplsAnalysisStageObserver,
	callObserver goplsCallObserver,
	definitionContributionObserver repositoryGoplsDefinitionContributionObserver,
	materializationHook repositoryGoplsMaterializationHook,
	batchHook repositoryBatchMaterializationHook,
) (RepositoryIngestResult, error) {
	if request.RequestID == "" {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	if strings.TrimSpace(request.WorkspaceRoot) == "" {
		return RepositoryIngestResult{}, newDomainError(ErrorInvalidInput, "workspace_root is required")
	}
	started := startRepositoryExtractionPhase(observer)
	input, err := buildRepositoryExtractorInput(ctx, db, request.RepositorySnapshotID)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhaseLoadInput, started, err)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	started = startRepositoryExtractionPhase(observer)
	runner, err := newRepositoryGoplsRunner(ctx, input, request.WorkspaceRoot)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhaseValidateRunner, started, err)
	if err != nil {
		return RepositoryIngestResult{}, err
	}
	return runRepositoryExtractorMaterializationCompared(
		ctx,
		db,
		input,
		request.RequestID,
		request.RetryFailedAttempt,
		runner.ExtractorDefinition(),
		func(ctx context.Context, input RepositoryExtractorInput) (FrozenExtractorOutput, error) {
			return runner.runProfiledInstrumented(
				ctx,
				input,
				observer,
				subphaseObserver,
				analysisStageObserver,
				callObserver,
				definitionContributionObserver,
				materializationHook,
			)
		},
		observer,
		subphaseObserver,
		occurrenceObserver,
		batchHook,
	)
}

func newRepositoryGoplsRunner(ctx context.Context, input RepositoryExtractorInput, workspaceRoot string) (*repositoryGoplsRunner, error) {
	validationCtx, cancel := context.WithTimeout(ctx, defaultGitRepositorySnapshotTimeout)
	defer cancel()
	observed, err := buildGitRepositorySnapshotContext(validationCtx, GitRepositorySnapshotConfig{
		WorkspaceRoot: workspaceRoot,
		RepoID:        input.RepositorySnapshot.RepoID,
		CommitSHA:     input.RepositorySnapshot.CommitSHA,
		RequestID:     "repository-gopls-workspace-validation",
	})
	if err != nil {
		return nil, err
	}
	if err := validateRepositoryGoplsAuthority(input, observed); err != nil {
		return nil, err
	}
	validated, err := validateGoplsWorkspaceConfig(validationCtx, GoplsWorkspaceInventoryConfig{
		WorkspaceRoot: workspaceRoot,
		RepoID:        input.RepositorySnapshot.RepoID,
		CommitSHA:     input.RepositorySnapshot.CommitSHA,
		Timeout:       defaultRepositoryGoplsTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &repositoryGoplsRunner{
		binaryPath:    validated.binaryPath,
		workspaceRoot: validated.workspaceRoot,
		repoID:        validated.repoID,
		commitSHA:     validated.commitSHA,
		timeout:       validated.timeout,
		goplsVersion:  validated.goplsVersion,
	}, nil
}

func validateRepositoryGoplsAuthority(input RepositoryExtractorInput, observed repositorySnapshotContext) error {
	if observed.snapshot != input.RepositorySnapshot {
		return newDomainError(
			ErrorRepositoryRevisionMismatch,
			"workspace Git snapshot %s/%s does not match persisted repository snapshot %s",
			observed.snapshot.RepoID,
			observed.snapshot.CommitSHA,
			input.RepositorySnapshot.ID,
		)
	}
	if len(observed.files) != len(input.Files) {
		return newDomainError(
			ErrorRepositorySnapshotIntegrity,
			"workspace Git snapshot has %d selected files, want %d persisted files",
			len(observed.files),
			len(input.Files),
		)
	}
	observedByPath := make(map[string]SourceFileSnapshot, len(observed.files))
	for _, observedFile := range observed.files {
		if _, exists := observedByPath[observedFile.Path]; exists {
			return newDomainError(
				ErrorRepositorySnapshotIntegrity,
				"workspace Git snapshot contains duplicate selected path %s",
				observedFile.Path,
			)
		}
		observedByPath[observedFile.Path] = observedFile
	}
	for _, file := range input.Files {
		observedFile, ok := observedByPath[file.FileSnapshot.Path]
		if !ok {
			return newDomainError(
				ErrorRepositorySnapshotIntegrity,
				"workspace Git snapshot is missing persisted path %s",
				file.FileSnapshot.Path,
			)
		}
		if observedFile != file.FileSnapshot {
			return newDomainError(
				ErrorRepositorySnapshotIntegrity,
				"workspace Git file %s does not match persisted file snapshot %s",
				observedFile.Path,
				file.FileSnapshot.ID,
			)
		}
		observedContent, ok := observed.blobs[observedFile.BlobHash]
		if !ok || !bytes.Equal(observedContent, file.Content) {
			return newDomainError(
				ErrorRepositorySnapshotIntegrity,
				"workspace Git bytes for %s do not match persisted file snapshot %s",
				observedFile.Path,
				file.FileSnapshot.ID,
			)
		}
	}
	return nil
}

func (r *repositoryGoplsRunner) ExtractorDefinition() ExtractorDefinitionInput {
	if r == nil {
		return ExtractorDefinitionInput{}
	}
	return ExtractorDefinitionInput{
		Name:    ExtractorRepositoryGoplsCodeFact,
		Version: ExtractorRepositoryGoplsCodeFactVersion,
		Config: map[string]string{
			"backend":                      "gopls/lsp",
			"gopls_version":                r.goplsVersion,
			"input_contract":               "repository-snapshot-tracked-go-files-v1",
			"lsp_methods":                  "textDocument/documentSymbol,workspace/executeCommand:gopls.package_symbols,textDocument/definition,textDocument/references,textDocument/prepareCallHierarchy,callHierarchy/outgoingCalls,callHierarchy/incomingCalls",
			"output_schemas":               CodeFactSchemaV1 + "," + CodeFactSchemaV2 + "," + CodeRelationSchemaV1 + "," + CodeRelationSchemaV2 + "," + CodeRelationSchemaV3,
			"coverage_schema":              RepositoryGoplsCoverageSchemaV8,
			"failure_coverage":             "coverage_only_no_partial_proposals",
			"position_encoding":            "utf-16",
			"proposal_kind":                ProposalKindStatement,
			"package_fact_scope":           "file_bound_parser_grounded_package_clause_no_import_path",
			"relation_coverage":            "definition_primary_positive_unique_selected_snapshot_files",
			"reference_enumeration":        "parser_grounded_declarations_include_declaration_false",
			"reference_reconciliation":     "coverage_only_no_duplicate_relation_proposals",
			"call_relation_scope":          "outgoing_primary_function_method_selected_snapshot_files",
			"call_reconciliation":          "incoming_coverage_only_no_duplicate_relation_proposals",
			"revision_binding":             RepositoryRevisionVerificationGitV1,
			"span_contract":                SpanCatalogCodeLineV1,
			"symbol_schema":                "repo-commit-path-package-symbol-v1",
			"relation_cardinality":         "unique_selected_snapshot_targets_only",
			"ambiguous_relation_semantics": "coverage_only_no_relation_proposal",
			"topology_scope":               "package_clauses_declarations_same_and_cross_file_definitions_and_calls",
			"unselected_result_semantics":  "coverage_counts_only_no_uri_path_or_relation_payload",
			"workspace_authority_contract": "git-manifest-and-persisted-overlays-v1",
		},
	}
}

func (r *repositoryGoplsRunner) Run(ctx context.Context, input RepositoryExtractorInput) (FrozenExtractorOutput, error) {
	return r.run(ctx, input, nil)
}

func (r *repositoryGoplsRunner) run(
	ctx context.Context,
	input RepositoryExtractorInput,
	observer repositoryExtractionPhaseObserver,
) (FrozenExtractorOutput, error) {
	return r.runProfiled(ctx, input, observer, nil)
}

func (r *repositoryGoplsRunner) runProfiled(
	ctx context.Context,
	input RepositoryExtractorInput,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
) (FrozenExtractorOutput, error) {
	return r.runProfiledCompared(ctx, input, observer, subphaseObserver, nil)
}

func (r *repositoryGoplsRunner) runProfiledCompared(
	ctx context.Context,
	input RepositoryExtractorInput,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	materializationHook repositoryGoplsMaterializationHook,
) (FrozenExtractorOutput, error) {
	return r.runProfiledInstrumented(
		ctx,
		input,
		observer,
		subphaseObserver,
		nil,
		nil,
		nil,
		materializationHook,
	)
}

func (r *repositoryGoplsRunner) runProfiledInstrumented(
	ctx context.Context,
	input RepositoryExtractorInput,
	observer repositoryExtractionPhaseObserver,
	subphaseObserver repositoryExtractionSubphaseObserver,
	analysisStageObserver repositoryGoplsAnalysisStageObserver,
	callObserver goplsCallObserver,
	definitionContributionObserver repositoryGoplsDefinitionContributionObserver,
	materializationHook repositoryGoplsMaterializationHook,
) (FrozenExtractorOutput, error) {
	if r == nil {
		return FrozenExtractorOutput{}, newDomainError(ErrorInvalidInput, "repository gopls runner is nil")
	}
	if input.RepositorySnapshot.RepoID != r.repoID || input.RepositorySnapshot.CommitSHA != r.commitSHA {
		return FrozenExtractorOutput{}, newDomainError(ErrorRepositoryRevisionMismatch, "repository gopls input does not match validated workspace revision")
	}
	if len(input.Files) == 0 {
		coverage := buildRepositoryGoplsCoverage(input, repositoryGoplsAnalysis{symbolsByPath: map[string][]lspDocumentSymbol{}}, nil, nil, nil, nil, nil)
		return FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{}, RepositoryGoplsCoverage: &coverage}, nil
	}
	started := startRepositoryExtractionPhase(observer)
	analysis, err := runRepositoryGoplsAnalysisProfiled(
		ctx,
		r.binaryPath,
		r.workspaceRoot,
		input,
		r.timeout,
		analysisStageObserver,
		callObserver,
		definitionContributionObserver,
	)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhaseGoplsAnalysis, started, err)
	if err != nil {
		return repositoryGoplsFailureOutput(input, analysis, analysis.failureStage), err
	}
	started = startRepositoryExtractionPhase(observer)
	output, err := materializeRepositoryGoplsAnalysisProfiled(input, analysis, subphaseObserver)
	finishRepositoryExtractionPhase(observer, repositoryExtractionPhaseExtractorMaterialization, started, err)
	if err == nil && materializationHook != nil {
		err = materializationHook(input, analysis, output)
	}
	return output, err
}

func materializeRepositoryGoplsAnalysis(
	input RepositoryExtractorInput,
	analysis repositoryGoplsAnalysis,
) (FrozenExtractorOutput, error) {
	return materializeRepositoryGoplsAnalysisProfiled(input, analysis, nil)
}

func materializeRepositoryGoplsAnalysisProfiled(
	input RepositoryExtractorInput,
	analysis repositoryGoplsAnalysis,
	observer repositoryExtractionSubphaseObserver,
) (FrozenExtractorOutput, error) {
	return materializeRepositoryGoplsAnalysisWithIndex(input, analysis, observer, true)
}

func materializeRepositoryGoplsAnalysisLegacy(
	input RepositoryExtractorInput,
	analysis repositoryGoplsAnalysis,
) (FrozenExtractorOutput, error) {
	return materializeRepositoryGoplsAnalysisWithIndex(input, analysis, nil, false)
}

func materializeRepositoryGoplsAnalysisWithIndex(
	input RepositoryExtractorInput,
	analysis repositoryGoplsAnalysis,
	observer repositoryExtractionSubphaseObserver,
	useIndex bool,
) (FrozenExtractorOutput, error) {
	var index *repositoryGoplsMaterializationIndex
	if useIndex {
		index = newRepositoryGoplsMaterializationIndex(input)
	}
	started := startRepositoryExtractionSubphase(observer)
	output, err := materializeRepositoryGoplsOutputIndexed(input, analysis.symbolsByPath, index)
	if err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorDeclarationsPackages, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageDocumentSymbolGrounding), err
	}
	packages, err := materializeRepositoryGoplsPackagesIndexed(input, analysis.packages.packages, index)
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorDeclarationsPackages, started, err)
	if err != nil {
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStagePackageGrounding), err
	}
	started = startRepositoryExtractionSubphase(observer)
	relations, ambiguousDefinitions, err := materializeRepositoryGoplsDefinitionsIndexed(
		input,
		append([]repositoryGoplsDefinition(nil), analysis.definitions.relations...),
		index,
	)
	if err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorDefinitionsReferences, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageDefinitionGrounding), err
	}
	analysis.definitions.ambiguousUsageCount = ambiguousDefinitions
	references, ambiguousReferences, err := materializeRepositoryGoplsDefinitionsIndexed(
		input,
		append([]repositoryGoplsDefinition(nil), analysis.references.relations...),
		index,
	)
	if err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorDefinitionsReferences, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageReferenceGrounding), err
	}
	analysis.references.ambiguousUsageCount = ambiguousReferences
	if err := verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(input, references, index); err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorDefinitionsReferences, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageReferenceGrounding), err
	}
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorDefinitionsReferences, started, nil)
	started = startRepositoryExtractionSubphase(observer)
	calls, err := materializeRepositoryGoplsCallsIndexed(
		input,
		append([]repositoryGoplsCall(nil), analysis.calls.calls...),
		index,
	)
	if err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorCallsIncoming, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageCallGrounding), err
	}
	incomingCalls, err := materializeRepositoryGoplsIncomingCallsIndexed(
		input,
		append([]repositoryGoplsCall(nil), analysis.calls.incomingCalls...),
		index,
	)
	if err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorCallsIncoming, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageIncomingCallGrounding), err
	}
	if err := verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(input, incomingCalls, index); err != nil {
		finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorCallsIncoming, started, err)
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageIncomingCallGrounding), err
	}
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorCallsIncoming, started, nil)
	started = startRepositoryExtractionSubphase(observer)
	coverage := buildRepositoryGoplsCoverage(input, analysis, packages, relations, references, calls, incomingCalls)
	output.Proposals = append(output.Proposals, packages...)
	output.Proposals = append(output.Proposals, relations...)
	output.Proposals = append(output.Proposals, calls...)
	output.RepositoryGoplsCoverage = &coverage
	finishRepositoryExtractionSubphase(observer, repositoryExtractionSubphaseExtractorCoverageAssembly, started, nil)
	return output, nil
}

func repositoryGoplsFailureOutput(input RepositoryExtractorInput, analysis repositoryGoplsAnalysis, stage string) FrozenExtractorOutput {
	analysis.failureStage = stage
	coverage := buildRepositoryGoplsCoverage(input, analysis, nil, nil, nil, nil, nil)
	return FrozenExtractorOutput{
		Proposals:               []ExtractorProposalOutput{},
		RepositoryGoplsCoverage: &coverage,
	}
}

func runRepositoryGoplsAnalysis(
	ctx context.Context,
	binaryPath string,
	workspaceRoot string,
	input RepositoryExtractorInput,
	timeout time.Duration,
) (repositoryGoplsAnalysis, error) {
	return runRepositoryGoplsAnalysisProfiled(
		ctx,
		binaryPath,
		workspaceRoot,
		input,
		timeout,
		nil,
		nil,
		nil,
	)
}

func runRepositoryGoplsAnalysisProfiled(
	ctx context.Context,
	binaryPath string,
	workspaceRoot string,
	input RepositoryExtractorInput,
	timeout time.Duration,
	stageObserver repositoryGoplsAnalysisStageObserver,
	callObserver goplsCallObserver,
	definitionContributionObserver repositoryGoplsDefinitionContributionObserver,
) (repositoryGoplsAnalysis, error) {
	analysis := repositoryGoplsAnalysis{
		symbolsByPath: make(map[string][]lspDocumentSymbol, len(input.Files)),
		failureStage:  repositoryGoplsStageSyntaxInventory,
	}
	stageStarted := startRepositoryGoplsAnalysisStage(stageObserver)
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	finishRepositoryGoplsAnalysisStage(
		stageObserver,
		repositoryGoplsAnalysisStageSyntaxInventory,
		stageStarted,
		len(input.Files),
		err,
	)
	if err != nil {
		return analysis, err
	}
	files := input.Files
	analysis.syntaxCounts = syntaxCounts
	analysis.failureStage = repositoryGoplsStageSessionInitialize
	err = withGoplsSessionObserved(ctx, binaryPath, workspaceRoot, timeout, repositoryGoplsSessionShutdownTimeout, callObserver, func(client *lspClient) error {
		filePaths := make(map[string]string, len(files))
		analysis.failureStage = repositoryGoplsStageOpenDocument
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		opened := 0
		for _, file := range files {
			path, err := cleanRepositoryPath(file.FileSnapshot.Path)
			if err != nil {
				finishRepositoryGoplsAnalysisStage(
					stageObserver,
					repositoryGoplsAnalysisStageOpenDocuments,
					stageStarted,
					opened,
					err,
				)
				return err
			}
			filePath := filepath.Join(workspaceRoot, filepath.FromSlash(path))
			if err := openGoplsDocument(client, filePath, file.Content); err != nil {
				stageErr := fmt.Errorf("opening repository gopls document %s: %w", path, err)
				finishRepositoryGoplsAnalysisStage(
					stageObserver,
					repositoryGoplsAnalysisStageOpenDocuments,
					stageStarted,
					opened,
					stageErr,
				)
				return stageErr
			}
			filePaths[path] = filePath
			opened++
		}
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStageOpenDocuments,
			stageStarted,
			opened,
			nil,
		)
		analysis.failureStage = repositoryGoplsStageDocumentSymbol
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		completedDocumentSymbols := 0
		for _, file := range files {
			path := file.FileSnapshot.Path
			analysis.documentSymbolRequestCount++
			symbols, err := requestGoplsDocumentSymbols(client, filePaths[path])
			if err != nil {
				stageErr := fmt.Errorf("requesting repository gopls symbols for %s: %w", path, err)
				finishRepositoryGoplsAnalysisStage(
					stageObserver,
					repositoryGoplsAnalysisStageDocumentSymbols,
					stageStarted,
					completedDocumentSymbols,
					stageErr,
				)
				return stageErr
			}
			analysis.symbolsByPath[path] = symbols
			completedDocumentSymbols++
		}
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStageDocumentSymbols,
			stageStarted,
			completedDocumentSymbols,
			nil,
		)
		analysis.failureStage = repositoryGoplsStagePackageSymbols
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		analysis.packages, err = requestRepositoryGoplsPackages(client, files, filePaths)
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStagePackageSymbols,
			stageStarted,
			analysis.packages.completedRequestCount,
			err,
		)
		if err != nil {
			return err
		}
		analysis.failureStage = repositoryGoplsStageDefinition
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		var definitionContributionRecorder *repositoryGoplsDefinitionContributionRecorder
		if definitionContributionObserver != nil {
			definitionContributionRecorder = newRepositoryGoplsDefinitionContributionRecorder()
		}
		analysis.definitions, err = requestRepositoryGoplsDefinitionsProfiled(
			client,
			files,
			filePaths,
			definitionContributionRecorder,
		)
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStageDefinitions,
			stageStarted,
			analysis.definitions.completedRequestCount,
			err,
		)
		if err != nil {
			return err
		}
		if definitionContributionObserver != nil {
			definitionContributionObserver(definitionContributionRecorder.result())
		}
		analysis.failureStage = repositoryGoplsStageReferences
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		analysis.references, err = requestRepositoryGoplsReferences(client, input, filePaths)
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStageReferences,
			stageStarted,
			analysis.references.completedRequestCount,
			err,
		)
		if err != nil {
			return err
		}
		analysis.failureStage = repositoryGoplsStagePrepareCallHierarchy
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		analysis.calls, err = requestRepositoryGoplsCalls(client, input, filePaths)
		completedCalls := analysis.calls.prepareCompletedRequestCount +
			analysis.calls.outgoingCompletedRequestCount +
			analysis.calls.incomingCompletedRequestCount
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStageCalls,
			stageStarted,
			completedCalls,
			err,
		)
		if err != nil {
			analysis.failureStage = analysis.calls.failureStage
			return err
		}
		analysis.failureStage = repositoryGoplsStageCloseDocument
		stageStarted = startRepositoryGoplsAnalysisStage(stageObserver)
		closed := 0
		for _, file := range files {
			path := file.FileSnapshot.Path
			if err := closeGoplsDocument(client, filePaths[path]); err != nil {
				stageErr := fmt.Errorf("closing repository gopls document %s: %w", path, err)
				finishRepositoryGoplsAnalysisStage(
					stageObserver,
					repositoryGoplsAnalysisStageCloseDocuments,
					stageStarted,
					closed,
					stageErr,
				)
				return stageErr
			}
			closed++
		}
		finishRepositoryGoplsAnalysisStage(
			stageObserver,
			repositoryGoplsAnalysisStageCloseDocuments,
			stageStarted,
			closed,
			nil,
		)
		analysis.failureStage = ""
		return nil
	})
	if err != nil {
		if analysis.failureStage == "" {
			analysis.failureStage = repositoryGoplsStageSessionShutdown
		}
		return analysis, err
	}
	return analysis, nil
}

func materializeRepositoryGoplsOutput(
	input RepositoryExtractorInput,
	symbolsByPath map[string][]lspDocumentSymbol,
) (FrozenExtractorOutput, error) {
	return materializeRepositoryGoplsOutputIndexed(input, symbolsByPath, nil)
}

func materializeRepositoryGoplsOutputIndexed(
	input RepositoryExtractorInput,
	symbolsByPath map[string][]lspDocumentSymbol,
	index *repositoryGoplsMaterializationIndex,
) (FrozenExtractorOutput, error) {
	proposals := make([]ExtractorProposalOutput, 0)
	for _, file := range input.Files {
		path := file.FileSnapshot.Path
		symbols, ok := symbolsByPath[path]
		if !ok {
			return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "repository gopls returned no document result for %s", path)
		}
		parserInput := GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      path,
			Source:    file.Content,
		}
		var output FrozenExtractorOutput
		var err error
		if index == nil {
			output, err = materializeGoplsDeclarations(parserInput, symbols)
		} else {
			extraction, extractionErr := index.extraction(path)
			if extractionErr != nil {
				return FrozenExtractorOutput{}, extractionErr
			}
			output, err = materializeGoplsDeclarationsWithExtraction(
				parserInput,
				symbols,
				extraction,
			)
		}
		if err != nil {
			return FrozenExtractorOutput{}, err
		}
		for _, proposal := range output.Proposals {
			qualified, err := qualifyRepositoryProposalLocalID(path, proposal)
			if err != nil {
				return FrozenExtractorOutput{}, err
			}
			proposals = append(proposals, qualified)
		}
	}
	return FrozenExtractorOutput{Proposals: proposals}, nil
}
