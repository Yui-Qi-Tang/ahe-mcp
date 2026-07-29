package detective

import (
	"strings"
	"testing"
)

func TestPrepareOrchestrationRunStartBuildsStableIdentity(t *testing.T) {
	input := OrchestrationRunStartInput{
		RequestID:   " start-run ",
		WorkspaceID: " workspace:test ",
		MaxSteps:    3,
	}
	first, firstID, firstHash, err := prepareOrchestrationRunStart(input)
	if err != nil {
		t.Fatalf("prepareOrchestrationRunStart() error = %v", err)
	}
	second, secondID, secondHash, err := prepareOrchestrationRunStart(input)
	if err != nil {
		t.Fatalf("second prepareOrchestrationRunStart() error = %v", err)
	}
	if first.RequestID != "start-run" || first.WorkspaceID != "workspace:test" || first.MaxSteps != 3 {
		t.Fatalf("prepared start = %+v", first)
	}
	if first != second || firstID != secondID || firstHash != secondHash {
		t.Fatalf("stable start mismatch: %+v/%q/%q != %+v/%q/%q", first, firstID, firstHash, second, secondID, secondHash)
	}
	if _, err := normalizeDetectiveHashID(firstID, "detective-run:", "run_id"); err != nil {
		t.Fatalf("generated run ID is invalid: %v", err)
	}
	if firstHash == "" || !strings.HasPrefix(firstHash, "sha256:") {
		t.Fatalf("payload hash = %q", firstHash)
	}
	if orchestrationStepID(firstID, 1) == orchestrationStepID(firstID, 2) {
		t.Fatal("step IDs for different ordinals are equal")
	}
}

func TestPrepareOrchestrationRunStartRejectsInvalidBounds(t *testing.T) {
	for _, maxSteps := range []int{0, maxOrchestrationSteps + 1} {
		_, _, _, err := prepareOrchestrationRunStart(OrchestrationRunStartInput{
			RequestID: "start", WorkspaceID: "workspace:test", MaxSteps: maxSteps,
		})
		assertDetectiveKind(t, err, ErrorInvalidInput)
	}
}

func TestPrepareOrchestrationRunStartBindsOptionalScopeHash(t *testing.T) {
	scopeHash := "sha256:" + strings.Repeat("a", 64)
	input := OrchestrationRunStartInput{
		RequestID: "start", WorkspaceID: "workspace:test", MaxSteps: 1, ScopeHash: " " + scopeHash + " ",
	}
	prepared, _, scopedHash, err := prepareOrchestrationRunStart(input)
	if err != nil {
		t.Fatalf("prepareOrchestrationRunStart() error = %v", err)
	}
	if prepared.ScopeHash != scopeHash {
		t.Fatalf("prepared scope hash = %q, want %q", prepared.ScopeHash, scopeHash)
	}
	input.ScopeHash = ""
	_, _, unscopedHash, err := prepareOrchestrationRunStart(input)
	if err != nil {
		t.Fatalf("unscoped prepareOrchestrationRunStart() error = %v", err)
	}
	if scopedHash == unscopedHash {
		t.Fatal("scoped and unscoped starts have the same payload hash")
	}

	for _, invalid := range []string{
		"sha256:bad",
		"sha256:" + strings.Repeat("A", 64),
		"md5:" + strings.Repeat("a", 64),
	} {
		input.ScopeHash = invalid
		_, _, _, err := prepareOrchestrationRunStart(input)
		assertDetectiveKind(t, err, ErrorInvalidInput)
	}
}

func TestPrepareOrchestrationStepPlanValidatesClosedAuthority(t *testing.T) {
	runID := orchestrationRunID("run")
	sourceID := "workspace-source:" + strings.Repeat("a", 64)
	valid := OrchestrationStepPlanInput{
		RequestID: "plan", RunID: runID, SourceBindingID: sourceID,
		ReasonCode:         OrchestrationReasonInitialSource,
		InputAuthorityKind: OrchestrationInputAuthoritySourceGeneration,
		InputAuthorityRef:  "generation:source-1",
	}
	prepared, payloadHash, err := prepareOrchestrationStepPlan(valid)
	if err != nil {
		t.Fatalf("prepareOrchestrationStepPlan() error = %v", err)
	}
	if prepared != valid || !strings.HasPrefix(payloadHash, "sha256:") {
		t.Fatalf("prepared plan = %+v, hash = %q", prepared, payloadHash)
	}

	tests := []struct {
		name   string
		mutate func(*OrchestrationStepPlanInput)
	}{
		{name: "unknown reason", mutate: func(input *OrchestrationStepPlanInput) { input.ReasonCode = "model_guess" }},
		{name: "none with ref", mutate: func(input *OrchestrationStepPlanInput) {
			input.InputAuthorityKind = OrchestrationInputAuthorityNone
			input.InputAuthorityRef = "unexpected"
		}},
		{name: "empty token", mutate: func(input *OrchestrationStepPlanInput) {
			input.InputAuthorityKind = OrchestrationInputAuthoritySourceToken
			input.InputAuthorityRef = ""
		}},
		{name: "invalid generation", mutate: func(input *OrchestrationStepPlanInput) { input.InputAuthorityRef = "not-generation" }},
		{name: "uppercase source digest", mutate: func(input *OrchestrationStepPlanInput) {
			input.SourceBindingID = "workspace-source:" + strings.Repeat("A", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, _, err := prepareOrchestrationStepPlan(input)
			assertDetectiveKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestPrepareOrchestrationStepCompleteAcceptsClosedOutcomes(t *testing.T) {
	runID := orchestrationRunID("run")
	stepID := orchestrationStepID(runID, 1)
	tests := []OrchestrationStepCompleteInput{
		completeInput("continue", runID, stepID, OrchestrationStepOutcomeContinue, "", false),
		completeInput("completed", runID, stepID, OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied, true),
		completeInput("failed", runID, stepID, OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed, false),
		completeInput("stopped", runID, stepID, OrchestrationStepOutcomeStopped, OrchestrationStopReasonUngroundedOutput, false),
	}
	for _, input := range tests {
		prepared, payloadHash, err := prepareOrchestrationStepComplete(input)
		if err != nil {
			t.Fatalf("prepareOrchestrationStepComplete(%s) error = %v", input.Outcome, err)
		}
		if prepared.Outcome != input.Outcome || !strings.HasPrefix(payloadHash, "sha256:") {
			t.Fatalf("prepared completion = %+v, hash = %q", prepared, payloadHash)
		}
	}
}

func TestPrepareOrchestrationStepCompleteRejectsUnboundedOrInconsistentCoverage(t *testing.T) {
	runID := orchestrationRunID("run")
	stepID := orchestrationStepID(runID, 1)
	valid := completeInput("complete", runID, stepID, OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied, true)
	tests := []struct {
		name   string
		mutate func(*OrchestrationStepCompleteInput)
	}{
		{name: "unknown schema", mutate: func(input *OrchestrationStepCompleteInput) { input.Coverage.SchemaVersion = "model-json" }},
		{name: "unknown unit", mutate: func(input *OrchestrationStepCompleteInput) { input.Coverage.UnitKind = "claims" }},
		{name: "covered exceeds attempted", mutate: func(input *OrchestrationStepCompleteInput) { input.Coverage.CoveredUnitCount = 4 }},
		{name: "sum exceeds attempted", mutate: func(input *OrchestrationStepCompleteInput) { input.Coverage.UnsupportedUnitCount = 2 }},
		{name: "complete misses units", mutate: func(input *OrchestrationStepCompleteInput) { input.Coverage.CoveredUnitCount = 2 }},
		{name: "completed has unsupported", mutate: func(input *OrchestrationStepCompleteInput) {
			input.Coverage.CoveredUnitCount = 2
			input.Coverage.UnsupportedUnitCount = 1
		}},
		{name: "completed incomplete", mutate: func(input *OrchestrationStepCompleteInput) { input.Coverage.Complete = false }},
		{name: "completed wrong reason", mutate: func(input *OrchestrationStepCompleteInput) { input.StopReason = OrchestrationStopReasonSourceFailed }},
		{name: "continue complete", mutate: func(input *OrchestrationStepCompleteInput) {
			input.Outcome = OrchestrationStepOutcomeContinue
			input.StopReason = ""
		}},
		{name: "failed wrong reason", mutate: func(input *OrchestrationStepCompleteInput) {
			input.Outcome = OrchestrationStepOutcomeFailed
			input.StopReason = OrchestrationStopReasonCoverageSatisfied
			input.Coverage.Complete = false
		}},
		{name: "stopped unknown reason", mutate: func(input *OrchestrationStepCompleteInput) {
			input.Outcome = OrchestrationStepOutcomeStopped
			input.StopReason = "because-model-said-so"
			input.Coverage.Complete = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, _, err := prepareOrchestrationStepComplete(input)
			assertDetectiveKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestOrchestrationPublicAPIsRequirePool(t *testing.T) {
	if _, err := StartOrchestrationRun(t.Context(), nil, OrchestrationRunStartInput{}); err == nil {
		t.Fatal("StartOrchestrationRun(nil) error = nil")
	}
	if _, err := PlanOrchestrationStep(t.Context(), nil, OrchestrationStepPlanInput{}); err == nil {
		t.Fatal("PlanOrchestrationStep(nil) error = nil")
	}
	if _, err := CompleteOrchestrationStep(t.Context(), nil, OrchestrationStepCompleteInput{}); err == nil {
		t.Fatal("CompleteOrchestrationStep(nil) error = nil")
	}
	if _, err := ResolveOrchestrationRun(t.Context(), nil, ""); err == nil {
		t.Fatal("ResolveOrchestrationRun(nil) error = nil")
	}
}

func TestOrchestrationCoverageUnitKindFollowsCapability(t *testing.T) {
	if got := orchestrationCoverageUnitKind(SourceCapabilityGitGoRepository); got != OrchestrationCoverageUnitRepositoryFiles {
		t.Fatalf("Git coverage unit = %q", got)
	}
	if got := orchestrationCoverageUnitKind(SourceCapabilityLocalPRDText); got != OrchestrationCoverageUnitTextFiles {
		t.Fatalf("text coverage unit = %q", got)
	}
}

func completeInput(requestID, runID, stepID, outcome, stopReason string, complete bool) OrchestrationStepCompleteInput {
	coveredUnitCount := 2
	unsupportedUnitCount := 1
	if complete {
		coveredUnitCount = 3
		unsupportedUnitCount = 0
	}
	return OrchestrationStepCompleteInput{
		RequestID: requestID, RunID: runID, StepID: stepID,
		Outcome: outcome, StopReason: stopReason,
		Coverage: OrchestrationCoverage{
			SchemaVersion:      OrchestrationCoverageSchemaVersion,
			UnitKind:           OrchestrationCoverageUnitTextFiles,
			AttemptedUnitCount: 3, CoveredUnitCount: coveredUnitCount,
			UnsupportedUnitCount: unsupportedUnitCount, Complete: complete,
		},
	}
}
