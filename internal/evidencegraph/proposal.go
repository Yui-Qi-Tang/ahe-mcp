package evidencegraph

import (
	"fmt"
	"slices"
	"strings"
)

// ProposalStatus describes the review state of a proposed graph relation.
type ProposalStatus string

const (
	// Proposed means evidence exists but human review is still required.
	Proposed ProposalStatus = "proposed"
	// Verified means a proposal may enter the runtime-visible graph.
	Verified ProposalStatus = "verified"
	// Rejected means a proposal must not enter any runtime graph.
	Rejected ProposalStatus = "rejected"
)

const (
	// FailureNone means proposal validation matched the expected workflow.
	FailureNone = "none"
	// FailureProposalWithoutEvidence means the proposal lacked source-backed evidence.
	FailureProposalWithoutEvidence = "proposal_without_evidence"
	// FailureInvalidRelation means the relation or endpoints were invalid.
	FailureInvalidRelation = "invalid_relation"
	// FailureSemanticGuess means the proposal was based on similarity or intuition.
	FailureSemanticGuess = "semantic_guess"
	// FailureGraphSelfReference means the graph tried to cite itself as evidence.
	FailureGraphSelfReference = "graph_self_reference"
	// FailureRuntimeLeak means a non-verified proposal became runtime-visible.
	FailureRuntimeLeak = "runtime_leak"
	// FailureFalseVerification means a proposal was verified against human expectation.
	FailureFalseVerification = "false_verification"
	// FailureFalseRejection means a proposal was rejected against human expectation.
	FailureFalseRejection = "false_rejection"
)

// EvidenceReference is the source quote backing a relation proposal.
type EvidenceReference struct {
	Source string `json:"source"`
	Quote  string `json:"quote"`
}

// RelationProposal is a candidate graph edge that is not runtime-visible until
// validation marks it verified.
type RelationProposal struct {
	ID         string              `json:"id"`
	From       string              `json:"from"`
	To         string              `json:"to"`
	Relation   EvidenceRelation    `json:"relation"`
	Evidence   []EvidenceReference `json:"evidence"`
	Confidence float64             `json:"confidence"`
	Reason     string              `json:"reason"`
	Status     ProposalStatus      `json:"status"`
}

// ProposalGraph stores non-runtime-visible proposals.
type ProposalGraph struct {
	SchemaVersion string             `json:"schema_version"`
	Proposals     []RelationProposal `json:"proposals"`
}

// ProposalLayers separates runtime-visible verified edges from invisible
// proposals and rejected candidates.
type ProposalLayers struct {
	VerifiedGraph Graph              `json:"verified_graph"`
	ProposedGraph ProposalGraph      `json:"proposed_graph"`
	Rejected      []RelationProposal `json:"rejected"`
}

// ProposalValidationResult records one validator decision.
type ProposalValidationResult struct {
	Proposal     RelationProposal `json:"proposal"`
	FailureClass string           `json:"failure_class"`
	Reasons      []string         `json:"reasons,omitempty"`
}

// ProposalValidator validates relation proposals against an existing graph.
type ProposalValidator struct {
	graph Graph
	nodes map[string]bool
}

// NewProposalValidator constructs a proposal validator for graph.
func NewProposalValidator(graph Graph) ProposalValidator {
	nodes := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes[node.ID] = true
	}
	return ProposalValidator{graph: graph, nodes: nodes}
}

// Validate checks a proposal and assigns proposed, verified, or rejected.
func (v ProposalValidator) Validate(proposal RelationProposal) ProposalValidationResult {
	proposal.Status = Proposed
	switch failure, reason := v.rejectionReason(proposal); failure {
	case FailureNone:
	case FailureInvalidRelation, FailureProposalWithoutEvidence, FailureSemanticGuess, FailureGraphSelfReference:
		proposal.Status = Rejected
		return ProposalValidationResult{Proposal: proposal, FailureClass: failure, Reasons: []string{reason}}
	default:
		proposal.Status = Rejected
		return ProposalValidationResult{Proposal: proposal, FailureClass: FailureInvalidRelation, Reasons: []string{reason}}
	}

	switch proposal.Relation {
	case References:
		if explicitReferenceEvidence(proposal) {
			proposal.Status = Verified
			return ProposalValidationResult{Proposal: proposal, FailureClass: FailureNone}
		}
		proposal.Status = Rejected
		return ProposalValidationResult{Proposal: proposal, FailureClass: FailureSemanticGuess, Reasons: []string{"reference relation lacks explicit reference wording"}}
	case Supersedes:
		if explicitSupersessionEvidence(proposal) {
			proposal.Status = Verified
			return ProposalValidationResult{Proposal: proposal, FailureClass: FailureNone}
		}
		proposal.Status = Rejected
		return ProposalValidationResult{Proposal: proposal, FailureClass: FailureSemanticGuess, Reasons: []string{"supersedes relation lacks explicit current/previous wording"}}
	case Implements, Supports, ConflictsWith, Requires:
		return ProposalValidationResult{Proposal: proposal, FailureClass: FailureNone, Reasons: []string{"human review required"}}
	default:
		proposal.Status = Rejected
		return ProposalValidationResult{Proposal: proposal, FailureClass: FailureInvalidRelation, Reasons: []string{"relation is not supported"}}
	}
}

// ValidateAll validates proposals in order.
func (v ProposalValidator) ValidateAll(proposals []RelationProposal) []ProposalValidationResult {
	results := make([]ProposalValidationResult, 0, len(proposals))
	for _, proposal := range proposals {
		results = append(results, v.Validate(proposal))
	}
	return results
}

// BuildProposalLayers builds separate verified, proposed, and rejected stores.
func BuildProposalLayers(base Graph, results []ProposalValidationResult) (ProposalLayers, error) {
	layers := ProposalLayers{
		VerifiedGraph: Graph{
			SchemaVersion: "evidence-graph-verified-proposals/v1",
			Nodes:         slices.Clone(base.Nodes),
		},
		ProposedGraph: ProposalGraph{SchemaVersion: "evidence-graph-proposals/v1"},
	}
	for _, result := range results {
		proposal := result.Proposal
		switch proposal.Status {
		case Verified:
			layers.VerifiedGraph.Edges = append(layers.VerifiedGraph.Edges, EvidenceEdge{
				From:       proposal.From,
				To:         proposal.To,
				Relation:   proposal.Relation,
				Source:     "proposal:" + proposal.ID,
				Confidence: proposal.Confidence,
			})
		case Proposed:
			layers.ProposedGraph.Proposals = append(layers.ProposedGraph.Proposals, proposal)
		case Rejected:
			layers.Rejected = append(layers.Rejected, proposal)
		default:
			return ProposalLayers{}, fmt.Errorf("proposal %q has invalid status %q", proposal.ID, proposal.Status)
		}
	}
	if err := layers.VerifiedGraph.Validate(); err != nil {
		return ProposalLayers{}, fmt.Errorf("validating verified proposal graph: %w", err)
	}
	return layers, nil
}

func (v ProposalValidator) rejectionReason(proposal RelationProposal) (string, string) {
	if strings.TrimSpace(proposal.ID) == "" {
		return FailureInvalidRelation, "id is required"
	}
	if strings.TrimSpace(proposal.From) == "" || strings.TrimSpace(proposal.To) == "" {
		return FailureInvalidRelation, "from and to are required"
	}
	if proposal.From == proposal.To {
		return FailureGraphSelfReference, "proposal from and to are the same node"
	}
	if !ValidRelation(proposal.Relation) {
		return FailureInvalidRelation, "relation is not allowed"
	}
	if !v.nodes[proposal.From] || !v.nodes[proposal.To] {
		return FailureInvalidRelation, "proposal endpoints must exist in the verified graph"
	}
	if proposal.Confidence < 0 || proposal.Confidence > 1 {
		return FailureInvalidRelation, "confidence must be between 0 and 1"
	}
	if proposalEvidenceMissing(proposal) {
		return FailureProposalWithoutEvidence, "proposal must include source-backed evidence"
	}
	if graphSelfReference(proposal) {
		return FailureGraphSelfReference, "proposal cites the evidence graph instead of source evidence"
	}
	if semanticGuess(proposal) {
		return FailureSemanticGuess, "proposal is based on similarity, proximity, or intuition"
	}
	return FailureNone, ""
}

func proposalEvidenceMissing(proposal RelationProposal) bool {
	if len(proposal.Evidence) == 0 {
		return true
	}
	for _, evidence := range proposal.Evidence {
		if strings.TrimSpace(evidence.Source) == "" || strings.TrimSpace(evidence.Quote) == "" {
			return true
		}
	}
	return false
}

func graphSelfReference(proposal RelationProposal) bool {
	text := proposalText(proposal)
	return strings.Contains(text, "evidence graph") ||
		strings.Contains(text, "verified graph") ||
		strings.Contains(text, "proposed graph") ||
		strings.Contains(text, "runtime graph") ||
		strings.Contains(text, "graph already says")
}

func semanticGuess(proposal RelationProposal) bool {
	text := proposalText(proposal)
	for _, term := range []string{
		"looks related",
		"semantic similarity",
		"similarity",
		"embedding",
		"proximity",
		"intuition",
		"chain-of-thought",
		"chain of thought",
		"guess",
		"probably related",
	} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func explicitReferenceEvidence(proposal RelationProposal) bool {
	text := proposalEvidenceText(proposal)
	for _, term := range []string{
		"see ",
		"refer to",
		"references",
		"appendix",
		"section",
		"table",
		"policy links",
	} {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func explicitSupersessionEvidence(proposal RelationProposal) bool {
	text := proposalEvidenceText(proposal)
	current := strings.Contains(text, "current") || strings.Contains(text, "active") || strings.Contains(text, "v2")
	previous := strings.Contains(text, "previous") ||
		strings.Contains(text, "archived") ||
		strings.Contains(text, "stale") ||
		strings.Contains(text, "v1") ||
		strings.Contains(text, "replaced")
	return strings.Contains(text, "supersedes") || current && previous
}

func proposalText(proposal RelationProposal) string {
	parts := []string{proposal.ID, proposal.From, proposal.To, string(proposal.Relation), proposal.Reason}
	for _, evidence := range proposal.Evidence {
		parts = append(parts, evidence.Source, evidence.Quote)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func proposalEvidenceText(proposal RelationProposal) string {
	var parts []string
	for _, evidence := range proposal.Evidence {
		parts = append(parts, evidence.Source, evidence.Quote)
	}
	return strings.ToLower(strings.Join(parts, " "))
}
