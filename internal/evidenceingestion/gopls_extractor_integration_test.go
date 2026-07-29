//go:build integration

package evidenceingestion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrationGoplsExtractorMaterializesGroundedCodeFacts(t *testing.T) {
	goplsPath := os.Getenv("AHE_GOPLS_PATH")
	if goplsPath == "" {
		t.Skip("AHE_GOPLS_PATH is not set")
	}

	workspaceRoot := t.TempDir()
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "go.mod"), []byte("module example.com/gopls-smoke\n\ngo 1.22\n"))
	source := []byte("package sample\n\ntype Service struct{}\n\nfunc NewService() *Service {\n\treturn &Service{}\n}\n")
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "service.go"), source)

	const (
		repoID    = "gopls-smoke"
		commitSHA = "commit-smoke"
		path      = "service.go"
	)
	sourceCtx, err := buildManualSourceContext(ManualTextInput{
		SourceSystem:  SourceSystemCodeFile,
		SourceID:      repoID + ":" + path,
		SourceVersion: commitSHA,
		Raw:           source,
		OriginMetadata: map[string]string{
			"repo_id":    repoID,
			"commit_sha": commitSHA,
			"path":       path,
		},
		RequestID: "gopls-smoke-source",
	})
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	runner, err := NewGoplsExtractorRunner(ctx, GoplsExtractorConfig{
		BinaryPath:    goplsPath,
		WorkspaceRoot: workspaceRoot,
		RepoID:        repoID,
		CommitSHA:     commitSHA,
		Path:          path,
		Timeout:       30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewGoplsExtractorRunner() error = %v", err)
	}
	data, err := runner.Run(ctx, sourceCtx.extractorInput())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output, err := decodeTrustedExtractorOutput(data)
	if err != nil {
		t.Fatalf("decodeTrustedExtractorOutput() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSourceWithDefinition(
		sourceCtx,
		"gopls-smoke-run",
		1,
		runner.ExtractorDefinition(),
	)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinition() error = %v", err)
	}
	batch, err := materializeBatch(attemptCtx, output)
	if err != nil {
		t.Fatalf("materializeBatch() error = %v", err)
	}
	if got := len(batch.Occurrences); got != 2 {
		t.Fatalf("len(occurrences) = %d, want 2", got)
	}

	wantSymbols := map[string]string{
		"sample.Service":    "type",
		"sample.NewService": "function",
	}
	for _, occurrence := range batch.Occurrences {
		if occurrence.CodeFact == nil {
			t.Fatalf("occurrence %s has no resolved code fact", occurrence.ID)
		}
		fact := occurrence.CodeFact
		wantKind, ok := wantSymbols[fact.QualifiedName]
		if !ok {
			t.Fatalf("unexpected qualified name %q", fact.QualifiedName)
		}
		if fact.SymbolKind != wantKind {
			t.Fatalf("symbol %s kind = %q, want %q", fact.QualifiedName, fact.SymbolKind, wantKind)
		}
		if fact.RepoID != repoID || fact.CommitSHA != commitSHA || fact.Path != path {
			t.Fatalf("symbol %s origin = %s/%s/%s", fact.QualifiedName, fact.RepoID, fact.CommitSHA, fact.Path)
		}
		delete(wantSymbols, fact.QualifiedName)
	}
	if len(wantSymbols) != 0 {
		t.Fatalf("missing symbols = %v", wantSymbols)
	}
}

func TestIntegrationGoplsWorkspaceInventory(t *testing.T) {
	goplsPath := os.Getenv("AHE_GOPLS_PATH")
	if goplsPath == "" {
		t.Skip("AHE_GOPLS_PATH is not set")
	}

	workspaceRoot := t.TempDir()
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "go.mod"), []byte("module example.com/gopls-inventory\n\ngo 1.22\n"))
	rootSource := []byte("package inventory\n\nfunc Root() {}\n")
	workerSource := []byte("package worker\n\ntype Worker struct{}\n")
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "root.go"), rootSource)
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "internal", "worker", "worker.go"), workerSource)
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "nested", "go.mod"), []byte("module example.com/nested\n\ngo 1.22\n"))
	writeGoplsSmokeFile(t, filepath.Join(workspaceRoot, "nested", "ignored.go"), []byte("package nested\n"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	inventory, err := CollectGoplsWorkspaceInventory(ctx, GoplsWorkspaceInventoryConfig{
		BinaryPath:    goplsPath,
		WorkspaceRoot: workspaceRoot,
		RepoID:        "gopls-inventory",
		CommitSHA:     "commit-inventory",
		Timeout:       45 * time.Second,
	})
	if err != nil {
		t.Fatalf("CollectGoplsWorkspaceInventory() error = %v", err)
	}
	if inventory.RepoID != "gopls-inventory" || inventory.CommitSHA != "commit-inventory" {
		t.Fatalf("inventory origin = %q/%q", inventory.RepoID, inventory.CommitSHA)
	}
	if inventory.GoplsVersion == "" || inventory.PackageDiscoveryMethod != goplsPackagesCommand || inventory.RevisionBindingMethod != goplsRevisionBinding {
		t.Fatalf("inventory discovery = version %q, package method %q, revision method %q", inventory.GoplsVersion, inventory.PackageDiscoveryMethod, inventory.RevisionBindingMethod)
	}
	if got := len(inventory.Modules); got != 1 {
		t.Fatalf("len(modules) = %d, want 1: %+v", got, inventory.Modules)
	}
	if module := inventory.Modules[0]; module.Path != "example.com/gopls-inventory" || module.GoModPath != "go.mod" {
		t.Fatalf("module = %+v", module)
	}
	wantPackages := map[string]bool{
		"example.com/gopls-inventory":                 false,
		"example.com/gopls-inventory/internal/worker": false,
	}
	for _, pkg := range inventory.Packages {
		if _, ok := wantPackages[pkg.Path]; !ok {
			t.Fatalf("unexpected package = %+v", pkg)
		}
		wantPackages[pkg.Path] = true
	}
	for path, found := range wantPackages {
		if !found {
			t.Fatalf("package %q missing from %+v", path, inventory.Packages)
		}
	}
	wantFiles := []GoplsWorkspaceFile{
		{Path: "internal/worker/worker.go", ContentHash: contentHash(workerSource), SizeBytes: int64(len(workerSource))},
		{Path: "root.go", ContentHash: contentHash(rootSource), SizeBytes: int64(len(rootSource))},
	}
	if len(inventory.Files) != len(wantFiles) {
		t.Fatalf("files = %+v, want %+v", inventory.Files, wantFiles)
	}
	for idx, want := range wantFiles {
		if got := inventory.Files[idx]; got != want {
			t.Fatalf("files[%d] = %+v, want %+v", idx, got, want)
		}
	}
}

func writeGoplsSmokeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}
