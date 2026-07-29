package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// ExtractorGoParserCodeFact identifies the deterministic Go source declaration extractor.
	ExtractorGoParserCodeFact = "go-parser-code-fact"
	// ExtractorGoParserCodeFactVersion is the Go declaration extractor contract version.
	ExtractorGoParserCodeFactVersion = "v1"
)

// GoParserExtractorConfig identifies one Go source file under a repository revision.
type GoParserExtractorConfig struct {
	RepoID    string
	CommitSHA string
	Path      string
}

// GoParserExtractorRunner extracts Go declaration facts from one grounded source view.
type GoParserExtractorRunner struct {
	repoID    string
	commitSHA string
	path      string
}

// GoParserFileInput is the deterministic parser input for a single Go file.
type GoParserFileInput struct {
	RepoID    string
	CommitSHA string
	Path      string
	Source    []byte
	Spans     []ExtractorInputSpan
}

// GoParserFileExtraction is the parser output plus the transient proposal envelope.
type GoParserFileExtraction struct {
	ExtractorDefinition ExtractorDefinitionInput
	Output              FrozenExtractorOutput
	PackageClause       GoParserPackageClause
	Declarations        []GoParserDeclaration
}

// GoParserPackageClause is diagnostic sidecar data for one file-bound package clause.
// EvidenceRefs is empty when a caller-supplied span catalog omits the package line.
type GoParserPackageClause struct {
	ProposalLocalID string
	Name            string
	Path            string
	StartLine       int
	EndLine         int
	StartByte       int
	EndByte         int
	EvidenceRefs    []string
	StatementText   string
	CodeFact        CodeFactOutput
}

// GoParserDeclaration is diagnostic sidecar data for one extracted Go declaration.
type GoParserDeclaration struct {
	ProposalLocalID string
	SymbolRef       string
	Kind            string
	Name            string
	Package         string
	Path            string
	StartLine       int
	EndLine         int
	StartByte       int
	EndByte         int
	EvidenceRefs    []string
	StatementText   string
	CodeFact        CodeFactOutput
}

// NewGoParserExtractorRunner builds a local deterministic Go declaration extractor.
func NewGoParserExtractorRunner(config GoParserExtractorConfig) (*GoParserExtractorRunner, error) {
	repoID := strings.TrimSpace(config.RepoID)
	if repoID == "" {
		return nil, newDomainError(ErrorInvalidInput, "go parser repo_id is required")
	}
	commitSHA := strings.TrimSpace(config.CommitSHA)
	if commitSHA == "" {
		return nil, newDomainError(ErrorInvalidInput, "go parser commit_sha is required")
	}
	path := strings.TrimSpace(config.Path)
	if path == "" {
		return nil, newDomainError(ErrorInvalidInput, "go parser path is required")
	}
	return &GoParserExtractorRunner{
		repoID:    repoID,
		commitSHA: commitSHA,
		path:      path,
	}, nil
}

// ExtractorDefinition returns the stable provenance identity for this extractor.
func (r *GoParserExtractorRunner) ExtractorDefinition() ExtractorDefinitionInput {
	if r == nil {
		return ExtractorDefinitionInput{}
	}
	return goParserExtractorDefinition()
}

// Run parses the grounded Go source and returns strict transient extractor output JSON.
func (r *GoParserExtractorRunner) Run(ctx context.Context, input ExtractorInput) ([]byte, error) {
	if r == nil {
		return nil, newDomainError(ErrorInvalidInput, "go parser extractor runner is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.SourceSystem != SourceSystemCodeFile {
		return nil, newDomainError(ErrorInvalidInput, "go parser extractor requires source system %q", SourceSystemCodeFile)
	}
	if input.SpanCatalogVersion != SpanCatalogCodeLineV1 {
		return nil, newDomainError(ErrorInvalidInput, "go parser extractor requires span catalog %q", SpanCatalogCodeLineV1)
	}
	if got := contentHash([]byte(input.RenderedText)); got != input.RawContentHash || got != input.RenderedContentHash {
		return nil, newDomainError(ErrorQuotedHashMismatch, "go parser input hash %s does not match source/view hashes %s/%s", got, input.RawContentHash, input.RenderedContentHash)
	}
	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    r.repoID,
		CommitSHA: r.commitSHA,
		Path:      r.path,
		Source:    []byte(input.RenderedText),
		Spans:     input.Spans,
	})
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(extraction.Output)
	if err != nil {
		return nil, fmt.Errorf("encoding go parser extractor output: %w", err)
	}
	return data, nil
}

// ExtractGoParserFile extracts declaration-grounded statement proposals from one Go file.
func ExtractGoParserFile(input GoParserFileInput) (GoParserFileExtraction, error) {
	repoID := strings.TrimSpace(input.RepoID)
	if repoID == "" {
		return GoParserFileExtraction{}, newDomainError(ErrorInvalidInput, "go parser repo_id is required")
	}
	commitSHA := strings.TrimSpace(input.CommitSHA)
	if commitSHA == "" {
		return GoParserFileExtraction{}, newDomainError(ErrorInvalidInput, "go parser commit_sha is required")
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return GoParserFileExtraction{}, newDomainError(ErrorInvalidInput, "go parser path is required")
	}
	if !utf8.Valid(input.Source) {
		return GoParserFileExtraction{}, newDomainError(ErrorInvalidUTF8, "go source is not valid UTF-8")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, input.Source, parser.ParseComments)
	if err != nil {
		return GoParserFileExtraction{}, newDomainError(ErrorInvalidInput, "parsing go source %s: %v", path, err)
	}
	lineSpans := goParserLineSpans(input.Source, input.Spans)
	packageClause, err := buildGoParserPackageClause(fset, file.Name, repoID, commitSHA, path, input.Source, lineSpans)
	if err != nil {
		return GoParserFileExtraction{}, err
	}
	declarations, err := collectGoParserDeclarations(fset, file, repoID, commitSHA, path, input.Source, lineSpans)
	if err != nil {
		return GoParserFileExtraction{}, err
	}
	proposals := make([]ExtractorProposalOutput, 0, len(declarations))
	for _, declaration := range declarations {
		codeFact := declaration.CodeFact
		proposals = append(proposals, ExtractorProposalOutput{
			ProposalLocalID: declaration.ProposalLocalID,
			StatementText:   declaration.StatementText,
			EvidenceRefs:    append([]string(nil), declaration.EvidenceRefs...),
			CodeFact:        &codeFact,
		})
	}
	return GoParserFileExtraction{
		ExtractorDefinition: goParserExtractorDefinition(),
		Output:              FrozenExtractorOutput{Proposals: proposals},
		PackageClause:       packageClause,
		Declarations:        declarations,
	}, nil
}

func goParserExtractorDefinition() ExtractorDefinitionInput {
	return ExtractorDefinitionInput{
		Name:    ExtractorGoParserCodeFact,
		Version: ExtractorGoParserCodeFactVersion,
		Config: map[string]string{
			"backend":        "go/parser",
			"output_schema":  CodeFactSchemaV1,
			"proposal_kind":  ProposalKindStatement,
			"source_kind":    "go_file",
			"span_contract":  SpanCatalogCodeLineV1,
			"symbol_schema":  "repo-commit-path-package-symbol-v1",
			"topology_scope": "declarations_only",
		},
	}
}

type goParserLineSpan struct {
	SpanID      string
	DisplayLine int
}

func goParserLineSpans(source []byte, spans []ExtractorInputSpan) map[int]goParserLineSpan {
	byLine := make(map[int]goParserLineSpan, len(spans))
	for _, span := range spans {
		if span.DisplayLine <= 0 || span.SpanID == "" {
			continue
		}
		byLine[span.DisplayLine] = goParserLineSpan{
			SpanID:      span.SpanID,
			DisplayLine: span.DisplayLine,
		}
	}
	if len(byLine) > 0 {
		return byLine
	}
	return goParserManualLineSpans(source)
}

func goParserManualLineSpans(source []byte) map[int]goParserLineSpan {
	byLine := map[int]goParserLineSpan{}
	lineStart := 0
	displayLine := 1
	spanNumber := 1
	for lineStart <= len(source) {
		newline := -1
		for idx := lineStart; idx < len(source); idx++ {
			if source[idx] == '\n' {
				newline = idx
				break
			}
		}
		lineEnd := len(source)
		nextLineStart := len(source) + 1
		if newline >= 0 {
			lineEnd = newline
			nextLineStart = lineEnd + 1
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && source[contentEnd-1] == '\r' {
			contentEnd--
		}
		if contentEnd > lineStart {
			byLine[displayLine] = goParserLineSpan{
				SpanID:      fmt.Sprintf("span:S%d", spanNumber),
				DisplayLine: displayLine,
			}
			spanNumber++
		}
		if newline < 0 {
			break
		}
		lineStart = nextLineStart
		displayLine++
	}
	return byLine
}

func collectGoParserDeclarations(
	fset *token.FileSet,
	file *ast.File,
	repoID string,
	commitSHA string,
	path string,
	source []byte,
	lineSpans map[int]goParserLineSpan,
) ([]GoParserDeclaration, error) {
	var declarations []GoParserDeclaration
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			kind := goParserGenDeclKind(d.Tok)
			if kind == "" {
				continue
			}
			for _, spec := range d.Specs {
				for _, ident := range goParserSpecIdentifiers(spec) {
					declaration, err := buildGoParserDeclaration(fset, repoID, commitSHA, path, file.Name.Name, kind, ident.Name, ident.Pos(), ident.End(), source, lineSpans)
					if err != nil {
						return nil, err
					}
					declarations = append(declarations, declaration)
				}
			}
		case *ast.FuncDecl:
			kind := "function"
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				name = goParserReceiverName(d.Recv.List[0].Type) + "." + name
			}
			declaration, err := buildGoParserDeclaration(fset, repoID, commitSHA, path, file.Name.Name, kind, name, d.Name.Pos(), d.Name.End(), source, lineSpans)
			if err != nil {
				return nil, err
			}
			declarations = append(declarations, declaration)
		}
	}
	slices.SortFunc(declarations, func(a, b GoParserDeclaration) int {
		switch {
		case a.StartLine < b.StartLine:
			return -1
		case a.StartLine > b.StartLine:
			return 1
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})
	return declarations, nil
}

func buildGoParserPackageClause(
	fset *token.FileSet,
	ident *ast.Ident,
	repoID string,
	commitSHA string,
	path string,
	source []byte,
	lineSpans map[int]goParserLineSpan,
) (GoParserPackageClause, error) {
	if ident == nil || ident.Name == "" {
		return GoParserPackageClause{}, newDomainError(ErrorInvalidInput, "go source %s has no package clause", path)
	}
	startPosition := fset.Position(ident.Pos())
	endPosition := fset.Position(ident.End())
	startLine := startPosition.Line
	endLine := endPosition.Line
	startByte := startPosition.Offset
	endByte := endPosition.Offset
	if startByte < 0 || endByte <= startByte || endByte > len(source) {
		return GoParserPackageClause{}, newDomainError(ErrorSpanOutOfBounds, "go package %s span [%d,%d) outside source bytes length %d", ident.Name, startByte, endByte, len(source))
	}
	evidenceRefs := goParserEvidenceRefs(lineSpans, startLine, endLine)
	statement := codePackageStatement(ident.Name, path, startLine, endLine)
	codeFact := CodeFactOutput{
		SchemaVersion:   CodeFactSchemaV2,
		FactKind:        CodeFactKindPackage,
		RepoID:          repoID,
		CommitSHA:       commitSHA,
		Path:            path,
		FileContentHash: contentHash(source),
		SymbolRef:       codeSymbolRef(repoID, commitSHA, path, ident.Name),
		SymbolKind:      CodeFactKindPackage,
		QualifiedName:   ident.Name,
		StartByte:       startByte,
		EndByte:         endByte,
		QuotedTextHash:  contentHash(source[startByte:endByte]),
	}
	return GoParserPackageClause{
		ProposalLocalID: fmt.Sprintf("go-package-%s-L%d", goParserSlug(ident.Name), startLine),
		Name:            ident.Name,
		Path:            path,
		StartLine:       startLine,
		EndLine:         endLine,
		StartByte:       startByte,
		EndByte:         endByte,
		EvidenceRefs:    evidenceRefs,
		StatementText:   statement,
		CodeFact:        codeFact,
	}, nil
}

func buildGoParserDeclaration(
	fset *token.FileSet,
	repoID string,
	commitSHA string,
	path string,
	pkg string,
	kind string,
	name string,
	start token.Pos,
	end token.Pos,
	source []byte,
	lineSpans map[int]goParserLineSpan,
) (GoParserDeclaration, error) {
	startPosition := fset.Position(start)
	endPosition := fset.Position(end)
	startLine := startPosition.Line
	endLine := endPosition.Line
	startByte := startPosition.Offset
	endByte := endPosition.Offset
	if startByte < 0 || endByte <= startByte || endByte > len(source) {
		return GoParserDeclaration{}, newDomainError(ErrorSpanOutOfBounds, "go declaration %s.%s span [%d,%d) outside source bytes length %d", pkg, name, startByte, endByte, len(source))
	}
	evidenceRefs := goParserEvidenceRefs(lineSpans, startLine, endLine)
	if len(evidenceRefs) == 0 {
		return GoParserDeclaration{}, newDomainError(ErrorUnknownSpan, "go declaration %s.%s has no matching line spans", pkg, name)
	}
	qualifiedName := pkg + "." + name
	statement := codeDeclarationStatement(kind, qualifiedName, path, startLine, endLine)
	symbolRef := codeSymbolRef(repoID, commitSHA, path, qualifiedName)
	quotedTextHash := contentHash(source[startByte:endByte])
	codeFact := CodeFactOutput{
		SchemaVersion:   CodeFactSchemaV1,
		FactKind:        CodeFactKindDeclaration,
		RepoID:          repoID,
		CommitSHA:       commitSHA,
		Path:            path,
		FileContentHash: contentHash(source),
		SymbolRef:       symbolRef,
		SymbolKind:      kind,
		QualifiedName:   qualifiedName,
		StartByte:       startByte,
		EndByte:         endByte,
		QuotedTextHash:  quotedTextHash,
	}
	return GoParserDeclaration{
		ProposalLocalID: goParserProposalLocalID(kind, qualifiedName, startLine),
		SymbolRef:       symbolRef,
		Kind:            kind,
		Name:            name,
		Package:         pkg,
		Path:            path,
		StartLine:       startLine,
		EndLine:         endLine,
		StartByte:       startByte,
		EndByte:         endByte,
		EvidenceRefs:    evidenceRefs,
		StatementText:   statement,
		CodeFact:        codeFact,
	}, nil
}

func goParserGenDeclKind(tok token.Token) string {
	switch tok {
	case token.TYPE:
		return "type"
	case token.CONST:
		return "const"
	case token.VAR:
		return "var"
	default:
		return ""
	}
}

func goParserSpecIdentifiers(spec ast.Spec) []*ast.Ident {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return []*ast.Ident{s.Name}
	case *ast.ValueSpec:
		return append([]*ast.Ident(nil), s.Names...)
	default:
		return nil
	}
}

func goParserEvidenceRefs(lineSpans map[int]goParserLineSpan, startLine int, endLine int) []string {
	seen := map[string]bool{}
	var refs []string
	for line := startLine; line <= endLine; line++ {
		span, ok := lineSpans[line]
		if !ok || seen[span.SpanID] {
			continue
		}
		refs = append(refs, span.SpanID)
		seen[span.SpanID] = true
	}
	return refs
}

func goParserProposalLocalID(kind string, qualifiedName string, startLine int) string {
	return fmt.Sprintf("go-decl-%s-L%d", goParserSlug(kind+"-"+qualifiedName), startLine)
}

func goParserSlug(value string) string {
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			out.WriteRune(r)
			lastDash = false
		case r == '_' || r == '-' || r == '.':
			out.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

func goParserReceiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return goParserReceiverName(t.X)
	case *ast.IndexExpr:
		return goParserReceiverName(t.X)
	case *ast.IndexListExpr:
		return goParserReceiverName(t.X)
	default:
		return "receiver"
	}
}
