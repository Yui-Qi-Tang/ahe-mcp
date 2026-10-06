//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRepositoryExtractorDefinitionConcurrentReuse(t *testing.T) {
	const writers, rounds = 16, 40
	for _, sharedRequest := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared-request-%t", sharedRequest), func(t *testing.T) {
			ctx, pool := integrationPool(t)
			root := repositoryDeltaGitFixture(t, map[string]string{"main.go": "package sample\n\nfunc Main() {}\n"})
			snapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "definition-race", "definition-race-snapshot")
			cfg := pool.Config()
			cfg.MaxConns = writers
			cfg.ConnConfig.Tracer = repositoryDefinitionInsertTracer{}
			contenders, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer contenders.Close()
			perRound := writers
			if sharedRequest {
				perRound = 1
			}
			for round := range rounds {
				definitionInput := repositoryGoParserExtractorDefinition()
				definitionInput.Name = fmt.Sprintf("repository-cold-definition-%d", round)
				definition, err := buildExtractorDefinition(definitionInput)
				if err != nil {
					t.Fatal(err)
				}
				request := RepositoryExtractionRunRequest{RequestID: fmt.Sprintf("definition-request-%d", round), RepositorySnapshotID: snapshot.RepositorySnapshot.ID, ExtractorDefinition: definitionInput}
				results := repositoryDefinitionConcurrentRound(t, ctx, contenders, request, writers, sharedRequest)
				seen := map[string]bool{}
				fresh := 0
				for i, result := range results {
					run := result.ExtractionRun
					wantRequest := request.RequestID
					if !sharedRequest {
						wantRequest += fmt.Sprintf("-%d", i)
					}
					if run.ID == "" || run.RequestID != wantRequest || run.ExtractorDefinitionID != definition.ID || run.RepositorySnapshotID != request.RepositorySnapshotID {
						t.Fatalf("round %d writer %d returned wrong identity: %+v", round, i, result)
					}
					seen[run.ID] = true
					if !result.Replayed {
						fresh++
					}
					var storedRunID, storedDefinitionID, storedSnapshotID string
					if err := pool.QueryRow(ctx, `SELECT r.extraction_run_id,r.extractor_definition_id,r.repository_snapshot_id FROM repository_extraction_run_requests q JOIN extraction_runs r USING(extraction_run_id) WHERE q.request_id=$1`, wantRequest).Scan(&storedRunID, &storedDefinitionID, &storedSnapshotID); err != nil {
						t.Fatal(err)
					}
					if storedRunID != run.ID || storedDefinitionID != definition.ID || storedSnapshotID != request.RepositorySnapshotID {
						t.Fatal("returned identity differs from persisted request")
					}
				}
				if len(seen) != perRound || fresh != perRound {
					t.Fatalf("round %d: distinct runs=%d fresh=%d, want %d", round, len(seen), fresh, perRound)
				}
				if !sharedRequest {
					request.RequestID += "-0"
				}
				replay, err := CreateRepositoryExtractionRun(ctx, pool, request)
				if err != nil || !replay.Replayed || replay.ExtractionRun.ID != results[0].ExtractionRun.ID {
					t.Fatalf("round %d replay: %+v %v", round, replay, err)
				}
				assertTableCount(t, ctx, pool, "extractor_definitions", round+1)
				assertTableCount(t, ctx, pool, "extraction_runs", (round+1)*perRound)
				assertTableCount(t, ctx, pool, "repository_extraction_run_requests", (round+1)*perRound)
			}
			assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
		})
	}
}

// Synchronize at the real INSERT, after connection acquisition.
// A goroutine-start barrier alone may miss the first-registration race.
func repositoryDefinitionConcurrentRound(t *testing.T, ctx context.Context, pool *pgxpool.Pool, request RepositoryExtractionRunRequest, writers int, sharedRequest bool) []RepositoryExtractionRunResult {
	t.Helper()
	barrier := &repositoryDefinitionInsertBarrier{arrived: make(chan repositoryDefinitionInsertArrival, writers), release: make(chan struct{})}
	callCtx, cancel := context.WithCancel(context.WithValue(ctx, repositoryDefinitionInsertBarrierKey{}, barrier))
	var workers sync.WaitGroup
	var release sync.Once
	defer func() {
		cancel()
		release.Do(func() { close(barrier.release) })
		workers.Wait()
	}()
	results, errs := make([]RepositoryExtractionRunResult, writers), make([]error, writers)
	for i := range writers {
		workers.Go(func() {
			input := request
			if !sharedRequest {
				input.RequestID += fmt.Sprintf("-%d", i)
			}
			results[i], errs[i] = CreateRepositoryExtractionRun(callCtx, pool, input)
		})
	}
	pids := map[uint32]bool{}
	var parameters []any
	for range writers {
		select {
		case arrival := <-barrier.arrived:
			pids[arrival.pid] = true
			if parameters == nil {
				parameters = arrival.parameters
			} else if !reflect.DeepEqual(parameters, arrival.parameters) {
				t.Fatal("writers submitted different extractor definitions")
			}
		case <-ctx.Done():
			t.Fatalf("writers did not reach definition INSERT: %v", ctx.Err())
		}
	}
	if len(pids) != writers {
		t.Fatal("writers were not using independent database connections")
	}
	release.Do(func() { close(barrier.release) })
	workers.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}
	return results
}

type repositoryDefinitionInsertBarrierKey struct{}
type repositoryDefinitionInsertArrival struct {
	pid        uint32
	parameters []any
}
type repositoryDefinitionInsertBarrier struct {
	arrived chan repositoryDefinitionInsertArrival
	release chan struct{}
}
type repositoryDefinitionInsertTracer struct{}

func (repositoryDefinitionInsertTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	barrier, ok := ctx.Value(repositoryDefinitionInsertBarrierKey{}).(*repositoryDefinitionInsertBarrier)
	if !ok || !strings.Contains(data.SQL, "INSERT INTO extractor_definitions") {
		return ctx
	}
	select {
	case barrier.arrived <- repositoryDefinitionInsertArrival{pid: conn.PgConn().PID(), parameters: append([]any(nil), data.Args...)}:
	case <-ctx.Done():
		return ctx
	}
	select {
	case <-barrier.release:
	case <-ctx.Done():
	}
	return ctx
}

func (repositoryDefinitionInsertTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestIntegrationRepositoryExtractorDefinitionConflictDoesNotCreateRun(t *testing.T) {
	ctx, pool := integrationPool(t)
	root := repositoryDeltaGitFixture(t, map[string]string{"main.go": "package sample\n"})
	snapshot := captureRepositoryDeltaSnapshot(t, ctx, pool, root, "definition-conflict", "definition-conflict-snapshot")
	input := repositoryGoParserExtractorDefinition()
	request := RepositoryExtractionRunRequest{RequestID: "definition-conflict-request", RepositorySnapshotID: snapshot.RepositorySnapshot.ID, ExtractorDefinition: input}
	first, err := CreateRepositoryExtractionRun(ctx, pool, request)
	if err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.ExtractorDefinition.Version += "-changed"
	_, err = CreateRepositoryExtractionRun(ctx, pool, changed)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	for _, table := range []string{"extractor_definitions", "extraction_runs", "repository_extraction_run_requests"} {
		assertTableCount(t, ctx, pool, table, 1)
	}
	replay, err := CreateRepositoryExtractionRun(ctx, pool, request)
	if err != nil || !replay.Replayed || replay.ExtractionRun.ID != first.ExtractionRun.ID {
		t.Fatalf("rejected reuse damaged original run: %+v %v", replay, err)
	}

	// A preexisting natural identity with an incompatible ID must not become a
	// successful run merely because DO NOTHING covers its unique constraint.
	input.Name += "-incompatible"
	definition, err := buildExtractorDefinition(input)
	if err != nil {
		t.Fatal(err)
	}
	config, err := jsonBytes(definition.Config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO extractor_definitions (extractor_definition_id,extractor_name,extractor_version,extractor_config_hash,extractor_config) VALUES($1,$2,$3,$4,$5::jsonb)`, definition.ID+"-legacy", definition.Name, definition.Version, definition.ConfigHash, string(config)); err != nil {
		t.Fatal(err)
	}
	assertStoredDefinition := func() {
		t.Helper()
		var id, storedConfig string
		if err := pool.QueryRow(ctx, `SELECT extractor_definition_id,extractor_config::text FROM extractor_definitions WHERE extractor_name=$1 AND extractor_version=$2 AND extractor_config_hash=$3`, definition.Name, definition.Version, definition.ConfigHash).Scan(&id, &storedConfig); err != nil {
			t.Fatal(err)
		}
		var configMatches, normalIDExists bool
		if err := pool.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb, EXISTS(SELECT 1 FROM extractor_definitions WHERE extractor_definition_id=$3)`, storedConfig, string(config), definition.ID).Scan(&configMatches, &normalIDExists); err != nil {
			t.Fatal(err)
		}
		if id != definition.ID+"-legacy" || !configMatches || normalIDExists {
			t.Fatal("incompatible stored definition changed or normal ID unexpectedly exists")
		}
	}
	assertStoredDefinition()
	request.RequestID += "-incompatible"
	request.ExtractorDefinition = input
	_, err = CreateRepositoryExtractionRun(ctx, pool, request)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "extraction_runs_extractor_definition_id_fkey" {
		t.Fatalf("incompatible stored definition was accepted or failed for the wrong reason: %v", err)
	}
	assertStoredDefinition()
	assertTableCount(t, ctx, pool, "extractor_definitions", 2)
	assertTableCount(t, ctx, pool, "extraction_runs", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_run_requests", 1)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 0)
}
