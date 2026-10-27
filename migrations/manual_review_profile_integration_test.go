//go:build integration

package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const manualProfileFixturePayload = `{"contract_version":"manual-source-review-profile/v1","title":"Manual note","location":"notes/example","coverage":"submitted_text","limitations":[]}`

func manualProfileFixtureHash(seed string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(seed)))
}

func TestIntegrationManualReviewProfileReadiness(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if status, err := VerifyCurrent(ctx, pool); err != nil || status.AppliedMigrations != 62 || status.LatestMigration != manualReviewProfileMigration {
		t.Fatalf("manual profile migration62 readiness = %+v, %v", status, err)
	}
	controls := make(map[string]string, len(manualReviewProfileFunctions)+len(manualReviewProfileTriggers)+len(manualReviewProfileConstraints)+4)
	for _, function := range manualReviewProfileFunctions {
		body := "BEGIN RETURN; END"
		if function.Result == "trigger" {
			body = "BEGIN RETURN NULL; END"
		}
		// CREATE OR REPLACE cannot rename existing named parameters, so replace
		// only the captured body inside its existing pg_get_functiondef output.
		var declaration string
		if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef(($1)::regprocedure)`, function.Name+"("+function.Arguments+")").Scan(&declaration); err != nil {
			t.Fatal(err)
		}
		source, err := embeddedMigrationFunctionSource(manualReviewProfileMigration, function.Name)
		if err != nil || !strings.Contains(declaration, source) {
			t.Fatalf("load %s body: %v", function.Name, err)
		}
		controls[function.Name] = strings.Replace(declaration, source, body, 1)
	}
	for _, trigger := range manualReviewProfileTriggers {
		controls[trigger.Name] = "ALTER TABLE " + trigger.Table + " DISABLE TRIGGER " + trigger.Name
	}
	for _, constraint := range manualReviewProfileConstraints {
		controls[constraint.Name] = "ALTER TABLE " + constraint.Table + " DROP CONSTRAINT " + constraint.Name
	}
	controls["capture_default"] = "ALTER TABLE source_snapshots ALTER COLUMN manual_profile_capture_xid SET DEFAULT pg_current_xact_id()"
	controls["capture_definer"] = "ALTER FUNCTION source_manual_review_profile_capture_trigger_v1() SECURITY INVOKER"
	controls["helper_definer"] = "ALTER FUNCTION source_manual_review_profile_assert_v1(TEXT) SECURITY DEFINER"
	controls["helper_public_execute"] = "GRANT EXECUTE ON FUNCTION source_manual_review_profile_assert_basis_v1(TEXT,JSONB) TO PUBLIC"
	for name, statement := range controls {
		t.Run(name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, statement); err != nil {
				t.Fatalf("inject readiness drift: %v", err)
			}
			if err := verifyFixedCoreSchemaObjects(ctx, tx, manualReviewProfileMigration); err == nil {
				t.Fatal("readiness accepted missing or widened manual profile authority")
			}
		})
	}
	if _, err := VerifyCurrent(ctx, pool); err != nil {
		t.Fatalf("readiness not restored by rollback: %v", err)
	}
}

func TestIntegrationManualReviewProfileCaptureAndAppendOnly(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A released savepoint uses a subtransaction xmin but the capture authority
	// must remain the top-level server xid. A rolled-back savepoint leaves none.
	rolledBack, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertManualProfileSourceFixture(t, ctx, rolledBack, "rollback", "manual_text")
	if err := insertManualProfileFixture(ctx, rolledBack, "rollback", manualProfileFixturePayload); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM source_snapshots)+(SELECT count(*) FROM source_manual_review_profiles)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back savepoint left authority rows: %d, %v", count, err)
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertManualProfileSourceFixture(t, ctx, savepoint, "capture", "manual_text")
	if err := savepoint.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var serverStamp bool
	if err := tx.QueryRow(ctx, `SELECT manual_profile_capture_xid=pg_current_xact_id() FROM source_snapshots WHERE source_snapshot_id='srcsnap:manual-profile-capture'`).Scan(&serverStamp); err != nil || !serverStamp {
		t.Fatalf("capture marker is not server-owned top-level xid: %t, %v", serverStamp, err)
	}
	if err := insertManualProfileFixture(ctx, tx, "capture", manualProfileFixturePayload); err != nil {
		t.Fatalf("same-capture profile: %v", err)
	}
	basis := manualProfileFixtureBasis(t, "capture", manualProfileFixturePayload)
	if _, err := tx.Exec(ctx, `SELECT source_manual_review_profile_assert_basis_v1($1,$2::jsonb)`, "srcsnap:manual-profile-capture", basis); err != nil {
		t.Fatalf("native exact profile review: %v", err)
	}
	down := manualProfileDownSQL(t)
	for _, control := range []struct{ name, statement string }{
		{"update_profile", `UPDATE source_manual_review_profiles SET profile_payload_utf8=profile_payload_utf8`},
		{"delete_profile", `DELETE FROM source_manual_review_profiles`},
		{"truncate_profile", `TRUNCATE source_manual_review_profiles`},
		{"capture_xid", `UPDATE source_snapshots SET manual_profile_capture_xid='1'::xid8`},
		{"source_metadata", `UPDATE source_snapshots SET origin_metadata='{"title":"new"}'::jsonb`},
		{"source_version", `UPDATE source_snapshots SET source_version='v2'`},
		{"request_hash", `UPDATE source_intake_requests SET request_payload_hash='sha256:changed'`},
		{"profile_down", down},
	} {
		t.Run(control.name, func(t *testing.T) {
			probe, err := tx.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = probe.Rollback(ctx) }()
			_, err = probe.Exec(ctx, control.statement)
			assertManualProfileCheckFailure(t, err)
		})
	}
	for _, field := range []string{"manual_review_profile", "source_title", "source_location", "source_coverage", "source_limitations", "source_version", "raw_content_hash"} {
		t.Run("review_"+field, func(t *testing.T) {
			probe, err := tx.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = probe.Rollback(ctx) }()
			_, err = probe.Exec(ctx, `SELECT source_manual_review_profile_assert_basis_v1($1,$2::jsonb-$3::text)`, "srcsnap:manual-profile-capture", basis, field)
			assertManualProfileCheckFailure(t, err)
		})
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM source_snapshots)+(SELECT count(*) FROM source_manual_review_profiles)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("top-level rollback left authority rows: %d, %v", count, err)
	}
}

func TestIntegrationManualReviewProfileRejectsRetrofitAndMalformedAuthority(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertManualProfileSourceFixture(t, ctx, tx, "prior", "manual_text")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, control := range []struct{ name, payload, tamper, sourceSystem string }{
		{name: "prior", payload: manualProfileFixturePayload, tamper: `UPDATE source_snapshots SET created_at=now() WHERE source_snapshot_id='srcsnap:manual-profile-prior'`},
		{name: "external", payload: manualProfileFixturePayload, sourceSystem: "external_agent"},
		{name: "null_limits", payload: strings.Replace(manualProfileFixturePayload, `"limitations":[]`, `"limitations":null`, 1)},
		{name: "empty_excerpt", payload: strings.Replace(manualProfileFixturePayload, "submitted_text", "exact_excerpt", 1)},
		{name: "duplicate_key", payload: strings.Replace(manualProfileFixturePayload, `"title":"Manual note"`, `"title":"Manual note","title":"Manual note"`, 1)},
		{name: "unknown_key", payload: strings.Replace(manualProfileFixturePayload, `"title":"Manual note"`, `"title":"Manual note","extra":true`, 1)},
		{name: "untrimmed", payload: strings.Replace(manualProfileFixturePayload, "Manual note", " Manual note", 1)},
		{name: "unicode_untrimmed", payload: strings.Replace(manualProfileFixturePayload, "Manual note", "\u0085Manual note", 1)},
		{name: "title_bytes", payload: strings.Replace(manualProfileFixturePayload, "Manual note", strings.Repeat("語", 334), 1)},
		{name: "limit_bytes", payload: strings.Replace(manualProfileFixturePayload, `"limitations":[]`, `"limitations":["`+strings.Repeat("x", 1001)+`"]`, 1)},
		{name: "limit_count", payload: strings.Replace(manualProfileFixturePayload, `"limitations":[]`, `"limitations":[`+strings.Repeat(`"x",`, 16)+`"x"]`, 1)},
		{name: "raw_hash", payload: manualProfileFixturePayload, tamper: `UPDATE source_blobs SET raw_content='wrong'::bytea`},
		{name: "view_hash", payload: manualProfileFixturePayload, tamper: `UPDATE extraction_views SET rendered_content='wrong'::bytea`},
		{name: "profile_hash", payload: manualProfileFixturePayload},
		{name: "request_binding", payload: manualProfileFixturePayload, tamper: `UPDATE source_intake_requests SET source_snapshot_id='srcsnap:manual-profile-prior' WHERE request_id='manual-profile-request-request_binding'`},
	} {
		t.Run(control.name, func(t *testing.T) {
			probe, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = probe.Rollback(ctx) }()
			if control.name != "prior" {
				system := control.sourceSystem
				if system == "" {
					system = "manual_text"
				}
				insertManualProfileSourceFixture(t, ctx, probe, control.name, system)
			}
			if control.tamper != "" {
				if _, err := probe.Exec(ctx, control.tamper); err != nil {
					t.Fatalf("inject direct SQL control: %v", err)
				}
			}
			if control.name == "profile_hash" {
				_, err = probe.Exec(ctx, `INSERT INTO source_manual_review_profiles(source_snapshot_id,intake_request_id,contract_version,profile_payload_utf8,profile_hash)
					VALUES($1,$2,'manual-source-review-profile/v1',$3,$4)`, "srcsnap:manual-profile-"+control.name, "manual-profile-request-"+control.name, control.payload, manualProfileFixtureHash("wrong exact profile bytes"))
				assertManualProfileCheckFailure(t, err)
			} else {
				assertManualProfileCheckFailure(t, insertManualProfileFixture(ctx, probe, control.name, control.payload))
			}
		})
	}
	probe, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Rollback(ctx) }()
	insertManualProfileSourceFixture(t, ctx, probe, "excerpt", "manual_text")
	excerpt := strings.Replace(strings.Replace(manualProfileFixturePayload, "submitted_text", "exact_excerpt", 1), `"limitations":[]`, `"limitations":["Only the submitted excerpt was reviewed."]`, 1)
	if err := insertManualProfileFixture(ctx, probe, "excerpt", excerpt); err != nil {
		t.Fatalf("explicit limited excerpt positive control: %v", err)
	}
	if err := probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_manual_review_profiles`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected profile writes leaked rows: %d, %v", count, err)
	}
}

func TestIntegrationManualReviewProfileDownReapplyPreservesLegacy(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, manualProfileDownSQL(t)); err != nil {
		t.Fatalf("empty manual profile migration62 down: %v", err)
	}
	// Insert pre57 source rows without a capture column or profile. Reapplying
	// the migration must leave them unqualified, regardless of origin_metadata.
	if _, err := tx.Exec(ctx, `INSERT INTO source_blobs(raw_content_hash,raw_content,byte_length) VALUES($1,$2,2);
		`, manualProfileFixtureHash("a\n"), []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO source_snapshots(source_snapshot_id,source_system,source_id,source_version,raw_content_hash,origin_metadata)
		VALUES('srcsnap:manual-profile-legacy','manual_text','manual-profile-legacy','v1',$1,'{"title":"not qualified"}')`, manualProfileFixtureHash("a\n")); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile(manualReviewProfileMigration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(up)); err != nil {
		t.Fatalf("manual profile migration62 reapply: %v", err)
	}
	if err := verifyFixedCoreSchemaObjects(ctx, tx, manualReviewProfileMigration); err != nil {
		t.Fatalf("reapplied readiness: %v", err)
	}
	var noMarker bool
	if err := tx.QueryRow(ctx, `SELECT manual_profile_capture_xid IS NULL FROM source_snapshots WHERE source_snapshot_id='srcsnap:manual-profile-legacy'`).Scan(&noMarker); err != nil || !noMarker {
		t.Fatalf("migration reinterpreted old source authority: %t, %v", noMarker, err)
	}
	if _, err := tx.Exec(ctx, `SELECT source_manual_review_profile_assert_basis_v1('srcsnap:manual-profile-legacy','{}')`); err != nil {
		t.Fatalf("legacy profile-free receipt rejected: %v", err)
	}
	for _, statement := range []string{
		`UPDATE source_snapshots SET manual_profile_capture_xid=pg_current_xact_id() WHERE source_snapshot_id='srcsnap:manual-profile-legacy'`,
		`SELECT source_manual_review_profile_assert_basis_v1('srcsnap:manual-profile-legacy','{"manual_review_profile":null}')`,
	} {
		probe, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe.Exec(ctx, statement)
		assertManualProfileCheckFailure(t, err)
		if err := probe.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCurrent(ctx, pool); err != nil {
		t.Fatalf("final manual profile migration62 readiness: %v", err)
	}
}

func insertManualProfileSourceFixture(t *testing.T, ctx context.Context, tx pgx.Tx, suffix, sourceSystem string) {
	t.Helper()
	raw := []byte("a\n")
	hash := manualProfileFixtureHash(string(raw))
	if _, err := tx.Exec(ctx, `INSERT INTO source_blobs(raw_content_hash,raw_content,byte_length) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, hash, raw, len(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO source_snapshots(source_snapshot_id,source_system,source_id,source_version,raw_content_hash,manual_profile_capture_xid)
		VALUES($1,$2,$3,'v1',$4,'1'::xid8)`, "srcsnap:manual-profile-"+suffix, sourceSystem, "manual-profile-"+suffix, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO extraction_views(extraction_view_id,source_snapshot_id,renderer_name,renderer_version,rendered_content,rendered_content_hash)
		VALUES($1,$2,'manual-test','v1',$3,$4)`, "view:manual-profile-"+suffix, "srcsnap:manual-profile-"+suffix, raw, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO source_intake_requests(request_id,source_snapshot_id,extraction_view_id,request_payload_hash)
		VALUES($1,$2,$3,$4)`, "manual-profile-request-"+suffix, "srcsnap:manual-profile-"+suffix, "view:manual-profile-"+suffix, manualProfileFixtureHash("manual-profile-intake-"+suffix)); err != nil {
		t.Fatal(err)
	}
}

func insertManualProfileFixture(ctx context.Context, tx pgx.Tx, suffix, payload string) error {
	_, err := tx.Exec(ctx, `INSERT INTO source_manual_review_profiles(source_snapshot_id,intake_request_id,contract_version,profile_payload_utf8,profile_hash)
		VALUES($1,$2,'manual-source-review-profile/v1',$3,$4)`, "srcsnap:manual-profile-"+suffix, "manual-profile-request-"+suffix, payload, manualProfileFixtureHash(payload))
	return err
}

func manualProfileFixtureBasis(t *testing.T, suffix, payload string) string {
	t.Helper()
	basis := map[string]any{
		"source_snapshot_id": "srcsnap:manual-profile-" + suffix,
		"source_system":      "manual_text", "source_id": "manual-profile-" + suffix,
		"source_version": "v1", "raw_content_hash": manualProfileFixtureHash("a\n"),
		"source_title": "Manual note", "source_location": "notes/example",
		"source_coverage": "submitted_text", "source_limitations": []string{},
		"manual_review_profile": map[string]any{
			"source_snapshot_id": "srcsnap:manual-profile-" + suffix,
			"intake_request_id":  "manual-profile-request-" + suffix,
			"contract_version":   "manual-source-review-profile/v1",
			"profile":            json.RawMessage(payload), "profile_hash": manualProfileFixtureHash(payload),
		},
	}
	encoded, err := json.Marshal(basis)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertManualProfileCheckFailure(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("manual profile rejection = %v, want SQLSTATE23514", err)
	}
}

func manualProfileDownSQL(t *testing.T) string {
	t.Helper()
	down, err := os.ReadFile("000062_manual_review_profile.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(down)
}

func rollbackManualReviewProfileMigrationForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	// Product references predates this prerequisite and is not rolled back.
	if _, err := pool.Exec(ctx, manualProfileDownSQL(t)); err != nil {
		t.Fatalf("apply manual profile migration62 down: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, manualReviewProfileMigration); err != nil {
		t.Fatalf("remove manual profile migration62 test ledger: %v", err)
	}
}
