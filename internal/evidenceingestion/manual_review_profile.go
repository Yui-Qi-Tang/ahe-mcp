package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

func buildManualReviewProfile(input ManualTextInput, snapshot SourceSnapshot) (*ManualReviewProfileBinding, error) {
	if input.ManualReviewProfile == nil {
		return nil, nil
	}
	if snapshot.SourceSystem != SourceSystemManualText {
		return nil, newDomainError(ErrorInvalidInput, "manual review profile requires manual_text intake")
	}
	if err := ValidateManualReviewProfileInput(*input.ManualReviewProfile); err != nil {
		return nil, err
	}
	profile := *input.ManualReviewProfile
	profile.Limitations = slices.Clone(profile.Limitations)
	data, err := deterministicJSON(profile)
	if err != nil {
		return nil, err
	}
	binding := &ManualReviewProfileBinding{SourceSnapshotID: snapshot.ID, IntakeRequestID: input.RequestID,
		ContractVersion: ManualSourceReviewProfileV1, Profile: profile, ProfileHash: contentHash(data)}
	if err := ValidateManualReviewProfileBinding(*binding); err != nil {
		return nil, err
	}
	return binding, nil
}

// LoadManualReviewProfileInTx loads a validated profile in the caller's transaction.
// A nil result denotes a legacy, unqualified source; it never reads metadata keys.
func LoadManualReviewProfileInTx(ctx context.Context, tx pgx.Tx, snapshotID string) (*ManualReviewProfileBinding, error) {
	if tx == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres transaction is required")
	}
	return loadManualReviewProfile(ctx, pgxTx{tx: tx}, snapshotID)
}

func loadManualReviewProfile(ctx context.Context, db sqlQueryer, snapshotID string) (*ManualReviewProfileBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !hasStableIDPrefix(snapshotID, "srcsnap:") {
		return nil, newDomainError(ErrorInvalidInput, "manual review profile requires a source snapshot ID")
	}
	if err := validateManualReviewText("source snapshot", snapshotID, 200); err != nil {
		return nil, err
	}
	var binding ManualReviewProfileBinding
	var payload, sourceSystem, intakeSnapshot string
	err := db.queryRow(ctx, `/* manual review profile binding */
		SELECT p.source_snapshot_id,p.intake_request_id,p.contract_version,
			CASE WHEN octet_length(p.profile_payload_utf8)<=$2 THEN p.profile_payload_utf8 ELSE '' END,
			p.profile_hash,COALESCE(s.source_system,''),COALESCE(r.source_snapshot_id,'')
		FROM source_manual_review_profiles p
		LEFT JOIN source_snapshots s ON s.source_snapshot_id=p.source_snapshot_id
		LEFT JOIN source_intake_requests r ON r.request_id=p.intake_request_id
		WHERE p.source_snapshot_id=$1`, snapshotID, ManualReviewProfileMaxBytes).
		Scan(&binding.SourceSnapshotID, &binding.IntakeRequestID, &binding.ContractVersion, &payload, &binding.ProfileHash, &sourceSystem, &intakeSnapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading manual review profile: %w", err)
	}
	if binding.SourceSnapshotID != snapshotID || sourceSystem != SourceSystemManualText || intakeSnapshot != snapshotID {
		return nil, newDomainError(ErrorReviewContractConflict, "manual review profile source/intake binding differs")
	}
	if err := json.Unmarshal([]byte(payload), &binding.Profile); err != nil {
		return nil, newDomainError(ErrorReviewContractConflict, "manual review profile payload is invalid")
	}
	if err := ValidateManualReviewProfileBinding(binding); err != nil {
		return nil, err
	}
	canonical, err := deterministicJSON(binding.Profile)
	if err != nil || string(canonical) != payload {
		return nil, newDomainError(ErrorReviewContractConflict, "manual review profile payload is not its exact canonical body")
	}
	return &binding, nil
}

func verifyManualReviewProfileReplay(ctx context.Context, tx sqlTx, snapshotID string, expected *ManualReviewProfileBinding) error {
	actual, err := loadManualReviewProfile(ctx, tx, snapshotID)
	if err != nil {
		return err
	}
	return validateManualReviewProfileReplay(actual, expected)
}

func validateManualReviewProfileReplay(actual, expected *ManualReviewProfileBinding) error {
	if actual == nil && expected == nil {
		return nil
	}
	if expected == nil {
		return newDomainError(ErrorOccurrenceConflict, "qualified manual snapshot replay cannot omit its review profile")
	}
	if actual == nil {
		return newDomainError(ErrorOccurrenceConflict, "existing manual snapshot cannot acquire a review profile; submit a new source version")
	}
	// New request IDs may reuse the exact profile; its first intake remains the
	// immutable authority and is not replaced by a later collection request.
	if actual.ProfileHash != expected.ProfileHash {
		return newDomainError(ErrorOccurrenceConflict, "manual snapshot already has a different review profile; submit a new source version")
	}
	return nil
}

func persistManualReviewProfileTx(ctx context.Context, tx sqlTx, binding *ManualReviewProfileBinding) (bool, error) {
	if binding == nil {
		return false, nil
	}
	actual, err := loadManualReviewProfile(ctx, tx, binding.SourceSnapshotID)
	if err != nil {
		return false, err
	}
	if actual != nil {
		// A replay must not invoke the INSERT trigger: first-capture qualification
		// is intentionally impossible in a later transaction, even with ON CONFLICT.
		return false, validateManualReviewProfileReplay(actual, binding)
	}
	data, err := deterministicJSON(binding.Profile)
	if err != nil {
		return false, err
	}
	tag, err := tx.exec(ctx, `INSERT INTO source_manual_review_profiles
		(source_snapshot_id,intake_request_id,contract_version,profile_payload_utf8,profile_hash)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(source_snapshot_id) DO NOTHING`,
		binding.SourceSnapshotID, binding.IntakeRequestID, binding.ContractVersion, string(data), binding.ProfileHash)
	if err != nil {
		return false, fmt.Errorf("persisting manual review profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, verifyManualReviewProfileReplay(ctx, tx, binding.SourceSnapshotID, binding)
	}
	return true, nil
}
