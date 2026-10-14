//go:build integration

package migrations

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func rollbackContradictionReviewForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	down, err := os.ReadFile("000059_reviewed_contradictions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, contradictionReviewMigration); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestIntegrationContradictionReviewQualification(t *testing.T) {
	for name, sql := range map[string]string{
		"missing guard":          `DROP TRIGGER canonical_contradiction_review_bindings_apply ON canonical_contradiction_review_bindings`,
		"conditional guard":      `DROP TRIGGER canonical_contradiction_review_bindings_apply ON canonical_contradiction_review_bindings; CREATE TRIGGER canonical_contradiction_review_bindings_apply BEFORE INSERT ON canonical_contradiction_review_bindings FOR EACH ROW WHEN(false) EXECUTE FUNCTION canonical_contradiction_review_apply_v1()`,
		"mutable receipt":        `ALTER TABLE canonical_contradiction_review_bindings DISABLE TRIGGER canonical_contradiction_review_bindings_immutable_rows`,
		"public writer":          `GRANT EXECUTE ON FUNCTION canonical_contradiction_review_apply_v1() TO PUBLIC`,
		"writer body":            `CREATE OR REPLACE FUNCTION canonical_contradiction_review_apply_v1() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$ BEGIN RETURN NEW; END $$`,
		"missing atomic receipt": `ALTER TABLE canonical_contradiction_admission_decisions DROP CONSTRAINT canonical_contradiction_decision_review_fk`,
		"immediate receipt":      `ALTER TABLE canonical_contradiction_admission_decisions ALTER CONSTRAINT canonical_contradiction_decision_review_fk NOT DEFERRABLE`,
		"missing digest":         `ALTER TABLE canonical_contradiction_review_bindings DROP CONSTRAINT canonical_contradiction_review_bindings_digest`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); !errors.Is(err, ErrSchemaNotCurrent) {
				t.Fatal("tampered native authority accepted", err)
			}
		})
	}
}
func TestIntegrationContradictionReviewUpgradeAndEmptyRollback(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rollbackContradictionReviewForTest(t, ctx, pool)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 58 {
		t.Fatal("schema58 rollback", n, err)
	}
	if _, err := VerifyCurrent(ctx, pool); !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatal("older schema passed current readiness")
	}
	if changed, err := ApplyUp(ctx, pool); err != nil || !changed {
		t.Fatal("native upgrade59", changed, err)
	}
	if _, err := VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if changed, err := ApplyUp(ctx, pool); err != nil || changed {
		t.Fatal("upgrade not idempotent", err)
	}
}
