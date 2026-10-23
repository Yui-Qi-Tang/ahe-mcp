//go:build integration && registration_upgrade

package evidenceingestion

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A schema-59 binary, not the new writer, must create this retained fixture.
// The explicit fixture is private, synthetic, and never a production DSN.
const registrationUpgradeSnapshotSQL = `SELECT jsonb_build_object(
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY revision) FROM canonical_supersession_admission_events e),
 'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY event_id) FROM supersession_review_bindings r),
 'endpoint_receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY admission_decision_id) FROM canonical_endpoint_review_bindings r),
 'members',(SELECT jsonb_agg(jsonb_build_object('node',canonical_node_id,'lineage',lineage_key,'first_event',first_admission_event_id,'bootstrap',was_bootstrapped,'created_at',created_at) ORDER BY canonical_node_id) FROM canonical_supersession_members),
 'head',(SELECT to_jsonb(h) FROM canonical_supersession_admission_head h),
 'nodes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY canonical_node_id) FROM canonical_graph_nodes n),
 'admissions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY admission_decision_id) FROM admission_decisions a),
 'derivations',(SELECT jsonb_agg(to_jsonb(d) ORDER BY derivation_id) FROM canonical_derivations d),
 'parents',(SELECT jsonb_agg(to_jsonb(p) ORDER BY derivation_id,parent_node_id) FROM canonical_derivation_parents p))::text`

func TestIntegrationInitialRegistrationSchema59Upgrade(t *testing.T) {
	path := os.Getenv("AHE_REGISTRATION_UPGRADE_FIXTURE")
	if path == "" {
		t.Fatal("explicit retained schema-59 fixture required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Base, Schema, Snapshot string
		Inputs                 []ReviewedSupersessionInput
		Receipts               []SupersessionReviewReceipt
		DerivedInput           AdmissionInput
		DerivedReceipt         AdmissionResult
		ReviewedDerivedInput   ReviewedEndpointAdmissionInput
		ReviewedDerivedReceipt EndpointAdmissionReceipt
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Base != "9d301f809448a8968c6d29550c73dd0f1173d891" || f.Schema != "issue24_upgrade" || len(f.Inputs) != 3 || len(f.Receipts) != 3 || f.ReviewedDerivedInput.RequestID == "" {
		t.Fatal("wrong frozen baseline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_DSN"))
	if err != nil {
		t.Fatal("invalid fixture configuration")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = f.Schema + ",pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open fixture failed")
	}
	defer pool.Close()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 59 {
		t.Fatal("fixture is not schema 59", count, err)
	}
	if changed, err := migrations.ApplyUp(ctx, pool); err != nil || !changed {
		t.Fatal("59 to 60", changed, err)
	}
	if status, err := migrations.VerifyCurrent(ctx, pool); err != nil || status.AppliedMigrations != 60 {
		t.Fatal("upgraded readiness", status, err)
	}
	var snapshot string
	if err := pool.QueryRow(ctx, registrationUpgradeSnapshotSQL).Scan(&snapshot); err != nil || snapshot != f.Snapshot {
		t.Fatal("upgrade changed old authority or derived history", err)
	}
	replay := func() {
		t.Helper()
		for i, in := range f.Inputs {
			in.ReviewerID = f.Receipts[i].ReviewerID
			r, err := AdmitReviewedSupersession(ctx, pool, in)
			if err != nil || !r.Admission.Replayed {
				t.Fatal("old v2 exact replay", i, err)
			}
			want := f.Receipts[i]
			want.Admission.Replayed = true
			if !reflect.DeepEqual(r, want) {
				t.Fatal("old receipt changed", i, r, want)
			}
		}
		d, err := AdmitPendingProposal(ctx, pool, f.DerivedInput)
		want := f.DerivedReceipt
		want.Replayed = true
		// Compare with the old binary's terminal retry, including its exact
		// public null/empty-slice representation.
		gotJSON, gotErr := json.Marshal(d)
		wantJSON, wantErr := json.Marshal(want)
		if err != nil || gotErr != nil || wantErr != nil || string(gotJSON) != string(wantJSON) {
			t.Fatal("schema-59 derived exact retry changed", d, want, err)
		}
		input := f.ReviewedDerivedInput
		input.ReviewerID = f.ReviewedDerivedReceipt.ReviewerID
		endpoint, err := AdmitReviewedEndpoint(ctx, pool, input)
		expected := f.ReviewedDerivedReceipt
		expected.Admission.Replayed = true
		if err != nil || !reflect.DeepEqual(endpoint, expected) {
			t.Fatal("schema-59 reviewed derived exact retry changed", err)
		}
	}
	replay()
	legacy, err := GetCanonicalSupersessionCurrentness(ctx, pool, f.Receipts[2].Admission.LineageKey)
	if err != nil || legacy.Projection == nil || legacy.JointProjection != nil {
		t.Fatal("empty catalog changed legacy query shape", err)
	}
	in := registrationNativeFixture(t, ctx, pool, "upgrade-registration", "UPGRADE-OTHER")
	r, err := ApplyInitialLineageRegistration(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Record.Payload.Binding.Anchor.Revision != 3 || r.Record.Payload.Binding.Anchor.HeadEventID == nil || *r.Record.Payload.Binding.Anchor.HeadEventID != f.Receipts[2].Admission.AdmissionEventID {
		t.Fatal("nonzero anchor differs")
	}
	p := createIntegrationClosureExternalProposal(t, ctx, pool, "upgrade-new", "UPGRADE-OTHER", "r2", "Synthetic newer revision.")
	b := in.Review.Basis
	basis := SupersessionLineageBasis{SourceSystem: b.SourceSystem, SourceNamespace: b.SourceNamespace, ObjectType: b.ObjectType, ObjectID: b.ObjectID, SlotKind: b.SlotKind, SlotID: b.SlotID}
	review, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: p.ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{in.Review.ClaimID}})
	if err != nil {
		t.Fatal(err)
	}
	next, err := AdmitReviewedSupersession(ctx, pool, ReviewedSupersessionInput{RequestID: "upgrade-new", Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic genuine replacement"})
	if err != nil || next.Admission.EventRevision != 4 || len(next.Admission.BootstrappedTargetNodeIDs) != 0 {
		t.Fatal("new v2 did not extend old prefix", next, err)
	}
	replay()
	joint, err := GetCanonicalSupersessionCurrentness(ctx, pool, f.Receipts[2].Admission.LineageKey)
	if err != nil || joint.Projection != nil || joint.JointProjection == nil || joint.JointProjection.Witness == nil || len(joint.JointProjection.FrontierNodeIDs) != 1 || joint.JointProjection.FrontierNodeIDs[0] != f.Receipts[2].Admission.CanonicalRef {
		t.Fatal("old lineage joint projection", err)
	}
	withdrawal := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: f.Receipts[2].Admission.CanonicalRef, Operation: "withdraw"}, "upgrade-parent-withdrawal")
	if _, err := ApplyGovernanceDecision(ctx, pool, withdrawal); err != nil {
		t.Fatal(err)
	}
	registrationEligibility(t, ctx, pool, f.DerivedReceipt.CanonicalRef, false)
	registrationEligibility(t, ctx, pool, f.ReviewedDerivedReceipt.Admission.CanonicalRef, false)
	replay()
	t.Log("schema 59->60: unchanged old events/receipts/head/members/derived history; all 3 old receipts replay before and after registration and revision 4")
}
