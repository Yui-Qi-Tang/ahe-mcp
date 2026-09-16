package evidenceingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/jackc/pgx/v5"
)

type persistedSupersessionEvent struct {
	ID                  string
	ContractVersion     string
	Revision            int64
	PreviousRevision    int64
	PreviousEventID     string
	Kind                string
	LineageKey          string
	ReplacementNodeID   string
	ProposalOccurrence  string
	AdmissionDecisionID string
	RequestPayloadHash  string
	DecisionPayloadHash string
	EventPayloadHash    string
	Payload             evidencesupersession.AdmissionEventPayloadV2
}

func replaySupersessionAdmission(
	ctx context.Context,
	tx sqlTx,
	proposal ProposalQueryResult,
	input SupersessionAdmissionInput,
	basis evidencesupersession.Basis,
) (SupersessionAdmissionResult, error) {
	admission, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return SupersessionAdmissionResult{}, err
	}
	event, err := loadPersistedSupersessionEvent(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return SupersessionAdmissionResult{}, err
	}
	if metadata.DecisionBy != input.DecisionBy || metadata.DecisionReason != input.DecisionReason {
		return SupersessionAdmissionResult{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s was admitted with different reviewer metadata",
			proposal.ProposalOccurrenceID,
		)
	}
	if admission.AdmissionOutcome != admissionOutcomeAdmitted ||
		admission.CanonicalRef != event.ReplacementNodeID ||
		admission.AdmissionDecisionID != event.AdmissionDecisionID ||
		proposal.CanonicalRef != event.ReplacementNodeID {
		return SupersessionAdmissionResult{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s has inconsistent supersession decision binding",
			proposal.ProposalOccurrenceID,
		)
	}
	request, err := evidencesupersession.NewRequestPayloadV2(
		proposal.ProposalOccurrenceID,
		admission.CanonicalRef,
		basis,
		input.TargetNodeIDs,
		input.ExpectedRevision,
		input.ExpectedHeadEventID,
	)
	if err != nil {
		return SupersessionAdmissionResult{}, supersessionInputError(err)
	}
	decision, err := evidencesupersession.NewDecisionPayloadV2(
		proposal.ProposalOccurrenceID,
		admission.CanonicalRef,
		input.DecisionBy,
		input.DecisionReason,
	)
	if err != nil {
		return SupersessionAdmissionResult{}, supersessionInputError(err)
	}
	wantEvent, err := evidencesupersession.NewAtomicReplacementEventV2(
		request,
		decision,
		admission.AdmissionDecisionID,
		event.Payload.BootstrappedTargetNodeIDs,
	)
	if err != nil {
		return SupersessionAdmissionResult{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s replay cannot reproduce its admitted event: %v",
			proposal.ProposalOccurrenceID,
			err,
		)
	}
	wantEventID, err := wantEvent.ID()
	if err != nil {
		return SupersessionAdmissionResult{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s replay event identity is invalid: %v",
			proposal.ProposalOccurrenceID,
			err,
		)
	}
	if wantEventID != event.ID {
		return SupersessionAdmissionResult{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s replay differs from its admitted supersession event",
			proposal.ProposalOccurrenceID,
		)
	}
	if err := validatePersistedSupersessionLineage(ctx, tx, event.LineageKey, event.Payload.LineageBasis); err != nil {
		return SupersessionAdmissionResult{}, err
	}
	expectedMutation, err := expectedSupersessionReplayMutation(proposal, input, event, admission)
	if err != nil {
		return SupersessionAdmissionResult{}, err
	}
	if err := validatePersistedSupersessionCanonicalMutation(ctx, tx, event, admission, expectedMutation); err != nil {
		return SupersessionAdmissionResult{}, err
	}
	supersedesEdgeIDs, err := validatePersistedSupersessionMaterialization(ctx, tx, event, admission)
	if err != nil {
		return SupersessionAdmissionResult{}, err
	}
	admission.Replayed = true
	return supersessionAdmissionResult(
		admission,
		event.LineageKey,
		event.Payload.TargetNodeIDs,
		event.Payload.BootstrappedTargetNodeIDs,
		supersedesEdgeIDs,
		event.ID,
		event.Payload,
		metadata.DecisionBy,
		metadata.DecisionReason,
		true,
	), nil
}

func validatePersistedSupersessionLineage(
	ctx context.Context,
	tx sqlTx,
	lineageKey string,
	want evidencesupersession.Basis,
) error {
	var contract string
	var got evidencesupersession.Basis
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
		&got.SourceSystem,
		&got.SourceNamespace,
		&got.ObjectType,
		&got.ObjectID,
		&got.SlotKind,
		&got.SlotID,
	); err != nil {
		return fmt.Errorf("loading persisted supersession lineage %s: %w", lineageKey, err)
	}
	if contract != evidencesupersession.LineageContractVersionV1 || got != want {
		return newDomainError(ErrorSupersessionReplayConflict, "persisted lineage %s does not match its admission event", lineageKey)
	}
	return nil
}

func expectedSupersessionReplayMutation(
	proposal ProposalQueryResult,
	input SupersessionAdmissionInput,
	event persistedSupersessionEvent,
	admission AdmissionResult,
) (canonicalAdmissionMutation, error) {
	mutation, err := buildCanonicalAdmissionMutation(proposal, AdmissionInput{
		ProposalOccurrenceID: proposal.ProposalOccurrenceID,
		DecisionBy:           input.DecisionBy,
		DecisionReason:       input.DecisionReason,
	})
	if err != nil {
		return canonicalAdmissionMutation{}, err
	}
	if len(mutation.nodes) == 0 || mutation.nodes[0].ID != event.ReplacementNodeID ||
		mutation.result.AdmissionDecisionID != admission.AdmissionDecisionID {
		return canonicalAdmissionMutation{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s canonical admission identity differs from event %s",
			proposal.ProposalOccurrenceID,
			event.ID,
		)
	}

	mutation.nodes[0].Provenance.ReviewRef = admission.AdmissionDecisionID
	for index := range mutation.edges {
		mutation.edges[index].Provenance.ReviewRef = admission.AdmissionDecisionID
	}
	supersedesEdges := buildSupersedesEdges(
		proposal,
		event.ReplacementNodeID,
		event.Payload.TargetNodeIDs,
		admission.AdmissionDecisionID,
		event.ID,
	)
	for _, edge := range supersedesEdges {
		mutation.result.CanonicalEdgeIDs = append(mutation.result.CanonicalEdgeIDs, edge.ID)
		mutation.decision.CanonicalEdgeIDs = append(mutation.decision.CanonicalEdgeIDs, edge.ID)
	}
	mutation.edges = append(mutation.edges, supersedesEdges...)
	return mutation, nil
}

func validatePersistedSupersessionCanonicalMutation(
	ctx context.Context,
	tx sqlTx,
	event persistedSupersessionEvent,
	admission AdmissionResult,
	expected canonicalAdmissionMutation,
) error {
	if admission.ProposalOccurrenceID != expected.result.ProposalOccurrenceID ||
		admission.AdmissionDecisionID != expected.result.AdmissionDecisionID ||
		admission.AdmissionOutcome != expected.result.AdmissionOutcome ||
		admission.CanonicalRef != expected.result.CanonicalRef ||
		!slices.Equal(admission.RawEvidenceNodeIDs, expected.result.RawEvidenceNodeIDs) ||
		!slices.Equal(admission.CanonicalEdgeIDs, expected.result.CanonicalEdgeIDs) ||
		admission.DerivationID != "" ||
		len(admission.ParentNodeIDs) != 0 {
		return newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s admission decision does not match its complete canonical mutation",
			event.ProposalOccurrence,
		)
	}

	expectedNodeIDs := make(map[string]struct{}, len(expected.nodes))
	for index, node := range expected.nodes {
		expectedNodeIDs[node.ID] = struct{}{}
		got, err := loadCanonicalNode(ctx, tx, node.ID)
		if err != nil {
			return newDomainError(
				ErrorSupersessionReplayConflict,
				"proposal %s canonical node %s is unavailable: %v",
				event.ProposalOccurrence,
				node.ID,
				err,
			)
		}
		if !supersessionCanonicalNodeMatches(got, node, index == 0) {
			return newDomainError(
				ErrorSupersessionReplayConflict,
				"proposal %s canonical node %s differs from its admitted mutation",
				event.ProposalOccurrence,
				node.ID,
			)
		}
	}

	expectedEdgeIDs := make([]string, 0, len(expected.edges))
	for _, edge := range expected.edges {
		expectedEdgeIDs = append(expectedEdgeIDs, edge.ID)
		got, err := loadCanonicalEdge(ctx, tx, edge.ID)
		if err != nil {
			return newDomainError(
				ErrorSupersessionReplayConflict,
				"proposal %s canonical edge %s is unavailable: %v",
				event.ProposalOccurrence,
				edge.ID,
				err,
			)
		}
		if !reflect.DeepEqual(got, edge) {
			return newDomainError(
				ErrorSupersessionReplayConflict,
				"proposal %s canonical edge %s differs from its admitted mutation",
				event.ProposalOccurrence,
				edge.ID,
			)
		}
	}
	slices.Sort(expectedEdgeIDs)

	rows, err := tx.query(ctx, `
		SELECT canonical_edge_id
		FROM canonical_graph_edges
		WHERE origin_proposal_occurrence_id = $1
		ORDER BY canonical_edge_id
	`, event.ProposalOccurrence)
	if err != nil {
		return fmt.Errorf("loading supersession proposal edge audit: %w", err)
	}
	var actualEdgeIDs []string
	for rows.Next() {
		var edgeID string
		if err := rows.Scan(&edgeID); err != nil {
			return fmt.Errorf("scanning supersession proposal edge audit: %w", err)
		}
		actualEdgeIDs = append(actualEdgeIDs, edgeID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating supersession proposal edge audit: %w", err)
	}
	rows.Close()
	actualEdgeIDs, err = supersessionAdmissionAttributedEdges(ctx, tx, event, actualEdgeIDs, expectedEdgeIDs)
	if err != nil {
		return err
	}
	if !slices.Equal(actualEdgeIDs, expectedEdgeIDs) {
		return newDomainError(
			ErrorSupersessionReplayConflict,
			"proposal %s canonical edge set differs from its admitted mutation",
			event.ProposalOccurrence,
		)
	}

	rows, err = tx.query(ctx, `
		SELECT canonical_node_id
		FROM canonical_graph_nodes
		WHERE origin_proposal_occurrence_id = $1
		ORDER BY canonical_node_id
	`, event.ProposalOccurrence)
	if err != nil {
		return fmt.Errorf("loading supersession proposal node audit: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return fmt.Errorf("scanning supersession proposal node audit: %w", err)
		}
		if _, exists := expectedNodeIDs[nodeID]; !exists {
			return newDomainError(
				ErrorSupersessionReplayConflict,
				"proposal %s has unexpected canonical node %s",
				event.ProposalOccurrence,
				nodeID,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating supersession proposal node audit: %w", err)
	}
	return nil
}

func supersessionCanonicalNodeMatches(
	actual CanonicalQueryResult,
	expected CanonicalGraphNode,
	requireOriginProposal bool,
) bool {
	return actual.CanonicalID == expected.ID &&
		actual.NodeKind == expected.Kind &&
		reflect.DeepEqual(actual.Payload, expected.Payload) &&
		reflect.DeepEqual(actual.Provenance, expected.Provenance) &&
		reflect.DeepEqual(actual.Temporal, expected.Temporal) &&
		reflect.DeepEqual(actual.Integrity, expected.Integrity) &&
		(!requireOriginProposal || actual.OriginProposalOccurrenceID == expected.OriginProposalOccurrenceID)
}

func loadPersistedSupersessionEvent(
	ctx context.Context,
	tx sqlTx,
	proposalOccurrenceID string,
) (persistedSupersessionEvent, error) {
	var result persistedSupersessionEvent
	var previousEventID sql.NullString
	var payloadData []byte
	err := tx.queryRow(ctx, `
		SELECT
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
			request_payload_hash,
			decision_payload_hash,
			event_payload,
			event_payload_hash
		FROM canonical_supersession_admission_events
		WHERE proposal_occurrence_id = $1
	`, proposalOccurrenceID).Scan(
		&result.ID,
		&result.ContractVersion,
		&result.Revision,
		&result.PreviousRevision,
		&previousEventID,
		&result.Kind,
		&result.LineageKey,
		&result.ReplacementNodeID,
		&result.ProposalOccurrence,
		&result.AdmissionDecisionID,
		&result.RequestPayloadHash,
		&result.DecisionPayloadHash,
		&payloadData,
		&result.EventPayloadHash,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return persistedSupersessionEvent{}, newDomainError(
				ErrorSupersessionReplayConflict,
				"admitted proposal %s has no supersession admission event; use its original admission path",
				proposalOccurrenceID,
			)
		}
		return persistedSupersessionEvent{}, fmt.Errorf("loading supersession admission event: %w", err)
	}
	if previousEventID.Valid {
		result.PreviousEventID = previousEventID.String
	}
	if err := json.Unmarshal(payloadData, &result.Payload); err != nil {
		return persistedSupersessionEvent{}, fmt.Errorf("decoding supersession admission event %s: %w", result.ID, err)
	}
	computedID, err := result.Payload.ID()
	if err != nil {
		return persistedSupersessionEvent{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"supersession admission event %s payload is invalid: %v",
			result.ID,
			err,
		)
	}
	if computedID != result.ID ||
		result.EventPayloadHash != strings.TrimPrefix(result.ID, "admission-event:v2:") ||
		result.ContractVersion != result.Payload.ContractVersion ||
		result.Revision != result.Payload.Revision ||
		result.PreviousRevision != result.Payload.PreviousRevision ||
		result.PreviousEventID != result.Payload.PreviousEventID ||
		result.Kind != result.Payload.Kind ||
		result.LineageKey != result.Payload.LineageKey ||
		result.ReplacementNodeID != result.Payload.ReplacementNodeID ||
		result.ProposalOccurrence != result.Payload.ProposalOccurrence ||
		result.AdmissionDecisionID != result.Payload.AdmissionDecisionID ||
		result.RequestPayloadHash != result.Payload.RequestPayloadHash ||
		result.DecisionPayloadHash != result.Payload.DecisionPayloadHash {
		return persistedSupersessionEvent{}, newDomainError(
			ErrorSupersessionReplayConflict,
			"supersession admission event %s does not match its persisted identity",
			result.ID,
		)
	}
	return result, nil
}

func validatePersistedSupersessionMaterialization(
	ctx context.Context,
	tx sqlTx,
	event persistedSupersessionEvent,
	admission AdmissionResult,
) ([]string, error) {
	if err := validatePersistedSupersessionEventMembers(ctx, tx, event); err != nil {
		return nil, err
	}

	var replacementLineage, replacementFirstEvent string
	var replacementBootstrapped bool
	if err := tx.queryRow(ctx, `
		SELECT lineage_key, first_admission_event_id, was_bootstrapped
		FROM canonical_supersession_members
		WHERE canonical_node_id = $1
	`, event.ReplacementNodeID).Scan(
		&replacementLineage,
		&replacementFirstEvent,
		&replacementBootstrapped,
	); err != nil {
		return nil, fmt.Errorf("loading supersession replacement membership: %w", err)
	}
	if replacementLineage != event.LineageKey || replacementFirstEvent != event.ID || replacementBootstrapped {
		return nil, newDomainError(ErrorSupersessionReplayConflict, "replacement membership does not match event %s", event.ID)
	}

	rows, err := tx.query(ctx, `
		SELECT
			t.target_node_id,
			t.canonical_edge_id,
			m.lineage_key,
			m.first_admission_event_id,
			m.was_bootstrapped,
			e.from_node_id,
			e.to_node_id,
			e.relation,
			e.origin_proposal_occurrence_id,
			COALESCE(e.provenance ->> 'trace_ref', ''),
			COALESCE(e.provenance ->> 'review_ref', ''),
			COALESCE(e.provenance ->> 'method', '')
		FROM canonical_supersession_replacement_targets t
		JOIN canonical_supersession_members m
			ON m.canonical_node_id = t.target_node_id
		JOIN canonical_graph_edges e
			ON e.canonical_edge_id = t.canonical_edge_id
		WHERE t.event_id = $1
		ORDER BY t.target_node_id
	`, event.ID)
	if err != nil {
		return nil, fmt.Errorf("loading supersession replacement targets: %w", err)
	}
	defer rows.Close()
	targets := make([]string, 0, len(event.Payload.TargetNodeIDs))
	edgeIDs := make([]string, 0, len(event.Payload.TargetNodeIDs))
	bootstrapped := make([]string, 0, len(event.Payload.BootstrappedTargetNodeIDs))
	for rows.Next() {
		var (
			targetID, edgeID, lineageKey, firstEventID string
			from, to, relation, originProposal         string
			traceRef, reviewRef, method                string
		)
		var wasBootstrapped bool
		if err := rows.Scan(
			&targetID,
			&edgeID,
			&lineageKey,
			&firstEventID,
			&wasBootstrapped,
			&from,
			&to,
			&relation,
			&originProposal,
			&traceRef,
			&reviewRef,
			&method,
		); err != nil {
			return nil, fmt.Errorf("scanning supersession replacement target: %w", err)
		}
		if lineageKey != event.LineageKey ||
			from != event.ReplacementNodeID ||
			to != targetID ||
			relation != string(evidencegraph.CanonicalSupersedes) ||
			originProposal != event.ProposalOccurrence ||
			traceRef != event.ID ||
			reviewRef != event.AdmissionDecisionID ||
			method != "supersession_admission_edge" {
			return nil, newDomainError(ErrorSupersessionReplayConflict, "replacement target %s is inconsistent with event %s", targetID, event.ID)
		}
		if wasBootstrapped && firstEventID == event.ID {
			bootstrapped = append(bootstrapped, targetID)
		}
		targets = append(targets, targetID)
		edgeIDs = append(edgeIDs, edgeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating supersession replacement targets: %w", err)
	}
	if !slices.Equal(targets, event.Payload.TargetNodeIDs) ||
		!slices.Equal(bootstrapped, event.Payload.BootstrappedTargetNodeIDs) {
		return nil, newDomainError(ErrorSupersessionReplayConflict, "replacement targets do not match event %s", event.ID)
	}
	for _, edgeID := range edgeIDs {
		if !slices.Contains(admission.CanonicalEdgeIDs, edgeID) {
			return nil, newDomainError(ErrorSupersessionReplayConflict, "admission decision omits supersedes edge %s", edgeID)
		}
	}
	return edgeIDs, nil
}

func validatePersistedSupersessionEventMembers(
	ctx context.Context,
	tx sqlTx,
	event persistedSupersessionEvent,
) error {
	expected := make(map[string]bool, 1+len(event.Payload.BootstrappedTargetNodeIDs))
	expected[event.ReplacementNodeID] = false
	for _, targetID := range event.Payload.BootstrappedTargetNodeIDs {
		expected[targetID] = true
	}

	rows, err := tx.query(ctx, `
		SELECT canonical_node_id, lineage_key, was_bootstrapped
		FROM canonical_supersession_members
		WHERE first_admission_event_id = $1
		ORDER BY canonical_node_id
	`, event.ID)
	if err != nil {
		return fmt.Errorf("loading supersession event member audit: %w", err)
	}
	defer rows.Close()

	actual := make(map[string]bool, len(expected))
	for rows.Next() {
		var nodeID, lineageKey string
		var bootstrapped bool
		if err := rows.Scan(&nodeID, &lineageKey, &bootstrapped); err != nil {
			return fmt.Errorf("scanning supersession event member audit: %w", err)
		}
		wantBootstrapped, exists := expected[nodeID]
		if !exists || lineageKey != event.LineageKey || bootstrapped != wantBootstrapped {
			return newDomainError(
				ErrorSupersessionReplayConflict,
				"event %s has an unexpected supersession member %s",
				event.ID,
				nodeID,
			)
		}
		actual[nodeID] = bootstrapped
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating supersession event member audit: %w", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return newDomainError(
			ErrorSupersessionReplayConflict,
			"event %s supersession member set differs from its admitted mutation",
			event.ID,
		)
	}
	return nil
}

func supersessionAdmissionResult(
	admission AdmissionResult,
	lineageKey string,
	targetNodeIDs []string,
	bootstrappedTargetNodeIDs []string,
	supersedesEdgeIDs []string,
	eventID string,
	event evidencesupersession.AdmissionEventPayloadV2,
	decisionBy string,
	decisionReason string,
	replayed bool,
) SupersessionAdmissionResult {
	admission.Replayed = replayed
	return SupersessionAdmissionResult{
		AdmissionResult:           admission,
		LineageKey:                lineageKey,
		TargetNodeIDs:             append([]string(nil), targetNodeIDs...),
		BootstrappedTargetNodeIDs: append([]string(nil), bootstrappedTargetNodeIDs...),
		SupersedesEdgeIDs:         append([]string(nil), supersedesEdgeIDs...),
		AdmissionEventID:          eventID,
		EventRevision:             event.Revision,
		PreviousHeadEventID:       event.PreviousEventID,
		DecisionBy:                decisionBy,
		DecisionReason:            decisionReason,
	}
}
