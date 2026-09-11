package labstatus

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestExtractorRunsSingleTurnWorkflowAndValidatesResult(t *testing.T) {
	t.Parallel()
	document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
	modelCandidates := validCandidates()
	modelCandidates.Records[0].Citation.ExactQuote = ""
	raw, err := json.Marshal(modelCandidates)
	if err != nil {
		t.Fatal(err)
	}
	llm := &staticLLM{response: string(raw)}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := extractor.Extract(context.Background(), document)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got, want := envelope.SchemaVersion, SchemaVersion; got != want {
		t.Fatalf("SchemaVersion = %q, want %q", got, want)
	}
	if got, want := envelope.Source.SHA256, document.Source().SHA256; got != want {
		t.Fatalf("source SHA256 = %q, want %q", got, want)
	}
	if got, want := envelope.Extractor.Model, llm.Name(); got != want {
		t.Fatalf("model = %q, want %q", got, want)
	}
	if got, want := envelope.Result.Records[0].Citation.ExactQuote, "LAB PROVEN, runtime adoption OPEN."; got != want {
		t.Fatalf("hydrated exact quote = %q, want %q", got, want)
	}
	if llm.request == nil {
		t.Fatal("model did not receive a request")
	}
	if llm.request.Config == nil || llm.request.Config.ResponseSchema == nil {
		t.Fatal("model request did not include the output schema")
	}
	if got, want := llm.request.Config.ResponseMIMEType, "application/json"; got != want {
		t.Fatalf("ResponseMIMEType = %q, want %q", got, want)
	}
	if got, want := llm.request.Config.MaxOutputTokens, int32(maxExtractorOutputTokens); got != want {
		t.Fatalf("MaxOutputTokens = %d, want %d", got, want)
	}
	if !requestContains(llm.request, "000002 | LAB PROVEN, runtime adoption OPEN.") {
		t.Fatal("model request did not contain the numbered source line")
	}
}

func TestExtractorDedupesDuplicateModelRecords(t *testing.T) {
	t.Parallel()
	document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")

	modelCandidates := validCandidates()
	modelCandidates.Records[0].Citation.ExactQuote = ""
	modelCandidates.Records = append(modelCandidates.Records, modelCandidates.Records[0])
	raw, err := json.Marshal(modelCandidates)
	if err != nil {
		t.Fatal(err)
	}
	llm := &staticLLM{response: string(raw)}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := extractor.Extract(context.Background(), document)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got, want := len(envelope.Result.Records), 1; got != want {
		t.Fatalf("result record count = %d, want %d", got, want)
	}
}

func TestExtractorAcceptsMultipleFinalModelResponses(t *testing.T) {
	document := loadTestDocument(t, "# Status\nLAB PROVEN, runtime adoption OPEN.\n")
	candidates := validCandidates()
	candidates.Records[0].Citation.ExactQuote = ""
	raw, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	llm := &sequenceLLM{responses: []string{string(raw), string(raw)}}
	extractor, err := NewExtractor(llm)
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := extractor.Extract(context.Background(), document)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if got, want := len(envelope.Result.Records), 1; got != want {
		t.Fatalf("record count = %d, want %d", got, want)
	}
	if got, want := llm.calls, 1; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}

type staticLLM struct {
	response string
	request  *model.LLMRequest
}

func (m *staticLLM) Name() string { return "static-test-model" }

func (m *staticLLM) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.request = request
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content:      genai.NewContentFromText(m.response, genai.RoleModel),
			TurnComplete: true,
		}, nil)
	}
}

type sequenceLLM struct {
	responses []string
	calls     int
}

func (m *sequenceLLM) Name() string { return "sequence-test-model" }

func (m *sequenceLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.calls++
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, response := range m.responses {
			if !yield(&model.LLMResponse{
				Content:      genai.NewContentFromText(response, genai.RoleModel),
				TurnComplete: true,
			}, nil) {
				return
			}
		}
	}
}

func requestContains(request *model.LLMRequest, target string) bool {
	for _, content := range request.Contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && strings.Contains(part.Text, target) {
				return true
			}
		}
	}
	return false
}
