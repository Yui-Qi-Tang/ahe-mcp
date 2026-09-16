package evidenceingestion

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// ReviewableIngestionContractV1 freezes the first source-claim review handoff.
	ReviewableIngestionContractV1 = "reviewable-ingestion/v1"
	// SourceClaimReviewTemplateV1 identifies the human-facing fields bound by a review package.
	SourceClaimReviewTemplateV1 = "source-claim-review-card/v1"
	// SourceClaimProposedEffectV1 identifies ordinary source-backed canonical admission.
	SourceClaimProposedEffectV1 = "admit-source-backed-statement/v1"

	sourceClaimReviewCoverageV1 = "One pending source-backed statement and every exact source span bound to it."
)

// ErrorReviewContractConflict means review material no longer matches its immutable subject.
const ErrorReviewContractConflict ErrorKind = "review_contract_conflict"

type reviewSourceSpanIdentity struct {
	ExtractionViewID string
	SpanID           string
}

// ProposalBatchManifestEntry binds one occurrence in a complete proposal batch.
type ProposalBatchManifestEntry struct {
	Ordinal                    int    `json:"ordinal"`
	ProposalOccurrenceID       string `json:"proposal_occurrence_id"`
	ProposalLocalID            string `json:"proposal_local_id"`
	ProposalFingerprint        string `json:"proposal_fingerprint"`
	ProposalFingerprintVersion string `json:"proposal_fingerprint_version"`
	ProposalKind               string `json:"proposal_kind"`
}

// ProposalBatchManifest freezes the complete, canonically ordered proposal set.
type ProposalBatchManifest struct {
	ContractVersion     string                       `json:"contract_version"`
	ID                  string                       `json:"proposal_manifest_id"`
	ProposalBatchID     string                       `json:"proposal_batch_id"`
	ExtractionAttemptID string                       `json:"extraction_attempt_id"`
	ExtractorOutputHash string                       `json:"extractor_output_hash"`
	ProposalSetHash     string                       `json:"proposal_set_hash"`
	ProposalCount       int                          `json:"proposal_count"`
	Entries             []ProposalBatchManifestEntry `json:"entries"`
}

// SubmissionReceipt binds one accepted extractor output to its complete manifest.
// ProducerSessionRef is audit/debug metadata and is deliberately excluded from ID.
type SubmissionReceipt struct {
	ContractVersion         string `json:"contract_version"`
	ID                      string `json:"submission_receipt_id"`
	RequestID               string `json:"request_id"`
	ProducerSessionRef      string `json:"producer_session_ref,omitempty"`
	SourceSnapshotID        string `json:"source_snapshot_id"`
	ExtractionViewID        string `json:"extraction_view_id"`
	ExtractorDefinitionID   string `json:"extractor_definition_id"`
	ExtractionRunID         string `json:"extraction_run_id"`
	ExtractionAttemptID     string `json:"extraction_attempt_id"`
	ExtractionAttemptNumber int    `json:"extraction_attempt_number"`
	ExtractorOutputHash     string `json:"extractor_output_hash"`
	ProposalBatchID         string `json:"proposal_batch_id"`
	ProposalManifestID      string `json:"proposal_manifest_id"`
	ProposalCount           int    `json:"proposal_count"`
}

// ProposalBasis is the immutable proposal and provenance material shown for review.
type ProposalBasis struct {
	ContractVersion            string              `json:"contract_version"`
	ID                         string              `json:"proposal_basis_id"`
	SubmissionReceiptID        string              `json:"submission_receipt_id"`
	ProposalManifestID         string              `json:"proposal_manifest_id"`
	ProposalManifestOrdinal    int                 `json:"proposal_manifest_ordinal"`
	ProposalOccurrenceID       string              `json:"proposal_occurrence_id"`
	ProposalLocalID            string              `json:"proposal_local_id"`
	ProposalFingerprint        string              `json:"proposal_fingerprint"`
	ProposalFingerprintVersion string              `json:"proposal_fingerprint_version"`
	ProposalKind               string              `json:"proposal_kind"`
	StatementText              string              `json:"statement_text"`
	SourceRefs                 []ResolvedSourceRef `json:"source_refs"`
	SourceSnapshotID           string              `json:"source_snapshot_id"`
	ExtractionViewID           string              `json:"extraction_view_id"`
	SourceSystem               string              `json:"source_system"`
	SourceID                   string              `json:"source_id"`
	SourceVersion              string              `json:"source_version"`
	RawContentHash             string              `json:"raw_content_hash"`
	RendererName               string              `json:"renderer_name"`
	RendererVersion            string              `json:"renderer_version"`
	RenderedContentHash        string              `json:"rendered_content_hash"`
	OriginMetadataHash         string              `json:"origin_metadata_hash"`
	SourceTitle                string              `json:"source_title,omitempty"`
	SourceLocation             string              `json:"source_location,omitempty"`
	SourceCoverage             string              `json:"source_coverage,omitempty"`
	SourceLimitations          []string            `json:"source_limitations"`
	ExtractorDefinitionID      string              `json:"extractor_definition_id"`
	ExtractionRunID            string              `json:"extraction_run_id"`
	ExtractionAttemptID        string              `json:"extraction_attempt_id"`
	ExtractorName              string              `json:"extractor_name"`
	ExtractorVersion           string              `json:"extractor_version"`
	ExtractorConfigHash        string              `json:"extractor_config_hash"`
	SourceBindingKind          string              `json:"source_binding_kind"`
	// Reserved for exact relation-review compatibility; current intake never sets it.
	ManualReviewProfile *ManualReviewProfileBinding `json:"manual_review_profile,omitempty"`
}

// ReviewPackage is the exact source-claim card a cooperating agent may display.
// It contains no reviewer identity, decision, session, or admission authority.
type ReviewPackage struct {
	ContractVersion     string        `json:"contract_version"`
	ID                  string        `json:"review_package_id"`
	ReviewTemplateID    string        `json:"review_template_id"`
	SubmissionReceiptID string        `json:"submission_receipt_id"`
	ProposalBasis       ProposalBasis `json:"proposal_basis"`
	ProposedEffect      string        `json:"proposed_effect"`
	Coverage            string        `json:"coverage"`
	Limitations         []string      `json:"limitations"`
}

// ExactReviewSubject is the minimum set a later approval receipt must bind.
type ExactReviewSubject struct {
	SubmissionReceiptID  string `json:"submission_receipt_id"`
	ProposalManifestID   string `json:"proposal_manifest_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	ProposalBasisID      string `json:"proposal_basis_id"`
	ReviewPackageID      string `json:"review_package_id"`
}

// BuildProposalBatchManifest validates and freezes one complete source-claim batch.
func BuildProposalBatchManifest(batch ReviewableSourceClaimBatch) (ProposalBatchManifest, error) {
	if err := validateReviewableSourceClaimBatch(batch); err != nil {
		return ProposalBatchManifest{}, err
	}
	entries := make([]ProposalBatchManifestEntry, 0, len(batch.Occurrences))
	for _, occurrence := range batch.Occurrences {
		entries = append(entries, ProposalBatchManifestEntry{
			ProposalOccurrenceID:       occurrence.ID,
			ProposalLocalID:            occurrence.ProposalLocalID,
			ProposalFingerprint:        occurrence.ProposalFingerprint,
			ProposalFingerprintVersion: occurrence.ProposalFingerprintVersion,
			ProposalKind:               occurrence.ProposalKind,
		})
	}
	slices.SortFunc(entries, compareProposalBatchManifestEntries)
	for index := range entries {
		entries[index].Ordinal = index + 1
	}
	manifest := ProposalBatchManifest{
		ContractVersion:     ReviewableIngestionContractV1,
		ProposalBatchID:     batch.ProposalBatch.ID,
		ExtractionAttemptID: batch.ExtractionAttempt.ID,
		ExtractorOutputHash: batch.ExtractionAttempt.OutputHash,
		ProposalSetHash:     batch.MaterializedProposalSetHash,
		ProposalCount:       len(entries),
		Entries:             entries,
	}
	id, err := proposalBatchManifestID(manifest)
	if err != nil {
		return ProposalBatchManifest{}, err
	}
	manifest.ID = id
	return cloneProposalBatchManifest(manifest), nil
}

// BuildSubmissionReceipt binds a validated batch to its complete manifest.
func BuildSubmissionReceipt(batch ReviewableSourceClaimBatch, manifest ProposalBatchManifest) (SubmissionReceipt, error) {
	expected, err := BuildProposalBatchManifest(batch)
	if err != nil {
		return SubmissionReceipt{}, err
	}
	if !reflect.DeepEqual(expected, manifest) {
		return SubmissionReceipt{}, newDomainError(ErrorReviewContractConflict, "proposal manifest does not match the complete materialized batch")
	}
	receipt := SubmissionReceipt{
		ContractVersion:         ReviewableIngestionContractV1,
		RequestID:               batch.ExtractionRun.RequestID,
		ProducerSessionRef:      batch.ExtractionRun.ProducerSessionRef,
		SourceSnapshotID:        batch.SourceSnapshot.ID,
		ExtractionViewID:        batch.ExtractionView.ID,
		ExtractorDefinitionID:   batch.ExtractorDefinition.ID,
		ExtractionRunID:         batch.ExtractionRun.ID,
		ExtractionAttemptID:     batch.ExtractionAttempt.ID,
		ExtractionAttemptNumber: batch.ExtractionAttempt.Number,
		ExtractorOutputHash:     batch.ExtractionAttempt.OutputHash,
		ProposalBatchID:         batch.ProposalBatch.ID,
		ProposalManifestID:      manifest.ID,
		ProposalCount:           manifest.ProposalCount,
	}
	id, err := submissionReceiptID(receipt)
	if err != nil {
		return SubmissionReceipt{}, err
	}
	receipt.ID = id
	return receipt, nil
}

// ValidateSubmissionReceiptReplay rejects any replay body drift, including session drift.
func ValidateSubmissionReceiptReplay(stored, replayed SubmissionReceipt) error {
	if err := validateSubmissionReceipt(stored); err != nil {
		return err
	}
	if err := validateSubmissionReceipt(replayed); err != nil {
		return err
	}
	if !reflect.DeepEqual(stored, replayed) {
		return newDomainError(ErrorReviewContractConflict, "submission receipt replay differs from the stored receipt body")
	}
	return nil
}

// BuildSourceClaimReviewPackage freezes one pending source-backed statement for review.
// A public adapter must load receipt, manifest, and proposal authority internally.
func BuildSourceClaimReviewPackage(receipt SubmissionReceipt, manifest ProposalBatchManifest, proposal ProposalQueryResult) (ReviewPackage, error) {
	if err := validateReceiptManifestPair(receipt, manifest); err != nil {
		return ReviewPackage{}, err
	}
	basis, err := buildProposalBasis(receipt, manifest, proposal)
	if err != nil {
		return ReviewPackage{}, err
	}
	result := ReviewPackage{
		ContractVersion:     ReviewableIngestionContractV1,
		ReviewTemplateID:    SourceClaimReviewTemplateV1,
		SubmissionReceiptID: receipt.ID,
		ProposalBasis:       basis,
		ProposedEffect:      SourceClaimProposedEffectV1,
		Coverage:            sourceClaimReviewCoverageV1,
		Limitations:         sourceClaimReviewLimitationsV1(),
	}
	id, err := reviewPackageID(result)
	if err != nil {
		return ReviewPackage{}, err
	}
	result.ID = id
	return cloneReviewPackage(result), nil
}

func sourceClaimReviewLimitationsV1() []string {
	return []string{
		"The package does not prove that the proposed statement is true.",
		"The package does not authenticate a reviewer or prove that a cooperating agent displayed it.",
		"The package covers only the named immutable source snapshot and extraction view.",
		"Relation, derivation, candidate, supersession, and repository-bound semantics are outside this contract.",
	}
}

// ValidateExactSourceClaimReviewSubject rebuilds the review package from trusted current input.
// It must not receive receipt, manifest, or current proposal bodies from a public payload.
func ValidateExactSourceClaimReviewSubject(receipt SubmissionReceipt, manifest ProposalBatchManifest, current ProposalQueryResult, reviewPackage ReviewPackage, subject ExactReviewSubject) error {
	expected, err := BuildSourceClaimReviewPackage(receipt, manifest, current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, reviewPackage) {
		return newDomainError(ErrorReviewContractConflict, "review package differs from the package rebuilt from current proposal authority")
	}
	if subject.SubmissionReceiptID != receipt.ID ||
		subject.ProposalManifestID != manifest.ID ||
		subject.ProposalOccurrenceID != current.ProposalOccurrenceID ||
		subject.ProposalBasisID != expected.ProposalBasis.ID ||
		subject.ReviewPackageID != expected.ID {
		return newDomainError(ErrorReviewContractConflict, "exact review subject does not bind the rebuilt receipt, manifest, occurrence, basis, and package")
	}
	return nil
}

func validateReviewableSourceClaimBatch(batch ReviewableSourceClaimBatch) error {
	if err := validateReviewableSourceEnvelope(batch); err != nil {
		return err
	}
	spans := make(map[string]SpanEntry, len(batch.Spans))
	for _, span := range batch.Spans {
		if err := validateReviewString("span_id", span.SpanID, true); err != nil {
			return err
		}
		if _, duplicate := spans[span.SpanID]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "duplicate span_id %q", span.SpanID)
		}
		if err := validateSpanBounds(batch.ExtractionView, span); err != nil {
			return err
		}
		spans[span.SpanID] = span
	}
	contract, err := renderingContract(batch.SourceSnapshot.SourceSystem)
	if err != nil {
		return err
	}
	expectedSpans, err := buildLineSpanCatalog(batch.ExtractionView, contract.SpanCatalogVersion)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expectedSpans, batch.Spans) {
		return newDomainError(ErrorReviewContractConflict, "extraction view span catalog drifted")
	}
	localIDs := make(map[string]struct{}, len(batch.Occurrences))
	occurrenceIDs := make(map[string]struct{}, len(batch.Occurrences))
	for _, occurrence := range batch.Occurrences {
		if _, duplicate := localIDs[occurrence.ProposalLocalID]; duplicate {
			return newDomainError(ErrorDuplicateProposalLocalID, "duplicate proposal_local_id %q", occurrence.ProposalLocalID)
		}
		if _, duplicate := occurrenceIDs[occurrence.ID]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "duplicate proposal occurrence %q", occurrence.ID)
		}
		if err := validateReviewableSourceClaimOccurrence(batch, occurrence, spans); err != nil {
			return err
		}
		localIDs[occurrence.ProposalLocalID] = struct{}{}
		occurrenceIDs[occurrence.ID] = struct{}{}
	}
	if batch.MaterializedProposalCount != len(batch.Occurrences) {
		return newDomainError(ErrorReviewContractConflict, "materialized proposal set count drifted")
	}
	expectedHash, err := materializedReviewProposalSetHash(batch.Occurrences)
	if err != nil {
		return err
	}
	if expectedHash != batch.MaterializedProposalSetHash {
		return newDomainError(ErrorReviewContractConflict, "materialized proposal set content drifted")
	}
	return nil
}

func validateReviewableSourceEnvelope(batch ReviewableSourceClaimBatch) error {
	if batch.RepositoryGoplsCoverage != nil {
		return newDomainError(ErrorReviewContractConflict, "source-claim review does not accept repository gopls coverage")
	}
	for field, value := range map[string]string{
		"source_snapshot_id":      batch.SourceSnapshot.ID,
		"source_system":           batch.SourceSnapshot.SourceSystem,
		"source_id":               batch.SourceSnapshot.SourceID,
		"source_version":          batch.SourceSnapshot.SourceVersion,
		"raw_content_hash":        batch.SourceSnapshot.RawContentHash,
		"extraction_view_id":      batch.ExtractionView.ID,
		"renderer_name":           batch.ExtractionView.RendererName,
		"renderer_version":        batch.ExtractionView.RendererVersion,
		"rendered_content_hash":   batch.ExtractionView.RenderedContentHash,
		"extractor_definition_id": batch.ExtractorDefinition.ID,
		"extractor_name":          batch.ExtractorDefinition.Name,
		"extractor_version":       batch.ExtractorDefinition.Version,
		"extractor_config_hash":   batch.ExtractorDefinition.ConfigHash,
		"request_id":              batch.ExtractionRun.RequestID,
		"extraction_run_id":       batch.ExtractionRun.ID,
		"extraction_attempt_id":   batch.ExtractionAttempt.ID,
		"extractor_output_hash":   batch.ExtractionAttempt.OutputHash,
		"proposal_batch_id":       batch.ProposalBatch.ID,
		"fixture_output_hash":     batch.FixtureOutputHash,
	} {
		if err := validateReviewString(field, value, true); err != nil {
			return err
		}
	}
	if err := validateReviewString("producer_session_ref", batch.ExtractionRun.ProducerSessionRef, false); err != nil {
		return err
	}
	if len(batch.ExtractionRun.ProducerSessionRef) > ProducerSessionRefMaxBytes || strings.TrimSpace(batch.ExtractionRun.ProducerSessionRef) != batch.ExtractionRun.ProducerSessionRef {
		return newDomainError(ErrorInvalidInput, "producer_session_ref must be normalized and at most %d bytes", ProducerSessionRefMaxBytes)
	}
	if err := validateReviewStringMap("origin_metadata", batch.SourceSnapshot.OriginMetadata); err != nil {
		return err
	}
	if !utf8.Valid(batch.ExtractionView.Rendered) || bytes.IndexByte(batch.ExtractionView.Rendered, 0) >= 0 {
		return newDomainError(ErrorInvalidUTF8, "rendered extraction view must be valid UTF-8 without NUL")
	}
	if batch.SourceSnapshot.RawContentHash != contentHash(batch.ExtractionView.Rendered) || batch.ExtractionView.RenderedContentHash != contentHash(batch.ExtractionView.Rendered) {
		return newDomainError(ErrorReviewContractConflict, "source and rendered content hashes do not match the identity-rendered view bytes")
	}
	expectedSnapshotID, err := sourceSnapshotID(batch.SourceSnapshot.SourceSystem, batch.SourceSnapshot.SourceID, batch.SourceSnapshot.SourceVersion, batch.SourceSnapshot.RawContentHash)
	if err != nil {
		return err
	}
	if expectedSnapshotID != batch.SourceSnapshot.ID || batch.ExtractionView.SourceSnapshotID != batch.SourceSnapshot.ID {
		return newDomainError(ErrorReviewContractConflict, "source snapshot or extraction view binding drifted")
	}
	contract, err := renderingContract(batch.SourceSnapshot.SourceSystem)
	if err != nil {
		return err
	}
	if batch.ExtractionView.RendererName != contract.RendererName || batch.ExtractionView.RendererVersion != contract.RendererVersion {
		return newDomainError(ErrorReviewContractConflict, "renderer does not match source system %q", batch.SourceSnapshot.SourceSystem)
	}
	expectedViewID, err := stableID("view:", "extraction_view", struct {
		SourceSnapshotID    string `json:"source_snapshot_id"`
		RendererName        string `json:"renderer_name"`
		RendererVersion     string `json:"renderer_version"`
		RenderedContentHash string `json:"rendered_content_hash"`
	}{batch.SourceSnapshot.ID, batch.ExtractionView.RendererName, batch.ExtractionView.RendererVersion, batch.ExtractionView.RenderedContentHash})
	if err != nil {
		return err
	}
	if expectedViewID != batch.ExtractionView.ID {
		return newDomainError(ErrorReviewContractConflict, "extraction view identity drifted")
	}
	expectedDefinition, err := buildExtractorDefinition(ExtractorDefinitionInput{
		Name: batch.ExtractorDefinition.Name, Version: batch.ExtractorDefinition.Version, Config: batch.ExtractorDefinition.Config,
	})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expectedDefinition, batch.ExtractorDefinition) {
		return newDomainError(ErrorReviewContractConflict, "extractor definition identity drifted")
	}
	if batch.ExtractionRun.ExtractorDefinitionID != batch.ExtractorDefinition.ID || batch.ExtractionRun.SourceSnapshotID != batch.SourceSnapshot.ID || batch.ExtractionRun.ExtractionViewID != batch.ExtractionView.ID || batch.ExtractionRun.RepositorySnapshotID != "" {
		return newDomainError(ErrorReviewContractConflict, "extraction run is not bound to the source snapshot and view")
	}
	expectedRunID, err := sourceReviewRunID(batch.ExtractionRun.RequestID, batch.ExtractorDefinition.ID, batch.SourceSnapshot.ID, batch.ExtractionView.ID)
	if err != nil {
		return err
	}
	if expectedRunID != batch.ExtractionRun.ID {
		return newDomainError(ErrorReviewContractConflict, "extraction run identity drifted")
	}
	if batch.ExtractionAttempt.Number < 1 || batch.ExtractionAttempt.RunID != batch.ExtractionRun.ID || batch.ExtractionAttempt.Status != attemptStatusSucceeded || batch.ExtractionAttempt.OutputHash != batch.FixtureOutputHash {
		return newDomainError(ErrorReviewContractConflict, "extraction attempt is not a completed, output-bound attempt")
	}
	expectedAttemptID, err := sourceReviewAttemptID(batch.ExtractionRun.ID, batch.ExtractionAttempt.Number)
	if err != nil {
		return err
	}
	if expectedAttemptID != batch.ExtractionAttempt.ID {
		return newDomainError(ErrorReviewContractConflict, "extraction attempt identity drifted")
	}
	if batch.ProposalBatch.ExtractionAttemptID != batch.ExtractionAttempt.ID || batch.ProposalBatch.Status != batchStatusCompleted {
		return newDomainError(ErrorReviewContractConflict, "proposal batch is not completed for the extraction attempt")
	}
	expectedBatchID, err := sourceReviewBatchID(batch.ExtractionAttempt.ID)
	if err != nil {
		return err
	}
	if expectedBatchID != batch.ProposalBatch.ID {
		return newDomainError(ErrorReviewContractConflict, "proposal batch identity drifted")
	}
	return nil
}

func validateReviewableSourceClaimOccurrence(batch ReviewableSourceClaimBatch, occurrence ProposalOccurrence, spans map[string]SpanEntry) error {
	for field, value := range map[string]string{
		"proposal_occurrence_id": occurrence.ID,
		"proposal_local_id":      occurrence.ProposalLocalID,
		"proposal_fingerprint":   occurrence.ProposalFingerprint,
		"statement_text":         occurrence.StatementText,
	} {
		if err := validateReviewString(field, value, true); err != nil {
			return err
		}
	}
	if strings.TrimSpace(occurrence.StatementText) == "" {
		return newDomainError(ErrorInvalidInput, "statement_text must contain non-space text")
	}
	if occurrence.BatchID != batch.ProposalBatch.ID || occurrence.ExtractionAttemptID != batch.ExtractionAttempt.ID {
		return newDomainError(ErrorReviewContractConflict, "proposal %s is outside the materialized batch", occurrence.ID)
	}
	if occurrence.ProposalKind != ProposalKindStatement || occurrence.ProposalFingerprintVersion != ProposalFingerprintStatementV1 || occurrence.AdmissionOutcome != admissionOutcomePending {
		return newDomainError(ErrorReviewContractConflict, "proposal %s is not a pending statement-v1 occurrence", occurrence.ID)
	}
	if occurrence.CanonicalRef != "" || occurrence.CodeFact != nil || occurrence.CodeRelation != nil {
		return newDomainError(ErrorReviewContractConflict, "proposal %s carries unsupported canonical, code, relation, or contradiction state", occurrence.ID)
	}
	if len(occurrence.SourceRefs) == 0 {
		return newDomainError(ErrorReviewContractConflict, "proposal %s has no exact source references", occurrence.ID)
	}
	refs := append([]ResolvedSourceRef(nil), occurrence.SourceRefs...)
	sortReviewSourceRefs(refs)
	seenRefs := make(map[reviewSourceSpanIdentity]struct{}, len(refs))
	for _, ref := range refs {
		identity := reviewSourceSpanIdentity{ExtractionViewID: ref.ExtractionViewID, SpanID: ref.SpanID}
		if _, duplicate := seenRefs[identity]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "proposal %s repeats source reference %q", occurrence.ID, ref.SpanID)
		}
		seenRefs[identity] = struct{}{}
		span, ok := spans[ref.SpanID]
		if !ok {
			return newDomainError(ErrorUnknownSpan, "proposal %s references unknown span %q", occurrence.ID, ref.SpanID)
		}
		expected := ResolvedSourceRef{
			ExtractionViewID: span.ExtractionViewID,
			SpanID:           span.SpanID,
			StartByte:        span.StartByte,
			EndByte:          span.EndByte,
			QuotedTextHash:   span.QuotedTextHash,
			QuotedText:       span.QuotedText,
		}
		if !reflect.DeepEqual(expected, ref) {
			return newDomainError(ErrorReviewContractConflict, "proposal %s source reference %q drifted from the extraction view", occurrence.ID, ref.SpanID)
		}
	}
	expectedFingerprint, err := proposalFingerprint(occurrence.StatementText, refs)
	if err != nil {
		return err
	}
	if expectedFingerprint != occurrence.ProposalFingerprint {
		return newDomainError(ErrorReviewContractConflict, "proposal %s fingerprint drifted", occurrence.ID)
	}
	expectedOccurrenceID, err := sourceReviewOccurrenceID(batch.ExtractionAttempt.ID, batch.ProposalBatch.ID, occurrence.ProposalLocalID)
	if err != nil {
		return err
	}
	if expectedOccurrenceID != occurrence.ID {
		return newDomainError(ErrorReviewContractConflict, "proposal occurrence identity drifted for local ID %q", occurrence.ProposalLocalID)
	}
	return nil
}

func buildProposalBasis(receipt SubmissionReceipt, manifest ProposalBatchManifest, proposal ProposalQueryResult) (ProposalBasis, error) {
	entry, err := manifestEntryForProposal(manifest, proposal.ProposalOccurrenceID)
	if err != nil {
		return ProposalBasis{}, err
	}
	if err := validateReviewableProposalQuery(receipt, entry, proposal); err != nil {
		return ProposalBasis{}, err
	}
	refs := append([]ResolvedSourceRef(nil), proposal.SourceRefs...)
	sortReviewSourceRefs(refs)
	originMetadataHash, err := reviewOriginMetadataHash(proposal.OriginMetadata)
	if err != nil {
		return ProposalBasis{}, err
	}
	sourceTitle, sourceLocation := reviewSourceDisplayMetadata(proposal)
	sourceCoverage, sourceLimitations, err := reviewSourceCompleteness(proposal)
	if err != nil {
		return ProposalBasis{}, err
	}
	basis := ProposalBasis{
		ContractVersion:            ReviewableIngestionContractV1,
		SubmissionReceiptID:        receipt.ID,
		ProposalManifestID:         manifest.ID,
		ProposalManifestOrdinal:    entry.Ordinal,
		ProposalOccurrenceID:       proposal.ProposalOccurrenceID,
		ProposalLocalID:            entry.ProposalLocalID,
		ProposalFingerprint:        proposal.ProposalFingerprint,
		ProposalFingerprintVersion: proposal.ProposalFingerprintVersion,
		ProposalKind:               proposal.ProposalKind,
		StatementText:              proposal.StatementText,
		SourceRefs:                 refs,
		SourceSnapshotID:           proposal.SourceSnapshotID,
		ExtractionViewID:           proposal.ExtractionViewID,
		SourceSystem:               proposal.SourceSystem,
		SourceID:                   proposal.SourceID,
		SourceVersion:              proposal.SourceVersion,
		RawContentHash:             proposal.RawContentHash,
		RendererName:               proposal.RendererName,
		RendererVersion:            proposal.RendererVersion,
		RenderedContentHash:        proposal.RenderedContentHash,
		OriginMetadataHash:         originMetadataHash,
		SourceTitle:                sourceTitle,
		SourceLocation:             sourceLocation,
		SourceCoverage:             sourceCoverage,
		SourceLimitations:          sourceLimitations,
		ExtractorDefinitionID:      proposal.ExtractorDefinitionID,
		ExtractionRunID:            proposal.ExtractionRunID,
		ExtractionAttemptID:        proposal.ExtractionAttemptID,
		ExtractorName:              proposal.ExtractorName,
		ExtractorVersion:           proposal.ExtractorVersion,
		ExtractorConfigHash:        proposal.ExtractorConfigHash,
		SourceBindingKind:          proposal.SourceBindingKind,
	}
	id, err := proposalBasisID(basis)
	if err != nil {
		return ProposalBasis{}, err
	}
	basis.ID = id
	return basis, nil
}

func validateReviewableProposalQuery(receipt SubmissionReceipt, entry ProposalBatchManifestEntry, proposal ProposalQueryResult) error {
	for field, value := range map[string]string{
		"proposal_occurrence_id":  proposal.ProposalOccurrenceID,
		"proposal_local_id":       proposal.ProposalLocalID,
		"proposal_fingerprint":    proposal.ProposalFingerprint,
		"statement_text":          proposal.StatementText,
		"source_snapshot_id":      proposal.SourceSnapshotID,
		"extraction_view_id":      proposal.ExtractionViewID,
		"source_system":           proposal.SourceSystem,
		"source_id":               proposal.SourceID,
		"source_version":          proposal.SourceVersion,
		"raw_content_hash":        proposal.RawContentHash,
		"renderer_name":           proposal.RendererName,
		"renderer_version":        proposal.RendererVersion,
		"rendered_content_hash":   proposal.RenderedContentHash,
		"extractor_definition_id": proposal.ExtractorDefinitionID,
		"extraction_run_id":       proposal.ExtractionRunID,
		"extraction_attempt_id":   proposal.ExtractionAttemptID,
		"extractor_name":          proposal.ExtractorName,
		"extractor_version":       proposal.ExtractorVersion,
		"extractor_config_hash":   proposal.ExtractorConfigHash,
	} {
		if err := validateReviewString(field, value, true); err != nil {
			return err
		}
	}
	if err := validateReviewString("producer_session_ref", proposal.ProducerSessionRef, false); err != nil {
		return err
	}
	if len(proposal.ProducerSessionRef) > ProducerSessionRefMaxBytes ||
		strings.TrimSpace(proposal.ProducerSessionRef) != proposal.ProducerSessionRef ||
		strings.TrimSpace(proposal.StatementText) == "" {
		return newDomainError(ErrorInvalidInput, "review subject statement or producer session metadata is invalid")
	}
	if err := validateReviewStringMap("origin_metadata", proposal.OriginMetadata); err != nil {
		return err
	}
	if proposal.SourceSystem == SourceSystemExternalDocument &&
		(len(proposal.OriginMetadata[externalSourceOriginTitleKey]) > externalSourceTitleMaxBytes ||
			len(proposal.OriginMetadata[externalSourceOriginLocationKey]) > externalSourceLocationMaxBytes) {
		return newDomainError(ErrorInvalidInput, "external source review title or location exceeds its bounded envelope")
	}
	if proposal.ProposalLocalID != entry.ProposalLocalID {
		return newDomainError(ErrorReviewContractConflict, "proposal local ID differs from the complete manifest")
	}
	if proposal.ProposalOccurrenceID != entry.ProposalOccurrenceID || proposal.ProposalFingerprint != entry.ProposalFingerprint || proposal.ProposalFingerprintVersion != entry.ProposalFingerprintVersion || proposal.ProposalKind != entry.ProposalKind {
		return newDomainError(ErrorReviewContractConflict, "proposal identity differs from the complete manifest entry")
	}
	if proposal.ProposalKind != ProposalKindStatement || proposal.ProposalFingerprintVersion != ProposalFingerprintStatementV1 || proposal.AdmissionOutcome != admissionOutcomePending || proposal.ExtractionAttemptStatus != attemptStatusSucceeded {
		return newDomainError(ErrorReviewContractConflict, "review subject is not a pending statement from a successful extraction attempt")
	}
	if proposal.SourceBindingKind != ProposalSourceBindingSourceSnapshot || proposal.RepositorySnapshot != nil || proposal.SourceGeneration != nil || proposal.RepositoryLifecycle != nil || proposal.SourceGenerationActive {
		return newDomainError(ErrorReviewContractConflict, "review subject is not exclusively source-snapshot bound")
	}
	if proposal.CanonicalRef != "" || proposal.CodeFact != nil || proposal.CodeRelation != nil {
		return newDomainError(ErrorReviewContractConflict, "review subject carries unsupported canonical, code, relation, or contradiction state")
	}
	if proposal.ExtractionAttemptID != receipt.ExtractionAttemptID || proposal.ExtractionRunID != receipt.ExtractionRunID || proposal.ExtractorDefinitionID != receipt.ExtractorDefinitionID || proposal.SourceSnapshotID != receipt.SourceSnapshotID || proposal.ExtractionViewID != receipt.ExtractionViewID || proposal.ProducerSessionRef != receipt.ProducerSessionRef {
		return newDomainError(ErrorReviewContractConflict, "proposal provenance differs from the submission receipt")
	}
	expectedOccurrenceID, err := sourceReviewOccurrenceID(receipt.ExtractionAttemptID, receipt.ProposalBatchID, entry.ProposalLocalID)
	if err != nil {
		return err
	}
	if expectedOccurrenceID != proposal.ProposalOccurrenceID {
		return newDomainError(ErrorReviewContractConflict, "proposal occurrence does not belong to the receipt batch")
	}
	refs := append([]ResolvedSourceRef(nil), proposal.SourceRefs...)
	if len(refs) == 0 {
		return newDomainError(ErrorReviewContractConflict, "review subject has no exact source references")
	}
	sortReviewSourceRefs(refs)
	seenRefs := make(map[reviewSourceSpanIdentity]struct{}, len(refs))
	for _, ref := range refs {
		identity := reviewSourceSpanIdentity{ExtractionViewID: ref.ExtractionViewID, SpanID: ref.SpanID}
		if _, duplicate := seenRefs[identity]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "review subject repeats source reference %q", ref.SpanID)
		}
		seenRefs[identity] = struct{}{}
		if err := validateReviewableQuerySourceRef(proposal.ExtractionViewID, ref); err != nil {
			return err
		}
	}
	expectedFingerprint, err := proposalFingerprint(proposal.StatementText, refs)
	if err != nil {
		return err
	}
	if expectedFingerprint != proposal.ProposalFingerprint {
		return newDomainError(ErrorReviewContractConflict, "proposal fingerprint differs from its current statement and exact source references")
	}
	expectedDefinitionID, err := stableID("extractor:", "extractor_definition", struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		ConfigHash string `json:"config_hash"`
	}{proposal.ExtractorName, proposal.ExtractorVersion, proposal.ExtractorConfigHash})
	if err != nil {
		return err
	}
	if expectedDefinitionID != proposal.ExtractorDefinitionID {
		return newDomainError(ErrorReviewContractConflict, "extractor definition identity drifted")
	}
	expectedSnapshotID, err := sourceSnapshotID(proposal.SourceSystem, proposal.SourceID, proposal.SourceVersion, proposal.RawContentHash)
	if err != nil {
		return err
	}
	if expectedSnapshotID != proposal.SourceSnapshotID {
		return newDomainError(ErrorReviewContractConflict, "source snapshot identity drifted")
	}
	contract, err := renderingContract(proposal.SourceSystem)
	if err != nil {
		return err
	}
	if contract.RendererName != proposal.RendererName || contract.RendererVersion != proposal.RendererVersion {
		return newDomainError(ErrorReviewContractConflict, "proposal renderer does not match its source system")
	}
	expectedViewID, err := stableID("view:", "extraction_view", struct {
		SourceSnapshotID    string `json:"source_snapshot_id"`
		RendererName        string `json:"renderer_name"`
		RendererVersion     string `json:"renderer_version"`
		RenderedContentHash string `json:"rendered_content_hash"`
	}{proposal.SourceSnapshotID, proposal.RendererName, proposal.RendererVersion, proposal.RenderedContentHash})
	if err != nil {
		return err
	}
	if expectedViewID != proposal.ExtractionViewID {
		return newDomainError(ErrorReviewContractConflict, "extraction view identity drifted")
	}
	expectedRunID, err := sourceReviewRunID(receipt.RequestID, proposal.ExtractorDefinitionID, proposal.SourceSnapshotID, proposal.ExtractionViewID)
	if err != nil {
		return err
	}
	if expectedRunID != proposal.ExtractionRunID {
		return newDomainError(ErrorReviewContractConflict, "extraction run identity drifted")
	}
	if proposal.RawContentHash != proposal.RenderedContentHash {
		return newDomainError(ErrorReviewContractConflict, "source and rendered content hashes differ under the identity renderer")
	}
	return nil
}

func validateReviewableQuerySourceRef(extractionViewID string, ref ResolvedSourceRef) error {
	for field, value := range map[string]string{
		"source_ref.extraction_view_id": ref.ExtractionViewID,
		"source_ref.span_id":            ref.SpanID,
		"source_ref.quoted_text_hash":   ref.QuotedTextHash,
		"source_ref.quoted_text":        ref.QuotedText,
	} {
		if err := validateReviewString(field, value, true); err != nil {
			return err
		}
	}
	if ref.TargetKind != "" || ref.RepositorySnapshotID != "" || ref.FileSnapshotID != "" || ref.RepoID != "" || ref.CommitSHA != "" || ref.Path != "" || ref.ExtractionViewID != extractionViewID {
		return newDomainError(ErrorReviewContractConflict, "source reference %q is not exclusively bound to extraction view %s", ref.SpanID, extractionViewID)
	}
	if ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte-ref.StartByte != len([]byte(ref.QuotedText)) {
		return newDomainError(ErrorSpanOutOfBounds, "source reference %q has invalid UTF-8 byte bounds", ref.SpanID)
	}
	if contentHash([]byte(ref.QuotedText)) != ref.QuotedTextHash {
		return newDomainError(ErrorQuotedHashMismatch, "source reference %q quoted text hash drifted", ref.SpanID)
	}
	return nil
}

func validateReceiptManifestPair(receipt SubmissionReceipt, manifest ProposalBatchManifest) error {
	if err := validateSubmissionReceipt(receipt); err != nil {
		return err
	}
	if err := validateProposalBatchManifest(manifest); err != nil {
		return err
	}
	if receipt.ProposalManifestID != manifest.ID || receipt.ProposalBatchID != manifest.ProposalBatchID || receipt.ExtractionAttemptID != manifest.ExtractionAttemptID || receipt.ExtractorOutputHash != manifest.ExtractorOutputHash || receipt.ProposalCount != manifest.ProposalCount {
		return newDomainError(ErrorReviewContractConflict, "submission receipt does not bind the supplied complete manifest")
	}
	return nil
}

func validateSubmissionReceipt(receipt SubmissionReceipt) error {
	if receipt.ContractVersion != ReviewableIngestionContractV1 {
		return newDomainError(ErrorReviewContractConflict, "submission receipt contract version %q is unsupported", receipt.ContractVersion)
	}
	for field, value := range map[string]string{
		"submission_receipt_id":   receipt.ID,
		"request_id":              receipt.RequestID,
		"source_snapshot_id":      receipt.SourceSnapshotID,
		"extraction_view_id":      receipt.ExtractionViewID,
		"extractor_definition_id": receipt.ExtractorDefinitionID,
		"extraction_run_id":       receipt.ExtractionRunID,
		"extraction_attempt_id":   receipt.ExtractionAttemptID,
		"extractor_output_hash":   receipt.ExtractorOutputHash,
		"proposal_batch_id":       receipt.ProposalBatchID,
		"proposal_manifest_id":    receipt.ProposalManifestID,
	} {
		if err := validateReviewString(field, value, true); err != nil {
			return err
		}
	}
	if err := validateReviewString("producer_session_ref", receipt.ProducerSessionRef, false); err != nil {
		return err
	}
	if receipt.ExtractionAttemptNumber < 1 || receipt.ProposalCount < 0 || len(receipt.ProducerSessionRef) > ProducerSessionRefMaxBytes || strings.TrimSpace(receipt.ProducerSessionRef) != receipt.ProducerSessionRef {
		return newDomainError(ErrorInvalidInput, "submission receipt count, attempt number, or session metadata is invalid")
	}
	expectedID, err := submissionReceiptID(receipt)
	if err != nil {
		return err
	}
	if expectedID != receipt.ID {
		return newDomainError(ErrorReviewContractConflict, "submission receipt identity drifted")
	}
	expectedRunID, err := sourceReviewRunID(receipt.RequestID, receipt.ExtractorDefinitionID, receipt.SourceSnapshotID, receipt.ExtractionViewID)
	if err != nil {
		return err
	}
	if expectedRunID != receipt.ExtractionRunID {
		return newDomainError(ErrorReviewContractConflict, "submission receipt extraction run identity drifted")
	}
	expectedAttemptID, err := sourceReviewAttemptID(receipt.ExtractionRunID, receipt.ExtractionAttemptNumber)
	if err != nil {
		return err
	}
	if expectedAttemptID != receipt.ExtractionAttemptID {
		return newDomainError(ErrorReviewContractConflict, "submission receipt extraction attempt identity drifted")
	}
	expectedBatchID, err := sourceReviewBatchID(receipt.ExtractionAttemptID)
	if err != nil {
		return err
	}
	if expectedBatchID != receipt.ProposalBatchID {
		return newDomainError(ErrorReviewContractConflict, "submission receipt proposal batch identity drifted")
	}
	return nil
}

func validateProposalBatchManifest(manifest ProposalBatchManifest) error {
	if manifest.ContractVersion != ReviewableIngestionContractV1 {
		return newDomainError(ErrorReviewContractConflict, "proposal manifest contract version %q is unsupported", manifest.ContractVersion)
	}
	for field, value := range map[string]string{
		"proposal_manifest_id":  manifest.ID,
		"proposal_batch_id":     manifest.ProposalBatchID,
		"extraction_attempt_id": manifest.ExtractionAttemptID,
		"extractor_output_hash": manifest.ExtractorOutputHash,
		"proposal_set_hash":     manifest.ProposalSetHash,
	} {
		if err := validateReviewString(field, value, true); err != nil {
			return err
		}
	}
	if manifest.Entries == nil || manifest.ProposalCount != len(manifest.Entries) {
		return newDomainError(ErrorReviewContractConflict, "proposal manifest count does not match an explicit entry list")
	}
	expectedBatchID, err := sourceReviewBatchID(manifest.ExtractionAttemptID)
	if err != nil {
		return err
	}
	if expectedBatchID != manifest.ProposalBatchID {
		return newDomainError(ErrorReviewContractConflict, "proposal manifest batch does not belong to its extraction attempt")
	}
	seenLocal := make(map[string]struct{}, len(manifest.Entries))
	seenOccurrence := make(map[string]struct{}, len(manifest.Entries))
	for index, entry := range manifest.Entries {
		if entry.Ordinal != index+1 || entry.ProposalKind != ProposalKindStatement || entry.ProposalFingerprintVersion != ProposalFingerprintStatementV1 {
			return newDomainError(ErrorReviewContractConflict, "proposal manifest entry %d is not canonical statement-v1 material", index+1)
		}
		for field, value := range map[string]string{
			"proposal_occurrence_id": entry.ProposalOccurrenceID,
			"proposal_local_id":      entry.ProposalLocalID,
			"proposal_fingerprint":   entry.ProposalFingerprint,
		} {
			if err := validateReviewString(field, value, true); err != nil {
				return err
			}
		}
		if _, duplicate := seenLocal[entry.ProposalLocalID]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "proposal manifest repeats local ID %q", entry.ProposalLocalID)
		}
		if _, duplicate := seenOccurrence[entry.ProposalOccurrenceID]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "proposal manifest repeats occurrence ID %q", entry.ProposalOccurrenceID)
		}
		expectedOccurrenceID, err := sourceReviewOccurrenceID(manifest.ExtractionAttemptID, manifest.ProposalBatchID, entry.ProposalLocalID)
		if err != nil {
			return err
		}
		if expectedOccurrenceID != entry.ProposalOccurrenceID {
			return newDomainError(ErrorReviewContractConflict, "manifest occurrence identity drifted for local ID %q", entry.ProposalLocalID)
		}
		if index > 0 && compareProposalBatchManifestEntries(manifest.Entries[index-1], entry) >= 0 {
			return newDomainError(ErrorReviewContractConflict, "proposal manifest entries are not in unique canonical order")
		}
		seenLocal[entry.ProposalLocalID] = struct{}{}
		seenOccurrence[entry.ProposalOccurrenceID] = struct{}{}
	}
	expectedID, err := proposalBatchManifestID(manifest)
	if err != nil {
		return err
	}
	if expectedID != manifest.ID {
		return newDomainError(ErrorReviewContractConflict, "proposal manifest identity drifted")
	}
	return nil
}

func manifestEntryForProposal(manifest ProposalBatchManifest, occurrenceID string) (ProposalBatchManifestEntry, error) {
	for _, entry := range manifest.Entries {
		if entry.ProposalOccurrenceID == occurrenceID {
			return entry, nil
		}
	}
	return ProposalBatchManifestEntry{}, newDomainError(ErrorReviewContractConflict, "proposal occurrence %q is absent from the complete manifest", occurrenceID)
}

func compareProposalBatchManifestEntries(left, right ProposalBatchManifestEntry) int {
	if compared := strings.Compare(left.ProposalLocalID, right.ProposalLocalID); compared != 0 {
		return compared
	}
	return strings.Compare(left.ProposalOccurrenceID, right.ProposalOccurrenceID)
}

func sortReviewSourceRefs(refs []ResolvedSourceRef) {
	slices.SortFunc(refs, func(left, right ResolvedSourceRef) int {
		for _, compared := range []int{
			strings.Compare(left.ExtractionViewID, right.ExtractionViewID),
			cmp.Compare(left.StartByte, right.StartByte),
			cmp.Compare(left.EndByte, right.EndByte),
			strings.Compare(left.QuotedTextHash, right.QuotedTextHash),
			strings.Compare(left.TargetKind, right.TargetKind),
			strings.Compare(left.RepositorySnapshotID, right.RepositorySnapshotID),
			strings.Compare(left.FileSnapshotID, right.FileSnapshotID),
			strings.Compare(left.RepoID, right.RepoID),
			strings.Compare(left.CommitSHA, right.CommitSHA),
			strings.Compare(left.Path, right.Path),
			strings.Compare(left.SpanID, right.SpanID),
			strings.Compare(left.QuotedText, right.QuotedText),
		} {
			if compared != 0 {
				return compared
			}
		}
		return 0
	})
}

func proposalBatchManifestID(manifest ProposalBatchManifest) (string, error) {
	return stableID("proposal-manifest:v1:sha256:", "proposal_batch_manifest", struct {
		ContractVersion     string                       `json:"contract_version"`
		ProposalBatchID     string                       `json:"proposal_batch_id"`
		ExtractionAttemptID string                       `json:"extraction_attempt_id"`
		ExtractorOutputHash string                       `json:"extractor_output_hash"`
		ProposalSetHash     string                       `json:"proposal_set_hash"`
		ProposalCount       int                          `json:"proposal_count"`
		Entries             []ProposalBatchManifestEntry `json:"entries"`
	}{manifest.ContractVersion, manifest.ProposalBatchID, manifest.ExtractionAttemptID, manifest.ExtractorOutputHash, manifest.ProposalSetHash, manifest.ProposalCount, manifest.Entries})
}

func submissionReceiptID(receipt SubmissionReceipt) (string, error) {
	return stableID("submission-receipt:v1:sha256:", "submission_receipt", struct {
		ContractVersion         string `json:"contract_version"`
		RequestID               string `json:"request_id"`
		SourceSnapshotID        string `json:"source_snapshot_id"`
		ExtractionViewID        string `json:"extraction_view_id"`
		ExtractorDefinitionID   string `json:"extractor_definition_id"`
		ExtractionRunID         string `json:"extraction_run_id"`
		ExtractionAttemptID     string `json:"extraction_attempt_id"`
		ExtractionAttemptNumber int    `json:"extraction_attempt_number"`
		ExtractorOutputHash     string `json:"extractor_output_hash"`
		ProposalBatchID         string `json:"proposal_batch_id"`
		ProposalManifestID      string `json:"proposal_manifest_id"`
		ProposalCount           int    `json:"proposal_count"`
	}{
		receipt.ContractVersion,
		receipt.RequestID,
		receipt.SourceSnapshotID,
		receipt.ExtractionViewID,
		receipt.ExtractorDefinitionID,
		receipt.ExtractionRunID,
		receipt.ExtractionAttemptID,
		receipt.ExtractionAttemptNumber,
		receipt.ExtractorOutputHash,
		receipt.ProposalBatchID,
		receipt.ProposalManifestID,
		receipt.ProposalCount,
	})
}

func proposalBasisID(basis ProposalBasis) (string, error) {
	copy := basis
	copy.ID = ""
	return stableID("proposal-basis:v1:sha256:", "proposal_basis", copy)
}

func reviewPackageID(reviewPackage ReviewPackage) (string, error) {
	copy := reviewPackage
	copy.ID = ""
	return stableID("review-package:v1:sha256:", "review_package", copy)
}

func reviewOriginMetadataHash(values map[string]string) (string, error) {
	data, err := deterministicJSON(cloneStringMap(values))
	if err != nil {
		return "", err
	}
	return contentHash(data), nil
}

func reviewSourceDisplayMetadata(proposal ProposalQueryResult) (string, string) {
	if proposal.SourceSystem != SourceSystemExternalDocument {
		return "", ""
	}
	return proposal.OriginMetadata[externalSourceOriginTitleKey], proposal.OriginMetadata[externalSourceOriginLocationKey]
}

func reviewSourceCompleteness(proposal ProposalQueryResult) (string, []string, error) {
	if proposal.SourceSystem != SourceSystemExternalDocument {
		return "", []string{}, nil
	}
	metadata := proposal.OriginMetadata
	if metadata[externalSourceOriginSchemaKey] != ExternalSourceEnvelopeSchemaV1 ||
		metadata[externalSourceOriginFidelityKey] != ExternalSourceContentFidelityVerbatim ||
		metadata[externalSourceOriginGlobalAbsenceKey] != "false" {
		return "", nil, newDomainError(ErrorReviewContractConflict, "external source review metadata does not preserve the source envelope boundary")
	}
	coverage := metadata[externalSourceOriginCoverageKey]
	rawLimitations, ok := metadata[externalSourceOriginLimitsKey]
	if !ok {
		return "", nil, newDomainError(ErrorReviewContractConflict, "external source review metadata omits limitations")
	}
	var limitations []string
	if err := json.Unmarshal([]byte(rawLimitations), &limitations); err != nil || limitations == nil {
		return "", nil, newDomainError(ErrorReviewContractConflict, "external source review limitations are not an explicit string array")
	}
	canonicalLimitations, err := json.Marshal(limitations)
	if err != nil {
		return "", nil, err
	}
	if string(canonicalLimitations) != rawLimitations {
		return "", nil, newDomainError(ErrorReviewContractConflict, "external source review limitations are not in their persisted canonical form")
	}
	if err := validateReviewSourceCompletenessValues(proposal.SourceSystem, coverage, limitations); err != nil {
		return "", nil, err
	}
	return coverage, append([]string{}, limitations...), nil
}

func validateReviewSourceBasisMetadata(basis ProposalBasis) error {
	return validateReviewSourceCompletenessValues(basis.SourceSystem, basis.SourceCoverage, basis.SourceLimitations)
}

func validateReviewSourceCompletenessValues(sourceSystem, coverage string, limitations []string) error {
	if limitations == nil {
		return newDomainError(ErrorReviewContractConflict, "source review limitations must be an explicit list")
	}
	if sourceSystem != SourceSystemExternalDocument {
		if coverage != "" || len(limitations) != 0 {
			return newDomainError(ErrorReviewContractConflict, "non-external source review cannot carry external coverage or limitations")
		}
		return nil
	}
	if len(limitations) > ExternalSourceLimitationsMaxCount {
		return newDomainError(ErrorReviewContractConflict, "external source review limitations exceed the source envelope bound")
	}
	for index, limitation := range limitations {
		if strings.TrimSpace(limitation) != limitation || limitation == "" || len(limitation) > ExternalSourceLimitationMaxBytes {
			return newDomainError(ErrorReviewContractConflict, "external source review limitation %d is not normalized and bounded", index)
		}
		if err := validateReviewString("source limitation", limitation, true); err != nil {
			return err
		}
	}
	switch coverage {
	case ExternalSourceCoverageFullDocument:
		if len(limitations) != 0 {
			return newDomainError(ErrorReviewContractConflict, "full external source coverage cannot carry limitations")
		}
	case ExternalSourceCoverageExactExcerpt, ExternalSourceCoverageTruncatedDocument:
		if len(limitations) == 0 {
			return newDomainError(ErrorReviewContractConflict, "%s external source coverage requires a limitation", coverage)
		}
	default:
		return newDomainError(ErrorReviewContractConflict, "external source review coverage %q is unsupported", coverage)
	}
	return nil
}

func sourceReviewRunID(requestID, extractorDefinitionID, sourceSnapshotID, extractionViewID string) (string, error) {
	return stableID("run:", "extraction_run", struct {
		RequestID             string `json:"request_id"`
		ExtractorDefinitionID string `json:"extractor_definition_id"`
		SourceSnapshotID      string `json:"source_snapshot_id"`
		ExtractionViewID      string `json:"extraction_view_id"`
	}{requestID, extractorDefinitionID, sourceSnapshotID, extractionViewID})
}

func sourceReviewAttemptID(extractionRunID string, attemptNumber int) (string, error) {
	return stableID("attempt:", "extraction_attempt", struct {
		ExtractionRunID string `json:"extraction_run_id"`
		AttemptNumber   int    `json:"attempt_number"`
	}{extractionRunID, attemptNumber})
}

func sourceReviewBatchID(extractionAttemptID string) (string, error) {
	return stableID("batch:", "proposal_batch", struct {
		ExtractionAttemptID string `json:"extraction_attempt_id"`
		BatchIndex          int    `json:"batch_index"`
	}{extractionAttemptID, 1})
}

func sourceReviewOccurrenceID(extractionAttemptID, proposalBatchID, proposalLocalID string) (string, error) {
	return stableID("occ:", "proposal_occurrence", struct {
		ExtractionAttemptID string `json:"extraction_attempt_id"`
		ProposalBatchID     string `json:"proposal_batch_id"`
		ProposalLocalID     string `json:"proposal_local_id"`
	}{extractionAttemptID, proposalBatchID, proposalLocalID})
}

func validateReviewString(field, value string, required bool) error {
	if value == "" {
		if required {
			return newDomainError(ErrorInvalidInput, "%s is required", field)
		}
		return nil
	}
	if !utf8.ValidString(value) {
		return newDomainError(ErrorInvalidUTF8, "%s is not valid UTF-8", field)
	}
	if strings.ContainsRune(value, '\x00') {
		return newDomainError(ErrorInvalidInput, "%s must not contain NUL", field)
	}
	return nil
}

func validateReviewStringMap(field string, values map[string]string) error {
	for key, value := range values {
		if err := validateReviewString(field+" key", key, true); err != nil {
			return err
		}
		if err := validateReviewString(field+" value", value, false); err != nil {
			return err
		}
	}
	return nil
}

func cloneProposalBatchManifest(manifest ProposalBatchManifest) ProposalBatchManifest {
	manifest.Entries = append([]ProposalBatchManifestEntry(nil), manifest.Entries...)
	if manifest.Entries == nil {
		manifest.Entries = []ProposalBatchManifestEntry{}
	}
	return manifest
}

func cloneReviewPackage(reviewPackage ReviewPackage) ReviewPackage {
	reviewPackage.ProposalBasis.ManualReviewProfile = cloneManualReviewProfile(reviewPackage.ProposalBasis.ManualReviewProfile)
	reviewPackage.ProposalBasis.SourceRefs = append([]ResolvedSourceRef(nil), reviewPackage.ProposalBasis.SourceRefs...)
	reviewPackage.ProposalBasis.SourceLimitations = append([]string(nil), reviewPackage.ProposalBasis.SourceLimitations...)
	if reviewPackage.ProposalBasis.SourceLimitations == nil {
		reviewPackage.ProposalBasis.SourceLimitations = []string{}
	}
	reviewPackage.Limitations = append([]string(nil), reviewPackage.Limitations...)
	if reviewPackage.Limitations == nil {
		reviewPackage.Limitations = []string{}
	}
	return reviewPackage
}

// materializedReviewProposalSetHash freezes the complete target-native immutable
// proposal set; its count is derived, not a new persisted or caller-supplied claim.
func materializedReviewProposalSetHash(occurrences []ProposalOccurrence) (string, error) {
	type immutableProposal struct {
		ID                         string                `json:"proposal_occurrence_id"`
		BatchID                    string                `json:"proposal_batch_id"`
		ExtractionAttemptID        string                `json:"extraction_attempt_id"`
		ProposalLocalID            string                `json:"proposal_local_id"`
		ProposalKind               string                `json:"proposal_kind"`
		StatementText              string                `json:"statement_text"`
		ProposalFingerprint        string                `json:"proposal_fingerprint"`
		ProposalFingerprintVersion string                `json:"proposal_fingerprint_version"`
		SourceRefs                 []ResolvedSourceRef   `json:"source_refs"`
		CodeFact                   *ResolvedCodeFact     `json:"code_fact,omitempty"`
		CodeRelation               *ResolvedCodeRelation `json:"code_relation,omitempty"`
	}
	items := make([]immutableProposal, 0, len(occurrences))
	for _, occurrence := range occurrences {
		refs := append([]ResolvedSourceRef(nil), occurrence.SourceRefs...)
		sortSourceRefs(refs)
		items = append(items, immutableProposal{
			ID:                         occurrence.ID,
			BatchID:                    occurrence.BatchID,
			ExtractionAttemptID:        occurrence.ExtractionAttemptID,
			ProposalLocalID:            occurrence.ProposalLocalID,
			ProposalKind:               occurrence.ProposalKind,
			StatementText:              occurrence.StatementText,
			ProposalFingerprint:        occurrence.ProposalFingerprint,
			ProposalFingerprintVersion: occurrence.ProposalFingerprintVersion,
			SourceRefs:                 refs,
			CodeFact:                   occurrence.CodeFact,
			CodeRelation:               occurrence.CodeRelation,
		})
	}
	slices.SortFunc(items, func(left, right immutableProposal) int {
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	data, err := deterministicJSON(struct {
		ContractVersion string              `json:"contract_version"`
		ProposalCount   int                 `json:"proposal_count"`
		Proposals       []immutableProposal `json:"proposals"`
	}{
		ContractVersion: "materialized-proposal-set/v1",
		ProposalCount:   len(items),
		Proposals:       items,
	})
	if err != nil {
		return "", fmt.Errorf("serializing materialized proposal set: %w", err)
	}
	return contentHash(data), nil
}

// ReviewableSourceClaimBatch retains the native materialization and its frozen
// complete-set commitment. It is review input, not persisted admission authority.
type ReviewableSourceClaimBatch struct {
	MaterializedBatch
	MaterializedProposalCount   int
	MaterializedProposalSetHash string
}

// materializeReviewableBatch preserves the set commitment before any caller can
// select or alter occurrences. PostgreSQL loaders also compare this complete set
// with the persisted proposal batch and its immutable fixture.
func materializeReviewableBatch(attempt attemptContext, fixture FrozenExtractorOutput) (ReviewableSourceClaimBatch, error) {
	batch, err := materializeBatch(attempt, fixture)
	if err != nil {
		return ReviewableSourceClaimBatch{}, err
	}
	setHash, err := materializedReviewProposalSetHash(batch.Occurrences)
	if err != nil {
		return ReviewableSourceClaimBatch{}, err
	}
	return ReviewableSourceClaimBatch{MaterializedBatch: batch, MaterializedProposalCount: len(batch.Occurrences), MaterializedProposalSetHash: setHash}, nil
}
