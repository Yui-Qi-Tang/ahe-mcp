//go:build integration

package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationPlannerPeriodicTickConsumesOpaqueCrossSourceChoice(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, primary, textSource := registerPlannerPeriodicWorkspace(t, ctx, pool, "cross-source")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: primary.ID, MaxSteps: 3,
	}
	invocations := 0
	runner := func(_ context.Context, plannerInput PlannerInput) ([]byte, error) {
		invocations++
		assertPlannerPeriodicInputOpaque(t, plannerInput, workspace)
		if len(plannerInput.ObservedCoverage) != 1 {
			t.Fatalf("planner observed coverage = %+v", plannerInput.ObservedCoverage)
		}
		choice, ok := plannerChoiceByCapability(plannerInput, SourceCapabilityLocalPRDText, nil)
		if !ok {
			t.Fatal("planner input has no local-text choice")
		}
		return plannerDecisionJSON(PlannerClassificationQuerySource, choice), nil
	}

	result, err := RunPlannerPeriodicSourceTick(ctx, pool, input, runner)
	if err != nil {
		t.Fatalf("RunPlannerPeriodicSourceTick() error = %v", err)
	}
	if invocations != 1 || result.PlannerContinuation == nil ||
		!result.PlannerContinuation.Attempted || result.PlannerContinuation.Resumed ||
		result.PlannerContinuation.Recommendation == nil ||
		result.PlannerContinuation.Consumption == nil ||
		result.PlannerContinuation.Recommendation.SourceBindingID != textSource.ID ||
		result.PlannerContinuation.Consumption.Step.SourceBindingID != textSource.ID ||
		result.Run.Status != OrchestrationRunStatusCompleted ||
		result.Run.StopReason != OrchestrationStopReasonCoverageSatisfied ||
		result.Run.StepCount != 2 || !result.CursorAdvanced || !result.SourceInvoked {
		t.Fatalf("planner periodic result = %+v, invocations = %d", result, invocations)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 2)
	assertDetectiveTableCount(t, ctx, pool, "repository_change_observations", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
	assertPeriodicCursor(t, ctx, pool, workspace.ID, primary.ID, 1, "", result.Cycle.ID, result.Cycle.SourceToken)

	replay, err := RunPlannerPeriodicSourceTick(ctx, pool, input, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return nil, errors.New("planner must not run for a committed source token")
	})
	if err != nil {
		t.Fatalf("terminal planner periodic replay error = %v", err)
	}
	if invocations != 1 || !replay.Replayed || !replay.CursorAdvanced || replay.SourceInvoked ||
		replay.PlannerContinuation != nil || replay.Cycle.ID != result.Cycle.ID || replay.Run.ID != result.Run.ID {
		t.Fatalf("terminal planner periodic replay = %+v, invocations = %d", replay, invocations)
	}
}

func TestIntegrationPlannerPeriodicTickAbstainsWithoutPlannerWrites(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, primary, _ := registerPlannerPeriodicWorkspace(t, ctx, pool, "abstain")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: primary.ID, MaxSteps: 3,
	}
	invocations := 0
	runner := func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return plannerDecisionJSON(PlannerClassificationAbstain, ""), nil
	}

	result, err := RunPlannerPeriodicSourceTick(ctx, pool, input, runner)
	if err != nil {
		t.Fatalf("abstaining RunPlannerPeriodicSourceTick() error = %v", err)
	}
	if invocations != 1 || result.Run.Status != OrchestrationRunStatusRunning ||
		result.Run.StepCount != 1 || result.CursorAdvanced ||
		result.PlannerContinuation == nil || !result.PlannerContinuation.Attempted ||
		result.PlannerContinuation.Recommendation == nil ||
		result.PlannerContinuation.Recommendation.Classification != PlannerClassificationAbstain ||
		result.PlannerContinuation.Consumption != nil {
		t.Fatalf("abstaining planner periodic result = %+v, invocations = %d", result, invocations)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)

	waitForOneShotGitStability(t)
	completed, err := RunPlannerPeriodicSourceTick(ctx, pool, input, runner)
	if err != nil {
		t.Fatalf("post-abstain deterministic recovery error = %v", err)
	}
	if invocations != 1 || completed.PlannerContinuation != nil ||
		completed.Run.Status != OrchestrationRunStatusCompleted || !completed.CursorAdvanced {
		t.Fatalf("post-abstain deterministic recovery = %+v, invocations = %d", completed, invocations)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 0)
}

func TestIntegrationPlannerPeriodicTickFailsClosedAndKeepsDurableCoverage(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, primary, _ := registerPlannerPeriodicWorkspace(t, ctx, pool, "fail-closed")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: primary.ID, MaxSteps: 3,
	}
	invocations := 0
	result, err := RunPlannerPeriodicSourceTick(ctx, pool, input, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return []byte(`{"schema_version":"detective-planner-decision-v1","classification":"query_source","source_choice":"source-99"}`), nil
	})
	assertDetectiveKind(t, err, ErrorPlannerUngroundedOutput)
	if invocations != 1 || result.Cycle.ID == "" || result.Run.ID == "" ||
		result.Run.Status != OrchestrationRunStatusRunning || result.Run.StepCount != 1 ||
		result.PlannerContinuation == nil || !result.PlannerContinuation.Attempted ||
		result.PlannerContinuation.Recommendation != nil ||
		result.PlannerContinuation.Consumption != nil {
		t.Fatalf("fail-closed planner periodic result = %+v, invocations = %d", result, invocations)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertPeriodicCursor(t, ctx, pool, workspace.ID, primary.ID, 1, result.Cycle.ID, "", "")

	waitForOneShotGitStability(t)
	completed, err := RunPlannerPeriodicSourceTick(ctx, pool, input, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return nil, errors.New("planner must not run after deterministic coverage completes")
	})
	if err != nil {
		t.Fatalf("post-failure deterministic recovery error = %v", err)
	}
	if invocations != 1 || completed.PlannerContinuation != nil ||
		completed.Run.Status != OrchestrationRunStatusCompleted || !completed.CursorAdvanced {
		t.Fatalf("post-failure deterministic recovery = %+v, invocations = %d", completed, invocations)
	}
}

func TestIntegrationPlannerPeriodicTickResumesCommittedPlanWithoutModel(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, primary, textSource := registerPlannerPeriodicWorkspace(t, ctx, pool, "resume")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: primary.ID, MaxSteps: 3,
	}
	first, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("initial RunPeriodicSourceTick() error = %v", err)
	}
	if first.Run.Status != OrchestrationRunStatusRunning || first.Run.StepCount != 1 {
		t.Fatalf("initial periodic result = %+v", first)
	}
	recommendation, err := RecommendNextSource(ctx, pool, first.Run.ID, func(_ context.Context, plannerInput PlannerInput) ([]byte, error) {
		choice, ok := plannerChoiceByCapability(plannerInput, SourceCapabilityLocalPRDText, nil)
		if !ok {
			t.Fatal("planner input has no local-text choice")
		}
		return plannerDecisionJSON(PlannerClassificationQuerySource, choice), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	request, err := preparePlannerRecommendationConsumption(PlannerRecommendationConsumptionInput{
		RequestID:      periodicPlannerConsumptionRequestID(first.Cycle.ID, recommendation.PlanningContextHash),
		Recommendation: recommendation,
	})
	if err != nil {
		t.Fatalf("preparePlannerRecommendationConsumption() error = %v", err)
	}
	resolved, err := ResolveWorkspace(ctx, pool, workspace.ID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	source, found := workspaceSourceByID(resolved.Sources, textSource.ID)
	if !found {
		t.Fatalf("text source %s was not resolved", textSource.ID)
	}
	authorityKind, authorityRef, err := plannerSourceAuthority(ctx, resolved, source)
	if err != nil {
		t.Fatalf("plannerSourceAuthority() error = %v", err)
	}
	planned, err := consumePlannerRecommendationPlan(ctx, pool, request, authorityKind, authorityRef)
	if err != nil {
		t.Fatalf("consumePlannerRecommendationPlan() error = %v", err)
	}
	if planned.Step.Status != OrchestrationStepStatusPlanned || planned.SourceInvoked {
		t.Fatalf("committed periodic planner plan = %+v", planned)
	}

	invocations := 0
	resumed, err := RunPlannerPeriodicSourceTick(ctx, pool, input, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return nil, errors.New("planner must not run while a durable plan is pending")
	})
	if err != nil {
		t.Fatalf("resumed RunPlannerPeriodicSourceTick() error = %v", err)
	}
	if invocations != 0 || resumed.PlannerContinuation == nil ||
		resumed.PlannerContinuation.Attempted || !resumed.PlannerContinuation.Resumed ||
		resumed.PlannerContinuation.Recommendation != nil ||
		resumed.PlannerContinuation.Consumption == nil ||
		!resumed.PlannerContinuation.Consumption.Replayed ||
		resumed.Run.Status != OrchestrationRunStatusCompleted ||
		!resumed.CursorAdvanced || !resumed.SourceInvoked {
		t.Fatalf("resumed periodic planner result = %+v, invocations = %d", resumed, invocations)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationPlannerPeriodicTickFinalizesTerminalConsumptionWithoutModel(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, primary, _ := registerPlannerPeriodicWorkspace(t, ctx, pool, "terminal-resume")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: primary.ID, MaxSteps: 3,
	}
	first, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("initial RunPeriodicSourceTick() error = %v", err)
	}
	recommendation, err := RecommendNextSource(ctx, pool, first.Run.ID, func(_ context.Context, plannerInput PlannerInput) ([]byte, error) {
		choice, ok := plannerChoiceByCapability(plannerInput, SourceCapabilityLocalPRDText, nil)
		if !ok {
			t.Fatal("planner input has no local-text choice")
		}
		return plannerDecisionJSON(PlannerClassificationQuerySource, choice), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	consumed, err := ConsumePlannerRecommendation(ctx, pool, PlannerRecommendationConsumptionInput{
		RequestID:      periodicPlannerConsumptionRequestID(first.Cycle.ID, recommendation.PlanningContextHash),
		Recommendation: recommendation,
	})
	if err != nil {
		t.Fatalf("ConsumePlannerRecommendation() error = %v", err)
	}
	if consumed.Run.Status != OrchestrationRunStatusCompleted || !consumed.SourceInvoked {
		t.Fatalf("terminal planner consumption = %+v", consumed)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, primary.ID, 1, first.Cycle.ID, "", "")

	invocations := 0
	recovered, err := RunPlannerPeriodicSourceTick(ctx, pool, input, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return nil, errors.New("planner must not run for a terminal durable run")
	})
	if err != nil {
		t.Fatalf("terminal planner periodic recovery error = %v", err)
	}
	if invocations != 0 || !recovered.Replayed || !recovered.CursorAdvanced ||
		recovered.SourceInvoked || recovered.PlannerContinuation != nil ||
		recovered.Run.Status != OrchestrationRunStatusCompleted ||
		recovered.Cycle.Status != OrchestrationRunStatusCompleted {
		t.Fatalf("terminal planner periodic recovery = %+v, invocations = %d", recovered, invocations)
	}
	assertPeriodicCursor(t, ctx, pool, workspace.ID, primary.ID, 1, "", recovered.Cycle.ID, recovered.Cycle.SourceToken)
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationConcurrentPlannerPeriodicTicksConvergeOnOneConsumption(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, primary, textSource := registerPlannerPeriodicWorkspace(t, ctx, pool, "concurrent")
	input := PeriodicSourceTickInput{
		WorkspaceID: workspace.ID, SourceBindingID: primary.ID, MaxSteps: 3,
	}
	first, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("initial RunPeriodicSourceTick() error = %v", err)
	}
	if first.Run.Status != OrchestrationRunStatusRunning {
		t.Fatalf("initial concurrent periodic result = %+v", first)
	}
	recommendation, err := RecommendNextSource(ctx, pool, first.Run.ID, func(_ context.Context, plannerInput PlannerInput) ([]byte, error) {
		choice, ok := plannerChoiceByCapability(plannerInput, SourceCapabilityLocalPRDText, nil)
		if !ok {
			t.Fatal("planner input has no local-text choice")
		}
		return plannerDecisionJSON(PlannerClassificationQuerySource, choice), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	request, err := preparePlannerRecommendationConsumption(PlannerRecommendationConsumptionInput{
		RequestID:      periodicPlannerConsumptionRequestID(first.Cycle.ID, recommendation.PlanningContextHash),
		Recommendation: recommendation,
	})
	if err != nil {
		t.Fatalf("preparePlannerRecommendationConsumption() error = %v", err)
	}
	resolved, err := ResolveWorkspace(ctx, pool, workspace.ID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	source, found := workspaceSourceByID(resolved.Sources, textSource.ID)
	if !found {
		t.Fatalf("text source %s was not resolved", textSource.ID)
	}
	authorityKind, authorityRef, err := plannerSourceAuthority(ctx, resolved, source)
	if err != nil {
		t.Fatalf("plannerSourceAuthority() error = %v", err)
	}
	planned, err := consumePlannerRecommendationPlan(ctx, pool, request, authorityKind, authorityRef)
	if err != nil {
		t.Fatalf("consumePlannerRecommendationPlan() error = %v", err)
	}
	if planned.Step.Status != OrchestrationStepStatusPlanned {
		t.Fatalf("concurrent committed plan = %+v", planned)
	}

	type outcome struct {
		result PeriodicSourceTickResult
		err    error
	}
	start := make(chan struct{})
	modelInvoked := make(chan struct{}, 2)
	outcomes := make(chan outcome, 2)
	runner := func(context.Context, PlannerInput) ([]byte, error) {
		modelInvoked <- struct{}{}
		return nil, errors.New("planner must not run while a durable plan is pending")
	}
	for range 2 {
		go func() {
			<-start
			result, runErr := RunPlannerPeriodicSourceTick(ctx, pool, input, runner)
			outcomes <- outcome{result: result, err: runErr}
		}()
	}
	close(start)

	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent planner tick error = %v", got.err)
		}
		if got.result.Run.Status != OrchestrationRunStatusCompleted || !got.result.CursorAdvanced {
			t.Fatalf("successful concurrent planner tick = %+v", got.result)
		}
	}
	if len(modelInvoked) != 0 {
		t.Fatalf("concurrent planner recovery invoked model %d times", len(modelInvoked))
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_periodic_source_cycles", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_runs", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)

	replay, err := RunPeriodicSourceTick(ctx, pool, input)
	if err != nil {
		t.Fatalf("concurrent periodic terminal replay error = %v", err)
	}
	if !replay.Replayed || !replay.CursorAdvanced || replay.Run.Status != OrchestrationRunStatusCompleted {
		t.Fatalf("concurrent periodic terminal replay = %+v", replay)
	}
}

func registerPlannerPeriodicWorkspace(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
) (Workspace, WorkspaceSourceBinding, WorkspaceSourceBinding) {
	t.Helper()
	root := gitWorkspaceFixture(t)
	files := map[string]string{
		"main.go":         "package sample\n\nfunc Evidence() string { return \"source backed\" }\n",
		"requirements.md": "Planner periodic requirement remains source backed.\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatalf("write planner periodic %s: %v", path, err)
		}
	}
	runOneShotGitCommand(t, root, "add", "main.go", "requirements.md")
	runOneShotGitCommand(t, root, "commit", "-q", "-m", "add planner periodic sources")
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID:     "register-planner-periodic-" + suffix,
		WorkspaceID:   "workspace:planner-periodic-" + suffix,
		WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{
			{
				CapabilityName: SourceCapabilityGitGoRepository, CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
				SourceID: "repository-a", RelativePath: ".",
			},
			{
				CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID: "requirements", RelativePath: "requirements.md",
			},
		},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	primary, found := workspaceSourceByLogicalID(registered.Workspace.Sources, "repository-a")
	if !found {
		t.Fatal("registered workspace has no primary Git source")
	}
	textSource, found := workspaceSourceByLogicalID(registered.Workspace.Sources, "requirements")
	if !found {
		t.Fatal("registered workspace has no local-text source")
	}
	return registered.Workspace, primary, textSource
}

func plannerChoiceByCapability(
	input PlannerInput,
	capability string,
	excluded map[string]bool,
) (string, bool) {
	for _, source := range input.Sources {
		if source.CapabilityName == capability && !excluded[source.ChoiceKey] {
			return source.ChoiceKey, true
		}
	}
	return "", false
}

func assertPlannerPeriodicInputOpaque(t *testing.T, input PlannerInput, workspace Workspace) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal planner input: %v", err)
	}
	visible := string(data)
	for _, forbidden := range []string{
		workspace.RootPath,
		"requirements.md",
		"Planner periodic requirement remains source backed.",
	} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("planner input exposed %q: %s", forbidden, visible)
		}
	}
	for _, source := range workspace.Sources {
		if strings.Contains(visible, source.ID) {
			t.Fatalf("planner input exposed source binding %q: %s", source.ID, visible)
		}
	}
}
