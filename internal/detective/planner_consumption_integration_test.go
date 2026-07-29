//go:build integration

package detective

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestIntegrationConsumePlannerRecommendationRunsLocalTextAndReplaysTerminalAudit(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "consume-local")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-consume-local", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	invocations := 0
	recommendation, err := RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	input := PlannerRecommendationConsumptionInput{
		RequestID:      "consume-planner-local",
		Recommendation: recommendation,
	}
	consumed, err := ConsumePlannerRecommendation(ctx, pool, input)
	if err != nil {
		t.Fatalf("ConsumePlannerRecommendation() error = %v", err)
	}
	if invocations != 1 || consumed.Replayed || !consumed.SourceInvoked ||
		consumed.Run.Status != OrchestrationRunStatusCompleted ||
		consumed.Run.StopReason != OrchestrationStopReasonCoverageSatisfied ||
		consumed.Step.SourceBindingID != recommendation.SourceBindingID ||
		consumed.Step.ReasonCode != recommendation.ReasonCode ||
		consumed.Step.Status != OrchestrationStepStatusCompleted ||
		consumed.PlanningContextHash != recommendation.PlanningContextHash ||
		!consumed.ConsumedAt.Equal(consumed.Step.PlannedAt) {
		t.Fatalf("local planner consumption = %+v, invocations = %d", consumed, invocations)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)

	if err := os.RemoveAll(workspace.RootPath); err != nil {
		t.Fatalf("remove terminal planner workspace: %v", err)
	}
	replay, err := ConsumePlannerRecommendation(ctx, pool, input)
	if err != nil {
		t.Fatalf("terminal ConsumePlannerRecommendation() replay error = %v", err)
	}
	if !replay.Replayed || replay.SourceInvoked || !reflect.DeepEqual(replay.Run, consumed.Run) || replay.Step.ID != consumed.Step.ID {
		t.Fatalf("terminal planner replay = %+v, want %+v", replay, consumed)
	}

	reused := input
	reused.Recommendation.ReasonCode = OrchestrationReasonCoverageGap
	_, err = ConsumePlannerRecommendation(ctx, pool, reused)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)
}

func TestIntegrationConsumePlannerRecommendationResumesAfterPlanCommit(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "consume-resume")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-consume-resume", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	recommendation, err := RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	request, err := preparePlannerRecommendationConsumption(PlannerRecommendationConsumptionInput{
		RequestID: "consume-planner-resume", Recommendation: recommendation,
	})
	if err != nil {
		t.Fatalf("preparePlannerRecommendationConsumption() error = %v", err)
	}
	resolved, err := ResolveWorkspace(ctx, pool, workspace.ID)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	source, found := workspaceSourceByID(resolved.Sources, recommendation.SourceBindingID)
	if !found {
		t.Fatalf("recommended source %s was not resolved", recommendation.SourceBindingID)
	}
	authorityKind, authorityRef, err := plannerSourceAuthority(ctx, resolved, source)
	if err != nil {
		t.Fatalf("plannerSourceAuthority() error = %v", err)
	}
	planned, err := consumePlannerRecommendationPlan(ctx, pool, request, authorityKind, authorityRef)
	if err != nil {
		t.Fatalf("consumePlannerRecommendationPlan() error = %v", err)
	}
	if planned.Step.Status != OrchestrationStepStatusPlanned || planned.Replayed || planned.SourceInvoked {
		t.Fatalf("committed planner plan = %+v", planned)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)

	resumed, err := ConsumePlannerRecommendation(ctx, pool, request.input)
	if err != nil {
		t.Fatalf("resumed ConsumePlannerRecommendation() error = %v", err)
	}
	if !resumed.Replayed || !resumed.SourceInvoked || resumed.Run.Status != OrchestrationRunStatusCompleted || resumed.Step.ID != planned.Step.ID {
		t.Fatalf("resumed planner consumption = %+v, plan = %+v", resumed, planned)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationConcurrentPlannerRecommendationConsumersHaveOneWinner(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "consume-concurrent")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-consume-concurrent", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	recommendation, err := RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	type outcome struct {
		result PlannerRecommendationConsumptionResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for index := range 2 {
		go func() {
			result, err := ConsumePlannerRecommendation(ctx, pool, PlannerRecommendationConsumptionInput{
				RequestID:      "consume-planner-concurrent-" + string(rune('a'+index)),
				Recommendation: recommendation,
			})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	winners := 0
	conflicts := 0
	for range 2 {
		got := <-outcomes
		if got.err == nil {
			winners++
			if got.result.Run.Status != OrchestrationRunStatusCompleted {
				t.Fatalf("winning planner consumption = %+v", got.result)
			}
			continue
		}
		if kind, ok := KindOf(got.err); ok && kind == ErrorOrchestrationRunConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent planner consumption error = %v", got.err)
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent planner consumers winners/conflicts = %d/%d, want 1/1", winners, conflicts)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationConsumePlannerRecommendationRejectsStaleOrTamperedAuthority(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "consume-fail-closed")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-consume-fail-closed", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	recommendation, err := RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*PlannerRecommendation)
		kind   ErrorKind
	}{
		{
			name: "stale context",
			mutate: func(value *PlannerRecommendation) {
				value.PlanningContextHash = "sha256:" + strings.Repeat("f", 64)
			},
			kind: ErrorOrchestrationRunConflict,
		},
		{
			name: "unknown source",
			mutate: func(value *PlannerRecommendation) {
				value.SourceBindingID = "workspace-source:" + strings.Repeat("f", 64)
			},
			kind: ErrorSourceBindingNotFound,
		},
		{
			name: "capability",
			mutate: func(value *PlannerRecommendation) {
				value.CapabilityName = SourceCapabilityGitGoRepository
				value.CapabilityVersion = SourceCapabilityGitGoRepositoryVersion
			},
			kind: ErrorPlannerUngroundedOutput,
		},
		{
			name: "reason",
			mutate: func(value *PlannerRecommendation) {
				value.ReasonCode = OrchestrationReasonCoverageGap
			},
			kind: ErrorPlannerUngroundedOutput,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := recommendation
			test.mutate(&changed)
			_, err := ConsumePlannerRecommendation(ctx, pool, PlannerRecommendationConsumptionInput{
				RequestID:      "consume-planner-fail-closed-" + string(rune('a'+index)),
				Recommendation: changed,
			})
			assertDetectiveKind(t, err, test.kind)
		})
	}
	abstain := recommendation
	abstain.Classification = PlannerClassificationAbstain
	_, err = ConsumePlannerRecommendation(ctx, pool, PlannerRecommendationConsumptionInput{
		RequestID: "consume-planner-fail-closed-abstain", Recommendation: abstain,
	})
	assertDetectiveKind(t, err, ErrorPlannerInsufficientCoverage)
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 0)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationConsumePlannerRecommendationSelectsGitGoAdapter(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace, _, _ := registerOneShotGitWorkspace(t, ctx, pool, "planner-consume")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-consume-git", WorkspaceID: workspace.ID, MaxSteps: 2,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	recommendation, err := RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
	})
	if err != nil {
		t.Fatalf("RecommendNextSource() error = %v", err)
	}
	consumed, err := ConsumePlannerRecommendation(ctx, pool, PlannerRecommendationConsumptionInput{
		RequestID: "consume-planner-git", Recommendation: recommendation,
	})
	if err != nil {
		t.Fatalf("ConsumePlannerRecommendation() error = %v", err)
	}
	if !consumed.SourceInvoked || consumed.Run.Status != OrchestrationRunStatusRunning ||
		consumed.Step.CapabilityName != SourceCapabilityGitGoRepository ||
		consumed.Step.Status != OrchestrationStepStatusCompleted ||
		consumed.Step.Outcome != OrchestrationStepOutcomeContinue ||
		consumed.Step.Coverage == nil ||
		consumed.Step.Coverage.UnitKind != OrchestrationCoverageUnitRepositoryFiles ||
		consumed.Step.Coverage.Complete {
		t.Fatalf("Git planner consumption = %+v", consumed)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_planner_recommendation_consumptions", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_change_observations", 1)
	assertDetectiveTableCount(t, ctx, pool, "repository_extraction_work_items", 0)
	assertDetectiveTableCount(t, ctx, pool, "repository_source_generations", 0)
}
