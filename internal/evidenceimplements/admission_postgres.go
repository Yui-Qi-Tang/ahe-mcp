package evidenceimplements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DerivedAdmissionContract identifies native persistence authority, not a
// replacement for the retained Lab receipt's structural review contract.
const DerivedAdmissionContract = "canonical-derived-implements-admission/v1"

const (
	derivedAdmissionAttempts        = 3
	maxDerivedAdmissionPacketBytes  = 8 << 20
	maxDerivedAdmissionReceiptBytes = 256 << 10
)

// DerivedAdmissionInput supplies a reviewed receipt and coordinates only.
// Source, parent and code authority are always reloaded from PostgreSQL.
// Decision must explicitly be approved; a reviewer string is audit metadata,
// not authentication. The caller must already hold admission authority.
type DerivedAdmissionInput struct {
	RequestID          string               `json:"request_id"`
	Review             DerivedReviewRequest `json:"review"`
	Receipt            DerivedReviewReceipt `json:"receipt"`
	Decision           string               `json:"decision"`
	ProducerSessionRef string               `json:"producer_session_ref"`
}

// DerivedAdmissionResult identifies a committed relation, never a new node or
// a change to the original derived claim's admission decision.
type DerivedAdmissionResult struct {
	RequestID string                               `json:"request_id"`
	Receipt   DerivedReviewReceipt                 `json:"receipt"`
	Edge      evidenceingestion.CanonicalGraphEdge `json:"edge"`
	Replayed  bool                                 `json:"replayed"`
}

type derivedAdmissionPacket struct {
	Cut    AdmittedCut        `json:"cut"`
	Report CandidateReport    `json:"report"`
	Basis  DerivedReviewBasis `json:"basis"`
}

// AdmitReviewedDerived creates exactly one specification-to-implementation
// edge and its distinct append-only review authority in one transaction. It
// does not admit a pending source proposal or accept a caller-defined graph.
func AdmitReviewedDerived(ctx context.Context, pool *pgxpool.Pool, input DerivedAdmissionInput) (DerivedAdmissionResult, error) {
	if pool == nil {
		return DerivedAdmissionResult{}, fmt.Errorf("%w: postgres pool is required", ErrInvalid)
	}
	if err := validateDerivedAdmissionInput(input); err != nil {
		return DerivedAdmissionResult{}, err
	}
	// Own nested witness/limitation slices before returning a committed receipt.
	receiptJSON, err := json.Marshal(input.Receipt)
	if err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("encoding reviewed receipt: %w", err)
	}
	var ownedReceipt DerivedReviewReceipt
	if err := json.Unmarshal(receiptJSON, &ownedReceipt); err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("copying reviewed receipt: %w", err)
	}
	input.Receipt = ownedReceipt
	var last error
	for range derivedAdmissionAttempts {
		if err := ctx.Err(); err != nil {
			return DerivedAdmissionResult{}, err
		}
		result, err := admitReviewedDerivedAttempt(ctx, pool, input)
		if err == nil {
			return result, nil
		}
		last = err
		if !retryDerivedAdmission(err) {
			break
		}
	}
	return DerivedAdmissionResult{}, last
}

func admitReviewedDerivedAttempt(ctx context.Context, pool *pgxpool.Pool, input DerivedAdmissionInput) (DerivedAdmissionResult, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("beginning derived implements admission: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()

	if _, err := tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("setting implements review policy: %w", err)
	}
	review, err := loadDerivedReviewInTx(ctx, tx, input.Review)
	if err != nil {
		return DerivedAdmissionResult{}, err
	}
	if input.Receipt.Subject != review.Subject || input.Receipt.Display != review.Display ||
		!reflect.DeepEqual(input.Receipt.Mapping, review.Request.Mapping) {
		return DerivedAdmissionResult{}, fmt.Errorf("%w: reviewed receipt differs from native review subject", ErrReplayConflict)
	}
	if err := ValidateDerivedReviewReceipt(review.Cut, review.Report, review.Basis, input.Receipt); err != nil {
		return DerivedAdmissionResult{}, err
	}
	want, edge, err := newDerivedAdmissionRecord(input, review)
	if err != nil {
		return DerivedAdmissionResult{}, err
	}
	return persistReviewedImplementsInTx(ctx, tx, want, edge, input.Receipt)
}

// Both review versions share the same single-use directed pair and atomic
// persistence path, after their separate native reconstruction and validation.
func persistReviewedImplementsInTx(ctx context.Context, tx pgx.Tx, want evidenceingestion.CanonicalImplementsAdmissionAuthority,
	edge evidenceingestion.CanonicalGraphEdge, receipt DerivedReviewReceipt,
) (DerivedAdmissionResult, error) {
	stored, found, err := loadDerivedAdmissionRecord(ctx, tx, want)
	if err != nil {
		return DerivedAdmissionResult{}, err
	}
	if found {
		// Exact text comparison also rejects unknown persisted JSON fields or
		// a replacement snapshot that merely decodes into the same Go fields.
		if stored != want {
			return DerivedAdmissionResult{}, fmt.Errorf("%w: request, directed pair or receipt already has different authority", ErrReplayConflict)
		}
		if err := validateDerivedAdmissionEdge(ctx, tx, edge); err != nil {
			return DerivedAdmissionResult{}, err
		}
	} else {
		if err := insertDerivedAdmissionRecord(ctx, tx, want); err != nil {
			return DerivedAdmissionResult{}, err
		}
		provenance, err := json.Marshal(edge.Provenance)
		if err != nil {
			return DerivedAdmissionResult{}, fmt.Errorf("encoding implements provenance: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO canonical_graph_edges
			(canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id)
			VALUES ($1,$2,$3,'implements',$4::jsonb,$5)`, edge.ID, edge.From, edge.To, string(provenance), edge.OriginProposalOccurrenceID); err != nil {
			return DerivedAdmissionResult{}, fmt.Errorf("inserting canonical implements edge: %w", err)
		}
	}
	// Constraints are deferred until commit; no edge/receipt escapes on failure.
	if err := tx.Commit(ctx); err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("committing derived implements admission: %w", err)
	}
	return DerivedAdmissionResult{RequestID: want.RequestID, Receipt: receipt, Edge: edge, Replayed: found}, nil
}

func validateDerivedAdmissionInput(input DerivedAdmissionInput) error {
	if strings.TrimSpace(input.RequestID) != input.RequestID {
		return fmt.Errorf("%w: request ID must not contain surrounding whitespace", ErrInvalid)
	}
	if err := validateDerivedReviewRequest(input.Review); err != nil {
		return err
	}
	return validateReviewedAdmissionEnvelope(input)
}

func validateReviewedAdmissionEnvelope(input DerivedAdmissionInput) error {
	if strings.TrimSpace(input.RequestID) != input.RequestID {
		return fmt.Errorf("%w: request ID must not contain surrounding whitespace", ErrInvalid)
	}
	if input.Decision != "approved" {
		return fmt.Errorf("%w: an explicit approved decision is required", ErrInvalid)
	}
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"request_id", input.RequestID, 200},
		{"reviewer_id", input.Receipt.ReviewerID, 200},
		{"decision_reason", input.Receipt.DecisionReason, 2000},
	} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > field.limit ||
			!utf8.ValidString(field.value) || strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("%w: invalid %s", ErrInvalid, field.name)
		}
	}
	if len(input.ProducerSessionRef) > 2000 || !utf8.ValidString(input.ProducerSessionRef) || strings.ContainsRune(input.ProducerSessionRef, '\x00') {
		return fmt.Errorf("%w: invalid producer session metadata", ErrInvalid)
	}
	if len(input.Receipt.Display.PayloadUTF8) > MaxReviewDisplayBytes || !utf8.ValidString(input.Receipt.Display.PayloadUTF8) ||
		strings.ContainsRune(input.Receipt.Display.PayloadUTF8, '\x00') {
		return fmt.Errorf("%w: derived display exceeds byte bound", ErrInvalid)
	}
	coordinates := Candidate{Specification: Specification{NodeID: input.Review.SpecificationNodeID},
		Implementation: Implementation{NodeID: input.Review.ImplementationNodeID}}
	if _, err := NormalizeReviewMapping(coordinates, input.Receipt.Mapping); err != nil {
		return err
	}
	// Every non-payload string is bounded before marshaling the receipt.
	for _, value := range []string{
		input.Receipt.ContractVersion, input.Receipt.ID, input.Receipt.Authority, input.Receipt.Direction,
		input.Receipt.SemanticEffect, input.Receipt.PolicyEffect, input.Receipt.Display.ContractVersion,
		input.Receipt.Display.ID, input.Receipt.Display.MediaType, input.Receipt.Subject.AdmittedCutID,
		input.Receipt.Subject.ReportID, input.Receipt.Subject.CandidateID, input.Receipt.Subject.BasisID,
		input.Receipt.Subject.ReviewPackageID, input.Receipt.Subject.DisplayID, input.Receipt.ProjectedEdge.ID,
		input.Receipt.ProjectedEdge.From, input.Receipt.ProjectedEdge.To, input.Receipt.ProjectedEdge.ProvenanceReceiptID,
		string(input.Receipt.ProjectedEdge.Relation),
	} {
		if len(value) > MaxLocationBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: invalid receipt coordinate", ErrInvalid)
		}
	}
	encoded, err := json.Marshal(input.Receipt)
	if err != nil || len(encoded) > maxDerivedAdmissionReceiptBytes {
		return fmt.Errorf("%w: derived receipt exceeds byte bound", ErrInvalid)
	}
	return nil
}

func newDerivedAdmissionRecord(input DerivedAdmissionInput, review DerivedReview) (evidenceingestion.CanonicalImplementsAdmissionAuthority, evidenceingestion.CanonicalGraphEdge, error) {
	fail := func(err error) (evidenceingestion.CanonicalImplementsAdmissionAuthority, evidenceingestion.CanonicalGraphEdge, error) {
		return evidenceingestion.CanonicalImplementsAdmissionAuthority{}, evidenceingestion.CanonicalGraphEdge{}, err
	}
	basis, err := json.Marshal(derivedAdmissionPacket{Cut: review.Cut, Report: review.Report, Basis: review.Basis})
	if err != nil || len(basis) > maxDerivedAdmissionPacketBytes {
		return fail(fmt.Errorf("%w: native review packet exceeds byte bound", ErrInvalid))
	}
	receipt, err := json.Marshal(input.Receipt)
	if err != nil {
		return fail(fmt.Errorf("encoding derived implements receipt: %w", err))
	}
	want := evidenceingestion.CanonicalImplementsAdmissionAuthority{
		RequestID: input.RequestID, ContractVersion: DerivedAdmissionContract, ReceiptID: input.Receipt.ID,
		CanonicalEdgeID: input.Receipt.ProjectedEdge.ID, SpecificationNodeID: input.Review.SpecificationNodeID,
		ImplementationNodeID: input.Review.ImplementationNodeID, OriginProposalOccurrenceID: review.OriginProposalOccurrenceID,
		ReviewerID: input.Receipt.ReviewerID, DecisionReason: input.Receipt.DecisionReason, ProducerSessionRef: input.ProducerSessionRef,
		BasisID: review.Basis.ID, ReportID: review.Report.ID, CandidateID: review.Subject.CandidateID, DisplayID: review.Display.ID,
		BasisPayloadUTF8: string(basis), ReceiptPayloadUTF8: string(receipt), DisplayMediaType: review.Display.MediaType,
		DisplayPayloadUTF8: review.Display.PayloadUTF8, BasisPayloadHash: ExcerptHash(string(basis)),
		ReceiptPayloadHash: ExcerptHash(string(receipt)), DisplayPayloadHash: ExcerptHash(review.Display.PayloadUTF8),
	}
	edge := evidenceingestion.CanonicalGraphEdge{
		ID: want.CanonicalEdgeID, From: want.SpecificationNodeID, To: want.ImplementationNodeID,
		Relation: evidencegraph.CanonicalImplements, OriginProposalOccurrenceID: want.OriginProposalOccurrenceID,
		Provenance: evidencegraph.ProvenanceRecord{
			ID:            "provenance:" + want.CanonicalEdgeID,
			OriginRefs:    []string{want.SpecificationNodeID, want.ImplementationNodeID, want.OriginProposalOccurrenceID},
			OriginGroupID: want.CanonicalEdgeID, Producer: "ahe-wrap", Method: "reviewed_derived_implements",
			MethodVersion: "v1", TraceRef: want.RequestID, ReviewRef: want.ReceiptID,
		},
	}
	return want, edge, nil
}

func loadDerivedAdmissionRecord(ctx context.Context, tx pgx.Tx, wanted evidenceingestion.CanonicalImplementsAdmissionAuthority) (evidenceingestion.CanonicalImplementsAdmissionAuthority, bool, error) {
	var result evidenceingestion.CanonicalImplementsAdmissionAuthority
	rows, err := tx.Query(ctx, `SELECT request_id,contract_version,receipt_id,canonical_edge_id,
		specification_node_id,implementation_node_id,origin_proposal_occurrence_id,reviewer_id,decision_reason,producer_session_ref,
		basis_id,report_id,candidate_id,display_id,basis_payload_utf8,receipt_payload_utf8,display_media_type,display_payload_utf8,
		basis_payload_hash,receipt_payload_hash,display_payload_hash
		FROM canonical_implements_admissions WHERE request_id=$1 OR receipt_id=$2
		OR (specification_node_id=$3 AND implementation_node_id=$4) ORDER BY request_id LIMIT 3`,
		wanted.RequestID, wanted.ReceiptID, wanted.SpecificationNodeID, wanted.ImplementationNodeID)
	if err != nil {
		return result, false, fmt.Errorf("loading implements admission authority: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count != 1 {
			return result, false, fmt.Errorf("%w: multiple conflicting admission coordinates", ErrReplayConflict)
		}
		if err := rows.Scan(&result.RequestID, &result.ContractVersion, &result.ReceiptID, &result.CanonicalEdgeID,
			&result.SpecificationNodeID, &result.ImplementationNodeID, &result.OriginProposalOccurrenceID, &result.ReviewerID, &result.DecisionReason, &result.ProducerSessionRef,
			&result.BasisID, &result.ReportID, &result.CandidateID, &result.DisplayID, &result.BasisPayloadUTF8, &result.ReceiptPayloadUTF8,
			&result.DisplayMediaType, &result.DisplayPayloadUTF8, &result.BasisPayloadHash, &result.ReceiptPayloadHash, &result.DisplayPayloadHash); err != nil {
			return result, false, fmt.Errorf("scanning implements admission authority: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return result, false, fmt.Errorf("reading implements admission authority: %w", err)
	}
	return result, count == 1, nil
}

func insertDerivedAdmissionRecord(ctx context.Context, tx pgx.Tx, record evidenceingestion.CanonicalImplementsAdmissionAuthority) error {
	_, err := tx.Exec(ctx, `INSERT INTO canonical_implements_admissions
		(request_id,contract_version,receipt_id,canonical_edge_id,specification_node_id,implementation_node_id,
		origin_proposal_occurrence_id,reviewer_id,decision_reason,producer_session_ref,basis_id,report_id,candidate_id,display_id,
		basis_payload_utf8,receipt_payload_utf8,display_media_type,display_payload_utf8,basis_payload_hash,receipt_payload_hash,display_payload_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		record.RequestID, record.ContractVersion, record.ReceiptID, record.CanonicalEdgeID, record.SpecificationNodeID, record.ImplementationNodeID,
		record.OriginProposalOccurrenceID, record.ReviewerID, record.DecisionReason, record.ProducerSessionRef,
		record.BasisID, record.ReportID, record.CandidateID, record.DisplayID, record.BasisPayloadUTF8, record.ReceiptPayloadUTF8,
		record.DisplayMediaType, record.DisplayPayloadUTF8, record.BasisPayloadHash, record.ReceiptPayloadHash, record.DisplayPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting implements admission authority: %w", err)
	}
	return nil
}

func validateDerivedAdmissionEdge(ctx context.Context, tx pgx.Tx, expected evidenceingestion.CanonicalGraphEdge) error {
	var id, from, to, relation, origin string
	var body []byte
	if err := tx.QueryRow(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id
		FROM canonical_graph_edges WHERE canonical_edge_id=$1`, expected.ID).Scan(&id, &from, &to, &relation, &body, &origin); err != nil {
		return fmt.Errorf("reloading canonical implements edge: %w", err)
	}
	wantJSON, err := json.Marshal(expected.Provenance)
	if err != nil {
		return err
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("decoding stored implements edge provenance: %w", err)
	}
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		return err
	}
	if id != expected.ID || from != expected.From || to != expected.To || relation != string(expected.Relation) ||
		origin != expected.OriginProposalOccurrenceID || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: stored implements edge differs from reviewed authority", ErrReplayConflict)
	}
	return nil
}

func retryDerivedAdmission(err error) bool {
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) {
		return false
	}
	switch pgerr.Code {
	case "40001", "40P01", "23505":
		// A waiter must start a new snapshot after a winner commits. Merely
		// acquiring a lock inside an old RR snapshot is insufficient for replay.
		return true
	default:
		return false
	}
}
