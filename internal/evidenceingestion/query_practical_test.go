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

func TestPracticalHanAnchorRequiresEligibleSubset(t *testing.T) {
	db := practicalEmptyQueryer(t, nil, func(args []any) sqlRow {
		if len(args) != 17 {
			t.Fatalf("candidate SQL has %d parameters; want 17 with separate eligible Han anchors", len(args))
		}
		if !slices.Equal(args[0].([]string), []string{"地震", "影響", "造成"}) ||
			!slices.Equal(args[16].([]string), []string{"地震"}) {
			t.Fatalf("raw Han=%v eligible anchors=%v; want all raw terms but only 地震 eligible", args[0], args[16])
		}
		return practicalCandidateRow(nil, 0, 0, 0, nil)
	})
	_, execution, err := executeGroundedEvidenceQuery(context.Background(), db, GroundedEvidenceBriefInput{
		Query: "地震 造成 影響", QueryMode: EvidenceQueryModePracticalMultisurfaceV1, ProposalListInput: ProposalListInput{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if execution.PlanVersion != "practical-multisurface-lexical-v2" {
		t.Fatalf("plan=%q; want practical-multisurface-lexical-v2", execution.PlanVersion)
	}
	if execution.OriginalQuery != "地震 造成 影響" ||
		!slices.Equal(execution.Multisurface.NormalizedQueryTerms, []string{"地震", "影響", "造成"}) ||
		!slices.Equal(execution.PracticalRecovery.HanTermPolicy.AuxiliaryTerms, []string{"影響", "造成"}) {
		t.Fatal("anchor eligibility rewrote the query or omitted auxiliary terms")
	}
	for _, predicate := range []string{"statement_han && $17::text[]", "source_han && $17::text[]"} {
		if !strings.Contains(multisurfaceCandidatesSQL, predicate) {
			t.Fatalf("missing pre-limit eligibility predicate %q", predicate)
		}
	}
}

func TestPracticalHanTermPolicyFixedListAndBoundaries(t *testing.T) {
	allAuxiliary := []string{"影響", "影响", "造成", "導致", "导致", "發生", "发生", "是否", "哪些", "什麼", "什么", "多少", "如何"}
	for _, term := range allAuxiliary {
		policy := practicalHanTermPolicy([]string{term, "地震"}, nil)
		if !slices.Equal(policy.AuxiliaryTerms, []string{term}) || !slices.Equal(policy.EligibleAnchorTerms, []string{"地震"}) {
			t.Fatalf("fixed auxiliary %q changed: %+v", term, policy)
		}
	}
	for _, tc := range []struct {
		name                             string
		han, english, auxiliary, anchors []string
		preserved                        bool
	}{
		{"empty", nil, nil, []string{}, []string{}, false},
		{"English only", nil, []string{"alpha"}, []string{}, []string{}, false},
		{"unlisted terms remain anchors", []string{"原因", "地震", "其他"}, nil, []string{}, []string{"其他", "原因", "地震"}, false},
		{"auxiliary short broad query", []string{"影響"}, nil, []string{"影響"}, []string{"影響"}, true},
		{"multiple auxiliary broad query", []string{"造成", "是否"}, nil, []string{"是否", "造成"}, []string{"是否", "造成"}, true},
		{"mixed query cannot enter on auxiliary alone", []string{"影響"}, []string{"alpha"}, []string{"影響"}, []string{}, false},
		{"stable dedup without input mutation", []string{"造成", "地震", "影響", "地震", "影響"}, nil, []string{"影響", "造成"}, []string{"地震"}, false},
		{"not semantic segmentation", []string{"影響", "響力"}, nil, []string{"影響"}, []string{"響力"}, false},
		{"unlisted cross-bigram remains eligible", []string{"造成", "成影", "影響"}, nil, []string{"影響", "造成"}, []string{"成影"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalHan, originalEnglish := slices.Clone(tc.han), slices.Clone(tc.english)
			got := practicalHanTermPolicy(tc.han, tc.english)
			if got.Version != EvidencePracticalHanTermPolicyVersionV1 || got.AuxiliaryTerms == nil || got.EligibleAnchorTerms == nil ||
				!slices.Equal(got.AuxiliaryTerms, tc.auxiliary) || !slices.Equal(got.EligibleAnchorTerms, tc.anchors) ||
				got.AuxiliaryOnlyQueryPreserved != tc.preserved || !reflect.DeepEqual(tc.han, originalHan) || !reflect.DeepEqual(tc.english, originalEnglish) {
				t.Fatalf("policy=%+v", got)
			}
			if len(got.EligibleAnchorTerms) > 0 {
				got.EligibleAnchorTerms[0] = "changed"
			}
			if len(got.AuxiliaryTerms) > 0 {
				got.AuxiliaryTerms[0] = "changed"
			}
			if !reflect.DeepEqual(tc.han, originalHan) {
				t.Fatal("policy arrays alias input")
			}
		})
	}
}

func TestPracticalHanAnchorsDoNotChangeResearchAndAreReusedForRecovery(t *testing.T) {
	for _, mode := range []string{EvidenceQueryModePracticalMultisurfaceV1, EvidenceQueryModeExperimentalMultisurfaceV1} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			db := practicalEmptyQueryer(t, []string{"alpha", "beta"}, func(args []any) sqlRow {
				calls++
				want := []string{"地震", "影響"}
				if mode == EvidenceQueryModePracticalMultisurfaceV1 {
					want = []string{"地震"}
				}
				if len(args) != 17 || !slices.Equal(args[0].([]string), []string{"地震", "影響"}) ||
					!slices.Equal(args[16].([]string), want) {
					t.Fatal("mode altered raw terms or passed wrong anchor eligibility")
				}
				return practicalCandidateRow(nil, 0, 0, 0, nil)
			})
			_, execution, err := executeGroundedEvidenceQuery(context.Background(), db, GroundedEvidenceBriefInput{
				Query: "alpha beta 地震 影響", QueryMode: mode, ProposalListInput: ProposalListInput{Limit: 1}})
			if err != nil {
				t.Fatal(err)
			}
			if mode == EvidenceQueryModePracticalMultisurfaceV1 {
				if calls != 2 || !execution.PracticalRecovery.FallbackAttempted {
					t.Fatal("qualified English recovery changed")
				}
			} else if calls != 1 || execution.PracticalRecovery != nil || execution.PlanVersion != EvidenceQueryPlanExperimentalMultisurfaceV1 {
				t.Fatal("research mode acquired practical policy or recovery")
			}
		})
	}
}

func TestPracticalEmptyRecoveryGate(t *testing.T) {
	for _, tc := range []struct {
		name, query       string
		english           []string
		excluded          int
		baselineTruncated bool
		wantRecovery      bool
	}{
		{"two English terms", "alpha beta", []string{"alpha", "beta"}, 0, false, true},
		{"mixed script", "alpha 地震", []string{"alpha"}, 0, false, true},
		{"single word still reports configured threshold", "alpha", []string{"alpha"}, 0, false, true},
		{"stopwords only", "the and", nil, 0, false, false},
		{"Han only", "地震", nil, 0, false, false},
		{"punctuation only", "!?", nil, 0, false, false},
		{"source over budget", "alpha beta", []string{"alpha", "beta"}, 1, false, false},
		{"incomplete baseline", "alpha beta", []string{"alpha", "beta"}, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := GroundedEvidenceBriefInput{Query: tc.query, QueryMode: EvidenceQueryModePracticalMultisurfaceV1,
				ProposalListInput: ProposalListInput{SourceSnapshotID: "snapshot", RepositorySnapshotID: "repository",
					SourceID: "source", SourceVersion: "version", AdmissionOutcome: "audit_only",
					SourceGenerationID: "generation", LifecycleScope: "historical", Limit: 1}}
			var calls [][]any
			db := practicalEmptyQueryer(t, tc.english, func(args []any) sqlRow {
				calls = append(calls, append([]any{}, args...))
				return practicalCandidateRow(nil, 2, tc.excluded, 0, nil)
			})
			if tc.baselineTruncated {
				db.queryFn = func(_ string, _ []any) (sqlRows, error) {
					return &multisurfaceIDRows{ids: []string{"a", "b"}, index: -1}, nil
				}
			}
			matches, execution, err := executeGroundedEvidenceQuery(context.Background(), db, input)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, wantMinimum := 1, 2
			wantReason := EvidenceQueryCompletionBoundedNoMatch
			if tc.wantRecovery {
				wantCalls, wantMinimum, wantReason = 2, 1, EvidenceQueryCompletionPracticalNoMatch
			}
			if len(calls) != wantCalls || len(matches) != 0 || execution.PracticalRecovery == nil ||
				execution.PracticalRecovery.FallbackAttempted != tc.wantRecovery ||
				execution.PracticalRecovery.ConfiguredMinimumEnglishTerms != wantMinimum ||
				execution.CompletionReason != wantReason || execution.GlobalAbsenceInferenceAllowed ||
				execution.CandidateCount != 0 || execution.QueryCount != len(execution.Multisurface.Baseline.Attempts)+wantCalls ||
				execution.PlanVersion != EvidenceQueryPlanPracticalMultisurfaceV2 {
				t.Fatalf("calls=%d matches=%d execution=%+v", len(calls), len(matches), execution)
			}
			wantComplete := tc.excluded == 0 && !tc.baselineTruncated
			if execution.SearchCompleteWithinSurface != wantComplete {
				t.Fatal("incomplete search was hidden")
			}
			for i, args := range calls {
				if len(args) != 17 || args[1] != "snapshot" || args[2] != "repository" || args[3] != "source" ||
					args[4] != "version" || args[5] != "audit_only" || args[6] != "generation" || args[7] != "historical" || args[8] != 2 ||
					args[15] != 2-i || !reflect.DeepEqual(args[:15], calls[0][:15]) ||
					!slices.Equal(args[16].([]string), calls[0][16].([]string)) {
					t.Fatalf("SQL filters/budget/terms changed in pass %d", i)
				}
			}
			if tc.wantRecovery && execution.Attempts[len(execution.Attempts)-1].Strategy != EvidenceQueryStrategyPracticalRecovery {
				t.Fatal("recovery attempt not disclosed")
			}
		})
	}
}

func TestPracticalFirstHitAndRecoveryKeepEvidence(t *testing.T) {
	ctx := context.Background()
	memory := newMockSQLDB()
	saved, err := ingestManualText(ctx, memory, testManualInput("practical-query"), testFixture())
	if err != nil {
		t.Fatal(err)
	}
	original, err := getProposalByOccurrenceID(ctx, memory, saved.ProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, firstHit := range []bool{true, false} {
		t.Run(map[bool]string{true: "first pass", false: "recovery"}[firstHit], func(t *testing.T) {
			calls, hydrated := 0, 0
			db := practicalEmptyQueryer(t, []string{"refund", "quasar"}, func(args []any) sqlRow {
				calls++
				if calls > 2 || args[15] != 3-calls {
					t.Fatal("unexpected candidate pass")
				}
				var candidates []multisurfaceCandidate
				if firstHit || calls == 2 {
					candidates = []multisurfaceCandidate{{ID: original.ProposalOccurrenceID, Score: 1,
						StatementTerms: []string{"refund"}, SourceStatus: "not_applicable"}}
				}
				return practicalCandidateRow(candidates, 0, 0, 0, nil)
			})
			originalRow := db.rowFn
			db.rowFn = func(query string, args []any) sqlRow {
				if strings.Contains(query, "po.proposal_fingerprint,") {
					hydrated++
					return memory.queryRow(ctx, query, args...)
				}
				return originalRow(query, args)
			}
			matches, execution, err := executeGroundedEvidenceQuery(ctx, db, GroundedEvidenceBriefInput{
				Query: "refund quasar", QueryMode: EvidenceQueryModePracticalMultisurfaceV1, ProposalListInput: ProposalListInput{Limit: 1}})
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, wantReason := 1, EvidenceQueryCompletionPracticalFirstCandidates
			if !firstHit {
				wantCalls, wantReason = 2, EvidenceQueryCompletionPracticalCandidates
			}
			if calls != wantCalls || hydrated != 1 || len(matches) != 1 || !reflect.DeepEqual(matches[0].Record, original) ||
				execution.PracticalRecovery.FallbackAttempted == firstHit || execution.CompletionReason != wantReason ||
				!slices.Equal(matches[0].RetrievalBasis.Surfaces[0].MissingTerms, []string{"quasar"}) {
				t.Fatalf("calls=%d hydration=%d execution=%+v matches=%+v", calls, hydrated, execution, matches)
			}
		})
	}
}

func TestPracticalErrorsAndCancellationDoNotBecomeRecovery(t *testing.T) {
	sentinel := errors.New("query failed")
	for _, tc := range []struct {
		name                                         string
		failPass, invalidPass, cancelPass, wantCalls int
		preCanceled                                  bool
	}{
		{"SQL error first pass", 1, 0, 0, 1, false},
		{"hash first pass", 0, 1, 0, 1, false},
		{"SQL error recovery", 2, 0, 0, 2, false},
		{"hash recovery", 0, 2, 0, 2, false},
		{"cancel before query", 0, 0, 0, 0, true},
		{"cancel after first pass", 0, 0, 1, 1, false},
		{"cancel during recovery", 0, 0, 2, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.preCanceled {
				cancel()
			}
			calls := 0
			db := practicalEmptyQueryer(t, []string{"alpha", "beta"}, func(_ []any) sqlRow {
				calls++
				if calls > tc.wantCalls {
					t.Fatal("unexpected retry")
				}
				if calls == tc.cancelPass {
					cancel()
					if calls == 2 {
						return practicalCandidateRow(nil, 0, 0, 0, ctx.Err())
					}
				}
				if calls == tc.failPass {
					return practicalCandidateRow(nil, 0, 0, 0, sentinel)
				}
				invalid := 0
				if calls == tc.invalidPass {
					invalid = 1
				}
				return practicalCandidateRow(nil, 0, 0, invalid, nil)
			})
			_, _, err := executeGroundedEvidenceQuery(ctx, db, GroundedEvidenceBriefInput{
				Query: "alpha beta", QueryMode: EvidenceQueryModePracticalMultisurfaceV1, ProposalListInput: ProposalListInput{Limit: 1}})
			if err == nil || calls != tc.wantCalls {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if tc.failPass != 0 && !errors.Is(err, sentinel) {
				t.Fatal("SQL error lost")
			}
			var domainErr *DomainError
			if tc.invalidPass != 0 && (!errors.As(err, &domainErr) || domainErr.Kind != ErrorQuotedHashMismatch) {
				t.Fatal("source integrity error lost its type")
			}
			if (tc.preCanceled || tc.cancelPass != 0) && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost")
			}
		})
	}
}

func practicalEmptyQueryer(t *testing.T, english []string, candidates func([]any) sqlRow) *hanRecoveryTestQueryer {
	t.Helper()
	return &hanRecoveryTestQueryer{t: t, queryFn: func(_ string, _ []any) (sqlRows, error) { return &mockRows{index: -1}, nil },
		rowFn: func(query string, args []any) sqlRow {
			switch {
			case strings.Contains(query, "plainto_tsquery('simple', $1)::text"):
				return multisurfaceCompileRow(english)
			case strings.HasPrefix(query, "SELECT tsvector_to_array"):
				return hanRecoveryTestRow(func(dest ...any) error { *dest[0].(*[]string) = english; return nil })
			case query == multisurfaceCandidatesSQL:
				return candidates(args)
			default:
				t.Fatalf("unexpected SQL %s", query)
				return nil
			}
		}}
}

func practicalCandidateRow(candidates []multisurfaceCandidate, searched, excluded, invalid int, err error) sqlRow {
	return hanRecoveryTestRow(func(dest ...any) error {
		if err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(candidates)
		if marshalErr != nil {
			return marshalErr
		}
		*dest[0].(*[]byte), *dest[1].(*int), *dest[2].(*int), *dest[3].(*int) = payload, searched, excluded, invalid
		return nil
	})
}
