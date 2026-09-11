package evidencequerymcp

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestPracticalRequiresExplicitV7Pair(t *testing.T) {
	schemas := []string{"", GroundedEvidenceBriefSchemaV2, GroundedEvidenceBriefSchemaV3,
		GroundedEvidenceBriefSchemaV4, GroundedEvidenceBriefSchemaV5, GroundedEvidenceBriefSchemaV6, GroundedEvidenceBriefSchemaV7}
	modes := []string{"", evidenceingestion.EvidenceQueryModeExactLexical,
		evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1,
		evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1, evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1}
	for _, schema := range schemas {
		for _, mode := range modes {
			practical, v7 := mode == evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1, schema == GroundedEvidenceBriefSchemaV7
			if practical == v7 {
				continue
			}
			t.Run(mode+"/"+schema, func(t *testing.T) {
				core := &fakeQueryCore{}
				_, err := newServer(core).GetGroundedEvidenceBrief(context.Background(), GetGroundedEvidenceBriefRequest{
					Query: "satellite", QueryMode: mode, ResponseSchema: schema})
				if err == nil || core.briefInput.Query != "" {
					t.Fatal("unpaired practical mode/schema reached core")
				}
			})
		}
	}
}

func TestPracticalV7DisclosesRecoveryWithoutChangingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name            string
		recovery, empty bool
		code            string
	}{
		{"first pass", false, false, evidenceingestion.EvidenceQueryCompletionPracticalFirstCandidates},
		{"recovery hit", true, false, evidenceingestion.EvidenceQueryCompletionPracticalCandidates},
		{"recovery empty", true, true, evidenceingestion.EvidenceQueryCompletionPracticalNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := testPracticalBriefResult(tc.recovery)
			result.Execution.CompletionReason = tc.code
			original := result.Matches[0].Record
			if tc.empty {
				result.Matches = nil
				result.Execution.CandidateCount = 0
			}
			core := &fakeQueryCore{briefResult: result}
			data, err := newServer(core).CallTool(context.Background(), ToolGetGroundedEvidenceBrief,
				[]byte("{\"query\":\"satellite\",\"query_mode\":\"practical_multisurface_lexical_v1\",\"response_schema\":\"grounded-evidence-brief-v7\",\"admission_outcome\":\"pending\",\"source_id\":\"fixture-refund-policy\",\"limit\":10}"))
			if err != nil {
				t.Fatal(err)
			}
			var response GroundedEvidenceBriefResponse
			if err := json.Unmarshal(data, &response); err != nil {
				t.Fatal(err)
			}
			meta := response.QueryExecution.PracticalRecovery
			minimum := 2
			if tc.recovery {
				minimum = 1
			}
			if meta == nil || meta.FallbackAttempted != tc.recovery || meta.ConfiguredMinimumEnglishTerms != minimum ||
				!slices.Contains(response.ObservationCodes, tc.code) || response.QueryExecution.GlobalAbsenceInferenceAllowed ||
				!bytes.Contains(data, []byte("\"fallback_attempted\":")) ||
				response.QueryExecution.Multisurface == nil || core.briefInput.IncludeSourceContext || core.briefInput.IncludeRepositoryContext ||
				core.briefInput.QueryMode != evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1 ||
				core.briefInput.AdmissionOutcome != "pending" || core.briefInput.SourceID != "fixture-refund-policy" {
				t.Fatalf("input=%+v response=%s", core.briefInput, data)
			}
			if tc.empty {
				if len(response.Matches) != 0 {
					t.Fatal("empty result acquired matches")
				}
			} else {
				match := response.Matches[0]
				if match.StatementText != original.StatementText || match.AdmissionOutcome != original.AdmissionOutcome ||
					!reflect.DeepEqual(match.SourceRefs, original.SourceRefs) || match.CanonicalRef != nil ||
					match.RecordState.AuthorityStatus != groundedEvidenceAuthorityProposalPending ||
					match.RetrievalBasis.Surfaces[1].MatchedSpans[0].WithinProposalSourceRefs {
					t.Fatal("search changed stored authority or promoted a new source hit into a citation")
				}
			}
			if !slices.Contains(response.Limitations, "source_text_match_does_not_establish_proposal_support") ||
				!slices.Contains(response.Limitations, "single_english_term_recovery_only_after_complete_empty_first_pass") {
				t.Fatal("practical retrieval lost its boundaries")
			}
		})
	}
}

func TestPracticalV7RejectsInconsistentRecoveryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*evidenceingestion.GroundedEvidenceBriefQueryResult)
	}{
		{"missing", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) { r.Execution.PracticalRecovery = nil }},
		{"wrong threshold", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.PracticalRecovery.ConfiguredMinimumEnglishTerms = 2
		}},
		{"wrong attempt flag", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.PracticalRecovery.FallbackAttempted = false
		}},
		{"excluded source", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.Multisurface.ExcludedSourceViews = 1
			r.Execution.SearchCompleteWithinSurface = false
		}},
		{"truncated baseline", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.Multisurface.Baseline.Truncated = true
		}},
		{"nested recovery", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.Multisurface.Baseline.PracticalRecovery = &evidenceingestion.EvidencePracticalRecovery{}
		}},
		{"unknown Han policy", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.PracticalRecovery.HanTermPolicy.Version = "unknown"
		}},
		{"null auxiliary terms", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.PracticalRecovery.HanTermPolicy.AuxiliaryTerms = nil
		}},
		{"null anchors", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.PracticalRecovery.HanTermPolicy.EligibleAnchorTerms = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := testPracticalBriefResult(true)
			tc.mutate(&result)
			if _, err := mapGroundedEvidenceBrief(result, GroundedEvidenceBriefSchemaV7); err == nil {
				t.Fatal("inconsistent recovery accepted")
			}
		})
	}
}

func TestPracticalHanPolicyProjectionPreservesArraysAndQueryDetails(t *testing.T) {
	for _, preserved := range []bool{false, true} {
		input := &evidenceingestion.EvidencePracticalRecovery{
			ConfiguredMinimumEnglishTerms: 2, HanTermPolicy: evidenceingestion.EvidencePracticalHanTermPolicy{
				Version:        evidenceingestion.EvidencePracticalHanTermPolicyVersionV1,
				AuxiliaryTerms: []string{"影響"}, EligibleAnchorTerms: []string{"地震"},
				AuxiliaryOnlyQueryPreserved: preserved,
			},
		}
		if preserved {
			input.HanTermPolicy.EligibleAnchorTerms = []string{"影響"}
		}
		mapped := mapGroundedEvidencePracticalRecovery(input)
		if mapped.HanTermPolicy.Version != input.HanTermPolicy.Version ||
			!slices.Equal(mapped.HanTermPolicy.AuxiliaryTerms, input.HanTermPolicy.AuxiliaryTerms) ||
			!slices.Equal(mapped.HanTermPolicy.EligibleAnchorTerms, input.HanTermPolicy.EligibleAnchorTerms) ||
			mapped.HanTermPolicy.AuxiliaryOnlyQueryPreserved != preserved {
			t.Fatal("Han policy changed during projection")
		}
		mapped.HanTermPolicy.AuxiliaryTerms[0] = "changed"
		mapped.HanTermPolicy.EligibleAnchorTerms[0] = "changed"
		if input.HanTermPolicy.AuxiliaryTerms[0] == "changed" || input.HanTermPolicy.EligibleAnchorTerms[0] == "changed" {
			t.Fatal("policy mapping aliases core arrays")
		}
	}
	result := testPracticalBriefResult(false)
	response, err := mapGroundedEvidenceBrief(result, GroundedEvidenceBriefSchemaV7)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"\"han_term_policy\":", "\"version\":\"han-auxiliary-anchor-v1\"", "\"auxiliary_terms\":[]", "\"eligible_anchor_terms\":[]", "\"auxiliary_only_query_preserved\":false"} {
		if !bytes.Contains(data, []byte(expected)) {
			t.Fatalf("missing explicit policy field %s", expected)
		}
	}
	if response.QueryExecution.OriginalQuery != result.Execution.OriginalQuery ||
		!slices.Equal(response.QueryExecution.Multisurface.NormalizedQueryTerms, result.Execution.Multisurface.NormalizedQueryTerms) ||
		response.Matches[0].Rank != result.Matches[0].Rank {
		t.Fatal("policy projection changed original query, complete terms or score")
	}
}

func TestLegacySchemasNeverEmitPracticalMetadata(t *testing.T) {
	for _, schema := range []string{GroundedEvidenceBriefSchemaV2, GroundedEvidenceBriefSchemaV3, GroundedEvidenceBriefSchemaV4, GroundedEvidenceBriefSchemaV5, GroundedEvidenceBriefSchemaV6} {
		t.Run(schema, func(t *testing.T) {
			result := testMultisurfaceBriefResult()
			if schema != GroundedEvidenceBriefSchemaV6 {
				result.Execution = result.Execution.Multisurface.Baseline
				result.Matches[0].RetrievalBasis = nil
			}
			if schema == GroundedEvidenceBriefSchemaV3 || schema == GroundedEvidenceBriefSchemaV4 {
				result.SourceContexts = []evidenceingestion.GroundedEvidenceSourceContext{
					testAvailableGroundedEvidenceSourceContext(result.Matches[0].Record),
				}
			}
			response, err := mapGroundedEvidenceBrief(result, schema)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("practical")) {
				t.Fatal("old schema gained practical fields or limitations")
			}
			result.Execution.PracticalRecovery = &evidenceingestion.EvidencePracticalRecovery{ConfiguredMinimumEnglishTerms: 2}
			if _, err := mapGroundedEvidenceBrief(result, schema); err == nil {
				t.Fatal("practical metadata leaked into old contract")
			}
		})
	}
}

func testPracticalBriefResult(recovery bool) evidenceingestion.GroundedEvidenceBriefQueryResult {
	result := testMultisurfaceBriefResult()
	result.Execution.QueryMode = evidenceingestion.EvidenceQueryModePracticalMultisurfaceV1
	result.Execution.PlanVersion = evidenceingestion.EvidenceQueryPlanPracticalMultisurfaceV2
	minimum := 2
	if recovery {
		minimum = 1
	}
	result.Execution.PracticalRecovery = &evidenceingestion.EvidencePracticalRecovery{
		FallbackAttempted: recovery, ConfiguredMinimumEnglishTerms: minimum,
		HanTermPolicy: evidenceingestion.EvidencePracticalHanTermPolicy{
			Version: evidenceingestion.EvidencePracticalHanTermPolicyVersionV1, AuxiliaryTerms: []string{}, EligibleAnchorTerms: []string{},
		}}
	return result
}
