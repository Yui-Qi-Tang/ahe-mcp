//go:build integration

package evidenceingestion

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationManualReviewProfileIntakeAndExtractor(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	input, fixture := integrationInputFixture(t, "manual-profile-intake")
	input.ManualReviewProfile = manualReviewProfileFixture()
	first, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := loadManualReviewProfile(ctx, pgxDB{pool: pool}, first.SourceSnapshotID)
	if err != nil || profile == nil || profile.IntakeRequestID != input.RequestID {
		t.Fatalf("profile readback: %+v %v", profile, err)
	}
	for table, want := range map[string]int{"source_snapshots": 1, "source_manual_review_profiles": 1, "source_intake_requests": 1,
		"proposal_occurrences": 0, "canonical_graph_nodes": 0, "canonical_graph_edges": 0} {
		assertTableCount(t, ctx, pool, table, want)
	}
	before := manualProfileIntakeCounts(t, ctx, pool)
	exact, err := CaptureManualSource(ctx, pool, input)
	if err != nil || !exact.Replayed || before != manualProfileIntakeCounts(t, ctx, pool) {
		t.Fatalf("exact capture replay changed records: %+v %v", exact, err)
	}
	input.RequestID += "-second"
	if _, err := CaptureManualSource(ctx, pool, input); err != nil {
		t.Fatal(err)
	}
	second, err := loadManualReviewProfile(ctx, pgxDB{pool: pool}, first.SourceSnapshotID)
	if err != nil || !reflect.DeepEqual(profile, second) {
		t.Fatalf("cross-request replay replaced first binding: %v", err)
	}
	result, err := submitHistoricalFixtureOutput(ctx, pool, ExtractorOutputInput{RequestID: "manual-profile-extractor",
		SourceSnapshotID: first.SourceSnapshotID, ExtractionViewID: first.ExtractionViewID, Output: fixture})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadManualSourceContext(ctx, pgxDB{pool: pool}, result.SourceSnapshotID, result.ExtractionViewID)
	if err != nil || !reflect.DeepEqual(loaded.ManualReviewProfile, profile) {
		t.Fatalf("extractor source lost qualification: %v", err)
	}
	proposal, err := GetProposalByOccurrenceID(ctx, pool, result.ProposalOccurrenceID)
	if err != nil || proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("source qualification mutated canonical admission: %+v %v", proposal, err)
	}
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func TestIntegrationManualReviewProfileConflictAndNewVersion(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	input, _ := integrationInputFixture(t, "manual-profile-conflict")
	input.ManualReviewProfile = manualReviewProfileFixture()
	first, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	before := manualProfileIntakeCounts(t, ctx, pool)
	for _, sameRequest := range []bool{true, false} {
		changed := input
		changed.ManualReviewProfile = manualReviewProfileFixture()
		changed.ManualReviewProfile.Title += " corrected"
		if !sameRequest {
			changed.RequestID += "-changed"
		}
		if _, err := CaptureManualSource(ctx, pool, changed); err == nil {
			t.Fatal("same identity acquired different profile metadata")
		}
		if before != manualProfileIntakeCounts(t, ctx, pool) {
			t.Fatal("profile conflict changed intake tables")
		}
	}
	stripped := input
	stripped.RequestID += "-stripped"
	stripped.ManualReviewProfile = nil
	if _, err := CaptureManualSource(ctx, pool, stripped); err == nil {
		t.Fatal("qualified replay omitted its profile")
	}
	if before != manualProfileIntakeCounts(t, ctx, pool) {
		t.Fatal("stripped replay changed intake tables")
	}
	input.SourceVersion += "-metadata-revision"
	input.RequestID += "-new-version"
	input.ManualReviewProfile.Title += " corrected"
	second, err := CaptureManualSource(ctx, pool, input)
	if err != nil || second.SourceSnapshotID == first.SourceSnapshotID || second.RawContentHash != first.RawContentHash {
		t.Fatalf("explicit metadata revision did not create a new immutable snapshot: %+v %v", second, err)
	}
	assertTableCount(t, ctx, pool, "source_snapshots", 2)
	assertTableCount(t, ctx, pool, "source_manual_review_profiles", 2)
	old, err := loadManualReviewProfile(ctx, pgxDB{pool: pool}, first.SourceSnapshotID)
	if err != nil || old.Profile.Title != manualReviewProfileFixture().Title {
		t.Fatalf("new submission rewrote old metadata: %v", err)
	}
}

func TestIntegrationManualReviewProfileRejectsRetrofitAndInvalidInput(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	input, _ := integrationInputFixture(t, "manual-profile-legacy")
	input.OriginMetadata = map[string]string{"contract_version": ManualSourceReviewProfileV1, "title": "Legacy metadata", "location": "manual:legacy"}
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := loadManualReviewProfile(ctx, pgxDB{pool: pool}, source.SourceSnapshotID)
	if err != nil || profile != nil {
		t.Fatalf("legacy metadata was treated as qualified: %v", err)
	}
	before := manualProfileIntakeCounts(t, ctx, pool)
	input.RequestID += "-retrofit"
	input.ManualReviewProfile = manualReviewProfileFixture()
	if _, err := CaptureManualSource(ctx, pool, input); err == nil {
		t.Fatal("legacy snapshot acquired a profile")
	}
	if before != manualProfileIntakeCounts(t, ctx, pool) {
		t.Fatal("retrofit failure changed intake tables")
	}
	input.SourceVersion += "-new"
	input.ManualReviewProfile.Limitations = nil
	if _, err := CaptureManualSource(ctx, pool, input); err == nil {
		t.Fatal("nil limitations accepted")
	}
	if before != manualProfileIntakeCounts(t, ctx, pool) {
		t.Fatal("invalid typed input reached persistence")
	}
}

func TestIntegrationManualReviewProfileConcurrentFirstCapture(t *testing.T) {
	for _, qualifiedWins := range []bool{false, true} {
		name := "legacy_first"
		if qualifiedWins {
			name = "qualified_first"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			first, _ := integrationInputFixture(t, "manual-profile-race-"+name)
			second := first
			second.RequestID += "-second"
			if qualifiedWins {
				first.ManualReviewProfile = manualReviewProfileFixture()
			} else {
				second.ManualReviewProfile = manualReviewProfileFixture()
			}
			winner, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer winner.Rollback(ctx)
			source, err := buildManualSourceContext(first)
			if err != nil {
				t.Fatal(err)
			}
			tx := pgxTx{tx: winner}
			if _, err := persistSourceAuthorityTx(ctx, tx, source); err != nil {
				t.Fatal(err)
			}
			if _, err := persistExtractionViewSpansTx(ctx, tx, source); err != nil {
				t.Fatal(err)
			}
			if _, err := persistSourceIntakeRequestTx(ctx, tx, source); err != nil {
				t.Fatal(err)
			}
			if _, err := persistManualReviewProfileTx(ctx, tx, source.ManualReviewProfile); err != nil {
				t.Fatal(err)
			}
			loser := manualProfileObservedDB{sqlDB: pgxDB{pool: pool}, pid: make(chan uint32, 1)}
			done := make(chan error, 1)
			go func() { _, err := captureManualSource(ctx, loser, second); done <- err }()
			var loserPID uint32
			select {
			case loserPID = <-loser.pid:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			manualProfileWaitBlocked(t, ctx, pool, winner.Conn().PgConn().PID(), loserPID)
			if err := winner.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("loser changed the first capture profile class")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			wantProfiles := 0
			if qualifiedWins {
				wantProfiles = 1
			}
			assertTableCount(t, ctx, pool, "source_snapshots", 1)
			assertTableCount(t, ctx, pool, "source_intake_requests", 1)
			assertTableCount(t, ctx, pool, "source_manual_review_profiles", wantProfiles)
			assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
			t.Log("pg_blocking_pids proved first-capture lock wait; loser was rejected after committed authority became visible")
		})
	}
}

func manualProfileIntakeCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) [7]int {
	var counts [7]int
	for i, table := range []string{"source_blobs", "source_snapshots", "extraction_views", "span_catalog_entries", "source_intake_requests", "source_manual_review_profiles", "proposal_occurrences"} {
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

type manualProfileObservedDB struct {
	sqlDB
	pid chan uint32
}

func (db manualProfileObservedDB) begin(ctx context.Context) (sqlTx, error) {
	tx, err := db.sqlDB.begin(ctx)
	if err != nil {
		return nil, err
	}
	return &manualProfileObservedTx{sqlTx: tx, pid: db.pid}, nil
}

type manualProfileObservedTx struct {
	sqlTx
	pid  chan uint32
	sent bool
}

func (tx *manualProfileObservedTx) exec(ctx context.Context, query string, args ...any) (execResult, error) {
	if !tx.sent && strings.Contains(query, "INSERT INTO source_blobs") {
		tx.pid <- tx.sqlTx.(pgxTx).tx.Conn().PgConn().PID()
		tx.sent = true
	}
	return tx.sqlTx.exec(ctx, query, args...)
}

func manualProfileWaitBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, winnerPID, loserPID uint32) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, "SELECT $1::int=ANY(pg_blocking_pids($2::int))", winnerPID, loserPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
