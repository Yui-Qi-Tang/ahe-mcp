package detective

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOllamaPlannerRunnerUsesStrictBoundedRequest(t *testing.T) {
	input := plannerTestInput(t)
	responseJSON := `{"schema_version":"detective-planner-decision-v1","classification":"query_source","source_choice":"source-01"}`
	var captured ollamaPlannerGenerateRequest
	client := &http.Client{Transport: plannerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "http://127.0.0.1:11434/api/generate" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return plannerJSONResponse(http.StatusOK, ollamaPlannerGenerateResponse{Response: responseJSON, DoneReason: "stop"}), nil
	})}
	runner, err := NewOllamaPlannerRunner(OllamaPlannerConfig{
		Model: "gemma4:31b", HTTPClient: client, NumPredict: 192,
	})
	if err != nil {
		t.Fatalf("NewOllamaPlannerRunner() error = %v", err)
	}
	got, err := runner.Run(t.Context(), input)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(got) != responseJSON {
		t.Fatalf("planner output = %q", got)
	}
	if _, err := decodePlannerDecision(got); err != nil {
		t.Fatalf("strict planner decode: %v", err)
	}
	if captured.Model != "gemma4:31b" || captured.Stream || captured.Think {
		t.Fatalf("ollama request controls = %+v", captured)
	}
	if numericPlannerOption(t, captured.Options, "temperature") != 0 || numericPlannerOption(t, captured.Options, "num_predict") != 192 {
		t.Fatalf("ollama options = %+v", captured.Options)
	}
	format, ok := captured.Format.(map[string]any)
	if !ok || format["additionalProperties"] != false {
		t.Fatalf("format = %#v", captured.Format)
	}
	properties, ok := format["properties"].(map[string]any)
	if !ok {
		t.Fatalf("format properties = %#v", format["properties"])
	}
	sourceChoice, ok := properties["source_choice"].(map[string]any)
	if !ok || !jsonArrayContains(sourceChoice["enum"], "source-01") || !jsonArrayContains(sourceChoice["enum"], "") {
		t.Fatalf("source_choice schema = %#v", properties["source_choice"])
	}
	for _, required := range []string{
		`{"schema_version":"detective-planner-decision-v1"`,
		`"choice_key":"source-01"`,
		"Do not invent IDs, paths, versions, retries, backoff, evidence, relations, or actions.",
	} {
		if !strings.Contains(captured.Prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, captured.Prompt)
		}
	}
	if strings.Contains(captured.Prompt, "workspace-source:") || strings.Contains(captured.Prompt, "/private/") || strings.Contains(captured.Prompt, `"capability_version"`) {
		t.Fatalf("prompt exposed controller authority: %s", captured.Prompt)
	}
}

func TestOllamaPlannerRunnerReturnsModelOutputWithoutRecovery(t *testing.T) {
	modelOutput := "```json\n{\"schema_version\":\"detective-planner-decision-v1\"}\n```"
	client := &http.Client{Transport: plannerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return plannerJSONResponse(http.StatusOK, ollamaPlannerGenerateResponse{Response: modelOutput}), nil
	})}
	runner, err := NewOllamaPlannerRunner(OllamaPlannerConfig{Model: "gemma4:31b", HTTPClient: client})
	if err != nil {
		t.Fatalf("NewOllamaPlannerRunner() error = %v", err)
	}
	got, err := runner.Run(t.Context(), plannerTestInput(t))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(got) != modelOutput {
		t.Fatalf("runner normalized output = %q", got)
	}
	_, err = decodePlannerDecision(got)
	assertDetectiveKind(t, err, ErrorPlannerMalformedOutput)
}

func TestOllamaPlannerRunnerProviderFailures(t *testing.T) {
	oversized := strings.Repeat("x", maxPlannerOutputBytes+1)
	tests := []struct {
		name      string
		response  *http.Response
		wantError string
	}{
		{name: "http", response: plannerTextResponse(http.StatusServiceUnavailable, "offline"), wantError: "HTTP 503"},
		{name: "provider", response: plannerJSONResponse(http.StatusOK, ollamaPlannerGenerateResponse{Error: "model missing"}), wantError: "model missing"},
		{name: "malformed envelope", response: plannerTextResponse(http.StatusOK, "{"), wantError: "parsing ollama planner response"},
		{name: "length stop", response: plannerJSONResponse(http.StatusOK, ollamaPlannerGenerateResponse{Response: `{}`, DoneReason: "length"}), wantError: `done_reason "length"`},
		{name: "oversized output", response: plannerJSONResponse(http.StatusOK, ollamaPlannerGenerateResponse{Response: oversized}), wantError: "output exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: plannerRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return test.response, nil
			})}
			runner, err := NewOllamaPlannerRunner(OllamaPlannerConfig{Model: "gemma4:31b", HTTPClient: client})
			if err != nil {
				t.Fatalf("NewOllamaPlannerRunner() error = %v", err)
			}
			_, err = runner.Run(t.Context(), plannerTestInput(t))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestOllamaPlannerRunnerAppliesItsOwnTimeout(t *testing.T) {
	client := &http.Client{Transport: plannerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	runner, err := NewOllamaPlannerRunner(OllamaPlannerConfig{
		Model: "gemma4:31b", HTTPClient: client, Timeout: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOllamaPlannerRunner() error = %v", err)
	}
	_, err = runner.Run(context.Background(), plannerTestInput(t))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
}

func TestNewOllamaPlannerRunnerValidatesBoundedLoopbackConfig(t *testing.T) {
	tests := []OllamaPlannerConfig{
		{},
		{Model: "model", BaseURL: "http://ollama.example.test:11434"},
		{Model: "model", BaseURL: "file:///tmp/ollama.sock"},
		{Model: "model", NumPredict: -1},
		{Model: "model", NumPredict: maxOllamaPlannerNumPredict + 1},
		{Model: "model", Timeout: -time.Second},
		{Model: "model", Timeout: maxOllamaPlannerTimeout + time.Second},
	}
	for _, config := range tests {
		_, err := NewOllamaPlannerRunner(config)
		assertDetectiveKind(t, err, ErrorInvalidInput)
	}
	runner, err := NewOllamaPlannerRunner(OllamaPlannerConfig{
		Model: "model", BaseURL: "http://localhost:11434/api/generate",
	})
	if err != nil {
		t.Fatalf("loopback config error = %v", err)
	}
	if runner.endpoint != "http://localhost:11434/api/generate" || runner.numPredict != defaultOllamaPlannerNumPredict || runner.timeout != defaultOllamaPlannerTimeout {
		t.Fatalf("default runner = %+v", runner)
	}
}

func plannerTestInput(t *testing.T) PlannerInput {
	t.Helper()
	workspace := plannerTestWorkspace()
	run := plannerTestRun(workspace.ID, 2)
	context, err := buildPlannerContext(run, workspace)
	if err != nil {
		t.Fatalf("build planner test input: %v", err)
	}
	return context.input
}

type plannerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn plannerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func plannerJSONResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return plannerTextResponse(status, string(data))
}

func plannerTextResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func numericPlannerOption(t *testing.T, options map[string]any, key string) float64 {
	t.Helper()
	value, ok := options[key]
	if !ok {
		t.Fatalf("missing option %q in %+v", key, options)
	}
	switch value := value.(type) {
	case int:
		return float64(value)
	case float64:
		return value
	default:
		t.Fatalf("option %q type = %T", key, value)
		return 0
	}
}

func jsonArrayContains(value any, want string) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
