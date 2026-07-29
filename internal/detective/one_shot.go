package detective

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	oneShotLocalTextContract         = "detective-local-text-one-shot-v1"
	oneShotLocalTextExtractorName    = "detective-local-text-lines"
	oneShotLocalTextExtractorVersion = "v1"
	oneShotGitGoContract             = "detective-git-go-one-shot-v1"
	oneShotGitGoStabilityWindow      = time.Second
	oneShotGitGoLeaseDuration        = time.Minute

	maxOneShotLocalTextEntries    = 4096
	maxOneShotLocalTextFiles      = 128
	maxOneShotLocalTextFileBytes  = 1 << 20
	maxOneShotLocalTextTotalBytes = 8 << 20
	maxOneShotLocalTextFileLines  = 4096
	maxOneShotLocalTextTotalLines = 16_384
)

// OneShotOrchestrationInput selects one registered source for one bounded run.
type OneShotOrchestrationInput struct {
	RequestID       string `json:"request_id"`
	WorkspaceID     string `json:"workspace_id"`
	SourceBindingID string `json:"source_binding_id"`
	MaxSteps        int    `json:"max_steps"`
}

// OneShotOrchestrationResult reports the durable audit state after one invocation.
type OneShotOrchestrationResult struct {
	RequestID     string             `json:"request_id"`
	Run           OrchestrationRun   `json:"run"`
	Step          *OrchestrationStep `json:"step,omitempty"`
	Replayed      bool               `json:"replayed"`
	SourceInvoked bool               `json:"source_invoked"`
}

type oneShotOrchestrationRequest struct {
	requestID       string
	workspaceID     string
	sourceBindingID string
	maxSteps        int
	scopeHash       string
}

type localTextSourceUnit struct {
	relativePath string
	content      []byte
	contentHash  string
	lineCount    int
}

type localTextSourceManifest struct {
	token string
	units []localTextSourceUnit
}

// RunOneShotOrchestration invokes at most one registered source step.
func RunOneShotOrchestration(ctx context.Context, pool *pgxpool.Pool, input OneShotOrchestrationInput) (OneShotOrchestrationResult, error) {
	if pool == nil {
		return OneShotOrchestrationResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := prepareOneShotOrchestration(input)
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	registered, found, err := readWorkspace(ctx, pool, request.workspaceID, false)
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	if !found {
		return OneShotOrchestrationResult{}, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", request.workspaceID)
	}
	registeredSource, found := workspaceSourceByID(registered.Sources, request.sourceBindingID)
	if !found {
		return OneShotOrchestrationResult{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			request.sourceBindingID,
			request.workspaceID,
		)
	}
	request, err = bindOneShotSourcePolicy(request, registeredSource)
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}

	started, err := StartOrchestrationRun(ctx, pool, OrchestrationRunStartInput{
		RequestID: request.requestID, WorkspaceID: request.workspaceID,
		MaxSteps: request.maxSteps, ScopeHash: request.scopeHash,
	})
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	if started.Run.Status != OrchestrationRunStatusRunning {
		return oneShotResult(request.requestID, started.Run, true, false), nil
	}
	planned, hasPlanned, err := oneShotPlannedStep(started.Run, request.sourceBindingID)
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}

	workspace, err := ResolveWorkspace(ctx, pool, request.workspaceID)
	if err != nil {
		kind, isDomainError := KindOf(err)
		if hasPlanned && isDomainError && (kind == ErrorWorkspaceUnavailable || kind == ErrorWorkspaceNotFound) {
			coverage := OrchestrationCoverage{
				SchemaVersion: OrchestrationCoverageSchemaVersion,
				UnitKind:      orchestrationCoverageUnitKind(planned.CapabilityName),
			}
			return completeOneShotStep(
				ctx, pool, request.requestID, planned,
				OrchestrationStepOutcomeFailed, OrchestrationStopReasonWorkspaceUnavailable,
				coverage, false,
			)
		}
		return OneShotOrchestrationResult{}, err
	}
	source, found := workspaceSourceByID(workspace.Sources, request.sourceBindingID)
	if !found {
		return OneShotOrchestrationResult{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in workspace %s",
			request.sourceBindingID,
			workspace.ID,
		)
	}

	switch {
	case source.CapabilityName == SourceCapabilityLocalPRDText && source.CapabilityVersion == SourceCapabilityLocalPRDTextVersion:
		return runOneShotLocalText(ctx, pool, request, started.Run, workspace, source, planned, hasPlanned)
	case source.CapabilityName == SourceCapabilityGitGoRepository && source.CapabilityVersion == SourceCapabilityGitGoRepositoryVersion:
		return runOneShotGitGo(ctx, pool, request, started.Run, workspace, source, planned, hasPlanned)
	default:
		return OneShotOrchestrationResult{}, newDomainError(
			ErrorUnsupportedCapability,
			"source capability %s/%s has no one-shot adapter",
			source.CapabilityName,
			source.CapabilityVersion,
		)
	}
}

func runOneShotLocalText(
	ctx context.Context,
	pool *pgxpool.Pool,
	request oneShotOrchestrationRequest,
	run OrchestrationRun,
	workspace Workspace,
	source WorkspaceSourceBinding,
	planned OrchestrationStep,
	hasPlanned bool,
) (OneShotOrchestrationResult, error) {
	manifest, manifestErr := collectLocalTextSource(workspace, source)
	if manifestErr != nil {
		if !hasPlanned {
			var err error
			planned, err = planOneShotStep(ctx, pool, request, run, source, OrchestrationInputAuthorityNone, "")
			if err != nil {
				return OneShotOrchestrationResult{}, err
			}
		}
		coverage := OrchestrationCoverage{
			SchemaVersion: OrchestrationCoverageSchemaVersion,
			UnitKind:      OrchestrationCoverageUnitTextFiles,
		}
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, false,
		)
	}

	if !hasPlanned {
		var err error
		planned, err = planOneShotStep(
			ctx, pool, request, run, source,
			OrchestrationInputAuthoritySourceToken, manifest.token,
		)
		if err != nil {
			return OneShotOrchestrationResult{}, err
		}
	} else if planned.InputAuthorityKind != OrchestrationInputAuthoritySourceToken || planned.InputAuthorityRef != manifest.token {
		coverage := OrchestrationCoverage{
			SchemaVersion: OrchestrationCoverageSchemaVersion,
			UnitKind:      OrchestrationCoverageUnitTextFiles,
		}
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, false,
		)
	}

	if len(manifest.units) == 0 {
		coverage := OrchestrationCoverage{
			SchemaVersion: OrchestrationCoverageSchemaVersion,
			UnitKind:      OrchestrationCoverageUnitTextFiles,
		}
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeStopped, OrchestrationStopReasonNoAllowlistedSource,
			coverage, false,
		)
	}

	covered, invokeErr := invokeLocalTextSource(ctx, pool, workspace, source, manifest.units)
	coverage := OrchestrationCoverage{
		SchemaVersion:      OrchestrationCoverageSchemaVersion,
		UnitKind:           OrchestrationCoverageUnitTextFiles,
		AttemptedUnitCount: len(manifest.units),
		CoveredUnitCount:   covered,
		Complete:           invokeErr == nil,
	}
	if invokeErr != nil {
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, true,
		)
	}
	return completeOneShotStep(
		ctx, pool, request.requestID, planned,
		OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied,
		coverage, true,
	)
}

func runOneShotGitGo(
	ctx context.Context,
	pool *pgxpool.Pool,
	request oneShotOrchestrationRequest,
	run OrchestrationRun,
	workspace Workspace,
	source WorkspaceSourceBinding,
	planned OrchestrationStep,
	hasPlanned bool,
) (OneShotOrchestrationResult, error) {
	coverage := OrchestrationCoverage{
		SchemaVersion: OrchestrationCoverageSchemaVersion,
		UnitKind:      OrchestrationCoverageUnitRepositoryFiles,
	}
	inspection, inspectionErr := evidenceingestion.InspectGitRepositoryChange(ctx, evidenceingestion.GitRepositoryChangeConfig{
		WorkspaceRoot: workspace.RootPath,
		RepoID:        oneShotGitGoRepositoryID(source),
	})
	if inspectionErr != nil {
		if !hasPlanned {
			var err error
			planned, err = planOneShotStep(ctx, pool, request, run, source, OrchestrationInputAuthorityNone, "")
			if err != nil {
				return OneShotOrchestrationResult{}, err
			}
		}
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, false,
		)
	}

	if !hasPlanned {
		var err error
		planned, err = planOneShotStep(
			ctx, pool, request, run, source,
			OrchestrationInputAuthoritySourceToken, inspection.ChangeToken,
		)
		if err != nil {
			return OneShotOrchestrationResult{}, err
		}
	} else if planned.InputAuthorityKind != OrchestrationInputAuthoritySourceToken || planned.InputAuthorityRef != inspection.ChangeToken {
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, false,
		)
	}

	observationRequestID := oneShotChildRequestID(request.requestID, "git-observe", planned.Number)
	observation, err := evidenceingestion.ObserveGitRepositoryChange(ctx, pool, evidenceingestion.GitRepositoryChangeObservationConfig{
		WorkspaceRoot:   workspace.RootPath,
		RepoID:          oneShotGitGoRepositoryID(source),
		RequestID:       observationRequestID,
		StabilityWindow: oneShotGitGoStabilityWindow,
	})
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	if observation.Inspection.ChangeToken != planned.InputAuthorityRef {
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, true,
		)
	}
	if observation.Inspection.Dirty || !observation.Stable {
		return completeWaitingOneShotGitStep(ctx, pool, request.requestID, run, planned, coverage)
	}

	scheduled, err := evidenceingestion.ScheduleRepositoryExtractionWork(ctx, pool, evidenceingestion.RepositoryExtractionWorkScheduleInput{
		RequestID:            oneShotChildRequestID(request.requestID, "git-schedule", planned.Number),
		ObservationRequestID: observationRequestID,
		ExtractorName:        evidenceingestion.ExtractorRepositoryGoParserCodeFact,
	})
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	workerTick, resolution, err := runOneShotGitWorkerTick(ctx, pool, request, workspace, source, planned, scheduled.Work)
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	switch resolution.State {
	case evidenceingestion.RepositoryExtractionWorkStatePending,
		evidenceingestion.RepositoryExtractionWorkStateRunning:
		return completeWaitingOneShotGitStep(ctx, pool, request.requestID, run, planned, coverage)
	case evidenceingestion.RepositoryExtractionWorkStateFailed,
		evidenceingestion.RepositoryExtractionWorkStateSuperseded:
		return completeOneShotStep(
			ctx, pool, request.requestID, planned,
			OrchestrationStepOutcomeFailed, OrchestrationStopReasonSourceFailed,
			coverage, true,
		)
	case evidenceingestion.RepositoryExtractionWorkStateSucceeded:
	default:
		return OneShotOrchestrationResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"repository work %s has unsupported resolved state %s",
			scheduled.Work.WorkItemID,
			resolution.State,
		)
	}
	if resolution.SourceGeneration == nil || resolution.RepositorySnapshot == nil ||
		resolution.Work.WorkItemID != scheduled.Work.WorkItemID ||
		resolution.Work.ChangeToken != observation.Inspection.ChangeToken ||
		resolution.SourceGeneration.ID == "" ||
		resolution.RepositorySnapshot.ID != resolution.SourceGeneration.RepositorySnapshotID {
		return OneShotOrchestrationResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"repository work %s returned inconsistent successful authority",
			scheduled.Work.WorkItemID,
		)
	}
	if workerTick.Claim.Claimed && workerTick.Execution == nil {
		return OneShotOrchestrationResult{}, newDomainError(
			ErrorOrchestrationRunConflict,
			"repository worker tick %s claimed work without a successful execution",
			workerTick.RequestID,
		)
	}

	selectedFileCount := resolution.RepositorySnapshot.SelectedFileCount
	coverage.AttemptedUnitCount = selectedFileCount
	coverage.CoveredUnitCount = selectedFileCount
	coverage.Complete = true
	return completeOneShotStep(
		ctx, pool, request.requestID, planned,
		OrchestrationStepOutcomeCompleted, OrchestrationStopReasonCoverageSatisfied,
		coverage, true,
	)
}

func runOneShotGitWorkerTick(
	ctx context.Context,
	pool *pgxpool.Pool,
	request oneShotOrchestrationRequest,
	workspace Workspace,
	source WorkspaceSourceBinding,
	planned OrchestrationStep,
	work evidenceingestion.RepositoryExtractionWork,
) (evidenceingestion.RepositoryExtractionWorkerTickResult, evidenceingestion.RepositoryExtractionWorkResolution, error) {
	resolution, err := evidenceingestion.ResolveRepositoryExtractionWork(ctx, pool, work.WorkItemID)
	if err != nil {
		return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, err
	}
	if resolution.State == evidenceingestion.RepositoryExtractionWorkStateFailed ||
		resolution.State == evidenceingestion.RepositoryExtractionWorkStateSuperseded {
		return evidenceingestion.RepositoryExtractionWorkerTickResult{}, resolution, nil
	}
	attemptNumber, err := oneShotGitWorkerAttemptNumber(resolution)
	if err != nil {
		return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, err
	}

	// One retry closes the race where maintenance requeues an expired claim
	// between resolution and the idempotent worker-tick replay.
	for invocation := 0; invocation < 2; invocation++ {
		workerTick, tickErr := evidenceingestion.RunRepositoryExtractionWorkerTick(ctx, pool, evidenceingestion.RepositoryExtractionWorkerTickInput{
			RequestID:                 oneShotGitWorkerRequestID(request.requestID, planned.Number, attemptNumber),
			WorkspaceRoot:             workspace.RootPath,
			RepoID:                    oneShotGitGoRepositoryID(source),
			ExtractorName:             evidenceingestion.ExtractorRepositoryGoParserCodeFact,
			WorkerID:                  oneShotGitGoWorkerID(source.ID),
			LeaseDurationMilliseconds: oneShotGitGoLeaseDuration.Milliseconds(),
		})
		latest, resolveErr := evidenceingestion.ResolveRepositoryExtractionWork(ctx, pool, work.WorkItemID)
		if resolveErr != nil {
			return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, resolveErr
		}
		if tickErr == nil {
			if !workerTick.Claim.Claimed && invocation == 0 {
				nextAttemptNumber, attemptErr := oneShotGitWorkerAttemptNumber(latest)
				if attemptErr != nil {
					return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, attemptErr
				}
				if latest.State == evidenceingestion.RepositoryExtractionWorkStatePending && nextAttemptNumber > attemptNumber {
					attemptNumber = nextAttemptNumber
					continue
				}
			}
			return workerTick, latest, nil
		}
		kind, isDomainError := evidenceingestion.KindOf(tickErr)
		if invocation == 0 && isDomainError &&
			(kind == evidenceingestion.ErrorRepositoryWorkConflict || kind == evidenceingestion.ErrorRepositoryWorkLeaseExpired) {
			if latest.State == evidenceingestion.RepositoryExtractionWorkStateFailed ||
				latest.State == evidenceingestion.RepositoryExtractionWorkStateSuperseded ||
				latest.State == evidenceingestion.RepositoryExtractionWorkStateSucceeded {
				return evidenceingestion.RepositoryExtractionWorkerTickResult{}, latest, nil
			}
			nextAttemptNumber, attemptErr := oneShotGitWorkerAttemptNumber(latest)
			if attemptErr != nil {
				return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, attemptErr
			}
			if latest.State == evidenceingestion.RepositoryExtractionWorkStatePending && nextAttemptNumber > attemptNumber {
				attemptNumber = nextAttemptNumber
				continue
			}
		}
		return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, tickErr
	}
	return evidenceingestion.RepositoryExtractionWorkerTickResult{}, evidenceingestion.RepositoryExtractionWorkResolution{}, newDomainError(
		ErrorOrchestrationRunConflict,
		"repository work %s exceeded one bounded attempt handoff",
		work.WorkItemID,
	)
}

func oneShotGitWorkerAttemptNumber(resolution evidenceingestion.RepositoryExtractionWorkResolution) (int, error) {
	if resolution.LatestAttemptNumber < 0 {
		return 0, newDomainError(ErrorOrchestrationRunConflict, "repository work %s has a negative latest attempt number", resolution.Work.WorkItemID)
	}
	attemptNumber := resolution.LatestAttemptNumber
	switch resolution.State {
	case evidenceingestion.RepositoryExtractionWorkStatePending:
		attemptNumber++
	case evidenceingestion.RepositoryExtractionWorkStateRunning,
		evidenceingestion.RepositoryExtractionWorkStateSucceeded,
		evidenceingestion.RepositoryExtractionWorkStateFailed,
		evidenceingestion.RepositoryExtractionWorkStateSuperseded:
	default:
		return 0, newDomainError(
			ErrorOrchestrationRunConflict,
			"repository work %s has unsupported resolved state %s",
			resolution.Work.WorkItemID,
			resolution.State,
		)
	}
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	return attemptNumber, nil
}

func completeWaitingOneShotGitStep(
	ctx context.Context,
	pool *pgxpool.Pool,
	requestID string,
	run OrchestrationRun,
	step OrchestrationStep,
	coverage OrchestrationCoverage,
) (OneShotOrchestrationResult, error) {
	if step.Number < run.MaxSteps {
		return completeOneShotStep(
			ctx, pool, requestID, step,
			OrchestrationStepOutcomeContinue, "", coverage, true,
		)
	}
	return completeOneShotStep(
		ctx, pool, requestID, step,
		OrchestrationStepOutcomeStopped, OrchestrationStopReasonBudgetExhausted,
		coverage, true,
	)
}

func prepareOneShotOrchestration(input OneShotOrchestrationInput) (oneShotOrchestrationRequest, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return oneShotOrchestrationRequest{}, err
	}
	workspaceID, err := normalizeWorkspaceID(input.WorkspaceID)
	if err != nil {
		return oneShotOrchestrationRequest{}, err
	}
	sourceBindingID, err := normalizeDetectiveHashID(input.SourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return oneShotOrchestrationRequest{}, err
	}
	if input.MaxSteps < 1 || input.MaxSteps > maxOrchestrationSteps {
		return oneShotOrchestrationRequest{}, newDomainError(ErrorInvalidInput, "max_steps must be between 1 and %d", maxOrchestrationSteps)
	}
	scopeHash, err := oneShotLocalTextScopeHash(workspaceID, sourceBindingID, input.MaxSteps)
	if err != nil {
		return oneShotOrchestrationRequest{}, err
	}
	return oneShotOrchestrationRequest{
		requestID: requestID, workspaceID: workspaceID,
		sourceBindingID: sourceBindingID, maxSteps: input.MaxSteps, scopeHash: scopeHash,
	}, nil
}

func bindOneShotSourcePolicy(request oneShotOrchestrationRequest, source WorkspaceSourceBinding) (oneShotOrchestrationRequest, error) {
	if source.ID != request.sourceBindingID || source.WorkspaceID != request.workspaceID {
		return oneShotOrchestrationRequest{}, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s does not belong to workspace %s",
			request.sourceBindingID,
			request.workspaceID,
		)
	}

	var (
		scopeHash string
		err       error
	)
	switch {
	case source.CapabilityName == SourceCapabilityLocalPRDText && source.CapabilityVersion == SourceCapabilityLocalPRDTextVersion:
		scopeHash, err = oneShotLocalTextScopeHash(request.workspaceID, request.sourceBindingID, request.maxSteps)
	case source.CapabilityName == SourceCapabilityGitGoRepository && source.CapabilityVersion == SourceCapabilityGitGoRepositoryVersion:
		scopeHash, err = orchestrationPayloadHash(struct {
			Contract                    string `json:"contract"`
			WorkspaceID                 string `json:"workspace_id"`
			SourceBindingID             string `json:"source_binding_id"`
			MaxSteps                    int    `json:"max_steps"`
			StabilityWindowMilliseconds int64  `json:"stability_window_milliseconds"`
			ExtractorName               string `json:"extractor_name"`
			LeaseDurationMilliseconds   int64  `json:"lease_duration_milliseconds"`
		}{
			Contract: oneShotGitGoContract, WorkspaceID: request.workspaceID,
			SourceBindingID: request.sourceBindingID, MaxSteps: request.maxSteps,
			StabilityWindowMilliseconds: oneShotGitGoStabilityWindow.Milliseconds(),
			ExtractorName:               evidenceingestion.ExtractorRepositoryGoParserCodeFact,
			LeaseDurationMilliseconds:   oneShotGitGoLeaseDuration.Milliseconds(),
		})
	default:
		return oneShotOrchestrationRequest{}, newDomainError(
			ErrorUnsupportedCapability,
			"source capability %s/%s has no one-shot policy",
			source.CapabilityName,
			source.CapabilityVersion,
		)
	}
	if err != nil {
		return oneShotOrchestrationRequest{}, err
	}
	request.scopeHash = scopeHash
	return request, nil
}

func oneShotLocalTextScopeHash(workspaceID, sourceBindingID string, maxSteps int) (string, error) {
	return orchestrationPayloadHash(struct {
		Contract        string `json:"contract"`
		WorkspaceID     string `json:"workspace_id"`
		SourceBindingID string `json:"source_binding_id"`
		MaxSteps        int    `json:"max_steps"`
	}{
		Contract: oneShotLocalTextContract, WorkspaceID: workspaceID,
		SourceBindingID: sourceBindingID, MaxSteps: maxSteps,
	})
}

func oneShotPlannedStep(run OrchestrationRun, sourceBindingID string) (OrchestrationStep, bool, error) {
	var planned OrchestrationStep
	found := false
	for _, step := range run.Steps {
		if step.SourceBindingID != sourceBindingID {
			return OrchestrationStep{}, false, newDomainError(
				ErrorOrchestrationRunConflict,
				"orchestration run %s contains source binding %s outside one-shot scope %s",
				run.ID, step.SourceBindingID, sourceBindingID,
			)
		}
		if step.Status != OrchestrationStepStatusPlanned {
			continue
		}
		if found {
			return OrchestrationStep{}, false, newDomainError(ErrorOrchestrationRunConflict, "orchestration run %s has multiple planned steps", run.ID)
		}
		planned = step
		found = true
	}
	return planned, found, nil
}

func planOneShotStep(
	ctx context.Context,
	pool *pgxpool.Pool,
	request oneShotOrchestrationRequest,
	run OrchestrationRun,
	source WorkspaceSourceBinding,
	authorityKind string,
	authorityRef string,
) (OrchestrationStep, error) {
	stepNumber := run.StepCount + 1
	reason := OrchestrationReasonInitialSource
	if stepNumber > 1 {
		reason = OrchestrationReasonRefresh
	}
	planned, err := PlanOrchestrationStep(ctx, pool, OrchestrationStepPlanInput{
		RequestID:          oneShotChildRequestID(request.requestID, "plan", stepNumber),
		RunID:              run.ID,
		SourceBindingID:    source.ID,
		ReasonCode:         reason,
		InputAuthorityKind: authorityKind,
		InputAuthorityRef:  authorityRef,
	})
	if err != nil {
		return OrchestrationStep{}, err
	}
	return planned.Step, nil
}

func completeOneShotStep(
	ctx context.Context,
	pool *pgxpool.Pool,
	requestID string,
	step OrchestrationStep,
	outcome string,
	stopReason string,
	coverage OrchestrationCoverage,
	sourceInvoked bool,
) (OneShotOrchestrationResult, error) {
	completed, err := CompleteOrchestrationStep(ctx, pool, OrchestrationStepCompleteInput{
		RequestID: oneShotChildRequestID(requestID, "complete", step.Number),
		RunID:     step.RunID, StepID: step.ID, Outcome: outcome,
		StopReason: stopReason, Coverage: coverage,
	})
	if err != nil {
		return OneShotOrchestrationResult{}, err
	}
	return oneShotResult(requestID, completed.Run, false, sourceInvoked), nil
}

func oneShotResult(requestID string, run OrchestrationRun, replayed, sourceInvoked bool) OneShotOrchestrationResult {
	result := OneShotOrchestrationResult{
		RequestID: requestID, Run: run, Replayed: replayed, SourceInvoked: sourceInvoked,
	}
	if len(run.Steps) > 0 {
		step := run.Steps[len(run.Steps)-1]
		result.Step = &step
	}
	return result
}

func collectLocalTextSource(workspace Workspace, source WorkspaceSourceBinding) (localTextSourceManifest, error) {
	sourcePath := filepath.Join(workspace.RootPath, filepath.FromSlash(source.RelativePath))
	paths := make([]string, 0, maxOneShotLocalTextFiles)
	if source.PathKind == "file" {
		paths = append(paths, sourcePath)
	} else {
		entries := 0
		if err := collectLocalTextDirectoryPaths(sourcePath, &entries, &paths); err != nil {
			return localTextSourceManifest{}, err
		}
	}
	sort.Strings(paths)

	units := make([]localTextSourceUnit, 0, len(paths))
	totalBytes := 0
	totalLines := 0
	for _, path := range paths {
		content, err := readBoundedLocalTextFile(path)
		if err != nil {
			return localTextSourceManifest{}, err
		}
		totalBytes += len(content)
		if totalBytes > maxOneShotLocalTextTotalBytes {
			return localTextSourceManifest{}, fmt.Errorf("local text source exceeds %d total bytes", maxOneShotLocalTextTotalBytes)
		}
		lineCount := nonEmptyLocalTextLineCount(content)
		if lineCount > maxOneShotLocalTextFileLines {
			return localTextSourceManifest{}, fmt.Errorf("local text path %q contains more than %d non-empty lines", path, maxOneShotLocalTextFileLines)
		}
		totalLines += lineCount
		if totalLines > maxOneShotLocalTextTotalLines {
			return localTextSourceManifest{}, fmt.Errorf("local text source contains more than %d non-empty lines", maxOneShotLocalTextTotalLines)
		}
		relativePath, err := filepath.Rel(workspace.RootPath, path)
		if err != nil {
			return localTextSourceManifest{}, fmt.Errorf("relating local text path to workspace: %w", err)
		}
		if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return localTextSourceManifest{}, fmt.Errorf("local text path %q escaped workspace root", path)
		}
		units = append(units, localTextSourceUnit{
			relativePath: filepath.ToSlash(relativePath),
			content:      content,
			contentHash:  contentHash(content),
			lineCount:    lineCount,
		})
	}
	token, err := localTextManifestToken(source.ID, units)
	if err != nil {
		return localTextSourceManifest{}, err
	}
	return localTextSourceManifest{token: token, units: units}, nil
}

func collectLocalTextDirectoryPaths(directory string, entries *int, paths *[]string) error {
	remaining := maxOneShotLocalTextEntries - *entries
	if remaining <= 0 {
		return fmt.Errorf("local text source contains more than %d entries", maxOneShotLocalTextEntries)
	}
	directoryEntries, err := readLocalTextDirectoryEntries(directory, remaining+1)
	if err != nil {
		return err
	}
	if len(directoryEntries) > remaining {
		return fmt.Errorf("local text source contains more than %d entries", maxOneShotLocalTextEntries)
	}
	*entries += len(directoryEntries)
	sort.Slice(directoryEntries, func(i, j int) bool {
		return directoryEntries[i].Name() < directoryEntries[j].Name()
	})
	for _, entry := range directoryEntries {
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			if entry.Name() == ".git" {
				continue
			}
			if err := collectLocalTextDirectoryPaths(path, entries, paths); err != nil {
				return err
			}
			continue
		}
		if !allowlistedLocalTextPath(path) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("local text path %q has unsupported file type", path)
		}
		if len(*paths) >= maxOneShotLocalTextFiles {
			return fmt.Errorf("local text source contains more than %d allowlisted files", maxOneShotLocalTextFiles)
		}
		*paths = append(*paths, path)
	}
	return nil
}

func readLocalTextDirectoryEntries(path string, limit int) ([]fs.DirEntry, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening local text directory: %w", err)
	}
	entries := make([]fs.DirEntry, 0, limit)
	var readErr error
	for len(entries) < limit {
		var batch []fs.DirEntry
		batch, readErr = directory.ReadDir(limit - len(entries))
		entries = append(entries, batch...)
		if readErr != nil || len(batch) == 0 {
			break
		}
	}
	closeErr := directory.Close()
	if readErr != nil && readErr != io.EOF {
		return nil, fmt.Errorf("reading local text directory: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing local text directory: %w", closeErr)
	}
	return entries, nil
}

func readBoundedLocalTextFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("reading local text path metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("local text path %q is not a regular file", path)
	}
	if info.Size() > maxOneShotLocalTextFileBytes {
		return nil, fmt.Errorf("local text path %q exceeds %d bytes", path, maxOneShotLocalTextFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening local text path: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxOneShotLocalTextFileBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("reading local text path: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing local text path: %w", closeErr)
	}
	if len(content) > maxOneShotLocalTextFileBytes {
		return nil, fmt.Errorf("local text path %q exceeds %d bytes", path, maxOneShotLocalTextFileBytes)
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("local text path %q is not valid UTF-8", path)
	}
	return content, nil
}

func allowlistedLocalTextPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt":
		return true
	default:
		return false
	}
}

func nonEmptyLocalTextLineCount(content []byte) int {
	count := 0
	lineStart := 0
	for lineStart <= len(content) {
		newline := bytes.IndexByte(content[lineStart:], '\n')
		lineEnd := len(content)
		nextLineStart := len(content) + 1
		if newline >= 0 {
			lineEnd = lineStart + newline
			nextLineStart = lineEnd + 1
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && content[contentEnd-1] == '\r' {
			contentEnd--
		}
		if contentEnd > lineStart {
			count++
		}
		if newline < 0 {
			break
		}
		lineStart = nextLineStart
	}
	return count
}

func localTextManifestToken(sourceBindingID string, units []localTextSourceUnit) (string, error) {
	type manifestUnit struct {
		RelativePath      string `json:"relative_path"`
		ContentHash       string `json:"content_hash"`
		ByteLength        int    `json:"byte_length"`
		NonEmptyLineCount int    `json:"non_empty_line_count"`
	}
	manifestUnits := make([]manifestUnit, 0, len(units))
	for _, unit := range units {
		manifestUnits = append(manifestUnits, manifestUnit{
			RelativePath:      unit.relativePath,
			ContentHash:       unit.contentHash,
			ByteLength:        len(unit.content),
			NonEmptyLineCount: unit.lineCount,
		})
	}
	return orchestrationPayloadHash(struct {
		Contract        string         `json:"contract"`
		SourceBindingID string         `json:"source_binding_id"`
		Units           []manifestUnit `json:"units"`
	}{Contract: oneShotLocalTextContract, SourceBindingID: sourceBindingID, Units: manifestUnits})
}

func invokeLocalTextSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	workspace Workspace,
	source WorkspaceSourceBinding,
	units []localTextSourceUnit,
) (int, error) {
	covered := 0
	for _, unit := range units {
		intake, err := evidenceingestion.CaptureManualSource(ctx, pool, evidenceingestion.ManualTextInput{
			SourceSystem:  evidenceingestion.SourceSystemManualText,
			SourceID:      localTextSourceID(source.ID, unit.relativePath),
			SourceVersion: unit.contentHash,
			Raw:           unit.content,
			OriginMetadata: map[string]string{
				"contract":             oneShotLocalTextContract,
				"workspace_id":         workspace.ID,
				"source_binding_id":    source.ID,
				"registered_source_id": source.SourceID,
				"relative_path":        unit.relativePath,
			},
			RequestID: localTextOperationRequestID(source.ID, "capture", unit),
		})
		if err != nil {
			return covered, err
		}
		if len(intake.Spans) != unit.lineCount {
			return covered, fmt.Errorf("local text span count %d does not match planned line count %d", len(intake.Spans), unit.lineCount)
		}
		if len(intake.Spans) > 0 {
			proposals := make([]evidenceingestion.ExtractorProposalOutput, 0, len(intake.Spans))
			for _, span := range intake.Spans {
				proposals = append(proposals, evidenceingestion.ExtractorProposalOutput{
					ProposalLocalID: fmt.Sprintf("line:%d", span.DisplayLine),
					StatementText:   span.QuotedText,
					EvidenceRefs:    []string{span.SpanID},
				})
			}
			_, err = evidenceingestion.SubmitExtractorOutput(ctx, pool, evidenceingestion.ExtractorOutputInput{
				RequestID:        localTextOperationRequestID(source.ID, "submit", unit),
				SourceSnapshotID: intake.SourceSnapshotID,
				ExtractionViewID: intake.ExtractionViewID,
				ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{
					Name: oneShotLocalTextExtractorName, Version: oneShotLocalTextExtractorVersion,
					Config: map[string]string{
						"proposal_contract": "exact-non-empty-lines-v1",
						"span_catalog":      evidenceingestion.SpanCatalogManualLineV1,
					},
				},
				Output: evidenceingestion.FrozenExtractorOutput{Proposals: proposals},
			})
			if err != nil {
				return covered, err
			}
		}
		covered++
	}
	return covered, nil
}

func localTextSourceID(sourceBindingID, relativePath string) string {
	return "detective-text:" + hashHex([]byte(detectiveIDVersion+"\x00local-text-source\x00"+sourceBindingID+"\x00"+relativePath))
}

func localTextOperationRequestID(sourceBindingID, operation string, unit localTextSourceUnit) string {
	material := fmt.Sprintf(
		"%s\x00%s\x00local-text-operation\x00%s\x00%s\x00%s\x00%s",
		detectiveIDVersion, oneShotLocalTextContract, sourceBindingID,
		operation, unit.relativePath, unit.contentHash,
	)
	return "detective-local-text:" + hashHex([]byte(material))
}

func oneShotGitGoWorkerID(sourceBindingID string) string {
	material := fmt.Sprintf(
		"%s\x00%s\x00git-go-worker\x00%s",
		detectiveIDVersion, oneShotGitGoContract, sourceBindingID,
	)
	return "detective-git-go:" + hashHex([]byte(material))
}

func oneShotGitWorkerRequestID(requestID string, stepNumber, attemptNumber int) string {
	if attemptNumber <= 1 {
		return oneShotChildRequestID(requestID, "git-worker", stepNumber)
	}
	return oneShotChildRequestID(requestID, fmt.Sprintf("git-worker-attempt-%d", attemptNumber), stepNumber)
}

func oneShotGitGoRepositoryID(source WorkspaceSourceBinding) string {
	return source.ID
}

func oneShotChildRequestID(requestID, operation string, stepNumber int) string {
	material := fmt.Sprintf(
		"%s\x00one-shot-request\x00%s\x00%s\x00%d",
		detectiveIDVersion, requestID, operation, stepNumber,
	)
	return "detective-one-shot:" + hashHex([]byte(material))
}
