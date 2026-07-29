package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GetGroundedEvidenceBrief reads one bounded lexical match set and its persisted diagnostics.
func GetGroundedEvidenceBrief(ctx context.Context, pool *pgxpool.Pool, input GroundedEvidenceBriefInput) (GroundedEvidenceBriefQueryResult, error) {
	if pool == nil {
		return GroundedEvidenceBriefQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getGroundedEvidenceBrief(ctx, pgxDB{pool: pool}, input)
}

func getGroundedEvidenceBrief(ctx context.Context, db sqlDB, input GroundedEvidenceBriefInput) (GroundedEvidenceBriefQueryResult, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.ProposalListInput = normalizeProposalListInput(input.ProposalListInput)
	queryMode, err := normalizeEvidenceQueryMode(input.QueryMode)
	if err != nil {
		return GroundedEvidenceBriefQueryResult{}, err
	}
	input.QueryMode = queryMode
	searchInput := ProposalSearchInput{
		Query:             input.Query,
		ProposalListInput: input.ProposalListInput,
	}
	if err := validateProposalSearchInput(searchInput); err != nil {
		return GroundedEvidenceBriefQueryResult{}, err
	}

	result := GroundedEvidenceBriefQueryResult{}
	err = withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		matches, execution, err := executeGroundedEvidenceQuery(ctx, tx, input)
		if err != nil {
			return err
		}
		result.Matches = matches
		result.Execution = execution

		attemptIDs := matchedExtractionAttemptIDs(matches)
		result.Coverage, err = loadGroundedEvidenceBriefCoverage(ctx, tx, attemptIDs)
		if err != nil {
			return err
		}
		if input.IncludeSourceContext {
			result.SourceContexts, err = loadGroundedEvidenceSourceContexts(ctx, tx, matches)
			if err != nil {
				return err
			}
		}
		if input.IncludeRepositoryContext {
			result.RepositoryContexts, err = loadGroundedEvidenceRepositoryContexts(
				ctx,
				tx,
				matches,
			)
		}
		return err
	})
	if err != nil {
		return GroundedEvidenceBriefQueryResult{}, err
	}
	return result, nil
}

func matchedExtractionAttemptIDs(matches []ProposalSearchResult) []string {
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if match.Record.ExtractionAttemptID != "" {
			seen[match.Record.ExtractionAttemptID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func loadGroundedEvidenceBriefCoverage(ctx context.Context, db sqlQueryer, attemptIDs []string) ([]GroundedEvidenceBriefCoverage, error) {
	if len(attemptIDs) == 0 {
		return []GroundedEvidenceBriefCoverage{}, nil
	}
	rows, err := db.query(ctx, `
		SELECT extraction_attempt_id, COALESCE(fixture_output, '{}'::jsonb)
		FROM extraction_attempts
		WHERE extraction_attempt_id = ANY($1::text[])
		ORDER BY extraction_attempt_id
	`, attemptIDs)
	if err != nil {
		return nil, fmt.Errorf("querying grounded evidence brief coverage: %w", err)
	}
	defer rows.Close()

	coverage := make([]GroundedEvidenceBriefCoverage, 0, len(attemptIDs))
	for rows.Next() {
		var attemptID string
		var outputData []byte
		if err := rows.Scan(&attemptID, &outputData); err != nil {
			return nil, fmt.Errorf("scanning grounded evidence brief coverage: %w", err)
		}
		var output FrozenExtractorOutput
		if err := json.Unmarshal(outputData, &output); err != nil {
			return nil, fmt.Errorf("decoding extraction attempt %s output: %w", attemptID, err)
		}
		if output.RepositoryGoplsCoverage == nil {
			continue
		}
		coverage = append(coverage, GroundedEvidenceBriefCoverage{
			ExtractionAttemptID:     attemptID,
			RepositoryGoplsCoverage: *output.RepositoryGoplsCoverage,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating grounded evidence brief coverage: %w", err)
	}
	return coverage, nil
}
