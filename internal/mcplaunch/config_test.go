package mcplaunch

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func validConfigFields() map[string]string {
	return map[string]string{
		"schema_version": "ahe-mcp-launcher/v1", "binary_path": "/trusted/ahe-query-mcp",
		"database_dns_file": "/protected/query.dsn", "database": "ahe_test", "session_user": "ahe_query_login",
		"schema": "ahe_private", "role": "ahe_query_role", "profile": "query", "principal_id": "launcher:test-consumer",
	}
}

func encodeConfig(t *testing.T, fields map[string]string) []byte {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal synthetic config: %v", err)
	}
	return body
}

func TestParseConfigClosedContract(t *testing.T) {
	for _, profile := range []string{"query", "intake", "source-claim-reviewer"} {
		t.Run(profile, func(t *testing.T) {
			fields := validConfigFields()
			fields["profile"] = profile
			if profile != "query" {
				fields["binary_path"] = "/trusted/ahe-ingest-mcp"
			}
			if cfg, err := parseConfig(encodeConfig(t, fields)); err != nil || cfg.Profile != profile {
				t.Fatalf("closed valid profile rejected: %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(map[string]string){
		"unknown field":        func(v map[string]string) { v["decision_by"] = "secret-marker" },
		"missing field":        func(v map[string]string) { delete(v, "role") },
		"future version":       func(v map[string]string) { v["schema_version"] = "ahe-mcp-launcher/v2" },
		"legacy profile":       func(v map[string]string) { v["profile"] = "legacy-reviewer" },
		"profile mismatch":     func(v map[string]string) { v["binary_path"] = "/trusted/ahe-ingest-mcp" },
		"relative binary":      func(v map[string]string) { v["binary_path"] = "ahe-query-mcp" },
		"unclean file":         func(v map[string]string) { v["database_dns_file"] = "/trusted/../secret" },
		"nul path":             func(v map[string]string) { v["database_dns_file"] = "/trusted/secret\x00" },
		"public schema":        func(v map[string]string) { v["schema"] = "public" },
		"temp schema":          func(v map[string]string) { v["schema"] = "pg_temp" },
		"system role":          func(v map[string]string) { v["role"] = "pg_write_all_data" },
		"role is login":        func(v map[string]string) { v["role"] = v["session_user"] },
		"database punctuation": func(v map[string]string) { v["database"] = "ahe_test/other" },
		"identifier length":    func(v map[string]string) { v["role"] = strings.Repeat("r", 64) },
		"identity absent":      func(v map[string]string) { v["principal_id"] = "" },
		"identity control":     func(v map[string]string) { v["principal_id"] = "secret-marker\n" },
	} {
		t.Run(name, func(t *testing.T) {
			fields := validConfigFields()
			mutate(fields)
			_, err := parseConfig(encodeConfig(t, fields))
			if !errors.Is(err, errConfig) || strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("invalid config did not produce the fixed safe error")
			}
		})
	}
	valid := encodeConfig(t, validConfigFields())
	for name, body := range map[string][]byte{
		"empty":             nil,
		"array":             []byte(`[]`),
		"trailing":          append(append([]byte{}, valid...), []byte(` {}`)...),
		"duplicate":         append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"profile":"query"}`)...),
		"escaped duplicate": append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"pro\u0066ile":"query"}`)...),
		"wrong type":        bytes.Replace(valid, []byte(`"query"`), []byte(`null`), 1),
		"invalid utf8":      append([]byte{'{'}, 0xff, '}'),
		"oversize":          bytes.Repeat([]byte{' '}, maxConfigBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig(body); !errors.Is(err, errConfig) {
				t.Fatal("non-closed JSON accepted")
			}
		})
	}
}

func TestDatabaseURLExactTargetAndDefaults(t *testing.T) {
	cfg, err := parseConfig(encodeConfig(t, validConfigFields()))
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"socket":           "postgres://ahe_query_login:synthetic-password@/ahe_test?host=/private/tmp/test-socket&port=55488&sslmode=disable\n",
		"tcp require":      "postgresql://ahe_query_login:synthetic-password@localhost:5432/ahe_test?sslmode=require",
		"ipv6 verify":      "postgres://ahe_query_login:synthetic-password@[::1]:5432/ahe_test?sslmode=verify-full&connect_timeout=30",
		"escaped password": "postgres://ahe_query_login:p%40ss%20word@localhost/ahe_test?sslmode=require",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := databaseURL([]byte(raw), cfg)
			if err != nil {
				t.Fatal("explicit single-target URL rejected")
			}
			u, err := url.Parse(result)
			if err != nil {
				t.Fatal("normalized URL could not be parsed")
			}
			q := u.Query()
			if u.User.Username() != cfg.SessionUser || u.Path != "/"+cfg.Database || q.Get("passfile") != "/dev/null" ||
				!q.Has("sslcert") || q.Get("sslcert") != "" || !q.Has("sslkey") || q.Get("sslkey") != "" || q.Has("service") {
				t.Fatal("normalized URL changed authority or retained file discovery")
			}
			root := ""
			if q.Get("sslmode") == "verify-full" {
				root = "system"
			}
			if q.Get("sslrootcert") != root {
				t.Fatal("TLS mode did not select the fixed root behavior")
			}
		})
	}
	const base = "postgres://ahe_query_login:secret-marker@localhost/ahe_test?sslmode=disable"
	for name, raw := range map[string]string{
		"keyword DSN":         "host=localhost user=ahe_query_login password=secret-marker dbname=ahe_test sslmode=disable",
		"wrong db":            strings.Replace(base, "/ahe_test?", "/wrong?", 1),
		"wrong user":          strings.Replace(base, "ahe_query_login", "other_login", 1),
		"db query override":   base + "&dbname=other",
		"user query override": base + "&user=other_login",
		"host query override": base + "&host=other",
		"duplicate mode":      base + "&sslmode=disable",
		"service":             base + "&service=other",
		"service file":        base + "&servicefile=/dev/null",
		"passfile":            base + "&passfile=/dev/null",
		"ssl file":            base + "&sslcert=/dev/null",
		"options":             base + "&options=-crole%3Dother",
		"search path":         base + "&search_path=public",
		"fallback hosts":      strings.Replace(base, "localhost", "localhost,other", 1),
		"ambiguous mode":      strings.Replace(base, "disable", "prefer", 1),
		"absent mode":         strings.Split(base, "?")[0],
		"absent host":         strings.Replace(base, "@localhost", "@", 1),
		"socket TLS promise":  "postgres://ahe_query_login:secret-marker@/ahe_test?host=/private/tmp/test-socket&sslmode=require",
		"fragment":            base + "#secret-marker",
		"invalid port":        strings.Replace(base, "localhost", "localhost:0", 1),
		"port list":           base + "&port=5432,5433",
		"unbounded timeout":   base + "&connect_timeout=301",
		"unknown key":         base + "&secret-marker=x",
		"nul password":        strings.Replace(base, "secret-marker", "%00", 1),
		"second line":         base + "\nsecret-marker",
		"oversize":            strings.Repeat("x", maxCredentialBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := databaseURL([]byte(raw), cfg)
			if !errors.Is(err, errCredential) || strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("invalid credential did not produce the fixed safe error")
			}
		})
	}
}

func TestChildEnvironmentIsolation(t *testing.T) {
	cfg, err := parseConfig(encodeConfig(t, validConfigFields()))
	if err != nil {
		t.Fatal(err)
	}
	inherited := []string{"HOME=/synthetic/home", "LANG=zh_TW.UTF-8", "PATH=/hostile/bin", "PGHOST=hostile", "PGSERVICE=hostile", "PGPASSFILE=/hostile/file", "DATABASE_DSN=hostile", "AHE_DATABASE_ROLE=hostile", "AHE_RUNTIME_PRINCIPAL_ID=hostile", "AHE_SOURCE=hostile", "DYLD_INSERT_LIBRARIES=hostile", "LD_PRELOAD=hostile", "GOFLAGS=hostile", "GODEBUG=hostile", "SSL_CERT_FILE=hostile", "SECRET=hostile"}
	before := append([]string{}, inherited...)
	result := childEnvironment(inherited, cfg, "synthetic-credential")
	if !reflect.DeepEqual(inherited, before) {
		t.Fatal("launcher mutated the inherited environment")
	}
	got := make(map[string]string)
	for _, entry := range result {
		key, value, _ := strings.Cut(entry, "=")
		if _, duplicate := got[key]; duplicate || strings.Contains(value, "hostile") {
			t.Fatal("unsafe or duplicate inherited value reached the child")
		}
		got[key] = value
	}
	want := map[string]string{
		"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": "/synthetic/home", "LANG": "zh_TW.UTF-8",
		"DATABASE_DSN": "synthetic-credential", "AHE_RUNTIME_PRINCIPAL_ID": cfg.PrincipalID,
		"AHE_RUNTIME_PROFILE": cfg.Profile, "AHE_DATABASE_ROLE": cfg.Role, "AHE_DATABASE_SCHEMA": cfg.Schema,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("child environment differs from the closed expected allowlist")
	}
}

func TestRunArgumentsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}} {
		var output bytes.Buffer
		if err := Run(args, nil, &output); err != nil || !strings.Contains(output.String(), "Usage:") {
			t.Fatal("standalone launcher help failed")
		}
	}
	for _, args := range [][]string{nil, {"--config"}, {"--unknown", "secret-marker"}, {"--help", "secret-marker"}, {"--config", "/synthetic/config", "--extra"}, {"--config=secret-marker"}} {
		var output bytes.Buffer
		err := Run(args, nil, &output)
		if !errors.Is(err, errArguments) || strings.Contains(err.Error()+output.String(), "secret-marker") {
			t.Fatal("unknown arguments were accepted or echoed")
		}
	}
}
