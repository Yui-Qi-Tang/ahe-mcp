package mcpadmin

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

func TestSourceIngressBackendUsesCPUBufferedSourceLanes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend, err := NewSourceIngressBackend(ctx, &recordingIngressBackend{})
	if err != nil {
		t.Fatalf("NewSourceIngressBackend() error = %v", err)
	}
	t.Cleanup(backend.Close)

	for key, lane := range backend.lanes {
		if got, want := cap(lane.jobs), runtime.NumCPU(); got != want {
			t.Fatalf("source ingress lane %s capacity = %d, want %d", key, got, want)
		}
	}
}

func TestSourceIngressBackendSerializesOneSourceAndRunsDifferentSourcesIndependently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapped := newBlockingIngressBackend()
	backend, err := newSourceIngressBackend(ctx, wrapped, 2)
	if err != nil {
		t.Fatalf("newSourceIngressBackend() error = %v", err)
	}
	t.Cleanup(backend.Close)

	manualFirst := callIngressTool(backend, evidenceingestionmcp.ToolSubmitManualEvidence, `{}`)
	assertIngressStarted(t, wrapped.started, evidenceingestionmcp.ToolSubmitManualEvidence)
	manualSecond := callIngressTool(backend, evidenceingestionmcp.ToolRunLocalOllamaExtractor, `{}`)
	assertIngressQueued(t, backend.lanes[sourceIngressManualText].jobs, 1)

	git := callIngressTool(backend, evidenceingestionmcp.ToolObserveGitRepositoryChange, `{}`)
	assertIngressStarted(t, wrapped.started, evidenceingestionmcp.ToolObserveGitRepositoryChange)

	wrapped.releaseTool(evidenceingestionmcp.ToolObserveGitRepositoryChange)
	assertIngressResult(t, git)
	wrapped.releaseTool(evidenceingestionmcp.ToolSubmitManualEvidence)
	assertIngressResult(t, manualFirst)
	assertIngressStarted(t, wrapped.started, evidenceingestionmcp.ToolRunLocalOllamaExtractor)
	wrapped.releaseTool(evidenceingestionmcp.ToolRunLocalOllamaExtractor)
	assertIngressResult(t, manualSecond)
}

func TestSourceIngressBackendRoutesTextSourceBySourceSystem(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments string
		want      sourceIngressLaneKey
	}{
		{name: "default manual", arguments: `{}`, want: sourceIngressManualText},
		{name: "manual", arguments: `{"source_system":"manual_text"}`, want: sourceIngressManualText},
		{name: "code file", arguments: `{"source_system":"code_file"}`, want: sourceIngressCodeFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, routed := sourceIngressLaneForTool(evidenceingestionmcp.ToolSubmitTextSource, json.RawMessage(tc.arguments))
			if !routed || got != tc.want {
				t.Fatalf("sourceIngressLaneForTool() = %q, %t, want %q, true", got, routed, tc.want)
			}
		})
	}
}

func TestSourceIngressBackendRoutesGenerationActivationThroughRepositoryLane(t *testing.T) {
	got, routed := sourceIngressLaneForTool(
		evidenceingestionmcp.ToolActivateRepositorySourceGeneration,
		json.RawMessage(`{"request_id":"activate-1","source_generation_id":"generation:1"}`),
	)
	if !routed || got != sourceIngressCodeRepository {
		t.Fatalf("sourceIngressLaneForTool() = %q, %t, want %q, true", got, routed, sourceIngressCodeRepository)
	}
	if got, routed := sourceIngressLaneForTool(
		evidenceingestionmcp.ToolListRepositorySourceGenerations,
		json.RawMessage(`{"repo_id":"repo","extractor_name":"repository-go-parser-code-fact","limit":10}`),
	); routed {
		t.Fatalf("read-only generation discovery routed through source lane %q", got)
	}
}

func TestSourceIngressBackendCloseCancelsActiveCall(t *testing.T) {
	wrapped := newBlockingIngressBackend()
	backend, err := newSourceIngressBackend(context.Background(), wrapped, 1)
	if err != nil {
		t.Fatalf("newSourceIngressBackend() error = %v", err)
	}

	result := callIngressTool(backend, evidenceingestionmcp.ToolSubmitManualEvidence, `{}`)
	assertIngressStarted(t, wrapped.started, evidenceingestionmcp.ToolSubmitManualEvidence)
	closed := make(chan struct{})
	go func() {
		backend.Close()
		close(closed)
	}()
	select {
	case got := <-result:
		if got.err == nil {
			t.Fatal("source ingress call survived backend close")
		}
	case <-time.After(time.Second):
		t.Fatal("active source ingress call was not cancelled")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("SourceIngressBackend.Close() did not return")
	}
}

type recordingIngressBackend struct{}

func (*recordingIngressBackend) Tools() []mcpstdio.Tool {
	return nil
}

func (*recordingIngressBackend) CallTool(_ context.Context, _ string, arguments json.RawMessage) (json.RawMessage, error) {
	return arguments, nil
}

type blockingIngressBackend struct {
	started  chan string
	mu       sync.Mutex
	calls    int
	releases map[string]chan struct{}
}

func newBlockingIngressBackend() *blockingIngressBackend {
	return &blockingIngressBackend{
		started:  make(chan string, 3),
		releases: make(map[string]chan struct{}),
	}
}

func (*blockingIngressBackend) Tools() []mcpstdio.Tool {
	return nil
}

func (b *blockingIngressBackend) CallTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.calls++
	release := b.releases[name]
	if release == nil {
		release = make(chan struct{}, 1)
		b.releases[name] = release
	}
	b.mu.Unlock()
	b.started <- name
	select {
	case <-release:
		return arguments, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingIngressBackend) releaseTool(name string) {
	b.mu.Lock()
	release := b.releases[name]
	b.mu.Unlock()
	release <- struct{}{}
}

type ingressCallResult struct {
	payload json.RawMessage
	err     error
}

func callIngressTool(backend *SourceIngressBackend, name, arguments string) <-chan ingressCallResult {
	result := make(chan ingressCallResult, 1)
	go func() {
		payload, err := backend.CallTool(context.Background(), name, json.RawMessage(arguments))
		result <- ingressCallResult{payload: payload, err: err}
	}()
	return result
}

func assertIngressStarted(t *testing.T, started <-chan string, want string) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("started tool = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("tool %s did not start", want)
	}
}

func assertIngressQueued(t *testing.T, jobs chan sourceIngressCall, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(jobs) != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(jobs); got != want {
		t.Fatalf("queued source calls = %d, want %d", got, want)
	}
}

func assertIngressResult(t *testing.T, result <-chan ingressCallResult) {
	t.Helper()
	select {
	case got := <-result:
		if got.err != nil || string(got.payload) != `{}` {
			t.Fatalf("source ingress result = %s, %v", got.payload, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("source ingress call did not complete")
	}
}
