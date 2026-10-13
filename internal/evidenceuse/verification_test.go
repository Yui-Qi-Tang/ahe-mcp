package evidenceuse

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func verificationFixture() VerificationReport {
	return VerificationReport{Version: VerificationV1, Subject: VerificationSubject{StoreID: "synthetic", CanonicalID: "a", ClaimHash: "claim:a", Criterion: "statement-support", CriterionVersion: "v1", Scope: "function:X"}, Materials: []Material{{Identity: "provider:repo:path", Revision: "r1", ContentHash: "sha256:source"}}, MaterialsComplete: true, Result: CheckPass, CheckedAt: testTime}
}

func TestVerificationCacheRequiresLiveCompleteMatchingMaterialCheck(t *testing.T) {
	r := verificationFixture()
	ctx := context.Background()
	noRefresh, err := ReuseVerification(ctx, r, r.Subject, r.Materials, nil)
	if err != nil || noRefresh.Reusable {
		t.Fatal(noRefresh, err)
	}
	calls := 0
	refresh := func(_ context.Context, expected []Material) ([]Material, error) {
		calls++
		return slices.Clone(expected), nil
	}
	for range 2 {
		got, err := ReuseVerification(ctx, r, r.Subject, r.Materials, refresh)
		if err != nil || !got.Reusable {
			t.Fatal(got, err)
		}
	}
	if calls != 2 {
		t.Fatal("saved matching hashes bypassed refresh", calls)
	}
	for _, change := range []string{"revision", "hash", "missing", "duplicate", "extra"} {
		t.Run(change, func(t *testing.T) {
			got, err := ReuseVerification(ctx, r, r.Subject, r.Materials, func(_ context.Context, m []Material) ([]Material, error) {
				switch change {
				case "revision":
					m[0].Revision = "r2"
				case "hash":
					m[0].ContentHash = "different"
				case "missing":
					return nil, nil
				case "duplicate":
					m = append(m, m[0])
				case "extra":
					m = append(m, Material{Identity: "other", Revision: "r1", ContentHash: "hash"})
				}
				return m, nil
			})
			if err != nil || got.Reusable {
				t.Fatal(got, err)
			}
		})
	}
	if r.Materials[0].Revision != "r1" {
		t.Fatal("refresh mutated historical report")
	}
}

func TestVerificationReportRemainsScopedAndCannotClearWithdrawal(t *testing.T) {
	for _, field := range []string{"store", "claim", "hash", "criterion", "version", "scope", "incomplete", "inconclusive", "material"} {
		t.Run(field, func(t *testing.T) {
			r := verificationFixture()
			subject := r.Subject
			switch field {
			case "store":
				subject.StoreID = "other"
			case "claim":
				subject.CanonicalID = "other"
			case "hash":
				subject.ClaimHash = "changed"
			case "criterion":
				subject.Criterion = "source-fidelity"
			case "version":
				subject.CriterionVersion = "v2"
			case "scope":
				subject.Scope = "whole-repo"
			case "incomplete":
				r.MaterialsComplete = false
			case "inconclusive":
				r.Result = CheckInconclusive
			case "material":
				r.Materials = nil
			}
			got, err := ReuseVerification(context.Background(), r, subject, verificationFixture().Materials, func(context.Context, []Material) ([]Material, error) {
				t.Fatal("inapplicable report refreshed")
				return nil, nil
			})
			if err != nil || got.Reusable {
				t.Fatal(got, err)
			}
		})
	}
	r := verificationFixture()
	got, err := ReuseVerification(context.Background(), r, r.Subject, r.Materials, func(_ context.Context, m []Material) ([]Material, error) { return m, nil })
	if err != nil || !got.Reusable {
		t.Fatal(got, err)
	}
	s := fixture()
	withdraw(&s, "a")
	plan, err := CheckPlan(s, GroundedActionV1, []ActionBasis{{ActionID: "act", BasisIDs: []string{"a"}}}, testTime)
	if err != nil || plan.Accepted {
		t.Fatal("semantic cache bypassed governance", plan, err)
	}
}

func TestVerificationRefreshFailureAndCancellationDoNotHit(t *testing.T) {
	r := verificationFixture()
	want := errors.New("provider unavailable")
	got, err := ReuseVerification(context.Background(), r, r.Subject, r.Materials, func(context.Context, []Material) ([]Material, error) { return nil, want })
	if !errors.Is(err, want) || got.Reusable {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got, err = ReuseVerification(ctx, r, r.Subject, r.Materials, func(_ context.Context, m []Material) ([]Material, error) { cancel(); return m, nil })
	if !errors.Is(err, context.Canceled) || got.Reusable {
		t.Fatal(got, err)
	}
}
