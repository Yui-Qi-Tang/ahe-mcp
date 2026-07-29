package detective

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	minPeriodicRuntimeInterval = time.Millisecond
	maxPeriodicRuntimeInterval = 24 * time.Hour
)

// PeriodicRuntimeConfig selects one registered workspace for periodic source wakeups.
type PeriodicRuntimeConfig struct {
	WorkspaceID      string        `json:"workspace_id"`
	MaxSteps         int           `json:"max_steps"`
	Interval         time.Duration `json:"interval"`
	SourceBindingIDs []string      `json:"source_binding_ids,omitempty"`
}

// PeriodicRuntimeOutcome is an informational result from one coalesced wakeup.
// Durable cycle and cursor state remain authoritative when this bounded channel drops an outcome.
type PeriodicRuntimeOutcome struct {
	SourceBindingID string
	Result          PeriodicSourceTickResult
	Err             error
}

type periodicSourceTickFunc func(context.Context, *pgxpool.Pool, PeriodicSourceTickInput) (PeriodicSourceTickResult, error)

type periodicRuntimeLane struct {
	sourceBindingID string
	wakeups         chan struct{}
	pending         atomic.Bool
}

// PeriodicRuntime owns bounded in-process wakeup lanes for one immutable workspace registry.
type PeriodicRuntime struct {
	ctx         context.Context
	cancel      context.CancelFunc
	pool        *pgxpool.Pool
	workspaceID string
	maxSteps    int
	interval    time.Duration
	invoke      periodicSourceTickFunc
	lanes       map[string]*periodicRuntimeLane
	sourceOrder []string
	slots       chan struct{}
	outcomes    chan PeriodicRuntimeOutcome
	wg          sync.WaitGroup
	closeOnce   sync.Once
}

// NewPeriodicRuntime starts one initial recovery wakeup followed by periodic source wakeups.
func NewPeriodicRuntime(ctx context.Context, pool *pgxpool.Pool, config PeriodicRuntimeConfig) (*PeriodicRuntime, error) {
	return newRegisteredPeriodicRuntime(ctx, pool, config, RunPeriodicSourceTick)
}

// NewPlannerPeriodicRuntime starts the existing durable per-source runtime with
// one bounded planner continuation after incomplete persisted coverage.
func NewPlannerPeriodicRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config PeriodicRuntimeConfig,
	runner PlannerRunner,
) (*PeriodicRuntime, error) {
	if runner == nil {
		return nil, newDomainError(ErrorInvalidInput, "planner runner is required")
	}
	return newRegisteredPeriodicRuntime(
		ctx,
		pool,
		config,
		func(ctx context.Context, pool *pgxpool.Pool, input PeriodicSourceTickInput) (PeriodicSourceTickResult, error) {
			return RunPlannerPeriodicSourceTick(ctx, pool, input, runner)
		},
	)
}

func newRegisteredPeriodicRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config PeriodicRuntimeConfig,
	invoke periodicSourceTickFunc,
) (*PeriodicRuntime, error) {
	if ctx == nil {
		return nil, errors.New("periodic runtime context is required")
	}
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := preparePeriodicRuntimeConfig(config)
	if err != nil {
		return nil, err
	}
	workspace, found, err := readWorkspace(ctx, pool, prepared.WorkspaceID, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, newDomainError(ErrorWorkspaceNotFound, "workspace %s is not registered", prepared.WorkspaceID)
	}
	sources := workspace.Sources
	if len(prepared.SourceBindingIDs) != 0 {
		selected := make([]WorkspaceSourceBinding, 0, len(prepared.SourceBindingIDs))
		for _, sourceBindingID := range prepared.SourceBindingIDs {
			source, found := workspaceSourceByID(workspace.Sources, sourceBindingID)
			if !found {
				return nil, newDomainError(
					ErrorSourceBindingNotFound,
					"source binding %s is not registered in periodic workspace %s",
					sourceBindingID,
					workspace.ID,
				)
			}
			selected = append(selected, source)
		}
		sources = selected
	}
	return newPeriodicRuntime(
		ctx,
		pool,
		prepared,
		sources,
		runtime.NumCPU(),
		invoke,
		true,
	)
}

func preparePeriodicRuntimeConfig(config PeriodicRuntimeConfig) (PeriodicRuntimeConfig, error) {
	workspaceID, err := normalizeWorkspaceID(config.WorkspaceID)
	if err != nil {
		return PeriodicRuntimeConfig{}, err
	}
	if config.MaxSteps < 1 || config.MaxSteps > maxOrchestrationSteps {
		return PeriodicRuntimeConfig{}, newDomainError(ErrorInvalidInput, "max_steps must be between 1 and %d", maxOrchestrationSteps)
	}
	if config.Interval < minPeriodicRuntimeInterval || config.Interval > maxPeriodicRuntimeInterval {
		return PeriodicRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"interval must be between %s and %s",
			minPeriodicRuntimeInterval,
			maxPeriodicRuntimeInterval,
		)
	}
	sourceBindingIDs := make([]string, len(config.SourceBindingIDs))
	seen := make(map[string]struct{}, len(config.SourceBindingIDs))
	for index, sourceBindingID := range config.SourceBindingIDs {
		sourceBindingID, err = normalizeDetectiveHashID(sourceBindingID, "workspace-source:", "source_binding_id")
		if err != nil {
			return PeriodicRuntimeConfig{}, fmt.Errorf("preparing periodic source binding %d: %w", index, err)
		}
		if _, exists := seen[sourceBindingID]; exists {
			return PeriodicRuntimeConfig{}, newDomainError(
				ErrorInvalidInput,
				"periodic source binding %s is duplicated",
				sourceBindingID,
			)
		}
		seen[sourceBindingID] = struct{}{}
		sourceBindingIDs[index] = sourceBindingID
	}
	if len(sourceBindingIDs) > maxWorkspaceSourceBindings {
		return PeriodicRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"periodic source bindings exceed %d entries",
			maxWorkspaceSourceBindings,
		)
	}
	return PeriodicRuntimeConfig{
		WorkspaceID: workspaceID, MaxSteps: config.MaxSteps, Interval: config.Interval,
		SourceBindingIDs: sourceBindingIDs,
	}, nil
}

func newPeriodicRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config PeriodicRuntimeConfig,
	sources []WorkspaceSourceBinding,
	bufferSize int,
	invoke periodicSourceTickFunc,
	initialWake bool,
) (*PeriodicRuntime, error) {
	if ctx == nil {
		return nil, errors.New("periodic runtime context is required")
	}
	if bufferSize < 1 {
		return nil, errors.New("periodic runtime buffer size must be positive")
	}
	if invoke == nil {
		return nil, errors.New("periodic source tick function is required")
	}
	if len(sources) == 0 || len(sources) > maxWorkspaceSourceBindings {
		return nil, fmt.Errorf("periodic runtime sources must contain 1 to %d bindings", maxWorkspaceSourceBindings)
	}

	lifecycle, cancel := context.WithCancel(ctx)
	periodic := &PeriodicRuntime{
		ctx: lifecycle, cancel: cancel, pool: pool,
		workspaceID: config.WorkspaceID, maxSteps: config.MaxSteps, interval: config.Interval,
		invoke: invoke, lanes: make(map[string]*periodicRuntimeLane, len(sources)),
		sourceOrder: make([]string, 0, len(sources)),
		slots:       make(chan struct{}, bufferSize),
		outcomes:    make(chan PeriodicRuntimeOutcome, bufferSize*len(sources)),
	}
	for _, source := range sources {
		if source.WorkspaceID != periodic.workspaceID {
			cancel()
			return nil, fmt.Errorf("periodic source %s does not belong to workspace %s", source.ID, periodic.workspaceID)
		}
		if _, exists := periodic.lanes[source.ID]; exists {
			cancel()
			return nil, fmt.Errorf("periodic runtime source %s is duplicated", source.ID)
		}
		lane := &periodicRuntimeLane{
			sourceBindingID: source.ID,
			wakeups:         make(chan struct{}, bufferSize),
		}
		periodic.lanes[source.ID] = lane
		periodic.sourceOrder = append(periodic.sourceOrder, source.ID)
		periodic.wg.Add(1)
		go periodic.runLane(lane)
	}
	periodic.wg.Add(1)
	go periodic.runScheduler(initialWake)
	return periodic, nil
}

// WakeSource submits one coalescible hint for a registered source lane.
func (r *PeriodicRuntime) WakeSource(sourceBindingID string) (bool, error) {
	sourceBindingID, err := normalizeDetectiveHashID(sourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return false, err
	}
	lane, found := r.lanes[sourceBindingID]
	if !found {
		return false, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in periodic workspace %s",
			sourceBindingID,
			r.workspaceID,
		)
	}
	return r.enqueueLane(lane), nil
}

// Outcomes returns bounded, non-authoritative wakeup observations.
func (r *PeriodicRuntime) Outcomes() <-chan PeriodicRuntimeOutcome {
	return r.outcomes
}

// Close cancels the scheduler and waits for active source ticks to return.
func (r *PeriodicRuntime) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.wg.Wait()
		close(r.outcomes)
	})
}

func (r *PeriodicRuntime) runScheduler(initialWake bool) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	if initialWake {
		r.wakeAll()
	}
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.wakeAll()
		}
	}
}

func (r *PeriodicRuntime) wakeAll() {
	for _, sourceBindingID := range r.sourceOrder {
		r.enqueueLane(r.lanes[sourceBindingID])
	}
}

func (r *PeriodicRuntime) enqueueLane(lane *periodicRuntimeLane) bool {
	if r.ctx.Err() != nil {
		return false
	}
	if !lane.pending.CompareAndSwap(false, true) {
		return false
	}
	select {
	case lane.wakeups <- struct{}{}:
		return true
	case <-r.ctx.Done():
		lane.pending.Store(false)
		return false
	default:
		lane.pending.Store(false)
		return false
	}
}

func (r *PeriodicRuntime) runLane(lane *periodicRuntimeLane) {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-lane.wakeups:
		}

		select {
		case r.slots <- struct{}{}:
		case <-r.ctx.Done():
			lane.pending.Store(false)
			return
		}
		result, err := r.invoke(r.ctx, r.pool, PeriodicSourceTickInput{
			WorkspaceID:     r.workspaceID,
			SourceBindingID: lane.sourceBindingID,
			MaxSteps:        r.maxSteps,
		})
		<-r.slots
		lane.pending.Store(false)

		outcome := PeriodicRuntimeOutcome{
			SourceBindingID: lane.sourceBindingID,
			Result:          result,
			Err:             err,
		}
		select {
		case r.outcomes <- outcome:
		default:
		}
	}
}
