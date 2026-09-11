package sourcepilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

const (
	// FixedClaimsVersion identifies caller-provided claims, not human approval.
	FixedClaimsVersion = "detective-fixed-claims/v1"
	// FixedReportVersion is separate from the three-stage model-decomposed report.
	FixedReportVersion = "detective-fixed-check/v1"
)

// FixedClaims retains complete caller-supplied sentences and their source bundle.
// The caller may be a person or a program; this does not authenticate a reviewer.
type FixedClaims struct {
	Version string       `json:"version"`
	Source  GuidedSource `json:"source"`
	Claims  []string     `json:"claims"`
}

// FixedReport records only source checking. Summary decomposition and independent
// body reading are not performed; their absence must not look like abstention.
type FixedReport struct {
	Version         string             `json:"version"`
	Input           FixedClaims        `json:"input"`
	ClaimsOrigin    string             `json:"claims_origin"`
	BodySHA256      string             `json:"body_sha256"`
	SummarySHA256   string             `json:"summary_sha256"`
	Projection      SegmentedBody      `json:"projection"`
	Assessments     []GuidedAssessment `json:"assessments"`
	Attempts        []GuidedAttempt    `json:"attempts"`
	Stage           string             `json:"stage"`
	HumanReview     string             `json:"human_review"`
	Readability     string             `json:"readability"`
	AuthorityEffect string             `json:"authority_effect"`
}

// ParseFixedClaims validates the private bounded input without changing its text.
func ParseFixedClaims(raw []byte) (FixedClaims, error) {
	var input FixedClaims
	if len(raw) > 64<<10 || guidedReportDecode(raw, &input) != nil {
		return input, errors.New("invalid fixed claims JSON")
	}
	_, err := NewFixedReport(input)
	return input, err
}

// NewFixedReport creates an offline view. Punctuation and nonempty text are only
// lexical checks; neither grammatical completeness nor atomicity is established.
func NewFixedReport(input FixedClaims) (FixedReport, error) {
	bad := errors.New("invalid fixed claims or source")
	if input.Version != FixedClaimsVersion || len(input.Claims) == 0 || len(input.Claims) > 3 {
		return FixedReport{}, bad
	}
	seen := make(map[string]bool)
	for _, claim := range input.Claims {
		if !guidedBounded(claim, 512, false) || strings.TrimSpace(claim) != claim || strings.Contains(claim, "...") || strings.ContainsRune(claim, '…') || seen[claim] {
			return FixedReport{}, bad
		}
		if !strings.HasSuffix(claim, ".") && !strings.HasSuffix(claim, "。") && !strings.HasSuffix(claim, "!") && !strings.HasSuffix(claim, "！") {
			return FixedReport{}, bad
		}
		seen[claim] = true
	}
	base, err := NewGuidedReport(input.Source)
	if err != nil {
		return FixedReport{}, bad
	}
	input.Source = base.Source
	input.Claims = append([]string{}, input.Claims...)
	return FixedReport{Version: FixedReportVersion, Input: input, ClaimsOrigin: "caller_supplied_unverified",
		BodySHA256: base.BodySHA256, SummarySHA256: base.SummarySHA256, Projection: base.Projection,
		Assessments: []GuidedAssessment{}, Attempts: []GuidedAttempt{}, Stage: "inspected",
		HumanReview: "not_reviewed", Readability: "requires_human_review", AuthorityEffect: "none"}, nil
}

// CheckFixed checks the exact supplied claims once, without automatically
// decomposing, repairing or approving them. It uses the existing check prompt.
func (r *GuidedReviewer) CheckFixed(ctx context.Context, input FixedClaims) (FixedReport, error) {
	report, err := NewFixedReport(input)
	if err != nil {
		return report, err
	}
	if ctx == nil || r == nil || r.model == nil {
		return report, errors.New("fixed check requires an active model context")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	base := fixedContext(report)
	_, instruction, schema := guidedRequest("source_check", base)
	// Only numbered, complete claims and the complete body enter this request.
	// Empty SVO fields would be misleading, so this input does not include them.
	type claimText struct {
		Number int    `json:"number"`
		Text   string `json:"text"`
	}
	claims := make([]claimText, len(report.Input.Claims))
	for i, claim := range report.Input.Claims {
		claims[i] = claimText{Number: i + 1, Text: claim}
	}
	segments := make([]Line, len(base.Projection.Segments))
	for i, segment := range base.Projection.Segments {
		segments[i] = Line{Number: segment.Number, Text: segment.Text}
	}
	wire, _ := json.Marshal(struct {
		Claims   []claimText `json:"claims"`
		Segments []Line      `json:"source_segments"`
	}{claims, segments}) // Concrete validated strings and integer fields cannot fail.
	report.Stage = "source_check"
	stageCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	raw, err := r.generate(stageCtx, string(wire), instruction, schema)
	report.Attempts = append(report.Attempts, GuidedAttempt{Stage: "source_check", RawText: raw})
	if err != nil {
		return report, err
	}
	if err := guidedApply(&base, "source_check", raw); err != nil {
		return report, err
	}
	report.Assessments = base.Assessments
	if err := stageCtx.Err(); err != nil {
		return report, err
	}
	report.Stage = "complete"
	return report, nil
}

// ParseFixedReport checks internal consistency, not publisher identity, actual
// model execution or human authorization. Failed attempts remain inspectable.
func ParseFixedReport(raw []byte) (FixedReport, error) {
	var report FixedReport
	if guidedReportDecode(raw, &report) != nil {
		return report, errors.New("invalid fixed report JSON")
	}
	expected, err := NewFixedReport(report.Input)
	if err != nil || len(report.Attempts) > 1 {
		return report, errors.New("invalid fixed report input or attempts")
	}
	if len(report.Attempts) == 1 {
		attempt := report.Attempts[0]
		if attempt.Stage != "source_check" || len(attempt.RawText) > 32<<10 {
			return report, errors.New("invalid fixed check attempt")
		}
		expected.Stage = "source_check"
		expected.Attempts = append(expected.Attempts, attempt)
		base := fixedContext(expected)
		if guidedApply(&base, "source_check", attempt.RawText) == nil {
			expected.Assessments = base.Assessments
			// Cancellation after parsing may retain a valid result at source_check.
			if report.Stage == "complete" {
				expected.Stage = "complete"
			}
		}
	}
	if !reflect.DeepEqual(report, expected) {
		return report, errors.New("fixed report does not match its retained input and attempt")
	}
	return report, nil
}

func fixedContext(report FixedReport) GuidedReport {
	claims := make([]GuidedClaim, len(report.Input.Claims))
	for i, claim := range report.Input.Claims {
		claims[i] = GuidedClaim{Number: i + 1, Statement: ReadableStatement{Text: claim}}
	}
	return GuidedReport{Source: report.Input.Source, BodySHA256: report.BodySHA256, SummarySHA256: report.SummarySHA256,
		Projection: report.Projection, Claims: claims, Assessments: report.Assessments, Stage: report.Stage}
}

// WriteFixedText shows caller-supplied claims without invented SVO annotations.
func WriteFixedText(w io.Writer, report FixedReport) error {
	return writeGuidedText(w, fixedContext(report), true)
}
