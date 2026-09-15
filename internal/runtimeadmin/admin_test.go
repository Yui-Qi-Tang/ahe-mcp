package runtimeadmin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
)

func testEnvironment() map[string]string {
	return map[string]string{
		"AHE_DATABASE_NAME": "new_pilot", "AHE_DATABASE_SCHEMA": "ahe_next",
		"AHE_DATABASE_ROLE": "ahe_query", "AHE_DATABASE_LOGIN": "query_login",
		"AHE_RUNTIME_PROFILE": "query",
	}
}

func TestConfigurationClosedBindings(t *testing.T) {
	for _, field := range []string{"AHE_DATABASE_NAME", "AHE_DATABASE_SCHEMA", "AHE_DATABASE_ROLE", "AHE_DATABASE_LOGIN", "AHE_RUNTIME_PROFILE"} {
		t.Run("missing_"+field, func(t *testing.T) {
			env := testEnvironment()
			delete(env, field)
			if _, err := loadConfiguration(func(key string) string { return env[key] }); err == nil {
				t.Fatal("missing configuration was accepted")
			}
		})
	}
	for _, profile := range dbrole.ProfileNames() {
		env := testEnvironment()
		env["AHE_RUNTIME_PROFILE"] = profile
		cfg, err := loadConfiguration(func(key string) string { return env[key] })
		if err != nil || string(cfg.profile) != profile {
			t.Fatal("implemented profile was not preserved")
		}
	}
	for _, field := range []string{"AHE_DATABASE_SCHEMA", "AHE_DATABASE_ROLE", "AHE_DATABASE_LOGIN"} {
		for _, bad := range []string{"public", "pg_owner", "information_schema", "bad, pg_catalog", "bad\nname", strings.Repeat("a", 64)} {
			env := testEnvironment()
			env[field] = bad
			if _, err := loadConfiguration(func(key string) string { return env[key] }); err == nil {
				t.Fatal("unsafe or truncated identifier was accepted")
			}
		}
	}
	for _, change := range []map[string]string{
		{"AHE_DATABASE_LOGIN": "ahe_query"}, {"AHE_RUNTIME_PROFILE": "legacy-operator"},
		{"AHE_DATABASE_NAME": " new_pilot"}, {"AHE_DATABASE_NAME": "pilot\x00"},
	} {
		env := testEnvironment()
		for key, value := range change {
			env[key] = value
		}
		if _, err := loadConfiguration(func(key string) string { return env[key] }); err == nil {
			t.Fatal("invalid configuration was accepted")
		}
	}
}

func TestExplicitDatabaseURLDoesNotUseImplicitCredentialFiles(t *testing.T) {
	for _, mode := range []string{"disable", "require", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			value, err := explicitDatabaseURL("postgres://fixture:synthetic@localhost/new_pilot?sslmode="+mode, "new_pilot")
			if err != nil {
				t.Fatal("explicit fixture URL rejected")
			}
			u, err := url.Parse(value)
			if err != nil {
				t.Fatal("normalized URL could not be parsed")
			}
			q := u.Query()
			if q.Get("passfile") != "/dev/null" || q.Has("service") || q.Get("sslmode") != mode ||
				!q.Has("sslcert") || q.Get("sslcert") != "" || !q.Has("sslkey") || q.Get("sslkey") != "" {
				t.Fatal("implicit credential input or TLS policy changed")
			}
			wantRoot := ""
			if mode == "verify-full" {
				wantRoot = "system"
			}
			if q.Get("sslrootcert") != wantRoot {
				t.Fatal("root certificate selection changed")
			}
		})
	}
	if _, err := explicitDatabaseURL("postgres:///new_pilot?host=/private/tmp/fixture&sslmode=disable", "new_pilot"); err == nil {
		t.Fatal("implicit OS username accepted")
	}
	if _, err := explicitDatabaseURL("postgres://fixture@/new_pilot?host=/private/tmp/fixture&port=55488&sslmode=disable", "new_pilot"); err != nil {
		t.Fatal("explicit private-socket URL rejected")
	}
	for _, mode := range []string{"require", "verify-full"} {
		if _, err := explicitDatabaseURL("postgres://fixture@/new_pilot?host=/private/tmp/fixture&sslmode="+mode, "new_pilot"); err == nil {
			t.Fatal("Unix socket must not accept a TLS promise that pgx cannot enforce")
		}
	}
}

func TestExplicitDatabaseURLRejectsAmbiguityWithoutEcho(t *testing.T) {
	const secret = "synthetic-do-not-echo"
	base := "postgres://fixture:" + secret + "@localhost/new_pilot?sslmode=disable"
	for _, value := range []string{
		"", "host=localhost password=" + secret, base + "&service=" + secret,
		base + "&passfile=/private/" + secret, base + "&user=owner", base + "&dbname=other",
		base + "&options=" + secret, base + "&sslkey=/private/" + secret,
		base + "&sslmode=require", base + "&host=other", base + "#fragment",
		strings.Replace(base, "localhost", "host1,host2", 1),
		strings.Replace(base, "new_pilot", "other", 1), base + "\n",
		strings.Replace(base, "disable", "prefer", 1), strings.Replace(base, "localhost", "", 1),
	} {
		_, err := explicitDatabaseURL(value, "new_pilot")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("ambiguous credential accepted or reflected in error")
		}
	}
}

func TestRunHelpAndClosedArguments(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		var out bytes.Buffer
		err := Run(context.Background(), []string{arg}, func(string) string { t.Fatal("help accessed environment"); return "" }, &out)
		if err != nil || !strings.Contains(out.String(), "provision | verify") {
			t.Fatal("help failed")
		}
	}
	for _, args := range [][]string{nil, {"unknown-secret"}, {"provision", "unknown-secret"}, {"--help", "verify"}} {
		var out bytes.Buffer
		err := Run(context.Background(), args, func(string) string { t.Fatal("invalid operation accessed environment"); return "" }, &out)
		if err == nil || strings.Contains(err.Error(), "unknown-secret") || out.Len() != 0 {
			t.Fatal("invalid arguments were accepted or reflected")
		}
	}
}

func TestRunRejectsAmbientPGConfigurationBeforeParsing(t *testing.T) {
	t.Setenv("PGSERVICEFILE", "/private/synthetic-never-read")
	env := testEnvironment()
	env["DATABASE_DSN"] = "postgres://fixture:synthetic@localhost/new_pilot?sslmode=disable"
	var out bytes.Buffer
	err := Run(context.Background(), []string{"provision"}, func(key string) string { return env[key] }, &out)
	if err == nil || !strings.HasPrefix(err.Error(), "PG environment") || out.Len() != 0 {
		t.Fatal("ambient PG configuration was not refused before parsing")
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("synthetic writer detail") }

func TestLostResultDoesNotInviteAutomaticProvisionRetry(t *testing.T) {
	if !strings.Contains(errProvisioning.Error(), "result is uncertain") || !strings.Contains(errProvisioning.Error(), "use verify") {
		t.Fatal("safe provisioning error lost the uncertain commit boundary")
	}
	err := writeResult(failedOutput{}, "provision", configuration{}, dbrole.RuntimeBinding{}, migrations.SchemaStatus{})
	if err == nil || !strings.Contains(err.Error(), "may have completed, use verify") || strings.Contains(err.Error(), "synthetic writer detail") {
		t.Fatal("lost output did not retain the ambiguous commit boundary")
	}
	for _, operation := range []string{"provision", "verify"} {
		if err := writeResult(io.Discard, operation, configuration{}, dbrole.RuntimeBinding{}, migrations.SchemaStatus{}); err != nil {
			t.Fatal(err)
		}
	}
}
