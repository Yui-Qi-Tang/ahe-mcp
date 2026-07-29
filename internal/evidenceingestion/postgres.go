package evidenceingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sqlQueryer interface {
	query(ctx context.Context, sql string, args ...any) (sqlRows, error)
	queryRow(ctx context.Context, sql string, args ...any) sqlRow
}

type sqlDB interface {
	sqlQueryer
	begin(ctx context.Context) (sqlTx, error)
}

type sqlTx interface {
	sqlQueryer
	exec(ctx context.Context, sql string, args ...any) (execResult, error)
	commit(ctx context.Context) error
	rollback(ctx context.Context) error
}

type sqlRow interface {
	Scan(dest ...any) error
}

type sqlRows interface {
	Close()
	Err() error
	Next() bool
	Scan(dest ...any) error
}

type execResult interface {
	RowsAffected() int64
}

type pgxDB struct {
	pool *pgxpool.Pool
}

func (db pgxDB) begin(ctx context.Context) (sqlTx, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxTx{tx: tx}, nil
}

func (db pgxDB) queryRow(ctx context.Context, sql string, args ...any) sqlRow {
	return db.pool.QueryRow(ctx, sql, args...)
}

func (db pgxDB) query(ctx context.Context, sql string, args ...any) (sqlRows, error) {
	rows, err := db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

type pgxTx struct {
	tx pgx.Tx
}

func (tx pgxTx) exec(ctx context.Context, sql string, args ...any) (execResult, error) {
	return tx.tx.Exec(ctx, sql, args...)
}

func (tx pgxTx) query(ctx context.Context, sql string, args ...any) (sqlRows, error) {
	rows, err := tx.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (tx pgxTx) queryRow(ctx context.Context, sql string, args ...any) sqlRow {
	return tx.tx.QueryRow(ctx, sql, args...)
}

func (tx pgxTx) commit(ctx context.Context) error {
	return tx.tx.Commit(ctx)
}

func (tx pgxTx) rollback(ctx context.Context) error {
	return tx.tx.Rollback(ctx)
}

// IngestManualText runs the Slice 1 manual text ingestion round trip.
func IngestManualText(ctx context.Context, pool *pgxpool.Pool, input ManualTextInput, fixture FrozenExtractorOutput) (IngestResult, error) {
	if pool == nil {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return ingestManualText(ctx, pgxDB{pool: pool}, input, fixture)
}

// CaptureManualSource persists bounded UTF-8 source bytes without creating proposals.
func CaptureManualSource(ctx context.Context, pool *pgxpool.Pool, input ManualTextInput) (SourceIntakeResult, error) {
	if pool == nil {
		return SourceIntakeResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return captureManualSource(ctx, pgxDB{pool: pool}, input)
}

// SubmitExtractorOutput materializes extractor output against an existing source view.
func SubmitExtractorOutput(ctx context.Context, pool *pgxpool.Pool, input ExtractorOutputInput) (IngestResult, error) {
	if pool == nil {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return submitExtractorOutput(ctx, pgxDB{pool: pool}, input)
}

// BuildExtractorInput loads grounded source view data for a trusted local extractor.
func BuildExtractorInput(ctx context.Context, pool *pgxpool.Pool, extractionViewID string) (ExtractorInput, error) {
	if pool == nil {
		return ExtractorInput{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return buildExtractorInput(ctx, pgxDB{pool: pool}, extractionViewID)
}

func captureManualSource(ctx context.Context, db sqlDB, input ManualTextInput) (SourceIntakeResult, error) {
	if input.RequestID == "" {
		return SourceIntakeResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	sourceCtx, err := buildManualSourceContext(input)
	if err != nil {
		return SourceIntakeResult{}, err
	}
	changed, err := persistManualSource(ctx, db, sourceCtx)
	if err != nil {
		return SourceIntakeResult{}, err
	}
	return sourceCtx.result(!changed), nil
}

func buildExtractorInput(ctx context.Context, db sqlDB, extractionViewID string) (ExtractorInput, error) {
	if extractionViewID == "" {
		return ExtractorInput{}, newDomainError(ErrorInvalidInput, "extraction_view_id is required")
	}
	if !strings.HasPrefix(extractionViewID, "view:") {
		return ExtractorInput{}, newDomainError(ErrorInvalidRecordID, "extraction_view_id %q must start with view:", extractionViewID)
	}
	sourceCtx, err := loadManualSourceContextByViewID(ctx, db, extractionViewID)
	if err != nil {
		return ExtractorInput{}, err
	}
	return sourceCtx.extractorInput(), nil
}

func submitExtractorOutput(ctx context.Context, db sqlDB, input ExtractorOutputInput) (IngestResult, error) {
	if input.RequestID == "" {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	if input.SourceSnapshotID == "" {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "source_snapshot_id is required")
	}
	if input.ExtractionViewID == "" {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "extraction_view_id is required")
	}
	sourceCtx, err := loadManualSourceContext(ctx, db, input.SourceSnapshotID, input.ExtractionViewID)
	if err != nil {
		return IngestResult{}, err
	}
	attemptCtx, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, input.RequestID, 0, input.ExtractorDefinition)
	if err != nil {
		return IngestResult{}, err
	}
	expectedOutputHash, err := extractorOutputHash(input.Output)
	if err != nil {
		return IngestResult{}, err
	}
	status, err := persistAttemptStartForExistingSource(ctx, db, attemptCtx)
	if err != nil {
		return IngestResult{}, err
	}
	switch status {
	case attemptStatusSucceeded:
		result, err := replaySucceededAttempt(ctx, db, attemptCtx, expectedOutputHash)
		if err != nil {
			return IngestResult{}, err
		}
		result.Replayed = true
		return result, nil
	case attemptStatusFailed:
		return IngestResult{}, newDomainError(ErrorPersistedAttemptFailed, "attempt %s already failed", attemptCtx.ExtractionAttempt.ID)
	}

	return completeAttemptWithOutput(ctx, db, attemptCtx, input.Output)
}

func ingestManualText(ctx context.Context, db sqlDB, input ManualTextInput, fixture FrozenExtractorOutput) (IngestResult, error) {
	attemptCtx, err := buildAttemptContext(input)
	if err != nil {
		return IngestResult{}, err
	}
	if err := persistSourceSnapshot(ctx, db, attemptCtx); err != nil {
		return IngestResult{}, err
	}
	status, err := persistAttemptStart(ctx, db, attemptCtx)
	if err != nil {
		return IngestResult{}, err
	}
	switch status {
	case attemptStatusSucceeded:
		expectedOutputHash, err := extractorOutputHash(fixture)
		if err != nil {
			return IngestResult{}, err
		}
		result, err := replaySucceededAttempt(ctx, db, attemptCtx, expectedOutputHash)
		if err != nil {
			return IngestResult{}, err
		}
		result.Replayed = true
		return result, nil
	case attemptStatusFailed:
		return IngestResult{}, newDomainError(ErrorPersistedAttemptFailed, "attempt %s already failed", attemptCtx.ExtractionAttempt.ID)
	}

	return completeAttemptWithOutput(ctx, db, attemptCtx, fixture)
}

func completeAttemptWithOutput(ctx context.Context, db sqlDB, attemptCtx attemptContext, output FrozenExtractorOutput) (IngestResult, error) {
	return completeAttemptWithRawOutput(ctx, db, attemptCtx, output, nil)
}

func completeAttemptWithRawOutput(ctx context.Context, db sqlDB, attemptCtx attemptContext, output FrozenExtractorOutput, rawOutput []byte) (IngestResult, error) {
	batch, err := materializeBatch(attemptCtx, output)
	if err != nil {
		failureCtx, cancel := detachedFailureContext(ctx)
		defer cancel()
		var persistErr error
		if rawOutput != nil {
			persistErr = persistAttemptFailureRawOutput(failureCtx, db, attemptCtx, output, rawOutput, err)
		} else {
			persistErr = persistAttemptFailure(failureCtx, db, attemptCtx, output, err)
		}
		if persistErr != nil {
			return IngestResult{}, fmt.Errorf("persisting attempt failure after %w: %v", err, persistErr)
		}
		return IngestResult{}, err
	}
	if err := persistProposalSuccess(ctx, db, batch, output); err != nil {
		return IngestResult{}, err
	}
	return ingestResultFromBatch(batch), nil
}

func ingestResultFromBatch(batch MaterializedBatch) IngestResult {
	result := IngestResult{
		ProposalCount:       len(batch.Occurrences),
		ExtractionAttemptID: batch.ExtractionAttempt.ID,
		ExtractionRunID:     batch.ExtractionRun.ID,
		SourceSnapshotID:    batch.SourceSnapshot.ID,
		ExtractionViewID:    batch.ExtractionView.ID,
	}
	if len(batch.Occurrences) != 0 {
		result.ProposalOccurrenceID = batch.Occurrences[0].ID
		result.ProposalFingerprint = batch.Occurrences[0].ProposalFingerprint
	}
	return result
}

func persistSourceSnapshot(ctx context.Context, db sqlDB, attemptCtx attemptContext) error {
	return withTx(ctx, db, func(tx sqlTx) error {
		_, err := persistSourceAuthorityTx(ctx, tx, attemptCtx.manualSourceContext)
		return err
	})
}

func persistManualSource(ctx context.Context, db sqlDB, sourceCtx manualSourceContext) (bool, error) {
	var changed bool
	err := withTx(ctx, db, func(tx sqlTx) error {
		sourceChanged, err := persistSourceAuthorityTx(ctx, tx, sourceCtx)
		if err != nil {
			return err
		}
		viewChanged, err := persistExtractionViewSpansTx(ctx, tx, sourceCtx)
		if err != nil {
			return err
		}
		requestChanged, err := persistSourceIntakeRequestTx(ctx, tx, sourceCtx)
		if err != nil {
			return err
		}
		changed = sourceChanged || viewChanged || requestChanged
		return nil
	})
	return changed, err
}

func persistSourceAuthorityTx(ctx context.Context, tx sqlTx, sourceCtx manualSourceContext) (bool, error) {
	changed := false
	tag, err := tx.exec(ctx, `
			INSERT INTO source_blobs (raw_content_hash, raw_content, byte_length)
			VALUES ($1, $2, $3)
			ON CONFLICT (raw_content_hash) DO NOTHING
		`, sourceCtx.SourceSnapshot.RawContentHash, sourceCtx.Raw, len(sourceCtx.Raw))
	if err != nil {
		return false, fmt.Errorf("upserting source blob: %w", err)
	}
	changed = changed || tag.RowsAffected() > 0
	origin, err := jsonBytes(sourceCtx.SourceSnapshot.OriginMetadata)
	if err != nil {
		return false, err
	}
	tag, err = tx.exec(ctx, `
			INSERT INTO source_snapshots (
				source_snapshot_id,
				source_system,
				source_id,
				source_version,
				raw_content_hash,
				origin_metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)
			ON CONFLICT (source_snapshot_id) DO NOTHING
		`,
		sourceCtx.SourceSnapshot.ID,
		sourceCtx.SourceSnapshot.SourceSystem,
		sourceCtx.SourceSnapshot.SourceID,
		sourceCtx.SourceSnapshot.SourceVersion,
		sourceCtx.SourceSnapshot.RawContentHash,
		string(origin),
	)
	if err != nil {
		return false, fmt.Errorf("upserting source snapshot: %w", err)
	}
	changed = changed || tag.RowsAffected() > 0
	return changed, nil
}

func persistSourceIntakeRequestTx(ctx context.Context, tx sqlTx, sourceCtx manualSourceContext) (bool, error) {
	tag, err := tx.exec(ctx, `
		INSERT INTO source_intake_requests (
			request_id,
			source_snapshot_id,
			extraction_view_id,
			request_payload_hash
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (request_id) DO NOTHING
	`,
		sourceCtx.RequestID,
		sourceCtx.SourceSnapshot.ID,
		sourceCtx.ExtractionView.ID,
		sourceCtx.RequestPayloadHash,
	)
	if err != nil {
		return false, fmt.Errorf("upserting source intake request: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var existingSnapshotID, existingViewID, existingPayloadHash string
	err = tx.queryRow(ctx, `
		SELECT source_snapshot_id, extraction_view_id, request_payload_hash
		FROM source_intake_requests
		WHERE request_id = $1
	`, sourceCtx.RequestID).Scan(&existingSnapshotID, &existingViewID, &existingPayloadHash)
	if err != nil {
		return false, fmt.Errorf("reading source intake request %s: %w", sourceCtx.RequestID, err)
	}
	if existingSnapshotID != sourceCtx.SourceSnapshot.ID || existingViewID != sourceCtx.ExtractionView.ID || existingPayloadHash != sourceCtx.RequestPayloadHash {
		return false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different source payload", sourceCtx.RequestID)
	}
	return false, nil
}

func persistAttemptStart(ctx context.Context, db sqlDB, attemptCtx attemptContext) (string, error) {
	return persistAttemptStartWithOptions(ctx, db, attemptCtx, true)
}

func persistAttemptStartForExistingSource(ctx context.Context, db sqlDB, attemptCtx attemptContext) (string, error) {
	return persistAttemptStartWithOptions(ctx, db, attemptCtx, false)
}

func persistAttemptStartWithOptions(ctx context.Context, db sqlDB, attemptCtx attemptContext, persistViewSpans bool) (string, error) {
	err := withTx(ctx, db, func(tx sqlTx) error {
		defConfig, err := jsonBytes(attemptCtx.ExtractorDefinition.Config)
		if err != nil {
			return err
		}
		if persistViewSpans {
			if _, err := persistExtractionViewSpansTx(ctx, tx, attemptCtx.manualSourceContext); err != nil {
				return err
			}
		}
		_, err = tx.exec(ctx, `
			INSERT INTO extractor_definitions (
				extractor_definition_id,
				extractor_name,
				extractor_version,
				extractor_config_hash,
				extractor_config
			)
			VALUES ($1, $2, $3, $4, $5::jsonb)
			ON CONFLICT (extractor_definition_id) DO NOTHING
		`,
			attemptCtx.ExtractorDefinition.ID,
			attemptCtx.ExtractorDefinition.Name,
			attemptCtx.ExtractorDefinition.Version,
			attemptCtx.ExtractorDefinition.ConfigHash,
			string(defConfig),
		)
		if err != nil {
			return fmt.Errorf("upserting extractor definition: %w", err)
		}
		_, err = tx.exec(ctx, `
			INSERT INTO extraction_runs (
				extraction_run_id,
				extractor_definition_id,
				source_snapshot_id,
				extraction_view_id,
				request_id
			)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (extraction_run_id) DO NOTHING
		`,
			attemptCtx.ExtractionRun.ID,
			attemptCtx.ExtractionRun.ExtractorDefinitionID,
			attemptCtx.ExtractionRun.SourceSnapshotID,
			attemptCtx.ExtractionRun.ExtractionViewID,
			attemptCtx.ExtractionRun.RequestID,
		)
		if err != nil {
			return fmt.Errorf("upserting extraction run: %w", err)
		}
		_, err = tx.exec(ctx, `
			INSERT INTO extraction_attempts (
				extraction_attempt_id,
				extraction_run_id,
				attempt_number,
				status
			)
			VALUES ($1, $2, $3, 'started')
			ON CONFLICT (extraction_attempt_id) DO NOTHING
		`,
			attemptCtx.ExtractionAttempt.ID,
			attemptCtx.ExtractionAttempt.RunID,
			attemptCtx.ExtractionAttempt.Number,
		)
		if err != nil {
			return fmt.Errorf("upserting extraction attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	var status string
	if err := db.queryRow(ctx, `
		SELECT status
		FROM extraction_attempts
		WHERE extraction_attempt_id = $1
	`, attemptCtx.ExtractionAttempt.ID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", newDomainError(ErrorMissingSourceViewAttempt, "attempt %s was not persisted", attemptCtx.ExtractionAttempt.ID)
		}
		return "", fmt.Errorf("reading attempt status: %w", err)
	}
	return status, nil
}

func persistExtractionViewSpansTx(ctx context.Context, tx sqlTx, sourceCtx manualSourceContext) (bool, error) {
	changed := false
	tag, err := tx.exec(ctx, `
		INSERT INTO extraction_views (
			extraction_view_id,
			source_snapshot_id,
			renderer_name,
			renderer_version,
			rendered_content,
			rendered_content_hash
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (extraction_view_id) DO NOTHING
	`,
		sourceCtx.ExtractionView.ID,
		sourceCtx.ExtractionView.SourceSnapshotID,
		sourceCtx.ExtractionView.RendererName,
		sourceCtx.ExtractionView.RendererVersion,
		sourceCtx.ExtractionView.Rendered,
		sourceCtx.ExtractionView.RenderedContentHash,
	)
	if err != nil {
		return false, fmt.Errorf("upserting extraction view: %w", err)
	}
	changed = changed || tag.RowsAffected() > 0
	for _, span := range sourceCtx.Spans {
		tag, err = tx.exec(ctx, `
			INSERT INTO span_catalog_entries (
				extraction_view_id,
				span_id,
				span_catalog_version,
				start_byte,
				end_byte,
				display_line,
				quoted_text_hash,
				quoted_text
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (extraction_view_id, span_id) DO NOTHING
		`,
			span.ExtractionViewID,
			span.SpanID,
			span.SpanCatalogVersion,
			span.StartByte,
			span.EndByte,
			span.DisplayLine,
			span.QuotedTextHash,
			[]byte(span.QuotedText),
		)
		if err != nil {
			return false, fmt.Errorf("inserting span %s: %w", span.SpanID, err)
		}
		changed = changed || tag.RowsAffected() > 0
	}
	return changed, nil
}

func loadManualSourceContext(ctx context.Context, db sqlQueryer, sourceSnapshotID, extractionViewID string) (manualSourceContext, error) {
	row := db.queryRow(ctx, `
		SELECT
			ss.source_snapshot_id,
			ss.source_system,
			ss.source_id,
			ss.source_version,
			ss.raw_content_hash,
			ss.origin_metadata,
			ev.extraction_view_id,
			ev.source_snapshot_id,
			ev.renderer_name,
			ev.renderer_version,
			ev.rendered_content,
			ev.rendered_content_hash,
			COALESCE(
				jsonb_agg(
					jsonb_build_object(
						'extraction_view_id', sce.extraction_view_id,
						'span_id', sce.span_id,
						'span_catalog_version', sce.span_catalog_version,
						'start_byte', sce.start_byte,
						'end_byte', sce.end_byte,
						'display_line', sce.display_line,
						'quoted_text_hash', sce.quoted_text_hash,
						'quoted_text', convert_from(sce.quoted_text, 'UTF8')
					)
					ORDER BY sce.start_byte, sce.span_id
				) FILTER (WHERE sce.span_id IS NOT NULL),
				'[]'::jsonb
			) AS spans
		FROM source_snapshots ss
		JOIN extraction_views ev
			ON ev.source_snapshot_id = ss.source_snapshot_id
		LEFT JOIN span_catalog_entries sce
			ON sce.extraction_view_id = ev.extraction_view_id
		WHERE ss.source_snapshot_id = $1
			AND ev.extraction_view_id = $2
		GROUP BY
			ss.source_snapshot_id,
			ss.source_system,
			ss.source_id,
			ss.source_version,
			ss.raw_content_hash,
			ss.origin_metadata,
			ev.extraction_view_id,
			ev.source_snapshot_id,
			ev.renderer_name,
			ev.renderer_version,
			ev.rendered_content,
			ev.rendered_content_hash
	`, sourceSnapshotID, extractionViewID)
	return scanManualSourceContext(row, func() error {
		return newDomainError(ErrorMissingSourceViewAttempt, "source snapshot %s and extraction view %s were not found", sourceSnapshotID, extractionViewID)
	})
}

func loadManualSourceContextByViewID(ctx context.Context, db sqlQueryer, extractionViewID string) (manualSourceContext, error) {
	row := db.queryRow(ctx, `
		SELECT
			ss.source_snapshot_id,
			ss.source_system,
			ss.source_id,
			ss.source_version,
			ss.raw_content_hash,
			ss.origin_metadata,
			ev.extraction_view_id,
			ev.source_snapshot_id,
			ev.renderer_name,
			ev.renderer_version,
			ev.rendered_content,
			ev.rendered_content_hash,
			COALESCE(
				jsonb_agg(
					jsonb_build_object(
						'extraction_view_id', sce.extraction_view_id,
						'span_id', sce.span_id,
						'span_catalog_version', sce.span_catalog_version,
						'start_byte', sce.start_byte,
						'end_byte', sce.end_byte,
						'display_line', sce.display_line,
						'quoted_text_hash', sce.quoted_text_hash,
						'quoted_text', convert_from(sce.quoted_text, 'UTF8')
					)
					ORDER BY sce.start_byte, sce.span_id
				) FILTER (WHERE sce.span_id IS NOT NULL),
				'[]'::jsonb
			) AS spans
		FROM extraction_views ev
		JOIN source_snapshots ss
			ON ss.source_snapshot_id = ev.source_snapshot_id
		LEFT JOIN span_catalog_entries sce
			ON sce.extraction_view_id = ev.extraction_view_id
		WHERE ev.extraction_view_id = $1
		GROUP BY
			ss.source_snapshot_id,
			ss.source_system,
			ss.source_id,
			ss.source_version,
			ss.raw_content_hash,
			ss.origin_metadata,
			ev.extraction_view_id,
			ev.source_snapshot_id,
			ev.renderer_name,
			ev.renderer_version,
			ev.rendered_content,
			ev.rendered_content_hash
	`, extractionViewID)
	return scanManualSourceContext(row, func() error {
		return newDomainError(ErrorMissingSourceViewAttempt, "extraction view %s was not found", extractionViewID)
	})
}

func scanManualSourceContext(row sqlRow, missing func() error) (manualSourceContext, error) {
	var snapshot SourceSnapshot
	var view ExtractionView
	var originMetadataData []byte
	var spansData []byte
	err := row.Scan(
		&snapshot.ID,
		&snapshot.SourceSystem,
		&snapshot.SourceID,
		&snapshot.SourceVersion,
		&snapshot.RawContentHash,
		&originMetadataData,
		&view.ID,
		&view.SourceSnapshotID,
		&view.RendererName,
		&view.RendererVersion,
		&view.Rendered,
		&view.RenderedContentHash,
		&spansData,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return manualSourceContext{}, missing()
		}
		return manualSourceContext{}, fmt.Errorf("loading source view: %w", err)
	}
	if err := json.Unmarshal(originMetadataData, &snapshot.OriginMetadata); err != nil {
		return manualSourceContext{}, fmt.Errorf("decoding origin metadata: %w", err)
	}
	var spans []SpanEntry
	if err := json.Unmarshal(spansData, &spans); err != nil {
		return manualSourceContext{}, fmt.Errorf("decoding span catalog: %w", err)
	}
	contract, err := renderingContract(snapshot.SourceSystem)
	if err != nil {
		return manualSourceContext{}, err
	}
	if view.RendererName != contract.RendererName || view.RendererVersion != contract.RendererVersion {
		return manualSourceContext{}, newDomainError(ErrorInvalidInput, "renderer %s/%s is not supported", view.RendererName, view.RendererVersion)
	}
	if got := contentHash(view.Rendered); got != view.RenderedContentHash {
		return manualSourceContext{}, newDomainError(ErrorQuotedHashMismatch, "rendered content hash %s, want %s", view.RenderedContentHash, got)
	}
	if snapshot.RawContentHash != view.RenderedContentHash {
		return manualSourceContext{}, newDomainError(ErrorQuotedHashMismatch, "identity view hash %s does not match raw source hash %s", view.RenderedContentHash, snapshot.RawContentHash)
	}
	for _, span := range spans {
		if span.SpanCatalogVersion != contract.SpanCatalogVersion {
			return manualSourceContext{}, newDomainError(ErrorInvalidInput, "span catalog %q is not supported for source system %q", span.SpanCatalogVersion, snapshot.SourceSystem)
		}
		if err := validateSpanBounds(view, span); err != nil {
			return manualSourceContext{}, err
		}
	}
	return manualSourceContext{
		SourceSnapshot:     snapshot,
		ExtractionView:     view,
		SpanCatalogVersion: contract.SpanCatalogVersion,
		Spans:              spans,
	}, nil
}

func persistProposalSuccess(ctx context.Context, db sqlDB, batch MaterializedBatch, fixture FrozenExtractorOutput) error {
	return persistProposalSuccessWithHook(ctx, db, batch, fixture, nil)
}

type proposalSuccessHook func(context.Context, sqlTx) error

func persistProposalSuccessWithHook(ctx context.Context, db sqlDB, batch MaterializedBatch, fixture FrozenExtractorOutput, hook proposalSuccessHook) error {
	return withTx(ctx, db, func(tx sqlTx) error {
		fixtureData, err := jsonBytes(fixture)
		if err != nil {
			return err
		}
		_, err = tx.exec(ctx, `
			UPDATE extraction_attempts
			SET status = 'succeeded',
				output_hash = $2,
				fixture_output = $3::jsonb,
				failure_class = NULL,
				failure_metadata = NULL,
				completed_at = now()
			WHERE extraction_attempt_id = $1
		`, batch.ExtractionAttempt.ID, batch.FixtureOutputHash, string(fixtureData))
		if err != nil {
			return fmt.Errorf("updating attempt success: %w", err)
		}
		_, err = tx.exec(ctx, `
			INSERT INTO proposal_batches (
				proposal_batch_id,
				extraction_attempt_id,
				status,
				proposal_count,
				completed_at
			)
			VALUES ($1, $2, 'completed', $3, now())
			ON CONFLICT (proposal_batch_id) DO UPDATE
			SET status = 'completed',
				proposal_count = EXCLUDED.proposal_count,
				completed_at = now()
		`, batch.ProposalBatch.ID, batch.ExtractionAttempt.ID, len(batch.Occurrences))
		if err != nil {
			return fmt.Errorf("upserting proposal batch: %w", err)
		}
		for _, occurrence := range batch.Occurrences {
			if err := insertOccurrence(ctx, tx, occurrence); err != nil {
				return err
			}
		}
		if hook != nil {
			if err := hook(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertOccurrence(ctx context.Context, tx sqlTx, occurrence ProposalOccurrence) error {
	sourceRefs, err := jsonBytes(occurrence.SourceRefs)
	if err != nil {
		return err
	}
	payload, err := jsonBytes(occurrence.ProposedPayload)
	if err != nil {
		return err
	}
	tag, err := tx.exec(ctx, `
		INSERT INTO proposal_occurrences (
			proposal_occurrence_id,
			proposal_batch_id,
			extraction_attempt_id,
			proposal_local_id,
			proposal_kind,
			statement_text,
			proposal_fingerprint,
			proposal_fingerprint_version,
			admission_outcome,
			canonical_ref,
			source_refs,
			proposed_payload
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', NULL, $9::jsonb, $10::jsonb)
		ON CONFLICT (proposal_occurrence_id) DO NOTHING
	`,
		occurrence.ID,
		occurrence.BatchID,
		occurrence.ExtractionAttemptID,
		occurrence.ProposalLocalID,
		occurrence.ProposalKind,
		occurrence.StatementText,
		occurrence.ProposalFingerprint,
		occurrence.ProposalFingerprintVersion,
		string(sourceRefs),
		string(payload),
	)
	if err != nil {
		return fmt.Errorf("inserting proposal occurrence %s: %w", occurrence.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var existingFingerprint, existingStatement string
	err = tx.queryRow(ctx, `
		SELECT proposal_fingerprint, statement_text
		FROM proposal_occurrences
		WHERE proposal_occurrence_id = $1
	`, occurrence.ID).Scan(&existingFingerprint, &existingStatement)
	if err != nil {
		return fmt.Errorf("reading existing occurrence %s: %w", occurrence.ID, err)
	}
	if existingFingerprint != occurrence.ProposalFingerprint || existingStatement != occurrence.StatementText {
		return newDomainError(ErrorOccurrenceConflict, "proposal occurrence %s already exists with different content", occurrence.ID)
	}
	return nil
}

func persistAttemptFailure(ctx context.Context, db sqlDB, attemptCtx attemptContext, fixture FrozenExtractorOutput, cause error) error {
	fixtureData, err := jsonBytes(fixture)
	if err != nil {
		return err
	}
	outputHash := contentHash(fixtureData)
	fixtureOutput := string(fixtureData)
	return persistAttemptFailureRecord(ctx, db, attemptCtx, &outputHash, &fixtureOutput, cause)
}

func persistAttemptFailureRawOutput(ctx context.Context, db sqlDB, attemptCtx attemptContext, fixture FrozenExtractorOutput, rawOutput []byte, cause error) error {
	fixtureData, err := jsonBytes(fixture)
	if err != nil {
		return err
	}
	outputHash := contentHash(rawOutput)
	fixtureOutput := string(fixtureData)
	return persistAttemptFailureRecord(ctx, db, attemptCtx, &outputHash, &fixtureOutput, cause)
}

func persistAttemptFailureBytes(ctx context.Context, db sqlDB, attemptCtx attemptContext, output []byte, cause error) error {
	var outputHash *string
	if output != nil {
		hash := contentHash(output)
		outputHash = &hash
	}
	return persistAttemptFailureRecord(ctx, db, attemptCtx, outputHash, nil, cause)
}

func persistAttemptFailureRecord(ctx context.Context, db sqlDB, attemptCtx attemptContext, outputHash, fixtureOutput *string, cause error) error {
	return withTx(ctx, db, func(tx sqlTx) error {
		kind, ok := KindOf(cause)
		if !ok {
			kind = ErrorInvalidInput
		}
		metadata, err := jsonBytes(map[string]string{
			"kind":  string(kind),
			"error": cause.Error(),
		})
		if err != nil {
			return err
		}
		var outputHashArg any
		if outputHash != nil {
			outputHashArg = *outputHash
		}
		var fixtureOutputArg any
		if fixtureOutput != nil {
			fixtureOutputArg = *fixtureOutput
		}
		_, err = tx.exec(ctx, `
			UPDATE extraction_attempts
			SET status = 'failed',
				output_hash = $2,
				fixture_output = $3::jsonb,
				failure_class = $4,
				failure_metadata = $5::jsonb,
				completed_at = now()
			WHERE extraction_attempt_id = $1
		`,
			attemptCtx.ExtractionAttempt.ID,
			outputHashArg,
			fixtureOutputArg,
			string(kind),
			string(metadata),
		)
		if err != nil {
			return fmt.Errorf("updating attempt failure: %w", err)
		}
		return nil
	})
}

func replaySucceededAttempt(ctx context.Context, db sqlDB, attemptCtx attemptContext, expectedOutputHash string) (IngestResult, error) {
	var occurrenceID, fingerprint, outputHash string
	var proposalCount int
	err := db.queryRow(ctx, `
		SELECT
			COALESCE(first_occurrence.proposal_occurrence_id, ''),
			COALESCE(first_occurrence.proposal_fingerprint, ''),
			ea.output_hash,
			pb.proposal_count
		FROM extraction_attempts ea
		JOIN proposal_batches pb ON pb.extraction_attempt_id = ea.extraction_attempt_id
		LEFT JOIN LATERAL (
			SELECT po.proposal_occurrence_id, po.proposal_fingerprint
			FROM proposal_occurrences po
			WHERE po.extraction_attempt_id = ea.extraction_attempt_id
			ORDER BY po.proposal_local_id
			LIMIT 1
		) first_occurrence ON true
		WHERE ea.extraction_attempt_id = $1
			AND ea.status = 'succeeded'
			AND pb.status = 'completed'
	`, attemptCtx.ExtractionAttempt.ID).Scan(&occurrenceID, &fingerprint, &outputHash, &proposalCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IngestResult{}, newDomainError(ErrorMissingSourceViewAttempt, "succeeded attempt %s has no completed proposal batch", attemptCtx.ExtractionAttempt.ID)
		}
		return IngestResult{}, fmt.Errorf("reading replayed attempt: %w", err)
	}
	if expectedOutputHash != "" && outputHash != expectedOutputHash {
		return IngestResult{}, newDomainError(ErrorIdempotencyKeyReused, "attempt %s already succeeded with different extractor output", attemptCtx.ExtractionAttempt.ID)
	}
	return IngestResult{
		ProposalOccurrenceID: occurrenceID,
		ProposalFingerprint:  fingerprint,
		ProposalCount:        proposalCount,
		ExtractionAttemptID:  attemptCtx.ExtractionAttempt.ID,
		ExtractionRunID:      attemptCtx.ExtractionRun.ID,
		SourceSnapshotID:     attemptCtx.SourceSnapshot.ID,
		ExtractionViewID:     attemptCtx.ExtractionView.ID,
	}, nil
}

// GetProposalByOccurrenceID returns a pending Slice 1 proposal and provenance.
func GetProposalByOccurrenceID(ctx context.Context, pool *pgxpool.Pool, occurrenceID string) (ProposalQueryResult, error) {
	if pool == nil {
		return ProposalQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getProposalByOccurrenceID(ctx, pgxDB{pool: pool}, occurrenceID)
}

func getProposalByOccurrenceID(ctx context.Context, db sqlQueryer, occurrenceID string) (ProposalQueryResult, error) {
	row := db.queryRow(ctx, `
		SELECT
			po.proposal_occurrence_id,
			po.proposal_fingerprint,
			po.proposal_fingerprint_version,
			po.proposal_kind,
			po.statement_text,
			po.admission_outcome,
			po.canonical_ref,
			po.source_refs,
			po.proposed_payload,
			ea.extraction_attempt_id,
			ea.status,
			er.extraction_run_id,
			ed.extractor_definition_id,
			ed.extractor_name,
			ed.extractor_version,
			ed.extractor_config_hash,
			COALESCE(ev.extraction_view_id, ''),
			COALESCE(ev.renderer_name, ''),
			COALESCE(ev.renderer_version, ''),
			COALESCE(ev.rendered_content_hash, ''),
			COALESCE(ss.source_snapshot_id, ''),
			COALESCE(ss.source_system, ''),
			COALESCE(ss.source_id, ''),
			COALESCE(ss.source_version, ''),
			COALESCE(ss.raw_content_hash, ''),
			COALESCE(ss.origin_metadata, '{}'::jsonb),
			COALESCE(rs.repository_snapshot_id, ''),
			COALESCE(rs.repo_id, ''),
			COALESCE(rs.commit_sha, ''),
			COALESCE(rs.manifest_hash, ''),
			COALESCE(rs.manifest_entry_count, 0),
			COALESCE(rs.revision_verification_method, ''),
			COALESCE(rs.manifest_contract, ''),
			COALESCE(rs.file_selection_contract, ''),
			COALESCE(rs.selected_file_count, 0),
			COALESCE(rsg.source_generation_id, ''),
			COALESCE(rsg.repo_id, ''),
			COALESCE(rsg.extractor_name, ''),
			COALESCE(rsg.extractor_definition_id, ''),
			COALESCE(rsg.generation_number, 0),
			COALESCE(rsg.repository_snapshot_id, ''),
			COALESCE(rsg.commit_sha, ''),
			COALESCE(rsg.proposal_batch_id, ''),
			COALESCE(rsg.extractor_output_hash, ''),
			COALESCE(rsg.proposal_count, 0),
			(rsh.active_generation_id IS NOT NULL),
			COALESCE(rgpr.source_generation_id, ''),
			COALESCE(rgpr.lifecycle_state, ''),
			COALESCE(rgr.identity_contract, ''),
			COALESCE(rgpr.proposal_identity, ''),
			COALESCE(rgpr.current_proposal_occurrence_id, ''),
			COALESCE(rgpr.previous_proposal_occurrence_id, '')
		FROM proposal_occurrences po
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		JOIN extractor_definitions ed ON ed.extractor_definition_id = er.extractor_definition_id
		LEFT JOIN extraction_views ev ON ev.extraction_view_id = er.extraction_view_id
		LEFT JOIN source_snapshots ss ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		LEFT JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
		LEFT JOIN repository_source_heads rsh
			ON rsh.repo_id = rsg.repo_id
			AND rsh.extractor_name = rsg.extractor_name
			AND rsh.active_generation_id = rsg.source_generation_id
		LEFT JOIN repository_source_heads stream_head
			ON stream_head.repo_id = rsg.repo_id
			AND stream_head.extractor_name = rsg.extractor_name
		LEFT JOIN repository_generation_proposal_reconciliations rgpr
			ON rgpr.source_generation_id = stream_head.active_generation_id
			AND (
				rgpr.current_proposal_occurrence_id = po.proposal_occurrence_id
				OR rgpr.previous_proposal_occurrence_id = po.proposal_occurrence_id
			)
		LEFT JOIN repository_generation_reconciliations rgr
			ON rgr.source_generation_id = rgpr.source_generation_id
		WHERE po.proposal_occurrence_id = $1
	`, occurrenceID)
	result, err := scanProposalQueryResult(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProposalQueryResult{}, newDomainError(ErrorMissingSourceViewAttempt, "proposal occurrence %s not found", occurrenceID)
		}
		return ProposalQueryResult{}, fmt.Errorf("querying proposal occurrence: %w", err)
	}
	return result, nil
}

// ListProposalRecords returns bounded proposal records under an explicit lifecycle scope.
func ListProposalRecords(ctx context.Context, pool *pgxpool.Pool, input ProposalListInput) ([]ProposalQueryResult, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listProposalRecords(ctx, pgxDB{pool: pool}, input)
}

func listProposalRecords(ctx context.Context, db sqlQueryer, input ProposalListInput) ([]ProposalQueryResult, error) {
	input = normalizeProposalListInput(input)
	if err := validateProposalListInput(input); err != nil {
		return nil, err
	}
	rows, err := db.query(ctx, `
		SELECT
			po.proposal_occurrence_id,
			po.proposal_fingerprint,
			po.proposal_fingerprint_version,
			po.proposal_kind,
			po.statement_text,
			po.admission_outcome,
			po.canonical_ref,
			po.source_refs,
			po.proposed_payload,
			ea.extraction_attempt_id,
			ea.status,
			er.extraction_run_id,
			ed.extractor_definition_id,
			ed.extractor_name,
			ed.extractor_version,
			ed.extractor_config_hash,
			COALESCE(ev.extraction_view_id, ''),
			COALESCE(ev.renderer_name, ''),
			COALESCE(ev.renderer_version, ''),
			COALESCE(ev.rendered_content_hash, ''),
			COALESCE(ss.source_snapshot_id, ''),
			COALESCE(ss.source_system, ''),
			COALESCE(ss.source_id, ''),
			COALESCE(ss.source_version, ''),
			COALESCE(ss.raw_content_hash, ''),
			COALESCE(ss.origin_metadata, '{}'::jsonb),
			COALESCE(rs.repository_snapshot_id, ''),
			COALESCE(rs.repo_id, ''),
			COALESCE(rs.commit_sha, ''),
			COALESCE(rs.manifest_hash, ''),
			COALESCE(rs.manifest_entry_count, 0),
			COALESCE(rs.revision_verification_method, ''),
			COALESCE(rs.manifest_contract, ''),
			COALESCE(rs.file_selection_contract, ''),
			COALESCE(rs.selected_file_count, 0),
			COALESCE(rsg.source_generation_id, ''),
			COALESCE(rsg.repo_id, ''),
			COALESCE(rsg.extractor_name, ''),
			COALESCE(rsg.extractor_definition_id, ''),
			COALESCE(rsg.generation_number, 0),
			COALESCE(rsg.repository_snapshot_id, ''),
			COALESCE(rsg.commit_sha, ''),
			COALESCE(rsg.proposal_batch_id, ''),
			COALESCE(rsg.extractor_output_hash, ''),
			COALESCE(rsg.proposal_count, 0),
			(rsh.active_generation_id IS NOT NULL),
			COALESCE(rgpr.source_generation_id, ''),
			COALESCE(rgpr.lifecycle_state, ''),
			COALESCE(rgr.identity_contract, ''),
			COALESCE(rgpr.proposal_identity, ''),
			COALESCE(rgpr.current_proposal_occurrence_id, ''),
			COALESCE(rgpr.previous_proposal_occurrence_id, '')
		FROM proposal_occurrences po
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		JOIN extractor_definitions ed ON ed.extractor_definition_id = er.extractor_definition_id
		LEFT JOIN extraction_views ev ON ev.extraction_view_id = er.extraction_view_id
		LEFT JOIN source_snapshots ss ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		LEFT JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
		LEFT JOIN repository_source_heads rsh
			ON rsh.repo_id = rsg.repo_id
			AND rsh.extractor_name = rsg.extractor_name
			AND rsh.active_generation_id = rsg.source_generation_id
		LEFT JOIN repository_source_heads stream_head
			ON stream_head.repo_id = rsg.repo_id
			AND stream_head.extractor_name = rsg.extractor_name
		LEFT JOIN repository_generation_proposal_reconciliations rgpr
			ON rgpr.source_generation_id = stream_head.active_generation_id
			AND (
				rgpr.current_proposal_occurrence_id = po.proposal_occurrence_id
				OR rgpr.previous_proposal_occurrence_id = po.proposal_occurrence_id
			)
		LEFT JOIN repository_generation_reconciliations rgr
			ON rgr.source_generation_id = rgpr.source_generation_id
		WHERE ($1 = '' OR ss.source_snapshot_id = $1)
			AND ($2 = '' OR rs.repository_snapshot_id = $2)
			AND ($3 = '' OR ss.source_id = $3)
			AND ($4 = '' OR ss.source_version = $4)
			AND ($5 = '' OR po.admission_outcome = $5)
			AND ($6 = '' OR rsg.source_generation_id = $6)
			AND (
				$7 = 'all'
				OR ($7 = 'active' AND (er.repository_snapshot_id IS NULL OR rsh.active_generation_id IS NOT NULL))
				OR ($7 = 'historical' AND er.repository_snapshot_id IS NOT NULL AND rsg.source_generation_id IS NOT NULL AND rsh.active_generation_id IS NULL)
			)
		ORDER BY po.created_at DESC, po.proposal_occurrence_id
		LIMIT $8
	`,
		input.SourceSnapshotID,
		input.RepositorySnapshotID,
		input.SourceID,
		input.SourceVersion,
		input.AdmissionOutcome,
		input.SourceGenerationID,
		input.LifecycleScope,
		input.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying proposal records: %w", err)
	}
	defer rows.Close()

	var results []ProposalQueryResult
	for rows.Next() {
		result, err := scanProposalQueryResult(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning proposal record: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating proposal records: %w", err)
	}
	return results, nil
}

func normalizeProposalListInput(input ProposalListInput) ProposalListInput {
	input.SourceSnapshotID = strings.TrimSpace(input.SourceSnapshotID)
	input.RepositorySnapshotID = strings.TrimSpace(input.RepositorySnapshotID)
	input.SourceGenerationID = strings.TrimSpace(input.SourceGenerationID)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.SourceVersion = strings.TrimSpace(input.SourceVersion)
	input.AdmissionOutcome = strings.TrimSpace(input.AdmissionOutcome)
	input.LifecycleScope = strings.TrimSpace(input.LifecycleScope)
	if input.LifecycleScope == "" {
		input.LifecycleScope = ProposalLifecycleScopeActive
		if input.SourceGenerationID != "" {
			input.LifecycleScope = ProposalLifecycleScopeAll
		}
	}
	if input.Limit == 0 {
		input.Limit = 20
	}
	return input
}

func validateProposalListInput(input ProposalListInput) error {
	if input.SourceSnapshotID != "" && !strings.HasPrefix(input.SourceSnapshotID, "srcsnap:") {
		return newDomainError(ErrorInvalidRecordID, "source_snapshot_id %q must start with srcsnap:", input.SourceSnapshotID)
	}
	if input.RepositorySnapshotID != "" && !strings.HasPrefix(input.RepositorySnapshotID, "repo-snapshot:") {
		return newDomainError(ErrorInvalidRecordID, "repository_snapshot_id %q must start with repo-snapshot:", input.RepositorySnapshotID)
	}
	if input.SourceGenerationID != "" && !strings.HasPrefix(input.SourceGenerationID, "generation:") {
		return newDomainError(ErrorInvalidRecordID, "source_generation_id %q must start with generation:", input.SourceGenerationID)
	}
	if input.SourceSnapshotID != "" && input.RepositorySnapshotID != "" {
		return newDomainError(ErrorInvalidInput, "source_snapshot_id and repository_snapshot_id are mutually exclusive")
	}
	if input.SourceGenerationID != "" && (input.SourceSnapshotID != "" || input.SourceID != "" || input.SourceVersion != "") {
		return newDomainError(ErrorInvalidInput, "source_generation_id cannot be combined with source snapshot or source identity filters")
	}
	if input.Limit < 0 {
		return newDomainError(ErrorInvalidInput, "limit must be non-negative")
	}
	if input.Limit > 100 {
		return newDomainError(ErrorInvalidInput, "limit must be at most 100")
	}
	if err := validateProposalLifecycleScope(input.LifecycleScope); err != nil {
		return err
	}
	switch input.AdmissionOutcome {
	case "", admissionOutcomePending, admissionOutcomeRejected, admissionOutcomeAuditOnly, admissionOutcomeAdmitted:
		return nil
	default:
		return newDomainError(ErrorInvalidInput, "admission_outcome %q is not supported", input.AdmissionOutcome)
	}
}

func validateProposalLifecycleScope(scope string) error {
	switch scope {
	case ProposalLifecycleScopeActive, ProposalLifecycleScopeHistorical, ProposalLifecycleScopeAll:
		return nil
	default:
		return newDomainError(ErrorInvalidInput, "lifecycle_scope %q is not supported", scope)
	}
}

func scanProposalQueryResult(row sqlRow) (ProposalQueryResult, error) {
	var result ProposalQueryResult
	var repositorySnapshot RepositorySnapshot
	var sourceGeneration RepositorySourceGeneration
	var repositoryLifecycle RepositoryProposalLifecycle
	var canonicalRef sql.NullString
	var sourceRefsData []byte
	var proposedPayloadData []byte
	var originMetadataData []byte
	if err := row.Scan(
		&result.ProposalOccurrenceID,
		&result.ProposalFingerprint,
		&result.ProposalFingerprintVersion,
		&result.ProposalKind,
		&result.StatementText,
		&result.AdmissionOutcome,
		&canonicalRef,
		&sourceRefsData,
		&proposedPayloadData,
		&result.ExtractionAttemptID,
		&result.ExtractionAttemptStatus,
		&result.ExtractionRunID,
		&result.ExtractorDefinitionID,
		&result.ExtractorName,
		&result.ExtractorVersion,
		&result.ExtractorConfigHash,
		&result.ExtractionViewID,
		&result.RendererName,
		&result.RendererVersion,
		&result.RenderedContentHash,
		&result.SourceSnapshotID,
		&result.SourceSystem,
		&result.SourceID,
		&result.SourceVersion,
		&result.RawContentHash,
		&originMetadataData,
		&repositorySnapshot.ID,
		&repositorySnapshot.RepoID,
		&repositorySnapshot.CommitSHA,
		&repositorySnapshot.ManifestHash,
		&repositorySnapshot.ManifestEntryCount,
		&repositorySnapshot.RevisionVerificationMethod,
		&repositorySnapshot.ManifestContract,
		&repositorySnapshot.FileSelectionContract,
		&repositorySnapshot.SelectedFileCount,
		&sourceGeneration.ID,
		&sourceGeneration.RepoID,
		&sourceGeneration.ExtractorName,
		&sourceGeneration.ExtractorDefinitionID,
		&sourceGeneration.Number,
		&sourceGeneration.RepositorySnapshotID,
		&sourceGeneration.CommitSHA,
		&sourceGeneration.ProposalBatchID,
		&sourceGeneration.ExtractorOutputHash,
		&sourceGeneration.ProposalCount,
		&result.SourceGenerationActive,
		&repositoryLifecycle.SourceGenerationID,
		&repositoryLifecycle.State,
		&repositoryLifecycle.IdentityContract,
		&repositoryLifecycle.ProposalIdentity,
		&repositoryLifecycle.CurrentProposalOccurrenceID,
		&repositoryLifecycle.PreviousProposalOccurrenceID,
	); err != nil {
		return ProposalQueryResult{}, err
	}
	if canonicalRef.Valid {
		result.CanonicalRef = canonicalRef.String
	}
	if err := json.Unmarshal(sourceRefsData, &result.SourceRefs); err != nil {
		return ProposalQueryResult{}, fmt.Errorf("decoding source refs: %w", err)
	}
	if err := decodeProposalCodePayload(proposedPayloadData, &result); err != nil {
		return ProposalQueryResult{}, err
	}
	if err := json.Unmarshal(originMetadataData, &result.OriginMetadata); err != nil {
		return ProposalQueryResult{}, fmt.Errorf("decoding origin metadata: %w", err)
	}
	if err := resolveProposalQuerySourceBinding(&result, repositorySnapshot); err != nil {
		return ProposalQueryResult{}, err
	}
	if sourceGeneration.ID != "" {
		sourceGeneration.ExtractionAttemptID = result.ExtractionAttemptID
		result.SourceGeneration = &sourceGeneration
	}
	if repositoryLifecycle.State != "" {
		result.RepositoryLifecycle = &repositoryLifecycle
	}
	if err := validateProposalQuerySourceGeneration(result); err != nil {
		return ProposalQueryResult{}, err
	}
	return result, nil
}

func validateProposalQuerySourceGeneration(result ProposalQueryResult) error {
	if result.SourceGeneration == nil {
		if result.SourceGenerationActive || result.RepositoryLifecycle != nil {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "proposal %s has repository lifecycle without a source generation", result.ProposalOccurrenceID)
		}
		return nil
	}
	generation := result.SourceGeneration
	if result.RepositorySnapshot == nil ||
		generation.RepositorySnapshotID != result.RepositorySnapshot.ID ||
		generation.RepoID != result.RepositorySnapshot.RepoID ||
		generation.CommitSHA != result.RepositorySnapshot.CommitSHA ||
		generation.ExtractionAttemptID != result.ExtractionAttemptID ||
		generation.ExtractorDefinitionID != result.ExtractorDefinitionID ||
		generation.ExtractorName != result.ExtractorName {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "proposal %s source generation does not match repository extraction provenance", result.ProposalOccurrenceID)
	}
	return validateRepositoryProposalLifecycle(result)
}

func validateRepositoryProposalLifecycle(result ProposalQueryResult) error {
	lifecycle := result.RepositoryLifecycle
	if lifecycle == nil {
		if result.SourceGenerationActive {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "active repository proposal %s has no generation reconciliation", result.ProposalOccurrenceID)
		}
		return nil
	}
	if lifecycle.SourceGenerationID == "" || lifecycle.IdentityContract != RepositoryProposalIdentityContractV1 || lifecycle.ProposalIdentity == "" {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s has invalid generation reconciliation metadata", result.ProposalOccurrenceID)
	}
	matchesCurrent := lifecycle.CurrentProposalOccurrenceID == result.ProposalOccurrenceID
	matchesPrevious := lifecycle.PreviousProposalOccurrenceID == result.ProposalOccurrenceID
	switch lifecycle.State {
	case RepositoryProposalLifecycleNew:
		if !matchesCurrent || lifecycle.PreviousProposalOccurrenceID != "" || !result.SourceGenerationActive || lifecycle.SourceGenerationID != result.SourceGeneration.ID {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s has invalid new lifecycle binding", result.ProposalOccurrenceID)
		}
	case RepositoryProposalLifecycleUnchanged:
		if lifecycle.CurrentProposalOccurrenceID == "" || lifecycle.PreviousProposalOccurrenceID == "" || matchesCurrent == matchesPrevious {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s has invalid unchanged lifecycle binding", result.ProposalOccurrenceID)
		}
		if result.SourceGenerationActive != matchesCurrent {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s unchanged lifecycle does not match active generation", result.ProposalOccurrenceID)
		}
	case RepositoryProposalLifecycleStale:
		if lifecycle.CurrentProposalOccurrenceID != "" || !matchesPrevious || result.SourceGenerationActive {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s has invalid stale lifecycle binding", result.ProposalOccurrenceID)
		}
	default:
		return newDomainError(ErrorRepositorySnapshotIntegrity, "repository proposal %s has unsupported lifecycle state %q", result.ProposalOccurrenceID, lifecycle.State)
	}
	return nil
}

func resolveProposalQuerySourceBinding(result *ProposalQueryResult, repositorySnapshot RepositorySnapshot) error {
	hasSourceBinding := result.SourceSnapshotID != "" || result.ExtractionViewID != ""
	hasRepositoryBinding := repositorySnapshot.ID != ""
	switch {
	case hasSourceBinding && !hasRepositoryBinding:
		if result.SourceSnapshotID == "" || result.ExtractionViewID == "" {
			return newDomainError(ErrorMissingSourceViewAttempt, "proposal %s has an incomplete source snapshot binding", result.ProposalOccurrenceID)
		}
		for _, ref := range result.SourceRefs {
			if ref.ExtractionViewID != result.ExtractionViewID || ref.RepositorySnapshotID != "" || ref.FileSnapshotID != "" {
				return newDomainError(ErrorMissingSourceViewAttempt, "proposal %s source ref %s does not match extraction view %s", result.ProposalOccurrenceID, ref.SpanID, result.ExtractionViewID)
			}
		}
		result.SourceBindingKind = ProposalSourceBindingSourceSnapshot
		return nil
	case !hasSourceBinding && hasRepositoryBinding:
		for _, ref := range result.SourceRefs {
			if ref.TargetKind != "file_snapshot" || ref.ExtractionViewID != "" || ref.RepositorySnapshotID != repositorySnapshot.ID || ref.FileSnapshotID == "" || ref.RepoID != repositorySnapshot.RepoID || ref.CommitSHA != repositorySnapshot.CommitSHA || ref.Path == "" {
				return newDomainError(ErrorRepositorySnapshotIntegrity, "proposal %s source ref %s does not match repository snapshot %s", result.ProposalOccurrenceID, ref.SpanID, repositorySnapshot.ID)
			}
		}
		result.SourceBindingKind = ProposalSourceBindingRepositorySnapshot
		result.RepositorySnapshot = &repositorySnapshot
		return nil
	default:
		return newDomainError(ErrorRepositorySnapshotIntegrity, "proposal %s must have exactly one source or repository binding", result.ProposalOccurrenceID)
	}
}

func decodeProposalCodePayload(proposedPayloadData []byte, result *ProposalQueryResult) error {
	var payload struct {
		ProposalLocalID string                `json:"proposal_local_id"`
		CodeFact        *ResolvedCodeFact     `json:"code_fact"`
		CodeRelation    *ResolvedCodeRelation `json:"code_relation"`
	}
	if err := json.Unmarshal(proposedPayloadData, &payload); err != nil {
		return fmt.Errorf("decoding proposed payload: %w", err)
	}
	result.ProposalLocalID = payload.ProposalLocalID
	result.CodeFact = payload.CodeFact
	result.CodeRelation = payload.CodeRelation
	return nil
}

// TraceProposalProvenance returns the same provenance-rich Slice 1 query shape.
func TraceProposalProvenance(ctx context.Context, pool *pgxpool.Pool, occurrenceID string) (ProposalQueryResult, error) {
	if pool == nil {
		return ProposalQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return traceProposalProvenance(ctx, pgxDB{pool: pool}, occurrenceID)
}

func traceProposalProvenance(ctx context.Context, db sqlQueryer, occurrenceID string) (ProposalQueryResult, error) {
	return getProposalByOccurrenceID(ctx, db, occurrenceID)
}

func withTx(ctx context.Context, db sqlDB, fn func(sqlTx) error) error {
	tx, err := db.begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.rollback(ctx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func jsonBytes(value any) ([]byte, error) {
	data, err := deterministicJSON(value)
	if err != nil {
		return nil, fmt.Errorf("serializing JSON: %w", err)
	}
	return data, nil
}
