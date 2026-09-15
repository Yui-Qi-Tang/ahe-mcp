package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const resultSchemaVersion = "ahe-migration-result-v1"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	out io.Writer,
) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(out, usage())
			return nil
		}
		return fmt.Errorf("unknown argument %q\n\n%s", args[0], usage())
	}
	databaseURL := getenv("DATABASE_DSN")
	if databaseURL == "" {
		return errors.New("DATABASE_DSN is required")
	}
	schema := getenv("AHE_DATABASE_SCHEMA")
	if err := migrations.ValidateTargetSchema(schema); err != nil {
		return err
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Parse errors can include the original connection string.
		return errors.New("invalid postgres configuration")
	}
	if _, configured := config.ConnConfig.RuntimeParams["search_path"]; !configured {
		config.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("opening postgres pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging postgres: %w", err)
	}

	changed, err := migrations.ApplyUpInSchema(ctx, pool, schema)
	if err != nil {
		return err
	}
	status, err := migrations.VerifyCurrentInSchema(ctx, pool, schema)
	if err != nil {
		return fmt.Errorf("verifying applied migrations: %w", err)
	}
	result := migrationResult{
		SchemaVersion:     resultSchemaVersion,
		Schema:            schema,
		Changed:           changed,
		AppliedMigrations: status.AppliedMigrations,
		LatestMigration:   status.LatestMigration,
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("encoding migration result: %w", err)
	}
	return nil
}

func usage() string {
	return `Usage: ahe-migrate

Environment:
  DATABASE_DSN         PostgreSQL DSN for the authoritative AHE store
  AHE_DATABASE_SCHEMA  Existing private schema to migrate; no fallback schema is allowed

The command acquires the migration advisory lock, applies missing embedded
migrations in one schema-bound transaction, verifies the migration ledger and
required tables, then returns a credential-free JSON result.`
}

type migrationResult struct {
	SchemaVersion     string `json:"schema_version"`
	Schema            string `json:"schema"`
	Changed           bool   `json:"changed"`
	AppliedMigrations int    `json:"applied_migrations"`
	LatestMigration   string `json:"latest_migration"`
}
