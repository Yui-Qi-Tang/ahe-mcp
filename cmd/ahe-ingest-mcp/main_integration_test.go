//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationRunRejectsUnmigratedSchema(t *testing.T) {
	t.Setenv("DATABASE_DNS", ingestCommandEmptySchemaURL(t))

	err := run(t.Context(), nil)
	if !errors.Is(err, migrations.ErrSchemaNotCurrent) {
		t.Fatalf("run() error = %v, want ErrSchemaNotCurrent", err)
	}
	if !strings.Contains(err.Error(), "run ahe-migrate") {
		t.Fatalf("run() error = %v, want migration action", err)
	}
}

func ingestCommandEmptySchemaURL(t *testing.T) string {
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
	schema := "ahe_ingest_command_test_" + hex.EncodeToString(suffix)
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
