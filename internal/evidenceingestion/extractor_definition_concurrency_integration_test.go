//go:build integration

package evidenceingestion

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Each request is distinct; only the content-addressed extractor definition is
// shared. No model runs and no claims are admitted by this fixture.
func TestIntegrationExtractorDefinitionConcurrentReuse(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, output := integrationInputFixture(t, "shared-definition-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	cfg := pool.Config()
	cfg.MaxConns = writers
	contenders, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer contenders.Close()
	var conns []*pgxpool.Conn
	for range writers {
		conn, err := contenders.Acquire(ctx)
		if err != nil {
			for _, c := range conns {
				c.Release()
			}
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	for _, c := range conns {
		c.Release()
	}
	for round := range 16 {
		definition := testExternalExtractorDefinition()
		definition.Name = fmt.Sprintf("shared-definition-%d", round)
		start := make(chan struct{})
		errs := make([]error, writers)
		results := make([]IngestResult, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Go(func() {
				<-start
				results[i], errs[i] = SubmitExtractorOutput(ctx, contenders, ExtractorOutputInput{
					RequestID:        fmt.Sprintf("definition-%d-request-%d", round, i),
					SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID,
					ExtractorDefinition: definition, Output: output,
				})
			})
		}
		close(start)
		wg.Wait()
		seen := map[string]bool{}
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d writer %d: %v", round, i, err)
			}
			id := results[i].ProposalOccurrenceID
			if id == "" || seen[id] || results[i].Replayed {
				t.Fatal("independent requests were merged")
			}
			seen[id] = true
		}
		assertTableCount(t, ctx, pool, "extractor_definitions", round+1)
		assertTableCount(t, ctx, pool, "extraction_runs", (round+1)*writers)
		assertTableCount(t, ctx, pool, "proposal_occurrences", (round+1)*writers)
	}
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
}
