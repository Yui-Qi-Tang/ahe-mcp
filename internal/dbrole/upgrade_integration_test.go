//go:build integration

package dbrole

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type roleUpgradeBaseline struct {
	SourceCommit       string               `json:"source_commit"`
	MigrationChecksums map[string]string    `json:"migration_checksums"`
	Manifests          map[Profile]Manifest `json:"manifests"`
}

type roleUpgradeActor struct {
	Label   string
	Login   string
	Group   string
	Profile Profile
	Config  *pgxpool.Config
}

type roleUpgradeSource struct {
	Content string
	Receipt evidenceingestion.ExternalSourceIntakeResult
}

func TestIntegrationRoleUpgradePreservesIdentityAndEvidence(t *testing.T) {
	baselineBytes, err := os.ReadFile("testdata/upgrade49_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if roleUpgradeHash(baselineBytes) != "92f41ffb96a4a442ed8db8ca76b99c2567842d2d144370b9eeeefbce602df5c9" {
		t.Fatal("frozen schema-49 role baseline changed; review the protocol before replacing it")
	}
	var baseline roleUpgradeBaseline
	if err := json.Unmarshal(baselineBytes, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.SourceCommit != "22039386078986ed6a5816baf3bcbe4ef092017a" || len(baseline.MigrationChecksums) != 49 || len(baseline.Manifests) != 6 {
		t.Fatal("unexpected historical baseline")
	}
	ctx, owner, pool, actors := roleUpgradeFixture(t, baseline)
	var sources []roleUpgradeSource
	for i, content := range []string{"升級證据甲：重試上限是三次。\n保留來源甲。\n", "升級證據乙：重試上限是五次。\n保留來源乙。\n"} {
		id := fmt.Sprintf("role-baseline-%d", i+1)
		source, err := evidenceingestion.CaptureExternalSource(ctx, pool, evidenceingestion.ExternalSourceEnvelopeV1{
			SchemaVersion: evidenceingestion.ExternalSourceEnvelopeSchemaV1, RequestID: id,
			SourceSystem: "jira", SourceNamespace: "role-upgrade-fixture", ObjectType: "issue", ObjectID: id, Revision: "1",
			SourceLocation: "https://example.invalid/" + id, Title: id,
			ContentFormat: evidenceingestion.ExternalSourceContentFormatPlainText, ContentFidelity: evidenceingestion.ExternalSourceContentFidelityVerbatim,
			Content: content, Coverage: evidenceingestion.ExternalSourceCoverageFullDocument, Limitations: []string{},
			CollectorID: "fixed-fixture", ConnectorID: "fixed-fixture", ObservedAt: "2026-10-04T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("seed source on schema 49: %v", err)
		}
		sources = append(sources, roleUpgradeSource{content, source})
	}
	if sources[0].Receipt.SourceSnapshotID == sources[1].Receipt.SourceSnapshotID || sources[0].Receipt.RawContentHash == sources[1].Receipt.RawContentHash {
		t.Fatal("fixture sources are not distinguishable")
	}
	roles := roleUpgradeIdentities(t, ctx, pool, actors)
	rows := roleUpgradeRows(t, ctx, pool, baseline)
	readBaseline := map[string]json.RawMessage{}
	stages := map[string]any{}
	for _, stage := range []string{"schema49", "schema53_old_policy", "schema53_current_policy"} {
		if !t.Run(stage, func(t *testing.T) {
			if stage == "schema53_old_policy" {
				changed, err := migrations.ApplyUp(ctx, pool)
				if err != nil || !changed {
					t.Fatalf("upgrade through ApplyUp: changed=%t err=%v", changed, err)
				}
				status, err := migrations.VerifyCurrent(ctx, pool)
				if err != nil || status.AppliedMigrations != 54 {
					t.Fatalf("upgraded schema: %+v %v", status, err)
				}
			}
			if stage == "schema53_current_policy" {
				for _, actor := range actors {
					if actor.Group == "" || actor.Label == "query_b" {
						continue
					}
					if _, err := InstallPolicy(ctx, owner, InstallInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile}); err != nil {
						t.Fatalf("refresh existing role %s: %v", actor.Label, err)
					}
				}
			}
			if got := roleUpgradeIdentities(t, ctx, pool, actors); !reflect.DeepEqual(got, roles) {
				t.Fatalf("role OIDs, attributes or memberships changed: before=%s after=%s", roles, got)
			}
			roleUpgradeAssertRows(t, ctx, pool, baseline, rows)
			stageResult := map[string]any{}
			for _, actor := range actors {
				if !t.Run(actor.Label, func(t *testing.T) {
					if stage == "schema53_old_policy" && actor.Group != "" {
						roleUpgradeRejectOldPolicy(t, ctx, actor, baseline.Manifests[actor.Profile])
					}
					var runtime *pgxpool.Pool
					var err error
					if stage == "schema53_current_policy" && actor.Group != "" {
						runtime, _, err = OpenRuntimePool(ctx, actor.Config, RuntimePoolInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile})
					} else {
						raw := actor.Config.Copy()
						raw.ConnConfig.RuntimeParams["search_path"] = "evidence"
						raw.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
							if actor.Group == "" {
								return nil
							}
							_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{actor.Group}.Sanitize())
							return err
						}
						runtime, err = pgxpool.NewWithConfig(ctx, raw)
					}
					if err != nil {
						t.Fatalf("open %s at %s: %v", actor.Label, stage, err)
					}
					defer runtime.Close()
					identity := roleUpgradeSession(t, ctx, runtime, actor)
					if actor.Group == "" {
						var schemaUse, sourceRead bool
						if err := runtime.QueryRow(ctx, "SELECT has_schema_privilege(current_user,'evidence','USAGE'),has_table_privilege(current_user,'evidence.source_snapshots','SELECT')").Scan(&schemaUse, &sourceRead); err != nil || !schemaUse || sourceRead {
							t.Fatalf("no-read control privileges: schema=%t select=%t err=%v", schemaUse, sourceRead, err)
						}
					}
					visible := map[string]string{}
					for i, source := range sources {
						got, err := evidenceingestion.LoadBoundedSourceView(ctx, runtime, source.Receipt.SourceSnapshotID, source.Receipt.ExtractionViewID)
						if actor.Group == "" {
							roleUpgradePermissionDenied(t, err)
							visible[fmt.Sprint(i+1)] = "denied:42501"
							continue
						}
						if err != nil {
							t.Fatal(err)
						}
						roleUpgradeCheckView(t, source, got)
						data, err := json.Marshal(got)
						if err != nil {
							t.Fatal(err)
						}
						key := source.Receipt.SourceSnapshotID
						if stage == "schema49" && actor.Label == "query_a" {
							readBaseline[key] = data
						}
						if !reflect.DeepEqual([]byte(readBaseline[key]), data) {
							t.Fatalf("visible evidence differs: %s %s", stage, actor.Label)
						}
						visible[fmt.Sprint(i+1)] = roleUpgradeHash(data)
					}
					if actor.Group != "" {
						roleUpgradeWriteMatrix(t, ctx, runtime, actor)
					}
					if got := roleUpgradeSession(t, ctx, runtime, actor); got != identity {
						t.Fatal("physical session changed during identity comparison")
					}
					stageResult[actor.Label] = map[string]any{"session": identity, "visible_evidence": visible}
				}) {
					return
				}
			}
			roleUpgradeAssertRows(t, ctx, pool, baseline, rows)
			stages[stage] = stageResult
		}) {
			return
		}
	}
	report := map[string]any{"historical_commit": baseline.SourceCommit, "role_identities": json.RawMessage(roles),
		"old_table_hashes": rows, "stages": stages, "roles_recreated": false, "model_calls": 0}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("role upgrade: three checkpoints, eight logins, two distinct sources; identity, visibility and write-authority checks passed")
	if path := os.Getenv("AHE_ROLE_UPGRADE_REPORT"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("AHE_ROLE_UPGRADE_REPORT must be absolute")
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(append(data, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("write report: %v %v", writeErr, closeErr)
		}
	}
}

func roleUpgradeHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func roleUpgradeFixture(t *testing.T, baseline roleUpgradeBaseline) (context.Context, *pgx.Conn, *pgxpool.Pool, []roleUpgradeActor) {
	t.Helper()
	dsn := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN")
	if dsn == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DSN is required for isolated role upgrade verification")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration; connection details omitted")
	}
	host := config.ConnConfig.Host
	if !filepath.IsAbs(host) && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		t.Fatal("role upgrade requires a local isolated PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatal("connect fixture administrator failed; connection details omitted")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var superuser bool
	if err := admin.QueryRow(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&superuser); err != nil {
		t.Fatal(err)
	}
	if !superuser {
		t.Fatal("role upgrade requires a local superuser to create a disposable database")
	}
	nonce := policyRandomHex(t, 8)
	database := "ahe_role_upgrade_" + nonce
	databaseID := pgx.Identifier{database}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+databaseID+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	var created []string
	t.Cleanup(func() {
		cctx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := admin.Exec(cctx, "DROP DATABASE "+databaseID); err != nil {
			t.Errorf("drop exact fixture database: %v", err)
			return
		}
		for i := len(created) - 1; i >= 0; i-- {
			if _, err := admin.Exec(cctx, "DROP ROLE "+pgx.Identifier{created[i]}.Sanitize()); err != nil {
				t.Errorf("drop exact fixture role: %v", err)
			}
		}
		var databases, roles int
		err := admin.QueryRow(cctx, "SELECT (SELECT count(*) FROM pg_database WHERE datname=$1),(SELECT count(*) FROM pg_roles WHERE rolname=ANY($2::text[]))", database, created).Scan(&databases, &roles)
		if err != nil || databases != 0 || roles != 0 {
			t.Errorf("cleanup residuals db=%d roles=%d err=%v", databases, roles, err)
		} else {
			t.Log("role-upgrade cleanup: database_residual=0 role_residual=0")
		}
	})
	if _, err := admin.Exec(ctx, "REVOKE CREATE,TEMPORARY ON DATABASE "+databaseID+" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = database
	config.ConnConfig.RuntimeParams = map[string]string{"search_path": "evidence", "statement_timeout": "30000", "lock_timeout": "10000"}
	config.MaxConns = 1
	owner, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatal("open disposable owner connection failed")
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	for _, statement := range []string{"CREATE SCHEMA evidence", "REVOKE ALL ON SCHEMA public FROM PUBLIC", "REVOKE UPDATE ON pg_catalog.pg_settings FROM PUBLIC"} {
		if _, err := owner.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal("open disposable owner pool failed")
	}
	t.Cleanup(pool.Close)
	roleUpgradeApplyBaseline(t, ctx, pool, baseline)
	relations, err := loadRelationInventory(ctx, owner, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	groups := map[Profile]string{}
	profiles := []Profile{ProfileQuery, ProfileSourceClaimReviewer, ProfileIntake, ProfileRelationReviewer, ProfileEndpointReviewer, ProfileRepositoryIntake}
	if _, ok := baseline.Manifests[ProfileCoreRecords]; ok {
		profiles = append(profiles, ProfileCoreRecords)
	}
	for i, profile := range profiles {
		group := fmt.Sprintf("ahe_ru_%s_g%d", nonce, i)
		if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{group}.Sanitize()+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"); err != nil {
			t.Fatal(err)
		}
		created = append(created, group)
		groups[profile] = group
		statements, err := policyStatements(database, "evidence", group, baseline.Manifests[profile], relations)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range statements {
			if _, err := owner.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	var actors []roleUpgradeActor
	for _, entry := range []struct {
		label   string
		profile Profile
	}{{"query_a", ProfileQuery}, {"query_b", ProfileQuery}, {"reviewer", ProfileSourceClaimReviewer}, {"collector", ProfileIntake}, {"relations", ProfileRelationReviewer}, {"endpoints", ProfileEndpointReviewer}, {"repository", ProfileRepositoryIntake}, {"no_scope", ""}, {"core", ProfileCoreRecords}} {
		if entry.profile == ProfileCoreRecords && groups[ProfileCoreRecords] == "" {
			continue
		}
		login := "ahe_ru_" + nonce + "_" + entry.label
		password := policyRandomHex(t, 24)
		if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{login}.Sanitize()+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '"+password+"'"); err != nil {
			t.Fatal("create test login failed; credential omitted")
		}
		created = append(created, login)
		group := groups[entry.profile]
		if group != "" {
			if _, err := admin.Exec(ctx, "GRANT "+pgx.Identifier{group}.Sanitize()+" TO "+pgx.Identifier{login}.Sanitize()+" WITH ADMIN FALSE, INHERIT FALSE, SET TRUE"); err != nil {
				t.Fatal(err)
			}
		} else {
			// Keep name resolution available; this control has no table SELECT grants.
			if _, err := owner.Exec(ctx, "GRANT USAGE ON SCHEMA evidence TO "+pgx.Identifier{login}.Sanitize()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+databaseID+" TO "+pgx.Identifier{login}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		runtime := config.Copy()
		runtime.ConnConfig.User, runtime.ConnConfig.Password = login, password
		delete(runtime.ConnConfig.RuntimeParams, "search_path")
		actors = append(actors, roleUpgradeActor{entry.label, login, group, entry.profile, runtime})
	}
	// Verify the frozen ACL itself, not just whether two callers agree.
	environment, err := loadConnectionEnvironment(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPublicPolicy(ctx, owner, database, "evidence"); err != nil {
		t.Fatal(err)
	}
	for profile, group := range groups {
		snapshot, err := loadPrincipalSnapshot(ctx, owner, environment, "evidence", group)
		if err != nil {
			t.Fatal(err)
		}
		if err := validatePrincipalSnapshot(snapshot, baseline.Manifests[profile], principalExpectation{name: group, login: false, exactPrivileges: true, requireSchemaUse: true, rejectAdminMembers: true}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, owner, pool, actors
}

func roleUpgradeApplyBaseline(t *testing.T, ctx context.Context, pool *pgxpool.Pool, baseline roleUpgradeBaseline) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "CREATE TABLE schema_migrations (migration_name TEXT PRIMARY KEY,migration_checksum TEXT NOT NULL,applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range baseline.MigrationChecksums {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if "sha256:"+roleUpgradeHash(sql) != baseline.MigrationChecksums[name] {
			t.Fatalf("historical migration drift: %s", name)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("old migration %s: %v", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(migration_name,migration_checksum) VALUES($1,$2)", name, baseline.MigrationChecksums[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	var latest string
	if err := pool.QueryRow(ctx, "SELECT count(*),max(migration_name) FROM schema_migrations").Scan(&count, &latest); err != nil || count != len(names) || latest != names[len(names)-1] {
		t.Fatalf("initial schema count=%d latest=%s err=%v", count, latest, err)
	}
}

func roleUpgradeIdentities(t *testing.T, ctx context.Context, pool *pgxpool.Pool, actors []roleUpgradeActor) []byte {
	t.Helper()
	var names []string
	for _, actor := range actors {
		names = append(names, actor.Login)
		if actor.Group != "" && !slices.Contains(names, actor.Group) {
			names = append(names, actor.Group)
		}
	}
	var body []byte
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'roles',(SELECT jsonb_agg(jsonb_build_object('oid',oid,'name',rolname,'login',rolcanlogin,'superuser',rolsuper,'createdb',rolcreatedb,'createrole',rolcreaterole,'inherit',rolinherit,'replication',rolreplication,'bypassrls',rolbypassrls,'config',rolconfig,'limit',rolconnlimit,'valid_until',rolvaliduntil) ORDER BY rolname) FROM pg_roles WHERE rolname=ANY($1::text[])),
		'memberships',(SELECT jsonb_agg(to_jsonb(m) ORDER BY roleid,member) FROM pg_auth_members m WHERE roleid IN (SELECT oid FROM pg_roles WHERE rolname=ANY($1::text[])) OR member IN (SELECT oid FROM pg_roles WHERE rolname=ANY($1::text[]))))`, names).Scan(&body)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname=ANY($1::text[])", names).Scan(&count); err != nil || count != len(names) {
		t.Fatalf("identity count=%d err=%v", count, err)
	}
	return body
}

func roleUpgradeRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, baseline roleUpgradeBaseline) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, rule := range baseline.Manifests[ProfileQuery].Tables {
		var body []byte
		row := "to_jsonb(r)"
		query := "SELECT COALESCE(jsonb_agg(" + row + " ORDER BY " + row + "::text),'[]'::jsonb) FROM " + pgx.Identifier{"evidence", rule.Table}.Sanitize() + " r"
		if rule.Table == "schema_migrations" {
			query += " WHERE migration_name < '000050_'"
		}
		if err := pool.QueryRow(ctx, query).Scan(&body); err != nil {
			t.Fatal(err)
		}
		result[rule.Table] = roleUpgradeHash(body)
	}
	return result
}

func roleUpgradeAssertRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, baseline roleUpgradeBaseline, want map[string]string) {
	t.Helper()
	got := roleUpgradeRows(t, ctx, pool, baseline)
	for table, digest := range want {
		if got[table] != digest {
			t.Fatalf("old table content changed: %s", table)
		}
	}
}

type roleUpgradeSessionIdentity struct {
	SessionUser, CurrentUser, Database string
	PID                                int
}

func roleUpgradeSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, actor roleUpgradeActor) roleUpgradeSessionIdentity {
	t.Helper()
	var got roleUpgradeSessionIdentity
	if err := pool.QueryRow(ctx, "SELECT session_user,current_user,current_database(),pg_backend_pid()").Scan(&got.SessionUser, &got.CurrentUser, &got.Database, &got.PID); err != nil {
		t.Fatal(err)
	}
	wantRole := actor.Group
	if wantRole == "" {
		wantRole = actor.Login
	}
	if got.SessionUser != actor.Login || got.CurrentUser != wantRole || got.Database != actor.Config.ConnConfig.Database {
		t.Fatalf("wrong session identity: %+v", got)
	}
	return got
}

func roleUpgradeCheckView(t *testing.T, source roleUpgradeSource, got evidenceingestion.BoundedSourceViewResult) {
	t.Helper()
	if got.Status != evidenceingestion.BoundedSourceViewStatusAvailable || got.Input == nil {
		t.Fatalf("nonempty source unavailable: %+v", got)
	}
	input := got.Input
	if input.SourceSnapshotID != source.Receipt.SourceSnapshotID || input.ExtractionViewID != source.Receipt.ExtractionViewID || input.SourceID != source.Receipt.SourceID || input.SourceVersion != "1" || input.RenderedText != source.Content || input.RawContentHash != source.Receipt.RawContentHash || len(input.Spans) == 0 {
		t.Fatalf("source identity or content differs: %+v", input)
	}
	raw := []byte(source.Content)
	for _, span := range input.Spans {
		if span.StartByte < 0 || span.EndByte <= span.StartByte || span.EndByte > len(raw) || span.Text != string(raw[span.StartByte:span.EndByte]) || span.QuotedTextHash != "sha256:"+roleUpgradeHash([]byte(span.Text)) {
			t.Fatalf("span not grounded in fixed source: %+v", span)
		}
	}
}

func roleUpgradePermissionDenied(t *testing.T, err error) {
	t.Helper()
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("want SQLSTATE 42501, got %v", err)
	}
}

func roleUpgradeWriteMatrix(t *testing.T, ctx context.Context, pool *pgxpool.Pool, actor roleUpgradeActor) {
	t.Helper()
	var common bool
	if err := pool.QueryRow(ctx, "SELECT has_schema_privilege(current_user,'evidence','USAGE') AND has_table_privilege(current_user,'evidence.source_blobs','SELECT') AND has_table_privilege(current_user,'evidence.proposal_occurrences','SELECT')").Scan(&common); err != nil || !common {
		t.Fatalf("write-probe common privileges absent: %t %v", common, err)
	}
	for _, probe := range []struct {
		table, privilege, sql string
		allow                 bool
	}{
		{"source_blobs", "INSERT", "INSERT INTO source_blobs(raw_content_hash,raw_content,byte_length) SELECT 'unused',decode('','hex'),0 WHERE false", (actor.Profile == ProfileIntake || actor.Profile == ProfileRepositoryIntake)},
		{"proposal_occurrences", "UPDATE", "UPDATE proposal_occurrences SET statement_text=statement_text WHERE false", (actor.Profile == ProfileSourceClaimReviewer || actor.Profile == ProfileEndpointReviewer)},
	} {
		var allowed bool
		if err := pool.QueryRow(ctx, "SELECT has_table_privilege(current_user,$1,$2)", "evidence."+probe.table, probe.privilege).Scan(&allowed); err != nil || allowed != probe.allow {
			t.Fatalf("wrong declared %s %s authority: %t %v", probe.table, probe.privilege, allowed, err)
		}
		tag, err := pool.Exec(ctx, probe.sql)
		if !probe.allow {
			roleUpgradePermissionDenied(t, err)
		} else if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("zero-row allowed probe: %v rows=%d", err, tag.RowsAffected())
		}
	}
}

func roleUpgradeRejectOldPolicy(t *testing.T, ctx context.Context, actor roleUpgradeActor, old Manifest) {
	t.Helper()
	runtime, _, err := OpenRuntimePool(ctx, actor.Config, RuntimePoolInput{Role: actor.Group, Schema: "evidence", Profile: actor.Profile})
	if runtime != nil {
		runtime.Close()
	}
	if !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("old policy must be rejected by policy verifier: %v", err)
	}
	current, buildErr := BuildManifest(actor.Profile)
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	missing := map[string]bool{}
	for _, rule := range current.Tables {
		for _, p := range rule.Privileges {
			missing[fmt.Sprintf("is missing %s on table %q", p, rule.Table)] = true
		}
	}
	for _, rule := range old.Tables {
		for _, p := range rule.Privileges {
			delete(missing, fmt.Sprintf("is missing %s on table %q", p, rule.Table))
		}
	}
	for fragment := range missing {
		if strings.Contains(err.Error(), fragment) {
			t.Logf("old policy rejected for a declared ACL delta: %s", fragment)
			return
		}
	}
	t.Fatalf("rejection was not a known old-to-new ACL delta: %v", err)
}
