package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PropositionKey is an externally assigned identity. Revision identifies the definition, not graph time.
type PropositionKey struct {
	Namespace string `json:"namespace"`
	LocalID   string `json:"local_id"`
	ScopeRef  string `json:"scope_ref"`
	Revision  string `json:"revision"`
}

// PropositionBindingInput records an external identity decision for an admitted claim. DecisionBy is audit text, not authentication.
type PropositionBindingInput struct {
	RequestID      string         `json:"request_id"`
	Key            PropositionKey `json:"key"`
	Definition     string         `json:"definition"`
	NodeID         string         `json:"node_id"`
	DecisionBy     string         `json:"decision_by"`
	DecisionReason string         `json:"decision_reason"`
}

// PropositionBindingReceipt describes revision zero when it was accepted; it does not report the current binding.
type PropositionBindingReceipt struct {
	Initial       PropositionBindingInput `json:"historical_initial_binding"`
	PropositionID string                  `json:"historical_proposition_id"`
	Replayed      bool                    `json:"replayed"`
}

func propositionDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		// Length framing preserves boundaries even when values contain colons.
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ID returns the versioned, length-framed identity digest.
func (k PropositionKey) ID() string {
	return "prop:sha256:" + propositionDigest("proposition-identity/v1", k.Namespace, k.LocalID, k.ScopeRef, k.Revision)
}

func (in PropositionBindingInput) hash() string {
	return propositionDigest("proposition-binding/v1", in.RequestID,
		in.Key.Namespace, in.Key.LocalID, in.Key.ScopeRef, in.Key.Revision,
		in.Definition, in.NodeID, in.DecisionBy, in.DecisionReason)
}

func (in PropositionBindingInput) validate() error {
	fields := []struct {
		name  string
		value string
		max   int
	}{
		{"namespace", in.Key.Namespace, 512}, {"local_id", in.Key.LocalID, 512},
		{"scope_ref", in.Key.ScopeRef, 512}, {"revision", in.Key.Revision, 512},
		{"definition", in.Definition, 8192}, {"request_id", in.RequestID, 512},
		{"node_id", in.NodeID, 512}, {"decision_by", in.DecisionBy, 512},
		{"decision_reason", in.DecisionReason, 2048},
	}
	for _, field := range fields {
		if len(field.value) == 0 || len(field.value) > field.max ||
			!utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			return newDomainError(ErrorInvalidInput, "invalid proposition %s", field.name)
		}
	}
	return nil
}

func loadInitialPropositionBinding(ctx context.Context, tx pgx.Tx, requestID string) (PropositionBindingInput, error) {
	var in PropositionBindingInput
	err := tx.QueryRow(ctx, `
		SELECT m.request_id, p.namespace_id, p.local_id, p.scope_ref, p.revision,
		       p.definition, m.canonical_node_id, m.decision_by, m.decision_reason
		FROM canonical_proposition_bindings m JOIN canonical_propositions p USING (proposition_id)
		WHERE m.request_id = $1`, requestID).Scan(&in.RequestID,
		&in.Key.Namespace, &in.Key.LocalID, &in.Key.ScopeRef, &in.Key.Revision,
		&in.Definition, &in.NodeID, &in.DecisionBy, &in.DecisionReason)
	return in, err
}

// BindCanonicalProposition records the initial binding; an exact replay never restores a withdrawn binding. Requires the core-records database profile.
func BindCanonicalProposition(ctx context.Context, pool *pgxpool.Pool, in PropositionBindingInput) (PropositionBindingReceipt, error) {
	if pool == nil {
		return PropositionBindingReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := in.validate(); err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Serialize exact request replay before creating any proposition.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('proposition-initial:' || $1,0))`, in.RequestID); err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	if old, err := loadInitialPropositionBinding(ctx, tx, in.RequestID); err == nil {
		if old != in {
			return PropositionBindingReceipt{}, newDomainError(ErrorIdempotencyKeyReused, "initial binding request differs from its receipt")
		}
		return PropositionBindingReceipt{Initial: in, PropositionID: in.Key.ID(), Replayed: true}, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO canonical_propositions(proposition_id, namespace_id, local_id, scope_ref, revision, definition)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`,
		in.Key.ID(), in.Key.Namespace, in.Key.LocalID, in.Key.ScopeRef, in.Key.Revision, in.Definition)
	if err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	var key PropositionKey
	var definition string
	if err := tx.QueryRow(ctx, `SELECT namespace_id, local_id, scope_ref, revision, definition
		FROM canonical_propositions WHERE proposition_id=$1`, in.Key.ID()).Scan(
		&key.Namespace, &key.LocalID, &key.ScopeRef, &key.Revision, &definition); err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	if key != in.Key || definition != in.Definition {
		return PropositionBindingReceipt{}, ErrPropositionBindingConflict
	}
	write, err := tx.Exec(ctx, `
		INSERT INTO canonical_proposition_bindings(request_id, proposition_id, canonical_node_id,
		    decision_by, decision_reason, request_hash)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (request_id) DO NOTHING`,
		in.RequestID, in.Key.ID(), in.NodeID, in.DecisionBy, in.DecisionReason, in.hash())
	if err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	replayed := write.RowsAffected() == 0
	if replayed {
		old, err := loadInitialPropositionBinding(ctx, tx, in.RequestID)
		if err != nil {
			return PropositionBindingReceipt{}, propositionBindingError(err)
		}
		if old != in {
			return PropositionBindingReceipt{}, newDomainError(ErrorIdempotencyKeyReused, "initial binding request differs from its receipt")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PropositionBindingReceipt{}, propositionBindingError(err)
	}
	return PropositionBindingReceipt{Initial: in, PropositionID: in.Key.ID(), Replayed: replayed}, nil
}

// PropositionBindingChange requires the exact prior revision, reference and proposition. Empty FromID means withdrawn.
type PropositionBindingChange struct {
	RequestID        string         `json:"request_id"`
	NodeID           string         `json:"node_id"`
	ExpectedRevision int64          `json:"expected_revision"`
	PreviousRef      string         `json:"previous_ref"`
	FromID           string         `json:"from_id"`
	Operation        string         `json:"operation"`
	Target           PropositionKey `json:"target"`
	Definition       string         `json:"definition"`
	DecisionBy       string         `json:"decision_by"`
	Reason           string         `json:"reason"`
	EvidenceRef      string         `json:"evidence_ref"`
}

// PropositionBindingEvent is one immutable correction, withdrawal or restoration.
type PropositionBindingEvent struct {
	Request    PropositionBindingChange `json:"request"`
	Revision   int64                    `json:"applied_revision"`
	TargetID   string                   `json:"target_id"`
	RecordedAt time.Time                `json:"recorded_at"`
}

// PropositionChangeReceipt is historical even when Replayed is true. Read current state separately.
type PropositionChangeReceipt struct {
	Event    PropositionBindingEvent `json:"historical_event"`
	Replayed bool                    `json:"replayed"`
}

// PropositionBindingHistory selects a binding revision, not a historical canonical graph. Active describes the binding only.
type PropositionBindingHistory struct {
	Initial          PropositionBindingInput   `json:"initial_receipt"`
	HeadRevision     int64                     `json:"head_revision"`
	SelectedRevision int64                     `json:"selected_revision"`
	PropositionID    string                    `json:"proposition_id"`
	HeadRef          string                    `json:"selected_ref"`
	Active           bool                      `json:"active"`
	Events           []PropositionBindingEvent `json:"events"`
	HistoryTruncated bool                      `json:"history_truncated"`
	Snapshot         string                    `json:"postgres_snapshot"`
}

// PropositionMembers contains current bindings and a bounded canonical graph from the same snapshot. Graph neighbours are not additional members.
type PropositionMembers struct {
	PropositionID  string                          `json:"proposition_id"`
	Nodes          []string                        `json:"current_nodes"`
	Truncated      bool                            `json:"members_truncated"`
	Snapshot       string                          `json:"postgres_snapshot"`
	Graph          evidencegraph.CanonicalArtifact `json:"graph"`
	GraphTruncated bool                            `json:"graph_truncated"`
	GraphMaxDepth  int                             `json:"graph_max_depth"`
	GraphMaxNodes  int                             `json:"graph_max_nodes"`
	GraphMaxEdges  int                             `json:"graph_max_edges"`
}

// ErrPropositionBindingConflict identifies a stale head, incompatible definition or illegal transition.
var ErrPropositionBindingConflict = errors.New("proposition binding conflicts with recorded identity or state")

func (in PropositionBindingChange) validate() error {
	if in.ExpectedRevision < 0 || in.ExpectedRevision == math.MaxInt64 {
		return newDomainError(ErrorInvalidInput, "invalid expected binding revision")
	}
	for _, f := range []struct {
		value string
		max   int
	}{
		{in.RequestID, 512}, {in.NodeID, 512}, {in.PreviousRef, 1024},
		{in.DecisionBy, 512}, {in.Reason, 2048}, {in.EvidenceRef, 2048},
	} {
		if f.value == "" || len(f.value) > f.max || !utf8.ValidString(f.value) || strings.ContainsRune(f.value, 0) {
			return newDomainError(ErrorInvalidInput, "invalid proposition event field")
		}
	}
	if len(in.FromID) > 128 || !utf8.ValidString(in.FromID) || strings.ContainsRune(in.FromID, 0) {
		return newDomainError(ErrorInvalidInput, "invalid previous proposition")
	}
	switch in.Operation {
	case "withdraw":
		if in.Target != (PropositionKey{}) || in.Definition != "" {
			return newDomainError(ErrorInvalidInput, "withdrawal cannot supply a target")
		}
	case "correct", "restore":
		return (PropositionBindingInput{RequestID: in.RequestID, NodeID: in.NodeID, Key: in.Target,
			Definition: in.Definition, DecisionBy: in.DecisionBy, DecisionReason: in.Reason}).validate()
	default:
		return newDomainError(ErrorInvalidInput, "invalid proposition operation")
	}
	return nil
}

const propositionBindingEventSelect = `SELECT e.request_id,e.canonical_node_id,e.revision-1,e.previous_ref,
	coalesce(e.from_id,''),e.operation,coalesce(p.namespace_id,''),coalesce(p.local_id,''),
	coalesce(p.scope_ref,''),coalesce(p.revision,''),coalesce(p.definition,''),
	e.decision_by,e.decision_reason,e.evidence_ref,e.revision,coalesce(e.target_id,''),e.recorded_at
	FROM canonical_proposition_binding_events e LEFT JOIN canonical_propositions p ON p.proposition_id=e.target_id `

func scanPropositionBindingEvent(row pgx.Row) (PropositionBindingEvent, error) {
	var event PropositionBindingEvent
	in := &event.Request
	err := row.Scan(&in.RequestID, &in.NodeID, &in.ExpectedRevision, &in.PreviousRef, &in.FromID, &in.Operation,
		&in.Target.Namespace, &in.Target.LocalID, &in.Target.ScopeRef, &in.Target.Revision, &in.Definition,
		&in.DecisionBy, &in.Reason, &in.EvidenceRef, &event.Revision, &event.TargetID, &event.RecordedAt)
	return event, err
}

// ChangePropositionBinding appends an external decision using compare-and-swap; an exact replay returns only its original receipt. Requires the core-records database profile.
func ChangePropositionBinding(ctx context.Context, pool *pgxpool.Pool, in PropositionBindingChange) (PropositionChangeReceipt, error) {
	if pool == nil {
		return PropositionChangeReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := in.validate(); err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Request and node locks have one fixed order. Hash collisions only serialize
	// unrelated requests; full field comparisons remain the authority.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('binding-request:' || $1,0))`, in.RequestID); err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	old, err := scanPropositionBindingEvent(tx.QueryRow(ctx, propositionBindingEventSelect+`WHERE e.request_id=$1`, in.RequestID))
	if err == nil {
		if old.Request != in {
			return PropositionChangeReceipt{}, newDomainError(ErrorIdempotencyKeyReused, "binding change request differs from its receipt")
		}
		return PropositionChangeReceipt{Event: old, Replayed: true}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	var initialID string
	if err := tx.QueryRow(ctx, `SELECT request_id FROM canonical_proposition_bindings WHERE canonical_node_id=$1 FOR UPDATE`, in.NodeID).Scan(&initialID); err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	var target any
	if in.Operation != "withdraw" {
		target = in.Target.ID()
		if _, err := tx.Exec(ctx, `INSERT INTO canonical_propositions(proposition_id,namespace_id,local_id,scope_ref,revision,definition)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, target, in.Target.Namespace, in.Target.LocalID,
			in.Target.ScopeRef, in.Target.Revision, in.Definition); err != nil {
			return PropositionChangeReceipt{}, propositionBindingError(err)
		}
		var key PropositionKey
		var definition string
		if err := tx.QueryRow(ctx, `SELECT namespace_id,local_id,scope_ref,revision,definition FROM canonical_propositions WHERE proposition_id=$1`, target).
			Scan(&key.Namespace, &key.LocalID, &key.ScopeRef, &key.Revision, &definition); err != nil {
			return PropositionChangeReceipt{}, propositionBindingError(err)
		}
		if key != in.Target || definition != in.Definition {
			return PropositionChangeReceipt{}, ErrPropositionBindingConflict
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO canonical_proposition_binding_events(request_id,canonical_node_id,revision,previous_ref,from_id,target_id,
		operation,decision_by,decision_reason,evidence_ref) VALUES ($1,$2,$3,$4,nullif($5,''),$6,$7,$8,$9,$10)`,
		in.RequestID, in.NodeID, in.ExpectedRevision+1, in.PreviousRef, in.FromID, target, in.Operation, in.DecisionBy, in.Reason, in.EvidenceRef)
	if err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	event, err := scanPropositionBindingEvent(tx.QueryRow(ctx, propositionBindingEventSelect+`WHERE e.request_id=$1`, in.RequestID))
	if err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PropositionChangeReceipt{}, propositionBindingError(err)
	}
	return PropositionChangeReceipt{Event: event}, nil
}

func readPropositionBindingHistoryTx(ctx context.Context, tx pgx.Tx, node string, revision int64, limit int) (PropositionBindingHistory, error) {
	if revision < -1 || limit < 1 || limit > 100 {
		return PropositionBindingHistory{}, errors.New("invalid proposition history bounds")
	}
	var result PropositionBindingHistory
	var initialRequest string
	err := tx.QueryRow(ctx, `SELECT request_id,txid_current_snapshot()::text FROM canonical_proposition_bindings WHERE canonical_node_id=$1`, node).
		Scan(&initialRequest, &result.Snapshot)
	if err != nil {
		return result, err
	}
	result.Initial, err = loadInitialPropositionBinding(ctx, tx, initialRequest)
	if err != nil {
		return result, err
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision),0) FROM canonical_proposition_binding_events WHERE canonical_node_id=$1`, node).Scan(&result.HeadRevision); err != nil {
		return result, err
	}
	if revision == -1 {
		revision = result.HeadRevision
	}
	if revision > result.HeadRevision {
		return result, errors.New("binding history revision unavailable")
	}
	result.SelectedRevision = revision
	result.PropositionID, result.HeadRef = result.Initial.Key.ID(), "membership:"+initialRequest
	if revision > 0 {
		event, err := scanPropositionBindingEvent(tx.QueryRow(ctx, propositionBindingEventSelect+`WHERE e.canonical_node_id=$1 AND e.revision=$2`, node, revision))
		if err != nil {
			return result, err
		}
		result.PropositionID, result.HeadRef = event.TargetID, "event:"+event.Request.RequestID
	}
	result.Active = result.PropositionID != ""
	result.Events = []PropositionBindingEvent{}
	rows, err := tx.Query(ctx, propositionBindingEventSelect+`WHERE e.canonical_node_id=$1 AND e.revision<=$2 ORDER BY e.revision LIMIT $3`, node, revision, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		event, err := scanPropositionBindingEvent(rows)
		if err != nil {
			return result, err
		}
		result.Events = append(result.Events, event)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Events) > limit {
		result.HistoryTruncated = true
		result.Events = result.Events[:limit]
	}
	return result, nil
}

// ReadPropositionBindingHistory reads revision -1 as current, or an explicit revision; limit is 1..100. A truncated event list does not change the selected state.
func ReadPropositionBindingHistory(ctx context.Context, pool *pgxpool.Pool, node string, revision int64, limit int) (PropositionBindingHistory, error) {
	if pool == nil {
		return PropositionBindingHistory{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PropositionBindingHistory{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Fail instead of silently treating an RLS-hidden withdrawal as no event.
	if _, err := tx.Exec(ctx, `SET LOCAL row_security = off`); err != nil {
		return PropositionBindingHistory{}, err
	}
	result, err := readPropositionBindingHistoryTx(ctx, tx, node, revision, limit)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func readPropositionMembersTx(ctx context.Context, tx pgx.Tx, key PropositionKey, limit int) (PropositionMembers, error) {
	view := PropositionMembers{PropositionID: key.ID(), Nodes: []string{}, GraphMaxDepth: canonicalReadMaxDepth, GraphMaxNodes: canonicalReadMaxNodes, GraphMaxEdges: canonicalReadMaxEdges}
	if limit < 1 || limit > canonicalReadMaxRoots {
		return view, errors.New("invalid proposition inventory limit")
	}
	var stored PropositionKey
	if err := tx.QueryRow(ctx, `SELECT namespace_id,local_id,scope_ref,revision,txid_current_snapshot()::text FROM canonical_propositions WHERE proposition_id=$1`, key.ID()).
		Scan(&stored.Namespace, &stored.LocalID, &stored.ScopeRef, &stored.Revision, &view.Snapshot); err != nil {
		return view, err
	}
	if stored != key {
		return view, ErrPropositionBindingConflict
	}
	rows, err := tx.Query(ctx, `WITH proposition_binding_current AS (
 SELECT m.canonical_node_id,
 CASE WHEN e.request_id IS NULL THEN m.proposition_id ELSE e.target_id END proposition_id
 FROM canonical_proposition_bindings m LEFT JOIN LATERAL (
 SELECT request_id,target_id FROM canonical_proposition_binding_events
 WHERE canonical_node_id=m.canonical_node_id ORDER BY revision DESC LIMIT 1
 ) e ON true
) SELECT canonical_node_id FROM proposition_binding_current WHERE proposition_id=$1 ORDER BY canonical_node_id COLLATE "C" LIMIT $2`, key.ID(), limit+1)
	if err != nil {
		return view, err
	}
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			rows.Close()
			return view, err
		}
		view.Nodes = append(view.Nodes, node)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return view, err
	}
	if len(view.Nodes) > limit {
		view.Truncated = true
		view.Nodes = view.Nodes[:limit]
	}
	view.Graph, view.GraphTruncated, err = loadCanonicalReadArtifact(ctx, pgxTx{tx: tx}, CanonicalReadInput{
		RootNodeIDs: view.Nodes, MaxDepth: view.GraphMaxDepth, MaxNodes: view.GraphMaxNodes, MaxEdges: view.GraphMaxEdges,
	})
	return view, err
}

// ReadPropositionMembers reads at most 32 current members and their native graph without merging derivations.
func ReadPropositionMembers(ctx context.Context, pool *pgxpool.Pool, key PropositionKey, limit int) (PropositionMembers, error) {
	if pool == nil {
		return PropositionMembers{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PropositionMembers{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Fail instead of silently treating an RLS-hidden withdrawal as no event.
	if _, err := tx.Exec(ctx, `SET LOCAL row_security = off`); err != nil {
		return PropositionMembers{}, err
	}
	result, err := readPropositionMembersTx(ctx, tx, key, limit)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func propositionBindingError(err error) error {
	var dbErr *pgconn.PgError
	if errors.As(err, &dbErr) && (dbErr.Code == "23505" || dbErr.Code == "23514" || dbErr.Code == "P0001") {
		return fmt.Errorf("%w: %s", ErrPropositionBindingConflict, dbErr.Message)
	}
	return err
}
