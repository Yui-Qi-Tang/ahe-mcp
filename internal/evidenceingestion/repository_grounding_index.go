package evidenceingestion

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
)

type repositoryGroundingIndex struct {
	snapshot          RepositorySnapshot
	filesByPath       map[string]RepositoryExtractorFile
	lineSpansByPath   map[string]repositoryGroundingLineSpans
	fileBytesByPath   map[string]repositoryGroundingFileBytes
	codeFactsByPath   map[string]map[codeFactSpan]CodeFactOutput
	goSyntaxByPath    map[string]repositoryGroundingGoSyntax
	goSyntaxAttempted map[string]bool
}

type repositoryGroundingLineSpans struct {
	ordered []SpanEntry
	byID    map[string]SpanEntry
}

type repositoryGroundingFileBytes struct {
	contentHash string
	lineStarts  []int
}

type repositoryGroundingGoSyntax struct {
	valid       bool
	identifiers map[codeFactSpan]struct{}
	callSites   map[repositoryGroundingCallSite]struct{}
}

type repositoryGroundingCallSite struct {
	callerStart int
	callerEnd   int
	usageStart  int
	usageEnd    int
}

func newRepositoryGroundingIndex(
	input RepositoryExtractorInput,
	filesByPath map[string]RepositoryExtractorFile,
) *repositoryGroundingIndex {
	if filesByPath == nil {
		filesByPath = make(map[string]RepositoryExtractorFile, len(input.Files))
		for _, file := range input.Files {
			filesByPath[file.FileSnapshot.Path] = file
		}
	}
	return &repositoryGroundingIndex{
		snapshot:          input.RepositorySnapshot,
		filesByPath:       filesByPath,
		lineSpansByPath:   make(map[string]repositoryGroundingLineSpans),
		fileBytesByPath:   make(map[string]repositoryGroundingFileBytes),
		codeFactsByPath:   make(map[string]map[codeFactSpan]CodeFactOutput),
		goSyntaxByPath:    make(map[string]repositoryGroundingGoSyntax),
		goSyntaxAttempted: make(map[string]bool),
	}
}

func (i *repositoryGroundingIndex) lineSpans(
	file RepositoryExtractorFile,
) (repositoryGroundingLineSpans, error) {
	path := file.FileSnapshot.Path
	if spans, ok := i.lineSpansByPath[path]; ok {
		return spans, nil
	}
	view := ExtractionView{
		ID:                  file.FileSnapshot.ID,
		Rendered:            file.Content,
		RenderedContentHash: file.FileSnapshot.BlobHash,
	}
	ordered, err := buildLineSpanCatalog(view, SpanCatalogCodeLineV1)
	if err != nil {
		return repositoryGroundingLineSpans{}, err
	}
	byID := make(map[string]SpanEntry, len(ordered))
	for _, span := range ordered {
		byID[span.SpanID] = span
	}
	spans := repositoryGroundingLineSpans{ordered: ordered, byID: byID}
	i.lineSpansByPath[path] = spans
	return spans, nil
}

func (i *repositoryGroundingIndex) fileBytes(
	file RepositoryExtractorFile,
) repositoryGroundingFileBytes {
	path := file.FileSnapshot.Path
	if indexed, ok := i.fileBytesByPath[path]; ok {
		return indexed
	}
	lineStarts := []int{0}
	for offset, value := range file.Content {
		if value == '\n' {
			lineStarts = append(lineStarts, offset+1)
		}
	}
	indexed := repositoryGroundingFileBytes{
		contentHash: contentHash(file.Content),
		lineStarts:  lineStarts,
	}
	i.fileBytesByPath[path] = indexed
	return indexed
}

func (i *repositoryGroundingIndex) lineForOffset(
	file RepositoryExtractorFile,
	offset int,
) int {
	lineStarts := i.fileBytes(file).lineStarts
	return sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	})
}

func (i *repositoryGroundingIndex) codeFacts(
	file RepositoryExtractorFile,
) (map[codeFactSpan]CodeFactOutput, error) {
	path := file.FileSnapshot.Path
	if facts, ok := i.codeFactsByPath[path]; ok {
		return facts, nil
	}
	facts, err := goCodeFactIndex(repositoryFileAttemptContext(i.snapshot, file))
	if err != nil {
		return nil, err
	}
	i.codeFactsByPath[path] = facts
	return facts, nil
}

func (i *repositoryGroundingIndex) goSyntax(
	file RepositoryExtractorFile,
) repositoryGroundingGoSyntax {
	path := file.FileSnapshot.Path
	if i.goSyntaxAttempted[path] {
		return i.goSyntaxByPath[path]
	}
	i.goSyntaxAttempted[path] = true

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, file.Content, parser.SkipObjectResolution)
	if err != nil {
		i.goSyntaxByPath[path] = repositoryGroundingGoSyntax{}
		return repositoryGroundingGoSyntax{}
	}
	syntax := repositoryGroundingGoSyntax{
		valid:       true,
		identifiers: make(map[codeFactSpan]struct{}),
		callSites:   make(map[repositoryGroundingCallSite]struct{}),
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		syntax.identifiers[codeFactSpan{
			startByte: fset.Position(identifier.Pos()).Offset,
			endByte:   fset.Position(identifier.End()).Offset,
		}] = struct{}{}
		return true
	})
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		callerStart := fset.Position(function.Name.Pos()).Offset
		callerEnd := fset.Position(function.Name.End()).Offset
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier := calledGoIdentifier(call.Fun)
			if identifier == nil {
				return true
			}
			syntax.callSites[repositoryGroundingCallSite{
				callerStart: callerStart,
				callerEnd:   callerEnd,
				usageStart:  fset.Position(identifier.Pos()).Offset,
				usageEnd:    fset.Position(identifier.End()).Offset,
			}] = struct{}{}
			return true
		})
	}
	i.goSyntaxByPath[path] = syntax
	return syntax
}

func (i *repositoryGroundingIndex) identifierAtSpan(
	file RepositoryExtractorFile,
	startByte int,
	endByte int,
) bool {
	syntax := i.goSyntax(file)
	if !syntax.valid {
		return false
	}
	_, ok := syntax.identifiers[codeFactSpan{startByte: startByte, endByte: endByte}]
	return ok
}

func (i *repositoryGroundingIndex) callSiteWithinCaller(
	file RepositoryExtractorFile,
	caller ResolvedCodeDeclarationEndpoint,
	usage ResolvedCodeUsageSite,
) bool {
	syntax := i.goSyntax(file)
	if !syntax.valid {
		return false
	}
	_, ok := syntax.callSites[repositoryGroundingCallSite{
		callerStart: caller.StartByte,
		callerEnd:   caller.EndByte,
		usageStart:  usage.StartByte,
		usageEnd:    usage.EndByte,
	}]
	return ok
}
