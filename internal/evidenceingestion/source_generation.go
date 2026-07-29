package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const repositorySourceGenerationSelect = `
	SELECT
		g.source_generation_id,
		g.repo_id,
		g.extractor_name,
		g.extractor_definition_id,
		g.generation_number,
		g.repository_snapshot_id,
		g.commit_sha,
		pb.extraction_attempt_id,
		g.proposal_batch_id,
		g.extractor_output_hash,
		g.proposal_count
	FROM repository_source_generations g
	JOIN proposal_batches pb ON pb.proposal_batch_id = g.proposal_batch_id
`

type repositorySourceGenerationCandidate struct {
	repoID                string
	extractorName         string
	extractorDefinitionID string
	repositorySnapshotID  string
	commitSHA             string
	extractionAttemptID   string
	proposalBatchID       string
	extractorOutputHash   string
	proposalCount         int
}

// CreateRepositorySourceGeneration records one completed repository proposal batch as an append-only generation.
func CreateRepositorySourceGeneration(ctx context.Context, pool *pgxpool.Pool, input RepositorySourceGenerationCreateInput) (RepositorySourceGenerationCreateResult, error) {
	if pool == nil {
		return RepositorySourceGenerationCreateResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return createRepositorySourceGeneration(ctx, pgxDB{pool: pool}, input)
}

func createRepositorySourceGeneration(ctx context.Context, db sqlDB, input RepositorySourceGenerationCreateInput) (RepositorySourceGenerationCreateResult, error) {
	if input.ProposalBatchID == "" {
		return RepositorySourceGenerationCreateResult{}, newDomainError(ErrorInvalidInput, "proposal_batch_id is required")
	}
	if !strings.HasPrefix(input.ProposalBatchID, "batch:") {
		return RepositorySourceGenerationCreateResult{}, newDomainError(ErrorInvalidRecordID, "proposal_batch_id %q must start with batch:", input.ProposalBatchID)
	}

	var result RepositorySourceGenerationCreateResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		candidate, err := loadRepositorySourceGenerationCandidate(ctx, tx, input.ProposalBatchID)
		if err != nil {
			return err
		}
		if err := ensureAndLockRepositorySourceStream(ctx, tx, candidate.repoID, candidate.extractorName); err != nil {
			return err
		}

		stored, err := readRepositorySourceGenerationBySnapshot(ctx, tx, candidate.repositorySnapshotID, candidate.extractorDefinitionID)
		switch {
		case err == nil:
			if stored.ExtractorOutputHash != candidate.extractorOutputHash || stored.ProposalCount != candidate.proposalCount {
				return newDomainError(ErrorSourceGenerationConflict, "repository snapshot %s and extractor %s already produced generation %s with different output", candidate.repositorySnapshotID, candidate.extractorDefinitionID, stored.ID)
			}
			result = RepositorySourceGenerationCreateResult{Generation: stored, Replayed: true}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("reading repository source generation: %w", err)
		}

		var number int
		if err := tx.queryRow(ctx, `
			SELECT COALESCE(MAX(generation_number), 0) + 1
			FROM repository_source_generations
			WHERE repo_id = $1 AND extractor_name = $2
		`, candidate.repoID, candidate.extractorName).Scan(&number); err != nil {
			return fmt.Errorf("allocating repository source generation number: %w", err)
		}
		generationID, err := repositorySourceGenerationID(
			candidate.repoID,
			candidate.extractorDefinitionID,
			candidate.repositorySnapshotID,
			candidate.extractorOutputHash,
		)
		if err != nil {
			return err
		}
		generation := RepositorySourceGeneration{
			ID:                    generationID,
			RepoID:                candidate.repoID,
			ExtractorName:         candidate.extractorName,
			ExtractorDefinitionID: candidate.extractorDefinitionID,
			Number:                number,
			RepositorySnapshotID:  candidate.repositorySnapshotID,
			CommitSHA:             candidate.commitSHA,
			ExtractionAttemptID:   candidate.extractionAttemptID,
			ProposalBatchID:       candidate.proposalBatchID,
			ExtractorOutputHash:   candidate.extractorOutputHash,
			ProposalCount:         candidate.proposalCount,
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_source_generations (
				source_generation_id,
				repo_id,
				extractor_name,
				extractor_definition_id,
				generation_number,
				repository_snapshot_id,
				commit_sha,
				proposal_batch_id,
				extractor_output_hash,
				proposal_count
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		`, generation.ID, generation.RepoID, generation.ExtractorName, generation.ExtractorDefinitionID, generation.Number, generation.RepositorySnapshotID, generation.CommitSHA, generation.ProposalBatchID, generation.ExtractorOutputHash, generation.ProposalCount); err != nil {
			return fmt.Errorf("inserting repository source generation: %w", err)
		}
		result = RepositorySourceGenerationCreateResult{Generation: generation}
		return nil
	})
	if err != nil {
		return RepositorySourceGenerationCreateResult{}, err
	}
	return result, nil
}

func repositorySourceGenerationID(repoID, extractorDefinitionID, repositorySnapshotID, extractorOutputHash string) (string, error) {
	return stableID("generation:", "repository_source_generation", struct {
		RepoID                string `json:"repo_id"`
		ExtractorDefinitionID string `json:"extractor_definition_id"`
		RepositorySnapshotID  string `json:"repository_snapshot_id"`
		ExtractorOutputHash   string `json:"extractor_output_hash"`
	}{repoID, extractorDefinitionID, repositorySnapshotID, extractorOutputHash})
}

func loadRepositorySourceGenerationCandidate(ctx context.Context, tx sqlTx, proposalBatchID string) (repositorySourceGenerationCandidate, error) {
	var candidate repositorySourceGenerationCandidate
	var attemptStatus, batchStatus string
	err := tx.queryRow(ctx, `
		SELECT
			rs.repo_id,
			rs.commit_sha,
			ed.extractor_name,
			er.extractor_definition_id,
			er.repository_snapshot_id,
			ea.extraction_attempt_id,
			ea.status,
			COALESCE(ea.output_hash, ''),
			pb.proposal_batch_id,
			pb.status,
			pb.proposal_count
		FROM proposal_batches pb
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = pb.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		JOIN extractor_definitions ed ON ed.extractor_definition_id = er.extractor_definition_id
		JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		WHERE pb.proposal_batch_id = $1
	`, proposalBatchID).Scan(
		&candidate.repoID,
		&candidate.commitSHA,
		&candidate.extractorName,
		&candidate.extractorDefinitionID,
		&candidate.repositorySnapshotID,
		&candidate.extractionAttemptID,
		&attemptStatus,
		&candidate.extractorOutputHash,
		&candidate.proposalBatchID,
		&batchStatus,
		&candidate.proposalCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return repositorySourceGenerationCandidate{}, newDomainError(ErrorMissingSourceViewAttempt, "repository proposal batch %s was not found", proposalBatchID)
	}
	if err != nil {
		return repositorySourceGenerationCandidate{}, fmt.Errorf("reading repository generation candidate: %w", err)
	}
	if attemptStatus != attemptStatusSucceeded || batchStatus != batchStatusCompleted || candidate.extractorOutputHash == "" {
		return repositorySourceGenerationCandidate{}, newDomainError(ErrorSourceGenerationConflict, "repository proposal batch %s is not a completed successful extraction", proposalBatchID)
	}
	return candidate, nil
}

func readRepositorySourceGenerationBySnapshot(ctx context.Context, tx sqlTx, repositorySnapshotID, extractorDefinitionID string) (RepositorySourceGeneration, error) {
	return scanRepositorySourceGeneration(tx.queryRow(ctx, repositorySourceGenerationSelect+`
		WHERE g.repository_snapshot_id = $1 AND g.extractor_definition_id = $2
	`, repositorySnapshotID, extractorDefinitionID))
}

func readRepositorySourceGenerationByID(ctx context.Context, tx sqlTx, generationID string) (RepositorySourceGeneration, error) {
	return scanRepositorySourceGeneration(tx.queryRow(ctx, repositorySourceGenerationSelect+`
		WHERE g.source_generation_id = $1
	`, generationID))
}

func scanRepositorySourceGeneration(row sqlRow) (RepositorySourceGeneration, error) {
	var generation RepositorySourceGeneration
	err := row.Scan(
		&generation.ID,
		&generation.RepoID,
		&generation.ExtractorName,
		&generation.ExtractorDefinitionID,
		&generation.Number,
		&generation.RepositorySnapshotID,
		&generation.CommitSHA,
		&generation.ExtractionAttemptID,
		&generation.ProposalBatchID,
		&generation.ExtractorOutputHash,
		&generation.ProposalCount,
	)
	return generation, err
}

// ActivateRepositorySourceGeneration atomically advances one repository/extractor stream head.
func ActivateRepositorySourceGeneration(ctx context.Context, pool *pgxpool.Pool, input RepositorySourceGenerationActivationInput) (RepositorySourceGenerationActivationResult, error) {
	if pool == nil {
		return RepositorySourceGenerationActivationResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return activateRepositorySourceGeneration(ctx, pgxDB{pool: pool}, input)
}

func activateRepositorySourceGeneration(ctx context.Context, db sqlDB, input RepositorySourceGenerationActivationInput) (RepositorySourceGenerationActivationResult, error) {
	if input.RequestID == "" {
		return RepositorySourceGenerationActivationResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	if input.SourceGenerationID == "" {
		return RepositorySourceGenerationActivationResult{}, newDomainError(ErrorInvalidInput, "source_generation_id is required")
	}
	if !strings.HasPrefix(input.SourceGenerationID, "generation:") {
		return RepositorySourceGenerationActivationResult{}, newDomainError(ErrorInvalidRecordID, "source_generation_id %q must start with generation:", input.SourceGenerationID)
	}
	payload, err := deterministicJSON(struct {
		SourceGenerationID string `json:"source_generation_id"`
	}{input.SourceGenerationID})
	if err != nil {
		return RepositorySourceGenerationActivationResult{}, err
	}
	payloadHash := contentHash(payload)

	var result RepositorySourceGenerationActivationResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		target, err := readRepositorySourceGenerationByID(ctx, tx, input.SourceGenerationID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository source generation %s was not found", input.SourceGenerationID)
		}
		if err != nil {
			return fmt.Errorf("reading repository source generation: %w", err)
		}
		if err := lockRepositorySourceStream(ctx, tx, target.RepoID, target.ExtractorName); err != nil {
			return err
		}
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-source-generation-activation", input.RequestID); err != nil {
			return err
		}

		replayed, ok, err := readRepositoryGenerationActivationRequest(ctx, tx, input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			if replayed.ActivatedGenerationID != input.SourceGenerationID || replayed.requestPayloadHash != payloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different source generation activation payload", input.RequestID)
			}
			reconciliation, err := readRepositoryGenerationReconciliation(ctx, tx, replayed.ActivatedGenerationID)
			if err != nil {
				return err
			}
			result = replayed.RepositorySourceGenerationActivationResult
			result.Replayed = true
			result.Reconciliation = reconciliation
			return nil
		}

		var previousGenerationID string
		var previousGenerationNumber int
		err = tx.queryRow(ctx, `
			SELECT h.active_generation_id, g.generation_number
			FROM repository_source_heads h
			JOIN repository_source_generations g ON g.source_generation_id = h.active_generation_id
			WHERE h.repo_id = $1 AND h.extractor_name = $2
			FOR UPDATE OF h
		`, target.RepoID, target.ExtractorName).Scan(&previousGenerationID, &previousGenerationNumber)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reading repository source head: %w", err)
		}
		if previousGenerationNumber > target.Number {
			return newDomainError(ErrorSourceGenerationConflict, "repository source generation %s cannot replace newer active generation %s", target.ID, previousGenerationID)
		}
		changed := previousGenerationID != target.ID
		var reconciliation RepositoryGenerationReconciliation
		if changed {
			reconciliation, err = reconcileRepositorySourceGeneration(ctx, tx, target, previousGenerationID)
		} else {
			reconciliation, err = readRepositoryGenerationReconciliation(ctx, tx, target.ID)
		}
		if err != nil {
			return err
		}
		var previous any
		if previousGenerationID != "" {
			previous = previousGenerationID
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_generation_activation_requests (
				request_id,
				repo_id,
				extractor_name,
				previous_generation_id,
				activated_generation_id,
				changed,
				request_payload_hash
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
		`, input.RequestID, target.RepoID, target.ExtractorName, previous, target.ID, changed, payloadHash); err != nil {
			return fmt.Errorf("inserting repository generation activation request: %w", err)
		}
		if changed {
			if _, err := tx.exec(ctx, `
				INSERT INTO repository_source_heads (
					repo_id,
					extractor_name,
					active_generation_id
				)
				VALUES ($1,$2,$3)
				ON CONFLICT (repo_id, extractor_name) DO UPDATE
				SET active_generation_id = EXCLUDED.active_generation_id, updated_at = now()
			`, target.RepoID, target.ExtractorName, target.ID); err != nil {
				return fmt.Errorf("advancing repository source head: %w", err)
			}
		}
		result = RepositorySourceGenerationActivationResult{
			RequestID:             input.RequestID,
			RepoID:                target.RepoID,
			ExtractorName:         target.ExtractorName,
			ExtractorDefinitionID: target.ExtractorDefinitionID,
			PreviousGenerationID:  previousGenerationID,
			ActivatedGenerationID: target.ID,
			Changed:               changed,
			Reconciliation:        reconciliation,
		}
		return nil
	})
	if err != nil {
		return RepositorySourceGenerationActivationResult{}, err
	}
	return result, nil
}

type repositoryGenerationActivationRequest struct {
	RepositorySourceGenerationActivationResult
	requestPayloadHash string
}

func readRepositoryGenerationActivationRequest(ctx context.Context, tx sqlTx, requestID string) (repositoryGenerationActivationRequest, bool, error) {
	var request repositoryGenerationActivationRequest
	var previous *string
	err := tx.queryRow(ctx, `
		SELECT
			r.request_id,
			r.repo_id,
			r.extractor_name,
			g.extractor_definition_id,
			r.previous_generation_id,
			r.activated_generation_id,
			r.changed,
			r.request_payload_hash
		FROM repository_generation_activation_requests r
		JOIN repository_source_generations g ON g.source_generation_id = r.activated_generation_id
		WHERE r.request_id = $1
	`, requestID).Scan(
		&request.RequestID,
		&request.RepoID,
		&request.ExtractorName,
		&request.ExtractorDefinitionID,
		&previous,
		&request.ActivatedGenerationID,
		&request.Changed,
		&request.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return repositoryGenerationActivationRequest{}, false, nil
	}
	if err != nil {
		return repositoryGenerationActivationRequest{}, false, fmt.Errorf("reading repository generation activation request: %w", err)
	}
	if previous != nil {
		request.PreviousGenerationID = *previous
	}
	return request, true, nil
}
