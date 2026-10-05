//go:build integration

package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func rollbackExternalRepresentationsForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	down, err := os.ReadFile("000052_evidence_ingestion_external_representations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, externalRepresentationMigration); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationExternalRepresentationMigrationReadinessAndRollback(t *testing.T) {
	mutations := map[string]string{
		"representation guard": `ALTER TABLE external_representation_records DISABLE TRIGGER external_representation_insert_guard`,
		"link guard":           `ALTER TABLE external_check_representation_links DISABLE TRIGGER external_representation_link_insert_guard`,
		"immutable history":    `ALTER TABLE external_representation_records DISABLE TRIGGER external_representation_immutable_table`,
		"function body":        `CREATE OR REPLACE FUNCTION external_representation_link_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$ BEGIN RETURN NEW; END $$`,
		"digest":               `ALTER TABLE external_representation_records DROP CONSTRAINT external_representation_digest`,
		"foreign key":          `ALTER TABLE external_check_representation_links DROP CONSTRAINT external_check_representation_links_representation_id_fkey`,
		"unique request":       `ALTER TABLE external_representation_records DROP CONSTRAINT external_representation_records_request_id_key`,
		"column":               `ALTER TABLE external_representation_records ALTER COLUMN body DROP NOT NULL`,
	}
	for name, sql := range mutations {
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
				t.Fatal("damaged representation schema passed")
			}
		})
	}
	t.Run("empty rollback and reapply", func(t *testing.T) {
		ctx, pool := migrationTestPool(t)
		if _, err := ApplyUp(ctx, pool); err != nil {
			t.Fatal(err)
		}
		rollbackExternalRepresentationsForTest(t, ctx, pool)
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
