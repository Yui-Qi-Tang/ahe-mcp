package evidenceingestion

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type repositoryGoplsPackageCollection struct {
	packages              []repositoryGoplsPackage
	requestCount          int
	completedRequestCount int
	resultFileCount       int
}

type repositoryGoplsPackage struct {
	path string
	name string
}

type goplsPackageSymbolsResult struct {
	PackageName string   `json:"PackageName"`
	Files       []string `json:"Files"`
}

func requestRepositoryGoplsPackages(
	client *lspClient,
	files []RepositoryExtractorFile,
	filePaths map[string]string,
) (repositoryGoplsPackageCollection, error) {
	var collection repositoryGoplsPackageCollection
	for _, file := range files {
		path := file.FileSnapshot.Path
		filePath := filePaths[path]
		collection.requestCount++
		result, err := requestGoplsPackageSymbols(client, filePath)
		if err != nil {
			return collection, fmt.Errorf("requesting repository gopls package symbols for %s: %w", path, err)
		}
		collection.completedRequestCount++
		collection.resultFileCount += len(result.Files)
		name := strings.TrimSpace(result.PackageName)
		if name == "" {
			return collection, newDomainError(ErrorInvalidExtractorOutput, "repository gopls package name for %s is empty", path)
		}
		containsFile := false
		seenFiles := make(map[string]struct{}, len(result.Files))
		for _, uri := range result.Files {
			absolute, err := pathFromFileURI(uri)
			if err != nil {
				return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls package file URI for %s: %v", path, err)
			}
			absolute = filepath.Clean(absolute)
			if _, ok := seenFiles[absolute]; ok {
				return collection, newDomainError(ErrorInvalidExtractorOutput, "repository gopls package for %s repeats member file %s", path, absolute)
			}
			seenFiles[absolute] = struct{}{}
			if absolute == filepath.Clean(filePath) {
				containsFile = true
			}
		}
		if !containsFile {
			return collection, newDomainError(ErrorInvalidExtractorOutput, "repository gopls package for %s does not contain the requested file", path)
		}
		collection.packages = append(collection.packages, repositoryGoplsPackage{path: path, name: name})
	}
	slices.SortFunc(collection.packages, func(a, b repositoryGoplsPackage) int {
		return strings.Compare(a.path, b.path)
	})
	return collection, nil
}

func requestGoplsPackageSymbols(client *lspClient, filePath string) (goplsPackageSymbolsResult, error) {
	var result goplsPackageSymbolsResult
	err := client.call("workspace/executeCommand", map[string]any{
		"command": goplsPackageSymbolsCommand,
		"arguments": []any{map[string]string{
			"URI": fileURI(filePath),
		}},
	}, &result)
	return result, err
}

func materializeRepositoryGoplsPackages(
	input RepositoryExtractorInput,
	packages []repositoryGoplsPackage,
) ([]ExtractorProposalOutput, error) {
	return materializeRepositoryGoplsPackagesIndexed(input, packages, nil)
}

func materializeRepositoryGoplsPackagesIndexed(
	input RepositoryExtractorInput,
	packages []repositoryGoplsPackage,
	index *repositoryGoplsMaterializationIndex,
) ([]ExtractorProposalOutput, error) {
	var filesByPath map[string]RepositoryExtractorFile
	if index == nil {
		filesByPath = make(map[string]RepositoryExtractorFile, len(input.Files))
		for _, file := range input.Files {
			filesByPath[file.FileSnapshot.Path] = file
		}
	} else {
		filesByPath = index.filesByPath
	}
	proposals := make([]ExtractorProposalOutput, 0, len(packages))
	for _, pkg := range packages {
		file, ok := filesByPath[pkg.path]
		if !ok {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls package references unknown path %q", pkg.path)
		}
		var extraction GoParserFileExtraction
		var err error
		if index == nil {
			extraction, err = ExtractGoParserFile(GoParserFileInput{
				RepoID:    input.RepositorySnapshot.RepoID,
				CommitSHA: input.RepositorySnapshot.CommitSHA,
				Path:      pkg.path,
				Source:    file.Content,
			})
		} else {
			extraction, err = index.extraction(pkg.path)
		}
		if err != nil {
			return nil, err
		}
		clause := extraction.PackageClause
		if clause.Name != pkg.name {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls package name %q for %s does not match parsed package %q", pkg.name, pkg.path, clause.Name)
		}
		fact := clause.CodeFact
		proposal, err := qualifyRepositoryProposalLocalID(pkg.path, ExtractorProposalOutput{
			ProposalLocalID: clause.ProposalLocalID,
			StatementText:   clause.StatementText,
			EvidenceRefs:    append([]string(nil), clause.EvidenceRefs...),
			CodeFact:        &fact,
		})
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, nil
}
