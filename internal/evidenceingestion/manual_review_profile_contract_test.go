package evidenceingestion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManualReviewContractRequiresExactProfile(t *testing.T) {
	profile := ManualReviewProfileInput{ContractVersion: ManualSourceReviewProfileV1,
		Title: "Synthetic requirement", Location: "fixture:requirement", Coverage: ManualReviewCoverageSubmittedText,
		Limitations: []string{}}
	data, err := deterministicJSON(profile)
	if err != nil {
		t.Fatal(err)
	}
	binding := ManualReviewProfileBinding{SourceSnapshotID: "srcsnap:" + strings.Repeat("a", 64),
		IntakeRequestID: "fixture-request", ContractVersion: ManualSourceReviewProfileV1, Profile: profile, ProfileHash: contentHash(data)}
	if err := ValidateManualReviewProfileBinding(binding); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ManualReviewProfileBinding){
		"changed title":       func(b *ManualReviewProfileBinding) { b.Profile.Title = "Changed" },
		"unknown contract":    func(b *ManualReviewProfileBinding) { b.ContractVersion = "v99" },
		"invalid source":      func(b *ManualReviewProfileBinding) { b.SourceSnapshotID = "invented" },
		"missing limitations": func(b *ManualReviewProfileBinding) { b.Profile.Limitations = nil },
		"unbounded excerpt":   func(b *ManualReviewProfileBinding) { b.Profile.Coverage = ManualReviewCoverageExactExcerpt },
	} {
		t.Run(name, func(t *testing.T) {
			bad := *cloneManualReviewProfile(&binding)
			mutate(&bad)
			if ValidateManualReviewProfileBinding(bad) == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestReviewPackageCloneDetachesOptionalProfile(t *testing.T) {
	original := ReviewPackage{ProposalBasis: ProposalBasis{ManualReviewProfile: &ManualReviewProfileBinding{
		Profile: ManualReviewProfileInput{Title: "Before", Limitations: []string{"Original limitation"}},
	}}}
	cloned := cloneReviewPackage(original)
	cloned.ProposalBasis.ManualReviewProfile.Profile.Title = "After"
	cloned.ProposalBasis.ManualReviewProfile.Profile.Limitations[0] = "Changed"
	got := original.ProposalBasis.ManualReviewProfile.Profile
	if got.Title != "Before" || got.Limitations[0] != "Original limitation" {
		t.Fatal("review copy aliases its caller")
	}
	original.ProposalBasis.ManualReviewProfile.Profile.Limitations = nil
	if cloneReviewPackage(original).ProposalBasis.ManualReviewProfile.Profile.Limitations != nil {
		t.Fatal("cloning normalized missing limitations into an explicit empty list")
	}
}

func TestPlainReviewWireDoesNotAcquireManualProfile(t *testing.T) {
	snapshot := reviewableIngestionTestSnapshot(t, "session:plain-contract")
	display, _, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(display.PayloadUTF8), &fields); err != nil {
		t.Fatal(err)
	}
	// Re-encode the prior wire shape independently, with the reserved field absent.
	var basis map[string]json.RawMessage
	if err := json.Unmarshal(fields["proposal_basis"], &basis); err != nil {
		t.Fatal(err)
	}
	if _, exists := basis["manual_review_profile"]; exists {
		t.Fatal("ordinary intake emitted an unimplemented profile")
	}
}
