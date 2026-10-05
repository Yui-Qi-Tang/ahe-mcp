package evidenceingestion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/cadical"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConsistencyWorker reevaluates registered watches. Run must be explicitly
// started by a native consumer; constructing it does not start a goroutine.
// Each watch uses one pinned connection for reads and writes.
type ConsistencyWorker struct {
	Pool         *pgxpool.Pool
	Runner       *cadical.Runner
	PollInterval time.Duration
	MaxWatches   int
}

type ConsistencyWork struct {
	WatchID string `json:"watch_id"`
	State   string `json:"state"`
	RunID   string `json:"run_id,omitempty"`
}

// Run polls immediately and then at the specified interval. Errors stop the
// loop for the caller to supervise; cancellation terminates all solver work.
// No SMTP, webhook, or ephemeral LISTEN message is used for notifications.
func (w ConsistencyWorker) Run(ctx context.Context) error {
	if w.PollInterval < 10*time.Millisecond {
		return fmt.Errorf("%w: polling interval", logicresolver.ErrInput)
	}
	for {
		if _, err := w.Tick(ctx); err != nil {
			return err
		}
		timer := time.NewTimer(w.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Tick detects changes and finishes unfinished targets. Detection is durable
// before solving; an absent run, not an observed marker, determines pending work.
func (w ConsistencyWorker) Tick(ctx context.Context) ([]ConsistencyWork, error) {
	if w.Pool == nil || w.Runner == nil {
		return nil, logicresolver.ErrInput
	}
	limit := w.MaxWatches
	if limit == 0 {
		limit = 256
	}
	if limit < 1 || limit > 1024 {
		return nil, logicresolver.ErrLimit
	}
	tx, err := w.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT watch_id FROM consistency_watches ORDER BY watch_id COLLATE "C" LIMIT $1`, limit+1)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if len(ids) > limit {
		return nil, logicresolver.ErrLimit
	}
	var result []ConsistencyWork
	var failures []error
	for _, id := range ids {
		out, err := w.tickWatch(ctx, id)
		result = append(result, out)
		if err != nil {
			failures = append(failures, fmt.Errorf("consistency watch %q: %w", id, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return result, errors.Join(failures...)
}

func (w ConsistencyWorker) tickWatch(ctx context.Context, id string) (ConsistencyWork, error) {
	out := ConsistencyWork{WatchID: id, State: "busy"}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	conn, err := w.Pool.Acquire(ctx)
	if err != nil {
		return out, err
	}
	locked := false
	defer func() {
		if locked {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released bool
			err := conn.QueryRow(c, `SELECT pg_advisory_unlock(`+consistencyWatchLock+`)`, id).Scan(&released)
			if err != nil || !released {
				raw := conn.Hijack()
				_ = raw.Close(c)
				return
			}
		}
		conn.Release()
	}()
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+consistencyWatchLock+`)`, id).Scan(&locked); err != nil {
		return out, err
	}
	if !locked {
		return out, nil
	}
	// All event/result writes use this same pinned connection. Losing it cannot
	// publish a stale worker's result after another worker acquires the lock.
	var body, hash string
	var revision int64
	configTx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	if _, err = configTx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		_ = configTx.Rollback(context.Background())
		return out, err
	}
	if err = configTx.QueryRow(ctx, `SELECT body,body_hash,revision FROM consistency_watch_versions WHERE watch_id=$1 ORDER BY revision DESC LIMIT 1`, id).Scan(&body, &hash, &revision); err != nil {
		_ = configTx.Rollback(context.Background())
		return out, err
	}
	if err = configTx.Commit(ctx); err != nil {
		return out, err
	}
	var config ConsistencyWatchInput
	if err = json.Unmarshal([]byte(body), &config); err != nil {
		return out, err
	}
	if config.Paused {
		out.State = "paused"
		return out, nil
	}
	if config.EngineID != w.Runner.Identity() {
		return out, fmt.Errorf("%w: configured solver identity mismatch", logicresolver.ErrInput)
	}
	read := func(ctx context.Context, scope ConsistencyScope) (ConsistencyView, error) {
		return readConsistencyConnection(ctx, conn.Conn(), scope)
	}
	view, err := read(ctx, config.Request.Scope)
	if err != nil {
		return out, err
	}
	target, err := consistencyTarget(hash, view.ID, w.Runner.Identity())
	if err != nil {
		return out, err
	}
	lookup, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	if _, err = lookup.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		_ = lookup.Rollback(context.Background())
		return out, err
	}
	err = lookup.QueryRow(ctx, `SELECT run_id FROM consistency_runs WHERE watch_id=$1 AND revision=$2 AND target_id=$3`, id, revision, target).Scan(&out.RunID)
	_ = lookup.Rollback(context.Background())
	if err == nil {
		out.State = "unchanged"
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	if err = appendConsistencyEvent(ctx, tx, id, revision, target, "invalidated", ""); err != nil {
		_ = tx.Rollback(context.Background())
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	request := config.Request
	request.ExpectedViewID = view.ID
	request.Evidence = nil
	selected, complete := consistencySelectedView(view)
	catalog := map[string]ConsistencyCondition{}
	for _, e := range config.Request.Evidence {
		catalog[e.ID] = e
	}
	missing := false
	for _, m := range selected.Members {
		e, ok := catalog[m.NodeID]
		if !ok {
			missing = true
			continue
		}
		request.Evidence = append(request.Evidence, e)
	}
	diagnosis := ConsistencyDiagnosis{ConsistencyResult: ConsistencyResult{Outcome: ConsistencyInconclusive, View: view, RuleVersion: request.RuleVersion}, Localization: ConsistencyLocalization{Reason: "not_applicable"}}
	var solveErr error
	switch {
	case !complete:
		diagnosis.Reason = "unknown_currentness"
	case missing:
		diagnosis.Reason = "missing_normalization"
	default:
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return out, err
		}
		diagnosis, solveErr = diagnoseConsistency(ctx, w.Runner, "watch-"+hex.EncodeToString(nonce[:]), request, config.MaxLocalizationCalls, read)
	}
	manifest, err := consistencyRunArtifacts(diagnosis)
	if err != nil {
		return out, err
	}
	artifacts := make(map[string][]byte, len(manifest))
	for _, a := range manifest {
		raw, err := w.Runner.ReadArtifact(a)
		if err != nil {
			return out, err
		}
		artifacts[a.ID] = raw
	}
	run := StoredConsistencyRun{Contract: "consistency-run/v1", WatchID: id, Revision: revision, TargetID: target, EngineID: w.Runner.Identity(), Request: request, Diagnosis: diagnosis, Artifacts: manifest}
	if solveErr != nil {
		run.Error = solveErr.Error()
	}
	if err := validateStoredConsistencyRun(run); err != nil {
		return out, err
	}
	data, err := json.Marshal(run)
	if err != nil {
		return out, err
	}
	if len(data) > 16<<20 {
		return out, logicresolver.ErrLimit
	}
	sum := sha256.Sum256(data)
	out.RunID = hex.EncodeToString(sum[:])
	tx, err = conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `INSERT INTO consistency_runs(run_id,watch_id,revision,target_id,body) VALUES($1,$2,$3,$4,$5)`, out.RunID, id, revision, target, string(data)); err != nil {
		return out, err
	}
	for _, a := range manifest {
		if _, err = tx.Exec(ctx, `INSERT INTO consistency_run_artifacts(run_id,artifact_id,digest,content) VALUES($1,$2,$3,$4)`, out.RunID, a.ID, a.SHA256, artifacts[a.ID]); err != nil {
			return out, err
		}
	}
	if err = appendConsistencyEvent(ctx, tx, id, revision, target, "completed", out.RunID); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.State = "computed"
	if diagnosis.Outcome == ConsistencyInconclusive {
		out.State = "blocked"
	}
	if solveErr != nil {
		out.State = "failed"
	}
	return out, nil
}

func readConsistencyConnection(ctx context.Context, conn *pgx.Conn, scope ConsistencyScope) (ConsistencyView, error) {
	scope, err := scope.normalized()
	if err != nil {
		return ConsistencyView{}, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ConsistencyView{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return ConsistencyView{}, err
	}
	view, err := readConsistencyScopeTx(ctx, tx, scope)
	if err != nil {
		return view, err
	}
	return view, tx.Commit(ctx)
}
