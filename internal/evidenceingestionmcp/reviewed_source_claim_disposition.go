package evidenceingestionmcp

import (
	"context"
	"encoding/json"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

// ToolRecordReviewedSourceClaimDisposition records one exact reviewed rejection
// or audit-only outcome without admitting canonical evidence.
const ToolRecordReviewedSourceClaimDisposition = "record_reviewed_source_claim_disposition"

// RecordReviewedSourceClaimDispositionDescription is the public bounded contract.
const RecordReviewedSourceClaimDispositionDescription = "After an explicit human reject or audit_only decision on the complete get_source_claim_review display, record exactly that noncanonical terminal outcome using the unchanged expected_subject and a decision_reason. Reviewer identity comes only from the authorized launcher. AHE atomically reconstructs the exact display and binds the decision, reviewer, reason and display. Exact replay requires all original inputs; changed inputs conflict. No canonical node, edge or admission manifest is created. A saved display or agent assignment is not human approval."

// RecordReviewedSourceClaimDispositionRequest accepts no caller identity or graph.
type RecordReviewedSourceClaimDispositionRequest struct {
	ExtractionAttemptID string                                        `json:"extraction_attempt_id"`
	ExpectedSubject     evidenceingestion.ExactDisplayedReviewSubject `json:"expected_subject"`
	Decision            string                                        `json:"decision"`
	DecisionReason      string                                        `json:"decision_reason"`
}

// RecordReviewedSourceClaimDispositionResponse contains no canonical authority.
type RecordReviewedSourceClaimDispositionResponse = RecordPendingProposalDispositionResponse

type sourceReviewDispositionCore interface {
	RecordReviewedSourceClaimDisposition(context.Context, evidenceingestion.ReviewedSourceClaimDispositionInput) (evidenceingestion.ProposalDispositionResult, error)
}

// RecordReviewedSourceClaimDisposition delegates exact review and replay to one
// atomic native writer; it never reloads a pending-only review before replay.
func (s *Server) RecordReviewedSourceClaimDisposition(ctx context.Context, req RecordReviewedSourceClaimDispositionRequest) (RecordReviewedSourceClaimDispositionResponse, error) {
	if ctx == nil {
		return RecordReviewedSourceClaimDispositionResponse{}, invalidSourceReviewRequest("context is required")
	}
	if err := s.requireSourceClaimReviewer(); err != nil {
		return RecordReviewedSourceClaimDispositionResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return RecordReviewedSourceClaimDispositionResponse{}, mapToolError(err)
	}
	outcome := evidenceingestion.ProposalDispositionAuditOnly
	switch req.Decision {
	case "reject":
		outcome = evidenceingestion.ProposalDispositionRejected
	case "audit_only":
	default:
		return RecordReviewedSourceClaimDispositionResponse{}, invalidSourceReviewRequest("decision must explicitly be reject or audit_only")
	}
	if err := validateSourceClaimAdmissionRequest(AdmitReviewedSourceClaimRequest{ExtractionAttemptID: req.ExtractionAttemptID,
		ExpectedSubject: req.ExpectedSubject, Decision: "approved", DecisionReason: req.DecisionReason}); err != nil {
		return RecordReviewedSourceClaimDispositionResponse{}, err
	}
	core, ok := s.sourceReview.(sourceReviewDispositionCore)
	if !ok {
		return RecordReviewedSourceClaimDispositionResponse{}, sourceReviewUnavailable()
	}
	result, err := core.RecordReviewedSourceClaimDisposition(ctx, evidenceingestion.ReviewedSourceClaimDispositionInput{
		ExtractionAttemptID: req.ExtractionAttemptID, ExpectedSubject: req.ExpectedSubject, Outcome: outcome,
		DecisionBy: s.reviewPrincipal.ID, DecisionReason: req.DecisionReason,
	})
	if err != nil {
		return RecordReviewedSourceClaimDispositionResponse{}, mapToolError(err)
	}
	return RecordReviewedSourceClaimDispositionResponse{ProposalOccurrenceID: result.ProposalOccurrenceID, AdmissionDecisionID: result.AdmissionDecisionID,
		AdmissionOutcome: result.AdmissionOutcome, DecisionBy: result.DecisionBy, DecisionReason: result.DecisionReason, Replayed: result.Replayed}, nil
}

func (c postgresCore) RecordReviewedSourceClaimDisposition(ctx context.Context, input evidenceingestion.ReviewedSourceClaimDispositionInput) (evidenceingestion.ProposalDispositionResult, error) {
	return evidenceingestion.RecordReviewedSourceClaimDisposition(ctx, c.pool, input)
}

func (s *Server) callSourceReviewDispositionTool(ctx context.Context, payload []byte) ([]byte, error) {
	if err := s.requireSourceClaimReviewer(); err != nil {
		return nil, err
	}
	var req RecordReviewedSourceClaimDispositionRequest
	if err := decodeBoundedReviewRequest(payload, &req, "source-claim disposition"); err != nil {
		return nil, err
	}
	response, err := s.RecordReviewedSourceClaimDisposition(ctx, req)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, mapToolError(err)
	}
	return encoded, nil
}
