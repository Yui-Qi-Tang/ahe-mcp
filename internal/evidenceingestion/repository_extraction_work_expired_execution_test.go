package evidenceingestion

import (
	"testing"
	"time"
)

func TestValidateRepositoryExtractionWorkExpiredExecutionListInput(t *testing.T) {
	for _, limit := range []int{1, RepositoryExtractionWorkExpiredExecutionMaxLimit} {
		if err := validateRepositoryExtractionWorkExpiredExecutionListInput(RepositoryExtractionWorkExpiredExecutionListInput{Limit: limit}); err != nil {
			t.Fatalf("validate limit %d: %v", limit, err)
		}
	}

	for _, limit := range []int{0, RepositoryExtractionWorkExpiredExecutionMaxLimit + 1} {
		err := validateRepositoryExtractionWorkExpiredExecutionListInput(RepositoryExtractionWorkExpiredExecutionListInput{Limit: limit})
		assertKind(t, err, ErrorInvalidInput)
	}
}

func TestListExpiredRepositoryExtractionWorkExecutionsValidatesPoolAndTime(t *testing.T) {
	_, err := ListExpiredRepositoryExtractionWorkExecutions(t.Context(), nil, RepositoryExtractionWorkExpiredExecutionListInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = listExpiredRepositoryExtractionWorkExecutions(t.Context(), nil, RepositoryExtractionWorkExpiredExecutionListInput{Limit: 1}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}
