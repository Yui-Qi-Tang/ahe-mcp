//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpadmin"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpquery"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationStdioMCPIngestAdmitAndQueryRoundTrip(t *testing.T) {
	ctx, pool := stdioIntegrationPool(t)
	ingestCore, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	queryCore, err := evidencequerymcp.NewServer(pool)
	if err != nil {
		t.Fatalf("query NewServer() error = %v", err)
	}
	ingestBackend, err := mcpadmin.NewSourceIngressBackend(ctx, mcpadmin.NewBackend(ingestCore))
	if err != nil {
		t.Fatalf("ingest NewSourceIngressBackend() error = %v", err)
	}
	t.Cleanup(ingestBackend.Close)
	ingestMCP := stdioServer(t, "ahe-ingest-mcp-test", ingestBackend)
	queryMCP := stdioServer(t, "ahe-query-mcp-test", mcpquery.NewBackend(queryCore))

	assertStdioInitialized(t, ctx, ingestMCP, "ahe-ingest-mcp-test")
	assertStdioInitialized(t, ctx, queryMCP, "ahe-query-mcp-test")
	assertStdioToolNames(t, ctx, ingestMCP, []string{
		evidenceingestionmcp.ToolSubmitManualEvidence,
		evidenceingestionmcp.ToolSubmitTextSource,
		evidenceingestionmcp.ToolSubmitExtractorOutput,
		evidenceingestionmcp.ToolGetExtractorInput,
		evidenceingestionmcp.ToolInspectGoplsWorkspace,
		evidenceingestionmcp.ToolInspectGitRepositoryChange,
		evidenceingestionmcp.ToolObserveGitRepositoryChange,
		evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRenewGitRepositoryExtractionWorkLease,
		evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRunGitRepositoryExtractionWorkerTick,
		evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkClaims,
		evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkExecutions,
		evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRecoverExpiredGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution,
		evidenceingestionmcp.ToolRetryFailedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolCaptureGitRepositorySnapshot,
		evidenceingestionmcp.ToolGetRepositoryExtractorInput,
		evidenceingestionmcp.ToolCreateRepositoryExtractionRun,
		evidenceingestionmcp.ToolRunRepositoryGoParserExtractor,
		evidenceingestionmcp.ToolRunRepositoryGoplsExtractor,
		evidenceingestionmcp.ToolListRepositorySourceGenerations,
		evidenceingestionmcp.ToolActivateRepositorySourceGeneration,
		evidenceingestionmcp.ToolRunLocalOllamaExtractor,
		evidenceingestionmcp.ToolRunGoParserExtractor,
		evidenceingestionmcp.ToolRunGoplsExtractor,
		evidenceingestionmcp.ToolAdmitPendingProposal,
		evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		evidenceingestionmcp.ToolSubmitCanonicalContradictionProposal,
		evidenceingestionmcp.ToolAdmitPendingCanonicalContradiction,
		evidenceingestionmcp.ToolRecordPendingCanonicalContradictionDisposition,
		evidenceingestionmcp.ToolAdmitPendingSupersession,
		evidenceingestionmcp.ToolClassifyFailedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolListDueGitRepositoryExtractionWorkRetryDecisions,
		evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision,
		evidenceingestionmcp.ToolRunDueGitRepositoryExtractionWorkRetryControllerTick,
		evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick,
		evidenceingestionmcp.ToolSubmitExternalSource,
	})
	assertStdioToolNames(t, ctx, queryMCP, []string{
		evidencequerymcp.ToolGetEvidenceRecord,
		evidencequerymcp.ToolListEvidenceRecords,
		evidencequerymcp.ToolSearchEvidenceRecords,
		evidencequerymcp.ToolGetGroundedEvidenceBrief,
		evidencequerymcp.ToolListEvidenceNeighbors,
		evidencequerymcp.ToolGetRelationProvenance,
		evidencequerymcp.ToolGetMCPReadSourceStates,
		evidencequerymcp.ToolOpenCanonicalReadView,
		evidencequerymcp.ToolFindCanonicalPath,
		evidencequerymcp.ToolGetCanonicalTopologyDiagnostics,
		evidencequerymcp.ToolGetCanonicalContradictionProposal,
		evidencequerymcp.ToolGetCanonicalSupersessionHead,
		evidencequerymcp.ToolGetCanonicalSupersessionCurrentness,
	})

	gitRoot := t.TempDir()
	stdioWriteFile(t, filepath.Join(gitRoot, "main.go"), []byte("package main\n"))
	stdioRunGit(t, gitRoot, "init", "--quiet")
	stdioRunGit(t, gitRoot, "config", "user.name", "AHE Test")
	stdioRunGit(t, gitRoot, "config", "user.email", "ahe-test@example.com")
	stdioRunGit(t, gitRoot, "add", ".")
	stdioRunGit(t, gitRoot, "commit", "--quiet", "-m", "initial")
	cleanChange := stdioCallTool[evidenceingestionmcp.InspectGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolInspectGitRepositoryChange, map[string]any{
		"workspace_root": gitRoot,
		"repo_id":        "stdio-change-token",
	})
	if cleanChange.Dirty || cleanChange.ChangeToken == "" || cleanChange.HeadCommitSHA == "" {
		t.Fatalf("clean Git change inspection = %+v", cleanChange)
	}
	stdioWriteFile(t, filepath.Join(gitRoot, "main.go"), []byte("package main\n\nfunc Changed() {}\n"))
	dirtyChange := stdioCallTool[evidenceingestionmcp.InspectGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolInspectGitRepositoryChange, map[string]any{
		"workspace_root": gitRoot,
		"repo_id":        "stdio-change-token",
	})
	if !dirtyChange.Dirty || dirtyChange.TrackedChangeCount != 1 || dirtyChange.DirtyFingerprint == "" || dirtyChange.ChangeToken == cleanChange.ChangeToken {
		t.Fatalf("dirty Git change inspection = %+v, clean = %+v", dirtyChange, cleanChange)
	}
	for _, table := range []string{"source_snapshots", "repository_snapshots", "proposal_occurrences", "repository_source_generations", "repository_change_observations", "repository_change_observation_requests", "repository_extraction_work_items", "repository_extraction_work_schedule_requests", "repository_extraction_work_claim_requests", "repository_extraction_work_finish_requests", "repository_extraction_work_claim_attempts", "repository_extraction_work_recovery_requests", "repository_extraction_work_retry_requests", "repository_extraction_work_lease_renewal_requests", "repository_extraction_work_execution_requests", "repository_extraction_work_execution_repairs", "repository_extraction_work_failure_policy_decisions", "repository_extraction_work_retry_decision_consumptions", "repository_extraction_work_retry_controller_tick_requests", "repository_extraction_work_retry_controller_tick_items", "repository_extraction_work_expired_maintenance_tick_requests", "repository_extraction_work_expired_maintenance_tick_items"} {
		stdioAssertTableCount(t, ctx, pool, table, 0)
	}
	observation := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-change-observation",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-change-token",
		"stability_window_milliseconds": 1,
	})
	if observation.Replayed || !observation.Changed || observation.Coalesced || observation.Stable || observation.ObservationNumber != 1 || observation.ObservationCount != 1 || observation.Inspection != dirtyChange {
		t.Fatalf("persisted Git change observation = %+v, dirty inspection = %+v", observation, dirtyChange)
	}
	observationReplay := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-change-observation",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-change-token",
		"stability_window_milliseconds": 1,
	})
	if !observationReplay.Replayed || observationReplay.ObservationID != observation.ObservationID || observationReplay.ObservationCount != observation.ObservationCount || observationReplay.LastObservedAt != observation.LastObservedAt {
		t.Fatalf("replayed Git change observation = %+v, first = %+v", observationReplay, observation)
	}
	time.Sleep(10 * time.Millisecond)
	dirtyStableObservation := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-dirty-stable-observation",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-change-token",
		"stability_window_milliseconds": 1,
	})
	if !dirtyStableObservation.Stable || !dirtyStableObservation.Coalesced || dirtyStableObservation.ObservationID != observation.ObservationID || dirtyStableObservation.ObservationCount != 2 {
		t.Fatalf("stable dirty Git observation = %+v, first = %+v", dirtyStableObservation, observation)
	}
	dirtyScheduleErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork, map[string]any{
		"request_id":             "stdio-dirty-work-schedule",
		"observation_request_id": dirtyStableObservation.RequestID,
		"extractor_name":         evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	})
	if dirtyScheduleErr.Code != string(evidenceingestion.ErrorRepositoryWorkConflict) {
		t.Fatalf("dirty schedule error = %+v, want %s", dirtyScheduleErr, evidenceingestion.ErrorRepositoryWorkConflict)
	}

	stdioRunGit(t, gitRoot, "add", ".")
	stdioRunGit(t, gitRoot, "commit", "--quiet", "-m", "changed")
	cleanObservation := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-clean-observation-first",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-change-token",
		"stability_window_milliseconds": 1,
	})
	if cleanObservation.Stable || !cleanObservation.Changed || cleanObservation.Inspection.Dirty || cleanObservation.ObservationNumber != 2 {
		t.Fatalf("first clean Git observation = %+v", cleanObservation)
	}
	time.Sleep(10 * time.Millisecond)
	cleanStableObservation := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-clean-observation-stable",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-change-token",
		"stability_window_milliseconds": 1,
	})
	if !cleanStableObservation.Stable || !cleanStableObservation.Coalesced || cleanStableObservation.ObservationID != cleanObservation.ObservationID || cleanStableObservation.ObservationCount != 2 {
		t.Fatalf("stable clean Git observation = %+v, first = %+v", cleanStableObservation, cleanObservation)
	}

	scheduledWork := stdioCallTool[evidenceingestionmcp.ScheduleGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork, map[string]any{
		"request_id":             "stdio-clean-work-schedule",
		"observation_request_id": cleanStableObservation.RequestID,
		"extractor_name":         evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	})
	if !scheduledWork.Created || scheduledWork.Coalesced || scheduledWork.Replayed || scheduledWork.Work.RepoID != "stdio-change-token" || scheduledWork.Work.ObservationID != cleanStableObservation.ObservationID {
		t.Fatalf("scheduled clean work = %+v", scheduledWork)
	}
	scheduledWorkReplay := stdioCallTool[evidenceingestionmcp.ScheduleGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork, map[string]any{
		"request_id":             "stdio-clean-work-schedule",
		"observation_request_id": cleanStableObservation.RequestID,
		"extractor_name":         evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	})
	if !scheduledWorkReplay.Replayed || scheduledWorkReplay.Work != scheduledWork.Work {
		t.Fatalf("scheduled work replay = %+v, first = %+v", scheduledWorkReplay, scheduledWork)
	}
	claimedWork := stdioCallTool[evidenceingestionmcp.ClaimGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, map[string]any{
		"request_id":                  "stdio-clean-work-claim",
		"repo_id":                     "stdio-change-token",
		"extractor_name":              evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"worker_id":                   "stdio-worker",
		"lease_duration_milliseconds": 1,
	})
	if !claimedWork.Claimed || claimedWork.Replayed || claimedWork.Work == nil || *claimedWork.Work != scheduledWork.Work || claimedWork.ClaimID == "" || claimedWork.AttemptNumber != 1 || claimedWork.ClaimedAt == nil || claimedWork.LeaseExpiresAt == nil || !claimedWork.LeaseExpiresAt.After(*claimedWork.ClaimedAt) {
		t.Fatalf("claimed clean work = %+v, scheduled = %+v", claimedWork, scheduledWork)
	}
	time.Sleep(10 * time.Millisecond)
	expiredClaims := stdioCallTool[evidenceingestionmcp.ListExpiredGitRepositoryExtractionWorkClaimsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkClaims, map[string]any{
		"repo_id":        "stdio-change-token",
		"extractor_name": evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"limit":          10,
	})
	if len(expiredClaims) != 1 || expiredClaims[0].Work != scheduledWork.Work || expiredClaims[0].ClaimID != claimedWork.ClaimID || expiredClaims[0].AttemptNumber != claimedWork.AttemptNumber || expiredClaims[0].WorkerID != claimedWork.WorkerID || claimedWork.ClaimedAt == nil || !expiredClaims[0].ClaimedAt.Equal(*claimedWork.ClaimedAt) || claimedWork.LeaseExpiresAt == nil || !expiredClaims[0].LeaseExpiresAt.Equal(*claimedWork.LeaseExpiresAt) {
		t.Fatalf("expired clean work claims = %+v, first claim = %+v", expiredClaims, claimedWork)
	}
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 0)
	recoveredWork := stdioCallTool[evidenceingestionmcp.RecoverExpiredGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRecoverExpiredGitRepositoryExtractionWork, map[string]any{
		"request_id":        "stdio-clean-work-recovery",
		"work_item_id":      scheduledWork.Work.WorkItemID,
		"claim_id":          claimedWork.ClaimID,
		"recovery_actor_id": "stdio-controller",
	})
	if recoveredWork.Replayed || recoveredWork.Work != scheduledWork.Work || recoveredWork.ClaimID != claimedWork.ClaimID || recoveredWork.AttemptNumber != 1 || recoveredWork.Disposition != evidenceingestion.RepositoryExtractionWorkRecoveryDispositionRequeued || recoveredWork.RecoveredAt.Before(*claimedWork.LeaseExpiresAt) {
		t.Fatalf("recovered clean work = %+v, first claim = %+v", recoveredWork, claimedWork)
	}
	expiredClaimsAfterRecovery := stdioCallTool[evidenceingestionmcp.ListExpiredGitRepositoryExtractionWorkClaimsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkClaims, map[string]any{
		"repo_id":        "stdio-change-token",
		"extractor_name": evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"limit":          10,
	})
	if len(expiredClaimsAfterRecovery) != 0 {
		t.Fatalf("expired claims after recovery = %+v, want empty", expiredClaimsAfterRecovery)
	}
	recoveredWorkReplay := stdioCallTool[evidenceingestionmcp.RecoverExpiredGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRecoverExpiredGitRepositoryExtractionWork, map[string]any{
		"request_id":        "stdio-clean-work-recovery",
		"work_item_id":      scheduledWork.Work.WorkItemID,
		"claim_id":          claimedWork.ClaimID,
		"recovery_actor_id": "stdio-controller",
	})
	if !recoveredWorkReplay.Replayed || recoveredWorkReplay.Work != recoveredWork.Work || !recoveredWorkReplay.RecoveredAt.Equal(recoveredWork.RecoveredAt) {
		t.Fatalf("recovered work replay = %+v, first = %+v", recoveredWorkReplay, recoveredWork)
	}
	reclaimedWork := stdioCallTool[evidenceingestionmcp.ClaimGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, map[string]any{
		"request_id":                  "stdio-clean-work-reclaim",
		"repo_id":                     "stdio-change-token",
		"extractor_name":              evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"worker_id":                   "stdio-worker-2",
		"lease_duration_milliseconds": 60000,
	})
	if !reclaimedWork.Claimed || reclaimedWork.Work == nil || *reclaimedWork.Work != scheduledWork.Work || reclaimedWork.AttemptNumber != 2 || reclaimedWork.ClaimID == claimedWork.ClaimID {
		t.Fatalf("reclaimed clean work = %+v, first claim = %+v", reclaimedWork, claimedWork)
	}
	renewedWork := stdioCallTool[evidenceingestionmcp.RenewGitRepositoryExtractionWorkLeaseResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRenewGitRepositoryExtractionWorkLease, map[string]any{
		"request_id":                  "stdio-clean-work-renew-lease",
		"work_item_id":                scheduledWork.Work.WorkItemID,
		"claim_id":                    reclaimedWork.ClaimID,
		"worker_id":                   reclaimedWork.WorkerID,
		"lease_duration_milliseconds": 120000,
	})
	if renewedWork.Replayed || renewedWork.Work != scheduledWork.Work || renewedWork.ClaimID != reclaimedWork.ClaimID || renewedWork.AttemptNumber != 2 || renewedWork.WorkerID != reclaimedWork.WorkerID || renewedWork.LeaseDurationMilliseconds != 120000 || !renewedWork.PriorLeaseExpiresAt.Equal(*reclaimedWork.LeaseExpiresAt) || !renewedWork.RenewedAt.Before(renewedWork.PriorLeaseExpiresAt) || !renewedWork.LeaseExpiresAt.After(renewedWork.PriorLeaseExpiresAt) {
		t.Fatalf("renewed clean work lease = %+v, reclaimed work = %+v", renewedWork, reclaimedWork)
	}
	expiredClaimsAfterRenewal := stdioCallTool[evidenceingestionmcp.ListExpiredGitRepositoryExtractionWorkClaimsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkClaims, map[string]any{
		"repo_id":        "stdio-change-token",
		"extractor_name": evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"limit":          10,
	})
	if len(expiredClaimsAfterRenewal) != 0 {
		t.Fatalf("expired claims after lease renewal = %+v, renewed work = %+v", expiredClaimsAfterRenewal, renewedWork)
	}
	staleFinishErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-stale-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        claimedWork.ClaimID,
		"worker_id":       claimedWork.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_invocation_timeout",
		"failure_message": "expired worker returned after recovery",
	})
	if staleFinishErr.Code != string(evidenceingestion.ErrorRepositoryWorkConflict) {
		t.Fatalf("stale finish error = %+v, want %s", staleFinishErr, evidenceingestion.ErrorRepositoryWorkConflict)
	}
	finishedWork := stdioCallTool[evidenceingestionmcp.FinishGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        reclaimedWork.ClaimID,
		"worker_id":       reclaimedWork.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_invocation_failed",
		"failure_message": "fixture worker stopped before extraction",
	})
	if finishedWork.Replayed || finishedWork.Outcome != evidenceingestion.RepositoryExtractionWorkOutcomeFailed || finishedWork.Work != scheduledWork.Work || finishedWork.ClaimID != reclaimedWork.ClaimID || finishedWork.AttemptNumber != reclaimedWork.AttemptNumber || !finishedWork.LeaseExpiresAt.Equal(renewedWork.LeaseExpiresAt) || finishedWork.FailureClass != "runner_invocation_failed" || finishedWork.FinishedAt.IsZero() {
		t.Fatalf("finished clean work = %+v", finishedWork)
	}
	renewedWorkReplay := stdioCallTool[evidenceingestionmcp.RenewGitRepositoryExtractionWorkLeaseResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRenewGitRepositoryExtractionWorkLease, map[string]any{
		"request_id":                  "stdio-clean-work-renew-lease",
		"work_item_id":                scheduledWork.Work.WorkItemID,
		"claim_id":                    reclaimedWork.ClaimID,
		"worker_id":                   reclaimedWork.WorkerID,
		"lease_duration_milliseconds": 120000,
	})
	if !renewedWorkReplay.Replayed || renewedWorkReplay.Work != renewedWork.Work || !renewedWorkReplay.RenewedAt.Equal(renewedWork.RenewedAt) || !renewedWorkReplay.LeaseExpiresAt.Equal(renewedWork.LeaseExpiresAt) {
		t.Fatalf("renewed work replay after finish = %+v, first = %+v", renewedWorkReplay, renewedWork)
	}
	finishedWorkReplay := stdioCallTool[evidenceingestionmcp.FinishGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        reclaimedWork.ClaimID,
		"worker_id":       reclaimedWork.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_invocation_failed",
		"failure_message": "fixture worker stopped before extraction",
	})
	if !finishedWorkReplay.Replayed || finishedWorkReplay.Work != finishedWork.Work || !finishedWorkReplay.FinishedAt.Equal(finishedWork.FinishedAt) {
		t.Fatalf("finished work replay = %+v, first = %+v", finishedWorkReplay, finishedWork)
	}
	failurePolicy := stdioCallTool[evidenceingestionmcp.ClassifyFailedGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClassifyFailedGitRepositoryExtractionWork, map[string]any{
		"request_id":        "stdio-clean-work-failure-policy",
		"work_item_id":      scheduledWork.Work.WorkItemID,
		"claim_id":          reclaimedWork.ClaimID,
		"decision_actor_id": "stdio-controller",
	})
	if failurePolicy.Replayed || failurePolicy.Work != scheduledWork.Work || failurePolicy.ClaimID != reclaimedWork.ClaimID || failurePolicy.AttemptNumber != 2 || failurePolicy.FailureClass != finishedWork.FailureClass || failurePolicy.PolicyVersion != evidenceingestion.RepositoryExtractionWorkFailurePolicyV1 || failurePolicy.Decision != evidenceingestion.RepositoryExtractionWorkFailureDecisionRetry || failurePolicy.DecisionReason != evidenceingestion.RepositoryExtractionWorkFailureReasonRetryable || failurePolicy.MaxAttempts != evidenceingestion.RepositoryExtractionWorkFailurePolicyMaxAttempts || failurePolicy.BackoffMilliseconds != 10000 || failurePolicy.RetryNotBefore == nil || !failurePolicy.RetryNotBefore.Equal(failurePolicy.DecidedAt.Add(10*time.Second)) {
		t.Fatalf("failed work policy = %+v, failed work = %+v", failurePolicy, finishedWork)
	}
	prematureRetryDecisionInput := map[string]any{
		"request_id":                "stdio-clean-work-retry-decision-consumption",
		"failure_policy_request_id": failurePolicy.RequestID,
		"work_item_id":              scheduledWork.Work.WorkItemID,
		"claim_id":                  reclaimedWork.ClaimID,
		"consumer_actor_id":         "stdio-retry-controller",
	}
	prematureRetryDecisionErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, prematureRetryDecisionInput)
	if prematureRetryDecisionErr.Code != string(evidenceingestion.ErrorRepositoryWorkConflict) {
		t.Fatalf("premature retry decision consumption error = %+v, want %s", prematureRetryDecisionErr, evidenceingestion.ErrorRepositoryWorkConflict)
	}
	dueRetryDecisionsBeforeDeadline := stdioCallTool[evidenceingestionmcp.ListDueGitRepositoryExtractionWorkRetryDecisionsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListDueGitRepositoryExtractionWorkRetryDecisions, map[string]any{
		"limit": 10,
	})
	if len(dueRetryDecisionsBeforeDeadline) != 0 {
		t.Fatalf("due retry decisions before deadline = %+v, want empty", dueRetryDecisionsBeforeDeadline)
	}
	if wait := time.Until(*failurePolicy.RetryNotBefore); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	dueRetryDecisions := stdioCallTool[evidenceingestionmcp.ListDueGitRepositoryExtractionWorkRetryDecisionsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListDueGitRepositoryExtractionWorkRetryDecisions, map[string]any{
		"limit": 10,
	})
	if len(dueRetryDecisions) != 1 || dueRetryDecisions[0].FailurePolicyRequestID != failurePolicy.RequestID || dueRetryDecisions[0].Work != scheduledWork.Work || dueRetryDecisions[0].ClaimID != reclaimedWork.ClaimID || dueRetryDecisions[0].AttemptNumber != 2 || dueRetryDecisions[0].WorkerID != reclaimedWork.WorkerID || dueRetryDecisions[0].Decision != evidenceingestion.RepositoryExtractionWorkFailureDecisionRetry || !dueRetryDecisions[0].RetryNotBefore.Equal(*failurePolicy.RetryNotBefore) {
		t.Fatalf("due retry decisions = %+v, policy = %+v", dueRetryDecisions, failurePolicy)
	}
	retryControllerInput := map[string]any{
		"request_id":        "stdio-clean-work-retry-controller-tick",
		"limit":             10,
		"consumer_actor_id": "stdio-retry-controller",
	}
	retryControllerTick := stdioCallTool[evidenceingestionmcp.RunDueGitRepositoryExtractionWorkRetryControllerTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunDueGitRepositoryExtractionWorkRetryControllerTick, retryControllerInput)
	if retryControllerTick.Replayed || retryControllerTick.RequestID != "stdio-clean-work-retry-controller-tick" || retryControllerTick.Limit != 10 || retryControllerTick.ConsumerActorID != "stdio-retry-controller" || retryControllerTick.DiscoveredAt.Before(*failurePolicy.RetryNotBefore) || len(retryControllerTick.Consumptions) != 1 {
		t.Fatalf("retry controller tick = %+v, policy = %+v", retryControllerTick, failurePolicy)
	}
	consumedRetryDecision := retryControllerTick.Consumptions[0]
	if consumedRetryDecision.Replayed || consumedRetryDecision.FailurePolicyRequestID != failurePolicy.RequestID || consumedRetryDecision.Work != scheduledWork.Work || consumedRetryDecision.ClaimID != reclaimedWork.ClaimID || consumedRetryDecision.AttemptNumber != 2 || consumedRetryDecision.WorkerID != reclaimedWork.WorkerID || consumedRetryDecision.Decision != evidenceingestion.RepositoryExtractionWorkFailureDecisionRetry || consumedRetryDecision.Disposition != evidenceingestion.RepositoryExtractionWorkRetryDispositionRequeued || consumedRetryDecision.FailureClass != finishedWork.FailureClass || consumedRetryDecision.FailureMessage != finishedWork.FailureMessage || !consumedRetryDecision.FinishedAt.Equal(finishedWork.FinishedAt) || !consumedRetryDecision.RetryNotBefore.Equal(*failurePolicy.RetryNotBefore) || consumedRetryDecision.ConsumedAt.Before(consumedRetryDecision.RetryNotBefore) {
		t.Fatalf("consumed retry decision = %+v, policy = %+v, failed work = %+v", consumedRetryDecision, failurePolicy, finishedWork)
	}
	retryDecisionReplayInput := map[string]any{
		"request_id":                consumedRetryDecision.RequestID,
		"failure_policy_request_id": failurePolicy.RequestID,
		"work_item_id":              scheduledWork.Work.WorkItemID,
		"claim_id":                  reclaimedWork.ClaimID,
		"consumer_actor_id":         "stdio-retry-controller",
	}
	dueRetryDecisionsAfterConsumption := stdioCallTool[evidenceingestionmcp.ListDueGitRepositoryExtractionWorkRetryDecisionsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListDueGitRepositoryExtractionWorkRetryDecisions, map[string]any{
		"limit": 10,
	})
	if len(dueRetryDecisionsAfterConsumption) != 0 {
		t.Fatalf("due retry decisions after consumption = %+v, want empty", dueRetryDecisionsAfterConsumption)
	}
	failurePolicyReplay := stdioCallTool[evidenceingestionmcp.ClassifyFailedGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClassifyFailedGitRepositoryExtractionWork, map[string]any{
		"request_id":        "stdio-clean-work-failure-policy",
		"work_item_id":      scheduledWork.Work.WorkItemID,
		"claim_id":          reclaimedWork.ClaimID,
		"decision_actor_id": "stdio-controller",
	})
	if !failurePolicyReplay.Replayed || failurePolicyReplay.AttemptNumber != failurePolicy.AttemptNumber || failurePolicyReplay.Decision != failurePolicy.Decision || failurePolicyReplay.RetryNotBefore == nil || !failurePolicyReplay.RetryNotBefore.Equal(*failurePolicy.RetryNotBefore) || !failurePolicyReplay.DecidedAt.Equal(failurePolicy.DecidedAt) {
		t.Fatalf("failure policy replay after retry = %+v, first = %+v", failurePolicyReplay, failurePolicy)
	}
	retryControllerTickReplay := stdioCallTool[evidenceingestionmcp.RunDueGitRepositoryExtractionWorkRetryControllerTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunDueGitRepositoryExtractionWorkRetryControllerTick, retryControllerInput)
	if !retryControllerTickReplay.Replayed || len(retryControllerTickReplay.Consumptions) != 1 || !retryControllerTickReplay.Consumptions[0].Replayed || !retryControllerTickReplay.DiscoveredAt.Equal(retryControllerTick.DiscoveredAt) || !retryControllerTickReplay.Consumptions[0].ConsumedAt.Equal(consumedRetryDecision.ConsumedAt) {
		t.Fatalf("retry controller tick replay = %+v, first = %+v", retryControllerTickReplay, retryControllerTick)
	}
	consumedRetryDecisionReplay := stdioCallTool[evidenceingestionmcp.ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, retryDecisionReplayInput)
	if !consumedRetryDecisionReplay.Replayed || consumedRetryDecisionReplay.Work != consumedRetryDecision.Work || !consumedRetryDecisionReplay.ConsumedAt.Equal(consumedRetryDecision.ConsumedAt) {
		t.Fatalf("consumed retry decision replay = %+v, first = %+v", consumedRetryDecisionReplay, consumedRetryDecision)
	}
	thirdClaim := stdioCallTool[evidenceingestionmcp.ClaimGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, map[string]any{
		"request_id":                  "stdio-clean-work-third-claim",
		"repo_id":                     "stdio-change-token",
		"extractor_name":              evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"worker_id":                   "stdio-worker-3",
		"lease_duration_milliseconds": 60000,
	})
	if !thirdClaim.Claimed || thirdClaim.Work == nil || *thirdClaim.Work != scheduledWork.Work || thirdClaim.AttemptNumber != 3 || thirdClaim.ClaimID == reclaimedWork.ClaimID {
		t.Fatalf("third clean work claim = %+v, failed claim = %+v", thirdClaim, reclaimedWork)
	}
	finishedWorkReplayAfterRetry := stdioCallTool[evidenceingestionmcp.FinishGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        reclaimedWork.ClaimID,
		"worker_id":       reclaimedWork.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_invocation_failed",
		"failure_message": "fixture worker stopped before extraction",
	})
	if !finishedWorkReplayAfterRetry.Replayed || !finishedWorkReplayAfterRetry.FinishedAt.Equal(finishedWork.FinishedAt) || finishedWorkReplayAfterRetry.AttemptNumber != 2 {
		t.Fatalf("finished work replay after retry = %+v, first = %+v", finishedWorkReplayAfterRetry, finishedWork)
	}
	consumedRetryDecisionReplayAfterClaim := stdioCallTool[evidenceingestionmcp.ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, retryDecisionReplayInput)
	if !consumedRetryDecisionReplayAfterClaim.Replayed || !consumedRetryDecisionReplayAfterClaim.ConsumedAt.Equal(consumedRetryDecision.ConsumedAt) || consumedRetryDecisionReplayAfterClaim.AttemptNumber != 2 {
		t.Fatalf("consumed retry decision replay after claim = %+v, first = %+v", consumedRetryDecisionReplayAfterClaim, consumedRetryDecision)
	}
	staleRetriedFinishErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-retried-stale-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        reclaimedWork.ClaimID,
		"worker_id":       reclaimedWork.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_invocation_failed",
		"failure_message": "retried worker returned another finish",
	})
	if staleRetriedFinishErr.Code != string(evidenceingestion.ErrorRepositoryWorkConflict) {
		t.Fatalf("stale retried finish error = %+v, want %s", staleRetriedFinishErr, evidenceingestion.ErrorRepositoryWorkConflict)
	}
	thirdFinishedWork := stdioCallTool[evidenceingestionmcp.FinishGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-third-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        thirdClaim.ClaimID,
		"worker_id":       thirdClaim.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_output_invalid",
		"failure_message": "third fixture worker returned invalid output",
	})
	if thirdFinishedWork.Replayed || thirdFinishedWork.AttemptNumber != 3 || thirdFinishedWork.ClaimID != thirdClaim.ClaimID || thirdFinishedWork.FailureClass != "runner_output_invalid" {
		t.Fatalf("third finished work = %+v, third claim = %+v", thirdFinishedWork, thirdClaim)
	}
	thirdFinishedWorkReplay := stdioCallTool[evidenceingestionmcp.FinishGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork, map[string]any{
		"request_id":      "stdio-clean-work-third-finish",
		"work_item_id":    scheduledWork.Work.WorkItemID,
		"claim_id":        thirdClaim.ClaimID,
		"worker_id":       thirdClaim.WorkerID,
		"outcome":         evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		"failure_class":   "runner_output_invalid",
		"failure_message": "third fixture worker returned invalid output",
	})
	if !thirdFinishedWorkReplay.Replayed || !thirdFinishedWorkReplay.FinishedAt.Equal(thirdFinishedWork.FinishedAt) {
		t.Fatalf("third finished work replay = %+v, first = %+v", thirdFinishedWorkReplay, thirdFinishedWork)
	}

	stdioAssertTableCount(t, ctx, pool, "repository_change_observations", 2)
	stdioAssertTableCount(t, ctx, pool, "repository_change_observation_requests", 4)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_schedule_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 3)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 2)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 3)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 0)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_retry_controller_tick_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_retry_controller_tick_items", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_requests", 0)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_items", 0)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 0)
	for _, table := range []string{"source_snapshots", "repository_snapshots", "proposal_occurrences", "repository_source_generations"} {
		stdioAssertTableCount(t, ctx, pool, table, 0)
	}

	raw, fixtureData := stdioRawAndFixtureData(t)
	source := stdioCallTool[evidenceingestionmcp.SubmitTextSourceResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitTextSource, map[string]any{
		"request_id":      "stdio-source-intake",
		"source_id":       "fixture-refund-policy",
		"source_version":  "v1",
		"raw_text":        string(raw),
		"origin_metadata": map[string]string{"fixture": "manual_refund_policy"},
	})
	if source.Replayed {
		t.Fatalf("submit_text_source replayed = true, want false")
	}
	if source.SourceSnapshotID == "" || source.ExtractionViewID == "" || len(source.Spans) != 2 {
		t.Fatalf("source response not grounded: %+v", source)
	}

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("ollama path = %q, want /api/generate", r.URL.Path)
		}
		var req struct {
			Model   string         `json:"model"`
			Prompt  string         `json:"prompt"`
			Stream  bool           `json:"stream"`
			Think   bool           `json:"think"`
			Options map[string]any `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode ollama request: %v", err)
		}
		if req.Model != "fixture-model" || req.Stream || req.Think {
			t.Fatalf("ollama request = %+v", req)
		}
		if !strings.Contains(req.Prompt, "span:S1") || strings.Contains(req.Prompt, "canonical_id") || strings.Contains(req.Prompt, "proposal_fingerprint") {
			t.Fatalf("unexpected prompt boundary: %s", req.Prompt)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"response": string(fixtureData)}); err != nil {
			t.Fatalf("encode ollama response: %v", err)
		}
	}))
	t.Cleanup(ollama.Close)

	proposal := stdioCallTool[evidenceingestionmcp.RunLocalOllamaExtractorResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, map[string]any{
		"request_id":         "stdio-local-ollama-run",
		"extraction_view_id": source.ExtractionViewID,
		"model":              "fixture-model",
		"base_url":           ollama.URL,
		"num_predict":        256,
	})
	if proposal.Status != "pending" {
		t.Fatalf("proposal status = %q, want pending", proposal.Status)
	}
	if proposal.SourceSnapshotID != source.SourceSnapshotID || proposal.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("proposal source = %s/%s, want %s/%s", proposal.SourceSnapshotID, proposal.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if proposal.ExtractionRunID == "" || proposal.ExtractionAttemptID == "" || proposal.ProposalOccurrenceID == "" || proposal.ProposalFingerprint == "" {
		t.Fatalf("proposal IDs not populated: %+v", proposal)
	}

	admission := stdioCallTool[evidenceingestionmcp.AdmitPendingProposalResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolAdmitPendingProposal, map[string]any{
		"proposal_occurrence_id": proposal.ProposalOccurrenceID,
		"decision_by":            "stdio-integration-test",
		"decision_reason":        "fixture statement accepted",
	})
	if admission.AdmissionOutcome != "admitted" {
		t.Fatalf("admission outcome = %q, want admitted", admission.AdmissionOutcome)
	}
	if admission.CanonicalRef == "" || admission.AdmissionDecisionID == "" {
		t.Fatalf("admission IDs not populated: %+v", admission)
	}

	proposalRecord := stdioCallTool[evidencequerymcp.GetEvidenceRecordResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetEvidenceRecord, map[string]any{
		"proposal_occurrence_id": proposal.ProposalOccurrenceID,
	})
	if proposalRecord.RecordRef.Kind != "proposal" || proposalRecord.RecordRef.ID != proposal.ProposalOccurrenceID {
		t.Fatalf("proposal record ref = %+v, want %s", proposalRecord.RecordRef, proposal.ProposalOccurrenceID)
	}
	if proposalRecord.AdmissionOutcome != "admitted" || proposalRecord.CanonicalRef == nil || *proposalRecord.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("proposal lifecycle = %q/%v, want admitted/%s", proposalRecord.AdmissionOutcome, proposalRecord.CanonicalRef, admission.CanonicalRef)
	}
	if proposalRecord.Extractor.Name != evidenceingestion.ExtractorOllamaLocal || proposalRecord.Extractor.AttemptStatus != "succeeded" {
		t.Fatalf("proposal extractor provenance = %+v", proposalRecord.Extractor)
	}

	canonicalRecord := stdioCallTool[evidencequerymcp.GetEvidenceRecordResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetEvidenceRecord, map[string]any{
		"canonical_id": admission.CanonicalRef,
	})
	if canonicalRecord.RecordRef.Kind != "canonical_evidence" || canonicalRecord.RecordRef.ID != admission.CanonicalRef {
		t.Fatalf("canonical record ref = %+v, want %s", canonicalRecord.RecordRef, admission.CanonicalRef)
	}
	if canonicalRecord.ProposalOriginRef == nil || canonicalRecord.ProposalOriginRef.ID != proposal.ProposalOccurrenceID {
		t.Fatalf("canonical proposal origin = %+v, want %s", canonicalRecord.ProposalOriginRef, proposal.ProposalOccurrenceID)
	}
	if canonicalRecord.Canonical == nil || canonicalRecord.Canonical.NodeKind != "source_claim" || canonicalRecord.Canonical.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("canonical payload = %+v", canonicalRecord.Canonical)
	}

	list := stdioCallTool[evidencequerymcp.ListEvidenceRecordsResponse](t, ctx, queryMCP, evidencequerymcp.ToolListEvidenceRecords, map[string]any{
		"source_snapshot_id": source.SourceSnapshotID,
		"admission_outcome":  "admitted",
		"limit":              10,
	})
	if list.Count != 1 || len(list.Records) != 1 || list.Records[0].RecordRef.ID != proposal.ProposalOccurrenceID {
		t.Fatalf("list evidence response = %+v, want admitted proposal %s", list, proposal.ProposalOccurrenceID)
	}
	if list.Records[0].CanonicalRef == nil || *list.Records[0].CanonicalRef != admission.CanonicalRef {
		t.Fatalf("listed canonical ref = %v, want %s", list.Records[0].CanonicalRef, admission.CanonicalRef)
	}
	search := stdioCallTool[evidencequerymcp.SearchEvidenceRecordsResponse](t, ctx, queryMCP, evidencequerymcp.ToolSearchEvidenceRecords, map[string]any{
		"query":              "refunds completed",
		"source_snapshot_id": source.SourceSnapshotID,
		"limit":              10,
	})
	if search.Count != 1 || search.Matches[0].Record.RecordRef.ID != proposal.ProposalOccurrenceID || search.Matches[0].Rank <= 0 {
		t.Fatalf("search evidence response = %+v", search)
	}
	brief := stdioCallTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetGroundedEvidenceBrief, map[string]any{
		"query":              "refund complete",
		"source_snapshot_id": source.SourceSnapshotID,
		"limit":              10,
	})
	if brief.Counts.ReturnedMatches != 1 || len(brief.Matches) != 1 || brief.Matches[0].RecordRef.ID != proposal.ProposalOccurrenceID {
		t.Fatalf("grounded evidence brief = %+v", brief)
	}
	if brief.SchemaVersion != evidencequerymcp.GroundedEvidenceBriefSchemaV2 ||
		brief.Matches[0].SourceContext != nil {
		t.Fatalf("grounded evidence brief v2 contract = %+v", brief)
	}
	if brief.Counts.Admission.Admitted != 1 || brief.Counts.Lifecycle.SourceSnapshot != 1 || brief.Boundary.Truncated {
		t.Fatalf("grounded evidence brief counts/boundary = %+v/%+v", brief.Counts, brief.Boundary)
	}
	if brief.QueryExecution.CompletionReason != evidenceingestion.EvidenceQueryCompletionMorphologyCandidates ||
		brief.QueryExecution.QueryCount != 2 ||
		brief.QueryExecution.GlobalAbsenceInferenceAllowed {
		t.Fatalf("grounded evidence brief query execution = %+v", brief.QueryExecution)
	}
	if len(brief.SourceScopes) != 1 || brief.SourceScopes[0].ScopeRef.ID != source.SourceSnapshotID || len(brief.Coverage) != 0 {
		t.Fatalf("grounded evidence brief source/coverage = %+v/%+v", brief.SourceScopes, brief.Coverage)
	}
	if !stdioBriefHasCanonicalFollowUp(brief.FollowUps, admission.CanonicalRef) {
		t.Fatalf("grounded evidence brief follow-ups = %+v", brief.FollowUps)
	}
	briefV3 := stdioCallTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetGroundedEvidenceBrief, map[string]any{
		"query":              "refund complete",
		"response_schema":    evidencequerymcp.GroundedEvidenceBriefSchemaV3,
		"source_snapshot_id": source.SourceSnapshotID,
		"limit":              10,
	})
	if briefV3.SchemaVersion != evidencequerymcp.GroundedEvidenceBriefSchemaV3 ||
		len(briefV3.Matches) != 1 ||
		briefV3.Matches[0].RecordRef.ID != proposal.ProposalOccurrenceID ||
		briefV3.Matches[0].SourceContext == nil {
		t.Fatalf("grounded evidence brief v3 = %+v", briefV3)
	}
	sourceContext := briefV3.Matches[0].SourceContext
	if sourceContext.Status != evidenceingestion.GroundedEvidenceSourceContextStatusAvailable ||
		sourceContext.SearchParticipation ||
		len(sourceContext.SearchCore) != 1 ||
		sourceContext.SearchCore[0] != briefV3.Matches[0].SourceRefs[0] ||
		sourceContext.AtomicContainer == nil ||
		sourceContext.AtomicContainer.UnitKind != evidenceingestion.SourceStructureUnitParagraph ||
		len(sourceContext.AtomicContainer.SourceRefs) != 2 ||
		sourceContext.ContextEnvelope == nil {
		t.Fatalf("grounded evidence brief v3 source context = %+v", sourceContext)
	}
	briefV4 := stdioCallTool[evidencequerymcp.GroundedEvidenceBriefResponse](
		t,
		ctx,
		queryMCP,
		evidencequerymcp.ToolGetGroundedEvidenceBrief,
		map[string]any{
			"query":              "refund complete",
			"response_schema":    evidencequerymcp.GroundedEvidenceBriefSchemaV4,
			"source_snapshot_id": source.SourceSnapshotID,
			"limit":              10,
		},
	)
	if briefV4.SchemaVersion != evidencequerymcp.GroundedEvidenceBriefSchemaV4 ||
		len(briefV4.Matches) != 1 ||
		briefV4.Matches[0].RecordRef.ID != proposal.ProposalOccurrenceID ||
		briefV4.Matches[0].SourceContext == nil ||
		briefV4.Matches[0].RepositoryContext != nil ||
		briefV4.Matches[0].SourceContext.Status !=
			evidenceingestion.GroundedEvidenceSourceContextStatusAvailable {
		t.Fatalf("grounded evidence brief v4 manual context = %+v", briefV4)
	}
	briefV5 := stdioCallTool[evidencequerymcp.GroundedEvidenceBriefResponse](
		t,
		ctx,
		queryMCP,
		evidencequerymcp.ToolGetGroundedEvidenceBrief,
		map[string]any{
			"query":              "refund complete",
			"response_schema":    evidencequerymcp.GroundedEvidenceBriefSchemaV5,
			"source_snapshot_id": source.SourceSnapshotID,
			"limit":              10,
		},
	)
	if briefV5.SchemaVersion != evidencequerymcp.GroundedEvidenceBriefSchemaV5 ||
		len(briefV5.Matches) != 1 ||
		briefV5.Matches[0].CanonicalRef == nil ||
		briefV5.Matches[0].CanonicalRef.ID != admission.CanonicalRef ||
		briefV5.Matches[0].SourceContext != nil ||
		briefV5.Matches[0].RepositoryContext != nil {
		t.Fatalf("grounded evidence brief v5 = %+v", briefV5)
	}
	recordState := briefV5.Matches[0].RecordState
	if recordState == nil ||
		recordState.AuthorityStatus != "canonical_evidence_admitted" ||
		recordState.RecordLifecycle != "source_snapshot" ||
		recordState.SourceBindingKind != evidenceingestion.ProposalSourceBindingSourceSnapshot ||
		recordState.RevisionKind != "source_version" ||
		recordState.Revision != "v1" ||
		recordState.ExternalFreshnessStatus != "not_evaluated" {
		t.Fatalf("grounded evidence brief v5 record state = %+v", recordState)
	}
	neighbors := stdioCallTool[evidencequerymcp.ListEvidenceNeighborsResponse](t, ctx, queryMCP, evidencequerymcp.ToolListEvidenceNeighbors, map[string]any{
		"canonical_id": admission.RawEvidenceNodeIDs[0],
		"direction":    evidenceingestion.RelationDirectionOutgoing,
		"relation":     "supports_claim",
		"limit":        10,
	})
	if neighbors.Depth != 1 || neighbors.Count != 1 || neighbors.Neighbors[0].AdjacentCanonical == nil || neighbors.Neighbors[0].AdjacentCanonical.RecordRef.ID != admission.CanonicalRef {
		t.Fatalf("canonical neighbors response = %+v", neighbors)
	}
	relation := stdioCallTool[evidencequerymcp.RelationProvenanceResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetRelationProvenance, map[string]any{
		"canonical_edge_id": admission.CanonicalEdgeIDs[0],
	})
	if relation.Surface != "canonical_evidence" || relation.CanonicalEdge == nil || relation.OriginRecord.RecordRef.ID != proposal.ProposalOccurrenceID {
		t.Fatalf("canonical relation provenance response = %+v", relation)
	}
	openedView := stdioCallTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, ctx, queryMCP, evidencequerymcp.ToolOpenCanonicalReadView, map[string]any{
		"root_node_ids": []string{admission.RawEvidenceNodeIDs[0]},
		"relations":     []string{"supports_claim"},
		"max_depth":     1,
		"max_nodes":     8,
		"max_edges":     8,
	})
	if openedView.View.Handle == "" || openedView.View.NodeCount != 2 || openedView.View.EdgeCount != 1 || openedView.View.Truncated {
		t.Fatalf("canonical read view response = %+v", openedView)
	}
	path := stdioCallTool[evidencequerymcp.FindCanonicalPathResponse](t, ctx, queryMCP, evidencequerymcp.ToolFindCanonicalPath, map[string]any{
		"handle":       openedView.View.Handle,
		"from_node_id": admission.RawEvidenceNodeIDs[0],
		"to_node_id":   admission.CanonicalRef,
		"relations":    []string{"supports_claim"},
	})
	if !path.Witness.Found || len(path.Witness.EdgeIDs) != 1 || path.Witness.EdgeIDs[0] != admission.CanonicalEdgeIDs[0] {
		t.Fatalf("canonical path response = %+v", path)
	}
	diagnostics := stdioCallTool[evidencequerymcp.GetCanonicalTopologyDiagnosticsResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetCanonicalTopologyDiagnostics, map[string]any{
		"handle": openedView.View.Handle,
	})
	if diagnostics.Diagnostics.DerivedFromCycle != nil || diagnostics.Diagnostics.SupersedesCycle != nil || len(diagnostics.ConflictClusters) != 0 {
		t.Fatalf("canonical topology diagnostics response = %+v", diagnostics)
	}

	contradictionSource := stdioCallTool[evidenceingestionmcp.SubmitTextSourceResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitTextSource, map[string]any{
		"request_id":      "stdio-contradiction-source-intake",
		"source_id":       "fixture-refund-policy-contradiction",
		"source_version":  "v1",
		"raw_text":        string(raw),
		"origin_metadata": map[string]string{"fixture": "manual_refund_policy"},
	})
	contradictionEndpointProposal := stdioCallTool[evidenceingestionmcp.RunLocalOllamaExtractorResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, map[string]any{
		"request_id":         "stdio-local-ollama-contradiction-endpoint",
		"extraction_view_id": contradictionSource.ExtractionViewID,
		"model":              "fixture-model",
		"base_url":           ollama.URL,
		"num_predict":        256,
	})
	contradictionEndpoint := stdioCallTool[evidenceingestionmcp.AdmitPendingProposalResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolAdmitPendingProposal, map[string]any{
		"proposal_occurrence_id": contradictionEndpointProposal.ProposalOccurrenceID,
		"decision_by":            "stdio-integration-test",
		"decision_reason":        "create second grounded claim for relation admission",
	})
	if contradictionEndpoint.CanonicalRef == admission.CanonicalRef {
		t.Fatalf("contradiction endpoint reused canonical claim %s", admission.CanonicalRef)
	}
	contradictionProposal := stdioCallTool[evidenceingestionmcp.SubmitCanonicalContradictionProposalResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitCanonicalContradictionProposal, map[string]any{
		"request_id":           "stdio-canonical-contradiction",
		"node_a_id":            contradictionEndpoint.CanonicalRef,
		"node_b_id":            admission.CanonicalRef,
		"rationale":            "the two grounded claims are mutually incompatible in this reviewed scope",
		"producer_name":        "stdio-agent",
		"producer_version":     "workflow-v1",
		"producer_session_ref": "session:stdio-contradiction",
	})
	if contradictionProposal.AdmissionOutcome != "pending" || contradictionProposal.Relation != "contradicts" || contradictionProposal.NodeAID >= contradictionProposal.NodeBID {
		t.Fatalf("contradiction proposal = %+v", contradictionProposal)
	}
	reviewCard := stdioCallTool[evidencequerymcp.CanonicalContradictionProposalResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetCanonicalContradictionProposal, map[string]any{
		"canonical_contradiction_proposal_id": contradictionProposal.CanonicalContradictionProposalID,
	})
	if reviewCard.Proposal.AdmissionOutcome != "pending" || reviewCard.Decision != nil || len(reviewCard.NodeA.SourceRefs) == 0 || len(reviewCard.NodeB.SourceRefs) == 0 {
		t.Fatalf("pending contradiction review card = %+v", reviewCard)
	}
	contradictionAdmission := stdioCallTool[evidenceingestionmcp.CanonicalContradictionDecisionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolAdmitPendingCanonicalContradiction, map[string]any{
		"canonical_contradiction_proposal_id": contradictionProposal.CanonicalContradictionProposalID,
		"decision_by":                         "stdio-integration-test",
		"decision_reason":                     "reviewed both grounded claims and accepted the conflict",
	})
	if contradictionAdmission.AdmissionOutcome != "admitted" || contradictionAdmission.CanonicalEdgeID == "" {
		t.Fatalf("contradiction admission = %+v", contradictionAdmission)
	}
	contradictionRelation := stdioCallTool[evidencequerymcp.RelationProvenanceResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetRelationProvenance, map[string]any{
		"canonical_edge_id": contradictionAdmission.CanonicalEdgeID,
	})
	if contradictionRelation.RelationKind != "contradicts" || contradictionRelation.OriginRecord != nil || contradictionRelation.OriginContradictionProposal == nil {
		t.Fatalf("contradiction relation provenance = %+v", contradictionRelation)
	}
	contradictionView := stdioCallTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, ctx, queryMCP, evidencequerymcp.ToolOpenCanonicalReadView, map[string]any{
		"root_node_ids": []string{admission.CanonicalRef, contradictionEndpoint.CanonicalRef},
		"relations":     []string{"contradicts"},
		"max_depth":     1,
		"max_nodes":     4,
		"max_edges":     4,
	})
	reverseContradictionPath := stdioCallTool[evidencequerymcp.FindCanonicalPathResponse](t, ctx, queryMCP, evidencequerymcp.ToolFindCanonicalPath, map[string]any{
		"handle":       contradictionView.View.Handle,
		"from_node_id": contradictionProposal.NodeBID,
		"to_node_id":   contradictionProposal.NodeAID,
		"relations":    []string{"contradicts"},
	})
	if !reverseContradictionPath.Witness.Found ||
		len(reverseContradictionPath.Witness.EdgeIDs) != 1 ||
		reverseContradictionPath.Witness.EdgeIDs[0] != contradictionAdmission.CanonicalEdgeID {
		t.Fatalf("reverse contradiction path = %+v", reverseContradictionPath)
	}
	contradictionDiagnostics := stdioCallTool[evidencequerymcp.GetCanonicalTopologyDiagnosticsResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetCanonicalTopologyDiagnostics, map[string]any{
		"handle": contradictionView.View.Handle,
	})
	if len(contradictionDiagnostics.ConflictClusters) != 1 || len(contradictionDiagnostics.ConflictClusters[0].NodeIDs) != 2 {
		t.Fatalf("contradiction topology diagnostics = %+v", contradictionDiagnostics)
	}

	externalEnvelope := func(requestID, revision, content, observedAt string) map[string]any {
		return map[string]any{
			"schema_version":   evidenceingestion.ExternalSourceEnvelopeSchemaV1,
			"request_id":       requestID,
			"source_system":    "jira",
			"source_namespace": "stdio-site",
			"object_type":      "issue",
			"object_id":        "AHE-42",
			"revision":         revision,
			"source_location":  "https://jira.example.invalid/browse/AHE-42",
			"content_format":   evidenceingestion.ExternalSourceContentFormatPlainText,
			"content_fidelity": evidenceingestion.ExternalSourceContentFidelityVerbatim,
			"content":          content,
			"coverage":         evidenceingestion.ExternalSourceCoverageFullDocument,
			"limitations":      []string{},
			"collector_id":     "stdio-agent",
			"connector_id":     "stdio-jira-connector",
			"observed_at":      observedAt,
		}
	}
	extractorOutput := func(statement string) map[string]any {
		return map[string]any{
			"proposals": []map[string]any{{
				"proposal_local_id": "refund-window",
				"statement_text":    statement,
				"evidence_refs":     []string{"span:S1"},
			}},
		}
	}

	oldStatement := "Refund requests are accepted for 60 days."
	oldSource := stdioCallTool[evidenceingestionmcp.SubmitExternalSourceResponse](
		t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitExternalSource,
		externalEnvelope("stdio-supersession-source-v1", "1", oldStatement, "2026-08-28T01:00:00Z"),
	)
	oldProposal := stdioCallTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitExtractorOutput, map[string]any{
		"request_id":         "stdio-supersession-extractor-v1",
		"source_snapshot_id": oldSource.SourceSnapshotID,
		"extraction_view_id": oldSource.ExtractionViewID,
		"extractor_definition": map[string]any{
			"name":    "stdio-agent",
			"version": "supersession-workflow-v2",
			"config":  map[string]string{},
		},
		"extractor_output": extractorOutput(oldStatement),
	})
	oldAdmission := stdioCallTool[evidenceingestionmcp.AdmitPendingProposalResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolAdmitPendingProposal, map[string]any{
		"proposal_occurrence_id": oldProposal.ProposalOccurrenceID,
		"decision_by":            "stdio-integration-test",
		"decision_reason":        "reviewed the grounded historical external claim",
	})

	newStatement := "Refund requests are accepted for 30 days."
	newSource := stdioCallTool[evidenceingestionmcp.SubmitExternalSourceResponse](
		t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitExternalSource,
		externalEnvelope("stdio-supersession-source-v2", "2", newStatement, "2026-08-28T02:00:00Z"),
	)
	newProposal := stdioCallTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitExtractorOutput, map[string]any{
		"request_id":         "stdio-supersession-extractor-v2",
		"source_snapshot_id": newSource.SourceSnapshotID,
		"extraction_view_id": newSource.ExtractionViewID,
		"extractor_definition": map[string]any{
			"name":    "stdio-agent",
			"version": "supersession-workflow-v2",
			"config":  map[string]string{},
		},
		"extractor_output": extractorOutput(newStatement),
	})
	newReview := stdioCallTool[evidencequerymcp.GetEvidenceRecordResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetEvidenceRecord, map[string]any{
		"proposal_occurrence_id": newProposal.ProposalOccurrenceID,
	})
	oldReview := stdioCallTool[evidencequerymcp.GetEvidenceRecordResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetEvidenceRecord, map[string]any{
		"canonical_id": oldAdmission.CanonicalRef,
	})
	if newReview.AdmissionOutcome != "pending" || newReview.StatementText != newStatement || len(newReview.SourceRefs) != 1 ||
		oldReview.AdmissionOutcome != "admitted" || oldReview.Canonical == nil || oldReview.Canonical.Payload.Claim != oldStatement {
		t.Fatalf("supersession review material = new %+v old %+v", newReview, oldReview)
	}

	supersessionHead := stdioCallTool[evidencequerymcp.CanonicalSupersessionHeadResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetCanonicalSupersessionHead, map[string]any{})
	if supersessionHead.Revision != 0 || supersessionHead.HeadEventID != "" || supersessionHead.ChainKey == "" {
		t.Fatalf("initial supersession head = %+v", supersessionHead)
	}
	supersessionInput := map[string]any{
		"proposal_occurrence_id": newProposal.ProposalOccurrenceID,
		"decision_by":            "stdio-integration-test",
		"decision_reason":        "reviewed both exact source revisions, the 60-to-30-day difference, full coverage, empty limitations, and the complete replacement target set",
		"basis": map[string]any{
			"source_system":    "jira",
			"source_namespace": "stdio-site",
			"object_type":      "issue",
			"object_id":        "AHE-42",
			"slot_kind":        "policy_field",
			"slot_id":          "refund_window",
		},
		"target_node_ids":        []string{oldAdmission.CanonicalRef},
		"expected_revision":      supersessionHead.Revision,
		"expected_head_event_id": supersessionHead.HeadEventID,
	}
	supersessionAdmission := stdioCallTool[evidenceingestionmcp.AdmitPendingSupersessionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolAdmitPendingSupersession, supersessionInput)
	if supersessionAdmission.AdmissionOutcome != "admitted" || supersessionAdmission.CanonicalRef == "" ||
		len(supersessionAdmission.SupersedesEdgeIDs) != 1 || supersessionAdmission.EventRevision != 1 ||
		supersessionAdmission.AdmissionEventID == "" || supersessionAdmission.LineageKey == "" {
		t.Fatalf("supersession admission = %+v", supersessionAdmission)
	}
	supersessionReplay := stdioCallTool[evidenceingestionmcp.AdmitPendingSupersessionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolAdmitPendingSupersession, supersessionInput)
	if !supersessionReplay.Replayed || supersessionReplay.AdmissionEventID != supersessionAdmission.AdmissionEventID ||
		supersessionReplay.CanonicalRef != supersessionAdmission.CanonicalRef {
		t.Fatalf("supersession replay = %+v, want %+v", supersessionReplay, supersessionAdmission)
	}

	currentness := stdioCallTool[evidencequerymcp.CanonicalSupersessionCurrentnessResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetCanonicalSupersessionCurrentness, map[string]any{
		"lineage_key": supersessionAdmission.LineageKey,
	})
	if !currentness.Projection.ClosureAvailable || currentness.Projection.Witness == nil ||
		len(currentness.Projection.FrontierNodeIDs) != 1 || currentness.Projection.FrontierNodeIDs[0] != supersessionAdmission.CanonicalRef ||
		len(currentness.Projection.Nodes) != 2 || len(currentness.Limitations) != 1 {
		t.Fatalf("supersession currentness = %+v", currentness)
	}
	statuses := make(map[string]string, len(currentness.Projection.Nodes))
	for _, node := range currentness.Projection.Nodes {
		statuses[node.NodeID] = string(node.Status)
	}
	if statuses[supersessionAdmission.CanonicalRef] != "current" || statuses[oldAdmission.CanonicalRef] != "superseded" {
		t.Fatalf("supersession statuses = %+v", statuses)
	}

	supersessionRelation := stdioCallTool[evidencequerymcp.RelationProvenanceResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetRelationProvenance, map[string]any{
		"canonical_edge_id": supersessionAdmission.SupersedesEdgeIDs[0],
	})
	if supersessionRelation.RelationKind != "supersedes" || supersessionRelation.OriginRecord == nil ||
		supersessionRelation.OriginRecord.RecordRef.ID != newProposal.ProposalOccurrenceID || supersessionRelation.OriginContradictionProposal != nil {
		t.Fatalf("supersession relation provenance = %+v", supersessionRelation)
	}
	supersessionView := stdioCallTool[evidencequerymcp.OpenCanonicalReadViewResponse](t, ctx, queryMCP, evidencequerymcp.ToolOpenCanonicalReadView, map[string]any{
		"root_node_ids": []string{supersessionAdmission.CanonicalRef},
		"relations":     []string{"supersedes"},
		"max_depth":     1,
		"max_nodes":     4,
		"max_edges":     4,
	})
	supersessionPath := stdioCallTool[evidencequerymcp.FindCanonicalPathResponse](t, ctx, queryMCP, evidencequerymcp.ToolFindCanonicalPath, map[string]any{
		"handle":       supersessionView.View.Handle,
		"from_node_id": supersessionAdmission.CanonicalRef,
		"to_node_id":   oldAdmission.CanonicalRef,
		"relations":    []string{"supersedes"},
	})
	if !supersessionPath.Witness.Found || len(supersessionPath.Witness.EdgeIDs) != 1 || supersessionPath.Witness.EdgeIDs[0] != supersessionAdmission.SupersedesEdgeIDs[0] {
		t.Fatalf("supersession path = %+v", supersessionPath)
	}
	supersessionDiagnostics := stdioCallTool[evidencequerymcp.GetCanonicalTopologyDiagnosticsResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetCanonicalTopologyDiagnostics, map[string]any{
		"handle": supersessionView.View.Handle,
	})
	if supersessionDiagnostics.Diagnostics.SupersedesCycle != nil {
		t.Fatalf("supersession topology diagnostics = %+v", supersessionDiagnostics)
	}

	dispositionSource := stdioCallTool[evidenceingestionmcp.SubmitTextSourceResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitTextSource, map[string]any{
		"request_id":      "stdio-disposition-source-intake",
		"source_id":       "fixture-refund-policy-disposition",
		"source_version":  "v1",
		"raw_text":        string(raw),
		"origin_metadata": map[string]string{"fixture": "manual_refund_policy"},
	})
	rejectedProposal := stdioCallTool[evidenceingestionmcp.RunLocalOllamaExtractorResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, map[string]any{
		"request_id":         "stdio-local-ollama-rejected",
		"extraction_view_id": dispositionSource.ExtractionViewID,
		"model":              "fixture-model",
		"base_url":           ollama.URL,
		"num_predict":        256,
	})
	disposition := stdioCallTool[evidenceingestionmcp.RecordPendingProposalDispositionResponse](
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		map[string]any{
			"proposal_occurrence_id": rejectedProposal.ProposalOccurrenceID,
			"outcome":                evidenceingestion.ProposalDispositionRejected,
			"decision_by":            "stdio-integration-test",
			"decision_reason":        "fixture retained outside canonical evidence",
		},
	)
	if disposition.AdmissionOutcome != evidenceingestion.ProposalDispositionRejected ||
		disposition.AdmissionDecisionID == "" ||
		disposition.DecisionBy != "stdio-integration-test" ||
		disposition.DecisionReason != "fixture retained outside canonical evidence" ||
		disposition.Replayed {
		t.Fatalf("proposal disposition = %+v", disposition)
	}
	rejectedRecord := stdioCallTool[evidencequerymcp.GetEvidenceRecordResponse](t, ctx, queryMCP, evidencequerymcp.ToolGetEvidenceRecord, map[string]any{
		"proposal_occurrence_id": rejectedProposal.ProposalOccurrenceID,
	})
	if rejectedRecord.AdmissionOutcome != evidenceingestion.ProposalDispositionRejected ||
		rejectedRecord.CanonicalRef != nil {
		t.Fatalf("rejected proposal query record = %+v", rejectedRecord)
	}
	replayedDisposition := stdioCallTool[evidenceingestionmcp.RecordPendingProposalDispositionResponse](
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		map[string]any{
			"proposal_occurrence_id": rejectedProposal.ProposalOccurrenceID,
			"outcome":                evidenceingestion.ProposalDispositionRejected,
			"decision_by":            "stdio-integration-test",
			"decision_reason":        "fixture retained outside canonical evidence",
		},
	)
	if !replayedDisposition.Replayed ||
		replayedDisposition.AdmissionDecisionID != disposition.AdmissionDecisionID ||
		replayedDisposition.DecisionBy != disposition.DecisionBy ||
		replayedDisposition.DecisionReason != disposition.DecisionReason {
		t.Fatalf("replayed proposal disposition = %+v, want %+v", replayedDisposition, disposition)
	}
	conflictingDispositionErr := stdioCallToolError(
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolRecordPendingProposalDisposition,
		map[string]any{
			"proposal_occurrence_id": rejectedProposal.ProposalOccurrenceID,
			"outcome":                evidenceingestion.ProposalDispositionRejected,
			"decision_by":            "stdio-integration-test-replay",
			"decision_reason":        "must not replace the first decision",
		},
	)
	if conflictingDispositionErr.Code != string(evidenceingestion.ErrorAdmissionStateConflict) {
		t.Fatalf("conflicting disposition error = %+v", conflictingDispositionErr)
	}

	stdioAssertTableCount(t, ctx, pool, "source_snapshots", 5)
	stdioAssertTableCount(t, ctx, pool, "extraction_attempts", 5)
	stdioAssertTableCount(t, ctx, pool, "proposal_occurrences", 5)
	stdioAssertTableCount(t, ctx, pool, "admission_decisions", 5)
	stdioAssertTableCount(t, ctx, pool, "canonical_contradiction_proposals", 1)
	stdioAssertTableCount(t, ctx, pool, "canonical_contradiction_admission_decisions", 1)
	stdioAssertTableCount(t, ctx, pool, "canonical_supersession_lineages", 1)
	stdioAssertTableCount(t, ctx, pool, "canonical_supersession_admission_events", 1)
	stdioAssertTableCount(t, ctx, pool, "canonical_supersession_admission_head", 1)
	stdioAssertTableCount(t, ctx, pool, "canonical_supersession_members", 2)
	stdioAssertTableCount(t, ctx, pool, "canonical_supersession_replacement_targets", 1)
	stdioAssertTableCount(t, ctx, pool, "canonical_graph_nodes", 8)
	stdioAssertTableCount(t, ctx, pool, "canonical_graph_edges", 6)

	retriedThirdWork := stdioCallTool[evidenceingestionmcp.RetryFailedGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRetryFailedGitRepositoryExtractionWork, map[string]any{
		"request_id":     "stdio-clean-work-third-retry",
		"work_item_id":   scheduledWork.Work.WorkItemID,
		"claim_id":       thirdClaim.ClaimID,
		"retry_actor_id": "stdio-controller",
	})
	if retriedThirdWork.Replayed || retriedThirdWork.AttemptNumber != 3 || retriedThirdWork.Disposition != evidenceingestion.RepositoryExtractionWorkRetryDispositionRequeued {
		t.Fatalf("retried third work = %+v, third finish = %+v", retriedThirdWork, thirdFinishedWork)
	}
	workerTick := stdioCallTool[evidenceingestionmcp.RunGitRepositoryExtractionWorkerTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunGitRepositoryExtractionWorkerTick, map[string]any{
		"request_id":                  "stdio-clean-work-worker-tick",
		"workspace_root":              gitRoot,
		"repo_id":                     "stdio-change-token",
		"extractor_name":              evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"worker_id":                   "stdio-worker-4",
		"lease_duration_milliseconds": 60000,
	})
	fourthClaim := workerTick.Claim
	if !fourthClaim.Claimed || fourthClaim.Work == nil || *fourthClaim.Work != scheduledWork.Work || fourthClaim.AttemptNumber != 4 || fourthClaim.ClaimID == thirdClaim.ClaimID {
		t.Fatalf("fourth clean work claim = %+v, third claim = %+v", fourthClaim, thirdClaim)
	}
	if workerTick.Replayed || workerTick.Execution == nil {
		t.Fatalf("first clean work worker tick = %+v", workerTick)
	}
	executedWork := *workerTick.Execution
	if executedWork.Replayed || executedWork.Work != scheduledWork.Work || executedWork.ClaimID != fourthClaim.ClaimID || executedWork.Extraction.SourceGeneration.ID == "" || executedWork.Finish.Outcome != evidenceingestion.RepositoryExtractionWorkOutcomeSucceeded || executedWork.Finish.SourceGenerationID != executedWork.Extraction.SourceGeneration.ID || executedWork.Finish.AttemptNumber != 4 {
		t.Fatalf("executed claimed work = %+v, fourth claim = %+v", executedWork, fourthClaim)
	}
	executedWorkReplay := stdioCallTool[evidenceingestionmcp.ExecuteClaimedGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork, map[string]any{
		"request_id":     executedWork.RequestID,
		"workspace_root": gitRoot,
		"work_item_id":   scheduledWork.Work.WorkItemID,
		"claim_id":       fourthClaim.ClaimID,
		"worker_id":      fourthClaim.WorkerID,
	})
	if !executedWorkReplay.Replayed || !executedWorkReplay.Extraction.Replayed || !executedWorkReplay.Finish.Replayed || executedWorkReplay.Extraction.SourceGeneration.ID != executedWork.Extraction.SourceGeneration.ID || !executedWorkReplay.Finish.FinishedAt.Equal(executedWork.Finish.FinishedAt) {
		t.Fatalf("replayed claimed work execution = %+v, first = %+v", executedWorkReplay, executedWork)
	}
	workerTickReplay := stdioCallTool[evidenceingestionmcp.RunGitRepositoryExtractionWorkerTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunGitRepositoryExtractionWorkerTick, map[string]any{
		"request_id":                  "stdio-clean-work-worker-tick",
		"workspace_root":              gitRoot,
		"repo_id":                     "stdio-change-token",
		"extractor_name":              evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		"worker_id":                   "stdio-worker-4",
		"lease_duration_milliseconds": 60000,
	})
	if !workerTickReplay.Replayed || !workerTickReplay.Claim.Replayed || workerTickReplay.Execution == nil || !workerTickReplay.Execution.Replayed || workerTickReplay.Claim.ClaimID != fourthClaim.ClaimID || workerTickReplay.Execution.Extraction.SourceGeneration.ID != executedWork.Extraction.SourceGeneration.ID {
		t.Fatalf("replayed clean work worker tick = %+v, first = %+v", workerTickReplay, workerTick)
	}
	generation := executedWork.Extraction.SourceGeneration
	generationDiscovery := stdioCallTool[evidenceingestionmcp.ListRepositorySourceGenerationsResponse](
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolListRepositorySourceGenerations,
		map[string]any{
			"repo_id":        generation.RepoID,
			"extractor_name": generation.ExtractorName,
			"limit":          10,
		},
	)
	if generationDiscovery.Count != 1 ||
		generationDiscovery.Limit != 10 ||
		len(generationDiscovery.Generations) != 1 ||
		generationDiscovery.Generations[0].RepositorySourceGeneration != generation ||
		generationDiscovery.Generations[0].Active {
		t.Fatalf("repository generation discovery = %+v, generation = %+v", generationDiscovery, generation)
	}
	activation := stdioCallTool[evidenceingestionmcp.ActivateRepositorySourceGenerationResponse](
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolActivateRepositorySourceGeneration,
		map[string]any{
			"request_id":           "stdio-activate-repository-generation",
			"source_generation_id": generation.ID,
		},
	)
	if activation.Replayed ||
		!activation.Changed ||
		activation.ActivatedGenerationID != generation.ID ||
		activation.RepoID != generation.RepoID ||
		activation.ExtractorName != generation.ExtractorName ||
		activation.PreviousGenerationID != "" ||
		activation.Reconciliation.SourceGenerationID != generation.ID ||
		activation.Reconciliation.NewCount != generation.ProposalCount ||
		activation.Reconciliation.UnchangedCount != 0 ||
		activation.Reconciliation.StaleCount != 0 {
		t.Fatalf("repository generation activation = %+v, generation = %+v", activation, generation)
	}
	activationReplay := stdioCallTool[evidenceingestionmcp.ActivateRepositorySourceGenerationResponse](
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolActivateRepositorySourceGeneration,
		map[string]any{
			"request_id":           "stdio-activate-repository-generation",
			"source_generation_id": generation.ID,
		},
	)
	if !activationReplay.Replayed ||
		activationReplay.ActivatedGenerationID != activation.ActivatedGenerationID ||
		activationReplay.Changed != activation.Changed ||
		activationReplay.Reconciliation != activation.Reconciliation {
		t.Fatalf("repository generation activation replay = %+v, first = %+v", activationReplay, activation)
	}
	activatedGenerationDiscovery := stdioCallTool[evidenceingestionmcp.ListRepositorySourceGenerationsResponse](
		t,
		ctx,
		ingestMCP,
		evidenceingestionmcp.ToolListRepositorySourceGenerations,
		map[string]any{
			"repo_id":        generation.RepoID,
			"extractor_name": generation.ExtractorName,
			"limit":          10,
		},
	)
	if activatedGenerationDiscovery.Count != 1 ||
		len(activatedGenerationDiscovery.Generations) != 1 ||
		!activatedGenerationDiscovery.Generations[0].Active ||
		activatedGenerationDiscovery.Generations[0].RepositorySourceGeneration != generation {
		t.Fatalf("activated repository generation discovery = %+v, generation = %+v", activatedGenerationDiscovery, generation)
	}
	activeGenerationRecords := stdioCallTool[evidencequerymcp.ListEvidenceRecordsResponse](
		t,
		ctx,
		queryMCP,
		evidencequerymcp.ToolListEvidenceRecords,
		map[string]any{
			"source_generation_id": generation.ID,
			"lifecycle_scope":      evidenceingestion.ProposalLifecycleScopeActive,
			"limit":                100,
		},
	)
	if activeGenerationRecords.Count != generation.ProposalCount ||
		len(activeGenerationRecords.Records) != generation.ProposalCount {
		t.Fatalf("active generation records = %+v, generation = %+v", activeGenerationRecords, generation)
	}
	for _, record := range activeGenerationRecords.Records {
		if record.SourceGeneration == nil ||
			record.SourceGeneration.ID != generation.ID ||
			!record.SourceGeneration.Active {
			t.Fatalf("active generation record = %+v, generation = %+v", record, generation)
		}
	}
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_worker_tick_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 4)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 4)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 3)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_snapshots", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_source_generations", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_source_heads", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_generation_activation_requests", 1)
}

func stdioBriefHasCanonicalFollowUp(followUps []evidencequerymcp.GroundedEvidenceBriefFollowUp, canonicalID string) bool {
	for _, followUp := range followUps {
		if followUp.Tool == evidencequerymcp.ToolGetEvidenceRecord && followUp.CanonicalID == canonicalID {
			return true
		}
	}
	return false
}

func TestIntegrationStdioMCPRepairsExpiredExecutionOwner(t *testing.T) {
	ctx, pool := stdioIntegrationPool(t)
	ingestCore, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	ingestMCP := stdioServer(t, "ahe-ingest-execution-repair-test", mcpadmin.NewBackend(ingestCore))
	assertStdioInitialized(t, ctx, ingestMCP, "ahe-ingest-execution-repair-test")
	assertStdioToolListed(t, ctx, ingestMCP, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkExecutions)
	assertStdioToolListed(t, ctx, ingestMCP, evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution)
	assertStdioToolListed(t, ctx, ingestMCP, evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick)

	gitRoot := t.TempDir()
	stdioWriteFile(t, filepath.Join(gitRoot, "main.go"), []byte("package main\n"))
	stdioRunGit(t, gitRoot, "init", "--quiet")
	stdioRunGit(t, gitRoot, "config", "user.name", "AHE Test")
	stdioRunGit(t, gitRoot, "config", "user.email", "ahe-test@example.com")
	stdioRunGit(t, gitRoot, "add", ".")
	stdioRunGit(t, gitRoot, "commit", "--quiet", "-m", "initial")

	firstObservation := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-execution-repair-observation-first",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-execution-repair",
		"stability_window_milliseconds": 1,
	})
	if !firstObservation.Changed || firstObservation.Stable || firstObservation.Inspection.Dirty {
		t.Fatalf("first execution repair observation = %+v", firstObservation)
	}
	time.Sleep(10 * time.Millisecond)
	stableObservation := stdioCallTool[evidenceingestionmcp.ObserveGitRepositoryChangeResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolObserveGitRepositoryChange, map[string]any{
		"request_id":                    "stdio-execution-repair-observation-stable",
		"workspace_root":                gitRoot,
		"repo_id":                       "stdio-execution-repair",
		"stability_window_milliseconds": 1,
	})
	if !stableObservation.Stable || !stableObservation.Coalesced || stableObservation.ObservationID != firstObservation.ObservationID {
		t.Fatalf("stable execution repair observation = %+v, first = %+v", stableObservation, firstObservation)
	}
	scheduled := stdioCallTool[evidenceingestionmcp.ScheduleGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork, map[string]any{
		"request_id":             "stdio-execution-repair-schedule",
		"observation_request_id": stableObservation.RequestID,
		"extractor_name":         evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	})
	claim := stdioCallTool[evidenceingestionmcp.ClaimGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, map[string]any{
		"request_id":                  "stdio-execution-repair-claim",
		"repo_id":                     scheduled.Work.RepoID,
		"extractor_name":              scheduled.Work.ExtractorName,
		"worker_id":                   "stdio-execution-repair-worker",
		"lease_duration_milliseconds": 1000,
	})
	if !claim.Claimed || claim.Work == nil || *claim.Work != scheduled.Work || claim.AttemptNumber != 1 {
		t.Fatalf("execution repair claim = %+v, scheduled = %+v", claim, scheduled)
	}

	executionRequestID := "stdio-execution-repair-execute"
	invalidWorkspaceRoot := t.TempDir()
	executionErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork, map[string]any{
		"request_id":     executionRequestID,
		"workspace_root": invalidWorkspaceRoot,
		"work_item_id":   scheduled.Work.WorkItemID,
		"claim_id":       claim.ClaimID,
		"worker_id":      claim.WorkerID,
	})
	if executionErr.Code == "" {
		t.Fatalf("failed execution returned no tool error: %+v", executionErr)
	}
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 0)

	repairInput := map[string]any{
		"request_id":           "stdio-execution-repair",
		"execution_request_id": executionRequestID,
		"work_item_id":         scheduled.Work.WorkItemID,
		"claim_id":             claim.ClaimID,
		"worker_id":            claim.WorkerID,
		"repair_actor_id":      "stdio-repair-controller",
	}
	prematureRepairErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution, repairInput)
	if prematureRepairErr.Code != string(evidenceingestion.ErrorRepositoryWorkConflict) {
		t.Fatalf("premature execution repair error = %+v, want %s", prematureRepairErr, evidenceingestion.ErrorRepositoryWorkConflict)
	}
	callerTimeInput := make(map[string]any, len(repairInput)+1)
	for key, value := range repairInput {
		callerTimeInput[key] = value
	}
	callerTimeInput["repaired_at"] = time.Now().UTC()
	strictErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution, callerTimeInput)
	if strictErr.Code == "" {
		t.Fatalf("execution repair accepted caller-owned time: %+v", strictErr)
	}

	var currentLeaseExpiresAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT lease_expires_at
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, claim.ClaimID).Scan(&currentLeaseExpiresAt); err != nil {
		t.Fatalf("read execution repair lease: %v", err)
	}
	if wait := time.Until(currentLeaseExpiresAt); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	discovered := stdioCallTool[evidenceingestionmcp.ListExpiredGitRepositoryExtractionWorkExecutionsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkExecutions, map[string]any{
		"limit": 10,
	})
	if len(discovered) != 1 || discovered[0].ExecutionRequestID != executionRequestID || discovered[0].Work != scheduled.Work || discovered[0].ClaimID != claim.ClaimID || discovered[0].AttemptNumber != 1 || discovered[0].WorkerID != claim.WorkerID || !discovered[0].LeaseExpiresAt.Equal(currentLeaseExpiresAt) || discovered[0].HeartbeatLeaseDurationMilliseconds != 1000 {
		t.Fatalf("discovered stdio execution owner = %+v, claim = %+v", discovered, claim)
	}
	repaired := stdioCallTool[evidenceingestionmcp.RepairExpiredGitRepositoryExtractionWorkExecutionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution, repairInput)
	if repaired.Replayed || repaired.ExecutionRequestID != executionRequestID || repaired.Work != scheduled.Work || repaired.ClaimID != claim.ClaimID || repaired.AttemptNumber != 1 || repaired.WorkerID != claim.WorkerID || repaired.RepairReason != evidenceingestion.RepositoryExtractionWorkExecutionRepairReasonLeaseExpired || repaired.Disposition != evidenceingestion.RepositoryExtractionWorkRecoveryDispositionRequeued || repaired.RepairedAt.Before(repaired.LeaseExpiresAt) {
		t.Fatalf("repaired stdio execution = %+v, claim = %+v", repaired, claim)
	}
	replay := stdioCallTool[evidenceingestionmcp.RepairExpiredGitRepositoryExtractionWorkExecutionResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution, repairInput)
	if !replay.Replayed || replay.Work != repaired.Work || !replay.RepairedAt.Equal(repaired.RepairedAt) || !replay.LeaseExpiresAt.Equal(repaired.LeaseExpiresAt) {
		t.Fatalf("stdio execution repair replay = %+v, first = %+v", replay, repaired)
	}
	discoveredAfterRepair := stdioCallTool[evidenceingestionmcp.ListExpiredGitRepositoryExtractionWorkExecutionsResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolListExpiredGitRepositoryExtractionWorkExecutions, map[string]any{
		"limit": 10,
	})
	if len(discoveredAfterRepair) != 0 {
		t.Fatalf("expired execution owners after repair = %+v, want empty", discoveredAfterRepair)
	}

	staleExecutionErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork, map[string]any{
		"request_id":     executionRequestID,
		"workspace_root": invalidWorkspaceRoot,
		"work_item_id":   scheduled.Work.WorkItemID,
		"claim_id":       claim.ClaimID,
		"worker_id":      claim.WorkerID,
	})
	if staleExecutionErr.Code != string(evidenceingestion.ErrorRepositoryWorkConflict) {
		t.Fatalf("stale execution after repair error = %+v, want %s", staleExecutionErr, evidenceingestion.ErrorRepositoryWorkConflict)
	}
	reclaimed := stdioCallTool[evidenceingestionmcp.ClaimGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, map[string]any{
		"request_id":                  "stdio-execution-repair-reclaim",
		"repo_id":                     scheduled.Work.RepoID,
		"extractor_name":              scheduled.Work.ExtractorName,
		"worker_id":                   "stdio-execution-repair-worker-2",
		"lease_duration_milliseconds": 1000,
	})
	if !reclaimed.Claimed || reclaimed.Work == nil || *reclaimed.Work != scheduled.Work || reclaimed.AttemptNumber != 2 || reclaimed.ClaimID == claim.ClaimID {
		t.Fatalf("claim after stdio execution repair = %+v, first = %+v", reclaimed, claim)
	}
	maintenanceExecutionRequestID := "stdio-expired-maintenance-execute"
	maintenanceExecutionErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork, map[string]any{
		"request_id":     maintenanceExecutionRequestID,
		"workspace_root": invalidWorkspaceRoot,
		"work_item_id":   scheduled.Work.WorkItemID,
		"claim_id":       reclaimed.ClaimID,
		"worker_id":      reclaimed.WorkerID,
	})
	if maintenanceExecutionErr.Code == "" {
		t.Fatalf("failed maintenance execution returned no tool error: %+v", maintenanceExecutionErr)
	}
	var maintenanceExecutionLeaseExpiresAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT lease_expires_at
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, reclaimed.ClaimID).Scan(&maintenanceExecutionLeaseExpiresAt); err != nil {
		t.Fatalf("read maintenance execution lease: %v", err)
	}
	if wait := time.Until(maintenanceExecutionLeaseExpiresAt); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	maintenanceInput := map[string]any{
		"request_id":           "stdio-expired-maintenance-owned",
		"limit":                10,
		"maintenance_actor_id": "stdio-maintenance-controller",
	}
	maintenance := stdioCallTool[evidenceingestionmcp.RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick, maintenanceInput)
	if maintenance.Replayed || len(maintenance.Transitions) != 1 || maintenance.Transitions[0].TransitionKind != evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair || maintenance.Transitions[0].Recovery != nil || maintenance.Transitions[0].ExecutionRepair == nil || maintenance.Transitions[0].ExecutionRepair.ExecutionRequestID != maintenanceExecutionRequestID || maintenance.Transitions[0].ExecutionRepair.ClaimID != reclaimed.ClaimID || maintenance.Transitions[0].ExecutionRepair.RepairActorID != "stdio-maintenance-controller" {
		t.Fatalf("stdio execution-owned maintenance tick = %+v", maintenance)
	}
	maintenanceReplay := stdioCallTool[evidenceingestionmcp.RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick, maintenanceInput)
	if !maintenanceReplay.Replayed || len(maintenanceReplay.Transitions) != 1 || maintenanceReplay.Transitions[0].ExecutionRepair == nil || !maintenanceReplay.Transitions[0].ExecutionRepair.Replayed || !maintenanceReplay.DiscoveredAt.Equal(maintenance.DiscoveredAt) {
		t.Fatalf("stdio execution-owned maintenance replay = %+v, first = %+v", maintenanceReplay, maintenance)
	}

	ownerlessClaim := stdioCallTool[evidenceingestionmcp.ClaimGitRepositoryExtractionWorkResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork, map[string]any{
		"request_id":                  "stdio-expired-maintenance-ownerless-claim",
		"repo_id":                     scheduled.Work.RepoID,
		"extractor_name":              scheduled.Work.ExtractorName,
		"worker_id":                   "stdio-expired-maintenance-ownerless-worker",
		"lease_duration_milliseconds": 1,
	})
	if !ownerlessClaim.Claimed || ownerlessClaim.Work == nil || ownerlessClaim.AttemptNumber != 3 {
		t.Fatalf("stdio ownerless maintenance claim = %+v", ownerlessClaim)
	}
	time.Sleep(10 * time.Millisecond)
	ownerlessMaintenance := stdioCallTool[evidenceingestionmcp.RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick, map[string]any{
		"request_id":           "stdio-expired-maintenance-ownerless",
		"limit":                10,
		"maintenance_actor_id": "stdio-maintenance-controller",
	})
	if ownerlessMaintenance.Replayed || len(ownerlessMaintenance.Transitions) != 1 || ownerlessMaintenance.Transitions[0].TransitionKind != evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery || ownerlessMaintenance.Transitions[0].Recovery == nil || ownerlessMaintenance.Transitions[0].ExecutionRepair != nil || ownerlessMaintenance.Transitions[0].Recovery.ClaimID != ownerlessClaim.ClaimID || ownerlessMaintenance.Transitions[0].Recovery.RecoveryActorID != "stdio-maintenance-controller" {
		t.Fatalf("stdio ownerless maintenance tick = %+v", ownerlessMaintenance)
	}

	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_execution_repairs", 2)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 1)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_requests", 2)
	stdioAssertTableCount(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_items", 2)
}

func TestIntegrationStdioMCPInvalidModelOutputPersistsFailedAttemptOnly(t *testing.T) {
	ctx, pool := stdioIntegrationPool(t)
	ingestCore, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	ingestMCP := stdioServer(t, "ahe-ingest-mcp-test", mcpadmin.NewBackend(ingestCore))

	raw, _ := stdioRawAndFixtureData(t)
	source := stdioCallTool[evidenceingestionmcp.SubmitTextSourceResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitTextSource, map[string]any{
		"request_id":      "stdio-invalid-source-intake",
		"source_id":       "fixture-refund-policy",
		"source_version":  "v1",
		"raw_text":        string(raw),
		"origin_metadata": map[string]string{"fixture": "manual_refund_policy"},
	})

	invalidOutput := []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"],"canonical_id":"canon-node:forbidden"}]}`)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("ollama path = %q, want /api/generate", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"response": string(invalidOutput)}); err != nil {
			t.Fatalf("encode ollama response: %v", err)
		}
	}))
	t.Cleanup(ollama.Close)

	toolErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, map[string]any{
		"request_id":         "stdio-invalid-local-ollama-run",
		"extraction_view_id": source.ExtractionViewID,
		"model":              "fixture-model",
		"base_url":           ollama.URL,
	})
	if toolErr.Code != string(evidenceingestion.ErrorInvalidExtractorOutput) {
		t.Fatalf("tool error code = %q, want %s; response=%+v", toolErr.Code, evidenceingestion.ErrorInvalidExtractorOutput, toolErr)
	}

	var status, failureClass, outputHash string
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_class, output_hash
		FROM extraction_attempts
	`).Scan(&status, &failureClass, &outputHash); err != nil {
		t.Fatalf("read failed attempt: %v", err)
	}
	if status != "failed" || failureClass != string(evidenceingestion.ErrorInvalidExtractorOutput) {
		t.Fatalf("attempt status/class = %q/%q, want failed/%s", status, failureClass, evidenceingestion.ErrorInvalidExtractorOutput)
	}
	if outputHash != stdioContentHash(invalidOutput) {
		t.Fatalf("attempt output_hash = %q, want hash of invalid model output", outputHash)
	}
	stdioAssertTableCount(t, ctx, pool, "extraction_attempts", 1)
	stdioAssertTableCount(t, ctx, pool, "proposal_batches", 0)
	stdioAssertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	stdioAssertTableCount(t, ctx, pool, "admission_decisions", 0)
	stdioAssertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	stdioAssertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func TestIntegrationStdioMCPFailedAttemptReplayAndRetry(t *testing.T) {
	ctx, pool := stdioIntegrationPool(t)
	ingestCore, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		t.Fatalf("ingest NewServer() error = %v", err)
	}
	ingestMCP := stdioServer(t, "ahe-ingest-mcp-test", mcpadmin.NewBackend(ingestCore))

	raw, fixtureData := stdioRawAndFixtureData(t)
	source := stdioCallTool[evidenceingestionmcp.SubmitTextSourceResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolSubmitTextSource, map[string]any{
		"request_id":      "stdio-retry-source-intake",
		"source_id":       "fixture-refund-policy",
		"source_version":  "v1",
		"raw_text":        string(raw),
		"origin_metadata": map[string]string{"fixture": "manual_refund_policy"},
	})

	invalidOutput := []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"],"proposal_fingerprint":"fp:forbidden"}]}`)
	ollamaCalls := 0
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("ollama path = %q, want /api/generate", r.URL.Path)
		}
		ollamaCalls++
		response := invalidOutput
		if ollamaCalls == 2 {
			response = fixtureData
		}
		if ollamaCalls > 2 {
			t.Fatalf("ollama called %d times, want at most 2", ollamaCalls)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"response": string(response)}); err != nil {
			t.Fatalf("encode ollama response: %v", err)
		}
	}))
	t.Cleanup(ollama.Close)

	requestArgs := map[string]any{
		"request_id":         "stdio-failed-replay-run",
		"extraction_view_id": source.ExtractionViewID,
		"model":              "fixture-model",
		"base_url":           ollama.URL,
	}
	toolErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, requestArgs)
	if toolErr.Code != string(evidenceingestion.ErrorInvalidExtractorOutput) {
		t.Fatalf("first tool error code = %q, want %s", toolErr.Code, evidenceingestion.ErrorInvalidExtractorOutput)
	}
	if ollamaCalls != 1 {
		t.Fatalf("ollama calls after first failure = %d, want 1", ollamaCalls)
	}
	stdioAssertTableCount(t, ctx, pool, "extraction_runs", 1)
	stdioAssertTableCount(t, ctx, pool, "extraction_attempts", 1)
	stdioAssertTableCount(t, ctx, pool, "proposal_batches", 0)
	stdioAssertTableCount(t, ctx, pool, "proposal_occurrences", 0)

	replayErr := stdioCallToolError(t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, requestArgs)
	if replayErr.Code != string(evidenceingestion.ErrorPersistedAttemptFailed) {
		t.Fatalf("replay error code = %q, want %s", replayErr.Code, evidenceingestion.ErrorPersistedAttemptFailed)
	}
	if ollamaCalls != 1 {
		t.Fatalf("ollama calls after failed replay = %d, want still 1", ollamaCalls)
	}
	stdioAssertTableCount(t, ctx, pool, "extraction_runs", 1)
	stdioAssertTableCount(t, ctx, pool, "extraction_attempts", 1)
	stdioAssertTableCount(t, ctx, pool, "proposal_batches", 0)
	stdioAssertTableCount(t, ctx, pool, "proposal_occurrences", 0)

	retryArgs := map[string]any{
		"request_id":           requestArgs["request_id"],
		"extraction_view_id":   requestArgs["extraction_view_id"],
		"model":                requestArgs["model"],
		"base_url":             requestArgs["base_url"],
		"retry_failed_attempt": true,
	}
	proposal := stdioCallTool[evidenceingestionmcp.RunLocalOllamaExtractorResponse](t, ctx, ingestMCP, evidenceingestionmcp.ToolRunLocalOllamaExtractor, retryArgs)
	if proposal.Status != "pending" {
		t.Fatalf("retry proposal status = %q, want pending", proposal.Status)
	}
	if proposal.ExtractionRunID == "" || proposal.ExtractionAttemptID == "" || proposal.ProposalOccurrenceID == "" {
		t.Fatalf("retry proposal IDs not populated: %+v", proposal)
	}
	if ollamaCalls != 2 {
		t.Fatalf("ollama calls after retry = %d, want 2", ollamaCalls)
	}
	stdioAssertTableCount(t, ctx, pool, "extraction_runs", 1)
	stdioAssertTableCount(t, ctx, pool, "extraction_attempts", 2)
	stdioAssertTableCount(t, ctx, pool, "proposal_batches", 1)
	stdioAssertTableCount(t, ctx, pool, "proposal_occurrences", 1)
	stdioAssertAttemptStatuses(t, ctx, pool, 1, 1)
}

func stdioServer(t *testing.T, name string, backend mcpstdio.Backend) *mcpstdio.Server {
	t.Helper()
	server, err := mcpstdio.NewServer(name, "test", backend)
	if err != nil {
		t.Fatalf("mcpstdio NewServer() error = %v", err)
	}
	return server
}

func assertStdioInitialized(t *testing.T, ctx context.Context, server *mcpstdio.Server, name string) {
	t.Helper()
	result := stdioRPC(t, ctx, server, "initialize", map[string]any{
		"protocolVersion": mcpstdio.ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "stdio-integration-test",
			"version": "test",
		},
	})
	var resp struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}
	if resp.ProtocolVersion != mcpstdio.ProtocolVersion || resp.ServerInfo.Name != name {
		t.Fatalf("initialize result = %+v, want protocol %s and server %s", resp, mcpstdio.ProtocolVersion, name)
	}
}

func assertStdioToolListed(t *testing.T, ctx context.Context, server *mcpstdio.Server, name string) {
	t.Helper()
	result := stdioRPC(t, ctx, server, "tools/list", nil)
	var resp struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	for _, tool := range resp.Tools {
		if tool.Name == name {
			return
		}
	}
	t.Fatalf("tools/list missing %q: %+v", name, resp.Tools)
}

func assertStdioToolNames(t *testing.T, ctx context.Context, server *mcpstdio.Server, want []string) {
	t.Helper()
	result := stdioRPC(t, ctx, server, "tools/list", nil)
	var resp struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if len(resp.Tools) != len(want) {
		t.Fatalf("tools/list count = %d, want %d: %+v", len(resp.Tools), len(want), resp.Tools)
	}
	for i, tool := range resp.Tools {
		if tool.Name != want[i] {
			t.Fatalf("tools/list[%d] = %q, want %q: %+v", i, tool.Name, want[i], resp.Tools)
		}
	}
}

func stdioCallTool[T any](t *testing.T, ctx context.Context, server *mcpstdio.Server, name string, arguments any) T {
	t.Helper()
	result := stdioRPC(t, ctx, server, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	var toolResp struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(result, &toolResp); err != nil {
		t.Fatalf("unmarshal tools/call result: %v", err)
	}
	if toolResp.IsError {
		t.Fatalf("tools/call %s returned error: %s", name, string(toolResp.StructuredContent))
	}
	if len(toolResp.StructuredContent) == 0 {
		t.Fatalf("tools/call %s returned empty structuredContent", name)
	}
	var out T
	if err := json.Unmarshal(toolResp.StructuredContent, &out); err != nil {
		t.Fatalf("unmarshal %s structuredContent: %v; payload=%s", name, err, string(toolResp.StructuredContent))
	}
	return out
}

func stdioCallToolError(t *testing.T, ctx context.Context, server *mcpstdio.Server, name string, arguments any) struct {
	Code    string `json:"code"`
	Message string `json:"message"`
} {
	t.Helper()
	result := stdioRPC(t, ctx, server, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	var toolResp struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(result, &toolResp); err != nil {
		t.Fatalf("unmarshal tools/call error result: %v", err)
	}
	if !toolResp.IsError {
		t.Fatalf("tools/call %s isError = false, want true: %s", name, string(toolResp.StructuredContent))
	}
	if len(toolResp.StructuredContent) == 0 {
		t.Fatalf("tools/call %s returned empty error structuredContent", name)
	}
	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(toolResp.StructuredContent, &out); err != nil {
		t.Fatalf("unmarshal %s error structuredContent: %v; payload=%s", name, err, string(toolResp.StructuredContent))
	}
	return out
}

func stdioRPC(t *testing.T, ctx context.Context, server *mcpstdio.Server, method string, params any) json.RawMessage {
	t.Helper()
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      "stdio-integration",
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal rpc request: %v", err)
	}
	var out bytes.Buffer
	if err := server.Serve(ctx, bytes.NewReader(append(data, '\n')), &out); err != nil {
		t.Fatalf("Serve(%s) error = %v", method, err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("Serve(%s) responses = %d, want 1: %q", method, len(lines), out.String())
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal rpc response: %v; response=%s", err, lines[0])
	}
	if resp.Error != nil {
		t.Fatalf("rpc %s error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		t.Fatalf("rpc %s returned empty result", method)
	}
	return resp.Result
}

func stdioIntegrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
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

	schema := "ahe_stdio_mcp_test_" + stdioRandomHex(t, 8)
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

	if _, err := pool.Exec(ctx, stdioReadMigrations(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return ctx, pool
}

func stdioRawAndFixtureData(t *testing.T) ([]byte, []byte) {
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
	return raw, fixtureData
}

func stdioReadMigrations(t *testing.T) string {
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
		combined.WriteByte('\n')
	}
	return combined.String()
}

func stdioAssertTableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func stdioAssertAttemptStatuses(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantFailed, wantSucceeded int) {
	t.Helper()
	var failed, succeeded int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'succeeded')
		FROM extraction_attempts
	`).Scan(&failed, &succeeded); err != nil {
		t.Fatalf("count attempt statuses: %v", err)
	}
	if failed != wantFailed || succeeded != wantSucceeded {
		t.Fatalf("attempt status counts = failed %d succeeded %d, want %d/%d", failed, succeeded, wantFailed, wantSucceeded)
	}
}

func stdioRandomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}

func stdioContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stdioWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func stdioRunGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
