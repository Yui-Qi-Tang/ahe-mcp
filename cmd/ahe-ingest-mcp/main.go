package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpadmin"
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
			fmt.Fprintln(os.Stdout, "Usage: ahe-ingest-mcp")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "Environment:")
			fmt.Fprintln(os.Stdout, "  DATABASE_DNS  PostgreSQL DSN for the authoritative AHE store")
			fmt.Fprintln(os.Stdout, "  AHE_RUNTIME_PRINCIPAL_ID  Launcher-fixed identity (not proof of human review)")
			fmt.Fprintln(os.Stdout, "  AHE_RUNTIME_PROFILE       intake or source-claim-reviewer (legacy writers remain disabled)")
			fmt.Fprintln(os.Stdout, "  AHE_DATABASE_ROLE         Installed matching NOLOGIN role")
			fmt.Fprintln(os.Stdout, "  AHE_DATABASE_SCHEMA       Explicit authoritative schema")
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "The database schema must be current; run ahe-migrate before startup.")
			return nil
		}
		return fmt.Errorf("unknown argument %q", args[0])
	}
	principal, err := runtimeauth.NewPrincipal(os.Getenv("AHE_RUNTIME_PRINCIPAL_ID"))
	if err != nil {
		return fmt.Errorf("configuring ingestion runtime identity: %w", err)
	}
	profile, err := mcpadmin.ParseRuntimeProfile(os.Getenv("AHE_RUNTIME_PROFILE"))
	if err != nil {
		return err
	}
	var databaseProfile dbrole.Profile
	switch profile {
	case mcpadmin.RuntimeProfileIntake:
		databaseProfile = dbrole.ProfileIntake
	case mcpadmin.RuntimeProfileSourceClaimReviewer:
		databaseProfile = dbrole.ProfileSourceClaimReviewer
	default:
		return errors.New("legacy writer runtime profiles are not enabled until their database authority companions are qualified")
	}
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		return errors.New("DATABASE_DNS is required")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid postgres configuration")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{
		Role:    os.Getenv("AHE_DATABASE_ROLE"),
		Schema:  os.Getenv("AHE_DATABASE_SCHEMA"),
		Profile: databaseProfile,
	})
	if err != nil {
		return fmt.Errorf("opening ingestion database role: %w", err)
	}
	defer pool.Close()
	if _, err := migrations.VerifyCurrentInSchema(ctx, pool, os.Getenv("AHE_DATABASE_SCHEMA")); err != nil {
		return fmt.Errorf("verifying database schema: %w", err)
	}

	var ingest *evidenceingestionmcp.Server
	if profile == mcpadmin.RuntimeProfileSourceClaimReviewer {
		ingest, err = evidenceingestionmcp.NewSourceClaimReviewerServer(pool, principal)
	} else {
		ingest, err = evidenceingestionmcp.NewServer(pool)
	}
	if err != nil {
		return err
	}
	var backend mcpstdio.Backend = mcpadmin.NewBackend(ingest)
	if profile == mcpadmin.RuntimeProfileIntake {
		ingress, err := mcpadmin.NewSourceIngressBackend(ctx, backend)
		if err != nil {
			return err
		}
		defer ingress.Close()
		backend = ingress
	}
	// Authorization surrounds the queue so denied calls cannot enter it.
	authorized, err := mcpadmin.NewAuthorizedBackend(backend, principal, profile)
	if err != nil {
		return err
	}
	server, err := mcpstdio.NewServer("ahe-ingest-mcp", version, authorized)
	if err != nil {
		return err
	}
	return server.Serve(ctx, os.Stdin, os.Stdout)
}
