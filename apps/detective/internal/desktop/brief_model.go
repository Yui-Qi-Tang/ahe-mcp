package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// NewBriefExtractor opens the explicit local model without a Desktop workspace,
// source access or AHE capabilities. Callers validate and freeze input first.
func NewBriefExtractor(ctx context.Context, modelName, baseURL string) (*sourcepilot.BriefExtractor, error) {
	if ctx == nil || ctx.Err() != nil || !safeText(modelName, 200) || strings.TrimSpace(modelName) != modelName {
		return nil, errors.New("brief requires an active context and an explicit model")
	}
	llm, err := localModel(ctx, Settings{Mode: "local", Model: modelName, BaseURL: baseURL})
	if err != nil {
		return nil, errors.New("brief local model is unavailable or invalid")
	}
	return sourcepilot.NewBriefExtractor(llm)
}
