//go:build integration

package dbrole

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Run on PostgreSQL 16/17 as well as 18+: the full inventory must parse even
// when has_largeobject_privilege does not exist. On 18+ it is also an oracle.
func TestIntegrationLargeObjectEffectiveAuthority(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot connect to explicitly selected ACL fixture")
	}
	defer conn.Close(context.Background())
	var version int
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		setup []string
		want  []string
	}{
		{name: "no_grants"},
		{name: "direct", setup: []string{"GRANT SELECT, UPDATE ON LARGE OBJECT %LO% TO %ROLE%"}, want: []string{"SELECT", "UPDATE"}},
		{name: "public", setup: []string{"GRANT SELECT ON LARGE OBJECT %LO% TO PUBLIC"}, want: []string{"SELECT"}},
		{name: "inherited", setup: []string{"GRANT %PARENT% TO %ROLE% WITH INHERIT TRUE, SET FALSE", "GRANT UPDATE ON LARGE OBJECT %LO% TO %PARENT%"}, want: []string{"UPDATE"}},
		{name: "set_role_only", setup: []string{"GRANT %PARENT% TO %ROLE% WITH INHERIT FALSE, SET TRUE", "GRANT SELECT ON LARGE OBJECT %LO% TO %PARENT%"}},
		{name: "nested_inheritance", setup: []string{"GRANT %PARENT% TO %ROLE% WITH INHERIT TRUE", "GRANT %ANCESTOR% TO %PARENT% WITH INHERIT TRUE", "GRANT SELECT ON LARGE OBJECT %LO% TO %ANCESTOR%"}, want: []string{"SELECT"}},
		{name: "owner_defaults", setup: []string{"ALTER LARGE OBJECT %LO% OWNER TO %ROLE%"}, want: []string{"SELECT", "UPDATE"}},
		{name: "owner_revoked", setup: []string{"ALTER LARGE OBJECT %LO% OWNER TO %ROLE%", "REVOKE ALL ON LARGE OBJECT %LO% FROM %ROLE%"}},
		{name: "grant_option", setup: []string{"GRANT SELECT ON LARGE OBJECT %LO% TO %ROLE% WITH GRANT OPTION"}, want: []string{"SELECT"}},
		{name: "revoked", setup: []string{"GRANT SELECT ON LARGE OBJECT %LO% TO %ROLE%", "REVOKE SELECT ON LARGE OBJECT %LO% FROM %ROLE%"}},
		{name: "overlapping_grants", setup: []string{"GRANT SELECT ON LARGE OBJECT %LO% TO PUBLIC", "GRANT SELECT ON LARGE OBJECT %LO% TO %ROLE%"}, want: []string{"SELECT"}},
		{name: "superuser_empty_acl", setup: []string{"REVOKE ALL ON LARGE OBJECT %LO% FROM CURRENT_USER", "ALTER ROLE %ROLE% SUPERUSER"}, want: []string{"SELECT", "UPDATE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			suffix := policyRandomHex(t, 8)
			role, parent, ancestor := "ahe_lo_"+suffix, "ahe_lo_parent_"+suffix, "ahe_lo_ancestor_"+suffix
			for _, name := range []string{role, parent, ancestor} {
				if _, err := tx.Exec(ctx, "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+" NOLOGIN"); err != nil {
					t.Fatal(err)
				}
			}
			var oid uint32
			if err := tx.QueryRow(ctx, `SELECT pg_catalog.lo_create(0)`).Scan(&oid); err != nil {
				t.Fatal(err)
			}
			replace := strings.NewReplacer("%LO%", fmt.Sprint(oid), "%ROLE%", pgx.Identifier{role}.Sanitize(), "%PARENT%", pgx.Identifier{parent}.Sanitize(), "%ANCESTOR%", pgx.Identifier{ancestor}.Sanitize())
			for _, sql := range tc.setup {
				if _, err := tx.Exec(ctx, replace.Replace(sql)); err != nil {
					t.Fatal(err)
				}
			}
			authorities, err := loadUnmanifestedAuthorities(ctx, tx, "public", role, version)
			if err != nil {
				t.Fatal(err)
			}
			prefix := fmt.Sprintf("LARGE OBJECT %d ", oid)
			var got []string
			for _, authority := range authorities {
				if strings.HasPrefix(authority, prefix) {
					got = append(got, strings.TrimPrefix(authority, prefix))
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("privileges = %v, want %v", got, tc.want)
			}
			if version >= 180000 {
				for _, privilege := range []string{"SELECT", "UPDATE"} {
					var native bool
					if err := tx.QueryRow(ctx, `SELECT pg_catalog.has_largeobject_privilege($1::name,$2::oid,$3::text)`, role, oid, privilege).Scan(&native); err != nil {
						t.Fatal(err)
					}
					if native != slices.Contains(got, privilege) {
						t.Fatalf("%s differs from native PostgreSQL check", privilege)
					}
				}
			}
		})
	}
}
