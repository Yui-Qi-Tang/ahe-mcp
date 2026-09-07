package evidenceingestion

import (
	"context"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// SourceClaimReviewDisplayArtifactV1 identifies the exact bytes emitted for review display.
	SourceClaimReviewDisplayArtifactV1 = "review-package-display/v1"
	// SourceClaimReviewDisplayMediaTypeV1 identifies canonical ReviewPackage JSON.
	SourceClaimReviewDisplayMediaTypeV1 = "application/vnd.ahe.review-package.v1+json"
	// SourceClaimReviewDisplayMaxBytesV1 bounds one exact display payload.
	SourceClaimReviewDisplayMaxBytesV1 = 128 << 10
)

// SourceClaimReviewDisplayArtifact contains the exact deterministic bytes AHE
// emits for a cooperating agent to display. It does not prove those bytes were
// rendered or seen.
type SourceClaimReviewDisplayArtifact struct {
	ContractVersion string `json:"contract_version"`
	ID              string `json:"review_display_artifact_id"`
	ReviewPackageID string `json:"review_package_id"`
	MediaType       string `json:"media_type"`
	PayloadUTF8     string `json:"payload_utf8"`
}

// ExactDisplayedReviewSubject binds the review subject to the exact bytes AHE
// emitted for display.
type ExactDisplayedReviewSubject struct {
	ReviewSubject           ExactReviewSubject `json:"review_subject"`
	ReviewDisplayArtifactID string             `json:"review_display_artifact_id"`
}

// LoadSourceClaimReviewDisplayArtifact rebuilds one exact display directly
// from PostgreSQL authority. The returned bytes grant no admission authority.
func LoadSourceClaimReviewDisplayArtifact(
	ctx context.Context,
	pool *pgxpool.Pool,
	extractionAttemptID string,
	proposalOccurrenceID string,
) (SourceClaimReviewDisplayArtifact, ExactDisplayedReviewSubject, error) {
	snapshot, err := LoadReviewableSourceClaimReviewSnapshot(ctx, pool, extractionAttemptID, proposalOccurrenceID)
	if err != nil {
		return SourceClaimReviewDisplayArtifact{}, ExactDisplayedReviewSubject{}, err
	}
	return BuildSourceClaimReviewDisplayArtifact(snapshot)
}

// BuildSourceClaimReviewDisplayArtifact emits canonical ReviewPackage JSON and
// binds it to the complete exact review subject.
func BuildSourceClaimReviewDisplayArtifact(snapshot ReviewableSourceClaimReviewSnapshot) (SourceClaimReviewDisplayArtifact, ExactDisplayedReviewSubject, error) {
	if err := validateReviewableSourceClaimReviewSnapshot(snapshot); err != nil {
		return SourceClaimReviewDisplayArtifact{}, ExactDisplayedReviewSubject{}, err
	}
	payload, err := deterministicJSON(snapshot.ReviewPackage)
	if err != nil {
		return SourceClaimReviewDisplayArtifact{}, ExactDisplayedReviewSubject{}, err
	}
	if len(payload) > SourceClaimReviewDisplayMaxBytesV1 {
		return SourceClaimReviewDisplayArtifact{}, ExactDisplayedReviewSubject{}, newDomainError(
			ErrorInvalidInput,
			"source-claim review display payload exceeds %d UTF-8 bytes",
			SourceClaimReviewDisplayMaxBytesV1,
		)
	}
	artifact := SourceClaimReviewDisplayArtifact{
		ContractVersion: SourceClaimReviewDisplayArtifactV1,
		ReviewPackageID: snapshot.ReviewPackage.ID,
		MediaType:       SourceClaimReviewDisplayMediaTypeV1,
		PayloadUTF8:     string(payload),
	}
	artifact.ID, err = sourceClaimReviewDisplayArtifactID(artifact)
	if err != nil {
		return SourceClaimReviewDisplayArtifact{}, ExactDisplayedReviewSubject{}, err
	}
	subject := ExactDisplayedReviewSubject{
		ReviewSubject:           snapshot.ExactReviewSubject,
		ReviewDisplayArtifactID: artifact.ID,
	}
	return artifact, subject, nil
}

// ValidateExactSourceClaimReviewDisplay rebuilds the display artifact from the
// supplied validated snapshot and rejects any byte or subject drift.
func ValidateExactSourceClaimReviewDisplay(snapshot ReviewableSourceClaimReviewSnapshot, artifact SourceClaimReviewDisplayArtifact, subject ExactDisplayedReviewSubject) error {
	expectedArtifact, expectedSubject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(artifact, expectedArtifact) {
		return newDomainError(ErrorReviewContractConflict, "review display artifact differs from canonical review package bytes")
	}
	if !reflect.DeepEqual(subject, expectedSubject) {
		return newDomainError(ErrorReviewContractConflict, "displayed review subject does not bind the canonical display artifact")
	}
	return nil
}

func sourceClaimReviewDisplayArtifactID(artifact SourceClaimReviewDisplayArtifact) (string, error) {
	commitment := artifact.MediaType + "\n" + artifact.PayloadUTF8
	return "review-display:v1:sha256:" + hashHex([]byte(commitment)), nil
}

func validateReviewableSourceClaimReviewSnapshot(snapshot ReviewableSourceClaimReviewSnapshot) error {
	if err := validateReceiptManifestPair(snapshot.SubmissionReceipt, snapshot.ProposalManifest); err != nil {
		return err
	}
	reviewPackage := snapshot.ReviewPackage
	basis := reviewPackage.ProposalBasis
	if reviewPackage.ContractVersion != ReviewableIngestionContractV1 ||
		reviewPackage.ReviewTemplateID != SourceClaimReviewTemplateV1 ||
		reviewPackage.ProposedEffect != SourceClaimProposedEffectV1 ||
		basis.ContractVersion != ReviewableIngestionContractV1 {
		return newDomainError(ErrorReviewContractConflict, "review snapshot uses an unsupported review package contract")
	}
	if basis.SourceRefs == nil || len(basis.SourceRefs) == 0 || basis.SourceLimitations == nil || reviewPackage.Limitations == nil {
		return newDomainError(ErrorReviewContractConflict, "review snapshot omits explicit review package material")
	}
	if reviewPackage.Coverage != sourceClaimReviewCoverageV1 ||
		!slices.Equal(reviewPackage.Limitations, sourceClaimReviewLimitationsV1()) {
		return newDomainError(ErrorReviewContractConflict, "review snapshot does not use the canonical review envelope")
	}
	if err := validateReviewSourceBasisMetadata(basis); err != nil {
		return err
	}
	if reviewPackage.SubmissionReceiptID != snapshot.SubmissionReceipt.ID ||
		basis.SubmissionReceiptID != snapshot.SubmissionReceipt.ID ||
		basis.ProposalManifestID != snapshot.ProposalManifest.ID ||
		basis.SourceSnapshotID != snapshot.SubmissionReceipt.SourceSnapshotID ||
		basis.ExtractionViewID != snapshot.SubmissionReceipt.ExtractionViewID ||
		basis.ExtractorDefinitionID != snapshot.SubmissionReceipt.ExtractorDefinitionID ||
		basis.ExtractionRunID != snapshot.SubmissionReceipt.ExtractionRunID ||
		basis.ExtractionAttemptID != snapshot.SubmissionReceipt.ExtractionAttemptID {
		return newDomainError(ErrorReviewContractConflict, "review package provenance differs from its receipt or manifest")
	}
	entry, err := manifestEntryForProposal(snapshot.ProposalManifest, basis.ProposalOccurrenceID)
	if err != nil {
		return err
	}
	if basis.ProposalManifestOrdinal != entry.Ordinal ||
		basis.ProposalLocalID != entry.ProposalLocalID ||
		basis.ProposalFingerprint != entry.ProposalFingerprint ||
		basis.ProposalFingerprintVersion != entry.ProposalFingerprintVersion ||
		basis.ProposalKind != entry.ProposalKind ||
		basis.SourceBindingKind != ProposalSourceBindingSourceSnapshot {
		return newDomainError(ErrorReviewContractConflict, "review package proposal basis differs from its complete manifest entry")
	}
	refs := append([]ResolvedSourceRef(nil), basis.SourceRefs...)
	seenRefs := make(map[reviewSourceSpanIdentity]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateReviewableQuerySourceRef(basis.ExtractionViewID, ref); err != nil {
			return err
		}
		identity := reviewSourceSpanIdentity{ExtractionViewID: ref.ExtractionViewID, SpanID: ref.SpanID}
		if _, duplicate := seenRefs[identity]; duplicate {
			return newDomainError(ErrorReviewContractConflict, "review package repeats source reference %q", ref.SpanID)
		}
		seenRefs[identity] = struct{}{}
	}
	sortReviewSourceRefs(refs)
	if !slices.Equal(refs, basis.SourceRefs) {
		return newDomainError(ErrorReviewContractConflict, "review package source references are not in canonical order")
	}
	expectedFingerprint, err := proposalFingerprint(basis.StatementText, refs)
	if err != nil {
		return err
	}
	if expectedFingerprint != basis.ProposalFingerprint {
		return newDomainError(ErrorReviewContractConflict, "review package statement and source references differ from its proposal fingerprint")
	}
	expectedBasisID, err := proposalBasisID(basis)
	if err != nil {
		return err
	}
	if expectedBasisID != basis.ID {
		return newDomainError(ErrorReviewContractConflict, "review snapshot proposal basis identity drifted")
	}
	expectedPackageID, err := reviewPackageID(reviewPackage)
	if err != nil {
		return err
	}
	if expectedPackageID != reviewPackage.ID {
		return newDomainError(ErrorReviewContractConflict, "review snapshot review package identity drifted")
	}
	expectedSubject := ExactReviewSubject{
		SubmissionReceiptID:  snapshot.SubmissionReceipt.ID,
		ProposalManifestID:   snapshot.ProposalManifest.ID,
		ProposalOccurrenceID: basis.ProposalOccurrenceID,
		ProposalBasisID:      basis.ID,
		ReviewPackageID:      reviewPackage.ID,
	}
	if !reflect.DeepEqual(snapshot.ExactReviewSubject, expectedSubject) {
		return newDomainError(ErrorReviewContractConflict, "review subject does not bind the supplied review snapshot")
	}
	return nil
}
