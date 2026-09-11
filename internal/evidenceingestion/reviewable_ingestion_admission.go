package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ReviewedSourceClaimAdmissionV1 identifies the exact-subject admission binding.
	ReviewedSourceClaimAdmissionV1                    = "reviewed-source-claim-admission/v1"
	reviewedSourceClaimAdmissionSerializationAttempts = 3
)

// ReviewedSourceClaimAdmissionInput admits exactly the source-claim review
// subject AHE can reconstruct from PostgreSQL authority. It accepts no review
// package body, canonical relation selector, derivation, or candidate metadata.
type ReviewedSourceClaimAdmissionInput struct {
	ExtractionAttemptID string                      `json:"extraction_attempt_id"`
	ExpectedSubject     ExactDisplayedReviewSubject `json:"expected_subject"`
	DecisionBy          string                      `json:"decision_by"`
	DecisionReason      string                      `json:"decision_reason"`
}

type sourceClaimReviewBinding struct {
	AdmissionDecisionID     string
	ProposalOccurrenceID    string
	ContractVersion         string
	ReviewContractVersion   string
	ProposedEffect          string
	ExtractionAttemptID     string
	SubmissionReceiptID     string
	ProposalManifestID      string
	ProposalBasisID         string
	ReviewPackageID         string
	ReviewDisplayArtifactID string
	ReviewDisplayMediaType  string
	ReviewDisplayPayload    string
}

// AdmitReviewedSourceClaim compares an exact display commitment and performs
// the corresponding source-backed canonical mutation in the same transaction.
func AdmitReviewedSourceClaim(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ReviewedSourceClaimAdmissionInput,
) (AdmissionResult, error) {
	if pool == nil {
		return AdmissionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return admitReviewedSourceClaim(ctx, pgxDB{pool: pool}, input)
}

func admitReviewedSourceClaim(
	ctx context.Context,
	db sqlDB,
	input ReviewedSourceClaimAdmissionInput,
) (AdmissionResult, error) {
	input, err := normalizeReviewedSourceClaimAdmissionInput(input)
	if err != nil {
		return AdmissionResult{}, err
	}

	var result AdmissionResult
	err = runReviewedSourceClaimAdmissionAttempts(func() error {
		result = AdmissionResult{}
		return withTx(ctx, db, func(tx sqlTx) error {
			if _, err := tx.exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
				return fmt.Errorf("configuring reviewed source-claim admission transaction: %w", err)
			}
			proposalOccurrenceID := input.ExpectedSubject.ReviewSubject.ProposalOccurrenceID
			proposal, err := loadProposalForAdmission(ctx, tx, proposalOccurrenceID)
			if err != nil {
				return err
			}

			admissionInput := AdmissionInput{
				ProposalOccurrenceID: proposalOccurrenceID,
				DecisionBy:           input.DecisionBy,
				DecisionReason:       input.DecisionReason,
			}
			switch proposal.AdmissionOutcome {
			case admissionOutcomePending:
				result, err = admitReviewedPendingSourceClaim(ctx, tx, proposal, admissionInput, input)
				return err
			case admissionOutcomeAdmitted:
				result, err = replayReviewedSourceClaimAdmission(ctx, tx, proposal, admissionInput, input)
				return err
			case admissionOutcomeRejected, admissionOutcomeAuditOnly:
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s outcome is %s",
					proposalOccurrenceID,
					proposal.AdmissionOutcome,
				)
			default:
				return newDomainError(
					ErrorAdmissionStateConflict,
					"proposal %s has unknown admission outcome %q",
					proposalOccurrenceID,
					proposal.AdmissionOutcome,
				)
			}
		})
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func admitReviewedPendingSourceClaim(
	ctx context.Context,
	tx sqlTx,
	proposal ProposalQueryResult,
	admissionInput AdmissionInput,
	input ReviewedSourceClaimAdmissionInput,
) (AdmissionResult, error) {
	if proposal.ExtractionAttemptStatus != attemptStatusSucceeded {
		return AdmissionResult{}, newDomainError(
			ErrorAdmissionStateConflict,
			"proposal %s attempt status is %s",
			proposal.ProposalOccurrenceID,
			proposal.ExtractionAttemptStatus,
		)
	}
	if proposal.ExtractionAttemptID != input.ExtractionAttemptID {
		return AdmissionResult{}, newDomainError(
			ErrorReviewContractConflict,
			"proposal %s is not bound to extraction attempt %s",
			proposal.ProposalOccurrenceID,
			input.ExtractionAttemptID,
		)
	}
	if proposal.ProposalKind != ProposalKindStatement || len(proposal.SourceRefs) == 0 {
		return AdmissionResult{}, newDomainError(
			ErrorUnsupportedAdmission,
			"proposal %s is not a source-backed statement",
			proposal.ProposalOccurrenceID,
		)
	}

	snapshot, err := loadReviewableSourceClaimReviewSnapshotInTx(
		ctx,
		tx,
		input.ExtractionAttemptID,
		proposal.ProposalOccurrenceID,
	)
	if err != nil {
		return AdmissionResult{}, err
	}
	actualArtifact, actualSubject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		return AdmissionResult{}, err
	}
	if actualSubject != input.ExpectedSubject {
		return AdmissionResult{}, newDomainError(
			ErrorReviewContractConflict,
			"reviewed source-claim admission subject differs from current PostgreSQL authority",
		)
	}

	mutation, err := buildCanonicalAdmissionMutation(proposal, admissionInput)
	if err != nil {
		return AdmissionResult{}, err
	}
	write, err := persistOrdinaryCanonicalMutation(ctx, tx, mutation)
	if err != nil {
		return AdmissionResult{}, err
	}
	if err := insertAdmissionDecision(ctx, tx, mutation.decision, admissionDecisionMetadata{
		DecisionBy:                   input.DecisionBy,
		DecisionReason:               input.DecisionReason,
		ReviewBindingContractVersion: ReviewedSourceClaimAdmissionV1,
	}); err != nil {
		return AdmissionResult{}, err
	}
	if err := insertOrdinaryAdmissionAuthority(ctx, tx, mutation, write); err != nil {
		return AdmissionResult{}, err
	}
	binding := newSourceClaimReviewBinding(mutation.result, input, snapshot, actualArtifact)
	if err := insertSourceClaimReviewBinding(ctx, tx, binding); err != nil {
		return AdmissionResult{}, err
	}
	if err := markProposalAdmitted(ctx, tx, proposal.ProposalOccurrenceID, mutation.result.CanonicalRef); err != nil {
		return AdmissionResult{}, err
	}
	return mutation.result, nil
}

func replayReviewedSourceClaimAdmission(
	ctx context.Context,
	tx sqlTx,
	proposal ProposalQueryResult,
	admissionInput AdmissionInput,
	input ReviewedSourceClaimAdmissionInput,
) (AdmissionResult, error) {
	replay, metadata, err := loadAdmissionDecisionResult(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return AdmissionResult{}, err
	}
	wasSupersessionAdmission, err := proposalHasSupersessionAdmissionEvent(ctx, tx, proposal.ProposalOccurrenceID)
	if err != nil {
		return AdmissionResult{}, err
	}
	if wasSupersessionAdmission {
		return AdmissionResult{}, newDomainError(
			ErrorAdmissionStateConflict,
			"proposal %s was admitted through the supersession writer",
			proposal.ProposalOccurrenceID,
		)
	}
	if err := validateAdmissionReplay(proposal, admissionInput, replay, metadata); err != nil {
		return AdmissionResult{}, err
	}
	if metadata.ReviewBindingContractVersion != ReviewedSourceClaimAdmissionV1 {
		return AdmissionResult{}, newDomainError(
			ErrorAdmissionReplayConflict,
			"proposal %s admission decision has no matching reviewed-source-claim mode",
			proposal.ProposalOccurrenceID,
		)
	}
	if err := validatePersistedOrdinaryAdmissionMutation(ctx, tx, proposal, admissionInput); err != nil {
		return AdmissionResult{}, err
	}
	binding, err := loadSourceClaimReviewBinding(ctx, tx, replay.AdmissionDecisionID)
	if err != nil {
		return AdmissionResult{}, newDomainError(
			ErrorAdmissionReplayConflict,
			"proposal %s review binding is unavailable: %v",
			proposal.ProposalOccurrenceID,
			err,
		)
	}
	if err := validatePersistedSourceClaimReviewBinding(binding, replay, input); err != nil {
		return AdmissionResult{}, err
	}
	replay.Replayed = true
	return replay, nil
}

func normalizeReviewedSourceClaimAdmissionInput(
	input ReviewedSourceClaimAdmissionInput,
) (ReviewedSourceClaimAdmissionInput, error) {
	input.ExtractionAttemptID = strings.TrimSpace(input.ExtractionAttemptID)
	input.DecisionBy = strings.TrimSpace(input.DecisionBy)
	input.DecisionReason = strings.TrimSpace(input.DecisionReason)
	occurrenceID := input.ExpectedSubject.ReviewSubject.ProposalOccurrenceID
	if err := validateReviewSnapshotCoordinates(input.ExtractionAttemptID, occurrenceID); err != nil {
		return ReviewedSourceClaimAdmissionInput{}, err
	}
	for _, field := range []struct {
		name   string
		value  string
		prefix string
	}{
		{"submission_receipt_id", input.ExpectedSubject.ReviewSubject.SubmissionReceiptID, "submission-receipt:v1:sha256:"},
		{"proposal_manifest_id", input.ExpectedSubject.ReviewSubject.ProposalManifestID, "proposal-manifest:v1:sha256:"},
		{"proposal_basis_id", input.ExpectedSubject.ReviewSubject.ProposalBasisID, "proposal-basis:v1:sha256:"},
		{"review_package_id", input.ExpectedSubject.ReviewSubject.ReviewPackageID, "review-package:v1:sha256:"},
		{"review_display_artifact_id", input.ExpectedSubject.ReviewDisplayArtifactID, "review-display:v1:sha256:"},
	} {
		if err := validateStableHexID(field.name, field.value, field.prefix); err != nil {
			return ReviewedSourceClaimAdmissionInput{}, err
		}
	}
	if input.DecisionBy == "" || !utf8.ValidString(input.DecisionBy) || strings.ContainsRune(input.DecisionBy, 0) || len(input.DecisionBy) > ProposalDispositionDecisionByMaxBytes {
		return ReviewedSourceClaimAdmissionInput{}, newDomainError(
			ErrorInvalidInput,
			"decision_by must contain 1 to %d bytes of valid UTF-8 without NUL",
			ProposalDispositionDecisionByMaxBytes,
		)
	}
	if input.DecisionReason == "" || !utf8.ValidString(input.DecisionReason) || strings.ContainsRune(input.DecisionReason, 0) || len(input.DecisionReason) > ProposalDispositionDecisionReasonMaxBytes {
		return ReviewedSourceClaimAdmissionInput{}, newDomainError(
			ErrorInvalidInput,
			"decision_reason must contain 1 to %d bytes of valid UTF-8 without NUL",
			ProposalDispositionDecisionReasonMaxBytes,
		)
	}
	return input, nil
}

func newSourceClaimReviewBinding(
	result AdmissionResult,
	input ReviewedSourceClaimAdmissionInput,
	snapshot ReviewableSourceClaimReviewSnapshot,
	artifact SourceClaimReviewDisplayArtifact,
) sourceClaimReviewBinding {
	reviewContractVersion := ReviewableIngestionContractV1
	proposedEffect := SourceClaimProposedEffectV1
	if snapshot.ReviewPackage.ContractVersion != "" {
		reviewContractVersion = snapshot.ReviewPackage.ContractVersion
		proposedEffect = snapshot.ReviewPackage.ProposedEffect
	}
	subject := input.ExpectedSubject.ReviewSubject
	return sourceClaimReviewBinding{
		AdmissionDecisionID:     result.AdmissionDecisionID,
		ProposalOccurrenceID:    result.ProposalOccurrenceID,
		ContractVersion:         ReviewedSourceClaimAdmissionV1,
		ReviewContractVersion:   reviewContractVersion,
		ProposedEffect:          proposedEffect,
		ExtractionAttemptID:     input.ExtractionAttemptID,
		SubmissionReceiptID:     subject.SubmissionReceiptID,
		ProposalManifestID:      subject.ProposalManifestID,
		ProposalBasisID:         subject.ProposalBasisID,
		ReviewPackageID:         subject.ReviewPackageID,
		ReviewDisplayArtifactID: input.ExpectedSubject.ReviewDisplayArtifactID,
		ReviewDisplayMediaType:  artifact.MediaType,
		ReviewDisplayPayload:    artifact.PayloadUTF8,
	}
}

func validatePersistedSourceClaimReviewBinding(
	binding sourceClaimReviewBinding,
	result AdmissionResult,
	input ReviewedSourceClaimAdmissionInput,
) error {
	return validatePersistedSourceClaimReviewBindingContract(binding, result, input, ReviewedSourceClaimAdmissionV1)
}

func validatePersistedSourceClaimReviewBindingContract(binding sourceClaimReviewBinding, result AdmissionResult, input ReviewedSourceClaimAdmissionInput, contract string) error {
	subject := input.ExpectedSubject.ReviewSubject
	if binding.AdmissionDecisionID != result.AdmissionDecisionID ||
		binding.ProposalOccurrenceID != result.ProposalOccurrenceID ||
		binding.ContractVersion != contract ||
		binding.ReviewContractVersion != ReviewableIngestionContractV1 ||
		binding.ProposedEffect != SourceClaimProposedEffectV1 ||
		binding.ExtractionAttemptID != input.ExtractionAttemptID ||
		binding.SubmissionReceiptID != subject.SubmissionReceiptID ||
		binding.ProposalManifestID != subject.ProposalManifestID ||
		binding.ProposalBasisID != subject.ProposalBasisID ||
		binding.ReviewPackageID != subject.ReviewPackageID ||
		binding.ReviewDisplayArtifactID != input.ExpectedSubject.ReviewDisplayArtifactID {
		return newDomainError(
			ErrorAdmissionReplayConflict,
			"repeated admission for proposal %s changes its exact displayed review subject",
			result.ProposalOccurrenceID,
		)
	}
	artifact := SourceClaimReviewDisplayArtifact{
		ContractVersion: SourceClaimReviewDisplayArtifactV1,
		ID:              binding.ReviewDisplayArtifactID,
		ReviewPackageID: binding.ReviewPackageID,
		MediaType:       binding.ReviewDisplayMediaType,
		PayloadUTF8:     binding.ReviewDisplayPayload,
	}
	if artifact.MediaType != SourceClaimReviewDisplayMediaTypeV1 || len(artifact.PayloadUTF8) > SourceClaimReviewDisplayMaxBytesV1 {
		return newDomainError(ErrorAdmissionReplayConflict, "persisted review display artifact metadata is invalid")
	}
	expectedArtifactID, err := sourceClaimReviewDisplayArtifactID(artifact)
	if err != nil || expectedArtifactID != artifact.ID {
		return newDomainError(ErrorAdmissionReplayConflict, "persisted review display artifact identity drifted")
	}
	var reviewPackage ReviewPackage
	if err := decodeReviewSnapshotStrictJSON([]byte(artifact.PayloadUTF8), &reviewPackage); err != nil {
		return newDomainError(ErrorAdmissionReplayConflict, "persisted review display payload is invalid: %v", err)
	}
	basis := reviewPackage.ProposalBasis
	if reviewPackage.ContractVersion != binding.ReviewContractVersion ||
		reviewPackage.ID != binding.ReviewPackageID ||
		reviewPackage.SubmissionReceiptID != binding.SubmissionReceiptID ||
		reviewPackage.ProposedEffect != binding.ProposedEffect ||
		basis.ID != binding.ProposalBasisID ||
		basis.SubmissionReceiptID != binding.SubmissionReceiptID ||
		basis.ProposalManifestID != binding.ProposalManifestID ||
		basis.ProposalOccurrenceID != binding.ProposalOccurrenceID ||
		basis.ExtractionAttemptID != binding.ExtractionAttemptID {
		return newDomainError(ErrorAdmissionReplayConflict, "persisted review display payload differs from its binding")
	}
	return nil
}

func insertSourceClaimReviewBinding(ctx context.Context, tx sqlTx, binding sourceClaimReviewBinding) error {
	return insertSourceClaimReviewBindingTable(ctx, tx, binding, false)
}

func sourceClaimReviewBindingTable(disposition bool) string {
	if disposition {
		return "source_claim_disposition_review_bindings"
	}
	return "canonical_source_claim_review_bindings"
}

func insertSourceClaimReviewBindingTable(ctx context.Context, tx sqlTx, binding sourceClaimReviewBinding, disposition bool) error {
	if _, err := tx.exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			admission_decision_id,
			proposal_occurrence_id,
			contract_version,
			review_contract_version,
			proposed_effect,
			extraction_attempt_id,
			submission_receipt_id,
			proposal_manifest_id,
			proposal_basis_id,
			review_package_id,
			review_display_artifact_id,
			review_display_media_type,
			review_display_payload_utf8
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, sourceClaimReviewBindingTable(disposition)),
		binding.AdmissionDecisionID,
		binding.ProposalOccurrenceID,
		binding.ContractVersion,
		binding.ReviewContractVersion,
		binding.ProposedEffect,
		binding.ExtractionAttemptID,
		binding.SubmissionReceiptID,
		binding.ProposalManifestID,
		binding.ProposalBasisID,
		binding.ReviewPackageID,
		binding.ReviewDisplayArtifactID,
		binding.ReviewDisplayMediaType,
		binding.ReviewDisplayPayload,
	); err != nil {
		return fmt.Errorf("inserting reviewed source-claim admission binding for %s: %w", binding.ProposalOccurrenceID, err)
	}
	return nil
}

func loadSourceClaimReviewBinding(ctx context.Context, tx sqlTx, decisionID string) (sourceClaimReviewBinding, error) {
	return loadSourceClaimReviewBindingTable(ctx, tx, decisionID, false)
}

func loadSourceClaimReviewBindingTable(ctx context.Context, tx sqlTx, decisionID string, disposition bool) (sourceClaimReviewBinding, error) {
	var binding sourceClaimReviewBinding
	err := tx.queryRow(ctx, fmt.Sprintf(`
		SELECT
			admission_decision_id,
			proposal_occurrence_id,
			contract_version,
			review_contract_version,
			proposed_effect,
			extraction_attempt_id,
			submission_receipt_id,
			proposal_manifest_id,
			proposal_basis_id,
			review_package_id,
			review_display_artifact_id,
			review_display_media_type,
			review_display_payload_utf8
		FROM %s
		WHERE admission_decision_id = $1
	`, sourceClaimReviewBindingTable(disposition)), decisionID).Scan(
		&binding.AdmissionDecisionID,
		&binding.ProposalOccurrenceID,
		&binding.ContractVersion,
		&binding.ReviewContractVersion,
		&binding.ProposedEffect,
		&binding.ExtractionAttemptID,
		&binding.SubmissionReceiptID,
		&binding.ProposalManifestID,
		&binding.ProposalBasisID,
		&binding.ReviewPackageID,
		&binding.ReviewDisplayArtifactID,
		&binding.ReviewDisplayMediaType,
		&binding.ReviewDisplayPayload,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sourceClaimReviewBinding{}, fmt.Errorf("review binding for admission decision %s not found", decisionID)
		}
		return sourceClaimReviewBinding{}, fmt.Errorf("loading review binding for admission decision %s: %w", decisionID, err)
	}
	return binding, nil
}

func runReviewedSourceClaimAdmissionAttempts(run func() error) error {
	var err error
	for attempt := 1; attempt <= reviewedSourceClaimAdmissionSerializationAttempts; attempt++ {
		err = run()
		if err == nil || !isReviewedSourceClaimAdmissionSerializationFailure(err) {
			return err
		}
	}
	return fmt.Errorf(
		"reviewed source-claim admission exceeded %d serialization attempts: %w",
		reviewedSourceClaimAdmissionSerializationAttempts,
		err,
	)
}

func isReviewedSourceClaimAdmissionSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

func proposalHasSourceClaimReviewBinding(ctx context.Context, tx sqlTx, proposalOccurrenceID string) (bool, error) {
	var exists bool
	if err := tx.queryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM canonical_source_claim_review_bindings
			WHERE proposal_occurrence_id = $1
		)
	`, proposalOccurrenceID).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking reviewed source-claim replay authority: %w", err)
	}
	return exists, nil
}
