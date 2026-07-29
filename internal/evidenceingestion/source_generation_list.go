package evidenceingestion

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositorySourceGenerationListMaxLimit bounds one operator discovery query.
	RepositorySourceGenerationListMaxLimit = 100
)

// RepositorySourceGenerationListInput selects one exact repository/extractor stream.
type RepositorySourceGenerationListInput struct {
	RepoID        string `json:"repo_id"`
	ExtractorName string `json:"extractor_name"`
	Limit         int    `json:"limit"`
}

// RepositorySourceGenerationStatus reports one immutable generation and current-head state.
type RepositorySourceGenerationStatus struct {
	RepositorySourceGeneration
	Active bool `json:"active"`
}

// ListRepositorySourceGenerations returns recent immutable generations without changing stream state.
func ListRepositorySourceGenerations(
	ctx context.Context,
	pool *pgxpool.Pool,
	input RepositorySourceGenerationListInput,
) ([]RepositorySourceGenerationStatus, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return listRepositorySourceGenerations(ctx, pgxDB{pool: pool}, input)
}

func listRepositorySourceGenerations(
	ctx context.Context,
	db sqlDB,
	input RepositorySourceGenerationListInput,
) ([]RepositorySourceGenerationStatus, error) {
	input, err := validateRepositorySourceGenerationListInput(input)
	if err != nil {
		return nil, err
	}
	rows, err := db.query(ctx, `
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
			g.proposal_count,
			(h.active_generation_id IS NOT NULL)
		FROM repository_source_generations g
		JOIN proposal_batches pb
		  ON pb.proposal_batch_id = g.proposal_batch_id
		LEFT JOIN repository_source_heads h
		  ON h.repo_id = g.repo_id
		 AND h.extractor_name = g.extractor_name
		 AND h.active_generation_id = g.source_generation_id
		WHERE g.repo_id = $1
		  AND g.extractor_name = $2
		ORDER BY g.generation_number DESC, g.source_generation_id
		LIMIT $3
	`, input.RepoID, input.ExtractorName, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing repository source generations: %w", err)
	}
	defer rows.Close()

	generations := make([]RepositorySourceGenerationStatus, 0, input.Limit)
	for rows.Next() {
		var generation RepositorySourceGenerationStatus
		if err := rows.Scan(
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
			&generation.Active,
		); err != nil {
			return nil, fmt.Errorf("scanning repository source generation: %w", err)
		}
		generations = append(generations, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating repository source generations: %w", err)
	}
	return generations, nil
}

func validateRepositorySourceGenerationListInput(
	input RepositorySourceGenerationListInput,
) (RepositorySourceGenerationListInput, error) {
	input.RepoID = strings.TrimSpace(input.RepoID)
	if input.RepoID == "" {
		return RepositorySourceGenerationListInput{}, newDomainError(
			ErrorInvalidInput,
			"repository source generation repo_id is required",
		)
	}
	extractorName, err := validateRepositoryExtractionWorkExtractor(input.ExtractorName)
	if err != nil {
		return RepositorySourceGenerationListInput{}, err
	}
	input.ExtractorName = extractorName
	if input.Limit < 1 || input.Limit > RepositorySourceGenerationListMaxLimit {
		return RepositorySourceGenerationListInput{}, newDomainError(
			ErrorInvalidInput,
			"repository source generation limit must be between 1 and %d",
			RepositorySourceGenerationListMaxLimit,
		)
	}
	return input, nil
}
