package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5"
)

const (
	ordinaryAdmissionContractV1 = "ordinary-admission/v1"

	ordinaryAdmissionSourceBacked = "source_backed_claim"
	ordinaryAdmissionDerived      = "derived_claim"

	canonicalAdmissionRoleCanonicalRef = "canonical_ref"
	canonicalAdmissionRoleRawEvidence  = "raw_evidence"
	canonicalAdmissionRoleSupports     = "supports_claim"
	canonicalAdmissionRoleDerivedFrom  = "derived_from"
)

type canonicalRowMaterialization string

const (
	canonicalRowMaterialized canonicalRowMaterialization = "materialized"
	canonicalRowReused       canonicalRowMaterialization = "reused"
)

type canonicalAdmissionNodeBinding struct {
	Position        int
	Role            string
	NodeID          string
	Materialization canonicalRowMaterialization
}

type canonicalAdmissionEdgeBinding struct {
	Position        int
	Role            string
	EdgeID          string
	Materialization canonicalRowMaterialization
}

type canonicalAdmissionWriteResult struct {
	Nodes []canonicalAdmissionNodeBinding
	Edges []canonicalAdmissionEdgeBinding
}

type persistedOrdinaryAdmissionAuthority struct {
	ContractVersion string
	MutationKind    string
	ProposalID      string
	CanonicalRef    string
	Outcome         string
	Nodes           []canonicalAdmissionNodeBinding
	Edges           []canonicalAdmissionEdgeBinding
}

// Ordinary source-backed and derived claims share this exact ordered receipt.
// Supersession and contradiction retain their independent mutation authorities.
func persistOrdinaryCanonicalMutation(
	ctx context.Context,
	tx sqlTx,
	mutation canonicalAdmissionMutation,
) (canonicalAdmissionWriteResult, error) {
	if err := validateCanonicalAdmissionMutationIdentity(mutation); err != nil {
		return canonicalAdmissionWriteResult{}, err
	}

	allowReuse := mutation.derivation == nil
	result := canonicalAdmissionWriteResult{
		Nodes: make([]canonicalAdmissionNodeBinding, 0, len(mutation.nodes)),
		Edges: make([]canonicalAdmissionEdgeBinding, 0, len(mutation.edges)),
	}
	for index, node := range mutation.nodes {
		materialization, err := insertOrValidateCanonicalNode(ctx, tx, node, allowReuse)
		if err != nil {
			return canonicalAdmissionWriteResult{}, err
		}
		role := canonicalAdmissionRoleCanonicalRef
		position := 0
		if index > 0 {
			role = canonicalAdmissionRoleRawEvidence
			position = index - 1
		}
		result.Nodes = append(result.Nodes, canonicalAdmissionNodeBinding{
			Position:        position,
			Role:            role,
			NodeID:          node.ID,
			Materialization: materialization,
		})
	}
	for index, edge := range mutation.edges {
		materialization, err := insertOrValidateCanonicalEdge(ctx, tx, edge, allowReuse)
		if err != nil {
			return canonicalAdmissionWriteResult{}, err
		}
		role := canonicalAdmissionRoleSupports
		if edge.Relation == evidencegraph.CanonicalDerivedFrom {
			role = canonicalAdmissionRoleDerivedFrom
		}
		result.Edges = append(result.Edges, canonicalAdmissionEdgeBinding{
			Position:        index,
			Role:            role,
			EdgeID:          edge.ID,
			Materialization: materialization,
		})
	}
	return result, nil
}

func validateCanonicalAdmissionMutationIdentity(mutation canonicalAdmissionMutation) error {
	nodeIDs := make(map[string]struct{}, len(mutation.nodes))
	for _, node := range mutation.nodes {
		if _, exists := nodeIDs[node.ID]; exists {
			return newDomainError(
				ErrorCanonicalAdmissionInvariant,
				"ordinary admission mutation repeats canonical node %s",
				node.ID,
			)
		}
		nodeIDs[node.ID] = struct{}{}
	}
	edgeIDs := make(map[string]struct{}, len(mutation.edges))
	edgeTriples := make(map[string]struct{}, len(mutation.edges))
	for _, edge := range mutation.edges {
		if _, exists := edgeIDs[edge.ID]; exists {
			return newDomainError(
				ErrorCanonicalAdmissionInvariant,
				"ordinary admission mutation repeats canonical edge %s",
				edge.ID,
			)
		}
		edgeIDs[edge.ID] = struct{}{}
		triple := edge.From + "\x00" + edge.To + "\x00" + string(edge.Relation)
		if _, exists := edgeTriples[triple]; exists {
			return newDomainError(
				ErrorCanonicalAdmissionInvariant,
				"ordinary admission mutation repeats canonical edge endpoints and relation",
			)
		}
		edgeTriples[triple] = struct{}{}
	}
	return nil
}

func insertOrValidateCanonicalNode(
	ctx context.Context,
	tx sqlTx,
	node CanonicalGraphNode,
	allowReuse bool,
) (canonicalRowMaterialization, error) {
	payload, err := jsonBytes(node.Payload)
	if err != nil {
		return "", err
	}
	provenance, err := jsonBytes(node.Provenance)
	if err != nil {
		return "", err
	}
	temporal, err := jsonBytes(node.Temporal)
	if err != nil {
		return "", err
	}
	integrity, err := jsonBytes(node.Integrity)
	if err != nil {
		return "", err
	}
	tag, err := tx.exec(ctx, `
		INSERT INTO canonical_graph_nodes (
			canonical_node_id,
			node_kind,
			payload,
			provenance,
			temporal,
			integrity,
			origin_proposal_occurrence_id
		)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7)
		ON CONFLICT (canonical_node_id) DO NOTHING
	`,
		node.ID,
		string(node.Kind),
		string(payload),
		string(provenance),
		string(temporal),
		string(integrity),
		node.OriginProposalOccurrenceID,
	)
	if err != nil {
		return "", fmt.Errorf("inserting canonical graph node %s: %w", node.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return canonicalRowMaterialized, nil
	}
	if !allowReuse {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical node %s must be newly materialized",
			node.ID,
		)
	}
	originProposalID, matches, err := canonicalGraphNodeRowMatches(ctx, tx, node)
	if err != nil {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical node %s conflict could not be resolved: %v",
			node.ID,
			err,
		)
	}
	if !matches {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical node %s conflicts with the expected semantic body",
			node.ID,
		)
	}
	if originProposalID == node.OriginProposalOccurrenceID {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical node %s claims current proposal ownership without materialization",
			node.ID,
		)
	}
	if err := validateCanonicalNodeFirstMaterializerAuthority(ctx, tx, node.ID, originProposalID); err != nil {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical node %s lacks independent first-materializer authority: %v",
			node.ID,
			err,
		)
	}
	return canonicalRowReused, nil
}

func insertOrValidateCanonicalEdge(
	ctx context.Context,
	tx sqlTx,
	edge CanonicalGraphEdge,
	allowReuse bool,
) (canonicalRowMaterialization, error) {
	provenance, err := jsonBytes(edge.Provenance)
	if err != nil {
		return "", err
	}
	tag, err := tx.exec(ctx, `
		INSERT INTO canonical_graph_edges (
			canonical_edge_id,
			from_node_id,
			to_node_id,
			relation,
			provenance,
			origin_proposal_occurrence_id
		)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		ON CONFLICT DO NOTHING
	`,
		edge.ID,
		edge.From,
		edge.To,
		string(edge.Relation),
		string(provenance),
		edge.OriginProposalOccurrenceID,
	)
	if err != nil {
		return "", fmt.Errorf("inserting canonical graph edge %s: %w", edge.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return canonicalRowMaterialized, nil
	}
	if !allowReuse {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical edge %s must be newly materialized",
			edge.ID,
		)
	}
	candidates, err := loadCanonicalEdgeCollisionCandidates(ctx, tx, edge)
	if err != nil {
		return "", err
	}
	if len(candidates) != 1 {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical edge %s conflicts by ID or endpoint-relation identity",
			edge.ID,
		)
	}
	originProposalID, matches, err := canonicalGraphEdgeRowMatches(ctx, tx, edge)
	if err != nil || !matches || originProposalID == edge.OriginProposalOccurrenceID {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical edge %s conflicts by ID, endpoint-relation identity, or semantic body",
			edge.ID,
		)
	}
	if err := validateCanonicalEdgeFirstMaterializerAuthority(ctx, tx, edge.ID, originProposalID); err != nil {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"canonical edge %s lacks independent first-materializer authority: %v",
			edge.ID,
			err,
		)
	}
	return canonicalRowReused, nil
}

func loadCanonicalEdgeCollisionCandidates(
	ctx context.Context,
	tx sqlTx,
	expected CanonicalGraphEdge,
) ([]CanonicalGraphEdge, error) {
	rows, err := tx.query(ctx, `
		SELECT
			canonical_edge_id,
			from_node_id,
			to_node_id,
			relation,
			provenance,
			origin_proposal_occurrence_id
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
		   OR (
			from_node_id = $2
			AND to_node_id = $3
			AND relation = $4
			   )
		ORDER BY canonical_edge_id
	`, expected.ID, expected.From, expected.To, string(expected.Relation))
	if err != nil {
		return nil, fmt.Errorf("loading canonical edge collision %s: %w", expected.ID, err)
	}
	defer rows.Close()
	var result []CanonicalGraphEdge
	for rows.Next() {
		var edge CanonicalGraphEdge
		var relation string
		var provenance []byte
		if err := rows.Scan(
			&edge.ID,
			&edge.From,
			&edge.To,
			&relation,
			&provenance,
			&edge.OriginProposalOccurrenceID,
		); err != nil {
			return nil, fmt.Errorf("scanning canonical edge collision %s: %w", expected.ID, err)
		}
		edge.Relation = evidencegraph.CanonicalEdgeRelation(relation)
		if err := json.Unmarshal(provenance, &edge.Provenance); err != nil {
			return nil, fmt.Errorf("decoding canonical edge collision %s: %w", expected.ID, err)
		}
		result = append(result, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating canonical edge collision %s: %w", expected.ID, err)
	}
	return result, nil
}

func canonicalGraphNodeRowMatches(
	ctx context.Context,
	db sqlQueryer,
	expected CanonicalGraphNode,
) (string, bool, error) {
	payload, err := jsonBytes(expected.Payload)
	if err != nil {
		return "", false, err
	}
	provenance, err := jsonBytes(expected.Provenance)
	if err != nil {
		return "", false, err
	}
	temporal, err := jsonBytes(expected.Temporal)
	if err != nil {
		return "", false, err
	}
	integrity, err := jsonBytes(expected.Integrity)
	if err != nil {
		return "", false, err
	}
	var originProposalID string
	var matches bool
	err = db.queryRow(ctx, `
		SELECT /* canonical-node-exact-semantic-row */
			(
				node_kind = $2
				AND payload = $3::jsonb
				AND provenance = $4::jsonb
				AND temporal = $5::jsonb
				AND integrity = $6::jsonb
			),
			origin_proposal_occurrence_id
		FROM canonical_graph_nodes
		WHERE canonical_node_id = $1
	`,
		expected.ID,
		string(expected.Kind),
		string(payload),
		string(provenance),
		string(temporal),
		string(integrity),
	).Scan(&matches, &originProposalID)
	if err != nil {
		return "", false, err
	}
	return originProposalID, matches, nil
}

func canonicalGraphEdgeRowMatches(
	ctx context.Context,
	db sqlQueryer,
	expected CanonicalGraphEdge,
) (string, bool, error) {
	provenance, err := jsonBytes(expected.Provenance)
	if err != nil {
		return "", false, err
	}
	var originProposalID string
	var matches bool
	err = db.queryRow(ctx, `
		SELECT /* canonical-edge-exact-semantic-row */
			(
				from_node_id = $2
				AND to_node_id = $3
				AND relation = $4
				AND provenance = $5::jsonb
				AND origin_canonical_contradiction_proposal_id IS NULL
			),
			origin_proposal_occurrence_id
		FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`,
		expected.ID,
		expected.From,
		expected.To,
		string(expected.Relation),
		string(provenance),
	).Scan(&matches, &originProposalID)
	if err != nil {
		return "", false, err
	}
	return originProposalID, matches, nil
}

func insertOrdinaryAdmissionAuthority(
	ctx context.Context,
	tx sqlTx,
	mutation canonicalAdmissionMutation,
	write canonicalAdmissionWriteResult,
) error {
	mutationKind, err := ordinaryAdmissionMutationKind(mutation)
	if err != nil {
		return err
	}
	if _, err := tx.exec(ctx, `
		INSERT INTO canonical_ordinary_admission_manifests (
			admission_decision_id,
			proposal_occurrence_id,
			contract_version,
			mutation_kind,
			canonical_ref,
			admission_outcome
		)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		mutation.decision.ID,
		mutation.decision.ProposalOccurrence,
		ordinaryAdmissionContractV1,
		mutationKind,
		mutation.decision.CanonicalRef,
		mutation.decision.Outcome,
	); err != nil {
		return fmt.Errorf("inserting ordinary admission manifest %s: %w", mutation.decision.ID, err)
	}
	for _, binding := range write.Nodes {
		if _, err := tx.exec(ctx, `
			INSERT INTO canonical_ordinary_admission_node_bindings (
				admission_decision_id,
				binding_role,
				binding_position,
				canonical_node_id,
				materialization
			)
			VALUES ($1, $2, $3, $4, $5)
		`,
			mutation.decision.ID,
			binding.Role,
			binding.Position,
			binding.NodeID,
			string(binding.Materialization),
		); err != nil {
			return fmt.Errorf("inserting ordinary admission node binding %s/%s: %w", mutation.decision.ID, binding.NodeID, err)
		}
	}
	for _, binding := range write.Edges {
		if _, err := tx.exec(ctx, `
			INSERT INTO canonical_ordinary_admission_edge_bindings (
				admission_decision_id,
				binding_role,
				binding_position,
				canonical_edge_id,
				materialization
			)
			VALUES ($1, $2, $3, $4, $5)
		`,
			mutation.decision.ID,
			binding.Role,
			binding.Position,
			binding.EdgeID,
			string(binding.Materialization),
		); err != nil {
			return fmt.Errorf("inserting ordinary admission edge binding %s/%s: %w", mutation.decision.ID, binding.EdgeID, err)
		}
	}
	return nil
}

func validatePersistedOrdinaryAdmissionMutation(
	ctx context.Context,
	tx sqlTx,
	proposal ProposalQueryResult,
	input AdmissionInput,
) error {
	expected, err := buildCanonicalAdmissionMutation(proposal, input)
	if err != nil {
		return admissionReplayMutationBuildError(proposal.ProposalOccurrenceID, err)
	}
	authority, err := loadOrdinaryAdmissionAuthority(ctx, tx, expected.decision.ID)
	if err != nil {
		return newDomainError(
			ErrorAdmissionReplayConflict,
			"proposal %s ordinary admission authority is unavailable: %v",
			proposal.ProposalOccurrenceID,
			err,
		)
	}
	wantKind, err := ordinaryAdmissionMutationKind(expected)
	if err != nil {
		return admissionReplayMutationBuildError(proposal.ProposalOccurrenceID, err)
	}
	if authority.ContractVersion != ordinaryAdmissionContractV1 ||
		authority.MutationKind != wantKind ||
		authority.ProposalID != proposal.ProposalOccurrenceID ||
		authority.CanonicalRef != expected.result.CanonicalRef ||
		authority.Outcome != admissionOutcomeAdmitted ||
		len(authority.Nodes) != len(expected.nodes) ||
		len(authority.Edges) != len(expected.edges) {
		return newDomainError(
			ErrorAdmissionReplayConflict,
			"proposal %s ordinary admission manifest differs from its expected mutation",
			proposal.ProposalOccurrenceID,
		)
	}

	materializedNodes := make([]string, 0, len(authority.Nodes))
	for index, binding := range authority.Nodes {
		wantRole := canonicalAdmissionRoleCanonicalRef
		wantPosition := 0
		if index > 0 {
			wantRole = canonicalAdmissionRoleRawEvidence
			wantPosition = index - 1
		}
		expectedNode := expected.nodes[index]
		if binding.Role != wantRole || binding.Position != wantPosition || binding.NodeID != expectedNode.ID {
			return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "node binding order or identity differs")
		}
		actualOriginProposalID, matches, err := canonicalGraphNodeRowMatches(ctx, tx, expectedNode)
		if err != nil || !matches {
			return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "canonical node body differs")
		}
		switch binding.Materialization {
		case canonicalRowMaterialized:
			if actualOriginProposalID != proposal.ProposalOccurrenceID {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "materialized node owner differs")
			}
			materializedNodes = append(materializedNodes, binding.NodeID)
		case canonicalRowReused:
			if actualOriginProposalID == proposal.ProposalOccurrenceID {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "reused node claims current proposal ownership")
			}
			if err := validateCanonicalNodeFirstMaterializerAuthority(
				ctx,
				tx,
				binding.NodeID,
				actualOriginProposalID,
			); err != nil {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "reused node first-materializer authority differs")
			}
		default:
			return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "node materialization status is invalid")
		}
	}

	materializedEdges := make([]string, 0, len(authority.Edges))
	for index, binding := range authority.Edges {
		expectedEdge := expected.edges[index]
		wantRole := canonicalAdmissionRoleSupports
		if expectedEdge.Relation == evidencegraph.CanonicalDerivedFrom {
			wantRole = canonicalAdmissionRoleDerivedFrom
		}
		if binding.Role != wantRole || binding.Position != index || binding.EdgeID != expectedEdge.ID {
			return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "edge binding order or identity differs")
		}
		actualOriginProposalID, matches, err := canonicalGraphEdgeRowMatches(ctx, tx, expectedEdge)
		if err != nil || !matches {
			return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "canonical edge body differs")
		}
		switch binding.Materialization {
		case canonicalRowMaterialized:
			if actualOriginProposalID != proposal.ProposalOccurrenceID {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "materialized edge owner differs")
			}
			materializedEdges = append(materializedEdges, binding.EdgeID)
		case canonicalRowReused:
			if actualOriginProposalID == proposal.ProposalOccurrenceID || expected.derivation != nil {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "reused edge materialization is invalid")
			}
			if err := validateCanonicalEdgeFirstMaterializerAuthority(
				ctx,
				tx,
				binding.EdgeID,
				actualOriginProposalID,
			); err != nil {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "reused edge first-materializer authority differs")
			}
		default:
			return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "edge materialization status is invalid")
		}
	}
	if expected.derivation != nil {
		for _, binding := range authority.Nodes {
			if binding.Materialization != canonicalRowMaterialized {
				return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "derivation target was not materialized")
			}
		}
	}
	slices.Sort(materializedNodes)
	slices.Sort(materializedEdges)
	actualNodes, err := loadCanonicalNodeIDsByOrigin(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return err
	}
	actualEdges, err := loadCanonicalEdgeIDsByOrigin(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return err
	}
	actualEdges, err = ordinaryAdmissionAttributedEdges(ctx, tx, proposal.ProposalOccurrenceID, expected, actualEdges, materializedEdges)
	if err != nil {
		return err
	}
	if !slices.Equal(actualNodes, materializedNodes) || !slices.Equal(actualEdges, materializedEdges) {
		return ordinaryAdmissionReplayConflict(proposal.ProposalOccurrenceID, "proposal-attributed canonical row set differs")
	}
	return nil
}

func ordinaryAdmissionMutationKind(mutation canonicalAdmissionMutation) (string, error) {
	if mutation.derivation == nil {
		return ordinaryAdmissionSourceBacked, nil
	}
	if len(mutation.nodes) != 1 {
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"derivation-backed ordinary admission must contain exactly one target node",
		)
	}
	switch mutation.nodes[0].Kind {
	case evidencegraph.CanonicalDerivedClaim:
		return ordinaryAdmissionDerived, nil
	default:
		return "", newDomainError(
			ErrorCanonicalAdmissionInvariant,
			"unsupported derivation-backed ordinary admission target kind %q",
			mutation.nodes[0].Kind,
		)
	}
}

func validateCanonicalNodeFirstMaterializerAuthority(
	ctx context.Context,
	tx sqlTx,
	nodeID string,
	originProposalID string,
) error {
	var authorityCount int64
	err := tx.queryRow(ctx, `
		SELECT /* canonical-node-first-materializer-authority */
			(
				SELECT count(*)
				FROM canonical_ordinary_admission_node_bindings AS binding
				JOIN canonical_ordinary_admission_manifests AS manifest
				  ON manifest.admission_decision_id = binding.admission_decision_id
				JOIN admission_decisions AS decision
				  ON decision.admission_decision_id = manifest.admission_decision_id
				JOIN proposal_occurrences AS proposal
				  ON proposal.proposal_occurrence_id = manifest.proposal_occurrence_id
				WHERE binding.canonical_node_id = $1
				  AND binding.materialization = 'materialized'
				  AND manifest.proposal_occurrence_id = $2
				  AND decision.proposal_occurrence_id = manifest.proposal_occurrence_id
				  AND decision.outcome = 'admitted'
				  AND proposal.admission_outcome = decision.outcome
				  AND proposal.canonical_ref = decision.canonical_ref
			)
			+
			(
				SELECT count(*)
				FROM canonical_supersession_admission_events AS event
				JOIN admission_decisions AS decision
				  ON decision.admission_decision_id = event.admission_decision_id
				JOIN proposal_occurrences AS proposal
				  ON proposal.proposal_occurrence_id = event.proposal_occurrence_id
				WHERE event.proposal_occurrence_id = $2
				  AND decision.proposal_occurrence_id = event.proposal_occurrence_id
				  AND decision.outcome = 'admitted'
				  AND proposal.admission_outcome = decision.outcome
				  AND proposal.canonical_ref = decision.canonical_ref
				  AND (
					decision.canonical_ref = $1
					OR decision.raw_evidence_node_ids @> jsonb_build_array($1::TEXT)
				  )
			)
	`, nodeID, originProposalID).Scan(&authorityCount)
	if err != nil {
		return fmt.Errorf("loading canonical node first-materializer authority: %w", err)
	}
	if authorityCount != 1 {
		return fmt.Errorf("canonical node first-materializer authority count = %d, want 1", authorityCount)
	}
	return nil
}

func validateCanonicalEdgeFirstMaterializerAuthority(
	ctx context.Context,
	tx sqlTx,
	edgeID string,
	originProposalID string,
) error {
	var authorityCount int64
	err := tx.queryRow(ctx, `
		SELECT /* canonical-edge-first-materializer-authority */
			(
				SELECT count(*)
				FROM canonical_ordinary_admission_edge_bindings AS binding
				JOIN canonical_ordinary_admission_manifests AS manifest
				  ON manifest.admission_decision_id = binding.admission_decision_id
				JOIN admission_decisions AS decision
				  ON decision.admission_decision_id = manifest.admission_decision_id
				JOIN proposal_occurrences AS proposal
				  ON proposal.proposal_occurrence_id = manifest.proposal_occurrence_id
				WHERE binding.canonical_edge_id = $1
				  AND binding.materialization = 'materialized'
				  AND manifest.proposal_occurrence_id = $2
				  AND decision.proposal_occurrence_id = manifest.proposal_occurrence_id
				  AND decision.outcome = 'admitted'
				  AND proposal.admission_outcome = decision.outcome
				  AND proposal.canonical_ref = decision.canonical_ref
			)
			+
			(
				SELECT count(*)
				FROM canonical_supersession_admission_events AS event
				JOIN admission_decisions AS decision
				  ON decision.admission_decision_id = event.admission_decision_id
				JOIN proposal_occurrences AS proposal
				  ON proposal.proposal_occurrence_id = event.proposal_occurrence_id
				WHERE event.proposal_occurrence_id = $2
				  AND decision.proposal_occurrence_id = event.proposal_occurrence_id
				  AND decision.outcome = 'admitted'
				  AND proposal.admission_outcome = decision.outcome
				  AND proposal.canonical_ref = decision.canonical_ref
				  AND decision.canonical_edge_ids @> jsonb_build_array($1::TEXT)
			)
	`, edgeID, originProposalID).Scan(&authorityCount)
	if err != nil {
		return fmt.Errorf("loading canonical edge first-materializer authority: %w", err)
	}
	if authorityCount != 1 {
		return fmt.Errorf("canonical edge first-materializer authority count = %d, want 1", authorityCount)
	}
	return nil
}

func ordinaryAdmissionReplayConflict(proposalID, detail string) error {
	return newDomainError(
		ErrorAdmissionReplayConflict,
		"proposal %s %s",
		proposalID,
		detail,
	)
}

func loadOrdinaryAdmissionAuthority(
	ctx context.Context,
	tx sqlTx,
	decisionID string,
) (persistedOrdinaryAdmissionAuthority, error) {
	var result persistedOrdinaryAdmissionAuthority
	err := tx.queryRow(ctx, `
		SELECT
			contract_version,
			mutation_kind,
			proposal_occurrence_id,
			canonical_ref,
			admission_outcome
		FROM canonical_ordinary_admission_manifests
		WHERE admission_decision_id = $1
	`, decisionID).Scan(
		&result.ContractVersion,
		&result.MutationKind,
		&result.ProposalID,
		&result.CanonicalRef,
		&result.Outcome,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("ordinary admission manifest %s not found", decisionID)
		}
		return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("loading ordinary admission manifest %s: %w", decisionID, err)
	}
	nodeRows, err := tx.query(ctx, `
		SELECT binding_position, binding_role, canonical_node_id, materialization
		FROM canonical_ordinary_admission_node_bindings
		WHERE admission_decision_id = $1
		ORDER BY CASE binding_role WHEN 'canonical_ref' THEN 0 ELSE 1 END,
			binding_position
	`, decisionID)
	if err != nil {
		return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("loading ordinary admission node bindings %s: %w", decisionID, err)
	}
	for nodeRows.Next() {
		var binding canonicalAdmissionNodeBinding
		var materialization string
		if err := nodeRows.Scan(&binding.Position, &binding.Role, &binding.NodeID, &materialization); err != nil {
			nodeRows.Close()
			return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("scanning ordinary admission node binding %s: %w", decisionID, err)
		}
		binding.Materialization = canonicalRowMaterialization(materialization)
		result.Nodes = append(result.Nodes, binding)
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("iterating ordinary admission node bindings %s: %w", decisionID, err)
	}
	nodeRows.Close()
	edgeRows, err := tx.query(ctx, `
		SELECT binding_position, binding_role, canonical_edge_id, materialization
		FROM canonical_ordinary_admission_edge_bindings
		WHERE admission_decision_id = $1
		ORDER BY binding_position
	`, decisionID)
	if err != nil {
		return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("loading ordinary admission edge bindings %s: %w", decisionID, err)
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var binding canonicalAdmissionEdgeBinding
		var materialization string
		if err := edgeRows.Scan(&binding.Position, &binding.Role, &binding.EdgeID, &materialization); err != nil {
			return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("scanning ordinary admission edge binding %s: %w", decisionID, err)
		}
		binding.Materialization = canonicalRowMaterialization(materialization)
		result.Edges = append(result.Edges, binding)
	}
	if err := edgeRows.Err(); err != nil {
		return persistedOrdinaryAdmissionAuthority{}, fmt.Errorf("iterating ordinary admission edge bindings %s: %w", decisionID, err)
	}
	return result, nil
}

func loadCanonicalNodeIDsByOrigin(ctx context.Context, tx sqlTx, proposalID string) ([]string, error) {
	rows, err := tx.query(ctx, `
		SELECT canonical_node_id
		FROM canonical_graph_nodes
		WHERE origin_proposal_occurrence_id = $1
		ORDER BY canonical_node_id
	`, proposalID)
	if err != nil {
		return nil, fmt.Errorf("loading proposal canonical node audit: %w", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning proposal canonical node audit: %w", err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating proposal canonical node audit: %w", err)
	}
	return result, nil
}

func loadCanonicalEdgeIDsByOrigin(ctx context.Context, tx sqlTx, proposalID string) ([]string, error) {
	rows, err := tx.query(ctx, `
		SELECT canonical_edge_id
		FROM canonical_graph_edges
		WHERE origin_proposal_occurrence_id = $1
		ORDER BY canonical_edge_id
	`, proposalID)
	if err != nil {
		return nil, fmt.Errorf("loading proposal canonical edge audit: %w", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning proposal canonical edge audit: %w", err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating proposal canonical edge audit: %w", err)
	}
	return result, nil
}
