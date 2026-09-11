// Package ahemcp provides bounded pending handoff and explicitly authorized
// source-claim review clients. Source collection and credentials stay external.
package ahemcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

const protocolVersion = "2025-06-18"

// Handoff is the pending-proposal result returned by AHE. It never represents
// an admission or a canonical graph mutation.
type Handoff struct {
	SchemaVersion        string `json:"schema_version"`
	SourceSnapshotID     string `json:"source_snapshot_id"`
	ExtractionViewID     string `json:"extraction_view_id"`
	ExtractionAttemptID  string `json:"extraction_attempt_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id,omitempty"`
	ProposalCount        int    `json:"proposal_count"`
	Status               string `json:"status"`
	Replayed             bool   `json:"replayed"`
}

// Submit revalidates candidates, stores exact document bytes, and submits only
// pending proposals. command is one absolute executable launcher, not a shell
// command. The operator-owned launcher supplies credentials outside Detective.
// An error after a write starts does not imply rollback; retry identical inputs
// to reconcile an uncertain outcome. This function never retries automatically.
func Submit(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record) (Handoff, error) {
	if err := ValidateSubmission(sourceID, document, extractor, records); err != nil {
		return Handoff{}, err
	}
	return submitValidated(ctx, command, sourceID, document, extractor, records)
}

// ValidateSubmission checks the pending handoff inputs without starting a
// process or writing source authority. Checkpoint consumers must apply the same
// rules before persisting candidates for a later exact retry.
func ValidateSubmission(sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record) error {
	if !validText(sourceID, 500) {
		return fmt.Errorf("ahe source ID must be nonempty UTF-8 text of at most 500 bytes without NUL")
	}
	if !validText(extractor.Name, 200) || !validText(extractor.Version, 200) || !validText(extractor.Model, 4096) {
		return fmt.Errorf("extractor name, version, and model must be bounded nonempty UTF-8 text without NUL")
	}
	if document == nil {
		return fmt.Errorf("document is required")
	}
	// Native manual-line-v1 splits on LF and strips only the CR in CRLF.
	// Detective's logical lines also split lone CR, so reject that ambiguity
	// before creating any source authority.
	if strings.Contains(strings.ReplaceAll(document.RawText(), "\r\n", "\n"), "\r") {
		return fmt.Errorf("ahe handoff requires LF or CRLF line endings, not lone CR")
	}
	if !withinResponseBudget(document.RawText()) {
		return fmt.Errorf("ahe source exceeds the bounded full-span response budget")
	}
	if err := labstatus.ValidateCandidateSet(document, labstatus.CandidateSet{
		Outcome: "extracted", Records: records,
		Abstentions: []labstatus.Abstention{}, Limitations: []string{},
	}); err != nil {
		// Do not echo candidate content at the subprocess/diagnostic boundary.
		return fmt.Errorf("ahe handoff requires source-validated candidate records")
	}
	for _, record := range records {
		if _, err := ProposalStatement(extractor, record); err != nil {
			return err
		}
		// This adapter requires one native span for every cited line. Empty
		// lines have no manual-line-v1 span; reject before source persistence.
		for line := record.Citation.StartLine; line <= record.Citation.EndLine; line++ {
			quote, ok := document.ExactQuote(line, line)
			if !ok || quote == "" {
				return fmt.Errorf("ahe citations must contain only nonempty source lines")
			}
		}
	}
	return nil
}

func submitValidated(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record) (Handoff, error) {
	return submitValidatedState(ctx, command, sourceID, document, extractor, records, false)
}

func submitValidatedState(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, allowTerminalReplay bool) (Handoff, error) {
	client, err := start(ctx, command)
	if err != nil {
		return Handoff{}, err
	}
	defer client.Close()
	if err := client.initialize(); err != nil {
		return Handoff{}, err
	}

	source := document.Source()
	sourceHash := "sha256:" + source.SHA256
	var sourceResult sourceResult
	if err := client.call("submit_text_source", sourceRequest{
		SourceID: sourceID, SourceVersion: sourceHash, RawText: document.RawText(),
		RequestID: requestID("source", sourceID, source.SHA256),
		OriginMetadata: map[string]string{
			"capture_kind": "frozen_local_snapshot", "snapshot_sha256": source.SHA256,
		},
	}, &sourceResult); err != nil {
		return Handoff{}, fmt.Errorf("submit source (write outcome uncertain; retry identical inputs): %w", err)
	}
	if !validText(sourceResult.SourceSnapshotID, 256) || !validText(sourceResult.ExtractionViewID, 256) ||
		sourceResult.SourceSystem != "manual_text" || sourceResult.SpanCatalogVersion != "manual-line-v1" ||
		sourceResult.RawContentHash != sourceHash || sourceResult.RenderedContentHash != sourceHash {
		return Handoff{}, fmt.Errorf("ahe source receipt does not match the submitted manual snapshot")
	}

	var input extractorInput
	if err := client.call("get_extractor_input", map[string]string{
		"extraction_view_id": sourceResult.ExtractionViewID,
	}, &input); err != nil {
		return Handoff{}, fmt.Errorf("get extractor input after source persistence: %w", err)
	}
	if err := validateInput(document, sourceID, sourceResult, input); err != nil {
		return Handoff{}, err
	}
	proposals, err := proposalsFor(extractor, records, input.Spans)
	if err != nil {
		return Handoff{}, err
	}
	definition := extractorDefinition{
		Name: extractor.Name, Version: extractor.Version,
		Config: map[string]string{"model": extractor.Model, "source_mode": "frozen_local_snapshot"},
	}
	request := extractorRequest{
		RequestID:        requestID("proposal", sourceID, source.SHA256, digest(definition), digest(proposals)),
		SourceSnapshotID: input.SourceSnapshotID, ExtractionViewID: input.ExtractionViewID,
		ExtractorDefinition: definition, ExtractorOutput: extractorOutput{Proposals: proposals},
	}
	var result Handoff
	if err := client.call("submit_extractor_output", request, &result); err != nil {
		return Handoff{}, fmt.Errorf("submit extractor output (write outcome uncertain; retry identical inputs): %w", err)
	}
	statusAccepted := result.Status == "pending" || (allowTerminalReplay && result.Replayed && candidateTerminalState(result.Status))
	if !statusAccepted || result.ProposalCount != len(proposals) ||
		result.SourceSnapshotID != input.SourceSnapshotID || result.ExtractionViewID != input.ExtractionViewID ||
		!validText(result.ExtractionAttemptID, 256) || !validText(result.ProposalOccurrenceID, 256) {
		return Handoff{}, fmt.Errorf("ahe pending receipt has invalid status, count, or source coordinates; write outcome uncertain")
	}
	if err := client.Close(); err != nil {
		return Handoff{}, fmt.Errorf("ahe acknowledged pending proposals but launcher did not exit cleanly; retry identical inputs: %w", err)
	}
	result.SchemaVersion = "ahe-mcp-pending-handoff/v0"
	return result, nil
}

type sourceRequest struct {
	SourceID       string            `json:"source_id"`
	SourceVersion  string            `json:"source_version"`
	RawText        string            `json:"raw_text"`
	RequestID      string            `json:"request_id"`
	OriginMetadata map[string]string `json:"origin_metadata"`
}

type sourceResult struct {
	SourceSnapshotID    string `json:"source_snapshot_id"`
	ExtractionViewID    string `json:"extraction_view_id"`
	SourceSystem        string `json:"source_system"`
	RawContentHash      string `json:"raw_content_hash"`
	RenderedContentHash string `json:"rendered_content_hash"`
	SpanCatalogVersion  string `json:"span_catalog_version"`
}

type extractorInput struct {
	SourceSnapshotID    string `json:"source_snapshot_id"`
	ExtractionViewID    string `json:"extraction_view_id"`
	SourceSystem        string `json:"source_system"`
	SourceID            string `json:"source_id"`
	SourceVersion       string `json:"source_version"`
	RawContentHash      string `json:"raw_content_hash"`
	RenderedContentHash string `json:"rendered_content_hash"`
	RenderedText        string `json:"rendered_text"`
	SpanCatalogVersion  string `json:"span_catalog_version"`
	Renderer            struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"renderer"`
	Spans []span `json:"spans"`
}

type span struct {
	SpanID         string `json:"span_id"`
	DisplayLine    int    `json:"display_line"`
	Text           string `json:"text"`
	StartByte      int    `json:"start_byte"`
	EndByte        int    `json:"end_byte"`
	QuotedTextHash string `json:"quoted_text_hash"`
}

func validateInput(document *labstatus.Document, sourceID string, receipt sourceResult, input extractorInput) error {
	raw := document.RawText()
	if input.SourceSnapshotID != receipt.SourceSnapshotID || input.ExtractionViewID != receipt.ExtractionViewID ||
		input.SourceSystem != "manual_text" || input.SourceID != sourceID || input.SourceVersion != receipt.RawContentHash ||
		input.RawContentHash != receipt.RawContentHash || input.RenderedContentHash != receipt.RenderedContentHash || input.RenderedText != raw ||
		input.Renderer.Name != "manual-text-identity" || input.Renderer.Version != "v1" || input.SpanCatalogVersion != "manual-line-v1" {
		return fmt.Errorf("ahe extractor input does not match the exact submitted source and rendering contract")
	}
	byLine := make(map[int]span, len(input.Spans))
	ids := make(map[string]bool, len(input.Spans))
	for _, current := range input.Spans {
		if !validText(current.SpanID, 256) || current.DisplayLine <= 0 || ids[current.SpanID] {
			return fmt.Errorf("ahe returned an invalid or duplicate span identity")
		}
		if _, exists := byLine[current.DisplayLine]; exists {
			return fmt.Errorf("ahe returned duplicate span line coordinates")
		}
		ids[current.SpanID] = true
		byLine[current.DisplayLine] = current
	}
	start := 0
	for index, line := range strings.Split(raw, "\n") {
		text := strings.TrimSuffix(line, "\r")
		if text != "" {
			current, exists := byLine[index+1]
			if !exists || current.StartByte != start || current.EndByte != start+len(text) ||
				current.Text != text || current.QuotedTextHash != contentHash(text) {
				return fmt.Errorf("ahe span does not match source line %d bytes, text, or hash", index+1)
			}
			delete(byLine, index+1)
		}
		start += len(line) + 1
	}
	if len(byLine) != 0 {
		return fmt.Errorf("ahe returned spans outside the nonempty source lines")
	}
	return nil
}

type extractorRequest struct {
	RequestID           string              `json:"request_id"`
	SourceSnapshotID    string              `json:"source_snapshot_id"`
	ExtractionViewID    string              `json:"extraction_view_id"`
	ExtractorDefinition extractorDefinition `json:"extractor_definition"`
	ExtractorOutput     extractorOutput     `json:"extractor_output"`
}

type extractorDefinition struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Config  map[string]string `json:"config"`
}

type extractorOutput struct {
	Proposals []proposal `json:"proposals"`
}

type proposal struct {
	ProposalLocalID string   `json:"proposal_local_id"`
	StatementText   string   `json:"statement_text"`
	EvidenceRefs    []string `json:"evidence_refs"`
}

func proposalsFor(extractor labstatus.ExtractorInfo, records []labstatus.Record, spans []span) ([]proposal, error) {
	spanByLine := make(map[int]string, len(spans))
	for _, current := range spans {
		if current.DisplayLine <= 0 || current.SpanID == "" {
			return nil, fmt.Errorf("ahe returned an invalid span")
		}
		if _, exists := spanByLine[current.DisplayLine]; exists {
			return nil, fmt.Errorf("ahe returned multiple spans for source line %d", current.DisplayLine)
		}
		spanByLine[current.DisplayLine] = current.SpanID
	}
	proposals := make([]proposal, 0, len(records))
	for index, record := range records {
		statement, err := ProposalStatement(extractor, record)
		if err != nil {
			return nil, err
		}
		if record.Citation.StartLine < 1 || record.Citation.EndLine < record.Citation.StartLine || record.Citation.EndLine-record.Citation.StartLine >= 12 {
			return nil, fmt.Errorf("record %d has an invalid citation range", index+1)
		}
		refs := make([]string, 0, record.Citation.EndLine-record.Citation.StartLine+1)
		for line := record.Citation.StartLine; ; line++ {
			spanID, exists := spanByLine[line]
			if !exists {
				return nil, fmt.Errorf("record %d citation line %d has no ahe span", index+1, line)
			}
			refs = append(refs, spanID)
			if line == record.Citation.EndLine {
				break
			}
		}
		proposals = append(proposals, proposal{
			ProposalLocalID: fmt.Sprintf("record-%d", index+1), StatementText: statement, EvidenceRefs: refs,
		})
	}
	return proposals, nil
}

func validText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// The native manual source/input replies repeat exact text and span metadata
// in both text content and structuredContent. Reserve generous JSON-escaping
// and per-span overhead before persisting a predictably oversized source.
// This is a conservative adapter bound, not a guarantee about future servers.
func withinResponseBudget(raw string) bool {
	encoded, _ := json.Marshal(raw) // A string has no failing JSON marshaler.
	budget := 32<<10 + 8*len(encoded)
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSuffix(line, "\r") != "" {
			budget += 1200
		}
	}
	return budget < maxRPCBytes
}

// v1 replaces delimiter-concatenated v0 keys; JSON framing avoids collisions,
// and the extractor definition participates in proposal retry identity.
func requestID(parts ...string) string {
	return "detective-v1-" + digest(parts)
}

func digest(value any) string {
	// Callers supply only concrete string/map/slice structs without custom marshalers.
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("unmarshalable internal request identity")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func contentHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
