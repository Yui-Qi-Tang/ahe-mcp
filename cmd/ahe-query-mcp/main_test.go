package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

func TestRunRequiresLauncherIdentityBeforeDatabase(t *testing.T) {
	t.Setenv("AHE_RUNTIME_PRINCIPAL_ID", "")
	t.Setenv("DATABASE_DNS", "invalid-secret-sentinel")
	if err := run(t.Context(), nil); !errors.Is(err, runtimeauth.ErrUnauthenticated) {
		t.Fatalf("run() error = %v, want unauthenticated", err)
	}
}

func TestRunRejectsIncompleteQueryConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, database, role, schema, want string
	}{
		{name: "missing database", want: "DATABASE_DNS is required"},
		{name: "invalid database", database: "invalid-secret-sentinel", want: "invalid postgres configuration"},
		{name: "missing role", database: "postgresql://fixture@localhost/fixture", schema: "fixture", want: "role"},
		{name: "missing schema", database: "postgresql://fixture@localhost/fixture", role: "fixture", want: "schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AHE_RUNTIME_PRINCIPAL_ID", "query-consumer")
			t.Setenv("DATABASE_DNS", tc.database)
			t.Setenv("AHE_DATABASE_ROLE", tc.role)
			t.Setenv("AHE_DATABASE_SCHEMA", tc.schema)
			err := run(t.Context(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("run() must reject configuration without exposing connection input: %v", err)
			}
		})
	}
}
