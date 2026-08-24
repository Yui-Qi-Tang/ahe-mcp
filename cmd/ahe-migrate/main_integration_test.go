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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationRunAppliesAndVerifiesMigrations(t *testing.T) {
	databaseURL := migrationCommandDatabaseURL(t)
	getenv := func(key string) string {
		if key == "DATABASE_DNS" {
			return databaseURL
		}
		return ""
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
	if first.AppliedMigrations != 41 {
		t.Fatalf("AppliedMigrations = %d, want 41", first.AppliedMigrations)
	}
	if first.LatestMigration != "000041_evidence_ingestion_canonical_supersessions.up.sql" {
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
		second.LatestMigration != first.LatestMigration {
		t.Fatalf("second result = %+v, want stable %+v", second, first)
	}
}

func decodeMigrationResult(t *testing.T, data []byte) migrationResult {
	t.Helper()
	var result migrationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode migration result: %v\n%s", err, string(data))
	}
	return result
}

func migrationCommandDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
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
	schema := "ahe_migrate_command_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_DNS: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
