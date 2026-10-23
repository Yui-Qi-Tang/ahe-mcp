//go:build integration

package mcpintegration

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencereferences"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TEST APPROVAL STUB: exact reference tokens and independently reviewed v2
// replacements share a canonical target. Exercise the actual bounded roles;
// only the retained internal native v2 call uses the fixture owner pool.
func TestIntegrationReferencesSupersessionLockOrder(t *testing.T) {
	for _, route := range []string{"native", "reviewed"} {
		for _, referencesFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/references_first=%t", route, referencesFirst), func(t *testing.T) {
				dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
				if dsn == "" {
					t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is required for isolated role verification")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
				defer cancel()
				f := newAuthorityProcessFixture(t, ctx, dsn, dbrole.ProfileSourceClaimReviewer, dbrole.ProfileRelationReviewer, dbrole.ProfileSupersessionReviewer)
				target := canonicalReferencesCrossSnapshot(t, ctx, f.pool, "lock-reference", true)
				raw := "[[ahe-anchor:target]] Synthetic replacement target.\n"
				captured, err := evidenceingestion.CaptureExternalSource(ctx, f.pool, evidenceingestion.ExternalSourceEnvelopeV1{
					SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: "lock-replacement-source",
					SourceSystem: "lab", SourceNamespace: "canonical-references-synthetic", ObjectType: "document", ObjectID: "lock-reference-target", Revision: "v2",
					SourceLocation: "https://example.invalid/references/lock-reference-target", Title: "Synthetic explicit-reference replacement",
					ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
					Content: raw, Coverage: evidenceingestion.ExternalSourceCoverageExactExcerpt, Limitations: []string{"Synthetic lock-order fixture."},
					CollectorID: "TEST APPROVAL STUB", ConnectorID: "synthetic", ObservedAt: "2026-10-23T00:00:00Z",
				})
				if err != nil {
					t.Fatal(err)
				}
				source := canonicalReferencesSource{Intake: captured.SourceIntakeResult, Raw: raw}
				pending := canonicalReferencesExtract(t, ctx, f.pool, source, "lock-new", []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "new", StatementText: captured.Spans[0].QuotedText, EvidenceRefs: []string{captured.Spans[0].SpanID}}})[0]
				basis := evidenceingestion.SupersessionLineageBasis{SourceSystem: "lab", SourceNamespace: "canonical-references-synthetic", ObjectType: "document", ObjectID: "lock-reference-target", SlotKind: "field", SlotID: "body"}
				sr, err := evidenceingestion.LoadSupersessionReview(ctx, f.pool, evidenceingestion.SupersessionReviewRequest{ProposalOccurrenceID: pending.ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{target.To.NodeIDs[0]}})
				if err != nil {
					t.Fatal(err)
				}
				rr, err := evidencereferences.LoadReview(ctx, f.pool, target.Request)
				if err != nil {
					t.Fatal(err)
				}
				ri := evidencereferences.AdmissionInput{RequestID: "lock-reference", Review: target.Request, ExpectedSubject: rr.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic navigation review"}
				si := evidenceingestion.ReviewedSupersessionInput{RequestID: "lock-supersession", Review: sr.Display.Request, ExpectedSubject: sr.Subject, Decision: "approved", ReviewerID: "TEST APPROVAL STUB", DecisionReason: "synthetic replacement review"}
				ni := evidenceingestion.SupersessionAdmissionInput{ProposalOccurrenceID: pending.ProposalOccurrenceID, Basis: basis, TargetNodeIDs: []string{target.To.NodeIDs[0]}, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic native replacement"}
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
				var supersession *pgxpool.Pool
				var supersessionPID int32
				if route == "reviewed" {
					supersession, supersessionPID = open(f.supersession, dbrole.ProfileSupersessionReviewer)
				} else {
					cfg := f.pool.Config().Copy()
					cfg.MaxConns = 1
					supersession, err = pgxpool.NewWithConfig(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(supersession.Close)
					if err := supersession.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&supersessionPID); err != nil {
						t.Fatal(err)
					}
				}
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
				// Either first writer reaches an edge INSERT after its governance
				// and endpoint locks. Do not replace any function or trigger body.
				if _, err := blocker.Exec(ctx, `LOCK TABLE canonical_graph_edges IN SHARE MODE`); err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					reference evidencereferences.AdmissionResult
					admission evidenceingestion.SupersessionAdmissionResult
					receipt   evidenceingestion.SupersessionReviewReceipt
					err       error
				}
				referenceCall := func() outcome {
					r, err := evidencereferences.AdmitReviewed(ctx, refs, ri)
					return outcome{reference: r, err: err}
				}
				supersessionCall := func() outcome {
					if route == "reviewed" {
						r, err := evidenceingestion.AdmitReviewedSupersession(ctx, supersession, si)
						return outcome{admission: r.Admission, receipt: r, err: err}
					}
					r, err := evidenceingestion.AdmitPendingSupersession(ctx, supersession, ni)
					return outcome{admission: r, err: err}
				}
				first, second := referenceCall, supersessionCall
				firstPID, secondPID := referencePID, supersessionPID
				if !referencesFirst {
					first, second = supersessionCall, referenceCall
					firstPID, secondPID = supersessionPID, referencePID
				}
				start := func(call func() outcome) <-chan outcome {
					ch := make(chan outcome, 1)
					workers.Go(func() { ch <- call() })
					return ch
				}
				a := start(first)
				waitReferenceWriterLock(t, ctx, f.pool, firstPID, blockerPID, "INSERT INTO canonical_graph_edges")
				b := start(second)
				waitReferenceWriterLock(t, ctx, f.pool, secondPID, firstPID, "SELECT evidence_governance_lock_v1")
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				receive := func(ch <-chan outcome) outcome {
					t.Helper()
					select {
					case r := <-ch:
						if r.err != nil || r.reference.Replayed || r.admission.Replayed {
							t.Fatal("fresh cross-writer admission failed", r.err)
						}
						return r
					case <-ctx.Done():
						t.Fatal(ctx.Err())
						return outcome{}
					}
				}
				ref, sup := receive(a), receive(b)
				if !referencesFirst {
					ref, sup = sup, ref
				}
				if sup.admission.EventRevision != 1 || ref.reference.Edge.From != target.From.NodeIDs[0] || ref.reference.Edge.To != target.To.NodeIDs[0] {
					t.Fatal("cross-writer authority changed its target or head")
				}
				rrAgain := referenceCall()
				wantRef := ref.reference
				wantRef.Replayed = true
				if rrAgain.err != nil || !reflect.DeepEqual(rrAgain.reference, wantRef) {
					t.Fatal("original references receipt changed", rrAgain.err)
				}
				srAgain := supersessionCall()
				wantSup := sup.admission
				wantSup.Replayed = true
				wantReceipt := sup.receipt
				wantReceipt.Admission.Replayed = true
				if srAgain.err != nil || !reflect.DeepEqual(srAgain.admission, wantSup) || (route == "reviewed" && !reflect.DeepEqual(srAgain.receipt, wantReceipt)) {
					t.Fatal("original v2 receipt changed", srAgain.err)
				}
				current, err := evidenceingestion.GetCanonicalSupersessionCurrentness(ctx, f.pool, sup.admission.LineageKey)
				if err != nil || current.Projection == nil || current.Projection.Witness == nil || !reflect.DeepEqual(current.Projection.FrontierNodeIDs, []string{sup.admission.CanonicalRef}) {
					t.Fatal("references altered v2 closure", err)
				}
			})
		}
	}
}

func waitReferenceWriterLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, waiter, blocker int32, statement string) {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		var query string
		if err := pool.QueryRow(deadline, `SELECT $1::int=ANY(pg_blocking_pids(pid)),query FROM pg_stat_activity WHERE pid=$2`, blocker, waiter).Scan(&blocked, &query); err != nil {
			t.Fatal("observing writer lock", err)
		}
		if blocked && strings.Contains(query, statement) {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatalf("writer did not wait at %s behind expected transaction; last query: %s", statement, query)
		case <-ticker.C:
		}
	}
}
