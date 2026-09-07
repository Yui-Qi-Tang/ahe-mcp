package evidenceingestion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOrdinaryAdmissionLoaderLocksBeforeJoinedRead(t *testing.T) {
	db := newMockSQLDB()
	proposal, err := ingestManualText(t.Context(), db, testManualInput("ordinary-lock-order"), testFixture())
	if err != nil {
		t.Fatal(err)
	}
	tx := &ordinaryAdmissionLockSpy{sqlTx: &mockSQLTx{db: db}}
	got, err := loadProposalForAdmission(t.Context(), tx, proposal.ProposalOccurrenceID)
	if err != nil || got.ProposalOccurrenceID != proposal.ProposalOccurrenceID {
		t.Fatalf("proposal load=%+v error=%v", got, err)
	}
	if len(tx.queries) != 2 || compactSQL(tx.queries[0]) != "SELECT proposal_occurrence_id FROM proposal_occurrences WHERE proposal_occurrence_id = $1 FOR UPDATE" ||
		!strings.Contains(tx.queries[1], "JOIN extraction_attempts") || strings.Contains(tx.queries[1], "FOR UPDATE") {
		t.Fatalf("expected exact proposal lock then fresh provenance query: %v", tx.queries)
	}
}

func TestOrdinaryAdmissionLoaderStopsOnLockFailure(t *testing.T) {
	for _, failure := range []error{pgx.ErrNoRows, errors.New("lock unavailable"), &pgconn.PgError{Code: "40001"}} {
		t.Run(failure.Error(), func(t *testing.T) {
			tx := &ordinaryAdmissionLockSpy{lockError: failure}
			_, err := loadProposalForAdmission(t.Context(), tx, "occ:missing")
			if errors.Is(failure, pgx.ErrNoRows) {
				assertKind(t, err, ErrorMissingSourceViewAttempt)
			} else if !errors.Is(err, failure) {
				t.Fatalf("lock error=%v, want original cause=%v", err, failure)
			}
			if len(tx.queries) != 1 {
				t.Fatal("joined read proceeded after lock failure")
			}
		})
	}
}

type ordinaryAdmissionLockSpy struct {
	sqlTx
	queries   []string
	lockError error
}

func (tx *ordinaryAdmissionLockSpy) queryRow(ctx context.Context, query string, args ...any) sqlRow {
	tx.queries = append(tx.queries, query)
	if tx.lockError != nil {
		return mockRow{err: tx.lockError}
	}
	return tx.sqlTx.queryRow(ctx, query, args...)
}
