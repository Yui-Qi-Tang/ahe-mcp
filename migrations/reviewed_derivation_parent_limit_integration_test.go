//go:build integration

package migrations

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestIntegrationReviewedDerivationParentLimitUpgradeRollback(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 1)
	applyMigrationsThrough(t, ctx, pool, "000056_reviewed_lifecycle_writers.up.sql")
	proposal := createOrdinaryFencePendingProposal(t, ctx, pool)
	input := evidenceingestion.AdmissionInput{ProposalOccurrenceID: proposal, DecisionBy: "synthetic:upgrade57", DecisionReason: "TEST APPROVAL STUB: retain native history"}
	original, err := evidenceingestion.AdmitPendingProposal(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	before := ordinaryFenceRows(t, ctx, pool)
	if _, err := VerifyCurrentInSchema(ctx, pool, schema); !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatal("schema 56 incorrectly accepted by schema 57 binary")
	}
	changed, err := ApplyUpInSchema(ctx, pool, schema)
	if err != nil || !changed {
		t.Fatalf("upgrade57: changed=%v err=%v", changed, err)
	}
	status, err := VerifyCurrentInSchema(ctx, pool, schema)
	if err != nil || status.AppliedMigrations != 57 {
		t.Fatalf("schema 57 readiness: %+v %v", status, err)
	}
	after := ordinaryFenceRows(t, ctx, pool)
	for table, rows := range before {
		if table != "schema_migrations" && !reflect.DeepEqual(rows, after[table]) {
			t.Fatalf("migration57 rewrote %s", table)
		}
	}
	if changed, err := ApplyUpInSchema(ctx, pool, schema); err != nil || changed {
		t.Fatal("migration57 is not idempotent")
	}
	down, err := os.ReadFile("000057_reviewed_derivation_parent_limit.down.sql")
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
	if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE migration_name='000057_reviewed_derivation_parent_limit.up.sql'"); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := tx.QueryRow(ctx, "SELECT encode(sha256(convert_to(prosrc,'UTF8')),'hex') FROM pg_catalog.pg_proc WHERE oid='canonical_implements_recursive_basis_assert_v2(text,jsonb)'::regprocedure").Scan(&restored); err != nil || restored != "cba549e9619e216571243771477e713ae5c1f191bd261011a5769b15c086dadc" {
		t.Fatal("rollback did not restore the exact schema56 guard")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if changed, err := ApplyUpInSchema(ctx, pool, schema); err != nil || !changed {
		t.Fatal("re-upgrade57 failed")
	}
	if _, err := VerifyCurrentInSchema(ctx, pool, schema); err != nil {
		t.Fatal(err)
	}
	replay, err := evidenceingestion.AdmitPendingProposal(ctx, pool, input)
	if err != nil || !replay.Replayed || replay.CanonicalRef != original.CanonicalRef {
		t.Fatal("upgrade/rollback changed native history replay")
	}
}

func TestIntegrationReviewedDerivationParentLimitRejectsGuardDrift(t *testing.T) {
	for _, current := range []bool{false, true} {
		queries := []string{
			"CREATE OR REPLACE FUNCTION canonical_implements_recursive_basis_assert_v2(root_id TEXT,basis JSONB) RETURNS VOID LANGUAGE plpgsql AS $$ BEGIN RETURN; END $$",
			"CREATE OR REPLACE FUNCTION canonical_implements_admission_assert_v2(checked_request_id TEXT) RETURNS VOID LANGUAGE plpgsql AS $$ BEGIN RETURN; END $$",
		}
		if current {
			queries = append(queries,
				"ALTER TABLE canonical_implements_admissions DROP CONSTRAINT canonical_implements_admissions_display_bytes_ck",
				"ALTER TABLE canonical_implements_admissions DROP CONSTRAINT canonical_implements_admissions_receipt_bytes_ck")
		}
		for _, query := range queries {
			t.Run(map[bool]string{false: "upgrade", true: "readiness"}[current]+"/"+query, func(t *testing.T) {
				ctx, pool, schema := migrationTestPoolWithMaxConns(t, 1)
				applyMigrationsThrough(t, ctx, pool, "000056_reviewed_lifecycle_writers.up.sql")
				if current {
					if _, err := ApplyUpInSchema(ctx, pool, schema); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := pool.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
				before := ordinaryFenceRows(t, ctx, pool)
				if current {
					if _, err := VerifyCurrentInSchema(ctx, pool, schema); !errors.Is(err, ErrSchemaNotCurrent) {
						t.Fatal("modified SQL guard/byte constraint accepted by readiness")
					}
				} else if changed, err := ApplyUpInSchema(ctx, pool, schema); err == nil || changed {
					t.Fatal("migration57 accepted an unexpected predecessor guard")
				}
				if !reflect.DeepEqual(before, ordinaryFenceRows(t, ctx, pool)) {
					t.Fatal("refused guard drift changed native rows or ledger")
				}
			})
		}
	}
}
