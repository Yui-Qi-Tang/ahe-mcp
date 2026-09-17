package dbrole

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrPolicyViolation identifies a database role or ACL state that is broader
// than the selected closed manifest.
var ErrPolicyViolation = errors.New("database role policy violation")

// InstallInput selects one pre-existing NOLOGIN group role and schema. The
// installer never creates roles, login credentials, or memberships.
type InstallInput struct {
	Role    string
	Schema  string
	Profile Profile
}

// RuntimeVerificationInput binds one physical PostgreSQL connection to the
// exact launcher login, SET ROLE target, schema, and closed profile expected by
// the caller.
type RuntimeVerificationInput struct {
	Role        string
	SessionUser string
	Schema      string
	Profile     Profile
}

// PolicyStatus is a credential-free witness for one successful install or
// physical-connection verification.
type PolicyStatus struct {
	SchemaVersion    string
	Profile          Profile
	Role             string
	SessionUser      string
	Schema           string
	Database         string
	ManifestHash     string
	ServerVersionNum int
}

type policyDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type connectionEnvironment struct {
	database                 string
	currentSchema            string
	currentRole              string
	sessionUser              string
	serverVersionNum         int
	sessionReplicationRole   string
	largeObjectCompatibility string
}

type relationInventory struct {
	name    string
	columns []string
}

// InstallPolicy transactionally replaces the effective ACL of one existing
// NOLOGIN group role with the selected manifest. It also removes PUBLIC
// schema, table, column, and function authority in the selected schema. The
// database-wide PUBLIC CREATE/TEMPORARY boundary must already be closed by an
// explicit deployment step; this installer verifies it but does not mutate it.
// Explicit grants to other roles are untouched. In particular, shipping
// Supersession writers must retain their separately provisioned invoker-helper
// EXECUTE grants; the query/intake policies do not provision admission roles.
// Call this only from a trusted one-shot owner or migrator connection.
func InstallPolicy(ctx context.Context, conn *pgx.Conn, input InstallInput) (PolicyStatus, error) {
	if ctx == nil {
		return PolicyStatus{}, errors.New("context is required")
	}
	if conn == nil {
		return PolicyStatus{}, errors.New("postgres connection is required")
	}
	if err := validateInstallInput(input); err != nil {
		return PolicyStatus{}, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PolicyStatus{}, fmt.Errorf("beginning database role policy install: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	status, err := installPolicy(ctx, tx, input)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyStatus{}, fmt.Errorf("committing database role policy install: %w", err)
	}
	return status, nil
}

// VerifyRuntimeConnection fails closed unless this exact physical connection
// is running as the expected NOLOGIN group role on behalf of the expected
// bounded LOGIN session user and neither identity has authority outside the
// selected manifest. Pool callers must invoke it once for every newly opened
// *pgx.Conn, for example from pgxpool.Config.AfterConnect.
func VerifyRuntimeConnection(
	ctx context.Context,
	conn *pgx.Conn,
	input RuntimeVerificationInput,
) (PolicyStatus, error) {
	if ctx == nil {
		return PolicyStatus{}, errors.New("context is required")
	}
	if conn == nil {
		return PolicyStatus{}, errors.New("postgres connection is required")
	}
	if err := validateRuntimeInput(input); err != nil {
		return PolicyStatus{}, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return PolicyStatus{}, fmt.Errorf("beginning database role policy verification: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	status, err := verifyRuntimeConnection(ctx, tx, input)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyStatus{}, fmt.Errorf("committing database role policy verification: %w", err)
	}
	return status, nil
}

func installPolicy(ctx context.Context, db policyDB, input InstallInput) (PolicyStatus, error) {
	manifest, err := BuildManifest(input.Profile)
	if err != nil {
		return PolicyStatus{}, err
	}
	environment, err := loadConnectionEnvironment(ctx, db)
	if err != nil {
		return PolicyStatus{}, err
	}
	role, err := loadRoleAttributes(ctx, db, input.Role)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := validateSafeRoleAttributes(role, input.Role, false); err != nil {
		return PolicyStatus{}, err
	}
	memberships, err := loadRoleMemberships(ctx, db, input.Role)
	if err != nil {
		return PolicyStatus{}, err
	}
	if len(memberships) != 0 {
		return PolicyStatus{}, policyViolation("NOLOGIN group role %q has outbound membership %+v", input.Role, memberships)
	}
	adminMembers, err := loadRoleAdminMembers(ctx, db, input.Role)
	if err != nil {
		return PolicyStatus{}, err
	}
	if len(adminMembers) != 0 {
		return PolicyStatus{}, policyViolation("NOLOGIN group role %q has membership administrators %v", input.Role, adminMembers)
	}
	ownership, err := loadOwnership(ctx, db, environment.database, input.Role)
	if err != nil {
		return PolicyStatus{}, err
	}
	if ownership.any() {
		return PolicyStatus{}, policyViolation("NOLOGIN group role %q owns database or schema objects: %+v", input.Role, ownership)
	}

	relations, err := loadRelationInventory(ctx, db, input.Schema)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := requireManifestRelations(manifest, relations); err != nil {
		return PolicyStatus{}, err
	}
	statements, err := policyStatements(environment.database, input.Schema, input.Role, manifest, relations)
	if err != nil {
		return PolicyStatus{}, err
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement); err != nil {
			return PolicyStatus{}, fmt.Errorf("applying database role policy statement %q: %w", statement, err)
		}
	}
	if err := verifyPublicPolicy(ctx, db, environment.database, input.Schema); err != nil {
		return PolicyStatus{}, err
	}
	snapshot, err := loadPrincipalSnapshot(ctx, db, environment, input.Schema, input.Role)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
		name:               input.Role,
		login:              false,
		exactPrivileges:    true,
		requireSchemaUse:   true,
		rejectAdminMembers: true,
	}); err != nil {
		return PolicyStatus{}, err
	}
	return newPolicyStatus(manifest, input.Role, "", input.Schema, environment), nil
}

func verifyRuntimeConnection(
	ctx context.Context,
	db policyDB,
	input RuntimeVerificationInput,
) (PolicyStatus, error) {
	manifest, err := BuildManifest(input.Profile)
	if err != nil {
		return PolicyStatus{}, err
	}
	environment, err := loadConnectionEnvironment(ctx, db)
	if err != nil {
		return PolicyStatus{}, err
	}
	if environment.currentRole != input.Role {
		return PolicyStatus{}, policyViolation("current_user = %q, want %q", environment.currentRole, input.Role)
	}
	if environment.sessionUser != input.SessionUser {
		return PolicyStatus{}, policyViolation("session_user = %q, want %q", environment.sessionUser, input.SessionUser)
	}
	if environment.currentSchema != input.Schema {
		return PolicyStatus{}, policyViolation("current_schema() = %q, want %q", environment.currentSchema, input.Schema)
	}
	if err := validateCriticalRuntimeSettings(environment); err != nil {
		return PolicyStatus{}, err
	}
	if err := verifyPublicPolicy(ctx, db, environment.database, input.Schema); err != nil {
		return PolicyStatus{}, err
	}
	group, err := loadPrincipalSnapshot(ctx, db, environment, input.Schema, input.Role)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := validatePrincipalSnapshot(group, manifest, principalExpectation{
		name:               input.Role,
		login:              false,
		exactPrivileges:    true,
		requireSchemaUse:   true,
		rejectAdminMembers: true,
	}); err != nil {
		return PolicyStatus{}, err
	}
	session, err := loadPrincipalSnapshot(ctx, db, environment, input.Schema, input.SessionUser)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := validatePrincipalSnapshot(session, manifest, principalExpectation{
		name:              input.SessionUser,
		login:             true,
		allowedMembership: input.Role,
	}); err != nil {
		return PolicyStatus{}, err
	}
	return newPolicyStatus(manifest, input.Role, input.SessionUser, input.Schema, environment), nil
}

func validateInstallInput(input InstallInput) error {
	if err := validateIdentifier("role", input.Role); err != nil {
		return err
	}
	if err := validateIdentifier("schema", input.Schema); err != nil {
		return err
	}
	_, err := BuildManifest(input.Profile)
	return err
}

func validateRuntimeInput(input RuntimeVerificationInput) error {
	if err := validateIdentifier("role", input.Role); err != nil {
		return err
	}
	if err := validateIdentifier("session user", input.SessionUser); err != nil {
		return err
	}
	if err := validateIdentifier("schema", input.Schema); err != nil {
		return err
	}
	if input.Role == input.SessionUser {
		return errors.New("runtime role and session user must be distinct")
	}
	_, err := BuildManifest(input.Profile)
	return err
}

func validateIdentifier(label, value string) error {
	if err := validateExactIdentifier(label, value); err != nil {
		return err
	}
	if strings.EqualFold(value, "PUBLIC") {
		return fmt.Errorf("%s must not be PUBLIC", label)
	}
	return nil
}

func validateExactIdentifier(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be an exact PostgreSQL identifier", label)
	}
	return nil
}

func loadConnectionEnvironment(ctx context.Context, db policyDB) (connectionEnvironment, error) {
	var environment connectionEnvironment
	if err := db.QueryRow(ctx, `
		SELECT
			current_database(),
			COALESCE(current_schema(), ''),
			current_user,
			session_user,
			current_setting('server_version_num')::INTEGER,
			current_setting('session_replication_role'),
			current_setting('lo_compat_privileges')
	`).Scan(
		&environment.database,
		&environment.currentSchema,
		&environment.currentRole,
		&environment.sessionUser,
		&environment.serverVersionNum,
		&environment.sessionReplicationRole,
		&environment.largeObjectCompatibility,
	); err != nil {
		return connectionEnvironment{}, fmt.Errorf("loading PostgreSQL connection identity: %w", err)
	}
	return environment, nil
}

func loadRelationInventory(ctx context.Context, db policyDB, schema string) ([]relationInventory, error) {
	rows, err := db.Query(ctx, `
		SELECT relation.relname, attribute.attname
		FROM pg_catalog.pg_class AS relation
		JOIN pg_catalog.pg_namespace AS relation_ns
		  ON relation_ns.oid = relation.relnamespace
		LEFT JOIN pg_catalog.pg_attribute AS attribute
		  ON attribute.attrelid = relation.oid
		 AND attribute.attnum > 0
		 AND NOT attribute.attisdropped
		WHERE relation_ns.nspname = $1
		  AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
		ORDER BY relation.relname, attribute.attnum
	`, schema)
	if err != nil {
		return nil, fmt.Errorf("listing policy relations: %w", err)
	}
	defer rows.Close()

	var result []relationInventory
	for rows.Next() {
		var table string
		var column *string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, fmt.Errorf("scanning policy relation: %w", err)
		}
		if len(result) == 0 || result[len(result)-1].name != table {
			result = append(result, relationInventory{name: table})
		}
		if column != nil {
			result[len(result)-1].columns = append(result[len(result)-1].columns, *column)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating policy relations: %w", err)
	}
	return result, nil
}

func requireManifestRelations(manifest Manifest, relations []relationInventory) error {
	physical := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		physical[relation.name] = struct{}{}
	}
	for _, rule := range manifest.Tables {
		if _, ok := physical[rule.Table]; !ok {
			return policyViolation("required table %q is missing from the policy schema", rule.Table)
		}
	}
	return nil
}

func policyStatements(
	database string,
	schema string,
	role string,
	manifest Manifest,
	relations []relationInventory,
) ([]string, error) {
	if err := validateIdentifier("database", database); err != nil {
		return nil, err
	}
	if err := validateIdentifier("schema", schema); err != nil {
		return nil, err
	}
	if err := validateIdentifier("role", role); err != nil {
		return nil, err
	}
	if err := requireManifestRelations(manifest, relations); err != nil {
		return nil, err
	}
	schemaID := pgx.Identifier{schema}.Sanitize()
	roleID := pgx.Identifier{role}.Sanitize()
	statements := []string{
		"REVOKE ALL PRIVILEGES ON DATABASE " + pgx.Identifier{database}.Sanitize() + " FROM " + roleID,
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{database}.Sanitize() + " TO " + roleID,
		"REVOKE ALL PRIVILEGES ON SCHEMA " + schemaID + " FROM PUBLIC",
		"REVOKE ALL PRIVILEGES ON SCHEMA " + schemaID + " FROM " + roleID,
		"GRANT USAGE ON SCHEMA " + schemaID + " TO " + roleID,
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA " + schemaID + " FROM PUBLIC",
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA " + schemaID + " FROM " + roleID,
		"REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA " + schemaID + " FROM PUBLIC",
		"REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA " + schemaID + " FROM " + roleID,
	}
	for _, relation := range relations {
		if len(relation.columns) == 0 {
			continue
		}
		columnIDs := make([]string, 0, len(relation.columns))
		for _, column := range relation.columns {
			columnIDs = append(columnIDs, pgx.Identifier{column}.Sanitize())
		}
		tableID := pgx.Identifier{schema, relation.name}.Sanitize()
		columns := strings.Join(columnIDs, ", ")
		statements = append(statements,
			"REVOKE ALL PRIVILEGES ("+columns+") ON TABLE "+tableID+" FROM PUBLIC",
			"REVOKE ALL PRIVILEGES ("+columns+") ON TABLE "+tableID+" FROM "+roleID,
		)
	}
	for _, rule := range manifest.Tables {
		privileges := make([]string, 0, len(rule.Privileges))
		for _, privilege := range rule.Privileges {
			privileges = append(privileges, string(privilege))
		}
		if len(privileges) == 0 {
			continue
		}
		statements = append(statements,
			"GRANT "+strings.Join(privileges, ", ")+" ON TABLE "+
				pgx.Identifier{schema, rule.Table}.Sanitize()+" TO "+roleID,
		)
	}
	if manifest.Profile == ProfileEndpointReviewer {
		statements = append(statements, "GRANT EXECUTE ON FUNCTION "+pgx.Identifier{schema, "canonical_endpoint_lock_nodes_v1"}.Sanitize()+"(text[]) TO "+roleID)
	}
	return statements, nil
}

func newPolicyStatus(
	manifest Manifest,
	role string,
	sessionUser string,
	schema string,
	environment connectionEnvironment,
) PolicyStatus {
	hash, _ := manifest.Hash()
	return PolicyStatus{
		SchemaVersion:    PolicyVersion,
		Profile:          manifest.Profile,
		Role:             role,
		SessionUser:      sessionUser,
		Schema:           schema,
		Database:         environment.database,
		ManifestHash:     hash,
		ServerVersionNum: environment.serverVersionNum,
	}
}

func policyViolation(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPolicyViolation, fmt.Sprintf(format, args...))
}
