//go:build integration

package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func rollbackConsistencyForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	down, err := os.ReadFile("000053_evidence_consistency_lifecycle.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, consistencyMigration); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationConsistencyMigration(t *testing.T) {
	for name, sql := range map[string]string{
		"disabled proof completeness": `ALTER TABLE consistency_runs DISABLE TRIGGER consistency_run_artifacts_complete`,
		"weakened cursor guard":       `CREATE OR REPLACE FUNCTION consistency_event_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$ BEGIN RETURN NEW; END $$`,
		"mutable history":             `ALTER TABLE consistency_events DISABLE TRIGGER consistency_events_immutable`,
		"missing result uniqueness":   `ALTER TABLE consistency_runs DROP CONSTRAINT consistency_runs_watch_id_revision_target_id_key`,
		"nullable configuration":      `ALTER TABLE consistency_watch_versions ALTER COLUMN body DROP NOT NULL`,
		"missing artifact digest":     `ALTER TABLE consistency_run_artifacts DROP CONSTRAINT consistency_run_artifacts_check`,
		"premature artifact check":    `DROP TRIGGER consistency_run_artifacts_complete ON consistency_runs; CREATE CONSTRAINT TRIGGER consistency_run_artifacts_complete AFTER INSERT ON consistency_runs NOT DEFERRABLE FOR EACH ROW EXECUTE FUNCTION consistency_artifact_guard()`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); err == nil {
				t.Fatal("damaged diagnostic schema accepted")
			}
		})
	}
	t.Run("empty rollback preserves earlier records", func(t *testing.T) {
		ctx, pool := migrationTestPool(t)
		if _, err := ApplyUp(ctx, pool); err != nil {
			t.Fatal(err)
		}
		rollbackConsistencyForTest(t, ctx, pool)
		if _, err := VerifyCurrent(ctx, pool); err == nil {
			t.Fatal("missing migration accepted")
		}
		if _, err := ApplyUp(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCurrent(ctx, pool); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("populated rollback rejected", func(t *testing.T) {
		ctx, pool := migrationTestPool(t)
		if _, err := ApplyUp(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO consistency_watches(watch_id) VALUES('keep-history')`); err != nil {
			t.Fatal(err)
		}
		down, err := os.ReadFile("000053_evidence_consistency_lifecycle.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(down)); err == nil {
			t.Fatal("destroyed diagnostic history")
		}
		if _, err := VerifyCurrent(ctx, pool); err != nil {
			t.Fatal(err)
		}
	})
}
