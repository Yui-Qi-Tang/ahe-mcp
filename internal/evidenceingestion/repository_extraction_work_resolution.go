package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositoryExtractionWorkStatePending means exact work is available for a future claim.
	RepositoryExtractionWorkStatePending = repositoryExtractionWorkPending
	// RepositoryExtractionWorkStateRunning means exact work currently has a claim owner.
	RepositoryExtractionWorkStateRunning = repositoryExtractionWorkRunning
	// RepositoryExtractionWorkStateSuperseded means newer work replaced this unclaimed item.
	RepositoryExtractionWorkStateSuperseded = repositoryExtractionWorkSuperseded
	// RepositoryExtractionWorkStateSucceeded means work is bound to an immutable generation.
	RepositoryExtractionWorkStateSucceeded = RepositoryExtractionWorkOutcomeSucceeded
	// RepositoryExtractionWorkStateFailed means work has bounded terminal failure diagnostics.
	RepositoryExtractionWorkStateFailed = RepositoryExtractionWorkOutcomeFailed
)

// RepositoryExtractionWorkResolution projects the current closed work state and,
// for succeeded work, its immutable generation and repository snapshot authority.
type RepositoryExtractionWorkResolution struct {
	Work                RepositoryExtractionWork    `json:"work"`
	State               string                      `json:"state"`
	LatestAttemptNumber int                         `json:"latest_attempt_number"`
	SourceGeneration    *RepositorySourceGeneration `json:"source_generation,omitempty"`
	RepositorySnapshot  *RepositorySnapshot         `json:"repository_snapshot,omitempty"`
}

// ResolveRepositoryExtractionWork returns a read-only, internally validated
// projection of one exact work item.
func ResolveRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, workItemID string) (RepositoryExtractionWorkResolution, error) {
	if pool == nil {
		return RepositoryExtractionWorkResolution{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	workItemID = strings.TrimSpace(workItemID)
	if !strings.HasPrefix(workItemID, "repo-work:") {
		return RepositoryExtractionWorkResolution{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", workItemID)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return RepositoryExtractionWorkResolution{}, fmt.Errorf("beginning repository extraction work resolution: %w", err)
	}
	defer tx.Rollback(context.Background())

	var result RepositoryExtractionWorkResolution
	err = func(tx sqlTx) error {
		work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work %s was not found", workItemID)
		}
		if err != nil {
			return err
		}
		var latestAttemptNumber int
		if err := tx.queryRow(ctx, `
			SELECT COALESCE(MAX(attempt_number), 0)
			FROM repository_extraction_work_claim_attempts
			WHERE work_item_id = $1
		`, workItemID).Scan(&latestAttemptNumber); err != nil {
			return fmt.Errorf("reading latest repository extraction work attempt number: %w", err)
		}
		result = RepositoryExtractionWorkResolution{
			Work: work.work, State: work.status, LatestAttemptNumber: latestAttemptNumber,
		}
		switch work.status {
		case RepositoryExtractionWorkStatePending,
			RepositoryExtractionWorkStateSuperseded:
			return nil
		case RepositoryExtractionWorkStateRunning,
			RepositoryExtractionWorkStateFailed:
			if latestAttemptNumber < 1 {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s has state %s without a claim attempt", workItemID, work.status)
			}
			return nil
		case RepositoryExtractionWorkStateSucceeded:
		default:
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s has unsupported status %s", workItemID, work.status)
		}
		if latestAttemptNumber < 1 {
			return newDomainError(ErrorRepositoryWorkConflict, "successful repository extraction work %s has no claim attempt", workItemID)
		}
		if work.sourceGenerationID == "" || work.finishedAt == nil || work.failureClass != "" || work.failureMessage != "" {
			return newDomainError(ErrorRepositoryWorkConflict, "successful repository extraction work %s has inconsistent terminal material", workItemID)
		}

		generation, err := readRepositorySourceGenerationByID(ctx, tx, work.sourceGenerationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository extraction work %s references missing generation %s", workItemID, work.sourceGenerationID)
		}
		if err != nil {
			return fmt.Errorf("reading repository source generation: %w", err)
		}
		if generation.RepoID != work.work.RepoID ||
			generation.ExtractorName != work.work.ExtractorName ||
			generation.CommitSHA != work.work.HeadCommitSHA {
			return newDomainError(ErrorRepositoryWorkConflict, "repository source generation %s does not match successful work %s", generation.ID, workItemID)
		}

		snapshot, err := readRepositorySnapshotByID(ctx, tx, generation.RepositorySnapshotID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository source generation %s references missing snapshot %s", generation.ID, generation.RepositorySnapshotID)
		}
		if err != nil {
			return fmt.Errorf("reading repository snapshot: %w", err)
		}
		if snapshot.RepoID != generation.RepoID || snapshot.CommitSHA != generation.CommitSHA {
			return newDomainError(ErrorRepositorySnapshotIntegrity, "repository snapshot %s does not match generation %s", snapshot.ID, generation.ID)
		}
		var persistedFileCount int
		if err := tx.queryRow(ctx, `
			SELECT COUNT(*)
			FROM source_file_snapshots
			WHERE repository_snapshot_id = $1
		`, snapshot.ID).Scan(&persistedFileCount); err != nil {
			return fmt.Errorf("counting repository snapshot files: %w", err)
		}
		if persistedFileCount != snapshot.SelectedFileCount {
			return newDomainError(
				ErrorRepositorySnapshotIntegrity,
				"repository snapshot %s selected file count %d does not match %d persisted files",
				snapshot.ID,
				snapshot.SelectedFileCount,
				persistedFileCount,
			)
		}

		result.SourceGeneration = &generation
		result.RepositorySnapshot = &snapshot
		return nil
	}(pgxTx{tx: tx})
	if err != nil {
		return RepositoryExtractionWorkResolution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RepositoryExtractionWorkResolution{}, fmt.Errorf("committing repository extraction work resolution: %w", err)
	}
	return result, nil
}

func readRepositorySnapshotByID(ctx context.Context, tx sqlTx, repositorySnapshotID string) (RepositorySnapshot, error) {
	var snapshot RepositorySnapshot
	err := tx.queryRow(ctx, `
		SELECT
			repository_snapshot_id,
			repo_id,
			commit_sha,
			manifest_hash,
			manifest_entry_count,
			revision_verification_method,
			manifest_contract,
			file_selection_contract,
			selected_file_count
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
	return snapshot, err
}
