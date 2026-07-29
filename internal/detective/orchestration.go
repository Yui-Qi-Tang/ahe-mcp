package detective

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxOrchestrationSteps = 32

	OrchestrationRunStatusRunning   = "running"
	OrchestrationRunStatusCompleted = "completed"
	OrchestrationRunStatusFailed    = "failed"
	OrchestrationRunStatusStopped   = "stopped"

	OrchestrationStepStatusPlanned   = "planned"
	OrchestrationStepStatusCompleted = "completed"
	OrchestrationStepStatusFailed    = "failed"
	OrchestrationStepStatusStopped   = "stopped"

	OrchestrationStepOutcomeContinue  = "continue"
	OrchestrationStepOutcomeCompleted = "completed"
	OrchestrationStepOutcomeFailed    = "failed"
	OrchestrationStepOutcomeStopped   = "stopped"

	OrchestrationReasonInitialSource = "initial_source"
	OrchestrationReasonCoverageGap   = "coverage_gap"
	OrchestrationReasonConflictCheck = "conflict_check"
	OrchestrationReasonRefresh       = "refresh"

	OrchestrationInputAuthorityNone             = "none"
	OrchestrationInputAuthoritySourceToken      = "source_token"
	OrchestrationInputAuthoritySourceGeneration = "source_generation"

	OrchestrationStopReasonCoverageSatisfied     = "coverage_satisfied"
	OrchestrationStopReasonSourceFailed          = "source_failed"
	OrchestrationStopReasonWorkspaceUnavailable  = "workspace_unavailable"
	OrchestrationStopReasonUnsupportedCapability = "unsupported_capability"
	OrchestrationStopReasonContradictoryOutput   = "contradictory_output"
	OrchestrationStopReasonUngroundedOutput      = "ungrounded_output"
	OrchestrationStopReasonBudgetExhausted       = "budget_exhausted"
	OrchestrationStopReasonNoAllowlistedSource   = "no_allowlisted_source"

	OrchestrationCoverageSchemaVersion       = "detective-coverage-v1"
	OrchestrationCoverageUnitRepositoryFiles = "repository_files"
	OrchestrationCoverageUnitTextFiles       = "text_files"

	orchestrationTransitionStartRun     = "start_run"
	orchestrationTransitionPlanStep     = "plan_step"
	orchestrationTransitionCompleteStep = "complete_step"
)

// OrchestrationRunStartInput starts one bounded, internal detective audit run.
type OrchestrationRunStartInput struct {
	RequestID   string `json:"request_id"`
	WorkspaceID string `json:"workspace_id"`
	MaxSteps    int    `json:"max_steps"`
	ScopeHash   string `json:"scope_hash,omitempty"`
}

// OrchestrationStepPlanInput persists source authority before a caller invokes it.
type OrchestrationStepPlanInput struct {
	RequestID          string `json:"request_id"`
	RunID              string `json:"run_id"`
	SourceBindingID    string `json:"source_binding_id"`
	ReasonCode         string `json:"reason_code"`
	InputAuthorityKind string `json:"input_authority_kind"`
	InputAuthorityRef  string `json:"input_authority_ref,omitempty"`
}

// OrchestrationCoverage is a bounded source-coverage summary, never evidence text.
type OrchestrationCoverage struct {
	SchemaVersion        string `json:"schema_version"`
	UnitKind             string `json:"unit_kind"`
	AttemptedUnitCount   int    `json:"attempted_unit_count"`
	CoveredUnitCount     int    `json:"covered_unit_count"`
	UnsupportedUnitCount int    `json:"unsupported_unit_count"`
	Complete             bool   `json:"complete"`
}

// OrchestrationStepCompleteInput closes the currently planned step and may close its run.
type OrchestrationStepCompleteInput struct {
	RequestID  string                `json:"request_id"`
	RunID      string                `json:"run_id"`
	StepID     string                `json:"step_id"`
	Outcome    string                `json:"outcome"`
	StopReason string                `json:"stop_reason,omitempty"`
	Coverage   OrchestrationCoverage `json:"coverage"`
}

// OrchestrationRun is one durable bounded run and its ordered immutable audit steps.
type OrchestrationRun struct {
	ID          string              `json:"run_id"`
	WorkspaceID string              `json:"workspace_id"`
	Status      string              `json:"status"`
	MaxSteps    int                 `json:"max_steps"`
	StepCount   int                 `json:"step_count"`
	StopReason  string              `json:"stop_reason,omitempty"`
	StartedAt   time.Time           `json:"started_at"`
	FinishedAt  *time.Time          `json:"finished_at,omitempty"`
	Steps       []OrchestrationStep `json:"steps"`
}

// OrchestrationStep is one exact source plan and its terminal coverage outcome.
type OrchestrationStep struct {
	ID                 string                 `json:"step_id"`
	RunID              string                 `json:"run_id"`
	WorkspaceID        string                 `json:"workspace_id"`
	Number             int                    `json:"step_number"`
	SourceBindingID    string                 `json:"source_binding_id"`
	CapabilityName     string                 `json:"capability_name"`
	CapabilityVersion  string                 `json:"capability_version"`
	SourceSystem       string                 `json:"source_system"`
	SourceID           string                 `json:"source_id"`
	ReasonCode         string                 `json:"reason_code"`
	InputAuthorityKind string                 `json:"input_authority_kind"`
	InputAuthorityRef  string                 `json:"input_authority_ref,omitempty"`
	Status             string                 `json:"status"`
	Outcome            string                 `json:"outcome,omitempty"`
	StopReason         string                 `json:"stop_reason,omitempty"`
	Coverage           *OrchestrationCoverage `json:"coverage,omitempty"`
	PlannedAt          time.Time              `json:"planned_at"`
	FinishedAt         *time.Time             `json:"finished_at,omitempty"`
}

// OrchestrationRunStartResult reports exact start replay state.
type OrchestrationRunStartResult struct {
	RequestID string           `json:"request_id"`
	Run       OrchestrationRun `json:"run"`
	Replayed  bool             `json:"replayed"`
}

// OrchestrationStepPlanResult reports exact plan replay state.
type OrchestrationStepPlanResult struct {
	RequestID string            `json:"request_id"`
	Run       OrchestrationRun  `json:"run"`
	Step      OrchestrationStep `json:"step"`
	Replayed  bool              `json:"replayed"`
}

// OrchestrationStepCompleteResult reports exact completion replay state.
type OrchestrationStepCompleteResult struct {
	RequestID string            `json:"request_id"`
	Run       OrchestrationRun  `json:"run"`
	Step      OrchestrationStep `json:"step"`
	Replayed  bool              `json:"replayed"`
}

type orchestrationTransitionRequest struct {
	RequestID   string
	Kind        string
	RunID       string
	StepID      string
	PayloadHash string
}

// StartOrchestrationRun creates or exactly replays one bounded run.
func StartOrchestrationRun(ctx context.Context, pool *pgxpool.Pool, input OrchestrationRunStartInput) (OrchestrationRunStartResult, error) {
	if pool == nil {
		return OrchestrationRunStartResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, runID, payloadHash, err := prepareOrchestrationRunStart(input)
	if err != nil {
		return OrchestrationRunStartResult{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return OrchestrationRunStartResult{}, fmt.Errorf("beginning orchestration run start: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockOrchestrationTransitionRequest(ctx, tx, prepared.RequestID); err != nil {
		return OrchestrationRunStartResult{}, err
	}
	persisted, ok, err := readOrchestrationTransitionRequest(ctx, tx, prepared.RequestID)
	if err != nil {
		return OrchestrationRunStartResult{}, err
	}
	if ok {
		if err := ensureOrchestrationTransitionReplay(persisted, orchestrationTransitionStartRun, runID, "", payloadHash); err != nil {
			return OrchestrationRunStartResult{}, err
		}
		run, found, err := readOrchestrationRun(ctx, tx, runID, true)
		if err != nil {
			return OrchestrationRunStartResult{}, err
		}
		if !found {
			return OrchestrationRunStartResult{}, newDomainError(ErrorOrchestrationRunConflict, "start request %s references missing run %s", prepared.RequestID, runID)
		}
		if err := tx.Commit(ctx); err != nil {
			return OrchestrationRunStartResult{}, fmt.Errorf("committing orchestration run start replay: %w", err)
		}
		return OrchestrationRunStartResult{RequestID: prepared.RequestID, Run: run, Replayed: true}, nil
	}
	workspace, workspaceFound, err := readWorkspace(ctx, tx, prepared.WorkspaceID, false)
	if err != nil {
		return OrchestrationRunStartResult{}, err
	}
	if !workspaceFound {
		return OrchestrationRunStartResult{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", prepared.WorkspaceID)
	}
	if err := validateResolvedWorkspace(ctx, workspace); err != nil {
		return OrchestrationRunStartResult{}, err
	}
	result, err := startOrchestrationRunLocked(ctx, tx, prepared, runID, payloadHash, workspace)
	if err != nil {
		return OrchestrationRunStartResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OrchestrationRunStartResult{}, fmt.Errorf("committing orchestration run start: %w", err)
	}
	return result, nil
}

// startOrchestrationRunLocked expects the transition request identity to be locked by its caller.
func startOrchestrationRunLocked(
	ctx context.Context,
	tx pgx.Tx,
	prepared OrchestrationRunStartInput,
	runID string,
	payloadHash string,
	workspace Workspace,
) (OrchestrationRunStartResult, error) {
	if workspace.ID != prepared.WorkspaceID {
		return OrchestrationRunStartResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"orchestration start workspace %s does not match resolved workspace %s",
			prepared.WorkspaceID,
			workspace.ID,
		)
	}
	var startedAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO detective_orchestration_runs (
			run_id, workspace_id, status, max_steps
		)
		VALUES ($1,$2,$3,$4)
		RETURNING started_at
	`, runID, prepared.WorkspaceID, OrchestrationRunStatusRunning, prepared.MaxSteps).Scan(&startedAt); err != nil {
		return OrchestrationRunStartResult{}, fmt.Errorf("inserting orchestration run: %w", err)
	}
	if err := insertOrchestrationTransitionRequest(ctx, tx, orchestrationTransitionRequest{
		RequestID: prepared.RequestID, Kind: orchestrationTransitionStartRun, RunID: runID, PayloadHash: payloadHash,
	}); err != nil {
		return OrchestrationRunStartResult{}, err
	}
	run := OrchestrationRun{
		ID: runID, WorkspaceID: prepared.WorkspaceID, Status: OrchestrationRunStatusRunning,
		MaxSteps: prepared.MaxSteps, StartedAt: startedAt.UTC(), Steps: []OrchestrationStep{},
	}
	return OrchestrationRunStartResult{RequestID: prepared.RequestID, Run: run}, nil
}

// PlanOrchestrationStep records exact source authority before any source invocation.
func PlanOrchestrationStep(ctx context.Context, pool *pgxpool.Pool, input OrchestrationStepPlanInput) (OrchestrationStepPlanResult, error) {
	if pool == nil {
		return OrchestrationStepPlanResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, payloadHash, err := prepareOrchestrationStepPlan(input)
	if err != nil {
		return OrchestrationStepPlanResult{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return OrchestrationStepPlanResult{}, fmt.Errorf("beginning orchestration step plan: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockOrchestrationTransitionRequest(ctx, tx, prepared.RequestID); err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	persisted, ok, err := readOrchestrationTransitionRequest(ctx, tx, prepared.RequestID)
	if err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	if ok {
		if persisted.Kind != orchestrationTransitionPlanStep || persisted.RunID != prepared.RunID || persisted.PayloadHash != payloadHash || persisted.StepID == "" {
			return OrchestrationStepPlanResult{}, newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different orchestration transition payload", prepared.RequestID)
		}
		run, found, err := readOrchestrationRun(ctx, tx, prepared.RunID, true)
		if err != nil {
			return OrchestrationStepPlanResult{}, err
		}
		if !found {
			return OrchestrationStepPlanResult{}, newDomainError(ErrorOrchestrationRunConflict, "step plan request %s references missing run %s", prepared.RequestID, prepared.RunID)
		}
		step, found := orchestrationStepByID(run.Steps, persisted.StepID)
		if !found {
			return OrchestrationStepPlanResult{}, newDomainError(ErrorOrchestrationRunConflict, "step plan request %s references missing step %s", prepared.RequestID, persisted.StepID)
		}
		if err := tx.Commit(ctx); err != nil {
			return OrchestrationStepPlanResult{}, fmt.Errorf("committing orchestration step plan replay: %w", err)
		}
		return OrchestrationStepPlanResult{RequestID: prepared.RequestID, Run: run, Step: step, Replayed: true}, nil
	}

	run, found, err := readOrchestrationRun(ctx, tx, prepared.RunID, true)
	if err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	if !found {
		return OrchestrationStepPlanResult{}, newDomainError(ErrorOrchestrationRunNotFound, "orchestration run %s was not found", prepared.RunID)
	}
	if err := validateOrchestrationRunForPlan(run); err != nil {
		return OrchestrationStepPlanResult{}, err
	}

	workspace, workspaceFound, err := readWorkspace(ctx, tx, run.WorkspaceID, false)
	if err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	if !workspaceFound {
		return OrchestrationStepPlanResult{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", run.WorkspaceID)
	}
	if err := validateResolvedWorkspace(ctx, workspace); err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	result, err := planOrchestrationStepLocked(ctx, tx, prepared, payloadHash, run, workspace)
	if err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OrchestrationStepPlanResult{}, fmt.Errorf("committing orchestration step plan: %w", err)
	}
	return result, nil
}

func validateOrchestrationRunForPlan(run OrchestrationRun) error {
	if run.Status != OrchestrationRunStatusRunning {
		return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has terminal status %s", run.ID, run.Status)
	}
	if run.StepCount != len(run.Steps) {
		return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s step count is inconsistent", run.ID)
	}
	for _, step := range run.Steps {
		if step.Status == OrchestrationStepStatusPlanned {
			return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s already has planned step %s", run.ID, step.ID)
		}
	}
	if run.StepCount >= run.MaxSteps {
		return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s exhausted its %d-step budget", run.ID, run.MaxSteps)
	}
	return nil
}

// planOrchestrationStepLocked expects the run row and request identity to be locked by its caller.
func planOrchestrationStepLocked(
	ctx context.Context,
	tx pgx.Tx,
	prepared OrchestrationStepPlanInput,
	payloadHash string,
	run OrchestrationRun,
	workspace Workspace,
) (OrchestrationStepPlanResult, error) {
	if err := validateOrchestrationRunForPlan(run); err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	if run.WorkspaceID != workspace.ID {
		return OrchestrationStepPlanResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"orchestration run %s does not belong to workspace %s",
			run.ID,
			workspace.ID,
		)
	}
	source, found := workspaceSourceByID(workspace.Sources, prepared.SourceBindingID)
	if !found {
		return OrchestrationStepPlanResult{}, newDomainError(ErrorSourceBindingNotFound, "source binding %s is not registered in workspace %s", prepared.SourceBindingID, workspace.ID)
	}

	stepNumber := run.StepCount + 1
	stepID := orchestrationStepID(run.ID, stepNumber)
	var plannedAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO detective_orchestration_steps (
			step_id, run_id, workspace_id, step_number, source_binding_id,
			capability_name, capability_version, source_system, source_id,
			reason_code, input_authority_kind, input_authority_ref, status
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING planned_at
	`, stepID, run.ID, run.WorkspaceID, stepNumber, source.ID,
		source.CapabilityName, source.CapabilityVersion, source.SourceSystem, source.SourceID,
		prepared.ReasonCode, prepared.InputAuthorityKind, nullableDetectiveText(prepared.InputAuthorityRef), OrchestrationStepStatusPlanned,
	).Scan(&plannedAt); err != nil {
		return OrchestrationStepPlanResult{}, fmt.Errorf("inserting orchestration step: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE detective_orchestration_runs
		SET step_count = $2
		WHERE run_id = $1 AND status = 'running'
	`, run.ID, stepNumber)
	if err != nil {
		return OrchestrationStepPlanResult{}, fmt.Errorf("advancing orchestration run step count: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return OrchestrationStepPlanResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s changed during step planning", run.ID)
	}
	if err := insertOrchestrationTransitionRequest(ctx, tx, orchestrationTransitionRequest{
		RequestID: prepared.RequestID, Kind: orchestrationTransitionPlanStep, RunID: run.ID, StepID: stepID, PayloadHash: payloadHash,
	}); err != nil {
		return OrchestrationStepPlanResult{}, err
	}
	step := OrchestrationStep{
		ID: stepID, RunID: run.ID, WorkspaceID: run.WorkspaceID, Number: stepNumber,
		SourceBindingID: source.ID, CapabilityName: source.CapabilityName,
		CapabilityVersion: source.CapabilityVersion, SourceSystem: source.SourceSystem,
		SourceID: source.SourceID, ReasonCode: prepared.ReasonCode,
		InputAuthorityKind: prepared.InputAuthorityKind, InputAuthorityRef: prepared.InputAuthorityRef,
		Status: OrchestrationStepStatusPlanned, PlannedAt: plannedAt.UTC(),
	}
	run.StepCount = stepNumber
	run.Steps = append(run.Steps, step)
	return OrchestrationStepPlanResult{RequestID: prepared.RequestID, Run: run, Step: step}, nil
}

// CompleteOrchestrationStep records bounded coverage and closes the current planned step.
func CompleteOrchestrationStep(ctx context.Context, pool *pgxpool.Pool, input OrchestrationStepCompleteInput) (OrchestrationStepCompleteResult, error) {
	if pool == nil {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, payloadHash, err := prepareOrchestrationStepComplete(input)
	if err != nil {
		return OrchestrationStepCompleteResult{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return OrchestrationStepCompleteResult{}, fmt.Errorf("beginning orchestration step completion: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockOrchestrationTransitionRequest(ctx, tx, prepared.RequestID); err != nil {
		return OrchestrationStepCompleteResult{}, err
	}
	persisted, ok, err := readOrchestrationTransitionRequest(ctx, tx, prepared.RequestID)
	if err != nil {
		return OrchestrationStepCompleteResult{}, err
	}
	if ok {
		if err := ensureOrchestrationTransitionReplay(persisted, orchestrationTransitionCompleteStep, prepared.RunID, prepared.StepID, payloadHash); err != nil {
			return OrchestrationStepCompleteResult{}, err
		}
		run, found, err := readOrchestrationRun(ctx, tx, prepared.RunID, true)
		if err != nil {
			return OrchestrationStepCompleteResult{}, err
		}
		if !found {
			return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "step completion request %s references missing run %s", prepared.RequestID, prepared.RunID)
		}
		step, found := orchestrationStepByID(run.Steps, prepared.StepID)
		if !found {
			return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "step completion request %s references missing step %s", prepared.RequestID, prepared.StepID)
		}
		if err := tx.Commit(ctx); err != nil {
			return OrchestrationStepCompleteResult{}, fmt.Errorf("committing orchestration step completion replay: %w", err)
		}
		return OrchestrationStepCompleteResult{RequestID: prepared.RequestID, Run: run, Step: step, Replayed: true}, nil
	}

	run, found, err := readOrchestrationRun(ctx, tx, prepared.RunID, true)
	if err != nil {
		return OrchestrationStepCompleteResult{}, err
	}
	if !found {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunNotFound, "orchestration run %s was not found", prepared.RunID)
	}
	if run.Status != OrchestrationRunStatusRunning {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has terminal status %s", run.ID, run.Status)
	}
	step, found := orchestrationStepByID(run.Steps, prepared.StepID)
	if !found {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationStepNotFound, "orchestration step %s was not found in run %s", prepared.StepID, run.ID)
	}
	if step.Status != OrchestrationStepStatusPlanned {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration step %s has status %s, want planned", step.ID, step.Status)
	}
	if expected := orchestrationCoverageUnitKind(step.CapabilityName); prepared.Coverage.UnitKind != expected {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorInvalidInput, "orchestration step %s capability %s requires coverage unit_kind %s", step.ID, step.CapabilityName, expected)
	}
	if prepared.Outcome == OrchestrationStepOutcomeContinue && step.Number >= run.MaxSteps {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration step %s cannot continue beyond the %d-step budget", step.ID, run.MaxSteps)
	}

	stepStatus, runStatus := orchestrationTerminalStatuses(prepared.Outcome)
	var finishedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&finishedAt); err != nil {
		return OrchestrationStepCompleteResult{}, fmt.Errorf("reading orchestration completion time: %w", err)
	}
	finishedAt = finishedAt.UTC()
	tag, err := tx.Exec(ctx, `
		UPDATE detective_orchestration_steps
		SET status = $4,
			outcome = $5,
			stop_reason = $6,
			coverage_schema_version = $7,
			coverage_unit_kind = $8,
			attempted_unit_count = $9,
			covered_unit_count = $10,
			unsupported_unit_count = $11,
			coverage_complete = $12,
			finished_at = $13
		WHERE run_id = $1 AND step_id = $2 AND status = $3
	`, run.ID, step.ID, OrchestrationStepStatusPlanned, stepStatus, prepared.Outcome,
		nullableDetectiveText(prepared.StopReason), prepared.Coverage.SchemaVersion, prepared.Coverage.UnitKind,
		prepared.Coverage.AttemptedUnitCount, prepared.Coverage.CoveredUnitCount,
		prepared.Coverage.UnsupportedUnitCount, prepared.Coverage.Complete, finishedAt)
	if err != nil {
		return OrchestrationStepCompleteResult{}, fmt.Errorf("completing orchestration step: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration step %s changed during completion", step.ID)
	}
	tag, err = tx.Exec(ctx, `
		UPDATE detective_orchestration_runs
		SET status = $2,
			stop_reason = $3,
			finished_at = $4
		WHERE run_id = $1 AND status = 'running'
	`, run.ID, runStatus, nullableDetectiveText(prepared.StopReason), nullableDetectiveTime(runStatus, finishedAt))
	if err != nil {
		return OrchestrationStepCompleteResult{}, fmt.Errorf("completing orchestration run state: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s changed during step completion", run.ID)
	}
	if err := insertOrchestrationTransitionRequest(ctx, tx, orchestrationTransitionRequest{
		RequestID: prepared.RequestID, Kind: orchestrationTransitionCompleteStep,
		RunID: run.ID, StepID: step.ID, PayloadHash: payloadHash,
	}); err != nil {
		return OrchestrationStepCompleteResult{}, err
	}

	run, found, err = readOrchestrationRun(ctx, tx, run.ID, false)
	if err != nil {
		return OrchestrationStepCompleteResult{}, err
	}
	if !found {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s disappeared during completion", prepared.RunID)
	}
	step, found = orchestrationStepByID(run.Steps, prepared.StepID)
	if !found {
		return OrchestrationStepCompleteResult{}, newDomainError(ErrorOrchestrationRunConflict, "orchestration step %s disappeared during completion", prepared.StepID)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrchestrationStepCompleteResult{}, fmt.Errorf("committing orchestration step completion: %w", err)
	}
	return OrchestrationStepCompleteResult{RequestID: prepared.RequestID, Run: run, Step: step}, nil
}

// ResolveOrchestrationRun returns one durable audit without re-invoking a source.
func ResolveOrchestrationRun(ctx context.Context, pool *pgxpool.Pool, runID string) (OrchestrationRun, error) {
	if pool == nil {
		return OrchestrationRun{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	runID, err := normalizeDetectiveHashID(runID, "detective-run:", "run_id")
	if err != nil {
		return OrchestrationRun{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return OrchestrationRun{}, fmt.Errorf("beginning orchestration run resolution: %w", err)
	}
	defer tx.Rollback(context.Background())
	run, found, err := readOrchestrationRun(ctx, tx, runID, false)
	if err != nil {
		return OrchestrationRun{}, err
	}
	if !found {
		return OrchestrationRun{}, newDomainError(ErrorOrchestrationRunNotFound, "orchestration run %s was not found", runID)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrchestrationRun{}, fmt.Errorf("committing orchestration run resolution: %w", err)
	}
	return run, nil
}

func prepareOrchestrationRunStart(input OrchestrationRunStartInput) (OrchestrationRunStartInput, string, string, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return OrchestrationRunStartInput{}, "", "", err
	}
	workspaceID, err := normalizeWorkspaceID(input.WorkspaceID)
	if err != nil {
		return OrchestrationRunStartInput{}, "", "", err
	}
	if input.MaxSteps < 1 || input.MaxSteps > maxOrchestrationSteps {
		return OrchestrationRunStartInput{}, "", "", newDomainError(ErrorInvalidInput, "max_steps must be between 1 and %d", maxOrchestrationSteps)
	}
	scopeHash, err := normalizeOptionalContentHash(input.ScopeHash, "scope_hash")
	if err != nil {
		return OrchestrationRunStartInput{}, "", "", err
	}
	prepared := OrchestrationRunStartInput{
		RequestID: requestID, WorkspaceID: workspaceID, MaxSteps: input.MaxSteps, ScopeHash: scopeHash,
	}
	payloadHash, err := orchestrationPayloadHash(struct {
		WorkspaceID string `json:"workspace_id"`
		MaxSteps    int    `json:"max_steps"`
		ScopeHash   string `json:"scope_hash,omitempty"`
	}{WorkspaceID: workspaceID, MaxSteps: input.MaxSteps, ScopeHash: scopeHash})
	if err != nil {
		return OrchestrationRunStartInput{}, "", "", err
	}
	return prepared, orchestrationRunID(requestID), payloadHash, nil
}

func prepareOrchestrationStepPlan(input OrchestrationStepPlanInput) (OrchestrationStepPlanInput, string, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return OrchestrationStepPlanInput{}, "", err
	}
	runID, err := normalizeDetectiveHashID(input.RunID, "detective-run:", "run_id")
	if err != nil {
		return OrchestrationStepPlanInput{}, "", err
	}
	sourceBindingID, err := normalizeDetectiveHashID(input.SourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return OrchestrationStepPlanInput{}, "", err
	}
	reasonCode := strings.TrimSpace(input.ReasonCode)
	if !validOrchestrationReason(reasonCode) {
		return OrchestrationStepPlanInput{}, "", newDomainError(ErrorInvalidInput, "unsupported orchestration reason_code %q", reasonCode)
	}
	authorityKind := strings.TrimSpace(input.InputAuthorityKind)
	authorityRef := strings.TrimSpace(input.InputAuthorityRef)
	if err := validateOrchestrationInputAuthority(authorityKind, authorityRef); err != nil {
		return OrchestrationStepPlanInput{}, "", err
	}
	prepared := OrchestrationStepPlanInput{
		RequestID: requestID, RunID: runID, SourceBindingID: sourceBindingID,
		ReasonCode: reasonCode, InputAuthorityKind: authorityKind, InputAuthorityRef: authorityRef,
	}
	payloadHash, err := orchestrationPayloadHash(struct {
		RunID              string `json:"run_id"`
		SourceBindingID    string `json:"source_binding_id"`
		ReasonCode         string `json:"reason_code"`
		InputAuthorityKind string `json:"input_authority_kind"`
		InputAuthorityRef  string `json:"input_authority_ref,omitempty"`
	}{
		RunID: runID, SourceBindingID: sourceBindingID, ReasonCode: reasonCode,
		InputAuthorityKind: authorityKind, InputAuthorityRef: authorityRef,
	})
	if err != nil {
		return OrchestrationStepPlanInput{}, "", err
	}
	return prepared, payloadHash, nil
}

func prepareOrchestrationStepComplete(input OrchestrationStepCompleteInput) (OrchestrationStepCompleteInput, string, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return OrchestrationStepCompleteInput{}, "", err
	}
	runID, err := normalizeDetectiveHashID(input.RunID, "detective-run:", "run_id")
	if err != nil {
		return OrchestrationStepCompleteInput{}, "", err
	}
	stepID, err := normalizeDetectiveHashID(input.StepID, "detective-step:", "step_id")
	if err != nil {
		return OrchestrationStepCompleteInput{}, "", err
	}
	outcome := strings.TrimSpace(input.Outcome)
	stopReason := strings.TrimSpace(input.StopReason)
	coverage := input.Coverage
	coverage.SchemaVersion = strings.TrimSpace(coverage.SchemaVersion)
	coverage.UnitKind = strings.TrimSpace(coverage.UnitKind)
	if err := validateOrchestrationCoverage(coverage); err != nil {
		return OrchestrationStepCompleteInput{}, "", err
	}
	if err := validateOrchestrationOutcome(outcome, stopReason, coverage); err != nil {
		return OrchestrationStepCompleteInput{}, "", err
	}
	prepared := OrchestrationStepCompleteInput{
		RequestID: requestID, RunID: runID, StepID: stepID,
		Outcome: outcome, StopReason: stopReason, Coverage: coverage,
	}
	payloadHash, err := orchestrationPayloadHash(struct {
		RunID      string                `json:"run_id"`
		StepID     string                `json:"step_id"`
		Outcome    string                `json:"outcome"`
		StopReason string                `json:"stop_reason,omitempty"`
		Coverage   OrchestrationCoverage `json:"coverage"`
	}{RunID: runID, StepID: stepID, Outcome: outcome, StopReason: stopReason, Coverage: coverage})
	if err != nil {
		return OrchestrationStepCompleteInput{}, "", err
	}
	return prepared, payloadHash, nil
}

func validateOrchestrationInputAuthority(kind, ref string) error {
	switch kind {
	case OrchestrationInputAuthorityNone:
		if ref != "" {
			return newDomainError(ErrorInvalidInput, "input_authority_ref must be empty when input_authority_kind is none")
		}
	case OrchestrationInputAuthoritySourceToken:
		if err := validateText(ref, "input_authority_ref", 500); err != nil {
			return err
		}
	case OrchestrationInputAuthoritySourceGeneration:
		if err := validateText(ref, "input_authority_ref", 300); err != nil {
			return err
		}
		if !strings.HasPrefix(ref, "generation:") || len(ref) == len("generation:") {
			return newDomainError(ErrorInvalidInput, "source generation input_authority_ref must start with generation:")
		}
	default:
		return newDomainError(ErrorInvalidInput, "unsupported input_authority_kind %q", kind)
	}
	return nil
}

func validateOrchestrationCoverage(coverage OrchestrationCoverage) error {
	if coverage.SchemaVersion != OrchestrationCoverageSchemaVersion {
		return newDomainError(ErrorInvalidInput, "coverage schema_version must be %s", OrchestrationCoverageSchemaVersion)
	}
	if coverage.UnitKind != OrchestrationCoverageUnitRepositoryFiles && coverage.UnitKind != OrchestrationCoverageUnitTextFiles {
		return newDomainError(ErrorInvalidInput, "unsupported coverage unit_kind %q", coverage.UnitKind)
	}
	if coverage.AttemptedUnitCount < 0 || coverage.AttemptedUnitCount > 1_000_000_000 ||
		coverage.CoveredUnitCount < 0 || coverage.UnsupportedUnitCount < 0 ||
		coverage.CoveredUnitCount > coverage.AttemptedUnitCount ||
		coverage.UnsupportedUnitCount > coverage.AttemptedUnitCount ||
		coverage.CoveredUnitCount+coverage.UnsupportedUnitCount > coverage.AttemptedUnitCount {
		return newDomainError(ErrorInvalidInput, "coverage unit counts must be bounded and covered plus unsupported must not exceed attempted")
	}
	if coverage.Complete && coverage.CoveredUnitCount+coverage.UnsupportedUnitCount != coverage.AttemptedUnitCount {
		return newDomainError(ErrorInvalidInput, "complete coverage must account for every attempted unit")
	}
	return nil
}

func validateOrchestrationOutcome(outcome, stopReason string, coverage OrchestrationCoverage) error {
	switch outcome {
	case OrchestrationStepOutcomeContinue:
		if stopReason != "" || coverage.Complete {
			return newDomainError(ErrorInvalidInput, "continue outcome requires incomplete coverage and no stop_reason")
		}
	case OrchestrationStepOutcomeCompleted:
		if stopReason != OrchestrationStopReasonCoverageSatisfied || !coverage.Complete ||
			coverage.CoveredUnitCount != coverage.AttemptedUnitCount || coverage.UnsupportedUnitCount != 0 {
			return newDomainError(ErrorInvalidInput, "completed outcome requires every attempted unit covered, no unsupported units, and coverage_satisfied stop_reason")
		}
	case OrchestrationStepOutcomeFailed:
		if stopReason != OrchestrationStopReasonSourceFailed && stopReason != OrchestrationStopReasonWorkspaceUnavailable {
			return newDomainError(ErrorInvalidInput, "failed outcome requires source_failed or workspace_unavailable stop_reason")
		}
	case OrchestrationStepOutcomeStopped:
		if !validOrchestrationStoppedReason(stopReason) {
			return newDomainError(ErrorInvalidInput, "stopped outcome has unsupported stop_reason %q", stopReason)
		}
	default:
		return newDomainError(ErrorInvalidInput, "unsupported orchestration outcome %q", outcome)
	}
	return nil
}

func validOrchestrationReason(value string) bool {
	switch value {
	case OrchestrationReasonInitialSource, OrchestrationReasonCoverageGap,
		OrchestrationReasonConflictCheck, OrchestrationReasonRefresh:
		return true
	default:
		return false
	}
}

func validOrchestrationStoppedReason(value string) bool {
	switch value {
	case OrchestrationStopReasonUnsupportedCapability, OrchestrationStopReasonContradictoryOutput,
		OrchestrationStopReasonUngroundedOutput, OrchestrationStopReasonBudgetExhausted,
		OrchestrationStopReasonNoAllowlistedSource:
		return true
	default:
		return false
	}
}

func orchestrationCoverageUnitKind(capabilityName string) string {
	switch capabilityName {
	case SourceCapabilityGitGoRepository:
		return OrchestrationCoverageUnitRepositoryFiles
	case SourceCapabilityLocalPRDText:
		return OrchestrationCoverageUnitTextFiles
	default:
		panic("validated orchestration capability is unsupported")
	}
}

func orchestrationTerminalStatuses(outcome string) (string, string) {
	switch outcome {
	case OrchestrationStepOutcomeContinue:
		return OrchestrationStepStatusCompleted, OrchestrationRunStatusRunning
	case OrchestrationStepOutcomeCompleted:
		return OrchestrationStepStatusCompleted, OrchestrationRunStatusCompleted
	case OrchestrationStepOutcomeFailed:
		return OrchestrationStepStatusFailed, OrchestrationRunStatusFailed
	case OrchestrationStepOutcomeStopped:
		return OrchestrationStepStatusStopped, OrchestrationRunStatusStopped
	default:
		panic("validated orchestration outcome is unsupported")
	}
}

func orchestrationRunID(requestID string) string {
	return "detective-run:" + hashHex([]byte(detectiveIDVersion+"\x00orchestration-run\x00"+requestID))
}

func orchestrationStepID(runID string, stepNumber int) string {
	return "detective-step:" + hashHex(fmt.Appendf(nil, "%s\x00orchestration-step\x00%s\x00%d", detectiveIDVersion, runID, stepNumber))
}

func normalizeDetectiveHashID(value, prefix, field string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return "", newDomainError(ErrorInvalidInput, "%s must contain %s followed by 64 lowercase hexadecimal characters", field, prefix)
	}
	digest := value[len(prefix):]
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return "", newDomainError(ErrorInvalidInput, "%s must contain %s followed by 64 lowercase hexadecimal characters", field, prefix)
	}
	return value, nil
}

func normalizeOptionalContentHash(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return "", newDomainError(ErrorInvalidInput, "%s must contain sha256: followed by 64 lowercase hexadecimal characters", field)
	}
	digest := value[len(prefix):]
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return "", newDomainError(ErrorInvalidInput, "%s must contain sha256: followed by 64 lowercase hexadecimal characters", field)
	}
	return value, nil
}

func orchestrationPayloadHash(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("serializing orchestration transition payload: %w", err)
	}
	return contentHash(payload), nil
}

func lockOrchestrationTransitionRequest(ctx context.Context, tx pgx.Tx, requestID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_ingestion_request_serializations (operation_name, request_id)
		VALUES ('detective-orchestration-transition', $1)
		ON CONFLICT (operation_name, request_id) DO NOTHING
	`, requestID); err != nil {
		return fmt.Errorf("creating orchestration transition serialization: %w", err)
	}
	var locked int
	if err := tx.QueryRow(ctx, `
		SELECT 1
		FROM evidence_ingestion_request_serializations
		WHERE operation_name = 'detective-orchestration-transition' AND request_id = $1
		FOR UPDATE
	`, requestID).Scan(&locked); err != nil {
		return fmt.Errorf("locking orchestration transition request: %w", err)
	}
	return nil
}

func readOrchestrationTransitionRequest(ctx context.Context, db workspaceQuerier, requestID string) (orchestrationTransitionRequest, bool, error) {
	var request orchestrationTransitionRequest
	var stepID *string
	err := db.QueryRow(ctx, `
		SELECT request_id, transition_kind, run_id, step_id, request_payload_hash
		FROM detective_orchestration_transition_requests
		WHERE request_id = $1
	`, requestID).Scan(&request.RequestID, &request.Kind, &request.RunID, &stepID, &request.PayloadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrationTransitionRequest{}, false, nil
	}
	if err != nil {
		return orchestrationTransitionRequest{}, false, fmt.Errorf("reading orchestration transition request: %w", err)
	}
	if stepID != nil {
		request.StepID = *stepID
	}
	return request, true, nil
}

func insertOrchestrationTransitionRequest(ctx context.Context, tx pgx.Tx, request orchestrationTransitionRequest) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO detective_orchestration_transition_requests (
			request_id, transition_kind, run_id, step_id, request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5)
	`, request.RequestID, request.Kind, request.RunID, nullableDetectiveText(request.StepID), request.PayloadHash)
	if err != nil {
		return fmt.Errorf("inserting orchestration transition request: %w", err)
	}
	return nil
}

func ensureOrchestrationTransitionReplay(persisted orchestrationTransitionRequest, kind, runID, stepID, payloadHash string) error {
	if persisted.Kind != kind || persisted.RunID != runID || persisted.StepID != stepID || persisted.PayloadHash != payloadHash {
		return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different orchestration transition payload", persisted.RequestID)
	}
	return nil
}

func readOrchestrationRun(ctx context.Context, db workspaceQuerier, runID string, forUpdate bool) (OrchestrationRun, bool, error) {
	query := `
		SELECT run_id, workspace_id, status, max_steps, step_count, stop_reason, started_at, finished_at
		FROM detective_orchestration_runs
		WHERE run_id = $1
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var run OrchestrationRun
	var stopReason *string
	var finishedAt *time.Time
	err := db.QueryRow(ctx, query, runID).Scan(
		&run.ID, &run.WorkspaceID, &run.Status, &run.MaxSteps, &run.StepCount,
		&stopReason, &run.StartedAt, &finishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationRun{}, false, nil
	}
	if err != nil {
		return OrchestrationRun{}, false, fmt.Errorf("reading orchestration run: %w", err)
	}
	run.StartedAt = run.StartedAt.UTC()
	if stopReason != nil {
		run.StopReason = *stopReason
	}
	if finishedAt != nil {
		value := finishedAt.UTC()
		run.FinishedAt = &value
	}
	run.Steps = make([]OrchestrationStep, 0, run.StepCount)

	rows, err := db.Query(ctx, `
		SELECT
			step.step_id, step.run_id, step.workspace_id, step.step_number, step.source_binding_id,
			step.capability_name, step.capability_version, step.source_system, step.source_id,
			step.reason_code, step.input_authority_kind, step.input_authority_ref,
			step.status, step.outcome, step.stop_reason, step.coverage_schema_version, step.coverage_unit_kind,
			step.attempted_unit_count, step.covered_unit_count, step.unsupported_unit_count,
			step.coverage_complete, step.planned_at, step.finished_at
		FROM detective_orchestration_steps AS step
		JOIN detective_workspace_sources AS source
		  ON source.workspace_id = step.workspace_id
		 AND source.source_binding_id = step.source_binding_id
		 AND source.capability_name = step.capability_name
		 AND source.capability_version = step.capability_version
		 AND source.source_system = step.source_system
		 AND source.source_id = step.source_id
		WHERE step.run_id = $1
		ORDER BY step.step_number
	`, runID)
	if err != nil {
		return OrchestrationRun{}, false, fmt.Errorf("listing orchestration steps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		step, err := scanOrchestrationStep(rows)
		if err != nil {
			return OrchestrationRun{}, false, err
		}
		run.Steps = append(run.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return OrchestrationRun{}, false, fmt.Errorf("iterating orchestration steps: %w", err)
	}
	if err := validateOrchestrationRunAudit(run); err != nil {
		return OrchestrationRun{}, false, err
	}
	return run, true, nil
}

type orchestrationStepScanner interface {
	Scan(dest ...any) error
}

func scanOrchestrationStep(scanner orchestrationStepScanner) (OrchestrationStep, error) {
	var step OrchestrationStep
	var inputAuthorityRef, outcome, stopReason, coverageSchema, coverageUnitKind *string
	var attempted, covered, unsupported *int
	var coverageComplete *bool
	var finishedAt *time.Time
	if err := scanner.Scan(
		&step.ID, &step.RunID, &step.WorkspaceID, &step.Number, &step.SourceBindingID,
		&step.CapabilityName, &step.CapabilityVersion, &step.SourceSystem, &step.SourceID,
		&step.ReasonCode, &step.InputAuthorityKind, &inputAuthorityRef,
		&step.Status, &outcome, &stopReason, &coverageSchema, &coverageUnitKind,
		&attempted, &covered, &unsupported, &coverageComplete, &step.PlannedAt, &finishedAt,
	); err != nil {
		return OrchestrationStep{}, fmt.Errorf("scanning orchestration step: %w", err)
	}
	step.PlannedAt = step.PlannedAt.UTC()
	if inputAuthorityRef != nil {
		step.InputAuthorityRef = *inputAuthorityRef
	}
	if outcome != nil {
		step.Outcome = *outcome
	}
	if stopReason != nil {
		step.StopReason = *stopReason
	}
	if coverageSchema != nil && coverageUnitKind != nil && attempted != nil && covered != nil && unsupported != nil && coverageComplete != nil {
		step.Coverage = &OrchestrationCoverage{
			SchemaVersion: *coverageSchema, UnitKind: *coverageUnitKind, AttemptedUnitCount: *attempted,
			CoveredUnitCount: *covered, UnsupportedUnitCount: *unsupported,
			Complete: *coverageComplete,
		}
	}
	if finishedAt != nil {
		value := finishedAt.UTC()
		step.FinishedAt = &value
	}
	return step, nil
}

func orchestrationStepByID(steps []OrchestrationStep, stepID string) (OrchestrationStep, bool) {
	for _, step := range steps {
		if step.ID == stepID {
			return step, true
		}
	}
	return OrchestrationStep{}, false
}

func validateOrchestrationRunAudit(run OrchestrationRun) error {
	if run.StepCount != len(run.Steps) {
		return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has %d persisted steps, want %d", run.ID, len(run.Steps), run.StepCount)
	}
	for index, step := range run.Steps {
		expectedNumber := index + 1
		if step.RunID != run.ID || step.WorkspaceID != run.WorkspaceID || step.Number != expectedNumber || step.ID != orchestrationStepID(run.ID, expectedNumber) {
			return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s step %d has inconsistent immutable identity", run.ID, expectedNumber)
		}
		capability, ok := lookupSourceCapability(step.CapabilityName, step.CapabilityVersion)
		if !ok || capability.SourceSystem != step.SourceSystem {
			return newDomainError(ErrorUnsupportedCapability, "orchestration step %s uses unsupported capability %s/%s", step.ID, step.CapabilityName, step.CapabilityVersion)
		}
		if index < len(run.Steps)-1 && (step.Status != OrchestrationStepStatusCompleted || step.Outcome != OrchestrationStepOutcomeContinue) {
			return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has non-continuing step %s before its final step", run.ID, step.ID)
		}
	}

	if run.Status == OrchestrationRunStatusRunning {
		if len(run.Steps) == 0 {
			return nil
		}
		last := run.Steps[len(run.Steps)-1]
		if last.Status == OrchestrationStepStatusPlanned {
			return nil
		}
		if last.Status == OrchestrationStepStatusCompleted && last.Outcome == OrchestrationStepOutcomeContinue && last.Number < run.MaxSteps {
			return nil
		}
		return newDomainError(ErrorOrchestrationRunConflict, "running orchestration run %s has incompatible final step %s", run.ID, last.ID)
	}
	if len(run.Steps) == 0 {
		return newDomainError(ErrorOrchestrationRunConflict, "terminal orchestration run %s has no audited step", run.ID)
	}
	last := run.Steps[len(run.Steps)-1]
	wantStepStatus := ""
	wantOutcome := ""
	switch run.Status {
	case OrchestrationRunStatusCompleted:
		wantStepStatus, wantOutcome = OrchestrationStepStatusCompleted, OrchestrationStepOutcomeCompleted
	case OrchestrationRunStatusFailed:
		wantStepStatus, wantOutcome = OrchestrationStepStatusFailed, OrchestrationStepOutcomeFailed
	case OrchestrationRunStatusStopped:
		wantStepStatus, wantOutcome = OrchestrationStepStatusStopped, OrchestrationStepOutcomeStopped
	default:
		return newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has unsupported status %s", run.ID, run.Status)
	}
	if last.Status != wantStepStatus || last.Outcome != wantOutcome || last.StopReason != run.StopReason {
		return newDomainError(ErrorOrchestrationRunConflict, "terminal orchestration run %s does not match final step %s", run.ID, last.ID)
	}
	return nil
}

func workspaceSourceByID(sources []WorkspaceSourceBinding, sourceBindingID string) (WorkspaceSourceBinding, bool) {
	for _, source := range sources {
		if source.ID == sourceBindingID {
			return source, true
		}
	}
	return WorkspaceSourceBinding{}, false
}

func nullableDetectiveText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableDetectiveTime(runStatus string, value time.Time) any {
	if runStatus == OrchestrationRunStatusRunning {
		return nil
	}
	return value
}
