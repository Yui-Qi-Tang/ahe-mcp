// Package runtimeadmin provides bounded one-shot runtime role administration.
package runtimeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const resultVersion = "ahe-runtime-admin-result/v1"

var errProvisioning = errors.New("runtime role provisioning failed or its result is uncertain; do not replace existing roles; use verify with the bounded LOGIN before any retry")

const usage = `Usage: ahe-runtime-admin provision | verify

Environment:
  DATABASE_DNS          Explicit PostgreSQL URL; never a command argument
  AHE_DATABASE_NAME     Exact separately approved dedicated database
  AHE_DATABASE_SCHEMA   Existing private schema with current native migrations
  AHE_DATABASE_ROLE     Selected NOLOGIN group role
  AHE_DATABASE_LOGIN    Distinct LOGIN session identity
  AHE_RUNTIME_PROFILE   query, intake, or source-claim-reviewer

provision creates a fresh role pair and installs the closed policy atomically.
Use separate trusted operator credentials. Existing roles are never adopted.
verify uses the bounded LOGIN credentials and performs read-only verification.
Neither command creates a database/schema/password, migrates data, changes
database-wide PUBLIC grants, or configures PostgreSQL authentication. PG*
environment configuration and implicit credential/service files are refused.`

type configuration struct {
	database string
	schema   string
	role     string
	login    string
	profile  dbrole.Profile
}

type result struct {
	SchemaVersion        string                `json:"schema_version"`
	Operation            string                `json:"operation"`
	CreatedRolePair      bool                  `json:"created_role_pair"`
	RuntimeLoginVerified bool                  `json:"runtime_login_verified"`
	SessionUser          string                `json:"session_user"`
	Binding              dbrole.RuntimeBinding `json:"binding"`
	AppliedMigrations    int                   `json:"applied_migrations"`
	LatestMigration      string                `json:"latest_migration"`
}

// Run executes one explicit operation and emits only credential-free status.
// The caller owns timeout, output, and the distinct operator/runtime credential.
func Run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if ctx == nil || getenv == nil || out == nil {
		return errors.New("runtime administration requires context, environment and output")
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, usage+"\n")
		return err
	}
	if len(args) != 1 || (args[0] != "provision" && args[0] != "verify") {
		return errors.New("expected exactly one operation: provision or verify; use --help")
	}
	cfg, err := loadConfiguration(getenv)
	if err != nil {
		return err
	}
	// pgx has no environment hook. Reject ambient libpq settings before its
	// parser can open a service/passfile or override the explicit URL.
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PG") {
			return errors.New("PG environment settings must be absent for runtime administration")
		}
	}
	databaseURL, err := explicitDatabaseURL(getenv("DATABASE_DNS"), cfg.database)
	if err != nil {
		return err
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid explicit postgres configuration")
	}
	poolConfig.ConnConfig.RuntimeParams = map[string]string{"search_path": pgx.Identifier{cfg.schema}.Sanitize()}
	if args[0] == "verify" {
		if poolConfig.ConnConfig.User != cfg.login {
			return errors.New("verification credential must name the selected LOGIN")
		}
		return verify(ctx, poolConfig, cfg, out)
	}
	if poolConfig.ConnConfig.User == cfg.login || poolConfig.ConnConfig.User == cfg.role {
		return errors.New("provisioning requires a separate operator identity")
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("opening operator connection failed")
	}
	defer pool.Close()
	status, err := migrations.VerifyCurrentInSchema(ctx, pool, cfg.schema)
	if err != nil {
		return errors.New("native schema verification failed; no role pair was provisioned")
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return errors.New("acquiring operator connection failed")
	}
	defer connection.Release()
	policy, err := dbrole.ProvisionRuntime(ctx, connection.Conn(), dbrole.ProvisionInput{
		Database: cfg.database, Schema: cfg.schema, Role: cfg.role, SessionUser: cfg.login, Profile: cfg.profile,
	})
	if err != nil {
		// Driver errors can include connection details or SQL supplied by a
		// hostile server. The operator can inspect that server separately.
		return errProvisioning
	}
	return writeResult(out, "provision", cfg, dbrole.RuntimeBinding{
		SchemaVersion: policy.SchemaVersion, Profile: policy.Profile, Role: policy.Role,
		Database: policy.Database, Schema: policy.Schema, ManifestHash: policy.ManifestHash,
	}, status)
}

func verify(ctx context.Context, config *pgxpool.Config, cfg configuration, out io.Writer) error {
	pool, binding, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{
		Role: cfg.role, Schema: cfg.schema, Profile: cfg.profile,
	})
	if err != nil {
		return errors.New("runtime login or database role policy verification failed")
	}
	defer pool.Close()
	status, err := migrations.VerifyCurrentInSchema(ctx, pool, cfg.schema)
	if err != nil {
		return errors.New("native runtime schema verification failed")
	}
	return writeResult(out, "verify", cfg, binding, status)
}

func writeResult(out io.Writer, operation string, cfg configuration, binding dbrole.RuntimeBinding, status migrations.SchemaStatus) error {
	if err := json.NewEncoder(out).Encode(result{
		SchemaVersion: resultVersion, Operation: operation, CreatedRolePair: operation == "provision",
		RuntimeLoginVerified: operation == "verify",
		SessionUser:          cfg.login, Binding: binding,
		AppliedMigrations: status.AppliedMigrations, LatestMigration: status.LatestMigration,
	}); err != nil {
		// A successful commit with lost stdout must not prompt automatic retry
		// or role replacement. The independent verify operation resolves it.
		return errors.New("writing runtime administration result failed; operation may have completed, use verify")
	}
	return nil
}

func loadConfiguration(getenv func(string) string) (configuration, error) {
	cfg := configuration{
		database: getenv("AHE_DATABASE_NAME"), schema: getenv("AHE_DATABASE_SCHEMA"),
		role: getenv("AHE_DATABASE_ROLE"), login: getenv("AHE_DATABASE_LOGIN"),
	}
	if cfg.database == "" || len(cfg.database) > 63 || !utf8.ValidString(cfg.database) ||
		strings.TrimSpace(cfg.database) != cfg.database || strings.ContainsFunc(cfg.database, unicode.IsControl) {
		return configuration{}, errors.New("AHE_DATABASE_NAME must be one exact database name")
	}
	// Use the same deliberately narrow identifier grammar as the migrator;
	// do not allow truncation, PUBLIC, system names or search-path expressions.
	for _, value := range []string{cfg.schema, cfg.role, cfg.login} {
		if err := migrations.ValidateTargetSchema(value); err != nil {
			return configuration{}, errors.New("schema, role and login must be exact private identifiers")
		}
	}
	if err := migrations.ValidateTargetSchema(cfg.database); err != nil {
		return configuration{}, errors.New("database must use the launcher's exact private identifier grammar")
	}
	if cfg.role == cfg.login {
		return configuration{}, errors.New("runtime group and LOGIN must be distinct")
	}
	profile, err := dbrole.ParseProfile(getenv("AHE_RUNTIME_PROFILE"))
	if err != nil {
		return configuration{}, errors.New("runtime profile must be query, intake or source-claim-reviewer")
	}
	cfg.profile = profile
	return cfg, nil
}

func explicitDatabaseURL(value, database string) (string, error) {
	reject := errors.New("DATABASE_DNS must be an explicit single-target PostgreSQL URL without external credential or service files")
	if len(value) == 0 || len(value) > 16*1024 || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", reject
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" ||
		u.Fragment != "" || u.User == nil || u.User.Username() == "" || u.Path != "/"+database {
		return "", reject
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", reject
	}
	for key, values := range query {
		if len(values) != 1 || (key != "host" && key != "port" && key != "sslmode" && key != "connect_timeout") {
			return "", reject
		}
	}
	host := u.Hostname()
	if query.Has("host") {
		if u.Host != "" {
			return "", reject
		}
		host = query.Get("host")
	}
	if host == "" || strings.ContainsAny(host, ",\r\n\x00") || (u.Port() != "" && query.Has("port")) {
		return "", reject
	}
	switch query.Get("sslmode") {
	case "disable", "require", "verify-full":
	default:
		return "", reject
	}
	if strings.HasPrefix(host, "/") && query.Get("sslmode") != "disable" {
		// pgx never uses TLS on a Unix socket. Refuse a contradictory TLS
		// request instead of reporting an encrypted connection that is not.
		return "", reject
	}
	query.Set("passfile", "/dev/null")
	query.Set("sslcert", "")
	query.Set("sslkey", "")
	query.Set("sslrootcert", "")
	if query.Get("sslmode") == "verify-full" {
		query.Set("sslrootcert", "system")
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}
