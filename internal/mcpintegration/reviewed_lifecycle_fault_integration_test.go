//go:build integration

package mcpintegration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpsupersession"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lifecyclePool(t *testing.T, ctx context.Context, f authorityProcessFixture, login authorityProcessLogin) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(login.dsn)
	if err != nil {
		t.Fatal("invalid synthetic runtime")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, cfg, dbrole.RuntimePoolInput{Role: login.group, Schema: f.schema, Profile: login.profile})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func verifyLifecycleFaults(t *testing.T, ctx context.Context, f authorityProcessFixture, endpoints, replacements *authorityProcess, req evidenceingestion.EndpointReviewRequest, sup evidenceingestion.ReviewedSupersessionInput) {
	t.Helper()
	ep := lifecyclePool(t, ctx, f, f.endpoints)
	sp := lifecyclePool(t, ctx, f, f.supersession)
	var pgerr *pgconn.PgError
	for _, pool := range []*pgxpool.Pool{ep, sp} {
		for _, q := range []string{"UPDATE canonical_graph_nodes SET node_kind=node_kind WHERE false", "SELECT canonical_supersession_assert_event('denied')"} {
			_, err := pool.Exec(ctx, q)
			if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
				t.Fatal("restricted role gained canonical UPDATE or internal assertion EXECUTE")
			}
		}
	}
	_, err := sp.Exec(ctx, "SELECT canonical_supersession_lock_targets_v1($1::text[])", make([]string, 65))
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatal("unbounded target lock accepted")
	}
	lock, err := sp.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var locked string
	if err = lock.QueryRow(ctx, "SELECT canonical_supersession_lock_targets_v1($1::text[])", sup.Review.TargetNodeIDs).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	other, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec(ctx, "SET LOCAL lock_timeout='100ms'"); err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(ctx, "SELECT canonical_node_id FROM canonical_graph_nodes WHERE canonical_node_id=$1 FOR UPDATE", locked)
	if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
		t.Fatal("replacement helper did not retain native target lock")
	}
	_ = other.Rollback(ctx)
	_ = lock.Rollback(ctx)

	beforeReplacement := relationCounts(t, ctx, f.pool)
	var headRevision int64
	if err = f.pool.QueryRow(ctx, "SELECT revision FROM canonical_supersession_admission_head").Scan(&headRevision); err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(ctx, `CREATE FUNCTION lifecycle_supersession_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='synthetic replacement commit failure'; END $$;
    REVOKE EXECUTE ON FUNCTION lifecycle_supersession_commit_failure() FROM PUBLIC;
    CREATE CONSTRAINT TRIGGER lifecycle_supersession_commit_failure AFTER INSERT ON supersession_review_bindings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lifecycle_supersession_commit_failure();`)
	if err != nil {
		t.Fatal(err)
	}
	relationRejected(t, replacements, mcpsupersession.ToolAdmit, sup)
	if relationCounts(t, ctx, f.pool) != beforeReplacement {
		t.Fatal("failed replacement commit retained canonical rows")
	}
	var afterRevision int64
	if err = f.pool.QueryRow(ctx, "SELECT revision FROM canonical_supersession_admission_head").Scan(&afterRevision); err != nil || afterRevision != headRevision {
		t.Fatal("failed replacement commit advanced head")
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER lifecycle_supersession_commit_failure ON supersession_review_bindings; DROP FUNCTION lifecycle_supersession_commit_failure()"); err != nil {
		t.Fatal(err)
	}
	committed := relationTool[evidenceingestion.SupersessionReviewReceipt](t, replacements, mcpsupersession.ToolAdmit, sup)
	if committed.Admission.EventRevision != headRevision+1 {
		t.Fatal("unchanged retry after rollback failed")
	}
	// The fault/retry above superseded the original race parent. Review this
	// terminal-state race against the now-current replacement so admission and
	// disposition are both valid contenders under the current-use contract.
	derivation := *req.Derivation
	derivation.ParentNodeIDs = []string{committed.Admission.CanonicalRef}
	req.Derivation = &derivation
	review := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
	input := evidenceingestion.ReviewedEndpointAdmissionInput{RequestID: "fault-then-race", Review: req, ExpectedSubject: review.Subject, Decision: "reject", DecisionReason: "TEST APPROVAL STUB: synthetic fault and terminal race."}
	before := relationCounts(t, ctx, f.pool)
	// The disposable schema alone gets a commit-time failure injector.
	_, err = f.pool.Exec(ctx, `CREATE FUNCTION lifecycle_test_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='synthetic disposition commit failure'; END $$;
 REVOKE EXECUTE ON FUNCTION lifecycle_test_commit_failure() FROM PUBLIC;
 CREATE CONSTRAINT TRIGGER lifecycle_test_commit_failure AFTER INSERT ON endpoint_disposition_review_bindings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lifecycle_test_commit_failure();`)
	if err != nil {
		t.Fatal(err)
	}
	relationRejected(t, endpoints, mcpendpoints.ToolDisposition, input)
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("failed disposition commit retained decision or canonical evidence")
	}
	var reserved int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM endpoint_review_requests WHERE request_id=$1", input.RequestID).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatal("failed commit retained request reservation")
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER lifecycle_test_commit_failure ON endpoint_disposition_review_bindings; DROP FUNCTION lifecycle_test_commit_failure()"); err != nil {
		t.Fatal(err)
	}
	input.ReviewerID = "mock:protected:endpoint-reviewer"
	approved := input
	approved.Decision = "approved"
	approved.RequestID = "concurrent-admission"
	start := make(chan struct{})
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := evidenceingestion.RecordReviewedEndpointDisposition(ctx, ep, input)
		outcomes <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := evidenceingestion.AdmitReviewedEndpoint(ctx, ep, approved)
		outcomes <- err
	}()
	close(start)
	wg.Wait()
	close(outcomes)
	success, conflict := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else {
			var domain *evidenceingestion.DomainError
			if !errors.As(err, &domain) || domain.Kind != evidenceingestion.ErrorAdmissionStateConflict {
				t.Fatalf("unexpected competing decision error: %v", err)
			}
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("competing decisions did not produce one terminal result")
	}
	after := relationTool[evidenceingestion.EndpointReview](t, endpoints, mcpendpoints.ToolReview, req)
	if after.Lifecycle.Mode != "exact_replay_only" {
		t.Fatal("concurrent terminal result lost receipt")
	}
}
