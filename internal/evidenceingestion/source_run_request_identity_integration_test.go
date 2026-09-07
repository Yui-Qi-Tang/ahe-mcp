//go:build integration

package evidenceingestion

import (
	"errors"
	"testing"
)

func TestIntegrationSourceExtractionRequestIdentityRejectsChangedInputs(t *testing.T) {
	for _, changedInput := range []string{"source", "extractor_definition"} {
		t.Run(changedInput, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, fixture := integrationInputFixture(t, "source-request-identity-first")
			firstSource, err := CaptureManualSource(ctx, pool, input)
			if err != nil {
				t.Fatal(err)
			}
			request := ExtractorOutputInput{
				RequestID:           "one-source-extraction-request",
				SourceSnapshotID:    firstSource.SourceSnapshotID,
				ExtractionViewID:    firstSource.ExtractionViewID,
				ExtractorDefinition: testExternalExtractorDefinition(),
				ProducerSessionRef:  "session:first-created",
				Output:              fixture,
			}
			first, err := SubmitExtractorOutput(ctx, pool, request)
			if err != nil {
				t.Fatalf("first source extraction: %v", err)
			}
			changed := request
			if changedInput == "source" {
				// This is a valid independently captured source/view pair, not a
				// malformed FK coordinate or an invented persisted run ID.
				input.RequestID = "source-request-identity-second"
				input.SourceID += "-second-object"
				secondSource, err := CaptureManualSource(ctx, pool, input)
				if err != nil {
					t.Fatal(err)
				}
				changed.SourceSnapshotID = secondSource.SourceSnapshotID
				changed.ExtractionViewID = secondSource.ExtractionViewID
			} else {
				changed.ExtractorDefinition.Version += "-second"
			}
			second, err := SubmitExtractorOutput(ctx, pool, changed)
			var domainErr *DomainError
			if !errors.As(err, &domainErr) || domainErr.Kind != ErrorIdempotencyKeyReused {
				t.Errorf("same request with changed %s: result=%+v error=%v, want idempotency_key_reused", changedInput, second, err)
			}
			// The request remains bound to the first committed extraction input.
			// Rejecting its reuse must not create another run, attempt, or proposal.
			replay, err := SubmitExtractorOutput(ctx, pool, request)
			if err != nil || !replay.Replayed || replay.ExtractionRunID != first.ExtractionRunID || replay.ProposalOccurrenceID != first.ProposalOccurrenceID {
				t.Fatalf("original exact replay after rejection: result=%+v error=%v", replay, err)
			}
			for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions"} {
				assertTableCount(t, ctx, pool, table, 0)
			}
			for _, table := range []string{"extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences", "extractor_definitions"} {
				assertTableCount(t, ctx, pool, table, 1)
			}
		})
	}
}
