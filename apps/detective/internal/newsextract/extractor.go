// Package newsextract produces bounded, unreviewed candidates from one source item.
// It does not fetch sources, persist results, execute tools, or grant admission.
package newsextract

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsfeed"
)

const (
	// SchemaVersion identifies the controller-owned result contract.
	SchemaVersion = "detective-news-extraction/v1"
	// ExtractorVersion identifies this limited source-field extractor.
	ExtractorVersion = "0.1.3"
	// PromptVersion identifies the fixed instruction and input projection.
	PromptVersion  = "detective-news-extraction-prompt/v4"
	maxInputBytes  = 64 << 10
	maxOutputBytes = 16 << 10
)

var (
	// ErrInput indicates that the selected item exceeds the extraction contract.
	ErrInput = errors.New("invalid news extraction input")
	// ErrModelResponse indicates that a single complete text response was not obtained.
	ErrModelResponse = errors.New("news extraction response incomplete or unsupported")
	// ErrOutput indicates that model JSON failed the sealed candidate contract.
	ErrOutput = errors.New("invalid news extraction output")
	// ErrNASAAttribution indicates a blocked literal attribution or endorsement phrase.
	ErrNASAAttribution = errors.New("news result contains restricted NASA attribution phrasing")
)

// Citation is copied from an entire selected source field by the controller.
type Citation struct {
	Field      string `json:"field"`
	ExactQuote string `json:"exact_quote"`
}

// Record is an unreviewed model interpretation, not an established world fact.
type Record struct {
	Statement      string     `json:"statement"`
	ReportedStatus string     `json:"reported_status"`
	Attribution    string     `json:"attribution"`
	EventTime      string     `json:"event_time"`
	Location       string     `json:"location"`
	EvidenceFields []string   `json:"evidence_fields"`
	Citations      []Citation `json:"citations"`
}

// Result separates a bounded extraction from a legitimate abstention.
// Version fields, citations, and mandatory limitations are controller-owned.
type Result struct {
	SchemaVersion    string   `json:"schema_version"`
	ExtractorVersion string   `json:"extractor_version"`
	PromptVersion    string   `json:"prompt_version"`
	Outcome          string   `json:"outcome"`
	Records          []Record `json:"records"`
	AbstentionReason string   `json:"abstention_reason"`
	Limitations      []string `json:"limitations"`
}

// Extractor runs a caller-supplied model with no tools and no retries.
// The caller owns transport restrictions and deadlines.
type Extractor struct {
	model model.LLM
}

// NewExtractor constructs an extractor without contacting the model.
func NewExtractor(llm model.LLM) (*Extractor, error) {
	if llm == nil {
		return nil, errors.New("news extraction model is required")
	}
	return &Extractor{model: llm}, nil
}

// ValidateItem checks the exact model-input projection without model or file I/O.
// It shares Extract's bounds; it does not verify a snapshot or source receipt.
func ValidateItem(item newsfeed.Item) error {
	_, err := modelInput(item)
	return err
}

// Extract processes one already-selected item. ID, URL, other items, and raw
// XML/JSON never enter the model request. Exact citations establish only field identity.
func (e *Extractor) Extract(ctx context.Context, item newsfeed.Item) (Result, error) {
	if ctx == nil {
		return Result{}, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	input, err := modelInput(item)
	if err != nil {
		return Result{}, err
	}
	if e == nil || e.model == nil {
		return Result{}, ErrModelResponse
	}
	temperature := float32(0)
	request := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText(input, genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(instruction, genai.RoleUser),
			Temperature:       &temperature, MaxOutputTokens: 1024,
			ResponseMIMEType: "application/json", ResponseJsonSchema: outputSchema(),
		},
	}
	var text string
	count := 0
	for response, modelErr := range e.model.GenerateContent(ctx, request, false) {
		count++
		if count != 1 || modelErr != nil {
			return Result{}, responseError(ctx)
		}
		text, err = finalText(response)
		if err != nil {
			return Result{}, responseError(ctx)
		}
	}
	if count != 1 {
		return Result{}, responseError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result, err := parseResult(text, item)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func responseError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrModelResponse
}

func modelInput(item newsfeed.Item) (string, error) {
	if !sourceText(item.Title, 16<<10, false) || !sourceText(item.Description, 32<<10, true) || !sourceText(item.PublishedAt, 256, true) {
		return "", ErrInput
	}
	// This explicit projection prevents future Item metadata from entering prompts.
	input := struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		PublishedAt string `json:"published_at"`
	}{item.Title, item.Description, item.PublishedAt}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxInputBytes {
		return "", ErrInput
	}
	return string(body), nil
}

func sourceText(value string, limit int, allowEmpty bool) bool {
	if len(value) > limit || !utf8.ValidString(value) || (!allowEmpty && strings.TrimSpace(value) == "") {
		return false
	}
	for _, r := range value {
		// Decoded source fields can legitimately contain line breaks and indentation.
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func finalText(response *model.LLMResponse) (string, error) {
	if response == nil || response.ErrorCode != "" || response.ErrorMessage != "" || response.Partial || response.Interrupted || response.FinishReason != genai.FinishReasonStop || response.Content == nil || response.Content.Role != genai.RoleModel || response.CitationMetadata != nil || response.GroundingMetadata != nil || !adapterMetadata(response.CustomMetadata) || response.InputTranscription != nil || response.OutputTranscription != nil {
		return "", ErrModelResponse
	}
	var text strings.Builder
	for _, part := range response.Content.Parts {
		if part == nil || part.Thought || len(part.ThoughtSignature) != 0 || part.FunctionCall != nil || part.FunctionResponse != nil || part.ToolCall != nil || part.ToolResponse != nil || part.ExecutableCode != nil || part.CodeExecutionResult != nil || part.InlineData != nil || part.FileData != nil || part.MediaResolution != nil || part.VideoMetadata != nil || part.AudioTranscription != nil || len(part.PartMetadata) != 0 {
			return "", ErrModelResponse
		}
		if len(part.Text) > maxOutputBytes-text.Len() {
			return "", ErrModelResponse
		}
		text.WriteString(part.Text)
	}
	if strings.TrimSpace(text.String()) == "" || !utf8.ValidString(text.String()) {
		return "", ErrModelResponse
	}
	return text.String(), nil
}

func adapterMetadata(values map[string]any) bool {
	// ADK v2.3.0 openaimodel.attachMetadata adds exactly these two strings.
	// Response.Model's shared.ResponsesModel is a string alias in openai-go v3.52.0.
	// Neither value is copied into candidate results or treated as authority.
	for key, value := range values {
		if key != "openai_response_id" && key != "openai_model" {
			return false
		}
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}
