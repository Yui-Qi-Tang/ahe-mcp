//go:build integration

package detective

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationOneShotGitGoContinuesUntilStableAndReusesInertGeneration(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, root, commitSHA := registerOneShotGitWorkspace(t, ctx, pool, "lifecycle")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-lifecycle", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 3,
	}

	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, first, 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_change_observations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 0)

	waitForOneShotGitStability(t)
	completed, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("stable RunOneShotOrchestration() error = %v", err)
	}
	if completed.Replayed || !completed.SourceInvoked || completed.Run.Status != OrchestrationRunStatusCompleted ||
		completed.Run.StopReason != OrchestrationStopReasonCoverageSatisfied || completed.Step == nil || completed.Step.Number != 2 {
		t.Fatalf("completed Git result = %+v", completed)
	}
	coverage := completed.Step.Coverage
	if coverage == nil || coverage.UnitKind != OrchestrationCoverageUnitRepositoryFiles ||
		coverage.AttemptedUnitCount != 1 || coverage.CoveredUnitCount != 1 || !coverage.Complete {
		t.Fatalf("completed Git coverage = %+v", coverage)
	}
	if completed.Step.InputAuthorityKind != OrchestrationInputAuthoritySourceToken || completed.Step.InputAuthorityRef == "" {
		t.Fatalf("completed Git authority = %q/%q", completed.Step.InputAuthorityKind, completed.Step.InputAuthorityRef)
	}

	var generationID, repoID, extractorName, generationCommit, snapshotID string
	var selectedFileCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			g.source_generation_id,
			g.repo_id,
			g.extractor_name,
			g.commit_sha,
			g.repository_snapshot_id,
			rs.selected_file_count
		FROM repository_source_generations g
		JOIN repository_snapshots rs ON rs.repository_snapshot_id = g.repository_snapshot_id
	`).Scan(&generationID, &repoID, &extractorName, &generationCommit, &snapshotID, &selectedFileCount); err != nil {
		t.Fatalf("read completed Git generation: %v", err)
	}
	if generationID == "" || snapshotID == "" || repoID != oneShotGitGoRepositoryID(workspace.Sources[0]) ||
		extractorName != evidenceingestion.ExtractorRepositoryGoParserCodeFact || generationCommit != commitSHA || selectedFileCount != 1 {
		t.Fatalf("completed Git generation = %q %q %q %q %q files=%d", generationID, repoID, extractorName, generationCommit, snapshotID, selectedFileCount)
	}
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_heads", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_generation_activation_requests", 0)

	reusedInput := input
	reusedInput.RequestID = "one-shot-git-reuse"
	reusedInput.MaxSteps = 1
	reused, err := RunOneShotOrchestration(ctx, pool, reusedInput)
	if err != nil {
		t.Fatalf("reused RunOneShotOrchestration() error = %v", err)
	}
	if reused.Run.Status != OrchestrationRunStatusCompleted || reused.Step == nil || reused.Step.Coverage == nil ||
		reused.Step.Coverage.CoveredUnitCount != 1 || !reused.Step.Coverage.Complete {
		t.Fatalf("reused Git result = %+v", reused)
	}
	var claimed bool
	if err := pool.QueryRow(ctx, `
		SELECT claimed
		FROM repository_extraction_worker_tick_requests
		WHERE request_id = $1
	`, oneShotChildRequestID(reusedInput.RequestID, "git-worker", 1)).Scan(&claimed); err != nil {
		t.Fatalf("read reused worker tick: %v", err)
	}
	if claimed {
		t.Fatal("reused successful work unexpectedly claimed new work")
	}
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_heads", 0)

	movedRoot := root + "-moved"
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("move completed Git workspace: %v", err)
	}
	replay, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("terminal RunOneShotOrchestration() replay error = %v", err)
	}
	if !replay.Replayed || replay.SourceInvoked || !reflect.DeepEqual(replay.Run, completed.Run) {
		t.Fatalf("terminal Git replay = %+v, want run %+v", replay, completed.Run)
	}
}

func TestIntegrationOneShotGitGoResumesPlannedObservation(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, root, _ := registerOneShotGitWorkspace(t, ctx, pool, "planned")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-planned", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 2,
	}
	request, err := prepareOneShotOrchestration(input)
	if err != nil {
		t.Fatalf("prepareOneShotOrchestration() error = %v", err)
	}
	request, err = bindOneShotSourcePolicy(request, workspace.Sources[0])
	if err != nil {
		t.Fatalf("bindOneShotSourcePolicy() error = %v", err)
	}
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: request.requestID, WorkspaceID: request.workspaceID,
		MaxSteps: request.maxSteps, ScopeHash: request.scopeHash,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	inspection, err := evidenceingestion.InspectGitRepositoryChange(ctx, evidenceingestion.GitRepositoryChangeConfig{
		WorkspaceRoot: root, RepoID: oneShotGitGoRepositoryID(workspace.Sources[0]),
	})
	if err != nil {
		t.Fatalf("InspectGitRepositoryChange() error = %v", err)
	}
	planned, err := planOneShotStep(
		ctx, pool, request, started.Run, workspace.Sources[0],
		OrchestrationInputAuthoritySourceToken, inspection.ChangeToken,
	)
	if err != nil {
		t.Fatalf("planOneShotStep() error = %v", err)
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("resumed RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, result, 1)
	if result.Step == nil || result.Step.ID != planned.ID {
		t.Fatalf("resumed Git step = %+v, want %s", result.Step, planned.ID)
	}
	assertDetectiveTableCount(t, ctx, pool, "repository_change_observations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 0)
}

func TestIntegrationConcurrentOneShotGitGoStableRefreshConverges(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "concurrent")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-concurrent", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 3,
	}
	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, first, 1)
	waitForOneShotGitStability(t)

	type outcome struct {
		result OneShotOrchestrationResult
		err    error
	}
	const concurrentRefreshes = 16
	outcomes := make(chan outcome, concurrentRefreshes)
	for range concurrentRefreshes {
		go func() {
			result, err := RunOneShotOrchestration(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	for range concurrentRefreshes {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent RunOneShotOrchestration() error = %v", got.err)
		}
		if got.result.Run.Status != OrchestrationRunStatusCompleted || len(got.result.Run.Steps) != 2 {
			var stable bool
			var workStatus string
			var workerRequests, claimedWorkerRequests int
			var workerRequestIDs string
			if err := pool.QueryRow(ctx, `
				SELECT stable
				FROM repository_change_observation_requests
				WHERE request_id = $1
			`, oneShotChildRequestID(input.RequestID, "git-observe", 2)).Scan(&stable); err != nil {
				t.Fatalf("read concurrent observation diagnostic: %v", err)
			}
			if err := pool.QueryRow(ctx, `
				SELECT status
				FROM repository_extraction_work_items
				LIMIT 1
			`).Scan(&workStatus); err != nil {
				t.Fatalf("read concurrent work diagnostic: %v", err)
			}
			if err := pool.QueryRow(ctx, `
				SELECT
					COUNT(*),
					COUNT(*) FILTER (WHERE claimed),
					COALESCE(string_agg(request_id, ',' ORDER BY request_id), '')
				FROM repository_extraction_worker_tick_requests
			`).Scan(&workerRequests, &claimedWorkerRequests, &workerRequestIDs); err != nil {
				t.Fatalf("read concurrent worker diagnostic: %v", err)
			}
			t.Fatalf(
				"concurrent Git result = %+v; persisted stable=%t work_status=%s worker_requests=%d claimed_worker_requests=%d worker_request_ids=%s",
				got.result,
				stable,
				workStatus,
				workerRequests,
				claimedWorkerRequests,
				workerRequestIDs,
			)
		}
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 2)
	assertDetectiveTableCount(t, ctx, pool, "repository_change_observations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_heads", 0)
}

func TestIntegrationOneShotGitGoDirtyRepositoryExhaustsBudgetWithoutWork(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, root, _ := registerOneShotGitWorkspace(t, ctx, pool, "dirty")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Dirty() {}\n"), 0o600); err != nil {
		t.Fatalf("dirty Git source: %v", err)
	}
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-dirty", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 2,
	}
	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first dirty RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, first, 1)
	waitForOneShotGitStability(t)

	stopped, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("stable dirty RunOneShotOrchestration() error = %v", err)
	}
	if stopped.Run.Status != OrchestrationRunStatusStopped || stopped.Run.StopReason != OrchestrationStopReasonBudgetExhausted ||
		stopped.Step == nil || stopped.Step.Number != 2 || stopped.Step.Coverage == nil || stopped.Step.Coverage.Complete {
		t.Fatalf("dirty Git result = %+v", stopped)
	}
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_heads", 0)
}

func TestIntegrationOneShotGitGoNoWorkContinuesWhileExactWorkRuns(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, root, _ := registerOneShotGitWorkspace(t, ctx, pool, "no-work")
	source := workspace.Sources[0]
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-no-work", WorkspaceID: workspace.ID,
		SourceBindingID: source.ID, MaxSteps: 3,
	}
	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, first, 1)
	waitForOneShotGitStability(t)

	scheduled, claimed := claimStableOneShotGitWork(t, ctx, pool, source, root, "no-work")
	if !claimed.Claimed || claimed.Work == nil || claimed.Work.WorkItemID != scheduled.Work.WorkItemID {
		t.Fatalf("external claim = %+v, scheduled = %+v", claimed, scheduled)
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("no-work RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, result, 2)
	var controllerClaimed bool
	if err := pool.QueryRow(ctx, `
		SELECT claimed
		FROM repository_extraction_worker_tick_requests
		WHERE request_id = $1
	`, oneShotChildRequestID(input.RequestID, "git-worker", 2)).Scan(&controllerClaimed); err != nil {
		t.Fatalf("read no-work worker tick: %v", err)
	}
	if controllerClaimed {
		t.Fatal("controller worker tick claimed work already owned by another worker")
	}
	assertDetectiveTableCount(t, ctx, pool, "repository_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 0)
}

func TestIntegrationOneShotGitGoTerminalFailedWorkFailsClosed(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, root, _ := registerOneShotGitWorkspace(t, ctx, pool, "failed-work")
	source := workspace.Sources[0]
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-failed-work", WorkspaceID: workspace.ID,
		SourceBindingID: source.ID, MaxSteps: 3,
	}
	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, first, 1)
	waitForOneShotGitStability(t)

	scheduled, claimed := claimStableOneShotGitWork(t, ctx, pool, source, root, "failed-work")
	if _, err := evidenceingestion.FinishRepositoryExtractionWork(ctx, pool, evidenceingestion.RepositoryExtractionWorkFinishInput{
		RequestID:  "one-shot-git-failed-work-external-finish",
		WorkItemID: scheduled.Work.WorkItemID, ClaimID: claimed.ClaimID, WorkerID: claimed.WorkerID,
		Outcome:      evidenceingestion.RepositoryExtractionWorkOutcomeFailed,
		FailureClass: "fixture_failure", FailureMessage: "intentional terminal failure",
	}); err != nil {
		t.Fatalf("external FinishRepositoryExtractionWork() error = %v", err)
	}

	result, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("failed-work RunOneShotOrchestration() error = %v", err)
	}
	if !result.SourceInvoked || result.Run.Status != OrchestrationRunStatusFailed ||
		result.Run.StopReason != OrchestrationStopReasonSourceFailed || result.Step == nil || result.Step.Number != 2 ||
		result.Step.Coverage == nil || result.Step.Coverage.Complete {
		t.Fatalf("failed-work Git result = %+v", result)
	}
	assertDetectiveTableCount(t, ctx, pool, "repository_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 0)
}

func TestIntegrationOneShotGitGoResumesSameStepAfterExpiredExecutionRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX Git wrapper")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "execution-repair")
	input := OneShotOrchestrationInput{
		RequestID: "one-shot-git-execution-repair", WorkspaceID: workspace.ID,
		SourceBindingID: workspace.Sources[0].ID, MaxSteps: 2,
	}

	first, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunOneShotOrchestration() error = %v", err)
	}
	assertWaitingOneShotGitResult(t, first, 1)
	waitForOneShotGitStability(t)

	binDir := t.TempDir()
	wrapperPath := filepath.Join(binDir, "git")
	wrapper := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "cat-file" ]; then
    echo "intentional cat-file failure" >&2
    exit 91
  fi
done
exec "$AHE_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write Git failure wrapper: %v", err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("AHE_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+originalPath)
	if _, err := RunOneShotOrchestration(ctx, pool, input); err == nil {
		t.Fatal("failed execution RunOneShotOrchestration() error = nil")
	}
	t.Setenv("PATH", originalPath)

	var workItemID, claimID string
	if err := pool.QueryRow(ctx, `
		SELECT work_item_id, claim_id
		FROM repository_extraction_work_claim_attempts
		WHERE attempt_number = 1 AND status = 'running'
	`).Scan(&workItemID, &claimID); err != nil {
		t.Fatalf("read interrupted claim attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE repository_extraction_work_claim_attempts
		SET lease_expires_at = claimed_at + INTERVAL '1 millisecond'
		WHERE claim_id = $1 AND status = 'running'
	`, claimID); err != nil {
		t.Fatalf("expire interrupted claim lease: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	repaired, err := evidenceingestion.RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pool, evidenceingestion.RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "one-shot-git-execution-repair-maintenance",
		Limit:              1,
		MaintenanceActorID: "one-shot-git-execution-repair-test",
	})
	if err != nil {
		t.Fatalf("RunExpiredRepositoryExtractionWorkMaintenanceTick() error = %v", err)
	}
	if len(repaired.Transitions) != 1 || repaired.Transitions[0].ExecutionRepair == nil ||
		repaired.Transitions[0].ExecutionRepair.Work.WorkItemID != workItemID ||
		repaired.Transitions[0].ExecutionRepair.ClaimID != claimID ||
		repaired.Transitions[0].ExecutionRepair.Disposition != evidenceingestion.RepositoryExtractionWorkRecoveryDispositionRequeued {
		t.Fatalf("execution repair = %+v", repaired)
	}

	completed, err := RunOneShotOrchestration(ctx, pool, input)
	if err != nil {
		t.Fatalf("repaired RunOneShotOrchestration() error = %v", err)
	}
	if completed.Run.Status != OrchestrationRunStatusCompleted || completed.Step == nil || completed.Step.Number != 2 ||
		completed.Step.Coverage == nil || !completed.Step.Coverage.Complete {
		t.Fatalf("repaired Git result = %+v", completed)
	}
	resolution, err := evidenceingestion.ResolveRepositoryExtractionWork(ctx, pool, workItemID)
	if err != nil {
		t.Fatalf("ResolveRepositoryExtractionWork() error = %v", err)
	}
	if resolution.State != evidenceingestion.RepositoryExtractionWorkStateSucceeded || resolution.LatestAttemptNumber != 2 {
		t.Fatalf("repaired work resolution = %+v", resolution)
	}

	var secondClaimed bool
	var secondAttempt int
	if err := pool.QueryRow(ctx, `
		SELECT claimed, attempt_number
		FROM repository_extraction_worker_tick_requests
		WHERE request_id = $1
	`, oneShotGitWorkerRequestID(input.RequestID, 2, 2)).Scan(&secondClaimed, &secondAttempt); err != nil {
		t.Fatalf("read retry worker tick: %v", err)
	}
	if !secondClaimed || secondAttempt != 2 {
		t.Fatalf("retry worker tick = claimed %t attempt %d", secondClaimed, secondAttempt)
	}
	var expiredAttempts, succeededAttempts int
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE attempt_number = 1 AND status = 'expired'),
			COUNT(*) FILTER (WHERE attempt_number = 2 AND status = 'succeeded')
		FROM repository_extraction_work_claim_attempts
		WHERE work_item_id = $1
	`, workItemID).Scan(&expiredAttempts, &succeededAttempts); err != nil {
		t.Fatalf("read repaired attempt history: %v", err)
	}
	if expiredAttempts != 1 || succeededAttempts != 1 {
		t.Fatalf("repaired attempt history = expired %d succeeded %d", expiredAttempts, succeededAttempts)
	}
}

func TestIntegrationOneShotGitGoScopesRepositoryStreamsBySourceBinding(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	first, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "binding-a")
	second, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "binding-b")
	requestIDs := []string{"one-shot-git-binding-a", "one-shot-git-binding-b"}
	for index, workspace := range []Workspace{first, second} {
		result, err := RunOneShotOrchestration(ctx, pool, OneShotOrchestrationInput{
			RequestID:   requestIDs[index],
			WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
		})
		if err != nil {
			t.Fatalf("RunOneShotOrchestration(%d) error = %v", index, err)
		}
		if result.Run.Status != OrchestrationRunStatusStopped || result.Run.StopReason != OrchestrationStopReasonBudgetExhausted {
			t.Fatalf("binding-scoped result %d = %+v", index, result)
		}
	}
	var repositoryCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(DISTINCT repo_id) FROM repository_change_observations`).Scan(&repositoryCount); err != nil {
		t.Fatalf("count repository observation streams: %v", err)
	}
	if repositoryCount != 2 || first.Sources[0].SourceID != second.Sources[0].SourceID || first.Sources[0].ID == second.Sources[0].ID {
		t.Fatalf("repository stream identities = %d, sources = %+v / %+v", repositoryCount, first.Sources[0], second.Sources[0])
	}
}

func registerOneShotGitWorkspace(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
) (Workspace, string, string) {
	t.Helper()
	root := gitWorkspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package sample\n\nfunc Hello() {}\n"), 0o600); err != nil {
		t.Fatalf("write Git Go source: %v", err)
	}
	runOneShotGitCommand(t, root, "add", "main.go")
	runOneShotGitCommand(t, root, "commit", "-q", "-m", "add Go source")
	commitSHA := oneShotGitOutput(t, root, "rev-parse", "HEAD")
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID:     "register-one-shot-git-" + suffix,
		WorkspaceID:   "workspace:one-shot-git-" + suffix,
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{{
			CapabilityName: SourceCapabilityGitGoRepository, CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
			SourceID: "repository", RelativePath: ".",
		}},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	return registered.Workspace, root, commitSHA
}

func claimStableOneShotGitWork(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	source WorkspaceSourceBinding,
	root string,
	suffix string,
) (evidenceingestion.RepositoryExtractionWorkScheduleResult, evidenceingestion.RepositoryExtractionWorkClaimResult) {
	t.Helper()
	observation, err := evidenceingestion.ObserveGitRepositoryChange(ctx, pool, evidenceingestion.GitRepositoryChangeObservationConfig{
		WorkspaceRoot: root, RepoID: oneShotGitGoRepositoryID(source),
		RequestID: "one-shot-git-" + suffix + "-external-observe", StabilityWindow: oneShotGitGoStabilityWindow,
	})
	if err != nil {
		t.Fatalf("external ObserveGitRepositoryChange() error = %v", err)
	}
	if !observation.Stable || observation.Inspection.Dirty {
		t.Fatalf("external stable observation = %+v", observation)
	}
	scheduled, err := evidenceingestion.ScheduleRepositoryExtractionWork(ctx, pool, evidenceingestion.RepositoryExtractionWorkScheduleInput{
		RequestID: "one-shot-git-" + suffix + "-external-schedule", ObservationRequestID: observation.RequestID,
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	})
	if err != nil {
		t.Fatalf("external ScheduleRepositoryExtractionWork() error = %v", err)
	}
	claimed, err := evidenceingestion.ClaimRepositoryExtractionWork(ctx, pool, evidenceingestion.RepositoryExtractionWorkClaimInput{
		RequestID: "one-shot-git-" + suffix + "-external-claim", RepoID: oneShotGitGoRepositoryID(source),
		ExtractorName: evidenceingestion.ExtractorRepositoryGoParserCodeFact,
		WorkerID:      "external-worker", LeaseDurationMilliseconds: oneShotGitGoLeaseDuration.Milliseconds(),
	})
	if err != nil {
		t.Fatalf("external ClaimRepositoryExtractionWork() error = %v", err)
	}
	if !claimed.Claimed || claimed.Work == nil || claimed.Work.WorkItemID != scheduled.Work.WorkItemID {
		t.Fatalf("external claim = %+v, scheduled = %+v", claimed, scheduled)
	}
	return scheduled, claimed
}

func runOneShotGitCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func oneShotGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func waitForOneShotGitStability(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(oneShotGitGoStabilityWindow + 100*time.Millisecond)
	defer timer.Stop()
	<-timer.C
}

func assertWaitingOneShotGitResult(t *testing.T, result OneShotOrchestrationResult, stepNumber int) {
	t.Helper()
	if !result.SourceInvoked || result.Run.Status != OrchestrationRunStatusRunning ||
		result.Step == nil || result.Step.Number != stepNumber || result.Step.Outcome != OrchestrationStepOutcomeContinue ||
		result.Step.Coverage == nil || result.Step.Coverage.UnitKind != OrchestrationCoverageUnitRepositoryFiles ||
		result.Step.Coverage.AttemptedUnitCount != 0 || result.Step.Coverage.CoveredUnitCount != 0 || result.Step.Coverage.Complete {
		t.Fatalf("waiting Git result = %+v", result)
	}
}
