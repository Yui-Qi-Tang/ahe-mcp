package ahemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// VerifyPending reads exactly one occurrence through an independently selected
// Query launcher and checks it against one validated candidate and its receipt.
// It performs no writes, retains no query payload, and establishes only the
// pending state observed by this read, not that the state cannot later change.
func VerifyPending(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, handoff Handoff) error {
	if len(records) != 1 {
		return errors.New("exact pending readback requires one candidate")
	}
	if err := ValidateSubmission(sourceID, document, extractor, records); err != nil {
		return err
	}
	if handoff.SchemaVersion != "ahe-mcp-pending-handoff/v0" || handoff.Status != "pending" || handoff.ProposalCount != 1 ||
		!pendingID(handoff.SourceSnapshotID, "srcsnap:") || !pendingID(handoff.ExtractionViewID, "view:") ||
		!pendingID(handoff.ExtractionAttemptID, "attempt:") || !pendingID(handoff.ProposalOccurrenceID, "occ:") {
		return errors.New("exact pending readback requires a complete one-proposal receipt")
	}
	c, err := startQuery(ctx, command)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return err
	}
	var result pendingRecord
	if err := c.call("get_evidence_record", map[string]string{"proposal_occurrence_id": handoff.ProposalOccurrenceID}, &result); err != nil {
		return fmt.Errorf("read pending evidence: %w", err)
	}
	if err := verifyPendingRecord(result, sourceID, document, extractor, records[0], handoff); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return fmt.Errorf("pending readback launcher did not exit cleanly: %w", err)
	}
	return nil
}

func startQuery(ctx context.Context, command string) (*client, error) {
	c, err := start(ctx, command)
	if err != nil {
		return nil, err
	}
	c.queryOnly = true
	return c, nil
}

// This is the selected manual-statement projection of the native Query
// response. Nullable authority fields remain raw so missing canonical_ref is
// not mistaken for the explicit JSON null required for a pending proposal.
type pendingRecord struct {
	RecordRef struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"record_ref"`
	ProposalLocalID            string          `json:"proposal_local_id"`
	ProposalFingerprint        string          `json:"proposal_fingerprint"`
	ProposalFingerprintVersion string          `json:"proposal_fingerprint_version"`
	ProposalKind               string          `json:"proposal_kind"`
	StatementText              string          `json:"statement_text"`
	AdmissionOutcome           string          `json:"admission_outcome"`
	CanonicalRef               json.RawMessage `json:"canonical_ref"`
	Canonical                  json.RawMessage `json:"canonical"`
	ProposalOriginRef          json.RawMessage `json:"proposal_origin_ref"`
	CodeFact                   json.RawMessage `json:"code_fact"`
	CodeRelation               json.RawMessage `json:"code_relation"`
	SourceGeneration           json.RawMessage `json:"source_generation"`
	RepositoryLifecycle        json.RawMessage `json:"repository_lifecycle"`
	Source                     struct {
		BindingKind        string          `json:"binding_kind"`
		SourceSnapshotID   string          `json:"source_snapshot_id"`
		SourceSystem       string          `json:"source_system"`
		SourceID           string          `json:"source_id"`
		SourceVersion      string          `json:"source_version"`
		RawContentHash     string          `json:"raw_content_hash"`
		RepositorySnapshot json.RawMessage `json:"repository_snapshot"`
		MCPRead            json.RawMessage `json:"mcp_read"`
		ExternalSource     json.RawMessage `json:"external_source"`
	} `json:"source"`
	SourceRefs []pendingSourceRef `json:"source_refs"`
	Extractor  struct {
		ExtractorDefinitionID string `json:"extractor_definition_id"`
		ExtractionRunID       string `json:"extraction_run_id"`
		ExtractionAttemptID   string `json:"extraction_attempt_id"`
		ProducerSessionRef    string `json:"producer_session_ref"`
		Name                  string `json:"name"`
		Version               string `json:"version"`
		ConfigHash            string `json:"config_hash"`
		AttemptStatus         string `json:"attempt_status"`
	} `json:"extractor"`
	ExtractionViewID    string `json:"extraction_view_id"`
	RendererName        string `json:"renderer_name"`
	RendererVersion     string `json:"renderer_version"`
	RenderedContentHash string `json:"rendered_content_hash"`
}

type pendingSourceRef struct {
	TargetKind           string `json:"target_kind,omitempty"`
	ExtractionViewID     string `json:"extraction_view_id"`
	RepositorySnapshotID string `json:"repository_snapshot_id,omitempty"`
	FileSnapshotID       string `json:"file_snapshot_id,omitempty"`
	RepoID               string `json:"repo_id,omitempty"`
	CommitSHA            string `json:"commit_sha,omitempty"`
	Path                 string `json:"path,omitempty"`
	SpanID               string `json:"span_id"`
	StartByte            *int   `json:"start_byte"`
	EndByte              *int   `json:"end_byte"`
	QuotedTextHash       string `json:"quoted_text_hash"`
	QuotedText           string `json:"quoted_text"`
}

func verifyPendingRecord(result pendingRecord, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, record labstatus.Record, handoff Handoff) error {
	statement, err := ProposalStatement(extractor, record)
	if err != nil {
		return err
	}
	if result.RecordRef.Kind != "proposal" || result.RecordRef.ID != handoff.ProposalOccurrenceID ||
		result.ProposalLocalID != "record-1" || result.ProposalKind != "statement" || result.ProposalFingerprintVersion != "statement-v1" ||
		!pendingID(result.ProposalFingerprint, "fp:") || result.StatementText != statement || result.AdmissionOutcome != "pending" ||
		!bytes.Equal(bytes.TrimSpace(result.CanonicalRef), []byte("null")) {
		return errors.New("query record does not match the exact pending statement")
	}
	for _, foreign := range []json.RawMessage{result.Canonical, result.ProposalOriginRef, result.CodeFact, result.CodeRelation,
		result.SourceGeneration, result.RepositoryLifecycle, result.Source.RepositorySnapshot, result.Source.MCPRead, result.Source.ExternalSource} {
		if len(foreign) != 0 && !bytes.Equal(bytes.TrimSpace(foreign), []byte("null")) {
			return errors.New("query record contains authority outside the manual pending handoff")
		}
	}
	sourceHash := "sha256:" + document.Source().SHA256
	if result.Source.BindingKind != "source_snapshot" || result.Source.SourceSnapshotID != handoff.SourceSnapshotID ||
		result.Source.SourceSystem != "manual_text" || result.Source.SourceID != sourceID || result.Source.SourceVersion != sourceHash ||
		result.Source.RawContentHash != sourceHash || result.ExtractionViewID != handoff.ExtractionViewID ||
		result.RendererName != "manual-text-identity" || result.RendererVersion != "v1" || result.RenderedContentHash != sourceHash {
		return errors.New("query source does not match the exact manual snapshot and rendering")
	}
	configHash := "sha256:" + digest(map[string]string{"model": extractor.Model, "source_mode": "frozen_local_snapshot"})
	if result.Extractor.ExtractionAttemptID != handoff.ExtractionAttemptID || result.Extractor.AttemptStatus != "succeeded" ||
		result.Extractor.Name != strings.TrimSpace(extractor.Name) || result.Extractor.Version != strings.TrimSpace(extractor.Version) ||
		result.Extractor.ConfigHash != configHash || result.Extractor.ProducerSessionRef != "" ||
		!pendingID(result.Extractor.ExtractorDefinitionID, "extractor:") || !pendingID(result.Extractor.ExtractionRunID, "run:") {
		return errors.New("query extractor does not match the pending handoff producer and attempt")
	}
	if len(result.SourceRefs) != record.Citation.EndLine-record.Citation.StartLine+1 {
		return errors.New("query source reference count does not match the exact citation")
	}
	// manual-line-v1 numbers only nonempty spans, while citations count all
	// lines. Original CRLF bytes still contribute to subsequent byte offsets.
	startByte, spanNumber, refIndex := 0, 0, 0
	for index, line := range strings.Split(document.RawText(), "\n") {
		text := strings.TrimSuffix(line, "\r")
		if text != "" {
			spanNumber++
			if index+1 >= record.Citation.StartLine && index+1 <= record.Citation.EndLine {
				ref := result.SourceRefs[refIndex]
				if ref.TargetKind != "" || ref.RepositorySnapshotID != "" || ref.FileSnapshotID != "" || ref.RepoID != "" || ref.CommitSHA != "" || ref.Path != "" ||
					ref.ExtractionViewID != handoff.ExtractionViewID || ref.SpanID != fmt.Sprintf("span:S%d", spanNumber) ||
					ref.StartByte == nil || ref.EndByte == nil || *ref.StartByte != startByte || *ref.EndByte != startByte+len(text) ||
					ref.QuotedText != text || ref.QuotedTextHash != contentHash(text) {
					return errors.New("query source references do not match the ordered exact citation bytes")
				}
				refIndex++
			}
		}
		startByte += len(line) + 1
	}
	return nil
}

func pendingID(value, prefix string) bool {
	return validText(value, 256) && strings.HasPrefix(value, prefix) && len(value) > len(prefix)
}
