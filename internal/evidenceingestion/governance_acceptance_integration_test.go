//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: only synthetic materials in the disposable integration DB.
func governanceManualRecord(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, recordID string) InterimGovernanceRecord {
	t.Helper()
	c, err := GetCanonicalEvidenceByID(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	var store string
	if err := pool.QueryRow(ctx, `SELECT store_id FROM evidence_governance_head`).Scan(&store); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(c.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return InterimGovernanceRecord{Version: InterimGovernanceV1, RecordID: recordID, Revision: 1, StoreID: store,
		Kind: "exclusion", CanonicalID: id, ClaimHash: governanceDigest("sha256:", payload), OccurrenceID: c.OriginProposalOccurrenceID,
		Purpose: "current-action", Scope: "claim", Reason: "TEST APPROVAL STUB: historical human exclusion", Outcome: "exclude",
		HistoricalReviewer: "TEST APPROVAL STUB: historical reviewer", HistoricalReviewedAt: time.Now().UTC(),
		OriginalMaterials: []evidenceuse.Material{{Identity: c.OriginProposal.SourceSnapshotID, Revision: c.OriginProposal.SourceVersion, ContentHash: c.OriginProposal.RawContentHash}},
		Limitations:       []string{"synthetic human decision; no checker was used"}, Native: GovernanceRequest{Kind: "withdrawal", TargetID: id, Operation: "withdraw"}}
}

func governanceBatchTestDecision(item GovernanceBatchManifestItem) GovernanceBatchDecision {
	return GovernanceBatchDecision{RecordID: item.Record.RecordID, Decision: GovernanceDecision{
		RequestID: item.NativeRequestID, Review: *item.NativeRequest, ExpectedSubject: item.NativeSubject,
		Decision: "approved", Reason: item.Reason}}
}

func TestIntegrationGovernanceManifestHumanMigrationAndMixedOutcomes(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	requests := []GovernanceBatchRequest{}
	for _, name := range []string{"a", "b", "c"} {
		id := propositionTestClaim(t, ctx, pool, "manifest-"+name)
		requests = append(requests, GovernanceBatchRequest{Record: governanceManualRecord(t, ctx, pool, id, "local:"+name), Reason: "TEST APPROVAL STUB: proposed native reason " + name})
	}
	gap := requests[0]
	gap.Record.RecordID = "local:unknown"
	gap.Record.MissingFields = []string{"historical checker identity unavailable"}
	requests = append(requests, gap)
	before, _ := json.Marshal(requests)
	batch, err := PrepareGovernanceBatch(ctx, pool, requests)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(requests)
	if string(before) != string(after) || batch.Manifest.Items[3].Status != "blocked" {
		t.Fatal("preparation rewrote history or treated unknown as not applicable")
	}
	for i := 0; i < 3; i++ {
		entry := batch.Manifest.Items[i]
		if entry.Status != "requires_exact_native_approval" || !reflect.DeepEqual(entry.Record, requests[i].Record) || entry.Reason != requests[i].Reason || entry.Record.Reason == entry.Reason {
			t.Fatal("manual record or new reason omitted from manifest", i)
		}
	}
	approved := []GovernanceBatchDecision{governanceBatchTestDecision(batch.Manifest.Items[0]), governanceBatchTestDecision(batch.Manifest.Items[1]), governanceBatchTestDecision(batch.Manifest.Items[2])}
	approval := GovernanceBatchApproval{Manifest: batch.Manifest, ExpectedManifestSubject: batch.Subject, ApprovedItems: approved}
	// Changed membership, historical material, reason or subject needs a fresh
	// manifest review. Rejected sets must write nothing, even if one item matches.
	for _, change := range []func(*GovernanceBatchManifest){
		func(m *GovernanceBatchManifest) { m.Items = m.Items[:2] },
		func(m *GovernanceBatchManifest) { m.Items[0].Record.OriginalMaterials[0].ContentHash = "changed" },
		func(m *GovernanceBatchManifest) { m.Items[0].Reason = "changed" },
		func(m *GovernanceBatchManifest) { m.Items[0].NativeSubject = "changed" },
	} {
		encoded, _ := json.Marshal(batch.Manifest)
		var modified GovernanceBatchManifest
		if err := json.Unmarshal(encoded, &modified); err != nil {
			t.Fatal(err)
		}
		change(&modified)
		bad := approval
		bad.Manifest = modified
		if _, err := ApplyGovernanceBatch(ctx, pool, bad, "TEST APPROVAL STUB: launcher"); err == nil {
			t.Fatal("changed batch approved")
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_withdrawal_events`).Scan(&count); err != nil || count != 0 {
		t.Fatal("prepare or rejected manifest mutated evidence", count, err)
	}
	// One target changed after preparation. Its original subject is stale, while
	// another item has a changed decision reason and must fail independently.
	governanceTestApply(t, ctx, pool, requests[1].Record.Native, "competing-withdrawal")
	approval.ApprovedItems[2].Decision.Reason = "not the reviewed reason"
	results, err := ApplyGovernanceBatch(ctx, pool, approval, "TEST APPROVAL STUB: launcher")
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "applied" || results[1].Status != "conflict" || results[2].Status != "failed" {
		t.Fatal("missing per-item mixed outcomes", results)
	}
	if results[0].Receipt.BatchSubject != batch.Subject || len(results[0].Readback) != 1 || results[0].Receipt.ReviewerID == requests[0].Record.HistoricalReviewer {
		t.Fatal("batch audit/readback/launcher binding lost")
	}
	// Keep the original reviewed set; retry only the uncertain/committed item.
	approval.ApprovedItems = approval.ApprovedItems[:1]
	replayed, err := ApplyGovernanceBatch(ctx, pool, approval, "TEST APPROVAL STUB: launcher")
	if err != nil || replayed[0].Status != "replayed" || replayed[0].Receipt.EventID != results[0].Receipt.EventID {
		t.Fatal("exact partial retry lost authority", err)
	}
	history, err := ReadGovernanceHistory(ctx, pool, "withdrawal", requests[0].Record.CanonicalID)
	if err != nil || history[0].BatchSubject != batch.Subject {
		t.Fatal("manifest identity not durable", err)
	}
	// The unmodified third native item retains its authority under the original
	// manifest even after other items committed; no whole-batch live CAS is used.
	approval.ApprovedItems = []GovernanceBatchDecision{governanceBatchTestDecision(batch.Manifest.Items[2])}
	last, err := ApplyGovernanceBatch(ctx, pool, approval, "TEST APPROVAL STUB: launcher")
	if err != nil || last[0].Status != "applied" {
		t.Fatal("unaffected item required a new approval", err)
	}
}

func governancePassSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sid, requestID string) GovernanceReceipt {
	t.Helper()
	source, err := loadGovernanceSource(ctx, pgxDB{pool: pool}, sid)
	if err != nil {
		t.Fatal(err)
	}
	r := GovernanceRequest{Kind: "source_assessment", TargetID: sid, Operation: "assess", MaterialIDs: []string{sid},
		Method: "TEST APPROVAL STUB: synthetic pinned comparison", CheckerName: "synthetic checker", CheckerVersion: "v1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"synthetic bytes; no provider-truth claim"}}
	for _, criterion := range []string{evidenceuse.RetentionV1, evidenceuse.CorrespondenceV1, evidenceuse.CoverageV1} {
		r.Checks = append(r.Checks, evidenceuse.SourceCheck{SourceSnapshotID: sid, ContentHash: source.Snapshot.RawContentHash, Criterion: criterion,
			Scope: evidenceuse.RetainedSnapshotScope, Result: evidenceuse.CheckPass, MaterialRef: sid, ComparedRevision: governanceProviderRevision(source.Snapshot)})
	}
	return governanceTestApply(t, ctx, pool, r, requestID)
}

func TestIntegrationGovernancePurposeBoundToReviewAndCommit(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, parent := prepareIntegrationClosurePair(t, ctx, pool, "purpose-parent")
	proposal := createIntegrationClosureExternalProposal(t, ctx, pool, "purpose-child", "PURPOSE-CHILD", "r1", "Synthetic grounded-purpose conclusion.")
	actions := []evidenceuse.ActionBasis{{ActionID: "act", BasisIDs: []string{parent.CanonicalRef}}}
	ordinary, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.CurrentActionV1, actions)
	if err != nil || !ordinary.Accepted {
		t.Fatal("ordinary policy fixture not current", err)
	}
	grounded, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.GroundedActionV1, actions)
	if err != nil || grounded.Accepted {
		t.Fatal("missing positive checks passed grounded policy", err)
	}
	req := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: proposal.ProposalOccurrenceID, Derivation: &EndpointDerivation{UsePolicy: evidenceuse.GroundedActionV1,
		ParentNodeIDs: []string{parent.CanonicalRef}, Method: "synthetic AND", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:purpose"}}
	review, err := LoadEndpointReview(ctx, pool, req)
	if err != nil || review.Display.UseReview.Policy != evidenceuse.GroundedActionV1 {
		t.Fatal("review lost policy", err)
	}
	in := ReviewedEndpointAdmissionInput{RequestID: "purpose-admit", Review: req, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
	if _, err := AdmitReviewedEndpoint(ctx, pool, in); err == nil {
		t.Fatal("commit omitted required source checks")
	}
	parentRecord, err := GetCanonicalEvidenceByID(ctx, pool, parent.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	governancePassSource(t, ctx, pool, parentRecord.OriginProposal.SourceSnapshotID, "purpose-parent-pass")
	governancePassSource(t, ctx, pool, proposal.SourceSnapshotID, "purpose-own-pass")
	review, err = LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSubject = review.Subject
	changed := in
	copyDerivation := *req.Derivation
	copyDerivation.UsePolicy = evidenceuse.CurrentActionV1
	changed.Review.Derivation = &copyDerivation
	if _, err := AdmitReviewedEndpoint(ctx, pool, changed); err == nil {
		t.Fatal("changed purpose reused grounded approval")
	}
	out, err := AdmitReviewedEndpoint(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	actions[0].BasisIDs = []string{out.Admission.CanonicalRef}
	if got, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.GroundedActionV1, actions); err != nil || !got.Accepted {
		t.Fatal("fully checked grounded basis refused", err)
	}
	missing := req
	d := *req.Derivation
	d.UsePolicy = ""
	missing.Derivation = &d
	newProposal := createIntegrationClosureExternalProposal(t, ctx, pool, "purpose-missing", "PURPOSE-MISSING", "r1", "Synthetic missing purpose.")
	missing.ProposalOccurrenceID = newProposal.ProposalOccurrenceID
	if _, err := LoadEndpointReview(ctx, pool, missing); err == nil {
		t.Fatal("new review silently defaulted an absent purpose")
	}
}
