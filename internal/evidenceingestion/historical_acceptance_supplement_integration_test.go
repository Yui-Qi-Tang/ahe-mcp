//go:build integration

package evidenceingestion

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

// TEST APPROVAL STUB: supplements the original bounded H03/H06/H09 evidence.
func TestIntegrationHistoricalFailedCoverageRemainsReviewContext(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parent := historicalSources(t, ctx, pool, 1, false)[0]
	n, err := GetCanonicalEvidenceByID(ctx, pool, parent)
	if err != nil {
		t.Fatal(err)
	}
	sid := n.OriginProposal.SourceSnapshotID
	finding := GovernanceRequest{Kind: "source_assessment", TargetID: sid, Operation: "assess", MaterialIDs: []string{sid}, Method: "synthetic coverage finding", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"coverage disputed; retained source binding remains intact"}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: sid, ContentHash: n.OriginProposal.RawContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: evidenceuse.CoverageV1, Result: evidenceuse.CheckFail, ComparedRevision: "r1", MaterialRef: sid}}}
	if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, finding, "historical-failed-coverage")); err != nil {
		t.Fatal(err)
	}
	req := historicalProposal(t, ctx, pool, "failed-coverage-context", parent)
	r, err := LoadHistoricalDerivationReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.Display.Basis.UseReview.Sources[sid].Checks, func(c evidenceuse.SourceCheck) bool {
		return c.Criterion == evidenceuse.CoverageV1 && c.Result == evidenceuse.CheckFail
	}) {
		t.Fatal("failed finding was hidden or relabeled")
	}
	result, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "failed-coverage-admission"))
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalBlocked(t, ctx, pool, result.Admission.CanonicalRef)
}

func TestIntegrationHistoricalRestoreAndOrdinaryWrapperAncestors(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parent := historicalSources(t, ctx, pool, 1, true)[0]
	req := historicalProposal(t, ctx, pool, "restore-history", parent)
	h, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "restore-history-admission"))
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parent, Operation: "withdraw"}, "restore-history-withdraw"))
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalBlocked(t, ctx, pool, h.Admission.CanonicalRef)
	if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parent, Operation: "restore", ExpectedRevision: withdrawn.Revision, PreviousEventID: withdrawn.EventID}, "restore-history-restore")); err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, parent, true)
	assertHistoricalBlocked(t, ctx, pool, h.Admission.CanonicalRef)

	// Legacy internal materialization is fixture setup only. It grants no
	// current approval and remains unavailable in the standard public profiles.
	wrapper := h.Admission.CanonicalRef
	for depth := range 2 {
		q := historicalProposal(t, ctx, pool, fmt.Sprintf("ordinary-wrapper-%d", depth), wrapper)
		d := q.Derivation
		result, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: q.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "legacy fixture; no current approval", Derivation: &DerivationAdmissionInput{ParentNodeIDs: d.ParentNodeIDs, Method: d.Method, Producer: d.Producer, TraceRef: d.TraceRef}})
		if err != nil {
			t.Fatal(err)
		}
		wrapper = result.CanonicalRef
		state := registrationEligibility(t, ctx, pool, wrapper, false)
		if state.Snapshot.Nodes[wrapper].UseClass != evidencegraph.DerivationOrdinary {
			t.Fatal("wrapper is not an ordinary positive-shape control")
		}
		assertHistoricalBlocked(t, ctx, pool, wrapper)
	}
	q := historicalProposal(t, ctx, pool, "wrapped-current-endpoint", wrapper)
	request := registrationCurrentEndpointRequest(q.ProposalOccurrenceID, wrapper)
	r, err := LoadEndpointReview(ctx, pool, request)
	if err != nil {
		t.Fatal("complete wrapper review", err)
	}
	if _, err := AdmitReviewedEndpoint(ctx, pool, ReviewedEndpointAdmissionInput{RequestID: "wrapped-endpoint", Review: request, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}); err == nil {
		t.Fatal("ordinary wrappers hid historical ancestry")
	}
}

func TestIntegrationHistoricalReplayRejectsChangedParentProducerAndTrace(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 3, false)
	req := historicalProposal(t, ctx, pool, "replay-fields", parents...)
	in := historicalDecision(t, ctx, pool, req, "replay-fields-admission")
	if _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	before := historicalCounts(t, ctx, pool)
	for _, change := range []struct {
		name   string
		mutate func(*HistoricalDerivationDecision)
	}{
		{"parents", func(x *HistoricalDerivationDecision) {
			x.Review.Derivation.ParentNodeIDs = slices.Clone(x.Review.Derivation.ParentNodeIDs[:2])
		}},
		{"producer", func(x *HistoricalDerivationDecision) { x.Review.Derivation.Producer = "different producer" }},
		{"trace", func(x *HistoricalDerivationDecision) { x.Review.Derivation.TraceRef = "synthetic:different-trace" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			bad := in
			change.mutate(&bad)
			if _, err := ApplyHistoricalDerivationDecision(ctx, pool, bad); err == nil {
				t.Fatal("changed historical replay accepted")
			}
			if !reflect.DeepEqual(before, historicalCounts(t, ctx, pool)) {
				t.Fatal("rejected replay changed canonical data")
			}
		})
	}
}
