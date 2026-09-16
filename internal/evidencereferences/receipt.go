package evidencereferences

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

// ErrReplayConflict means retained authority or the reviewed subject differs.
var ErrReplayConflict = errors.New("references reviewed authority conflicts")

// AdmissionInput asserts explicit approval; callers must already be authorized.
// Reviewer identity is audit metadata here, not human authentication.
type AdmissionInput struct {
	RequestID          string        `json:"request_id"`
	Review             ReviewRequest `json:"review"`
	ExpectedSubject    ReviewSubject `json:"expected_subject"`
	Decision           string        `json:"decision"`
	ReviewerID         string        `json:"reviewer_id"`
	DecisionReason     string        `json:"decision_reason"`
	ProducerSessionRef string        `json:"producer_session_ref,omitempty"`
}

// AdmissionResult describes a future native writer's response. The pure
// constructors in this package do not persist an edge or admission authority.
type AdmissionResult struct {
	RequestID string                                       `json:"request_id"`
	Receipt   evidenceingestion.CanonicalReferencesReceipt `json:"receipt"`
	Edge      evidenceingestion.CanonicalGraphEdge         `json:"edge"`
	Replayed  bool                                         `json:"replayed"`
}

func validateAdmission(in AdmissionInput) error {
	if in.Decision != "approved" {
		return fmt.Errorf("%w: explicit approved decision required", ErrUnresolved)
	}
	for _, f := range []struct {
		name, value string
		max         int
		optional    bool
	}{
		{"request_id", in.RequestID, 200, false}, {"reviewer_id", in.ReviewerID, 200, false},
		{"decision_reason", in.DecisionReason, 2000, false}, {"producer_session_ref", in.ProducerSessionRef, 2000, true},
		{"review_subject_id", in.ExpectedSubject.ReviewSubjectID, 200, false}, {"cut_id", in.ExpectedSubject.CutID, 200, false},
	} {
		if len(f.value) > f.max || !utf8.ValidString(f.value) || strings.ContainsRune(f.value, 0) || strings.TrimSpace(f.value) != f.value || (!f.optional && f.value == "") {
			return fmt.Errorf("%w: %s violates normalized text bound", ErrUnresolved, f.name)
		}
	}
	data, err := json.Marshal(in.Review)
	if err != nil {
		return fmt.Errorf("encoding references request: %w", err)
	}
	if len(data) > 16384 {
		return fmt.Errorf("%w: review request exceeds byte bound", ErrUnresolved)
	}
	return nil
}

func admissionRecord(in AdmissionInput, review Review, origin string) (evidenceingestion.CanonicalReferencesAdmissionAuthority, evidenceingestion.CanonicalGraphEdge, evidenceingestion.CanonicalReferencesReceipt, error) {
	// Retain the native caller's approval/subject checks in this isolated pure
	// constructor too. A valid envelope still does not authenticate a reviewer.
	if err := validateAdmission(in); err != nil {
		return evidenceingestion.CanonicalReferencesAdmissionAuthority{}, evidenceingestion.CanonicalGraphEdge{}, evidenceingestion.CanonicalReferencesReceipt{}, err
	}
	if in.ExpectedSubject != review.Subject || in.Review != review.Request {
		return evidenceingestion.CanonicalReferencesAdmissionAuthority{}, evidenceingestion.CanonicalGraphEdge{}, evidenceingestion.CanonicalReferencesReceipt{}, ErrReplayConflict
	}
	request, err := json.Marshal(in.Review)
	if err != nil {
		return evidenceingestion.CanonicalReferencesAdmissionAuthority{}, evidenceingestion.CanonicalGraphEdge{}, evidenceingestion.CanonicalReferencesReceipt{}, fmt.Errorf("encoding references request: %w", err)
	}
	a := evidenceingestion.CanonicalReferencesAdmissionAuthority{RequestID: in.RequestID, ContractVersion: evidenceingestion.CanonicalReferencesAdmissionV1,
		CanonicalEdgeID: "canon-edge:" + evidenceingestion.ReferencesFramedHash("canonical-references-edge/v1", in.Review.FromNodeID, in.Review.ToNodeID),
		FromNodeID:      in.Review.FromNodeID, ToNodeID: in.Review.ToNodeID, OriginProposalOccurrenceID: origin,
		ReviewerID: in.ReviewerID, DecisionReason: in.DecisionReason, ProducerSessionRef: in.ProducerSessionRef,
		ReviewSubjectID: review.Subject.ReviewSubjectID, CutID: review.Subject.CutID,
		RequestPayloadUTF8: string(request), RequestPayloadHash: evidenceingestion.ReferencesPayloadHash(string(request)),
		ReviewPayloadUTF8: review.Display.PayloadUTF8, ReviewPayloadHash: evidenceingestion.ReferencesPayloadHash(review.Display.PayloadUTF8)}
	receipt := evidenceingestion.ReferencesReceiptForAuthority(a)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return a, evidenceingestion.CanonicalGraphEdge{}, receipt, fmt.Errorf("encoding references receipt: %w", err)
	}
	a.ReceiptID = receipt.ID
	a.ReceiptPayloadUTF8 = string(receiptJSON)
	a.ReceiptPayloadHash = evidenceingestion.ReferencesPayloadHash(a.ReceiptPayloadUTF8)
	edge := evidenceingestion.CanonicalGraphEdge{ID: a.CanonicalEdgeID, From: a.FromNodeID, To: a.ToNodeID,
		Relation: evidencegraph.CanonicalReferences, OriginProposalOccurrenceID: origin,
		Provenance: evidencegraph.ProvenanceRecord{ID: "provenance:" + a.CanonicalEdgeID,
			OriginRefs: []string{a.FromNodeID, a.ToNodeID, origin}, OriginGroupID: a.CanonicalEdgeID,
			Producer: "ahe-wrap", Method: "reviewed_references", MethodVersion: "v1", TraceRef: a.RequestID, ReviewRef: a.ReceiptID}}
	return a, edge, receipt, evidenceingestion.ValidateCanonicalReferencesReadAuthority(edge, a)
}
