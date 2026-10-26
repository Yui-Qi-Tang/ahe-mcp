package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ContradictionReviewV1 = "contradiction-review/v1"
const ReviewedContradictionV1 = "reviewed-contradiction/v1"

// ContradictionReviewRequest selects one pair-v1 proposal and the explicitly
// stated applicability. Scope is human review material, not machine entailment.
type ContradictionReviewRequest struct {
	ProposalRequestID          string   `json:"proposal_request_id"`
	NodeAID                    string   `json:"node_a_id"`
	NodeBID                    string   `json:"node_b_id"`
	Rationale                  string   `json:"rationale"`
	Scope                      string   `json:"scope"`
	Conditions                 []string `json:"conditions"`
	ProducerName               string   `json:"producer_name"`
	ProducerVersion            string   `json:"producer_version"`
	ProducerSessionRef         string   `json:"producer_session_ref,omitempty"`
	SourceProposalOccurrenceID string   `json:"source_proposal_occurrence_id,omitempty"`
	Relation                   string   `json:"relation"`
}

type ContradictionReviewDisplay struct {
	Contract           string                         `json:"contract"`
	StoreID            string                         `json:"store_id"`
	GovernanceRevision int64                          `json:"governance_revision"`
	Request            ContradictionReviewRequest     `json:"request"`
	Proposal           CanonicalContradictionProposal `json:"proposal"`
	NodeA              CanonicalQueryResult           `json:"node_a"`
	NodeB              CanonicalQueryResult           `json:"node_b"`
	SourceProposal     *ProposalQueryResult           `json:"source_proposal,omitempty"`
	Sources            []GovernanceSource             `json:"sources"`
	Limitations        string                         `json:"limitations"`
}

type ContradictionReview struct {
	Subject string                      `json:"subject"`
	Display ContradictionReviewDisplay  `json:"display"`
	Mode    string                      `json:"mode"`
	Receipt *ContradictionReviewReceipt `json:"receipt,omitempty"`
}

type ReviewedContradictionInput struct {
	RequestID       string                     `json:"request_id"`
	Review          ContradictionReviewRequest `json:"review"`
	ExpectedSubject string                     `json:"expected_subject"`
	Decision        string                     `json:"decision"`
	DecisionReason  string                     `json:"decision_reason"`
	ReviewerID      string                     `json:"-"`
}

type ContradictionReviewReceipt struct {
	ID        string                         `json:"id"`
	Contract  string                         `json:"contract"`
	RequestID string                         `json:"request_id"`
	Subject   string                         `json:"subject"`
	Display   ContradictionReviewDisplay     `json:"display"`
	Decision  CanonicalContradictionDecision `json:"decision"`
	Replayed  bool                           `json:"replayed"`
}

type contradictionReviewBody struct {
	Contract  string                         `json:"contract"`
	RequestID string                         `json:"request_id"`
	Subject   string                         `json:"subject"`
	Display   string                         `json:"display"`
	Decision  CanonicalContradictionDecision `json:"decision"`
	Edge      CanonicalGraphEdge             `json:"edge"`
}

func prepareContradictionReview(req ContradictionReviewRequest) (preparedCanonicalContradictionProposal, error) {
	if req.Relation != "contradicts" || !validateEndpointText(req.Scope, 4000) || req.Conditions == nil || len(req.Conditions) > 32 {
		return preparedCanonicalContradictionProposal{}, newDomainError(ErrorInvalidInput, "explicit contradiction effect, scope and conditions array required")
	}
	for _, c := range req.Conditions {
		if !validateEndpointText(c, 2000) {
			return preparedCanonicalContradictionProposal{}, newDomainError(ErrorInvalidInput, "invalid contradiction condition")
		}
	}
	return prepareCanonicalContradictionProposal(CanonicalContradictionProposalInput{RequestID: req.ProposalRequestID, NodeAID: req.NodeAID, NodeBID: req.NodeBID, Rationale: req.Rationale, ProducerName: req.ProducerName, ProducerVersion: req.ProducerVersion, ProducerSessionRef: req.ProducerSessionRef, SourceProposalOccurrenceID: req.SourceProposalOccurrenceID})
}

// LoadContradictionReview is read-only: abandoned reviews do not consume a pair.
// Applying any explicit decision atomically persists the pair-v1 proposal.
func LoadContradictionReview(ctx context.Context, pool *pgxpool.Pool, req ContradictionReviewRequest) (ContradictionReview, error) {
	if pool == nil {
		return ContradictionReview{}, newDomainError(ErrorInvalidInput, "postgres pool required")
	}
	var out ContradictionReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error { var err error; out, err = loadContradictionReview(ctx, tx, req); return err })
	return out, err
}

func loadContradictionReview(ctx context.Context, tx sqlTx, req ContradictionReviewRequest) (ContradictionReview, error) {
	prepared, err := prepareContradictionReview(req)
	if err != nil {
		return ContradictionReview{}, err
	}
	in := prepared.input
	req.ProposalRequestID, req.NodeAID, req.NodeBID, req.Rationale = in.RequestID, in.NodeAID, in.NodeBID, in.Rationale
	req.ProducerName, req.ProducerVersion, req.ProducerSessionRef, req.SourceProposalOccurrenceID = in.ProducerName, in.ProducerVersion, in.ProducerSessionRef, in.SourceProposalOccurrenceID
	if _, err = tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return ContradictionReview{}, err
	}
	receipt, err := readContradictionReviewReceipt(ctx, tx, "proposal_id", prepared.proposalID)
	if err == nil {
		if !reflect.DeepEqual(receipt.Display.Request, req) {
			return ContradictionReview{}, newDomainError(ErrorReviewContractConflict, "terminal pair requires unchanged original review")
		}
		return ContradictionReview{Subject: receipt.Subject, Display: receipt.Display, Mode: "exact_replay_only", Receipt: &receipt}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ContradictionReview{}, err
	}
	p := CanonicalContradictionProposal{ID: prepared.proposalID, RequestID: in.RequestID, RequestPayloadHash: prepared.payloadHash, ProposalFingerprint: prepared.fingerprint, NodeAID: in.NodeAID, NodeBID: in.NodeBID, Relation: evidencegraph.CanonicalContradicts, Rationale: in.Rationale, ProducerName: in.ProducerName, ProducerVersion: in.ProducerVersion, ProducerSessionRef: in.ProducerSessionRef, SourceProposalOccurrenceID: in.SourceProposalOccurrenceID, AdmissionOutcome: admissionOutcomePending}
	// Check both coordinates, including a request ID already assigned to another pair.
	rows, err := tx.query(ctx, `SELECT canonical_contradiction_proposal_id FROM canonical_contradiction_proposals WHERE canonical_contradiction_proposal_id=$1 OR request_id=$2 ORDER BY canonical_contradiction_proposal_id`, p.ID, p.RequestID)
	if err != nil {
		return ContradictionReview{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return ContradictionReview{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ContradictionReview{}, err
	}
	for _, id := range ids {
		old, _, err := loadCanonicalContradictionProposalByID(ctx, tx, id, false)
		if err != nil {
			return ContradictionReview{}, err
		}
		if old.ID != p.ID || old.RequestPayloadHash != p.RequestPayloadHash {
			return ContradictionReview{}, newDomainError(ErrorReviewContractConflict, "pair-v1 proposal identity or payload conflicts")
		}
		if old.AdmissionOutcome != admissionOutcomePending {
			return ContradictionReview{}, newDomainError(ErrorAdmissionStateConflict, "terminal pair has no native review binding; cannot reopen")
		}
		p = old
		req.ProposalRequestID = old.RequestID
	}
	d := ContradictionReviewDisplay{Contract: ContradictionReviewV1, Request: req, Proposal: p, Sources: []GovernanceSource{}, Limitations: "Exact retained records and available source material only; scope and conditions are human declarations, not proven semantic incompatibility. Retention does not establish provider correspondence, extraction completeness or truth. This symmetric pair chooses no false endpoint, withdraws neither endpoint, infers no other edges and cannot reopen a terminal pair. Any governance revision change requires new review; historical replay grants no current-use permission."}
	if err = tx.queryRow(ctx, `SELECT store_id,revision FROM evidence_governance_head WHERE singleton`).Scan(&d.StoreID, &d.GovernanceRevision); err != nil {
		return ContradictionReview{}, err
	}
	if d.NodeA, err = getCanonicalEvidenceByID(ctx, tx, p.NodeAID); err != nil {
		return ContradictionReview{}, err
	}
	if d.NodeB, err = getCanonicalEvidenceByID(ctx, tx, p.NodeBID); err != nil {
		return ContradictionReview{}, err
	}
	sourceIDs := []string{}
	for _, id := range []string{p.NodeAID, p.NodeBID} {
		ids, err := governanceClaimSources(ctx, tx, id)
		if err != nil {
			return ContradictionReview{}, err
		}
		sourceIDs = append(sourceIDs, ids...)
	}
	if p.SourceProposalOccurrenceID != "" {
		source, err := loadContradictionSource(ctx, tx, p)
		if err != nil {
			return ContradictionReview{}, err
		}
		d.SourceProposal = &source
		if source.SourceSnapshotID != "" {
			sourceIDs = append(sourceIDs, source.SourceSnapshotID)
		}
	}
	slices.Sort(sourceIDs)
	sourceIDs = slices.Compact(sourceIDs)
	for _, id := range sourceIDs {
		s, err := loadGovernanceSource(ctx, tx, id)
		if err != nil {
			return ContradictionReview{}, err
		}
		d.Sources = append(d.Sources, s)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return ContradictionReview{}, err
	}
	if err := validateContradictionDisplaySize(raw, d.Proposal); err != nil {
		return ContradictionReview{}, err
	}
	return ContradictionReview{Subject: governanceDigest("contradiction-review:sha256:", raw), Display: d, Mode: "pending_decision"}, nil
}

// Reserve the actual nested encoding without changing the reviewed bytes.
// Approval has the largest envelope: it includes the edge and its provenance.
// The unknown decision fields use their maximum permitted lengths and escaping.
func validateContradictionDisplaySize(raw []byte, proposal CanonicalContradictionProposal) error {
	decisionID, err := canonicalContradictionDecisionID(proposal.ID, admissionOutcomeAdmitted)
	if err != nil {
		return err
	}
	edge := buildCanonicalContradictionEdge(proposal, decisionID)
	body, err := json.Marshal(contradictionReviewBody{
		Contract: ReviewedContradictionV1, RequestID: strings.Repeat("\x01", 200),
		Subject: governanceDigest("contradiction-review:sha256:", raw), Display: string(raw),
		Decision: CanonicalContradictionDecision{
			ID: decisionID, ProposalID: proposal.ID, Outcome: admissionOutcomeAdmitted,
			CanonicalEdgeID: edge.ID, DecisionBy: strings.Repeat("\x01", 200),
			DecisionReason: strings.Repeat("\x01", 2000),
		},
		Edge: edge,
	})
	if err != nil {
		return err
	}
	if len(body) > governanceEventMaxBytes {
		return newDomainError(ErrorInvalidInput, "contradiction review exceeds bounded receipt size")
	}
	return nil
}

// ApplyReviewedContradiction commits only the reviewed pair. The profile can
// insert this immutable command/receipt, not directly mutate canonical tables.
func ApplyReviewedContradiction(ctx context.Context, pool *pgxpool.Pool, in ReviewedContradictionInput) (ContradictionReviewReceipt, error) {
	if pool == nil || !validateEndpointText(in.RequestID, 200) || !validateEndpointText(in.ReviewerID, 200) || !validateEndpointText(in.DecisionReason, 2000) || !validateEndpointText(in.ExpectedSubject, 200) {
		return ContradictionReviewReceipt{}, newDomainError(ErrorInvalidInput, "request, subject, launcher reviewer and human reason required")
	}
	outcome := map[string]string{"approved": "admitted", "reject": "rejected", "audit_only": "audit_only"}[in.Decision]
	if outcome == "" {
		return ContradictionReviewReceipt{}, newDomainError(ErrorInvalidInput, "explicit relation decision required")
	}
	if _, err := prepareContradictionReview(in.Review); err != nil {
		return ContradictionReviewReceipt{}, err
	}
	var out ContradictionReviewReceipt
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
				return err
			}
			if err := lockGovernance(ctx, tx); err != nil {
				return err
			}
			receipt, err := readContradictionReviewReceipt(ctx, tx, "request_id", in.RequestID)
			if err == nil {
				if receipt.Subject != in.ExpectedSubject || receipt.Decision.Outcome != outcome || receipt.Decision.DecisionBy != in.ReviewerID || receipt.Decision.DecisionReason != in.DecisionReason || !reflect.DeepEqual(receipt.Display.Request, in.Review) {
					return newDomainError(ErrorReviewContractConflict, "contradiction request reused with changed inputs")
				}
				receipt.Replayed = true
				out = receipt
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			review, err := loadContradictionReview(ctx, tx, in.Review)
			if err != nil {
				return err
			}
			if review.Mode != "pending_decision" || review.Subject != in.ExpectedSubject || !reflect.DeepEqual(review.Display.Request, in.Review) {
				return newDomainError(ErrorReviewContractConflict, "unchanged pending native review required")
			}
			decisionID, err := canonicalContradictionDecisionID(review.Display.Proposal.ID, outcome)
			if err != nil {
				return err
			}
			decision := CanonicalContradictionDecision{ID: decisionID, ProposalID: review.Display.Proposal.ID, Outcome: outcome, DecisionBy: in.ReviewerID, DecisionReason: in.DecisionReason}
			edge := CanonicalGraphEdge{}
			if outcome == "admitted" {
				edge = buildCanonicalContradictionEdge(review.Display.Proposal, decisionID)
				decision.CanonicalEdgeID = edge.ID
			}
			raw, err := json.Marshal(review.Display)
			if err != nil {
				return err
			}
			body, err := json.Marshal(contradictionReviewBody{Contract: ReviewedContradictionV1, RequestID: in.RequestID, Subject: review.Subject, Display: string(raw), Decision: decision, Edge: edge})
			if err != nil {
				return err
			}
			if len(body) > governanceEventMaxBytes {
				return newDomainError(ErrorInvalidInput, "contradiction receipt exceeds byte limit")
			}
			id := governanceDigest("contradiction-review-receipt:sha256:", body)
			if _, err = tx.exec(ctx, `INSERT INTO canonical_contradiction_review_bindings(receipt_id,request_id,proposal_id,body) VALUES($1,$2,$3,$4)`, id, in.RequestID, decision.ProposalID, string(body)); err != nil {
				return err
			}
			out, err = readContradictionReviewReceipt(ctx, tx, "request_id", in.RequestID)
			return err
		})
	})
	return out, err
}

func readContradictionReviewReceipt(ctx context.Context, db sqlQueryer, column, id string) (ContradictionReviewReceipt, error) {
	if column != "request_id" && column != "proposal_id" {
		return ContradictionReviewReceipt{}, fmt.Errorf("invalid receipt coordinate")
	}
	var out ContradictionReviewReceipt
	var body string
	if err := db.queryRow(ctx, `SELECT receipt_id,body FROM canonical_contradiction_review_bindings WHERE `+column+`=$1`, id).Scan(&out.ID, &body); err != nil {
		return out, err
	}
	var b contradictionReviewBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return out, err
	}
	if b.Contract != ReviewedContradictionV1 || out.ID != governanceDigest("contradiction-review-receipt:sha256:", []byte(body)) || b.Subject != governanceDigest("contradiction-review:sha256:", []byte(b.Display)) {
		return out, newDomainError(ErrorReviewContractConflict, "invalid contradiction review receipt")
	}
	if err := json.Unmarshal([]byte(b.Display), &out.Display); err != nil {
		return out, err
	}
	out.Contract, out.RequestID, out.Subject, out.Decision = b.Contract, b.RequestID, b.Subject, b.Decision
	return out, nil
}
