package pending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

const (
	reasonAssessmentVersion = "detective-reason-assessment/v2"
	reasonPromptVersion     = "detective-reason-adviser/v6"
	maxReasonResponseBytes  = 64 << 10
	maxReasonRequestBytes   = 2 << 20
	maxReasonThinkingBytes  = 64 << 10
)

var errReasonAdvisoryContract = errors.New("model advisory contract violation")

// ReasonAnchor is a controller-created exact UTF-8 byte range in a saved input.
// Its validity proves a quotation's identity, not a model's interpretation.
type ReasonAnchor struct {
	ID         string `json:"id"`
	Field      string `json:"field"`
	StartByte  int    `json:"start_byte"`
	EndByte    int    `json:"end_byte"`
	ExactQuote string `json:"exact_quote"`
}

// ReasonConcern is an untrusted model explanation and question with exact
// controller-validated quotations. It cannot approve or change a decision.
type ReasonConcern struct {
	Kind        string         `json:"kind"`
	Explanation string         `json:"explanation"`
	Question    string         `json:"question"`
	Anchors     []ReasonAnchor `json:"anchors"`
}

// ReasonAssessmentOptions selects local advisory behavior, never authority.
// The zero value keeps thinking disabled.
type ReasonAssessmentOptions struct {
	Think bool
}

// ReasonThinking records the requested mode and bounded response observation.
// SHA256 covers decoded, untrimmed UTF-8 bytes, not the JSON wire literal.
// The omitted text and its meaning are not independently verified on readback.
type ReasonThinking struct {
	Mode    string `json:"mode"`
	Present bool   `json:"present"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

// ReasonAssessment preserves an advisory and its complete immutable inputs in
// a private file. The embedded decision remains an intent, never authority.
type ReasonAssessment struct {
	SchemaVersion   string          `json:"schema_version"`
	PromptVersion   string          `json:"prompt_version"`
	Model           string          `json:"model"`
	ModelDigest     string          `json:"model_digest"`
	DecisionDigest  string          `json:"decision_digest"`
	ReviewDisplayID string          `json:"review_display_id"`
	ReviewDecision  ReviewDecision  `json:"review_decision"`
	Verdict         string          `json:"verdict"`
	Summary         string          `json:"summary"`
	Concerns        []ReasonConcern `json:"concerns"`
	AuthorityEffect string          `json:"authority_effect"`
	Validation      string          `json:"validation"`
	Thinking        *ReasonThinking `json:"thinking,omitempty"`
	Digest          string          `json:"digest"`
}

type reasonModelReply struct {
	Verdict  string               `json:"verdict"`
	Summary  string               `json:"summary"`
	Concerns []reasonModelConcern `json:"concerns"`
}

type reasonModelConcern struct {
	Kind        string   `json:"kind"`
	Explanation string   `json:"explanation"`
	Question    string   `json:"question"`
	AnchorIDs   []string `json:"anchor_ids"`
}

type reasonInputs struct {
	Decision string         `json:"decision"`
	Source   string         `json:"source"`
	Claim    string         `json:"claim"`
	Reason   string         `json:"reason"`
	Review   string         `json:"review"`
	Anchors  []ReasonAnchor `json:"anchors"`
}

type reasonAssessmentFailure struct {
	stage string
	cause error
}

func (e *reasonAssessmentFailure) Error() string { return "reason assessment failed during " + e.stage }
func (e *reasonAssessmentFailure) Unwrap() error { return e.cause }

// ReasonAssessmentFailureStage returns a fixed diagnostic label, never source,
// reason, model response, endpoint, credentials, or filesystem details.
func ReasonAssessmentFailureStage(err error) string {
	var failure *reasonAssessmentFailure
	if errors.As(err, &failure) {
		return failure.stage
	}
	return "input_or_private_output"
}

// AssessReviewReason makes one explicitly selected loopback model request and
// saves a bounded advisory without replacing any prior output or decision. It
// never calls MCP. A model or persistence failure is not a favorable assessment.
func AssessReviewReason(ctx context.Context, decisionPath, endpoint, model, outPath string) (ReasonAssessment, error) {
	return AssessReviewReasonWithOptions(ctx, decisionPath, endpoint, model, outPath, ReasonAssessmentOptions{})
}

// AssessReviewReasonWithOptions explicitly selects thinking for one advisory.
// Reasoning is never used as final content, evidence, or an executable decision.
func AssessReviewReasonWithOptions(ctx context.Context, decisionPath, endpoint, model, outPath string, options ReasonAssessmentOptions) (ReasonAssessment, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ReasonAssessment{}, err
	}
	base, err := reasonEndpoint(endpoint)
	if err != nil || !reasonModelName(model) {
		return ReasonAssessment{}, errors.New("reason assessment requires an explicit local endpoint and model")
	}
	decision, err := LoadReviewDecision(decisionPath)
	if err != nil {
		return ReasonAssessment{}, errors.New("reason assessment requires a valid saved private decision")
	}
	inputs, err := buildReasonInputs(decision)
	if err != nil {
		return ReasonAssessment{}, err
	}
	requestBody, err := reasonRequestWithOptions(model, inputs, options)
	if err != nil {
		return ReasonAssessment{}, err
	}
	w, err := Reserve(outPath)
	if err != nil {
		return ReasonAssessment{}, fmt.Errorf("reserve private advisory output: %w", err)
	}
	defer w.Close()
	client := reasonHTTPClient()
	defer client.CloseIdleConnections()
	modelDigest, err := verifyReasonLocalModel(ctx, client, base, model)
	if err != nil {
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: "local_model_inventory", cause: err}
	}
	body, err := reasonHTTP(ctx, client, base+"/api/chat", requestBody, 256<<10)
	if err != nil {
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: "chat_transport", cause: err}
	}
	content, thinking, err := reasonChatContentWithOptions(body, model, options)
	if err != nil {
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: "completion_envelope", cause: err}
	}
	var reply reasonModelReply
	if err := decodeReasonJSON([]byte(content), &reply); err != nil {
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: "advisory_json", cause: err}
	}
	concerns, err := groundReasonConcerns(reply, inputs.Anchors)
	if err != nil {
		stage := "quote_grounding"
		if errors.Is(err, errReasonAdvisoryContract) {
			stage = "advisory_contract"
		}
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: stage, cause: err}
	}
	assessment := ReasonAssessment{
		SchemaVersion: reasonAssessmentVersion, PromptVersion: reasonPromptVersion,
		Model: model, ModelDigest: modelDigest, DecisionDigest: decision.Digest,
		ReviewDisplayID: decision.ConfirmedDisplayID, ReviewDecision: decision,
		Verdict: reply.Verdict, Summary: reply.Summary, Concerns: concerns,
		AuthorityEffect: "none", Validation: "closed_schema_and_exact_quotes_only",
		Thinking: &thinking,
	}
	assessment.Digest, err = reasonAssessmentHash(assessment)
	if err != nil {
		return ReasonAssessment{}, err
	}
	if err := assessment.validate(); err != nil {
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: "assessment_integrity", cause: err}
	}
	if err := writeReviewFile(w, assessment); err != nil {
		return ReasonAssessment{}, &reasonAssessmentFailure{stage: "private_save", cause: err}
	}
	return assessment, nil
}

// LoadReasonAssessment rechecks saved inputs and exact quote offsets without
// contacting the model, original source or MCP. It is not a current DB query.
func LoadReasonAssessment(path string) (ReasonAssessment, error) {
	var assessment ReasonAssessment
	if err := readReviewFile(path, &assessment); err != nil {
		return ReasonAssessment{}, err
	}
	if err := assessment.validate(); err != nil {
		return ReasonAssessment{}, err
	}
	return assessment, nil
}

func (a ReasonAssessment) validate() error {
	digest, err := reasonAssessmentHash(a)
	if err != nil || digest != a.Digest || !a.validVersionMetadata() || a.AuthorityEffect != "none" || a.Validation != "closed_schema_and_exact_quotes_only" || !reasonModelName(a.Model) || !reasonDigest(a.ModelDigest) {
		return errors.New("saved advisory metadata or digest mismatch")
	}
	if err := a.ReviewDecision.validate(); err != nil || a.DecisionDigest != a.ReviewDecision.Digest || a.ReviewDisplayID != a.ReviewDecision.ConfirmedDisplayID {
		return errors.New("saved advisory differs from its exact decision")
	}
	inputs, err := buildReasonInputs(a.ReviewDecision)
	if err != nil {
		return err
	}
	reply := reasonModelReply{Verdict: a.Verdict, Summary: a.Summary, Concerns: make([]reasonModelConcern, len(a.Concerns))}
	for i, concern := range a.Concerns {
		ids := make([]string, len(concern.Anchors))
		for j, anchor := range concern.Anchors {
			ids[j] = anchor.ID
		}
		reply.Concerns[i] = reasonModelConcern{Kind: concern.Kind, Explanation: concern.Explanation, Question: concern.Question, AnchorIDs: ids}
	}
	grounded, err := groundReasonConcerns(reply, inputs.Anchors)
	if err != nil || !reflect.DeepEqual(grounded, a.Concerns) {
		return errors.New("saved advisory quote or byte range differs from its exact input")
	}
	return nil
}

func (a ReasonAssessment) validVersionMetadata() bool {
	switch a.SchemaVersion {
	case "detective-reason-assessment/v1":
		if a.Thinking != nil {
			return false
		}
		switch a.PromptVersion {
		case "detective-reason-adviser/v1", "detective-reason-adviser/v2", "detective-reason-adviser/v3", "detective-reason-adviser/v4", "detective-reason-adviser/v5":
			return true
		}
	case reasonAssessmentVersion:
		return a.PromptVersion == reasonPromptVersion && a.Thinking != nil && a.Thinking.valid()
	}
	return false
}

func (t ReasonThinking) valid() bool {
	if (t.Mode != "off" && t.Mode != "on") || t.Bytes < 0 || t.Bytes > maxReasonThinkingBytes || (t.Mode == "off" && t.Bytes != 0) {
		return false
	}
	if !t.Present {
		return t.Bytes == 0 && t.SHA256 == ""
	}
	if t.Bytes == 0 {
		return t.SHA256 == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	}
	return reasonDigest(t.SHA256) && t.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
}

func reasonAssessmentHash(a ReasonAssessment) (string, error) {
	a.Digest = ""
	return reviewHash(a)
}

func buildReasonInputs(decision ReviewDecision) (reasonInputs, error) {
	if err := decision.validate(); err != nil {
		return reasonInputs{}, err
	}
	checkpoint := decision.ReviewBundle.Checkpoint
	claim, err := ahemcp.ProposalStatement(checkpoint.Batch.Extractor, checkpoint.Batch.Rows[0].Result.Records[0])
	if err != nil {
		return reasonInputs{}, errors.New("reason assessment requires the exact proposal statement")
	}
	inputs := reasonInputs{Decision: decision.Decision, Source: checkpoint.RawText,
		Claim: claim, Reason: decision.Reason,
		Review: decision.ReviewBundle.Review.Display.PayloadUTF8, Anchors: []ReasonAnchor{}}
	add := func(field, value string, start, end int) {
		inputs.Anchors = append(inputs.Anchors, ReasonAnchor{
			ID: fmt.Sprintf("a%04d", len(inputs.Anchors)), Field: field,
			StartByte: start, EndByte: end, ExactQuote: value[start:end],
		})
	}
	add("claim", inputs.Claim, 0, len(inputs.Claim))
	add("reason", inputs.Reason, 0, len(inputs.Reason))
	start := 0
	for _, line := range strings.SplitAfter(inputs.Source, "\n") {
		end := start + len(line)
		quoteEnd := end
		if quoteEnd > start && inputs.Source[quoteEnd-1] == '\n' {
			quoteEnd--
		}
		if quoteEnd > start && inputs.Source[quoteEnd-1] == '\r' {
			quoteEnd--
		}
		if quoteEnd > start {
			add("source", inputs.Source, start, quoteEnd)
		}
		start = end
		if len(inputs.Anchors) > 2048 {
			return reasonInputs{}, errors.New("source exceeds the bounded advisory anchor catalog")
		}
	}
	return inputs, nil
}

func groundReasonConcerns(reply reasonModelReply, catalog []ReasonAnchor) ([]ReasonConcern, error) {
	if !reasonText(reply.Summary, 1200) || len(reply.Concerns) > 5 || reply.Concerns == nil {
		return nil, fmt.Errorf("%w: missing, unbounded, or incomplete", errReasonAdvisoryContract)
	}
	switch reply.Verdict {
	case "no_specific_concern":
		if len(reply.Concerns) != 0 {
			return nil, fmt.Errorf("%w: verdict conflicts with its concerns", errReasonAdvisoryContract)
		}
	case "follow_up_needed", "cannot_assess":
		if len(reply.Concerns) == 0 {
			return nil, fmt.Errorf("%w: follow-up requires a concern", errReasonAdvisoryContract)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported verdict", errReasonAdvisoryContract)
	}
	byID := make(map[string]ReasonAnchor, len(catalog))
	for _, anchor := range catalog {
		byID[anchor.ID] = anchor
	}
	result := make([]ReasonConcern, 0, len(reply.Concerns))
	for _, concern := range reply.Concerns {
		switch concern.Kind {
		case "missing_justification", "scope_mismatch", "unsupported_inference", "evidence_conflict", "missing_context":
		default:
			return nil, fmt.Errorf("%w: unsupported concern kind", errReasonAdvisoryContract)
		}
		if !reasonText(concern.Explanation, 1200) || !reasonText(concern.Question, 1000) {
			return nil, fmt.Errorf("%w: concern needs a bounded explanation and question", errReasonAdvisoryContract)
		}
		if len(concern.AnchorIDs) < 2 || len(concern.AnchorIDs) > 6 {
			return nil, errors.New("advisory concern needs two to six exact anchors")
		}
		grounded := ReasonConcern{Kind: concern.Kind, Explanation: concern.Explanation, Question: concern.Question, Anchors: []ReasonAnchor{}}
		seen := make(map[string]bool)
		hasReason, hasEvidence := false, false
		for _, id := range concern.AnchorIDs {
			anchor, ok := byID[id]
			if !ok || seen[id] {
				return nil, errors.New("advisory contains missing, invented, or repeated evidence anchors")
			}
			seen[id] = true
			hasReason = hasReason || anchor.Field == "reason"
			hasEvidence = hasEvidence || anchor.Field == "source" || anchor.Field == "claim"
			grounded.Anchors = append(grounded.Anchors, anchor)
		}
		if !hasReason || !hasEvidence {
			return nil, errors.New("advisory concern must cite the saved reason and source or claim")
		}
		result = append(result, grounded)
	}
	return result, nil
}

func reasonText(value string, limit int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func decodeReasonJSON(body []byte, output any) error {
	if len(body) == 0 || len(body) > maxReasonResponseBytes || !utf8.Valid(body) {
		return errors.New("invalid bounded advisory JSON")
	}
	check := json.NewDecoder(bytes.NewReader(body))
	if uniqueJSON(check, 0) != nil {
		return errors.New("duplicate or malformed advisory JSON")
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("trailing advisory JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		return errors.New("advisory JSON does not match its closed schema")
	}
	canonical, err := json.Marshal(output)
	var observed, expected any
	if err != nil || json.Unmarshal(body, &observed) != nil || json.Unmarshal(canonical, &expected) != nil || !reflect.DeepEqual(observed, expected) {
		return errors.New("advisory JSON has missing or aliased fields")
	}
	return nil
}

// WriteReasonAssessmentText safely displays every concern and question. Model
// strings remain quoted data, never terminal control or a new instruction path.
func WriteReasonAssessmentText(w io.Writer, assessment ReasonAssessment) error {
	if err := assessment.validate(); err != nil {
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Detective 理由評估（模型建議，不是核准、真實性或真人身分證明）\n保存決策：%q\n決策識別碼：%s\n模型觀測：%q／%s\n建議狀態：%q\n說明：%q\n", assessment.ReviewDecision.Decision, assessment.DecisionDigest, assessment.Model, assessment.ModelDigest, assessment.Verdict, assessment.Summary)
	if t := assessment.Thinking; t != nil {
		fmt.Fprintf(&text, "thinking 請求模式：%q；回應欄位存在：%t；UTF-8 bytes：%d；SHA256：%q\n此為本機觀測紀錄，不是模型思考、語意正確或核准的證明；不保存推理文字。\n", t.Mode, t.Present, t.Bytes, t.SHA256)
	}
	for i, concern := range assessment.Concerns {
		fmt.Fprintf(&text, "\n疑點 %d：%q\n說明：%q\n追問：%q\n", i+1, concern.Kind, concern.Explanation, concern.Question)
		for _, anchor := range concern.Anchors {
			fmt.Fprintf(&text, "引用 %s（%s，bytes [%d,%d)）：%q\n", anchor.ID, anchor.Field, anchor.StartByte, anchor.EndByte, anchor.ExactQuote)
		}
	}
	text.WriteString("\n只驗證格式與精確引用，未驗證模型的語意判斷。原決策與 AHE 均未改變。\n若要回答追問，請重新執行 review decide，讀完完整原 review 後，以新的理由保存到新 decision 檔；保留原檔，再對新 decision 另做 assess。\n既有評估只屬於上列決策識別碼，不代表目前資料庫狀態，也不能交給 apply 當成決策。\n")
	_, err := io.WriteString(w, text.String())
	return err
}
