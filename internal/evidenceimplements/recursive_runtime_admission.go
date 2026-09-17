package evidenceimplements

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecursiveAdmissionContract binds a v2 recursive review to native persistence.
// It does not reinterpret any v1 receipt or authorize transitive implements.
const RecursiveAdmissionContract = "canonical-derived-implements-admission/v2"

// RecursiveAdmissionInput requires explicit approval of the exact v2 review.
// Session is audit-only; changing it on replay fails without changing identity.
type RecursiveAdmissionInput struct {
	RequestID          string                 `json:"request_id"`
	Review             RecursiveReviewRequest `json:"review"`
	Receipt            DerivedReviewReceipt   `json:"receipt"`
	Decision           string                 `json:"decision"`
	ProducerSessionRef string                 `json:"producer_session_ref"`
}

type recursiveAdmissionPacket struct {
	Cut    AdmittedCut          `json:"cut"`
	Report CandidateReport      `json:"report"`
	Basis  RecursiveReviewBasis `json:"basis"`
}

// AdmitReviewedRecursive reloads and validates the complete native AND closure
// before atomically appending one root-to-code edge and its v2 receipt. Missing
// layers, stale review bytes, cycles and unknown receipt versions fail closed.
func AdmitReviewedRecursive(ctx context.Context, pool *pgxpool.Pool, input RecursiveAdmissionInput) (DerivedAdmissionResult, error) {
	if pool == nil {
		return DerivedAdmissionResult{}, fmt.Errorf("%w: postgres pool is required", ErrInvalid)
	}
	if err := validateRecursiveAdmissionInput(input); err != nil {
		return DerivedAdmissionResult{}, err
	}
	// Detach both the rules and the nested mapping/receipt before the transaction.
	encoded, err := json.Marshal(input)
	if err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("encoding recursive admission: %w", err)
	}
	var owned RecursiveAdmissionInput
	if err := json.Unmarshal(encoded, &owned); err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("copying recursive admission: %w", err)
	}
	var last error
	for range derivedAdmissionAttempts {
		if err := ctx.Err(); err != nil {
			return DerivedAdmissionResult{}, err
		}
		result, err := admitReviewedRecursiveAttempt(ctx, pool, owned)
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

func validateRecursiveAdmissionInput(input RecursiveAdmissionInput) error {
	if err := validateRecursiveReviewRequest(input.Review); err != nil {
		return err
	}
	if input.Receipt.ContractVersion != RecursiveReceiptVersion || input.Receipt.Display.ContractVersion != RecursiveDisplayVersion {
		return fmt.Errorf("%w: recursive writer requires an explicit v2 receipt and display", ErrInvalid)
	}
	return validateReviewedAdmissionEnvelope(DerivedAdmissionInput{RequestID: input.RequestID, Decision: input.Decision,
		ProducerSessionRef: input.ProducerSessionRef, Receipt: input.Receipt, Review: DerivedReviewRequest{
			SpecificationNodeID: input.Review.SpecificationNodeID, ImplementationNodeID: input.Review.ImplementationNodeID}})
}

func admitReviewedRecursiveAttempt(ctx context.Context, pool *pgxpool.Pool, input RecursiveAdmissionInput) (DerivedAdmissionResult, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("beginning recursive implements admission: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return DerivedAdmissionResult{}, fmt.Errorf("setting implements review policy: %w", err)
	}
	review, err := loadRecursiveReviewInTx(ctx, tx, input.Review)
	if err != nil {
		return DerivedAdmissionResult{}, err
	}
	if input.Receipt.Subject != review.Subject || input.Receipt.Display != review.Display ||
		!reflect.DeepEqual(input.Receipt.Mapping, review.Request.Mapping) {
		return DerivedAdmissionResult{}, fmt.Errorf("%w: recursive receipt differs from the complete native review", ErrReplayConflict)
	}
	if err := ValidateRecursiveReviewReceipt(review.Cut, review.Report, review.Basis, input.Receipt); err != nil {
		return DerivedAdmissionResult{}, err
	}
	want, edge, err := newRecursiveAdmissionRecord(input, review)
	if err != nil {
		return DerivedAdmissionResult{}, err
	}
	return persistReviewedImplementsInTx(ctx, tx, want, edge, input.Receipt)
}

func newRecursiveAdmissionRecord(input RecursiveAdmissionInput, review RecursiveReview) (evidenceingestion.CanonicalImplementsAdmissionAuthority, evidenceingestion.CanonicalGraphEdge, error) {
	fail := func(err error) (evidenceingestion.CanonicalImplementsAdmissionAuthority, evidenceingestion.CanonicalGraphEdge, error) {
		return evidenceingestion.CanonicalImplementsAdmissionAuthority{}, evidenceingestion.CanonicalGraphEdge{}, err
	}
	basis, err := json.Marshal(recursiveAdmissionPacket{Cut: review.Cut, Report: review.Report, Basis: review.Basis})
	if err != nil || len(basis) > maxDerivedAdmissionPacketBytes {
		return fail(fmt.Errorf("%w: recursive native packet exceeds byte bound", ErrInvalid))
	}
	receipt, err := json.Marshal(input.Receipt)
	if err != nil {
		return fail(fmt.Errorf("encoding recursive implements receipt: %w", err))
	}
	want := evidenceingestion.CanonicalImplementsAdmissionAuthority{
		RequestID: input.RequestID, ContractVersion: RecursiveAdmissionContract, ReceiptID: input.Receipt.ID,
		CanonicalEdgeID: input.Receipt.ProjectedEdge.ID, SpecificationNodeID: input.Review.SpecificationNodeID,
		ImplementationNodeID: input.Review.ImplementationNodeID, OriginProposalOccurrenceID: review.OriginProposalOccurrenceID,
		ReviewerID: input.Receipt.ReviewerID, DecisionReason: input.Receipt.DecisionReason, ProducerSessionRef: input.ProducerSessionRef,
		BasisID: review.Basis.ID, ReportID: review.Report.ID, CandidateID: review.Subject.CandidateID, DisplayID: review.Display.ID,
		BasisPayloadUTF8: string(basis), ReceiptPayloadUTF8: string(receipt), DisplayMediaType: review.Display.MediaType,
		DisplayPayloadUTF8: review.Display.PayloadUTF8, BasisPayloadHash: ExcerptHash(string(basis)),
		ReceiptPayloadHash: ExcerptHash(string(receipt)), DisplayPayloadHash: ExcerptHash(review.Display.PayloadUTF8),
	}
	edge := evidenceingestion.CanonicalGraphEdge{ID: want.CanonicalEdgeID, From: want.SpecificationNodeID, To: want.ImplementationNodeID,
		Relation: evidencegraph.CanonicalImplements, OriginProposalOccurrenceID: want.OriginProposalOccurrenceID,
		Provenance: evidencegraph.ProvenanceRecord{ID: "provenance:" + want.CanonicalEdgeID,
			OriginRefs:    []string{want.SpecificationNodeID, want.ImplementationNodeID, want.OriginProposalOccurrenceID},
			OriginGroupID: want.CanonicalEdgeID, Producer: "ahe-wrap", Method: "reviewed_derived_implements",
			MethodVersion: "v2", TraceRef: want.RequestID, ReviewRef: want.ReceiptID}}
	return want, edge, nil
}
