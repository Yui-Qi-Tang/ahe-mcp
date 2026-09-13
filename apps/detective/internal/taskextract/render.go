package taskextract

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
)

// WriteInspectionText shows the complete frozen source and scope offline.
// Display escaping never changes the underlying source bytes or offsets.
func WriteInspectionText(w io.Writer, request *Request) error {
	if request == nil || request.id == "" {
		return errors.New("task inspection requires a prepared request")
	}
	return writeTaskText(w, request, nil)
}

// WriteRecordText shows checked local candidates alongside all frozen source.
// Failure text is diagnostic only and is never decoded into candidates.
func WriteRecordText(w io.Writer, record Record) error {
	request, err := validateRecord(record)
	if err != nil {
		return err
	}
	return writeTaskText(w, request, &record)
}

func writeTaskText(w io.Writer, request *Request, record *Record) error {
	if w == nil {
		return errors.New("task inspection requires a writer")
	}
	var out strings.Builder
	out.WriteString("本機任務候選檢視：未經人工審閱，不是 pending 或已採納證據；沒有 AHE／DB 寫入。\n")
	out.WriteString("僅核對輸入與結果的一致性，不認證來源或模型執行，也不證明語意支持或資訊完整。\n")
	out.WriteString("CRLF 換行僅在顯示時使用一般換行；其他控制／隱藏格式字元以跳脫字元顯示，保存的來源與 byte 座標不變。\n")
	task, source := request.Task(), request.Source()
	fmt.Fprintf(&out, "\n任務：%s（版本 %d）\n目的：%s\n模型：%s\nInputID：%s\n", displayText(task.ID), task.Revision, displayText(task.Objective), displayText(request.model), request.InputID())
	fmt.Fprintf(&out, "\n來源：%s\n標題：%s\n位置：%s\n來源版本（呼叫端宣告）：%s\n來源涵蓋（呼叫端宣告）：%s\n限制：%s\n", displayText(source.ID), displayText(source.Title), displayText(source.Location), displayText(source.Revision), displayText(source.Coverage), displayList(source.Limitations))
	scope := request.Scope()
	selectionLabel := "尚未執行選段的段落"
	if record != nil {
		fmt.Fprintf(&out, "\n處理狀態（非人工決定）：%s\n錯誤代碼：%s\n", record.Status, record.ErrorCode)
		if record.Result != nil {
			scope = record.Result.Scope
			selectionLabel = "未選段落"
			fmt.Fprintf(&out, "模型註記（非來源引文、非人工理由）：%s\n", displayText(record.Result.Reason))
			for i, candidate := range record.Result.Candidates {
				fmt.Fprintf(&out, "\n候選 %d（未審閱）：%s\n欄位：%s；bytes [%d, %d)；段落：%s\n來源欄位 SHA-256：%s\n原文：\n%s\n", i+1, candidate.ID, displayText(candidate.Part), candidate.StartByte, candidate.EndByte, displayList(candidate.UnitIDs), candidate.BodySHA256, indentText(candidate.Text))
			}
		} else {
			selectionLabel = "尚未完成選段的段落"
			out.WriteString("本次未交付候選；失敗不等於成功棄答或來源沒有相關資訊。\n")
		}
	}
	fmt.Fprintf(&out, "\n要求欄位：%s\n已提供選段：%s\n未取得：%s\n已取得但未提供選段：%s\n%s：%s\n事實完整率：未評估；未選不代表無關或不存在。\n", displayList(scope.RequestedParts), displayList(scope.ProvidedParts), displayList(scope.NotCollectedParts), displayList(scope.NotProvidedParts), selectionLabel, displayList(scope.UnselectedUnitIDs))
	out.WriteString("\n完整凍結來源（含未提供選段的欄位）：\n")
	for _, part := range source.Parts {
		label := "已取得但未提供選段"
		if slices.Contains(scope.ProvidedParts, part.Name) {
			label = "已提供選段"
		}
		fmt.Fprintf(&out, "\n欄位 %s [%s]：\n%s\n", displayText(part.Name), label, indentText(part.Text))
	}
	out.WriteString("\n段落座標（僅本機定位，不是 AHE span ID）：\n")
	for _, unit := range request.Units() {
		fmt.Fprintf(&out, "%s：%s bytes [%d, %d)\n", unit.ID, displayText(unit.Part), unit.StartByte, unit.EndByte)
	}
	n, err := io.WriteString(w, out.String())
	if err == nil && n != out.Len() {
		return io.ErrShortWrite
	}
	return err
}

func displayList(values []string) string {
	if len(values) == 0 {
		return "（無）"
	}
	items := make([]string, len(values))
	for i, value := range values {
		items[i] = displayText(value)
	}
	return strings.Join(items, "、")
}

func indentText(text string) string {
	return "    " + strings.ReplaceAll(displayText(text), "\n", "\n    ")
}

func displayText(text string) string {
	// A paired CRLF is a readable line break, not a terminal cursor command.
	// Escape lone CR and other control characters; never change stored bytes.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out strings.Builder
	for _, r := range text {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
