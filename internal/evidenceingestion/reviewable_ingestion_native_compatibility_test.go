package evidenceingestion

import (
	"strings"
	"testing"
)

func TestReviewableIngestionPlainManualDoesNotInventQualification(t *testing.T) {
	batch := reviewableIngestionTestBatch(t, 1, "session:manual-audit")
	batch.SourceSnapshot.OriginMetadata = map[string]string{
		"title": "unqualified title", "location": "unqualified location", "coverage": "full_document",
	}
	manifest := mustReviewableIngestionManifest(t, batch)
	receipt := mustReviewableIngestionReceipt(t, batch, manifest)
	card, err := BuildSourceClaimReviewPackage(receipt, manifest, reviewableIngestionTestQuery(batch, 0))
	if err != nil {
		t.Fatal(err)
	}
	basis := card.ProposalBasis
	if basis.SourceTitle != "" || basis.SourceLocation != "" || basis.SourceCoverage != "" || basis.SourceLimitations == nil || len(basis.SourceLimitations) != 0 {
		t.Fatal("plain manual source acquired qualification from arbitrary origin metadata")
	}
	snapshot := ReviewableSourceClaimReviewSnapshot{
		SubmissionReceipt: receipt, ProposalManifest: manifest, ReviewPackage: card,
		ExactReviewSubject: ExactReviewSubject{SubmissionReceiptID: receipt.ID, ProposalManifestID: manifest.ID,
			ProposalOccurrenceID: basis.ProposalOccurrenceID, ProposalBasisID: basis.ID, ReviewPackageID: card.ID},
	}
	display, _, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(display.PayloadUTF8, "manual_review_profile") || strings.Contains(display.PayloadUTF8, "unqualified title") {
		t.Fatal("plain manual display claims unimplemented profile metadata")
	}
}
