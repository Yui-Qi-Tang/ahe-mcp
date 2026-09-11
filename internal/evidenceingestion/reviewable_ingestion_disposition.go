package evidenceingestion

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewedSourceClaimDispositionV1 binds a terminal noncanonical decision to
// the complete displayed source-claim subject, separately from admission.
const ReviewedSourceClaimDispositionV1 = "reviewed-source-claim-disposition/v1"

// ReviewedSourceClaimDispositionInput selects one exact displayed statement.
// Outcome is rejected or audit_only; the trusted caller supplies the reviewer.
type ReviewedSourceClaimDispositionInput struct {
	ExtractionAttemptID string                      `json:"extraction_attempt_id"`
	ExpectedSubject     ExactDisplayedReviewSubject `json:"expected_subject"`
	Outcome             string                      `json:"outcome"`
	DecisionBy          string                      `json:"decision_by"`
	DecisionReason      string                      `json:"decision_reason"`
}

// RecordReviewedSourceClaimDisposition atomically reconstructs the display,
// records a noncanonical terminal decision, and binds its exact review bytes.
// It never creates canonical nodes, edges, or ordinary admission manifests.
func RecordReviewedSourceClaimDisposition(ctx context.Context, pool *pgxpool.Pool, input ReviewedSourceClaimDispositionInput) (ProposalDispositionResult, error) {
	if pool == nil {
		return ProposalDispositionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return recordReviewedSourceClaimDisposition(ctx, pgxDB{pool: pool}, input)
}

func recordReviewedSourceClaimDisposition(ctx context.Context, db sqlDB, input ReviewedSourceClaimDispositionInput) (ProposalDispositionResult, error) {
	if input.Outcome != ProposalDispositionRejected && input.Outcome != ProposalDispositionAuditOnly {
		return ProposalDispositionResult{}, newDomainError(ErrorInvalidInput, "reviewed disposition outcome must be rejected or audit_only")
	}
	reviewed, err := normalizeReviewedSourceClaimAdmissionInput(ReviewedSourceClaimAdmissionInput{
		ExtractionAttemptID: input.ExtractionAttemptID, ExpectedSubject: input.ExpectedSubject,
		DecisionBy: input.DecisionBy, DecisionReason: input.DecisionReason,
	})
	if err != nil {
		return ProposalDispositionResult{}, err
	}
	var result ProposalDispositionResult
	err = runReviewedSourceClaimAdmissionAttempts(func() error {
		result = ProposalDispositionResult{}
		return withTx(ctx, db, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
				return fmt.Errorf("configuring reviewed disposition transaction: %w", err)
			}
			proposal, err := loadProposalForAdmission(ctx, tx, reviewed.ExpectedSubject.ReviewSubject.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			if proposal.AdmissionOutcome != admissionOutcomePending {
				result, err = replayReviewedSourceClaimDisposition(ctx, tx, proposal, reviewed, input.Outcome)
				return err
			}
			if proposal.ExtractionAttemptID != reviewed.ExtractionAttemptID || proposal.ExtractionAttemptStatus != attemptStatusSucceeded ||
				proposal.ProposalKind != ProposalKindStatement || len(proposal.SourceRefs) == 0 {
				return newDomainError(ErrorReviewContractConflict, "reviewed disposition requires the exact succeeded source-backed statement attempt")
			}
			snapshot, err := loadReviewableSourceClaimReviewSnapshotInTx(ctx, tx, reviewed.ExtractionAttemptID, proposal.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			artifact, actual, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
			if err != nil {
				return err
			}
			if actual != reviewed.ExpectedSubject {
				return newDomainError(ErrorReviewContractConflict, "reviewed disposition subject differs from current PostgreSQL authority")
			}
			decisionID, err := stableID("adm:", "reviewed_source_claim_disposition", struct {
				Proposal string `json:"proposal_occurrence_id"`
				Outcome  string `json:"outcome"`
			}{proposal.ProposalOccurrenceID, input.Outcome})
			if err != nil {
				return err
			}
			if _, err := tx.exec(ctx, `
				INSERT INTO admission_decisions (admission_decision_id, proposal_occurrence_id, outcome,
					canonical_ref, raw_evidence_node_ids, canonical_edge_ids, decision_by, decision_reason,
					disposition_review_binding_contract_version)
				VALUES ($1, $2, $3, NULL, '[]'::jsonb, '[]'::jsonb, $4, $5, $6)
			`, decisionID, proposal.ProposalOccurrenceID, input.Outcome, reviewed.DecisionBy, reviewed.DecisionReason, ReviewedSourceClaimDispositionV1); err != nil {
				return fmt.Errorf("inserting exact reviewed disposition: %w", err)
			}
			binding := newSourceClaimReviewBinding(AdmissionResult{AdmissionDecisionID: decisionID, ProposalOccurrenceID: proposal.ProposalOccurrenceID}, reviewed, snapshot, artifact)
			binding.ContractVersion = ReviewedSourceClaimDispositionV1
			if err := insertSourceClaimReviewBindingTable(ctx, tx, binding, true); err != nil {
				return err
			}
			if err := markProposalDisposition(ctx, tx, proposal.ProposalOccurrenceID, input.Outcome); err != nil {
				return err
			}
			result = ProposalDispositionResult{ProposalOccurrenceID: proposal.ProposalOccurrenceID, AdmissionDecisionID: decisionID,
				AdmissionOutcome: input.Outcome, DecisionBy: reviewed.DecisionBy, DecisionReason: reviewed.DecisionReason}
			return nil
		})
	})
	if err != nil {
		return ProposalDispositionResult{}, err
	}
	return result, nil
}

func replayReviewedSourceClaimDisposition(ctx context.Context, tx sqlTx, proposal ProposalQueryResult, input ReviewedSourceClaimAdmissionInput, outcome string) (ProposalDispositionResult, error) {
	if proposal.AdmissionOutcome != outcome || proposal.CanonicalRef != "" {
		return ProposalDispositionResult{}, newDomainError(ErrorAdmissionStateConflict, "proposal has a different terminal decision")
	}
	result, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return ProposalDispositionResult{}, err
	}
	if result.AdmissionOutcome != outcome || result.CanonicalRef != "" || len(result.RawEvidenceNodeIDs) != 0 || len(result.CanonicalEdgeIDs) != 0 ||
		metadata.DecisionBy != input.DecisionBy || metadata.DecisionReason != input.DecisionReason || metadata.ReviewBindingContractVersion != "" {
		return ProposalDispositionResult{}, newDomainError(ErrorAdmissionReplayConflict, "reviewed disposition changes its outcome or reviewer metadata")
	}
	binding, err := loadSourceClaimReviewBindingTable(ctx, tx, result.AdmissionDecisionID, true)
	if err != nil {
		return ProposalDispositionResult{}, newDomainError(ErrorAdmissionReplayConflict, "reviewed disposition binding is unavailable")
	}
	if err := validatePersistedSourceClaimReviewBindingContract(binding, result, input, ReviewedSourceClaimDispositionV1); err != nil {
		return ProposalDispositionResult{}, err
	}
	return ProposalDispositionResult{ProposalOccurrenceID: result.ProposalOccurrenceID, AdmissionDecisionID: result.AdmissionDecisionID,
		AdmissionOutcome: outcome, DecisionBy: metadata.DecisionBy, DecisionReason: metadata.DecisionReason, Replayed: true}, nil
}

func proposalHasSourceClaimDispositionReviewBinding(ctx context.Context, tx sqlTx, occurrenceID string) (bool, error) {
	var exists bool
	if err := tx.queryRow(ctx, `SELECT EXISTS (SELECT 1 FROM source_claim_disposition_review_bindings WHERE proposal_occurrence_id=$1)`, occurrenceID).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking exact reviewed disposition authority: %w", err)
	}
	return exists, nil
}
