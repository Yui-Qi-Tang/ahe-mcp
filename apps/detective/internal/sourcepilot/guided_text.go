package sourcepilot

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// WriteGuidedText presents complete source paragraphs and an explicit human
// reading checklist. It does not record a review decision or execute markup.
func WriteGuidedText(w io.Writer, report GuidedReport) error {
	return writeGuidedText(w, report, false)
}

func writeGuidedText(w io.Writer, report GuidedReport, fixed bool) error {
	var out strings.Builder
	if fixed {
		fmt.Fprintln(&out, "固定主張查證｜caller_supplied_unverified，不是人工核准")
		fmt.Fprintln(&out, "未執行摘要拆解；未執行獨立內文觀察。此模式最多執行一次固定主張對原文查證。")
	} else {
		fmt.Fprintln(&out, "摘要引導查證｜待人審閱，不是採納或真實性證明")
	}
	fmt.Fprintf(&out, "階段：%s\n來源：%s\n版本：%s\n來源連結（僅文字，不開啟）：%s\n觀測時間：%s\n摘要提供者：%s\n宣告涵蓋：%s\n",
		guidedDisplay(report.Stage), guidedDisplay(report.Source.SourceID), guidedDisplay(report.Source.SourceRevision), guidedDisplay(report.Source.SourceURL), guidedDisplay(report.Source.ObservedAt), guidedDisplay(report.Source.SummaryOrigin), guidedDisplay(report.Source.Coverage))
	fmt.Fprintf(&out, "原文 SHA-256：%s\n摘要 SHA-256：%s\n", guidedDisplay(report.BodySHA256), guidedDisplay(report.SummarySHA256))
	fmt.Fprintln(&out, "來源座標與摘要配對由輸入者宣告；雜湊只固定內容，不驗證發布者身分。")
	for _, limitation := range report.Source.Limitations {
		fmt.Fprintf(&out, "來源限制：%s\n", guidedDisplay(limitation))
	}
	fmt.Fprintf(&out, "\n外部摘要（待核對，不是答案）\n%s\n", guidedDisplay(report.Source.Summary))
	if fixed {
		fmt.Fprintln(&out, "\n呼叫者提供的主張與模型查證建議（不保證等同原摘要或已拆成原子主張）")
	} else {
		fmt.Fprintln(&out, "\n摘要主張與模型查證建議")
	}
	if report.ClaimsReason != "" {
		fmt.Fprintf(&out, "未列出主張的理由：%s\n", guidedDisplay(report.ClaimsReason))
	}
	for _, claim := range report.Claims {
		fmt.Fprintf(&out, "\n主張 %d：%s\n", claim.Number, guidedDisplay(claim.Statement.Text))
		if !fixed {
			guidedReadingAid(&out, claim.Statement)
		}
		found := false
		for _, assessment := range report.Assessments {
			if assessment.Claim != claim.Number {
				continue
			}
			found = true
			label := map[string]string{"supported": "原文支持（模型建議）", "contradicted": "原文明確衝突（模型建議）", "insufficient": "資料不足／歧義（模型建議）"}[assessment.Relation]
			if label == "" {
				label = "未識別的建議，停止交接"
			}
			fmt.Fprintf(&out, "%s：%s\n", label, guidedDisplay(assessment.Explanation))
			guidedQuotes(&out, assessment.Citations)
		}
		if !found {
			fmt.Fprintln(&out, "尚無完成的查證建議。")
		}
	}
	if !fixed {
		fmt.Fprintln(&out, "\n不看摘要的內文觀察（最多四筆，不代表完整涵蓋，也不自動標為摘要遺漏）")
		if report.ObservationsReason != "" {
			fmt.Fprintf(&out, "未列出觀察的理由：%s\n", guidedDisplay(report.ObservationsReason))
		}
		for i, observation := range report.Observations {
			fmt.Fprintf(&out, "\n觀察 %d：%s\n", i+1, guidedDisplay(observation.Statement.Text))
			guidedReadingAid(&out, observation.Statement)
			guidedQuotes(&out, observation.Citations)
		}
	}
	guidedInventoryText(&out, report, fixed)
	fmt.Fprintln(&out, "\n完整內文（本次實際提供的範圍；未按模型選取結果刪減）")
	fmt.Fprintln(&out, guidedDisplay(report.Source.Body))
	fmt.Fprintln(&out, "\n人工確認清單（本命令不記錄核准）")
	fmt.Fprintln(&out, "[ ] 每個陳述句都能獨立理解：誰、做了什麼或處於什麼狀態、對象或結果；不強迫不及物句有受詞。")
	fmt.Fprintln(&out, "[ ] 原文上下文足夠；代名詞有所指，條件、時間、否定、部分範圍與未知都沒有被改掉。")
	fmt.Fprintln(&out, "[ ] 引用支持的是這個完整主張；相同關鍵字、前後發生或模型信心都不是證明。")
	fmt.Fprintln(&out, "[ ] 已核對其他段落的反例、限制與摘要外資訊；來源缺漏仍明確保留。")
	fmt.Fprintln(&out, "這裡沒有刪字、最短證據或必要性搜尋；段落引用是一組閱讀依據，不是因果證明。")
	fmt.Fprintln(&out, "可讀性與語意都仍待人審。supported/contradicted/insufficient 不是 admit/reject/audit_only。")
	fmt.Fprintln(&out, "完整句的字面檢查不是文法或原子主張證明；請逐句確認，而非相信欄位標籤。")
	fmt.Fprintln(&out, "未寫入 AHE、pending 或 DB；本機資料包不能取代 Core 的正式 review subject 與 admission。")
	if report.Stage != "complete" && report.Stage != "inspected" {
		fmt.Fprintln(&out, "本次流程未完成；保留原件與已完成階段供查錯，不得當成完整查證結果。")
	}
	n, err := io.WriteString(w, out.String())
	if err == nil && n != out.Len() {
		return io.ErrShortWrite
	}
	return err
}

func guidedReadingAid(out *strings.Builder, statement ReadableStatement) {
	fmt.Fprintf(out, "模型文法提示（未驗證）：主體＝%s；動作／狀態＝%s；對象／補語＝%s\n", guidedDisplay(statement.Subject), guidedDisplay(statement.Predicate), guidedDisplay(statement.Object))
	if statement.Object != "" && statement.Subject == statement.Object {
		fmt.Fprintln(out, "請核對：主體與對象欄位相同，可能是文法提示填錯；這只是提醒，不自動判定句子錯誤。")
	}
}

func guidedInventoryText(out *strings.Builder, report GuidedReport, fixed bool) {
	fmt.Fprintln(out, "\n原文段落引用清單（程式計算，不是語意涵蓋率）")
	fmt.Fprintln(out, "未引用不等於不重要、不支持或已被忽略；已引用也不代表整段資訊已寫進主張。請合看完整上下文。")
	if report.Stage != "complete" {
		fmt.Fprintln(out, "未完成或未執行的階段可能尚無引用；不能推論模型已經查過。")
	}
	for _, row := range GuidedReferences(report) {
		check := "未引用"
		if len(row.Claims) != 0 {
			check = fmt.Sprint(row.Claims)
		}
		observation := "未引用"
		if fixed {
			observation = "未執行"
		} else if len(row.Observations) != 0 {
			observation = fmt.Sprint(row.Observations)
		}
		fmt.Fprintf(out, "\n段落 %d｜查證主張：%s｜獨立觀察：%s\n", row.Segment.Number, check, observation)
		guidedQuotes(out, []Segment{row.Segment})
	}
}

func guidedQuotes(out *strings.Builder, quotes []Segment) {
	for _, quote := range quotes {
		fmt.Fprintf(out, "原文段 %d（decoded body UTF-8 bytes [%d,%d)）：\n%s\n", quote.Number, quote.StartByte, quote.EndByte, guidedDisplay(quote.Text))
	}
}

// Defensive display escaping is separate from the byte-preserved source.
func guidedDisplay(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch {
		case r == '\n':
			out.WriteRune(r)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			fmt.Fprintf(&out, "\\u%04x", r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
