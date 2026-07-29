package detective

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewPlannerPeriodicRuntimeRequiresRunner(t *testing.T) {
	_, err := NewPlannerPeriodicRuntime(t.Context(), nil, PeriodicRuntimeConfig{}, nil)
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestPreparePeriodicRuntimeConfigPinsExplicitSourceSubset(t *testing.T) {
	first := "workspace-source:" + periodicSourceCycleID("workspace:subset", "one", 1, "token")[len("detective-periodic-cycle:"):]
	second := "workspace-source:" + periodicSourceCycleID("workspace:subset", "two", 1, "token")[len("detective-periodic-cycle:"):]
	prepared, err := preparePeriodicRuntimeConfig(PeriodicRuntimeConfig{
		WorkspaceID:      " workspace:subset ",
		MaxSteps:         2,
		Interval:         time.Second,
		SourceBindingIDs: []string{" " + first + " ", second},
	})
	if err != nil {
		t.Fatalf("preparePeriodicRuntimeConfig() error = %v", err)
	}
	if prepared.WorkspaceID != "workspace:subset" ||
		len(prepared.SourceBindingIDs) != 2 ||
		prepared.SourceBindingIDs[0] != first ||
		prepared.SourceBindingIDs[1] != second {
		t.Fatalf("prepared periodic runtime config = %+v", prepared)
	}
	_, err = preparePeriodicRuntimeConfig(PeriodicRuntimeConfig{
		WorkspaceID:      "workspace:subset",
		MaxSteps:         2,
		Interval:         time.Second,
		SourceBindingIDs: []string{first, first},
	})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestPeriodicRuntimeUsesCPUBufferedPerSourceLanes(t *testing.T) {
	workspaceID := "workspace:periodic-runtime-buffer"
	sources := periodicRuntimeTestSources(workspaceID, 2)
	runtimeController, err := newPeriodicRuntime(
		t.Context(),
		nil,
		PeriodicRuntimeConfig{WorkspaceID: workspaceID, MaxSteps: 2, Interval: time.Hour},
		sources,
		runtime.NumCPU(),
		func(context.Context, *pgxpool.Pool, PeriodicSourceTickInput) (PeriodicSourceTickResult, error) {
			return PeriodicSourceTickResult{}, nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("newPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)

	if got, want := cap(runtimeController.slots), runtime.NumCPU(); got != want {
		t.Fatalf("periodic runtime slot capacity = %d, want %d", got, want)
	}
	for sourceID, lane := range runtimeController.lanes {
		if got, want := cap(lane.wakeups), runtime.NumCPU(); got != want {
			t.Fatalf("periodic source lane %s capacity = %d, want %d", sourceID, got, want)
		}
	}
}

func TestPeriodicRuntimeCoalescesSameSourceAndRunsDifferentSourcesConcurrently(t *testing.T) {
	workspaceID := "workspace:periodic-runtime-concurrency"
	sources := periodicRuntimeTestSources(workspaceID, 2)
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	var mu sync.Mutex
	active := 0
	maxActive := 0
	invoke := func(ctx context.Context, _ *pgxpool.Pool, input PeriodicSourceTickInput) (PeriodicSourceTickResult, error) {
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
			return PeriodicSourceTickResult{}, ctx.Err()
		}
		mu.Lock()
		active--
		mu.Unlock()
		return PeriodicSourceTickResult{}, nil
	}
	runtimeController, err := newPeriodicRuntime(
		t.Context(),
		nil,
		PeriodicRuntimeConfig{WorkspaceID: workspaceID, MaxSteps: 2, Interval: time.Hour},
		sources,
		2,
		invoke,
		false,
	)
	if err != nil {
		t.Fatalf("newPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)

	accepted, err := runtimeController.WakeSource(sources[0].ID)
	if err != nil || !accepted {
		t.Fatalf("first WakeSource() = %t, %v", accepted, err)
	}
	accepted, err = runtimeController.WakeSource(sources[0].ID)
	if err != nil || accepted {
		t.Fatalf("coalesced WakeSource() = %t, %v", accepted, err)
	}
	accepted, err = runtimeController.WakeSource(sources[1].ID)
	if err != nil || !accepted {
		t.Fatalf("other-source WakeSource() = %t, %v", accepted, err)
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case sourceID := <-started:
			seen[sourceID] = true
		case <-time.After(time.Second):
			t.Fatal("periodic source tick did not start")
		}
	}
	if !seen[sources[0].ID] || !seen[sources[1].ID] {
		t.Fatalf("started periodic sources = %#v", seen)
	}
	mu.Lock()
	gotMaxActive := maxActive
	mu.Unlock()
	if gotMaxActive != 2 {
		t.Fatalf("periodic runtime maximum concurrency = %d, want 2", gotMaxActive)
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		select {
		case outcome := <-runtimeController.Outcomes():
			if outcome.Err != nil {
				t.Fatalf("periodic runtime outcome error = %v", outcome.Err)
			}
		case <-time.After(time.Second):
			t.Fatal("periodic runtime outcome was not reported")
		}
	}
}

func TestPeriodicRuntimeBoundsCrossSourceConcurrency(t *testing.T) {
	workspaceID := "workspace:periodic-runtime-bounded"
	sources := periodicRuntimeTestSources(workspaceID, 3)
	started := make(chan string, 3)
	release := make(chan struct{}, 3)
	runtimeController, err := newPeriodicRuntime(
		t.Context(),
		nil,
		PeriodicRuntimeConfig{WorkspaceID: workspaceID, MaxSteps: 1, Interval: time.Hour},
		sources,
		2,
		func(ctx context.Context, _ *pgxpool.Pool, input PeriodicSourceTickInput) (PeriodicSourceTickResult, error) {
			started <- input.SourceBindingID
			select {
			case <-release:
				return PeriodicSourceTickResult{}, nil
			case <-ctx.Done():
				return PeriodicSourceTickResult{}, ctx.Err()
			}
		},
		false,
	)
	if err != nil {
		t.Fatalf("newPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)
	for _, source := range sources {
		if accepted, wakeErr := runtimeController.WakeSource(source.ID); wakeErr != nil || !accepted {
			t.Fatalf("WakeSource(%s) = %t, %v", source.ID, accepted, wakeErr)
		}
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("bounded periodic source tick did not start")
		}
	}
	select {
	case third := <-started:
		t.Fatalf("third periodic source %s exceeded concurrency bound", third)
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("third periodic source did not start after a slot was released")
	}
	release <- struct{}{}
	release <- struct{}{}
}

func TestPeriodicRuntimeSchedulesRepeatedTimerWakeups(t *testing.T) {
	workspaceID := "workspace:periodic-runtime-timer"
	sources := periodicRuntimeTestSources(workspaceID, 1)
	started := make(chan struct{}, 3)
	runtimeController, err := newPeriodicRuntime(
		t.Context(),
		nil,
		PeriodicRuntimeConfig{WorkspaceID: workspaceID, MaxSteps: 1, Interval: 5 * time.Millisecond},
		sources,
		1,
		func(context.Context, *pgxpool.Pool, PeriodicSourceTickInput) (PeriodicSourceTickResult, error) {
			started <- struct{}{}
			return PeriodicSourceTickResult{}, nil
		},
		true,
	)
	if err != nil {
		t.Fatalf("newPeriodicRuntime() error = %v", err)
	}
	t.Cleanup(runtimeController.Close)
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("periodic timer did not invoke repeated source wakeups")
		}
	}
}

func periodicRuntimeTestSources(workspaceID string, count int) []WorkspaceSourceBinding {
	sources := make([]WorkspaceSourceBinding, 0, count)
	for index := range count {
		sourceID := periodicSourceCycleID(workspaceID, "source", int64(index+1), "token")
		sources = append(sources, WorkspaceSourceBinding{
			ID:                "workspace-source:" + sourceID[len("detective-periodic-cycle:"):],
			WorkspaceID:       workspaceID,
			CapabilityName:    SourceCapabilityLocalPRDText,
			CapabilityVersion: SourceCapabilityLocalPRDTextVersion,
		})
	}
	return sources
}
