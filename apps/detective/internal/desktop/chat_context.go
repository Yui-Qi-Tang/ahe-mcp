package desktop

import (
	"encoding/json"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// These private input types are an allowlist, not copies of UI/storage objects.
// Adding fields to State, SourceView or RowBatch must not expand model access.
type chatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Kind string `json:"kind"`
}

type chatSource struct {
	Kind    string `json:"kind"`
	RawText string `json:"raw_text"`
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
}

type chatCandidate struct {
	Ordinal int        `json:"ordinal"`
	Record  chatRecord `json:"record"`
}

type chatRecord struct {
	RecordType       string       `json:"record_type"`
	Subject          string       `json:"subject"`
	Statement        string       `json:"statement"`
	EpistemicClass   string       `json:"epistemic_class"`
	Status           string       `json:"status"`
	Scope            string       `json:"scope"`
	SelectionState   string       `json:"selection_state"`
	Citation         chatCitation `json:"citation"`
	BlockedBy        []string     `json:"blocked_by"`
	DoesNotEstablish []string     `json:"does_not_establish"`
	Qualifiers       []string     `json:"qualifiers"`
}

type chatCitation struct {
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	ExactQuote string `json:"exact_quote,omitempty"`
}

type chatSection struct {
	Heading   string `json:"heading"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type chatSourceCoordinate struct {
	SHA256           string        `json:"sha256"`
	Bytes            int           `json:"bytes"`
	Lines            int           `json:"lines"`
	SelectedSections []chatSection `json:"selected_sections"`
}

type chatExtraction struct {
	Source  chatSourceCoordinate `json:"source"`
	Section chatSection          `json:"section"`
	Rows    []chatRow            `json:"rows"`
}

type chatRow struct {
	Row    chatSourceRow     `json:"row"`
	Status string            `json:"status"`
	Result *chatCandidateSet `json:"result,omitempty"`
}

type chatSourceRow struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Text      string `json:"text"`
}

type chatCandidateSet struct {
	Outcome          string           `json:"outcome"`
	Records          []chatRecord     `json:"records"`
	Abstentions      []chatAbstention `json:"abstentions"`
	Limitations      []string         `json:"limitations"`
	AbstentionReason string           `json:"abstention_reason"`
}

type chatAbstention struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

func projectChatRecord(record labstatus.Record) chatRecord {
	return chatRecord{RecordType: record.RecordType, Subject: record.Subject,
		Statement: record.Statement, EpistemicClass: record.EpistemicClass,
		Status: record.Status, Scope: record.Scope, SelectionState: record.SelectionState,
		Citation:  chatCitation{StartLine: record.Citation.StartLine, EndLine: record.Citation.EndLine, ExactQuote: record.Citation.ExactQuote},
		BlockedBy: record.BlockedBy, DoesNotEstablish: record.DoesNotEstablish, Qualifiers: record.Qualifiers}
}

func projectChatSection(section labstatus.SourceSection) chatSection {
	return chatSection{Heading: section.Heading, StartLine: section.StartLine, EndLine: section.EndLine}
}

// chatInput preserves semantic text exactly. It does not inspect or scrub text
// for secrets: paths explicitly present in source, messages or quotes are data.
// The caller gets only serialized bytes, so no projected slice can mutate State.
func chatInput(state State) (string, error) {
	input := struct {
		SchemaVersion string          `json:"schema_version"`
		Conversation  []chatMessage   `json:"conversation"`
		Source        *chatSource     `json:"source"`
		Candidates    []chatCandidate `json:"candidates"`
		Extraction    *chatExtraction `json:"extraction_context,omitempty"`
		Limitations   []string        `json:"limitations"`
	}{SchemaVersion: "detective-chat-context/v1",
		Conversation: make([]chatMessage, 0, len(state.Messages)),
		Candidates:   make([]chatCandidate, 0, len(state.Candidates)),
		Limitations: []string{
			"MCP 回覆可能只是程式索引、關係或候選位置，不是原文證據。只能引用實際提供的原文；不可把搜尋摘要或圖關係當成程式內容，也不能用目前片段聲稱 repository 已完整檢查。",
			"來源為已保存的觀測，不證明上游目前狀態、完整性或真實性；候選與引用不等於人工核准或 canonical 採納。",
			"此閱讀輸入未附帶系統設定、私有路徑欄位、收據、工具、操作紀錄或 DB 查詢結果；原文與訊息未自動遮罩。來源分類與抽取列狀態不是目前 DB 狀態，請由人使用介面與明確 Query 查證。",
		}}
	for _, message := range state.Messages {
		input.Conversation = append(input.Conversation, chatMessage{Role: message.Role, Text: message.Text, Kind: message.Kind})
	}
	if source := state.Source; source != nil {
		input.Source = &chatSource{Kind: source.Kind, RawText: source.RawText, SHA256: source.SHA256, Bytes: source.Bytes}
	}
	for _, candidate := range state.Candidates {
		input.Candidates = append(input.Candidates, chatCandidate{Ordinal: candidate.Ordinal, Record: projectChatRecord(candidate.Record)})
	}
	if batch := state.Extraction; batch != nil {
		extraction := &chatExtraction{
			Source:  chatSourceCoordinate{SHA256: batch.Source.SHA256, Bytes: batch.Source.Bytes, Lines: batch.Source.Lines},
			Section: projectChatSection(batch.Section), Rows: make([]chatRow, 0, len(batch.Rows)),
		}
		if batch.Source.SelectedSections != nil {
			extraction.Source.SelectedSections = make([]chatSection, 0, len(batch.Source.SelectedSections))
		}
		for _, section := range batch.Source.SelectedSections {
			extraction.Source.SelectedSections = append(extraction.Source.SelectedSections, projectChatSection(section))
		}
		for _, row := range batch.Rows {
			projected := chatRow{Row: chatSourceRow{StartLine: row.Row.StartLine, EndLine: row.Row.EndLine, Text: row.Row.Text}, Status: row.Status}
			if set := row.Result; set != nil {
				result := &chatCandidateSet{Outcome: set.Outcome, Limitations: set.Limitations, AbstentionReason: set.AbstentionReason}
				if set.Records != nil {
					result.Records = make([]chatRecord, 0, len(set.Records))
				}
				if set.Abstentions != nil {
					result.Abstentions = make([]chatAbstention, 0, len(set.Abstentions))
				}
				for _, record := range set.Records {
					result.Records = append(result.Records, projectChatRecord(record))
				}
				for _, abstention := range set.Abstentions {
					result.Abstentions = append(result.Abstentions, chatAbstention{Subject: abstention.Subject, Reason: abstention.Reason})
				}
				projected.Result = result
			}
			extraction.Rows = append(extraction.Rows, projected)
		}
		input.Extraction = extraction
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > 512<<10 {
		return "", errors.New("chat context exceeds its bound")
	}
	return string(body), nil
}
