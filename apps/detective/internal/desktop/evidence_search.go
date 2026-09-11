package desktop

import (
	"context"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

// EvidenceSearchRequest selects existing material, not a new review decision.
// The launcher is deliberately absent: only saved Settings can select it.
type EvidenceSearchRequest struct {
	Question         string `json:"question"`
	SourceID         string `json:"sourceID"`
	AdmissionOutcome string `json:"admissionOutcome"`
	LifecycleScope   string `json:"lifecycleScope"`
	Limit            int    `json:"limit"`
}

// EvidenceSearchView is session-only retrieval data, never admission authority.
// RawJSON is a string to preserve native numbers and unknown fields unchanged
// across State snapshots and the JavaScript bridge.
type EvidenceSearchView struct {
	Request           EvidenceSearchRequest          `json:"request"`
	RawJSON           string                         `json:"rawJSON"`
	ReturnedMatches   int                            `json:"returnedMatches"`
	FallbackAttempted bool                           `json:"fallbackAttempted"`
	Truncated         bool                           `json:"truncated"`
	HanTermPolicy     *ahemcp.PracticalHanTermPolicy `json:"hanTermPolicy,omitempty"`
	Demo              bool                           `json:"demo"`
}

type evidenceSearcher func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error)

// SearchEvidence performs one explicit read-only request without model, intake,
// review or automatic retry. Search results are never saved to the workspace.
func (s *Service) SearchEvidence(ctx context.Context, request EvidenceSearchRequest) (State, error) {
	return s.searchEvidence(ctx, request, ahemcp.SearchPractical)
}

func (s *Service) searchEvidence(ctx context.Context, request EvidenceSearchRequest, search evidenceSearcher) (State, error) {
	ctx, done, err := s.begin(ctx, "evidence_search")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	if search == nil {
		return s.finish(done, errors.New("missing evidence search adapter"), "唯讀搜尋無法啟動；未建立連線。")
	}
	settings := s.Snapshot().Settings
	if settings.Mode != "local" || settings.QueryLauncher == "" {
		return s.finish(done, errors.New("query configuration required"), "唯讀證據搜尋需已保存的本機模式及 Query launcher；未建立連線，不需要模型。")
	}
	result, err := search(ctx, settings.QueryLauncher, ahemcp.PracticalQueryInput{
		Question: request.Question, SourceID: request.SourceID,
		AdmissionOutcome: request.AdmissionOutcome, LifecycleScope: request.LifecycleScope, Limit: request.Limit,
	})
	if err != nil {
		if ctx.Err() != nil {
			return s.finish(done, ctx.Err(), "唯讀查詢已取消或逾時；沒有可接受的新結果，也未發出入庫或審查操作。")
		}
		return s.finish(done, err, "唯讀查詢未完成；請核對問題、篩選條件與已保存的 Query launcher，並確認 MCP 支援 practical_multisurface_lexical_v1／grounded-evidence-brief-v7（舊版需升級）。沒有接受結果、重試、降級、入庫或審查。")
	}
	view := &EvidenceSearchView{Request: request, RawJSON: string(result.Response),
		ReturnedMatches: result.ReturnedMatches, FallbackAttempted: result.FallbackAttempted,
		Truncated: result.Truncated, HanTermPolicy: result.HanTermPolicy}
	return s.finishEvidenceSearch(ctx, done, view, "唯讀查詢已完成；這是既有材料，不是答案或新待審。空結果不代表資料不存在；不相關材料可略過，不需要 reject。")
}

func (s *Service) finishEvidenceSearch(ctx context.Context, done func(), view *EvidenceSearchView, detail string) (State, error) {
	// Detach adapter-owned metadata before taking the completion lock.
	detached := cloneState(State{Search: view}).Search
	s.mu.Lock()
	err := ctx.Err()
	if err == nil && s.closed {
		err = errors.New("desktop service is closed")
	}
	if err != nil {
		s.mu.Unlock()
		return s.finish(done, err, "唯讀查詢已取消或逾時；沒有可接受的新結果，也未發出入庫或審查操作。")
	}
	// Publication and completion have one ordering point with Cancel and Close.
	// A later Cancel is a no-op; done still releases the context and Busy below.
	s.state.Search = detached
	s.eventLocked(s.state.Operation, "completed", detail)
	s.cancel = nil
	s.mu.Unlock()
	done()
	return s.Snapshot(), nil
}
