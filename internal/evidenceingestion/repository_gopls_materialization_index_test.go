package evidenceingestion

import (
	"reflect"
	"testing"
)

func TestRepositoryGoplsMaterializationIndexMatchesLegacy(t *testing.T) {
	input, analysis := repositoryGoplsMaterializationIndexFixture(t)

	indexed, err := materializeRepositoryGoplsAnalysis(input, analysis)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsAnalysis() error = %v", err)
	}
	legacy, err := materializeRepositoryGoplsAnalysisLegacy(input, analysis)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsAnalysisLegacy() error = %v", err)
	}
	if !reflect.DeepEqual(indexed, legacy) {
		t.Fatalf("indexed output differs from legacy:\nindexed=%+v\nlegacy=%+v", indexed, legacy)
	}
}

func TestRepositoryGoplsMaterializationIndexReusesParsedGrounding(t *testing.T) {
	input, analysis := repositoryGoplsMaterializationIndexFixture(t)
	index := newRepositoryGoplsMaterializationIndex(input)

	output, err := materializeRepositoryGoplsOutputIndexed(input, analysis.symbolsByPath, index)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsOutputIndexed() error = %v", err)
	}
	packages, err := materializeRepositoryGoplsPackagesIndexed(
		input,
		analysis.packages.packages,
		index,
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsPackagesIndexed() error = %v", err)
	}
	definitions, _, err := materializeRepositoryGoplsDefinitionsIndexed(
		input,
		append([]repositoryGoplsDefinition(nil), analysis.definitions.relations...),
		index,
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsDefinitionsIndexed() error = %v", err)
	}
	references, _, err := materializeRepositoryGoplsDefinitionsIndexed(
		input,
		append([]repositoryGoplsDefinition(nil), analysis.references.relations...),
		index,
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsDefinitionsIndexed(references) error = %v", err)
	}
	if err := verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(input, references, index); err != nil {
		t.Fatalf("verifyRepositoryGoplsDiagnosticRelationProposalsIndexed() error = %v", err)
	}
	calls, err := materializeRepositoryGoplsCallsIndexed(
		input,
		append([]repositoryGoplsCall(nil), analysis.calls.calls...),
		index,
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsCallsIndexed() error = %v", err)
	}
	incoming, err := materializeRepositoryGoplsIncomingCallsIndexed(
		input,
		append([]repositoryGoplsCall(nil), analysis.calls.incomingCalls...),
		index,
	)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsIncomingCallsIndexed() error = %v", err)
	}
	if err := verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(input, incoming, index); err != nil {
		t.Fatalf("verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(incoming) error = %v", err)
	}

	if len(output.Proposals) != 2 ||
		len(packages) != 2 ||
		len(definitions) != 1 ||
		len(references) != 1 ||
		len(calls) != 1 ||
		len(incoming) != 1 {
		t.Fatalf(
			"materialized counts = declarations %d packages %d definitions %d references %d calls %d incoming %d",
			len(output.Proposals),
			len(packages),
			len(definitions),
			len(references),
			len(calls),
			len(incoming),
		)
	}
	if len(index.extractionsByPath) != len(input.Files) ||
		len(index.grounding.codeFactsByPath) != len(input.Files) ||
		!index.declarationsBuilt {
		t.Fatalf(
			"materialization cache = extractions %d facts %d declarations_built %t",
			len(index.extractionsByPath),
			len(index.grounding.codeFactsByPath),
			index.declarationsBuilt,
		)
	}
	if len(index.grounding.goSyntaxAttempted) != 1 {
		t.Fatalf(
			"syntax cache paths = %d, want only the caller file",
			len(index.grounding.goSyntaxAttempted),
		)
	}
}

func TestRepositoryGoplsMaterializationIndexPreservesFailure(t *testing.T) {
	input, analysis := repositoryGoplsMaterializationIndexFixture(t)
	analysis.calls.calls[0].usageEndByte = len(input.Files[1].Content) + 1

	indexedOutput, indexedErr := materializeRepositoryGoplsAnalysis(input, analysis)
	legacyOutput, legacyErr := materializeRepositoryGoplsAnalysisLegacy(input, analysis)
	assertKind(t, indexedErr, ErrorSpanOutOfBounds)
	assertKind(t, legacyErr, ErrorSpanOutOfBounds)
	if indexedErr.Error() != legacyErr.Error() {
		t.Fatalf("indexed error = %q, legacy = %q", indexedErr, legacyErr)
	}
	if !reflect.DeepEqual(indexedOutput, legacyOutput) {
		t.Fatalf(
			"indexed failure output differs:\nindexed=%+v\nlegacy=%+v",
			indexedOutput,
			legacyOutput,
		)
	}
	if indexedOutput.RepositoryGoplsCoverage == nil ||
		indexedOutput.RepositoryGoplsCoverage.FailureStage != repositoryGoplsStageCallGrounding {
		t.Fatalf("indexed failure coverage = %+v", indexedOutput.RepositoryGoplsCoverage)
	}
}

func repositoryGoplsMaterializationIndexFixture(
	t *testing.T,
) (RepositoryExtractorInput, repositoryGoplsAnalysis) {
	t.Helper()
	input, proposal := repositoryCodeCallTestFixture(t)
	relation := *proposal.CodeRelation
	call := repositoryGoplsCall{
		callerPath:      relation.Caller.Path,
		callerStartByte: relation.Caller.StartByte,
		callerEndByte:   relation.Caller.EndByte,
		usagePath:       relation.Usage.Path,
		usageStartByte:  relation.Usage.StartByte,
		usageEndByte:    relation.Usage.EndByte,
		targetPath:      relation.Target.Path,
		targetStartByte: relation.Target.StartByte,
		targetEndByte:   relation.Target.EndByte,
	}
	definition := repositoryGoplsDefinition{
		usagePath:       relation.Usage.Path,
		usageStartByte:  relation.Usage.StartByte,
		usageEndByte:    relation.Usage.EndByte,
		targetPath:      relation.Target.Path,
		targetStartByte: relation.Target.StartByte,
		targetEndByte:   relation.Target.EndByte,
	}

	symbolsByPath := make(map[string][]lspDocumentSymbol, len(input.Files))
	packages := make([]repositoryGoplsPackage, 0, len(input.Files))
	for _, file := range input.Files {
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      file.FileSnapshot.Path,
			Source:    file.Content,
		})
		if err != nil {
			t.Fatalf("ExtractGoParserFile(%s) error = %v", file.FileSnapshot.Path, err)
		}
		symbols := make([]lspDocumentSymbol, 0, len(extraction.Declarations))
		for _, declaration := range extraction.Declarations {
			symbols = append(symbols, testDocumentSymbol(t, file.Content, declaration.Name, 12))
		}
		symbolsByPath[file.FileSnapshot.Path] = symbols
		packages = append(packages, repositoryGoplsPackage{
			path: file.FileSnapshot.Path,
			name: extraction.PackageClause.Name,
		})
	}
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
	return input, repositoryGoplsAnalysis{
		symbolsByPath:              symbolsByPath,
		syntaxCounts:               syntaxCounts,
		documentSymbolRequestCount: len(input.Files),
		packages: repositoryGoplsPackageCollection{
			packages:              packages,
			requestCount:          len(input.Files),
			completedRequestCount: len(input.Files),
			resultFileCount:       len(input.Files),
		},
		definitions: repositoryGoplsRelationCollection{
			relations:             []repositoryGoplsDefinition{definition},
			requestCount:          syntaxCounts.identifiers,
			completedRequestCount: syntaxCounts.identifiers,
			locationCount:         1,
		},
		references: repositoryGoplsRelationCollection{
			relations:             []repositoryGoplsDefinition{definition},
			requestCount:          syntaxCounts.declarations,
			completedRequestCount: syntaxCounts.declarations,
			locationCount:         1,
		},
		calls: repositoryGoplsCallCollection{
			calls:                         []repositoryGoplsCall{call},
			incomingCalls:                 []repositoryGoplsCall{call},
			prepareRequestCount:           syntaxCounts.callables,
			prepareCompletedRequestCount:  syntaxCounts.callables,
			preparedItemCount:             syntaxCounts.callables,
			outgoingRequestCount:          syntaxCounts.callables,
			outgoingCompletedRequestCount: syntaxCounts.callables,
			outgoingCallResultCount:       1,
			outgoingCallSiteCount:         1,
			incomingRequestCount:          syntaxCounts.callables,
			incomingCompletedRequestCount: syntaxCounts.callables,
			incomingCallResultCount:       1,
			incomingCallSiteCount:         1,
		},
	}
}
