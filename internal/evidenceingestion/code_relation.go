package evidenceingestion

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

func resolveRepositoryCodeRelation(
	input RepositoryExtractorInput,
	proposal ExtractorProposalOutput,
	sourceRefs []ResolvedSourceRef,
) (*ResolvedCodeRelation, error) {
	return resolveRepositoryCodeRelationIndexed(input, proposal, sourceRefs, nil)
}

func resolveRepositoryCodeRelationIndexed(
	input RepositoryExtractorInput,
	proposal ExtractorProposalOutput,
	sourceRefs []ResolvedSourceRef,
	index *repositoryGroundingIndex,
) (*ResolvedCodeRelation, error) {
	if proposal.CodeRelation == nil {
		return nil, nil
	}
	relation := normalizeCodeRelationOutput(*proposal.CodeRelation)
	if err := validateCodeRelationShape(relation); err != nil {
		return nil, err
	}
	if relation.RepoID != input.RepositorySnapshot.RepoID || relation.CommitSHA != input.RepositorySnapshot.CommitSHA {
		return nil, newDomainError(ErrorRepositoryRevisionMismatch, "code relation repository revision %s/%s does not match snapshot %s/%s", relation.RepoID, relation.CommitSHA, input.RepositorySnapshot.RepoID, input.RepositorySnapshot.CommitSHA)
	}

	var filesByPath map[string]RepositoryExtractorFile
	if index == nil {
		filesByPath = make(map[string]RepositoryExtractorFile, len(input.Files))
		for _, file := range input.Files {
			filesByPath[file.FileSnapshot.Path] = file
		}
	} else {
		filesByPath = index.filesByPath
	}
	usageFile, ok := filesByPath[relation.Usage.Path]
	if !ok {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation usage references unknown path %q", relation.Usage.Path)
	}
	targetFile, ok := filesByPath[relation.Target.Path]
	if !ok {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation target references unknown path %q", relation.Target.Path)
	}
	var caller *ResolvedCodeDeclarationEndpoint
	if relation.Caller != nil {
		callerFile, ok := filesByPath[relation.Caller.Path]
		if !ok {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation caller references unknown path %q", relation.Caller.Path)
		}
		resolvedCaller, err := resolveCodeDeclarationEndpointIndexed(
			input.RepositorySnapshot,
			callerFile,
			*relation.Caller,
			index,
		)
		if err != nil {
			return nil, err
		}
		if !codeRelationEndpointCovered(sourceRefs, resolvedCaller.Path, resolvedCaller.StartByte, resolvedCaller.EndByte) {
			return nil, newDomainError(ErrorUnknownSpan, "code relation caller span [%d,%d) in %s is not covered by source refs", resolvedCaller.StartByte, resolvedCaller.EndByte, resolvedCaller.Path)
		}
		caller = &resolvedCaller
	}

	usage, err := resolveCodeUsageSiteIndexed(usageFile, relation.Usage, index)
	if err != nil {
		return nil, err
	}
	if !codeRelationEndpointCovered(sourceRefs, usage.Path, usage.StartByte, usage.EndByte) {
		return nil, newDomainError(ErrorUnknownSpan, "code relation usage span [%d,%d) in %s is not covered by source refs", usage.StartByte, usage.EndByte, usage.Path)
	}
	target, err := resolveCodeDeclarationEndpointIndexed(
		input.RepositorySnapshot,
		targetFile,
		relation.Target,
		index,
	)
	if err != nil {
		return nil, err
	}
	if !codeRelationEndpointCovered(sourceRefs, target.Path, target.StartByte, target.EndByte) {
		return nil, newDomainError(ErrorUnknownSpan, "code relation target span [%d,%d) in %s is not covered by source refs", target.StartByte, target.EndByte, target.Path)
	}
	if usage.QuotedText != target.QuotedText {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation usage identifier %q does not match target declaration %q", usage.QuotedText, target.QuotedText)
	}

	resolved := &ResolvedCodeRelation{
		SchemaVersion: relation.SchemaVersion,
		RelationKind:  relation.RelationKind,
		RepoID:        relation.RepoID,
		CommitSHA:     relation.CommitSHA,
		Caller:        caller,
		Usage:         usage,
		Target:        target,
	}
	var expectedStatement string
	switch resolved.RelationKind {
	case CodeRelationKindDefinition:
		expectedStatement = codeDefinitionStatement(*resolved)
	case CodeRelationKindCall:
		if resolved.Caller == nil {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "code call relation requires caller endpoint")
		}
		if !isCallableCodeSymbol(resolved.Caller.SymbolKind) || !isCallableCodeSymbol(resolved.Target.SymbolKind) {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "code call relation requires function or method endpoints")
		}
		callerFile := filesByPath[resolved.Caller.Path]
		var callSiteWithinCaller bool
		if index == nil {
			callSiteWithinCaller = goCallSiteWithinCaller(
				callerFile.Content,
				resolved.Caller.Path,
				*resolved.Caller,
				resolved.Usage,
			)
		} else {
			callSiteWithinCaller = index.callSiteWithinCaller(
				callerFile,
				*resolved.Caller,
				resolved.Usage,
			)
		}
		if !callSiteWithinCaller {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "code call site [%d,%d) in %s is not a call within caller %s", resolved.Usage.StartByte, resolved.Usage.EndByte, resolved.Usage.Path, resolved.Caller.QualifiedName)
		}
		expectedStatement = codeCallStatement(*resolved)
	}
	if proposal.StatementText != expectedStatement {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation statement %q, want %q", proposal.StatementText, expectedStatement)
	}
	return resolved, nil
}

func validateCodeRelationShape(relation CodeRelationOutput) error {
	switch {
	case relation.SchemaVersion == CodeRelationSchemaV1 && relation.RelationKind == CodeRelationKindDefinition:
		if relation.Caller != nil {
			return newDomainError(ErrorInvalidExtractorOutput, "code definition relation cannot include caller endpoint")
		}
		if relation.Usage.Path == relation.Target.Path {
			return newDomainError(ErrorInvalidExtractorOutput, "code definition relation must cross file boundaries")
		}
		return nil
	case relation.SchemaVersion == CodeRelationSchemaV2 && relation.RelationKind == CodeRelationKindCall:
		if relation.Caller == nil {
			return newDomainError(ErrorInvalidExtractorOutput, "code call relation requires caller endpoint")
		}
		if relation.Caller.Path != relation.Usage.Path {
			return newDomainError(ErrorInvalidExtractorOutput, "code call relation caller and call site must share a file")
		}
		if relation.Caller.Path == relation.Target.Path {
			return newDomainError(ErrorInvalidExtractorOutput, "code call relation must cross file boundaries")
		}
		return nil
	case relation.SchemaVersion == CodeRelationSchemaV3 && relation.RelationKind == CodeRelationKindDefinition:
		if relation.Caller != nil {
			return newDomainError(ErrorInvalidExtractorOutput, "code definition relation cannot include caller endpoint")
		}
		if relation.Usage.Path != relation.Target.Path {
			return newDomainError(ErrorInvalidExtractorOutput, "code relation v3 definition must remain within one file")
		}
		if relation.Usage.StartByte == relation.Target.StartByte && relation.Usage.EndByte == relation.Target.EndByte {
			return newDomainError(ErrorInvalidExtractorOutput, "code relation v3 definition usage cannot be its declaration endpoint")
		}
		return nil
	case relation.SchemaVersion == CodeRelationSchemaV3 && relation.RelationKind == CodeRelationKindCall:
		if relation.Caller == nil {
			return newDomainError(ErrorInvalidExtractorOutput, "code call relation requires caller endpoint")
		}
		if relation.Caller.Path != relation.Usage.Path || relation.Caller.Path != relation.Target.Path {
			return newDomainError(ErrorInvalidExtractorOutput, "code relation v3 call caller, call site, and target must remain within one file")
		}
		return nil
	default:
		return newDomainError(ErrorInvalidExtractorOutput, "code relation schema/kind %q/%q is not supported", relation.SchemaVersion, relation.RelationKind)
	}
}

func normalizeCodeRelationOutput(relation CodeRelationOutput) CodeRelationOutput {
	relation.SchemaVersion = strings.TrimSpace(relation.SchemaVersion)
	relation.RelationKind = strings.TrimSpace(relation.RelationKind)
	relation.RepoID = strings.TrimSpace(relation.RepoID)
	relation.CommitSHA = strings.TrimSpace(relation.CommitSHA)
	if relation.Caller != nil {
		caller := normalizeCodeDeclarationEndpoint(*relation.Caller)
		relation.Caller = &caller
	}
	relation.Usage.Path = strings.TrimSpace(relation.Usage.Path)
	relation.Usage.FileContentHash = strings.TrimSpace(relation.Usage.FileContentHash)
	relation.Usage.QuotedTextHash = strings.TrimSpace(relation.Usage.QuotedTextHash)
	relation.Target = normalizeCodeDeclarationEndpoint(relation.Target)
	return relation
}

func normalizeCodeDeclarationEndpoint(endpoint CodeDeclarationEndpointOutput) CodeDeclarationEndpointOutput {
	endpoint.Path = strings.TrimSpace(endpoint.Path)
	endpoint.FileContentHash = strings.TrimSpace(endpoint.FileContentHash)
	endpoint.SymbolRef = strings.TrimSpace(endpoint.SymbolRef)
	endpoint.SymbolKind = strings.TrimSpace(endpoint.SymbolKind)
	endpoint.QualifiedName = strings.TrimSpace(endpoint.QualifiedName)
	endpoint.QuotedTextHash = strings.TrimSpace(endpoint.QuotedTextHash)
	return endpoint
}

func resolveCodeUsageSiteIndexed(
	file RepositoryExtractorFile,
	usage CodeUsageSiteOutput,
	index *repositoryGroundingIndex,
) (ResolvedCodeUsageSite, error) {
	quoted, startLine, endLine, err := verifyRepositoryCodeSpanIndexed(
		file,
		usage.Path,
		usage.FileContentHash,
		usage.StartByte,
		usage.EndByte,
		usage.QuotedTextHash,
		index,
	)
	if err != nil {
		return ResolvedCodeUsageSite{}, err
	}
	if !token.IsIdentifier(string(quoted)) {
		return ResolvedCodeUsageSite{}, newDomainError(ErrorInvalidExtractorOutput, "code relation usage span [%d,%d) in %s is not a Go identifier", usage.StartByte, usage.EndByte, usage.Path)
	}
	var identifierAtSpan bool
	if index == nil {
		identifierAtSpan = goIdentifierAtSpan(
			file.Content,
			usage.Path,
			usage.StartByte,
			usage.EndByte,
		)
	} else {
		identifierAtSpan = index.identifierAtSpan(file, usage.StartByte, usage.EndByte)
	}
	if !identifierAtSpan {
		return ResolvedCodeUsageSite{}, newDomainError(ErrorInvalidExtractorOutput, "code relation usage span [%d,%d) in %s is not a Go identifier", usage.StartByte, usage.EndByte, usage.Path)
	}
	return ResolvedCodeUsageSite{
		Path:            usage.Path,
		FileContentHash: usage.FileContentHash,
		StartByte:       usage.StartByte,
		EndByte:         usage.EndByte,
		StartLine:       startLine,
		EndLine:         endLine,
		QuotedTextHash:  usage.QuotedTextHash,
		QuotedText:      string(quoted),
	}, nil
}

func resolveCodeDeclarationEndpointIndexed(
	snapshot RepositorySnapshot,
	file RepositoryExtractorFile,
	target CodeDeclarationEndpointOutput,
	index *repositoryGroundingIndex,
) (ResolvedCodeDeclarationEndpoint, error) {
	if _, ok := supportedCodeSymbolKinds[target.SymbolKind]; !ok {
		return ResolvedCodeDeclarationEndpoint{}, newDomainError(ErrorInvalidExtractorOutput, "code relation target symbol kind %q is not supported", target.SymbolKind)
	}
	if target.QualifiedName == "" {
		return ResolvedCodeDeclarationEndpoint{}, newDomainError(ErrorInvalidExtractorOutput, "code relation target qualified_name is required")
	}
	expectedSymbolRef := codeSymbolRef(snapshot.RepoID, snapshot.CommitSHA, target.Path, target.QualifiedName)
	if target.SymbolRef != expectedSymbolRef {
		return ResolvedCodeDeclarationEndpoint{}, newDomainError(ErrorInvalidExtractorOutput, "code relation target symbol_ref %q, want %q", target.SymbolRef, expectedSymbolRef)
	}
	quoted, startLine, endLine, err := verifyRepositoryCodeSpanIndexed(
		file,
		target.Path,
		target.FileContentHash,
		target.StartByte,
		target.EndByte,
		target.QuotedTextHash,
		index,
	)
	if err != nil {
		return ResolvedCodeDeclarationEndpoint{}, err
	}
	resolvedFact := ResolvedCodeFact{
		SchemaVersion:   CodeFactSchemaV1,
		FactKind:        CodeFactKindDeclaration,
		RepoID:          snapshot.RepoID,
		CommitSHA:       snapshot.CommitSHA,
		Path:            target.Path,
		FileContentHash: target.FileContentHash,
		SymbolRef:       target.SymbolRef,
		SymbolKind:      target.SymbolKind,
		QualifiedName:   target.QualifiedName,
		StartByte:       target.StartByte,
		EndByte:         target.EndByte,
		StartLine:       startLine,
		EndLine:         endLine,
		QuotedTextHash:  target.QuotedTextHash,
		QuotedText:      string(quoted),
	}
	var codeFacts map[codeFactSpan]CodeFactOutput
	if index == nil {
		codeFacts, err = goCodeFactIndex(repositoryFileAttemptContext(snapshot, file))
	} else {
		codeFacts, err = index.codeFacts(file)
	}
	if err != nil {
		return ResolvedCodeDeclarationEndpoint{}, err
	}
	if err := verifyGoCodeFact(codeFacts, resolvedFact); err != nil {
		return ResolvedCodeDeclarationEndpoint{}, err
	}
	return ResolvedCodeDeclarationEndpoint{
		Path:            target.Path,
		FileContentHash: target.FileContentHash,
		SymbolRef:       target.SymbolRef,
		SymbolKind:      target.SymbolKind,
		QualifiedName:   target.QualifiedName,
		StartByte:       target.StartByte,
		EndByte:         target.EndByte,
		StartLine:       startLine,
		EndLine:         endLine,
		QuotedTextHash:  target.QuotedTextHash,
		QuotedText:      string(quoted),
	}, nil
}

func verifyRepositoryCodeSpanIndexed(
	file RepositoryExtractorFile,
	path string,
	fileContentHash string,
	startByte int,
	endByte int,
	quotedTextHash string,
	index *repositoryGroundingIndex,
) ([]byte, int, int, error) {
	if path != file.FileSnapshot.Path {
		return nil, 0, 0, newDomainError(ErrorInvalidExtractorOutput, "code relation path %q does not match file snapshot path %q", path, file.FileSnapshot.Path)
	}
	if fileContentHash != file.FileSnapshot.BlobHash {
		return nil, 0, 0, newDomainError(ErrorQuotedHashMismatch, "code relation file hash %s does not match persisted bytes for %s", fileContentHash, path)
	}
	var gotContentHash string
	if index == nil {
		gotContentHash = contentHash(file.Content)
	} else {
		gotContentHash = index.fileBytes(file).contentHash
	}
	if fileContentHash != gotContentHash {
		return nil, 0, 0, newDomainError(ErrorQuotedHashMismatch, "code relation file hash %s does not match persisted bytes for %s", fileContentHash, path)
	}
	if startByte < 0 || endByte <= startByte || endByte > len(file.Content) {
		return nil, 0, 0, newDomainError(ErrorSpanOutOfBounds, "code relation span [%d,%d) outside %s bytes length %d", startByte, endByte, path, len(file.Content))
	}
	quoted := file.Content[startByte:endByte]
	if got := contentHash(quoted); got != quotedTextHash {
		return nil, 0, 0, newDomainError(ErrorQuotedHashMismatch, "code relation quoted hash %s, want %s", quotedTextHash, got)
	}
	startLine := codeLineForOffset(file.Content, startByte)
	endLine := codeLineForOffset(file.Content, endByte-1)
	if index != nil {
		startLine = index.lineForOffset(file, startByte)
		endLine = index.lineForOffset(file, endByte-1)
	}
	return quoted, startLine, endLine, nil
}

func goIdentifierAtSpan(source []byte, path string, startByte, endByte int) bool {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return !found
		}
		start := fset.Position(ident.Pos()).Offset
		end := fset.Position(ident.End()).Offset
		if start == startByte && end == endByte {
			found = true
			return false
		}
		return !found
	})
	return found
}

func goCallSiteWithinCaller(source []byte, path string, caller ResolvedCodeDeclarationEndpoint, usage ResolvedCodeUsageSite) bool {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	found := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		callerStart := fset.Position(function.Name.Pos()).Offset
		callerEnd := fset.Position(function.Name.End()).Offset
		if callerStart != caller.StartByte || callerEnd != caller.EndByte {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if found {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier := calledGoIdentifier(call.Fun)
			if identifier == nil {
				return true
			}
			start := fset.Position(identifier.Pos()).Offset
			end := fset.Position(identifier.End()).Offset
			if start == usage.StartByte && end == usage.EndByte {
				found = true
				return false
			}
			return true
		})
		break
	}
	return found
}

func calledGoIdentifier(expression ast.Expr) *ast.Ident {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression
	case *ast.SelectorExpr:
		return expression.Sel
	case *ast.IndexExpr:
		return calledGoIdentifier(expression.X)
	case *ast.IndexListExpr:
		return calledGoIdentifier(expression.X)
	case *ast.ParenExpr:
		return calledGoIdentifier(expression.X)
	default:
		return nil
	}
}

func isCallableCodeSymbol(kind string) bool {
	return kind == "function" || kind == "method"
}

func codeRelationEndpointCovered(refs []ResolvedSourceRef, path string, startByte, endByte int) bool {
	for _, ref := range refs {
		if ref.Path == path && ref.StartByte <= startByte && ref.EndByte >= endByte {
			return true
		}
	}
	return false
}

func repositoryCodeRelationSourceRefs(input RepositoryExtractorInput, relation CodeRelationOutput) ([]ResolvedSourceRef, error) {
	return repositoryCodeRelationSourceRefsIndexed(input, relation, nil)
}

func repositoryCodeRelationSourceRefsIndexed(
	input RepositoryExtractorInput,
	relation CodeRelationOutput,
	index *repositoryGroundingIndex,
) ([]ResolvedSourceRef, error) {
	var filesByPath map[string]RepositoryExtractorFile
	if index == nil {
		filesByPath = make(map[string]RepositoryExtractorFile, len(input.Files))
		for _, file := range input.Files {
			filesByPath[file.FileSnapshot.Path] = file
		}
	} else {
		filesByPath = index.filesByPath
	}
	usageFile, ok := filesByPath[relation.Usage.Path]
	if !ok {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation usage references unknown path %q", relation.Usage.Path)
	}
	targetFile, ok := filesByPath[relation.Target.Path]
	if !ok {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation target references unknown path %q", relation.Target.Path)
	}
	usageRef, err := repositoryCodeEndpointSourceRefIndexed(
		input.RepositorySnapshot,
		usageFile,
		relation.Usage.StartByte,
		relation.Usage.EndByte,
		index,
	)
	if err != nil {
		return nil, err
	}
	targetRef, err := repositoryCodeEndpointSourceRefIndexed(
		input.RepositorySnapshot,
		targetFile,
		relation.Target.StartByte,
		relation.Target.EndByte,
		index,
	)
	if err != nil {
		return nil, err
	}
	refs := []ResolvedSourceRef{usageRef, targetRef}
	if relation.Caller != nil {
		callerFile, ok := filesByPath[relation.Caller.Path]
		if !ok {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "code relation caller references unknown path %q", relation.Caller.Path)
		}
		callerRef, err := repositoryCodeEndpointSourceRefIndexed(
			input.RepositorySnapshot,
			callerFile,
			relation.Caller.StartByte,
			relation.Caller.EndByte,
			index,
		)
		if err != nil {
			return nil, err
		}
		refs = append(refs, callerRef)
	}
	sortRepositorySourceRefs(refs)
	return uniqueRepositorySourceRefs(refs), nil
}

func uniqueRepositorySourceRefs(refs []ResolvedSourceRef) []ResolvedSourceRef {
	unique := refs[:0]
	for _, ref := range refs {
		if len(unique) == 0 || !sameRepositorySourceRef(unique[len(unique)-1], ref) {
			unique = append(unique, ref)
		}
	}
	return unique
}

func sameRepositorySourceRef(a, b ResolvedSourceRef) bool {
	return a.RepositorySnapshotID == b.RepositorySnapshotID &&
		a.FileSnapshotID == b.FileSnapshotID &&
		a.StartByte == b.StartByte &&
		a.EndByte == b.EndByte &&
		a.QuotedTextHash == b.QuotedTextHash
}

func repositoryCodeEndpointSourceRefIndexed(
	snapshot RepositorySnapshot,
	file RepositoryExtractorFile,
	startByte int,
	endByte int,
	index *repositoryGroundingIndex,
) (ResolvedSourceRef, error) {
	var (
		spans []SpanEntry
		err   error
	)
	if index == nil {
		view := ExtractionView{ID: file.FileSnapshot.ID, Rendered: file.Content, RenderedContentHash: file.FileSnapshot.BlobHash}
		spans, err = buildLineSpanCatalog(view, SpanCatalogCodeLineV1)
	} else {
		catalog, catalogErr := index.lineSpans(file)
		spans = catalog.ordered
		err = catalogErr
	}
	if err != nil {
		return ResolvedSourceRef{}, err
	}
	var span SpanEntry
	if index == nil {
		for _, candidate := range spans {
			if candidate.StartByte <= startByte && candidate.EndByte >= endByte {
				span = candidate
				break
			}
		}
	} else {
		candidateIndex := sort.Search(len(spans), func(position int) bool {
			return spans[position].StartByte > startByte
		}) - 1
		if candidateIndex >= 0 &&
			spans[candidateIndex].StartByte <= startByte &&
			spans[candidateIndex].EndByte >= endByte {
			span = spans[candidateIndex]
		}
	}
	if span.SpanID != "" {
		return ResolvedSourceRef{
			TargetKind:           "file_snapshot",
			RepositorySnapshotID: snapshot.ID,
			FileSnapshotID:       file.FileSnapshot.ID,
			RepoID:               snapshot.RepoID,
			CommitSHA:            snapshot.CommitSHA,
			Path:                 file.FileSnapshot.Path,
			SpanID:               span.SpanID,
			StartByte:            span.StartByte,
			EndByte:              span.EndByte,
			QuotedTextHash:       span.QuotedTextHash,
			QuotedText:           span.QuotedText,
		}, nil
	}
	return ResolvedSourceRef{}, newDomainError(ErrorUnknownSpan, "code relation span [%d,%d) in %s has no covering line span", startByte, endByte, file.FileSnapshot.Path)
}

func repositoryCodeRelationProposalFingerprint(statementText string, refs []ResolvedSourceRef, relation ResolvedCodeRelation) (string, error) {
	fingerprintVersion, err := codeRelationFingerprintVersion(relation)
	if err != nil {
		return "", err
	}
	type fingerprintSourceRef struct {
		RepositorySnapshotID string `json:"repository_snapshot_id"`
		FileSnapshotID       string `json:"file_snapshot_id"`
		StartByte            int    `json:"start_byte"`
		EndByte              int    `json:"end_byte"`
		QuotedTextHash       string `json:"quoted_text_hash"`
	}
	fingerprintRefs := make([]fingerprintSourceRef, 0, len(refs))
	for _, ref := range refs {
		fingerprintRefs = append(fingerprintRefs, fingerprintSourceRef{
			RepositorySnapshotID: ref.RepositorySnapshotID,
			FileSnapshotID:       ref.FileSnapshotID,
			StartByte:            ref.StartByte,
			EndByte:              ref.EndByte,
			QuotedTextHash:       ref.QuotedTextHash,
		})
	}
	return stableID("fp:", "repository_code_relation_proposal_fingerprint", struct {
		FingerprintVersion string                 `json:"fingerprint_version"`
		ProposalKind       string                 `json:"proposal_kind"`
		StatementText      string                 `json:"statement_text"`
		SourceRefs         []fingerprintSourceRef `json:"source_refs"`
		CodeRelation       ResolvedCodeRelation   `json:"code_relation"`
	}{
		FingerprintVersion: fingerprintVersion,
		ProposalKind:       ProposalKindStatement,
		StatementText:      statementText,
		SourceRefs:         fingerprintRefs,
		CodeRelation:       relation,
	})
}

func codeRelationFingerprintVersion(relation ResolvedCodeRelation) (string, error) {
	switch relation.SchemaVersion {
	case CodeRelationSchemaV1:
		return ProposalFingerprintCodeRelationV1, nil
	case CodeRelationSchemaV2:
		return ProposalFingerprintCodeRelationV2, nil
	case CodeRelationSchemaV3:
		return ProposalFingerprintCodeRelationV3, nil
	default:
		return "", newDomainError(ErrorInvalidExtractorOutput, "code relation fingerprint schema %q is not supported", relation.SchemaVersion)
	}
}

func codeDefinitionStatement(relation ResolvedCodeRelation) string {
	return "Go identifier " + relation.Usage.QuotedText + " in " + relation.Usage.Path + ":" + lineRange(relation.Usage.StartLine, relation.Usage.EndLine) +
		" resolves to " + relation.Target.SymbolKind + " " + relation.Target.QualifiedName + " declared in " + relation.Target.Path + ":" + lineRange(relation.Target.StartLine, relation.Target.EndLine) + "."
}

func codeCallStatement(relation ResolvedCodeRelation) string {
	if relation.Caller == nil {
		return ""
	}
	return "Go " + relation.Caller.SymbolKind + " " + relation.Caller.QualifiedName + " declared in " + relation.Caller.Path + ":" + lineRange(relation.Caller.StartLine, relation.Caller.EndLine) +
		" calls " + relation.Target.SymbolKind + " " + relation.Target.QualifiedName + " declared in " + relation.Target.Path + ":" + lineRange(relation.Target.StartLine, relation.Target.EndLine) +
		" at " + relation.Usage.Path + ":" + lineRange(relation.Usage.StartLine, relation.Usage.EndLine) + "."
}

func lineRange(startLine, endLine int) string {
	if startLine == endLine {
		return strconv.Itoa(startLine)
	}
	return strconv.Itoa(startLine) + "-" + strconv.Itoa(endLine)
}
