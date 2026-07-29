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

func TestIntegrationRecommendNextSourceUsesRegisteredAuditCoverage(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "coverage")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-coverage", WorkspaceID: workspace.ID, MaxSteps: 2,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}

	first, err := RecommendNextSource(ctx, pool, started.Run.ID, func(_ context.Context, input PlannerInput) ([]byte, error) {
		assertPlannerInputHasNoAuthority(t, input, workspace)
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
	})
	if err != nil {
		t.Fatalf("initial RecommendNextSource() error = %v", err)
	}
	if first.SourceBindingID != workspace.Sources[0].ID || first.ReasonCode != OrchestrationReasonInitialSource || first.PlanningContextHash == "" {
		t.Fatalf("initial recommendation = %+v", first)
	}
	resolved, err := ResolveOrchestrationRun(ctx, pool, started.Run.ID)
	if err != nil {
		t.Fatalf("ResolveOrchestrationRun() error = %v", err)
	}
	if resolved.StepCount != 0 {
		t.Fatalf("recommendation mutated run = %+v", resolved)
	}

	planned, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID: "plan-planner-coverage-1", RunID: started.Run.ID,
		SourceBindingID: first.SourceBindingID, ReasonCode: first.ReasonCode,
		InputAuthorityKind: OrchestrationInputAuthorityNone,
	})
	if err != nil {
		t.Fatalf("PlanOrchestrationStep() error = %v", err)
	}
	completed, err := CompleteOrchestrationStep(ctx, pool, completeInput(
		"complete-planner-coverage-1", started.Run.ID, planned.Step.ID,
		OrchestrationStepOutcomeContinue, "", false,
	))
	if err != nil {
		t.Fatalf("CompleteOrchestrationStep() error = %v", err)
	}
	if completed.Run.Status != OrchestrationRunStatusRunning {
		t.Fatalf("continued run = %+v", completed.Run)
	}

	second, err := RecommendNextSource(ctx, pool, started.Run.ID, func(_ context.Context, input PlannerInput) ([]byte, error) {
		assertPlannerInputHasNoAuthority(t, input, workspace)
		if input.RemainingStepBudget != 1 || len(input.ObservedCoverage) != 1 || input.ObservedCoverage[0].SourceChoice != "source-01" {
			t.Fatalf("continued planner input = %+v", input)
		}
		return plannerDecisionJSON(PlannerClassificationQuerySource, "source-02"), nil
	})
	if err != nil {
		t.Fatalf("coverage-gap RecommendNextSource() error = %v", err)
	}
	if second.SourceBindingID != workspace.Sources[1].ID || second.ReasonCode != OrchestrationReasonCoverageGap || second.PlanningContextHash == first.PlanningContextHash {
		t.Fatalf("coverage-gap recommendation = %+v; initial = %+v", second, first)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 3)
}

func TestIntegrationRecommendNextSourceRejectsAuditChangeDuringModelCall(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "concurrent")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-concurrent", WorkspaceID: workspace.ID, MaxSteps: 2,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}

	invoked := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		result PlannerRecommendation
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		recommendation, err := RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
			close(invoked)
			<-release
			return plannerDecisionJSON(PlannerClassificationQuerySource, "source-01"), nil
		})
		result <- outcome{result: recommendation, err: err}
	}()
	<-invoked
	if _, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID: "plan-planner-concurrent", RunID: started.Run.ID,
		SourceBindingID: workspace.Sources[0].ID,
		ReasonCode:      OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
	}); err != nil {
		t.Fatalf("concurrent PlanOrchestrationStep() error = %v", err)
	}
	close(release)
	got := <-result
	assertDetectiveKind(t, got.err, ErrorOrchestrationRunConflict)
	if got.result != (PlannerRecommendation{}) {
		t.Fatalf("stale recommendation returned = %+v", got.result)
	}
}

func TestIntegrationRecommendNextSourceDoesNotInvokePlannerForTerminalRun(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "terminal")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-terminal", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	planned, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID: "plan-planner-terminal", RunID: started.Run.ID,
		SourceBindingID: workspace.Sources[0].ID,
		ReasonCode:      OrchestrationReasonInitialSource, InputAuthorityKind: OrchestrationInputAuthorityNone,
	})
	if err != nil {
		t.Fatalf("PlanOrchestrationStep() error = %v", err)
	}
	completion := completeInput(
		"complete-planner-terminal", started.Run.ID, planned.Step.ID,
		OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied, true,
	)
	if _, err := CompleteOrchestrationStep(ctx, pool, completion); err != nil {
		t.Fatalf("CompleteOrchestrationStep() error = %v", err)
	}
	invocations := 0
	_, err = RecommendNextSource(ctx, pool, started.Run.ID, func(context.Context, PlannerInput) ([]byte, error) {
		invocations++
		return plannerDecisionJSON(PlannerClassificationAbstain, ""), nil
	})
	assertDetectiveKind(t, err, ErrorOrchestrationRunConflict)
	if invocations != 0 {
		t.Fatalf("terminal planner invocations = %d, want 0", invocations)
	}
}

func TestIntegrationRecommendNextSourceFailsClosedWithoutAuditMutation(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	workspace := registerPlannerWorkspace(t, ctx, pool, "fail-closed")
	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: "start-planner-fail-closed", WorkspaceID: workspace.ID, MaxSteps: 1,
	})
	if err != nil {
		t.Fatalf("StartOrchestrationRun() error = %v", err)
	}
	tests := []struct {
		name   string
		runner PlannerRunner
		kind   ErrorKind
	}{
		{
			name: "invocation",
			runner: func(context.Context, PlannerInput) ([]byte, error) {
				return nil, errors.New("provider unavailable")
			},
			kind: ErrorPlannerInvocationFailed,
		},
		{
			name: "malformed",
			runner: func(context.Context, PlannerInput) ([]byte, error) {
				return []byte("```json\n{}\n```"), nil
			},
			kind: ErrorPlannerMalformedOutput,
		},
		{
			name: "contradictory",
			runner: func(context.Context, PlannerInput) ([]byte, error) {
				return plannerDecisionJSON(PlannerClassificationAbstain, "source-01"), nil
			},
			kind: ErrorPlannerContradictoryOutput,
		},
		{
			name: "ungrounded",
			runner: func(context.Context, PlannerInput) ([]byte, error) {
				return plannerDecisionJSON(PlannerClassificationQuerySource, "source-99"), nil
			},
			kind: ErrorPlannerUngroundedOutput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RecommendNextSource(ctx, pool, started.Run.ID, test.runner)
			assertDetectiveKind(t, err, test.kind)
		})
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_steps", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_orchestration_transition_requests", 1)
}

func registerPlannerWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) Workspace {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name+".txt"), []byte(name+" requirement\n"), 0o600); err != nil {
			t.Fatalf("write planner source %s: %v", name, err)
		}
	}
	registered, err := RegisterWorkspace(ctx, pool, WorkspaceRegistrationInput{
		RequestID: "register-planner-" + suffix, WorkspaceID: "workspace:planner-" + suffix, WorkspaceRoot: root,
		Sources: []WorkspaceSourceRegistrationInput{
			{
				CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID: "a", RelativePath: "a.txt",
			},
			{
				CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceID: "b", RelativePath: "b.txt",
			},
		},
	})
	if err != nil {
		t.Fatalf("RegisterWorkspace() error = %v", err)
	}
	return registered.Workspace
}

func assertPlannerInputHasNoAuthority(t *testing.T, input PlannerInput, workspace Workspace) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal planner input: %v", err)
	}
	visible := string(data)
	for _, source := range workspace.Sources {
		for _, forbidden := range []string{source.ID, source.SourceID + `.txt`, source.RelativePath} {
			if strings.Contains(visible, forbidden) {
				t.Fatalf("planner input exposed %q: %s", forbidden, visible)
			}
		}
	}
}

func plannerDecisionJSON(classification, sourceChoice string) []byte {
	data, _ := json.Marshal(PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: classification, SourceChoice: sourceChoice,
	})
	return data
}
