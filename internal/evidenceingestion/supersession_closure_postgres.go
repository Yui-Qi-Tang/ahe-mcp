package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"

	"github.com/jackc/pgx/v5"
)

func loadCanonicalSupersessionClosureInput(
	ctx context.Context,
	tx sqlTx,
	lineageKey string,
) (evidencesupersession.ClosureCutInput, error) {
	basis, err := loadCanonicalSupersessionClosureLineage(ctx, tx, lineageKey)
	if err != nil {
		return evidencesupersession.ClosureCutInput{}, err
	}
	head, err := loadCanonicalSupersessionHead(ctx, tx, false)
	if err != nil {
		return evidencesupersession.ClosureCutInput{}, err
	}
	events, err := loadCanonicalSupersessionClosureEvents(
		ctx,
		tx,
		evidencesupersession.MaxClosureEvents,
	)
	if err != nil {
		return evidencesupersession.ClosureCutInput{}, err
	}
	members, err := loadCanonicalSupersessionClosureMembers(
		ctx,
		tx,
		lineageKey,
		evidencesupersession.MaxClosureMembers,
	)
	if err != nil {
		return evidencesupersession.ClosureCutInput{}, err
	}
	edges, err := loadCanonicalSupersessionClosureEdges(
		ctx,
		tx,
		lineageKey,
		evidencesupersession.MaxClosureEdges,
	)
	if err != nil {
		return evidencesupersession.ClosureCutInput{}, err
	}
	objectClaims, err := loadCanonicalSupersessionObjectClaims(
		ctx,
		tx,
		basis,
		evidencesupersession.MaxClosureObjectClaims,
	)
	if err != nil {
		return evidencesupersession.ClosureCutInput{}, err
	}
	return evidencesupersession.ClosureCutInput{
		Basis:      basis,
		LineageKey: lineageKey,
		Head: evidencesupersession.ClosureHead{
			ChainKey:    head.ChainKey,
			Revision:    head.Revision,
			HeadEventID: head.HeadEventID,
		},
		Events:       events,
		Members:      members,
		Edges:        edges,
		ObjectClaims: objectClaims,
	}, nil
}

func loadCanonicalSupersessionClosureLineage(
	ctx context.Context,
	queryer sqlQueryer,
	lineageKey string,
) (evidencesupersession.Basis, error) {
	var (
		contractVersion string
		basis           evidencesupersession.Basis
	)
	err := queryer.queryRow(ctx, `
		/* canonical supersession closure lineage */
		SELECT
			contract_version,
			source_system,
			source_namespace,
			object_type,
			object_id,
			slot_kind,
			slot_id
		FROM canonical_supersession_lineages
		WHERE lineage_key = $1
	`, lineageKey).Scan(
		&contractVersion,
		&basis.SourceSystem,
		&basis.SourceNamespace,
		&basis.ObjectType,
		&basis.ObjectID,
		&basis.SlotKind,
		&basis.SlotID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return evidencesupersession.Basis{}, newDomainError(
			ErrorSupersessionLineageNotFound,
			"canonical supersession lineage %s not found",
			lineageKey,
		)
	}
	if err != nil {
		return evidencesupersession.Basis{}, fmt.Errorf(
			"loading canonical supersession lineage %s: %w",
			lineageKey,
			err,
		)
	}
	if contractVersion != evidencesupersession.LineageContractVersionV1 {
		return evidencesupersession.Basis{}, newDomainError(
			ErrorSupersessionInvariant,
			"lineage %s has contract %q",
			lineageKey,
			contractVersion,
		)
	}
	wantLineageKey, err := basis.Key()
	if err != nil {
		return evidencesupersession.Basis{}, newDomainError(
			ErrorSupersessionInvariant,
			"lineage %s has invalid persisted basis: %v",
			lineageKey,
			err,
		)
	}
	if wantLineageKey != lineageKey {
		return evidencesupersession.Basis{}, newDomainError(
			ErrorSupersessionInvariant,
			"lineage %s persisted basis recomputes to %s",
			lineageKey,
			wantLineageKey,
		)
	}
	return basis, nil
}

func loadCanonicalSupersessionClosureEvents(
	ctx context.Context,
	queryer sqlQueryer,
	maxEvents int,
) ([]evidencesupersession.ClosureEventRecord, error) {
	if maxEvents < 1 {
		return nil, newDomainError(ErrorSupersessionInvariant, "closure event limit must be positive")
	}
	rows, err := queryer.query(ctx, `
		/* canonical supersession closure events */
		SELECT
			event.event_id,
			event.contract_version,
			event.revision,
			event.previous_revision,
			COALESCE(event.previous_event_id, ''),
			event.event_kind,
			event.lineage_key,
			event.replacement_node_id,
			event.proposal_occurrence_id,
			event.admission_decision_id,
			event.admission_outcome,
			event.request_payload_hash,
			event.decision_payload_hash,
			COALESCE(decision.decision_by, ''),
			COALESCE(decision.decision_reason, ''),
			COALESCE(decision.canonical_edge_ids, 'null'::jsonb),
			event.event_payload,
			event.event_payload_hash
		FROM canonical_supersession_admission_events AS event
		LEFT JOIN admission_decisions AS decision
			ON decision.admission_decision_id = event.admission_decision_id
		ORDER BY event.revision
		LIMIT $1
	`, maxEvents+1)
	if err != nil {
		return nil, fmt.Errorf("querying canonical supersession admission events: %w", err)
	}
	defer rows.Close()

	events := make([]evidencesupersession.ClosureEventRecord, 0)
	for rows.Next() {
		var (
			event                    evidencesupersession.ClosureEventRecord
			decisionCanonicalEdgeIDs []byte
			payloadRaw               []byte
		)
		if err := rows.Scan(
			&event.ID,
			&event.ContractVersion,
			&event.Revision,
			&event.PreviousRevision,
			&event.PreviousEventID,
			&event.Kind,
			&event.LineageKey,
			&event.ReplacementNodeID,
			&event.ProposalOccurrence,
			&event.AdmissionDecisionID,
			&event.AdmissionOutcome,
			&event.RequestPayloadHash,
			&event.DecisionPayloadHash,
			&event.DecisionBy,
			&event.DecisionReason,
			&decisionCanonicalEdgeIDs,
			&payloadRaw,
			&event.EventPayloadHash,
		); err != nil {
			return nil, fmt.Errorf("scanning canonical supersession admission event: %w", err)
		}
		if len(events) == maxEvents {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersession admission event count exceeds %d; closure cannot truncate history",
				maxEvents,
			)
		}
		if err := json.Unmarshal(payloadRaw, &event.Payload); err != nil {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"event %s has invalid payload JSON: %v",
				event.ID,
				err,
			)
		}
		if err := validateCanonicalSupersessionDecisionEdgeIDs(
			event,
			decisionCanonicalEdgeIDs,
		); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating canonical supersession admission events: %w", err)
	}
	return events, nil
}

func validateCanonicalSupersessionDecisionEdgeIDs(
	event evidencesupersession.ClosureEventRecord,
	raw []byte,
) error {
	var edgeIDs []string
	if err := json.Unmarshal(raw, &edgeIDs); err != nil || edgeIDs == nil {
		return newDomainError(
			ErrorSupersessionInvariant,
			"event %s admission decision canonical_edge_ids must be a JSON string array",
			event.ID,
		)
	}
	seen := make(map[string]struct{}, len(edgeIDs))
	for _, edgeID := range edgeIDs {
		if edgeID == "" {
			return newDomainError(
				ErrorSupersessionInvariant,
				"event %s admission decision contains an invalid empty edge ID",
				event.ID,
			)
		}
		if _, duplicate := seen[edgeID]; duplicate {
			return newDomainError(
				ErrorSupersessionInvariant,
				"event %s admission decision contains duplicate edge %s",
				event.ID,
				edgeID,
			)
		}
		seen[edgeID] = struct{}{}
	}
	for _, targetID := range event.Payload.TargetNodeIDs {
		expectedEdgeID := evidencegraph.StableCanonicalID(
			"canon-edge",
			event.Payload.ReplacementNodeID,
			targetID,
			string(evidencegraph.CanonicalSupersedes),
		)
		if _, exists := seen[expectedEdgeID]; !exists {
			return newDomainError(
				ErrorSupersessionInvariant,
				"event %s admission decision omits supersedes edge %s",
				event.ID,
				expectedEdgeID,
			)
		}
	}
	return nil
}

func loadCanonicalSupersessionClosureMembers(
	ctx context.Context,
	queryer sqlQueryer,
	lineageKey string,
	maxMembers int,
) ([]evidencesupersession.ClosureMemberRecord, error) {
	if maxMembers < 1 {
		return nil, newDomainError(ErrorSupersessionInvariant, "closure member limit must be positive")
	}
	rows, err := queryer.query(ctx, `
		/* canonical supersession closure members */
		SELECT
			canonical_node_id,
			lineage_key,
			first_admission_event_id,
			was_bootstrapped
		FROM canonical_supersession_members
		WHERE lineage_key = $1
		ORDER BY canonical_node_id
		LIMIT $2
	`, lineageKey, maxMembers+1)
	if err != nil {
		return nil, fmt.Errorf("querying canonical supersession lineage members: %w", err)
	}
	defer rows.Close()

	members := make([]evidencesupersession.ClosureMemberRecord, 0)
	for rows.Next() {
		var member evidencesupersession.ClosureMemberRecord
		if err := rows.Scan(
			&member.NodeID,
			&member.LineageKey,
			&member.FirstAdmissionEventID,
			&member.WasBootstrapped,
		); err != nil {
			return nil, fmt.Errorf("scanning canonical supersession lineage member: %w", err)
		}
		if len(members) == maxMembers {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersession lineage member count exceeds %d; closure cannot truncate membership",
				maxMembers,
			)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating canonical supersession lineage members: %w", err)
	}
	return members, nil
}

func loadCanonicalSupersessionClosureEdges(
	ctx context.Context,
	queryer sqlQueryer,
	lineageKey string,
	maxEdges int,
) ([]evidencesupersession.ClosureEdgeRecord, error) {
	targetEdges, err := loadCanonicalSupersessionTargetEdges(ctx, queryer, lineageKey, maxEdges)
	if err != nil {
		return nil, err
	}
	incidentEdges, err := loadCanonicalSupersessionIncidentEdges(ctx, queryer, lineageKey, maxEdges)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]evidencesupersession.ClosureEdgeRecord, len(targetEdges)+len(incidentEdges))
	for _, edge := range append(targetEdges, incidentEdges...) {
		if previous, exists := byID[edge.ID]; exists && !reflect.DeepEqual(previous, edge) {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersedes edge %s has conflicting target and graph records",
				edge.ID,
			)
		}
		byID[edge.ID] = edge
	}
	edges := make([]evidencesupersession.ClosureEdgeRecord, 0, len(byID))
	for _, edge := range byID {
		edges = append(edges, edge)
	}
	return edges, nil
}

func loadCanonicalSupersessionTargetEdges(
	ctx context.Context,
	queryer sqlQueryer,
	lineageKey string,
	maxEdges int,
) ([]evidencesupersession.ClosureEdgeRecord, error) {
	if maxEdges < 1 {
		return nil, newDomainError(ErrorSupersessionInvariant, "closure edge limit must be positive")
	}
	rows, err := queryer.query(ctx, `
		/* canonical supersession closure target edges */
		SELECT
			t.event_id,
			t.lineage_key,
			t.replacement_node_id,
			t.target_node_id,
			t.canonical_edge_id,
			COALESCE(e.canonical_edge_id, ''),
			COALESCE(e.from_node_id, ''),
			COALESCE(e.to_node_id, ''),
			COALESCE(e.relation, ''),
			COALESCE(e.provenance::text, ''),
			COALESCE(e.origin_proposal_occurrence_id, ''),
			COALESCE(event.admission_decision_id, '')
		FROM canonical_supersession_replacement_targets AS t
		LEFT JOIN canonical_graph_edges AS e
			ON e.canonical_edge_id = t.canonical_edge_id
		LEFT JOIN canonical_supersession_admission_events AS event
			ON event.event_id = t.event_id
		WHERE t.lineage_key = $1
		ORDER BY t.canonical_edge_id
		LIMIT $2
	`, lineageKey, maxEdges+1)
	if err != nil {
		return nil, fmt.Errorf("querying canonical supersession replacement target edges: %w", err)
	}
	defer rows.Close()
	return scanCanonicalSupersessionClosureEdges(rows, maxEdges, true)
}

func loadCanonicalSupersessionIncidentEdges(
	ctx context.Context,
	queryer sqlQueryer,
	lineageKey string,
	maxEdges int,
) ([]evidencesupersession.ClosureEdgeRecord, error) {
	if maxEdges < 1 {
		return nil, newDomainError(ErrorSupersessionInvariant, "closure edge limit must be positive")
	}
	rows, err := queryer.query(ctx, `
		/* canonical supersession closure incident edges */
		SELECT
			COALESCE(t.event_id, ''),
			COALESCE(t.lineage_key, ''),
			COALESCE(t.replacement_node_id, ''),
			COALESCE(t.target_node_id, ''),
			COALESCE(t.canonical_edge_id, ''),
			e.canonical_edge_id,
			e.from_node_id,
			e.to_node_id,
			e.relation,
			e.provenance::text,
			e.origin_proposal_occurrence_id,
			COALESCE(event.admission_decision_id, '')
		FROM canonical_graph_edges AS e
		LEFT JOIN canonical_supersession_replacement_targets AS t
			ON t.canonical_edge_id = e.canonical_edge_id
		LEFT JOIN canonical_supersession_admission_events AS event
			ON event.event_id = t.event_id
		WHERE e.relation = 'supersedes'
			AND EXISTS (
				SELECT 1
				FROM canonical_supersession_members AS member
				WHERE member.lineage_key = $1
					AND member.canonical_node_id IN (e.from_node_id, e.to_node_id)
			)
		ORDER BY e.canonical_edge_id
		LIMIT $2
	`, lineageKey, maxEdges+1)
	if err != nil {
		return nil, fmt.Errorf("querying canonical supersession incident edges: %w", err)
	}
	defer rows.Close()
	return scanCanonicalSupersessionClosureEdges(rows, maxEdges, false)
}

func scanCanonicalSupersessionClosureEdges(
	rows sqlRows,
	maxEdges int,
	fromTargets bool,
) ([]evidencesupersession.ClosureEdgeRecord, error) {
	edges := make([]evidencesupersession.ClosureEdgeRecord, 0)
	for rows.Next() {
		var (
			targetEventID        string
			targetLineageKey     string
			targetReplacementID  string
			targetNodeID         string
			targetEdgeID         string
			edgeID               string
			fromNodeID           string
			toNodeID             string
			relation             string
			provenanceRaw        string
			proposalOccurrenceID string
			admissionDecisionID  string
		)
		if err := rows.Scan(
			&targetEventID,
			&targetLineageKey,
			&targetReplacementID,
			&targetNodeID,
			&targetEdgeID,
			&edgeID,
			&fromNodeID,
			&toNodeID,
			&relation,
			&provenanceRaw,
			&proposalOccurrenceID,
			&admissionDecisionID,
		); err != nil {
			return nil, fmt.Errorf("scanning canonical supersession edge: %w", err)
		}
		if len(edges) == maxEdges {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersession edge count exceeds %d; closure cannot truncate topology",
				maxEdges,
			)
		}
		if targetEventID == "" || targetLineageKey == "" || targetReplacementID == "" ||
			targetNodeID == "" || targetEdgeID == "" || edgeID == "" {
			kind := "incident edge"
			if fromTargets {
				kind = "replacement target"
			}
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersession %s lacks its exact edge/target authority mirror",
				kind,
			)
		}
		if targetEdgeID != edgeID || targetReplacementID != fromNodeID || targetNodeID != toNodeID ||
			relation != string(evidencegraph.CanonicalSupersedes) || provenanceRaw == "" ||
			proposalOccurrenceID == "" || admissionDecisionID == "" {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersession edge %s does not mirror replacement target authority",
				edgeID,
			)
		}
		var provenance evidencegraph.ProvenanceRecord
		if err := json.Unmarshal([]byte(provenanceRaw), &provenance); err != nil {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical supersession edge %s has invalid provenance JSON: %v",
				edgeID,
				err,
			)
		}
		edges = append(edges, evidencesupersession.ClosureEdgeRecord{
			ID:                         edgeID,
			LineageKey:                 targetLineageKey,
			EventID:                    targetEventID,
			FromNodeID:                 fromNodeID,
			ToNodeID:                   toNodeID,
			Relation:                   relation,
			OriginProposalOccurrenceID: proposalOccurrenceID,
			TraceRef:                   provenance.TraceRef,
			ReviewRef:                  provenance.ReviewRef,
			Method:                     provenance.Method,
			MethodVersion:              provenance.MethodVersion,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating canonical supersession edges: %w", err)
	}
	return edges, nil
}

func loadCanonicalSupersessionObjectClaims(
	ctx context.Context,
	queryer sqlQueryer,
	basis evidencesupersession.Basis,
	maxClaims int,
) ([]evidencesupersession.ObjectClaimClassification, error) {
	if maxClaims < 1 {
		return nil, newDomainError(ErrorSupersessionInvariant, "closure object claim limit must be positive")
	}
	rows, err := queryer.query(ctx, `
		/* canonical supersession closure object claims */
		SELECT
			node.canonical_node_id,
			COALESCE(member.lineage_key, ''),
			COALESCE(lineage.contract_version, ''),
			COALESCE(lineage.source_system, ''),
			COALESCE(lineage.source_namespace, ''),
			COALESCE(lineage.object_type, ''),
			COALESCE(lineage.object_id, ''),
			COALESCE(lineage.slot_kind, ''),
			COALESCE(lineage.slot_id, ''),
			COALESCE(snapshot.origin_metadata ->> 'external_source_schema', ''),
			COALESCE(snapshot.origin_metadata ->> 'external_content_fidelity', '')
		FROM canonical_graph_nodes AS node
		JOIN proposal_occurrences AS proposal
			ON proposal.proposal_occurrence_id = node.origin_proposal_occurrence_id
		JOIN extraction_attempts AS attempt
			ON attempt.extraction_attempt_id = proposal.extraction_attempt_id
		JOIN extraction_runs AS run
			ON run.extraction_run_id = attempt.extraction_run_id
		JOIN source_snapshots AS snapshot
			ON snapshot.source_snapshot_id = run.source_snapshot_id
		LEFT JOIN canonical_supersession_members AS member
			ON member.canonical_node_id = node.canonical_node_id
		LEFT JOIN canonical_supersession_lineages AS lineage
			ON lineage.lineage_key = member.lineage_key
		WHERE node.node_kind = 'source_claim'
			AND snapshot.source_system = $1
			AND snapshot.origin_metadata ->> 'external_source_system' = $2
			AND snapshot.origin_metadata ->> 'external_source_namespace' = $3
			AND snapshot.origin_metadata ->> 'external_object_type' = $4
			AND snapshot.origin_metadata ->> 'external_object_id' = $5
		ORDER BY node.canonical_node_id
		LIMIT $6
	`,
		SourceSystemExternalDocument,
		basis.SourceSystem,
		basis.SourceNamespace,
		basis.ObjectType,
		basis.ObjectID,
		maxClaims+1,
	)
	if err != nil {
		return nil, fmt.Errorf("querying canonical external source object claims: %w", err)
	}
	defer rows.Close()

	claims := make([]evidencesupersession.ObjectClaimClassification, 0)
	for rows.Next() {
		var (
			claim            evidencesupersession.ObjectClaimClassification
			contractVersion  string
			classifiedBasis  evidencesupersession.Basis
			envelopeContract string
			contentFidelity  string
		)
		if err := rows.Scan(
			&claim.NodeID,
			&claim.LineageKey,
			&contractVersion,
			&classifiedBasis.SourceSystem,
			&classifiedBasis.SourceNamespace,
			&classifiedBasis.ObjectType,
			&classifiedBasis.ObjectID,
			&classifiedBasis.SlotKind,
			&classifiedBasis.SlotID,
			&envelopeContract,
			&contentFidelity,
		); err != nil {
			return nil, fmt.Errorf("scanning canonical external source object claim: %w", err)
		}
		if len(claims) == maxClaims {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical source object claim count exceeds %d; closure cannot truncate coverage",
				maxClaims,
			)
		}
		if envelopeContract != ExternalSourceEnvelopeSchemaV1 ||
			contentFidelity != ExternalSourceContentFidelityVerbatim {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"canonical source claim %s lacks a verbatim external source envelope",
				claim.NodeID,
			)
		}
		if claim.LineageKey != "" {
			if contractVersion != evidencesupersession.LineageContractVersionV1 {
				return nil, newDomainError(
					ErrorSupersessionInvariant,
					"canonical source claim %s has lineage contract %q",
					claim.NodeID,
					contractVersion,
				)
			}
			classifiedKey, err := classifiedBasis.Key()
			if err != nil || classifiedKey != claim.LineageKey {
				return nil, newDomainError(
					ErrorSupersessionInvariant,
					"canonical source claim %s has invalid persisted lineage basis",
					claim.NodeID,
				)
			}
			if classifiedBasis.SourceSystem != basis.SourceSystem ||
				classifiedBasis.SourceNamespace != basis.SourceNamespace ||
				classifiedBasis.ObjectType != basis.ObjectType ||
				classifiedBasis.ObjectID != basis.ObjectID {
				return nil, newDomainError(
					ErrorSupersessionInvariant,
					"canonical source claim %s lineage disagrees with its external source object",
					claim.NodeID,
				)
			}
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating canonical external source object claims: %w", err)
	}
	return claims, nil
}
