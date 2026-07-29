package evidenceingestion

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRepositoryGoplsCallsReconcilesIncomingAndOutgoing(t *testing.T) {
	input, _ := repositoryCodeCallTestFixture(t)
	target := repositoryTestDeclaration(t, input.RepositorySnapshot, "target.go", input.Files[0].Content, "sample.Run")
	caller := repositoryTestDeclaration(t, input.RepositorySnapshot, "caller.go", input.Files[1].Content, "sample.Main")
	filePaths := map[string]string{
		"target.go": filepath.Join("/workspace", "target.go"),
		"caller.go": filepath.Join("/workspace", "caller.go"),
	}
	targetItem := testCallHierarchyItem(t, filePaths[target.Path], input.Files[0].Content, target)
	callerItem := testCallHierarchyItem(t, filePaths[caller.Path], input.Files[1].Content, caller)
	usageStart := bytes.LastIndex(input.Files[1].Content, []byte("Run"))
	usageRange := testLSPRange(t, input.Files[1].Content, usageStart, usageStart+len("Run"))
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspCallHierarchyItem{targetItem}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspCallHierarchyOutgoingCall{}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "result": []lspCallHierarchyIncomingCall{{From: callerItem, FromRanges: []lspRange{usageRange}}}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "result": []lspCallHierarchyItem{callerItem}},
		map[string]any{"jsonrpc": "2.0", "id": 5, "result": []lspCallHierarchyOutgoingCall{{To: targetItem, FromRanges: []lspRange{usageRange}}}},
		map[string]any{"jsonrpc": "2.0", "id": 6, "result": []lspCallHierarchyIncomingCall{}},
	)

	calls, err := requestRepositoryGoplsCalls(client, input, filePaths)
	if err != nil {
		t.Fatalf("requestRepositoryGoplsCalls() error = %v", err)
	}
	if calls.prepareRequestCount != 2 || calls.preparedItemCount != 2 || calls.outgoingCompletedRequestCount != 2 || calls.incomingCompletedRequestCount != 2 {
		t.Fatalf("call request coverage = %+v", calls)
	}
	if calls.outgoingCallResultCount != 1 || calls.outgoingCallSiteCount != 1 || calls.incomingCallResultCount != 1 || calls.incomingCallSiteCount != 1 {
		t.Fatalf("call result coverage = %+v", calls)
	}
	if len(calls.calls) != 1 || len(calls.incomingCalls) != 1 || compareRepositoryGoplsCall(calls.calls[0], calls.incomingCalls[0]) != 0 {
		t.Fatalf("outgoing/incoming calls = %+v / %+v", calls.calls, calls.incomingCalls)
	}
	incomingProposals, err := materializeRepositoryGoplsIncomingCalls(input, calls.incomingCalls)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsIncomingCalls() error = %v", err)
	}
	if len(incomingProposals) != 1 {
		t.Fatalf("incoming proposals = %d, want 1", len(incomingProposals))
	}
	if err := verifyRepositoryGoplsDiagnosticRelationProposals(input, incomingProposals); err != nil {
		t.Fatalf("verifyRepositoryGoplsDiagnosticRelationProposals() error = %v", err)
	}
}

func TestRepositoryGoplsSameFileCallsReconcileAndMaterialize(t *testing.T) {
	input, proposal := repositorySameFileCallTestFixture(t)
	file := input.Files[0]
	target := repositoryTestDeclaration(t, input.RepositorySnapshot, file.FileSnapshot.Path, file.Content, "sample.Run")
	caller := repositoryTestDeclaration(t, input.RepositorySnapshot, file.FileSnapshot.Path, file.Content, "sample.Main")
	path := filepath.Join("/workspace", file.FileSnapshot.Path)
	targetItem := testCallHierarchyItem(t, path, file.Content, target)
	callerItem := testCallHierarchyItem(t, path, file.Content, caller)
	usageStart := bytes.LastIndex(file.Content, []byte("Run"))
	usageRange := testLSPRange(t, file.Content, usageStart, usageStart+len("Run"))
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspCallHierarchyItem{targetItem}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspCallHierarchyOutgoingCall{}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "result": []lspCallHierarchyIncomingCall{{From: callerItem, FromRanges: []lspRange{usageRange}}}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "result": []lspCallHierarchyItem{callerItem}},
		map[string]any{"jsonrpc": "2.0", "id": 5, "result": []lspCallHierarchyOutgoingCall{{To: targetItem, FromRanges: []lspRange{usageRange}}}},
		map[string]any{"jsonrpc": "2.0", "id": 6, "result": []lspCallHierarchyIncomingCall{}},
	)

	calls, err := requestRepositoryGoplsCalls(client, input, map[string]string{file.FileSnapshot.Path: path})
	if err != nil {
		t.Fatalf("requestRepositoryGoplsCalls() error = %v", err)
	}
	if len(calls.calls) != 1 || len(calls.incomingCalls) != 1 || compareRepositoryGoplsCall(calls.calls[0], calls.incomingCalls[0]) != 0 {
		t.Fatalf("same-file outgoing/incoming calls = %+v / %+v", calls.calls, calls.incomingCalls)
	}
	incoming, err := materializeRepositoryGoplsIncomingCalls(input, calls.incomingCalls)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsIncomingCalls() error = %v", err)
	}
	if len(incoming) != 1 || incoming[0].ProposalLocalID != proposal.ProposalLocalID {
		t.Fatalf("same-file incoming proposals = %+v, want local ID %q", incoming, proposal.ProposalLocalID)
	}

	output := repositoryGoplsTestOutputWithCallCoverage(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal}, incoming)
	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsTestOutput() error = %v", err)
	}
	var occurrence ProposalOccurrence
	for _, candidate := range batch.Occurrences {
		if candidate.CodeRelation != nil {
			occurrence = candidate
		}
	}
	if occurrence.CodeRelation == nil || occurrence.CodeRelation.SchemaVersion != CodeRelationSchemaV3 || occurrence.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV3 {
		t.Fatalf("same-file call occurrence = %+v", occurrence)
	}
	if len(occurrence.SourceRefs) != 3 {
		t.Fatalf("same-file call source refs = %+v", occurrence.SourceRefs)
	}
	coverage := batch.RepositoryGoplsCoverage
	if coverage == nil || coverage.SupportedCrossFileCallCount != 0 || coverage.SupportedSameFileCallCount != 1 || coverage.SupportedIncomingSameFileCallCount != 1 || coverage.CorroboratedCallRelationCount != 1 {
		t.Fatalf("same-file call coverage = %+v", coverage)
	}
}

func TestRepositoryGoplsIncomingReferenceMustBeCallExpression(t *testing.T) {
	targetSource := []byte("package sample\n\nfunc Run() {}\n")
	callerSource := []byte("package sample\n\nfunc Main() {\n\t_ = Run\n}\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:incoming-reference", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 2}
	input := RepositoryExtractorInput{
		RepositorySnapshot: snapshot,
		Files: []RepositoryExtractorFile{
			repositoryCodeDefinitionTestFile(snapshot, "target.go", targetSource),
			repositoryCodeDefinitionTestFile(snapshot, "caller.go", callerSource),
		},
	}
	target := repositoryTestDeclaration(t, snapshot, "target.go", targetSource, "sample.Run")
	caller := repositoryTestDeclaration(t, snapshot, "caller.go", callerSource, "sample.Main")
	usageStart := bytes.LastIndex(callerSource, []byte("Run"))
	proposals, err := materializeRepositoryGoplsIncomingCalls(input, []repositoryGoplsCall{{
		callerPath:      caller.Path,
		callerStartByte: caller.StartByte,
		callerEndByte:   caller.EndByte,
		usagePath:       caller.Path,
		usageStartByte:  usageStart,
		usageEndByte:    usageStart + len("Run"),
		targetPath:      target.Path,
		targetStartByte: target.StartByte,
		targetEndByte:   target.EndByte,
	}})
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsCalls() error = %v", err)
	}
	if len(proposals) != 0 {
		t.Fatalf("incoming non-call reference proposals = %d, want 0", len(proposals))
	}
}

func testCallHierarchyItem(t *testing.T, path string, source []byte, declaration GoParserDeclaration) lspCallHierarchyItem {
	t.Helper()
	selection := testLSPRange(t, source, declaration.StartByte, declaration.EndByte)
	return lspCallHierarchyItem{
		Name:           declaration.Name,
		Kind:           12,
		URI:            fileURI(path),
		Range:          selection,
		SelectionRange: selection,
	}
}

func testLSPRange(t *testing.T, source []byte, startByte, endByte int) lspRange {
	t.Helper()
	start, err := lspPositionForByteOffset(source, startByte)
	if err != nil {
		t.Fatalf("lspPositionForByteOffset(start) error = %v", err)
	}
	end, err := lspPositionForByteOffset(source, endByte)
	if err != nil {
		t.Fatalf("lspPositionForByteOffset(end) error = %v", err)
	}
	return lspRange{Start: start, End: end}
}

func TestMaterializeRepositoryCodeCallRelation(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	output := repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal})
	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryBatch() error = %v", err)
	}
	if len(batch.Occurrences) != len(input.Files)+1 {
		t.Fatalf("occurrence count = %d, want %d", len(batch.Occurrences), len(input.Files)+1)
	}
	var occurrence ProposalOccurrence
	for _, candidate := range batch.Occurrences {
		if candidate.CodeRelation != nil {
			occurrence = candidate
		}
	}
	if occurrence.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV2 {
		t.Fatalf("fingerprint version = %q", occurrence.ProposalFingerprintVersion)
	}
	if occurrence.CodeRelation == nil || occurrence.CodeRelation.Caller == nil {
		t.Fatalf("resolved call relation = %+v", occurrence.CodeRelation)
	}
	if occurrence.CodeRelation.RelationKind != CodeRelationKindCall || occurrence.CodeRelation.Caller.QualifiedName != "sample.Main" || occurrence.CodeRelation.Target.QualifiedName != "sample.Run" {
		t.Fatalf("resolved call relation = %+v", occurrence.CodeRelation)
	}
	if occurrence.CodeRelation.Usage.QuotedText != "Run" || len(occurrence.SourceRefs) != 3 {
		t.Fatalf("resolved call site/source refs = %+v / %+v", occurrence.CodeRelation.Usage, occurrence.SourceRefs)
	}
	if batch.RepositoryGoplsCoverage == nil || batch.RepositoryGoplsCoverage.EmittedCallRelationCount != 1 || batch.RepositoryGoplsCoverage.CorroboratedCallRelationCount != 1 || batch.RepositoryGoplsCoverage.EmittedRelationCount != 1 {
		t.Fatalf("call coverage = %+v", batch.RepositoryGoplsCoverage)
	}
}

func TestMaterializeRepositoryCodeMethodCallRelation(t *testing.T) {
	targetSource := []byte("package sample\n\ntype Service struct{}\n\nfunc (Service) Run() {}\n")
	callerSource := []byte("package sample\n\ntype Handler struct{}\n\nfunc (Handler) Handle(service Service) {\n\tservice.Run()\n}\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:method-call", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 2}
	input := RepositoryExtractorInput{
		RepositorySnapshot: snapshot,
		Files: []RepositoryExtractorFile{
			repositoryCodeDefinitionTestFile(snapshot, "service.go", targetSource),
			repositoryCodeDefinitionTestFile(snapshot, "handler.go", callerSource),
		},
	}
	target := repositoryTestDeclaration(t, snapshot, "service.go", targetSource, "sample.Service.Run")
	caller := repositoryTestDeclaration(t, snapshot, "handler.go", callerSource, "sample.Handler.Handle")
	usageStart := bytes.LastIndex(callerSource, []byte("Run"))
	proposals, err := materializeRepositoryGoplsCalls(input, []repositoryGoplsCall{{
		callerPath:      caller.Path,
		callerStartByte: caller.StartByte,
		callerEndByte:   caller.EndByte,
		usagePath:       caller.Path,
		usageStartByte:  usageStart,
		usageEndByte:    usageStart + len("Run"),
		targetPath:      target.Path,
		targetStartByte: target.StartByte,
		targetEndByte:   target.EndByte,
	}})
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsCalls() error = %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("method call proposals = %d, want 1", len(proposals))
	}
	output := repositoryGoplsTestOutputWithCalls(t, input, proposals, nil, proposals)
	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryBatch() error = %v", err)
	}
	var relation *ResolvedCodeRelation
	for _, occurrence := range batch.Occurrences {
		if occurrence.CodeRelation != nil {
			relation = occurrence.CodeRelation
		}
	}
	if len(batch.Occurrences) != len(input.Files)+1 || relation == nil || relation.Caller == nil {
		t.Fatalf("method call occurrences = %+v", batch.Occurrences)
	}
	if relation.Caller.SymbolKind != "method" || relation.Caller.QualifiedName != "sample.Handler.Handle" || relation.Target.SymbolKind != "method" || relation.Target.QualifiedName != "sample.Service.Run" || relation.Usage.QuotedText != "Run" {
		t.Fatalf("resolved method call = %+v", relation)
	}
}

func TestMaterializeRepositoryCodeCallRejectsTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RepositoryExtractorInput, *ExtractorProposalOutput)
	}{
		{
			name: "missing caller",
			mutate: func(_ *RepositoryExtractorInput, proposal *ExtractorProposalOutput) {
				proposal.CodeRelation.Caller = nil
			},
		},
		{
			name: "caller symbol",
			mutate: func(_ *RepositoryExtractorInput, proposal *ExtractorProposalOutput) {
				proposal.CodeRelation.Caller.QualifiedName = "sample.Other"
			},
		},
		{
			name: "definition schema",
			mutate: func(_ *RepositoryExtractorInput, proposal *ExtractorProposalOutput) {
				proposal.CodeRelation.SchemaVersion = CodeRelationSchemaV1
			},
		},
		{
			name:   "call site outside caller",
			mutate: moveCallSiteOutsideCaller,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, proposal := repositoryCodeCallTestFixture(t)
			test.mutate(&input, &proposal)
			output := repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal})
			_, err := materializeRepositoryGoplsTestOutput(t, input, output)
			assertKind(t, err, ErrorInvalidExtractorOutput)
		})
	}
}

func TestMaterializeRepositoryCodeCallRejectsWrongLocalitySchema(t *testing.T) {
	t.Run("same-file as v2", func(t *testing.T) {
		input, proposal := repositorySameFileCallTestFixture(t)
		proposal.CodeRelation.SchemaVersion = CodeRelationSchemaV2
		output := repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal})
		_, err := materializeRepositoryGoplsTestOutput(t, input, output)
		assertKind(t, err, ErrorInvalidExtractorOutput)
	})

	t.Run("cross-file as v3", func(t *testing.T) {
		input, proposal := repositoryCodeCallTestFixture(t)
		proposal.CodeRelation.SchemaVersion = CodeRelationSchemaV3
		output := repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal})
		_, err := materializeRepositoryGoplsTestOutput(t, input, output)
		assertKind(t, err, ErrorInvalidExtractorOutput)
	})
}

func moveCallSiteOutsideCaller(input *RepositoryExtractorInput, proposal *ExtractorProposalOutput) {
	callerFile := &input.Files[1]
	callerSource := []byte("package sample\n\nfunc Other() { Run() }\n\nfunc Main() {}\n")
	callerFile.Content = callerSource
	callerFile.FileSnapshot.BlobHash = contentHash(callerSource)
	callerFile.FileSnapshot.ByteLength = len(callerSource)
	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    input.RepositorySnapshot.RepoID,
		CommitSHA: input.RepositorySnapshot.CommitSHA,
		Path:      callerFile.FileSnapshot.Path,
		Source:    callerSource,
	})
	if err != nil {
		panic(err)
	}
	var mainDeclaration GoParserDeclaration
	for _, declaration := range extraction.Declarations {
		if declaration.CodeFact.QualifiedName == "sample.Main" {
			mainDeclaration = declaration
		}
	}
	caller := codeDeclarationOutput(mainDeclaration)
	usageStart := bytes.Index(callerSource, []byte("Run"))
	proposal.CodeRelation.Caller = &caller
	proposal.CodeRelation.Usage = CodeUsageSiteOutput{
		Path:            callerFile.FileSnapshot.Path,
		FileContentHash: callerFile.FileSnapshot.BlobHash,
		StartByte:       usageStart,
		EndByte:         usageStart + len("Run"),
		QuotedTextHash:  contentHash([]byte("Run")),
	}
	resolvedCaller := resolvedDeclarationForStatement(mainDeclaration)
	resolved := ResolvedCodeRelation{
		SchemaVersion: proposal.CodeRelation.SchemaVersion,
		RelationKind:  proposal.CodeRelation.RelationKind,
		Caller:        &resolvedCaller,
		Usage: ResolvedCodeUsageSite{
			Path:      callerFile.FileSnapshot.Path,
			StartLine: codeLineForOffset(callerSource, usageStart),
			EndLine:   codeLineForOffset(callerSource, usageStart+len("Run")-1),
		},
		Target: ResolvedCodeDeclarationEndpoint{
			Path:          proposal.CodeRelation.Target.Path,
			SymbolKind:    proposal.CodeRelation.Target.SymbolKind,
			QualifiedName: proposal.CodeRelation.Target.QualifiedName,
			StartLine:     3,
			EndLine:       3,
		},
	}
	proposal.StatementText = codeCallStatement(resolved)
}

func repositoryCodeCallTestFixture(t *testing.T) (RepositoryExtractorInput, ExtractorProposalOutput) {
	t.Helper()
	targetSource := []byte("package sample\n\nfunc Run() {}\n")
	callerSource := []byte("package sample\n\nfunc Main() {\n\tRun()\n}\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:call", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 2}
	input := RepositoryExtractorInput{
		RepositorySnapshot: snapshot,
		Files: []RepositoryExtractorFile{
			repositoryCodeDefinitionTestFile(snapshot, "target.go", targetSource),
			repositoryCodeDefinitionTestFile(snapshot, "caller.go", callerSource),
		},
	}
	targetExtraction, err := ExtractGoParserFile(GoParserFileInput{RepoID: snapshot.RepoID, CommitSHA: snapshot.CommitSHA, Path: "target.go", Source: targetSource})
	if err != nil {
		t.Fatalf("ExtractGoParserFile(target) error = %v", err)
	}
	callerExtraction, err := ExtractGoParserFile(GoParserFileInput{RepoID: snapshot.RepoID, CommitSHA: snapshot.CommitSHA, Path: "caller.go", Source: callerSource})
	if err != nil {
		t.Fatalf("ExtractGoParserFile(caller) error = %v", err)
	}
	usageStart := bytes.LastIndex(callerSource, []byte("Run"))
	calls := []repositoryGoplsCall{{
		callerPath:      "caller.go",
		callerStartByte: callerExtraction.Declarations[0].StartByte,
		callerEndByte:   callerExtraction.Declarations[0].EndByte,
		usagePath:       "caller.go",
		usageStartByte:  usageStart,
		usageEndByte:    usageStart + len("Run"),
		targetPath:      "target.go",
		targetStartByte: targetExtraction.Declarations[0].StartByte,
		targetEndByte:   targetExtraction.Declarations[0].EndByte,
	}}
	proposals, err := materializeRepositoryGoplsCalls(input, calls)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsCalls() error = %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("call proposals = %d, want 1", len(proposals))
	}
	return input, proposals[0]
}

func repositorySameFileCallTestFixture(t *testing.T) (RepositoryExtractorInput, ExtractorProposalOutput) {
	t.Helper()
	source := []byte("package sample\n\nfunc Run() {}\n\nfunc Main() {\n\tRun()\n}\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:same-file-call", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 1}
	file := repositoryCodeDefinitionTestFile(snapshot, "sample.go", source)
	input := RepositoryExtractorInput{RepositorySnapshot: snapshot, Files: []RepositoryExtractorFile{file}}
	target := repositoryTestDeclaration(t, snapshot, file.FileSnapshot.Path, source, "sample.Run")
	caller := repositoryTestDeclaration(t, snapshot, file.FileSnapshot.Path, source, "sample.Main")
	usageStart := bytes.LastIndex(source, []byte("Run"))
	proposals, err := materializeRepositoryGoplsCalls(input, []repositoryGoplsCall{{
		callerPath:      caller.Path,
		callerStartByte: caller.StartByte,
		callerEndByte:   caller.EndByte,
		usagePath:       caller.Path,
		usageStartByte:  usageStart,
		usageEndByte:    usageStart + len("Run"),
		targetPath:      target.Path,
		targetStartByte: target.StartByte,
		targetEndByte:   target.EndByte,
	}})
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsCalls() error = %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("same-file call proposals = %d, want 1", len(proposals))
	}
	return input, proposals[0]
}

func repositoryTestDeclaration(t *testing.T, snapshot RepositorySnapshot, path string, source []byte, qualifiedName string) GoParserDeclaration {
	t.Helper()
	extraction, err := ExtractGoParserFile(GoParserFileInput{RepoID: snapshot.RepoID, CommitSHA: snapshot.CommitSHA, Path: path, Source: source})
	if err != nil {
		t.Fatalf("ExtractGoParserFile(%s) error = %v", path, err)
	}
	for _, declaration := range extraction.Declarations {
		if declaration.CodeFact.QualifiedName == qualifiedName {
			return declaration
		}
	}
	t.Fatalf("declaration %q not found in %s", qualifiedName, path)
	return GoParserDeclaration{}
}
