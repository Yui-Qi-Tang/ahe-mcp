package ahemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

func validateBriefRead(input BriefSubmission, handoff Handoff) error {
	if err := ValidateBriefSubmission(input); err != nil {
		return err
	}
	return validateBriefHandoff(handoff)
}

func verifyBriefRecord(record pendingRecord, input BriefSubmission, handoff Handoff) (CandidateObservation, error) {
	outcome := record.AdmissionOutcome
	if outcome != "pending" && !candidateTerminalState(outcome) {
		return CandidateObservation{}, errors.New("brief Query returned unsupported lifecycle")
	}
	if handoff.Status != "pending" && outcome != handoff.Status {
		return CandidateObservation{}, errors.New("brief Query differs from its terminal submission receipt")
	}
	observation := CandidateObservation{AdmissionOutcome: outcome}
	if outcome == "admitted" {
		if json.Unmarshal(record.CanonicalRef, &observation.CanonicalRef) != nil || !hexID(observation.CanonicalRef, "canon-node:", 16) {
			return CandidateObservation{}, errors.New("admitted brief Query requires exact canonical identity")
		}
	} else if !bytes.Equal(bytes.TrimSpace(record.CanonicalRef), []byte("null")) {
		return CandidateObservation{}, errors.New("non-admitted brief Query contains canonical authority")
	}
	if record.Extractor.ConfigHash != "sha256:"+digest(briefDefinition(input).Config) {
		return CandidateObservation{}, errors.New("brief Query producer configuration differs from frozen selection")
	}
	// The existing checker knows manual-line byte offsets and foreign authority.
	// Only the already-checked lifecycle/config projection is substituted; no
	// capability classification or lab-status validation is fabricated.
	x := briefExtractor(input)
	record.Extractor.ConfigHash = "sha256:" + digest(map[string]string{"model": x.Model, "source_mode": "frozen_local_snapshot"})
	record.AdmissionOutcome, record.CanonicalRef = "pending", json.RawMessage("null")
	document, _ := briefDocument(input) // All callers validate the frozen input first.
	if err := verifyPendingRecord(record, briefSourceID(input), document, x, labstatus.Record{Statement: input.Statement, Citation: input.Citation}, handoff); err != nil {
		return CandidateObservation{}, err
	}
	return observation, nil
}

// ObserveBrief independently reads one saved occurrence and verifies the exact
// statement, source and producer. An admitted outcome additionally requires the
// canonical source-claim readback; this is not a reviewer-authentication check.
func ObserveBrief(ctx context.Context, command string, input BriefSubmission, handoff Handoff) (CandidateObservation, error) {
	if ctx == nil {
		return CandidateObservation{}, errors.New("brief Query context is required")
	}
	if err := validateBriefRead(input, handoff); err != nil {
		return CandidateObservation{}, err
	}
	c, err := startQuery(ctx, command)
	if err != nil {
		return CandidateObservation{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return CandidateObservation{}, err
	}
	var record pendingRecord
	if err := c.call("get_evidence_record", map[string]string{"proposal_occurrence_id": handoff.ProposalOccurrenceID}, &record); err != nil {
		return CandidateObservation{}, err
	}
	observation, err := verifyBriefRecord(record, input, handoff)
	if err != nil {
		return CandidateObservation{}, err
	}
	if observation.AdmissionOutcome == "admitted" {
		var canonical pendingRecord
		if err := c.call("get_evidence_record", map[string]string{"canonical_id": observation.CanonicalRef}, &canonical); err != nil {
			return CandidateObservation{}, err
		}
		record.AdmissionOutcome, record.CanonicalRef = "pending", json.RawMessage("null")
		if err := verifyCanonicalReadback(canonical, record, ReviewedAdmission{CanonicalRef: observation.CanonicalRef}); err != nil {
			return CandidateObservation{}, err
		}
	}
	if err := c.Close(); err != nil {
		return CandidateObservation{}, errors.New("brief Query launcher did not exit cleanly")
	}
	return observation, nil
}

// ReviewBrief reads the complete current native pending review through the
// explicitly selected reviewer launcher. It sends no decision or admission.
func ReviewBrief(ctx context.Context, command string, input BriefSubmission, handoff Handoff) (SourceClaimReview, error) {
	if ctx == nil {
		return SourceClaimReview{}, errors.New("brief review context is required")
	}
	if err := validateBriefRead(input, handoff); err != nil {
		return SourceClaimReview{}, err
	}
	if handoff.Status != "pending" {
		return SourceClaimReview{}, errors.New("brief review requires a pending handoff")
	}
	c, err := startReviewer(ctx, command)
	if err != nil {
		return SourceClaimReview{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return SourceClaimReview{}, err
	}
	var review SourceClaimReview
	if err := c.call("get_source_claim_review", map[string]string{"extraction_attempt_id": handoff.ExtractionAttemptID, "proposal_occurrence_id": handoff.ProposalOccurrenceID}, &review); err != nil {
		return SourceClaimReview{}, err
	}
	if err := ValidateBriefReview(review, input, handoff); err != nil {
		return SourceClaimReview{}, err
	}
	if err := c.Close(); err != nil {
		return SourceClaimReview{}, errors.New("brief reviewer launcher did not exit cleanly")
	}
	return review, nil
}

// ValidateBriefReview binds the native display to exact saved source, statement,
// excerpt and caller-declared provenance. Native manual source metadata remains
// a digest commitment, not verified provider identity or source truth.
func ValidateBriefReview(review SourceClaimReview, input BriefSubmission, handoff Handoff) error {
	if err := validateBriefRead(input, handoff); err != nil {
		return err
	}
	if handoff.Status != "pending" {
		return errors.New("brief review requires its saved pending receipt")
	}
	pkg, err := validateReviewEnvelope(review)
	if err != nil {
		return err
	}
	b, r := pkg.ProposalBasis, review.SubmissionReceipt
	if r.SourceSnapshotID != handoff.SourceSnapshotID || r.ExtractionViewID != handoff.ExtractionViewID || r.ExtractionAttemptID != handoff.ExtractionAttemptID || b.ProposalOccurrenceID != handoff.ProposalOccurrenceID {
		return errors.New("brief review receipt differs from saved handoff")
	}
	var projection pendingRecord
	projection.RecordRef.Kind, projection.RecordRef.ID = "proposal", b.ProposalOccurrenceID
	projection.ProposalLocalID, projection.ProposalKind = b.ProposalLocalID, b.ProposalKind
	projection.ProposalFingerprint, projection.ProposalFingerprintVersion = b.ProposalFingerprint, b.ProposalFingerprintVersion
	projection.StatementText, projection.AdmissionOutcome, projection.CanonicalRef = b.StatementText, "pending", json.RawMessage("null")
	projection.Source.BindingKind, projection.Source.SourceSnapshotID, projection.Source.SourceSystem = b.SourceBindingKind, b.SourceSnapshotID, b.SourceSystem
	projection.Source.SourceID, projection.Source.SourceVersion, projection.Source.RawContentHash = b.SourceID, b.SourceVersion, b.RawContentHash
	projection.SourceRefs = b.SourceRefs
	projection.ExtractionViewID, projection.RendererName, projection.RendererVersion, projection.RenderedContentHash = b.ExtractionViewID, b.RendererName, b.RendererVersion, b.RenderedContentHash
	projection.Extractor.ExtractorDefinitionID, projection.Extractor.ExtractionRunID, projection.Extractor.ExtractionAttemptID = b.ExtractorDefinitionID, b.ExtractionRunID, b.ExtractionAttemptID
	projection.Extractor.Name, projection.Extractor.Version, projection.Extractor.ConfigHash, projection.Extractor.AttemptStatus = b.ExtractorName, b.ExtractorVersion, b.ExtractorConfigHash, "succeeded"
	if _, err := verifyBriefRecord(projection, input, handoff); err != nil {
		return err
	}
	if b.OriginMetadataHash != "sha256:"+digest(briefOrigin(input)) {
		return errors.New("brief review source metadata differs from saved caller declarations")
	}
	spans := make([]span, 0, len(b.SourceRefs))
	for i, ref := range b.SourceRefs {
		spans = append(spans, span{SpanID: ref.SpanID, DisplayLine: input.Citation.StartLine + i})
	}
	request, err := briefRequest(input, sourceResult{SourceSnapshotID: b.SourceSnapshotID, ExtractionViewID: b.ExtractionViewID}, spans)
	if err != nil {
		return err
	}
	if r.RequestID != request.RequestID || r.ExtractorOutputHash != "sha256:"+digest(request.ExtractorOutput) {
		return errors.New("brief review request or output differs from saved selection")
	}
	return nil
}

// VerifyBriefAdmitted checks a native admission acknowledgement and independently
// reads the selected occurrence plus canonical source claim through Query.
func VerifyBriefAdmitted(ctx context.Context, command string, input BriefSubmission, handoff Handoff, admission ReviewedAdmission) error {
	if err := validateBriefRead(input, handoff); err != nil {
		return err
	}
	if err := validateReviewedAdmission(admission, handoff.ProposalOccurrenceID, input.Citation.EndLine-input.Citation.StartLine+1); err != nil {
		return err
	}
	observation, err := ObserveBrief(ctx, command, input, handoff)
	if err != nil {
		return err
	}
	if observation.AdmissionOutcome != "admitted" || observation.CanonicalRef != admission.CanonicalRef {
		return errors.New("brief Query differs from admission acknowledgement")
	}
	return nil
}

// VerifyBriefDisposed independently checks the selected noncanonical terminal
// outcome. Query cannot authenticate the reviewer or verify their decision reason.
func VerifyBriefDisposed(ctx context.Context, command string, input BriefSubmission, handoff Handoff, disposition ReviewedDisposition) error {
	if err := validateBriefRead(input, handoff); err != nil {
		return err
	}
	decision := disposition.AdmissionOutcome
	if decision == "rejected" {
		decision = "reject"
	}
	if err := validateReviewedDisposition(disposition, handoff.ProposalOccurrenceID, decision, disposition.DecisionReason); err != nil {
		return err
	}
	observation, err := ObserveBrief(ctx, command, input, handoff)
	if err != nil {
		return err
	}
	if observation.AdmissionOutcome != disposition.AdmissionOutcome || observation.CanonicalRef != "" {
		return errors.New("brief Query differs from noncanonical acknowledgement")
	}
	return nil
}
