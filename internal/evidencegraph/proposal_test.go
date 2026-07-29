package evidencegraph

import (
	"path/filepath"
	"testing"
)

func TestProposalValidatorAutoVerifiesReference(t *testing.T) {
	validator := NewProposalValidator(loadProposalSeedGraph(t))
	result := validator.Validate(RelationProposal{
		ID:         "test-reference",
		From:       "annual-leave-reference",
		To:         "appendix-a-annual-leave",
		Relation:   References,
		Evidence:   []EvidenceReference{{Source: "handbook", Quote: "See Appendix A for annual leave."}},
		Confidence: 0.3,
		Reason:     "explicit document reference",
	})

	if result.Proposal.Status != Verified {
		t.Fatalf("Status = %s, want verified", result.Proposal.Status)
	}
	if result.FailureClass != FailureNone {
		t.Fatalf("FailureClass = %s, want none", result.FailureClass)
	}
}

func TestProposalValidatorRejectsMissingEvidence(t *testing.T) {
	validator := NewProposalValidator(loadProposalSeedGraph(t))
	result := validator.Validate(RelationProposal{
		ID:         "test-missing-evidence",
		From:       "annual-leave-reference",
		To:         "appendix-a-annual-leave",
		Relation:   References,
		Confidence: 0.99,
		Reason:     "explicit document reference",
	})

	if result.Proposal.Status != Rejected {
		t.Fatalf("Status = %s, want rejected", result.Proposal.Status)
	}
	if result.FailureClass != FailureProposalWithoutEvidence {
		t.Fatalf("FailureClass = %s, want proposal_without_evidence", result.FailureClass)
	}
}

func TestProposalValidatorRejectsSemanticGuessDespiteConfidence(t *testing.T) {
	validator := NewProposalValidator(loadProposalSeedGraph(t))
	result := validator.Validate(RelationProposal{
		ID:         "test-semantic-guess",
		From:       "doc:rfc-user-service",
		To:         "sym:user.UserService",
		Relation:   Implements,
		Evidence:   []EvidenceReference{{Source: "embedding search", Quote: "High semantic similarity only."}},
		Confidence: 0.99,
		Reason:     "looks related",
	})

	if result.Proposal.Status != Rejected {
		t.Fatalf("Status = %s, want rejected", result.Proposal.Status)
	}
	if result.FailureClass != FailureSemanticGuess {
		t.Fatalf("FailureClass = %s, want semantic_guess", result.FailureClass)
	}
}

func TestProposalValidatorRejectsReferenceWithoutExplicitEvidence(t *testing.T) {
	validator := NewProposalValidator(loadProposalSeedGraph(t))
	result := validator.Validate(RelationProposal{
		ID:         "test-reference-without-explicit-evidence",
		From:       "annual-leave-reference",
		To:         "appendix-a-annual-leave",
		Relation:   References,
		Evidence:   []EvidenceReference{{Source: "handbook", Quote: "Annual leave information page."}},
		Confidence: 0.99,
		Reason:     "candidate relation",
	})

	if result.Proposal.Status != Rejected {
		t.Fatalf("Status = %s, want rejected", result.Proposal.Status)
	}
	if result.FailureClass != FailureSemanticGuess {
		t.Fatalf("FailureClass = %s, want semantic_guess", result.FailureClass)
	}
}

func TestProposalValidatorKeepsImplementsProposedForHumanReview(t *testing.T) {
	validator := NewProposalValidator(loadProposalSeedGraph(t))
	result := validator.Validate(RelationProposal{
		ID:         "test-implements-human-review",
		From:       "doc:rfc-user-service",
		To:         "sym:user.UserService",
		Relation:   Implements,
		Evidence:   []EvidenceReference{{Source: "RFC section", Quote: "Implementation target: UserService."}},
		Confidence: 1,
		Reason:     "source-backed implementation hint",
	})

	if result.Proposal.Status != Proposed {
		t.Fatalf("Status = %s, want proposed", result.Proposal.Status)
	}
	if result.FailureClass != FailureNone {
		t.Fatalf("FailureClass = %s, want none", result.FailureClass)
	}
}

func TestBuildProposalLayersKeepsOnlyVerifiedRuntimeVisible(t *testing.T) {
	graph := loadProposalSeedGraph(t)
	validator := NewProposalValidator(graph)
	results := validator.ValidateAll([]RelationProposal{
		{
			ID:         "test-verified-reference",
			From:       "annual-leave-reference",
			To:         "appendix-a-annual-leave",
			Relation:   References,
			Evidence:   []EvidenceReference{{Source: "handbook", Quote: "See Appendix A."}},
			Confidence: 1,
		},
		{
			ID:         "test-proposed-implements",
			From:       "doc:rfc-user-service",
			To:         "sym:user.UserService",
			Relation:   Implements,
			Evidence:   []EvidenceReference{{Source: "RFC section", Quote: "Implementation target: UserService."}},
			Confidence: 1,
		},
	})
	layers, err := BuildProposalLayers(graph, results)
	if err != nil {
		t.Fatalf("BuildProposalLayers() error = %v", err)
	}

	if got := len(layers.VerifiedGraph.Edges); got != 1 {
		t.Fatalf("VerifiedGraph edges = %d, want 1", got)
	}
	if got := len(layers.ProposedGraph.Proposals); got != 1 {
		t.Fatalf("ProposedGraph proposals = %d, want 1", got)
	}
	if layers.VerifiedGraph.Edges[0].Relation != References {
		t.Fatalf("runtime edge relation = %s, want references", layers.VerifiedGraph.Edges[0].Relation)
	}
}

func loadProposalSeedGraph(t *testing.T) Graph {
	t.Helper()
	graph, err := Load(filepath.Join("testdata", "evidence_graph_seed.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return graph
}
