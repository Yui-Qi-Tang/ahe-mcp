//go:build integration

package evidenceingestion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationManualTextRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "round-trip")

	result, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("IngestManualText() error = %v", err)
	}
	got, err := GetProposalByOccurrenceID(ctx, pool, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID() error = %v", err)
	}
	trace, err := TraceProposalProvenance(ctx, pool, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}

	if got.ProposalOccurrenceID != result.ProposalOccurrenceID {
		t.Fatalf("proposal occurrence ID = %q, want %q", got.ProposalOccurrenceID, result.ProposalOccurrenceID)
	}
	if got.StatementText != "Refunds must be completed within 7 days." {
		t.Fatalf("statement text = %q", got.StatementText)
	}
	if got.AdmissionOutcome != admissionOutcomePending {
		t.Fatalf("admission outcome = %q, want pending", got.AdmissionOutcome)
	}
	if got.CanonicalRef != "" {
		t.Fatalf("canonical ref = %q, want empty", got.CanonicalRef)
	}
	if len(got.SourceRefs) != 1 {
		t.Fatalf("len(source refs) = %d, want 1", len(got.SourceRefs))
	}
	ref := got.SourceRefs[0]
	if ref.SpanID != "span:S1" || ref.StartByte != 0 || ref.QuotedText != got.StatementText {
		t.Fatalf("unexpected source ref: %+v", ref)
	}
	if got.SourceSystem != SourceSystemManualText || got.SourceID != "fixture-refund-policy" || got.SourceVersion != "v1" {
		t.Fatalf("unexpected source identity: %+v", got)
	}
	if got.RendererName != RendererManualTextIdentity || got.RendererVersion != RendererManualTextIdentityVersion {
		t.Fatalf("unexpected renderer: %+v", got)
	}
	if got.ExtractorName != ExtractorFrozenManualFixture || got.ExtractorVersion != ExtractorFrozenManualFixtureVersion {
		t.Fatalf("unexpected extractor: %+v", got)
	}
	if got.ProposalFingerprint == "" || got.ProposalFingerprintVersion != ProposalFingerprintStatementV1 {
		t.Fatalf("fingerprint not populated: %+v", got)
	}
	if !reflect.DeepEqual(trace, got) {
		t.Fatalf("trace result differs from get result")
	}
}

func TestIntegrationIdempotentSourceSubmission(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "source-a")
	if _, err := IngestManualText(ctx, pool, input, fixture); err != nil {
		t.Fatalf("first IngestManualText() error = %v", err)
	}
	input.RequestID = "source-b"
	if _, err := IngestManualText(ctx, pool, input, fixture); err != nil {
		t.Fatalf("second IngestManualText() error = %v", err)
	}
	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
}

func TestIntegrationSameOccurrenceReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "same-occurrence")
	first, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("first IngestManualText() error = %v", err)
	}
	second, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("second IngestManualText() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second ingestion Replayed = false, want true")
	}
	if first.ProposalOccurrenceID != second.ProposalOccurrenceID {
		t.Fatalf("replay occurrence ID = %q, want %q", second.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationSameFingerprintDifferentAttempts(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "fingerprint-run-a")
	first, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("first IngestManualText() error = %v", err)
	}
	input.RequestID = "fingerprint-run-b"
	second, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("second IngestManualText() error = %v", err)
	}
	if first.ProposalOccurrenceID == second.ProposalOccurrenceID {
		t.Fatalf("different runs produced same occurrence ID")
	}
	if first.ProposalFingerprint != second.ProposalFingerprint {
		t.Fatalf("fingerprint mismatch: %s != %s", first.ProposalFingerprint, second.ProposalFingerprint)
	}
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM proposal_occurrences
		WHERE proposal_fingerprint = $1
	`, first.ProposalFingerprint).Scan(&count); err != nil {
		t.Fatalf("count fingerprint occurrences: %v", err)
	}
	if count != 2 {
		t.Fatalf("fingerprint occurrence count = %d, want 2", count)
	}
}

func TestIntegrationInvalidSpanPersistsFailure(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "invalid-span")
	fixture.Proposals[0].EvidenceRefs = []string{"span:S404"}
	_, err := IngestManualText(ctx, pool, input, fixture)
	assertKind(t, err, ErrorUnknownSpan)

	attemptCtx := mustAttemptContext(t, input)
	assertAttemptFailedNoBatch(t, ctx, pool, attemptCtx, ErrorUnknownSpan)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationHashMismatchPersistsFailure(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "hash-mismatch")
	attemptCtx := mustAttemptContext(t, input)
	db := pgxDB{pool: pool}
	if err := persistSourceSnapshot(ctx, db, attemptCtx); err != nil {
		t.Fatalf("persistSourceSnapshot() error = %v", err)
	}
	if _, err := persistAttemptStart(ctx, db, attemptCtx); err != nil {
		t.Fatalf("persistAttemptStart() error = %v", err)
	}
	attemptCtx.Spans[0].QuotedTextHash = "sha256:bad"
	_, err := materializeBatch(attemptCtx, fixture)
	assertKind(t, err, ErrorQuotedHashMismatch)
	if err := persistAttemptFailure(ctx, db, attemptCtx, fixture, err); err != nil {
		t.Fatalf("persistAttemptFailure() error = %v", err)
	}

	assertAttemptFailedNoBatch(t, ctx, pool, attemptCtx, ErrorQuotedHashMismatch)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationCaptureManualSourceRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "source-intake")

	result, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	if result.Replayed {
		t.Fatalf("first capture Replayed = true, want false")
	}
	if result.SourceSnapshotID == "" || result.ExtractionViewID == "" {
		t.Fatalf("capture IDs not populated: %+v", result)
	}
	if result.SpanCatalogVersion != SpanCatalogManualLineV1 {
		t.Fatalf("span catalog version = %q, want %q", result.SpanCatalogVersion, SpanCatalogManualLineV1)
	}
	if len(result.Spans) != 2 || result.Spans[0].SpanID != "span:S1" || result.Spans[0].StartByte != 0 {
		t.Fatalf("unexpected spans: %+v", result.Spans)
	}

	var raw []byte
	if err := pool.QueryRow(ctx, `
		SELECT raw_content
		FROM source_blobs
		WHERE raw_content_hash = $1
	`, result.RawContentHash).Scan(&raw); err != nil {
		t.Fatalf("read raw source bytes: %v", err)
	}
	if string(raw) != string(input.Raw) {
		t.Fatalf("raw bytes = %q, want %q", string(raw), string(input.Raw))
	}

	var rendered []byte
	if err := pool.QueryRow(ctx, `
		SELECT rendered_content
		FROM extraction_views
		WHERE extraction_view_id = $1
	`, result.ExtractionViewID).Scan(&rendered); err != nil {
		t.Fatalf("read rendered bytes: %v", err)
	}
	if string(rendered) != string(input.Raw) {
		t.Fatalf("rendered bytes = %q, want %q", string(rendered), string(input.Raw))
	}

	var startByte, endByte int
	var quotedHash string
	var quoted []byte
	if err := pool.QueryRow(ctx, `
		SELECT start_byte, end_byte, quoted_text_hash, quoted_text
		FROM span_catalog_entries
		WHERE extraction_view_id = $1 AND span_id = 'span:S1'
	`, result.ExtractionViewID).Scan(&startByte, &endByte, &quotedHash, &quoted); err != nil {
		t.Fatalf("read span:S1: %v", err)
	}
	if startByte != result.Spans[0].StartByte || endByte != result.Spans[0].EndByte || quotedHash != result.Spans[0].QuotedTextHash || string(quoted) != result.Spans[0].QuotedText {
		t.Fatalf("persisted span mismatch: got [%d,%d) %s %q, want %+v", startByte, endByte, quotedHash, string(quoted), result.Spans[0])
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "extractor_definitions", 0)
	assertTableCount(t, ctx, pool, "extraction_runs", 0)
	assertTableCount(t, ctx, pool, "extraction_attempts", 0)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationCaptureManualSourceReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "source-intake-replay")

	first, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("first CaptureManualSource() error = %v", err)
	}
	second, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("second CaptureManualSource() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second capture Replayed = false, want true")
	}
	if second.SourceSnapshotID != first.SourceSnapshotID || second.ExtractionViewID != first.ExtractionViewID {
		t.Fatalf("replay IDs = %+v, want %+v", second, first)
	}
	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationCaptureManualSourceRequestConflictRollsBack(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "source-intake-conflict")
	if _, err := CaptureManualSource(ctx, pool, input); err != nil {
		t.Fatalf("first CaptureManualSource() error = %v", err)
	}

	input.Raw = []byte("Refunds must be completed within 8 days.\n")
	_, err := CaptureManualSource(ctx, pool, input)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationBuildExtractorInputReadOnly(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "extractor-input")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}

	got, err := BuildExtractorInput(ctx, pool, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("BuildExtractorInput() error = %v", err)
	}
	if got.SourceSnapshotID != source.SourceSnapshotID || got.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("extractor input source = %s/%s, want %s/%s", got.SourceSnapshotID, got.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if got.RenderedText != string(input.Raw) {
		t.Fatalf("rendered text = %q, want raw fixture", got.RenderedText)
	}
	if got.Renderer.Name != RendererManualTextIdentity || got.Renderer.Version != RendererManualTextIdentityVersion {
		t.Fatalf("renderer = %+v, want manual identity", got.Renderer)
	}
	if got.RenderedContentHash != source.RenderedContentHash || got.RawContentHash != source.RawContentHash {
		t.Fatalf("hashes = %s/%s, want %s/%s", got.RawContentHash, got.RenderedContentHash, source.RawContentHash, source.RenderedContentHash)
	}
	if len(got.Spans) != len(source.Spans) {
		t.Fatalf("span count = %d, want %d", len(got.Spans), len(source.Spans))
	}
	assertExtractorInputSpansOrdered(t, got.Spans)
	rendered := []byte(got.RenderedText)
	for i, span := range got.Spans {
		wantText := string(rendered[span.StartByte:span.EndByte])
		if span.Text != wantText {
			t.Fatalf("span %d text = %q, want %q", i, span.Text, wantText)
		}
		if gotHash := contentHash(rendered[span.StartByte:span.EndByte]); gotHash != span.QuotedTextHash {
			t.Fatalf("span %d hash = %s, want %s", i, span.QuotedTextHash, gotHash)
		}
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "extractor_definitions", 0)
	assertTableCount(t, ctx, pool, "extraction_runs", 0)
	assertTableCount(t, ctx, pool, "extraction_attempts", 0)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationBuildExtractorInputRejectsInvalidOrCorruptView(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "extractor-input-errors")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}

	_, err = BuildExtractorInput(ctx, pool, "bad-id")
	assertKind(t, err, ErrorInvalidRecordID)

	_, err = BuildExtractorInput(ctx, pool, "view:missing")
	assertKind(t, err, ErrorMissingSourceViewAttempt)

	if _, err := pool.Exec(ctx, `
		UPDATE span_catalog_entries
		SET quoted_text_hash = 'sha256:bad'
		WHERE extraction_view_id = $1
			AND span_id = 'span:S1'
	`, source.ExtractionViewID); err != nil {
		t.Fatalf("corrupt span hash: %v", err)
	}
	_, err = BuildExtractorInput(ctx, pool, source.ExtractionViewID)
	assertKind(t, err, ErrorQuotedHashMismatch)
}

func TestIntegrationSubmitExtractorOutputRoundTrip(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "source-for-output")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}

	result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:           "extractor-output-round-trip",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              fixture,
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput() error = %v", err)
	}
	got, err := TraceProposalProvenance(ctx, pool, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.SourceSnapshotID != source.SourceSnapshotID || got.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("trace source = %s/%s, want %s/%s", got.SourceSnapshotID, got.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if got.StatementText != "Refunds must be completed within 7 days." {
		t.Fatalf("statement text = %q", got.StatementText)
	}
	if got.AdmissionOutcome != admissionOutcomePending || got.CanonicalRef != "" {
		t.Fatalf("admission/canonical = %q/%q, want pending/empty", got.AdmissionOutcome, got.CanonicalRef)
	}
	if len(got.SourceRefs) != 1 || got.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("source refs = %+v, want span:S1", got.SourceRefs)
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "extractor_definitions", 1)
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationCombinedAndSplitFlowsShareMaterializationIdentity(t *testing.T) {
	ctx, pool := integrationPool(t)
	combinedInput, fixture := integrationInputFixture(t, "combined-materialization")
	combined, err := IngestManualText(ctx, pool, combinedInput, fixture)
	if err != nil {
		t.Fatalf("IngestManualText() error = %v", err)
	}
	splitInput := combinedInput
	splitInput.RequestID = "split-source-materialization"
	source, err := CaptureManualSource(ctx, pool, splitInput)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	split, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:           "split-extractor-materialization",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              fixture,
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput() error = %v", err)
	}
	if split.ProposalOccurrenceID == combined.ProposalOccurrenceID {
		t.Fatalf("split flow reused combined occurrence ID")
	}
	if split.ProposalFingerprint != combined.ProposalFingerprint {
		t.Fatalf("split fingerprint = %q, want combined %q", split.ProposalFingerprint, combined.ProposalFingerprint)
	}

	combinedTrace, err := TraceProposalProvenance(ctx, pool, combined.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance(combined) error = %v", err)
	}
	splitTrace, err := TraceProposalProvenance(ctx, pool, split.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance(split) error = %v", err)
	}
	if combinedTrace.StatementText != splitTrace.StatementText {
		t.Fatalf("statement mismatch: %q != %q", combinedTrace.StatementText, splitTrace.StatementText)
	}
	if !reflect.DeepEqual(combinedTrace.SourceRefs, splitTrace.SourceRefs) {
		t.Fatalf("source refs mismatch: %+v != %+v", combinedTrace.SourceRefs, splitTrace.SourceRefs)
	}

	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "extraction_attempts", 2)
	assertTableCount(t, ctx, pool, "proposal_batches", 2)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 2)
}

func TestIntegrationSubmitExtractorOutputReplayAndConflict(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "source-for-output-replay")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	output := ExtractorOutputInput{
		RequestID:           "extractor-output-replay",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              fixture,
	}
	first, err := SubmitExtractorOutput(ctx, pool, output)
	if err != nil {
		t.Fatalf("first SubmitExtractorOutput() error = %v", err)
	}
	second, err := SubmitExtractorOutput(ctx, pool, output)
	if err != nil {
		t.Fatalf("second SubmitExtractorOutput() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second submit Replayed = false, want true")
	}
	if second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replay occurrence = %q, want %q", second.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}

	output.Output.Proposals[0].StatementText = "Refunds must be completed within 8 days."
	_, err = SubmitExtractorOutput(ctx, pool, output)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationSubmitExtractorOutputSessionChangeReplaysFirstAnnotation(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "source-for-session-replay")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	output := ExtractorOutputInput{
		RequestID:           "extractor-session-replay",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ProducerSessionRef:  "session:first",
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              fixture,
	}
	first, err := SubmitExtractorOutput(ctx, pool, output)
	if err != nil {
		t.Fatalf("first SubmitExtractorOutput() error = %v", err)
	}
	output.ProducerSessionRef = "session:retry"
	second, err := SubmitExtractorOutput(ctx, pool, output)
	if err != nil {
		t.Fatalf("second SubmitExtractorOutput() error = %v", err)
	}
	if !second.Replayed || second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("session retry result = %+v, want replay of %s", second, first.ProposalOccurrenceID)
	}
	proposal, err := TraceProposalProvenance(ctx, pool, first.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if proposal.ProducerSessionRef != "session:first" {
		t.Fatalf("producer session ref = %q, want first annotation", proposal.ProducerSessionRef)
	}
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationProposalSuccessRejectsStaleConcurrentCompletion(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, firstOutput := integrationInputFixture(t, "stale-concurrent-completion-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	sourceCtx, err := loadManualSourceContext(ctx, pgxDB{pool: pool}, source.SourceSnapshotID, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContext() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSource(sourceCtx, "stale-concurrent-completion", 0)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	if _, err := persistAttemptStartForExistingSource(ctx, pgxDB{pool: pool}, attemptCtx); err != nil {
		t.Fatalf("persistAttemptStartForExistingSource() error = %v", err)
	}

	firstBatch, err := materializeBatch(attemptCtx, firstOutput)
	if err != nil {
		t.Fatalf("materialize first output: %v", err)
	}
	secondOutput := firstOutput
	secondOutput.Proposals = append([]ExtractorProposalOutput(nil), firstOutput.Proposals...)
	secondOutput.Proposals[0].ProposalLocalID = "stale-second-completion"
	secondBatch, err := materializeBatch(attemptCtx, secondOutput)
	if err != nil {
		t.Fatalf("materialize second output: %v", err)
	}

	replayed, err := persistProposalSuccess(ctx, pgxDB{pool: pool}, firstBatch, firstOutput)
	if err != nil {
		t.Fatalf("persist first proposal success: %v", err)
	}
	if replayed {
		t.Fatal("persist first proposal success replayed = true, want false")
	}
	_, err = persistProposalSuccess(ctx, pgxDB{pool: pool}, secondBatch, secondOutput)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationSubmitExtractorOutputConcurrentConflictHasOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, firstOutput := integrationInputFixture(t, "concurrent-extractor-output-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	secondOutput := firstOutput
	secondOutput.Proposals = append([]ExtractorProposalOutput(nil), firstOutput.Proposals...)
	secondOutput.Proposals[0].ProposalLocalID = "concurrent-second-output"

	type call struct {
		result IngestResult
		err    error
	}
	calls := make(chan call, 2)
	start := make(chan struct{})
	for _, output := range []FrozenExtractorOutput{firstOutput, secondOutput} {
		go func(output FrozenExtractorOutput) {
			<-start
			result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
				RequestID:           "concurrent-extractor-output",
				SourceSnapshotID:    source.SourceSnapshotID,
				ExtractionViewID:    source.ExtractionViewID,
				ExtractorDefinition: testExternalExtractorDefinition(),
				Output:              output,
			})
			calls <- call{result: result, err: err}
		}(output)
	}
	close(start)

	successes := 0
	conflicts := 0
	for range 2 {
		got := <-calls
		if got.err == nil {
			if got.result.Replayed {
				t.Fatalf("winning result Replayed = true: %+v", got.result)
			}
			successes++
			continue
		}
		kind, ok := KindOf(got.err)
		if !ok || kind != ErrorIdempotencyKeyReused {
			t.Fatalf("concurrent SubmitExtractorOutput() error = %v, want %s", got.err, ErrorIdempotencyKeyReused)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = %d successes/%d conflicts, want 1/1", successes, conflicts)
	}

	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationSubmitExtractorOutputUnknownSpanPersistsFailure(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "source-for-output-unknown-span")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	fixture.Proposals[0].EvidenceRefs = []string{"span:S404"}
	_, err = SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:           "extractor-output-unknown-span",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              fixture,
	})
	assertKind(t, err, ErrorUnknownSpan)

	sourceCtx, err := loadManualSourceContext(ctx, pgxDB{pool: pool}, source.SourceSnapshotID, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContext() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, "extractor-output-unknown-span", 0, testExternalExtractorDefinition())
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	assertAttemptFailedNoBatch(t, ctx, pool, attemptCtx, ErrorUnknownSpan)

	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "span_catalog_entries", 2)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationRunTrustedExtractorRoundTripReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "trusted-round-trip-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	outputData, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("jsonBytes(fixture) error = %v", err)
	}
	calls := 0
	request := TrustedExtractorRequest{
		RequestID:        "trusted-round-trip",
		ExtractionViewID: source.ExtractionViewID,
	}
	first, err := RunTrustedExtractor(ctx, pool, request, func(_ context.Context, input ExtractorInput) ([]byte, error) {
		calls++
		if input.ExtractionViewID != source.ExtractionViewID {
			t.Fatalf("runner input view = %q, want %q", input.ExtractionViewID, source.ExtractionViewID)
		}
		return outputData, nil
	})
	if err != nil {
		t.Fatalf("RunTrustedExtractor() error = %v", err)
	}
	second, err := RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return nil, context.Canceled
	})
	if err != nil {
		t.Fatalf("replay RunTrustedExtractor() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("runner calls = %d, want 1", calls)
	}
	if !second.Replayed || second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replay result = %+v, want occurrence %s replayed", second, first.ProposalOccurrenceID)
	}

	assertTableCount(t, ctx, pool, "extractor_definitions", 1)
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationRunTrustedExtractorWithOllamaRunnerHTTP(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "trusted-ollama-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	outputData, err := jsonBytes(fixture)
	if err != nil {
		t.Fatalf("jsonBytes(fixture) error = %v", err)
	}
	var captured ollamaExtractorGenerateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("path = %s, want /api/generate", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if err := json.NewEncoder(w).Encode(ollamaExtractorGenerateResponse{Response: string(outputData)}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	runner, err := NewOllamaExtractorRunner(OllamaExtractorConfig{
		BaseURL:    server.URL,
		Model:      "gemma4:12b",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOllamaExtractorRunner() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:           "trusted-ollama",
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: runner.ExtractorDefinition(),
	}

	result, err := RunTrustedExtractor(ctx, pool, request, runner.Run)
	if err != nil {
		t.Fatalf("RunTrustedExtractor() error = %v", err)
	}
	got, err := TraceProposalProvenance(ctx, pool, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.ExtractorName != ExtractorOllamaLocal || got.ExtractorVersion != ExtractorOllamaLocalVersion {
		t.Fatalf("extractor provenance = %s/%s, want ollama-local/v1", got.ExtractorName, got.ExtractorVersion)
	}
	if !strings.Contains(captured.Prompt, source.ExtractionViewID) || !strings.Contains(captured.Prompt, "span:S1") {
		t.Fatalf("prompt did not include grounded view/span: %s", captured.Prompt)
	}
	assertTableCount(t, ctx, pool, "extractor_definitions", 1)
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationRunTrustedExtractorFailureAndRetry(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "trusted-retry-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:        "trusted-retry",
		ExtractionViewID: source.ExtractionViewID,
	}
	_, err = RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	assertKind(t, err, ErrorRunnerInvocationTimeout)

	sourceCtx, err := loadManualSourceContextByViewID(ctx, pgxDB{pool: pool}, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContextByViewID() error = %v", err)
	}
	failedAttempt, err := buildAttemptContextFromSource(sourceCtx, request.RequestID, 1)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	assertAttemptFailedNoBatch(t, ctx, pool, failedAttempt, ErrorRunnerInvocationTimeout)

	outputData, err := jsonBytes(fixture)
	if err != nil {
		t.Fatalf("jsonBytes(fixture) error = %v", err)
	}
	request.RetryFailedAttempt = true
	result, err := RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return outputData, nil
	})
	if err != nil {
		t.Fatalf("retry RunTrustedExtractor() error = %v", err)
	}
	if result.ExtractionRunID != failedAttempt.ExtractionRun.ID {
		t.Fatalf("retry run ID = %q, want %q", result.ExtractionRunID, failedAttempt.ExtractionRun.ID)
	}
	if result.ExtractionAttemptID == failedAttempt.ExtractionAttempt.ID {
		t.Fatalf("retry reused failed attempt %q", result.ExtractionAttemptID)
	}
	calls := 0
	replay, err := RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		calls++
		return nil, context.Canceled
	})
	if err != nil {
		t.Fatalf("retry replay RunTrustedExtractor() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("runner calls after successful retry = %d, want 0", calls)
	}
	if !replay.Replayed || replay.ProposalOccurrenceID != result.ProposalOccurrenceID {
		t.Fatalf("retry replay result = %+v, want occurrence %s replayed", replay, result.ProposalOccurrenceID)
	}

	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 2)
	assertTableCount(t, ctx, pool, "proposal_batches", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 1)
}

func TestIntegrationRunTrustedExtractorCancelledContextPersistsFailure(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "trusted-cancel-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:        "trusted-cancel",
		ExtractionViewID: source.ExtractionViewID,
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	outputData := []byte(`partial local runner output`)

	_, err = RunTrustedExtractor(cancelledCtx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		cancel()
		return outputData, context.Canceled
	})
	assertKind(t, err, ErrorRunnerInvocationCancelled)

	sourceCtx, err := loadManualSourceContextByViewID(ctx, pgxDB{pool: pool}, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContextByViewID() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSource(sourceCtx, request.RequestID, 1)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	assertAttemptFailedNoBatch(t, ctx, pool, attemptCtx, ErrorRunnerInvocationCancelled)
	assertAttemptOutputHash(t, ctx, pool, attemptCtx.ExtractionAttempt.ID, contentHash(outputData))
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationRunTrustedExtractorInvalidOutputHashPersisted(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "trusted-invalid-output-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	outputData := []byte(`{"proposals":[{"proposal_local_id":"stmt-1","statement_text":"Refunds must be completed within 7 days.","evidence_refs":["span:S1"],"canonical_id":"evidence:1"}]}`)
	request := TrustedExtractorRequest{
		RequestID:        "trusted-invalid-output",
		ExtractionViewID: source.ExtractionViewID,
	}

	_, err = RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return outputData, nil
	})
	assertKind(t, err, ErrorInvalidExtractorOutput)

	sourceCtx, err := loadManualSourceContextByViewID(ctx, pgxDB{pool: pool}, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContextByViewID() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSource(sourceCtx, request.RequestID, 1)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	assertAttemptFailedNoBatch(t, ctx, pool, attemptCtx, ErrorInvalidExtractorOutput)
	assertAttemptOutputHash(t, ctx, pool, attemptCtx.ExtractionAttempt.ID, contentHash(outputData))
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationRunTrustedExtractorMaterializationFailureOutputHashPersisted(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "trusted-materialization-failure-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("CaptureManualSource() error = %v", err)
	}
	fixture.Proposals[0].EvidenceRefs = []string{"span:S404"}
	outputData, err := jsonBytes(fixture)
	if err != nil {
		t.Fatalf("jsonBytes(fixture) error = %v", err)
	}
	request := TrustedExtractorRequest{
		RequestID:        "trusted-materialization-failure",
		ExtractionViewID: source.ExtractionViewID,
	}

	_, err = RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		return outputData, nil
	})
	assertKind(t, err, ErrorUnknownSpan)

	sourceCtx, err := loadManualSourceContextByViewID(ctx, pgxDB{pool: pool}, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("loadManualSourceContextByViewID() error = %v", err)
	}
	attemptCtx, err := buildAttemptContextFromSource(sourceCtx, request.RequestID, 1)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSource() error = %v", err)
	}
	assertAttemptFailedNoBatch(t, ctx, pool, attemptCtx, ErrorUnknownSpan)
	assertAttemptOutputHash(t, ctx, pool, attemptCtx.ExtractionAttempt.ID, contentHash(outputData))
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationAdmitPendingProposalCreatesCanonicalGraph(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "admission-source")
	ingested, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("IngestManualText() error = %v", err)
	}

	admission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		DecisionBy:           "integration-test",
		DecisionReason:       "fixture statement accepted",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal() error = %v", err)
	}
	if admission.AdmissionOutcome != admissionOutcomeAdmitted || admission.CanonicalRef == "" {
		t.Fatalf("admission result = %+v, want admitted with canonical ref", admission)
	}
	if len(admission.RawEvidenceNodeIDs) != 1 || len(admission.CanonicalEdgeIDs) != 1 {
		t.Fatalf("admission graph IDs = raw %v edges %v, want one raw node and one edge", admission.RawEvidenceNodeIDs, admission.CanonicalEdgeIDs)
	}

	got, err := TraceProposalProvenance(ctx, pool, ingested.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.AdmissionOutcome != admissionOutcomeAdmitted || got.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("proposal state = %s/%s, want admitted/%s", got.AdmissionOutcome, got.CanonicalRef, admission.CanonicalRef)
	}

	canonical, err := GetCanonicalEvidenceByID(ctx, pool, admission.CanonicalRef)
	if err != nil {
		t.Fatalf("GetCanonicalEvidenceByID() error = %v", err)
	}
	if canonical.CanonicalID != admission.CanonicalRef || canonical.NodeKind != "source_claim" {
		t.Fatalf("canonical record = %s/%s, want source_claim %s", canonical.CanonicalID, canonical.NodeKind, admission.CanonicalRef)
	}
	if canonical.Payload.Claim != "Refunds must be completed within 7 days." {
		t.Fatalf("canonical claim = %q", canonical.Payload.Claim)
	}
	if canonical.OriginProposalOccurrenceID != ingested.ProposalOccurrenceID || canonical.OriginProposal.ProposalOccurrenceID != ingested.ProposalOccurrenceID {
		t.Fatalf("canonical origin = %q/%q, want %q", canonical.OriginProposalOccurrenceID, canonical.OriginProposal.ProposalOccurrenceID, ingested.ProposalOccurrenceID)
	}
	if len(canonical.OriginProposal.SourceRefs) != 1 || canonical.OriginProposal.SourceRefs[0].SpanID != "span:S1" {
		t.Fatalf("canonical origin refs = %+v, want span:S1", canonical.OriginProposal.SourceRefs)
	}

	var fromKind, toKind, relation string
	if err := pool.QueryRow(ctx, `
		SELECT from_node.node_kind, to_node.node_kind, edge.relation
		FROM canonical_graph_edges edge
		JOIN canonical_graph_nodes from_node
			ON from_node.canonical_node_id = edge.from_node_id
		JOIN canonical_graph_nodes to_node
			ON to_node.canonical_node_id = edge.to_node_id
		WHERE edge.canonical_edge_id = $1
	`, admission.CanonicalEdgeIDs[0]).Scan(&fromKind, &toKind, &relation); err != nil {
		t.Fatalf("read canonical graph edge: %v", err)
	}
	if fromKind != "raw_evidence" || toKind != "source_claim" || relation != "supports_claim" {
		t.Fatalf("canonical graph edge = %s -%s-> %s, want raw_evidence -supports_claim-> source_claim", fromKind, relation, toKind)
	}

	replay, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: ingested.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("replay AdmitPendingProposal() error = %v", err)
	}
	if !replay.Replayed || replay.AdmissionDecisionID != admission.AdmissionDecisionID || replay.CanonicalRef != admission.CanonicalRef {
		t.Fatalf("replay admission = %+v, want replay of %+v", replay, admission)
	}
	assertTableCount(t, ctx, pool, "admission_decisions", 1)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 2)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 1)
}

func TestIntegrationRecordPendingProposalDispositionPreservesAuditWithoutCanonicalGraph(t *testing.T) {
	ctx, pool := integrationPool(t)
	for _, outcome := range []string{
		ProposalDispositionRejected,
		ProposalDispositionAuditOnly,
	} {
		input, fixture := integrationInputFixture(t, "disposition-"+outcome)
		ingested, err := IngestManualText(ctx, pool, input, fixture)
		if err != nil {
			t.Fatalf("IngestManualText(%s) error = %v", outcome, err)
		}
		decision, err := RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{
			ProposalOccurrenceID: ingested.ProposalOccurrenceID,
			Outcome:              outcome,
			DecisionBy:           "integration-test",
			DecisionReason:       "reviewed for non-canonical retention",
		})
		if err != nil {
			t.Fatalf("RecordPendingProposalDisposition(%s) error = %v", outcome, err)
		}
		if decision.AdmissionOutcome != outcome || decision.AdmissionDecisionID == "" || decision.Replayed {
			t.Fatalf("disposition result = %+v", decision)
		}
		if decision.DecisionBy != "integration-test" ||
			decision.DecisionReason != "reviewed for non-canonical retention" {
			t.Fatalf("disposition audit metadata = %+v", decision)
		}

		got, err := TraceProposalProvenance(ctx, pool, ingested.ProposalOccurrenceID)
		if err != nil {
			t.Fatalf("TraceProposalProvenance(%s) error = %v", outcome, err)
		}
		if got.AdmissionOutcome != outcome || got.CanonicalRef != "" {
			t.Fatalf("proposal state = %s/%s, want %s/empty", got.AdmissionOutcome, got.CanonicalRef, outcome)
		}

		replay, err := RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{
			ProposalOccurrenceID: ingested.ProposalOccurrenceID,
			Outcome:              outcome,
			DecisionBy:           "integration-test",
			DecisionReason:       "reviewed for non-canonical retention",
		})
		if err != nil {
			t.Fatalf("replay RecordPendingProposalDisposition(%s) error = %v", outcome, err)
		}
		if !replay.Replayed ||
			replay.AdmissionDecisionID != decision.AdmissionDecisionID ||
			replay.DecisionBy != decision.DecisionBy ||
			replay.DecisionReason != decision.DecisionReason {
			t.Fatalf("replay = %+v, want decision %s", replay, decision.AdmissionDecisionID)
		}
		_, err = RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{
			ProposalOccurrenceID: ingested.ProposalOccurrenceID,
			Outcome:              outcome,
			DecisionBy:           "different-reviewer",
			DecisionReason:       "must not replace the first decision",
		})
		assertKind(t, err, ErrorAdmissionStateConflict)
	}

	var nonCanonicalDecisions, canonicalDecisions int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (
				WHERE outcome IN ('rejected', 'audit_only')
					AND canonical_ref IS NULL
					AND raw_evidence_node_ids = '[]'::jsonb
					AND canonical_edge_ids = '[]'::jsonb
			),
			count(*) FILTER (WHERE canonical_ref IS NOT NULL)
		FROM admission_decisions
	`).Scan(&nonCanonicalDecisions, &canonicalDecisions); err != nil {
		t.Fatalf("read disposition decisions: %v", err)
	}
	if nonCanonicalDecisions != 2 || canonicalDecisions != 0 {
		t.Fatalf("decision counts = non-canonical %d canonical %d, want 2/0", nonCanonicalDecisions, canonicalDecisions)
	}
	assertTableCount(t, ctx, pool, "admission_decisions", 2)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func TestIntegrationConcurrentPendingProposalDispositionHasOneTerminalWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "disposition-concurrent")
	ingested, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("IngestManualText() error = %v", err)
	}

	type dispositionCall struct {
		result ProposalDispositionResult
		err    error
	}
	calls := make(chan dispositionCall, 2)
	for _, outcome := range []string{
		ProposalDispositionRejected,
		ProposalDispositionAuditOnly,
	} {
		go func(outcome string) {
			result, err := RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{
				ProposalOccurrenceID: ingested.ProposalOccurrenceID,
				Outcome:              outcome,
				DecisionBy:           "integration-test",
				DecisionReason:       "concurrent terminal decision",
			})
			calls <- dispositionCall{result: result, err: err}
		}(outcome)
	}

	var winner ProposalDispositionResult
	conflicts := 0
	for range 2 {
		call := <-calls
		if call.err == nil {
			if winner.AdmissionDecisionID != "" {
				t.Fatalf("multiple successful dispositions: first %+v second %+v", winner, call.result)
			}
			winner = call.result
			continue
		}
		var domainErr *DomainError
		if !errors.As(call.err, &domainErr) || domainErr.Kind != ErrorAdmissionStateConflict {
			t.Fatalf("concurrent disposition error = %v, want admission state conflict", call.err)
		}
		conflicts++
	}
	if winner.AdmissionDecisionID == "" || conflicts != 1 {
		t.Fatalf("concurrent result = winner %+v conflicts %d, want one/one", winner, conflicts)
	}
	got, err := TraceProposalProvenance(ctx, pool, ingested.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("TraceProposalProvenance() error = %v", err)
	}
	if got.AdmissionOutcome != winner.AdmissionOutcome || got.CanonicalRef != "" {
		t.Fatalf("proposal state = %s/%s, want winner %s/empty", got.AdmissionOutcome, got.CanonicalRef, winner.AdmissionOutcome)
	}
	assertTableCount(t, ctx, pool, "admission_decisions", 1)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func TestIntegrationAdmitExactDuplicateReusesCanonicalGraph(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "admission-duplicate-a")
	first, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("first IngestManualText() error = %v", err)
	}
	input.RequestID = "admission-duplicate-b"
	second, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatalf("second IngestManualText() error = %v", err)
	}
	if first.ProposalOccurrenceID == second.ProposalOccurrenceID || first.ProposalFingerprint != second.ProposalFingerprint {
		t.Fatalf("fixture did not create duplicate fingerprint across distinct occurrences: first %+v second %+v", first, second)
	}

	firstAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("first AdmitPendingProposal() error = %v", err)
	}
	secondAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: second.ProposalOccurrenceID})
	if err != nil {
		t.Fatalf("second AdmitPendingProposal() error = %v", err)
	}
	if secondAdmission.CanonicalRef != firstAdmission.CanonicalRef {
		t.Fatalf("second canonical ref = %s, want reused %s", secondAdmission.CanonicalRef, firstAdmission.CanonicalRef)
	}
	assertTableCount(t, ctx, pool, "admission_decisions", 2)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 2)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 1)
}

func integrationPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_ingestion_test_" + randomHex(t, 8)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, readMigrations(t)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return ctx, pool
}

func integrationInputFixture(t *testing.T, requestID string) (ManualTextInput, FrozenExtractorOutput) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	raw, err := os.ReadFile(filepath.Join(dir, "testdata", "manual_refund_policy.txt"))
	if err != nil {
		t.Fatalf("read manual fixture: %v", err)
	}
	fixtureData, err := os.ReadFile(filepath.Join(dir, "testdata", "frozen_fixture.json"))
	if err != nil {
		t.Fatalf("read extractor fixture: %v", err)
	}
	var fixture FrozenExtractorOutput
	if err := json.Unmarshal(fixtureData, &fixture); err != nil {
		t.Fatalf("parse extractor fixture: %v", err)
	}
	return ManualTextInput{
		SourceID:      "fixture-refund-policy",
		SourceVersion: "v1",
		Raw:           raw,
		OriginMetadata: map[string]string{
			"fixture": "manual_refund_policy",
		},
		RequestID:     requestID,
		AttemptNumber: 1,
	}, fixture
}

func readMigrations(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no migrations found")
	}
	sort.Strings(paths)
	var combined strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		combined.Write(data)
		combined.WriteString("\n")
	}
	return combined.String()
}

func assertTableCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertAttemptFailedNoBatch(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptCtx attemptContext, want ErrorKind) {
	t.Helper()
	var attemptStatus, failureClass string
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_class
		FROM extraction_attempts
		WHERE extraction_attempt_id = $1
	`, attemptCtx.ExtractionAttempt.ID).Scan(&attemptStatus, &failureClass); err != nil {
		t.Fatalf("read failed attempt: %v", err)
	}
	if attemptStatus != attemptStatusFailed || failureClass != string(want) {
		t.Fatalf("attempt status/class = %q/%q, want failed/%s", attemptStatus, failureClass, want)
	}
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
}

func assertAttemptOutputHash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `
		SELECT output_hash
		FROM extraction_attempts
		WHERE extraction_attempt_id = $1
	`, attemptID).Scan(&got); err != nil {
		t.Fatalf("read attempt output hash: %v", err)
	}
	if got != want {
		t.Fatalf("attempt output_hash = %q, want %q", got, want)
	}
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}
