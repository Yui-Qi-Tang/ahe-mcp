package evidenceingestionmcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestToolsExposeIngestAndExtractorInputTools(t *testing.T) {
	server := newServer(&fakeCore{})
	tools := server.Tools()
	if len(tools) != 35 {
		t.Fatalf("len(Tools()) = %d, want 35", len(tools))
	}
	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name] = tool.Write
	}
	for _, name := range []string{ToolSubmitManualEvidence, ToolSubmitTextSource, ToolSubmitExtractorOutput, ToolObserveGitRepositoryChange, ToolScheduleGitRepositoryExtractionWork, ToolClaimGitRepositoryExtractionWork, ToolRenewGitRepositoryExtractionWorkLease, ToolExecuteClaimedGitRepositoryExtractionWork, ToolRunGitRepositoryExtractionWorkerTick, ToolFinishGitRepositoryExtractionWork, ToolRecoverExpiredGitRepositoryExtractionWork, ToolRepairExpiredGitRepositoryExtractionWorkExecution, ToolRetryFailedGitRepositoryExtractionWork, ToolCaptureGitRepositorySnapshot, ToolCreateRepositoryExtractionRun, ToolRunRepositoryGoParserExtractor, ToolRunRepositoryGoplsExtractor, ToolActivateRepositorySourceGeneration, ToolRunLocalOllamaExtractor, ToolRunGoParserExtractor, ToolRunGoplsExtractor, ToolAdmitPendingProposal, ToolRecordPendingProposalDisposition, ToolClassifyFailedGitRepositoryExtractionWork, ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, ToolRunDueGitRepositoryExtractionWorkRetryControllerTick, ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick} {
		if !got[name] {
			t.Fatalf("tool %s missing or not write-enabled: %+v", name, tools)
		}
	}
	if got[ToolGetExtractorInput] {
		t.Fatalf("tool %s is write-enabled: %+v", ToolGetExtractorInput, tools)
	}
	if got[ToolInspectGoplsWorkspace] {
		t.Fatalf("tool %s is write-enabled: %+v", ToolInspectGoplsWorkspace, tools)
	}
	if got[ToolInspectGitRepositoryChange] {
		t.Fatalf("tool %s is write-enabled: %+v", ToolInspectGitRepositoryChange, tools)
	}
	if write, ok := got[ToolListExpiredGitRepositoryExtractionWorkClaims]; !ok || write {
		t.Fatalf("tool %s missing or write-enabled: %+v", ToolListExpiredGitRepositoryExtractionWorkClaims, tools)
	}
	if write, ok := got[ToolListExpiredGitRepositoryExtractionWorkExecutions]; !ok || write {
		t.Fatalf("tool %s missing or write-enabled: %+v", ToolListExpiredGitRepositoryExtractionWorkExecutions, tools)
	}
	if write, ok := got[ToolListDueGitRepositoryExtractionWorkRetryDecisions]; !ok || write {
		t.Fatalf("tool %s missing or write-enabled: %+v", ToolListDueGitRepositoryExtractionWorkRetryDecisions, tools)
	}
	if got[ToolGetRepositoryExtractorInput] {
		t.Fatalf("tool %s is write-enabled: %+v", ToolGetRepositoryExtractorInput, tools)
	}
	if write, ok := got[ToolListRepositorySourceGenerations]; !ok || write {
		t.Fatalf("tool %s missing or write-enabled: %+v", ToolListRepositorySourceGenerations, tools)
	}
}

func TestCallToolCapturesGitRepositorySnapshotThroughCore(t *testing.T) {
	core := &fakeCore{
		repositoryResult: evidenceingestion.RepositorySnapshotCaptureResult{
			RepositorySnapshot: evidenceingestion.RepositorySnapshot{
				ID:                         "repo-snapshot:1",
				RepoID:                     "ahe-wrap",
				CommitSHA:                  strings.Repeat("a", 40),
				ManifestHash:               "sha256:manifest",
				ManifestEntryCount:         12,
				RevisionVerificationMethod: evidenceingestion.RepositoryRevisionVerificationGitV1,
				ManifestContract:           evidenceingestion.RepositoryManifestGitTreeV1,
				FileSelectionContract:      evidenceingestion.RepositoryFileSelectionTrackedGoV1,
				SelectedFileCount:          4,
			},
		},
	}
	server := newServer(core)
	payload := []byte(`{"request_id":"repo-capture","workspace_root":"/repo","repo_id":"ahe-wrap","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)

	data, err := server.CallTool(context.Background(), ToolCaptureGitRepositorySnapshot, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp CaptureGitRepositorySnapshotResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryCalls != 1 {
		t.Fatalf("repository calls = %d, want 1", core.repositoryCalls)
	}
	if core.repositoryConfig.RequestID != "repo-capture" || core.repositoryConfig.WorkspaceRoot != "/repo" || core.repositoryConfig.GitBinaryPath != "" {
		t.Fatalf("repository config = %+v", core.repositoryConfig)
	}
	if resp.RepositorySnapshotID != "repo-snapshot:1" || resp.SelectedFileCount != 4 || resp.RevisionVerificationMethod != evidenceingestion.RepositoryRevisionVerificationGitV1 {
		t.Fatalf("repository response = %+v", resp)
	}
}

func TestCallToolInspectsGitRepositoryChangeWithoutExecutionControls(t *testing.T) {
	core := &fakeCore{
		gitChangeResult: evidenceingestion.GitRepositoryChangeInspection{
			TokenContract:            evidenceingestion.GitRepositoryChangeTokenV1,
			DirtyFingerprintContract: evidenceingestion.GitRepositoryDirtyFingerprintV1,
			RepoID:                   "ahe-wrap",
			HeadCommitSHA:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Dirty:                    true,
			DirtyFingerprint:         "sha256:dirty",
			TrackedChangeCount:       2,
			UntrackedFileCount:       1,
			ChangeToken:              "sha256:token",
		},
	}
	server := newServer(core)
	payload := []byte(`{"workspace_root":"/repo","repo_id":"ahe-wrap"}`)

	data, err := server.CallTool(context.Background(), ToolInspectGitRepositoryChange, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response InspectGitRepositoryChangeResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.gitChangeCalls != 1 || core.gitChangeConfig.WorkspaceRoot != "/repo" || core.gitChangeConfig.RepoID != "ahe-wrap" || core.gitChangeConfig.GitBinaryPath != "" || core.gitChangeConfig.Timeout != 0 {
		t.Fatalf("git change config/calls = %+v/%d", core.gitChangeConfig, core.gitChangeCalls)
	}
	if response != core.gitChangeResult {
		t.Fatalf("git change response = %+v, want %+v", response, core.gitChangeResult)
	}
}

func TestCallToolObservesGitRepositoryChangeWithoutExecutionControls(t *testing.T) {
	observedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	core := &fakeCore{
		gitObservationResult: evidenceingestion.GitRepositoryChangeObservationResult{
			RequestID:         "observe-1",
			ObservationID:     "change-observation:1",
			ObservationNumber: 1,
			Inspection: evidenceingestion.GitRepositoryChangeInspection{
				TokenContract:            evidenceingestion.GitRepositoryChangeTokenV1,
				DirtyFingerprintContract: evidenceingestion.GitRepositoryDirtyFingerprintV1,
				RepoID:                   "ahe-wrap",
				HeadCommitSHA:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ChangeToken:              "sha256:token",
			},
			FirstObservedAt:       observedAt,
			LastObservedAt:        observedAt,
			ObservationCount:      1,
			StabilityWindowMillis: 2000,
			StableAfter:           observedAt.Add(2 * time.Second),
			Changed:               true,
		},
	}
	server := newServer(core)
	payload := []byte(`{"request_id":"observe-1","workspace_root":"/repo","repo_id":"ahe-wrap","stability_window_milliseconds":2000}`)

	data, err := server.CallTool(context.Background(), ToolObserveGitRepositoryChange, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ObserveGitRepositoryChangeResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.gitObservationCalls != 1 || core.gitObservationConfig.RequestID != "observe-1" || core.gitObservationConfig.WorkspaceRoot != "/repo" || core.gitObservationConfig.RepoID != "ahe-wrap" || core.gitObservationConfig.StabilityWindow != 2*time.Second || core.gitObservationConfig.GitBinaryPath != "" || core.gitObservationConfig.Timeout != 0 {
		t.Fatalf("Git observation config/calls = %+v/%d", core.gitObservationConfig, core.gitObservationCalls)
	}
	if response != core.gitObservationResult {
		t.Fatalf("Git observation response = %+v, want %+v", response, core.gitObservationResult)
	}
}

func TestCallToolSchedulesGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:        "repo-work:1",
		RepoID:            "ahe-wrap",
		ExtractorName:     evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		ObservationID:     "change-observation:1",
		ObservationNumber: 2,
		ChangeToken:       "sha256:token",
		HeadCommitSHA:     strings.Repeat("a", 40),
	}
	core := &fakeCore{repositoryWorkScheduleResult: evidenceingestion.RepositoryExtractionWorkScheduleResult{
		RequestID:            "schedule-1",
		ObservationRequestID: "observe-2",
		Work:                 work,
		Created:              true,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolScheduleGitRepositoryExtractionWork, []byte(`{"request_id":"schedule-1","observation_request_id":"observe-2","extractor_name":"repository-go-parser-code-fact"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ScheduleGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryWorkScheduleCalls != 1 || core.repositoryWorkScheduleInput.RequestID != "schedule-1" || core.repositoryWorkScheduleInput.ObservationRequestID != "observe-2" || core.repositoryWorkScheduleInput.ExtractorName != evidenceingestion.ExtractorRepositoryGoParserCodeFact {
		t.Fatalf("schedule input/calls = %+v/%d", core.repositoryWorkScheduleInput, core.repositoryWorkScheduleCalls)
	}
	if response != core.repositoryWorkScheduleResult {
		t.Fatalf("schedule response = %+v, want %+v", response, core.repositoryWorkScheduleResult)
	}
}

func TestCallToolClaimsGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	claimedAt := time.Date(2026, 7, 21, 12, 1, 0, 0, time.UTC)
	leaseExpiresAt := claimedAt.Add(time.Minute)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoplsCodeFact,
	}
	core := &fakeCore{repositoryWorkClaimResult: evidenceingestion.RepositoryExtractionWorkClaimResult{
		RequestID:      "claim-1",
		RepoID:         "ahe-wrap",
		ExtractorName:  evidenceingestion.ExtractorRepositoryGoplsCodeFact,
		WorkerID:       "worker-1",
		Claimed:        true,
		Work:           &work,
		ClaimID:        "work-claim:1",
		AttemptNumber:  1,
		ClaimedAt:      &claimedAt,
		LeaseExpiresAt: &leaseExpiresAt,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolClaimGitRepositoryExtractionWork, []byte(`{"request_id":"claim-1","repo_id":"ahe-wrap","extractor_name":"repository-gopls-code-fact","worker_id":"worker-1","lease_duration_milliseconds":60000}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ClaimGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryWorkClaimCalls != 1 || core.repositoryWorkClaimInput.RequestID != "claim-1" || core.repositoryWorkClaimInput.RepoID != "ahe-wrap" || core.repositoryWorkClaimInput.ExtractorName != evidenceingestion.ExtractorRepositoryGoplsCodeFact || core.repositoryWorkClaimInput.WorkerID != "worker-1" || core.repositoryWorkClaimInput.LeaseDurationMilliseconds != 60000 {
		t.Fatalf("claim input/calls = %+v/%d", core.repositoryWorkClaimInput, core.repositoryWorkClaimCalls)
	}
	if response.ClaimID != core.repositoryWorkClaimResult.ClaimID || response.Work == nil || *response.Work != work || response.AttemptNumber != 1 || response.ClaimedAt == nil || !response.ClaimedAt.Equal(claimedAt) || response.LeaseExpiresAt == nil || !response.LeaseExpiresAt.Equal(leaseExpiresAt) {
		t.Fatalf("claim response = %+v, want %+v", response, core.repositoryWorkClaimResult)
	}
}

func TestCallToolRenewsGitRepositoryExtractionWorkLeaseThroughCore(t *testing.T) {
	priorLeaseExpiresAt := time.Date(2026, 7, 21, 12, 2, 0, 0, time.UTC)
	renewedAt := priorLeaseExpiresAt.Add(-30 * time.Second)
	leaseExpiresAt := renewedAt.Add(2 * time.Minute)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoplsCodeFact,
	}
	core := &fakeCore{repositoryWorkRenewResult: evidenceingestion.RepositoryExtractionWorkLeaseRenewalResult{
		RequestID:                 "renew-1",
		Work:                      work,
		ClaimID:                   "work-claim:1",
		AttemptNumber:             1,
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: 120_000,
		PriorLeaseExpiresAt:       priorLeaseExpiresAt,
		RenewedAt:                 renewedAt,
		LeaseExpiresAt:            leaseExpiresAt,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRenewGitRepositoryExtractionWorkLease, []byte(`{"request_id":"renew-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","worker_id":"worker-1","lease_duration_milliseconds":120000}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RenewGitRepositoryExtractionWorkLeaseResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	input := core.repositoryWorkRenewInput
	if core.repositoryWorkRenewCalls != 1 || input.RequestID != "renew-1" || input.WorkItemID != "repo-work:1" || input.ClaimID != "work-claim:1" || input.WorkerID != "worker-1" || input.LeaseDurationMilliseconds != 120_000 {
		t.Fatalf("lease renewal input/calls = %+v/%d", input, core.repositoryWorkRenewCalls)
	}
	if response != core.repositoryWorkRenewResult {
		t.Fatalf("lease renewal response = %+v, want %+v", response, core.repositoryWorkRenewResult)
	}
}

func TestCallToolExecutesClaimedGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		HeadCommitSHA: strings.Repeat("a", 40),
	}
	generation := evidenceingestion.RepositorySourceGeneration{
		ID:            "generation:1",
		RepoID:        work.RepoID,
		ExtractorName: work.ExtractorName,
		CommitSHA:     work.HeadCommitSHA,
	}
	core := &fakeCore{repositoryWorkExecutionResult: evidenceingestion.RepositoryExtractionWorkExecutionResult{
		RequestID: "execute-1",
		Work:      work,
		ClaimID:   "work-claim:1",
		WorkerID:  "worker-1",
		Extraction: evidenceingestion.RepositoryIngestResult{
			RepositorySnapshotID: "repo-snapshot:1",
			SourceGeneration:     generation,
		},
		Finish: evidenceingestion.RepositoryExtractionWorkFinishResult{
			RequestID:          "work-exec-finish:1",
			Work:               work,
			ClaimID:            "work-claim:1",
			WorkerID:           "worker-1",
			Outcome:            evidenceingestion.RepositoryExtractionWorkOutcomeSucceeded,
			SourceGenerationID: generation.ID,
		},
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolExecuteClaimedGitRepositoryExtractionWork, []byte(`{"request_id":"execute-1","workspace_root":"/repo","work_item_id":"repo-work:1","claim_id":"work-claim:1","worker_id":"worker-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ExecuteClaimedGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	input := core.repositoryWorkExecutionInput
	if core.repositoryWorkExecutionCalls != 1 || input.RequestID != "execute-1" || input.WorkspaceRoot != "/repo" || input.WorkItemID != "repo-work:1" || input.ClaimID != "work-claim:1" || input.WorkerID != "worker-1" {
		t.Fatalf("work execution input/calls = %+v/%d", input, core.repositoryWorkExecutionCalls)
	}
	if response.RequestID != core.repositoryWorkExecutionResult.RequestID || response.Work != work || response.Extraction.SourceGeneration != generation || response.Finish.SourceGenerationID != generation.ID {
		t.Fatalf("work execution response = %+v, want %+v", response, core.repositoryWorkExecutionResult)
	}
}

func TestCallToolRunsGitRepositoryExtractionWorkerTickThroughCore(t *testing.T) {
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		HeadCommitSHA: strings.Repeat("a", 40),
	}
	execution := evidenceingestion.RepositoryExtractionWorkExecutionResult{
		RequestID: "work-tick-execute:1",
		Work:      work,
		ClaimID:   "work-claim:1",
		WorkerID:  "worker-1",
	}
	core := &fakeCore{repositoryWorkerTickResult: evidenceingestion.RepositoryExtractionWorkerTickResult{
		RequestID: "tick-1",
		Claim: evidenceingestion.RepositoryExtractionWorkClaimResult{
			RequestID:     "work-tick-claim:1",
			RepoID:        work.RepoID,
			ExtractorName: work.ExtractorName,
			WorkerID:      "worker-1",
			Claimed:       true,
			Work:          &work,
			ClaimID:       execution.ClaimID,
			AttemptNumber: 1,
		},
		Execution: &execution,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRunGitRepositoryExtractionWorkerTick, []byte(`{"request_id":"tick-1","workspace_root":"/repo","repo_id":"ahe-wrap","extractor_name":"repository-go-parser-code-fact","worker_id":"worker-1","lease_duration_milliseconds":60000}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RunGitRepositoryExtractionWorkerTickResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	input := core.repositoryWorkerTickInput
	if core.repositoryWorkerTickCalls != 1 || input.RequestID != "tick-1" || input.WorkspaceRoot != "/repo" || input.RepoID != "ahe-wrap" || input.ExtractorName != evidenceingestion.ExtractorRepositoryGoParserCodeFact || input.WorkerID != "worker-1" || input.LeaseDurationMilliseconds != 60_000 {
		t.Fatalf("worker tick input/calls = %+v/%d", input, core.repositoryWorkerTickCalls)
	}
	if response.RequestID != core.repositoryWorkerTickResult.RequestID || !response.Claim.Claimed || response.Claim.Work == nil || *response.Claim.Work != work || response.Execution == nil || !reflect.DeepEqual(*response.Execution, execution) {
		t.Fatalf("worker tick response = %+v, want %+v", response, core.repositoryWorkerTickResult)
	}
}

func TestCallToolListsExpiredGitRepositoryExtractionWorkClaimsThroughCore(t *testing.T) {
	claimedAt := time.Date(2026, 7, 21, 12, 2, 0, 0, time.UTC)
	leaseExpiresAt := claimedAt.Add(time.Minute)
	claims := []evidenceingestion.RepositoryExtractionWorkExpiredClaim{{
		Work: evidenceingestion.RepositoryExtractionWork{
			WorkItemID:    "repo-work:1",
			RepoID:        "ahe-wrap",
			ExtractorName: evidenceingestion.ExtractorRepositoryGoplsCodeFact,
		},
		ClaimID:        "work-claim:1",
		AttemptNumber:  1,
		WorkerID:       "worker-1",
		ClaimedAt:      claimedAt,
		LeaseExpiresAt: leaseExpiresAt,
	}}
	core := &fakeCore{expiredClaims: claims}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolListExpiredGitRepositoryExtractionWorkClaims, []byte(`{"repo_id":"ahe-wrap","extractor_name":"repository-gopls-code-fact","limit":25}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ListExpiredGitRepositoryExtractionWorkClaimsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	input := core.expiredClaimInput
	if core.expiredClaimCalls != 1 || input.RepoID != "ahe-wrap" || input.ExtractorName != evidenceingestion.ExtractorRepositoryGoplsCodeFact || input.Limit != 25 {
		t.Fatalf("expired claim list input/calls = %+v/%d", input, core.expiredClaimCalls)
	}
	if !reflect.DeepEqual(response, claims) {
		t.Fatalf("expired claim list response = %+v, want %+v", response, claims)
	}
}

func TestCallToolListsExpiredGitRepositoryExtractionWorkExecutionsThroughCore(t *testing.T) {
	claimedAt := time.Date(2026, 7, 22, 10, 2, 0, 0, time.UTC)
	leaseExpiresAt := claimedAt.Add(time.Minute)
	executions := []evidenceingestion.RepositoryExtractionWorkExpiredExecution{{
		ExecutionRequestID: "execution:1",
		Work: evidenceingestion.RepositoryExtractionWork{
			WorkItemID:    "repo-work:1",
			RepoID:        "ahe-wrap",
			ExtractorName: evidenceingestion.ExtractorRepositoryGoplsCodeFact,
		},
		ClaimID:                            "work-claim:1",
		AttemptNumber:                      2,
		WorkerID:                           "worker-1",
		ClaimedAt:                          claimedAt,
		LeaseExpiresAt:                     leaseExpiresAt,
		HeartbeatLeaseDurationMilliseconds: 60_000,
		HeartbeatCount:                     3,
	}}
	core := &fakeCore{expiredExecutions: executions}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolListExpiredGitRepositoryExtractionWorkExecutions, []byte(`{"limit":25}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ListExpiredGitRepositoryExtractionWorkExecutionsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.expiredExecutionCalls != 1 || core.expiredExecutionInput.Limit != 25 {
		t.Fatalf("expired execution list input/calls = %+v/%d", core.expiredExecutionInput, core.expiredExecutionCalls)
	}
	if !reflect.DeepEqual(response, executions) {
		t.Fatalf("expired execution list response = %+v, want %+v", response, executions)
	}
}

func TestCallToolFinishesGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	finishedAt := time.Date(2026, 7, 21, 12, 2, 0, 0, time.UTC)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		HeadCommitSHA: strings.Repeat("a", 40),
	}
	core := &fakeCore{repositoryWorkFinishResult: evidenceingestion.RepositoryExtractionWorkFinishResult{
		RequestID:          "finish-1",
		Work:               work,
		ClaimID:            "work-claim:1",
		WorkerID:           "worker-1",
		Outcome:            evidenceingestion.RepositoryExtractionWorkOutcomeSucceeded,
		SourceGenerationID: "generation:1",
		FinishedAt:         finishedAt,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolFinishGitRepositoryExtractionWork, []byte(`{"request_id":"finish-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","worker_id":"worker-1","outcome":"succeeded","source_generation_id":"generation:1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response FinishGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryWorkFinishCalls != 1 || core.repositoryWorkFinishInput.RequestID != "finish-1" || core.repositoryWorkFinishInput.WorkItemID != "repo-work:1" || core.repositoryWorkFinishInput.ClaimID != "work-claim:1" || core.repositoryWorkFinishInput.WorkerID != "worker-1" || core.repositoryWorkFinishInput.Outcome != evidenceingestion.RepositoryExtractionWorkOutcomeSucceeded || core.repositoryWorkFinishInput.SourceGenerationID != "generation:1" {
		t.Fatalf("finish input/calls = %+v/%d", core.repositoryWorkFinishInput, core.repositoryWorkFinishCalls)
	}
	if response != core.repositoryWorkFinishResult {
		t.Fatalf("finish response = %+v, want %+v", response, core.repositoryWorkFinishResult)
	}
}

func TestCallToolRecoversExpiredGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	recoveredAt := time.Date(2026, 7, 21, 12, 3, 0, 0, time.UTC)
	leaseExpiresAt := recoveredAt.Add(-time.Second)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	}
	core := &fakeCore{repositoryWorkRecoveryResult: evidenceingestion.RepositoryExtractionWorkRecoveryResult{
		RequestID:       "recover-1",
		Work:            work,
		ClaimID:         "work-claim:1",
		AttemptNumber:   1,
		WorkerID:        "worker-1",
		LeaseExpiresAt:  leaseExpiresAt,
		RecoveredAt:     recoveredAt,
		RecoveryActorID: "operator-1",
		Disposition:     evidenceingestion.RepositoryExtractionWorkRecoveryDispositionRequeued,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRecoverExpiredGitRepositoryExtractionWork, []byte(`{"request_id":"recover-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","recovery_actor_id":"operator-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RecoverExpiredGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryWorkRecoveryCalls != 1 || core.repositoryWorkRecoveryInput.RequestID != "recover-1" || core.repositoryWorkRecoveryInput.WorkItemID != "repo-work:1" || core.repositoryWorkRecoveryInput.ClaimID != "work-claim:1" || core.repositoryWorkRecoveryInput.RecoveryActorID != "operator-1" {
		t.Fatalf("recovery input/calls = %+v/%d", core.repositoryWorkRecoveryInput, core.repositoryWorkRecoveryCalls)
	}
	if response != core.repositoryWorkRecoveryResult {
		t.Fatalf("recovery response = %+v, want %+v", response, core.repositoryWorkRecoveryResult)
	}
}

func TestCallToolRepairsExpiredGitRepositoryExtractionWorkExecutionThroughCore(t *testing.T) {
	leaseExpiresAt := time.Date(2026, 7, 21, 12, 3, 0, 0, time.UTC)
	repairedAt := leaseExpiresAt.Add(time.Second)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	}
	core := &fakeCore{executionRepairResult: evidenceingestion.RepositoryExtractionWorkExecutionRepairResult{
		RequestID:          "repair-execution-1",
		ExecutionRequestID: "execute-1",
		Work:               work,
		ClaimID:            "work-claim:1",
		AttemptNumber:      1,
		WorkerID:           "worker-1",
		RepairActorID:      "controller-1",
		RepairReason:       evidenceingestion.RepositoryExtractionWorkExecutionRepairReasonLeaseExpired,
		LeaseExpiresAt:     leaseExpiresAt,
		RepairedAt:         repairedAt,
		Disposition:        evidenceingestion.RepositoryExtractionWorkRecoveryDispositionRequeued,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRepairExpiredGitRepositoryExtractionWorkExecution, []byte(`{"request_id":"repair-execution-1","execution_request_id":"execute-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","worker_id":"worker-1","repair_actor_id":"controller-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RepairExpiredGitRepositoryExtractionWorkExecutionResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.executionRepairCalls != 1 || core.executionRepairInput.RequestID != "repair-execution-1" || core.executionRepairInput.ExecutionRequestID != "execute-1" || core.executionRepairInput.WorkItemID != "repo-work:1" || core.executionRepairInput.ClaimID != "work-claim:1" || core.executionRepairInput.WorkerID != "worker-1" || core.executionRepairInput.RepairActorID != "controller-1" {
		t.Fatalf("execution repair input/calls = %+v/%d", core.executionRepairInput, core.executionRepairCalls)
	}
	if response != core.executionRepairResult {
		t.Fatalf("execution repair response = %+v, want %+v", response, core.executionRepairResult)
	}
}

func TestCallToolRetriesFailedGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	finishedAt := time.Date(2026, 7, 21, 12, 3, 0, 0, time.UTC)
	retriedAt := finishedAt.Add(time.Second)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	}
	core := &fakeCore{repositoryWorkRetryResult: evidenceingestion.RepositoryExtractionWorkRetryResult{
		RequestID:      "retry-1",
		Work:           work,
		ClaimID:        "work-claim:1",
		AttemptNumber:  1,
		WorkerID:       "worker-1",
		FinishedAt:     finishedAt,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "gopls exited before initialization",
		RetriedAt:      retriedAt,
		RetryActorID:   "operator-1",
		Disposition:    evidenceingestion.RepositoryExtractionWorkRetryDispositionRequeued,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRetryFailedGitRepositoryExtractionWork, []byte(`{"request_id":"retry-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","retry_actor_id":"operator-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RetryFailedGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryWorkRetryCalls != 1 || core.repositoryWorkRetryInput.RequestID != "retry-1" || core.repositoryWorkRetryInput.WorkItemID != "repo-work:1" || core.repositoryWorkRetryInput.ClaimID != "work-claim:1" || core.repositoryWorkRetryInput.RetryActorID != "operator-1" {
		t.Fatalf("retry input/calls = %+v/%d", core.repositoryWorkRetryInput, core.repositoryWorkRetryCalls)
	}
	if response != core.repositoryWorkRetryResult {
		t.Fatalf("retry response = %+v, want %+v", response, core.repositoryWorkRetryResult)
	}
}

func TestCallToolClassifiesFailedGitRepositoryExtractionWorkThroughCore(t *testing.T) {
	finishedAt := time.Date(2026, 7, 21, 12, 4, 0, 0, time.UTC)
	decidedAt := finishedAt.Add(time.Second)
	retryNotBefore := decidedAt.Add(5 * time.Second)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	}
	core := &fakeCore{failurePolicyResult: evidenceingestion.RepositoryExtractionWorkFailurePolicyResult{
		RequestID:           "failure-policy-1",
		Work:                work,
		ClaimID:             "work-claim:1",
		AttemptNumber:       1,
		WorkerID:            "worker-1",
		FinishedAt:          finishedAt,
		FailureClass:        string(evidenceingestion.ErrorRunnerInvocationFailed),
		FailureMessage:      "gopls exited before initialization",
		PolicyVersion:       evidenceingestion.RepositoryExtractionWorkFailurePolicyV1,
		Decision:            evidenceingestion.RepositoryExtractionWorkFailureDecisionRetry,
		DecisionReason:      evidenceingestion.RepositoryExtractionWorkFailureReasonRetryable,
		MaxAttempts:         evidenceingestion.RepositoryExtractionWorkFailurePolicyMaxAttempts,
		BackoffMilliseconds: 5000,
		RetryNotBefore:      &retryNotBefore,
		DecidedAt:           decidedAt,
		DecisionActorID:     "controller-1",
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolClassifyFailedGitRepositoryExtractionWork, []byte(`{"request_id":"failure-policy-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","decision_actor_id":"controller-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ClassifyFailedGitRepositoryExtractionWorkResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.failurePolicyCalls != 1 || core.failurePolicyInput.RequestID != "failure-policy-1" || core.failurePolicyInput.WorkItemID != "repo-work:1" || core.failurePolicyInput.ClaimID != "work-claim:1" || core.failurePolicyInput.DecisionActorID != "controller-1" {
		t.Fatalf("failure policy input/calls = %+v/%d", core.failurePolicyInput, core.failurePolicyCalls)
	}
	if !reflect.DeepEqual(response, core.failurePolicyResult) {
		t.Fatalf("failure policy response = %+v, want %+v", response, core.failurePolicyResult)
	}
}

func TestCallToolListsDueGitRepositoryExtractionWorkRetryDecisionsThroughCore(t *testing.T) {
	finishedAt := time.Date(2026, 7, 22, 12, 4, 0, 0, time.UTC)
	decidedAt := finishedAt.Add(time.Second)
	retryNotBefore := decidedAt.Add(5 * time.Second)
	decisions := []evidenceingestion.RepositoryExtractionWorkDueRetryDecision{{
		FailurePolicyRequestID: "failure-policy-1",
		Work: evidenceingestion.RepositoryExtractionWork{
			WorkItemID:    "repo-work:1",
			RepoID:        "ahe-wrap",
			ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		},
		ClaimID:             "work-claim:1",
		AttemptNumber:       1,
		WorkerID:            "worker-1",
		FinishedAt:          finishedAt,
		FailureClass:        string(evidenceingestion.ErrorRunnerInvocationFailed),
		FailureMessage:      "gopls exited before initialization",
		PolicyVersion:       evidenceingestion.RepositoryExtractionWorkFailurePolicyV1,
		Decision:            evidenceingestion.RepositoryExtractionWorkFailureDecisionRetry,
		DecisionReason:      evidenceingestion.RepositoryExtractionWorkFailureReasonRetryable,
		MaxAttempts:         evidenceingestion.RepositoryExtractionWorkFailurePolicyMaxAttempts,
		BackoffMilliseconds: 5000,
		RetryNotBefore:      retryNotBefore,
		DecidedAt:           decidedAt,
		DecisionActorID:     "controller-1",
	}}
	core := &fakeCore{dueRetryDecisions: decisions}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolListDueGitRepositoryExtractionWorkRetryDecisions, []byte(`{"limit":25}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ListDueGitRepositoryExtractionWorkRetryDecisionsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.dueRetryDecisionCalls != 1 || core.dueRetryDecisionInput.Limit != 25 {
		t.Fatalf("due retry decision list input/calls = %+v/%d", core.dueRetryDecisionInput, core.dueRetryDecisionCalls)
	}
	if !reflect.DeepEqual(response, decisions) {
		t.Fatalf("due retry decision list response = %+v, want %+v", response, decisions)
	}
}

func TestCallToolConsumesDueGitRepositoryExtractionWorkRetryDecisionThroughCore(t *testing.T) {
	finishedAt := time.Date(2026, 7, 21, 12, 4, 0, 0, time.UTC)
	decidedAt := finishedAt.Add(time.Second)
	retryNotBefore := decidedAt.Add(5 * time.Second)
	consumedAt := retryNotBefore.Add(time.Second)
	work := evidenceingestion.RepositoryExtractionWork{
		WorkItemID:    "repo-work:1",
		RepoID:        "ahe-wrap",
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	}
	core := &fakeCore{retryDecisionResult: evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult{
		RequestID:              "consume-retry-decision-1",
		FailurePolicyRequestID: "failure-policy-1",
		Work:                   work,
		ClaimID:                "work-claim:1",
		AttemptNumber:          1,
		WorkerID:               "worker-1",
		FinishedAt:             finishedAt,
		FailureClass:           string(evidenceingestion.ErrorRunnerInvocationFailed),
		FailureMessage:         "gopls exited before initialization",
		PolicyVersion:          evidenceingestion.RepositoryExtractionWorkFailurePolicyV1,
		Decision:               evidenceingestion.RepositoryExtractionWorkFailureDecisionRetry,
		DecisionReason:         evidenceingestion.RepositoryExtractionWorkFailureReasonRetryable,
		BackoffMilliseconds:    5000,
		RetryNotBefore:         retryNotBefore,
		DecidedAt:              decidedAt,
		ConsumedAt:             consumedAt,
		ConsumerActorID:        "retry-controller-1",
		Disposition:            evidenceingestion.RepositoryExtractionWorkRetryDispositionRequeued,
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, []byte(`{"request_id":"consume-retry-decision-1","failure_policy_request_id":"failure-policy-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","consumer_actor_id":"retry-controller-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ConsumeDueGitRepositoryExtractionWorkRetryDecisionResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.retryDecisionCalls != 1 || core.retryDecisionInput.RequestID != "consume-retry-decision-1" || core.retryDecisionInput.FailurePolicyRequestID != "failure-policy-1" || core.retryDecisionInput.WorkItemID != "repo-work:1" || core.retryDecisionInput.ClaimID != "work-claim:1" || core.retryDecisionInput.ConsumerActorID != "retry-controller-1" {
		t.Fatalf("retry decision input/calls = %+v/%d", core.retryDecisionInput, core.retryDecisionCalls)
	}
	if response != core.retryDecisionResult {
		t.Fatalf("retry decision response = %+v, want %+v", response, core.retryDecisionResult)
	}
}

func TestCallToolRunsDueGitRepositoryExtractionWorkRetryControllerTickThroughCore(t *testing.T) {
	discoveredAt := time.Date(2026, 7, 22, 12, 5, 0, 0, time.UTC)
	core := &fakeCore{retryControllerTickResult: evidenceingestion.RepositoryExtractionWorkRetryControllerTickResult{
		RequestID:       "retry-controller-tick-1",
		Limit:           25,
		ConsumerActorID: "retry-controller-1",
		DiscoveredAt:    discoveredAt,
		Consumptions:    []evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult{},
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRunDueGitRepositoryExtractionWorkRetryControllerTick, []byte(`{"request_id":"retry-controller-tick-1","limit":25,"consumer_actor_id":"retry-controller-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RunDueGitRepositoryExtractionWorkRetryControllerTickResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.retryControllerTickCalls != 1 || core.retryControllerTickInput.RequestID != "retry-controller-tick-1" || core.retryControllerTickInput.Limit != 25 || core.retryControllerTickInput.ConsumerActorID != "retry-controller-1" {
		t.Fatalf("retry controller tick input/calls = %+v/%d", core.retryControllerTickInput, core.retryControllerTickCalls)
	}
	if !reflect.DeepEqual(response, core.retryControllerTickResult) {
		t.Fatalf("retry controller tick response = %+v, want %+v", response, core.retryControllerTickResult)
	}
}

func TestCallToolRunsExpiredGitRepositoryExtractionWorkMaintenanceTickThroughCore(t *testing.T) {
	discoveredAt := time.Date(2026, 7, 22, 12, 10, 0, 0, time.UTC)
	core := &fakeCore{expiredMaintenanceTickResult: evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickResult{
		RequestID:          "expired-maintenance-tick-1",
		Limit:              25,
		MaintenanceActorID: "maintenance-controller-1",
		DiscoveredAt:       discoveredAt,
		Transitions:        []evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTransition{},
	}}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick, []byte(`{"request_id":"expired-maintenance-tick-1","limit":25,"maintenance_actor_id":"maintenance-controller-1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response RunExpiredGitRepositoryExtractionWorkMaintenanceTickResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.expiredMaintenanceTickCalls != 1 || core.expiredMaintenanceTickInput.RequestID != "expired-maintenance-tick-1" || core.expiredMaintenanceTickInput.Limit != 25 || core.expiredMaintenanceTickInput.MaintenanceActorID != "maintenance-controller-1" {
		t.Fatalf("expired maintenance tick input/calls = %+v/%d", core.expiredMaintenanceTickInput, core.expiredMaintenanceTickCalls)
	}
	if !reflect.DeepEqual(response, core.expiredMaintenanceTickResult) {
		t.Fatalf("expired maintenance tick response = %+v, want %+v", response, core.expiredMaintenanceTickResult)
	}
}

func TestCallToolLoadsRepositoryExtractorInputThroughCore(t *testing.T) {
	core := &fakeCore{
		buildRepositoryResult: evidenceingestion.RepositoryExtractorInput{
			RepositorySnapshot: evidenceingestion.RepositorySnapshot{ID: "repo-snapshot:1"},
			Files: []evidenceingestion.RepositoryExtractorFile{{
				FileSnapshot: evidenceingestion.SourceFileSnapshot{Path: "main.go"},
				Content:      []byte("package main\n"),
			}},
		},
	}
	server := newServer(core)
	data, err := server.CallTool(context.Background(), ToolGetRepositoryExtractorInput, []byte(`{"repository_snapshot_id":"repo-snapshot:1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp GetRepositoryExtractorInputResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.buildRepositorySnapshotID != "repo-snapshot:1" || len(resp.Files) != 1 || string(resp.Files[0].Content) != "package main\n" {
		t.Fatalf("repository extractor input = %+v, core snapshot ID = %q", resp, core.buildRepositorySnapshotID)
	}
}

func TestCallToolRunsRepositoryGoParserThroughCore(t *testing.T) {
	core := &fakeCore{repositoryParserResult: evidenceingestion.RepositoryIngestResult{
		RepositorySnapshotID: "repo-snapshot:1",
		ExtractionRunID:      "run:1",
		ExtractionAttemptID:  "attempt:1",
		ProposalBatchID:      "batch:1",
		ProposalCount:        2,
		SourceGeneration: evidenceingestion.RepositorySourceGeneration{
			ID:                   "generation:1",
			Number:               1,
			RepositorySnapshotID: "repo-snapshot:1",
			ProposalBatchID:      "batch:1",
		},
	}}
	server := newServer(core)
	data, err := server.CallTool(context.Background(), ToolRunRepositoryGoParserExtractor, []byte(`{"request_id":"repo-run","repository_snapshot_id":"repo-snapshot:1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunRepositoryGoParserExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryParserCalls != 1 || core.repositoryParserRequest.RequestID != "repo-run" || core.repositoryParserRequest.RepositorySnapshotID != "repo-snapshot:1" {
		t.Fatalf("repository parser request/calls = %+v/%d", core.repositoryParserRequest, core.repositoryParserCalls)
	}
	if resp.ProposalBatchID != "batch:1" || resp.ProposalCount != 2 || resp.SourceGeneration.ID != "generation:1" || resp.SourceGeneration.Number != 1 || resp.SourceGenerationReplayed {
		t.Fatalf("repository parser response = %+v", resp)
	}
}

func TestCallToolRunsRepositoryGoplsThroughCore(t *testing.T) {
	core := &fakeCore{repositoryGoplsResult: evidenceingestion.RepositoryIngestResult{
		RepositorySnapshotID: "repo-snapshot:1",
		ExtractionRunID:      "run:1",
		ExtractionAttemptID:  "attempt:1",
		ProposalBatchID:      "batch:1",
		ProposalCount:        2,
		SourceGeneration: evidenceingestion.RepositorySourceGeneration{
			ID:                   "generation:2",
			Number:               2,
			RepositorySnapshotID: "repo-snapshot:1",
			ProposalBatchID:      "batch:1",
		},
		SourceGenerationReplayed: true,
		RepositoryGoplsCoverage: &evidenceingestion.RepositoryGoplsCoverage{
			SchemaVersion:                 evidenceingestion.RepositoryGoplsCoverageSchemaV8,
			RequestCoverageComplete:       true,
			CorroboratedCallRelationCount: 1,
			EmittedCallRelationCount:      1,
		},
	}}
	server := newServer(core)
	data, err := server.CallTool(context.Background(), ToolRunRepositoryGoplsExtractor, []byte(`{"request_id":"repo-gopls","repository_snapshot_id":"repo-snapshot:1","workspace_root":"/repo"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunRepositoryGoplsExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.repositoryGoplsCalls != 1 || core.repositoryGoplsRequest.RequestID != "repo-gopls" || core.repositoryGoplsRequest.RepositorySnapshotID != "repo-snapshot:1" || core.repositoryGoplsRequest.WorkspaceRoot != "/repo" {
		t.Fatalf("repository gopls request/calls = %+v/%d", core.repositoryGoplsRequest, core.repositoryGoplsCalls)
	}
	if resp.ProposalBatchID != "batch:1" || resp.ProposalCount != 2 || resp.SourceGeneration.ID != "generation:2" || !resp.SourceGenerationReplayed || resp.RepositoryGoplsCoverage == nil || !resp.RepositoryGoplsCoverage.RequestCoverageComplete || resp.RepositoryGoplsCoverage.SchemaVersion != evidenceingestion.RepositoryGoplsCoverageSchemaV8 || resp.RepositoryGoplsCoverage.CorroboratedCallRelationCount != 1 || resp.RepositoryGoplsCoverage.EmittedCallRelationCount != 1 {
		t.Fatalf("repository gopls response = %+v", resp)
	}
}

func TestCallToolActivatesRepositorySourceGenerationThroughCore(t *testing.T) {
	core := &fakeCore{
		activationResult: evidenceingestion.RepositorySourceGenerationActivationResult{
			RequestID:             "activate-generation-1",
			RepoID:                "ahe-wrap",
			ExtractorName:         evidenceingestion.ExtractorRepositoryGoParserCodeFact,
			ExtractorDefinitionID: "extractor-definition:1",
			ActivatedGenerationID: "generation:1",
			Changed:               true,
			Reconciliation: evidenceingestion.RepositoryGenerationReconciliation{
				SourceGenerationID: "generation:1",
				IdentityContract:   evidenceingestion.RepositoryProposalIdentityContractV1,
				NewCount:           2,
			},
		},
	}
	server := newServer(core)

	data, err := server.CallTool(
		context.Background(),
		ToolActivateRepositorySourceGeneration,
		[]byte(`{"request_id":"activate-generation-1","source_generation_id":"generation:1"}`),
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ActivateRepositorySourceGenerationResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.activationCalls != 1 ||
		core.activationInput.RequestID != "activate-generation-1" ||
		core.activationInput.SourceGenerationID != "generation:1" {
		t.Fatalf("activation input/calls = %+v/%d", core.activationInput, core.activationCalls)
	}
	if !reflect.DeepEqual(response, core.activationResult) {
		t.Fatalf("activation response = %+v, want %+v", response, core.activationResult)
	}
}

func TestCallToolListsRepositorySourceGenerationsThroughCore(t *testing.T) {
	want := []evidenceingestion.RepositorySourceGenerationStatus{
		{
			RepositorySourceGeneration: evidenceingestion.RepositorySourceGeneration{
				ID:            "generation:2",
				RepoID:        "ahe-wrap",
				ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
				Number:        2,
			},
			Active: false,
		},
		{
			RepositorySourceGeneration: evidenceingestion.RepositorySourceGeneration{
				ID:            "generation:1",
				RepoID:        "ahe-wrap",
				ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
				Number:        1,
			},
			Active: true,
		},
	}
	core := &fakeCore{generationListResult: want}
	server := newServer(core)

	data, err := server.CallTool(
		context.Background(),
		ToolListRepositorySourceGenerations,
		[]byte(`{"repo_id":"ahe-wrap","extractor_name":"repository-go-parser-code-fact","limit":10}`),
	)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var response ListRepositorySourceGenerationsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if core.generationListCalls != 1 ||
		core.generationListInput.RepoID != "ahe-wrap" ||
		core.generationListInput.ExtractorName != evidenceingestion.ExtractorRepositoryGoParserCodeFact ||
		core.generationListInput.Limit != 10 {
		t.Fatalf("generation list input/calls = %+v/%d", core.generationListInput, core.generationListCalls)
	}
	if response.Count != len(want) ||
		response.Limit != 10 ||
		!reflect.DeepEqual(response.Generations, want) {
		t.Fatalf("generation list response = %+v, want %+v", response, want)
	}
}

func TestCallToolInspectsGoplsWorkspaceWithoutCoreWrites(t *testing.T) {
	core := &fakeCore{}
	server := newServer(core)
	want := evidenceingestion.GoplsWorkspaceInventory{
		RepoID:                 "ahe-wrap",
		CommitSHA:              "abc123",
		GoplsVersion:           "v0.23.0",
		RevisionBindingMethod:  "caller-asserted",
		PackageDiscoveryMethod: "gopls.packages",
		FileDiscoveryMethod:    "controller/module-root-walk",
		Modules: []evidenceingestion.GoplsWorkspaceModule{{
			Path:      "ahe-wrap",
			GoModPath: "go.mod",
		}},
		Packages: []evidenceingestion.GoplsWorkspacePackage{{
			Path:       "github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion",
			ModulePath: "ahe-wrap",
		}},
		Files: []evidenceingestion.GoplsWorkspaceFile{{
			Path:        "internal/evidenceingestion/types.go",
			ContentHash: "sha256:file",
			SizeBytes:   42,
		}},
	}
	var gotConfig evidenceingestion.GoplsWorkspaceInventoryConfig
	server.collectGoplsWorkspaceInventory = func(_ context.Context, config evidenceingestion.GoplsWorkspaceInventoryConfig) (evidenceingestion.GoplsWorkspaceInventory, error) {
		gotConfig = config
		return want, nil
	}

	data, err := server.CallTool(context.Background(), ToolInspectGoplsWorkspace, []byte(`{"workspace_root":"/repo","repo_id":"ahe-wrap","commit_sha":"abc123"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var got InspectGoplsWorkspaceResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if gotConfig.WorkspaceRoot != "/repo" || gotConfig.RepoID != "ahe-wrap" || gotConfig.CommitSHA != "abc123" || gotConfig.BinaryPath != "" {
		t.Fatalf("inventory config = %+v", gotConfig)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inventory response = %+v, want %+v", got, want)
	}
	if core.captureCalls != 0 || core.ingestCalls != 0 || core.extractorCalls != 0 || core.trustedCalls != 0 || core.traceCalls != 0 {
		t.Fatalf("inventory invoked ingestion core: %+v", core)
	}
}

func TestCallToolRunsLocalOllamaExtractorThroughCore(t *testing.T) {
	core := &fakeCore{
		trustedResult: evidenceingestion.IngestResult{
			SourceSnapshotID:     "srcsnap:1",
			ExtractionViewID:     "view:1",
			ExtractionRunID:      "run:1",
			ExtractionAttemptID:  "attempt:1",
			ProposalOccurrenceID: "occ:1",
			ProposalFingerprint:  "fp:1",
		},
		traceResult: evidenceingestion.ProposalQueryResult{
			ProposalOccurrenceID: "occ:1",
			AdmissionOutcome:     "pending",
		},
	}
	server := newServer(core)
	payload := []byte(`{"request_id":"ollama-run","extraction_view_id":"view:1","model":"gemma4:12b","base_url":"http://127.0.0.1:11434","retry_failed_attempt":true,"num_predict":512}`)

	data, err := server.CallTool(context.Background(), ToolRunLocalOllamaExtractor, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunLocalOllamaExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.trustedCalls != 1 || core.traceCalls != 1 {
		t.Fatalf("core calls = trusted %d trace %d, want 1/1", core.trustedCalls, core.traceCalls)
	}
	if core.trustedRequest.RequestID != "ollama-run" || core.trustedRequest.ExtractionViewID != "view:1" || !core.trustedRequest.RetryFailedAttempt {
		t.Fatalf("trusted request not forwarded: %+v", core.trustedRequest)
	}
	definition := core.trustedRequest.ExtractorDefinition
	if definition.Name != evidenceingestion.ExtractorOllamaLocal || definition.Version != evidenceingestion.ExtractorOllamaLocalVersion {
		t.Fatalf("extractor definition = %+v, want ollama-local/v1", definition)
	}
	if definition.Config["model"] != "gemma4:12b" || definition.Config["num_predict"] != "512" {
		t.Fatalf("extractor config = %+v", definition.Config)
	}
	if resp.SourceSnapshotID != "srcsnap:1" || resp.ExtractionRunID != "run:1" || resp.ExtractionAttemptID != "attempt:1" || resp.ProposalOccurrenceID != "occ:1" || resp.ProposalFingerprint != "fp:1" {
		t.Fatalf("unexpected response IDs: %+v", resp)
	}
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
}

func TestCallToolRunsGoParserExtractorThroughCore(t *testing.T) {
	core := &fakeCore{
		trustedResult: evidenceingestion.IngestResult{
			SourceSnapshotID:     "srcsnap:1",
			ExtractionViewID:     "view:1",
			ExtractionRunID:      "run:1",
			ExtractionAttemptID:  "attempt:1",
			ProposalOccurrenceID: "occ:1",
			ProposalFingerprint:  "fp:1",
		},
		traceResult: evidenceingestion.ProposalQueryResult{
			ProposalOccurrenceID: "occ:1",
			AdmissionOutcome:     "pending",
		},
	}
	server := newServer(core)
	payload := []byte(`{"request_id":"go-parser-run","extraction_view_id":"view:1","repo_id":"ahe-wrap","commit_sha":"abc123","path":"internal/refund/service.go","retry_failed_attempt":true}`)

	data, err := server.CallTool(context.Background(), ToolRunGoParserExtractor, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RunGoParserExtractorResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.trustedCalls != 1 || core.traceCalls != 1 {
		t.Fatalf("core calls = trusted %d trace %d, want 1/1", core.trustedCalls, core.traceCalls)
	}
	if core.trustedRequest.RequestID != "go-parser-run" || core.trustedRequest.ExtractionViewID != "view:1" || !core.trustedRequest.RetryFailedAttempt {
		t.Fatalf("trusted request not forwarded: %+v", core.trustedRequest)
	}
	definition := core.trustedRequest.ExtractorDefinition
	if definition.Name != evidenceingestion.ExtractorGoParserCodeFact || definition.Version != evidenceingestion.ExtractorGoParserCodeFactVersion {
		t.Fatalf("extractor definition = %+v, want go-parser-code-fact/v1", definition)
	}
	if definition.Config["backend"] != "go/parser" || definition.Config["topology_scope"] != "declarations_only" {
		t.Fatalf("extractor config = %+v", definition.Config)
	}
	if resp.SourceSnapshotID != "srcsnap:1" || resp.ExtractionRunID != "run:1" || resp.ExtractionAttemptID != "attempt:1" || resp.ProposalOccurrenceID != "occ:1" || resp.ProposalFingerprint != "fp:1" {
		t.Fatalf("unexpected response IDs: %+v", resp)
	}
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
}

func TestCallToolAdmitsPendingProposalThroughCore(t *testing.T) {
	core := &fakeCore{
		admitResult: evidenceingestion.AdmissionResult{
			ProposalOccurrenceID: "occ:1",
			AdmissionDecisionID:  "adm:1",
			AdmissionOutcome:     "admitted",
			CanonicalRef:         "canon-node:1",
			RawEvidenceNodeIDs:   []string{"canon-node:raw"},
			CanonicalEdgeIDs:     []string{"canon-edge:1"},
			DerivationID:         "derivation:1",
			ParentNodeIDs:        []string{"canon-node:parent"},
		},
	}
	server := newServer(core)
	payload := []byte(`{"proposal_occurrence_id":"occ:1","decision_by":"unit-test","decision_reason":"accepted","derivation":{"parent_node_ids":["canon-node:parent"],"method":"summary","producer":"unit-test","trace_ref":"trace:1"}}`)

	data, err := server.CallTool(context.Background(), ToolAdmitPendingProposal, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp AdmitPendingProposalResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.admitCalls != 1 {
		t.Fatalf("admit calls = %d, want 1", core.admitCalls)
	}
	if core.admissionInput.ProposalOccurrenceID != "occ:1" || core.admissionInput.DecisionBy != "unit-test" || core.admissionInput.DecisionReason != "accepted" {
		t.Fatalf("admission input not forwarded: %+v", core.admissionInput)
	}
	if core.admissionInput.Derivation == nil || !reflect.DeepEqual(core.admissionInput.Derivation.ParentNodeIDs, []string{"canon-node:parent"}) || core.admissionInput.Derivation.Method != "summary" || core.admissionInput.Derivation.Producer != "unit-test" || core.admissionInput.Derivation.TraceRef != "trace:1" {
		t.Fatalf("derivation input not forwarded: %+v", core.admissionInput.Derivation)
	}
	if resp.ProposalOccurrenceID != "occ:1" || resp.AdmissionDecisionID != "adm:1" || resp.AdmissionOutcome != "admitted" || resp.CanonicalRef != "canon-node:1" {
		t.Fatalf("unexpected response IDs: %+v", resp)
	}
	if !reflect.DeepEqual(resp.RawEvidenceNodeIDs, []string{"canon-node:raw"}) || !reflect.DeepEqual(resp.CanonicalEdgeIDs, []string{"canon-edge:1"}) {
		t.Fatalf("graph IDs = raw %v edges %v", resp.RawEvidenceNodeIDs, resp.CanonicalEdgeIDs)
	}
	if resp.DerivationID != "derivation:1" || !reflect.DeepEqual(resp.ParentNodeIDs, []string{"canon-node:parent"}) {
		t.Fatalf("derivation response = %+v", resp)
	}
}

func TestCallToolRecordsPendingProposalDispositionThroughCore(t *testing.T) {
	core := &fakeCore{
		dispositionResult: evidenceingestion.ProposalDispositionResult{
			ProposalOccurrenceID: "occ:1",
			AdmissionDecisionID:  "adm:1",
			AdmissionOutcome:     evidenceingestion.ProposalDispositionAuditOnly,
			DecisionBy:           "unit-test",
			DecisionReason:       "retain for audit",
		},
	}
	server := newServer(core)
	payload := []byte(`{"proposal_occurrence_id":"occ:1","outcome":"audit_only","decision_by":"unit-test","decision_reason":"retain for audit"}`)

	data, err := server.CallTool(context.Background(), ToolRecordPendingProposalDisposition, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp RecordPendingProposalDispositionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.dispositionCalls != 1 {
		t.Fatalf("disposition calls = %d, want 1", core.dispositionCalls)
	}
	if core.dispositionInput.ProposalOccurrenceID != "occ:1" ||
		core.dispositionInput.Outcome != evidenceingestion.ProposalDispositionAuditOnly ||
		core.dispositionInput.DecisionBy != "unit-test" ||
		core.dispositionInput.DecisionReason != "retain for audit" {
		t.Fatalf("disposition input not forwarded: %+v", core.dispositionInput)
	}
	if resp.ProposalOccurrenceID != "occ:1" ||
		resp.AdmissionDecisionID != "adm:1" ||
		resp.AdmissionOutcome != evidenceingestion.ProposalDispositionAuditOnly ||
		resp.DecisionBy != "unit-test" ||
		resp.DecisionReason != "retain for audit" ||
		resp.Replayed {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCallToolSubmitsManualEvidenceThroughCore(t *testing.T) {
	core := &fakeCore{
		ingestResult: evidenceingestion.IngestResult{
			SourceSnapshotID:     "srcsnap:1",
			ExtractionAttemptID:  "attempt:1",
			ProposalOccurrenceID: "occ:1",
			ProposalFingerprint:  "fp:1",
		},
		traceResult: evidenceingestion.ProposalQueryResult{
			ProposalOccurrenceID: "occ:1",
			AdmissionOutcome:     "pending",
		},
	}
	server := newServer(core)
	req := testRequest("request-a")
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}

	data, err := server.CallTool(context.Background(), ToolSubmitManualEvidence, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitManualEvidenceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.ingestCalls != 1 || core.traceCalls != 1 {
		t.Fatalf("core calls = ingest %d trace %d, want 1/1", core.ingestCalls, core.traceCalls)
	}
	if core.traceOccurrenceID != "occ:1" {
		t.Fatalf("trace occurrence ID = %q, want occ:1", core.traceOccurrenceID)
	}
	if core.input.SourceID != req.SourceID || core.input.SourceVersion != req.SourceVersion || string(core.input.Raw) != req.RawText {
		t.Fatalf("manual input not forwarded: %+v", core.input)
	}
	if !reflect.DeepEqual(core.input.OriginMetadata, req.OriginMetadata) {
		t.Fatalf("origin metadata = %#v, want %#v", core.input.OriginMetadata, req.OriginMetadata)
	}
	if !reflect.DeepEqual(core.fixture, req.ExtractorOutput) {
		t.Fatalf("fixture output not forwarded: %#v", core.fixture)
	}
	if resp.SourceSnapshotID != "srcsnap:1" || resp.ExtractionAttemptID != "attempt:1" || resp.ProposalOccurrenceID != "occ:1" || resp.ProposalFingerprint != "fp:1" {
		t.Fatalf("unexpected response IDs: %+v", resp)
	}
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
}

func TestCallToolSubmitsTextSourceThroughCore(t *testing.T) {
	core := &fakeCore{
		captureResult: evidenceingestion.SourceIntakeResult{
			SourceSnapshotID:    "srcsnap:1",
			ExtractionViewID:    "view:1",
			SourceSystem:        evidenceingestion.SourceSystemCodeFile,
			RawContentHash:      "sha256:raw",
			RenderedContentHash: "sha256:rendered",
			SpanCatalogVersion:  evidenceingestion.SpanCatalogCodeLineV1,
			Spans: []evidenceingestion.SpanEntry{{
				ExtractionViewID:   "view:1",
				SpanID:             "span:S1",
				SpanCatalogVersion: evidenceingestion.SpanCatalogCodeLineV1,
				StartByte:          0,
				EndByte:            42,
				QuotedTextHash:     "sha256:quote",
				QuotedText:         "Refunds must be completed within 7 days.",
			}},
		},
	}
	server := newServer(core)
	req := testTextSourceRequest("source-only")
	req.SourceSystem = evidenceingestion.SourceSystemCodeFile
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}

	data, err := server.CallTool(context.Background(), ToolSubmitTextSource, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitTextSourceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.captureCalls != 1 || core.ingestCalls != 0 || core.traceCalls != 0 {
		t.Fatalf("core calls = capture %d ingest %d trace %d, want 1/0/0", core.captureCalls, core.ingestCalls, core.traceCalls)
	}
	if core.input.SourceSystem != req.SourceSystem || core.input.SourceID != req.SourceID || core.input.SourceVersion != req.SourceVersion || string(core.input.Raw) != req.RawText {
		t.Fatalf("source input not forwarded: %+v", core.input)
	}
	if !reflect.DeepEqual(core.input.OriginMetadata, req.OriginMetadata) {
		t.Fatalf("origin metadata = %#v, want %#v", core.input.OriginMetadata, req.OriginMetadata)
	}
	if resp.SourceSnapshotID != "srcsnap:1" || resp.ExtractionViewID != "view:1" || resp.SourceSystem != evidenceingestion.SourceSystemCodeFile || resp.SpanCatalogVersion != evidenceingestion.SpanCatalogCodeLineV1 {
		t.Fatalf("unexpected response IDs: %+v", resp)
	}
	if len(resp.Spans) != 1 || resp.Spans[0].SpanID != "span:S1" {
		t.Fatalf("response spans = %+v, want span:S1", resp.Spans)
	}
}

func TestCallToolSubmitsExtractorOutputThroughCore(t *testing.T) {
	core := &fakeCore{
		extractorResult: evidenceingestion.IngestResult{
			SourceSnapshotID:     "srcsnap:1",
			ExtractionViewID:     "view:1",
			ExtractionAttemptID:  "attempt:1",
			ProposalOccurrenceID: "occ:1",
			ProposalFingerprint:  "fp:1",
		},
		traceResult: evidenceingestion.ProposalQueryResult{
			ProposalOccurrenceID: "occ:1",
			AdmissionOutcome:     "pending",
		},
	}
	server := newServer(core)
	req := testExtractorOutputRequest("extractor-output")
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}

	data, err := server.CallTool(context.Background(), ToolSubmitExtractorOutput, payload)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp SubmitExtractorOutputResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.extractorCalls != 1 || core.traceCalls != 1 || core.captureCalls != 0 || core.ingestCalls != 0 {
		t.Fatalf("core calls = extractor %d trace %d capture %d ingest %d, want 1/1/0/0",
			core.extractorCalls, core.traceCalls, core.captureCalls, core.ingestCalls)
	}
	if core.extractorInput.RequestID != req.RequestID || core.extractorInput.SourceSnapshotID != req.SourceSnapshotID || core.extractorInput.ExtractionViewID != req.ExtractionViewID {
		t.Fatalf("extractor input not forwarded: %+v", core.extractorInput)
	}
	if !reflect.DeepEqual(core.extractorInput.ExtractorDefinition, req.ExtractorDefinition) {
		t.Fatalf("extractor definition not forwarded: %#v", core.extractorInput.ExtractorDefinition)
	}
	if !reflect.DeepEqual(core.extractorInput.Output, req.ExtractorOutput) {
		t.Fatalf("extractor output not forwarded: %#v", core.extractorInput.Output)
	}
	if resp.SourceSnapshotID != "srcsnap:1" || resp.ExtractionViewID != "view:1" || resp.ExtractionAttemptID != "attempt:1" || resp.ProposalOccurrenceID != "occ:1" || resp.ProposalFingerprint != "fp:1" {
		t.Fatalf("unexpected response IDs: %+v", resp)
	}
	if resp.Status != "pending" {
		t.Fatalf("status = %q, want pending", resp.Status)
	}
}

func TestCallToolGetsExtractorInputThroughCore(t *testing.T) {
	core := &fakeCore{
		buildResult: evidenceingestion.ExtractorInput{
			SourceSnapshotID:    "srcsnap:1",
			ExtractionViewID:    "view:1",
			SourceSystem:        evidenceingestion.SourceSystemManualText,
			SourceID:            "policy",
			SourceVersion:       "v1",
			RawContentHash:      "sha256:raw",
			Renderer:            evidenceingestion.RendererRef{Name: evidenceingestion.RendererManualTextIdentity, Version: evidenceingestion.RendererManualTextIdentityVersion},
			RenderedContentHash: "sha256:rendered",
			RenderedText:        "Refunds must be completed within 7 days.\n",
			SpanCatalogVersion:  evidenceingestion.SpanCatalogManualLineV1,
			Spans: []evidenceingestion.ExtractorInputSpan{{
				SpanID:         "span:S1",
				Text:           "Refunds must be completed within 7 days.",
				StartByte:      0,
				EndByte:        42,
				DisplayLine:    1,
				QuotedTextHash: "sha256:quote",
			}},
		},
	}
	server := newServer(core)

	data, err := server.CallTool(context.Background(), ToolGetExtractorInput, []byte(`{"extraction_view_id":"view:1"}`))
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	var resp GetExtractorInputResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if core.buildCalls != 1 || core.buildExtractionViewID != "view:1" {
		t.Fatalf("build call = %d/%q, want 1/view:1", core.buildCalls, core.buildExtractionViewID)
	}
	if core.captureCalls != 0 || core.ingestCalls != 0 || core.extractorCalls != 0 || core.trustedCalls != 0 || core.traceCalls != 0 {
		t.Fatalf("unexpected write/query calls: capture %d ingest %d extractor %d trusted %d trace %d",
			core.captureCalls, core.ingestCalls, core.extractorCalls, core.trustedCalls, core.traceCalls)
	}
	if !reflect.DeepEqual(resp, core.buildResult) {
		t.Fatalf("extractor input response = %#v, want %#v", resp, core.buildResult)
	}
}

func TestCallToolErrorsAreStable(t *testing.T) {
	t.Run("unknown tool", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), "query_evidence", []byte(`{}`))
		assertToolError(t, err, toolErrorUnknownTool)
	})

	t.Run("invalid request", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolSubmitManualEvidence, []byte(`{`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("text source unknown field", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolSubmitTextSource, []byte(`{"source_id":"s","source_version":"v1","raw_text":"x","extractor_output":{}}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("expired claim discovery caller time rejected", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolListExpiredGitRepositoryExtractionWorkClaims, []byte(`{"repo_id":"ahe-wrap","extractor_name":"repository-go-parser-code-fact","limit":1,"as_of":"2026-07-21T12:00:00Z"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("expired execution discovery policy fields rejected", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolListExpiredGitRepositoryExtractionWorkExecutions, []byte(`{"limit":1,"as_of":"2026-07-22T10:00:00Z","repo_id":"ahe-wrap","repair_actor_id":"controller-1"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("manual evidence attempt number rejected", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolSubmitManualEvidence, []byte(`{"source_id":"s","source_version":"v1","raw_text":"x","request_id":"r","attempt_number":99,"extractor_output":{"proposals":[]}}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("extractor output raw text rejected", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolSubmitExtractorOutput, []byte(`{"request_id":"r","source_snapshot_id":"srcsnap:1","extraction_view_id":"view:1","raw_text":"x","extractor_output":{"proposals":[]}}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("extractor output attempt number rejected", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolSubmitExtractorOutput, []byte(`{"request_id":"r","source_snapshot_id":"srcsnap:1","extraction_view_id":"view:1","attempt_number":99,"extractor_output":{"proposals":[]}}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("extractor input rejects non-grounded fields", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolGetExtractorInput, []byte(`{"extraction_view_id":"view:1","raw_text":"x","extractor_output":{"proposals":[]},"attempt_number":99}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("ollama extractor rejects output fixture", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunLocalOllamaExtractor, []byte(`{"request_id":"r","extraction_view_id":"view:1","model":"gemma4:12b","extractor_output":{"proposals":[]}}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("ollama extractor requires model", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunLocalOllamaExtractor, []byte(`{"request_id":"r","extraction_view_id":"view:1","model":""}`))
		assertToolError(t, err, string(evidenceingestion.ErrorInvalidInput))
	})

	t.Run("go parser extractor requires path", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunGoParserExtractor, []byte(`{"request_id":"r","extraction_view_id":"view:1","repo_id":"repo","commit_sha":"commit","path":""}`))
		assertToolError(t, err, string(evidenceingestion.ErrorInvalidInput))
	})

	t.Run("gopls extractor requires workspace root", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunGoplsExtractor, []byte(`{"request_id":"r","extraction_view_id":"view:1","workspace_root":"","repo_id":"repo","commit_sha":"commit","path":"service.go"}`))
		assertToolError(t, err, string(evidenceingestion.ErrorInvalidInput))
	})

	t.Run("gopls extractor rejects caller selected binary", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunGoplsExtractor, []byte(`{"request_id":"r","extraction_view_id":"view:1","workspace_root":"/repo","repo_id":"repo","commit_sha":"commit","path":"service.go","binary_path":"/tmp/gopls"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("repository gopls extractor rejects caller selected binary", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunRepositoryGoplsExtractor, []byte(`{"request_id":"r","repository_snapshot_id":"repo-snapshot:1","workspace_root":"/repo","binary_path":"/tmp/gopls"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("gopls workspace inventory rejects caller selected binary", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolInspectGoplsWorkspace, []byte(`{"workspace_root":"/repo","repo_id":"repo","commit_sha":"commit","binary_path":"/tmp/gopls"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})
	t.Run("git change binary path", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolInspectGitRepositoryChange, []byte(`{"workspace_root":"/repo","repo_id":"repo","git_binary_path":"/tmp/git"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("git observation binary path", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolObserveGitRepositoryChange, []byte(`{"request_id":"r","workspace_root":"/repo","repo_id":"repo","stability_window_milliseconds":1000,"git_binary_path":"/tmp/git"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("git observation stability window", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolObserveGitRepositoryChange, []byte(`{"request_id":"r","workspace_root":"/repo","repo_id":"repo","stability_window_milliseconds":0}`))
		assertToolError(t, err, string(evidenceingestion.ErrorInvalidInput))
	})

	t.Run("git repository capture rejects caller selected binary", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolCaptureGitRepositorySnapshot, []byte(`{"request_id":"r","workspace_root":"/repo","repo_id":"repo","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","git_binary_path":"/tmp/git"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("admission rejects unknown fields", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolAdmitPendingProposal, []byte(`{"proposal_occurrence_id":"occ:1","canonical_ref":"canon-node:1"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("proposal disposition rejects caller-selected canonical state", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(
			context.Background(),
			ToolRecordPendingProposalDisposition,
			[]byte(`{"proposal_occurrence_id":"occ:1","outcome":"rejected","decision_by":"reviewer","decision_reason":"not source backed","canonical_ref":"canon-node:1"}`),
		)
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("generation activation rejects caller-selected stream fields", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(
			context.Background(),
			ToolActivateRepositorySourceGeneration,
			[]byte(`{"request_id":"activate-1","source_generation_id":"generation:1","repo_id":"ahe-wrap","extractor_name":"repository-go-parser-code-fact"}`),
		)
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("generation listing rejects caller-selected lifecycle fields", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(
			context.Background(),
			ToolListRepositorySourceGenerations,
			[]byte(`{"repo_id":"ahe-wrap","extractor_name":"repository-go-parser-code-fact","limit":10,"lifecycle_scope":"historical"}`),
		)
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("work execution rejects caller policy", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolExecuteClaimedGitRepositoryExtractionWork, []byte(`{"request_id":"execute-1","workspace_root":"/repo","work_item_id":"repo-work:1","claim_id":"work-claim:1","worker_id":"worker-1","retry_failed_attempt":true}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("worker tick rejects caller policy", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunGitRepositoryExtractionWorkerTick, []byte(`{"request_id":"tick-1","workspace_root":"/repo","repo_id":"ahe-wrap","extractor_name":"repository-go-parser-code-fact","worker_id":"worker-1","lease_duration_milliseconds":60000,"retry_failed_attempt":true}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("failure policy rejects caller decision controls", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolClassifyFailedGitRepositoryExtractionWork, []byte(`{"request_id":"policy-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","decision_actor_id":"controller-1","failure_class":"runner_invocation_failed","max_attempts":9,"backoff_milliseconds":1}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("due retry decision discovery rejects caller policy", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolListDueGitRepositoryExtractionWorkRetryDecisions, []byte(`{"limit":1,"as_of":"2026-07-22T12:00:00Z","repo_id":"ahe-wrap","consumer_actor_id":"controller-1","disposition":"requeued"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("retry decision consumption rejects caller time and disposition", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolConsumeDueGitRepositoryExtractionWorkRetryDecision, []byte(`{"request_id":"consume-1","failure_policy_request_id":"policy-1","work_item_id":"repo-work:1","claim_id":"work-claim:1","consumer_actor_id":"controller-1","consumed_at":"2026-07-21T12:00:00Z","disposition":"requeued"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("retry controller tick rejects caller policy and execution controls", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunDueGitRepositoryExtractionWorkRetryControllerTick, []byte(`{"request_id":"retry-controller-tick-1","limit":1,"consumer_actor_id":"controller-1","as_of":"2026-07-22T12:00:00Z","failure_policy_request_id":"policy-1","disposition":"requeued","workspace_root":"/repo"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("expired maintenance tick rejects caller selection and execution controls", func(t *testing.T) {
		server := newServer(&fakeCore{})
		_, err := server.CallTool(context.Background(), ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick, []byte(`{"request_id":"expired-maintenance-tick-1","limit":1,"maintenance_actor_id":"controller-1","as_of":"2026-07-22T12:00:00Z","repo_id":"ahe-wrap","work_item_id":"repo-work:1","claim_id":"work-claim:1","execution_request_id":"execution-1","worker_id":"worker-1","transition_kind":"recovery","workspace_root":"/repo"}`))
		assertToolError(t, err, toolErrorInvalidRequest)
	})

	t.Run("extractor input invalid record id", func(t *testing.T) {
		server := newServer(&fakeCore{
			buildErr: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorInvalidRecordID, Message: "bad view id"},
		})
		_, err := server.CallTool(context.Background(), ToolGetExtractorInput, []byte(`{"extraction_view_id":"bad-id"}`))
		assertToolError(t, err, string(evidenceingestion.ErrorInvalidRecordID))
	})

	t.Run("text source idempotency conflict", func(t *testing.T) {
		server := newServer(&fakeCore{
			captureErr: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorIdempotencyKeyReused, Message: "request reused"},
		})
		payload, err := json.Marshal(testTextSourceRequest("source-conflict"))
		if err != nil {
			t.Fatalf("Marshal request: %v", err)
		}
		_, err = server.CallTool(context.Background(), ToolSubmitTextSource, payload)
		assertToolError(t, err, string(evidenceingestion.ErrorIdempotencyKeyReused))
	})

	t.Run("admission state conflict", func(t *testing.T) {
		server := newServer(&fakeCore{
			admitErr: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorAdmissionStateConflict, Message: "proposal is not pending"},
		})
		_, err := server.CallTool(context.Background(), ToolAdmitPendingProposal, []byte(`{"proposal_occurrence_id":"occ:1"}`))
		assertToolError(t, err, string(evidenceingestion.ErrorAdmissionStateConflict))
	})

	t.Run("proposal disposition state conflict", func(t *testing.T) {
		server := newServer(&fakeCore{
			dispositionErr: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorAdmissionStateConflict, Message: "proposal is not pending"},
		})
		_, err := server.CallTool(
			context.Background(),
			ToolRecordPendingProposalDisposition,
			[]byte(`{"proposal_occurrence_id":"occ:1","outcome":"rejected","decision_by":"reviewer","decision_reason":"not source backed"}`),
		)
		assertToolError(t, err, string(evidenceingestion.ErrorAdmissionStateConflict))
	})

	for _, tc := range []struct {
		name string
		kind evidenceingestion.ErrorKind
	}{
		{name: "invalid utf8", kind: evidenceingestion.ErrorInvalidUTF8},
		{name: "unknown span", kind: evidenceingestion.ErrorUnknownSpan},
		{name: "hash mismatch", kind: evidenceingestion.ErrorQuotedHashMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newServer(&fakeCore{
				ingestErr: &evidenceingestion.DomainError{Kind: tc.kind, Message: "fixture failure"},
			})
			payload, err := json.Marshal(testRequest(tc.name))
			if err != nil {
				t.Fatalf("Marshal request: %v", err)
			}
			_, err = server.CallTool(context.Background(), ToolSubmitManualEvidence, payload)
			assertToolError(t, err, string(tc.kind))
		})
	}
}

func TestSubmitTextSourceMapsInvalidUTF8(t *testing.T) {
	server := newServer(&fakeCore{
		captureErr: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorInvalidUTF8, Message: "manual text source is not valid UTF-8"},
	})
	req := testTextSourceRequest("invalid-utf8")
	req.RawText = string([]byte{0xff})

	_, err := server.SubmitTextSource(context.Background(), req)
	assertToolError(t, err, string(evidenceingestion.ErrorInvalidUTF8))
}

func TestSubmitManualEvidenceMapsInvalidUTF8(t *testing.T) {
	server := newServer(&fakeCore{
		ingestErr: &evidenceingestion.DomainError{Kind: evidenceingestion.ErrorInvalidUTF8, Message: "manual text source is not valid UTF-8"},
	})
	req := testRequest("invalid-utf8")
	req.RawText = string([]byte{0xff})

	_, err := server.SubmitManualEvidence(context.Background(), req)
	assertToolError(t, err, string(evidenceingestion.ErrorInvalidUTF8))
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

func testRequest(requestID string) SubmitManualEvidenceRequest {
	return SubmitManualEvidenceRequest{
		SourceID:      "fixture-refund-policy",
		SourceVersion: "v1",
		RawText: "Refunds must be completed within 7 days.\n" +
			"This rule applies only to overseas orders.\n",
		OriginMetadata: map[string]string{"fixture": "manual_refund_policy"},
		RequestID:      requestID,
		ExtractorOutput: evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{
			ProposalLocalID: "stmt-1",
			StatementText:   "Refunds must be completed within 7 days.",
			EvidenceRefs:    []string{"span:S1"},
		}}},
	}
}

func testTextSourceRequest(requestID string) SubmitTextSourceRequest {
	return SubmitTextSourceRequest{
		SourceID:      "fixture-refund-policy",
		SourceVersion: "v1",
		RawText: "Refunds must be completed within 7 days.\n" +
			"This rule applies only to overseas orders.\n",
		OriginMetadata: map[string]string{"fixture": "manual_refund_policy"},
		RequestID:      requestID,
	}
}

func testExtractorOutputRequest(requestID string) SubmitExtractorOutputRequest {
	return SubmitExtractorOutputRequest{
		RequestID:        requestID,
		SourceSnapshotID: "srcsnap:1",
		ExtractionViewID: "view:1",
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{
			Name:    "unit-test-extractor",
			Version: "v1",
			Config:  map[string]string{"fixture": "server-test"},
		},
		ExtractorOutput: testRequest(requestID).ExtractorOutput,
	}
}

type fakeCore struct {
	admitCalls                    int
	dispositionCalls              int
	activationCalls               int
	generationListCalls           int
	buildCalls                    int
	captureCalls                  int
	ingestCalls                   int
	extractorCalls                int
	trustedCalls                  int
	traceCalls                    int
	repositoryCalls               int
	repositoryParserCalls         int
	repositoryGoplsCalls          int
	buildRepositorySnapshotID     string
	buildExtractionViewID         string
	traceOccurrenceID             string
	admissionInput                evidenceingestion.AdmissionInput
	dispositionInput              evidenceingestion.ProposalDispositionInput
	activationInput               evidenceingestion.RepositorySourceGenerationActivationInput
	generationListInput           evidenceingestion.RepositorySourceGenerationListInput
	input                         evidenceingestion.ManualTextInput
	extractorInput                evidenceingestion.ExtractorOutputInput
	trustedRequest                evidenceingestion.TrustedExtractorRequest
	repositoryConfig              evidenceingestion.GitRepositorySnapshotConfig
	repositoryParserRequest       evidenceingestion.RepositoryGoParserRequest
	repositoryGoplsRequest        evidenceingestion.RepositoryGoplsRequest
	fixture                       evidenceingestion.FrozenExtractorOutput
	admitResult                   evidenceingestion.AdmissionResult
	dispositionResult             evidenceingestion.ProposalDispositionResult
	activationResult              evidenceingestion.RepositorySourceGenerationActivationResult
	generationListResult          []evidenceingestion.RepositorySourceGenerationStatus
	buildResult                   evidenceingestion.ExtractorInput
	buildRepositoryResult         evidenceingestion.RepositoryExtractorInput
	captureResult                 evidenceingestion.SourceIntakeResult
	ingestResult                  evidenceingestion.IngestResult
	extractorResult               evidenceingestion.IngestResult
	trustedResult                 evidenceingestion.IngestResult
	traceResult                   evidenceingestion.ProposalQueryResult
	repositoryResult              evidenceingestion.RepositorySnapshotCaptureResult
	repositoryParserResult        evidenceingestion.RepositoryIngestResult
	repositoryGoplsResult         evidenceingestion.RepositoryIngestResult
	repositoryWorkScheduleResult  evidenceingestion.RepositoryExtractionWorkScheduleResult
	repositoryWorkClaimResult     evidenceingestion.RepositoryExtractionWorkClaimResult
	repositoryWorkRenewResult     evidenceingestion.RepositoryExtractionWorkLeaseRenewalResult
	repositoryWorkExecutionResult evidenceingestion.RepositoryExtractionWorkExecutionResult
	repositoryWorkerTickResult    evidenceingestion.RepositoryExtractionWorkerTickResult
	expiredClaims                 []evidenceingestion.RepositoryExtractionWorkExpiredClaim
	expiredExecutions             []evidenceingestion.RepositoryExtractionWorkExpiredExecution
	repositoryWorkFinishResult    evidenceingestion.RepositoryExtractionWorkFinishResult
	repositoryWorkRecoveryResult  evidenceingestion.RepositoryExtractionWorkRecoveryResult
	executionRepairResult         evidenceingestion.RepositoryExtractionWorkExecutionRepairResult
	repositoryWorkRetryResult     evidenceingestion.RepositoryExtractionWorkRetryResult
	failurePolicyResult           evidenceingestion.RepositoryExtractionWorkFailurePolicyResult
	dueRetryDecisions             []evidenceingestion.RepositoryExtractionWorkDueRetryDecision
	retryDecisionResult           evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult
	retryControllerTickResult     evidenceingestion.RepositoryExtractionWorkRetryControllerTickResult
	expiredMaintenanceTickResult  evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickResult
	gitChangeResult               evidenceingestion.GitRepositoryChangeInspection
	gitObservationResult          evidenceingestion.GitRepositoryChangeObservationResult
	admitErr                      error
	dispositionErr                error
	activationErr                 error
	generationListErr             error
	buildErr                      error
	captureErr                    error
	ingestErr                     error
	extractorErr                  error
	trustedErr                    error
	traceErr                      error
	repositoryErr                 error
	gitChangeConfig               evidenceingestion.GitRepositoryChangeConfig
	gitChangeCalls                int
	gitObservationConfig          evidenceingestion.GitRepositoryChangeObservationConfig
	gitObservationCalls           int
	repositoryWorkScheduleInput   evidenceingestion.RepositoryExtractionWorkScheduleInput
	repositoryWorkScheduleCalls   int
	repositoryWorkClaimInput      evidenceingestion.RepositoryExtractionWorkClaimInput
	repositoryWorkClaimCalls      int
	repositoryWorkRenewInput      evidenceingestion.RepositoryExtractionWorkLeaseRenewalInput
	repositoryWorkRenewCalls      int
	repositoryWorkExecutionInput  evidenceingestion.RepositoryExtractionWorkExecutionInput
	repositoryWorkExecutionCalls  int
	repositoryWorkerTickInput     evidenceingestion.RepositoryExtractionWorkerTickInput
	repositoryWorkerTickCalls     int
	expiredClaimInput             evidenceingestion.RepositoryExtractionWorkExpiredClaimListInput
	expiredClaimCalls             int
	expiredExecutionInput         evidenceingestion.RepositoryExtractionWorkExpiredExecutionListInput
	expiredExecutionCalls         int
	repositoryWorkFinishInput     evidenceingestion.RepositoryExtractionWorkFinishInput
	repositoryWorkFinishCalls     int
	repositoryWorkRecoveryInput   evidenceingestion.RepositoryExtractionWorkRecoveryInput
	repositoryWorkRecoveryCalls   int
	executionRepairInput          evidenceingestion.RepositoryExtractionWorkExecutionRepairInput
	executionRepairCalls          int
	repositoryWorkRetryInput      evidenceingestion.RepositoryExtractionWorkRetryInput
	repositoryWorkRetryCalls      int
	failurePolicyInput            evidenceingestion.RepositoryExtractionWorkFailurePolicyInput
	failurePolicyCalls            int
	dueRetryDecisionInput         evidenceingestion.RepositoryExtractionWorkDueRetryDecisionListInput
	dueRetryDecisionCalls         int
	retryDecisionInput            evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionInput
	retryDecisionCalls            int
	retryControllerTickInput      evidenceingestion.RepositoryExtractionWorkRetryControllerTickInput
	retryControllerTickCalls      int
	expiredMaintenanceTickInput   evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput
	expiredMaintenanceTickCalls   int
}

func (c *fakeCore) AdmitPendingProposal(_ context.Context, input evidenceingestion.AdmissionInput) (evidenceingestion.AdmissionResult, error) {
	c.admitCalls++
	c.admissionInput = input
	if c.admitErr != nil {
		return evidenceingestion.AdmissionResult{}, c.admitErr
	}
	return c.admitResult, nil
}

func (c *fakeCore) RecordPendingProposalDisposition(
	_ context.Context,
	input evidenceingestion.ProposalDispositionInput,
) (evidenceingestion.ProposalDispositionResult, error) {
	c.dispositionCalls++
	c.dispositionInput = input
	if c.dispositionErr != nil {
		return evidenceingestion.ProposalDispositionResult{}, c.dispositionErr
	}
	return c.dispositionResult, nil
}

func (c *fakeCore) ActivateRepositorySourceGeneration(
	_ context.Context,
	input evidenceingestion.RepositorySourceGenerationActivationInput,
) (evidenceingestion.RepositorySourceGenerationActivationResult, error) {
	c.activationCalls++
	c.activationInput = input
	if c.activationErr != nil {
		return evidenceingestion.RepositorySourceGenerationActivationResult{}, c.activationErr
	}
	return c.activationResult, nil
}

func (c *fakeCore) ListRepositorySourceGenerations(
	_ context.Context,
	input evidenceingestion.RepositorySourceGenerationListInput,
) ([]evidenceingestion.RepositorySourceGenerationStatus, error) {
	c.generationListCalls++
	c.generationListInput = input
	if c.generationListErr != nil {
		return nil, c.generationListErr
	}
	return append([]evidenceingestion.RepositorySourceGenerationStatus(nil), c.generationListResult...), nil
}

func (c *fakeCore) BuildExtractorInput(_ context.Context, extractionViewID string) (evidenceingestion.ExtractorInput, error) {
	c.buildCalls++
	c.buildExtractionViewID = extractionViewID
	if c.buildErr != nil {
		return evidenceingestion.ExtractorInput{}, c.buildErr
	}
	return c.buildResult, nil
}

func (c *fakeCore) BuildRepositoryExtractorInput(_ context.Context, repositorySnapshotID string) (evidenceingestion.RepositoryExtractorInput, error) {
	c.buildRepositorySnapshotID = repositorySnapshotID
	return c.buildRepositoryResult, nil
}

func (c *fakeCore) CaptureGitRepositorySnapshot(_ context.Context, config evidenceingestion.GitRepositorySnapshotConfig) (evidenceingestion.RepositorySnapshotCaptureResult, error) {
	c.repositoryCalls++
	c.repositoryConfig = config
	if c.repositoryErr != nil {
		return evidenceingestion.RepositorySnapshotCaptureResult{}, c.repositoryErr
	}
	return c.repositoryResult, nil
}

func (c *fakeCore) InspectGitRepositoryChange(_ context.Context, config evidenceingestion.GitRepositoryChangeConfig) (evidenceingestion.GitRepositoryChangeInspection, error) {
	c.gitChangeCalls++
	c.gitChangeConfig = config
	return c.gitChangeResult, nil
}

func (c *fakeCore) ObserveGitRepositoryChange(_ context.Context, config evidenceingestion.GitRepositoryChangeObservationConfig) (evidenceingestion.GitRepositoryChangeObservationResult, error) {
	c.gitObservationCalls++
	c.gitObservationConfig = config
	return c.gitObservationResult, nil
}

func (c *fakeCore) ScheduleRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkScheduleInput) (evidenceingestion.RepositoryExtractionWorkScheduleResult, error) {
	c.repositoryWorkScheduleCalls++
	c.repositoryWorkScheduleInput = input
	return c.repositoryWorkScheduleResult, nil
}

func (c *fakeCore) ClaimRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkClaimInput) (evidenceingestion.RepositoryExtractionWorkClaimResult, error) {
	c.repositoryWorkClaimCalls++
	c.repositoryWorkClaimInput = input
	return c.repositoryWorkClaimResult, nil
}

func (c *fakeCore) RenewRepositoryExtractionWorkLease(_ context.Context, input evidenceingestion.RepositoryExtractionWorkLeaseRenewalInput) (evidenceingestion.RepositoryExtractionWorkLeaseRenewalResult, error) {
	c.repositoryWorkRenewCalls++
	c.repositoryWorkRenewInput = input
	return c.repositoryWorkRenewResult, nil
}

func (c *fakeCore) ExecuteClaimedRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkExecutionInput) (evidenceingestion.RepositoryExtractionWorkExecutionResult, error) {
	c.repositoryWorkExecutionCalls++
	c.repositoryWorkExecutionInput = input
	return c.repositoryWorkExecutionResult, nil
}

func (c *fakeCore) RunRepositoryExtractionWorkerTick(_ context.Context, input evidenceingestion.RepositoryExtractionWorkerTickInput) (evidenceingestion.RepositoryExtractionWorkerTickResult, error) {
	c.repositoryWorkerTickCalls++
	c.repositoryWorkerTickInput = input
	return c.repositoryWorkerTickResult, nil
}

func (c *fakeCore) ListExpiredRepositoryExtractionWorkClaims(_ context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredClaimListInput) ([]evidenceingestion.RepositoryExtractionWorkExpiredClaim, error) {
	c.expiredClaimCalls++
	c.expiredClaimInput = input
	return c.expiredClaims, nil
}

func (c *fakeCore) ListExpiredRepositoryExtractionWorkExecutions(_ context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredExecutionListInput) ([]evidenceingestion.RepositoryExtractionWorkExpiredExecution, error) {
	c.expiredExecutionCalls++
	c.expiredExecutionInput = input
	return c.expiredExecutions, nil
}

func (c *fakeCore) FinishRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkFinishInput) (evidenceingestion.RepositoryExtractionWorkFinishResult, error) {
	c.repositoryWorkFinishCalls++
	c.repositoryWorkFinishInput = input
	return c.repositoryWorkFinishResult, nil
}

func (c *fakeCore) RecoverExpiredRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkRecoveryInput) (evidenceingestion.RepositoryExtractionWorkRecoveryResult, error) {
	c.repositoryWorkRecoveryCalls++
	c.repositoryWorkRecoveryInput = input
	return c.repositoryWorkRecoveryResult, nil
}

func (c *fakeCore) RepairExpiredRepositoryExtractionWorkExecution(_ context.Context, input evidenceingestion.RepositoryExtractionWorkExecutionRepairInput) (evidenceingestion.RepositoryExtractionWorkExecutionRepairResult, error) {
	c.executionRepairCalls++
	c.executionRepairInput = input
	return c.executionRepairResult, nil
}

func (c *fakeCore) RetryFailedRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkRetryInput) (evidenceingestion.RepositoryExtractionWorkRetryResult, error) {
	c.repositoryWorkRetryCalls++
	c.repositoryWorkRetryInput = input
	return c.repositoryWorkRetryResult, nil
}

func (c *fakeCore) ClassifyFailedRepositoryExtractionWork(_ context.Context, input evidenceingestion.RepositoryExtractionWorkFailurePolicyInput) (evidenceingestion.RepositoryExtractionWorkFailurePolicyResult, error) {
	c.failurePolicyCalls++
	c.failurePolicyInput = input
	return c.failurePolicyResult, nil
}

func (c *fakeCore) ListDueRepositoryExtractionWorkRetryDecisions(_ context.Context, input evidenceingestion.RepositoryExtractionWorkDueRetryDecisionListInput) ([]evidenceingestion.RepositoryExtractionWorkDueRetryDecision, error) {
	c.dueRetryDecisionCalls++
	c.dueRetryDecisionInput = input
	return c.dueRetryDecisions, nil
}

func (c *fakeCore) ConsumeDueRepositoryExtractionWorkRetryDecision(_ context.Context, input evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionInput) (evidenceingestion.RepositoryExtractionWorkRetryDecisionConsumptionResult, error) {
	c.retryDecisionCalls++
	c.retryDecisionInput = input
	return c.retryDecisionResult, nil
}

func (c *fakeCore) RunRepositoryExtractionWorkRetryControllerTick(_ context.Context, input evidenceingestion.RepositoryExtractionWorkRetryControllerTickInput) (evidenceingestion.RepositoryExtractionWorkRetryControllerTickResult, error) {
	c.retryControllerTickCalls++
	c.retryControllerTickInput = input
	return c.retryControllerTickResult, nil
}

func (c *fakeCore) RunExpiredRepositoryExtractionWorkMaintenanceTick(_ context.Context, input evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput) (evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickResult, error) {
	c.expiredMaintenanceTickCalls++
	c.expiredMaintenanceTickInput = input
	return c.expiredMaintenanceTickResult, nil
}

func (c *fakeCore) CreateRepositoryExtractionRun(_ context.Context, request evidenceingestion.RepositoryExtractionRunRequest) (evidenceingestion.RepositoryExtractionRunResult, error) {
	return evidenceingestion.RepositoryExtractionRunResult{ExtractionRun: evidenceingestion.ExtractionRun{ID: "run:test", RequestID: request.RequestID, RepositorySnapshotID: request.RepositorySnapshotID}}, nil
}

func (c *fakeCore) CaptureManualSource(_ context.Context, input evidenceingestion.ManualTextInput) (evidenceingestion.SourceIntakeResult, error) {
	c.captureCalls++
	c.input = input
	if c.captureErr != nil {
		return evidenceingestion.SourceIntakeResult{}, c.captureErr
	}
	return c.captureResult, nil
}

func (c *fakeCore) IngestManualText(_ context.Context, input evidenceingestion.ManualTextInput, fixture evidenceingestion.FrozenExtractorOutput) (evidenceingestion.IngestResult, error) {
	c.ingestCalls++
	c.input = input
	c.fixture = fixture
	if c.ingestErr != nil {
		return evidenceingestion.IngestResult{}, c.ingestErr
	}
	return c.ingestResult, nil
}

func (c *fakeCore) RunTrustedExtractor(_ context.Context, request evidenceingestion.TrustedExtractorRequest, _ evidenceingestion.ExtractorRunner) (evidenceingestion.IngestResult, error) {
	c.trustedCalls++
	c.trustedRequest = request
	if c.trustedErr != nil {
		return evidenceingestion.IngestResult{}, c.trustedErr
	}
	return c.trustedResult, nil
}

func (c *fakeCore) RunRepositoryGoParserExtractor(_ context.Context, request evidenceingestion.RepositoryGoParserRequest) (evidenceingestion.RepositoryIngestResult, error) {
	c.repositoryParserCalls++
	c.repositoryParserRequest = request
	return c.repositoryParserResult, nil
}

func (c *fakeCore) RunRepositoryGoplsExtractor(_ context.Context, request evidenceingestion.RepositoryGoplsRequest) (evidenceingestion.RepositoryIngestResult, error) {
	c.repositoryGoplsCalls++
	c.repositoryGoplsRequest = request
	return c.repositoryGoplsResult, nil
}

func (c *fakeCore) SubmitExtractorOutput(_ context.Context, input evidenceingestion.ExtractorOutputInput) (evidenceingestion.IngestResult, error) {
	c.extractorCalls++
	c.extractorInput = input
	if c.extractorErr != nil {
		return evidenceingestion.IngestResult{}, c.extractorErr
	}
	return c.extractorResult, nil
}

func (c *fakeCore) TraceProposalProvenance(_ context.Context, occurrenceID string) (evidenceingestion.ProposalQueryResult, error) {
	c.traceCalls++
	c.traceOccurrenceID = occurrenceID
	if c.traceErr != nil {
		return evidenceingestion.ProposalQueryResult{}, c.traceErr
	}
	return c.traceResult, nil
}
