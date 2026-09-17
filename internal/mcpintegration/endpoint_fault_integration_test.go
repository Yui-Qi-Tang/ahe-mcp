//go:build integration

package mcpintegration

import (
	"context"
	"errors"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpendpoints"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The helper retains a real row lock without canonical UPDATE privilege.
func verifyEndpointLockAndRollback(t *testing.T, ctx context.Context, f authorityProcessFixture, endpoints *authorityProcess, input evidenceingestion.ReviewedEndpointAdmissionInput) {
	t.Helper()
	config, err := pgxpool.ParseConfig(f.endpoints.dsn)
	if err != nil {
		t.Fatal("invalid synthetic endpoint role")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: f.endpoints.group, Schema: f.schema, Profile: dbrole.ProfileEndpointReviewer})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	var locked string
	if err := lock.QueryRow(ctx, "SELECT canonical_endpoint_lock_nodes_v1($1::text[])", []string{input.Review.Derivation.ParentNodeIDs[0]}).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	other, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, "SET LOCAL lock_timeout='100ms'"); err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(ctx, "SELECT canonical_node_id FROM canonical_graph_nodes WHERE canonical_node_id=$1 FOR UPDATE", locked)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
		t.Fatal("endpoint helper did not retain native row lock")
	}
	_ = other.Rollback(ctx)
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, "UPDATE canonical_graph_nodes SET canonical_node_id=canonical_node_id WHERE false")
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatal("endpoint reviewer obtained canonical UPDATE")
	}
	_, err = pool.Exec(ctx, "SELECT canonical_endpoint_lock_nodes_v1($1::text[])", make([]string, 10))
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatal("unbounded lock request accepted")
	}
	before := relationCounts(t, ctx, f.pool)
	// Only the disposable test schema receives this temporary fault injector.
	_, err = f.pool.Exec(ctx, `BEGIN;
 CREATE FUNCTION endpoint_test_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='synthetic endpoint commit failure'; END $$;
 REVOKE EXECUTE ON FUNCTION endpoint_test_commit_failure() FROM PUBLIC;
 CREATE CONSTRAINT TRIGGER endpoint_test_commit_failure AFTER INSERT ON canonical_endpoint_review_bindings
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION endpoint_test_commit_failure();
 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	relationRejected(t, endpoints, mcpendpoints.ToolAdmit, input)
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("failed endpoint commit left canonical or decision rows")
	}
	var bindings int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM canonical_endpoint_review_bindings").Scan(&bindings); err != nil || bindings != 0 {
		t.Fatal("failed commit retained endpoint receipt")
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER endpoint_test_commit_failure ON canonical_endpoint_review_bindings; DROP FUNCTION endpoint_test_commit_failure();"); err != nil {
		t.Fatal(err)
	}
}
