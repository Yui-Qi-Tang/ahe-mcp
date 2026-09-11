package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// This opt-in wrapper preserves the complete recovery-v2 result, including its
// morphology ordering and partial-term relaxation. It uses the caller's same
// read-only snapshot; only a final empty baseline can reach the fallback.
func executeExperimentalHanEvidenceQuery(
	ctx context.Context,
	db sqlQueryer,
	input GroundedEvidenceBriefInput,
) ([]ProposalSearchResult, EvidenceQueryExecution, error) {
	baselineInput := input
	baselineInput.QueryMode = EvidenceQueryModeDeterministicLexicalRecovery
	matches, execution, err := executeGroundedEvidenceQuery(ctx, db, baselineInput)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	return recoverEmptyHanEvidenceQuery(ctx, db, input, matches, execution)
}

func recoverEmptyHanEvidenceQuery(
	ctx context.Context,
	db sqlQueryer,
	input GroundedEvidenceBriefInput,
	matches []ProposalSearchResult,
	execution EvidenceQueryExecution,
) ([]ProposalSearchResult, EvidenceQueryExecution, error) {
	execution.QueryMode = EvidenceQueryModeExperimentalHanRecoveryV1
	execution.PlanVersion = EvidenceQueryPlanExperimentalHanRecoveryV1
	execution.NormalizerVersion = EvidenceQueryNormalizerExperimentalHanV1
	if len(matches) > 0 {
		execution.FallbackStatus = "baseline_hit_preserved"
		return matches, execution, nil
	}
	han, ascii, terms, gate := hanEvidenceQueryTerms(input.Query)
	execution.FallbackStatus = gate
	if gate != "eligible" {
		return matches, execution, nil
	}

	// CompiledQuery is explicitly a JSON description of two match predicates,
	// not a tsquery. Only its ASCII component is a PostgreSQL simple tsquery.
	plan := struct {
		HanLiteralTerms    []string `json:"han_literal_terms"`
		ASCIISimpleTSQuery string   `json:"ascii_simple_tsquery"`
	}{HanLiteralTerms: han}
	if len(ascii) > 0 {
		err := db.queryRow(ctx, `SELECT plainto_tsquery('simple', $1)::text`, strings.Join(ascii, " ")).Scan(&plan.ASCIISimpleTSQuery)
		if err != nil {
			return nil, EvidenceQueryExecution{}, fmt.Errorf("compiling experimental Han ASCII query: %w", err)
		}
	}
	compiled, err := json.Marshal(plan)
	if err != nil {
		return nil, EvidenceQueryExecution{}, fmt.Errorf("encoding experimental Han query plan: %w", err)
	}
	matches, err = queryHanLiteralProposalSearchResults(ctx, db, input.ProposalListInput, han, plan.ASCIISimpleTSQuery, input.Limit+1)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	execution.RankingPolicy = "created_at_desc,id_asc_unscored"
	execution.Attempts = append(execution.Attempts, newEvidenceQueryAttempt(
		EvidenceQueryStrategyHanLiteralAllTerms,
		"han-literal+simple",
		string(compiled),
		terms,
		len(matches),
		len(matches) > input.Limit,
	))
	completion := EvidenceQueryCompletionBoundedNoMatch
	if len(matches) > 0 {
		completion = EvidenceQueryCompletionHanCandidates
	}
	bounded, finished := finishEvidenceQuery(matches, execution, input.Limit, completion)
	return bounded, finished, nil
}

// Delimiters and gates match the frozen CJK lab v1. There is no stemming,
// aliasing, arbitrary segmentation, or modification of stored statement text.
func hanEvidenceQueryTerms(query string) (han, ascii, terms []string, gate string) {
	input := ProposalSearchInput{Query: strings.TrimSpace(query), ProposalListInput: normalizeProposalListInput(ProposalListInput{})}
	if err := validateProposalSearchInput(input); err != nil {
		return nil, nil, nil, "invalid_query"
	}
	terms = strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，、；：！？。,;:!?", r)
	})
	if len(terms) > proposalSearchMaxTerms {
		return nil, nil, nil, "too_many_terms"
	}
	for _, term := range terms {
		hasHan, hasASCII := false, false
		for _, r := range term {
			switch {
			case unicode.Is(unicode.Han, r):
				hasHan = true
			case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
				hasASCII = true
			default:
				return nil, nil, nil, "unsupported_token"
			}
		}
		if hasHan && hasASCII {
			return nil, nil, nil, "mixed_token_requires_separator"
		}
		if hasHan {
			if len([]rune(term)) < 2 {
				return nil, nil, nil, "single_han_character"
			}
			han = append(han, term)
		} else {
			ascii = append(ascii, term)
		}
	}
	if len(han) == 0 {
		return nil, nil, nil, "no_han_terms"
	}
	return han, ascii, terms, "eligible"
}

func queryHanLiteralProposalSearchResults(
	ctx context.Context,
	db sqlQueryer,
	input ProposalListInput,
	han []string,
	asciiQuery string,
	limit int,
) ([]ProposalSearchResult, error) {
	rows, err := db.query(ctx, `
		SELECT po.proposal_occurrence_id, 0::double precision
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
		WHERE NOT EXISTS (
			SELECT 1 FROM unnest($10::text[]) AS term
			WHERE strpos(po.statement_text, term) = 0
		)
			AND ($1 = '' OR to_tsvector('simple', po.statement_text) @@ $1::tsquery)
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
		ORDER BY po.created_at DESC, po.proposal_occurrence_id ASC
		LIMIT $9
	`,
		asciiQuery,
		input.SourceSnapshotID,
		input.RepositorySnapshotID,
		input.SourceID,
		input.SourceVersion,
		input.AdmissionOutcome,
		input.SourceGenerationID,
		input.LifecycleScope,
		limit,
		han,
	)
	if err != nil {
		return nil, fmt.Errorf("searching experimental Han proposal records: %w", err)
	}
	return collectProposalSearchResults(ctx, db, rows)
}
