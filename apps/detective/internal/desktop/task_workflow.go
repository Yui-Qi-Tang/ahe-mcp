package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

// PrepareTask freezes the displayed saved text and purpose without connecting.
// The generic capture has no provider field boundaries or qualified revision.
func (s *Service) PrepareTask(draft TaskDraftRequest) (State, error) {
	_, done, err := s.begin(context.Background(), "task_prepare")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	s.mu.Lock()
	s.state.Task = nil // A changed or invalid draft cannot keep an old selection active.
	s.mu.Unlock()
	if state.Brief != nil || state.BatchPath != "" || state.Source == nil ||
		(state.Source.Kind != "local" && state.Source.Kind != "mcp") ||
		draft.SourcePath != state.Source.Path || draft.SourceSHA256 != state.Source.SHA256 {
		return s.finish(done, errors.New("task source changed"), "請重新確認目前保存的本機或 MCP 文字來源；Brief、演練及既有批次不能混用任務選段。")
	}
	if !validMessage(draft.Objective, 2048) {
		return s.finish(done, errors.New("invalid task objective"), "請填入非空的蒐集目的，最多 2,048 bytes；尚未呼叫模型。")
	}
	if len(state.Source.RawText) > taskextract.MaxProvidedBytes {
		return s.finish(done, errors.New("task source exceeds limit"), "目前單次任務最多提供 32 KiB 原文；請在來源工具縮小取得範圍後重新保存。未截短、分塊或呼叫模型。")
	}
	source, err := taskSource(*state.Source)
	if err != nil {
		return s.finish(done, err, "來源 bytes、身分或範圍資訊不符；請重新開啟來源收據或匯入檔案，未呼叫模型。")
	}
	if err := s.verifyTaskSource(*state.Source); err != nil {
		return s.finish(done, err, "保存來源已變更、遺失或不再是私有檔案；請重新確認來源，未呼叫模型。")
	}
	task := taskextract.Task{ID: "desktop-task-" + newID(), Revision: 1, Objective: draft.Objective, SourceID: source.ID, Parts: []string{source.Parts[0].Name}}
	if previous := state.Task; previous != nil && previous.Input.Source.ID == source.ID && previous.Input.Source.Location == source.Location {
		task.ID, task.Revision = previous.Input.Task.ID, previous.Input.Task.Revision+1
	}
	request, err := taskextract.Prepare(task, source, state.Settings.Model)
	if err != nil || len(request.Units()) == 0 {
		return s.finish(done, errors.New("invalid task input"), "任務需可讀原文與有效來源資訊；上限為 32 KiB／128 段，目的 2,048 bytes，來源限制每項 1,024 bytes。超限整體拒絕，不截短；未呼叫模型。")
	}
	work := taskWork(request, state.Settings.Model, state.Settings.BaseURL)
	s.mu.Lock()
	s.state.Task = &work
	s.state.Candidates, s.state.Extraction, s.state.BatchResult = []CandidateView{}, nil, nil
	s.mu.Unlock()
	return s.finish(done, nil, "目的與保存原文已凍結；請確認範圍後執行一次選段。這不是完整 ticket 宣告，也沒有呼叫模型或寫入 AHE。")
}

func taskSource(view SourceView) (taskextract.Source, error) {
	hash := sha256.Sum256([]byte(view.RawText))
	if view.ID != "sha256:"+view.SHA256 || view.SHA256 != hex.EncodeToString(hash[:]) || view.Bytes != len(view.RawText) {
		return taskextract.Source{}, errors.New("source content identity mismatch")
	}
	part := "body"
	limitations := []string{
		"Only this saved text is supplied. Provider object identity, revision and document completeness are not established.",
		"Generic text has no verified description, comments or history field boundaries; no other fields were requested or collected by this task.",
	}
	if view.Kind == "mcp" {
		part = "tool_return"
		limitations = append(limitations, "This is the exact saved MCP text return, not a declaration that the original ticket or page was fully collected.")
	}
	if view.Note != "" {
		limitations = append(limitations, "Recorded capture note (unverified metadata): "+view.Note)
	}
	if view.Capture != nil {
		capture := view.Capture
		limitations = append(limitations,
			"Recorded capture time: "+capture.CapturedAt,
			"Saved receipt reference: "+capture.Receipt.Path+"; sha256="+capture.Receipt.SHA256,
			"Saved raw result reference: "+capture.RawResult.Path+"; sha256="+capture.RawResult.SHA256,
		)
	}
	return taskextract.Source{ID: view.ID, Revision: "unknown", Title: view.Title, Location: view.Path,
		Coverage: "exact_excerpt", Limitations: limitations, Parts: []taskextract.SourcePart{{Name: part, Text: view.RawText}}}, nil
}

func (s *Service) verifyTaskSource(view SourceView) error {
	if filepath.Clean(view.Path) != view.Path || filepath.Dir(view.Path) != s.dataDir {
		// Reopened MCP captures may live in another private source directory.
		if view.Kind != "mcp" || view.Capture == nil {
			return errors.New("task source outside capture workspace")
		}
		verified, err := InspectSource(context.Background(), view.Capture.Receipt.Path)
		if err != nil || verified.Path != view.Path || verified.SHA256 != view.SHA256 || verified.RawText != view.RawText {
			return errors.New("task source receipt mismatch")
		}
		return nil
	}
	raw, err := s.readPrivate(filepath.Base(view.Path), savedSourceLimit)
	if err != nil || string(raw) != view.RawText {
		return errors.New("saved task source changed")
	}
	return nil
}

func taskWork(request *taskextract.Request, modelName, baseURL string) TaskWork {
	return TaskWork{Input: taskextract.Input{Version: taskextract.InputVersion, Task: request.Task(), Source: request.Source()},
		Model: modelName, BaseURL: baseURL, InputID: request.InputID(), Scope: request.Scope(), Units: request.Units(), Status: "prepared"}
}

// RunTask executes only the exact prepared request, once, and saves a private
// record. It never creates legacy candidates, pending proposals or decisions.
func (s *Service) RunTask(ctx context.Context, inputID string) (State, error) {
	ctx, done, err := s.begin(ctx, "task_run")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	work := state.Task
	if work == nil || work.Status != "prepared" || inputID != work.InputID ||
		state.Settings.Mode != "local" || work.Model != state.Settings.Model || work.BaseURL != state.Settings.BaseURL ||
		state.Brief != nil || state.BatchPath != "" || state.Source == nil ||
		(state.Source.Kind != "local" && state.Source.Kind != "mcp") {
		return s.finish(done, errors.New("task confirmation changed"), "請在本機模型模式重新確認目前的目的、來源與模型，再執行同一份準備結果；未呼叫模型。")
	}
	source, sourceErr := taskSource(*state.Source)
	request, err := taskextract.Prepare(work.Input.Task, source, work.Model)
	if sourceErr != nil || err != nil || request.InputID() != inputID || s.verifyTaskSource(*state.Source) != nil || s.privateDirectory() != nil {
		s.mu.Lock()
		s.state.Task = nil
		s.mu.Unlock()
		return s.finish(done, errors.New("task source changed"), "準備後的來源或私有工作區已變更；請重新確認，未呼叫模型。")
	}
	name := "task-run-" + newID() + ".json"
	work.Status, work.Record, work.Path = "running", nil, filepath.Join(s.dataDir, name)
	s.mu.Lock()
	s.state.Task = work
	s.mu.Unlock()
	result, attemptErr := ExtractTask(ctx, request.Task(), request.Source(), work.Model, work.BaseURL)
	// Serialize completion with Cancel: once a record is published, a later
	// cancel cannot turn that completed attempt into a different saved outcome.
	s.mu.Lock()
	if ctx.Err() != nil {
		attemptErr = ctx.Err()
	}
	record, recordErr := taskextract.NewRecord(request, result, attemptErr)
	if recordErr != nil {
		attemptErr = taskextract.ErrReplay
		record, recordErr = taskextract.NewRecord(request, taskextract.Result{}, attemptErr)
	}
	var body []byte
	if recordErr == nil {
		body, recordErr = json.Marshal(record)
	}
	if recordErr == nil {
		recordErr = s.writePrivate(name, body, false)
	}
	if recordErr != nil {
		work.Status, work.Record = "failed", nil
		s.mu.Unlock()
		return s.finish(done, recordErr, "任務紀錄保存未確認；保留顯示路徑，不覆寫或自動重跑。未發布候選，也沒有寫入 AHE。")
	}
	work.Status, work.Record = record.Status, &record
	if record.Result != nil {
		work.Scope = record.Result.Scope
	}
	s.mu.Unlock()
	if attemptErr != nil || record.Status == "failed" {
		return s.finish(done, errors.New("task attempt failed"), "選段未完成（"+record.ErrorCode+"）；已保存失敗紀錄與原文，沒有候選、重試或 AHE 寫入。")
	}
	return s.finish(done, nil, "原文選段紀錄已保存；候選與模型註記僅供檢視，不是人工核准，也尚未送入 pending。")
}

// OpenTaskRecord reopens this workspace's saved input without reading the
// original source or using current settings, models, MCP or database services.
func (s *Service) OpenTaskRecord(path string) (State, error) {
	_, done, err := s.begin(context.Background(), "task_open")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	if filepath.Dir(path) != s.dataDir || filepath.Clean(path) != path || !strings.HasPrefix(filepath.Base(path), "task-run-") {
		return s.finish(done, errors.New("foreign task path"), "請選擇目前私有工作區的 task-run 紀錄；未建立連線。")
	}
	raw, err := s.readPrivate(filepath.Base(path), 2<<20)
	if err != nil {
		return s.finish(done, err, "任務紀錄無法安全讀取；原本工作保留，未建立連線。")
	}
	record, err := taskextract.ParseRecord(raw)
	if err != nil {
		return s.finish(done, err, "任務紀錄與凍結輸入不符；未顯示未核對候選，也未重新執行模型。")
	}
	request, err := taskextract.Prepare(record.Input.Task, record.Input.Source, record.Model)
	if err != nil {
		return s.finish(done, err, "任務原文無法還原；未建立連線。")
	}
	work := taskWork(request, record.Model, "")
	work.Path, work.Status, work.Record = path, record.Status, &record
	if record.Result != nil {
		work.Scope = record.Result.Scope
	}
	s.mu.Lock()
	s.state.Task = &work
	s.state.Source = nil // Full source remains in Task.Input, not a new live capture.
	s.state.Brief = nil
	s.state.Candidates, s.state.Extraction, s.state.BatchResult = []CandidateView{}, nil, nil
	s.state.BatchPath, s.state.BatchDigest = "", ""
	s.mu.Unlock()
	return s.finish(done, nil, "任務已離線重開；完整原文與候選一致性已核對。這是保存紀錄，不是來源真實性、最新狀態或人工核准。")
}
