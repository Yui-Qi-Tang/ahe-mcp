package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateRepositoryExtractionRun records a run bound to an immutable repository snapshot.
// It intentionally does not start an extraction attempt; multi-file execution is a separate step.
func CreateRepositoryExtractionRun(ctx context.Context, pool *pgxpool.Pool, request RepositoryExtractionRunRequest) (RepositoryExtractionRunResult, error) {
	if pool == nil {
		return RepositoryExtractionRunResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return createRepositoryExtractionRun(ctx, pgxDB{pool: pool}, request)
}

func createRepositoryExtractionRun(ctx context.Context, db sqlDB, request RepositoryExtractionRunRequest) (RepositoryExtractionRunResult, error) {
	if request.RequestID == "" {
		return RepositoryExtractionRunResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	if !strings.HasPrefix(request.RepositorySnapshotID, "repo-snapshot:") {
		return RepositoryExtractionRunResult{}, newDomainError(ErrorInvalidRecordID, "repository_snapshot_id %q must start with repo-snapshot:", request.RepositorySnapshotID)
	}
	definition, err := buildExtractorDefinition(request.ExtractorDefinition)
	if err != nil {
		return RepositoryExtractionRunResult{}, err
	}
	runID, err := stableID("run:", "repository_extraction_run", struct {
		RequestID             string `json:"request_id"`
		RepositorySnapshotID  string `json:"repository_snapshot_id"`
		ExtractorDefinitionID string `json:"extractor_definition_id"`
	}{request.RequestID, request.RepositorySnapshotID, definition.ID})
	if err != nil {
		return RepositoryExtractionRunResult{}, err
	}
	run := ExtractionRun{ID: runID, RequestID: request.RequestID, ExtractorDefinitionID: definition.ID, RepositorySnapshotID: request.RepositorySnapshotID}
	payloadData, err := deterministicJSON(struct {
		RepositorySnapshotID  string `json:"repository_snapshot_id"`
		ExtractorDefinitionID string `json:"extractor_definition_id"`
	}{RepositorySnapshotID: request.RepositorySnapshotID, ExtractorDefinitionID: definition.ID})
	if err != nil {
		return RepositoryExtractionRunResult{}, err
	}
	payloadHash := contentHash(payloadData)
	configData, err := json.Marshal(definition.Config)
	if err != nil {
		return RepositoryExtractionRunResult{}, fmt.Errorf("serializing extractor config: %w", err)
	}
	inserted := false
	err = withTx(ctx, db, func(tx sqlTx) error {
		if _, err := tx.exec(ctx, `INSERT INTO extractor_definitions (extractor_definition_id, extractor_name, extractor_version, extractor_config_hash, extractor_config) VALUES ($1,$2,$3,$4,$5::jsonb) ON CONFLICT (extractor_definition_id) DO NOTHING`, definition.ID, definition.Name, definition.Version, definition.ConfigHash, string(configData)); err != nil {
			return fmt.Errorf("upserting extractor definition: %w", err)
		}
		result, err := tx.exec(ctx, `INSERT INTO extraction_runs (extraction_run_id, extractor_definition_id, repository_snapshot_id, request_id) VALUES ($1,$2,$3,$4) ON CONFLICT (extraction_run_id) DO NOTHING`, run.ID, definition.ID, run.RepositorySnapshotID, run.RequestID)
		if err != nil {
			return fmt.Errorf("upserting repository extraction run: %w", err)
		}
		inserted = result.RowsAffected() == 1
		requestInserted, err := persistRepositoryExtractionRunRequestTx(ctx, tx, run, payloadHash)
		if err != nil {
			return err
		}
		inserted = inserted || requestInserted
		return nil
	})
	if err != nil {
		return RepositoryExtractionRunResult{}, err
	}
	var stored ExtractionRun
	err = db.queryRow(ctx, `SELECT extraction_run_id, request_id, extractor_definition_id, COALESCE(source_snapshot_id,''), COALESCE(extraction_view_id,''), COALESCE(repository_snapshot_id,'') FROM extraction_runs WHERE extraction_run_id = $1`, run.ID).Scan(&stored.ID, &stored.RequestID, &stored.ExtractorDefinitionID, &stored.SourceSnapshotID, &stored.ExtractionViewID, &stored.RepositorySnapshotID)
	if err != nil {
		return RepositoryExtractionRunResult{}, fmt.Errorf("reading repository extraction run: %w", err)
	}
	return RepositoryExtractionRunResult{ExtractionRun: stored, Replayed: !inserted}, nil
}

func persistRepositoryExtractionRunRequestTx(ctx context.Context, tx sqlTx, run ExtractionRun, payloadHash string) (bool, error) {
	result, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_run_requests (
			request_id, extraction_run_id, request_payload_hash
		)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id) DO NOTHING
	`, run.RequestID, run.ID, payloadHash)
	if err != nil {
		return false, fmt.Errorf("upserting repository extraction run request: %w", err)
	}
	if result.RowsAffected() == 1 {
		return true, nil
	}
	var existingRunID, existingPayloadHash string
	if err := tx.queryRow(ctx, `
		SELECT extraction_run_id, request_payload_hash
		FROM repository_extraction_run_requests
		WHERE request_id = $1
	`, run.RequestID).Scan(&existingRunID, &existingPayloadHash); err != nil {
		return false, fmt.Errorf("reading repository extraction run request %s: %w", run.RequestID, err)
	}
	if existingRunID != run.ID || existingPayloadHash != payloadHash {
		return false, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction payload", run.RequestID)
	}
	return false, nil
}
