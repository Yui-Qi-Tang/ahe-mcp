package evidencegraph

import "testing"

func TestResolveLLMProposalPromotesOnlyAfterSupportingCollision(t *testing.T) {
	result := ResolveLLMProposal(LLMProposal{
		ID:           "P13-001",
		Query:        "Where is membership policy implemented?",
		ProposalType: ProposalImplementation,
		Summary:      "membership-policy likely maps to UserService",
		SourceModel:  "gemma4:12b-mock",
		Confidence:   0.74,
		Status:       Proposed,
	}, ProposalCollision{
		ProposalID: "P13-001",
		Outcome:    CollisionSupports,
		Evidence:   []EvidenceReference{{Source: "RFC", Quote: "Implementation Target: UserService."}},
	})

	if result.Status != Verified {
		t.Fatalf("Status = %s, want verified", result.Status)
	}
	if !result.VerifiedCreatedFromProposal {
		t.Fatal("VerifiedCreatedFromProposal = false, want true")
	}
	if result.RuntimeLeak {
		t.Fatal("RuntimeLeak = true, want false")
	}
}

func TestResolveLLMProposalRejectsContradictingCollision(t *testing.T) {
	result := ResolveLLMProposal(LLMProposal{
		ID:           "P13-002",
		Query:        "Where is membership policy implemented?",
		ProposalType: ProposalImplementation,
		Summary:      "membership-policy likely maps to UserService",
		SourceModel:  "gemma4:12b-mock",
		Confidence:   0.74,
		Status:       Proposed,
	}, ProposalCollision{
		ProposalID: "P13-002",
		Outcome:    CollisionContradicts,
		Evidence:   []EvidenceReference{{Source: "RFC", Quote: "Implementation Target: MembershipEngine."}},
	})

	if result.Status != Rejected {
		t.Fatalf("Status = %s, want rejected", result.Status)
	}
	if !result.Rejected {
		t.Fatal("Rejected = false, want true")
	}
	if result.VerifiedCreatedFromProposal {
		t.Fatal("VerifiedCreatedFromProposal = true, want false")
	}
}

func TestResolveLLMProposalWithoutCollisionStaysProposed(t *testing.T) {
	result := ResolveLLMProposal(LLMProposal{
		ID:           "P13-003",
		Query:        "Does finance reimbursement exist?",
		ProposalType: ProposalMissingEvidence,
		Summary:      "There should be a Finance reimbursement role.",
		SourceModel:  "gemma4:12b-mock",
		Confidence:   0.52,
		Status:       Proposed,
	}, ProposalCollision{ProposalID: "P13-003", Outcome: CollisionNone})

	if result.Status != Proposed {
		t.Fatalf("Status = %s, want proposed", result.Status)
	}
	if !result.Stale {
		t.Fatal("Stale = false, want true")
	}
	if result.RuntimeLeak {
		t.Fatal("RuntimeLeak = true, want false")
	}
}

func TestResolveLLMProposalFlagsVerifiedInputAsRuntimeLeak(t *testing.T) {
	result := ResolveLLMProposal(LLMProposal{
		ID:           "P13-004",
		Query:        "Can LLM output become truth?",
		ProposalType: ProposalFact,
		Summary:      "unsafe pre-verified proposal",
		SourceModel:  "gemma4:12b-mock",
		Confidence:   0.99,
		Status:       Verified,
	}, ProposalCollision{ProposalID: "P13-004", Outcome: CollisionNone})

	if !result.RuntimeLeak {
		t.Fatal("RuntimeLeak = false, want true")
	}
	if result.Status != Proposed {
		t.Fatalf("Status = %s, want proposed after no collision", result.Status)
	}
}
