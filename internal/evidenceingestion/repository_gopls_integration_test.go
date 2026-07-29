//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIntegrationRepositoryGoplsPartialCoveragePersistsAndRetries(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root := t.TempDir()
	source := []byte("package sample\n\nfunc Run() {}\n")
	writeGitSnapshotTestFile(t, filepath.Join(root, "sample.go"), source)
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	snapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "repository-gopls-partial",
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     "repository-gopls-partial-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	db := pgxDB{pool: pool}
	input, err := buildRepositoryExtractorInput(ctx, db, snapshot.RepositorySnapshot.ID)
	if err != nil {
		t.Fatalf("buildRepositoryExtractorInput() error = %v", err)
	}
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
	definition := (&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition()
	const requestID = "repository-gopls-partial-run"
	invocations := 0
	partialExtractor := func(_ context.Context, _ RepositoryExtractorInput) (FrozenExtractorOutput, error) {
		invocations++
		analysis := repositoryGoplsAnalysis{
			symbolsByPath:              map[string][]lspDocumentSymbol{"sample.go": {}},
			syntaxCounts:               syntaxCounts,
			documentSymbolRequestCount: 1,
			packages: repositoryGoplsPackageCollection{
				packages:              []repositoryGoplsPackage{{path: "sample.go", name: "sample"}},
				requestCount:          1,
				completedRequestCount: 1,
				resultFileCount:       1,
			},
			definitions: repositoryGoplsRelationCollection{
				requestCount:          2,
				completedRequestCount: 1,
			},
			failureStage: repositoryGoplsStageDefinition,
		}
		return repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageDefinition), newDomainError(ErrorRunnerInvocationFailed, "injected definition failure")
	}

	_, err = runRepositoryExtractor(ctx, db, input, requestID, false, definition, partialExtractor)
	assertKind(t, err, ErrorRunnerInvocationFailed)
	if invocations != 1 {
		t.Fatalf("partial extractor invocations = %d, want 1", invocations)
	}
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertTableCount(t, ctx, pool, "repository_source_generations", 0)

	var status, failureClass, outputHash string
	var fixtureOutput []byte
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_class, output_hash, fixture_output
		FROM extraction_attempts
		WHERE attempt_number = 1
	`).Scan(&status, &failureClass, &outputHash, &fixtureOutput); err != nil {
		t.Fatalf("reading partial repository attempt: %v", err)
	}
	if status != attemptStatusFailed || failureClass != string(ErrorRunnerInvocationFailed) || outputHash == "" {
		t.Fatalf("partial attempt status/class/hash = %s/%s/%s", status, failureClass, outputHash)
	}
	var persisted FrozenExtractorOutput
	if err := json.Unmarshal(fixtureOutput, &persisted); err != nil {
		t.Fatalf("decoding partial fixture output: %v", err)
	}
	if len(persisted.Proposals) != 0 || persisted.RepositoryGoplsCoverage == nil {
		t.Fatalf("partial fixture output = %+v", persisted)
	}
	partialCoverage := persisted.RepositoryGoplsCoverage
	if partialCoverage.RequestCoverageComplete || partialCoverage.FailureStage != repositoryGoplsStageDefinition || partialCoverage.DefinitionRequestCount != 2 || partialCoverage.DefinitionCompletedRequestCount != 1 || partialCoverage.EmittedRelationCount != 0 {
		t.Fatalf("persisted partial coverage = %+v", partialCoverage)
	}

	_, err = runRepositoryExtractor(ctx, db, input, requestID, false, definition, partialExtractor)
	assertKind(t, err, ErrorPersistedAttemptFailed)
	if invocations != 1 {
		t.Fatalf("failed replay reinvoked extractor %d times", invocations)
	}

	symbols := map[string][]lspDocumentSymbol{
		"sample.go": {testDocumentSymbol(t, source, "Run", 12)},
	}
	successOutput, err := materializeRepositoryGoplsOutput(input, symbols)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsOutput() error = %v", err)
	}
	successOutput = repositoryGoplsTestOutput(t, input, successOutput.Proposals, nil)
	result, err := runRepositoryExtractor(ctx, db, input, requestID, true, definition, func(_ context.Context, _ RepositoryExtractorInput) (FrozenExtractorOutput, error) {
		invocations++
		return successOutput, nil
	})
	if err != nil {
		t.Fatalf("retry runRepositoryExtractor() error = %v", err)
	}
	if result.Replayed || result.ProposalCount != 2 || result.RepositoryGoplsCoverage == nil || !result.RepositoryGoplsCoverage.RequestCoverageComplete {
		t.Fatalf("retry result = %+v", result)
	}
	if result.SourceGenerationReplayed || result.SourceGeneration.ID == "" || result.SourceGeneration.ExtractorName != ExtractorRepositoryGoplsCodeFact || result.SourceGeneration.ProposalBatchID != result.ProposalBatchID {
		t.Fatalf("retry automatic generation = %+v, result = %+v", result.SourceGeneration, result)
	}
	if invocations != 2 {
		t.Fatalf("retry extractor invocations = %d, want 2", invocations)
	}
	assertTableCount(t, ctx, pool, "extraction_attempts", 2)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 2)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
}

func TestIntegrationRunRepositoryGoplsPersistsGroundedMultiFileBatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not on PATH")
	}

	ctx, pool := integrationPool(t)
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module example.com/repository-gopls\n\ngo 1.22\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte("package sample\n\nimport (\n\t\"fmt\"\n\t\"example.com/repository-gopls/worker\"\n)\n\nfunc helper() {}\n\nfunc Main() {\n\thelper()\n\tworker.Run()\n\tfmt.Println()\n}\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "worker", "worker.go"), []byte("package worker\n\ntype Worker struct{}\n\nfunc Run() {}\n"))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")

	snapshot, err := CaptureGitRepositorySnapshot(ctx, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "repository-gopls",
		CommitSHA:     gitSnapshotTestOutput(t, root, "rev-parse", "HEAD"),
		RequestID:     "repository-gopls-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	request := RepositoryGoplsRequest{
		RequestID:            "repository-gopls-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		WorkspaceRoot:        root,
	}
	result, err := RunRepositoryGoplsExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("RunRepositoryGoplsExtractor() error = %v", err)
	}
	if result.Replayed || result.ProposalCount != 10 || len(result.ProposalOccurrenceIDs) != 10 {
		t.Fatalf("repository gopls result = %+v", result)
	}
	if result.SourceGenerationReplayed || result.SourceGeneration.ID == "" || result.SourceGeneration.Number != 1 || result.SourceGeneration.ExtractorName != ExtractorRepositoryGoplsCodeFact || result.SourceGeneration.RepositorySnapshotID != result.RepositorySnapshotID || result.SourceGeneration.ExtractionAttemptID != result.ExtractionAttemptID || result.SourceGeneration.ProposalBatchID != result.ProposalBatchID || result.SourceGeneration.ExtractorOutputHash != result.OutputHash || result.SourceGeneration.ProposalCount != result.ProposalCount {
		t.Fatalf("repository gopls automatic generation = %+v, result = %+v", result.SourceGeneration, result)
	}
	coverage := result.RepositoryGoplsCoverage
	if coverage == nil || coverage.SchemaVersion != RepositoryGoplsCoverageSchemaV8 || !coverage.RequestCoverageComplete || coverage.FailureStage != "" {
		t.Fatalf("repository gopls coverage = %+v", coverage)
	}
	if coverage.SelectedFileCount != 2 || coverage.DocumentSymbolRequestCount != 2 || coverage.DocumentSymbolFileCount != 2 || coverage.DefinitionCompletedRequestCount != coverage.DefinitionRequestCount || coverage.GroundedDeclarationCount != 4 || coverage.ReferenceRequestCount != 4 || coverage.ReferenceCompletedRequestCount != 4 {
		t.Fatalf("repository gopls file/declaration coverage = %+v", coverage)
	}
	if coverage.ParsedPackageClauseCount != 2 || coverage.PackageRequestCount != 2 || coverage.PackageCompletedRequestCount != 2 || coverage.PackageResultFileCount < 2 || coverage.SupportedPackageFactCount != 2 || coverage.EmittedPackageFactCount != 2 {
		t.Fatalf("repository gopls package coverage = %+v", coverage)
	}
	if coverage.SupportedCrossFileDefinitionCount != 1 || coverage.SupportedSameFileDefinitionCount != 1 || coverage.SupportedCrossFileReferenceCount != 1 || coverage.SupportedSameFileReferenceCount != 1 || coverage.CorroboratedRelationCount != 2 {
		t.Fatalf("repository gopls relation coverage = %+v", coverage)
	}
	if coverage.DefinitionUnselectedLocationCount == 0 {
		t.Fatalf("repository gopls unselected definition coverage = %+v", coverage)
	}
	if coverage.CallableDeclarationCount != 3 || coverage.PrepareCallHierarchyRequestCount != 3 || coverage.PrepareCallHierarchyCompletedRequestCount != 3 || coverage.PreparedCallHierarchyItemCount != 3 || coverage.OutgoingCallRequestCount != 3 || coverage.OutgoingCallCompletedRequestCount != 3 {
		t.Fatalf("repository gopls call request coverage = %+v", coverage)
	}
	if coverage.IncomingCallRequestCount != 3 || coverage.IncomingCallCompletedRequestCount != 3 {
		t.Fatalf("repository gopls incoming call request coverage = %+v", coverage)
	}
	if coverage.OutgoingCallResultCount != 3 || coverage.OutgoingCallSiteCount != 3 || coverage.OutgoingUnselectedCallResultCount != 1 || coverage.OutgoingUnselectedCallSiteCount != 1 || coverage.SupportedCrossFileCallCount != 1 || coverage.SupportedSameFileCallCount != 1 || coverage.IncomingCallResultCount != 2 || coverage.IncomingCallSiteCount != 2 || coverage.IncomingUnselectedCallResultCount != 0 || coverage.IncomingUnselectedCallSiteCount != 0 || coverage.SupportedIncomingCrossFileCallCount != 1 || coverage.SupportedIncomingSameFileCallCount != 1 || coverage.CorroboratedCallRelationCount != 2 || coverage.OutgoingOnlyCallRelationCount != 0 || coverage.IncomingOnlyCallRelationCount != 0 || coverage.EmittedCallRelationCount != 2 || coverage.EmittedRelationCount != 4 {
		t.Fatalf("repository gopls call relation coverage = %+v", coverage)
	}
	if coverage.DefinitionOnlyRelationCount != 0 || coverage.ReferenceOnlyRelationCount != 0 || coverage.MissingResultSemantics != missingSemanticResultUnknown {
		t.Fatalf("repository gopls reconciliation coverage = %+v", coverage)
	}

	wantPaths := map[string]int{"main.go": 3, "worker/worker.go": 3}
	gotPaths := make(map[string]int, len(wantPaths))
	packageCount := 0
	definitionCount := 0
	callCount := 0
	sameFileDefinitionCount := 0
	sameFileCallCount := 0
	var packageOccurrenceID string
	var callOccurrenceID string
	for _, occurrenceID := range result.ProposalOccurrenceIDs {
		proposal, err := GetProposalByOccurrenceID(ctx, pool, occurrenceID)
		if err != nil {
			t.Fatalf("GetProposalByOccurrenceID(%q) error = %v", occurrenceID, err)
		}
		if proposal.ExtractorName != ExtractorRepositoryGoplsCodeFact || proposal.ExtractorVersion != ExtractorRepositoryGoplsCodeFactVersion {
			t.Fatalf("proposal extractor = %s/%s", proposal.ExtractorName, proposal.ExtractorVersion)
		}
		if proposal.RepositorySnapshot == nil || proposal.RepositorySnapshot.ID != snapshot.RepositorySnapshot.ID {
			t.Fatalf("proposal repository binding = %+v", proposal)
		}
		if proposal.SourceGeneration == nil || *proposal.SourceGeneration != result.SourceGeneration || proposal.SourceGenerationActive {
			t.Fatalf("repository gopls proposal generation = %+v active %t, want inactive %+v", proposal.SourceGeneration, proposal.SourceGenerationActive, result.SourceGeneration)
		}
		if (proposal.CodeFact == nil) == (proposal.CodeRelation == nil) {
			t.Fatalf("proposal must have exactly one code payload = %+v", proposal)
		}
		if proposal.CodeRelation != nil {
			switch proposal.CodeRelation.RelationKind {
			case CodeRelationKindDefinition:
				definitionCount++
				if proposal.CodeRelation.Caller != nil || proposal.CodeRelation.Usage.Path != "main.go" {
					t.Fatalf("definition relation = %+v", proposal.CodeRelation)
				}
				if proposal.CodeRelation.Target.Path == proposal.CodeRelation.Usage.Path {
					sameFileDefinitionCount++
					if proposal.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV3 || proposal.CodeRelation.SchemaVersion != CodeRelationSchemaV3 || proposal.CodeRelation.Target.QualifiedName != "sample.helper" {
						t.Fatalf("same-file definition relation = %+v", proposal)
					}
				} else if proposal.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV1 || proposal.CodeRelation.SchemaVersion != CodeRelationSchemaV1 || proposal.CodeRelation.Target.QualifiedName != "worker.Run" {
					t.Fatalf("cross-file definition relation = %+v", proposal)
				}
				if len(proposal.SourceRefs) != 2 {
					t.Fatalf("definition source refs = %+v", proposal.SourceRefs)
				}
			case CodeRelationKindCall:
				callCount++
				if proposal.CodeRelation.Caller == nil || proposal.CodeRelation.Caller.QualifiedName != "sample.Main" || proposal.CodeRelation.Usage.Path != "main.go" {
					t.Fatalf("call relation = %+v", proposal.CodeRelation)
				}
				if proposal.CodeRelation.Target.Path == proposal.CodeRelation.Usage.Path {
					sameFileCallCount++
					if proposal.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV3 || proposal.CodeRelation.SchemaVersion != CodeRelationSchemaV3 || proposal.CodeRelation.Target.QualifiedName != "sample.helper" {
						t.Fatalf("same-file call relation = %+v", proposal)
					}
				} else {
					callOccurrenceID = occurrenceID
					if proposal.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV2 || proposal.CodeRelation.SchemaVersion != CodeRelationSchemaV2 || proposal.CodeRelation.Target.QualifiedName != "worker.Run" {
						t.Fatalf("cross-file call relation = %+v", proposal)
					}
				}
				if len(proposal.SourceRefs) != 3 {
					t.Fatalf("call source refs = %+v", proposal.SourceRefs)
				}
			default:
				t.Fatalf("unexpected relation = %+v", proposal.CodeRelation)
			}
			continue
		}
		if proposal.CodeFact.FactKind == CodeFactKindPackage {
			packageCount++
			if packageOccurrenceID == "" {
				packageOccurrenceID = occurrenceID
			}
			if proposal.CodeFact.SchemaVersion != CodeFactSchemaV2 || proposal.CodeFact.SymbolKind != CodeFactKindPackage || proposal.CodeFact.QuotedText != proposal.CodeFact.QualifiedName || len(proposal.SourceRefs) != 1 {
				t.Fatalf("package fact = %+v refs = %+v", proposal.CodeFact, proposal.SourceRefs)
			}
		} else if proposal.CodeFact.SchemaVersion != CodeFactSchemaV1 || proposal.CodeFact.FactKind != CodeFactKindDeclaration {
			t.Fatalf("declaration fact = %+v", proposal.CodeFact)
		}
		gotPaths[proposal.CodeFact.Path]++
	}
	if packageCount != 2 || definitionCount != 2 || sameFileDefinitionCount != 1 || callCount != 2 || sameFileCallCount != 1 {
		t.Fatalf("package/definition/same-file-definition/call/same-file-call counts = %d/%d/%d/%d/%d, want 2/2/1/2/1", packageCount, definitionCount, sameFileDefinitionCount, callCount, sameFileCallCount)
	}
	for path, want := range wantPaths {
		if gotPaths[path] != want {
			t.Fatalf("proposal count for %s = %d, want %d; all paths = %v", path, gotPaths[path], want, gotPaths)
		}
	}
	admission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: callOccurrenceID,
		DecisionBy:           "repository-gopls-integration-test",
		DecisionReason:       "verified cross-file call accepted",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(call) error = %v", err)
	}
	canonical, err := GetCanonicalEvidenceByID(ctx, pool, admission.CanonicalRef)
	if err != nil {
		t.Fatalf("GetCanonicalEvidenceByID(call) error = %v", err)
	}
	if canonical.OriginProposal.CodeRelation == nil || canonical.OriginProposal.CodeRelation.Caller == nil || canonical.OriginProposal.CodeRelation.Caller.QualifiedName != "sample.Main" || canonical.OriginProposal.CodeRelation.Target.QualifiedName != "worker.Run" {
		t.Fatalf("canonical call origin = %+v", canonical.OriginProposal)
	}
	packageAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: packageOccurrenceID,
		DecisionBy:           "repository-gopls-integration-test",
		DecisionReason:       "verified package clause accepted",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(package) error = %v", err)
	}
	packageCanonical, err := GetCanonicalEvidenceByID(ctx, pool, packageAdmission.CanonicalRef)
	if err != nil {
		t.Fatalf("GetCanonicalEvidenceByID(package) error = %v", err)
	}
	if packageCanonical.OriginProposal.CodeFact == nil || packageCanonical.OriginProposal.CodeFact.SchemaVersion != CodeFactSchemaV2 || packageCanonical.OriginProposal.CodeFact.FactKind != CodeFactKindPackage {
		t.Fatalf("canonical package origin = %+v", packageCanonical.OriginProposal)
	}

	replay, err := RunRepositoryGoplsExtractor(ctx, pool, request)
	if err != nil {
		t.Fatalf("replay RunRepositoryGoplsExtractor() error = %v", err)
	}
	if !replay.Replayed || !replay.SourceGenerationReplayed || replay.ExtractionAttemptID != result.ExtractionAttemptID || replay.ProposalBatchID != result.ProposalBatchID || replay.ProposalCount != result.ProposalCount || replay.SourceGeneration != result.SourceGeneration {
		t.Fatalf("replay = %+v, want identifiers from %+v", replay, result)
	}
	if replay.RepositoryGoplsCoverage == nil || *replay.RepositoryGoplsCoverage != *result.RepositoryGoplsCoverage {
		t.Fatalf("replayed coverage = %+v, want %+v", replay.RepositoryGoplsCoverage, result.RepositoryGoplsCoverage)
	}
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 10)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
}
