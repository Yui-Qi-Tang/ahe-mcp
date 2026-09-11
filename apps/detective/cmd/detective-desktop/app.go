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

// LoadDemo starts a clearly labelled offline rehearsal, preserving saved files.
func (a *App) LoadDemo() (desktop.State, error) { return a.service.LoadDemo() }

// ChooseSource imports a user-selected local source, not an arbitrary model path.
func (a *App) ChooseSource() (desktop.State, error) {
	if a.service.Snapshot().Busy {
		return a.Snapshot(), errors.New("請先取消或等待目前工作完成")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "選擇來源文件（目前抽取支援 STATUS 表列）",
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
