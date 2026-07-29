package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ExtractorOllamaLocal identifies the Slice 8 local Ollama extractor adapter.
	ExtractorOllamaLocal = "ollama-local"
	// ExtractorOllamaLocalVersion is the adapter/prompt contract version.
	ExtractorOllamaLocalVersion = "v1"
	// DefaultOllamaExtractorURL is the local Ollama endpoint default.
	DefaultOllamaExtractorURL = "http://127.0.0.1:11434"
	// OllamaExtractorPromptBoundedExactQuote selects the source-quote-only prompt.
	OllamaExtractorPromptBoundedExactQuote = "bounded-exact-quote-v1"

	defaultOllamaExtractorTimeout = 60 * time.Second
	ollamaExtractorPromptVersion  = "ahe-evidence-extractor-prompt-v1"
	ollamaExactQuotePromptVersion = "ahe-bounded-exact-quote-extractor-prompt-v1"
)

// OllamaExtractorConfig configures the local Ollama runner.
type OllamaExtractorConfig struct {
	BaseURL      string
	Model        string
	Timeout      time.Duration
	HTTPClient   *http.Client
	NumPredict   int
	PromptMode   string
	MaxProposals int
}

// OllamaExtractorRunner calls Ollama and returns raw extractor output bytes.
type OllamaExtractorRunner struct {
	endpoint     string
	model        string
	httpClient   *http.Client
	numPredict   int
	promptMode   string
	maxProposals int
}

// NewOllamaExtractorRunner builds a bounded local Ollama extractor runner.
func NewOllamaExtractorRunner(config OllamaExtractorConfig) (*OllamaExtractorRunner, error) {
	model := strings.TrimSpace(config.Model)
	if model == "" {
		return nil, newDomainError(ErrorInvalidInput, "ollama model is required")
	}
	if config.NumPredict < 0 {
		return nil, newDomainError(ErrorInvalidInput, "ollama num_predict must be non-negative")
	}
	promptMode := strings.TrimSpace(config.PromptMode)
	if promptMode != "" && promptMode != OllamaExtractorPromptBoundedExactQuote {
		return nil, newDomainError(ErrorInvalidInput, "unsupported ollama extractor prompt mode %q", promptMode)
	}
	if promptMode == OllamaExtractorPromptBoundedExactQuote {
		if config.MaxProposals < 1 {
			return nil, newDomainError(ErrorInvalidInput, "bounded exact-quote max_proposals must be positive")
		}
	} else if config.MaxProposals != 0 {
		return nil, newDomainError(ErrorInvalidInput, "max_proposals requires bounded exact-quote prompt mode")
	}
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultOllamaExtractorURL
	}
	endpoint, err := normalizeOllamaGenerateEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		timeout := config.Timeout
		if timeout <= 0 {
			timeout = defaultOllamaExtractorTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &OllamaExtractorRunner{
		endpoint:     endpoint,
		model:        model,
		httpClient:   &boundedClient,
		numPredict:   config.NumPredict,
		promptMode:   promptMode,
		maxProposals: config.MaxProposals,
	}, nil
}

// ExtractorDefinition returns the provenance identity this runner should attach to attempts.
func (r *OllamaExtractorRunner) ExtractorDefinition() ExtractorDefinitionInput {
	if r == nil {
		return ExtractorDefinitionInput{}
	}
	promptVersion := ollamaExtractorPromptVersion
	if r.promptMode == OllamaExtractorPromptBoundedExactQuote {
		promptVersion = ollamaExactQuotePromptVersion
	}
	config := map[string]string{
		"format":         "json",
		"model":          r.model,
		"prompt_version": promptVersion,
		"provider":       "ollama",
		"temperature":    "0",
		"think":          "false",
	}
	if r.numPredict > 0 {
		config["num_predict"] = fmt.Sprintf("%d", r.numPredict)
	}
	if r.promptMode == OllamaExtractorPromptBoundedExactQuote {
		config["max_proposals"] = fmt.Sprintf("%d", r.maxProposals)
		config["output_contract"] = OllamaExtractorPromptBoundedExactQuote
	}
	return ExtractorDefinitionInput{
		Name:    ExtractorOllamaLocal,
		Version: ExtractorOllamaLocalVersion,
		Config:  config,
	}
}

// Run sends grounded extractor input to Ollama and returns the provider response bytes unchanged.
func (r *OllamaExtractorRunner) Run(ctx context.Context, input ExtractorInput) ([]byte, error) {
	if r == nil {
		return nil, newDomainError(ErrorInvalidInput, "ollama extractor runner is nil")
	}
	prompt, err := buildOllamaExtractorPrompt(input, r.promptMode, r.maxProposals)
	if err != nil {
		return nil, err
	}
	options := map[string]any{
		"temperature": 0,
	}
	if r.numPredict > 0 {
		options["num_predict"] = r.numPredict
	}
	reqBody := ollamaExtractorGenerateRequest{
		Model:   r.model,
		Prompt:  prompt,
		Stream:  false,
		Format:  "json",
		Think:   false,
		Options: options,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encoding ollama extractor request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating ollama extractor request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling ollama extractor: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading ollama extractor response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama extractor returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out ollamaExtractorGenerateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parsing ollama extractor response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama extractor error: %s", out.Error)
	}
	return []byte(out.Response), nil
}

func buildOllamaExtractorPrompt(input ExtractorInput, promptMode string, maxProposals int) (string, error) {
	inputJSON, err := jsonBytes(input)
	if err != nil {
		return "", err
	}
	if promptMode == OllamaExtractorPromptBoundedExactQuote {
		return strings.Join([]string{
			"You extract objective source statements for AHE evidence ingestion.",
			"Return exactly one JSON object with this shape:",
			`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"exact source quote","evidence_refs":["span:S1"]}]}`,
			"Rules:",
			fmt.Sprintf("- Return at most %d proposals. Return {\"proposals\":[]} when no objective statement is present.", maxProposals),
			"- Copy statement_text exactly from one referenced span. Do not paraphrase, summarize, infer, or combine text.",
			"- Use only span IDs present in input.spans.",
			"- Do not answer questions, recommend actions, classify truth, or create causal or derived relations.",
			"- Do not include byte offsets, authoritative source refs, canonical IDs, proposal fingerprints, admission outcomes, markdown, or prose.",
			"",
			"Bounded input JSON:",
			string(inputJSON),
		}, "\n"), nil
	}
	return strings.Join([]string{
		"You are a local extractor for AHE evidence ingestion.",
		"Return exactly one JSON object with this shape:",
		`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"...","evidence_refs":["span:S1"]}]}`,
		"Rules:",
		"- Use only span IDs present in input.spans.",
		"- Do not include byte offsets, authoritative source refs, canonical IDs, proposal fingerprints, admission outcomes, markdown, or prose.",
		"- Keep statement_text directly grounded in the referenced spans.",
		"",
		"Input JSON:",
		string(inputJSON),
	}, "\n"), nil
}

func normalizeOllamaGenerateEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", newDomainError(ErrorInvalidInput, "parsing ollama base_url: %v", err)
	}
	if parsed.Scheme != "http" {
		return "", newDomainError(ErrorInvalidInput, "ollama base_url must use http")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", newDomainError(ErrorInvalidInput, "ollama base_url must not include credentials, query, or fragment")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", newDomainError(ErrorInvalidInput, "ollama base_url must use a loopback host")
	}
	if parsed.Host == "" {
		return "", newDomainError(ErrorInvalidInput, "ollama base_url host is required")
	}
	switch parsed.Path {
	case "", "/":
		parsed.Path = "/api/generate"
	case "/api/generate":
	default:
		return "", newDomainError(ErrorInvalidInput, "ollama base_url path must be /api/generate")
	}
	return parsed.String(), nil
}

type ollamaExtractorGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Format  any            `json:"format"`
	Think   bool           `json:"think"`
	Options map[string]any `json:"options"`
}

type ollamaExtractorGenerateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}
