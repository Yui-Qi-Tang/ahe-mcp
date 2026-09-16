package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5"
)

const (
	recursiveImplementsMaxDepth = 8
	recursiveImplementsMaxNodes = 64
	recursiveImplementsMaxEdges = 128
	recursiveImplementsMaxBytes = 8 << 20
)

// LoadRecursiveImplementsNativeBasisInTx reconstructs the complete admitted AND
// ancestor cut in one repeatable-read or serializable transaction. It accepts
// only native coordinates, not a caller-supplied graph or completeness flag.
// Every derived layer retains its own registered derivation and origin; this
// loader neither invents semantic rules nor creates transitive implements edges.
func LoadRecursiveImplementsNativeBasisInTx(ctx context.Context, tx pgx.Tx, specificationID, implementationID string) (DerivedImplementsNativeBasis, error) {
	if tx == nil {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "postgres transaction is required")
	}
	return loadRecursiveImplementsNativeBasis(ctx, pgxTx{tx: tx}, specificationID, implementationID)
}

func loadRecursiveImplementsNativeBasis(ctx context.Context, tx sqlTx, specificationID, implementationID string) (DerivedImplementsNativeBasis, error) {
	if err := validateDerivedImplementsCoordinates(specificationID, implementationID); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	if err := ctx.Err(); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	var isolation string
	if err := tx.queryRow(ctx, `SELECT current_setting('transaction_isolation')`).Scan(&isolation); err != nil {
		return DerivedImplementsNativeBasis{}, fmt.Errorf("reading recursive implements review isolation: %w", err)
	}
	if isolation != "repeatable read" && isolation != "serializable" {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "implements review requires repeatable-read or serializable isolation")
	}
	// Inspect bounded native topology before reconstructing potentially large
	// original submissions. CanonicalArtifact.Validate does not detect cycles.
	headers, err := recursiveImplementsClosure(ctx, specificationID, func(ctx context.Context, id string) (recursiveImplementsHeader, error) {
		return loadRecursiveImplementsHeader(ctx, tx, id)
	})
	if err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	nodeIDs := make([]string, 0, len(headers))
	for id := range headers {
		nodeIDs = append(nodeIDs, id)
	}
	slices.Sort(nodeIDs)
	result := DerivedImplementsNativeBasis{
		SourceLeaves: make([]DerivedImplementsSourceLeaf, 0, len(headers)),
		DerivedNodes: make([]CanonicalQueryResult, 0, len(headers)),
	}
	edges := make(map[string]canonicalReadEdge)
	materializedBytes := 0
	for _, id := range nodeIDs {
		header := headers[id]
		node, metadata, err := loadDerivedImplementsAdmittedNode(ctx, tx, id, header.kind)
		if err != nil {
			return DerivedImplementsNativeBasis{}, err
		}
		if err := recursiveImplementsConsumeJSON(&materializedBytes, node); err != nil {
			return DerivedImplementsNativeBasis{}, err
		}
		if id == specificationID {
			result.Specification = node
		}
		if header.kind == evidencegraph.CanonicalSourceClaim {
			// Reserve the original bytes and extractor output before the legacy
			// reconstruction allocates them. Final retained JSON is bounded too.
			var size int
			if err := tx.queryRow(ctx, `SELECT octet_length(b.raw_content)+octet_length(v.rendered_content)+octet_length(a.fixture_output::text)
				FROM extraction_attempts a JOIN extraction_runs r USING(extraction_run_id)
				JOIN source_snapshots s ON s.source_snapshot_id=r.source_snapshot_id JOIN source_blobs b USING(raw_content_hash)
				JOIN extraction_views v ON v.extraction_view_id=r.extraction_view_id AND v.source_snapshot_id=s.source_snapshot_id
				WHERE a.extraction_attempt_id=$1`, node.OriginProposal.ExtractionAttemptID).Scan(&size); err != nil {
				return DerivedImplementsNativeBasis{}, fmt.Errorf("preflighting recursive implements source bytes: %w", err)
			}
			if size > recursiveImplementsMaxBytes-materializedBytes {
				return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "recursive implements evidence exceeds cumulative byte limit")
			}
			leaf, err := loadDerivedImplementsSourceLeaf(ctx, tx, node)
			if err != nil {
				return DerivedImplementsNativeBasis{}, err
			}
			if err := recursiveImplementsConsumeJSON(&materializedBytes, leaf); err != nil {
				return DerivedImplementsNativeBasis{}, err
			}
			result.SourceLeaves = append(result.SourceLeaves, leaf)
			continue
		}
		if metadata.Derivation == nil || !slices.Equal(metadata.Derivation.Parents, header.parents) {
			return DerivedImplementsNativeBasis{}, newDomainError(ErrorDerivationInvariant, "recursive implements registered AND parents differ from admitted origin")
		}
		result.DerivedNodes = append(result.DerivedNodes, node)
		for _, parentID := range header.parents {
			edgeID := metadata.DerivationParentEdges[parentID]
			var edge canonicalReadEdge
			var relation, owner string
			var provenance []byte
			if err := tx.queryRow(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id
				FROM canonical_graph_edges WHERE canonical_edge_id=$1 AND octet_length(provenance::text)<=$2`, edgeID, derivedImplementsRecordBytes).
				Scan(&edge.id, &edge.from, &edge.to, &relation, &provenance, &owner); err != nil {
				return DerivedImplementsNativeBasis{}, fmt.Errorf("loading recursive implements derivation edge: %w", err)
			}
			if edge.from != parentID || edge.to != id || relation != string(evidencegraph.CanonicalDerivedFrom) || owner != node.OriginProposalOccurrenceID {
				return DerivedImplementsNativeBasis{}, newDomainError(ErrorDerivationInvariant, "recursive implements parent edge or ownership differs")
			}
			edge.relation = evidencegraph.CanonicalDerivedFrom
			if err := json.Unmarshal(provenance, &edge.provenance); err != nil {
				return DerivedImplementsNativeBasis{}, fmt.Errorf("decoding recursive implements derivation edge: %w", err)
			}
			if len(provenance) > recursiveImplementsMaxBytes-materializedBytes {
				return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "recursive implements evidence exceeds cumulative byte limit")
			}
			materializedBytes += len(provenance)
			edges[edgeID] = edge
		}
	}
	result.Ancestors, err = assembleCanonicalReadArtifact(ctx, tx, nodeIDs, edges, []string{specificationID})
	if err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	if err := recursiveImplementsConsumeJSON(&materializedBytes, result.Ancestors); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	result.Implementation, _, err = loadDerivedImplementsAdmittedNode(ctx, tx, implementationID, evidencegraph.CanonicalSourceClaim)
	if err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	if err := recursiveImplementsConsumeJSON(&materializedBytes, result.Implementation); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	if len(result.Implementation.OriginProposal.SourceRefs) != 1 || result.Implementation.OriginProposal.SourceBindingKind != ProposalSourceBindingRepositorySnapshot {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorUnsupportedAdmission, "recursive implements code requires one repository-backed source reference")
	}
	var codeBytes int
	if err := tx.queryRow(ctx, `SELECT octet_length(b.raw_content) FROM source_file_snapshots f
		JOIN source_blobs b ON b.raw_content_hash=f.blob_hash WHERE f.file_snapshot_id=$1`,
		result.Implementation.OriginProposal.SourceRefs[0].FileSnapshotID).Scan(&codeBytes); err != nil {
		return DerivedImplementsNativeBasis{}, fmt.Errorf("preflighting recursive implements code bytes: %w", err)
	}
	if codeBytes > recursiveImplementsMaxBytes-materializedBytes {
		return DerivedImplementsNativeBasis{}, newDomainError(ErrorInvalidInput, "recursive implements evidence exceeds cumulative byte limit")
	}
	if err := validateDerivedImplementsCode(ctx, tx, result.Implementation.OriginProposal); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	// Bound the complete returned structure, including duplicated root and
	// source-node representations, not only the individual stored rows.
	retainedBytes := 0
	if err := recursiveImplementsConsumeJSON(&retainedBytes, result); err != nil {
		return DerivedImplementsNativeBasis{}, err
	}
	return result, nil
}

type recursiveImplementsHeader struct {
	kind    evidencegraph.CanonicalNodeKind
	parents []string
}

func loadRecursiveImplementsHeader(ctx context.Context, tx sqlTx, nodeID string) (recursiveImplementsHeader, error) {
	var header recursiveImplementsHeader
	var kind string
	var size, refs, parents, incoming, derivations int
	err := tx.queryRow(ctx, `SELECT n.node_kind,
		octet_length(n.payload::text)+octet_length(n.provenance::text)+octet_length(n.temporal::text)+octet_length(n.integrity::text)
		+octet_length(p.source_refs::text)+octet_length(p.proposed_payload::text), jsonb_array_length(p.source_refs),
		(SELECT count(*) FROM canonical_derivation_parents dp JOIN canonical_derivations d USING(derivation_id) WHERE d.node_id=n.canonical_node_id),
		(SELECT count(*) FROM canonical_graph_edges e WHERE e.to_node_id=n.canonical_node_id AND e.relation='derived_from'),
		(SELECT count(*) FROM canonical_derivations d WHERE d.node_id=n.canonical_node_id)
		FROM canonical_graph_nodes n JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
		WHERE n.canonical_node_id=$1`, nodeID).Scan(&kind, &size, &refs, &parents, &incoming, &derivations)
	if err != nil {
		return header, fmt.Errorf("preflighting recursive implements node: %w", err)
	}
	header.kind = evidencegraph.CanonicalNodeKind(kind)
	if size > derivedImplementsRecordBytes || refs > 64 || parents > derivedImplementsMaxParents || parents != incoming {
		return header, newDomainError(ErrorInvalidInput, "recursive implements native node exceeds bounded review limits or has unregistered parent edges")
	}
	if header.kind == evidencegraph.CanonicalSourceClaim {
		if parents != 0 || derivations != 0 {
			return header, newDomainError(ErrorDerivationInvariant, "recursive implements source leaf carries a derivation")
		}
		return header, nil
	}
	if header.kind != evidencegraph.CanonicalDerivedClaim {
		return header, newDomainError(ErrorUnsupportedAdmission, "recursive implements ancestors require admitted source or derived claims")
	}
	if parents == 0 || derivations != 1 {
		return header, newDomainError(ErrorDerivationInvariant, "recursive implements derived node requires exactly one nonempty AND derivation")
	}
	rows, err := tx.query(ctx, `SELECT dp.parent_node_id FROM canonical_derivation_parents dp JOIN canonical_derivations d USING(derivation_id)
		WHERE d.node_id=$1 ORDER BY dp.parent_node_id LIMIT 9`, nodeID)
	if err != nil {
		return header, fmt.Errorf("reading recursive implements AND parents: %w", err)
	}
	defer rows.Close()
	header.parents = make([]string, 0, parents)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return header, fmt.Errorf("scanning recursive implements AND parent: %w", err)
		}
		header.parents = append(header.parents, id)
	}
	if err := rows.Err(); err != nil {
		return header, fmt.Errorf("iterating recursive implements AND parents: %w", err)
	}
	if len(header.parents) != parents {
		return header, newDomainError(ErrorDerivationInvariant, "recursive implements AND parent count differs")
	}
	return header, nil
}

// The loader callback is kept at the I/O boundary so cycle and shared-ancestor
// path limits can be tested independently of otherwise immutable database rows.
func recursiveImplementsClosure(ctx context.Context, root string, load func(context.Context, string) (recursiveImplementsHeader, error)) (map[string]recursiveImplementsHeader, error) {
	headers := make(map[string]recursiveImplementsHeader)
	active := make(map[string]bool)
	heights := make(map[string]int)
	edgeCount := 0
	var visit func(string, int) (int, error)
	visit = func(id string, derivedDepth int) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if active[id] {
			return 0, newDomainError(ErrorDerivationInvariant, "recursive implements ancestor cycle")
		}
		if height, ok := heights[id]; ok {
			if derivedDepth+height > recursiveImplementsMaxDepth {
				return 0, newDomainError(ErrorInvalidInput, "recursive implements ancestor depth exceeds limit")
			}
			return height, nil
		}
		if len(headers) == recursiveImplementsMaxNodes {
			return 0, newDomainError(ErrorInvalidInput, "recursive implements ancestor node count exceeds limit")
		}
		header, err := load(ctx, id)
		if err != nil {
			return 0, err
		}
		if header.kind != evidencegraph.CanonicalSourceClaim && header.kind != evidencegraph.CanonicalDerivedClaim {
			return 0, newDomainError(ErrorUnsupportedAdmission, "recursive implements ancestors require source or derived claims")
		}
		if (id == root && header.kind != evidencegraph.CanonicalDerivedClaim) ||
			(header.kind == evidencegraph.CanonicalSourceClaim && len(header.parents) != 0) ||
			(header.kind == evidencegraph.CanonicalDerivedClaim && (len(header.parents) < 1 || len(header.parents) > derivedImplementsMaxParents)) {
			return 0, newDomainError(ErrorDerivationInvariant, "recursive implements ancestor has invalid AND parents")
		}
		header.parents = slices.Clone(header.parents)
		slices.Sort(header.parents)
		if len(slices.Compact(slices.Clone(header.parents))) != len(header.parents) {
			return 0, newDomainError(ErrorDerivationInvariant, "recursive implements ancestor has duplicate AND parents")
		}
		headers[id] = header
		edgeCount += len(header.parents)
		if edgeCount > recursiveImplementsMaxEdges {
			return 0, newDomainError(ErrorInvalidInput, "recursive implements ancestor edge count exceeds limit")
		}
		if header.kind == evidencegraph.CanonicalSourceClaim {
			heights[id] = 0
			return 0, nil
		}
		if derivedDepth == recursiveImplementsMaxDepth {
			return 0, newDomainError(ErrorInvalidInput, "recursive implements ancestor depth exceeds limit")
		}
		active[id] = true
		height := 1
		for _, parent := range header.parents {
			parentHeight, err := visit(parent, derivedDepth+1)
			if err != nil {
				return 0, err
			}
			height = max(height, parentHeight+1)
		}
		delete(active, id)
		heights[id] = height
		return height, nil
	}
	if _, err := visit(root, 0); err != nil {
		return nil, err
	}
	return headers, nil
}

func recursiveImplementsConsumeJSON(used *int, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding recursive implements materialized basis: %w", err)
	}
	if len(body) > recursiveImplementsMaxBytes-*used {
		return newDomainError(ErrorInvalidInput, "recursive implements evidence exceeds cumulative byte limit")
	}
	*used += len(body)
	return nil
}
