package evidencegraph

import (
	"slices"
	"strings"
)

const (
	// CanonicalMissingImplementation asks retrieval to find implementation
	// evidence rather than preserving a guessed filename or service name.
	CanonicalMissingImplementation = "missing_implementation"
	// CanonicalMissingEvidence asks retrieval to find a factual source.
	CanonicalMissingEvidence = "missing_evidence"
	// CanonicalReasoningRequired records that the question needs judgment
	// instead of source collision.
	CanonicalReasoningRequired = "reasoning_required"
)

// CanonicalProposal is the evidence-seeking form of a raw model proposal.
type CanonicalProposal struct {
	ID              string              `json:"id"`
	ProposalType    string              `json:"proposal_type"`
	Entities        []string            `json:"entities"`
	Relations       []CanonicalRelation `json:"relations"`
	MissingEvidence []string            `json:"missing_evidence"`
	RetrievalHints  []string            `json:"retrieval_hints"`
	Confidence      float64             `json:"confidence"`
}

// CanonicalRelation is a relation candidate after answer-shaped guesses are
// removed or normalized.
type CanonicalRelation struct {
	From     string           `json:"from"`
	To       string           `json:"to"`
	Relation EvidenceRelation `json:"relation"`
}

// CanonicalizeProposal converts answer-shaped model output into evidence
// proposals that can safely collide with later external evidence.
func CanonicalizeProposal(proposal LLMProposal) CanonicalProposal {
	text := strings.ToLower(proposal.Query + " " + proposal.Summary + " " + strings.Join(proposal.CandidateNodes, " "))
	for _, relation := range proposal.CandidateRelations {
		text += " " + strings.ToLower(relation.From+" "+relation.To+" "+relation.Reason)
	}

	canonical := CanonicalProposal{
		ID:         proposal.ID + "-canonical",
		Confidence: proposal.Confidence,
	}

	switch {
	case reasoningQuestion(text):
		canonical.ProposalType = CanonicalReasoningRequired
	case containsAnyCanonical(text, "policy owner", "owner", "誰"):
		canonical.ProposalType = CanonicalMissingEvidence
		canonical.MissingEvidence = []string{"policy owner"}
		canonical.RetrievalHints = []string{"policy", "owner"}
		canonical.Entities = ownershipEntities(proposal)
	case containsAnyCanonical(text, "refund"):
		canonical.ProposalType = CanonicalMissingImplementation
		canonical.MissingEvidence = []string{"refund route", "refund handler"}
		canonical.RetrievalHints = []string{"refund"}
	case containsAnyCanonical(text, "membership"):
		canonical.ProposalType = CanonicalMissingImplementation
		canonical.MissingEvidence = []string{"implementation target"}
		canonical.RetrievalHints = []string{"membership"}
	case containsAnyCanonical(text, "rfc", "target service", "目標服務"):
		canonical.ProposalType = CanonicalMissingImplementation
		canonical.MissingEvidence = []string{"implementation target", "target service"}
		canonical.RetrievalHints = []string{"rfc", "target service"}
	case containsAnyCanonical(text, "config", "configuration", "設定"):
		canonical.ProposalType = CanonicalMissingImplementation
		canonical.MissingEvidence = []string{"configuration location"}
		canonical.RetrievalHints = []string{"config"}
	case containsAnyCanonical(text, "implementation", "implements", "module", "service", "controller", ".go", ".py", ".json", ".yaml", ".toml"):
		canonical.ProposalType = CanonicalMissingImplementation
		canonical.MissingEvidence = []string{"implementation target"}
		canonical.RetrievalHints = implementationHints(text)
	default:
		canonical.ProposalType = CanonicalMissingEvidence
		canonical.MissingEvidence = []string{"supporting evidence"}
		canonical.RetrievalHints = fallbackHints(proposal.Query)
	}

	canonical.MissingEvidence = uniqueNonEmpty(canonical.MissingEvidence)
	canonical.RetrievalHints = uniqueNonEmpty(canonical.RetrievalHints)
	canonical.Entities = uniqueNonEmpty(canonical.Entities)
	return canonical
}

func reasoningQuestion(text string) bool {
	return containsAnyCanonical(text, "architecture", "design risk", "scalable", "risk", "好不好", "風險", "為什麼")
}

func ownershipEntities(proposal LLMProposal) []string {
	var entities []string
	for _, node := range proposal.CandidateNodes {
		lower := strings.ToLower(node)
		if strings.Contains(lower, "owner") || strings.Contains(lower, "policy") {
			continue
		}
		entities = append(entities, node)
	}
	summary := proposal.Summary
	for _, marker := range []string{"Department", "DUPD", "Security", "Governance", "applicant"} {
		if strings.Contains(summary, marker) {
			entities = append(entities, summary)
			break
		}
	}
	return entities
}

func implementationHints(text string) []string {
	var hints []string
	for _, hint := range []string{"membership", "refund", "rfc", "config", "implementation", "route", "service"} {
		if strings.Contains(text, hint) {
			hints = append(hints, hint)
		}
	}
	if len(hints) == 0 {
		hints = append(hints, "implementation")
	}
	return hints
}

func fallbackHints(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= '\u4e00' && r <= '\u9fff')
	})
	var hints []string
	for _, field := range fields {
		if len([]rune(field)) >= 2 {
			hints = append(hints, field)
		}
	}
	return hints
}

func containsAnyCanonical(text string, values ...string) bool {
	return slices.ContainsFunc(values, func(value string) bool {
		return strings.Contains(text, strings.ToLower(value))
	})
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}
