package evidenceingestion

import (
	"fmt"
	"path/filepath"
)

func requestRepositoryGoplsReferences(
	client *lspClient,
	input RepositoryExtractorInput,
	filePaths map[string]string,
) (repositoryGoplsRelationCollection, error) {
	pathsByAbsolute := make(map[string]string, len(filePaths))
	filesByPath := make(map[string]RepositoryExtractorFile, len(input.Files))
	for _, file := range input.Files {
		path := file.FileSnapshot.Path
		pathsByAbsolute[filepath.Clean(filePaths[path])] = path
		filesByPath[path] = file
	}

	var collection repositoryGoplsRelationCollection
	for _, targetFile := range input.Files {
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      targetFile.FileSnapshot.Path,
			Source:    targetFile.Content,
		})
		if err != nil {
			return collection, err
		}
		for _, declaration := range extraction.Declarations {
			if declaration.Name == "_" {
				continue
			}
			collection.requestCount++
			position, err := lspPositionForByteOffset(targetFile.Content, declaration.StartByte)
			if err != nil {
				return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls declaration %s byte %d: %v", declaration.SymbolRef, declaration.StartByte, err)
			}
			locations, err := requestGoplsReferences(client, filePaths[declaration.Path], position)
			if err != nil {
				return collection, fmt.Errorf("requesting repository gopls references for %s: %w", declaration.SymbolRef, err)
			}
			collection.completedRequestCount++
			collection.locationCount += len(locations)
			for _, location := range locations {
				usageAbsolute, err := pathFromFileURI(location.URI)
				if err != nil {
					return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls reference URI: %v", err)
				}
				usagePath, ok := pathsByAbsolute[filepath.Clean(usageAbsolute)]
				if !ok {
					collection.unselectedLocationCount++
					continue
				}
				usageFile := filesByPath[usagePath]
				usageStart, err := lspPositionByteOffset(usageFile.Content, location.Range.Start)
				if err != nil {
					return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls reference usage %s start: %v", usagePath, err)
				}
				usageEnd, err := lspPositionByteOffset(usageFile.Content, location.Range.End)
				if err != nil {
					return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls reference usage %s end: %v", usagePath, err)
				}
				if usagePath == declaration.Path && usageStart == declaration.StartByte && usageEnd == declaration.EndByte {
					collection.selfDeclarationLocationCount++
					continue
				}
				collection.relations = append(collection.relations, repositoryGoplsDefinition{
					usagePath:       usagePath,
					usageStartByte:  usageStart,
					usageEndByte:    usageEnd,
					targetPath:      declaration.Path,
					targetStartByte: declaration.StartByte,
					targetEndByte:   declaration.EndByte,
				})
			}
		}
	}
	collection.relations = uniqueRepositoryGoplsDefinitions(collection.relations)
	return collection, nil
}

func verifyRepositoryGoplsDiagnosticRelationProposals(input RepositoryExtractorInput, proposals []ExtractorProposalOutput) error {
	return verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(input, proposals, nil)
}

func verifyRepositoryGoplsDiagnosticRelationProposalsIndexed(
	input RepositoryExtractorInput,
	proposals []ExtractorProposalOutput,
	index *repositoryGoplsMaterializationIndex,
) error {
	var grounding *repositoryGroundingIndex
	if index != nil {
		grounding = index.grounding
	}
	for _, proposal := range proposals {
		if proposal.CodeRelation == nil {
			return newDomainError(ErrorInvalidExtractorOutput, "repository gopls diagnostic proposal %s has no code relation", proposal.ProposalLocalID)
		}
		refs, err := repositoryCodeRelationSourceRefsIndexed(input, *proposal.CodeRelation, grounding)
		if err != nil {
			return err
		}
		if _, err := resolveRepositoryCodeRelationIndexed(input, proposal, refs, grounding); err != nil {
			return err
		}
	}
	return nil
}
