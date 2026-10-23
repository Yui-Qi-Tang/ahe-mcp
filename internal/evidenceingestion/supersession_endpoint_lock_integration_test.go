//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: both routes are individually valid; concurrent admission
// of one occurrence must resolve without a proposal/governance deadlock.
func TestIntegrationSupersessionEndpointLockOrder(t *testing.T) {
	for _, mode := range []string{"supersession_first", "endpoint_first"} {
		t.Run(mode, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			a := createSupersessionExternalProposal(t, ctx, pool, "review-lock-a", "r1", "Synthetic A original.")
			admitted, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: a.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic A"})
			if err != nil {
				t.Fatal(err)
			}
			b1 := createIntegrationClosureExternalProposal(t, ctx, pool, "review-lock-b1", "LOCK-B", "r1", "Synthetic B original.")
			b, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: b1.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic B"})
			if err != nil {
				t.Fatal(err)
			}
			b2 := createIntegrationClosureExternalProposal(t, ctx, pool, "review-lock-b2", "LOCK-B", "r2", "Synthetic B current.")
			basis := supersessionIntegrationBasis()
			basis.ObjectID = "LOCK-B"
			current, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: b2.ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{b.CanonicalRef}, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic current B"})
			if err != nil {
				t.Fatal(err)
			}
			pending := createSupersessionExternalProposal(t, ctx, pool, "review-lock-p", "r2", "Synthetic new A claim.")
			sr, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: pending.ProposalOccurrenceID, Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{admitted.CanonicalRef}})
			if err != nil {
				t.Fatal(err)
			}
			er, err := LoadEndpointReview(ctx, pool, registrationCurrentEndpointRequest(pending.ProposalOccurrenceID, current.CanonicalRef))
			if err != nil {
				t.Fatal(err)
			}
			sin := ReviewedSupersessionInput{RequestID: "review-lock-s", Review: sr.Display.Request, ExpectedSubject: sr.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic replacement"}
			ein := ReviewedEndpointAdmissionInput{RequestID: "review-lock-e", Review: er.Display.Request, ExpectedSubject: er.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic derivation"}

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
			firstPool, secondPool := open("issue24-lock-first"), open("issue24-lock-second")
			type outcome struct {
				replayed bool
				err      error
			}
			supersession := func(p *pgxpool.Pool) outcome {
				r, err := AdmitReviewedSupersession(ctx, p, sin)
				return outcome{r.Admission.Replayed, err}
			}
			endpoint := func(p *pgxpool.Pool) outcome {
				r, err := AdmitReviewedEndpoint(ctx, p, ein)
				return outcome{r.Admission.Replayed, err}
			}
			first, second := supersession, endpoint
			if mode == "endpoint_first" {
				first, second = endpoint, supersession
			}
			// Pause the first real writer after its real head-row lock is acquired.
			// Both the old and repaired code can reach this barrier. The second writer
			// must wait on the occurrence, without first retaining the governance row.
			_, err = pool.Exec(ctx, `CREATE FUNCTION issue24_lock_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF current_setting('application_name')='issue24-lock-first' THEN PERFORM pg_advisory_xact_lock(7102301); END IF; RETURN NEW; END $$; CREATE TRIGGER issue24_lock_barrier AFTER UPDATE ON evidence_governance_head FOR EACH ROW EXECUTE FUNCTION issue24_lock_barrier()`)
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(7102301)`); err != nil {
				t.Fatal(err)
			}
			firstResult, secondResult := make(chan outcome, 1), make(chan outcome, 1)
			go func() { firstResult <- first(firstPool) }()
			waitInitialWriterBarrier(t, ctx, pool)
			go func() { secondResult <- second(secondPool) }()
			waitIssue24ProposalBlock(t, ctx, pool)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var winner, loser outcome
			select {
			case winner = <-firstResult:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case loser = <-secondResult:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if winner.err != nil || winner.replayed {
				t.Fatal("first native decision failed", winner.err)
			}
			var pgerr *pgconn.PgError
			if errors.As(loser.err, &pgerr) {
				t.Fatal("database error escaped instead of terminal admission conflict", pgerr.Code)
			}
			if kind, ok := KindOf(loser.err); !ok || kind != ErrorAdmissionStateConflict {
				t.Fatal("loser was not an admission conflict", loser.err)
			}
			if r := first(firstPool); r.err != nil || !r.replayed {
				t.Fatal("winner exact retry changed", r.err)
			}
			var admissions, endpointReceipts, supersessionReceipts int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM admission_decisions WHERE proposal_occurrence_id=$1),(SELECT count(*) FROM canonical_endpoint_review_bindings WHERE proposal_occurrence_id=$1),(SELECT count(*) FROM supersession_review_bindings WHERE proposal_occurrence_id=$1)`, pending.ProposalOccurrenceID).Scan(&admissions, &endpointReceipts, &supersessionReceipts); err != nil {
				t.Fatal(err)
			}
			if admissions != 1 || endpointReceipts+supersessionReceipts != 1 || (mode == "endpoint_first" && endpointReceipts != 1) || (mode == "supersession_first" && supersessionReceipts != 1) {
				t.Fatal("competing decisions changed canonical authority", admissions, endpointReceipts, supersessionReceipts)
			}
		})
	}
}
func waitIssue24ProposalBlock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='issue24-lock-second' AND wait_event_type='Lock' AND query LIKE '%FROM proposal_occurrences%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("second writer did not wait on proposal before governance")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
