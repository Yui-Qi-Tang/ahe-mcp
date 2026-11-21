//go:build integration

package evidenceingestion

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
)

func TestIntegrationReviewConflictsSourceCutAndReplay(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	first := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "conflict-review", "v1")
	reader := externalRecordRolePool(t, ctx, pool, dbrole.ProfileSourceClaimReviewer)
	before := reviewableIngestionRowCounts(t, ctx, pool)
	empty, err := LoadSourceClaimReviewWithConflicts(ctx, reader, first.ExtractionAttemptID, first.ProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if empty.ConflictCandidates.Status != "observed" || len(empty.ConflictCandidates.Targets) != 1 || len(empty.ConflictCandidates.Targets[0].Suggestions) != 0 || empty.ConflictCandidates.DatabaseSnapshot == "" {
		t.Fatal("empty bounded observation missing", empty.ConflictCandidates)
	}
	if !reflect.DeepEqual(before, reviewableIngestionRowCounts(t, ctx, pool)) {
		t.Fatal("review getter wrote data")
	}
	originalDisplay, originalSubject, err := BuildSourceClaimReviewDisplayArtifact(empty)
	if err != nil {
		t.Fatal(err)
	}
	// Establish the review cut first; another connection then admits a peer.
	// The candidate reader must not see that newer commit in this RR transaction.
	var concurrent AdmissionResult
	err = withReadOnlyTx(ctx, pgxDB{pool: reader}, func(tx sqlTx) error {
		if _, err := loadReviewableSourceClaimReviewSnapshotInTx(ctx, tx, first.ExtractionAttemptID, first.ProposalOccurrenceID); err != nil {
			return err
		}
		peer := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "conflict-peer", "v1")
		var err error
		concurrent, err = AdmitReviewedSourceClaim(ctx, pool, reviewedSourceClaimIntegrationInput(t, ctx, pool, peer.ExtractionAttemptID, peer.ProposalOccurrenceID))
		if err != nil {
			return err
		}
		cut, err := loadReviewConflictCandidates(ctx, tx, []string{first.ProposalOccurrenceID})
		if err != nil {
			return err
		}
		if cut.Targets[0].Coverage.ScannedClaims != 0 || cut.GovernanceRevision != empty.ConflictCandidates.GovernanceRevision {
			t.Fatal("candidate lookup escaped review transaction", cut)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := LoadSourceClaimReviewWithConflicts(ctx, reader, first.ExtractionAttemptID, first.ProposalOccurrenceID)
	if err != nil || len(fresh.ConflictCandidates.Targets[0].Suggestions) == 0 {
		t.Fatal("new read did not see peer", err)
	}
	found := false
	for _, candidate := range fresh.ConflictCandidates.Targets[0].Suggestions {
		found = found || candidate.CanonicalID == concurrent.CanonicalRef
	}
	display, subject, err := BuildSourceClaimReviewDisplayArtifact(fresh)
	if err != nil || !found || display != originalDisplay || subject != originalSubject {
		t.Fatal("advisory changed exact approval or peer missing", err)
	}
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, first.ExtractionAttemptID, first.ProposalOccurrenceID)
	input.ExpectedSubject = originalSubject
	admitted, err := AdmitReviewedSourceClaim(ctx, pool, input)
	if err != nil {
		t.Fatal("unchanged exact approval rejected due to advisory", err)
	}
	replay, err := AdmitReviewedSourceClaim(ctx, pool, input)
	if err != nil || !replay.Replayed || replay.AdmissionDecisionID != admitted.AdmissionDecisionID || replay.CanonicalRef != admitted.CanonicalRef {
		t.Fatal("source receipt replay changed", err)
	}
	pending := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "conflict-rls", "v1")
	if _, err := pool.Exec(ctx, `ALTER TABLE canonical_graph_nodes ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSourceClaimReviewWithConflicts(ctx, reader, pending.ExtractionAttemptID, pending.ProposalOccurrenceID); err == nil {
		t.Fatal("candidate visibility failure became a successful empty review")
	}
}

func TestIntegrationReviewConflictsEndpointHistoryAndAncestry(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 2, true)
	request := historicalProposal(t, ctx, pool, "ordinary-advisory", parents...)
	ordinary, err := historicalEndpointRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	endpointReader := externalRecordRolePool(t, ctx, pool, dbrole.ProfileEndpointReviewer)
	review, err := LoadEndpointReview(ctx, endpointReader, ordinary)
	if err != nil || len(review.ConflictCandidates.Targets) != 3 {
		t.Fatal("ordinary statement/parent targets", err)
	}
	var bare EndpointReview
	err = withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error { var e error; bare, e = loadEndpointReview(ctx, tx, ordinary); return e })
	if err != nil || review.Subject != bare.Subject || !reflect.DeepEqual(review.Display, bare.Display) || bare.ConflictCandidates != nil {
		t.Fatal("advisory entered writer display", err)
	}
	input := ReviewedEndpointAdmissionInput{RequestID: "ordinary-advisory", Review: ordinary, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
	a, err := AdmitReviewedEndpoint(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	after, err := LoadEndpointReview(ctx, endpointReader, ordinary)
	if err != nil || after.Subject != review.Subject || !reflect.DeepEqual(after.Display, review.Display) || after.Lifecycle.Mode != "exact_replay_only" {
		t.Fatal("historical endpoint display changed", err)
	}
	ar, err := AdmitReviewedEndpoint(ctx, pool, input)
	if err != nil || !ar.Admission.Replayed || ar.Admission.AdmissionDecisionID != a.Admission.AdmissionDecisionID {
		t.Fatal("ordinary replay", err)
	}
	h1 := historicalProposal(t, ctx, pool, "historical-advisory-parent", parents...)
	h1Receipt, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, h1, "historical-advisory-parent"))
	if err != nil {
		t.Fatal(err)
	}
	h2 := historicalProposal(t, ctx, pool, "historical-advisory-nested", h1Receipt.Admission.CanonicalRef)
	historicalReader := externalRecordRolePool(t, ctx, pool, dbrole.ProfileHistoricalDerivationReviewer)
	hreview, err := LoadHistoricalDerivationReview(ctx, historicalReader, h2)
	if err != nil || len(hreview.ConflictCandidates.Targets) != 4 {
		t.Fatal("transitive historical targets missing", err)
	}
	hinput := historicalDecision(t, ctx, pool, h2, "historical-advisory-nested")
	hreceipt, err := ApplyHistoricalDerivationDecision(ctx, pool, hinput)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(hreceipt)
	if bytes.Contains(saved, []byte(`"conflict_candidates"`)) {
		t.Fatal("live advisory persisted in historical receipt")
	}
	hafter, err := LoadHistoricalDerivationReview(ctx, historicalReader, h2)
	if err != nil || hafter.Subject != hreview.Subject || !reflect.DeepEqual(hafter.Display, hreview.Display) || hafter.ConflictCandidates == nil {
		t.Fatal("historical getter lost saved display or live advisory", err)
	}
	hreplay, err := ApplyHistoricalDerivationDecision(ctx, pool, hinput)
	if err != nil || !hreplay.Replayed {
		t.Fatal("historical replay", err)
	}
	hreplay.Replayed = false
	replayed, _ := json.Marshal(hreplay)
	if string(saved) != string(replayed) {
		t.Fatal("historical receipt bytes changed")
	}
}
