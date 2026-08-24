package evidencequerymcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestToolsExposeReadOnlyQueryTools(t *testing.T) {
	server := newServer(&fakeQueryCore{})
	tools := server.Tools()
	if len(tools) != 12 {
		t.Fatalf("len(Tools()) = %d, want 12", len(tools))
	}
	if tools[0].Name != ToolGetEvidenceRecord {
		t.Fatalf("tool name = %q, want %q", tools[0].Name, ToolGetEvidenceRecord)
	}
	if tools[1].Name != ToolListEvidenceRecords {
		t.Fatalf("tool name = %q, want %q", tools[1].Name, ToolListEvidenceRecords)
	}
	wantNames := []string{
		ToolGetEvidenceRecord,
		ToolListEvidenceRecords,
		ToolSearchEvidenceRecords,
		ToolGetGroundedEvidenceBrief,
		ToolListEvidenceNeighbors,
		ToolGetRelationProvenance,
		ToolGetMCPReadSourceStates,
		ToolOpenCanonicalReadView,
		ToolFindCanonicalPath,
		ToolGetCanonicalTopologyDiagnostics,
		ToolGetCanonicalContradictionProposal,
		ToolGetCanonicalSupersessionProposal,
	}
	for i, want := range wantNames {
		if tools[i].Name != want {
			t.Fatalf("tool[%d] name = %q, want %q", i, tools[i].Name, want)
		}
	}
	for _, tool := range tools {
		if !tool.ReadOnly {
			t.Fatalf("%s ReadOnly = false, want true", tool.Name)
		}
	}
}

func TestGetMCPReadSourceStatesDelegatesBoundedRequest(t *testing.T) {
	core := &fakeQueryCore{
		sourceStateResult: evidenceingestion.MCPReadSourceStateQueryResult{
			SchemaVersion: evidenceingestion.MCPReadSourceStateQuerySchemaV1,
			Mode:          evidenceingestion.MCPReadSourceStateModeLatestObserved,
		},
	}
	server := newServer(core)
	sourceBindingID := "workspace-source:" + strings.Repeat("a", 64)
	data, err := server.CallTool(
		context.Background(),
		ToolGetMCPReadSourceStates,
		[]byte(`{"source_binding_id":"`+sourceBindingID+`","mode":"latest_observed"}`),
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetMCPReadSourceStatesResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode source-state response: %v", err)
	}
	if response.SchemaVersion != evidenceingestion.MCPReadSourceStateQuerySchemaV1 ||
		core.sourceStateInput.SourceBindingID != sourceBindingID ||
		core.sourceStateInput.Mode != evidenceingestion.MCPReadSourceStateModeLatestObserved {
		t.Fatalf("source-state response/input = %+v / %+v", response, core.sourceStateInput)
	}
}

func TestMapSourceInfoExposesTypedMCPReadProvenance(t *testing.T) {
	source := mapSourceInfo(evidenceingestion.ProposalQueryResult{
		SourceBindingKind: evidenceingestion.ProposalSourceBindingSourceSnapshot,
		SourceSnapshotID:  "srcsnap:test",
		SourceSystem:      evidenceingestion.SourceSystemMCPReadDocument,
		SourceID:          "mcp-read-source:test",
		SourceVersion:     "revision-42",
		RawContentHash:    "sha256:document",
		OriginMetadata: map[string]string{
			"connector_delivery_id":            "detective-connector-delivery:test",
			"mcp_provider":                     "atlassian",
			"mcp_object_id":                    "AHE-42",
			"mcp_revision":                     "revision-42",
			"mcp_document_id":                  "AHE-42-description",
			"mcp_source_location":              "https://fixture.invalid/AHE-42",
			"mcp_source_identity_contract":     "mcp-read-source-v2",
			"mcp_proposal_candidate_contract":  "ahe-mcp-proposal-candidates-v1",
			"mcp_coverage_complete":            "true",
			"mcp_coverage_truncated":           "false",
			"mcp_coverage_completion_reason":   "complete",
			"mcp_limitations_json":             `["description field only"]`,
			"mcp_proposal_candidates_json":     `[{"local_id":"jira-description","selector_kind":"json_pointer_string","selector":"/description"}]`,
			"global_absence_inference_allowed": "false",
		},
	})
	if source.MCPRead == nil ||
		source.MCPRead.Provider != "atlassian" ||
		source.MCPRead.ObjectID != "AHE-42" ||
		source.MCPRead.Revision != "revision-42" ||
		source.MCPRead.SourceIdentityContract != "mcp-read-source-v2" ||
		source.MCPRead.ProposalCandidateContract != "ahe-mcp-proposal-candidates-v1" ||
		!source.MCPRead.CoverageComplete ||
		source.MCPRead.CoverageTruncated ||
		len(source.MCPRead.Limitations) != 1 ||
		len(source.MCPRead.ProposalCandidates) != 1 ||
		source.MCPRead.ProposalCandidates[0].LocalID != "jira-description" ||
		source.MCPRead.GlobalAbsenceInferenceAllowed {
		t.Fatalf("typed MCP source provenance = %+v", source.MCPRead)
	}
}

func TestMapSourceInfoExposesTypedExternalSourceProvenance(t *testing.T) {
	source := mapSourceInfo(evidenceingestion.ProposalQueryResult{
		SourceBindingKind: evidenceingestion.ProposalSourceBindingSourceSnapshot,
		SourceSnapshotID:  "srcsnap:test",
		SourceSystem:      evidenceingestion.SourceSystemExternalDocument,
		SourceID:          "extsrc:test",
		SourceVersion:     "revision-42",
		RawContentHash:    "sha256:document",
		OriginMetadata: map[string]string{
			"external_source_schema":           evidenceingestion.ExternalSourceEnvelopeSchemaV1,
			"external_source_system":           "jira",
			"external_source_namespace":        "acme/eng",
			"external_object_type":             "issue",
			"external_object_id":               "AHE-42",
			"external_revision":                "revision-42",
			"external_source_location":         "https://fixture.invalid/AHE-42",
			"external_title":                   "Agent-first source intake",
			"external_content_format":          evidenceingestion.ExternalSourceContentFormatMarkdown,
			"external_content_fidelity":        evidenceingestion.ExternalSourceContentFidelityVerbatim,
			"external_coverage":                evidenceingestion.ExternalSourceCoverageExactExcerpt,
			"external_limitations_json":        `["comments were not requested"]`,
			"external_source_created_at":       "2026-08-22T01:00:00Z",
			"external_source_updated_at":       "2026-08-23T02:00:00Z",
			"global_absence_inference_allowed": "false",
		},
	})

	if source.ExternalSource == nil ||
		source.ExternalSource.SchemaVersion != evidenceingestion.ExternalSourceEnvelopeSchemaV1 ||
		source.ExternalSource.SourceSystem != "jira" ||
		source.ExternalSource.SourceNamespace != "acme/eng" ||
		source.ExternalSource.ObjectType != "issue" ||
		source.ExternalSource.ObjectID != "AHE-42" ||
		source.ExternalSource.Revision != "revision-42" ||
		source.ExternalSource.SourceLocation != "https://fixture.invalid/AHE-42" ||
		source.ExternalSource.Title != "Agent-first source intake" ||
		source.ExternalSource.Coverage != evidenceingestion.ExternalSourceCoverageExactExcerpt ||
		len(source.ExternalSource.Limitations) != 1 ||
		source.ExternalSource.Limitations[0] != "comments were not requested" ||
		source.ExternalSource.GlobalAbsenceInferenceAllowed {
		t.Fatalf("typed external source provenance = %+v", source.ExternalSource)
	}
}

func TestCallToolGetsDeterministicGroundedEvidenceBrief(t *testing.T) {
	record := testRepositoryRelationResult("occ:brief")
	coverage := evidenceingestion.RepositoryGoplsCoverage{
		SchemaVersion:           evidenceingestion.RepositoryGoplsCoverageSchemaV8,
		Scope:                   "selected_repository_go_files",
		RequestCoverageComplete: true,
		SelectedFileCount:       2,
	}
	execution := testEvidenceQueryExecution(
		"worker Run",
		evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
		10,
		evidenceingestion.ProposalLifecycleScopeActive,
		true,
		evidenceingestion.EvidenceQueryCompletionMorphologyCandidates,
	)
	core := &fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches: []evidenceingestion.ProposalSearchResult{{
				Record: record,
				Rank:   0.75,
			}},
			Coverage: []evidenceingestion.GroundedEvidenceBriefCoverage{{
				ExtractionAttemptID:     record.ExtractionAttemptID,
				RepositoryGoplsCoverage: coverage,
			}},
			Execution: execution,
		},
	}
	server := newServer(core)
	payload := []byte(`{"query":" worker Run ","query_mode":"deterministic_lexical_recovery","lifecycle_scope":"active","limit":10}`)

	first, err := server.CallTool(context.Background(), ToolGetGroundedEvidenceBrief, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	second, err := server.CallTool(context.Background(), ToolGetGroundedEvidenceBrief, payload)
	if err != nil {
		t.Fatalf("CallTool() replay error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("brief replay differs:\nfirst  %s\nsecond %s", first, second)
	}
	explicitV2, err := server.CallTool(
		context.Background(),
		ToolGetGroundedEvidenceBrief,
		[]byte(`{"query":" worker Run ","query_mode":"deterministic_lexical_recovery","response_schema":"grounded-evidence-brief-v2","lifecycle_scope":"active","limit":10}`),
	)
	if err != nil {
		t.Fatalf("CallTool() explicit v2 error = %v", err)
	}
	if !bytes.Equal(first, explicitV2) {
		t.Fatalf("default and explicit v2 differ:\ndefault  %s\nexplicit %s", first, explicitV2)
	}
	for _, forbidden := range [][]byte{[]byte(`"answer"`), []byte(`"conclusion"`), []byte(`"recommendation"`)} {
		if bytes.Contains(first, forbidden) {
			t.Fatalf("brief contains forbidden generated field %s: %s", forbidden, first)
		}
	}

	var response GroundedEvidenceBriefResponse
	if err := json.Unmarshal(first, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.briefInput.Query != "worker Run" ||
		core.briefInput.QueryMode != evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery ||
		core.briefInput.IncludeSourceContext ||
		core.briefInput.Limit != 10 ||
		core.briefInput.LifecycleScope != evidenceingestion.ProposalLifecycleScopeActive {
		t.Fatalf("brief input = %+v", core.briefInput)
	}
	if response.SchemaVersion != groundedEvidenceBriefSchemaV2 || response.Query != "worker Run" {
		t.Fatalf("brief identity = %+v", response)
	}
	if response.Boundary.SearchSurface != evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement || !response.Boundary.Truncated {
		t.Fatalf("brief boundary = %+v", response.Boundary)
	}
	if response.QueryExecution.PlanVersion != evidenceingestion.EvidenceQueryPlanRecoveryV2 ||
		response.QueryExecution.QueryCount != 2 ||
		response.QueryExecution.GlobalAbsenceInferenceAllowed {
		t.Fatalf("brief query execution = %+v", response.QueryExecution)
	}
	if len(response.Matches) != 1 || response.Matches[0].RecordRef.ID != record.ProposalOccurrenceID || response.Matches[0].StatementText != record.StatementText {
		t.Fatalf("brief matches = %+v", response.Matches)
	}
	if response.Counts.ReturnedMatches != 1 || response.Counts.Admission.Pending != 1 || response.Counts.Lifecycle.RepositoryActive != 1 {
		t.Fatalf("brief counts = %+v", response.Counts)
	}
	if len(response.SourceScopes) != 1 || response.SourceScopes[0].ScopeRef.ID != record.SourceGeneration.ID {
		t.Fatalf("brief source scopes = %+v", response.SourceScopes)
	}
	if len(response.Coverage) != 1 || response.Coverage[0].Diagnostic.SchemaVersion != coverage.SchemaVersion || response.Coverage[0].SourceGenerationRef == nil {
		t.Fatalf("brief coverage = %+v", response.Coverage)
	}
	if len(response.ObservationCodes) != 2 ||
		response.ObservationCodes[0] != "morphology_candidates_found" ||
		response.ObservationCodes[1] != "bounded_result_truncated" {
		t.Fatalf("brief observations = %+v", response.ObservationCodes)
	}
	assertBriefFollowUp(t, response.FollowUps, GroundedEvidenceBriefFollowUp{
		Tool:                 ToolGetEvidenceRecord,
		ProposalOccurrenceID: record.ProposalOccurrenceID,
	})
	assertBriefFollowUp(t, response.FollowUps, GroundedEvidenceBriefFollowUp{
		Tool:                 ToolGetRelationProvenance,
		ProposalOccurrenceID: record.ProposalOccurrenceID,
	})
	assertBriefFollowUp(t, response.FollowUps, GroundedEvidenceBriefFollowUp{
		Tool:               ToolListEvidenceNeighbors,
		SymbolRef:          record.CodeRelation.Target.SymbolRef,
		SourceGenerationID: record.SourceGeneration.ID,
		LifecycleScope:     evidenceingestion.ProposalLifecycleScopeAll,
	})
}

func TestCallToolGetsOptInGroundedEvidenceBriefSourceContext(t *testing.T) {
	record := testQueryResult("occ:brief-context")
	sourceContext := testAvailableGroundedEvidenceSourceContext(record)
	core := &fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches: []evidenceingestion.ProposalSearchResult{{
				Record: record,
				Rank:   0.5,
			}},
			Execution: testEvidenceQueryExecution(
				"refund complete",
				evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
				10,
				evidenceingestion.ProposalLifecycleScopeActive,
				false,
				evidenceingestion.EvidenceQueryCompletionMorphologyCandidates,
			),
			SourceContexts: []evidenceingestion.GroundedEvidenceSourceContext{
				sourceContext,
			},
		},
	}
	server := newServer(core)

	v2, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query: "refund complete",
			Limit: 10,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v2) error = %v", err)
	}
	if core.briefInput.IncludeSourceContext ||
		core.briefInput.IncludeRepositoryContext {
		t.Fatalf("v2 brief input requests context: %+v", core.briefInput)
	}
	v3, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "refund complete",
			ResponseSchema: GroundedEvidenceBriefSchemaV3,
			Limit:          10,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v3) error = %v", err)
	}
	if !core.briefInput.IncludeSourceContext ||
		core.briefInput.IncludeRepositoryContext {
		t.Fatalf("v3 brief input = %+v", core.briefInput)
	}
	v4, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "refund complete",
			ResponseSchema: GroundedEvidenceBriefSchemaV4,
			Limit:          10,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v4) error = %v", err)
	}
	if !core.briefInput.IncludeSourceContext ||
		!core.briefInput.IncludeRepositoryContext {
		t.Fatalf("v4 brief input = %+v", core.briefInput)
	}
	if v2.SchemaVersion != GroundedEvidenceBriefSchemaV2 ||
		v2.Matches[0].SourceContext != nil ||
		v2.Matches[0].RepositoryContext != nil {
		t.Fatalf("v2 response = %+v", v2)
	}
	if v3.SchemaVersion != GroundedEvidenceBriefSchemaV3 ||
		len(v3.Matches) != 1 ||
		v3.Matches[0].SourceContext == nil ||
		v3.Matches[0].SourceContext.Status !=
			evidenceingestion.GroundedEvidenceSourceContextStatusAvailable ||
		v3.Matches[0].SourceContext.SearchParticipation {
		t.Fatalf("v3 response = %+v", v3)
	}
	if v4.SchemaVersion != GroundedEvidenceBriefSchemaV4 ||
		len(v4.Matches) != 1 ||
		v4.Matches[0].SourceContext == nil ||
		v4.Matches[0].RepositoryContext != nil ||
		v4.Matches[0].SourceContext.Status !=
			evidenceingestion.GroundedEvidenceSourceContextStatusAvailable {
		t.Fatalf("v4 manual-source response = %+v", v4)
	}

	projectedV3 := v3
	projectedV3.SchemaVersion = GroundedEvidenceBriefSchemaV2
	projectedV3.Matches = append(
		[]GroundedEvidenceBriefMatch(nil),
		v3.Matches...,
	)
	projectedV3.Matches[0].SourceContext = nil
	v2JSON, err := json.Marshal(v2)
	if err != nil {
		t.Fatalf("Marshal(v2): %v", err)
	}
	projectedV3JSON, err := json.Marshal(projectedV3)
	if err != nil {
		t.Fatalf("Marshal(projected v3): %v", err)
	}
	if !bytes.Equal(v2JSON, projectedV3JSON) {
		t.Fatalf(
			"v3 changes the v2 base projection:\nv2 %s\nv3 %s",
			v2JSON,
			projectedV3JSON,
		)
	}

	projectedV4 := v4
	projectedV4.SchemaVersion = GroundedEvidenceBriefSchemaV3
	v3JSON, err := json.Marshal(v3)
	if err != nil {
		t.Fatalf("Marshal(v3): %v", err)
	}
	projectedV4JSON, err := json.Marshal(projectedV4)
	if err != nil {
		t.Fatalf("Marshal(projected v4): %v", err)
	}
	if !bytes.Equal(v3JSON, projectedV4JSON) {
		t.Fatalf(
			"v4 changes the v3 manual-source projection:\nv3 %s\nv4 %s",
			v3JSON,
			projectedV4JSON,
		)
	}
}

func TestCallToolGetsOptInGroundedEvidenceBriefCompactRecordState(t *testing.T) {
	sourceRecord := testQueryResult("occ:brief-record-state-source")
	repositoryRecord := testRepositoryQueryResult("occ:brief-record-state-repository")
	repositoryRecord.SourceGenerationActive = false
	core := &fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches: []evidenceingestion.ProposalSearchResult{
				{Record: sourceRecord, Rank: 0.75},
				{Record: repositoryRecord, Rank: 0.5},
			},
			Execution: testEvidenceQueryExecution(
				"refund complete",
				evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
				10,
				evidenceingestion.ProposalLifecycleScopeAll,
				false,
				evidenceingestion.EvidenceQueryCompletionMorphologyCandidates,
			),
		},
	}
	server := newServer(core)

	v2, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "refund complete",
			ResponseSchema: GroundedEvidenceBriefSchemaV2,
			LifecycleScope: evidenceingestion.ProposalLifecycleScopeAll,
			Limit:          10,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v2) error = %v", err)
	}
	v5, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "refund complete",
			ResponseSchema: GroundedEvidenceBriefSchemaV5,
			LifecycleScope: evidenceingestion.ProposalLifecycleScopeAll,
			Limit:          10,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v5) error = %v", err)
	}
	if core.briefInput.IncludeSourceContext ||
		core.briefInput.IncludeRepositoryContext {
		t.Fatalf("v5 brief input requests hydrated context: %+v", core.briefInput)
	}
	if v5.SchemaVersion != GroundedEvidenceBriefSchemaV5 ||
		len(v5.Matches) != 2 ||
		v5.Matches[0].SourceContext != nil ||
		v5.Matches[0].RepositoryContext != nil ||
		v5.Matches[1].SourceContext != nil ||
		v5.Matches[1].RepositoryContext != nil {
		t.Fatalf("v5 response = %+v", v5)
	}
	sourceState := v5.Matches[0].RecordState
	if sourceState == nil ||
		sourceState.AuthorityStatus != groundedEvidenceAuthorityProposalPending ||
		sourceState.RecordLifecycle != groundedEvidenceRecordLifecycleSourceSnapshot ||
		sourceState.SourceBindingKind != evidenceingestion.ProposalSourceBindingSourceSnapshot ||
		sourceState.RevisionKind != groundedEvidenceRevisionSourceVersion ||
		sourceState.Revision != sourceRecord.SourceVersion ||
		sourceState.ExternalFreshnessStatus != groundedEvidenceExternalFreshnessNotEvaluated ||
		sourceState.ExternalFreshnessBasis != groundedEvidenceExternalFreshnessNoComparison {
		t.Fatalf("v5 source state = %+v", sourceState)
	}
	repositoryState := v5.Matches[1].RecordState
	if repositoryState == nil ||
		repositoryState.AuthorityStatus != groundedEvidenceAuthorityProposalPending ||
		repositoryState.RecordLifecycle != groundedEvidenceRecordLifecycleRepositoryHistorical ||
		repositoryState.SourceBindingKind != evidenceingestion.ProposalSourceBindingRepositorySnapshot ||
		repositoryState.RevisionKind != groundedEvidenceRevisionGitCommit ||
		repositoryState.Revision != repositoryRecord.RepositorySnapshot.CommitSHA ||
		repositoryState.ExternalFreshnessStatus != groundedEvidenceExternalFreshnessNotEvaluated {
		t.Fatalf("v5 repository state = %+v", repositoryState)
	}
	if !slices.Contains(v5.Limitations, "external_source_freshness_not_evaluated") {
		t.Fatalf("v5 limitations = %+v", v5.Limitations)
	}

	projectedV5 := v5
	projectedV5.SchemaVersion = GroundedEvidenceBriefSchemaV2
	projectedV5.Matches = append(
		[]GroundedEvidenceBriefMatch(nil),
		v5.Matches...,
	)
	for index := range projectedV5.Matches {
		projectedV5.Matches[index].RecordState = nil
	}
	projectedV5.Limitations = slices.DeleteFunc(
		append([]string(nil), v5.Limitations...),
		func(item string) bool {
			return item == "external_source_freshness_not_evaluated"
		},
	)
	if !reflect.DeepEqual(v2, projectedV5) {
		t.Fatalf(
			"v5 changes the v2 base projection:\nv2 %+v\nv5 %+v",
			v2,
			projectedV5,
		)
	}
}

func TestGroundedEvidenceBriefRecordState(t *testing.T) {
	admitted := testQueryResult("occ:record-state-admitted")
	admitted.AdmissionOutcome = "admitted"
	admitted.CanonicalRef = "canon-node:admitted"
	rejected := testQueryResult("occ:record-state-rejected")
	rejected.AdmissionOutcome = "rejected"
	auditOnly := testQueryResult("occ:record-state-audit-only")
	auditOnly.AdmissionOutcome = "audit_only"
	repositoryActive := testRepositoryQueryResult("occ:record-state-repository-active")
	repositoryHistorical := testRepositoryQueryResult("occ:record-state-repository-historical")
	repositoryHistorical.SourceGenerationActive = false

	for _, tc := range []struct {
		name      string
		record    evidenceingestion.ProposalQueryResult
		authority string
		lifecycle string
	}{
		{
			name:      "pending source snapshot",
			record:    testQueryResult("occ:record-state-pending"),
			authority: groundedEvidenceAuthorityProposalPending,
			lifecycle: groundedEvidenceRecordLifecycleSourceSnapshot,
		},
		{
			name:      "admitted source snapshot",
			record:    admitted,
			authority: groundedEvidenceAuthorityCanonicalAdmitted,
			lifecycle: groundedEvidenceRecordLifecycleSourceSnapshot,
		},
		{
			name:      "rejected source snapshot",
			record:    rejected,
			authority: groundedEvidenceAuthorityProposalRejected,
			lifecycle: groundedEvidenceRecordLifecycleSourceSnapshot,
		},
		{
			name:      "audit-only source snapshot",
			record:    auditOnly,
			authority: groundedEvidenceAuthorityProposalAuditOnly,
			lifecycle: groundedEvidenceRecordLifecycleSourceSnapshot,
		},
		{
			name:      "active repository",
			record:    repositoryActive,
			authority: groundedEvidenceAuthorityProposalPending,
			lifecycle: groundedEvidenceRecordLifecycleRepositoryActive,
		},
		{
			name:      "historical repository",
			record:    repositoryHistorical,
			authority: groundedEvidenceAuthorityProposalPending,
			lifecycle: groundedEvidenceRecordLifecycleRepositoryHistorical,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := groundedEvidenceBriefRecordState(tc.record)
			if err != nil {
				t.Fatalf("groundedEvidenceBriefRecordState() error = %v", err)
			}
			if state.AuthorityStatus != tc.authority ||
				state.RecordLifecycle != tc.lifecycle ||
				state.ExternalFreshnessStatus !=
					groundedEvidenceExternalFreshnessNotEvaluated {
				t.Fatalf("record state = %+v", state)
			}
		})
	}
}

func TestGroundedEvidenceBriefRecordStateRejectsContradictions(t *testing.T) {
	nonAdmittedCanonical := testQueryResult("occ:record-state-pending-canonical")
	nonAdmittedCanonical.CanonicalRef = "canon-node:invalid"
	admittedWithoutCanonical := testQueryResult("occ:record-state-admitted-no-canonical")
	admittedWithoutCanonical.AdmissionOutcome = "admitted"
	badBinding := testQueryResult("occ:record-state-bad-binding")
	badBinding.SourceBindingKind = "external_index"
	incompleteRepository := testRepositoryQueryResult("occ:record-state-incomplete-repository")
	incompleteRepository.SourceGeneration = nil
	mismatchedRepository := testRepositoryQueryResult("occ:record-state-mismatched-repository")
	mismatchedRepository.SourceGeneration.CommitSHA = "different-commit"

	for _, tc := range []struct {
		name   string
		record evidenceingestion.ProposalQueryResult
	}{
		{name: "pending with canonical", record: nonAdmittedCanonical},
		{name: "admitted without canonical", record: admittedWithoutCanonical},
		{name: "unsupported binding", record: badBinding},
		{name: "repository without generation", record: incompleteRepository},
		{name: "repository generation mismatch", record: mismatchedRepository},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := groundedEvidenceBriefRecordState(tc.record); err == nil {
				t.Fatal("groundedEvidenceBriefRecordState() error = nil")
			}
		})
	}
}

func TestCallToolGetsOptInGroundedEvidenceBriefRepositoryContext(t *testing.T) {
	record := testRepositoryDeclarationResult("occ:brief-repository-context")
	sourceContext := testNotApplicableGroundedEvidenceSourceContext(record)
	repositoryContext := testAvailableGroundedEvidenceRepositoryContext(record)
	core := &fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches: []evidenceingestion.ProposalSearchResult{{
				Record: record,
				Rank:   0.5,
			}},
			Execution: testEvidenceQueryExecution(
				"Build",
				evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
				10,
				evidenceingestion.ProposalLifecycleScopeActive,
				false,
				evidenceingestion.EvidenceQueryCompletionExactCandidates,
			),
			SourceContexts: []evidenceingestion.GroundedEvidenceSourceContext{
				sourceContext,
			},
			RepositoryContexts: []evidenceingestion.GroundedEvidenceRepositoryContext{
				repositoryContext,
			},
		},
	}
	server := newServer(core)

	v2, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{Query: "Build"},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v2) error = %v", err)
	}
	v3, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "Build",
			ResponseSchema: GroundedEvidenceBriefSchemaV3,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v3) error = %v", err)
	}
	v4, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "Build",
			ResponseSchema: GroundedEvidenceBriefSchemaV4,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v4) error = %v", err)
	}
	if !core.briefInput.IncludeSourceContext ||
		!core.briefInput.IncludeRepositoryContext {
		t.Fatalf("v4 brief input = %+v", core.briefInput)
	}
	if v2.Matches[0].SourceContext != nil ||
		v2.Matches[0].RepositoryContext != nil {
		t.Fatalf("v2 repository response = %+v", v2)
	}
	if v3.Matches[0].SourceContext == nil ||
		v3.Matches[0].SourceContext.Status !=
			evidenceingestion.GroundedEvidenceSourceContextStatusNotApplicable ||
		v3.Matches[0].RepositoryContext != nil {
		t.Fatalf("v3 repository response = %+v", v3)
	}
	if v4.SchemaVersion != GroundedEvidenceBriefSchemaV4 ||
		v4.Matches[0].SourceContext != nil ||
		v4.Matches[0].RepositoryContext == nil ||
		v4.Matches[0].RepositoryContext.Status !=
			evidenceingestion.GroundedEvidenceRepositoryContextStatusAvailable ||
		v4.Matches[0].RepositoryContext.Declaration == nil ||
		v4.Matches[0].RepositoryContext.Relation != nil {
		t.Fatalf("v4 repository response = %+v", v4)
	}

	projectedV4 := v4
	projectedV4.SchemaVersion = GroundedEvidenceBriefSchemaV2
	projectedV4.Matches[0].RepositoryContext = nil
	v2JSON, err := json.Marshal(v2)
	if err != nil {
		t.Fatalf("Marshal(v2): %v", err)
	}
	projectedV4JSON, err := json.Marshal(projectedV4)
	if err != nil {
		t.Fatalf("Marshal(projected v4): %v", err)
	}
	if !bytes.Equal(v2JSON, projectedV4JSON) {
		t.Fatalf(
			"v4 changes the v2 repository base projection:\nv2 %s\nv4 %s",
			v2JSON,
			projectedV4JSON,
		)
	}
}

func TestGroundedEvidenceBriefV4ReportsAmbiguousRepositoryCoverage(t *testing.T) {
	record := testRepositoryDeclarationResult("occ:brief-repository-ambiguity")
	server := newServer(&fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches: []evidenceingestion.ProposalSearchResult{{
				Record: record,
				Rank:   0.5,
			}},
			Coverage: []evidenceingestion.GroundedEvidenceBriefCoverage{{
				ExtractionAttemptID: record.ExtractionAttemptID,
				RepositoryGoplsCoverage: evidenceingestion.RepositoryGoplsCoverage{
					SchemaVersion:                 evidenceingestion.RepositoryGoplsCoverageSchemaV8,
					DefinitionAmbiguousUsageCount: 1,
					ReferenceAmbiguousUsageCount:  2,
				},
			}},
			Execution: testEvidenceQueryExecution(
				"Build",
				evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
				10,
				evidenceingestion.ProposalLifecycleScopeActive,
				false,
				evidenceingestion.EvidenceQueryCompletionExactCandidates,
			),
			SourceContexts: []evidenceingestion.GroundedEvidenceSourceContext{
				testNotApplicableGroundedEvidenceSourceContext(record),
			},
			RepositoryContexts: []evidenceingestion.GroundedEvidenceRepositoryContext{
				testAvailableGroundedEvidenceRepositoryContext(record),
			},
		},
	})

	response, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "Build",
			ResponseSchema: GroundedEvidenceBriefSchemaV4,
		},
	)
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief(v4) error = %v", err)
	}
	if !slices.Equal(response.ObservationCodes, []string{
		"repository_definition_targets_ambiguous_and_withheld",
		"repository_reference_targets_ambiguous_and_withheld",
	}) {
		t.Fatalf("v4 ambiguity observations = %+v", response.ObservationCodes)
	}
}

func TestGroundedEvidenceBriefV4FailsClosedOnRepositoryContextMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(
			*evidenceingestion.GroundedEvidenceBriefQueryResult,
		)
	}{
		{
			name: "missing repository context",
			mutate: func(result *evidenceingestion.GroundedEvidenceBriefQueryResult) {
				result.RepositoryContexts = nil
			},
		},
		{
			name: "repository search core drift",
			mutate: func(result *evidenceingestion.GroundedEvidenceBriefQueryResult) {
				result.RepositoryContexts[0].SearchCore[0].QuotedText = "drifted"
			},
		},
		{
			name: "repository record kind drift",
			mutate: func(result *evidenceingestion.GroundedEvidenceBriefQueryResult) {
				result.RepositoryContexts[0].RecordKind =
					evidenceingestion.GroundedEvidenceRepositoryRecordRelation
			},
		},
		{
			name: "repository slice hash drift",
			mutate: func(result *evidenceingestion.GroundedEvidenceBriefQueryResult) {
				result.RepositoryContexts[0].Declaration.AtomicContainer.ExactText =
					"func Other() {}"
			},
		},
		{
			name: "repository referenced bytes drift",
			mutate: func(result *evidenceingestion.GroundedEvidenceBriefQueryResult) {
				result.RepositoryContexts[0].ReferencedBytes++
			},
		},
		{
			name: "repository available payload missing",
			mutate: func(result *evidenceingestion.GroundedEvidenceBriefQueryResult) {
				result.RepositoryContexts[0].Declaration = nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := testRepositoryDeclarationResult(
				"occ:brief-repository-context-mismatch",
			)
			result := evidenceingestion.GroundedEvidenceBriefQueryResult{
				Matches: []evidenceingestion.ProposalSearchResult{{
					Record: record,
					Rank:   0.5,
				}},
				Execution: testEvidenceQueryExecution(
					"Build",
					evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
					10,
					evidenceingestion.ProposalLifecycleScopeActive,
					false,
					evidenceingestion.EvidenceQueryCompletionExactCandidates,
				),
				SourceContexts: []evidenceingestion.GroundedEvidenceSourceContext{
					testNotApplicableGroundedEvidenceSourceContext(record),
				},
				RepositoryContexts: []evidenceingestion.GroundedEvidenceRepositoryContext{
					testAvailableGroundedEvidenceRepositoryContext(record),
				},
			}
			test.mutate(&result)
			server := newServer(&fakeQueryCore{briefResult: result})

			_, err := server.GetGroundedEvidenceBrief(
				context.Background(),
				GetGroundedEvidenceBriefRequest{
					Query:          "Build",
					ResponseSchema: GroundedEvidenceBriefSchemaV4,
				},
			)
			assertToolError(t, err, toolErrorInternal)
		})
	}
}

func TestGroundedEvidenceBriefV4RejectsReorderedRelationEndpoints(t *testing.T) {
	record := testRepositoryRelationContextResult(
		"occ:brief-repository-relation",
	)
	repositoryContext := testAvailableGroundedEvidenceRepositoryRelationContext(
		record,
	)
	repositoryContext.Relation.Endpoints[0],
		repositoryContext.Relation.Endpoints[1] =
		repositoryContext.Relation.Endpoints[1],
		repositoryContext.Relation.Endpoints[0]
	server := newServer(&fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches: []evidenceingestion.ProposalSearchResult{{
				Record: record,
				Rank:   0.5,
			}},
			Execution: testEvidenceQueryExecution(
				"Build",
				evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
				10,
				evidenceingestion.ProposalLifecycleScopeActive,
				false,
				evidenceingestion.EvidenceQueryCompletionExactCandidates,
			),
			SourceContexts: []evidenceingestion.GroundedEvidenceSourceContext{
				testNotApplicableGroundedEvidenceSourceContext(record),
			},
			RepositoryContexts: []evidenceingestion.GroundedEvidenceRepositoryContext{
				repositoryContext,
			},
		},
	})

	_, err := server.GetGroundedEvidenceBrief(
		context.Background(),
		GetGroundedEvidenceBriefRequest{
			Query:          "Build",
			ResponseSchema: GroundedEvidenceBriefSchemaV4,
		},
	)
	assertToolError(t, err, toolErrorInternal)
}

func TestGroundedEvidenceBriefV3FailsClosedOnSourceContextMismatch(t *testing.T) {
	record := testQueryResult("occ:brief-context-mismatch")
	execution := testEvidenceQueryExecution(
		"refund",
		evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
		10,
		evidenceingestion.ProposalLifecycleScopeActive,
		false,
		evidenceingestion.EvidenceQueryCompletionExactCandidates,
	)
	tests := []struct {
		name     string
		contexts []evidenceingestion.GroundedEvidenceSourceContext
	}{
		{name: "missing"},
		{
			name: "search core drift",
			contexts: []evidenceingestion.GroundedEvidenceSourceContext{
				func() evidenceingestion.GroundedEvidenceSourceContext {
					sourceContext := testAvailableGroundedEvidenceSourceContext(record)
					sourceContext.SearchCore[0].QuotedText = "drifted"
					return sourceContext
				}(),
			},
		},
		{
			name: "available with over-budget view",
			contexts: []evidenceingestion.GroundedEvidenceSourceContext{
				func() evidenceingestion.GroundedEvidenceSourceContext {
					sourceContext := testAvailableGroundedEvidenceSourceContext(record)
					sourceContext.SourceView.Status =
						evidenceingestion.BoundedSourceViewStatusOverBudget
					sourceContext.SourceView.LimitReason =
						evidenceingestion.BoundedSourceViewLimitAtomicSpan
					return sourceContext
				}(),
			},
		},
		{
			name: "over-budget without view observation",
			contexts: []evidenceingestion.GroundedEvidenceSourceContext{
				func() evidenceingestion.GroundedEvidenceSourceContext {
					sourceContext := testAvailableGroundedEvidenceSourceContext(record)
					sourceContext.Status =
						evidenceingestion.GroundedEvidenceSourceContextStatusSourceViewOverBudget
					sourceContext.SourceView = nil
					sourceContext.AtomicContainer = nil
					sourceContext.ContextEnvelope = nil
					return sourceContext
				}(),
			},
		},
		{
			name: "source view identity drift",
			contexts: []evidenceingestion.GroundedEvidenceSourceContext{
				func() evidenceingestion.GroundedEvidenceSourceContext {
					sourceContext := testAvailableGroundedEvidenceSourceContext(record)
					sourceContext.SourceView.Observation.ExtractionViewID = "view:drifted"
					return sourceContext
				}(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newServer(&fakeQueryCore{
				briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
					Matches: []evidenceingestion.ProposalSearchResult{{
						Record: record,
						Rank:   0.5,
					}},
					Execution:      execution,
					SourceContexts: tt.contexts,
				},
			})
			_, err := server.GetGroundedEvidenceBrief(
				context.Background(),
				GetGroundedEvidenceBriefRequest{
					Query:          "refund",
					ResponseSchema: GroundedEvidenceBriefSchemaV3,
				},
			)
			assertToolError(t, err, toolErrorInternal)
		})
	}
}

func TestGroundedEvidenceBriefClosesNoMatchObservation(t *testing.T) {
	core := &fakeQueryCore{
		briefResult: evidenceingestion.GroundedEvidenceBriefQueryResult{
			Matches:  []evidenceingestion.ProposalSearchResult{},
			Coverage: []evidenceingestion.GroundedEvidenceBriefCoverage{},
			Execution: testEvidenceQueryExecution(
				"not persisted",
				evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery,
				20,
				evidenceingestion.ProposalLifecycleScopeActive,
				false,
				evidenceingestion.EvidenceQueryCompletionBoundedNoMatch,
			),
		},
	}
	response, err := newServer(core).GetGroundedEvidenceBrief(context.Background(), GetGroundedEvidenceBriefRequest{
		Query: "not persisted",
	})
	if err != nil {
		t.Fatalf("GetGroundedEvidenceBrief() error = %v", err)
	}
	if response.Counts.ReturnedMatches != 0 || len(response.Matches) != 0 || len(response.Coverage) != 0 || len(response.FollowUps) != 0 {
		t.Fatalf("empty brief = %+v", response)
	}
	if len(response.ObservationCodes) != 2 ||
		response.ObservationCodes[0] != "exact_lexical_no_match" ||
		response.ObservationCodes[1] != "bounded_retrieval_no_match" {
		t.Fatalf("empty observations = %+v", response.ObservationCodes)
	}
	if !response.QueryExecution.SearchCompleteWithinSurface || response.QueryExecution.GlobalAbsenceInferenceAllowed {
		t.Fatalf("empty query execution = %+v", response.QueryExecution)
	}
	if len(response.Limitations) == 0 || response.Limitations[len(response.Limitations)-1] != "no_absence_inference_outside_the_bounded_search_surface" {
		t.Fatalf("empty limitations = %+v", response.Limitations)
	}
}

func assertBriefFollowUp(t *testing.T, got []GroundedEvidenceBriefFollowUp, want GroundedEvidenceBriefFollowUp) {
	t.Helper()
	for _, item := range got {
		if item == want {
			return
		}
	}
	t.Fatalf("follow-ups %+v do not contain %+v", got, want)
}

func testEvidenceQueryExecution(
	query string,
	mode string,
	limit int,
	lifecycleScope string,
	truncated bool,
	completionReason string,
) evidenceingestion.EvidenceQueryExecution {
	planVersion := evidenceingestion.EvidenceQueryPlanRecoveryV2
	normalizerVersion := evidenceingestion.EvidenceQueryNormalizerRecoveryV1
	if mode == evidenceingestion.EvidenceQueryModeExactLexical {
		planVersion = evidenceingestion.EvidenceQueryPlanExactV1
		normalizerVersion = evidenceingestion.EvidenceQueryNormalizerSimpleV1
	}
	attemptCandidateCount := 0
	candidateCount := 0
	if completionReason != evidenceingestion.EvidenceQueryCompletionBoundedNoMatch {
		attemptCandidateCount = 1
		candidateCount = 1
	}
	if truncated {
		attemptCandidateCount = limit + 1
	}
	attempts := []evidenceingestion.EvidenceQueryAttempt{{
		Strategy:                evidenceingestion.EvidenceQueryStrategyExactSimple,
		TextSearchConfiguration: "simple",
		CompiledQuery:           "'fixture'",
		NormalizedQueryTerms:    []string{"fixture"},
		CandidateCount:          attemptCandidateCount,
		Truncated:               truncated,
	}}
	if mode == evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery {
		morphologyCandidateCount := 0
		if completionReason == evidenceingestion.EvidenceQueryCompletionMorphologyCandidates {
			morphologyCandidateCount = attemptCandidateCount
		}
		attempts = append(attempts, evidenceingestion.EvidenceQueryAttempt{
			Strategy:                evidenceingestion.EvidenceQueryStrategyEnglishMorphology,
			TextSearchConfiguration: "english",
			CompiledQuery:           "'fixtur'",
			NormalizedQueryTerms:    []string{"fixtur"},
			CandidateCount:          morphologyCandidateCount,
			Truncated:               truncated && morphologyCandidateCount > 0,
		})
	}
	return evidenceingestion.EvidenceQueryExecution{
		OriginalQuery:              query,
		QueryMode:                  mode,
		PlanVersion:                planVersion,
		NormalizerVersion:          normalizerVersion,
		SearchSurface:              evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement,
		SearchedFields:             []string{evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement},
		SearchedRecordKinds:        []string{"proposal_occurrence"},
		EligibleSourceBindingKinds: []string{evidenceingestion.ProposalSourceBindingSourceSnapshot, evidenceingestion.ProposalSourceBindingRepositorySnapshot},
		Filters: evidenceingestion.EvidenceQueryFilters{
			LifecycleScope: lifecycleScope,
		},
		Limit:                         limit,
		QueryCount:                    len(attempts),
		CandidateCount:                candidateCount,
		Truncated:                     truncated,
		SearchCompleteWithinSurface:   !truncated,
		CompletionReason:              completionReason,
		GlobalAbsenceInferenceAllowed: false,
		Attempts:                      attempts,
	}
}

func TestCallToolGetsEvidenceRecordThroughQueryCore(t *testing.T) {
	core := &fakeQueryCore{
		result: testQueryResult("occ:1"),
	}
	server := newServer(core)
	payload := []byte(`{"proposal_occurrence_id":"occ:1"}`)

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.calls != 1 || core.occurrenceID != "occ:1" {
		t.Fatalf("query core calls = %d/%q, want 1/occ:1", core.calls, core.occurrenceID)
	}
	if resp.RecordRef.Kind != "proposal" || resp.RecordRef.ID != "occ:1" {
		t.Fatalf("record ref = %+v, want proposal occ:1", resp.RecordRef)
	}
	if resp.AdmissionOutcome != "pending" {
		t.Fatalf("admission outcome = %q, want pending", resp.AdmissionOutcome)
	}
	if resp.CanonicalRef != nil {
		t.Fatalf("canonical ref = %q, want nil", *resp.CanonicalRef)
	}
	if len(resp.SourceRefs) != 1 || resp.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", resp.SourceRefs)
	}
	if resp.Extractor.ExtractionAttemptID != "attempt:1" || resp.Extractor.Name == "" {
		t.Fatalf("extractor provenance not populated: %+v", resp.Extractor)
	}
}

func TestCallToolPreservesResolvedCodeFact(t *testing.T) {
	result := testQueryResult("occ:code")
	result.ProposalFingerprintVersion = evidenceingestion.ProposalFingerprintCodeFactV1
	result.SourceSystem = evidenceingestion.SourceSystemCodeFile
	result.CodeFact = &evidenceingestion.ResolvedCodeFact{
		SchemaVersion:   evidenceingestion.CodeFactSchemaV1,
		FactKind:        evidenceingestion.CodeFactKindDeclaration,
		RepoID:          "ahe-wrap",
		CommitSHA:       "abc123",
		Path:            "internal/refund/service.go",
		FileContentHash: "sha256:file",
		SymbolRef:       "symbol:ahe-wrap:abc123:internal/refund/service.go:refund.Build",
		SymbolKind:      "function",
		QualifiedName:   "refund.Build",
		StartByte:       21,
		EndByte:         26,
		StartLine:       3,
		EndLine:         3,
		QuotedTextHash:  "sha256:quote",
		QuotedText:      "Build",
	}
	server := newServer(&fakeQueryCore{result: result})

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:code"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.CodeFact == nil || response.CodeFact.SymbolRef != result.CodeFact.SymbolRef || response.CodeFact.QuotedText != "Build" {
		t.Fatalf("code fact = %+v", response.CodeFact)
	}
}

func TestCallToolPreservesResolvedPackageFact(t *testing.T) {
	result := testRepositoryQueryResult("occ:package")
	result.ProposalFingerprintVersion = evidenceingestion.ProposalFingerprintCodeFactV1
	result.CodeFact = &evidenceingestion.ResolvedCodeFact{
		SchemaVersion:   evidenceingestion.CodeFactSchemaV2,
		FactKind:        evidenceingestion.CodeFactKindPackage,
		RepoID:          "ahe-wrap",
		CommitSHA:       "abc123",
		Path:            "internal/refund/service.go",
		FileContentHash: "sha256:file",
		SymbolRef:       "symbol:ahe-wrap:abc123:internal/refund/service.go:refund",
		SymbolKind:      evidenceingestion.CodeFactKindPackage,
		QualifiedName:   "refund",
		StartByte:       8,
		EndByte:         14,
		StartLine:       1,
		EndLine:         1,
		QuotedTextHash:  "sha256:quote",
		QuotedText:      "refund",
	}
	server := newServer(&fakeQueryCore{result: result})

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:package"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.CodeFact == nil || response.CodeFact.SchemaVersion != evidenceingestion.CodeFactSchemaV2 || response.CodeFact.FactKind != evidenceingestion.CodeFactKindPackage || response.CodeFact.QuotedText != "refund" {
		t.Fatalf("package fact = %+v", response.CodeFact)
	}
}

func TestCallToolPreservesResolvedCodeRelation(t *testing.T) {
	result := testRepositoryQueryResult("occ:definition")
	result.ProposalFingerprintVersion = evidenceingestion.ProposalFingerprintCodeRelationV1
	result.CodeRelation = &evidenceingestion.ResolvedCodeRelation{
		SchemaVersion: evidenceingestion.CodeRelationSchemaV1,
		RelationKind:  evidenceingestion.CodeRelationKindDefinition,
		RepoID:        "ahe-wrap",
		CommitSHA:     "abc123",
		Usage: evidenceingestion.ResolvedCodeUsageSite{
			Path:       "handler.go",
			QuotedText: "Build",
		},
		Target: evidenceingestion.ResolvedCodeDeclarationEndpoint{
			Path:          "service.go",
			SymbolRef:     "symbol:ahe-wrap:abc123:service.go:sample.Build",
			QualifiedName: "sample.Build",
			QuotedText:    "Build",
		},
	}
	server := newServer(&fakeQueryCore{result: result})

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:definition"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.CodeFact != nil || response.CodeRelation == nil || response.CodeRelation.Target.SymbolRef != result.CodeRelation.Target.SymbolRef {
		t.Fatalf("code relation response = fact %+v relation %+v", response.CodeFact, response.CodeRelation)
	}
}

func TestCallToolPreservesResolvedCodeCallRelation(t *testing.T) {
	result := testRepositoryQueryResult("occ:call")
	result.ProposalFingerprintVersion = evidenceingestion.ProposalFingerprintCodeRelationV2
	caller := evidenceingestion.ResolvedCodeDeclarationEndpoint{
		Path:          "handler.go",
		SymbolRef:     "symbol:ahe-wrap:abc123:handler.go:sample.Handle",
		QualifiedName: "sample.Handle",
		QuotedText:    "Handle",
	}
	result.CodeRelation = &evidenceingestion.ResolvedCodeRelation{
		SchemaVersion: evidenceingestion.CodeRelationSchemaV2,
		RelationKind:  evidenceingestion.CodeRelationKindCall,
		RepoID:        "ahe-wrap",
		CommitSHA:     "abc123",
		Caller:        &caller,
		Usage: evidenceingestion.ResolvedCodeUsageSite{
			Path:       "handler.go",
			QuotedText: "Build",
		},
		Target: evidenceingestion.ResolvedCodeDeclarationEndpoint{
			Path:          "service.go",
			SymbolRef:     "symbol:ahe-wrap:abc123:service.go:sample.Build",
			QualifiedName: "sample.Build",
			QuotedText:    "Build",
		},
	}
	server := newServer(&fakeQueryCore{result: result})

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:call"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.CodeRelation == nil || response.CodeRelation.Caller == nil || response.CodeRelation.Caller.SymbolRef != caller.SymbolRef || response.CodeRelation.Target.SymbolRef != result.CodeRelation.Target.SymbolRef {
		t.Fatalf("code call relation response = %+v", response.CodeRelation)
	}
}

func TestCallToolMapsRepositoryProposalWithoutSyntheticView(t *testing.T) {
	result := testRepositoryQueryResult("occ:repository")
	server := newServer(&fakeQueryCore{result: result})

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:repository"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.Source.BindingKind != evidenceingestion.ProposalSourceBindingRepositorySnapshot || response.Source.RepositorySnapshot == nil {
		t.Fatalf("repository source = %+v", response.Source)
	}
	if response.Source.RepositorySnapshot.ID != result.RepositorySnapshot.ID || response.Source.RepositorySnapshot.CommitSHA != result.RepositorySnapshot.CommitSHA {
		t.Fatalf("repository snapshot = %+v, want %+v", response.Source.RepositorySnapshot, result.RepositorySnapshot)
	}
	if response.Source.SourceSnapshotID != "" || response.ExtractionViewID != "" || response.RendererName != "" {
		t.Fatalf("repository response fabricated source/view fields: %+v", response)
	}
	if len(response.SourceRefs) != 1 || response.SourceRefs[0].FileSnapshotID == "" || response.SourceRefs[0].ExtractionViewID != "" {
		t.Fatalf("repository source refs = %+v", response.SourceRefs)
	}
	if response.SourceGeneration == nil || response.SourceGeneration.ID != result.SourceGeneration.ID || !response.SourceGeneration.Active {
		t.Fatalf("repository source generation = %+v", response.SourceGeneration)
	}
	if response.RepositoryLifecycle == nil || response.RepositoryLifecycle.State != evidenceingestion.RepositoryProposalLifecycleNew || response.RepositoryLifecycle.CurrentProposalOccurrenceID != result.ProposalOccurrenceID {
		t.Fatalf("repository lifecycle = %+v", response.RepositoryLifecycle)
	}
}

func TestCallToolPreservesRepositoryStaleLifecycle(t *testing.T) {
	result := testRepositoryQueryResult("occ:stale")
	result.SourceGenerationActive = false
	result.RepositoryLifecycle = &evidenceingestion.RepositoryProposalLifecycle{
		SourceGenerationID:           "generation:2",
		State:                        evidenceingestion.RepositoryProposalLifecycleStale,
		IdentityContract:             evidenceingestion.RepositoryProposalIdentityContractV1,
		ProposalIdentity:             "sha256:stale-identity",
		PreviousProposalOccurrenceID: result.ProposalOccurrenceID,
	}
	server := newServer(&fakeQueryCore{result: result})

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:stale"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.SourceGeneration == nil || response.SourceGeneration.Active {
		t.Fatalf("stale source generation = %+v", response.SourceGeneration)
	}
	if response.RepositoryLifecycle == nil || response.RepositoryLifecycle.State != evidenceingestion.RepositoryProposalLifecycleStale || response.RepositoryLifecycle.PreviousProposalOccurrenceID != result.ProposalOccurrenceID {
		t.Fatalf("stale repository lifecycle = %+v", response.RepositoryLifecycle)
	}
}

func TestCallToolGetsCanonicalEvidenceRecordThroughQueryCore(t *testing.T) {
	core := &fakeQueryCore{
		canonicalResult: testCanonicalQueryResult("canon-node:1", "occ:1"),
	}
	server := newServer(core)
	payload := []byte(`{"canonical_id":"canon-node:1"}`)

	data, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp GetEvidenceRecordResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.canonicalCalls != 1 || core.canonicalID != "canon-node:1" || core.calls != 0 {
		t.Fatalf("query core calls = canonical %d/%q proposal %d, want canonical only", core.canonicalCalls, core.canonicalID, core.calls)
	}
	if resp.RecordRef.Kind != "canonical_evidence" || resp.RecordRef.ID != "canon-node:1" {
		t.Fatalf("record ref = %+v, want canonical_evidence canon-node:1", resp.RecordRef)
	}
	if resp.ProposalOriginRef == nil || resp.ProposalOriginRef.ID != "occ:1" || resp.ProposalOriginRef.Kind != "proposal" {
		t.Fatalf("proposal origin ref = %+v, want proposal occ:1", resp.ProposalOriginRef)
	}
	if resp.AdmissionOutcome != "admitted" {
		t.Fatalf("admission outcome = %q, want admitted", resp.AdmissionOutcome)
	}
	if resp.CanonicalRef == nil || *resp.CanonicalRef != "canon-node:1" {
		t.Fatalf("canonical ref = %v, want canon-node:1", resp.CanonicalRef)
	}
	if resp.Canonical == nil || resp.Canonical.NodeKind != "source_claim" || resp.Canonical.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("canonical payload = %+v", resp.Canonical)
	}
	if len(resp.SourceRefs) != 1 || resp.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", resp.SourceRefs)
	}
}

func TestCallToolListsEvidenceRecordsThroughQueryCore(t *testing.T) {
	core := &fakeQueryCore{
		listResult: []evidenceingestion.ProposalQueryResult{
			testQueryResult("occ:1"),
			testQueryResult("occ:2"),
		},
	}
	server := newServer(core)
	payload := []byte(`{"source_id":"fixture-refund-policy","admission_outcome":"pending","limit":2}`)

	data, err := server.CallTool(context.Background(), ToolListEvidenceRecords, payload)
	if err != nil {
		t.Fatalf("CallTool(list) error = %v", err)
	}
	var resp ListEvidenceRecordsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal list response: %v", err)
	}

	if core.listCalls != 1 {
		t.Fatalf("list calls = %d, want 1", core.listCalls)
	}
	if core.listInput.SourceID != "fixture-refund-policy" || core.listInput.AdmissionOutcome != "pending" || core.listInput.Limit != 2 {
		t.Fatalf("list input = %+v", core.listInput)
	}
	if resp.Count != 2 || resp.Limit != 2 || len(resp.Records) != 2 {
		t.Fatalf("list response counts = %+v", resp)
	}
	if resp.Records[0].RecordRef.Kind != "proposal" || resp.Records[0].RecordRef.ID != "occ:1" {
		t.Fatalf("first record = %+v", resp.Records[0].RecordRef)
	}
}

func TestCallToolListsRepositoryRecordsThroughQueryCore(t *testing.T) {
	core := &fakeQueryCore{listResult: []evidenceingestion.ProposalQueryResult{testRepositoryQueryResult("occ:repository")}}
	server := newServer(core)
	payload := []byte(`{"repository_snapshot_id":"repo-snapshot:1","source_generation_id":"generation:1","limit":5}`)

	data, err := server.CallTool(context.Background(), ToolListEvidenceRecords, payload)
	if err != nil {
		t.Fatalf("CallTool(list) error = %v", err)
	}
	var response ListEvidenceRecordsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal list response: %v", err)
	}
	if core.listInput.RepositorySnapshotID != "repo-snapshot:1" || core.listInput.SourceGenerationID != "generation:1" || core.listInput.Limit != 5 {
		t.Fatalf("list input = %+v", core.listInput)
	}
	if response.Count != 1 || response.Records[0].Source.RepositorySnapshot == nil || response.Records[0].SourceGeneration == nil || !response.Records[0].SourceGeneration.Active {
		t.Fatalf("repository list response = %+v", response)
	}
	if core.listInput.LifecycleScope != evidenceingestion.ProposalLifecycleScopeAll || response.LifecycleScope != evidenceingestion.ProposalLifecycleScopeAll {
		t.Fatalf("exact generation lifecycle scope = %q/%q, want all", core.listInput.LifecycleScope, response.LifecycleScope)
	}
}

func TestCallToolSearchesGroundedEvidenceRecords(t *testing.T) {
	core := &fakeQueryCore{searchResult: []evidenceingestion.ProposalSearchResult{{
		Record: testQueryResult("occ:search"),
		Rank:   0.75,
	}}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolSearchEvidenceRecords, []byte(`{"query":"refund completed","source_id":"fixture-refund-policy","limit":3}`))
	if err != nil {
		t.Fatalf("CallTool(search) error = %v", err)
	}
	var response SearchEvidenceRecordsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal search response: %v", err)
	}
	if core.searchInput.Query != "refund completed" || core.searchInput.SourceID != "fixture-refund-policy" || core.searchInput.LifecycleScope != evidenceingestion.ProposalLifecycleScopeActive {
		t.Fatalf("search input = %+v", core.searchInput)
	}
	if response.Count != 1 || response.Limit != 3 || response.Matches[0].Rank != 0.75 || response.Matches[0].Record.RecordRef.ID != "occ:search" {
		t.Fatalf("search response = %+v", response)
	}
	if response.SchemaVersion != evidenceSearchSchemaV2 ||
		response.QueryExecution.QueryMode != evidenceingestion.EvidenceQueryModeExactLexical ||
		response.QueryExecution.Filters.SourceID != "fixture-refund-policy" ||
		response.QueryExecution.GlobalAbsenceInferenceAllowed {
		t.Fatalf("search execution = %+v", response.QueryExecution)
	}
	if len(response.Matches[0].Record.SourceRefs) != 1 || response.Matches[0].Record.Extractor.ExtractionAttemptID == "" {
		t.Fatalf("search result is not grounded: %+v", response.Matches[0].Record)
	}
}

func TestCallToolGetsRepositoryRelationProvenance(t *testing.T) {
	relation := testRepositoryRelationResult("occ:relation")
	server := newServer(&fakeQueryCore{result: relation})

	data, err := server.CallTool(context.Background(), ToolGetRelationProvenance, []byte(`{"proposal_occurrence_id":"occ:relation"}`))
	if err != nil {
		t.Fatalf("CallTool(relation provenance) error = %v", err)
	}
	var response RelationProvenanceResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal relation provenance response: %v", err)
	}
	if response.Surface != "repository_code" || response.RelationRef.Kind != "proposal_relation" || response.CodeRelation == nil {
		t.Fatalf("repository relation provenance = %+v", response)
	}
	if response.OriginRecord.RecordRef.ID != relation.ProposalOccurrenceID || len(response.SourceRefs) == 0 {
		t.Fatalf("repository relation origin = %+v", response)
	}
}

func TestCallToolRejectsNonRelationProposalProvenance(t *testing.T) {
	server := newServer(&fakeQueryCore{result: testQueryResult("occ:statement")})
	_, err := server.CallTool(context.Background(), ToolGetRelationProvenance, []byte(`{"proposal_occurrence_id":"occ:statement"}`))
	assertToolError(t, err, toolErrorInvalidRelation)
}

func TestCallToolGetsCanonicalRelationProvenance(t *testing.T) {
	relation := testCanonicalRelationResult()
	server := newServer(&fakeQueryCore{canonicalRelationResult: relation})

	data, err := server.CallTool(context.Background(), ToolGetRelationProvenance, []byte(`{"canonical_edge_id":"canon-edge:1"}`))
	if err != nil {
		t.Fatalf("CallTool(canonical relation provenance) error = %v", err)
	}
	var response RelationProvenanceResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal canonical relation provenance response: %v", err)
	}
	if response.Surface != "canonical_evidence" || response.CanonicalEdge == nil || response.CodeRelation != nil {
		t.Fatalf("canonical relation provenance = %+v", response)
	}
	if response.FromRecord == nil || response.ToRecord == nil || response.OriginRecord.RecordRef.ID != "occ:canonical" {
		t.Fatalf("canonical grounded endpoints = %+v", response)
	}
}

func TestCallToolGetsCanonicalContradictionReviewCardAndRelationOrigin(t *testing.T) {
	contradiction := testCanonicalContradictionResult()
	core := &fakeQueryCore{contradictionResult: contradiction}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolGetCanonicalContradictionProposal, []byte(`{"canonical_contradiction_proposal_id":"contradiction-proposal:1"}`))
	if err != nil {
		t.Fatalf("CallTool(contradiction proposal) error = %v", err)
	}
	var review CanonicalContradictionProposalResponse
	if err := json.Unmarshal(data, &review); err != nil {
		t.Fatalf("Unmarshal contradiction proposal: %v", err)
	}
	if core.contradictionProposalID != "contradiction-proposal:1" || review.Proposal.Relation != "contradicts" || review.Decision == nil {
		t.Fatalf("contradiction review = %+v", review)
	}
	if review.NodeA.RecordRef.ID != contradiction.NodeA.CanonicalID || review.NodeB.RecordRef.ID != contradiction.NodeB.CanonicalID || len(review.NodeA.SourceRefs) == 0 || len(review.NodeB.SourceRefs) == 0 {
		t.Fatalf("review endpoints are not grounded: A=%+v B=%+v", review.NodeA, review.NodeB)
	}

	relation := evidenceingestion.CanonicalRelationQueryResult{
		Edge: evidenceingestion.CanonicalGraphEdge{
			ID:                            "canon-edge:contradiction",
			From:                          contradiction.NodeA.CanonicalID,
			To:                            contradiction.NodeB.CanonicalID,
			Relation:                      evidencegraph.CanonicalContradicts,
			OriginContradictionProposalID: contradiction.Proposal.ID,
		},
		From:                        contradiction.NodeA,
		To:                          contradiction.NodeB,
		OriginContradictionProposal: &contradiction,
	}
	server = newServer(&fakeQueryCore{canonicalRelationResult: relation})
	data, err = server.CallTool(context.Background(), ToolGetRelationProvenance, []byte(`{"canonical_edge_id":"canon-edge:contradiction"}`))
	if err != nil {
		t.Fatalf("CallTool(contradiction relation) error = %v", err)
	}
	var provenance RelationProvenanceResponse
	if err := json.Unmarshal(data, &provenance); err != nil {
		t.Fatalf("Unmarshal contradiction relation: %v", err)
	}
	if provenance.OriginRecord != nil || provenance.OriginContradictionProposal == nil || provenance.OriginContradictionProposal.Proposal.CanonicalContradictionProposalID != contradiction.Proposal.ID {
		t.Fatalf("contradiction relation origin = %+v", provenance)
	}
}

func TestCallToolGetsCanonicalSupersessionReviewCardAndRelationOrigin(t *testing.T) {
	supersession := testCanonicalSupersessionResult()
	core := &fakeQueryCore{supersessionResult: supersession}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolGetCanonicalSupersessionProposal, []byte(`{"canonical_supersession_proposal_id":"supersession-proposal:1"}`))
	if err != nil {
		t.Fatalf("CallTool(supersession proposal) error = %v", err)
	}
	var review CanonicalSupersessionProposalResponse
	if err := json.Unmarshal(data, &review); err != nil {
		t.Fatalf("Unmarshal supersession proposal: %v", err)
	}
	if core.supersessionProposalID != "supersession-proposal:1" || review.Proposal.Relation != "supersedes" || review.Decision == nil {
		t.Fatalf("supersession review = %+v", review)
	}
	if review.Proposal.ProposalSentence == "" || review.Proposal.VersionDifference == "" || len(review.Proposal.Limitations) != 1 {
		t.Fatalf("supersession review material = %+v", review.Proposal)
	}
	if review.From.RecordRef.ID != supersession.From.CanonicalID || review.To.RecordRef.ID != supersession.To.CanonicalID || len(review.From.SourceRefs) == 0 || len(review.To.SourceRefs) == 0 {
		t.Fatalf("review endpoints are not grounded: from=%+v to=%+v", review.From, review.To)
	}

	relation := evidenceingestion.CanonicalRelationQueryResult{
		Edge: evidenceingestion.CanonicalGraphEdge{
			ID:                           "canon-edge:supersession",
			From:                         supersession.From.CanonicalID,
			To:                           supersession.To.CanonicalID,
			Relation:                     evidencegraph.CanonicalSupersedes,
			OriginSupersessionProposalID: supersession.Proposal.ID,
		},
		From:                       supersession.From,
		To:                         supersession.To,
		OriginSupersessionProposal: &supersession,
	}
	server = newServer(&fakeQueryCore{canonicalRelationResult: relation})
	data, err = server.CallTool(context.Background(), ToolGetRelationProvenance, []byte(`{"canonical_edge_id":"canon-edge:supersession"}`))
	if err != nil {
		t.Fatalf("CallTool(supersession relation) error = %v", err)
	}
	var provenance RelationProvenanceResponse
	if err := json.Unmarshal(data, &provenance); err != nil {
		t.Fatalf("Unmarshal supersession relation: %v", err)
	}
	if provenance.OriginRecord != nil || provenance.OriginContradictionProposal != nil || provenance.OriginSupersessionProposal == nil || provenance.OriginSupersessionProposal.Proposal.CanonicalSupersessionProposalID != supersession.Proposal.ID {
		t.Fatalf("supersession relation origin = %+v", provenance)
	}
}

func TestCallToolListsRepositorySymbolNeighbors(t *testing.T) {
	relation := testRepositoryRelationResult("occ:neighbor")
	neighbor := relation.CodeRelation.Target
	core := &fakeQueryCore{repositoryNeighborResult: []evidenceingestion.RepositoryRelationNeighborResult{{
		Directions:          []string{evidenceingestion.RelationDirectionOutgoing},
		Relation:            relation,
		NeighborDeclaration: &neighbor,
	}}}
	server := newServer(core)
	symbolRef := relation.CodeRelation.Caller.SymbolRef
	payload, err := json.Marshal(ListEvidenceNeighborsRequest{SymbolRef: symbolRef, Direction: "outgoing", Relation: "call", Limit: 4})
	if err != nil {
		t.Fatalf("Marshal neighbors request: %v", err)
	}

	data, err := server.CallTool(context.Background(), ToolListEvidenceNeighbors, payload)
	if err != nil {
		t.Fatalf("CallTool(repository neighbors) error = %v", err)
	}
	var response ListEvidenceNeighborsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal repository neighbors response: %v", err)
	}
	if response.Depth != 1 || response.Count != 1 || response.RootRef.Kind != "code_symbol" {
		t.Fatalf("repository neighbors response = %+v", response)
	}
	if core.repositoryNeighborInput.SymbolRef != symbolRef || core.repositoryNeighborInput.LifecycleScope != evidenceingestion.ProposalLifecycleScopeActive {
		t.Fatalf("repository neighbor input = %+v", core.repositoryNeighborInput)
	}
	if response.Neighbors[0].AdjacentDeclaration == nil || response.Neighbors[0].AdjacentCanonical != nil {
		t.Fatalf("repository adjacent endpoint = %+v", response.Neighbors[0])
	}
}

func TestCallToolListsCanonicalNeighbors(t *testing.T) {
	relation := testCanonicalRelationResult()
	core := &fakeQueryCore{canonicalNeighborResult: []evidenceingestion.CanonicalNeighborResult{{
		Directions: []string{evidenceingestion.RelationDirectionOutgoing},
		Relation:   relation,
		Neighbor:   relation.To,
	}}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolListEvidenceNeighbors, []byte(`{"canonical_id":"canon-node:raw","direction":"outgoing","relation":"supports_claim","limit":2}`))
	if err != nil {
		t.Fatalf("CallTool(canonical neighbors) error = %v", err)
	}
	var response ListEvidenceNeighborsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal canonical neighbors response: %v", err)
	}
	if response.Depth != 1 || response.Count != 1 || response.RootRef.ID != "canon-node:raw" || response.LifecycleScope != "" {
		t.Fatalf("canonical neighbors response = %+v", response)
	}
	if response.Neighbors[0].AdjacentCanonical == nil || response.Neighbors[0].Relation.CanonicalEdge == nil {
		t.Fatalf("canonical neighbor = %+v", response.Neighbors[0])
	}
}

func TestCallToolRejectsInvalidRequests(t *testing.T) {
	server := newServer(&fakeQueryCore{})
	for _, tc := range []struct {
		name    string
		tool    string
		payload []byte
		code    string
	}{
		{name: "unknown tool", tool: "submit_manual_evidence", payload: []byte(`{}`), code: toolErrorUnknownTool},
		{name: "malformed json", tool: ToolGetEvidenceRecord, payload: []byte(`{`), code: toolErrorInvalidRequest},
		{name: "unknown field", tool: ToolGetEvidenceRecord, payload: []byte(`{"proposal_occurrence_id":"occ:1","projection":"raw"}`), code: toolErrorInvalidRequest},
		{name: "proposal and canonical id", tool: ToolGetEvidenceRecord, payload: []byte(`{"proposal_occurrence_id":"occ:1","canonical_id":"canon-node:1"}`), code: toolErrorInvalidRequest},
		{name: "empty id", tool: ToolGetEvidenceRecord, payload: []byte(`{"proposal_occurrence_id":""}`), code: toolErrorInvalidRecordID},
		{name: "bad id prefix", tool: ToolGetEvidenceRecord, payload: []byte(`{"proposal_occurrence_id":"proposal:1"}`), code: toolErrorInvalidRecordID},
		{name: "bad canonical prefix", tool: ToolGetEvidenceRecord, payload: []byte(`{"canonical_id":"canon-edge:1"}`), code: toolErrorInvalidRecordID},
		{name: "list unknown field", tool: ToolListEvidenceRecords, payload: []byte(`{"source_id":"fixture","projection":"raw"}`), code: toolErrorInvalidRequest},
		{name: "list bad source snapshot", tool: ToolListEvidenceRecords, payload: []byte(`{"source_snapshot_id":"source:1"}`), code: toolErrorInvalidRecordID},
		{name: "list bad repository snapshot", tool: ToolListEvidenceRecords, payload: []byte(`{"repository_snapshot_id":"repository:1"}`), code: toolErrorInvalidRecordID},
		{name: "list bad source generation", tool: ToolListEvidenceRecords, payload: []byte(`{"source_generation_id":"source-generation:1"}`), code: toolErrorInvalidRecordID},
		{name: "list mutually exclusive snapshots", tool: ToolListEvidenceRecords, payload: []byte(`{"source_snapshot_id":"srcsnap:1","repository_snapshot_id":"repo-snapshot:1"}`), code: toolErrorInvalidRequest},
		{name: "list source snapshot with generation", tool: ToolListEvidenceRecords, payload: []byte(`{"source_snapshot_id":"srcsnap:1","source_generation_id":"generation:1"}`), code: toolErrorInvalidRequest},
		{name: "list source id with generation", tool: ToolListEvidenceRecords, payload: []byte(`{"source_id":"source:1","source_generation_id":"generation:1"}`), code: toolErrorInvalidRequest},
		{name: "list source version with generation", tool: ToolListEvidenceRecords, payload: []byte(`{"source_version":"v1","source_generation_id":"generation:1"}`), code: toolErrorInvalidRequest},
		{name: "list bad outcome", tool: ToolListEvidenceRecords, payload: []byte(`{"admission_outcome":"proposal_graph"}`), code: toolErrorInvalidRequest},
		{name: "list bad lifecycle", tool: ToolListEvidenceRecords, payload: []byte(`{"lifecycle_scope":"current"}`), code: toolErrorInvalidRequest},
		{name: "list negative limit", tool: ToolListEvidenceRecords, payload: []byte(`{"limit":-1}`), code: toolErrorInvalidRequest},
		{name: "list too large limit", tool: ToolListEvidenceRecords, payload: []byte(`{"limit":101}`), code: toolErrorInvalidRequest},
		{name: "search empty query", tool: ToolSearchEvidenceRecords, payload: []byte(`{"query":""}`), code: toolErrorInvalidRequest},
		{name: "search punctuation only", tool: ToolSearchEvidenceRecords, payload: []byte(`{"query":"---"}`), code: toolErrorInvalidRequest},
		{name: "brief bad query mode", tool: ToolGetGroundedEvidenceBrief, payload: []byte(`{"query":"refund","query_mode":"semantic"}`), code: toolErrorInvalidRequest},
		{name: "brief bad response schema", tool: ToolGetGroundedEvidenceBrief, payload: []byte(`{"query":"refund","response_schema":"grounded-evidence-brief-v6"}`), code: toolErrorInvalidRequest},
		{name: "brief unknown field", tool: ToolGetGroundedEvidenceBrief, payload: []byte(`{"query":"refund","provider":"ollama"}`), code: toolErrorInvalidRequest},
		{name: "neighbors both roots", tool: ToolListEvidenceNeighbors, payload: []byte(`{"canonical_id":"canon-node:1","symbol_ref":"symbol:1"}`), code: toolErrorInvalidRequest},
		{name: "neighbors no root", tool: ToolListEvidenceNeighbors, payload: []byte(`{}`), code: toolErrorInvalidRequest},
		{name: "neighbors graph layer", tool: ToolListEvidenceNeighbors, payload: []byte(`{"canonical_id":"canon-node:1","graph_layer":"canonical"}`), code: toolErrorInvalidRequest},
		{name: "canonical neighbors lifecycle", tool: ToolListEvidenceNeighbors, payload: []byte(`{"canonical_id":"canon-node:1","lifecycle_scope":"active"}`), code: toolErrorInvalidRequest},
		{name: "repository neighbors bad relation", tool: ToolListEvidenceNeighbors, payload: []byte(`{"symbol_ref":"symbol:1","relation":"supports_claim"}`), code: toolErrorInvalidRequest},
		{name: "relation both IDs", tool: ToolGetRelationProvenance, payload: []byte(`{"proposal_occurrence_id":"occ:1","canonical_edge_id":"canon-edge:1"}`), code: toolErrorInvalidRequest},
		{name: "relation bad edge ID", tool: ToolGetRelationProvenance, payload: []byte(`{"canonical_edge_id":"edge:1"}`), code: toolErrorInvalidRecordID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.CallTool(context.Background(), tc.tool, tc.payload)
			assertToolError(t, err, tc.code)
		})
	}
}

func TestCallToolMapsNotFound(t *testing.T) {
	server := newServer(&fakeQueryCore{
		err: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorMissingSourceViewAttempt, Message: "missing"},
	})

	_, err := server.CallTool(context.Background(), ToolGetEvidenceRecord, []byte(`{"proposal_occurrence_id":"occ:missing"}`))
	assertToolError(t, err, toolErrorNotFound)
}

func assertToolError(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", wantCode)
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %T = %v, want *ToolError", err, err)
	}
	if toolErr.Code != wantCode {
		t.Fatalf("tool error code = %q, want %q: %v", toolErr.Code, wantCode, err)
	}
}

func testQueryResult(occurrenceID string) evidenceingestion.ProposalQueryResult {
	return evidenceingestion.ProposalQueryResult{
		ProposalOccurrenceID:       occurrenceID,
		ProposalFingerprint:        "fp:1",
		ProposalFingerprintVersion: "statement-v1",
		ProposalKind:               "statement",
		StatementText:              "Refunds must be completed within 7 days.",
		AdmissionOutcome:           "pending",
		SourceRefs: []evidenceingestion.ResolvedSourceRef{{
			ExtractionViewID: "view:1",
			SpanID:           "span:S1",
			StartByte:        0,
			EndByte:          41,
			QuotedTextHash:   "sha256:quote",
			QuotedText:       "Refunds must be completed within 7 days.",
		}},
		ExtractionAttemptID:     "attempt:1",
		ExtractionAttemptStatus: "succeeded",
		ExtractionRunID:         "run:1",
		ExtractorDefinitionID:   "extractor:1",
		ExtractorName:           "frozen-manual-fixture",
		ExtractorVersion:        "v1",
		ExtractorConfigHash:     "sha256:config",
		SourceBindingKind:       evidenceingestion.ProposalSourceBindingSourceSnapshot,
		ExtractionViewID:        "view:1",
		RendererName:            "manual-text-identity",
		RendererVersion:         "v1",
		RenderedContentHash:     "sha256:rendered",
		SourceSnapshotID:        "srcsnap:1",
		SourceSystem:            "manual_text",
		SourceID:                "fixture-refund-policy",
		SourceVersion:           "v1",
		RawContentHash:          "sha256:raw",
	}
}

func testAvailableGroundedEvidenceSourceContext(
	record evidenceingestion.ProposalQueryResult,
) evidenceingestion.GroundedEvidenceSourceContext {
	sourceRef := record.SourceRefs[0]
	return evidenceingestion.GroundedEvidenceSourceContext{
		ProposalOccurrenceID: record.ProposalOccurrenceID,
		Contract: evidenceingestion.
			GroundedEvidenceSourceContextContractV1,
		Status:              evidenceingestion.GroundedEvidenceSourceContextStatusAvailable,
		SearchParticipation: false,
		SearchCore:          append([]evidenceingestion.ResolvedSourceRef(nil), record.SourceRefs...),
		SourceView: &evidenceingestion.GroundedEvidenceSourceView{
			Contract: evidenceingestion.BoundedSourceViewContractV1,
			Status:   evidenceingestion.BoundedSourceViewStatusAvailable,
			Observation: evidenceingestion.BoundedSourceViewObservation{
				SourceSnapshotID: record.SourceSnapshotID,
				ExtractionViewID: record.ExtractionViewID,
				SourceSystem:     record.SourceSystem,
			},
		},
		AtomicContainer: &evidenceingestion.GroundedEvidenceSourceUnit{
			UnitID:     "source-unit:test",
			UnitKind:   evidenceingestion.SourceStructureUnitParagraph,
			UnitClass:  evidenceingestion.SourceStructureUnitClassContent,
			ExactText:  record.StatementText,
			SourceRefs: []evidenceingestion.ResolvedSourceRef{sourceRef},
		},
		ContextEnvelope: &evidenceingestion.GroundedEvidenceSourceContextEnvelope{
			Contract: evidenceingestion.SourceContextEnvelopeContractV1,
			Refs: []evidenceingestion.GroundedEvidenceSourceContextRef{{
				Role:      evidenceingestion.SourceStructureContextCore,
				SourceRef: sourceRef,
			}},
			ReferencedBytes: sourceRef.EndByte - sourceRef.StartByte,
		},
		Limitations: []string{
			"context_does_not_participate_in_candidate_selection_or_ranking",
			"structural_units_are_source_context_not_evidence_claims",
		},
	}
}

func testNotApplicableGroundedEvidenceSourceContext(
	record evidenceingestion.ProposalQueryResult,
) evidenceingestion.GroundedEvidenceSourceContext {
	return evidenceingestion.GroundedEvidenceSourceContext{
		ProposalOccurrenceID: record.ProposalOccurrenceID,
		Contract: evidenceingestion.
			GroundedEvidenceSourceContextContractV1,
		Status: evidenceingestion.
			GroundedEvidenceSourceContextStatusNotApplicable,
		SearchParticipation: false,
		SearchCore: append(
			[]evidenceingestion.ResolvedSourceRef(nil),
			record.SourceRefs...,
		),
		Limitations: []string{
			"context_does_not_participate_in_candidate_selection_or_ranking",
			"source_context_supports_manual_text_source_snapshots_only",
		},
	}
}

func testAvailableGroundedEvidenceRepositoryContext(
	record evidenceingestion.ProposalQueryResult,
) evidenceingestion.GroundedEvidenceRepositoryContext {
	atomic := testGroundedEvidenceRepositoryContextSlice(
		"atomic_container",
		"function",
		record.CodeFact.Path,
		16,
		"func Build() {}",
	)
	packageClause := testGroundedEvidenceRepositoryContextSlice(
		"package_clause",
		"package",
		record.CodeFact.Path,
		0,
		"package refund",
	)
	return evidenceingestion.GroundedEvidenceRepositoryContext{
		ProposalOccurrenceID: record.ProposalOccurrenceID,
		Contract: evidenceingestion.
			GroundedEvidenceRepositoryContextContractV1,
		Status: evidenceingestion.
			GroundedEvidenceRepositoryContextStatusAvailable,
		RecordKind: evidenceingestion.
			GroundedEvidenceRepositoryRecordDeclaration,
		SearchParticipation: false,
		SearchCore: append(
			[]evidenceingestion.ResolvedSourceRef(nil),
			record.SourceRefs...,
		),
		ReferencedBytes: len(atomic.ExactText) + len(packageClause.ExactText),
		Declaration: &evidenceingestion.GroundedEvidenceRepositoryDeclarationContext{
			ContainerKind:   "function",
			AtomicContainer: atomic,
			PackageClause:   &packageClause,
		},
		Limitations: []string{
			"context_does_not_participate_in_candidate_selection_or_ranking",
			"repository_syntax_is_source_context_not_an_evidence_claim",
			"declaration_context_does_not_establish_runtime_behavior",
		},
	}
}

func testAvailableGroundedEvidenceRepositoryRelationContext(
	record evidenceingestion.ProposalQueryResult,
) evidenceingestion.GroundedEvidenceRepositoryContext {
	callerSource := "func Handle() {\n\tBuild()\n}"
	usageText := "Build()"
	targetSource := "func Build() {}"
	packageClause := "package sample"
	usage := testGroundedEvidenceRepositoryRelationEndpoint(
		evidenceingestion.GroundedEvidenceRepositoryContextUsage,
		record.CodeRelation.Usage.Path,
		record.CodeRelation.Usage.StartByte,
		record.CodeRelation.Usage.QuotedText,
		record.CodeRelation.Usage.StartByte,
		usageText,
		0,
		packageClause,
	)
	caller := testGroundedEvidenceRepositoryRelationEndpoint(
		evidenceingestion.GroundedEvidenceRepositoryContextCaller,
		record.CodeRelation.Caller.Path,
		record.CodeRelation.Caller.StartByte,
		record.CodeRelation.Caller.QuotedText,
		16,
		callerSource,
		0,
		packageClause,
	)
	target := testGroundedEvidenceRepositoryRelationEndpoint(
		evidenceingestion.GroundedEvidenceRepositoryContextTarget,
		record.CodeRelation.Target.Path,
		record.CodeRelation.Target.StartByte,
		record.CodeRelation.Target.QuotedText,
		16,
		targetSource,
		0,
		packageClause,
	)
	return evidenceingestion.GroundedEvidenceRepositoryContext{
		ProposalOccurrenceID: record.ProposalOccurrenceID,
		Contract: evidenceingestion.
			GroundedEvidenceRepositoryContextContractV1,
		Status: evidenceingestion.
			GroundedEvidenceRepositoryContextStatusAvailable,
		RecordKind:          evidenceingestion.GroundedEvidenceRepositoryRecordRelation,
		RelationKind:        record.CodeRelation.RelationKind,
		SearchParticipation: false,
		SearchCore: append(
			[]evidenceingestion.ResolvedSourceRef(nil),
			record.SourceRefs...,
		),
		ReferencedBytes: usage.ReferencedBytes +
			caller.ReferencedBytes +
			target.ReferencedBytes,
		Relation: &evidenceingestion.GroundedEvidenceRepositoryRelationContext{
			Endpoints: []evidenceingestion.GroundedEvidenceRepositoryRelationEndpoint{
				usage,
				caller,
				target,
			},
		},
		Limitations: []string{
			"context_does_not_participate_in_candidate_selection_or_ranking",
			"repository_syntax_is_source_context_not_an_evidence_claim",
			"relation_endpoints_remain_separate_source_contexts",
			"relation_context_does_not_establish_causality_or_truth",
			"ambiguous_targets_are_coverage_diagnostics_not_relation_records",
		},
	}
}

func testGroundedEvidenceRepositoryRelationEndpoint(
	role evidenceingestion.GroundedEvidenceRepositoryContextRole,
	path string,
	anchorStart int,
	anchorText string,
	containerStart int,
	containerText string,
	packageStart int,
	packageText string,
) evidenceingestion.GroundedEvidenceRepositoryRelationEndpoint {
	roleText := string(role)
	anchor := testGroundedEvidenceRepositoryContextSlice(
		roleText+"_anchor",
		"identifier",
		path,
		anchorStart,
		anchorText,
	)
	containerKind := "function"
	if role == evidenceingestion.GroundedEvidenceRepositoryContextUsage {
		containerKind = "call_expression"
	}
	container := testGroundedEvidenceRepositoryContextSlice(
		roleText+"_container",
		containerKind,
		path,
		containerStart,
		containerText,
	)
	packageClause := testGroundedEvidenceRepositoryContextSlice(
		roleText+"_package_clause",
		"package",
		path,
		packageStart,
		packageText,
	)
	return evidenceingestion.GroundedEvidenceRepositoryRelationEndpoint{
		Role:            role,
		Path:            path,
		Anchor:          anchor,
		AtomicContainer: container,
		PackageClause:   packageClause,
		ReferencedBytes: len(containerText) + len(packageText),
	}
}

func testGroundedEvidenceRepositoryContextSlice(
	role string,
	kind string,
	path string,
	start int,
	exact string,
) evidenceingestion.GroundedEvidenceRepositoryContextSlice {
	return evidenceingestion.GroundedEvidenceRepositoryContextSlice{
		Role:        role,
		Kind:        kind,
		Path:        path,
		StartByte:   start,
		EndByte:     start + len(exact),
		ExactText:   exact,
		ContentHash: testGroundedEvidenceRepositoryContentHash(exact),
	}
}

func testGroundedEvidenceRepositoryContentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func testRepositoryQueryResult(occurrenceID string) evidenceingestion.ProposalQueryResult {
	result := testQueryResult(occurrenceID)
	result.SourceBindingKind = evidenceingestion.ProposalSourceBindingRepositorySnapshot
	result.SourceRefs = []evidenceingestion.ResolvedSourceRef{{
		TargetKind:           "file_snapshot",
		RepositorySnapshotID: "repo-snapshot:1",
		FileSnapshotID:       "file:1",
		RepoID:               "ahe-wrap",
		CommitSHA:            "abc123",
		Path:                 "internal/refund/service.go",
		SpanID:               "span:S3",
		StartByte:            16,
		EndByte:              31,
		QuotedTextHash:       "sha256:quote",
		QuotedText:           "func Build() {}",
	}}
	result.ExtractionViewID = ""
	result.RendererName = ""
	result.RendererVersion = ""
	result.RenderedContentHash = ""
	result.SourceSnapshotID = ""
	result.SourceSystem = ""
	result.SourceID = ""
	result.SourceVersion = ""
	result.RawContentHash = ""
	result.RepositorySnapshot = &evidenceingestion.RepositorySnapshot{
		ID:                         "repo-snapshot:1",
		RepoID:                     "ahe-wrap",
		CommitSHA:                  "abc123",
		ManifestHash:               "sha256:manifest",
		ManifestEntryCount:         4,
		RevisionVerificationMethod: evidenceingestion.RepositoryRevisionVerificationGitV1,
		ManifestContract:           evidenceingestion.RepositoryManifestGitTreeV1,
		FileSelectionContract:      evidenceingestion.RepositoryFileSelectionTrackedGoV1,
		SelectedFileCount:          2,
	}
	result.SourceGeneration = &evidenceingestion.RepositorySourceGeneration{
		ID:                    "generation:1",
		RepoID:                "ahe-wrap",
		ExtractorName:         result.ExtractorName,
		ExtractorDefinitionID: result.ExtractorDefinitionID,
		Number:                1,
		RepositorySnapshotID:  result.RepositorySnapshot.ID,
		CommitSHA:             result.RepositorySnapshot.CommitSHA,
		ExtractionAttemptID:   result.ExtractionAttemptID,
		ProposalBatchID:       "batch:1",
		ExtractorOutputHash:   "sha256:output",
		ProposalCount:         1,
	}
	result.SourceGenerationActive = true
	result.RepositoryLifecycle = &evidenceingestion.RepositoryProposalLifecycle{
		SourceGenerationID:          result.SourceGeneration.ID,
		State:                       evidenceingestion.RepositoryProposalLifecycleNew,
		IdentityContract:            evidenceingestion.RepositoryProposalIdentityContractV1,
		ProposalIdentity:            "sha256:proposal-identity",
		CurrentProposalOccurrenceID: occurrenceID,
	}
	return result
}

func testRepositoryDeclarationResult(
	occurrenceID string,
) evidenceingestion.ProposalQueryResult {
	result := testRepositoryQueryResult(occurrenceID)
	result.SourceRefs[0].QuotedTextHash =
		testGroundedEvidenceRepositoryContentHash(result.SourceRefs[0].QuotedText)
	result.CodeFact = &evidenceingestion.ResolvedCodeFact{
		SchemaVersion: evidenceingestion.CodeFactSchemaV1,
		FactKind:      evidenceingestion.CodeFactKindDeclaration,
		RepoID:        result.RepositorySnapshot.RepoID,
		CommitSHA:     result.RepositorySnapshot.CommitSHA,
		Path:          result.SourceRefs[0].Path,
		FileContentHash: testGroundedEvidenceRepositoryContentHash(
			"package refund\n\nfunc Build() {}\n",
		),
		SymbolRef:      "symbol:ahe-wrap:abc123:internal/refund/service.go:refund.Build",
		SymbolKind:     "function",
		QualifiedName:  "refund.Build",
		StartByte:      21,
		EndByte:        26,
		QuotedTextHash: testGroundedEvidenceRepositoryContentHash("Build"),
		QuotedText:     "Build",
	}
	return result
}

func testCanonicalQueryResult(canonicalID, occurrenceID string) evidenceingestion.CanonicalQueryResult {
	origin := testQueryResult(occurrenceID)
	origin.AdmissionOutcome = "admitted"
	origin.CanonicalRef = canonicalID
	return evidenceingestion.CanonicalQueryResult{
		CanonicalID:                canonicalID,
		NodeKind:                   evidencegraph.CanonicalSourceClaim,
		OriginProposalOccurrenceID: occurrenceID,
		Payload: evidencegraph.EvidencePayload{
			ID:         "payload:1",
			SourceType: "manual_text",
			Title:      "source claim",
			Source:     "manual_text:fixture-refund-policy@v1",
			Span:       "Refunds must be completed within 7 days.",
			Claim:      "Refunds must be completed within 7 days.",
		},
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            "provenance:1",
			OriginRefs:    []string{"srcsnap:1", "view:1"},
			OriginGroupID: "srcsnap:1",
			Producer:      "frozen-manual-fixture",
			Method:        "proposal_admission",
			MethodVersion: "v1",
			TraceRef:      "fp:1",
		},
		Temporal: evidencegraph.TemporalRecord{
			ID:     "temporal:1",
			Status: evidencegraph.TemporalUnknown,
		},
		Integrity: evidencegraph.IntegrityRecord{
			ID:        "integrity:1",
			Algorithm: "sha256",
			Digest:    "digest",
		},
		OriginProposal: origin,
	}
}

func testRepositoryRelationResult(occurrenceID string) evidenceingestion.ProposalQueryResult {
	result := testRepositoryQueryResult(occurrenceID)
	caller := evidenceingestion.ResolvedCodeDeclarationEndpoint{
		Path:          "handler.go",
		SymbolRef:     "symbol:ahe-wrap:abc123:handler.go:sample.Handle",
		QualifiedName: "sample.Handle",
		QuotedText:    "Handle",
	}
	result.CodeRelation = &evidenceingestion.ResolvedCodeRelation{
		SchemaVersion: evidenceingestion.CodeRelationSchemaV2,
		RelationKind:  evidenceingestion.CodeRelationKindCall,
		RepoID:        "ahe-wrap",
		CommitSHA:     "abc123",
		Caller:        &caller,
		Usage: evidenceingestion.ResolvedCodeUsageSite{
			Path:       "handler.go",
			QuotedText: "Build",
		},
		Target: evidenceingestion.ResolvedCodeDeclarationEndpoint{
			Path:          "service.go",
			SymbolRef:     "symbol:ahe-wrap:abc123:service.go:sample.Build",
			QualifiedName: "sample.Build",
			QuotedText:    "Build",
		},
	}
	return result
}

func testRepositoryRelationContextResult(
	occurrenceID string,
) evidenceingestion.ProposalQueryResult {
	result := testRepositoryQueryResult(occurrenceID)
	callerSource := "package sample\n\nfunc Handle() {\n\tBuild()\n}\n"
	targetSource := "package sample\n\nfunc Build() {}\n"
	callerStart := strings.Index(callerSource, "Handle")
	usageStart := strings.Index(callerSource, "Build")
	targetStart := strings.Index(targetSource, "Build")
	callerPath := "handler.go"
	targetPath := "service.go"
	caller := evidenceingestion.ResolvedCodeDeclarationEndpoint{
		Path:            callerPath,
		FileContentHash: testGroundedEvidenceRepositoryContentHash(callerSource),
		SymbolRef:       "symbol:ahe-wrap:abc123:handler.go:sample.Handle",
		SymbolKind:      "function",
		QualifiedName:   "sample.Handle",
		StartByte:       callerStart,
		EndByte:         callerStart + len("Handle"),
		QuotedTextHash:  testGroundedEvidenceRepositoryContentHash("Handle"),
		QuotedText:      "Handle",
	}
	usage := evidenceingestion.ResolvedCodeUsageSite{
		Path:            callerPath,
		FileContentHash: testGroundedEvidenceRepositoryContentHash(callerSource),
		StartByte:       usageStart,
		EndByte:         usageStart + len("Build"),
		QuotedTextHash:  testGroundedEvidenceRepositoryContentHash("Build"),
		QuotedText:      "Build",
	}
	target := evidenceingestion.ResolvedCodeDeclarationEndpoint{
		Path:            targetPath,
		FileContentHash: testGroundedEvidenceRepositoryContentHash(targetSource),
		SymbolRef:       "symbol:ahe-wrap:abc123:service.go:sample.Build",
		SymbolKind:      "function",
		QualifiedName:   "sample.Build",
		StartByte:       targetStart,
		EndByte:         targetStart + len("Build"),
		QuotedTextHash:  testGroundedEvidenceRepositoryContentHash("Build"),
		QuotedText:      "Build",
	}
	result.CodeRelation = &evidenceingestion.ResolvedCodeRelation{
		SchemaVersion: evidenceingestion.CodeRelationSchemaV2,
		RelationKind:  evidenceingestion.CodeRelationKindCall,
		RepoID:        result.RepositorySnapshot.RepoID,
		CommitSHA:     result.RepositorySnapshot.CommitSHA,
		Caller:        &caller,
		Usage:         usage,
		Target:        target,
	}
	result.SourceRefs = []evidenceingestion.ResolvedSourceRef{
		testRepositoryRelationLineRef(
			result,
			"file:caller",
			callerPath,
			callerSource,
			caller.StartByte,
			caller.EndByte,
			"span:S1",
		),
		testRepositoryRelationLineRef(
			result,
			"file:caller",
			callerPath,
			callerSource,
			usage.StartByte,
			usage.EndByte,
			"span:S2",
		),
		testRepositoryRelationLineRef(
			result,
			"file:target",
			targetPath,
			targetSource,
			target.StartByte,
			target.EndByte,
			"span:S3",
		),
	}
	return result
}

func testRepositoryRelationLineRef(
	record evidenceingestion.ProposalQueryResult,
	fileID string,
	path string,
	source string,
	start int,
	end int,
	spanID string,
) evidenceingestion.ResolvedSourceRef {
	lineStart := strings.LastIndex(source[:start], "\n") + 1
	lineEndOffset := strings.Index(source[end:], "\n")
	lineEnd := len(source)
	if lineEndOffset >= 0 {
		lineEnd = end + lineEndOffset
	}
	exact := source[lineStart:lineEnd]
	return evidenceingestion.ResolvedSourceRef{
		TargetKind:           "file_snapshot",
		RepositorySnapshotID: record.RepositorySnapshot.ID,
		FileSnapshotID:       fileID,
		RepoID:               record.RepositorySnapshot.RepoID,
		CommitSHA:            record.RepositorySnapshot.CommitSHA,
		Path:                 path,
		SpanID:               spanID,
		StartByte:            lineStart,
		EndByte:              lineEnd,
		QuotedTextHash:       testGroundedEvidenceRepositoryContentHash(exact),
		QuotedText:           exact,
	}
}

func testCanonicalRelationResult() evidenceingestion.CanonicalRelationQueryResult {
	from := testCanonicalQueryResult("canon-node:raw", "occ:canonical")
	from.NodeKind = evidencegraph.CanonicalRawEvidence
	from.Payload.Claim = ""
	to := testCanonicalQueryResult("canon-node:claim", "occ:canonical")
	origin := testQueryResult("occ:canonical")
	origin.AdmissionOutcome = "admitted"
	origin.CanonicalRef = to.CanonicalID
	return evidenceingestion.CanonicalRelationQueryResult{
		Edge: evidenceingestion.CanonicalGraphEdge{
			ID:                         "canon-edge:1",
			From:                       from.CanonicalID,
			To:                         to.CanonicalID,
			Relation:                   evidencegraph.CanonicalSupportsClaim,
			Provenance:                 from.Provenance,
			OriginProposalOccurrenceID: origin.ProposalOccurrenceID,
		},
		From:           from,
		To:             to,
		OriginProposal: &origin,
	}
}

func testCanonicalContradictionResult() evidenceingestion.CanonicalContradictionQueryResult {
	nodeA := testCanonicalQueryResult("canon-node:a", "occ:a")
	nodeB := testCanonicalQueryResult("canon-node:b", "occ:b")
	nodeB.Payload.Claim = "Refunds may take 30 days."
	return evidenceingestion.CanonicalContradictionQueryResult{
		Proposal: evidenceingestion.CanonicalContradictionProposal{
			ID:                  "contradiction-proposal:1",
			RequestID:           "request:1",
			ProposalFingerprint: "contradiction-fp:1",
			NodeAID:             nodeA.CanonicalID,
			NodeBID:             nodeB.CanonicalID,
			Relation:            evidencegraph.CanonicalContradicts,
			Rationale:           "The refund limits differ.",
			ProducerName:        "claude-code",
			ProducerVersion:     "workflow-v1",
			ProducerSessionRef:  "session:1",
			AdmissionOutcome:    "admitted",
			CanonicalEdgeID:     "canon-edge:contradiction",
		},
		NodeA: nodeA,
		NodeB: nodeB,
		Decision: &evidenceingestion.CanonicalContradictionDecision{
			ID:              "contradiction-adm:1",
			ProposalID:      "contradiction-proposal:1",
			Outcome:         "admitted",
			CanonicalEdgeID: "canon-edge:contradiction",
			DecisionBy:      "reviewer",
			DecisionReason:  "reviewed both grounded claims",
		},
	}
}

func testCanonicalSupersessionResult() evidenceingestion.CanonicalSupersessionQueryResult {
	current := testCanonicalQueryResult("canon-node:current", "occ:current")
	replaced := testCanonicalQueryResult("canon-node:replaced", "occ:replaced")
	replaced.Payload.Claim = "Refunds may take 60 days."
	return evidenceingestion.CanonicalSupersessionQueryResult{
		Proposal: evidenceingestion.CanonicalSupersessionProposal{
			ID:                  "supersession-proposal:1",
			RequestID:           "request:1",
			ProposalFingerprint: "supersession-fp:1",
			FromNodeID:          current.CanonicalID,
			ToNodeID:            replaced.CanonicalID,
			Relation:            evidencegraph.CanonicalSupersedes,
			ProposalSentence:    "The current refund claim supersedes the historical claim.",
			Rationale:           "The current source explicitly replaces the older revision.",
			VersionDifference:   "The refund period changes from 60 days to 30 days.",
			Limitations:         []string{"Only the supplied policy scope was compared."},
			ProducerName:        "claude-code",
			ProducerVersion:     "workflow-v1",
			ProducerSessionRef:  "session:1",
			AdmissionOutcome:    "admitted",
			CanonicalEdgeID:     "canon-edge:supersession",
		},
		From: current,
		To:   replaced,
		Decision: &evidenceingestion.CanonicalSupersessionDecision{
			ID:              "supersession-adm:1",
			ProposalID:      "supersession-proposal:1",
			Outcome:         "admitted",
			CanonicalEdgeID: "canon-edge:supersession",
			DecisionBy:      "reviewer",
			DecisionReason:  "reviewed both grounded versions",
		},
	}
}

type fakeQueryCore struct {
	calls                    int
	canonicalCalls           int
	listCalls                int
	occurrenceID             string
	canonicalID              string
	result                   evidenceingestion.ProposalQueryResult
	canonicalResult          evidenceingestion.CanonicalQueryResult
	listInput                evidenceingestion.ProposalListInput
	listResult               []evidenceingestion.ProposalQueryResult
	err                      error
	canonicalErr             error
	listErr                  error
	searchInput              evidenceingestion.ProposalSearchInput
	searchResult             []evidenceingestion.ProposalSearchResult
	searchErr                error
	canonicalRelationResult  evidenceingestion.CanonicalRelationQueryResult
	canonicalRelationErr     error
	contradictionProposalID  string
	contradictionResult      evidenceingestion.CanonicalContradictionQueryResult
	contradictionErr         error
	supersessionProposalID   string
	supersessionResult       evidenceingestion.CanonicalSupersessionQueryResult
	supersessionErr          error
	canonicalNeighborInput   evidenceingestion.CanonicalNeighborInput
	canonicalNeighborResult  []evidenceingestion.CanonicalNeighborResult
	canonicalNeighborErr     error
	repositoryNeighborInput  evidenceingestion.RepositoryRelationNeighborInput
	repositoryNeighborResult []evidenceingestion.RepositoryRelationNeighborResult
	repositoryNeighborErr    error
	briefInput               evidenceingestion.GroundedEvidenceBriefInput
	briefResult              evidenceingestion.GroundedEvidenceBriefQueryResult
	briefErr                 error
	sourceStateInput         evidenceingestion.MCPReadSourceStateQueryInput
	sourceStateResult        evidenceingestion.MCPReadSourceStateQueryResult
	sourceStateErr           error
	canonicalReadInput       evidenceingestion.CanonicalReadInput
	canonicalReadResult      evidenceingestion.CanonicalReadView
	canonicalReadErr         error
	canonicalReadCalls       int
}

func (c *fakeQueryCore) ReadCanonicalGraphView(_ context.Context, input evidenceingestion.CanonicalReadInput) (evidenceingestion.CanonicalReadView, error) {
	c.canonicalReadCalls++
	c.canonicalReadInput = input
	if c.canonicalReadErr != nil {
		return evidenceingestion.CanonicalReadView{}, c.canonicalReadErr
	}
	return c.canonicalReadResult, nil
}

func (c *fakeQueryCore) QueryMCPReadSourceStates(_ context.Context, input evidenceingestion.MCPReadSourceStateQueryInput) (evidenceingestion.MCPReadSourceStateQueryResult, error) {
	c.sourceStateInput = input
	if c.sourceStateErr != nil {
		return evidenceingestion.MCPReadSourceStateQueryResult{}, c.sourceStateErr
	}
	return c.sourceStateResult, nil
}

func (c *fakeQueryCore) GetCanonicalRelationByID(_ context.Context, _ string) (evidenceingestion.CanonicalRelationQueryResult, error) {
	if c.canonicalRelationErr != nil {
		return evidenceingestion.CanonicalRelationQueryResult{}, c.canonicalRelationErr
	}
	return c.canonicalRelationResult, nil
}

func (c *fakeQueryCore) GetCanonicalContradictionProposal(_ context.Context, proposalID string) (evidenceingestion.CanonicalContradictionQueryResult, error) {
	c.contradictionProposalID = proposalID
	if c.contradictionErr != nil {
		return evidenceingestion.CanonicalContradictionQueryResult{}, c.contradictionErr
	}
	return c.contradictionResult, nil
}

func (c *fakeQueryCore) GetCanonicalSupersessionProposal(_ context.Context, proposalID string) (evidenceingestion.CanonicalSupersessionQueryResult, error) {
	c.supersessionProposalID = proposalID
	if c.supersessionErr != nil {
		return evidenceingestion.CanonicalSupersessionQueryResult{}, c.supersessionErr
	}
	return c.supersessionResult, nil
}

func (c *fakeQueryCore) GetCanonicalEvidenceByID(_ context.Context, canonicalID string) (evidenceingestion.CanonicalQueryResult, error) {
	c.canonicalCalls++
	c.canonicalID = canonicalID
	if c.canonicalErr != nil {
		return evidenceingestion.CanonicalQueryResult{}, c.canonicalErr
	}
	return c.canonicalResult, nil
}

func (c *fakeQueryCore) TraceProposalProvenance(_ context.Context, occurrenceID string) (evidenceingestion.ProposalQueryResult, error) {
	c.calls++
	c.occurrenceID = occurrenceID
	if c.err != nil {
		return evidenceingestion.ProposalQueryResult{}, c.err
	}
	return c.result, nil
}

func (c *fakeQueryCore) ListProposalRecords(_ context.Context, input evidenceingestion.ProposalListInput) ([]evidenceingestion.ProposalQueryResult, error) {
	c.listCalls++
	c.listInput = input
	if c.listErr != nil {
		return nil, c.listErr
	}
	return append([]evidenceingestion.ProposalQueryResult(nil), c.listResult...), nil
}

func (c *fakeQueryCore) SearchProposalRecordsPage(_ context.Context, input evidenceingestion.ProposalSearchInput) (evidenceingestion.ProposalSearchQueryResult, error) {
	c.searchInput = input
	if c.searchErr != nil {
		return evidenceingestion.ProposalSearchQueryResult{}, c.searchErr
	}
	execution := testEvidenceQueryExecution(
		input.Query,
		evidenceingestion.EvidenceQueryModeExactLexical,
		input.Limit,
		input.LifecycleScope,
		false,
		evidenceingestion.EvidenceQueryCompletionExactCandidates,
	)
	execution.Filters = evidenceingestion.EvidenceQueryFilters{
		SourceSnapshotID:     input.SourceSnapshotID,
		RepositorySnapshotID: input.RepositorySnapshotID,
		SourceGenerationID:   input.SourceGenerationID,
		SourceID:             input.SourceID,
		SourceVersion:        input.SourceVersion,
		AdmissionOutcome:     input.AdmissionOutcome,
		LifecycleScope:       input.LifecycleScope,
	}
	execution.CandidateCount = len(c.searchResult)
	return evidenceingestion.ProposalSearchQueryResult{
		Matches:   append([]evidenceingestion.ProposalSearchResult(nil), c.searchResult...),
		Execution: execution,
	}, nil
}

func (c *fakeQueryCore) GetGroundedEvidenceBrief(_ context.Context, input evidenceingestion.GroundedEvidenceBriefInput) (evidenceingestion.GroundedEvidenceBriefQueryResult, error) {
	c.briefInput = input
	if c.briefErr != nil {
		return evidenceingestion.GroundedEvidenceBriefQueryResult{}, c.briefErr
	}
	return c.briefResult, nil
}

func (c *fakeQueryCore) ListCanonicalNeighbors(_ context.Context, input evidenceingestion.CanonicalNeighborInput) ([]evidenceingestion.CanonicalNeighborResult, error) {
	c.canonicalNeighborInput = input
	if c.canonicalNeighborErr != nil {
		return nil, c.canonicalNeighborErr
	}
	return append([]evidenceingestion.CanonicalNeighborResult(nil), c.canonicalNeighborResult...), nil
}

func (c *fakeQueryCore) ListRepositoryRelationNeighbors(_ context.Context, input evidenceingestion.RepositoryRelationNeighborInput) ([]evidenceingestion.RepositoryRelationNeighborResult, error) {
	c.repositoryNeighborInput = input
	if c.repositoryNeighborErr != nil {
		return nil, c.repositoryNeighborErr
	}
	return append([]evidenceingestion.RepositoryRelationNeighborResult(nil), c.repositoryNeighborResult...), nil
}
