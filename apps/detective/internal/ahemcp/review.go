package ahemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

const reviewContract = "reviewable-ingestion/v1"
const reviewMediaType = "application/vnd.ahe.review-package.v1+json"

// SourceClaimReview retains the complete native one-statement review response.
// It is display material, not a human decision or an admission capability.
type SourceClaimReview struct {
	ContractVersion   string                  `json:"contract_version"`
	SubmissionReceipt ReviewSubmissionReceipt `json:"submission_receipt"`
	ProposalManifest  ReviewProposalManifest  `json:"proposal_manifest"`
	Display           ReviewDisplay           `json:"display"`
	Subject           ReviewSubject           `json:"subject"`
}

// ReviewSubmissionReceipt binds a completed native extraction to its batch.
type ReviewSubmissionReceipt struct {
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

// ReviewProposalManifest is the complete batch, never a page or approval set.
type ReviewProposalManifest struct {
	ContractVersion     string                `json:"contract_version"`
	ID                  string                `json:"proposal_manifest_id"`
	ProposalBatchID     string                `json:"proposal_batch_id"`
	ExtractionAttemptID string                `json:"extraction_attempt_id"`
	ExtractorOutputHash string                `json:"extractor_output_hash"`
	ProposalSetHash     string                `json:"proposal_set_hash"`
	ProposalCount       int                   `json:"proposal_count"`
	Entries             []ReviewManifestEntry `json:"entries"`
}

// ReviewManifestEntry identifies one occurrence in the native batch.
type ReviewManifestEntry struct {
	Ordinal                    int    `json:"ordinal"`
	ProposalOccurrenceID       string `json:"proposal_occurrence_id"`
	ProposalLocalID            string `json:"proposal_local_id"`
	ProposalFingerprint        string `json:"proposal_fingerprint"`
	ProposalFingerprintVersion string `json:"proposal_fingerprint_version"`
	ProposalKind               string `json:"proposal_kind"`
}

// ReviewDisplay preserves the exact native UTF-8 display commitment.
type ReviewDisplay struct {
	ContractVersion string `json:"contract_version"`
	ID              string `json:"review_display_artifact_id"`
	ReviewPackageID string `json:"review_package_id"`
	MediaType       string `json:"media_type"`
	PayloadUTF8     string `json:"payload_utf8"`
}

// ReviewSubject binds all native review coordinates to the exact display bytes.
type ReviewSubject struct {
	ReviewSubject           ExactReviewSubject `json:"review_subject"`
	ReviewDisplayArtifactID string             `json:"review_display_artifact_id"`
}

// ExactReviewSubject names the five native identities required by the writer.
type ExactReviewSubject struct {
	SubmissionReceiptID  string `json:"submission_receipt_id"`
	ProposalManifestID   string `json:"proposal_manifest_id"`
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	ProposalBasisID      string `json:"proposal_basis_id"`
	ReviewPackageID      string `json:"review_package_id"`
}

// ReviewedAdmission is a native writer acknowledgement, not independent Query
// verification and not proof that a human was authenticated or read the card.
type ReviewedAdmission struct {
	ProposalOccurrenceID string   `json:"proposal_occurrence_id"`
	AdmissionDecisionID  string   `json:"admission_decision_id"`
	AdmissionOutcome     string   `json:"admission_outcome"`
	CanonicalRef         string   `json:"canonical_ref"`
	RawEvidenceNodeIDs   []string `json:"raw_evidence_node_ids"`
	CanonicalEdgeIDs     []string `json:"canonical_edge_ids"`
	DerivationID         string   `json:"derivation_id,omitempty"`
	ParentNodeIDs        []string `json:"parent_node_ids,omitempty"`
	Replayed             bool     `json:"replayed"`
}

type reviewPackage struct {
	ContractVersion     string      `json:"contract_version"`
	ID                  string      `json:"review_package_id"`
	ReviewTemplateID    string      `json:"review_template_id"`
	SubmissionReceiptID string      `json:"submission_receipt_id"`
	ProposalBasis       reviewBasis `json:"proposal_basis"`
	ProposedEffect      string      `json:"proposed_effect"`
	Coverage            string      `json:"coverage"`
	Limitations         []string    `json:"limitations"`
}

type reviewBasis struct {
	ContractVersion            string             `json:"contract_version"`
	ID                         string             `json:"proposal_basis_id"`
	SubmissionReceiptID        string             `json:"submission_receipt_id"`
	ProposalManifestID         string             `json:"proposal_manifest_id"`
	ProposalManifestOrdinal    int                `json:"proposal_manifest_ordinal"`
	ProposalOccurrenceID       string             `json:"proposal_occurrence_id"`
	ProposalLocalID            string             `json:"proposal_local_id"`
	ProposalFingerprint        string             `json:"proposal_fingerprint"`
	ProposalFingerprintVersion string             `json:"proposal_fingerprint_version"`
	ProposalKind               string             `json:"proposal_kind"`
	StatementText              string             `json:"statement_text"`
	SourceRefs                 []pendingSourceRef `json:"source_refs"`
	SourceSnapshotID           string             `json:"source_snapshot_id"`
	ExtractionViewID           string             `json:"extraction_view_id"`
	SourceSystem               string             `json:"source_system"`
	SourceID                   string             `json:"source_id"`
	SourceVersion              string             `json:"source_version"`
	RawContentHash             string             `json:"raw_content_hash"`
	RendererName               string             `json:"renderer_name"`
	RendererVersion            string             `json:"renderer_version"`
	RenderedContentHash        string             `json:"rendered_content_hash"`
	OriginMetadataHash         string             `json:"origin_metadata_hash"`
	SourceTitle                string             `json:"source_title,omitempty"`
	SourceLocation             string             `json:"source_location,omitempty"`
	SourceCoverage             string             `json:"source_coverage,omitempty"`
	SourceLimitations          []string           `json:"source_limitations"`
	ExtractorDefinitionID      string             `json:"extractor_definition_id"`
	ExtractionRunID            string             `json:"extraction_run_id"`
	ExtractionAttemptID        string             `json:"extraction_attempt_id"`
	ExtractorName              string             `json:"extractor_name"`
	ExtractorVersion           string             `json:"extractor_version"`
	ExtractorConfigHash        string             `json:"extractor_config_hash"`
	SourceBindingKind          string             `json:"source_binding_kind"`
}

// ReviewSourceClaim reads a complete pending-only review from an explicitly
// selected reviewer launcher. No decision or write is sent.
func ReviewSourceClaim(ctx context.Context, command, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, handoff Handoff) (SourceClaimReview, error) {
	if ctx == nil {
		return SourceClaimReview{}, errors.New("review context is required")
	}
	if err := validateReviewInputs(sourceID, document, extractor, records, handoff); err != nil {
		return SourceClaimReview{}, err
	}
	c, err := startReviewer(ctx, command)
	if err != nil {
		return SourceClaimReview{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return SourceClaimReview{}, err
	}
	var review SourceClaimReview
	if err := c.call("get_source_claim_review", map[string]string{"extraction_attempt_id": handoff.ExtractionAttemptID, "proposal_occurrence_id": handoff.ProposalOccurrenceID}, &review); err != nil {
		return SourceClaimReview{}, err
	}
	if err := ValidateSourceClaimReview(review, sourceID, document, extractor, records, handoff); err != nil {
		return SourceClaimReview{}, err
	}
	if err := c.Close(); err != nil {
		return SourceClaimReview{}, errors.New("review launcher did not exit cleanly")
	}
	return review, nil
}

func startReviewer(ctx context.Context, command string) (*client, error) {
	c, err := start(ctx, command)
	if err != nil {
		return nil, err
	}
	c.reviewerOnly = true
	return c, nil
}

func validateReviewInputs(sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, handoff Handoff) error {
	if err := ValidateSubmission(sourceID, document, extractor, records); err != nil {
		return err
	}
	if len(records) != 1 || handoff.SchemaVersion != "ahe-mcp-pending-handoff/v0" || handoff.Status != "pending" || handoff.ProposalCount != 1 {
		return errors.New("review requires one validated candidate and its pending receipt")
	}
	for _, field := range []struct{ value, prefix string }{{handoff.SourceSnapshotID, "srcsnap:"}, {handoff.ExtractionViewID, "view:"}, {handoff.ExtractionAttemptID, "attempt:"}, {handoff.ProposalOccurrenceID, "occ:"}} {
		if !reviewID(field.value, field.prefix) {
			return errors.New("review requires exact native handoff coordinates")
		}
	}
	return nil
}

// ValidateSourceClaimReview checks the complete display against saved local
// source/candidate bytes. It does not authenticate a server or a human reviewer.
func ValidateSourceClaimReview(review SourceClaimReview, sourceID string, document *labstatus.Document, extractor labstatus.ExtractorInfo, records []labstatus.Record, handoff Handoff) error {
	if err := validateReviewInputs(sourceID, document, extractor, records, handoff); err != nil {
		return err
	}
	pkg, err := validateReviewEnvelope(review)
	if err != nil {
		return err
	}
	b := pkg.ProposalBasis
	r := review.SubmissionReceipt
	if r.SourceSnapshotID != handoff.SourceSnapshotID || r.ExtractionViewID != handoff.ExtractionViewID || r.ExtractionAttemptID != handoff.ExtractionAttemptID || b.ProposalOccurrenceID != handoff.ProposalOccurrenceID {
		return errors.New("review receipt differs from the saved handoff")
	}
	// Reuse the strict pending projection without weakening its public caller.
	var projection pendingRecord
	projection.RecordRef.Kind, projection.RecordRef.ID = "proposal", b.ProposalOccurrenceID
	projection.ProposalLocalID, projection.ProposalKind = b.ProposalLocalID, b.ProposalKind
	projection.ProposalFingerprint, projection.ProposalFingerprintVersion = b.ProposalFingerprint, b.ProposalFingerprintVersion
	projection.StatementText, projection.AdmissionOutcome, projection.CanonicalRef = b.StatementText, "pending", json.RawMessage("null")
	projection.Source.BindingKind, projection.Source.SourceSnapshotID, projection.Source.SourceSystem = b.SourceBindingKind, b.SourceSnapshotID, b.SourceSystem
	projection.Source.SourceID, projection.Source.SourceVersion, projection.Source.RawContentHash = b.SourceID, b.SourceVersion, b.RawContentHash
	projection.SourceRefs = b.SourceRefs
	projection.ExtractionViewID, projection.RendererName, projection.RendererVersion, projection.RenderedContentHash = b.ExtractionViewID, b.RendererName, b.RendererVersion, b.RenderedContentHash
	projection.Extractor.ExtractorDefinitionID, projection.Extractor.ExtractionRunID, projection.Extractor.ExtractionAttemptID = b.ExtractorDefinitionID, b.ExtractionRunID, b.ExtractionAttemptID
	projection.Extractor.Name, projection.Extractor.Version, projection.Extractor.ConfigHash, projection.Extractor.AttemptStatus = b.ExtractorName, b.ExtractorVersion, b.ExtractorConfigHash, "succeeded"
	if err := verifyPendingRecord(projection, sourceID, document, extractor, records[0], handoff); err != nil {
		return err
	}
	if b.OriginMetadataHash != "sha256:"+digest(map[string]string{"capture_kind": "frozen_local_snapshot", "snapshot_sha256": document.Source().SHA256}) {
		return errors.New("review origin metadata differs from the frozen snapshot")
	}
	spans := make([]span, 0, len(b.SourceRefs))
	for i, ref := range b.SourceRefs {
		spans = append(spans, span{SpanID: ref.SpanID, DisplayLine: records[0].Citation.StartLine + i})
	}
	proposals, err := proposalsFor(extractor, records, spans)
	if err != nil {
		return err
	}
	definition := extractorDefinition{Name: extractor.Name, Version: extractor.Version, Config: map[string]string{"model": extractor.Model, "source_mode": "frozen_local_snapshot"}}
	if r.RequestID != requestID("proposal", sourceID, document.Source().SHA256, digest(definition), digest(proposals)) {
		return errors.New("review extraction request differs from the saved candidate")
	}
	if r.ExtractorOutputHash != "sha256:"+digest(extractorOutput{Proposals: proposals}) {
		return errors.New("review extractor output differs from the saved candidate")
	}
	return nil
}

func validateReviewEnvelope(review SourceClaimReview) (reviewPackage, error) {
	bad := func() (reviewPackage, error) {
		return reviewPackage{}, errors.New("review display, manifest or subject does not match the bounded native contract")
	}
	encoded, err := json.Marshal(review)
	if err != nil || len(encoded) > 1<<20 || review.ContractVersion != "source-claim-review-mcp/v1" {
		return bad()
	}
	d, r, m, s := review.Display, review.SubmissionReceipt, review.ProposalManifest, review.Subject.ReviewSubject
	if d.ContractVersion != "review-package-display/v1" || d.MediaType != reviewMediaType || len(d.PayloadUTF8) > 128<<10 || d.ID != "review-display:v1:"+contentHash(d.MediaType+"\n"+d.PayloadUTF8) || review.Subject.ReviewDisplayArtifactID != d.ID {
		return bad()
	}
	var p reviewPackage
	if decodeRPCJSON([]byte(d.PayloadUTF8), &p, true) != nil {
		return bad()
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, []byte(d.PayloadUTF8)) {
		return bad()
	}
	b := p.ProposalBasis
	if r.ContractVersion != reviewContract || m.ContractVersion != reviewContract || p.ContractVersion != reviewContract || b.ContractVersion != reviewContract || p.ReviewTemplateID != "source-claim-review-card/v1" || p.ProposedEffect != "admit-source-backed-statement/v1" || p.Coverage != "One pending source-backed statement and every exact source span bound to it." || !slices.Equal(p.Limitations, reviewLimitations()) {
		return bad()
	}
	if r.ProducerSessionRef != "" || r.ExtractionAttemptNumber != 1 || r.ProposalCount != 1 || m.ProposalCount != 1 || len(m.Entries) != 1 || r.ProposalManifestID != m.ID || r.ProposalBatchID != m.ProposalBatchID || r.ExtractionAttemptID != m.ExtractionAttemptID || r.ExtractorOutputHash != m.ExtractorOutputHash {
		return bad()
	}
	e := m.Entries[0]
	if e.Ordinal != 1 || e.ProposalLocalID != "record-1" || e.ProposalKind != "statement" || e.ProposalFingerprintVersion != "statement-v1" || b.ProposalManifestOrdinal != 1 || b.ProposalLocalID != e.ProposalLocalID || b.ProposalFingerprint != e.ProposalFingerprint || b.ProposalFingerprintVersion != e.ProposalFingerprintVersion || b.ProposalKind != e.ProposalKind || b.ProposalOccurrenceID != e.ProposalOccurrenceID {
		return bad()
	}
	if s.SubmissionReceiptID != r.ID || p.SubmissionReceiptID != r.ID || b.SubmissionReceiptID != r.ID || s.ProposalManifestID != m.ID || b.ProposalManifestID != m.ID || s.ProposalOccurrenceID != b.ProposalOccurrenceID || s.ProposalBasisID != b.ID || s.ReviewPackageID != p.ID || d.ReviewPackageID != p.ID {
		return bad()
	}
	if b.SourceSnapshotID != r.SourceSnapshotID || b.ExtractionViewID != r.ExtractionViewID || b.ExtractorDefinitionID != r.ExtractorDefinitionID || b.ExtractionRunID != r.ExtractionRunID || b.ExtractionAttemptID != r.ExtractionAttemptID || b.SourceSystem != "manual_text" || b.SourceBindingKind != "source_snapshot" || b.SourceTitle != "" || b.SourceLocation != "" || b.SourceCoverage != "" || b.SourceLimitations == nil || len(b.SourceLimitations) != 0 || len(b.SourceRefs) == 0 {
		return bad()
	}
	for _, field := range []struct{ value, prefix string }{{r.ID, "submission-receipt:v1:sha256:"}, {m.ID, "proposal-manifest:v1:sha256:"}, {p.ID, "review-package:v1:sha256:"}, {b.ID, "proposal-basis:v1:sha256:"}, {b.ProposalOccurrenceID, "occ:"}, {b.ProposalFingerprint, "fp:"}, {r.SourceSnapshotID, "srcsnap:"}, {r.ExtractionViewID, "view:"}, {r.ExtractorDefinitionID, "extractor:"}, {r.ExtractionRunID, "run:"}, {r.ExtractionAttemptID, "attempt:"}, {r.ExtractorOutputHash, "sha256:"}, {r.ProposalBatchID, "batch:"}, {m.ProposalSetHash, "sha256:"}} {
		if !reviewID(field.value, field.prefix) {
			return bad()
		}
	}
	return p, nil
}

func reviewLimitations() []string {
	return []string{
		"The package does not prove that the proposed statement is true.",
		"The package does not authenticate a reviewer or prove that a cooperating agent displayed it.",
		"The package covers only the named immutable source snapshot and extraction view.",
		"Relation, derivation, candidate, supersession, and repository-bound semantics are outside this contract.",
	}
}

func reviewID(value, prefix string) bool {
	return hexID(value, prefix, 64)
}

func hexID(value, prefix string, size int) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+size {
		return false
	}
	for _, c := range value[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// AdmitSourceClaim sends exactly the saved subject with an explicit reason.
// Callers must first obtain an explicit human decision for the complete display
// and durably save that decision. A transport error does not imply rollback.
// Exact retries intentionally do not call the pending-only review reader.
func AdmitSourceClaim(ctx context.Context, command string, review SourceClaimReview, reason string) (ReviewedAdmission, error) {
	if ctx == nil {
		return ReviewedAdmission{}, errors.New("review context is required")
	}
	p, err := validateReviewEnvelope(review)
	if err != nil {
		return ReviewedAdmission{}, err
	}
	if !validText(reason, 2000) || strings.TrimSpace(reason) != reason {
		return ReviewedAdmission{}, errors.New("review reason requires 1 to 2000 normalized UTF-8 bytes without NUL")
	}
	c, err := startReviewer(ctx, command)
	if err != nil {
		return ReviewedAdmission{}, err
	}
	defer c.Close()
	if err := c.initialize(); err != nil {
		return ReviewedAdmission{}, err
	}
	request := struct {
		ExtractionAttemptID string        `json:"extraction_attempt_id"`
		ExpectedSubject     ReviewSubject `json:"expected_subject"`
		Decision            string        `json:"decision"`
		DecisionReason      string        `json:"decision_reason"`
	}{review.SubmissionReceipt.ExtractionAttemptID, review.Subject, "approved", reason}
	var result ReviewedAdmission
	if err := c.call("admit_reviewed_source_claim", request, &result); err != nil {
		return ReviewedAdmission{}, fmt.Errorf("reviewed write outcome uncertain; preserve the exact decision for retry: %w", err)
	}
	if err := validateReviewedAdmission(result, p.ProposalBasis.ProposalOccurrenceID, len(p.ProposalBasis.SourceRefs)); err != nil {
		return ReviewedAdmission{}, err
	}
	if err := c.Close(); err != nil {
		return ReviewedAdmission{}, errors.New("reviewed admission acknowledged but launcher exit failed; preserve exact decision for retry")
	}
	return result, nil
}

func validateReviewedAdmission(result ReviewedAdmission, occurrenceID string, refCount int) error {
	if result.ProposalOccurrenceID != occurrenceID || result.AdmissionOutcome != "admitted" || !reviewID(result.AdmissionDecisionID, "adm:") || !hexID(result.CanonicalRef, "canon-node:", 16) || result.DerivationID != "" || len(result.ParentNodeIDs) != 0 || len(result.RawEvidenceNodeIDs) != refCount || len(result.CanonicalEdgeIDs) != refCount {
		return errors.New("reviewed admission acknowledgement is outside the exact source-claim contract; write outcome uncertain")
	}
	seen := map[string]bool{result.CanonicalRef: true}
	for _, group := range []struct {
		values []string
		prefix string
	}{{result.RawEvidenceNodeIDs, "canon-node:"}, {result.CanonicalEdgeIDs, "canon-edge:"}} {
		for _, id := range group.values {
			if !hexID(id, group.prefix, 16) || seen[id] {
				return errors.New("reviewed admission acknowledgement contains invalid or repeated identities; write outcome uncertain")
			}
			seen[id] = true
		}
	}
	return nil
}
