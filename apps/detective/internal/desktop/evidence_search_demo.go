package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

// LoadEvidenceSearchDemo displays one fixed synthetic example, not a query run.
// It neither changes source/Brief work nor saves or executes the displayed data.
func (s *Service) LoadEvidenceSearchDemo(scenario string) (State, error) {
	ctx, done, err := s.begin(context.Background(), "evidence_search_demo")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	if scenario != "mixed" && scenario != "empty" {
		return s.finish(done, errors.New("unknown evidence search demo"), "離線搜尋示範只接受 mixed 或 empty 固定案例；沒有處理自由問題或建立連線。")
	}
	view, err := evidenceSearchDemo(scenario)
	if err != nil {
		return s.finish(done, err, "離線搜尋示範無法顯示；未建立連線。")
	}
	return s.finishEvidenceSearch(ctx, done, view, "已載入固定合成搜尋示範；未實查 MCP 或 DB，也未使用問題計算結果。既有來源、批次與審查未改動。")
}

func evidenceSearchDemo(scenario string) (*EvidenceSearchView, error) {
	request := EvidenceSearchRequest{Question: "維護 影響？", LifecycleScope: "active", Limit: 20}
	policy := &ahemcp.PracticalHanTermPolicy{Version: "han-auxiliary-anchor-v1",
		AuxiliaryTerms: []string{"影響"}, EligibleAnchorTerms: []string{"維護"}}
	matches := []any{}
	if scenario == "mixed" {
		for i, item := range []struct{ outcome, statement, quote, authority string }{
			{"admitted", "港灣中繼站的維護讓訂閱通知延後十分鐘。", "維護期間，港灣中繼站的訂閱通知延後十分鐘；查詢服務維持可用。", "admitted"},
			{"rejected", "港灣中繼站的維護讓全部服務停止。", "維護期間，港灣中繼站的訂閱通知延後十分鐘；查詢服務維持可用。", "not_admitted"},
			{"audit_only", "港灣中繼站的維護紀錄未列出通知恢復時間。", "維護紀錄未列出訂閱通知的恢復時間。", "not_admitted"},
			{"pending", "港灣中繼站已排定下次維護的通知窗口。", "港灣中繼站已排定下次維護的通知窗口，細節另行公布。", "not_admitted"},
		} {
			original := map[string]string{"span_id": "span:demo-original-" + strconv.Itoa(i+1), "quoted_text": item.quote}
			excerpt := map[string]string{"span_id": "span:demo-search-context", "quoted_text": "維護影響說明：本頁列出港灣中繼站的通知排程；其他站台不在此紀錄範圍。"}
			matches = append(matches, map[string]any{
				"record_ref":     map[string]string{"kind": "proposal", "id": "occ:desktop-search-demo-" + strconv.Itoa(i+1)},
				"statement_text": item.statement, "admission_outcome": item.outcome,
				"record_state": map[string]string{"authority_status": item.authority, "record_lifecycle": "active"},
				"source_refs":  []any{original},
				"retrieval_basis": map[string]any{"baseline_matched": true, "surfaces": []any{
					map[string]any{"surface": "proposal.statement_text", "status": "matched", "matched_terms": []string{"維護"}, "missing_terms": []string{"影響"}, "matched_spans": []any{}, "spans_truncated": false},
					map[string]any{"surface": "extraction_views.rendered_content", "status": "matched", "matched_terms": []string{"維護", "影響"}, "missing_terms": []string{}, "matched_spans": []any{map[string]any{"source_ref": excerpt}}, "spans_truncated": false},
				}},
			})
		}
	} else {
		request.Question = "磁暴 影響？"
		policy.EligibleAnchorTerms = []string{"磁暴"}
	}
	raw, err := json.Marshal(map[string]any{
		"schema_version": ahemcp.PracticalQuerySchema, "query": request.Question,
		"demo": true, "demo_notice": "固定合成 UI 示範；不是 MCP 或 DB 查詢，也不是實際人工決定。",
		"query_execution": map[string]any{
			"query_mode": ahemcp.PracticalQueryMode, "plan_version": "practical-multisurface-lexical-v2", "original_query": request.Question,
			"limit": request.Limit, "candidate_count": len(matches), "truncated": false, "global_absence_inference_allowed": false,
			"filters":            map[string]string{"source_id": "", "admission_outcome": "", "lifecycle_scope": request.LifecycleScope},
			"multisurface":       map[string]int{"excluded_source_views": 0},
			"practical_recovery": map[string]any{"fallback_attempted": false, "configured_minimum_english_terms": 2, "han_term_policy": policy},
		},
		"counts": map[string]int{"returned_matches": len(matches)}, "matches": matches,
		"source_scopes": []any{map[string]string{"source_id": "synthetic-harbor-relay", "scope": "固定合成示範，非真實資料源"}},
		"limitations":   []string{"四種處置均為合成標籤，不是此畫面的審查操作。", "搜尋命中片段不是新增原引用，不能用來擴張既有主張。", "空結果不代表資料不存在。"},
	})
	if err != nil {
		return nil, err
	}
	return &EvidenceSearchView{Request: request, RawJSON: string(raw), ReturnedMatches: len(matches), HanTermPolicy: policy, Demo: true}, nil
}
