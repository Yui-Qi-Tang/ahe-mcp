package ahemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

// BriefSubmission freezes a user-selected statement and exact source citation.
// The complete reading brief remains unreviewed; its presence does not establish
// that the selected source lines support the statement or authorize admission.
type BriefSubmission struct {
	Version    string                  `json:"version"`
	Report     sourcepilot.BriefReport `json:"report"`
	SourcePath string                  `json:"source_path"`
	Model      string                  `json:"model"`
	Statement  string                  `json:"statement"`
	Citation   labstatus.Citation      `json:"citation"`
	Digest     string                  `json:"digest"`
}

const briefSubmissionVersion = "detective-brief-submission/v1"

// NewBriefSubmission validates and detaches selected inputs without I/O. The
// caller must supply the exact displayed quote; it is never inferred from the
// generated brief. SourcePath is a recorded local label, not a file to reopen.
func NewBriefSubmission(report sourcepilot.BriefReport, sourcePath, model, statement string, citation labstatus.Citation) (BriefSubmission, error) {
	input := BriefSubmission{Version: briefSubmissionVersion, Report: report, SourcePath: sourcePath, Model: model, Statement: statement, Citation: citation}
	input.Digest = digest(input)
	if err := ValidateBriefSubmission(input); err != nil {
		return BriefSubmission{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return BriefSubmission{}, err
	}
	var detached BriefSubmission
	if err := json.Unmarshal(raw, &detached); err != nil {
		return BriefSubmission{}, err
	}
	return detached, nil
}

// ValidateBriefSubmission checks source bytes, the selected full-line quote and
// immutable local identity. It does not perform semantic or human review.
func ValidateBriefSubmission(input BriefSubmission) error {
	copy := input
	copy.Digest = ""
	if input.Version != briefSubmissionVersion || input.Digest != digest(copy) {
		return errors.New("brief submission differs from its frozen inputs")
	}
	raw, err := json.Marshal(input.Report)
	if err != nil {
		return errors.New("brief report cannot be encoded")
	}
	report, err := sourcepilot.ParseBriefReport(raw)
	if err != nil || report.Stage != "complete" {
		return errors.New("brief submission requires a complete unreviewed reading report")
	}
	if !validText(report.Source.SourceID, 500) || strings.TrimSpace(report.Source.SourceID) != report.Source.SourceID || !validText(input.Model, 4096) || strings.TrimSpace(input.Model) != input.Model || !validText(input.Statement, 2000) || strings.TrimSpace(input.Statement) != input.Statement {
		return errors.New("brief source identity, model and selected statement must be bounded exact text")
	}
	for _, r := range input.Statement {
		if unicode.IsControl(r) && r != '\n' && r != '\t' || unicode.Is(unicode.Cf, r) {
			return errors.New("brief statement contains unsupported display characters")
		}
	}
	document, err := briefDocument(input)
	if err != nil || strings.Contains(strings.ReplaceAll(report.Source.Body, "\r\n", "\n"), "\r") || !withinResponseBudget(report.Source.Body) {
		return errors.New("brief source requires bounded exact LF or CRLF document bytes")
	}
	citation := input.Citation
	if citation.StartLine < 1 || citation.EndLine < citation.StartLine || citation.EndLine-citation.StartLine >= 12 {
		return errors.New("brief citation requires 1 to 12 selected full source lines")
	}
	quote, ok := document.ExactQuote(citation.StartLine, citation.EndLine)
	if !ok || quote != citation.ExactQuote {
		return errors.New("brief selected quote differs from the exact source lines")
	}
	for line := citation.StartLine; line <= citation.EndLine; line++ {
		quote, ok := document.ExactQuote(line, line)
		if !ok || strings.TrimSpace(quote) == "" {
			return errors.New("brief citation cannot include empty source lines")
		}
	}
	return nil
}

func briefDocument(input BriefSubmission) (*labstatus.Document, error) {
	return labstatus.RestoreDocument(input.SourcePath, input.Report.Source.Body)
}

func briefExtractor(input BriefSubmission) labstatus.ExtractorInfo {
	return labstatus.ExtractorInfo{Name: "detective-brief-selection", Version: "1", Model: input.Model}
}

func briefDefinition(input BriefSubmission) extractorDefinition {
	x := briefExtractor(input)
	return extractorDefinition{Name: x.Name, Version: x.Version, Config: map[string]string{
		"model": input.Model, "source_mode": "frozen_local_snapshot", "brief_report_sha256": digest(input.Report),
		"selection_sha256": input.Digest, "prompt_version": input.Report.PromptVersion,
		"source_metadata_sha256": digest(briefOrigin(input)),
	}}
}

func briefOrigin(input BriefSubmission) map[string]string {
	s := input.Report.Source
	limitations, _ := json.Marshal(s.Limitations) // Validated string slices cannot fail encoding.
	return map[string]string{
		"capture_kind": "caller_declared_brief_source", "snapshot_sha256": input.Report.BodySHA256,
		"declared_source_id": s.SourceID, "declared_source_revision": s.SourceRevision,
		"declared_source_url": s.SourceURL, "declared_observed_at": s.ObservedAt,
		"declared_coverage": s.Coverage, "declared_limitations_json": string(limitations),
	}
}

func briefProposals(input BriefSubmission, spans []span) ([]proposal, error) {
	// Only the existing low-level statement/line mapping is reused. This is not
	// a capability-state record and never passes through lab-status semantics.
	return proposalsFor(briefExtractor(input), []labstatus.Record{{Statement: input.Statement, Citation: input.Citation}}, spans)
}

func briefRequest(input BriefSubmission, receipt sourceResult, spans []span) (extractorRequest, error) {
	proposals, err := briefProposals(input, spans)
	if err != nil {
		return extractorRequest{}, err
	}
	definition := briefDefinition(input)
	return extractorRequest{RequestID: requestID("brief-proposal", input.Report.Source.SourceID, input.Report.BodySHA256, digest(definition), digest(proposals)), SourceSnapshotID: receipt.SourceSnapshotID, ExtractionViewID: receipt.ExtractionViewID, ExtractorDefinition: definition, ExtractorOutput: extractorOutput{Proposals: proposals}}, nil
}

// SubmitBrief persists the complete original body and one selected statement as
// pending. It never calls a reviewer writer. Exact retries may return an already
// terminal occurrence; an error after a call starts does not imply rollback.
func SubmitBrief(ctx context.Context, command string, input BriefSubmission) (Handoff, error) {
	if ctx == nil {
		return Handoff{}, errors.New("brief submission context is required")
	}
	if err := ValidateBriefSubmission(input); err != nil {
		return Handoff{}, err
	}
	c, err := start(ctx, command)
	if err != nil {
		return Handoff{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return Handoff{}, err
	}
	source := input.Report.Source
	sourceHash := "sha256:" + input.Report.BodySHA256
	origin := briefOrigin(input)
	var receipt sourceResult
	if err := c.call("submit_text_source", sourceRequest{SourceID: source.SourceID, SourceVersion: sourceHash, RawText: source.Body,
		RequestID: requestID("brief-source", source.SourceID, input.Report.BodySHA256, digest(origin)), OriginMetadata: origin}, &receipt); err != nil {
		return Handoff{}, fmt.Errorf("submit brief source (write outcome uncertain; retry identical inputs): %w", err)
	}
	if !reviewID(receipt.SourceSnapshotID, "srcsnap:") || !reviewID(receipt.ExtractionViewID, "view:") || receipt.SourceSystem != "manual_text" || receipt.SpanCatalogVersion != "manual-line-v1" || receipt.RawContentHash != sourceHash || receipt.RenderedContentHash != sourceHash {
		return Handoff{}, errors.New("brief source receipt differs from the exact source; write outcome uncertain")
	}
	var extracted extractorInput
	if err := c.call("get_extractor_input", map[string]string{"extraction_view_id": receipt.ExtractionViewID}, &extracted); err != nil {
		return Handoff{}, fmt.Errorf("get brief source after persistence: %w", err)
	}
	document, _ := briefDocument(input) // Validated before the first external call.
	if err := validateInput(document, source.SourceID, receipt, extracted); err != nil {
		return Handoff{}, err
	}
	request, err := briefRequest(input, receipt, extracted.Spans)
	if err != nil {
		return Handoff{}, err
	}
	var handoff Handoff
	if err := c.call("submit_extractor_output", request, &handoff); err != nil {
		return Handoff{}, fmt.Errorf("submit brief proposal (write outcome uncertain; retry identical inputs): %w", err)
	}
	handoff.SchemaVersion = "ahe-mcp-pending-handoff/v0"
	if handoff.SourceSnapshotID != receipt.SourceSnapshotID || handoff.ExtractionViewID != receipt.ExtractionViewID || validateBriefHandoff(handoff) != nil {
		return Handoff{}, errors.New("brief pending receipt differs from submission; write outcome uncertain")
	}
	if err := c.Close(); err != nil {
		return Handoff{}, errors.New("brief acknowledged but launcher exit failed; preserve identical inputs for retry")
	}
	return handoff, nil
}

func validateBriefHandoff(handoff Handoff) error {
	if handoff.SchemaVersion != "ahe-mcp-pending-handoff/v0" || handoff.ProposalCount != 1 {
		return errors.New("brief handoff requires one exact native occurrence")
	}
	return ValidateCandidateLocator(CandidateLocator{SourceSnapshotID: handoff.SourceSnapshotID, ExtractionViewID: handoff.ExtractionViewID, ExtractionAttemptID: handoff.ExtractionAttemptID, ProposalOccurrenceID: handoff.ProposalOccurrenceID, SubmissionOutcome: handoff.Status, Replayed: handoff.Replayed})
}
