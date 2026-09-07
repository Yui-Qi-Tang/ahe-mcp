package dbrole

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RuntimePoolInput fixes the PostgreSQL group role, schema, and capability
// profile for one serving process. Callers must derive Profile from the
// process binary or its already validated public tool profile; it is not a
// request parameter.
type RuntimePoolInput struct {
	Role    string
	Schema  string
	Profile Profile
}

// RuntimeBinding is a credential-free witness for the policy a pool enforces
// on every newly opened physical connection and rechecks before each checkout.
type RuntimeBinding struct {
	SchemaVersion string  `json:"schema_version"`
	Profile       Profile `json:"profile"`
	Role          string  `json:"role"`
	Database      string  `json:"database"`
	Schema        string  `json:"schema"`
	ManifestHash  string  `json:"manifest_hash"`
}

// OpenRuntimePool opens a PostgreSQL pool whose every physical connection is
// fail-closed against one installed database-role policy. The DSN must name a
// bounded LOGIN role with exactly one non-admin SET membership in input.Role;
// input.Role itself must be the corresponding NOLOGIN group role.
func OpenRuntimePool(
	ctx context.Context,
	config *pgxpool.Config,
	input RuntimePoolInput,
) (*pgxpool.Pool, RuntimeBinding, error) {
	if ctx == nil {
		return nil, RuntimeBinding{}, errors.New("context is required")
	}
	if config == nil {
		return nil, RuntimeBinding{}, errors.New("postgres pool configuration is required")
	}
	if err := validateClosedPoolHooks(config); err != nil {
		return nil, RuntimeBinding{}, err
	}
	if err := validateIdentifier("role", input.Role); err != nil {
		return nil, RuntimeBinding{}, err
	}
	if err := validateIdentifier("schema", input.Schema); err != nil {
		return nil, RuntimeBinding{}, err
	}
	manifest, err := BuildManifest(input.Profile)
	if err != nil {
		return nil, RuntimeBinding{}, err
	}
	hash, err := manifest.Hash()
	if err != nil {
		return nil, RuntimeBinding{}, fmt.Errorf("hashing database role manifest: %w", err)
	}
	poolConfig := config.Copy()
	expectedSessionUser := poolConfig.ConnConfig.User
	expectedDatabase := poolConfig.ConnConfig.Database
	if expectedSessionUser == "" {
		return nil, RuntimeBinding{}, errors.New("postgres pool configuration must fix one session user")
	}
	if expectedDatabase == "" {
		return nil, RuntimeBinding{}, errors.New("postgres pool configuration must fix one database")
	}
	binding := RuntimeBinding{
		SchemaVersion: PolicyVersion,
		Profile:       input.Profile,
		Role:          input.Role,
		Database:      expectedDatabase,
		Schema:        input.Schema,
		ManifestHash:  hash,
	}
	poolConfig.AfterConnect = func(connectCtx context.Context, conn *pgx.Conn) error {
		if err := bindRuntimeConnection(
			connectCtx,
			conn,
			input,
			expectedSessionUser,
			expectedDatabase,
		); err != nil {
			return fmt.Errorf("binding postgres runtime connection: %w", err)
		}
		return nil
	}
	poolConfig.PrepareConn = func(acquireCtx context.Context, conn *pgx.Conn) (bool, error) {
		if err := verifyRuntimeSessionBinding(
			acquireCtx,
			conn,
			input,
			expectedSessionUser,
			expectedDatabase,
		); err != nil {
			return false, fmt.Errorf("verifying reused postgres runtime connection: %w", err)
		}
		return true, nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, RuntimeBinding{}, fmt.Errorf("opening policy-bound postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, RuntimeBinding{}, fmt.Errorf("verifying first policy-bound postgres connection: %w", err)
	}
	return pool, binding, nil
}

// ValidateRuntimeBinding verifies a credential-free witness before another
// runtime boundary relies on it. It never opens PostgreSQL or expands the
// profile selected by the process composition root.
func ValidateRuntimeBinding(binding RuntimeBinding, expectedProfile Profile) error {
	if binding.SchemaVersion != PolicyVersion {
		return fmt.Errorf(
			"database runtime binding schema_version = %q, want %q",
			binding.SchemaVersion,
			PolicyVersion,
		)
	}
	if binding.Profile != expectedProfile {
		return fmt.Errorf(
			"database runtime binding profile = %q, want %q",
			binding.Profile,
			expectedProfile,
		)
	}
	if err := validateIdentifier("role", binding.Role); err != nil {
		return fmt.Errorf("database runtime binding: %w", err)
	}
	if err := validateExactIdentifier("database", binding.Database); err != nil {
		return fmt.Errorf("database runtime binding: %w", err)
	}
	if err := validateIdentifier("schema", binding.Schema); err != nil {
		return fmt.Errorf("database runtime binding: %w", err)
	}
	manifest, err := BuildManifest(expectedProfile)
	if err != nil {
		return err
	}
	expectedHash, err := manifest.Hash()
	if err != nil {
		return fmt.Errorf("hashing expected database role manifest: %w", err)
	}
	if binding.ManifestHash != expectedHash {
		return policyViolation(
			"database runtime binding manifest_hash = %q, want %q",
			binding.ManifestHash,
			expectedHash,
		)
	}
	return nil
}

func validateClosedPoolHooks(config *pgxpool.Config) error {
	if config.ConnConfig == nil {
		return errors.New("postgres pool configuration must include a connection configuration")
	}
	if config.BeforeConnect != nil {
		return errors.New("postgres pool configuration must not define BeforeConnect")
	}
	if config.AfterConnect != nil {
		return errors.New("postgres pool configuration must not define AfterConnect")
	}
	if config.BeforeAcquire != nil {
		return errors.New("postgres pool configuration must not define BeforeAcquire")
	}
	if config.PrepareConn != nil {
		return errors.New("postgres pool configuration must not define PrepareConn")
	}
	if config.AfterRelease != nil {
		return errors.New("postgres pool configuration must not define AfterRelease")
	}
	if config.ConnConfig.ValidateConnect != nil {
		return errors.New("postgres connection configuration must not define ValidateConnect")
	}
	if config.ConnConfig.AfterConnect != nil {
		return errors.New("postgres connection configuration must not define pgconn AfterConnect")
	}
	if config.ConnConfig.Tracer != nil {
		return errors.New("postgres connection configuration must not define Tracer")
	}
	return nil
}

func bindRuntimeConnection(
	ctx context.Context,
	conn *pgx.Conn,
	input RuntimePoolInput,
	expectedSessionUser string,
	expectedDatabase string,
) error {
	var currentRole, sessionUser, database string
	if err := conn.QueryRow(ctx, `SELECT current_user, session_user, current_database()`).Scan(
		&currentRole,
		&sessionUser,
		&database,
	); err != nil {
		return fmt.Errorf("loading initial postgres connection identity: %w", err)
	}
	if currentRole != sessionUser {
		return policyViolation(
			"initial current_user = %q, want unchanged session_user %q",
			currentRole,
			sessionUser,
		)
	}
	if sessionUser == input.Role {
		return policyViolation("session user must be distinct from NOLOGIN runtime role %q", input.Role)
	}
	if sessionUser != expectedSessionUser {
		return policyViolation("session_user = %q, want configured login %q", sessionUser, expectedSessionUser)
	}
	if database != expectedDatabase {
		return policyViolation("current_database() = %q, want configured database %q", database, expectedDatabase)
	}
	if _, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{input.Role}.Sanitize()); err != nil {
		return fmt.Errorf("setting postgres runtime role %q: %w", input.Role, err)
	}
	expectedSearchPath := runtimeSearchPath(input.Schema)
	if _, err := conn.Exec(
		ctx,
		`SELECT pg_catalog.set_config('search_path', $1, false)`,
		expectedSearchPath,
	); err != nil {
		return fmt.Errorf("fixing postgres runtime search path: %w", err)
	}
	if _, err := VerifyRuntimeConnection(ctx, conn, RuntimeVerificationInput{
		Role:        input.Role,
		SessionUser: sessionUser,
		Schema:      input.Schema,
		Profile:     input.Profile,
	}); err != nil {
		return err
	}
	return nil
}

func verifyRuntimeSessionBinding(
	ctx context.Context,
	conn *pgx.Conn,
	input RuntimePoolInput,
	expectedSessionUser string,
	expectedDatabase string,
) error {
	var currentRole, sessionUser, database, currentSearchPath string
	var sessionReplicationRole, largeObjectCompatibility string
	if err := conn.QueryRow(ctx, `
		SELECT
			current_user,
			session_user,
			current_database(),
			current_setting('search_path'),
			current_setting('session_replication_role'),
			current_setting('lo_compat_privileges')
	`).Scan(
		&currentRole,
		&sessionUser,
		&database,
		&currentSearchPath,
		&sessionReplicationRole,
		&largeObjectCompatibility,
	); err != nil {
		return fmt.Errorf("loading reused postgres connection identity: %w", err)
	}
	if currentRole != input.Role {
		return policyViolation("reused current_user = %q, want %q", currentRole, input.Role)
	}
	if sessionUser != expectedSessionUser {
		return policyViolation("reused session_user = %q, want configured login %q", sessionUser, expectedSessionUser)
	}
	if database != expectedDatabase {
		return policyViolation("reused current_database() = %q, want configured database %q", database, expectedDatabase)
	}
	expectedSearchPath := runtimeSearchPath(input.Schema)
	if currentSearchPath != expectedSearchPath {
		return policyViolation("reused search_path = %q, want %q", currentSearchPath, expectedSearchPath)
	}
	if err := validateCriticalRuntimeSettings(connectionEnvironment{
		sessionReplicationRole:   sessionReplicationRole,
		largeObjectCompatibility: largeObjectCompatibility,
	}); err != nil {
		return err
	}
	// ACL and membership may change while an idle connection stays open. A
	// startup witness alone must not authorize its next checkout.
	_, err := VerifyRuntimeConnection(ctx, conn, RuntimeVerificationInput{
		Role: input.Role, SessionUser: expectedSessionUser,
		Schema: input.Schema, Profile: input.Profile,
	})
	return err
}

func runtimeSearchPath(schema string) string {
	return pgx.Identifier{schema}.Sanitize() + ", pg_catalog"
}
