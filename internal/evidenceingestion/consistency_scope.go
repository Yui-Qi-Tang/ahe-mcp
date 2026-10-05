package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConsistencyProfile checks bound declarations, including historical content.
// It does not select temporally current truth or apply supersession policy.
const ConsistencyProfile = "bound-declarations/cnf-v0"

// ConsistencyScope selects all currently bound declarations in an exact scope,
// across all proposition definition revisions, regardless of graph connectivity.
// Zero limits use the maxima (256 nodes and 4096 internal edges).
type ConsistencyScope struct {
	Policy    string `json:"policy,omitempty"`
	Namespace string `json:"namespace"`
	ScopeRef  string `json:"scope_ref"`
	MaxNodes  int    `json:"max_nodes"`
	MaxEdges  int    `json:"max_edges"`
}

// ConsistencyMember preserves alternative derivation and current binding identity.
type ConsistencyMember struct {
	NodeID       string         `json:"node_id"`
	Key          PropositionKey `json:"key"`
	Definition   string         `json:"definition"`
	HeadRevision int64          `json:"head_revision"`
	HeadRef      string         `json:"head_ref"`
}

// ConsistencyView is a read-only snapshot, not a claim of global completeness.
// ID covers scope, binding heads, definitions, artifact and policy dependencies.
// Snapshot is a PostgreSQL receipt; it is excluded from the semantic fingerprint.
type ConsistencyView struct {
	Profile     string                          `json:"profile"`
	Scope       ConsistencyScope                `json:"scope"`
	Members     []ConsistencyMember             `json:"members"`
	Artifact    evidencegraph.CanonicalArtifact `json:"artifact"`
	ID          string                          `json:"id"`
	Snapshot    string                          `json:"snapshot"`
	Selection   []ConsistencySelection          `json:"selection,omitempty"`
	Currentness []ConsistencyLineage            `json:"currentness,omitempty"`
}

func (s ConsistencyScope) normalized() (ConsistencyScope, error) {
	if s.Policy == ConsistencyProfile {
		s.Policy = ""
	}
	if s.Policy != "" && s.Policy != ConsistencyFrontierPolicy {
		return s, fmt.Errorf("%w: consistency selection policy", logicresolver.ErrInput)
	}
	if !consistencyText(s.Namespace) || !consistencyText(s.ScopeRef) {
		return s, fmt.Errorf("%w: namespace and scope_ref required", logicresolver.ErrInput)
	}
	if s.MaxNodes == 0 {
		s.MaxNodes = 256
	}
	if s.MaxEdges == 0 {
		s.MaxEdges = 4096
	}
	if s.MaxNodes < 1 || s.MaxNodes > 256 || s.MaxEdges < 1 || s.MaxEdges > 4096 {
		return s, fmt.Errorf("%w: scope bounds", logicresolver.ErrInput)
	}
	return s, nil
}

func consistencyText(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// ReadConsistencyScope fails on truncation, inaccessible rows or incomplete AND
// parents. It never falls back to a connected neighborhood or a ranked search.
// The caller must have unfiltered SELECT access to the involved Core tables.
func ReadConsistencyScope(ctx context.Context, pool *pgxpool.Pool, scope ConsistencyScope) (ConsistencyView, error) {
	if pool == nil {
		return ConsistencyView{}, fmt.Errorf("%w: postgres pool required", logicresolver.ErrInput)
	}
	scope, err := scope.normalized()
	if err != nil {
		return ConsistencyView{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ConsistencyView{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// This fails under RLS; it does not grant permission to bypass a policy.
	if _, err := tx.Exec(ctx, `SET LOCAL row_security = off`); err != nil {
		return ConsistencyView{}, err
	}
	view, err := readConsistencyScopeTx(ctx, tx, scope)
	if err != nil {
		return ConsistencyView{}, err
	}
	return view, tx.Commit(ctx)
}

func readConsistencyScopeTx(ctx context.Context, tx pgx.Tx, scope ConsistencyScope) (ConsistencyView, error) {
	view := ConsistencyView{Profile: ConsistencyProfile, Scope: scope}
	if err := tx.QueryRow(ctx, `SELECT txid_current_snapshot()::text`).Scan(&view.Snapshot); err != nil {
		return view, err
	}
	// Resolve the head across the full event history BEFORE filtering its target.
	// A NULL target from a withdrawal must never fall back to the initial binding.
	rows, err := tx.Query(ctx, `
		WITH heads AS (
		 SELECT m.canonical_node_id,
		 CASE WHEN e.request_id IS NULL THEN m.proposition_id ELSE e.target_id END proposition_id,
		 coalesce(e.revision, 0) revision,
		 CASE WHEN e.request_id IS NULL THEN 'membership:' || m.request_id ELSE 'event:' || e.request_id END ref
		 FROM canonical_proposition_bindings m LEFT JOIN LATERAL (
		  SELECT request_id, target_id, revision FROM canonical_proposition_binding_events
		  WHERE canonical_node_id=m.canonical_node_id ORDER BY revision DESC LIMIT 1
		 ) e ON true
		)
		SELECT h.canonical_node_id,p.namespace_id,p.local_id,p.scope_ref,p.revision,p.definition,h.revision,h.ref
		FROM heads h JOIN canonical_propositions p ON p.proposition_id=h.proposition_id
		WHERE p.namespace_id=$1 AND p.scope_ref=$2
		ORDER BY h.canonical_node_id COLLATE "C" LIMIT $3`, scope.Namespace, scope.ScopeRef, scope.MaxNodes+1)
	if err != nil {
		return view, err
	}
	var nodeIDs []string
	for rows.Next() {
		var m ConsistencyMember
		if err := rows.Scan(&m.NodeID, &m.Key.Namespace, &m.Key.LocalID, &m.Key.ScopeRef, &m.Key.Revision, &m.Definition, &m.HeadRevision, &m.HeadRef); err != nil {
			rows.Close()
			return view, err
		}
		view.Members = append(view.Members, m)
		nodeIDs = append(nodeIDs, m.NodeID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return view, err
	}
	if len(nodeIDs) > scope.MaxNodes {
		return view, fmt.Errorf("%w: scope nodes", logicresolver.ErrLimit)
	}
	if len(nodeIDs) > 0 {
		edges, err := consistencyScopeEdges(ctx, tx, nodeIDs, scope.MaxEdges)
		if err != nil {
			return view, err
		}
		view.Artifact, err = assembleCanonicalReadArtifact(ctx, pgxTx{tx: tx}, nodeIDs, edges, nodeIDs)
		if err != nil {
			return view, fmt.Errorf("incomplete consistency scope: %w", err)
		}
	}
	if scope.Policy == ConsistencyFrontierPolicy {
		view.Profile = ConsistencyFrontierPolicy
		if err := selectConsistencyFrontier(ctx, tx, &view); err != nil {
			return view, err
		}
	}
	view.ID, err = consistencyHash(view) // ID and transaction receipt are blanked below.
	return view, err
}

func consistencyScopeEdges(ctx context.Context, tx pgx.Tx, nodes []string, limit int) (map[string]canonicalReadEdge, error) {
	rows, err := tx.Query(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,relation,provenance
		FROM canonical_graph_edges WHERE from_node_id=ANY($1::text[]) AND to_node_id=ANY($1::text[])
		ORDER BY canonical_edge_id COLLATE "C" LIMIT $2`, nodes, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := make(map[string]canonicalReadEdge)
	for rows.Next() {
		var e canonicalReadEdge
		var data []byte
		if err := rows.Scan(&e.id, &e.from, &e.to, &e.relation, &data); err != nil {
			return nil, err
		}
		if len(edges) == limit {
			return nil, fmt.Errorf("%w: scope edges", logicresolver.ErrLimit)
		}
		if err := json.Unmarshal(data, &e.provenance); err != nil {
			return nil, err
		}
		edges[e.id] = e
	}
	return edges, rows.Err()
}

func consistencyHash(v any) (string, error) {
	if view, ok := v.(ConsistencyView); ok {
		view.ID, view.Snapshot = "", ""
		v = view
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
