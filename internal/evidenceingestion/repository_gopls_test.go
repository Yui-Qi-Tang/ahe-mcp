package evidenceingestion

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMaterializeRepositoryGoplsOutputUsesGroundedUniqueProposals(t *testing.T) {
	input := repositoryParserTestInput(t)
	symbolsByPath := make(map[string][]lspDocumentSymbol, len(input.Files))
	for _, file := range input.Files {
		symbolsByPath[file.FileSnapshot.Path] = []lspDocumentSymbol{
			testDocumentSymbol(t, file.Content, "Run", 12),
		}
	}

	output, err := materializeRepositoryGoplsOutput(input, symbolsByPath)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsOutput() error = %v", err)
	}
	if len(output.Proposals) != 2 {
		t.Fatalf("proposal count = %d, want 2", len(output.Proposals))
	}
	if output.Proposals[0].ProposalLocalID == output.Proposals[1].ProposalLocalID {
		t.Fatalf("proposal local IDs collide: %q", output.Proposals[0].ProposalLocalID)
	}
	for _, proposal := range output.Proposals {
		if proposal.CodeFact == nil || proposal.CodeFact.QualifiedName == "" {
			t.Fatalf("proposal is not code-fact grounded: %+v", proposal)
		}
	}
	analysis := repositoryGoplsAnalysis{symbolsByPath: symbolsByPath}
	defaultAnalysisOutput, err := materializeRepositoryGoplsAnalysis(input, analysis)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsAnalysis() error = %v", err)
	}
	var observed []string
	profiledAnalysisOutput, err := materializeRepositoryGoplsAnalysisProfiled(
		input,
		analysis,
		func(subphase repositoryExtractionSubphase, _ time.Duration, succeeded bool) {
			if !succeeded {
				t.Fatalf("subphase %s failed", subphase)
			}
			observed = append(observed, string(subphase))
		},
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsAnalysisProfiled() error = %v", err)
	}
	if !reflect.DeepEqual(profiledAnalysisOutput, defaultAnalysisOutput) {
		t.Fatalf(
			"profiled output differs:\nprofiled=%+v\ndefault=%+v",
			profiledAnalysisOutput,
			defaultAnalysisOutput,
		)
	}
	wantSubphases := []string{
		"extractor_declarations_packages",
		"extractor_definitions_references",
		"extractor_calls_incoming",
		"extractor_coverage_assembly",
	}
	if !reflect.DeepEqual(observed, wantSubphases) {
		t.Fatalf("observed subphases = %v, want %v", observed, wantSubphases)
	}
	output = repositoryGoplsTestOutput(t, input, output.Proposals, nil)

	runner := repositoryGoplsRunner{goplsVersion: "v-test"}
	definition, err := buildExtractorDefinition(runner.ExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:gopls-test", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	batch, err := materializeRepositoryBatch(attemptCtx, output)
	if err != nil {
		t.Fatalf("materializeRepositoryBatch() error = %v", err)
	}
	if len(batch.Occurrences) != 4 {
		t.Fatalf("occurrence count = %d, want 4", len(batch.Occurrences))
	}
}

func TestRepositoryGoplsAnalysisStageObserverPreservesOrderAndFailure(t *testing.T) {
	type observedStage struct {
		name      repositoryGoplsAnalysisStage
		count     int
		succeeded bool
	}
	var observed []observedStage
	observer := func(
		stage repositoryGoplsAnalysisStage,
		duration time.Duration,
		count int,
		succeeded bool,
	) {
		observed = append(observed, observedStage{
			name:      stage,
			count:     count,
			succeeded: succeeded,
		})
	}
	started := startRepositoryGoplsAnalysisStage(observer)
	finishRepositoryGoplsAnalysisStage(
		observer,
		repositoryGoplsAnalysisStageSyntaxInventory,
		started,
		2,
		nil,
	)
	started = startRepositoryGoplsAnalysisStage(observer)
	finishRepositoryGoplsAnalysisStage(
		observer,
		repositoryGoplsAnalysisStageDefinitions,
		started,
		3,
		errors.New("injected"),
	)
	want := []observedStage{
		{name: repositoryGoplsAnalysisStageSyntaxInventory, count: 2, succeeded: true},
		{name: repositoryGoplsAnalysisStageDefinitions, count: 3, succeeded: false},
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("observed stages = %+v, want %+v", observed, want)
	}
}

func TestMaterializeRepositoryGoplsOutputRejectsMissingDocumentResult(t *testing.T) {
	_, err := materializeRepositoryGoplsOutput(repositoryParserTestInput(t), map[string][]lspDocumentSymbol{})
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestValidateRepositoryGoplsAuthority(t *testing.T) {
	input := repositoryParserTestInput(t)
	observed := repositorySnapshotContext{
		snapshot: input.RepositorySnapshot,
		files:    make([]SourceFileSnapshot, 0, len(input.Files)),
		blobs:    make(map[string][]byte, len(input.Files)),
	}
	for _, file := range input.Files {
		observed.files = append(observed.files, file.FileSnapshot)
		observed.blobs[file.FileSnapshot.BlobHash] = append([]byte(nil), file.Content...)
	}
	if err := validateRepositoryGoplsAuthority(input, observed); err != nil {
		t.Fatalf("validateRepositoryGoplsAuthority() error = %v", err)
	}

	t.Run("database collation order differs", func(t *testing.T) {
		if len(observed.files) < 2 {
			t.Fatal("authority fixture requires at least two files")
		}
		reordered := observed
		reordered.files = append([]SourceFileSnapshot(nil), observed.files...)
		reordered.files[0], reordered.files[1] = reordered.files[1], reordered.files[0]
		if err := validateRepositoryGoplsAuthority(input, reordered); err != nil {
			t.Fatalf("validateRepositoryGoplsAuthority(reordered) error = %v", err)
		}
	})

	t.Run("manifest mismatch", func(t *testing.T) {
		conflict := observed
		conflict.snapshot.ManifestHash = "sha256:other"
		assertKind(t, validateRepositoryGoplsAuthority(input, conflict), ErrorRepositoryRevisionMismatch)
	})

	t.Run("blob mismatch", func(t *testing.T) {
		conflict := observed
		conflict.blobs = make(map[string][]byte, len(observed.blobs))
		for hash, content := range observed.blobs {
			conflict.blobs[hash] = append([]byte(nil), content...)
		}
		conflict.blobs[input.Files[0].FileSnapshot.BlobHash] = []byte("tampered")
		assertKind(t, validateRepositoryGoplsAuthority(input, conflict), ErrorRepositorySnapshotIntegrity)
	})
}

func TestRunRepositoryGoplsValidatesBeforeDatabaseMutation(t *testing.T) {
	var db sqlDB
	_, err := runRepositoryGoplsExtractor(context.Background(), db, RepositoryGoplsRequest{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = runRepositoryGoplsExtractor(context.Background(), db, RepositoryGoplsRequest{RequestID: "request"})
	assertKind(t, err, ErrorInvalidInput)
}

func TestRepositoryGoplsExtractorDefinitionRecordsVersionAndAuthority(t *testing.T) {
	runner := repositoryGoplsRunner{goplsVersion: "v0.23.0"}
	definition := runner.ExtractorDefinition()
	if definition.Name != ExtractorRepositoryGoplsCodeFact || definition.Version != ExtractorRepositoryGoplsCodeFactVersion {
		t.Fatalf("extractor definition = %+v", definition)
	}
	if definition.Config["gopls_version"] != "v0.23.0" || definition.Config["revision_binding"] != RepositoryRevisionVerificationGitV1 {
		t.Fatalf("extractor config = %+v", definition.Config)
	}
	if definition.Config["lsp_methods"] != "textDocument/documentSymbol,workspace/executeCommand:gopls.package_symbols,textDocument/definition,textDocument/references,textDocument/prepareCallHierarchy,callHierarchy/outgoingCalls,callHierarchy/incomingCalls" || definition.Config["output_schemas"] != CodeFactSchemaV1+","+CodeFactSchemaV2+","+CodeRelationSchemaV1+","+CodeRelationSchemaV2+","+CodeRelationSchemaV3 {
		t.Fatalf("extractor methods/schemas = %+v", definition.Config)
	}
	if definition.Config["coverage_schema"] != RepositoryGoplsCoverageSchemaV8 ||
		definition.Config["ambiguous_relation_semantics"] != "coverage_only_no_relation_proposal" ||
		definition.Config["reference_reconciliation"] == "" ||
		definition.Config["call_relation_scope"] == "" ||
		definition.Config["call_reconciliation"] == "" ||
		definition.Config["failure_coverage"] == "" {
		t.Fatalf("extractor coverage config = %+v", definition.Config)
	}
	if definition.Config["package_fact_scope"] != "file_bound_parser_grounded_package_clause_no_import_path" {
		t.Fatalf("extractor package scope = %+v", definition.Config)
	}
	if definition.Config["topology_scope"] != "package_clauses_declarations_same_and_cross_file_definitions_and_calls" || definition.Config["relation_coverage"] == "" {
		t.Fatalf("extractor relation scope = %+v", definition.Config)
	}
	if definition.Config["unselected_result_semantics"] != "coverage_counts_only_no_uri_path_or_relation_payload" {
		t.Fatalf("extractor unselected result semantics = %+v", definition.Config)
	}
}
