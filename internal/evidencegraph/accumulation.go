package evidencegraph

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// CandidateType describes the review queue bucket for accumulated evidence.
type CandidateType string

const (
	CandidateEvidenceItem        CandidateType = "evidence_item"
	CandidateRelationProposal    CandidateType = "relation_proposal"
	CandidatePolicyProposal      CandidateType = "policy_proposal"
	CandidateRejectedPattern     CandidateType = "rejected_pattern"
	CandidateSourceDeterministic               = "deterministic"
	CandidateSourceLocalModel                  = "local_model"
	CandidateSourceHuman                       = "human"
)

// EvidenceTraceEvent is a policy-gate trace captured after runtime decisions.
// It is diagnostic input only and must not mutate the verified runtime graph.
type EvidenceTraceEvent struct {
	ID                     string    `json:"id"`
	Time                   time.Time `json:"time"`
	Query                  string    `json:"query"`
	Decision               string    `json:"decision"`
	RetrievedNodeIDs       []string  `json:"retrieved_node_ids"`
	EvidenceStatus         string    `json:"evidence_status"`
	PartialReason          string    `json:"partial_reason"`
	RequiresReasoning      bool      `json:"requires_reasoning"`
	AppliedPolicyRules     []string  `json:"applied_policy_rules"`
	GraphEdgesUsed         []string  `json:"graph_edges_used"`
	MissingReferences      []string  `json:"missing_references"`
	MissingImplementations []string  `json:"missing_implementations"`
	MissingScopes          []string  `json:"missing_scopes"`
	Conflicts              []string  `json:"conflicts"`
	AnswerCandidate        string    `json:"answer_candidate"`
	Source                 string    `json:"source"`
}

// AccumulatedEvidenceCandidate is a non-runtime candidate derived from traces.
type AccumulatedEvidenceCandidate struct {
	ID             string              `json:"id"`
	CandidateType  CandidateType       `json:"candidate_type"`
	SourceTraceIDs []string            `json:"source_trace_ids"`
	Summary        string              `json:"summary"`
	RelatedNodeIDs []string            `json:"related_node_ids"`
	RelatedRuleIDs []string            `json:"related_rule_ids"`
	Status         ProposalStatus      `json:"status"`
	CreatedBy      string              `json:"created_by"`
	Evidence       []EvidenceReference `json:"evidence"`
}

// ReviewPriority stores the deterministic ranking score for a candidate.
type ReviewPriority struct {
	CandidateID    string   `json:"candidate_id"`
	ImpactScore    float64  `json:"impact_score"`
	FrequencyScore float64  `json:"frequency_score"`
	RiskScore      float64  `json:"risk_score"`
	TotalScore     float64  `json:"total_score"`
	Reasons        []string `json:"reasons"`
}

// ReviewQueueItem is the review-facing scored candidate shape.
type ReviewQueueItem struct {
	CandidateID     string              `json:"candidate_id"`
	CandidateType   CandidateType       `json:"candidate_type"`
	TotalScore      float64             `json:"total_score"`
	ImpactScore     float64             `json:"impact_score"`
	FrequencyScore  float64             `json:"frequency_score"`
	RiskScore       float64             `json:"risk_score"`
	Summary         string              `json:"summary"`
	SuggestedAction string              `json:"suggested_action"`
	Evidence        []EvidenceReference `json:"evidence"`
}

// AccumulateTraceCandidates groups gate traces into review-only candidates.
func AccumulateTraceCandidates(events []EvidenceTraceEvent) []AccumulatedEvidenceCandidate {
	builders := map[string]*candidateBuilder{}
	for _, event := range events {
		handled := false
		for _, target := range event.MissingReferences {
			key := "missing_reference:" + target
			builder := ensureCandidate(builders, key, CandidateRelationProposal, Proposed, CandidateSourceDeterministic, fmt.Sprintf("missing_reference repair candidate for %s", target))
			builder.addTrace(event, []string{target})
			handled = true
		}
		for _, target := range event.MissingImplementations {
			key := "missing_implementation:" + target
			builder := ensureCandidate(builders, key, CandidateRelationProposal, Proposed, CandidateSourceDeterministic, fmt.Sprintf("missing_implementation relation candidate for %s", target))
			builder.addTrace(event, []string{target})
			handled = true
		}
		for _, conflict := range event.Conflicts {
			key := "conflict:" + conflict
			builder := ensureCandidate(builders, key, CandidateRelationProposal, Proposed, CandidateSourceDeterministic, fmt.Sprintf("conflict relation candidate for %s", conflict))
			builder.addTrace(event, []string{conflict})
			handled = true
		}
		if traceHasRule(event, "EPL-002") || traceHasRule(event, "EPL-002G") {
			key := "template_caveat:" + firstOr(event.RetrievedNodeIDs, "unknown-template")
			builder := ensureCandidate(builders, key, CandidatePolicyProposal, Proposed, CandidateSourceDeterministic, "template caveat requirement repeated by policy traces")
			builder.addTrace(event, nil)
			handled = true
		}
		if event.RequiresReasoning || traceHasRule(event, "EPL-001") {
			builder := ensureCandidate(builders, "reasoning_escalation", CandidatePolicyProposal, Proposed, CandidateSourceLocalModel, "direct_return/escalate boundary policy rule candidate from reasoning escalations")
			builder.addTrace(event, nil)
			handled = true
		}
		if traceHasRule(event, FailureSemanticGuess) {
			builder := ensureCandidate(builders, FailureSemanticGuess, CandidateRejectedPattern, Rejected, CandidateSourceDeterministic, "semantic_guess rejected pattern repeated by proposal validator traces")
			builder.addTrace(event, nil)
			handled = true
		}
		if traceHasRule(event, FailureGraphSelfReference) {
			builder := ensureCandidate(builders, FailureGraphSelfReference, CandidateRejectedPattern, Rejected, CandidateSourceDeterministic, "graph_self_reference rejected pattern repeated by proposal validator traces")
			builder.addTrace(event, nil)
			handled = true
		}
		if !handled && lowRiskTrace(event) {
			key := "low_value:" + event.ID
			builder := ensureCandidate(builders, key, CandidateEvidenceItem, Proposed, CandidateSourceDeterministic, "low-risk documentation lookup evidence item")
			builder.addTrace(event, nil)
		}
	}
	candidates := make([]AccumulatedEvidenceCandidate, 0, len(builders))
	for _, builder := range builders {
		candidates = append(candidates, builder.candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ID < candidates[j].ID
	})
	return candidates
}

// ScoreReviewPriority scores a candidate with the Phase 12 priority formula.
func ScoreReviewPriority(candidate AccumulatedEvidenceCandidate) ReviewPriority {
	impact, impactReason := impactScore(candidate)
	risk, riskReason := riskScore(candidate)
	frequency, frequencyReason := frequencyScore(len(candidate.SourceTraceIDs))
	return ReviewPriority{
		CandidateID:    candidate.ID,
		ImpactScore:    impact,
		FrequencyScore: frequency,
		RiskScore:      risk,
		TotalScore:     0.45*impact + 0.30*risk + 0.25*frequency,
		Reasons:        []string{impactReason, riskReason, frequencyReason},
	}
}

// BuildReviewQueue creates a stable, descending-priority review queue.
func BuildReviewQueue(candidates []AccumulatedEvidenceCandidate) []ReviewQueueItem {
	queue := make([]ReviewQueueItem, 0, len(candidates))
	for _, candidate := range candidates {
		priority := ScoreReviewPriority(candidate)
		queue = append(queue, ReviewQueueItem{
			CandidateID:     candidate.ID,
			CandidateType:   candidate.CandidateType,
			TotalScore:      priority.TotalScore,
			ImpactScore:     priority.ImpactScore,
			FrequencyScore:  priority.FrequencyScore,
			RiskScore:       priority.RiskScore,
			Summary:         candidate.Summary,
			SuggestedAction: suggestedAction(candidate, priority),
			Evidence:        candidate.Evidence,
		})
	}
	sort.Slice(queue, func(i, j int) bool {
		if queue[i].TotalScore != queue[j].TotalScore {
			return queue[i].TotalScore > queue[j].TotalScore
		}
		return queue[i].CandidateID < queue[j].CandidateID
	})
	return queue
}

type candidateBuilder struct {
	candidate AccumulatedEvidenceCandidate
	seen      map[string]bool
}

func ensureCandidate(builders map[string]*candidateBuilder, key string, typ CandidateType, status ProposalStatus, createdBy, summary string) *candidateBuilder {
	id := "AC-" + sanitizeID(key)
	if builder, ok := builders[id]; ok {
		return builder
	}
	builder := &candidateBuilder{
		candidate: AccumulatedEvidenceCandidate{
			ID:            id,
			CandidateType: typ,
			Summary:       summary,
			Status:        status,
			CreatedBy:     createdBy,
		},
		seen: map[string]bool{},
	}
	builders[id] = builder
	return builder
}

func (b *candidateBuilder) addTrace(event EvidenceTraceEvent, related []string) {
	if event.ID != "" && !b.seen["trace:"+event.ID] {
		b.candidate.SourceTraceIDs = append(b.candidate.SourceTraceIDs, event.ID)
		b.seen["trace:"+event.ID] = true
	}
	for _, id := range event.RetrievedNodeIDs {
		b.addRelatedNode(id)
	}
	for _, id := range related {
		b.addRelatedNode(id)
	}
	for _, rule := range event.AppliedPolicyRules {
		b.addRelatedRule(rule)
	}
	b.addEvidence(event)
}

func (b *candidateBuilder) addRelatedNode(id string) {
	if id == "" || b.seen["node:"+id] {
		return
	}
	b.candidate.RelatedNodeIDs = append(b.candidate.RelatedNodeIDs, id)
	b.seen["node:"+id] = true
}

func (b *candidateBuilder) addRelatedRule(id string) {
	if id == "" || b.seen["rule:"+id] {
		return
	}
	b.candidate.RelatedRuleIDs = append(b.candidate.RelatedRuleIDs, id)
	b.seen["rule:"+id] = true
}

func (b *candidateBuilder) addEvidence(event EvidenceTraceEvent) {
	if event.ID == "" || b.seen["evidence:"+event.ID] {
		return
	}
	source := event.Source
	if source == "" {
		source = "trace:" + event.ID
	}
	quote := event.AnswerCandidate
	if quote == "" {
		quote = event.Query
	}
	if quote == "" {
		quote = event.ID
	}
	b.candidate.Evidence = append(b.candidate.Evidence, EvidenceReference{Source: source, Quote: quote})
	b.seen["evidence:"+event.ID] = true
}

func impactScore(candidate AccumulatedEvidenceCandidate) (float64, string) {
	text := candidateText(candidate)
	switch {
	case containsAny(text, "direct_return", "direct return", "escalate boundary", "policy rule", "conflict", "conflicts_with", "supersedes", "runtime edge", "runtime-visible"):
		return 1.0, "impact: affects direct_return/escalate boundary, policy rule, or runtime-critical edge"
	case containsAny(text, "missing_reference", "missing reference", "missing_implementation", "missing implementation", "caveat requirement"):
		return 0.7, "impact: repairs missing reference, missing implementation, or caveat requirement"
	case candidate.CandidateType == CandidateEvidenceItem || containsAny(text, "answer candidate", "summary"):
		return 0.4, "impact: answer candidate or summary only"
	default:
		return 0.1, "impact: diagnostic only"
	}
}

func riskScore(candidate AccumulatedEvidenceCandidate) (float64, string) {
	text := candidateText(candidate)
	switch {
	case containsAny(text, "legal", "compliance", "security", "financial", "finance", "conflict", "direct-return", "direct_return", "direct return enabler"):
		return 1.0, "risk: legal, compliance, security, financial, conflict, or direct-return enabler"
	case containsAny(text, "implementation", "implements", "supersedes", "missing_reference repair", "missing reference repair"):
		return 0.7, "risk: implementation, supersession, or missing-reference repair"
	case containsAny(text, "caveat", "template", "source-status", "source status"):
		return 0.4, "risk: caveat, template, or source-status correction"
	default:
		return 0.1, "risk: low-risk document lookup"
	}
}

func frequencyScore(count int) (float64, string) {
	switch {
	case count >= 10:
		return 1.0, "frequency: seen at least 10 times"
	case count >= 5:
		return 0.7, "frequency: seen 5-9 times"
	case count >= 2:
		return 0.4, "frequency: seen 2-4 times"
	case count == 1:
		return 0.1, "frequency: seen once"
	default:
		return 0.0, "frequency: no source traces"
	}
}

func suggestedAction(candidate AccumulatedEvidenceCandidate, priority ReviewPriority) string {
	switch candidate.CandidateType {
	case CandidateRelationProposal:
		if priority.RiskScore >= 0.7 || priority.ImpactScore >= 0.7 {
			return "verify_relation"
		}
		return "needs_more_evidence"
	case CandidatePolicyProposal:
		if priority.ImpactScore >= 0.7 {
			return "add_policy_rule"
		}
		return "needs_more_evidence"
	case CandidateRejectedPattern:
		if priority.RiskScore >= 0.7 || priority.FrequencyScore >= 0.4 {
			return "reject_relation"
		}
		return "ignore"
	case CandidateEvidenceItem:
		if priority.TotalScore < 0.4 {
			return "ignore"
		}
		return "needs_more_evidence"
	default:
		return "needs_more_evidence"
	}
}

func candidateText(candidate AccumulatedEvidenceCandidate) string {
	parts := []string{candidate.ID, string(candidate.CandidateType), candidate.Summary, candidate.CreatedBy}
	parts = append(parts, candidate.RelatedNodeIDs...)
	parts = append(parts, candidate.RelatedRuleIDs...)
	for _, evidence := range candidate.Evidence {
		parts = append(parts, evidence.Source, evidence.Quote)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func traceHasRule(event EvidenceTraceEvent, want string) bool {
	want = strings.ToLower(want)
	for _, rule := range event.AppliedPolicyRules {
		if strings.Contains(strings.ToLower(rule), want) {
			return true
		}
	}
	return false
}

func lowRiskTrace(event EvidenceTraceEvent) bool {
	return event.EvidenceStatus == "sufficient" && event.Decision == "direct_return" && len(event.Conflicts) == 0
}

func firstOr(values []string, fallback string) string {
	if len(values) == 0 || values[0] == "" {
		return fallback
	}
	return values[0]
}

func containsAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func sanitizeID(value string) string {
	value = strings.ToLower(value)
	var out strings.Builder
	previousDash := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(r)
			previousDash = false
		default:
			if !previousDash {
				out.WriteByte('-')
				previousDash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}
