//go:build integration

package dbrole

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationProvisionFreshRuntimeControls(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot connect to explicitly selected provisioning fixture")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal("cannot identify provisioning fixture")
	}
	suffix := policyRandomHex(t, 8)
	schema := "ahe_provision_" + suffix
	schemaID := pgx.Identifier{schema}.Sanitize()
	roles := make([]string, 0, 16)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatal("cannot prepare private provisioning schema")
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaID+" CASCADE"); err != nil {
			t.Error("cannot remove owned provisioning schema")
		}
		for _, role := range roles {
			var exists bool
			if err := admin.QueryRow(cleanupCtx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
				t.Error("cannot inspect owned role cleanup")
				continue
			}
			if exists {
				roleID := pgx.Identifier{role}.Sanitize()
				if _, err := admin.Exec(cleanupCtx, "DROP OWNED BY "+roleID); err != nil {
					t.Error("cannot remove owned fixture grants")
				}
				if _, err := admin.Exec(cleanupCtx, "DROP ROLE "+roleID); err != nil {
					t.Error("cannot remove owned fixture role")
				}
			}
		}
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid explicit fixture configuration")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open isolated migration pool")
	}
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("fixture migration: %v", err)
	}
	pool.Close()
	if _, err := admin.Exec(ctx, "SET search_path TO "+schemaID); err != nil {
		t.Fatal("cannot fix operator fixture schema")
	}
	newInput := func(label string, profile Profile) ProvisionInput {
		input := ProvisionInput{Database: database, Schema: schema, Role: "ahe_provision_" + label + "_g_" + suffix,
			SessionUser: "ahe_provision_" + label + "_l_" + suffix, Profile: profile}
		roles = append(roles, input.SessionUser, input.Role)
		return input
	}
	assertAbsent := func(input ProvisionInput) {
		t.Helper()
		var count int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname IN ($1,$2)`, input.Role, input.SessionUser).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed provisioning left role authority")
		}
	}
	for _, profile := range []Profile{ProfileQuery, ProfileIntake, ProfileSourceClaimReviewer} {
		t.Run(string(profile), func(t *testing.T) {
			label := "query"
			if profile == ProfileIntake {
				label = "intake"
			}
			if profile == ProfileSourceClaimReviewer {
				label = "reviewer"
			}
			input := newInput(label, profile)
			status, err := ProvisionRuntime(ctx, admin, input)
			if err != nil {
				t.Fatalf("fresh policy provisioning: %v", err)
			}
			if status.Database != database || status.Role != input.Role || status.SessionUser != input.SessionUser || status.Profile != profile || status.SchemaVersion != PolicyVersion {
				t.Fatal("provisioned policy witness differs from selected identity")
			}
			var passwordAbsent bool
			if err := admin.QueryRow(ctx, `SELECT rolpassword IS NULL FROM pg_authid WHERE rolname=$1`, input.SessionUser).Scan(&passwordAbsent); err != nil || !passwordAbsent {
				t.Fatal("provisioning created a password")
			}
			loginConfig := config.Copy()
			loginConfig.ConnConfig.User = input.SessionUser
			loginConfig.ConnConfig.Password = ""
			runtimePool, binding, err := OpenRuntimePool(ctx, loginConfig, RuntimePoolInput{Role: input.Role, Schema: schema, Profile: profile})
			if err != nil {
				t.Fatalf("fresh LOGIN runtime verification: %v", err)
			}
			runtimePool.Close()
			if binding.ManifestHash != status.ManifestHash {
				t.Fatal("runtime manifest differs from provisioned policy")
			}
			if _, err := ProvisionRuntime(ctx, admin, input); !errors.Is(err, ErrRoleExists) {
				t.Fatal("repeat provision did not refuse role adoption")
			}
		})
	}
	t.Run("wrong_database", func(t *testing.T) {
		input := newInput("wrongdb", ProfileQuery)
		input.Database = "not_selected"
		if _, err := ProvisionRuntime(ctx, admin, input); !errors.Is(err, ErrPolicyViolation) {
			t.Fatal("wrong database accepted")
		}
		assertAbsent(input)
	})
	for _, groupExists := range []bool{true, false} {
		label := "existing_login"
		if groupExists {
			label = "existing_group"
		}
		t.Run(label, func(t *testing.T) {
			input := newInput(label, ProfileQuery)
			existing, absent := input.SessionUser, input.Role
			if groupExists {
				existing, absent = input.Role, input.SessionUser
			}
			if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{existing}.Sanitize()+" NOLOGIN INHERIT"); err != nil {
				t.Fatal("cannot prepare owned collision role")
			}
			if _, err := ProvisionRuntime(ctx, admin, input); !errors.Is(err, ErrRoleExists) {
				t.Fatal("existing role was adopted")
			}
			var preserved, companionAbsent bool
			if err := admin.QueryRow(ctx, `SELECT
				EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1 AND rolinherit AND NOT rolcanlogin),
				NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$2)
			`, existing, absent).Scan(&preserved, &companionAbsent); err != nil || !preserved || !companionAbsent {
				t.Fatal("existing role was changed or companion role was created")
			}
		})
	}
	t.Run("non_superuser_creator_is_refused", func(t *testing.T) {
		input := newInput("nonsuper", ProfileQuery)
		operator := "ahe_provision_operator_" + suffix
		roles = append(roles, operator)
		operatorID := pgx.Identifier{operator}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE ROLE "+operatorID+" LOGIN PASSWORD NULL CREATEROLE NOINHERIT"); err != nil {
			t.Fatal("cannot prepare owned non-superuser operator")
		}
		if _, err := admin.Exec(ctx, "GRANT USAGE ON SCHEMA "+schemaID+" TO "+operatorID); err != nil {
			t.Fatal("cannot prepare operator schema visibility")
		}
		operatorConfig := config.ConnConfig.Copy()
		operatorConfig.User, operatorConfig.Password = operator, ""
		operatorConn, err := pgx.ConnectConfig(ctx, operatorConfig)
		if err != nil {
			t.Fatal("cannot connect owned non-superuser operator")
		}
		defer operatorConn.Close(context.Background())
		if _, err := ProvisionRuntime(ctx, operatorConn, input); !errors.Is(err, ErrPolicyViolation) {
			t.Fatal("non-superuser role creator accepted")
		}
		assertAbsent(input)
	})
	t.Run("public_prerequisite_failure_rolls_back_pair", func(t *testing.T) {
		input := newInput("rollback", ProfileQuery)
		databaseID := pgx.Identifier{database}.Sanitize()
		// This is the explicitly dedicated disposable DB, not an existing
		// operator environment. Restore its prechecked closed TEMP authority.
		var temporary bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database d, LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) acl WHERE d.datname=current_database() AND acl.grantee=0 AND acl.privilege_type='TEMPORARY')`).Scan(&temporary); err != nil || temporary {
			t.Fatal("fixture must begin with closed PUBLIC TEMP authority")
		}
		if _, err := admin.Exec(ctx, "GRANT TEMPORARY ON DATABASE "+databaseID+" TO PUBLIC"); err != nil {
			t.Fatal("cannot prepare PUBLIC prerequisite negative")
		}
		if _, err := admin.Exec(ctx, "GRANT SELECT ON TABLE "+schemaID+".source_blobs TO PUBLIC"); err != nil {
			t.Fatal("cannot prepare reversible schema ACL")
		}
		defer func() {
			if _, err := admin.Exec(ctx, "REVOKE TEMPORARY ON DATABASE "+databaseID+" FROM PUBLIC"); err != nil {
				t.Error("cannot restore fixture PUBLIC TEMP boundary")
			}
			if _, err := admin.Exec(ctx, "REVOKE SELECT ON TABLE "+schemaID+".source_blobs FROM PUBLIC"); err != nil {
				t.Error("cannot restore fixture table ACL")
			}
		}()
		if _, err := ProvisionRuntime(ctx, admin, input); !errors.Is(err, ErrPolicyViolation) {
			t.Fatal("missing PUBLIC prerequisite did not fail closed")
		}
		assertAbsent(input)
		var publicSelect bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace,
			LATERAL aclexplode(c.relacl) acl
			WHERE n.nspname=$1 AND c.relname='source_blobs' AND acl.grantee=0 AND acl.privilege_type='SELECT'
		)`, schema).Scan(&publicSelect); err != nil || !publicSelect {
			t.Fatal("failed provisioning did not roll back its schema ACL removal")
		}
		if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database d, LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) acl WHERE d.datname=current_database() AND acl.grantee=0 AND acl.privilege_type='TEMPORARY')`).Scan(&temporary); err != nil || !temporary {
			t.Fatal("provisioner changed an external database-wide prerequisite")
		}
	})
}
