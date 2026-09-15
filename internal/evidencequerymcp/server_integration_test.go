//go:build integration

package evidencequerymcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationGetEvidenceRecordRoundTripReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	submit := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-round-trip"))
	before := tableCounts(t, ctx, pool)
	resp := callGetEvidenceRecord(t, ctx, queryServer, submit.ProposalOccurrenceID)
	after := tableCounts(t, ctx, pool)

	if before != after {
		t.Fatalf("table counts changed during read: before %+v after %+v", before, after)
	}
	if resp.RecordRef.Kind != "proposal" || resp.RecordRef.ID != submit.ProposalOccurrenceID {
		t.Fatalf("record ref = %+v, want proposal %s", resp.RecordRef, submit.ProposalOccurrenceID)
	}
	if resp.AdmissionOutcome != "pending" {
		t.Fatalf("admission outcome = %q, want pending", resp.AdmissionOutcome)
	}
	if resp.CanonicalRef != nil {
		t.Fatalf("canonical ref = %q, want nil", *resp.CanonicalRef)
	}
	if resp.ProposalFingerprint != submit.ProposalFingerprint {
		t.Fatalf("fingerprint = %q, want %q", resp.ProposalFingerprint, submit.ProposalFingerprint)
	}
	if len(resp.SourceRefs) != 1 || resp.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", resp.SourceRefs)
	}
	if resp.Source.SourceSnapshotID != submit.SourceSnapshotID || resp.Extractor.ExtractionAttemptID != submit.ExtractionAttemptID {
		t.Fatalf("provenance mismatch: response %+v submit %+v", resp, submit)
	}
}

func TestIntegrationExternalAgentSourceProposalReviewAndAdmission(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	source, err := ingestServer.SubmitExternalSource(ctx, evidenceingestionmcp.SubmitExternalSourceRequest{
		SchemaVersion:   evidenceingestion.ExternalSourceEnvelopeSchemaV1,
		RequestID:       "query-external-agent-source",
		SourceSystem:    "jira",
		SourceNamespace: "acme/eng",
		ObjectType:      "issue",
		ObjectID:        "AHE-42",
		Revision:        "revision-42",
		SourceLocation:  "https://fixture.invalid/AHE-42",
		Title:           "Agent-first source intake",
		ContentFormat:   evidenceingestion.ExternalSourceContentFormatMarkdown,
		ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
		Content:         "# AHE-42\nExternal connector owns collection.\n",
		Coverage:        evidenceingestion.ExternalSourceCoverageExactExcerpt,
		Limitations:     []string{"comments were not requested"},
		CollectorID:     "claude-code",
		ConnectorID:     "atlassian-rovo",
		ObservedAt:      "2026-08-23T02:03:04Z",
		SourceUpdatedAt: "2026-08-23T02:00:00Z",
	})
	if err != nil {
		t.Fatalf("SubmitExternalSource() error = %v", err)
	}
	input, err := ingestServer.GetExtractorInput(ctx, evidenceingestionmcp.GetExtractorInputRequest{
		ExtractionViewID: source.ExtractionViewID,
	})
	if err != nil {
		t.Fatalf("GetExtractorInput() error = %v", err)
	}
	if input.SourceSystem != evidenceingestion.SourceSystemExternalDocument ||
		len(input.Spans) != 2 ||
		input.Spans[1].Text != "External connector owns collection." {
		t.Fatalf("external agent input = %+v", input)
	}

	proposal, err := ingestServer.SubmitExtractorOutput(ctx, evidenceingestionmcp.SubmitExtractorOutputRequest{
		RequestID:          "query-external-agent-proposal",
		SourceSnapshotID:   source.SourceSnapshotID,
		ExtractionViewID:   source.ExtractionViewID,
		ProducerSessionRef: "claude-code-session:integration-test",
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{
			Name:    "external-cooperating-agent",
			Version: "v1",
			Config: map[string]string{
				"producer_class": "external_agent",
			},
		},
		ExtractorOutput: evidenceingestion.FrozenExtractorOutput{
			Proposals: []evidenceingestion.ExtractorProposalOutput{{
				ProposalLocalID: "collection-owner",
				StatementText:   "The connector is responsible for external collection.",
				EvidenceRefs:    []string{input.Spans[1].SpanID},
			}},
		},
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput() error = %v", err)
	}

	review := callGetEvidenceRecord(t, ctx, queryServer, proposal.ProposalOccurrenceID)
	if review.AdmissionOutcome != "pending" ||
		review.StatementText != "The connector is responsible for external collection." ||
		len(review.SourceRefs) != 1 ||
		review.SourceRefs[0].QuotedText != "External connector owns collection." ||
		review.Extractor.Name != "external-cooperating-agent" ||
		review.Extractor.ProducerSessionRef != "claude-code-session:integration-test" {
		t.Fatalf("external agent review record = %+v", review)
	}
	if review.Source.ExternalSource == nil ||
		review.Source.ExternalSource.Title != "Agent-first source intake" ||
		review.Source.ExternalSource.SourceLocation != "https://fixture.invalid/AHE-42" ||
		review.Source.ExternalSource.Revision != "revision-42" ||
		review.Source.ExternalSource.Coverage != evidenceingestion.ExternalSourceCoverageExactExcerpt ||
		len(review.Source.ExternalSource.Limitations) != 1 {
		t.Fatalf("external source review provenance = %+v", review.Source.ExternalSource)
	}

	admission := callAdmitPendingProposal(t, ctx, ingestServer, evidenceingestionmcp.AdmitPendingProposalRequest{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           "integration-human-reviewer",
		DecisionReason:       "proposal and source quote reviewed",
	})
	admitted := callGetEvidenceRecord(t, ctx, queryServer, proposal.ProposalOccurrenceID)
	if admitted.AdmissionOutcome != "admitted" ||
		admitted.CanonicalRef == nil ||
		*admitted.CanonicalRef != admission.CanonicalRef ||
		admitted.Extractor.ProducerSessionRef != "claude-code-session:integration-test" {
		t.Fatalf("external agent admission = %+v", admitted)
	}
}

func TestIntegrationGetEvidenceRecordAfterAdmissionReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	submit := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-admitted"))
	admission := callAdmitPendingProposal(t, ctx, ingestServer, evidenceingestionmcp.AdmitPendingProposalRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "fixture statement accepted",
	})
	before := tableCounts(t, ctx, pool)
	resp := callGetEvidenceRecord(t, ctx, queryServer, submit.ProposalOccurrenceID)
	after := tableCounts(t, ctx, pool)

	if before != after {
		t.Fatalf("table counts changed during admitted read: before %+v after %+v", before, after)
	}
	if resp.RecordRef.Kind != "proposal" || resp.RecordRef.ID != submit.ProposalOccurrenceID {
		t.Fatalf("record ref = %+v, want proposal %s", resp.RecordRef, submit.ProposalOccurrenceID)
	}
	if resp.AdmissionOutcome != "admitted" {
		t.Fatalf("admission outcome = %q, want admitted", resp.AdmissionOutcome)
	}
	if resp.CanonicalRef == nil || *resp.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("canonical ref = %v, want %s", resp.CanonicalRef, admission.CanonicalRef)
	}
	if resp.ProposalFingerprint != submit.ProposalFingerprint {
		t.Fatalf("fingerprint = %q, want %q", resp.ProposalFingerprint, submit.ProposalFingerprint)
	}
	if len(resp.SourceRefs) != 1 || resp.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", resp.SourceRefs)
	}

	before = tableCounts(t, ctx, pool)
	canonical := callGetCanonicalEvidenceRecord(t, ctx, queryServer, admission.CanonicalRef)
	after = tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("table counts changed during canonical read: before %+v after %+v", before, after)
	}
	if canonical.RecordRef.Kind != "canonical_evidence" || canonical.RecordRef.ID != admission.CanonicalRef {
		t.Fatalf("canonical record ref = %+v, want %s", canonical.RecordRef, admission.CanonicalRef)
	}
	if canonical.ProposalOriginRef == nil || canonical.ProposalOriginRef.ID != submit.ProposalOccurrenceID {
		t.Fatalf("canonical proposal origin = %+v, want %s", canonical.ProposalOriginRef, submit.ProposalOccurrenceID)
	}
	if canonical.Canonical == nil || canonical.Canonical.NodeKind != "source_claim" || canonical.Canonical.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("canonical record = %+v", canonical.Canonical)
	}
	if canonical.CanonicalRef == nil || *canonical.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("canonical self ref = %v, want %s", canonical.CanonicalRef, admission.CanonicalRef)
	}
	if len(canonical.SourceRefs) != 1 || canonical.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("canonical source refs = %+v, want span:S1", canonical.SourceRefs)
	}
}

func TestIntegrationCanonicalReadViewHandlePathAndDiagnosticsReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	ctx, err := BindCanonicalReadViewOwner(ctx, "integration-query-consumer")
	if err != nil {
		t.Fatal(err)
	}
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	submit := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-canonical-read-view"))
	admission := callAdmitPendingProposal(t, ctx, ingestServer, evidenceingestionmcp.AdmitPendingProposalRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "exercise external canonical read view handle",
	})
	if len(admission.RawEvidenceNodeIDs) != 1 || len(admission.CanonicalEdgeIDs) != 1 {
		t.Fatalf("admission topology = %+v", admission)
	}

	before := tableCounts(t, ctx, pool)
	opened, err := queryServer.OpenCanonicalReadView(ctx, OpenCanonicalReadViewRequest{
		RootNodeIDs: []string{admission.CanonicalRef},
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupportsClaim},
		MaxDepth:    1,
		MaxNodes:    8,
		MaxEdges:    8,
	})
	if err != nil {
		t.Fatalf("OpenCanonicalReadView() error = %v", err)
	}
	if opened.View.Truncated || opened.View.NodeCount != 2 || opened.View.EdgeCount != 1 || opened.View.Handle == "" {
		t.Fatalf("opened canonical read view = %+v", opened.View)
	}

	path, err := queryServer.FindCanonicalPath(ctx, FindCanonicalPathRequest{
		Handle:     opened.View.Handle,
		FromNodeID: admission.RawEvidenceNodeIDs[0],
		ToNodeID:   admission.CanonicalRef,
		Relations:  []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupportsClaim},
	})
	if err != nil {
		t.Fatalf("FindCanonicalPath() error = %v", err)
	}
	if !path.Witness.Found || len(path.Witness.EdgeIDs) != 1 || path.Witness.EdgeIDs[0] != admission.CanonicalEdgeIDs[0] {
		t.Fatalf("path witness = %+v", path.Witness)
	}

	diagnostics, err := queryServer.GetCanonicalTopologyDiagnostics(ctx, GetCanonicalTopologyDiagnosticsRequest{Handle: opened.View.Handle})
	if err != nil {
		t.Fatalf("GetCanonicalTopologyDiagnostics() error = %v", err)
	}
	if diagnostics.Diagnostics.DerivedFromCycle != nil ||
		diagnostics.Diagnostics.SupersedesCycle != nil ||
		len(diagnostics.ConflictClusters) != 0 {
		t.Fatalf("topology diagnostics = %+v", diagnostics)
	}
	after := tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("canonical view reads mutated tables: before %+v after %+v", before, after)
	}
}

func TestIntegrationGroundedSearchCanonicalNeighborsAndRelationProvenanceReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	submit := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-p5-canonical"))
	admission := callAdmitPendingProposal(t, ctx, ingestServer, evidenceingestionmcp.AdmitPendingProposalRequest{
		ProposalOccurrenceID: submit.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "P5 grounded query fixture",
	})
	if len(admission.RawEvidenceNodeIDs) != 1 || len(admission.CanonicalEdgeIDs) != 1 {
		t.Fatalf("admission topology = %+v", admission)
	}

	before := tableCounts(t, ctx, pool)
	search := callSearchEvidenceRecords(t, ctx, queryServer, SearchEvidenceRecordsRequest{
		Query:    "refunds completed",
		SourceID: "fixture-refund-policy",
		Limit:    10,
	})
	if search.Count != 1 || search.Matches[0].Record.RecordRef.ID != submit.ProposalOccurrenceID || search.Matches[0].Rank <= 0 {
		t.Fatalf("grounded search = %+v", search)
	}
	if search.Matches[0].Record.CanonicalRef == nil || len(search.Matches[0].Record.SourceRefs) != 1 {
		t.Fatalf("search match lacks canonical/source provenance: %+v", search.Matches[0])
	}
	historical := callSearchEvidenceRecords(t, ctx, queryServer, SearchEvidenceRecordsRequest{
		Query:          "refunds completed",
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeHistorical,
		Limit:          10,
	})
	if historical.Count != 0 {
		t.Fatalf("historical search fabricated lifecycle for source-bound record: %+v", historical)
	}

	neighbors := callListEvidenceNeighbors(t, ctx, queryServer, ListEvidenceNeighborsRequest{
		CanonicalID: admission.RawEvidenceNodeIDs[0],
		Direction:   evidenceingestion.RelationDirectionOutgoing,
		Relation:    string(evidencegraph.CanonicalSupportsClaim),
		Limit:       10,
	})
	if neighbors.Depth != 1 || neighbors.Count != 1 || neighbors.Neighbors[0].AdjacentCanonical == nil {
		t.Fatalf("canonical neighbors = %+v", neighbors)
	}
	if neighbors.Neighbors[0].AdjacentCanonical.RecordRef.ID != admission.CanonicalRef {
		t.Fatalf("canonical adjacent record = %+v, want %s", neighbors.Neighbors[0].AdjacentCanonical.RecordRef, admission.CanonicalRef)
	}

	provenance := callGetRelationProvenance(t, ctx, queryServer, GetRelationProvenanceRequest{CanonicalEdgeID: admission.CanonicalEdgeIDs[0]})
	if provenance.Surface != "canonical_evidence" || provenance.CanonicalEdge == nil || provenance.OriginRecord.RecordRef.ID != submit.ProposalOccurrenceID {
		t.Fatalf("canonical relation provenance = %+v", provenance)
	}
	after := tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("P5 canonical queries mutated tables: before %+v after %+v", before, after)
	}
}

func TestIntegrationGroundedEvidenceBriefDeterministicLexicalRecovery(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	submit := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-recovery"))

	tests := []struct {
		name             string
		query            string
		queryMode        string
		wantMatches      int
		wantCompletion   string
		wantAttempts     int
		wantObservations []string
	}{
		{
			name:             "plural exact baseline",
			query:            "refunds completed",
			wantMatches:      1,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionMorphologyCandidates,
			wantAttempts:     2,
			wantObservations: []string{"morphology_candidates_found"},
		},
		{
			name:             "singular morphology",
			query:            "refund complete",
			wantMatches:      1,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionMorphologyCandidates,
			wantAttempts:     2,
			wantObservations: []string{"exact_lexical_no_match", "morphology_candidates_found"},
		},
		{
			name:             "derivational morphology",
			query:            "refund completion",
			wantMatches:      1,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionMorphologyCandidates,
			wantAttempts:     2,
			wantObservations: []string{"exact_lexical_no_match", "morphology_candidates_found"},
		},
		{
			name:             "bounded extra term relaxation",
			query:            "refund completion timing",
			wantMatches:      1,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionRelatedCandidates,
			wantAttempts:     3,
			wantObservations: []string{"exact_lexical_no_match", "related_candidates_found"},
		},
		{
			name:             "requested constraint remains candidate only",
			query:            "refunds completed 24 hours",
			wantMatches:      1,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionRelatedCandidates,
			wantAttempts:     3,
			wantObservations: []string{"exact_lexical_no_match", "related_candidates_found"},
		},
		{
			name:             "bounded unrelated no match",
			query:            "shipping address retention period",
			wantMatches:      0,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionBoundedNoMatch,
			wantAttempts:     3,
			wantObservations: []string{"exact_lexical_no_match", "bounded_retrieval_no_match"},
		},
		{
			name:             "exact mode preserves miss",
			query:            "refund complete",
			queryMode:        evidenceingestion.EvidenceQueryModeExactLexical,
			wantMatches:      0,
			wantCompletion:   evidenceingestion.EvidenceQueryCompletionBoundedNoMatch,
			wantAttempts:     1,
			wantObservations: []string{"exact_lexical_no_match", "bounded_retrieval_no_match"},
		},
	}

	before := tableCounts(t, ctx, pool)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			brief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
				Query:     tt.query,
				QueryMode: tt.queryMode,
				SourceID:  "fixture-refund-policy",
				Limit:     10,
			})
			if brief.SchemaVersion != groundedEvidenceBriefSchemaV2 ||
				brief.Counts.ReturnedMatches != tt.wantMatches ||
				brief.QueryExecution.CompletionReason != tt.wantCompletion ||
				brief.QueryExecution.QueryCount != tt.wantAttempts ||
				len(brief.QueryExecution.Attempts) != tt.wantAttempts {
				t.Fatalf("brief execution = %+v", brief)
			}
			if !reflect.DeepEqual(brief.ObservationCodes, tt.wantObservations) {
				t.Fatalf("observation codes = %+v, want %+v", brief.ObservationCodes, tt.wantObservations)
			}
			if brief.QueryExecution.Filters.SourceID != "fixture-refund-policy" ||
				brief.QueryExecution.SearchSurface != evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement ||
				!brief.QueryExecution.SearchCompleteWithinSurface ||
				brief.QueryExecution.GlobalAbsenceInferenceAllowed {
				t.Fatalf("query boundary = %+v", brief.QueryExecution)
			}
			if tt.wantMatches == 0 {
				if len(brief.SourceScopes) != 0 {
					t.Fatalf("no-match source scopes = %+v", brief.SourceScopes)
				}
				return
			}
			if brief.Matches[0].RecordRef.ID != submit.ProposalOccurrenceID {
				t.Fatalf("match = %+v, want %s", brief.Matches[0], submit.ProposalOccurrenceID)
			}
			if len(brief.SourceScopes) != 1 ||
				brief.SourceScopes[0].Source.SourceSystem != evidenceingestion.SourceSystemManualText ||
				brief.SourceScopes[0].Source.RepositorySnapshot != nil {
				t.Fatalf("manual source scope = %+v", brief.SourceScopes)
			}
		})
	}
	after := tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("query recovery mutated tables: before %+v after %+v", before, after)
	}
}

func TestIntegrationGroundedEvidenceBriefV3HydratesPostRetrievalSourceContext(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	submit := callSubmitManualEvidence(
		t,
		ctx,
		ingestServer,
		integrationSubmitRequest(t, "query-source-context-v3"),
	)

	before := tableCounts(t, ctx, pool)
	defaultV2 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		GetGroundedEvidenceBriefRequest{
			Query:            "refund complete",
			SourceSnapshotID: submit.SourceSnapshotID,
			Limit:            10,
		},
	)
	explicitV2 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		GetGroundedEvidenceBriefRequest{
			Query:            "refund complete",
			ResponseSchema:   GroundedEvidenceBriefSchemaV2,
			SourceSnapshotID: submit.SourceSnapshotID,
			Limit:            10,
		},
	)
	v3 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		GetGroundedEvidenceBriefRequest{
			Query:            "refund complete",
			ResponseSchema:   GroundedEvidenceBriefSchemaV3,
			SourceSnapshotID: submit.SourceSnapshotID,
			Limit:            10,
		},
	)
	replayedV3 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		GetGroundedEvidenceBriefRequest{
			Query:            "refund complete",
			ResponseSchema:   GroundedEvidenceBriefSchemaV3,
			SourceSnapshotID: submit.SourceSnapshotID,
			Limit:            10,
		},
	)
	after := tableCounts(t, ctx, pool)

	if before != after {
		t.Fatalf("source-context brief mutated tables: before %+v after %+v", before, after)
	}
	if !reflect.DeepEqual(defaultV2, explicitV2) {
		t.Fatalf("default and explicit v2 differ:\ndefault  %+v\nexplicit %+v", defaultV2, explicitV2)
	}
	if defaultV2.SchemaVersion != GroundedEvidenceBriefSchemaV2 ||
		len(defaultV2.Matches) != 1 ||
		defaultV2.Matches[0].SourceContext != nil {
		t.Fatalf("default v2 = %+v", defaultV2)
	}
	if !reflect.DeepEqual(v3, replayedV3) {
		t.Fatalf("v3 replay differs:\nfirst  %+v\nsecond %+v", v3, replayedV3)
	}
	if v3.SchemaVersion != GroundedEvidenceBriefSchemaV3 ||
		len(v3.Matches) != 1 ||
		v3.Matches[0].RecordRef.ID != submit.ProposalOccurrenceID ||
		v3.Matches[0].SourceContext == nil {
		t.Fatalf("v3 identity = %+v", v3)
	}
	sourceContext := v3.Matches[0].SourceContext
	if sourceContext.Status != evidenceingestion.GroundedEvidenceSourceContextStatusAvailable ||
		sourceContext.SearchParticipation ||
		!reflect.DeepEqual(sourceContext.SearchCore, v3.Matches[0].SourceRefs) ||
		sourceContext.SourceView == nil ||
		sourceContext.SourceView.Status != evidenceingestion.BoundedSourceViewStatusAvailable ||
		sourceContext.AtomicContainer == nil ||
		sourceContext.ContextEnvelope == nil {
		t.Fatalf("v3 source context = %+v", sourceContext)
	}
	if sourceContext.AtomicContainer.UnitKind != evidenceingestion.SourceStructureUnitParagraph ||
		sourceContext.AtomicContainer.UnitClass != evidenceingestion.SourceStructureUnitClassContent ||
		sourceContext.AtomicContainer.ExactText !=
			"Refunds must be completed within 7 days.\nThis rule applies only to overseas orders." ||
		len(sourceContext.AtomicContainer.SourceRefs) != 2 {
		t.Fatalf("v3 atomic container = %+v", sourceContext.AtomicContainer)
	}

	projectedV3 := v3
	projectedV3.SchemaVersion = GroundedEvidenceBriefSchemaV2
	projectedV3.Matches[0].SourceContext = nil
	if !reflect.DeepEqual(defaultV2, projectedV3) {
		t.Fatalf(
			"v3 changes the v2 base projection:\nv2 %+v\nv3 %+v",
			defaultV2,
			projectedV3,
		)
	}
}

func TestIntegrationGroundedEvidenceBriefV3WithholdsOverBudgetSourceView(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	request := integrationSubmitRequest(t, "query-source-context-over-budget-v3")
	request.SourceID = "fixture-source-context-over-budget"
	request.RawText =
		"Refunds must be completed within 7 days.\n" +
			strings.Repeat("x", int(evidenceingestion.BoundedSourceViewMaxSpanBytesV1)+1) +
			"\n"
	submit := callSubmitManualEvidence(t, ctx, ingestServer, request)

	before := tableCounts(t, ctx, pool)
	v3 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		GetGroundedEvidenceBriefRequest{
			Query:            "refund complete",
			ResponseSchema:   GroundedEvidenceBriefSchemaV3,
			SourceSnapshotID: submit.SourceSnapshotID,
			Limit:            10,
		},
	)
	after := tableCounts(t, ctx, pool)

	if before != after {
		t.Fatalf("over-budget brief mutated tables: before %+v after %+v", before, after)
	}
	if len(v3.Matches) != 1 || v3.Matches[0].SourceContext == nil {
		t.Fatalf("over-budget v3 = %+v", v3)
	}
	sourceContext := v3.Matches[0].SourceContext
	if sourceContext.Status !=
		evidenceingestion.GroundedEvidenceSourceContextStatusSourceViewOverBudget ||
		sourceContext.SourceView == nil ||
		sourceContext.SourceView.Status != evidenceingestion.BoundedSourceViewStatusOverBudget ||
		sourceContext.SourceView.LimitReason != evidenceingestion.BoundedSourceViewLimitAtomicSpan ||
		sourceContext.AtomicContainer != nil ||
		sourceContext.ContextEnvelope != nil {
		t.Fatalf("over-budget source context = %+v", sourceContext)
	}
}

func TestIntegrationGroundedEvidenceBriefMorphologyStablePairedQueries(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	sourceID := "fixture-proposal-lifecycle"
	singular := callSubmitManualEvidence(t, ctx, ingestServer, integrationStatementRequest(
		t,
		"query-morphology-stable-singular",
		sourceID,
		"v1",
		"A proposal lifecycle remains pending.",
	))
	plural := callSubmitManualEvidence(t, ctx, ingestServer, integrationStatementRequest(
		t,
		"query-morphology-stable-plural",
		sourceID,
		"v2",
		"Proposals lifecycle records remain pending.",
	))

	singularRecovery := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:    "proposal lifecycle",
		SourceID: sourceID,
		Limit:    10,
	})
	pluralRecovery := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:    "proposals lifecycle",
		SourceID: sourceID,
		Limit:    10,
	})

	singularIDs := groundedEvidenceBriefMatchIDs(singularRecovery)
	pluralIDs := groundedEvidenceBriefMatchIDs(pluralRecovery)
	if !reflect.DeepEqual(singularIDs, pluralIDs) {
		t.Fatalf("recovery result IDs differ: singular %+v plural %+v", singularIDs, pluralIDs)
	}
	if len(singularIDs) != 2 {
		t.Fatalf("recovery result IDs = %+v, want both %s and %s", singularIDs, singular.ProposalOccurrenceID, plural.ProposalOccurrenceID)
	}
	for _, brief := range []GroundedEvidenceBriefResponse{singularRecovery, pluralRecovery} {
		if brief.QueryExecution.PlanVersion != evidenceingestion.EvidenceQueryPlanRecoveryV2 ||
			brief.QueryExecution.CompletionReason != evidenceingestion.EvidenceQueryCompletionMorphologyCandidates ||
			brief.QueryExecution.QueryCount != 2 ||
			len(brief.QueryExecution.Attempts) != 2 ||
			brief.QueryExecution.Attempts[0].Strategy != evidenceingestion.EvidenceQueryStrategyExactSimple ||
			brief.QueryExecution.Attempts[0].CandidateCount != 1 ||
			brief.QueryExecution.Attempts[1].Strategy != evidenceingestion.EvidenceQueryStrategyEnglishMorphology ||
			brief.QueryExecution.Attempts[1].CandidateCount != 2 {
			t.Fatalf("recovery execution = %+v", brief.QueryExecution)
		}
	}

	singularExact := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:     "proposal lifecycle",
		QueryMode: evidenceingestion.EvidenceQueryModeExactLexical,
		SourceID:  sourceID,
		Limit:     10,
	})
	pluralExact := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:     "proposals lifecycle",
		QueryMode: evidenceingestion.EvidenceQueryModeExactLexical,
		SourceID:  sourceID,
		Limit:     10,
	})
	if got := groundedEvidenceBriefMatchIDs(singularExact); !reflect.DeepEqual(got, []string{singular.ProposalOccurrenceID}) {
		t.Fatalf("singular exact IDs = %+v, want %s", got, singular.ProposalOccurrenceID)
	}
	if got := groundedEvidenceBriefMatchIDs(pluralExact); !reflect.DeepEqual(got, []string{plural.ProposalOccurrenceID}) {
		t.Fatalf("plural exact IDs = %+v, want %s", got, plural.ProposalOccurrenceID)
	}
	for _, brief := range []GroundedEvidenceBriefResponse{singularExact, pluralExact} {
		if brief.QueryExecution.PlanVersion != evidenceingestion.EvidenceQueryPlanExactV1 ||
			brief.QueryExecution.CompletionReason != evidenceingestion.EvidenceQueryCompletionExactCandidates ||
			brief.QueryExecution.QueryCount != 1 {
			t.Fatalf("exact execution = %+v", brief.QueryExecution)
		}
	}
}

func TestIntegrationGroundedEvidenceBriefReturnsConflictingCandidates(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	first := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-conflict-v1"))
	secondStatement := "Refunds must be completed within 30 days."
	second := callSubmitManualEvidence(t, ctx, ingestServer, integrationStatementRequest(
		t,
		"query-conflict-v2",
		"fixture-refund-policy",
		"v2",
		secondStatement,
	))

	brief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:    "refund completion deadline",
		SourceID: "fixture-refund-policy",
		Limit:    10,
	})
	if brief.QueryExecution.CompletionReason != evidenceingestion.EvidenceQueryCompletionRelatedCandidates ||
		brief.Counts.ReturnedMatches != 2 ||
		brief.Counts.Admission.Pending != 2 {
		t.Fatalf("conflicting brief = %+v", brief)
	}
	got := map[string]string{}
	for _, match := range brief.Matches {
		got[match.RecordRef.ID] = match.StatementText
	}
	if got[first.ProposalOccurrenceID] != "Refunds must be completed within 7 days." ||
		got[second.ProposalOccurrenceID] != secondStatement {
		t.Fatalf("conflicting candidates = %+v", got)
	}
}

func TestIntegrationRepositoryRelationNeighborsAndProvenanceReadOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not on PATH")
	}
	ctx, pool := integrationPool(t)
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	root := t.TempDir()
	writeRepositoryFile(t, filepath.Join(root, "go.mod"), "module example.com/query-relations\n\ngo 1.22\n")
	writeRepositoryFile(t, filepath.Join(root, "main.go"), "package sample\n\nimport \"example.com/query-relations/worker\"\n\nfunc helper() {}\n\nfunc Main() {\n\thelper()\n\tworker.Run()\n}\n")
	writeRepositoryFile(t, filepath.Join(root, "worker", "worker.go"), "package worker\n\nfunc Run() {}\n")
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.name", "AHE Test")
	runGit(t, root, "config", "user.email", "ahe-test@example.com")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "--quiet", "-m", "initial")
	snapshot, err := evidenceingestion.CaptureGitRepositorySnapshot(ctx, pool, evidenceingestion.GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "query-relations",
		CommitSHA:     runGit(t, root, "rev-parse", "HEAD"),
		RequestID:     "query-relations-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	run, err := evidenceingestion.RunRepositoryGoplsExtractor(ctx, pool, evidenceingestion.RepositoryGoplsRequest{
		RequestID:            "query-relations-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		WorkspaceRoot:        root,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoplsExtractor() error = %v", err)
	}

	var callRecord evidenceingestion.ProposalQueryResult
	for _, occurrenceID := range run.ProposalOccurrenceIDs {
		record, err := evidenceingestion.GetProposalByOccurrenceID(ctx, pool, occurrenceID)
		if err != nil {
			t.Fatalf("GetProposalByOccurrenceID(%s) error = %v", occurrenceID, err)
		}
		if record.CodeRelation != nil && record.CodeRelation.RelationKind == evidenceingestion.CodeRelationKindCall && record.CodeRelation.Target.QualifiedName == "worker.Run" {
			callRecord = record
			break
		}
	}
	if callRecord.CodeRelation == nil || callRecord.CodeRelation.Caller == nil {
		t.Fatalf("cross-file call relation not found in run %+v", run)
	}
	callerSymbol := callRecord.CodeRelation.Caller.SymbolRef

	inactive := callListEvidenceNeighbors(t, ctx, queryServer, ListEvidenceNeighborsRequest{
		SymbolRef: callerSymbol,
		Direction: evidenceingestion.RelationDirectionOutgoing,
		Relation:  evidenceingestion.CodeRelationKindCall,
		Limit:     10,
	})
	if inactive.Count != 0 || inactive.LifecycleScope != evidenceingestion.ProposalLifecycleScopeActive {
		t.Fatalf("inactive generation leaked into active neighbors: %+v", inactive)
	}
	exactInactive := callListEvidenceNeighbors(t, ctx, queryServer, ListEvidenceNeighborsRequest{
		SymbolRef:          callerSymbol,
		Direction:          evidenceingestion.RelationDirectionOutgoing,
		Relation:           evidenceingestion.CodeRelationKindCall,
		SourceGenerationID: run.SourceGeneration.ID,
		Limit:              10,
	})
	if exactInactive.Count == 0 || exactInactive.LifecycleScope != evidenceingestion.ProposalLifecycleScopeAll {
		t.Fatalf("exact inactive generation neighbors = %+v", exactInactive)
	}

	if _, err := evidenceingestion.ActivateRepositorySourceGeneration(ctx, pool, evidenceingestion.RepositorySourceGenerationActivationInput{
		RequestID:          "query-relations-activate",
		SourceGenerationID: run.SourceGeneration.ID,
	}); err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration() error = %v", err)
	}
	before := tableCounts(t, ctx, pool)
	active := callListEvidenceNeighbors(t, ctx, queryServer, ListEvidenceNeighborsRequest{
		SymbolRef: callerSymbol,
		Direction: evidenceingestion.RelationDirectionOutgoing,
		Relation:  evidenceingestion.CodeRelationKindCall,
		Limit:     10,
	})
	if active.Count == 0 {
		t.Fatalf("active repository neighbors = %+v", active)
	}
	foundCrossFile := false
	for _, neighbor := range active.Neighbors {
		if neighbor.Relation.RelationRef.ID == callRecord.ProposalOccurrenceID {
			foundCrossFile = true
			if neighbor.AdjacentDeclaration == nil || neighbor.AdjacentDeclaration.SymbolRef != callRecord.CodeRelation.Target.SymbolRef {
				t.Fatalf("cross-file adjacent declaration = %+v", neighbor.AdjacentDeclaration)
			}
		}
	}
	if !foundCrossFile {
		t.Fatalf("active neighbors omitted relation %s: %+v", callRecord.ProposalOccurrenceID, active)
	}
	provenance := callGetRelationProvenance(t, ctx, queryServer, GetRelationProvenanceRequest{ProposalOccurrenceID: callRecord.ProposalOccurrenceID})
	if provenance.Surface != "repository_code" || provenance.CodeRelation == nil || provenance.OriginRecord.SourceGeneration == nil || !provenance.OriginRecord.SourceGeneration.Active {
		t.Fatalf("repository relation provenance = %+v", provenance)
	}
	search := callSearchEvidenceRecords(t, ctx, queryServer, SearchEvidenceRecordsRequest{
		Query:          "Go",
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeActive,
		Limit:          100,
	})
	if search.Count == 0 {
		t.Fatalf("active repository grounded search = %+v", search)
	}
	brief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:          "Go",
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeActive,
		Limit:          100,
	})
	replayedBrief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:          "Go",
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeActive,
		Limit:          100,
	})
	if !reflect.DeepEqual(brief, replayedBrief) {
		t.Fatalf("grounded evidence brief replay differs:\nfirst  %+v\nsecond %+v", brief, replayedBrief)
	}
	if brief.Counts.ReturnedMatches == 0 || brief.Counts.Lifecycle.RepositoryActive != brief.Counts.ReturnedMatches || brief.Boundary.Truncated {
		t.Fatalf("active repository brief = %+v", brief)
	}
	if len(brief.Coverage) != 1 || run.RepositoryGoplsCoverage == nil || brief.Coverage[0].Diagnostic != *run.RepositoryGoplsCoverage {
		t.Fatalf("repository brief coverage = %+v, want %+v", brief.Coverage, run.RepositoryGoplsCoverage)
	}
	v4Request := GetGroundedEvidenceBriefRequest{
		Query:          "Go",
		ResponseSchema: GroundedEvidenceBriefSchemaV4,
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeActive,
		Limit:          100,
	}
	briefV4 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		v4Request,
	)
	replayedBriefV4 := callGetGroundedEvidenceBrief(
		t,
		ctx,
		queryServer,
		v4Request,
	)
	if !reflect.DeepEqual(briefV4, replayedBriefV4) {
		t.Fatalf(
			"grounded evidence brief v4 replay differs:\nfirst  %+v\nsecond %+v",
			briefV4,
			replayedBriefV4,
		)
	}
	if briefV4.SchemaVersion != GroundedEvidenceBriefSchemaV4 ||
		briefV4.Counts.ReturnedMatches != brief.Counts.ReturnedMatches {
		t.Fatalf("active repository brief v4 = %+v", briefV4)
	}
	foundCrossFileContext := false
	for _, match := range briefV4.Matches {
		repositoryContext := match.RepositoryContext
		if match.SourceContext != nil ||
			repositoryContext == nil ||
			repositoryContext.Status !=
				evidenceingestion.GroundedEvidenceRepositoryContextStatusAvailable ||
			repositoryContext.SearchParticipation ||
			!reflect.DeepEqual(repositoryContext.SearchCore, match.SourceRefs) {
			t.Fatalf(
				"repository brief v4 match %s context = %+v",
				match.RecordRef.ID,
				repositoryContext,
			)
		}
		if match.RecordRef.ID != callRecord.ProposalOccurrenceID {
			continue
		}
		if repositoryContext.RecordKind !=
			evidenceingestion.GroundedEvidenceRepositoryRecordRelation ||
			repositoryContext.Relation == nil ||
			len(repositoryContext.Relation.Endpoints) != 3 ||
			repositoryContext.Relation.Endpoints[0].Role !=
				evidenceingestion.GroundedEvidenceRepositoryContextUsage ||
			repositoryContext.Relation.Endpoints[1].Role !=
				evidenceingestion.GroundedEvidenceRepositoryContextCaller ||
			repositoryContext.Relation.Endpoints[2].Role !=
				evidenceingestion.GroundedEvidenceRepositoryContextTarget ||
			repositoryContext.Relation.Endpoints[0].Path ==
				repositoryContext.Relation.Endpoints[2].Path {
			t.Fatalf(
				"cross-file repository context = %+v",
				repositoryContext,
			)
		}
		foundCrossFileContext = true
	}
	if !foundCrossFileContext {
		t.Fatalf(
			"repository brief v4 omitted cross-file context %s",
			callRecord.ProposalOccurrenceID,
		)
	}
	for _, followUp := range brief.FollowUps {
		assertGroundedEvidenceBriefFollowUpResolves(t, ctx, queryServer, followUp)
	}
	after := tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("repository P5 queries mutated tables: before %+v after %+v", before, after)
	}
}

func TestIntegrationListEvidenceRecordsReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	ingestServer, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	first := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-list-a"))
	second := callSubmitManualEvidence(t, ctx, ingestServer, integrationSubmitRequest(t, "query-list-b"))
	admission := callAdmitPendingProposal(t, ctx, ingestServer, evidenceingestionmcp.AdmitPendingProposalRequest{
		ProposalOccurrenceID: first.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "fixture statement accepted",
	})

	before := tableCounts(t, ctx, pool)
	all := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		SourceID: "fixture-refund-policy",
		Limit:    10,
	})
	after := tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("table counts changed during list: before %+v after %+v", before, after)
	}
	if all.Count != 2 || all.Limit != 10 || len(all.Records) != 2 {
		t.Fatalf("all list response = %+v", all)
	}
	records := recordsByID(all.Records)
	if records[first.ProposalOccurrenceID].CanonicalRef == nil || *records[first.ProposalOccurrenceID].CanonicalRef != admission.CanonicalRef {
		t.Fatalf("admitted listed record = %+v, want canonical %s", records[first.ProposalOccurrenceID], admission.CanonicalRef)
	}
	if records[second.ProposalOccurrenceID].AdmissionOutcome != "pending" || records[second.ProposalOccurrenceID].CanonicalRef != nil {
		t.Fatalf("pending listed record = %+v", records[second.ProposalOccurrenceID])
	}

	pending := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		AdmissionOutcome: "pending",
		Limit:            10,
	})
	if pending.Count != 1 || pending.Records[0].RecordRef.ID != second.ProposalOccurrenceID {
		t.Fatalf("pending list = %+v, want %s", pending, second.ProposalOccurrenceID)
	}

	admitted := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		SourceSnapshotID: first.SourceSnapshotID,
		AdmissionOutcome: "admitted",
		Limit:            10,
	})
	if admitted.Count != 1 || admitted.Records[0].RecordRef.ID != first.ProposalOccurrenceID {
		t.Fatalf("admitted list = %+v, want %s", admitted, first.ProposalOccurrenceID)
	}
	truncatedSearch := callSearchEvidenceRecords(t, ctx, queryServer, SearchEvidenceRecordsRequest{
		Query:    "refunds completed",
		SourceID: "fixture-refund-policy",
		Limit:    1,
	})
	if truncatedSearch.Count != 1 ||
		!truncatedSearch.QueryExecution.Truncated ||
		truncatedSearch.QueryExecution.SearchCompleteWithinSurface ||
		truncatedSearch.QueryExecution.CompletionReason != evidenceingestion.EvidenceQueryCompletionExactCandidates {
		t.Fatalf("truncated exact search = %+v", truncatedSearch)
	}
	beforeBrief := tableCounts(t, ctx, pool)
	truncatedBrief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:    "refunds completed",
		SourceID: "fixture-refund-policy",
		Limit:    1,
	})
	afterBrief := tableCounts(t, ctx, pool)
	if beforeBrief != afterBrief {
		t.Fatalf("grounded evidence brief mutated tables: before %+v after %+v", beforeBrief, afterBrief)
	}
	if truncatedBrief.Counts.ReturnedMatches != 1 ||
		!truncatedBrief.Boundary.Truncated ||
		!reflect.DeepEqual(truncatedBrief.ObservationCodes, []string{"morphology_candidates_found", "bounded_result_truncated"}) {
		t.Fatalf("truncated grounded evidence brief = %+v", truncatedBrief)
	}
	v5 := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:            "refund complete",
		ResponseSchema:   GroundedEvidenceBriefSchemaV5,
		SourceSnapshotID: first.SourceSnapshotID,
		AdmissionOutcome: "admitted",
		Limit:            10,
	})
	if v5.SchemaVersion != GroundedEvidenceBriefSchemaV5 ||
		len(v5.Matches) != 1 ||
		v5.Matches[0].CanonicalRef == nil ||
		v5.Matches[0].CanonicalRef.ID != admission.CanonicalRef ||
		v5.Matches[0].SourceContext != nil ||
		v5.Matches[0].RepositoryContext != nil {
		t.Fatalf("source-snapshot v5 brief = %+v", v5)
	}
	state := v5.Matches[0].RecordState
	if state == nil ||
		state.AuthorityStatus != groundedEvidenceAuthorityCanonicalAdmitted ||
		state.RecordLifecycle != groundedEvidenceRecordLifecycleSourceSnapshot ||
		state.RevisionKind != groundedEvidenceRevisionSourceVersion ||
		state.Revision != "v1" ||
		state.ExternalFreshnessStatus != groundedEvidenceExternalFreshnessNotEvaluated {
		t.Fatalf("source-snapshot v5 record state = %+v", state)
	}
	if afterV5 := tableCounts(t, ctx, pool); afterV5 != afterBrief {
		t.Fatalf("grounded evidence brief v5 mutated tables: before %+v after %+v", afterBrief, afterV5)
	}
}

func TestIntegrationRepositoryGenerationProjectionReadOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	root := t.TempDir()
	writeRepositoryFile(t, filepath.Join(root, "go.mod"), "module example.com/query-generation\n\ngo 1.22\n")
	writeRepositoryFile(t, filepath.Join(root, "main.go"), "package sample\n\nfunc Visible() {}\n")
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.name", "AHE Test")
	runGit(t, root, "config", "user.email", "ahe-test@example.com")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "--quiet", "-m", "initial")

	snapshot, err := evidenceingestion.CaptureGitRepositorySnapshot(ctx, pool, evidenceingestion.GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "query-generation",
		CommitSHA:     runGit(t, root, "rev-parse", "HEAD"),
		RequestID:     "query-generation-snapshot",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot() error = %v", err)
	}
	run, err := evidenceingestion.RunRepositoryGoParserExtractor(ctx, pool, evidenceingestion.RepositoryGoParserRequest{
		RequestID:            "query-generation-run",
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor() error = %v", err)
	}

	before := tableCounts(t, ctx, pool)
	defaultBeforeActivation := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		Limit:                10,
	})
	if defaultBeforeActivation.Count != 0 {
		t.Fatalf("default candidate list before activation = %+v, want empty", defaultBeforeActivation)
	}
	historicalBeforeActivation := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		SourceGenerationID:   run.SourceGeneration.ID,
		Limit:                10,
	})
	assertGenerationResponse(t, historicalBeforeActivation, run.SourceGeneration, false)
	exactBeforeActivation := callGetEvidenceRecord(t, ctx, queryServer, run.ProposalOccurrenceIDs[0])
	if exactBeforeActivation.SourceGeneration == nil || exactBeforeActivation.SourceGeneration.ID != run.SourceGeneration.ID || exactBeforeActivation.SourceGeneration.Active {
		t.Fatalf("exact candidate generation before activation = %+v", exactBeforeActivation.SourceGeneration)
	}
	if exactBeforeActivation.RepositoryLifecycle != nil {
		t.Fatalf("candidate lifecycle before activation = %+v, want nil", exactBeforeActivation.RepositoryLifecycle)
	}
	after := tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("table counts changed during candidate reads: before %+v after %+v", before, after)
	}

	if _, err := evidenceingestion.ActivateRepositorySourceGeneration(ctx, pool, evidenceingestion.RepositorySourceGenerationActivationInput{
		RequestID:          "query-generation-activate",
		SourceGenerationID: run.SourceGeneration.ID,
	}); err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration() error = %v", err)
	}
	before = tableCounts(t, ctx, pool)
	defaultAfterActivation := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		Limit:                10,
	})
	assertGenerationResponse(t, defaultAfterActivation, run.SourceGeneration, true)
	exactAfterActivation := callGetEvidenceRecord(t, ctx, queryServer, run.ProposalOccurrenceIDs[0])
	if exactAfterActivation.SourceGeneration == nil || exactAfterActivation.SourceGeneration.ID != run.SourceGeneration.ID || !exactAfterActivation.SourceGeneration.Active {
		t.Fatalf("exact generation after activation = %+v", exactAfterActivation.SourceGeneration)
	}
	if exactAfterActivation.RepositoryLifecycle == nil || exactAfterActivation.RepositoryLifecycle.State != evidenceingestion.RepositoryProposalLifecycleNew || exactAfterActivation.RepositoryLifecycle.CurrentProposalOccurrenceID != run.ProposalOccurrenceIDs[0] {
		t.Fatalf("exact lifecycle after activation = %+v", exactAfterActivation.RepositoryLifecycle)
	}
	after = tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("table counts changed during active reads: before %+v after %+v", before, after)
	}

	writeRepositoryFile(t, filepath.Join(root, "main.go"), "package sample\n\nfunc Replacement() {}\n")
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "--quiet", "-m", "replacement")
	secondSnapshot, err := evidenceingestion.CaptureGitRepositorySnapshot(ctx, pool, evidenceingestion.GitRepositorySnapshotConfig{
		WorkspaceRoot: root,
		RepoID:        "query-generation",
		CommitSHA:     runGit(t, root, "rev-parse", "HEAD"),
		RequestID:     "query-generation-snapshot-second",
	})
	if err != nil {
		t.Fatalf("CaptureGitRepositorySnapshot(second) error = %v", err)
	}
	secondRun, err := evidenceingestion.RunRepositoryGoParserExtractor(ctx, pool, evidenceingestion.RepositoryGoParserRequest{
		RequestID:            "query-generation-run-second",
		RepositorySnapshotID: secondSnapshot.RepositorySnapshot.ID,
	})
	if err != nil {
		t.Fatalf("RunRepositoryGoParserExtractor(second) error = %v", err)
	}
	if _, err := evidenceingestion.ActivateRepositorySourceGeneration(ctx, pool, evidenceingestion.RepositorySourceGenerationActivationInput{
		RequestID:          "query-generation-activate-second",
		SourceGenerationID: secondRun.SourceGeneration.ID,
	}); err != nil {
		t.Fatalf("ActivateRepositorySourceGeneration(second) error = %v", err)
	}

	before = tableCounts(t, ctx, pool)
	firstDefaultAfterSwitch := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		Limit:                10,
	})
	if firstDefaultAfterSwitch.Count != 0 {
		t.Fatalf("first default generation after switch = %+v, want empty", firstDefaultAfterSwitch)
	}
	firstHistoricalAfterSwitch := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		SourceGenerationID: run.SourceGeneration.ID,
		Limit:              10,
	})
	assertGenerationResponse(t, firstHistoricalAfterSwitch, run.SourceGeneration, false)
	for _, record := range firstHistoricalAfterSwitch.Records {
		if record.RepositoryLifecycle == nil || record.RepositoryLifecycle.SourceGenerationID != secondRun.SourceGeneration.ID || record.RepositoryLifecycle.State != evidenceingestion.RepositoryProposalLifecycleStale {
			t.Fatalf("first historical record %s lifecycle = %+v", record.RecordRef.ID, record.RepositoryLifecycle)
		}
	}
	secondDefaultAfterSwitch := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		RepositorySnapshotID: secondSnapshot.RepositorySnapshot.ID,
		Limit:                10,
	})
	assertGenerationResponse(t, secondDefaultAfterSwitch, secondRun.SourceGeneration, true)
	for _, record := range secondDefaultAfterSwitch.Records {
		if record.RepositoryLifecycle == nil || record.RepositoryLifecycle.State != evidenceingestion.RepositoryProposalLifecycleNew {
			t.Fatalf("second active record %s lifecycle = %+v", record.RecordRef.ID, record.RepositoryLifecycle)
		}
	}
	exactFirstAfterSwitch := callGetEvidenceRecord(t, ctx, queryServer, run.ProposalOccurrenceIDs[0])
	if exactFirstAfterSwitch.RepositoryLifecycle == nil || exactFirstAfterSwitch.RepositoryLifecycle.State != evidenceingestion.RepositoryProposalLifecycleStale {
		t.Fatalf("exact first lifecycle after switch = %+v", exactFirstAfterSwitch.RepositoryLifecycle)
	}
	historicalScope := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeHistorical,
		Limit:          100,
	})
	if historicalScope.Count != run.SourceGeneration.ProposalCount || historicalScope.LifecycleScope != evidenceingestion.ProposalLifecycleScopeHistorical {
		t.Fatalf("historical lifecycle scope = %+v", historicalScope)
	}
	for _, record := range historicalScope.Records {
		if record.SourceGeneration == nil || record.SourceGeneration.Active || record.SourceGeneration.ID != run.SourceGeneration.ID {
			t.Fatalf("historical lifecycle record = %+v", record.SourceGeneration)
		}
	}
	allScope := callListEvidenceRecords(t, ctx, queryServer, ListEvidenceRecordsRequest{
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeAll,
		Limit:          100,
	})
	if allScope.Count != run.SourceGeneration.ProposalCount+secondRun.SourceGeneration.ProposalCount {
		t.Fatalf("all lifecycle scope count = %d, want %d", allScope.Count, run.SourceGeneration.ProposalCount+secondRun.SourceGeneration.ProposalCount)
	}
	historicalBrief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:          "Go",
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeHistorical,
		Limit:          100,
	})
	if historicalBrief.Counts.ReturnedMatches == 0 || historicalBrief.Counts.Lifecycle.RepositoryHistorical != historicalBrief.Counts.ReturnedMatches || historicalBrief.Counts.Lifecycle.RepositoryActive != 0 {
		t.Fatalf("historical grounded evidence brief = %+v", historicalBrief)
	}
	activeBrief := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query: "Go",
		Limit: 100,
	})
	if activeBrief.Counts.ReturnedMatches == 0 || activeBrief.Counts.Lifecycle.RepositoryActive != activeBrief.Counts.ReturnedMatches || activeBrief.Counts.Lifecycle.RepositoryHistorical != 0 {
		t.Fatalf("active grounded evidence brief = %+v", activeBrief)
	}
	historicalBriefV5 := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:          "Go",
		ResponseSchema: GroundedEvidenceBriefSchemaV5,
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeHistorical,
		Limit:          100,
	})
	if len(historicalBriefV5.Matches) == 0 {
		t.Fatalf("historical grounded evidence brief v5 = %+v", historicalBriefV5)
	}
	for _, match := range historicalBriefV5.Matches {
		state := match.RecordState
		if state == nil ||
			state.RecordLifecycle != groundedEvidenceRecordLifecycleRepositoryHistorical ||
			state.RevisionKind != groundedEvidenceRevisionGitCommit ||
			state.Revision != snapshot.RepositorySnapshot.CommitSHA ||
			state.ExternalFreshnessStatus != groundedEvidenceExternalFreshnessNotEvaluated {
			t.Fatalf("historical grounded evidence state = %+v", state)
		}
	}
	activeBriefV5 := callGetGroundedEvidenceBrief(t, ctx, queryServer, GetGroundedEvidenceBriefRequest{
		Query:          "Go",
		ResponseSchema: GroundedEvidenceBriefSchemaV5,
		LifecycleScope: evidenceingestion.ProposalLifecycleScopeActive,
		Limit:          100,
	})
	if len(activeBriefV5.Matches) == 0 {
		t.Fatalf("active grounded evidence brief v5 = %+v", activeBriefV5)
	}
	for _, match := range activeBriefV5.Matches {
		state := match.RecordState
		if state == nil ||
			state.RecordLifecycle != groundedEvidenceRecordLifecycleRepositoryActive ||
			state.RevisionKind != groundedEvidenceRevisionGitCommit ||
			state.Revision != secondSnapshot.RepositorySnapshot.CommitSHA ||
			state.ExternalFreshnessStatus != groundedEvidenceExternalFreshnessNotEvaluated {
			t.Fatalf("active grounded evidence state = %+v", state)
		}
	}
	after = tableCounts(t, ctx, pool)
	if before != after {
		t.Fatalf("table counts changed during generation lifecycle reads: before %+v after %+v", before, after)
	}
}

func TestIntegrationGetEvidenceRecordStableErrors(t *testing.T) {
	ctx, pool := integrationPool(t)
	queryServer, err := NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}

	_, err = queryServer.CallTool(ctx, ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:missing"}`))
	assertToolError(t, err, toolErrorNotFound)

	_, err = queryServer.CallTool(ctx, ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":""}`))
	assertToolError(t, err, toolErrorInvalidRecordID)

	_, err = queryServer.CallTool(ctx, ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:missing","graph_layer":"canonical"}`))
	assertToolError(t, err, toolErrorInvalidRequest)

	_, err = queryServer.CallTool(ctx, ToolGetEvidenceRecord, []byte(`{"canonical_id":"canon-edge:missing"}`))
	assertToolError(t, err, toolErrorInvalidRecordID)

	_, err = queryServer.CallTool(ctx, ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:missing","canonical_id":"canon-node:missing"}`))
	assertToolError(t, err, toolErrorInvalidRequest)

	_, err = queryServer.CallTool(ctx, ToolListEvidenceRecords, []byte(`{"source_snapshot_id":"source:missing"}`))
	assertToolError(t, err, toolErrorInvalidRecordID)

	_, err = queryServer.CallTool(ctx, ToolListEvidenceRecords, []byte(`{"repository_snapshot_id":"repository:missing"}`))
	assertToolError(t, err, toolErrorInvalidRecordID)

	_, err = queryServer.CallTool(ctx, ToolListEvidenceRecords, []byte(`{"admission_outcome":"proposal_graph"}`))
	assertToolError(t, err, toolErrorInvalidRequest)

	_, err = queryServer.CallTool(ctx, ToolListEvidenceRecords, []byte(`{"limit":101}`))
	assertToolError(t, err, toolErrorInvalidRequest)

	_, err = queryServer.CallTool(ctx, ToolListEvidenceRecords, []byte(`{"source_id":"fixture","graph_layer":"canonical"}`))
	assertToolError(t, err, toolErrorInvalidRequest)
}

type readCounts struct {
	sourceSnapshots     int
	extractionAttempts  int
	proposalOccurrences int
	admissionDecisions  int
	canonicalGraphNodes int
	canonicalGraphEdges int
	sourceGenerations   int
	sourceHeads         int
	activationRequests  int
	reconciliations     int
	reconciliationItems int
	allTablesDigest     string
}

func tableCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) readCounts {
	t.Helper()
	return readCounts{
		sourceSnapshots:     tableCount(t, ctx, pool, "source_snapshots"),
		extractionAttempts:  tableCount(t, ctx, pool, "extraction_attempts"),
		proposalOccurrences: tableCount(t, ctx, pool, "proposal_occurrences"),
		admissionDecisions:  tableCount(t, ctx, pool, "admission_decisions"),
		canonicalGraphNodes: tableCount(t, ctx, pool, "canonical_graph_nodes"),
		canonicalGraphEdges: tableCount(t, ctx, pool, "canonical_graph_edges"),
		sourceGenerations:   tableCount(t, ctx, pool, "repository_source_generations"),
		sourceHeads:         tableCount(t, ctx, pool, "repository_source_heads"),
		activationRequests:  tableCount(t, ctx, pool, "repository_generation_activation_requests"),
		reconciliations:     tableCount(t, ctx, pool, "repository_generation_reconciliations"),
		reconciliationItems: tableCount(t, ctx, pool, "repository_generation_proposal_reconciliations"),
		allTablesDigest:     allTableCountDigest(t, ctx, pool),
	}
}

func allTableCountDigest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_catalog.pg_tables
		WHERE schemaname = current_schema()
		ORDER BY tablename
	`)
	if err != nil {
		t.Fatalf("list domain tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatalf("scan domain table: %v", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate domain tables: %v", err)
	}
	rows.Close()

	var digest strings.Builder
	for _, table := range tables {
		var count int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
			t.Fatalf("count domain table %s: %v", table, err)
		}
		digest.WriteString(table)
		digest.WriteByte('=')
		digest.WriteString(strconv.FormatInt(count, 10))
		digest.WriteByte('\n')
	}
	return digest.String()
}

func tableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return got
}

func callGetEvidenceRecord(t *testing.T, ctx context.Context, server *Server, occurrenceID string) GetEvidenceRecordResponse {
	t.Helper()
	payload, err := json.Marshal(GetEvidenceRecordRequest{ProposalOccurrenceID: occurrenceID})
	if err != nil {
		t.Fatalf("Marshal get request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolGetEvidenceRecord, payload)
	if err != nil {
		t.Fatalf("CallTool(get) error = %v", err)
	}
	var resp GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal get response: %v", err)
	}
	return resp
}

func callGetCanonicalEvidenceRecord(t *testing.T, ctx context.Context, server *Server, canonicalID string) GetEvidenceRecordResponse {
	t.Helper()
	payload, err := json.Marshal(GetEvidenceRecordRequest{CanonicalID: canonicalID})
	if err != nil {
		t.Fatalf("Marshal get canonical request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolGetEvidenceRecord, payload)
	if err != nil {
		t.Fatalf("CallTool(get canonical) error = %v", err)
	}
	var resp GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal get canonical response: %v", err)
	}
	return resp
}

func callListEvidenceRecords(t *testing.T, ctx context.Context, server *Server, req ListEvidenceRecordsRequest) ListEvidenceRecordsResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal list request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolListEvidenceRecords, payload)
	if err != nil {
		t.Fatalf("CallTool(list) error = %v", err)
	}
	var resp ListEvidenceRecordsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal list response: %v", err)
	}
	return resp
}

func callSearchEvidenceRecords(t *testing.T, ctx context.Context, server *Server, req SearchEvidenceRecordsRequest) SearchEvidenceRecordsResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal search request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolSearchEvidenceRecords, payload)
	if err != nil {
		t.Fatalf("CallTool(search) error = %v", err)
	}
	var resp SearchEvidenceRecordsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal search response: %v", err)
	}
	return resp
}

func callGetGroundedEvidenceBrief(t *testing.T, ctx context.Context, server *Server, req GetGroundedEvidenceBriefRequest) GroundedEvidenceBriefResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal grounded evidence brief request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolGetGroundedEvidenceBrief, payload)
	if err != nil {
		t.Fatalf("CallTool(grounded evidence brief) error = %v", err)
	}
	var resp GroundedEvidenceBriefResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal grounded evidence brief response: %v", err)
	}
	return resp
}

func groundedEvidenceBriefMatchIDs(brief GroundedEvidenceBriefResponse) []string {
	ids := make([]string, 0, len(brief.Matches))
	for _, match := range brief.Matches {
		ids = append(ids, match.RecordRef.ID)
	}
	return ids
}

func assertGroundedEvidenceBriefFollowUpResolves(t *testing.T, ctx context.Context, server *Server, followUp GroundedEvidenceBriefFollowUp) {
	t.Helper()
	switch followUp.Tool {
	case ToolGetEvidenceRecord:
		_, err := server.GetEvidenceRecord(ctx, GetEvidenceRecordRequest{
			ProposalOccurrenceID: followUp.ProposalOccurrenceID,
			CanonicalID:          followUp.CanonicalID,
		})
		if err != nil {
			t.Fatalf("grounded evidence brief get follow-up %+v: %v", followUp, err)
		}
	case ToolGetRelationProvenance:
		_, err := server.GetRelationProvenance(ctx, GetRelationProvenanceRequest{
			ProposalOccurrenceID: followUp.ProposalOccurrenceID,
		})
		if err != nil {
			t.Fatalf("grounded evidence brief provenance follow-up %+v: %v", followUp, err)
		}
	case ToolListEvidenceNeighbors:
		_, err := server.ListEvidenceNeighbors(ctx, ListEvidenceNeighborsRequest{
			SymbolRef:          followUp.SymbolRef,
			SourceGenerationID: followUp.SourceGenerationID,
			LifecycleScope:     followUp.LifecycleScope,
		})
		if err != nil {
			t.Fatalf("grounded evidence brief neighbors follow-up %+v: %v", followUp, err)
		}
	default:
		t.Fatalf("unsupported grounded evidence brief follow-up %+v", followUp)
	}
}

func callListEvidenceNeighbors(t *testing.T, ctx context.Context, server *Server, req ListEvidenceNeighborsRequest) ListEvidenceNeighborsResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal neighbors request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolListEvidenceNeighbors, payload)
	if err != nil {
		t.Fatalf("CallTool(neighbors) error = %v", err)
	}
	var resp ListEvidenceNeighborsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal neighbors response: %v", err)
	}
	return resp
}

func callGetRelationProvenance(t *testing.T, ctx context.Context, server *Server, req GetRelationProvenanceRequest) RelationProvenanceResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal relation provenance request: %v", err)
	}
	data, err := server.CallTool(ctx, ToolGetRelationProvenance, payload)
	if err != nil {
		t.Fatalf("CallTool(relation provenance) error = %v", err)
	}
	var resp RelationProvenanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal relation provenance response: %v", err)
	}
	return resp
}

func recordsByID(records []GetEvidenceRecordResponse) map[string]GetEvidenceRecordResponse {
	out := make(map[string]GetEvidenceRecordResponse, len(records))
	for _, record := range records {
		out[record.RecordRef.ID] = record
	}
	return out
}

func assertGenerationResponse(t *testing.T, response ListEvidenceRecordsResponse, generation evidenceingestion.RepositorySourceGeneration, active bool) {
	t.Helper()
	if response.Count != generation.ProposalCount || len(response.Records) != generation.ProposalCount {
		t.Fatalf("generation response count = %d/%d, want %d: %+v", response.Count, len(response.Records), generation.ProposalCount, response)
	}
	for _, record := range response.Records {
		if record.SourceGeneration == nil || record.SourceGeneration.RepositorySourceGeneration != generation || record.SourceGeneration.Active != active {
			t.Fatalf("record %s generation = %+v, want %+v active %t", record.RecordRef.ID, record.SourceGeneration, generation, active)
		}
		if active && (record.RepositoryLifecycle == nil || record.RepositoryLifecycle.SourceGenerationID != generation.ID) {
			t.Fatalf("active record %s lifecycle = %+v, want generation %s", record.RecordRef.ID, record.RepositoryLifecycle, generation.ID)
		}
	}
}

func writeRepositoryFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write repository file %s: %v", path, err)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func callSubmitManualEvidence(t *testing.T, ctx context.Context, server *evidenceingestionmcp.Server, req evidenceingestionmcp.SubmitManualEvidenceRequest) evidenceingestionmcp.SubmitManualEvidenceResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal submit request: %v", err)
	}
	data, err := server.CallTool(ctx, evidenceingestionmcp.ToolSubmitManualEvidence, payload)
	if err != nil {
		t.Fatalf("CallTool(submit) error = %v", err)
	}
	var resp evidenceingestionmcp.SubmitManualEvidenceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal submit response: %v", err)
	}
	return resp
}

func callAdmitPendingProposal(t *testing.T, ctx context.Context, server *evidenceingestionmcp.Server, req evidenceingestionmcp.AdmitPendingProposalRequest) evidenceingestionmcp.AdmitPendingProposalResponse {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal admit request: %v", err)
	}
	data, err := server.CallTool(ctx, evidenceingestionmcp.ToolAdmitPendingProposal, payload)
	if err != nil {
		t.Fatalf("CallTool(admit) error = %v", err)
	}
	var resp evidenceingestionmcp.AdmitPendingProposalResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal admit response: %v", err)
	}
	return resp
}

func integrationSubmitRequest(t *testing.T, requestID string) evidenceingestionmcp.SubmitManualEvidenceRequest {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	raw, err := os.ReadFile(filepath.Join(dir, "..", "evidenceingestion", "testdata", "manual_refund_policy.txt"))
	if err != nil {
		t.Fatalf("read manual fixture: %v", err)
	}
	fixtureData, err := os.ReadFile(filepath.Join(dir, "..", "evidenceingestion", "testdata", "frozen_fixture.json"))
	if err != nil {
		t.Fatalf("read extractor fixture: %v", err)
	}
	var fixture evidenceingestion.FrozenExtractorOutput
	if err := json.Unmarshal(fixtureData, &fixture); err != nil {
		t.Fatalf("parse extractor fixture: %v", err)
	}
	return evidenceingestionmcp.SubmitManualEvidenceRequest{
		SourceID:        "fixture-refund-policy",
		SourceVersion:   "v1",
		RawText:         string(raw),
		OriginMetadata:  map[string]string{"fixture": "manual_refund_policy"},
		RequestID:       requestID,
		ExtractorOutput: fixture,
	}
}

func integrationStatementRequest(
	t *testing.T,
	requestID string,
	sourceID string,
	sourceVersion string,
	statement string,
) evidenceingestionmcp.SubmitManualEvidenceRequest {
	t.Helper()
	request := integrationSubmitRequest(t, requestID)
	request.SourceID = sourceID
	request.SourceVersion = sourceVersion
	request.RawText = statement + "\n"
	request.OriginMetadata = map[string]string{"fixture": requestID}
	request.ExtractorOutput.Proposals[0].StatementText = statement
	return request
}

func integrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_query_mcp_test_" + randomHex(t, 8)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, readMigrations(t)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return ctx, pool
}

func readMigrations(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(paths)
	var combined strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		combined.Write(data)
		combined.WriteString("\n")
	}
	return combined.String()
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}
