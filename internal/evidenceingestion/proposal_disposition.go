package evidenceingestion

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RecordPendingProposalDisposition records a terminal non-canonical outcome for one pending proposal.
func RecordPendingProposalDisposition(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ProposalDispositionInput,
) (ProposalDispositionResult, error) {
	if pool == nil {
		return ProposalDispositionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return recordPendingProposalDisposition(ctx, pgxDB{pool: pool}, input)
}

func recordPendingProposalDisposition(
	ctx context.Context,
	db sqlDB,
	input ProposalDispositionInput,
) (ProposalDispositionResult, error) {
	input = normalizeProposalDispositionInput(input)
	if err := validateProposalDispositionInput(input); err != nil {
		return ProposalDispositionResult{}, err
	}

	var result ProposalDispositionResult
	err := withTx(ctx, db, func(tx sqlTx) error {
		proposal, err := loadProposalForAdmission(ctx, tx, input.ProposalOccurrenceID)
		if err != nil {
			return err
		}
		switch proposal.AdmissionOutcome {
		case admissionOutcomePending:
		case input.Outcome:
			replay, metadata, err := loadAdmissionDecisionResult(ctx, tx, input.ProposalOccurrenceID)
			if err != nil {
				return err
			}
			if replay.AdmissionOutcome != input.Outcome ||
				replay.CanonicalRef != "" ||
				len(replay.RawEvidenceNodeIDs) != 0 ||
				len(replay.CanonicalEdgeIDs) != 0 {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s has inconsistent %s disposition state",
					input.ProposalOccurrenceID,
					input.Outcome,
				)
			}
			if metadata.DecisionBy != input.DecisionBy ||
				metadata.DecisionReason != input.DecisionReason {
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s already has a %s disposition with different audit metadata",
					input.ProposalOccurrenceID,
					input.Outcome,
				)
			}
			result = ProposalDispositionResult{
				ProposalOccurrenceID: replay.ProposalOccurrenceID,
				AdmissionDecisionID:  replay.AdmissionDecisionID,
				AdmissionOutcome:     replay.AdmissionOutcome,
				DecisionBy:           metadata.DecisionBy,
				DecisionReason:       metadata.DecisionReason,
				Replayed:             true,
			}
			return nil
		case admissionOutcomeAdmitted, admissionOutcomeRejected, admissionOutcomeAuditOnly:
			return newDomainError(
				ErrorAdmissionStateConflict,
				"proposal %s outcome is %s",
				input.ProposalOccurrenceID,
				proposal.AdmissionOutcome,
			)
		default:
			return newDomainError(
				ErrorAdmissionStateConflict,
				"proposal %s has unknown admission outcome %q",
				input.ProposalOccurrenceID,
				proposal.AdmissionOutcome,
			)
		}
		if proposal.ExtractionAttemptStatus != attemptStatusSucceeded {
			return newDomainError(
				ErrorAdmissionStateConflict,
				"proposal %s attempt status is %s",
				input.ProposalOccurrenceID,
				proposal.ExtractionAttemptStatus,
			)
		}

		decisionID, err := stableID("adm:", "admission_decision", struct {
			ProposalOccurrenceID string `json:"proposal_occurrence_id"`
			Outcome              string `json:"outcome"`
			CanonicalRef         string `json:"canonical_ref"`
		}{
			ProposalOccurrenceID: proposal.ProposalOccurrenceID,
			Outcome:              input.Outcome,
		})
		if err != nil {
			return err
		}
		decision := admissionDecision{
			ID:                 decisionID,
			ProposalOccurrence: proposal.ProposalOccurrenceID,
			Outcome:            input.Outcome,
			RawEvidenceNodeIDs: []string{},
			CanonicalEdgeIDs:   []string{},
		}
		if err := insertAdmissionDecision(ctx, tx, decision, admissionDecisionMetadata{
			DecisionBy:     input.DecisionBy,
			DecisionReason: input.DecisionReason,
		}); err != nil {
			return err
		}
		if err := markProposalDisposition(ctx, tx, proposal.ProposalOccurrenceID, input.Outcome); err != nil {
			return err
		}
		result = ProposalDispositionResult{
			ProposalOccurrenceID: proposal.ProposalOccurrenceID,
			AdmissionDecisionID:  decisionID,
			AdmissionOutcome:     input.Outcome,
			DecisionBy:           input.DecisionBy,
			DecisionReason:       input.DecisionReason,
		}
		return nil
	})
	if err != nil {
		return ProposalDispositionResult{}, err
	}
	return result, nil
}

func normalizeProposalDispositionInput(input ProposalDispositionInput) ProposalDispositionInput {
	input.ProposalOccurrenceID = strings.TrimSpace(input.ProposalOccurrenceID)
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	return input
}

func validateProposalDispositionInput(input ProposalDispositionInput) error {
	if input.ProposalOccurrenceID == "" {
		return newDomainError(ErrorInvalidInput, "proposal_occurrence_id is required")
	}
	if !strings.HasPrefix(input.ProposalOccurrenceID, "occ:") {
		return newDomainError(
			ErrorInvalidRecordID,
			"proposal_occurrence_id %q must start with occ:",
			input.ProposalOccurrenceID,
		)
	}
	switch input.Outcome {
	case admissionOutcomeRejected, admissionOutcomeAuditOnly:
	default:
		return newDomainError(
			ErrorInvalidInput,
			"outcome must be %s or %s",
			ProposalDispositionRejected,
			ProposalDispositionAuditOnly,
		)
	}
	if input.DecisionBy == "" ||
		!utf8.ValidString(input.DecisionBy) ||
		len(input.DecisionBy) > ProposalDispositionDecisionByMaxBytes {
		return newDomainError(
			ErrorInvalidInput,
			"decision_by must contain 1 to %d bytes of valid UTF-8",
			ProposalDispositionDecisionByMaxBytes,
		)
	}
	if input.DecisionReason == "" ||
		!utf8.ValidString(input.DecisionReason) ||
		len(input.DecisionReason) > ProposalDispositionDecisionReasonMaxBytes {
		return newDomainError(
			ErrorInvalidInput,
			"decision_reason must contain 1 to %d bytes of valid UTF-8",
			ProposalDispositionDecisionReasonMaxBytes,
		)
	}
	return nil
}

func markProposalDisposition(ctx context.Context, tx sqlTx, occurrenceID, outcome string) error {
	tag, err := tx.exec(ctx, `
		UPDATE proposal_occurrences
		SET admission_outcome = $2,
			canonical_ref = NULL
		WHERE proposal_occurrence_id = $1
			AND admission_outcome = 'pending'
	`, occurrenceID, outcome)
	if err != nil {
		return fmt.Errorf("marking proposal %s: %w", outcome, err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorAdmissionStateConflict, "proposal %s was not pending during disposition", occurrenceID)
	}
	return nil
}
