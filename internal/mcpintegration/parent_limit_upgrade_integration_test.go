//go:build integration

package mcpintegration

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcprelations"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

func assertParentLimitRollbackRefused(t *testing.T, ctx context.Context, f authorityProcessFixture) {
	t.Helper()
	down, err := os.ReadFile("../../migrations/000057_reviewed_derivation_parent_limit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := relationCounts(t, ctx, f.pool)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, string(down))
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" || pgerr.Message != "migration 57 rollback refuses reviewed history beyond schema 56 bounds" {
		t.Fatalf("rollback over wide review history: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if relationCounts(t, ctx, f.pool) != before {
		t.Fatal("refused rollback changed native history")
	}
	if _, err := migrations.VerifyCurrentInSchema(ctx, f.pool, f.schema); err != nil {
		t.Fatal(err)
	}
}

func verifyParentLimitUpgradeRetainsRelation(t *testing.T, ctx context.Context, f authorityProcessFixture, relations *authorityProcess, input mcprelations.ImplementsAdmissionRequest) {
	t.Helper()
	replayBefore := relationTool[map[string]any](t, relations, mcprelations.ToolAdmitReviewedImplements, input)
	var historyBefore string
	if err := f.pool.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY request_id),'[]'::jsonb)::text FROM canonical_implements_admissions a").Scan(&historyBefore); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000057_reviewed_derivation_parent_limit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	// Exercise the parent-limit DDL round trip without removing the later
	// governance migration or its runtime grants. Full native upgrade coverage
	// is owned by the migrations suite.
	up, err := os.ReadFile("../../migrations/000057_reviewed_derivation_parent_limit.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Runtime requests resume only after the native schema is upgraded again.
	if changed, err := migrations.ApplyUpInSchema(ctx, f.pool, f.schema); err != nil || changed {
		t.Fatalf("history-preserving upgrade57: changed=%v err=%v", changed, err)
	}
	if _, err := migrations.VerifyCurrentInSchema(ctx, f.pool, f.schema); err != nil {
		t.Fatal(err)
	}
	var historyAfter string
	if err := f.pool.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY request_id),'[]'::jsonb)::text FROM canonical_implements_admissions a").Scan(&historyAfter); err != nil || historyAfter != historyBefore {
		t.Fatal("upgrade57 changed existing implements receipt/history")
	}
	replayAfter := relationTool[map[string]any](t, relations, mcprelations.ToolAdmitReviewedImplements, input)
	if !reflect.DeepEqual(replayBefore, replayAfter) {
		t.Fatal("upgrade57 changed exact implements replay")
	}
}
