package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewedEndpointDispositionV1 binds a noncanonical endpoint decision to its exact display.
const ReviewedEndpointDispositionV1 = "reviewed-endpoint-disposition/v1"

// EndpointDispositionReceipt preserves a terminal decision without a canonical mutation.
type EndpointDispositionReceipt struct {
	ContractVersion      string `json:"contract_version"`
	RequestID            string `json:"request_id"`
	ReviewSubject        string `json:"review_subject"`
	ReviewerID           string `json:"reviewer_id"`
	DecisionReason       string `json:"decision_reason"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	AdmissionDecisionID  string `json:"admission_decision_id"`
	AdmissionOutcome     string `json:"admission_outcome"`
	Replayed             bool   `json:"replayed"`
}

// RecordReviewedEndpointDisposition records an explicit reject or audit_only of one occurrence.
func RecordReviewedEndpointDisposition(ctx context.Context, pool *pgxpool.Pool, input ReviewedEndpointAdmissionInput) (EndpointDispositionReceipt, error) {
	if pool == nil {
		return EndpointDispositionReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := validateEndpointRequest(input.Review); err != nil {
		return EndpointDispositionReceipt{}, err
	}
	outcome := admissionOutcomeRejected
	if input.Decision == "audit_only" {
		outcome = admissionOutcomeAuditOnly
	} else if input.Decision != "reject" {
		return EndpointDispositionReceipt{}, newDomainError(ErrorInvalidInput, "endpoint disposition requires reject or audit_only")
	}
	if !validateEndpointText(input.RequestID, 200) || !validateEndpointText(input.ReviewerID, 200) || !validateEndpointText(input.DecisionReason, 2000) || !validateEndpointText(input.ExpectedSubject, 200) {
		return EndpointDispositionReceipt{}, newDomainError(ErrorInvalidInput, "exact subject, request ID, reviewer and human reason are required")
	}
	var result EndpointDispositionReceipt
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
				return err
			}
			p, err := loadProposalForAdmission(ctx, tx, input.Review.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			review, err := loadEndpointReview(ctx, tx, input.Review)
			if err != nil {
				return err
			}
			if review.Subject != input.ExpectedSubject {
				return newDomainError(ErrorReviewContractConflict, "endpoint review subject changed")
			}
			if p.AdmissionOutcome != admissionOutcomePending {
				r := review.Lifecycle.DispositionReceipt
				if r == nil || r.RequestID != input.RequestID || r.ReviewerID != input.ReviewerID || r.DecisionReason != input.DecisionReason || r.AdmissionOutcome != outcome {
					return newDomainError(ErrorAdmissionStateConflict, "endpoint disposition replay differs")
				}
				result = *r
				result.Replayed = true
				return nil
			}
			id, err := stableID("adm:", ReviewedEndpointDispositionV1, struct{ Proposal, Outcome string }{p.ProposalOccurrenceID, outcome})
			if err != nil {
				return err
			}
			if err := reserveEndpointReviewRequest(ctx, tx, input.RequestID, p.ProposalOccurrenceID, id); err != nil {
				return err
			}
			if _, err = tx.exec(ctx, `INSERT INTO admission_decisions (admission_decision_id,proposal_occurrence_id,outcome,canonical_ref,raw_evidence_node_ids,canonical_edge_ids,decision_by,decision_reason,disposition_review_binding_contract_version)
VALUES ($1,$2,$3,NULL,'[]'::jsonb,'[]'::jsonb,$4,$5,$6)`, id, p.ProposalOccurrenceID, outcome, input.ReviewerID, input.DecisionReason, ReviewedEndpointDispositionV1); err != nil {
				return err
			}
			if err = markProposalDisposition(ctx, tx, p.ProposalOccurrenceID, outcome); err != nil {
				return err
			}
			result = EndpointDispositionReceipt{ContractVersion: ReviewedEndpointDispositionV1, RequestID: input.RequestID, ReviewSubject: review.Subject, ReviewerID: input.ReviewerID, DecisionReason: input.DecisionReason, ProposalOccurrenceID: p.ProposalOccurrenceID, AdmissionDecisionID: id, AdmissionOutcome: outcome}
			payload, err := deterministicJSON(review.Display)
			if err != nil {
				return err
			}
			receipt, err := deterministicJSON(result)
			if err != nil {
				return err
			}
			_, err = tx.exec(ctx, `INSERT INTO endpoint_disposition_review_bindings (admission_decision_id,proposal_occurrence_id,request_id,review_subject,display_payload,receipt_payload) VALUES ($1,$2,$3,$4,$5,$6::jsonb)`, id, p.ProposalOccurrenceID, input.RequestID, review.Subject, string(payload), string(receipt))
			return err
		})
	})
	if err != nil {
		return EndpointDispositionReceipt{}, err
	}
	return result, err
}

func reserveEndpointReviewRequest(ctx context.Context, tx sqlTx, request, proposal, decision string) error {
	var inserted string
	err := tx.queryRow(ctx, `INSERT INTO endpoint_review_requests (request_id,proposal_occurrence_id,admission_decision_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING RETURNING request_id`, request, proposal, decision).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return newDomainError(ErrorReviewContractConflict, "endpoint request ID or occurrence is already bound")
	}
	if err != nil {
		return err
	}
	return nil
}

func loadEndpointDispositionReview(ctx context.Context, db sqlQueryer, p ProposalQueryResult, req *EndpointReviewRequest) (EndpointReview, error) {
	var payload string
	var raw []byte
	err := db.queryRow(ctx, `SELECT b.display_payload,b.receipt_payload FROM endpoint_disposition_review_bindings b JOIN admission_decisions d USING(admission_decision_id)
WHERE b.proposal_occurrence_id=$1 AND d.outcome=$2 AND d.disposition_review_binding_contract_version=$3 AND d.canonical_ref IS NULL
AND b.receipt_payload->>'reviewer_id'=d.decision_by AND b.receipt_payload->>'decision_reason'=d.decision_reason
AND b.receipt_payload->>'admission_decision_id'=d.admission_decision_id AND b.receipt_payload->>'request_id'=b.request_id AND b.receipt_payload->>'review_subject'=b.review_subject`, p.ProposalOccurrenceID, p.AdmissionOutcome, ReviewedEndpointDispositionV1).Scan(&payload, &raw)
	if err != nil {
		return EndpointReview{}, newDomainError(ErrorAdmissionStateConflict, "terminal proposal has no valid endpoint disposition receipt")
	}
	var display EndpointReviewDisplay
	var receipt EndpointDispositionReceipt
	if err = json.Unmarshal([]byte(payload), &display); err != nil {
		return EndpointReview{}, err
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return EndpointReview{}, err
	}
	if display.ContractVersion != EndpointReviewV1 || receipt.ContractVersion != ReviewedEndpointDispositionV1 || receipt.ReviewSubject != "endpoint-review:sha256:"+hashHex([]byte(payload)) || display.Request.ProposalOccurrenceID != p.ProposalOccurrenceID || receipt.ProposalOccurrenceID != p.ProposalOccurrenceID || receipt.AdmissionOutcome != p.AdmissionOutcome || p.CanonicalRef != "" || receipt.Replayed || display.Proposal.ProposalOccurrenceID != p.ProposalOccurrenceID || display.Proposal.ExtractionAttemptID != p.ExtractionAttemptID || display.Proposal.ProposalFingerprint != p.ProposalFingerprint || display.Proposal.StatementText != p.StatementText || !reflect.DeepEqual(display.Proposal.SourceRefs, p.SourceRefs) {
		return EndpointReview{}, newDomainError(ErrorReviewContractConflict, "endpoint disposition authority differs")
	}
	if req != nil && !reflect.DeepEqual(*req, display.Request) {
		return EndpointReview{}, newDomainError(ErrorReviewContractConflict, "endpoint disposition request differs from original review")
	}
	return EndpointReview{Subject: receipt.ReviewSubject, Display: display, Lifecycle: EndpointReviewLifecycle{Mode: "exact_replay_only", AdmissionOutcome: p.AdmissionOutcome, DispositionReceipt: &receipt}}, nil
}
