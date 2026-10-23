package evidencesupersession

import (
	"fmt"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

type sourceObjectIdentity struct{ system, namespace, objectType, id string }

func registrationObject(b Basis) sourceObjectIdentity {
	return sourceObjectIdentity{b.SourceSystem, b.SourceNamespace, b.ObjectType, b.ObjectID}
}

type jointReplay struct {
	members        map[string]JointMemberRecord
	lineageMembers map[string]map[string]JointMemberRecord
	lineageEdges   map[string]map[string]ClosureEdgeRecord
}

func replayJointMembership(cut JointAuthorityInput) (jointReplay, error) {
	global := make(map[string]JointMemberRecord)
	lineages := make(map[string]bool)
	objects := make(map[sourceObjectIdentity]bool)
	expectedEdges := make(map[string]map[string]ClosureEdgeRecord)
	at := make(map[int64][]RegistrationRecord)
	for _, r := range cut.Registrations {
		at[r.Payload.Binding.Anchor.Revision] = append(at[r.Payload.Binding.Anchor.Revision], r)
	}
	for revision := int64(0); revision <= cut.Head.Revision; revision++ {
		if revision > 0 {
			e := cut.Events[revision-1]
			if _, exists := global[e.ReplacementNodeID]; exists {
				return jointReplay{}, fmt.Errorf("%w: joint replacement is not fresh", ErrInvalidClosureCut)
			}
			for _, target := range e.Payload.TargetNodeIDs {
				m, exists := global[target]
				bootstrap := slices.Contains(e.Payload.BootstrappedTargetNodeIDs, target)
				switch {
				case !exists && bootstrap:
					global[target] = JointMemberRecord{NodeID: target, LineageKey: e.LineageKey, Origin: MembershipOrigin{Kind: MembershipBootstrap, EventID: e.ID}}
				case !exists:
					return jointReplay{}, fmt.Errorf("%w: joint target missing before event revision %d", ErrInvalidClosureCut, revision)
				case bootstrap:
					return jointReplay{}, fmt.Errorf("%w: joint target bootstrapped twice", ErrInvalidClosureCut)
				case m.LineageKey != e.LineageKey:
					return jointReplay{}, fmt.Errorf("%w: joint target crosses lineages", ErrInvalidClosureCut)
				}
				if expectedEdges[e.LineageKey] == nil {
					expectedEdges[e.LineageKey] = make(map[string]ClosureEdgeRecord)
				}
				edgeID := evidencegraph.StableCanonicalID("canon-edge", e.ReplacementNodeID, target, string(evidencegraph.CanonicalSupersedes))
				expectedEdges[e.LineageKey][edgeID] = ClosureEdgeRecord{ID: edgeID, LineageKey: e.LineageKey, EventID: e.ID, FromNodeID: e.ReplacementNodeID, ToNodeID: target,
					Relation: string(evidencegraph.CanonicalSupersedes), OriginProposalOccurrenceID: e.ProposalOccurrence, TraceRef: e.ID, ReviewRef: e.AdmissionDecisionID,
					Method: "supersession_admission_edge", MethodVersion: "v1"}
			}
			global[e.ReplacementNodeID] = JointMemberRecord{NodeID: e.ReplacementNodeID, LineageKey: e.LineageKey, Origin: MembershipOrigin{Kind: MembershipReplacement, EventID: e.ID}}
			lineages[e.LineageKey] = true
			objects[registrationObject(e.Payload.LineageBasis)] = true
		}
		// Never preload all registrations: an anchor N only grants membership after
		// event N has independently succeeded. A late R cannot repair an earlier gap.
		for _, r := range at[revision] {
			b := r.Payload.Binding
			_, exists := global[b.ClaimID]
			if exists || lineages[b.LineageKey] || objects[registrationObject(b.Basis)] {
				return jointReplay{}, fmt.Errorf("%w: registration requires fresh claim membership, lineage and singleton object", ErrInvalidClosureCut)
			}
			global[b.ClaimID] = JointMemberRecord{NodeID: b.ClaimID, LineageKey: b.LineageKey, Origin: MembershipOrigin{Kind: MembershipRegistration, EventID: r.ID}}
			lineages[b.LineageKey] = true
			objects[registrationObject(b.Basis)] = true
		}
	}

	byLineage := make(map[string]map[string]JointMemberRecord)
	for id, m := range global {
		if byLineage[m.LineageKey] == nil {
			byLineage[m.LineageKey] = make(map[string]JointMemberRecord)
		}
		byLineage[m.LineageKey][id] = m
	}
	return jointReplay{members: global, lineageMembers: byLineage, lineageEdges: expectedEdges}, nil
}

func validateJointMaterialization(cut JointLineageInput, replay jointReplay) error {
	expectedMembers := replay.lineageMembers[cut.LineageKey]
	expectedEdges := replay.lineageEdges[cut.LineageKey]
	if len(cut.Members) != len(expectedMembers) || len(cut.Edges) != len(expectedEdges) {
		return fmt.Errorf("%w: joint materialization count mismatch", ErrInvalidClosureCut)
	}
	for i, m := range cut.Members {
		want, exists := expectedMembers[m.NodeID]
		if !exists || m != want || (i > 0 && cut.Members[i-1].NodeID == m.NodeID) {
			return fmt.Errorf("%w: joint membership authority mismatch", ErrInvalidClosureCut)
		}
	}
	for i, e := range cut.Edges {
		want, exists := expectedEdges[e.ID]
		if !exists || e != want || (i > 0 && cut.Edges[i-1].ID == e.ID) {
			return fmt.Errorf("%w: joint edge authority mismatch", ErrInvalidClosureCut)
		}
	}
	return nil
}
