package detective

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PlannerRecommendationConsumptionInput identifies one exact advisory recommendation.
type PlannerRecommendationConsumptionInput struct {
	RequestID      string                `json:"request_id"`
	Recommendation PlannerRecommendation `json:"recommendation"`
}

// PlannerRecommendationConsumptionResult reports the durable plan and its current execution state.
type PlannerRecommendationConsumptionResult struct {
	RequestID           string            `json:"request_id"`
	PlanningContextHash string            `json:"planning_context_hash"`
	Classification      string            `json:"classification"`
	Run                 OrchestrationRun  `json:"run"`
	Step                OrchestrationStep `json:"step"`
	ConsumedAt          time.Time         `json:"consumed_at"`
	Replayed            bool              `json:"replayed"`
	SourceInvoked       bool              `json:"source_invoked"`
}

type plannerRecommendationConsumptionRequest struct {
	input              PlannerRecommendationConsumptionInput
	requestPayloadHash string
}

type persistedPlannerRecommendationConsumption struct {
	result             PlannerRecommendationConsumptionResult
	requestPayloadHash string
}

// ConsumePlannerRecommendation revalidates, plans, and invokes one allowlisted source recommendation.
func ConsumePlannerRecommendation(
	ctx context.Context,
	pool *pgxpool.Pool,
	input PlannerRecommendationConsumptionInput,
) (PlannerRecommendationConsumptionResult, error) {
	if pool == nil {
		return PlannerRecommendationConsumptionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := preparePlannerRecommendationConsumption(input)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}

	if replay, ok, err := replayPlannerRecommendationConsumptionIfPresent(ctx, pool, request); err != nil || ok {
		if err != nil || replay.Step.Status != OrchestrationStepStatusPlanned {
			return replay, err
		}
		return resumePlannerRecommendationConsumption(ctx, pool, replay)
	}

	run, err := ResolveOrchestrationRun(ctx, pool, request.input.Recommendation.RunID)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	workspace, err := ResolveWorkspace(ctx, pool, run.WorkspaceID)
	if err != nil {
		kind, isDomainError := KindOf(err)
		if !isDomainError || (kind != ErrorWorkspaceUnavailable && kind != ErrorWorkspaceNotFound) {
			return PlannerRecommendationConsumptionResult{}, err
		}
		planned, planErr := consumePlannerRecommendationPlan(
			ctx,
			pool,
			request,
			OrchestrationInputAuthorityNone,
			"",
		)
		if planErr != nil {
			return PlannerRecommendationConsumptionResult{}, planErr
		}
		return completePlannerRecommendationConsumption(
			ctx,
			pool,
			planned,
			OrchestrationStepOutcomeFailed,
			OrchestrationStopReasonWorkspaceUnavailable,
			false,
		)
	}
	source, found := workspaceSourceByID(workspace.Sources, request.input.Recommendation.SourceBindingID)
	if !found {
		return PlannerRecommendationConsumptionResult{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			request.input.Recommendation.SourceBindingID,
			workspace.ID,
		)
	}
	authorityKind, authorityRef, preflightErr := plannerSourceAuthority(ctx, workspace, source)
	if preflightErr != nil {
		authorityKind = OrchestrationInputAuthorityNone
		authorityRef = ""
	}
	planned, err := consumePlannerRecommendationPlan(ctx, pool, request, authorityKind, authorityRef)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	if planned.Step.Status != OrchestrationStepStatusPlanned {
		return planned, nil
	}
	if preflightErr != nil {
		return completePlannerRecommendationConsumption(
			ctx,
			pool,
			planned,
			OrchestrationStepOutcomeFailed,
			OrchestrationStopReasonSourceFailed,
			false,
		)
	}
	return invokePlannerRecommendationSource(ctx, pool, planned, workspace, source)
}

func preparePlannerRecommendationConsumption(input PlannerRecommendationConsumptionInput) (plannerRecommendationConsumptionRequest, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	recommendation := input.Recommendation
	recommendation.RunID, err = normalizeDetectiveHashID(recommendation.RunID, "detective-run:", "run_id")
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	recommendation.PlanningContextHash, err = normalizeOptionalContentHash(recommendation.PlanningContextHash, "planning_context_hash")
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	if recommendation.PlanningContextHash == "" {
		return plannerRecommendationConsumptionRequest{}, newDomainError(ErrorInvalidInput, "planning_context_hash is required")
	}
	recommendation.Classification, err = normalizeBoundedText(recommendation.Classification, "classification", 100)
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	if recommendation.Classification == PlannerClassificationAbstain {
		return plannerRecommendationConsumptionRequest{}, newDomainError(ErrorPlannerInsufficientCoverage, "abstain recommendations cannot create source steps")
	}
	if recommendation.Classification != PlannerClassificationQuerySource {
		return plannerRecommendationConsumptionRequest{}, newDomainError(ErrorPlannerMalformedOutput, "unsupported planner classification %q", recommendation.Classification)
	}
	recommendation.SourceBindingID, err = normalizeDetectiveHashID(recommendation.SourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	recommendation.CapabilityName, err = normalizeBoundedText(recommendation.CapabilityName, "capability_name", 100)
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	recommendation.CapabilityVersion, err = normalizeBoundedText(recommendation.CapabilityVersion, "capability_version", 100)
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	if _, ok := lookupSourceCapability(recommendation.CapabilityName, recommendation.CapabilityVersion); !ok {
		return plannerRecommendationConsumptionRequest{}, newDomainError(
			ErrorUnsupportedCapability,
			"planner recommendation uses unsupported capability %s/%s",
			recommendation.CapabilityName,
			recommendation.CapabilityVersion,
		)
	}
	recommendation.ReasonCode, err = normalizeBoundedText(recommendation.ReasonCode, "reason_code", 100)
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	if recommendation.ReasonCode != OrchestrationReasonInitialSource && recommendation.ReasonCode != OrchestrationReasonCoverageGap {
		return plannerRecommendationConsumptionRequest{}, newDomainError(ErrorPlannerUngroundedOutput, "planner recommendation uses unsupported source reason %q", recommendation.ReasonCode)
	}

	input = PlannerRecommendationConsumptionInput{RequestID: requestID, Recommendation: recommendation}
	payloadHash, err := orchestrationPayloadHash(recommendation)
	if err != nil {
		return plannerRecommendationConsumptionRequest{}, err
	}
	return plannerRecommendationConsumptionRequest{input: input, requestPayloadHash: payloadHash}, nil
}

func consumePlannerRecommendationPlan(
	ctx context.Context,
	pool *pgxpool.Pool,
	request plannerRecommendationConsumptionRequest,
	authorityKind string,
	authorityRef string,
) (PlannerRecommendationConsumptionResult, error) {
	recommendation := request.input.Recommendation
	planInput, planPayloadHash, err := prepareOrchestrationStepPlan(OrchestrationStepPlanInput{
		RequestID:          request.input.RequestID,
		RunID:              recommendation.RunID,
		SourceBindingID:    recommendation.SourceBindingID,
		ReasonCode:         recommendation.ReasonCode,
		InputAuthorityKind: authorityKind,
		InputAuthorityRef:  authorityRef,
	})
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, fmt.Errorf("beginning planner recommendation consumption: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockOrchestrationTransitionRequest(ctx, tx, request.input.RequestID); err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	if replay, ok, err := replayPlannerRecommendationConsumptionIfPresent(ctx, tx, request); err != nil || ok {
		if err != nil {
			return PlannerRecommendationConsumptionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return PlannerRecommendationConsumptionResult{}, fmt.Errorf("committing planner recommendation consumption replay: %w", err)
		}
		return replay, nil
	}
	if _, ok, err := readOrchestrationTransitionRequest(ctx, tx, request.input.RequestID); err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	} else if ok {
		return PlannerRecommendationConsumptionResult{}, newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists without matching planner recommendation consumption",
			request.input.RequestID,
		)
	}

	run, found, err := readOrchestrationRun(ctx, tx, recommendation.RunID, true)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	if !found {
		return PlannerRecommendationConsumptionResult{}, newDomainError(ErrorOrchestrationRunNotFound, "orchestration run %s was not found", recommendation.RunID)
	}
	if err := validateOrchestrationRunForPlan(run); err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	workspace, found, err := readWorkspace(ctx, tx, run.WorkspaceID, false)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	if !found {
		return PlannerRecommendationConsumptionResult{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", run.WorkspaceID)
	}
	planningContext, err := buildPlannerContext(run, workspace)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	if err := validatePlannerRecommendationForConsumption(recommendation, planningContext); err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}

	planned, err := planOrchestrationStepLocked(ctx, tx, planInput, planPayloadHash, run, workspace)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	result := PlannerRecommendationConsumptionResult{
		RequestID:           request.input.RequestID,
		PlanningContextHash: recommendation.PlanningContextHash,
		Classification:      recommendation.Classification,
		Run:                 planned.Run,
		Step:                planned.Step,
		ConsumedAt:          planned.Step.PlannedAt,
	}
	if err := insertPlannerRecommendationConsumption(ctx, tx, result, request.requestPayloadHash); err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlannerRecommendationConsumptionResult{}, fmt.Errorf("committing planner recommendation consumption: %w", err)
	}
	return result, nil
}

func validatePlannerRecommendationForConsumption(recommendation PlannerRecommendation, context plannerContext) error {
	if recommendation.PlanningContextHash != context.hash {
		return newDomainError(
			ErrorOrchestrationRunConflict,
			"orchestration run %s planning context changed before recommendation consumption",
			recommendation.RunID,
		)
	}
	choice := ""
	for candidate, source := range context.sources {
		if source.ID == recommendation.SourceBindingID {
			choice = candidate
			break
		}
	}
	if choice == "" {
		return newDomainError(ErrorPlannerUngroundedOutput, "planner recommendation references an ineligible source binding")
	}
	validated, err := validatePlannerDecision(recommendation.RunID, context, PlannerDecision{
		Classification: PlannerClassificationQuerySource,
		SourceChoice:   choice,
	})
	if err != nil {
		return err
	}
	if validated != recommendation {
		return newDomainError(ErrorPlannerUngroundedOutput, "planner recommendation does not match controller-derived source authority")
	}
	return nil
}

func insertPlannerRecommendationConsumption(
	ctx context.Context,
	tx pgx.Tx,
	result PlannerRecommendationConsumptionResult,
	payloadHash string,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO detective_planner_recommendation_consumptions (
			request_id, transition_kind, run_id, step_id,
			planning_context_hash, classification, request_payload_hash, consumed_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, result.RequestID, orchestrationTransitionPlanStep, result.Run.ID, result.Step.ID,
		result.PlanningContextHash, result.Classification, payloadHash, result.ConsumedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting planner recommendation consumption: %w", err)
	}
	return nil
}

func replayPlannerRecommendationConsumptionIfPresent(
	ctx context.Context,
	db workspaceQuerier,
	request plannerRecommendationConsumptionRequest,
) (PlannerRecommendationConsumptionResult, bool, error) {
	persisted, ok, err := readPlannerRecommendationConsumption(ctx, db, request.input.RequestID)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, false, err
	}
	if !ok {
		return PlannerRecommendationConsumptionResult{}, false, nil
	}
	if persisted.requestPayloadHash != request.requestPayloadHash {
		return PlannerRecommendationConsumptionResult{}, false, newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists with different planner recommendation consumption payload",
			request.input.RequestID,
		)
	}
	result := persisted.result
	result.Replayed = true
	return result, true, nil
}

func readPlannerRecommendationConsumption(
	ctx context.Context,
	db workspaceQuerier,
	requestID string,
) (persistedPlannerRecommendationConsumption, bool, error) {
	var persisted persistedPlannerRecommendationConsumption
	var runID, stepID string
	err := db.QueryRow(ctx, `
		SELECT
			request_id, run_id, step_id, planning_context_hash,
			classification, request_payload_hash, consumed_at
		FROM detective_planner_recommendation_consumptions
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&runID,
		&stepID,
		&persisted.result.PlanningContextHash,
		&persisted.result.Classification,
		&persisted.requestPayloadHash,
		&persisted.result.ConsumedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedPlannerRecommendationConsumption{}, false, nil
	}
	if err != nil {
		return persistedPlannerRecommendationConsumption{}, false, fmt.Errorf("reading planner recommendation consumption: %w", err)
	}
	persisted.result.ConsumedAt = persisted.result.ConsumedAt.UTC()

	transition, found, err := readOrchestrationTransitionRequest(ctx, db, requestID)
	if err != nil {
		return persistedPlannerRecommendationConsumption{}, false, err
	}
	if !found || transition.Kind != orchestrationTransitionPlanStep || transition.RunID != runID || transition.StepID != stepID {
		return persistedPlannerRecommendationConsumption{}, false, newDomainError(
			ErrorOrchestrationRunConflict,
			"planner recommendation consumption request %s has inconsistent transition authority",
			requestID,
		)
	}
	run, found, err := readOrchestrationRun(ctx, db, runID, false)
	if err != nil {
		return persistedPlannerRecommendationConsumption{}, false, err
	}
	if !found {
		return persistedPlannerRecommendationConsumption{}, false, newDomainError(ErrorOrchestrationRunConflict, "planner recommendation consumption request %s references missing run %s", requestID, runID)
	}
	step, found := orchestrationStepByID(run.Steps, stepID)
	if !found {
		return persistedPlannerRecommendationConsumption{}, false, newDomainError(ErrorOrchestrationRunConflict, "planner recommendation consumption request %s references missing step %s", requestID, stepID)
	}
	_, planPayloadHash, err := prepareOrchestrationStepPlan(OrchestrationStepPlanInput{
		RequestID:          requestID,
		RunID:              runID,
		SourceBindingID:    step.SourceBindingID,
		ReasonCode:         step.ReasonCode,
		InputAuthorityKind: step.InputAuthorityKind,
		InputAuthorityRef:  step.InputAuthorityRef,
	})
	if err != nil {
		return persistedPlannerRecommendationConsumption{}, false, err
	}
	if transition.PayloadHash != planPayloadHash || persisted.result.Classification != PlannerClassificationQuerySource || !persisted.result.ConsumedAt.Equal(step.PlannedAt) {
		return persistedPlannerRecommendationConsumption{}, false, newDomainError(
			ErrorOrchestrationRunConflict,
			"planner recommendation consumption request %s has inconsistent durable plan",
			requestID,
		)
	}
	persisted.result.Run = run
	persisted.result.Step = step
	return persisted, true, nil
}

func hasPlannerRecommendationConsumption(
	ctx context.Context,
	db workspaceQuerier,
	runID string,
) (bool, error) {
	runID, err := normalizeDetectiveHashID(runID, "detective-run:", "run_id")
	if err != nil {
		return false, err
	}
	var found bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM detective_planner_recommendation_consumptions
			WHERE run_id = $1
		)
	`, runID).Scan(&found); err != nil {
		return false, fmt.Errorf("checking planner recommendation consumption: %w", err)
	}
	return found, nil
}

func resumePendingPlannerRecommendationConsumption(
	ctx context.Context,
	pool *pgxpool.Pool,
	runID string,
) (PlannerRecommendationConsumptionResult, bool, error) {
	runID, err := normalizeDetectiveHashID(runID, "detective-run:", "run_id")
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, false, err
	}
	run, err := ResolveOrchestrationRun(ctx, pool, runID)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, false, err
	}
	var planned *OrchestrationStep
	for index := range run.Steps {
		if run.Steps[index].Status != OrchestrationStepStatusPlanned {
			continue
		}
		if planned != nil {
			return PlannerRecommendationConsumptionResult{}, false, newDomainError(
				ErrorOrchestrationRunConflict,
				"orchestration run %s has multiple planned steps",
				runID,
			)
		}
		planned = &run.Steps[index]
	}
	if planned == nil {
		return PlannerRecommendationConsumptionResult{}, false, nil
	}

	var requestID string
	err = pool.QueryRow(ctx, `
		SELECT request_id
		FROM detective_planner_recommendation_consumptions
		WHERE run_id = $1 AND step_id = $2
	`, runID, planned.ID).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlannerRecommendationConsumptionResult{}, false, nil
	}
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, false, fmt.Errorf("finding pending planner recommendation consumption: %w", err)
	}
	persisted, found, err := readPlannerRecommendationConsumption(ctx, pool, requestID)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, false, err
	}
	if !found {
		return PlannerRecommendationConsumptionResult{}, false, newDomainError(
			ErrorOrchestrationRunConflict,
			"planner recommendation consumption %s disappeared during resume",
			requestID,
		)
	}
	persisted.result.Replayed = true
	resumed, err := resumePlannerRecommendationConsumption(ctx, pool, persisted.result)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, false, err
	}
	return resumed, true, nil
}

func plannerSourceAuthority(
	ctx context.Context,
	workspace Workspace,
	source WorkspaceSourceBinding,
) (string, string, error) {
	switch {
	case source.CapabilityName == SourceCapabilityLocalPRDText && source.CapabilityVersion == SourceCapabilityLocalPRDTextVersion:
		manifest, err := collectLocalTextSource(workspace, source)
		if err != nil {
			return "", "", err
		}
		return OrchestrationInputAuthoritySourceToken, manifest.token, nil
	case source.CapabilityName == SourceCapabilityGitGoRepository && source.CapabilityVersion == SourceCapabilityGitGoRepositoryVersion:
		inspection, err := evidenceingestion.InspectGitRepositoryChange(ctx, evidenceingestion.GitRepositoryChangeConfig{
			WorkspaceRoot: workspace.RootPath,
			RepoID:        oneShotGitGoRepositoryID(source),
		})
		if err != nil {
			return "", "", err
		}
		return OrchestrationInputAuthoritySourceToken, inspection.ChangeToken, nil
	default:
		return "", "", newDomainError(
			ErrorUnsupportedCapability,
			"source capability %s/%s has no planner consumption adapter",
			source.CapabilityName,
			source.CapabilityVersion,
		)
	}
}

func resumePlannerRecommendationConsumption(
	ctx context.Context,
	pool *pgxpool.Pool,
	result PlannerRecommendationConsumptionResult,
) (PlannerRecommendationConsumptionResult, error) {
	workspace, err := ResolveWorkspace(ctx, pool, result.Run.WorkspaceID)
	if err != nil {
		kind, isDomainError := KindOf(err)
		if isDomainError && (kind == ErrorWorkspaceUnavailable || kind == ErrorWorkspaceNotFound) {
			return completePlannerRecommendationConsumption(
				ctx,
				pool,
				result,
				OrchestrationStepOutcomeFailed,
				OrchestrationStopReasonWorkspaceUnavailable,
				false,
			)
		}
		return PlannerRecommendationConsumptionResult{}, err
	}
	source, found := workspaceSourceByID(workspace.Sources, result.Step.SourceBindingID)
	if !found {
		return PlannerRecommendationConsumptionResult{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			result.Step.SourceBindingID,
			workspace.ID,
		)
	}
	return invokePlannerRecommendationSource(ctx, pool, result, workspace, source)
}

func invokePlannerRecommendationSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	result PlannerRecommendationConsumptionResult,
	workspace Workspace,
	source WorkspaceSourceBinding,
) (PlannerRecommendationConsumptionResult, error) {
	request := oneShotOrchestrationRequest{
		requestID:       result.RequestID,
		workspaceID:     workspace.ID,
		sourceBindingID: source.ID,
		maxSteps:        result.Run.MaxSteps,
	}
	var oneShot OneShotOrchestrationResult
	var err error
	switch {
	case source.CapabilityName == SourceCapabilityLocalPRDText && source.CapabilityVersion == SourceCapabilityLocalPRDTextVersion:
		oneShot, err = runOneShotLocalText(ctx, pool, request, result.Run, workspace, source, result.Step, true)
	case source.CapabilityName == SourceCapabilityGitGoRepository && source.CapabilityVersion == SourceCapabilityGitGoRepositoryVersion:
		oneShot, err = runOneShotGitGo(ctx, pool, request, result.Run, workspace, source, result.Step, true)
	default:
		return PlannerRecommendationConsumptionResult{}, newDomainError(
			ErrorUnsupportedCapability,
			"source capability %s/%s has no planner consumption adapter",
			source.CapabilityName,
			source.CapabilityVersion,
		)
	}
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	result.Run = oneShot.Run
	if oneShot.Step == nil {
		return PlannerRecommendationConsumptionResult{}, newDomainError(ErrorOrchestrationRunConflict, "planner recommendation source invocation returned no audited step")
	}
	result.Step = *oneShot.Step
	result.SourceInvoked = oneShot.SourceInvoked
	return result, nil
}

func completePlannerRecommendationConsumption(
	ctx context.Context,
	pool *pgxpool.Pool,
	result PlannerRecommendationConsumptionResult,
	outcome string,
	stopReason string,
	sourceInvoked bool,
) (PlannerRecommendationConsumptionResult, error) {
	coverage := OrchestrationCoverage{
		SchemaVersion: OrchestrationCoverageSchemaVersion,
		UnitKind:      orchestrationCoverageUnitKind(result.Step.CapabilityName),
	}
	oneShot, err := completeOneShotStep(
		ctx,
		pool,
		result.RequestID,
		result.Step,
		outcome,
		stopReason,
		coverage,
		sourceInvoked,
	)
	if err != nil {
		return PlannerRecommendationConsumptionResult{}, err
	}
	result.Run = oneShot.Run
	if oneShot.Step == nil {
		return PlannerRecommendationConsumptionResult{}, newDomainError(ErrorOrchestrationRunConflict, "planner recommendation completion returned no audited step")
	}
	result.Step = *oneShot.Step
	result.SourceInvoked = oneShot.SourceInvoked
	return result, nil
}
