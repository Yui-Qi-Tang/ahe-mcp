package evidencesupersession

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func prepareTestJoint(t *testing.T, cut JointCutInput) *JointProjector {
	t.Helper()
	p, err := PrepareJointAuthority(JointAuthorityInput{StoreID: cut.StoreID, Head: cut.Head, Events: cut.Events, Registrations: cut.Registrations})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testJointLineage(cut JointCutInput) JointLineageInput {
	return JointLineageInput{LineageKey: cut.LineageKey, Basis: cut.Basis, Members: cut.Members, Edges: cut.Edges, ObjectClaims: cut.ObjectClaims}
}

// Frozen from the pre-optimization projector. This protects all serialized
// projection/witness bytes, rather than comparing two wrappers of the new code.
func TestJointProjectorPreservesFrozenWitnessBytes(t *testing.T) {
	base := testJointCut(t)
	cuts := map[string]JointCutInput{"zero": base}
	cuts["replacement"] = appendJointReplacement(t, base, "occ:c2", "canon-node:c2", "canon-node:c")
	cuts["branch"] = appendJointReplacement(t, cuts["replacement"], "occ:c3", "canon-node:c3", "canon-node:c")
	cuts["merge"] = appendJointReplacement(t, cuts["branch"], "occ:c4", "canon-node:c4", "canon-node:c2", "canon-node:c3")
	incomplete := cloneJoint(t, base)
	incomplete.ObjectClaims = append(incomplete.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:extra"})
	cuts["incomplete"] = incomplete
	want := map[string]string{
		"zero":        "28a0b044277d5288c05b47dc49cb3c62cbe099543e76043141b0616cf2e717ff",
		"replacement": "841d7215aef1483a8af6992e7f4ef68865d1a3ee4fa31a4cbf2701bc80d8d20b",
		"branch":      "3a17c4fec50c7e60ac1f511570afb97885522acd2d3851f527d74db5978c997a",
		"merge":       "c48bbbd36f31ee6a8c652d072088af1165fad471c91bcfb614fc5e88faad7a69",
		"incomplete":  "cff02cdac67bb24074beacd8ab464d972c0e3bbb5f281357c16c6fb352667857",
	}
	for name, cut := range cuts {
		t.Run(name, func(t *testing.T) {
			p, err := prepareTestJoint(t, cut).ProjectLineage(testJointLineage(cut))
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want[name] {
				t.Fatalf("projection bytes changed: %s", got)
			}
		})
	}
}

func TestJointProjectorOwnsAuthorityAndRechecksEachLineage(t *testing.T) {
	cut := appendJointReplacement(t, testJointCut(t), "occ:c2", "canon-node:c2", "canon-node:c")
	other := cut.Basis
	other.ObjectID = "independent"
	r := testRegistration(t, other, "canon-node:other", cut.Head)
	cut.Registrations = append(cut.Registrations, r)
	lineage := testJointLineage(cloneJoint(t, cut))
	projector := prepareTestJoint(t, cut)
	want, err := projector.ProjectLineage(lineage)
	if err != nil {
		t.Fatal(err)
	}
	// The prepared frame must not borrow the registration's anchor pointer or
	// the event's nested target slices, nor the input slice backing arrays.
	*cut.Registrations[1].Payload.Binding.Anchor.HeadEventID = "changed"
	cut.Events[0].Payload.TargetNodeIDs[0] = "changed"
	cut.Registrations[0].ID = "changed"
	got, err := projector.ProjectLineage(lineage)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("borrowed caller authority", err)
	}
	got.FrontierNodeIDs[0] = "changed"
	got.Witness.FrontierNodeIDs[0] = "changed"
	again, err := projector.ProjectLineage(lineage)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("returned projection changed prepared authority", err)
	}
	otherCut := JointLineageInput{LineageKey: r.Payload.Binding.LineageKey, Basis: other,
		Members:      []JointMemberRecord{{NodeID: r.Payload.Binding.ClaimID, LineageKey: r.Payload.Binding.LineageKey, Origin: MembershipOrigin{Kind: MembershipRegistration, EventID: r.ID}}},
		ObjectClaims: []ObjectClaimClassification{{NodeID: r.Payload.Binding.ClaimID, LineageKey: r.Payload.Binding.LineageKey}}}
	if p, err := projector.ProjectLineage(otherCut); err != nil || p.Witness == nil {
		t.Fatal("second lineage", err)
	}
	otherCut.Members[0].Origin.EventID = lineage.Members[0].Origin.EventID
	if _, err := projector.ProjectLineage(otherCut); !errors.Is(err, ErrInvalidClosureCut) {
		t.Fatal("cross-lineage origin accepted", err)
	}
	lineage.ObjectClaims = append(lineage.ObjectClaims, ObjectClaimClassification{NodeID: "canon-node:extra"})
	if p, err := projector.ProjectLineage(lineage); err != nil || p.Witness != nil {
		t.Fatal("current manifest not checked", err)
	}
	for _, p := range []*JointProjector{nil, {}} {
		if _, err := p.ProjectLineage(lineage); !errors.Is(err, ErrInvalidClosureCut) {
			t.Fatal("unprepared authority accepted", err)
		}
	}
}
