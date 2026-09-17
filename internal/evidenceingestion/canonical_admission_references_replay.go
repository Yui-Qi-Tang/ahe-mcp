package evidenceingestion

import (
	"context"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

// This is intentionally only bounded receipt/edge integrity, not endpoint
// reconstruction or today's resolver candidate cut. Re-entering node admission
// here would make a valid A->B->A references cycle recurse into its own proof.
func validateIndependentReferencesOriginEdge(ctx context.Context, db sqlQueryer, edge CanonicalGraphEdge, proposalID, canonicalRef string) error {
	if edge.Relation != evidencegraph.CanonicalReferences || edge.From != canonicalRef || edge.OriginProposalOccurrenceID != proposalID {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "references edge differs from its original source carrier")
	}
	if _, err := loadCanonicalReferencesAdmission(ctx, db, edge); err != nil {
		return err
	}
	origin, matches, err := canonicalGraphEdgeRowMatches(ctx, db, edge)
	if err != nil {
		return err
	}
	if !matches || origin != proposalID {
		return newDomainError(ErrorCanonicalAdmissionInvariant, "references edge body differs from its independent receipt authority")
	}
	return nil
}

func supersessionAdmissionAttributedEdges(ctx context.Context, db sqlQueryer, event persistedSupersessionEvent, actual, materialized []string) ([]string, error) {
	if slices.Equal(actual, materialized) {
		return actual, nil
	}
	result := make([]string, 0, len(materialized))
	for _, edgeID := range actual {
		if _, found := slices.BinarySearch(materialized, edgeID); found {
			result = append(result, edgeID)
			continue
		}
		edge, err := loadCanonicalEdge(ctx, db, edgeID)
		if err != nil || validateIndependentReferencesOriginEdge(ctx, db, edge, event.ProposalOccurrence, event.ReplacementNodeID) != nil {
			return nil, newDomainError(ErrorSupersessionReplayConflict, "proposal %s extra edge lacks exact independent references authority", event.ProposalOccurrence)
		}
	}
	return result, nil
}
