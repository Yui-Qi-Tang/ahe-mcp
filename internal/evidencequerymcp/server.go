// Package evidencequerymcp exposes the external read-only evidence query surface.
package evidencequerymcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ToolGetEvidenceRecord reads one evidence record by exact ID.
	ToolGetEvidenceRecord = "get_evidence_record"
	// ToolListEvidenceRecords lists bounded proposal records by exact filters.
	ToolListEvidenceRecords = "list_evidence_records"
	// ToolSearchEvidenceRecords performs grounded lexical search over persisted proposal statements.
	ToolSearchEvidenceRecords = "search_evidence_records"
	// ToolGetGroundedEvidenceBrief returns a deterministic bounded summary of persisted matches.
	ToolGetGroundedEvidenceBrief = "get_grounded_evidence_brief"
	// ToolListEvidenceNeighbors returns a bounded one-hop neighborhood.
	ToolListEvidenceNeighbors = "list_evidence_neighbors"
	// ToolGetRelationProvenance returns one exact relation with grounded provenance.
	ToolGetRelationProvenance = "get_relation_provenance"
	// ToolGetMCPReadSourceStates reads bounded immutable states for one MCP source binding.
	ToolGetMCPReadSourceStates = "get_mcp_read_source_states"
	// ToolOpenCanonicalReadView materializes one bounded immutable canonical graph view.
	ToolOpenCanonicalReadView = "open_canonical_read_view"
	// ToolFindCanonicalPath finds one relation-scoped path inside an opened canonical read view.
	ToolFindCanonicalPath = "find_canonical_path"
	// ToolGetCanonicalTopologyDiagnostics reads cycle and conflict-cluster diagnostics from an opened view.
	ToolGetCanonicalTopologyDiagnostics = "get_canonical_topology_diagnostics"
	// ToolGetCanonicalContradictionProposal returns a review card for one contradiction proposal.
	ToolGetCanonicalContradictionProposal = "get_canonical_contradiction_proposal"
	// ToolGetCanonicalSupersessionProposal returns a review card for one directed supersession proposal.
	ToolGetCanonicalSupersessionProposal = "get_canonical_supersession_proposal"

	toolErrorInvalidRequest  = "invalid_request"
	toolErrorInvalidRecordID = "invalid_record_id"
	toolErrorInvalidRelation = "invalid_relation"
	toolErrorNotFound        = "not_found"
	toolErrorReadViewMissing = "read_view_not_found"
	toolErrorUnknownTool     = "unknown_tool"
	toolErrorInternal        = "internal_error"

	evidenceSearchSchemaV2 = "evidence-search-v2"

	// GroundedEvidenceBriefSchemaV2 is the stable brief without source context.
	GroundedEvidenceBriefSchemaV2 = "grounded-evidence-brief-v2"
	// GroundedEvidenceBriefSchemaV3 adds opt-in post-retrieval source context.
	GroundedEvidenceBriefSchemaV3 = "grounded-evidence-brief-v3"
	// GroundedEvidenceBriefSchemaV4 adds opt-in typed repository context.
	GroundedEvidenceBriefSchemaV4 = "grounded-evidence-brief-v4"
	// GroundedEvidenceBriefSchemaV5 adds compact per-match authority and source state.
	GroundedEvidenceBriefSchemaV5 = "grounded-evidence-brief-v5"

	groundedEvidenceBriefSchemaV2 = GroundedEvidenceBriefSchemaV2

	groundedEvidenceAuthorityProposalPending   = "proposal_pending"
	groundedEvidenceAuthorityProposalRejected  = "proposal_rejected"
	groundedEvidenceAuthorityProposalAuditOnly = "proposal_audit_only"
	groundedEvidenceAuthorityCanonicalAdmitted = "canonical_evidence_admitted"

	groundedEvidenceRecordLifecycleSourceSnapshot       = "source_snapshot"
	groundedEvidenceRecordLifecycleRepositoryActive     = "repository_active"
	groundedEvidenceRecordLifecycleRepositoryHistorical = "repository_historical"

	groundedEvidenceRevisionSourceVersion = "source_version"
	groundedEvidenceRevisionGitCommit     = "git_commit"

	groundedEvidenceExternalFreshnessNotEvaluated = "not_evaluated"
	groundedEvidenceExternalFreshnessNoComparison = "no_external_source_comparison"
)

// ToolDefinition describes one exposed external MCP-shaped tool.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
}

// GetEvidenceRecordRequest is the bounded exact-get query input.
type GetEvidenceRecordRequest struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id,omitempty"`
	CanonicalID          string `json:"canonical_id,omitempty"`
}

// GetMCPReadSourceStatesRequest selects a bounded MCP source-state projection.
type GetMCPReadSourceStatesRequest = evidenceingestion.MCPReadSourceStateQueryInput

// GetMCPReadSourceStatesResponse preserves immutable states without joining evidence.
type GetMCPReadSourceStatesResponse = evidenceingestion.MCPReadSourceStateQueryResult

// ListEvidenceRecordsRequest is the bounded exact-filter list query input.
type ListEvidenceRecordsRequest struct {
	SourceSnapshotID     string `json:"source_snapshot_id,omitempty"`
	RepositorySnapshotID string `json:"repository_snapshot_id,omitempty"`
	SourceGenerationID   string `json:"source_generation_id,omitempty"`
	SourceID             string `json:"source_id,omitempty"`
	SourceVersion        string `json:"source_version,omitempty"`
	AdmissionOutcome     string `json:"admission_outcome,omitempty"`
	LifecycleScope       string `json:"lifecycle_scope,omitempty"`
	Limit                int    `json:"limit,omitempty"`
}

// ListEvidenceRecordsResponse preserves lifecycle/provenance metadata per record.
type ListEvidenceRecordsResponse struct {
	Records        []GetEvidenceRecordResponse `json:"records"`
	Count          int                         `json:"count"`
	Limit          int                         `json:"limit"`
	LifecycleScope string                      `json:"lifecycle_scope"`
}

// SearchEvidenceRecordsRequest is a bounded grounded lexical query.
type SearchEvidenceRecordsRequest struct {
	Query                string `json:"query"`
	SourceSnapshotID     string `json:"source_snapshot_id,omitempty"`
	RepositorySnapshotID string `json:"repository_snapshot_id,omitempty"`
	SourceGenerationID   string `json:"source_generation_id,omitempty"`
	SourceID             string `json:"source_id,omitempty"`
	SourceVersion        string `json:"source_version,omitempty"`
	AdmissionOutcome     string `json:"admission_outcome,omitempty"`
	LifecycleScope       string `json:"lifecycle_scope,omitempty"`
	Limit                int    `json:"limit,omitempty"`
}

// GroundedSearchMatch pairs PostgreSQL lexical rank with a complete source-backed record.
type GroundedSearchMatch struct {
	Rank   float64                   `json:"rank"`
	Record GetEvidenceRecordResponse `json:"record"`
}

// SearchEvidenceRecordsResponse contains only persisted, provenance-rich proposal matches.
type SearchEvidenceRecordsResponse struct {
	SchemaVersion  string                         `json:"schema_version"`
	Query          string                         `json:"query"`
	QueryExecution GroundedEvidenceQueryExecution `json:"query_execution"`
	Matches        []GroundedSearchMatch          `json:"matches"`
	Count          int                            `json:"count"`
	Limit          int                            `json:"limit"`
	LifecycleScope string                         `json:"lifecycle_scope"`
}

// GetGroundedEvidenceBriefRequest is a bounded deterministic query over persisted evidence.
type GetGroundedEvidenceBriefRequest struct {
	Query                string `json:"query"`
	QueryMode            string `json:"query_mode,omitempty"`
	ResponseSchema       string `json:"response_schema,omitempty"`
	SourceSnapshotID     string `json:"source_snapshot_id,omitempty"`
	RepositorySnapshotID string `json:"repository_snapshot_id,omitempty"`
	SourceGenerationID   string `json:"source_generation_id,omitempty"`
	SourceID             string `json:"source_id,omitempty"`
	SourceVersion        string `json:"source_version,omitempty"`
	AdmissionOutcome     string `json:"admission_outcome,omitempty"`
	LifecycleScope       string `json:"lifecycle_scope,omitempty"`
	Limit                int    `json:"limit,omitempty"`
}

// GroundedEvidenceBriefBoundary states the closed persisted search surface.
type GroundedEvidenceBriefBoundary struct {
	SearchSurface  string `json:"search_surface"`
	QueryMode      string `json:"query_mode"`
	LifecycleScope string `json:"lifecycle_scope"`
	Limit          int    `json:"limit"`
	Truncated      bool   `json:"truncated"`
}

// GroundedEvidenceQueryFilters records the exact filters applied by Query Core.
type GroundedEvidenceQueryFilters struct {
	SourceSnapshotID     string `json:"source_snapshot_id,omitempty"`
	RepositorySnapshotID string `json:"repository_snapshot_id,omitempty"`
	SourceGenerationID   string `json:"source_generation_id,omitempty"`
	SourceID             string `json:"source_id,omitempty"`
	SourceVersion        string `json:"source_version,omitempty"`
	AdmissionOutcome     string `json:"admission_outcome,omitempty"`
	LifecycleScope       string `json:"lifecycle_scope"`
}

// GroundedEvidenceQueryAttempt records one deterministic lexical lookup.
type GroundedEvidenceQueryAttempt struct {
	Strategy                string   `json:"strategy"`
	TextSearchConfiguration string   `json:"text_search_configuration"`
	CompiledQuery           string   `json:"compiled_query"`
	NormalizedQueryTerms    []string `json:"normalized_query_terms"`
	CandidateCount          int      `json:"candidate_count"`
	Truncated               bool     `json:"truncated"`
}

// GroundedEvidenceQueryExecution is the authoritative bounded query trace.
type GroundedEvidenceQueryExecution struct {
	OriginalQuery                 string                         `json:"original_query"`
	QueryMode                     string                         `json:"query_mode"`
	PlanVersion                   string                         `json:"plan_version"`
	NormalizerVersion             string                         `json:"normalizer_version"`
	SearchSurface                 string                         `json:"search_surface"`
	SearchedFields                []string                       `json:"searched_fields"`
	SearchedRecordKinds           []string                       `json:"searched_record_kinds"`
	EligibleSourceBindingKinds    []string                       `json:"eligible_source_binding_kinds"`
	Filters                       GroundedEvidenceQueryFilters   `json:"filters"`
	Limit                         int                            `json:"limit"`
	QueryCount                    int                            `json:"query_count"`
	CandidateCount                int                            `json:"candidate_count"`
	Truncated                     bool                           `json:"truncated"`
	SearchCompleteWithinSurface   bool                           `json:"search_complete_within_surface"`
	CompletionReason              string                         `json:"completion_reason"`
	GlobalAbsenceInferenceAllowed bool                           `json:"global_absence_inference_allowed"`
	Attempts                      []GroundedEvidenceQueryAttempt `json:"attempts"`
}

// GroundedEvidenceBriefMatch preserves exact statement and provenance references.
type GroundedEvidenceBriefMatch struct {
	Rank              float64                                              `json:"rank"`
	RecordRef         RecordRef                                            `json:"record_ref"`
	ProposalLocalID   string                                               `json:"proposal_local_id,omitempty"`
	StatementText     string                                               `json:"statement_text"`
	ProposalKind      string                                               `json:"proposal_kind"`
	AdmissionOutcome  string                                               `json:"admission_outcome"`
	CanonicalRef      *RecordRef                                           `json:"canonical_ref,omitempty"`
	SourceScopeRef    RecordRef                                            `json:"source_scope_ref"`
	SourceRefs        []evidenceingestion.ResolvedSourceRef                `json:"source_refs"`
	SourceContext     *evidenceingestion.GroundedEvidenceSourceContext     `json:"source_context,omitempty"`
	RepositoryContext *evidenceingestion.GroundedEvidenceRepositoryContext `json:"repository_context,omitempty"`
	RelationRef       *RecordRef                                           `json:"relation_ref,omitempty"`
	SymbolRefs        []RecordRef                                          `json:"symbol_refs,omitempty"`
	RecordState       *GroundedEvidenceBriefRecordState                    `json:"record_state,omitempty"`
}

// GroundedEvidenceBriefRecordState is a compact deterministic projection of
// persisted lifecycle, admission, canonical, and source-revision fields.
type GroundedEvidenceBriefRecordState struct {
	AuthorityStatus         string `json:"authority_status"`
	RecordLifecycle         string `json:"record_lifecycle"`
	SourceBindingKind       string `json:"source_binding_kind"`
	RevisionKind            string `json:"revision_kind"`
	Revision                string `json:"revision"`
	ExternalFreshnessStatus string `json:"external_freshness_status"`
	ExternalFreshnessBasis  string `json:"external_freshness_basis"`
}

// GroundedEvidenceBriefSourceScope identifies one exact persisted source boundary.
type GroundedEvidenceBriefSourceScope struct {
	ScopeRef         RecordRef             `json:"scope_ref"`
	Source           SourceInfo            `json:"source"`
	SourceGeneration *SourceGenerationInfo `json:"source_generation,omitempty"`
}

// GroundedEvidenceBriefAdmissionCounts counts only returned bounded matches.
type GroundedEvidenceBriefAdmissionCounts struct {
	Pending   int `json:"pending"`
	Rejected  int `json:"rejected"`
	AuditOnly int `json:"audit_only"`
	Admitted  int `json:"admitted"`
}

// GroundedEvidenceBriefLifecycleCounts counts only returned bounded matches.
type GroundedEvidenceBriefLifecycleCounts struct {
	SourceSnapshot       int `json:"source_snapshot"`
	RepositoryActive     int `json:"repository_active"`
	RepositoryHistorical int `json:"repository_historical"`
}

// GroundedEvidenceBriefCounts labels all counts as bounded returned-match aggregates.
type GroundedEvidenceBriefCounts struct {
	ReturnedMatches int                                  `json:"returned_matches"`
	Admission       GroundedEvidenceBriefAdmissionCounts `json:"admission"`
	Lifecycle       GroundedEvidenceBriefLifecycleCounts `json:"lifecycle"`
}

// GroundedEvidenceBriefCoverage exposes exact persisted extractor diagnostics.
type GroundedEvidenceBriefCoverage struct {
	ExtractionAttemptRef  RecordRef                                 `json:"extraction_attempt_ref"`
	RepositorySnapshotRef *RecordRef                                `json:"repository_snapshot_ref,omitempty"`
	SourceGenerationRef   *RecordRef                                `json:"source_generation_ref,omitempty"`
	Diagnostic            evidenceingestion.RepositoryGoplsCoverage `json:"diagnostic"`
}

// GroundedEvidenceBriefFollowUp is an exact argument set for an existing query tool.
type GroundedEvidenceBriefFollowUp struct {
	Tool                 string `json:"tool"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id,omitempty"`
	CanonicalID          string `json:"canonical_id,omitempty"`
	SymbolRef            string `json:"symbol_ref,omitempty"`
	SourceGenerationID   string `json:"source_generation_id,omitempty"`
	LifecycleScope       string `json:"lifecycle_scope,omitempty"`
}

// GroundedEvidenceBriefResponse is evidence-only and contains no generated conclusion.
type GroundedEvidenceBriefResponse struct {
	SchemaVersion    string                             `json:"schema_version"`
	Query            string                             `json:"query"`
	QueryExecution   GroundedEvidenceQueryExecution     `json:"query_execution"`
	Boundary         GroundedEvidenceBriefBoundary      `json:"boundary"`
	Matches          []GroundedEvidenceBriefMatch       `json:"matches"`
	SourceScopes     []GroundedEvidenceBriefSourceScope `json:"source_scopes"`
	Counts           GroundedEvidenceBriefCounts        `json:"counts"`
	Coverage         []GroundedEvidenceBriefCoverage    `json:"coverage"`
	FollowUps        []GroundedEvidenceBriefFollowUp    `json:"follow_ups"`
	ObservationCodes []string                           `json:"observation_codes"`
	Limitations      []string                           `json:"limitations"`
}

// GetRelationProvenanceRequest selects exactly one repository or canonical relation.
type GetRelationProvenanceRequest struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id,omitempty"`
	CanonicalEdgeID      string `json:"canonical_edge_id,omitempty"`
}

// GetCanonicalContradictionProposalRequest selects one exact relation proposal.
type GetCanonicalContradictionProposalRequest struct {
	CanonicalContradictionProposalID string `json:"canonical_contradiction_proposal_id"`
}

// CanonicalContradictionProposalInfo exposes proposal and producer audit metadata.
type CanonicalContradictionProposalInfo struct {
	CanonicalContradictionProposalID string `json:"canonical_contradiction_proposal_id"`
	RequestID                        string `json:"request_id"`
	ProposalFingerprint              string `json:"proposal_fingerprint"`
	NodeAID                          string `json:"node_a_id"`
	NodeBID                          string `json:"node_b_id"`
	Relation                         string `json:"relation"`
	Rationale                        string `json:"rationale"`
	ProducerName                     string `json:"producer_name"`
	ProducerVersion                  string `json:"producer_version"`
	ProducerSessionRef               string `json:"producer_session_ref,omitempty"`
	AdmissionOutcome                 string `json:"admission_outcome"`
	CanonicalEdgeID                  string `json:"canonical_edge_id,omitempty"`
}

// CanonicalContradictionDecisionInfo exposes the immutable human review result.
type CanonicalContradictionDecisionInfo struct {
	AdmissionDecisionID string `json:"admission_decision_id"`
	AdmissionOutcome    string `json:"admission_outcome"`
	CanonicalEdgeID     string `json:"canonical_edge_id,omitempty"`
	DecisionBy          string `json:"decision_by"`
	DecisionReason      string `json:"decision_reason"`
}

// CanonicalContradictionProposalResponse is the minimum human review card:
// the proposal rationale, both complete grounded claims, and any decision.
type CanonicalContradictionProposalResponse struct {
	Proposal CanonicalContradictionProposalInfo  `json:"proposal"`
	NodeA    GetEvidenceRecordResponse           `json:"node_a"`
	NodeB    GetEvidenceRecordResponse           `json:"node_b"`
	Decision *CanonicalContradictionDecisionInfo `json:"decision,omitempty"`
}

// GetCanonicalSupersessionProposalRequest selects one exact directed relation proposal.
type GetCanonicalSupersessionProposalRequest struct {
	CanonicalSupersessionProposalID string `json:"canonical_supersession_proposal_id"`
}

// CanonicalSupersessionProposalInfo exposes direction, review material, and producer metadata.
type CanonicalSupersessionProposalInfo struct {
	CanonicalSupersessionProposalID string   `json:"canonical_supersession_proposal_id"`
	RequestID                       string   `json:"request_id"`
	ProposalFingerprint             string   `json:"proposal_fingerprint"`
	FromNodeID                      string   `json:"from_node_id"`
	ToNodeID                        string   `json:"to_node_id"`
	Relation                        string   `json:"relation"`
	ProposalSentence                string   `json:"proposal_sentence"`
	Rationale                       string   `json:"rationale"`
	VersionDifference               string   `json:"version_difference"`
	Limitations                     []string `json:"limitations"`
	ProducerName                    string   `json:"producer_name"`
	ProducerVersion                 string   `json:"producer_version"`
	ProducerSessionRef              string   `json:"producer_session_ref,omitempty"`
	AdmissionOutcome                string   `json:"admission_outcome"`
	CanonicalEdgeID                 string   `json:"canonical_edge_id,omitempty"`
}

// CanonicalSupersessionDecisionInfo exposes the immutable human review result.
type CanonicalSupersessionDecisionInfo struct {
	AdmissionDecisionID string `json:"admission_decision_id"`
	AdmissionOutcome    string `json:"admission_outcome"`
	CanonicalEdgeID     string `json:"canonical_edge_id,omitempty"`
	DecisionBy          string `json:"decision_by"`
	DecisionReason      string `json:"decision_reason"`
}

// CanonicalSupersessionProposalResponse is the minimum human review card:
// proposal material, current and replaced grounded claims, and any decision.
type CanonicalSupersessionProposalResponse struct {
	Proposal CanonicalSupersessionProposalInfo  `json:"proposal"`
	From     GetEvidenceRecordResponse          `json:"from"`
	To       GetEvidenceRecordResponse          `json:"to"`
	Decision *CanonicalSupersessionDecisionInfo `json:"decision,omitempty"`
}

// CanonicalEdgeInfo exposes an admitted canonical edge without projection controls.
type CanonicalEdgeInfo struct {
	From       RecordRef                      `json:"from"`
	To         RecordRef                      `json:"to"`
	Relation   string                         `json:"relation"`
	Provenance evidencegraph.ProvenanceRecord `json:"provenance"`
}

// RelationProvenanceResponse preserves the distinct canonical and repository relation surfaces.
type RelationProvenanceResponse struct {
	RelationRef                 RecordRef                               `json:"relation_ref"`
	Surface                     string                                  `json:"surface"`
	RelationKind                string                                  `json:"relation_kind"`
	OriginRecord                *GetEvidenceRecordResponse              `json:"origin_record,omitempty"`
	OriginContradictionProposal *CanonicalContradictionProposalResponse `json:"origin_contradiction_proposal,omitempty"`
	OriginSupersessionProposal  *CanonicalSupersessionProposalResponse  `json:"origin_supersession_proposal,omitempty"`
	SourceRefs                  []evidenceingestion.ResolvedSourceRef   `json:"source_refs,omitempty"`
	CodeRelation                *evidenceingestion.ResolvedCodeRelation `json:"code_relation,omitempty"`
	CanonicalEdge               *CanonicalEdgeInfo                      `json:"canonical_edge,omitempty"`
	FromRecord                  *GetEvidenceRecordResponse              `json:"from_record,omitempty"`
	ToRecord                    *GetEvidenceRecordResponse              `json:"to_record,omitempty"`
}

// ListEvidenceNeighborsRequest selects a canonical node or repository symbol as a one-hop root.
type ListEvidenceNeighborsRequest struct {
	CanonicalID        string `json:"canonical_id,omitempty"`
	SymbolRef          string `json:"symbol_ref,omitempty"`
	Direction          string `json:"direction,omitempty"`
	Relation           string `json:"relation,omitempty"`
	LifecycleScope     string `json:"lifecycle_scope,omitempty"`
	SourceGenerationID string `json:"source_generation_id,omitempty"`
	Limit              int    `json:"limit,omitempty"`
}

// EvidenceNeighborResponse is one relation and its adjacent grounded endpoint.
type EvidenceNeighborResponse struct {
	Directions          []string                                           `json:"directions"`
	Relation            RelationProvenanceResponse                         `json:"relation"`
	AdjacentCanonical   *GetEvidenceRecordResponse                         `json:"adjacent_canonical,omitempty"`
	AdjacentDeclaration *evidenceingestion.ResolvedCodeDeclarationEndpoint `json:"adjacent_declaration,omitempty"`
	AdjacentUsage       *evidenceingestion.ResolvedCodeUsageSite           `json:"adjacent_usage,omitempty"`
}

// ListEvidenceNeighborsResponse is always depth one and bounded by Limit.
type ListEvidenceNeighborsResponse struct {
	RootRef        RecordRef                  `json:"root_ref"`
	Depth          int                        `json:"depth"`
	Neighbors      []EvidenceNeighborResponse `json:"neighbors"`
	Count          int                        `json:"count"`
	Limit          int                        `json:"limit"`
	LifecycleScope string                     `json:"lifecycle_scope,omitempty"`
}

// GetEvidenceRecordResponse preserves proposal lifecycle and provenance metadata.
type GetEvidenceRecordResponse struct {
	RecordRef                  RecordRef                                      `json:"record_ref"`
	ProposalLocalID            string                                         `json:"proposal_local_id,omitempty"`
	ProposalFingerprint        string                                         `json:"proposal_fingerprint"`
	ProposalFingerprintVersion string                                         `json:"proposal_fingerprint_version"`
	ProposalKind               string                                         `json:"proposal_kind"`
	StatementText              string                                         `json:"statement_text"`
	AdmissionOutcome           string                                         `json:"admission_outcome"`
	CanonicalRef               *string                                        `json:"canonical_ref"`
	ProposalOriginRef          *RecordRef                                     `json:"proposal_origin_ref,omitempty"`
	Canonical                  *CanonicalInfo                                 `json:"canonical,omitempty"`
	Source                     SourceInfo                                     `json:"source"`
	SourceRefs                 []evidenceingestion.ResolvedSourceRef          `json:"source_refs"`
	CodeFact                   *evidenceingestion.ResolvedCodeFact            `json:"code_fact,omitempty"`
	CodeRelation               *evidenceingestion.ResolvedCodeRelation        `json:"code_relation,omitempty"`
	Extractor                  ExtractorInfo                                  `json:"extractor"`
	SourceGeneration           *SourceGenerationInfo                          `json:"source_generation,omitempty"`
	RepositoryLifecycle        *evidenceingestion.RepositoryProposalLifecycle `json:"repository_lifecycle,omitempty"`
	ExtractionViewID           string                                         `json:"extraction_view_id,omitempty"`
	RendererName               string                                         `json:"renderer_name,omitempty"`
	RendererVersion            string                                         `json:"renderer_version,omitempty"`
	RenderedContentHash        string                                         `json:"rendered_content_hash,omitempty"`
}

// SourceGenerationInfo reports the immutable repository generation and whether it is the current stream head.
type SourceGenerationInfo struct {
	evidenceingestion.RepositorySourceGeneration
	Active bool `json:"active"`
}

// RecordRef identifies the returned record kind and ID.
type RecordRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// SourceInfo identifies the immutable source backing the proposal.
type SourceInfo struct {
	BindingKind        string                                `json:"binding_kind"`
	SourceSnapshotID   string                                `json:"source_snapshot_id,omitempty"`
	SourceSystem       string                                `json:"source_system,omitempty"`
	SourceID           string                                `json:"source_id,omitempty"`
	SourceVersion      string                                `json:"source_version,omitempty"`
	RawContentHash     string                                `json:"raw_content_hash,omitempty"`
	RepositorySnapshot *evidenceingestion.RepositorySnapshot `json:"repository_snapshot,omitempty"`
	MCPRead            *MCPReadSourceProvenance              `json:"mcp_read,omitempty"`
	ExternalSource     *ExternalSourceProvenance             `json:"external_source,omitempty"`
}

// ExternalSourceProvenance exposes the provider-neutral source authority
// declared by the external agent or connector before proposal production.
type ExternalSourceProvenance struct {
	SchemaVersion                 string   `json:"schema_version"`
	SourceSystem                  string   `json:"source_system"`
	SourceNamespace               string   `json:"source_namespace"`
	ObjectType                    string   `json:"object_type"`
	ObjectID                      string   `json:"object_id"`
	Revision                      string   `json:"revision"`
	SourceLocation                string   `json:"source_location"`
	Title                         string   `json:"title,omitempty"`
	ContentFormat                 string   `json:"content_format"`
	ContentFidelity               string   `json:"content_fidelity"`
	Coverage                      string   `json:"coverage"`
	Limitations                   []string `json:"limitations"`
	SourceCreatedAt               string   `json:"source_created_at,omitempty"`
	SourceUpdatedAt               string   `json:"source_updated_at,omitempty"`
	GlobalAbsenceInferenceAllowed bool     `json:"global_absence_inference_allowed"`
}

// MCPReadSourceProvenance exposes the bounded, non-secret remote source
// authority persisted before proposal conversion.
type MCPReadSourceProvenance struct {
	ConnectorDeliveryID           string                               `json:"connector_delivery_id"`
	Provider                      string                               `json:"provider"`
	ObjectID                      string                               `json:"object_id"`
	Revision                      string                               `json:"revision"`
	DocumentID                    string                               `json:"document_id"`
	SourceLocation                string                               `json:"source_location"`
	SourceIdentityContract        string                               `json:"source_identity_contract"`
	ProposalCandidateContract     string                               `json:"proposal_candidate_contract"`
	CoverageComplete              bool                                 `json:"coverage_complete"`
	CoverageTruncated             bool                                 `json:"coverage_truncated"`
	CoverageCompletionReason      string                               `json:"coverage_completion_reason"`
	Limitations                   []string                             `json:"limitations"`
	ProposalCandidates            []MCPReadProposalCandidateProvenance `json:"proposal_candidates"`
	GlobalAbsenceInferenceAllowed bool                                 `json:"global_absence_inference_allowed"`
}

// MCPReadProposalCandidateProvenance identifies one adapter-selected source
// location. The matching proposal_local_id links it to a returned record.
type MCPReadProposalCandidateProvenance struct {
	LocalID      string `json:"local_id"`
	SelectorKind string `json:"selector_kind"`
	Selector     string `json:"selector"`
}

// ExtractorInfo identifies the producer provenance for the proposal.
type ExtractorInfo struct {
	ExtractorDefinitionID string `json:"extractor_definition_id"`
	ExtractionRunID       string `json:"extraction_run_id"`
	ExtractionAttemptID   string `json:"extraction_attempt_id"`
	ProducerSessionRef    string `json:"producer_session_ref,omitempty"`
	Name                  string `json:"name"`
	Version               string `json:"version"`
	ConfigHash            string `json:"config_hash"`
	AttemptStatus         string `json:"attempt_status"`
}

// CanonicalInfo exposes one admitted canonical node without projection controls.
type CanonicalInfo struct {
	NodeKind   string                         `json:"node_kind"`
	Payload    evidencegraph.EvidencePayload  `json:"payload"`
	Provenance evidencegraph.ProvenanceRecord `json:"provenance"`
	Temporal   evidencegraph.TemporalRecord   `json:"temporal"`
	Integrity  evidencegraph.IntegrityRecord  `json:"integrity"`
}

// ToolError is the stable MCP-shaped query error.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error
}

func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *ToolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type queryCore interface {
	ReadCanonicalGraphView(ctx context.Context, input evidenceingestion.CanonicalReadInput) (evidenceingestion.CanonicalReadView, error)
	GetCanonicalRelationByID(ctx context.Context, canonicalEdgeID string) (evidenceingestion.CanonicalRelationQueryResult, error)
	GetCanonicalContradictionProposal(ctx context.Context, proposalID string) (evidenceingestion.CanonicalContradictionQueryResult, error)
	GetCanonicalSupersessionProposal(ctx context.Context, proposalID string) (evidenceingestion.CanonicalSupersessionQueryResult, error)
	GetCanonicalEvidenceByID(ctx context.Context, canonicalID string) (evidenceingestion.CanonicalQueryResult, error)
	ListCanonicalNeighbors(ctx context.Context, input evidenceingestion.CanonicalNeighborInput) ([]evidenceingestion.CanonicalNeighborResult, error)
	ListProposalRecords(ctx context.Context, input evidenceingestion.ProposalListInput) ([]evidenceingestion.ProposalQueryResult, error)
	ListRepositoryRelationNeighbors(ctx context.Context, input evidenceingestion.RepositoryRelationNeighborInput) ([]evidenceingestion.RepositoryRelationNeighborResult, error)
	QueryMCPReadSourceStates(ctx context.Context, input evidenceingestion.MCPReadSourceStateQueryInput) (evidenceingestion.MCPReadSourceStateQueryResult, error)
	GetGroundedEvidenceBrief(ctx context.Context, input evidenceingestion.GroundedEvidenceBriefInput) (evidenceingestion.GroundedEvidenceBriefQueryResult, error)
	SearchProposalRecordsPage(ctx context.Context, input evidenceingestion.ProposalSearchInput) (evidenceingestion.ProposalSearchQueryResult, error)
	TraceProposalProvenance(ctx context.Context, occurrenceID string) (evidenceingestion.ProposalQueryResult, error)
}

// Server exposes bounded external read-only evidence queries.
type Server struct {
	core      queryCore
	readViews *canonicalReadViewCache
}

// NewServer constructs the external read-only query server over PostgreSQL.
func NewServer(pool *pgxpool.Pool) (*Server, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return newServer(postgresCore{pool: pool}), nil
}

func newServer(core queryCore) *Server {
	return &Server{
		core:      core,
		readViews: newCanonicalReadViewCache(canonicalReadViewCacheCapacity),
	}
}

// Tools returns the read-only query tool surface. It intentionally contains no write tools.
func (s *Server) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolGetEvidenceRecord,
			Description: "Read one proposal or admitted canonical evidence record by exact ID.",
			ReadOnly:    true,
		},
		{
			Name:        ToolListEvidenceRecords,
			Description: "List bounded proposal records under an explicit active, historical, or all lifecycle scope.",
			ReadOnly:    true,
		},
		{
			Name:        ToolSearchEvidenceRecords,
			Description: "Run one exact bounded lexical lookup over persisted proposal statements and return complete grounded records.",
			ReadOnly:    true,
		},
		{
			Name:        ToolGetGroundedEvidenceBrief,
			Description: "Compile one information need through bounded deterministic lexical recovery and return evidence plus authoritative query provenance, with optional bounded post-retrieval manual or typed repository context, without generating conclusions.",
			ReadOnly:    true,
		},
		{
			Name:        ToolListEvidenceNeighbors,
			Description: "List a bounded one-hop canonical or repository-symbol neighborhood.",
			ReadOnly:    true,
		},
		{
			Name:        ToolGetRelationProvenance,
			Description: "Read one exact canonical edge or repository typed relation with provenance.",
			ReadOnly:    true,
		},
		{
			Name:        ToolGetMCPReadSourceStates,
			Description: "Read latest-observed, exact-revision, history, or explicit compare metadata for one immutable MCP source stream without joining evidence across revisions.",
			ReadOnly:    true,
		},
		{
			Name:        ToolOpenCanonicalReadView,
			Description: "Materialize one bounded immutable canonical graph view from PostgreSQL and retain it under an instance-local cached handle; the handle can be evicted and is lost on process restart.",
			ReadOnly:    true,
		},
		{
			Name:        ToolFindCanonicalPath,
			Description: "Find one relation-scoped structural path witness inside an explicitly opened bounded view. found_in_view=false is not global absence. contradicts traverses both directions; every other relation follows its stored direction.",
			ReadOnly:    true,
		},
		{
			Name:        ToolGetCanonicalTopologyDiagnostics,
			Description: "Read derived_from and supersedes cycle witnesses plus contradiction connected components inside an opened view. Cluster membership does not imply a contradicts edge between every node pair.",
			ReadOnly:    true,
		},
		{
			Name:        ToolGetCanonicalContradictionProposal,
			Description: "Read one pending or terminal contradiction proposal with both complete grounded canonical nodes, producer rationale, and any human review decision.",
			ReadOnly:    true,
		},
		{
			Name:        ToolGetCanonicalSupersessionProposal,
			Description: "Read one pending or terminal supersession proposal with its proposal sentence, both complete grounded canonical nodes (from=current, to=replaced), version difference, coverage/limitations, producer metadata, and any human review decision.",
			ReadOnly:    true,
		},
	}
}

// CallTool decodes one read-only query JSON request and encodes its response.
func (s *Server) CallTool(ctx context.Context, name string, payload []byte) ([]byte, error) {
	switch name {
	case ToolGetEvidenceRecord:
		var req GetEvidenceRecordRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetEvidenceRecord(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolListEvidenceRecords:
		var req ListEvidenceRecordsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ListEvidenceRecords(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolSearchEvidenceRecords:
		var req SearchEvidenceRecordsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.SearchEvidenceRecords(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetGroundedEvidenceBrief:
		var req GetGroundedEvidenceBriefRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetGroundedEvidenceBrief(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolListEvidenceNeighbors:
		var req ListEvidenceNeighborsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ListEvidenceNeighbors(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetRelationProvenance:
		var req GetRelationProvenanceRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetRelationProvenance(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetCanonicalContradictionProposal:
		var req GetCanonicalContradictionProposalRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetCanonicalContradictionProposal(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetCanonicalSupersessionProposal:
		var req GetCanonicalSupersessionProposalRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetCanonicalSupersessionProposal(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetMCPReadSourceStates:
		var req GetMCPReadSourceStatesRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetMCPReadSourceStates(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolOpenCanonicalReadView:
		var req OpenCanonicalReadViewRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.OpenCanonicalReadView(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolFindCanonicalPath:
		var req FindCanonicalPathRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.FindCanonicalPath(req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetCanonicalTopologyDiagnostics:
		var req GetCanonicalTopologyDiagnosticsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetCanonicalTopologyDiagnostics(req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	default:
		return nil, &ToolError{Code: toolErrorUnknownTool, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

// GetMCPReadSourceStates returns one bounded immutable source-state projection.
func (s *Server) GetMCPReadSourceStates(
	ctx context.Context,
	req GetMCPReadSourceStatesRequest,
) (GetMCPReadSourceStatesResponse, error) {
	response, err := s.core.QueryMCPReadSourceStates(ctx, req)
	if err != nil {
		return GetMCPReadSourceStatesResponse{}, mapToolError(err)
	}
	return response, nil
}

// GetGroundedEvidenceBrief returns a deterministic evidence-only brief over persisted matches.
func (s *Server) GetGroundedEvidenceBrief(ctx context.Context, req GetGroundedEvidenceBriefRequest) (GroundedEvidenceBriefResponse, error) {
	query := strings.TrimSpace(req.Query)
	if err := validateSearchQuery(query); err != nil {
		return GroundedEvidenceBriefResponse{}, err
	}
	responseSchema := strings.TrimSpace(req.ResponseSchema)
	if responseSchema == "" {
		responseSchema = GroundedEvidenceBriefSchemaV2
	}
	switch responseSchema {
	case GroundedEvidenceBriefSchemaV2,
		GroundedEvidenceBriefSchemaV3,
		GroundedEvidenceBriefSchemaV4,
		GroundedEvidenceBriefSchemaV5:
	default:
		return GroundedEvidenceBriefResponse{}, &ToolError{
			Code: toolErrorInvalidRequest,
			Message: fmt.Sprintf(
				"response_schema must be %s, %s, %s, or %s",
				GroundedEvidenceBriefSchemaV2,
				GroundedEvidenceBriefSchemaV3,
				GroundedEvidenceBriefSchemaV4,
				GroundedEvidenceBriefSchemaV5,
			),
		}
	}
	queryMode := strings.TrimSpace(req.QueryMode)
	switch queryMode {
	case "", evidenceingestion.EvidenceQueryModeExactLexical, evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery:
	default:
		return GroundedEvidenceBriefResponse{}, &ToolError{
			Code:    toolErrorInvalidRequest,
			Message: "query_mode must be exact_lexical or deterministic_lexical_recovery",
		}
	}
	listInput, err := proposalListInput(ListEvidenceRecordsRequest{
		SourceSnapshotID:     req.SourceSnapshotID,
		RepositorySnapshotID: req.RepositorySnapshotID,
		SourceGenerationID:   req.SourceGenerationID,
		SourceID:             req.SourceID,
		SourceVersion:        req.SourceVersion,
		AdmissionOutcome:     req.AdmissionOutcome,
		LifecycleScope:       req.LifecycleScope,
		Limit:                req.Limit,
	})
	if err != nil {
		return GroundedEvidenceBriefResponse{}, err
	}
	result, err := s.core.GetGroundedEvidenceBrief(ctx, evidenceingestion.GroundedEvidenceBriefInput{
		Query:     query,
		QueryMode: queryMode,
		IncludeSourceContext: responseSchema == GroundedEvidenceBriefSchemaV3 ||
			responseSchema == GroundedEvidenceBriefSchemaV4,
		IncludeRepositoryContext: responseSchema ==
			GroundedEvidenceBriefSchemaV4,
		ProposalListInput: listInput,
	})
	if err != nil {
		return GroundedEvidenceBriefResponse{}, mapToolError(err)
	}
	response, err := mapGroundedEvidenceBrief(result, responseSchema)
	if err != nil {
		return GroundedEvidenceBriefResponse{}, &ToolError{
			Code:    toolErrorInternal,
			Message: err.Error(),
			cause:   err,
		}
	}
	return response, nil
}

// GetEvidenceRecord returns one proposal evidence record with lifecycle metadata.
func (s *Server) GetEvidenceRecord(ctx context.Context, req GetEvidenceRecordRequest) (GetEvidenceRecordResponse, error) {
	proposalID := strings.TrimSpace(req.ProposalOccurrenceID)
	canonicalID := strings.TrimSpace(req.CanonicalID)
	if proposalID != "" && canonicalID != "" {
		return GetEvidenceRecordResponse{}, &ToolError{Code: toolErrorInvalidRequest, Message: "exactly one of proposal_occurrence_id or canonical_id is allowed"}
	}
	if canonicalID != "" {
		if err := validateCanonicalID(canonicalID); err != nil {
			return GetEvidenceRecordResponse{}, err
		}
		result, err := s.core.GetCanonicalEvidenceByID(ctx, canonicalID)
		if err != nil {
			return GetEvidenceRecordResponse{}, mapToolError(err)
		}
		return mapCanonicalResult(result), nil
	}
	if err := validateOccurrenceID(proposalID); err != nil {
		return GetEvidenceRecordResponse{}, err
	}
	result, err := s.core.TraceProposalProvenance(ctx, proposalID)
	if err != nil {
		return GetEvidenceRecordResponse{}, mapToolError(err)
	}
	return mapProposalResult(result), nil
}

// ListEvidenceRecords returns bounded proposal records under the requested lifecycle scope.
func (s *Server) ListEvidenceRecords(ctx context.Context, req ListEvidenceRecordsRequest) (ListEvidenceRecordsResponse, error) {
	input, err := proposalListInput(req)
	if err != nil {
		return ListEvidenceRecordsResponse{}, err
	}
	results, err := s.core.ListProposalRecords(ctx, input)
	if err != nil {
		return ListEvidenceRecordsResponse{}, mapToolError(err)
	}
	records := make([]GetEvidenceRecordResponse, 0, len(results))
	for _, result := range results {
		records = append(records, mapProposalResult(result))
	}
	return ListEvidenceRecordsResponse{
		Records:        records,
		Count:          len(records),
		Limit:          input.Limit,
		LifecycleScope: input.LifecycleScope,
	}, nil
}

// SearchEvidenceRecords returns lexical matches only as complete grounded proposal records.
func (s *Server) SearchEvidenceRecords(ctx context.Context, req SearchEvidenceRecordsRequest) (SearchEvidenceRecordsResponse, error) {
	query := strings.TrimSpace(req.Query)
	if err := validateSearchQuery(query); err != nil {
		return SearchEvidenceRecordsResponse{}, err
	}
	listInput, err := proposalListInput(ListEvidenceRecordsRequest{
		SourceSnapshotID:     req.SourceSnapshotID,
		RepositorySnapshotID: req.RepositorySnapshotID,
		SourceGenerationID:   req.SourceGenerationID,
		SourceID:             req.SourceID,
		SourceVersion:        req.SourceVersion,
		AdmissionOutcome:     req.AdmissionOutcome,
		LifecycleScope:       req.LifecycleScope,
		Limit:                req.Limit,
	})
	if err != nil {
		return SearchEvidenceRecordsResponse{}, err
	}
	result, err := s.core.SearchProposalRecordsPage(ctx, evidenceingestion.ProposalSearchInput{
		Query:             query,
		ProposalListInput: listInput,
	})
	if err != nil {
		return SearchEvidenceRecordsResponse{}, mapToolError(err)
	}
	matches := make([]GroundedSearchMatch, 0, len(result.Matches))
	for _, match := range result.Matches {
		matches = append(matches, GroundedSearchMatch{Rank: match.Rank, Record: mapProposalResult(match.Record)})
	}
	return SearchEvidenceRecordsResponse{
		SchemaVersion:  evidenceSearchSchemaV2,
		Query:          result.Execution.OriginalQuery,
		QueryExecution: mapGroundedEvidenceQueryExecution(result.Execution),
		Matches:        matches,
		Count:          len(matches),
		Limit:          result.Execution.Limit,
		LifecycleScope: result.Execution.Filters.LifecycleScope,
	}, nil
}

// GetRelationProvenance returns one exact relation without accepting a graph-layer selector.
func (s *Server) GetRelationProvenance(ctx context.Context, req GetRelationProvenanceRequest) (RelationProvenanceResponse, error) {
	proposalID := strings.TrimSpace(req.ProposalOccurrenceID)
	edgeID := strings.TrimSpace(req.CanonicalEdgeID)
	if proposalID != "" && edgeID != "" {
		return RelationProvenanceResponse{}, &ToolError{Code: toolErrorInvalidRequest, Message: "exactly one of proposal_occurrence_id or canonical_edge_id is allowed"}
	}
	if edgeID != "" {
		if err := validateCanonicalEdgeID(edgeID); err != nil {
			return RelationProvenanceResponse{}, err
		}
		result, err := s.core.GetCanonicalRelationByID(ctx, edgeID)
		if err != nil {
			return RelationProvenanceResponse{}, mapToolError(err)
		}
		return mapCanonicalRelationProvenance(result), nil
	}
	if err := validateOccurrenceID(proposalID); err != nil {
		return RelationProvenanceResponse{}, err
	}
	result, err := s.core.TraceProposalProvenance(ctx, proposalID)
	if err != nil {
		return RelationProvenanceResponse{}, mapToolError(err)
	}
	if result.CodeRelation == nil {
		return RelationProvenanceResponse{}, &ToolError{Code: toolErrorInvalidRelation, Message: fmt.Sprintf("proposal occurrence %s is not a typed relation", proposalID)}
	}
	return mapRepositoryRelationProvenance(result), nil
}

// GetCanonicalContradictionProposal returns the complete review card for one proposal.
func (s *Server) GetCanonicalContradictionProposal(ctx context.Context, req GetCanonicalContradictionProposalRequest) (CanonicalContradictionProposalResponse, error) {
	proposalID := strings.TrimSpace(req.CanonicalContradictionProposalID)
	if !strings.HasPrefix(proposalID, evidenceingestion.CanonicalContradictionProposalIDPrefix) {
		return CanonicalContradictionProposalResponse{}, &ToolError{
			Code:    toolErrorInvalidRecordID,
			Message: fmt.Sprintf("canonical_contradiction_proposal_id %q must start with %s", proposalID, evidenceingestion.CanonicalContradictionProposalIDPrefix),
		}
	}
	result, err := s.core.GetCanonicalContradictionProposal(ctx, proposalID)
	if err != nil {
		return CanonicalContradictionProposalResponse{}, mapToolError(err)
	}
	return mapCanonicalContradictionProposal(result), nil
}

// GetCanonicalSupersessionProposal returns the complete review card for one directed proposal.
func (s *Server) GetCanonicalSupersessionProposal(ctx context.Context, req GetCanonicalSupersessionProposalRequest) (CanonicalSupersessionProposalResponse, error) {
	proposalID := strings.TrimSpace(req.CanonicalSupersessionProposalID)
	if !strings.HasPrefix(proposalID, evidenceingestion.CanonicalSupersessionProposalIDPrefix) {
		return CanonicalSupersessionProposalResponse{}, &ToolError{
			Code:    toolErrorInvalidRecordID,
			Message: fmt.Sprintf("canonical_supersession_proposal_id %q must start with %s", proposalID, evidenceingestion.CanonicalSupersessionProposalIDPrefix),
		}
	}
	result, err := s.core.GetCanonicalSupersessionProposal(ctx, proposalID)
	if err != nil {
		return CanonicalSupersessionProposalResponse{}, mapToolError(err)
	}
	return mapCanonicalSupersessionProposal(result), nil
}

// ListEvidenceNeighbors returns a bounded one-hop neighborhood on the surface implied by the root ID.
func (s *Server) ListEvidenceNeighbors(ctx context.Context, req ListEvidenceNeighborsRequest) (ListEvidenceNeighborsResponse, error) {
	canonicalID := strings.TrimSpace(req.CanonicalID)
	symbolRef := strings.TrimSpace(req.SymbolRef)
	if (canonicalID == "") == (symbolRef == "") {
		return ListEvidenceNeighborsResponse{}, &ToolError{Code: toolErrorInvalidRequest, Message: "exactly one of canonical_id or symbol_ref is required"}
	}
	direction, limit, err := normalizeNeighborBounds(req.Direction, req.Limit)
	if err != nil {
		return ListEvidenceNeighborsResponse{}, err
	}
	relation := strings.TrimSpace(req.Relation)
	if canonicalID != "" {
		if err := validateCanonicalID(canonicalID); err != nil {
			return ListEvidenceNeighborsResponse{}, err
		}
		if strings.TrimSpace(req.LifecycleScope) != "" || strings.TrimSpace(req.SourceGenerationID) != "" {
			return ListEvidenceNeighborsResponse{}, &ToolError{Code: toolErrorInvalidRequest, Message: "canonical neighbors do not accept repository lifecycle filters"}
		}
		results, err := s.core.ListCanonicalNeighbors(ctx, evidenceingestion.CanonicalNeighborInput{
			CanonicalID: canonicalID,
			Direction:   direction,
			Relation:    relation,
			Limit:       limit,
		})
		if err != nil {
			return ListEvidenceNeighborsResponse{}, mapToolError(err)
		}
		neighbors := make([]EvidenceNeighborResponse, 0, len(results))
		for _, result := range results {
			adjacent := mapCanonicalResult(result.Neighbor)
			neighbors = append(neighbors, EvidenceNeighborResponse{
				Directions:        append([]string(nil), result.Directions...),
				Relation:          mapCanonicalRelationProvenance(result.Relation),
				AdjacentCanonical: &adjacent,
			})
		}
		return ListEvidenceNeighborsResponse{
			RootRef:   RecordRef{Kind: "canonical_evidence", ID: canonicalID},
			Depth:     1,
			Neighbors: neighbors,
			Count:     len(neighbors),
			Limit:     limit,
		}, nil
	}

	if err := validateSymbolRef(symbolRef); err != nil {
		return ListEvidenceNeighborsResponse{}, err
	}
	lifecycleScope, err := normalizeLifecycleScope(req.LifecycleScope, req.SourceGenerationID)
	if err != nil {
		return ListEvidenceNeighborsResponse{}, err
	}
	generationID := strings.TrimSpace(req.SourceGenerationID)
	if generationID != "" && !strings.HasPrefix(generationID, "generation:") {
		return ListEvidenceNeighborsResponse{}, &ToolError{Code: toolErrorInvalidRecordID, Message: "source_generation_id must start with generation:"}
	}
	if relation != "" && relation != evidenceingestion.CodeRelationKindDefinition && relation != evidenceingestion.CodeRelationKindCall {
		return ListEvidenceNeighborsResponse{}, &ToolError{Code: toolErrorInvalidRequest, Message: "repository relation must be definition or call"}
	}
	results, err := s.core.ListRepositoryRelationNeighbors(ctx, evidenceingestion.RepositoryRelationNeighborInput{
		SymbolRef:          symbolRef,
		Direction:          direction,
		RelationKind:       relation,
		LifecycleScope:     lifecycleScope,
		SourceGenerationID: generationID,
		Limit:              limit,
	})
	if err != nil {
		return ListEvidenceNeighborsResponse{}, mapToolError(err)
	}
	neighbors := make([]EvidenceNeighborResponse, 0, len(results))
	for _, result := range results {
		neighbors = append(neighbors, EvidenceNeighborResponse{
			Directions:          append([]string(nil), result.Directions...),
			Relation:            mapRepositoryRelationProvenance(result.Relation),
			AdjacentDeclaration: cloneDeclaration(result.NeighborDeclaration),
			AdjacentUsage:       cloneUsage(result.NeighborUsage),
		})
	}
	return ListEvidenceNeighborsResponse{
		RootRef:        RecordRef{Kind: "code_symbol", ID: symbolRef},
		Depth:          1,
		Neighbors:      neighbors,
		Count:          len(neighbors),
		Limit:          limit,
		LifecycleScope: lifecycleScope,
	}, nil
}

type postgresCore struct {
	pool *pgxpool.Pool
}

func (c postgresCore) ReadCanonicalGraphView(ctx context.Context, input evidenceingestion.CanonicalReadInput) (evidenceingestion.CanonicalReadView, error) {
	return evidenceingestion.ReadCanonicalGraphView(ctx, c.pool, input)
}

func (c postgresCore) GetCanonicalRelationByID(ctx context.Context, canonicalEdgeID string) (evidenceingestion.CanonicalRelationQueryResult, error) {
	return evidenceingestion.GetCanonicalRelationByID(ctx, c.pool, canonicalEdgeID)
}

func (c postgresCore) GetCanonicalContradictionProposal(ctx context.Context, proposalID string) (evidenceingestion.CanonicalContradictionQueryResult, error) {
	return evidenceingestion.GetCanonicalContradictionProposal(ctx, c.pool, proposalID)
}

func (c postgresCore) GetCanonicalSupersessionProposal(ctx context.Context, proposalID string) (evidenceingestion.CanonicalSupersessionQueryResult, error) {
	return evidenceingestion.GetCanonicalSupersessionProposal(ctx, c.pool, proposalID)
}

func (c postgresCore) TraceProposalProvenance(ctx context.Context, occurrenceID string) (evidenceingestion.ProposalQueryResult, error) {
	return evidenceingestion.TraceProposalProvenance(ctx, c.pool, occurrenceID)
}

func (c postgresCore) GetCanonicalEvidenceByID(ctx context.Context, canonicalID string) (evidenceingestion.CanonicalQueryResult, error) {
	return evidenceingestion.GetCanonicalEvidenceByID(ctx, c.pool, canonicalID)
}

func (c postgresCore) ListProposalRecords(ctx context.Context, input evidenceingestion.ProposalListInput) ([]evidenceingestion.ProposalQueryResult, error) {
	return evidenceingestion.ListProposalRecords(ctx, c.pool, input)
}

func (c postgresCore) SearchProposalRecordsPage(ctx context.Context, input evidenceingestion.ProposalSearchInput) (evidenceingestion.ProposalSearchQueryResult, error) {
	return evidenceingestion.SearchProposalRecordsPage(ctx, c.pool, input)
}

func (c postgresCore) GetGroundedEvidenceBrief(ctx context.Context, input evidenceingestion.GroundedEvidenceBriefInput) (evidenceingestion.GroundedEvidenceBriefQueryResult, error) {
	return evidenceingestion.GetGroundedEvidenceBrief(ctx, c.pool, input)
}

func (c postgresCore) ListCanonicalNeighbors(ctx context.Context, input evidenceingestion.CanonicalNeighborInput) ([]evidenceingestion.CanonicalNeighborResult, error) {
	return evidenceingestion.ListCanonicalNeighbors(ctx, c.pool, input)
}

func (c postgresCore) ListRepositoryRelationNeighbors(ctx context.Context, input evidenceingestion.RepositoryRelationNeighborInput) ([]evidenceingestion.RepositoryRelationNeighborResult, error) {
	return evidenceingestion.ListRepositoryRelationNeighbors(ctx, c.pool, input)
}

func (c postgresCore) QueryMCPReadSourceStates(ctx context.Context, input evidenceingestion.MCPReadSourceStateQueryInput) (evidenceingestion.MCPReadSourceStateQueryResult, error) {
	return evidenceingestion.QueryMCPReadSourceStates(ctx, c.pool, input)
}

func decodeStrict(payload []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

func validateOccurrenceID(id string) error {
	if strings.TrimSpace(id) == "" {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "proposal_occurrence_id is required"}
	}
	if !strings.HasPrefix(id, "occ:") {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "proposal_occurrence_id must start with occ:"}
	}
	return nil
}

func validateCanonicalID(id string) error {
	if strings.TrimSpace(id) == "" {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "canonical_id is required"}
	}
	if !strings.HasPrefix(id, "canon-node:") {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "canonical_id must start with canon-node:"}
	}
	return nil
}

func validateCanonicalEdgeID(id string) error {
	if strings.TrimSpace(id) == "" {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "canonical_edge_id is required"}
	}
	if !strings.HasPrefix(id, "canon-edge:") {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "canonical_edge_id must start with canon-edge:"}
	}
	return nil
}

func validateSymbolRef(symbolRef string) error {
	if !strings.HasPrefix(symbolRef, "symbol:") || utf8.RuneCountInString(symbolRef) > 2048 {
		return &ToolError{Code: toolErrorInvalidRecordID, Message: "symbol_ref must start with symbol: and contain at most 2048 UTF-8 characters"}
	}
	return nil
}

func validateSearchQuery(query string) error {
	if query == "" {
		return &ToolError{Code: toolErrorInvalidRequest, Message: "query is required"}
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > 256 {
		return &ToolError{Code: toolErrorInvalidRequest, Message: "query must contain at most 256 UTF-8 characters"}
	}
	if len(strings.Fields(query)) > 16 {
		return &ToolError{Code: toolErrorInvalidRequest, Message: "query must contain at most 16 terms"}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return nil
		}
	}
	return &ToolError{Code: toolErrorInvalidRequest, Message: "query must contain at least one letter or number"}
}

func normalizeNeighborBounds(direction string, limit int) (string, int, error) {
	direction = strings.TrimSpace(direction)
	if direction == "" {
		direction = evidenceingestion.RelationDirectionBoth
	}
	switch direction {
	case evidenceingestion.RelationDirectionIncoming, evidenceingestion.RelationDirectionOutgoing, evidenceingestion.RelationDirectionBoth:
	default:
		return "", 0, &ToolError{Code: toolErrorInvalidRequest, Message: "direction must be incoming, outgoing, or both"}
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 0 {
		return "", 0, &ToolError{Code: toolErrorInvalidRequest, Message: "limit must be non-negative"}
	}
	if limit > 100 {
		return "", 0, &ToolError{Code: toolErrorInvalidRequest, Message: "limit must be at most 100"}
	}
	return direction, limit, nil
}

func normalizeLifecycleScope(scope, sourceGenerationID string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = evidenceingestion.ProposalLifecycleScopeActive
		if strings.TrimSpace(sourceGenerationID) != "" {
			scope = evidenceingestion.ProposalLifecycleScopeAll
		}
	}
	switch scope {
	case evidenceingestion.ProposalLifecycleScopeActive, evidenceingestion.ProposalLifecycleScopeHistorical, evidenceingestion.ProposalLifecycleScopeAll:
		return scope, nil
	default:
		return "", &ToolError{Code: toolErrorInvalidRequest, Message: "lifecycle_scope must be active, historical, or all"}
	}
}

func proposalListInput(req ListEvidenceRecordsRequest) (evidenceingestion.ProposalListInput, error) {
	input := evidenceingestion.ProposalListInput{
		SourceSnapshotID:     strings.TrimSpace(req.SourceSnapshotID),
		RepositorySnapshotID: strings.TrimSpace(req.RepositorySnapshotID),
		SourceGenerationID:   strings.TrimSpace(req.SourceGenerationID),
		SourceID:             strings.TrimSpace(req.SourceID),
		SourceVersion:        strings.TrimSpace(req.SourceVersion),
		AdmissionOutcome:     strings.TrimSpace(req.AdmissionOutcome),
		LifecycleScope:       strings.TrimSpace(req.LifecycleScope),
		Limit:                req.Limit,
	}
	var err error
	input.LifecycleScope, err = normalizeLifecycleScope(input.LifecycleScope, input.SourceGenerationID)
	if err != nil {
		return evidenceingestion.ProposalListInput{}, err
	}
	if input.SourceSnapshotID != "" && !strings.HasPrefix(input.SourceSnapshotID, "srcsnap:") {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRecordID, Message: "source_snapshot_id must start with srcsnap:"}
	}
	if input.RepositorySnapshotID != "" && !strings.HasPrefix(input.RepositorySnapshotID, "repo-snapshot:") {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRecordID, Message: "repository_snapshot_id must start with repo-snapshot:"}
	}
	if input.SourceGenerationID != "" && !strings.HasPrefix(input.SourceGenerationID, "generation:") {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRecordID, Message: "source_generation_id must start with generation:"}
	}
	if input.SourceSnapshotID != "" && input.RepositorySnapshotID != "" {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRequest, Message: "source_snapshot_id and repository_snapshot_id are mutually exclusive"}
	}
	if input.SourceGenerationID != "" && (input.SourceSnapshotID != "" || input.SourceID != "" || input.SourceVersion != "") {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRequest, Message: "source_generation_id cannot be combined with source snapshot or source identity filters"}
	}
	if input.Limit == 0 {
		input.Limit = 20
	}
	if input.Limit < 0 {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRequest, Message: "limit must be non-negative"}
	}
	if input.Limit > 100 {
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRequest, Message: "limit must be at most 100"}
	}
	switch input.AdmissionOutcome {
	case "", "pending", "rejected", "audit_only", "admitted":
		return input, nil
	default:
		return evidenceingestion.ProposalListInput{}, &ToolError{Code: toolErrorInvalidRequest, Message: "admission_outcome must be pending, rejected, audit_only, or admitted"}
	}
}

func mapProposalResult(result evidenceingestion.ProposalQueryResult) GetEvidenceRecordResponse {
	var canonicalRef *string
	if result.CanonicalRef != "" {
		value := result.CanonicalRef
		canonicalRef = &value
	}
	return GetEvidenceRecordResponse{
		RecordRef: RecordRef{
			Kind: "proposal",
			ID:   result.ProposalOccurrenceID,
		},
		ProposalFingerprint:        result.ProposalFingerprint,
		ProposalLocalID:            result.ProposalLocalID,
		ProposalFingerprintVersion: result.ProposalFingerprintVersion,
		ProposalKind:               result.ProposalKind,
		StatementText:              result.StatementText,
		AdmissionOutcome:           result.AdmissionOutcome,
		CanonicalRef:               canonicalRef,
		Source:                     mapSourceInfo(result),
		SourceRefs:                 append([]evidenceingestion.ResolvedSourceRef(nil), result.SourceRefs...),
		CodeFact:                   cloneCodeFact(result.CodeFact),
		CodeRelation:               cloneCodeRelation(result.CodeRelation),
		SourceGeneration:           mapSourceGeneration(result),
		RepositoryLifecycle:        mapRepositoryLifecycle(result),
		Extractor: ExtractorInfo{
			ExtractorDefinitionID: result.ExtractorDefinitionID,
			ExtractionRunID:       result.ExtractionRunID,
			ExtractionAttemptID:   result.ExtractionAttemptID,
			ProducerSessionRef:    result.ProducerSessionRef,
			Name:                  result.ExtractorName,
			Version:               result.ExtractorVersion,
			ConfigHash:            result.ExtractorConfigHash,
			AttemptStatus:         result.ExtractionAttemptStatus,
		},
		ExtractionViewID:    result.ExtractionViewID,
		RendererName:        result.RendererName,
		RendererVersion:     result.RendererVersion,
		RenderedContentHash: result.RenderedContentHash,
	}
}

func mapCanonicalResult(result evidenceingestion.CanonicalQueryResult) GetEvidenceRecordResponse {
	origin := result.OriginProposal
	canonicalRef := result.CanonicalID
	return GetEvidenceRecordResponse{
		RecordRef: RecordRef{
			Kind: "canonical_evidence",
			ID:   result.CanonicalID,
		},
		ProposalLocalID:            origin.ProposalLocalID,
		ProposalFingerprint:        origin.ProposalFingerprint,
		ProposalFingerprintVersion: origin.ProposalFingerprintVersion,
		ProposalKind:               origin.ProposalKind,
		StatementText:              result.Payload.Claim,
		AdmissionOutcome:           "admitted",
		CanonicalRef:               &canonicalRef,
		ProposalOriginRef: &RecordRef{
			Kind: "proposal",
			ID:   result.OriginProposalOccurrenceID,
		},
		Canonical: &CanonicalInfo{
			NodeKind:   string(result.NodeKind),
			Payload:    result.Payload,
			Provenance: result.Provenance,
			Temporal:   result.Temporal,
			Integrity:  result.Integrity,
		},
		Source:              mapSourceInfo(origin),
		SourceRefs:          append([]evidenceingestion.ResolvedSourceRef(nil), origin.SourceRefs...),
		CodeFact:            cloneCodeFact(origin.CodeFact),
		CodeRelation:        cloneCodeRelation(origin.CodeRelation),
		SourceGeneration:    mapSourceGeneration(origin),
		RepositoryLifecycle: mapRepositoryLifecycle(origin),
		Extractor: ExtractorInfo{
			ExtractorDefinitionID: origin.ExtractorDefinitionID,
			ExtractionRunID:       origin.ExtractionRunID,
			ExtractionAttemptID:   origin.ExtractionAttemptID,
			ProducerSessionRef:    origin.ProducerSessionRef,
			Name:                  origin.ExtractorName,
			Version:               origin.ExtractorVersion,
			ConfigHash:            origin.ExtractorConfigHash,
			AttemptStatus:         origin.ExtractionAttemptStatus,
		},
		ExtractionViewID:    origin.ExtractionViewID,
		RendererName:        origin.RendererName,
		RendererVersion:     origin.RendererVersion,
		RenderedContentHash: origin.RenderedContentHash,
	}
}

func mapGroundedEvidenceBrief(
	result evidenceingestion.GroundedEvidenceBriefQueryResult,
	responseSchema string,
) (GroundedEvidenceBriefResponse, error) {
	queryExecution := mapGroundedEvidenceQueryExecution(result.Execution)
	response := GroundedEvidenceBriefResponse{
		SchemaVersion:  responseSchema,
		Query:          result.Execution.OriginalQuery,
		QueryExecution: queryExecution,
		Boundary: GroundedEvidenceBriefBoundary{
			SearchSurface:  result.Execution.SearchSurface,
			QueryMode:      result.Execution.QueryMode,
			LifecycleScope: result.Execution.Filters.LifecycleScope,
			Limit:          result.Execution.Limit,
			Truncated:      result.Execution.Truncated,
		},
		Matches:          make([]GroundedEvidenceBriefMatch, 0, len(result.Matches)),
		SourceScopes:     []GroundedEvidenceBriefSourceScope{},
		Coverage:         []GroundedEvidenceBriefCoverage{},
		FollowUps:        []GroundedEvidenceBriefFollowUp{},
		ObservationCodes: []string{},
		Limitations:      groundedEvidenceBriefLimitations(result.Execution),
	}

	sourceScopes := make(map[string]GroundedEvidenceBriefSourceScope)
	recordsByAttempt := make(map[string]evidenceingestion.ProposalQueryResult)
	followUps := make(map[string]GroundedEvidenceBriefFollowUp)
	sourceContexts, err := indexGroundedEvidenceSourceContexts(result, responseSchema)
	if err != nil {
		return GroundedEvidenceBriefResponse{}, err
	}
	repositoryContexts, err := indexGroundedEvidenceRepositoryContexts(
		result,
		responseSchema,
	)
	if err != nil {
		return GroundedEvidenceBriefResponse{}, err
	}
	for _, match := range result.Matches {
		record := match.Record
		recordsByAttempt[record.ExtractionAttemptID] = record
		scope := briefSourceScope(record)
		sourceScopes[scope.ScopeRef.Kind+":"+scope.ScopeRef.ID] = scope

		mapped := GroundedEvidenceBriefMatch{
			Rank:             match.Rank,
			RecordRef:        RecordRef{Kind: "proposal", ID: record.ProposalOccurrenceID},
			ProposalLocalID:  record.ProposalLocalID,
			StatementText:    record.StatementText,
			ProposalKind:     record.ProposalKind,
			AdmissionOutcome: record.AdmissionOutcome,
			SourceScopeRef:   scope.ScopeRef,
			SourceRefs:       append([]evidenceingestion.ResolvedSourceRef(nil), record.SourceRefs...),
			SymbolRefs:       briefSymbolRefs(record),
		}
		if responseSchema == GroundedEvidenceBriefSchemaV3 ||
			responseSchema == GroundedEvidenceBriefSchemaV4 {
			contextSchema := groundedEvidenceBriefContextSchemaLabel(
				responseSchema,
			)
			sourceContext, ok := sourceContexts[record.ProposalOccurrenceID]
			if !ok {
				return GroundedEvidenceBriefResponse{}, fmt.Errorf(
					"grounded evidence brief %s has no source context for proposal occurrence %s",
					contextSchema,
					record.ProposalOccurrenceID,
				)
			}
			if err := validateGroundedEvidenceSourceContext(record, sourceContext); err != nil {
				return GroundedEvidenceBriefResponse{}, err
			}
			delete(sourceContexts, record.ProposalOccurrenceID)
			if responseSchema == GroundedEvidenceBriefSchemaV3 ||
				record.SourceBindingKind !=
					evidenceingestion.ProposalSourceBindingRepositorySnapshot {
				mapped.SourceContext = &sourceContext
			} else if sourceContext.Status !=
				evidenceingestion.GroundedEvidenceSourceContextStatusNotApplicable {
				return GroundedEvidenceBriefResponse{}, fmt.Errorf(
					"proposal occurrence %s repository source context status is %s",
					record.ProposalOccurrenceID,
					sourceContext.Status,
				)
			}
		}
		if responseSchema == GroundedEvidenceBriefSchemaV4 &&
			record.SourceBindingKind ==
				evidenceingestion.ProposalSourceBindingRepositorySnapshot {
			repositoryContext, ok := repositoryContexts[record.ProposalOccurrenceID]
			if !ok {
				return GroundedEvidenceBriefResponse{}, fmt.Errorf(
					"grounded evidence brief v4 has no repository context for proposal occurrence %s",
					record.ProposalOccurrenceID,
				)
			}
			if err := validateGroundedEvidenceRepositoryContext(
				record,
				repositoryContext,
			); err != nil {
				return GroundedEvidenceBriefResponse{}, err
			}
			delete(repositoryContexts, record.ProposalOccurrenceID)
			mapped.RepositoryContext = &repositoryContext
		}
		if record.CanonicalRef != "" {
			ref := RecordRef{Kind: "canonical_evidence", ID: record.CanonicalRef}
			mapped.CanonicalRef = &ref
		}
		if record.CodeRelation != nil {
			ref := RecordRef{Kind: "proposal_relation", ID: record.ProposalOccurrenceID}
			mapped.RelationRef = &ref
		}
		if responseSchema == GroundedEvidenceBriefSchemaV5 {
			recordState, err := groundedEvidenceBriefRecordState(record)
			if err != nil {
				return GroundedEvidenceBriefResponse{}, err
			}
			mapped.RecordState = &recordState
		}
		response.Matches = append(response.Matches, mapped)
		incrementBriefCounts(&response.Counts, record)
		addBriefFollowUps(followUps, record, mapped.SymbolRefs)
	}
	response.Counts.ReturnedMatches = len(response.Matches)

	scopeKeys := sortedMapKeys(sourceScopes)
	for _, key := range scopeKeys {
		response.SourceScopes = append(response.SourceScopes, sourceScopes[key])
	}
	for _, item := range result.Coverage {
		record := recordsByAttempt[item.ExtractionAttemptID]
		coverage := GroundedEvidenceBriefCoverage{
			ExtractionAttemptRef: RecordRef{Kind: "extraction_attempt", ID: item.ExtractionAttemptID},
			Diagnostic:           item.RepositoryGoplsCoverage,
		}
		if record.RepositorySnapshot != nil {
			ref := RecordRef{Kind: "repository_snapshot", ID: record.RepositorySnapshot.ID}
			coverage.RepositorySnapshotRef = &ref
		}
		if record.SourceGeneration != nil {
			ref := RecordRef{Kind: "source_generation", ID: record.SourceGeneration.ID}
			coverage.SourceGenerationRef = &ref
		}
		response.Coverage = append(response.Coverage, coverage)
	}

	followUpKeys := sortedMapKeys(followUps)
	for _, key := range followUpKeys {
		response.FollowUps = append(response.FollowUps, followUps[key])
	}
	response.ObservationCodes = append(response.ObservationCodes, groundedEvidenceBriefObservationCodes(result.Execution)...)
	if responseSchema == GroundedEvidenceBriefSchemaV4 {
		response.ObservationCodes = append(
			response.ObservationCodes,
			groundedEvidenceRepositoryCoverageObservationCodes(result.Coverage)...,
		)
	}
	if responseSchema == GroundedEvidenceBriefSchemaV5 {
		response.Limitations = append(
			response.Limitations,
			"external_source_freshness_not_evaluated",
		)
	}
	if result.Execution.Truncated {
		response.ObservationCodes = append(response.ObservationCodes, "bounded_result_truncated")
	}
	if len(sourceContexts) != 0 {
		return GroundedEvidenceBriefResponse{}, fmt.Errorf(
			"grounded evidence brief %s has %d unmatched source contexts",
			groundedEvidenceBriefContextSchemaLabel(responseSchema),
			len(sourceContexts),
		)
	}
	if len(repositoryContexts) != 0 {
		return GroundedEvidenceBriefResponse{}, fmt.Errorf(
			"grounded evidence brief v4 has %d unmatched repository contexts",
			len(repositoryContexts),
		)
	}
	return response, nil
}

func groundedEvidenceBriefRecordState(
	record evidenceingestion.ProposalQueryResult,
) (GroundedEvidenceBriefRecordState, error) {
	state := GroundedEvidenceBriefRecordState{
		SourceBindingKind:       record.SourceBindingKind,
		ExternalFreshnessStatus: groundedEvidenceExternalFreshnessNotEvaluated,
		ExternalFreshnessBasis:  groundedEvidenceExternalFreshnessNoComparison,
	}

	switch record.AdmissionOutcome {
	case "pending":
		state.AuthorityStatus = groundedEvidenceAuthorityProposalPending
	case "rejected":
		state.AuthorityStatus = groundedEvidenceAuthorityProposalRejected
	case "audit_only":
		state.AuthorityStatus = groundedEvidenceAuthorityProposalAuditOnly
	case "admitted":
		state.AuthorityStatus = groundedEvidenceAuthorityCanonicalAdmitted
	default:
		return GroundedEvidenceBriefRecordState{}, fmt.Errorf(
			"proposal occurrence %s has unsupported admission outcome %q",
			record.ProposalOccurrenceID,
			record.AdmissionOutcome,
		)
	}
	if record.AdmissionOutcome == "admitted" && record.CanonicalRef == "" {
		return GroundedEvidenceBriefRecordState{}, fmt.Errorf(
			"admitted proposal occurrence %s has no canonical reference",
			record.ProposalOccurrenceID,
		)
	}
	if record.AdmissionOutcome != "admitted" && record.CanonicalRef != "" {
		return GroundedEvidenceBriefRecordState{}, fmt.Errorf(
			"non-admitted proposal occurrence %s has canonical reference %s",
			record.ProposalOccurrenceID,
			record.CanonicalRef,
		)
	}

	switch record.SourceBindingKind {
	case evidenceingestion.ProposalSourceBindingSourceSnapshot:
		if record.SourceSnapshotID == "" ||
			record.SourceVersion == "" ||
			record.RepositorySnapshot != nil ||
			record.SourceGeneration != nil {
			return GroundedEvidenceBriefRecordState{}, fmt.Errorf(
				"proposal occurrence %s has inconsistent source-snapshot state",
				record.ProposalOccurrenceID,
			)
		}
		state.RecordLifecycle = groundedEvidenceRecordLifecycleSourceSnapshot
		state.RevisionKind = groundedEvidenceRevisionSourceVersion
		state.Revision = record.SourceVersion
	case evidenceingestion.ProposalSourceBindingRepositorySnapshot:
		if record.RepositorySnapshot == nil ||
			record.RepositorySnapshot.CommitSHA == "" ||
			record.SourceGeneration == nil ||
			record.SourceGeneration.ID == "" ||
			record.SourceGeneration.RepoID != record.RepositorySnapshot.RepoID ||
			record.SourceGeneration.RepositorySnapshotID != record.RepositorySnapshot.ID ||
			record.SourceGeneration.CommitSHA != record.RepositorySnapshot.CommitSHA ||
			record.SourceSnapshotID != "" {
			return GroundedEvidenceBriefRecordState{}, fmt.Errorf(
				"proposal occurrence %s has inconsistent repository state",
				record.ProposalOccurrenceID,
			)
		}
		if record.SourceGenerationActive {
			state.RecordLifecycle = groundedEvidenceRecordLifecycleRepositoryActive
		} else {
			state.RecordLifecycle = groundedEvidenceRecordLifecycleRepositoryHistorical
		}
		state.RevisionKind = groundedEvidenceRevisionGitCommit
		state.Revision = record.RepositorySnapshot.CommitSHA
	default:
		return GroundedEvidenceBriefRecordState{}, fmt.Errorf(
			"proposal occurrence %s has unsupported source binding kind %q",
			record.ProposalOccurrenceID,
			record.SourceBindingKind,
		)
	}
	return state, nil
}

func indexGroundedEvidenceSourceContexts(
	result evidenceingestion.GroundedEvidenceBriefQueryResult,
	responseSchema string,
) (map[string]evidenceingestion.GroundedEvidenceSourceContext, error) {
	indexed := make(map[string]evidenceingestion.GroundedEvidenceSourceContext)
	if responseSchema != GroundedEvidenceBriefSchemaV3 &&
		responseSchema != GroundedEvidenceBriefSchemaV4 {
		return indexed, nil
	}
	contextSchema := groundedEvidenceBriefContextSchemaLabel(responseSchema)
	if len(result.SourceContexts) != len(result.Matches) {
		return nil, fmt.Errorf(
			"grounded evidence brief %s source context count %d does not match result count %d",
			contextSchema,
			len(result.SourceContexts),
			len(result.Matches),
		)
	}
	for _, sourceContext := range result.SourceContexts {
		if sourceContext.ProposalOccurrenceID == "" {
			return nil, fmt.Errorf(
				"grounded evidence brief %s source context has no proposal occurrence ID",
				contextSchema,
			)
		}
		if _, duplicate := indexed[sourceContext.ProposalOccurrenceID]; duplicate {
			return nil, fmt.Errorf(
				"grounded evidence brief %s repeats source context for proposal occurrence %s",
				contextSchema,
				sourceContext.ProposalOccurrenceID,
			)
		}
		indexed[sourceContext.ProposalOccurrenceID] = sourceContext
	}
	return indexed, nil
}

func groundedEvidenceBriefContextSchemaLabel(responseSchema string) string {
	if responseSchema == GroundedEvidenceBriefSchemaV3 {
		return "v3"
	}
	return "v4"
}

func indexGroundedEvidenceRepositoryContexts(
	result evidenceingestion.GroundedEvidenceBriefQueryResult,
	responseSchema string,
) (map[string]evidenceingestion.GroundedEvidenceRepositoryContext, error) {
	indexed := make(
		map[string]evidenceingestion.GroundedEvidenceRepositoryContext,
	)
	if responseSchema != GroundedEvidenceBriefSchemaV4 {
		return indexed, nil
	}
	expected := 0
	for _, match := range result.Matches {
		if match.Record.SourceBindingKind ==
			evidenceingestion.ProposalSourceBindingRepositorySnapshot {
			expected++
		}
	}
	if len(result.RepositoryContexts) != expected {
		return nil, fmt.Errorf(
			"grounded evidence brief v4 repository context count %d does not match repository result count %d",
			len(result.RepositoryContexts),
			expected,
		)
	}
	for _, repositoryContext := range result.RepositoryContexts {
		if repositoryContext.ProposalOccurrenceID == "" {
			return nil, errors.New(
				"grounded evidence brief v4 repository context has no proposal occurrence ID",
			)
		}
		if _, duplicate := indexed[repositoryContext.ProposalOccurrenceID]; duplicate {
			return nil, fmt.Errorf(
				"grounded evidence brief v4 repeats repository context for proposal occurrence %s",
				repositoryContext.ProposalOccurrenceID,
			)
		}
		indexed[repositoryContext.ProposalOccurrenceID] = repositoryContext
	}
	return indexed, nil
}

func validateGroundedEvidenceSourceContext(
	record evidenceingestion.ProposalQueryResult,
	sourceContext evidenceingestion.GroundedEvidenceSourceContext,
) error {
	if sourceContext.Contract != evidenceingestion.GroundedEvidenceSourceContextContractV1 {
		return fmt.Errorf(
			"proposal occurrence %s has unsupported source context contract %q",
			record.ProposalOccurrenceID,
			sourceContext.Contract,
		)
	}
	if sourceContext.SearchParticipation {
		return fmt.Errorf(
			"proposal occurrence %s source context participates in search",
			record.ProposalOccurrenceID,
		)
	}
	if !slices.Equal(sourceContext.SearchCore, record.SourceRefs) {
		return fmt.Errorf(
			"proposal occurrence %s source context search core differs from persisted source refs",
			record.ProposalOccurrenceID,
		)
	}
	if sourceContext.SourceView != nil {
		if sourceContext.SourceView.Contract != evidenceingestion.BoundedSourceViewContractV1 ||
			sourceContext.SourceView.Observation.SourceSnapshotID !=
				record.SourceSnapshotID ||
			sourceContext.SourceView.Observation.ExtractionViewID !=
				record.ExtractionViewID {
			return fmt.Errorf(
				"proposal occurrence %s source context view identity differs from persisted source identity",
				record.ProposalOccurrenceID,
			)
		}
	}
	switch sourceContext.Status {
	case evidenceingestion.GroundedEvidenceSourceContextStatusAvailable:
		if record.SourceBindingKind !=
			evidenceingestion.ProposalSourceBindingSourceSnapshot ||
			record.SourceSystem != evidenceingestion.SourceSystemManualText ||
			sourceContext.SourceView == nil ||
			sourceContext.SourceView.Status !=
				evidenceingestion.BoundedSourceViewStatusAvailable ||
			sourceContext.AtomicContainer == nil ||
			sourceContext.ContextEnvelope == nil ||
			sourceContext.ContextEnvelope.Contract !=
				evidenceingestion.SourceContextEnvelopeContractV1 {
			return fmt.Errorf(
				"proposal occurrence %s has incomplete available source context",
				record.ProposalOccurrenceID,
			)
		}
	case evidenceingestion.GroundedEvidenceSourceContextStatusNotApplicable:
		if sourceContext.AtomicContainer != nil || sourceContext.ContextEnvelope != nil {
			return fmt.Errorf(
				"proposal occurrence %s has source payloads for status %s",
				record.ProposalOccurrenceID,
				sourceContext.Status,
			)
		}
		if sourceContext.SourceView != nil &&
			sourceContext.SourceView.Status !=
				evidenceingestion.BoundedSourceViewStatusAvailable {
			return fmt.Errorf(
				"proposal occurrence %s has incompatible source view status %s",
				record.ProposalOccurrenceID,
				sourceContext.SourceView.Status,
			)
		}
	case evidenceingestion.GroundedEvidenceSourceContextStatusSourceViewOverBudget:
		if record.SourceBindingKind !=
			evidenceingestion.ProposalSourceBindingSourceSnapshot ||
			record.SourceSystem != evidenceingestion.SourceSystemManualText ||
			sourceContext.SourceView == nil ||
			sourceContext.SourceView.Status !=
				evidenceingestion.BoundedSourceViewStatusOverBudget ||
			sourceContext.SourceView.LimitReason == "" ||
			sourceContext.AtomicContainer != nil ||
			sourceContext.ContextEnvelope != nil {
			return fmt.Errorf(
				"proposal occurrence %s has incomplete over-budget source context",
				record.ProposalOccurrenceID,
			)
		}
	default:
		return fmt.Errorf(
			"proposal occurrence %s has unsupported source context status %q",
			record.ProposalOccurrenceID,
			sourceContext.Status,
		)
	}
	return nil
}

func validateGroundedEvidenceRepositoryContext(
	record evidenceingestion.ProposalQueryResult,
	repositoryContext evidenceingestion.GroundedEvidenceRepositoryContext,
) error {
	if record.SourceBindingKind !=
		evidenceingestion.ProposalSourceBindingRepositorySnapshot ||
		record.RepositorySnapshot == nil ||
		(record.CodeFact == nil) == (record.CodeRelation == nil) {
		return fmt.Errorf(
			"proposal occurrence %s has incompatible repository context authority",
			record.ProposalOccurrenceID,
		)
	}
	if repositoryContext.Contract !=
		evidenceingestion.GroundedEvidenceRepositoryContextContractV1 ||
		repositoryContext.SearchParticipation ||
		!slices.Equal(repositoryContext.SearchCore, record.SourceRefs) {
		return fmt.Errorf(
			"proposal occurrence %s has incompatible repository context core",
			record.ProposalOccurrenceID,
		)
	}
	if record.CodeFact != nil {
		if repositoryContext.RecordKind !=
			evidenceingestion.GroundedEvidenceRepositoryRecordDeclaration ||
			repositoryContext.RelationKind != "" {
			return fmt.Errorf(
				"proposal occurrence %s has incompatible declaration context kind",
				record.ProposalOccurrenceID,
			)
		}
	} else if repositoryContext.RecordKind !=
		evidenceingestion.GroundedEvidenceRepositoryRecordRelation ||
		repositoryContext.RelationKind != record.CodeRelation.RelationKind {
		return fmt.Errorf(
			"proposal occurrence %s has incompatible relation context kind",
			record.ProposalOccurrenceID,
		)
	}

	switch repositoryContext.Status {
	case evidenceingestion.GroundedEvidenceRepositoryContextStatusAvailable:
		if repositoryContext.LimitReason != "" {
			return fmt.Errorf(
				"proposal occurrence %s available repository context has a limit reason",
				record.ProposalOccurrenceID,
			)
		}
		if record.CodeFact != nil {
			declaration := repositoryContext.Declaration
			if declaration == nil ||
				repositoryContext.Relation != nil ||
				declaration.ContainerKind != record.CodeFact.SymbolKind ||
				!groundedEvidenceRepositoryDeclarationPayloadMatches(
					record.CodeFact,
					declaration,
					repositoryContext.ReferencedBytes,
				) ||
				repositoryContext.ReferencedBytes >
					evidenceingestion.GroundedEvidenceRepositoryDeclarationMaxBytesV1 ||
				(declaration.ContainerKind == "package") !=
					(declaration.PackageClause == nil) {
				return fmt.Errorf(
					"proposal occurrence %s has incomplete declaration context",
					record.ProposalOccurrenceID,
				)
			}
		} else {
			if repositoryContext.Declaration != nil ||
				repositoryContext.Relation == nil ||
				repositoryContext.ReferencedBytes >
					evidenceingestion.GroundedEvidenceRepositoryRelationMaxBytesV1 ||
				!groundedEvidenceRepositoryRelationPayloadMatches(
					record.CodeRelation,
					repositoryContext.Relation.Endpoints,
					repositoryContext.ReferencedBytes,
				) {
				return fmt.Errorf(
					"proposal occurrence %s has incomplete relation context",
					record.ProposalOccurrenceID,
				)
			}
		}
	case evidenceingestion.GroundedEvidenceRepositoryContextStatusOverBudget:
		if repositoryContext.Declaration != nil ||
			repositoryContext.Relation != nil {
			return fmt.Errorf(
				"proposal occurrence %s over-budget repository context has payload",
				record.ProposalOccurrenceID,
			)
		}
		wantReason := "declaration_context_bytes"
		overBudget := repositoryContext.ReferencedBytes >
			evidenceingestion.GroundedEvidenceRepositoryDeclarationMaxBytesV1
		if record.CodeRelation != nil {
			wantReason = "complete_relation_context_budget"
			overBudget = repositoryContext.ReferencedBytes >
				evidenceingestion.GroundedEvidenceRepositoryRelationEndpointMaxBytesV1 ||
				repositoryContext.ReferencedBytes >
					evidenceingestion.GroundedEvidenceRepositoryRelationMaxBytesV1
		}
		if repositoryContext.LimitReason != wantReason || !overBudget {
			return fmt.Errorf(
				"proposal occurrence %s has invalid over-budget repository context",
				record.ProposalOccurrenceID,
			)
		}
	case evidenceingestion.GroundedEvidenceRepositoryContextStatusFileViewOverBudget:
		if repositoryContext.LimitReason != "repository_file_view_bytes" ||
			repositoryContext.ReferencedBytes != 0 ||
			repositoryContext.Declaration != nil ||
			repositoryContext.Relation != nil {
			return fmt.Errorf(
				"proposal occurrence %s has incomplete file-over-budget context",
				record.ProposalOccurrenceID,
			)
		}
	default:
		return fmt.Errorf(
			"proposal occurrence %s has unsupported repository context status %q",
			record.ProposalOccurrenceID,
			repositoryContext.Status,
		)
	}
	return nil
}

func groundedEvidenceRepositoryDeclarationPayloadMatches(
	fact *evidenceingestion.ResolvedCodeFact,
	declaration *evidenceingestion.GroundedEvidenceRepositoryDeclarationContext,
	referencedBytes int,
) bool {
	container := declaration.AtomicContainer
	if !groundedEvidenceRepositoryContextSliceValid(
		container,
		"atomic_container",
		declaration.ContainerKind,
		fact.Path,
	) ||
		container.StartByte > fact.StartByte ||
		container.EndByte < fact.EndByte {
		return false
	}
	relativeStart := fact.StartByte - container.StartByte
	relativeEnd := fact.EndByte - container.StartByte
	if relativeStart < 0 ||
		relativeEnd <= relativeStart ||
		relativeEnd > len(container.ExactText) ||
		container.ExactText[relativeStart:relativeEnd] != fact.QuotedText ||
		groundedEvidenceRepositoryTextHash(fact.QuotedText) !=
			fact.QuotedTextHash {
		return false
	}
	calculatedBytes := container.EndByte - container.StartByte
	if declaration.PackageClause != nil {
		if !groundedEvidenceRepositoryContextSliceValid(
			*declaration.PackageClause,
			"package_clause",
			"package",
			fact.Path,
		) {
			return false
		}
		calculatedBytes += declaration.PackageClause.EndByte -
			declaration.PackageClause.StartByte
	}
	return referencedBytes > 0 && referencedBytes == calculatedBytes
}

func groundedEvidenceRepositoryRelationPayloadMatches(
	relation *evidenceingestion.ResolvedCodeRelation,
	endpoints []evidenceingestion.GroundedEvidenceRepositoryRelationEndpoint,
	referencedBytes int,
) bool {
	type expectedEndpoint struct {
		role       evidenceingestion.GroundedEvidenceRepositoryContextRole
		path       string
		startByte  int
		endByte    int
		quotedText string
		quotedHash string
	}
	want := []expectedEndpoint{{
		role:       evidenceingestion.GroundedEvidenceRepositoryContextUsage,
		path:       relation.Usage.Path,
		startByte:  relation.Usage.StartByte,
		endByte:    relation.Usage.EndByte,
		quotedText: relation.Usage.QuotedText,
		quotedHash: relation.Usage.QuotedTextHash,
	}}
	if relation.Caller != nil {
		want = append(
			want,
			expectedEndpoint{
				role:       evidenceingestion.GroundedEvidenceRepositoryContextCaller,
				path:       relation.Caller.Path,
				startByte:  relation.Caller.StartByte,
				endByte:    relation.Caller.EndByte,
				quotedText: relation.Caller.QuotedText,
				quotedHash: relation.Caller.QuotedTextHash,
			},
		)
	}
	want = append(
		want,
		expectedEndpoint{
			role:       evidenceingestion.GroundedEvidenceRepositoryContextTarget,
			path:       relation.Target.Path,
			startByte:  relation.Target.StartByte,
			endByte:    relation.Target.EndByte,
			quotedText: relation.Target.QuotedText,
			quotedHash: relation.Target.QuotedTextHash,
		},
	)
	if len(endpoints) != len(want) {
		return false
	}
	calculatedBytes := 0
	for index, expected := range want {
		endpoint := endpoints[index]
		role := string(expected.role)
		if endpoint.Role != expected.role ||
			endpoint.Path != expected.path ||
			!groundedEvidenceRepositoryContextSliceValid(
				endpoint.Anchor,
				role+"_anchor",
				"identifier",
				expected.path,
			) ||
			endpoint.Anchor.StartByte != expected.startByte ||
			endpoint.Anchor.EndByte != expected.endByte ||
			endpoint.Anchor.ExactText != expected.quotedText ||
			endpoint.Anchor.ContentHash != expected.quotedHash ||
			!groundedEvidenceRepositoryContextSliceValid(
				endpoint.AtomicContainer,
				role+"_container",
				endpoint.AtomicContainer.Kind,
				expected.path,
			) ||
			endpoint.AtomicContainer.StartByte > endpoint.Anchor.StartByte ||
			endpoint.AtomicContainer.EndByte < endpoint.Anchor.EndByte ||
			!groundedEvidenceRepositoryContextSliceValid(
				endpoint.PackageClause,
				role+"_package_clause",
				"package",
				expected.path,
			) {
			return false
		}
		relativeStart := endpoint.Anchor.StartByte -
			endpoint.AtomicContainer.StartByte
		relativeEnd := endpoint.Anchor.EndByte -
			endpoint.AtomicContainer.StartByte
		if relativeStart < 0 ||
			relativeEnd <= relativeStart ||
			relativeEnd > len(endpoint.AtomicContainer.ExactText) ||
			endpoint.AtomicContainer.ExactText[relativeStart:relativeEnd] !=
				endpoint.Anchor.ExactText {
			return false
		}
		endpointBytes := endpoint.AtomicContainer.EndByte -
			endpoint.AtomicContainer.StartByte +
			endpoint.PackageClause.EndByte -
			endpoint.PackageClause.StartByte
		if endpoint.ReferencedBytes != endpointBytes ||
			endpoint.ReferencedBytes <= 0 ||
			endpoint.ReferencedBytes >
				evidenceingestion.GroundedEvidenceRepositoryRelationEndpointMaxBytesV1 {
			return false
		}
		calculatedBytes += endpointBytes
	}
	return referencedBytes > 0 && referencedBytes == calculatedBytes
}

func groundedEvidenceRepositoryContextSliceValid(
	slice evidenceingestion.GroundedEvidenceRepositoryContextSlice,
	role string,
	kind string,
	path string,
) bool {
	if slice.Role != role ||
		slice.Kind == "" ||
		slice.Kind != kind ||
		slice.Path != path ||
		slice.StartByte < 0 ||
		slice.EndByte <= slice.StartByte ||
		len(slice.ExactText) != slice.EndByte-slice.StartByte {
		return false
	}
	return slice.ContentHash == groundedEvidenceRepositoryTextHash(
		slice.ExactText,
	)
}

func groundedEvidenceRepositoryTextHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func groundedEvidenceRepositoryCoverageObservationCodes(
	coverage []evidenceingestion.GroundedEvidenceBriefCoverage,
) []string {
	definitionAmbiguous := false
	referenceAmbiguous := false
	for _, item := range coverage {
		diagnostic := item.RepositoryGoplsCoverage
		if diagnostic.SchemaVersion !=
			evidenceingestion.RepositoryGoplsCoverageSchemaV8 {
			continue
		}
		definitionAmbiguous =
			definitionAmbiguous || diagnostic.DefinitionAmbiguousUsageCount > 0
		referenceAmbiguous =
			referenceAmbiguous || diagnostic.ReferenceAmbiguousUsageCount > 0
	}
	codes := []string{}
	if definitionAmbiguous {
		codes = append(
			codes,
			"repository_definition_targets_ambiguous_and_withheld",
		)
	}
	if referenceAmbiguous {
		codes = append(
			codes,
			"repository_reference_targets_ambiguous_and_withheld",
		)
	}
	return codes
}

func mapGroundedEvidenceQueryExecution(execution evidenceingestion.EvidenceQueryExecution) GroundedEvidenceQueryExecution {
	attempts := make([]GroundedEvidenceQueryAttempt, 0, len(execution.Attempts))
	for _, attempt := range execution.Attempts {
		attempts = append(attempts, GroundedEvidenceQueryAttempt{
			Strategy:                attempt.Strategy,
			TextSearchConfiguration: attempt.TextSearchConfiguration,
			CompiledQuery:           attempt.CompiledQuery,
			NormalizedQueryTerms:    append([]string(nil), attempt.NormalizedQueryTerms...),
			CandidateCount:          attempt.CandidateCount,
			Truncated:               attempt.Truncated,
		})
	}
	return GroundedEvidenceQueryExecution{
		OriginalQuery:              execution.OriginalQuery,
		QueryMode:                  execution.QueryMode,
		PlanVersion:                execution.PlanVersion,
		NormalizerVersion:          execution.NormalizerVersion,
		SearchSurface:              execution.SearchSurface,
		SearchedFields:             append([]string(nil), execution.SearchedFields...),
		SearchedRecordKinds:        append([]string(nil), execution.SearchedRecordKinds...),
		EligibleSourceBindingKinds: append([]string(nil), execution.EligibleSourceBindingKinds...),
		Filters: GroundedEvidenceQueryFilters{
			SourceSnapshotID:     execution.Filters.SourceSnapshotID,
			RepositorySnapshotID: execution.Filters.RepositorySnapshotID,
			SourceGenerationID:   execution.Filters.SourceGenerationID,
			SourceID:             execution.Filters.SourceID,
			SourceVersion:        execution.Filters.SourceVersion,
			AdmissionOutcome:     execution.Filters.AdmissionOutcome,
			LifecycleScope:       execution.Filters.LifecycleScope,
		},
		Limit:                         execution.Limit,
		QueryCount:                    execution.QueryCount,
		CandidateCount:                execution.CandidateCount,
		Truncated:                     execution.Truncated,
		SearchCompleteWithinSurface:   execution.SearchCompleteWithinSurface,
		CompletionReason:              execution.CompletionReason,
		GlobalAbsenceInferenceAllowed: execution.GlobalAbsenceInferenceAllowed,
		Attempts:                      attempts,
	}
}

func groundedEvidenceBriefObservationCodes(execution evidenceingestion.EvidenceQueryExecution) []string {
	codes := []string{}
	if len(execution.Attempts) > 0 &&
		execution.Attempts[0].Strategy == evidenceingestion.EvidenceQueryStrategyExactSimple &&
		execution.Attempts[0].CandidateCount == 0 {
		codes = append(codes, "exact_lexical_no_match")
	}
	switch execution.CompletionReason {
	case evidenceingestion.EvidenceQueryCompletionMorphologyCandidates:
		codes = append(codes, "morphology_candidates_found")
	case evidenceingestion.EvidenceQueryCompletionRelatedCandidates:
		codes = append(codes, "related_candidates_found")
	case evidenceingestion.EvidenceQueryCompletionBoundedNoMatch:
		codes = append(codes, "bounded_retrieval_no_match")
	}
	return codes
}

func groundedEvidenceBriefLimitations(execution evidenceingestion.EvidenceQueryExecution) []string {
	limitations := []string{"persisted_proposal_statement_text_only"}
	if execution.QueryMode == evidenceingestion.EvidenceQueryModeExactLexical {
		limitations = append(limitations, "exact_simple_fts_without_query_recovery")
	} else {
		limitations = append(limitations, "deterministic_lexical_recovery_without_semantic_aliases")
	}
	return append(limitations,
		"retrieval_candidates_are_not_evidence_claims",
		"coverage_only_for_returned_match_extraction_attempts",
		"no_absence_inference_outside_the_bounded_search_surface",
	)
}

func briefSourceScope(record evidenceingestion.ProposalQueryResult) GroundedEvidenceBriefSourceScope {
	scope := GroundedEvidenceBriefSourceScope{
		Source:           mapSourceInfo(record),
		SourceGeneration: mapSourceGeneration(record),
	}
	if record.SourceGeneration != nil {
		scope.ScopeRef = RecordRef{Kind: "source_generation", ID: record.SourceGeneration.ID}
		return scope
	}
	if record.RepositorySnapshot != nil {
		scope.ScopeRef = RecordRef{Kind: "repository_snapshot", ID: record.RepositorySnapshot.ID}
		return scope
	}
	scope.ScopeRef = RecordRef{Kind: "source_snapshot", ID: record.SourceSnapshotID}
	return scope
}

func briefSymbolRefs(record evidenceingestion.ProposalQueryResult) []RecordRef {
	refs := make(map[string]RecordRef)
	if record.CodeFact != nil && record.CodeFact.SymbolRef != "" {
		refs[record.CodeFact.SymbolRef] = RecordRef{Kind: "code_symbol", ID: record.CodeFact.SymbolRef}
	}
	if record.CodeRelation != nil {
		if record.CodeRelation.Caller != nil && record.CodeRelation.Caller.SymbolRef != "" {
			refs[record.CodeRelation.Caller.SymbolRef] = RecordRef{Kind: "code_symbol", ID: record.CodeRelation.Caller.SymbolRef}
		}
		if record.CodeRelation.Target.SymbolRef != "" {
			refs[record.CodeRelation.Target.SymbolRef] = RecordRef{Kind: "code_symbol", ID: record.CodeRelation.Target.SymbolRef}
		}
	}
	keys := sortedMapKeys(refs)
	result := make([]RecordRef, 0, len(keys))
	for _, key := range keys {
		result = append(result, refs[key])
	}
	return result
}

func incrementBriefCounts(counts *GroundedEvidenceBriefCounts, record evidenceingestion.ProposalQueryResult) {
	switch record.AdmissionOutcome {
	case "pending":
		counts.Admission.Pending++
	case "rejected":
		counts.Admission.Rejected++
	case "audit_only":
		counts.Admission.AuditOnly++
	case "admitted":
		counts.Admission.Admitted++
	}
	if record.RepositorySnapshot == nil {
		counts.Lifecycle.SourceSnapshot++
	} else if record.SourceGenerationActive {
		counts.Lifecycle.RepositoryActive++
	} else {
		counts.Lifecycle.RepositoryHistorical++
	}
}

func addBriefFollowUps(followUps map[string]GroundedEvidenceBriefFollowUp, record evidenceingestion.ProposalQueryResult, symbolRefs []RecordRef) {
	recordFollowUp := GroundedEvidenceBriefFollowUp{
		Tool:                 ToolGetEvidenceRecord,
		ProposalOccurrenceID: record.ProposalOccurrenceID,
	}
	followUps[briefFollowUpKey(recordFollowUp)] = recordFollowUp
	if record.CanonicalRef != "" {
		canonicalFollowUp := GroundedEvidenceBriefFollowUp{
			Tool:        ToolGetEvidenceRecord,
			CanonicalID: record.CanonicalRef,
		}
		followUps[briefFollowUpKey(canonicalFollowUp)] = canonicalFollowUp
	}
	if record.CodeRelation != nil {
		relationFollowUp := GroundedEvidenceBriefFollowUp{
			Tool:                 ToolGetRelationProvenance,
			ProposalOccurrenceID: record.ProposalOccurrenceID,
		}
		followUps[briefFollowUpKey(relationFollowUp)] = relationFollowUp
	}
	for _, ref := range symbolRefs {
		symbolFollowUp := GroundedEvidenceBriefFollowUp{
			Tool:           ToolListEvidenceNeighbors,
			SymbolRef:      ref.ID,
			LifecycleScope: evidenceingestion.ProposalLifecycleScopeActive,
		}
		if record.SourceGeneration != nil {
			symbolFollowUp.SourceGenerationID = record.SourceGeneration.ID
			symbolFollowUp.LifecycleScope = evidenceingestion.ProposalLifecycleScopeAll
		}
		followUps[briefFollowUpKey(symbolFollowUp)] = symbolFollowUp
	}
}

func briefFollowUpKey(followUp GroundedEvidenceBriefFollowUp) string {
	return strings.Join([]string{
		followUp.Tool,
		followUp.ProposalOccurrenceID,
		followUp.CanonicalID,
		followUp.SymbolRef,
		followUp.SourceGenerationID,
		followUp.LifecycleScope,
	}, "\x00")
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mapRepositoryRelationProvenance(result evidenceingestion.ProposalQueryResult) RelationProvenanceResponse {
	origin := mapProposalResult(result)
	return RelationProvenanceResponse{
		RelationRef:  RecordRef{Kind: "proposal_relation", ID: result.ProposalOccurrenceID},
		Surface:      "repository_code",
		RelationKind: result.CodeRelation.RelationKind,
		OriginRecord: &origin,
		SourceRefs:   append([]evidenceingestion.ResolvedSourceRef(nil), result.SourceRefs...),
		CodeRelation: cloneCodeRelation(result.CodeRelation),
	}
}

func mapCanonicalRelationProvenance(result evidenceingestion.CanonicalRelationQueryResult) RelationProvenanceResponse {
	from := mapCanonicalResult(result.From)
	to := mapCanonicalResult(result.To)
	response := RelationProvenanceResponse{
		RelationRef:  RecordRef{Kind: "canonical_relation", ID: result.Edge.ID},
		Surface:      "canonical_evidence",
		RelationKind: string(result.Edge.Relation),
		CanonicalEdge: &CanonicalEdgeInfo{
			From:       RecordRef{Kind: "canonical_evidence", ID: result.Edge.From},
			To:         RecordRef{Kind: "canonical_evidence", ID: result.Edge.To},
			Relation:   string(result.Edge.Relation),
			Provenance: result.Edge.Provenance,
		},
		FromRecord: &from,
		ToRecord:   &to,
	}
	if result.OriginProposal != nil {
		origin := mapProposalResult(*result.OriginProposal)
		response.OriginRecord = &origin
		response.SourceRefs = append([]evidenceingestion.ResolvedSourceRef(nil), result.OriginProposal.SourceRefs...)
	}
	if result.OriginContradictionProposal != nil {
		origin := mapCanonicalContradictionProposal(*result.OriginContradictionProposal)
		response.OriginContradictionProposal = &origin
	}
	if result.OriginSupersessionProposal != nil {
		origin := mapCanonicalSupersessionProposal(*result.OriginSupersessionProposal)
		response.OriginSupersessionProposal = &origin
	}
	return response
}

func mapCanonicalContradictionProposal(result evidenceingestion.CanonicalContradictionQueryResult) CanonicalContradictionProposalResponse {
	proposal := result.Proposal
	response := CanonicalContradictionProposalResponse{
		Proposal: CanonicalContradictionProposalInfo{
			CanonicalContradictionProposalID: proposal.ID,
			RequestID:                        proposal.RequestID,
			ProposalFingerprint:              proposal.ProposalFingerprint,
			NodeAID:                          proposal.NodeAID,
			NodeBID:                          proposal.NodeBID,
			Relation:                         string(proposal.Relation),
			Rationale:                        proposal.Rationale,
			ProducerName:                     proposal.ProducerName,
			ProducerVersion:                  proposal.ProducerVersion,
			ProducerSessionRef:               proposal.ProducerSessionRef,
			AdmissionOutcome:                 proposal.AdmissionOutcome,
			CanonicalEdgeID:                  proposal.CanonicalEdgeID,
		},
		NodeA: mapCanonicalResult(result.NodeA),
		NodeB: mapCanonicalResult(result.NodeB),
	}
	if result.Decision != nil {
		response.Decision = &CanonicalContradictionDecisionInfo{
			AdmissionDecisionID: result.Decision.ID,
			AdmissionOutcome:    result.Decision.Outcome,
			CanonicalEdgeID:     result.Decision.CanonicalEdgeID,
			DecisionBy:          result.Decision.DecisionBy,
			DecisionReason:      result.Decision.DecisionReason,
		}
	}
	return response
}

func mapCanonicalSupersessionProposal(result evidenceingestion.CanonicalSupersessionQueryResult) CanonicalSupersessionProposalResponse {
	proposal := result.Proposal
	response := CanonicalSupersessionProposalResponse{
		Proposal: CanonicalSupersessionProposalInfo{
			CanonicalSupersessionProposalID: proposal.ID,
			RequestID:                       proposal.RequestID,
			ProposalFingerprint:             proposal.ProposalFingerprint,
			FromNodeID:                      proposal.FromNodeID,
			ToNodeID:                        proposal.ToNodeID,
			Relation:                        string(proposal.Relation),
			ProposalSentence:                proposal.ProposalSentence,
			Rationale:                       proposal.Rationale,
			VersionDifference:               proposal.VersionDifference,
			Limitations:                     append([]string{}, proposal.Limitations...),
			ProducerName:                    proposal.ProducerName,
			ProducerVersion:                 proposal.ProducerVersion,
			ProducerSessionRef:              proposal.ProducerSessionRef,
			AdmissionOutcome:                proposal.AdmissionOutcome,
			CanonicalEdgeID:                 proposal.CanonicalEdgeID,
		},
		From: mapCanonicalResult(result.From),
		To:   mapCanonicalResult(result.To),
	}
	if result.Decision != nil {
		response.Decision = &CanonicalSupersessionDecisionInfo{
			AdmissionDecisionID: result.Decision.ID,
			AdmissionOutcome:    result.Decision.Outcome,
			CanonicalEdgeID:     result.Decision.CanonicalEdgeID,
			DecisionBy:          result.Decision.DecisionBy,
			DecisionReason:      result.Decision.DecisionReason,
		}
	}
	return response
}

func mapSourceGeneration(result evidenceingestion.ProposalQueryResult) *SourceGenerationInfo {
	if result.SourceGeneration == nil {
		return nil
	}
	return &SourceGenerationInfo{
		RepositorySourceGeneration: *result.SourceGeneration,
		Active:                     result.SourceGenerationActive,
	}
}

func mapRepositoryLifecycle(result evidenceingestion.ProposalQueryResult) *evidenceingestion.RepositoryProposalLifecycle {
	if result.RepositoryLifecycle == nil {
		return nil
	}
	copy := *result.RepositoryLifecycle
	return &copy
}

func mapSourceInfo(result evidenceingestion.ProposalQueryResult) SourceInfo {
	var repositorySnapshot *evidenceingestion.RepositorySnapshot
	if result.RepositorySnapshot != nil {
		copy := *result.RepositorySnapshot
		repositorySnapshot = &copy
	}
	source := SourceInfo{
		BindingKind:        result.SourceBindingKind,
		SourceSnapshotID:   result.SourceSnapshotID,
		SourceSystem:       result.SourceSystem,
		SourceID:           result.SourceID,
		SourceVersion:      result.SourceVersion,
		RawContentHash:     result.RawContentHash,
		RepositorySnapshot: repositorySnapshot,
	}
	if result.SourceSystem == evidenceingestion.SourceSystemMCPReadDocument {
		limitations := []string{}
		_ = json.Unmarshal(
			[]byte(result.OriginMetadata["mcp_limitations_json"]),
			&limitations,
		)
		proposalCandidates := []MCPReadProposalCandidateProvenance{}
		_ = json.Unmarshal(
			[]byte(result.OriginMetadata["mcp_proposal_candidates_json"]),
			&proposalCandidates,
		)
		source.MCPRead = &MCPReadSourceProvenance{
			ConnectorDeliveryID:           result.OriginMetadata["connector_delivery_id"],
			Provider:                      result.OriginMetadata["mcp_provider"],
			ObjectID:                      result.OriginMetadata["mcp_object_id"],
			Revision:                      result.OriginMetadata["mcp_revision"],
			DocumentID:                    result.OriginMetadata["mcp_document_id"],
			SourceLocation:                result.OriginMetadata["mcp_source_location"],
			SourceIdentityContract:        result.OriginMetadata["mcp_source_identity_contract"],
			ProposalCandidateContract:     result.OriginMetadata["mcp_proposal_candidate_contract"],
			CoverageComplete:              result.OriginMetadata["mcp_coverage_complete"] == "true",
			CoverageTruncated:             result.OriginMetadata["mcp_coverage_truncated"] == "true",
			CoverageCompletionReason:      result.OriginMetadata["mcp_coverage_completion_reason"],
			Limitations:                   limitations,
			ProposalCandidates:            proposalCandidates,
			GlobalAbsenceInferenceAllowed: result.OriginMetadata["global_absence_inference_allowed"] == "true",
		}
	}
	if result.SourceSystem == evidenceingestion.SourceSystemExternalDocument {
		limitations := []string{}
		_ = json.Unmarshal(
			[]byte(result.OriginMetadata["external_limitations_json"]),
			&limitations,
		)
		source.ExternalSource = &ExternalSourceProvenance{
			SchemaVersion:                 result.OriginMetadata["external_source_schema"],
			SourceSystem:                  result.OriginMetadata["external_source_system"],
			SourceNamespace:               result.OriginMetadata["external_source_namespace"],
			ObjectType:                    result.OriginMetadata["external_object_type"],
			ObjectID:                      result.OriginMetadata["external_object_id"],
			Revision:                      result.OriginMetadata["external_revision"],
			SourceLocation:                result.OriginMetadata["external_source_location"],
			Title:                         result.OriginMetadata["external_title"],
			ContentFormat:                 result.OriginMetadata["external_content_format"],
			ContentFidelity:               result.OriginMetadata["external_content_fidelity"],
			Coverage:                      result.OriginMetadata["external_coverage"],
			Limitations:                   limitations,
			SourceCreatedAt:               result.OriginMetadata["external_source_created_at"],
			SourceUpdatedAt:               result.OriginMetadata["external_source_updated_at"],
			GlobalAbsenceInferenceAllowed: result.OriginMetadata["global_absence_inference_allowed"] == "true",
		}
	}
	return source
}

func cloneCodeFact(fact *evidenceingestion.ResolvedCodeFact) *evidenceingestion.ResolvedCodeFact {
	if fact == nil {
		return nil
	}
	copy := *fact
	return &copy
}

func cloneCodeRelation(relation *evidenceingestion.ResolvedCodeRelation) *evidenceingestion.ResolvedCodeRelation {
	if relation == nil {
		return nil
	}
	copy := *relation
	if relation.Caller != nil {
		caller := *relation.Caller
		copy.Caller = &caller
	}
	return &copy
}

func cloneDeclaration(declaration *evidenceingestion.ResolvedCodeDeclarationEndpoint) *evidenceingestion.ResolvedCodeDeclarationEndpoint {
	if declaration == nil {
		return nil
	}
	copy := *declaration
	return &copy
}

func cloneUsage(usage *evidenceingestion.ResolvedCodeUsageSite) *evidenceingestion.ResolvedCodeUsageSite {
	if usage == nil {
		return nil
	}
	copy := *usage
	return &copy
}

func mapToolError(err error) error {
	var domainErr *evidenceingestion.DomainError
	if errors.As(err, &domainErr) {
		code := toolErrorInternal
		switch domainErr.Kind {
		case evidenceingestion.ErrorMissingSourceViewAttempt,
			evidenceingestion.ErrorSourceStateNotFound:
			code = toolErrorNotFound
		case evidenceingestion.ErrorInvalidRecordID:
			code = toolErrorInvalidRecordID
		case evidenceingestion.ErrorInvalidInput:
			code = toolErrorInvalidRequest
		}
		return &ToolError{
			Code:    code,
			Message: domainErr.Error(),
			cause:   err,
		}
	}
	return &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
}
