package detective

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMCPReadPeriodicRuntimeUsesCPUBufferedLanes(t *testing.T) {
	config := mcpReadPeriodicRuntimeTestConfig("workspace:mcp-runtime-buffer", 2)
	controller, err := newMCPReadPeriodicRuntime(
		t.Context(),
		nil,
		config,
		runtime.NumCPU(),
		func(context.Context, *pgxpool.Pool, MCPReadSourceTickInput) (MCPReadSourceTickResult, error) {
			return MCPReadSourceTickResult{}, nil
		},
		func(context.Context, *pgxpool.Pool, MCPReadProposalConversionInput) (MCPReadProposalConversionResult, error) {
			return MCPReadProposalConversionResult{}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newMCPReadPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(controller.Close)

	if got, want := cap(controller.slots), runtime.NumCPU(); got != want {
		t.Fatalf("MCP runtime slot capacity = %d, want %d", got, want)
	}
	for sourceID, lane := range controller.lanes {
		if got, want := cap(lane.wakeups), runtime.NumCPU(); got != want {
			t.Fatalf("MCP source lane %s capacity = %d, want %d", sourceID, got, want)
		}
	}
}

func TestMCPReadPeriodicRuntimeCoalescesPerSourceAndRunsDistinctSourcesInParallel(t *testing.T) {
	config := mcpReadPeriodicRuntimeTestConfig("workspace:mcp-runtime-parallel", 2)
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	var mu sync.Mutex
	active := 0
	maxActive := 0
	controller, err := newMCPReadPeriodicRuntime(
		t.Context(),
		nil,
		config,
		2,
		func(ctx context.Context, _ *pgxpool.Pool, input MCPReadSourceTickInput) (MCPReadSourceTickResult, error) {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			started <- input.SourceBindingID
			select {
			case <-release:
			case <-ctx.Done():
				return MCPReadSourceTickResult{}, ctx.Err()
			}
			mu.Lock()
			active--
			mu.Unlock()
			return MCPReadSourceTickResult{}, nil
		},
		func(context.Context, *pgxpool.Pool, MCPReadProposalConversionInput) (MCPReadProposalConversionResult, error) {
			return MCPReadProposalConversionResult{}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newMCPReadPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(controller.Close)

	firstID := config.Sources[0].SourceBindingID
	secondID := config.Sources[1].SourceBindingID
	if accepted, wakeErr := controller.WakeSource(firstID); wakeErr != nil || !accepted {
		t.Fatalf("first WakeSource() = %t, %v", accepted, wakeErr)
	}
	if accepted, wakeErr := controller.WakeSource(firstID); wakeErr != nil || accepted {
		t.Fatalf("coalesced WakeSource() = %t, %v", accepted, wakeErr)
	}
	if accepted, wakeErr := controller.WakeSource(secondID); wakeErr != nil || !accepted {
		t.Fatalf("second WakeSource() = %t, %v", accepted, wakeErr)
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case sourceID := <-started:
			seen[sourceID] = true
		case <-time.After(time.Second):
			t.Fatal("MCP source tick did not start")
		}
	}
	mu.Lock()
	gotMaxActive := maxActive
	mu.Unlock()
	if !seen[firstID] || !seen[secondID] || gotMaxActive != 2 {
		t.Fatalf("started MCP sources = %#v, maximum active = %d", seen, gotMaxActive)
	}
	release <- struct{}{}
	release <- struct{}{}
}

func TestMCPReadPeriodicRuntimeSchedulesInitialAndRepeatedWakeups(t *testing.T) {
	config := mcpReadPeriodicRuntimeTestConfig("workspace:mcp-runtime-timer", 1)
	config.Interval = 5 * time.Millisecond
	started := make(chan struct{}, 3)
	controller, err := newMCPReadPeriodicRuntime(
		t.Context(),
		nil,
		config,
		1,
		func(context.Context, *pgxpool.Pool, MCPReadSourceTickInput) (MCPReadSourceTickResult, error) {
			started <- struct{}{}
			return MCPReadSourceTickResult{}, nil
		},
		func(context.Context, *pgxpool.Pool, MCPReadProposalConversionInput) (MCPReadProposalConversionResult, error) {
			return MCPReadProposalConversionResult{}, nil
		},
		true,
	)
	if err != nil {
		t.Fatalf("newMCPReadPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(controller.Close)
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("MCP periodic timer did not invoke repeated source wakeups")
		}
	}
}

func TestMCPReadPeriodicRuntimeConvertsOnlyCompletedOptInSource(t *testing.T) {
	config := mcpReadPeriodicRuntimeTestConfig("workspace:mcp-runtime-convert", 1)
	config.Sources[0].ProposalConversion = true
	sourceSnapshotID := "srcsnap:" + config.Sources[0].SourceBindingID[len("workspace-source:"):]
	converted := make(chan MCPReadProposalConversionInput, 1)
	controller, err := newMCPReadPeriodicRuntime(
		t.Context(),
		nil,
		config,
		1,
		func(context.Context, *pgxpool.Pool, MCPReadSourceTickInput) (MCPReadSourceTickResult, error) {
			return MCPReadSourceTickResult{
				Cycle: MCPReadCollectionCycle{
					Status:           MCPReadCycleStatusCompleted,
					SourceSnapshotID: sourceSnapshotID,
				},
			}, nil
		},
		func(
			_ context.Context,
			_ *pgxpool.Pool,
			input MCPReadProposalConversionInput,
		) (MCPReadProposalConversionResult, error) {
			converted <- input
			return MCPReadProposalConversionResult{
				Status:           MCPReadProposalConversionStatusConverted,
				SourceSnapshotID: input.SourceSnapshotID,
				ProposalCount:    1,
			}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newMCPReadPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(controller.Close)
	if accepted, wakeErr := controller.WakeSource(config.Sources[0].SourceBindingID); wakeErr != nil || !accepted {
		t.Fatalf("WakeSource() = %t, %v", accepted, wakeErr)
	}
	select {
	case input := <-converted:
		if input.SourceSnapshotID != sourceSnapshotID {
			t.Fatalf("conversion input = %+v", input)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP proposal conversion was not invoked")
	}
	select {
	case outcome := <-controller.Outcomes():
		if outcome.Err != nil || outcome.ConversionErr != nil ||
			outcome.Conversion == nil ||
			outcome.Conversion.ProposalCount != 1 {
			t.Fatalf("MCP conversion outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP conversion outcome was not emitted")
	}
}

func mcpReadPeriodicRuntimeTestConfig(workspaceID string, count int) MCPReadPeriodicRuntimeConfig {
	sources := make([]MCPReadPeriodicSourceConfig, 0, count)
	for index := range count {
		sourceID := periodicSourceCycleID(workspaceID, "mcp-source", int64(index+1), "binding")
		sources = append(sources, MCPReadPeriodicSourceConfig{
			SourceBindingID: "workspace-source:" + sourceID[len("detective-periodic-cycle:"):],
		})
	}
	return MCPReadPeriodicRuntimeConfig{
		WorkspaceID: workspaceID,
		Interval:    time.Hour,
		Sources:     sources,
	}
}
