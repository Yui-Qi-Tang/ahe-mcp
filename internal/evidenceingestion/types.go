package evidenceingestion

import (
	"context"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

const (
	// SourceSystemManualText identifies manually submitted natural-language text.
	SourceSystemManualText = "manual_text"
	// SourceSystemCodeFile identifies one immutable source file under a repository revision.
	SourceSystemCodeFile = "code_file"
	// SourceSystemConnectorText identifies exact text bytes received through a durable connector inbox.
	SourceSystemConnectorText = "connector_text"
	// SourceSystemMCPReadDocument identifies one exact document projected from a durable MCP read receipt.
	SourceSystemMCPReadDocument = "mcp_read_document"
	// SourceSystemCodeRepository identifies one immutable repository snapshot projection.
	SourceSystemCodeRepository = "code_repository"

	// ProposalSourceBindingSourceSnapshot identifies the legacy source snapshot and extraction view binding.
	ProposalSourceBindingSourceSnapshot = "source_snapshot"
	// ProposalSourceBindingRepositorySnapshot identifies a repository snapshot binding without a synthetic extraction view.
	ProposalSourceBindingRepositorySnapshot = "repository_snapshot"

	// RendererManualTextIdentity names the Slice 1 identity renderer.
	RendererManualTextIdentity = "manual-text-identity"
	// RendererManualTextIdentityVersion is the identity renderer version.
	RendererManualTextIdentityVersion = "v1"
	// RendererCodeIdentity names the identity renderer for immutable code file bytes.
	RendererCodeIdentity = "code-identity"
	// RendererCodeIdentityVersion is the code identity renderer version.
	RendererCodeIdentityVersion = "v1"
	// RendererConnectorTextIdentity names the identity renderer for exact connector text bytes.
	RendererConnectorTextIdentity = "connector-text-identity"
	// RendererConnectorTextIdentityVersion is the connector text identity renderer version.
	RendererConnectorTextIdentityVersion = "v1"
	// RendererMCPReadDocumentIdentity names the identity renderer for one MCP document projection.
	RendererMCPReadDocumentIdentity = "mcp-read-document-identity"
	// RendererMCPReadDocumentIdentityVersion is the MCP document identity renderer version.
	RendererMCPReadDocumentIdentityVersion = "v1"

	// SpanCatalogManualLineV1 identifies the Slice 1 line span catalog.
	SpanCatalogManualLineV1 = "manual-line-v1"
	// SpanCatalogCodeLineV1 identifies deterministic line anchors for code files.
	SpanCatalogCodeLineV1 = "code-line-v1"
	// SpanCatalogConnectorTextLineV1 identifies deterministic line anchors for connector text.
	SpanCatalogConnectorTextLineV1 = "connector-text-line-v1"
	// SpanCatalogMCPReadDocumentLineV1 identifies deterministic line anchors for one MCP document.
	SpanCatalogMCPReadDocumentLineV1 = "mcp-read-document-line-v1"

	// ExtractorFrozenManualFixture names the Slice 1 fixture extractor.
	ExtractorFrozenManualFixture = "frozen-manual-fixture"
	// ExtractorFrozenManualFixtureVersion is the fixture extractor version.
	ExtractorFrozenManualFixtureVersion = "v1"

	// ProposalKindStatement is the only proposal kind supported in Slice 1.
	ProposalKindStatement = "statement"
	// ProposalFingerprintStatementV1 is the Slice 1 statement fingerprint version.
	ProposalFingerprintStatementV1 = "statement-v1"
	// ProposalFingerprintCodeFactV1 includes verified code identity and exact span fields.
	ProposalFingerprintCodeFactV1 = "code-fact-v1"
	// ProposalFingerprintCodeRelationV1 includes verified definition endpoints and exact source spans.
	ProposalFingerprintCodeRelationV1 = "code-relation-v1"
	// ProposalFingerprintCodeRelationV2 includes verified caller, call-site, and callee endpoints.
	ProposalFingerprintCodeRelationV2 = "code-relation-v2"
	// ProposalFingerprintCodeRelationV3 identifies snapshot-local same-file relations.
	ProposalFingerprintCodeRelationV3 = "code-relation-v3"

	// CodeFactSchemaV1 is the first controller-verified code fact transport contract.
	CodeFactSchemaV1 = "code-fact-v1"
	// CodeFactKindDeclaration identifies a parser-grounded Go declaration.
	CodeFactKindDeclaration = "declaration"
	// CodeFactSchemaV2 adds file-bound Go package clause facts.
	CodeFactSchemaV2 = "code-fact-v2"
	// CodeFactKindPackage identifies one source file's package clause.
	CodeFactKindPackage = "package"
	// CodeRelationSchemaV1 is the first controller-verified typed code relation contract.
	CodeRelationSchemaV1 = "code-relation-v1"
	// CodeRelationKindDefinition binds one identifier usage to one declaration.
	CodeRelationKindDefinition = "definition"
	// CodeRelationSchemaV2 adds a caller declaration endpoint for call relations.
	CodeRelationSchemaV2 = "code-relation-v2"
	// CodeRelationKindCall binds a caller declaration and call site to a callee declaration.
	CodeRelationKindCall = "call"
	// CodeRelationSchemaV3 carries snapshot-local same-file definition and call relations.
	CodeRelationSchemaV3 = "code-relation-v3"
	// RepositoryGoplsCoverageSchemaV1 identifies successful repository semantic-run diagnostics.
	RepositoryGoplsCoverageSchemaV1 = "repository-gopls-coverage-v1"
	// RepositoryGoplsCoverageSchemaV2 adds bounded outgoing call-hierarchy diagnostics.
	RepositoryGoplsCoverageSchemaV2 = "repository-gopls-coverage-v2"
	// RepositoryGoplsCoverageSchemaV3 distinguishes attempted and completed semantic requests.
	RepositoryGoplsCoverageSchemaV3 = "repository-gopls-coverage-v3"
	// RepositoryGoplsCoverageSchemaV4 adds bounded package-symbol request diagnostics.
	RepositoryGoplsCoverageSchemaV4 = "repository-gopls-coverage-v4"
	// RepositoryGoplsCoverageSchemaV5 adds incoming-call enumeration and reconciliation diagnostics.
	RepositoryGoplsCoverageSchemaV5 = "repository-gopls-coverage-v5"
	// RepositoryGoplsCoverageSchemaV6 distinguishes supported same-file relations.
	RepositoryGoplsCoverageSchemaV6 = "repository-gopls-coverage-v6"
	// RepositoryGoplsCoverageSchemaV7 adds diagnostics for unselected semantic results.
	RepositoryGoplsCoverageSchemaV7 = "repository-gopls-coverage-v7"
	// RepositoryGoplsCoverageSchemaV8 adds diagnostics for ambiguous grounded relation targets.
	RepositoryGoplsCoverageSchemaV8 = "repository-gopls-coverage-v8"
	// RepositoryFileSelectionTrackedGoV1 selects tracked regular .go blobs from one Git commit.
	RepositoryFileSelectionTrackedGoV1 = "tracked-regular-go-files-v1"
	// RepositoryRevisionVerificationGitV1 verifies revisions and bytes through the Git object database.
	RepositoryRevisionVerificationGitV1 = "git-object-database-v1"
	// RepositoryManifestGitTreeV1 hashes the exact recursive NUL-delimited Git tree listing.
	RepositoryManifestGitTreeV1 = "git-ls-tree-r-z-sha256-v1"
	// RepositoryProposalIdentityContractV1 compares the stored fingerprint and complete resolved source references.
	RepositoryProposalIdentityContractV1 = "proposal-fingerprint+source-refs-v1"
	// RepositoryProposalLifecycleNew identifies a proposal absent from the previous active generation.
	RepositoryProposalLifecycleNew = "new"
	// RepositoryProposalLifecycleUnchanged identifies an exact proposal identity present in both generations.
	RepositoryProposalLifecycleUnchanged = "unchanged"
	// RepositoryProposalLifecycleStale identifies a previous proposal absent from the newly active generation.
	RepositoryProposalLifecycleStale = "stale"
	// ProposalLifecycleScopeActive returns source-bound records and the active repository generation.
	ProposalLifecycleScopeActive = "active"
	// ProposalLifecycleScopeHistorical returns only inactive repository generations.
	ProposalLifecycleScopeHistorical = "historical"
	// ProposalLifecycleScopeAll returns source-bound records and all repository generations.
	ProposalLifecycleScopeAll = "all"
	// RelationDirectionIncoming returns relations whose target is the query root.
	RelationDirectionIncoming = "incoming"
	// RelationDirectionOutgoing returns relations whose source is the query root.
	RelationDirectionOutgoing = "outgoing"
	// RelationDirectionBoth returns incoming and outgoing relations.
	RelationDirectionBoth = "both"
	// AdmissionProducerSlice9 identifies the first bounded canonical admission path.
	AdmissionProducerSlice9 = "ahe-ingestion-admission-slice9"
	// ProposalDispositionRejected permanently excludes one pending proposal from canonical admission.
	ProposalDispositionRejected = "rejected"
	// ProposalDispositionAuditOnly retains one pending proposal only as reviewed audit material.
	ProposalDispositionAuditOnly = "audit_only"
	// ProposalDispositionDecisionByMaxBytes caps one disposition reviewer identity.
	ProposalDispositionDecisionByMaxBytes = 200
	// ProposalDispositionDecisionReasonMaxBytes caps one disposition reason.
	ProposalDispositionDecisionReasonMaxBytes = 2000

	attemptStatusStarted   = "started"
	attemptStatusSucceeded = "succeeded"
	attemptStatusFailed    = "failed"

	batchStatusCompleted = "completed"
	batchStatusFailed    = "failed"

	admissionOutcomePending   = "pending"
	admissionOutcomeRejected  = ProposalDispositionRejected
	admissionOutcomeAuditOnly = ProposalDispositionAuditOnly
	admissionOutcomeAdmitted  = "admitted"
)

// ManualTextInput is the legacy source input shared by manual text, code-file, and connector-text intake.
type ManualTextInput struct {
	SourceSystem   string
	SourceID       string
	SourceVersion  string
	Raw            []byte
	OriginMetadata map[string]string
	RequestID      string
	AttemptNumber  int
}

// SourceIntakeResult is returned after raw manual text has been fixed as source authority.
type SourceIntakeResult struct {
	SourceSnapshotID    string
	ExtractionViewID    string
	RawContentHash      string
	RenderedContentHash string
	SourceSystem        string
	SourceID            string
	SourceVersion       string
	RendererName        string
	RendererVersion     string
	SpanCatalogVersion  string
	Spans               []SpanEntry
	Replayed            bool
}

// RepositorySnapshot identifies one immutable Git repository revision and manifest.
type RepositorySnapshot struct {
	ID                         string `json:"repository_snapshot_id"`
	RepoID                     string `json:"repo_id"`
	CommitSHA                  string `json:"commit_sha"`
	ManifestHash               string `json:"manifest_hash"`
	ManifestEntryCount         int    `json:"manifest_entry_count"`
	RevisionVerificationMethod string `json:"revision_verification_method"`
	ManifestContract           string `json:"manifest_contract"`
	FileSelectionContract      string `json:"file_selection_contract"`
	SelectedFileCount          int    `json:"selected_file_count"`
}

// SourceFileSnapshot identifies immutable file bytes under one repository snapshot.
type SourceFileSnapshot struct {
	ID                   string `json:"file_snapshot_id"`
	RepositorySnapshotID string `json:"repository_snapshot_id"`
	RepoID               string `json:"repo_id"`
	CommitSHA            string `json:"commit_sha"`
	Path                 string `json:"path"`
	BlobHash             string `json:"blob_hash"`
	GitBlobOID           string `json:"git_blob_oid"`
	ByteLength           int    `json:"byte_length"`
}

// RepositorySnapshotCaptureResult reports one persisted Git snapshot intake.
type RepositorySnapshotCaptureResult struct {
	RepositorySnapshot RepositorySnapshot   `json:"repository_snapshot"`
	FileSnapshots      []SourceFileSnapshot `json:"file_snapshots"`
	Replayed           bool                 `json:"replayed"`
}

// RepositoryExtractorInput is the immutable multi-file input for one code extraction run.
type RepositoryExtractorInput struct {
	RepositorySnapshot RepositorySnapshot        `json:"repository_snapshot"`
	Files              []RepositoryExtractorFile `json:"files"`
}

// RepositoryExtractorFile binds exact persisted bytes to one repository file snapshot.
type RepositoryExtractorFile struct {
	FileSnapshot SourceFileSnapshot `json:"file_snapshot"`
	Content      []byte             `json:"content_base64"`
}

// FrozenExtractorOutput is the strict v1 extractor output envelope after decode.
// The name is retained for Slice 1 fixture compatibility.
type FrozenExtractorOutput struct {
	Proposals               []ExtractorProposalOutput `json:"proposals"`
	RepositoryGoplsCoverage *RepositoryGoplsCoverage  `json:"repository_gopls_coverage,omitempty"`
	DocumentSectionCoverage *DocumentSectionCoverage  `json:"document_section_coverage,omitempty"`
}

// DocumentSectionCoverage records deterministic structural coverage around
// bounded document extraction. It is attempt diagnostic metadata, not evidence.
type DocumentSectionCoverage struct {
	SchemaVersion            string                        `json:"schema_version"`
	SegmentationContract     string                        `json:"segmentation_contract"`
	OffsetBasis              string                        `json:"offset_basis"`
	CoverageComplete         bool                          `json:"coverage_complete"`
	NegativeInferenceAllowed bool                          `json:"negative_inference_allowed"`
	SelectedSourceCount      int                           `json:"selected_source_count"`
	SectionCount             int                           `json:"section_count"`
	ModelCallCount           int                           `json:"model_call_count"`
	MaxSections              int                           `json:"max_sections"`
	MaxProposalsPerSection   int                           `json:"max_proposals_per_section"`
	Sections                 []DocumentSectionCoverageItem `json:"sections"`
}

// DocumentSectionCoverageItem binds one deterministic section envelope to the
// proposals or abstention returned for it.
type DocumentSectionCoverageItem struct {
	SectionID        string   `json:"section_id"`
	CandidateLocalID string   `json:"candidate_local_id"`
	Heading          string   `json:"heading,omitempty"`
	StartByte        int      `json:"start_byte"`
	EndByte          int      `json:"end_byte"`
	ContentHash      string   `json:"content_hash"`
	ResponseHash     string   `json:"response_hash"`
	ProposalLocalIDs []string `json:"proposal_local_ids"`
	Abstained        bool     `json:"abstained"`
}

// RepositoryGoplsCoverage records bounded LSP request/result coverage for one run.
// It is attempt diagnostic metadata, not an evidence proposal or a negative semantic fact.
type RepositoryGoplsCoverage struct {
	SchemaVersion                             string `json:"schema_version"`
	Scope                                     string `json:"scope"`
	RequestCoverageComplete                   bool   `json:"request_coverage_complete"`
	FailureStage                              string `json:"failure_stage,omitempty"`
	SelectedFileCount                         int    `json:"selected_file_count"`
	DocumentSymbolRequestCount                int    `json:"document_symbol_request_count"`
	DocumentSymbolFileCount                   int    `json:"document_symbol_file_count"`
	ParsedPackageClauseCount                  int    `json:"parsed_package_clause_count"`
	PackageRequestCount                       int    `json:"package_request_count"`
	PackageCompletedRequestCount              int    `json:"package_completed_request_count"`
	PackageResultFileCount                    int    `json:"package_result_file_count"`
	SupportedPackageFactCount                 int    `json:"supported_package_fact_count"`
	EmittedPackageFactCount                   int    `json:"emitted_package_fact_count"`
	ParsedIdentifierCount                     int    `json:"parsed_identifier_count"`
	GroundedDeclarationCount                  int    `json:"grounded_declaration_count"`
	DefinitionRequestCount                    int    `json:"definition_request_count"`
	DefinitionCompletedRequestCount           int    `json:"definition_completed_request_count"`
	DefinitionResultLocationCount             int    `json:"definition_result_location_count"`
	DefinitionUnselectedLocationCount         int    `json:"definition_unselected_location_count"`
	DefinitionSelfDeclarationLocationCount    int    `json:"definition_self_declaration_location_count"`
	DefinitionAmbiguousUsageCount             int    `json:"definition_ambiguous_usage_count"`
	SupportedCrossFileDefinitionCount         int    `json:"supported_cross_file_definition_count"`
	SupportedSameFileDefinitionCount          int    `json:"supported_same_file_definition_count"`
	ReferenceRequestCount                     int    `json:"reference_request_count"`
	ReferenceCompletedRequestCount            int    `json:"reference_completed_request_count"`
	ReferenceResultLocationCount              int    `json:"reference_result_location_count"`
	ReferenceUnselectedLocationCount          int    `json:"reference_unselected_location_count"`
	ReferenceSelfDeclarationLocationCount     int    `json:"reference_self_declaration_location_count"`
	ReferenceAmbiguousUsageCount              int    `json:"reference_ambiguous_usage_count"`
	SupportedCrossFileReferenceCount          int    `json:"supported_cross_file_reference_count"`
	SupportedSameFileReferenceCount           int    `json:"supported_same_file_reference_count"`
	CorroboratedRelationCount                 int    `json:"corroborated_relation_count"`
	DefinitionOnlyRelationCount               int    `json:"definition_only_relation_count"`
	ReferenceOnlyRelationCount                int    `json:"reference_only_relation_count"`
	CallableDeclarationCount                  int    `json:"callable_declaration_count"`
	PrepareCallHierarchyRequestCount          int    `json:"prepare_call_hierarchy_request_count"`
	PrepareCallHierarchyCompletedRequestCount int    `json:"prepare_call_hierarchy_completed_request_count"`
	PreparedCallHierarchyItemCount            int    `json:"prepared_call_hierarchy_item_count"`
	OutgoingCallRequestCount                  int    `json:"outgoing_call_request_count"`
	OutgoingCallCompletedRequestCount         int    `json:"outgoing_call_completed_request_count"`
	OutgoingCallResultCount                   int    `json:"outgoing_call_result_count"`
	OutgoingCallSiteCount                     int    `json:"outgoing_call_site_count"`
	OutgoingUnselectedCallResultCount         int    `json:"outgoing_unselected_call_result_count"`
	OutgoingUnselectedCallSiteCount           int    `json:"outgoing_unselected_call_site_count"`
	SupportedCrossFileCallCount               int    `json:"supported_cross_file_call_count"`
	SupportedSameFileCallCount                int    `json:"supported_same_file_call_count"`
	IncomingCallRequestCount                  int    `json:"incoming_call_request_count"`
	IncomingCallCompletedRequestCount         int    `json:"incoming_call_completed_request_count"`
	IncomingCallResultCount                   int    `json:"incoming_call_result_count"`
	IncomingCallSiteCount                     int    `json:"incoming_call_site_count"`
	IncomingUnselectedCallResultCount         int    `json:"incoming_unselected_call_result_count"`
	IncomingUnselectedCallSiteCount           int    `json:"incoming_unselected_call_site_count"`
	SupportedIncomingCrossFileCallCount       int    `json:"supported_incoming_cross_file_call_count"`
	SupportedIncomingSameFileCallCount        int    `json:"supported_incoming_same_file_call_count"`
	CorroboratedCallRelationCount             int    `json:"corroborated_call_relation_count"`
	OutgoingOnlyCallRelationCount             int    `json:"outgoing_only_call_relation_count"`
	IncomingOnlyCallRelationCount             int    `json:"incoming_only_call_relation_count"`
	EmittedCallRelationCount                  int    `json:"emitted_call_relation_count"`
	EmittedRelationCount                      int    `json:"emitted_relation_count"`
	MissingResultSemantics                    string `json:"missing_result_semantics"`
}

// ExtractorOutputInput submits transient extractor output for an existing source view.
type ExtractorOutputInput struct {
	RequestID           string
	SourceSnapshotID    string
	ExtractionViewID    string
	ExtractorDefinition ExtractorDefinitionInput
	Output              FrozenExtractorOutput
}

// TrustedExtractorRequest runs one trusted local extractor over an existing grounded view.
type TrustedExtractorRequest struct {
	RequestID           string
	ExtractionViewID    string
	RetryFailedAttempt  bool
	ExtractorDefinition ExtractorDefinitionInput
}

// ExtractorRunner is supplied by the caller; provider adapters implement it at the edge.
type ExtractorRunner func(context.Context, ExtractorInput) ([]byte, error)

// ExtractorProposalOutput is one transient extractor output item.
type ExtractorProposalOutput struct {
	ProposalLocalID string              `json:"proposal_local_id"`
	StatementText   string              `json:"statement_text"`
	EvidenceRefs    []string            `json:"evidence_refs"`
	CodeFact        *CodeFactOutput     `json:"code_fact,omitempty"`
	CodeRelation    *CodeRelationOutput `json:"code_relation,omitempty"`
}

// CodeFactOutput is source-bound candidate material emitted by a deterministic code extractor.
// Its byte and identity fields are not authoritative until controller verification succeeds.
type CodeFactOutput struct {
	SchemaVersion   string `json:"schema_version"`
	FactKind        string `json:"fact_kind"`
	RepoID          string `json:"repo_id"`
	CommitSHA       string `json:"commit_sha"`
	Path            string `json:"path"`
	FileContentHash string `json:"file_content_hash"`
	SymbolRef       string `json:"symbol_ref"`
	SymbolKind      string `json:"symbol_kind"`
	QualifiedName   string `json:"qualified_name"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	QuotedTextHash  string `json:"quoted_text_hash"`
}

// ResolvedCodeFact is a code fact after controller verification against immutable source bytes.
type ResolvedCodeFact struct {
	SchemaVersion   string `json:"schema_version"`
	FactKind        string `json:"fact_kind"`
	RepoID          string `json:"repo_id"`
	CommitSHA       string `json:"commit_sha"`
	Path            string `json:"path"`
	FileContentHash string `json:"file_content_hash"`
	SymbolRef       string `json:"symbol_ref"`
	SymbolKind      string `json:"symbol_kind"`
	QualifiedName   string `json:"qualified_name"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	QuotedTextHash  string `json:"quoted_text_hash"`
	QuotedText      string `json:"quoted_text"`
}

// CodeRelationOutput is a transient, source-bound typed relation emitted by a code extractor.
type CodeRelationOutput struct {
	SchemaVersion string                         `json:"schema_version"`
	RelationKind  string                         `json:"relation_kind"`
	RepoID        string                         `json:"repo_id"`
	CommitSHA     string                         `json:"commit_sha"`
	Caller        *CodeDeclarationEndpointOutput `json:"caller,omitempty"`
	Usage         CodeUsageSiteOutput            `json:"usage"`
	Target        CodeDeclarationEndpointOutput  `json:"target"`
}

// CodeUsageSiteOutput identifies the exact identifier bytes used at a relation site.
type CodeUsageSiteOutput struct {
	Path            string `json:"path"`
	FileContentHash string `json:"file_content_hash"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	QuotedTextHash  string `json:"quoted_text_hash"`
}

// CodeDeclarationEndpointOutput identifies one candidate declaration endpoint.
type CodeDeclarationEndpointOutput struct {
	Path            string `json:"path"`
	FileContentHash string `json:"file_content_hash"`
	SymbolRef       string `json:"symbol_ref"`
	SymbolKind      string `json:"symbol_kind"`
	QualifiedName   string `json:"qualified_name"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	QuotedTextHash  string `json:"quoted_text_hash"`
}

// ResolvedCodeRelation is a typed code relation after controller verification.
type ResolvedCodeRelation struct {
	SchemaVersion string                           `json:"schema_version"`
	RelationKind  string                           `json:"relation_kind"`
	RepoID        string                           `json:"repo_id"`
	CommitSHA     string                           `json:"commit_sha"`
	Caller        *ResolvedCodeDeclarationEndpoint `json:"caller,omitempty"`
	Usage         ResolvedCodeUsageSite            `json:"usage"`
	Target        ResolvedCodeDeclarationEndpoint  `json:"target"`
}

// ResolvedCodeUsageSite is one verified identifier usage endpoint.
type ResolvedCodeUsageSite struct {
	Path            string `json:"path"`
	FileContentHash string `json:"file_content_hash"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	QuotedTextHash  string `json:"quoted_text_hash"`
	QuotedText      string `json:"quoted_text"`
}

// ResolvedCodeDeclarationEndpoint is one verified declaration endpoint.
type ResolvedCodeDeclarationEndpoint struct {
	Path            string `json:"path"`
	FileContentHash string `json:"file_content_hash"`
	SymbolRef       string `json:"symbol_ref"`
	SymbolKind      string `json:"symbol_kind"`
	QualifiedName   string `json:"qualified_name"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	QuotedTextHash  string `json:"quoted_text_hash"`
	QuotedText      string `json:"quoted_text"`
}

// ExtractorInput is the grounded, read-only source view package for a trusted local extractor.
type ExtractorInput struct {
	SourceSnapshotID    string               `json:"source_snapshot_id"`
	ExtractionViewID    string               `json:"extraction_view_id"`
	SourceSystem        string               `json:"source_system"`
	SourceID            string               `json:"source_id"`
	SourceVersion       string               `json:"source_version"`
	RawContentHash      string               `json:"raw_content_hash"`
	Renderer            RendererRef          `json:"renderer"`
	RenderedContentHash string               `json:"rendered_content_hash"`
	RenderedText        string               `json:"rendered_text"`
	SpanCatalogVersion  string               `json:"span_catalog_version"`
	Spans               []ExtractorInputSpan `json:"spans"`
}

// RendererRef identifies the deterministic extraction view renderer.
type RendererRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ExtractorInputSpan is one grounded span exposed to a trusted local extractor.
type ExtractorInputSpan struct {
	SpanID         string `json:"span_id"`
	Text           string `json:"text"`
	StartByte      int    `json:"start_byte"`
	EndByte        int    `json:"end_byte"`
	DisplayLine    int    `json:"display_line"`
	QuotedTextHash string `json:"quoted_text_hash"`
}

// SourceSnapshot is the immutable source identity materialized from raw text.
type SourceSnapshot struct {
	ID             string
	SourceSystem   string
	SourceID       string
	SourceVersion  string
	RawContentHash string
	OriginMetadata map[string]string
}

// ExtractionView is a deterministic rendered view of a source snapshot.
type ExtractionView struct {
	ID                  string
	SourceSnapshotID    string
	RendererName        string
	RendererVersion     string
	Rendered            []byte
	RenderedContentHash string
}

// SpanEntry is one deterministic source span over extraction view bytes.
type SpanEntry struct {
	ExtractionViewID   string `json:"extraction_view_id"`
	SpanID             string `json:"span_id"`
	SpanCatalogVersion string `json:"span_catalog_version"`
	StartByte          int    `json:"start_byte"`
	EndByte            int    `json:"end_byte"`
	DisplayLine        int    `json:"display_line"`
	QuotedTextHash     string `json:"quoted_text_hash"`
	QuotedText         string `json:"quoted_text"`
}

// ExtractorDefinitionInput identifies an extractor implementation/config before persistence.
// The zero value preserves the Slice 1 frozen fixture definition.
type ExtractorDefinitionInput struct {
	Name    string            `json:"name,omitempty"`
	Version string            `json:"version,omitempty"`
	Config  map[string]string `json:"config,omitempty"`
}

// ExtractorDefinition identifies the extractor implementation/config.
type ExtractorDefinition struct {
	ID         string
	Name       string
	Version    string
	ConfigHash string
	Config     map[string]string
}

// ExtractionRun identifies one logical extraction request.
type ExtractionRun struct {
	ID                    string `json:"extraction_run_id"`
	RequestID             string `json:"request_id"`
	ExtractorDefinitionID string `json:"extractor_definition_id"`
	SourceSnapshotID      string `json:"source_snapshot_id,omitempty"`
	ExtractionViewID      string `json:"extraction_view_id,omitempty"`
	RepositorySnapshotID  string `json:"repository_snapshot_id,omitempty"`
}

// RepositoryExtractionRunRequest identifies one logical repository extraction request.
type RepositoryExtractionRunRequest struct {
	RequestID            string                   `json:"request_id"`
	RepositorySnapshotID string                   `json:"repository_snapshot_id"`
	ExtractorDefinition  ExtractorDefinitionInput `json:"extractor_definition,omitempty"`
}

// RepositoryExtractionRunResult reports a newly persisted or replayed repository run.
type RepositoryExtractionRunResult struct {
	ExtractionRun ExtractionRun `json:"extraction_run"`
	Replayed      bool          `json:"replayed"`
}

// ExtractionAttempt identifies one concrete fixture/model/parser invocation.
type ExtractionAttempt struct {
	ID         string
	RunID      string
	Number     int
	Status     string
	OutputHash string
}

// ProposalBatch is the formal materialized batch envelope for Slice 1.
type ProposalBatch struct {
	ID                  string
	ExtractionAttemptID string
	Status              string
}

// ResolvedSourceRef is an authoritative source reference resolved by controller code.
type ResolvedSourceRef struct {
	TargetKind           string `json:"target_kind,omitempty"`
	ExtractionViewID     string `json:"extraction_view_id,omitempty"`
	RepositorySnapshotID string `json:"repository_snapshot_id,omitempty"`
	FileSnapshotID       string `json:"file_snapshot_id,omitempty"`
	RepoID               string `json:"repo_id,omitempty"`
	CommitSHA            string `json:"commit_sha,omitempty"`
	Path                 string `json:"path,omitempty"`
	SpanID               string `json:"span_id"`
	StartByte            int    `json:"start_byte"`
	EndByte              int    `json:"end_byte"`
	QuotedTextHash       string `json:"quoted_text_hash"`
	QuotedText           string `json:"quoted_text"`
}

// RepositoryGoParserRequest identifies one repository-wide deterministic extraction run.
type RepositoryGoParserRequest struct {
	RequestID            string `json:"request_id"`
	RepositorySnapshotID string `json:"repository_snapshot_id"`
	RetryFailedAttempt   bool   `json:"retry_failed_attempt,omitempty"`
}

// RepositoryGoplsRequest identifies one repository-wide real gopls extraction run.
type RepositoryGoplsRequest struct {
	RequestID            string `json:"request_id"`
	RepositorySnapshotID string `json:"repository_snapshot_id"`
	WorkspaceRoot        string `json:"workspace_root"`
	RetryFailedAttempt   bool   `json:"retry_failed_attempt,omitempty"`
}

// RepositoryIngestResult reports one repository-scoped attempt, proposal batch, and candidate generation.
type RepositoryIngestResult struct {
	RepositorySnapshotID     string                     `json:"repository_snapshot_id"`
	ExtractionRunID          string                     `json:"extraction_run_id"`
	ExtractionAttemptID      string                     `json:"extraction_attempt_id"`
	ProposalBatchID          string                     `json:"proposal_batch_id"`
	ProposalOccurrenceIDs    []string                   `json:"proposal_occurrence_ids"`
	ProposalCount            int                        `json:"proposal_count"`
	OutputHash               string                     `json:"output_hash"`
	SourceGeneration         RepositorySourceGeneration `json:"source_generation"`
	SourceGenerationReplayed bool                       `json:"source_generation_replayed"`
	RepositoryGoplsCoverage  *RepositoryGoplsCoverage   `json:"repository_gopls_coverage,omitempty"`
	DeltaExtraction          *RepositoryDeltaExtraction `json:"delta_extraction,omitempty"`
	Replayed                 bool                       `json:"replayed"`
}

// RepositoryDeltaExtraction records one adapter-proven delta decision and its full-snapshot verification.
type RepositoryDeltaExtraction struct {
	ID                        string `json:"delta_extraction_id"`
	AdapterName               string `json:"adapter_name"`
	AdapterVersion            string `json:"adapter_version"`
	RevisionAuthorityContract string `json:"revision_authority_contract"`
	FileSetAuthorityContract  string `json:"file_set_authority_contract"`
	EndpointAuthorityContract string `json:"endpoint_authority_contract"`
	Decision                  string `json:"decision"`
	FallbackReason            string `json:"fallback_reason,omitempty"`
	RepoID                    string `json:"repo_id"`
	ExtractorName             string `json:"extractor_name"`
	ExtractorDefinitionID     string `json:"extractor_definition_id"`
	BaseSourceGenerationID    string `json:"base_source_generation_id,omitempty"`
	BaseRepositorySnapshotID  string `json:"base_repository_snapshot_id,omitempty"`
	RepositorySnapshotID      string `json:"repository_snapshot_id"`
	ExtractionRunID           string `json:"extraction_run_id"`
	ExtractionAttemptID       string `json:"extraction_attempt_id"`
	ProposalBatchID           string `json:"proposal_batch_id"`
	AddedFileCount            int    `json:"added_file_count"`
	ModifiedFileCount         int    `json:"modified_file_count"`
	DeletedFileCount          int    `json:"deleted_file_count"`
	UnchangedFileCount        int    `json:"unchanged_file_count"`
	DeltaParsedFileCount      int    `json:"delta_parsed_file_count"`
	FullVerifiedFileCount     int    `json:"full_verified_file_count"`
	RevisionProofHash         string `json:"revision_proof_hash"`
	FileSetProofHash          string `json:"file_set_proof_hash"`
	EndpointProofHash         string `json:"endpoint_proof_hash"`
	CandidateOutputHash       string `json:"candidate_output_hash,omitempty"`
	FullOutputHash            string `json:"full_output_hash"`
	SelectedOutputHash        string `json:"selected_output_hash"`
	Replayed                  bool   `json:"replayed"`
}

// RepositorySourceGeneration binds one immutable repository extraction result to an append-only stream position.
type RepositorySourceGeneration struct {
	ID                    string `json:"source_generation_id"`
	RepoID                string `json:"repo_id"`
	ExtractorName         string `json:"extractor_name"`
	ExtractorDefinitionID string `json:"extractor_definition_id"`
	Number                int    `json:"generation_number"`
	RepositorySnapshotID  string `json:"repository_snapshot_id"`
	CommitSHA             string `json:"commit_sha"`
	ExtractionAttemptID   string `json:"extraction_attempt_id"`
	ProposalBatchID       string `json:"proposal_batch_id"`
	ExtractorOutputHash   string `json:"extractor_output_hash"`
	ProposalCount         int    `json:"proposal_count"`
}

// RepositorySourceGenerationCreateInput selects one completed repository proposal batch.
type RepositorySourceGenerationCreateInput struct {
	ProposalBatchID string `json:"proposal_batch_id"`
}

// RepositorySourceGenerationCreateResult reports a newly persisted or replayed generation.
type RepositorySourceGenerationCreateResult struct {
	Generation RepositorySourceGeneration `json:"generation"`
	Replayed   bool                       `json:"replayed"`
}

// RepositorySourceGenerationActivationInput advances one repository/extractor stream head.
type RepositorySourceGenerationActivationInput struct {
	RequestID          string `json:"request_id"`
	SourceGenerationID string `json:"source_generation_id"`
}

// RepositorySourceGenerationActivationResult records one atomic head transition.
type RepositorySourceGenerationActivationResult struct {
	RequestID             string                             `json:"request_id"`
	RepoID                string                             `json:"repo_id"`
	ExtractorName         string                             `json:"extractor_name"`
	ExtractorDefinitionID string                             `json:"extractor_definition_id"`
	PreviousGenerationID  string                             `json:"previous_generation_id,omitempty"`
	ActivatedGenerationID string                             `json:"activated_generation_id"`
	Changed               bool                               `json:"changed"`
	Replayed              bool                               `json:"replayed"`
	Reconciliation        RepositoryGenerationReconciliation `json:"reconciliation"`
}

// RepositoryGenerationReconciliation summarizes the exact proposal transition for one activated generation.
type RepositoryGenerationReconciliation struct {
	SourceGenerationID   string `json:"source_generation_id"`
	PreviousGenerationID string `json:"previous_generation_id,omitempty"`
	IdentityContract     string `json:"identity_contract"`
	UnchangedCount       int    `json:"unchanged_count"`
	NewCount             int    `json:"new_count"`
	StaleCount           int    `json:"stale_count"`
}

// RepositoryProposalLifecycle projects one proposal occurrence into the current stream-head transition.
type RepositoryProposalLifecycle struct {
	SourceGenerationID           string `json:"source_generation_id"`
	State                        string `json:"state"`
	IdentityContract             string `json:"identity_contract"`
	ProposalIdentity             string `json:"proposal_identity"`
	CurrentProposalOccurrenceID  string `json:"current_proposal_occurrence_id,omitempty"`
	PreviousProposalOccurrenceID string `json:"previous_proposal_occurrence_id,omitempty"`
}

// ProposalOccurrence is one proposal occurrence from one batch and attempt.
type ProposalOccurrence struct {
	ID                         string
	BatchID                    string
	ExtractionAttemptID        string
	ProposalLocalID            string
	ProposalKind               string
	StatementText              string
	ProposalFingerprint        string
	ProposalFingerprintVersion string
	AdmissionOutcome           string
	CanonicalRef               string
	SourceRefs                 []ResolvedSourceRef
	CodeFact                   *ResolvedCodeFact
	CodeRelation               *ResolvedCodeRelation
	ProposedPayload            map[string]any
}

// MaterializedBatch is a formal EvidenceProposalBatch for Slice 1.
type MaterializedBatch struct {
	SourceSnapshot          SourceSnapshot
	ExtractionView          ExtractionView
	Spans                   []SpanEntry
	ExtractorDefinition     ExtractorDefinition
	ExtractionRun           ExtractionRun
	ExtractionAttempt       ExtractionAttempt
	ProposalBatch           ProposalBatch
	Occurrences             []ProposalOccurrence
	RepositoryGoplsCoverage *RepositoryGoplsCoverage
	FixtureOutputHash       string
}

// IngestResult is returned after one source-bound ingestion attempt.
type IngestResult struct {
	ProposalOccurrenceID string
	ProposalFingerprint  string
	ProposalCount        int
	ExtractionAttemptID  string
	ExtractionRunID      string
	SourceSnapshotID     string
	ExtractionViewID     string
	Replayed             bool
}

// ProposalQueryResult is the internal query result for a proposal occurrence.
type ProposalQueryResult struct {
	ProposalOccurrenceID       string
	ProposalLocalID            string
	ProposalFingerprint        string
	ProposalFingerprintVersion string
	ProposalKind               string
	StatementText              string
	AdmissionOutcome           string
	CanonicalRef               string
	SourceRefs                 []ResolvedSourceRef
	CodeFact                   *ResolvedCodeFact
	CodeRelation               *ResolvedCodeRelation
	ExtractionAttemptID        string
	ExtractionAttemptStatus    string
	ExtractionRunID            string
	ExtractorDefinitionID      string
	ExtractorName              string
	ExtractorVersion           string
	ExtractorConfigHash        string
	SourceBindingKind          string
	ExtractionViewID           string
	RendererName               string
	RendererVersion            string
	RenderedContentHash        string
	SourceSnapshotID           string
	SourceSystem               string
	SourceID                   string
	SourceVersion              string
	RawContentHash             string
	OriginMetadata             map[string]string
	RepositorySnapshot         *RepositorySnapshot
	SourceGeneration           *RepositorySourceGeneration
	SourceGenerationActive     bool
	RepositoryLifecycle        *RepositoryProposalLifecycle
}

// ProposalListInput is the bounded read-only filter for proposal records.
type ProposalListInput struct {
	SourceSnapshotID     string
	RepositorySnapshotID string
	SourceGenerationID   string
	SourceID             string
	SourceVersion        string
	AdmissionOutcome     string
	LifecycleScope       string
	Limit                int
}

// ProposalSearchInput is a bounded lexical query over persisted proposal statements.
type ProposalSearchInput struct {
	Query string
	ProposalListInput
}

// ProposalSearchResult preserves the grounded proposal record and its PostgreSQL text rank.
type ProposalSearchResult struct {
	Record ProposalQueryResult
	Rank   float64
}

// GroundedEvidenceBriefInput bounds one deterministic brief over persisted proposal statements.
type GroundedEvidenceBriefInput struct {
	Query                    string
	QueryMode                string
	IncludeSourceContext     bool
	IncludeRepositoryContext bool
	ProposalListInput
}

// GroundedEvidenceBriefCoverage preserves one matched extraction attempt's persisted diagnostics.
type GroundedEvidenceBriefCoverage struct {
	ExtractionAttemptID     string
	RepositoryGoplsCoverage RepositoryGoplsCoverage
}

// GroundedEvidenceBriefQueryResult contains persisted matches and their exact extraction coverage.
type GroundedEvidenceBriefQueryResult struct {
	Matches            []ProposalSearchResult
	Coverage           []GroundedEvidenceBriefCoverage
	Execution          EvidenceQueryExecution
	SourceContexts     []GroundedEvidenceSourceContext
	RepositoryContexts []GroundedEvidenceRepositoryContext
}

// CanonicalRelationQueryResult is one canonical edge with both grounded endpoint records.
type CanonicalRelationQueryResult struct {
	Edge           CanonicalGraphEdge
	From           CanonicalQueryResult
	To             CanonicalQueryResult
	OriginProposal ProposalQueryResult
}

// CanonicalNeighborInput bounds one-hop canonical graph lookup.
type CanonicalNeighborInput struct {
	CanonicalID string
	Direction   string
	Relation    string
	Limit       int
}

// CanonicalNeighborResult identifies one canonical edge adjacent to the root.
type CanonicalNeighborResult struct {
	Directions []string
	Relation   CanonicalRelationQueryResult
	Neighbor   CanonicalQueryResult
}

// RepositoryRelationNeighborInput bounds one-hop repository relation lookup by exact symbol identity.
type RepositoryRelationNeighborInput struct {
	SymbolRef          string
	Direction          string
	RelationKind       string
	LifecycleScope     string
	SourceGenerationID string
	Limit              int
}

// RepositoryRelationNeighborResult identifies one grounded typed relation adjacent to a symbol.
type RepositoryRelationNeighborResult struct {
	Directions          []string
	Relation            ProposalQueryResult
	NeighborDeclaration *ResolvedCodeDeclarationEndpoint
	NeighborUsage       *ResolvedCodeUsageSite
}

// AdmissionInput admits one pending proposal occurrence into the canonical graph tables.
type AdmissionInput struct {
	ProposalOccurrenceID string
	DecisionBy           string
	DecisionReason       string
}

// ProposalDispositionInput records a terminal non-canonical outcome for one pending proposal.
type ProposalDispositionInput struct {
	ProposalOccurrenceID string
	Outcome              string
	DecisionBy           string
	DecisionReason       string
}

// AdmissionResult records one proposal admission and its canonical graph mutation.
type AdmissionResult struct {
	ProposalOccurrenceID string
	AdmissionDecisionID  string
	AdmissionOutcome     string
	CanonicalRef         string
	RawEvidenceNodeIDs   []string
	CanonicalEdgeIDs     []string
	Replayed             bool
}

// ProposalDispositionResult records one immutable rejected or audit-only decision.
type ProposalDispositionResult struct {
	ProposalOccurrenceID string
	AdmissionDecisionID  string
	AdmissionOutcome     string
	DecisionBy           string
	DecisionReason       string
	Replayed             bool
}

// CanonicalGraphNode is the DB-facing persisted form of an evidencegraph canonical node.
type CanonicalGraphNode struct {
	ID                         string
	Kind                       evidencegraph.CanonicalNodeKind
	Payload                    evidencegraph.EvidencePayload
	Provenance                 evidencegraph.ProvenanceRecord
	Temporal                   evidencegraph.TemporalRecord
	Integrity                  evidencegraph.IntegrityRecord
	OriginProposalOccurrenceID string
}

// CanonicalQueryResult is the external-query shape for one admitted canonical node.
type CanonicalQueryResult struct {
	CanonicalID                string
	NodeKind                   evidencegraph.CanonicalNodeKind
	Payload                    evidencegraph.EvidencePayload
	Provenance                 evidencegraph.ProvenanceRecord
	Temporal                   evidencegraph.TemporalRecord
	Integrity                  evidencegraph.IntegrityRecord
	OriginProposalOccurrenceID string
	OriginProposal             ProposalQueryResult
}

// CanonicalGraphEdge is the DB-facing persisted form of an evidencegraph canonical edge.
type CanonicalGraphEdge struct {
	ID                         string
	From                       string
	To                         string
	Relation                   evidencegraph.CanonicalEdgeRelation
	Provenance                 evidencegraph.ProvenanceRecord
	OriginProposalOccurrenceID string
}
