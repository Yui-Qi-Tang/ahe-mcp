package evidenceingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSourceSnapshotConflictReadbackBindsIdentity(t *testing.T) {
	readFailure := errors.New("snapshot read failed")
	for _, tc := range []struct {
		name     string
		external bool
		missing  bool
		mutate   func(*mockSourceSnapshot)
		readErr  error
		conflict bool
		invalid  bool
	}{
		{name: "exact_manual"},
		{name: "exact_external", external: true},
		{name: "different_id", missing: true, conflict: true},
		{name: "system", mutate: func(s *mockSourceSnapshot) { s.sourceSystem = "other" }, conflict: true},
		{name: "source_id", mutate: func(s *mockSourceSnapshot) { s.sourceID = "other" }, conflict: true},
		{name: "version", mutate: func(s *mockSourceSnapshot) { s.sourceVersion = "other" }, conflict: true},
		{name: "raw_hash", mutate: func(s *mockSourceSnapshot) { s.rawContentHash = "other" }, conflict: true},
		{name: "manual_metadata_is_first_writer", mutate: func(s *mockSourceSnapshot) { s.originMetadata = []byte(`{"audit":"second"}`) }},
		{name: "external_metadata_conflict", external: true, mutate: func(s *mockSourceSnapshot) { s.originMetadata = []byte(`{"audit":"second"}`) }, conflict: true},
		{name: "invalid_stored_metadata", mutate: func(s *mockSourceSnapshot) { s.originMetadata = []byte(`not json`) }, invalid: true},
		{name: "read_failure", readErr: readFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newMockSQLDB()
			source := manualSourceContext{
				SourceSnapshot: SourceSnapshot{
					ID: "srcsnap:expected", SourceSystem: SourceSystemManualText,
					SourceID: "source", SourceVersion: "v1", RawContentHash: "sha256:expected",
					OriginMetadata: map[string]string{"audit": "first"},
				},
				Raw: []byte("exact source"),
			}
			if tc.external {
				source.SourceSnapshot.SourceSystem = SourceSystemExternalDocument
			}
			expected := source.SourceSnapshot
			stored := mockSourceSnapshot{
				sourceSystem: expected.SourceSystem, sourceID: expected.SourceID,
				sourceVersion: expected.SourceVersion, rawContentHash: expected.RawContentHash,
				originMetadata: []byte(`{"audit":"first"}`),
			}
			if tc.mutate != nil {
				tc.mutate(&stored)
			}
			if !tc.missing {
				db.sourceSnapshots[expected.ID] = stored
			}
			db.sourceBlobs[expected.RawContentHash] = append([]byte(nil), source.Raw...)
			tx := &snapshotConflictReadbackTx{sqlTx: &mockSQLTx{db: db}, readErr: tc.readErr}
			changed, err := persistSourceAuthorityTx(t.Context(), tx, source)
			switch {
			case tc.conflict:
				assertKind(t, err, ErrorOccurrenceConflict)
			case tc.readErr != nil:
				if !errors.Is(err, tc.readErr) {
					t.Fatalf("read failure = %v, want wrapped original failure", err)
				}
			case tc.invalid:
				if err == nil {
					t.Fatal("invalid stored metadata was accepted")
				}
			default:
				if err != nil {
					t.Fatalf("exact conflict readback: %v", err)
				}
			}
			if changed {
				t.Fatal("conflicting insert reported new source authority")
			}
			if tx.reads != 1 {
				t.Fatalf("snapshot readback calls = %d, want one exact-ID read", tx.reads)
			}
		})
	}
}

// The insert has already lost either unique-key conflict. This double tests
// readback independently of the mock store's primary-key-only insert behavior.
type snapshotConflictReadbackTx struct {
	sqlTx
	readErr error
	reads   int
}

func (tx *snapshotConflictReadbackTx) exec(ctx context.Context, query string, args ...any) (execResult, error) {
	if strings.Contains(query, "INSERT INTO source_snapshots") {
		return mockExecResult(0), nil
	}
	return tx.sqlTx.exec(ctx, query, args...)
}

func (tx *snapshotConflictReadbackTx) queryRow(ctx context.Context, query string, args ...any) sqlRow {
	if strings.Contains(query, "FROM source_snapshots") {
		tx.reads++
		if tx.readErr != nil {
			return mockRow{err: tx.readErr}
		}
	}
	return tx.sqlTx.queryRow(ctx, query, args...)
}
