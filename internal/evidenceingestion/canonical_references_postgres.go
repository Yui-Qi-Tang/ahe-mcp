package evidenceingestion

import (
	"context"
	"fmt"
)

func loadCanonicalReferencesAdmission(ctx context.Context, db sqlQueryer, edge CanonicalGraphEdge) (*CanonicalReferencesAdmissionAuthority, error) {
	var a CanonicalReferencesAdmissionAuthority
	err := db.queryRow(ctx, `SELECT request_id,contract_version,receipt_id,canonical_edge_id,
		from_node_id,to_node_id,origin_proposal_occurrence_id,reviewer_id,decision_reason,producer_session_ref,
		review_subject_id,cut_id,request_payload_utf8,request_payload_hash,review_payload_utf8,review_payload_hash,
		receipt_payload_utf8,receipt_payload_hash FROM canonical_references_admissions
		WHERE canonical_edge_id=$1 AND octet_length(request_payload_utf8) BETWEEN 1 AND 16384
		AND octet_length(review_payload_utf8) BETWEEN 1 AND 524288
		AND octet_length(receipt_payload_utf8) BETWEEN 1 AND 524288`, edge.ID).Scan(
		&a.RequestID, &a.ContractVersion, &a.ReceiptID, &a.CanonicalEdgeID, &a.FromNodeID, &a.ToNodeID,
		&a.OriginProposalOccurrenceID, &a.ReviewerID, &a.DecisionReason, &a.ProducerSessionRef,
		&a.ReviewSubjectID, &a.CutID, &a.RequestPayloadUTF8, &a.RequestPayloadHash, &a.ReviewPayloadUTF8,
		&a.ReviewPayloadHash, &a.ReceiptPayloadUTF8, &a.ReceiptPayloadHash)
	if err != nil {
		return nil, fmt.Errorf("loading canonical references authority: %w", err)
	}
	if err := ValidateCanonicalReferencesReadAuthority(edge, a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ValidateCanonicalReferencesReadAuthority validates a retained observation.
// It never scans today's candidates or recursively validates endpoint relations.
