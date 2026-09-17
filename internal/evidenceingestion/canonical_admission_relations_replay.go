package evidenceingestion

import (
	"context"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

// A separately reviewed relation retains its endpoint's original proposal as a
// source carrier without becoming part of that proposal's old mutation. Only
// exact independently bound authority may cross this boundary; a relation label
// alone never permits an extra edge. Receipt checks here do not reload endpoints:
// independently reviewed references may form cycles.
func ordinaryAdmissionAttributedEdges(ctx context.Context, db sqlQueryer, proposalID string, expected canonicalAdmissionMutation, actual, materialized []string) ([]string, error) {
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
		if err != nil {
			return nil, ordinaryAdmissionReplayConflict(proposalID, "extra relation edge cannot be loaded")
		}
		if edge.Relation == evidencegraph.CanonicalReferences {
			if expected.derivation != nil || !slices.ContainsFunc(expected.nodes, func(node CanonicalGraphNode) bool {
				return node.ID == expected.result.CanonicalRef && node.Kind == evidencegraph.CanonicalSourceClaim
			}) {
				return nil, ordinaryAdmissionReplayConflict(proposalID, "extra references edge requires an original source claim")
			}
			if err := validateIndependentReferencesOriginEdge(ctx, db, edge, proposalID, expected.result.CanonicalRef); err != nil {
				return nil, ordinaryAdmissionReplayConflict(proposalID, "extra references edge lacks its exact independent reviewed receipt")
			}
			continue
		}
		if len(expected.nodes) != 1 || expected.nodes[0].Kind != evidencegraph.CanonicalDerivedClaim || expected.derivation == nil {
			return nil, ordinaryAdmissionReplayConflict(proposalID, "unexpected proposal-attributed edge lacks derived relation authority")
		}
		if edge.Relation != evidencegraph.CanonicalImplements || edge.From != expected.result.CanonicalRef || edge.OriginProposalOccurrenceID != proposalID {
			return nil, ordinaryAdmissionReplayConflict(proposalID, "extra edge is not separately reviewed implements navigation from this derived claim")
		}
		if _, err := loadCanonicalImplementsAdmission(ctx, db, edge); err != nil {
			return nil, ordinaryAdmissionReplayConflict(proposalID, "extra implements edge lacks its exact independent reviewed receipt")
		}
		// Compare the complete persisted JSONB body too. Decoding known Go fields
		// alone would silently discard an unknown provenance key.
		origin, matches, err := canonicalGraphEdgeRowMatches(ctx, db, edge)
		if err != nil || !matches || origin != proposalID {
			return nil, ordinaryAdmissionReplayConflict(proposalID, "extra implements edge body differs from its exact authority")
		}
	}
	return result, nil
}
