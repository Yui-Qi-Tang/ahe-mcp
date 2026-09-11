package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// BriefWork retains one exact reading/selection workflow. Local hashes verify
// consistency only; neither saved decisions nor model output authenticate humans.
type BriefWork struct {
	Version        string                      `json:"version"`
	Report         sourcepilot.BriefReport     `json:"report"`
	SourcePath     string                      `json:"sourcePath"`
	Model          string                      `json:"model"`
	Path           string                      `json:"path"`
	Digest         string                      `json:"digest"`
	Submission     *ahemcp.BriefSubmission     `json:"submission,omitempty"`
	Handoff        *ahemcp.Handoff             `json:"handoff,omitempty"`
	Review         *ahemcp.SourceClaimReview   `json:"review,omitempty"`
	Decision       *BriefDecision              `json:"decision,omitempty"`
	Admission      *ahemcp.ReviewedAdmission   `json:"admission,omitempty"`
	Disposition    *ahemcp.ReviewedDisposition `json:"disposition,omitempty"`
	Outcome        string                      `json:"outcome"`
	FailureStage   string                      `json:"failureStage"`
	IntakeLauncher string                      `json:"intakeLauncher"`
	QueryLauncher  string                      `json:"queryLauncher"`
	ReviewLauncher string                      `json:"reviewLauncher"`
}

// BriefDecision is an explicit, immutable operator input bound before writing.
type BriefDecision struct {
	DisplayID      string `json:"displayID"`
	Outcome        string `json:"outcome"`
	Reason         string `json:"reason"`
	ReviewLauncher string `json:"reviewLauncher"`
	QueryLauncher  string `json:"queryLauncher"`
}

// BriefCandidateRequest selects a statement and exact original source lines.
type BriefCandidateRequest struct {
	Statement    string `json:"statement"`
	StartLine    int    `json:"startLine"`
	EndLine      int    `json:"endLine"`
	SourceSHA256 string `json:"sourceSHA256"`
}

// BriefReviewRequest contains no default outcome or inferred chat approval.
type BriefReviewRequest struct {
	DisplayID string `json:"displayID"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
}

func briefWorkDigest(work BriefWork) string {
	work.Digest, work.Path = "", ""
	raw, _ := json.Marshal(work) // Concrete JSON-compatible fields only.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateBriefWork(work BriefWork) error {
	bad := errors.New("brief work does not preserve its exact source and selection")
	raw, err := json.Marshal(work.Report)
	if err != nil || work.Version != "detective-desktop-brief/v1" || work.Digest != briefWorkDigest(work) || !filepath.IsAbs(work.SourcePath) {
		return bad
	}
	if _, err := sourcepilot.ParseBriefReport(raw); err != nil {
		return bad
	}
	if work.Submission != nil {
		if ahemcp.ValidateBriefSubmission(*work.Submission) != nil || !reflect.DeepEqual(work.Submission.Report, work.Report) || work.Submission.SourcePath != work.SourcePath || work.Submission.Model != work.Model {
			return bad
		}
	}
	if work.Handoff != nil && work.Submission == nil {
		return bad
	}
	if work.Review != nil && (work.Handoff == nil || ahemcp.ValidateBriefReview(*work.Review, *work.Submission, *work.Handoff) != nil) {
		return bad
	}
	if work.Decision != nil {
		d := work.Decision
		if work.Review == nil || d.DisplayID != work.Review.Display.ID || !validBriefDecision(d.Outcome, d.Reason) || !filepath.IsAbs(d.ReviewLauncher) || !filepath.IsAbs(d.QueryLauncher) {
			return bad
		}
	}
	if (work.Admission != nil || work.Disposition != nil) && work.Decision == nil {
		return bad
	}
	return nil
}

func validBriefDecision(outcome, reason string) bool {
	return (outcome == "admit" || outcome == "reject" || outcome == "audit_only") && safeText(reason, 2000) && strings.TrimSpace(reason) == reason
}

// saveBrief publishes immutable complete recovery material before each writer.
// It never replaces an older source, selection, decision or receipt.
func (s *Service) saveBrief(work BriefWork) error {
	work.Version = "detective-desktop-brief/v1"
	name := "brief-work-" + newID() + ".json"
	work.Path = filepath.Join(s.dataDir, name)
	work.Digest = briefWorkDigest(work)
	if err := validateBriefWork(work); err != nil {
		return err
	}
	raw, err := json.Marshal(work)
	if err != nil {
		return err
	}
	if err := s.writePrivate(name, raw, false); err != nil {
		return err
	}
	s.mu.Lock()
	s.state.Brief = &work
	s.mu.Unlock()
	return nil
}

// ImportBriefSource freezes caller-declared source JSON without network access.
func (s *Service) ImportBriefSource(path string) (State, error) {
	_, done, err := s.begin(context.Background(), "brief_import")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.BatchPath != "" || state.Brief != nil {
		return s.finish(done, errors.New("active work"), "請先開始新工作；既有 Brief 與批次保留，不直接替換。")
	}
	document, err := labstatus.LoadDocument(path)
	if err != nil {
		return s.finish(done, err, "Brief 來源必須是有界 UTF-8 一般 JSON 檔案，不能是符號連結。")
	}
	source, err := sourcepilot.ParseBriefSource([]byte(document.RawText()))
	if err != nil {
		return s.finish(done, err, "Brief 來源格式、範圍或限制不符；未連線。")
	}
	report, err := sourcepilot.NewBriefReport(source)
	if err != nil {
		return s.finish(done, err, "Brief 來源無法凍結。")
	}
	view, err := s.captureSource(filepath.Base(path), "local", source.Body)
	if err != nil {
		return s.finish(done, err, "Brief 原始內文無法安全保存。")
	}
	err = s.saveBrief(BriefWork{Report: report, SourcePath: view.Path, Outcome: "inspected"})
	if err != nil {
		return s.finish(done, err, "Brief 完整來源與來源座標保存失敗。")
	}
	s.mu.Lock()
	s.state.Source, s.state.Candidates, s.state.Extraction, s.state.BatchResult = &view, []CandidateView{}, nil, nil
	s.mu.Unlock()
	return s.finish(done, nil, "Brief 原文與呼叫者聲明的來源座標已保存；尚未呼叫模型或 MCP。")
}

// ExtractBrief makes the same single tool-free model call as the Brief CLI.
func (s *Service) ExtractBrief(ctx context.Context) (State, error) {
	ctx, done, err := s.begin(ctx, "brief_extract")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.Settings.Mode != "local" || state.Brief == nil || state.Brief.Report.Stage != "inspected" || state.Brief.Outcome != "inspected" || state.Brief.Submission != nil {
		return s.finish(done, errors.New("brief unavailable"), "請開啟新的 Brief 來源並明確啟用本機模式；不覆寫既有抽取。")
	}
	work := *state.Brief
	work.Model, work.Outcome = state.Settings.Model, "model_requested"
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "抽取前紀錄未保存；未呼叫模型。")
	}
	extractor, err := NewBriefExtractor(ctx, state.Settings.Model, state.Settings.BaseURL)
	if err == nil {
		work.Report, err = extractor.Extract(ctx, work.Report.Source)
	}
	work.Outcome = "completed_unreviewed"
	if err != nil || ctx.Err() != nil {
		work.Outcome, work.FailureStage = "extraction_failed", "model"
	}
	if saveErr := s.saveBrief(work); saveErr != nil {
		return s.finish(done, saveErr, "抽取結果保存未確認；保留原工作，不提交。")
	}
	if err != nil || ctx.Err() != nil {
		return s.finish(done, errors.New("brief extraction failed"), "Brief 抽取未完成；來源及可保存結果保留，沒有送出 pending。")
	}
	return s.finish(done, nil, "Brief 已保存，仍未審閱；請對照完整原文並選取精確引用，不以摘要存在當作支持證明。")
}

// PrepareBriefCandidate saves a user-selected claim without source or DB I/O.
func (s *Service) PrepareBriefCandidate(request BriefCandidateRequest) (State, error) {
	_, done, err := s.begin(context.Background(), "brief_select")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.Brief == nil || state.Brief.Submission != nil || request.SourceSHA256 != state.Brief.Report.BodySHA256 {
		return s.finish(done, errors.New("selection changed"), "來源確認不符或已有凍結候選；未建立或替換提案。")
	}
	work := *state.Brief
	document, err := labstatus.RestoreDocument(work.SourcePath, work.Report.Source.Body)
	if err != nil {
		return s.finish(done, err, "完整來源無法還原。")
	}
	quote, ok := document.ExactQuote(request.StartLine, request.EndLine)
	if !ok {
		return s.finish(done, errors.New("invalid citation"), "請選擇原文存在的完整行範圍。")
	}
	selection, err := ahemcp.NewBriefSubmission(work.Report, work.SourcePath, work.Model, request.Statement, labstatus.Citation{StartLine: request.StartLine, EndLine: request.EndLine, ExactQuote: quote})
	if err != nil {
		return s.finish(done, err, "候選需為完整 Brief、2000 bytes 內敘述與 1–12 行非空精確引用；不截短內容，也不自動證明語意支持。")
	}
	work.Submission, work.Outcome, work.FailureStage = &selection, "candidate_saved", ""
	return s.finishBrief(done, work, "候選與原始 Brief 已凍結；尚未送 pending，送出需另行確認完整 digest。")
}

func (s *Service) briefOnline(state State, confirmation string) error {
	if state.Settings.Mode != "local" || state.BatchPath != "" || state.Brief == nil || state.Brief.Submission == nil || state.Source == nil || state.Source.RawText != state.Brief.Report.Source.Body || state.Source.SHA256 != state.Brief.Report.BodySHA256 || confirmation != state.Brief.Submission.Digest {
		return errors.New("explicit local Brief selection confirmation required")
	}
	return validateBriefWork(*state.Brief)
}

// SubmitBriefPending writes only a frozen source and one pending candidate.
func (s *Service) SubmitBriefPending(ctx context.Context, confirmation string) (State, error) {
	ctx, done, err := s.begin(ctx, "brief_submit")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if err := s.briefOnline(state, confirmation); err != nil {
		return s.finish(done, err, "請確認本機模式與完整候選 digest；未啟動 MCP。")
	}
	work := *state.Brief
	if work.Decision != nil || state.Settings.IntakeLauncher == "" || state.Settings.QueryLauncher == "" || (work.IntakeLauncher != "" && (work.IntakeLauncher != state.Settings.IntakeLauncher || work.QueryLauncher != state.Settings.QueryLauncher)) {
		return s.finish(done, errors.New("launchers changed"), "需明確 intake／Query launcher；重試須使用原 launcher，已有審查決定不重新提交。")
	}
	work.IntakeLauncher, work.QueryLauncher = state.Settings.IntakeLauncher, state.Settings.QueryLauncher
	work.Outcome, work.FailureStage = "submission_started", "intake"
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "送出前恢復紀錄未保存；未寫入。")
	}
	handoff, err := ahemcp.SubmitBrief(ctx, work.IntakeLauncher, *work.Submission)
	if err != nil {
		return s.finish(done, err, "提交結果未確認；可能已寫入，保留同一工作並用相同輸入重試，不重新抽取。")
	}
	work.Handoff, work.Outcome, work.FailureStage = &handoff, "submitted_unverified", "query"
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "MCP 已回覆，但收據保存未確認；保留送出前工作以精確重試。")
	}
	observation, err := ahemcp.ObserveBrief(ctx, work.QueryLauncher, *work.Submission, handoff)
	if err != nil {
		return s.finish(done, err, "MCP 收據已保存，但精確 Query 尚未確認；這不代表回滾。")
	}
	work.Outcome, work.FailureStage = observation.AdmissionOutcome, ""
	return s.finishBrief(done, work, "來源及候選已精確讀回；pending 不是採納。")
}

// QueryBriefPending observes the saved occurrence without resubmission.
func (s *Service) QueryBriefPending(ctx context.Context) (State, error) {
	ctx, done, err := s.begin(ctx, "brief_query")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.Brief == nil || state.Brief.Submission == nil || s.briefOnline(state, state.Brief.Submission.Digest) != nil || state.Brief.Handoff == nil || state.Settings.QueryLauncher == "" || state.Settings.QueryLauncher != state.Brief.QueryLauncher {
		return s.finish(done, errors.New("receipt required"), "精確查詢需同一工作、已保存收據、本機模式及原 Query launcher。")
	}
	work := *state.Brief
	work.FailureStage = "query"
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "查詢前紀錄未保存。")
	}
	observation, err := ahemcp.ObserveBrief(ctx, work.QueryLauncher, *work.Submission, *work.Handoff)
	if err != nil {
		return s.finish(done, err, "精確查詢未確認；舊觀測不是目前 DB 狀態，未重送。")
	}
	work.Outcome, work.FailureStage = observation.AdmissionOutcome, ""
	return s.finishBrief(done, work, "已精確讀回保存的提案；未執行處置。")
}

// PrepareBriefReview obtains and saves the complete native pending-only display.
func (s *Service) PrepareBriefReview(ctx context.Context, confirmation string) (State, error) {
	ctx, done, err := s.begin(ctx, "brief_review")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if s.briefOnline(state, confirmation) != nil || state.Brief.Handoff == nil || state.Brief.Decision != nil || state.Settings.ReviewLauncher == "" || state.Settings.QueryLauncher != state.Brief.QueryLauncher {
		return s.finish(done, errors.New("review unavailable"), "審查需精確收據、未決定候選及明確 reviewer／原 Query launcher。")
	}
	work := *state.Brief
	observation, err := ahemcp.ObserveBrief(ctx, work.QueryLauncher, *work.Submission, *work.Handoff)
	if err != nil || observation.AdmissionOutcome != "pending" {
		return s.finish(done, errors.New("not pending"), "Query 未確認此精確候選為 pending；未取得新的審查或執行處置。")
	}
	review, err := ahemcp.ReviewBrief(ctx, state.Settings.ReviewLauncher, *work.Submission, *work.Handoff)
	if err != nil {
		return s.finish(done, err, "無法取得相符的完整原生審查內容；未核准。")
	}
	work.Review, work.Outcome, work.FailureStage = &review, "review_ready", ""
	work.ReviewLauncher = state.Settings.ReviewLauncher
	return s.finishBrief(done, work, "精確審查已保存；請閱讀來源、引用與限制，再明確選擇處置與理由。")
}

// ApplyBriefReview durably binds an explicit decision before invoking the writer.
// Retries use that exact saved decision, never a newly prepared pending review.
func (s *Service) ApplyBriefReview(ctx context.Context, request BriefReviewRequest) (State, error) {
	ctx, done, err := s.begin(ctx, "brief_apply")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	state := s.Snapshot()
	if state.Brief == nil || state.Brief.Submission == nil || s.briefOnline(state, state.Brief.Submission.Digest) != nil || state.Brief.Review == nil || request.DisplayID != state.Brief.Review.Display.ID || !validBriefDecision(request.Outcome, request.Reason) || state.Settings.ReviewLauncher == "" || state.Settings.ReviewLauncher != state.Brief.ReviewLauncher || state.Settings.QueryLauncher == "" || state.Settings.QueryLauncher != state.Brief.QueryLauncher {
		return s.finish(done, errors.New("explicit review required"), "處置需本機模式、精確 display ID、明確結果與理由，以及 reviewer／原 Query launcher；未執行。")
	}
	work := *state.Brief
	decision := BriefDecision{DisplayID: request.DisplayID, Outcome: request.Outcome, Reason: request.Reason, ReviewLauncher: state.Settings.ReviewLauncher, QueryLauncher: state.Settings.QueryLauncher}
	if work.Decision != nil && *work.Decision != decision {
		return s.finish(done, errors.New("decision changed"), "此工作已有凍結決定；只能使用相同決定與原 launcher 重試，不能改寫。")
	}
	work.Decision, work.Outcome, work.FailureStage = &decision, "decision_saved", "review_write"
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "決定尚未安全保存；未啟動審查 writer。")
	}
	if decision.Outcome == "admit" {
		result, callErr := ahemcp.AdmitSourceClaim(ctx, decision.ReviewLauncher, *work.Review, decision.Reason)
		err = callErr
		if err == nil {
			work.Admission = &result
		}
	} else {
		result, callErr := ahemcp.DisposeSourceClaim(ctx, decision.ReviewLauncher, *work.Review, decision.Outcome, decision.Reason)
		err = callErr
		if err == nil {
			work.Disposition = &result
		}
	}
	if err != nil {
		return s.finish(done, err, "處置結果未確認，可能已生效；保留此決定並精確重試，不重新取得 pending 審查。")
	}
	work.Outcome, work.FailureStage = "decision_acknowledged", "review_readback"
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "處置已回覆，但收據保存未確認；保留原決定精確重試。")
	}
	if work.Admission != nil {
		err = ahemcp.VerifyBriefAdmitted(ctx, decision.QueryLauncher, *work.Submission, *work.Handoff, *work.Admission)
	} else {
		err = ahemcp.VerifyBriefDisposed(ctx, decision.QueryLauncher, *work.Submission, *work.Handoff, *work.Disposition)
	}
	if err != nil {
		return s.finish(done, err, "處置已回覆，Query 尚未驗證；不可當作回滾，請保留原決定重試。")
	}
	work.Outcome, work.FailureStage = decision.Outcome+"_verified", ""
	return s.finishBrief(done, work, "處置及精確 Query 已核對；這是操作與 DB 狀態紀錄，不是語意正確或真人身分認證。")
}

func (s *Service) finishBrief(done func(), work BriefWork, detail string) (State, error) {
	if err := s.saveBrief(work); err != nil {
		return s.finish(done, err, "本次結果或收據保存未確認；請保留前一版工作紀錄。若先前 MCP 操作已生效，保存失敗不代表回復原狀。")
	}
	return s.finish(done, nil, detail)
}

// OpenBriefWork reopens only this workspace's immutable record, without I/O to
// the original source, model, MCP or DB. Saved outcomes are historical receipts.
func (s *Service) OpenBriefWork(path string) (State, error) {
	_, done, err := s.begin(context.Background(), "brief_open")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	if filepath.Dir(path) != s.dataDir || filepath.Clean(path) != path || !strings.HasPrefix(filepath.Base(path), "brief-work-") {
		return s.finish(done, errors.New("foreign work path"), "僅可開啟目前私有工作區的 Brief 紀錄。")
	}
	raw, err := s.readPrivate(filepath.Base(path), 2<<20)
	if err != nil {
		return s.finish(done, err, "Brief 工作檔無法安全讀取。")
	}
	var work BriefWork
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&work) != nil || decoder.Decode(new(any)) != io.EOF || work.Path != path || filepath.Dir(work.SourcePath) != s.dataDir || validateBriefWork(work) != nil {
		return s.finish(done, errors.New("invalid work"), "Brief 工作內容、座標或 digest 不符；未連線。")
	}
	canonical, err := json.Marshal(work)
	if err != nil || !bytes.Equal(canonical, raw) {
		return s.finish(done, errors.New("noncanonical work"), "Brief 工作檔不是本工具保存的唯一 JSON 表示；不接受重複欄位或改寫。")
	}
	work.FailureStage = "historical_not_rechecked"
	work.Digest = briefWorkDigest(work)
	s.mu.Lock()
	s.state.Brief = &work
	s.state.Source = &SourceView{ID: "sha256:" + work.Report.BodySHA256, Title: work.Report.Source.SourceID, Path: work.SourcePath, Kind: "local", RawText: work.Report.Source.Body, SHA256: work.Report.BodySHA256, Bytes: len(work.Report.Source.Body), Rows: []SourceRow{}, Note: "保存的 Brief 原文；尚未重新查詢 DB。"}
	s.state.Candidates, s.state.Extraction, s.state.BatchResult = []CandidateView{}, nil, nil
	s.state.BatchPath, s.state.BatchDigest = "", ""
	s.mu.Unlock()
	return s.finish(done, nil, "Brief 工作已離線開啟；保存的結果是歷史收據，請使用原 launcher 精確查詢或重試。")
}
