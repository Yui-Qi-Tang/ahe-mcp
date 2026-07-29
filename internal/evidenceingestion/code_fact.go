package evidenceingestion

import (
	"bytes"
	"fmt"
	"strings"
)

var supportedCodeSymbolKinds = map[string]struct{}{
	"const":    {},
	"function": {},
	"method":   {},
	"type":     {},
	"var":      {},
}

type codeFactSpan struct {
	startByte int
	endByte   int
}

func resolveCodeFact(
	ctx attemptContext,
	proposal ExtractorProposalOutput,
	sourceRefs []ResolvedSourceRef,
) (*ResolvedCodeFact, error) {
	if proposal.CodeFact == nil {
		return nil, nil
	}
	fact := normalizeCodeFactOutput(*proposal.CodeFact)
	if err := validateCodeFactContract(fact); err != nil {
		return nil, err
	}
	if ctx.SourceSnapshot.SourceSystem != SourceSystemCodeFile {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code fact requires source system %q", SourceSystemCodeFile)
	}
	if err := verifyCodeOrigin(ctx.SourceSnapshot, fact); err != nil {
		return nil, err
	}
	if fact.FileContentHash != ctx.SourceSnapshot.RawContentHash || fact.FileContentHash != ctx.ExtractionView.RenderedContentHash {
		return nil, newDomainError(
			ErrorQuotedHashMismatch,
			"code fact file hash %s does not match source/view hashes %s/%s",
			fact.FileContentHash,
			ctx.SourceSnapshot.RawContentHash,
			ctx.ExtractionView.RenderedContentHash,
		)
	}
	if fact.StartByte < 0 || fact.EndByte <= fact.StartByte || fact.EndByte > len(ctx.ExtractionView.Rendered) {
		return nil, newDomainError(
			ErrorSpanOutOfBounds,
			"code fact span [%d,%d) outside rendered bytes length %d",
			fact.StartByte,
			fact.EndByte,
			len(ctx.ExtractionView.Rendered),
		)
	}
	quoted := ctx.ExtractionView.Rendered[fact.StartByte:fact.EndByte]
	if got := contentHash(quoted); got != fact.QuotedTextHash {
		return nil, newDomainError(ErrorQuotedHashMismatch, "code fact quoted hash %s, want %s", fact.QuotedTextHash, got)
	}
	if !codeFactCoveredBySourceRef(fact, sourceRefs) {
		return nil, newDomainError(ErrorUnknownSpan, "code fact span [%d,%d) is not covered by its evidence refs", fact.StartByte, fact.EndByte)
	}
	expectedSymbolRef := codeSymbolRef(fact.RepoID, fact.CommitSHA, fact.Path, fact.QualifiedName)
	if fact.SymbolRef != expectedSymbolRef {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code fact symbol_ref %q, want %q", fact.SymbolRef, expectedSymbolRef)
	}
	startLine := codeLineForOffset(ctx.ExtractionView.Rendered, fact.StartByte)
	endLine := codeLineForOffset(ctx.ExtractionView.Rendered, fact.EndByte-1)
	expectedStatement := codeFactStatement(fact, startLine, endLine)
	if proposal.StatementText != expectedStatement {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "code fact statement %q, want %q", proposal.StatementText, expectedStatement)
	}
	return &ResolvedCodeFact{
		SchemaVersion:   fact.SchemaVersion,
		FactKind:        fact.FactKind,
		RepoID:          fact.RepoID,
		CommitSHA:       fact.CommitSHA,
		Path:            fact.Path,
		FileContentHash: fact.FileContentHash,
		SymbolRef:       fact.SymbolRef,
		SymbolKind:      fact.SymbolKind,
		QualifiedName:   fact.QualifiedName,
		StartByte:       fact.StartByte,
		EndByte:         fact.EndByte,
		StartLine:       startLine,
		EndLine:         endLine,
		QuotedTextHash:  fact.QuotedTextHash,
		QuotedText:      string(quoted),
	}, nil
}

func validateCodeFactContract(fact CodeFactOutput) error {
	switch fact.SchemaVersion {
	case CodeFactSchemaV1:
		if fact.FactKind != CodeFactKindDeclaration {
			return newDomainError(ErrorInvalidExtractorOutput, "code fact schema %q does not support kind %q", fact.SchemaVersion, fact.FactKind)
		}
		if _, ok := supportedCodeSymbolKinds[fact.SymbolKind]; !ok {
			return newDomainError(ErrorInvalidExtractorOutput, "code symbol kind %q is not supported", fact.SymbolKind)
		}
	case CodeFactSchemaV2:
		if fact.FactKind != CodeFactKindPackage || fact.SymbolKind != CodeFactKindPackage {
			return newDomainError(ErrorInvalidExtractorOutput, "code fact schema %q requires package fact and symbol kinds", fact.SchemaVersion)
		}
	default:
		return newDomainError(ErrorInvalidExtractorOutput, "code fact schema_version %q is not supported", fact.SchemaVersion)
	}
	return nil
}

func codeFactStatement(fact CodeFactOutput, startLine, endLine int) string {
	if fact.FactKind == CodeFactKindPackage {
		return codePackageStatement(fact.QualifiedName, fact.Path, startLine, endLine)
	}
	return codeDeclarationStatement(fact.SymbolKind, fact.QualifiedName, fact.Path, startLine, endLine)
}

func normalizeCodeFactOutput(fact CodeFactOutput) CodeFactOutput {
	fact.SchemaVersion = strings.TrimSpace(fact.SchemaVersion)
	fact.FactKind = strings.TrimSpace(fact.FactKind)
	fact.RepoID = strings.TrimSpace(fact.RepoID)
	fact.CommitSHA = strings.TrimSpace(fact.CommitSHA)
	fact.Path = strings.TrimSpace(fact.Path)
	fact.FileContentHash = strings.TrimSpace(fact.FileContentHash)
	fact.SymbolRef = strings.TrimSpace(fact.SymbolRef)
	fact.SymbolKind = strings.TrimSpace(fact.SymbolKind)
	fact.QualifiedName = strings.TrimSpace(fact.QualifiedName)
	fact.QuotedTextHash = strings.TrimSpace(fact.QuotedTextHash)
	return fact
}

func verifyCodeOrigin(snapshot SourceSnapshot, fact CodeFactOutput) error {
	if fact.RepoID == "" || fact.CommitSHA == "" || fact.Path == "" || fact.QualifiedName == "" {
		return newDomainError(ErrorInvalidExtractorOutput, "code fact repo_id, commit_sha, path, and qualified_name are required")
	}
	if snapshot.SourceVersion != fact.CommitSHA {
		return newDomainError(ErrorInvalidExtractorOutput, "code fact commit_sha %q does not match source version %q", fact.CommitSHA, snapshot.SourceVersion)
	}
	for key, got := range map[string]string{
		"repo_id":    fact.RepoID,
		"commit_sha": fact.CommitSHA,
		"path":       fact.Path,
	} {
		want := strings.TrimSpace(snapshot.OriginMetadata[key])
		if want == "" {
			return newDomainError(ErrorInvalidExtractorOutput, "code source origin metadata %q is required", key)
		}
		if got != want {
			return newDomainError(ErrorInvalidExtractorOutput, "code fact %s %q does not match source origin %q", key, got, want)
		}
	}
	return nil
}

func codeFactCoveredBySourceRef(fact CodeFactOutput, refs []ResolvedSourceRef) bool {
	for _, ref := range refs {
		if ref.StartByte <= fact.StartByte && ref.EndByte >= fact.EndByte {
			return true
		}
	}
	return false
}

func codeLineForOffset(source []byte, offset int) int {
	return bytes.Count(source[:offset], []byte{'\n'}) + 1
}

func codeSymbolRef(repoID, commitSHA, path, qualifiedName string) string {
	return strings.Join([]string{
		"symbol",
		codeSymbolRefPart(repoID),
		codeSymbolRefPart(commitSHA),
		codeSymbolRefPart(path),
		codeSymbolRefPart(qualifiedName),
	}, ":")
}

func codeSymbolRefPart(value string) string {
	replacer := strings.NewReplacer("%", "%25", ":", "%3A")
	return replacer.Replace(value)
}

func codeDeclarationStatement(symbolKind, qualifiedName, path string, startLine, endLine int) string {
	lineRange := fmt.Sprintf("%d", startLine)
	if startLine != endLine {
		lineRange = fmt.Sprintf("%d-%d", startLine, endLine)
	}
	return fmt.Sprintf("Go %s %s is declared in %s:%s.", symbolKind, qualifiedName, path, lineRange)
}

func codePackageStatement(packageName, path string, startLine, endLine int) string {
	lineRange := fmt.Sprintf("%d", startLine)
	if startLine != endLine {
		lineRange = fmt.Sprintf("%d-%d", startLine, endLine)
	}
	return fmt.Sprintf("Go package %s is declared in %s:%s.", packageName, path, lineRange)
}

func goCodeFactIndex(ctx attemptContext) (map[codeFactSpan]CodeFactOutput, error) {
	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    ctx.SourceSnapshot.OriginMetadata["repo_id"],
		CommitSHA: ctx.SourceSnapshot.OriginMetadata["commit_sha"],
		Path:      ctx.SourceSnapshot.OriginMetadata["path"],
		Source:    ctx.ExtractionView.Rendered,
	})
	if err != nil {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "verifying go declaration facts: %v", err)
	}
	index := make(map[codeFactSpan]CodeFactOutput, len(extraction.Declarations)+1)
	packageFact := extraction.PackageClause.CodeFact
	index[codeFactSpan{startByte: packageFact.StartByte, endByte: packageFact.EndByte}] = packageFact
	for _, declaration := range extraction.Declarations {
		fact := declaration.CodeFact
		index[codeFactSpan{startByte: fact.StartByte, endByte: fact.EndByte}] = fact
	}
	return index, nil
}

func verifyGoCodeFact(index map[codeFactSpan]CodeFactOutput, fact ResolvedCodeFact) error {
	want, ok := index[codeFactSpan{startByte: fact.StartByte, endByte: fact.EndByte}]
	if !ok {
		return newDomainError(ErrorInvalidExtractorOutput, "code fact span [%d,%d) is not a supported go fact identifier", fact.StartByte, fact.EndByte)
	}
	got := CodeFactOutput{
		SchemaVersion:   fact.SchemaVersion,
		FactKind:        fact.FactKind,
		RepoID:          fact.RepoID,
		CommitSHA:       fact.CommitSHA,
		Path:            fact.Path,
		FileContentHash: fact.FileContentHash,
		SymbolRef:       fact.SymbolRef,
		SymbolKind:      fact.SymbolKind,
		QualifiedName:   fact.QualifiedName,
		StartByte:       fact.StartByte,
		EndByte:         fact.EndByte,
		QuotedTextHash:  fact.QuotedTextHash,
	}
	if got != want {
		return newDomainError(ErrorInvalidExtractorOutput, "code fact does not match parsed go fact at span [%d,%d)", fact.StartByte, fact.EndByte)
	}
	return nil
}
