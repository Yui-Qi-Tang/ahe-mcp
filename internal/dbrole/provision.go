package dbrole

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProvisionInput selects a fresh group/LOGIN pair in a separately approved
// dedicated database and already migrated private schema. Authentication,
// database creation, migration and database-wide PUBLIC ACLs stay external.
type ProvisionInput struct {
	Database    string
	Schema      string
	Role        string
	SessionUser string
	Profile     Profile
}

// ErrRoleExists refuses adoption of either pre-existing role, including a
// partially configured pair. Use independent runtime verification after an
// uncertain provisioning result; never replace existing identities on retry.
var ErrRoleExists = errors.New("runtime provisioning role already exists")

// ProvisionRuntime atomically creates one new bounded role pair and installs
// its native policy. This first operator entrypoint requires an unchanged
// superuser session: non-superuser CREATE ROLE can add creator-admin membership
// on PostgreSQL 16+, which the closed runtime policy deliberately refuses.
// No password, credential file, schema or database is created. A successful
// return verifies role policy, not external authentication of the new LOGIN.
func ProvisionRuntime(ctx context.Context, conn *pgx.Conn, input ProvisionInput) (PolicyStatus, error) {
	if ctx == nil || conn == nil {
		return PolicyStatus{}, errors.New("context and postgres connection are required")
	}
	if err := validateProvisionInput(input); err != nil {
		return PolicyStatus{}, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PolicyStatus{}, fmt.Errorf("beginning runtime provisioning: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	environment, err := loadConnectionEnvironment(ctx, tx)
	if err != nil {
		return PolicyStatus{}, err
	}
	if environment.database != input.Database || environment.currentSchema != input.Schema ||
		environment.currentRole != environment.sessionUser || environment.serverVersionNum < 160000 {
		return PolicyStatus{}, policyViolation("provisioning requires the exact database/schema and an unchanged PostgreSQL 16+ operator session")
	}
	operator, err := loadRoleAttributes(ctx, tx, environment.currentRole)
	if err != nil {
		return PolicyStatus{}, err
	}
	if !operator.superuser {
		return PolicyStatus{}, policyViolation("fresh role provisioning requires a separately authorized superuser")
	}
	if err := validateCriticalRuntimeSettings(environment); err != nil {
		return PolicyStatus{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_roles WHERE rolname IN ($1, $2)
	)`, input.Role, input.SessionUser).Scan(&exists); err != nil {
		return PolicyStatus{}, fmt.Errorf("checking fresh role names: %w", err)
	}
	if exists {
		return PolicyStatus{}, ErrRoleExists
	}
	group := pgx.Identifier{input.Role}.Sanitize()
	login := pgx.Identifier{input.SessionUser}.Sanitize()
	const bounded = " NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"
	for _, statement := range []string{
		"CREATE ROLE " + group + " NOLOGIN" + bounded,
		"CREATE ROLE " + login + " LOGIN PASSWORD NULL" + bounded,
		"GRANT " + group + " TO " + login + " WITH ADMIN FALSE, INHERIT FALSE, SET TRUE",
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{input.Database}.Sanitize() + " TO " + login,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return PolicyStatus{}, fmt.Errorf("creating bounded runtime pair: %w", err)
		}
	}
	status, err := installPolicy(ctx, tx, InstallInput{Role: input.Role, Schema: input.Schema, Profile: input.Profile})
	if err != nil {
		return PolicyStatus{}, err
	}
	manifest, err := BuildManifest(input.Profile)
	if err != nil {
		return PolicyStatus{}, err
	}
	snapshot, err := loadPrincipalSnapshot(ctx, tx, environment, input.Schema, input.SessionUser)
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
		name: input.SessionUser, login: true, allowedMembership: input.Role, rejectAdminMembers: true,
	}); err != nil {
		return PolicyStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyStatus{}, fmt.Errorf("committing runtime provisioning; verify independently before retry: %w", err)
	}
	status.SessionUser = input.SessionUser
	return status, nil
}

func validateProvisionInput(input ProvisionInput) error {
	for _, value := range []string{input.Database, input.Schema, input.Role, input.SessionUser} {
		if !provisionIdentifier(value) {
			return errors.New("provisioning requires exact private identifiers of at most 63 bytes")
		}
	}
	if input.Role == input.SessionUser {
		return errors.New("provisioning group and LOGIN must be distinct")
	}
	_, err := BuildManifest(input.Profile)
	return err
}

func provisionIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for index := range len(value) {
		letter := value[index] >= 'a' && value[index] <= 'z' || value[index] >= 'A' && value[index] <= 'Z'
		if !letter && value[index] != '_' && !(index > 0 && value[index] >= '0' && value[index] <= '9') {
			return false
		}
	}
	lower := strings.ToLower(value)
	return lower != "public" && lower != "information_schema" && !strings.HasPrefix(lower, "pg_")
}
