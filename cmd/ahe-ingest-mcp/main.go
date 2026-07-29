package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpadmin"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
)

const version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(os.Stdout, "Usage: ahe-ingest-mcp")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "Environment:")
			fmt.Fprintln(os.Stdout, "  DATABASE_DNS  PostgreSQL DSN for the authoritative AHE store")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "The database schema must be current; run ahe-migrate before startup.")
			return nil
		}
		return fmt.Errorf("unknown argument %q", args[0])
	}
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		return errors.New("DATABASE_DNS is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("opening postgres pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging postgres: %w", err)
	}
	if _, err := migrations.VerifyCurrent(ctx, pool); err != nil {
		return fmt.Errorf("verifying database schema: %w", err)
	}

	ingest, err := evidenceingestionmcp.NewServer(pool)
	if err != nil {
		return err
	}
	ingress, err := mcpadmin.NewSourceIngressBackend(ctx, mcpadmin.NewBackend(ingest))
	if err != nil {
		return err
	}
	defer ingress.Close()
	server, err := mcpstdio.NewServer("ahe-ingest-mcp", version, ingress)
	if err != nil {
		return err
	}
	return server.Serve(ctx, os.Stdin, os.Stdout)
}
