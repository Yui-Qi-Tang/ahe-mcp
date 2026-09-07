package evidenceingestion

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestLoadReviewableSourceClaimReviewSnapshotRebuildsAuthorityInReadOnlySnapshot(t *testing.T) {
	db := newReviewSnapshotTestDB(t, 2)
	selected := db.batch.Occurrences[1]

	got, err := loadReviewableSourceClaimReviewSnapshot(
		context.Background(),
		db,
		db.batch.ExtractionAttempt.ID,
		selected.ID,
	)
	if err != nil {
		t.Fatalf("loadReviewableSourceClaimReviewSnapshot() error = %v", err)
	}
	wantManifest := mustReviewableIngestionManifest(t, db.batch)
	wantReceipt := mustReviewableIngestionReceipt(t, db.batch, wantManifest)
	wantPackage, err := BuildSourceClaimReviewPackage(
		wantReceipt,
		wantManifest,
		reviewableIngestionTestQuery(db.batch, 1),
	)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewPackage() error = %v", err)
	}
	want := ReviewableSourceClaimReviewSnapshot{
		SubmissionReceipt: wantReceipt,
		ProposalManifest:  wantManifest,
		ReviewPackage:     wantPackage,
		ExactReviewSubject: ExactReviewSubject{
			SubmissionReceiptID:  wantReceipt.ID,
			ProposalManifestID:   wantManifest.ID,
			ProposalOccurrenceID: selected.ID,
			ProposalBasisID:      wantPackage.ProposalBasis.ID,
			ReviewPackageID:      wantPackage.ID,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded snapshot = %+v, want %+v", got, want)
	}
	if !db.configuredReadOnly || !db.committed {
		t.Fatalf("transaction configured/committed = %t/%t, want true/true", db.configuredReadOnly, db.committed)
	}
}

func TestLoadReviewableSourceClaimReviewSnapshotAllowsTerminalSiblingLifecycle(t *testing.T) {
	db := newReviewSnapshotTestDB(t, 2)
	db.occurrences[0].AdmissionOutcome = admissionOutcomeAdmitted
	db.occurrences[0].CanonicalRef = "canon-node:" + strings.Repeat("a", 64)
	selected := db.batch.Occurrences[1]

	got, err := loadReviewableSourceClaimReviewSnapshot(
		context.Background(),
		db,
		db.batch.ExtractionAttempt.ID,
		selected.ID,
	)
	if err != nil {
		t.Fatalf("loadReviewableSourceClaimReviewSnapshot() error = %v", err)
	}
	if got.ExactReviewSubject.ProposalOccurrenceID != selected.ID {
		t.Fatalf("selected occurrence = %q, want %q", got.ExactReviewSubject.ProposalOccurrenceID, selected.ID)
	}
}

func TestLoadReviewableSourceClaimReviewSnapshotFailsClosedOnPersistedDrift(t *testing.T) {
	tests := []struct {
		name string
		edit func(*reviewSnapshotTestDB)
	}{
		{
			name: "output hash",
			edit: func(db *reviewSnapshotTestDB) {
				db.outputHash = contentHash([]byte("different output"))
			},
		},
		{
			name: "succeeded attempt retains failure class",
			edit: func(db *reviewSnapshotTestDB) {
				db.failureClassNull = false
			},
		},
		{
			name: "succeeded attempt retains failure metadata",
			edit: func(db *reviewSnapshotTestDB) {
				db.failureMetadataNull = false
			},
		},
		{
			name: "fixture output unknown field",
			edit: func(db *reviewSnapshotTestDB) {
				data, err := jsonBytes(db.fixture)
				if err != nil {
					t.Fatalf("jsonBytes(fixture) error = %v", err)
				}
				db.fixtureData = append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"unknown":true}`)...)
			},
		},
		{
			name: "second proposal batch",
			edit: func(db *reviewSnapshotTestDB) {
				extra := db.batchRecord
				extra.batch.ID = "batch:" + strings.Repeat("f", 64)
				db.batchRecords = append(db.batchRecords, extra)
			},
		},
		{
			name: "proposal count",
			edit: func(db *reviewSnapshotTestDB) {
				db.batchRecords[0].proposalCount++
			},
		},
		{
			name: "proposal lifecycle",
			edit: func(db *reviewSnapshotTestDB) {
				db.occurrences[0].AdmissionOutcome = admissionOutcomeAuditOnly
			},
		},
		{
			name: "source ref unknown field",
			edit: func(db *reviewSnapshotTestDB) {
				occurrence := db.occurrences[0]
				data, err := jsonBytes(occurrence.SourceRefs)
				if err != nil {
					t.Fatalf("jsonBytes(source refs) error = %v", err)
				}
				data = append(append([]byte(nil), data[:len(data)-2]...), []byte(`,"unknown":true}]`)...)
				db.sourceRefsData = map[string][]byte{occurrence.ID: data}
			},
		},
		{
			name: "proposed payload",
			edit: func(db *reviewSnapshotTestDB) {
				db.occurrences[0].ProposedPayload["caller_authority"] = "smuggled"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newReviewSnapshotTestDB(t, 1)
			test.edit(db)
			_, err := loadReviewableSourceClaimReviewSnapshot(
				context.Background(),
				db,
				db.batch.ExtractionAttempt.ID,
				db.batch.Occurrences[0].ID,
			)
			assertKind(t, err, ErrorReviewContractConflict)
			if db.committed {
				t.Fatal("failed review snapshot transaction committed")
			}
		})
	}
}

func TestLoadReviewableSourceClaimReviewSnapshotRejectsBatchAWithAttemptB(t *testing.T) {
	db := newReviewSnapshotTestDB(t, 1)
	crossPaired := db.occurrences[0]
	crossPaired.ID = "occ:" + strings.Repeat("a", 64)
	crossPaired.ExtractionAttemptID = "attempt:" + strings.Repeat("b", 64)
	db.occurrences = append(db.occurrences, crossPaired)

	_, err := loadReviewableSourceClaimReviewSnapshot(
		context.Background(),
		db,
		db.batch.ExtractionAttempt.ID,
		db.batch.Occurrences[0].ID,
	)
	assertKind(t, err, ErrorReviewContractConflict)
}

func TestLoadReviewableSourceClaimReviewSnapshotRejectsBatchBWithAttemptA(t *testing.T) {
	db := newReviewSnapshotTestDB(t, 1)
	crossPaired := db.occurrences[0]
	crossPaired.ID = "occ:" + strings.Repeat("c", 64)
	crossPaired.BatchID = "batch:" + strings.Repeat("d", 64)
	db.occurrences = append(db.occurrences, crossPaired)

	_, err := loadReviewableSourceClaimReviewSnapshot(
		context.Background(),
		db,
		db.batch.ExtractionAttempt.ID,
		db.batch.Occurrences[0].ID,
	)
	assertKind(t, err, ErrorReviewContractConflict)
}

func TestLoadReviewableSourceClaimReviewSnapshotRejectsForeignOccurrence(t *testing.T) {
	db := newReviewSnapshotTestDB(t, 1)
	foreign := "occ:" + strings.Repeat("f", 64)

	_, err := loadReviewableSourceClaimReviewSnapshot(
		context.Background(),
		db,
		db.batch.ExtractionAttempt.ID,
		foreign,
	)
	assertKind(t, err, ErrorReviewContractConflict)
}

func TestLoadReviewableSourceClaimReviewSnapshotRejectsNilPool(t *testing.T) {
	_, err := LoadReviewableSourceClaimReviewSnapshot(context.Background(), nil, "attempt:x", "occ:x")
	assertKind(t, err, ErrorInvalidInput)
}

func TestLoadReviewableSourceClaimReviewSnapshotValidatesCoordinatesBeforeBegin(t *testing.T) {
	db := newReviewSnapshotTestDB(t, 1)

	_, err := loadReviewableSourceClaimReviewSnapshot(context.Background(), db, "wrong", db.batch.Occurrences[0].ID)
	assertKind(t, err, ErrorInvalidRecordID)
	if db.began {
		t.Fatal("invalid coordinates began a database transaction")
	}
}

type reviewSnapshotTestDB struct {
	batch               ReviewableSourceClaimBatch
	fixture             FrozenExtractorOutput
	outputHash          string
	batchRecord         reviewableProposalBatchRecord
	batchRecords        []reviewableProposalBatchRecord
	occurrences         []ProposalOccurrence
	fixtureData         []byte
	sourceRefsData      map[string][]byte
	failureClassNull    bool
	failureMetadataNull bool
	configuredReadOnly  bool
	began               bool
	committed           bool
}

func newReviewSnapshotTestDB(t *testing.T, proposalCount int) *reviewSnapshotTestDB {
	t.Helper()
	batch := reviewableIngestionTestBatch(t, proposalCount, "session:postgres-loader")
	fixture := FrozenExtractorOutput{Proposals: make([]ExtractorProposalOutput, 0, len(batch.Occurrences))}
	for _, occurrence := range batch.Occurrences {
		evidenceRefs := make([]string, 0, len(occurrence.SourceRefs))
		for _, ref := range occurrence.SourceRefs {
			evidenceRefs = append(evidenceRefs, ref.SpanID)
		}
		fixture.Proposals = append(fixture.Proposals, ExtractorProposalOutput{
			ProposalLocalID: occurrence.ProposalLocalID,
			StatementText:   occurrence.StatementText,
			EvidenceRefs:    evidenceRefs,
		})
	}
	outputHash, err := extractorOutputHash(fixture)
	if err != nil {
		t.Fatalf("extractorOutputHash() error = %v", err)
	}
	if outputHash != batch.FixtureOutputHash {
		t.Fatalf("test fixture hash = %s, want %s", outputHash, batch.FixtureOutputHash)
	}
	record := reviewableProposalBatchRecord{batch: batch.ProposalBatch, proposalCount: len(batch.Occurrences)}
	return &reviewSnapshotTestDB{
		batch:               batch,
		fixture:             fixture,
		outputHash:          outputHash,
		batchRecord:         record,
		batchRecords:        []reviewableProposalBatchRecord{record},
		occurrences:         cloneReviewSnapshotOccurrences(batch.Occurrences),
		failureClassNull:    true,
		failureMetadataNull: true,
	}
}

func (db *reviewSnapshotTestDB) begin(context.Context) (sqlTx, error) {
	db.began = true
	return &reviewSnapshotTestTx{db: db}, nil
}

func (db *reviewSnapshotTestDB) query(context.Context, string, ...any) (sqlRows, error) {
	return nil, fmt.Errorf("query outside review snapshot transaction")
}

func (db *reviewSnapshotTestDB) queryRow(context.Context, string, ...any) sqlRow {
	return mockRow{err: fmt.Errorf("query row outside review snapshot transaction")}
}

type reviewSnapshotTestTx struct {
	db *reviewSnapshotTestDB
}

func (tx *reviewSnapshotTestTx) exec(_ context.Context, query string, _ ...any) (execResult, error) {
	if strings.Contains(query, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY") {
		tx.db.configuredReadOnly = true
		return mockExecResult(0), nil
	}
	return nil, fmt.Errorf("unexpected review snapshot exec: %s", compactSQL(query))
}

func (tx *reviewSnapshotTestTx) query(_ context.Context, query string, args ...any) (sqlRows, error) {
	switch {
	case strings.Contains(query, "FROM proposal_batches"):
		rows := make([]mockRow, 0, len(tx.db.batchRecords))
		for _, record := range tx.db.batchRecords {
			rows = append(rows, mockRow{values: []any{
				record.batch.ID,
				record.batch.ExtractionAttemptID,
				record.batch.Status,
				record.proposalCount,
				true,
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	case strings.Contains(query, "FROM proposal_occurrences"):
		rows := make([]mockRow, 0, len(tx.db.occurrences))
		for _, occurrence := range tx.db.occurrences {
			matchesAttempt := occurrence.ExtractionAttemptID == args[0].(string)
			matchesBatch := len(args) > 1 && occurrence.BatchID == args[1].(string)
			if !matchesAttempt && !matchesBatch {
				continue
			}
			sourceRefs := tx.db.sourceRefsData[occurrence.ID]
			if sourceRefs == nil {
				var err error
				sourceRefs, err = jsonBytes(occurrence.SourceRefs)
				if err != nil {
					return nil, err
				}
			}
			payload, err := jsonBytes(occurrence.ProposedPayload)
			if err != nil {
				return nil, err
			}
			rows = append(rows, mockRow{values: []any{
				occurrence.ID,
				occurrence.BatchID,
				occurrence.ExtractionAttemptID,
				occurrence.ProposalLocalID,
				occurrence.ProposalKind,
				occurrence.StatementText,
				occurrence.ProposalFingerprint,
				occurrence.ProposalFingerprintVersion,
				occurrence.AdmissionOutcome,
				occurrence.CanonicalRef,
				sourceRefs,
				payload,
			}})
		}
		return &mockRows{rows: rows, index: -1}, nil
	default:
		return nil, fmt.Errorf("unexpected review snapshot query: %s", compactSQL(query))
	}
}

func (tx *reviewSnapshotTestTx) queryRow(_ context.Context, query string, _ ...any) sqlRow {
	batch := tx.db.batch
	switch {
	case strings.Contains(query, "FROM extraction_attempts ea"):
		fixtureData := tx.db.fixtureData
		if fixtureData == nil {
			var err error
			fixtureData, err = jsonBytes(tx.db.fixture)
			if err != nil {
				return mockRow{err: err}
			}
		}
		configData, err := jsonBytes(batch.ExtractorDefinition.Config)
		if err != nil {
			return mockRow{err: err}
		}
		return mockRow{values: []any{
			batch.ExtractionAttempt.ID,
			batch.ExtractionAttempt.RunID,
			batch.ExtractionAttempt.Number,
			batch.ExtractionAttempt.Status,
			tx.db.outputHash,
			fixtureData,
			true,
			tx.db.failureClassNull,
			tx.db.failureMetadataNull,
			batch.ExtractionRun.ID,
			batch.ExtractionRun.RequestID,
			batch.ExtractionRun.ExtractorDefinitionID,
			batch.ExtractionRun.ProducerSessionRef,
			batch.SourceSnapshot.ID,
			batch.ExtractionView.ID,
			"",
			batch.ExtractorDefinition.ID,
			batch.ExtractorDefinition.Name,
			batch.ExtractorDefinition.Version,
			batch.ExtractorDefinition.ConfigHash,
			configData,
		}}
	case strings.Contains(query, "FROM source_snapshots ss"):
		originMetadata, err := jsonBytes(batch.SourceSnapshot.OriginMetadata)
		if err != nil {
			return mockRow{err: err}
		}
		spans, err := jsonBytes(batch.Spans)
		if err != nil {
			return mockRow{err: err}
		}
		return mockRow{values: []any{
			batch.SourceSnapshot.ID,
			batch.SourceSnapshot.SourceSystem,
			batch.SourceSnapshot.SourceID,
			batch.SourceSnapshot.SourceVersion,
			batch.SourceSnapshot.RawContentHash,
			originMetadata,
			batch.ExtractionView.ID,
			batch.ExtractionView.SourceSnapshotID,
			batch.ExtractionView.RendererName,
			batch.ExtractionView.RendererVersion,
			batch.ExtractionView.Rendered,
			batch.ExtractionView.RenderedContentHash,
			spans,
		}}
	default:
		return mockRow{err: fmt.Errorf("unexpected review snapshot query row: %s", compactSQL(query))}
	}
}

func (tx *reviewSnapshotTestTx) commit(context.Context) error {
	tx.db.committed = true
	return nil
}

func (*reviewSnapshotTestTx) rollback(context.Context) error {
	return nil
}

func cloneReviewSnapshotOccurrences(input []ProposalOccurrence) []ProposalOccurrence {
	result := make([]ProposalOccurrence, len(input))
	for index, occurrence := range input {
		result[index] = occurrence
		result[index].SourceRefs = append([]ResolvedSourceRef(nil), occurrence.SourceRefs...)
		result[index].ProposedPayload = make(map[string]any, len(occurrence.ProposedPayload))
		for key, value := range occurrence.ProposedPayload {
			result[index].ProposedPayload[key] = value
		}
	}
	return result
}
