//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: retain both live writers and exercise different proposals
// and source objects. Sharing a global head permits one first commit; the other
// must report a stale head, then accept a separately refreshed decision.
func TestIntegrationSupersessionNativeReviewedLockOrder(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, nativeFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("registered=%t/native_first=%t", registered, nativeFirst), func(t *testing.T) {
				ctx, pool := integrationPoolWithMigrations(t)
				fixture := func(label, object string, register bool) (IngestResult, string, SupersessionLineageBasis) {
					t.Helper()
					var target string
					if register {
						in := registrationNativeFixture(t, ctx, pool, label, object)
						if _, err := ApplyInitialLineageRegistration(ctx, pool, in); err != nil {
							t.Fatal(err)
						}
						target = in.Review.ClaimID
					} else {
						old := createIntegrationClosureExternalProposal(t, ctx, pool, label+"-old", object, "r1", "Synthetic original "+object)
						admitted, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic original"})
						if err != nil {
							t.Fatal(err)
						}
						target = admitted.CanonicalRef
					}
					next := createIntegrationClosureExternalProposal(t, ctx, pool, label+"-new", object, "r2", "Synthetic replacement "+object)
					basis := supersessionIntegrationBasis()
					basis.ObjectID = object
					return next, target, basis
				}
				a, oldA, basisA := fixture("mixed-native", "MIXED-A", registered)
				b, oldB, basisB := fixture("mixed-reviewed", "MIXED-B", false)
				native := SupersessionAdmissionInput{ProposalOccurrenceID: a.ProposalOccurrenceID, Basis: basisA, TargetNodeIDs: []string{oldA}, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic native replacement"}
				r, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: b.ProposalOccurrenceID, Basis: basisB, TargetNodeIDs: []string{oldB}})
				if err != nil {
					t.Fatal(err)
				}
				reviewed := ReviewedSupersessionInput{RequestID: "mixed-reviewed", Review: r.Display.Request, ExpectedSubject: r.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic reviewed replacement"}
				open := func(name string) *pgxpool.Pool {
					t.Helper()
					cfg := pool.Config().Copy()
					cfg.ConnConfig.RuntimeParams["application_name"] = name
					p, err := pgxpool.NewWithConfig(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(p.Close)
					return p
				}
				firstPool, secondPool := open("mixed-first"), open("mixed-second")
				runCtx, cancel := context.WithCancel(ctx)
				type outcome struct {
					result SupersessionAdmissionResult
					err    error
				}
				nativeCall := func(p *pgxpool.Pool) outcome {
					r, err := AdmitPendingSupersession(runCtx, p, native)
					return outcome{r, err}
				}
				reviewedCall := func(p *pgxpool.Pool) outcome {
					r, err := AdmitReviewedSupersession(runCtx, p, reviewed)
					return outcome{r.Admission, err}
				}
				first, second := nativeCall, reviewedCall
				loserOccurrence := b.ProposalOccurrenceID
				if !nativeFirst {
					first, second = reviewedCall, nativeCall
					loserOccurrence = a.ProposalOccurrenceID
				}
				blocker, err := pool.Begin(ctx)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				firstResult, secondResult := make(chan outcome, 1), make(chan outcome, 1)
				startedFirst, startedSecond, readFirst, readSecond := false, false, false, false
				defer func() {
					cancel()
					_ = blocker.Rollback(context.Background())
					for _, pending := range []struct {
						started, read bool
						result        chan outcome
					}{{startedFirst, readFirst, firstResult}, {startedSecond, readSecond, secondResult}} {
						if pending.started && !pending.read {
							select {
							case <-pending.result:
							case <-time.After(5 * time.Second):
								t.Error("mixed writer did not stop before fixture cleanup")
							}
						}
					}
				}()
				// Holding only the v2 head lets us observe the first writer retaining
				// governance while waiting for that head. The second must then wait
				// on governance behind the first, never take the head before it.
				if _, err := blocker.Exec(ctx, `SELECT revision FROM canonical_supersession_admission_head WHERE chain_key='canonical-supersession/v1' FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				startedFirst = true
				go func() { firstResult <- first(firstPool) }()
				waitMixedSupersessionLock(t, ctx, pool, "mixed-first", false)
				startedSecond = true
				go func() { secondResult <- second(secondPool) }()
				waitMixedSupersessionLock(t, ctx, pool, "mixed-second", true)
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				var winner, loser outcome
				select {
				case winner = <-firstResult:
					readFirst = true
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case loser = <-secondResult:
					readSecond = true
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if winner.err != nil || winner.result.Replayed || winner.result.EventRevision != 1 {
					t.Fatal("first writer failed", winner.err)
				}
				var pgerr *pgconn.PgError
				if errors.As(loser.err, &pgerr) {
					t.Fatal("database error escaped mixed writers", pgerr.Code)
				}
				if kind, ok := KindOf(loser.err); !ok || kind != ErrorSupersessionHeadConflict {
					t.Fatal("expected stale-head domain conflict", loser.err)
				}
				var state string
				var events, decisions int
				if err := pool.QueryRow(ctx, `SELECT admission_outcome,(SELECT count(*) FROM canonical_supersession_admission_events),(SELECT count(*) FROM admission_decisions WHERE proposal_occurrence_id=$1) FROM proposal_occurrences WHERE proposal_occurrence_id=$1`, loserOccurrence).Scan(&state, &events, &decisions); err != nil || state != "pending" || events != 1 || decisions != 0 {
					t.Fatal("losing write was not atomic", state, events, decisions, err)
				}
				assertRetry := func() {
					t.Helper()
					again := first(firstPool)
					want := winner.result
					want.Replayed = true
					if again.err != nil || !reflect.DeepEqual(again.result, want) {
						t.Fatal("original winner retry changed", again.err)
					}
				}
				assertRetry()
				// A new synthetic decision supplies the advanced CAS coordinate.
				// The old request is not silently normalized into an approval.
				if nativeFirst {
					fresh, err := LoadSupersessionReview(ctx, pool, reviewed.Review)
					if err != nil {
						t.Fatal(err)
					}
					reviewed.RequestID = "mixed-reviewed-refreshed"
					reviewed.Review, reviewed.ExpectedSubject = fresh.Display.Request, fresh.Subject
					reviewed.DecisionReason = "TEST APPROVAL STUB: separately refreshed decision"
				} else {
					native.ExpectedRevision, native.ExpectedHeadEventID = winner.result.EventRevision, winner.result.AdmissionEventID
					native.DecisionReason = "TEST APPROVAL STUB: separately refreshed decision"
				}
				later := second(secondPool)
				if later.err != nil || later.result.Replayed || later.result.EventRevision != 2 {
					t.Fatal("retained writer could not admit with refreshed authority", later.err)
				}
				assertRetry()
				for _, admitted := range []SupersessionAdmissionResult{winner.result, later.result} {
					p, err := GetCanonicalSupersessionCurrentness(ctx, pool, admitted.LineageKey)
					if err != nil {
						t.Fatal(err)
					}
					if registered {
						if p.JointProjection == nil || p.JointProjection.Witness == nil || len(p.JointProjection.FrontierNodeIDs) != 1 || p.JointProjection.FrontierNodeIDs[0] != admitted.CanonicalRef {
							t.Fatal("mixed history lost joint closure", p)
						}
					} else if p.Projection == nil || p.Projection.Witness == nil || len(p.Projection.FrontierNodeIDs) != 1 || p.Projection.FrontierNodeIDs[0] != admitted.CanonicalRef {
						t.Fatal("mixed history lost legacy closure", p)
					}
				}
			})
		}
	}
}

func waitMixedSupersessionLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, governance bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting bool
		var err error
		if governance {
			err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$1 AND a.wait_event_type='Lock' AND a.query LIKE '%SELECT evidence_governance_lock_v1%' AND EXISTS(SELECT 1 FROM pg_stat_activity b WHERE b.application_name='mixed-first' AND b.pid=ANY(pg_blocking_pids(a.pid))))`, name).Scan(&waiting)
		} else {
			err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event_type='Lock' AND query LIKE '%canonical_supersession_admission_head%' AND query LIKE '%FOR UPDATE%')`, name).Scan(&waiting)
		}
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("writer %s did not reach expected lock order (governance=%t)", name, governance)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
