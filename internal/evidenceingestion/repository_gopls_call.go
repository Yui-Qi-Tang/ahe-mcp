package evidenceingestion

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type repositoryGoplsCallCollection struct {
	calls                         []repositoryGoplsCall
	incomingCalls                 []repositoryGoplsCall
	prepareRequestCount           int
	prepareCompletedRequestCount  int
	preparedItemCount             int
	outgoingRequestCount          int
	outgoingCompletedRequestCount int
	outgoingCallResultCount       int
	outgoingCallSiteCount         int
	outgoingUnselectedResultCount int
	outgoingUnselectedSiteCount   int
	incomingRequestCount          int
	incomingCompletedRequestCount int
	incomingCallResultCount       int
	incomingCallSiteCount         int
	incomingUnselectedResultCount int
	incomingUnselectedSiteCount   int
	failureStage                  string
}

type repositoryGoplsCall struct {
	callerPath      string
	callerStartByte int
	callerEndByte   int
	usagePath       string
	usageStartByte  int
	usageEndByte    int
	targetPath      string
	targetStartByte int
	targetEndByte   int
}

func requestRepositoryGoplsCalls(
	client *lspClient,
	input RepositoryExtractorInput,
	filePaths map[string]string,
) (repositoryGoplsCallCollection, error) {
	pathsByAbsolute := make(map[string]string, len(filePaths))
	filesByPath := make(map[string]RepositoryExtractorFile, len(input.Files))
	for _, file := range input.Files {
		path := file.FileSnapshot.Path
		pathsByAbsolute[filepath.Clean(filePaths[path])] = path
		filesByPath[path] = file
	}

	var collection repositoryGoplsCallCollection
	for _, callerFile := range input.Files {
		collection.failureStage = repositoryGoplsStagePrepareCallHierarchy
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      callerFile.FileSnapshot.Path,
			Source:    callerFile.Content,
		})
		if err != nil {
			return collection, err
		}
		for _, caller := range extraction.Declarations {
			if !isCallableCodeSymbol(caller.Kind) {
				continue
			}
			collection.prepareRequestCount++
			position, err := lspPositionForByteOffset(callerFile.Content, caller.StartByte)
			if err != nil {
				return collection, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls call hierarchy caller %s: %v", caller.SymbolRef, err)
			}
			items, err := requestGoplsPrepareCallHierarchy(client, filePaths[caller.Path], position)
			if err != nil {
				return collection, fmt.Errorf("preparing repository gopls call hierarchy for %s: %w", caller.SymbolRef, err)
			}
			collection.prepareCompletedRequestCount++
			prepared, err := matchingRepositoryCallHierarchyItems(items, caller, callerFile, pathsByAbsolute)
			if err != nil {
				return collection, err
			}
			if len(prepared) == 0 {
				continue
			}
			if len(prepared) != 1 {
				return collection, newDomainError(ErrorInvalidExtractorOutput, "repository gopls caller %s prepared %d matching call hierarchy items", caller.SymbolRef, len(prepared))
			}
			collection.preparedItemCount++
			collection.outgoingRequestCount++
			collection.failureStage = repositoryGoplsStageOutgoingCalls
			outgoing, err := requestGoplsOutgoingCalls(client, prepared[0])
			if err != nil {
				return collection, fmt.Errorf("requesting repository gopls outgoing calls for %s: %w", caller.SymbolRef, err)
			}
			collection.outgoingCompletedRequestCount++
			collection.outgoingCallResultCount += len(outgoing)
			if err := collectRepositoryGoplsOutgoingCalls(&collection, outgoing, caller, callerFile, filesByPath, pathsByAbsolute); err != nil {
				return collection, err
			}

			collection.incomingRequestCount++
			collection.failureStage = repositoryGoplsStageIncomingCalls
			incoming, err := requestGoplsIncomingCalls(client, prepared[0])
			if err != nil {
				return collection, fmt.Errorf("requesting repository gopls incoming calls for %s: %w", caller.SymbolRef, err)
			}
			collection.incomingCompletedRequestCount++
			collection.incomingCallResultCount += len(incoming)
			if err := collectRepositoryGoplsIncomingCalls(&collection, incoming, caller, filesByPath, pathsByAbsolute); err != nil {
				return collection, err
			}
			collection.failureStage = repositoryGoplsStagePrepareCallHierarchy
		}
	}
	collection.calls = uniqueRepositoryGoplsCalls(collection.calls)
	collection.incomingCalls = uniqueRepositoryGoplsCalls(collection.incomingCalls)
	collection.failureStage = ""
	return collection, nil
}

func collectRepositoryGoplsOutgoingCalls(
	collection *repositoryGoplsCallCollection,
	outgoing []lspCallHierarchyOutgoingCall,
	caller GoParserDeclaration,
	callerFile RepositoryExtractorFile,
	filesByPath map[string]RepositoryExtractorFile,
	pathsByAbsolute map[string]string,
) error {
	for _, call := range outgoing {
		collection.outgoingCallSiteCount += len(call.FromRanges)
		targetPath, targetStart, targetEnd, ok, err := selectedCallHierarchyDeclaration(call.To, filesByPath, pathsByAbsolute)
		if err != nil {
			return err
		}
		if !ok {
			collection.outgoingUnselectedResultCount++
			collection.outgoingUnselectedSiteCount += len(call.FromRanges)
			continue
		}
		for _, callRange := range call.FromRanges {
			usageStart, err := lspPositionByteOffset(callerFile.Content, callRange.Start)
			if err != nil {
				return newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls outgoing call site in %s start: %v", caller.Path, err)
			}
			usageEnd, err := lspPositionByteOffset(callerFile.Content, callRange.End)
			if err != nil {
				return newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls outgoing call site in %s end: %v", caller.Path, err)
			}
			collection.calls = append(collection.calls, repositoryGoplsCall{
				callerPath:      caller.Path,
				callerStartByte: caller.StartByte,
				callerEndByte:   caller.EndByte,
				usagePath:       caller.Path,
				usageStartByte:  usageStart,
				usageEndByte:    usageEnd,
				targetPath:      targetPath,
				targetStartByte: targetStart,
				targetEndByte:   targetEnd,
			})
		}
	}
	return nil
}

func collectRepositoryGoplsIncomingCalls(
	collection *repositoryGoplsCallCollection,
	incoming []lspCallHierarchyIncomingCall,
	callee GoParserDeclaration,
	filesByPath map[string]RepositoryExtractorFile,
	pathsByAbsolute map[string]string,
) error {
	for _, call := range incoming {
		collection.incomingCallSiteCount += len(call.FromRanges)
		callerPath, callerStart, callerEnd, ok, err := selectedCallHierarchyDeclaration(call.From, filesByPath, pathsByAbsolute)
		if err != nil {
			return err
		}
		if !ok {
			collection.incomingUnselectedResultCount++
			collection.incomingUnselectedSiteCount += len(call.FromRanges)
			continue
		}
		callerFile := filesByPath[callerPath]
		for _, callRange := range call.FromRanges {
			usageStart, err := lspPositionByteOffset(callerFile.Content, callRange.Start)
			if err != nil {
				return newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls incoming call site in %s start: %v", callerPath, err)
			}
			usageEnd, err := lspPositionByteOffset(callerFile.Content, callRange.End)
			if err != nil {
				return newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls incoming call site in %s end: %v", callerPath, err)
			}
			collection.incomingCalls = append(collection.incomingCalls, repositoryGoplsCall{
				callerPath:      callerPath,
				callerStartByte: callerStart,
				callerEndByte:   callerEnd,
				usagePath:       callerPath,
				usageStartByte:  usageStart,
				usageEndByte:    usageEnd,
				targetPath:      callee.Path,
				targetStartByte: callee.StartByte,
				targetEndByte:   callee.EndByte,
			})
		}
	}
	return nil
}

func matchingRepositoryCallHierarchyItems(
	items []lspCallHierarchyItem,
	caller GoParserDeclaration,
	callerFile RepositoryExtractorFile,
	pathsByAbsolute map[string]string,
) ([]lspCallHierarchyItem, error) {
	var matching []lspCallHierarchyItem
	for _, item := range items {
		absolute, err := pathFromFileURI(item.URI)
		if err != nil {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls prepared caller URI: %v", err)
		}
		path, ok := pathsByAbsolute[filepath.Clean(absolute)]
		if !ok || path != caller.Path {
			continue
		}
		start, err := lspPositionByteOffset(callerFile.Content, item.SelectionRange.Start)
		if err != nil {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls prepared caller %s start: %v", caller.SymbolRef, err)
		}
		end, err := lspPositionByteOffset(callerFile.Content, item.SelectionRange.End)
		if err != nil {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls prepared caller %s end: %v", caller.SymbolRef, err)
		}
		if start == caller.StartByte && end == caller.EndByte {
			matching = append(matching, item)
		}
	}
	return matching, nil
}

func selectedCallHierarchyDeclaration(
	item lspCallHierarchyItem,
	filesByPath map[string]RepositoryExtractorFile,
	pathsByAbsolute map[string]string,
) (string, int, int, bool, error) {
	absolute, err := pathFromFileURI(item.URI)
	if err != nil {
		return "", 0, 0, false, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls call hierarchy declaration URI: %v", err)
	}
	path, ok := pathsByAbsolute[filepath.Clean(absolute)]
	if !ok {
		return "", 0, 0, false, nil
	}
	file := filesByPath[path]
	start, err := lspPositionByteOffset(file.Content, item.SelectionRange.Start)
	if err != nil {
		return "", 0, 0, false, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls call hierarchy declaration %s start: %v", path, err)
	}
	end, err := lspPositionByteOffset(file.Content, item.SelectionRange.End)
	if err != nil {
		return "", 0, 0, false, newDomainError(ErrorInvalidExtractorOutput, "mapping repository gopls call hierarchy declaration %s end: %v", path, err)
	}
	return path, start, end, true, nil
}

func uniqueRepositoryGoplsCalls(calls []repositoryGoplsCall) []repositoryGoplsCall {
	slices.SortFunc(calls, compareRepositoryGoplsCall)
	unique := calls[:0]
	for _, call := range calls {
		if len(unique) == 0 || compareRepositoryGoplsCall(unique[len(unique)-1], call) != 0 {
			unique = append(unique, call)
		}
	}
	return unique
}

func compareRepositoryGoplsCall(a, b repositoryGoplsCall) int {
	for _, comparison := range []int{
		strings.Compare(a.callerPath, b.callerPath),
		a.callerStartByte - b.callerStartByte,
		a.callerEndByte - b.callerEndByte,
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

func materializeRepositoryGoplsCalls(input RepositoryExtractorInput, calls []repositoryGoplsCall) ([]ExtractorProposalOutput, error) {
	return materializeRepositoryGoplsCallsIndexed(input, calls, nil)
}

func materializeRepositoryGoplsCallsIndexed(
	input RepositoryExtractorInput,
	calls []repositoryGoplsCall,
	index *repositoryGoplsMaterializationIndex,
) ([]ExtractorProposalOutput, error) {
	declarations, filesByPath, err := repositoryGoplsCallGroundingIndex(input, index)
	if err != nil {
		return nil, err
	}
	return materializeRepositoryGoplsCallsWithIndex(input, calls, declarations, filesByPath)
}

func materializeRepositoryGoplsCallsWithIndex(
	input RepositoryExtractorInput,
	calls []repositoryGoplsCall,
	declarations map[repositoryGoplsDeclarationKey]GoParserDeclaration,
	filesByPath map[string]RepositoryExtractorFile,
) ([]ExtractorProposalOutput, error) {
	proposals := make([]ExtractorProposalOutput, 0, len(calls))
	for _, call := range uniqueRepositoryGoplsCalls(calls) {
		caller, callerOK := declarations[repositoryGoplsDeclarationKey{path: call.callerPath, startByte: call.callerStartByte, endByte: call.callerEndByte}]
		target, targetOK := declarations[repositoryGoplsDeclarationKey{path: call.targetPath, startByte: call.targetStartByte, endByte: call.targetEndByte}]
		if !callerOK || !targetOK || !isCallableCodeSymbol(caller.Kind) || !isCallableCodeSymbol(target.Kind) {
			continue
		}
		usageFile, ok := filesByPath[call.usagePath]
		if !ok {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls call references unknown usage path %q", call.usagePath)
		}
		relation, statement, err := buildRepositoryGoplsCallRelation(input.RepositorySnapshot, usageFile, call.usageStartByte, call.usageEndByte, caller, target)
		if err != nil {
			return nil, err
		}
		localID, err := stableID("local:", "repository_gopls_call", struct {
			CallerSymbol   string `json:"caller_symbol"`
			UsagePath      string `json:"usage_path"`
			UsageStartByte int    `json:"usage_start_byte"`
			TargetSymbol   string `json:"target_symbol"`
		}{CallerSymbol: caller.SymbolRef, UsagePath: call.usagePath, UsageStartByte: call.usageStartByte, TargetSymbol: target.SymbolRef})
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, ExtractorProposalOutput{
			ProposalLocalID: localID,
			StatementText:   statement,
			EvidenceRefs:    []string{},
			CodeRelation:    &relation,
		})
	}
	return proposals, nil
}

func materializeRepositoryGoplsIncomingCalls(input RepositoryExtractorInput, calls []repositoryGoplsCall) ([]ExtractorProposalOutput, error) {
	return materializeRepositoryGoplsIncomingCallsIndexed(input, calls, nil)
}

func materializeRepositoryGoplsIncomingCallsIndexed(
	input RepositoryExtractorInput,
	calls []repositoryGoplsCall,
	index *repositoryGoplsMaterializationIndex,
) ([]ExtractorProposalOutput, error) {
	declarations, filesByPath, err := repositoryGoplsCallGroundingIndex(input, index)
	if err != nil {
		return nil, err
	}

	supported := make([]repositoryGoplsCall, 0, len(calls))
	for _, call := range uniqueRepositoryGoplsCalls(calls) {
		caller, ok := declarations[repositoryGoplsDeclarationKey{path: call.callerPath, startByte: call.callerStartByte, endByte: call.callerEndByte}]
		if !ok || !isCallableCodeSymbol(caller.Kind) {
			continue
		}
		usageFile, ok := filesByPath[call.usagePath]
		if !ok {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls incoming call references unknown usage path %q", call.usagePath)
		}
		usage := ResolvedCodeUsageSite{
			Path:      call.usagePath,
			StartByte: call.usageStartByte,
			EndByte:   call.usageEndByte,
		}
		var callSiteWithinCaller bool
		if index == nil {
			callSiteWithinCaller = goCallSiteWithinCaller(
				usageFile.Content,
				call.usagePath,
				resolvedDeclarationForStatement(caller),
				usage,
			)
		} else {
			callSiteWithinCaller = index.grounding.callSiteWithinCaller(
				usageFile,
				resolvedDeclarationForStatement(caller),
				usage,
			)
		}
		if !callSiteWithinCaller {
			continue
		}
		supported = append(supported, call)
	}
	return materializeRepositoryGoplsCallsWithIndex(input, supported, declarations, filesByPath)
}

func repositoryGoplsCallGroundingIndex(
	input RepositoryExtractorInput,
	index *repositoryGoplsMaterializationIndex,
) (map[repositoryGoplsDeclarationKey]GoParserDeclaration, map[string]RepositoryExtractorFile, error) {
	if index != nil {
		declarations, err := index.declarationIndex()
		if err != nil {
			return nil, nil, err
		}
		return declarations, index.filesByPath, nil
	}

	declarations := make(map[repositoryGoplsDeclarationKey]GoParserDeclaration)
	filesByPath := make(map[string]RepositoryExtractorFile, len(input.Files))
	for _, file := range input.Files {
		filesByPath[file.FileSnapshot.Path] = file
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      file.FileSnapshot.Path,
			Source:    file.Content,
		})
		if err != nil {
			return nil, nil, err
		}
		for _, declaration := range extraction.Declarations {
			declarations[repositoryGoplsDeclarationKey{path: declaration.Path, startByte: declaration.StartByte, endByte: declaration.EndByte}] = declaration
		}
	}
	return declarations, filesByPath, nil
}

func buildRepositoryGoplsCallRelation(
	snapshot RepositorySnapshot,
	usageFile RepositoryExtractorFile,
	usageStartByte int,
	usageEndByte int,
	caller GoParserDeclaration,
	target GoParserDeclaration,
) (CodeRelationOutput, string, error) {
	if usageStartByte < 0 || usageEndByte <= usageStartByte || usageEndByte > len(usageFile.Content) {
		return CodeRelationOutput{}, "", newDomainError(ErrorSpanOutOfBounds, "repository gopls call-site span [%d,%d) outside %s", usageStartByte, usageEndByte, usageFile.FileSnapshot.Path)
	}
	callerEndpoint := codeDeclarationOutput(caller)
	targetEndpoint := codeDeclarationOutput(target)
	usageText := usageFile.Content[usageStartByte:usageEndByte]
	schemaVersion := CodeRelationSchemaV2
	if caller.Path == target.Path {
		schemaVersion = CodeRelationSchemaV3
	}
	relation := CodeRelationOutput{
		SchemaVersion: schemaVersion,
		RelationKind:  CodeRelationKindCall,
		RepoID:        snapshot.RepoID,
		CommitSHA:     snapshot.CommitSHA,
		Caller:        &callerEndpoint,
		Usage: CodeUsageSiteOutput{
			Path:            usageFile.FileSnapshot.Path,
			FileContentHash: usageFile.FileSnapshot.BlobHash,
			StartByte:       usageStartByte,
			EndByte:         usageEndByte,
			QuotedTextHash:  contentHash(usageText),
		},
		Target: targetEndpoint,
	}
	resolvedCaller := resolvedDeclarationForStatement(caller)
	resolved := ResolvedCodeRelation{
		SchemaVersion: relation.SchemaVersion,
		RelationKind:  relation.RelationKind,
		RepoID:        relation.RepoID,
		CommitSHA:     relation.CommitSHA,
		Caller:        &resolvedCaller,
		Usage: ResolvedCodeUsageSite{
			Path:       relation.Usage.Path,
			StartLine:  codeLineForOffset(usageFile.Content, usageStartByte),
			EndLine:    codeLineForOffset(usageFile.Content, usageEndByte-1),
			QuotedText: string(usageText),
		},
		Target: resolvedDeclarationForStatement(target),
	}
	return relation, codeCallStatement(resolved), nil
}

func codeDeclarationOutput(declaration GoParserDeclaration) CodeDeclarationEndpointOutput {
	return CodeDeclarationEndpointOutput{
		Path:            declaration.Path,
		FileContentHash: declaration.CodeFact.FileContentHash,
		SymbolRef:       declaration.SymbolRef,
		SymbolKind:      declaration.Kind,
		QualifiedName:   declaration.CodeFact.QualifiedName,
		StartByte:       declaration.StartByte,
		EndByte:         declaration.EndByte,
		QuotedTextHash:  declaration.CodeFact.QuotedTextHash,
	}
}

func resolvedDeclarationForStatement(declaration GoParserDeclaration) ResolvedCodeDeclarationEndpoint {
	return ResolvedCodeDeclarationEndpoint{
		Path:          declaration.Path,
		SymbolRef:     declaration.SymbolRef,
		SymbolKind:    declaration.Kind,
		QualifiedName: declaration.CodeFact.QualifiedName,
		StartByte:     declaration.StartByte,
		EndByte:       declaration.EndByte,
		StartLine:     declaration.StartLine,
		EndLine:       declaration.EndLine,
	}
}
