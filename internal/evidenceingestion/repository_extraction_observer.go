package evidenceingestion

import "time"

type repositoryExtractionPhase string

const (
	repositoryExtractionPhaseLoadInput                repositoryExtractionPhase = "load_repository_input"
	repositoryExtractionPhaseValidateRunner           repositoryExtractionPhase = "validate_repository_runner"
	repositoryExtractionPhasePrepareAttempt           repositoryExtractionPhase = "prepare_attempt"
	repositoryExtractionPhaseGoplsAnalysis            repositoryExtractionPhase = "gopls_analysis"
	repositoryExtractionPhaseExtractorMaterialization repositoryExtractionPhase = "extractor_materialization"
	repositoryExtractionPhaseBatchMaterialization     repositoryExtractionPhase = "batch_materialization"
	repositoryExtractionPhasePersistSuccess           repositoryExtractionPhase = "persist_success"
	repositoryExtractionPhaseCreateGeneration         repositoryExtractionPhase = "create_generation"
)

type repositoryExtractionSubphase string

const (
	repositoryExtractionSubphaseExtractorDeclarationsPackages  repositoryExtractionSubphase = "extractor_declarations_packages"
	repositoryExtractionSubphaseExtractorDefinitionsReferences repositoryExtractionSubphase = "extractor_definitions_references"
	repositoryExtractionSubphaseExtractorCallsIncoming         repositoryExtractionSubphase = "extractor_calls_incoming"
	repositoryExtractionSubphaseExtractorCoverageAssembly      repositoryExtractionSubphase = "extractor_coverage_assembly"
	repositoryExtractionSubphaseBatchCoverageIndex             repositoryExtractionSubphase = "batch_coverage_index"
	repositoryExtractionSubphaseBatchOccurrences               repositoryExtractionSubphase = "batch_occurrences"
	repositoryExtractionSubphaseBatchOutputHash                repositoryExtractionSubphase = "batch_output_hash"
)

type repositoryExtractionPhaseObserver func(repositoryExtractionPhase, time.Duration, bool)

type repositoryExtractionSubphaseObserver func(repositoryExtractionSubphase, time.Duration, bool)

type repositoryOccurrenceComponent string

const (
	repositoryOccurrenceComponentFactResolution     repositoryOccurrenceComponent = "fact_resolution_verification"
	repositoryOccurrenceComponentRelationResolution repositoryOccurrenceComponent = "relation_resolution_verification"
	repositoryOccurrenceComponentFingerprint        repositoryOccurrenceComponent = "fingerprint_generation"
	repositoryOccurrenceComponentAssembly           repositoryOccurrenceComponent = "occurrence_assembly"
)

type repositoryOccurrenceComponentObserver func(repositoryOccurrenceComponent, time.Duration, int, bool)

type repositoryGoplsAnalysisStage string

const (
	repositoryGoplsAnalysisStageSyntaxInventory repositoryGoplsAnalysisStage = "analysis_syntax_inventory"
	repositoryGoplsAnalysisStageOpenDocuments   repositoryGoplsAnalysisStage = "analysis_open_documents"
	repositoryGoplsAnalysisStageDocumentSymbols repositoryGoplsAnalysisStage = "analysis_document_symbols"
	repositoryGoplsAnalysisStagePackageSymbols  repositoryGoplsAnalysisStage = "analysis_package_symbols"
	repositoryGoplsAnalysisStageDefinitions     repositoryGoplsAnalysisStage = "analysis_definitions"
	repositoryGoplsAnalysisStageReferences      repositoryGoplsAnalysisStage = "analysis_references"
	repositoryGoplsAnalysisStageCalls           repositoryGoplsAnalysisStage = "analysis_calls"
	repositoryGoplsAnalysisStageCloseDocuments  repositoryGoplsAnalysisStage = "analysis_close_documents"
)

type repositoryGoplsAnalysisStageObserver func(
	repositoryGoplsAnalysisStage,
	time.Duration,
	int,
	bool,
)

type goplsCallObserver func(string, time.Duration, bool)

func startRepositoryExtractionPhase(observer repositoryExtractionPhaseObserver) time.Time {
	if observer == nil {
		return time.Time{}
	}
	return time.Now()
}

func finishRepositoryExtractionPhase(
	observer repositoryExtractionPhaseObserver,
	phase repositoryExtractionPhase,
	started time.Time,
	err error,
) {
	if observer == nil {
		return
	}
	observer(phase, time.Since(started), err == nil)
}

func startRepositoryExtractionSubphase(observer repositoryExtractionSubphaseObserver) time.Time {
	if observer == nil {
		return time.Time{}
	}
	return time.Now()
}

func finishRepositoryExtractionSubphase(
	observer repositoryExtractionSubphaseObserver,
	subphase repositoryExtractionSubphase,
	started time.Time,
	err error,
) {
	if observer == nil {
		return
	}
	observer(subphase, time.Since(started), err == nil)
}

func startRepositoryGoplsAnalysisStage(
	observer repositoryGoplsAnalysisStageObserver,
) time.Time {
	if observer == nil {
		return time.Time{}
	}
	return time.Now()
}

func finishRepositoryGoplsAnalysisStage(
	observer repositoryGoplsAnalysisStageObserver,
	stage repositoryGoplsAnalysisStage,
	started time.Time,
	count int,
	err error,
) {
	if observer == nil {
		return
	}
	observer(stage, time.Since(started), count, err == nil)
}
