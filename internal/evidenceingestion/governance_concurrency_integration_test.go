//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: all sources, approvals and synchronization triggers live
// only in the disposable schema. Actual native/domain writers still perform
// their validation, mutations, retries and commits; the trigger only pauses.
func governanceDerivedInput(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label, parent string) ReviewedEndpointAdmissionInput {
	t.Helper()
	p := createIntegrationClosureExternalProposal(t, ctx, pool, label, label, "r1", "Synthetic AND conclusion "+label+".")
	req := EndpointReviewRequest{Kind: "derived_spec", ProposalOccurrenceID: p.ProposalOccurrenceID,
		Derivation: &EndpointDerivation{UsePolicy: evidenceuse.CurrentActionV1, ParentNodeIDs: []string{parent}, Method: "synthetic AND", Producer: "TEST APPROVAL STUB", TraceRef: "synthetic:" + label}}
	r, err := LoadEndpointReview(ctx, pool, req)
	if err != nil {
		t.Fatal(err)
	}
	return ReviewedEndpointAdmissionInput{RequestID: label, Review: req, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
}

func governanceRacePool(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) (*pgxpool.Pool, int32) {
	t.Helper()
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var pid int32
	if err := p.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return p, pid
}

func governanceWaitBlocked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, waiter, blocker int32) {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(deadline, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, blocker, waiter).Scan(&blocked); err != nil {
			t.Fatal("observing real transaction blocking", err)
		}
		if blocked {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatal("transaction did not reach the expected lock boundary")
		case <-tick.C:
		}
	}
}

func TestIntegrationGovernanceConcurrentNativeWritersAndDerivedCommit(t *testing.T) {
	for _, kind := range []string{"withdrawal", "source_correction", "own_source_correction", "contradiction", "supersession", "classification_and_source_binding"} {
		for _, writerFirst := range []bool{true, false} {
			order := "derived_first"
			if writerFirst {
				order = "governance_first"
			}
			t.Run(kind+"/"+order, func(t *testing.T) {
				base, pool := integrationPoolWithMigrations(t)
				ctx, cancel := context.WithCancel(base)
				defer cancel()
				_, parent := prepareIntegrationClosurePair(t, ctx, pool, "race-parent")
				// An intermediate AND node makes parent governance an ancestor
				// dependency, rather than only a direct-output-node check.
				middleInput := governanceDerivedInput(t, ctx, pool, "race-middle", parent.CanonicalRef)
				middle, err := AdmitReviewedEndpoint(ctx, pool, middleInput)
				if err != nil {
					t.Fatal(err)
				}
				derivedInput := governanceDerivedInput(t, ctx, pool, "race-output", middle.Admission.CanonicalRef)
				parentRecord, err := GetCanonicalEvidenceByID(ctx, pool, parent.CanonicalRef)
				if err != nil {
					t.Fatal(err)
				}
				var write func(context.Context, *pgxpool.Pool) error
				table := "canonical_graph_nodes"
				switch kind {
				case "withdrawal":
					table = "canonical_withdrawal_events"
					decision := governanceTestDecision(t, ctx, pool, GovernanceRequest{Kind: "withdrawal", TargetID: parent.CanonicalRef, Operation: "withdraw"}, "race-withdraw")
					write = func(ctx context.Context, p *pgxpool.Pool) error {
						_, err := ApplyGovernanceDecision(ctx, p, decision)
						return err
					}
				case "source_correction", "own_source_correction":
					table = "source_assessment_events"
					sid := parentRecord.OriginProposal.SourceSnapshotID
					if kind == "own_source_correction" {
						p, err := TraceProposalProvenance(ctx, pool, derivedInput.Review.ProposalOccurrenceID)
						if err != nil {
							t.Fatal(err)
						}
						sid = p.SourceSnapshotID
					}
					prior := governancePassSource(t, ctx, pool, sid, "race-prior-pass")
					req := prior.Display.Request
					req.ExpectedRevision, req.PreviousEventID = prior.Revision, prior.EventID
					req.ResolvesEventIDs = []string{prior.EventID}
					for i := range req.Checks {
						req.Checks[i].Result = evidenceuse.CheckFail
					}
					decision := governanceTestDecision(t, ctx, pool, req, "race-source-correction")
					write = func(ctx context.Context, p *pgxpool.Pool) error {
						_, err := ApplyGovernanceDecision(ctx, p, decision)
						return err
					}
				case "contradiction":
					table = "canonical_graph_edges"
					pair, err := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{RequestID: "race-conflict", NodeAID: parent.CanonicalRef, NodeBID: middle.Admission.CanonicalRef,
						Rationale: "TEST APPROVAL STUB: explicit synthetic pair", ProducerName: "synthetic", ProducerVersion: "v1", ProducerSessionRef: "synthetic:race"})
					if err != nil {
						t.Fatal(err)
					}
					in := CanonicalContradictionAdmissionInput{ProposalID: pair.Proposal.ID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "TEST APPROVAL STUB"}
					write = func(ctx context.Context, p *pgxpool.Pool) error {
						_, err := AdmitPendingCanonicalContradiction(ctx, p, in)
						return err
					}
				case "supersession":
					proposal := createSupersessionExternalProposal(t, ctx, pool, "race-replacement", "r3", "Synthetic replacement policy.")
					req := SupersessionReviewRequest{ProposalOccurrenceID: proposal.ProposalOccurrenceID, Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{parent.CanonicalRef}, ExpectedRevision: parent.EventRevision, ExpectedHeadEventID: parent.AdmissionEventID}
					r, err := LoadSupersessionReview(ctx, pool, req)
					if err != nil {
						t.Fatal(err)
					}
					in := ReviewedSupersessionInput{RequestID: "race-replace", Review: req, ExpectedSubject: r.Subject, Decision: "approved", DecisionReason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"}
					write = func(ctx context.Context, p *pgxpool.Pool) error {
						_, err := AdmitReviewedSupersession(ctx, p, in)
						return err
					}
				case "classification_and_source_binding":
					// Existing source bindings are immutable. A real new ordinary
					// admission binds another snapshot of the same object and makes
					// the previously complete lineage classification incomplete.
					proposal := createSupersessionExternalProposal(t, ctx, pool, "race-unclassified", "r3", "Synthetic unclassified statement on the same object.")
					snapshot, err := LoadReviewableSourceClaimReviewSnapshot(ctx, pool, proposal.ExtractionAttemptID, proposal.ProposalOccurrenceID)
					if err != nil {
						t.Fatal(err)
					}
					_, subject, err := BuildSourceClaimReviewDisplayArtifact(snapshot)
					if err != nil {
						t.Fatal(err)
					}
					in := ReviewedSourceClaimAdmissionInput{ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: subject, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "TEST APPROVAL STUB"}
					write = func(ctx context.Context, p *pgxpool.Pool) error {
						_, err := AdmitReviewedSourceClaim(ctx, p, in)
						return err
					}
				}
				// Fixture preparation may add positive source assessments; review
				// the final pre-race material exactly once after all preparation.
				r, err := LoadEndpointReview(ctx, pool, derivedInput.Review)
				if err != nil {
					t.Fatal(err)
				}
				derivedInput.ExpectedSubject = r.Subject
				writer, writerPID := governanceRacePool(t, ctx, pool, "issue18-governance-writer")
				derived, derivedPID := governanceRacePool(t, ctx, pool, "issue18-derived-writer")
				blocker, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var workers sync.WaitGroup
				defer func() { _ = blocker.Rollback(context.Background()); cancel(); workers.Wait() }()
				key := derivationAdmissionLockKey
				if writerFirst {
					key = 180058
					_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION issue18_pause_writer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('application_name')='issue18-governance-writer' THEN PERFORM pg_advisory_xact_lock(%d); END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER issue18_pause_writer AFTER INSERT ON %s FOR EACH STATEMENT EXECUTE FUNCTION issue18_pause_writer()`, key, pgx.Identifier{table}.Sanitize()))
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
					t.Fatal(err)
				}
				writerDone := make(chan error, 1)
				type derivedResult struct {
					receipt EndpointAdmissionReceipt
					err     error
				}
				derivedDone := make(chan derivedResult, 1)
				startWriter := func() { workers.Go(func() { writerDone <- write(ctx, writer) }) }
				startDerived := func() {
					workers.Go(func() {
						out, err := AdmitReviewedEndpoint(ctx, derived, derivedInput)
						derivedDone <- derivedResult{out, err}
					})
				}
				if writerFirst {
					startWriter()
					governanceWaitBlocked(t, ctx, pool, writerPID, int32(blocker.Conn().PgConn().PID()))
					startDerived()
					governanceWaitBlocked(t, ctx, pool, derivedPID, writerPID)
				} else {
					startDerived()
					governanceWaitBlocked(t, ctx, pool, derivedPID, int32(blocker.Conn().PgConn().PID()))
					startWriter()
					governanceWaitBlocked(t, ctx, pool, writerPID, derivedPID)
				}
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-writerDone; err != nil {
					t.Fatal("real governance writer failed", err)
				}
				result := <-derivedDone
				if writerFirst {
					if result.err == nil {
						t.Fatal("obsolete reviewed basis admitted after governance commit")
					}
					if kind, ok := KindOf(result.err); !ok || kind != ErrorReviewContractConflict {
						t.Fatal("race failed for an unexpected reason instead of stale review", result.err)
					}
					var nodes, receipts int
					if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM canonical_graph_nodes WHERE origin_proposal_occurrence_id=$1), (SELECT count(*) FROM canonical_endpoint_review_bindings WHERE proposal_occurrence_id=$1)`, derivedInput.Review.ProposalOccurrenceID).Scan(&nodes, &receipts); err != nil || nodes != 0 || receipts != 0 {
						t.Fatal("rejected derived transaction left authority", nodes, receipts, err)
					}
					// A fresh subject after the competing commit must also fail
					// the semantic gate, not merely the earlier RR/CAS check.
					fresh, err := LoadEndpointReview(ctx, pool, derivedInput.Review)
					if err != nil {
						t.Fatal(err)
					}
					derivedInput.ExpectedSubject = fresh.Subject
					if _, err := AdmitReviewedEndpoint(ctx, pool, derivedInput); err == nil || !strings.HasPrefix(err.Error(), "current-action governance rejected basis:") {
						t.Fatal("fresh review bypassed semantic current-use gate", err)
					}
				} else {
					if result.err != nil {
						t.Fatal("eligible earlier derived commit failed", result.err)
					}
					plan, err := CheckCurrentEvidencePlan(ctx, pool, evidenceuse.CurrentActionV1, []evidenceuse.ActionBasis{{ActionID: "later-external-action", BasisIDs: []string{result.receipt.Admission.CanonicalRef}}})
					if err != nil || plan.Accepted {
						t.Fatal("historical commit treated as a later-use permit", err)
					}
					replay, err := AdmitReviewedEndpoint(ctx, pool, derivedInput)
					if err != nil || !replay.Admission.Replayed {
						t.Fatal("historical exact receipt lost", err)
					}
				}
			})
		}
	}
}
