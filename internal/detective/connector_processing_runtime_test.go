package detective

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectorTextProcessingRuntimeUsesCPUBufferedConnectorLanes(t *testing.T) {
	config, err := prepareConnectorTextProcessingRuntimeConfig(ConnectorTextProcessingRuntimeConfig{
		ConnectorIDs: []string{"connector-b", "connector-a"}, MaxDeliveries: 2, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("prepareConnectorTextProcessingRuntimeConfig() error = %v", err)
	}
	runtimeController, err := newConnectorTextProcessingRuntime(
		t.Context(), nil, config, runtime.NumCPU(),
		func(context.Context, *pgxpool.Pool, ConnectorTextProcessingTickInput) (ConnectorTextProcessingTickResult, error) {
			return ConnectorTextProcessingTickResult{}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newConnectorTextProcessingRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)

	if got, want := cap(runtimeController.slots), runtime.NumCPU(); got != want {
		t.Fatalf("connector processing runtime slot capacity = %d, want %d", got, want)
	}
	for connectorID, lane := range runtimeController.lanes {
		if got, want := cap(lane.wakeups), runtime.NumCPU(); got != want {
			t.Fatalf("connector lane %s capacity = %d, want %d", connectorID, got, want)
		}
	}
	if got := runtimeController.connectorOrder; len(got) != 2 || got[0] != "connector-a" || got[1] != "connector-b" {
		t.Fatalf("connector processing order = %v", got)
	}
}

func TestConnectorTextProcessingRuntimeCoalescesSameConnectorAndRunsDifferentConnectorsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	var mu sync.Mutex
	active := 0
	maxActive := 0
	runtimeController, err := newConnectorTextProcessingRuntime(
		t.Context(), nil,
		ConnectorTextProcessingRuntimeConfig{
			ConnectorIDs: []string{"connector-a", "connector-b"}, MaxDeliveries: 2, Interval: time.Hour,
		},
		2,
		func(ctx context.Context, _ *pgxpool.Pool, input ConnectorTextProcessingTickInput) (ConnectorTextProcessingTickResult, error) {
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			started <- input.ConnectorID
			select {
			case <-release:
			case <-ctx.Done():
				return ConnectorTextProcessingTickResult{}, ctx.Err()
			}
			mu.Lock()
			active--
			mu.Unlock()
			return ConnectorTextProcessingTickResult{ConnectorID: input.ConnectorID}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newConnectorTextProcessingRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)

	accepted, err := runtimeController.WakeConnector("connector-a")
	if err != nil || !accepted {
		t.Fatalf("first WakeConnector() = %t, %v", accepted, err)
	}
	accepted, err = runtimeController.WakeConnector("connector-a")
	if err != nil || accepted {
		t.Fatalf("coalesced WakeConnector() = %t, %v", accepted, err)
	}
	accepted, err = runtimeController.WakeConnector("connector-b")
	if err != nil || !accepted {
		t.Fatalf("other WakeConnector() = %t, %v", accepted, err)
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case connectorID := <-started:
			seen[connectorID] = true
		case <-time.After(time.Second):
			t.Fatal("connector processing tick did not start")
		}
	}
	if !seen["connector-a"] || !seen["connector-b"] {
		t.Fatalf("started connectors = %#v", seen)
	}
	mu.Lock()
	gotMaxActive := maxActive
	mu.Unlock()
	if gotMaxActive != 2 {
		t.Fatalf("connector processing maximum concurrency = %d, want 2", gotMaxActive)
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		select {
		case outcome := <-runtimeController.Outcomes():
			if outcome.Err != nil {
				t.Fatalf("connector processing outcome error = %v", outcome.Err)
			}
		case <-time.After(time.Second):
			t.Fatal("connector processing outcome was not reported")
		}
	}
}

func TestConnectorTextProcessingRuntimeBoundsCrossConnectorConcurrency(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{}, 3)
	runtimeController, err := newConnectorTextProcessingRuntime(
		t.Context(), nil,
		ConnectorTextProcessingRuntimeConfig{
			ConnectorIDs: []string{"connector-a", "connector-b", "connector-c"}, MaxDeliveries: 1, Interval: time.Hour,
		},
		2,
		func(ctx context.Context, _ *pgxpool.Pool, input ConnectorTextProcessingTickInput) (ConnectorTextProcessingTickResult, error) {
			started <- input.ConnectorID
			select {
			case <-release:
				return ConnectorTextProcessingTickResult{}, nil
			case <-ctx.Done():
				return ConnectorTextProcessingTickResult{}, ctx.Err()
			}
		},
		false,
	)
	if err != nil {
		t.Fatalf("newConnectorTextProcessingRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)
	for _, connectorID := range []string{"connector-a", "connector-b", "connector-c"} {
		if accepted, wakeErr := runtimeController.WakeConnector(connectorID); wakeErr != nil || !accepted {
			t.Fatalf("WakeConnector(%s) = %t, %v", connectorID, accepted, wakeErr)
		}
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("bounded connector processing tick did not start")
		}
	}
	select {
	case third := <-started:
		t.Fatalf("third connector %s exceeded concurrency bound", third)
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("third connector did not start after a slot was released")
	}
	release <- struct{}{}
	release <- struct{}{}
}

func TestPrepareConnectorTextProcessingRuntimeConfigRejectsDuplicateAndUnknownWake(t *testing.T) {
	_, err := prepareConnectorTextProcessingRuntimeConfig(ConnectorTextProcessingRuntimeConfig{
		ConnectorIDs: []string{"connector-a", " connector-a "}, MaxDeliveries: 1, Interval: time.Hour,
	})
	assertDetectiveKind(t, err, ErrorInvalidInput)

	runtimeController, err := newConnectorTextProcessingRuntime(
		t.Context(), nil,
		ConnectorTextProcessingRuntimeConfig{
			ConnectorIDs: []string{"connector-a"}, MaxDeliveries: 1, Interval: time.Hour,
		},
		1,
		func(context.Context, *pgxpool.Pool, ConnectorTextProcessingTickInput) (ConnectorTextProcessingTickResult, error) {
			return ConnectorTextProcessingTickResult{}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newConnectorTextProcessingRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)
	_, err = runtimeController.WakeConnector("connector-b")
	assertDetectiveKind(t, err, ErrorUnsupportedCapability)
}
