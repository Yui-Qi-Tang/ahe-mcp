package evidenceingestion

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestResolveRepositoryExtractionWorkValidatesInput(t *testing.T) {
	if _, err := ResolveRepositoryExtractionWork(t.Context(), nil, "repo-work:test"); err == nil {
		t.Fatal("ResolveRepositoryExtractionWork(nil) error = nil")
	}

	_, err := ResolveRepositoryExtractionWork(t.Context(), new(pgxpool.Pool), "bad-id")
	assertKind(t, err, ErrorInvalidRecordID)
}
