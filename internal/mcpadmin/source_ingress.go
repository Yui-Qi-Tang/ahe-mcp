package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
)

type sourceIngressLaneKey string

const (
	sourceIngressManualText     sourceIngressLaneKey = evidenceingestion.SourceSystemManualText
	sourceIngressCodeFile       sourceIngressLaneKey = evidenceingestion.SourceSystemCodeFile
	sourceIngressCodeRepository sourceIngressLaneKey = evidenceingestion.SourceSystemCodeRepository
)

type sourceIngressCall struct {
	ctx       context.Context
	name      string
	arguments json.RawMessage
	result    chan sourceIngressResult
}

type sourceIngressResult struct {
	payload json.RawMessage
	err     error
}

type sourceIngressLane struct {
	jobs chan sourceIngressCall
}

// SourceIngressBackend applies bounded, source-local backpressure before database-backed ingestion.
type SourceIngressBackend struct {
	backend mcpstdio.Backend
	ctx     context.Context
	cancel  context.CancelFunc
	lanes   map[sourceIngressLaneKey]*sourceIngressLane
	wg      sync.WaitGroup
	once    sync.Once
}

// NewSourceIngressBackend creates one buffered lane per supported source system.
func NewSourceIngressBackend(ctx context.Context, backend mcpstdio.Backend) (*SourceIngressBackend, error) {
	return newSourceIngressBackend(ctx, backend, runtime.NumCPU())
}

func newSourceIngressBackend(ctx context.Context, backend mcpstdio.Backend, bufferSize int) (*SourceIngressBackend, error) {
	if ctx == nil {
		return nil, errors.New("source ingress context is required")
	}
	if backend == nil {
		return nil, errors.New("source ingress backend is required")
	}
	if bufferSize < 1 {
		return nil, errors.New("source ingress buffer size must be positive")
	}

	lifecycle, cancel := context.WithCancel(ctx)
	b := &SourceIngressBackend{
		backend: backend,
		ctx:     lifecycle,
		cancel:  cancel,
		lanes: map[sourceIngressLaneKey]*sourceIngressLane{
			sourceIngressManualText:     {jobs: make(chan sourceIngressCall, bufferSize)},
			sourceIngressCodeFile:       {jobs: make(chan sourceIngressCall, bufferSize)},
			sourceIngressCodeRepository: {jobs: make(chan sourceIngressCall, bufferSize)},
		},
	}
	for _, lane := range b.lanes {
		b.wg.Add(1)
		go b.runLane(lane)
	}
	return b, nil
}

// Close stops all source ingress lanes.
func (b *SourceIngressBackend) Close() {
	b.once.Do(func() {
		b.cancel()
		b.wg.Wait()
	})
}

// Tools delegates the deterministic tool list to the wrapped backend.
func (b *SourceIngressBackend) Tools() []mcpstdio.Tool {
	return b.backend.Tools()
}

// CallTool routes source-facing work through its bounded lane and leaves authority to the database-backed backend.
func (b *SourceIngressBackend) CallTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	laneKey, routed := sourceIngressLaneForTool(name, arguments)
	if !routed {
		return b.backend.CallTool(ctx, name, arguments)
	}
	lane := b.lanes[laneKey]
	result := make(chan sourceIngressResult, 1)
	call := sourceIngressCall{
		ctx:       ctx,
		name:      name,
		arguments: append(json.RawMessage(nil), arguments...),
		result:    result,
	}
	select {
	case lane.jobs <- call:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	}
	select {
	case response := <-result:
		return response.payload, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	}
}

func (b *SourceIngressBackend) runLane(lane *sourceIngressLane) {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case call := <-lane.jobs:
			if err := b.ctx.Err(); err != nil {
				return
			}
			if err := call.ctx.Err(); err != nil {
				call.result <- sourceIngressResult{err: err}
				continue
			}
			callCtx, cancel := context.WithCancel(call.ctx)
			stop := context.AfterFunc(b.ctx, cancel)
			payload, err := b.backend.CallTool(callCtx, call.name, call.arguments)
			stop()
			cancel()
			call.result <- sourceIngressResult{payload: payload, err: err}
		}
	}
}

func sourceIngressLaneForTool(name string, arguments json.RawMessage) (sourceIngressLaneKey, bool) {
	switch name {
	case evidenceingestionmcp.ToolSubmitManualEvidence,
		evidenceingestionmcp.ToolRunLocalOllamaExtractor:
		return sourceIngressManualText, true
	case evidenceingestionmcp.ToolSubmitTextSource:
		var input struct {
			SourceSystem string `json:"source_system"`
		}
		if json.Unmarshal(arguments, &input) == nil && input.SourceSystem == evidenceingestion.SourceSystemCodeFile {
			return sourceIngressCodeFile, true
		}
		return sourceIngressManualText, true
	case evidenceingestionmcp.ToolRunGoParserExtractor,
		evidenceingestionmcp.ToolRunGoplsExtractor:
		return sourceIngressCodeFile, true
	case evidenceingestionmcp.ToolInspectGoplsWorkspace,
		evidenceingestionmcp.ToolInspectGitRepositoryChange,
		evidenceingestionmcp.ToolObserveGitRepositoryChange,
		evidenceingestionmcp.ToolScheduleGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolClaimGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRenewGitRepositoryExtractionWorkLease,
		evidenceingestionmcp.ToolExecuteClaimedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRunGitRepositoryExtractionWorkerTick,
		evidenceingestionmcp.ToolFinishGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRecoverExpiredGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolRepairExpiredGitRepositoryExtractionWorkExecution,
		evidenceingestionmcp.ToolRetryFailedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolClassifyFailedGitRepositoryExtractionWork,
		evidenceingestionmcp.ToolConsumeDueGitRepositoryExtractionWorkRetryDecision,
		evidenceingestionmcp.ToolRunDueGitRepositoryExtractionWorkRetryControllerTick,
		evidenceingestionmcp.ToolRunExpiredGitRepositoryExtractionWorkMaintenanceTick,
		evidenceingestionmcp.ToolCaptureGitRepositorySnapshot,
		evidenceingestionmcp.ToolCreateRepositoryExtractionRun,
		evidenceingestionmcp.ToolRunRepositoryGoParserExtractor,
		evidenceingestionmcp.ToolRunRepositoryGoplsExtractor,
		evidenceingestionmcp.ToolActivateRepositorySourceGeneration:
		return sourceIngressCodeRepository, true
	default:
		return "", false
	}
}
