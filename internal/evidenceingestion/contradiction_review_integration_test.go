//go:build integration

package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func contradictionTestRequest(a, b, id string) ContradictionReviewRequest {
	return ContradictionReviewRequest{ProposalRequestID: id, NodeAID: a, NodeBID: b, Rationale: "TEST APPROVAL STUB: these synthetic claims conflict in the declared scope", Scope: "synthetic release and identical operating conditions", Conditions: []string{"same synthetic time window"}, ProducerName: "synthetic-test", ProducerVersion: "v1", Relation: "contradicts"}
}
func TestIntegrationReviewedContradictionLifecycle(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := propositionTestClaim(t, ctx, pool, "reviewed-contradiction-a")
	b := propositionTestClaim(t, ctx, pool, "reviewed-contradiction-b")
	r, err := LoadContradictionReview(ctx, pool, contradictionTestRequest(b, a, "review-pair"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Display.Request.NodeAID >= r.Display.Request.NodeBID || len(r.Display.Sources) != 2 || r.Mode != "pending_decision" {
		t.Fatal("incomplete review")
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM canonical_contradiction_proposals`).Scan(&n)
	if n != 0 {
		t.Fatal("getter consumed pair")
	}
	in := ReviewedContradictionInput{RequestID: "decision", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: independent relation review", ReviewerID: "TEST APPROVAL STUB"}
	bad := in
	bad.Review.Scope = "changed"
	if _, err = ApplyReviewedContradiction(ctx, pool, bad); err == nil {
		t.Fatal("changed scope accepted")
	}
	bad = in
	bad.ExpectedSubject = "node-approval"
	if _, err = ApplyReviewedContradiction(ctx, pool, bad); err == nil {
		t.Fatal("node approval accepted")
	}
	result, err := ApplyReviewedContradiction(ctx, pool, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetCanonicalContradictionProposal(ctx, pool, result.Decision.ProposalID)
	if err != nil || got.ReviewReceipt == nil || got.ReviewReceipt.ID != result.ID || got.Decision.CanonicalEdgeID == "" {
		t.Fatal("query receipt", err)
	}
	var edges, nodes int
	pool.QueryRow(ctx, `SELECT count(*) FROM canonical_graph_edges WHERE relation='contradicts'`).Scan(&edges)
	pool.QueryRow(ctx, `SELECT count(*) FROM canonical_graph_nodes`).Scan(&nodes)
	if edges != 1 {
		t.Fatal("symmetric relation not unique")
	}
	// Change current state, then retry unchanged historical approval.
	governanceTestApply(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: a, Operation: "withdraw"}, "later-withdrawal")
	again, err := ApplyReviewedContradiction(ctx, pool, in)
	if err != nil || !again.Replayed || again.ID != result.ID {
		t.Fatal("historical exact replay", err)
	}
	historical, err := LoadContradictionReview(ctx, pool, in.Review)
	if err != nil || historical.Mode != "exact_replay_only" || historical.Subject != r.Subject {
		t.Fatal("historical getter", err)
	}
	for _, change := range []func(*ReviewedContradictionInput){func(i *ReviewedContradictionInput) { i.RequestID = "new-decision" }, func(i *ReviewedContradictionInput) { i.Decision = "reject" }, func(i *ReviewedContradictionInput) { i.ReviewerID = "other" }, func(i *ReviewedContradictionInput) { i.DecisionReason = "other" }, func(i *ReviewedContradictionInput) { i.Review.Conditions = []string{"other"} }} {
		bad = in
		change(&bad)
		if _, err = ApplyReviewedContradiction(ctx, pool, bad); err == nil {
			t.Fatal("changed retry accepted")
		}
	}
	down, err := os.ReadFile("../../migrations/000059_reviewed_contradictions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback discarded authority")
	}
	for _, q := range []string{`UPDATE canonical_contradiction_review_bindings SET body=body`, `DELETE FROM canonical_contradiction_review_bindings`, `TRUNCATE canonical_contradiction_review_bindings`} {
		if _, err = pool.Exec(ctx, q); err == nil {
			t.Fatal("mutable binding")
		}
	}
}
func TestIntegrationReviewedContradictionTerminalAndLegacy(t *testing.T) {
	for _, outcome := range []string{"reject", "audit_only"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			a := propositionTestClaim(t, ctx, pool, "a")
			b := propositionTestClaim(t, ctx, pool, "b")
			req := contradictionTestRequest(a, b, "legacy-pending")
			p, err := prepareContradictionReview(req)
			if err != nil {
				t.Fatal(err)
			}
			old, err := SubmitCanonicalContradictionProposal(ctx, pool, p.input)
			if err != nil {
				t.Fatal(err)
			}
			r, err := LoadContradictionReview(ctx, pool, req)
			if err != nil || r.Display.Proposal.ID != old.Proposal.ID {
				t.Fatal("legacy pending changed identity", err)
			}
			bad := req
			bad.Rationale = "changed"
			if _, err = LoadContradictionReview(ctx, pool, bad); err == nil {
				t.Fatal("changed legacy payload accepted")
			}
			in := ReviewedContradictionInput{RequestID: "terminal", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: outcome, DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
			first, err := ApplyReviewedContradiction(ctx, pool, in)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ApplyReviewedContradiction(ctx, pool, in)
			if err != nil || !again.Replayed || again.ID != first.ID {
				t.Fatal("terminal replay", err)
			}
			var n int
			pool.QueryRow(ctx, `SELECT count(*) FROM canonical_graph_edges WHERE relation='contradicts'`).Scan(&n)
			if n != 0 {
				t.Fatal("disposition created edge")
			}
			if _, err = AdmitPendingCanonicalContradiction(ctx, pool, CanonicalContradictionAdmissionInput{ProposalID: first.Decision.ProposalID, DecisionBy: "test", DecisionReason: "reopen"}); err == nil {
				t.Fatal("legacy reopened reviewed pair")
			}
		})
	}
	ctx, pool := integrationPoolWithMigrations(t)
	a := propositionTestClaim(t, ctx, pool, "legacy-a")
	b := propositionTestClaim(t, ctx, pool, "legacy-b")
	req := contradictionTestRequest(a, b, "legacy-terminal")
	p, _ := prepareContradictionReview(req)
	old, err := SubmitCanonicalContradictionProposal(ctx, pool, p.input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RecordPendingCanonicalContradictionDisposition(ctx, pool, CanonicalContradictionDispositionInput{ProposalID: old.Proposal.ID, Outcome: "audit_only", DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadContradictionReview(ctx, pool, req); err == nil {
		t.Fatal("legacy terminal reopened")
	}
}
func TestIntegrationReviewedContradictionRolesConcurrencyAndAtomicity(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	a := propositionTestClaim(t, ctx, pool, "role-a")
	b := propositionTestClaim(t, ctx, pool, "role-b")
	reviewer := externalRecordRolePool(t, ctx, pool, dbrole.ProfileContradictionReviewer)
	// The fixture helper grants table DML. Grant the one declared helper as well.
	var schema string
	pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema)
	role := strings.ReplaceAll(schema+"_contradiction_reviewer", "-", "_")
	if len(role) > 63 {
		role = role[:63]
	}
	if _, err := pool.Exec(ctx, `GRANT EXECUTE ON FUNCTION evidence_governance_lock_v1(regclass) TO "`+role+`"`); err != nil {
		t.Fatal(err)
	}
	r, err := LoadContradictionReview(ctx, reviewer, contradictionTestRequest(a, b, "role-pair"))
	if err != nil {
		t.Fatal(err)
	}
	in := ReviewedContradictionInput{RequestID: "concurrent", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
	var wg sync.WaitGroup
	results := make(chan ContradictionReviewReceipt, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { out, e := ApplyReviewedContradiction(ctx, reviewer, in); results <- out; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	replays := 0
	for got := range results {
		if got.Replayed {
			replays++
		}
	}
	if replays != 1 {
		t.Fatal("parallel retry did not preserve one decision")
	}
	for _, q := range []string{`INSERT INTO canonical_graph_nodes SELECT * FROM canonical_graph_nodes WHERE false`, `INSERT INTO canonical_graph_edges SELECT * FROM canonical_graph_edges WHERE false`, `UPDATE canonical_contradiction_proposals SET rationale=rationale`, `INSERT INTO canonical_contradiction_admission_decisions SELECT * FROM canonical_contradiction_admission_decisions WHERE false`, `INSERT INTO canonical_withdrawal_events SELECT * FROM canonical_withdrawal_events WHERE false`} {
		if _, err = reviewer.Exec(ctx, q); err == nil {
			t.Fatal("expanded role authority", q)
		}
	}
	// A conflicting request ID with ON CONFLICT DO NOTHING must not leave the
	// BEFORE trigger's new pair effects behind. Deferred FK ties decision to receipt.
	c := propositionTestClaim(t, ctx, pool, "atomic-c")
	d := propositionTestClaim(t, ctx, pool, "atomic-d")
	fresh, err := LoadContradictionReview(ctx, reviewer, contradictionTestRequest(c, d, "atomic-pair"))
	if err != nil {
		t.Fatal(err)
	}
	decisionID, _ := canonicalContradictionDecisionID(fresh.Display.Proposal.ID, "admitted")
	edge := buildCanonicalContradictionEdge(fresh.Display.Proposal, decisionID)
	raw, _ := json.Marshal(fresh.Display)
	body, _ := json.Marshal(contradictionReviewBody{Contract: ReviewedContradictionV1, RequestID: "concurrent", Subject: fresh.Subject, Display: string(raw), Decision: CanonicalContradictionDecision{ID: decisionID, ProposalID: fresh.Display.Proposal.ID, Outcome: "admitted", CanonicalEdgeID: edge.ID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic"}, Edge: edge})
	id := governanceDigest("contradiction-review-receipt:sha256:", body)
	if _, err = reviewer.Exec(ctx, `INSERT INTO canonical_contradiction_review_bindings(receipt_id,request_id,proposal_id,body) VALUES($1,$2,$3,$4) ON CONFLICT(request_id) DO NOTHING`, id, "concurrent", fresh.Display.Proposal.ID, string(body)); err == nil {
		t.Fatal("orphan trigger effects committed")
	}
	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM canonical_contradiction_proposals WHERE canonical_contradiction_proposal_id=$1`, fresh.Display.Proposal.ID).Scan(&count)
	if count != 0 {
		t.Fatal("failed receipt retained proposal")
	}
	// Stale native review after a real concurrent source change must fail.
	if _, err = pool.Exec(ctx, `UPDATE source_snapshots SET source_version=source_version`); err != nil {
		t.Fatal(err)
	}
	stale := in
	stale.RequestID = "stale"
	stale.Review = fresh.Display.Request
	stale.ExpectedSubject = fresh.Subject
	if _, err = ApplyReviewedContradiction(ctx, reviewer, stale); err == nil {
		t.Fatal("stale governance review accepted")
	}
}

func TestIntegrationReviewedContradictionWaitsForConcurrentGovernance(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	a := propositionTestClaim(t, ctx, pool, "concurrent-review-a")
	b := propositionTestClaim(t, ctx, pool, "concurrent-review-b")
	r, err := LoadContradictionReview(ctx, pool, contradictionTestRequest(a, b, "concurrent-review"))
	if err != nil {
		t.Fatal(err)
	}
	// Bind apply to one known physical session so the test observes an actual
	// PostgreSQL lock wait, rather than assuming overlap from goroutine timing.
	cfg := pool.Config()
	cfg.MaxConns = 1
	writer, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var waiter int32
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&waiter); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	var blockerID int32
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerID); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `UPDATE source_snapshots SET source_version=source_version`); err != nil {
		t.Fatal(err)
	}
	in := ReviewedContradictionInput{RequestID: "concurrent-decision", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
	finished := make(chan error, 1)
	go func() { _, err := ApplyReviewedContradiction(ctx, writer, in); finished <- err }()
	governanceWaitBlocked(t, ctx, pool, waiter, blockerID)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err == nil {
		t.Fatal("committed against stale pre-wait source review")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM canonical_contradiction_review_bindings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale decision left receipt", err)
	}
}

func TestIntegrationReviewedContradictionDerivedEmptyConditionsRoundTrip(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	a := propositionTestClaim(t, ctx, pool, "round-trip-parent-a")
	b := propositionTestClaim(t, ctx, pool, "round-trip-parent-b")
	d := propositionTestClaim(t, ctx, pool, "round-trip-derived", a, b)
	req := contradictionTestRequest(d, b, "round-trip-pair")
	req.Conditions = []string{}
	review, err := LoadContradictionReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	endpoints := map[string]bool{d: true, b: true}
	if !endpoints[review.Display.NodeA.CanonicalID] || !endpoints[review.Display.NodeB.CanonicalID] || review.Display.NodeA.CanonicalID == review.Display.NodeB.CanonicalID {
		t.Fatal("review changed the exact derived/source endpoints")
	}
	wantSources := map[string]bool{
		"Externally declared evidence round-trip-parent-a\n": true,
		"Externally declared evidence round-trip-parent-b\n": true,
		"Externally declared evidence round-trip-derived\n":  true,
	}
	for _, source := range review.Display.Sources {
		if !wantSources[string(source.RetainedBytes)] {
			t.Fatal("unexpected or duplicate retained source")
		}
		delete(wantSources, string(source.RetainedBytes))
	}
	if len(wantSources) != 0 {
		t.Fatalf("review omitted %d derived/ancestor sources", len(wantSources))
	}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ContradictionReview
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Display.Request.Conditions == nil || len(decoded.Display.Request.Conditions) != 0 {
		t.Fatal("getter round-trip did not preserve an explicit empty conditions array")
	}
	in := ReviewedContradictionInput{RequestID: "round-trip-decision", Review: decoded.Display.Request, ExpectedSubject: decoded.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB: synthetic derived relation", ReviewerID: "TEST APPROVAL STUB"}
	packet, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var incoming ReviewedContradictionInput
	if err := json.Unmarshal(packet, &incoming); err != nil {
		t.Fatal(err)
	}
	if incoming.Review.Conditions == nil || len(incoming.Review.Conditions) != 0 {
		t.Fatal("apply round-trip did not preserve an explicit empty conditions array")
	}
	if incoming.ReviewerID != "" {
		t.Fatal("launcher reviewer identity leaked into client JSON")
	}
	incoming.ReviewerID = in.ReviewerID // TEST APPROVAL STUB: trusted launcher binding.
	receipt, err := ApplyReviewedContradiction(ctx, pool, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed || receipt.Subject != review.Subject || receipt.Decision.Outcome != "admitted" {
		t.Fatal("round-trip did not admit the exact native review")
	}
	replay, err := ApplyReviewedContradiction(ctx, pool, incoming)
	if err != nil || !replay.Replayed || replay.ID != receipt.ID {
		t.Fatal("exact round-trip replay", err)
	}
	query, err := GetCanonicalContradictionProposal(ctx, pool, receipt.Decision.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if query.ReviewReceipt == nil || query.ReviewReceipt.ID != receipt.ID {
		t.Fatal("Query omitted the round-trip receipt")
	}
	originalDisplay, err := json.Marshal(review.Display)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range []ContradictionReviewReceipt{receipt, replay, *query.ReviewReceipt} {
		savedDisplay, err := json.Marshal(saved.Display)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(originalDisplay, savedDisplay) {
			t.Fatal("receipt/replay/Query changed the historical display after JSON round-trip")
		}
	}
}

func TestIntegrationReviewedContradictionCompetingDecisions(t *testing.T) {
	for _, first := range []string{"approved", "reject"} {
		t.Run(first+"_queues_first", func(t *testing.T) {
			base, pool := integrationPoolWithMigrations(t)
			ctx, cancel := context.WithCancel(base)
			defer cancel()
			a := propositionTestClaim(t, ctx, pool, "competing-a")
			b := propositionTestClaim(t, ctx, pool, "competing-b")
			review, err := LoadContradictionReview(ctx, pool, contradictionTestRequest(a, b, "competing-pair"))
			if err != nil {
				t.Fatal(err)
			}
			firstWriter, firstPID := governanceRacePool(t, ctx, pool, "contradiction-first")
			secondWriter, secondPID := governanceRacePool(t, ctx, pool, "contradiction-second")
			writers := []*pgxpool.Pool{firstWriter, secondWriter}
			pids := []int32{firstPID, secondPID}
			var wg sync.WaitGroup
			t.Cleanup(wg.Wait) // Join canceled writers before their pools are closed.
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			var blockerPID int32
			if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			if firstPID == secondPID || firstPID == blockerPID || secondPID == blockerPID {
				t.Fatal("competing decisions need three distinct physical sessions")
			}
			if _, err := blocker.Exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`); err != nil {
				t.Fatal(err)
			}
			decisions := []string{first, "reject"}
			if first == "reject" {
				decisions[1] = "approved"
			}
			type result struct {
				input   ReviewedContradictionInput
				receipt ContradictionReviewReceipt
				err     error
			}
			done := make(chan result, 2)
			waitCtx, stopWait := context.WithTimeout(ctx, 5*time.Second)
			defer stopWait()
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for i, decision := range decisions {
				in := ReviewedContradictionInput{RequestID: "competing-" + decision, Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: decision, DecisionReason: "TEST APPROVAL STUB: " + decision, ReviewerID: "TEST APPROVAL STUB"}
				wg.Go(func() {
					got, err := ApplyReviewedContradiction(ctx, writers[i], in)
					done <- result{input: in, receipt: got, err: err}
				})
				// The second writer can wait on the first writer's tuple lock or
				// the blocker transaction. Observe either known blocker, not timing.
				blockers := []int32{blockerPID}
				if i == 1 {
					blockers = append(blockers, firstPID)
				}
				for {
					var waiting bool
					if err := pool.QueryRow(waitCtx, `SELECT pg_blocking_pids($1) && $2::int[]`, pids[i], blockers).Scan(&waiting); err != nil {
						t.Fatal("observing actual competing decision lock wait", err)
					}
					if waiting {
						break
					}
					select {
					case <-waitCtx.Done():
						t.Fatal("decision did not reach the expected lock boundary")
					case <-tick.C:
					}
				}
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var winner, loser result
			successes := 0
			for range 2 {
				got := <-done
				if got.err == nil {
					successes++
					winner = got
				} else {
					if kind, ok := KindOf(got.err); !ok || kind != ErrorReviewContractConflict {
						t.Fatalf("competitor failed outside the review contract: %v", got.err)
					}
					loser = got
				}
			}
			if successes != 1 {
				t.Fatalf("competing terminal decisions: got %d successes, want 1", successes)
			}
			wantOutcome, wantEdges := "rejected", 0
			if winner.input.Decision == "approved" {
				wantOutcome, wantEdges = "admitted", 1
			}
			if winner.receipt.Replayed || winner.receipt.RequestID != winner.input.RequestID || winner.receipt.Decision.Outcome != wantOutcome || winner.receipt.Subject != review.Subject || winner.receipt.Decision.DecisionReason != winner.input.DecisionReason {
				t.Fatal("winner receipt does not bind its exact request and decision")
			}
			replay, err := ApplyReviewedContradiction(ctx, pool, winner.input)
			if err != nil || !replay.Replayed || replay.ID != winner.receipt.ID {
				t.Fatal("winning decision exact replay", err)
			}
			_, err = ApplyReviewedContradiction(ctx, pool, loser.input)
			if kind, ok := KindOf(err); !ok || kind != ErrorReviewContractConflict {
				t.Fatalf("losing request reopened the terminal pair: %v", err)
			}
			var receipts, decisionsCount, edges, losingReceipts int
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM canonical_contradiction_review_bindings),
				(SELECT count(*) FROM canonical_contradiction_admission_decisions),
				(SELECT count(*) FROM canonical_graph_edges WHERE relation='contradicts'),
				(SELECT count(*) FROM canonical_contradiction_review_bindings WHERE request_id=$1)`, loser.input.RequestID).Scan(&receipts, &decisionsCount, &edges, &losingReceipts); err != nil {
				t.Fatal(err)
			}
			if receipts != 1 || decisionsCount != 1 || edges != wantEdges || losingReceipts != 0 {
				t.Fatalf("partial/duplicate terminal effects: receipts=%d decisions=%d edges=%d losing receipts=%d", receipts, decisionsCount, edges, losingReceipts)
			}
			// Serialization retries may reorder the writers; queue order is not
			// a promise that one particular human decision will win.
			t.Logf("both writers observed blocked; one winner=%s, loser=review_contract_conflict", wantOutcome)
		})
	}
}
