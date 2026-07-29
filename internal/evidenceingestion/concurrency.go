package evidenceingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func lockEvidenceIngestionRequest(ctx context.Context, tx sqlTx, operation, requestID string) error {
	if _, err := tx.exec(ctx, `
		INSERT INTO evidence_ingestion_request_serializations (operation_name, request_id)
		VALUES ($1,$2)
		ON CONFLICT (operation_name, request_id) DO NOTHING
	`, operation, requestID); err != nil {
		return fmt.Errorf("creating evidence ingestion request serialization: %w", err)
	}
	var locked int
	if err := tx.queryRow(ctx, `
		SELECT 1
		FROM evidence_ingestion_request_serializations
		WHERE operation_name = $1 AND request_id = $2
		FOR UPDATE
	`, operation, requestID).Scan(&locked); err != nil {
		return fmt.Errorf("locking evidence ingestion request serialization: %w", err)
	}
	return nil
}

func ensureAndLockGitRepositorySourceStream(ctx context.Context, tx sqlTx, repoID string) error {
	if _, err := tx.exec(ctx, `
		INSERT INTO git_repository_source_streams (repo_id)
		VALUES ($1)
		ON CONFLICT (repo_id) DO NOTHING
	`, repoID); err != nil {
		return fmt.Errorf("creating Git repository source stream: %w", err)
	}
	return lockGitRepositorySourceStream(ctx, tx, repoID)
}

func lockGitRepositorySourceStream(ctx context.Context, tx sqlTx, repoID string) error {
	var locked string
	err := tx.queryRow(ctx, `
		SELECT repo_id
		FROM git_repository_source_streams
		WHERE repo_id = $1
		FOR UPDATE
	`, repoID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return newDomainError(ErrorRepositoryWorkConflict, "Git repository source stream %s was not found", repoID)
	}
	if err != nil {
		return fmt.Errorf("locking Git repository source stream: %w", err)
	}
	return nil
}

func ensureAndLockRepositorySourceStream(ctx context.Context, tx sqlTx, repoID, extractorName string) error {
	if _, err := tx.exec(ctx, `
		INSERT INTO repository_source_streams (repo_id, extractor_name)
		VALUES ($1,$2)
		ON CONFLICT (repo_id, extractor_name) DO NOTHING
	`, repoID, extractorName); err != nil {
		return fmt.Errorf("creating repository source stream: %w", err)
	}
	return lockRepositorySourceStream(ctx, tx, repoID, extractorName)
}

func lockRepositorySourceStream(ctx context.Context, tx sqlTx, repoID, extractorName string) error {
	var lockedRepoID, lockedExtractorName string
	err := tx.queryRow(ctx, `
		SELECT repo_id, extractor_name
		FROM repository_source_streams
		WHERE repo_id = $1 AND extractor_name = $2
		FOR UPDATE
	`, repoID, extractorName).Scan(&lockedRepoID, &lockedExtractorName)
	if errors.Is(err, pgx.ErrNoRows) {
		return newDomainError(ErrorRepositoryWorkConflict, "repository source stream %s/%s was not found", repoID, extractorName)
	}
	if err != nil {
		return fmt.Errorf("locking repository source stream: %w", err)
	}
	return nil
}

func lockRepositorySourceStreamForWork(ctx context.Context, tx sqlTx, workItemID string) (repositoryExtractionWorkRecord, error) {
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return repositoryExtractionWorkRecord{}, newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work %s was not found", workItemID)
	}
	if err != nil {
		return repositoryExtractionWorkRecord{}, err
	}
	if err := lockRepositorySourceStream(ctx, tx, work.work.RepoID, work.work.ExtractorName); err != nil {
		return repositoryExtractionWorkRecord{}, err
	}
	return work, nil
}
