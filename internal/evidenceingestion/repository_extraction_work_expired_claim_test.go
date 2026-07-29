package evidenceingestion

import (
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkExpiredClaimListInput(t *testing.T) {
	valid := RepositoryExtractionWorkExpiredClaimListInput{
		RepoID:        "ahe-wrap",
		ExtractorName: ExtractorRepositoryGoplsCodeFact,
		Limit:         RepositoryExtractionWorkExpiredClaimMaxLimit,
	}
	got, err := validateRepositoryExtractionWorkExpiredClaimListInput(RepositoryExtractionWorkExpiredClaimListInput{
		RepoID:        " ahe-wrap ",
		ExtractorName: " " + ExtractorRepositoryGoplsCodeFact + " ",
		Limit:         RepositoryExtractionWorkExpiredClaimMaxLimit,
	})
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkExpiredClaimListInput() error = %v", err)
	}
	if got != valid {
		t.Fatalf("validated input = %+v, want %+v", got, valid)
	}

	tests := []struct {
		name  string
		input RepositoryExtractionWorkExpiredClaimListInput
	}{
		{name: "missing repo", input: RepositoryExtractionWorkExpiredClaimListInput{ExtractorName: valid.ExtractorName, Limit: 1}},
		{name: "unsupported extractor", input: RepositoryExtractionWorkExpiredClaimListInput{RepoID: valid.RepoID, ExtractorName: "arbitrary", Limit: 1}},
		{name: "zero limit", input: RepositoryExtractionWorkExpiredClaimListInput{RepoID: valid.RepoID, ExtractorName: valid.ExtractorName}},
		{name: "large limit", input: RepositoryExtractionWorkExpiredClaimListInput{RepoID: valid.RepoID, ExtractorName: valid.ExtractorName, Limit: RepositoryExtractionWorkExpiredClaimMaxLimit + 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRepositoryExtractionWorkExpiredClaimListInput(tt.input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestListExpiredRepositoryExtractionWorkClaimsValidatesPoolAndTime(t *testing.T) {
	_, err := ListExpiredRepositoryExtractionWorkClaims(t.Context(), nil, RepositoryExtractionWorkExpiredClaimListInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = listExpiredRepositoryExtractionWorkClaims(t.Context(), nil, RepositoryExtractionWorkExpiredClaimListInput{
		RepoID:        "ahe-wrap",
		ExtractorName: ExtractorRepositoryGoParserCodeFact,
		Limit:         1,
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}
