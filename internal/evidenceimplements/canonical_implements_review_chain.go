package evidenceimplements

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

const (
	// ReviewEvidenceContractVersion preserves the retained review contract value.
	ReviewEvidenceContractVersion = "lab-canonical-implements-review-evidence/v1"
	// ReviewDisplayContractVersion preserves the retained review contract value.
	ReviewDisplayContractVersion = "lab-canonical-implements-review-display/v1"
	// ReviewedContractVersion preserves the retained review contract value.
	ReviewedContractVersion = "lab-canonical-implements-review-receipt/v2"
	// ReviewMediaType preserves the retained review contract value.
	ReviewMediaType = "application/vnd.ahe.lab-implements-review.v1+json"
	// MaxReviewSourceBytes preserves the retained review contract value.
	MaxReviewSourceBytes = 1 << 20
	// MaxReviewSourceRefs preserves the retained review contract value.
	MaxReviewSourceRefs = 64
	// MaxReviewManifestEntries preserves the retained review contract value.
	MaxReviewManifestEntries = 205
	// MaxReviewDisplayBytes preserves the retained review contract value.
	MaxReviewDisplayBytes = 128 << 10
)

// ErrReviewDerivedUnsupported identifies a rejected pure review contract.
var ErrReviewDerivedUnsupported = errors.New("implements review chain requires a source_claim specification")

// The controller supplies this complete original source basis independently
// of the candidate or incoming review. It is not an admission/freshness proof.
// ReviewEvidenceInput is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewEvidenceInput struct {
	AdmittedCutID       string
	SpecificationNodeID string
	ReviewSnapshot      evidenceingestion.ReviewableSourceClaimReviewSnapshot
	RawText             string
	RenderedText        string
}

// ReviewEvidenceSnapshot is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewEvidenceSnapshot struct {
	ContractVersion     string                                                `json:"contract_version"`
	ID                  string                                                `json:"id"`
	AdmittedCutID       string                                                `json:"admitted_cut_id"`
	SpecificationNodeID string                                                `json:"specification_node_id"`
	ReviewSnapshot      evidenceingestion.ReviewableSourceClaimReviewSnapshot `json:"review_snapshot"`
	RawText             string                                                `json:"raw_text"`
	RenderedText        string                                                `json:"rendered_text"`
}

// No proposer method, score or candidate path can substitute for Witnesses.
// The asserted witness meaning remains a separate, unauthenticated judgment.
// ReviewMapping is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewMapping struct {
	ProposalSentence string    `json:"proposal_sentence"`
	Witnesses        []Witness `json:"witnesses"`
	Coverage         string    `json:"coverage"`
	Limitations      []string  `json:"limitations"`
}

// ReviewPackage is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewPackage struct {
	ContractVersion            string                                             `json:"contract_version"`
	ID                         string                                             `json:"id"`
	ReportID                   string                                             `json:"report_id"`
	Candidate                  Candidate                                          `json:"candidate"`
	EvidenceSnapshotID         string                                             `json:"evidence_snapshot_id"`
	SpecificationSourceDisplay evidenceingestion.SourceClaimReviewDisplayArtifact `json:"specification_source_display"`
	SpecificationSourceSubject evidenceingestion.ExactDisplayedReviewSubject      `json:"specification_source_subject"`
	Mapping                    ReviewMapping                                      `json:"mapping"`
	Limitations                []string                                           `json:"limitations"`
}

// ReviewDisplayArtifact is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewDisplayArtifact struct {
	ContractVersion string `json:"contract_version"`
	ID              string `json:"id"`
	MediaType       string `json:"media_type"`
	PayloadUTF8     string `json:"payload_utf8"`
}

// ExactReviewSubject is a versioned pure review value; constructing it does not grant canonical write authority.
type ExactReviewSubject struct {
	AdmittedCutID      string `json:"admitted_cut_id"`
	ReportID           string `json:"report_id"`
	CandidateID        string `json:"candidate_id"`
	EvidenceSnapshotID string `json:"evidence_snapshot_id"`
	ReviewPackageID    string `json:"review_package_id"`
	DisplayArtifactID  string `json:"display_artifact_id"`
}

// ReviewedInput is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewedInput struct {
	CandidateID      string
	Mapping          ReviewMapping
	DisplayArtifact  ReviewDisplayArtifact
	DisplayedSubject ExactReviewSubject
	ReviewerID       string
	DecisionReason   string
}

// This is a distinct receipt, not an embedded v1 receipt that could bypass the
// new chain. Its only projected edge names this v2 receipt as provenance.
// ReviewedReceipt is a versioned pure review value; constructing it does not grant canonical write authority.
type ReviewedReceipt struct {
	ContractVersion  string                `json:"contract_version"`
	ID               string                `json:"id"`
	Authority        string                `json:"authority"`
	Direction        string                `json:"direction"`
	SemanticEffect   string                `json:"semantic_effect"`
	PolicyEffect     string                `json:"policy_effect"`
	Mapping          ReviewMapping         `json:"mapping"`
	DisplayArtifact  ReviewDisplayArtifact `json:"display_artifact"`
	DisplayedSubject ExactReviewSubject    `json:"displayed_subject"`
	ReviewerID       string                `json:"reviewer_id"`
	DecisionReason   string                `json:"decision_reason"`
	ProjectedEdge    ProjectedEdge         `json:"projected_edge"`
}

// NewReviewEvidenceSnapshot applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func NewReviewEvidenceSnapshot(cut AdmittedCut, input ReviewEvidenceInput) (ReviewEvidenceSnapshot, error) {
	if err := ValidateReviewEvidenceBounds(input); err != nil {
		return ReviewEvidenceSnapshot{}, err
	}
	if len(cut.Specifications) > MaxCutNodes || len(cut.Implementations) > MaxCutNodes {
		return ReviewEvidenceSnapshot{}, ErrInvalid
	}
	if err := ValidateAdmittedCut(cut); err != nil {
		return ReviewEvidenceSnapshot{}, err
	}
	specification, found := FindSpecification(cut.Specifications, input.SpecificationNodeID)
	if input.AdmittedCutID != cut.ID || !found {
		return ReviewEvidenceSnapshot{}, ErrEndpointMissing
	}
	if specification.NodeKind != evidencegraph.CanonicalSourceClaim {
		return ReviewEvidenceSnapshot{}, ErrReviewDerivedUnsupported
	}
	if _, _, err := evidenceingestion.BuildSourceClaimReviewDisplayArtifact(input.ReviewSnapshot); err != nil {
		return ReviewEvidenceSnapshot{}, fmt.Errorf("%w: invalid original specification review basis", ErrInvalid)
	}
	basis := input.ReviewSnapshot.ReviewPackage.ProposalBasis
	if basis.StatementText != specification.ClaimText || ExcerptHash(basis.StatementText) != specification.ClaimHash ||
		basis.SourceTitle != specification.SourceTitle || basis.SourceLocation != specification.SourceLocation || basis.SourceVersion != specification.SourceRevision ||
		basis.RawContentHash != ExcerptHash(input.RawText) || basis.RenderedContentHash != ExcerptHash(input.RenderedText) {
		return ReviewEvidenceSnapshot{}, fmt.Errorf("%w: specification source identity or bytes differ", ErrInvalid)
	}
	for _, ref := range basis.SourceRefs {
		if ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte > len(input.RenderedText) ||
			!utf8.ValidString(input.RenderedText[:ref.StartByte]) || !utf8.ValidString(input.RenderedText[:ref.EndByte]) ||
			input.RenderedText[ref.StartByte:ref.EndByte] != ref.QuotedText || ExcerptHash(ref.QuotedText) != ref.QuotedTextHash {
			return ReviewEvidenceSnapshot{}, fmt.Errorf("%w: original specification span differs from source bytes", ErrInvalid)
		}
	}
	snapshot := ReviewEvidenceSnapshot{ContractVersion: ReviewEvidenceContractVersion,
		AdmittedCutID: cut.ID, SpecificationNodeID: specification.NodeID, ReviewSnapshot: input.ReviewSnapshot,
		RawText: input.RawText, RenderedText: input.RenderedText}
	var err error
	snapshot.ID, err = Digest("canonical-implements-review-evidence:v1", snapshot)
	if err != nil {
		return ReviewEvidenceSnapshot{}, err
	}
	snapshot.ReviewSnapshot.ProposalManifest.Entries = slices.Clone(input.ReviewSnapshot.ProposalManifest.Entries)
	snapshot.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceRefs = slices.Clone(basis.SourceRefs)
	snapshot.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceLimitations = slices.Clone(basis.SourceLimitations)
	snapshot.ReviewSnapshot.ReviewPackage.Limitations = slices.Clone(input.ReviewSnapshot.ReviewPackage.Limitations)
	return snapshot, nil
}

// ValidateReviewEvidenceSnapshot applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateReviewEvidenceSnapshot(cut AdmittedCut, expected ReviewEvidenceSnapshot) error {
	want, err := NewReviewEvidenceSnapshot(cut, ReviewEvidenceInput{
		AdmittedCutID: expected.AdmittedCutID, SpecificationNodeID: expected.SpecificationNodeID, ReviewSnapshot: expected.ReviewSnapshot,
		RawText: expected.RawText, RenderedText: expected.RenderedText})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, expected) {
		return fmt.Errorf("%w: specification evidence snapshot is not canonical", ErrInvalid)
	}
	return nil
}

// BuildReviewDisplay applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func BuildReviewDisplay(cut AdmittedCut, report CandidateReport, candidateID string,
	expected ReviewEvidenceSnapshot, mapping ReviewMapping,
) (ReviewDisplayArtifact, ExactReviewSubject, error) {
	fail := func(err error) (ReviewDisplayArtifact, ExactReviewSubject, error) {
		return ReviewDisplayArtifact{}, ExactReviewSubject{}, err
	}
	if err := ValidateReviewEvidenceSnapshot(cut, expected); err != nil {
		return fail(err)
	}
	if len(report.Candidates) > MaxCandidatesPerReport || len(report.Gaps) > MaxCandidateGapRecords || len(report.Truncations) > MaxCutNodes {
		return fail(ErrInvalid)
	}
	if err := ValidateCandidateReport(cut, report); err != nil {
		return fail(err)
	}
	index := slices.IndexFunc(report.Candidates, func(candidate Candidate) bool { return candidate.ID == candidateID })
	if index < 0 || report.Candidates[index].Specification.NodeID != expected.SpecificationNodeID {
		return fail(ErrEndpointMissing)
	}
	candidate := report.Candidates[index]
	normalized, err := NormalizeReviewMapping(candidate, mapping)
	if err != nil {
		return fail(err)
	}
	sourceDisplay, sourceSubject, err := evidenceingestion.BuildSourceClaimReviewDisplayArtifact(expected.ReviewSnapshot)
	if err != nil {
		return fail(fmt.Errorf("%w: specification display rebuild failed", ErrInvalid))
	}
	review := ReviewPackage{ContractVersion: ReviewDisplayContractVersion, ReportID: report.ID,
		Candidate: candidate, EvidenceSnapshotID: expected.ID, SpecificationSourceDisplay: sourceDisplay, SpecificationSourceSubject: sourceSubject,
		Mapping: normalized, Limitations: []string{
			"Only the complete controller-pinned original source basis is covered; no current admission, pending state, source authenticity or database freshness is proved.",
			"The historical source-claim display is original evidence material, not a new source-claim admission request.",
			"Candidate lexical/symbol paths locate review material and are not relation witnesses; deliberate semantic mislabeling of an allowlisted witness is not mechanically detected.",
			"Exact display identity proves byte binding, not that an interface rendered it or an authenticated human approved it.",
			"This Lab receipt has no runtime writer, MCP, truth, support, status, supersession or currentness effect.",
		}}
	review.ID, err = Digest("canonical-implements-review-package:v1", review)
	if err != nil {
		return fail(err)
	}
	payload, err := json.Marshal(review)
	if err != nil || len(payload) > MaxReviewDisplayBytes {
		return fail(fmt.Errorf("%w: exact implements display exceeds its bound", ErrInvalid))
	}
	artifact := ReviewDisplayArtifact{ContractVersion: ReviewDisplayContractVersion,
		MediaType: ReviewMediaType, PayloadUTF8: string(payload)}
	artifact.ID, err = Digest("canonical-implements-review-display:v1", artifact)
	if err != nil {
		return fail(err)
	}
	subject := ExactReviewSubject{AdmittedCutID: cut.ID, ReportID: report.ID, CandidateID: candidate.ID,
		EvidenceSnapshotID: expected.ID, ReviewPackageID: review.ID, DisplayArtifactID: artifact.ID}
	return artifact, subject, nil
}

// NewReviewedReceipt applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func NewReviewedReceipt(cut AdmittedCut, report CandidateReport,
	expected ReviewEvidenceSnapshot, input ReviewedInput,
) (ReviewedReceipt, error) {
	artifact, subject, err := BuildReviewDisplay(cut, report, input.CandidateID, expected, input.Mapping)
	if err != nil {
		return ReviewedReceipt{}, err
	}
	if !reflect.DeepEqual(input.DisplayArtifact, artifact) || input.DisplayedSubject != subject {
		return ReviewedReceipt{}, fmt.Errorf("%w: exact relation review display or subject changed", ErrInvalid)
	}
	index := slices.IndexFunc(report.Candidates, func(candidate Candidate) bool { return candidate.ID == input.CandidateID })
	candidate := report.Candidates[index] // The rebuilt display already proved membership.
	validated, err := NewReceipt(cut, Input{AdmittedCutID: cut.ID,
		SpecificationNodeID: candidate.Specification.NodeID, ImplementationNodeID: candidate.Implementation.NodeID,
		ProposalSentence: input.Mapping.ProposalSentence, Witnesses: input.Mapping.Witnesses, Coverage: input.Mapping.Coverage,
		Limitations: input.Mapping.Limitations, ReviewerID: input.ReviewerID, DecisionReason: input.DecisionReason})
	if err != nil {
		return ReviewedReceipt{}, err
	}
	receipt := ReviewedReceipt{ContractVersion: ReviewedContractVersion,
		Authority: Authority, Direction: Direction, SemanticEffect: Effect,
		PolicyEffect: PolicyEffect, DisplayArtifact: artifact, DisplayedSubject: subject,
		Mapping: ReviewMapping{ProposalSentence: validated.ProposalSentence, Witnesses: validated.Witnesses,
			Coverage: validated.Coverage, Limitations: validated.Limitations}, ReviewerID: validated.ReviewerID, DecisionReason: validated.DecisionReason}
	receipt.ID, err = Digest("canonical-implements-reviewed-receipt:v2", receipt)
	if err != nil {
		return ReviewedReceipt{}, err
	}
	receipt.ProjectedEdge = validated.ProjectedEdge
	receipt.ProjectedEdge.ProvenanceReceiptID = receipt.ID
	return receipt, nil
}

// ValidateReviewedReceipt applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateReviewedReceipt(cut AdmittedCut, report CandidateReport,
	expected ReviewEvidenceSnapshot, receipt ReviewedReceipt,
) error {
	want, err := NewReviewedReceipt(cut, report, expected, ReviewedInput{
		CandidateID: receipt.DisplayedSubject.CandidateID, Mapping: receipt.Mapping, DisplayArtifact: receipt.DisplayArtifact,
		DisplayedSubject: receipt.DisplayedSubject, ReviewerID: receipt.ReviewerID, DecisionReason: receipt.DecisionReason})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(receipt, want) {
		return fmt.Errorf("%w: reviewed relation receipt is not canonical", ErrInvalid)
	}
	return nil
}

// ValidateReviewedReplay applies the unchanged versioned pure review contract.
// The result alone does not prove database admission or authorize canonical writes.
func ValidateReviewedReplay(cut AdmittedCut, report CandidateReport,
	expected ReviewEvidenceSnapshot, stored ReviewedReceipt, requested ReviewedInput,
) error {
	if err := ValidateReviewedReceipt(cut, report, expected, stored); err != nil {
		return err
	}
	want, err := NewReviewedReceipt(cut, report, expected, requested)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(stored, want) {
		return ErrReplayConflict
	}
	return nil
}

// NormalizeReviewMapping retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func NormalizeReviewMapping(candidate Candidate, input ReviewMapping) (ReviewMapping, error) {
	if len(input.Witnesses) == 0 {
		return ReviewMapping{}, ErrWitnessRequired
	}
	if len(input.Witnesses) > MaxWitnesses {
		return ReviewMapping{}, ErrInvalid
	}
	for _, witness := range input.Witnesses {
		if len(witness.EndpointNodeIDs) != 2 {
			return ReviewMapping{}, ErrInvalid
		}
	}
	var mapping ReviewMapping
	var err error
	if mapping.ProposalSentence, err = RequiredText("proposal_sentence", input.ProposalSentence, MaxProposalBytes); err != nil {
		return ReviewMapping{}, err
	}
	if mapping.Witnesses, err = normalizeCanonicalImplementsWitnesses(input.Witnesses, candidate.Specification.NodeID, candidate.Implementation.NodeID); err != nil {
		return ReviewMapping{}, err
	}
	if mapping.Coverage, err = RequiredText("coverage", input.Coverage, MaxCoverageBytes); err != nil {
		return ReviewMapping{}, err
	}
	if input.Limitations == nil {
		return ReviewMapping{}, ErrInvalid
	}
	if mapping.Limitations, err = normalizeCanonicalImplementsLimitations(input.Limitations); err != nil {
		return ReviewMapping{}, err
	}
	return mapping, nil
}

// ValidateReviewEvidenceBounds retains a pure helper for historical Lab-test compatibility.
// It is not a substitute for the complete runtime admission validator.
func ValidateReviewEvidenceBounds(input ReviewEvidenceInput) error {
	basis := input.ReviewSnapshot.ReviewPackage.ProposalBasis
	if basis.ManualReviewProfile != nil {
		if err := evidenceingestion.ValidateManualReviewProfileBinding(*basis.ManualReviewProfile); err != nil {
			return fmt.Errorf("%w: invalid manual review profile: %v", ErrInvalid, err)
		}
	}
	manifest := input.ReviewSnapshot.ProposalManifest
	receipt := input.ReviewSnapshot.SubmissionReceipt
	review := input.ReviewSnapshot.ReviewPackage
	subject := input.ReviewSnapshot.ExactReviewSubject
	if len(input.RawText) == 0 || len(input.RawText) > MaxReviewSourceBytes ||
		len(input.RenderedText) == 0 || len(input.RenderedText) > MaxReviewSourceBytes ||
		!utf8.ValidString(input.RawText) || !utf8.ValidString(input.RenderedText) ||
		strings.ContainsRune(input.RawText, '\x00') || strings.ContainsRune(input.RenderedText, '\x00') ||
		len(basis.SourceRefs) == 0 || len(basis.SourceRefs) > MaxReviewSourceRefs ||
		len(manifest.Entries) == 0 || len(manifest.Entries) > MaxReviewManifestEntries ||
		manifest.ProposalCount != len(manifest.Entries) || input.ReviewSnapshot.SubmissionReceipt.ProposalCount != len(manifest.Entries) ||
		len(basis.StatementText) > MaxExcerptBytes || len(basis.SourceLimitations) > MaxLimitations ||
		len(input.ReviewSnapshot.ReviewPackage.Limitations) > MaxLimitations {
		return fmt.Errorf("%w: original specification basis exceeds its bounds", ErrInvalid)
	}
	// The controller basis is still bounded before core builders allocate or
	// hash it. Empty optional metadata is allowed here; existing validators own
	// its meaning, required fields and exact identity rules.
	metadata := make([]string, 0, 60+len(basis.SourceLimitations)+len(review.Limitations)+5*len(manifest.Entries)+9*len(basis.SourceRefs))
	metadata = append(metadata, input.AdmittedCutID, input.SpecificationNodeID,
		receipt.ContractVersion, receipt.ID, receipt.RequestID, receipt.ProducerSessionRef, receipt.SourceSnapshotID, receipt.ExtractionViewID,
		receipt.ExtractorDefinitionID, receipt.ExtractionRunID, receipt.ExtractionAttemptID, receipt.ExtractorOutputHash, receipt.ProposalBatchID, receipt.ProposalManifestID,
		manifest.ContractVersion, manifest.ID, manifest.ProposalBatchID, manifest.ExtractionAttemptID, manifest.ExtractorOutputHash, manifest.ProposalSetHash,
		review.ContractVersion, review.ID, review.ReviewTemplateID, review.SubmissionReceiptID, review.ProposedEffect, review.Coverage,
		basis.ContractVersion, basis.ID, basis.SubmissionReceiptID, basis.ProposalManifestID, basis.ProposalOccurrenceID, basis.ProposalLocalID,
		basis.ProposalFingerprint, basis.ProposalFingerprintVersion, basis.ProposalKind, basis.SourceSnapshotID, basis.ExtractionViewID,
		basis.SourceSystem, basis.SourceID, basis.SourceVersion, basis.RawContentHash, basis.RendererName, basis.RendererVersion,
		basis.RenderedContentHash, basis.OriginMetadataHash, basis.SourceTitle, basis.SourceLocation, basis.SourceCoverage,
		basis.ExtractorDefinitionID, basis.ExtractionRunID, basis.ExtractionAttemptID, basis.ExtractorName, basis.ExtractorVersion, basis.ExtractorConfigHash, basis.SourceBindingKind,
		subject.SubmissionReceiptID, subject.ProposalManifestID, subject.ProposalOccurrenceID, subject.ProposalBasisID, subject.ReviewPackageID)
	metadata = append(metadata, basis.SourceLimitations...)
	metadata = append(metadata, review.Limitations...)
	for _, entry := range manifest.Entries {
		metadata = append(metadata, entry.ProposalOccurrenceID, entry.ProposalLocalID, entry.ProposalFingerprint, entry.ProposalFingerprintVersion, entry.ProposalKind)
	}
	for _, ref := range basis.SourceRefs {
		if len(ref.QuotedText) > MaxExcerptBytes || len(ref.SpanID) > MaxNodeIDBytes {
			return ErrInvalid
		}
		metadata = append(metadata, ref.ExtractionViewID, ref.SpanID, ref.QuotedTextHash, ref.TargetKind, ref.RepositorySnapshotID,
			ref.FileSnapshotID, ref.RepoID, ref.CommitSHA, ref.Path)
	}
	for _, value := range metadata {
		if len(value) > MaxLocationBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: invalid or oversized original source metadata", ErrInvalid)
		}
	}
	return nil
}
