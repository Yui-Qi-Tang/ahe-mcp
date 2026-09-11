package evidenceingestion

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type compiledEvidenceQuery struct {
	simpleQuery  string
	simpleTerms  []string
	englishQuery string
	englishTerms []string
	relaxedQuery string
}

func normalizeEvidenceQueryMode(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return EvidenceQueryModeDeterministicLexicalRecovery, nil
	}
	switch mode {
	case EvidenceQueryModeExactLexical, EvidenceQueryModeDeterministicLexicalRecovery, EvidenceQueryModeExperimentalHanRecoveryV1:
		return mode, nil
	default:
		return "", newDomainError(ErrorInvalidInput, "query_mode %q is not supported", mode)
	}
}

// SearchProposalRecordsPage performs the existing exact lookup and reports its bounded execution.
func SearchProposalRecordsPage(ctx context.Context, pool *pgxpool.Pool, input ProposalSearchInput) (ProposalSearchQueryResult, error) {
	if pool == nil {
		return ProposalSearchQueryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return searchProposalRecordsPage(ctx, pgxDB{pool: pool}, input)
}

func searchProposalRecordsPage(ctx context.Context, db sqlDB, input ProposalSearchInput) (ProposalSearchQueryResult, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.ProposalListInput = normalizeProposalListInput(input.ProposalListInput)
	if err := validateProposalSearchInput(input); err != nil {
		return ProposalSearchQueryResult{}, err
	}

	result := ProposalSearchQueryResult{}
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		compiled, err := compileEvidenceQuery(ctx, tx, input.Query)
		if err != nil {
			return err
		}
		matches, err := queryProposalSearchResults(ctx, tx, input, input.Limit+1)
		if err != nil {
			return err
		}
		result.Execution = newEvidenceQueryExecution(input.Query, EvidenceQueryModeExactLexical, input.ProposalListInput)
		result.Execution.Attempts = []EvidenceQueryAttempt{newEvidenceQueryAttempt(
			EvidenceQueryStrategyExactSimple,
			"simple",
			compiled.simpleQuery,
			compiled.simpleTerms,
			len(matches),
			len(matches) > input.Limit,
		)}
		result.Matches, result.Execution.Truncated = boundProposalSearchMatches(matches, input.Limit)
		result.Execution.QueryCount = len(result.Execution.Attempts)
		result.Execution.CandidateCount = len(result.Matches)
		result.Execution.SearchCompleteWithinSurface = !result.Execution.Truncated
		result.Execution.CompletionReason = EvidenceQueryCompletionBoundedNoMatch
		if len(result.Matches) > 0 {
			result.Execution.CompletionReason = EvidenceQueryCompletionExactCandidates
		}
		return nil
	})
	if err != nil {
		return ProposalSearchQueryResult{}, err
	}
	return result, nil
}

func executeGroundedEvidenceQuery(
	ctx context.Context,
	db sqlQueryer,
	input GroundedEvidenceBriefInput,
) ([]ProposalSearchResult, EvidenceQueryExecution, error) {
	if input.QueryMode == EvidenceQueryModeExperimentalHanRecoveryV1 {
		return executeExperimentalHanEvidenceQuery(ctx, db, input)
	}
	compiled, err := compileEvidenceQuery(ctx, db, input.Query)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	execution := newEvidenceQueryExecution(input.Query, input.QueryMode, input.ProposalListInput)
	searchInput := ProposalSearchInput{
		Query:             input.Query,
		ProposalListInput: input.ProposalListInput,
	}

	matches, err := queryProposalSearchResults(ctx, db, searchInput, input.Limit+1)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	execution.Attempts = append(execution.Attempts, newEvidenceQueryAttempt(
		EvidenceQueryStrategyExactSimple,
		"simple",
		compiled.simpleQuery,
		compiled.simpleTerms,
		len(matches),
		len(matches) > input.Limit,
	))
	if input.QueryMode == EvidenceQueryModeExactLexical {
		completionReason := EvidenceQueryCompletionBoundedNoMatch
		if len(matches) > 0 {
			completionReason = EvidenceQueryCompletionExactCandidates
		}
		bounded, finished := finishEvidenceQuery(matches, execution, input.Limit, completionReason)
		return bounded, finished, nil
	}

	matches, err = queryEnglishMorphologyProposalSearchResults(ctx, db, searchInput, input.Limit+1)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	execution.Attempts = append(execution.Attempts, newEvidenceQueryAttempt(
		EvidenceQueryStrategyEnglishMorphology,
		"english",
		compiled.englishQuery,
		compiled.englishTerms,
		len(matches),
		len(matches) > input.Limit,
	))
	if len(matches) > 0 {
		bounded, finished := finishEvidenceQuery(matches, execution, input.Limit, EvidenceQueryCompletionMorphologyCandidates)
		return bounded, finished, nil
	}

	if len(compiled.englishTerms) >= 3 {
		matches, err = queryEnglishRelaxedProposalSearchResults(ctx, db, searchInput, input.Limit+1)
		if err != nil {
			return nil, EvidenceQueryExecution{}, err
		}
		execution.Attempts = append(execution.Attempts, newEvidenceQueryAttempt(
			EvidenceQueryStrategyEnglishRelaxation,
			"english",
			compiled.relaxedQuery,
			compiled.englishTerms,
			len(matches),
			len(matches) > input.Limit,
		))
		if len(matches) > 0 {
			bounded, finished := finishEvidenceQuery(matches, execution, input.Limit, EvidenceQueryCompletionRelatedCandidates)
			return bounded, finished, nil
		}
	}
	bounded, finished := finishEvidenceQuery(matches, execution, input.Limit, EvidenceQueryCompletionBoundedNoMatch)
	return bounded, finished, nil
}

func compileEvidenceQuery(ctx context.Context, db sqlQueryer, query string) (compiledEvidenceQuery, error) {
	var compiled compiledEvidenceQuery
	err := db.queryRow(ctx, `
		SELECT
			plainto_tsquery('simple', $1)::text,
			tsvector_to_array(to_tsvector('simple', $1)),
			plainto_tsquery('english', $1)::text,
			tsvector_to_array(to_tsvector('english', $1)),
			COALESCE((
				SELECT to_tsquery(
					'english',
					string_agg(quote_literal(lexeme), ' | ' ORDER BY lexeme)
				)::text
				FROM unnest(tsvector_to_array(to_tsvector('english', $1))) AS lexeme
				HAVING count(*) >= 3
			), '')
	`, query).Scan(
		&compiled.simpleQuery,
		&compiled.simpleTerms,
		&compiled.englishQuery,
		&compiled.englishTerms,
		&compiled.relaxedQuery,
	)
	if err != nil {
		return compiledEvidenceQuery{}, fmt.Errorf("compiling evidence query: %w", err)
	}
	return compiled, nil
}

func queryEnglishMorphologyProposalSearchResults(
	ctx context.Context,
	db sqlQueryer,
	input ProposalSearchInput,
	limit int,
) ([]ProposalSearchResult, error) {
	rows, err := db.query(ctx, `
		SELECT
			po.proposal_occurrence_id,
			ts_rank_cd(to_tsvector('english', po.statement_text), plainto_tsquery('english', $1))::double precision
		FROM proposal_occurrences po
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		LEFT JOIN source_snapshots ss ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		LEFT JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
		LEFT JOIN repository_source_heads rsh
			ON rsh.repo_id = rsg.repo_id
			AND rsh.extractor_name = rsg.extractor_name
			AND rsh.active_generation_id = rsg.source_generation_id
		WHERE to_tsvector('english', po.statement_text) @@ plainto_tsquery('english', $1)
			AND ($2 = '' OR ss.source_snapshot_id = $2)
			AND ($3 = '' OR rs.repository_snapshot_id = $3)
			AND ($4 = '' OR ss.source_id = $4)
			AND ($5 = '' OR ss.source_version = $5)
			AND ($6 = '' OR po.admission_outcome = $6)
			AND ($7 = '' OR rsg.source_generation_id = $7)
			AND (
				$8 = 'all'
				OR ($8 = 'active' AND (er.repository_snapshot_id IS NULL OR rsh.active_generation_id IS NOT NULL))
				OR ($8 = 'historical' AND er.repository_snapshot_id IS NOT NULL AND rsg.source_generation_id IS NOT NULL AND rsh.active_generation_id IS NULL)
			)
		ORDER BY
			ts_rank_cd(to_tsvector('english', po.statement_text), plainto_tsquery('english', $1)) DESC,
			po.created_at DESC,
			po.proposal_occurrence_id
		LIMIT $9
	`,
		input.Query,
		input.SourceSnapshotID,
		input.RepositorySnapshotID,
		input.SourceID,
		input.SourceVersion,
		input.AdmissionOutcome,
		input.SourceGenerationID,
		input.LifecycleScope,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("searching morphology-normalized proposal records: %w", err)
	}
	return collectProposalSearchResults(ctx, db, rows)
}

func queryEnglishRelaxedProposalSearchResults(
	ctx context.Context,
	db sqlQueryer,
	input ProposalSearchInput,
	limit int,
) ([]ProposalSearchResult, error) {
	rows, err := db.query(ctx, `
		WITH query_terms AS (
			SELECT lexeme
			FROM unnest(tsvector_to_array(to_tsvector('english', $1))) AS lexeme
		),
		query_plan AS (
			SELECT
				count(*)::integer AS term_count,
				to_tsquery('english', string_agg(quote_literal(lexeme), ' | ' ORDER BY lexeme)) AS relaxed_query
			FROM query_terms
			HAVING count(*) >= 3
		)
		SELECT
			po.proposal_occurrence_id,
			(matched.matched_term_count::double precision / qp.term_count::double precision)::double precision
		FROM proposal_occurrences po
		JOIN extraction_attempts ea ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		LEFT JOIN source_snapshots ss ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN repository_snapshots rs ON rs.repository_snapshot_id = er.repository_snapshot_id
		LEFT JOIN repository_source_generations rsg ON rsg.proposal_batch_id = po.proposal_batch_id
		LEFT JOIN repository_source_heads rsh
			ON rsh.repo_id = rsg.repo_id
			AND rsh.extractor_name = rsg.extractor_name
			AND rsh.active_generation_id = rsg.source_generation_id
		CROSS JOIN query_plan qp
		CROSS JOIN LATERAL (
			SELECT count(*)::integer AS matched_term_count
			FROM query_terms qt
			WHERE qt.lexeme = ANY(tsvector_to_array(to_tsvector('english', po.statement_text)))
		) matched
		WHERE to_tsvector('english', po.statement_text) @@ qp.relaxed_query
			AND matched.matched_term_count >= 2
			AND ($2 = '' OR ss.source_snapshot_id = $2)
			AND ($3 = '' OR rs.repository_snapshot_id = $3)
			AND ($4 = '' OR ss.source_id = $4)
			AND ($5 = '' OR ss.source_version = $5)
			AND ($6 = '' OR po.admission_outcome = $6)
			AND ($7 = '' OR rsg.source_generation_id = $7)
			AND (
				$8 = 'all'
				OR ($8 = 'active' AND (er.repository_snapshot_id IS NULL OR rsh.active_generation_id IS NOT NULL))
				OR ($8 = 'historical' AND er.repository_snapshot_id IS NOT NULL AND rsg.source_generation_id IS NOT NULL AND rsh.active_generation_id IS NULL)
			)
		ORDER BY
			matched.matched_term_count DESC,
			ts_rank_cd(to_tsvector('english', po.statement_text), qp.relaxed_query) DESC,
			po.created_at DESC,
			po.proposal_occurrence_id
		LIMIT $9
	`,
		input.Query,
		input.SourceSnapshotID,
		input.RepositorySnapshotID,
		input.SourceID,
		input.SourceVersion,
		input.AdmissionOutcome,
		input.SourceGenerationID,
		input.LifecycleScope,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("searching relaxed proposal records: %w", err)
	}
	return collectProposalSearchResults(ctx, db, rows)
}

func newEvidenceQueryExecution(query, mode string, filters ProposalListInput) EvidenceQueryExecution {
	planVersion := EvidenceQueryPlanRecoveryV2
	normalizerVersion := EvidenceQueryNormalizerRecoveryV1
	if mode == EvidenceQueryModeExactLexical {
		planVersion = EvidenceQueryPlanExactV1
		normalizerVersion = EvidenceQueryNormalizerSimpleV1
	} else if mode == EvidenceQueryModeExperimentalHanRecoveryV1 {
		planVersion = EvidenceQueryPlanExperimentalHanRecoveryV1
		normalizerVersion = EvidenceQueryNormalizerExperimentalHanV1
	}
	return EvidenceQueryExecution{
		OriginalQuery:              query,
		QueryMode:                  mode,
		PlanVersion:                planVersion,
		NormalizerVersion:          normalizerVersion,
		SearchSurface:              EvidenceQuerySearchSurfaceProposalStatement,
		SearchedFields:             []string{EvidenceQuerySearchSurfaceProposalStatement},
		SearchedRecordKinds:        []string{"proposal_occurrence"},
		EligibleSourceBindingKinds: []string{ProposalSourceBindingSourceSnapshot, ProposalSourceBindingRepositorySnapshot},
		Filters: EvidenceQueryFilters{
			SourceSnapshotID:     filters.SourceSnapshotID,
			RepositorySnapshotID: filters.RepositorySnapshotID,
			SourceGenerationID:   filters.SourceGenerationID,
			SourceID:             filters.SourceID,
			SourceVersion:        filters.SourceVersion,
			AdmissionOutcome:     filters.AdmissionOutcome,
			LifecycleScope:       filters.LifecycleScope,
		},
		Limit:                         filters.Limit,
		GlobalAbsenceInferenceAllowed: false,
		Attempts:                      []EvidenceQueryAttempt{},
	}
}

func newEvidenceQueryAttempt(
	strategy string,
	configuration string,
	compiledQuery string,
	normalizedTerms []string,
	candidateCount int,
	truncated bool,
) EvidenceQueryAttempt {
	return EvidenceQueryAttempt{
		Strategy:                strategy,
		TextSearchConfiguration: configuration,
		CompiledQuery:           compiledQuery,
		NormalizedQueryTerms:    append([]string(nil), normalizedTerms...),
		CandidateCount:          candidateCount,
		Truncated:               truncated,
	}
}

func finishEvidenceQuery(
	matches []ProposalSearchResult,
	execution EvidenceQueryExecution,
	limit int,
	completionReason string,
) ([]ProposalSearchResult, EvidenceQueryExecution) {
	bounded, truncated := boundProposalSearchMatches(matches, limit)
	execution.QueryCount = len(execution.Attempts)
	execution.CandidateCount = len(bounded)
	execution.Truncated = truncated
	execution.SearchCompleteWithinSurface = !truncated
	execution.CompletionReason = completionReason
	return bounded, execution
}

func boundProposalSearchMatches(matches []ProposalSearchResult, limit int) ([]ProposalSearchResult, bool) {
	if len(matches) <= limit {
		return matches, false
	}
	return matches[:limit], true
}
