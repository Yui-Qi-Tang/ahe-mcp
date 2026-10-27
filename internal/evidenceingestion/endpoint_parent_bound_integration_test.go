//go:build integration

package evidenceingestion

import (
	"fmt"
	"slices"
	"testing"
)

// TEST APPROVAL STUB: synthetic sources in the selected disposable database.
func TestIntegrationEndpointCompleteParentBound(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := make([]string, 33)
	for i := range parents {
		in := registrationNativeFixture(t, ctx, pool, fmt.Sprintf("bound-%d", i), fmt.Sprintf("BOUND-%d", i))
		r, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
		if err != nil {
			t.Fatal(err)
		}
		in.ExpectedSubject = r.Subject
		if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
			t.Fatal(err)
		}
		parents[i] = in.Review.ClaimID
	}
	slices.Sort(parents)
	for _, count := range []int{8, 9, 32, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			occ := historicalPending(t, ctx, pool, fmt.Sprintf("bound-derived-%d", count))
			req := registrationCurrentEndpointRequest(occ, parents[:count]...)
			r, err := LoadEndpointReview(ctx, pool, req)
			if count == 33 {
				if err == nil {
					t.Fatal("33 parents accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Display.Ancestors) != count || len(r.Display.SourceLeaves) != count {
				t.Fatal("incomplete parent display")
			}
			in := ReviewedEndpointAdmissionInput{RequestID: fmt.Sprintf("bound-admit-%d", count), Review: req, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
			got, err := AdmitReviewedEndpoint(ctx, pool, in)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Admission.ParentNodeIDs, parents[:count]) {
				t.Fatal("parent manifest changed")
			}
			replay, err := AdmitReviewedEndpoint(ctx, pool, in)
			if err != nil || !replay.Admission.Replayed {
				t.Fatal("exact replay", err)
			}
			if count == 32 {
				child := historicalPending(t, ctx, pool, "bound-nested")
				nested, err := LoadEndpointReview(ctx, pool, registrationCurrentEndpointRequest(child, got.Admission.CanonicalRef))
				if err != nil || len(nested.Display.Ancestors) != 33 {
					t.Fatal("32-parent ancestor", err)
				}
			}
		})
	}
}
