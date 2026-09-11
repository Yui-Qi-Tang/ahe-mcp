//go:build integration

package mcpintegration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Actual admin and launcher binaries own all runtime provisioning and startup.
// The fixture creates only its private schema; approval is synthetic test data,
// not evidence of authenticated human review or a production deployment.
func TestIntegrationRuntimeProvisioningLauncherRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	directory := provisioningProtectedDirectory(t, databaseURL)
	binaries := buildProvisioningCommands(t, ctx, directory)
	fixture := newProvisioningLauncherFixture(t, ctx, databaseURL, binaries["ahe-runtime-admin"])
	configs := make(map[dbrole.Profile]provisioningLauncherConfig)
	paths := make(map[dbrole.Profile]string)
	for _, profile := range []dbrole.Profile{dbrole.ProfileQuery, dbrole.ProfileIntake, dbrole.ProfileSourceClaimReviewer} {
		identity := fixture.identities[profile]
		command := "ahe-ingest-mcp"
		if profile == dbrole.ProfileQuery {
			command = "ahe-query-mcp"
		}
		credentialPath := filepath.Join(directory, string(profile)+".dsn")
		writeProvisioningProtectedFile(t, credentialPath, []byte(identity.dsn))
		cfg := provisioningLauncherConfig{
			SchemaVersion: "ahe-mcp-launcher/v1", BinaryPath: binaries[command], DatabaseDNSFile: credentialPath,
			Database: fixture.database, SessionUser: identity.login, Schema: fixture.schema,
			Role: identity.group, Profile: string(profile), PrincipalID: "mock:provisioned:" + string(profile),
		}
		configPath := filepath.Join(directory, string(profile)+".json")
		writeProvisioningConfig(t, configPath, cfg)
		configs[profile], paths[profile] = cfg, configPath
	}

	// Bad coordinates and unprotected files must produce no MCP startup reply.
	// Each case starts a real process with initialize on stdin and joins its EOF.
	for _, name := range []string{"database", "session_user", "schema", "role", "profile", "unknown_field", "duplicate_field", "config_permissions", "credential_permissions"} {
		t.Run("startup_rejects_"+name, func(t *testing.T) {
			cfg := configs[dbrole.ProfileQuery]
			path := filepath.Join(directory, "rejected-"+name+".json")
			switch name {
			case "database":
				cfg.Database = "unselected_database"
			case "session_user":
				cfg.SessionUser = fixture.identities[dbrole.ProfileIntake].login
			case "schema":
				cfg.Schema = fixture.schema + "_missing"
			case "role":
				cfg.Role = fixture.identities[dbrole.ProfileIntake].group
			case "profile":
				cfg.Profile = "intake"
			}
			writeProvisioningConfig(t, path, cfg)
			if name == "unknown_field" || name == "duplicate_field" {
				body, marshalErr := json.Marshal(cfg)
				if marshalErr != nil {
					t.Fatal("cannot encode rejected launcher configuration")
				}
				suffix := `,"unknown":"denied"}`
				if name == "duplicate_field" {
					suffix = `,"profile":"query"}`
				}
				writeProvisioningProtectedFile(t, path, append(body[:len(body)-1], suffix...))
			}
			permissionPath := ""
			if name == "config_permissions" {
				permissionPath = path
			} else if name == "credential_permissions" {
				permissionPath = cfg.DatabaseDNSFile
			}
			if permissionPath != "" {
				if err := os.Chmod(permissionPath, 0o640); err != nil {
					t.Fatal("cannot prepare unprotected fixture")
				}
				defer func() {
					if err := os.Chmod(permissionPath, 0o600); err != nil {
						t.Error("cannot restore protected fixture")
					}
				}()
			}
			assertProvisioningLauncherStartupRejected(t, ctx, binaries["ahe-mcp-launch"], path)
		})
	}
	for _, table := range []string{"source_snapshots", "proposal_occurrences", "canonical_graph_nodes", "admission_decisions", "canonical_source_claim_review_bindings"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}

	intake := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], paths[dbrole.ProfileIntake], "ahe-ingest-mcp")
	reviewer := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], paths[dbrole.ProfileSourceClaimReviewer], "ahe-ingest-mcp")
	query := startProvisionedLauncher(t, ctx, binaries["ahe-mcp-launch"], paths[dbrole.ProfileQuery], "ahe-query-mcp")
	intake.assertTools(t, []string{"submit_manual_evidence", "submit_text_source", "submit_external_source", "submit_extractor_output", "get_extractor_input"})
	reviewer.assertTools(t, []string{"get_source_claim_review", "admit_reviewed_source_claim", "record_reviewed_source_claim_disposition"})
	query.assertTools(t, []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"})
	const statement = "合成啟動器測試：退款應於七日內完成。"
	source := authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", map[string]any{
		"request_id": "provisioned-source", "source_id": "mock:provisioned-refunds", "source_version": "1", "raw_text": statement,
	})
	extraction := authorityProcessTool[evidenceingestionmcp.GetExtractorInputResponse](t, intake, "get_extractor_input", map[string]any{"extraction_view_id": source.ExtractionViewID})
	if extraction.RenderedText != statement || len(extraction.Spans) != 1 || extraction.Spans[0].Text != statement || extraction.Spans[0].QuotedTextHash != stdioContentHash([]byte(statement)) {
		t.Fatal("provisioned intake lost exact source bytes")
	}
	proposal := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", map[string]any{
		"request_id": "provisioned-output", "source_snapshot_id": source.SourceSnapshotID, "extraction_view_id": source.ExtractionViewID,
		"extractor_definition": map[string]any{"name": "mock-provisioned-extractor", "version": "1", "config": map[string]string{}},
		"extractor_output": map[string]any{"proposals": []any{map[string]any{
			"proposal_local_id": "claim-1", "statement_text": statement, "evidence_refs": []string{extraction.Spans[0].SpanID},
		}}},
	})
	if proposal.Status != "pending" || proposal.ProposalCount != 1 {
		t.Fatal("provisioned intake did not stop at pending")
	}
	pending := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"proposal_occurrence_id": proposal.ProposalOccurrenceID})
	if pending.AdmissionOutcome != "pending" || pending.CanonicalRef != nil || pending.StatementText != statement {
		t.Fatal("Query must expose the pending lifecycle without canonical authority")
	}
	reviewArgs := evidenceingestionmcp.GetSourceClaimReviewRequest{ExtractionAttemptID: proposal.ExtractionAttemptID, ProposalOccurrenceID: proposal.ProposalOccurrenceID}
	review := authorityProcessTool[evidenceingestionmcp.GetSourceClaimReviewResponse](t, reviewer, "get_source_claim_review", reviewArgs)
	if review.ProposalManifest.ProposalCount != 1 || len(review.ProposalManifest.Entries) != 1 || review.Subject.ReviewSubject.ProposalOccurrenceID != proposal.ProposalOccurrenceID || review.Display.ID != review.Subject.ReviewDisplayArtifactID || !strings.Contains(review.Display.PayloadUTF8, statement) {
		t.Fatal("provisioned reviewer lost the complete exact review subject")
	}
	approved := evidenceingestionmcp.AdmitReviewedSourceClaimRequest{
		ExtractionAttemptID: proposal.ExtractionAttemptID, ExpectedSubject: review.Subject,
		Decision: "approved", DecisionReason: "合成角色與啟動器驗證，非真人核准證明",
	}
	for _, process := range []*authorityProcess{intake, query} {
		process.assertDenied(t, "get_source_claim_review", reviewArgs)
		process.assertDenied(t, "admit_reviewed_source_claim", approved)
	}
	for _, tool := range []string{"submit_text_source", "submit_extractor_output", "admit_pending_proposal", "admit_pending_supersession", "admit_pending_canonical_contradiction", "activate_repository_source_generation"} {
		reviewer.assertDenied(t, tool, approved)
	}
	query.assertDenied(t, "submit_text_source", map[string]any{})
	encoded, _ := json.Marshal(approved)
	forged := append(append(json.RawMessage(nil), encoded[:len(encoded)-1]...), `,"decision_by":"spoofed"}`...)
	assertReviewProcessToolError(t, reviewer, forged, "invalid_request")
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_ordinary_admission_manifests", "canonical_source_claim_review_bindings"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
	admitted := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", approved)
	if admitted.Replayed || admitted.AdmissionOutcome != "admitted" || admitted.CanonicalRef == "" || len(admitted.RawEvidenceNodeIDs) != 1 || len(admitted.CanonicalEdgeIDs) != 1 {
		t.Fatal("provisioned reviewer did not admit one grounded source claim")
	}
	replay := authorityProcessTool[evidenceingestionmcp.AdmitReviewedSourceClaimResponse](t, reviewer, "admit_reviewed_source_claim", approved)
	if !replay.Replayed {
		t.Fatal("provisioned exact admission retry did not replay")
	}
	replay.Replayed = false
	if !reflect.DeepEqual(replay, admitted) {
		t.Fatal("provisioned exact retry changed canonical identity")
	}
	changed := approved
	changed.DecisionReason = "different synthetic reason"
	assertReviewProcessToolError(t, reviewer, changed, "admission_replay_conflict")
	canonical := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{"canonical_id": admitted.CanonicalRef})
	if canonical.RecordRef.Kind != "canonical_evidence" || canonical.RecordRef.ID != admitted.CanonicalRef || canonical.Canonical == nil || canonical.ProposalOriginRef == nil || canonical.ProposalOriginRef.ID != proposal.ProposalOccurrenceID || canonical.StatementText != statement || len(canonical.SourceRefs) != 1 || canonical.SourceRefs[0].QuotedText != statement || canonical.SourceRefs[0].QuotedTextHash != stdioContentHash([]byte(statement)) {
		t.Fatal("provisioned Query lost the canonical claim, original source or exact quote")
	}
	var principal, payload, displayID, reviewMode string
	if err := fixture.pool.QueryRow(ctx, `SELECT d.decision_by, b.review_display_payload_utf8, b.review_display_artifact_id, d.review_binding_contract_version FROM admission_decisions d JOIN canonical_source_claim_review_bindings b USING (admission_decision_id) WHERE d.admission_decision_id=$1`, admitted.AdmissionDecisionID).Scan(&principal, &payload, &displayID, &reviewMode); err != nil {
		t.Fatal("cannot inspect the persisted launcher-bound review receipt")
	}
	if principal != configs[dbrole.ProfileSourceClaimReviewer].PrincipalID || payload != review.Display.PayloadUTF8 || displayID != review.Display.ID || reviewMode != evidenceingestion.ReviewedSourceClaimAdmissionV1 {
		t.Fatal("inherited environment replaced the protected review principal or display")
	}
	for table, count := range map[string]int{"source_snapshots": 1, "extraction_attempts": 1, "proposal_occurrences": 1, "canonical_graph_nodes": 2, "canonical_graph_edges": 1, "admission_decisions": 1, "canonical_ordinary_admission_manifests": 1, "canonical_ordinary_admission_node_bindings": 2, "canonical_ordinary_admission_edge_bindings": 1, "canonical_source_claim_review_bindings": 1, "canonical_contradiction_proposals": 0, "canonical_supersession_admission_events": 0} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, count)
	}
	query.finish(t)
	reviewer.finish(t)
	intake.finish(t)
}

type provisioningLauncherConfig struct {
	SchemaVersion   string `json:"schema_version"`
	BinaryPath      string `json:"binary_path"`
	DatabaseDNSFile string `json:"database_dns_file"`
	Database        string `json:"database"`
	SessionUser     string `json:"session_user"`
	Schema          string `json:"schema"`
	Role            string `json:"role"`
	Profile         string `json:"profile"`
	PrincipalID     string `json:"principal_id"`
}

func provisioningProtectedDirectory(t *testing.T, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal("invalid explicit provisioning acceptance URL")
	}
	parent := u.Query().Get("host")
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent || !strings.HasPrefix(parent, "/private/tmp/") || filepath.Clean(parent) != parent {
		t.Fatal("provisioning acceptance requires its selected private temporary Unix socket directory")
	}
	directory, err := os.MkdirTemp(parent, "launcher-fixture-")
	if err != nil {
		t.Fatal("cannot create private launcher fixture directory")
	}
	t.Cleanup(func() {
		// Only the exact private directory just created by this fixture is removed.
		if err := os.RemoveAll(directory); err != nil {
			t.Error("cannot remove private launcher fixtures")
		}
	})
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal("cannot protect private launcher fixture directory")
	}
	return directory
}

type provisioningIdentity struct {
	group, login string
	profile      dbrole.Profile
	dsn          string // Ephemeral fixture credential: never print or retain it.
}

type provisioningLauncherFixture struct {
	database, schema string
	pool             *pgxpool.Pool
	identities       map[dbrole.Profile]provisioningIdentity
}

func newProvisioningLauncherFixture(t *testing.T, ctx context.Context, databaseURL, command string) provisioningLauncherFixture {
	t.Helper()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot open dedicated provisioning acceptance database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	fixture := provisioningLauncherFixture{identities: make(map[dbrole.Profile]provisioningIdentity)}
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&fixture.database); err != nil {
		t.Fatal("cannot identify provisioning database")
	}
	suffix := stdioRandomHex(t, 8)
	fixture.schema = "ahe_launch_" + suffix
	schemaID := pgx.Identifier{fixture.schema}.Sanitize()
	var roles []string
	schemaCreated := false
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if schemaCreated {
			if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaID+" CASCADE"); err != nil {
				t.Error("cannot drop isolated provisioning schema")
			}
		}
		for i := len(roles) - 1; i >= 0; i-- {
			roleID := pgx.Identifier{roles[i]}.Sanitize()
			var exists bool
			if err := admin.QueryRow(cleanupCtx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)`, roles[i]).Scan(&exists); err != nil {
				t.Error("cannot inspect isolated provisioning role cleanup")
				continue
			}
			if !exists {
				continue
			}
			if _, err := admin.Exec(cleanupCtx, "DROP OWNED BY "+roleID); err != nil {
				t.Error("cannot remove isolated provisioning role grants")
			}
			if _, err := admin.Exec(cleanupCtx, "DROP ROLE "+roleID); err != nil {
				t.Error("cannot drop isolated provisioning role")
			}
		}
	})
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatal("cannot create isolated provisioning schema")
	}
	schemaCreated = true
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid dedicated provisioning database configuration")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaID
	fixture.pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot open private migration pool")
	}
	t.Cleanup(fixture.pool.Close)
	if _, err := migrations.ApplyUp(ctx, fixture.pool); err != nil {
		t.Fatalf("apply private native migrations: %v", err)
	}
	operatorURL := provisioningExplicitURL(cfg, fixture.database, cfg.ConnConfig.User, cfg.ConnConfig.Password)
	for index, profile := range []dbrole.Profile{dbrole.ProfileQuery, dbrole.ProfileIntake, dbrole.ProfileSourceClaimReviewer} {
		identity := provisioningIdentity{group: "ahe_launch_group_" + strconv.Itoa(index) + "_" + suffix, login: "ahe_launch_login_" + strconv.Itoa(index) + "_" + suffix, profile: profile}
		var existing int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname=ANY($1::text[])`, []string{identity.group, identity.login}).Scan(&existing); err != nil || existing != 0 {
			t.Fatal("generated provisioning roles must be absent before this fixture owns them")
		}
		roles = append(roles, identity.group, identity.login)
		if index == 0 {
			for _, change := range []struct{ name, database, schema string }{
				{"database", "unselected_database", fixture.schema},
				{"schema", fixture.database, fixture.schema + "_missing"},
			} {
				t.Run("provision_rejects_"+change.name, func(t *testing.T) {
					runProvisioningAdmin(t, ctx, command, "provision", operatorURL, change.database, change.schema, identity, false)
					var count int
					if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname=ANY($1::text[])`, []string{identity.group, identity.login}).Scan(&count); err != nil || count != 0 {
						t.Fatal("rejected provisioning left a runtime role behind")
					}
				})
			}
		}
		provisioned := runProvisioningAdmin(t, ctx, command, "provision", operatorURL, fixture.database, fixture.schema, identity, true)
		// Provision deliberately creates PASSWORD NULL. Local test trust provides
		// authentication; this random URL password is never installed or retained.
		identity.dsn = provisioningExplicitURL(cfg, fixture.database, identity.login, stdioRandomHex(t, 24))
		verified := runProvisioningAdmin(t, ctx, command, "verify", identity.dsn, fixture.database, fixture.schema, identity, true)
		if provisioned.Binding != verified.Binding {
			t.Fatal("independent bounded LOGIN verification changed provisioned policy binding")
		}
		manifest, err := dbrole.BuildManifest(profile)
		if err != nil {
			t.Fatal("cannot build expected native runtime manifest")
		}
		manifestHash, err := manifest.Hash()
		if err != nil || verified.Binding != (dbrole.RuntimeBinding{SchemaVersion: dbrole.PolicyVersion, Profile: profile, Role: identity.group, Database: fixture.database, Schema: fixture.schema, ManifestHash: manifestHash}) {
			t.Fatal("provisioned role is not the exact current native policy")
		}
		if index == 0 {
			t.Run("existing_pair_is_not_adopted", func(t *testing.T) {
				runProvisioningAdmin(t, ctx, command, "provision", operatorURL, fixture.database, fixture.schema, identity, false)
				after := runProvisioningAdmin(t, ctx, command, "verify", identity.dsn, fixture.database, fixture.schema, identity, true)
				if after.Binding != verified.Binding {
					t.Fatal("rejected repeated provisioning changed the existing policy")
				}
			})
			t.Run("verify_rejects_operator_login", func(t *testing.T) {
				runProvisioningAdmin(t, ctx, command, "verify", operatorURL, fixture.database, fixture.schema, identity, false)
			})
		}
		fixture.identities[profile] = identity
	}
	return fixture
}

type provisioningAdminResult struct {
	SchemaVersion     string                `json:"schema_version"`
	Operation         string                `json:"operation"`
	CreatedRolePair   bool                  `json:"created_role_pair"`
	SessionUser       string                `json:"session_user"`
	Binding           dbrole.RuntimeBinding `json:"binding"`
	AppliedMigrations int                   `json:"applied_migrations"`
	LatestMigration   string                `json:"latest_migration"`
}

func runProvisioningAdmin(t *testing.T, ctx context.Context, command, operation, dsn, database, schema string, identity provisioningIdentity, wantSuccess bool) provisioningAdminResult {
	t.Helper()
	cmd := exec.CommandContext(ctx, command, operation)
	// In particular, no inherited PG* values reach the operator command.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_DNS=" + dsn, "AHE_DATABASE_NAME=" + database,
		"AHE_DATABASE_SCHEMA=" + schema, "AHE_DATABASE_ROLE=" + identity.group, "AHE_DATABASE_LOGIN=" + identity.login,
		"AHE_RUNTIME_PROFILE=" + string(identity.profile), "GORACE=halt_on_error=1"}
	var stdout bytes.Buffer
	raceOutput := &authorityRaceOutput{}
	cmd.Stdout, cmd.Stderr = &stdout, raceOutput
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if raceOutput.detected {
		t.Fatal("runtime admin child reported a data race")
	}
	if !wantSuccess {
		if err == nil || stdout.Len() != 0 {
			t.Fatal("invalid runtime administration did not fail without a success receipt")
		}
		return provisioningAdminResult{}
	}
	var result provisioningAdminResult
	if err != nil || json.Unmarshal(stdout.Bytes(), &result) != nil || result.SchemaVersion != "ahe-runtime-admin-result/v1" || result.Operation != operation || result.CreatedRolePair != (operation == "provision") || result.SessionUser != identity.login || result.AppliedMigrations != 46 || result.LatestMigration != "000046_evidence_ingestion_reviewed_disposition.up.sql" {
		t.Fatalf("compiled runtime admin %s did not return its exact credential-free receipt (output suppressed)", operation)
	}
	if strings.Contains(stdout.String(), dsn) {
		t.Fatal("runtime admin exposed a credential in its receipt")
	}
	return result
}

func provisioningExplicitURL(config *pgxpool.Config, database, login, password string) string {
	u := url.URL{Scheme: "postgresql", User: url.UserPassword(login, password), Path: "/" + database}
	u.RawQuery = url.Values{"host": {config.ConnConfig.Host}, "port": {strconv.Itoa(int(config.ConnConfig.Port))}, "sslmode": {"disable"}, "connect_timeout": {"3"}}.Encode()
	return u.String()
}

func buildProvisioningCommands(t *testing.T, ctx context.Context, directory string) map[string]string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate provisioning command sources")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	binaries := make(map[string]string)
	for _, name := range []string{"ahe-runtime-admin", "ahe-mcp-launch", "ahe-ingest-mcp", "ahe-query-mcp"} {
		binary := filepath.Join(directory, name)
		build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/"+name)
		build.Dir = root
		// GOFLAGS=-race from the acceptance launcher applies to every child build.
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
		if err := os.Chmod(binary, 0o700); err != nil {
			t.Fatal("cannot protect compiled runtime binary")
		}
		binaries[name] = binary
	}
	return binaries
}

func writeProvisioningProtectedFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal("cannot write isolated protected fixture")
	}
}

func writeProvisioningConfig(t *testing.T, path string, config provisioningLauncherConfig) {
	t.Helper()
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal("cannot encode protected runtime configuration")
	}
	writeProvisioningProtectedFile(t, path, body)
}

func provisioningPollutedEnvironment() []string {
	return []string{"PATH=/invalid/inherited/path", "PGHOST=/invalid/inherited/socket", "PGPORT=1", "PGUSER=spoofed", "PGDATABASE=spoofed", "PGOPTIONS=-c search_path=public",
		"DATABASE_DNS=invalid-inherited-credential", "AHE_DATABASE_NAME=spoofed", "AHE_DATABASE_SCHEMA=public", "AHE_DATABASE_ROLE=spoofed",
		"AHE_RUNTIME_PROFILE=legacy-operator", "AHE_RUNTIME_PRINCIPAL_ID=spoofed", "AHE_DATABASE_LOGIN=spoofed", "GORACE=halt_on_error=1"}
}

func assertProvisioningLauncherStartupRejected(t *testing.T, ctx context.Context, command, configPath string) {
	t.Helper()
	childCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, command, "--config", configPath)
	cmd.Env = provisioningPollutedEnvironment()
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var stdout bytes.Buffer
	raceOutput := &authorityRaceOutput{}
	cmd.Stdout, cmd.Stderr = &stdout, raceOutput
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if childCtx.Err() != nil || err == nil || stdout.Len() != 0 || raceOutput.detected {
		t.Fatal("invalid protected launcher configuration did not fail closed before MCP startup")
	}
}

func startProvisionedLauncher(t *testing.T, ctx context.Context, command, configPath, serverName string) *authorityProcess {
	t.Helper()
	processCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	cmd := exec.CommandContext(processCtx, command, "--config", configPath)
	cmd.Env = provisioningPollutedEnvironment()
	raceOutput := &authorityRaceOutput{}
	cmd.Stderr = raceOutput // Retain no child stderr or connection material.
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal("cannot open protected launcher stdin")
	}
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal("cannot start protected launcher")
	}
	responses := make(chan []byte, 1)
	process := &authorityProcess{ctx: processCtx, input: json.NewEncoder(stdin), stdin: stdin, responses: responses,
		processDone: make(chan struct{}), scannerDone: make(chan struct{}), raceOutput: raceOutput}
	go func() {
		process.waitErr = cmd.Wait()
		_ = writer.Close()
		close(process.processDone)
	}()
	go func() {
		defer func() { close(responses); close(process.scannerDone) }()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 16<<20)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case responses <- line:
			case <-processCtx.Done():
				return
			}
		}
		process.scanErr = scanner.Err()
	}()
	t.Cleanup(func() {
		cancel()
		_ = stdin.Close()
		_ = reader.Close()
		select {
		case <-process.processDone:
		default:
			_ = cmd.Process.Kill()
			select {
			case <-process.processDone:
			case <-time.After(5 * time.Second):
				t.Error("protected launcher did not join before role cleanup")
			}
		}
		select {
		case <-process.scannerDone:
		case <-time.After(5 * time.Second):
			t.Error("protected launcher stdout did not join before role cleanup")
		}
	})
	initialized := process.request(t, "initialize", map[string]any{"protocolVersion": mcpstdio.ProtocolVersion,
		"capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mock-provisioning-test", "version": "1"}})
	var result struct {
		ProtocolVersion string                `json:"protocolVersion"`
		ServerInfo      struct{ Name string } `json:"serverInfo"`
	}
	if initialized.Error != nil || json.Unmarshal(initialized.Result, &result) != nil || result.ProtocolVersion != mcpstdio.ProtocolVersion || result.ServerInfo.Name != serverName {
		t.Fatal("protected launcher did not initialize its configured MCP binary")
	}
	return process
}
