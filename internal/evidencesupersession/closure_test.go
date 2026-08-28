package evidencesupersession

import (
	"errors"
	"slices"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

func TestProjectCurrentnessUniqueBranchAndMerge(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	unique, err := ProjectCurrentness(builder.input())
	if err != nil {
		t.Fatalf("ProjectCurrentness(unique) error = %v", err)
	}
	assertClosureStatus(t, unique, "canon-node:v1", CurrentnessSuperseded)
	assertClosureStatus(t, unique, "canon-node:v2", CurrentnessCurrent)
	if !unique.ClosureAvailable || unique.Witness == nil ||
		!slices.Equal(unique.FrontierNodeIDs, []string{"canon-node:v2"}) {
		t.Fatalf("unique projection = %+v", unique)
	}
	const (
		wantHistoryHash        = "supersession-history:v2:sha256:e5b793e47632a7026e77ad01ca521a3a6cc5b3187e689fc396c26421b2999bd7"
		wantCutHash            = "closure-cut:v1:sha256:a3253b17fa0f465d16934c4e4027fe8eeb8bbc54b32e958a45bd0701271f0776"
		wantObjectManifestHash = "object-claim-manifest:v1:sha256:34a21fe9b2c52e64d603b83d186fbd5b8b13ebd2c4cafb50d1a37882711e3930"
		wantWitnessID          = "closure-witness:v1:sha256:dfaf2863b5eb1841203c0312178f8b5decbcffe310a17dccf037975b46a51b3c"
	)
	if unique.HistoryHash != wantHistoryHash || unique.CutHash != wantCutHash ||
		unique.ObjectClaimManifestHash != wantObjectManifestHash || unique.Witness.ID != wantWitnessID {
		t.Fatalf(
			"unique identities: history=%q cut=%q object=%q witness=%q",
			unique.HistoryHash,
			unique.CutHash,
			unique.ObjectClaimManifestHash,
			unique.Witness.ID,
		)
	}

	builder.add(t, "occ:v3", "canon-node:v3", []string{"canon-node:v1"})
	branch, err := ProjectCurrentness(builder.input())
	if err != nil {
		t.Fatalf("ProjectCurrentness(branch) error = %v", err)
	}
	assertClosureStatus(t, branch, "canon-node:v1", CurrentnessSuperseded)
	assertClosureStatus(t, branch, "canon-node:v2", CurrentnessAmbiguous)
	assertClosureStatus(t, branch, "canon-node:v3", CurrentnessAmbiguous)
	if !slices.Equal(branch.FrontierNodeIDs, []string{"canon-node:v2", "canon-node:v3"}) {
		t.Fatalf("branch frontiers = %v", branch.FrontierNodeIDs)
	}

	builder.add(t, "occ:v4", "canon-node:v4", []string{"canon-node:v2", "canon-node:v3"})
	merged, err := ProjectCurrentness(builder.input())
	if err != nil {
		t.Fatalf("ProjectCurrentness(merged) error = %v", err)
	}
	for _, nodeID := range []string{"canon-node:v1", "canon-node:v2", "canon-node:v3"} {
		assertClosureStatus(t, merged, nodeID, CurrentnessSuperseded)
	}
	assertClosureStatus(t, merged, "canon-node:v4", CurrentnessCurrent)
	if !slices.Equal(merged.FrontierNodeIDs, []string{"canon-node:v4"}) {
		t.Fatalf("merged frontiers = %v", merged.FrontierNodeIDs)
	}
	if merged.Semantics != CurrentnessSemanticsV1 || merged.CoveragePolicy != ObjectCoveragePolicyV1 ||
		merged.Witness.Semantics != CurrentnessSemanticsV1 {
		t.Fatalf("merged contract = %+v", merged)
	}
}

func TestProjectCurrentnessUnclassifiedObjectClaimFailsClosed(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	input := builder.input()
	input.ObjectClaims = append(input.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:unclassified"})

	projection, err := ProjectCurrentness(input)
	if err != nil {
		t.Fatalf("ProjectCurrentness() error = %v", err)
	}
	if projection.ClosureAvailable || projection.Witness != nil {
		t.Fatalf("unclassified projection unexpectedly has closure: %+v", projection)
	}
	assertClosureStatus(t, projection, "canon-node:v1", CurrentnessSuperseded)
	assertClosureStatus(t, projection, "canon-node:v2", CurrentnessUnknown)
	if projection.ObjectClaimManifestHash == "" || projection.CutHash == "" {
		t.Fatalf("unclassified projection omitted snapshot coordinates: %+v", projection)
	}
}

func TestProjectCurrentnessDeterministicAcrossInputOrder(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	builder.add(t, "occ:v3", "canon-node:v3", []string{"canon-node:v1"})
	canonical := builder.input()
	want, err := ProjectCurrentness(canonical)
	if err != nil {
		t.Fatalf("ProjectCurrentness(canonical) error = %v", err)
	}

	permuted := builder.input()
	slices.Reverse(permuted.Events)
	slices.Reverse(permuted.Members)
	slices.Reverse(permuted.Edges)
	slices.Reverse(permuted.ObjectClaims)
	got, err := ProjectCurrentness(permuted)
	if err != nil {
		t.Fatalf("ProjectCurrentness(permuted) error = %v", err)
	}
	if want.CutHash != got.CutHash ||
		want.ObjectClaimManifestHash != got.ObjectClaimManifestHash ||
		want.Witness.ID != got.Witness.ID ||
		!slices.Equal(want.FrontierNodeIDs, got.FrontierNodeIDs) {
		t.Fatalf("canonical projection = %+v, permuted = %+v", want, got)
	}
	if err := ValidateCurrentnessProjection(permuted, got); err != nil {
		t.Fatalf("ValidateCurrentnessProjection() error = %v", err)
	}
	got.Nodes[0].Status = CurrentnessCurrent
	if err := ValidateCurrentnessProjection(permuted, got); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatalf("modified projection error = %v, want ErrInvalidClosureCut", err)
	}
}

func TestProjectCurrentnessRejectsHistoryAndMaterializationDrift(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	base := builder.input()
	otherBasis := base.Basis
	otherBasis.SlotID = "other-field"
	otherLineageKey, err := otherBasis.Key()
	if err != nil {
		t.Fatalf("other Basis.Key() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ClosureCutInput)
	}{
		{name: "head revision", mutate: func(input *ClosureCutInput) { input.Head.Revision++ }},
		{name: "head event", mutate: func(input *ClosureCutInput) { input.Head.HeadEventID = testEventID("a") }},
		{name: "event decision", mutate: func(input *ClosureCutInput) { input.Events[0].DecisionReason = "changed" }},
		{name: "event payload hash", mutate: func(input *ClosureCutInput) { input.Events[0].EventPayloadHash = testHash("f") }},
		{name: "missing member", mutate: func(input *ClosureCutInput) { input.Members = input.Members[1:] }},
		{name: "wrong first event", mutate: func(input *ClosureCutInput) { input.Members[0].FirstAdmissionEventID = testEventID("b") }},
		{name: "missing edge", mutate: func(input *ClosureCutInput) { input.Edges = nil }},
		{name: "wrong direction", mutate: func(input *ClosureCutInput) {
			input.Edges[0].FromNodeID, input.Edges[0].ToNodeID = input.Edges[0].ToNodeID, input.Edges[0].FromNodeID
		}},
		{name: "wrong edge provenance", mutate: func(input *ClosureCutInput) { input.Edges[0].ReviewRef = "adm:other" }},
		{name: "member missing from object manifest", mutate: func(input *ClosureCutInput) { input.ObjectClaims = input.ObjectClaims[1:] }},
		{name: "classified nonmember", mutate: func(input *ClosureCutInput) {
			input.ObjectClaims = append(input.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:extra", LineageKey: input.LineageKey})
		}},
		{name: "fake other-lineage classification", mutate: func(input *ClosureCutInput) {
			input.ObjectClaims = append(input.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:extra", LineageKey: otherLineageKey})
		}},
		{name: "malformed unclassified claim", mutate: func(input *ClosureCutInput) {
			input.ObjectClaims = append(input.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneClosureInput(base)
			test.mutate(&input)
			if _, err := ProjectCurrentness(input); !errors.Is(err, ErrInvalidClosureCut) {
				t.Fatalf("ProjectCurrentness() error = %v, want ErrInvalidClosureCut", err)
			}
		})
	}
}

func TestProjectCurrentnessRejectsDuplicateGlobalAuditBindings(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	base := builder.input()

	duplicateProposal := closureTestEvent(
		t,
		base.Basis,
		"occ:v2",
		"canon-node:v3",
		[]string{"canon-node:v1"},
		nil,
		base.Head.Revision,
		base.Head.HeadEventID,
	)
	proposalInput := cloneClosureInput(base)
	proposalInput.Events = append(proposalInput.Events, duplicateProposal)
	proposalInput.Head.Revision = duplicateProposal.Revision
	proposalInput.Head.HeadEventID = duplicateProposal.ID
	if _, err := ProjectCurrentness(proposalInput); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatalf("duplicate proposal error = %v, want ErrInvalidClosureCut", err)
	}

	duplicateDecision := closureTestEvent(
		t,
		base.Basis,
		"occ:v3",
		"canon-node:v3",
		[]string{"canon-node:v1"},
		nil,
		base.Head.Revision,
		base.Head.HeadEventID,
	)
	duplicateDecision.Payload.AdmissionDecisionID = base.Events[0].AdmissionDecisionID
	duplicateDecision.AdmissionDecisionID = base.Events[0].AdmissionDecisionID
	duplicateDecisionID, err := duplicateDecision.Payload.ID()
	if err != nil {
		t.Fatalf("duplicate decision event ID error = %v", err)
	}
	duplicateDecision.ID = duplicateDecisionID
	duplicateDecision.EventPayloadHash = stringsTrimEventPrefix(duplicateDecisionID)
	decisionInput := cloneClosureInput(base)
	decisionInput.Events = append(decisionInput.Events, duplicateDecision)
	decisionInput.Head.Revision = duplicateDecision.Revision
	decisionInput.Head.HeadEventID = duplicateDecision.ID
	if _, err := ProjectCurrentness(decisionInput); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatalf("duplicate decision error = %v, want ErrInvalidClosureCut", err)
	}
}

func TestProjectCurrentnessValidatesOtherLineageEventsWithoutMixingProjection(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	input := builder.input()
	otherBasis := input.Basis
	otherBasis.SlotID = "other-field"

	other := closureTestEvent(
		t,
		otherBasis,
		"occ:other-v2",
		"canon-node:other-v2",
		[]string{"canon-node:other-v1"},
		[]string{"canon-node:other-v1"},
		input.Head.Revision,
		input.Head.HeadEventID,
	)
	input.Events = append(input.Events, other)
	input.Head.Revision = other.Revision
	input.Head.HeadEventID = other.ID

	projection, err := ProjectCurrentness(input)
	if err != nil {
		t.Fatalf("ProjectCurrentness(other lineage) error = %v", err)
	}
	if len(projection.Nodes) != 2 || !projection.ClosureAvailable {
		t.Fatalf("other-lineage event changed selected projection = %+v", projection)
	}
	assertClosureStatus(t, projection, "canon-node:v1", CurrentnessSuperseded)
	assertClosureStatus(t, projection, "canon-node:v2", CurrentnessCurrent)

	crossLineage := cloneClosureInput(builder.input())
	invalid := closureTestEvent(
		t,
		otherBasis,
		"occ:other-cross",
		"canon-node:other-v3",
		[]string{"canon-node:v1"},
		nil,
		crossLineage.Head.Revision,
		crossLineage.Head.HeadEventID,
	)
	crossLineage.Events = append(crossLineage.Events, invalid)
	crossLineage.Head.Revision = invalid.Revision
	crossLineage.Head.HeadEventID = invalid.ID
	if _, err := ProjectCurrentness(crossLineage); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatalf("ProjectCurrentness(cross lineage) error = %v, want ErrInvalidClosureCut", err)
	}
}

func TestProjectCurrentnessRejectsLimitsAndCycles(t *testing.T) {
	builder := newClosureTestBuilder(t)
	builder.add(t, "occ:v2", "canon-node:v2", []string{"canon-node:v1"})
	input := builder.input()
	input.Events = make([]ClosureEventRecord, MaxClosureEvents+1)
	if _, err := ProjectCurrentness(input); !errors.Is(err, ErrClosureLimitExceeded) {
		t.Fatalf("ProjectCurrentness(over limit) error = %v, want ErrClosureLimitExceeded", err)
	}

	members := []ClosureMemberRecord{{NodeID: "canon-node:a"}, {NodeID: "canon-node:b"}}
	edges := []ClosureEdgeRecord{
		{FromNodeID: "canon-node:a", ToNodeID: "canon-node:b"},
		{FromNodeID: "canon-node:b", ToNodeID: "canon-node:a"},
	}
	if !closureEdgesContainCycle(members, edges) {
		t.Fatal("closureEdgesContainCycle() = false, want true")
	}
}

type closureTestBuilder struct {
	basis        Basis
	lineageKey   string
	events       []ClosureEventRecord
	members      map[string]ClosureMemberRecord
	edges        map[string]ClosureEdgeRecord
	objectClaims map[string]ObjectClaimClassification
}

func newClosureTestBuilder(t *testing.T) *closureTestBuilder {
	t.Helper()
	basis := testBasis()
	lineageKey, err := basis.Key()
	if err != nil {
		t.Fatalf("Basis.Key() error = %v", err)
	}
	return &closureTestBuilder{
		basis:        basis,
		lineageKey:   lineageKey,
		members:      make(map[string]ClosureMemberRecord),
		edges:        make(map[string]ClosureEdgeRecord),
		objectClaims: make(map[string]ObjectClaimClassification),
	}
}

func (builder *closureTestBuilder) add(
	t *testing.T,
	proposalOccurrence string,
	replacementNodeID string,
	targetNodeIDs []string,
) {
	t.Helper()
	revision := int64(len(builder.events))
	headEventID := ""
	if len(builder.events) > 0 {
		headEventID = builder.events[len(builder.events)-1].ID
	}
	request, err := NewRequestPayloadV2(
		proposalOccurrence,
		replacementNodeID,
		builder.basis,
		targetNodeIDs,
		revision,
		headEventID,
	)
	if err != nil {
		t.Fatalf("NewRequestPayloadV2() error = %v", err)
	}
	decisionBy := "reviewer"
	decisionReason := "reviewed " + proposalOccurrence
	decision, err := NewDecisionPayloadV2(
		proposalOccurrence,
		replacementNodeID,
		decisionBy,
		decisionReason,
	)
	if err != nil {
		t.Fatalf("NewDecisionPayloadV2() error = %v", err)
	}
	bootstrapped := make([]string, 0, len(targetNodeIDs))
	for _, targetID := range request.TargetNodeIDs {
		if _, exists := builder.members[targetID]; !exists {
			bootstrapped = append(bootstrapped, targetID)
		}
	}
	admissionDecisionID := "adm:" + proposalOccurrence
	payload, err := NewAtomicReplacementEventV2(request, decision, admissionDecisionID, bootstrapped)
	if err != nil {
		t.Fatalf("NewAtomicReplacementEventV2() error = %v", err)
	}
	eventID, err := payload.ID()
	if err != nil {
		t.Fatalf("AdmissionEventPayloadV2.ID() error = %v", err)
	}
	decisionHash, err := decision.Hash()
	if err != nil {
		t.Fatalf("DecisionPayloadV2.Hash() error = %v", err)
	}
	builder.events = append(builder.events, ClosureEventRecord{
		ID:                  eventID,
		ContractVersion:     payload.ContractVersion,
		Revision:            payload.Revision,
		PreviousRevision:    payload.PreviousRevision,
		PreviousEventID:     payload.PreviousEventID,
		Kind:                payload.Kind,
		LineageKey:          payload.LineageKey,
		ReplacementNodeID:   payload.ReplacementNodeID,
		ProposalOccurrence:  payload.ProposalOccurrence,
		AdmissionDecisionID: payload.AdmissionDecisionID,
		AdmissionOutcome:    admissionOutcome,
		DecisionBy:          decisionBy,
		DecisionReason:      decisionReason,
		RequestPayloadHash:  payload.RequestPayloadHash,
		DecisionPayloadHash: decisionHash,
		EventPayloadHash:    stringsTrimEventPrefix(eventID),
		Payload:             payload,
	})
	builder.members[replacementNodeID] = ClosureMemberRecord{
		NodeID:                replacementNodeID,
		LineageKey:            builder.lineageKey,
		FirstAdmissionEventID: eventID,
	}
	builder.objectClaims[replacementNodeID] = ObjectClaimClassification{
		NodeID:     replacementNodeID,
		LineageKey: builder.lineageKey,
	}
	bootstrappedSet := make(map[string]struct{}, len(bootstrapped))
	for _, nodeID := range bootstrapped {
		bootstrappedSet[nodeID] = struct{}{}
		builder.members[nodeID] = ClosureMemberRecord{
			NodeID:                nodeID,
			LineageKey:            builder.lineageKey,
			FirstAdmissionEventID: eventID,
			WasBootstrapped:       true,
		}
		builder.objectClaims[nodeID] = ObjectClaimClassification{
			NodeID:     nodeID,
			LineageKey: builder.lineageKey,
		}
	}
	for _, targetID := range request.TargetNodeIDs {
		edgeID := evidencegraph.StableCanonicalID(
			"canon-edge",
			replacementNodeID,
			targetID,
			string(evidencegraph.CanonicalSupersedes),
		)
		builder.edges[edgeID] = ClosureEdgeRecord{
			ID:                         edgeID,
			LineageKey:                 builder.lineageKey,
			EventID:                    eventID,
			FromNodeID:                 replacementNodeID,
			ToNodeID:                   targetID,
			Relation:                   string(evidencegraph.CanonicalSupersedes),
			OriginProposalOccurrenceID: proposalOccurrence,
			TraceRef:                   eventID,
			ReviewRef:                  admissionDecisionID,
			Method:                     "supersession_admission_edge",
			MethodVersion:              "v1",
		}
	}
	_ = bootstrappedSet
}

func closureTestEvent(
	t *testing.T,
	basis Basis,
	proposalOccurrence string,
	replacementNodeID string,
	targetNodeIDs []string,
	bootstrappedNodeIDs []string,
	previousRevision int64,
	previousEventID string,
) ClosureEventRecord {
	t.Helper()
	request, err := NewRequestPayloadV2(
		proposalOccurrence,
		replacementNodeID,
		basis,
		targetNodeIDs,
		previousRevision,
		previousEventID,
	)
	if err != nil {
		t.Fatalf("NewRequestPayloadV2() error = %v", err)
	}
	decisionBy := "reviewer"
	decisionReason := "reviewed " + proposalOccurrence
	decision, err := NewDecisionPayloadV2(
		proposalOccurrence,
		replacementNodeID,
		decisionBy,
		decisionReason,
	)
	if err != nil {
		t.Fatalf("NewDecisionPayloadV2() error = %v", err)
	}
	payload, err := NewAtomicReplacementEventV2(
		request,
		decision,
		"adm:"+proposalOccurrence,
		bootstrappedNodeIDs,
	)
	if err != nil {
		t.Fatalf("NewAtomicReplacementEventV2() error = %v", err)
	}
	eventID, err := payload.ID()
	if err != nil {
		t.Fatalf("AdmissionEventPayloadV2.ID() error = %v", err)
	}
	return ClosureEventRecord{
		ID:                  eventID,
		ContractVersion:     payload.ContractVersion,
		Revision:            payload.Revision,
		PreviousRevision:    payload.PreviousRevision,
		PreviousEventID:     payload.PreviousEventID,
		Kind:                payload.Kind,
		LineageKey:          payload.LineageKey,
		ReplacementNodeID:   payload.ReplacementNodeID,
		ProposalOccurrence:  payload.ProposalOccurrence,
		AdmissionDecisionID: payload.AdmissionDecisionID,
		AdmissionOutcome:    admissionOutcome,
		DecisionBy:          decisionBy,
		DecisionReason:      decisionReason,
		RequestPayloadHash:  payload.RequestPayloadHash,
		DecisionPayloadHash: payload.DecisionPayloadHash,
		EventPayloadHash:    stringsTrimEventPrefix(eventID),
		Payload:             payload,
	}
}

func (builder *closureTestBuilder) input() ClosureCutInput {
	members := make([]ClosureMemberRecord, 0, len(builder.members))
	for _, member := range builder.members {
		members = append(members, member)
	}
	edges := make([]ClosureEdgeRecord, 0, len(builder.edges))
	for _, edge := range builder.edges {
		edges = append(edges, edge)
	}
	claims := make([]ObjectClaimClassification, 0, len(builder.objectClaims))
	for _, claim := range builder.objectClaims {
		claims = append(claims, claim)
	}
	return ClosureCutInput{
		LineageKey: builder.lineageKey,
		Basis:      builder.basis,
		Head: ClosureHead{
			ChainKey:    ChainKey,
			Revision:    int64(len(builder.events)),
			HeadEventID: builder.events[len(builder.events)-1].ID,
		},
		Events:       cloneClosureEvents(builder.events),
		Members:      members,
		Edges:        edges,
		ObjectClaims: claims,
	}
}

func assertClosureStatus(
	t *testing.T,
	projection CurrentnessProjection,
	nodeID string,
	want CurrentnessStatus,
) {
	t.Helper()
	for _, node := range projection.Nodes {
		if node.NodeID == nodeID {
			if node.Status != want {
				t.Fatalf("node %s status = %q, want %q", nodeID, node.Status, want)
			}
			return
		}
	}
	t.Fatalf("node %s missing from projection", nodeID)
}

func cloneClosureInput(input ClosureCutInput) ClosureCutInput {
	input.Events = cloneClosureEvents(input.Events)
	input.Members = append([]ClosureMemberRecord(nil), input.Members...)
	input.Edges = append([]ClosureEdgeRecord(nil), input.Edges...)
	input.ObjectClaims = append([]ObjectClaimClassification(nil), input.ObjectClaims...)
	return input
}

func stringsTrimEventPrefix(eventID string) string {
	return eventID[len(admissionEventIDPrefix):]
}
