//go:build integration

package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Synthetic journal records exercise the SQL/Go boundary, not solver truth.
func TestIntegrationConsistencyJSONContract(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	digest := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	configBody := `{"contract":"consistency-watch/v1","watch_id":"fixed","request_id":"first","expected_revision":0,"request":{}}`
	configSQL := `INSERT INTO consistency_watch_versions(watch_id,revision,request_id,body,body_hash) VALUES($1,1,$2,$3,$4)`
	if _, err := pool.Exec(ctx, `INSERT INTO consistency_watches(watch_id) VALUES('fixed'),('other')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, configSQL, "fixed", "first", configBody, digest(configBody)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"configuration predecessor", "result revision", "artifact byte length"} {
		t.Run(field, func(t *testing.T) {
			valid := "0"
			if field == "result revision" {
				valid = "1"
			}
			if field == "artifact byte length" {
				valid = "3"
			}
			for _, tc := range []struct {
				name, token string
				valid       bool
			}{
				{"integer", valid, true}, {"string", `"` + valid + `"`, false}, {"decimal", valid + ".0", false}, {"exponent", valid + "e0", false}, {"null", "null", false}, {"boolean", "true", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tx, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback(ctx)
					var body string
					switch field {
					case "configuration predecessor":
						body = fmt.Sprintf(`{"contract":"consistency-watch/v1","watch_id":"other","request_id":"other","expected_revision":%s,"request":{}}`, tc.token)
						_, err = tx.Exec(ctx, configSQL, "other", "other", body, digest(body))
					default:
						revision, manifest := "1", "[]"
						if field == "result revision" {
							revision = tc.token
						} else {
							manifest = fmt.Sprintf(`[{"id":"proof","sha256":"%s","bytes":%s}]`, digest("abc"), tc.token)
						}
						body = fmt.Sprintf(`{"contract":"consistency-run/v1","watch_id":"fixed","revision":%s,"target_id":"target","diagnosis":{},"artifacts":%s}`, revision, manifest)
						_, err = tx.Exec(ctx, `INSERT INTO consistency_runs(run_id,watch_id,revision,target_id,body) VALUES($1,'fixed',1,'target',$2)`, digest(body), body)
						if err == nil && field == "artifact byte length" {
							_, err = tx.Exec(ctx, `INSERT INTO consistency_run_artifacts(run_id,artifact_id,digest,content) VALUES($1,'proof',$2,$3)`, digest(body), digest("abc"), []byte("abc"))
						}
					}
					if err == nil {
						_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
					}
					if !tc.valid {
						var pgErr *pgconn.PgError
						if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
							t.Fatalf("want SQLSTATE 23514, got %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					var decoded struct {
						ExpectedRevision int64 `json:"expected_revision"`
						Revision         int64 `json:"revision"`
						Artifacts        []struct {
							Bytes int64 `json:"bytes"`
						} `json:"artifacts"`
					}
					if err := json.Unmarshal([]byte(body), &decoded); err != nil {
						t.Fatalf("accepted record cannot be decoded: %v", err)
					}
				})
			}
		})
	}
	t.Run("configured notification hash", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO consistency_events(watch_id,cursor,revision,target_id,kind) VALUES('fixed',1,1,'wrong','configured')`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("want SQLSTATE 23514, got %v", err)
		}
	})
}

func TestIntegrationConsistencyJSONUpgrade(t *testing.T) {
	for _, bad := range []string{"valid history", "predecessor exponent", "revision exponent", "byte count exponent", "notification hash"} {
		t.Run(bad, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			rollbackCandidateRelationSourceForTest(t, ctx, pool)
			down, err := os.ReadFile("000054_evidence_consistency_contract.down.sql")
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
			if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name=$1`, consistencyContractMigration); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			pred, rev, size := "0", "1", "3"
			switch bad {
			case "predecessor exponent":
				pred = "0e0"
			case "revision exponent":
				rev = "1e0"
			case "byte count exponent":
				size = "3e0"
			}
			digest := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
			body := fmt.Sprintf(`{"contract":"consistency-watch/v1","watch_id":"fixed","request_id":"first","expected_revision":%s,"request":{}}`, pred)
			if _, err := pool.Exec(ctx, `INSERT INTO consistency_watches(watch_id) VALUES('fixed')`); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO consistency_watch_versions(watch_id,revision,request_id,body,body_hash) VALUES($1,$2,$3,$4,encode(sha256(convert_to($4,'UTF8')),'hex'))`, "fixed", 1, "first", body); err != nil {
				t.Fatal(err)
			}
			run := fmt.Sprintf(`{"contract":"consistency-run/v1","watch_id":"fixed","revision":%s,"target_id":"target","diagnosis":{},"artifacts":[{"id":"proof","sha256":"%s","bytes":%s}]}`, rev, digest("abc"), size)
			tx, err = pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `INSERT INTO consistency_runs(run_id,watch_id,revision,target_id,body) VALUES($1,'fixed',1,'target',$2)`, digest(run), run); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO consistency_run_artifacts(run_id,artifact_id,digest,content) VALUES($1,'proof',$2,$3)`, digest(run), digest("abc"), []byte("abc")); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var s string
				err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(c)) FROM consistency_watch_versions c),(SELECT jsonb_agg(to_jsonb(r)) FROM consistency_runs r),(SELECT jsonb_agg(to_jsonb(a)) FROM consistency_run_artifacts a),(SELECT jsonb_agg(to_jsonb(e)) FROM consistency_events e))::text`).Scan(&s)
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			if bad == "notification hash" {
				if _, err := pool.Exec(ctx, `INSERT INTO consistency_events(watch_id,cursor,revision,target_id,kind) VALUES('fixed',1,1,'wrong','configured')`); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot()
			var oid uint32
			var acl, oldSource string
			if err := pool.QueryRow(ctx, `SELECT oid,coalesce(proacl::text,''),prosrc FROM pg_proc WHERE oid='consistency_version_guard()'::regprocedure`).Scan(&oid, &acl, &oldSource); err != nil {
				t.Fatal(err)
			}
			changed, err := ApplyUp(ctx, pool)
			if bad != "valid history" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, "incompatible JSON integers or configured hash") {
					t.Fatalf("upgrade must reject historical bad integer: %v", err)
				}
				var count int
				var source string
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE migration_name=$1`, consistencyContractMigration).Scan(&count); err != nil || count != 0 {
					t.Fatal("failed upgrade wrote ledger", count, err)
				}
				if err := pool.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid=$1`, oid).Scan(&source); err != nil || source != oldSource {
					t.Fatal("failed upgrade changed guard", err)
				}
			} else {
				if err != nil || !changed {
					t.Fatal(changed, err)
				}
				if _, err := VerifyCurrent(ctx, pool); err != nil {
					t.Fatal(err)
				}
				if again, err := ApplyUp(ctx, pool); err != nil || again {
					t.Fatal("upgrade not idempotent", again, err)
				}
			}
			if after := snapshot(); after != before {
				t.Fatal("upgrade rewrote history")
			}
			var afterOID uint32
			var afterACL string
			if err := pool.QueryRow(ctx, `SELECT oid,coalesce(proacl::text,'') FROM pg_proc WHERE oid='consistency_version_guard()'::regprocedure`).Scan(&afterOID, &afterACL); err != nil || afterOID != oid || afterACL != acl {
				t.Fatal("upgrade changed function identity/authority", err)
			}
		})
	}
}
