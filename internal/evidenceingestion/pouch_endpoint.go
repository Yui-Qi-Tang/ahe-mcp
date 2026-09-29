package evidenceingestion

import (
	"context"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/pouchvalidation"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PouchEndpointReview combines independent finite validation with exact native
// review. Authority must be supplied by the application, outside sender input.
// Generic endpoint admission remains generic and never implies this validation.
type PouchEndpointReview struct {
	Validation pouchvalidation.Receipt `json:"validation"`
	Native     EndpointReview          `json:"native"`
}

// PouchEndpointReceipt separates a model conclusion from an admission receipt.
type PouchEndpointReceipt struct {
	Validation pouchvalidation.Receipt  `json:"validation"`
	Native     EndpointAdmissionReceipt `json:"native"`
}

// LoadPouchEndpointReview validates a typed certificate, then checks its exact
// native statement and direct source bytes. It accepts source-claim parents only.
func LoadPouchEndpointReview(ctx context.Context, pool *pgxpool.Pool, authority *pouchvalidation.Authority, requestID string, packet []byte, req EndpointReviewRequest) (PouchEndpointReview, error) {
	var result PouchEndpointReview
	receipt, err := authority.Validate(ctx, requestID, packet)
	if err != nil {
		return result, err
	}
	if req.Kind != "derived_spec" || req.Derivation == nil || !slices.Equal(req.Derivation.ParentNodeIDs, authority.SourceIDs()) || req.Derivation.Method != pouchvalidation.Version || req.Derivation.Producer != "ahe-pouch-validator" || req.Derivation.TraceRef != "pouch:sha256:"+receipt.PackageSHA256 {
		return result, newDomainError(ErrorInvalidInput, "pouch derivation must bind the exact validated package and caller sources")
	}
	review, err := LoadEndpointReview(ctx, pool, req)
	if err != nil {
		return result, err
	}
	if err := validatePouchNativeBasis(authority, receipt, review); err != nil {
		return result, err
	}
	return PouchEndpointReview{Validation: receipt, Native: review}, nil
}
func validatePouchNativeBasis(authority *pouchvalidation.Authority, receipt pouchvalidation.Receipt, review EndpointReview) error {
	if review.Display.Proposal.StatementText != receipt.Statement() || len(review.Display.SourceLeaves) != len(authority.SourceIDs()) {
		return newDomainError(ErrorInvalidInput, "pouch native statement or source set differs")
	}
	seen := map[string]bool{}
	for _, leaf := range review.Display.SourceLeaves {
		id := leaf.Node.CanonicalID
		expected, ok := authority.SourceDigest(id)
		if !ok || seen[id] || pouchvalidation.Digest([]byte(leaf.RawText)) != expected {
			return newDomainError(ErrorInvalidInput, "pouch native source bytes differ from caller authority")
		}
		seen[id] = true
	}
	return nil
}

// AdmitReviewedPouchEndpoint repeats validation before ordinary exact admission.
// Approval is still required. No sender verdict grants write authority. The
// native transaction rechecks the exact review subject, covering changes between
// this read-only validation and admission. Source snapshots are immutable.
func AdmitReviewedPouchEndpoint(ctx context.Context, pool *pgxpool.Pool, authority *pouchvalidation.Authority, requestID string, packet []byte, input ReviewedEndpointAdmissionInput) (PouchEndpointReceipt, error) {
	var result PouchEndpointReceipt
	reviewed, err := LoadPouchEndpointReview(ctx, pool, authority, requestID, packet, input.Review)
	if err != nil {
		return result, err
	}
	if reviewed.Native.Subject != input.ExpectedSubject {
		return result, newDomainError(ErrorReviewContractConflict, "pouch exact review subject changed")
	}
	receipt, err := AdmitReviewedEndpoint(ctx, pool, input)
	if err != nil {
		return result, err
	}
	return PouchEndpointReceipt{Validation: reviewed.Validation, Native: receipt}, nil
}
