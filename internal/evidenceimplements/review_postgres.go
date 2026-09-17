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

// DerivedReviewRequest identifies admitted endpoints and explicit assertions
// to review. RuleStatement and Mapping are not native provenance authority.
type DerivedReviewRequest struct {
	SpecificationNodeID  string        `json:"specification_node_id"`
	ImplementationNodeID string        `json:"implementation_node_id"`
	RuleStatement        string        `json:"rule_statement"`
	Mapping              ReviewMapping `json:"mapping"`
}

// DerivedReview preserves one PostgreSQL-loaded exact review subject. Calling
// LoadDerivedReview does not approve it or write a canonical relation.
type DerivedReview struct {
	Request                    DerivedReviewRequest `json:"request"`
	Cut                        AdmittedCut          `json:"cut"`
	Report                     CandidateReport      `json:"report"`
	Basis                      DerivedReviewBasis   `json:"basis"`
	Display                    DerivedReviewDisplay `json:"display"`
	Subject                    DerivedReviewSubject `json:"subject"`
	OriginProposalOccurrenceID string               `json:"origin_proposal_occurrence_id"`
}

// LoadDerivedReview reconstructs a depth-one derived-specification review in
// one repeatable-read, read-only transaction. It does not trust caller parents,
// source maps, source bytes, admitted cuts, candidate reports or native graphs.
func LoadDerivedReview(ctx context.Context, pool *pgxpool.Pool, input DerivedReviewRequest) (DerivedReview, error) {
	if pool == nil {
		return DerivedReview{}, fmt.Errorf("%w: postgres pool is required", ErrInvalid)
	}
	if err := validateDerivedReviewRequest(input); err != nil {
		return DerivedReview{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return DerivedReview{}, fmt.Errorf("beginning derived implements review: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return DerivedReview{}, fmt.Errorf("setting implements review policy: %w", err)
	}
	result, err := loadDerivedReviewInTx(ctx, tx, input)
	if err != nil {
		return DerivedReview{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DerivedReview{}, fmt.Errorf("committing derived implements review snapshot: %w", err)
	}
	return result, nil
}

func loadDerivedReviewInTx(ctx context.Context, tx pgx.Tx, input DerivedReviewRequest) (DerivedReview, error) {
	if err := validateDerivedReviewRequest(input); err != nil {
		return DerivedReview{}, err
	}
	native, err := evidenceingestion.LoadDerivedImplementsNativeBasisInTx(ctx, tx, input.SpecificationNodeID, input.ImplementationNodeID)
	if err != nil {
		return DerivedReview{}, err
	}
	return buildDerivedReviewFromNative(input, native)
}

func buildDerivedReviewFromNative(input DerivedReviewRequest, native evidenceingestion.DerivedImplementsNativeBasis) (DerivedReview, error) {
	if len(native.Ancestors.Derivations) != 1 || len(native.SourceLeaves) < 1 || len(native.SourceLeaves) > MaxDerivedParents ||
		native.Specification.CanonicalID != input.SpecificationNodeID || native.Implementation.CanonicalID != input.ImplementationNodeID || native.Implementation.OriginProposal.CodeFact == nil {
		return DerivedReview{}, fmt.Errorf("%w: incomplete native derived review coordinates", ErrInvalid)
	}
	root, code := native.Specification, native.Implementation
	specs := make([]SpecificationInput, 0, len(native.SourceLeaves)+1)
	specs = append(specs, SpecificationInput{NodeID: root.CanonicalID, NodeKind: root.NodeKind,
		DerivationID: native.Ancestors.Derivations[0].ID, SourceTitle: root.Payload.Title,
		SourceLocation: root.Payload.Source, SourceRevision: root.OriginProposal.SourceVersion,
		ClaimText: root.Payload.Claim, ClaimHash: ExcerptHash(root.Payload.Claim)})
	for _, leaf := range native.SourceLeaves {
		original := leaf.ReviewSnapshot.ReviewPackage.ProposalBasis
		specs = append(specs, SpecificationInput{NodeID: leaf.Node.CanonicalID, NodeKind: leaf.Node.NodeKind,
			SourceTitle: original.SourceTitle, SourceLocation: original.SourceLocation, SourceRevision: original.SourceVersion,
			ClaimText: leaf.Node.Payload.Claim, ClaimHash: ExcerptHash(leaf.Node.Payload.Claim)})
	}
	fact := code.OriginProposal.CodeFact
	cut, err := NewAdmittedCut(specs, []ImplementationInput{{NodeID: code.CanonicalID, NodeKind: code.NodeKind,
		RepositoryID: fact.RepoID, SourceTitle: code.Payload.Title, SourceLocation: code.Payload.Source,
		Revision: fact.CommitSHA, Path: fact.Path, Span: fmt.Sprintf("bytes:%d-%d", fact.StartByte, fact.EndByte),
		SymbolKind: fact.SymbolKind, QualifiedName: fact.QualifiedName, ExactExcerpt: fact.QuotedText, ExcerptHash: fact.QuotedTextHash}})
	if err != nil {
		return DerivedReview{}, err
	}
	basis := DerivedReviewBasis{AdmittedCutID: cut.ID, RootNodeID: root.CanonicalID, Ancestors: native.Ancestors,
		RuleStatement: input.RuleStatement, SourceLeaves: make([]ReviewEvidenceSnapshot, 0, len(native.SourceLeaves))}
	for _, leaf := range native.SourceLeaves {
		evidence, err := NewReviewEvidenceSnapshot(cut, ReviewEvidenceInput{AdmittedCutID: cut.ID, SpecificationNodeID: leaf.Node.CanonicalID,
			ReviewSnapshot: leaf.ReviewSnapshot, RawText: leaf.RawText, RenderedText: leaf.RenderedText})
		if err != nil {
			return DerivedReview{}, err
		}
		basis.SourceLeaves = append(basis.SourceLeaves, evidence)
	}
	basis, err = NewDerivedReviewBasis(cut, basis)
	if err != nil {
		return DerivedReview{}, err
	}
	report, err := ProposeCandidates(cut)
	if err != nil {
		return DerivedReview{}, err
	}
	for _, candidate := range report.Candidates {
		if candidate.Specification.NodeID != root.CanonicalID || candidate.Implementation.NodeID != code.CanonicalID {
			continue
		}
		input.Mapping, err = NormalizeReviewMapping(candidate, input.Mapping)
		if err != nil {
			return DerivedReview{}, err
		}
		display, subject, err := BuildDerivedReviewDisplay(cut, report, candidate.ID, basis, input.Mapping)
		if err != nil {
			return DerivedReview{}, err
		}
		return DerivedReview{Request: input, Cut: cut, Report: report, Basis: basis, Display: display, Subject: subject,
			OriginProposalOccurrenceID: root.OriginProposalOccurrenceID}, nil
	}
	return DerivedReview{}, fmt.Errorf("%w: requested derived pair was not proposed by the bounded candidate policy", ErrEndpointMissing)
}

func validateDerivedReviewRequest(input DerivedReviewRequest) error {
	for _, id := range []string{input.SpecificationNodeID, input.ImplementationNodeID} {
		if !strings.HasPrefix(id, "canon-node:") || len(id) > MaxNodeIDBytes || strings.TrimSpace(id) != id || !utf8.ValidString(id) || strings.ContainsRune(id, '\x00') {
			return fmt.Errorf("%w: derived review endpoint is not a bounded canonical node ID", ErrInvalid)
		}
	}
	if input.SpecificationNodeID == input.ImplementationNodeID || strings.TrimSpace(input.RuleStatement) == "" ||
		strings.TrimSpace(input.RuleStatement) != input.RuleStatement || len(input.RuleStatement) > MaxCoverageBytes || !utf8.ValidString(input.RuleStatement) || strings.ContainsRune(input.RuleStatement, '\x00') ||
		len(input.Mapping.ProposalSentence) > MaxProposalBytes || len(input.Mapping.Coverage) > MaxCoverageBytes ||
		len(input.Mapping.Witnesses) > MaxWitnesses || len(input.Mapping.Limitations) > MaxLimitations {
		return fmt.Errorf("%w: derived review assertion exceeds its bounded contract", ErrInvalid)
	}
	// Mapping normalization only needs endpoint IDs. Apply it before native SQL
	// to reject oversized nested witnesses, malformed hashes and NUL-bearing text.
	_, err := NormalizeReviewMapping(Candidate{Specification: Specification{NodeID: input.SpecificationNodeID},
		Implementation: Implementation{NodeID: input.ImplementationNodeID}}, input.Mapping)
	return err
}
