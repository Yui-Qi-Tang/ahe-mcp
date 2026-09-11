package labstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// Extractor runs one tool-free, single-turn ADK agent and validates its output.
type Extractor struct {
	model model.LLM
}

type statusConstraint struct {
	clause  string
	allowed []string
}

const (
	maxModelJSONBytes        = 1024 * 1024
	maxExtractorOutputTokens = 1024
)

// NewExtractor constructs a concrete Lab status extractor.
func NewExtractor(llm model.LLM) (*Extractor, error) {
	if llm == nil {
		return nil, fmt.Errorf("model is required")
	}
	return &Extractor{model: llm}, nil
}

// Extract produces candidate records from one already-loaded source document.
func (e *Extractor) Extract(ctx context.Context, document *Document) (Envelope, error) {
	return e.extract(ctx, document, nil)
}

func (e *Extractor) extract(ctx context.Context, document *Document, constraint *statusConstraint) (Envelope, error) {
	if document == nil {
		return Envelope{}, fmt.Errorf("document is required")
	}

	temperature := float32(0)
	a, err := llmagent.New(llmagent.Config{
		Name:                     "lab_status_extractor",
		Description:              "Extracts grounded candidate status records from one STATUS.md document.",
		Model:                    e.model,
		Mode:                     llmagent.ModeChat,
		IncludeContents:          llmagent.IncludeContentsNone,
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
		Instruction:              agentInstruction,
		OutputSchema:             OutputSchema(),
		GenerateContentConfig: &genai.GenerateContentConfig{
			Temperature:     &temperature,
			MaxOutputTokens: maxExtractorOutputTokens,
		},
	})
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: create ADK agent: %w", ErrModelResponse, err)
	}
	r, err := runner.NewInMemory("detective", a)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: create ADK runner: %w", ErrModelResponse, err)
	}

	message := genai.NewContentFromText(buildPromptWithStatusConstraint(document, constraint), genai.RoleUser)
	var finalText string
	for event, runErr := range r.Run(ctx, "detective", document.source.SHA256, message, agent.RunConfig{StreamingMode: agent.StreamingModeNone}) {
		if runErr != nil {
			return Envelope{}, fmt.Errorf("%w: run ADK agent: %w", ErrModelResponse, runErr)
		}
		if event.ErrorCode != "" || event.ErrorMessage != "" {
			return Envelope{}, fmt.Errorf("%w: model response: %s", ErrModelResponse, event.ErrorMessage)
		}
		// ADK can surface final text after a token limit or filtering stop.
		// Valid JSON alone does not make that response a completed extraction.
		// An empty finish reason remains supported for custom model adapters.
		if event.FinishReason != "" && event.FinishReason != genai.FinishReasonStop {
			return Envelope{}, fmt.Errorf("%w: model finish reason: %s", ErrModelResponse, event.FinishReason)
		}
		if !event.IsFinalResponse() || event.Content == nil {
			continue
		}

		var response strings.Builder
		for _, part := range event.Content.Parts {
			if part == nil || part.Thought {
				continue
			}
			response.WriteString(part.Text)
			if response.Len() > maxModelJSONBytes {
				return Envelope{}, fmt.Errorf("%w: model JSON exceeds %d bytes", ErrModelJSON, maxModelJSONBytes)
			}
		}
		if strings.TrimSpace(response.String()) != "" {
			finalText = response.String()
		}
	}
	if strings.TrimSpace(finalText) == "" {
		return Envelope{}, fmt.Errorf("%w: model returned no final JSON response", ErrModelJSON)
	}

	candidates, err := decodeCandidateSet(finalText)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrModelJSON, err)
	}
	candidates = dedupeCandidateRecords(candidates)
	if err := hydrateCitations(document, &candidates); err != nil {
		return Envelope{}, fmt.Errorf("%w: hydrate model citations: %w", ErrCitation, err)
	}
	if err := ValidateCandidateSet(document, candidates); err != nil {
		return Envelope{}, fmt.Errorf("%w: validate model candidates: %w", ErrCandidateValidation, err)
	}
	for _, record := range candidates.Records {
		if err := ValidateStatementForm(record); err != nil {
			return Envelope{}, fmt.Errorf("%w: %w", ErrCandidateValidation, err)
		}
	}
	if constraint != nil {
		for index, record := range candidates.Records {
			if !contains(constraint.allowed, record.Status) {
				return Envelope{}, fmt.Errorf("%w: validate status clause %q: record %d status %q not allowed; want one of %v", ErrCandidateValidation, constraint.clause, index, record.Status, constraint.allowed)
			}
		}
	}

	return Envelope{
		SchemaVersion: SchemaVersion,
		Source:        document.Source(),
		Extractor: ExtractorInfo{
			Name:    ExtractorName,
			Version: ExtractorVersion,
			Model:   e.model.Name(),
		},
		Result: candidates,
	}, nil
}

func dedupeCandidateRecords(candidates CandidateSet) CandidateSet {
	records := candidates.Records
	if len(records) == 0 {
		return candidates
	}

	seen := make(map[string]struct{}, len(records))
	deduped := make([]Record, 0, len(records))
	for _, record := range records {
		// Only byte-equivalent model records may be collapsed. A shared atomic
		// key with different text or limitations must reach validation and stop,
		// never silently discard one of the model's distinct assertions.
		body, _ := json.Marshal(record) // Record has no fallible JSON fields.
		key := string(body)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, record)
	}
	candidates.Records = deduped
	return candidates
}
