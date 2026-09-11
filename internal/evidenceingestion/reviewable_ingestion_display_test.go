package evidenceingestion

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestLoadSourceClaimReviewDisplayArtifactRejectsNilPool(t *testing.T) {
	_, _, err := LoadSourceClaimReviewDisplayArtifact(context.Background(), nil, "attempt:x", "occ:x")
	assertKind(t, err, ErrorInvalidInput)
}

func TestBuildSourceClaimReviewDisplayArtifactEmitsExactDeterministicPackage(t *testing.T) {
	snapshot := reviewableIngestionTestSnapshot(t, "session:display")

	artifact, subject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewDisplayArtifact() error = %v", err)
	}
	payload, err := deterministicJSON(snapshot.ReviewPackage)
	if err != nil {
		t.Fatalf("deterministicJSON(review package) error = %v", err)
	}
	if artifact.ContractVersion != SourceClaimReviewDisplayArtifactV1 ||
		artifact.MediaType != SourceClaimReviewDisplayMediaTypeV1 ||
		artifact.ReviewPackageID != snapshot.ReviewPackage.ID ||
		artifact.PayloadUTF8 != string(payload) ||
		strings.HasSuffix(artifact.PayloadUTF8, "\n") {
		t.Fatalf("display artifact = %+v, want exact canonical review package bytes", artifact)
	}
	wantArtifactID := "review-display:v1:sha256:" + hashHex([]byte(artifact.MediaType+"\n"+artifact.PayloadUTF8))
	if artifact.ID != wantArtifactID {
		t.Fatalf("display artifact ID = %q, want %q", artifact.ID, wantArtifactID)
	}
	if subject.ReviewSubject != snapshot.ExactReviewSubject || subject.ReviewDisplayArtifactID != artifact.ID {
		t.Fatalf("displayed subject = %+v, want snapshot subject bound to %q", subject, artifact.ID)
	}
	if err := ValidateExactSourceClaimReviewDisplay(snapshot, artifact, subject); err != nil {
		t.Fatalf("ValidateExactSourceClaimReviewDisplay(exact) error = %v", err)
	}

	rebuiltArtifact, rebuiltSubject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewDisplayArtifact(rebuild) error = %v", err)
	}
	if !reflect.DeepEqual(rebuiltArtifact, artifact) || !reflect.DeepEqual(rebuiltSubject, subject) {
		t.Fatalf("rebuilt display changed:\n first: %+v / %+v\nsecond: %+v / %+v", artifact, subject, rebuiltArtifact, rebuiltSubject)
	}
}

func TestValidateExactSourceClaimReviewDisplayRejectsArtifactOrSubjectDrift(t *testing.T) {
	snapshot := reviewableIngestionTestSnapshot(t, "session:display-drift")
	artifact, subject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewDisplayArtifact() error = %v", err)
	}

	tests := []struct {
		name           string
		mutateArtifact func(*SourceClaimReviewDisplayArtifact)
		mutateSubject  func(*ExactDisplayedReviewSubject)
	}{
		{name: "artifact ID", mutateArtifact: func(value *SourceClaimReviewDisplayArtifact) { value.ID += "x" }},
		{name: "package ID", mutateArtifact: func(value *SourceClaimReviewDisplayArtifact) { value.ReviewPackageID += "x" }},
		{name: "media type", mutateArtifact: func(value *SourceClaimReviewDisplayArtifact) { value.MediaType += "+changed" }},
		{name: "payload", mutateArtifact: func(value *SourceClaimReviewDisplayArtifact) { value.PayloadUTF8 += " " }},
		{name: "display subject", mutateSubject: func(value *ExactDisplayedReviewSubject) { value.ReviewDisplayArtifactID += "x" }},
		{name: "review subject", mutateSubject: func(value *ExactDisplayedReviewSubject) { value.ReviewSubject.ProposalBasisID += "x" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateArtifact := artifact
			candidateSubject := subject
			if test.mutateArtifact != nil {
				test.mutateArtifact(&candidateArtifact)
			}
			if test.mutateSubject != nil {
				test.mutateSubject(&candidateSubject)
			}
			assertKind(t, ValidateExactSourceClaimReviewDisplay(snapshot, candidateArtifact, candidateSubject), ErrorReviewContractConflict)
		})
	}
}

func TestSourceClaimReviewDisplayIdentityExcludesSessionAuditMetadata(t *testing.T) {
	first := reviewableIngestionTestSnapshot(t, "session:first")
	second := reviewableIngestionTestSnapshot(t, "session:second")

	firstArtifact, firstSubject, err := BuildSourceClaimReviewDisplayArtifact(first)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewDisplayArtifact(first) error = %v", err)
	}
	secondArtifact, secondSubject, err := BuildSourceClaimReviewDisplayArtifact(second)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewDisplayArtifact(second) error = %v", err)
	}
	if first.SubmissionReceipt.ProducerSessionRef == second.SubmissionReceipt.ProducerSessionRef {
		t.Fatal("test snapshots have identical session metadata")
	}
	if !reflect.DeepEqual(firstArtifact, secondArtifact) || !reflect.DeepEqual(firstSubject, secondSubject) {
		t.Fatalf("session metadata changed display identity:\n first: %+v / %+v\nsecond: %+v / %+v", firstArtifact, firstSubject, secondArtifact, secondSubject)
	}
}

func TestBuildSourceClaimReviewDisplayArtifactRejectsOversizedValidPackage(t *testing.T) {
	statement := strings.Repeat("x", SourceClaimReviewDisplayMaxBytesV1+1)
	input := testManualInput("review-display-bound")
	input.Raw = []byte(statement + "\n")
	source, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	attempt, err := buildAttemptContextFromSourceWithDefinitionAndSession(
		source,
		input.RequestID,
		input.AttemptNumber,
		ExtractorDefinitionInput{},
		"session:display-bound",
	)
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinitionAndSession() error = %v", err)
	}
	batch, err := materializeReviewableBatch(attempt, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: "oversized",
		StatementText:   statement,
		EvidenceRefs:    []string{"span:S1"},
	}}})
	if err != nil {
		t.Fatalf("materializeReviewableBatch() error = %v", err)
	}
	batch.ExtractionAttempt.Status = attemptStatusSucceeded
	batch.ExtractionAttempt.OutputHash = batch.FixtureOutputHash
	manifest := mustReviewableIngestionManifest(t, batch)
	receipt := mustReviewableIngestionReceipt(t, batch, manifest)
	current := reviewableIngestionTestQuery(batch, 0)
	reviewPackage, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
	if err != nil {
		t.Fatalf("BuildSourceClaimReviewPackage() error = %v", err)
	}
	snapshot := ReviewableSourceClaimReviewSnapshot{
		SubmissionReceipt: receipt,
		ProposalManifest:  manifest,
		ReviewPackage:     reviewPackage,
		ExactReviewSubject: ExactReviewSubject{
			SubmissionReceiptID:  receipt.ID,
			ProposalManifestID:   manifest.ID,
			ProposalOccurrenceID: current.ProposalOccurrenceID,
			ProposalBasisID:      reviewPackage.ProposalBasis.ID,
			ReviewPackageID:      reviewPackage.ID,
		},
	}
	payload, err := deterministicJSON(snapshot.ReviewPackage)
	if err != nil {
		t.Fatalf("deterministicJSON(review package) error = %v", err)
	}
	if len(payload) <= SourceClaimReviewDisplayMaxBytesV1 {
		t.Fatalf("fixture payload = %d bytes, want above %d-byte bound", len(payload), SourceClaimReviewDisplayMaxBytesV1)
	}
	_, _, err = BuildSourceClaimReviewDisplayArtifact(snapshot)
	assertKind(t, err, ErrorInvalidInput)
}

func reviewableIngestionTestSnapshot(t *testing.T, session string) ReviewableSourceClaimReviewSnapshot {
	t.Helper()
	artifacts := newReviewableIngestionTestArtifacts(t, session)
	return ReviewableSourceClaimReviewSnapshot{
		SubmissionReceipt:  artifacts.receipt,
		ProposalManifest:   artifacts.manifest,
		ReviewPackage:      artifacts.reviewPackage,
		ExactReviewSubject: artifacts.subject,
	}
}
