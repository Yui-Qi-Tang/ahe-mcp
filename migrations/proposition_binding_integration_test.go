//go:build integration

package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationPropositionBindingMigrationRollbackAndReadiness(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rollbackExternalChecksForTest(t, ctx, pool)
	down, err := os.ReadFile("000050_evidence_ingestion_proposition_bindings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, propositionBindingMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCurrent(ctx, pool); err == nil {
		t.Fatal("missing binding migration passed readiness")
	}
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if status, err := VerifyCurrent(ctx, pool); err != nil || status.AppliedMigrations != 55 {
		t.Fatalf("reapply=%+v %v", status, err)
	}
	// A broken uniqueness guard would permit two events claiming one revision.
	if _, err := pool.Exec(ctx, `ALTER TABLE canonical_proposition_binding_events DROP CONSTRAINT proposition_binding_event_revision_uq`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCurrent(ctx, pool); err == nil {
		t.Fatal("missing event revision uniqueness passed readiness")
	}
}

func rollbackPropositionBindingMigrationForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rollbackExternalChecksForTest(t, ctx, pool)
	down, err := os.ReadFile("000050_evidence_ingestion_proposition_bindings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, propositionBindingMigration); err != nil {
		t.Fatal(err)
	}
}
