package evidenceingestion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

const (
	// CanonicalReferencesAdmissionV1 identifies independent relation authority.
	CanonicalReferencesAdmissionV1 = "canonical-references-admission/v1"
	// ReferencesReviewMediaTypeV1 identifies exact emitted review bytes.
	ReferencesReviewMediaTypeV1 = "application/vnd.ahe.references-review.v1+json"
)

// CanonicalReferencesReceipt binds an asserted review, not authenticated viewing.
// Session remains audit metadata outside this content identity.
type CanonicalReferencesReceipt struct {
	ContractVersion            string `json:"contract_version"`
	ID                         string `json:"receipt_id"`
	RequestID                  string `json:"request_id"`
	ReviewSubjectID            string `json:"review_subject_id"`
	CutID                      string `json:"cut_id"`
	CanonicalEdgeID            string `json:"canonical_edge_id"`
	FromNodeID                 string `json:"from_node_id"`
	ToNodeID                   string `json:"to_node_id"`
	OriginProposalOccurrenceID string `json:"origin_proposal_occurrence_id"`
	ReviewerID                 string `json:"reviewer_id"`
	DecisionReason             string `json:"decision_reason"`
	RequestPayloadHash         string `json:"request_payload_hash"`
	ReviewPayloadHash          string `json:"review_payload_hash"`
}

// CanonicalReferencesAdmissionAuthority retains exact bounded UTF-8 payloads.
type CanonicalReferencesAdmissionAuthority struct {
	RequestID                  string `json:"request_id"`
	ContractVersion            string `json:"contract_version"`
	ReceiptID                  string `json:"receipt_id"`
	CanonicalEdgeID            string `json:"canonical_edge_id"`
	FromNodeID                 string `json:"from_node_id"`
	ToNodeID                   string `json:"to_node_id"`
	OriginProposalOccurrenceID string `json:"origin_proposal_occurrence_id"`
	ReviewerID                 string `json:"reviewer_id"`
	DecisionReason             string `json:"decision_reason"`
	ProducerSessionRef         string `json:"producer_session_ref"`
	ReviewSubjectID            string `json:"review_subject_id"`
	CutID                      string `json:"cut_id"`
	RequestPayloadUTF8         string `json:"request_payload_utf8"`
	RequestPayloadHash         string `json:"request_payload_hash"`
	ReviewPayloadUTF8          string `json:"review_payload_utf8"`
	ReviewPayloadHash          string `json:"review_payload_hash"`
	ReceiptPayloadUTF8         string `json:"receipt_payload_utf8"`
	ReceiptPayloadHash         string `json:"receipt_payload_hash"`
}

// ReferencesFramedHash hashes length-prefixed UTF-8 fields, including the scope.
// This framing is shared with PostgreSQL; it does not rely on JSON re-encoding.
func ReferencesFramedHash(fields ...string) string {
	h := sha256.New()
	for _, field := range fields {
		_, _ = h.Write([]byte(strconv.Itoa(len(field)) + ":" + field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ReferencesPayloadHash binds exact bytes rather than a JSONB serialization.
func ReferencesPayloadHash(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReferencesReceiptForAuthority reconstructs the exact immutable receipt.
func ReferencesReceiptForAuthority(a CanonicalReferencesAdmissionAuthority) CanonicalReferencesReceipt {
	r := CanonicalReferencesReceipt{ContractVersion: CanonicalReferencesAdmissionV1,
		RequestID: a.RequestID, ReviewSubjectID: a.ReviewSubjectID, CutID: a.CutID,
		CanonicalEdgeID: a.CanonicalEdgeID, FromNodeID: a.FromNodeID, ToNodeID: a.ToNodeID,
		OriginProposalOccurrenceID: a.OriginProposalOccurrenceID, ReviewerID: a.ReviewerID,
		DecisionReason: a.DecisionReason, RequestPayloadHash: a.RequestPayloadHash, ReviewPayloadHash: a.ReviewPayloadHash}
	r.ID = "references-receipt:v1:sha256:" + ReferencesFramedHash(CanonicalReferencesAdmissionV1,
		r.RequestID, r.ReviewSubjectID, r.CutID, r.CanonicalEdgeID, r.FromNodeID, r.ToNodeID,
		r.OriginProposalOccurrenceID, r.ReviewerID, r.DecisionReason, r.RequestPayloadHash, r.ReviewPayloadHash)
	return r
}

// ValidateCanonicalReferencesReadAuthority validates a retained observation.
// It never scans today's candidates or recursively validates endpoint relations.
func ValidateCanonicalReferencesReadAuthority(edge CanonicalGraphEdge, a CanonicalReferencesAdmissionAuthority) error {
	fail := func() error {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical references retained authority differs")
	}
	if a.ContractVersion != CanonicalReferencesAdmissionV1 || a.FromNodeID == a.ToNodeID ||
		a.CanonicalEdgeID != edge.ID || a.FromNodeID != edge.From || a.ToNodeID != edge.To ||
		a.OriginProposalOccurrenceID != edge.OriginProposalOccurrenceID || edge.Relation != evidencegraph.CanonicalReferences ||
		edge.ID != "canon-edge:"+ReferencesFramedHash("canonical-references-edge/v1", edge.From, edge.To) {
		return fail()
	}
	for _, field := range []struct {
		value    string
		max      int
		optional bool
	}{
		{a.RequestID, 200, false}, {a.ReviewerID, 200, false}, {a.DecisionReason, 2000, false}, {a.ProducerSessionRef, 2000, true},
	} {
		if len(field.value) > field.max || !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) ||
			strings.TrimSpace(field.value) != field.value || (!field.optional && field.value == "") {
			return fail()
		}
	}
	for _, p := range []struct {
		text, hash string
		max        int
	}{
		{a.RequestPayloadUTF8, a.RequestPayloadHash, 16384}, {a.ReviewPayloadUTF8, a.ReviewPayloadHash, 524288},
		{a.ReceiptPayloadUTF8, a.ReceiptPayloadHash, 524288},
	} {
		if len(p.text) == 0 || len(p.text) > p.max || !utf8.ValidString(p.text) || !json.Valid([]byte(p.text)) || ReferencesPayloadHash(p.text) != p.hash {
			return fail()
		}
	}
	if a.ReviewSubjectID != "references-review:v1:"+ReferencesPayloadHash(ReferencesReviewMediaTypeV1+"\n"+a.ReviewPayloadUTF8) {
		return fail()
	}
	receipt := ReferencesReceiptForAuthority(a)
	want, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encoding references receipt: %w", err)
	}
	if receipt.ID != a.ReceiptID || string(want) != a.ReceiptPayloadUTF8 {
		return fail()
	}
	var request CanonicalReferencesReviewRequest
	var review CanonicalReferencesReviewBody
	if json.Unmarshal([]byte(a.RequestPayloadUTF8), &request) != nil ||
		json.Unmarshal([]byte(a.ReviewPayloadUTF8), &review) != nil {
		return fail()
	}
	// Exact typed re-encoding rejects unknown/duplicate fields, null arrays and
	// alternate JSON byte spellings without depending on PostgreSQL key order.
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encoding retained references request: %w", err)
	}
	reviewJSON, err := json.Marshal(review)
	if err != nil {
		return fmt.Errorf("encoding retained references review: %w", err)
	}
	if string(requestJSON) != a.RequestPayloadUTF8 || string(reviewJSON) != a.ReviewPayloadUTF8 ||
		request.FromNodeID != edge.From || request.ToNodeID != edge.To || review.Request != request ||
		review.ContractVersion != "canonical-references-review/v1" || review.Relation != "references" ||
		review.Effect != "direct_structural_navigation_only" ||
		!reflect.DeepEqual(review.Limitations, CanonicalReferencesLimitationsV1()) ||
		review.From.NodeID != edge.From || review.To.NodeID != edge.To ||
		review.From.OriginProposalOccurrenceID != a.OriginProposalOccurrenceID ||
		review.From.SourceSnapshotID != request.Reference.SourceSnapshotID ||
		review.From.ExtractionViewID != request.Reference.ExtractionViewID ||
		review.To.SourceSnapshotID != request.Target.SourceSnapshotID ||
		review.To.ExtractionViewID != request.Target.ExtractionViewID ||
		review.From.Limitations == nil || review.To.Limitations == nil ||
		len(review.From.Limitations) > 64 || len(review.To.Limitations) > 64 ||
		len(review.From.SourceRefs) < 1 || len(review.From.SourceRefs) > 64 || len(review.To.SourceRefs) != 1 {
		return fail()
	}
	c := review.Resolution.TargetCut
	resolution := CanonicalReferencesResolution{ContractVersion: "canonical-references-resolution/v1",
		ResolverProfile: request.ResolverProfile, FromNodeID: request.FromNodeID, ToNodeID: request.ToNodeID,
		Reference: request.Reference, Target: request.Target, TargetCut: c}
	targetRef := review.To.SourceRefs[0]
	if !reflect.DeepEqual(review.Resolution, resolution) ||
		c.ContractVersion != "canonical-references-target-cut/v1" || len(c.CandidateIDs) != 1 ||
		c.CandidateIDs[0] != edge.To || c.SourceSnapshotID != request.Target.SourceSnapshotID ||
		c.ExtractionViewID != request.Target.ExtractionViewID || c.SpanID != request.Target.SpanID ||
		c.SpanHash != request.Target.SpanHash || c.StartByte < 0 || c.EndByte <= c.StartByte ||
		targetRef.ExtractionViewID != c.ExtractionViewID || targetRef.SpanID != c.SpanID ||
		targetRef.QuotedTextHash != c.SpanHash || targetRef.StartByte != c.StartByte || targetRef.EndByte != c.EndByte ||
		a.CutID != "references-cut:v1:sha256:"+ReferencesFramedHash(c.ContractVersion, c.SourceSnapshotID,
			c.ExtractionViewID, c.SpanID, strconv.Itoa(c.StartByte), strconv.Itoa(c.EndByte), c.SpanHash, c.CandidateIDs[0]) {
		return fail()
	}
	wantProvenance := evidencegraph.ProvenanceRecord{ID: "provenance:" + edge.ID,
		OriginRefs: []string{edge.From, edge.To, a.OriginProposalOccurrenceID}, OriginGroupID: edge.ID,
		Producer: "ahe-wrap", Method: "reviewed_references", MethodVersion: "v1", TraceRef: a.RequestID, ReviewRef: a.ReceiptID}
	if !reflect.DeepEqual(edge.Provenance, wantProvenance) {
		return fail()
	}
	return nil
}
