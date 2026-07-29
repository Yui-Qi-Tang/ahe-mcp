package evidenceingestion

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

type groundedEvidenceRepositoryASTMatch struct {
	node ast.Node
	kind string
}

type groundedEvidenceRepositoryRelationAnchor struct {
	role            GroundedEvidenceRepositoryContextRole
	path            string
	fileContentHash string
	symbolRef       string
	symbolKind      string
	qualifiedName   string
	startByte       int
	endByte         int
	quotedTextHash  string
	quotedText      string
}

type groundedEvidenceRepositoryUsageASTMatch struct {
	node ast.Node
	kind string
	size int
}

type groundedEvidenceRepositoryParsedFile struct {
	fset *token.FileSet
	file *ast.File
	data RepositoryExtractorFile
}

type groundedEvidenceRepositoryContextCompiler struct {
	parsedFiles map[string]groundedEvidenceRepositoryParsedFile
}

func newGroundedEvidenceRepositoryContextCompiler() *groundedEvidenceRepositoryContextCompiler {
	return &groundedEvidenceRepositoryContextCompiler{
		parsedFiles: make(map[string]groundedEvidenceRepositoryParsedFile),
	}
}

func compileGroundedEvidenceRepositoryContext(
	record ProposalQueryResult,
	filesByPath map[string]RepositoryExtractorFile,
) (GroundedEvidenceRepositoryContext, error) {
	return (&groundedEvidenceRepositoryContextCompiler{}).compile(
		record,
		filesByPath,
	)
}

func (c *groundedEvidenceRepositoryContextCompiler) compile(
	record ProposalQueryResult,
	filesByPath map[string]RepositoryExtractorFile,
) (GroundedEvidenceRepositoryContext, error) {
	if record.CodeFact != nil {
		return c.compileDeclaration(record, filesByPath)
	}
	return c.compileRelation(record, filesByPath)
}

func (c *groundedEvidenceRepositoryContextCompiler) compileDeclaration(
	record ProposalQueryResult,
	filesByPath map[string]RepositoryExtractorFile,
) (GroundedEvidenceRepositoryContext, error) {
	result := newGroundedEvidenceRepositoryContext(record)
	if record.CodeFact == nil || record.CodeRelation != nil {
		return GroundedEvidenceRepositoryContext{}, fmt.Errorf(
			"declaration context requires exactly one code fact",
		)
	}
	file, ok := filesByPath[record.CodeFact.Path]
	if !ok {
		return GroundedEvidenceRepositoryContext{}, fmt.Errorf(
			"declaration path %s has no repository file",
			record.CodeFact.Path,
		)
	}
	if err := validateGroundedEvidenceRepositoryDeclaration(
		record,
		file,
		filesByPath,
	); err != nil {
		return GroundedEvidenceRepositoryContext{}, err
	}
	parsed, err := c.parseFile(file)
	if err != nil {
		return GroundedEvidenceRepositoryContext{}, err
	}
	match, err := findGroundedEvidenceRepositoryDeclaration(
		parsed.fset,
		parsed.file,
		*record.CodeFact,
	)
	if err != nil {
		return GroundedEvidenceRepositoryContext{}, err
	}
	container, err := buildGroundedEvidenceRepositoryContextSlice(
		parsed.fset,
		file.Content,
		file.FileSnapshot.Path,
		"atomic_container",
		match.kind,
		match.node,
	)
	if err != nil {
		return GroundedEvidenceRepositoryContext{}, err
	}
	if container.StartByte > record.CodeFact.StartByte ||
		container.EndByte < record.CodeFact.EndByte {
		return GroundedEvidenceRepositoryContext{}, fmt.Errorf(
			"declaration container does not cover code fact",
		)
	}

	result.ReferencedBytes = container.EndByte - container.StartByte
	var packageClause *GroundedEvidenceRepositoryContextSlice
	if match.kind != "package" {
		value, err := buildGroundedEvidenceRepositoryContextSlice(
			parsed.fset,
			file.Content,
			file.FileSnapshot.Path,
			"package_clause",
			"package",
			&groundedEvidenceRepositoryPackageClauseNode{file: parsed.file},
		)
		if err != nil {
			return GroundedEvidenceRepositoryContext{}, err
		}
		packageClause = &value
		result.ReferencedBytes += value.EndByte - value.StartByte
	}
	if result.ReferencedBytes >
		GroundedEvidenceRepositoryDeclarationMaxBytesV1 {
		result.Status = GroundedEvidenceRepositoryContextStatusOverBudget
		result.LimitReason = "declaration_context_bytes"
		return result, nil
	}
	result.Status = GroundedEvidenceRepositoryContextStatusAvailable
	result.Declaration = &GroundedEvidenceRepositoryDeclarationContext{
		ContainerKind:   match.kind,
		AtomicContainer: container,
		PackageClause:   packageClause,
	}
	return result, nil
}

func validateGroundedEvidenceRepositoryDeclaration(
	record ProposalQueryResult,
	file RepositoryExtractorFile,
	filesByPath map[string]RepositoryExtractorFile,
) error {
	snapshot := record.RepositorySnapshot
	fact := record.CodeFact
	if record.SourceBindingKind != ProposalSourceBindingRepositorySnapshot ||
		snapshot == nil ||
		fact.RepoID != snapshot.RepoID ||
		fact.CommitSHA != snapshot.CommitSHA ||
		fact.Path != file.FileSnapshot.Path ||
		fact.FileContentHash != file.FileSnapshot.BlobHash ||
		fact.StartByte < 0 ||
		fact.EndByte <= fact.StartByte ||
		fact.EndByte > len(file.Content) {
		return fmt.Errorf("declaration differs from repository authority")
	}
	if err := validateRepositoryExtractorFile(*snapshot, file); err != nil {
		return err
	}
	if fact.QuotedText != string(file.Content[fact.StartByte:fact.EndByte]) ||
		fact.QuotedTextHash != contentHash(
			file.Content[fact.StartByte:fact.EndByte],
		) {
		return fmt.Errorf("declaration bytes differ from repository authority")
	}
	anchorCovered := false
	for _, ref := range record.SourceRefs {
		refFile, ok := filesByPath[ref.Path]
		if !ok || ref.Path != fact.Path {
			return fmt.Errorf("declaration source ref has incompatible path")
		}
		if err := validateGroundedEvidenceRepositorySourceRef(
			*snapshot,
			refFile,
			ref,
		); err != nil {
			return err
		}
		if ref.StartByte <= fact.StartByte && ref.EndByte >= fact.EndByte {
			anchorCovered = true
		}
	}
	if !anchorCovered {
		return fmt.Errorf("declaration source refs do not cover code fact")
	}
	return nil
}

func (c *groundedEvidenceRepositoryContextCompiler) compileRelation(
	record ProposalQueryResult,
	filesByPath map[string]RepositoryExtractorFile,
) (GroundedEvidenceRepositoryContext, error) {
	result := newGroundedEvidenceRepositoryContext(record)
	if record.CodeRelation == nil || record.CodeFact != nil {
		return GroundedEvidenceRepositoryContext{}, fmt.Errorf(
			"relation context requires exactly one code relation",
		)
	}
	if err := validateGroundedEvidenceRepositoryRelation(
		record,
		filesByPath,
	); err != nil {
		return GroundedEvidenceRepositoryContext{}, err
	}
	relation := record.CodeRelation
	anchors := []groundedEvidenceRepositoryRelationAnchor{
		groundedEvidenceRepositoryUsageAnchor(relation.Usage),
	}
	if relation.Caller != nil {
		anchors = append(
			anchors,
			groundedEvidenceRepositoryDeclarationAnchor(
				GroundedEvidenceRepositoryContextCaller,
				*relation.Caller,
			),
		)
	}
	anchors = append(
		anchors,
		groundedEvidenceRepositoryDeclarationAnchor(
			GroundedEvidenceRepositoryContextTarget,
			relation.Target,
		),
	)

	parsedFiles := make(map[string]groundedEvidenceRepositoryParsedFile)
	endpoints := make(
		[]GroundedEvidenceRepositoryRelationEndpoint,
		0,
		len(anchors),
	)
	for _, anchor := range anchors {
		parsed, ok := parsedFiles[anchor.path]
		if !ok {
			file := filesByPath[anchor.path]
			var err error
			parsed, err = c.parseFile(file)
			if err != nil {
				return GroundedEvidenceRepositoryContext{}, err
			}
			parsedFiles[anchor.path] = parsed
		}
		endpoint, err := compileGroundedEvidenceRepositoryRelationEndpoint(
			record,
			anchor,
			parsed,
		)
		if err != nil {
			return GroundedEvidenceRepositoryContext{}, err
		}
		result.ReferencedBytes += endpoint.ReferencedBytes
		if endpoint.ReferencedBytes >
			GroundedEvidenceRepositoryRelationEndpointMaxBytesV1 ||
			result.ReferencedBytes >
				GroundedEvidenceRepositoryRelationMaxBytesV1 {
			result.Status = GroundedEvidenceRepositoryContextStatusOverBudget
			result.LimitReason = "complete_relation_context_budget"
			return result, nil
		}
		endpoints = append(endpoints, endpoint)
	}
	result.Status = GroundedEvidenceRepositoryContextStatusAvailable
	result.Relation = &GroundedEvidenceRepositoryRelationContext{
		Endpoints: endpoints,
	}
	return result, nil
}

func validateGroundedEvidenceRepositoryRelation(
	record ProposalQueryResult,
	filesByPath map[string]RepositoryExtractorFile,
) error {
	snapshot := record.RepositorySnapshot
	relation := record.CodeRelation
	if record.SourceBindingKind != ProposalSourceBindingRepositorySnapshot ||
		snapshot == nil ||
		relation.RepoID != snapshot.RepoID ||
		relation.CommitSHA != snapshot.CommitSHA {
		return fmt.Errorf("relation differs from repository authority")
	}
	for path, file := range filesByPath {
		if path != file.FileSnapshot.Path {
			return fmt.Errorf("relation file map path differs")
		}
		if err := validateRepositoryExtractorFile(*snapshot, file); err != nil {
			return err
		}
	}
	for _, ref := range record.SourceRefs {
		file, ok := filesByPath[ref.Path]
		if !ok {
			return fmt.Errorf("relation source ref path has no file")
		}
		if err := validateGroundedEvidenceRepositorySourceRef(
			*snapshot,
			file,
			ref,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateGroundedEvidenceRepositorySourceRef(
	snapshot RepositorySnapshot,
	file RepositoryExtractorFile,
	ref ResolvedSourceRef,
) error {
	meta := file.FileSnapshot
	if ref.TargetKind != "file_snapshot" ||
		ref.RepositorySnapshotID != snapshot.ID ||
		ref.FileSnapshotID != meta.ID ||
		ref.RepoID != snapshot.RepoID ||
		ref.CommitSHA != snapshot.CommitSHA ||
		ref.Path != meta.Path ||
		ref.StartByte < 0 ||
		ref.EndByte <= ref.StartByte ||
		ref.EndByte > len(file.Content) ||
		ref.QuotedText != string(file.Content[ref.StartByte:ref.EndByte]) ||
		ref.QuotedTextHash != contentHash(
			file.Content[ref.StartByte:ref.EndByte],
		) {
		return fmt.Errorf("repository context source ref differs from authority")
	}
	return nil
}

func compileGroundedEvidenceRepositoryRelationEndpoint(
	record ProposalQueryResult,
	anchor groundedEvidenceRepositoryRelationAnchor,
	parsed groundedEvidenceRepositoryParsedFile,
) (GroundedEvidenceRepositoryRelationEndpoint, error) {
	if err := validateGroundedEvidenceRepositoryRelationAnchor(
		record,
		anchor,
		parsed.data,
	); err != nil {
		return GroundedEvidenceRepositoryRelationEndpoint{}, err
	}
	var containerNode ast.Node
	var containerKind string
	if anchor.role == GroundedEvidenceRepositoryContextUsage {
		match, err := findGroundedEvidenceRepositoryUsage(
			parsed.fset,
			parsed.file,
			anchor,
		)
		if err != nil {
			return GroundedEvidenceRepositoryRelationEndpoint{}, err
		}
		containerNode = match.node
		containerKind = match.kind
	} else {
		match, err := findGroundedEvidenceRepositoryDeclaration(
			parsed.fset,
			parsed.file,
			groundedEvidenceRepositoryRelationAnchorFact(anchor),
		)
		if err != nil {
			return GroundedEvidenceRepositoryRelationEndpoint{}, err
		}
		containerNode = match.node
		containerKind = match.kind
	}
	anchorSlice, err := buildGroundedEvidenceRepositoryContextSlice(
		parsed.fset,
		parsed.data.Content,
		anchor.path,
		string(anchor.role)+"_anchor",
		"identifier",
		&groundedEvidenceRepositoryByteRangeNode{
			file:  parsed.fset.File(parsed.file.Pos()),
			start: anchor.startByte,
			end:   anchor.endByte,
		},
	)
	if err != nil {
		return GroundedEvidenceRepositoryRelationEndpoint{}, err
	}
	container, err := buildGroundedEvidenceRepositoryContextSlice(
		parsed.fset,
		parsed.data.Content,
		anchor.path,
		string(anchor.role)+"_container",
		containerKind,
		containerNode,
	)
	if err != nil {
		return GroundedEvidenceRepositoryRelationEndpoint{}, err
	}
	if container.StartByte > anchor.startByte ||
		container.EndByte < anchor.endByte {
		return GroundedEvidenceRepositoryRelationEndpoint{}, fmt.Errorf(
			"relation %s container does not cover anchor",
			anchor.role,
		)
	}
	packageClause, err := buildGroundedEvidenceRepositoryContextSlice(
		parsed.fset,
		parsed.data.Content,
		anchor.path,
		string(anchor.role)+"_package_clause",
		"package",
		&groundedEvidenceRepositoryPackageClauseNode{file: parsed.file},
	)
	if err != nil {
		return GroundedEvidenceRepositoryRelationEndpoint{}, err
	}
	return GroundedEvidenceRepositoryRelationEndpoint{
		Role:            anchor.role,
		Path:            anchor.path,
		Anchor:          anchorSlice,
		AtomicContainer: container,
		PackageClause:   packageClause,
		ReferencedBytes: container.EndByte - container.StartByte +
			packageClause.EndByte - packageClause.StartByte,
	}, nil
}

func validateGroundedEvidenceRepositoryRelationAnchor(
	record ProposalQueryResult,
	anchor groundedEvidenceRepositoryRelationAnchor,
	file RepositoryExtractorFile,
) error {
	snapshot := record.RepositorySnapshot
	meta := file.FileSnapshot
	if anchor.path == "" ||
		snapshot == nil ||
		meta.Path != anchor.path ||
		meta.RepositorySnapshotID != snapshot.ID ||
		anchor.fileContentHash != meta.BlobHash ||
		anchor.startByte < 0 ||
		anchor.endByte <= anchor.startByte ||
		anchor.endByte > len(file.Content) ||
		anchor.quotedText != string(file.Content[anchor.startByte:anchor.endByte]) ||
		anchor.quotedTextHash != contentHash(
			file.Content[anchor.startByte:anchor.endByte],
		) {
		return fmt.Errorf("relation %s anchor differs from authority", anchor.role)
	}
	for _, ref := range record.SourceRefs {
		if ref.Path == anchor.path &&
			ref.StartByte <= anchor.startByte &&
			ref.EndByte >= anchor.endByte {
			return nil
		}
	}
	return fmt.Errorf("relation %s anchor has no covering source ref", anchor.role)
}

func parseGroundedEvidenceRepositoryFile(
	file RepositoryExtractorFile,
) (groundedEvidenceRepositoryParsedFile, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(
		fset,
		file.FileSnapshot.Path,
		file.Content,
		parser.ParseComments|parser.SkipObjectResolution,
	)
	if err != nil {
		return groundedEvidenceRepositoryParsedFile{}, fmt.Errorf(
			"parsing repository context source %s: %w",
			file.FileSnapshot.Path,
			err,
		)
	}
	return groundedEvidenceRepositoryParsedFile{
		fset: fset,
		file: parsed,
		data: file,
	}, nil
}

func (c *groundedEvidenceRepositoryContextCompiler) parseFile(
	file RepositoryExtractorFile,
) (groundedEvidenceRepositoryParsedFile, error) {
	if c == nil {
		return groundedEvidenceRepositoryParsedFile{}, fmt.Errorf(
			"repository context compiler is required",
		)
	}
	meta := file.FileSnapshot
	if meta.RepositorySnapshotID == "" || meta.ID == "" {
		return groundedEvidenceRepositoryParsedFile{}, fmt.Errorf(
			"repository context file identity is incomplete",
		)
	}
	if c.parsedFiles == nil {
		return parseGroundedEvidenceRepositoryFile(file)
	}
	cacheKey := meta.RepositorySnapshotID + "\x00" + meta.ID
	if parsed, ok := c.parsedFiles[cacheKey]; ok {
		if parsed.data.FileSnapshot != meta ||
			!bytes.Equal(parsed.data.Content, file.Content) {
			return groundedEvidenceRepositoryParsedFile{}, fmt.Errorf(
				"repository context parsed file identity differs",
			)
		}
		return parsed, nil
	}
	parsed, err := parseGroundedEvidenceRepositoryFile(file)
	if err != nil {
		return groundedEvidenceRepositoryParsedFile{}, err
	}
	c.parsedFiles[cacheKey] = parsed
	return parsed, nil
}

func findGroundedEvidenceRepositoryDeclaration(
	fset *token.FileSet,
	file *ast.File,
	fact ResolvedCodeFact,
) (groundedEvidenceRepositoryASTMatch, error) {
	var matches []groundedEvidenceRepositoryASTMatch
	if groundedEvidenceRepositoryIdentifierMatches(fset, file.Name, fact) {
		matches = append(matches, groundedEvidenceRepositoryASTMatch{
			node: &groundedEvidenceRepositoryPackageClauseNode{file: file},
			kind: "package",
		})
	}
	for _, declaration := range file.Decls {
		switch node := declaration.(type) {
		case *ast.FuncDecl:
			if !groundedEvidenceRepositoryIdentifierMatches(
				fset,
				node.Name,
				fact,
			) {
				continue
			}
			kind := "function"
			if node.Recv != nil && len(node.Recv.List) > 0 {
				kind = "method"
			}
			matches = append(matches, groundedEvidenceRepositoryASTMatch{
				node: groundedEvidenceRepositoryFunctionContextNode(node),
				kind: kind,
			})
		case *ast.GenDecl:
			kind := strings.ToLower(node.Tok.String())
			for _, spec := range node.Specs {
				for _, ident := range groundedEvidenceRepositorySpecIdentifiers(spec) {
					if groundedEvidenceRepositoryIdentifierMatches(
						fset,
						ident,
						fact,
					) {
						matches = append(
							matches,
							groundedEvidenceRepositoryASTMatch{
								node: groundedEvidenceRepositoryGeneralDeclarationContextNode(
									node,
									spec,
								),
								kind: kind,
							},
						)
					}
				}
			}
		}
	}
	if len(matches) != 1 {
		return groundedEvidenceRepositoryASTMatch{}, fmt.Errorf(
			"code fact %s resolved to %d syntax containers",
			fact.SymbolRef,
			len(matches),
		)
	}
	if fact.SymbolKind != "" && fact.SymbolKind != matches[0].kind {
		return groundedEvidenceRepositoryASTMatch{}, fmt.Errorf(
			"code fact kind %s differs from syntax kind %s",
			fact.SymbolKind,
			matches[0].kind,
		)
	}
	return matches[0], nil
}

func findGroundedEvidenceRepositoryUsage(
	fset *token.FileSet,
	file *ast.File,
	anchor groundedEvidenceRepositoryRelationAnchor,
) (groundedEvidenceRepositoryUsageASTMatch, error) {
	var candidates []groundedEvidenceRepositoryUsageASTMatch
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		start := fset.Position(node.Pos()).Offset
		end := fset.Position(node.End()).Offset
		if start > anchor.startByte || end < anchor.endByte {
			return true
		}
		kind, ok := groundedEvidenceRepositoryUsageContainerKind(node)
		if ok {
			candidates = append(
				candidates,
				groundedEvidenceRepositoryUsageASTMatch{
					node: node,
					kind: kind,
					size: end - start,
				},
			)
		}
		return true
	})
	if len(candidates) == 0 {
		return groundedEvidenceRepositoryUsageASTMatch{}, fmt.Errorf(
			"relation usage at %s [%d,%d) has no syntax container",
			anchor.path,
			anchor.startByte,
			anchor.endByte,
		)
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.size < best.size ||
			candidate.size == best.size && candidate.kind < best.kind {
			best = candidate
		}
	}
	return best, nil
}

func groundedEvidenceRepositoryUsageContainerKind(
	node ast.Node,
) (string, bool) {
	switch node.(type) {
	case *ast.CallExpr:
		return "call_expression", true
	case *ast.SelectorExpr:
		return "selector_expression", true
	case *ast.IndexExpr, *ast.IndexListExpr, *ast.StarExpr, *ast.UnaryExpr,
		*ast.BinaryExpr, *ast.CompositeLit, *ast.TypeAssertExpr:
		return "expression", true
	case ast.Stmt:
		return "statement", true
	case ast.Spec:
		return "declaration_spec", true
	case *ast.Field:
		return "field", true
	default:
		return "", false
	}
}

func groundedEvidenceRepositoryFunctionContextNode(
	node *ast.FuncDecl,
) ast.Node {
	start := node.Pos()
	if node.Doc != nil {
		start = node.Doc.Pos()
	}
	return &groundedEvidenceRepositoryTokenRangeNode{
		start: start,
		end:   node.End(),
	}
}

func groundedEvidenceRepositoryGeneralDeclarationContextNode(
	declaration *ast.GenDecl,
	spec ast.Spec,
) ast.Node {
	if len(declaration.Specs) == 1 {
		start := declaration.Pos()
		if declaration.Doc != nil {
			start = declaration.Doc.Pos()
		}
		return &groundedEvidenceRepositoryTokenRangeNode{
			start: start,
			end:   declaration.End(),
		}
	}
	start := spec.Pos()
	end := spec.End()
	switch node := spec.(type) {
	case *ast.TypeSpec:
		if node.Doc != nil {
			start = node.Doc.Pos()
		}
		if node.Comment != nil {
			end = node.Comment.End()
		}
	case *ast.ValueSpec:
		if node.Doc != nil {
			start = node.Doc.Pos()
		}
		if node.Comment != nil {
			end = node.Comment.End()
		}
	}
	return &groundedEvidenceRepositoryTokenRangeNode{start: start, end: end}
}

func groundedEvidenceRepositorySpecIdentifiers(spec ast.Spec) []*ast.Ident {
	switch node := spec.(type) {
	case *ast.TypeSpec:
		return []*ast.Ident{node.Name}
	case *ast.ValueSpec:
		return node.Names
	default:
		return nil
	}
}

func groundedEvidenceRepositoryIdentifierMatches(
	fset *token.FileSet,
	ident *ast.Ident,
	fact ResolvedCodeFact,
) bool {
	if ident == nil {
		return false
	}
	return fset.Position(ident.Pos()).Offset == fact.StartByte &&
		fset.Position(ident.End()).Offset == fact.EndByte &&
		ident.Name == fact.QuotedText
}

func buildGroundedEvidenceRepositoryContextSlice(
	fset *token.FileSet,
	source []byte,
	path string,
	role string,
	kind string,
	node ast.Node,
) (GroundedEvidenceRepositoryContextSlice, error) {
	start := fset.Position(node.Pos()).Offset
	end := fset.Position(node.End()).Offset
	if start < 0 || end <= start || end > len(source) {
		return GroundedEvidenceRepositoryContextSlice{}, fmt.Errorf(
			"repository context %s range [%d,%d) is outside %s",
			role,
			start,
			end,
			path,
		)
	}
	exact := source[start:end]
	return GroundedEvidenceRepositoryContextSlice{
		Role:        role,
		Kind:        kind,
		Path:        path,
		StartByte:   start,
		EndByte:     end,
		ExactText:   string(exact),
		ContentHash: contentHash(exact),
	}, nil
}

func groundedEvidenceRepositoryUsageAnchor(
	usage ResolvedCodeUsageSite,
) groundedEvidenceRepositoryRelationAnchor {
	return groundedEvidenceRepositoryRelationAnchor{
		role:            GroundedEvidenceRepositoryContextUsage,
		path:            usage.Path,
		fileContentHash: usage.FileContentHash,
		startByte:       usage.StartByte,
		endByte:         usage.EndByte,
		quotedTextHash:  usage.QuotedTextHash,
		quotedText:      usage.QuotedText,
	}
}

func groundedEvidenceRepositoryDeclarationAnchor(
	role GroundedEvidenceRepositoryContextRole,
	endpoint ResolvedCodeDeclarationEndpoint,
) groundedEvidenceRepositoryRelationAnchor {
	return groundedEvidenceRepositoryRelationAnchor{
		role:            role,
		path:            endpoint.Path,
		fileContentHash: endpoint.FileContentHash,
		symbolRef:       endpoint.SymbolRef,
		symbolKind:      endpoint.SymbolKind,
		qualifiedName:   endpoint.QualifiedName,
		startByte:       endpoint.StartByte,
		endByte:         endpoint.EndByte,
		quotedTextHash:  endpoint.QuotedTextHash,
		quotedText:      endpoint.QuotedText,
	}
}

func groundedEvidenceRepositoryRelationAnchorFact(
	anchor groundedEvidenceRepositoryRelationAnchor,
) ResolvedCodeFact {
	return ResolvedCodeFact{
		SchemaVersion:   CodeFactSchemaV1,
		FactKind:        CodeFactKindDeclaration,
		Path:            anchor.path,
		FileContentHash: anchor.fileContentHash,
		SymbolRef:       anchor.symbolRef,
		SymbolKind:      anchor.symbolKind,
		QualifiedName:   anchor.qualifiedName,
		StartByte:       anchor.startByte,
		EndByte:         anchor.endByte,
		QuotedTextHash:  anchor.quotedTextHash,
		QuotedText:      anchor.quotedText,
	}
}

type groundedEvidenceRepositoryPackageClauseNode struct {
	file *ast.File
}

func (n *groundedEvidenceRepositoryPackageClauseNode) Pos() token.Pos {
	return n.file.Package
}

func (n *groundedEvidenceRepositoryPackageClauseNode) End() token.Pos {
	return n.file.Name.End()
}

type groundedEvidenceRepositoryTokenRangeNode struct {
	start token.Pos
	end   token.Pos
}

func (n *groundedEvidenceRepositoryTokenRangeNode) Pos() token.Pos {
	return n.start
}

func (n *groundedEvidenceRepositoryTokenRangeNode) End() token.Pos {
	return n.end
}

type groundedEvidenceRepositoryByteRangeNode struct {
	file  *token.File
	start int
	end   int
}

func (n *groundedEvidenceRepositoryByteRangeNode) Pos() token.Pos {
	return n.file.Pos(n.start)
}

func (n *groundedEvidenceRepositoryByteRangeNode) End() token.Pos {
	return n.file.Pos(n.end)
}
