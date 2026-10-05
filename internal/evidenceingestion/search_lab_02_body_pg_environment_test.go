//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func searchLab02PGPool(t *testing.T) (context.Context, *pgxpool.Pool, func()) {
	t.Helper()
	root := os.Getenv("AHE_SEARCH_LAB_02_PG_ROOT")
	if root == "" {
		t.Skip("SEARCH-LAB-02 PostgreSQL environment is not enabled")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !strings.HasPrefix(filepath.Base(root), "ahe-full-lab-replay-") {
		t.Fatal("SEARCH-LAB-02 PostgreSQL root path guard failed")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode() != os.ModeDir|0700 {
		t.Fatal("SEARCH-LAB-02 PostgreSQL root directory guard failed")
	}

	// Empty passwords still trigger pgx's passfile lookup. Explicit null files
	// prevent private credential/service files from contributing configuration.
	// An inherited PGSERVICE fails closed against the empty service file.
	escapedRoot := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(filepath.Join(root, "socket"))
	config, err := pgxpool.ParseConfig("host='" + escapedRoot + "' port=55476 dbname=ahe_lab_replay user=yuki sslmode=disable password='' passfile=/dev/null servicefile=/dev/null connect_timeout=5 target_session_attrs=any require_auth=none channel_binding=disable min_protocol_version=3.0 max_protocol_version=3.0")
	if err != nil {
		t.Fatal("SEARCH-LAB-02 PostgreSQL configuration guard failed")
	}
	config.ConnConfig.Host = filepath.Join(root, "socket")
	config.ConnConfig.Port = 55476
	config.ConnConfig.Database = "ahe_lab_replay"
	config.ConnConfig.User = "yuki"
	config.ConnConfig.Password = ""
	config.ConnConfig.TLSConfig = nil
	config.ConnConfig.Fallbacks = nil
	config.ConnConfig.ValidateConnect = nil
	config.ConnConfig.OAuthTokenProvider = nil
	config.ConnConfig.RuntimeParams = map[string]string{"search_path": "pg_catalog"}
	config.MaxConns = 4

	validate := func(ctx context.Context, connection *pgx.Conn) error {
		var database, user, dataDirectory, listenAddresses, socketDirectory, isolation string
		err := connection.QueryRow(ctx, `
			SELECT current_database(), current_user,
				current_setting('data_directory'),
				current_setting('listen_addresses'),
				current_setting('unix_socket_directories'),
				current_setting('default_transaction_isolation')
		`).Scan(&database, &user, &dataDirectory, &listenAddresses, &socketDirectory, &isolation)
		if err != nil {
			return errors.New("SEARCH-LAB-02 PostgreSQL server inspection failed")
		}
		if database != "ahe_lab_replay" || user != "yuki" ||
			dataDirectory != filepath.Join(root, "data") || listenAddresses != "" ||
			socketDirectory != filepath.Join(root, "socket") || isolation != "read committed" {
			return errors.New("SEARCH-LAB-02 PostgreSQL server identity guard failed")
		}
		return nil
	}
	config.AfterConnect = validate

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatal("SEARCH-LAB-02 PostgreSQL connection failed")
	}

	var pool *pgxpool.Pool
	var schema string
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cleanupCancel()
			if pool != nil {
				pool.Close()
			}
			if schema != "" {
				quotedSchema := pgx.Identifier{schema}.Sanitize()
				if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
					t.Error("SEARCH-LAB-02 PostgreSQL schema cleanup failed")
				}
				var residual int
				if err := admin.QueryRow(cleanupCtx, `SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname = $1`, schema).Scan(&residual); err != nil {
					t.Error("SEARCH-LAB-02 PostgreSQL cleanup inspection failed")
				} else if residual != 0 {
					t.Error("SEARCH-LAB-02 PostgreSQL cleanup left a schema")
				}
			}
			if err := admin.Close(cleanupCtx); err != nil {
				t.Error("SEARCH-LAB-02 PostgreSQL connection cleanup failed")
			}
		})
	}
	t.Cleanup(cleanup)

	if err := validate(ctx, admin); err != nil {
		t.Fatal(err)
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("SEARCH-LAB-02 PostgreSQL schema identity generation failed")
	}
	candidateSchema := "ahe_search_lab02_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{candidateSchema}.Sanitize()); err != nil {
		t.Fatal("SEARCH-LAB-02 PostgreSQL schema creation failed")
	}
	schema = candidateSchema
	config.ConnConfig.RuntimeParams = map[string]string{"search_path": schema}
	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("SEARCH-LAB-02 PostgreSQL pool creation failed")
	}
	if _, err := migrations.ApplyUp(ctx, pool); err != nil {
		t.Fatal("SEARCH-LAB-02 PostgreSQL migrations failed")
	}
	return ctx, pool, cleanup
}
