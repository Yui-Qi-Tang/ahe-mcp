//go:build integration

package evidenceingestion

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These helpers produce only synthetic fixtures and TEST APPROVAL STUB inputs.
func submitHistoricalFixtureOutput(ctx context.Context, pool *pgxpool.Pool, input ExtractorOutputInput) (IngestResult, error) {
	if input.ExtractorDefinition.Name == "" && input.ExtractorDefinition.Version == "" {
		input.ExtractorDefinition = ExtractorDefinitionInput{Name: "synthetic-product-historical-fixture", Version: "v1"}
	}
	return SubmitExtractorOutput(ctx, pool, input)
}

func historicalPending(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) string {
	t.Helper()
	p := createIntegrationClosureExternalProposal(t, ctx, pool, name, "HISTORICAL-PENDING-"+name, "r1", "Retained synthetic fixture material: "+name)
	return p.ProposalOccurrenceID
}
