package detective

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	periodicSourceCycleContract        = "detective-periodic-source-cycle-v1"
	periodicPlannerConsumptionContract = "detective-periodic-planner-consumption-v1"
)

// PeriodicSourceTickInput identifies one registered source for one durable wakeup.
// A wakeup is only a hint; durable cycle identity is derived inside the controller.
type PeriodicSourceTickInput struct {
	WorkspaceID     string `json:"workspace_id"`
	SourceBindingID string `json:"source_binding_id"`
	MaxSteps        int    `json:"max_steps"`
}

// PeriodicSourceCycle is one durable source-token processing cycle.
type PeriodicSourceCycle struct {
	ID               string     `json:"cycle_id"`
	WorkspaceID      string     `json:"workspace_id"`
	SourceBindingID  string     `json:"source_binding_id"`
	Number           int64      `json:"cycle_number"`
	SourceToken      string     `json:"source_token"`
	OneShotRequestID string     `json:"one_shot_request_id"`
	RunID            string     `json:"run_id"`
	MaxSteps         int        `json:"max_steps"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	FinalizedAt      *time.Time `json:"finalized_at,omitempty"`
}

// PeriodicSourceTickResult reports the durable cycle and one-shot state after a wakeup.
type PeriodicSourceTickResult struct {
	Cycle               PeriodicSourceCycle          `json:"cycle"`
	Run                 OrchestrationRun             `json:"run"`
	PlannerContinuation *PeriodicPlannerContinuation `json:"planner_continuation,omitempty"`
	Replayed            bool                         `json:"replayed"`
	SourceInvoked       bool                         `json:"source_invoked"`
	CursorAdvanced      bool                         `json:"cursor_advanced"`
}

// PeriodicPlannerContinuation reports one bounded advisory continuation.
// Raw planner output is intentionally neither returned nor persisted.
type PeriodicPlannerContinuation struct {
	Attempted      bool                                    `json:"attempted"`
	Recommendation *PlannerRecommendation                  `json:"recommendation,omitempty"`
	Consumption    *PlannerRecommendationConsumptionResult `json:"consumption,omitempty"`
	Resumed        bool                                    `json:"resumed"`
}

type periodicSourceTickRequest struct {
	workspaceID     string
	sourceBindingID string
	maxSteps        int
}

type periodicSourceCursor struct {
	workspaceID      string
	sourceBindingID  string
	cycleCount       int64
	activeCycleID    string
	lastCycleID      string
	cursorToken      string
	cursorAdvancedAt *time.Time
}

type periodicSourceReservation struct {
	cycle           PeriodicSourceCycle
	run             OrchestrationRun
	cycleReplayed   bool
	cursorCommitted bool
}

// RunPeriodicSourceTick invokes at most one step of one durable source cycle.
func RunPeriodicSourceTick(ctx context.Context, pool *pgxpool.Pool, input PeriodicSourceTickInput) (PeriodicSourceTickResult, error) {
	return runPeriodicSourceTick(ctx, pool, input, nil)
}

// RunPlannerPeriodicSourceTick composes one deterministic source tick with at
// most one bounded planner continuation. The planner remains advisory until the
// exact PostgreSQL-authoritative consumer accepts its recommendation.
func RunPlannerPeriodicSourceTick(
	ctx context.Context,
	pool *pgxpool.Pool,
	input PeriodicSourceTickInput,
	runner PlannerRunner,
) (PeriodicSourceTickResult, error) {
	if runner == nil {
		return PeriodicSourceTickResult{}, newDomainError(ErrorInvalidInput, "planner runner is required")
	}
	return runPeriodicSourceTick(ctx, pool, input, runner)
}

func runPeriodicSourceTick(
	ctx context.Context,
	pool *pgxpool.Pool,
	input PeriodicSourceTickInput,
	runner PlannerRunner,
) (PeriodicSourceTickResult, error) {
	if pool == nil {
		return PeriodicSourceTickResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := preparePeriodicSourceTick(input)
	if err != nil {
		return PeriodicSourceTickResult{}, err
	}
	workspace, err := ResolveWorkspace(ctx, pool, request.workspaceID)
	if err != nil {
		return PeriodicSourceTickResult{}, err
	}
	source, found := workspaceSourceByID(workspace.Sources, request.sourceBindingID)
	if !found {
		return PeriodicSourceTickResult{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			request.sourceBindingID,
			request.workspaceID,
		)
	}
	authorityKind, sourceToken, err := plannerSourceAuthority(ctx, workspace, source)
	if err != nil {
		return PeriodicSourceTickResult{}, err
	}
	if authorityKind != OrchestrationInputAuthoritySourceToken {
		return PeriodicSourceTickResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source %s returned unsupported authority kind %s",
			source.ID,
			authorityKind,
		)
	}
	if err := validateText(sourceToken, "source_token", 500); err != nil {
		return PeriodicSourceTickResult{}, err
	}

	reservation, err := reservePeriodicSourceCycle(ctx, pool, request, workspace, source, sourceToken)
	if err != nil {
		return PeriodicSourceTickResult{}, err
	}
	result := PeriodicSourceTickResult{
		Cycle: reservation.cycle, Run: reservation.run,
		Replayed: reservation.cycleReplayed, CursorAdvanced: reservation.cursorCommitted,
	}
	if reservation.cursorCommitted {
		return result, nil
	}
	if reservation.run.Status != OrchestrationRunStatusRunning {
		return finalizePeriodicSourceTick(ctx, pool, result)
	}

	if runner != nil {
		resumed, found, err := resumePendingPlannerRecommendationConsumption(ctx, pool, reservation.run.ID)
		if err != nil {
			return result, err
		}
		if found {
			result.PlannerContinuation = &PeriodicPlannerContinuation{
				Consumption: &resumed,
				Resumed:     true,
			}
			result.Run = resumed.Run
			result.SourceInvoked = resumed.SourceInvoked
			return finalizePeriodicSourceTick(ctx, pool, result)
		}

		hasPlannerHistory, err := hasPlannerRecommendationConsumption(ctx, pool, reservation.run.ID)
		if err != nil {
			return result, err
		}
		if hasPlannerHistory {
			return continuePeriodicSourceTickWithPlanner(ctx, pool, result, runner)
		}
	}

	oneShot, err := RunOneShotOrchestration(ctx, pool, OneShotOrchestrationInput{
		RequestID:       reservation.cycle.OneShotRequestID,
		WorkspaceID:     reservation.cycle.WorkspaceID,
		SourceBindingID: reservation.cycle.SourceBindingID,
		MaxSteps:        reservation.cycle.MaxSteps,
	})
	if err != nil {
		return PeriodicSourceTickResult{}, err
	}
	if oneShot.RequestID != reservation.cycle.OneShotRequestID || oneShot.Run.ID != reservation.cycle.RunID {
		return PeriodicSourceTickResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic cycle %s returned inconsistent one-shot run authority",
			reservation.cycle.ID,
		)
	}
	result.Run = oneShot.Run
	result.SourceInvoked = oneShot.SourceInvoked
	if oneShot.Run.Status == OrchestrationRunStatusRunning && runner != nil {
		return continuePeriodicSourceTickWithPlanner(ctx, pool, result, runner)
	}
	return finalizePeriodicSourceTick(ctx, pool, result)
}

func continuePeriodicSourceTickWithPlanner(
	ctx context.Context,
	pool *pgxpool.Pool,
	result PeriodicSourceTickResult,
	runner PlannerRunner,
) (PeriodicSourceTickResult, error) {
	continuation := &PeriodicPlannerContinuation{Attempted: true}
	result.PlannerContinuation = continuation

	recommendation, err := RecommendNextSource(ctx, pool, result.Run.ID, runner)
	if err != nil {
		return result, err
	}
	continuation.Recommendation = &recommendation
	if recommendation.Classification == PlannerClassificationAbstain {
		return result, nil
	}
	if recommendation.Classification != PlannerClassificationQuerySource {
		return result, newDomainError(
			ErrorPlannerMalformedOutput,
			"planner recommendation uses unsupported classification %q",
			recommendation.Classification,
		)
	}

	consumed, err := ConsumePlannerRecommendation(ctx, pool, PlannerRecommendationConsumptionInput{
		RequestID:      periodicPlannerConsumptionRequestID(result.Cycle.ID, recommendation.PlanningContextHash),
		Recommendation: recommendation,
	})
	if err != nil {
		return result, err
	}
	continuation.Consumption = &consumed
	result.Run = consumed.Run
	result.SourceInvoked = result.SourceInvoked || consumed.SourceInvoked
	return finalizePeriodicSourceTick(ctx, pool, result)
}

func finalizePeriodicSourceTick(
	ctx context.Context,
	pool *pgxpool.Pool,
	result PeriodicSourceTickResult,
) (PeriodicSourceTickResult, error) {
	if result.Run.Status == OrchestrationRunStatusRunning {
		return result, nil
	}
	cycle, run, err := finalizePeriodicSourceCycle(ctx, pool, result.Cycle.ID)
	if err != nil {
		return result, err
	}
	result.Cycle = cycle
	result.Run = run
	result.CursorAdvanced = true
	return result, nil
}

func periodicPlannerConsumptionRequestID(cycleID, planningContextHash string) string {
	return "detective-periodic-planner:" + hashHex([]byte(
		periodicPlannerConsumptionContract+"\x00"+cycleID+"\x00"+planningContextHash,
	))
}

func preparePeriodicSourceTick(input PeriodicSourceTickInput) (periodicSourceTickRequest, error) {
	workspaceID, err := normalizeWorkspaceID(input.WorkspaceID)
	if err != nil {
		return periodicSourceTickRequest{}, err
	}
	sourceBindingID, err := normalizeDetectiveHashID(input.SourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return periodicSourceTickRequest{}, err
	}
	if input.MaxSteps < 1 || input.MaxSteps > maxOrchestrationSteps {
		return periodicSourceTickRequest{}, newDomainError(ErrorInvalidInput, "max_steps must be between 1 and %d", maxOrchestrationSteps)
	}
	return periodicSourceTickRequest{
		workspaceID: workspaceID, sourceBindingID: sourceBindingID, maxSteps: input.MaxSteps,
	}, nil
}

func reservePeriodicSourceCycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	request periodicSourceTickRequest,
	resolvedWorkspace Workspace,
	resolvedSource WorkspaceSourceBinding,
	sourceToken string,
) (periodicSourceReservation, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return periodicSourceReservation{}, fmt.Errorf("beginning periodic source cycle reservation: %w", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `
		INSERT INTO detective_periodic_source_cursors (workspace_id, source_binding_id)
		VALUES ($1,$2)
		ON CONFLICT (workspace_id, source_binding_id) DO NOTHING
	`, request.workspaceID, request.sourceBindingID); err != nil {
		return periodicSourceReservation{}, fmt.Errorf("creating periodic source cursor: %w", err)
	}
	cursor, found, err := readPeriodicSourceCursor(ctx, tx, request.workspaceID, request.sourceBindingID, true)
	if err != nil {
		return periodicSourceReservation{}, err
	}
	if !found {
		return periodicSourceReservation{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cursor %s/%s could not be created",
			request.workspaceID,
			request.sourceBindingID,
		)
	}
	workspace, found, err := readWorkspace(ctx, tx, request.workspaceID, false)
	if err != nil {
		return periodicSourceReservation{}, err
	}
	if !found {
		return periodicSourceReservation{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", request.workspaceID)
	}
	source, found := workspaceSourceByID(workspace.Sources, request.sourceBindingID)
	if !found {
		return periodicSourceReservation{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			request.sourceBindingID,
			request.workspaceID,
		)
	}
	if workspace.RootPath != resolvedWorkspace.RootPath || source != resolvedSource {
		return periodicSourceReservation{}, newDomainError(
			ErrorWorkspaceConflict,
			"periodic source %s registry authority changed during reservation",
			request.sourceBindingID,
		)
	}

	if cursor.activeCycleID != "" {
		cycle, run, err := readAndValidatePeriodicSourceCycle(ctx, tx, workspace, source, cursor.activeCycleID)
		if err != nil {
			return periodicSourceReservation{}, err
		}
		if cycle.Status != OrchestrationRunStatusRunning {
			return periodicSourceReservation{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"periodic source cursor %s/%s references terminal active cycle %s",
				request.workspaceID,
				request.sourceBindingID,
				cycle.ID,
			)
		}
		if cycle.Number != cursor.cycleCount {
			return periodicSourceReservation{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"periodic source active cycle %s does not match cursor sequence %d",
				cycle.ID,
				cursor.cycleCount,
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return periodicSourceReservation{}, fmt.Errorf("committing periodic active-cycle replay: %w", err)
		}
		return periodicSourceReservation{cycle: cycle, run: run, cycleReplayed: true}, nil
	}

	if cursor.cursorToken == sourceToken {
		if cursor.lastCycleID == "" {
			return periodicSourceReservation{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"periodic source cursor %s/%s has a token without a terminal cycle",
				request.workspaceID,
				request.sourceBindingID,
			)
		}
		cycle, run, err := readAndValidatePeriodicSourceCycle(ctx, tx, workspace, source, cursor.lastCycleID)
		if err != nil {
			return periodicSourceReservation{}, err
		}
		if cycle.Status == OrchestrationRunStatusRunning || run.Status == OrchestrationRunStatusRunning || cycle.SourceToken != sourceToken {
			return periodicSourceReservation{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"periodic source cursor %s/%s does not match terminal cycle %s",
				request.workspaceID,
				request.sourceBindingID,
				cycle.ID,
			)
		}
		if cycle.Number != cursor.cycleCount {
			return periodicSourceReservation{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"periodic source terminal cycle %s does not match cursor sequence %d",
				cycle.ID,
				cursor.cycleCount,
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return periodicSourceReservation{}, fmt.Errorf("committing periodic cursor replay: %w", err)
		}
		return periodicSourceReservation{
			cycle: cycle, run: run, cycleReplayed: true, cursorCommitted: true,
		}, nil
	}

	cycleNumber := cursor.cycleCount + 1
	cycleID := periodicSourceCycleID(request.workspaceID, request.sourceBindingID, cycleNumber, sourceToken)
	oneShotRequestID := periodicOneShotRequestID(cycleID)
	oneShotRequest, err := prepareOneShotOrchestration(OneShotOrchestrationInput{
		RequestID: oneShotRequestID, WorkspaceID: request.workspaceID,
		SourceBindingID: request.sourceBindingID, MaxSteps: request.maxSteps,
	})
	if err != nil {
		return periodicSourceReservation{}, err
	}
	oneShotRequest, err = bindOneShotSourcePolicy(oneShotRequest, source)
	if err != nil {
		return periodicSourceReservation{}, err
	}
	startInput, runID, startPayloadHash, err := prepareOrchestrationRunStart(OrchestrationRunStartInput{
		RequestID: oneShotRequest.requestID, WorkspaceID: oneShotRequest.workspaceID,
		MaxSteps: oneShotRequest.maxSteps, ScopeHash: oneShotRequest.scopeHash,
	})
	if err != nil {
		return periodicSourceReservation{}, err
	}
	if err := lockOrchestrationTransitionRequest(ctx, tx, oneShotRequestID); err != nil {
		return periodicSourceReservation{}, err
	}
	if _, exists, err := readOrchestrationTransitionRequest(ctx, tx, oneShotRequestID); err != nil {
		return periodicSourceReservation{}, err
	} else if exists {
		return periodicSourceReservation{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic cycle %s has a start transition without cursor authority",
			cycleID,
		)
	}
	if err := validateResolvedWorkspace(ctx, workspace); err != nil {
		return periodicSourceReservation{}, err
	}
	started, err := startOrchestrationRunLocked(ctx, tx, startInput, runID, startPayloadHash, workspace)
	if err != nil {
		return periodicSourceReservation{}, err
	}

	cycle := PeriodicSourceCycle{
		ID: cycleID, WorkspaceID: request.workspaceID, SourceBindingID: request.sourceBindingID,
		Number: cycleNumber, SourceToken: sourceToken, OneShotRequestID: oneShotRequestID,
		RunID: runID, MaxSteps: request.maxSteps, Status: OrchestrationRunStatusRunning,
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO detective_periodic_source_cycles (
			cycle_id, workspace_id, source_binding_id, cycle_number,
			source_token, one_shot_request_id, run_id, max_steps, status
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING created_at
	`, cycle.ID, cycle.WorkspaceID, cycle.SourceBindingID, cycle.Number,
		cycle.SourceToken, cycle.OneShotRequestID, cycle.RunID, cycle.MaxSteps, cycle.Status,
	).Scan(&cycle.CreatedAt); err != nil {
		return periodicSourceReservation{}, fmt.Errorf("inserting periodic source cycle: %w", err)
	}
	cycle.CreatedAt = cycle.CreatedAt.UTC()
	tag, err := tx.Exec(ctx, `
		UPDATE detective_periodic_source_cursors
		SET cycle_count = $3, active_cycle_id = $4
		WHERE workspace_id = $1 AND source_binding_id = $2
		  AND cycle_count = $5 AND active_cycle_id IS NULL
	`, request.workspaceID, request.sourceBindingID, cycleNumber, cycle.ID, cursor.cycleCount)
	if err != nil {
		return periodicSourceReservation{}, fmt.Errorf("activating periodic source cycle: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return periodicSourceReservation{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cursor %s/%s changed during cycle reservation",
			request.workspaceID,
			request.sourceBindingID,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return periodicSourceReservation{}, fmt.Errorf("committing periodic source cycle reservation: %w", err)
	}
	return periodicSourceReservation{cycle: cycle, run: started.Run}, nil
}

func finalizePeriodicSourceCycle(
	ctx context.Context,
	pool *pgxpool.Pool,
	cycleID string,
) (PeriodicSourceCycle, OrchestrationRun, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, fmt.Errorf("beginning periodic source cycle finalization: %w", err)
	}
	defer tx.Rollback(context.Background())

	cycle, found, err := readPeriodicSourceCycle(ctx, tx, cycleID)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if !found {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s was not found",
			cycleID,
		)
	}
	cursor, found, err := readPeriodicSourceCursor(ctx, tx, cycle.WorkspaceID, cycle.SourceBindingID, true)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if !found {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s has no cursor",
			cycle.ID,
		)
	}
	workspace, found, err := readWorkspace(ctx, tx, cycle.WorkspaceID, false)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if !found {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", cycle.WorkspaceID)
	}
	source, found := workspaceSourceByID(workspace.Sources, cycle.SourceBindingID)
	if !found {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			cycle.SourceBindingID,
			cycle.WorkspaceID,
		)
	}
	cycle, run, err := readAndValidatePeriodicSourceCycle(ctx, tx, workspace, source, cycle.ID)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if cycle.Status != OrchestrationRunStatusRunning {
		if cursor.activeCycleID != "" || cursor.lastCycleID != cycle.ID || cursor.cursorToken != cycle.SourceToken {
			return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"terminal periodic source cycle %s has inconsistent cursor authority",
				cycle.ID,
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return PeriodicSourceCycle{}, OrchestrationRun{}, fmt.Errorf("committing periodic source cycle finalization replay: %w", err)
		}
		return cycle, run, nil
	}
	if cursor.activeCycleID != cycle.ID {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s does not own its source cursor",
			cycle.ID,
		)
	}
	if cursor.cycleCount != cycle.Number {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s does not match cursor sequence %d",
			cycle.ID,
			cursor.cycleCount,
		)
	}
	if run.Status == OrchestrationRunStatusRunning || run.FinishedAt == nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s cannot advance before run %s is terminal",
			cycle.ID,
			run.ID,
		)
	}

	finalizedAt := run.FinishedAt.UTC()
	tag, err := tx.Exec(ctx, `
		UPDATE detective_periodic_source_cycles
		SET status = $2, finalized_at = $3
		WHERE cycle_id = $1 AND status = 'running' AND finalized_at IS NULL
	`, cycle.ID, run.Status, finalizedAt)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, fmt.Errorf("finalizing periodic source cycle: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s changed during finalization",
			cycle.ID,
		)
	}
	tag, err = tx.Exec(ctx, `
		UPDATE detective_periodic_source_cursors
		SET active_cycle_id = NULL,
			last_cycle_id = $3,
			cursor_token = $4,
			cursor_advanced_at = $5
		WHERE workspace_id = $1 AND source_binding_id = $2
		  AND active_cycle_id = $3 AND cycle_count = $6
	`, cycle.WorkspaceID, cycle.SourceBindingID, cycle.ID, cycle.SourceToken, finalizedAt, cycle.Number)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, fmt.Errorf("advancing periodic source cursor: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cursor %s/%s changed during finalization",
			cycle.WorkspaceID,
			cycle.SourceBindingID,
		)
	}
	cycle.Status = run.Status
	cycle.FinalizedAt = &finalizedAt
	if err := tx.Commit(ctx); err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, fmt.Errorf("committing periodic source cycle finalization: %w", err)
	}
	return cycle, run, nil
}

func readAndValidatePeriodicSourceCycle(
	ctx context.Context,
	db workspaceQuerier,
	workspace Workspace,
	source WorkspaceSourceBinding,
	cycleID string,
) (PeriodicSourceCycle, OrchestrationRun, error) {
	cycle, found, err := readPeriodicSourceCycle(ctx, db, cycleID)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if !found {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(ErrorOrchestrationRunConflict, "periodic source cycle %s was not found", cycleID)
	}
	if cycle.WorkspaceID != workspace.ID || cycle.SourceBindingID != source.ID ||
		cycle.ID != periodicSourceCycleID(cycle.WorkspaceID, cycle.SourceBindingID, cycle.Number, cycle.SourceToken) ||
		cycle.OneShotRequestID != periodicOneShotRequestID(cycle.ID) ||
		cycle.RunID != orchestrationRunID(cycle.OneShotRequestID) {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s has inconsistent deterministic identity",
			cycle.ID,
		)
	}
	oneShotRequest, err := prepareOneShotOrchestration(OneShotOrchestrationInput{
		RequestID: cycle.OneShotRequestID, WorkspaceID: cycle.WorkspaceID,
		SourceBindingID: cycle.SourceBindingID, MaxSteps: cycle.MaxSteps,
	})
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	oneShotRequest, err = bindOneShotSourcePolicy(oneShotRequest, source)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	_, expectedRunID, payloadHash, err := prepareOrchestrationRunStart(OrchestrationRunStartInput{
		RequestID: oneShotRequest.requestID, WorkspaceID: oneShotRequest.workspaceID,
		MaxSteps: oneShotRequest.maxSteps, ScopeHash: oneShotRequest.scopeHash,
	})
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	transition, found, err := readOrchestrationTransitionRequest(ctx, db, cycle.OneShotRequestID)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if !found {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s has no start transition",
			cycle.ID,
		)
	}
	if err := ensureOrchestrationTransitionReplay(
		transition,
		orchestrationTransitionStartRun,
		expectedRunID,
		"",
		payloadHash,
	); err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	run, found, err := readOrchestrationRun(ctx, db, cycle.RunID, true)
	if err != nil {
		return PeriodicSourceCycle{}, OrchestrationRun{}, err
	}
	if !found || run.WorkspaceID != cycle.WorkspaceID || run.MaxSteps != cycle.MaxSteps {
		return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"periodic source cycle %s has inconsistent run authority",
			cycle.ID,
		)
	}
	if cycle.Status != OrchestrationRunStatusRunning {
		if run.Status != cycle.Status || run.FinishedAt == nil || cycle.FinalizedAt == nil || !run.FinishedAt.Equal(*cycle.FinalizedAt) {
			return PeriodicSourceCycle{}, OrchestrationRun{}, newDomainError(
				ErrorOrchestrationRunConflict,
				"terminal periodic source cycle %s does not match run %s",
				cycle.ID,
				run.ID,
			)
		}
	}
	return cycle, run, nil
}

func readPeriodicSourceCursor(
	ctx context.Context,
	db workspaceQuerier,
	workspaceID string,
	sourceBindingID string,
	lock bool,
) (periodicSourceCursor, bool, error) {
	query := `
		SELECT
			workspace_id, source_binding_id, cycle_count,
			active_cycle_id, last_cycle_id, cursor_token, cursor_advanced_at
		FROM detective_periodic_source_cursors
		WHERE workspace_id = $1 AND source_binding_id = $2
	`
	if lock {
		query += " FOR UPDATE"
	}
	var cursor periodicSourceCursor
	var activeCycleID, lastCycleID, cursorToken *string
	err := db.QueryRow(ctx, query, workspaceID, sourceBindingID).Scan(
		&cursor.workspaceID,
		&cursor.sourceBindingID,
		&cursor.cycleCount,
		&activeCycleID,
		&lastCycleID,
		&cursorToken,
		&cursor.cursorAdvancedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return periodicSourceCursor{}, false, nil
	}
	if err != nil {
		return periodicSourceCursor{}, false, fmt.Errorf("reading periodic source cursor: %w", err)
	}
	if activeCycleID != nil {
		cursor.activeCycleID = *activeCycleID
	}
	if lastCycleID != nil {
		cursor.lastCycleID = *lastCycleID
	}
	if cursorToken != nil {
		cursor.cursorToken = *cursorToken
	}
	if cursor.cursorAdvancedAt != nil {
		value := cursor.cursorAdvancedAt.UTC()
		cursor.cursorAdvancedAt = &value
	}
	return cursor, true, nil
}

func readPeriodicSourceCycle(
	ctx context.Context,
	db workspaceQuerier,
	cycleID string,
) (PeriodicSourceCycle, bool, error) {
	var cycle PeriodicSourceCycle
	err := db.QueryRow(ctx, `
		SELECT
			cycle_id, workspace_id, source_binding_id, cycle_number,
			source_token, one_shot_request_id, run_id, max_steps,
			status, created_at, finalized_at
		FROM detective_periodic_source_cycles
		WHERE cycle_id = $1
	`, cycleID).Scan(
		&cycle.ID,
		&cycle.WorkspaceID,
		&cycle.SourceBindingID,
		&cycle.Number,
		&cycle.SourceToken,
		&cycle.OneShotRequestID,
		&cycle.RunID,
		&cycle.MaxSteps,
		&cycle.Status,
		&cycle.CreatedAt,
		&cycle.FinalizedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PeriodicSourceCycle{}, false, nil
	}
	if err != nil {
		return PeriodicSourceCycle{}, false, fmt.Errorf("reading periodic source cycle: %w", err)
	}
	cycle.CreatedAt = cycle.CreatedAt.UTC()
	if cycle.FinalizedAt != nil {
		value := cycle.FinalizedAt.UTC()
		cycle.FinalizedAt = &value
	}
	return cycle, true, nil
}

func periodicSourceCycleID(workspaceID, sourceBindingID string, cycleNumber int64, sourceToken string) string {
	material := fmt.Sprintf(
		"%s\x00%s\x00%s\x00%d\x00%s",
		periodicSourceCycleContract,
		workspaceID,
		sourceBindingID,
		cycleNumber,
		sourceToken,
	)
	return "detective-periodic-cycle:" + hashHex([]byte(material))
}

func periodicOneShotRequestID(cycleID string) string {
	return "detective-periodic-one-shot:" + hashHex([]byte(periodicSourceCycleContract+"\x00one-shot\x00"+cycleID))
}
