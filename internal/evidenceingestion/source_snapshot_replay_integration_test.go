//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationSourceSnapshotConflictReadbackFailsClosed(t *testing.T) {
	for _, differentID := range []bool{true, false} {
		name := "same_id_different_natural_tuple"
		if differentID {
			name = "same_natural_tuple_different_id"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, _ := integrationInputFixture(t, "snapshot-collision")
			source, err := buildManualSourceContext(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO source_blobs(raw_content_hash,raw_content,byte_length) VALUES($1,$2,$3)`, source.SourceSnapshot.RawContentHash, input.Raw, len(input.Raw)); err != nil {
				t.Fatal(err)
			}
			id, sourceID := source.SourceSnapshot.ID, source.SourceSnapshot.SourceID
			if differentID {
				id = "srcsnap:deliberately-different-fixture-id"
			} else {
				sourceID = "deliberately-different-fixture-source"
			}
			// Model conflicting persisted authority without claiming this malformed
			// coordinate is a valid output of public source capture.
			if _, err := pool.Exec(ctx, `INSERT INTO source_snapshots(source_snapshot_id,source_system,source_id,source_version,raw_content_hash) VALUES($1,$2,$3,$4,$5)`,
				id, source.SourceSnapshot.SourceSystem, sourceID, source.SourceSnapshot.SourceVersion, source.SourceSnapshot.RawContentHash); err != nil {
				t.Fatal(err)
			}
			_, err = CaptureManualSource(ctx, pool, input)
			assertKind(t, err, ErrorOccurrenceConflict)
			assertTableCount(t, ctx, pool, "source_blobs", 1)
			assertTableCount(t, ctx, pool, "source_snapshots", 1)
			for _, table := range []string{"extraction_views", "span_catalog_entries", "source_intake_requests", "extraction_runs", "extraction_attempts", "proposal_occurrences", "canonical_graph_nodes", "canonical_graph_edges"} {
				assertTableCount(t, ctx, pool, table, 0)
			}
		})
	}
}

func TestIntegrationSourceSnapshotManualMetadataReplayContract(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "manual-metadata-contract")
	input.OriginMetadata = map[string]string{"audit": "first"}
	first, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	input.OriginMetadata = map[string]string{"audit": "second"}
	_, err = CaptureManualSource(ctx, pool, input)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	input.RequestID = "manual-metadata-new-request"
	second, err := CaptureManualSource(ctx, pool, input)
	if err != nil || second.SourceSnapshotID != first.SourceSnapshotID || second.ExtractionViewID != first.ExtractionViewID {
		t.Fatalf("manual across-request snapshot reuse changed: result=%+v error=%v", second, err)
	}
	var data []byte
	if err := pool.QueryRow(ctx, "SELECT origin_metadata FROM source_snapshots WHERE source_snapshot_id=$1", first.SourceSnapshotID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil || metadata["audit"] != "first" {
		t.Fatal("manual first-writer audit metadata was overwritten")
	}
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "source_intake_requests", 2)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
}

func TestIntegrationSourceSnapshotParallelCaptureConvergesAfterWait(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, _ := integrationInputFixture(t, "snapshot-parallel")
	source, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatal(err)
	}
	// Commit the blob first so the second capture blocks specifically on the
	// competing snapshot, not on source_blobs' unique index.
	if _, err := pool.Exec(ctx, `INSERT INTO source_blobs(raw_content_hash,raw_content,byte_length) VALUES($1,$2,$3)`, source.SourceSnapshot.RawContentHash, input.Raw, len(input.Raw)); err != nil {
		t.Fatal(err)
	}
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(context.Background())
	if _, err := persistSourceAuthorityTx(ctx, pgxTx{tx: first}, source); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	type outcome struct {
		result SourceIntakeResult
		err    error
	}
	completed := make(chan outcome, 1)
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		result, err := captureManualSource(callCtx, snapshotReplayConnectionDB{conn: second.Conn()}, input)
		completed <- outcome{result: result, err: err}
	}()
	// Cleanup unblocks and joins the worker before its connection is released,
	// including when an assertion fails while the first transaction holds locks.
	joined := false
	defer func() {
		cancel()
		_ = first.Rollback(context.Background())
		if !joined {
			<-completed
		}
	}()
	waitForSourceSnapshotInsertLock(t, ctx, pool, second.Conn().PgConn().PID())
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var got outcome
	select {
	case got = <-completed:
		joined = true
	case <-ctx.Done():
		t.Fatalf("waiting for source capture: %v", ctx.Err())
	}
	if got.err != nil {
		t.Fatalf("capture after competing snapshot commit: %v", got.err)
	}
	if got.result.SourceSnapshotID != source.SourceSnapshot.ID || got.result.ExtractionViewID != source.ExtractionView.ID {
		t.Fatalf("capture lost exact snapshot/view: %+v", got.result)
	}
	replay, err := CaptureManualSource(ctx, pool, input)
	if err != nil || !replay.Replayed || replay.SourceSnapshotID != got.result.SourceSnapshotID {
		t.Fatalf("capture exact replay: result=%+v error=%v", replay, err)
	}
	assertTableCount(t, ctx, pool, "source_blobs", 1)
	assertTableCount(t, ctx, pool, "source_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_views", 1)
	assertTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertTableCount(t, ctx, pool, "proposal_occurrences", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 0)
}

func waitForSourceSnapshotInsertLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(waitCtx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE pid = $1 AND wait_event_type = 'Lock'
				  AND query LIKE '%INSERT INTO source_snapshots%'
				  AND cardinality(pg_blocking_pids(pid)) > 0
			)
		`, pid).Scan(&blocked); err != nil {
			t.Fatalf("reading exact source insert waiter: %v", err)
		}
		if blocked {
			return
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("source capture did not wait on competing snapshot: %v", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// Pin the public capture workflow to one observable test connection.
type snapshotReplayConnectionDB struct {
	conn *pgx.Conn
}

func (db snapshotReplayConnectionDB) begin(ctx context.Context) (sqlTx, error) {
	tx, err := db.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxTx{tx: tx}, nil
}

func (db snapshotReplayConnectionDB) queryRow(ctx context.Context, query string, args ...any) sqlRow {
	return db.conn.QueryRow(ctx, query, args...)
}

func (db snapshotReplayConnectionDB) query(ctx context.Context, query string, args ...any) (sqlRows, error) {
	return db.conn.Query(ctx, query, args...)
}
