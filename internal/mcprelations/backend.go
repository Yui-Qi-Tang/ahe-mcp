// Package mcprelations exposes independently reviewed navigation relations.
// It never admits nodes, runs collectors, or delegates arbitrary graph writes.
package mcprelations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceimplements"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencereferences"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ToolGetImplementsReview     = "get_implements_review"
	ToolAdmitReviewedImplements = "admit_reviewed_implements"
	ToolGetReferencesReview     = "get_references_review"
	ToolAdmitReviewedReferences = "admit_reviewed_references"
)

// Backend binds all decisions to one launcher identity. This records the
// cooperating reviewer's assertion; it does not authenticate human attention.
type Backend struct {
	pool      *pgxpool.Pool
	principal runtimeauth.Principal
}

func NewBackend(pool *pgxpool.Pool, principal runtimeauth.Principal) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("relation reviewer requires PostgreSQL")
	}
	p, err := runtimeauth.NewPrincipal(principal.ID)
	if err != nil {
		return nil, err
	}
	return &Backend{pool: pool, principal: p}, nil
}

type ImplementsReviewResponse struct {
	Request evidenceimplements.RecursiveReviewRequest `json:"review"`
	Subject evidenceimplements.DerivedReviewSubject   `json:"subject"`
	Display evidenceimplements.DerivedReviewDisplay   `json:"display"`
}

// ImplementsAdmissionRequest binds the approved complete recursive display.
// No caller-issued receipt, source map, admitted cut, or reviewer is accepted.
type ImplementsAdmissionRequest struct {
	RequestID       string                                    `json:"request_id"`
	Review          evidenceimplements.RecursiveReviewRequest `json:"review"`
	ExpectedSubject evidenceimplements.DerivedReviewSubject   `json:"expected_subject"`
	Decision        string                                    `json:"decision"`
	DecisionReason  string                                    `json:"decision_reason"`
}

type ReferencesAdmissionRequest struct {
	RequestID       string                           `json:"request_id"`
	Review          evidencereferences.ReviewRequest `json:"review"`
	ExpectedSubject evidencereferences.ReviewSubject `json:"expected_subject"`
	Decision        string                           `json:"decision"`
	DecisionReason  string                           `json:"decision_reason"`
}

func (b *Backend) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.principal.ID == "" {
		return nil, runtimeauth.NewUnauthenticatedError("relation reviewer identity is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("relation review context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var value any
	var err error
	switch name {
	case ToolGetImplementsReview:
		var req evidenceimplements.RecursiveReviewRequest
		if err = decodeRequest(args, &req); err == nil {
			var review evidenceimplements.RecursiveReview
			review, err = evidenceimplements.LoadRecursiveReview(ctx, b.pool, req)
			if err == nil {
				// Only exact display, request and subject cross the transport. The native
				// graph/candidate packet stays DB-owned and is reconstructed on admission.
				value = ImplementsReviewResponse{review.Request, review.Subject, review.Display}
			}
		}
	case ToolAdmitReviewedImplements:
		var req ImplementsAdmissionRequest
		if err = decodeRequest(args, &req); err != nil {
			break
		}
		if err = validateApproval(req.RequestID, req.Decision, req.DecisionReason); err != nil {
			break
		}
		var review evidenceimplements.RecursiveReview
		review, err = evidenceimplements.LoadRecursiveReview(ctx, b.pool, req.Review)
		if err != nil {
			break
		}
		if review.Subject != req.ExpectedSubject {
			return nil, fmt.Errorf("%w: exact implements subject changed", evidenceimplements.ErrReplayConflict)
		}
		var receipt evidenceimplements.DerivedReviewReceipt
		receipt, err = evidenceimplements.NewRecursiveReviewReceipt(review.Cut, review.Report, review.Basis, evidenceimplements.DerivedReviewInput{
			CandidateID: review.Subject.CandidateID, Mapping: review.Request.Mapping, Display: review.Display, Subject: review.Subject,
			ReviewerID: b.principal.ID, DecisionReason: req.DecisionReason})
		if err != nil {
			break
		}
		// The native writer reloads again inside its write transaction; the
		// preceding read does not weaken atomic stale-subject detection.
		value, err = evidenceimplements.AdmitReviewedRecursive(ctx, b.pool, evidenceimplements.RecursiveAdmissionInput{
			RequestID: req.RequestID, Review: req.Review, Receipt: receipt, Decision: req.Decision})
	case ToolGetReferencesReview:
		var req evidencereferences.ReviewRequest
		if err = decodeRequest(args, &req); err == nil {
			value, err = evidencereferences.LoadReview(ctx, b.pool, req)
		}
	case ToolAdmitReviewedReferences:
		var req ReferencesAdmissionRequest
		if err = decodeRequest(args, &req); err != nil {
			break
		}
		if err = validateApproval(req.RequestID, req.Decision, req.DecisionReason); err != nil {
			break
		}
		value, err = evidencereferences.AdmitReviewed(ctx, b.pool, evidencereferences.AdmissionInput{
			RequestID: req.RequestID, Review: req.Review, ExpectedSubject: req.ExpectedSubject,
			Decision: req.Decision, DecisionReason: req.DecisionReason, ReviewerID: b.principal.ID})
	default:
		return nil, runtimeauth.NewUnauthorizedError("operation is unavailable in relation-reviewer")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func validateApproval(requestID, decision, reason string) error {
	if decision != "approved" {
		return errors.New("explicit decision=approved is required for this exact relation; node approval is insufficient")
	}
	for _, f := range []struct {
		s   string
		max int
	}{{requestID, 200}, {reason, 2000}} {
		if f.s == "" || len(f.s) > f.max || strings.TrimSpace(f.s) != f.s || !utf8.ValidString(f.s) || strings.ContainsRune(f.s, 0) {
			return errors.New("request_id and decision_reason must be nonempty bounded normalized text")
		}
	}
	return nil
}
