package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
)

// These ID-only SQL copies deliberately preserve recovery-v2 predicates, filters,
// ordering and limit+1. Parity tests pin them to the native SELECT literals;
// unlike the native collector, they do not hydrate losing candidates.
const multisurfaceSimpleSQL = `
		SELECT
			po.proposal_occurrence_id,
			ts_rank_cd(to_tsvector('simple', po.statement_text), plainto_tsquery('simple', $1))::double precision
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
		WHERE to_tsvector('simple', po.statement_text) @@ plainto_tsquery('simple', $1)
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
			ts_rank_cd(to_tsvector('simple', po.statement_text), plainto_tsquery('simple', $1)) DESC,
			po.created_at DESC,
			po.proposal_occurrence_id
		LIMIT $9
	`
const multisurfaceEnglishSQL = `
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
	`
const multisurfaceRelaxedSQL = `
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
	`

type multisurfaceCandidate struct {
	ID             string   `json:"id"`
	Score          int      `json:"score"`
	Baseline       bool     `json:"baseline"`
	StatementTerms []string `json:"statement_terms"`
	SourceTerms    []string `json:"source_terms"`
	SourceStatus   string   `json:"source_status"`
	SnapshotID     string   `json:"snapshot_id"`
	ViewID         string   `json:"view_id"`
	RawHash        string   `json:"raw_hash"`
	RenderedHash   string   `json:"rendered_hash"`
}

func executeMultisurfaceEvidenceQuery(ctx context.Context, db sqlQueryer, input GroundedEvidenceBriefInput) ([]ProposalSearchResult, EvidenceQueryExecution, error) {
	practical := input.QueryMode == EvidenceQueryModePracticalMultisurfaceV1
	if practical && ctx.Err() != nil {
		return nil, EvidenceQueryExecution{}, ctx.Err()
	}
	baselineIDs, baseline, err := multisurfaceBaseline(ctx, db, input)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	han, englishText := multisurfaceQueryParts(input.Query)
	var english []string
	if err := db.queryRow(ctx, `SELECT tsvector_to_array(to_tsvector('english', regexp_replace($1, '[^[:alnum:]]', ' ', 'g')))`, englishText).Scan(&english); err != nil {
		return nil, EvidenceQueryExecution{}, fmt.Errorf("normalizing multisurface English terms: %w", err)
	}
	terms := append(append([]string{}, han...), english...)
	slices.Sort(terms)
	terms = slices.Compact(terms)
	eligibleHanAnchors := han
	var hanPolicy EvidencePracticalHanTermPolicy
	if practical {
		hanPolicy = practicalHanTermPolicy(han, english)
		eligibleHanAnchors = hanPolicy.EligibleAnchorTerms
	}
	var searched, excluded, invalid int
	queryCandidates := func(minEnglish int) ([]multisurfaceCandidate, error) {
		var payload []byte
		err := db.queryRow(ctx, multisurfaceCandidatesSQL,
			han, input.SourceSnapshotID, input.RepositorySnapshotID, input.SourceID,
			input.SourceVersion, input.AdmissionOutcome, input.SourceGenerationID,
			input.LifecycleScope, input.Limit+1, english, baselineIDs,
			BoundedSourceViewMaxRenderedBytesV1, BoundedSourceViewMaxSpansV1, BoundedSourceViewMaxSpanBytesV1,
			multisurfaceHanPattern(), minEnglish, eligibleHanAnchors,
		).Scan(&payload, &searched, &excluded, &invalid)
		if err != nil {
			var pgerr *pgconn.PgError
			if errors.As(err, &pgerr) && pgerr.Code == "22021" {
				return nil, newDomainError(ErrorInvalidUTF8, "multisurface identity text cannot be represented as PostgreSQL UTF-8 text")
			}
			return nil, fmt.Errorf("searching multisurface candidates: %w", err)
		}
		if invalid != 0 {
			return nil, newDomainError(ErrorQuotedHashMismatch, "in-scope bounded identity source failed renderer or content integrity validation")
		}
		var candidates []multisurfaceCandidate
		if err := json.Unmarshal(payload, &candidates); err != nil {
			return nil, fmt.Errorf("decoding multisurface candidates: %w", err)
		}
		return candidates, nil
	}
	candidates, err := queryCandidates(2)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	execution := newEvidenceQueryExecution(input.Query, input.QueryMode, input.ProposalListInput)
	execution.PlanVersion = EvidenceQueryPlanExperimentalMultisurfaceV1
	if practical {
		execution.PlanVersion = EvidenceQueryPlanPracticalMultisurfaceV2
		execution.PracticalRecovery = &EvidencePracticalRecovery{ConfiguredMinimumEnglishTerms: 2, HanTermPolicy: hanPolicy}
	}
	execution.NormalizerVersion = EvidenceQueryNormalizerExperimentalMultisurfaceV1
	execution.SearchSurface = EvidenceQuerySearchSurfaceProposalStatement + "+" + EvidenceQuerySearchSurfaceIdentitySource
	execution.SearchedFields = []string{EvidenceQuerySearchSurfaceProposalStatement, EvidenceQuerySearchSurfaceIdentitySource}
	execution.RankingPolicy = "matched_term_count_desc,created_at_desc,id_asc_not_confidence"
	execution.Multisurface = &EvidenceMultisurfaceExecution{
		Baseline: baseline, NormalizedQueryTerms: terms, SearchedSourceViews: searched, ExcludedSourceViews: excluded,
	}
	compiled, err := json.Marshal(struct {
		Han     []string `json:"han_bigrams"`
		English []string `json:"english_lexemes"`
	}{han, english})
	if err != nil {
		return nil, EvidenceQueryExecution{}, fmt.Errorf("encoding multisurface query plan: %w", err)
	}
	execution.Attempts = append(append([]EvidenceQueryAttempt{}, baseline.Attempts...), newEvidenceQueryAttempt(
		EvidenceQueryStrategyMultisurface, "han-bigram+english", string(compiled), terms, len(candidates), len(candidates) > input.Limit,
	))
	// Only a complete empty first pass permits recovery. Errors and excluded
	// sources never become a reason to broaden search, and the baseline is not rerun.
	if practical && len(candidates) == 0 && len(english) > 0 && !baseline.Truncated && excluded == 0 {
		if err := ctx.Err(); err != nil {
			return nil, EvidenceQueryExecution{}, err
		}
		candidates, err = queryCandidates(1)
		if err != nil {
			return nil, EvidenceQueryExecution{}, err
		}
		execution.PracticalRecovery.FallbackAttempted = true
		execution.PracticalRecovery.ConfiguredMinimumEnglishTerms = 1
		execution.Multisurface.SearchedSourceViews = searched
		execution.Multisurface.ExcludedSourceViews = excluded
		execution.Attempts = append(execution.Attempts, newEvidenceQueryAttempt(
			EvidenceQueryStrategyPracticalRecovery, "han-bigram+english", string(compiled), terms,
			len(candidates), len(candidates) > input.Limit,
		))
	}
	execution.Truncated = len(candidates) > input.Limit
	if execution.Truncated {
		candidates = candidates[:input.Limit]
	}
	execution.QueryCount = len(execution.Attempts)
	execution.CandidateCount = len(candidates)
	execution.SearchCompleteWithinSurface = !execution.Truncated && !baseline.Truncated && excluded == 0
	execution.CompletionReason = EvidenceQueryCompletionBoundedNoMatch
	if len(candidates) > 0 {
		execution.CompletionReason = EvidenceQueryCompletionMultisurfaceCandidates
		if practical {
			execution.CompletionReason = EvidenceQueryCompletionPracticalFirstCandidates
		}
	}
	if practical && execution.PracticalRecovery.FallbackAttempted {
		execution.CompletionReason = EvidenceQueryCompletionPracticalNoMatch
		if len(candidates) > 0 {
			execution.CompletionReason = EvidenceQueryCompletionPracticalCandidates
		}
	}

	results := make([]ProposalSearchResult, 0, len(candidates))
	sourceCache := make(map[string][]EvidenceRetrievalSpan)
	for _, candidate := range candidates {
		record, err := getProposalByOccurrenceID(ctx, db, candidate.ID)
		if err != nil {
			return nil, EvidenceQueryExecution{}, err
		}
		statement := EvidenceRetrievalSurface{
			Surface: EvidenceQuerySearchSurfaceProposalStatement, Status: "searched",
			MatchedTerms: candidate.StatementTerms, MissingTerms: multisurfaceMissingTerms(terms, candidate.StatementTerms),
		}
		source := EvidenceRetrievalSurface{
			Surface: EvidenceQuerySearchSurfaceIdentitySource, Status: candidate.SourceStatus,
			MatchedTerms: candidate.SourceTerms, MissingTerms: multisurfaceMissingTerms(terms, candidate.SourceTerms),
		}
		if source.Status != "not_applicable" {
			source.SourceSnapshotID, source.ExtractionViewID = candidate.SnapshotID, candidate.ViewID
			source.RawContentHash, source.RenderedContentHash = candidate.RawHash, candidate.RenderedHash
		}
		if source.Status == "searched" {
			spans, ok := sourceCache[candidate.ViewID]
			if !ok {
				spans, err = loadMultisurfaceMatchedSpans(ctx, db, candidate, han, english)
				if err != nil {
					return nil, EvidenceQueryExecution{}, err
				}
				sourceCache[candidate.ViewID] = spans
			}
			source.SpansTruncated = len(spans) > 8
			if source.SpansTruncated {
				spans = spans[:8]
			}
			source.MatchedSpans = append([]EvidenceRetrievalSpan{}, spans...)
			for i := range source.MatchedSpans {
				source.MatchedSpans[i].WithinProposalSourceRefs = multisurfaceWithinRefs(source.MatchedSpans[i].SourceRef, record.SourceRefs)
			}
		}
		results = append(results, ProposalSearchResult{Record: record, Rank: float64(candidate.Score), RetrievalBasis: &EvidenceRetrievalBasis{
			NormalizedQueryTerms: append([]string{}, terms...), BaselineMatched: candidate.Baseline,
			Surfaces: []EvidenceRetrievalSurface{statement, source},
		}})
	}
	return results, execution, nil
}

func practicalHanTermPolicy(han, english []string) EvidencePracticalHanTermPolicy {
	policy := EvidencePracticalHanTermPolicy{
		Version:        EvidencePracticalHanTermPolicyVersionV1,
		AuxiliaryTerms: []string{}, EligibleAnchorTerms: []string{},
	}
	// This exact list controls only supplemental Han eligibility. All original
	// terms still participate in baseline search, scoring and retrieval details.
	for _, term := range han {
		switch term {
		case "影響", "影响", "造成", "導致", "导致", "發生", "发生",
			"是否", "哪些", "什麼", "什么", "多少", "如何":
			policy.AuxiliaryTerms = append(policy.AuxiliaryTerms, term)
		default:
			policy.EligibleAnchorTerms = append(policy.EligibleAnchorTerms, term)
		}
	}
	if len(han) > 0 && len(policy.EligibleAnchorTerms) == 0 && len(english) == 0 {
		policy.AuxiliaryOnlyQueryPreserved = true
		policy.EligibleAnchorTerms = append(policy.EligibleAnchorTerms, han...)
	}
	slices.Sort(policy.AuxiliaryTerms)
	policy.AuxiliaryTerms = slices.Compact(policy.AuxiliaryTerms)
	slices.Sort(policy.EligibleAnchorTerms)
	policy.EligibleAnchorTerms = slices.Compact(policy.EligibleAnchorTerms)
	return policy
}

func multisurfaceBaseline(ctx context.Context, db sqlQueryer, input GroundedEvidenceBriefInput) ([]string, EvidenceQueryExecution, error) {
	compiled, err := compileEvidenceQuery(ctx, db, input.Query)
	if err != nil {
		return nil, EvidenceQueryExecution{}, err
	}
	execution := newEvidenceQueryExecution(input.Query, EvidenceQueryModeDeterministicLexicalRecovery, input.ProposalListInput)
	plans := []struct {
		sql, strategy, query string
		terms                []string
		configuration        string
	}{
		{multisurfaceSimpleSQL, EvidenceQueryStrategyExactSimple, compiled.simpleQuery, compiled.simpleTerms, "simple"},
		{multisurfaceEnglishSQL, EvidenceQueryStrategyEnglishMorphology, compiled.englishQuery, compiled.englishTerms, "english"},
		{multisurfaceRelaxedSQL, EvidenceQueryStrategyEnglishRelaxation, compiled.relaxedQuery, compiled.englishTerms, "english"},
	}
	var ids []string
	for i, plan := range plans {
		if i == 2 && len(compiled.englishTerms) < 3 {
			break
		}
		rows, err := db.query(ctx, plan.sql, input.Query, input.SourceSnapshotID, input.RepositorySnapshotID,
			input.SourceID, input.SourceVersion, input.AdmissionOutcome, input.SourceGenerationID, input.LifecycleScope, input.Limit+1)
		if err != nil {
			return nil, EvidenceQueryExecution{}, fmt.Errorf("searching multisurface baseline: %w", err)
		}
		ids, err = collectMultisurfaceIDs(rows)
		if err != nil {
			return nil, EvidenceQueryExecution{}, err
		}
		execution.Attempts = append(execution.Attempts, newEvidenceQueryAttempt(plan.strategy, plan.configuration, plan.query, plan.terms, len(ids), len(ids) > input.Limit))
		if i > 0 && len(ids) > 0 {
			execution.CompletionReason = EvidenceQueryCompletionMorphologyCandidates
			if i == 2 {
				execution.CompletionReason = EvidenceQueryCompletionRelatedCandidates
			}
			break
		}
	}
	if len(ids) == 0 {
		execution.CompletionReason = EvidenceQueryCompletionBoundedNoMatch
	}
	execution.Truncated = len(ids) > input.Limit
	if execution.Truncated {
		ids = ids[:input.Limit]
	}
	execution.CandidateCount, execution.QueryCount = len(ids), len(execution.Attempts)
	execution.SearchCompleteWithinSurface = !execution.Truncated
	return ids, execution, nil
}

func collectMultisurfaceIDs(rows sqlRows) ([]string, error) {
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		var rank float64
		if err := rows.Scan(&id, &rank); err != nil {
			return nil, fmt.Errorf("scanning multisurface baseline: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading multisurface baseline: %w", err)
	}
	return ids, nil
}

// Han runs yield adjacent bigrams, never case-specific words. All punctuation,
// including straight/curly apostrophes, separates non-Han words. English stemming
// is exclusively PostgreSQL's responsibility; this does not translate language.
func multisurfaceQueryParts(query string) ([]string, string) {
	var han []string
	var words []string
	var run []rune
	kind := 0
	flush := func() {
		if kind == 1 {
			for i := 1; i < len(run); i++ {
				han = append(han, string(run[i-1:i+1]))
			}
		} else if len(run) > 0 {
			words = append(words, string(run))
		}
		run = nil
	}
	for _, r := range query {
		next := 0
		if unicode.Is(unicode.Han, r) {
			next = 1
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			next = 2
		}
		if next == 0 || next != kind {
			flush()
		}
		kind = next
		if next != 0 {
			run = append(run, r)
		}
	}
	flush()
	slices.Sort(han)
	return slices.Compact(han), strings.Join(words, " ")
}

func multisurfaceMissingTerms(terms, matched []string) []string {
	missing := []string{}
	for _, term := range terms {
		if !slices.Contains(matched, term) {
			missing = append(missing, term)
		}
	}
	return missing
}

// PostgreSQL does not expose Unicode script classes. Generate its literal
// character class from the same Go Han table used by the query tokenizer.
func multisurfaceHanPattern() string {
	var pattern strings.Builder
	pattern.WriteByte('[')
	writeRange := func(lo, hi, stride rune) {
		if stride == 1 && lo != hi {
			pattern.WriteRune(lo)
			pattern.WriteByte('-')
			pattern.WriteRune(hi)
			return
		}
		for r := lo; r <= hi; r += stride {
			pattern.WriteRune(r)
		}
	}
	for _, r := range unicode.Han.R16 {
		writeRange(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	for _, r := range unicode.Han.R32 {
		writeRange(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	pattern.WriteByte(']')
	return pattern.String()
}

func multisurfaceWithinRefs(span ResolvedSourceRef, refs []ResolvedSourceRef) bool {
	for _, ref := range refs {
		if ref.ExtractionViewID == span.ExtractionViewID && span.ExtractionViewID != "" &&
			ref.StartByte <= span.StartByte && ref.EndByte >= span.EndByte {
			return true
		}
	}
	return false
}

// One bounded view and one batched span query per selected view; never a
// per-span query or source materialization for losing proposal candidates.
func loadMultisurfaceMatchedSpans(ctx context.Context, db sqlQueryer, candidate multisurfaceCandidate, han, english []string) ([]EvidenceRetrievalSpan, error) {
	view, err := loadBoundedSourceViewFromQueryer(ctx, db, candidate.SnapshotID, candidate.ViewID)
	if err != nil {
		return nil, err
	}
	if view.Status != BoundedSourceViewStatusAvailable || view.Input == nil ||
		view.Input.RawContentHash != candidate.RawHash || view.Input.RenderedContentHash != candidate.RenderedHash {
		return nil, newDomainError(ErrorOccurrenceConflict, "selected multisurface source differs from its search observation")
	}
	rows, err := db.query(ctx, `
		SELECT sce.span_id, ARRAY(
			SELECT term FROM (
				SELECT term FROM unnest($2::text[]) term WHERE strpos(convert_from(sce.quoted_text,'UTF8'),term)>0
				UNION
				SELECT term FROM unnest($3::text[]) term
				WHERE term=ANY(tsvector_to_array(to_tsvector('english', regexp_replace(regexp_replace(convert_from(sce.quoted_text,'UTF8'), $4, ' ', 'g'), '[^[:alnum:]]', ' ', 'g'))))
			) matched ORDER BY term
		)
		FROM span_catalog_entries sce WHERE sce.extraction_view_id=$1
		ORDER BY sce.start_byte,sce.span_id
	`, candidate.ViewID, han, english, multisurfaceHanPattern())
	if err != nil {
		return nil, fmt.Errorf("matching bounded source spans: %w", err)
	}
	defer rows.Close()
	byID := make(map[string]ExtractorInputSpan, len(view.Input.Spans))
	for _, span := range view.Input.Spans {
		byID[span.SpanID] = span
	}
	matches := []EvidenceRetrievalSpan{}
	for rows.Next() {
		var id string
		var terms []string
		if err := rows.Scan(&id, &terms); err != nil {
			return nil, fmt.Errorf("scanning matched source spans: %w", err)
		}
		if len(terms) == 0 {
			continue
		}
		span, ok := byID[id]
		if !ok {
			return nil, newDomainError(ErrorOccurrenceConflict, "source match span was absent from its validated view")
		}
		matches = append(matches, EvidenceRetrievalSpan{
			SourceRef: ResolvedSourceRef{ExtractionViewID: candidate.ViewID, SpanID: id,
				StartByte: span.StartByte, EndByte: span.EndByte, QuotedTextHash: span.QuotedTextHash, QuotedText: span.Text},
			MatchedTerms: terms,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading matched source spans: %w", err)
	}
	return matches, nil
}

const multisurfaceCandidatesSQL = `
	WITH eligible AS MATERIALIZED (
		SELECT DISTINCT po.proposal_occurrence_id,po.statement_text,po.created_at,
			ss.source_snapshot_id,ss.source_system,ss.raw_content_hash,er.extraction_view_id
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
		WHERE ($2 = '' OR ss.source_snapshot_id = $2)
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
	), source_views AS MATERIALIZED (
		SELECT DISTINCT e.source_snapshot_id,e.raw_content_hash,ev.extraction_view_id,
			ev.rendered_content_hash,ev.renderer_name,ev.renderer_version,
			(octet_length(ev.rendered_content)>$12 OR metrics.span_count>$13 OR metrics.max_bytes>$14) over_budget
		FROM eligible e
		JOIN extraction_views ev ON ev.extraction_view_id=e.extraction_view_id AND ev.source_snapshot_id=e.source_snapshot_id
		CROSS JOIN LATERAL (
			SELECT count(*) span_count,COALESCE(max(GREATEST(sce.end_byte-sce.start_byte,octet_length(sce.quoted_text))),0) max_bytes
			FROM span_catalog_entries sce WHERE sce.extraction_view_id=ev.extraction_view_id
		) metrics
		WHERE e.source_system='manual_text'
	), validated_views AS MATERIALIZED (
		SELECT v.*, CASE WHEN over_budget THEN NULL ELSE ev.rendered_content END rendered_content,
			CASE WHEN over_budget THEN false ELSE
			v.raw_content_hash!=v.rendered_content_hash
			OR v.rendered_content_hash!='sha256:'||encode(sha256(ev.rendered_content),'hex')
			OR v.renderer_name!='manual-text-identity' OR v.renderer_version!='v1'
			END invalid
		FROM source_views v JOIN extraction_views ev USING (extraction_view_id)
	), source_matches AS MATERIALIZED (
		SELECT v.*, CASE WHEN over_budget THEN ARRAY[]::text[] ELSE ARRAY(
			SELECT term FROM unnest($1::text[]) term
			WHERE strpos(CASE WHEN invalid THEN '' ELSE convert_from(v.rendered_content,'UTF8') END,term)>0 ORDER BY term
		) END han_matches,
		CASE WHEN over_budget THEN ARRAY[]::text[] ELSE ARRAY(
			SELECT term FROM unnest($10::text[]) term
			WHERE term=ANY(tsvector_to_array(to_tsvector('english',
				regexp_replace(regexp_replace(CASE WHEN invalid THEN '' ELSE convert_from(v.rendered_content,'UTF8') END, $15, ' ', 'g'), '[^[:alnum:]]', ' ', 'g')))) ORDER BY term
		) END english_matches
		FROM validated_views v
	), matched AS (
		SELECT e.*, COALESCE(s.rendered_content_hash,'') rendered_content_hash,
			CASE WHEN s.extraction_view_id IS NULL THEN 'not_applicable' WHEN s.over_budget THEN 'over_budget' ELSE 'searched' END source_status,
			ARRAY(SELECT term FROM unnest($1::text[]) term WHERE strpos(e.statement_text,term)>0 ORDER BY term) statement_han,
			ARRAY(SELECT term FROM unnest($10::text[]) term
				WHERE term=ANY(tsvector_to_array(to_tsvector('english',regexp_replace(regexp_replace(e.statement_text, $15, ' ', 'g'), '[^[:alnum:]]', ' ', 'g')))) ORDER BY term) statement_english,
			COALESCE(s.han_matches,ARRAY[]::text[]) source_han,
			COALESCE(s.english_matches,ARRAY[]::text[]) source_english,
			e.proposal_occurrence_id=ANY($11::text[]) baseline
		FROM eligible e LEFT JOIN source_matches s ON s.extraction_view_id=e.extraction_view_id AND s.source_snapshot_id=e.source_snapshot_id
	), candidates AS (
		SELECT *, ARRAY(SELECT DISTINCT term FROM unnest(statement_han||statement_english) term ORDER BY term) statement_terms,
			ARRAY(SELECT DISTINCT term FROM unnest(source_han||source_english) term ORDER BY term) source_terms,
			(SELECT count(DISTINCT term)::integer FROM unnest(statement_han||statement_english||source_han||source_english) term) score
		FROM matched
		WHERE baseline OR statement_han && $17::text[] OR source_han && $17::text[]
			OR (cardinality($10::text[])>0 AND (
				cardinality(statement_english)>=LEAST($16::integer,cardinality($10::text[]))
				OR cardinality(source_english)>=LEAST($16::integer,cardinality($10::text[]))))
	), bounded AS (
		SELECT * FROM candidates ORDER BY score DESC,created_at DESC,proposal_occurrence_id LIMIT $9
	)
	SELECT COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'id',proposal_occurrence_id,'score',score,'baseline',baseline,
		'statement_terms',statement_terms,'source_terms',source_terms,'source_status',source_status,
		'snapshot_id',COALESCE(source_snapshot_id,''),'view_id',COALESCE(extraction_view_id,''),
		'raw_hash',COALESCE(raw_content_hash,''),'rendered_hash',rendered_content_hash
	) ORDER BY score DESC,created_at DESC,proposal_occurrence_id) FROM bounded),'[]'::jsonb),
		(SELECT count(*)::integer FROM source_views WHERE NOT over_budget),
		(SELECT count(*)::integer FROM source_views WHERE over_budget),
		(SELECT count(*)::integer FROM validated_views WHERE invalid)
`
