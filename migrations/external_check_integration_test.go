//go:build integration

package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func rollbackExternalChecksForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rollbackExternalRepresentationsForTest(t, ctx, pool)
	down, err := os.ReadFile("000051_evidence_ingestion_external_checks.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, externalCheckMigration); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationExternalCheckMigrationReadinessAndRollback(t *testing.T) {
	for _, broken := range []string{"trigger", "function", "constraint", "column", "conditional trigger"} {
		t.Run(broken, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); err != nil {
				t.Fatal(err)
			}
			mutations := map[string]string{"conditional trigger": `DROP TRIGGER external_check_insert_guard ON external_check_records; CREATE TRIGGER external_check_insert_guard BEFORE INSERT ON external_check_records FOR EACH ROW WHEN (false) EXECUTE FUNCTION external_check_record_guard()`, "trigger": `ALTER TABLE external_check_records DISABLE TRIGGER external_check_insert_guard`, "function": `CREATE OR REPLACE FUNCTION external_check_record_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$ BEGIN RETURN NEW; END $$`, "constraint": `ALTER TABLE external_check_records DROP CONSTRAINT external_check_digest`, "column": `ALTER TABLE external_check_records ALTER COLUMN body DROP NOT NULL`}
			if _, err := pool.Exec(ctx, mutations[broken]); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); err == nil {
				t.Fatal("damaged schema passed readiness")
			}
		})
	}
	t.Run("empty rollback and reapply", func(t *testing.T) {
		ctx, pool := migrationTestPool(t)
		if _, err := ApplyUp(ctx, pool); err != nil {
			t.Fatal(err)
		}
		rollbackExternalChecksForTest(t, ctx, pool)
		if _, err := VerifyCurrent(ctx, pool); err == nil {
			t.Fatal("missing migration passed")
		}
		if _, err := ApplyUp(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCurrent(ctx, pool); err != nil {
			t.Fatal(err)
		}
	})
}
