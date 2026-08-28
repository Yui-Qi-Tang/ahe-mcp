package evidenceingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
)

func loadCanonicalSupersessionHead(
	ctx context.Context,
	queryer sqlQueryer,
	lock bool,
) (CanonicalSupersessionHead, error) {
	query := `
		SELECT chain_key, revision, head_event_id
		FROM canonical_supersession_admission_head
		WHERE chain_key = $1
	`
	if lock {
		query += ` FOR UPDATE`
	}
	var result CanonicalSupersessionHead
	var headEventID sql.NullString
	err := queryer.queryRow(ctx, query, evidencesupersession.ChainKey).Scan(
		&result.ChainKey,
		&result.Revision,
		&headEventID,
	)
	if err != nil {
		return CanonicalSupersessionHead{}, fmt.Errorf("loading canonical supersession head: %w", err)
	}
	if headEventID.Valid {
		result.HeadEventID = headEventID.String
	}
	return result, nil
}

func requireFreshSupersessionReplacement(ctx context.Context, tx sqlTx, nodeID string) error {
	var exists bool
	if err := tx.queryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM canonical_graph_nodes
			WHERE canonical_node_id = $1
		)
	`, nodeID).Scan(&exists); err != nil {
		return fmt.Errorf("checking fresh supersession replacement %s: %w", nodeID, err)
	}
	if exists {
		return newDomainError(
			ErrorSupersessionInvariant,
			"replacement node %s already exists; supersession requires a fresh source_claim",
			nodeID,
		)
	}
	return nil
}

func ensureSupersessionLineage(
	ctx context.Context,
	tx sqlTx,
	lineageKey string,
	basis evidencesupersession.Basis,
) error {
	if _, err := tx.exec(ctx, `
		INSERT INTO canonical_supersession_lineages (
			lineage_key,
			contract_version,
			source_system,
			source_namespace,
			object_type,
			object_id,
			slot_kind,
			slot_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (lineage_key) DO NOTHING
	`,
		lineageKey,
		evidencesupersession.LineageContractVersionV1,
		basis.SourceSystem,
		basis.SourceNamespace,
		basis.ObjectType,
		basis.ObjectID,
		basis.SlotKind,
		basis.SlotID,
	); err != nil {
		return fmt.Errorf("ensuring supersession lineage %s: %w", lineageKey, err)
	}
	var stored evidencesupersession.Basis
	var contract string
	if err := tx.queryRow(ctx, `
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
		&contract,
		&stored.SourceSystem,
		&stored.SourceNamespace,
		&stored.ObjectType,
		&stored.ObjectID,
		&stored.SlotKind,
		&stored.SlotID,
	); err != nil {
		return fmt.Errorf("loading supersession lineage %s: %w", lineageKey, err)
	}
	if contract != evidencesupersession.LineageContractVersionV1 || stored != basis {
		return newDomainError(ErrorSupersessionInvariant, "lineage %s has different persisted basis", lineageKey)
	}
	return nil
}

type supersessionTargetRecord struct {
	NodeID              string
	NodeKind            evidencegraph.CanonicalNodeKind
	SourceSystem        string
	OriginMetadata      map[string]string
	LineageKey          string
	FirstAdmissionEvent string
	WasBootstrapped     bool
}

func loadSupersessionTargetsForUpdate(
	ctx context.Context,
	tx sqlTx,
	targetNodeIDs []string,
) ([]supersessionTargetRecord, error) {
	rows, err := tx.query(ctx, `
		SELECT
			n.canonical_node_id,
			n.node_kind,
			COALESCE(ss.source_system, ''),
			COALESCE(ss.origin_metadata, '{}'::jsonb),
			COALESCE(m.lineage_key, ''),
			COALESCE(m.first_admission_event_id, ''),
			COALESCE(m.was_bootstrapped, false)
		FROM canonical_graph_nodes n
		JOIN proposal_occurrences po
			ON po.proposal_occurrence_id = n.origin_proposal_occurrence_id
		JOIN extraction_attempts ea
			ON ea.extraction_attempt_id = po.extraction_attempt_id
		JOIN extraction_runs er
			ON er.extraction_run_id = ea.extraction_run_id
		LEFT JOIN source_snapshots ss
			ON ss.source_snapshot_id = er.source_snapshot_id
		LEFT JOIN canonical_supersession_members m
			ON m.canonical_node_id = n.canonical_node_id
		WHERE n.canonical_node_id = ANY($1::text[])
		ORDER BY n.canonical_node_id
		FOR UPDATE OF n
	`, targetNodeIDs)
	if err != nil {
		return nil, fmt.Errorf("loading supersession targets: %w", err)
	}
	defer rows.Close()
	records := make([]supersessionTargetRecord, 0, len(targetNodeIDs))
	for rows.Next() {
		var record supersessionTargetRecord
		var originData []byte
		if err := rows.Scan(
			&record.NodeID,
			&record.NodeKind,
			&record.SourceSystem,
			&originData,
			&record.LineageKey,
			&record.FirstAdmissionEvent,
			&record.WasBootstrapped,
		); err != nil {
			return nil, fmt.Errorf("scanning supersession target: %w", err)
		}
		if err := json.Unmarshal(originData, &record.OriginMetadata); err != nil {
			return nil, fmt.Errorf("decoding supersession target %s origin metadata: %w", record.NodeID, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating supersession targets: %w", err)
	}
	return records, nil
}

func validateSupersessionTargets(
	records []supersessionTargetRecord,
	targetNodeIDs []string,
	lineageKey string,
	basis evidencesupersession.Basis,
) ([]string, error) {
	if len(records) != len(targetNodeIDs) {
		found := make(map[string]bool, len(records))
		for _, record := range records {
			found[record.NodeID] = true
		}
		for _, targetID := range targetNodeIDs {
			if !found[targetID] {
				return nil, newDomainError(ErrorSupersessionInvariant, "replacement target %s does not exist", targetID)
			}
		}
		return nil, newDomainError(ErrorSupersessionInvariant, "replacement target set is inconsistent")
	}
	bootstrapped := make([]string, 0, len(records))
	for index, record := range records {
		if record.NodeID != targetNodeIDs[index] {
			return nil, newDomainError(ErrorSupersessionInvariant, "replacement target set is not canonical")
		}
		if record.NodeKind != evidencegraph.CanonicalSourceClaim {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"replacement target %s has kind %s; source_claim is required",
				record.NodeID,
				record.NodeKind,
			)
		}
		if record.SourceSystem != SourceSystemExternalDocument ||
			record.OriginMetadata[externalSourceOriginSchemaKey] != ExternalSourceEnvelopeSchemaV1 ||
			record.OriginMetadata[externalSourceOriginFidelityKey] != ExternalSourceContentFidelityVerbatim {
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"replacement target %s lacks a verbatim external source envelope",
				record.NodeID,
			)
		}
		if err := compareSupersessionExternalOrigin(record.OriginMetadata, basis, record.NodeID); err != nil {
			return nil, err
		}
		switch record.LineageKey {
		case "":
			bootstrapped = append(bootstrapped, record.NodeID)
		case lineageKey:
		default:
			return nil, newDomainError(
				ErrorSupersessionInvariant,
				"replacement target %s is already bound to lineage %s",
				record.NodeID,
				record.LineageKey,
			)
		}
	}
	return bootstrapped, nil
}

func buildSupersedesEdges(
	proposal ProposalQueryResult,
	from string,
	targetNodeIDs []string,
	decisionID string,
	eventID string,
) []CanonicalGraphEdge {
	edges := make([]CanonicalGraphEdge, 0, len(targetNodeIDs))
	for _, targetID := range targetNodeIDs {
		edgeID := evidencegraph.StableCanonicalID(
			"canon-edge",
			from,
			targetID,
			string(evidencegraph.CanonicalSupersedes),
		)
		edges = append(edges, CanonicalGraphEdge{
			ID:       edgeID,
			From:     from,
			To:       targetID,
			Relation: evidencegraph.CanonicalSupersedes,
			Provenance: evidencegraph.ProvenanceRecord{
				ID:            evidencegraph.StableCanonicalID("provenance", edgeID),
				OriginRefs:    append(canonicalOriginRefs(proposal), targetID),
				OriginGroupID: proposalOriginGroupID(proposal),
				Producer:      proposal.ExtractorName,
				Method:        "supersession_admission_edge",
				MethodVersion: "v1",
				TraceRef:      eventID,
				ReviewRef:     decisionID,
			},
			OriginProposalOccurrenceID: proposal.ProposalOccurrenceID,
		})
	}
	return edges
}

func insertSupersessionCanonicalMutation(
	ctx context.Context,
	tx sqlTx,
	mutation canonicalAdmissionMutation,
	supersedesEdgeCount int,
) error {
	for index, node := range mutation.nodes {
		if err := insertCanonicalNode(ctx, tx, node, index == 0); err != nil {
			return err
		}
		if index == 0 {
			continue
		}
		persisted, err := loadCanonicalNode(ctx, tx, node.ID)
		if err != nil {
			return fmt.Errorf("loading reused supersession raw evidence node %s: %w", node.ID, err)
		}
		if !supersessionCanonicalNodeMatches(persisted, node, false) {
			return newDomainError(
				ErrorSupersessionInvariant,
				"reused raw evidence node %s differs from the replacement admission materialization",
				node.ID,
			)
		}
	}
	firstSupersedes := len(mutation.edges) - supersedesEdgeCount
	for index, edge := range mutation.edges {
		requireNew := index >= firstSupersedes || edge.Relation == evidencegraph.CanonicalSupportsClaim
		if err := insertCanonicalEdge(ctx, tx, edge, requireNew); err != nil {
			return err
		}
	}
	return nil
}

func insertSupersessionAdmissionEvent(
	ctx context.Context,
	tx sqlTx,
	eventID string,
	event evidencesupersession.AdmissionEventPayloadV2,
) error {
	eventPayload, err := jsonBytes(event)
	if err != nil {
		return err
	}
	eventPayloadHash := strings.TrimPrefix(eventID, "admission-event:v2:")
	var previousEventID any
	if event.PreviousEventID != "" {
		previousEventID = event.PreviousEventID
	}
	if _, err := tx.exec(ctx, `
		INSERT INTO canonical_supersession_admission_events (
			event_id,
			contract_version,
			revision,
			previous_revision,
			previous_event_id,
			event_kind,
			lineage_key,
			replacement_node_id,
			proposal_occurrence_id,
			admission_decision_id,
			admission_outcome,
			request_payload_hash,
			decision_payload_hash,
			event_payload,
			event_payload_hash
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			'admitted', $11, $12, $13::jsonb, $14
		)
	`,
		eventID,
		event.ContractVersion,
		event.Revision,
		event.PreviousRevision,
		previousEventID,
		event.Kind,
		event.LineageKey,
		event.ReplacementNodeID,
		event.ProposalOccurrence,
		event.AdmissionDecisionID,
		event.RequestPayloadHash,
		event.DecisionPayloadHash,
		string(eventPayload),
		eventPayloadHash,
	); err != nil {
		return fmt.Errorf("inserting supersession admission event %s: %w", eventID, err)
	}
	return nil
}

func insertSupersessionMember(
	ctx context.Context,
	tx sqlTx,
	nodeID string,
	lineageKey string,
	eventID string,
	wasBootstrapped bool,
) error {
	if _, err := tx.exec(ctx, `
		INSERT INTO canonical_supersession_members (
			canonical_node_id,
			lineage_key,
			first_admission_event_id,
			was_bootstrapped
		)
		VALUES ($1, $2, $3, $4)
	`, nodeID, lineageKey, eventID, wasBootstrapped); err != nil {
		return fmt.Errorf("inserting supersession member %s: %w", nodeID, err)
	}
	return nil
}

func insertSupersessionReplacementTargets(
	ctx context.Context,
	tx sqlTx,
	eventID string,
	lineageKey string,
	replacementNodeID string,
	targetNodeIDs []string,
	edgeIDs []string,
) error {
	if len(targetNodeIDs) != len(edgeIDs) {
		return newDomainError(ErrorSupersessionInvariant, "replacement target and edge counts differ")
	}
	for index, targetID := range targetNodeIDs {
		if _, err := tx.exec(ctx, `
			INSERT INTO canonical_supersession_replacement_targets (
				event_id,
				lineage_key,
				replacement_node_id,
				target_node_id,
				canonical_edge_id
			)
			VALUES ($1, $2, $3, $4, $5)
		`, eventID, lineageKey, replacementNodeID, targetID, edgeIDs[index]); err != nil {
			return fmt.Errorf("inserting supersession replacement target %s: %w", targetID, err)
		}
	}
	return nil
}

func advanceCanonicalSupersessionHead(
	ctx context.Context,
	tx sqlTx,
	previous CanonicalSupersessionHead,
	eventID string,
	revision int64,
) error {
	tag, err := tx.exec(ctx, `
		UPDATE canonical_supersession_admission_head
		SET revision = $2,
			head_event_id = $3,
			updated_at = now()
		WHERE chain_key = $1
			AND revision = $4
			AND head_event_id IS NOT DISTINCT FROM NULLIF($5, '')
	`,
		evidencesupersession.ChainKey,
		revision,
		eventID,
		previous.Revision,
		previous.HeadEventID,
	)
	if err != nil {
		return fmt.Errorf("advancing canonical supersession head: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorSupersessionHeadConflict, "canonical supersession head changed during admission")
	}
	return nil
}
