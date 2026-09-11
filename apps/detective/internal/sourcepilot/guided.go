package sourcepilot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

const (
	// GuidedSourceVersion identifies a local, caller-declared summary/body pair.
	GuidedSourceVersion = "detective-summary-source/v1"
	// GuidedVersion identifies an advisory report, never an AHE proposal.
	GuidedVersion = "detective-summary-check/v1"
)

// GuidedSource preserves source text independently of the untrusted summary.
// Metadata is caller-declared, not proof of publisher authenticity or Core intake.
type GuidedSource struct {
	Version        string   `json:"version"`
	SourceID       string   `json:"source_id"`
	SourceRevision string   `json:"source_revision"`
	SourceURL      string   `json:"source_url"`
	ObservedAt     string   `json:"observed_at"`
	SummaryOrigin  string   `json:"summary_origin"`
	Coverage       string   `json:"coverage"`
	Limitations    []string `json:"limitations"`
	Summary        string   `json:"summary"`
	Body           string   `json:"body"`
}

// ReadableStatement carries one complete sentence and lexical reading aids.
// Object may be empty for an intransitive/state sentence. These fields do not
// establish grammatical completeness, entailment, causality or human approval.
type ReadableStatement struct {
	Text      string `json:"text"`
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// GuidedClaim is numbered by the controller after decomposition of the summary.
type GuidedClaim struct {
	Number    int               `json:"number"`
	Statement ReadableStatement `json:"statement"`
}

// GuidedAssessment is a model suggestion about one unchanged claim. Segments
// form one joint context set, not independently sufficient or necessary causes.
type GuidedAssessment struct {
	Claim       int       `json:"claim"`
	Relation    string    `json:"relation"`
	Segments    []int     `json:"segments"`
	Explanation string    `json:"explanation"`
	Citations   []Segment `json:"citations"`
}

// GuidedObservation is a body-only observation; it is not automatically an
// omission from the summary. A person compares these independently collected facts.
type GuidedObservation struct {
	Statement ReadableStatement `json:"statement"`
	Segments  []int             `json:"segments"`
	Citations []Segment         `json:"citations"`
}

// GuidedAttempt retains only final model text, not private reasoning or tools.
type GuidedAttempt struct {
	Stage   string `json:"stage"`
	RawText string `json:"raw_text"`
}

// GuidedReport retains all original context even if a model selects fragments.
// The local human-review requirement is not the Core admission workflow.
type GuidedReport struct {
	Version            string              `json:"version"`
	Source             GuidedSource        `json:"source"`
	BodySHA256         string              `json:"body_sha256"`
	SummarySHA256      string              `json:"summary_sha256"`
	Projection         SegmentedBody       `json:"projection"`
	Claims             []GuidedClaim       `json:"claims"`
	ClaimsReason       string              `json:"claims_reason"`
	Assessments        []GuidedAssessment  `json:"assessments"`
	Observations       []GuidedObservation `json:"observations"`
	ObservationsReason string              `json:"observations_reason"`
	Attempts           []GuidedAttempt     `json:"attempts"`
	Stage              string              `json:"stage"`
	HumanReview        string              `json:"human_review"`
	Readability        string              `json:"readability"`
	AuthorityEffect    string              `json:"authority_effect"`
}

// ParseGuidedSource rejects unknown/duplicate keys, nulls, and oversized pairs.
func ParseGuidedSource(raw []byte) (GuidedSource, error) {
	var source GuidedSource
	if len(raw) > 64<<10 || !guidedDecode(string(raw), &source, "version", "source_id", "source_revision", "source_url", "observed_at", "summary_origin", "coverage", "limitations", "summary", "body") {
		return source, errors.New("invalid summary source JSON")
	}
	return source, ValidateGuidedSource(source)
}

// ValidateGuidedSource checks the declared source boundary without external I/O.
func ValidateGuidedSource(s GuidedSource) error {
	bad := errors.New("invalid summary source or coverage")
	if s.Version != GuidedSourceVersion || !guidedText(s.SourceID, 512, false, false) || !guidedText(s.SourceRevision, 512, false, false) || !guidedText(s.SummaryOrigin, 512, false, false) || !guidedText(s.SourceURL, 2048, false, false) || !guidedText(s.Summary, 4096, false, true) || !guidedText(s.Body, 32<<10, false, true) || s.Limitations == nil || len(s.Limitations) > 8 {
		return bad
	}
	u, err := url.Parse(s.SourceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return bad
	}
	if _, err := time.Parse(time.RFC3339Nano, s.ObservedAt); err != nil {
		return bad
	}
	if s.Coverage != "full_document" && s.Coverage != "exact_excerpt" && s.Coverage != "truncated_document" {
		return bad
	}
	if (s.Coverage != "full_document" && len(s.Limitations) == 0) || (s.Coverage == "full_document" && len(s.Limitations) != 0) {
		return bad
	}
	for _, limitation := range s.Limitations {
		if !guidedText(limitation, 1024, false, false) {
			return bad
		}
	}
	projection, err := SegmentBody(s.Body)
	if err != nil || len(projection.Segments) == 0 {
		return bad
	}
	return nil
}

// NewGuidedReport builds an offline inspection, retaining the complete body.
func NewGuidedReport(source GuidedSource) (GuidedReport, error) {
	if err := ValidateGuidedSource(source); err != nil {
		return GuidedReport{}, err
	}
	projection, err := SegmentBody(source.Body)
	if err != nil {
		return GuidedReport{}, err
	}
	source.Limitations = append([]string{}, source.Limitations...)
	digest := sha256.Sum256([]byte(source.Summary))
	return GuidedReport{Version: GuidedVersion, Source: source, BodySHA256: projection.BodySHA256,
		SummarySHA256: hex.EncodeToString(digest[:]), Projection: projection,
		Claims: []GuidedClaim{}, Assessments: []GuidedAssessment{}, Observations: []GuidedObservation{}, Attempts: []GuidedAttempt{},
		Stage: "inspected", HumanReview: "not_reviewed", Readability: "requires_human_review", AuthorityEffect: "none"}, nil
}

// GuidedReviewer makes at most three independent model calls, without erasure,
// retries, tools, confidence-based acceptance, source fetching or persistence.
type GuidedReviewer struct{ model model.LLM }

// NewGuidedReviewer constructs a reviewer; the caller owns the model transport.
func NewGuidedReviewer(llm model.LLM) (*GuidedReviewer, error) {
	if llm == nil {
		return nil, errors.New("summary review model is required")
	}
	return &GuidedReviewer{model: llm}, nil
}

// Review first decomposes only the summary, checks the resulting fixed claims
// against the complete body, then reads the body without the summary or claims.
// Failure retains prior results for inspection, never as a completed assessment.
func (r *GuidedReviewer) Review(ctx context.Context, source GuidedSource) (GuidedReport, error) {
	report, err := NewGuidedReport(source)
	if err != nil {
		return report, err
	}
	if ctx == nil || r == nil || r.model == nil {
		return report, errors.New("summary review requires an active model context")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	stages := []string{"summary_claims", "source_check", "body_observations"}
	for _, stage := range stages {
		// There is nothing to verify if the summary has no factual claim, but a
		// separate body reading can still expose useful source information.
		if stage == "source_check" && len(report.Claims) == 0 {
			continue
		}
		report.Stage = stage
		input, instruction, schema := guidedRequest(stage, report)
		stageCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		raw, callErr := r.generate(stageCtx, input, instruction, schema)
		cancel()
		report.Attempts = append(report.Attempts, GuidedAttempt{Stage: stage, RawText: raw})
		if callErr != nil {
			return report, callErr
		}
		if err := guidedApply(&report, stage, raw); err != nil {
			return report, err
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
	}
	report.Stage = "complete"
	return report, nil
}

func (r *GuidedReviewer) generate(ctx context.Context, input, instruction string, schema map[string]any) (string, error) {
	temperature := float32(0)
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(input, genai.RoleUser)}, Config: &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(instruction, genai.RoleUser), Temperature: &temperature,
		MaxOutputTokens: 1536, ResponseMIMEType: "application/json", ResponseJsonSchema: schema,
	}}
	bad := errors.New("summary review model response incomplete or unsupported")
	count := 0
	var text strings.Builder
	for response, err := range r.model.GenerateContent(ctx, req, false) {
		count++
		if count != 1 || err != nil || response == nil || response.Partial || response.Interrupted || response.ErrorCode != "" || response.ErrorMessage != "" || response.FinishReason != genai.FinishReasonStop || response.Content == nil || response.Content.Role != genai.RoleModel {
			return "", bad
		}
		for _, part := range response.Content.Parts {
			if part == nil || part.Thought || part.FunctionCall != nil || part.FunctionResponse != nil || part.ToolCall != nil || part.ToolResponse != nil || part.InlineData != nil || part.FileData != nil || part.ExecutableCode != nil || part.CodeExecutionResult != nil || text.Len()+len(part.Text) > 32<<10 {
				return "", bad
			}
			text.WriteString(part.Text)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if count != 1 || text.Len() == 0 {
		return "", bad
	}
	return text.String(), nil
}

func guidedDecode(raw string, into any, keys ...string) bool {
	if len(raw) > 256<<10 || sourcemcp.ValidateRecordedJSON(raw) != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if len(fields[key]) == 0 || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return false
		}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into) == nil
}

func guidedText(s string, maximum int, empty, multiline bool) bool {
	if len(s) > maximum || !utf8.ValidString(s) || (!empty && strings.TrimSpace(s) == "") {
		return false
	}
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t'))) {
			return false
		}
	}
	return true
}
