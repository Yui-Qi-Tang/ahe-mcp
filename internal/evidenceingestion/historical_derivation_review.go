package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HistoricalDerivationReviewV1 identifies a purpose-bound historical display.
const HistoricalDerivationReviewV1 = "historical-derivation-review/v1"

// HistoricalDerivationAdmissionV1 identifies terminal historical decisions.
const HistoricalDerivationAdmissionV1 = "historical-derivation-admission/v1"

// HistoricalDerivationRequest declares a complete AND without selecting a use policy.
type HistoricalDerivationRequest struct {
	ProposalOccurrenceID string               `json:"proposal_occurrence_id"`
	Derivation           HistoricalDerivation `json:"derivation"`
}

// HistoricalDerivation cannot select a current-use policy. Keeping that field
// out of the wire type also rejects an explicitly empty policy at the decoder.
type HistoricalDerivation struct {
	ParentNodeIDs []string `json:"parent_node_ids"`
	Method        string   `json:"method"`
	Producer      string   `json:"producer"`
	TraceRef      string   `json:"trace_ref"`
}

// HistoricalDerivationDisplay binds historical purpose and the full native basis.
// Basis.UseReview is diagnostic; its outcome is not historical admission policy.
type HistoricalDerivationDisplay struct {
	ContractVersion string                      `json:"contract_version"`
	UseClass        string                      `json:"use_class"`
	Request         HistoricalDerivationRequest `json:"request"`
	Basis           EndpointReviewDisplay       `json:"basis"`
	Limitations     string                      `json:"limitations"`
	// Original submission carriers of derived ancestors, not additional AND
	// parents or purported quotations of their derived conclusions. Omit for
	// saved displays predating this reconstruction; never rewrite old receipts.
	AncestorSources []DerivedImplementsSourceLeaf `json:"ancestor_sources,omitempty"`
}

// HistoricalDerivationReview separates saved approval from the current lifecycle.
type HistoricalDerivationReview struct {
	Mode    string                       `json:"mode"`
	Subject string                       `json:"subject"`
	Display HistoricalDerivationDisplay  `json:"display"`
	Receipt *HistoricalDerivationReceipt `json:"receipt,omitempty"`
}

// HistoricalDerivationDecision is accepted only at the historical writer.
type HistoricalDerivationDecision struct {
	RequestID       string                      `json:"request_id"`
	Review          HistoricalDerivationRequest `json:"review"`
	ExpectedSubject string                      `json:"expected_subject"`
	Decision        string                      `json:"decision"`
	DecisionReason  string                      `json:"decision_reason"`
	ReviewerID      string                      `json:"-"`
}

// HistoricalDerivationReceipt records a terminal historical decision, not clearance.
type HistoricalDerivationReceipt struct {
	ContractVersion string                      `json:"contract_version"`
	RequestID       string                      `json:"request_id"`
	ReviewSubject   string                      `json:"review_subject"`
	ReviewerID      string                      `json:"reviewer_id"`
	DecisionReason  string                      `json:"decision_reason"`
	Outcome         string                      `json:"outcome"`
	Request         HistoricalDerivationRequest `json:"request"`
	Admission       *AdmissionResult            `json:"admission,omitempty"`
	Disposition     *ProposalDispositionResult  `json:"disposition,omitempty"`
	Replayed        bool                        `json:"replayed,omitempty"`
}

func historicalEndpointRequest(req HistoricalDerivationRequest) (EndpointReviewRequest, error) {
	// Reuse current-state material for disclosure, never its eligibility gate.
	d := EndpointDerivation{UsePolicy: evidenceuse.CurrentActionV1, ParentNodeIDs: req.Derivation.ParentNodeIDs,
		Method: req.Derivation.Method, Producer: req.Derivation.Producer, TraceRef: req.Derivation.TraceRef}
	out := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: req.ProposalOccurrenceID, Derivation: &d}
	return out, validateEndpointRequest(out)
}

func historicalAdmissionInput(req HistoricalDerivationRequest, by, reason string) AdmissionInput {
	d := req.Derivation
	return AdmissionInput{ProposalOccurrenceID: req.ProposalOccurrenceID, DecisionBy: by, DecisionReason: reason,
		Derivation: &DerivationAdmissionInput{ParentNodeIDs: d.ParentNodeIDs, Method: d.Method, Producer: d.Producer, TraceRef: d.TraceRef, useClass: evidencegraph.DerivationHistoricalOnly}}
}

// LoadHistoricalDerivationReview reconstructs a bounded native historical review.
func LoadHistoricalDerivationReview(ctx context.Context, pool *pgxpool.Pool, req HistoricalDerivationRequest) (HistoricalDerivationReview, error) {
	if pool == nil {
		return HistoricalDerivationReview{}, newDomainError(ErrorInvalidInput, "postgres pool required")
	}
	if _, err := historicalEndpointRequest(req); err != nil {
		return HistoricalDerivationReview{}, err
	}
	var out HistoricalDerivationReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = loadHistoricalDerivationReview(ctx, tx, req)
		return err
	})
	return out, err
}

func loadHistoricalDerivationReview(ctx context.Context, tx sqlTx, req HistoricalDerivationRequest) (HistoricalDerivationReview, error) {
	endpoint, err := historicalEndpointRequest(req)
	if err != nil {
		return HistoricalDerivationReview{}, err
	}
	saved, err := loadHistoricalDerivationReceipt(ctx, tx, req.ProposalOccurrenceID)
	if err == nil {
		if !reflect.DeepEqual(saved.Display.Request, req) {
			return HistoricalDerivationReview{}, newDomainError(ErrorReviewContractConflict, "historical terminal request differs")
		}
		return saved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return HistoricalDerivationReview{}, err
	}
	p, err := getProposalByOccurrenceID(ctx, tx, req.ProposalOccurrenceID)
	if err != nil {
		return HistoricalDerivationReview{}, err
	}
	if p.AdmissionOutcome != admissionOutcomePending {
		return HistoricalDerivationReview{}, newDomainError(ErrorAdmissionStateConflict, "historical request requires pending occurrence or its exact historical receipt")
	}
	basis, err := loadEndpointReview(ctx, tx, endpoint)
	if err != nil {
		return HistoricalDerivationReview{}, err
	}
	display := HistoricalDerivationDisplay{ContractVersion: HistoricalDerivationReviewV1, UseClass: evidencegraph.DerivationHistoricalOnly, Request: req, Basis: basis.Display,
		Limitations: "Reviewed historical derivation, permanently ineligible for current use. The new statement need not occur in source text; quotations belong to its premises. Complete registered AND and source context are traceability, not entailment, truth or exhaustive collection. Recorded withdrawal, replacement, contradiction and source findings remain in force. Current-use assessments below are diagnostic, not admission conditions."}
	// Endpoint ancestry reconstructs source-claim leaves, but a derived
	// ancestor can have a different proposal carrier. New historical approval
	// must verify that source too, even when its saved admission still replays.
	if err := loadHistoricalAncestorSources(ctx, tx, &display); err != nil {
		return HistoricalDerivationReview{}, err
	}
	body, err := deterministicJSON(display)
	if err != nil {
		return HistoricalDerivationReview{}, err
	}
	if len(body) > endpointReviewMaxBytes {
		return HistoricalDerivationReview{}, newDomainError(ErrorInvalidInput, "complete historical review exceeds bound")
	}
	return HistoricalDerivationReview{Mode: "pending_decision", Subject: governanceDigest("historical-derivation-review:sha256:", body), Display: display}, nil
}

func loadHistoricalAncestorSources(ctx context.Context, tx sqlTx, display *HistoricalDerivationDisplay) error {
	body, err := deterministicJSON(display)
	if err != nil {
		return err
	}
	used := len(body)
	// Ancestors are already complete, unique and sorted by canonical ID.
	for _, node := range display.Basis.Ancestors {
		if node.NodeKind != evidencegraph.CanonicalDerivedClaim {
			continue
		}
		if err := endpointSourcePreflight(ctx, tx, node.OriginProposal.ExtractionAttemptID); err != nil {
			return err
		}
		source, err := loadDerivedImplementsSourceLeaf(ctx, tx, node)
		if err != nil {
			return fmt.Errorf("reconstructing historical ancestor %s source: %w", node.CanonicalID, err)
		}
		encoded, err := deterministicJSON(source)
		if err != nil {
			return err
		}
		used += len(encoded)
		if used > endpointReviewMaxBytes {
			return newDomainError(ErrorInvalidInput, "complete historical ancestor sources exceed review bound")
		}
		display.AncestorSources = append(display.AncestorSources, source)
	}
	// The final display check also includes array separators and the field name.
	return nil
}

// ApplyHistoricalDerivationDecision commits one exact historical decision.
// Current entry points never dispatch here as a fallback.
func ApplyHistoricalDerivationDecision(ctx context.Context, pool *pgxpool.Pool, in HistoricalDerivationDecision) (HistoricalDerivationReceipt, error) {
	if pool == nil {
		return HistoricalDerivationReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool required")
	}
	if _, err := historicalEndpointRequest(in.Review); err != nil {
		return HistoricalDerivationReceipt{}, err
	}
	outcome := map[string]string{"approved": admissionOutcomeAdmitted, "reject": admissionOutcomeRejected, "audit_only": admissionOutcomeAuditOnly}[in.Decision]
	if outcome == "" || !validateEndpointText(in.RequestID, 200) || !validateEndpointText(in.ReviewerID, 200) || !validateEndpointText(in.DecisionReason, 2000) || !validateEndpointText(in.ExpectedSubject, 200) {
		return HistoricalDerivationReceipt{}, newDomainError(ErrorInvalidInput, "exact historical subject, decision, request ID, reviewer and reason required")
	}
	var out HistoricalDerivationReceipt
	err := runReviewedSourceClaimAdmissionAttempts(func() error {
		return withTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
				return err
			}
			p, err := loadProposalForAdmission(ctx, tx, in.Review.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			if p.AdmissionOutcome == admissionOutcomePending {
				if outcome == admissionOutcomeAdmitted {
					// All reviewed derived writers acquire derivation before head.
					if _, err := tx.exec(ctx, `SELECT pg_advisory_xact_lock($1)`, derivationAdmissionLockKey); err != nil {
						return err
					}
				}
				if err := lockGovernance(ctx, tx); err != nil {
					return err
				}
			}
			r, err := loadHistoricalDerivationReview(ctx, tx, in.Review)
			if err != nil {
				return err
			}
			if r.Subject != in.ExpectedSubject {
				return newDomainError(ErrorReviewContractConflict, "historical review subject changed")
			}
			if r.Receipt != nil {
				old := *r.Receipt
				if old.RequestID != in.RequestID || old.ReviewerID != in.ReviewerID || old.DecisionReason != in.DecisionReason || old.Outcome != outcome {
					return newDomainError(ErrorAdmissionReplayConflict, "historical replay differs from original decision")
				}
				if old.Admission != nil {
					native := historicalAdmissionInput(in.Review, in.ReviewerID, in.DecisionReason)
					if err := validatePersistedOrdinaryAdmissionMutation(ctx, tx, p, native); err != nil {
						return err
					}
				}
				old.Replayed = true
				out = old
				return nil
			}
			out = HistoricalDerivationReceipt{ContractVersion: HistoricalDerivationAdmissionV1, RequestID: in.RequestID, ReviewSubject: r.Subject, ReviewerID: in.ReviewerID, DecisionReason: in.DecisionReason, Outcome: outcome, Request: in.Review}
			var decisionID string
			if outcome == admissionOutcomeAdmitted {
				mutation, err := buildCanonicalAdmissionMutation(p, historicalAdmissionInput(in.Review, in.ReviewerID, in.DecisionReason))
				if err != nil {
					return err
				}
				if err := validateDerivedAdmissionWithLock(ctx, tx, mutation, true); err != nil {
					return err
				}
				write, err := persistOrdinaryCanonicalMutation(ctx, tx, mutation)
				if err != nil {
					return err
				}
				if err := insertCanonicalDerivation(ctx, tx, *mutation.derivation, mutation.derivationParentEdges, p.ProposalOccurrenceID); err != nil {
					return err
				}
				if err := insertAdmissionDecision(ctx, tx, mutation.decision, admissionDecisionMetadata{DecisionBy: in.ReviewerID, DecisionReason: in.DecisionReason, ReviewBindingContractVersion: HistoricalDerivationAdmissionV1}); err != nil {
					return err
				}
				if err := insertOrdinaryAdmissionAuthority(ctx, tx, mutation, write); err != nil {
					return err
				}
				if err := markProposalAdmitted(ctx, tx, p.ProposalOccurrenceID, mutation.result.CanonicalRef); err != nil {
					return err
				}
				out.Admission = &mutation.result
				decisionID = mutation.decision.ID
			} else {
				id, err := stableID("adm:", HistoricalDerivationAdmissionV1, struct{ Proposal, Outcome string }{p.ProposalOccurrenceID, outcome})
				if err != nil {
					return err
				}
				if _, err := tx.exec(ctx, `INSERT INTO admission_decisions (admission_decision_id,proposal_occurrence_id,outcome,canonical_ref,raw_evidence_node_ids,canonical_edge_ids,decision_by,decision_reason,disposition_review_binding_contract_version) VALUES ($1,$2,$3,NULL,'[]'::jsonb,'[]'::jsonb,$4,$5,$6)`, id, p.ProposalOccurrenceID, outcome, in.ReviewerID, in.DecisionReason, HistoricalDerivationAdmissionV1); err != nil {
					return err
				}
				if err := markProposalDisposition(ctx, tx, p.ProposalOccurrenceID, outcome); err != nil {
					return err
				}
				out.Disposition = &ProposalDispositionResult{ProposalOccurrenceID: p.ProposalOccurrenceID, AdmissionDecisionID: id, AdmissionOutcome: outcome, DecisionBy: in.ReviewerID, DecisionReason: in.DecisionReason}
				decisionID = id
			}
			body, err := deterministicJSON(out)
			if err != nil {
				return err
			}
			display, err := deterministicJSON(r.Display)
			if err != nil {
				return err
			}
			_, err = tx.exec(ctx, `INSERT INTO canonical_historical_derivation_reviews
 (admission_decision_id,proposal_occurrence_id,request_id,review_subject,display_payload,receipt_payload)
 VALUES ($1,$2,$3,$4,$5,$6)`, decisionID, p.ProposalOccurrenceID, in.RequestID, r.Subject, string(display), string(body))
			return err
		})
	})
	return out, err
}

func loadHistoricalDerivationReceipt(ctx context.Context, db sqlQueryer, occurrence string) (HistoricalDerivationReview, error) {
	var subject, rawDisplay, rawReceipt, decisionID, outcome, reviewer, reason, requestID, canonicalID, useClass, proposalOutcome, contract string
	err := db.queryRow(ctx, `SELECT h.review_subject,h.display_payload,h.receipt_payload,d.admission_decision_id,d.outcome,d.decision_by,d.decision_reason,h.request_id,coalesce(d.canonical_ref,''),coalesce(dr.use_class,''),p.admission_outcome,coalesce(d.review_binding_contract_version,d.disposition_review_binding_contract_version,'')
 FROM canonical_historical_derivation_reviews h JOIN admission_decisions d USING(admission_decision_id) JOIN proposal_occurrences p ON p.proposal_occurrence_id=h.proposal_occurrence_id LEFT JOIN canonical_derivations dr ON dr.node_id=d.canonical_ref
 WHERE h.proposal_occurrence_id=$1`, occurrence).Scan(&subject, &rawDisplay, &rawReceipt, &decisionID, &outcome, &reviewer, &reason, &requestID, &canonicalID, &useClass, &proposalOutcome, &contract)
	if err != nil {
		return HistoricalDerivationReview{}, err
	}
	var display HistoricalDerivationDisplay
	var receipt HistoricalDerivationReceipt
	if err := json.Unmarshal([]byte(rawDisplay), &display); err != nil {
		return HistoricalDerivationReview{}, err
	}
	if err := json.Unmarshal([]byte(rawReceipt), &receipt); err != nil {
		return HistoricalDerivationReview{}, err
	}
	if contract != HistoricalDerivationAdmissionV1 || proposalOutcome != outcome || receipt.RequestID != requestID || subject != governanceDigest("historical-derivation-review:sha256:", []byte(rawDisplay)) || display.ContractVersion != HistoricalDerivationReviewV1 || display.UseClass != evidencegraph.DerivationHistoricalOnly || receipt.ContractVersion != HistoricalDerivationAdmissionV1 || receipt.ReviewSubject != subject || receipt.Outcome != outcome || receipt.ReviewerID != reviewer || receipt.DecisionReason != reason || display.Request.ProposalOccurrenceID != occurrence || !reflect.DeepEqual(receipt.Request, display.Request) {
		return HistoricalDerivationReview{}, fmt.Errorf("historical review authority differs")
	}
	if outcome == admissionOutcomeAdmitted {
		if useClass != evidencegraph.DerivationHistoricalOnly || receipt.Admission == nil || receipt.Admission.CanonicalRef != canonicalID || receipt.Disposition != nil || receipt.Admission.AdmissionDecisionID != decisionID || receipt.Admission.ProposalOccurrenceID != occurrence {
			return HistoricalDerivationReview{}, fmt.Errorf("historical admission receipt differs")
		}
	} else if receipt.Disposition == nil || receipt.Admission != nil || receipt.Disposition.AdmissionDecisionID != decisionID || receipt.Disposition.ProposalOccurrenceID != occurrence {
		return HistoricalDerivationReview{}, fmt.Errorf("historical disposition receipt differs")
	}
	return HistoricalDerivationReview{Mode: "exact_replay_only", Subject: subject, Display: display, Receipt: &receipt}, nil
}

// ReadHistoricalDerivationHistory returns the original terminal review and receipt.
func ReadHistoricalDerivationHistory(ctx context.Context, pool *pgxpool.Pool, occurrence string) (HistoricalDerivationReview, error) {
	if pool == nil || !validateEndpointText(occurrence, 200) {
		return HistoricalDerivationReview{}, newDomainError(ErrorInvalidInput, "postgres pool and exact occurrence required")
	}
	var out HistoricalDerivationReview
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		var err error
		out, err = loadHistoricalDerivationReceipt(ctx, tx, occurrence)
		return err
	})
	return out, err
}
