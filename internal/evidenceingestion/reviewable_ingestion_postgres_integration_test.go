//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationLoadReviewableSourceClaimReviewSnapshot(t *testing.T) {
	ctx, pool := integrationPool(t)
	first := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "first", "revision-1")

	before := reviewableIngestionRowCounts(t, ctx, pool)
	snapshot, err := LoadReviewableSourceClaimReviewSnapshot(
		ctx,
		pool,
		first.ExtractionAttemptID,
		first.ProposalOccurrenceID,
	)
	if err != nil {
		t.Fatalf("LoadReviewableSourceClaimReviewSnapshot() error = %v", err)
	}
	after := reviewableIngestionRowCounts(t, ctx, pool)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("row counts after snapshot load = %+v, want unchanged %+v", after, before)
	}

	if got, want := snapshot.SubmissionReceipt.ExtractionAttemptID, first.ExtractionAttemptID; got != want {
		t.Fatalf("receipt attempt ID = %q, want %q", got, want)
	}
	if receipt := snapshot.SubmissionReceipt; receipt.ContractVersion != ReviewableIngestionContractV1 ||
		receipt.RequestID != "reviewable-extractor-first" ||
		receipt.SourceSnapshotID != first.SourceSnapshotID ||
		receipt.ExtractionViewID != first.ExtractionViewID ||
		receipt.ExtractorDefinitionID == "" ||
		receipt.ExtractionRunID != first.ExtractionRunID ||
		receipt.ExtractionAttemptNumber != 1 ||
		receipt.ProposalCount != 2 {
		t.Fatalf("submission receipt = %+v, want complete persisted ingestion authority", receipt)
	}
	if got, want := snapshot.SubmissionReceipt.ProducerSessionRef, "session:reviewable-integration-first"; got != want {
		t.Fatalf("receipt producer session = %q, want %q", got, want)
	}
	if manifest := snapshot.ProposalManifest; manifest.ContractVersion != ReviewableIngestionContractV1 ||
		manifest.ExtractionAttemptID != first.ExtractionAttemptID ||
		manifest.ProposalBatchID != snapshot.SubmissionReceipt.ProposalBatchID ||
		manifest.ExtractorOutputHash != snapshot.SubmissionReceipt.ExtractorOutputHash {
		t.Fatalf("proposal manifest = %+v, want receipt-bound persisted batch", manifest)
	}
	if got, want := snapshot.ProposalManifest.ProposalCount, 2; got != want {
		t.Fatalf("manifest proposal count = %d, want %d", got, want)
	}
	if got, want := len(snapshot.ProposalManifest.Entries), 2; got != want {
		t.Fatalf("manifest entry count = %d, want %d", got, want)
	}
	foundReviewedOccurrence := false
	for index, entry := range snapshot.ProposalManifest.Entries {
		if entry.Ordinal != index+1 || entry.ProposalOccurrenceID == "" || entry.ProposalLocalID == "" ||
			entry.ProposalFingerprint == "" || entry.ProposalFingerprintVersion != ProposalFingerprintStatementV1 ||
			entry.ProposalKind != ProposalKindStatement {
			t.Fatalf("manifest entry %d = %+v, want complete canonical statement entry", index, entry)
		}
		if entry.ProposalOccurrenceID == first.ProposalOccurrenceID {
			foundReviewedOccurrence = true
		}
	}
	if !foundReviewedOccurrence {
		t.Fatalf("manifest entries = %+v, want reviewed occurrence %q", snapshot.ProposalManifest.Entries, first.ProposalOccurrenceID)
	}
	if got, want := snapshot.SubmissionReceipt.ProposalManifestID, snapshot.ProposalManifest.ID; got != want {
		t.Fatalf("receipt manifest ID = %q, want %q", got, want)
	}
	if snapshot.SubmissionReceipt.ID == "" || snapshot.ProposalManifest.ID == "" ||
		snapshot.SubmissionReceipt.ExtractorOutputHash == "" || snapshot.ProposalManifest.ProposalSetHash == "" {
		t.Fatalf("receipt/manifest omitted immutable identities: receipt=%+v manifest=%+v", snapshot.SubmissionReceipt, snapshot.ProposalManifest)
	}
	if got, want := snapshot.ReviewPackage.ProposalBasis.ProposalOccurrenceID, first.ProposalOccurrenceID; got != want {
		t.Fatalf("review occurrence ID = %q, want %q", got, want)
	}
	if review := snapshot.ReviewPackage; review.ContractVersion != ReviewableIngestionContractV1 ||
		review.ReviewTemplateID != SourceClaimReviewTemplateV1 ||
		review.SubmissionReceiptID != snapshot.SubmissionReceipt.ID ||
		review.ProposedEffect != SourceClaimProposedEffectV1 ||
		review.Coverage == "" || len(review.Limitations) == 0 ||
		review.ProposalBasis.ID == "" {
		t.Fatalf("review package = %+v, want complete receipt-bound review material", review)
	}
	if got, want := snapshot.ReviewPackage.ProposalBasis.SourceCoverage, ExternalSourceCoverageTruncatedDocument; got != want {
		t.Fatalf("source coverage = %q, want %q", got, want)
	}
	wantLimitations := []string{"Only the first two pages were returned by the connector."}
	if got := snapshot.ReviewPackage.ProposalBasis.SourceLimitations; !reflect.DeepEqual(got, wantLimitations) {
		t.Fatalf("source limitations = %#v, want %#v", got, wantLimitations)
	}
	if got, want := snapshot.ReviewPackage.ProposalBasis.SourceTitle, "Reviewable ingestion integration fixture"; got != want {
		t.Fatalf("source title = %q, want %q", got, want)
	}
	if got, want := snapshot.ReviewPackage.ProposalBasis.SourceLocation, "https://example.invalid/issues/first"; got != want {
		t.Fatalf("source location = %q, want %q", got, want)
	}
	if got := snapshot.ReviewPackage.ProposalBasis.SourceRefs; len(got) != 1 || got[0].QuotedText != "External connector owns collection." {
		t.Fatalf("review source refs = %+v, want exact connector statement", got)
	}
	subject := snapshot.ExactReviewSubject
	if subject.SubmissionReceiptID == "" || subject.SubmissionReceiptID != snapshot.SubmissionReceipt.ID ||
		subject.ProposalManifestID != snapshot.ProposalManifest.ID ||
		subject.ProposalOccurrenceID != snapshot.ReviewPackage.ProposalBasis.ProposalOccurrenceID ||
		subject.ProposalBasisID != snapshot.ReviewPackage.ProposalBasis.ID ||
		subject.ReviewPackageID != snapshot.ReviewPackage.ID {
		t.Fatalf("exact review subject = %+v, want receipt/manifest/occurrence/basis/package IDs from snapshot", subject)
	}

	reloaded, err := LoadReviewableSourceClaimReviewSnapshot(
		ctx,
		pool,
		first.ExtractionAttemptID,
		first.ProposalOccurrenceID,
	)
	if err != nil {
		t.Fatalf("second LoadReviewableSourceClaimReviewSnapshot() error = %v", err)
	}
	if !reflect.DeepEqual(reloaded, snapshot) {
		t.Fatalf("second snapshot = %+v, want stable %+v", reloaded, snapshot)
	}

	second := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "second", "revision-2")
	beforeMismatch := reviewableIngestionRowCounts(t, ctx, pool)
	_, err = LoadReviewableSourceClaimReviewSnapshot(
		ctx,
		pool,
		first.ExtractionAttemptID,
		second.ProposalOccurrenceID,
	)
	assertKind(t, err, ErrorReviewContractConflict)
	afterMismatch := reviewableIngestionRowCounts(t, ctx, pool)
	if !reflect.DeepEqual(afterMismatch, beforeMismatch) {
		t.Fatalf("row counts after mismatched occurrence = %+v, want unchanged %+v", afterMismatch, beforeMismatch)
	}
	secondProposalBatchID, err := sourceReviewBatchID(second.ExtractionAttemptID)
	if err != nil {
		t.Fatalf("sourceReviewBatchID(second attempt) error = %v", err)
	}
	firstProposalBatchID := snapshot.ProposalManifest.ProposalBatchID
	t.Run("batch A with attempt B", func(t *testing.T) {
		assertReviewableCrossPairRejected(
			t,
			ctx,
			pool,
			second.ProposalOccurrenceID,
			firstProposalBatchID,
			secondProposalBatchID,
			first.ExtractionAttemptID,
			first.ProposalOccurrenceID,
		)
	})
	t.Run("batch B with attempt A", func(t *testing.T) {
		assertReviewableCrossPairRejected(
			t,
			ctx,
			pool,
			first.ProposalOccurrenceID,
			secondProposalBatchID,
			firstProposalBatchID,
			first.ExtractionAttemptID,
			first.ProposalOccurrenceID,
		)
	})

	const driftedOutputHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	commandTag, err := pool.Exec(ctx, `
		UPDATE extraction_attempts
		SET output_hash = $2
		WHERE extraction_attempt_id = $1
	`, first.ExtractionAttemptID, driftedOutputHash)
	if err != nil {
		t.Fatalf("inject persisted output hash drift: %v", err)
	}
	if got, want := commandTag.RowsAffected(), int64(1); got != want {
		t.Fatalf("output-hash drift rows affected = %d, want %d", got, want)
	}
	beforeOutputDrift := reviewableIngestionRowCounts(t, ctx, pool)
	_, err = LoadReviewableSourceClaimReviewSnapshot(
		ctx,
		pool,
		first.ExtractionAttemptID,
		first.ProposalOccurrenceID,
	)
	assertKind(t, err, ErrorReviewContractConflict)
	afterOutputDrift := reviewableIngestionRowCounts(t, ctx, pool)
	if !reflect.DeepEqual(afterOutputDrift, beforeOutputDrift) {
		t.Fatalf("row counts after output-hash drift load = %+v, want unchanged %+v", afterOutputDrift, beforeOutputDrift)
	}
}

func assertReviewableCrossPairRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proposalOccurrenceID string,
	injectedProposalBatchID string,
	restoredProposalBatchID string,
	loadExtractionAttemptID string,
	loadProposalOccurrenceID string,
) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		UPDATE proposal_occurrences
		SET proposal_batch_id = $2
		WHERE proposal_occurrence_id = $1
	`, proposalOccurrenceID, injectedProposalBatchID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" ||
		pgErr.Message != "pending proposal occurrence is immutable before terminal disposition" {
		t.Fatalf("ordinary cross-pair update error = %v, want pending proposal immutability rejection", err)
	}
	if err := setReviewableTestProposalBatch(ctx, pool, proposalOccurrenceID, injectedProposalBatchID); err != nil {
		t.Fatalf("inject cross-bound proposal batch/attempt pair as fixture owner: %v", err)
	}
	restored := false
	defer func() {
		if restored {
			return
		}
		if restoreErr := setReviewableTestProposalBatch(ctx, pool, proposalOccurrenceID, restoredProposalBatchID); restoreErr != nil {
			t.Errorf("restore cross-bound proposal batch after failed assertion: %v", restoreErr)
		}
	}()

	t.Log("fixture owner injected a cross-bound pair with the named guard restored; loader must reject it")
	beforeCrossBinding := reviewableIngestionRowCounts(t, ctx, pool)
	_, err = LoadReviewableSourceClaimReviewSnapshot(
		ctx,
		pool,
		loadExtractionAttemptID,
		loadProposalOccurrenceID,
	)
	assertKind(t, err, ErrorReviewContractConflict)
	afterCrossBinding := reviewableIngestionRowCounts(t, ctx, pool)
	if !reflect.DeepEqual(afterCrossBinding, beforeCrossBinding) {
		t.Fatalf("row counts after cross-bound occurrence load = %+v, want unchanged %+v", afterCrossBinding, beforeCrossBinding)
	}

	if err := setReviewableTestProposalBatch(ctx, pool, proposalOccurrenceID, restoredProposalBatchID); err != nil {
		t.Fatalf("restore proposal batch binding: %v", err)
	}
	restored = true
	if _, err := LoadReviewableSourceClaimReviewSnapshot(
		ctx,
		pool,
		loadExtractionAttemptID,
		loadProposalOccurrenceID,
	); err != nil {
		t.Fatalf("snapshot load after restoring cross-bound occurrence error = %v", err)
	}
}

// setReviewableTestProposalBatch creates owner-only corruption in the disposable
// test schema. The one named guard is restored before commit, including rollback
// on setup failure; runtime code never disables database guards.
func setReviewableTestProposalBatch(ctx context.Context, pool *pgxpool.Pool, occurrenceID, batchID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE proposal_occurrences DISABLE TRIGGER proposal_occurrences_terminal_guard`); err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE proposal_occurrences SET proposal_batch_id = $2 WHERE proposal_occurrence_id = $1
	`, occurrenceID, batchID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return errors.New("cross-bound fixture update did not affect exactly one occurrence")
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE proposal_occurrences ENABLE TRIGGER proposal_occurrences_terminal_guard`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func persistReviewableSourceClaimIntegrationFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	revision string,
) IngestResult {
	t.Helper()
	envelope := ExternalSourceEnvelopeV1{
		SchemaVersion:   ExternalSourceEnvelopeSchemaV1,
		RequestID:       "reviewable-source-" + suffix,
		SourceSystem:    "jira",
		SourceNamespace: "ahe/integration",
		ObjectType:      "issue",
		ObjectID:        "REVIEW-" + suffix,
		Revision:        revision,
		SourceLocation:  "https://example.invalid/issues/" + suffix,
		Title:           "Reviewable ingestion integration fixture",
		ContentFormat:   ExternalSourceContentFormatMarkdown,
		ContentFidelity: ExternalSourceContentFidelityVerbatim,
		Content:         "# Review fixture\nExternal connector owns collection.\nOnly the first two pages were returned.\n",
		Coverage:        ExternalSourceCoverageTruncatedDocument,
		Limitations:     []string{"Only the first two pages were returned by the connector."},
		CollectorID:     "integration-test-collector",
		ConnectorID:     "integration-test-connector",
		ObservedAt:      "2026-09-04T08:00:00Z",
	}
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatalf("CaptureExternalSource(%s) error = %v", suffix, err)
	}
	if len(source.Spans) < 3 {
		t.Fatalf("external source spans = %d, want at least 3", len(source.Spans))
	}

	result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID:          "reviewable-extractor-" + suffix,
		SourceSnapshotID:   source.SourceSnapshotID,
		ExtractionViewID:   source.ExtractionViewID,
		ProducerSessionRef: "session:reviewable-integration-" + suffix,
		ExtractorDefinition: ExtractorDefinitionInput{
			Name:    "reviewable-integration-extractor",
			Version: "v1",
			Config:  map[string]string{"mode": "deterministic-fixture"},
		},
		Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{
			{
				ProposalLocalID: "connector-ownership",
				StatementText:   "External connector owns collection.",
				EvidenceRefs:    []string{source.Spans[1].SpanID},
			},
			{
				ProposalLocalID: "bounded-coverage",
				StatementText:   "Only the first two pages were returned.",
				EvidenceRefs:    []string{source.Spans[2].SpanID},
			},
		}},
	})
	if err != nil {
		t.Fatalf("SubmitExtractorOutput(%s) error = %v", suffix, err)
	}
	return result
}

type reviewableIngestionIntegrationRowCounts struct {
	SourceBlobs          int
	SourceSnapshots      int
	ExtractionViews      int
	SpanCatalogEntries   int
	SourceIntakeRequests int
	ExternalReceipts     int
	ExtractorDefinitions int
	ExtractionRuns       int
	ExtractionAttempts   int
	ProposalBatches      int
	ProposalOccurrences  int
}

func reviewableIngestionRowCounts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) reviewableIngestionIntegrationRowCounts {
	t.Helper()
	var result reviewableIngestionIntegrationRowCounts
	err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM source_blobs),
			(SELECT count(*) FROM source_snapshots),
			(SELECT count(*) FROM extraction_views),
			(SELECT count(*) FROM span_catalog_entries),
			(SELECT count(*) FROM source_intake_requests),
			(SELECT count(*) FROM external_source_intake_receipts),
			(SELECT count(*) FROM extractor_definitions),
			(SELECT count(*) FROM extraction_runs),
			(SELECT count(*) FROM extraction_attempts),
			(SELECT count(*) FROM proposal_batches),
			(SELECT count(*) FROM proposal_occurrences)
	`).Scan(
		&result.SourceBlobs,
		&result.SourceSnapshots,
		&result.ExtractionViews,
		&result.SpanCatalogEntries,
		&result.SourceIntakeRequests,
		&result.ExternalReceipts,
		&result.ExtractorDefinitions,
		&result.ExtractionRuns,
		&result.ExtractionAttempts,
		&result.ProposalBatches,
		&result.ProposalOccurrences,
	)
	if err != nil {
		t.Fatalf("load reviewable ingestion row counts: %v", err)
	}
	return result
}
