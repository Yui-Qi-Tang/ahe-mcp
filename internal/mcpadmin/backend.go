// Package mcpadmin exposes trusted AHE administration tools over MCP.
package mcpadmin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

// Backend adapts existing MCP-shaped packages to the generic stdio transport.
type Backend struct {
	tools []mcpstdio.Tool
	call  func(context.Context, string, []byte) ([]byte, error)
}

// NewBackend exposes the trusted write-capable ingestion tools.
func NewBackend(server *evidenceingestionmcp.Server) *Backend {
	return &Backend{
		tools: ingestionTools(),
		call:  server.CallTool,
	}
}

// Tools returns the deterministic MCP tool list.
func (b *Backend) Tools() []mcpstdio.Tool {
	return append([]mcpstdio.Tool(nil), b.tools...)
}

// CallTool forwards one JSON tool payload to the underlying evidence adapter.
func (b *Backend) CallTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	data, err := b.call(ctx, name, []byte(arguments))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func ingestionTools() []mcpstdio.Tool {
	readOnly := true
	write := false
	destructive := false
	idempotent := true
	return []mcpstdio.Tool{
		{
			Name:        evidenceingestionmcp.ToolSubmitManualEvidence,
			Title:       "Submit Manual Evidence",
			Description: "Submit bounded manual UTF-8 text and already-produced extractor output.",
			InputSchema: submitManualEvidenceSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolSubmitTextSource,
			Title:       "Submit Text Source",
			Description: "Persist raw UTF-8 natural-language or code-file bytes as source authority for later local extraction.",
			InputSchema: submitTextSourceSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolSubmitExtractorOutput,
			Title:       "Submit Extractor Output",
			Description: "Submit local extractor output for an existing source snapshot and extraction view.",
			InputSchema: submitExtractorOutputSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolGetExtractorInput,
			Title:       "Get Extractor Input",
			Description: "Load grounded source view data for a trusted local extractor.",
			InputSchema: objectSchema(map[string]any{
				"extraction_view_id": stringSchema("Extraction view ID returned by submit_text_source."),
			}, []string{"extraction_view_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolInspectGoplsWorkspace,
			Title:       "Inspect gopls Workspace",
			Description: "Return gopls package metadata and controller-hashed Go files without creating ingestion records.",
			InputSchema: objectSchema(map[string]any{
				"workspace_root": stringSchema("Local Go workspace root to inspect."),
				"repo_id":        stringSchema("Stable repository identifier."),
				"commit_sha":     stringSchema("Repository revision represented by the workspace."),
			}, []string{"workspace_root", "repo_id", "commit_sha"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolInspectGitRepositoryChange,
			Title:       "Inspect Git Repository Change",
			Description: "Return Git HEAD plus a bounded dirty-worktree change token without creating ingestion records.",
			InputSchema: objectSchema(map[string]any{
				"workspace_root": stringSchema("Git worktree top-level to inspect."),
				"repo_id":        stringSchema("Stable repository identifier."),
			}, []string{"workspace_root", "repo_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolObserveGitRepositoryChange,
			Title:       "Observe Git Repository Change",
			Description: "Inspect Git state and persist one idempotent, coalesced stability-window observation.",
			InputSchema: objectSchema(map[string]any{
				"request_id":                    stringSchema("Idempotent Git change observation request ID."),
				"workspace_root":                stringSchema("Git worktree top-level to inspect."),
				"repo_id":                       stringSchema("Stable repository identifier."),
				"stability_window_milliseconds": boundedIntegerSchema("Caller-owned stability window in milliseconds.", 1, evidenceingestion.GitRepositoryChangeMaxStabilityWindow.Milliseconds()),
			}, []string{"request_id", "workspace_root", "repo_id", "stability_window_milliseconds"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork,
			Title:       "Schedule Git Repository Extraction Work",
			Description: "Schedule one latest stable clean Git observation for an allowlisted repository extractor stream.",
			InputSchema: objectSchema(map[string]any{
				"request_id":             stringSchema("Idempotent repository extraction work schedule request ID."),
				"observation_request_id": stringSchema("Stable Git observation request ID returned by observe_git_repository_change."),
				"extractor_name":         repositoryExtractionWorkExtractorSchema(),
			}, []string{"request_id", "observation_request_id", "extractor_name"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork,
			Title:       "Claim Git Repository Extraction Work",
			Description: "Atomically claim pending repository extraction work without running the extractor.",
			InputSchema: objectSchema(map[string]any{
				"request_id":                  stringSchema("Idempotent repository extraction work claim request ID."),
				"repo_id":                     stringSchema("Stable repository identifier."),
				"extractor_name":              repositoryExtractionWorkExtractorSchema(),
				"worker_id":                   stringSchema("Stable identity of the worker claiming this item."),
				"lease_duration_milliseconds": boundedIntegerSchema("Bounded claim lease duration in milliseconds.", evidenceingestion.RepositoryExtractionWorkMinLeaseDuration.Milliseconds(), evidenceingestion.RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()),
			}, []string{"request_id", "repo_id", "extractor_name", "worker_id", "lease_duration_milliseconds"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRenewGitRepositoryExtractionWorkLease,
			Title:       "Renew Git Repository Extraction Work Lease",
			Description: "Extend one exact running claim before its current lease expires.",
			InputSchema: objectSchema(map[string]any{
				"request_id":                  stringSchema("Idempotent repository extraction work lease renewal request ID."),
				"work_item_id":                stringSchema("Work item ID currently bound to the running claim."),
				"claim_id":                    stringSchema("Exact running claim ID returned by claim_git_repository_extraction_work."),
				"worker_id":                   stringSchema("Worker identity recorded by the running claim."),
				"lease_duration_milliseconds": boundedIntegerSchema("Bounded lease duration from the renewal time in milliseconds.", evidenceingestion.RepositoryExtractionWorkMinLeaseDuration.Milliseconds(), evidenceingestion.RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()),
			}, []string{"request_id", "work_item_id", "claim_id", "worker_id", "lease_duration_milliseconds"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork,
			Title:       "Execute Claimed Git Repository Extraction Work",
			Description: "Capture and deterministically extract one exact running claim with its inherited lease heartbeat, then finish it only on success.",
			InputSchema: objectSchema(map[string]any{
				"request_id":     stringSchema("Idempotent repository extraction work execution request ID."),
				"workspace_root": stringSchema("Git worktree top-level used to read the work item's immutable commit."),
				"work_item_id":   stringSchema("Work item ID currently bound to the running claim."),
				"claim_id":       stringSchema("Exact running claim ID returned by claim_git_repository_extraction_work."),
				"worker_id":      stringSchema("Worker identity recorded by the running claim."),
			}, []string{"request_id", "workspace_root", "work_item_id", "claim_id", "worker_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunGitRepositoryExtractionWorkerTick,
			Title:       "Run Git Repository Extraction Worker Tick",
			Description: "Claim and, when available, execute at most one item from one repository extraction stream.",
			InputSchema: objectSchema(map[string]any{
				"request_id":                  stringSchema("Idempotent repository extraction worker tick request ID."),
				"workspace_root":              stringSchema("Git worktree top-level used to read a claimed work item's immutable commit."),
				"repo_id":                     stringSchema("Stable repository identifier."),
				"extractor_name":              repositoryExtractionWorkExtractorSchema(),
				"worker_id":                   stringSchema("Stable identity of the worker running this tick."),
				"lease_duration_milliseconds": boundedIntegerSchema("Bounded claim lease inherited by execution heartbeats.", evidenceingestion.RepositoryExtractionWorkMinLeaseDuration.Milliseconds(), evidenceingestion.RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()),
			}, []string{"request_id", "workspace_root", "repo_id", "extractor_name", "worker_id", "lease_duration_milliseconds"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkClaims,
			Title:       "List Expired Git Repository Extraction Work Claims",
			Description: "List bounded still-running claims whose current leases have elapsed, without deciding abandonment or recovering work.",
			InputSchema: objectSchema(map[string]any{
				"repo_id":        stringSchema("Stable repository identifier."),
				"extractor_name": repositoryExtractionWorkExtractorSchema(),
				"limit":          boundedIntegerSchema("Maximum elapsed-lease claims to return.", 1, evidenceingestion.RepositoryExtractionWorkExpiredClaimMaxLimit),
			}, []string{"repo_id", "extractor_name", "limit"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkExecutions,
			Title:       "List Expired Git Repository Extraction Work Executions",
			Description: "List bounded execution-owned running claims whose current leases have elapsed, without deciding abandonment or repairing work.",
			InputSchema: objectSchema(map[string]any{
				"limit": boundedIntegerSchema("Maximum execution-owned elapsed-lease claims to return across streams.", 1, evidenceingestion.RepositoryExtractionWorkExpiredExecutionMaxLimit),
			}, []string{"limit"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork,
			Title:       "Finish Git Repository Extraction Work",
			Description: "Record a generation-bound success or bounded failure for claimed repository extraction work.",
			InputSchema: repositoryExtractionWorkFinishSchema(),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRecoverExpiredGitRepositoryExtractionWork,
			Title:       "Recover Expired Git Repository Extraction Work",
			Description: "Release one exact expired claim by requeueing it or superseding it behind newer pending work.",
			InputSchema: objectSchema(map[string]any{
				"request_id":        stringSchema("Idempotent repository extraction work recovery request ID."),
				"work_item_id":      stringSchema("Work item ID currently bound to the expired claim."),
				"claim_id":          stringSchema("Exact expired claim ID returned by claim_git_repository_extraction_work."),
				"recovery_actor_id": stringSchema("Stable identity of the operator or controller requesting recovery."),
			}, []string{"request_id", "work_item_id", "claim_id", "recovery_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution,
			Title:       "Repair Expired Git Repository Extraction Work Execution",
			Description: "Repair one exact execution-owned claim after its lease expires, preserving immutable execution and claim audit.",
			InputSchema: objectSchema(map[string]any{
				"request_id":           stringSchema("Idempotent repository extraction work execution repair request ID."),
				"execution_request_id": stringSchema("Exact execution request that owns the expired claim."),
				"work_item_id":         stringSchema("Work item ID bound to the execution request."),
				"claim_id":             stringSchema("Exact expired claim ID bound to the execution request."),
				"worker_id":            stringSchema("Worker identity bound to the execution request."),
				"repair_actor_id":      stringSchema("Stable identity of the operator or controller requesting repair."),
			}, []string{"request_id", "execution_request_id", "work_item_id", "claim_id", "worker_id", "repair_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRetryFailedGitRepositoryExtractionWork,
			Title:       "Retry Failed Git Repository Extraction Work",
			Description: "Release one exact failed claim by requeueing it or superseding it behind newer pending work.",
			InputSchema: objectSchema(map[string]any{
				"request_id":     stringSchema("Idempotent repository extraction work retry request ID."),
				"work_item_id":   stringSchema("Work item ID currently bound to the failed claim."),
				"claim_id":       stringSchema("Exact failed claim ID returned by claim_git_repository_extraction_work."),
				"retry_actor_id": stringSchema("Stable identity of the operator or controller requesting retry."),
			}, []string{"request_id", "work_item_id", "claim_id", "retry_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolCaptureGitRepositorySnapshot,
			Title:       "Capture Git Repository Snapshot",
			Description: "Persist one verified full Git commit and its tracked regular Go file snapshots.",
			InputSchema: objectSchema(map[string]any{
				"request_id":     stringSchema("Idempotent repository snapshot intake request ID."),
				"workspace_root": stringSchema("Git worktree top-level used only to access the object database."),
				"repo_id":        stringSchema("Stable repository identifier."),
				"commit_sha":     stringSchema("Lowercase full Git commit SHA to verify and persist."),
			}, []string{"request_id", "workspace_root", "repo_id", "commit_sha"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolGetRepositoryExtractorInput,
			Title:       "Get Repository Extractor Input",
			Description: "Load exact persisted Go file bytes for one immutable repository snapshot.",
			InputSchema: objectSchema(map[string]any{
				"repository_snapshot_id": stringSchema("Repository snapshot ID returned by capture_git_repository_snapshot."),
			}, []string{"repository_snapshot_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolCreateRepositoryExtractionRun,
			Title:       "Create Repository Extraction Run",
			Description: "Create an extraction run bound to one immutable repository snapshot without starting execution.",
			InputSchema: objectSchema(map[string]any{
				"request_id":             stringSchema("Idempotent extraction run request ID."),
				"repository_snapshot_id": stringSchema("Repository snapshot ID returned by capture_git_repository_snapshot."),
				"extractor_definition":   objectSchema(map[string]any{"name": stringSchema("Extractor name."), "version": stringSchema("Extractor version."), "config": objectSchema(map[string]any{}, nil)}, nil),
			}, []string{"request_id", "repository_snapshot_id"}),
			Annotations: mcpstdio.Annotations{ReadOnlyHint: &write, DestructiveHint: &destructive, IdempotentHint: &idempotent},
		},
		{
			Name:        evidenceingestionmcp.ToolRunRepositoryGoParserExtractor,
			Title:       "Run Repository Go Parser Extractor",
			Description: "Run deterministic declaration extraction over all persisted Go files in one repository snapshot.",
			InputSchema: objectSchema(map[string]any{
				"request_id":             stringSchema("Logical repository extraction request ID."),
				"repository_snapshot_id": stringSchema("Repository snapshot ID returned by capture_git_repository_snapshot."),
				"retry_failed_attempt":   boolSchema("Retry a failed repository attempt under the same logical run."),
			}, []string{"request_id", "repository_snapshot_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunRepositoryGoplsExtractor,
			Title:       "Run Repository gopls Extractor",
			Description: "Run one gopls session over Git-verified persisted Go files, persist parser-grounded declarations and unique cross-file definitions, and report definition/reference coverage.",
			InputSchema: objectSchema(map[string]any{
				"request_id":             stringSchema("Logical repository extraction request ID."),
				"repository_snapshot_id": stringSchema("Repository snapshot ID returned by capture_git_repository_snapshot."),
				"workspace_root":         stringSchema("Git worktree top-level used only for object access and the gopls workspace."),
				"retry_failed_attempt":   boolSchema("Retry a failed repository attempt under the same logical run."),
			}, []string{"request_id", "repository_snapshot_id", "workspace_root"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolListRepositorySourceGenerations,
			Title:       "List Repository Source Generations",
			Description: "List recent immutable generations and current-head state for one exact repository/extractor stream.",
			InputSchema: objectSchema(map[string]any{
				"repo_id": stringSchema("Exact repository stream identity."),
				"extractor_name": enumStringSchema(
					"Allowlisted repository extractor stream.",
					evidenceingestion.ExtractorRepositoryGoParserCodeFact,
					evidenceingestion.ExtractorRepositoryGoplsCodeFact,
				),
				"limit": boundedIntegerSchema(
					"Maximum recent generations to return.",
					1,
					evidenceingestion.RepositorySourceGenerationListMaxLimit,
				),
			}, []string{"repo_id", "extractor_name", "limit"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolActivateRepositorySourceGeneration,
			Title:       "Activate Repository Source Generation",
			Description: "Atomically activate one exact repository source generation and reconcile it against the current stream head.",
			InputSchema: objectSchema(map[string]any{
				"request_id":           stringSchema("Idempotent repository source generation activation request ID."),
				"source_generation_id": stringSchema("Exact generation ID returned by repository extraction or repository query results."),
			}, []string{"request_id", "source_generation_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunLocalOllamaExtractor,
			Title:       "Run Local Ollama Extractor",
			Description: "Invoke local Ollama against one grounded source view and persist materialized proposals.",
			InputSchema: objectSchema(map[string]any{
				"request_id":           stringSchema("Logical extraction request ID."),
				"extraction_view_id":   stringSchema("Extraction view ID returned by submit_text_source."),
				"model":                stringSchema("Local Ollama model name."),
				"base_url":             stringSchema("Optional local Ollama base URL. Defaults to http://127.0.0.1:11434."),
				"retry_failed_attempt": boolSchema("Retry a previously failed attempt as a new attempt in the same run."),
				"num_predict":          integerSchema("Optional Ollama num_predict limit."),
			}, []string{"request_id", "extraction_view_id", "model"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunGoParserExtractor,
			Title:       "Run Go Parser Extractor",
			Description: "Extract Go declarations from one grounded source view and persist materialized proposals.",
			InputSchema: objectSchema(map[string]any{
				"request_id":           stringSchema("Logical extraction request ID."),
				"extraction_view_id":   stringSchema("Extraction view ID returned by submit_text_source."),
				"repo_id":              stringSchema("Stable repository identifier."),
				"commit_sha":           stringSchema("Repository revision for this Go source file."),
				"path":                 stringSchema("Repository-relative Go file path."),
				"retry_failed_attempt": boolSchema("Retry a previously failed attempt as a new attempt in the same run."),
			}, []string{"request_id", "extraction_view_id", "repo_id", "commit_sha", "path"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunGoplsExtractor,
			Title:       "Run gopls Extractor",
			Description: "Extract gopls-confirmed Go declarations from one grounded source view and persist materialized proposals.",
			InputSchema: objectSchema(map[string]any{
				"request_id":           stringSchema("Logical extraction request ID."),
				"extraction_view_id":   stringSchema("Extraction view ID returned by submit_text_source."),
				"workspace_root":       stringSchema("Local Go workspace root containing the source path."),
				"repo_id":              stringSchema("Stable repository identifier."),
				"commit_sha":           stringSchema("Repository revision for this Go source file."),
				"path":                 stringSchema("Repository-relative Go file path."),
				"retry_failed_attempt": boolSchema("Retry a previously failed attempt as a new attempt in the same run."),
			}, []string{"request_id", "extraction_view_id", "workspace_root", "repo_id", "commit_sha", "path"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolAdmitPendingProposal,
			Title:       "Admit Pending Proposal",
			Description: "Admit one pending proposal occurrence into the canonical evidence graph.",
			InputSchema: objectSchema(map[string]any{
				"proposal_occurrence_id": stringSchema("Pending proposal occurrence ID."),
				"decision_by":            stringSchema("Optional admission producer or reviewer identity."),
				"decision_reason":        stringSchema("Optional bounded reason for admission."),
			}, []string{"proposal_occurrence_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRecordPendingProposalDisposition,
			Title:       "Record Pending Proposal Disposition",
			Description: "Record one exact pending proposal as rejected or audit-only without creating canonical evidence.",
			InputSchema: objectSchema(map[string]any{
				"proposal_occurrence_id": stringSchema("Pending proposal occurrence ID."),
				"outcome": enumStringSchema(
					"Terminal non-canonical outcome.",
					evidenceingestion.ProposalDispositionRejected,
					evidenceingestion.ProposalDispositionAuditOnly,
				),
				"decision_by": boundedStringSchema(
					fmt.Sprintf(
						"Stable operator or reviewer identity; at most %d UTF-8 bytes.",
						evidenceingestion.ProposalDispositionDecisionByMaxBytes,
					),
					evidenceingestion.ProposalDispositionDecisionByMaxBytes,
				),
				"decision_reason": boundedStringSchema(
					fmt.Sprintf(
						"Source-bounded reason for the terminal disposition; at most %d UTF-8 bytes.",
						evidenceingestion.ProposalDispositionDecisionReasonMaxBytes,
					),
					evidenceingestion.ProposalDispositionDecisionReasonMaxBytes,
				),
			}, []string{"proposal_occurrence_id", "outcome", "decision_by", "decision_reason"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolClassifyFailedGitRepositoryExtractionWork,
			Title:       "Classify Failed Git Repository Extraction Work",
			Description: "Record deterministic retry-policy-v1 classification and optional backoff for one exact terminal failed claim without requeueing it.",
			InputSchema: objectSchema(map[string]any{
				"request_id":        stringSchema("Idempotent repository extraction work failure policy request ID."),
				"work_item_id":      stringSchema("Work item ID currently bound to the failed claim."),
				"claim_id":          stringSchema("Exact terminal failed claim ID returned by claim_git_repository_extraction_work."),
				"decision_actor_id": stringSchema("Stable identity of the controller recording the policy decision."),
			}, []string{"request_id", "work_item_id", "claim_id", "decision_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolListDueGitRepositoryExtractionWorkRetryDecisions,
			Title:       "List Due Git Repository Extraction Work Retry Decisions",
			Description: "List bounded unconsumed retry-policy decisions whose retry deadlines have elapsed, without consuming decisions or changing work.",
			InputSchema: objectSchema(map[string]any{
				"limit": boundedIntegerSchema("Maximum due retry-policy decisions to return across streams.", 1, evidenceingestion.RepositoryExtractionWorkDueRetryDecisionMaxLimit),
			}, []string{"limit"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision,
			Title:       "Consume Due Git Repository Extraction Work Retry Decision",
			Description: "Consume one exact due retry-policy decision by requeueing its failed work or superseding it behind newer pending work.",
			InputSchema: objectSchema(map[string]any{
				"request_id":                stringSchema("Idempotent retry decision consumption request ID."),
				"failure_policy_request_id": stringSchema("Exact persisted failure-policy request ID authorizing retry."),
				"work_item_id":              stringSchema("Work item ID bound to the failed claim and policy decision."),
				"claim_id":                  stringSchema("Exact terminal failed claim ID bound to the policy decision."),
				"consumer_actor_id":         stringSchema("Stable identity of the controller consuming the due decision."),
			}, []string{"request_id", "failure_policy_request_id", "work_item_id", "claim_id", "consumer_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunDueGitRepositoryExtractionWorkRetryControllerTick,
			Title:       "Run Due Git Repository Extraction Work Retry Controller Tick",
			Description: "Atomically consume one durable bounded cross-stream batch of due retry-policy decisions without polling or executing extractors.",
			InputSchema: objectSchema(map[string]any{
				"request_id":        stringSchema("Idempotent repository extraction work retry controller tick request ID."),
				"limit":             boundedIntegerSchema("Maximum due retry-policy decisions to consume in this tick.", 1, evidenceingestion.RepositoryExtractionWorkRetryControllerTickMaxLimit),
				"consumer_actor_id": stringSchema("Stable identity of the controller consuming the bounded due batch."),
			}, []string{"request_id", "limit", "consumer_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
		{
			Name:        evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick,
			Title:       "Run Expired Git Repository Extraction Work Maintenance Tick",
			Description: "Atomically recover ownerless claims or repair execution-owned claims in one durable bounded cross-stream elapsed-lease batch without polling or worker-liveness inference.",
			InputSchema: objectSchema(map[string]any{
				"request_id":           stringSchema("Idempotent repository extraction work expired maintenance tick request ID."),
				"limit":                boundedIntegerSchema("Maximum elapsed-lease work transitions to apply in this tick.", 1, evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit),
				"maintenance_actor_id": stringSchema("Stable identity of the controller applying the bounded maintenance batch."),
			}, []string{"request_id", "limit", "maintenance_actor_id"}),
			Annotations: mcpstdio.Annotations{
				ReadOnlyHint:    &write,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
			},
		},
	}
}

func submitManualEvidenceSchema() map[string]any {
	properties := manualSourceProperties()
	properties["extractor_output"] = extractorOutputSchema()
	return objectSchema(properties, []string{
		"source_id",
		"source_version",
		"raw_text",
		"request_id",
		"extractor_output",
	})
}

func submitTextSourceSchema() map[string]any {
	properties := manualSourceProperties()
	properties["source_system"] = stringSchema("Optional source system: manual_text (default) or code_file.")
	return objectSchema(properties, []string{
		"source_id",
		"source_version",
		"raw_text",
		"request_id",
	})
}

func submitExtractorOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"request_id":           stringSchema("Logical extraction request ID."),
		"source_snapshot_id":   stringSchema("Source snapshot ID returned by submit_text_source."),
		"extraction_view_id":   stringSchema("Extraction view ID returned by submit_text_source."),
		"extractor_definition": extractorDefinitionSchema(),
		"extractor_output":     extractorOutputSchema(),
	}, []string{
		"request_id",
		"source_snapshot_id",
		"extraction_view_id",
		"extractor_output",
	})
}

func extractorDefinitionSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":    stringSchema("Extractor implementation name."),
		"version": stringSchema("Extractor implementation/configuration version."),
		"config":  stringMapSchema("Bounded deterministic extractor configuration."),
	}, []string{"name", "version"})
}

func manualSourceProperties() map[string]any {
	return map[string]any{
		"source_id":       stringSchema("Stable source identifier supplied by the caller."),
		"source_version":  stringSchema("Stable source version supplied by the caller."),
		"raw_text":        stringSchema("Exact UTF-8 source text bytes represented as JSON string."),
		"request_id":      stringSchema("Idempotency key for the logical ingestion request."),
		"origin_metadata": stringMapSchema("Optional bounded source-origin metadata."),
	}
}

func extractorOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"proposals": map[string]any{
			"type": "array",
			"items": objectSchema(map[string]any{
				"proposal_local_id": stringSchema("Batch-local proposal identifier."),
				"statement_text":    stringSchema("Proposed statement text."),
				"code_fact":         codeFactOutputSchema(),
				"evidence_refs": map[string]any{
					"type":        "array",
					"description": "Span catalog IDs such as span:S1.",
					"items":       map[string]any{"type": "string"},
				},
			}, []string{"proposal_local_id", "statement_text", "evidence_refs"}),
		},
	}, []string{"proposals"})
}

func codeFactOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"schema_version":    stringSchema("Code fact transport schema version."),
		"fact_kind":         stringSchema("Bounded code fact kind."),
		"repo_id":           stringSchema("Stable repository identifier."),
		"commit_sha":        stringSchema("Immutable repository revision."),
		"path":              stringSchema("Repository-relative file path."),
		"file_content_hash": stringSchema("SHA-256 identity of the exact file bytes."),
		"symbol_ref":        stringSchema("Version-bound typed symbol reference."),
		"symbol_kind":       stringSchema("Declaration kind such as type, function, or method."),
		"qualified_name":    stringSchema("Package-qualified symbol name."),
		"start_byte":        integerSchema("Exact symbol start byte in the extraction view."),
		"end_byte":          integerSchema("Exact symbol end byte in the extraction view."),
		"quoted_text_hash":  stringSchema("SHA-256 hash of the exact symbol bytes."),
	}, []string{
		"schema_version",
		"fact_kind",
		"repo_id",
		"commit_sha",
		"path",
		"file_content_hash",
		"symbol_ref",
		"symbol_kind",
		"qualified_name",
		"start_byte",
		"end_byte",
		"quoted_text_hash",
	})
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

func boundedStringSchema(description string, maxLength int) map[string]any {
	schema := stringSchema(description)
	schema["maxLength"] = maxLength
	return schema
}

func enumStringSchema(description string, values ...string) map[string]any {
	schema := stringSchema(description)
	schema["enum"] = append([]string(nil), values...)
	return schema
}

func repositoryExtractionWorkExtractorSchema() map[string]any {
	schema := stringSchema("Allowlisted repository extractor stream name.")
	schema["enum"] = []string{
		evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		evidenceingestion.ExtractorRepositoryGoplsCodeFact,
	}
	return schema
}

func repositoryExtractionWorkFinishSchema() map[string]any {
	schema := objectSchema(map[string]any{
		"request_id":           stringSchema("Idempotent repository extraction work finish request ID."),
		"work_item_id":         stringSchema("Work item ID returned by schedule or claim."),
		"claim_id":             stringSchema("Claim ID returned by claim_git_repository_extraction_work."),
		"worker_id":            stringSchema("Worker identity recorded by the claim."),
		"outcome":              repositoryExtractionWorkOutcomeSchema(),
		"source_generation_id": stringSchema("Required succeeded outcome authority from a completed repository extraction."),
		"failure_class":        stringSchema("Required bounded failure classification for a failed outcome."),
		"failure_message":      stringSchema("Required bounded failure diagnostic for a failed outcome."),
	}, []string{"request_id", "work_item_id", "claim_id", "worker_id", "outcome"})
	schema["oneOf"] = []map[string]any{
		{
			"properties": map[string]any{
				"outcome": map[string]any{"const": evidenceingestion.RepositoryExtractionWorkOutcomeSucceeded},
			},
			"required": []string{"source_generation_id"},
			"not": map[string]any{
				"anyOf": []map[string]any{
					{"required": []string{"failure_class"}},
					{"required": []string{"failure_message"}},
				},
			},
		},
		{
			"properties": map[string]any{
				"outcome": map[string]any{"const": evidenceingestion.RepositoryExtractionWorkOutcomeFailed},
			},
			"required": []string{"failure_class", "failure_message"},
			"not":      map[string]any{"required": []string{"source_generation_id"}},
		},
	}
	return schema
}

func repositoryExtractionWorkOutcomeSchema() map[string]any {
	schema := stringSchema("Terminal repository extraction work outcome.")
	schema["enum"] = []string{
		evidenceingestion.RepositoryExtractionWorkOutcomeSucceeded,
		evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
	}
	return schema
}

func stringMapSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"additionalProperties": map[string]any{"type": "string"},
	}
}

func boolSchema(description string) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": description,
	}
}

func integerSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

func boundedIntegerSchema(description string, minimum, maximum int64) map[string]any {
	schema := integerSchema(description)
	schema["minimum"] = minimum
	schema["maximum"] = maximum
	return schema
}
