package evidencereferences

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

// EndpointCard shares the exact retained wire schema with ingestion.
type EndpointCard = evidenceingestion.CanonicalReferencesEndpointCard

// ReviewBody shares the exact retained wire schema with ingestion.
type ReviewBody = evidenceingestion.CanonicalReferencesReviewBody

// ReviewSubject binds the exact display bytes and observed target cut.
type ReviewSubject struct {
	ReviewSubjectID string `json:"review_subject_id"`
	CutID           string `json:"cut_id"`
}

// ReviewDisplay contains the bytes a cooperating agent must present for review.
type ReviewDisplay struct {
	MediaType   string `json:"media_type"`
	PayloadUTF8 string `json:"payload_utf8"`
}

// Review contains a canonical request, exact subject and complete display.
type Review struct {
	Request ReviewRequest `json:"review"`
	Subject ReviewSubject `json:"subject"`
	Display ReviewDisplay `json:"display"`
}

func buildReview(req ReviewRequest, basis evidenceingestion.ReferencesNativeBasis) (Review, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return Review{}, fmt.Errorf("encoding references request: %w", err)
	}
	if len(encoded) > 16384 {
		return Review{}, fmt.Errorf("%w: request exceeds byte bound", ErrUnresolved)
	}
	resolution, err := Resolve(req, basis)
	if err != nil {
		return Review{}, err
	}
	body := ReviewBody{ContractVersion: "canonical-references-review/v1", Request: req, Resolution: resolution,
		From: endpointCard(basis.From), To: endpointCard(basis.To), Relation: "references", Effect: "direct_structural_navigation_only",
		Limitations: evidenceingestion.CanonicalReferencesLimitationsV1()}
	payload, err := json.Marshal(body)
	if err != nil {
		return Review{}, fmt.Errorf("encoding references display: %w", err)
	}
	if len(payload) > 524288 {
		return Review{}, fmt.Errorf("%w: review exceeds byte bound", ErrUnresolved)
	}
	display := ReviewDisplay{MediaType: evidenceingestion.ReferencesReviewMediaTypeV1, PayloadUTF8: string(payload)}
	return Review{Request: req, Subject: ReviewSubject{
		ReviewSubjectID: "references-review:v1:" + evidenceingestion.ReferencesPayloadHash(display.MediaType+"\n"+display.PayloadUTF8),
		CutID:           targetCutID(resolution.TargetCut)}, Display: display}, nil
}

func endpointCard(endpoint evidenceingestion.ReferencesNativeEndpoint) EndpointCard {
	b := endpoint.ReviewSnapshot.ReviewPackage.ProposalBasis
	return EndpointCard{NodeID: endpoint.Node.CanonicalID, OriginProposalOccurrenceID: endpoint.Node.OriginProposalOccurrenceID,
		Claim: b.StatementText, SourceSnapshotID: b.SourceSnapshotID, ExtractionViewID: b.ExtractionViewID,
		SourceSystem: b.SourceSystem, SourceID: b.SourceID, SourceVersion: b.SourceVersion, RawContentHash: b.RawContentHash,
		RenderedContentHash: b.RenderedContentHash, Title: b.SourceTitle, Location: b.SourceLocation, Coverage: b.SourceCoverage,
		Limitations: append([]string{}, b.SourceLimitations...), SourceRefs: append([]evidenceingestion.ResolvedSourceRef{}, b.SourceRefs...)}
}

func targetCutID(c TargetCut) string {
	fields := make([]string, 0, 8+len(c.CandidateIDs))
	fields = append(fields, c.ContractVersion, c.SourceSnapshotID, c.ExtractionViewID, c.SpanID, strconv.Itoa(c.StartByte), strconv.Itoa(c.EndByte), c.SpanHash)
	fields = append(fields, c.CandidateIDs...)
	return "references-cut:v1:sha256:" + evidenceingestion.ReferencesFramedHash(fields...)
}
