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

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationRunRejectsUnmigratedOwnerBeforeServing(t *testing.T) {
	t.Setenv("DATABASE_DSN", ingestCommandEmptySchemaURL(t))
	t.Setenv("AHE_RUNTIME_PRINCIPAL_ID", "intake-fixture")
	t.Setenv("AHE_RUNTIME_PROFILE", "intake")

	// Runtime authority now precedes schema verification. An owner credential
	// cannot serve even when its unmigrated schema was explicitly selected.
	err := run(t.Context(), nil)
	if !errors.Is(err, dbrole.ErrPolicyViolation) {
		t.Fatalf("run() error = %v, want ErrPolicyViolation", err)
	}
	if !strings.Contains(err.Error(), "session user must be distinct") {
		t.Fatalf("run() error = %v, want login/group separation failure", err)
	}
}

func ingestCommandEmptySchemaURL(t *testing.T) string {
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
	schema := "ahe_ingest_command_test_" + hex.EncodeToString(suffix)
	t.Setenv("AHE_DATABASE_SCHEMA", schema)
	t.Setenv("AHE_DATABASE_ROLE", admin.Config().User)
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
		t.Fatalf("parse DATABASE_DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
