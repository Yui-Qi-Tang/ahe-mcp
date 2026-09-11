package desktop

import (
	"context"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// extractionFailureText projects error identity into fixed public diagnostics.
// Never include upstream errors, source text, paths, or model output here.
func extractionFailureText(err error) string {
	if errors.Is(err, context.Canceled) {
		return "[cancelled] 抽取已取消。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "[timeout] 模型抽取逾時。"
	}
	var failure modelFailure
	if errors.As(err, &failure) {
		messages := map[modelFailure]string{
			"model_connection":      "無法完成本機模型連線；請確認模型服務仍在執行。",
			"model_response_size":   "模型回應無法讀取，或超過大小限制。",
			"model_http":            "模型服務回傳失敗狀態；未自動重試。",
			"model_response_format": "模型服務回應不符合 Responses 格式，或回傳錯誤。",
			"model_incomplete":      "模型回應尚未完整完成；未使用部分內容。",
			"model_mismatch":        "模型回應名稱與設定不符。",
			"model_output_kind":     "模型回應含工具呼叫或不支援的輸出類型；未執行工具。",
			"model_no_final_text":   "模型沒有最終文字；reasoning 區段不能當成抽取結果。",
		}
		if message, ok := messages[failure]; ok {
			return "[" + string(failure) + "] " + message
		}
	}
	switch {
	case errors.Is(err, labstatus.ErrModelResponse):
		return "[model_response] 模型呼叫或回應處理失敗；未自動重試。"
	case errors.Is(err, labstatus.ErrModelJSON):
		return "[model_json] 模型最終結果不是符合契約的完整 JSON。"
	case errors.Is(err, labstatus.ErrCitation):
		return "[citation] 模型引用無法對應選定來源的精確行數。"
	case errors.Is(err, labstatus.ErrCandidateValidation):
		return "[candidate_validation] 候選的欄位、狀態或敘述未通過校驗。"
	default:
		return "[extraction_failed] 抽取未完成或未通過校驗。"
	}
}
