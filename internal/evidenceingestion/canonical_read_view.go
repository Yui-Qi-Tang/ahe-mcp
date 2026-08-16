package evidenceingestion

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	canonicalReadMaxRoots = 32
	canonicalReadMaxDepth = 8
	canonicalReadMaxNodes = 1024
	canonicalReadMaxEdges = 4096
)

// CanonicalReadInput defines one bounded PostgreSQL canonical graph read.
// Empty Relations means all canonical relation types.
type CanonicalReadInput struct {
	RootNodeIDs []string
	Relations   []evidencegraph.CanonicalEdgeRelation
	MaxDepth    int
	MaxNodes    int
	MaxEdges    int
}

// CanonicalReadView is a repeatable-read projection around explicit roots.
// Truncated reports budget truncation; MaxDepth remains an intentional scope.
type CanonicalReadView struct {
	Artifact    evidencegraph.CanonicalArtifact
	RootNodeIDs []string
	Relations   []evidencegraph.CanonicalEdgeRelation
	MaxDepth    int
	MaxNodes    int
	MaxEdges    int
	Truncated   bool
}

type canonicalReadEdge struct {
	id         string
	from       string
	to         string
	relation   evidencegraph.CanonicalEdgeRelation
	provenance evidencegraph.ProvenanceRecord
}

// ReadCanonicalGraphView loads one bounded, read-consistent canonical graph
// view. It does not create graph authority, admission state, or a DB revision.
func ReadCanonicalGraphView(ctx context.Context, pool *pgxpool.Pool, input CanonicalReadInput) (CanonicalReadView, error) {
	if pool == nil {
		return CanonicalReadView{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return readCanonicalGraphView(ctx, pgxDB{pool: pool}, input)
}

func readCanonicalGraphView(ctx context.Context, db sqlDB, input CanonicalReadInput) (CanonicalReadView, error) {
	input, err := normalizeCanonicalReadInput(input)
	if err != nil {
		return CanonicalReadView{}, err
	}
	view := CanonicalReadView{
		RootNodeIDs: append([]string(nil), input.RootNodeIDs...),
		Relations:   append([]evidencegraph.CanonicalEdgeRelation(nil), input.Relations...),
		MaxDepth:    input.MaxDepth,
		MaxNodes:    input.MaxNodes,
		MaxEdges:    input.MaxEdges,
	}
	err = withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		artifact, truncated, err := loadCanonicalReadArtifact(ctx, tx, input)
		if err != nil {
			return err
		}
		view.Artifact = artifact
		view.Truncated = truncated
		return nil
	})
	if err != nil {
		return CanonicalReadView{}, err
	}
	return view, nil
}

func normalizeCanonicalReadInput(input CanonicalReadInput) (CanonicalReadInput, error) {
	if len(input.RootNodeIDs) == 0 {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "at least one root canonical node is required")
	}
	if len(input.RootNodeIDs) > canonicalReadMaxRoots {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "root canonical nodes must be at most %d", canonicalReadMaxRoots)
	}
	if input.MaxDepth < 0 || input.MaxDepth > canonicalReadMaxDepth {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "max depth must be between 0 and %d", canonicalReadMaxDepth)
	}
	if input.MaxNodes < 1 || input.MaxNodes > canonicalReadMaxNodes {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "max nodes must be between 1 and %d", canonicalReadMaxNodes)
	}
	if input.MaxEdges < 0 || input.MaxEdges > canonicalReadMaxEdges {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "max edges must be between 0 and %d", canonicalReadMaxEdges)
	}
	if input.MaxDepth > 0 && input.MaxEdges == 0 {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "max edges must be positive when max depth is positive")
	}

	rootValues := append([]string(nil), input.RootNodeIDs...)
	roots := make(map[string]struct{}, len(rootValues))
	input.RootNodeIDs = input.RootNodeIDs[:0]
	for _, root := range rootValues {
		root = strings.TrimSpace(root)
		if !strings.HasPrefix(root, "canon-node:") {
			return CanonicalReadInput{}, newDomainError(ErrorInvalidRecordID, "canonical node %q must start with canon-node:", root)
		}
		if _, exists := roots[root]; exists {
			continue
		}
		roots[root] = struct{}{}
		input.RootNodeIDs = append(input.RootNodeIDs, root)
	}
	if len(input.RootNodeIDs) > input.MaxNodes {
		return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "max nodes must cover every root canonical node")
	}
	slices.Sort(input.RootNodeIDs)

	allowed := make(map[evidencegraph.CanonicalEdgeRelation]bool, len(evidencegraph.CanonicalRelations()))
	for _, relation := range evidencegraph.CanonicalRelations() {
		allowed[relation] = true
	}
	relationValues := append([]evidencegraph.CanonicalEdgeRelation(nil), input.Relations...)
	relations := make(map[evidencegraph.CanonicalEdgeRelation]struct{}, len(relationValues))
	input.Relations = input.Relations[:0]
	for _, relation := range relationValues {
		if !allowed[relation] {
			return CanonicalReadInput{}, newDomainError(ErrorInvalidInput, "canonical relation %q is not supported", relation)
		}
		if _, exists := relations[relation]; exists {
			continue
		}
		relations[relation] = struct{}{}
		input.Relations = append(input.Relations, relation)
	}
	slices.Sort(input.Relations)
	return input, nil
}

func loadCanonicalReadArtifact(ctx context.Context, tx sqlTx, input CanonicalReadInput) (evidencegraph.CanonicalArtifact, bool, error) {
	selected := make(map[string]struct{}, input.MaxNodes)
	for _, root := range input.RootNodeIDs {
		selected[root] = struct{}{}
	}
	frontier := append([]string(nil), input.RootNodeIDs...)
	edges := make(map[string]canonicalReadEdge, input.MaxEdges)
	truncated := false

	for depth := 0; depth < input.MaxDepth && len(frontier) > 0; depth++ {
		remaining := input.MaxEdges - len(edges)
		batch, hasMore, err := queryCanonicalReadEdges(ctx, tx, frontier, input.Relations, sortedCanonicalReadEdgeIDs(edges), remaining)
		if err != nil {
			return evidencegraph.CanonicalArtifact{}, false, err
		}
		truncated = truncated || hasMore
		next := map[string]struct{}{}
		for _, edge := range batch {
			neighbor := edge.from
			if _, exists := selected[neighbor]; exists {
				neighbor = edge.to
			}
			if _, exists := selected[neighbor]; !exists {
				if len(selected) == input.MaxNodes {
					truncated = true
					continue
				}
				selected[neighbor] = struct{}{}
				next[neighbor] = struct{}{}
			}
			edges[edge.id] = edge
		}
		frontier = sortedCanonicalReadNodeIDs(next)
		if hasMore {
			break
		}
	}

	artifact, err := assembleCanonicalReadArtifact(ctx, tx, sortedCanonicalReadNodeIDs(selected), edges, input.RootNodeIDs)
	if err != nil {
		return evidencegraph.CanonicalArtifact{}, false, err
	}
	return artifact, truncated, nil
}

func queryCanonicalReadEdges(
	ctx context.Context,
	tx sqlTx,
	frontier []string,
	relations []evidencegraph.CanonicalEdgeRelation,
	seen []string,
	limit int,
) ([]canonicalReadEdge, bool, error) {
	relationValues := make([]string, len(relations))
	for i, relation := range relations {
		relationValues[i] = string(relation)
	}
	rows, err := tx.query(ctx, `
		SELECT canonical_edge_id, from_node_id, to_node_id, relation, provenance
		FROM canonical_graph_edges
		WHERE (from_node_id = ANY($1::text[]) OR to_node_id = ANY($1::text[]))
		  AND (cardinality($2::text[]) = 0 OR relation = ANY($2::text[]))
		  AND NOT (canonical_edge_id = ANY($3::text[]))
		ORDER BY canonical_edge_id
		LIMIT $4
	`, frontier, relationValues, seen, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("querying bounded canonical edges: %w", err)
	}
	defer rows.Close()

	edges := make([]canonicalReadEdge, 0, limit)
	for rows.Next() {
		var edge canonicalReadEdge
		var relation string
		var provenanceData []byte
		if err := rows.Scan(&edge.id, &edge.from, &edge.to, &relation, &provenanceData); err != nil {
			return nil, false, fmt.Errorf("scanning bounded canonical edge: %w", err)
		}
		if len(edges) == limit {
			return edges, true, nil
		}
		edge.relation = evidencegraph.CanonicalEdgeRelation(relation)
		if err := json.Unmarshal(provenanceData, &edge.provenance); err != nil {
			return nil, false, fmt.Errorf("decoding canonical edge provenance: %w", err)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterating bounded canonical edges: %w", err)
	}
	return edges, false, nil
}

func assembleCanonicalReadArtifact(
	ctx context.Context,
	tx sqlTx,
	nodeIDs []string,
	edges map[string]canonicalReadEdge,
	roots []string,
) (evidencegraph.CanonicalArtifact, error) {
	rows, err := tx.query(ctx, `
		SELECT canonical_node_id, node_kind, payload, provenance, temporal, integrity
		FROM canonical_graph_nodes
		WHERE canonical_node_id = ANY($1::text[])
		ORDER BY canonical_node_id
	`, nodeIDs)
	if err != nil {
		return evidencegraph.CanonicalArtifact{}, fmt.Errorf("querying bounded canonical nodes: %w", err)
	}
	defer rows.Close()

	artifact := evidencegraph.CanonicalArtifact{SchemaVersion: evidencegraph.CanonicalSchemaVersion}
	payloads := map[string][]byte{}
	provenance := map[string][]byte{}
	temporal := map[string][]byte{}
	integrity := map[string][]byte{}
	loadedNodes := map[string]struct{}{}
	var derivedNodeIDs []string
	for rows.Next() {
		var node evidencegraph.CanonicalNode
		var kind string
		var payloadData, provenanceData, temporalData, integrityData []byte
		if err := rows.Scan(&node.ID, &kind, &payloadData, &provenanceData, &temporalData, &integrityData); err != nil {
			return evidencegraph.CanonicalArtifact{}, fmt.Errorf("scanning bounded canonical node: %w", err)
		}
		node.Kind = evidencegraph.CanonicalNodeKind(kind)
		if node.Kind == evidencegraph.CanonicalDerivedClaim || node.Kind == evidencegraph.CanonicalCandidate {
			derivedNodeIDs = append(derivedNodeIDs, node.ID)
		}
		var payload evidencegraph.EvidencePayload
		var nodeProvenance evidencegraph.ProvenanceRecord
		var nodeTemporal evidencegraph.TemporalRecord
		var nodeIntegrity evidencegraph.IntegrityRecord
		if err := json.Unmarshal(payloadData, &payload); err != nil {
			return evidencegraph.CanonicalArtifact{}, fmt.Errorf("decoding canonical node %q payload: %w", node.ID, err)
		}
		if err := json.Unmarshal(provenanceData, &nodeProvenance); err != nil {
			return evidencegraph.CanonicalArtifact{}, fmt.Errorf("decoding canonical node %q provenance: %w", node.ID, err)
		}
		if err := json.Unmarshal(temporalData, &nodeTemporal); err != nil {
			return evidencegraph.CanonicalArtifact{}, fmt.Errorf("decoding canonical node %q temporal state: %w", node.ID, err)
		}
		if err := json.Unmarshal(integrityData, &nodeIntegrity); err != nil {
			return evidencegraph.CanonicalArtifact{}, fmt.Errorf("decoding canonical node %q integrity: %w", node.ID, err)
		}
		node.PayloadRef = payload.ID
		node.ProvenanceRef = nodeProvenance.ID
		node.TemporalRef = nodeTemporal.ID
		node.IntegrityRef = nodeIntegrity.ID
		artifact.Nodes = append(artifact.Nodes, node)
		if err := appendCanonicalReadRecord("payload", payload.ID, payload, &artifact.Payloads, payloads); err != nil {
			return evidencegraph.CanonicalArtifact{}, err
		}
		if err := appendCanonicalReadRecord("provenance", nodeProvenance.ID, nodeProvenance, &artifact.Provenance, provenance); err != nil {
			return evidencegraph.CanonicalArtifact{}, err
		}
		if err := appendCanonicalReadRecord("temporal", nodeTemporal.ID, nodeTemporal, &artifact.Temporal, temporal); err != nil {
			return evidencegraph.CanonicalArtifact{}, err
		}
		if err := appendCanonicalReadRecord("integrity", nodeIntegrity.ID, nodeIntegrity, &artifact.Integrity, integrity); err != nil {
			return evidencegraph.CanonicalArtifact{}, err
		}
		loadedNodes[node.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return evidencegraph.CanonicalArtifact{}, fmt.Errorf("iterating bounded canonical nodes: %w", err)
	}
	for _, root := range roots {
		if _, exists := loadedNodes[root]; !exists {
			return evidencegraph.CanonicalArtifact{}, fmt.Errorf("root canonical node %q was not found", root)
		}
	}
	if len(loadedNodes) != len(nodeIDs) {
		return evidencegraph.CanonicalArtifact{}, fmt.Errorf("bounded canonical view contains a missing edge endpoint")
	}

	for _, edgeID := range sortedCanonicalReadEdgeIDs(edges) {
		edge := edges[edgeID]
		if _, exists := loadedNodes[edge.from]; !exists {
			continue
		}
		if _, exists := loadedNodes[edge.to]; !exists {
			continue
		}
		artifact.Edges = append(artifact.Edges, evidencegraph.CanonicalEdge{
			ID:            edge.id,
			From:          edge.from,
			To:            edge.to,
			Relation:      edge.relation,
			ProvenanceRef: edge.provenance.ID,
		})
		if err := appendCanonicalReadRecord("provenance", edge.provenance.ID, edge.provenance, &artifact.Provenance, provenance); err != nil {
			return evidencegraph.CanonicalArtifact{}, err
		}
	}
	derivations, err := loadCanonicalReadDerivations(ctx, tx, derivedNodeIDs, loadedNodes, edges)
	if err != nil {
		return evidencegraph.CanonicalArtifact{}, err
	}
	artifact.Derivations = derivations
	sortCanonicalReadArtifact(&artifact)
	snapshotID, err := canonicalReadSnapshotID(artifact)
	if err != nil {
		return evidencegraph.CanonicalArtifact{}, err
	}
	artifact.SnapshotID = snapshotID
	if err := artifact.Validate(); err != nil {
		return evidencegraph.CanonicalArtifact{}, fmt.Errorf("validating PostgreSQL canonical read view: %w", err)
	}
	return artifact, nil
}

func loadCanonicalReadDerivations(
	ctx context.Context,
	tx sqlTx,
	derivedNodeIDs []string,
	loadedNodes map[string]struct{},
	edges map[string]canonicalReadEdge,
) ([]evidencegraph.DerivationRecord, error) {
	if len(derivedNodeIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.query(ctx, `
		SELECT
			d.derivation_id,
			d.node_id,
			d.method,
			d.producer,
			d.trace_ref,
			d.provenance_ref,
			p.parent_node_id,
			p.canonical_edge_id
		FROM canonical_derivations d
		JOIN canonical_derivation_parents p ON p.derivation_id = d.derivation_id
		WHERE d.node_id = ANY($1::text[])
		ORDER BY d.node_id, p.parent_node_id
	`, derivedNodeIDs)
	if err != nil {
		return nil, fmt.Errorf("querying bounded canonical derivations: %w", err)
	}
	defer rows.Close()

	byNode := make(map[string]*evidencegraph.DerivationRecord, len(derivedNodeIDs))
	for rows.Next() {
		var derivationID, nodeID, method, producer, traceRef, provenanceRef, parentID, edgeID string
		if err := rows.Scan(
			&derivationID,
			&nodeID,
			&method,
			&producer,
			&traceRef,
			&provenanceRef,
			&parentID,
			&edgeID,
		); err != nil {
			return nil, fmt.Errorf("scanning bounded canonical derivation: %w", err)
		}
		if _, exists := loadedNodes[parentID]; !exists {
			return nil, fmt.Errorf(
				"canonical node %q requires derivation parent %q outside the bounded PostgreSQL view",
				nodeID,
				parentID,
			)
		}
		edge, exists := edges[edgeID]
		if !exists || edge.from != parentID || edge.to != nodeID || edge.relation != evidencegraph.CanonicalDerivedFrom {
			return nil, fmt.Errorf(
				"canonical derivation %q parent %q has no matching derived_from edge in the bounded PostgreSQL view",
				derivationID,
				parentID,
			)
		}
		derivation, exists := byNode[nodeID]
		if !exists {
			derivation = &evidencegraph.DerivationRecord{
				ID:            derivationID,
				NodeID:        nodeID,
				Method:        method,
				Producer:      producer,
				TraceRef:      traceRef,
				ProvenanceRef: provenanceRef,
			}
			byNode[nodeID] = derivation
		} else if derivation.ID != derivationID || derivation.Method != method || derivation.Producer != producer || derivation.TraceRef != traceRef || derivation.ProvenanceRef != provenanceRef {
			return nil, fmt.Errorf("canonical node %q has conflicting persisted derivation data", nodeID)
		}
		derivation.Parents = append(derivation.Parents, parentID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating bounded canonical derivations: %w", err)
	}

	derivations := make([]evidencegraph.DerivationRecord, 0, len(derivedNodeIDs))
	for _, nodeID := range derivedNodeIDs {
		derivation, exists := byNode[nodeID]
		if !exists {
			return nil, fmt.Errorf("canonical node %q requires derivation data unavailable in PostgreSQL", nodeID)
		}
		derivations = append(derivations, *derivation)
	}
	return derivations, nil
}

func appendCanonicalReadRecord[T any](kind, id string, value T, values *[]T, seen map[string][]byte) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding canonical %s %q: %w", kind, id, err)
	}
	previous, exists := seen[id]
	if !exists {
		seen[id] = data
		*values = append(*values, value)
		return nil
	}
	if !bytes.Equal(previous, data) {
		return fmt.Errorf("canonical %s %q has conflicting values", kind, id)
	}
	return nil
}

func sortCanonicalReadArtifact(artifact *evidencegraph.CanonicalArtifact) {
	slices.SortFunc(artifact.Nodes, func(a, b evidencegraph.CanonicalNode) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(artifact.Edges, func(a, b evidencegraph.CanonicalEdge) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(artifact.Payloads, func(a, b evidencegraph.EvidencePayload) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(artifact.Provenance, func(a, b evidencegraph.ProvenanceRecord) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(artifact.Temporal, func(a, b evidencegraph.TemporalRecord) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(artifact.Integrity, func(a, b evidencegraph.IntegrityRecord) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(artifact.Derivations, func(a, b evidencegraph.DerivationRecord) int { return cmp.Compare(a.ID, b.ID) })
}

func canonicalReadSnapshotID(artifact evidencegraph.CanonicalArtifact) (string, error) {
	artifact.SnapshotID = ""
	data, err := json.Marshal(artifact)
	if err != nil {
		return "", fmt.Errorf("encoding PostgreSQL canonical read identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return "postgres-read-view:" + hex.EncodeToString(digest[:]), nil
}

func sortedCanonicalReadNodeIDs[T any](values map[string]T) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func sortedCanonicalReadEdgeIDs(values map[string]canonicalReadEdge) []string {
	return sortedCanonicalReadNodeIDs(values)
}
