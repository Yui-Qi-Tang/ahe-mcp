package detective

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// PlannerInputSchemaVersion identifies the bounded, model-visible planning context.
	PlannerInputSchemaVersion = "detective-planner-input-v1"
	// PlannerDecisionSchemaVersion identifies the strict local-model output contract.
	PlannerDecisionSchemaVersion = "detective-planner-decision-v1"

	PlannerClassificationQuerySource = "query_source"
	PlannerClassificationAbstain     = "abstain"

	maxPlannerOutputBytes = 16 << 10
)

// PlannerRunner is one bounded local-model invocation over controller-built input.
type PlannerRunner func(context.Context, PlannerInput) ([]byte, error)

// PlannerInput contains only opaque source choices and structured audit coverage.
// It intentionally excludes canonical IDs, local paths, source versions, and evidence payloads.
type PlannerInput struct {
	SchemaVersion            string                       `json:"schema_version"`
	CapabilityCatalogVersion string                       `json:"capability_catalog_version"`
	RemainingStepBudget      int                          `json:"remaining_step_budget"`
	Sources                  []PlannerSourceOption        `json:"sources"`
	ObservedCoverage         []PlannerCoverageObservation `json:"observed_coverage"`
}

// PlannerSourceOption is a controller-owned opaque choice for one registered source.
type PlannerSourceOption struct {
	ChoiceKey        string `json:"choice_key"`
	CapabilityName   string `json:"capability_name"`
	CoverageUnitKind string `json:"coverage_unit_kind"`
}

// PlannerCoverageObservation is one model-visible, structured completed-step summary.
type PlannerCoverageObservation struct {
	StepNumber   int                   `json:"step_number"`
	SourceChoice string                `json:"source_choice"`
	ReasonCode   string                `json:"reason_code"`
	Outcome      string                `json:"outcome"`
	Coverage     OrchestrationCoverage `json:"coverage"`
}

// PlannerRecommendation is a validated advisory result. It does not mutate the audit or invoke a source.
type PlannerRecommendation struct {
	RunID               string `json:"run_id"`
	PlanningContextHash string `json:"planning_context_hash"`
	Classification      string `json:"classification"`
	SourceBindingID     string `json:"source_binding_id,omitempty"`
	CapabilityName      string `json:"capability_name,omitempty"`
	CapabilityVersion   string `json:"capability_version,omitempty"`
	ReasonCode          string `json:"reason_code,omitempty"`
}

// PlannerDecision is one strict, model-produced advisory source selection.
type PlannerDecision struct {
	SchemaVersion  string `json:"schema_version"`
	Classification string `json:"classification"`
	SourceChoice   string `json:"source_choice"`
}

type plannerContext struct {
	input   PlannerInput
	sources map[string]WorkspaceSourceBinding
	hash    string
}

// RecommendNextSource asks a caller-supplied local planner for one advisory source choice.
// The function rechecks its read-only audit context after the model call and fails closed if it changed.
func RecommendNextSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	runID string,
	runner PlannerRunner,
) (PlannerRecommendation, error) {
	if pool == nil {
		return PlannerRecommendation{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if runner == nil {
		return PlannerRecommendation{}, newDomainError(ErrorInvalidInput, "planner runner is required")
	}
	runID, err := normalizeDetectiveHashID(runID, "detective-run:", "run_id")
	if err != nil {
		return PlannerRecommendation{}, err
	}

	initial, err := loadPlannerContext(ctx, pool, runID)
	if err != nil {
		return PlannerRecommendation{}, err
	}
	data, err := runner(ctx, initial.input)
	if err != nil {
		return PlannerRecommendation{}, newDomainError(ErrorPlannerInvocationFailed, "local planner invocation failed: %v", err)
	}
	decision, err := ValidatePlannerOutput(initial.input, data)
	if err != nil {
		return PlannerRecommendation{}, err
	}
	recommendation, err := validatePlannerDecision(runID, initial, decision)
	if err != nil {
		return PlannerRecommendation{}, err
	}

	current, err := loadPlannerContext(ctx, pool, runID)
	if err != nil {
		return PlannerRecommendation{}, err
	}
	if current.hash != initial.hash {
		return PlannerRecommendation{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"orchestration run %s changed while the local planner was running",
			runID,
		)
	}
	return recommendation, nil
}

func loadPlannerContext(ctx context.Context, pool *pgxpool.Pool, runID string) (plannerContext, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plannerContext{}, fmt.Errorf("beginning planner context read: %w", err)
	}
	defer tx.Rollback(context.Background())

	run, found, err := readOrchestrationRun(ctx, tx, runID, false)
	if err != nil {
		return plannerContext{}, err
	}
	if !found {
		return plannerContext{}, newDomainError(ErrorOrchestrationRunNotFound, "orchestration run %s was not found", runID)
	}
	workspace, found, err := readWorkspace(ctx, tx, run.WorkspaceID, false)
	if err != nil {
		return plannerContext{}, err
	}
	if !found {
		return plannerContext{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", run.WorkspaceID)
	}
	result, err := buildPlannerContext(run, workspace)
	if err != nil {
		return plannerContext{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return plannerContext{}, fmt.Errorf("committing planner context read: %w", err)
	}
	return result, nil
}

func buildPlannerContext(run OrchestrationRun, workspace Workspace) (plannerContext, error) {
	if run.WorkspaceID != workspace.ID {
		return plannerContext{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s does not belong to workspace %s", run.ID, workspace.ID)
	}
	if run.Status != OrchestrationRunStatusRunning {
		return plannerContext{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has terminal status %s", run.ID, run.Status)
	}
	if run.StepCount >= run.MaxSteps {
		return plannerContext{}, newDomainError(ErrorPlannerInsufficientCoverage, "orchestration run %s has no remaining step budget", run.ID)
	}
	if len(workspace.Sources) == 0 {
		return plannerContext{}, newDomainError(ErrorPlannerInsufficientCoverage, "workspace %s has no registered source choices", workspace.ID)
	}

	sources := append([]WorkspaceSourceBinding(nil), workspace.Sources...)
	sortWorkspaceSources(sources)
	input := PlannerInput{
		SchemaVersion:            PlannerInputSchemaVersion,
		CapabilityCatalogVersion: SourceCapabilityCatalogV1,
		RemainingStepBudget:      run.MaxSteps - run.StepCount,
		Sources:                  make([]PlannerSourceOption, 0, len(sources)),
		ObservedCoverage:         make([]PlannerCoverageObservation, 0, len(run.Steps)),
	}
	choicesByBinding := make(map[string]string, len(sources))
	sourcesByChoice := make(map[string]WorkspaceSourceBinding, len(sources))
	for index, source := range sources {
		capability, ok := lookupSourceCapability(source.CapabilityName, source.CapabilityVersion)
		if !ok || capability.SourceSystem != source.SourceSystem {
			return plannerContext{}, newDomainError(
				ErrorUnsupportedCapability,
				"registered source %s uses unsupported capability %s/%s",
				source.ID,
				source.CapabilityName,
				source.CapabilityVersion,
			)
		}
		choice := fmt.Sprintf("source-%02d", index+1)
		choicesByBinding[source.ID] = choice
		sourcesByChoice[choice] = source
		input.Sources = append(input.Sources, PlannerSourceOption{
			ChoiceKey:        choice,
			CapabilityName:   capability.Name,
			CoverageUnitKind: orchestrationCoverageUnitKind(capability.Name),
		})
	}

	for _, step := range run.Steps {
		if step.Status == OrchestrationStepStatusPlanned {
			return plannerContext{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s already has planned step %s", run.ID, step.ID)
		}
		if step.Status != OrchestrationStepStatusCompleted || step.Outcome != OrchestrationStepOutcomeContinue || step.Coverage == nil {
			return plannerContext{}, newDomainError(ErrorPlannerInsufficientCoverage, "orchestration step %s cannot ground another source recommendation", step.ID)
		}
		choice, ok := choicesByBinding[step.SourceBindingID]
		if !ok {
			return plannerContext{}, newDomainError(ErrorPlannerUngroundedOutput, "orchestration step %s references an unregistered source", step.ID)
		}
		input.ObservedCoverage = append(input.ObservedCoverage, PlannerCoverageObservation{
			StepNumber:   step.Number,
			SourceChoice: choice,
			ReasonCode:   step.ReasonCode,
			Outcome:      step.Outcome,
			Coverage:     *step.Coverage,
		})
	}

	if err := validatePlannerInput(input); err != nil {
		return plannerContext{}, err
	}
	hash, err := orchestrationPayloadHash(input)
	if err != nil {
		return plannerContext{}, err
	}
	return plannerContext{input: input, sources: sourcesByChoice, hash: hash}, nil
}

func validatePlannerInput(input PlannerInput) error {
	if input.SchemaVersion != PlannerInputSchemaVersion {
		return newDomainError(ErrorInvalidInput, "planner input schema_version must be %s", PlannerInputSchemaVersion)
	}
	if input.CapabilityCatalogVersion != SourceCapabilityCatalogV1 {
		return newDomainError(ErrorInvalidInput, "planner capability catalog must be %s", SourceCapabilityCatalogV1)
	}
	if input.RemainingStepBudget < 1 || input.RemainingStepBudget > maxOrchestrationSteps {
		return newDomainError(ErrorInvalidInput, "planner remaining_step_budget must be between 1 and %d", maxOrchestrationSteps)
	}
	if len(input.Sources) < 1 || len(input.Sources) > maxWorkspaceSourceBindings {
		return newDomainError(ErrorInvalidInput, "planner sources must contain between 1 and %d choices", maxWorkspaceSourceBindings)
	}

	unitByChoice := make(map[string]string, len(input.Sources))
	for index, source := range input.Sources {
		wantChoice := fmt.Sprintf("source-%02d", index+1)
		if source.ChoiceKey != wantChoice {
			return newDomainError(ErrorInvalidInput, "planner source choice %d must be %s", index+1, wantChoice)
		}
		var wantUnit string
		switch source.CapabilityName {
		case SourceCapabilityGitGoRepository:
			wantUnit = OrchestrationCoverageUnitRepositoryFiles
		case SourceCapabilityLocalPRDText:
			wantUnit = OrchestrationCoverageUnitTextFiles
		default:
			return newDomainError(ErrorUnsupportedCapability, "planner source choice %s uses unsupported capability %s", source.ChoiceKey, source.CapabilityName)
		}
		if source.CoverageUnitKind != wantUnit {
			return newDomainError(ErrorInvalidInput, "planner source choice %s has incompatible coverage unit %s", source.ChoiceKey, source.CoverageUnitKind)
		}
		unitByChoice[source.ChoiceKey] = wantUnit
	}

	if len(input.ObservedCoverage)+input.RemainingStepBudget > maxOrchestrationSteps {
		return newDomainError(ErrorInvalidInput, "planner observed coverage and remaining budget exceed %d steps", maxOrchestrationSteps)
	}
	for index, observation := range input.ObservedCoverage {
		if observation.StepNumber != index+1 {
			return newDomainError(ErrorInvalidInput, "planner coverage observations must have contiguous step numbers")
		}
		unit, ok := unitByChoice[observation.SourceChoice]
		if !ok {
			return newDomainError(ErrorInvalidInput, "planner coverage observation %d uses unknown source choice %q", observation.StepNumber, observation.SourceChoice)
		}
		if !validOrchestrationReason(observation.ReasonCode) {
			return newDomainError(ErrorInvalidInput, "planner coverage observation %d has unsupported reason %q", observation.StepNumber, observation.ReasonCode)
		}
		if observation.Outcome != OrchestrationStepOutcomeContinue {
			return newDomainError(ErrorInvalidInput, "planner coverage observation %d must have continue outcome", observation.StepNumber)
		}
		if err := validateOrchestrationCoverage(observation.Coverage); err != nil {
			return err
		}
		if observation.Coverage.UnitKind != unit || observation.Coverage.Complete {
			return newDomainError(ErrorInvalidInput, "planner coverage observation %d must be incomplete and capability-compatible", observation.StepNumber)
		}
	}
	return nil
}

// ValidatePlannerInput verifies one controller-built model-visible context.
func ValidatePlannerInput(input PlannerInput) error {
	return validatePlannerInput(input)
}

// ValidatePlannerOutput applies the exact strict JSON and grounded-choice
// contract used before a planner recommendation can reach controller mapping.
func ValidatePlannerOutput(input PlannerInput, data []byte) (PlannerDecision, error) {
	if err := validatePlannerInput(input); err != nil {
		return PlannerDecision{}, err
	}
	decision, err := decodePlannerDecision(data)
	if err != nil {
		return PlannerDecision{}, err
	}
	if err := validatePlannerDecisionAgainstInput(input, decision); err != nil {
		return PlannerDecision{}, err
	}
	return decision, nil
}

func decodePlannerDecision(data []byte) (PlannerDecision, error) {
	if len(data) > maxPlannerOutputBytes {
		return PlannerDecision{}, newDomainError(ErrorPlannerMalformedOutput, "planner output exceeds %d bytes", maxPlannerOutputBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decision PlannerDecision
	if err := decoder.Decode(&decision); err != nil {
		return PlannerDecision{}, newDomainError(ErrorPlannerMalformedOutput, "decoding planner output: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PlannerDecision{}, newDomainError(ErrorPlannerMalformedOutput, "planner output must contain exactly one JSON document")
	}
	if decision.SchemaVersion != PlannerDecisionSchemaVersion {
		return PlannerDecision{}, newDomainError(ErrorPlannerMalformedOutput, "unsupported planner schema_version %q", decision.SchemaVersion)
	}
	return decision, nil
}

func validatePlannerDecisionAgainstInput(input PlannerInput, decision PlannerDecision) error {
	switch decision.Classification {
	case PlannerClassificationAbstain:
		if decision.SourceChoice != "" {
			return newDomainError(ErrorPlannerContradictoryOutput, "abstain planner output must not select a source")
		}
		return nil
	case PlannerClassificationQuerySource:
		if decision.SourceChoice == "" {
			return newDomainError(ErrorPlannerContradictoryOutput, "query_source planner output must select a source")
		}
	default:
		return newDomainError(ErrorPlannerMalformedOutput, "unsupported planner classification %q", decision.Classification)
	}

	offered := false
	for _, source := range input.Sources {
		if source.ChoiceKey == decision.SourceChoice {
			offered = true
			break
		}
	}
	if !offered {
		return newDomainError(ErrorPlannerUngroundedOutput, "planner selected unknown source choice %q", decision.SourceChoice)
	}
	for _, observation := range input.ObservedCoverage {
		if observation.SourceChoice == decision.SourceChoice {
			return newDomainError(
				ErrorPlannerUngroundedOutput,
				"planner selected previously queried source choice %q; retry and refresh policy is controller-owned",
				decision.SourceChoice,
			)
		}
	}
	return nil
}

func validatePlannerDecision(runID string, context plannerContext, decision PlannerDecision) (PlannerRecommendation, error) {
	if err := validatePlannerDecisionAgainstInput(context.input, decision); err != nil {
		return PlannerRecommendation{}, err
	}
	recommendation := PlannerRecommendation{
		RunID: runID, PlanningContextHash: context.hash, Classification: decision.Classification,
	}
	if decision.Classification == PlannerClassificationAbstain {
		return recommendation, nil
	}

	source, ok := context.sources[decision.SourceChoice]
	if !ok {
		return PlannerRecommendation{}, newDomainError(ErrorPlannerUngroundedOutput, "planner selected unknown source choice %q", decision.SourceChoice)
	}

	reasonCode := OrchestrationReasonInitialSource
	if len(context.input.ObservedCoverage) > 0 {
		last := context.input.ObservedCoverage[len(context.input.ObservedCoverage)-1]
		if last.Coverage.Complete {
			return PlannerRecommendation{}, newDomainError(ErrorPlannerInsufficientCoverage, "complete observed coverage cannot ground another source query")
		}
		reasonCode = OrchestrationReasonCoverageGap
	}
	recommendation.SourceBindingID = source.ID
	recommendation.CapabilityName = source.CapabilityName
	recommendation.CapabilityVersion = source.CapabilityVersion
	recommendation.ReasonCode = reasonCode
	return recommendation, nil
}
