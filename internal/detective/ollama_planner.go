package detective

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
	// DefaultOllamaPlannerURL is the loopback Ollama endpoint default.
	DefaultOllamaPlannerURL = "http://127.0.0.1:11434"

	defaultOllamaPlannerTimeout    = 30 * time.Second
	maxOllamaPlannerTimeout        = 2 * time.Minute
	defaultOllamaPlannerNumPredict = 128
	maxOllamaPlannerNumPredict     = 512
	maxOllamaPlannerEnvelopeBytes  = 1 << 20
)

// OllamaPlannerConfig configures one bounded loopback Ollama planner runner.
type OllamaPlannerConfig struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
	NumPredict int
}

// OllamaPlannerRunner calls a loopback Ollama endpoint with a strict planner schema.
type OllamaPlannerRunner struct {
	endpoint   string
	model      string
	timeout    time.Duration
	httpClient *http.Client
	numPredict int
}

// NewOllamaPlannerRunner builds a bounded local planner runner.
func NewOllamaPlannerRunner(config OllamaPlannerConfig) (*OllamaPlannerRunner, error) {
	model := strings.TrimSpace(config.Model)
	if model == "" {
		return nil, newDomainError(ErrorInvalidInput, "ollama planner model is required")
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultOllamaPlannerTimeout
	}
	if timeout < 0 || timeout > maxOllamaPlannerTimeout {
		return nil, newDomainError(ErrorInvalidInput, "ollama planner timeout must be positive and at most %s", maxOllamaPlannerTimeout)
	}
	numPredict := config.NumPredict
	if numPredict == 0 {
		numPredict = defaultOllamaPlannerNumPredict
	}
	if numPredict < 1 || numPredict > maxOllamaPlannerNumPredict {
		return nil, newDomainError(ErrorInvalidInput, "ollama planner num_predict must be between 1 and %d", maxOllamaPlannerNumPredict)
	}
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultOllamaPlannerURL
	}
	endpoint, err := normalizeOllamaPlannerEndpoint(baseURL)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	return &OllamaPlannerRunner{
		endpoint: endpoint, model: model, timeout: timeout,
		httpClient: client, numPredict: numPredict,
	}, nil
}

// Run sends one controller-built planning context to Ollama and returns model output unchanged.
func (r *OllamaPlannerRunner) Run(ctx context.Context, input PlannerInput) ([]byte, error) {
	if r == nil {
		return nil, newDomainError(ErrorInvalidInput, "ollama planner runner is nil")
	}
	if err := validatePlannerInput(input); err != nil {
		return nil, err
	}
	prompt, err := BuildPlannerPrompt(input)
	if err != nil {
		return nil, err
	}
	format, err := PlannerDecisionSchema(input)
	if err != nil {
		return nil, err
	}
	requestBody := ollamaPlannerGenerateRequest{
		Model:  r.model,
		Prompt: prompt,
		Stream: false,
		Format: format,
		Think:  false,
		Options: map[string]any{
			"temperature": 0,
			"num_predict": r.numPredict,
		},
	}
	data, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encoding ollama planner request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, r.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating ollama planner request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("calling ollama planner: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxOllamaPlannerEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading ollama planner response: %w", err)
	}
	if len(body) > maxOllamaPlannerEnvelopeBytes {
		return nil, fmt.Errorf("ollama planner response exceeds %d bytes", maxOllamaPlannerEnvelopeBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama planner returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var output ollamaPlannerGenerateResponse
	if err := json.Unmarshal(body, &output); err != nil {
		return nil, fmt.Errorf("parsing ollama planner response: %w", err)
	}
	if output.Error != "" {
		return nil, fmt.Errorf("ollama planner error: %s", output.Error)
	}
	if output.DoneReason != "" && output.DoneReason != "stop" {
		return nil, fmt.Errorf("ollama planner stopped with done_reason %q", output.DoneReason)
	}
	if len(output.Response) > maxPlannerOutputBytes {
		return nil, fmt.Errorf("ollama planner output exceeds %d bytes", maxPlannerOutputBytes)
	}
	return []byte(output.Response), nil
}

// BuildPlannerPrompt returns the provider-neutral bounded planner prompt.
func BuildPlannerPrompt(input PlannerInput) (string, error) {
	if err := validatePlannerInput(input); err != nil {
		return "", err
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encoding planner input: %w", err)
	}
	return strings.Join([]string{
		"You are the bounded local source planner for AHE detective orchestration.",
		"Return exactly one JSON object with this schema and no other keys:",
		`{"schema_version":"detective-planner-decision-v1","classification":"query_source|abstain","source_choice":"source-NN or empty"}`,
		"Rules:",
		"- Choose only a source_choice listed in input.sources.",
		"- Choose query_source only for an unqueried option that can address incomplete observed coverage.",
		"- Choose abstain with an empty source_choice when there is no grounded recommendation.",
		"- Do not invent IDs, paths, versions, retries, backoff, evidence, relations, or actions.",
		"- The controller validates every field and owns all side effects.",
		"",
		"Input JSON:",
		string(data),
	}, "\n"), nil
}

// PlannerDecisionSchema returns the strict JSON schema for one planner input.
func PlannerDecisionSchema(input PlannerInput) (map[string]any, error) {
	if err := validatePlannerInput(input); err != nil {
		return nil, err
	}
	choices := make([]string, 0, len(input.Sources)+1)
	choices = append(choices, "")
	for _, source := range input.Sources {
		choices = append(choices, source.ChoiceKey)
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"schema_version", "classification", "source_choice"},
		"properties": map[string]any{
			"schema_version": map[string]any{
				"type": "string",
				"enum": []string{PlannerDecisionSchemaVersion},
			},
			"classification": map[string]any{
				"type": "string",
				"enum": []string{PlannerClassificationQuerySource, PlannerClassificationAbstain},
			},
			"source_choice": map[string]any{
				"type": "string",
				"enum": choices,
			},
		},
	}, nil
}

func normalizeOllamaPlannerEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if !strings.HasSuffix(baseURL, "/api/generate") {
		baseURL = strings.TrimRight(baseURL, "/") + "/api/generate"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", newDomainError(ErrorInvalidInput, "ollama planner base_url must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", newDomainError(ErrorInvalidInput, "ollama planner base_url must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", newDomainError(ErrorInvalidInput, "ollama planner base_url must not contain user info, query, or fragment")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", newDomainError(ErrorInvalidInput, "ollama planner base_url must use a loopback host")
	}
	return parsed.String(), nil
}

type ollamaPlannerGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Format  any            `json:"format"`
	Think   bool           `json:"think"`
	Options map[string]any `json:"options"`
}

type ollamaPlannerGenerateResponse struct {
	Response   string `json:"response"`
	DoneReason string `json:"done_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}
