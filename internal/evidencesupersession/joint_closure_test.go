package evidencesupersession

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func testRegistration(t *testing.T, basis Basis, claim string, head ClosureHead) RegistrationRecord {
	t.Helper()
	key, err := basis.Key()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := closureObjectManifestHash(basis, []ObjectClaimClassification{{NodeID: claim}})
	if err != nil {
		t.Fatal(err)
	}
	anchor := RegistrationAnchor{ChainKey: ChainKey, Revision: head.Revision}
	if head.Revision > 0 {
		id := head.HeadEventID
		anchor.HeadEventID = &id
	}
	binding := RegistrationReviewBinding{StoreID: "store:test", ClaimID: claim, OriginalAdmissionID: "adm:original", OriginalOccurrenceID: "occ:original",
		SnapshotID: "snapshot:original", ViewID: "view:original", GroundingHash: testHash("1"), ObservationReceiptID: "receipt:new", ObservationHash: testHash("2"),
		Basis: basis, LineageKey: key, Anchor: anchor, CreationObjectManifestHash: manifest, GovernanceDigest: testHash("3")}
	subject, err := binding.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	p := InitialRegistrationPayload{ContractVersion: InitialRegistrationContractV1, Binding: binding, ReviewSubjectID: subject, RequestID: "request:" + claim, Reviewer: "reviewer", Reason: "reviewed first membership and derived effects"}
	id, err := p.ID()
	if err != nil {
		t.Fatal(err)
	}
	return RegistrationRecord{ID: id, Payload: p}
}

func testJointCut(t *testing.T) JointCutInput {
	t.Helper()
	basis := testBasis()
	key, err := basis.Key()
	if err != nil {
		t.Fatal(err)
	}
	head := ClosureHead{ChainKey: ChainKey}
	r := testRegistration(t, basis, "canon-node:c", head)
	return JointCutInput{ContractVersion: JointCutContractV1, StoreID: "store:test", LineageKey: key, Basis: basis, Head: head,
		Registrations: []RegistrationRecord{r}, Members: []JointMemberRecord{{NodeID: r.Payload.Binding.ClaimID, LineageKey: key, Origin: MembershipOrigin{Kind: MembershipRegistration, EventID: r.ID}}},
		ObjectClaims: []ObjectClaimClassification{{NodeID: r.Payload.Binding.ClaimID, LineageKey: key}}}
}

func cloneJoint(t *testing.T, cut JointCutInput) JointCutInput {
	t.Helper()
	data, err := json.Marshal(cut)
	if err != nil {
		t.Fatal(err)
	}
	var out JointCutInput
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func rebindRegistration(t *testing.T, r *RegistrationRecord) {
	t.Helper()
	subject, err := r.Payload.Binding.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	r.Payload.ReviewSubjectID = subject
	id, err := r.Payload.ID()
	if err != nil {
		t.Fatal(err)
	}
	r.ID = id
}

func appendJointReplacement(t *testing.T, cut JointCutInput, occurrence, replacement string, targets ...string) JointCutInput {
	t.Helper()
	b := newClosureTestBuilder(t)
	b.basis = cut.Basis
	b.lineageKey = cut.LineageKey
	b.events = cloneClosureEvents(cut.Events)
	origins := map[string]MembershipOrigin{}
	for _, m := range cut.Members {
		b.members[m.NodeID] = m.legacy()
		origins[m.NodeID] = m.Origin
	}
	for _, e := range cut.Edges {
		b.edges[e.ID] = e
	}
	for _, c := range cut.ObjectClaims {
		b.objectClaims[c.NodeID] = c
	}
	b.add(t, occurrence, replacement, targets)
	legacy := b.input()
	cut.Events = legacy.Events
	cut.Head = legacy.Head
	cut.Edges = legacy.Edges
	cut.ObjectClaims = legacy.ObjectClaims
	cut.Members = nil
	for _, m := range legacy.Members {
		origin, exists := origins[m.NodeID]
		if !exists {
			origin = MembershipOrigin{Kind: MembershipReplacement, EventID: m.FirstAdmissionEventID}
			if m.WasBootstrapped {
				origin.Kind = MembershipBootstrap
			}
		}
		cut.Members = append(cut.Members, JointMemberRecord{NodeID: m.NodeID, LineageKey: m.LineageKey, Origin: origin})
	}
	return cut
}

func assertJointNode(t *testing.T, got JointCurrentnessProjection, id string, status CurrentnessStatus) {
	t.Helper()
	for _, node := range got.Nodes {
		if node.NodeID == id {
			if node.Status != status {
				t.Fatalf("%s status=%s want=%s", id, node.Status, status)
			}
			return
		}
	}
	t.Fatalf("node %s missing", id)
}

func TestJointZeroHeadRegistrationAndV2Replacement(t *testing.T) {
	cut := testJointCut(t)
	absent := cloneJoint(t, cut)
	absent.Registrations = nil
	absent.Members = nil
	absent.ObjectClaims[0].LineageKey = ""
	p, err := ProjectJointCurrentness(absent)
	if err != nil {
		t.Fatal(err)
	}
	if p.Witness != nil || p.ClosureAvailable {
		t.Fatal("J0 produced witness without authority")
	}
	p, err = ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ClosureAvailable || p.Witness == nil || p.Head.Revision != 0 {
		t.Fatalf("J1=%+v", p)
	}
	assertJointNode(t, p, "canon-node:c", CurrentnessCurrent)
	before, err := json.Marshal(cut.Registrations[0])
	if err != nil {
		t.Fatal(err)
	}
	cut = appendJointReplacement(t, cut, "occ:c2", "canon-node:c2", "canon-node:c")
	if cut.Events[0].Revision != 1 || cut.Events[0].PreviousEventID != "" || len(cut.Events[0].Payload.BootstrappedTargetNodeIDs) != 0 {
		t.Fatal("J2 did not preserve zero-head v2 replacement semantics")
	}
	p, err = ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	assertJointNode(t, p, "canon-node:c", CurrentnessSuperseded)
	assertJointNode(t, p, "canon-node:c2", CurrentnessCurrent)
	after, err := json.Marshal(cut.Registrations[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("replacement rewrote R")
	}
	if err := ValidateJointCurrentness(cut, p); err != nil {
		t.Fatal(err)
	}
	// Domain validates history only; native exact request retry remains a separate gate.
	if err := cut.Registrations[0].validate(); err != nil {
		t.Fatal("historical R invalid after replacement", err)
	}
}

func TestJointNonzeroAnchorAndBranchMerge(t *testing.T) {
	cut := testJointCut(t)
	other := testBasis()
	other.ObjectID = "other-object"
	e := closureTestEvent(t, other, "occ:other2", "canon-node:other2", []string{"canon-node:other1"}, []string{"canon-node:other1"}, 0, "")
	cut.Events = []ClosureEventRecord{e}
	cut.Head = ClosureHead{ChainKey: ChainKey, Revision: 1, HeadEventID: e.ID}
	r := testRegistration(t, cut.Basis, "canon-node:c", cut.Head)
	cut.Registrations = []RegistrationRecord{r}
	cut.Members[0].Origin.EventID = r.ID
	cut = appendJointReplacement(t, cut, "occ:c2", "canon-node:c2", "canon-node:c")
	if cut.Events[1].PreviousEventID != e.ID || cut.Events[1].Revision != 2 {
		t.Fatal("J4 prefix not preserved")
	}
	cut = appendJointReplacement(t, cut, "occ:c3", "canon-node:c3", "canon-node:c")
	p, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	if p.Witness == nil || len(p.FrontierNodeIDs) != 2 {
		t.Fatalf("ambiguous closure=%+v", p)
	}
	assertJointNode(t, p, "canon-node:c2", CurrentnessAmbiguous)
	assertJointNode(t, p, "canon-node:c3", CurrentnessAmbiguous)
	cut = appendJointReplacement(t, cut, "occ:c4", "canon-node:c4", "canon-node:c2", "canon-node:c3")
	p, err = ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	assertJointNode(t, p, "canon-node:c4", CurrentnessCurrent)
}

func TestJointRejectsLateRegistrationAndRepeatedBootstrap(t *testing.T) {
	base := testJointCut(t)
	t.Run("late R cannot repair earlier event", func(t *testing.T) {
		cut := appendJointReplacement(t, base, "occ:c2", "canon-node:c2", "canon-node:c")
		cut.Registrations[0].Payload.Binding.Anchor = RegistrationAnchor{ChainKey: ChainKey, Revision: 1, HeadEventID: &cut.Events[0].ID}
		rebindRegistration(t, &cut.Registrations[0])
		for i := range cut.Members {
			if cut.Members[i].NodeID == "canon-node:c" {
				cut.Members[i].Origin.EventID = cut.Registrations[0].ID
			}
		}
		if _, err := ProjectJointCurrentness(cut); !errors.Is(err, ErrInvalidClosureCut) {
			t.Fatalf("late R accepted: %v", err)
		}
	})
	t.Run("registered target cannot bootstrap", func(t *testing.T) {
		cut := cloneJoint(t, base)
		event := closureTestEvent(t, cut.Basis, "occ:c2", "canon-node:c2", []string{"canon-node:c"}, []string{"canon-node:c"}, 0, "")
		cut.Events = []ClosureEventRecord{event}
		cut.Head = ClosureHead{ChainKey: ChainKey, Revision: 1, HeadEventID: event.ID}
		if _, err := ProjectJointCurrentness(cut); !errors.Is(err, ErrInvalidClosureCut) {
			t.Fatalf("double bootstrap accepted: %v", err)
		}
	})
}

func TestJointCurrentManifestChangesWithoutInvalidatingHistory(t *testing.T) {
	cut := testJointCut(t)
	before, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	cut.ObjectClaims = append(cut.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:c-prime"})
	after, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	if after.Witness != nil || after.ClosureAvailable || after.CutHash == before.CutHash {
		t.Fatal("J6 reused creation manifest as current closure")
	}
	assertJointNode(t, after, "canon-node:c", CurrentnessUnknown)
	if err := cut.Registrations[0].validate(); err != nil {
		t.Fatal("J6 invalidated historical authority", err)
	}
	if err := ValidateJointCurrentness(cut, before); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatal("stale projection accepted")
	}
}

func TestJointWitnessBindsSameHeadCatalogAndStore(t *testing.T) {
	cut := testJointCut(t)
	before, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	other := cut.Basis
	other.ObjectID = "independent-object"
	cut.Registrations = append(cut.Registrations, testRegistration(t, other, "canon-node:other", cut.Head))
	after, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	if before.Head != after.Head || before.Witness.ID == after.Witness.ID || before.Witness.RegistrationManifestHash == after.Witness.RegistrationManifestHash {
		t.Fatal("same head aliased changed registration catalog")
	}
	permuted := cloneJoint(t, cut)
	slices.Reverse(permuted.Registrations)
	again, err := ProjectJointCurrentness(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, again) {
		t.Fatal("catalog ordering changed witness")
	}
	changed := testJointCut(t)
	changed.StoreID = "store:other"
	changed.Registrations[0].Payload.Binding.StoreID = changed.StoreID
	rebindRegistration(t, &changed.Registrations[0])
	changed.Members[0].Origin.EventID = changed.Registrations[0].ID
	p, err := ProjectJointCurrentness(changed)
	if err != nil {
		t.Fatal(err)
	}
	if p.Witness.ID == before.Witness.ID {
		t.Fatal("witness did not bind store")
	}
}

func TestJointRejectsAuthorityAndMaterializationMutations(t *testing.T) {
	base := testJointCut(t)
	cases := []struct {
		name   string
		mutate func(*JointCutInput)
	}{
		{"missing R", func(c *JointCutInput) { c.Registrations = nil }},
		{"unknown cut", func(c *JointCutInput) { c.ContractVersion = "unknown" }},
		{"unknown registration", func(c *JointCutInput) { c.Registrations[0].Payload.ContractVersion = "unknown" }},
		{"wrong store", func(c *JointCutInput) { c.StoreID = "store:wrong" }},
		{"payload changed", func(c *JointCutInput) { c.Registrations[0].Payload.Reason = "other" }},
		{"subject changed", func(c *JointCutInput) { c.Registrations[0].Payload.ReviewSubjectID = "wrong" }},
		{"origin changed", func(c *JointCutInput) { c.Members[0].Origin.Kind = MembershipBootstrap }},
		{"origin reference changed", func(c *JointCutInput) { c.Members[0].Origin.EventID = testEventID("a") }},
		{"missing member", func(c *JointCutInput) { c.Members = nil }},
		{"duplicate member", func(c *JointCutInput) { c.Members = append(c.Members, c.Members[0]) }},
		{"missing manifest", func(c *JointCutInput) { c.ObjectClaims = nil }},
		{"duplicate manifest", func(c *JointCutInput) { c.ObjectClaims = append(c.ObjectClaims, c.ObjectClaims[0]) }},
		{"fake classification", func(c *JointCutInput) {
			c.ObjectClaims = append(c.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:fake", LineageKey: c.LineageKey})
		}},
		{"registered claim marked unclassified", func(c *JointCutInput) { c.ObjectClaims[0].LineageKey = "" }},
		{"duplicate R", func(c *JointCutInput) { c.Registrations = append(c.Registrations, c.Registrations[0]) }},
		{"different request same claim", func(c *JointCutInput) {
			r := c.Registrations[0]
			r.Payload.RequestID = "different"
			rebindRegistration(t, &r)
			c.Registrations = append(c.Registrations, r)
		}},
		{"same object other slot", func(c *JointCutInput) {
			b := c.Basis
			b.SlotID = "other"
			r := testRegistration(t, b, "canon-node:other", c.Head)
			c.Registrations = append(c.Registrations, r)
		}},
		{"zero anchor nonnull", func(c *JointCutInput) { id := ""; c.Registrations[0].Payload.Binding.Anchor.HeadEventID = &id }},
		{"negative anchor", func(c *JointCutInput) { c.Registrations[0].Payload.Binding.Anchor.Revision = -1 }},
		{"future anchor", func(c *JointCutInput) {
			r := &c.Registrations[0]
			id := testEventID("a")
			r.Payload.Binding.Anchor = RegistrationAnchor{ChainKey: ChainKey, Revision: 1, HeadEventID: &id}
			rebindRegistration(t, r)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cloneJoint(t, base)
			tc.mutate(&c)
			if _, err := ProjectJointCurrentness(c); !errors.Is(err, ErrInvalidClosureCut) {
				t.Fatalf("mutation accepted/error=%v", err)
			}
		})
	}
}

func TestJointLegacyV2ChainBranchMergeCompatibility(t *testing.T) {
	b := newClosureTestBuilder(t)
	for i, targets := range [][]string{{"canon-node:v1"}, {"canon-node:v1"}, {"canon-node:v2", "canon-node:v3"}} {
		b.add(t, fmt.Sprintf("occ:v%d", i+2), fmt.Sprintf("canon-node:v%d", i+2), targets)
		old := b.input()
		want, err := ProjectCurrentness(old)
		if err != nil {
			t.Fatal(err)
		}
		cut := JointCutInput{ContractVersion: JointCutContractV1, StoreID: "store:test", LineageKey: old.LineageKey, Basis: old.Basis, Head: old.Head, Events: old.Events, Edges: old.Edges, ObjectClaims: old.ObjectClaims}
		for _, m := range old.Members {
			kind := MembershipReplacement
			if m.WasBootstrapped {
				kind = MembershipBootstrap
			}
			cut.Members = append(cut.Members, JointMemberRecord{NodeID: m.NodeID, LineageKey: m.LineageKey, Origin: MembershipOrigin{Kind: kind, EventID: m.FirstAdmissionEventID}})
		}
		original, err := json.Marshal(cut.Events)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ProjectJointCurrentness(cut)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Nodes, want.Nodes) || !slices.Equal(got.FrontierNodeIDs, want.FrontierNodeIDs) || got.Witness.HistoryHash != want.HistoryHash {
			t.Fatal("legacy semantics/history changed")
		}
		after, err := json.Marshal(cut.Events)
		if err != nil {
			t.Fatal(err)
		}
		if string(original) != string(after) {
			t.Fatal("event payload mutated")
		}
	}
}

func TestRegistrationCatalogCapacityBoundaries(t *testing.T) {
	for _, count := range []int64{MaxRegistrationEvents - 1, MaxRegistrationEvents, MaxRegistrationEvents + 1} {
		err := ValidateRegistrationCatalogCapacity(count, 2)
		if (count > MaxRegistrationEvents) != errors.Is(err, ErrClosureLimitExceeded) {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
	for _, size := range []int64{MaxRegistrationCatalogBytes - 1, MaxRegistrationCatalogBytes, MaxRegistrationCatalogBytes + 1} {
		err := ValidateRegistrationCatalogCapacity(1, size)
		if (size > MaxRegistrationCatalogBytes) != errors.Is(err, ErrClosureLimitExceeded) {
			t.Fatalf("bytes=%d err=%v", size, err)
		}
	}
	cut := testJointCut(t)
	cut.Registrations = make([]RegistrationRecord, MaxRegistrationEvents+1)
	if _, err := ProjectJointCurrentness(cut); !errors.Is(err, ErrClosureLimitExceeded) {
		t.Fatalf("count truncation=%v", err)
	}
	// Valid individually bounded authorities must still respect the total byte cap.
	cut = testJointCut(t)
	base := cut.Registrations[0]
	cut.Registrations = nil
	for i := 0; i < MaxRegistrationEvents; i++ {
		r := base
		r.Payload.RequestID = fmt.Sprintf("request:%05d", i)
		r.Payload.Reason = strings.Repeat("x", maxDecisionReasonBytes)
		rebindRegistration(t, &r)
		cut.Registrations = append(cut.Registrations, r)
	}
	if _, err := ProjectJointCurrentness(cut); !errors.Is(err, ErrClosureLimitExceeded) {
		t.Fatalf("byte truncation=%v", err)
	}
}

func TestRegistrationRejectsUnboundOrNoncanonicalEvidence(t *testing.T) {
	base := testJointCut(t)
	tests := []struct {
		name   string
		mutate func(*InitialRegistrationPayload)
	}{
		{"store", func(p *InitialRegistrationPayload) { p.Binding.StoreID = "store:other" }},
		{"claim", func(p *InitialRegistrationPayload) { p.Binding.ClaimID = "canon-node:other" }},
		{"original admission", func(p *InitialRegistrationPayload) { p.Binding.OriginalAdmissionID = "adm:other" }},
		{"original occurrence", func(p *InitialRegistrationPayload) { p.Binding.OriginalOccurrenceID = "occ:other" }},
		{"snapshot", func(p *InitialRegistrationPayload) { p.Binding.SnapshotID = "snapshot:other" }},
		{"view", func(p *InitialRegistrationPayload) { p.Binding.ViewID = "view:other" }},
		{"grounding", func(p *InitialRegistrationPayload) { p.Binding.GroundingHash = testHash("a") }},
		{"observation", func(p *InitialRegistrationPayload) { p.Binding.ObservationReceiptID = "receipt:other" }},
		{"observation hash", func(p *InitialRegistrationPayload) { p.Binding.ObservationHash = testHash("b") }},
		{"governance", func(p *InitialRegistrationPayload) { p.Binding.GovernanceDigest = testHash("c") }},
		{"creation manifest", func(p *InitialRegistrationPayload) { p.Binding.CreationObjectManifestHash = "wrong" }},
		{"missing reviewer", func(p *InitialRegistrationPayload) { p.Reviewer = "" }},
		{"missing reason", func(p *InitialRegistrationPayload) { p.Reason = "" }},
		{"request normalization", func(p *InitialRegistrationPayload) { p.RequestID = " padded " }},
		{"reason limit", func(p *InitialRegistrationPayload) { p.Reason = strings.Repeat("x", maxDecisionReasonBytes+1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := cloneJoint(t, base).Registrations[0].Payload
			tc.mutate(&p)
			if _, err := p.ID(); err == nil {
				t.Fatal("accepted unbound/noncanonical registration")
			}
		})
	}
	original := base.Registrations[0]
	for _, field := range []string{"reviewer", "reason", "request"} {
		t.Run("decision identity "+field, func(t *testing.T) {
			r := original
			switch field {
			case "reviewer":
				r.Payload.Reviewer = "other"
			case "reason":
				r.Payload.Reason = "other reason"
			case "request":
				r.Payload.RequestID = "other request"
			}
			id, err := r.Payload.ID()
			if err != nil {
				t.Fatal(err)
			}
			if id == original.ID {
				t.Fatal("decision not bound")
			}
			if err := r.validate(); err == nil {
				t.Fatal("old event ID accepted changed decision")
			}
		})
	}
}

func TestJointNonzeroAnchorMustNameExactPrefix(t *testing.T) {
	cut := testJointCut(t)
	other := testBasis()
	other.ObjectID = "other-object"
	e := closureTestEvent(t, other, "occ:other2", "canon-node:other2", []string{"canon-node:other1"}, []string{"canon-node:other1"}, 0, "")
	cut.Events = []ClosureEventRecord{e}
	cut.Head = ClosureHead{ChainKey: ChainKey, Revision: 1, HeadEventID: e.ID}
	fake := testEventID("a")
	cut.Registrations[0].Payload.Binding.Anchor = RegistrationAnchor{ChainKey: ChainKey, Revision: 1, HeadEventID: &fake}
	rebindRegistration(t, &cut.Registrations[0])
	cut.Members[0].Origin.EventID = cut.Registrations[0].ID
	if _, err := ProjectJointCurrentness(cut); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatalf("wrong anchor accepted: %v", err)
	}
}

func TestJointDoesNotMutateAndCanonicalizesAllInputOrdering(t *testing.T) {
	cut := appendJointReplacement(t, testJointCut(t), "occ:c2", "canon-node:c2", "canon-node:c")
	cut = appendJointReplacement(t, cut, "occ:c3", "canon-node:c3", "canon-node:c")
	before, err := json.Marshal(cut)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(cut)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("projection mutated input")
	}
	slices.Reverse(cut.Events)
	slices.Reverse(cut.Members)
	slices.Reverse(cut.Edges)
	slices.Reverse(cut.ObjectClaims)
	got, err := ProjectJointCurrentness(cut)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("input ordering changed projection")
	}
}
