package detective

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBuildPlannerContextHidesControllerAuthority(t *testing.T) {
	workspace := plannerTestWorkspace()
	run := plannerTestRun(workspace.ID, 2)
	context, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("buildPlannerContext() error = %v", err)
	}
	data, err := json.Marshal(context.input)
	if err != nil {
		t.Fatalf("marshal planner input: %v", err)
	}
	visible := string(data)
	for _, forbidden := range []string{
		workspace.ID,
		workspace.RootPath,
		workspace.Sources[0].ID,
		workspace.Sources[0].SourceID,
		workspace.Sources[0].RelativePath,
		`"capability_version"`,
		`"source_binding_id"`,
	} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("planner input exposed %q: %s", forbidden, visible)
		}
	}
	if len(context.input.Sources) != 2 || context.input.Sources[0].ChoiceKey != "source-01" || context.input.Sources[1].ChoiceKey != "source-02" {
		t.Fatalf("planner source choices = %+v", context.input.Sources)
	}
	if context.hash == "" || !strings.HasPrefix(context.hash, "sha256:") {
		t.Fatalf("planner context hash = %q", context.hash)
	}
}

func TestValidatePlannerDecisionMapsOnlyUnqueriedChoices(t *testing.T) {
	workspace := plannerTestWorkspace()
	run := plannerTestRun(workspace.ID, 2)
	initial, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("build initial context: %v", err)
	}
	got, err := validatePlannerDecision(run.ID, initial, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationQuerySource, SourceChoice: "source-01",
	})
	if err != nil {
		t.Fatalf("validate initial decision: %v", err)
	}
	if got.SourceBindingID != workspace.Sources[0].ID || got.CapabilityVersion != SourceCapabilityGitGoRepositoryVersion || got.ReasonCode != OrchestrationReasonInitialSource {
		t.Fatalf("initial recommendation = %+v", got)
	}

	run.StepCount = 1
	run.Steps = []OrchestrationStep{plannerTestContinuingStep(run, workspace.Sources[0], 1)}
	continued, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("build continued context: %v", err)
	}
	got, err = validatePlannerDecision(run.ID, continued, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationQuerySource, SourceChoice: "source-02",
	})
	if err != nil {
		t.Fatalf("validate coverage-gap decision: %v", err)
	}
	if got.SourceBindingID != workspace.Sources[1].ID || got.ReasonCode != OrchestrationReasonCoverageGap {
		t.Fatalf("coverage-gap recommendation = %+v", got)
	}

	_, err = validatePlannerDecision(run.ID, continued, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationQuerySource, SourceChoice: "source-01",
	})
	assertDetectiveKind(t, err, ErrorPlannerUngroundedOutput)
}

func TestValidatePlannerDecisionSupportsStrictAbstention(t *testing.T) {
	workspace := plannerTestWorkspace()
	run := plannerTestRun(workspace.ID, 1)
	context, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("buildPlannerContext() error = %v", err)
	}
	got, err := validatePlannerDecision(run.ID, context, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationAbstain,
	})
	if err != nil {
		t.Fatalf("validate abstain: %v", err)
	}
	if got.Classification != PlannerClassificationAbstain || got.SourceBindingID != "" || got.ReasonCode != "" {
		t.Fatalf("abstain recommendation = %+v", got)
	}

	_, err = validatePlannerDecision(run.ID, context, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationAbstain, SourceChoice: "source-01",
	})
	assertDetectiveKind(t, err, ErrorPlannerContradictoryOutput)
	_, err = validatePlannerDecision(run.ID, context, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationQuerySource,
	})
	assertDetectiveKind(t, err, ErrorPlannerContradictoryOutput)
	_, err = validatePlannerDecision(run.ID, context, PlannerDecision{
		SchemaVersion: PlannerDecisionSchemaVersion, Classification: PlannerClassificationQuerySource, SourceChoice: "source-99",
	})
	assertDetectiveKind(t, err, ErrorPlannerUngroundedOutput)
}

func TestDecodePlannerDecisionRejectsNonCanonicalJSON(t *testing.T) {
	valid := `{"schema_version":"detective-planner-decision-v1","classification":"abstain","source_choice":""}`
	if _, err := decodePlannerDecision([]byte(valid)); err != nil {
		t.Fatalf("decode valid planner decision: %v", err)
	}
	tests := []string{
		``,
		`{`,
		"```json\n" + valid + "\n```",
		valid + valid,
		`{"schema_version":"detective-planner-decision-v2","classification":"abstain","source_choice":""}`,
		`{"schema_version":"detective-planner-decision-v1","classification":"abstain","source_choice":"","explanation":"because"}`,
	}
	for _, data := range tests {
		_, err := decodePlannerDecision([]byte(data))
		assertDetectiveKind(t, err, ErrorPlannerMalformedOutput)
	}
	_, err := decodePlannerDecision(make([]byte, maxPlannerOutputBytes+1))
	assertDetectiveKind(t, err, ErrorPlannerMalformedOutput)
}

func TestValidatePlannerOutputUsesGroundedInputContract(t *testing.T) {
	workspace := plannerTestWorkspace()
	run := plannerTestRun(workspace.ID, 2)
	run.StepCount = 1
	run.Steps = []OrchestrationStep{plannerTestContinuingStep(run, workspace.Sources[0], 1)}
	context, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("build planner context: %v", err)
	}
	valid := plannerUnitDecisionJSON(t, PlannerClassificationQuerySource, "source-02")
	decision, err := ValidatePlannerOutput(context.input, valid)
	if err != nil {
		t.Fatalf("ValidatePlannerOutput() error = %v", err)
	}
	if decision.Classification != PlannerClassificationQuerySource || decision.SourceChoice != "source-02" {
		t.Fatalf("decision = %+v", decision)
	}

	_, err = ValidatePlannerOutput(context.input, []byte("```json\n"+string(valid)+"\n```"))
	assertDetectiveKind(t, err, ErrorPlannerMalformedOutput)
	_, err = ValidatePlannerOutput(
		context.input,
		plannerUnitDecisionJSON(t, PlannerClassificationQuerySource, "source-01"),
	)
	assertDetectiveKind(t, err, ErrorPlannerUngroundedOutput)

	decision, err = ValidatePlannerOutput(
		context.input,
		plannerUnitDecisionJSON(t, PlannerClassificationAbstain, ""),
	)
	if err != nil || decision.Classification != PlannerClassificationAbstain {
		t.Fatalf("abstain decision = %+v, err = %v", decision, err)
	}
}

func plannerUnitDecisionJSON(t *testing.T, classification, sourceChoice string) []byte {
	t.Helper()
	data, err := json.Marshal(PlannerDecision{
		SchemaVersion:  PlannerDecisionSchemaVersion,
		Classification: classification,
		SourceChoice:   sourceChoice,
	})
	if err != nil {
		t.Fatalf("marshal planner decision: %v", err)
	}
	return data
}

func TestValidatePlannerInputRejectsUnsupportedOrInconsistentContext(t *testing.T) {
	workspace := plannerTestWorkspace()
	run := plannerTestRun(workspace.ID, 2)
	context, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("buildPlannerContext() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*PlannerInput)
		kind   ErrorKind
	}{
		{name: "schema", mutate: func(input *PlannerInput) { input.SchemaVersion = "v2" }, kind: ErrorInvalidInput},
		{name: "catalog", mutate: func(input *PlannerInput) { input.CapabilityCatalogVersion = "dynamic" }, kind: ErrorInvalidInput},
		{name: "budget", mutate: func(input *PlannerInput) { input.RemainingStepBudget = 0 }, kind: ErrorInvalidInput},
		{name: "choice", mutate: func(input *PlannerInput) { input.Sources[0].ChoiceKey = "binding-id" }, kind: ErrorInvalidInput},
		{name: "capability", mutate: func(input *PlannerInput) { input.Sources[0].CapabilityName = "jira" }, kind: ErrorUnsupportedCapability},
		{name: "coverage unit", mutate: func(input *PlannerInput) { input.Sources[0].CoverageUnitKind = OrchestrationCoverageUnitTextFiles }, kind: ErrorInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := context.input
			input.Sources = append([]PlannerSourceOption(nil), context.input.Sources...)
			test.mutate(&input)
			err := validatePlannerInput(input)
			assertDetectiveKind(t, err, test.kind)
		})
	}
}

func TestRecommendNextSourceValidatesDependenciesBeforeDatabaseAccess(t *testing.T) {
	runID := orchestrationRunID("planner-public-api")
	_, err := RecommendNextSource(t.Context(), nil, runID, func(context.Context, PlannerInput) ([]byte, error) {
		return nil, nil
	})
	assertDetectiveKind(t, err, ErrorInvalidInput)
	_, err = RecommendNextSource(t.Context(), new(pgxpool.Pool), runID, nil)
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func plannerTestWorkspace() Workspace {
	gitCapability, _ := lookupSourceCapability(SourceCapabilityGitGoRepository, SourceCapabilityGitGoRepositoryVersion)
	textCapability, _ := lookupSourceCapability(SourceCapabilityLocalPRDText, SourceCapabilityLocalPRDTextVersion)
	return Workspace{
		ID: "workspace:planner-test", RootPath: "/private/planner-test",
		Sources: []WorkspaceSourceBinding{
			{
				ID: "workspace-source:" + strings.Repeat("a", 64), WorkspaceID: "workspace:planner-test",
				CapabilityName: SourceCapabilityGitGoRepository, CapabilityVersion: SourceCapabilityGitGoRepositoryVersion,
				SourceSystem: gitCapability.SourceSystem, SourceID: "repository-secret", RelativePath: ".", PathKind: "directory",
			},
			{
				ID: "workspace-source:" + strings.Repeat("b", 64), WorkspaceID: "workspace:planner-test",
				CapabilityName: SourceCapabilityLocalPRDText, CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
				SourceSystem: textCapability.SourceSystem, SourceID: "requirements-secret", RelativePath: "private/prd.txt", PathKind: "file",
			},
		},
	}
}

func plannerTestRun(workspaceID string, maxSteps int) OrchestrationRun {
	return OrchestrationRun{
		ID: orchestrationRunID("planner-test-run"), WorkspaceID: workspaceID,
		Status: OrchestrationRunStatusRunning, MaxSteps: maxSteps, Steps: []OrchestrationStep{},
	}
}

func plannerTestContinuingStep(run OrchestrationRun, source WorkspaceSourceBinding, number int) OrchestrationStep {
	coverage := OrchestrationCoverage{
		SchemaVersion:      OrchestrationCoverageSchemaVersion,
		UnitKind:           orchestrationCoverageUnitKind(source.CapabilityName),
		AttemptedUnitCount: 2, CoveredUnitCount: 1,
	}
	return OrchestrationStep{
		ID: orchestrationStepID(run.ID, number), RunID: run.ID, WorkspaceID: run.WorkspaceID,
		Number: number, SourceBindingID: source.ID,
		CapabilityName: source.CapabilityName, CapabilityVersion: source.CapabilityVersion,
		SourceSystem: source.SourceSystem, SourceID: source.SourceID,
		ReasonCode: OrchestrationReasonInitialSource, Status: OrchestrationStepStatusCompleted,
		Outcome: OrchestrationStepOutcomeContinue, Coverage: &coverage,
	}
}
