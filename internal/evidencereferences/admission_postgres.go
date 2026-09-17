package evidencereferences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdmitReviewed checks historical requests before observing a fresh target cut.
// New admissions atomically append a receipt and an edge under native SQL guards.
func AdmitReviewed(ctx context.Context, pool *pgxpool.Pool, input AdmissionInput) (AdmissionResult, error) {
	if pool == nil {
		return AdmissionResult{}, fmt.Errorf("%w: postgres pool is required", ErrUnresolved)
	}
	if err := validateAdmission(input); err != nil {
		return AdmissionResult{}, err
	}
	var last error
	for range 3 {
		if err := ctx.Err(); err != nil {
			return AdmissionResult{}, err
		}
		result, err := admitAttempt(ctx, pool, input)
		if err == nil {
			return result, nil
		}
		last = err
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "40001" && pgErr.Code != "40P01" && pgErr.Code != "23505") {
			break
		}
	}
	return AdmissionResult{}, last
}

func admitAttempt(ctx context.Context, pool *pgxpool.Pool, in AdmissionInput) (AdmissionResult, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("beginning references admission: %w", err)
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return AdmissionResult{}, fmt.Errorf("setting references admission policy: %w", err)
	}
	stored, found, err := loadAuthority(ctx, tx, in.RequestID)
	if err != nil {
		return AdmissionResult{}, err
	}
	var review Review
	var basis evidenceingestion.ReferencesNativeBasis
	if found {
		requestJSON, _ := json.Marshal(in.Review)
		if stored.RequestPayloadUTF8 != string(requestJSON) || stored.ReviewerID != in.ReviewerID ||
			stored.DecisionReason != in.DecisionReason || stored.ProducerSessionRef != in.ProducerSessionRef ||
			stored.ReviewSubjectID != in.ExpectedSubject.ReviewSubjectID || stored.CutID != in.ExpectedSubject.CutID {
			return AdmissionResult{}, fmt.Errorf("%w: historical request metadata or reviewed subject differs", ErrReplayConflict)
		}
		edge, err := loadEdge(ctx, tx, stored.CanonicalEdgeID)
		if err != nil {
			return AdmissionResult{}, err
		}
		if err = evidenceingestion.ValidateCanonicalReferencesReadAuthority(edge, stored); err != nil {
			return AdmissionResult{}, err
		}
		// The retained receipt is native immutable authority, not a caller cut.
		var body ReviewBody
		if err = json.Unmarshal([]byte(stored.ReviewPayloadUTF8), &body); err != nil {
			return AdmissionResult{}, fmt.Errorf("decoding retained references review: %w", err)
		}
		basis, err = evidenceingestion.LoadReferencesNativeEndpointsInTx(ctx, tx, in.Review.FromNodeID, in.Review.ToNodeID)
		if err != nil {
			return AdmissionResult{}, err
		}
		basis.TargetCandidateIDs = body.Resolution.TargetCut.CandidateIDs
		review, err = buildReview(in.Review, basis)
		if err != nil {
			return AdmissionResult{}, err
		}
		if review.Display.PayloadUTF8 != stored.ReviewPayloadUTF8 || review.Subject != in.ExpectedSubject {
			return AdmissionResult{}, fmt.Errorf("%w: immutable references reconstruction differs", ErrReplayConflict)
		}
	} else {
		var pairExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM canonical_references_admissions WHERE from_node_id=$1 AND to_node_id=$2)`, in.Review.FromNodeID, in.Review.ToNodeID).Scan(&pairExists); err != nil {
			return AdmissionResult{}, fmt.Errorf("checking references directed pair: %w", err)
		}
		if pairExists {
			return AdmissionResult{}, fmt.Errorf("%w: directed pair is single-use", ErrReplayConflict)
		}
		basis, err = evidenceingestion.LoadReferencesNativeBasisInTx(ctx, tx, in.Review.FromNodeID, in.Review.ToNodeID)
		if err != nil {
			return AdmissionResult{}, err
		}
		review, err = buildReview(in.Review, basis)
		if err != nil {
			return AdmissionResult{}, err
		}
		if review.Subject != in.ExpectedSubject {
			return AdmissionResult{}, fmt.Errorf("%w: obtain and review a fresh references display", ErrReplayConflict)
		}
	}
	want, edge, receipt, err := admissionRecord(in, review, basis.From.Node.OriginProposalOccurrenceID)
	if err != nil {
		return AdmissionResult{}, err
	}
	if found {
		if stored != want {
			return AdmissionResult{}, fmt.Errorf("%w: retained exact payload differs", ErrReplayConflict)
		}
	} else {
		if err = insertAuthority(ctx, tx, want); err != nil {
			return AdmissionResult{}, err
		}
		provenance, err := json.Marshal(edge.Provenance)
		if err != nil {
			return AdmissionResult{}, fmt.Errorf("encoding references provenance: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO canonical_graph_edges(canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id)
			VALUES($1,$2,$3,'references',$4::jsonb,$5)`, edge.ID, edge.From, edge.To, string(provenance), edge.OriginProposalOccurrenceID); err != nil {
			return AdmissionResult{}, fmt.Errorf("inserting canonical references edge: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return AdmissionResult{}, fmt.Errorf("committing references admission: %w", err)
	}
	return AdmissionResult{RequestID: in.RequestID, Receipt: receipt, Edge: edge, Replayed: found}, nil
}

func loadAuthority(ctx context.Context, tx pgx.Tx, requestID string) (evidenceingestion.CanonicalReferencesAdmissionAuthority, bool, error) {
	var a evidenceingestion.CanonicalReferencesAdmissionAuthority
	err := tx.QueryRow(ctx, `SELECT request_id,contract_version,receipt_id,canonical_edge_id,from_node_id,to_node_id,
		origin_proposal_occurrence_id,reviewer_id,decision_reason,producer_session_ref,review_subject_id,cut_id,
		request_payload_utf8,request_payload_hash,review_payload_utf8,review_payload_hash,receipt_payload_utf8,receipt_payload_hash
		FROM canonical_references_admissions WHERE request_id=$1
		AND octet_length(request_payload_utf8) BETWEEN 1 AND 16384 AND octet_length(review_payload_utf8) BETWEEN 1 AND 524288
		AND octet_length(receipt_payload_utf8) BETWEEN 1 AND 524288`, requestID).Scan(
		&a.RequestID, &a.ContractVersion, &a.ReceiptID, &a.CanonicalEdgeID, &a.FromNodeID, &a.ToNodeID, &a.OriginProposalOccurrenceID,
		&a.ReviewerID, &a.DecisionReason, &a.ProducerSessionRef, &a.ReviewSubjectID, &a.CutID, &a.RequestPayloadUTF8, &a.RequestPayloadHash,
		&a.ReviewPayloadUTF8, &a.ReviewPayloadHash, &a.ReceiptPayloadUTF8, &a.ReceiptPayloadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, fmt.Errorf("loading retained references request: %w", err)
	}
	return a, true, nil
}

func insertAuthority(ctx context.Context, tx pgx.Tx, a evidenceingestion.CanonicalReferencesAdmissionAuthority) error {
	_, err := tx.Exec(ctx, `INSERT INTO canonical_references_admissions(request_id,contract_version,receipt_id,canonical_edge_id,
		from_node_id,to_node_id,origin_proposal_occurrence_id,reviewer_id,decision_reason,producer_session_ref,review_subject_id,cut_id,
		request_payload_utf8,request_payload_hash,review_payload_utf8,review_payload_hash,receipt_payload_utf8,receipt_payload_hash)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		a.RequestID, a.ContractVersion, a.ReceiptID, a.CanonicalEdgeID, a.FromNodeID, a.ToNodeID, a.OriginProposalOccurrenceID,
		a.ReviewerID, a.DecisionReason, a.ProducerSessionRef, a.ReviewSubjectID, a.CutID, a.RequestPayloadUTF8, a.RequestPayloadHash,
		a.ReviewPayloadUTF8, a.ReviewPayloadHash, a.ReceiptPayloadUTF8, a.ReceiptPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting references receipt authority: %w", err)
	}
	return nil
}

func loadEdge(ctx context.Context, tx pgx.Tx, id string) (evidenceingestion.CanonicalGraphEdge, error) {
	var e evidenceingestion.CanonicalGraphEdge
	var provenance []byte
	err := tx.QueryRow(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_proposal_occurrence_id
		FROM canonical_graph_edges WHERE canonical_edge_id=$1 AND octet_length(provenance::text)<=16384`, id).Scan(
		&e.ID, &e.From, &e.To, &e.Relation, &provenance, &e.OriginProposalOccurrenceID)
	if err != nil {
		return e, fmt.Errorf("loading retained references edge: %w", err)
	}
	if err = json.Unmarshal(provenance, &e.Provenance); err != nil {
		return e, fmt.Errorf("decoding retained references provenance: %w", err)
	}
	want, err := json.Marshal(e.Provenance)
	if err != nil {
		return e, fmt.Errorf("encoding retained references provenance: %w", err)
	}
	var exact bool
	if err = tx.QueryRow(ctx, `SELECT provenance=$2::jsonb FROM canonical_graph_edges WHERE canonical_edge_id=$1`, id, string(want)).Scan(&exact); err != nil {
		return e, fmt.Errorf("checking exact references provenance: %w", err)
	}
	if !exact {
		return e, fmt.Errorf("%w: retained provenance has undeclared fields", ErrReplayConflict)
	}
	return e, nil
}
