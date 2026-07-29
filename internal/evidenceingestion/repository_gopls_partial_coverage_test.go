package evidenceingestion

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/textproto"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoryGoplsSyntaxInventoryFailureReturnsCoverage(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	input.Files[0].Content = []byte("package sample\nfunc {")

	analysis, err := runRepositoryGoplsAnalysis(t.Context(), "unused-gopls", "/workspace", input, time.Second)
	if err == nil {
		t.Fatal("runRepositoryGoplsAnalysis() succeeded, want syntax failure")
	}
	if analysis.failureStage != repositoryGoplsStageSyntaxInventory {
		t.Fatalf("syntax failure stage = %q", analysis.failureStage)
	}
	coverage := buildRepositoryGoplsCoverage(input, analysis, nil, nil, nil, nil, nil)
	if coverage.RequestCoverageComplete || coverage.FailureStage != repositoryGoplsStageSyntaxInventory {
		t.Fatalf("syntax failure coverage = %+v", coverage)
	}
	if err := validateRepositoryGoplsCoverageCounts(coverage); err != nil {
		t.Fatalf("validateRepositoryGoplsCoverageCounts() error = %v", err)
	}
}

func TestRepositoryGoplsDefinitionFailureReturnsPartialCoverage(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "error": map[string]any{"code": -32603, "message": "injected definition failure"}},
	)
	filePaths := map[string]string{"sample.go": filepath.Join("/workspace", "sample.go")}

	definitions, err := requestRepositoryGoplsDefinitions(client, input.Files, filePaths)
	if err == nil {
		t.Fatal("requestRepositoryGoplsDefinitions() succeeded, want injected failure")
	}
	if definitions.requestCount != 2 || definitions.completedRequestCount != 1 || definitions.locationCount != 0 {
		t.Fatalf("partial definition progress = %+v", definitions)
	}

	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
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
		definitions:  definitions,
		failureStage: repositoryGoplsStageDefinition,
	}
	coverage := buildRepositoryGoplsCoverage(input, analysis, nil, nil, nil, nil, nil)
	if coverage.RequestCoverageComplete || coverage.FailureStage != repositoryGoplsStageDefinition {
		t.Fatalf("partial coverage status = %+v", coverage)
	}
	if coverage.ParsedIdentifierCount != 2 || coverage.DefinitionRequestCount != 2 || coverage.DefinitionCompletedRequestCount != 1 {
		t.Fatalf("partial definition coverage = %+v", coverage)
	}
	if coverage.SupportedCrossFileDefinitionCount != 0 || coverage.EmittedRelationCount != 0 {
		t.Fatalf("partial failure emitted semantic facts = %+v", coverage)
	}
	if err := validateRepositoryGoplsCoverageCounts(coverage); err != nil {
		t.Fatalf("validateRepositoryGoplsCoverageCounts() error = %v", err)
	}
	output := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{}, RepositoryGoplsCoverage: &coverage}
	_, err = materializeRepositoryGoplsTestOutput(t, input, output)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestRepositoryGoplsReferenceFailureReturnsPartialProgress(t *testing.T) {
	input := repositoryParserTestInput(t)
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "error": map[string]any{"code": -32603, "message": "injected reference failure"}},
	)
	filePaths := make(map[string]string, len(input.Files))
	for _, file := range input.Files {
		filePaths[file.FileSnapshot.Path] = filepath.Join("/workspace", file.FileSnapshot.Path)
	}

	references, err := requestRepositoryGoplsReferences(client, input, filePaths)
	if err == nil {
		t.Fatal("requestRepositoryGoplsReferences() succeeded, want injected failure")
	}
	if references.requestCount != 2 || references.completedRequestCount != 1 || references.locationCount != 0 {
		t.Fatalf("partial reference progress = %+v", references)
	}
}

func TestRepositoryGoplsOutgoingFailureReturnsPartialProgress(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	declaration := repositoryTestDeclaration(t, input.RepositorySnapshot, "sample.go", input.Files[0].Content, "sample.Run")
	start, err := lspPositionForByteOffset(input.Files[0].Content, declaration.StartByte)
	if err != nil {
		t.Fatalf("lspPositionForByteOffset(start) error = %v", err)
	}
	end, err := lspPositionForByteOffset(input.Files[0].Content, declaration.EndByte)
	if err != nil {
		t.Fatalf("lspPositionForByteOffset(end) error = %v", err)
	}
	path := filepath.Join("/workspace", "sample.go")
	item := lspCallHierarchyItem{
		Name:           "Run",
		Kind:           12,
		URI:            fileURI(path),
		Range:          lspRange{Start: start, End: end},
		SelectionRange: lspRange{Start: start, End: end},
	}
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspCallHierarchyItem{item}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "error": map[string]any{"code": -32603, "message": "injected outgoing failure"}},
	)

	calls, err := requestRepositoryGoplsCalls(client, input, map[string]string{"sample.go": path})
	if err == nil {
		t.Fatal("requestRepositoryGoplsCalls() succeeded, want injected failure")
	}
	if calls.failureStage != repositoryGoplsStageOutgoingCalls {
		t.Fatalf("call failure stage = %q", calls.failureStage)
	}
	if calls.prepareRequestCount != 1 || calls.prepareCompletedRequestCount != 1 || calls.preparedItemCount != 1 {
		t.Fatalf("partial prepare progress = %+v", calls)
	}
	if calls.outgoingRequestCount != 1 || calls.outgoingCompletedRequestCount != 0 || calls.outgoingCallResultCount != 0 || calls.outgoingCallSiteCount != 0 {
		t.Fatalf("partial outgoing progress = %+v", calls)
	}
}

func TestRepositoryGoplsIncomingFailureReturnsPartialProgress(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	declaration := repositoryTestDeclaration(t, input.RepositorySnapshot, "sample.go", input.Files[0].Content, "sample.Run")
	path := filepath.Join("/workspace", "sample.go")
	item := testCallHierarchyItem(t, path, input.Files[0].Content, declaration)
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspCallHierarchyItem{item}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspCallHierarchyOutgoingCall{}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "error": map[string]any{"code": -32603, "message": "injected incoming failure"}},
	)

	calls, err := requestRepositoryGoplsCalls(client, input, map[string]string{"sample.go": path})
	if err == nil {
		t.Fatal("requestRepositoryGoplsCalls() succeeded, want injected failure")
	}
	if calls.failureStage != repositoryGoplsStageIncomingCalls {
		t.Fatalf("call failure stage = %q", calls.failureStage)
	}
	if calls.prepareRequestCount != 1 || calls.prepareCompletedRequestCount != 1 || calls.preparedItemCount != 1 || calls.outgoingRequestCount != 1 || calls.outgoingCompletedRequestCount != 1 {
		t.Fatalf("partial outgoing progress = %+v", calls)
	}
	if calls.incomingRequestCount != 1 || calls.incomingCompletedRequestCount != 0 || calls.incomingCallResultCount != 0 || calls.incomingCallSiteCount != 0 {
		t.Fatalf("partial incoming progress = %+v", calls)
	}
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
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
			requestCount:          syntaxCounts.identifiers,
			completedRequestCount: syntaxCounts.identifiers,
		},
		references: repositoryGoplsRelationCollection{
			requestCount:          syntaxCounts.declarations,
			completedRequestCount: syntaxCounts.declarations,
		},
		calls:        calls,
		failureStage: repositoryGoplsStageIncomingCalls,
	}
	coverage := buildRepositoryGoplsCoverage(input, analysis, nil, nil, nil, nil, nil)
	if coverage.RequestCoverageComplete || coverage.FailureStage != repositoryGoplsStageIncomingCalls || coverage.IncomingCallRequestCount != 1 || coverage.IncomingCallCompletedRequestCount != 0 || coverage.EmittedRelationCount != 0 {
		t.Fatalf("partial incoming coverage = %+v", coverage)
	}
	if err := validateRepositoryGoplsCoverageCounts(coverage); err != nil {
		t.Fatalf("validateRepositoryGoplsCoverageCounts() error = %v", err)
	}
}

func TestRepositoryGoplsPrepareFailureReturnsPartialProgress(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32603, "message": "injected prepare failure"}},
	)
	path := filepath.Join("/workspace", "sample.go")

	calls, err := requestRepositoryGoplsCalls(client, input, map[string]string{"sample.go": path})
	if err == nil {
		t.Fatal("requestRepositoryGoplsCalls() succeeded, want injected failure")
	}
	if calls.failureStage != repositoryGoplsStagePrepareCallHierarchy {
		t.Fatalf("call failure stage = %q", calls.failureStage)
	}
	if calls.prepareRequestCount != 1 || calls.prepareCompletedRequestCount != 0 || calls.preparedItemCount != 0 || calls.outgoingRequestCount != 0 {
		t.Fatalf("partial prepare progress = %+v", calls)
	}
}

func TestFailRepositoryAttemptPersistsCoverageWithoutProposals(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
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
	output := repositoryGoplsFailureOutput(input, analysis, repositoryGoplsStageDefinition)
	db := newMockSQLDB()
	const attemptID = "attempt:partial-coverage"
	db.extractionAttempts[attemptID] = mockExtractionAttempt{status: attemptStatusStarted}
	attemptCtx := repositoryAttemptContext{extractionAttempt: ExtractionAttempt{ID: attemptID}}
	cause := newDomainError(ErrorRunnerInvocationFailed, "injected gopls failure")

	err = failRepositoryAttempt(t.Context(), db, attemptCtx, output, cause)
	assertKind(t, err, ErrorRunnerInvocationFailed)
	attempt := db.extractionAttempts[attemptID]
	if attempt.status != attemptStatusFailed || attempt.failureClass != string(ErrorRunnerInvocationFailed) || attempt.outputHash == "" || attempt.fixtureOutput == "" {
		t.Fatalf("persisted partial attempt = %+v", attempt)
	}
	var persisted FrozenExtractorOutput
	if err := json.Unmarshal([]byte(attempt.fixtureOutput), &persisted); err != nil {
		t.Fatalf("decode persisted partial output: %v", err)
	}
	if len(persisted.Proposals) != 0 || persisted.RepositoryGoplsCoverage == nil {
		t.Fatalf("persisted partial output = %+v", persisted)
	}
	coverage := persisted.RepositoryGoplsCoverage
	if coverage.RequestCoverageComplete || coverage.FailureStage != repositoryGoplsStageDefinition || coverage.DefinitionCompletedRequestCount != 1 {
		t.Fatalf("persisted partial coverage = %+v", coverage)
	}
	if len(db.proposalBatches) != 0 || len(db.proposalOccurrences) != 0 {
		t.Fatalf("partial failure persisted proposals: batches=%d occurrences=%d", len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func repositoryGoplsPartialTestInput() RepositoryExtractorInput {
	source := []byte("package sample\n\nfunc Run() {}\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:partial", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 1}
	file := RepositoryExtractorFile{
		FileSnapshot: SourceFileSnapshot{
			ID:                   "file:sample.go",
			RepositorySnapshotID: snapshot.ID,
			RepoID:               snapshot.RepoID,
			CommitSHA:            snapshot.CommitSHA,
			Path:                 "sample.go",
			BlobHash:             contentHash(source),
			ByteLength:           len(source),
		},
		Content: source,
	}
	return RepositoryExtractorInput{RepositorySnapshot: snapshot, Files: []RepositoryExtractorFile{file}}
}

func repositoryGoplsFailureTestClient(t *testing.T, messages ...map[string]any) *lspClient {
	t.Helper()
	var responses []byte
	for _, message := range messages {
		responses = append(responses, lspTestFrame(t, message)...)
	}
	return &lspClient{
		reader: textproto.NewReader(bufio.NewReader(bytes.NewReader(responses))),
		writer: &bytes.Buffer{},
	}
}
