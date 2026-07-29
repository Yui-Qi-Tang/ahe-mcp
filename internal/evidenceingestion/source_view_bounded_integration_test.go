//go:build integration

package evidenceingestion

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationLoadBoundedSourceViewMaterializesAvailableViewReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	input := ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "bounded-source-view-available",
		SourceVersion: "v1",
		Raw: []byte(
			"# Retry policy\n\n" +
				"Failed work remains fenced.\n",
		),
		RequestID: "bounded-source-view-available",
	}
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}

	before := boundedSourceViewIntegrationCounts(t, ctx, pool)
	result, err := LoadBoundedSourceView(
		ctx,
		pool,
		source.SourceSnapshotID,
		source.ExtractionViewID,
	)
	if err != nil {
		t.Fatalf("LoadBoundedSourceView() error = %v", err)
	}
	after := boundedSourceViewIntegrationCounts(t, ctx, pool)

	if result.Contract != BoundedSourceViewContractV1 ||
		result.Status != BoundedSourceViewStatusAvailable ||
		result.LimitReason != "" ||
		result.Input == nil {
		t.Fatalf("bounded result = %+v", result)
	}
	if result.Input.RenderedText != string(input.Raw) ||
		result.Observation.RenderedBytes != int64(len(input.Raw)) ||
		result.Observation.SpanCount != int64(len(source.Spans)) {
		t.Fatalf("bounded observation/input = %+v / %+v", result.Observation, result.Input)
	}
	if before != after {
		t.Fatalf("bounded load changed relation counts: before %+v after %+v", before, after)
	}
}

func TestIntegrationLoadBoundedSourceViewWithholdsOversizedAtomicSpanReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	input := ManualTextInput{
		SourceSystem:  SourceSystemManualText,
		SourceID:      "bounded-source-view-oversized-atomic-span",
		SourceVersion: "v1",
		Raw: []byte(
			strings.Repeat("x", int(BoundedSourceViewMaxSpanBytesV1)+1) + "\n",
		),
		RequestID: "bounded-source-view-oversized-atomic-span",
	}
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}

	before := boundedSourceViewIntegrationCounts(t, ctx, pool)
	result, err := LoadBoundedSourceView(
		ctx,
		pool,
		source.SourceSnapshotID,
		source.ExtractionViewID,
	)
	if err != nil {
		t.Fatalf("LoadBoundedSourceView() error = %v", err)
	}
	after := boundedSourceViewIntegrationCounts(t, ctx, pool)

	if result.Contract != BoundedSourceViewContractV1 ||
		result.Status != BoundedSourceViewStatusOverBudget ||
		result.LimitReason != BoundedSourceViewLimitAtomicSpan ||
		result.Input != nil {
		t.Fatalf("bounded result = %+v", result)
	}
	if result.Observation.MaxSpanBytes != BoundedSourceViewMaxSpanBytesV1+1 {
		t.Fatalf(
			"max span bytes = %d, want %d",
			result.Observation.MaxSpanBytes,
			BoundedSourceViewMaxSpanBytesV1+1,
		)
	}
	if before != after {
		t.Fatalf("bounded load changed relation counts: before %+v after %+v", before, after)
	}
}

type boundedSourceViewCounts struct {
	SourceBlobs        int
	SourceSnapshots    int
	ExtractionViews    int
	SpanCatalog        int
	ExtractionAttempts int
	ProposalBatches    int
	Proposals          int
	Admissions         int
	GraphNodes         int
	GraphEdges         int
}

func boundedSourceViewIntegrationCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) boundedSourceViewCounts {
	t.Helper()
	return boundedSourceViewCounts{
		SourceBlobs:        boundedSourceViewTableCount(t, ctx, pool, "source_blobs"),
		SourceSnapshots:    boundedSourceViewTableCount(t, ctx, pool, "source_snapshots"),
		ExtractionViews:    boundedSourceViewTableCount(t, ctx, pool, "extraction_views"),
		SpanCatalog:        boundedSourceViewTableCount(t, ctx, pool, "span_catalog_entries"),
		ExtractionAttempts: boundedSourceViewTableCount(t, ctx, pool, "extraction_attempts"),
		ProposalBatches:    boundedSourceViewTableCount(t, ctx, pool, "proposal_batches"),
		Proposals:          boundedSourceViewTableCount(t, ctx, pool, "proposal_occurrences"),
		Admissions:         boundedSourceViewTableCount(t, ctx, pool, "admission_decisions"),
		GraphNodes:         boundedSourceViewTableCount(t, ctx, pool, "canonical_graph_nodes"),
		GraphEdges:         boundedSourceViewTableCount(t, ctx, pool, "canonical_graph_edges"),
	}
}

func boundedSourceViewTableCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
