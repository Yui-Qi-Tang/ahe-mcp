package evidenceingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// BoundedSourceViewContractV1 identifies the fixed source-view preflight contract.
	BoundedSourceViewContractV1 = "bounded-source-view-v1"

	// BoundedSourceViewMaxRenderedBytesV1 caps rendered bytes materialized by the v1 loader.
	BoundedSourceViewMaxRenderedBytesV1 int64 = 1 << 20
	// BoundedSourceViewMaxSpansV1 caps span rows materialized by the v1 loader.
	BoundedSourceViewMaxSpansV1 int64 = 4096
	// BoundedSourceViewMaxSpanBytesV1 caps one atomic span materialized by the v1 loader.
	BoundedSourceViewMaxSpanBytesV1 int64 = 8192
)

// BoundedSourceViewStatus describes whether a source view was materialized.
type BoundedSourceViewStatus string

const (
	// BoundedSourceViewStatusAvailable means the immutable view passed preflight and integrity validation.
	BoundedSourceViewStatusAvailable BoundedSourceViewStatus = "available"
	// BoundedSourceViewStatusOverBudget means preflight withheld the rendered view.
	BoundedSourceViewStatusOverBudget BoundedSourceViewStatus = "over_budget"
)

// BoundedSourceViewLimitReason identifies the first fixed v1 cap exceeded.
type BoundedSourceViewLimitReason string

const (
	BoundedSourceViewLimitRenderedBytes BoundedSourceViewLimitReason = "rendered_bytes"
	BoundedSourceViewLimitSpanCount     BoundedSourceViewLimitReason = "span_count"
	BoundedSourceViewLimitAtomicSpan    BoundedSourceViewLimitReason = "atomic_span_bytes"
)

// BoundedSourceViewObservation is metadata read before rendered bytes or span payloads.
type BoundedSourceViewObservation struct {
	SourceSnapshotID    string      `json:"source_snapshot_id"`
	ExtractionViewID    string      `json:"extraction_view_id"`
	SourceSystem        string      `json:"source_system"`
	RawContentHash      string      `json:"raw_content_hash"`
	Renderer            RendererRef `json:"renderer"`
	RenderedContentHash string      `json:"rendered_content_hash"`
	RenderedBytes       int64       `json:"rendered_bytes"`
	SpanCount           int64       `json:"span_count"`
	MaxSpanBytes        int64       `json:"max_span_bytes"`
}

// BoundedSourceViewResult contains either one validated view or a typed budget outcome.
type BoundedSourceViewResult struct {
	Contract    string                       `json:"contract"`
	Status      BoundedSourceViewStatus      `json:"status"`
	LimitReason BoundedSourceViewLimitReason `json:"limit_reason,omitempty"`
	Observation BoundedSourceViewObservation `json:"observation"`
	Input       *ExtractorInput              `json:"input,omitempty"`
}

// LoadBoundedSourceView preflights and conditionally materializes one immutable source view.
func LoadBoundedSourceView(
	ctx context.Context,
	pool *pgxpool.Pool,
	sourceSnapshotID string,
	extractionViewID string,
) (BoundedSourceViewResult, error) {
	if pool == nil {
		return BoundedSourceViewResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return loadBoundedSourceView(ctx, pgxDB{pool: pool}, sourceSnapshotID, extractionViewID)
}

func loadBoundedSourceView(
	ctx context.Context,
	db sqlDB,
	sourceSnapshotID string,
	extractionViewID string,
) (BoundedSourceViewResult, error) {
	if err := validateBoundedSourceViewIDs(sourceSnapshotID, extractionViewID); err != nil {
		return BoundedSourceViewResult{}, err
	}
	var result BoundedSourceViewResult
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		var err error
		result, err = loadBoundedSourceViewFromQueryer(
			ctx,
			tx,
			sourceSnapshotID,
			extractionViewID,
		)
		return err
	})
	if err != nil {
		return BoundedSourceViewResult{}, err
	}
	return result, nil
}

func loadBoundedSourceViewFromQueryer(
	ctx context.Context,
	db sqlQueryer,
	sourceSnapshotID string,
	extractionViewID string,
) (BoundedSourceViewResult, error) {
	observation, err := loadBoundedSourceViewObservation(
		ctx,
		db,
		sourceSnapshotID,
		extractionViewID,
	)
	if err != nil {
		return BoundedSourceViewResult{}, err
	}
	result := BoundedSourceViewResult{
		Contract:    BoundedSourceViewContractV1,
		Observation: observation,
	}
	if reason := boundedSourceViewLimitReason(observation); reason != "" {
		result.Status = BoundedSourceViewStatusOverBudget
		result.LimitReason = reason
		return result, nil
	}

	sourceCtx, err := loadManualSourceContext(ctx, db, sourceSnapshotID, extractionViewID)
	if err != nil {
		return BoundedSourceViewResult{}, err
	}
	input := sourceCtx.extractorInput()
	if err := validateBoundedSourceViewMaterialization(observation, input); err != nil {
		return BoundedSourceViewResult{}, err
	}
	result.Status = BoundedSourceViewStatusAvailable
	result.Input = &input
	return result, nil
}

func validateBoundedSourceViewIDs(sourceSnapshotID string, extractionViewID string) error {
	if !hasStableIDPrefix(sourceSnapshotID, "srcsnap:") {
		return newDomainError(
			ErrorInvalidRecordID,
			"source_snapshot_id %q must start with srcsnap:",
			sourceSnapshotID,
		)
	}
	if !hasStableIDPrefix(extractionViewID, "view:") {
		return newDomainError(
			ErrorInvalidRecordID,
			"extraction_view_id %q must start with view:",
			extractionViewID,
		)
	}
	return nil
}

func hasStableIDPrefix(value string, prefix string) bool {
	return len(value) > len(prefix) && value[:len(prefix)] == prefix
}

func loadBoundedSourceViewObservation(
	ctx context.Context,
	db sqlQueryer,
	sourceSnapshotID string,
	extractionViewID string,
) (BoundedSourceViewObservation, error) {
	var observation BoundedSourceViewObservation
	var viewSourceSnapshotID string
	row := db.queryRow(ctx, `
		SELECT
			ss.source_snapshot_id,
			ss.source_system,
			ss.raw_content_hash,
			ev.extraction_view_id,
			ev.source_snapshot_id,
			ev.renderer_name,
			ev.renderer_version,
			ev.rendered_content_hash,
			octet_length(ev.rendered_content)::bigint AS rendered_byte_length,
			span_metrics.span_count,
			span_metrics.max_span_byte_length
		FROM source_snapshots ss
		JOIN extraction_views ev
			ON ev.source_snapshot_id = ss.source_snapshot_id
		CROSS JOIN LATERAL (
			SELECT
				count(*)::bigint AS span_count,
				COALESCE(
					max(
						GREATEST(
							sce.end_byte - sce.start_byte,
							octet_length(sce.quoted_text)
						)
					),
					0
				)::bigint AS max_span_byte_length
			FROM span_catalog_entries sce
			WHERE sce.extraction_view_id = ev.extraction_view_id
		) span_metrics
		WHERE ss.source_snapshot_id = $1
			AND ev.extraction_view_id = $2
	`, sourceSnapshotID, extractionViewID)
	err := row.Scan(
		&observation.SourceSnapshotID,
		&observation.SourceSystem,
		&observation.RawContentHash,
		&observation.ExtractionViewID,
		&viewSourceSnapshotID,
		&observation.Renderer.Name,
		&observation.Renderer.Version,
		&observation.RenderedContentHash,
		&observation.RenderedBytes,
		&observation.SpanCount,
		&observation.MaxSpanBytes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BoundedSourceViewObservation{}, newDomainError(
				ErrorMissingSourceViewAttempt,
				"source snapshot %s and extraction view %s were not found",
				sourceSnapshotID,
				extractionViewID,
			)
		}
		return BoundedSourceViewObservation{}, fmt.Errorf("preflighting bounded source view: %w", err)
	}
	if viewSourceSnapshotID != observation.SourceSnapshotID {
		return BoundedSourceViewObservation{}, newDomainError(
			ErrorOccurrenceConflict,
			"extraction view %s belongs to source snapshot %s, want %s",
			observation.ExtractionViewID,
			viewSourceSnapshotID,
			observation.SourceSnapshotID,
		)
	}
	contract, err := renderingContract(observation.SourceSystem)
	if err != nil {
		return BoundedSourceViewObservation{}, err
	}
	if observation.Renderer.Name != contract.RendererName ||
		observation.Renderer.Version != contract.RendererVersion {
		return BoundedSourceViewObservation{}, newDomainError(
			ErrorInvalidInput,
			"renderer %s/%s is not supported",
			observation.Renderer.Name,
			observation.Renderer.Version,
		)
	}
	if observation.RawContentHash != observation.RenderedContentHash {
		return BoundedSourceViewObservation{}, newDomainError(
			ErrorQuotedHashMismatch,
			"identity view hash %s does not match raw source hash %s",
			observation.RenderedContentHash,
			observation.RawContentHash,
		)
	}
	if observation.RenderedBytes < 0 ||
		observation.SpanCount < 0 ||
		observation.MaxSpanBytes < 0 {
		return BoundedSourceViewObservation{}, newDomainError(
			ErrorInvalidInput,
			"bounded source view preflight returned negative measurements",
		)
	}
	return observation, nil
}

func boundedSourceViewLimitReason(
	observation BoundedSourceViewObservation,
) BoundedSourceViewLimitReason {
	switch {
	case observation.RenderedBytes > BoundedSourceViewMaxRenderedBytesV1:
		return BoundedSourceViewLimitRenderedBytes
	case observation.SpanCount > BoundedSourceViewMaxSpansV1:
		return BoundedSourceViewLimitSpanCount
	case observation.MaxSpanBytes > BoundedSourceViewMaxSpanBytesV1:
		return BoundedSourceViewLimitAtomicSpan
	default:
		return ""
	}
}

func validateBoundedSourceViewMaterialization(
	observation BoundedSourceViewObservation,
	input ExtractorInput,
) error {
	if input.SourceSnapshotID != observation.SourceSnapshotID ||
		input.ExtractionViewID != observation.ExtractionViewID ||
		input.SourceSystem != observation.SourceSystem ||
		input.RawContentHash != observation.RawContentHash ||
		input.Renderer != observation.Renderer ||
		input.RenderedContentHash != observation.RenderedContentHash {
		return newDomainError(
			ErrorOccurrenceConflict,
			"materialized source view differs from its bounded preflight identity",
		)
	}
	renderedBytes := int64(len([]byte(input.RenderedText)))
	spanCount := int64(len(input.Spans))
	var maxSpanBytes int64
	for _, span := range input.Spans {
		spanBytes := int64(span.EndByte - span.StartByte)
		if textBytes := int64(len([]byte(span.Text))); textBytes > spanBytes {
			spanBytes = textBytes
		}
		if spanBytes > maxSpanBytes {
			maxSpanBytes = spanBytes
		}
	}
	if renderedBytes != observation.RenderedBytes ||
		spanCount != observation.SpanCount ||
		maxSpanBytes != observation.MaxSpanBytes {
		return newDomainError(
			ErrorOccurrenceConflict,
			"materialized source view measurements differ from bounded preflight",
		)
	}
	return nil
}
