package evidenceingestionmcp

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ToolGetSourceClaimReview returns one exact display and its full batch manifest.
	ToolGetSourceClaimReview = "get_source_claim_review"
	// ToolAdmitReviewedSourceClaim admits an explicitly approved exact display subject.
	ToolAdmitReviewedSourceClaim = "admit_reviewed_source_claim"
	// SourceClaimReviewMCPContract versions this bounded transport only.
	SourceClaimReviewMCPContract = "source-claim-review-mcp/v1"
	// SourceClaimReviewMaxManifestEntries bounds a complete response, not a page.
	SourceClaimReviewMaxManifestEntries = 205
	// SourceClaimReviewMaxResponseBytes bounds the complete encoded tool response.
	SourceClaimReviewMaxResponseBytes = 1 << 20
	// GetSourceClaimReviewDescription is shared by typed and stdio registries.
	GetSourceClaimReviewDescription = "Read one pending source-backed statement review from one PostgreSQL snapshot. Returns its submission receipt, complete batch manifest (at most 205 entries; no pagination or truncation), exact display bytes, and subject. Show the complete proposal sentence, exact source quotations, source title/location/revision, coverage and limitations; preserve the subject unchanged. Other manifest entries are discovery coordinates, not an approval of the batch. No write or approval occurs. Responses exceeding 1 MiB fail closed. Display is cooperating-agent behavior, not proof that a human read it."
	// AdmitReviewedSourceClaimDescription is shared by typed and stdio registries.
	AdmitReviewedSourceClaimDescription = "After explicit approval of the complete get_source_claim_review display, admit exactly its source-backed statement using decision=approved and the unchanged expected_subject. Reviewer identity comes only from the authorized launcher. The native writer reconstructs and compares display identity atomically with canonical writes; exact subject, reviewer and reason replay only. Do not send a caller receipt, reviewer, request/session ID, graph, derivation, or complete flag. Reject and audit-only use the separate exact-reviewed disposition writer. Legacy admission and independent relation writers remain disabled in this profile. This is not authenticated human review or universal database cutover."
)

// GetSourceClaimReviewRequest identifies one pending member of an exact batch.
type GetSourceClaimReviewRequest struct {
	ExtractionAttemptID  string `json:"extraction_attempt_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
}

// GetSourceClaimReviewResponse carries the full manifest and exact display bytes.
type GetSourceClaimReviewResponse struct {
	ContractVersion   string                                             `json:"contract_version"`
	SubmissionReceipt evidenceingestion.SubmissionReceipt                `json:"submission_receipt"`
	ProposalManifest  evidenceingestion.ProposalBatchManifest            `json:"proposal_manifest"`
	Display           evidenceingestion.SourceClaimReviewDisplayArtifact `json:"display"`
	Subject           evidenceingestion.ExactDisplayedReviewSubject      `json:"subject"`
}

// AdmitReviewedSourceClaimRequest binds approval to the displayed subject only.
type AdmitReviewedSourceClaimRequest struct {
	ExtractionAttemptID string                                        `json:"extraction_attempt_id"`
	ExpectedSubject     evidenceingestion.ExactDisplayedReviewSubject `json:"expected_subject"`
	Decision            string                                        `json:"decision"`
	DecisionReason      string                                        `json:"decision_reason"`
}

// AdmitReviewedSourceClaimResponse preserves the existing snake_case wire result.
type AdmitReviewedSourceClaimResponse = AdmitPendingProposalResponse

type sourceReviewCore interface {
	LoadReviewableSourceClaimReviewSnapshot(context.Context, string, string) (evidenceingestion.ReviewableSourceClaimReviewSnapshot, error)
	AdmitReviewedSourceClaim(context.Context, evidenceingestion.ReviewedSourceClaimAdmissionInput) (evidenceingestion.AdmissionResult, error)
}

// GetSourceClaimReview emits one bounded complete review without persisting a
// preview, approval token or decision. Its display is built from the same
// snapshot as its receipt and manifest, not another independently timed read.
func (s *Server) GetSourceClaimReview(ctx context.Context, req GetSourceClaimReviewRequest) (GetSourceClaimReviewResponse, error) {
	if ctx == nil {
		return GetSourceClaimReviewResponse{}, invalidSourceReviewRequest("context is required")
	}
	if err := ctx.Err(); err != nil {
		return GetSourceClaimReviewResponse{}, mapToolError(err)
	}
	if err := validateSourceReviewID("extraction_attempt_id", req.ExtractionAttemptID, "attempt:"); err != nil {
		return GetSourceClaimReviewResponse{}, err
	}
	if err := validateSourceReviewID("proposal_occurrence_id", req.ProposalOccurrenceID, "occ:"); err != nil {
		return GetSourceClaimReviewResponse{}, err
	}
	if s == nil || s.sourceReview == nil {
		return GetSourceClaimReviewResponse{}, sourceReviewUnavailable()
	}
	snapshot, err := s.sourceReview.LoadReviewableSourceClaimReviewSnapshot(ctx, req.ExtractionAttemptID, req.ProposalOccurrenceID)
	if err != nil {
		return GetSourceClaimReviewResponse{}, mapToolError(err)
	}
	if snapshot.SubmissionReceipt.ExtractionAttemptID != req.ExtractionAttemptID || snapshot.ExactReviewSubject.ProposalOccurrenceID != req.ProposalOccurrenceID ||
		snapshot.ProposalManifest.Entries == nil || len(snapshot.ProposalManifest.Entries) > SourceClaimReviewMaxManifestEntries || snapshot.ProposalManifest.ProposalCount != len(snapshot.ProposalManifest.Entries) {
		return GetSourceClaimReviewResponse{}, invalidSourceReviewRequest("review must contain the exact requested subject and complete manifest within 205 entries")
	}
	display, subject, err := evidenceingestion.BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		return GetSourceClaimReviewResponse{}, mapToolError(err)
	}
	response := GetSourceClaimReviewResponse{ContractVersion: SourceClaimReviewMCPContract, SubmissionReceipt: snapshot.SubmissionReceipt, ProposalManifest: snapshot.ProposalManifest, Display: display, Subject: subject}
	if _, err := marshalSourceClaimReviewResponse(response); err != nil {
		return GetSourceClaimReviewResponse{}, err
	}
	return response, nil
}

// AdmitReviewedSourceClaim deliberately does not call the pending-only review
// loader first: the existing atomic native writer owns both the fresh-subject
// check and exact replay for an already admitted proposal.
func (s *Server) AdmitReviewedSourceClaim(ctx context.Context, req AdmitReviewedSourceClaimRequest) (AdmitReviewedSourceClaimResponse, error) {
	if ctx == nil {
		return AdmitReviewedSourceClaimResponse{}, invalidSourceReviewRequest("context is required")
	}
	if err := s.requireSourceClaimReviewer(); err != nil {
		return AdmitReviewedSourceClaimResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return AdmitReviewedSourceClaimResponse{}, mapToolError(err)
	}
	if err := validateSourceClaimAdmissionRequest(req); err != nil {
		return AdmitReviewedSourceClaimResponse{}, err
	}
	if s.sourceReview == nil {
		return AdmitReviewedSourceClaimResponse{}, sourceReviewUnavailable()
	}
	result, err := s.sourceReview.AdmitReviewedSourceClaim(ctx, evidenceingestion.ReviewedSourceClaimAdmissionInput{ExtractionAttemptID: req.ExtractionAttemptID, ExpectedSubject: req.ExpectedSubject, DecisionBy: s.reviewPrincipal.ID, DecisionReason: req.DecisionReason})
	if err != nil {
		return AdmitReviewedSourceClaimResponse{}, mapToolError(err)
	}
	return AdmitReviewedSourceClaimResponse{
		ProposalOccurrenceID: result.ProposalOccurrenceID,
		AdmissionDecisionID:  result.AdmissionDecisionID,
		AdmissionOutcome:     result.AdmissionOutcome,
		CanonicalRef:         result.CanonicalRef,
		RawEvidenceNodeIDs:   append([]string{}, result.RawEvidenceNodeIDs...),
		CanonicalEdgeIDs:     append([]string{}, result.CanonicalEdgeIDs...),
		DerivationID:         result.DerivationID,
		ParentNodeIDs:        append([]string{}, result.ParentNodeIDs...),
		Replayed:             result.Replayed,
	}, nil
}

func validateSourceClaimAdmissionRequest(req AdmitReviewedSourceClaimRequest) error {
	if req.Decision != "approved" {
		return invalidSourceReviewRequest("decision must be explicitly approved; noncanonical outcomes use the reviewed disposition writer")
	}
	if strings.TrimSpace(req.DecisionReason) != req.DecisionReason || req.DecisionReason == "" || !utf8.ValidString(req.DecisionReason) || strings.ContainsRune(req.DecisionReason, '\x00') || len(req.DecisionReason) > evidenceingestion.ProposalDispositionDecisionReasonMaxBytes {
		return invalidSourceReviewRequest("decision_reason must contain 1 to 2000 normalized UTF-8 bytes without NUL")
	}
	subject := req.ExpectedSubject.ReviewSubject
	for _, field := range []struct{ name, value, prefix string }{
		{"extraction_attempt_id", req.ExtractionAttemptID, "attempt:"}, {"proposal_occurrence_id", subject.ProposalOccurrenceID, "occ:"},
		{"submission_receipt_id", subject.SubmissionReceiptID, "submission-receipt:v1:sha256:"}, {"proposal_manifest_id", subject.ProposalManifestID, "proposal-manifest:v1:sha256:"},
		{"proposal_basis_id", subject.ProposalBasisID, "proposal-basis:v1:sha256:"}, {"review_package_id", subject.ReviewPackageID, "review-package:v1:sha256:"},
		{"review_display_artifact_id", req.ExpectedSubject.ReviewDisplayArtifactID, "review-display:v1:sha256:"},
	} {
		if err := validateSourceReviewID(field.name, field.value, field.prefix); err != nil {
			return err
		}
	}
	return nil
}

func validateSourceReviewID(name, value, prefix string) error {
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return invalidSourceReviewRequest(name + " requires its exact prefix and 64 lowercase hexadecimal characters")
	}
	for _, c := range value[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return invalidSourceReviewRequest(name + " requires lowercase hexadecimal characters")
		}
	}
	return nil
}

func (c postgresCore) LoadReviewableSourceClaimReviewSnapshot(ctx context.Context, attemptID, occurrenceID string) (evidenceingestion.ReviewableSourceClaimReviewSnapshot, error) {
	return evidenceingestion.LoadReviewableSourceClaimReviewSnapshot(ctx, c.pool, attemptID, occurrenceID)
}

func (c postgresCore) AdmitReviewedSourceClaim(ctx context.Context, input evidenceingestion.ReviewedSourceClaimAdmissionInput) (evidenceingestion.AdmissionResult, error) {
	return evidenceingestion.AdmitReviewedSourceClaim(ctx, c.pool, input)
}

func (s *Server) callSourceReviewTool(ctx context.Context, name string, payload []byte) ([]byte, error) {
	var response any
	var err error
	if name == ToolGetSourceClaimReview {
		var req GetSourceClaimReviewRequest
		if err := decodeBoundedReviewRequest(payload, &req, "source-claim review"); err != nil {
			return nil, err
		}
		response, err = s.GetSourceClaimReview(ctx, req)
	} else {
		if err := s.requireSourceClaimReviewer(); err != nil {
			return nil, err
		}
		var req AdmitReviewedSourceClaimRequest
		if err := decodeBoundedReviewRequest(payload, &req, "source-claim review"); err != nil {
			return nil, err
		}
		response, err = s.AdmitReviewedSourceClaim(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, mapToolError(err)
	}
	return encoded, nil
}

func sourceReviewUnavailable() error {
	return &ToolError{Code: toolErrorInternal, Message: "source-claim review adapter is not configured"}
}

// Bound the complete JSON envelope independently from the smaller canonical
// display payload. No prefix, page or partial response is returned on overflow.
func marshalSourceClaimReviewResponse(response GetSourceClaimReviewResponse) ([]byte, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, mapToolError(err)
	}
	if len(encoded) > SourceClaimReviewMaxResponseBytes {
		return nil, invalidSourceReviewRequest("complete source-claim review response exceeds 1 MiB; no partial display was returned")
	}
	return encoded, nil
}
func invalidSourceReviewRequest(message string) error {
	return &ToolError{Code: toolErrorInvalidRequest, Message: message}
}

// NewSourceClaimReviewerServer binds the reviewed writer to one validated
// launcher principal. The constructor does not authenticate a human decision.
func NewSourceClaimReviewerServer(pool *pgxpool.Pool, principal runtimeauth.Principal) (*Server, error) {
	validated, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	server, err := NewServer(pool)
	if err != nil {
		return nil, err
	}
	server.reviewPrincipal = validated
	return server, nil
}

// SourceClaimReviewerPrincipal reports the constructor binding for the outer
// closed-profile adapter to compare; tool arguments can never replace it.
func (s *Server) SourceClaimReviewerPrincipal() runtimeauth.Principal {
	if s == nil {
		return runtimeauth.Principal{}
	}
	return s.reviewPrincipal
}

func (s *Server) requireSourceClaimReviewer() error {
	if _, err := runtimeauth.NewPrincipal(s.SourceClaimReviewerPrincipal().ID); err != nil {
		return runtimeauth.NewUnauthenticatedError("reviewed source-claim writer requires a launcher-bound principal")
	}
	return nil
}
