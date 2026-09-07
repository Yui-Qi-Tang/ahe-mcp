package evidenceingestion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAdmitReviewedSourceClaimRejectsNilPool(t *testing.T) {
	_, err := AdmitReviewedSourceClaim(context.Background(), nil, ReviewedSourceClaimAdmissionInput{})
	assertKind(t, err, ErrorInvalidInput)
}

func TestNormalizeReviewedSourceClaimAdmissionInputRequiresBoundedExactCoordinates(t *testing.T) {
	valid := reviewedSourceClaimUnitInput(t)
	tests := []struct {
		name   string
		mutate func(*ReviewedSourceClaimAdmissionInput)
		kind   ErrorKind
	}{
		{name: "attempt", mutate: func(input *ReviewedSourceClaimAdmissionInput) { input.ExtractionAttemptID = "wrong" }, kind: ErrorInvalidRecordID},
		{name: "attempt oversized", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExtractionAttemptID = "attempt:" + strings.Repeat("a", 65)
		}, kind: ErrorInvalidRecordID},
		{name: "occurrence", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalOccurrenceID = "wrong"
		}, kind: ErrorInvalidRecordID},
		{name: "occurrence nonhex", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalOccurrenceID = "occ:" + strings.Repeat("g", 64)
		}, kind: ErrorInvalidRecordID},
		{name: "receipt", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.SubmissionReceiptID = "wrong"
		}, kind: ErrorInvalidRecordID},
		{name: "receipt oversized", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.SubmissionReceiptID = "submission-receipt:v1:sha256:" + strings.Repeat("a", 65)
		}, kind: ErrorInvalidRecordID},
		{name: "manifest", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalManifestID = "wrong"
		}, kind: ErrorInvalidRecordID},
		{name: "basis", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalBasisID = "wrong"
		}, kind: ErrorInvalidRecordID},
		{name: "package", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ReviewPackageID = "wrong"
		}, kind: ErrorInvalidRecordID},
		{name: "display", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewDisplayArtifactID = "wrong"
		}, kind: ErrorInvalidRecordID},
		{name: "display nonhex", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewDisplayArtifactID = "review-display:v1:sha256:" + strings.Repeat("A", 64)
		}, kind: ErrorInvalidRecordID},
		{name: "reviewer missing", mutate: func(input *ReviewedSourceClaimAdmissionInput) { input.DecisionBy = " " }, kind: ErrorInvalidInput},
		{name: "reviewer NUL", mutate: func(input *ReviewedSourceClaimAdmissionInput) { input.DecisionBy = "reviewer\x00" }, kind: ErrorInvalidInput},
		{name: "reviewer oversized", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.DecisionBy = strings.Repeat("x", ProposalDispositionDecisionByMaxBytes+1)
		}, kind: ErrorInvalidInput},
		{name: "reason missing", mutate: func(input *ReviewedSourceClaimAdmissionInput) { input.DecisionReason = " " }, kind: ErrorInvalidInput},
		{name: "reason NUL", mutate: func(input *ReviewedSourceClaimAdmissionInput) { input.DecisionReason = "reason\x00" }, kind: ErrorInvalidInput},
		{name: "reason oversized", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.DecisionReason = strings.Repeat("x", ProposalDispositionDecisionReasonMaxBytes+1)
		}, kind: ErrorInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := normalizeReviewedSourceClaimAdmissionInput(input)
			assertKind(t, err, test.kind)
		})
	}

	got, err := normalizeReviewedSourceClaimAdmissionInput(valid)
	if err != nil {
		t.Fatalf("normalizeReviewedSourceClaimAdmissionInput(valid) error = %v", err)
	}
	if got != valid {
		t.Fatalf("normalized input = %+v, want %+v", got, valid)
	}
}

func TestRunReviewedSourceClaimAdmissionAttemptsRetriesOnlySerializationFailure(t *testing.T) {
	attempts := 0
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		attempts++
		if attempts < reviewedSourceClaimAdmissionSerializationAttempts {
			return &pgconn.PgError{Code: "40001"}
		}
		return nil
	})
	if err != nil || attempts != reviewedSourceClaimAdmissionSerializationAttempts {
		t.Fatalf("serialization retry error/attempts = %v/%d", err, attempts)
	}

	want := errors.New("stop")
	attempts = 0
	err = runReviewedSourceClaimAdmissionAttempts(func() error {
		attempts++
		return want
	})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("non-serialization error/attempts = %v/%d, want stop/1", err, attempts)
	}
}

func reviewedSourceClaimUnitInput(t *testing.T) ReviewedSourceClaimAdmissionInput {
	t.Helper()
	snapshot := reviewableIngestionTestSnapshot(t, "session:reviewed-admission")
	_, subject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewDisplayArtifact() error = %v", err)
	}
	return ReviewedSourceClaimAdmissionInput{
		ExtractionAttemptID: snapshot.SubmissionReceipt.ExtractionAttemptID,
		ExpectedSubject:     subject,
		DecisionBy:          "reviewer",
		DecisionReason:      "exact package approved",
	}
}
