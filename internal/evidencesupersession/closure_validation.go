package evidencesupersession

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func validateAndCanonicalizeClosureCut(input ClosureCutInput) (canonicalClosureCut, error) {
	if len(input.Events) > MaxClosureEvents {
		return canonicalClosureCut{}, fmt.Errorf("%w: event count exceeds %d", ErrClosureLimitExceeded, MaxClosureEvents)
	}
	if len(input.Members) > MaxClosureMembers {
		return canonicalClosureCut{}, fmt.Errorf("%w: member count exceeds %d", ErrClosureLimitExceeded, MaxClosureMembers)
	}
	if len(input.Edges) > MaxClosureEdges {
		return canonicalClosureCut{}, fmt.Errorf("%w: edge count exceeds %d", ErrClosureLimitExceeded, MaxClosureEdges)
	}
	if len(input.ObjectClaims) > MaxClosureObjectClaims {
		return canonicalClosureCut{}, fmt.Errorf("%w: object claim count exceeds %d", ErrClosureLimitExceeded, MaxClosureObjectClaims)
	}

	basis := input.Basis.Normalize()
	if basis != input.Basis {
		return canonicalClosureCut{}, fmt.Errorf("%w: lineage basis is not canonical", ErrInvalidClosureCut)
	}
	lineageKey, err := basis.Key()
	if err != nil || input.LineageKey != lineageKey {
		return canonicalClosureCut{}, fmt.Errorf("%w: lineage key does not match basis", ErrInvalidClosureCut)
	}
	if err := validateClosureHead(input.Head, len(input.Events)); err != nil {
		return canonicalClosureCut{}, err
	}

	events := cloneClosureEvents(input.Events)
	slices.SortFunc(events, func(left, right ClosureEventRecord) int {
		if left.Revision < right.Revision {
			return -1
		}
		if left.Revision > right.Revision {
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	if err := validateClosureHistory(input.Head, events); err != nil {
		return canonicalClosureCut{}, err
	}

	members := append([]ClosureMemberRecord(nil), input.Members...)
	slices.SortFunc(members, func(left, right ClosureMemberRecord) int {
		return strings.Compare(left.NodeID, right.NodeID)
	})
	edges := append([]ClosureEdgeRecord(nil), input.Edges...)
	slices.SortFunc(edges, func(left, right ClosureEdgeRecord) int {
		return strings.Compare(left.ID, right.ID)
	})
	globalMembership, err := validateClosureMaterialization(lineageKey, events, members, edges)
	if err != nil {
		return canonicalClosureCut{}, err
	}
	if closureEdgesContainCycle(members, edges) {
		return canonicalClosureCut{}, fmt.Errorf("%w: supersedes relation contains a cycle", ErrInvalidClosureCut)
	}

	objectClaims := append([]ObjectClaimClassification(nil), input.ObjectClaims...)
	slices.SortFunc(objectClaims, func(left, right ObjectClaimClassification) int {
		return strings.Compare(left.NodeID, right.NodeID)
	})
	coverageComplete, err := validateObjectClaimCoverage(
		lineageKey,
		members,
		globalMembership,
		objectClaims,
	)
	if err != nil {
		return canonicalClosureCut{}, err
	}

	historyHash, err := closureHistoryHash(input.Head, events)
	if err != nil {
		return canonicalClosureCut{}, err
	}
	objectManifestHash, err := closureObjectManifestHash(basis, objectClaims)
	if err != nil {
		return canonicalClosureCut{}, err
	}
	cutHash, err := closureCutHash(lineageKey, basis, input.Head, historyHash, members, edges)
	if err != nil {
		return canonicalClosureCut{}, err
	}
	return canonicalClosureCut{
		lineageKey:              lineageKey,
		basis:                   basis,
		head:                    input.Head,
		events:                  events,
		members:                 members,
		edges:                   edges,
		objectClaims:            objectClaims,
		historyHash:             historyHash,
		cutHash:                 cutHash,
		objectClaimManifestHash: objectManifestHash,
		coverageComplete:        coverageComplete,
	}, nil
}

func validateClosureHead(head ClosureHead, eventCount int) error {
	if head.ChainKey != ChainKey || head.Revision <= 0 || head.HeadEventID == "" {
		return fmt.Errorf("%w: invalid admission head", ErrInvalidClosureCut)
	}
	if head.Revision != int64(eventCount) {
		return fmt.Errorf("%w: head revision %d has %d visible events", ErrInvalidClosureCut, head.Revision, eventCount)
	}
	return nil
}

func validateClosureHistory(head ClosureHead, events []ClosureEventRecord) error {
	seenIDs := make(map[string]struct{}, len(events))
	seenProposals := make(map[string]struct{}, len(events))
	seenDecisions := make(map[string]struct{}, len(events))
	for index, event := range events {
		wantRevision := int64(index + 1)
		if event.Revision != wantRevision {
			return fmt.Errorf("%w: event revision gap at %d", ErrInvalidClosureCut, wantRevision)
		}
		if _, duplicate := seenIDs[event.ID]; duplicate {
			return fmt.Errorf("%w: duplicate event %q", ErrInvalidClosureCut, event.ID)
		}
		seenIDs[event.ID] = struct{}{}
		if _, duplicate := seenProposals[event.ProposalOccurrence]; duplicate {
			return fmt.Errorf("%w: duplicate event proposal %q", ErrInvalidClosureCut, event.ProposalOccurrence)
		}
		seenProposals[event.ProposalOccurrence] = struct{}{}
		if _, duplicate := seenDecisions[event.AdmissionDecisionID]; duplicate {
			return fmt.Errorf("%w: duplicate event decision %q", ErrInvalidClosureCut, event.AdmissionDecisionID)
		}
		seenDecisions[event.AdmissionDecisionID] = struct{}{}
		if err := validateClosureEvent(event); err != nil {
			return err
		}
		if index == 0 {
			if event.PreviousRevision != 0 || event.PreviousEventID != "" {
				return fmt.Errorf("%w: first event has a previous head", ErrInvalidClosureCut)
			}
		} else {
			previous := events[index-1]
			if event.PreviousRevision != previous.Revision || event.PreviousEventID != previous.ID {
				return fmt.Errorf("%w: event %q does not follow %q", ErrInvalidClosureCut, event.ID, previous.ID)
			}
		}
	}
	if events[len(events)-1].ID != head.HeadEventID {
		return fmt.Errorf("%w: head event does not match visible history", ErrInvalidClosureCut)
	}
	return nil
}

func validateClosureEvent(event ClosureEventRecord) error {
	if err := event.Payload.Validate(); err != nil {
		return fmt.Errorf("%w: event %q payload: %v", ErrInvalidClosureCut, event.ID, err)
	}
	wantID, err := event.Payload.ID()
	if err != nil {
		return fmt.Errorf("%w: event %q identity: %v", ErrInvalidClosureCut, event.ID, err)
	}
	decision, err := NewDecisionPayloadV2(
		event.Payload.ProposalOccurrence,
		event.Payload.ReplacementNodeID,
		event.DecisionBy,
		event.DecisionReason,
	)
	if err != nil {
		return fmt.Errorf("%w: event %q decision: %v", ErrInvalidClosureCut, event.ID, err)
	}
	decisionHash, err := decision.Hash()
	if err != nil {
		return fmt.Errorf("%w: event %q decision hash: %v", ErrInvalidClosureCut, event.ID, err)
	}
	rowMatches := []struct {
		name    string
		matches bool
	}{
		{name: "event_id", matches: event.ID == wantID},
		{name: "contract_version", matches: event.ContractVersion == event.Payload.ContractVersion},
		{name: "revision", matches: event.Revision == event.Payload.Revision},
		{name: "previous_revision", matches: event.PreviousRevision == event.Payload.PreviousRevision},
		{name: "previous_event_id", matches: event.PreviousEventID == event.Payload.PreviousEventID},
		{name: "event_kind", matches: event.Kind == event.Payload.Kind},
		{name: "lineage_key", matches: event.LineageKey == event.Payload.LineageKey},
		{name: "replacement_node_id", matches: event.ReplacementNodeID == event.Payload.ReplacementNodeID},
		{name: "proposal_occurrence_id", matches: event.ProposalOccurrence == event.Payload.ProposalOccurrence},
		{name: "admission_decision_id", matches: event.AdmissionDecisionID == event.Payload.AdmissionDecisionID},
		{name: "admission_outcome", matches: event.AdmissionOutcome == admissionOutcome},
		{name: "request_payload_hash", matches: event.RequestPayloadHash == event.Payload.RequestPayloadHash},
		{name: "decision_payload_hash", matches: event.DecisionPayloadHash == event.Payload.DecisionPayloadHash},
		{name: "decision_audit", matches: decisionHash == event.DecisionPayloadHash},
		{name: "event_payload_hash", matches: event.EventPayloadHash == strings.TrimPrefix(event.ID, admissionEventIDPrefix)},
	}
	for _, field := range rowMatches {
		if !field.matches {
			if field.name == "event_id" {
				return fmt.Errorf(
					"%w: event row ID %q recomputes to %q",
					ErrInvalidClosureCut,
					event.ID,
					wantID,
				)
			}
			return fmt.Errorf("%w: event %q row field %s does not match its identity", ErrInvalidClosureCut, event.ID, field.name)
		}
	}
	return nil
}

func validateClosureMaterialization(
	lineageKey string,
	events []ClosureEventRecord,
	members []ClosureMemberRecord,
	edges []ClosureEdgeRecord,
) (map[string]ClosureMemberRecord, error) {
	globalMembers := make(map[string]ClosureMemberRecord)
	expectedEdges := make(map[string]ClosureEdgeRecord)
	for _, event := range events {
		if _, exists := globalMembers[event.ReplacementNodeID]; exists {
			return nil, fmt.Errorf("%w: replacement node %q was not fresh", ErrInvalidClosureCut, event.ReplacementNodeID)
		}
		bootstrapped := make(map[string]struct{}, len(event.Payload.BootstrappedTargetNodeIDs))
		for _, nodeID := range event.Payload.BootstrappedTargetNodeIDs {
			bootstrapped[nodeID] = struct{}{}
		}
		for _, targetID := range event.Payload.TargetNodeIDs {
			_, newlyBootstrapped := bootstrapped[targetID]
			member, exists := globalMembers[targetID]
			switch {
			case !exists && newlyBootstrapped:
				globalMembers[targetID] = ClosureMemberRecord{
					NodeID:                targetID,
					LineageKey:            event.LineageKey,
					FirstAdmissionEventID: event.ID,
					WasBootstrapped:       true,
				}
			case !exists:
				return nil, fmt.Errorf("%w: target %q was not previously enrolled", ErrInvalidClosureCut, targetID)
			case newlyBootstrapped:
				return nil, fmt.Errorf("%w: target %q was bootstrapped more than once", ErrInvalidClosureCut, targetID)
			case member.LineageKey != event.LineageKey:
				return nil, fmt.Errorf("%w: target %q crosses lineages", ErrInvalidClosureCut, targetID)
			}
		}
		globalMembers[event.ReplacementNodeID] = ClosureMemberRecord{
			NodeID:                event.ReplacementNodeID,
			LineageKey:            event.LineageKey,
			FirstAdmissionEventID: event.ID,
		}
		if event.LineageKey != lineageKey {
			continue
		}
		for _, targetID := range event.Payload.TargetNodeIDs {
			edgeID := evidencegraph.StableCanonicalID(
				"canon-edge",
				event.ReplacementNodeID,
				targetID,
				string(evidencegraph.CanonicalSupersedes),
			)
			expectedEdges[edgeID] = ClosureEdgeRecord{
				ID:                         edgeID,
				LineageKey:                 lineageKey,
				EventID:                    event.ID,
				FromNodeID:                 event.ReplacementNodeID,
				ToNodeID:                   targetID,
				Relation:                   string(evidencegraph.CanonicalSupersedes),
				OriginProposalOccurrenceID: event.ProposalOccurrence,
				TraceRef:                   event.ID,
				ReviewRef:                  event.AdmissionDecisionID,
				Method:                     "supersession_admission_edge",
				MethodVersion:              "v1",
			}
		}
	}
	expectedMembers := make(map[string]ClosureMemberRecord)
	for nodeID, member := range globalMembers {
		if member.LineageKey == lineageKey {
			expectedMembers[nodeID] = member
		}
	}
	if len(expectedMembers) == 0 {
		return nil, fmt.Errorf("%w: selected lineage has no admission events", ErrInvalidClosureCut)
	}
	if len(members) != len(expectedMembers) || len(edges) != len(expectedEdges) {
		return nil, fmt.Errorf("%w: materialized member or edge count differs from event replay", ErrInvalidClosureCut)
	}
	for index, member := range members {
		if index > 0 && members[index-1].NodeID == member.NodeID {
			return nil, fmt.Errorf("%w: duplicate member %q", ErrInvalidClosureCut, member.NodeID)
		}
		want, exists := expectedMembers[member.NodeID]
		if !exists || member != want {
			return nil, fmt.Errorf("%w: member %q differs from event replay", ErrInvalidClosureCut, member.NodeID)
		}
	}
	for index, edge := range edges {
		if index > 0 && edges[index-1].ID == edge.ID {
			return nil, fmt.Errorf("%w: duplicate edge %q", ErrInvalidClosureCut, edge.ID)
		}
		want, exists := expectedEdges[edge.ID]
		if !exists || edge != want {
			return nil, fmt.Errorf("%w: edge %q differs from event replay", ErrInvalidClosureCut, edge.ID)
		}
	}
	return globalMembers, nil
}

func validateObjectClaimCoverage(
	lineageKey string,
	members []ClosureMemberRecord,
	globalMembers map[string]ClosureMemberRecord,
	claims []ObjectClaimClassification,
) (bool, error) {
	memberIDs := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberIDs[member.NodeID] = struct{}{}
	}
	classifiedMembers := make(map[string]struct{}, len(members))
	complete := true
	for index, claim := range claims {
		if err := validateRecordID("object_claim_node_id", claim.NodeID, "canon-node:"); err != nil {
			return false, fmt.Errorf("%w: invalid object claim ID %q", ErrInvalidClosureCut, claim.NodeID)
		}
		if index > 0 && claims[index-1].NodeID == claim.NodeID {
			return false, fmt.Errorf("%w: duplicate object claim %q", ErrInvalidClosureCut, claim.NodeID)
		}
		if claim.LineageKey == "" {
			if _, enrolled := globalMembers[claim.NodeID]; enrolled {
				return false, fmt.Errorf("%w: enrolled object claim %q is marked unclassified", ErrInvalidClosureCut, claim.NodeID)
			}
			complete = false
			continue
		}
		if !validLineageKey(claim.LineageKey) {
			return false, fmt.Errorf("%w: invalid assigned lineage %q", ErrInvalidClosureCut, claim.LineageKey)
		}
		globalMember, enrolled := globalMembers[claim.NodeID]
		if !enrolled || globalMember.LineageKey != claim.LineageKey {
			return false, fmt.Errorf("%w: object claim %q classification does not match admission history", ErrInvalidClosureCut, claim.NodeID)
		}
		if claim.LineageKey == lineageKey {
			if _, exists := memberIDs[claim.NodeID]; !exists {
				return false, fmt.Errorf("%w: classified claim %q is absent from lineage membership", ErrInvalidClosureCut, claim.NodeID)
			}
			classifiedMembers[claim.NodeID] = struct{}{}
		}
	}
	for nodeID := range memberIDs {
		if _, exists := classifiedMembers[nodeID]; !exists {
			return false, fmt.Errorf("%w: lineage member %q is absent from the object manifest", ErrInvalidClosureCut, nodeID)
		}
	}
	return complete, nil
}

func closureEdgesContainCycle(members []ClosureMemberRecord, edges []ClosureEdgeRecord) bool {
	indegree := make(map[string]int, len(members))
	adjacency := make(map[string][]string, len(members))
	for _, member := range members {
		indegree[member.NodeID] = 0
	}
	for _, edge := range edges {
		if _, exists := indegree[edge.FromNodeID]; !exists {
			return true
		}
		if _, exists := indegree[edge.ToNodeID]; !exists {
			return true
		}
		adjacency[edge.FromNodeID] = append(adjacency[edge.FromNodeID], edge.ToNodeID)
		indegree[edge.ToNodeID]++
	}
	queue := make([]string, 0, len(members))
	for nodeID, degree := range indegree {
		if degree == 0 {
			queue = append(queue, nodeID)
		}
	}
	visited := 0
	for index := 0; index < len(queue); index++ {
		nodeID := queue[index]
		visited++
		for _, targetID := range adjacency[nodeID] {
			indegree[targetID]--
			if indegree[targetID] == 0 {
				queue = append(queue, targetID)
			}
		}
	}
	return visited != len(members)
}

func validLineageKey(value string) bool {
	const prefix = "lineage:v1:"
	return strings.HasPrefix(value, prefix) && validHash(strings.TrimPrefix(value, prefix))
}
