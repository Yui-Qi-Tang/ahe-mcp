package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const sourceChatBytes = 32 << 10

// SourceChat is one human-confirmed offline Codebase read, not a review.
// A follow-up creates a new ID; a confirmed ID is consumed even on failure.
type SourceChat struct {
	ID           string      `json:"id"`
	Question     string      `json:"question"`
	ConnectionID string      `json:"connectionID"`
	Status       string      `json:"status"`
	ExpiresAt    string      `json:"expiresAt"`
	Advice       *ToolAdvice `json:"advice,omitempty"`
	Source       *SourceView `json:"source,omitempty"`
}

// StartSourceChat proposes one read. Only the unchanged, network-denied
// Codebase preset passes; no generic MCP connection reaches the planner.
func (s *Service) StartSourceChat(parent context.Context, id, question string) (State, error) {
	ctx, done, err := s.begin(parent, "codebase_chat_plan")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if !validMessage(question, 4096) || state.BatchPath != "" || state.BatchDigest != "" {
		return s.finish(done, errors.New("invalid chat read"), "請輸入問題；已有待審批次時請先開始新工作，避免更換審閱來源。")
	}
	config, err := s.codebaseConnection(id)
	if err != nil {
		return s.finish(done, err, "聊天室只接受未變更、網路封鎖下的 Codebase 預設；未呼叫模型或來源。")
	}
	tools, input, err := toolAdviceInput(config, state.Tools, question)
	if err != nil {
		return s.finish(done, err, "請先取得 Codebase 工具清單；未呼叫模型或來源。")
	}
	for _, tool := range tools {
		if !slices.Contains(codebaseReadTools, tool.Name) {
			return s.finish(done, errors.New("non-read tool"), "工具清單包含未允許的工具；沒有執行。")
		}
	}
	prior := ""
	recent := []chatMessage{}
	if state.SourceChat != nil && state.SourceChat.ConnectionID == id {
		// Carry the immediately preceding clarification/answer, not just the
		// new one-word reply. This is bounded history, never a full task memory.
		start := max(0, len(state.Messages)-4)
		for _, message := range state.Messages[start:] {
			recent = append(recent, chatMessage{Role: message.Role, Text: message.Text, Kind: message.Kind})
		}
	}
	if state.SourceChat != nil && state.SourceChat.ConnectionID == id && state.SourceChat.Source != nil {
		prior = state.SourceChat.Source.RawText
		if len(prior) > sourceChatBytes {
			prior = ""
		}
	}
	body, err := json.Marshal(struct {
		Request             json.RawMessage `json:"request"`
		PreviousObservation string          `json:"previous_observation"`
		RecentConversation  []chatMessage   `json:"recent_conversation"`
		HistoryIsPartial    bool            `json:"history_is_partial"`
	}{json.RawMessage(input), prior, recent, true})
	if err != nil || len(body) > 160<<10 {
		return s.finish(done, errors.New("chat input too large"), "工具輸入超過本回合上限；未呼叫模型。")
	}
	s.mu.Lock()
	s.state.SourceChat = nil
	s.messageLocked("user", question, "user")
	s.mu.Unlock()
	text, err := sourceToolAdviceResponse(ctx, state.Settings, string(body))
	if err != nil {
		return s.finish(done, err, "模型未完成讀取建議；沒有呼叫來源或 DB。")
	}
	advice, err := parseToolAdvice(text, config, tools)
	if err != nil {
		return s.finish(done, err, "模型建議未通過工具及參數核對；沒有執行。")
	}
	request := SourceChat{ID: newID(), Question: question, ConnectionID: id, Status: "awaiting_confirmation", ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano), Advice: &advice}
	if advice.Outcome == "abstain" {
		request.Status = "needs_input"
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return s.finish(done, err, "讀取建議已取消。")
	}
	s.state.SourceChat = &request
	s.messageLocked("assistant", advice.Reason, "tool_advice")
	s.mu.Unlock()
	return s.finish(done, nil, "請核對聊天室的讀取卡；尚未執行工具，也沒有入庫。")
}

func confirmedChatRead(state State, id string, now time.Time) (*SourceChat, error) {
	r := state.SourceChat
	if r == nil || r.ID != id || r.Status != "awaiting_confirmation" || r.Advice == nil || r.Advice.Outcome != "suggested" || r.Advice.Tool == nil || r.Advice.Tool.ConnectionID != r.ConnectionID || !slices.Contains(codebaseReadTools, r.Advice.Tool.Name) || state.BatchPath != "" || state.BatchDigest != "" {
		return nil, errors.New("chat confirmation is absent, stale or consumed")
	}
	deadline, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	if err != nil || !now.Before(deadline) {
		return nil, errors.New("chat confirmation expired")
	}
	return r, nil
}

// ConfirmSourceChat performs at most one source call and one answer generation.
// The backend rechecks the offline preset before any MCP activity, retaining
// one operation/cancellation scope through capture and model generation.
func (s *Service) ConfirmSourceChat(parent context.Context, id string) (State, error) {
	ctx, done, err := s.begin(parent, "codebase_chat_confirm")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	r, err := confirmedChatRead(state, id, time.Now())
	if err != nil {
		return s.finish(done, err, "這份讀取確認已過期、使用過或不再適用；請重新提出問題。")
	}
	config, err := s.codebaseConnection(r.ConnectionID)
	if err != nil {
		return s.finish(done, err, "來源不是核准的網路封鎖 Codebase 預設；未執行讀取。")
	}
	expected := recordedTool(*r.Advice.Tool)
	if err := sourcemcp.ValidateToolArguments(config, expected, r.Advice.ArgumentsJSON); err != nil {
		return s.finish(done, err, "工具或設定與讀取卡不一致；未執行。")
	}
	s.mu.Lock()
	s.state.SourceChat.Status = "failed"
	s.mu.Unlock()
	result, err := sourcemcp.Call(ctx, config, expected, r.Advice.ArgumentsJSON)
	if err != nil {
		return s.finish(done, err, "來源讀取未完成；本次確認已使用，不自動重試，也沒有寫入 AHE。")
	}
	source, err := s.captureToolResult(ctx, config, expected, r.Advice.ArgumentsJSON, result)
	if err != nil {
		return s.finish(done, err, err.Error())
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return s.finish(done, err, "讀取已取消；已保存資料保留，未產生答案或入庫。")
	}
	s.state.SourceChat.Source = &source
	s.state.Source = &source
	s.state.Task = nil
	s.state.Brief = nil
	s.state.Candidates = []CandidateView{}
	s.state.Extraction = nil
	s.state.BatchResult = nil
	s.mu.Unlock()
	if len(source.RawText) > sourceChatBytes {
		return s.finish(done, errors.New("read exceeds model budget"), "工具結果已完整保存，但超過本回合 32 KiB 模型輸入上限；請縮小範圍，不會截斷後冒充完整答案。")
	}
	response, err := chatResponse(ctx, s.Snapshot())
	if err != nil {
		return s.finish(done, err, "來源已保存，但模型回答未完成；沒有自動再次呼叫工具或入庫。")
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return s.finish(done, err, "回答已取消；來源仍保留，沒有入庫。")
	}
	s.state.SourceChat.Status = "completed"
	s.messageLocked("assistant", response, "model")
	s.mu.Unlock()
	return s.finish(done, nil, "完成一次來源讀取與回答；可繼續追問。原始結果與來源收據已保存，沒有 pending 或 admission。")
}
