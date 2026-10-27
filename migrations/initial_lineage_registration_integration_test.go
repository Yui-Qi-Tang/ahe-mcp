//go:build integration

package migrations

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

func TestIntegrationInitialRegistrationReadiness(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCurrent(ctx, pool); err != nil {
		t.Fatal("current registration schema", err)
	}
	for _, q := range []string{
		`DROP TRIGGER canonical_initial_registration_apply ON canonical_initial_lineage_registrations`,
		`ALTER TABLE canonical_supersession_members DROP CONSTRAINT canonical_supersession_members_origin_choice`,
		`ALTER TABLE canonical_supersession_members DROP CONSTRAINT canonical_supersession_members_initial_registration_fk`,
		`ALTER TABLE canonical_initial_lineage_registrations DROP CONSTRAINT canonical_initial_registration_digest`,
		`CREATE OR REPLACE FUNCTION canonical_initial_lineage_registration_apply_v1() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$ BEGIN RETURN NEW; END $$`,
	} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, q); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := verifyFixedCoreSchemaObjects(ctx, tx, initialRegistrationMigration); err == nil {
			tx.Rollback(ctx)
			t.Fatal("weakened registration schema accepted", q)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	down, err := os.ReadFile("000060_initial_lineage_registration.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal("empty rollback", err)
	}
}

func rollbackInitialRegistrationForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rollbackManualReviewProfileMigrationForTest(t, ctx, pool)
	historicalDown, err := os.ReadFile("000061_historical_derivation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(historicalDown)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, historicalDerivationMigration); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("000060_initial_lineage_registration.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, initialRegistrationMigration); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
