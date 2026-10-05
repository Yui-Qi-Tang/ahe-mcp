//go:build integration && consistencylab

package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The wrapper pauses a real pinned solver; it never fabricates solver output.
func consistencyPausedRunner(t *testing.T) (*cadical.Runner, string, string) {
	t.Helper()
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	tool := filepath.Join(dir, "solver")
	raw := []byte("#!/bin/sh\ntouch " + quote(started) + "\nwhile [ ! -f " + quote(release) + " ]; do sleep 0.01; done\nexec " + quote(os.Getenv("AHE_CONSISTENCY_CADICAL")) + " \"$@\"\n")
	if err := os.WriteFile(tool, raw, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	runner, err := cadical.New(cadical.Config{SolverPath: tool, SolverSHA256: hex.EncodeToString(sum[:]), CheckerPath: os.Getenv("AHE_CONSISTENCY_DRAT"), CheckerSHA256: "6dfac4c795691b620c0d8adf359f67232e65e27782c9cd88e4d4d4d28342dc62", ArtifactDir: filepath.Join(os.Getenv("AHE_CONSISTENCY_REPORT_DIR"), "interleaving-"+randomHex(t, 8))})
	if err != nil {
		t.Fatal(err)
	}
	return runner, started, release
}

func TestIntegrationConsistencyWorkerInterleaving(t *testing.T) {
	for _, name := range []string{"source_changes_during_solve", "configuration_changes_during_solve", "pause_changes_during_solve", "lost_connection_cannot_publish"} {
		disconnect := name == "lost_connection_cannot_publish"
		t.Run(name, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			runner, started, release := consistencyPausedRunner(t)
			scope := ConsistencyScope{Namespace: "interleaving", ScopeRef: "scope"}
			a, _ := consistencyClaim(t, ctx, pool, scope, "a")
			b, kb := consistencyClaim(t, ctx, pool, scope, "b")
			lit := consistencyLiteral("s", "p", "c", false)
			neg := lit
			neg.Negated = true
			in := ConsistencyWatchInput{WatchID: "interleaving", RequestID: "configuration", EngineID: runner.Identity(), Request: ConsistencyRequest{Scope: scope, RuleVersion: "1", Evidence: []ConsistencyCondition{{ID: a, Clauses: [][]ConsistencyLiteral{{lit}}}, {ID: b, Clauses: [][]ConsistencyLiteral{{neg}}}}}}
			if _, err := RegisterConsistencyWatch(ctx, pool, in); err != nil {
				t.Fatal(err)
			}
			cfg := pool.Config().Copy()
			cfg.MaxConns = 1
			label := "consistency-interleave-" + randomHex(t, 8)
			cfg.ConnConfig.RuntimeParams["application_name"] = label
			workerPool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer workerPool.Close()
			worker := ConsistencyWorker{Pool: workerPool, Runner: runner}
			type reply struct {
				work []ConsistencyWork
				err  error
			}
			done := make(chan reply, 1)
			workerCtx, cancelWorker := context.WithCancel(ctx)
			finished := false
			defer func() {
				cancelWorker()
				_ = os.WriteFile(release, nil, 0600)
				if !finished {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("worker did not stop after test cancellation")
					}
				}
			}()
			go func() { out, err := worker.Tick(workerCtx); done <- reply{out, err} }()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
		wait:
			for {
				select {
				case <-deadline.C:
					t.Fatal("solver did not start")
				case got := <-done:
					t.Fatal("worker finished before barrier", got.err)
				case <-ticker.C:
					if _, err := os.Stat(started); err == nil {
						break wait
					}
				}
			}
			if disconnect {
				var killed bool
				if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name=$1`, label).Scan(&killed); err != nil || !killed {
					t.Fatal(killed, err)
				}
			} else if name == "configuration_changes_during_solve" || name == "pause_changes_during_solve" {
				in.ExpectedRevision = 1
				in.RequestID = "configuration-2"
				in.Request.RuleVersion = "2"
				in.Paused = name == "pause_changes_during_solve"
				updateCtx, cancelUpdate := context.WithTimeout(ctx, 2*time.Second)
				defer cancelUpdate()
				if _, err := RegisterConsistencyWatch(updateCtx, pool, in); err != nil {
					t.Fatal("configuration blocked behind solver", err)
				}
			} else {
				if _, err := ChangePropositionBinding(ctx, pool, propositionTestChange(b, "withdraw-during-solve", 0, "membership:b", kb.ID(), "withdraw", PropositionKey{})); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			got := <-done
			finished = true
			if disconnect {
				if got.err == nil {
					t.Fatal("lost worker published")
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM consistency_runs`).Scan(&count); err != nil || count != 0 {
					t.Fatal(count, err)
				}
			} else {
				if got.err != nil {
					t.Fatal(got.err)
				}
				read, err := ReadConsistencyRun(ctx, pool, got.work[0].RunID)
				if err != nil || read.Freshness != "stale" {
					t.Fatal(read.Freshness, err)
				}
			}
			next, err := worker.Tick(ctx)
			if name == "pause_changes_during_solve" {
				if err != nil || len(next) != 1 || next[0].State != "paused" {
					t.Fatal("paused configuration started another calculation", next, err)
				}
				in.ExpectedRevision = 2
				in.RequestID = "configuration-3"
				in.Paused = false
				if _, err := RegisterConsistencyWatch(ctx, pool, in); err != nil {
					t.Fatal(err)
				}
				next, err = worker.Tick(ctx)
			}
			if err != nil || len(next) != 1 || next[0].State != "computed" {
				t.Fatal(next, err)
			}
			read, err := ReadConsistencyRun(ctx, pool, next[0].RunID)
			if err != nil || read.Freshness != "matches_snapshot" {
				t.Fatal(read.Freshness, err)
			}
			want := ConsistencyCompatible
			if name != "source_changes_during_solve" {
				want = ConsistencyConflict
			}
			if name == "configuration_changes_during_solve" && (read.Run.Revision != 2 || read.Run.Request.RuleVersion != "2") {
				t.Fatal("new configuration was not recomputed")
			}
			if read.Run.Diagnosis.Outcome != want {
				t.Fatal(read.Run.Diagnosis.Outcome)
			}
		})
	}
}
