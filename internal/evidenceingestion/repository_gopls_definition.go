package evidenceingestion

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
)

type repositoryGoplsAnalysis struct {
	symbolsByPath              map[string][]lspDocumentSymbol
	syntaxCounts               repositoryGoSyntaxCount
	documentSymbolRequestCount int
	packages                   repositoryGoplsPackageCollection
	definitions                repositoryGoplsRelationCollection
	references                 repositoryGoplsRelationCollection
	calls                      repositoryGoplsCallCollection
	failureStage               string
}

type repositoryGoplsRelationCollection struct {
	relations                    []repositoryGoplsDefinition
	requestCount                 int
	completedRequestCount        int
	locationCount                int
	unselectedLocationCount      int
	selfDeclarationLocationCount int
	ambiguousUsageCount          int
}

type repositoryGoplsDefinition struct {
	usagePath       string
	usageStartByte  int
	usageEndByte    int
	targetPath      string
	targetStartByte int
	targetEndByte   int
}

type repositoryGoplsDefinitionContribution struct {
	requestCount                          int
	noLocationRequestCount                int
	selfOnlyRequestCount                  int
	unselectedOnlyRequestCount            int
	selfAndUnselectedOnlyRequestCount     int
	selectedSingleTargetRequestCount      int
	selectedAmbiguousTargetRequestCount   int
	selectedUniqueOnlyRequestCount        int
	selectedDuplicateOnlyRequestCount     int
	selectedMixedContributionRequestCount int
	selectedLocationCount                 int
	uniqueRelationCount                   int
	duplicateLocationCount                int
}

type repositoryGoplsDefinitionContributionObserver func(repositoryGoplsDefinitionContribution)

type repositoryGoplsDefinitionContributionRecorder struct {
	contribution repositoryGoplsDefinitionContribution
	seen         map[repositoryGoplsDefinition]struct{}
}

type goIdentifierSite struct {
	startByte int
	endByte   int
}

func requestRepositoryGoplsDefinitions(
	client *lspClient,
	files []RepositoryExtractorFile,
	filePaths map[string]string,
) (repositoryGoplsRelationCollection, error) {
	return requestRepositoryGoplsDefinitionsProfiled(client, files, filePaths, nil)
}

func requestRepositoryGoplsDefinitionsProfiled(
	client *lspClient,
	files []RepositoryExtractorFile,
	filePaths map[string]string,
	recorder *repositoryGoplsDefinitionContributionRecorder,
) (repositoryGoplsRelationCollection, error) {
	pathsByAbsolute := make(map[string]string, len(filePaths))
	filesByPath := make(map[string]RepositoryExtractorFile, len(files))
	for _, file := range files {
		path := file.FileSnapshot.Path
		pathsByAbsolute[filepath.Clean(filePaths[path])] = path
		filesByPath[path] = file
	}

	var collection repositoryGoplsRelationCollection
	for _, file := range files {
		usagePath := file.FileSnapshot.Path
		sites, err := collectGoIdentifierSites(usagePath, file.Content)
		if err != nil {
			return collection, err
		}
		for _, site := range sites {
			collection.requestCount++
			position, err := lspPositionForByteOffset(file.Content, site.startByte)
			if err != nil {
				return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls usage %s byte %d: %v", usagePath, site.startByte, err)
			}
			locations, err := requestGoplsDefinition(client, filePaths[usagePath], position)
			if err != nil {
				return collection, fmt.Errorf("requesting repository gopls definition for %s at byte %d: %w", usagePath, site.startByte, err)
			}
			collection.completedRequestCount++
			collection.locationCount += len(locations)
			relationStart := len(collection.relations)
			unselectedStart := collection.unselectedLocationCount
			selfDeclarationStart := collection.selfDeclarationLocationCount
			for _, location := range locations {
				targetAbsolute, err := pathFromFileURI(location.URI)
				if err != nil {
					return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls definition URI: %v", err)
				}
				targetPath, ok := pathsByAbsolute[filepath.Clean(targetAbsolute)]
				if !ok {
					collection.unselectedLocationCount++
					continue
				}
				targetFile := filesByPath[targetPath]
				targetStart, err := lspPositionByteOffset(targetFile.Content, location.Range.Start)
				if err != nil {
					return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls definition target %s start: %v", targetPath, err)
				}
				targetEnd, err := lspPositionByteOffset(targetFile.Content, location.Range.End)
				if err != nil {
					return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls definition target %s end: %v", targetPath, err)
				}
				if targetPath == usagePath && targetStart == site.startByte && targetEnd == site.endByte {
					collection.selfDeclarationLocationCount++
					continue
				}
				collection.relations = append(collection.relations, repositoryGoplsDefinition{
					usagePath:       usagePath,
					usageStartByte:  site.startByte,
					usageEndByte:    site.endByte,
					targetPath:      targetPath,
					targetStartByte: targetStart,
					targetEndByte:   targetEnd,
				})
			}
			if recorder != nil {
				recorder.record(
					len(locations),
					collection.selfDeclarationLocationCount-selfDeclarationStart,
					collection.unselectedLocationCount-unselectedStart,
					collection.relations[relationStart:],
				)
			}
		}
	}
	collection.relations = uniqueRepositoryGoplsDefinitions(collection.relations)
	return collection, nil
}

func newRepositoryGoplsDefinitionContributionRecorder() *repositoryGoplsDefinitionContributionRecorder {
	return &repositoryGoplsDefinitionContributionRecorder{
		seen: make(map[repositoryGoplsDefinition]struct{}),
	}
}

func (r *repositoryGoplsDefinitionContributionRecorder) record(
	locationCount int,
	selfDeclarationCount int,
	unselectedCount int,
	selected []repositoryGoplsDefinition,
) {
	r.contribution.requestCount++
	if locationCount == 0 {
		r.contribution.noLocationRequestCount++
		return
	}
	if len(selected) == 0 {
		switch {
		case selfDeclarationCount > 0 && unselectedCount == 0:
			r.contribution.selfOnlyRequestCount++
		case selfDeclarationCount == 0 && unselectedCount > 0:
			r.contribution.unselectedOnlyRequestCount++
		default:
			r.contribution.selfAndUnselectedOnlyRequestCount++
		}
		return
	}

	distinctTargets := 1
	if len(selected) > 1 {
		targets := make(map[repositoryGoplsDefinition]struct{}, len(selected))
		for _, definition := range selected {
			targets[definition] = struct{}{}
		}
		distinctTargets = len(targets)
	}
	if distinctTargets == 1 {
		r.contribution.selectedSingleTargetRequestCount++
	} else {
		r.contribution.selectedAmbiguousTargetRequestCount++
	}

	uniqueCount := 0
	duplicateCount := 0
	for _, definition := range selected {
		r.contribution.selectedLocationCount++
		if _, exists := r.seen[definition]; exists {
			r.contribution.duplicateLocationCount++
			duplicateCount++
			continue
		}
		r.seen[definition] = struct{}{}
		r.contribution.uniqueRelationCount++
		uniqueCount++
	}
	switch {
	case uniqueCount > 0 && duplicateCount == 0:
		r.contribution.selectedUniqueOnlyRequestCount++
	case uniqueCount == 0:
		r.contribution.selectedDuplicateOnlyRequestCount++
	default:
		r.contribution.selectedMixedContributionRequestCount++
	}
}

func (r *repositoryGoplsDefinitionContributionRecorder) result() repositoryGoplsDefinitionContribution {
	if r == nil {
		return repositoryGoplsDefinitionContribution{}
	}
	return r.contribution
}

func collectGoIdentifierSites(path string, source []byte) ([]goIdentifierSite, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, newDomainError(ErrorInvalidInput, "parsing Go identifier sites in %s: %v", path, err)
	}
	var sites []goIdentifierSite
	ast.Inspect(file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok || ident.Name == "_" {
			return true
		}
		sites = append(sites, goIdentifierSite{
			startByte: fset.Position(ident.Pos()).Offset,
			endByte:   fset.Position(ident.End()).Offset,
		})
		return true
	})
	slices.SortFunc(sites, func(a, b goIdentifierSite) int {
		if a.startByte != b.startByte {
			return a.startByte - b.startByte
		}
		return a.endByte - b.endByte
	})
	return sites, nil
}

func uniqueRepositoryGoplsDefinitions(definitions []repositoryGoplsDefinition) []repositoryGoplsDefinition {
	slices.SortFunc(definitions, compareRepositoryGoplsDefinition)
	unique := definitions[:0]
	for _, definition := range definitions {
		if len(unique) == 0 || compareRepositoryGoplsDefinition(unique[len(unique)-1], definition) != 0 {
			unique = append(unique, definition)
		}
	}
	return unique
}

func compareRepositoryGoplsDefinition(a, b repositoryGoplsDefinition) int {
	for _, comparison := range []int{
		strings.Compare(a.usagePath, b.usagePath),
		a.usageStartByte - b.usageStartByte,
		a.usageEndByte - b.usageEndByte,
		strings.Compare(a.targetPath, b.targetPath),
		a.targetStartByte - b.targetStartByte,
		a.targetEndByte - b.targetEndByte,
	} {
		if comparison != 0 {
			return comparison
		}
	}
	return 0
}

func materializeRepositoryGoplsDefinitions(
	input RepositoryExtractorInput,
	definitions []repositoryGoplsDefinition,
) ([]ExtractorProposalOutput, int, error) {
	return materializeRepositoryGoplsDefinitionsIndexed(input, definitions, nil)
}

func materializeRepositoryGoplsDefinitionsIndexed(
	input RepositoryExtractorInput,
	definitions []repositoryGoplsDefinition,
	index *repositoryGoplsMaterializationIndex,
) ([]ExtractorProposalOutput, int, error) {
	var declarations map[repositoryGoplsDeclarationKey]GoParserDeclaration
	var filesByPath map[string]RepositoryExtractorFile
	if index == nil {
		declarations = make(map[repositoryGoplsDeclarationKey]GoParserDeclaration)
		filesByPath = make(map[string]RepositoryExtractorFile, len(input.Files))
		for _, file := range input.Files {
			path := file.FileSnapshot.Path
			filesByPath[path] = file
			extraction, err := ExtractGoParserFile(GoParserFileInput{
				RepoID:    input.RepositorySnapshot.RepoID,
				CommitSHA: input.RepositorySnapshot.CommitSHA,
				Path:      path,
				Source:    file.Content,
			})
			if err != nil {
				return nil, 0, err
			}
			for _, declaration := range extraction.Declarations {
				declarations[repositoryGoplsDeclarationKey{
					path:      path,
					startByte: declaration.StartByte,
					endByte:   declaration.EndByte,
				}] = declaration
			}
		}
	} else {
		var err error
		declarations, err = index.declarationIndex()
		if err != nil {
			return nil, 0, err
		}
		filesByPath = index.filesByPath
	}

	type usageKey struct {
		path      string
		startByte int
		endByte   int
	}
	byUsage := make(map[usageKey][]GoParserDeclaration)
	for _, definition := range definitions {
		declaration, ok := declarations[repositoryGoplsDeclarationKey{
			path:      definition.targetPath,
			startByte: definition.targetStartByte,
			endByte:   definition.targetEndByte,
		}]
		if !ok {
			continue
		}
		key := usageKey{path: definition.usagePath, startByte: definition.usageStartByte, endByte: definition.usageEndByte}
		targets := byUsage[key]
		if len(targets) == 0 || targets[len(targets)-1].SymbolRef != declaration.SymbolRef {
			byUsage[key] = append(targets, declaration)
		}
	}

	usageKeys := make([]usageKey, 0, len(byUsage))
	for key := range byUsage {
		usageKeys = append(usageKeys, key)
	}
	slices.SortFunc(usageKeys, func(a, b usageKey) int {
		if comparison := strings.Compare(a.path, b.path); comparison != 0 {
			return comparison
		}
		if a.startByte != b.startByte {
			return a.startByte - b.startByte
		}
		return a.endByte - b.endByte
	})

	proposals := make([]ExtractorProposalOutput, 0, len(usageKeys))
	ambiguousUsageCount := 0
	for _, key := range usageKeys {
		targets := byUsage[key]
		if len(targets) > 1 {
			ambiguousUsageCount++
			continue
		}
		usageFile, ok := filesByPath[key.path]
		if !ok {
			return nil, 0, newDomainError(ErrorInvalidExtractorOutput, "repository gopls definition references unknown usage path %q", key.path)
		}
		target := targets[0]
		relation, statement, err := buildRepositoryGoplsDefinitionRelation(input.RepositorySnapshot, usageFile, key.startByte, key.endByte, target)
		if err != nil {
			return nil, 0, err
		}
		localID, err := stableID("local:", "repository_gopls_definition", struct {
			UsagePath      string `json:"usage_path"`
			UsageStartByte int    `json:"usage_start_byte"`
			TargetSymbol   string `json:"target_symbol"`
		}{UsagePath: key.path, UsageStartByte: key.startByte, TargetSymbol: target.SymbolRef})
		if err != nil {
			return nil, 0, err
		}
		proposals = append(proposals, ExtractorProposalOutput{
			ProposalLocalID: localID,
			StatementText:   statement,
			EvidenceRefs:    []string{},
			CodeRelation:    &relation,
		})
	}
	return proposals, ambiguousUsageCount, nil
}

func buildRepositoryGoplsDefinitionRelation(
	snapshot RepositorySnapshot,
	usageFile RepositoryExtractorFile,
	usageStartByte int,
	usageEndByte int,
	target GoParserDeclaration,
) (CodeRelationOutput, string, error) {
	if usageStartByte < 0 || usageEndByte <= usageStartByte || usageEndByte > len(usageFile.Content) {
		return CodeRelationOutput{}, "", newDomainError(ErrorSpanOutOfBounds, "repository gopls usage span [%d,%d) outside %s", usageStartByte, usageEndByte, usageFile.FileSnapshot.Path)
	}
	usageText := usageFile.Content[usageStartByte:usageEndByte]
	schemaVersion := CodeRelationSchemaV1
	if usageFile.FileSnapshot.Path == target.Path {
		schemaVersion = CodeRelationSchemaV3
	}
	relation := CodeRelationOutput{
		SchemaVersion: schemaVersion,
		RelationKind:  CodeRelationKindDefinition,
		RepoID:        snapshot.RepoID,
		CommitSHA:     snapshot.CommitSHA,
		Usage: CodeUsageSiteOutput{
			Path:            usageFile.FileSnapshot.Path,
			FileContentHash: usageFile.FileSnapshot.BlobHash,
			StartByte:       usageStartByte,
			EndByte:         usageEndByte,
			QuotedTextHash:  contentHash(usageText),
		},
		Target: CodeDeclarationEndpointOutput{
			Path:            target.Path,
			FileContentHash: target.CodeFact.FileContentHash,
			SymbolRef:       target.SymbolRef,
			SymbolKind:      target.Kind,
			QualifiedName:   target.CodeFact.QualifiedName,
			StartByte:       target.StartByte,
			EndByte:         target.EndByte,
			QuotedTextHash:  target.CodeFact.QuotedTextHash,
		},
	}
	resolved := ResolvedCodeRelation{
		SchemaVersion: relation.SchemaVersion,
		RelationKind:  relation.RelationKind,
		RepoID:        relation.RepoID,
		CommitSHA:     relation.CommitSHA,
		Usage: ResolvedCodeUsageSite{
			Path:            relation.Usage.Path,
			FileContentHash: relation.Usage.FileContentHash,
			StartByte:       relation.Usage.StartByte,
			EndByte:         relation.Usage.EndByte,
			StartLine:       codeLineForOffset(usageFile.Content, relation.Usage.StartByte),
			EndLine:         codeLineForOffset(usageFile.Content, relation.Usage.EndByte-1),
			QuotedTextHash:  relation.Usage.QuotedTextHash,
			QuotedText:      string(usageText),
		},
		Target: ResolvedCodeDeclarationEndpoint{
			Path:            relation.Target.Path,
			FileContentHash: relation.Target.FileContentHash,
			SymbolRef:       relation.Target.SymbolRef,
			SymbolKind:      relation.Target.SymbolKind,
			QualifiedName:   relation.Target.QualifiedName,
			StartByte:       relation.Target.StartByte,
			EndByte:         relation.Target.EndByte,
			StartLine:       target.StartLine,
			EndLine:         target.EndLine,
			QuotedTextHash:  relation.Target.QuotedTextHash,
			QuotedText:      target.Name[strings.LastIndex(target.Name, ".")+1:],
		},
	}
	return relation, codeDefinitionStatement(resolved), nil
}
