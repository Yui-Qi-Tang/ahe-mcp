package ahemcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// ReviewedDisposition is the exact-reviewed noncanonical writer's receipt.
// Its reason is sensitive review material; callers must not log it by default.
type ReviewedDisposition struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	AdmissionDecisionID  string `json:"admission_decision_id"`
	AdmissionOutcome     string `json:"admission_outcome"`
	DecisionBy           string `json:"decision_by"`
	DecisionReason       string `json:"decision_reason"`
	Replayed             bool   `json:"replayed"`
}

// DisposeSourceClaim records only an explicitly saved reject or audit_only.
// It sends the exact saved subject directly, including after an uncertain
// write. A pending-only review reload would prevent valid terminal replay.
func DisposeSourceClaim(ctx context.Context, command string, review SourceClaimReview, decision, reason string) (ReviewedDisposition, error) {
	if ctx == nil {
		return ReviewedDisposition{}, errors.New("review context is required")
	}
	p, err := validateReviewEnvelope(review)
	if err != nil {
		return ReviewedDisposition{}, err
	}
	if decision != "reject" && decision != "audit_only" {
		return ReviewedDisposition{}, errors.New("review disposition requires explicit reject or audit_only")
	}
	if !validText(reason, 2000) || strings.TrimSpace(reason) != reason {
		return ReviewedDisposition{}, errors.New("review reason requires 1 to 2000 normalized UTF-8 bytes without NUL")
	}
	c, err := startReviewer(ctx, command)
	if err != nil {
		return ReviewedDisposition{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return ReviewedDisposition{}, err
	}
	request := struct {
		ExtractionAttemptID string        `json:"extraction_attempt_id"`
		ExpectedSubject     ReviewSubject `json:"expected_subject"`
		Decision            string        `json:"decision"`
		DecisionReason      string        `json:"decision_reason"`
	}{review.SubmissionReceipt.ExtractionAttemptID, review.Subject, decision, reason}
	var result ReviewedDisposition
	if err := c.call("record_reviewed_source_claim_disposition", request, &result); err != nil {
		return ReviewedDisposition{}, fmt.Errorf("reviewed disposition outcome uncertain; preserve the exact decision for retry: %w", err)
	}
	if err := validateReviewedDisposition(result, p.ProposalBasis.ProposalOccurrenceID, decision, reason); err != nil {
		return ReviewedDisposition{}, err
	}
	if err := c.Close(); err != nil {
		return ReviewedDisposition{}, errors.New("reviewed disposition acknowledged but launcher exit failed; preserve exact decision for retry")
	}
	return result, nil
}

func validateReviewedDisposition(result ReviewedDisposition, occurrenceID, decision, reason string) error {
	outcome := decision
	if decision == "reject" {
		outcome = "rejected"
	}
	if (outcome != "rejected" && outcome != "audit_only") || result.AdmissionOutcome != outcome ||
		result.ProposalOccurrenceID != occurrenceID || !reviewID(result.AdmissionDecisionID, "adm:") ||
		!validText(result.DecisionBy, 200) || strings.TrimSpace(result.DecisionBy) != result.DecisionBy || result.DecisionReason != reason {
		return errors.New("reviewed disposition acknowledgement differs from the exact request; write outcome uncertain")
	}
	return nil
}

// VerifyDisposed independently checks the exact occurrence/source and terminal
// noncanonical state. Query does not expose the persisted review binding or
// decision reason, and this function does not claim to authenticate a person.
func VerifyDisposed(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, handoff Handoff, disposition ReviewedDisposition) error {
	if ctx == nil {
		return errors.New("disposition Query context is required")
	}
	if err := validateReviewInputs(sourceID, document, extractor, records, handoff); err != nil {
		return err
	}
	decision := disposition.AdmissionOutcome
	if decision == "rejected" {
		decision = "reject"
	}
	if err := validateReviewedDisposition(disposition, handoff.ProposalOccurrenceID, decision, disposition.DecisionReason); err != nil {
		return err
	}
	c, err := startQuery(ctx, command)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return err
	}
	var proposal pendingRecord
	if err := c.call("get_evidence_record", map[string]string{"proposal_occurrence_id": handoff.ProposalOccurrenceID}, &proposal); err != nil {
		return err
	}
	if proposal.AdmissionOutcome != disposition.AdmissionOutcome || string(proposal.CanonicalRef) != "null" {
		return errors.New("Query did not verify the exact noncanonical disposition")
	}
	// Verify the same immutable material without weakening pending-only callers.
	proposal.AdmissionOutcome, proposal.CanonicalRef = "pending", json.RawMessage("null")
	if err := verifyPendingRecord(proposal, sourceID, document, extractor, records[0], handoff); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return errors.New("disposition Query launcher did not exit cleanly")
	}
	return nil
}
