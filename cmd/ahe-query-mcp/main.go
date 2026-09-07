package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpquery"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
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
			fmt.Fprintln(os.Stdout, "Usage: ahe-query-mcp")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "Environment:")
			fmt.Fprintln(os.Stdout, "  DATABASE_DNS  PostgreSQL DSN for the authoritative AHE store")
			fmt.Fprintln(os.Stdout, "  AHE_RUNTIME_PRINCIPAL_ID  Launcher-fixed query consumer identity")
			fmt.Fprintln(os.Stdout, "  AHE_DATABASE_ROLE         Installed NOLOGIN query role")
			fmt.Fprintln(os.Stdout, "  AHE_DATABASE_SCHEMA       Explicit authoritative schema/visibility boundary")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "The database schema must be current; run ahe-migrate before startup.")
			return nil
		}
		return fmt.Errorf("unknown argument %q", args[0])
	}
	principal, err := runtimeauth.NewPrincipal(os.Getenv("AHE_RUNTIME_PRINCIPAL_ID"))
	if err != nil {
		return fmt.Errorf("configuring query runtime identity: %w", err)
	}
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		return errors.New("DATABASE_DNS is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Parse errors can include the original connection string.
		return errors.New("invalid postgres configuration")
	}
	pool, binding, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{
		Role:    os.Getenv("AHE_DATABASE_ROLE"),
		Schema:  os.Getenv("AHE_DATABASE_SCHEMA"),
		Profile: dbrole.ProfileQuery,
	})
	if err != nil {
		return fmt.Errorf("opening query database role: %w", err)
	}
	defer pool.Close()
	if _, err := migrations.VerifyCurrentInSchema(ctx, pool, os.Getenv("AHE_DATABASE_SCHEMA")); err != nil {
		return fmt.Errorf("verifying database schema: %w", err)
	}
	authorization, err := mcpquery.NewQueryRuntimeAuthorization(principal.ID, binding)
	if err != nil {
		return fmt.Errorf("binding query runtime authority: %w", err)
	}

	query, err := evidencequerymcp.NewServer(pool)
	if err != nil {
		return err
	}
	backend, err := mcpquery.NewAuthorizedQueryBackend(mcpquery.NewBackend(query), authorization)
	if err != nil {
		return err
	}
	server, err := mcpstdio.NewServer("ahe-query-mcp", version, backend)
	if err != nil {
		return err
	}
	return server.Serve(ctx, os.Stdin, os.Stdout)
}
