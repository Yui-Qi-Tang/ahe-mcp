package detective

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const maxConnectorTextProcessingRuntimeConnectors = 32

// ConnectorTextProcessingRuntimeConfig selects an immutable connector allowlist.
type ConnectorTextProcessingRuntimeConfig struct {
	ConnectorIDs  []string      `json:"connector_ids"`
	MaxDeliveries int           `json:"max_deliveries"`
	Interval      time.Duration `json:"interval"`
}

// ConnectorTextProcessingRuntimeOutcome is an informational result from one coalesced wakeup.
type ConnectorTextProcessingRuntimeOutcome struct {
	ConnectorID string
	Result      ConnectorTextProcessingTickResult
	Err         error
}

type connectorTextProcessingTickFunc func(
	context.Context,
	*pgxpool.Pool,
	ConnectorTextProcessingTickInput,
) (ConnectorTextProcessingTickResult, error)

type connectorTextProcessingRuntimeLane struct {
	connectorID string
	wakeups     chan struct{}
	pending     atomic.Bool
}

// ConnectorTextProcessingRuntime owns bounded process-local wakeup lanes.
// PostgreSQL processing work remains authoritative across processes.
type ConnectorTextProcessingRuntime struct {
	ctx            context.Context
	cancel         context.CancelFunc
	pool           *pgxpool.Pool
	maxDeliveries  int
	interval       time.Duration
	invoke         connectorTextProcessingTickFunc
	lanes          map[string]*connectorTextProcessingRuntimeLane
	connectorOrder []string
	slots          chan struct{}
	outcomes       chan ConnectorTextProcessingRuntimeOutcome
	wg             sync.WaitGroup
	closeOnce      sync.Once
}

// NewConnectorTextProcessingRuntime starts initial recovery discovery and periodic wakeups.
func NewConnectorTextProcessingRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config ConnectorTextProcessingRuntimeConfig,
) (*ConnectorTextProcessingRuntime, error) {
	if ctx == nil {
		return nil, errors.New("connector text processing runtime context is required")
	}
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareConnectorTextProcessingRuntimeConfig(config)
	if err != nil {
		return nil, err
	}
	return newConnectorTextProcessingRuntime(
		ctx,
		pool,
		prepared,
		runtime.NumCPU(),
		RunConnectorTextProcessingTick,
		true,
	)
}

func prepareConnectorTextProcessingRuntimeConfig(
	config ConnectorTextProcessingRuntimeConfig,
) (ConnectorTextProcessingRuntimeConfig, error) {
	if len(config.ConnectorIDs) == 0 || len(config.ConnectorIDs) > maxConnectorTextProcessingRuntimeConnectors {
		return ConnectorTextProcessingRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"connector_ids must contain 1 to %d identities",
			maxConnectorTextProcessingRuntimeConnectors,
		)
	}
	if config.MaxDeliveries < 1 || config.MaxDeliveries > maxConnectorTextProcessingTickDeliveries {
		return ConnectorTextProcessingRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"max_deliveries must be between 1 and %d",
			maxConnectorTextProcessingTickDeliveries,
		)
	}
	if config.Interval < minPeriodicRuntimeInterval || config.Interval > maxPeriodicRuntimeInterval {
		return ConnectorTextProcessingRuntimeConfig{}, newDomainError(
			ErrorInvalidInput,
			"interval must be between %s and %s",
			minPeriodicRuntimeInterval,
			maxPeriodicRuntimeInterval,
		)
	}
	connectorIDs := make([]string, 0, len(config.ConnectorIDs))
	seen := make(map[string]struct{}, len(config.ConnectorIDs))
	for _, value := range config.ConnectorIDs {
		connectorID, err := normalizeBoundedText(value, "connector_id", 200)
		if err != nil {
			return ConnectorTextProcessingRuntimeConfig{}, err
		}
		if _, exists := seen[connectorID]; exists {
			return ConnectorTextProcessingRuntimeConfig{}, newDomainError(
				ErrorInvalidInput,
				"connector_id %s is duplicated",
				connectorID,
			)
		}
		seen[connectorID] = struct{}{}
		connectorIDs = append(connectorIDs, connectorID)
	}
	sort.Strings(connectorIDs)
	return ConnectorTextProcessingRuntimeConfig{
		ConnectorIDs: connectorIDs, MaxDeliveries: config.MaxDeliveries, Interval: config.Interval,
	}, nil
}

func newConnectorTextProcessingRuntime(
	ctx context.Context,
	pool *pgxpool.Pool,
	config ConnectorTextProcessingRuntimeConfig,
	bufferSize int,
	invoke connectorTextProcessingTickFunc,
	initialWake bool,
) (*ConnectorTextProcessingRuntime, error) {
	if ctx == nil {
		return nil, errors.New("connector text processing runtime context is required")
	}
	if bufferSize < 1 {
		return nil, errors.New("connector text processing runtime buffer size must be positive")
	}
	if invoke == nil {
		return nil, errors.New("connector text processing tick function is required")
	}
	if len(config.ConnectorIDs) == 0 || len(config.ConnectorIDs) > maxConnectorTextProcessingRuntimeConnectors {
		return nil, fmt.Errorf(
			"connector text processing runtime must contain 1 to %d connectors",
			maxConnectorTextProcessingRuntimeConnectors,
		)
	}

	lifecycle, cancel := context.WithCancel(ctx)
	r := &ConnectorTextProcessingRuntime{
		ctx: lifecycle, cancel: cancel, pool: pool,
		maxDeliveries: config.MaxDeliveries, interval: config.Interval,
		invoke:         invoke,
		lanes:          make(map[string]*connectorTextProcessingRuntimeLane, len(config.ConnectorIDs)),
		connectorOrder: make([]string, 0, len(config.ConnectorIDs)),
		slots:          make(chan struct{}, bufferSize),
		outcomes:       make(chan ConnectorTextProcessingRuntimeOutcome, bufferSize*len(config.ConnectorIDs)),
	}
	for _, connectorID := range config.ConnectorIDs {
		if _, exists := r.lanes[connectorID]; exists {
			cancel()
			return nil, fmt.Errorf("connector text processing runtime connector %s is duplicated", connectorID)
		}
		lane := &connectorTextProcessingRuntimeLane{
			connectorID: connectorID,
			wakeups:     make(chan struct{}, bufferSize),
		}
		r.lanes[connectorID] = lane
		r.connectorOrder = append(r.connectorOrder, connectorID)
		r.wg.Add(1)
		go r.runLane(lane)
	}
	r.wg.Add(1)
	go r.runScheduler(initialWake)
	return r, nil
}

// WakeConnector submits one coalescible hint for an allowlisted connector lane.
func (r *ConnectorTextProcessingRuntime) WakeConnector(connectorID string) (bool, error) {
	connectorID, err := normalizeBoundedText(connectorID, "connector_id", 200)
	if err != nil {
		return false, err
	}
	lane, found := r.lanes[connectorID]
	if !found {
		return false, newDomainError(
			ErrorUnsupportedCapability,
			"connector %s is not allowlisted in this processing runtime",
			connectorID,
		)
	}
	return r.enqueueLane(lane), nil
}

// Outcomes returns bounded, non-authoritative wakeup observations.
func (r *ConnectorTextProcessingRuntime) Outcomes() <-chan ConnectorTextProcessingRuntimeOutcome {
	return r.outcomes
}

// Close cancels the scheduler and waits for active processing ticks to return.
func (r *ConnectorTextProcessingRuntime) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		r.wg.Wait()
		close(r.outcomes)
	})
}

func (r *ConnectorTextProcessingRuntime) runScheduler(initialWake bool) {
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

func (r *ConnectorTextProcessingRuntime) wakeAll() {
	for _, connectorID := range r.connectorOrder {
		r.enqueueLane(r.lanes[connectorID])
	}
}

func (r *ConnectorTextProcessingRuntime) enqueueLane(lane *connectorTextProcessingRuntimeLane) bool {
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

func (r *ConnectorTextProcessingRuntime) runLane(lane *connectorTextProcessingRuntimeLane) {
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
		result, err := r.invoke(r.ctx, r.pool, ConnectorTextProcessingTickInput{
			ConnectorID: lane.connectorID, MaxDeliveries: r.maxDeliveries,
		})
		<-r.slots
		lane.pending.Store(false)

		outcome := ConnectorTextProcessingRuntimeOutcome{
			ConnectorID: lane.connectorID,
			Result:      result,
			Err:         err,
		}
		select {
		case r.outcomes <- outcome:
		default:
		}
	}
}
