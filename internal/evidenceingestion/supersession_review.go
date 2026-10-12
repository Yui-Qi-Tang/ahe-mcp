package evidenceingestion

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SupersessionReviewV1 identifies the complete fresh-v2 replacement display.
const SupersessionReviewV1 = "supersession-review/v1"

// ReviewedSupersessionAdmissionV1 identifies a launcher-bound exact-reviewed replacement.
const ReviewedSupersessionAdmissionV1 = "reviewed-supersession-admission/v1"

// SupersessionReviewRequest binds the complete replacement set and native CAS coordinate.
type SupersessionReviewRequest struct {
	ProposalOccurrenceID string                   `json:"proposal_occurrence_id"`
	Basis                SupersessionLineageBasis `json:"basis"`
	TargetNodeIDs        []string                 `json:"target_node_ids"`
	ExpectedRevision     int64                    `json:"expected_revision"`
	ExpectedHeadEventID  string                   `json:"expected_head_event_id,omitempty"`
}

// SupersessionReviewDisplay retains exact new and old grounding, without inferring semantic replacement.
type SupersessionReviewDisplay struct {
	ContractVersion   string                                `json:"contract_version"`
	Request           SupersessionReviewRequest             `json:"request"`
	Proposal          ProposalQueryResult                   `json:"proposal"`
	SourceBasis       ReviewableSourceClaimReviewSnapshot   `json:"source_basis"`
	Targets           []CanonicalQueryResult                `json:"targets"`
	TargetSourceBases []ReviewableSourceClaimReviewSnapshot `json:"target_source_bases"`
	Limitations       string                                `json:"limitations"`
}

// SupersessionReview distinguishes a new decision from immutable terminal replay.
type SupersessionReview struct {
	Lifecycle SupersessionReviewLifecycle `json:"lifecycle"`
	Subject   string                      `json:"subject"`
	Display   SupersessionReviewDisplay   `json:"display"`
}

// SupersessionReviewLifecycle describes current state separately from the immutable display.
type SupersessionReviewLifecycle struct {
	Mode             string                     `json:"mode"`
	AdmissionOutcome string                     `json:"admission_outcome"`
	Receipt          *SupersessionReviewReceipt `json:"receipt,omitempty"`
}

// SupersessionReviewReceipt records the exact review and native atomic event.
type SupersessionReviewReceipt struct {
	ContractVersion string                      `json:"contract_version"`
	RequestID       string                      `json:"request_id"`
	ReviewSubject   string                      `json:"review_subject"`
	ReviewerID      string                      `json:"reviewer_id"`
	DecisionReason  string                      `json:"decision_reason"`
	Admission       SupersessionAdmissionResult `json:"admission"`
}

// ReviewedSupersessionInput carries the returned native request, never caller-derived authority.
type ReviewedSupersessionInput struct {
	RequestID       string                    `json:"request_id"`
	Review          SupersessionReviewRequest `json:"review"`
	ExpectedSubject string                    `json:"expected_subject"`
	Decision        string                    `json:"decision"`
	DecisionReason  string                    `json:"decision_reason"`
	ReviewerID      string                    `json:"-"`
}

func supersessionNativeInput(req SupersessionReviewRequest, by, reason string) SupersessionAdmissionInput {
	return SupersessionAdmissionInput{ProposalOccurrenceID: req.ProposalOccurrenceID, Basis: req.Basis, TargetNodeIDs: req.TargetNodeIDs, ExpectedRevision: req.ExpectedRevision, ExpectedHeadEventID: req.ExpectedHeadEventID, DecisionBy: by, DecisionReason: reason}
}

func loadSupersessionReviewReceipt(ctx context.Context, db sqlQueryer, proposalID string) (*SupersessionReviewReceipt, error) {
	var raw []byte
	err := db.queryRow(ctx, `SELECT (SELECT b.receipt_payload FROM supersession_review_bindings b
JOIN admission_decisions d USING(admission_decision_id)
JOIN canonical_supersession_admission_events e ON e.event_id=b.event_id AND e.admission_decision_id=d.admission_decision_id
WHERE b.proposal_occurrence_id=$1 AND d.supersession_review_binding_contract_version='reviewed-supersession-admission/v1')`, proposalID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var result SupersessionReviewReceipt
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.ContractVersion != ReviewedSupersessionAdmissionV1 || result.Admission.ProposalOccurrenceID != proposalID {
		return nil, newDomainError(ErrorReviewContractConflict, "supersession receipt authority differs")
	}
	return &result, nil
}

// LoadSupersessionReview reconstructs one complete review in a read-only repeatable-read cut.
// The getter derives the head coordinate; its caller selects only the proposal, basis and targets.
func LoadSupersessionReview(ctx context.Context, pool *pgxpool.Pool, req SupersessionReviewRequest) (SupersessionReview, error) {
	if pool == nil {
		return SupersessionReview{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	var out SupersessionReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		p, err := getProposalByOccurrenceID(ctx, tx, req.ProposalOccurrenceID)
		if err != nil {
			return err
		}
		if p.AdmissionOutcome == admissionOutcomeAdmitted {
			out, err = loadSupersessionRecordedReview(ctx, tx, p, req, false)
			return err
		}
		head, err := loadCanonicalSupersessionHead(ctx, tx, false)
		if err != nil {
			return err
		}
		req.ExpectedRevision = head.Revision
		req.ExpectedHeadEventID = head.HeadEventID
		out, err = loadSupersessionReview(ctx, tx, req)
		return err
	})
	return out, err
}

func loadSupersessionReview(ctx context.Context, tx sqlTx, req SupersessionReviewRequest) (SupersessionReview, error) {
	in, basis, err := normalizeSupersessionAdmissionInput(supersessionNativeInput(req, "review-loader", "read-only reconstruction"))
	if err != nil {
		return SupersessionReview{}, err
	}
	req.Basis = in.Basis
	req.TargetNodeIDs = in.TargetNodeIDs
	p, err := getProposalByOccurrenceID(ctx, tx, req.ProposalOccurrenceID)
	if err != nil {
		return SupersessionReview{}, err
	}
	if p.AdmissionOutcome != admissionOutcomePending || p.ExtractionAttemptStatus != attemptStatusSucceeded || p.ProposalKind != ProposalKindStatement {
		return SupersessionReview{}, newDomainError(ErrorAdmissionStateConflict, "fresh pending succeeded external statement is required")
	}
	if err = validateProposalSupersessionBasis(p, basis); err != nil {
		return SupersessionReview{}, err
	}
	head, err := loadCanonicalSupersessionHead(ctx, tx, false)
	if err != nil {
		return SupersessionReview{}, err
	}
	if head.Revision != req.ExpectedRevision || head.HeadEventID != req.ExpectedHeadEventID {
		return SupersessionReview{}, newDomainError(ErrorSupersessionHeadConflict, "supersession head changed; obtain a new review")
	}
	if err = endpointSourcePreflight(ctx, tx, p.ExtractionAttemptID); err != nil {
		return SupersessionReview{}, err
	}
	source, err := loadSourceClaimReviewSnapshotForLifecycleInTx(ctx, tx, p.ExtractionAttemptID, p.ProposalOccurrenceID, "")
	if err != nil {
		return SupersessionReview{}, err
	}
	records, err := loadSupersessionTargetRecords(ctx, tx, req.TargetNodeIDs, "read")
	if err != nil {
		return SupersessionReview{}, err
	}
	key, err := basis.Key()
	if err != nil {
		return SupersessionReview{}, err
	}
	if _, err = validateSupersessionTargets(records, req.TargetNodeIDs, key, basis); err != nil {
		return SupersessionReview{}, err
	}
	display := SupersessionReviewDisplay{ContractVersion: SupersessionReviewV1, Request: req, Proposal: p, SourceBasis: source, Targets: []CanonicalQueryResult{}, TargetSourceBases: []ReviewableSourceClaimReviewSnapshot{}, Limitations: "Fresh-v2 external source_claim replacement only. Review the complete target set and stable semantic slot explicitly. Exact retained old/new revisions and excerpts are shown; revision ordering does not prove replacement, source freshness, semantic entailment or global truth. No inferred version difference or source coverage is supplied."}
	for _, id := range req.TargetNodeIDs {
		node, err := getCanonicalEvidenceByID(ctx, tx, id)
		if err != nil {
			return SupersessionReview{}, err
		}
		if err = endpointSourcePreflight(ctx, tx, node.OriginProposal.ExtractionAttemptID); err != nil {
			return SupersessionReview{}, err
		}
		old, err := loadSourceClaimReviewSnapshotForLifecycleInTx(ctx, tx, node.OriginProposal.ExtractionAttemptID, node.OriginProposalOccurrenceID, node.CanonicalID)
		if err != nil {
			return SupersessionReview{}, err
		}
		display.Targets = append(display.Targets, node)
		display.TargetSourceBases = append(display.TargetSourceBases, old)
		payload, err := deterministicJSON(display)
		if err != nil {
			return SupersessionReview{}, err
		}
		if len(payload) > endpointReviewMaxBytes {
			return SupersessionReview{}, newDomainError(ErrorInvalidInput, "supersession display exceeds one MiB; reduce review scope")
		}
	}
	payload, err := deterministicJSON(display)
	if err != nil {
		return SupersessionReview{}, err
	}
	return SupersessionReview{Subject: "supersession-review:sha256:" + hashHex(payload), Display: display, Lifecycle: SupersessionReviewLifecycle{Mode: "pending_admission", AdmissionOutcome: p.AdmissionOutcome}}, nil
}

// AdmitReviewedSupersession atomically validates exact review, writes fresh-v2, and persists its receipt.
func AdmitReviewedSupersession(ctx context.Context, pool *pgxpool.Pool, input ReviewedSupersessionInput) (SupersessionReviewReceipt, error) {
	if pool == nil || input.Decision != "approved" || !validateEndpointText(input.RequestID, 200) || !validateEndpointText(input.ReviewerID, 200) || !validateEndpointText(input.DecisionReason, 2000) || !validateEndpointText(input.ExpectedSubject, 200) {
		return SupersessionReviewReceipt{}, newDomainError(ErrorInvalidInput, "explicit approval, exact subject, request ID, reviewer and reason are required")
	}
	in, basis, err := normalizeSupersessionAdmissionInput(supersessionNativeInput(input.Review, input.ReviewerID, input.DecisionReason))
	if err != nil {
		return SupersessionReviewReceipt{}, err
	}
	if !reflect.DeepEqual(in, supersessionNativeInput(input.Review, input.ReviewerID, input.DecisionReason)) {
		return SupersessionReviewReceipt{}, newDomainError(ErrorReviewContractConflict, "use the unchanged native review request")
	}
	var result SupersessionReviewReceipt
	err = runSupersessionAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
				return err
			}
			p, err := loadProposalForAdmission(ctx, tx, in.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			if p.AdmissionOutcome == admissionOutcomeAdmitted {
				review, err := loadSupersessionRecordedReview(ctx, tx, p, input.Review, true)
				if err != nil {
					return err
				}
				r := review.Lifecycle.Receipt
				if r.RequestID != input.RequestID || r.ReviewerID != input.ReviewerID || r.DecisionReason != input.DecisionReason || r.ReviewSubject != input.ExpectedSubject {
					return newDomainError(ErrorSupersessionReplayConflict, "supersession replay differs from original review")
				}
				result = *r
				result.Admission.Replayed = true
				return nil
			}
			review, err := loadSupersessionReview(ctx, tx, input.Review)
			if err != nil {
				return err
			}
			if review.Subject != input.ExpectedSubject {
				return newDomainError(ErrorReviewContractConflict, "supersession review subject changed")
			}
			// A unique request must not be rebound to another occurrence.
			var exists bool
			if err = tx.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supersession_review_bindings WHERE request_id=$1)`, input.RequestID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return newDomainError(ErrorSupersessionReplayConflict, "supersession request ID is already bound")
			}
			var admission SupersessionAdmissionResult
			if err = admitPendingSupersessionInTx(ctx, tx, in, basis, true, &admission); err != nil {
				return err
			}
			result = SupersessionReviewReceipt{ContractVersion: ReviewedSupersessionAdmissionV1, RequestID: input.RequestID, ReviewSubject: review.Subject, ReviewerID: input.ReviewerID, DecisionReason: input.DecisionReason, Admission: admission}
			payload, err := deterministicJSON(review.Display)
			if err != nil {
				return err
			}
			receipt, err := deterministicJSON(result)
			if err != nil {
				return err
			}

			_, err = tx.exec(ctx, `INSERT INTO supersession_review_bindings (admission_decision_id,proposal_occurrence_id,request_id,event_id,review_subject,display_payload,receipt_payload) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)`, admission.AdmissionDecisionID, in.ProposalOccurrenceID, input.RequestID, admission.AdmissionEventID, review.Subject, string(payload), string(receipt))
			return err
		})
	})
	if err != nil {
		return SupersessionReviewReceipt{}, err
	}
	return result, err
}

func loadSupersessionRecordedReview(ctx context.Context, tx sqlTx, p ProposalQueryResult, req SupersessionReviewRequest, exact bool) (SupersessionReview, error) {
	var payload string
	var raw []byte
	err := tx.queryRow(ctx, `SELECT b.display_payload,b.receipt_payload FROM supersession_review_bindings b JOIN admission_decisions d USING(admission_decision_id) WHERE b.proposal_occurrence_id=$1 AND d.supersession_review_binding_contract_version=$2 AND b.receipt_payload->>'reviewer_id'=d.decision_by AND b.receipt_payload->>'decision_reason'=d.decision_reason`, p.ProposalOccurrenceID, ReviewedSupersessionAdmissionV1).Scan(&payload, &raw)
	if err != nil {
		return SupersessionReview{}, newDomainError(ErrorAdmissionStateConflict, "admitted proposal has no reviewed supersession receipt")
	}
	var display SupersessionReviewDisplay
	var receipt SupersessionReviewReceipt
	if err = json.Unmarshal([]byte(payload), &display); err != nil {
		return SupersessionReview{}, err
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return SupersessionReview{}, err
	}
	if !exact {
		req.ExpectedRevision = display.Request.ExpectedRevision
		req.ExpectedHeadEventID = display.Request.ExpectedHeadEventID
	}
	if display.ContractVersion != SupersessionReviewV1 || receipt.ContractVersion != ReviewedSupersessionAdmissionV1 || receipt.ReviewSubject != "supersession-review:sha256:"+hashHex([]byte(payload)) || !reflect.DeepEqual(req, display.Request) || receipt.Admission.CanonicalRef != p.CanonicalRef || receipt.Admission.ProposalOccurrenceID != p.ProposalOccurrenceID {
		return SupersessionReview{}, newDomainError(ErrorSupersessionReplayConflict, "recorded supersession review differs")
	}

	nativeInput, basis, err := normalizeSupersessionAdmissionInput(supersessionNativeInput(display.Request, receipt.ReviewerID, receipt.DecisionReason))
	if err != nil {
		return SupersessionReview{}, err
	}
	native, err := replaySupersessionAdmission(ctx, tx, p, nativeInput, basis)
	if err != nil {
		return SupersessionReview{}, err
	}
	native.Replayed = false
	if !reflect.DeepEqual(native, receipt.Admission) {
		return SupersessionReview{}, newDomainError(ErrorSupersessionReplayConflict, "supersession receipt differs from native event and admission authority")
	}
	return SupersessionReview{Subject: receipt.ReviewSubject, Display: display, Lifecycle: SupersessionReviewLifecycle{Mode: "exact_replay_only", AdmissionOutcome: p.AdmissionOutcome, Receipt: &receipt}}, nil
}
