package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

// CanonicalImplementsAdmissionAuthority is the native persistence authority for
// one reviewed implements edge. OriginProposalOccurrenceID is a source carrier;
// that proposal's earlier node admission did not authorize this relation. The
// retained Lab receipt stays labelled as Lab material inside this new envelope.
type CanonicalImplementsAdmissionAuthority struct {
	RequestID                  string `json:"request_id"`
	ContractVersion            string `json:"contract_version"`
	ReceiptID                  string `json:"receipt_id"`
	CanonicalEdgeID            string `json:"canonical_edge_id"`
	SpecificationNodeID        string `json:"specification_node_id"`
	ImplementationNodeID       string `json:"implementation_node_id"`
	OriginProposalOccurrenceID string `json:"origin_proposal_occurrence_id"`
	ReviewerID                 string `json:"reviewer_id"`
	DecisionReason             string `json:"decision_reason"`
	ProducerSessionRef         string `json:"producer_session_ref"`
	BasisID                    string `json:"basis_id"`
	ReportID                   string `json:"report_id"`
	CandidateID                string `json:"candidate_id"`
	DisplayID                  string `json:"display_id"`
	BasisPayloadUTF8           string `json:"basis_payload_utf8"`
	ReceiptPayloadUTF8         string `json:"receipt_payload_utf8"`
	DisplayMediaType           string `json:"display_media_type"`
	DisplayPayloadUTF8         string `json:"display_payload_utf8"`
	BasisPayloadHash           string `json:"basis_payload_hash"`
	ReceiptPayloadHash         string `json:"receipt_payload_hash"`
	DisplayPayloadHash         string `json:"display_payload_hash"`
}

func loadCanonicalImplementsAdmission(ctx context.Context, db sqlQueryer, edge CanonicalGraphEdge) (*CanonicalImplementsAdmissionAuthority, error) {
	var result CanonicalImplementsAdmissionAuthority
	err := db.queryRow(ctx, `
		SELECT request_id, contract_version, receipt_id, canonical_edge_id,
			specification_node_id, implementation_node_id, origin_proposal_occurrence_id,
			reviewer_id, decision_reason, producer_session_ref, basis_id, report_id,
			candidate_id, display_id, basis_payload_utf8, receipt_payload_utf8,
			display_media_type, display_payload_utf8, basis_payload_hash,
			receipt_payload_hash, display_payload_hash
		FROM canonical_implements_admissions WHERE canonical_edge_id = $1
			AND octet_length(basis_payload_utf8) BETWEEN 1 AND 8388608
			AND octet_length(receipt_payload_utf8) BETWEEN 1 AND 262144
			AND octet_length(display_payload_utf8) BETWEEN 1 AND 131072
	`, edge.ID).Scan(
		&result.RequestID, &result.ContractVersion, &result.ReceiptID, &result.CanonicalEdgeID,
		&result.SpecificationNodeID, &result.ImplementationNodeID, &result.OriginProposalOccurrenceID,
		&result.ReviewerID, &result.DecisionReason, &result.ProducerSessionRef,
		&result.BasisID, &result.ReportID, &result.CandidateID, &result.DisplayID,
		&result.BasisPayloadUTF8, &result.ReceiptPayloadUTF8, &result.DisplayMediaType,
		&result.DisplayPayloadUTF8, &result.BasisPayloadHash, &result.ReceiptPayloadHash,
		&result.DisplayPayloadHash,
	)
	if err != nil {
		return nil, fmt.Errorf("loading canonical implements reviewed authority: %w", err)
	}
	if err := validateCanonicalImplementsReadAuthority(edge, result); err != nil {
		return nil, err
	}
	return &result, nil
}

func validateCanonicalImplementsReadAuthority(edge CanonicalGraphEdge, authority CanonicalImplementsAdmissionAuthority) error {
	var methodVersion, receiptContract, displayMediaType string
	switch authority.ContractVersion {
	case "canonical-derived-implements-admission/v1":
		methodVersion = "v1"
		receiptContract = "lab-canonical-implements-derived-review-receipt/v1"
		displayMediaType = "application/vnd.ahe.lab-implements-derived-review.v1+json"
	case "canonical-derived-implements-admission/v2":
		methodVersion = "v2"
		receiptContract = "canonical-implements-recursive-review-receipt/v2"
		displayMediaType = "application/vnd.ahe.implements-recursive-review.v2+json"
	default:
		return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements authority has an unsupported contract version")
	}
	if authority.CanonicalEdgeID != edge.ID || authority.SpecificationNodeID != edge.From ||
		authority.ImplementationNodeID != edge.To || authority.OriginProposalOccurrenceID != edge.OriginProposalOccurrenceID ||
		edge.Relation != evidencegraph.CanonicalImplements {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements authority differs from its edge")
	}
	if len(authority.RequestID) == 0 || len(authority.RequestID) > 200 || strings.TrimSpace(authority.RequestID) != authority.RequestID ||
		len(authority.ReviewerID) == 0 || len(authority.ReviewerID) > 200 || strings.TrimSpace(authority.ReviewerID) == "" ||
		len(authority.DecisionReason) == 0 || len(authority.DecisionReason) > 2000 || strings.TrimSpace(authority.DecisionReason) == "" ||
		len(authority.ProducerSessionRef) > 2000 || authority.DisplayMediaType != displayMediaType {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements authority metadata exceeds its fixed contract")
	}
	for _, body := range []struct {
		text string
		hash string
		max  int
	}{
		{authority.BasisPayloadUTF8, authority.BasisPayloadHash, 8 << 20},
		{authority.ReceiptPayloadUTF8, authority.ReceiptPayloadHash, 256 << 10},
		{authority.DisplayPayloadUTF8, authority.DisplayPayloadHash, 128 << 10},
	} {
		if len(body.text) == 0 || len(body.text) > body.max {
			return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements retained payload exceeds its byte bound")
		}
		sum := sha256.Sum256([]byte(body.text))
		if body.hash != "sha256:"+hex.EncodeToString(sum[:]) || !json.Valid([]byte(body.text)) {
			return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements retained payload hash or JSON differs")
		}
	}
	want := evidencegraph.ProvenanceRecord{
		ID:            "provenance:" + edge.ID,
		OriginRefs:    []string{edge.From, edge.To, authority.OriginProposalOccurrenceID},
		OriginGroupID: edge.ID, Producer: "ahe-wrap", Method: "reviewed_derived_implements",
		MethodVersion: methodVersion, TraceRef: authority.RequestID, ReviewRef: authority.ReceiptID,
	}
	if !reflect.DeepEqual(edge.Provenance, want) {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements provenance lacks its exact reviewed authority")
	}
	var receipt struct {
		ID              string `json:"id"`
		ContractVersion string `json:"contract_version"`
		Authority       string `json:"authority"`
		Direction       string `json:"direction"`
		SemanticEffect  string `json:"semantic_effect"`
		PolicyEffect    string `json:"policy_effect"`
		ReviewerID      string `json:"reviewer_id"`
		DecisionReason  string `json:"decision_reason"`
		Subject         struct {
			AdmittedCutID   string `json:"admitted_cut_id"`
			BasisID         string `json:"basis_id"`
			ReportID        string `json:"report_id"`
			CandidateID     string `json:"candidate_id"`
			DisplayID       string `json:"display_id"`
			ReviewPackageID string `json:"review_package_id"`
		} `json:"subject"`
		Display struct {
			ID          string `json:"id"`
			MediaType   string `json:"media_type"`
			PayloadUTF8 string `json:"payload_utf8"`
		} `json:"display"`
		ProjectedEdge struct {
			ID                  string `json:"id"`
			From                string `json:"from"`
			To                  string `json:"to"`
			Relation            string `json:"relation"`
			ProvenanceReceiptID string `json:"provenance_receipt_id"`
		} `json:"projected_edge"`
	}
	if err := json.Unmarshal([]byte(authority.ReceiptPayloadUTF8), &receipt); err != nil {
		return fmt.Errorf("decoding canonical implements retained receipt: %w", err)
	}
	if receipt.ContractVersion != receiptContract || receipt.ID != authority.ReceiptID ||
		receipt.Authority != "review_asserted_lab_receipt" || receipt.Direction != "specification_claim_to_implementation_claim" ||
		receipt.SemanticEffect != "direct_structural_navigation_only" || receipt.PolicyEffect != "no_truth_support_status_supersession_or_currentness_effect" ||
		receipt.ReviewerID != authority.ReviewerID || receipt.DecisionReason != authority.DecisionReason ||
		receipt.Subject.BasisID != authority.BasisID || receipt.Subject.ReportID != authority.ReportID ||
		receipt.Subject.CandidateID != authority.CandidateID || receipt.Subject.DisplayID != authority.DisplayID ||
		receipt.Display.ID != authority.DisplayID || receipt.Display.MediaType != authority.DisplayMediaType ||
		receipt.Display.PayloadUTF8 != authority.DisplayPayloadUTF8 || receipt.ProjectedEdge.ID != edge.ID ||
		receipt.ProjectedEdge.From != edge.From || receipt.ProjectedEdge.To != edge.To ||
		receipt.ProjectedEdge.Relation != "implements" || receipt.ProjectedEdge.ProvenanceReceiptID != authority.ReceiptID {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "canonical implements retained receipt differs from native authority")
	}
	return nil
}
