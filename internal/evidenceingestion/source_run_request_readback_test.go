package evidenceingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSourceExtractionRequestReadbackBindsExactIdentity(t *testing.T) {
	readFailure := errors.New("run read failed")
	for _, tc := range []struct {
		name       string
		otherRunID bool
		mutate     func(*mockExtractionRun)
		readErr    error
		conflict   bool
	}{
		{name: "exact"},
		{name: "session_is_annotation", mutate: func(run *mockExtractionRun) { run.producerSessionRef = "session:first-created" }},
		{name: "other_run_id_is_not_rebound", otherRunID: true, conflict: true},
		{name: "request", mutate: func(run *mockExtractionRun) { run.requestID = "other-request" }, conflict: true},
		{name: "definition", mutate: func(run *mockExtractionRun) { run.extractorDefinitionID = "other-definition" }, conflict: true},
		{name: "source", mutate: func(run *mockExtractionRun) { run.sourceSnapshotID = "other-source" }, conflict: true},
		{name: "view", mutate: func(run *mockExtractionRun) { run.extractionViewID = "other-view" }, conflict: true},
		{name: "read_failure", readErr: readFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt, err := buildAttemptContext(testManualInput("run-request-readback"))
			if err != nil {
				t.Fatal(err)
			}
			expected := attempt.ExtractionRun
			stored := mockExtractionRun{
				extractorDefinitionID: expected.ExtractorDefinitionID,
				sourceSnapshotID:      expected.SourceSnapshotID,
				extractionViewID:      expected.ExtractionViewID,
				requestID:             expected.RequestID,
			}
			if tc.mutate != nil {
				tc.mutate(&stored)
			}
			storedID := expected.ID
			if tc.otherRunID {
				storedID = "run:other-persisted-id"
			}
			// Deliberately malformed persisted coordinates exercise fail-closed
			// readback independently of the public-input PostgreSQL regressions.
			mock := newMockSQLDB()
			mock.extractionRuns[storedID] = stored
			db := &sourceRunReadbackDB{mockSQLDB: mock, readErr: tc.readErr}
			status, err := persistAttemptStartForExistingSource(t.Context(), db, attempt)
			switch {
			case tc.conflict:
				assertKind(t, err, ErrorIdempotencyKeyReused)
			case tc.readErr != nil:
				if !errors.Is(err, tc.readErr) {
					t.Fatalf("readback error = %v, want original read failure", err)
				}
			default:
				if err != nil || status != attemptStatusStarted {
					t.Fatalf("exact readback: status=%q error=%v", status, err)
				}
			}
			wantAttempts := 1
			if tc.conflict || tc.readErr != nil {
				wantAttempts = 0
			}
			if len(mock.extractionAttempts) != wantAttempts {
				t.Fatalf("attempt count = %d, want %d", len(mock.extractionAttempts), wantAttempts)
			}
			if len(mock.extractionRuns) != 1 || mock.extractionRuns[storedID] != stored {
				t.Fatal("conflict readback changed persisted run identity or annotation")
			}
		})
	}
}

type sourceRunReadbackDB struct {
	*mockSQLDB
	readErr error
}

func (db *sourceRunReadbackDB) begin(context.Context) (sqlTx, error) {
	return sourceRunReadbackTx{sqlTx: &mockSQLTx{db: db.mockSQLDB}, readErr: db.readErr}, nil
}

type sourceRunReadbackTx struct {
	sqlTx
	readErr error
}

func (tx sourceRunReadbackTx) queryRow(ctx context.Context, query string, args ...any) sqlRow {
	if tx.readErr != nil && strings.Contains(query, "FROM extraction_runs") {
		return mockRow{err: tx.readErr}
	}
	return tx.sqlTx.queryRow(ctx, query, args...)
}

func TestPersistAttemptFailureOnlyChangesStartedAttempt(t *testing.T) {
	for _, status := range []string{"missing", attemptStatusStarted, attemptStatusSucceeded, attemptStatusFailed} {
		t.Run(status, func(t *testing.T) {
			attempt, err := buildAttemptContext(testManualInput("failure-cas"))
			if err != nil {
				t.Fatal(err)
			}
			db := newMockSQLDB()
			stored := mockExtractionAttempt{
				runID: attempt.ExtractionRun.ID, number: 1, status: status,
				outputHash: "first-hash", fixtureOutput: `{"first":true}`, failureClass: "first-failure",
			}
			if status != "missing" {
				db.extractionAttempts[attempt.ExtractionAttempt.ID] = stored
			}
			outputHash, output := "late-hash", `{"late":true}`
			cause := newDomainError(ErrorInvalidInput, "late failure")
			if err := persistAttemptFailureRecord(t.Context(), db, attempt, &outputHash, &output, cause); err != nil {
				t.Fatal(err)
			}
			got, exists := db.extractionAttempts[attempt.ExtractionAttempt.ID]
			switch status {
			case "missing":
				if exists {
					t.Fatal("failure record created a missing attempt")
				}
			case attemptStatusStarted:
				if !exists || got.status != attemptStatusFailed || got.outputHash != outputHash || got.fixtureOutput != output || got.failureClass != string(ErrorInvalidInput) {
					t.Fatalf("started attempt did not record failure: %+v", got)
				}
			default:
				if !exists || got != stored {
					t.Fatalf("late failure changed terminal attempt: got=%+v want=%+v", got, stored)
				}
			}
		})
	}
}
