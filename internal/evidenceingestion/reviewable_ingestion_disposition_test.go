package evidenceingestion

import "testing"

func TestReviewedSourceClaimDispositionRejectsInvalidInputBeforeTransaction(t *testing.T) {
	if _, err := RecordReviewedSourceClaimDisposition(t.Context(), nil, ReviewedSourceClaimDispositionInput{}); err == nil {
		t.Fatal("nil pool accepted")
	}
	valid := reviewedSourceClaimUnitInput(t)
	for _, outcome := range []string{"", "pending", "approved", "admitted", "reject", " rejected "} {
		got, err := recordReviewedSourceClaimDisposition(t.Context(), nil, ReviewedSourceClaimDispositionInput{
			ExtractionAttemptID: valid.ExtractionAttemptID, ExpectedSubject: valid.ExpectedSubject, Outcome: outcome,
			DecisionBy: valid.DecisionBy, DecisionReason: valid.DecisionReason})
		assertKind(t, err, ErrorInvalidInput)
		if got != (ProposalDispositionResult{}) {
			t.Fatal("invalid disposition returned a result")
		}
	}
	_, err := recordReviewedSourceClaimDisposition(t.Context(), nil, ReviewedSourceClaimDispositionInput{Outcome: ProposalDispositionRejected})
	assertKind(t, err, ErrorInvalidRecordID)
}

func TestReviewedDispositionBindingIsNotAdmissionBinding(t *testing.T) {
	snapshot := reviewableIngestionTestSnapshot(t, "session:reviewed-disposition")
	display, subject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input := ReviewedSourceClaimAdmissionInput{ExtractionAttemptID: snapshot.SubmissionReceipt.ExtractionAttemptID,
		ExpectedSubject: subject, DecisionBy: "synthetic-reviewer", DecisionReason: "synthetic decision, not authenticated human review"}
	result := AdmissionResult{AdmissionDecisionID: "adm:synthetic", ProposalOccurrenceID: subject.ReviewSubject.ProposalOccurrenceID,
		AdmissionOutcome: ProposalDispositionRejected}
	binding := newSourceClaimReviewBinding(result, input, snapshot, display)
	binding.ContractVersion = ReviewedSourceClaimDispositionV1
	if err := validatePersistedSourceClaimReviewBindingContract(binding, result, input, ReviewedSourceClaimDispositionV1); err != nil {
		t.Fatal(err)
	}
	assertKind(t, validatePersistedSourceClaimReviewBinding(binding, result, input), ErrorAdmissionReplayConflict)
	changed := binding
	changed.ReviewDisplayPayload += " "
	assertKind(t, validatePersistedSourceClaimReviewBindingContract(changed, result, input, ReviewedSourceClaimDispositionV1), ErrorAdmissionReplayConflict)
	changed = binding
	changed.ReviewDisplayArtifactID = differentReviewStableIDForUnit(changed.ReviewDisplayArtifactID)
	assertKind(t, validatePersistedSourceClaimReviewBindingContract(changed, result, input, ReviewedSourceClaimDispositionV1), ErrorAdmissionReplayConflict)
}

func differentReviewStableIDForUnit(value string) string {
	if value[len(value)-1] == '0' {
		return value[:len(value)-1] + "1"
	}
	return value[:len(value)-1] + "0"
}
