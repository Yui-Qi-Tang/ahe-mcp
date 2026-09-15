//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationRunAppliesAndVerifiesMigrations(t *testing.T) {
	fixture := newMigrationCommandFixture(t)
	getenv := func(key string) string {
		switch key {
		case "DATABASE_DSN":
			return fixture.databaseURL
		case "AHE_DATABASE_SCHEMA":
			return fixture.targetSchema
		default:
			return ""
		}
	}

	var firstOut bytes.Buffer
	if err := run(t.Context(), nil, getenv, &firstOut); err != nil {
		t.Fatalf("first run() error = %v", err)
	}
	first := decodeMigrationResult(t, firstOut.Bytes())
	if !first.Changed {
		t.Fatal("first Changed = false, want true")
	}
	if first.SchemaVersion != resultSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", first.SchemaVersion, resultSchemaVersion)
	}
	if first.Schema != fixture.targetSchema {
		t.Fatalf("Schema = %q, want %q", first.Schema, fixture.targetSchema)
	}
	if first.AppliedMigrations != 46 {
		t.Fatalf("AppliedMigrations = %d, want 46", first.AppliedMigrations)
	}
	if first.LatestMigration != "000046_evidence_ingestion_reviewed_disposition.up.sql" {
		t.Fatalf("LatestMigration = %q", first.LatestMigration)
	}

	var secondOut bytes.Buffer
	if err := run(t.Context(), nil, getenv, &secondOut); err != nil {
		t.Fatalf("second run() error = %v", err)
	}
	second := decodeMigrationResult(t, secondOut.Bytes())
	if second.Changed {
		t.Fatal("second Changed = true, want false")
	}
	if second.AppliedMigrations != first.AppliedMigrations ||
		second.LatestMigration != first.LatestMigration ||
		second.Schema != first.Schema {
		t.Fatalf("second result = %+v, want stable %+v", second, first)
	}
	fixture.assertOldSchemaUnchanged(t)
}

func TestIntegrationRunRejectsFallbackSearchPathBeforeWriting(t *testing.T) {
	fixture := newMigrationCommandFixture(t)
	mixedURL := migrationCommandURLWithSearchPath(
		t,
		fixture.databaseURL,
		fixture.targetSchema+", "+fixture.oldSchema,
	)
	err := run(t.Context(), nil, func(key string) string {
		switch key {
		case "DATABASE_DSN":
			return mixedURL
		case "AHE_DATABASE_SCHEMA":
			return fixture.targetSchema
		default:
			return ""
		}
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "migration search_path must contain only") {
		t.Fatalf("run() error = %v, want fallback search_path rejection", err)
	}
	fixture.assertTargetSchemaUntouched(t)
	fixture.assertOldSchemaUnchanged(t)
}

func TestIntegrationRunRejectsMissingSchemaBeforeWriting(t *testing.T) {
	fixture := newMigrationCommandFixture(t)
	missingSchema := fixture.targetSchema + "_missing"
	err := run(t.Context(), nil, func(key string) string {
		switch key {
		case "DATABASE_DSN":
			return fixture.databaseURL
		case "AHE_DATABASE_SCHEMA":
			return missingSchema
		default:
			return ""
		}
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("run() error = %v, want missing schema rejection", err)
	}
	fixture.assertTargetSchemaUntouched(t)
	fixture.assertOldSchemaUnchanged(t)
}

func TestIntegrationRunRejectsPublicSchemaAuthorityBeforeWriting(t *testing.T) {
	fixture := newMigrationCommandFixture(t)
	targetID := pgx.Identifier{fixture.targetSchema}.Sanitize()
	if _, err := fixture.admin.Exec(t.Context(), "GRANT USAGE ON SCHEMA "+targetID+" TO PUBLIC"); err != nil {
		t.Fatalf("grant target schema usage to PUBLIC: %v", err)
	}
	err := run(t.Context(), nil, func(key string) string {
		switch key {
		case "DATABASE_DSN":
			return fixture.databaseURL
		case "AHE_DATABASE_SCHEMA":
			return fixture.targetSchema
		default:
			return ""
		}
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "grants USAGE or CREATE to PUBLIC") {
		t.Fatalf("run() error = %v, want PUBLIC authority rejection", err)
	}
	fixture.assertTargetSchemaUntouched(t)
	fixture.assertOldSchemaUnchanged(t)
}

func decodeMigrationResult(t *testing.T, data []byte) migrationResult {
	t.Helper()
	var result migrationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode migration result: %v\n%s", err, string(data))
	}
	return result
}

type migrationCommandFixture struct {
	databaseURL  string
	targetSchema string
	oldSchema    string
	admin        *pgx.Conn
}

func newMigrationCommandFixture(t *testing.T) migrationCommandFixture {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	targetSchema := "ahe_migrate_command_test_" + hex.EncodeToString(suffix)
	oldSchema := targetSchema + "_old"
	targetID := pgx.Identifier{targetSchema}.Sanitize()
	oldID := pgx.Identifier{oldSchema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+targetID); err != nil {
		t.Fatalf("create target schema: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+oldID); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE TABLE "+oldID+".migration_sentinel (value TEXT NOT NULL)"); err != nil {
		t.Fatalf("create old-schema sentinel: %v", err)
	}
	if _, err := admin.Exec(ctx, "INSERT INTO "+oldID+".migration_sentinel (value) VALUES ('preserved')"); err != nil {
		t.Fatalf("insert old-schema sentinel: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+targetID+" CASCADE")
		_, _ = admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+oldID+" CASCADE")
	})

	databaseURL = migrationCommandURLWithoutSearchPath(t, databaseURL)
	return migrationCommandFixture{
		databaseURL:  databaseURL,
		targetSchema: targetSchema,
		oldSchema:    oldSchema,
		admin:        admin,
	}
}

func migrationCommandURLWithSearchPath(t *testing.T, databaseURL, searchPath string) string {
	t.Helper()
	if databaseURL == "postgresql://" || databaseURL == "postgres://" {
		return databaseURL + "?search_path=" + url.QueryEscape(searchPath)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", searchPath)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func migrationCommandURLWithoutSearchPath(t *testing.T, databaseURL string) string {
	t.Helper()
	if databaseURL == "postgresql://" || databaseURL == "postgres://" {
		return databaseURL
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_DSN: %v", err)
	}
	query := parsed.Query()
	query.Del("search_path")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (f migrationCommandFixture) assertTargetSchemaUntouched(t *testing.T) {
	t.Helper()
	var migrationTable *string
	qualified := pgx.Identifier{f.targetSchema, "schema_migrations"}.Sanitize()
	if err := f.admin.QueryRow(t.Context(), `SELECT pg_catalog.to_regclass($1)::text`, qualified).Scan(&migrationTable); err != nil {
		t.Fatalf("check target schema ledger: %v", err)
	}
	if migrationTable != nil {
		t.Fatalf("target schema ledger survived rejected migration: %q", *migrationTable)
	}
}

func (f migrationCommandFixture) assertOldSchemaUnchanged(t *testing.T) {
	t.Helper()
	oldID := pgx.Identifier{f.oldSchema}.Sanitize()
	var values []string
	rows, err := f.admin.Query(t.Context(), "SELECT value FROM "+oldID+".migration_sentinel ORDER BY value")
	if err != nil {
		t.Fatalf("read old-schema sentinel: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan old-schema sentinel: %v", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate old-schema sentinel: %v", err)
	}
	if len(values) != 1 || values[0] != "preserved" {
		t.Fatalf("old-schema sentinel = %v, want [preserved]", values)
	}

	var migrationTable *string
	qualified := pgx.Identifier{f.oldSchema, "schema_migrations"}.Sanitize()
	if err := f.admin.QueryRow(t.Context(), `SELECT pg_catalog.to_regclass($1)::text`, qualified).Scan(&migrationTable); err != nil {
		t.Fatalf("check old schema ledger: %v", err)
	}
	if migrationTable != nil {
		t.Fatalf("old schema unexpectedly received migration ledger %q", *migrationTable)
	}
}
