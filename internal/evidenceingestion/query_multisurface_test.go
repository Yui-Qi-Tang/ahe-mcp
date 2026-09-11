package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMultisurfaceTerms(t *testing.T) {
	for _, tc := range []struct {
		query   string
		han     []string
		english string
	}{
		{"Atlas地震，地震！索引重建", []string{"地震", "引重", "索引", "重建"}, "Atlas"},
		{"地震Atlas／planned—rebuilding", []string{"地震"}, "Atlas planned rebuilding"},
		{"Atlas's recovery", nil, "Atlas s recovery"},
		{"Atlas’s recovery", nil, "Atlas s recovery"},
		{"地 芮，🙂。", nil, ""},
		{"地震，芮氏；地震", []string{"地震", "芮氏"}, ""},
		{"退款refund完成42", []string{"完成", "退款"}, "refund 42"},
		{"café\tPAYMENT", nil, "café PAYMENT"},
		{"", nil, ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			han, english := multisurfaceQueryParts(tc.query)
			if !slices.Equal(han, tc.han) || english != tc.english {
				t.Fatalf("parts=%v/%q, want %v/%q", han, english, tc.han, tc.english)
			}
		})
	}
}

func TestMultisurfaceHanPatternUsesSameUnicodeTable(t *testing.T) {
	pattern, err := regexp.Compile(multisurfaceHanPattern())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []rune("Atlas地震〆〇漢龥𠀀𰀀，’9🙂") {
		if got, want := pattern.MatchString(string(r)), unicode.Is(unicode.Han, r); got != want {
			t.Fatalf("rune %U Han=%v want %v", r, got, want)
		}
	}
}

func TestMultisurfaceBaselineSQLParity(t *testing.T) {
	input := ProposalSearchInput{Query: "service recovery timing", ProposalListInput: ProposalListInput{
		SourceSnapshotID: "srcsnap:1", RepositorySnapshotID: "reposnap:2", SourceID: "source", SourceVersion: "version",
		AdmissionOutcome: "audit_only", SourceGenerationID: "generation", LifecycleScope: "historical", Limit: 2,
	}}
	var nativeSQL []string
	var nativeArgs [][]any
	db := &hanRecoveryTestQueryer{t: t, queryFn: func(query string, args []any) (sqlRows, error) {
		nativeSQL = append(nativeSQL, query)
		nativeArgs = append(nativeArgs, args)
		return &mockRows{index: -1}, nil
	}}
	for _, fn := range []func(context.Context, sqlQueryer, ProposalSearchInput, int) ([]ProposalSearchResult, error){
		queryProposalSearchResults, queryEnglishMorphologyProposalSearchResults, queryEnglishRelaxedProposalSearchResults,
	} {
		if _, err := fn(context.Background(), db, input, 3); err != nil {
			t.Fatal(err)
		}
	}
	for i, sql := range []string{multisurfaceSimpleSQL, multisurfaceEnglishSQL, multisurfaceRelaxedSQL} {
		if sql != nativeSQL[i] {
			t.Fatalf("baseline SQL %d differs from native literal", i)
		}
	}
	db.rowFn = func(_ string, _ []any) sqlRow { return multisurfaceCompileRow([]string{"recover", "servic", "time"}) }
	calls := 0
	db.queryFn = func(query string, args []any) (sqlRows, error) {
		if query != nativeSQL[calls] || !reflect.DeepEqual(args, nativeArgs[calls]) {
			t.Fatalf("baseline %d differs in SQL/args", calls)
		}
		calls++
		return &mockRows{index: -1}, nil
	}
	ids, execution, err := multisurfaceBaseline(context.Background(), db, GroundedEvidenceBriefInput{Query: input.Query, ProposalListInput: input.ProposalListInput})
	if err != nil || calls != 3 || len(ids) != 0 || execution.CompletionReason != EvidenceQueryCompletionBoundedNoMatch {
		t.Fatalf("baseline=%v/%+v error=%v", ids, execution, err)
	}
}

func TestMultisurfaceBaselineKeepsRecoveryV2Decisions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		terms      []string
		rows       [][]string
		want       []string
		completion string
		truncated  bool
	}{
		{"simple is not final", []string{"a", "b"}, [][]string{{"simple-only"}, {"english"}}, []string{"english"}, EvidenceQueryCompletionMorphologyCandidates, false},
		{"relaxed partial survives", []string{"a", "b", "c"}, [][]string{{"simple-only"}, {}, {"partial", "extra"}}, []string{"partial"}, EvidenceQueryCompletionRelatedCandidates, true},
		{"no relaxation for two terms", []string{"a", "b"}, [][]string{{"simple-only"}, {}}, []string{}, EvidenceQueryCompletionBoundedNoMatch, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			db := &hanRecoveryTestQueryer{t: t, rowFn: func(_ string, _ []any) sqlRow { return multisurfaceCompileRow(tc.terms) }}
			db.queryFn = func(_ string, _ []any) (sqlRows, error) {
				if calls >= len(tc.rows) {
					t.Fatal("unexpected baseline query")
				}
				rows := &multisurfaceIDRows{ids: tc.rows[calls], index: -1}
				calls++
				return rows, nil
			}
			ids, execution, err := multisurfaceBaseline(context.Background(), db, GroundedEvidenceBriefInput{Query: "a b c", ProposalListInput: ProposalListInput{Limit: 1}})
			if err != nil || !slices.Equal(ids, tc.want) || execution.CompletionReason != tc.completion || execution.Truncated != tc.truncated || calls != len(tc.rows) {
				t.Fatalf("baseline=%v/%+v calls=%d error=%v", ids, execution, calls, err)
			}
		})
	}
}

func TestMultisurfaceSQLBoundaries(t *testing.T) {
	compact := compactSQL(multisurfaceCandidatesSQL)
	native := compactSQL(multisurfaceSimpleSQL)
	segment := func(sql, start, end string) string {
		p := strings.Index(sql, start)
		if p < 0 {
			t.Fatalf("missing boundary %q", start)
		}
		q := strings.Index(sql[p:], end)
		if q < 0 {
			t.Fatalf("missing boundary %q", end)
		}
		return sql[p : p+q]
	}
	if segment(compact, "FROM proposal_occurrences", "WHERE") != segment(native, "FROM proposal_occurrences", "WHERE") {
		t.Fatal("supplemental joins differ")
	}
	want := segment(native, "($2 = ''", "ORDER BY")
	// Compare the seven complete predicates, including all three lifecycle branches.
	if !strings.Contains(compact, strings.TrimSpace(want)) {
		t.Fatal("supplemental identity/lifecycle filters differ")
	}
	for _, fragment := range []string{
		"WHERE e.source_system='manual_text'",
		"CASE WHEN over_budget THEN NULL ELSE ev.rendered_content END",
		"octet_length(ev.rendered_content)>$12 OR metrics.span_count>$13 OR metrics.max_bytes>$14",
		"v.raw_content_hash!=v.rendered_content_hash",
		"encode(sha256(ev.rendered_content),'hex')",
		"WHERE baseline OR statement_han && $17::text[] OR source_han && $17::text[]",
		"cardinality($10::text[])>0",
		"cardinality(statement_english)>=LEAST($16::integer,cardinality($10::text[]))",
		"cardinality(source_english)>=LEAST($16::integer,cardinality($10::text[]))",
		"count(DISTINCT term)::integer",
		"ORDER BY score DESC,created_at DESC,proposal_occurrence_id LIMIT $9",
		"regexp_replace(regexp_replace(e.statement_text, $15, ' ', 'g'), '[^[:alnum:]]', ' ', 'g')",
	} {
		if !strings.Contains(compact, fragment) {
			t.Fatalf("missing SQL boundary %q", fragment)
		}
	}
}

func TestMultisurfaceRunsSupplementAfterBaselineAndReportsExclusions(t *testing.T) {
	for _, excluded := range []int{0, 1} {
		db := &hanRecoveryTestQueryer{t: t}
		queryCalls, rowCalls := 0, 0
		db.queryFn = func(_ string, _ []any) (sqlRows, error) { queryCalls++; return &mockRows{index: -1}, nil }
		db.rowFn = func(query string, args []any) sqlRow {
			rowCalls++
			switch rowCalls {
			case 1:
				return multisurfaceCompileRow([]string{"atlas", "地震"})
			case 2:
				if args[0] != "Atlas" {
					t.Fatalf("English text=%v", args)
				}
				return hanRecoveryTestRow(func(dest ...any) error { *dest[0].(*[]string) = []string{"atlas"}; return nil })
			case 3:
				if query != multisurfaceCandidatesSQL || len(args) != 17 || args[8] != 4 || args[15] != 2 ||
					!slices.Equal(args[0].([]string), args[16].([]string)) ||
					!reflect.DeepEqual(args[0], []string{"地震"}) || args[7] != "all" {
					t.Fatalf("supplemental arguments=%v", args)
				}
				return hanRecoveryTestRow(func(dest ...any) error {
					*dest[0].(*[]byte) = []byte("[]")
					*dest[1].(*int) = 2
					*dest[2].(*int) = excluded
					*dest[3].(*int) = 0
					return nil
				})
			default:
				t.Fatal("unexpected hydration for empty candidates")
				return nil
			}
		}
		matches, execution, err := executeGroundedEvidenceQuery(context.Background(), db, GroundedEvidenceBriefInput{
			Query: "Atlas地震", QueryMode: EvidenceQueryModeExperimentalMultisurfaceV1, ProposalListInput: ProposalListInput{Limit: 3, LifecycleScope: "all"},
		})
		if err != nil || len(matches) != 0 || queryCalls != 2 || execution.QueryCount != 3 || execution.Multisurface == nil {
			t.Fatalf("execution=%+v error=%v", execution, err)
		}
		if execution.SearchCompleteWithinSurface != (excluded == 0) || execution.GlobalAbsenceInferenceAllowed || execution.Multisurface.ExcludedSourceViews != excluded {
			t.Fatalf("completeness=%+v", execution)
		}
	}
}

func TestMultisurfaceTypedSourceFailures(t *testing.T) {
	for _, pgerr := range []bool{false, true} {
		db := &hanRecoveryTestQueryer{t: t, queryFn: func(_ string, _ []any) (sqlRows, error) { return &mockRows{index: -1}, nil }}
		calls := 0
		db.rowFn = func(_ string, _ []any) sqlRow {
			calls++
			if calls == 1 {
				return multisurfaceCompileRow(nil)
			}
			if calls == 2 {
				return hanRecoveryTestRow(func(dest ...any) error { *dest[0].(*[]string) = nil; return nil })
			}
			return hanRecoveryTestRow(func(dest ...any) error {
				if pgerr {
					return &pgconn.PgError{Code: "22021"}
				}
				*dest[0].(*[]byte) = []byte("[]")
				*dest[1].(*int) = 1
				*dest[2].(*int) = 0
				*dest[3].(*int) = 1
				return nil
			})
		}
		_, _, err := executeMultisurfaceEvidenceQuery(context.Background(), db, GroundedEvidenceBriefInput{Query: "地震", ProposalListInput: ProposalListInput{Limit: 1}})
		var domainErr *DomainError
		if !errors.As(err, &domainErr) {
			t.Fatalf("want typed domain failure, got %v", err)
		}
	}
}

func TestMultisurfaceOriginalRefsAndMissingTerms(t *testing.T) {
	span := ResolvedSourceRef{ExtractionViewID: "view:1", StartByte: 20, EndByte: 40, SpanID: "span:2"}
	for _, tc := range []struct {
		ref  ResolvedSourceRef
		want bool
	}{
		{ResolvedSourceRef{ExtractionViewID: "view:1", StartByte: 0, EndByte: 50, SpanID: "other"}, true},
		{ResolvedSourceRef{ExtractionViewID: "view:2", StartByte: 0, EndByte: 50, SpanID: "span:2"}, false},
		{ResolvedSourceRef{ExtractionViewID: "view:1", StartByte: 21, EndByte: 40, SpanID: "span:2"}, false},
		{ResolvedSourceRef{ExtractionViewID: "view:1", StartByte: 20, EndByte: 39, SpanID: "span:2"}, false},
	} {
		if got := multisurfaceWithinRefs(span, []ResolvedSourceRef{tc.ref}); got != tc.want {
			t.Fatalf("range membership=%v want %v", got, tc.want)
		}
	}
	if got := multisurfaceMissingTerms([]string{"atlas", "地震", "芮氏"}, []string{"地震"}); !slices.Equal(got, []string{"atlas", "芮氏"}) {
		t.Fatalf("missing=%v", got)
	}
}

func multisurfaceCompileRow(terms []string) sqlRow {
	return hanRecoveryTestRow(func(dest ...any) error {
		*dest[0].(*string), *dest[2].(*string), *dest[4].(*string) = "simple", "english", "relaxed"
		*dest[1].(*[]string), *dest[3].(*[]string) = terms, terms
		return nil
	})
}

type multisurfaceIDRows struct {
	ids   []string
	index int
}

func (r *multisurfaceIDRows) Close()     {}
func (r *multisurfaceIDRows) Err() error { return nil }
func (r *multisurfaceIDRows) Next() bool { r.index++; return r.index < len(r.ids) }
func (r *multisurfaceIDRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.ids[r.index]
	*dest[1].(*float64) = 0.5
	return nil
}

func TestMultisurfaceHydratesOnlyFinalIDsAndKeepsOriginalRefs(t *testing.T) {
	ctx := context.Background()
	memory := newMockSQLDB()
	input := testManualInput("multisurface-projection")
	input.Raw = append(input.Raw, []byte(strings.Repeat("地震\n", 10))...)
	saved, err := ingestManualText(ctx, memory, input, testFixture())
	if err != nil {
		t.Fatal(err)
	}
	original, err := getProposalByOccurrenceID(ctx, memory, saved.ProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := loadBoundedSourceViewFromQueryer(ctx, memory, original.SourceSnapshotID, original.ExtractionViewID)
	if err != nil {
		t.Fatal(err)
	}
	memory.boundedViewPreflights, memory.sourceViewLoads = 0, 0
	db := &hanRecoveryTestQueryer{t: t}
	hydrated, spanCalls := []string{}, 0
	db.queryFn = func(query string, args []any) (sqlRows, error) {
		if query == multisurfaceSimpleSQL || query == multisurfaceEnglishSQL {
			// Baseline partial hits never prevent the supplemental candidate query.
			return &multisurfaceIDRows{ids: []string{original.ProposalOccurrenceID, "occ:outside-baseline-page"}, index: -1}, nil
		}
		if !strings.Contains(query, "SELECT sce.span_id, ARRAY(") {
			t.Fatalf("unexpected row query: %s", query)
		}
		spanCalls++
		rows := &multisurfaceSpanRows{index: -1}
		for _, span := range view.Input.Spans {
			terms := []string{}
			if strings.Contains(span.Text, "Refunds") {
				terms = append(terms, "refund")
			}
			if strings.Contains(span.Text, "地震") {
				terms = append(terms, "地震")
			}
			rows.ids = append(rows.ids, span.SpanID)
			rows.terms = append(rows.terms, terms)
		}
		return rows, nil
	}
	db.rowFn = func(query string, args []any) sqlRow {
		switch {
		case strings.Contains(query, "plainto_tsquery('simple', $1)::text"):
			return multisurfaceCompileRow([]string{"refund", "地震"})
		case strings.HasPrefix(query, "SELECT tsvector_to_array"):
			return hanRecoveryTestRow(func(dest ...any) error { *dest[0].(*[]string) = []string{"refund"}; return nil })
		case query == multisurfaceCandidatesSQL:
			if !slices.Equal(args[10].([]string), []string{original.ProposalOccurrenceID}) {
				t.Fatal("baseline IDs were not bounded before union")
			}
			candidates := []multisurfaceCandidate{{
				ID: original.ProposalOccurrenceID, Score: 2, Baseline: true, StatementTerms: []string{"refund"},
				SourceTerms: []string{"refund", "地震"}, SourceStatus: "searched", SnapshotID: original.SourceSnapshotID,
				ViewID: original.ExtractionViewID, RawHash: original.RawContentHash, RenderedHash: original.RenderedContentHash,
			}}
			payload, err := json.Marshal(candidates)
			if err != nil {
				t.Fatal(err)
			}
			return hanRecoveryTestRow(func(dest ...any) error {
				*dest[0].(*[]byte) = payload
				*dest[1].(*int) = 1
				*dest[2].(*int) = 0
				*dest[3].(*int) = 0
				return nil
			})
		case strings.Contains(query, "po.proposal_fingerprint,"):
			hydrated = append(hydrated, args[0].(string))
		}
		return memory.queryRow(ctx, query, args...)
	}
	matches, execution, err := executeMultisurfaceEvidenceQuery(ctx, db, GroundedEvidenceBriefInput{
		Query: "refund 地震", QueryMode: EvidenceQueryModeExperimentalMultisurfaceV1, ProposalListInput: ProposalListInput{Limit: 1, LifecycleScope: "all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || !slices.Equal(hydrated, []string{original.ProposalOccurrenceID}) || spanCalls != 1 {
		t.Fatalf("bounded hydration: matches=%d IDs=%v spanQueries=%d", len(matches), hydrated, spanCalls)
	}
	if execution.Truncated || !execution.Multisurface.Baseline.Truncated || execution.SearchCompleteWithinSurface {
		t.Fatalf("baseline truncation was hidden: %+v", execution)
	}
	if !reflect.DeepEqual(matches[0].Record.SourceRefs, original.SourceRefs) {
		t.Fatal("original refs were mutated")
	}
	source := matches[0].RetrievalBasis.Surfaces[1]
	if !source.SpansTruncated || len(source.MatchedSpans) != 8 || !slices.Equal(source.MatchedTerms, []string{"refund", "地震"}) {
		t.Fatalf("source excerpt cap/term listing=%+v", source)
	}
	if !source.MatchedSpans[0].WithinProposalSourceRefs || source.MatchedSpans[1].WithinProposalSourceRefs {
		t.Fatal("new source hits were confused with original proposal refs")
	}
	if memory.boundedViewPreflights != 1 || memory.sourceViewLoads != 1 {
		t.Fatal("selected source was loaded more than once")
	}
}

type multisurfaceSpanRows struct {
	ids   []string
	terms [][]string
	index int
}

func (r *multisurfaceSpanRows) Close()     {}
func (r *multisurfaceSpanRows) Err() error { return nil }
func (r *multisurfaceSpanRows) Next() bool { r.index++; return r.index < len(r.ids) }
func (r *multisurfaceSpanRows) Scan(dest ...any) error {
	*dest[0].(*string) = r.ids[r.index]
	*dest[1].(*[]string) = r.terms[r.index]
	return nil
}
