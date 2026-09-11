//go:build integration

package detective

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationReceiveConnectorDeliveryControlledParallel(t *testing.T) {
	for _, mode := range []string{
		"exact_delivery", "changed_payload", "changed_content_type",
		"same_request_exact", "same_request_changed_payload", "first_rollback",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx, pool := detectiveIntegrationPool(t)
			firstPool, firstPID := connectorParallelPool(t, ctx, pool)
			secondPool, secondPID := connectorParallelPool(t, ctx, pool)
			installConnectorParallelIndexGate(t, ctx, pool, firstPID)

			controller, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Release()
			var controllerPID uint32
			if err := controller.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&controllerPID); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.Exec(ctx, `SELECT pg_advisory_lock(184726381, $1)`, int32(firstPID)); err != nil {
				t.Fatal(err)
			}
			callCtx, cancelCalls := context.WithCancel(ctx)
			firstCtx, cancelFirst := context.WithCancel(callCtx)
			var workers sync.WaitGroup
			defer func() {
				cancelFirst()
				cancelCalls()
				cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancelCleanup()
				// Closing this dedicated controller releases its session lock even
				// when an assertion fails before the normal explicit unlock.
				_ = controller.Conn().Close(cleanupCtx)
				workers.Wait()
			}()

			firstInput := ConnectorDeliveryInput{
				RequestID: "controlled-first", ConnectorID: "controlled-connector",
				ExternalDeliveryID: "controlled-delivery", ContentType: ConnectorDeliveryContentTypeText,
				Payload: []byte("controlled exact bytes"),
			}
			secondInput := firstInput
			secondInput.RequestID = "controlled-second"
			switch mode {
			case "changed_payload":
				secondInput.Payload = []byte("controlled other bytes")
			case "changed_content_type":
				secondInput.ContentType = ConnectorDeliveryContentTypeJSON
			case "same_request_exact":
				secondInput.RequestID = firstInput.RequestID
			case "same_request_changed_payload":
				secondInput.RequestID = firstInput.RequestID
				secondInput.Payload = []byte("controlled other bytes")
			}
			var ackCalls atomic.Int32
			run := func(callCtx context.Context, callPool *pgxpool.Pool, input ConnectorDeliveryInput, done chan<- connectorParallelResult) {
				calls := 0
				receipt, err := ReceiveConnectorDeliveryAndAcknowledge(callCtx, callPool, input, func(ackCtx context.Context, receipt ConnectorDeliveryReceipt) error {
					calls++
					ackCalls.Add(1)
					var payload []byte
					var requestPresent, workPresent bool
					err := pool.QueryRow(ackCtx, `
						SELECT payload_bytes,
							EXISTS (SELECT 1 FROM detective_connector_inbox_receive_requests
								WHERE request_id=$2 AND connector_delivery_id=$1),
							EXISTS (SELECT 1 FROM detective_connector_inbox_processing_work
								WHERE connector_delivery_id=$1)
						FROM detective_connector_inbox_deliveries WHERE connector_delivery_id=$1
					`, receipt.ConnectorDeliveryID, input.RequestID).Scan(&payload, &requestPresent, &workPresent)
					if err != nil {
						return fmt.Errorf("acknowledgement readback: %w", err)
					}
					if !bytes.Equal(payload, input.Payload) || !requestPresent || !workPresent {
						return fmt.Errorf("acknowledgement did not observe committed exact delivery, request, and processing work")
					}
					return nil
				})
				done <- connectorParallelResult{receipt: receipt, err: err, ackCalls: calls}
			}
			firstDone, secondDone := make(chan connectorParallelResult, 1), make(chan connectorParallelResult, 1)
			workers.Go(func() { run(firstCtx, firstPool, firstInput, firstDone) })
			waitConnectorParallelBlock(t, ctx, pool, firstPID, controllerPID, []string{"advisory"}, firstDone)
			workers.Go(func() { run(callCtx, secondPool, secondInput, secondDone) })
			waitEvents := []string{"spectoken", "transactionid"}
			waitConnectorParallelBlock(t, ctx, pool, secondPID, firstPID, waitEvents, secondDone)
			if calls := ackCalls.Load(); calls != 0 {
				t.Fatalf("acknowledgements before either transaction committed = %d", calls)
			}
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 0)
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", 0)
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_work", 0)

			if mode == "first_rollback" {
				cancelFirst()
			} else {
				var unlocked bool
				if err := controller.QueryRow(ctx, `SELECT pg_advisory_unlock(184726381, $1)`, int32(firstPID)).Scan(&unlocked); err != nil || !unlocked {
					t.Fatalf("release first index writer: unlocked=%v error=%v", unlocked, err)
				}
			}
			first := receiveConnectorParallelResult(t, ctx, firstDone)
			second := receiveConnectorParallelResult(t, ctx, secondDone)
			wantRequests := 1
			if mode == "first_rollback" {
				if first.err == nil || first.ackCalls != 0 {
					t.Fatalf("canceled first receipt = %+v, error=%v", first, first.err)
				}
				if second.err != nil || second.ackCalls != 1 || !second.receipt.DeliveryCreated || second.receipt.Replayed {
					t.Fatalf("receipt after first rollback = %+v, error=%v", second, second.err)
				}
				var canceledRequest bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM detective_connector_inbox_receive_requests WHERE request_id=$1)`, firstInput.RequestID).Scan(&canceledRequest); err != nil || canceledRequest {
					t.Fatalf("canceled request persisted=%v error=%v", canceledRequest, err)
				}
			} else {
				if first.err != nil || first.ackCalls != 1 || !first.receipt.DeliveryCreated || first.receipt.Replayed {
					t.Fatalf("first receipt = %+v, error=%v", first, first.err)
				}
				switch mode {
				case "exact_delivery", "same_request_exact":
					replayed := mode == "same_request_exact"
					if second.err != nil || second.ackCalls != 1 || second.receipt.DeliveryCreated != replayed || second.receipt.Replayed != replayed ||
						second.receipt.ConnectorDeliveryID != first.receipt.ConnectorDeliveryID || !second.receipt.ReceivedAt.Equal(first.receipt.ReceivedAt) {
						t.Fatalf("second exact receipt = %+v, error=%v, first=%+v", second, second.err, first.receipt)
					}
					if !replayed {
						wantRequests = 2
					}
				case "changed_payload", "changed_content_type":
					assertDetectiveKind(t, second.err, ErrorConnectorDeliveryConflict)
				case "same_request_changed_payload":
					assertDetectiveKind(t, second.err, ErrorIdempotencyKeyReused)
				}
				if second.err != nil && second.ackCalls != 0 {
					t.Fatalf("failed second receipt acknowledged %d times", second.ackCalls)
				}
			}
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", wantRequests)
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_work", 1)
		})
	}
}

type connectorParallelResult struct {
	receipt  ConnectorDeliveryReceipt
	err      error
	ackCalls int
}

func connectorParallelPool(t *testing.T, ctx context.Context, source *pgxpool.Pool) (*pgxpool.Pool, uint32) {
	t.Helper()
	config := source.Config()
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var pid uint32
	if err := pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pool, pid
}

// installConnectorParallelIndexGate changes only this test's disposable schema.
// The original primary/composite unique indexes stay in place. Recreating the
// identical natural-key constraint after a nonunique index opens a controlled
// interval where a speculative primary-key entry exists before its natural-key
// entry. The gate returns the original value; its deliberately false IMMUTABLE
// declaration permits an advisory-lock side effect ONLY for this test index.
// No migration, production trigger, or runtime hook uses this mechanism.
func installConnectorParallelIndexGate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, firstPID uint32) {
	t.Helper()
	var constraintName, constraintDefinition string
	if err := pool.QueryRow(ctx, `
		SELECT conname, pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid='detective_connector_inbox_deliveries'::regclass
			AND contype='u' AND conkey=ARRAY[2,3]::smallint[]
	`).Scan(&constraintName, &constraintDefinition); err != nil {
		t.Fatal(err)
	}
	if constraintDefinition != "UNIQUE (connector_id, external_delivery_id)" {
		t.Fatalf("unexpected natural-key constraint: %q", constraintDefinition)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE detective_connector_inbox_deliveries DROP CONSTRAINT `+pgx.Identifier{constraintName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	gateSQL := fmt.Sprintf(`
		CREATE FUNCTION connector_parallel_index_gate(value text) RETURNS text
		LANGUAGE plpgsql IMMUTABLE STRICT AS $$
		BEGIN
			IF pg_backend_pid() = %d THEN
				PERFORM pg_advisory_xact_lock(184726381, %d);
			END IF;
			RETURN value;
		END
		$$;
		CREATE INDEX connector_parallel_gate_idx ON detective_connector_inbox_deliveries
			(connector_parallel_index_gate(connector_delivery_id));
	`, firstPID, firstPID)
	if _, err := pool.Exec(ctx, gateSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE detective_connector_inbox_deliveries ADD CONSTRAINT `+pgx.Identifier{constraintName}.Sanitize()+` UNIQUE (connector_id, external_delivery_id)`); err != nil {
		t.Fatal(err)
	}
	var ordered, naturalValid bool
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT indexrelid FROM pg_index WHERE indrelid='detective_connector_inbox_deliveries'::regclass AND indisprimary)
				< 'connector_parallel_gate_idx'::regclass::oid
				AND 'connector_parallel_gate_idx'::regclass::oid < c.conindid,
			c.conkey=ARRAY[2,3]::smallint[] AND c.contype='u'
				AND i.indisunique AND i.indisvalid AND i.indimmediate
		FROM pg_constraint c JOIN pg_index i ON i.indexrelid=c.conindid
		WHERE c.conrelid='detective_connector_inbox_deliveries'::regclass AND c.conname=$1
	`, constraintName).Scan(&ordered, &naturalValid); err != nil || !ordered || !naturalValid {
		t.Fatalf("controlled index order=%v identical natural constraint=%v error=%v", ordered, naturalValid, err)
	}
}

func waitConnectorParallelBlock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, waiter, blocker uint32, events []string, done <-chan connectorParallelResult) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(waitCtx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1
				AND wait_event_type='Lock' AND wait_event=ANY($3::text[])
				AND $2::integer=ANY(pg_blocking_pids(pid)))
		`, waiter, blocker, events).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case result := <-done:
			t.Fatalf("writer exited before expected database lock wait: %+v, error=%v", result, result.err)
		case <-waitCtx.Done():
			t.Fatalf("writer %d never waited on writer %d with events %v", waiter, blocker, events)
		case <-ticker.C:
		}
	}
}

func receiveConnectorParallelResult(t *testing.T, ctx context.Context, done <-chan connectorParallelResult) connectorParallelResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return connectorParallelResult{}
	}
}
