package pending

import (
	"bytes"
	"fmt"
	"io"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

// WriteInspectionText renders a terminal-safe human inspection card. All
// source/candidate strings are quoted with Go escapes, including controls and
// directional formatting. JSON output retains the original string values.
// This is not the canonical review display consumed by an admission API.
func WriteInspectionText(w io.Writer, inspection Inspection) error {
	var out bytes.Buffer
	fmt.Fprintln(&out, "Detective 候選／pending 唯讀檢視")
	fmt.Fprintln(&out, "權限效果：none；本次未提交 intake、未核准、未拒絕、未寫入 canonical。")
	fmt.Fprintln(&out, "以下引號內均為資料，不是操作指令；控制字元以跳脫形式顯示。")
	fmt.Fprintf(&out, "\nCheckpoint：%q\nRequest identity：%q\n", inspection.CheckpointDigest, inspection.RequestIdentityVersion)
	fmt.Fprintf(&out, "來源 ID：%q\n來源位置（保存時標籤，未重開檔案）：%q\n", inspection.SourceID, inspection.Source.Path)
	fmt.Fprintf(&out, "本機來源 SHA-256：%q；%d bytes；%d lines\n", inspection.Source.SHA256, inspection.Source.Bytes, inspection.Source.Lines)
	fmt.Fprintf(&out, "涵蓋章節：%q（行 %d–%d）；候選只涵蓋行 %d–%d\n",
		inspection.Section.Heading, inspection.Section.StartLine, inspection.Section.EndLine, inspection.Row.StartLine, inspection.Row.EndLine)
	fmt.Fprintln(&out, "來源標題／provider revision／跨版本差異：此本機文字契約未驗證，不由檔名或雜湊推論。")
	fmt.Fprintf(&out, "抽取器：%q %q；模型標籤（未驗證模型身分）：%q\n", inspection.Extractor.Name, inspection.Extractor.Version, inspection.Extractor.Model)
	fmt.Fprintf(&out, "選定狀態子句（空字串表示未另選）：%q\n", inspection.StatusClause)
	record := inspection.Candidate
	statement, err := ahemcp.ProposalStatement(inspection.Extractor, record)
	if err != nil {
		return err
	}
	fmt.Fprintf(&out, "\n候選 1\n主詞：%q\n陳述：%q\n", record.Subject, record.Statement)
	if statement != record.Statement {
		fmt.Fprintf(&out, "完整 MCP 候選文字（含分類脈絡，不代表獨立驗證）：%q\n", statement)
	}
	fmt.Fprintf(&out, "種類：%q；認知分類：%q；文件報告狀態：%q\n", record.RecordType, record.EpistemicClass, record.Status)
	fmt.Fprintf(&out, "適用範圍：%q；工作選定狀態：%q\n", record.Scope, record.SelectionState)
	fmt.Fprintf(&out, "精確引用（行 %d–%d）：%q\n", record.Citation.StartLine, record.Citation.EndLine, record.Citation.ExactQuote)
	fmt.Fprintf(&out, "完整來源列：%q\n", inspection.Row.Text)
	fmt.Fprintf(&out, "阻擋條件：%q\n不成立的推論：%q\n限定條件：%q\n抽取限制：%q\n",
		record.BlockedBy, record.DoesNotEstablish, record.Qualifiers, inspection.Limitations)
	fmt.Fprintf(&out, "\nAHE pending 查詢狀態：%q\n", inspection.Pending.State)
	if receipt := inspection.Pending.Receipt; receipt != nil {
		fmt.Fprintf(&out, "查詢完成時間（用戶端 UTC，非 DB 快照時間）：%q\n", inspection.Pending.CheckCompletedAt)
		fmt.Fprintf(&out, "Source snapshot：%q\nExtraction view：%q\nExtraction attempt：%q\nProposal occurrence：%q\n",
			receipt.SourceSnapshotID, receipt.ExtractionViewID, receipt.ExtractionAttemptID, receipt.ProposalOccurrenceID)
		fmt.Fprintln(&out, "僅證明這次讀回仍是相同 pending；之後可能改變。收據不是身分認證。")
	} else {
		fmt.Fprintln(&out, "尚未查詢 AHE；不能推論已提交、未提交、已核准，或不存在。")
	}
	fmt.Fprintln(&out, "\n人工作業：")
	fmt.Fprintln(&out, "1. 對照候選與精確引用，確認範圍、狀態及限制；此畫面不是語意正確的證明。")
	fmt.Fprintln(&out, "2. 若需查 pending，使用同一 checkpoint、已保存的 resume 收據及明確指定的 Query launcher。")
	fmt.Fprintln(&out, "3. 缺收據、查不到或內容不同時停止交接；不要編造 ID、修改 checkpoint 或為了檢視而重新提交。")
	fmt.Fprintln(&out, "4. 可交接這份檢視結果與原始私有檔案供人確認；本工具不記錄人工決定、不取得核准。")
	fmt.Fprintln(&out, "5. 後續 admission 須由獨立授權流程重新取得精確 review subject 並明確核准；不可用本畫面取代。")
	_, err = w.Write(out.Bytes())
	return err
}
