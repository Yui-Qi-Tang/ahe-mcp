//go:build integration

package evidenceingestion

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationQueryMCPReadSourceStatesKeepsOneSnapshot(t *testing.T) {
	for _, observation := range []string{"new_revision", "repeated_revision"} {
		for _, mode := range []string{
			MCPReadSourceStateModeLatestObserved,
			MCPReadSourceStateModeHistory,
			"limited_history",
			MCPReadSourceStateModeExactRevision,
			MCPReadSourceStateModeCompare,
		} {
			t.Run(observation+"/"+mode, func(t *testing.T) {
				ctx, pool := integrationPool(t)
				fixture := insertMCPReadSourceStateQueryFixture(t, ctx, pool)
				snapshot, revision := fixture.snapshotB, fixture.revisionB
				if observation == "new_revision" {
					revision = "provider-revision-c"
					source, err := CaptureManualSource(ctx, pool, ManualTextInput{
						SourceSystem:  SourceSystemMCPReadDocument,
						SourceID:      "atlassian:jira:AHE-42",
						SourceVersion: revision,
						Raw:           []byte("Contact the billing team for refund assistance.\n"),
						RequestID:     "mcp-source-state-query-source-c",
					})
					if err != nil {
						t.Fatalf("capture source revision C: %v", err)
					}
					snapshot = source.SourceSnapshotID
				}
				complete := prepareMCPSourceStateConcurrentCompletion(t, ctx, pool, fixture.sourceBindingID, snapshot, revision)
				input := MCPReadSourceStateQueryInput{SourceBindingID: fixture.sourceBindingID, Mode: mode}
				switch mode {
				case "limited_history":
					input.Mode, input.Limit = MCPReadSourceStateModeHistory, 1
				case MCPReadSourceStateModeExactRevision:
					input.Revision = fixture.revisionB
				case MCPReadSourceStateModeCompare:
					input.FromRevision, input.ToRevision = fixture.revisionA, fixture.revisionB
				}
				before, err := QueryMCPReadSourceStates(ctx, pool, input)
				if err != nil {
					t.Fatalf("query before completion: %v", err)
				}
				beforeCounts := boundedSourceViewIntegrationCounts(t, ctx, pool)
				during := queryMCPSourceStateAcrossCompletion(t, ctx, pool, input, complete)
				if !reflect.DeepEqual(during, before) {
					t.Errorf("response mixed a concurrent completion into its snapshot:\nbefore=%+v\nduring=%+v", before, during)
				}
				after, err := QueryMCPReadSourceStates(ctx, pool, input)
				if err != nil {
					t.Fatalf("query after completion: %v", err)
				}
				if reflect.DeepEqual(after, before) {
					t.Error("fresh query did not observe the completed cycle")
				}
				latest, err := QueryMCPReadSourceStates(ctx, pool, MCPReadSourceStateQueryInput{
					SourceBindingID: fixture.sourceBindingID,
					Mode:            MCPReadSourceStateModeLatestObserved,
				})
				if err != nil {
					t.Fatalf("fresh latest query: %v", err)
				}
				wantCount := 1
				if observation == "repeated_revision" {
					wantCount = 3
				}
				if len(latest.States) != 1 || latest.States[0].SourceSnapshotID != snapshot ||
					latest.States[0].LastObservedCycle != 4 || !latest.States[0].LatestObserved ||
					latest.States[0].ObservationCount != wantCount {
					t.Fatalf("fresh latest state = %+v, want completed cycle 4 with count %d", latest, wantCount)
				}
				if afterCounts := boundedSourceViewIntegrationCounts(t, ctx, pool); afterCounts != beforeCounts {
					t.Fatalf("query/completion changed evidence counts: before=%+v after=%+v", beforeCounts, afterCounts)
				}
			})
		}
	}
}

// prepareMCPSourceStateConcurrentCompletion uses the existing source-state SQL
// fixture boundary. It tests a committed collection transition, not a provider
// process or Detective's source acquisition.
func prepareMCPSourceStateConcurrentCompletion(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	bindingID, snapshot, revision string,
) func() {
	t.Helper()
	cycleID := "detective-mcp-read-cycle:" + strings.Repeat("d", 64)
	deliveryID := "detective-connector-delivery:" + strings.Repeat("e", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_deliveries (
		  connector_delivery_id, connector_id, external_delivery_id,
		  content_type, payload_hash, payload_bytes, byte_length
		) VALUES ($1,'fixture-connector','snapshot-cycle-4','application/json',$2,$3,2)
	`, deliveryID, "sha256:"+strings.Repeat("e", 64), []byte("{}")); err != nil {
		t.Fatalf("insert concurrent delivery fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_mcp_read_collection_cycles (
		  cycle_id, workspace_id, source_binding_id, cycle_number,
		  binding_hash, collection_request_id, status
		)
		SELECT $1, workspace_id, source_binding_id, 4, binding_hash,
		       'snapshot-cycle-4', 'running'
		FROM detective_mcp_read_source_bindings WHERE source_binding_id = $2
	`, cycleID, bindingID); err != nil {
		t.Fatalf("insert running collection fixture: %v", err)
	}
	return func() {
		t.Helper()
		// Exec returns after this separate connection's autocommit completes.
		tag, err := pool.Exec(ctx, `
			UPDATE detective_mcp_read_collection_cycles
			SET status = 'completed', connector_delivery_id = $2, source_snapshot_id = $3,
			    provider_object_id = 'AHE-42', provider_revision = $4,
			    document_id = 'AHE-42-description', coverage_complete = true,
			    coverage_truncated = false, completion_reason = 'complete', finished_at = now()
			WHERE cycle_id = $1 AND status = 'running'
		`, cycleID, deliveryID, snapshot, revision)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("complete concurrent collection: rows=%d error=%v", tag.RowsAffected(), err)
		}
	}
}

func queryMCPSourceStateAcrossCompletion(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	input MCPReadSourceStateQueryInput, complete func(),
) MCPReadSourceStateQueryResult {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	barrier := &mcpSourceStateSnapshotBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	config := pool.Config()
	// The production query must enforce its own snapshot, even when the
	// connection default is Read Committed. Do not let a DB default mask the bug.
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	config.ConnConfig.Tracer = barrier
	reader, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create snapshot reader pool: %v", err)
	}
	defer reader.Close()
	type outcome struct {
		result MCPReadSourceStateQueryResult
		err    error
	}
	done := make(chan outcome, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		result, err := QueryMCPReadSourceStates(ctx, reader, input)
		done <- outcome{result: result, err: err}
	}()
	var release sync.Once
	resume := func() { release.Do(func() { close(barrier.release) }) }
	defer func() {
		cancel()
		resume()
		<-exited
	}()
	select {
	case <-barrier.reached:
	case result := <-done:
		t.Fatalf("reader finished before latest-query barrier: %+v", result)
	case <-ctx.Done():
		t.Fatalf("waiting for latest-query barrier: %v", ctx.Err())
	}
	complete()
	resume()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("query spanning collection completion: %v", result.err)
		}
		return result.result
	case <-ctx.Done():
		t.Fatalf("waiting for snapshot query: %v", ctx.Err())
		return MCPReadSourceStateQueryResult{}
	}
}

type mcpSourceStateSnapshotBarrier struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

type mcpSourceStateSnapshotBarrierKey struct{}

func (b *mcpSourceStateSnapshotBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM detective_mcp_read_collection_cycles AS cycle") &&
		len(data.Args) == 3 && data.Args[1] == "" && data.Args[2] == 1 {
		return context.WithValue(ctx, mcpSourceStateSnapshotBarrierKey{}, true)
	}
	return ctx
}

func (b *mcpSourceStateSnapshotBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil || ctx.Value(mcpSourceStateSnapshotBarrierKey{}) != true {
		return
	}
	// pgx invokes this after the rows have been consumed and closed, before
	// the reader can issue the next statement. No timing sleeps or runtime hook.
	b.once.Do(func() {
		close(b.reached)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
}
