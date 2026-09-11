package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsextract"
)

// NewNewsExtractor opens the explicitly selected loopback model using the same
// credential-free, no-retry transport as Desktop. It does not open a workspace,
// read settings, fetch news, or grant any AHE capability. Callers validate and
// freeze their selected source before invoking this factory.
func NewNewsExtractor(ctx context.Context, modelName, baseURL string) (*newsextract.Extractor, error) {
	if ctx == nil || ctx.Err() != nil || !safeText(modelName, 200) || strings.TrimSpace(modelName) != modelName {
		return nil, errors.New("news extraction requires an active context and an explicit model")
	}
	llm, err := localModel(ctx, Settings{Mode: "local", Model: modelName, BaseURL: baseURL})
	if err != nil {
		return nil, errors.New("news local model is unavailable or invalid")
	}
	return newsextract.NewExtractor(llm)
}
