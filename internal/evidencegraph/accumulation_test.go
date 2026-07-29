package evidencegraph

import (
	"testing"
	"time"
)

func TestAccumulateTraceCandidatesGroupsMissingEvidence(t *testing.T) {
	events := []EvidenceTraceEvent{
		{
			ID:                 "T1",
			Time:               time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
			Query:              "正式員工特休幾天？",
			Decision:           "escalate",
			RetrievedNodeIDs:   []string{"annual-leave-reference"},
			EvidenceStatus:     "partial",
			PartialReason:      "missing_reference",
			AppliedPolicyRules: []string{"EPL-005"},
			MissingReferences:  []string{"appendix-a-annual-leave"},
			Source:             "phase12-trace",
		},
		{
			ID:                 "T2",
			Time:               time.Date(2026, 6, 22, 0, 1, 0, 0, time.UTC),
			Query:              "特休細節在哪？",
			Decision:           "escalate",
			RetrievedNodeIDs:   []string{"annual-leave-reference"},
			EvidenceStatus:     "partial",
			PartialReason:      "missing_reference",
			AppliedPolicyRules: []string{"EPL-005"},
			MissingReferences:  []string{"appendix-a-annual-leave"},
			Source:             "phase12-trace",
		},
		{
			ID:                     "T3",
			Time:                   time.Date(2026, 6, 22, 0, 2, 0, 0, time.UTC),
			Query:                  "GDPR 刪除在哪裡實作？",
			Decision:               "escalate",
			RetrievedNodeIDs:       []string{"doc:gdpr-delete-policy"},
			EvidenceStatus:         "partial",
			PartialReason:          "missing_implementation",
			AppliedPolicyRules:     []string{"EPL-IMP-001"},
			MissingImplementations: []string{"sym:user.UserService"},
			Source:                 "phase12-trace",
		},
	}

	candidates := AccumulateTraceCandidates(events)
	if got, want := len(candidates), 2; got != want {
		t.Fatalf("len(candidates) = %d, want %d", got, want)
	}
	assertCandidate(t, candidates, "AC-missing-reference-appendix-a-annual-leave", CandidateRelationProposal, Proposed, 2)
	assertCandidate(t, candidates, "AC-missing-implementation-sym-user-userservice", CandidateRelationProposal, Proposed, 1)
}

func TestAccumulateTraceCandidatesKeepsRejectedPatternsOutOfRuntime(t *testing.T) {
	events := []EvidenceTraceEvent{
		{
			ID:                 "T1",
			Time:               time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
			Query:              "這兩個文件看起來相關嗎？",
			Decision:           "escalate",
			EvidenceStatus:     "partial",
			AppliedPolicyRules: []string{FailureSemanticGuess},
			Source:             "phase12-trace",
		},
		{
			ID:                 "T2",
			Time:               time.Date(2026, 6, 22, 0, 1, 0, 0, time.UTC),
			Query:              "graph 已經說這條邊存在嗎？",
			Decision:           "escalate",
			EvidenceStatus:     "partial",
			AppliedPolicyRules: []string{FailureGraphSelfReference},
			Source:             "phase12-trace",
		},
		{
			ID:                "T3",
			Time:              time.Date(2026, 6, 22, 0, 2, 0, 0, time.UTC),
			Query:             "請推理付款流程。",
			Decision:          "escalate",
			EvidenceStatus:    "partial",
			RequiresReasoning: true,
			Source:            "phase12-trace",
		},
	}

	candidates := AccumulateTraceCandidates(events)
	assertCandidate(t, candidates, "AC-semantic-guess", CandidateRejectedPattern, Rejected, 1)
	assertCandidate(t, candidates, "AC-graph-self-reference", CandidateRejectedPattern, Rejected, 1)
	local := findCandidate(candidates, "AC-reasoning-escalation")
	if local.CreatedBy != CandidateSourceLocalModel {
		t.Fatalf("CreatedBy = %q, want local_model", local.CreatedBy)
	}
	if local.Status == Verified {
		t.Fatal("local_model candidate must not become verified")
	}
}

func TestReviewPriorityUsesPhase12Formula(t *testing.T) {
	candidate := AccumulatedEvidenceCandidate{
		ID:             "AC-high",
		CandidateType:  CandidateRelationProposal,
		SourceTraceIDs: []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8", "T9", "T10"},
		Summary:        "financial conflict relation candidate used by runtime edge",
		Status:         Proposed,
		CreatedBy:      CandidateSourceDeterministic,
	}
	priority := ScoreReviewPriority(candidate)

	if priority.ImpactScore != 1.0 {
		t.Fatalf("ImpactScore = %.2f, want 1.0", priority.ImpactScore)
	}
	if priority.RiskScore != 1.0 {
		t.Fatalf("RiskScore = %.2f, want 1.0", priority.RiskScore)
	}
	if priority.FrequencyScore != 1.0 {
		t.Fatalf("FrequencyScore = %.2f, want 1.0", priority.FrequencyScore)
	}
	if priority.TotalScore != 1.0 {
		t.Fatalf("TotalScore = %.2f, want 1.0", priority.TotalScore)
	}
}

func TestBuildReviewQueueSuppressesLowValueItems(t *testing.T) {
	queue := BuildReviewQueue([]AccumulatedEvidenceCandidate{
		{
			ID:             "AC-low",
			CandidateType:  CandidateEvidenceItem,
			SourceTraceIDs: []string{"T1"},
			Summary:        "low-risk documentation lookup evidence item",
			Status:         Proposed,
			CreatedBy:      CandidateSourceDeterministic,
		},
		{
			ID:             "AC-high",
			CandidateType:  CandidatePolicyProposal,
			SourceTraceIDs: []string{"T1", "T2", "T3", "T4", "T5"},
			Summary:        "direct_return/escalate boundary policy rule candidate from reasoning escalations",
			Status:         Proposed,
			CreatedBy:      CandidateSourceLocalModel,
		},
	})

	if got := queue[0].CandidateID; got != "AC-high" {
		t.Fatalf("top candidate = %s, want AC-high", got)
	}
	if got := queue[1].SuggestedAction; got != "ignore" {
		t.Fatalf("low value SuggestedAction = %s, want ignore", got)
	}
}

func assertCandidate(t *testing.T, candidates []AccumulatedEvidenceCandidate, id string, typ CandidateType, status ProposalStatus, traces int) {
	t.Helper()
	candidate := findCandidate(candidates, id)
	if candidate.ID == "" {
		t.Fatalf("candidate %q not found", id)
	}
	if candidate.CandidateType != typ {
		t.Fatalf("%s CandidateType = %s, want %s", id, candidate.CandidateType, typ)
	}
	if candidate.Status != status {
		t.Fatalf("%s Status = %s, want %s", id, candidate.Status, status)
	}
	if got := len(candidate.SourceTraceIDs); got != traces {
		t.Fatalf("%s SourceTraceIDs = %d, want %d", id, got, traces)
	}
}

func findCandidate(candidates []AccumulatedEvidenceCandidate, id string) AccumulatedEvidenceCandidate {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate
		}
	}
	return AccumulatedEvidenceCandidate{}
}
