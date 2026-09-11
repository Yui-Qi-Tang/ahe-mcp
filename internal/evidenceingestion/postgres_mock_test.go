package evidenceingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5"
)

func TestMockSQLManualTextRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()

	result, err := ingestManualText(ctx, db, testManualInput("mock-round-trip"), testFixture())
	if err != nil {
		t.Fatalf("ingestManualText() error = %v", err)
	}
	got, err := getProposalByOccurrenceID(ctx, db, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("getProposalByOccurrenceID() error = %v", err)
	}
	trace, err := traceProposalProvenance(ctx, db, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("traceProposalProvenance() error = %v", err)
	}

	if got.ProposalOccurrenceID != result.ProposalOccurrenceID {
		t.Fatalf("proposal occurrence ID = %q, want %q", got.ProposalOccurrenceID, result.ProposalOccurrenceID)
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
	if got.ExtractionAttemptStatus != attemptStatusSucceeded {
		t.Fatalf("attempt status = %q, want succeeded", got.ExtractionAttemptStatus)
	}
	if got.SourceSystem != SourceSystemManualText {
		t.Fatalf("source system = %q, want %q", got.SourceSystem, SourceSystemManualText)
	}
	if got.RendererName != RendererManualTextIdentity || got.ExtractorName != ExtractorFrozenManualFixture {
		t.Fatalf("unexpected provenance: %+v", got)
	}
	if !reflect.DeepEqual(trace, got) {
		t.Fatalf("trace result differs from get result")
	}
	if len(db.proposalOccurrences) != 1 {
		t.Fatalf("proposal occurrence count = %d, want 1", len(db.proposalOccurrences))
	}
}

func TestMockSQLListProposalRecordsFiltersAndLimit(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	first, err := ingestManualText(ctx, db, testManualInput("mock-list-a"), testFixture())
	if err != nil {
		t.Fatalf("first ingestManualText() error = %v", err)
	}
	second, err := ingestManualText(ctx, db, testManualInput("mock-list-b"), testFixture())
	if err != nil {
		t.Fatalf("second ingestManualText() error = %v", err)
	}
	if _, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: first.ProposalOccurrenceID}); err != nil {
		t.Fatalf("admitPendingProposal() error = %v", err)
	}

	bySource, err := listProposalRecords(ctx, db, ProposalListInput{SourceID: "mock-list-a"})
	if err != nil {
		t.Fatalf("listProposalRecords(source) error = %v", err)
	}
	if len(bySource) != 1 || bySource[0].ProposalOccurrenceID != first.ProposalOccurrenceID || bySource[0].AdmissionOutcome != admissionOutcomeAdmitted || bySource[0].CanonicalRef == "" {
		t.Fatalf("source-filtered results = %+v, want admitted first occurrence", bySource)
	}

	pending, err := listProposalRecords(ctx, db, ProposalListInput{AdmissionOutcome: admissionOutcomePending})
	if err != nil {
		t.Fatalf("listProposalRecords(pending) error = %v", err)
	}
	if len(pending) != 1 || pending[0].ProposalOccurrenceID != second.ProposalOccurrenceID {
		t.Fatalf("pending results = %+v, want second occurrence %s", pending, second.ProposalOccurrenceID)
	}

	limited, err := listProposalRecords(ctx, db, ProposalListInput{Limit: 1})
	if err != nil {
		t.Fatalf("listProposalRecords(limit) error = %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited results = %d, want 1", len(limited))
	}

	historical, err := listProposalRecords(ctx, db, ProposalListInput{LifecycleScope: ProposalLifecycleScopeHistorical})
	if err != nil {
		t.Fatalf("listProposalRecords(historical) error = %v", err)
	}
	if len(historical) != 0 {
		t.Fatalf("historical source-bound results = %d, want 0", len(historical))
	}

	all, err := listProposalRecords(ctx, db, ProposalListInput{LifecycleScope: ProposalLifecycleScopeAll})
	if err != nil {
		t.Fatalf("listProposalRecords(all) error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all results = %d, want 2", len(all))
	}
}

func TestMockSQLListProposalRecordsRejectsInvalidFilters(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	for _, tc := range []struct {
		name  string
		input ProposalListInput
		kind  ErrorKind
	}{
		{name: "bad snapshot prefix", input: ProposalListInput{SourceSnapshotID: "source:1"}, kind: ErrorInvalidRecordID},
		{name: "bad repository snapshot prefix", input: ProposalListInput{RepositorySnapshotID: "repository:1"}, kind: ErrorInvalidRecordID},
		{name: "bad source generation prefix", input: ProposalListInput{SourceGenerationID: "source-generation:1"}, kind: ErrorInvalidRecordID},
		{name: "mutually exclusive snapshots", input: ProposalListInput{SourceSnapshotID: "srcsnap:1", RepositorySnapshotID: "repo-snapshot:1"}, kind: ErrorInvalidInput},
		{name: "source snapshot with generation", input: ProposalListInput{SourceSnapshotID: "srcsnap:1", SourceGenerationID: "generation:1"}, kind: ErrorInvalidInput},
		{name: "source identity with generation", input: ProposalListInput{SourceID: "source:1", SourceGenerationID: "generation:1"}, kind: ErrorInvalidInput},
		{name: "source version with generation", input: ProposalListInput{SourceVersion: "v1", SourceGenerationID: "generation:1"}, kind: ErrorInvalidInput},
		{name: "bad outcome", input: ProposalListInput{AdmissionOutcome: "canonical_graph"}, kind: ErrorInvalidInput},
		{name: "bad lifecycle scope", input: ProposalListInput{LifecycleScope: "current"}, kind: ErrorInvalidInput},
		{name: "negative limit", input: ProposalListInput{Limit: -1}, kind: ErrorInvalidInput},
		{name: "too large limit", input: ProposalListInput{Limit: 101}, kind: ErrorInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := listProposalRecords(ctx, db, tc.input)
			assertKind(t, err, tc.kind)
		})
	}
}

func TestMockSQLReplayAndSameFingerprintDifferentAttempts(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-replay")

	first, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("first ingestManualText() error = %v", err)
	}
	replay, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("replay ingestManualText() error = %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("replay Replayed = false, want true")
	}
	if replay.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replay occurrence ID = %q, want %q", replay.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}
	if len(db.proposalOccurrences) != 1 {
		t.Fatalf("proposal occurrence count after replay = %d, want 1", len(db.proposalOccurrences))
	}

	input.RequestID = "mock-replay-comparison"
	second, err := ingestManualText(ctx, db, input, testFixture())
	if err != nil {
		t.Fatalf("comparison ingestManualText() error = %v", err)
	}
	if second.Replayed {
		t.Fatalf("comparison run Replayed = true, want false")
	}
	if second.ProposalOccurrenceID == first.ProposalOccurrenceID {
		t.Fatalf("comparison run reused occurrence ID %q", second.ProposalOccurrenceID)
	}
	if second.ProposalFingerprint != first.ProposalFingerprint {
		t.Fatalf("comparison fingerprint = %q, want %q", second.ProposalFingerprint, first.ProposalFingerprint)
	}
	if len(db.proposalOccurrences) != 2 {
		t.Fatalf("proposal occurrence count after comparison = %d, want 2", len(db.proposalOccurrences))
	}
}

func TestMockSQLFailurePersistsAttemptOnly(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-failure")
	fixture := testFixture()
	fixture.Proposals[0].EvidenceRefs = []string{"span:S404"}

	_, err := ingestManualText(ctx, db, input, fixture)
	assertKind(t, err, ErrorUnknownSpan)

	attemptCtx := mustAttemptContext(t, input)
	attempt := db.extractionAttempts[attemptCtx.ExtractionAttempt.ID]
	if attempt.status != attemptStatusFailed || attempt.failureClass != string(ErrorUnknownSpan) {
		t.Fatalf("attempt status/class = %q/%q, want failed/%s", attempt.status, attempt.failureClass, ErrorUnknownSpan)
	}
	if len(db.proposalBatches) != 0 {
		t.Fatalf("proposal batch count = %d, want 0", len(db.proposalBatches))
	}
	if len(db.proposalOccurrences) != 0 {
		t.Fatalf("proposal occurrence count = %d, want 0", len(db.proposalOccurrences))
	}
}

func TestMockSQLCaptureManualSourcePersistsOnlySourceAuthority(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()

	result, err := captureManualSource(ctx, db, testManualInput("mock-source-only"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	if result.Replayed {
		t.Fatalf("first capture Replayed = true, want false")
	}
	if result.SourceSnapshotID == "" || result.ExtractionViewID == "" || result.RawContentHash == "" || result.RenderedContentHash == "" {
		t.Fatalf("capture result IDs not populated: %+v", result)
	}
	if len(result.Spans) != 2 || result.Spans[0].SpanID != "span:S1" {
		t.Fatalf("spans = %+v, want deterministic line spans", result.Spans)
	}
	if len(db.sourceBlobs) != 1 || len(db.sourceSnapshots) != 1 || len(db.extractionViews) != 1 || len(db.spanEntries) != 2 {
		t.Fatalf("source authority counts = blobs %d snapshots %d views %d spans %d, want 1/1/1/2",
			len(db.sourceBlobs), len(db.sourceSnapshots), len(db.extractionViews), len(db.spanEntries))
	}
	if len(db.extractorDefinitions) != 0 || len(db.extractionRuns) != 0 || len(db.extractionAttempts) != 0 || len(db.proposalBatches) != 0 || len(db.proposalOccurrences) != 0 {
		t.Fatalf("capture created extractor/proposal state: defs %d runs %d attempts %d batches %d occurrences %d",
			len(db.extractorDefinitions), len(db.extractionRuns), len(db.extractionAttempts), len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func TestMockSQLCaptureManualSourceReplay(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-source-replay")

	first, err := captureManualSource(ctx, db, input)
	if err != nil {
		t.Fatalf("first captureManualSource() error = %v", err)
	}
	second, err := captureManualSource(ctx, db, input)
	if err != nil {
		t.Fatalf("second captureManualSource() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second capture Replayed = false, want true")
	}
	if second.SourceSnapshotID != first.SourceSnapshotID || second.ExtractionViewID != first.ExtractionViewID {
		t.Fatalf("replay IDs = %+v, want %+v", second, first)
	}
	if len(db.sourceBlobs) != 1 || len(db.sourceSnapshots) != 1 || len(db.extractionViews) != 1 || len(db.spanEntries) != 2 {
		t.Fatalf("source authority counts after replay = blobs %d snapshots %d views %d spans %d, want 1/1/1/2",
			len(db.sourceBlobs), len(db.sourceSnapshots), len(db.extractionViews), len(db.spanEntries))
	}
}

func TestMockSQLCaptureManualSourceRequestConflict(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-source-conflict")

	if _, err := captureManualSource(ctx, db, input); err != nil {
		t.Fatalf("first captureManualSource() error = %v", err)
	}
	input.Raw = []byte("Refunds must be completed within 8 days.\n")
	_, err := captureManualSource(ctx, db, input)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	if len(db.sourceIntakeRequests) != 1 {
		t.Fatalf("source intake request count = %d, want 1", len(db.sourceIntakeRequests))
	}
}

func TestMockSQLBuildExtractorInputFromExistingView(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-input"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	input, err := buildExtractorInput(ctx, db, source.ExtractionViewID)
	if err != nil {
		t.Fatalf("buildExtractorInput() error = %v", err)
	}
	if input.SourceSnapshotID != source.SourceSnapshotID || input.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("extractor input source = %s/%s, want %s/%s", input.SourceSnapshotID, input.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if input.Renderer.Name != RendererManualTextIdentity || input.Renderer.Version != RendererManualTextIdentityVersion {
		t.Fatalf("renderer = %+v, want manual identity", input.Renderer)
	}
	if input.RenderedText != string(testManualInput("mock-extractor-input").Raw) {
		t.Fatalf("rendered text = %q", input.RenderedText)
	}
	if len(input.Spans) != 2 || input.Spans[0].SpanID != "span:S1" || input.Spans[0].Text != "Refunds must be completed within 7 days." {
		t.Fatalf("spans = %+v, want grounded span:S1", input.Spans)
	}
	assertExtractorInputSpansOrdered(t, input.Spans)
	if len(db.extractorDefinitions) != 0 || len(db.extractionRuns) != 0 || len(db.extractionAttempts) != 0 || len(db.proposalBatches) != 0 || len(db.proposalOccurrences) != 0 {
		t.Fatalf("build extractor input created extractor/proposal state: defs %d runs %d attempts %d batches %d occurrences %d",
			len(db.extractorDefinitions), len(db.extractionRuns), len(db.extractionAttempts), len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func TestMockSQLBuildExtractorInputRejectsInvalidOrCorruptView(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-input-errors"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	_, err = buildExtractorInput(ctx, db, "bad-id")
	assertKind(t, err, ErrorInvalidRecordID)

	_, err = buildExtractorInput(ctx, db, "view:missing")
	assertKind(t, err, ErrorMissingSourceViewAttempt)

	key := source.ExtractionViewID + "\x00span:S1"
	span := db.spanEntries[key]
	span.QuotedTextHash = "sha256:bad"
	db.spanEntries[key] = span
	_, err = buildExtractorInput(ctx, db, source.ExtractionViewID)
	assertKind(t, err, ErrorQuotedHashMismatch)
}

func TestMockSQLSubmitExtractorOutputFromExistingSource(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("mock-extractor-output")
	source, err := captureManualSource(ctx, db, input)
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	result, err := submitExtractorOutput(ctx, db, ExtractorOutputInput{
		RequestID:           "extractor-output-request",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ProducerSessionRef:  "  claude-code-session:test  ",
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              testFixture(),
	})
	if err != nil {
		t.Fatalf("submitExtractorOutput() error = %v", err)
	}
	got, err := getProposalByOccurrenceID(ctx, db, result.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("getProposalByOccurrenceID() error = %v", err)
	}
	if got.SourceSnapshotID != source.SourceSnapshotID || got.ExtractionViewID != source.ExtractionViewID {
		t.Fatalf("proposal source = %s/%s, want %s/%s", got.SourceSnapshotID, got.ExtractionViewID, source.SourceSnapshotID, source.ExtractionViewID)
	}
	if got.StatementText != "Refunds must be completed within 7 days." {
		t.Fatalf("statement text = %q", got.StatementText)
	}
	if got.ProducerSessionRef != "claude-code-session:test" {
		t.Fatalf("producer session ref = %q, want trimmed debug reference", got.ProducerSessionRef)
	}
	if len(db.sourceBlobs) != 1 || len(db.sourceSnapshots) != 1 || len(db.extractionViews) != 1 || len(db.spanEntries) != 2 {
		t.Fatalf("source rows changed after extractor output: blobs %d snapshots %d views %d spans %d, want 1/1/1/2",
			len(db.sourceBlobs), len(db.sourceSnapshots), len(db.extractionViews), len(db.spanEntries))
	}
	if len(db.extractionAttempts) != 1 || len(db.proposalBatches) != 1 || len(db.proposalOccurrences) != 1 {
		t.Fatalf("proposal rows = attempts %d batches %d occurrences %d, want 1/1/1",
			len(db.extractionAttempts), len(db.proposalBatches), len(db.proposalOccurrences))
	}
}

func TestMockSQLSubmitExtractorOutputRequiresDefinition(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-definition-required"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	_, err = submitExtractorOutput(ctx, db, ExtractorOutputInput{
		RequestID:        "extractor-definition-required",
		SourceSnapshotID: source.SourceSnapshotID,
		ExtractionViewID: source.ExtractionViewID,
		Output:           testFixture(),
	})
	assertKind(t, err, ErrorInvalidInput)
	if len(db.extractionRuns) != 0 {
		t.Fatalf("extraction runs = %d, want no write without producer identity", len(db.extractionRuns))
	}
}

func TestMockSQLSubmitExtractorOutputRejectsOversizedProducerSessionRef(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-session-ref-limit"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}

	_, err = submitExtractorOutput(ctx, db, ExtractorOutputInput{
		RequestID:           "extractor-session-ref-limit",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ProducerSessionRef:  strings.Repeat("x", ProducerSessionRefMaxBytes+1),
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              testFixture(),
	})
	assertKind(t, err, ErrorInvalidInput)
	if len(db.extractionRuns) != 0 {
		t.Fatalf("extraction runs = %d, want no write after invalid session ref", len(db.extractionRuns))
	}
}

func TestMockSQLSubmitExtractorOutputReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-replay"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	input := ExtractorOutputInput{
		RequestID:           "extractor-output-replay",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              testFixture(),
	}

	first, err := submitExtractorOutput(ctx, db, input)
	if err != nil {
		t.Fatalf("first submitExtractorOutput() error = %v", err)
	}
	second, err := submitExtractorOutput(ctx, db, input)
	if err != nil {
		t.Fatalf("second submitExtractorOutput() error = %v", err)
	}
	if !second.Replayed {
		t.Fatalf("second submit Replayed = false, want true")
	}
	if second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("replay occurrence = %q, want %q", second.ProposalOccurrenceID, first.ProposalOccurrenceID)
	}

	input.Output.Proposals[0].StatementText = "Refunds must be completed within 8 days."
	_, err = submitExtractorOutput(ctx, db, input)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	if len(db.proposalOccurrences) != 1 {
		t.Fatalf("proposal occurrence count after conflict = %d, want 1", len(db.proposalOccurrences))
	}
}

func TestMockSQLSubmitExtractorOutputSessionChangeReplaysFirstAnnotation(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-session-replay"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	input := ExtractorOutputInput{
		RequestID:           "extractor-session-replay",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ProducerSessionRef:  "session:first",
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              testFixture(),
	}
	first, err := submitExtractorOutput(ctx, db, input)
	if err != nil {
		t.Fatalf("first submitExtractorOutput() error = %v", err)
	}
	input.ProducerSessionRef = "session:retry"
	second, err := submitExtractorOutput(ctx, db, input)
	if err != nil {
		t.Fatalf("second submitExtractorOutput() error = %v", err)
	}
	if !second.Replayed || second.ProposalOccurrenceID != first.ProposalOccurrenceID {
		t.Fatalf("session retry result = %+v, want replay of %s", second, first.ProposalOccurrenceID)
	}
	proposal, err := getProposalByOccurrenceID(ctx, db, first.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("getProposalByOccurrenceID() error = %v", err)
	}
	if proposal.ProducerSessionRef != "session:first" {
		t.Fatalf("producer session ref = %q, want first annotation", proposal.ProducerSessionRef)
	}
	if len(db.extractionRuns) != 1 {
		t.Fatalf("extraction runs = %d, want 1 semantic run", len(db.extractionRuns))
	}
}

func TestMockSQLSubmitExtractorOutputUnknownSpanPersistsFailure(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	source, err := captureManualSource(ctx, db, testManualInput("mock-extractor-unknown-span"))
	if err != nil {
		t.Fatalf("captureManualSource() error = %v", err)
	}
	fixture := testFixture()
	fixture.Proposals[0].EvidenceRefs = []string{"span:S404"}

	_, err = submitExtractorOutput(ctx, db, ExtractorOutputInput{
		RequestID:           "extractor-output-unknown-span",
		SourceSnapshotID:    source.SourceSnapshotID,
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
		Output:              fixture,
	})
	assertKind(t, err, ErrorUnknownSpan)
	if len(db.proposalOccurrences) != 0 {
		t.Fatalf("proposal occurrence count = %d, want 0", len(db.proposalOccurrences))
	}
	if len(db.extractionAttempts) != 1 || len(db.proposalBatches) != 0 {
		t.Fatalf("failure rows = attempts %d batches %d, want 1/0", len(db.extractionAttempts), len(db.proposalBatches))
	}
	for _, attempt := range db.extractionAttempts {
		if attempt.status != attemptStatusFailed || attempt.failureClass != string(ErrorUnknownSpan) {
			t.Fatalf("attempt status/class = %s/%s, want failed/%s", attempt.status, attempt.failureClass, ErrorUnknownSpan)
		}
	}
}

type mockSQLDB struct {
	sourceBlobs          map[string][]byte
	sourceSnapshots      map[string]mockSourceSnapshot
	extractionViews      map[string]mockExtractionView
	spanEntries          map[string]SpanEntry
	sourceIntakeRequests map[string]mockSourceIntakeRequest

	externalSourceReceipts map[string]mockExternalSourceReceipt

	extractorDefinitions  map[string]mockExtractorDefinition
	extractionRuns        map[string]mockExtractionRun
	extractionAttempts    map[string]mockExtractionAttempt
	proposalBatches       map[string]mockProposalBatch
	proposalOccurrences   map[string]ProposalOccurrence
	canonicalGraphNodes   map[string]CanonicalGraphNode
	canonicalGraphEdges   map[string]CanonicalGraphEdge
	canonicalDerivations  map[string]evidencegraph.DerivationRecord
	derivationOrigins     map[string]string
	derivationParentEdges map[string]map[string]string
	admissionDecisions    map[string]mockAdmissionDecision
	ordinaryManifests     map[string]mockOrdinaryAdmissionManifest
	ordinaryNodeBindings  map[string][]canonicalAdmissionNodeBinding
	ordinaryEdgeBindings  map[string][]canonicalAdmissionEdgeBinding
	supersessionEvents    map[string]struct{}
	boundedViewPreflights int
	sourceViewLoads       int
}

type mockSourceSnapshot struct {
	sourceSystem   string
	sourceID       string
	sourceVersion  string
	rawContentHash string
	originMetadata []byte
}

type mockExtractionView struct {
	sourceSnapshotID    string
	rendererName        string
	rendererVersion     string
	rendered            []byte
	renderedContentHash string
}

type mockSourceIntakeRequest struct {
	sourceSnapshotID   string
	extractionViewID   string
	requestPayloadHash string
	createdAt          time.Time
}

type mockExternalSourceReceipt struct {
	sourceSnapshotID      string
	extractionViewID      string
	envelopeSchemaVersion string
	collectorID           string
	connectorID           string
	observedAt            time.Time
	receiptPayloadHash    string
	createdAt             time.Time
}

type mockExtractorDefinition struct {
	name       string
	version    string
	configHash string
}

type mockExtractionRun struct {
	extractorDefinitionID string
	sourceSnapshotID      string
	extractionViewID      string
	requestID             string
	producerSessionRef    string
}

type mockExtractionAttempt struct {
	runID         string
	number        int
	status        string
	outputHash    string
	fixtureOutput string
	failureClass  string
}

type mockProposalBatch struct {
	attemptID     string
	status        string
	proposalCount int
}

type mockAdmissionDecision struct {
	proposalOccurrence string
	outcome            string
	canonicalRef       string
	rawEvidenceNodeIDs []string
	canonicalEdgeIDs   []string
	decisionBy         string
	decisionReason     string
	reviewContract     string
}

type mockOrdinaryAdmissionManifest struct {
	proposalID      string
	contractVersion string
	mutationKind    string
	canonicalRef    string
	outcome         string
}

func newMockSQLDB() *mockSQLDB {
	return &mockSQLDB{
		sourceBlobs:          map[string][]byte{},
		sourceSnapshots:      map[string]mockSourceSnapshot{},
		extractionViews:      map[string]mockExtractionView{},
		spanEntries:          map[string]SpanEntry{},
		sourceIntakeRequests: map[string]mockSourceIntakeRequest{},

		externalSourceReceipts: map[string]mockExternalSourceReceipt{},

		extractorDefinitions:  map[string]mockExtractorDefinition{},
		extractionRuns:        map[string]mockExtractionRun{},
		extractionAttempts:    map[string]mockExtractionAttempt{},
		proposalBatches:       map[string]mockProposalBatch{},
		proposalOccurrences:   map[string]ProposalOccurrence{},
		canonicalGraphNodes:   map[string]CanonicalGraphNode{},
		canonicalGraphEdges:   map[string]CanonicalGraphEdge{},
		canonicalDerivations:  map[string]evidencegraph.DerivationRecord{},
		derivationOrigins:     map[string]string{},
		derivationParentEdges: map[string]map[string]string{},
		admissionDecisions:    map[string]mockAdmissionDecision{},
		ordinaryManifests:     map[string]mockOrdinaryAdmissionManifest{},
		ordinaryNodeBindings:  map[string][]canonicalAdmissionNodeBinding{},
		ordinaryEdgeBindings:  map[string][]canonicalAdmissionEdgeBinding{},
		supersessionEvents:    map[string]struct{}{},
	}
}

func (db *mockSQLDB) begin(context.Context) (sqlTx, error) {
	return &mockSQLTx{db: db}, nil
}

func (db *mockSQLDB) query(ctx context.Context, query string, args ...any) (sqlRows, error) {
	return mockQuery(ctx, db, query, args...)
}

func (db *mockSQLDB) queryRow(ctx context.Context, query string, args ...any) sqlRow {
	return mockQueryRow(ctx, db, query, args...)
}

type mockSQLTx struct {
	db *mockSQLDB
}

func (tx *mockSQLTx) exec(_ context.Context, query string, args ...any) (execResult, error) {
	return mockExec(tx.db, query, args...)
}

func (tx *mockSQLTx) query(ctx context.Context, query string, args ...any) (sqlRows, error) {
	return mockQuery(ctx, tx.db, query, args...)
}

func (tx *mockSQLTx) queryRow(ctx context.Context, query string, args ...any) sqlRow {
	return mockQueryRow(ctx, tx.db, query, args...)
}

func (tx *mockSQLTx) commit(context.Context) error {
	return nil
}

func (tx *mockSQLTx) rollback(context.Context) error {
	return nil
}

type mockExecResult int64

func (r mockExecResult) RowsAffected() int64 {
	return int64(r)
}

func mockExec(db *mockSQLDB, query string, args ...any) (execResult, error) {
	switch {
	case strings.Contains(query, "INSERT INTO canonical_ordinary_admission_manifests"):
		decisionID := args[0].(string)
		if _, exists := db.ordinaryManifests[decisionID]; exists {
			return nil, fmt.Errorf("duplicate ordinary admission manifest %s", decisionID)
		}
		db.ordinaryManifests[decisionID] = mockOrdinaryAdmissionManifest{
			proposalID:      args[1].(string),
			contractVersion: args[2].(string),
			mutationKind:    args[3].(string),
			canonicalRef:    args[4].(string),
			outcome:         args[5].(string),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO canonical_ordinary_admission_node_bindings"):
		decisionID := args[0].(string)
		binding := canonicalAdmissionNodeBinding{
			Role:            args[1].(string),
			Position:        args[2].(int),
			NodeID:          args[3].(string),
			Materialization: canonicalRowMaterialization(args[4].(string)),
		}
		db.ordinaryNodeBindings[decisionID] = append(db.ordinaryNodeBindings[decisionID], binding)
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO canonical_ordinary_admission_edge_bindings"):
		decisionID := args[0].(string)
		binding := canonicalAdmissionEdgeBinding{
			Role:            args[1].(string),
			Position:        args[2].(int),
			EdgeID:          args[3].(string),
			Materialization: canonicalRowMaterialization(args[4].(string)),
		}
		db.ordinaryEdgeBindings[decisionID] = append(db.ordinaryEdgeBindings[decisionID], binding)
		return mockExecResult(1), nil
	case strings.Contains(query, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"):
		return mockExecResult(0), nil
	case strings.Contains(query, "pg_advisory_xact_lock"):
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO source_blobs"):
		hash := args[0].(string)
		if _, ok := db.sourceBlobs[hash]; ok {
			return mockExecResult(0), nil
		}
		db.sourceBlobs[hash] = append([]byte(nil), args[1].([]byte)...)
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO source_snapshots"):
		id := args[0].(string)
		if _, ok := db.sourceSnapshots[id]; ok {
			return mockExecResult(0), nil
		}
		db.sourceSnapshots[id] = mockSourceSnapshot{
			sourceSystem:   args[1].(string),
			sourceID:       args[2].(string),
			sourceVersion:  args[3].(string),
			rawContentHash: args[4].(string),
			originMetadata: []byte(args[5].(string)),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO extraction_views"):
		id := args[0].(string)
		if _, ok := db.extractionViews[id]; ok {
			return mockExecResult(0), nil
		}
		db.extractionViews[id] = mockExtractionView{
			sourceSnapshotID:    args[1].(string),
			rendererName:        args[2].(string),
			rendererVersion:     args[3].(string),
			rendered:            append([]byte(nil), args[4].([]byte)...),
			renderedContentHash: args[5].(string),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO span_catalog_entries"):
		key := args[0].(string) + "\x00" + args[1].(string)
		if _, ok := db.spanEntries[key]; ok {
			return mockExecResult(0), nil
		}
		db.spanEntries[key] = SpanEntry{
			ExtractionViewID:   args[0].(string),
			SpanID:             args[1].(string),
			SpanCatalogVersion: args[2].(string),
			StartByte:          args[3].(int),
			EndByte:            args[4].(int),
			DisplayLine:        args[5].(int),
			QuotedTextHash:     args[6].(string),
			QuotedText:         string(args[7].([]byte)),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO source_intake_requests"):
		id := args[0].(string)
		if _, ok := db.sourceIntakeRequests[id]; ok {
			return mockExecResult(0), nil
		}
		db.sourceIntakeRequests[id] = mockSourceIntakeRequest{
			sourceSnapshotID:   args[1].(string),
			extractionViewID:   args[2].(string),
			requestPayloadHash: args[3].(string),
			createdAt:          time.Date(2026, 8, 23, 2, 4, 5, 0, time.UTC),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO external_source_intake_receipts"):
		id := args[0].(string)
		if _, ok := db.externalSourceReceipts[id]; ok {
			return mockExecResult(0), nil
		}
		db.externalSourceReceipts[id] = mockExternalSourceReceipt{
			sourceSnapshotID:      args[1].(string),
			extractionViewID:      args[2].(string),
			envelopeSchemaVersion: args[3].(string),
			collectorID:           args[4].(string),
			connectorID:           args[5].(string),
			observedAt:            args[6].(time.Time),
			receiptPayloadHash:    args[7].(string),
			createdAt:             time.Date(2026, 8, 23, 2, 4, 5, 0, time.UTC),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO extractor_definitions"):
		db.extractorDefinitions[args[0].(string)] = mockExtractorDefinition{
			name:       args[1].(string),
			version:    args[2].(string),
			configHash: args[3].(string),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO extraction_runs"):
		id := args[0].(string)
		if _, ok := db.extractionRuns[id]; ok {
			return mockExecResult(0), nil
		}
		for _, run := range db.extractionRuns {
			if run.requestID == args[4].(string) {
				return mockExecResult(0), nil
			}
		}
		db.extractionRuns[id] = mockExtractionRun{
			extractorDefinitionID: args[1].(string),
			sourceSnapshotID:      args[2].(string),
			extractionViewID:      args[3].(string),
			requestID:             args[4].(string),
			producerSessionRef:    args[5].(string),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO extraction_attempts"):
		id := args[0].(string)
		if _, ok := db.extractionAttempts[id]; !ok {
			db.extractionAttempts[id] = mockExtractionAttempt{
				runID:  args[1].(string),
				number: args[2].(int),
				status: attemptStatusStarted,
			}
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "UPDATE extraction_attempts") && strings.Contains(query, "status = 'succeeded'"):
		id := args[0].(string)
		attempt, ok := db.extractionAttempts[id]
		if !ok || attempt.status != attemptStatusStarted {
			return mockExecResult(0), nil
		}
		attempt.status = attemptStatusSucceeded
		attempt.outputHash = args[1].(string)
		attempt.fixtureOutput = args[2].(string)
		attempt.failureClass = ""
		db.extractionAttempts[id] = attempt
		return mockExecResult(1), nil
	case strings.Contains(query, "UPDATE extraction_attempts") && strings.Contains(query, "status = 'failed'"):
		id := args[0].(string)
		attempt, ok := db.extractionAttempts[id]
		if !ok || attempt.status != attemptStatusStarted {
			return mockExecResult(0), nil
		}
		attempt.status = attemptStatusFailed
		if args[1] != nil {
			attempt.outputHash = args[1].(string)
		}
		if args[2] != nil {
			attempt.fixtureOutput = args[2].(string)
		}
		attempt.failureClass = args[3].(string)
		db.extractionAttempts[id] = attempt
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO proposal_batches") && strings.Contains(query, "'completed'"):
		db.proposalBatches[args[0].(string)] = mockProposalBatch{
			attemptID:     args[1].(string),
			status:        batchStatusCompleted,
			proposalCount: args[2].(int),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO proposal_batches") && strings.Contains(query, "'failed'"):
		db.proposalBatches[args[0].(string)] = mockProposalBatch{
			attemptID:     args[1].(string),
			status:        batchStatusFailed,
			proposalCount: 0,
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO proposal_occurrences"):
		id := args[0].(string)
		if _, ok := db.proposalOccurrences[id]; ok {
			return mockExecResult(0), nil
		}
		var sourceRefs []ResolvedSourceRef
		if err := json.Unmarshal([]byte(args[8].(string)), &sourceRefs); err != nil {
			return nil, fmt.Errorf("decode source refs: %w", err)
		}
		var proposedPayload map[string]any
		if err := json.Unmarshal([]byte(args[9].(string)), &proposedPayload); err != nil {
			return nil, fmt.Errorf("decode proposed payload: %w", err)
		}
		db.proposalOccurrences[id] = ProposalOccurrence{
			ID:                         id,
			BatchID:                    args[1].(string),
			ExtractionAttemptID:        args[2].(string),
			ProposalLocalID:            args[3].(string),
			ProposalKind:               args[4].(string),
			StatementText:              args[5].(string),
			ProposalFingerprint:        args[6].(string),
			ProposalFingerprintVersion: args[7].(string),
			AdmissionOutcome:           admissionOutcomePending,
			SourceRefs:                 sourceRefs,
			ProposedPayload:            proposedPayload,
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO canonical_graph_nodes"):
		id := args[0].(string)
		if _, ok := db.canonicalGraphNodes[id]; ok {
			return mockExecResult(0), nil
		}
		var payload evidencegraph.EvidencePayload
		if err := json.Unmarshal([]byte(args[2].(string)), &payload); err != nil {
			return nil, fmt.Errorf("decode canonical payload: %w", err)
		}
		var provenance evidencegraph.ProvenanceRecord
		if err := json.Unmarshal([]byte(args[3].(string)), &provenance); err != nil {
			return nil, fmt.Errorf("decode canonical provenance: %w", err)
		}
		var temporal evidencegraph.TemporalRecord
		if err := json.Unmarshal([]byte(args[4].(string)), &temporal); err != nil {
			return nil, fmt.Errorf("decode canonical temporal: %w", err)
		}
		var integrity evidencegraph.IntegrityRecord
		if err := json.Unmarshal([]byte(args[5].(string)), &integrity); err != nil {
			return nil, fmt.Errorf("decode canonical integrity: %w", err)
		}
		db.canonicalGraphNodes[id] = CanonicalGraphNode{
			ID:                         id,
			Kind:                       evidencegraph.CanonicalNodeKind(args[1].(string)),
			Payload:                    payload,
			Provenance:                 provenance,
			Temporal:                   temporal,
			Integrity:                  integrity,
			OriginProposalOccurrenceID: args[6].(string),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO canonical_graph_edges"):
		id := args[0].(string)
		if _, ok := db.canonicalGraphEdges[id]; ok {
			return mockExecResult(0), nil
		}
		for _, edge := range db.canonicalGraphEdges {
			if edge.From == args[1].(string) && edge.To == args[2].(string) && string(edge.Relation) == args[3].(string) {
				return mockExecResult(0), nil
			}
		}
		var provenance evidencegraph.ProvenanceRecord
		if err := json.Unmarshal([]byte(args[4].(string)), &provenance); err != nil {
			return nil, fmt.Errorf("decode canonical edge provenance: %w", err)
		}
		db.canonicalGraphEdges[id] = CanonicalGraphEdge{
			ID:                         id,
			From:                       args[1].(string),
			To:                         args[2].(string),
			Relation:                   evidencegraph.CanonicalEdgeRelation(args[3].(string)),
			Provenance:                 provenance,
			OriginProposalOccurrenceID: args[5].(string),
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO canonical_derivation_parents"):
		derivationID := args[0].(string)
		parentID := args[1].(string)
		edgeID := args[2].(string)
		parents, ok := db.derivationParentEdges[derivationID]
		if !ok {
			parents = map[string]string{}
			db.derivationParentEdges[derivationID] = parents
		}
		if _, exists := parents[parentID]; exists {
			return nil, fmt.Errorf("duplicate derivation parent %s/%s", derivationID, parentID)
		}
		parents[parentID] = edgeID
		derivation := db.canonicalDerivations[derivationID]
		derivation.Parents = append(derivation.Parents, parentID)
		db.canonicalDerivations[derivationID] = derivation
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO canonical_derivations"):
		derivationID := args[0].(string)
		if _, exists := db.canonicalDerivations[derivationID]; exists {
			return nil, fmt.Errorf("duplicate canonical derivation %s", derivationID)
		}
		db.canonicalDerivations[derivationID] = evidencegraph.DerivationRecord{
			ID:            derivationID,
			NodeID:        args[1].(string),
			Method:        args[2].(string),
			Producer:      args[3].(string),
			TraceRef:      args[4].(string),
			ProvenanceRef: args[5].(string),
		}
		db.derivationOrigins[derivationID] = args[6].(string)
		return mockExecResult(1), nil
	case strings.Contains(query, "INSERT INTO admission_decisions"):
		id := args[0].(string)
		if _, ok := db.admissionDecisions[id]; ok {
			return nil, fmt.Errorf("duplicate admission decision %s", id)
		}
		var rawNodeIDs []string
		if err := json.Unmarshal([]byte(args[4].(string)), &rawNodeIDs); err != nil {
			return nil, fmt.Errorf("decode raw node IDs: %w", err)
		}
		var edgeIDs []string
		if err := json.Unmarshal([]byte(args[5].(string)), &edgeIDs); err != nil {
			return nil, fmt.Errorf("decode canonical edge IDs: %w", err)
		}
		var canonicalRef string
		if args[3] != nil {
			canonicalRef = args[3].(string)
		}
		var reviewContract string
		if args[8] != nil {
			reviewContract = args[8].(string)
		}
		db.admissionDecisions[id] = mockAdmissionDecision{
			proposalOccurrence: args[1].(string),
			outcome:            args[2].(string),
			canonicalRef:       canonicalRef,
			rawEvidenceNodeIDs: rawNodeIDs,
			canonicalEdgeIDs:   edgeIDs,
			decisionBy:         args[6].(string),
			decisionReason:     args[7].(string),
			reviewContract:     reviewContract,
		}
		return mockExecResult(1), nil
	case strings.Contains(query, "UPDATE proposal_occurrences") && strings.Contains(query, "admission_outcome = $2"):
		id := args[0].(string)
		occurrence, ok := db.proposalOccurrences[id]
		if !ok || occurrence.AdmissionOutcome != admissionOutcomePending {
			return mockExecResult(0), nil
		}
		occurrence.AdmissionOutcome = args[1].(string)
		occurrence.CanonicalRef = ""
		db.proposalOccurrences[id] = occurrence
		return mockExecResult(1), nil
	case strings.Contains(query, "UPDATE proposal_occurrences") && strings.Contains(query, "admission_outcome = 'admitted'"):
		id := args[0].(string)
		occurrence, ok := db.proposalOccurrences[id]
		if !ok || occurrence.AdmissionOutcome != admissionOutcomePending {
			return mockExecResult(0), nil
		}
		occurrence.AdmissionOutcome = admissionOutcomeAdmitted
		occurrence.CanonicalRef = args[1].(string)
		db.proposalOccurrences[id] = occurrence
		return mockExecResult(1), nil
	default:
		return nil, fmt.Errorf("unsupported mock exec query: %s", compactSQL(query))
	}
}

func mockQuery(_ context.Context, db *mockSQLDB, query string, args ...any) (sqlRows, error) {
	switch {
	case strings.Contains(query, "FROM canonical_graph_edges") && strings.Contains(query, "WHERE canonical_edge_id = $1"):
		edgeID := args[0].(string)
		from := args[1].(string)
		to := args[2].(string)
		relation := args[3].(string)
		var edges []CanonicalGraphEdge
		for _, edge := range db.canonicalGraphEdges {
			if edge.ID == edgeID ||
				(edge.From == from && edge.To == to && string(edge.Relation) == relation) {
				edges = append(edges, edge)
			}
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
		rows := make([]mockRow, 0, len(edges))
		for _, edge := range edges {
			provenance, err := jsonBytes(edge.Provenance)
			if err != nil {
				return nil, err
			}
			rows = append(rows, mockRow{values: []any{
				edge.ID,
				edge.From,
				edge.To,
				string(edge.Relation),
				provenance,
				edge.OriginProposalOccurrenceID,
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM canonical_ordinary_admission_node_bindings"):
		bindings := append([]canonicalAdmissionNodeBinding(nil), db.ordinaryNodeBindings[args[0].(string)]...)
		sort.Slice(bindings, func(i, j int) bool {
			if bindings[i].Role != bindings[j].Role {
				return bindings[i].Role == canonicalAdmissionRoleCanonicalRef
			}
			return bindings[i].Position < bindings[j].Position
		})
		rows := make([]mockRow, 0, len(bindings))
		for _, binding := range bindings {
			rows = append(rows, mockRow{values: []any{
				binding.Position,
				binding.Role,
				binding.NodeID,
				string(binding.Materialization),
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM canonical_ordinary_admission_edge_bindings"):
		bindings := append([]canonicalAdmissionEdgeBinding(nil), db.ordinaryEdgeBindings[args[0].(string)]...)
		sort.Slice(bindings, func(i, j int) bool { return bindings[i].Position < bindings[j].Position })
		rows := make([]mockRow, 0, len(bindings))
		for _, binding := range bindings {
			rows = append(rows, mockRow{values: []any{
				binding.Position,
				binding.Role,
				binding.EdgeID,
				string(binding.Materialization),
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "SELECT canonical_node_id") &&
		strings.Contains(query, "WHERE origin_proposal_occurrence_id = $1"):
		proposalID := args[0].(string)
		var ids []string
		for _, node := range db.canonicalGraphNodes {
			if node.OriginProposalOccurrenceID == proposalID {
				ids = append(ids, node.ID)
			}
		}
		sort.Strings(ids)
		rows := make([]mockRow, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, mockRow{values: []any{id}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "SELECT canonical_edge_id") &&
		strings.Contains(query, "WHERE origin_proposal_occurrence_id = $1"):
		proposalID := args[0].(string)
		var ids []string
		for _, edge := range db.canonicalGraphEdges {
			if edge.OriginProposalOccurrenceID == proposalID {
				ids = append(ids, edge.ID)
			}
		}
		sort.Strings(ids)
		rows := make([]mockRow, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, mockRow{values: []any{id}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM canonical_derivations d"):
		selectedValues := args[0].([]string)
		selected := make(map[string]struct{}, len(selectedValues))
		for _, nodeID := range selectedValues {
			selected[nodeID] = struct{}{}
		}
		var derivations []evidencegraph.DerivationRecord
		for _, derivation := range db.canonicalDerivations {
			if _, exists := selected[derivation.NodeID]; exists {
				derivations = append(derivations, derivation)
			}
		}
		sort.Slice(derivations, func(i, j int) bool { return derivations[i].NodeID < derivations[j].NodeID })
		var result []mockRow
		for _, derivation := range derivations {
			parents := append([]string(nil), derivation.Parents...)
			sort.Strings(parents)
			for _, parentID := range parents {
				result = append(result, mockRow{values: []any{
					derivation.ID,
					derivation.NodeID,
					derivation.Method,
					derivation.Producer,
					derivation.TraceRef,
					derivation.ProvenanceRef,
					parentID,
					db.derivationParentEdges[derivation.ID][parentID],
				}})
			}
		}
		return &mockRows{rows: result, index: -1}, nil
	case strings.Contains(query, "FROM canonical_derivation_parents"):
		derivationID := args[0].(string)
		derivation, exists := db.canonicalDerivations[derivationID]
		if !exists {
			return &mockRows{index: -1}, nil
		}
		parents := append([]string(nil), derivation.Parents...)
		sort.Strings(parents)
		rows := make([]mockRow, 0, len(parents))
		for _, parentID := range parents {
			rows = append(rows, mockRow{values: []any{parentID, db.derivationParentEdges[derivationID][parentID]}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM canonical_graph_edges") && strings.Contains(query, "ANY($1::text[])"):
		frontierValues := args[0].([]string)
		relationValues := args[1].([]string)
		seenValues := args[2].([]string)
		limit := args[3].(int)
		frontier := make(map[string]struct{}, len(frontierValues))
		for _, id := range frontierValues {
			frontier[id] = struct{}{}
		}
		relations := make(map[string]struct{}, len(relationValues))
		for _, relation := range relationValues {
			relations[relation] = struct{}{}
		}
		seen := make(map[string]struct{}, len(seenValues))
		for _, id := range seenValues {
			seen[id] = struct{}{}
		}
		edges := make([]CanonicalGraphEdge, 0, len(db.canonicalGraphEdges))
		for _, edge := range db.canonicalGraphEdges {
			if _, fromSelected := frontier[edge.From]; !fromSelected {
				if _, toSelected := frontier[edge.To]; !toSelected {
					continue
				}
			}
			if len(relations) > 0 {
				if _, selected := relations[string(edge.Relation)]; !selected {
					continue
				}
			}
			if _, alreadySeen := seen[edge.ID]; alreadySeen {
				continue
			}
			edges = append(edges, edge)
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
		if limit < len(edges) {
			edges = edges[:limit]
		}
		rows := make([]mockRow, 0, len(edges))
		for _, edge := range edges {
			provenance, err := jsonBytes(edge.Provenance)
			if err != nil {
				return nil, err
			}
			rows = append(rows, mockRow{values: []any{
				edge.ID,
				edge.From,
				edge.To,
				string(edge.Relation),
				provenance,
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "SELECT canonical_node_id") && !strings.Contains(query, "node_kind") && strings.Contains(query, "FROM canonical_graph_nodes") && strings.Contains(query, "ANY($1::text[])"):
		selectedValues := args[0].([]string)
		rows := make([]mockRow, 0, len(selectedValues))
		for _, nodeID := range selectedValues {
			if _, exists := db.canonicalGraphNodes[nodeID]; exists {
				rows = append(rows, mockRow{values: []any{nodeID}})
			}
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM canonical_graph_nodes") && strings.Contains(query, "ANY($1::text[])"):
		selectedValues := args[0].([]string)
		selected := make(map[string]struct{}, len(selectedValues))
		for _, id := range selectedValues {
			selected[id] = struct{}{}
		}
		nodes := make([]CanonicalGraphNode, 0, len(selected))
		for id, node := range db.canonicalGraphNodes {
			if _, exists := selected[id]; exists {
				nodes = append(nodes, node)
			}
		}
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
		rows := make([]mockRow, 0, len(nodes))
		for _, node := range nodes {
			payload, err := jsonBytes(node.Payload)
			if err != nil {
				return nil, err
			}
			provenance, err := jsonBytes(node.Provenance)
			if err != nil {
				return nil, err
			}
			temporal, err := jsonBytes(node.Temporal)
			if err != nil {
				return nil, err
			}
			integrity, err := jsonBytes(node.Integrity)
			if err != nil {
				return nil, err
			}
			rows = append(rows, mockRow{values: []any{
				node.ID,
				string(node.Kind),
				payload,
				provenance,
				temporal,
				integrity,
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM proposal_occurrences po") && strings.Contains(query, "LIMIT $8"):
		sourceSnapshotID := args[0].(string)
		repositorySnapshotID := args[1].(string)
		sourceID := args[2].(string)
		sourceVersion := args[3].(string)
		admissionOutcome := args[4].(string)
		sourceGenerationID := args[5].(string)
		lifecycleScope := args[6].(string)
		limit := args[7].(int)
		occurrences := make([]ProposalOccurrence, 0, len(db.proposalOccurrences))
		for _, occurrence := range db.proposalOccurrences {
			attempt := db.extractionAttempts[occurrence.ExtractionAttemptID]
			run := db.extractionRuns[attempt.runID]
			snapshot := db.sourceSnapshots[run.sourceSnapshotID]
			if sourceSnapshotID != "" && run.sourceSnapshotID != sourceSnapshotID {
				continue
			}
			if repositorySnapshotID != "" {
				continue
			}
			if sourceGenerationID != "" {
				continue
			}
			if lifecycleScope == ProposalLifecycleScopeHistorical {
				continue
			}
			if sourceID != "" && snapshot.sourceID != sourceID {
				continue
			}
			if sourceVersion != "" && snapshot.sourceVersion != sourceVersion {
				continue
			}
			if admissionOutcome != "" && occurrence.AdmissionOutcome != admissionOutcome {
				continue
			}
			occurrences = append(occurrences, occurrence)
		}
		sort.Slice(occurrences, func(i, j int) bool {
			return occurrences[i].ID < occurrences[j].ID
		})
		if limit < len(occurrences) {
			occurrences = occurrences[:limit]
		}
		rows := make([]mockRow, 0, len(occurrences))
		for _, occurrence := range occurrences {
			values, err := mockProposalQueryValues(db, occurrence)
			if err != nil {
				return nil, err
			}
			rows = append(rows, mockRow{values: values})
		}
		return &mockRows{rows: rows, index: -1}, nil
	default:
		return nil, fmt.Errorf("unsupported mock query: %s", compactSQL(query))
	}
}

func mockQueryRow(_ context.Context, db *mockSQLDB, query string, args ...any) sqlRow {
	switch {
	case strings.Contains(query, "canonical-node-exact-semantic-row"):
		node, exists := db.canonicalGraphNodes[args[0].(string)]
		if !exists {
			return mockRow{err: pgx.ErrNoRows}
		}
		payload, payloadErr := jsonBytes(node.Payload)
		provenance, provenanceErr := jsonBytes(node.Provenance)
		temporal, temporalErr := jsonBytes(node.Temporal)
		integrity, integrityErr := jsonBytes(node.Integrity)
		for _, err := range []error{payloadErr, provenanceErr, temporalErr, integrityErr} {
			if err != nil {
				return mockRow{err: err}
			}
		}
		matches := string(node.Kind) == args[1].(string) &&
			mockJSONEqual(payload, []byte(args[2].(string))) &&
			mockJSONEqual(provenance, []byte(args[3].(string))) &&
			mockJSONEqual(temporal, []byte(args[4].(string))) &&
			mockJSONEqual(integrity, []byte(args[5].(string)))
		return mockRow{values: []any{matches, node.OriginProposalOccurrenceID}}
	case strings.Contains(query, "canonical-edge-exact-semantic-row"):
		edge, exists := db.canonicalGraphEdges[args[0].(string)]
		if !exists {
			return mockRow{err: pgx.ErrNoRows}
		}
		provenance, err := jsonBytes(edge.Provenance)
		if err != nil {
			return mockRow{err: err}
		}
		matches := edge.From == args[1].(string) &&
			edge.To == args[2].(string) &&
			string(edge.Relation) == args[3].(string) &&
			edge.OriginContradictionProposalID == "" &&
			mockJSONEqual(provenance, []byte(args[4].(string)))
		return mockRow{values: []any{matches, edge.OriginProposalOccurrenceID}}
	case strings.Contains(query, "canonical-node-first-materializer-authority"):
		nodeID := args[0].(string)
		originProposalID := args[1].(string)
		var count int64
		for decisionID, manifest := range db.ordinaryManifests {
			decision, decisionExists := db.admissionDecisions[decisionID]
			proposal, proposalExists := db.proposalOccurrences[manifest.proposalID]
			if !decisionExists || !proposalExists ||
				manifest.proposalID != originProposalID ||
				decision.proposalOccurrence != manifest.proposalID ||
				decision.outcome != admissionOutcomeAdmitted ||
				proposal.AdmissionOutcome != decision.outcome ||
				proposal.CanonicalRef != decision.canonicalRef {
				continue
			}
			for _, binding := range db.ordinaryNodeBindings[decisionID] {
				if binding.NodeID == nodeID && binding.Materialization == canonicalRowMaterialized {
					count++
				}
			}
		}
		return mockRow{values: []any{count}}
	case strings.Contains(query, "canonical-edge-first-materializer-authority"):
		edgeID := args[0].(string)
		originProposalID := args[1].(string)
		var count int64
		for decisionID, manifest := range db.ordinaryManifests {
			decision, decisionExists := db.admissionDecisions[decisionID]
			proposal, proposalExists := db.proposalOccurrences[manifest.proposalID]
			if !decisionExists || !proposalExists ||
				manifest.proposalID != originProposalID ||
				decision.proposalOccurrence != manifest.proposalID ||
				decision.outcome != admissionOutcomeAdmitted ||
				proposal.AdmissionOutcome != decision.outcome ||
				proposal.CanonicalRef != decision.canonicalRef {
				continue
			}
			for _, binding := range db.ordinaryEdgeBindings[decisionID] {
				if binding.EdgeID == edgeID && binding.Materialization == canonicalRowMaterialized {
					count++
				}
			}
		}
		return mockRow{values: []any{count}}
	case strings.Contains(query, "FROM canonical_ordinary_admission_manifests"):
		manifest, exists := db.ordinaryManifests[args[0].(string)]
		if !exists {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{
			manifest.contractVersion,
			manifest.mutationKind,
			manifest.proposalID,
			manifest.canonicalRef,
			manifest.outcome,
		}}
	case strings.Contains(query, "SELECT proposal_occurrence_id") && strings.Contains(query, "FROM proposal_occurrences") && strings.Contains(query, "FOR UPDATE"):
		id := args[0].(string)
		if _, ok := db.proposalOccurrences[id]; !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{id}}
	case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "canonical_source_claim_review_bindings"):
		return mockRow{values: []any{false}}
	case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "source_claim_disposition_review_bindings"):
		return mockRow{values: []any{false}}
	case strings.Contains(query, "FROM extraction_runs") && strings.Contains(query, "WHERE request_id = $1"):
		for id, run := range db.extractionRuns {
			if run.requestID == args[0].(string) {
				return mockRow{values: []any{id, run.requestID, run.extractorDefinitionID, run.sourceSnapshotID, run.extractionViewID}}
			}
		}
		return mockRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "canonical_supersession_admission_events"):
		_, found := db.supersessionEvents[args[0].(string)]
		return mockRow{values: []any{found}}
	case strings.Contains(query, "WITH RECURSIVE descendants"):
		return mockRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "FROM canonical_derivations") && strings.Contains(query, "WHERE node_id = $1"):
		nodeID := args[0].(string)
		for _, derivation := range db.canonicalDerivations {
			if derivation.NodeID == nodeID {
				return mockRow{values: []any{derivation.ID, derivation.NodeID, derivation.Method, derivation.Producer, derivation.TraceRef, derivation.ProvenanceRef, db.derivationOrigins[derivation.ID]}}
			}
		}
		return mockRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "SELECT COALESCE(MAX(attempt_number), 0)"):
		runID := args[0].(string)
		maxAttempt := 0
		for _, attempt := range db.extractionAttempts {
			if attempt.runID == runID && attempt.number > maxAttempt {
				maxAttempt = attempt.number
			}
		}
		return mockRow{values: []any{maxAttempt}}
	case strings.Contains(query, "SELECT attempt_number") && strings.Contains(query, "status = 'succeeded'"):
		runID := args[0].(string)
		best := 0
		for _, attempt := range db.extractionAttempts {
			if attempt.runID == runID && attempt.status == attemptStatusSucceeded && (best == 0 || attempt.number < best) {
				best = attempt.number
			}
		}
		if best == 0 {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{best}}
	case strings.Contains(query, "SELECT status, COALESCE(output_hash, '')") && strings.Contains(query, "FROM extraction_attempts"):
		attempt, ok := db.extractionAttempts[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{attempt.status, attempt.outputHash}}
	case strings.Contains(query, "SELECT status") && strings.Contains(query, "FROM extraction_attempts"):
		attempt, ok := db.extractionAttempts[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{attempt.status}}
	case strings.Contains(query, "LEFT JOIN LATERAL") && strings.Contains(query, "FROM extraction_attempts ea"):
		attemptID := args[0].(string)
		attempt, ok := db.extractionAttempts[attemptID]
		if !ok || attempt.status != attemptStatusSucceeded {
			return mockRow{err: pgx.ErrNoRows}
		}
		proposalCount := -1
		for _, batch := range db.proposalBatches {
			if batch.attemptID == attemptID && batch.status == batchStatusCompleted {
				proposalCount = batch.proposalCount
				break
			}
		}
		if proposalCount < 0 {
			return mockRow{err: pgx.ErrNoRows}
		}
		occurrences := make([]ProposalOccurrence, 0, len(db.proposalOccurrences))
		for _, occurrence := range db.proposalOccurrences {
			if occurrence.ExtractionAttemptID == attemptID {
				occurrences = append(occurrences, occurrence)
			}
		}
		if len(occurrences) == 0 {
			return mockRow{values: []any{"", "", attempt.outputHash, proposalCount}}
		}
		sort.Slice(occurrences, func(i, j int) bool {
			return occurrences[i].ProposalLocalID < occurrences[j].ProposalLocalID
		})
		first := occurrences[0]
		return mockRow{values: []any{first.ID, first.ProposalFingerprint, attempt.outputHash, proposalCount}}
	case strings.Contains(query, "SELECT proposal_fingerprint, statement_text"):
		occurrence, ok := db.proposalOccurrences[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{occurrence.ProposalFingerprint, occurrence.StatementText}}
	case strings.Contains(query, "SELECT created_at") && strings.Contains(query, "FROM external_source_intake_receipts"):
		receipt, ok := db.externalSourceReceipts[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{receipt.createdAt}}
	case strings.Contains(query, "FROM external_source_intake_receipts"):
		receipt, ok := db.externalSourceReceipts[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{
			receipt.sourceSnapshotID,
			receipt.extractionViewID,
			receipt.envelopeSchemaVersion,
			receipt.collectorID,
			receipt.connectorID,
			receipt.observedAt,
			receipt.receiptPayloadHash,
		}}
	case strings.Contains(query, "SELECT created_at") && strings.Contains(query, "FROM source_intake_requests"):
		request, ok := db.sourceIntakeRequests[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{request.createdAt}}
	case strings.Contains(query, "FROM source_intake_requests"):
		request, ok := db.sourceIntakeRequests[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{request.sourceSnapshotID, request.extractionViewID, request.requestPayloadHash}}
	case strings.Contains(query, "SELECT source_system, source_id, source_version, raw_content_hash, origin_metadata") &&
		strings.Contains(query, "FROM source_snapshots"):
		snapshot, ok := db.sourceSnapshots[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockRow{values: []any{
			snapshot.sourceSystem,
			snapshot.sourceID,
			snapshot.sourceVersion,
			snapshot.rawContentHash,
			snapshot.originMetadata,
		}}
	case strings.Contains(query, "AS rendered_byte_length") &&
		strings.Contains(query, "AS max_span_byte_length"):
		db.boundedViewPreflights++
		snapshotID := args[0].(string)
		viewID := args[1].(string)
		snapshot, snapshotOK := db.sourceSnapshots[snapshotID]
		view, viewOK := db.extractionViews[viewID]
		if !snapshotOK || !viewOK || view.sourceSnapshotID != snapshotID {
			return mockRow{err: pgx.ErrNoRows}
		}
		var spanCount int64
		var maxSpanBytes int64
		for _, span := range db.spanEntries {
			if span.ExtractionViewID != viewID {
				continue
			}
			spanCount++
			spanBytes := int64(span.EndByte - span.StartByte)
			if quotedBytes := int64(len([]byte(span.QuotedText))); quotedBytes > spanBytes {
				spanBytes = quotedBytes
			}
			if spanBytes > maxSpanBytes {
				maxSpanBytes = spanBytes
			}
		}
		return mockRow{values: []any{
			snapshotID,
			snapshot.sourceSystem,
			snapshot.rawContentHash,
			viewID,
			view.sourceSnapshotID,
			view.rendererName,
			view.rendererVersion,
			view.renderedContentHash,
			int64(len(view.rendered)),
			spanCount,
			maxSpanBytes,
		}}
	case strings.Contains(query, "FROM extraction_views ev") && strings.Contains(query, "WHERE ev.extraction_view_id = $1"):
		db.sourceViewLoads++
		viewID := args[0].(string)
		view, ok := db.extractionViews[viewID]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		return mockSourceViewRow(db, view.sourceSnapshotID, viewID)
	case strings.Contains(query, "FROM source_snapshots ss") && strings.Contains(query, "JOIN extraction_views ev"):
		db.sourceViewLoads++
		snapshotID := args[0].(string)
		viewID := args[1].(string)
		return mockSourceViewRow(db, snapshotID, viewID)
	case strings.Contains(query, "FROM canonical_graph_nodes"):
		node, ok := db.canonicalGraphNodes[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		payload, err := jsonBytes(node.Payload)
		if err != nil {
			return mockRow{err: err}
		}
		provenance, err := jsonBytes(node.Provenance)
		if err != nil {
			return mockRow{err: err}
		}
		temporal, err := jsonBytes(node.Temporal)
		if err != nil {
			return mockRow{err: err}
		}
		integrity, err := jsonBytes(node.Integrity)
		if err != nil {
			return mockRow{err: err}
		}
		return mockRow{values: []any{
			node.ID,
			string(node.Kind),
			payload,
			provenance,
			temporal,
			integrity,
			node.OriginProposalOccurrenceID,
		}}
	case strings.Contains(query, "FROM proposal_occurrences po"):
		occurrence, ok := db.proposalOccurrences[args[0].(string)]
		if !ok {
			return mockRow{err: pgx.ErrNoRows}
		}
		values, err := mockProposalQueryValues(db, occurrence)
		if err != nil {
			return mockRow{err: err}
		}
		return mockRow{values: values}
	case strings.Contains(query, "FROM admission_decisions"):
		for id, decision := range db.admissionDecisions {
			if decision.proposalOccurrence != args[0].(string) {
				continue
			}
			rawNodeIDs, err := jsonBytes(decision.rawEvidenceNodeIDs)
			if err != nil {
				return mockRow{err: err}
			}
			edgeIDs, err := jsonBytes(decision.canonicalEdgeIDs)
			if err != nil {
				return mockRow{err: err}
			}
			var canonicalRef any
			if decision.canonicalRef != "" {
				canonicalRef = decision.canonicalRef
			}
			return mockRow{values: []any{
				id,
				decision.outcome,
				canonicalRef,
				rawNodeIDs,
				edgeIDs,
				decision.decisionBy,
				decision.decisionReason,
				decision.reviewContract,
			}}
		}
		return mockRow{err: pgx.ErrNoRows}
	default:
		return mockRow{err: fmt.Errorf("unsupported mock query row: %s", compactSQL(query))}
	}
}

func mockJSONEqual(first, second []byte) bool {
	var firstValue, secondValue any
	if err := json.Unmarshal(first, &firstValue); err != nil {
		return false
	}
	if err := json.Unmarshal(second, &secondValue); err != nil {
		return false
	}
	return reflect.DeepEqual(firstValue, secondValue)
}

func mockProposalQueryValues(db *mockSQLDB, occurrence ProposalOccurrence) ([]any, error) {
	sourceRefs, err := jsonBytes(occurrence.SourceRefs)
	if err != nil {
		return nil, err
	}
	proposedPayload, err := jsonBytes(occurrence.ProposedPayload)
	if err != nil {
		return nil, err
	}
	attempt := db.extractionAttempts[occurrence.ExtractionAttemptID]
	run := db.extractionRuns[attempt.runID]
	definition := db.extractorDefinitions[run.extractorDefinitionID]
	view := db.extractionViews[run.extractionViewID]
	snapshot := db.sourceSnapshots[run.sourceSnapshotID]
	var canonicalRef any
	if occurrence.CanonicalRef != "" {
		canonicalRef = occurrence.CanonicalRef
	}
	return []any{
		occurrence.ID,
		occurrence.ProposalFingerprint,
		occurrence.ProposalFingerprintVersion,
		occurrence.ProposalKind,
		occurrence.StatementText,
		occurrence.AdmissionOutcome,
		canonicalRef,
		sourceRefs,
		proposedPayload,
		occurrence.ExtractionAttemptID,
		attempt.status,
		attempt.runID,
		run.producerSessionRef,
		run.extractorDefinitionID,
		definition.name,
		definition.version,
		definition.configHash,
		run.extractionViewID,
		view.rendererName,
		view.rendererVersion,
		view.renderedContentHash,
		run.sourceSnapshotID,
		snapshot.sourceSystem,
		snapshot.sourceID,
		snapshot.sourceVersion,
		snapshot.rawContentHash,
		snapshot.originMetadata,
		"",
		"",
		"",
		"",
		0,
		"",
		"",
		"",
		0,
		"",
		"",
		"",
		"",
		0,
		"",
		"",
		"",
		"",
		0,
		false,
		"",
		"",
		"",
		"",
		"",
		"",
	}, nil
}

func mockSourceViewRow(db *mockSQLDB, snapshotID, viewID string) mockRow {
	snapshot, ok := db.sourceSnapshots[snapshotID]
	if !ok {
		return mockRow{err: pgx.ErrNoRows}
	}
	view, ok := db.extractionViews[viewID]
	if !ok || view.sourceSnapshotID != snapshotID {
		return mockRow{err: pgx.ErrNoRows}
	}
	spans := make([]SpanEntry, 0, len(db.spanEntries))
	for _, span := range db.spanEntries {
		if span.ExtractionViewID == viewID {
			spans = append(spans, span)
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].StartByte == spans[j].StartByte {
			return spans[i].SpanID < spans[j].SpanID
		}
		return spans[i].StartByte < spans[j].StartByte
	})
	spansData, err := jsonBytes(spans)
	if err != nil {
		return mockRow{err: err}
	}
	return mockRow{values: []any{
		snapshotID,
		snapshot.sourceSystem,
		snapshot.sourceID,
		snapshot.sourceVersion,
		snapshot.rawContentHash,
		snapshot.originMetadata,
		viewID,
		view.sourceSnapshotID,
		view.rendererName,
		view.rendererVersion,
		view.rendered,
		view.renderedContentHash,
		spansData,
	}}
}

type mockRow struct {
	values []any
	err    error
}

func (r mockRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(r.values))
	}
	for i, value := range r.values {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = value.(string)
		case *int:
			*ptr = value.(int)
		case *int64:
			*ptr = value.(int64)
		case *bool:
			*ptr = value.(bool)
		case *time.Time:
			*ptr = value.(time.Time)
		case *[]byte:
			switch v := value.(type) {
			case []byte:
				*ptr = append((*ptr)[:0], v...)
			case string:
				*ptr = []byte(v)
			default:
				return fmt.Errorf("scan value %d has type %T, want []byte or string", i, value)
			}
		case *sql.NullString:
			if value == nil {
				*ptr = sql.NullString{}
				continue
			}
			*ptr = sql.NullString{String: value.(string), Valid: true}
		default:
			return fmt.Errorf("unsupported scan destination %d: %T", i, dest[i])
		}
	}
	return nil
}

type mockRows struct {
	rows  []mockRow
	index int
	err   error
}

func (r *mockRows) Close() {}

func (r *mockRows) Err() error {
	return r.err
}

func (r *mockRows) Next() bool {
	if r.err != nil || r.index+1 >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

func (r *mockRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.rows) {
		return fmt.Errorf("mock rows scan before next")
	}
	return r.rows[r.index].Scan(dest...)
}

func assertExtractorInputSpansOrdered(t *testing.T, spans []ExtractorInputSpan) {
	t.Helper()
	for i := 1; i < len(spans); i++ {
		if spans[i].StartByte < spans[i-1].StartByte {
			t.Fatalf("span %d starts at %d before previous start %d: %+v", i, spans[i].StartByte, spans[i-1].StartByte, spans)
		}
	}
}

func compactSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}
