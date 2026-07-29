package evidenceingestion

import (
	"path/filepath"
	"testing"
)

func TestRepositoryGoplsPackagesMaterializeGroundedFileFacts(t *testing.T) {
	input := repositoryParserTestInput(t)
	filePaths := make(map[string]string, len(input.Files))
	packageFiles := make([]string, 0, len(input.Files))
	for _, file := range input.Files {
		path := filepath.Join("/workspace", file.FileSnapshot.Path)
		filePaths[file.FileSnapshot.Path] = path
		packageFiles = append(packageFiles, fileURI(path))
	}
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"PackageName": "alpha", "Files": []string{packageFiles[0]}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"PackageName": "beta", "Files": []string{packageFiles[1]}}},
	)

	packages, err := requestRepositoryGoplsPackages(client, input.Files, filePaths)
	if err != nil {
		t.Fatalf("requestRepositoryGoplsPackages() error = %v", err)
	}
	if packages.requestCount != 2 || packages.completedRequestCount != 2 || packages.resultFileCount != 2 || len(packages.packages) != 2 {
		t.Fatalf("package collection = %+v", packages)
	}
	proposals, err := materializeRepositoryGoplsPackages(input, packages.packages)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsPackages() error = %v", err)
	}
	if len(proposals) != 2 {
		t.Fatalf("package proposals = %d, want 2", len(proposals))
	}
	for _, proposal := range proposals {
		if proposal.CodeFact == nil || proposal.CodeFact.SchemaVersion != CodeFactSchemaV2 || proposal.CodeFact.FactKind != CodeFactKindPackage || proposal.CodeFact.Path == "" || proposal.CodeFact.QualifiedName == "" {
			t.Fatalf("package proposal = %+v", proposal)
		}
	}
}

func TestRepositoryGoplsPackagesRejectUngroundedResults(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	path := filepath.Join("/workspace", "sample.go")

	t.Run("missing requested file", func(t *testing.T) {
		client := repositoryGoplsFailureTestClient(t,
			map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"PackageName": "sample", "Files": []string{fileURI(filepath.Join("/workspace", "other.go"))}}},
		)
		_, err := requestRepositoryGoplsPackages(client, input.Files, map[string]string{"sample.go": path})
		assertKind(t, err, ErrorInvalidExtractorOutput)
	})

	t.Run("parsed name mismatch", func(t *testing.T) {
		_, err := materializeRepositoryGoplsPackages(input, []repositoryGoplsPackage{{path: "sample.go", name: "other"}})
		assertKind(t, err, ErrorInvalidExtractorOutput)
	})
}

func TestRepositoryGoplsPackageFailureReturnsPartialProgress(t *testing.T) {
	input := repositoryParserTestInput(t)
	filePaths := make(map[string]string, len(input.Files))
	for _, file := range input.Files {
		filePaths[file.FileSnapshot.Path] = filepath.Join("/workspace", file.FileSnapshot.Path)
	}
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"PackageName": "alpha", "Files": []string{fileURI(filePaths[input.Files[0].FileSnapshot.Path])}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "error": map[string]any{"code": -32603, "message": "injected package failure"}},
	)

	packages, err := requestRepositoryGoplsPackages(client, input.Files, filePaths)
	if err == nil {
		t.Fatal("requestRepositoryGoplsPackages() succeeded, want injected failure")
	}
	if packages.requestCount != 2 || packages.completedRequestCount != 1 || packages.resultFileCount != 1 || len(packages.packages) != 1 {
		t.Fatalf("partial package progress = %+v", packages)
	}
	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
	analysis := repositoryGoplsAnalysis{
		symbolsByPath:              map[string][]lspDocumentSymbol{"alpha.go": {}, "beta.go": {}},
		syntaxCounts:               syntaxCounts,
		documentSymbolRequestCount: len(input.Files),
		packages:                   packages,
		failureStage:               repositoryGoplsStagePackageSymbols,
	}
	coverage := buildRepositoryGoplsCoverage(input, analysis, nil, nil, nil, nil, nil)
	if coverage.RequestCoverageComplete || coverage.FailureStage != repositoryGoplsStagePackageSymbols || coverage.PackageRequestCount != 2 || coverage.PackageCompletedRequestCount != 1 || coverage.SupportedPackageFactCount != 0 || coverage.EmittedPackageFactCount != 0 {
		t.Fatalf("partial package coverage = %+v", coverage)
	}
	if err := validateRepositoryGoplsCoverageCounts(coverage); err != nil {
		t.Fatalf("validateRepositoryGoplsCoverageCounts() error = %v", err)
	}
}
