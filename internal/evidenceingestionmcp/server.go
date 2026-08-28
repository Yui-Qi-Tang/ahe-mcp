// Package evidenceingestionmcp exposes the Slice 2 internal ingest tool.
package evidenceingestionmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ToolSubmitManualEvidence submits bounded manual text evidence and extractor output.
	ToolSubmitManualEvidence = "submit_manual_evidence"
	// ToolSubmitTextSource is the Slice 4 source-only intake tool.
	ToolSubmitTextSource = "submit_text_source"
	// ToolSubmitExternalSource accepts one provider-neutral external source envelope.
	ToolSubmitExternalSource = "submit_external_source"
	// ToolSubmitExtractorOutput is the Slice 5 proposal-producer handoff tool.
	ToolSubmitExtractorOutput = "submit_extractor_output"
	// ToolGetExtractorInput is the Slice 6 grounded producer-input handoff tool.
	ToolGetExtractorInput = "get_extractor_input"
	// ToolInspectGoplsWorkspace returns bounded package and file inventory without persistence.
	ToolInspectGoplsWorkspace = "inspect_gopls_workspace"
	// ToolInspectGitRepositoryChange returns one read-only Git HEAD and dirty-worktree change token.
	ToolInspectGitRepositoryChange = "inspect_git_repository_change"
	// ToolObserveGitRepositoryChange persists one idempotent Git change observation and stability decision.
	ToolObserveGitRepositoryChange = "observe_git_repository_change"
	// ToolScheduleGitRepositoryExtractionWork schedules one stable clean observation for an extractor stream.
	ToolScheduleGitRepositoryExtractionWork = "schedule_git_repository_extraction_work"
	// ToolClaimGitRepositoryExtractionWork atomically claims pending work for an extractor stream.
	ToolClaimGitRepositoryExtractionWork = "claim_git_repository_extraction_work"
	// ToolRenewGitRepositoryExtractionWorkLease extends one exact running claim before expiry.
	ToolRenewGitRepositoryExtractionWorkLease = "renew_git_repository_extraction_work_lease"
	// ToolExecuteClaimedGitRepositoryExtractionWork runs one exact claimed work item with inherited lease heartbeat.
	ToolExecuteClaimedGitRepositoryExtractionWork = "execute_claimed_git_repository_extraction_work"
	// ToolRunGitRepositoryExtractionWorkerTick claims and optionally executes one item from one stream.
	ToolRunGitRepositoryExtractionWorkerTick = "run_git_repository_extraction_worker_tick"
	// ToolListExpiredGitRepositoryExtractionWorkClaims returns bounded elapsed-lease recovery candidates.
	ToolListExpiredGitRepositoryExtractionWorkClaims = "list_expired_git_repository_extraction_work_claims"
	// ToolListExpiredGitRepositoryExtractionWorkExecutions returns bounded execution-owned repair candidates.
	ToolListExpiredGitRepositoryExtractionWorkExecutions = "list_expired_git_repository_extraction_work_executions"
	// ToolFinishGitRepositoryExtractionWork records one claimed work item's terminal outcome.
	ToolFinishGitRepositoryExtractionWork = "finish_git_repository_extraction_work"
	// ToolRecoverExpiredGitRepositoryExtractionWork releases one exact claim after its lease expires.
	ToolRecoverExpiredGitRepositoryExtractionWork = "recover_expired_git_repository_extraction_work"
	// ToolRepairExpiredGitRepositoryExtractionWorkExecution repairs one exact expired execution owner.
	ToolRepairExpiredGitRepositoryExtractionWorkExecution = "repair_expired_git_repository_extraction_work_execution"
	// ToolRetryFailedGitRepositoryExtractionWork releases one exact failed claim for another attempt.
	ToolRetryFailedGitRepositoryExtractionWork = "retry_failed_git_repository_extraction_work"
	// ToolClassifyFailedGitRepositoryExtractionWork records policy v1 for one exact failed claim.
	ToolClassifyFailedGitRepositoryExtractionWork = "classify_failed_git_repository_extraction_work"
	// ToolListDueGitRepositoryExtractionWorkRetryDecisions returns bounded due retry candidates.
	ToolListDueGitRepositoryExtractionWorkRetryDecisions = "list_due_git_repository_extraction_work_retry_decisions"
	// ToolConsumeDueGitRepositoryExtractionWorkRetryDecision consumes one exact due retry decision.
	ToolConsumeDueGitRepositoryExtractionWorkRetryDecision = "consume_due_git_repository_extraction_work_retry_decision"
	// ToolRunDueGitRepositoryExtractionWorkRetryControllerTick consumes one bounded due retry batch.
	ToolRunDueGitRepositoryExtractionWorkRetryControllerTick = "run_due_git_repository_extraction_work_retry_controller_tick"
	// ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick repairs one bounded expired-work batch.
	ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick = "run_expired_git_repository_extraction_work_maintenance_tick"
	// ToolCaptureGitRepositorySnapshot persists one verified Git commit and tracked Go files.
	ToolCaptureGitRepositorySnapshot = "capture_git_repository_snapshot"
	// ToolGetRepositoryExtractorInput loads immutable multi-file repository input.
	ToolGetRepositoryExtractorInput = "get_repository_extractor_input"
	// ToolCreateRepositoryExtractionRun creates a repository-bound logical run.
	ToolCreateRepositoryExtractionRun = "create_repository_extraction_run"
	// ToolRunRepositoryGoParserExtractor runs deterministic declarations over a repository snapshot.
	ToolRunRepositoryGoParserExtractor = "run_repository_go_parser_extractor"
	// ToolRunRepositoryGoplsExtractor runs repository gopls facts, outgoing-primary relations, and semantic corroboration.
	ToolRunRepositoryGoplsExtractor = "run_repository_gopls_extractor"
	// ToolListRepositorySourceGenerations lists recent generations for one exact repository/extractor stream.
	ToolListRepositorySourceGenerations = "list_repository_source_generations"
	// ToolActivateRepositorySourceGeneration advances one exact repository/extractor stream head.
	ToolActivateRepositorySourceGeneration = "activate_repository_source_generation"
	// ToolAdmitPendingProposal admits one pending proposal into the canonical graph.
	ToolAdmitPendingProposal = "admit_pending_proposal"
	// ToolRecordPendingProposalDisposition records one rejected or audit-only operator decision.
	ToolRecordPendingProposalDisposition = "record_pending_proposal_disposition"
	// ToolSubmitCanonicalContradictionProposal proposes a human-reviewable contradiction between admitted nodes.
	ToolSubmitCanonicalContradictionProposal = "submit_canonical_contradiction_proposal"
	// ToolAdmitPendingCanonicalContradiction admits one reviewed contradiction proposal.
	ToolAdmitPendingCanonicalContradiction = "admit_pending_canonical_contradiction"
	// ToolRecordPendingCanonicalContradictionDisposition records a rejected or audit-only contradiction decision.
	ToolRecordPendingCanonicalContradictionDisposition = "record_pending_canonical_contradiction_disposition"
	// ToolAdmitPendingSupersession atomically admits one fresh replacement claim and its reviewed supersedes edges.
	ToolAdmitPendingSupersession = "admit_pending_supersession"
	// ToolRunLocalOllamaExtractor invokes a trusted local Ollama extractor over a saved source view.
	ToolRunLocalOllamaExtractor = "run_local_ollama_extractor"
	// ToolRunGoParserExtractor invokes a deterministic Go declaration extractor over a saved source view.
	ToolRunGoParserExtractor = "run_go_parser_extractor"
	// ToolRunGoplsExtractor invokes a real local gopls declaration extractor over a saved source view.
	ToolRunGoplsExtractor = "run_gopls_extractor"

	toolErrorInvalidRequest = "invalid_request"
	toolErrorUnknownTool    = "unknown_tool"
	toolErrorInternal       = "internal_error"
)

// ToolDefinition describes one exposed internal MCP-shaped tool.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Write       bool   `json:"write"`
}

// SubmitManualEvidenceRequest is the bounded Slice 2 manual ingest input.
type SubmitManualEvidenceRequest struct {
	SourceID        string                                  `json:"source_id"`
	SourceVersion   string                                  `json:"source_version"`
	RawText         string                                  `json:"raw_text"`
	OriginMetadata  map[string]string                       `json:"origin_metadata,omitempty"`
	RequestID       string                                  `json:"request_id"`
	ExtractorOutput evidenceingestion.FrozenExtractorOutput `json:"extractor_output"`
}

// SubmitTextSourceRequest is the bounded Slice 4 source-only intake input.
type SubmitTextSourceRequest struct {
	RequestID      string            `json:"request_id,omitempty"`
	SourceSystem   string            `json:"source_system,omitempty"`
	SourceID       string            `json:"source_id"`
	SourceVersion  string            `json:"source_version"`
	RawText        string            `json:"raw_text"`
	OriginMetadata map[string]string `json:"origin_metadata,omitempty"`
}

// SubmitExternalSourceRequest is the provider-neutral external source envelope.
type SubmitExternalSourceRequest = evidenceingestion.ExternalSourceEnvelopeV1

// SubmitExtractorOutputRequest submits one identified producer's output for an existing source view.
type SubmitExtractorOutputRequest struct {
	RequestID           string                                     `json:"request_id"`
	SourceSnapshotID    string                                     `json:"source_snapshot_id"`
	ExtractionViewID    string                                     `json:"extraction_view_id"`
	ProducerSessionRef  string                                     `json:"producer_session_ref,omitempty"`
	ExtractorDefinition evidenceingestion.ExtractorDefinitionInput `json:"extractor_definition,omitempty"`
	ExtractorOutput     evidenceingestion.FrozenExtractorOutput    `json:"extractor_output"`
}

// GetExtractorInputRequest loads one grounded source view for a trusted proposal producer.
type GetExtractorInputRequest struct {
	ExtractionViewID string `json:"extraction_view_id"`
}

// InspectGoplsWorkspaceRequest identifies one repository workspace at one revision.
type InspectGoplsWorkspaceRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	RepoID        string `json:"repo_id"`
	CommitSHA     string `json:"commit_sha"`
}

// InspectGitRepositoryChangeRequest identifies one local Git worktree without selecting Git execution controls.
type InspectGitRepositoryChangeRequest struct {
	WorkspaceRoot string `json:"workspace_root"`
	RepoID        string `json:"repo_id"`
}

// ObserveGitRepositoryChangeRequest identifies one persisted Git observation and caller-owned stability policy.
type ObserveGitRepositoryChangeRequest struct {
	RequestID                   string `json:"request_id"`
	WorkspaceRoot               string `json:"workspace_root"`
	RepoID                      string `json:"repo_id"`
	StabilityWindowMilliseconds int64  `json:"stability_window_milliseconds"`
}

// ScheduleGitRepositoryExtractionWorkRequest selects one stable observation and extractor stream.
type ScheduleGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkScheduleInput

// ClaimGitRepositoryExtractionWorkRequest selects one extractor stream for a worker claim.
type ClaimGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkClaimInput

// RenewGitRepositoryExtractionWorkLeaseRequest identifies one exact running claim to extend.
type RenewGitRepositoryExtractionWorkLeaseRequest = evidenceingestion.RepositoryExtractionWorkLeaseRenewalInput

// ExecuteClaimedGitRepositoryExtractionWorkRequest identifies one exact running claim to execute.
type ExecuteClaimedGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkExecutionInput

// RunGitRepositoryExtractionWorkerTickRequest selects one stream for one bounded worker tick.
type RunGitRepositoryExtractionWorkerTickRequest = evidenceingestion.RepositoryExtractionWorkerTickInput

// ListExpiredGitRepositoryExtractionWorkClaimsRequest selects one extractor stream to inspect.
type ListExpiredGitRepositoryExtractionWorkClaimsRequest = evidenceingestion.RepositoryExtractionWorkExpiredClaimListInput

// ListExpiredGitRepositoryExtractionWorkExecutionsRequest bounds one cross-stream execution-owner scan.
type ListExpiredGitRepositoryExtractionWorkExecutionsRequest = evidenceingestion.RepositoryExtractionWorkExpiredExecutionListInput

// FinishGitRepositoryExtractionWorkRequest records one verified success or bounded failure.
type FinishGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkFinishInput

// RecoverExpiredGitRepositoryExtractionWorkRequest identifies one exact expired claim.
type RecoverExpiredGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkRecoveryInput

// RepairExpiredGitRepositoryExtractionWorkExecutionRequest identifies one exact expired execution owner.
type RepairExpiredGitRepositoryExtractionWorkExecutionRequest = evidenceingestion.RepositoryExtractionWorkExecutionRepairInput

// RetryFailedGitRepositoryExtractionWorkRequest identifies one exact terminal failed claim.
type RetryFailedGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkRetryInput

// ClassifyFailedGitRepositoryExtractionWorkRequest identifies one exact terminal failed claim for policy classification.
type ClassifyFailedGitRepositoryExtractionWorkRequest = evidenceingestion.RepositoryExtractionWorkFailurePolicyInput

// ListDueGitRepositoryExtractionWorkRetryDecisionsRequest bounds one cross-stream due-decision scan.
type ListDueGitRepositoryExtractionWorkRetryDecisionsRequest = evidenceingestion.RepositoryExtractionWorkDueRetryDecisionListInput

// ConsumeDueGitRepositoryExtractionWorkRetryDecisionRequest identifies one exact persisted retry decision.
type ConsumeDueGitRepositoryExtractionWorkRetryDecisionRequest = evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionInput

// RunDueGitRepositoryExtractionWorkRetryControllerTickRequest identifies one durable bounded controller run.
type RunDueGitRepositoryExtractionWorkRetryControllerTickRequest = evidenceingestion.RepositoryExtractionWorkRetryControllerTickInput

// RunExpiredGitRepositoryExtractionWorkMaintenanceTickRequest identifies one durable bounded maintenance run.
type RunExpiredGitRepositoryExtractionWorkMaintenanceTickRequest = evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput

// CaptureGitRepositorySnapshotRequest identifies one canonical Git revision intake.
type CaptureGitRepositorySnapshotRequest struct {
	RequestID     string `json:"request_id"`
	WorkspaceRoot string `json:"workspace_root"`
	RepoID        string `json:"repo_id"`
	CommitSHA     string `json:"commit_sha"`
}

// GetRepositoryExtractorInputRequest identifies one persisted repository snapshot.
type GetRepositoryExtractorInputRequest struct {
	RepositorySnapshotID string `json:"repository_snapshot_id"`
}

// CreateRepositoryExtractionRunRequest identifies one repository-bound logical run.
type CreateRepositoryExtractionRunRequest struct {
	RequestID            string                                     `json:"request_id"`
	RepositorySnapshotID string                                     `json:"repository_snapshot_id"`
	ExtractorDefinition  evidenceingestion.ExtractorDefinitionInput `json:"extractor_definition,omitempty"`
}

// CreateRepositoryExtractionRunResponse reports a newly created or replayed run.
type CreateRepositoryExtractionRunResponse struct {
	ExtractionRun evidenceingestion.ExtractionRun `json:"extraction_run"`
	Replayed      bool                            `json:"replayed"`
}

// RunRepositoryGoParserExtractorRequest identifies one repository-wide deterministic extraction.
type RunRepositoryGoParserExtractorRequest = evidenceingestion.RepositoryGoParserRequest

// RunRepositoryGoParserExtractorResponse reports one persisted repository proposal batch.
type RunRepositoryGoParserExtractorResponse = evidenceingestion.RepositoryIngestResult

// RunRepositoryGoplsExtractorRequest identifies one repository-wide gopls extraction.
type RunRepositoryGoplsExtractorRequest = evidenceingestion.RepositoryGoplsRequest

// RunRepositoryGoplsExtractorResponse reports one persisted repository batch and semantic coverage.
type RunRepositoryGoplsExtractorResponse = evidenceingestion.RepositoryIngestResult

// ListRepositorySourceGenerationsRequest selects one exact repository/extractor stream.
type ListRepositorySourceGenerationsRequest = evidenceingestion.RepositorySourceGenerationListInput

// ListRepositorySourceGenerationsResponse reports bounded immutable generation metadata.
type ListRepositorySourceGenerationsResponse struct {
	Generations []evidenceingestion.RepositorySourceGenerationStatus `json:"generations"`
	Count       int                                                  `json:"count"`
	Limit       int                                                  `json:"limit"`
}

// ActivateRepositorySourceGenerationRequest selects one exact repository generation.
type ActivateRepositorySourceGenerationRequest = evidenceingestion.RepositorySourceGenerationActivationInput

// ActivateRepositorySourceGenerationResponse records one atomic repository head transition.
type ActivateRepositorySourceGenerationResponse = evidenceingestion.RepositorySourceGenerationActivationResult

// RunLocalOllamaExtractorRequest invokes local Ollama over an existing grounded source view.
type RunLocalOllamaExtractorRequest struct {
	RequestID          string `json:"request_id"`
	ExtractionViewID   string `json:"extraction_view_id"`
	Model              string `json:"model"`
	BaseURL            string `json:"base_url,omitempty"`
	RetryFailedAttempt bool   `json:"retry_failed_attempt,omitempty"`
	NumPredict         int    `json:"num_predict,omitempty"`
}

// RunGoParserExtractorRequest invokes the deterministic Go declaration extractor.
type RunGoParserExtractorRequest struct {
	RequestID          string `json:"request_id"`
	ExtractionViewID   string `json:"extraction_view_id"`
	RepoID             string `json:"repo_id"`
	CommitSHA          string `json:"commit_sha"`
	Path               string `json:"path"`
	RetryFailedAttempt bool   `json:"retry_failed_attempt,omitempty"`
}

// RunGoplsExtractorRequest invokes real local gopls over one grounded Go file.
type RunGoplsExtractorRequest struct {
	RequestID          string `json:"request_id"`
	ExtractionViewID   string `json:"extraction_view_id"`
	WorkspaceRoot      string `json:"workspace_root"`
	RepoID             string `json:"repo_id"`
	CommitSHA          string `json:"commit_sha"`
	Path               string `json:"path"`
	RetryFailedAttempt bool   `json:"retry_failed_attempt,omitempty"`
}

// AdmitPendingProposalRequest admits one pending proposal occurrence.
type AdmitPendingProposalRequest struct {
	ProposalOccurrenceID string                      `json:"proposal_occurrence_id"`
	DecisionBy           string                      `json:"decision_by,omitempty"`
	DecisionReason       string                      `json:"decision_reason,omitempty"`
	Derivation           *DerivationAdmissionRequest `json:"derivation,omitempty"`
}

// DerivationAdmissionRequest declares the complete AND-parent set for a new derived claim.
type DerivationAdmissionRequest struct {
	ParentNodeIDs []string `json:"parent_node_ids"`
	Method        string   `json:"method"`
	Producer      string   `json:"producer"`
	TraceRef      string   `json:"trace_ref"`
}

// RecordPendingProposalDispositionRequest records one terminal non-canonical operator decision.
type RecordPendingProposalDispositionRequest struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	Outcome              string `json:"outcome"`
	DecisionBy           string `json:"decision_by"`
	DecisionReason       string `json:"decision_reason"`
}

// SubmitCanonicalContradictionProposalRequest proposes one symmetric relation
// between two already-admitted canonical nodes.
type SubmitCanonicalContradictionProposalRequest struct {
	RequestID          string `json:"request_id"`
	NodeAID            string `json:"node_a_id"`
	NodeBID            string `json:"node_b_id"`
	Rationale          string `json:"rationale"`
	ProducerName       string `json:"producer_name"`
	ProducerVersion    string `json:"producer_version"`
	ProducerSessionRef string `json:"producer_session_ref,omitempty"`
}

// AdmitPendingCanonicalContradictionRequest records explicit human approval.
type AdmitPendingCanonicalContradictionRequest struct {
	ProposalID     string `json:"canonical_contradiction_proposal_id"`
	DecisionBy     string `json:"decision_by"`
	DecisionReason string `json:"decision_reason"`
}

// RecordPendingCanonicalContradictionDispositionRequest records a non-admitted terminal decision.
type RecordPendingCanonicalContradictionDispositionRequest struct {
	ProposalID     string `json:"canonical_contradiction_proposal_id"`
	Outcome        string `json:"outcome"`
	DecisionBy     string `json:"decision_by"`
	DecisionReason string `json:"decision_reason"`
}

// AdmitPendingSupersessionRequest records explicit human approval for one fresh
// source-backed replacement and its complete reviewed target set.
type AdmitPendingSupersessionRequest struct {
	ProposalOccurrenceID string                                     `json:"proposal_occurrence_id"`
	DecisionBy           string                                     `json:"decision_by"`
	DecisionReason       string                                     `json:"decision_reason"`
	Basis                evidenceingestion.SupersessionLineageBasis `json:"basis"`
	TargetNodeIDs        []string                                   `json:"target_node_ids"`
	ExpectedRevision     int64                                      `json:"expected_revision"`
	ExpectedHeadEventID  string                                     `json:"expected_head_event_id,omitempty"`
}

// SubmitManualEvidenceResponse is the minimal pending proposal response.
type SubmitManualEvidenceResponse struct {
	SourceSnapshotID     string `json:"source_snapshot_id"`
	ExtractionAttemptID  string `json:"extraction_attempt_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	ProposalFingerprint  string `json:"proposal_fingerprint"`
	Status               string `json:"status"`
	Replayed             bool   `json:"replayed"`
}

// SubmitExtractorOutputResponse is the minimal pending proposal response for existing source views.
type SubmitExtractorOutputResponse struct {
	SourceSnapshotID     string `json:"source_snapshot_id"`
	ExtractionViewID     string `json:"extraction_view_id"`
	ExtractionAttemptID  string `json:"extraction_attempt_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	ProposalFingerprint  string `json:"proposal_fingerprint"`
	ProposalCount        int    `json:"proposal_count"`
	Status               string `json:"status"`
	Replayed             bool   `json:"replayed"`
}

// SubmitTextSourceResponse returns the grounded source authority for LLM extraction.
type SubmitTextSourceResponse struct {
	SourceSnapshotID    string                        `json:"source_snapshot_id"`
	ExtractionViewID    string                        `json:"extraction_view_id"`
	SourceSystem        string                        `json:"source_system"`
	RawContentHash      string                        `json:"raw_content_hash"`
	RenderedContentHash string                        `json:"rendered_content_hash"`
	SpanCatalogVersion  string                        `json:"span_catalog_version"`
	Spans               []evidenceingestion.SpanEntry `json:"spans"`
	Replayed            bool                          `json:"replayed"`
}

// SubmitExternalSourceResponse returns immutable external source authority without proposals.
type SubmitExternalSourceResponse struct {
	SourceSnapshotID      string                        `json:"source_snapshot_id"`
	ExtractionViewID      string                        `json:"extraction_view_id"`
	AuthoritySourceSystem string                        `json:"authority_source_system"`
	SourceID              string                        `json:"source_id"`
	SourceSystem          string                        `json:"source_system"`
	SourceNamespace       string                        `json:"source_namespace"`
	ObjectType            string                        `json:"object_type"`
	ObjectID              string                        `json:"object_id"`
	Revision              string                        `json:"revision"`
	Coverage              string                        `json:"coverage"`
	ObservedAt            time.Time                     `json:"observed_at"`
	ReceivedAt            time.Time                     `json:"received_at"`
	RawContentHash        string                        `json:"raw_content_hash"`
	RenderedContentHash   string                        `json:"rendered_content_hash"`
	SpanCatalogVersion    string                        `json:"span_catalog_version"`
	Spans                 []evidenceingestion.SpanEntry `json:"spans"`
	Replayed              bool                          `json:"replayed"`
}

// GetExtractorInputResponse is the grounded, read-only source view package.
type GetExtractorInputResponse = evidenceingestion.ExtractorInput

// GetRepositoryExtractorInputResponse is the immutable multi-file code input.
type GetRepositoryExtractorInputResponse = evidenceingestion.RepositoryExtractorInput

// InspectGoplsWorkspaceResponse is grounded discovery data without proposal persistence.
type InspectGoplsWorkspaceResponse = evidenceingestion.GoplsWorkspaceInventory

// InspectGitRepositoryChangeResponse is a read-only scheduling hint without source persistence.
type InspectGitRepositoryChangeResponse = evidenceingestion.GitRepositoryChangeInspection

// ObserveGitRepositoryChangeResponse reports one persisted, coalesced Git observation decision.
type ObserveGitRepositoryChangeResponse = evidenceingestion.GitRepositoryChangeObservationResult

// ScheduleGitRepositoryExtractionWorkResponse reports an idempotent pending-work decision.
type ScheduleGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkScheduleResult

// ClaimGitRepositoryExtractionWorkResponse reports an atomic claim or persisted no-work decision.
type ClaimGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkClaimResult

// RenewGitRepositoryExtractionWorkLeaseResponse reports one durable lease extension.
type RenewGitRepositoryExtractionWorkLeaseResponse = evidenceingestion.RepositoryExtractionWorkLeaseRenewalResult

// ExecuteClaimedGitRepositoryExtractionWorkResponse reports one deterministic successful execution.
type ExecuteClaimedGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkExecutionResult

// RunGitRepositoryExtractionWorkerTickResponse reports one durable claim decision and optional execution.
type RunGitRepositoryExtractionWorkerTickResponse = evidenceingestion.RepositoryExtractionWorkerTickResult

// ListExpiredGitRepositoryExtractionWorkClaimsResponse reports bounded recovery candidates.
type ListExpiredGitRepositoryExtractionWorkClaimsResponse = []evidenceingestion.RepositoryExtractionWorkExpiredClaim

// ListExpiredGitRepositoryExtractionWorkExecutionsResponse reports bounded exact execution-owner candidates.
type ListExpiredGitRepositoryExtractionWorkExecutionsResponse = []evidenceingestion.RepositoryExtractionWorkExpiredExecution

// FinishGitRepositoryExtractionWorkResponse reports one terminal work transition.
type FinishGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkFinishResult

// RecoverExpiredGitRepositoryExtractionWorkResponse reports one durable recovery transition.
type RecoverExpiredGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkRecoveryResult

// RepairExpiredGitRepositoryExtractionWorkExecutionResponse reports one durable execution repair transition.
type RepairExpiredGitRepositoryExtractionWorkExecutionResponse = evidenceingestion.RepositoryExtractionWorkExecutionRepairResult

// RetryFailedGitRepositoryExtractionWorkResponse reports one durable retry transition.
type RetryFailedGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkRetryResult

// ClassifyFailedGitRepositoryExtractionWorkResponse reports one immutable failure policy decision.
type ClassifyFailedGitRepositoryExtractionWorkResponse = evidenceingestion.RepositoryExtractionWorkFailurePolicyResult

// ListDueGitRepositoryExtractionWorkRetryDecisionsResponse reports bounded exact consumption candidates.
type ListDueGitRepositoryExtractionWorkRetryDecisionsResponse = []evidenceingestion.RepositoryExtractionWorkDueRetryDecision

// ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse reports one policy-authorized retry transition.
type ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse = evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult

// RunDueGitRepositoryExtractionWorkRetryControllerTickResponse reports one durable bounded controller batch.
type RunDueGitRepositoryExtractionWorkRetryControllerTickResponse = evidenceingestion.RepositoryExtractionWorkRetryControllerTickResult

// RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse reports one durable bounded maintenance batch.
type RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse = evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickResult

// CaptureGitRepositorySnapshotResponse reports the persisted repository authority summary.
type CaptureGitRepositorySnapshotResponse struct {
	RepositorySnapshotID       string `json:"repository_snapshot_id"`
	RepoID                     string `json:"repo_id"`
	CommitSHA                  string `json:"commit_sha"`
	ManifestHash               string `json:"manifest_hash"`
	ManifestEntryCount         int    `json:"manifest_entry_count"`
	RevisionVerificationMethod string `json:"revision_verification_method"`
	ManifestContract           string `json:"manifest_contract"`
	FileSelectionContract      string `json:"file_selection_contract"`
	SelectedFileCount          int    `json:"selected_file_count"`
	Replayed                   bool   `json:"replayed"`
}

// RunLocalOllamaExtractorResponse is the minimal proposal result from local model extraction.
type RunLocalOllamaExtractorResponse struct {
	SourceSnapshotID     string `json:"source_snapshot_id"`
	ExtractionViewID     string `json:"extraction_view_id"`
	ExtractionRunID      string `json:"extraction_run_id"`
	ExtractionAttemptID  string `json:"extraction_attempt_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	ProposalFingerprint  string `json:"proposal_fingerprint"`
	Status               string `json:"status"`
	Replayed             bool   `json:"replayed"`
}

// RunGoParserExtractorResponse is the minimal proposal result from Go source extraction.
type RunGoParserExtractorResponse = RunLocalOllamaExtractorResponse

// RunGoplsExtractorResponse is the minimal proposal result from real gopls extraction.
type RunGoplsExtractorResponse = RunLocalOllamaExtractorResponse

// AdmitPendingProposalResponse records one canonical admission result.
type AdmitPendingProposalResponse struct {
	ProposalOccurrenceID string   `json:"proposal_occurrence_id"`
	AdmissionDecisionID  string   `json:"admission_decision_id"`
	AdmissionOutcome     string   `json:"admission_outcome"`
	CanonicalRef         string   `json:"canonical_ref"`
	RawEvidenceNodeIDs   []string `json:"raw_evidence_node_ids"`
	CanonicalEdgeIDs     []string `json:"canonical_edge_ids"`
	DerivationID         string   `json:"derivation_id,omitempty"`
	ParentNodeIDs        []string `json:"parent_node_ids,omitempty"`
	Replayed             bool     `json:"replayed"`
}

// RecordPendingProposalDispositionResponse records one rejected or audit-only result.
type RecordPendingProposalDispositionResponse struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	AdmissionDecisionID  string `json:"admission_decision_id"`
	AdmissionOutcome     string `json:"admission_outcome"`
	DecisionBy           string `json:"decision_by"`
	DecisionReason       string `json:"decision_reason"`
	Replayed             bool   `json:"replayed"`
}

// SubmitCanonicalContradictionProposalResponse reports durable proposal identity and state.
type SubmitCanonicalContradictionProposalResponse struct {
	CanonicalContradictionProposalID string `json:"canonical_contradiction_proposal_id"`
	NodeAID                          string `json:"node_a_id"`
	NodeBID                          string `json:"node_b_id"`
	Relation                         string `json:"relation"`
	Rationale                        string `json:"rationale"`
	ProducerName                     string `json:"producer_name"`
	ProducerVersion                  string `json:"producer_version"`
	ProducerSessionRef               string `json:"producer_session_ref,omitempty"`
	AdmissionOutcome                 string `json:"admission_outcome"`
	CanonicalEdgeID                  string `json:"canonical_edge_id,omitempty"`
	Replayed                         bool   `json:"replayed"`
}

// CanonicalContradictionDecisionResponse reports one immutable review result.
type CanonicalContradictionDecisionResponse struct {
	CanonicalContradictionProposalID string `json:"canonical_contradiction_proposal_id"`
	AdmissionDecisionID              string `json:"admission_decision_id"`
	AdmissionOutcome                 string `json:"admission_outcome"`
	CanonicalEdgeID                  string `json:"canonical_edge_id,omitempty"`
	DecisionBy                       string `json:"decision_by"`
	DecisionReason                   string `json:"decision_reason"`
	Replayed                         bool   `json:"replayed"`
}

// AdmitPendingSupersessionResponse reports the admitted canonical mutation,
// governed lineage event, compare-and-swap revision, and immutable review audit.
type AdmitPendingSupersessionResponse struct {
	ProposalOccurrenceID      string   `json:"proposal_occurrence_id"`
	AdmissionDecisionID       string   `json:"admission_decision_id"`
	AdmissionOutcome          string   `json:"admission_outcome"`
	CanonicalRef              string   `json:"canonical_ref"`
	RawEvidenceNodeIDs        []string `json:"raw_evidence_node_ids"`
	CanonicalEdgeIDs          []string `json:"canonical_edge_ids"`
	DerivationID              string   `json:"derivation_id,omitempty"`
	ParentNodeIDs             []string `json:"parent_node_ids,omitempty"`
	LineageKey                string   `json:"lineage_key"`
	TargetNodeIDs             []string `json:"target_node_ids"`
	BootstrappedTargetNodeIDs []string `json:"bootstrapped_target_node_ids"`
	SupersedesEdgeIDs         []string `json:"supersedes_edge_ids"`
	AdmissionEventID          string   `json:"admission_event_id"`
	EventRevision             int64    `json:"event_revision"`
	PreviousHeadEventID       string   `json:"previous_head_event_id,omitempty"`
	DecisionBy                string   `json:"decision_by"`
	DecisionReason            string   `json:"decision_reason"`
	Replayed                  bool     `json:"replayed"`
}

// ToolError is the stable MCP-shaped error returned by the adapter.
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

type ingestionCore interface {
	AdmitPendingProposal(ctx context.Context, input evidenceingestion.AdmissionInput) (evidenceingestion.AdmissionResult, error)
	RecordPendingProposalDisposition(ctx context.Context, input evidenceingestion.ProposalDispositionInput) (evidenceingestion.ProposalDispositionResult, error)
	SubmitCanonicalContradictionProposal(ctx context.Context, input evidenceingestion.CanonicalContradictionProposalInput) (evidenceingestion.CanonicalContradictionProposalResult, error)
	AdmitPendingCanonicalContradiction(ctx context.Context, input evidenceingestion.CanonicalContradictionAdmissionInput) (evidenceingestion.CanonicalContradictionDecisionResult, error)
	RecordPendingCanonicalContradictionDisposition(ctx context.Context, input evidenceingestion.CanonicalContradictionDispositionInput) (evidenceingestion.CanonicalContradictionDecisionResult, error)
	AdmitPendingSupersession(ctx context.Context, input evidenceingestion.SupersessionAdmissionInput) (evidenceingestion.SupersessionAdmissionResult, error)
	ActivateRepositorySourceGeneration(ctx context.Context, input evidenceingestion.RepositorySourceGenerationActivationInput) (evidenceingestion.RepositorySourceGenerationActivationResult, error)
	ListRepositorySourceGenerations(ctx context.Context, input evidenceingestion.RepositorySourceGenerationListInput) ([]evidenceingestion.RepositorySourceGenerationStatus, error)
	BuildExtractorInput(ctx context.Context, extractionViewID string) (evidenceingestion.ExtractorInput, error)
	BuildRepositoryExtractorInput(ctx context.Context, repositorySnapshotID string) (evidenceingestion.RepositoryExtractorInput, error)
	CaptureGitRepositorySnapshot(ctx context.Context, config evidenceingestion.GitRepositorySnapshotConfig) (evidenceingestion.RepositorySnapshotCaptureResult, error)
	InspectGitRepositoryChange(ctx context.Context, config evidenceingestion.GitRepositoryChangeConfig) (evidenceingestion.GitRepositoryChangeInspection, error)
	ObserveGitRepositoryChange(ctx context.Context, config evidenceingestion.GitRepositoryChangeObservationConfig) (evidenceingestion.GitRepositoryChangeObservationResult, error)
	ScheduleRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkScheduleInput) (evidenceingestion.RepositoryExtractionWorkScheduleResult, error)
	ClaimRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkClaimInput) (evidenceingestion.RepositoryExtractionWorkClaimResult, error)
	RenewRepositoryExtractionWorkLease(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkLeaseRenewalInput) (evidenceingestion.RepositoryExtractionWorkLeaseRenewalResult, error)
	ExecuteClaimedRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExecutionInput) (evidenceingestion.RepositoryExtractionWorkExecutionResult, error)
	RunRepositoryExtractionWorkerTick(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkerTickInput) (evidenceingestion.RepositoryExtractionWorkerTickResult, error)
	ListExpiredRepositoryExtractionWorkClaims(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredClaimListInput) ([]evidenceingestion.RepositoryExtractionWorkExpiredClaim, error)
	ListExpiredRepositoryExtractionWorkExecutions(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredExecutionListInput) ([]evidenceingestion.RepositoryExtractionWorkExpiredExecution, error)
	FinishRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkFinishInput) (evidenceingestion.RepositoryExtractionWorkFinishResult, error)
	RecoverExpiredRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRecoveryInput) (evidenceingestion.RepositoryExtractionWorkRecoveryResult, error)
	RepairExpiredRepositoryExtractionWorkExecution(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExecutionRepairInput) (evidenceingestion.RepositoryExtractionWorkExecutionRepairResult, error)
	RetryFailedRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRetryInput) (evidenceingestion.RepositoryExtractionWorkRetryResult, error)
	ClassifyFailedRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkFailurePolicyInput) (evidenceingestion.RepositoryExtractionWorkFailurePolicyResult, error)
	ListDueRepositoryExtractionWorkRetryDecisions(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkDueRetryDecisionListInput) ([]evidenceingestion.RepositoryExtractionWorkDueRetryDecision, error)
	ConsumeDueRepositoryExtractionWorkRetryDecision(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionInput) (evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult, error)
	RunRepositoryExtractionWorkRetryControllerTick(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRetryControllerTickInput) (evidenceingestion.RepositoryExtractionWorkRetryControllerTickResult, error)
	RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput) (evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickResult, error)
	CreateRepositoryExtractionRun(ctx context.Context, request evidenceingestion.RepositoryExtractionRunRequest) (evidenceingestion.RepositoryExtractionRunResult, error)
	CaptureManualSource(ctx context.Context, input evidenceingestion.ManualTextInput) (evidenceingestion.SourceIntakeResult, error)
	CaptureExternalSource(ctx context.Context, input evidenceingestion.ExternalSourceEnvelopeV1) (evidenceingestion.ExternalSourceIntakeResult, error)
	IngestManualText(ctx context.Context, input evidenceingestion.ManualTextInput, fixture evidenceingestion.FrozenExtractorOutput) (evidenceingestion.IngestResult, error)
	RunTrustedExtractor(ctx context.Context, request evidenceingestion.TrustedExtractorRequest, runner evidenceingestion.ExtractorRunner) (evidenceingestion.IngestResult, error)
	RunRepositoryGoParserExtractor(ctx context.Context, request evidenceingestion.RepositoryGoParserRequest) (evidenceingestion.RepositoryIngestResult, error)
	RunRepositoryGoplsExtractor(ctx context.Context, request evidenceingestion.RepositoryGoplsRequest) (evidenceingestion.RepositoryIngestResult, error)
	SubmitExtractorOutput(ctx context.Context, input evidenceingestion.ExtractorOutputInput) (evidenceingestion.IngestResult, error)
	TraceProposalProvenance(ctx context.Context, occurrenceID string) (evidenceingestion.ProposalQueryResult, error)
}

type goplsWorkspaceInventoryCollector func(context.Context, evidenceingestion.GoplsWorkspaceInventoryConfig) (evidenceingestion.GoplsWorkspaceInventory, error)

// Server exposes the internal ingestion capability surface.
type Server struct {
	core                           ingestionCore
	collectGoplsWorkspaceInventory goplsWorkspaceInventoryCollector
}

// NewServer constructs the internal ingest server over PostgreSQL.
func NewServer(pool *pgxpool.Pool) (*Server, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return newServer(postgresCore{pool: pool}), nil
}

func newServer(core ingestionCore) *Server {
	return &Server{
		core:                           core,
		collectGoplsWorkspaceInventory: evidenceingestion.CollectGoplsWorkspaceInventory,
	}
}

// Tools returns ingestion and grounded discovery tools, but no external evidence query tools.
func (s *Server) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolSubmitManualEvidence,
			Description: "Submit bounded manual text evidence through the Slice 1 ingestion core.",
			Write:       true,
		},
		{
			Name:        ToolSubmitTextSource,
			Description: "Submit raw UTF-8 natural-language or code-file source authority without creating proposals.",
			Write:       true,
		},
		{
			Name:        ToolSubmitExternalSource,
			Description: "Submit one exact connector-observed external source envelope without creating proposals or canonical evidence.",
			Write:       true,
		},
		{
			Name:        ToolSubmitExtractorOutput,
			Description: "Submit identified external-agent or local-extractor proposal output for an existing grounded source view, with an optional non-secret producer session reference for debugging.",
			Write:       true,
		},
		{
			Name:        ToolGetExtractorInput,
			Description: "Load grounded source bytes and deterministic spans for an external agent or local extractor.",
			Write:       false,
		},
		{
			Name:        ToolInspectGoplsWorkspace,
			Description: "Inspect gopls package metadata and controller-hashed Go files without creating source or proposal records.",
			Write:       false,
		},
		{
			Name:        ToolInspectGitRepositoryChange,
			Description: "Inspect Git HEAD and a bounded dirty-worktree fingerprint as a read-only scheduling hint.",
			Write:       false,
		},
		{
			Name:        ToolObserveGitRepositoryChange,
			Description: "Inspect and persist one idempotent Git change observation with bounded stability-window coalescing.",
			Write:       true,
		},
		{
			Name:        ToolScheduleGitRepositoryExtractionWork,
			Description: "Schedule one latest stable clean Git observation as pending work for an allowlisted repository extractor stream.",
			Write:       true,
		},
		{
			Name:        ToolClaimGitRepositoryExtractionWork,
			Description: "Atomically claim pending repository extraction work without running the extractor.",
			Write:       true,
		},
		{
			Name:        ToolRenewGitRepositoryExtractionWorkLease,
			Description: "Extend one exact running repository extraction claim before its current lease expires.",
			Write:       true,
		},
		{
			Name:        ToolExecuteClaimedGitRepositoryExtractionWork,
			Description: "Capture, deterministically extract with inherited lease heartbeat, and finish success for one exact running repository extraction claim.",
			Write:       true,
		},
		{
			Name:        ToolRunGitRepositoryExtractionWorkerTick,
			Description: "Claim and, when available, execute at most one item from one repository extraction stream.",
			Write:       true,
		},
		{
			Name:        ToolListExpiredGitRepositoryExtractionWorkClaims,
			Description: "List bounded still-running repository extraction claims whose current leases have elapsed, without deciding abandonment or recovering work.",
			Write:       false,
		},
		{
			Name:        ToolListExpiredGitRepositoryExtractionWorkExecutions,
			Description: "List bounded execution-owned running claims whose current leases have elapsed, without deciding abandonment or repairing work.",
			Write:       false,
		},
		{
			Name:        ToolFinishGitRepositoryExtractionWork,
			Description: "Record a generation-bound success or bounded failure for claimed repository extraction work.",
			Write:       true,
		},
		{
			Name:        ToolRecoverExpiredGitRepositoryExtractionWork,
			Description: "Release one exact expired repository extraction claim by requeueing it or superseding it behind newer pending work.",
			Write:       true,
		},
		{
			Name:        ToolRepairExpiredGitRepositoryExtractionWorkExecution,
			Description: "Repair one exact execution-owned claim after its lease expires, preserving the execution and claim audit.",
			Write:       true,
		},
		{
			Name:        ToolRetryFailedGitRepositoryExtractionWork,
			Description: "Release one exact failed repository extraction claim by requeueing it or superseding it behind newer pending work.",
			Write:       true,
		},
		{
			Name:        ToolCaptureGitRepositorySnapshot,
			Description: "Persist one Git-verified repository snapshot and its tracked regular Go file snapshots.",
			Write:       true,
		},
		{
			Name:        ToolGetRepositoryExtractorInput,
			Description: "Load exact persisted Go file bytes for one immutable repository snapshot.",
			Write:       false,
		},
		{Name: ToolCreateRepositoryExtractionRun, Description: "Create an extraction run bound to one immutable repository snapshot without starting execution.", Write: true},
		{
			Name:        ToolRunRepositoryGoParserExtractor,
			Description: "Run deterministic declaration extraction over all persisted Go files in one repository snapshot.",
			Write:       true,
		},
		{
			Name:        ToolRunRepositoryGoplsExtractor,
			Description: "Run one gopls session over Git-verified persisted Go files, persist parser-grounded selected-snapshot facts and relations, and retain reference, incoming-call, locality, and unselected-result count diagnostics without persisting unverified URIs or endpoints.",
			Write:       true,
		},
		{
			Name:        ToolListRepositorySourceGenerations,
			Description: "List recent immutable generations and current-head state for one exact repository/extractor stream.",
			Write:       false,
		},
		{
			Name:        ToolActivateRepositorySourceGeneration,
			Description: "Atomically activate one exact repository source generation and reconcile it against the current stream head.",
			Write:       true,
		},
		{
			Name:        ToolRunLocalOllamaExtractor,
			Description: "Invoke local Ollama against one grounded source view and persist materialized proposals.",
			Write:       true,
		},
		{
			Name:        ToolRunGoParserExtractor,
			Description: "Extract Go declarations from one grounded source view and persist materialized proposals.",
			Write:       true,
		},
		{
			Name:        ToolRunGoplsExtractor,
			Description: "Extract gopls-confirmed Go declarations from one grounded source view and persist materialized proposals.",
			Write:       true,
		},
		{
			Name:        ToolAdmitPendingProposal,
			Description: "Call only after the cooperating agent has displayed the proposal sentence, exact source quotes, source title/location, coverage/limitations, revision, and any comparable version difference and received explicit human approval. This call immediately and terminally admits one pending proposal as source-backed evidence or an explicitly parented derived claim. AHE records the decision but does not prove the review occurred.",
			Write:       true,
		},
		{
			Name:        ToolRecordPendingProposalDisposition,
			Description: "Call only after an explicit human rejection or audit-only decision. This call immediately and terminally marks one pending proposal without creating canonical evidence; it cannot later be admitted. AHE records the decision but does not prove the review occurred.",
			Write:       true,
		},
		{
			Name:        ToolSubmitCanonicalContradictionProposal,
			Description: "Create or exactly replay the single governed contradiction proposal for one unordered node pair of existing admitted canonical nodes. The pair is single-use across pending, admitted, rejected, and audit-only outcomes; changed metadata conflicts. This call does not create a canonical edge, and AHE does not infer or validate the semantic conflict.",
			Write:       true,
		},
		{
			Name:        ToolAdmitPendingCanonicalContradiction,
			Description: "Call only after the cooperating agent has displayed both grounded canonical nodes, their source context, and the rationale and received explicit human approval. This call immediately and terminally admits the pending proposal and creates one symmetric canonical contradicts edge. AHE records the decision but does not prove the review occurred.",
			Write:       true,
		},
		{
			Name:        ToolRecordPendingCanonicalContradictionDisposition,
			Description: "Call only after an explicit human rejected or audit-only decision. This call immediately and terminally disposes the pending contradiction proposal without creating an edge; the unordered node pair remains single-use in v1. AHE records the decision but does not prove the review occurred.",
			Write:       true,
		},
		{
			Name:        ToolAdmitPendingSupersession,
			Description: "Call only after the cooperating agent has displayed the fresh pending proposal sentence, exact source quotes, source title and location, provider revision, the complete set of older canonical claims it replaces, version differences, coverage/limitations, the reviewed six-field source-object/slot basis, and the current supersession head coordinate (revision and event ID), then received explicit human approval. This call immediately and terminally admits the fresh source-backed claim and exact new-to-old supersedes edge set. AHE checks the first four basis fields against grounded source identity; slot_kind and slot_id are human-reviewed stable semantic-slot declarations. AHE derives lineage, event identity, and the next revision, and records but does not prove the human review. Use record_pending_proposal_disposition for rejected or audit-only outcomes.",
			Write:       true,
		},
		{
			Name:        ToolClassifyFailedGitRepositoryExtractionWork,
			Description: "Record deterministic retry-policy-v1 classification and optional backoff for one exact terminal failed claim without requeueing it.",
			Write:       true,
		},
		{
			Name:        ToolListDueGitRepositoryExtractionWorkRetryDecisions,
			Description: "List bounded unconsumed retry-policy decisions whose retry deadlines have elapsed, without consuming decisions or changing work.",
			Write:       false,
		},
		{
			Name:        ToolConsumeDueGitRepositoryExtractionWorkRetryDecision,
			Description: "Consume one exact due retry-policy decision by requeueing its failed work or superseding it behind newer pending work.",
			Write:       true,
		},
		{
			Name:        ToolRunDueGitRepositoryExtractionWorkRetryControllerTick,
			Description: "Atomically consume one durable bounded cross-stream batch of due retry-policy decisions without polling or executing extractors.",
			Write:       true,
		},
		{
			Name:        ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick,
			Description: "Atomically recover or repair one durable bounded cross-stream batch of elapsed-lease work without polling or inferring worker liveness.",
			Write:       true,
		},
	}
}

// CallTool decodes one MCP-shaped JSON tool request and encodes its response.
func (s *Server) CallTool(ctx context.Context, name string, payload []byte) ([]byte, error) {
	switch name {
	case ToolSubmitManualEvidence:
		var req SubmitManualEvidenceRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.SubmitManualEvidence(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolSubmitTextSource:
		var req SubmitTextSourceRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.SubmitTextSource(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolSubmitExternalSource:
		var req SubmitExternalSourceRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.SubmitExternalSource(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolSubmitExtractorOutput:
		var req SubmitExtractorOutputRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.SubmitExtractorOutput(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetExtractorInput:
		var req GetExtractorInputRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetExtractorInput(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolInspectGoplsWorkspace:
		var req InspectGoplsWorkspaceRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.InspectGoplsWorkspace(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolInspectGitRepositoryChange:
		var req InspectGitRepositoryChangeRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.InspectGitRepositoryChange(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolObserveGitRepositoryChange:
		var req ObserveGitRepositoryChangeRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ObserveGitRepositoryChange(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolScheduleGitRepositoryExtractionWork:
		var req ScheduleGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ScheduleGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolClaimGitRepositoryExtractionWork:
		var req ClaimGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ClaimGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRenewGitRepositoryExtractionWorkLease:
		var req RenewGitRepositoryExtractionWorkLeaseRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RenewGitRepositoryExtractionWorkLease(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolExecuteClaimedGitRepositoryExtractionWork:
		var req ExecuteClaimedGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ExecuteClaimedGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunGitRepositoryExtractionWorkerTick:
		var req RunGitRepositoryExtractionWorkerTickRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunGitRepositoryExtractionWorkerTick(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolListExpiredGitRepositoryExtractionWorkClaims:
		var req ListExpiredGitRepositoryExtractionWorkClaimsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ListExpiredGitRepositoryExtractionWorkClaims(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolListExpiredGitRepositoryExtractionWorkExecutions:
		var req ListExpiredGitRepositoryExtractionWorkExecutionsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ListExpiredGitRepositoryExtractionWorkExecutions(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolFinishGitRepositoryExtractionWork:
		var req FinishGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.FinishGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRecoverExpiredGitRepositoryExtractionWork:
		var req RecoverExpiredGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RecoverExpiredGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRepairExpiredGitRepositoryExtractionWorkExecution:
		var req RepairExpiredGitRepositoryExtractionWorkExecutionRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RepairExpiredGitRepositoryExtractionWorkExecution(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRetryFailedGitRepositoryExtractionWork:
		var req RetryFailedGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RetryFailedGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolClassifyFailedGitRepositoryExtractionWork:
		var req ClassifyFailedGitRepositoryExtractionWorkRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ClassifyFailedGitRepositoryExtractionWork(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolListDueGitRepositoryExtractionWorkRetryDecisions:
		var req ListDueGitRepositoryExtractionWorkRetryDecisionsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ListDueGitRepositoryExtractionWorkRetryDecisions(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolConsumeDueGitRepositoryExtractionWorkRetryDecision:
		var req ConsumeDueGitRepositoryExtractionWorkRetryDecisionRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ConsumeDueGitRepositoryExtractionWorkRetryDecision(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunDueGitRepositoryExtractionWorkRetryControllerTick:
		var req RunDueGitRepositoryExtractionWorkRetryControllerTickRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunDueGitRepositoryExtractionWorkRetryControllerTick(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick:
		var req RunExpiredGitRepositoryExtractionWorkMaintenanceTickRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunExpiredGitRepositoryExtractionWorkMaintenanceTick(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolCaptureGitRepositorySnapshot:
		var req CaptureGitRepositorySnapshotRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.CaptureGitRepositorySnapshot(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolGetRepositoryExtractorInput:
		var req GetRepositoryExtractorInputRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.GetRepositoryExtractorInput(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolCreateRepositoryExtractionRun:
		var req CreateRepositoryExtractionRunRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.CreateRepositoryExtractionRun(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	case ToolRunRepositoryGoParserExtractor:
		var req RunRepositoryGoParserExtractorRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunRepositoryGoParserExtractor(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunRepositoryGoplsExtractor:
		var req RunRepositoryGoplsExtractorRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunRepositoryGoplsExtractor(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolListRepositorySourceGenerations:
		var req ListRepositorySourceGenerationsRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ListRepositorySourceGenerations(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolActivateRepositorySourceGeneration:
		var req ActivateRepositorySourceGenerationRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.ActivateRepositorySourceGeneration(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunLocalOllamaExtractor:
		var req RunLocalOllamaExtractorRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunLocalOllamaExtractor(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunGoParserExtractor:
		var req RunGoParserExtractorRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunGoParserExtractor(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRunGoplsExtractor:
		var req RunGoplsExtractorRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RunGoplsExtractor(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolAdmitPendingProposal:
		var req AdmitPendingProposalRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.AdmitPendingProposal(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolRecordPendingProposalDisposition:
		var req RecordPendingProposalDispositionRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RecordPendingProposalDisposition(ctx, req)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(resp)
		if err != nil {
			return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
		}
		return data, nil
	case ToolSubmitCanonicalContradictionProposal:
		var req SubmitCanonicalContradictionProposalRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.SubmitCanonicalContradictionProposal(ctx, req)
		if err != nil {
			return nil, err
		}
		return marshalToolResponse(resp)
	case ToolAdmitPendingCanonicalContradiction:
		var req AdmitPendingCanonicalContradictionRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.AdmitPendingCanonicalContradiction(ctx, req)
		if err != nil {
			return nil, err
		}
		return marshalToolResponse(resp)
	case ToolRecordPendingCanonicalContradictionDisposition:
		var req RecordPendingCanonicalContradictionDispositionRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.RecordPendingCanonicalContradictionDisposition(ctx, req)
		if err != nil {
			return nil, err
		}
		return marshalToolResponse(resp)
	case ToolAdmitPendingSupersession:
		var req AdmitPendingSupersessionRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, &ToolError{Code: toolErrorInvalidRequest, Message: err.Error(), cause: err}
		}
		resp, err := s.AdmitPendingSupersession(ctx, req)
		if err != nil {
			return nil, err
		}
		return marshalToolResponse(resp)
	default:
		return nil, &ToolError{Code: toolErrorUnknownTool, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

// InspectGoplsWorkspace returns package and file discovery without mutating ingestion state.
func (s *Server) InspectGoplsWorkspace(ctx context.Context, req InspectGoplsWorkspaceRequest) (InspectGoplsWorkspaceResponse, error) {
	result, err := s.collectGoplsWorkspaceInventory(ctx, evidenceingestion.GoplsWorkspaceInventoryConfig{
		WorkspaceRoot: req.WorkspaceRoot,
		RepoID:        req.RepoID,
		CommitSHA:     req.CommitSHA,
	})
	if err != nil {
		return InspectGoplsWorkspaceResponse{}, mapToolError(err)
	}
	return result, nil
}

// InspectGitRepositoryChange returns a bounded scheduling token without mutating ingestion state.
func (s *Server) InspectGitRepositoryChange(ctx context.Context, req InspectGitRepositoryChangeRequest) (InspectGitRepositoryChangeResponse, error) {
	result, err := s.core.InspectGitRepositoryChange(ctx, evidenceingestion.GitRepositoryChangeConfig{
		WorkspaceRoot: req.WorkspaceRoot,
		RepoID:        req.RepoID,
	})
	if err != nil {
		return InspectGitRepositoryChangeResponse{}, mapToolError(err)
	}
	return result, nil
}

// ObserveGitRepositoryChange persists one trusted Git observation without starting extraction work.
func (s *Server) ObserveGitRepositoryChange(ctx context.Context, req ObserveGitRepositoryChangeRequest) (ObserveGitRepositoryChangeResponse, error) {
	maxWindow := evidenceingestion.GitRepositoryChangeMaxStabilityWindow.Milliseconds()
	if req.StabilityWindowMilliseconds < 1 || req.StabilityWindowMilliseconds > maxWindow {
		return ObserveGitRepositoryChangeResponse{}, mapToolError(&evidenceingestion.DomainError{
			Kind:    evidenceingestion.ErrorInvalidInput,
			Message: fmt.Sprintf("Git change stability window must be between 1 and %d milliseconds", maxWindow),
		})
	}
	result, err := s.core.ObserveGitRepositoryChange(ctx, evidenceingestion.GitRepositoryChangeObservationConfig{
		WorkspaceRoot:   req.WorkspaceRoot,
		RepoID:          req.RepoID,
		RequestID:       req.RequestID,
		StabilityWindow: time.Duration(req.StabilityWindowMilliseconds) * time.Millisecond,
	})
	if err != nil {
		return ObserveGitRepositoryChangeResponse{}, mapToolError(err)
	}
	return result, nil
}

// ScheduleGitRepositoryExtractionWork persists one eligible pending-work decision without executing it.
func (s *Server) ScheduleGitRepositoryExtractionWork(ctx context.Context, req ScheduleGitRepositoryExtractionWorkRequest) (ScheduleGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.ScheduleRepositoryExtractionWork(ctx, req)
	if err != nil {
		return ScheduleGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// ClaimGitRepositoryExtractionWork atomically assigns pending work without executing it.
func (s *Server) ClaimGitRepositoryExtractionWork(ctx context.Context, req ClaimGitRepositoryExtractionWorkRequest) (ClaimGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.ClaimRepositoryExtractionWork(ctx, req)
	if err != nil {
		return ClaimGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// RenewGitRepositoryExtractionWorkLease extends one exact running claim without executing work.
func (s *Server) RenewGitRepositoryExtractionWorkLease(ctx context.Context, req RenewGitRepositoryExtractionWorkLeaseRequest) (RenewGitRepositoryExtractionWorkLeaseResponse, error) {
	result, err := s.core.RenewRepositoryExtractionWorkLease(ctx, req)
	if err != nil {
		return RenewGitRepositoryExtractionWorkLeaseResponse{}, mapToolError(err)
	}
	return result, nil
}

// ExecuteClaimedGitRepositoryExtractionWork runs the extractor selected by one exact claim.
func (s *Server) ExecuteClaimedGitRepositoryExtractionWork(ctx context.Context, req ExecuteClaimedGitRepositoryExtractionWorkRequest) (ExecuteClaimedGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.ExecuteClaimedRepositoryExtractionWork(ctx, req)
	if err != nil {
		return ExecuteClaimedGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// RunGitRepositoryExtractionWorkerTick performs one bounded claim-and-optional-execute step.
func (s *Server) RunGitRepositoryExtractionWorkerTick(ctx context.Context, req RunGitRepositoryExtractionWorkerTickRequest) (RunGitRepositoryExtractionWorkerTickResponse, error) {
	result, err := s.core.RunRepositoryExtractionWorkerTick(ctx, req)
	if err != nil {
		return RunGitRepositoryExtractionWorkerTickResponse{}, mapToolError(err)
	}
	return result, nil
}

// ListExpiredGitRepositoryExtractionWorkClaims returns elapsed-lease candidates without changing work state.
func (s *Server) ListExpiredGitRepositoryExtractionWorkClaims(ctx context.Context, req ListExpiredGitRepositoryExtractionWorkClaimsRequest) (ListExpiredGitRepositoryExtractionWorkClaimsResponse, error) {
	result, err := s.core.ListExpiredRepositoryExtractionWorkClaims(ctx, req)
	if err != nil {
		return nil, mapToolError(err)
	}
	return result, nil
}

// ListExpiredGitRepositoryExtractionWorkExecutions returns exact execution owners without changing work state.
func (s *Server) ListExpiredGitRepositoryExtractionWorkExecutions(ctx context.Context, req ListExpiredGitRepositoryExtractionWorkExecutionsRequest) (ListExpiredGitRepositoryExtractionWorkExecutionsResponse, error) {
	result, err := s.core.ListExpiredRepositoryExtractionWorkExecutions(ctx, req)
	if err != nil {
		return nil, mapToolError(err)
	}
	return result, nil
}

// FinishGitRepositoryExtractionWork records a terminal outcome without running extraction.
func (s *Server) FinishGitRepositoryExtractionWork(ctx context.Context, req FinishGitRepositoryExtractionWorkRequest) (FinishGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.FinishRepositoryExtractionWork(ctx, req)
	if err != nil {
		return FinishGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// RecoverExpiredGitRepositoryExtractionWork releases one exact claim without scanning the stream.
func (s *Server) RecoverExpiredGitRepositoryExtractionWork(ctx context.Context, req RecoverExpiredGitRepositoryExtractionWorkRequest) (RecoverExpiredGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.RecoverExpiredRepositoryExtractionWork(ctx, req)
	if err != nil {
		return RecoverExpiredGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// RepairExpiredGitRepositoryExtractionWorkExecution repairs one exact expired execution owner without scanning the stream.
func (s *Server) RepairExpiredGitRepositoryExtractionWorkExecution(ctx context.Context, req RepairExpiredGitRepositoryExtractionWorkExecutionRequest) (RepairExpiredGitRepositoryExtractionWorkExecutionResponse, error) {
	result, err := s.core.RepairExpiredRepositoryExtractionWorkExecution(ctx, req)
	if err != nil {
		return RepairExpiredGitRepositoryExtractionWorkExecutionResponse{}, mapToolError(err)
	}
	return result, nil
}

// RetryFailedGitRepositoryExtractionWork releases one exact failed claim without selecting retry policy.
func (s *Server) RetryFailedGitRepositoryExtractionWork(ctx context.Context, req RetryFailedGitRepositoryExtractionWorkRequest) (RetryFailedGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.RetryFailedRepositoryExtractionWork(ctx, req)
	if err != nil {
		return RetryFailedGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// ClassifyFailedGitRepositoryExtractionWork records policy without requeueing or executing work.
func (s *Server) ClassifyFailedGitRepositoryExtractionWork(ctx context.Context, req ClassifyFailedGitRepositoryExtractionWorkRequest) (ClassifyFailedGitRepositoryExtractionWorkResponse, error) {
	result, err := s.core.ClassifyFailedRepositoryExtractionWork(ctx, req)
	if err != nil {
		return ClassifyFailedGitRepositoryExtractionWorkResponse{}, mapToolError(err)
	}
	return result, nil
}

// ListDueGitRepositoryExtractionWorkRetryDecisions returns due retry candidates without changing work state.
func (s *Server) ListDueGitRepositoryExtractionWorkRetryDecisions(ctx context.Context, req ListDueGitRepositoryExtractionWorkRetryDecisionsRequest) (ListDueGitRepositoryExtractionWorkRetryDecisionsResponse, error) {
	result, err := s.core.ListDueRepositoryExtractionWorkRetryDecisions(ctx, req)
	if err != nil {
		return nil, mapToolError(err)
	}
	return result, nil
}

// ConsumeDueGitRepositoryExtractionWorkRetryDecision applies one due persisted retry decision.
func (s *Server) ConsumeDueGitRepositoryExtractionWorkRetryDecision(ctx context.Context, req ConsumeDueGitRepositoryExtractionWorkRetryDecisionRequest) (ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse, error) {
	result, err := s.core.ConsumeDueRepositoryExtractionWorkRetryDecision(ctx, req)
	if err != nil {
		return ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse{}, mapToolError(err)
	}
	return result, nil
}

// RunDueGitRepositoryExtractionWorkRetryControllerTick consumes one durable bounded due batch.
func (s *Server) RunDueGitRepositoryExtractionWorkRetryControllerTick(ctx context.Context, req RunDueGitRepositoryExtractionWorkRetryControllerTickRequest) (RunDueGitRepositoryExtractionWorkRetryControllerTickResponse, error) {
	result, err := s.core.RunRepositoryExtractionWorkRetryControllerTick(ctx, req)
	if err != nil {
		return RunDueGitRepositoryExtractionWorkRetryControllerTickResponse{}, mapToolError(err)
	}
	return result, nil
}

// RunExpiredGitRepositoryExtractionWorkMaintenanceTick applies one durable bounded expired-work batch.
func (s *Server) RunExpiredGitRepositoryExtractionWorkMaintenanceTick(ctx context.Context, req RunExpiredGitRepositoryExtractionWorkMaintenanceTickRequest) (RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse, error) {
	result, err := s.core.RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx, req)
	if err != nil {
		return RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse{}, mapToolError(err)
	}
	return result, nil
}

// CaptureGitRepositorySnapshot persists source authority from the Git object database.
func (s *Server) CaptureGitRepositorySnapshot(ctx context.Context, req CaptureGitRepositorySnapshotRequest) (CaptureGitRepositorySnapshotResponse, error) {
	result, err := s.core.CaptureGitRepositorySnapshot(ctx, evidenceingestion.GitRepositorySnapshotConfig{
		WorkspaceRoot: req.WorkspaceRoot,
		RepoID:        req.RepoID,
		CommitSHA:     req.CommitSHA,
		RequestID:     req.RequestID,
	})
	if err != nil {
		return CaptureGitRepositorySnapshotResponse{}, mapToolError(err)
	}
	snapshot := result.RepositorySnapshot
	return CaptureGitRepositorySnapshotResponse{
		RepositorySnapshotID:       snapshot.ID,
		RepoID:                     snapshot.RepoID,
		CommitSHA:                  snapshot.CommitSHA,
		ManifestHash:               snapshot.ManifestHash,
		ManifestEntryCount:         snapshot.ManifestEntryCount,
		RevisionVerificationMethod: snapshot.RevisionVerificationMethod,
		ManifestContract:           snapshot.ManifestContract,
		FileSelectionContract:      snapshot.FileSelectionContract,
		SelectedFileCount:          snapshot.SelectedFileCount,
		Replayed:                   result.Replayed,
	}, nil
}

// GetRepositoryExtractorInput loads persisted repository source authority without mutation.
func (s *Server) GetRepositoryExtractorInput(ctx context.Context, req GetRepositoryExtractorInputRequest) (GetRepositoryExtractorInputResponse, error) {
	result, err := s.core.BuildRepositoryExtractorInput(ctx, req.RepositorySnapshotID)
	if err != nil {
		return GetRepositoryExtractorInputResponse{}, mapToolError(err)
	}
	return result, nil
}

// CreateRepositoryExtractionRun creates a logical run without starting an invocation.
func (s *Server) CreateRepositoryExtractionRun(ctx context.Context, req CreateRepositoryExtractionRunRequest) (CreateRepositoryExtractionRunResponse, error) {
	result, err := s.core.CreateRepositoryExtractionRun(ctx, evidenceingestion.RepositoryExtractionRunRequest{RequestID: req.RequestID, RepositorySnapshotID: req.RepositorySnapshotID, ExtractorDefinition: req.ExtractorDefinition})
	if err != nil {
		return CreateRepositoryExtractionRunResponse{}, mapToolError(err)
	}
	return CreateRepositoryExtractionRunResponse{ExtractionRun: result.ExtractionRun, Replayed: result.Replayed}, nil
}

// RunRepositoryGoParserExtractor executes one repository-scoped deterministic attempt.
func (s *Server) RunRepositoryGoParserExtractor(ctx context.Context, req RunRepositoryGoParserExtractorRequest) (RunRepositoryGoParserExtractorResponse, error) {
	result, err := s.core.RunRepositoryGoParserExtractor(ctx, req)
	if err != nil {
		return RunRepositoryGoParserExtractorResponse{}, mapToolError(err)
	}
	return result, nil
}

// RunRepositoryGoplsExtractor executes one Git-authority-bound repository gopls attempt.
func (s *Server) RunRepositoryGoplsExtractor(ctx context.Context, req RunRepositoryGoplsExtractorRequest) (RunRepositoryGoplsExtractorResponse, error) {
	result, err := s.core.RunRepositoryGoplsExtractor(ctx, req)
	if err != nil {
		return RunRepositoryGoplsExtractorResponse{}, mapToolError(err)
	}
	return result, nil
}

// RunGoParserExtractor invokes a deterministic Go declaration extractor over one saved source view.
func (s *Server) RunGoParserExtractor(ctx context.Context, req RunGoParserExtractorRequest) (RunGoParserExtractorResponse, error) {
	runner, err := evidenceingestion.NewGoParserExtractorRunner(evidenceingestion.GoParserExtractorConfig{
		RepoID:    req.RepoID,
		CommitSHA: req.CommitSHA,
		Path:      req.Path,
	})
	if err != nil {
		return RunGoParserExtractorResponse{}, mapToolError(err)
	}
	result, err := s.core.RunTrustedExtractor(ctx, evidenceingestion.TrustedExtractorRequest{
		RequestID:           req.RequestID,
		ExtractionViewID:    req.ExtractionViewID,
		RetryFailedAttempt:  req.RetryFailedAttempt,
		ExtractorDefinition: runner.ExtractorDefinition(),
	}, runner.Run)
	if err != nil {
		return RunGoParserExtractorResponse{}, mapToolError(err)
	}
	trace, err := s.core.TraceProposalProvenance(ctx, result.ProposalOccurrenceID)
	if err != nil {
		return RunGoParserExtractorResponse{}, mapToolError(err)
	}
	return RunGoParserExtractorResponse{
		SourceSnapshotID:     result.SourceSnapshotID,
		ExtractionViewID:     result.ExtractionViewID,
		ExtractionRunID:      result.ExtractionRunID,
		ExtractionAttemptID:  result.ExtractionAttemptID,
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		ProposalFingerprint:  result.ProposalFingerprint,
		Status:               trace.AdmissionOutcome,
		Replayed:             result.Replayed,
	}, nil
}

// RunGoplsExtractor invokes real local gopls over one saved source view.
func (s *Server) RunGoplsExtractor(ctx context.Context, req RunGoplsExtractorRequest) (RunGoplsExtractorResponse, error) {
	runner, err := evidenceingestion.NewGoplsExtractorRunner(ctx, evidenceingestion.GoplsExtractorConfig{
		WorkspaceRoot: req.WorkspaceRoot,
		RepoID:        req.RepoID,
		CommitSHA:     req.CommitSHA,
		Path:          req.Path,
	})
	if err != nil {
		return RunGoplsExtractorResponse{}, mapToolError(err)
	}
	result, err := s.core.RunTrustedExtractor(ctx, evidenceingestion.TrustedExtractorRequest{
		RequestID:           req.RequestID,
		ExtractionViewID:    req.ExtractionViewID,
		RetryFailedAttempt:  req.RetryFailedAttempt,
		ExtractorDefinition: runner.ExtractorDefinition(),
	}, runner.Run)
	if err != nil {
		return RunGoplsExtractorResponse{}, mapToolError(err)
	}
	trace, err := s.core.TraceProposalProvenance(ctx, result.ProposalOccurrenceID)
	if err != nil {
		return RunGoplsExtractorResponse{}, mapToolError(err)
	}
	return RunGoplsExtractorResponse{
		SourceSnapshotID:     result.SourceSnapshotID,
		ExtractionViewID:     result.ExtractionViewID,
		ExtractionRunID:      result.ExtractionRunID,
		ExtractionAttemptID:  result.ExtractionAttemptID,
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		ProposalFingerprint:  result.ProposalFingerprint,
		Status:               trace.AdmissionOutcome,
		Replayed:             result.Replayed,
	}, nil
}

// RunLocalOllamaExtractor invokes a trusted local Ollama extractor over one saved source view.
func (s *Server) RunLocalOllamaExtractor(ctx context.Context, req RunLocalOllamaExtractorRequest) (RunLocalOllamaExtractorResponse, error) {
	runner, err := evidenceingestion.NewOllamaExtractorRunner(evidenceingestion.OllamaExtractorConfig{
		BaseURL:    req.BaseURL,
		Model:      req.Model,
		NumPredict: req.NumPredict,
	})
	if err != nil {
		return RunLocalOllamaExtractorResponse{}, mapToolError(err)
	}
	result, err := s.core.RunTrustedExtractor(ctx, evidenceingestion.TrustedExtractorRequest{
		RequestID:           req.RequestID,
		ExtractionViewID:    req.ExtractionViewID,
		RetryFailedAttempt:  req.RetryFailedAttempt,
		ExtractorDefinition: runner.ExtractorDefinition(),
	}, runner.Run)
	if err != nil {
		return RunLocalOllamaExtractorResponse{}, mapToolError(err)
	}
	trace, err := s.core.TraceProposalProvenance(ctx, result.ProposalOccurrenceID)
	if err != nil {
		return RunLocalOllamaExtractorResponse{}, mapToolError(err)
	}
	return RunLocalOllamaExtractorResponse{
		SourceSnapshotID:     result.SourceSnapshotID,
		ExtractionViewID:     result.ExtractionViewID,
		ExtractionRunID:      result.ExtractionRunID,
		ExtractionAttemptID:  result.ExtractionAttemptID,
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		ProposalFingerprint:  result.ProposalFingerprint,
		Status:               trace.AdmissionOutcome,
		Replayed:             result.Replayed,
	}, nil
}

// AdmitPendingProposal admits one pending proposal through the bounded admission core.
func (s *Server) AdmitPendingProposal(ctx context.Context, req AdmitPendingProposalRequest) (AdmitPendingProposalResponse, error) {
	var derivation *evidenceingestion.DerivationAdmissionInput
	if req.Derivation != nil {
		derivation = &evidenceingestion.DerivationAdmissionInput{
			ParentNodeIDs: append([]string(nil), req.Derivation.ParentNodeIDs...),
			Method:        req.Derivation.Method,
			Producer:      req.Derivation.Producer,
			TraceRef:      req.Derivation.TraceRef,
		}
	}
	result, err := s.core.AdmitPendingProposal(ctx, evidenceingestion.AdmissionInput{
		ProposalOccurrenceID: req.ProposalOccurrenceID,
		DecisionBy:           req.DecisionBy,
		DecisionReason:       req.DecisionReason,
		Derivation:           derivation,
	})
	if err != nil {
		return AdmitPendingProposalResponse{}, mapToolError(err)
	}
	return AdmitPendingProposalResponse{
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		AdmissionDecisionID:  result.AdmissionDecisionID,
		AdmissionOutcome:     result.AdmissionOutcome,
		CanonicalRef:         result.CanonicalRef,
		RawEvidenceNodeIDs:   append([]string(nil), result.RawEvidenceNodeIDs...),
		CanonicalEdgeIDs:     append([]string(nil), result.CanonicalEdgeIDs...),
		DerivationID:         result.DerivationID,
		ParentNodeIDs:        append([]string(nil), result.ParentNodeIDs...),
		Replayed:             result.Replayed,
	}, nil
}

// RecordPendingProposalDisposition records one terminal non-canonical outcome through the bounded core.
func (s *Server) RecordPendingProposalDisposition(
	ctx context.Context,
	req RecordPendingProposalDispositionRequest,
) (RecordPendingProposalDispositionResponse, error) {
	result, err := s.core.RecordPendingProposalDisposition(ctx, evidenceingestion.ProposalDispositionInput{
		ProposalOccurrenceID: req.ProposalOccurrenceID,
		Outcome:              req.Outcome,
		DecisionBy:           req.DecisionBy,
		DecisionReason:       req.DecisionReason,
	})
	if err != nil {
		return RecordPendingProposalDispositionResponse{}, mapToolError(err)
	}
	return RecordPendingProposalDispositionResponse{
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		AdmissionDecisionID:  result.AdmissionDecisionID,
		AdmissionOutcome:     result.AdmissionOutcome,
		DecisionBy:           result.DecisionBy,
		DecisionReason:       result.DecisionReason,
		Replayed:             result.Replayed,
	}, nil
}

// SubmitCanonicalContradictionProposal persists one dedicated relation proposal.
func (s *Server) SubmitCanonicalContradictionProposal(
	ctx context.Context,
	req SubmitCanonicalContradictionProposalRequest,
) (SubmitCanonicalContradictionProposalResponse, error) {
	result, err := s.core.SubmitCanonicalContradictionProposal(ctx, evidenceingestion.CanonicalContradictionProposalInput{
		RequestID:          req.RequestID,
		NodeAID:            req.NodeAID,
		NodeBID:            req.NodeBID,
		Rationale:          req.Rationale,
		ProducerName:       req.ProducerName,
		ProducerVersion:    req.ProducerVersion,
		ProducerSessionRef: req.ProducerSessionRef,
	})
	if err != nil {
		return SubmitCanonicalContradictionProposalResponse{}, mapToolError(err)
	}
	proposal := result.Proposal
	return SubmitCanonicalContradictionProposalResponse{
		CanonicalContradictionProposalID: proposal.ID,
		NodeAID:                          proposal.NodeAID,
		NodeBID:                          proposal.NodeBID,
		Relation:                         string(proposal.Relation),
		Rationale:                        proposal.Rationale,
		ProducerName:                     proposal.ProducerName,
		ProducerVersion:                  proposal.ProducerVersion,
		ProducerSessionRef:               proposal.ProducerSessionRef,
		AdmissionOutcome:                 proposal.AdmissionOutcome,
		CanonicalEdgeID:                  proposal.CanonicalEdgeID,
		Replayed:                         result.Replayed,
	}, nil
}

// AdmitPendingCanonicalContradiction records explicit approval and creates the edge.
func (s *Server) AdmitPendingCanonicalContradiction(
	ctx context.Context,
	req AdmitPendingCanonicalContradictionRequest,
) (CanonicalContradictionDecisionResponse, error) {
	result, err := s.core.AdmitPendingCanonicalContradiction(ctx, evidenceingestion.CanonicalContradictionAdmissionInput{
		ProposalID:     req.ProposalID,
		DecisionBy:     req.DecisionBy,
		DecisionReason: req.DecisionReason,
	})
	if err != nil {
		return CanonicalContradictionDecisionResponse{}, mapToolError(err)
	}
	return mapCanonicalContradictionDecision(result), nil
}

// RecordPendingCanonicalContradictionDisposition records a terminal non-edge outcome.
func (s *Server) RecordPendingCanonicalContradictionDisposition(
	ctx context.Context,
	req RecordPendingCanonicalContradictionDispositionRequest,
) (CanonicalContradictionDecisionResponse, error) {
	result, err := s.core.RecordPendingCanonicalContradictionDisposition(ctx, evidenceingestion.CanonicalContradictionDispositionInput{
		ProposalID:     req.ProposalID,
		Outcome:        req.Outcome,
		DecisionBy:     req.DecisionBy,
		DecisionReason: req.DecisionReason,
	})
	if err != nil {
		return CanonicalContradictionDecisionResponse{}, mapToolError(err)
	}
	return mapCanonicalContradictionDecision(result), nil
}

func mapCanonicalContradictionDecision(result evidenceingestion.CanonicalContradictionDecisionResult) CanonicalContradictionDecisionResponse {
	decision := result.Decision
	return CanonicalContradictionDecisionResponse{
		CanonicalContradictionProposalID: decision.ProposalID,
		AdmissionDecisionID:              decision.ID,
		AdmissionOutcome:                 decision.Outcome,
		CanonicalEdgeID:                  decision.CanonicalEdgeID,
		DecisionBy:                       decision.DecisionBy,
		DecisionReason:                   decision.DecisionReason,
		Replayed:                         result.Replayed,
	}
}

// AdmitPendingSupersession atomically admits a fresh replacement and its exact
// outgoing supersedes edge set.
func (s *Server) AdmitPendingSupersession(
	ctx context.Context,
	req AdmitPendingSupersessionRequest,
) (AdmitPendingSupersessionResponse, error) {
	result, err := s.core.AdmitPendingSupersession(ctx, evidenceingestion.SupersessionAdmissionInput{
		ProposalOccurrenceID: req.ProposalOccurrenceID,
		DecisionBy:           req.DecisionBy,
		DecisionReason:       req.DecisionReason,
		Basis:                req.Basis,
		TargetNodeIDs:        append([]string{}, req.TargetNodeIDs...),
		ExpectedRevision:     req.ExpectedRevision,
		ExpectedHeadEventID:  req.ExpectedHeadEventID,
	})
	if err != nil {
		return AdmitPendingSupersessionResponse{}, mapToolError(err)
	}
	return AdmitPendingSupersessionResponse{
		ProposalOccurrenceID:      result.ProposalOccurrenceID,
		AdmissionDecisionID:       result.AdmissionDecisionID,
		AdmissionOutcome:          result.AdmissionOutcome,
		CanonicalRef:              result.CanonicalRef,
		RawEvidenceNodeIDs:        append([]string{}, result.RawEvidenceNodeIDs...),
		CanonicalEdgeIDs:          append([]string{}, result.CanonicalEdgeIDs...),
		DerivationID:              result.DerivationID,
		ParentNodeIDs:             append([]string{}, result.ParentNodeIDs...),
		LineageKey:                result.LineageKey,
		TargetNodeIDs:             append([]string{}, result.TargetNodeIDs...),
		BootstrappedTargetNodeIDs: append([]string{}, result.BootstrappedTargetNodeIDs...),
		SupersedesEdgeIDs:         append([]string{}, result.SupersedesEdgeIDs...),
		AdmissionEventID:          result.AdmissionEventID,
		EventRevision:             result.EventRevision,
		PreviousHeadEventID:       result.PreviousHeadEventID,
		DecisionBy:                result.DecisionBy,
		DecisionReason:            result.DecisionReason,
		Replayed:                  result.Replayed,
	}, nil
}

// ActivateRepositorySourceGeneration advances one exact repository/extractor stream head.
func (s *Server) ActivateRepositorySourceGeneration(
	ctx context.Context,
	req ActivateRepositorySourceGenerationRequest,
) (ActivateRepositorySourceGenerationResponse, error) {
	result, err := s.core.ActivateRepositorySourceGeneration(ctx, req)
	if err != nil {
		return ActivateRepositorySourceGenerationResponse{}, mapToolError(err)
	}
	return result, nil
}

// ListRepositorySourceGenerations lists recent immutable generations for one exact stream.
func (s *Server) ListRepositorySourceGenerations(
	ctx context.Context,
	req ListRepositorySourceGenerationsRequest,
) (ListRepositorySourceGenerationsResponse, error) {
	generations, err := s.core.ListRepositorySourceGenerations(ctx, req)
	if err != nil {
		return ListRepositorySourceGenerationsResponse{}, mapToolError(err)
	}
	return ListRepositorySourceGenerationsResponse{
		Generations: append([]evidenceingestion.RepositorySourceGenerationStatus(nil), generations...),
		Count:       len(generations),
		Limit:       req.Limit,
	}, nil
}

// GetExtractorInput returns grounded source view data without mutating ingestion state.
func (s *Server) GetExtractorInput(ctx context.Context, req GetExtractorInputRequest) (GetExtractorInputResponse, error) {
	result, err := s.core.BuildExtractorInput(ctx, req.ExtractionViewID)
	if err != nil {
		return GetExtractorInputResponse{}, mapToolError(err)
	}
	return result, nil
}

// SubmitManualEvidence submits one manual evidence request through the ingestion core.
func (s *Server) SubmitManualEvidence(ctx context.Context, req SubmitManualEvidenceRequest) (SubmitManualEvidenceResponse, error) {
	result, err := s.core.IngestManualText(ctx, evidenceingestion.ManualTextInput{
		SourceID:       req.SourceID,
		SourceVersion:  req.SourceVersion,
		Raw:            []byte(req.RawText),
		OriginMetadata: cloneStringMap(req.OriginMetadata),
		RequestID:      req.RequestID,
	}, req.ExtractorOutput)
	if err != nil {
		return SubmitManualEvidenceResponse{}, mapToolError(err)
	}
	trace, err := s.core.TraceProposalProvenance(ctx, result.ProposalOccurrenceID)
	if err != nil {
		return SubmitManualEvidenceResponse{}, mapToolError(err)
	}
	return SubmitManualEvidenceResponse{
		SourceSnapshotID:     result.SourceSnapshotID,
		ExtractionAttemptID:  result.ExtractionAttemptID,
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		ProposalFingerprint:  result.ProposalFingerprint,
		Status:               trace.AdmissionOutcome,
		Replayed:             result.Replayed,
	}, nil
}

// SubmitExtractorOutput materializes proposals from extractor output against an existing source view.
func (s *Server) SubmitExtractorOutput(ctx context.Context, req SubmitExtractorOutputRequest) (SubmitExtractorOutputResponse, error) {
	req.ExtractorDefinition.Name = strings.TrimSpace(req.ExtractorDefinition.Name)
	req.ExtractorDefinition.Version = strings.TrimSpace(req.ExtractorDefinition.Version)
	if req.ExtractorDefinition.Name == "" || req.ExtractorDefinition.Version == "" {
		return SubmitExtractorOutputResponse{}, &ToolError{
			Code:    toolErrorInvalidRequest,
			Message: "extractor_definition.name and extractor_definition.version are required for producer provenance",
		}
	}
	result, err := s.core.SubmitExtractorOutput(ctx, evidenceingestion.ExtractorOutputInput{
		RequestID:           req.RequestID,
		SourceSnapshotID:    req.SourceSnapshotID,
		ExtractionViewID:    req.ExtractionViewID,
		ProducerSessionRef:  req.ProducerSessionRef,
		ExtractorDefinition: req.ExtractorDefinition,
		Output:              req.ExtractorOutput,
	})
	if err != nil {
		return SubmitExtractorOutputResponse{}, mapToolError(err)
	}
	if result.ProposalCount == 0 {
		return SubmitExtractorOutputResponse{
			SourceSnapshotID:    result.SourceSnapshotID,
			ExtractionViewID:    result.ExtractionViewID,
			ExtractionAttemptID: result.ExtractionAttemptID,
			ProposalCount:       0,
			Status:              "abstained",
			Replayed:            result.Replayed,
		}, nil
	}
	trace, err := s.core.TraceProposalProvenance(ctx, result.ProposalOccurrenceID)
	if err != nil {
		return SubmitExtractorOutputResponse{}, mapToolError(err)
	}
	return SubmitExtractorOutputResponse{
		SourceSnapshotID:     result.SourceSnapshotID,
		ExtractionViewID:     result.ExtractionViewID,
		ExtractionAttemptID:  result.ExtractionAttemptID,
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		ProposalFingerprint:  result.ProposalFingerprint,
		ProposalCount:        result.ProposalCount,
		Status:               trace.AdmissionOutcome,
		Replayed:             result.Replayed,
	}, nil
}

// SubmitTextSource persists raw UTF-8 source authority for later extractor use.
func (s *Server) SubmitTextSource(ctx context.Context, req SubmitTextSourceRequest) (SubmitTextSourceResponse, error) {
	result, err := s.core.CaptureManualSource(ctx, evidenceingestion.ManualTextInput{
		SourceSystem:   req.SourceSystem,
		SourceID:       req.SourceID,
		SourceVersion:  req.SourceVersion,
		Raw:            []byte(req.RawText),
		OriginMetadata: cloneStringMap(req.OriginMetadata),
		RequestID:      req.RequestID,
	})
	if err != nil {
		return SubmitTextSourceResponse{}, mapToolError(err)
	}
	return SubmitTextSourceResponse{
		SourceSnapshotID:    result.SourceSnapshotID,
		ExtractionViewID:    result.ExtractionViewID,
		SourceSystem:        result.SourceSystem,
		RawContentHash:      result.RawContentHash,
		RenderedContentHash: result.RenderedContentHash,
		SpanCatalogVersion:  result.SpanCatalogVersion,
		Spans:               append([]evidenceingestion.SpanEntry(nil), result.Spans...),
		Replayed:            result.Replayed,
	}, nil
}

// SubmitExternalSource persists one exact external object for later AHE-owned context assembly.
func (s *Server) SubmitExternalSource(
	ctx context.Context,
	req SubmitExternalSourceRequest,
) (SubmitExternalSourceResponse, error) {
	result, err := s.core.CaptureExternalSource(ctx, req)
	if err != nil {
		return SubmitExternalSourceResponse{}, mapToolError(err)
	}
	return SubmitExternalSourceResponse{
		SourceSnapshotID:      result.SourceSnapshotID,
		ExtractionViewID:      result.ExtractionViewID,
		AuthoritySourceSystem: result.SourceIntakeResult.SourceSystem,
		SourceID:              result.SourceIntakeResult.SourceID,
		SourceSystem:          result.ExternalSourceSystem,
		SourceNamespace:       result.SourceNamespace,
		ObjectType:            result.ObjectType,
		ObjectID:              result.ObjectID,
		Revision:              result.Revision,
		Coverage:              result.Coverage,
		ObservedAt:            result.ObservedAt,
		ReceivedAt:            result.ReceivedAt,
		RawContentHash:        result.RawContentHash,
		RenderedContentHash:   result.RenderedContentHash,
		SpanCatalogVersion:    result.SpanCatalogVersion,
		Spans:                 append([]evidenceingestion.SpanEntry(nil), result.Spans...),
		Replayed:              result.Replayed,
	}, nil
}

type postgresCore struct {
	pool *pgxpool.Pool
}

func (c postgresCore) AdmitPendingProposal(ctx context.Context, input evidenceingestion.AdmissionInput) (evidenceingestion.AdmissionResult, error) {
	return evidenceingestion.AdmitPendingProposal(ctx, c.pool, input)
}

func (c postgresCore) RecordPendingProposalDisposition(
	ctx context.Context,
	input evidenceingestion.ProposalDispositionInput,
) (evidenceingestion.ProposalDispositionResult, error) {
	return evidenceingestion.RecordPendingProposalDisposition(ctx, c.pool, input)
}

func (c postgresCore) SubmitCanonicalContradictionProposal(ctx context.Context, input evidenceingestion.CanonicalContradictionProposalInput) (evidenceingestion.CanonicalContradictionProposalResult, error) {
	return evidenceingestion.SubmitCanonicalContradictionProposal(ctx, c.pool, input)
}

func (c postgresCore) AdmitPendingCanonicalContradiction(ctx context.Context, input evidenceingestion.CanonicalContradictionAdmissionInput) (evidenceingestion.CanonicalContradictionDecisionResult, error) {
	return evidenceingestion.AdmitPendingCanonicalContradiction(ctx, c.pool, input)
}

func (c postgresCore) RecordPendingCanonicalContradictionDisposition(ctx context.Context, input evidenceingestion.CanonicalContradictionDispositionInput) (evidenceingestion.CanonicalContradictionDecisionResult, error) {
	return evidenceingestion.RecordPendingCanonicalContradictionDisposition(ctx, c.pool, input)
}

func (c postgresCore) AdmitPendingSupersession(ctx context.Context, input evidenceingestion.SupersessionAdmissionInput) (evidenceingestion.SupersessionAdmissionResult, error) {
	return evidenceingestion.AdmitPendingSupersession(ctx, c.pool, input)
}

func (c postgresCore) ActivateRepositorySourceGeneration(
	ctx context.Context,
	input evidenceingestion.RepositorySourceGenerationActivationInput,
) (evidenceingestion.RepositorySourceGenerationActivationResult, error) {
	return evidenceingestion.ActivateRepositorySourceGeneration(ctx, c.pool, input)
}

func (c postgresCore) ListRepositorySourceGenerations(
	ctx context.Context,
	input evidenceingestion.RepositorySourceGenerationListInput,
) ([]evidenceingestion.RepositorySourceGenerationStatus, error) {
	return evidenceingestion.ListRepositorySourceGenerations(ctx, c.pool, input)
}

func (c postgresCore) BuildExtractorInput(ctx context.Context, extractionViewID string) (evidenceingestion.ExtractorInput, error) {
	return evidenceingestion.BuildExtractorInput(ctx, c.pool, extractionViewID)
}

func (c postgresCore) BuildRepositoryExtractorInput(ctx context.Context, repositorySnapshotID string) (evidenceingestion.RepositoryExtractorInput, error) {
	return evidenceingestion.BuildRepositoryExtractorInput(ctx, c.pool, repositorySnapshotID)
}

func (c postgresCore) CaptureGitRepositorySnapshot(ctx context.Context, config evidenceingestion.GitRepositorySnapshotConfig) (evidenceingestion.RepositorySnapshotCaptureResult, error) {
	return evidenceingestion.CaptureGitRepositorySnapshot(ctx, c.pool, config)
}

func (c postgresCore) InspectGitRepositoryChange(ctx context.Context, config evidenceingestion.GitRepositoryChangeConfig) (evidenceingestion.GitRepositoryChangeInspection, error) {
	return evidenceingestion.InspectGitRepositoryChange(ctx, config)
}

func (c postgresCore) ObserveGitRepositoryChange(ctx context.Context, config evidenceingestion.GitRepositoryChangeObservationConfig) (evidenceingestion.GitRepositoryChangeObservationResult, error) {
	return evidenceingestion.ObserveGitRepositoryChange(ctx, c.pool, config)
}

func (c postgresCore) ScheduleRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkScheduleInput) (evidenceingestion.RepositoryExtractionWorkScheduleResult, error) {
	return evidenceingestion.ScheduleRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) ClaimRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkClaimInput) (evidenceingestion.RepositoryExtractionWorkClaimResult, error) {
	return evidenceingestion.ClaimRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) RenewRepositoryExtractionWorkLease(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkLeaseRenewalInput) (evidenceingestion.RepositoryExtractionWorkLeaseRenewalResult, error) {
	return evidenceingestion.RenewRepositoryExtractionWorkLease(ctx, c.pool, input)
}

func (c postgresCore) ExecuteClaimedRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExecutionInput) (evidenceingestion.RepositoryExtractionWorkExecutionResult, error) {
	return evidenceingestion.ExecuteClaimedRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) RunRepositoryExtractionWorkerTick(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkerTickInput) (evidenceingestion.RepositoryExtractionWorkerTickResult, error) {
	return evidenceingestion.RunRepositoryExtractionWorkerTick(ctx, c.pool, input)
}

func (c postgresCore) ListExpiredRepositoryExtractionWorkClaims(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredClaimListInput) ([]evidenceingestion.RepositoryExtractionWorkExpiredClaim, error) {
	return evidenceingestion.ListExpiredRepositoryExtractionWorkClaims(ctx, c.pool, input)
}

func (c postgresCore) ListExpiredRepositoryExtractionWorkExecutions(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredExecutionListInput) ([]evidenceingestion.RepositoryExtractionWorkExpiredExecution, error) {
	return evidenceingestion.ListExpiredRepositoryExtractionWorkExecutions(ctx, c.pool, input)
}

func (c postgresCore) FinishRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkFinishInput) (evidenceingestion.RepositoryExtractionWorkFinishResult, error) {
	return evidenceingestion.FinishRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) RecoverExpiredRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRecoveryInput) (evidenceingestion.RepositoryExtractionWorkRecoveryResult, error) {
	return evidenceingestion.RecoverExpiredRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) RepairExpiredRepositoryExtractionWorkExecution(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExecutionRepairInput) (evidenceingestion.RepositoryExtractionWorkExecutionRepairResult, error) {
	return evidenceingestion.RepairExpiredRepositoryExtractionWorkExecution(ctx, c.pool, input)
}

func (c postgresCore) RetryFailedRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRetryInput) (evidenceingestion.RepositoryExtractionWorkRetryResult, error) {
	return evidenceingestion.RetryFailedRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) ClassifyFailedRepositoryExtractionWork(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkFailurePolicyInput) (evidenceingestion.RepositoryExtractionWorkFailurePolicyResult, error) {
	return evidenceingestion.ClassifyFailedRepositoryExtractionWork(ctx, c.pool, input)
}

func (c postgresCore) ListDueRepositoryExtractionWorkRetryDecisions(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkDueRetryDecisionListInput) ([]evidenceingestion.RepositoryExtractionWorkDueRetryDecision, error) {
	return evidenceingestion.ListDueRepositoryExtractionWorkRetryDecisions(ctx, c.pool, input)
}

func (c postgresCore) ConsumeDueRepositoryExtractionWorkRetryDecision(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionInput) (evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult, error) {
	return evidenceingestion.ConsumeDueRepositoryExtractionWorkRetryDecision(ctx, c.pool, input)
}

func (c postgresCore) RunRepositoryExtractionWorkRetryControllerTick(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkRetryControllerTickInput) (evidenceingestion.RepositoryExtractionWorkRetryControllerTickResult, error) {
	return evidenceingestion.RunRepositoryExtractionWorkRetryControllerTick(ctx, c.pool, input)
}

func (c postgresCore) RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput) (evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickResult, error) {
	return evidenceingestion.RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx, c.pool, input)
}

func (c postgresCore) CreateRepositoryExtractionRun(ctx context.Context, request evidenceingestion.RepositoryExtractionRunRequest) (evidenceingestion.RepositoryExtractionRunResult, error) {
	return evidenceingestion.CreateRepositoryExtractionRun(ctx, c.pool, request)
}

func (c postgresCore) CaptureManualSource(ctx context.Context, input evidenceingestion.ManualTextInput) (evidenceingestion.SourceIntakeResult, error) {
	return evidenceingestion.CaptureManualSource(ctx, c.pool, input)
}

func (c postgresCore) CaptureExternalSource(ctx context.Context, input evidenceingestion.ExternalSourceEnvelopeV1) (evidenceingestion.ExternalSourceIntakeResult, error) {
	return evidenceingestion.CaptureExternalSource(ctx, c.pool, input)
}

func (c postgresCore) IngestManualText(ctx context.Context, input evidenceingestion.ManualTextInput, fixture evidenceingestion.FrozenExtractorOutput) (evidenceingestion.IngestResult, error) {
	return evidenceingestion.IngestManualText(ctx, c.pool, input, fixture)
}

func (c postgresCore) RunTrustedExtractor(ctx context.Context, request evidenceingestion.TrustedExtractorRequest, runner evidenceingestion.ExtractorRunner) (evidenceingestion.IngestResult, error) {
	return evidenceingestion.RunTrustedExtractor(ctx, c.pool, request, runner)
}

func (c postgresCore) RunRepositoryGoParserExtractor(ctx context.Context, request evidenceingestion.RepositoryGoParserRequest) (evidenceingestion.RepositoryIngestResult, error) {
	return evidenceingestion.RunRepositoryGoParserExtractor(ctx, c.pool, request)
}

func (c postgresCore) RunRepositoryGoplsExtractor(ctx context.Context, request evidenceingestion.RepositoryGoplsRequest) (evidenceingestion.RepositoryIngestResult, error) {
	return evidenceingestion.RunRepositoryGoplsExtractor(ctx, c.pool, request)
}

func (c postgresCore) SubmitExtractorOutput(ctx context.Context, input evidenceingestion.ExtractorOutputInput) (evidenceingestion.IngestResult, error) {
	return evidenceingestion.SubmitExtractorOutput(ctx, c.pool, input)
}

func (c postgresCore) TraceProposalProvenance(ctx context.Context, occurrenceID string) (evidenceingestion.ProposalQueryResult, error) {
	return evidenceingestion.TraceProposalProvenance(ctx, c.pool, occurrenceID)
}

func mapToolError(err error) error {
	var domainErr *evidenceingestion.DomainError
	if errors.As(err, &domainErr) {
		return &ToolError{
			Code:    string(domainErr.Kind),
			Message: domainErr.Error(),
			cause:   err,
		}
	}
	return &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
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

func marshalToolResponse(response any) ([]byte, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return nil, &ToolError{Code: toolErrorInternal, Message: err.Error(), cause: err}
	}
	return data, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
