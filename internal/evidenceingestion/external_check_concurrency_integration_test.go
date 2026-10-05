//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationExternalCheckReplayContention(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "replay-contention", "1")
	const writers = 16
	cfg := pool.Config()
	cfg.MaxConns = writers // The test must also work on hosts with fewer CPUs.
	contenders, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer contenders.Close()
	pool = contenders
	// Reuse already-open connections so connection startup cannot accidentally
	// serialize the first INSERTs and hide the competing unique-index checks.
	var conns []*pgxpool.Conn
	for range writers {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			for _, c := range conns {
				c.Release()
			}
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		conn.Release()
	}
	for round := range 32 {
		in := externalCheckFixture(subject, fmt.Sprintf("replay-round-%d", round))
		receipts, errs := concurrentExternalChecks(ctx, pool, repeatExternalCheck(in, writers))
		fresh := 0
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d writer %d: %v", round, i, err)
			}
			if !receipts[i].Replayed {
				fresh++
			}
			if !reflect.DeepEqual(receipts[i].Record, receipts[0].Record) || !reflect.DeepEqual(receipts[i].Record.Report, in) {
				t.Fatalf("round %d returned a different historical receipt", round)
			}
		}
		if fresh != 1 {
			t.Fatalf("round %d fresh receipts=%d, want 1", round, fresh)
		}
		assertTableCount(t, ctx, pool, "external_check_records", round+1)
	}
}

func TestIntegrationExternalCheckSessionIsolation(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "session-isolation", "1")
	for i, isolation := range []string{"repeatable read", "serializable"} {
		t.Run(isolation, func(t *testing.T) {
			cfg := pool.Config()
			cfg.MaxConns = 16
			cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = isolation
			other, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			var actual string
			if err := other.QueryRow(ctx, `SHOW default_transaction_isolation`).Scan(&actual); err != nil || actual != isolation {
				t.Fatal("session default not applied", err, actual)
			}
			for round := range 8 {
				input := externalCheckFixture(subject, fmt.Sprintf("session-%d-round-%d", i, round))
				receipts, errs := concurrentExternalChecks(ctx, other, repeatExternalCheck(input, 16))
				fresh := 0
				for n, err := range errs {
					if err != nil {
						t.Fatal("session default broke exact replay", err)
					}
					if !receipts[n].Replayed {
						fresh++
					}
					if !reflect.DeepEqual(receipts[n].Record, receipts[0].Record) {
						t.Fatal("replay receipt changed")
					}
				}
				if fresh != 1 {
					t.Fatalf("fresh=%d, want 1", fresh)
				}
			}
		})
	}
	assertTableCount(t, ctx, pool, "external_check_records", 16)
}

func concurrentExternalChecks(ctx context.Context, pool *pgxpool.Pool, inputs []ExternalCheckInput) ([]ExternalCheckReceipt, []error) {
	start := make(chan struct{})
	receipts, errs := make([]ExternalCheckReceipt, len(inputs)), make([]error, len(inputs))
	var wg sync.WaitGroup
	for i, in := range inputs {
		wg.Go(func() {
			<-start
			receipts[i], errs[i] = RecordExternalCheck(ctx, pool, in)
		})
	}
	close(start)
	wg.Wait()
	return receipts, errs
}

func repeatExternalCheck(in ExternalCheckInput, n int) []ExternalCheckInput {
	inputs := make([]ExternalCheckInput, n)
	for i := range inputs {
		inputs[i] = in
	}
	return inputs
}

func TestIntegrationExternalCheckConcurrentIdentity(t *testing.T) {
	ctx, pool := integrationPool(t)
	subject := externalCheckTestSubject(t, ctx, pool, "concurrent-identity", "1")
	t.Run("different_content_same_request_is_conflict", func(t *testing.T) {
		inputs := repeatExternalCheck(externalCheckFixture(subject, "competing-bodies"), 16)
		for i := range inputs {
			inputs[i].CheckerVersion = fmt.Sprint(i)
		}
		receipts, errs := concurrentExternalChecks(ctx, pool, inputs)
		winner := -1
		for i, err := range errs {
			if err == nil {
				if winner != -1 || receipts[i].Replayed {
					t.Fatal("more than one body accepted or aliased as replay")
				}
				winner = i
				continue
			}
			var domain *DomainError
			if !errors.As(err, &domain) || domain.Kind != ErrorIdempotencyKeyReused {
				t.Fatalf("writer %d: expected request conflict, got %v", i, err)
			}
		}
		if winner < 0 {
			t.Fatal("no body accepted")
		}
		replay, err := RecordExternalCheck(ctx, pool, inputs[winner])
		if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Record, receipts[winner].Record) {
			t.Fatal("winner no longer replays", err)
		}
		assertTableCount(t, ctx, pool, "external_check_records", 1)
	})
	t.Run("different_requests_remain_distinct", func(t *testing.T) {
		inputs := repeatExternalCheck(externalCheckFixture(subject, "unused"), 16)
		for i := range inputs {
			inputs[i].RequestID = fmt.Sprintf("independent-%d", i)
		}
		receipts, errs := concurrentExternalChecks(ctx, pool, inputs)
		seen := map[string]bool{}
		for i, err := range errs {
			if err != nil || receipts[i].Replayed || seen[receipts[i].Record.ID] {
				t.Fatalf("distinct request collapsed: %v", err)
			}
			seen[receipts[i].Record.ID] = true
			if !reflect.DeepEqual(receipts[i].Record.Report, inputs[i]) {
				t.Fatal("wrong request receipt")
			}
		}
		assertTableCount(t, ctx, pool, "external_check_records", 17)
	})
	t.Run("cancelled_request_does_not_report_success", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		receipt, err := RecordExternalCheck(cancelled, pool, externalCheckFixture(subject, "cancelled"))
		if !errors.Is(err, context.Canceled) || receipt.Record.ID != "" || receipt.Replayed {
			t.Fatalf("cancelled write returned success: %+v %v", receipt, err)
		}
		assertTableCount(t, ctx, pool, "external_check_records", 17)
	})
	t.Run("database_validation_and_unique_constraints_still_apply", func(t *testing.T) {
		in := externalCheckFixture(subject, "guarded-replay")
		first, err := RecordExternalCheck(ctx, pool, in)
		if err != nil {
			t.Fatal(err)
		}
		invalid := in
		invalid.Subject.RawContentHash = "wrong"
		if _, err := RecordExternalCheck(ctx, pool, invalid); err == nil {
			t.Fatal("bad database subject turned into replay")
		}
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO external_check_records(check_id,request_id,proposal_occurrence_id,body) VALUES($1,$2,$3,$4)`, first.Record.ID, in.RequestID, subject.ProposalOccurrenceID, string(body))
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23505" {
			t.Fatalf("direct duplicate did not fail uniqueness: %v", err)
		}
		again, err := ReadExternalCheck(ctx, pool, first.Record.ID)
		if err != nil || !reflect.DeepEqual(again, first.Record) {
			t.Fatal("failed writes changed history", err)
		}
		assertTableCount(t, ctx, pool, "external_check_records", 18)
	})
}
