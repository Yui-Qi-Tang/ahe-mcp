//go:build integration

package mcpintegration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencereferences"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: references originates at an already reviewed-v2 claim.
// Its fresh authority FK and that v2 request's exact replay must coexist.
func TestIntegrationReferencesReviewedSupersessionReplayLockOrder(t *testing.T) {
	for _, interleaved := range []bool{false, true} {
		t.Run(fmt.Sprintf("interleaved=%t", interleaved), func(t *testing.T) {
			dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
			if dsn == "" {
				t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is required for isolated role verification")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileRelationReviewer, dbrole.ProfileSupersessionReviewer)
			fixture, input, original := referencesReviewedV2Fixture(t, ctx, f.pool, "reviewed-replay-lock")
			review, err := evidencereferences.LoadReview(ctx, f.pool, fixture.Request)
			if err != nil {
				t.Fatal(err)
			}
			referenceInput := evidencereferences.AdmissionInput{RequestID: "reviewed-replay-reference", Review: fixture.Request, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic reference"}
			open := func(login authorityProcessLogin, profile dbrole.Profile) (*pgxpool.Pool, int32) {
				t.Helper()
				cfg, err := pgxpool.ParseConfig(login.dsn)
				if err != nil {
					t.Fatal("invalid test runtime configuration")
				}
				cfg.MaxConns = 1
				p, _, err := dbrole.OpenRuntimePool(ctx, cfg, dbrole.RuntimePoolInput{Role: login.group, Schema: f.schema, Profile: profile})
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
			refs, referencePID := open(f.relations, dbrole.ProfileRelationReviewer)
			supersession, _ := open(f.supersession, dbrole.ProfileSupersessionReviewer)
			readAuthority := func() string {
				t.Helper()
				var state string
				if err := f.pool.QueryRow(ctx, `SELECT jsonb_build_object(
     'head',(SELECT to_jsonb(h) FROM canonical_supersession_admission_head h),
     'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY revision) FROM canonical_supersession_admission_events e),
     'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY event_id) FROM supersession_review_bindings r),
     'decisions',(SELECT jsonb_agg(to_jsonb(d) ORDER BY admission_decision_id) FROM admission_decisions d))::text`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				return state
			}
			before := readAuthority()
			replay := func(in evidenceingestion.ReviewedSupersessionInput) (evidenceingestion.SupersessionReviewReceipt, error) {
				// A blocked replay must fail before the references barrier is released.
				replayCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				defer stop()
				return evidenceingestion.AdmitReviewedSupersession(replayCtx, supersession, in)
			}
			assertReplay := func() {
				t.Helper()
				got, err := replay(input)
				want := original
				want.Admission.Replayed = true
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("original v2 retry blocked or changed receipt", err)
				}
			}
			type referenceOutcome struct {
				result evidencereferences.AdmissionResult
				err    error
			}
			referenceCall := func() referenceOutcome {
				r, err := evidencereferences.AdmitReviewed(ctx, refs, referenceInput)
				return referenceOutcome{r, err}
			}
			var admitted referenceOutcome
			if interleaved {
				blocker, err := f.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
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
				// References has taken governance but not its origin-proposal FK when
				// it reaches this real INSERT. No function/trigger body is replaced.
				if _, err := blocker.Exec(ctx, `LOCK TABLE canonical_references_admissions IN SHARE MODE`); err != nil {
					t.Fatal(err)
				}
				done := make(chan referenceOutcome, 1)
				workers.Go(func() { done <- referenceCall() })
				waitReferenceWriterLock(t, ctx, f.pool, referencePID, blockerPID, "INSERT INTO canonical_references_admissions")
				// Before starting replay, references is the only writer. Verify it
				// really holds the fence rather than merely reaching the INSERT.
				_, err = f.pool.Exec(ctx, `SELECT singleton FROM evidence_governance_head WHERE singleton FOR UPDATE NOWAIT`)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
					t.Fatal("references did not retain governance at the barrier", err)
				}
				assertReplay()
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case admitted = <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			} else {
				assertReplay()
				admitted = referenceCall()
			}
			if admitted.err != nil || admitted.result.Replayed || admitted.result.Edge.From != original.Admission.CanonicalRef {
				t.Fatal("fresh references admission failed", admitted.err)
			}
			assertReplay()
			referenceRetry := referenceCall()
			wantReference := admitted.result
			wantReference.Replayed = true
			if referenceRetry.err != nil || !reflect.DeepEqual(referenceRetry.result, wantReference) {
				t.Fatal("references receipt changed", referenceRetry.err)
			}

			// Removing the historical fence must not weaken any exact retry binding.
			for _, field := range []string{"request_id", "subject", "reviewer", "reason", "targets"} {
				t.Run("changed_"+field, func(t *testing.T) {
					changed := input
					switch field {
					case "request_id":
						changed.RequestID += "-changed"
					case "subject":
						changed.ExpectedSubject += "-changed"
					case "reviewer":
						changed.ReviewerID += "-changed"
					case "reason":
						changed.DecisionReason += "-changed"
					case "targets":
						changed.Review.TargetNodeIDs = []string{fixture.To.NodeIDs[0]}
					}
					if _, err := replay(changed); err == nil {
						t.Fatal("changed original request was accepted")
					} else if kind, ok := evidenceingestion.KindOf(err); !ok || kind != evidenceingestion.ErrorSupersessionReplayConflict {
						t.Fatal("expected exact replay conflict", err)
					}
				})
			}
			if after := readAuthority(); after != before {
				t.Fatal("references/replay changed saved v2 receipts, event, decision or head")
			}
		})
	}
}

func referencesReviewedV2Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) (canonicalReferencesFixture, evidenceingestion.ReviewedSupersessionInput, evidenceingestion.SupersessionReviewReceipt) {
	t.Helper()
	object := "references-origin-" + suffix
	initial := canonicalReferencesExternalVersion(t, ctx, pool, object, "v1", "[[ahe-anchor:initial]] Initial synthetic rule.\n")
	initial.Proposals = canonicalReferencesExtract(t, ctx, pool, initial, suffix+"-initial", []evidenceingestion.ExtractorProposalOutput{
		{ProposalLocalID: "initial", StatementText: initial.Intake.Spans[0].QuotedText, EvidenceRefs: []string{initial.Intake.Spans[0].SpanID}},
	})
	old, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: initial.Proposals[0].ProposalOccurrenceID,
		DecisionBy: "test-stub:ordinary-origin", DecisionReason: "APPROVE STUB: create the original source materializer."})
	if err != nil {
		t.Fatal(err)
	}
	initial.NodeIDs = []string{old.CanonicalRef}
	// A fixed external target can itself cite v1. The v2 source is subsequently
	// authored to reference this already captured target; no hash cycle exists.
	targetToken := "[[ahe-ref:" + initial.Intake.SourceSnapshotID + "#initial]]"
	target := canonicalReferencesCapture(t, ctx, pool, suffix+"-external-target", "[[ahe-anchor:source]] External target cites the fixed original "+targetToken+"\n", true)
	target = canonicalReferencesAdmitSource(t, ctx, pool, target, suffix+"-external-target", 0)
	token := "[[ahe-ref:" + target.Intake.SourceSnapshotID + "#source]]"
	replacement := canonicalReferencesExternalVersion(t, ctx, pool, object, "v2", "[[ahe-anchor:replacement]] Replacement rule explicitly cites "+token+"\n")
	replacement.Proposals = canonicalReferencesExtract(t, ctx, pool, replacement, suffix+"-replacement", []evidenceingestion.ExtractorProposalOutput{
		{ProposalLocalID: "replacement", StatementText: replacement.Intake.Spans[0].QuotedText, EvidenceRefs: []string{replacement.Intake.Spans[0].SpanID}},
	})
	basis := evidenceingestion.SupersessionLineageBasis{SourceSystem: "lab", SourceNamespace: "canonical-references-synthetic", ObjectType: "document", ObjectID: object, SlotKind: "field", SlotID: "rule"}
	review, err := evidenceingestion.LoadSupersessionReview(ctx, pool, evidenceingestion.SupersessionReviewRequest{ProposalOccurrenceID: replacement.Proposals[0].ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{old.CanonicalRef}})
	if err != nil {
		t.Fatal(err)
	}
	input := evidenceingestion.ReviewedSupersessionInput{RequestID: "reviewed-origin", Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic reviewed replacement"}
	current, err := evidenceingestion.AdmitReviewedSupersession(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	replacement.NodeIDs = []string{current.Admission.CanonicalRef}
	request := canonicalReferencesRequest(t, replacement, target, 0, 0, 0, 0, token, "source", evidencereferences.ResolverNativeSnapshotV1)
	return canonicalReferencesFixture{From: replacement, To: target, Request: request}, input, current
}
