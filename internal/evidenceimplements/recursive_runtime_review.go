package evidenceimplements

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecursiveReviewRequest names admitted endpoints and per-layer assertions.
// Rules are reviewed assertions, not reconstructed historical reasoning. Native
// derivation IDs, parents, source bytes and code facts are loaded independently.
type RecursiveReviewRequest struct {
	SpecificationNodeID  string                    `json:"specification_node_id"`
	ImplementationNodeID string                    `json:"implementation_node_id"`
	Rules                []RecursiveDerivationRule `json:"rules"`
	Mapping              ReviewMapping             `json:"mapping"`
}

// RecursiveReview is one bounded, complete native ancestor AND closure. Loading
// a review is read-only and does not authorize the projected implements edge.
type RecursiveReview struct {
	Request                    RecursiveReviewRequest `json:"request"`
	Cut                        AdmittedCut            `json:"cut"`
	Report                     CandidateReport        `json:"report"`
	Basis                      RecursiveReviewBasis   `json:"basis"`
	Display                    DerivedReviewDisplay   `json:"display"`
	Subject                    DerivedReviewSubject   `json:"subject"`
	OriginProposalOccurrenceID string                 `json:"origin_proposal_occurrence_id"`
}

// LoadRecursiveReview reconstructs every ancestor in a read-only repeatable-read
// transaction. Missing evidence, cycles and exceeded bounds are hard failures.
func LoadRecursiveReview(ctx context.Context, pool *pgxpool.Pool, input RecursiveReviewRequest) (RecursiveReview, error) {
	if pool == nil {
		return RecursiveReview{}, fmt.Errorf("%w: postgres pool is required", ErrInvalid)
	}
	if err := validateRecursiveReviewRequest(input); err != nil {
		return RecursiveReview{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RecursiveReview{}, fmt.Errorf("beginning recursive implements review: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return RecursiveReview{}, fmt.Errorf("setting implements review policy: %w", err)
	}
	result, err := loadRecursiveReviewInTx(ctx, tx, input)
	if err != nil {
		return RecursiveReview{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RecursiveReview{}, fmt.Errorf("committing recursive review snapshot: %w", err)
	}
	return result, nil
}

func loadRecursiveReviewInTx(ctx context.Context, tx pgx.Tx, input RecursiveReviewRequest) (RecursiveReview, error) {
	if err := validateRecursiveReviewRequest(input); err != nil {
		return RecursiveReview{}, err
	}
	native, err := evidenceingestion.LoadRecursiveImplementsNativeBasisInTx(ctx, tx, input.SpecificationNodeID, input.ImplementationNodeID)
	if err != nil {
		return RecursiveReview{}, err
	}
	if native.Specification.CanonicalID != input.SpecificationNodeID || native.Implementation.CanonicalID != input.ImplementationNodeID ||
		native.Implementation.OriginProposal.CodeFact == nil || len(native.DerivedNodes) == 0 ||
		len(native.DerivedNodes)+len(native.SourceLeaves) > MaxRecursiveNodes {
		return RecursiveReview{}, fmt.Errorf("%w: incomplete recursive native coordinates", ErrInvalid)
	}
	derivationIDs := make(map[string]string, len(native.Ancestors.Derivations))
	for _, d := range native.Ancestors.Derivations {
		derivationIDs[d.NodeID] = d.ID
	}
	specs := make([]SpecificationInput, 0, len(native.DerivedNodes)+len(native.SourceLeaves))
	for _, node := range native.DerivedNodes {
		specs = append(specs, SpecificationInput{NodeID: node.CanonicalID, NodeKind: node.NodeKind,
			DerivationID: derivationIDs[node.CanonicalID], SourceTitle: node.Payload.Title, SourceLocation: node.Payload.Source,
			SourceRevision: node.OriginProposal.SourceVersion, ClaimText: node.Payload.Claim, ClaimHash: ExcerptHash(node.Payload.Claim)})
	}
	for _, leaf := range native.SourceLeaves {
		original := leaf.ReviewSnapshot.ReviewPackage.ProposalBasis
		specs = append(specs, SpecificationInput{NodeID: leaf.Node.CanonicalID, NodeKind: leaf.Node.NodeKind,
			SourceTitle: original.SourceTitle, SourceLocation: original.SourceLocation, SourceRevision: original.SourceVersion,
			ClaimText: leaf.Node.Payload.Claim, ClaimHash: ExcerptHash(leaf.Node.Payload.Claim)})
	}
	code, fact := native.Implementation, native.Implementation.OriginProposal.CodeFact
	cut, err := NewAdmittedCut(specs, []ImplementationInput{{NodeID: code.CanonicalID, NodeKind: code.NodeKind,
		RepositoryID: fact.RepoID, SourceTitle: code.Payload.Title, SourceLocation: code.Payload.Source,
		Revision: fact.CommitSHA, Path: fact.Path, Span: fmt.Sprintf("bytes:%d-%d", fact.StartByte, fact.EndByte),
		SymbolKind: fact.SymbolKind, QualifiedName: fact.QualifiedName, ExactExcerpt: fact.QuotedText, ExcerptHash: fact.QuotedTextHash}})
	if err != nil {
		return RecursiveReview{}, err
	}
	basis := RecursiveReviewBasis{AdmittedCutID: cut.ID, RootNodeID: native.Specification.CanonicalID,
		Ancestors: native.Ancestors, Rules: input.Rules, SourceLeaves: make([]ReviewEvidenceSnapshot, 0, len(native.SourceLeaves))}
	for _, leaf := range native.SourceLeaves {
		evidence, err := NewReviewEvidenceSnapshot(cut, ReviewEvidenceInput{AdmittedCutID: cut.ID,
			SpecificationNodeID: leaf.Node.CanonicalID, ReviewSnapshot: leaf.ReviewSnapshot, RawText: leaf.RawText, RenderedText: leaf.RenderedText})
		if err != nil {
			return RecursiveReview{}, err
		}
		basis.SourceLeaves = append(basis.SourceLeaves, evidence)
	}
	basis, err = NewRecursiveReviewBasis(cut, basis)
	if err != nil {
		return RecursiveReview{}, err
	}
	report, err := ProposeCandidates(cut)
	if err != nil {
		return RecursiveReview{}, err
	}
	for _, candidate := range report.Candidates {
		if candidate.Specification.NodeID != input.SpecificationNodeID || candidate.Implementation.NodeID != input.ImplementationNodeID {
			continue
		}
		input.Mapping, err = NormalizeReviewMapping(candidate, input.Mapping)
		if err != nil {
			return RecursiveReview{}, err
		}
		display, subject, err := BuildRecursiveReviewDisplay(cut, report, candidate.ID, basis, input.Mapping)
		if err != nil {
			return RecursiveReview{}, err
		}
		input.Rules = append([]RecursiveDerivationRule(nil), basis.Rules...)
		return RecursiveReview{Request: input, Cut: cut, Report: report, Basis: basis, Display: display,
			Subject: subject, OriginProposalOccurrenceID: native.Specification.OriginProposalOccurrenceID}, nil
	}
	return RecursiveReview{}, fmt.Errorf("%w: requested recursive pair was not proposed by the bounded candidate policy", ErrEndpointMissing)
}

func validateRecursiveReviewRequest(input RecursiveReviewRequest) error {
	if len(input.Rules) < 1 || len(input.Rules) > MaxRecursiveNodes {
		return fmt.Errorf("%w: recursive rules exceed the bounded contract", ErrInvalid)
	}
	seen := make(map[string]bool, len(input.Rules))
	for _, rule := range input.Rules {
		for _, id := range []string{rule.NodeID, rule.DerivationID} {
			if id == "" || len(id) > MaxNodeIDBytes || strings.TrimSpace(id) != id || !utf8.ValidString(id) || strings.ContainsRune(id, '\x00') {
				return fmt.Errorf("%w: invalid recursive rule coordinate", ErrInvalid)
			}
		}
		if seen[rule.NodeID] {
			return fmt.Errorf("%w: duplicate recursive rule", ErrInvalid)
		}
		seen[rule.NodeID] = true
		// Reuse the bounded assertion/mapping checks, not the v1 depth contract.
		if err := validateDerivedReviewRequest(DerivedReviewRequest{SpecificationNodeID: input.SpecificationNodeID,
			ImplementationNodeID: input.ImplementationNodeID, RuleStatement: rule.RuleStatement, Mapping: input.Mapping}); err != nil {
			return err
		}
	}
	return nil
}
