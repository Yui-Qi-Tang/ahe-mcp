package main

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/desktop"
)

// App is the narrow native bridge; writes require explicit typed confirmation.
// It exposes no SQL, shell, or chat-triggered writer.
type App struct {
	ctx     context.Context
	service *desktop.Service
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// Snapshot returns the current detached desktop projection.
func (a *App) Snapshot() desktop.State { return a.service.Snapshot() }

// SaveSettings changes explicit connection settings without connecting.
func (a *App) SaveSettings(settings desktop.Settings) (desktop.State, error) {
	return a.service.SaveSettings(settings)
}

// ChooseCodebaseRepository creates a draft, not a connection or index.
func (a *App) ChooseCodebaseRepository() (desktop.Connection, error) {
	if a.service.Snapshot().Busy {
		return desktop.Connection{}, errors.New("請先等待目前工作完成")
	}
	path, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "選擇 Codebase 可讀取的 repository"})
	if err != nil {
		return desktop.Connection{}, errors.New("無法開啟資料夾選擇視窗")
	}
	if path == "" {
		return desktop.Connection{}, nil
	}
	return a.service.CodebasePreset(path)
}

func (a *App) IndexCodebase(id string) (desktop.State, error) {
	return a.service.IndexCodebase(a.ctx, id)
}

func (a *App) StartSourceChat(id, question string) (desktop.State, error) {
	return a.service.StartSourceChat(a.ctx, id, question)
}

func (a *App) ConfirmSourceChat(id string) (desktop.State, error) {
	return a.service.ConfirmSourceChat(a.ctx, id)
}

// NewWork starts an empty work without changing connections or saved files.
func (a *App) NewWork() (desktop.State, error) { return a.service.NewWork() }

// LoadDemo starts a clearly labelled offline rehearsal, preserving saved files.
func (a *App) LoadDemo() (desktop.State, error) { return a.service.LoadDemo() }

// ChooseSource imports a user-selected local source, not an arbitrary model path.
func (a *App) ChooseSource() (desktop.State, error) {
	if a.service.Snapshot().Busy {
		return a.Snapshot(), errors.New("請先取消或等待目前工作完成")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "選擇來源文件（原文選段或 STATUS 表列）",
		Filters: []runtime.FileFilter{{DisplayName: "Markdown / Text", Pattern: "*.md;*.txt"}},
	})
	if err != nil {
		return a.Snapshot(), errors.New("無法開啟來源選擇視窗")
	}
	if path == "" {
		return a.Snapshot(), nil
	}
	return a.service.ImportSource(path)
}

// ChooseSourceReceipt selects a saved capture for offline inspection only.
func (a *App) ChooseSourceReceipt() (desktop.State, error) {
	state := a.Snapshot()
	if state.Busy || state.BatchPath != "" || state.BatchDigest != "" {
		return state, errors.New("請先等待目前工作完成；既有候選批次不能替換來源")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "開啟已保存來源收據（離線核對）",
		Filters: []runtime.FileFilter{{DisplayName: "Saved source receipt", Pattern: "*.md;*.json"}},
	})
	if err != nil {
		return a.Snapshot(), errors.New("無法開啟來源收據選擇視窗")
	}
	if path == "" {
		return a.Snapshot(), nil
	}
	return a.service.OpenSourceReceipt(a.ctx, path)
}

// ChooseBatch reopens an exact private batch without rerunning extraction.
func (a *App) ChooseBatch() (desktop.State, error) {
	if a.service.Snapshot().Busy {
		return a.Snapshot(), errors.New("請先取消或等待目前工作完成")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "恢復已保存的 Detective batch index",
		Filters: []runtime.FileFilter{{DisplayName: "Batch index JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return a.Snapshot(), errors.New("無法開啟批次選擇視窗")
	}
	if path == "" {
		return a.Snapshot(), nil
	}
	return a.service.OpenBatch(path)
}

// PrepareTask freezes an explicit objective against the current saved source.
func (a *App) PrepareTask(request desktop.TaskDraftRequest) (desktop.State, error) {
	return a.service.PrepareTask(request)
}

// RunTask selects original text for the exact prepared input, without intake.
func (a *App) RunTask(inputID string) (desktop.State, error) {
	return a.service.RunTask(a.ctx, inputID)
}

// ChooseTaskRecord reopens a saved task run without model or database operations.
func (a *App) ChooseTaskRecord() (desktop.State, error) {
	state := a.service.Snapshot()
	if state.Busy {
		return a.Snapshot(), errors.New("請先取消或等待目前工作完成")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "開啟此工作區已保存的任務選段紀錄（離線核對）",
		DefaultDirectory: state.DataDir,
		Filters:          []runtime.FileFilter{{DisplayName: "Task run JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return a.Snapshot(), errors.New("無法開啟任務紀錄選擇視窗")
	}
	if path == "" {
		return a.Snapshot(), nil
	}
	return a.service.OpenTaskRecord(path)
}

// ChooseBriefSource imports a caller-declared JSON source without model or DB I/O.
func (a *App) ChooseBriefSource() (desktop.State, error) {
	if a.service.Snapshot().Busy {
		return a.Snapshot(), errors.New("請先取消或等待目前工作完成")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "選擇 Brief 來源 JSON（保留完整 body）",
		Filters: []runtime.FileFilter{{DisplayName: "Brief source JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return a.Snapshot(), errors.New("無法開啟 Brief 來源選擇視窗")
	}
	if path == "" {
		return a.Snapshot(), nil
	}
	return a.service.ImportBriefSource(path)
}

// ChooseBriefWork reopens a saved local work record without network operations.
func (a *App) ChooseBriefWork() (desktop.State, error) {
	if a.service.Snapshot().Busy {
		return a.Snapshot(), errors.New("請先取消或等待目前工作完成")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "恢復此工作區已保存的 Brief 工作",
		Filters: []runtime.FileFilter{{DisplayName: "Brief work JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return a.Snapshot(), errors.New("無法開啟 Brief 工作選擇視窗")
	}
	if path == "" {
		return a.Snapshot(), nil
	}
	return a.service.OpenBriefWork(path)
}

// ExtractBrief makes an explicit local-model reading request without intake.
func (a *App) ExtractBrief() (desktop.State, error) {
	return a.service.ExtractBrief(a.ctx)
}

// PrepareBriefCandidate saves a user-selected statement and exact source range.
func (a *App) PrepareBriefCandidate(request desktop.BriefCandidateRequest) (desktop.State, error) {
	return a.service.PrepareBriefCandidate(request)
}

// SubmitBriefPending writes only the exact candidate matching this digest.
func (a *App) SubmitBriefPending(confirmation string) (desktop.State, error) {
	return a.service.SubmitBriefPending(a.ctx, confirmation)
}

// QueryBriefPending observes the saved Brief handoff without intake.
func (a *App) QueryBriefPending() (desktop.State, error) {
	return a.service.QueryBriefPending(a.ctx)
}

// PrepareBriefReview obtains the exact native display without recording a decision.
func (a *App) PrepareBriefReview(confirmation string) (desktop.State, error) {
	return a.service.PrepareBriefReview(a.ctx, confirmation)
}

// ApplyBriefReview submits an explicit decision for the current exact display.
func (a *App) ApplyBriefReview(request desktop.BriefReviewRequest) (desktop.State, error) {
	return a.service.ApplyBriefReview(a.ctx, request)
}

// SendMessage requests tool-free conversation; text never authorizes a writer.
func (a *App) SendMessage(text string) (desktop.State, error) {
	return a.service.SendMessage(a.ctx, text)
}

// Extract prepares candidates from one explicitly selected source row.
func (a *App) Extract(line int) (desktop.State, error) { return a.service.Extract(a.ctx, line) }

// SubmitPending confirms the exact saved batch before its bounded intake.
func (a *App) SubmitPending(digest string) (desktop.State, error) {
	return a.service.SubmitPending(a.ctx, digest)
}

// QueryPending observes the existing batch without intake or model calls.
func (a *App) QueryPending() (desktop.State, error) { return a.service.QueryPending(a.ctx) }

// SearchEvidence reads existing evidence using only the saved Query launcher.
func (a *App) SearchEvidence(request desktop.EvidenceSearchRequest) (desktop.State, error) {
	return a.service.SearchEvidence(a.ctx, request)
}

// LoadEvidenceSearchDemo displays a fixed offline example without connecting.
func (a *App) LoadEvidenceSearchDemo(scenario string) (desktop.State, error) {
	return a.service.LoadEvidenceSearchDemo(scenario)
}

// Cancel requests cancellation; it does not claim a DB rollback.
func (a *App) Cancel() desktop.State { return a.service.Cancel() }

// DiscoverTools lists the explicitly configured source server's allowed tools.
func (a *App) DiscoverTools(id string) (desktop.State, error) {
	return a.service.DiscoverTools(a.ctx, id)
}

// SuggestSourceTool requests advice only; applying it still requires manual confirmation.
func (a *App) SuggestSourceTool(id, question string) (desktop.State, error) {
	return a.service.SuggestSourceTool(a.ctx, id, question)
}

// CallSourceTool executes only an exact, confirmed and allowlisted source call.
func (a *App) CallSourceTool(id, tool, argsJSON, confirmation string) (desktop.State, error) {
	return a.service.CallSourceTool(a.ctx, id, tool, argsJSON, confirmation)
}

// BeginAtlassianLogin returns a consent link; it does not open a browser.
func (a *App) BeginAtlassianLogin(id string) (desktop.State, error) {
	return a.service.BeginAtlassianLogin(a.ctx, id)
}

// DisconnectAtlassian forgets the local login and discovered tools.
func (a *App) DisconnectAtlassian(id string) (desktop.State, error) {
	return a.service.DisconnectAtlassian(id)
}
