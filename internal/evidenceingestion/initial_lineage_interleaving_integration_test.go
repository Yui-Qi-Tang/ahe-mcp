//go:build integration

package evidenceingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationInitialRegistrationInterleavesWithNativeWriters(t *testing.T) {
	for _, mode := range []string{"ordinary_admission", "withdrawal", "source_finding", "supersession", "current_derivation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, pool := integrationPoolWithMigrations(t)
			in := registrationNativeFixture(t, ctx, pool, "interleave-c", "AHE-42")
			var writer func() error
			var table, wantError string
			switch mode {
			case "ordinary_admission":
				p := createSupersessionExternalProposal(t, ctx, pool, "interleave-ordinary", "r2", "Second admitted claim on the same object.")
				writer = func() error {
					_, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic concurrent admission"})
					return err
				}
				table = "canonical_graph_nodes"
				wantError = "singleton"
			case "withdrawal":
				decision := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: in.Review.ClaimID, Operation: "withdraw"}, "interleave-withdrawal")
				writer = func() error { _, err := ApplyGovernanceDecision(ctx, pool, decision); return err }
				table = "canonical_withdrawal_events"
				wantError = "withdrawn"
			case "source_finding":
				n, err := GetCanonicalEvidenceByID(ctx, pool, in.Review.ClaimID)
				if err != nil {
					t.Fatal(err)
				}
				sid := n.OriginProposal.SourceSnapshotID
				req := GovernanceRequest{Kind: "source_assessment", TargetID: sid, Operation: "assess", Method: "TEST APPROVAL STUB", CheckerName: "synthetic", CheckerVersion: "v1", CheckedAt: "2026-10-23T00:00:00Z", Limitations: []string{"synthetic review"}, MaterialIDs: []string{sid}, Checks: []evidenceuse.SourceCheck{{SourceSnapshotID: sid, Scope: evidenceuse.RetainedSnapshotScope, ContentHash: n.OriginProposal.RawContentHash, Criterion: evidenceuse.RetentionV1, Result: evidenceuse.CheckFail, MaterialRef: sid}}}
				decision := governanceTestDecision(t, ctx, pool, req, "interleave-finding")
				writer = func() error { _, err := ApplyGovernanceDecision(ctx, pool, decision); return err }
				table = "source_assessment_events"
				wantError = "review changed"
			case "supersession":
				p := createSupersessionExternalProposal(t, ctx, pool, "interleave-replacement", "r2", "Genuine replacement.")
				review, err := LoadSupersessionReview(ctx, pool, SupersessionReviewRequest{ProposalOccurrenceID: p.ProposalOccurrenceID, Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{in.Review.ClaimID}})
				if err != nil {
					t.Fatal(err)
				}
				decision := ReviewedSupersessionInput{RequestID: "interleave-replacement", Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic genuine replacement"}
				writer = func() error { _, err := AdmitReviewedSupersession(ctx, pool, decision); return err }
				table = "canonical_supersession_admission_events"
				wantError = "unclassified"
			case "current_derivation":
				// An independent reviewed lineage supplies eligible parents while C remains unclassified.
				old := createIntegrationClosureExternalProposal(t, ctx, pool, "interleave-b1", "INDEPENDENT-B", "r1", "Independent parent v1.")
				first, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: old.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic independent admission"})
				if err != nil {
					t.Fatal(err)
				}
				next := createIntegrationClosureExternalProposal(t, ctx, pool, "interleave-b2", "INDEPENDENT-B", "r2", "Independent parent v2.")
				basis := supersessionIntegrationBasis()
				basis.ObjectID = "INDEPENDENT-B"
				b, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{ProposalOccurrenceID: next.ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{first.CanonicalRef}, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic B replacement"})
				if err != nil {
					t.Fatal(err)
				}
				decision := governanceDerivedInput(t, ctx, pool, "interleave-derived-decision", b.CanonicalRef)
				writer = func() error { _, err := AdmitReviewedEndpoint(ctx, pool, decision); return err }
				table = "canonical_endpoint_review_bindings"
				wantError = "review changed"
			}
			review, err := LoadInitialLineageRegistrationReview(ctx, pool, in.Review)
			if err != nil {
				t.Fatal(err)
			}
			in.ExpectedSubject = review.Subject
			// The real native writer holds the governance fence before its AFTER trigger
			// reaches this independent advisory barrier. No sleeps choose commit order.
			if _, err := pool.Exec(ctx, `CREATE FUNCTION initial_registration_writer_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(7102301); RETURN NEW; END $$; CREATE TRIGGER initial_registration_writer_barrier AFTER INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION initial_registration_writer_barrier()`); err != nil {
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
			writerResult := make(chan error, 1)
			go func() { writerResult <- writer() }()
			waitInitialWriterBarrier(t, ctx, pool)
			registrationResult := make(chan error, 1)
			go func() { _, err := ApplyInitialLineageRegistration(ctx, pool, in); registrationResult <- err }()
			waitInitialRegistrationLocks(t, ctx, pool, 1)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-writerResult:
				if err != nil {
					t.Fatal("native writer failed", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-registrationResult:
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("registration did not recheck %s: %v", wantError, err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_initial_lineage_registrations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("stale registration left authority", err)
			}
		})
	}
}

func waitInitialWriterBarrier(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE a.datname=current_database() AND l.locktype='advisory' AND NOT l.granted AND l.objid::bigint=7102301`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("native writer did not reach explicit database barrier")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
