package dbrole

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// UpgradeRuntime updates the policy of an existing exact group/LOGIN pair.
// It never creates, replaces or changes role identities, credentials or membership.
// The operator must stop serving processes and migrate the schema first; a separate
// LOGIN verification still checks authentication after the transaction commits.
func UpgradeRuntime(ctx context.Context, conn *pgx.Conn, input ProvisionInput) (PolicyStatus, error) {
	if ctx == nil || conn == nil {
		return PolicyStatus{}, errors.New("context and postgres connection are required")
	}
	if err := validateProvisionInput(input); err != nil {
		return PolicyStatus{}, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PolicyStatus{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	env, err := loadConnectionEnvironment(ctx, tx)
	if err != nil {
		return PolicyStatus{}, err
	}
	if env.database != input.Database || env.currentSchema != input.Schema || env.currentRole != env.sessionUser || env.serverVersionNum < 160000 {
		return PolicyStatus{}, policyViolation("upgrade requires the exact database/schema and unchanged PostgreSQL 16+ operator session")
	}
	operator, err := loadRoleAttributes(ctx, tx, env.currentRole)
	if err != nil {
		return PolicyStatus{}, err
	}
	if !operator.superuser {
		return PolicyStatus{}, policyViolation("role upgrade requires a separately authorized superuser")
	}
	if err := validateCriticalRuntimeSettings(env); err != nil {
		return PolicyStatus{}, err
	}
	manifest, err := BuildManifest(input.Profile)
	if err != nil {
		return PolicyStatus{}, err
	}
	checkLogin := func() error {
		snapshot, err := loadPrincipalSnapshot(ctx, tx, env, input.Schema, input.SessionUser)
		if err != nil {
			return err
		}
		return validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
			name: input.SessionUser, login: true, allowedMembership: input.Role, rejectAdminMembers: true,
		})
	}
	// Reject a wrong pair before touching any ACL. Post-checks and installation
	// share this transaction, so a failed check cannot leave a partial upgrade.
	if err := checkLogin(); err != nil {
		return PolicyStatus{}, err
	}
	status, err := installPolicy(ctx, tx, InstallInput{Role: input.Role, Schema: input.Schema, Profile: input.Profile})
	if err != nil {
		return PolicyStatus{}, err
	}
	if err := checkLogin(); err != nil {
		return PolicyStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyStatus{}, fmt.Errorf("committing role upgrade; verify before retry: %w", err)
	}
	status.SessionUser = input.SessionUser
	return status, nil
}
