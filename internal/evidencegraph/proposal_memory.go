package evidencegraph

// ProposalType describes the kind of speculation stored in proposal memory.
type ProposalType string

const (
	// ProposalFact is an LLM-proposed fact that still needs external evidence.
	ProposalFact ProposalType = "fact"
	// ProposalRelation is an LLM-proposed graph relation.
	ProposalRelation ProposalType = "relation"
	// ProposalImplementation is an LLM-proposed document-to-code mapping.
	ProposalImplementation ProposalType = "implementation"
	// ProposalReference is an LLM-proposed reference target.
	ProposalReference ProposalType = "reference"
	// ProposalScope is an LLM-proposed missing or clarifying scope.
	ProposalScope ProposalType = "scope"
	// ProposalMissingEvidence is an LLM-proposed missing evidence target.
	ProposalMissingEvidence ProposalType = "missing_evidence"
)

// CollisionOutcome describes how later external evidence interacts with a
// stored LLM proposal.
type CollisionOutcome string

const (
	// CollisionNone means no external evidence has appeared yet.
	CollisionNone CollisionOutcome = "none"
	// CollisionSupports means external evidence supports the proposal.
	CollisionSupports CollisionOutcome = "supports"
	// CollisionContradicts means external evidence contradicts the proposal.
	CollisionContradicts CollisionOutcome = "contradicts"
)

// LLMProposal is speculative proposal memory. It is never runtime truth by
// itself; future external evidence must collide with it first.
type LLMProposal struct {
	ID                 string             `json:"id"`
	Query              string             `json:"query"`
	ProposalType       ProposalType       `json:"proposal_type"`
	Summary            string             `json:"summary"`
	CandidateNodes     []string           `json:"candidate_nodes"`
	CandidateRelations []RelationProposal `json:"candidate_relations"`
	SourceModel        string             `json:"source_model"`
	Confidence         float64            `json:"confidence"`
	Status             ProposalStatus     `json:"status"`
	SourceTraceIDs     []string           `json:"source_trace_ids"`
}

// ProposalCollision records future external evidence for one proposal.
type ProposalCollision struct {
	ProposalID          string              `json:"proposal_id"`
	Outcome             CollisionOutcome    `json:"outcome"`
	Evidence            []EvidenceReference `json:"evidence,omitempty"`
	UsedAsRetrievalHint bool                `json:"used_as_retrieval_hint"`
	RuntimeVisible      bool                `json:"runtime_visible"`
}

// ProposalMemoryDecision is the deterministic post-collision state.
type ProposalMemoryDecision struct {
	Proposal                    LLMProposal       `json:"proposal"`
	Collision                   ProposalCollision `json:"collision"`
	Status                      ProposalStatus    `json:"status"`
	Promoted                    bool              `json:"promoted"`
	Rejected                    bool              `json:"rejected"`
	Stale                       bool              `json:"stale"`
	RuntimeLeak                 bool              `json:"runtime_leak"`
	UsedAsRetrievalHint         bool              `json:"used_as_retrieval_hint"`
	VerifiedCreatedFromProposal bool              `json:"verified_created_from_proposal"`
}

// ResolveLLMProposal applies a future evidence collision to proposal memory.
func ResolveLLMProposal(proposal LLMProposal, collision ProposalCollision) ProposalMemoryDecision {
	if proposal.Status == "" {
		proposal.Status = Proposed
	}
	status := Proposed
	switch collision.Outcome {
	case CollisionSupports:
		status = Verified
	case CollisionContradicts:
		status = Rejected
	default:
		status = Proposed
	}
	return ProposalMemoryDecision{
		Proposal:                    proposal,
		Collision:                   collision,
		Status:                      status,
		Promoted:                    status == Verified,
		Rejected:                    status == Rejected,
		Stale:                       status == Proposed,
		RuntimeLeak:                 collision.RuntimeVisible || proposal.Status == Verified,
		UsedAsRetrievalHint:         collision.UsedAsRetrievalHint,
		VerifiedCreatedFromProposal: status == Verified && collision.Outcome == CollisionSupports,
	}
}

// ValidProposalType reports whether proposalType is allowed in proposal memory.
func ValidProposalType(proposalType ProposalType) bool {
	switch proposalType {
	case ProposalFact, ProposalRelation, ProposalImplementation, ProposalReference, ProposalScope, ProposalMissingEvidence:
		return true
	default:
		return false
	}
}

// ValidCollisionOutcome reports whether outcome is a supported collision state.
func ValidCollisionOutcome(outcome CollisionOutcome) bool {
	switch outcome {
	case "", CollisionNone, CollisionSupports, CollisionContradicts:
		return true
	default:
		return false
	}
}
