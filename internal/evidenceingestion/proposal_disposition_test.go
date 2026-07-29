package evidenceingestion

import (
	"context"
	"strings"
	"testing"
)

func TestMockSQLRecordPendingProposalDisposition(t *testing.T) {
	for _, outcome := range []string{
		ProposalDispositionRejected,
		ProposalDispositionAuditOnly,
	} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			db := newMockSQLDB()
			ingested, err := ingestManualText(ctx, db, testManualInput("mock-disposition-"+outcome), testFixture())
			if err != nil {
				t.Fatalf("ingestManualText() error = %v", err)
			}

			input := ProposalDispositionInput{
				ProposalOccurrenceID: ingested.ProposalOccurrenceID,
				Outcome:              outcome,
				DecisionBy:           "unit-test",
				DecisionReason:       "not eligible for canonical publication",
			}
			result, err := recordPendingProposalDisposition(ctx, db, input)
			if err != nil {
				t.Fatalf("recordPendingProposalDisposition() error = %v", err)
			}
			if result.ProposalOccurrenceID != ingested.ProposalOccurrenceID ||
				result.AdmissionDecisionID == "" ||
				result.AdmissionOutcome != outcome ||
				result.DecisionBy != input.DecisionBy ||
				result.DecisionReason != input.DecisionReason ||
				result.Replayed {
				t.Fatalf("disposition result = %+v", result)
			}

			got, err := traceProposalProvenance(ctx, db, ingested.ProposalOccurrenceID)
			if err != nil {
				t.Fatalf("traceProposalProvenance() error = %v", err)
			}
			if got.AdmissionOutcome != outcome || got.CanonicalRef != "" {
				t.Fatalf("proposal state = %s/%s, want %s/empty", got.AdmissionOutcome, got.CanonicalRef, outcome)
			}
			decision := db.admissionDecisions[result.AdmissionDecisionID]
			if decision.outcome != outcome ||
				decision.canonicalRef != "" ||
				len(decision.rawEvidenceNodeIDs) != 0 ||
				len(decision.canonicalEdgeIDs) != 0 ||
				decision.decisionBy != input.DecisionBy ||
				decision.decisionReason != input.DecisionReason {
				t.Fatalf("admission decision = %+v", decision)
			}
			if len(db.canonicalGraphNodes) != 0 || len(db.canonicalGraphEdges) != 0 {
				t.Fatalf("disposition mutated canonical graph: nodes %d edges %d", len(db.canonicalGraphNodes), len(db.canonicalGraphEdges))
			}

			replay, err := recordPendingProposalDisposition(ctx, db, input)
			if err != nil {
				t.Fatalf("replay recordPendingProposalDisposition() error = %v", err)
			}
			if !replay.Replayed ||
				replay.AdmissionDecisionID != result.AdmissionDecisionID ||
				replay.AdmissionOutcome != outcome ||
				replay.DecisionBy != input.DecisionBy ||
				replay.DecisionReason != input.DecisionReason {
				t.Fatalf("replay = %+v, want replay of %+v", replay, result)
			}
			conflictingInput := input
			conflictingInput.DecisionBy = "different-reviewer"
			_, err = recordPendingProposalDisposition(ctx, db, conflictingInput)
			assertKind(t, err, ErrorAdmissionStateConflict)
			if len(db.admissionDecisions) != 1 {
				t.Fatalf("admission decision count = %d, want 1", len(db.admissionDecisions))
			}
		})
	}
}

func TestMockSQLRecordPendingProposalDispositionRejectsTerminalStateChange(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	rejected, err := ingestManualText(ctx, db, testManualInput("mock-disposition-rejected"), testFixture())
	if err != nil {
		t.Fatalf("ingest rejected fixture: %v", err)
	}
	if _, err := recordPendingProposalDisposition(ctx, db, ProposalDispositionInput{
		ProposalOccurrenceID: rejected.ProposalOccurrenceID,
		Outcome:              ProposalDispositionRejected,
		DecisionBy:           "unit-test",
		DecisionReason:       "rejected",
	}); err != nil {
		t.Fatalf("record rejected disposition: %v", err)
	}
	_, err = recordPendingProposalDisposition(ctx, db, ProposalDispositionInput{
		ProposalOccurrenceID: rejected.ProposalOccurrenceID,
		Outcome:              ProposalDispositionAuditOnly,
		DecisionBy:           "unit-test",
		DecisionReason:       "changed mind",
	})
	assertKind(t, err, ErrorAdmissionStateConflict)
	_, err = admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: rejected.ProposalOccurrenceID})
	assertKind(t, err, ErrorAdmissionStateConflict)

	admitted, err := ingestManualText(ctx, db, testManualInput("mock-disposition-admitted"), testFixture())
	if err != nil {
		t.Fatalf("ingest admitted fixture: %v", err)
	}
	if _, err := admitPendingProposal(ctx, db, AdmissionInput{ProposalOccurrenceID: admitted.ProposalOccurrenceID}); err != nil {
		t.Fatalf("admit fixture: %v", err)
	}
	_, err = recordPendingProposalDisposition(ctx, db, ProposalDispositionInput{
		ProposalOccurrenceID: admitted.ProposalOccurrenceID,
		Outcome:              ProposalDispositionRejected,
		DecisionBy:           "unit-test",
		DecisionReason:       "too late",
	})
	assertKind(t, err, ErrorAdmissionStateConflict)
}

func TestMockSQLRecordPendingProposalDispositionReportsMissingTerminalAudit(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	ingested, err := ingestManualText(ctx, db, testManualInput("mock-disposition-missing-audit"), testFixture())
	if err != nil {
		t.Fatalf("ingest fixture: %v", err)
	}
	occurrence := db.proposalOccurrences[ingested.ProposalOccurrenceID]
	occurrence.AdmissionOutcome = admissionOutcomeRejected
	db.proposalOccurrences[ingested.ProposalOccurrenceID] = occurrence

	_, err = recordPendingProposalDisposition(ctx, db, ProposalDispositionInput{
		ProposalOccurrenceID: ingested.ProposalOccurrenceID,
		Outcome:              ProposalDispositionRejected,
		DecisionBy:           "unit-test",
		DecisionReason:       "reason",
	})
	assertKind(t, err, ErrorAdmissionStateConflict)
	if !strings.Contains(err.Error(), "terminal proposal") {
		t.Fatalf("missing-audit error = %v, want terminal proposal diagnostic", err)
	}
}

func TestRecordPendingProposalDispositionValidatesOperatorDecision(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input ProposalDispositionInput
		kind  ErrorKind
	}{
		{
			name: "missing occurrence",
			input: ProposalDispositionInput{
				Outcome:        ProposalDispositionRejected,
				DecisionBy:     "unit-test",
				DecisionReason: "reason",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "invalid occurrence",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "proposal:1",
				Outcome:              ProposalDispositionRejected,
				DecisionBy:           "unit-test",
				DecisionReason:       "reason",
			},
			kind: ErrorInvalidRecordID,
		},
		{
			name: "unsupported outcome",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              "admitted",
				DecisionBy:           "unit-test",
				DecisionReason:       "reason",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "missing decision by",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              ProposalDispositionRejected,
				DecisionReason:       "reason",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "missing reason",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              ProposalDispositionAuditOnly,
				DecisionBy:           "unit-test",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "long decision by",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              ProposalDispositionRejected,
				DecisionBy:           strings.Repeat("r", ProposalDispositionDecisionByMaxBytes+1),
				DecisionReason:       "reason",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "invalid decision by utf8",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              ProposalDispositionRejected,
				DecisionBy:           string([]byte{0xff}),
				DecisionReason:       "reason",
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "long decision reason",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              ProposalDispositionAuditOnly,
				DecisionBy:           "unit-test",
				DecisionReason:       strings.Repeat("r", ProposalDispositionDecisionReasonMaxBytes+1),
			},
			kind: ErrorInvalidInput,
		},
		{
			name: "invalid decision reason utf8",
			input: ProposalDispositionInput{
				ProposalOccurrenceID: "occ:1",
				Outcome:              ProposalDispositionAuditOnly,
				DecisionBy:           "unit-test",
				DecisionReason:       string([]byte{0xff}),
			},
			kind: ErrorInvalidInput,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := recordPendingProposalDisposition(context.Background(), newMockSQLDB(), tc.input)
			assertKind(t, err, tc.kind)
		})
	}
}
