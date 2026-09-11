package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// NewGuidedReviewer opens only the explicit loopback model. It reuses Desktop's
// credential-free, no-retry transport without opening a workspace, sources or
// AHE capabilities. Callers validate and freeze their input before this call.
func NewGuidedReviewer(ctx context.Context, modelName, baseURL string) (*sourcepilot.GuidedReviewer, error) {
	if ctx == nil || ctx.Err() != nil || !safeText(modelName, 200) || strings.TrimSpace(modelName) != modelName {
		return nil, errors.New("claim check requires an active context and an explicit model")
	}
	llm, err := localModel(ctx, Settings{Mode: "local", Model: modelName, BaseURL: baseURL})
	if err != nil {
		return nil, errors.New("claim check local model is unavailable or invalid")
	}
	return sourcepilot.NewGuidedReviewer(llm)
}
