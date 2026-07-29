package evidenceingestion

import "testing"

func TestNormalizeEvidenceQueryMode(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default", want: EvidenceQueryModeDeterministicLexicalRecovery},
		{name: "exact", input: EvidenceQueryModeExactLexical, want: EvidenceQueryModeExactLexical},
		{name: "recovery", input: EvidenceQueryModeDeterministicLexicalRecovery, want: EvidenceQueryModeDeterministicLexicalRecovery},
		{name: "trimmed", input: " exact_lexical ", want: EvidenceQueryModeExactLexical},
		{name: "unsupported", input: "semantic", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeEvidenceQueryMode(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("normalizeEvidenceQueryMode() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeEvidenceQueryMode() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeEvidenceQueryMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewEvidenceQueryExecutionPreservesExactBoundary(t *testing.T) {
	filters := ProposalListInput{
		SourceSnapshotID: "srcsnap:1",
		SourceID:         "refund-policy",
		SourceVersion:    "v1",
		AdmissionOutcome: "admitted",
		LifecycleScope:   ProposalLifecycleScopeAll,
		Limit:            7,
	}
	got := newEvidenceQueryExecution("refund timing", EvidenceQueryModeDeterministicLexicalRecovery, filters)
	if got.PlanVersion != EvidenceQueryPlanRecoveryV2 ||
		got.NormalizerVersion != EvidenceQueryNormalizerRecoveryV1 ||
		got.SearchSurface != EvidenceQuerySearchSurfaceProposalStatement {
		t.Fatalf("query versions/surface = %+v", got)
	}
	if got.Filters.SourceSnapshotID != filters.SourceSnapshotID ||
		got.Filters.SourceID != filters.SourceID ||
		got.Filters.SourceVersion != filters.SourceVersion ||
		got.Filters.AdmissionOutcome != filters.AdmissionOutcome ||
		got.Filters.LifecycleScope != filters.LifecycleScope {
		t.Fatalf("query filters = %+v, want %+v", got.Filters, filters)
	}
	if got.GlobalAbsenceInferenceAllowed {
		t.Fatal("GlobalAbsenceInferenceAllowed = true")
	}
	if len(got.EligibleSourceBindingKinds) != 2 {
		t.Fatalf("EligibleSourceBindingKinds = %+v", got.EligibleSourceBindingKinds)
	}
}

func TestFinishEvidenceQueryReportsBoundedTruncation(t *testing.T) {
	matches := make([]ProposalSearchResult, 3)
	execution := newEvidenceQueryExecution("refund", EvidenceQueryModeExactLexical, ProposalListInput{
		LifecycleScope: ProposalLifecycleScopeActive,
		Limit:          2,
	})
	execution.Attempts = []EvidenceQueryAttempt{{
		Strategy:       EvidenceQueryStrategyExactSimple,
		CandidateCount: 3,
		Truncated:      true,
	}}

	bounded, got := finishEvidenceQuery(matches, execution, 2, EvidenceQueryCompletionExactCandidates)
	if len(bounded) != 2 || got.CandidateCount != 2 || !got.Truncated {
		t.Fatalf("bounded result = %d/%+v", len(bounded), got)
	}
	if got.SearchCompleteWithinSurface {
		t.Fatal("SearchCompleteWithinSurface = true for truncated result")
	}
	if got.QueryCount != 1 || got.CompletionReason != EvidenceQueryCompletionExactCandidates {
		t.Fatalf("query completion = %+v", got)
	}
}
