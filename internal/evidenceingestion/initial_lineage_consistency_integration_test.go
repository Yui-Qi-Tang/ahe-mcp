//go:build integration

package evidenceingestion

import "testing"

// TEST APPROVAL STUB: synthetic membership and proposition bindings only.
func TestIntegrationInitialRegistrationConsistencyFrontier(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	in := registrationNativeFixture(t, ctx, pool, "consistency-registration", "AHE-42")
	scope := ConsistencyScope{Namespace: "initial-registration", ScopeRef: "release", Policy: ConsistencyFrontierPolicy}
	key := PropositionKey{Namespace: scope.Namespace, ScopeRef: scope.ScopeRef, LocalID: "old", Revision: "1"}
	propositionTestBind(t, ctx, pool, in.Review.ClaimID, "bind-old", key)
	before := consistencyRead(t, ctx, pool, scope)
	if len(before.Selection) != 1 || before.Selection[0].State != "blocked" {
		t.Fatal("unclassified claim participated", before.Selection)
	}
	// Binding changes the evidence snapshot; approve the refreshed review.
	review, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSubject = review.Subject
	if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	registered := consistencyRead(t, ctx, pool, scope)
	if len(registered.Currentness) != 1 || registered.Currentness[0].JointProjection == nil || registered.Currentness[0].Projection != nil || len(registered.Selection) != 1 || registered.Selection[0].State != "included" || registered.ID == before.ID {
		t.Fatal("consistency did not consume joint registration authority", registered)
	}
	p := createSupersessionExternalProposal(t, ctx, pool, "consistency-replacement", "r2", "Synthetic newer claim.")
	r, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: p.ProposalOccurrenceID, Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{in.Review.ClaimID}})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := AdmitReviewedSupersession(ctx, pool, ReviewedSupersessionInput{RequestID: "consistency-replacement", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic genuine replacement"})
	if err != nil {
		t.Fatal(err)
	}
	key.LocalID = "new"
	propositionTestBind(t, ctx, pool, replaced.Admission.CanonicalRef, "bind-new", key)
	after := consistencyRead(t, ctx, pool, scope)
	if len(after.Selection) != 2 || after.ID == registered.ID {
		t.Fatal("replacement view unchanged", after)
	}
	for _, s := range after.Selection {
		want := "included"
		if s.NodeID == in.Review.ClaimID {
			want = "excluded"
		}
		if s.State != want {
			t.Fatalf("selection %s=%s, want %s", s.NodeID, s.State, want)
		}
	}
}
