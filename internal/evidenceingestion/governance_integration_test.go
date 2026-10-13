//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: synthetic, disposable database only. No operational review.
func governanceTestDecision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, r GovernanceRequest, id string) GovernanceDecision {
	t.Helper()
	review, err := LoadGovernanceReview(ctx, pool, r)
	if err != nil {
		t.Fatal(err)
	}
	return GovernanceDecision{RequestID: id, Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", Reason: "TEST APPROVAL STUB: synthetic governance finding", ReviewerID: "TEST APPROVAL STUB"}
}
func governanceTestApply(t *testing.T, ctx context.Context, pool *pgxpool.Pool, r GovernanceRequest, id string) GovernanceReceipt {
	t.Helper()
	out, err := ApplyGovernanceDecision(ctx, pool, governanceTestDecision(t, ctx, pool, r, id))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func governanceOutcome(t *testing.T, p EvidenceUseProjection, id string) evidenceuse.Outcome {
	t.Helper()
	for _, n := range p.Assessment.Nodes {
		if n.NodeID == id {
			return n.Outcome
		}
	}
	t.Fatal("missing assessment", id)
	return ""
}
func TestIntegrationGovernanceLifecycleAndSourceReview(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := propositionTestClaim(t, ctx, pool, "governance-original")
	b := propositionTestClaim(t, ctx, pool, "governance-correction")
	child := propositionTestClaim(t, ctx, pool, "governance-child", a)
	original, err := GetCanonicalEvidenceByID(ctx, pool, a)
	if err != nil {
		t.Fatal(err)
	}
	r := GovernanceRequest{Kind: "withdrawal", TargetID: a, Operation: "withdraw", CorrectionIDs: []string{b}}
	in := governanceTestDecision(t, ctx, pool, r, "withdraw-1")
	first, err := ApplyGovernanceDecision(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ApplyGovernanceDecision(ctx, pool, in)
	if err != nil || !replay.Replayed || replay.EventID != first.EventID {
		t.Fatal("exact replay", replay, err)
	}
	changed := in
	changed.Reason = "different"
	if _, err := ApplyGovernanceDecision(ctx, pool, changed); err == nil {
		t.Fatal("changed retry accepted")
	}
	if _, err := LoadGovernanceReview(ctx, pool, r); err == nil {
		t.Fatal("stale revision accepted")
	}
	p, err := ReadEvidenceUseProjection(ctx, pool, []string{child})
	if err != nil {
		t.Fatal(err)
	}
	if governanceOutcome(t, p, a) != evidenceuse.Excluded || governanceOutcome(t, p, child) != evidenceuse.Excluded {
		t.Fatal("AND withdrawal not propagated", p.Assessment)
	}
	current, err := GetCanonicalEvidenceByID(ctx, pool, a)
	if err != nil || !reflect.DeepEqual(current, original) {
		t.Fatal("withdrawal rewrote canonical history", err)
	}
	restored := governanceTestApply(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: a, Operation: "restore", ExpectedRevision: 1, PreviousEventID: first.EventID}, "restore-2")
	sid := original.OriginProposal.SourceSnapshotID
	var source SourceSnapshot
	material, err := loadGovernanceSource(ctx, pgxDB{pool: pool}, sid)
	if err != nil {
		t.Fatal(err)
	}
	source = material.Snapshot
	sr := GovernanceRequest{Kind: "source_assessment", TargetID: sid, Operation: "assess", MaterialIDs: []string{sid}, Method: "synthetic comparison", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "v1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"synthetic fixture; not a source truth claim"}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: sid, ContentHash: source.RawContentHash, Criterion: evidenceuse.RetentionV1, Scope: evidenceuse.RetainedSnapshotScope, Result: evidenceuse.CheckFail, MaterialRef: sid}}}
	failed := governanceTestApply(t, ctx, pool, sr, "source-fail")
	p, err = ReadEvidenceUseProjection(ctx, pool, []string{child})
	if err != nil {
		t.Fatal(err)
	}
	if p.Snapshot.Nodes[a].Withdrawal.EventID != restored.EventID || governanceOutcome(t, p, child) != evidenceuse.Excluded {
		t.Fatal("restore cleared source findings")
	}
	sr.ExpectedRevision = 1
	sr.PreviousEventID = failed.EventID
	sr.Checks[0].Result = evidenceuse.CheckPass
	passed := governanceTestApply(t, ctx, pool, sr, "source-pass-unresolved")
	p, err = ReadEvidenceUseProjection(ctx, pool, []string{child})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Snapshot.Sources[sid].Checks) != 2 {
		t.Fatal("latest finding silently erased previous finding")
	}
	sr.ExpectedRevision = 2
	sr.PreviousEventID = passed.EventID
	sr.ResolvesEventIDs = []string{failed.EventID, passed.EventID}
	governanceTestApply(t, ctx, pool, sr, "source-resolve")
	p, err = ReadEvidenceUseProjection(ctx, pool, []string{child})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Snapshot.Sources[sid].Checks) != 1 || p.Snapshot.Sources[sid].AssessmentRevision != 3 {
		t.Fatal("explicit source resolution not projected")
	}
	// Historical withdrawal receipt still replays after restore and source review.
	if got, err := ApplyGovernanceDecision(ctx, pool, in); err != nil || !got.Replayed || got.EventID != first.EventID {
		t.Fatal("historical receipt failed", err)
	}
	for _, sql := range []string{`UPDATE canonical_withdrawal_events SET body=body`, `DELETE FROM source_assessment_events`, `TRUNCATE canonical_withdrawal_events`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("mutable governance history", sql)
		}
	}
}

func TestIntegrationGovernanceGuardRejectsStaleSnapshotsAndSerializesWriters(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	a := propositionTestClaim(t, ctx, pool, "guard-parent")
	in := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: a, Operation: "withdraw"}, "guard-withdraw")
	stale, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Rollback(ctx)
	var rev int64
	if err := stale.QueryRow(ctx, `SELECT revision FROM evidence_governance_head`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyGovernanceDecision(ctx, pool, in); err != nil {
		t.Fatal(err)
	}
	err = lockGovernance(ctx, pgxTx{tx: stale})
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "40001" {
		t.Fatalf("stale RR admitted: %v", err)
	}
	_ = stale.Rollback(ctx)
	guard, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Rollback(ctx)
	if err := lockGovernance(ctx, pgxTx{tx: guard}); err != nil {
		t.Fatal(err)
	}
	// Every relevant table has a statement trigger, including absent-edge and
	// ordinary source classification writes. Even no-op writes acquire the lock.
	for _, table := range []string{"canonical_graph_edges", "canonical_graph_nodes", "canonical_supersession_members", "canonical_supersession_admission_events", "source_snapshots", "source_assessment_events", "canonical_derivation_parents", "proposal_occurrences"} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='50ms'`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE false`)
		if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
			t.Fatalf("%s bypassed common lock: %v", table, err)
		}
		_ = tx.Rollback(ctx)
	}
}

func TestIntegrationGovernanceCurrentPlanAndRoles(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, current := prepareIntegrationClosurePair(t, ctx, pool, "governance-current")
	id := current.CanonicalRef
	actions := []evidenceuse.ActionBasis{{ActionID: "synthetic-action", BasisIDs: []string{id}}}
	before, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.CurrentActionV1, actions)
	if err != nil || !before.Accepted {
		t.Fatal("current baseline", before, err)
	}
	reviewer := externalRecordRolePool(t, ctx, pool, dbrole.ProfileWithdrawalReviewer)
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := schema + "_withdrawal_reviewer"
	if len(role) > 63 {
		role = role[:63]
	}
	if _, err := pool.Exec(ctx, `GRANT EXECUTE ON FUNCTION evidence_governance_lock_v1(regclass) TO `+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	in := governanceTestDecision(t, ctx, reviewer, GovernanceRequest{Kind: "withdrawal", TargetID: id, Operation: "withdraw"}, "role-withdraw")
	if _, err := ApplyGovernanceDecision(ctx, reviewer, in); err != nil {
		t.Fatal(err)
	}
	after, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.CurrentActionV1, actions)
	if err != nil || after.Accepted {
		t.Fatal("cached/SAT pass bypassed withdrawal", err)
	}
	for _, query := range []string{`UPDATE source_snapshots SET source_version=source_version`, `INSERT INTO source_assessment_events SELECT * FROM source_assessment_events WHERE false`, `UPDATE canonical_graph_nodes SET payload=payload`, `UPDATE evidence_governance_head SET revision=revision`} {
		if _, err := reviewer.Exec(ctx, query); err == nil {
			t.Fatal("reviewer authority expanded", query)
		}
	}
	query := externalRecordRolePool(t, ctx, pool, dbrole.ProfileQuery)
	if _, err := ApplyGovernanceDecision(ctx, query, in); err == nil {
		t.Fatal("query wrote governance")
	}
	if got, err := CheckCurrentEvidencePlan(ctx, query, evidenceuse.CurrentActionV1, actions); err != nil || got.Accepted {
		t.Fatal("query role cannot project governance", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE canonical_withdrawal_events ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckCurrentEvidencePlan(ctx, query, evidenceuse.CurrentActionV1, actions); err == nil {
		t.Fatal("RLS hidden withdrawal accepted")
	}
}

func TestIntegrationGovernanceBatchMigration(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	id := propositionTestClaim(t, ctx, pool, "migration-claim")
	c, err := GetCanonicalEvidenceByID(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c.Payload)
	var store string
	if err := pool.QueryRow(ctx, `SELECT store_id FROM evidence_governance_head`).Scan(&store); err != nil {
		t.Fatal(err)
	}
	material := evidenceuse.Material{Identity: c.OriginProposal.SourceSnapshotID, Revision: "v1", ContentHash: c.OriginProposal.RawContentHash}
	r := InterimGovernanceRecord{Version: InterimGovernanceV1, RecordID: "local:1", Revision: 1, StoreID: store, Kind: "exclusion", CanonicalID: id, ClaimHash: governanceDigest("sha256:", raw), OccurrenceID: c.OriginProposalOccurrenceID, Purpose: "current-action", Scope: "claim", Reason: "TEST APPROVAL STUB", Outcome: "exclude", HistoricalReviewer: "historical-human", HistoricalReviewedAt: time.Now().UTC(), CheckerName: "checker", CheckerVersion: "v1", CheckedAt: time.Now().UTC(), Criterion: "source-support", CriterionVersion: "v1", FreshnessRule: "live revision and content check", OriginalMaterials: []evidenceuse.Material{material}, CheckedMaterials: []evidenceuse.Material{material}, Native: GovernanceRequest{Kind: "withdrawal", TargetID: id, Operation: "withdraw"}}
	gap := r
	gap.RecordID = "local:missing"
	gap.MissingFields = []string{"actual_source_revision"}
	positive := r
	positive.RecordID = "local:verify"
	positive.Kind = "verification"
	positive.Outcome = "pass"
	positive.Verification = &evidenceuse.VerificationReport{Version: evidenceuse.VerificationV1, Subject: evidenceuse.VerificationSubject{StoreID: store, CanonicalID: id, ClaimHash: r.ClaimHash, Criterion: r.Criterion, CriterionVersion: r.CriterionVersion, Scope: r.Scope}, Materials: []evidenceuse.Material{material}, MaterialsComplete: true, Result: evidenceuse.CheckPass, CheckedAt: r.CheckedAt}
	batch, err := PrepareGovernanceBatch(ctx, pool, []GovernanceBatchRequest{{Record: r, Reason: "TEST APPROVAL STUB: exact native approval"}, {Record: gap, Reason: "TEST APPROVAL STUB"}, {Record: positive}})
	if err != nil {
		t.Fatal(err)
	}
	prepared := batch.Reviews
	if prepared[0].Review == nil || prepared[1].Status != "blocked" || prepared[2].Status != "verification_only_no_native_effect" {
		t.Fatal("batch classification", prepared)
	}
	decision := GovernanceDecision{RequestID: prepared[0].NativeRequestID, Review: prepared[0].Review.Display.Request, ExpectedSubject: prepared[0].Review.Subject, Decision: "approved", Reason: "TEST APPROVAL STUB: exact native approval"}
	bad, err := ApplyGovernanceBatch(ctx, pool, GovernanceBatchApproval{Manifest: batch.Manifest, ExpectedManifestSubject: batch.Subject, ApprovedItems: []GovernanceBatchDecision{{RecordID: "local", Decision: decision}}}, "current-launcher-reviewer")
	if err != nil || bad[0].Status != "failed" {
		t.Fatal("ambiguous local ID prefix accepted", bad, err)
	}
	approved := []GovernanceBatchDecision{{RecordID: r.RecordID, Decision: decision}}
	result, err := ApplyGovernanceBatch(ctx, pool, GovernanceBatchApproval{Manifest: batch.Manifest, ExpectedManifestSubject: batch.Subject, ApprovedItems: approved}, "current-launcher-reviewer")
	if err != nil || result[0].Status != "applied" || len(result[0].Readback) != 1 || result[0].Receipt.ReviewerID == r.HistoricalReviewer {
		t.Fatal("batch apply", result, err)
	}
	result, err = ApplyGovernanceBatch(ctx, pool, GovernanceBatchApproval{Manifest: batch.Manifest, ExpectedManifestSubject: batch.Subject, ApprovedItems: approved}, "current-launcher-reviewer")
	if err != nil || result[0].Status != "replayed" {
		t.Fatal("batch exact retry", result, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_withdrawal_events`).Scan(&count); err != nil || count != 1 {
		t.Fatal("unapproved/positive item migrated", count, err)
	}
	if !strings.Contains(result[0].Receipt.Display.Request.MigrationRef, prepared[0].RecordHash) {
		t.Fatal(fmt.Sprint(result[0].Receipt))
	}
}

func TestIntegrationGovernanceDerivedEndpoint(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, current := prepareIntegrationClosurePair(t, ctx, pool, "current-derived")
	pending := createIntegrationClosureExternalProposal(t, ctx, pool, "derived-pending", "DERIVED", "r1", "Synthetic reviewed conclusion.")
	req := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: pending.ProposalOccurrenceID, Derivation: &EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: []string{current.CanonicalRef}, Method: "synthetic inference", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:inference"}}
	review, err := LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	if review.Display.UseReview == nil {
		t.Fatal("native display omits current-action policy")
	}
	in := ReviewedEndpointAdmissionInput{RequestID: "derived-current", Review: req, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
	admitted, err := AdmitReviewedEndpoint(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := governanceTestApply(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: current.CanonicalRef, Operation: "withdraw"}, "derived-parent-withdraw")
	if got, err := AdmitReviewedEndpoint(ctx, pool, in); err != nil || !got.Admission.Replayed {
		t.Fatal("historical derived replay changed", got, err)
	}
	plan, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.CurrentActionV1, []evidenceuse.ActionBasis{{ActionID: "action", BasisIDs: []string{admitted.Admission.CanonicalRef}}})
	if err != nil || plan.Accepted {
		t.Fatal("derived wrapper bypass", err)
	}
	pending2 := createIntegrationClosureExternalProposal(t, ctx, pool, "derived-pending-two", "DERIVED2", "r1", "Second synthetic conclusion.")
	req.ProposalOccurrenceID = pending2.ProposalOccurrenceID
	review, err = LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "new-derived-blocked"
	in.Review = req
	in.ExpectedSubject = review.Subject
	if _, err := AdmitReviewedEndpoint(ctx, pool, in); err == nil {
		t.Fatal("new derived admitted from withdrawn parent")
	}
	// Restore is independently reviewed; it neither undoes supersession nor edits
	// the original derived receipt. Unrelated changes do not invalidate review.
	governanceTestApply(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: current.CanonicalRef, Operation: "restore", ExpectedRevision: 1, PreviousEventID: withdrawal.EventID}, "derived-parent-restore")
	review, err = LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSubject = review.Subject
	propositionTestClaim(t, ctx, pool, "unrelated-write")
	if _, err := AdmitReviewedEndpoint(ctx, pool, in); err != nil {
		t.Fatal("unrelated write invalidated scoped review", err)
	}
}

func TestIntegrationGovernanceConcurrentCASAndReplay(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	id := propositionTestClaim(t, ctx, pool, "concurrent-governance")
	a := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: id, Operation: "withdraw"}, "race-a")
	a.Reason = "TEST APPROVAL STUB: " + strings.Repeat("review reason ", 70)
	b := a
	b.RequestID = "race-b"
	type result struct {
		in      GovernanceDecision
		receipt GovernanceReceipt
		err     error
	}
	run := func(inputs ...GovernanceDecision) []result {
		start := make(chan struct{})
		done := make(chan result, len(inputs))
		for _, in := range inputs {
			go func() {
				<-start
				receipt, err := ApplyGovernanceDecision(ctx, pool, in)
				done <- result{in, receipt, err}
			}()
		}
		close(start)
		out := make([]result, len(inputs))
		for i := range out {
			out[i] = <-done
		}
		return out
	}
	results := run(a, b)
	var winner GovernanceDecision
	successes := 0
	for _, r := range results {
		if r.err == nil {
			successes++
			winner = r.in
		}
	}
	if successes != 1 {
		t.Fatalf("competing CAS successes=%d results=%+v", successes, results)
	}
	for _, r := range run(winner, winner) {
		if r.err != nil || !r.receipt.Replayed {
			t.Fatalf("concurrent exact retry: %+v", r)
		}
	}
	history, err := ReadGovernanceHistory(ctx, pool, "withdrawal", id)
	if err != nil || len(history) != 1 {
		t.Fatal("concurrent retries duplicated history", history, err)
	}
	// A restore and a repeated withdrawal reviewed at the same active revision
	// also compete for exactly one next event, regardless of which wins.
	next := GovernanceRequest{Kind: "withdrawal", TargetID: id, Operation: "withdraw", ExpectedRevision: history[0].Revision, PreviousEventID: history[0].EventID}
	withdraw := governanceTestDecision(t, ctx, pool, next, "race-withdraw-again")
	next.Operation = "restore"
	restore := governanceTestDecision(t, ctx, pool, next, "race-restore")
	successes = 0
	for _, r := range run(withdraw, restore) {
		if r.err == nil {
			successes++
		}
	}
	history, err = ReadGovernanceHistory(ctx, pool, "withdrawal", id)
	if successes != 1 || err != nil || len(history) != 2 {
		t.Fatal("withdraw/restore both won the same revision", successes, history, err)
	}
}

func TestIntegrationGovernanceDerivedOwnSourceFinding(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, current := prepareIntegrationClosurePair(t, ctx, pool, "own-source-parent")
	pending := createIntegrationClosureExternalProposal(t, ctx, pool, "own-source-output", "DERIVED-OWN", "r1", "Synthetic derived output.")
	req := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: pending.ProposalOccurrenceID, Derivation: &EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: []string{current.CanonicalRef}, Method: "synthetic rule", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:own-source"}}
	before, err := LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	source, err := loadGovernanceSource(ctx, pgxDB{pool: pool}, pending.SourceSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	sr := GovernanceRequest{Kind: "source_assessment", TargetID: pending.SourceSnapshotID, Operation: "assess", MaterialIDs: []string{pending.SourceSnapshotID}, Method: "synthetic retention check", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "v1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"synthetic finding"}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: pending.SourceSnapshotID, ContentHash: source.Snapshot.RawContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: evidenceuse.RetentionV1, Result: evidenceuse.CheckFail, MaterialRef: pending.SourceSnapshotID}}}
	governanceTestApply(t, ctx, pool, sr, "own-source-fail")
	after, err := LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	if before.Subject == after.Subject {
		t.Fatal("own-source finding absent from review subject")
	}
	for _, subject := range []string{before.Subject, after.Subject} {
		_, err := AdmitReviewedEndpoint(ctx, pool, ReviewedEndpointAdmissionInput{RequestID: "own-source-derived", Review: req, ExpectedSubject: subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"})
		if err == nil {
			t.Fatal("new derived admission bypassed its own source finding")
		}
	}
	var outcome string
	if err := pool.QueryRow(ctx, `SELECT admission_outcome FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, pending.ProposalOccurrenceID).Scan(&outcome); err != nil || outcome != "pending" {
		t.Fatal("failed gate mutated admission", outcome, err)
	}
}

func TestIntegrationGovernanceHistoricalCondensedSource(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	// Historical synthetic fixture uses internal materialization. New external
	// intake still requires its original verbatim declaration contract.
	statement := "Synthetic historical condensed statement."
	p, err := IngestManualText(ctx, pool, ManualTextInput{SourceSystem: SourceSystemExternalDocument, SourceID: "historical:unknown", SourceVersion: "legacy", Raw: []byte(statement), RequestID: "condensed-history", OriginMetadata: map[string]string{externalSourceOriginFidelityKey: "condensed", externalSourceOriginCoverageKey: "partial", externalSourceOriginObjectIDKey: "unknown"}}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic historical admission"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	receipt := governanceTestApply(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: admitted.CanonicalRef, Operation: "withdraw"}, "condensed-withdraw")
	if len(receipt.Display.Materials) != 1 || receipt.Display.Materials[0].Snapshot.OriginMetadata[externalSourceOriginFidelityKey] != "condensed" {
		t.Fatal("historical declaration fabricated")
	}
	after, err := GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("historical material rewritten", err)
	}
	sr := GovernanceRequest{Kind: "source_assessment", TargetID: p.SourceSnapshotID, Operation: "assess", MaterialIDs: []string{p.SourceSnapshotID}, Method: "retrospective source comparison", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "v1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"original provider revision unavailable"}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: p.SourceSnapshotID, ContentHash: receipt.Display.Materials[0].Snapshot.RawContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: evidenceuse.CorrespondenceV1, Result: evidenceuse.CheckPass, MaterialRef: p.SourceSnapshotID, ComparedRevision: "legacy"}}}
	if _, err := LoadGovernanceReview(ctx, pool, sr); err == nil {
		t.Fatal("missing historical revision treated as conclusive")
	}
	sr.Checks[0].Result = evidenceuse.CheckInconclusive
	governanceTestApply(t, ctx, pool, sr, "condensed-source-inconclusive")
}

func TestIntegrationGovernanceSourceCorrectionUsesExactBindings(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	// Synthetic historical metadata claims verbatim/exact_excerpt; the separately
	// retained provider material lets the human stub review a concrete mismatch.
	capture := func(sourceID, request, raw, statement string) (string, string) {
		p, err := IngestManualText(ctx, pool, ManualTextInput{SourceSystem: SourceSystemExternalDocument, SourceID: sourceID, SourceVersion: "r1", Raw: []byte(raw), RequestID: request, OriginMetadata: map[string]string{externalSourceOriginFidelityKey: "verbatim", externalSourceOriginCoverageKey: "exact_excerpt", externalSourceOriginRevisionKey: "r1", externalSourceOriginObjectIDKey: "unknown"}}, FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: statement, EvidenceRefs: []string{"span:S1"}}}})
		if err != nil {
			t.Fatal(err)
		}
		a, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic historical claim"})
		if err != nil {
			t.Fatal(err)
		}
		return a.CanonicalRef, p.SourceSnapshotID
	}
	a, sid := capture("synthetic:condensation", "binding-a", "Synthetic condensed summary.", "Synthetic correct statement.")
	b, sid2 := capture("synthetic:condensation", "binding-b", "Synthetic condensed summary.", "Synthetic unsupported statement.")
	_, other := capture("synthetic:unrelated", "binding-other", "Unrelated source.", "Unrelated statement.")
	_, compared := capture("synthetic:provider-r1", "binding-provider", "Full original provider text, including the omitted condition.", "Provider material retained for comparison.")
	if sid != sid2 || sid == other {
		t.Fatal("fixture did not separate exact bindings")
	}
	original, err := loadGovernanceSource(ctx, pgxDB{pool: pool}, sid)
	if err != nil {
		t.Fatal(err)
	}
	sr := GovernanceRequest{Kind: "source_assessment", TargetID: sid, Operation: "assess", MaterialIDs: []string{compared}, Method: "compare retained summary to revision r1", CheckerName: "TEST APPROVAL STUB", CheckerVersion: "v1", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{"synthetic mismatch; no claim truth verdict"}}
	for _, criterion := range []string{evidenceuse.CorrespondenceV1, evidenceuse.CoverageV1} {
		corrected := "condensed"
		if criterion == evidenceuse.CoverageV1 {
			corrected = "partial"
		}
		sr.Checks = append(sr.Checks, evidenceuse.SourceCheck{SourceSnapshotID: sid, ContentHash: original.Snapshot.RawContentHash, Scope: evidenceuse.RetainedSnapshotScope, Criterion: criterion, Result: evidenceuse.CheckFail, ComparedRevision: "r1", MaterialRef: compared, CorrectedValue: corrected})
	}
	event := governanceTestApply(t, ctx, pool, sr, "source-declaration-correction")
	for _, id := range []string{a, b} {
		p, err := ReadEvidenceUseProjection(ctx, pool, []string{id})
		if err != nil || governanceOutcome(t, p, id) != evidenceuse.Excluded {
			t.Fatal("bound claim did not inherit finding", id, err)
		}
		source := p.Snapshot.Sources[sid]
		if source.DeclaredFidelity != "verbatim" || source.DeclaredCoverage != "exact_excerpt" || len(source.Checks) != 2 || source.Checks[0].AssessedField == "" || p.Snapshot.Sources[other].AssessmentRevision != 0 {
			t.Fatal("declaration rewritten, field missing or object unknown fanned out", source)
		}
	}
	retained, err := loadGovernanceSource(ctx, pgxDB{pool: pool}, sid)
	if err != nil || !reflect.DeepEqual(original, retained) {
		t.Fatal("source correction changed retained source", err)
	}
	sr.ExpectedRevision = 1
	sr.PreviousEventID = event.EventID
	sr.ResolvesEventIDs = []string{event.EventID}
	sr.Checks = sr.Checks[:1]
	if _, err := LoadGovernanceReview(ctx, pool, sr); err == nil {
		t.Fatal("resolution silently erased unassessed coverage finding")
	}
}

func TestIntegrationGovernanceConflictOutsideDisplayStillBlocks(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	_, current := prepareIntegrationClosurePair(t, ctx, pool, "conflict-current")
	a := current.CanonicalRef
	pending := createIntegrationClosureExternalProposal(t, ctx, pool, "conflict-derived", "CONFLICT-DERIVED", "r1", "Synthetic derived conflict peer.")
	req := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: pending.ProposalOccurrenceID, Derivation: &EndpointDerivation{UsePolicy: "current-action/v1", ParentNodeIDs: []string{a}, Method: "synthetic inference", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:conflict"}}
	review, err := LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := AdmitReviewedEndpoint(ctx, pool, ReviewedEndpointAdmissionInput{RequestID: "conflict-derived", Review: req, ExpectedSubject: review.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"})
	if err != nil {
		t.Fatal(err)
	}
	b := admitted.Admission.CanonicalRef
	pair, err := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{RequestID: "governance-pair", NodeAID: a, NodeBID: b, Rationale: "TEST APPROVAL STUB: explicit synthetic conflict", ProducerName: "synthetic", ProducerVersion: "v1", ProducerSessionRef: "test:governance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{ProposalID: pair.Proposal.ID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "explicit synthetic pair"}); err != nil {
		t.Fatal(err)
	}
	view, err := readCanonicalGraphView(ctx, pgxDB{pool: pool}, CanonicalReadInput{RootNodeIDs: []string{a}, MaxDepth: 0, MaxNodes: 1, MaxEdges: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Artifact.Nodes) != 1 || len(view.UseState.Snapshot.Contradictions) != 1 || governanceOutcome(t, *view.UseState, a) != evidenceuse.Blocked {
		t.Fatal("display limit hid incident conflict")
	}
	p, err := ReadEvidenceUseProjection(ctx, pool, []string{a, b})
	if err != nil || governanceOutcome(t, p, a) != evidenceuse.Blocked || governanceOutcome(t, p, b) != evidenceuse.Blocked {
		t.Fatal("conflict chose an arbitrary winner", err)
	}
	governanceTestApply(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: b, Operation: "withdraw"}, "conflict-withdraw-peer")
	p, err = ReadEvidenceUseProjection(ctx, pool, []string{a, b})
	if err != nil || governanceOutcome(t, p, a) != evidenceuse.Eligible || governanceOutcome(t, p, b) != evidenceuse.Excluded || len(p.Snapshot.Contradictions) != 1 {
		t.Fatal("historical conflict edge treatment", err)
	}
}
