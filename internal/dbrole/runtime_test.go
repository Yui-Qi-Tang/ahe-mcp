package dbrole

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOpenRuntimePoolRejectsInvalidInputBeforeConnecting(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://runtime@example.invalid/ahe")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input RuntimePoolInput
		want  string
	}{
		{name: "role", input: RuntimePoolInput{Schema: "ahe", Profile: ProfileQuery}, want: "role is required"},
		{name: "schema", input: RuntimePoolInput{Role: "ahe_query", Profile: ProfileQuery}, want: "schema is required"},
		{name: "profile", input: RuntimePoolInput{Role: "ahe_query", Schema: "ahe", Profile: "unknown"}, want: "profile must be one of"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, binding, err := OpenRuntimePool(context.Background(), config, test.input)
			if pool != nil || binding != (RuntimeBinding{}) || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenRuntimePool() = (%v, %+v, %v), want nil/zero/%q", pool, binding, err, test.want)
			}
		})
	}
}

func TestOpenRuntimePoolRejectsNilContextAndConfig(t *testing.T) {
	input := RuntimePoolInput{Role: "ahe_query", Schema: "ahe", Profile: ProfileQuery}
	if _, _, err := OpenRuntimePool(nil, &pgxpool.Config{}, input); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, _, err := OpenRuntimePool(context.Background(), nil, input); err == nil {
		t.Fatal("nil config accepted")
	}
}

func TestOpenRuntimePoolRejectsConnectionLifecycleHooks(t *testing.T) {
	input := RuntimePoolInput{Role: "ahe_query", Schema: "ahe", Profile: ProfileQuery}
	tests := []struct {
		name   string
		mutate func(*pgxpool.Config)
		want   string
	}{
		{name: "before connect", mutate: func(c *pgxpool.Config) {
			c.BeforeConnect = func(context.Context, *pgx.ConnConfig) error { return nil }
		}, want: "BeforeConnect"},
		{name: "after connect", mutate: func(c *pgxpool.Config) {
			c.AfterConnect = func(context.Context, *pgx.Conn) error { return nil }
		}, want: "AfterConnect"},
		{name: "before acquire", mutate: func(c *pgxpool.Config) {
			c.BeforeAcquire = func(context.Context, *pgx.Conn) bool { return true }
		}, want: "BeforeAcquire"},
		{name: "prepare connection", mutate: func(c *pgxpool.Config) {
			c.PrepareConn = func(context.Context, *pgx.Conn) (bool, error) { return true, nil }
		}, want: "PrepareConn"},
		{name: "after release", mutate: func(c *pgxpool.Config) {
			c.AfterRelease = func(*pgx.Conn) bool { return true }
		}, want: "AfterRelease"},
		{name: "pgconn validate connect", mutate: func(c *pgxpool.Config) {
			c.ConnConfig.ValidateConnect = func(context.Context, *pgconn.PgConn) error { return nil }
		}, want: "ValidateConnect"},
		{name: "pgconn after connect", mutate: func(c *pgxpool.Config) {
			c.ConnConfig.AfterConnect = func(context.Context, *pgconn.PgConn) error { return nil }
		}, want: "pgconn AfterConnect"},
		{name: "query tracer", mutate: func(c *pgxpool.Config) {
			c.ConnConfig.Tracer = noOpQueryTracer{}
		}, want: "Tracer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := pgxpool.ParseConfig("postgres://runtime@example.invalid/ahe")
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(config)
			pool, _, err := OpenRuntimePool(t.Context(), config, input)
			if pool != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenRuntimePool() = (%v, %v), want hook error %q", pool, err, test.want)
			}
		})
	}
}

type noOpQueryTracer struct{}

func (noOpQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (noOpQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestRuntimeSearchPathIsExactAndQuoted(t *testing.T) {
	if got, want := runtimeSearchPath(`tenant "weird`), `"tenant ""weird", pg_catalog`; got != want {
		t.Fatalf("runtimeSearchPath() = %q, want %q", got, want)
	}
}

func TestValidateRuntimeBindingRequiresExactQueryWitness(t *testing.T) {
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := manifest.Hash()
	if err != nil {
		t.Fatal(err)
	}
	valid := RuntimeBinding{
		SchemaVersion: PolicyVersion,
		Profile:       ProfileQuery,
		Role:          "ahe_query_runtime",
		Database:      "ahe",
		Schema:        "ahe",
		ManifestHash:  hash,
	}
	if err := ValidateRuntimeBinding(valid, ProfileQuery); err != nil {
		t.Fatalf("ValidateRuntimeBinding(valid) error = %v", err)
	}
	publicDatabase := valid
	publicDatabase.Database = "public"
	if err := ValidateRuntimeBinding(publicDatabase, ProfileQuery); err != nil {
		t.Fatalf("ValidateRuntimeBinding(database public) error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*RuntimeBinding)
	}{
		{name: "schema version", mutate: func(binding *RuntimeBinding) { binding.SchemaVersion = "database-role-policy/v0" }},
		{name: "profile", mutate: func(binding *RuntimeBinding) { binding.Profile = "detective" }},
		{name: "role", mutate: func(binding *RuntimeBinding) { binding.Role = "" }},
		{name: "database", mutate: func(binding *RuntimeBinding) { binding.Database = "" }},
		{name: "schema", mutate: func(binding *RuntimeBinding) { binding.Schema = "" }},
		{name: "manifest hash", mutate: func(binding *RuntimeBinding) { binding.ManifestHash = "sha256:forged" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := ValidateRuntimeBinding(candidate, ProfileQuery); err == nil {
				t.Fatal("ValidateRuntimeBinding() error = nil")
			}
		})
	}
}
