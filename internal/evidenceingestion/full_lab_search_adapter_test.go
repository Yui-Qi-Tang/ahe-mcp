//go:build integration && labreplay

package evidenceingestion

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This request belongs only to the original Lab probe. The extra filter is rejected
// by that probe before native list calls; it is not advertised as a product filter.
type fullLabSearchInput struct {
	SourceSnapshotID, RepositorySnapshotID, SourceGenerationID, SourceID, SourceVersion, AdmissionOutcome, LifecycleScope string
	Limit                                                                                                                 int
	ExcludeCanonicalCandidates                                                                                            bool
}

func (x fullLabSearchInput) native() ProposalListInput {
	return ProposalListInput{SourceSnapshotID: x.SourceSnapshotID, RepositorySnapshotID: x.RepositorySnapshotID, SourceGenerationID: x.SourceGenerationID, SourceID: x.SourceID, SourceVersion: x.SourceVersion, AdmissionOutcome: x.AdmissionOutcome, LifecycleScope: x.LifecycleScope, Limit: x.Limit}
}
func fullLabNormalizeSearchInput(x fullLabSearchInput) fullLabSearchInput {
	n := normalizeProposalListInput(x.native())
	return fullLabSearchInput{SourceSnapshotID: n.SourceSnapshotID, RepositorySnapshotID: n.RepositorySnapshotID, SourceGenerationID: n.SourceGenerationID, SourceID: n.SourceID, SourceVersion: n.SourceVersion, AdmissionOutcome: n.AdmissionOutcome, LifecycleScope: n.LifecycleScope, Limit: n.Limit, ExcludeCanonicalCandidates: x.ExcludeCanonicalCandidates}
}
func fullLabListSearchRecords(ctx context.Context, pool *pgxpool.Pool, x fullLabSearchInput) ([]ProposalQueryResult, error) {
	return ListProposalRecords(ctx, pool, x.native())
}
