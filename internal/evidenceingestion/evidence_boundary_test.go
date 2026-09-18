package evidenceingestion

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestEvidenceBoundaryExperiment is a synthetic contract experiment, not an
// agent benchmark. It calls production materialization/review functions; only
// the revision-collision case uses the existing SQL test double.
func TestEvidenceBoundaryExperiment(t *testing.T) {
	const sourceText = "Refunds for overseas orders must be completed within 7 days.\n"
	const supported = "Refunds for overseas orders must be completed within 7 days."
	const unsupported = "Refunds for all orders must be completed within 30 days."
	input := validExternalSourceEnvelope("evidence-boundary-v1")
	input.Content = sourceText
	input.Title = "Synthetic overseas refund policy"
	prepared, err := prepareExternalSource(input)
	if err != nil {
		t.Fatal(err)
	}
	source, err := buildManualSourceContext(prepared.manual)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := buildAttemptContextFromSourceWithDefinitionAndSession(source, input.RequestID, 1,
		ExtractorDefinitionInput{Name: "synthetic-boundary-fixture", Version: "v1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	materialize := func(statement, span string) (ReviewableSourceClaimBatch, error) {
		batch, err := materializeReviewableBatch(attempt, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "refund-rule",
			StatementText:   statement,
			EvidenceRefs:    []string{span},
		}}})
		if err == nil {
			// The fixture supplies the successful extractor completion coordinate.
			batch.ExtractionAttempt.Status = attemptStatusSucceeded
			batch.ExtractionAttempt.OutputHash = batch.FixtureOutputHash
		}
		return batch, err
	}
	batch, err := materialize(supported, "span:S1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := mustReviewableIngestionManifest(t, batch)
	receipt := mustReviewableIngestionReceipt(t, batch, manifest)
	current := reviewableIngestionTestQuery(batch, 0)
	review, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
	if err != nil {
		t.Fatal(err)
	}
	subject := ExactReviewSubject{
		SubmissionReceiptID: receipt.ID, ProposalManifestID: manifest.ID,
		ProposalOccurrenceID: current.ProposalOccurrenceID,
		ProposalBasisID:      review.ProposalBasis.ID, ReviewPackageID: review.ID,
	}
	validate := func(candidate ProposalQueryResult) error {
		return ValidateExactSourceClaimReviewSubject(receipt, manifest, candidate, review, subject)
	}
	t.Logf("fixture=v1 source_sha256=%s source=%q", hashHex([]byte(sourceText)), sourceText)

	t.Run("supported_claim_stays_pending", func(t *testing.T) {
		if current.AdmissionOutcome != admissionOutcomePending || current.CanonicalRef != "" || len(current.SourceRefs) != 1 {
			t.Fatalf("expected one cited pending claim without canonical authority: %+v", current)
		}
		if current.SourceRefs[0].QuotedText != supported {
			t.Fatalf("quote = %q, want exact source sentence", current.SourceRefs[0].QuotedText)
		}
		t.Log("observed: supported candidate is pending, not admitted")
	})
	t.Run("unsupported_claim_also_stays_pending", func(t *testing.T) {
		other, err := materialize(unsupported, "span:S1")
		if err != nil {
			t.Fatalf("semantic counterexample unexpectedly rejected: %v", err)
		}
		otherManifest := mustReviewableIngestionManifest(t, other)
		otherReceipt := mustReviewableIngestionReceipt(t, other, otherManifest)
		proposal := reviewableIngestionTestQuery(other, 0)
		if !reflect.DeepEqual(proposal.SourceRefs, current.SourceRefs) || other.SourceSnapshot.ID != batch.SourceSnapshot.ID {
			t.Fatal("paired claims must have identical source identity and exact citations")
		}
		otherReview, err := BuildSourceClaimReviewPackage(otherReceipt, otherManifest, proposal)
		if err != nil {
			t.Fatal(err)
		}
		if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" || otherReview.ID == review.ID {
			t.Fatal("unsupported candidate must remain pending with a different review identity")
		}
		t.Logf("observed limitation: %q has a valid citation and review package; semantic truth is not checked", unsupported)
	})
	t.Run("unknown_span_rejected", func(t *testing.T) {
		_, err := materialize(supported, "span:MISSING")
		assertKind(t, err, ErrorUnknownSpan)
	})
	t.Run("exact_review_accepted", func(t *testing.T) {
		if err := validate(current); err != nil {
			t.Fatal(err)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*ProposalQueryResult)
	}{
		{"changed_claim_rejected", func(p *ProposalQueryResult) { p.StatementText = unsupported }},
		{"changed_revision_rejected", func(p *ProposalQueryResult) { p.SourceVersion += "-changed" }},
		{"changed_coverage_rejected", func(p *ProposalQueryResult) {
			p.OriginMetadata[externalSourceOriginCoverageKey] = ExternalSourceCoverageTruncatedDocument
			p.OriginMetadata[externalSourceOriginLimitsKey] = `["Appendix was not collected."]`
		}},
		{"rejected_proposal_rejected", func(p *ProposalQueryResult) { p.AdmissionOutcome = admissionOutcomeRejected }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneReviewableIngestionQuery(current)
			test.mutate(&candidate)
			// Retrieval sees the same bytes/references in both conditions. Only
			// one claim/revision/coverage/lifecycle dimension changes per case.
			if !reflect.DeepEqual(candidate.SourceRefs, current.SourceRefs) || candidate.RawContentHash != current.RawContentHash {
				t.Fatal("mutation changed the controlled citation or source bytes")
			}
			assertKind(t, validate(candidate), ErrorReviewContractConflict)
			if err := validate(current); err != nil {
				t.Fatalf("unchanged paired control failed: %v", err)
			}
		})
	}
	t.Run("changed_display_rejected", func(t *testing.T) {
		snapshot := ReviewableSourceClaimReviewSnapshot{
			SubmissionReceipt: receipt, ProposalManifest: manifest,
			ReviewPackage: review, ExactReviewSubject: subject,
		}
		artifact, displayed, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateExactSourceClaimReviewDisplay(snapshot, artifact, displayed); err != nil {
			t.Fatal(err)
		}
		artifact.PayloadUTF8 += " "
		assertKind(t, ValidateExactSourceClaimReviewDisplay(snapshot, artifact, displayed), ErrorReviewContractConflict)
	})
	t.Run("same_revision_changed_bytes_rejected", func(t *testing.T) {
		db := newMockSQLDB()
		ctx := context.Background()
		first, err := captureExternalSource(ctx, db, input)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := captureExternalSource(ctx, db, input)
		if err != nil || !replay.Replayed || replay.SourceSnapshotID != first.SourceSnapshotID {
			t.Fatalf("exact replay = %+v, error = %v", replay, err)
		}
		storedHash := db.sourceSnapshots[first.SourceSnapshotID].rawContentHash
		changed := input
		changed.RequestID += "-new-delivery"
		changed.Content = strings.Replace(sourceText, "7 days", "9 days", 1)
		_, err = captureExternalSource(ctx, db, changed)
		assertKind(t, err, ErrorOccurrenceConflict)
		if len(db.sourceSnapshots) != 1 || len(db.externalSourceReceipts) != 1 || len(db.proposalOccurrences) != 0 || len(db.canonicalGraphNodes) != 0 {
			t.Fatal("revision collision changed mock source/receipt/proposal/canonical counts")
		}
		if db.sourceSnapshots[first.SourceSnapshotID].rawContentHash != storedHash {
			t.Fatal("revision collision replaced the stored source content hash")
		}
	})
}
