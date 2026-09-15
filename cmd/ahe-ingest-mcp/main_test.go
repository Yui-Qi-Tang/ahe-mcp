package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

func TestRunRequiresLauncherIdentityBeforeDatabase(t *testing.T) {
	t.Setenv("AHE_RUNTIME_PRINCIPAL_ID", "")
	t.Setenv("DATABASE_DSN", "invalid-secret-sentinel")
	if err := run(t.Context(), nil); !errors.Is(err, runtimeauth.ErrUnauthenticated) {
		t.Fatalf("run() error = %v, want unauthenticated", err)
	}
}

func TestRunRequiresQualifiedPublicProfile(t *testing.T) {
	for _, profile := range []string{"", "unknown", "legacy-reviewer", "legacy-operator"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv("AHE_RUNTIME_PRINCIPAL_ID", "detective")
			t.Setenv("AHE_RUNTIME_PROFILE", profile)
			t.Setenv("DATABASE_DSN", "invalid-secret-sentinel")
			err := run(t.Context(), nil)
			if err == nil || !strings.Contains(err.Error(), "profile") || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("unqualified profile must fail before database access: %v", err)
			}
		})
	}
}

func TestRunRejectsInvalidDatabaseWithoutEcho(t *testing.T) {
	for _, profile := range []string{"intake", "source-claim-reviewer"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv("AHE_RUNTIME_PRINCIPAL_ID", "mock:launcher")
			t.Setenv("AHE_RUNTIME_PROFILE", profile)
			t.Setenv("DATABASE_DSN", "invalid-secret-sentinel")
			if err := run(t.Context(), nil); err == nil || err.Error() != "invalid postgres configuration" {
				t.Fatalf("unsafe or missing configuration error: %v", err)
			}
		})
	}
}
