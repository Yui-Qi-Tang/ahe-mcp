//go:build integration

package detective

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationOrchestrationAuditLifecycleAndReplay(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, sourcePath := registerOrchestrationWorkspace(t, ctx, pool, "lifecycle")

	startInput := OrchestrationRunStartInput{
		RequestID: "start-lifecycle", WorkspaceID: workspace.ID, MaxSteps: 2,
	}
	started, err := StartOrchestrationRun(ctx, pool, startInput)
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	if started.Replayed || started.Run.Status != OrchestrationRunStatusRunning || started.Run.StepCount != 0 || len(started.Run.Steps) != 0 {
		t.Fatalf("started run = %+v", started)
	}
	startReplay, err := StartOrchestrationRun(ctx, pool, startInput)
	if err != nil {
		t.Fatalf("start replay error = %v", err)
	}
	if !startReplay.Replayed || !reflect.DeepEqual(startReplay.Run, started.Run) {
		t.Fatalf("start replay = %+v, want %+v", startReplay, started.Run)
	}

	firstPlanInput := OrchestrationStepPlanInput{
		RequestID: "plan-lifecycle-1", RunID: started.Run.ID,
		SourceBindingID:    workspace.Sources[0].ID,
		ReasonCode:         OrchestrationReasonInitialSource,
		InputAuthorityKind: OrchestrationInputAuthorityNone,
	}
	firstPlan, err := PlanOrchestrationStep(ctx, pool, firstPlanInput)
	if err != nil {
		t.Fatalf("PlanOrchestrationStep(first) error = %v", err)
	}
	if firstPlan.Replayed || firstPlan.Step.Status != OrchestrationStepStatusPlanned || firstPlan.Step.Number != 1 ||
		firstPlan.Step.CapabilityName != workspace.Sources[0].CapabilityName ||
		firstPlan.Step.CapabilityVersion != workspace.Sources[0].CapabilityVersion ||
		firstPlan.Step.SourceSystem != workspace.Sources[0].SourceSystem ||
		firstPlan.Step.SourceID != workspace.Sources[0].SourceID {
		t.Fatalf("first plan = %+v", firstPlan)
	}
	firstPlanReplay, err := PlanOrchestrationStep(ctx, pool, firstPlanInput)
	if err != nil {
		t.Fatalf("first plan replay error = %v", err)
	}
	if !firstPlanReplay.Replayed || !reflect.DeepEqual(firstPlanReplay.Step, firstPlan.Step) {
		t.Fatalf("first plan replay = %+v, want step %+v", firstPlanReplay, firstPlan.Step)
	}

	firstCompleteInput := completeInput(
		"complete-lifecycle-1", started.Run.ID, firstPlan.Step.ID,
		OrchestrationStepOutcomeContinue, "", false,
	)
	firstComplete, err := CompleteOrchestrationStep(ctx, pool, firstCompleteInput)
	if err != nil {
		t.Fatalf("CompleteOrchestrationStep(first) error = %v", err)
	}
	if firstComplete.Run.Status != OrchestrationRunStatusRunning || firstComplete.Step.Status != OrchestrationStepStatusCompleted || firstComplete.Step.Outcome != OrchestrationStepOutcomeContinue {
		t.Fatalf("first completion = %+v", firstComplete)
	}

	secondPlanInput := OrchestrationStepPlanInput{
		RequestID: "plan-lifecycle-2", RunID: started.Run.ID,
		SourceBindingID:    workspace.Sources[0].ID,
		ReasonCode:         OrchestrationReasonCoverageGap,
		InputAuthorityKind: OrchestrationInputAuthoritySourceToken,
		InputAuthorityRef:  "source-token:lifecycle-2",
	}
	secondPlan, err := PlanOrchestrationStep(ctx, pool, secondPlanInput)
	if err != nil {
		t.Fatalf("PlanOrchestrationStep(second) error = %v", err)
	}
	if secondPlan.Step.Number != 2 || secondPlan.Step.InputAuthorityRef != secondPlanInput.InputAuthorityRef {
		t.Fatalf("second plan = %+v", secondPlan)
	}

	secondCompleteInput := completeInput(
		"complete-lifecycle-2", started.Run.ID, secondPlan.Step.ID,
		OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied, true,
	)
	secondComplete, err := CompleteOrchestrationStep(ctx, pool, secondCompleteInput)
	if err != nil {
		t.Fatalf("CompleteOrchestrationStep(second) error = %v", err)
	}
	if secondComplete.Run.Status != OrchestrationRunStatusCompleted || secondComplete.Run.StopReason != OrchestrationStopReasonCoverageSatisfied ||
		secondComplete.Run.FinishedAt == nil || secondComplete.Step.Coverage == nil || !secondComplete.Step.Coverage.Complete {
		t.Fatalf("second completion = %+v", secondComplete)
	}
	resolved, err := ResolveOrchestrationRun(ctx, pool, started.Run.ID)
	if err != nil {
		t.Fatalf("ResolveOrchestrationRun() error = %v", err)
	}
	if !reflect.DeepEqual(resolved, secondComplete.Run) || len(resolved.Steps) != 2 {
		t.Fatalf("resolved run = %+v, want %+v", resolved, secondComplete.Run)
	}

	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove source after terminal audit: %v", err)
	}
	terminalReplay, err := CompleteOrchestrationStep(ctx, pool, secondCompleteInput)
	if err != nil {
		t.Fatalf("terminal completion replay after source removal error = %v", err)
	}
	if !terminalReplay.Replayed || !reflect.DeepEqual(terminalReplay.Run, secondComplete.Run) {
		t.Fatalf("terminal completion replay = %+v, want %+v", terminalReplay, secondComplete.Run)
	}
	startTerminalReplay, err := StartOrchestrationRun(ctx, pool, startInput)
	if err != nil {
		t.Fatalf("terminal start replay after source removal error = %v", err)
	}
	if !startTerminalReplay.Replayed || startTerminalReplay.Run.Status != OrchestrationRunStatusCompleted {
		t.Fatalf("terminal start replay = %+v", startTerminalReplay)
	}
	_, err = PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID: "plan-after-terminal", RunID: started.Run.ID,
		SourceBindingID: workspace.Sources[0].ID,
		ReasonCode:      OrchestrationReasonRefresh, InputAuthorityKind: OrchestrationInputAuthorityNone,
	})
	assertDetectiveKind(t, err, ErrorOrchestrationRunConflict)

	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 5)
}

func TestIntegrationOrchestrationAuditRejectsIdempotencyConflicts(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOrchestrationWorkspace(t, ctx, pool, "idempotency")
	startInput := OrchestrationRunStartInput{RequestID: "start-idempotency", WorkspaceID: workspace.ID, MaxSteps: 2}
	started, err := StartOrchestrationRun(ctx, pool, startInput)
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	conflictingStart := startInput
	conflictingStart.MaxSteps = 1
	_, err = StartOrchestrationRun(ctx, pool, conflictingStart)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)

	planInput := OrchestrationStepPlanInput{
		RequestID: "plan-idempotency", RunID: started.Run.ID,
		SourceBindingID: workspace.Sources[0].ID,
		ReasonCode:      OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
	}
	planned, err := PlanOrchestrationStep(ctx, pool, planInput)
	if err != nil {
		t.Fatalf("PlanOrchestrationStep() error = %v", err)
	}
	conflictingPlan := planInput
	conflictingPlan.ReasonCode = OrchestrationReasonRefresh
	_, err = PlanOrchestrationStep(ctx, pool, conflictingPlan)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)

	complete := completeInput(
		"complete-idempotency", started.Run.ID, planned.Step.ID,
		OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied, true,
	)
	if _, err := CompleteOrchestrationStep(ctx, pool, complete); err != nil {
		t.Fatalf("CompleteOrchestrationStep() error = %v", err)
	}
	conflictingComplete := complete
	conflictingComplete.Coverage.AttemptedUnitCount = 4
	conflictingComplete.Coverage.CoveredUnitCount = 4
	_, err = CompleteOrchestrationStep(ctx, pool, conflictingComplete)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)

	crossTransition := planInput
	crossTransition.RequestID = startInput.RequestID
	_, err = PlanOrchestrationStep(ctx, pool, crossTransition)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)
}

func TestIntegrationOrchestrationAuditTerminalOutcomesAreImmutable(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOrchestrationWorkspace(t, ctx, pool, "terminal")
	tests := []struct {
		name       string
		outcome    string
		stopReason string
		wantStatus string
	}{
		{name: "failed", outcome: OrchestrationStepOutcomeFailed, stopReason: OrchestrationStopReasonSourceFailed, wantStatus: OrchestrationRunStatusFailed},
		{name: "stopped", outcome: OrchestrationStepOutcomeStopped, stopReason: OrchestrationStopReasonContradictoryOutput, wantStatus: OrchestrationRunStatusStopped},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
				RequestID: "start-terminal-" + test.name, WorkspaceID: workspace.ID, MaxSteps: 1,
			})
			if err != nil {
				t.Fatalf("StartOrchestrationRun() error = %v", err)
			}
			planned, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
				RequestID: "plan-terminal-" + test.name, RunID: started.Run.ID,
				SourceBindingID: workspace.Sources[0].ID,
				ReasonCode:      OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
			})
			if err != nil {
				t.Fatalf("PlanOrchestrationStep() error = %v", err)
			}
			input := completeInput("complete-terminal-"+test.name, started.Run.ID, planned.Step.ID, test.outcome, test.stopReason, false)
			completed, err := CompleteOrchestrationStep(ctx, pool, input)
			if err != nil {
				t.Fatalf("CompleteOrchestrationStep() error = %v", err)
			}
			if completed.Run.Status != test.wantStatus || completed.Run.StopReason != test.stopReason || completed.Run.FinishedAt == nil {
				t.Fatalf("terminal run = %+v", completed.Run)
			}
			replay, err := CompleteOrchestrationStep(ctx, pool, input)
			if err != nil {
				t.Fatalf("terminal replay error = %v", err)
			}
			if !replay.Replayed || !reflect.DeepEqual(replay.Run, completed.Run) {
				t.Fatalf("terminal replay = %+v, want %+v", replay, completed.Run)
			}
		})
	}
}

func TestIntegrationOrchestrationAuditRequiresExplicitBudgetStop(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOrchestrationWorkspace(t, ctx, pool, "budget")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-budget", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	planned, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID: "plan-budget", RunID: started.Run.ID, SourceBindingID: workspace.Sources[0].ID,
		ReasonCode: OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
	})
	if err != nil {
		t.Fatalf("PlanOrchestrationStep() error = %v", err)
	}
	_, err = CompleteOrchestrationStep(ctx, pool, completeInput(
		"complete-budget-invalid", started.Run.ID, planned.Step.ID,
		OrchestrationStepOutcomeContinue, "", false,
	))
	assertDetectiveKind(t, err, ErrorOrchestrationRunConflict)
	wrongUnit := completeInput(
		"complete-budget-wrong-unit", started.Run.ID, planned.Step.ID,
		OrchestrationStepOutcomeStopped, OrchestrationStopReasonBudgetExhausted, false,
	)
	wrongUnit.Coverage.UnitKind = OrchestrationCoverageUnitRepositoryFiles
	_, err = CompleteOrchestrationStep(ctx, pool, wrongUnit)
	assertDetectiveKind(t, err, ErrorInvalidInput)

	stopped, err := CompleteOrchestrationStep(ctx, pool, completeInput(
		"complete-budget-stop", started.Run.ID, planned.Step.ID,
		OrchestrationStepOutcomeStopped, OrchestrationStopReasonBudgetExhausted, false,
	))
	if err != nil {
		t.Fatalf("budget stop error = %v", err)
	}
	if stopped.Run.Status != OrchestrationRunStatusStopped || stopped.Step.StopReason != OrchestrationStopReasonBudgetExhausted {
		t.Fatalf("budget stop = %+v", stopped)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 3)
}

func TestIntegrationOrchestrationAuditConcurrentTransitionsCommitOnce(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOrchestrationWorkspace(t, ctx, pool, "concurrent-audit")
	startInput := OrchestrationRunStartInput{RequestID: "start-concurrent-audit", WorkspaceID: workspace.ID, MaxSteps: 1}

	type startOutcome struct {
		result OrchestrationRunStartResult
		err    error
	}
	startGate := make(chan struct{})
	startOutcomes := make(chan startOutcome, 2)
	for range 2 {
		go func() {
			<-startGate
			result, err := StartOrchestrationRun(ctx, pool, startInput)
			startOutcomes <- startOutcome{result: result, err: err}
		}()
	}
	close(startGate)
	startResults := make([]OrchestrationRunStartResult, 0, 2)
	for range 2 {
		outcome := <-startOutcomes
		if outcome.err != nil {
			t.Fatalf("concurrent start error = %v", outcome.err)
		}
		startResults = append(startResults, outcome.result)
	}
	assertOneOrchestrationReplay(t, startResults[0].Replayed, startResults[1].Replayed)
	runID := startResults[0].Run.ID

	planInput := OrchestrationStepPlanInput{
		RequestID: "plan-concurrent-audit", RunID: runID, SourceBindingID: workspace.Sources[0].ID,
		ReasonCode: OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
	}
	type planOutcome struct {
		result OrchestrationStepPlanResult
		err    error
	}
	planGate := make(chan struct{})
	planOutcomes := make(chan planOutcome, 2)
	for range 2 {
		go func() {
			<-planGate
			result, err := PlanOrchestrationStep(ctx, pool, planInput)
			planOutcomes <- planOutcome{result: result, err: err}
		}()
	}
	close(planGate)
	planResults := make([]OrchestrationStepPlanResult, 0, 2)
	for range 2 {
		outcome := <-planOutcomes
		if outcome.err != nil {
			t.Fatalf("concurrent plan error = %v", outcome.err)
		}
		planResults = append(planResults, outcome.result)
	}
	assertOneOrchestrationReplay(t, planResults[0].Replayed, planResults[1].Replayed)
	if planResults[0].Step.ID != planResults[1].Step.ID {
		t.Fatalf("concurrent plan step IDs differ: %s != %s", planResults[0].Step.ID, planResults[1].Step.ID)
	}

	completeInput := completeInput(
		"complete-concurrent-audit", runID, planResults[0].Step.ID,
		OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied, true,
	)
	type completionOutcome struct {
		result OrchestrationStepCompleteResult
		err    error
	}
	completeGate := make(chan struct{})
	completeOutcomes := make(chan completionOutcome, 2)
	for range 2 {
		go func() {
			<-completeGate
			result, err := CompleteOrchestrationStep(ctx, pool, completeInput)
			completeOutcomes <- completionOutcome{result: result, err: err}
		}()
	}
	close(completeGate)
	completionResults := make([]OrchestrationStepCompleteResult, 0, 2)
	for range 2 {
		outcome := <-completeOutcomes
		if outcome.err != nil {
			t.Fatalf("concurrent completion error = %v", outcome.err)
		}
		completionResults = append(completionResults, outcome.result)
	}
	assertOneOrchestrationReplay(t, completionResults[0].Replayed, completionResults[1].Replayed)
	if !reflect.DeepEqual(completionResults[0].Run, completionResults[1].Run) {
		t.Fatalf("concurrent completion runs differ: %+v != %+v", completionResults[0].Run, completionResults[1].Run)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 3)
}

func TestIntegrationOrchestrationAuditAllowsOnlyOneConcurrentPlannedStep(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _ := registerOrchestrationWorkspace(t, ctx, pool, "planned-step")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planned-step", WorkspaceID: workspace.ID, MaxSteps: 2,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}

	type outcome struct {
		result OrchestrationStepPlanResult
		err    error
	}
	gate := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for _, requestID := range []string{"plan-planned-step-a", "plan-planned-step-b"} {
		go func(requestID string) {
			<-gate
			result, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
				RequestID: requestID, RunID: started.Run.ID,
				SourceBindingID:    workspace.Sources[0].ID,
				ReasonCode:         OrchestrationReasonInitialSource,
				InputAuthorityKind: OrchestrationInputAuthorityNone,
			})
			outcomes <- outcome{result: result, err: err}
		}(requestID)
	}
	close(gate)

	succeeded := 0
	conflicted := 0
	for range 2 {
		outcome := <-outcomes
		if outcome.err == nil {
			succeeded++
			if outcome.result.Step.Number != 1 || outcome.result.Step.Status != OrchestrationStepStatusPlanned {
				t.Fatalf("winning plan = %+v", outcome.result)
			}
			continue
		}
		if kind, ok := KindOf(outcome.err); ok && kind == ErrorOrchestrationRunConflict {
			conflicted++
			continue
		}
		t.Fatalf("concurrent plan error = %v", outcome.err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent plan outcomes = %d succeeded/%d conflicted, want 1/1", succeeded, conflicted)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 2)
}

func TestIntegrationOrchestrationAuditFailsClosedWhenWorkspaceBecomesUnavailable(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, sourcePath := registerOrchestrationWorkspace(t, ctx, pool, "unavailable")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-unavailable", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove registered source: %v", err)
	}
	_, err = PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID: "plan-unavailable", RunID: started.Run.ID, SourceBindingID: workspace.Sources[0].ID,
		ReasonCode: OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
	})
	assertDetectiveKind(t, err, ErrorWorkspaceUnavailable)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 1)
}

func registerOrchestrationWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) (Workspace, string) {
	t.Helper()
	root := t.TempDir()
	sourceName := "prd-" + suffix
	sourcePath := filepath.Join(root, sourceName+".txt")
	if err := os.WriteFile(sourcePath, []byte("source-backed orchestration\n"), 0o600); err != nil {
		t.Fatalf("write orchestration source: %v", err)
	}
	registered, err := RegisterWorkspace(ctx, pool, localTextWorkspaceInput(
		"register-orchestration-"+suffix,
		"workspace:orchestration-"+suffix,
		root,
		sourceName,
	))
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	return registered.Workspace, sourcePath
}

func assertOneOrchestrationReplay(t *testing.T, first, second bool) {
	t.Helper()
	if first == second {
		t.Fatalf("replayed flags = %v/%v, want one replay", first, second)
	}
}
