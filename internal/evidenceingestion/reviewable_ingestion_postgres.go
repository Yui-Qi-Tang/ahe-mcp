package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewableSourceClaimReviewSnapshot is an ephemeral, one-snapshot review
// handoff. None of its fields are persisted by this loader.
type ReviewableSourceClaimReviewSnapshot struct {
	SubmissionReceipt  SubmissionReceipt     `json:"submission_receipt"`
	ProposalManifest   ProposalBatchManifest `json:"proposal_manifest"`
	ReviewPackage      ReviewPackage         `json:"review_package"`
	ExactReviewSubject ExactReviewSubject    `json:"exact_review_subject"`
}

// LoadReviewableSourceClaimReviewSnapshot rebuilds one review package from
// PostgreSQL authority in a single repeatable-read, read-only transaction.
// Callers provide coordinates only; receipt, manifest, and review bodies are
// derived from persisted state.
func LoadReviewableSourceClaimReviewSnapshot(ctx context.Context, pool *pgxpool.Pool, extractionAttemptID, proposalOccurrenceID string) (ReviewableSourceClaimReviewSnapshot, error) {
	if pool == nil {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return loadReviewableSourceClaimReviewSnapshot(ctx, pgxDB{pool: pool}, extractionAttemptID, proposalOccurrenceID)
}

func loadReviewableSourceClaimReviewSnapshot(ctx context.Context, db sqlDB, extractionAttemptID, proposalOccurrenceID string) (ReviewableSourceClaimReviewSnapshot, error) {
	if err := validateReviewSnapshotCoordinates(extractionAttemptID, proposalOccurrenceID); err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}

	var snapshot ReviewableSourceClaimReviewSnapshot
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		loaded, err := loadReviewableSourceClaimReviewSnapshotInTx(ctx, tx, extractionAttemptID, proposalOccurrenceID)
		if err == nil {
			snapshot = loaded
		}
		return err
	})
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	return snapshot, nil
}

// loadReviewableSourceClaimReviewSnapshotInTx rebuilds the immutable review
// subject inside its caller's transaction. The selected occurrence must still
// be pending; sibling lifecycle transitions do not change the batch identity.
func loadReviewableSourceClaimReviewSnapshotInTx(
	ctx context.Context,
	tx sqlTx,
	extractionAttemptID string,
	proposalOccurrenceID string,
) (ReviewableSourceClaimReviewSnapshot, error) {
	authority, err := loadReviewableAttemptAuthority(ctx, tx, extractionAttemptID)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	source, err := loadManualSourceContext(ctx, tx, authority.sourceSnapshotID, authority.extractionViewID)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	batchRecord, err := loadUniqueReviewableProposalBatch(ctx, tx, extractionAttemptID)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}

	attempt := attemptContext{
		manualSourceContext: source,
		ExtractorDefinition: authority.extractorDefinition,
		ExtractionRun:       authority.extractionRun,
		ExtractionAttempt:   authority.extractionAttempt,
		ProposalBatch:       batchRecord.batch,
	}
	batch, err := materializeReviewableBatch(attempt, authority.fixtureOutput)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, fmt.Errorf("re-materializing persisted extractor output: %w", err)
	}
	if batch.FixtureOutputHash != authority.extractionAttempt.OutputHash {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorReviewContractConflict, "persisted extractor output hash differs from its fixture output")
	}
	if batchRecord.proposalCount != len(batch.Occurrences) {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorReviewContractConflict, "completed proposal batch count differs from the re-materialized proposal set")
	}

	persisted, err := loadReviewableProposalOccurrences(ctx, tx, extractionAttemptID, batchRecord.batch.ID)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	if err := validatePersistedReviewableOccurrences(batch, persisted); err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	selected, ok := reviewableOccurrenceByID(batch.Occurrences, proposalOccurrenceID)
	if !ok {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorReviewContractConflict, "proposal occurrence %s is not in extraction attempt %s", proposalOccurrenceID, extractionAttemptID)
	}
	persistedSelected, ok := persistedReviewableOccurrenceByID(persisted, proposalOccurrenceID)
	if !ok {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorReviewContractConflict, "proposal occurrence %s is absent from persisted batch authority", proposalOccurrenceID)
	}
	if persistedSelected.occurrence.AdmissionOutcome != admissionOutcomePending {
		return ReviewableSourceClaimReviewSnapshot{}, newDomainError(ErrorReviewContractConflict, "proposal occurrence %s is no longer pending", proposalOccurrenceID)
	}

	manifest, err := BuildProposalBatchManifest(batch)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	receipt, err := BuildSubmissionReceipt(batch, manifest)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	current := reviewableProposalQueryFromBatch(batch, selected)
	reviewPackage, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
	if err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	subject := ExactReviewSubject{
		SubmissionReceiptID:  receipt.ID,
		ProposalManifestID:   manifest.ID,
		ProposalOccurrenceID: selected.ID,
		ProposalBasisID:      reviewPackage.ProposalBasis.ID,
		ReviewPackageID:      reviewPackage.ID,
	}
	if err := ValidateExactSourceClaimReviewSubject(receipt, manifest, current, reviewPackage, subject); err != nil {
		return ReviewableSourceClaimReviewSnapshot{}, err
	}
	return ReviewableSourceClaimReviewSnapshot{
		SubmissionReceipt:  receipt,
		ProposalManifest:   manifest,
		ReviewPackage:      reviewPackage,
		ExactReviewSubject: subject,
	}, nil
}

type reviewableAttemptAuthority struct {
	extractionAttempt   ExtractionAttempt
	extractionRun       ExtractionRun
	extractorDefinition ExtractorDefinition
	sourceSnapshotID    string
	extractionViewID    string
	fixtureOutput       FrozenExtractorOutput
}

func loadReviewableAttemptAuthority(ctx context.Context, db sqlQueryer, extractionAttemptID string) (reviewableAttemptAuthority, error) {
	var (
		authority            reviewableAttemptAuthority
		repositorySnapshotID string
		fixtureData          []byte
		extractorConfigData  []byte
		completed            bool
		failureClassNull     bool
		failureMetadataNull  bool
	)
	err := db.queryRow(ctx, `
		SELECT
			ea.extraction_attempt_id,
			ea.extraction_run_id,
			ea.attempt_number,
			ea.status,
			COALESCE(ea.output_hash, ''),
			COALESCE(ea.fixture_output, 'null'::jsonb),
			(ea.completed_at IS NOT NULL),
			(ea.failure_class IS NULL),
			(ea.failure_metadata IS NULL),
			er.extraction_run_id,
			er.request_id,
			er.extractor_definition_id,
			COALESCE(er.producer_session_ref, ''),
			COALESCE(er.source_snapshot_id, ''),
			COALESCE(er.extraction_view_id, ''),
			COALESCE(er.repository_snapshot_id, ''),
			ed.extractor_definition_id,
			ed.extractor_name,
			ed.extractor_version,
			ed.extractor_config_hash,
			ed.extractor_config
		FROM extraction_attempts ea
		JOIN extraction_runs er ON er.extraction_run_id = ea.extraction_run_id
		JOIN extractor_definitions ed ON ed.extractor_definition_id = er.extractor_definition_id
		WHERE ea.extraction_attempt_id = $1
	`, extractionAttemptID).Scan(
		&authority.extractionAttempt.ID,
		&authority.extractionAttempt.RunID,
		&authority.extractionAttempt.Number,
		&authority.extractionAttempt.Status,
		&authority.extractionAttempt.OutputHash,
		&fixtureData,
		&completed,
		&failureClassNull,
		&failureMetadataNull,
		&authority.extractionRun.ID,
		&authority.extractionRun.RequestID,
		&authority.extractionRun.ExtractorDefinitionID,
		&authority.extractionRun.ProducerSessionRef,
		&authority.sourceSnapshotID,
		&authority.extractionViewID,
		&repositorySnapshotID,
		&authority.extractorDefinition.ID,
		&authority.extractorDefinition.Name,
		&authority.extractorDefinition.Version,
		&authority.extractorDefinition.ConfigHash,
		&extractorConfigData,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reviewableAttemptAuthority{}, newDomainError(ErrorMissingSourceViewAttempt, "extraction attempt %s was not found", extractionAttemptID)
		}
		return reviewableAttemptAuthority{}, fmt.Errorf("loading reviewable extraction attempt: %w", err)
	}
	if authority.extractionAttempt.Status != attemptStatusSucceeded || !completed ||
		!failureClassNull || !failureMetadataNull || authority.extractionAttempt.OutputHash == "" {
		return reviewableAttemptAuthority{}, newDomainError(ErrorReviewContractConflict, "extraction attempt %s is not a completed successful attempt", extractionAttemptID)
	}
	if authority.sourceSnapshotID == "" || authority.extractionViewID == "" || repositorySnapshotID != "" {
		return reviewableAttemptAuthority{}, newDomainError(ErrorReviewContractConflict, "extraction attempt %s is not exclusively source-snapshot bound", extractionAttemptID)
	}
	if authority.extractionAttempt.RunID != authority.extractionRun.ID || authority.extractionRun.ExtractorDefinitionID != authority.extractorDefinition.ID {
		return reviewableAttemptAuthority{}, newDomainError(ErrorReviewContractConflict, "extraction attempt, run, and extractor definition bindings disagree")
	}
	if strings.TrimSpace(string(fixtureData)) == "null" {
		return reviewableAttemptAuthority{}, newDomainError(ErrorReviewContractConflict, "successful extraction attempt %s has no fixture output", extractionAttemptID)
	}
	if err := json.Unmarshal(extractorConfigData, &authority.extractorDefinition.Config); err != nil {
		return reviewableAttemptAuthority{}, newDomainError(ErrorReviewContractConflict, "persisted extractor config is invalid: %v", err)
	}
	if err := decodeReviewSnapshotStrictJSON(fixtureData, &authority.fixtureOutput); err != nil {
		return reviewableAttemptAuthority{}, newDomainError(ErrorReviewContractConflict, "persisted extractor output is invalid: %v", err)
	}
	authority.extractionRun.SourceSnapshotID = authority.sourceSnapshotID
	authority.extractionRun.ExtractionViewID = authority.extractionViewID
	return authority, nil
}

type reviewableProposalBatchRecord struct {
	batch         ProposalBatch
	proposalCount int
}

func loadUniqueReviewableProposalBatch(ctx context.Context, db sqlQueryer, extractionAttemptID string) (reviewableProposalBatchRecord, error) {
	rows, err := db.query(ctx, `
		SELECT proposal_batch_id, extraction_attempt_id, status, proposal_count,
			(completed_at IS NOT NULL)
		FROM proposal_batches
		WHERE extraction_attempt_id = $1
		ORDER BY proposal_batch_id
		LIMIT 2
	`, extractionAttemptID)
	if err != nil {
		return reviewableProposalBatchRecord{}, fmt.Errorf("loading reviewable proposal batch: %w", err)
	}
	defer rows.Close()

	var records []reviewableProposalBatchRecord
	for rows.Next() {
		var record reviewableProposalBatchRecord
		var completed bool
		if err := rows.Scan(&record.batch.ID, &record.batch.ExtractionAttemptID, &record.batch.Status, &record.proposalCount, &completed); err != nil {
			return reviewableProposalBatchRecord{}, fmt.Errorf("scanning reviewable proposal batch: %w", err)
		}
		if record.batch.Status != batchStatusCompleted || !completed {
			return reviewableProposalBatchRecord{}, newDomainError(ErrorReviewContractConflict, "proposal batch %s is not completed", record.batch.ID)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return reviewableProposalBatchRecord{}, fmt.Errorf("iterating reviewable proposal batches: %w", err)
	}
	if len(records) != 1 {
		return reviewableProposalBatchRecord{}, newDomainError(ErrorReviewContractConflict, "extraction attempt %s must have exactly one completed proposal batch", extractionAttemptID)
	}
	return records[0], nil
}

type persistedReviewableOccurrence struct {
	occurrence      ProposalOccurrence
	proposedPayload any
}

func loadReviewableProposalOccurrences(ctx context.Context, db sqlQueryer, extractionAttemptID, proposalBatchID string) ([]persistedReviewableOccurrence, error) {
	rows, err := db.query(ctx, `
		SELECT
			proposal_occurrence_id,
			proposal_batch_id,
			extraction_attempt_id,
			proposal_local_id,
			proposal_kind,
			statement_text,
			proposal_fingerprint,
			proposal_fingerprint_version,
			admission_outcome,
			COALESCE(canonical_ref, ''),
			source_refs,
			proposed_payload
		FROM proposal_occurrences
		WHERE extraction_attempt_id = $1
			OR proposal_batch_id = $2
		ORDER BY proposal_occurrence_id
	`, extractionAttemptID, proposalBatchID)
	if err != nil {
		return nil, fmt.Errorf("loading reviewable proposal occurrences: %w", err)
	}
	defer rows.Close()

	var result []persistedReviewableOccurrence
	for rows.Next() {
		var (
			item                persistedReviewableOccurrence
			sourceRefsData      []byte
			proposedPayloadData []byte
		)
		if err := rows.Scan(
			&item.occurrence.ID,
			&item.occurrence.BatchID,
			&item.occurrence.ExtractionAttemptID,
			&item.occurrence.ProposalLocalID,
			&item.occurrence.ProposalKind,
			&item.occurrence.StatementText,
			&item.occurrence.ProposalFingerprint,
			&item.occurrence.ProposalFingerprintVersion,
			&item.occurrence.AdmissionOutcome,
			&item.occurrence.CanonicalRef,
			&sourceRefsData,
			&proposedPayloadData,
		); err != nil {
			return nil, fmt.Errorf("scanning reviewable proposal occurrence: %w", err)
		}
		if err := decodeReviewSnapshotStrictJSON(sourceRefsData, &item.occurrence.SourceRefs); err != nil {
			return nil, newDomainError(ErrorReviewContractConflict, "persisted proposal source refs are invalid: %v", err)
		}
		if err := json.Unmarshal(proposedPayloadData, &item.proposedPayload); err != nil {
			return nil, newDomainError(ErrorReviewContractConflict, "persisted proposed payload is invalid: %v", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating reviewable proposal occurrences: %w", err)
	}
	return result, nil
}

func validatePersistedReviewableOccurrences(batch ReviewableSourceClaimBatch, persisted []persistedReviewableOccurrence) error {
	for _, item := range persisted {
		if item.occurrence.ExtractionAttemptID != batch.ExtractionAttempt.ID ||
			item.occurrence.BatchID != batch.ProposalBatch.ID {
			return newDomainError(
				ErrorReviewContractConflict,
				"persisted proposal occurrence %s has a cross-paired extraction attempt or proposal batch",
				item.occurrence.ID,
			)
		}
		if err := validatePersistedReviewableOccurrenceLifecycle(item.occurrence); err != nil {
			return err
		}
	}
	if len(persisted) != len(batch.Occurrences) {
		return newDomainError(ErrorReviewContractConflict, "persisted proposal occurrence count differs from the re-materialized proposal set")
	}
	expected := make(map[string]ProposalOccurrence, len(batch.Occurrences))
	for _, occurrence := range batch.Occurrences {
		expected[occurrence.ID] = occurrence
	}
	seen := make(map[string]struct{}, len(persisted))
	for _, item := range persisted {
		want, ok := expected[item.occurrence.ID]
		if !ok {
			return newDomainError(ErrorReviewContractConflict, "persisted proposal occurrence %s is absent from the re-materialized proposal set", item.occurrence.ID)
		}
		if _, duplicate := seen[item.occurrence.ID]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "persisted proposal occurrence %s is duplicated", item.occurrence.ID)
		}
		seen[item.occurrence.ID] = struct{}{}
		if !samePersistedReviewableOccurrence(want, item) {
			return newDomainError(ErrorReviewContractConflict, "persisted proposal occurrence %s differs from the re-materialized proposal", item.occurrence.ID)
		}
	}
	return nil
}

func samePersistedReviewableOccurrence(want ProposalOccurrence, got persistedReviewableOccurrence) bool {
	if want.ID != got.occurrence.ID ||
		want.BatchID != got.occurrence.BatchID ||
		want.ExtractionAttemptID != got.occurrence.ExtractionAttemptID ||
		want.ProposalLocalID != got.occurrence.ProposalLocalID ||
		want.ProposalKind != got.occurrence.ProposalKind ||
		want.StatementText != got.occurrence.StatementText ||
		want.ProposalFingerprint != got.occurrence.ProposalFingerprint ||
		want.ProposalFingerprintVersion != got.occurrence.ProposalFingerprintVersion ||
		!reflect.DeepEqual(want.SourceRefs, got.occurrence.SourceRefs) {
		return false
	}
	wantData, err := json.Marshal(want.ProposedPayload)
	if err != nil {
		return false
	}
	var normalizedWant any
	if err := json.Unmarshal(wantData, &normalizedWant); err != nil {
		return false
	}
	return reflect.DeepEqual(normalizedWant, got.proposedPayload)
}

func validatePersistedReviewableOccurrenceLifecycle(occurrence ProposalOccurrence) error {
	switch occurrence.AdmissionOutcome {
	case admissionOutcomePending, admissionOutcomeRejected, admissionOutcomeAuditOnly:
		if occurrence.CanonicalRef != "" {
			return newDomainError(ErrorReviewContractConflict, "proposal occurrence %s has canonical authority without admission", occurrence.ID)
		}
	case admissionOutcomeAdmitted:
		if occurrence.CanonicalRef == "" {
			return newDomainError(ErrorReviewContractConflict, "admitted source-claim proposal occurrence %s has invalid canonical authority", occurrence.ID)
		}
	default:
		return newDomainError(ErrorReviewContractConflict, "proposal occurrence %s has unknown admission outcome %q", occurrence.ID, occurrence.AdmissionOutcome)
	}
	return nil
}

func persistedReviewableOccurrenceByID(occurrences []persistedReviewableOccurrence, id string) (persistedReviewableOccurrence, bool) {
	for _, occurrence := range occurrences {
		if occurrence.occurrence.ID == id {
			return occurrence, true
		}
	}
	return persistedReviewableOccurrence{}, false
}

func reviewableOccurrenceByID(occurrences []ProposalOccurrence, id string) (ProposalOccurrence, bool) {
	for _, occurrence := range occurrences {
		if occurrence.ID == id {
			return occurrence, true
		}
	}
	return ProposalOccurrence{}, false
}

func reviewableProposalQueryFromBatch(batch ReviewableSourceClaimBatch, occurrence ProposalOccurrence) ProposalQueryResult {
	return ProposalQueryResult{
		ProposalOccurrenceID:       occurrence.ID,
		ProposalLocalID:            occurrence.ProposalLocalID,
		ProposalFingerprint:        occurrence.ProposalFingerprint,
		ProposalFingerprintVersion: occurrence.ProposalFingerprintVersion,
		ProposalKind:               occurrence.ProposalKind,
		StatementText:              occurrence.StatementText,
		AdmissionOutcome:           occurrence.AdmissionOutcome,
		CanonicalRef:               occurrence.CanonicalRef,
		SourceRefs:                 append([]ResolvedSourceRef(nil), occurrence.SourceRefs...),
		CodeFact:                   occurrence.CodeFact,
		CodeRelation:               occurrence.CodeRelation,
		ExtractionAttemptID:        batch.ExtractionAttempt.ID,
		ExtractionAttemptStatus:    batch.ExtractionAttempt.Status,
		ExtractionRunID:            batch.ExtractionRun.ID,
		ProducerSessionRef:         batch.ExtractionRun.ProducerSessionRef,
		ExtractorDefinitionID:      batch.ExtractorDefinition.ID,
		ExtractorName:              batch.ExtractorDefinition.Name,
		ExtractorVersion:           batch.ExtractorDefinition.Version,
		ExtractorConfigHash:        batch.ExtractorDefinition.ConfigHash,
		SourceBindingKind:          ProposalSourceBindingSourceSnapshot,
		ExtractionViewID:           batch.ExtractionView.ID,
		RendererName:               batch.ExtractionView.RendererName,
		RendererVersion:            batch.ExtractionView.RendererVersion,
		RenderedContentHash:        batch.ExtractionView.RenderedContentHash,
		SourceSnapshotID:           batch.SourceSnapshot.ID,
		SourceSystem:               batch.SourceSnapshot.SourceSystem,
		SourceID:                   batch.SourceSnapshot.SourceID,
		SourceVersion:              batch.SourceSnapshot.SourceVersion,
		RawContentHash:             batch.SourceSnapshot.RawContentHash,
		OriginMetadata:             cloneStringMap(batch.SourceSnapshot.OriginMetadata),
	}
}

func validateReviewSnapshotCoordinates(extractionAttemptID, proposalOccurrenceID string) error {
	if err := validateStableHexID("extraction_attempt_id", extractionAttemptID, "attempt:"); err != nil {
		return err
	}
	if err := validateStableHexID("proposal_occurrence_id", proposalOccurrenceID, "occ:"); err != nil {
		return err
	}
	return nil
}

func validateStableHexID(name, value, prefix string) error {
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return newDomainError(
			ErrorInvalidRecordID,
			"%s must use %s followed by 64 lowercase hexadecimal characters",
			name,
			prefix,
		)
	}
	for index := len(prefix); index < len(value); index++ {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return newDomainError(
				ErrorInvalidRecordID,
				"%s must use %s followed by 64 lowercase hexadecimal characters",
				name,
				prefix,
			)
		}
	}
	return nil
}

func decodeReviewSnapshotStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
