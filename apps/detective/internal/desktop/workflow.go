package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/pending"
)

const demoSource = "## Status at a Glance\n| Capability | Status | Notes |\n| --- | --- | --- |\n| Demonstration review | IMPLEMENTED | Synthetic demonstration only; no production claim or admission. |\n"

// LoadDemo starts a deterministic rehearsal and clears only active UI pointers.
// Previously saved sources, checkpoints and receipts are never deleted.
func (s *Service) LoadDemo() (State, error) {
	_, done, err := s.begin(context.Background(), "demo")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	source, err := s.captureSource("確定性演練 STATUS（合成資料）", "demo", demoSource)
	if err != nil {
		return s.finish(done, err, "演練來源無法安全保存。")
	}
	s.mu.Lock()
	s.state.Settings.Mode = "demo"
	s.forgetAllSourceLoginsLocked()
	s.state.Brief = nil
	s.state.Source = &source
	s.state.Candidates = []CandidateView{}
	s.state.Extraction = nil
	s.state.BatchResult = nil
	s.state.BatchPath = ""
	s.state.BatchDigest = ""
	s.state.Messages = []Message{}
	s.state.Tools = []Tool{}
	s.messageLocked("assistant", "這是合成資料的固定操作演練，未做語意判斷，也未記錄人工採納；沒有呼叫模型、MCP 或 DB。可查看來源與固定候選；輸入 admit 不會改變候選狀態，也不會執行工具或送出 pending。", "demo")
	s.mu.Unlock()
	return s.finish(done, nil, "已開始新演練；先前保存的來源與批次仍保留。")
}

// ImportSource captures a local file without model or MCP access.
func (s *Service) ImportSource(path string) (State, error) {
	_, done, err := s.begin(context.Background(), "import")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	if s.Snapshot().BatchPath != "" {
		return s.finish(done, errors.New("active batch"), "目前已有待檢視批次；請先開始新任務，不直接替換其來源。")
	}
	document, err := labstatus.LoadDocument(path)
	if err != nil {
		return s.finish(done, err, "來源必須是絕對路徑、非符號連結且不超過 256 KiB 的 UTF-8 一般檔案。")
	}
	source, err := s.captureSource(filepath.Base(path), "local", document.RawText())
	if err != nil {
		return s.finish(done, err, "來源快照未完成保存；沒有呼叫模型。")
	}
	metadata, marshalErr := json.Marshal(struct {
		OriginalPath string `json:"original_path"`
		SnapshotPath string `json:"snapshot_path"`
		SHA256       string `json:"sha256"`
	}{path, source.Path, source.SHA256})
	if marshalErr != nil {
		return s.finish(done, marshalErr, "來源座標無法保存；沒有呼叫模型。")
	}
	provenance, err := s.captureSource(filepath.Base(path)+" — original source coordinate", "local-provenance", string(metadata))
	if err != nil {
		return s.finish(done, err, "來源座標無法完整保存；沒有呼叫模型。")
	}
	source.Note = "原始路徑：" + path + "；抽取使用保存的快照 bytes。不可覆寫來源座標紀錄：" + provenance.Path
	s.mu.Lock()
	s.state.Brief = nil
	s.state.Source = &source
	s.state.Candidates = []CandidateView{}
	s.state.Extraction = nil
	s.state.BatchResult = nil
	s.mu.Unlock()
	detail := "來源快照已保存；沒有呼叫模型或 MCP。"
	if len(source.Rows) == 0 {
		detail = "來源已保存供檢視；目前抽取只支援 Status at a Glance 表列，不支援此文件形狀。"
	}
	return s.finish(done, nil, detail)
}

// OpenBatch reopens immutable local inputs and inspects file availability only.
// It does not read the original source, run a model, or query a database.
func (s *Service) OpenBatch(path string) (State, error) {
	ctx, done, err := s.begin(context.Background(), "open_batch")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	index, err := pending.LoadBatchIndex(path)
	if err != nil {
		return s.finish(done, err, "批次無法通過私有檔案、完整來源與 digest 檢查；未建立連線。")
	}
	document, err := index.Document()
	if err != nil {
		return s.finish(done, err, "批次來源無法驗證。")
	}
	coordinate := document.Source()
	source := SourceView{ID: "sha256:" + coordinate.SHA256, Title: filepath.Base(coordinate.Path), Path: coordinate.Path, Kind: "local", RawText: document.RawText(), SHA256: coordinate.SHA256, Bytes: coordinate.Bytes, Rows: sourceRows(document), Note: "從批次中的保存 bytes 還原，沒有重新讀來源；digest 不認證原始模型或真人身分。"}
	result, inspectErr := pending.InspectBatch(ctx, path, "")
	if err := validateBatchResultDigest(result, index.Digest); err != nil {
		return s.finish(done, err, "批次在檢查期間變更；保留原畫面，請重新開啟並核對批次。")
	}
	s.mu.Lock()
	s.state.Source = &source
	s.state.BatchPath = path
	s.state.BatchDigest = index.Digest
	s.state.Brief = nil
	s.state.Extraction = &index.Batch
	s.state.Candidates = candidateViews(index.Batch)
	inspectErr = errors.Join(inspectErr, s.applyBatchResultLocked(result))
	s.mu.Unlock()
	if inspectErr != nil {
		return s.finish(done, inspectErr, "批次已保留，但部分本機檔案檢查失敗；請查看完整候選狀態。")
	}
	return s.finish(done, nil, "批次已離線開啟；本機檔案不是目前 DB 狀態或人工核准。")
}

// Extract uses the current frozen source. A successful local extraction only
// prepares immutable inputs; it never submits them to AHE.
func (s *Service) Extract(ctx context.Context, line int) (State, error) {
	ctx, done, err := s.begin(ctx, "extract")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.Source == nil || state.BatchPath != "" || state.Brief != nil {
		return s.finish(done, errors.New("source unavailable"), "請先選擇來源；既有批次只能檢視或恢復，不重新抽取覆寫。")
	}
	if state.Source.Kind == "demo" {
		if line != 4 {
			return s.finish(done, errors.New("invalid demo line"), "請選擇演練資料第 4 行。")
		}
		record := labstatus.Record{RecordType: "capability_state", Subject: "Demonstration review", Statement: "The synthetic source describes demonstration review only.", EpistemicClass: "claim", Status: "implemented", Scope: "lab_contract", SelectionState: "unspecified", Citation: labstatus.Citation{StartLine: 4, EndLine: 4, ExactQuote: state.Source.Rows[0].Text}, BlockedBy: []string{}, DoesNotEstablish: []string{"Production readiness or admission."}, Qualifiers: []string{"Deterministic synthetic demonstration; not generated by a model."}}
		s.mu.Lock()
		s.state.Candidates = []CandidateView{{Ordinal: 1, Record: record, State: "demo_not_submittable"}}
		s.mu.Unlock()
		return s.finish(done, nil, "已顯示確定性合成候選；沒有模型、checkpoint 或 DB 寫入，不能送出 pending。")
	}
	if state.Source.Kind != "local" && state.Source.Kind != "mcp" {
		return s.finish(done, errors.New("unsupported source kind"), "此項目是來源取得紀錄，不能當成抽取來源。")
	}
	if state.Settings.Mode != "local" {
		return s.finish(done, errors.New("local mode required"), "真實抽取需明確切換本機模型模式；未呼叫模型。")
	}
	document, err := labstatus.RestoreDocument(state.Source.Path, state.Source.RawText)
	if err != nil || document.Source().SHA256 != state.Source.SHA256 {
		return s.finish(done, errors.New("snapshot invalid"), "來源快照驗證失敗；未呼叫模型。")
	}
	if line <= 0 || !sourceHasLine(state.Source.Rows, line) {
		return s.finish(done, errors.New("unsupported row"), "目前只支援選定的 Status at a Glance 資料列；未呼叫模型。")
	}
	path := filepath.Join(s.dataDir, "batch-"+newID()+".json")
	writer, err := pending.Reserve(path)
	if err != nil {
		return s.finish(done, err, "無法保留私有批次路徑；未呼叫模型。")
	}
	defer writer.Close()
	llm, err := localModel(ctx, state.Settings)
	if err != nil {
		return s.finish(done, err, "本機模型連線或模型名稱檢查失敗；來源快照保留，沒有送出 pending。")
	}
	extractor, err := labstatus.NewExtractor(llm)
	if err != nil {
		return s.finish(done, err, "無法建立來源抽取器。")
	}
	batch, extractErr := extractor.ExtractTableRow(ctx, document, "Status at a Glance", line)
	// Row failures can contain raw upstream diagnostics. Keep their state, not
	// their unsafe diagnostic text, in the UI and saved local report.
	failureDetail := ""
	for i := range batch.Rows {
		if batch.Rows[i].Error != "" {
			batch.Rows[i].Error = extractionFailureText(batch.Rows[i].Cause)
			failureDetail = batch.Rows[i].Error
		}
		// Do not retain raw diagnostics even in in-process UI state.
		batch.Rows[i].Cause = nil
	}
	if batch.SchemaVersion != "" {
		body, marshalErr := json.Marshal(batch)
		if marshalErr == nil {
			marshalErr = s.writePrivate("extraction-"+newID()+".json", body, false)
		}
		s.mu.Lock()
		s.state.Extraction = &batch
		s.state.Candidates = candidateViews(batch)
		s.mu.Unlock()
		if marshalErr != nil {
			return s.finish(done, marshalErr, "抽取結果無法安全保存；沒有送出 pending。")
		}
	}
	if extractErr != nil || ctx.Err() != nil || len(batch.Rows) != 1 || batch.Rows[0].Status != "validated" || batch.Rows[0].Result == nil {
		if ctx.Err() != nil {
			failureDetail = extractionFailureText(ctx.Err())
		} else if failureDetail == "" {
			failureDetail = extractionFailureText(extractErr)
		}
		return s.finish(done, errors.New("extraction not validated"), failureDetail+" 來源與檢查結果已保留；沒有送出 pending 或寫入 DB。")
	}
	if batch.Rows[0].Result.Outcome != "extracted" || len(batch.Rows[0].Result.Records) == 0 {
		return s.finish(done, nil, "模型選擇不抽取候選；已保留 abstention 與限制，沒有建立 pending 批次。")
	}
	index, err := pending.NewBatchIndex(state.Settings.SourceID, document, batch, "")
	if err != nil {
		return s.finish(done, err, "抽取結果已保留，但不符合 pending 的單列、1–16 候選或限制保存契約；未提交。")
	}
	if err = writer.WriteBatchIndex(index); err != nil {
		if errors.Is(err, pending.ErrUncertain) {
			s.mu.Lock()
			s.state.BatchPath = path
			s.state.BatchDigest = index.Digest
			s.mu.Unlock()
		}
		return s.finish(done, err, "批次保存未確認完成；請保留原路徑，不覆寫或送出新批次。")
	}
	s.mu.Lock()
	s.state.BatchPath = path
	s.state.BatchDigest = index.Digest
	s.mu.Unlock()
	if err = writer.Close(); err != nil {
		return s.finish(done, err, "批次已保存，但保留鎖釋放未確認完成；請恢復同一批次。")
	}
	if err = pending.MaterializeBatch(path); err != nil {
		return s.finish(done, err, "完整 index 已保存，部分 checkpoint 尚未完成；請恢復同一批次，不重新抽取。")
	}
	result, err := pending.InspectBatch(ctx, path, "")
	s.mu.Lock()
	err = errors.Join(err, s.applyBatchResultLocked(result))
	s.mu.Unlock()
	if errors.Is(err, errBatchChanged) {
		return s.finish(done, err, "批次檢查結果與畫面 digest 不同；未套用其他批次狀態，也沒有送出 pending。")
	}
	return s.finish(done, err, "來源、完整抽取與不可覆寫 checkpoint 已保存；尚未寫入 AHE，送出需另行確認整個批次 digest。")
}

// SubmitPending requires exact confirmation of the complete currently displayed
// index. It never calls any reviewer writer or interprets a chat as approval.
func (s *Service) SubmitPending(ctx context.Context, confirmation string) (State, error) {
	ctx, done, err := s.begin(ctx, "submit_pending")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	s.mu.Lock()
	s.invalidateObservationsLocked()
	s.mu.Unlock()
	state := s.Snapshot()
	if state.Settings.Mode != "local" || state.Source == nil || state.Source.Kind == "demo" || state.BatchPath == "" || state.BatchDigest == "" || confirmation != state.BatchDigest {
		return s.finish(done, errors.New("confirmation required"), "送出需本機模式、非演練批次，以及完整且相同的 batch digest；聊天文字不是授權。")
	}
	index, err := pending.LoadBatchIndex(state.BatchPath)
	if err != nil || index.Digest != state.BatchDigest {
		return s.finish(done, errors.New("batch changed"), "批次與已顯示 digest 不同；未啟動 launcher。")
	}
	if state.Settings.IntakeLauncher == "" || state.Settings.QueryLauncher == "" {
		return s.finish(done, errors.New("launchers required"), "送出需明確指定 intake 與 Query launcher；未建立連線。")
	}
	result, err := pending.ResumeBatch(ctx, state.BatchPath, state.Settings.IntakeLauncher, state.Settings.QueryLauncher)
	s.mu.Lock()
	err = errors.Join(err, s.applyBatchResultLocked(result))
	s.mu.Unlock()
	if errors.Is(err, errBatchChanged) {
		return s.finish(done, err, "回傳批次與畫面 digest 不同；未套用其他批次狀態，但可能已有 DB 寫入。請保留檔案並核對原批次，不要重新提交。")
	}
	if err != nil {
		return s.finish(done, err, "提交或讀回尚未全部確認；可能已有 DB 寫入，請查看每筆 failure stage 並恢復同一批次。")
	}
	return s.finish(done, nil, "批次已逐筆讀回；這是 pending／既有狀態觀測，不是人工核准或 canonical 寫入。")
}

// QueryPending only observes the exact saved occurrences; it never retries intake.
func (s *Service) QueryPending(ctx context.Context) (State, error) {
	ctx, done, err := s.begin(ctx, "query_pending")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	s.mu.Lock()
	s.invalidateObservationsLocked()
	s.mu.Unlock()
	state := s.Snapshot()
	if state.Settings.Mode != "local" || state.Source == nil || state.Source.Kind == "demo" || state.BatchPath == "" || state.BatchDigest == "" || state.Settings.QueryLauncher == "" {
		return s.finish(done, errors.New("query configuration required"), "精確查詢需本機模式、既有批次及明確 Query launcher；未建立連線。")
	}
	index, err := pending.LoadBatchIndex(state.BatchPath)
	if err != nil || index.Digest != state.BatchDigest {
		return s.finish(done, errBatchChanged, "批次與已顯示 digest 不同或無法驗證；未啟動 Query launcher。請重新開啟並核對批次。")
	}
	result, err := pending.InspectBatch(ctx, state.BatchPath, state.Settings.QueryLauncher)
	s.mu.Lock()
	err = errors.Join(err, s.applyBatchResultLocked(result))
	s.mu.Unlock()
	if errors.Is(err, errBatchChanged) {
		return s.finish(done, err, "Query 結果與畫面 digest 不同；未套用其他批次狀態。請重新開啟並核對批次，沒有重送 intake。")
	}
	if err != nil {
		return s.finish(done, err, "部分精確 Query 尚未確認；不推論資料不存在，也沒有重送 intake。")
	}
	return s.finish(done, nil, "已完成可取得 locator 的精確唯讀檢查；not_checked 不代表 DB 沒有資料。")
}

func sourceHasLine(rows []SourceRow, line int) bool {
	for _, row := range rows {
		if row.Line == line {
			return true
		}
	}
	return false
}

func candidateViews(batch labstatus.RowBatch) []CandidateView {
	views := []CandidateView{}
	for _, row := range batch.Rows {
		if row.Result != nil {
			for _, record := range row.Result.Records {
				views = append(views, CandidateView{Ordinal: len(views) + 1, Record: record, State: "local_candidate"})
			}
		}
	}
	return views
}

var errBatchChanged = errors.New("batch result does not match the displayed digest")

func validateBatchResultDigest(result pending.BatchResult, digest string) error {
	if result.SchemaVersion != "" && (digest == "" || result.IndexDigest != digest) {
		return errBatchChanged
	}
	return nil
}

func (s *Service) applyBatchResultLocked(result pending.BatchResult) error {
	if err := validateBatchResultDigest(result, s.state.BatchDigest); err != nil {
		s.invalidateObservationsLocked()
		return err
	}
	if result.SchemaVersion == "" {
		s.invalidateObservationsLocked()
		return nil
	}
	s.state.BatchResult = &result
	for i := range s.state.Candidates {
		candidate := &s.state.Candidates[i]
		candidate.State = "not_checked"
		candidate.FailureStage = ""
		for _, member := range result.Members {
			if member.Ordinal == candidate.Ordinal {
				candidate.State = member.State
				candidate.FailureStage = member.FailureStage
				candidate.CheckpointPath = member.CheckpointPath
				break
			}
		}
	}
	return nil
}

func (s *Service) invalidateObservationsLocked() {
	s.state.BatchResult = nil
	for i := range s.state.Candidates {
		s.state.Candidates[i].State = "not_checked"
		s.state.Candidates[i].FailureStage = ""
	}
}

// SendMessage is a tool-free conversation, never an extraction or write command.
func (s *Service) SendMessage(ctx context.Context, text string) (State, error) {
	ctx, done, err := s.begin(ctx, "chat")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	if !validMessage(text, 4096) {
		return s.finish(done, errors.New("invalid message"), "訊息需為非空 UTF-8 文字，最多 4096 bytes，不能含控制字元。")
	}
	s.mu.Lock()
	s.messageLocked("user", text, "user")
	s.mu.Unlock()
	state := s.Snapshot()
	if state.Settings.Mode == "demo" {
		s.mu.Lock()
		s.messageLocked("assistant", "這是固定操作演練回覆，未做語意判斷，也未記錄人工採納。輸入 admit 或其他文字不會改變候選狀態；此模式不呼叫模型、MCP 或 DB，也不會執行工具或送出 pending。", "demo")
		s.mu.Unlock()
		return s.finish(done, nil, "已完成確定性演練回覆；沒有模型、工具或 DB 操作。")
	}
	response, err := chatResponse(ctx, state)
	if err != nil {
		return s.finish(done, err, "本機模型聊天未完成或回應不符合工具隔離限制；未執行工具或 DB 操作。")
	}
	s.mu.Lock()
	s.messageLocked("assistant", response, "model")
	s.mu.Unlock()
	return s.finish(done, nil, "本機模型回覆僅供閱讀；沒有執行工具、抽取或 pending 提交。")
}

func validMessage(text string, limit int) bool {
	// Newlines and tabs are allowed in displayed text, unlike connection fields.
	return safeText(strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text), limit) && len(text) <= limit
}
