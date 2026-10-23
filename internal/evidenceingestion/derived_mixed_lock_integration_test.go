//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TEST APPROVAL STUB: different pending proposals share a current parent.
// Both internal historical materialization and reviewed current-use admission
// must remain writable, without reversing governance and derivation locks.
func TestIntegrationDerivedNativeReviewedLockOrder(t *testing.T) {
	for _, nativeFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("native_first=%t", nativeFirst), func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			_, parent := prepareIntegrationClosurePair(t, ctx, pool, "mixed-derived-parent")
			p := createIntegrationClosureExternalProposal(t, ctx, pool, "mixed-native-derived", "NATIVE-D", "r1", "Synthetic historical derived conclusion.")
			native := AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic native derived", Derivation: &DerivationAdmissionInput{ParentNodeIDs: []string{parent.CanonicalRef}, Method: "AND", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:mixed-derived"}}
			reviewed := governanceDerivedInput(t, ctx, pool, "mixed-reviewed-derived", parent.CanonicalRef)
			np, nativePID := governanceRacePool(t, ctx, pool, "mixed-native-derived")
			rp, reviewedPID := governanceRacePool(t, ctx, pool, "mixed-reviewed-derived")
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			runCtx, cancel := context.WithCancel(ctx)
			var workers sync.WaitGroup
			defer func() {
				cancel()
				_ = blocker.Rollback(context.Background())
				workers.Wait()
			}()
			var blockerPID int32
			if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			// This real advisory lock is the only barrier. The first writer must
			// already hold governance when it waits here, in either ordering.
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, derivationAdmissionLockKey); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				admission AdmissionResult
				receipt   EndpointAdmissionReceipt
				err       error
			}
			nativeCall := func() outcome {
				r, err := AdmitPendingProposal(runCtx, np, native)
				return outcome{admission: r, err: err}
			}
			reviewedCall := func() outcome {
				r, err := AdmitReviewedEndpoint(runCtx, rp, reviewed)
				return outcome{admission: r.Admission, receipt: r, err: err}
			}
			first, second := nativeCall, reviewedCall
			firstPID, secondPID := nativePID, reviewedPID
			if !nativeFirst {
				first, second = reviewedCall, nativeCall
				firstPID, secondPID = reviewedPID, nativePID
			}
			start := func(call func() outcome) <-chan outcome {
				ch := make(chan outcome, 1)
				workers.Go(func() { ch <- call() })
				return ch
			}
			firstResult := start(first)
			governanceWaitBlocked(t, ctx, pool, firstPID, blockerPID)
			secondResult := start(second)
			governanceWaitBlocked(t, ctx, pool, secondPID, firstPID)
			var query string
			if err := pool.QueryRow(ctx, `SELECT query FROM pg_stat_activity WHERE pid=$1`, secondPID).Scan(&query); err != nil {
				t.Fatal(err)
			}
			if query != `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)` {
				t.Fatalf("second writer waited outside governance: %s", query)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			receive := func(ch <-chan outcome) outcome {
				t.Helper()
				select {
				case r := <-ch:
					if r.err != nil || r.admission.Replayed {
						t.Fatal("fresh derived write failed", r.err)
					}
					return r
				case <-ctx.Done():
					t.Fatal(ctx.Err())
					return outcome{}
				}
			}
			a, b := receive(firstResult), receive(secondResult)
			if !nativeFirst {
				a, b = b, a
			}
			for _, admitted := range []AdmissionResult{a.admission, b.admission} {
				var parents, decisions int
				if err := pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM canonical_derivation_parents p JOIN canonical_derivations d USING(derivation_id)
  JOIN canonical_graph_edges e ON e.canonical_edge_id=p.canonical_edge_id
  WHERE d.node_id=$1 AND p.parent_node_id=$2 AND e.from_node_id=$2 AND e.to_node_id=$1 AND e.relation='derived_from'),
 (SELECT count(*) FROM admission_decisions WHERE canonical_ref=$1)`, admitted.CanonicalRef, parent.CanonicalRef).Scan(&parents, &decisions); err != nil || parents != 1 || decisions != 1 {
					t.Fatal("derived authority lost its exact parent or admission", parents, decisions, err)
				}
				registrationEligibility(t, ctx, pool, admitted.CanonicalRef, true)
			}
			assertRetry := func() {
				t.Helper()
				n := nativeCall()
				if n.err != nil || !n.admission.Replayed || n.admission.CanonicalRef != a.admission.CanonicalRef || n.admission.AdmissionDecisionID != a.admission.AdmissionDecisionID {
					t.Fatal("native historical retry changed", n.err)
				}
				r := reviewedCall()
				want := b.receipt
				want.Admission.Replayed = true
				if r.err != nil || !reflect.DeepEqual(r.receipt, want) {
					t.Fatal("reviewed receipt changed", r.err)
				}
			}
			assertRetry()
			withdrawal := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parent.CanonicalRef, Operation: "withdraw"}, "mixed-derived-withdrawal")
			if _, err := ApplyGovernanceDecision(ctx, pool, withdrawal); err != nil {
				t.Fatal(err)
			}
			registrationEligibility(t, ctx, pool, a.admission.CanonicalRef, false)
			registrationEligibility(t, ctx, pool, b.admission.CanonicalRef, false)
			assertRetry()
			// Replay must not acquire the fresh-write fence, even while governance
			// is locked elsewhere. A timeout turns an accidental new gate into FAIL.
			replayBlocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer replayBlocker.Rollback(context.Background())
			if _, err := replayBlocker.Exec(ctx, `SELECT evidence_governance_lock_v1('evidence_governance_head'::regclass)`); err != nil {
				t.Fatal(err)
			}
			replayCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			if r, err := AdmitPendingProposal(replayCtx, np, native); err != nil || !r.Replayed {
				t.Fatal("native historical retry waited for governance", err)
			}
		})
	}
}
