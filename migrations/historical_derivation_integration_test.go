//go:build integration

package migrations

import (
	"os"
	"testing"
)

func TestIntegrationHistoricalDerivationReadinessAndEmptyRollback(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	_, err := ApplyUp(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	status, err := VerifyCurrent(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if status.AppliedMigrations != 62 || status.LatestMigration != manualReviewProfileMigration {
		t.Fatal("wrong historical schema", status)
	}
	for _, query := range []string{
		`ALTER TABLE canonical_derivations DROP CONSTRAINT canonical_derivation_use_class_ck`,
		`DROP TRIGGER historical_derivation_class_authority ON canonical_derivations`,
		`DROP TRIGGER historical_derivation_decision_authority ON admission_decisions`,
		`DROP TRIGGER historical_derivation_immutable_rows ON canonical_historical_derivation_reviews`,
		`CREATE OR REPLACE FUNCTION historical_derivation_authority_trigger_v1() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$ BEGIN RETURN NEW; END $$`,
		`GRANT EXECUTE ON FUNCTION historical_derivation_assert_v1(text) TO PUBLIC`,
	} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, query); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := verifyFixedCoreSchemaObjects(ctx, tx, historicalDerivationMigration); err == nil {
			tx.Rollback(ctx)
			t.Fatal("weakened historical authority accepted", query)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	down, err := os.ReadFile("000061_historical_derivation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal("empty historical rollback", err)
	}
}
