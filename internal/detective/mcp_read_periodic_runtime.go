package detective

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MCPReadProposalExtractorConfig supplies one bounded model runner at the host edge.
type MCPReadProposalExtractorConfig struct {
	MaxProposals        int
	SectionMode         string
	MaxSections         int
	ExtractorDefinition evidenceingestion.ExtractorDefinitionInput
	Runner              evidenceingestion.ExtractorRunner
}

// MCPReadPeriodicSourceConfig binds one registered source to its in-memory
// command material. Only its immutable hash is persisted by registration.
type MCPReadPeriodicSourceConfig struct {
	SourceBindingID    string
	Config             MCPReadSourceConfig
	ProposalConversion bool
	ProposalExtractor  *MCPReadProposalExtractorConfig
}

// MCPReadPeriodicRuntimeConfig selects exact MCP read sources for periodic polling.
type MCPReadPeriodicRuntimeConfig struct {
	WorkspaceID string
	Interval    time.Duration
	Sources     []MCPReadPeriodicSourceConfig
}

// MCPReadPeriodicRuntimeOutcome is a non-authoritative wakeup observation.
type MCPReadPeriodicRuntimeOutcome struct {
	SourceBindingID string
	Result          MCPReadSourceTickResult
	Conversion      *MCPReadProposalConversionResult
	ConversionErr   error
	Extraction      *MCPReadProposalExtractionResult
	ExtractionErr   error
	Err             error
}

type mcpReadSourceTickFunc func(
	context.Context,
	*pgxpool.Pool,
	MCPReadSourceTickInput,
) (MCPReadSourceTickResult, error)

type mcpReadProposalConversionFunc func(
	context.Context,
	*pgxpool.Pool,
	MCPReadProposalConversionInput,
) (MCPReadProposalConversionResult, error)

type mcpReadPeriodicLane struct {
	source  MCPReadPeriodicSourceConfig
	wakeups chan struct{}
	pending atomic.Bool
}

// MCPReadPeriodicRuntime owns bounded in-process polling lanes. PostgreSQL
// cycle state, not channel state, is the durable recovery authority.
type MCPReadPeriodicRuntime struct {
	ctx         context.Context
	cancel      context.CancelFunc
	pool        *pgxpool.Pool
	workspaceID string
	interval    time.Duration
	invoke      mcpReadSourceTickFunc
	convert     mcpReadProposalConversionFunc
	lanes       map[string]*mcpReadPeriodicLane
	sourceOrder []string
	slots       chan struct{}
	outcomes    chan MCPReadPeriodicRuntimeOutcome
	wg          sync.WaitGroup
	closeOnce   sync.Once
}

// NewMCPReadPeriodicRuntime starts one recovery wake followed by periodic polls.
func NewMCPReadPeriodicRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config MCPReadPeriodicRuntimeConfig,
) (*MCPReadPeriodicRuntime, error) {
	if ctx == nil {
		return nil, errors.New("mcp read periodic runtime context is required")
	}
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareMCPReadPeriodicRuntimeConfig(config)
	if err != nil {
		return nil, err
	}
	workspace, err := ResolveWorkspace(ctx, pool, prepared.WorkspaceID)
	if err != nil {
		return nil, err
	}
	for index, sourceConfig := range prepared.Sources {
		source, found := workspaceSourceByID(workspace.Sources, sourceConfig.SourceBindingID)
		if !found {
			return nil, newDomainError(
				ErrorSourceBindingNotFound,
				"mcp read periodic source %s is not registered in workspace %s",
				sourceConfig.SourceBindingID,
				workspace.ID,
			)
		}
		sourcePrepared, err := prepareMCPReadSource(workspace.ID, source.ID, sourceConfig.Config)
		if err != nil {
			return nil, fmt.Errorf("preparing mcp read periodic source %d: %w", index, err)
		}
		binding, found, err := readMCPReadSourceBinding(ctx, pool, source.ID, false)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, newDomainError(
				ErrorSourceBindingNotFound,
				"mcp read invocation binding %s is not registered",
				source.ID,
			)
		}
		if err := ensureMCPReadSourceBindingMatches(sourcePrepared, source.SourceID, binding); err != nil {
			return nil, err
		}
		prepared.Sources[index].Config = sourcePrepared.config
	}
	return newMCPReadPeriodicRuntime(
		ctx,
		pool,
		prepared,
		runtime.NumCPU(),
		RunMCPReadSourceTick,
		ConvertMCPReadDocumentSnapshot,
		true,
	)
}

func prepareMCPReadPeriodicRuntimeConfig(
	config MCPReadPeriodicRuntimeConfig,
) (MCPReadPeriodicRuntimeConfig, error) {
	workspaceID, err := normalizeWorkspaceID(config.WorkspaceID)
	if err != nil {
		return MCPReadPeriodicRuntimeConfig{}, err
	}
	if config.Interval < minPeriodicRuntimeInterval || config.Interval > maxPeriodicRuntimeInterval {
		return MCPReadPeriodicRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"interval must be between %s and %s",
			minPeriodicRuntimeInterval,
			maxPeriodicRuntimeInterval,
		)
	}
	if len(config.Sources) == 0 || len(config.Sources) > maxWorkspaceSourceBindings {
		return MCPReadPeriodicRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"mcp read periodic sources must contain 1 to %d bindings",
			maxWorkspaceSourceBindings,
		)
	}
	sources := make([]MCPReadPeriodicSourceConfig, len(config.Sources))
	seen := make(map[string]struct{}, len(config.Sources))
	for index, source := range config.Sources {
		sourceID, err := normalizeDetectiveHashID(source.SourceBindingID, "workspace-source:", "source_binding_id")
		if err != nil {
			return MCPReadPeriodicRuntimeConfig{}, fmt.Errorf("preparing mcp read periodic source %d: %w", index, err)
		}
		if _, exists := seen[sourceID]; exists {
			return MCPReadPeriodicRuntimeConfig{}, newDomainError(
				ErrorInvalidInput,
				"mcp read periodic source %s is duplicated",
				sourceID,
			)
		}
		seen[sourceID] = struct{}{}
		sources[index] = MCPReadPeriodicSourceConfig{
			SourceBindingID:    sourceID,
			Config:             source.Config,
			ProposalConversion: source.ProposalConversion,
			ProposalExtractor:  cloneMCPReadProposalExtractorConfig(source.ProposalExtractor),
		}
		if sources[index].ProposalConversion && sources[index].ProposalExtractor != nil {
			return MCPReadPeriodicRuntimeConfig{}, newDomainError(
				ErrorInvalidInput,
				"mcp read periodic source %s cannot enable proposal conversion and model extraction together",
				sourceID,
			)
		}
		if extractor := sources[index].ProposalExtractor; extractor != nil {
			if extractor.Runner == nil {
				return MCPReadPeriodicRuntimeConfig{}, newDomainError(
					ErrorInvalidInput,
					"mcp read periodic source %s proposal extractor runner is required",
					sourceID,
				)
			}
			if _, err := validateMCPReadProposalExtractionInput(MCPReadProposalExtractionInput{
				MaxProposals: extractor.MaxProposals,
				SectionMode:  extractor.SectionMode,
				MaxSections:  extractor.MaxSections,
			}); err != nil {
				return MCPReadPeriodicRuntimeConfig{}, fmt.Errorf(
					"mcp read periodic source %s: %w",
					sourceID,
					err,
				)
			}
		}
	}
	return MCPReadPeriodicRuntimeConfig{
		WorkspaceID: workspaceID,
		Interval:    config.Interval,
		Sources:     sources,
	}, nil
}

func cloneMCPReadProposalExtractorConfig(
	input *MCPReadProposalExtractorConfig,
) *MCPReadProposalExtractorConfig {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.ExtractorDefinition.Config = make(map[string]string, len(input.ExtractorDefinition.Config))
	for key, value := range input.ExtractorDefinition.Config {
		cloned.ExtractorDefinition.Config[key] = value
	}
	return &cloned
}

func newMCPReadPeriodicRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config MCPReadPeriodicRuntimeConfig,
	bufferSize int,
	invoke mcpReadSourceTickFunc,
	convert mcpReadProposalConversionFunc,
	initialWake bool,
) (*MCPReadPeriodicRuntime, error) {
	if ctx == nil {
		return nil, errors.New("mcp read periodic runtime context is required")
	}
	if bufferSize < 1 {
		return nil, errors.New("mcp read periodic runtime buffer size must be positive")
	}
	if invoke == nil {
		return nil, errors.New("mcp read source tick function is required")
	}
	if convert == nil {
		return nil, errors.New("mcp read proposal conversion function is required")
	}
	prepared, err := prepareMCPReadPeriodicRuntimeConfig(config)
	if err != nil {
		return nil, err
	}

	lifecycle, cancel := context.WithCancel(ctx)
	periodic := &MCPReadPeriodicRuntime{
		ctx: lifecycle, cancel: cancel, pool: pool,
		workspaceID: prepared.WorkspaceID, interval: prepared.Interval,
		invoke: invoke, convert: convert,
		lanes:       make(map[string]*mcpReadPeriodicLane, len(prepared.Sources)),
		sourceOrder: make([]string, 0, len(prepared.Sources)),
		slots:       make(chan struct{}, bufferSize),
		outcomes:    make(chan MCPReadPeriodicRuntimeOutcome, bufferSize*len(prepared.Sources)),
	}
	for _, source := range prepared.Sources {
		lane := &mcpReadPeriodicLane{
			source:  source,
			wakeups: make(chan struct{}, bufferSize),
		}
		periodic.lanes[source.SourceBindingID] = lane
		periodic.sourceOrder = append(periodic.sourceOrder, source.SourceBindingID)
		periodic.wg.Add(1)
		go periodic.runLane(lane)
	}
	periodic.wg.Add(1)
	go periodic.runScheduler(initialWake)
	return periodic, nil
}

// WakeSource submits one coalescible polling hint.
func (r *MCPReadPeriodicRuntime) WakeSource(sourceBindingID string) (bool, error) {
	sourceBindingID, err := normalizeDetectiveHashID(sourceBindingID, "workspace-source:", "source_binding_id")
	if err != nil {
		return false, err
	}
	lane, found := r.lanes[sourceBindingID]
	if !found {
		return false, newDomainError(
			ErrorSourceBindingNotFound,
			"source binding %s is not registered in mcp read periodic workspace %s",
			sourceBindingID,
			r.workspaceID,
		)
	}
	return r.enqueueLane(lane), nil
}

// Outcomes returns bounded, non-authoritative wakeup observations.
func (r *MCPReadPeriodicRuntime) Outcomes() <-chan MCPReadPeriodicRuntimeOutcome {
	return r.outcomes
}

// Close cancels the scheduler and waits for active source ticks.
func (r *MCPReadPeriodicRuntime) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.wg.Wait()
		close(r.outcomes)
	})
}

func (r *MCPReadPeriodicRuntime) runScheduler(initialWake bool) {
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

func (r *MCPReadPeriodicRuntime) wakeAll() {
	for _, sourceBindingID := range r.sourceOrder {
		r.enqueueLane(r.lanes[sourceBindingID])
	}
}

func (r *MCPReadPeriodicRuntime) enqueueLane(lane *mcpReadPeriodicLane) bool {
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

func (r *MCPReadPeriodicRuntime) runLane(lane *mcpReadPeriodicLane) {
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
			return
		}
		result, err := r.invoke(r.ctx, r.pool, MCPReadSourceTickInput{
			WorkspaceID:     r.workspaceID,
			SourceBindingID: lane.source.SourceBindingID,
			Config:          lane.source.Config,
		})
		var conversion *MCPReadProposalConversionResult
		var conversionErr error
		var extraction *MCPReadProposalExtractionResult
		var extractionErr error
		if err == nil &&
			lane.source.ProposalConversion &&
			!result.Deferred &&
			result.Cycle.Status == MCPReadCycleStatusCompleted {
			converted, convertErr := r.convert(r.ctx, r.pool, MCPReadProposalConversionInput{
				SourceSnapshotID: result.Cycle.SourceSnapshotID,
			})
			conversion = &converted
			conversionErr = convertErr
		}
		if err == nil &&
			lane.source.ProposalExtractor != nil &&
			!result.Deferred &&
			result.Cycle.Status == MCPReadCycleStatusCompleted {
			extractor := lane.source.ProposalExtractor
			extracted, extractErr := ExtractMCPReadDocumentSnapshot(
				r.ctx,
				r.pool,
				MCPReadProposalExtractionInput{
					SourceSnapshotID:    result.Cycle.SourceSnapshotID,
					MaxProposals:        extractor.MaxProposals,
					SectionMode:         extractor.SectionMode,
					MaxSections:         extractor.MaxSections,
					ExtractorDefinition: extractor.ExtractorDefinition,
					Runner:              extractor.Runner,
				},
			)
			extraction = &extracted
			extractionErr = extractErr
		}
		<-r.slots
		lane.pending.Store(false)

		outcome := MCPReadPeriodicRuntimeOutcome{
			SourceBindingID: lane.source.SourceBindingID,
			Result:          result,
			Conversion:      conversion,
			ConversionErr:   conversionErr,
			Extraction:      extraction,
			ExtractionErr:   extractionErr,
			Err:             err,
		}
		select {
		case r.outcomes <- outcome:
		case <-r.ctx.Done():
			return
		default:
		}
	}
}
