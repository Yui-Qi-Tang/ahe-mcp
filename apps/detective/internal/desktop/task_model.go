package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

// ExtractTask freezes and validates input before opening the explicitly chosen
// loopback model. It does not open a Desktop workspace, collect sources, persist
// results or acquire AHE capabilities. Preflight/connection failures return no
// result; once generation starts, taskextract supplies the bounded result.
func ExtractTask(ctx context.Context, task taskextract.Task, source taskextract.Source, modelName, baseURL string) (taskextract.Result, error) {
	if ctx == nil {
		return taskextract.Result{}, errors.New("task extraction requires an active context")
	}
	if err := ctx.Err(); err != nil {
		return taskextract.Result{}, err
	}
	if !safeText(modelName, 200) || strings.TrimSpace(modelName) != modelName {
		return taskextract.Result{}, errors.New("task extraction requires an explicit valid model")
	}
	request, err := taskextract.Prepare(task, source, modelName)
	if err != nil {
		return taskextract.Result{}, err
	}
	if len(request.Units()) == 0 {
		return taskextract.Result{}, errors.New("task extraction has no readable supplied source")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	llm, err := localModel(ctx, Settings{Mode: "local", Model: modelName, BaseURL: baseURL})
	if err != nil {
		if ctx.Err() != nil {
			return taskextract.Result{}, ctx.Err()
		}
		return taskextract.Result{}, errors.New("task local model is unavailable or invalid")
	}
	return taskextract.Extract(ctx, llm, request)
}
