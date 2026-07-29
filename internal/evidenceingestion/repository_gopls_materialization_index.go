package evidenceingestion

type repositoryGoplsDeclarationKey struct {
	path      string
	startByte int
	endByte   int
}

type repositoryGoplsMaterializationIndex struct {
	input             RepositoryExtractorInput
	filesByPath       map[string]RepositoryExtractorFile
	extractionsByPath map[string]GoParserFileExtraction
	declarations      map[repositoryGoplsDeclarationKey]GoParserDeclaration
	declarationsBuilt bool
	grounding         *repositoryGroundingIndex
}

func newRepositoryGoplsMaterializationIndex(
	input RepositoryExtractorInput,
) *repositoryGoplsMaterializationIndex {
	filesByPath := make(map[string]RepositoryExtractorFile, len(input.Files))
	for _, file := range input.Files {
		filesByPath[file.FileSnapshot.Path] = file
	}
	return &repositoryGoplsMaterializationIndex{
		input:             input,
		filesByPath:       filesByPath,
		extractionsByPath: make(map[string]GoParserFileExtraction, len(input.Files)),
		grounding:         newRepositoryGroundingIndex(input, filesByPath),
	}
}

func (i *repositoryGoplsMaterializationIndex) extraction(
	path string,
) (GoParserFileExtraction, error) {
	if extraction, ok := i.extractionsByPath[path]; ok {
		return extraction, nil
	}
	file, ok := i.filesByPath[path]
	if !ok {
		return GoParserFileExtraction{}, newDomainError(
			ErrorInvalidExtractorOutput,
			"repository gopls materialization references unknown path %q",
			path,
		)
	}
	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    i.input.RepositorySnapshot.RepoID,
		CommitSHA: i.input.RepositorySnapshot.CommitSHA,
		Path:      path,
		Source:    file.Content,
	})
	if err != nil {
		return GoParserFileExtraction{}, err
	}
	i.extractionsByPath[path] = extraction
	i.grounding.codeFactsByPath[path] = repositoryGoParserExtractionFactIndex(extraction)
	return extraction, nil
}

func (i *repositoryGoplsMaterializationIndex) declarationIndex() (map[repositoryGoplsDeclarationKey]GoParserDeclaration, error) {
	if i.declarationsBuilt {
		return i.declarations, nil
	}
	declarations := make(map[repositoryGoplsDeclarationKey]GoParserDeclaration)
	for _, file := range i.input.Files {
		extraction, err := i.extraction(file.FileSnapshot.Path)
		if err != nil {
			return nil, err
		}
		for _, declaration := range extraction.Declarations {
			declarations[repositoryGoplsDeclarationKey{
				path:      declaration.Path,
				startByte: declaration.StartByte,
				endByte:   declaration.EndByte,
			}] = declaration
		}
	}
	i.declarations = declarations
	i.declarationsBuilt = true
	return declarations, nil
}

func repositoryGoParserExtractionFactIndex(
	extraction GoParserFileExtraction,
) map[codeFactSpan]CodeFactOutput {
	index := make(map[codeFactSpan]CodeFactOutput, len(extraction.Declarations)+1)
	packageFact := extraction.PackageClause.CodeFact
	index[codeFactSpan{
		startByte: packageFact.StartByte,
		endByte:   packageFact.EndByte,
	}] = packageFact
	for _, declaration := range extraction.Declarations {
		fact := declaration.CodeFact
		index[codeFactSpan{
			startByte: fact.StartByte,
			endByte:   fact.EndByte,
		}] = fact
	}
	return index
}
