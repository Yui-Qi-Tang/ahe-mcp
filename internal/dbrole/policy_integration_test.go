//go:build integration

package dbrole

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationInstallPolicyAppliesExactQueryACL(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	suffix := policyRandomHex(t, 8)
	schema := "ahe_dbrole_test_" + suffix
	role := "ahe_dbrole_query_" + suffix
	schemaID := pgx.Identifier{schema}.Sanitize()
	roleID := pgx.Identifier{role}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaID+" CASCADE")
		_, _ = admin.Exec(cleanupCtx, "DROP OWNED BY "+roleID)
		_, _ = admin.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+roleID)
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire physical connection: %v", err)
	}
	t.Cleanup(connection.Release)
	tx, err := connection.Conn().Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback-scoped policy transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, "CREATE ROLE "+roleID+" NOLOGIN"); err != nil {
		t.Fatalf("create NOLOGIN policy role: %v", err)
	}
	var database string
	if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("load current database: %v", err)
	}
	databaseID := pgx.Identifier{database}.Sanitize()
	outsideSchema := schema + "_outside"
	outsideSchemaID := pgx.Identifier{outsideSchema}.Sanitize()
	for _, statement := range []string{
		"REVOKE CREATE, TEMPORARY ON DATABASE " + databaseID + " FROM PUBLIC",
		"REVOKE ALL PRIVILEGES ON SCHEMA public FROM PUBLIC",
		"CREATE SCHEMA " + outsideSchemaID,
		"CREATE TABLE " + outsideSchemaID + ".outside_table (payload TEXT)",
		"CREATE TABLE " + schemaID + ".policy_unknown_table (payload TEXT)",
		"CREATE FUNCTION " + schemaID + ".policy_unknown_function() RETURNS INTEGER LANGUAGE SQL AS 'SELECT 1'",
		"GRANT CREATE ON SCHEMA " + schemaID + " TO " + roleID,
		"GRANT USAGE ON SCHEMA " + schemaID + " TO PUBLIC",
		"GRANT TEMPORARY ON DATABASE " + databaseID + " TO " + roleID,
		"GRANT SELECT ON TABLE " + schemaID + ".policy_unknown_table TO " + roleID,
		"GRANT UPDATE (payload) ON TABLE " + schemaID + ".policy_unknown_table TO " + roleID,
		"GRANT SELECT (payload) ON TABLE " + schemaID + ".policy_unknown_table TO PUBLIC",
		"GRANT EXECUTE ON FUNCTION " + schemaID + ".policy_unknown_function() TO " + roleID,
		"GRANT USAGE ON SCHEMA " + outsideSchemaID + " TO " + roleID,
		"GRANT UPDATE ON TABLE " + outsideSchemaID + ".outside_table TO " + roleID,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("seed excessive ACL with %q: %v", statement, err)
		}
	}
	var largeObjectID uint32
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.lo_create(0)`).Scan(&largeObjectID); err != nil {
		t.Fatalf("create test large object: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		"GRANT SELECT, UPDATE ON LARGE OBJECT %d TO %s",
		largeObjectID,
		roleID,
	)); err != nil {
		t.Fatalf("grant excessive large-object ACL: %v", err)
	}
	if _, err := installPolicy(ctx, tx, InstallInput{
		Role:    role,
		Schema:  schema,
		Profile: ProfileQuery,
	}); !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("installPolicy() with outside-schema authority error = %v, want ErrPolicyViolation", err)
	}
	for _, statement := range []string{
		"REVOKE UPDATE ON TABLE " + outsideSchemaID + ".outside_table FROM " + roleID,
		"REVOKE USAGE ON SCHEMA " + outsideSchemaID + " FROM " + roleID,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("remove outside-schema ACL with %q: %v", statement, err)
		}
	}
	if _, err := installPolicy(ctx, tx, InstallInput{
		Role:    role,
		Schema:  schema,
		Profile: ProfileQuery,
	}); !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("installPolicy() with large-object authority error = %v, want ErrPolicyViolation", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		"REVOKE SELECT, UPDATE ON LARGE OBJECT %d FROM %s",
		largeObjectID,
		roleID,
	)); err != nil {
		t.Fatalf("remove large-object ACL: %v", err)
	}
	if _, err := tx.Exec(ctx, "ALTER ROLE "+roleID+" SET statement_timeout TO '1s'"); err != nil {
		t.Fatalf("seed persistent role configuration: %v", err)
	}
	if _, err := installPolicy(ctx, tx, InstallInput{
		Role:    role,
		Schema:  schema,
		Profile: ProfileQuery,
	}); !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("installPolicy() with persistent role configuration error = %v, want ErrPolicyViolation", err)
	}
	if _, err := tx.Exec(ctx, "ALTER ROLE "+roleID+" RESET ALL"); err != nil {
		t.Fatalf("remove persistent role configuration: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"GRANT EXECUTE ON FUNCTION pg_catalog.pg_sleep(double precision) TO "+roleID,
	); err != nil {
		t.Fatalf("seed direct system-function ACL: %v", err)
	}
	if _, err := installPolicy(ctx, tx, InstallInput{
		Role:    role,
		Schema:  schema,
		Profile: ProfileQuery,
	}); !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("installPolicy() with system-function ACL error = %v, want ErrPolicyViolation", err)
	}
	if _, err := tx.Exec(ctx,
		"REVOKE EXECUTE ON FUNCTION pg_catalog.pg_sleep(double precision) FROM "+roleID,
	); err != nil {
		t.Fatalf("remove direct system-function ACL: %v", err)
	}
	if _, err := tx.Exec(ctx, "GRANT SET ON PARAMETER session_replication_role TO "+roleID); err != nil {
		t.Fatalf("seed parameter ACL: %v", err)
	}
	if _, err := installPolicy(ctx, tx, InstallInput{
		Role:    role,
		Schema:  schema,
		Profile: ProfileQuery,
	}); !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("installPolicy() with parameter ACL error = %v, want ErrPolicyViolation", err)
	}
	if _, err := tx.Exec(ctx, "REVOKE SET ON PARAMETER session_replication_role FROM "+roleID); err != nil {
		t.Fatalf("remove parameter ACL: %v", err)
	}

	status, err := installPolicy(ctx, tx, InstallInput{
		Role:    role,
		Schema:  schema,
		Profile: ProfileQuery,
	})
	if err != nil {
		t.Fatalf("installPolicy() error = %v", err)
	}
	if status.SchemaVersion != PolicyVersion || status.Profile != ProfileQuery ||
		status.Role != role || status.Schema != schema || status.Database != database ||
		status.ManifestHash == "" || status.ServerVersionNum < 160000 {
		t.Fatalf("installPolicy() status = %+v", status)
	}

	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := loadConnectionEnvironment(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadPrincipalSnapshot(ctx, tx, environment, schema, role)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
		name:             role,
		login:            false,
		exactPrivileges:  true,
		requireSchemaUse: true,
	}); err != nil {
		t.Fatalf("installed role snapshot: %v", err)
	}
	if err := verifyPublicPolicy(ctx, tx, database, schema); err != nil {
		t.Fatalf("installed PUBLIC policy: %v", err)
	}
}

func policyRandomHex(t *testing.T, size int) string {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("generate random suffix: %v", err)
	}
	return hex.EncodeToString(data)
}
