package detective

import (
	"strings"
	"testing"
)

func TestPreparePeriodicSourceTickNormalizesAndBoundsInput(t *testing.T) {
	sourceID := "workspace-source:" + strings.Repeat("a", 64)
	prepared, err := preparePeriodicSourceTick(PeriodicSourceTickInput{
		WorkspaceID:     " workspace:periodic ",
		SourceBindingID: " " + sourceID + " ",
		MaxSteps:        3,
	})
	if err != nil {
		t.Fatalf("preparePeriodicSourceTick() error = %v", err)
	}
	if prepared.workspaceID != "workspace:periodic" || prepared.sourceBindingID != sourceID || prepared.maxSteps != 3 {
		t.Fatalf("prepared periodic tick = %+v", prepared)
	}
	for _, maxSteps := range []int{0, maxOrchestrationSteps + 1} {
		_, err := preparePeriodicSourceTick(PeriodicSourceTickInput{
			WorkspaceID: "workspace:periodic", SourceBindingID: sourceID, MaxSteps: maxSteps,
		})
		assertDetectiveKind(t, err, ErrorInvalidInput)
	}
}

func TestPeriodicSourceCycleIdentityBindsSequenceAndToken(t *testing.T) {
	workspaceID := "workspace:periodic"
	sourceID := "workspace-source:" + strings.Repeat("b", 64)
	first := periodicSourceCycleID(workspaceID, sourceID, 1, "token-a")
	replay := periodicSourceCycleID(workspaceID, sourceID, 1, "token-a")
	nextSequence := periodicSourceCycleID(workspaceID, sourceID, 2, "token-a")
	nextToken := periodicSourceCycleID(workspaceID, sourceID, 1, "token-b")
	if first != replay || first == nextSequence || first == nextToken || !strings.HasPrefix(first, "detective-periodic-cycle:") {
		t.Fatalf("periodic cycle IDs = %q/%q/%q/%q", first, replay, nextSequence, nextToken)
	}
	requestID := periodicOneShotRequestID(first)
	if requestID != periodicOneShotRequestID(replay) || !strings.HasPrefix(requestID, "detective-periodic-one-shot:") {
		t.Fatalf("periodic one-shot request ID = %q", requestID)
	}

	contextHash := "sha256:" + strings.Repeat("c", 64)
	plannerRequestID := periodicPlannerConsumptionRequestID(first, contextHash)
	if plannerRequestID != periodicPlannerConsumptionRequestID(replay, contextHash) ||
		plannerRequestID == periodicPlannerConsumptionRequestID(nextToken, contextHash) ||
		plannerRequestID == periodicPlannerConsumptionRequestID(first, "sha256:"+strings.Repeat("d", 64)) ||
		!strings.HasPrefix(plannerRequestID, "detective-periodic-planner:") ||
		len(plannerRequestID) > 300 {
		t.Fatalf("periodic planner request ID = %q", plannerRequestID)
	}
}

func TestRunPeriodicSourceTickRequiresPool(t *testing.T) {
	_, err := RunPeriodicSourceTick(t.Context(), nil, PeriodicSourceTickInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestRunPlannerPeriodicSourceTickRequiresRunner(t *testing.T) {
	_, err := RunPlannerPeriodicSourceTick(t.Context(), nil, PeriodicSourceTickInput{}, nil)
	assertDetectiveKind(t, err, ErrorInvalidInput)
}
