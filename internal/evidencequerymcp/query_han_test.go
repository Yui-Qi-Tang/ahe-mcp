package evidencequerymcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestHanExperimentalModeDelegatesAndPreservesProvenance(t *testing.T) {
	execution := testEvidenceQueryExecution("地震 芮氏", evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, 10, "all", false, evidenceingestion.EvidenceQueryCompletionBoundedNoMatch)
	execution.QueryMode = evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1
	execution.PlanVersion = "experimental-han-lexical-recovery-v1"
	execution.NormalizerVersion = "postgresql-simple-english-han-literal-v1"
	execution.FallbackStatus = "eligible"
	execution.RankingPolicy = "created_at_desc,id_asc_unscored"
	execution.CompletionReason = evidenceingestion.EvidenceQueryCompletionHanCandidates
	execution.CandidateCount = 1
	execution.Attempts = append(execution.Attempts, evidenceingestion.EvidenceQueryAttempt{
		Strategy: evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms, TextSearchConfiguration: "han-literal+simple",
		CompiledQuery:        `{"han_literal_terms":["地震","芮氏"],"ascii_simple_tsquery":""}`,
		NormalizedQueryTerms: []string{"地震", "芮氏"}, CandidateCount: 1,
	})
	execution.QueryCount = len(execution.Attempts)
	core := &fakeQueryCore{briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
		Execution: execution,
		Matches: []evidenceingestion.ProposalSearchResult{{
			Record: testQueryResult("occ:han"),
			Rank:   0,
		}},
	}}
	server := newServer(core)
	data, err := server.CallTool(context.Background(), ToolGetGroundedEvidenceBrief,
		[]byte(`{"query":"地震 芮氏","query_mode":"experimental_han_lexical_recovery_v1","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	var response GroundedEvidenceBriefResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if core.briefInput.QueryMode != evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1 || response.QueryExecution.FallbackStatus != execution.FallbackStatus || response.QueryExecution.RankingPolicy != execution.RankingPolicy || response.QueryExecution.PlanVersion != execution.PlanVersion || response.QueryExecution.GlobalAbsenceInferenceAllowed {
		t.Fatalf("experimental provenance was lost: %+v", response.QueryExecution)
	}
	if !bytes.Contains(data, []byte("experimental_han_literal_fallback_without_semantic_segmentation")) || !bytes.Contains(data, []byte("han_fallback_zero_rank_is_unscored_not_confidence")) {
		t.Fatal("experimental limitations missing")
	}
}

func TestLegacyQueryExecutionOmitsHanFields(t *testing.T) {
	execution := testEvidenceQueryExecution("Orion", evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, 10, "all", false, evidenceingestion.EvidenceQueryCompletionBoundedNoMatch)
	data, err := json.Marshal(mapGroundedEvidenceQueryExecution(execution))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("fallback_status")) || bytes.Contains(data, []byte("ranking_policy")) {
		t.Fatal("legacy response gained experimental fields")
	}
}
