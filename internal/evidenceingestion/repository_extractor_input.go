package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BuildRepositoryExtractorInput loads exact persisted file bytes for one repository snapshot.
func BuildRepositoryExtractorInput(ctx context.Context, pool *pgxpool.Pool, repositorySnapshotID string) (RepositoryExtractorInput, error) {
	if pool == nil {
		return RepositoryExtractorInput{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return buildRepositoryExtractorInput(ctx, pgxDB{pool: pool}, repositorySnapshotID)
}

func buildRepositoryExtractorInput(ctx context.Context, db sqlDB, repositorySnapshotID string) (RepositoryExtractorInput, error) {
	if repositorySnapshotID == "" {
		return RepositoryExtractorInput{}, newDomainError(ErrorInvalidInput, "repository_snapshot_id is required")
	}
	if !strings.HasPrefix(repositorySnapshotID, "repo-snapshot:") {
		return RepositoryExtractorInput{}, newDomainError(ErrorInvalidRecordID, "repository_snapshot_id %q must start with repo-snapshot:", repositorySnapshotID)
	}

	var snapshot RepositorySnapshot
	err := db.queryRow(ctx, `
		SELECT repository_snapshot_id, repo_id, commit_sha, manifest_hash,
			manifest_entry_count, revision_verification_method, manifest_contract,
			file_selection_contract, selected_file_count
		FROM repository_snapshots
		WHERE repository_snapshot_id = $1
	`, repositorySnapshotID).Scan(
		&snapshot.ID,
		&snapshot.RepoID,
		&snapshot.CommitSHA,
		&snapshot.ManifestHash,
		&snapshot.ManifestEntryCount,
		&snapshot.RevisionVerificationMethod,
		&snapshot.ManifestContract,
		&snapshot.FileSelectionContract,
		&snapshot.SelectedFileCount,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RepositoryExtractorInput{}, newDomainError(ErrorMissingSourceViewAttempt, "repository snapshot %s was not found", repositorySnapshotID)
		}
		return RepositoryExtractorInput{}, fmt.Errorf("loading repository snapshot: %w", err)
	}

	rows, err := db.query(ctx, `
		SELECT sfs.file_snapshot_id, sfs.repository_snapshot_id, sfs.repo_id,
			sfs.commit_sha, sfs.path, sfs.blob_hash, sfs.git_blob_oid,
			sfs.byte_length, sb.raw_content
		FROM source_file_snapshots sfs
		JOIN source_blobs sb ON sb.raw_content_hash = sfs.blob_hash
		WHERE sfs.repository_snapshot_id = $1
		ORDER BY sfs.path, sfs.file_snapshot_id
	`, repositorySnapshotID)
	if err != nil {
		return RepositoryExtractorInput{}, fmt.Errorf("loading repository snapshot files: %w", err)
	}
	defer rows.Close()

	files := make([]RepositoryExtractorFile, 0, snapshot.SelectedFileCount)
	for rows.Next() {
		var file RepositoryExtractorFile
		if err := rows.Scan(
			&file.FileSnapshot.ID,
			&file.FileSnapshot.RepositorySnapshotID,
			&file.FileSnapshot.RepoID,
			&file.FileSnapshot.CommitSHA,
			&file.FileSnapshot.Path,
			&file.FileSnapshot.BlobHash,
			&file.FileSnapshot.GitBlobOID,
			&file.FileSnapshot.ByteLength,
			&file.Content,
		); err != nil {
			return RepositoryExtractorInput{}, fmt.Errorf("scanning repository snapshot file: %w", err)
		}
		if err := validateRepositoryExtractorFile(snapshot, file); err != nil {
			return RepositoryExtractorInput{}, err
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return RepositoryExtractorInput{}, fmt.Errorf("iterating repository snapshot files: %w", err)
	}
	if len(files) != snapshot.SelectedFileCount {
		return RepositoryExtractorInput{}, newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s selected_file_count is %d but %d files were loaded", snapshot.ID, snapshot.SelectedFileCount, len(files))
	}
	return RepositoryExtractorInput{RepositorySnapshot: snapshot, Files: files}, nil
}

func validateRepositoryExtractorFile(snapshot RepositorySnapshot, file RepositoryExtractorFile) error {
	meta := file.FileSnapshot
	if meta.RepositorySnapshotID != snapshot.ID || meta.RepoID != snapshot.RepoID || meta.CommitSHA != snapshot.CommitSHA {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "file snapshot %s does not belong to repository snapshot %s", meta.ID, snapshot.ID)
	}
	if meta.ByteLength != len(file.Content) {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "file snapshot %s byte length does not match persisted content", meta.ID)
	}
	if meta.BlobHash != contentHash(file.Content) {
		return newDomainError(ErrorRepositorySnapshotIntegrity, "file snapshot %s blob hash does not match persisted content", meta.ID)
	}
	return nil
}
