//go:build integration

package dbrole

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type publicDatabaseAuthority struct {
	create    bool
	temporary bool
}

func TestIntegrationOpenRuntimePoolBindsEveryPhysicalConnection(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	var database string
	var adminSuperuser bool
	if err := admin.QueryRow(ctx, `
		SELECT current_database(), role_row.rolsuper
		FROM pg_catalog.pg_roles AS role_row
		WHERE role_row.rolname = current_user
	`).Scan(&database, &adminSuperuser); err != nil {
		t.Fatalf("load PostgreSQL test identity: %v", err)
	}
	if !adminSuperuser {
		t.Fatal("runtime role integration requires a superuser fixture owner for reversible role cleanup")
	}
	suffix := policyRandomHex(t, 8)
	schema := "ahe_dbrole_runtime_" + suffix
	groupRole := "ahe_dbrole_query_group_" + suffix
	loginRole := "ahe_dbrole_query_login_" + suffix
	helperRole := "ahe_dbrole_helper_" + suffix
	password := policyRandomHex(t, 24)
	databaseID := pgx.Identifier{database}.Sanitize()
	schemaID := pgx.Identifier{schema}.Sanitize()
	groupID := pgx.Identifier{groupRole}.Sanitize()
	loginID := pgx.Identifier{loginRole}.Sanitize()
	helperID := pgx.Identifier{helperRole}.Sanitize()

	schemaCreated := false
	groupCreated := false
	loginCreated := false
	helperCreated := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		cleanupExec := func(operation, statement string) {
			t.Helper()
			if _, cleanupErr := admin.Exec(cleanupCtx, statement); cleanupErr != nil {
				t.Errorf("%s: %v", operation, cleanupErr)
			}
		}

		if schemaCreated {
			cleanupExec("drop runtime test schema", "DROP SCHEMA IF EXISTS "+schemaID+" CASCADE")
		}
		if loginCreated {
			cleanupExec("drop runtime login grants", "DROP OWNED BY "+loginID)
			cleanupExec("drop runtime login role", "DROP ROLE IF EXISTS "+loginID)
		}
		if groupCreated {
			cleanupExec("drop runtime group grants", "DROP OWNED BY "+groupID)
			cleanupExec("drop runtime group role", "DROP ROLE IF EXISTS "+groupID)
		}
		if helperCreated {
			cleanupExec("drop helper role grants", "DROP OWNED BY "+helperID)
			cleanupExec("drop helper role", "DROP ROLE IF EXISTS "+helperID)
		}
	})

	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatalf("create runtime test schema: %v", err)
	}
	schemaCreated = true

	migrationConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse migration PostgreSQL URL: %v", err)
	}
	migrationConfig.ConnConfig.RuntimeParams["search_path"] = schema
	migrationPool, err := pgxpool.NewWithConfig(ctx, migrationConfig)
	if err != nil {
		t.Fatalf("open migration pool: %v", err)
	}
	t.Cleanup(migrationPool.Close)
	if _, err := migrations.ApplyUp(ctx, migrationPool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	if _, err := admin.Exec(ctx,
		"CREATE ROLE "+groupID+
			" WITH NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS",
	); err != nil {
		t.Fatalf("create NOLOGIN runtime group: %v", err)
	}
	groupCreated = true
	if _, err := admin.Exec(ctx,
		"CREATE ROLE "+loginID+
			" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"+
			" PASSWORD '"+password+"'",
	); err != nil {
		t.Fatalf("create bounded runtime login: %v", err)
	}
	loginCreated = true
	if _, err := admin.Exec(ctx, "CREATE ROLE "+helperID+" NOLOGIN"); err != nil {
		t.Fatalf("create helper-grant control role: %v", err)
	}
	helperCreated = true
	helperFunction := schemaID + ".canonical_supersession_stable_id(text, text[])"
	if _, err := admin.Exec(ctx, "GRANT EXECUTE ON FUNCTION "+helperFunction+" TO "+helperID); err != nil {
		t.Fatalf("grant shipping invoker helper control: %v", err)
	}
	if _, err := admin.Exec(
		ctx,
		"GRANT "+groupID+" TO "+loginID+" WITH ADMIN FALSE, INHERIT FALSE, SET TRUE",
	); err != nil {
		t.Fatalf("grant runtime group membership: %v", err)
	}
	if _, err := admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+databaseID+" TO "+loginID); err != nil {
		t.Fatalf("grant bounded runtime login CONNECT: %v", err)
	}
	publicAuthority, err := loadPublicDatabaseAuthority(ctx, admin, database)
	if err != nil {
		t.Fatal(err)
	}
	if publicAuthority.create || publicAuthority.temporary {
		t.Fatalf(
			"AHE_DBROLE_ACCEPTANCE_DATABASE_DSN must name a dedicated database whose PUBLIC CREATE/TEMPORARY authority is already revoked; got %+v",
			publicAuthority,
		)
	}

	status, err := InstallPolicy(ctx, admin, InstallInput{
		Role:    groupRole,
		Schema:  schema,
		Profile: ProfileQuery,
	})
	if err != nil {
		t.Fatalf("InstallPolicy() error = %v", err)
	}
	if status.Database != database || status.Role != groupRole || status.Schema != schema ||
		status.Profile != ProfileQuery || status.ManifestHash == "" {
		t.Fatalf("InstallPolicy() status = %+v", status)
	}
	var helperStillGranted, queryHelperGranted bool
	if err := admin.QueryRow(ctx, `
		SELECT pg_catalog.has_function_privilege($1, $3, 'EXECUTE'),
		       pg_catalog.has_function_privilege($2, $3, 'EXECUTE')
	`, helperRole, groupRole, helperFunction).Scan(&helperStillGranted, &queryHelperGranted); err != nil {
		t.Fatalf("read shipping invoker-helper privileges: %v", err)
	}
	if !helperStillGranted || queryHelperGranted {
		t.Fatalf("helper privileges = writer control %t, query %t", helperStillGranted, queryHelperGranted)
	}

	runtimeConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse runtime PostgreSQL URL: %v", err)
	}
	runtimeConfig.ConnConfig.User = loginRole
	runtimeConfig.ConnConfig.Password = password
	delete(runtimeConfig.ConnConfig.RuntimeParams, "role")
	delete(runtimeConfig.ConnConfig.RuntimeParams, "search_path")
	runtimeConfig.MaxConns = 3
	runtimePool, binding, err := OpenRuntimePool(ctx, runtimeConfig, RuntimePoolInput{
		Role:    groupRole,
		Schema:  schema,
		Profile: ProfileQuery,
	})
	if err != nil {
		t.Fatalf("OpenRuntimePool() error = %v", err)
	}
	t.Cleanup(runtimePool.Close)
	if binding.Role != groupRole || binding.Database != database || binding.Schema != schema ||
		binding.Profile != ProfileQuery || binding.ManifestHash != status.ManifestHash {
		t.Fatalf("OpenRuntimePool() binding = %+v, install status = %+v", binding, status)
	}

	connections := make([]*pgxpool.Conn, 0, 3)
	defer func() {
		for _, connection := range connections {
			connection.Release()
		}
	}()
	backendPIDs := make(map[int]struct{}, 3)
	for connectionIndex := 0; connectionIndex < 3; connectionIndex++ {
		connection, err := runtimePool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire runtime connection %d: %v", connectionIndex, err)
		}
		connections = append(connections, connection)

		var currentRole, sessionUser string
		var backendPID, migrationCount int
		if err := connection.QueryRow(ctx, `
			SELECT current_user, session_user, pg_catalog.pg_backend_pid(),
			       (SELECT count(*) FROM schema_migrations)
		`).Scan(&currentRole, &sessionUser, &backendPID, &migrationCount); err != nil {
			t.Fatalf("query through runtime connection %d: %v", connectionIndex, err)
		}
		if currentRole != groupRole || sessionUser != loginRole {
			t.Fatalf(
				"runtime connection %d identity = current_user %q / session_user %q, want %q / %q",
				connectionIndex,
				currentRole,
				sessionUser,
				groupRole,
				loginRole,
			)
		}
		if migrationCount == 0 {
			t.Fatalf("runtime connection %d query returned no applied migrations", connectionIndex)
		}
		backendPIDs[backendPID] = struct{}{}
	}
	if len(backendPIDs) != 3 {
		t.Fatalf("runtime pool used %d physical backends, want 3: %v", len(backendPIDs), backendPIDs)
	}
	for _, statement := range []string{
		`INSERT INTO source_blobs DEFAULT VALUES`,
		`UPDATE proposal_occurrences SET admission_outcome = 'pending' WHERE FALSE`,
		`DELETE FROM canonical_graph_edges WHERE FALSE`,
		`TRUNCATE schema_migrations`,
		`CREATE TABLE query_forbidden_table (id TEXT)`,
		`CREATE TEMP TABLE query_forbidden_temp (id TEXT)`,
		`SELECT canonical_supersession_stable_id('denied', ARRAY['input'])`,
		`SET ROLE pg_read_all_data`,
		`SET lo_compat_privileges TO on`,
		`SET session_replication_role TO replica`,
		`UPDATE pg_catalog.pg_settings SET setting = 'replica' WHERE name = 'session_replication_role'`,
	} {
		_, err := connections[0].Exec(ctx, statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("forbidden statement %q: %v, want SQLSTATE 42501", statement, err)
		}
	}

	tampered := connections[0]
	if _, err := tampered.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatalf("reset runtime role for reuse test: %v", err)
	}
	if _, err := tampered.Exec(ctx, `SELECT count(*) FROM `+schemaID+`.source_snapshots`); err == nil {
		t.Fatal("bounded LOGIN retained source access after RESET ROLE")
	}
	tampered.Release()
	connections = connections[1:]
	if connection, err := runtimePool.Acquire(ctx); err == nil {
		connection.Release()
		t.Fatal("reacquire accepted a connection whose runtime role was reset")
	} else if !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("reacquire reset-role error = %v, want ErrPolicyViolation", err)
	}
	replacement, err := runtimePool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire replacement runtime connection: %v", err)
	}
	connections = append(connections, replacement)

	// Existing physical connections must not retain a startup-only witness.
	// Keep the other two connections checked out, so the next checkout must
	// inspect this idle connection after an external ACL change.
	if _, err := admin.Exec(ctx, "GRANT UPDATE ON "+schemaID+".source_snapshots TO "+groupID); err != nil {
		t.Fatalf("seed idle-connection ACL drift: %v", err)
	}
	replacement.Release()
	connections = connections[:len(connections)-1]
	if connection, err := runtimePool.Acquire(ctx); err == nil {
		connection.Release()
		t.Fatal("reacquire accepted UPDATE granted after startup verification")
	} else if !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("reacquire ACL-drift error = %v, want ErrPolicyViolation", err)
	}
	if _, err := admin.Exec(ctx, "REVOKE UPDATE ON "+schemaID+".source_snapshots FROM "+groupID); err != nil {
		t.Fatalf("remove idle-connection ACL drift: %v", err)
	}
	replacement, err = runtimePool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire after restoring Query ACL: %v", err)
	}
	connections = append(connections, replacement)
	var replacementCurrentRole, replacementSessionUser string
	if err := replacement.QueryRow(ctx, `SELECT current_user, session_user`).Scan(
		&replacementCurrentRole,
		&replacementSessionUser,
	); err != nil {
		t.Fatalf("read replacement runtime identity: %v", err)
	}
	if replacementCurrentRole != groupRole || replacementSessionUser != loginRole {
		t.Fatalf(
			"replacement runtime identity = %q/%q, want %q/%q",
			replacementCurrentRole,
			replacementSessionUser,
			groupRole,
			loginRole,
		)
	}
	if _, err := replacement.Exec(ctx, `SET search_path TO `+schemaID+`, public, pg_catalog`); err != nil {
		t.Fatalf("tamper with runtime search path: %v", err)
	}
	replacement.Release()
	connections = connections[:len(connections)-1]
	if connection, err := runtimePool.Acquire(ctx); err == nil {
		connection.Release()
		t.Fatal("reacquire accepted a connection whose runtime search path was widened")
	} else if !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("reacquire widened-search-path error = %v, want ErrPolicyViolation", err)
	}
	replacement, err = runtimePool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire second replacement runtime connection: %v", err)
	}
	connections = append(connections, replacement)
	var beforeCount int
	if err := connections[0].QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&beforeCount); err != nil {
		t.Fatalf("read schema_migrations before forbidden DML: %v", err)
	}
	_, err = connections[0].Exec(ctx, `DELETE FROM schema_migrations WHERE FALSE`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("forbidden DELETE error = %v, want SQLSTATE 42501", err)
	}
	var afterCount int
	if err := connections[0].QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&afterCount); err != nil {
		t.Fatalf("read schema_migrations after forbidden DML: %v", err)
	}
	if afterCount != beforeCount {
		t.Fatalf("schema_migrations count changed after forbidden DML: %d -> %d", beforeCount, afterCount)
	}
}

func loadPublicDatabaseAuthority(
	ctx context.Context,
	db *pgx.Conn,
	database string,
) (publicDatabaseAuthority, error) {
	var authority publicDatabaseAuthority
	if err := db.QueryRow(ctx, `
		SELECT
			COALESCE(pg_catalog.bool_or(acl.privilege_type = 'CREATE'), FALSE),
			COALESCE(pg_catalog.bool_or(acl.privilege_type IN ('TEMPORARY', 'TEMP')), FALSE)
		FROM pg_catalog.pg_database AS database_row
		CROSS JOIN LATERAL pg_catalog.aclexplode(
			COALESCE(database_row.datacl, pg_catalog.acldefault('d', database_row.datdba))
		) AS acl
		WHERE database_row.datname = $1
		  AND acl.grantee = 0
	`, database).Scan(&authority.create, &authority.temporary); err != nil {
		return publicDatabaseAuthority{}, fmt.Errorf("loading PUBLIC database ACL: %w", err)
	}
	return authority, nil
}
