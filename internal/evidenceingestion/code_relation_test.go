package evidenceingestion

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestMaterializeRepositoryCodeDefinitionRelation(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	definition, err := buildExtractorDefinition((&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:definition", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}

	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, nil)
	batch, err := materializeRepositoryBatch(attemptCtx, output)
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
	if occurrence.CodeFact != nil || occurrence.CodeRelation == nil {
		t.Fatalf("occurrence code payload = fact %+v relation %+v", occurrence.CodeFact, occurrence.CodeRelation)
	}
	if occurrence.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV1 {
		t.Fatalf("fingerprint version = %q", occurrence.ProposalFingerprintVersion)
	}
	if len(occurrence.SourceRefs) != 2 || occurrence.SourceRefs[0].FileSnapshotID == occurrence.SourceRefs[1].FileSnapshotID {
		t.Fatalf("relation source refs = %+v", occurrence.SourceRefs)
	}
	if occurrence.CodeRelation.Usage.QuotedText != "Target" || occurrence.CodeRelation.Target.QualifiedName != "sample.Target" {
		t.Fatalf("resolved relation = %+v", occurrence.CodeRelation)
	}
	if _, ok := occurrence.ProposedPayload["code_relation"]; !ok {
		t.Fatalf("proposed payload has no code_relation: %+v", occurrence.ProposedPayload)
	}
}

func TestMaterializeRepositorySameFileCodeDefinitionRelation(t *testing.T) {
	input, proposal := repositorySameFileDefinitionTestFixture(t)
	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, []ExtractorProposalOutput{proposal})
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
	if occurrence.CodeRelation == nil || occurrence.CodeRelation.SchemaVersion != CodeRelationSchemaV3 {
		t.Fatalf("same-file relation = %+v", occurrence.CodeRelation)
	}
	if occurrence.ProposalFingerprintVersion != ProposalFingerprintCodeRelationV3 {
		t.Fatalf("same-file fingerprint version = %q", occurrence.ProposalFingerprintVersion)
	}
	if len(occurrence.SourceRefs) != 2 || occurrence.SourceRefs[0].FileSnapshotID != occurrence.SourceRefs[1].FileSnapshotID {
		t.Fatalf("same-file source refs = %+v", occurrence.SourceRefs)
	}
	if batch.RepositoryGoplsCoverage == nil || batch.RepositoryGoplsCoverage.SupportedSameFileDefinitionCount != 1 || batch.RepositoryGoplsCoverage.SupportedSameFileReferenceCount != 1 || batch.RepositoryGoplsCoverage.CorroboratedRelationCount != 1 {
		t.Fatalf("same-file coverage = %+v", batch.RepositoryGoplsCoverage)
	}
}

func TestMaterializeRepositoryCodeDefinitionRejectsWrongLocalitySchema(t *testing.T) {
	t.Run("same-file as v1", func(t *testing.T) {
		input, proposal := repositorySameFileDefinitionTestFixture(t)
		proposal.CodeRelation.SchemaVersion = CodeRelationSchemaV1
		_, err := materializeRepositoryDefinitionTestProposal(t, input, proposal)
		assertKind(t, err, ErrorInvalidExtractorOutput)
	})

	t.Run("cross-file as v3", func(t *testing.T) {
		input, proposal := repositoryCodeDefinitionTestFixture(t)
		proposal.CodeRelation.SchemaVersion = CodeRelationSchemaV3
		_, err := materializeRepositoryDefinitionTestProposal(t, input, proposal)
		assertKind(t, err, ErrorInvalidExtractorOutput)
	})
}

func TestRepositoryGoplsSameFileDefinitionSkipsDeclarationEndpoint(t *testing.T) {
	input, proposal := repositorySameFileDefinitionTestFixture(t)
	file := input.Files[0]
	target := proposal.CodeRelation.Target
	targetRange := testLSPRange(t, file.Content, target.StartByte, target.EndByte)
	usageRange := testLSPRange(t, file.Content, proposal.CodeRelation.Usage.StartByte, proposal.CodeRelation.Usage.EndByte)
	path := filepath.Join("/workspace", file.FileSnapshot.Path)
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspLocation{{URI: fileURI(path), Range: targetRange}}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "result": []lspLocation{{URI: fileURI(path), Range: targetRange}}},
	)

	definitions, err := requestRepositoryGoplsDefinitions(client, input.Files, map[string]string{file.FileSnapshot.Path: path})
	if err != nil {
		t.Fatalf("requestRepositoryGoplsDefinitions() error = %v", err)
	}
	if definitions.requestCount != 4 || definitions.completedRequestCount != 4 || definitions.locationCount != 2 || definitions.selfDeclarationLocationCount != 1 || definitions.unselectedLocationCount != 0 {
		t.Fatalf("same-file definition request coverage = %+v", definitions)
	}
	if len(definitions.relations) != 1 || definitions.relations[0].usageStartByte != proposal.CodeRelation.Usage.StartByte || definitions.relations[0].usageEndByte != proposal.CodeRelation.Usage.EndByte {
		t.Fatalf("same-file definitions = %+v, usage range = %+v", definitions.relations, usageRange)
	}
}

func TestRepositoryGoplsSameFileReferencesSkipDeclarationEndpoint(t *testing.T) {
	input, proposal := repositorySameFileDefinitionTestFixture(t)
	file := input.Files[0]
	target := proposal.CodeRelation.Target
	targetRange := testLSPRange(t, file.Content, target.StartByte, target.EndByte)
	usageRange := testLSPRange(t, file.Content, proposal.CodeRelation.Usage.StartByte, proposal.CodeRelation.Usage.EndByte)
	path := filepath.Join("/workspace", file.FileSnapshot.Path)
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{{URI: fileURI(path), Range: targetRange}, {URI: fileURI(path), Range: usageRange}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspLocation{}},
	)

	references, err := requestRepositoryGoplsReferences(client, input, map[string]string{file.FileSnapshot.Path: path})
	if err != nil {
		t.Fatalf("requestRepositoryGoplsReferences() error = %v", err)
	}
	if references.requestCount != 2 || references.completedRequestCount != 2 || references.locationCount != 2 || references.selfDeclarationLocationCount != 1 || references.unselectedLocationCount != 0 {
		t.Fatalf("same-file reference request coverage = %+v", references)
	}
	if len(references.relations) != 1 || references.relations[0].usageStartByte != proposal.CodeRelation.Usage.StartByte {
		t.Fatalf("same-file references = %+v", references.relations)
	}
}

func TestMaterializeRepositoryCodeDefinitionRejectsTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExtractorProposalOutput)
		kind   ErrorKind
	}{
		{
			name: "usage hash",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeRelation.Usage.FileContentHash = "sha256:tampered"
			},
			kind: ErrorQuotedHashMismatch,
		},
		{
			name: "usage is not identifier",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeRelation.Usage.EndByte++
				proposal.CodeRelation.Usage.QuotedTextHash = contentHash([]byte("Target("))
			},
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "target symbol",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeRelation.Target.QualifiedName = "sample.Other"
			},
			kind: ErrorInvalidExtractorOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, proposal := repositoryCodeDefinitionTestFixture(t)
			test.mutate(&proposal)
			_, err := materializeRepositoryDefinitionTestProposal(t, input, proposal)
			assertKind(t, err, test.kind)
		})
	}
}

func TestMaterializeRepositoryProposalRejectsMixedCodePayload(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	targetExtraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    input.RepositorySnapshot.RepoID,
		CommitSHA: input.RepositorySnapshot.CommitSHA,
		Path:      input.Files[0].FileSnapshot.Path,
		Source:    input.Files[0].Content,
	})
	if err != nil {
		t.Fatalf("ExtractGoParserFile() error = %v", err)
	}
	fact := targetExtraction.Declarations[0].CodeFact
	proposal.CodeFact = &fact
	_, err = materializeRepositoryDefinitionTestProposal(t, input, proposal)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestMaterializeRepositoryGoplsDefinitionsRecordsMultipleTargetsAsAmbiguous(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	usage := *proposal.CodeRelation
	otherSource := []byte("package other\n\nfunc Target() {}\n")
	otherFile := repositoryCodeDefinitionTestFile(input.RepositorySnapshot, "other/target.go", otherSource)
	input.Files = append(input.Files, otherFile)
	input.RepositorySnapshot.SelectedFileCount = len(input.Files)
	otherExtraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    input.RepositorySnapshot.RepoID,
		CommitSHA: input.RepositorySnapshot.CommitSHA,
		Path:      otherFile.FileSnapshot.Path,
		Source:    otherSource,
	})
	if err != nil {
		t.Fatalf("ExtractGoParserFile() error = %v", err)
	}
	definitions := []repositoryGoplsDefinition{
		{
			usagePath:       usage.Usage.Path,
			usageStartByte:  usage.Usage.StartByte,
			usageEndByte:    usage.Usage.EndByte,
			targetPath:      usage.Target.Path,
			targetStartByte: usage.Target.StartByte,
			targetEndByte:   usage.Target.EndByte,
		},
		{
			usagePath:       usage.Usage.Path,
			usageStartByte:  usage.Usage.StartByte,
			usageEndByte:    usage.Usage.EndByte,
			targetPath:      otherFile.FileSnapshot.Path,
			targetStartByte: otherExtraction.Declarations[0].StartByte,
			targetEndByte:   otherExtraction.Declarations[0].EndByte,
		},
	}
	proposals, ambiguous, err := materializeRepositoryGoplsDefinitions(input, definitions)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsDefinitions() error = %v", err)
	}
	if len(proposals) != 0 || ambiguous != 1 {
		t.Fatalf("ambiguous definitions = proposals %d, ambiguous %d", len(proposals), ambiguous)
	}
}

func materializeRepositoryDefinitionTestProposal(t *testing.T, input RepositoryExtractorInput, proposal ExtractorProposalOutput) (MaterializedBatch, error) {
	t.Helper()
	definition, err := buildExtractorDefinition((&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:definition-test", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, nil)
	return materializeRepositoryBatch(attemptCtx, output)
}

func repositoryGoplsTestOutput(
	t *testing.T,
	input RepositoryExtractorInput,
	proposals []ExtractorProposalOutput,
	references []ExtractorProposalOutput,
) FrozenExtractorOutput {
	return repositoryGoplsTestOutputWithCalls(t, input, proposals, references, nil)
}

func repositoryGoplsTestOutputWithCalls(
	t *testing.T,
	input RepositoryExtractorInput,
	proposals []ExtractorProposalOutput,
	references []ExtractorProposalOutput,
	calls []ExtractorProposalOutput,
) FrozenExtractorOutput {
	return repositoryGoplsTestOutputWithCallCoverage(t, input, proposals, references, calls, calls)
}

func repositoryGoplsTestOutputWithCallCoverage(
	t *testing.T,
	input RepositoryExtractorInput,
	proposals []ExtractorProposalOutput,
	references []ExtractorProposalOutput,
	calls []ExtractorProposalOutput,
	incomingCalls []ExtractorProposalOutput,
) FrozenExtractorOutput {
	t.Helper()
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
	symbolsByPath := make(map[string][]lspDocumentSymbol, len(input.Files))
	packageObservations := make([]repositoryGoplsPackage, 0, len(input.Files))
	for _, file := range input.Files {
		symbolsByPath[file.FileSnapshot.Path] = []lspDocumentSymbol{}
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      file.FileSnapshot.Path,
			Source:    file.Content,
		})
		if err != nil {
			t.Fatalf("ExtractGoParserFile(%s) error = %v", file.FileSnapshot.Path, err)
		}
		packageObservations = append(packageObservations, repositoryGoplsPackage{path: file.FileSnapshot.Path, name: extraction.PackageClause.Name})
	}
	packageProposals, err := materializeRepositoryGoplsPackages(input, packageObservations)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsPackages() error = %v", err)
	}
	definitions := make([]ExtractorProposalOutput, 0, len(proposals))
	for _, proposal := range proposals {
		if proposal.CodeRelation != nil && proposal.CodeRelation.RelationKind == CodeRelationKindDefinition {
			definitions = append(definitions, proposal)
		}
	}
	analysis := repositoryGoplsAnalysis{
		symbolsByPath:              symbolsByPath,
		syntaxCounts:               syntaxCounts,
		documentSymbolRequestCount: len(input.Files),
		packages: repositoryGoplsPackageCollection{
			packages:              packageObservations,
			requestCount:          len(input.Files),
			completedRequestCount: len(input.Files),
			resultFileCount:       len(input.Files),
		},
		definitions: repositoryGoplsRelationCollection{
			requestCount:          syntaxCounts.identifiers,
			completedRequestCount: syntaxCounts.identifiers,
			locationCount:         len(definitions),
		},
		references: repositoryGoplsRelationCollection{
			requestCount:          syntaxCounts.declarations,
			completedRequestCount: syntaxCounts.declarations,
			locationCount:         len(references),
		},
		calls: repositoryGoplsCallCollection{
			prepareRequestCount:           syntaxCounts.callables,
			prepareCompletedRequestCount:  syntaxCounts.callables,
			preparedItemCount:             syntaxCounts.callables,
			outgoingRequestCount:          syntaxCounts.callables,
			outgoingCompletedRequestCount: syntaxCounts.callables,
			outgoingCallResultCount:       len(calls),
			outgoingCallSiteCount:         len(calls),
			incomingRequestCount:          syntaxCounts.callables,
			incomingCompletedRequestCount: syntaxCounts.callables,
			incomingCallResultCount:       len(incomingCalls),
			incomingCallSiteCount:         len(incomingCalls),
		},
	}
	coverage := buildRepositoryGoplsCoverage(input, analysis, packageProposals, definitions, references, calls, incomingCalls)
	allProposals := append([]ExtractorProposalOutput(nil), packageProposals...)
	allProposals = append(allProposals, proposals...)
	return FrozenExtractorOutput{Proposals: allProposals, RepositoryGoplsCoverage: &coverage}
}

func repositoryCodeDefinitionTestFixture(t *testing.T) (RepositoryExtractorInput, ExtractorProposalOutput) {
	t.Helper()
	targetSource := []byte("package sample\n\nfunc Target() {}\n")
	usageSource := []byte("package sample\n\nfunc Use() { Target() }\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:definition", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 2}
	input := RepositoryExtractorInput{
		RepositorySnapshot: snapshot,
		Files: []RepositoryExtractorFile{
			repositoryCodeDefinitionTestFile(snapshot, "target.go", targetSource),
			repositoryCodeDefinitionTestFile(snapshot, "usage.go", usageSource),
		},
	}
	targetExtraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    snapshot.RepoID,
		CommitSHA: snapshot.CommitSHA,
		Path:      "target.go",
		Source:    targetSource,
	})
	if err != nil {
		t.Fatalf("ExtractGoParserFile() error = %v", err)
	}
	target := targetExtraction.Declarations[0]
	usageStart := bytes.LastIndex(usageSource, []byte("Target"))
	definition := repositoryGoplsDefinition{
		usagePath:       "usage.go",
		usageStartByte:  usageStart,
		usageEndByte:    usageStart + len("Target"),
		targetPath:      "target.go",
		targetStartByte: target.StartByte,
		targetEndByte:   target.EndByte,
	}
	proposals, ambiguous, err := materializeRepositoryGoplsDefinitions(
		input,
		[]repositoryGoplsDefinition{definition},
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsDefinitions() error = %v", err)
	}
	if len(proposals) != 1 || ambiguous != 0 {
		t.Fatalf("definition proposals/ambiguous = %d/%d, want 1/0", len(proposals), ambiguous)
	}
	return input, proposals[0]
}

func repositorySameFileDefinitionTestFixture(t *testing.T) (RepositoryExtractorInput, ExtractorProposalOutput) {
	t.Helper()
	source := []byte("package sample\n\nfunc Target() {}\n\nfunc Use() {\n\tTarget()\n}\n")
	snapshot := RepositorySnapshot{ID: "repo-snapshot:same-file-definition", RepoID: "repo", CommitSHA: "commit", SelectedFileCount: 1}
	file := repositoryCodeDefinitionTestFile(snapshot, "sample.go", source)
	input := RepositoryExtractorInput{RepositorySnapshot: snapshot, Files: []RepositoryExtractorFile{file}}
	target := repositoryTestDeclaration(t, snapshot, file.FileSnapshot.Path, source, "sample.Target")
	usageStart := bytes.LastIndex(source, []byte("Target"))
	proposals, ambiguous, err := materializeRepositoryGoplsDefinitions(
		input,
		[]repositoryGoplsDefinition{{
			usagePath:       file.FileSnapshot.Path,
			usageStartByte:  usageStart,
			usageEndByte:    usageStart + len("Target"),
			targetPath:      target.Path,
			targetStartByte: target.StartByte,
			targetEndByte:   target.EndByte,
		}},
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsDefinitions() error = %v", err)
	}
	if len(proposals) != 1 || ambiguous != 0 {
		t.Fatalf(
			"same-file definition proposals/ambiguous = %d/%d, want 1/0",
			len(proposals),
			ambiguous,
		)
	}
	return input, proposals[0]
}

func repositoryCodeDefinitionTestFile(snapshot RepositorySnapshot, path string, content []byte) RepositoryExtractorFile {
	return RepositoryExtractorFile{
		FileSnapshot: SourceFileSnapshot{
			ID:                   "file:" + path,
			RepositorySnapshotID: snapshot.ID,
			RepoID:               snapshot.RepoID,
			CommitSHA:            snapshot.CommitSHA,
			Path:                 path,
			BlobHash:             contentHash(content),
			ByteLength:           len(content),
		},
		Content: content,
	}
}
