package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const consistencyWatchLock = `hashtextextended(current_database() || ':' || current_schema() || ':consistency:' || $1, 0)`

// Keep the worker lock key stable across upgrades. Configuration writes use a
// separate short lock; the existing anchor-row lock orders journal commits.
const consistencyJournalLock = `hashtextextended(current_database() || ':' || 'consistency_watches'::regclass::oid::text || ':consistency-journal:' || $1, 0)`

// ConsistencyWatchInput registers immutable, externally normalized inputs.
// Evidence is a catalog: withdrawn/excluded nodes can remain for future history.
// New eligible nodes without an entry block recomputation until a new revision.
type ConsistencyWatchInput struct {
	Contract             string             `json:"contract"`
	WatchID              string             `json:"watch_id"`
	RequestID            string             `json:"request_id"`
	ExpectedRevision     int64              `json:"expected_revision"`
	Request              ConsistencyRequest `json:"request"`
	MaxLocalizationCalls int                `json:"max_localization_calls"`
	EngineID             string             `json:"engine_id"`
	Paused               bool               `json:"paused"`
	RecordedBy           string             `json:"recorded_by,omitempty"`
}

// ConsistencyWatchReceipt identifies one historical configuration version.
type ConsistencyWatchReceipt struct {
	WatchID  string `json:"watch_id"`
	Revision int64  `json:"revision"`
	Hash     string `json:"hash"`
	Replayed bool   `json:"replayed"`
}

// StoredConsistencyRun retains computed results and their exact input version.
// No stored field claims permanent currentness. ReadConsistencyRun refreshes it.
type StoredConsistencyRun struct {
	Contract  string                   `json:"contract"`
	WatchID   string                   `json:"watch_id"`
	Revision  int64                    `json:"revision"`
	TargetID  string                   `json:"target_id"`
	EngineID  string                   `json:"engine_id"`
	Request   ConsistencyRequest       `json:"request"`
	Diagnosis ConsistencyDiagnosis     `json:"diagnosis"`
	Error     string                   `json:"error,omitempty"`
	Artifacts []logicresolver.Artifact `json:"artifacts"`
}

// ConsistencyRunRead distinguishes an immutable result from its observed
// freshness. Freshness is only valid in the read transaction's snapshot.
type ConsistencyRunRead struct {
	ID        string               `json:"id"`
	Run       StoredConsistencyRun `json:"run"`
	Freshness string               `json:"freshness"` // matches_snapshot, stale, unavailable
	Snapshot  string               `json:"snapshot,omitempty"`
}

// ConsistencyEvent is a durable notification. Cursor ordering is per watch,
// serialized at commit; consumers must keep one cursor per watch identity.
type ConsistencyEvent struct {
	WatchID    string    `json:"watch_id"`
	Cursor     int64     `json:"cursor"`
	Revision   int64     `json:"revision"`
	TargetID   string    `json:"target_id"`
	Kind       string    `json:"kind"`
	RunID      string    `json:"run_id,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

type ConsistencyEvents struct {
	Events     []ConsistencyEvent `json:"events"`
	NextCursor int64              `json:"next_cursor"`
	Truncated  bool               `json:"truncated"`
}

func normalizeConsistencyWatch(in ConsistencyWatchInput) (ConsistencyWatchInput, error) {
	if in.RecordedBy != "" && !consistencyText(in.RecordedBy) {
		return in, logicresolver.ErrInput
	}
	if in.Contract != "" && in.Contract != "consistency-watch/v1" {
		return in, logicresolver.ErrInput
	}
	in.Contract = "consistency-watch/v1"
	if !consistencyText(in.WatchID) || !consistencyText(in.RequestID) || !consistencyText(in.EngineID) || in.ExpectedRevision < 0 || !consistencyText(in.Request.RuleVersion) || in.Request.ExpectedViewID != "" {
		return in, logicresolver.ErrInput
	}
	var err error
	in.Request.Scope, err = in.Request.Scope.normalized()
	if err != nil {
		return in, err
	}
	if in.MaxLocalizationCalls == 0 {
		in.MaxLocalizationCalls = 64
	}
	if in.MaxLocalizationCalls < 1 || in.MaxLocalizationCalls > 64 || len(in.Request.Evidence) > 4096 {
		return in, logicresolver.ErrLimit
	}
	view := ConsistencyView{}
	for _, e := range in.Request.Evidence {
		view.Members = append(view.Members, ConsistencyMember{NodeID: e.ID})
	}
	if _, _, err := compileConsistency(view, in.Request); err != nil {
		return in, err
	}
	return in, nil
}

// ConsistencyWatchRead returns an immutable configuration at an explicit revision.
// CurrentRevision is observed in this read snapshot, not a lasting validity claim.
type ConsistencyWatchRead struct {
	Receipt         ConsistencyWatchReceipt `json:"receipt"`
	Configuration   ConsistencyWatchInput   `json:"configuration"`
	CurrentRevision int64                   `json:"current_revision"`
}

// ReadConsistencyWatch reads -1 for the latest configuration, or an exact positive
// revision. It never silently resolves a missing revision to the current one.
func ReadConsistencyWatch(ctx context.Context, pool *pgxpool.Pool, watch string, revision int64) (ConsistencyWatchRead, error) {
	var out ConsistencyWatchRead
	if pool == nil || !consistencyText(watch) || revision == 0 || revision < -1 {
		return out, logicresolver.ErrInput
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(revision),0) FROM consistency_watch_versions WHERE watch_id=$1`, watch).Scan(&out.CurrentRevision); err != nil {
		return out, err
	}
	if revision == -1 {
		revision = out.CurrentRevision
	}
	var body, hash string
	if err = tx.QueryRow(ctx, `SELECT body,body_hash FROM consistency_watch_versions WHERE watch_id=$1 AND revision=$2`, watch, revision).Scan(&body, &hash); err != nil {
		return out, err
	}
	sum := sha256.Sum256([]byte(body))
	if hex.EncodeToString(sum[:]) != hash {
		return out, logicresolver.ErrValidation
	}
	if err = json.Unmarshal([]byte(body), &out.Configuration); err != nil {
		return out, err
	}
	out.Receipt = ConsistencyWatchReceipt{WatchID: watch, Revision: revision, Hash: hash}
	return out, tx.Commit(ctx)
}

// RegisterConsistencyWatch appends a configuration using optimistic revision
// matching. Exact request retries return the original receipt, not currentness.
func RegisterConsistencyWatch(ctx context.Context, pool *pgxpool.Pool, in ConsistencyWatchInput) (ConsistencyWatchReceipt, error) {
	if pool == nil {
		return ConsistencyWatchReceipt{}, logicresolver.ErrInput
	}
	in, err := normalizeConsistencyWatch(in)
	if err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	if len(b) > 16<<20 {
		return ConsistencyWatchReceipt{}, logicresolver.ErrLimit
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+consistencyJournalLock+`)`, in.WatchID); err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	readReplay := func() (ConsistencyWatchReceipt, bool, error) {
		var body, watch, hash string
		var revision int64
		err := tx.QueryRow(ctx, `SELECT body,watch_id,revision,body_hash FROM consistency_watch_versions WHERE request_id=$1`, in.RequestID).Scan(&body, &watch, &revision, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ConsistencyWatchReceipt{}, false, nil
		}
		if err != nil {
			return ConsistencyWatchReceipt{}, false, err
		}
		if body != string(b) {
			return ConsistencyWatchReceipt{}, false, newDomainError(ErrorIdempotencyKeyReused, "consistency request content changed")
		}
		return ConsistencyWatchReceipt{WatchID: watch, Revision: revision, Hash: hash, Replayed: true}, true, nil
	}
	if r, ok, err := readReplay(); err != nil {
		return r, err
	} else if ok {
		return r, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO consistency_watches(watch_id) VALUES($1) ON CONFLICT DO NOTHING`, in.WatchID); err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(revision),0) FROM consistency_watch_versions WHERE watch_id=$1`, in.WatchID).Scan(&revision); err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	if revision != in.ExpectedRevision {
		return ConsistencyWatchReceipt{}, newDomainError(ErrorIdempotencyKeyReused, "consistency configuration revision changed")
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	command, err := tx.Exec(ctx, `INSERT INTO consistency_watch_versions(watch_id,revision,request_id,body,body_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT(request_id) DO NOTHING`, in.WatchID, revision+1, in.RequestID, string(b), hash)
	if err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	if command.RowsAffected() == 0 {
		r, _, err := readReplay()
		if err != nil {
			return r, err
		}
		return r, tx.Commit(ctx)
	}
	if err := appendConsistencyEvent(ctx, tx, in.WatchID, revision+1, hash, "configured", ""); err != nil {
		return ConsistencyWatchReceipt{}, err
	}
	return ConsistencyWatchReceipt{WatchID: in.WatchID, Revision: revision + 1, Hash: hash}, tx.Commit(ctx)
}

func appendConsistencyEvent(ctx context.Context, tx pgx.Tx, watch string, revision int64, target, kind, run string) error {
	// Lock BEFORE computing max(cursor), rather than relying on the insert
	// trigger to serialize a value computed in an older statement snapshot.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT watch_id FROM consistency_watches WHERE watch_id=$1 FOR UPDATE`, watch).Scan(&locked); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM consistency_events WHERE watch_id=$1 AND revision=$2 AND target_id=$3 AND kind=$4)`, watch, revision, target, kind).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO consistency_events(watch_id,cursor,revision,target_id,kind,run_id)
	SELECT $1,coalesce(max(cursor),0)+1,$2,$3,$4,NULLIF($5,'') FROM consistency_events WHERE watch_id=$1`, watch, revision, target, kind, run)
	return err
}

// ReadConsistencyEvents supports repeatable delivery; persisting NextCursor is
// the consumer's responsibility. Events do not prove that a result is still fresh.
func ReadConsistencyEvents(ctx context.Context, pool *pgxpool.Pool, watch string, after int64, limit int) (ConsistencyEvents, error) {
	out := ConsistencyEvents{NextCursor: after}
	if pool == nil || !consistencyText(watch) || after < 0 || limit < 1 || limit > 256 {
		return out, logicresolver.ErrInput
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT watch_id,cursor,revision,target_id,kind,coalesce(run_id,''),recorded_at FROM consistency_events WHERE watch_id=$1 AND cursor>$2 ORDER BY cursor LIMIT $3`, watch, after, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e ConsistencyEvent
		if err := rows.Scan(&e.WatchID, &e.Cursor, &e.Revision, &e.TargetID, &e.Kind, &e.RunID, &e.RecordedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Events = append(out.Events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Events) > limit {
		out.Truncated = true
		out.Events = out.Events[:limit]
	}
	if len(out.Events) > 0 {
		out.NextCursor = out.Events[len(out.Events)-1].Cursor
	}
	return out, tx.Commit(ctx)
}

// ReadConsistencyRun rechecks both configuration and source view in one fresh
// transaction. A failed source read never falls back to matches_snapshot.
func ReadConsistencyRun(ctx context.Context, pool *pgxpool.Pool, id string) (ConsistencyRunRead, error) {
	out := ConsistencyRunRead{ID: id, Freshness: "unavailable"}
	if pool == nil || !consistencyText(id) {
		return out, logicresolver.ErrInput
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return out, err
	}
	var body string
	if err = tx.QueryRow(ctx, `SELECT body FROM consistency_runs WHERE run_id=$1`, id).Scan(&body); err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(body), &out.Run); err != nil {
		return out, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT max(revision) FROM consistency_watch_versions WHERE watch_id=$1`, out.Run.WatchID).Scan(&revision); err != nil {
		return out, err
	}
	view, err := readConsistencyScopeTx(ctx, tx, out.Run.Request.Scope)
	if err != nil {
		return out, err
	}
	out.Snapshot = view.Snapshot
	out.Freshness = "stale"
	if revision == out.Run.Revision && out.Run.Diagnosis.View.ID != "" && view.ID == out.Run.Diagnosis.View.ID && view.ID == out.Run.Request.ExpectedViewID {
		out.Freshness = "matches_snapshot"
	}
	return out, tx.Commit(ctx)
}

// ReadConsistencyArtifact returns independently hash-checked archived bytes.
func ReadConsistencyArtifact(ctx context.Context, pool *pgxpool.Pool, runID, artifactID string) ([]byte, error) {
	if pool == nil || !consistencyText(runID) || !consistencyText(artifactID) {
		return nil, logicresolver.ErrInput
	}
	var b []byte
	var digest string
	err := pool.QueryRow(ctx, `SELECT content,digest FROM consistency_run_artifacts WHERE run_id=$1 AND artifact_id=$2`, runID, artifactID).Scan(&b, &digest)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, logicresolver.ErrValidation
	}
	return b, nil
}

func consistencyRunArtifacts(d ConsistencyDiagnosis) ([]logicresolver.Artifact, error) {
	all := append(slices.Clone(d.Rules.Artifacts), d.Declarations.Artifacts...)
	for _, step := range d.Localization.Steps {
		all = append(all, step.Result.Artifacts...)
	}
	byID := map[string]logicresolver.Artifact{}
	var size int64
	for _, a := range all {
		if old, ok := byID[a.ID]; ok {
			if old != a {
				return nil, logicresolver.ErrValidation
			}
			continue
		}
		byID[a.ID] = a
		size += a.Bytes
		if a.Bytes < 0 || a.Bytes > 64<<20 || size > 128<<20 {
			return nil, logicresolver.ErrLimit
		}
	}
	result := make([]logicresolver.Artifact, 0, len(byID))
	for _, a := range byID {
		result = append(result, a)
	}
	slices.SortFunc(result, func(a, b logicresolver.Artifact) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result, nil
}

func consistencyTarget(versionHash, viewID, engine string) (string, error) {
	return consistencyHash([]string{"consistency-work/v1", versionHash, viewID, engine})
}

func validateStoredConsistencyRun(run StoredConsistencyRun) error {
	if run.Contract != "consistency-run/v1" || !consistencyText(run.WatchID) || run.Revision < 1 || !consistencyText(run.TargetID) {
		return fmt.Errorf("%w: stored run identity", logicresolver.ErrInput)
	}
	return nil
}
