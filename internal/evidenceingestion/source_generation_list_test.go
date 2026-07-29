package evidenceingestion

import "testing"

func TestListRepositorySourceGenerationsValidatesInput(t *testing.T) {
	_, err := ListRepositorySourceGenerations(t.Context(), nil, RepositorySourceGenerationListInput{})
	assertKind(t, err, ErrorInvalidInput)

	valid := RepositorySourceGenerationListInput{
		RepoID:        " repo ",
		ExtractorName: " " + ExtractorRepositoryGoParserCodeFact + " ",
		Limit:         10,
	}
	got, err := validateRepositorySourceGenerationListInput(valid)
	if err != nil {
		t.Fatalf("validateRepositorySourceGenerationListInput() error = %v", err)
	}
	if got.RepoID != "repo" ||
		got.ExtractorName != ExtractorRepositoryGoParserCodeFact ||
		got.Limit != 10 {
		t.Fatalf("validated input = %+v", got)
	}

	tests := map[string]RepositorySourceGenerationListInput{
		"missing repo": {
			ExtractorName: ExtractorRepositoryGoParserCodeFact,
			Limit:         1,
		},
		"unsupported extractor": {
			RepoID:        "repo",
			ExtractorName: "ollama-local",
			Limit:         1,
		},
		"zero limit": {
			RepoID:        "repo",
			ExtractorName: ExtractorRepositoryGoParserCodeFact,
		},
		"large limit": {
			RepoID:        "repo",
			ExtractorName: ExtractorRepositoryGoParserCodeFact,
			Limit:         RepositorySourceGenerationListMaxLimit + 1,
		},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := validateRepositorySourceGenerationListInput(input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}
