//go:build integration

package detective

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationPeriodicLocalTextLifecycleAndChangedToken(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, sourcePath := registerOneShotFileWorkspace(t, ctx, pool, "periodic-lifecycle", "first periodic requirement\n")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}

	first, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("first RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, first, 1, false, true)
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cursors", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)

	replay, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("unchanged RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, replay, 1, true, false)
	if !reflect.DeepEqual(replay.Cycle, first.Cycle) || !reflect.DeepEqual(replay.Run, first.Run) {
		t.Fatalf("unchanged periodic replay = %+v, want %+v", replay, first)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)

	if err := os.WriteFile(sourcePath, []byte("second periodic requirement\n"), 0o600); err != nil {
		t.Fatalf("update periodic source: %v", err)
	}
	changed, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("changed RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, changed, 2, false, true)
	if changed.Cycle.ID == first.Cycle.ID || changed.Run.ID == first.Run.ID || changed.Cycle.SourceToken == first.Cycle.SourceToken {
		t.Fatalf("changed periodic identities = %+v, first = %+v", changed, first)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 2)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 2)
	assertPeriodicCursor(t, ctx, pool, workspace.ID, workspace.Sources[0].ID, 2, "", changed.Cycle.ID, changed.Cycle.SourceToken)
}

func TestIntegrationPeriodicSourceRecoversAfterRunCommitBeforeCursorAdvance(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOneShotFileWorkspace(t, ctx, pool, "periodic-restart", "restart-safe requirement\n")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	request, err := preparePeriodicSourceTick(input)
	if err != nil {
		t.Fatalf("preparePeriodicSourceTick() error = %v", err)
	}
	resolved, err := ResolveWorkspace(ctx, pool, workspace.ID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	source := resolved.Sources[0]
	_, sourceToken, err := plannerSourceAuthority(ctx, resolved, source)
	if err != nil {
		t.Fatalf("plannerSourceAuthority() error = %v", err)
	}
	reserved, err := reservePeriodicSourceCycle(ctx, pool, request, resolved, source, sourceToken)
	if err != nil {
		t.Fatalf("reservePeriodicSourceCycle() error = %v", err)
	}
	if reserved.cycleReplayed || reserved.cursorCommitted || reserved.run.Status != OrchestrationRunStatusRunning || reserved.run.StepCount != 0 {
		t.Fatalf("reserved periodic source = %+v", reserved)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, source.ID, 1, reserved.cycle.ID, "", "")

	oneShot, err := RunOneShotOrchestration(ctx, pool, OneShotOrchestrationInput{
		RequestID: reserved.cycle.OneShotRequestID, WorkspaceID: workspace.ID,
		SourceBindingID: source.ID, MaxSteps: reserved.cycle.MaxSteps,
	})
	if err != nil {
		t.Fatalf("RunOneShotOrchestration() before simulated restart error = %v", err)
	}
	if oneShot.Run.Status != OrchestrationRunStatusCompleted || !oneShot.SourceInvoked {
		t.Fatalf("committed one-shot before simulated restart = %+v", oneShot)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, source.ID, 1, reserved.cycle.ID, "", "")
	var cycleStatus string
	if err := pool.QueryRow(ctx, `
		SELECT status
		FROM detective_periodic_source_cycles
		WHERE cycle_id = $1
	`, reserved.cycle.ID).Scan(&cycleStatus); err != nil {
		t.Fatalf("read unfinalized periodic cycle: %v", err)
	}
	if cycleStatus != OrchestrationRunStatusRunning {
		t.Fatalf("unfinalized periodic cycle status = %s, want running", cycleStatus)
	}

	recovered, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("recovered RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, recovered, 1, true, false)
	if recovered.Cycle.ID != reserved.cycle.ID || recovered.Run.ID != reserved.run.ID {
		t.Fatalf("recovered periodic identities = %+v, reserved = %+v", recovered, reserved)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, source.ID, 1, "", reserved.cycle.ID, sourceToken)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationConcurrentPeriodicWakeupsYieldOneDurableRun(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOneShotFileWorkspace(t, ctx, pool, "periodic-concurrent", "concurrent periodic requirement\n")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, MaxSteps: 1,
	}
	type outcome struct {
		result PeriodicSourceTickResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := RunPeriodicSourceTick(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	results := make([]PeriodicSourceTickResult, 0, 2)
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent RunPeriodicSourceTick() error = %v", got.err)
		}
		results = append(results, got.result)
	}
	if results[0].Cycle.ID != results[1].Cycle.ID || results[0].Run.ID != results[1].Run.ID ||
		results[0].Run.Status != OrchestrationRunStatusCompleted || results[1].Run.Status != OrchestrationRunStatusCompleted {
		t.Fatalf("concurrent periodic results = %+v / %+v", results[0], results[1])
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cursors", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationConcurrentPeriodicGitWakeupsReadOneRunAudit(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "periodic-concurrent-git")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, MaxSteps: 3,
	}
	type outcome struct {
		result PeriodicSourceTickResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := RunPeriodicSourceTick(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	results := make([]PeriodicSourceTickResult, 0, 2)
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent Git RunPeriodicSourceTick() error = %v", got.err)
		}
		results = append(results, got.result)
	}
	if results[0].Cycle.ID != results[1].Cycle.ID ||
		results[0].Run.ID != results[1].Run.ID ||
		results[0].Run.Status != OrchestrationRunStatusRunning ||
		results[1].Run.Status != OrchestrationRunStatusRunning {
		t.Fatalf("concurrent Git periodic results = %+v / %+v", results[0], results[1])
	}

	waitForOneShotGitStability(t)
	completed, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("stable Git RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, completed, 1, true, true)
	if completed.Cycle.ID != results[0].Cycle.ID || completed.Run.ID != results[0].Run.ID {
		t.Fatalf("completed Git periodic result = %+v, initial = %+v", completed, results[0])
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_change_observations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 1)
}

func TestIntegrationPeriodicGitGoContinuesOneCycleUntilStable(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "periodic")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: workspace.Sources[0].ID, MaxSteps: 3,
	}
	first, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("first Git RunPeriodicSourceTick() error = %v", err)
	}
	if first.Replayed || !first.SourceInvoked || first.CursorAdvanced || first.Run.Status != OrchestrationRunStatusRunning ||
		first.Cycle.Status != OrchestrationRunStatusRunning || first.Run.StepCount != 1 {
		t.Fatalf("first periodic Git tick = %+v", first)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, workspace.Sources[0].ID, 1, first.Cycle.ID, "", "")

	waitForOneShotGitStability(t)
	completed, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("stable Git RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, completed, 1, true, true)
	if completed.Cycle.ID != first.Cycle.ID || completed.Run.ID != first.Run.ID || completed.Run.StepCount != 2 ||
		completed.Cycle.SourceToken != first.Cycle.SourceToken {
		t.Fatalf("completed periodic Git tick = %+v, first = %+v", completed, first)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, workspace.Sources[0].ID, 1, "", completed.Cycle.ID, completed.Cycle.SourceToken)
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_heads", 0)

	replay, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("unchanged Git RunPeriodicSourceTick() error = %v", err)
	}
	assertCompletedPeriodicTick(t, replay, 1, true, false)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 1)
}

func TestIntegrationPeriodicRuntimeInitialWakeAndRestartReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name+".txt"), []byte(name+" periodic runtime requirement\n"), 0o600); err != nil {
			t.Fatalf("write periodic runtime source %s: %v", name, err)
		}
	}
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID: "register-periodic-runtime", WorkspaceID: "workspace:periodic-runtime", WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{
			{CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion, SourceID: "a", RelativePath: "a.txt"},
			{CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion, SourceID: "b", RelativePath: "b.txt"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	config := PeriodicRuntimeConfig{WorkspaceID: registered.Workspace.ID, MaxSteps: 1, Interval: time.Hour}
	first, err := NewPeriodicRuntime(ctx, pool, config)
	if err != nil {
		t.Fatalf("NewPeriodicRuntime() error = %v", err)
	}
	for range len(registered.Workspace.Sources) {
		outcome := waitForPeriodicRuntimeOutcome(t, first.Outcomes())
		if outcome.Err != nil || !outcome.Result.CursorAdvanced || outcome.Result.Replayed {
			t.Fatalf("initial periodic runtime outcome = %+v", outcome)
		}
	}
	first.Close()
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cursors", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 2)

	restarted, err := NewPeriodicRuntime(ctx, pool, config)
	if err != nil {
		t.Fatalf("restarted NewPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(restarted.Close)
	for range len(registered.Workspace.Sources) {
		outcome := waitForPeriodicRuntimeOutcome(t, restarted.Outcomes())
		if outcome.Err != nil || !outcome.Result.CursorAdvanced || !outcome.Result.Replayed || outcome.Result.SourceInvoked {
			t.Fatalf("restarted periodic runtime outcome = %+v", outcome)
		}
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 2)
}

func assertCompletedPeriodicTick(
	t *testing.T,
	result PeriodicSourceTickResult,
	wantCycleNumber int64,
	wantReplayed bool,
	wantSourceInvoked bool,
) {
	t.Helper()
	if result.Cycle.Number != wantCycleNumber || result.Cycle.Status != OrchestrationRunStatusCompleted ||
		result.Cycle.FinalizedAt == nil || result.Run.Status != OrchestrationRunStatusCompleted ||
		!result.CursorAdvanced || result.Replayed != wantReplayed || result.SourceInvoked != wantSourceInvoked {
		t.Fatalf("periodic tick = %+v, want cycle=%d replayed=%t invoked=%t", result, wantCycleNumber, wantReplayed, wantSourceInvoked)
	}
}

func assertPeriodicCursor(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID string,
	sourceBindingID string,
	wantCycleCount int64,
	wantActiveCycleID string,
	wantLastCycleID string,
	wantCursorToken string,
) {
	t.Helper()
	cursor, found, err := readPeriodicSourceCursor(ctx, pool, workspaceID, sourceBindingID, false)
	if err != nil {
		t.Fatalf("readPeriodicSourceCursor() error = %v", err)
	}
	if !found || cursor.cycleCount != wantCycleCount || cursor.activeCycleID != wantActiveCycleID ||
		cursor.lastCycleID != wantLastCycleID || cursor.cursorToken != wantCursorToken {
		t.Fatalf("periodic cursor = %+v, found=%t", cursor, found)
	}
	if wantCursorToken == "" && cursor.cursorAdvancedAt != nil {
		t.Fatalf("unadvanced periodic cursor has timestamp %v", cursor.cursorAdvancedAt)
	}
	if wantCursorToken != "" && cursor.cursorAdvancedAt == nil {
		t.Fatal("advanced periodic cursor has no timestamp")
	}
}

func waitForPeriodicRuntimeOutcome(t *testing.T, outcomes <-chan PeriodicRuntimeOutcome) PeriodicRuntimeOutcome {
	t.Helper()
	select {
	case outcome, ok := <-outcomes:
		if !ok {
			t.Fatal("periodic runtime outcomes closed early")
		}
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("periodic runtime outcome timed out")
		return PeriodicRuntimeOutcome{}
	}
}
