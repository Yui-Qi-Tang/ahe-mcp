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

func TestMultisurfaceRequiresExplicitV6Pair(t *testing.T) {
	for _, schema := range []string{"", GroundedEvidenceBriefSchemaV2, GroundedEvidenceBriefSchemaV3, GroundedEvidenceBriefSchemaV4, GroundedEvidenceBriefSchemaV5} {
		t.Run(schema, func(t *testing.T) {
			core := &fakeQueryCore{}
			_, err := newServer(core).GetGroundedEvidenceBrief(context.Background(), GetGroundedEvidenceBriefRequest{
				Query: "Atlas's 搜尋延遲？", QueryMode: evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1, ResponseSchema: schema,
			})
			if err == nil || core.briefInput.Query != "" {
				t.Fatal("unpaired expanded mode reached the query core")
			}
		})
	}
	for _, mode := range []string{"", evidenceingestion.EvidenceQueryModeExactLexical, evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1} {
		t.Run(mode, func(t *testing.T) {
			core := &fakeQueryCore{}
			_, err := newServer(core).GetGroundedEvidenceBrief(context.Background(), GetGroundedEvidenceBriefRequest{
				Query: "Atlas", QueryMode: mode, ResponseSchema: GroundedEvidenceBriefSchemaV6,
			})
			if err == nil || core.briefInput.Query != "" {
				t.Fatal("v6 implicitly enabled the expanded mode")
			}
		})
	}
}

func TestMultisurfaceV6KeepsSourceHitSeparateFromClaimAndOriginalRefs(t *testing.T) {
	result := testMultisurfaceBriefResult()
	original := result.Matches[0].Record
	core := &fakeQueryCore{briefResult: result}
	data, err := newServer(core).CallTool(context.Background(), ToolGetGroundedEvidenceBrief,
		[]byte(`{"query":"satellite","query_mode":"experimental_multisurface_lexical_v1","response_schema":"grounded-evidence-brief-v6","admission_outcome":"pending","source_id":"fixture-refund-policy","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	var response GroundedEvidenceBriefResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if core.briefInput.QueryMode != evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1 ||
		core.briefInput.IncludeSourceContext || core.briefInput.IncludeRepositoryContext ||
		core.briefInput.SourceID != "fixture-refund-policy" || core.briefInput.AdmissionOutcome != "pending" {
		t.Fatalf("v6 input changed: %+v", core.briefInput)
	}
	match := response.Matches[0]
	if match.StatementText != original.StatementText || match.AdmissionOutcome != original.AdmissionOutcome ||
		!reflect.DeepEqual(match.SourceRefs, original.SourceRefs) || match.CanonicalRef != nil ||
		match.RecordState.AuthorityStatus != groundedEvidenceAuthorityProposalPending {
		t.Fatalf("retrieval altered evidence authority: %+v", match)
	}
	if match.RetrievalBasis == nil || len(match.RetrievalBasis.Surfaces[0].MatchedTerms) != 0 ||
		match.RetrievalBasis.Surfaces[1].MatchedSpans[0].WithinProposalSourceRefs ||
		response.QueryExecution.Multisurface == nil || response.QueryExecution.GlobalAbsenceInferenceAllowed {
		t.Fatal("source-only hit basis lost")
	}
	for _, unwanted := range []string{`"source_context"`, `"repository_context"`, `"persisted_proposal_statement_text_only"`} {
		if bytes.Contains(data, []byte(unwanted)) {
			t.Fatalf("v6 silently reused an incompatible old contract: %s", unwanted)
		}
	}
	for _, limitation := range []string{"source_text_match_does_not_establish_proposal_support", "cross_language_requires_shared_text_no_translation", "matched_term_count_is_not_confidence_or_admission_priority"} {
		if !slices.Contains(response.Limitations, limitation) {
			t.Fatalf("missing limitation: %s", limitation)
		}
	}
	response.Matches[0].RetrievalBasis.Surfaces[1].MatchedTerms[0] = "changed"
	if result.Matches[0].RetrievalBasis.Surfaces[1].MatchedTerms[0] != "satellit" {
		t.Fatal("mapping exposed core backing arrays")
	}
}

func TestMultisurfaceProjectionRejectsInconsistentProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*evidenceingestion.GroundedEvidenceBriefQueryResult)
	}{
		{"missing execution", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) { r.Execution.Multisurface = nil }},
		{"missing basis", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) { r.Matches[0].RetrievalBasis = nil }},
		{"query mismatch", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.NormalizedQueryTerms = []string{"invented"}
		}},
		{"unreported exclusion", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Execution.Multisurface.ExcludedSourceViews = 1
		}},
		{"wrong source", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].SourceSnapshotID = "srcsnap:other"
		}},
		{"wrong view", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].ExtractionViewID = "view:other"
		}},
		{"wrong hash", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].RenderedContentHash = "sha256:other"
		}},
		{"same-length changed excerpt", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].MatchedSpans[0].SourceRef.QuotedText = "The bulletin does NOT report satellite damage."
		}},
		{"invented citation membership", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].MatchedSpans[0].WithinProposalSourceRefs = true
		}},
		{"unknown match term", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].MatchedTerms = []string{"invented"}
		}},
		{"matched and missing", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].MissingTerms = []string{"satellit"}
		}},
		{"unsearched with hits", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[1].Status = "over_budget"
		}},
		{"source field on statement", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces[0].ExtractionViewID = "view:1"
		}},
		{"unaccounted source surface", func(r *evidenceingestion.GroundedEvidenceBriefQueryResult) {
			r.Matches[0].RetrievalBasis.Surfaces = r.Matches[0].RetrievalBasis.Surfaces[:1]
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := testMultisurfaceBriefResult()
			tc.mutate(&result)
			if _, err := mapGroundedEvidenceBrief(result, GroundedEvidenceBriefSchemaV6); err == nil {
				t.Fatal("inconsistent retrieval projection accepted")
			}
		})
	}
}

func TestLegacyBriefOmitsMultisurfaceMetadata(t *testing.T) {
	result := evidenceingestion.GroundedEvidenceBriefQueryResult{
		Execution: testEvidenceQueryExecution("refund", evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, 10, "all", false, evidenceingestion.EvidenceQueryCompletionMorphologyCandidates),
		Matches:   []evidenceingestion.ProposalSearchResult{{Record: testQueryResult("occ:legacy"), Rank: 1}},
	}
	for _, schema := range []string{GroundedEvidenceBriefSchemaV2, GroundedEvidenceBriefSchemaV5} {
		response, err := mapGroundedEvidenceBrief(result, schema)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("multisurface")) || bytes.Contains(data, []byte("retrieval_basis")) {
			t.Fatal("legacy response gained expanded search metadata")
		}
	}
}

func testMultisurfaceBriefResult() evidenceingestion.GroundedEvidenceBriefQueryResult {
	baseline := testEvidenceQueryExecution("satellite", evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, 10, "all", false, evidenceingestion.EvidenceQueryCompletionBoundedNoMatch)
	execution := baseline
	execution.QueryMode = evidenceingestion.EvidenceQueryModeExperimentalMultisurfaceV1
	execution.PlanVersion = evidenceingestion.EvidenceQueryPlanExperimentalMultisurfaceV1
	execution.NormalizerVersion = evidenceingestion.EvidenceQueryNormalizerExperimentalMultisurfaceV1
	execution.CandidateCount = 1
	execution.Multisurface = &evidenceingestion.EvidenceMultisurfaceExecution{Baseline: baseline, NormalizedQueryTerms: []string{"satellit"}, SearchedSourceViews: 1}
	record := testQueryResult("occ:multisurface")
	record.RawContentHash, record.RenderedContentHash = "sha256:identity", "sha256:identity"
	quote := "The bulletin does not report satellite damage."
	span := evidenceingestion.ResolvedSourceRef{ExtractionViewID: record.ExtractionViewID, SpanID: "span:S2", StartByte: 100, EndByte: 100 + len(quote), QuotedText: quote, QuotedTextHash: groundedEvidenceRepositoryTextHash(quote)}
	basis := &evidenceingestion.EvidenceRetrievalBasis{
		NormalizedQueryTerms: []string{"satellit"},
		Surfaces: []evidenceingestion.EvidenceRetrievalSurface{
			{Surface: evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement, Status: "searched", MissingTerms: []string{"satellit"}},
			{Surface: "extraction_views.rendered_content", Status: "searched", MatchedTerms: []string{"satellit"}, SourceSnapshotID: record.SourceSnapshotID,
				ExtractionViewID: record.ExtractionViewID, RawContentHash: record.RawContentHash, RenderedContentHash: record.RenderedContentHash,
				MatchedSpans: []evidenceingestion.EvidenceRetrievalSpan{{SourceRef: span, MatchedTerms: []string{"satellit"}, WithinProposalSourceRefs: false}}},
		},
	}
	return evidenceingestion.GroundedEvidenceBriefQueryResult{Execution: execution, Matches: []evidenceingestion.ProposalSearchResult{{Record: record, Rank: 1, RetrievalBasis: basis}}}
}
