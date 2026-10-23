//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This unchanged historical materializer supplies legacy D/E fixtures only.
// It grants no current use and adds no bypass to either current reviewed route.
func registrationHistoricalDerived(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string, parents ...string) AdmissionResult {
	t.Helper()
	occ := createIntegrationClosureExternalProposal(t, ctx, pool, label, "OBJECT-"+label, "r1", "Synthetic historical derivation.").ProposalOccurrenceID
	r, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: occ, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "historical synthetic AND fixture", Derivation: &DerivationAdmissionInput{ParentNodeIDs: parents, Method: "historical AND", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:registration-history"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func registrationEligibility(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, wantEligible bool) EvidenceUseProjection {
	t.Helper()
	p, err := ReadEvidenceUseProjectionForPolicy(ctx, pool, evidenceuse.CurrentActionV1, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Assessment.Nodes {
		if n.NodeID == id {
			if (n.Outcome == evidenceuse.Eligible) != wantEligible {
				t.Fatalf("node %s outcome=%s reasons=%+v eligible wanted=%v", id, n.Outcome, n.Reasons, wantEligible)
			}
			return p
		}
	}
	t.Fatal("assessment omitted requested node")
	return p
}

func TestIntegrationInitialRegistrationHistoricalANDAndOwnSource(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, b := prepareIntegrationClosurePair(t, ctx, pool, "and-b")
	in := registrationNativeFixture(t, ctx, pool, "and-c", "REGISTRATION-C")
	d := registrationHistoricalDerived(t, ctx, pool, "historical-d", in.Review.ClaimID, b.CanonicalRef)
	e := registrationHistoricalDerived(t, ctx, pool, "historical-e", d.CanonicalRef)
	snapshot := func() string {
		t.Helper()
		var text string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY canonical_node_id) FROM canonical_graph_nodes n WHERE canonical_node_id=ANY($1::text[])),'admissions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY admission_decision_id) FROM admission_decisions a WHERE canonical_ref=ANY($1::text[])),'derivations',(SELECT jsonb_agg(to_jsonb(d) ORDER BY node_id) FROM canonical_derivations d WHERE node_id=ANY($1::text[])),'parents',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.parent_node_id) FROM canonical_derivation_parents p JOIN canonical_derivations d USING(derivation_id) WHERE d.node_id=ANY($1::text[])))::text`, []string{d.CanonicalRef, e.CanonicalRef}).Scan(&text); err != nil {
			t.Fatal(err)
		}
		return text
	}
	before := snapshot()
	blocked := registrationEligibility(t, ctx, pool, e.CanonicalRef, false)
	fresh, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSubject = fresh.Subject
	receipt, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	allowed := registrationEligibility(t, ctx, pool, e.CanonicalRef, true)
	if allowed.Assessment.StateDigest == blocked.Assessment.StateDigest || allowed.Snapshot.CutID == blocked.Snapshot.CutID {
		t.Fatal("registration did not invalidate use-state digest/cut")
	}
	if snapshot() != before {
		t.Fatal("registration rewrote D/E admission, parents, temporal or payload")
	}
	query, err := GetCanonicalSupersessionCurrentness(ctx, pool, receipt.Record.Payload.Binding.LineageKey)
	if err != nil || query.Projection != nil || query.JointProjection == nil || query.JointProjection.Witness == nil {
		t.Fatal("existing Query did not expose joint proof", err)
	}
	withdrawal := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: b.CanonicalRef, Operation: "withdraw"}, "and-b-withdraw")
	withdrawn, err := ApplyGovernanceDecision(ctx, pool, withdrawal)
	if err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, e.CanonicalRef, false)
	restore := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: b.CanonicalRef, Operation: "restore", ExpectedRevision: withdrawn.Revision, PreviousEventID: withdrawn.EventID}, "and-b-restore")
	if _, err := ApplyGovernanceDecision(ctx, pool, restore); err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, e.CanonicalRef, true)
	node, err := GetCanonicalEvidenceByID(ctx, pool, d.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	sid := node.OriginProposal.SourceSnapshotID
	finding := GovernanceRequest{Kind: "source_assessment", TargetID: sid, Operation: "assess", Method: "TEST APPROVAL STUB", CheckerName: "synthetic", CheckerVersion: "v1", CheckedAt: "2026-10-23T00:00:00Z", Limitations: []string{"synthetic governance gate; no semantic fidelity claim"}, MaterialIDs: []string{sid}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: sid, Scope: evidenceuse.RetainedSnapshotScope, ContentHash: node.OriginProposal.RawContentHash, Criterion: evidenceuse.RetentionV1, Result: evidenceuse.CheckFail, MaterialRef: sid}}}
	failed, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, finding, "and-own-source-fail"))
	if err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, e.CanonicalRef, false)
	finding.Checks[0].Result = evidenceuse.CheckPass
	finding.ResolvesEventIDs = []string{failed.EventID}
	finding.ExpectedRevision = failed.Revision
	finding.PreviousEventID = failed.EventID
	if _, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, finding, "and-own-source-resolution")); err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, e.CanonicalRef, true)
	newer := createIntegrationClosureExternalProposal(t, ctx, pool, "and-new-c", "REGISTRATION-C", "r2", "New revision of C.")
	basis := in.Review.Basis
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: newer.ProposalOccurrenceID, Basis: SupersessionLineageBasis{SourceSystem: basis.SourceSystem, SourceNamespace: basis.SourceNamespace, ObjectType: basis.ObjectType, ObjectID: basis.ObjectID, SlotKind: basis.SlotKind, SlotID: basis.SlotID}, TargetNodeIDs: []string{in.Review.ClaimID}, ExpectedRevision: head.Revision, ExpectedHeadEventID: head.HeadEventID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "new C replaces registered C"})
	if err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, e.CanonicalRef, false)
	if snapshot() != before {
		t.Fatal("governance/supersession rewrote historical D/E")
	}
	if r, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil || !r.Replayed {
		t.Fatal("historical registration retry", err)
	}
}

func registrationCurrentEndpointRequest(occ string, parents ...string) EndpointReviewRequest {
	return EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: occ, Derivation: &EndpointDerivation{UsePolicy: evidenceuse.CurrentActionV1, ParentNodeIDs: parents, Method: "synthetic AND", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:registration"}}
}

func TestIntegrationInitialRegistrationDerivedConcurrency(t *testing.T) {
	for _, route := range []string{"endpoint"} {
		t.Run(route, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			in := registrationNativeFixture(t, ctx, pool, "race-c", "RACE-C")
			if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
				t.Fatal(err)
			}
			pending := createIntegrationClosureExternalProposal(t, ctx, pool, "race-d", "RACE-D", "r1", "Concurrent derived candidate.")
			var apply func() error
			{
				r, err := LoadEndpointReview(ctx, pool, registrationCurrentEndpointRequest(pending.ProposalOccurrenceID, in.Review.ClaimID))
				if err != nil {
					t.Fatal(err)
				}
				decision := ReviewedEndpointAdmissionInput{RequestID: "race-derived", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic race"}
				apply = func() error { _, err := AdmitReviewedEndpoint(ctx, pool, decision); return err }
			}
			withdrawal := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: in.Review.ClaimID, Operation: "withdraw"}, "race-withdraw")
			review, err := LoadGovernanceReview(ctx, pool, withdrawal.Review)
			if err != nil {
				t.Fatal(err)
			}
			display, err := json.Marshal(review.Display)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(governanceEventBody{Contract: GovernanceEventV1, RequestID: withdrawal.RequestID, Subject: withdrawal.ExpectedSubject, Display: string(display), Decision: withdrawal.Decision, Reason: withdrawal.Reason, ReviewerID: withdrawal.ReviewerID})
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { result <- apply() }()
			waitInitialRegistrationLocks(t, ctx, pool, 1)
			_, err = blocker.Exec(ctx, `INSERT INTO canonical_withdrawal_events(event_id,request_id,target_id,revision,previous_event_id,body) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)`, governanceDigest("governance:sha256:", body), withdrawal.RequestID, withdrawal.Review.TargetID, withdrawal.Review.ExpectedRevision+1, withdrawal.Review.PreviousEventID, string(body))
			if err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("derived commit accepted stale pre-withdrawal review")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var outcome string
			if err := pool.QueryRow(ctx, `SELECT admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, pending.ProposalOccurrenceID).Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			if outcome != "pending" {
				t.Fatal("failed current derived commit consumed proposal")
			}
		})
	}
}

func TestIntegrationInitialRegistrationPreservesContradictionGate(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	c := registrationNativeFixture(t, ctx, pool, "conflict-c", "CONFLICT-C")
	peer := registrationNativeFixture(t, ctx, pool, "conflict-peer", "CONFLICT-PEER")
	d := registrationHistoricalDerived(t, ctx, pool, "conflict-d", c.Review.ClaimID)
	// This is an explicit synthetic relation decision; it is not an NLP accuracy test.
	review, err := LoadContradictionReview(ctx, pool, contradictionTestRequest(c.Review.ClaimID, peer.Review.ClaimID, "registration-conflict"))
	if err != nil {
		t.Fatal(err)
	}
	relation, err := ApplyReviewedContradiction(ctx, pool, ReviewedContradictionInput{RequestID: "registration-conflict-decision", Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: synthetic formal conflict", ReviewerID: "TEST APPROVAL STUB"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []InitialLineageRegistrationDecision{c, peer} {
		r, err := LoadInitialLineageRegistrationReview(ctx, pool, input.Review)
		if err != nil {
			t.Fatal(err)
		}
		input.ExpectedSubject = r.Subject
		if _, err := ApplyInitialLineageRegistration(ctx, pool, input); err != nil {
			t.Fatal(err)
		}
	}
	assessed := registrationEligibility(t, ctx, pool, d.CanonicalRef, false)
	if assessed.Snapshot.Nodes[c.Review.ClaimID].Currentness.Status != "current" || assessed.Snapshot.Nodes[peer.Review.ClaimID].Currentness.Status != "current" {
		t.Fatal("test did not isolate contradiction from currentness")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_graph_edges WHERE canonical_edge_id=$1 AND relation='contradicts'`, relation.Decision.CanonicalEdgeID).Scan(&count); err != nil || count != 1 {
		t.Fatal("registration erased contradiction authority", err)
	}
}
