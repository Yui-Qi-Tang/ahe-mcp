// Package consistencyservice wires the operator-owned consistency worker process.
package consistencyservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeconfig"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const usage = `Usage: ahe-consistency-worker identity | once | run

Operator-owned environment (never tool arguments):
  AHE_CONSISTENCY_CADICAL         Absolute CaDiCaL executable
  AHE_CONSISTENCY_CADICAL_SHA256  Expected SHA-256, required
  AHE_CONSISTENCY_DRAT            Absolute DRAT-trim executable
  AHE_CONSISTENCY_DRAT_SHA256     Expected SHA-256, required
  AHE_CONSISTENCY_ARTIFACT_DIR    Existing private directory (0700)
  AHE_CONSISTENCY_INTERVAL       Poll interval (default 5s, minimum 10ms)

once/run additionally require DATABASE_DSN (explicit PostgreSQL URL),
AHE_DATABASE_NAME, AHE_DATABASE_LOGIN, AHE_DATABASE_ROLE, AHE_DATABASE_SCHEMA.
Use a bounded core-records LOGIN; owner/superuser credentials are refused.
identity verifies binaries and prints engine_id without connecting to PostgreSQL.
once computes one scan. run polls until SIGINT/SIGTERM or a worker error.
Errors exit nonzero: a service manager must supervise/restart the process.
Paused watches are skipped. Stored solver failures need a new watch revision.
Each start creates a separate artifact session. Only identity sessions are
removed automatically; retain other sessions for diagnosis until reviewed.
No migration, provisioning, extraction, admission or evidence repair occurs.`

// Run validates configuration and runs one process operation. Errors are redacted
// at this boundary; PostgreSQL errors may contain credentials or source material.
func Run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if ctx == nil || getenv == nil || out == nil {
		return errors.New("worker requires context, environment and output")
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, usage+"\n")
		return err
	}
	if len(args) != 1 || (args[0] != "identity" && args[0] != "once" && args[0] != "run") {
		return errors.New("expected identity, once or run; use --help")
	}
	interval := 5 * time.Second
	if value := getenv("AHE_CONSISTENCY_INTERVAL"); value != "" {
		var err error
		interval, err = time.ParseDuration(value)
		if err != nil || interval < 10*time.Millisecond || interval > time.Hour {
			return errors.New("worker interval must be between 10ms and 1h")
		}
	}
	// Identity and execution use exactly the same defaults and pinned binaries.
	runner, session, err := newRunner(getenv)
	if err != nil {
		return err
	}
	if args[0] == "identity" {
		encodeErr := json.NewEncoder(out).Encode(map[string]string{"engine_id": runner.Identity()})
		cleanupErr := os.RemoveAll(session)
		return errors.Join(encodeErr, cleanupErr)
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	pool, err := openPool(startup, getenv)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	w := evidenceingestion.ConsistencyWorker{Pool: pool, Runner: runner, PollInterval: interval}
	if err = json.NewEncoder(out).Encode(map[string]string{"event": "started", "engine_id": runner.Identity(), "artifact_session": session}); err != nil {
		return errors.New("writing worker startup status failed")
	}
	if args[0] == "once" {
		work, err := w.Tick(ctx)
		if err != nil {
			return workerError(ctx)
		}
		if err = json.NewEncoder(out).Encode(work); err != nil {
			return errors.New("writing worker scan result failed; read stored events before retry")
		}
		return nil
	}
	if err = w.Run(ctx); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return workerError(ctx)
	}
	return nil
}

func workerError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("consistency worker stopped; inspect watch engine/configuration and database availability, then restart pending work; stored failed results require a new configuration revision")
}

func newRunner(getenv func(string) string) (*cadical.Runner, string, error) {
	root := getenv("AHE_CONSISTENCY_ARTIFACT_DIR")
	info, err := os.Lstat(root)
	if err != nil || !filepath.IsAbs(root) || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, "", errors.New("worker artifact directory must be an existing absolute private directory with mode 0700")
	}
	solver, checker := getenv("AHE_CONSISTENCY_CADICAL"), getenv("AHE_CONSISTENCY_DRAT")
	if !filepath.IsAbs(solver) || !filepath.IsAbs(checker) {
		return nil, "", errors.New("solver and checker require absolute executable paths")
	}
	session, err := os.MkdirTemp(root, "session-")
	if err != nil {
		return nil, "", errors.New("creating private worker session failed")
	}
	runner, err := cadical.New(cadical.Config{SolverPath: solver, SolverSHA256: getenv("AHE_CONSISTENCY_CADICAL_SHA256"), CheckerPath: checker, CheckerSHA256: getenv("AHE_CONSISTENCY_DRAT_SHA256"), ArtifactDir: filepath.Join(session, "solver")})
	if err != nil {
		if cleanupErr := os.RemoveAll(session); cleanupErr != nil {
			return nil, "", errors.New("worker initialization and cleanup failed; inspect the private artifact directory")
		}
		return nil, "", errors.New("worker solver initialization failed; verify executable hashes and artifact permissions")
	}
	return runner, session, nil
}

func openPool(ctx context.Context, getenv func(string) string) (*pgxpool.Pool, error) {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PG") {
			return nil, errors.New("PG environment settings must be absent for worker startup")
		}
	}
	for _, key := range []string{"AHE_DATABASE_NAME", "AHE_DATABASE_LOGIN", "AHE_DATABASE_ROLE", "AHE_DATABASE_SCHEMA"} {
		if err := migrations.ValidateTargetSchema(getenv(key)); err != nil {
			return nil, errors.New("worker requires exact private database, login, role and schema identifiers")
		}
	}
	dsn, err := runtimeconfig.PostgresURL(getenv("DATABASE_DSN"), getenv("AHE_DATABASE_NAME"))
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.User != getenv("AHE_DATABASE_LOGIN") {
		return nil, errors.New("worker credential must name the selected bounded LOGIN")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, cfg, dbrole.RuntimePoolInput{Role: getenv("AHE_DATABASE_ROLE"), Schema: getenv("AHE_DATABASE_SCHEMA"), Profile: dbrole.ProfileCoreRecords})
	if err != nil {
		return nil, errors.New("worker database role verification failed")
	}
	if _, err = migrations.VerifyCurrentInSchema(ctx, pool, getenv("AHE_DATABASE_SCHEMA")); err != nil {
		pool.Close()
		return nil, errors.New("worker requires the current native schema")
	}
	return pool, nil
}
