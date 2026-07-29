package evidencegraph

import "testing"

func TestCanonicalizeImplementationGuess(t *testing.T) {
	proposal := LLMProposal{
		ID:             "p1",
		Query:          "membership implementation 在哪裡？",
		ProposalType:   ProposalImplementation,
		Summary:        "membership implementation may be located in membership_manager.py",
		CandidateNodes: []string{"membership_manager.py"},
		Confidence:     0.3,
	}

	got := CanonicalizeProposal(proposal)
	if got.ProposalType != CanonicalMissingImplementation {
		t.Fatalf("ProposalType = %q, want %q", got.ProposalType, CanonicalMissingImplementation)
	}
	if len(got.Entities) != 0 {
		t.Fatalf("Entities = %v, want filename speculation removed", got.Entities)
	}
	assertContains(t, got.MissingEvidence, "implementation target")
	assertContains(t, got.RetrievalHints, "membership")
}

func TestCanonicalizeRouteGuess(t *testing.T) {
	proposal := LLMProposal{
		ID:             "p2",
		Query:          "refund route 在哪裡？",
		ProposalType:   ProposalImplementation,
		Summary:        "RefundController may implement refund handling",
		CandidateNodes: []string{"RefundController"},
		Confidence:     0.3,
	}

	got := CanonicalizeProposal(proposal)
	assertContains(t, got.MissingEvidence, "refund route")
	assertContains(t, got.MissingEvidence, "refund handler")
	assertContains(t, got.RetrievalHints, "refund")
}

func TestCanonicalizeUnknownOwnership(t *testing.T) {
	proposal := LLMProposal{
		ID:             "p3",
		Query:          "policy owner 是誰？",
		ProposalType:   ProposalFact,
		Summary:        "The policy owner is likely Department of Policy Management",
		CandidateNodes: []string{"Policy Owner Role", "Department of Policy Management"},
		Confidence:     0.25,
	}

	got := CanonicalizeProposal(proposal)
	if got.ProposalType != CanonicalMissingEvidence {
		t.Fatalf("ProposalType = %q, want %q", got.ProposalType, CanonicalMissingEvidence)
	}
	assertContains(t, got.MissingEvidence, "policy owner")
	assertContains(t, got.RetrievalHints, "policy")
}

func TestCanonicalizeReasoningQuestion(t *testing.T) {
	proposal := LLMProposal{
		ID:           "p4",
		Query:        "這個 architecture 好不好？",
		ProposalType: ProposalImplementation,
		Summary:      "Architecture is scalable",
		Confidence:   0.25,
	}

	got := CanonicalizeProposal(proposal)
	if got.ProposalType != CanonicalReasoningRequired {
		t.Fatalf("ProposalType = %q, want %q", got.ProposalType, CanonicalReasoningRequired)
	}
	if len(got.MissingEvidence) != 0 {
		t.Fatalf("MissingEvidence = %v, want none for reasoning-only proposal", got.MissingEvidence)
	}
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%v does not contain %q", values, want)
}
