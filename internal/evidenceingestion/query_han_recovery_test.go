package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestExperimentalHanQueryModeBoundary(t *testing.T) {
	for _, mode := range []string{"", EvidenceQueryModeExactLexical, EvidenceQueryModeDeterministicLexicalRecovery, EvidenceQueryModeExperimentalHanRecoveryV1} {
		normalized, err := normalizeEvidenceQueryMode(" " + mode + " ")
		if err != nil {
			t.Fatal(err)
		}
		if mode == "" {
			if normalized != EvidenceQueryModeDeterministicLexicalRecovery {
				t.Fatalf("default changed to %q", normalized)
			}
		} else if normalized != mode {
			t.Fatalf("normalized mode = %q, want %q", normalized, mode)
		}
		execution := newEvidenceQueryExecution("地震 芮氏", normalized, ProposalListInput{})
		if execution.FallbackStatus != "" || execution.RankingPolicy != "" {
			t.Fatalf("unevaluated execution has fallback metadata: %+v", execution)
		}
		if mode == EvidenceQueryModeExperimentalHanRecoveryV1 &&
			(execution.PlanVersion != EvidenceQueryPlanExperimentalHanRecoveryV1 || execution.NormalizerVersion != EvidenceQueryNormalizerExperimentalHanV1) {
			t.Fatalf("experimental versions = %+v", execution)
		}
	}
}

func TestExperimentalHanTermsFrozenLabParity(t *testing.T) {
	queries := []string{
		"地震，芮氏", " Orion\t索引重建\n", "Orion索引重建", "Orion-Beta 索引重建",
		"地 芮", "proposal lifecycle", "地震 café", "", strings.Repeat("地震，", 17),
		"地震，芮氏，地震", "地震 42", "地震🙂", "地震'芮氏", "地震　芮氏", "。！？", "\xff",
	}
	for _, tc := range cjkLabCases {
		queries = append(queries, tc.Query)
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			han, ascii, _, gate := hanEvidenceQueryTerms(query)
			wantHan, wantASCII, wantGate := cjkLabTerms(query)
			if !slices.Equal(han, wantHan) || !slices.Equal(ascii, wantASCII) || gate != wantGate {
				t.Fatalf("terms = %v/%v/%q, lab = %v/%v/%q", han, ascii, gate, wantHan, wantASCII, wantGate)
			}
		})
	}
	_, _, terms, gate := hanEvidenceQueryTerms("地震，Orion；芮氏，地震")
	if gate != "eligible" || !slices.Equal(terms, []string{"地震", "Orion", "芮氏", "地震"}) {
		t.Fatalf("exact ordered terms = %v, gate = %q", terms, gate)
	}
}

func TestExperimentalHanPreservesBaselineHitsAndTruncation(t *testing.T) {
	input := GroundedEvidenceBriefInput{
		Query: "Orion Search 是否已恢復", QueryMode: EvidenceQueryModeExperimentalHanRecoveryV1,
		ProposalListInput: ProposalListInput{Limit: 1},
	}
	baseline := newEvidenceQueryExecution(input.Query, EvidenceQueryModeDeterministicLexicalRecovery, input.ProposalListInput)
	baseline.Attempts = []EvidenceQueryAttempt{{Strategy: EvidenceQueryStrategyEnglishRelaxation, CandidateCount: 2, Truncated: true}}
	// A partial three-term relaxation hit remains a hit; the fallback does not
	// repair semantic completeness or replace this baseline's ranking.
	matches, baseline := finishEvidenceQuery([]ProposalSearchResult{{Rank: 0.75}, {Rank: 0.5}}, baseline, 1, EvidenceQueryCompletionRelatedCandidates)
	got, execution, err := recoverEmptyHanEvidenceQuery(context.Background(), &hanRecoveryTestQueryer{t: t}, input, matches, baseline)
	if err != nil {
		t.Fatal(err)
	}
	want := baseline
	want.QueryMode = EvidenceQueryModeExperimentalHanRecoveryV1
	want.PlanVersion = EvidenceQueryPlanExperimentalHanRecoveryV1
	want.NormalizerVersion = EvidenceQueryNormalizerExperimentalHanV1
	want.FallbackStatus = "baseline_hit_preserved"
	if !reflect.DeepEqual(got, matches) || !reflect.DeepEqual(execution, want) {
		t.Fatalf("baseline changed: matches=%+v execution=%+v", got, execution)
	}
}

func TestExperimentalHanIneligibleQueryDoesNotSearch(t *testing.T) {
	for _, query := range []string{"Orion quasar", "Orion索引重建", "地 芮", "地震 café", ""} {
		t.Run(query, func(t *testing.T) {
			input := GroundedEvidenceBriefInput{Query: query, ProposalListInput: ProposalListInput{Limit: 20}}
			baseline := newEvidenceQueryExecution(query, EvidenceQueryModeDeterministicLexicalRecovery, input.ProposalListInput)
			_, baseline = finishEvidenceQuery(nil, baseline, input.Limit, EvidenceQueryCompletionBoundedNoMatch)
			matches, execution, err := recoverEmptyHanEvidenceQuery(context.Background(), &hanRecoveryTestQueryer{t: t}, input, nil, baseline)
			_, _, _, wantGate := hanEvidenceQueryTerms(query)
			if err != nil || len(matches) != 0 || execution.FallbackStatus != wantGate || execution.RankingPolicy != "" || execution.QueryCount != 0 || execution.CompletionReason != baseline.CompletionReason {
				t.Fatalf("ineligible result: matches=%v execution=%+v error=%v", matches, execution, err)
			}
		})
	}
}

func TestExperimentalHanFallbackKeepsJoinsFiltersAndStatementSurface(t *testing.T) {
	input := ProposalListInput{
		SourceSnapshotID: "source-snapshot", RepositorySnapshotID: "repository-snapshot",
		SourceID: "source", SourceVersion: "version", AdmissionOutcome: "audit_only",
		SourceGenerationID: "generation", LifecycleScope: "historical", Limit: 100,
	}
	var queries []string
	var arguments [][]any
	db := &hanRecoveryTestQueryer{t: t, queryFn: func(query string, args []any) (sqlRows, error) {
		queries = append(queries, compactSQL(query))
		arguments = append(arguments, args)
		return &mockRows{index: -1}, nil
	}}
	_, err := queryProposalSearchResults(context.Background(), db, ProposalSearchInput{Query: "Orion 索引重建", ProposalListInput: input}, 101)
	if err != nil {
		t.Fatal(err)
	}
	han := []string{"索引重建"}
	_, err = queryHanLiteralProposalSearchResults(context.Background(), db, input, han, "'orion'", 101)
	if err != nil {
		t.Fatal(err)
	}
	for _, boundaries := range [][2]string{{"FROM proposal_occurrences", "WHERE"}, {"AND ($2 = ''", "ORDER BY"}} {
		segment := func(query string) string {
			start := strings.Index(query, boundaries[0])
			if start < 0 {
				t.Fatalf("missing SQL boundaries %v", boundaries)
			}
			end := strings.Index(query[start:], boundaries[1])
			if end < 0 {
				t.Fatalf("missing SQL boundaries %v", boundaries)
			}
			end += start
			return query[start:end]
		}
		if segment(queries[0]) != segment(queries[1]) {
			t.Fatalf("joins or filters differ: %s", queries[1])
		}
	}
	if !reflect.DeepEqual(arguments[0][1:], arguments[1][1:9]) || arguments[1][0] != "'orion'" || !reflect.DeepEqual(arguments[1][9], han) {
		t.Fatalf("fallback arguments = %v", arguments[1])
	}
	for _, fragment := range []string{
		"SELECT po.proposal_occurrence_id, 0::double precision",
		"NOT EXISTS ( SELECT 1 FROM unnest($10::text[]) AS term WHERE strpos(po.statement_text, term) = 0 )",
		"AND ($1 = '' OR to_tsvector('simple', po.statement_text) @@ $1::tsquery)",
		"ORDER BY po.created_at DESC, po.proposal_occurrence_id ASC LIMIT $9",
	} {
		if !strings.Contains(queries[1], fragment) {
			t.Fatalf("missing fallback SQL contract %q", fragment)
		}
	}
	if strings.Contains(queries[1], "source_body") || strings.Contains(queries[1], "raw_content") {
		t.Fatal("fallback expanded text search surface")
	}
}

func TestExperimentalHanRunsOnlyAfterFinalEmptyBaseline(t *testing.T) {
	for _, tc := range []struct {
		query string
		terms []string
		ascii string
	}{
		{"地震 芮氏", []string{"地震", "芮氏"}, ""},
		{"Orion 索引重建", []string{"Orion", "索引重建"}, "'orion'"},
		{"Orion Search 索引重建", []string{"Orion", "Search", "索引重建"}, "'orion' & 'search'"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var queries []string
			var compileCalls int
			db := &hanRecoveryTestQueryer{t: t}
			db.queryFn = func(query string, args []any) (sqlRows, error) {
				queries = append(queries, compactSQL(query))
				if args[8] != 21 {
					t.Fatalf("limit = %v, want limit+1", args[8])
				}
				return &mockRows{index: -1}, nil
			}
			db.rowFn = func(_ string, _ []any) sqlRow {
				compileCalls++
				return hanRecoveryTestRow(func(dest ...any) error {
					if len(dest) == 1 {
						*dest[0].(*string) = tc.ascii
						return nil
					}
					*dest[0].(*string), *dest[2].(*string), *dest[4].(*string) = "simple", "english", "relaxed"
					*dest[1].(*[]string), *dest[3].(*[]string) = tc.terms, tc.terms
					return nil
				})
			}
			input := GroundedEvidenceBriefInput{Query: tc.query, QueryMode: EvidenceQueryModeExperimentalHanRecoveryV1, ProposalListInput: ProposalListInput{Limit: 20, AdmissionOutcome: "audit_only", LifecycleScope: "active"}}
			matches, execution, err := executeGroundedEvidenceQuery(context.Background(), db, input)
			if err != nil {
				t.Fatal(err)
			}
			wantQueries := 3
			if len(tc.terms) >= 3 {
				wantQueries++
				if execution.Attempts[2].Strategy != EvidenceQueryStrategyEnglishRelaxation {
					t.Fatal("fallback skipped final baseline relaxation")
				}
			}
			wantCompileCalls := 1
			if tc.ascii != "" {
				wantCompileCalls++
			}
			if len(matches) != 0 || len(queries) != wantQueries || compileCalls != wantCompileCalls || execution.QueryCount != wantQueries || execution.FallbackStatus != "eligible" || execution.RankingPolicy != "created_at_desc,id_asc_unscored" || execution.CompletionReason != EvidenceQueryCompletionBoundedNoMatch || execution.Truncated || !execution.SearchCompleteWithinSurface {
				t.Fatalf("fallback execution = %+v; queries=%d compiles=%d", execution, len(queries), compileCalls)
			}
			attempt := execution.Attempts[len(execution.Attempts)-1]
			var plan struct {
				HanLiteralTerms    []string `json:"han_literal_terms"`
				ASCIISimpleTSQuery string   `json:"ascii_simple_tsquery"`
			}
			if err := json.Unmarshal([]byte(attempt.CompiledQuery), &plan); err != nil {
				t.Fatal(err)
			}
			wantHan, _, _, _ := hanEvidenceQueryTerms(tc.query)
			if attempt.Strategy != EvidenceQueryStrategyHanLiteralAllTerms || attempt.TextSearchConfiguration != "han-literal+simple" || !slices.Equal(attempt.NormalizedQueryTerms, tc.terms) || !slices.Equal(plan.HanLiteralTerms, wantHan) || plan.ASCIISimpleTSQuery != tc.ascii || execution.GlobalAbsenceInferenceAllowed {
				t.Fatalf("fallback metadata = %+v", execution)
			}
		})
	}
}

func TestExperimentalHanBaselineErrorStopsFallback(t *testing.T) {
	wantErr := errors.New("baseline compile failed")
	db := &hanRecoveryTestQueryer{t: t, rowFn: func(string, []any) sqlRow {
		return hanRecoveryTestRow(func(...any) error { return wantErr })
	}}
	_, _, err := executeGroundedEvidenceQuery(context.Background(), db, GroundedEvidenceBriefInput{QueryMode: EvidenceQueryModeExperimentalHanRecoveryV1})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

type hanRecoveryTestQueryer struct {
	t       *testing.T
	queryFn func(string, []any) (sqlRows, error)
	rowFn   func(string, []any) sqlRow
}

func (db *hanRecoveryTestQueryer) query(_ context.Context, query string, args ...any) (sqlRows, error) {
	db.t.Helper()
	if db.queryFn == nil {
		db.t.Fatal("unexpected rows query")
	}
	return db.queryFn(query, args)
}

func (db *hanRecoveryTestQueryer) queryRow(_ context.Context, query string, args ...any) sqlRow {
	db.t.Helper()
	if db.rowFn == nil {
		db.t.Fatal("unexpected row query")
	}
	return db.rowFn(query, args)
}

type hanRecoveryTestRow func(...any) error

func (row hanRecoveryTestRow) Scan(dest ...any) error { return row(dest...) }
