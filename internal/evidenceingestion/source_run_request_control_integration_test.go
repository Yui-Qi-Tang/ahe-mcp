//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationSourceExtractionRequestSessionAnnotationRemainsFirstCreated(t *testing.T) {
	for _, firstSession := range []string{"", "session:first"} {
		t.Run("first_"+firstSession, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, fixture := integrationInputFixture(t, "session-annotation-source")
			source, err := CaptureManualSource(ctx, pool, input)
			if err != nil {
				t.Fatal(err)
			}
			request := ExtractorOutputInput{
				RequestID: "session-annotation-request", SourceSnapshotID: source.SourceSnapshotID,
				ExtractionViewID: source.ExtractionViewID, ExtractorDefinition: testExternalExtractorDefinition(),
				ProducerSessionRef: firstSession, Output: fixture,
			}
			first, err := SubmitExtractorOutput(ctx, pool, request)
			if err != nil {
				t.Fatal(err)
			}
			for _, session := range []string{"session:retry", "", "  session:first  "} {
				request.ProducerSessionRef = session
				replay, err := SubmitExtractorOutput(ctx, pool, request)
				if err != nil || !replay.Replayed || replay.ExtractionRunID != first.ExtractionRunID || replay.ProposalOccurrenceID != first.ProposalOccurrenceID {
					t.Fatalf("annotation-only retry: result=%+v error=%v", replay, err)
				}
			}
			trace, err := TraceProposalProvenance(ctx, pool, first.ProposalOccurrenceID)
			if err != nil || trace.ProducerSessionRef != firstSession || trace.AdmissionOutcome != admissionOutcomePending {
				t.Fatalf("first-created annotation was replaced: trace=%+v error=%v", trace, err)
			}
			for _, table := range []string{"extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
				assertTableCount(t, ctx, pool, table, 1)
			}
			assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
			assertTableCount(t, ctx, pool, "admission_decisions", 0)
		})
	}
}

func TestIntegrationSourceExtractionRequestParallelIdentity(t *testing.T) {
	for _, secondInput := range []string{"same", "source", "extractor_definition"} {
		t.Run(secondInput, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, fixture := integrationInputFixture(t, "parallel-request-source")
			source, err := CaptureManualSource(ctx, pool, input)
			if err != nil {
				t.Fatal(err)
			}
			first := ExtractorOutputInput{
				RequestID: "parallel-source-extraction-request", SourceSnapshotID: source.SourceSnapshotID,
				ExtractionViewID: source.ExtractionViewID, ExtractorDefinition: testExternalExtractorDefinition(),
				ProducerSessionRef: "session:first-insert", Output: fixture,
			}
			second := first
			second.ProducerSessionRef = "session:second-caller"
			switch secondInput {
			case "source":
				input.RequestID += "-second"
				input.SourceID += "-second-object"
				otherSource, err := CaptureManualSource(ctx, pool, input)
				if err != nil {
					t.Fatal(err)
				}
				second.SourceSnapshotID = otherSource.SourceSnapshotID
				second.ExtractionViewID = otherSource.ExtractionViewID
			case "extractor_definition":
				second.ExtractorDefinition.Version += "-second"
			}
			// Precommit both exact definitions, so the wait being measured is on
			// extraction_runs rather than a definition's speculative insertion.
			for _, request := range []ExtractorOutputInput{first, second} {
				definition, err := buildExtractorDefinition(request.ExtractorDefinition)
				if err != nil {
					t.Fatal(err)
				}
				config, err := jsonBytes(definition.Config)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO extractor_definitions
					(extractor_definition_id, extractor_name, extractor_version, extractor_config_hash, extractor_config)
					VALUES ($1,$2,$3,$4,$5::jsonb) ON CONFLICT DO NOTHING`,
					definition.ID, definition.Name, definition.Version, definition.ConfigHash, string(config)); err != nil {
					t.Fatal(err)
				}
			}
			firstResult, secondResult, blocked := sourceRunRequestsAcrossUncommittedInsert(t, ctx, pool, first, second)
			if firstResult.err != nil {
				t.Fatalf("first caller: %v", firstResult.err)
			}
			if secondInput == "same" {
				if secondResult.err != nil || firstResult.result.ExtractionRunID != secondResult.result.ExtractionRunID || firstResult.result.ProposalOccurrenceID != secondResult.result.ProposalOccurrenceID {
					t.Fatalf("same-input parallel result: first=%+v second=%+v", firstResult, secondResult)
				}
				if firstResult.result.Replayed == secondResult.result.Replayed {
					t.Errorf("same-input calls must have one fresh completion and one replay: first=%+v second=%+v", firstResult.result, secondResult.result)
				}
			} else {
				var domainErr *DomainError
				if !errors.As(secondResult.err, &domainErr) || domainErr.Kind != ErrorIdempotencyKeyReused {
					t.Errorf("changed %s created a second run or returned a non-domain conflict: result=%+v error=%v", secondInput, secondResult.result, secondResult.err)
				}
			}
			if !blocked {
				t.Error("second extraction_runs INSERT did not wait for the first request transaction")
			}
			var session string
			if err := pool.QueryRow(ctx, `SELECT producer_session_ref FROM extraction_runs WHERE extraction_run_id=$1`, firstResult.result.ExtractionRunID).Scan(&session); err != nil || session != first.ProducerSessionRef {
				t.Fatalf("first-insert session annotation = %q, error=%v", session, err)
			}
			for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions"} {
				assertTableCount(t, ctx, pool, table, 0)
			}
			for _, table := range []string{"extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
				assertTableCount(t, ctx, pool, table, 1)
			}
		})
	}
}

type sourceRunRequestResult struct {
	result IngestResult
	err    error
}

func sourceRunRequestsAcrossUncommittedInsert(t *testing.T, ctx context.Context, pool *pgxpool.Pool, first, second ExtractorOutputInput) (sourceRunRequestResult, sourceRunRequestResult, bool) {
	t.Helper()
	barrier := &sourceRunInsertBarrier{reached: make(chan struct{}), release: make(chan struct{}, 1)}
	firstPool := sourceRunRequestPool(t, ctx, pool, barrier)
	secondPool := sourceRunRequestPool(t, ctx, pool, nil)
	var secondPID uint32
	if err := secondPool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondPID); err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	// Unblock the tracer and join every public writer before pool cleanup,
	// including failures while the first transaction still holds its locks.
	defer func() {
		cancel()
		close(barrier.release)
		workers.Wait()
	}()
	firstDone := make(chan sourceRunRequestResult, 1)
	secondDone := make(chan sourceRunRequestResult, 1)
	workers.Go(func() {
		result, err := SubmitExtractorOutput(callCtx, firstPool, first)
		firstDone <- sourceRunRequestResult{result: result, err: err}
	})
	select {
	case <-barrier.reached:
	case result := <-firstDone:
		t.Fatalf("first caller finished before its run-insert barrier: %+v", result)
	case <-ctx.Done():
		t.Fatalf("waiting for first run insert: %v", ctx.Err())
	}
	workers.Go(func() {
		result, err := SubmitExtractorOutput(callCtx, secondPool, second)
		secondDone <- sourceRunRequestResult{result: result, err: err}
	})
	secondResult, completed, blocked := observeSourceRunRequestWait(t, ctx, pool, secondPID, secondDone)
	// A buffered permit releases the successful INSERT before COMMIT. It
	// remains separate from cleanup's channel close to avoid double closing.
	barrier.release <- struct{}{}
	var firstResult sourceRunRequestResult
	select {
	case firstResult = <-firstDone:
	case <-ctx.Done():
		t.Fatalf("waiting for first source extraction: %v", ctx.Err())
	}
	if !completed {
		select {
		case secondResult = <-secondDone:
		case <-ctx.Done():
			t.Fatalf("waiting for second source extraction: %v", ctx.Err())
		}
	}
	return firstResult, secondResult, blocked
}

func sourceRunRequestPool(t *testing.T, ctx context.Context, source *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	config := source.Config()
	config.MaxConns = 1
	config.ConnConfig.Tracer = tracer
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func observeSourceRunRequestWait(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pid uint32, completed <-chan sourceRunRequestResult) (sourceRunRequestResult, bool, bool) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(waitCtx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock'
			  AND query LIKE '%INSERT INTO extraction_runs%'
			  AND cardinality(pg_blocking_pids(pid)) > 0
		)`, pid).Scan(&blocked); err != nil {
			t.Fatalf("observing source request unique-index wait: %v", err)
		}
		if blocked {
			return sourceRunRequestResult{}, false, true
		}
		select {
		case result := <-completed:
			// The old implementation can complete changed input while the
			// first same-request run is still uncommitted. Return its real
			// result so the test records the second successful run as well.
			return result, true, false
		case <-waitCtx.Done():
			t.Fatalf("second source request neither completed nor waited: %v", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

type sourceRunInsertBarrier struct {
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

type sourceRunInsertBarrierContextKey struct{}

func (b *sourceRunInsertBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "INSERT INTO extraction_runs") {
		return context.WithValue(ctx, sourceRunInsertBarrierContextKey{}, true)
	}
	return ctx
}

func (b *sourceRunInsertBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if marked, _ := ctx.Value(sourceRunInsertBarrierContextKey{}).(bool); !marked || data.Err != nil {
		return
	}
	b.once.Do(func() {
		close(b.reached)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
}
