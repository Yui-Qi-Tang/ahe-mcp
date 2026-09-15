//go:build integration

package mcpintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const hanQueryLabMode = "experimental_han_lexical_recovery_v1"

// This witness creates only synthetic pending proposals and typed repository
// generations. It does not reproduce the real T1-T4 admission decisions, call a
// model, or establish that a retrieved statement is supported by its source.
type hanQueryLabSource struct {
	Label, SourceID, Version, RawText string
	Statements                        []string
	Source                            evidenceingestionmcp.SubmitTextSourceResponse
	Input                             evidenceingestionmcp.GetExtractorInputResponse
	Output                            evidenceingestionmcp.SubmitExtractorOutputResponse
	Records                           []evidencequerymcp.GetEvidenceRecordResponse
}

type hanQueryLabRepository struct {
	Label, GoMod, GoSource string
	Snapshot               evidenceingestion.RepositorySnapshotCaptureResult
	Extraction             evidenceingestion.RepositoryIngestResult
	Records                []evidencequerymcp.GetEvidenceRecordResponse
}

type hanQueryLabCase struct {
	Name              string
	Request           evidencequerymcp.GetGroundedEvidenceBriefRequest
	ExpectedIDs       []string
	WantFallback      bool
	WantTruncated     bool
	ExpectedToolError string
	Note              string
}

type hanQueryLabObservation struct {
	Case      hanQueryLabCase
	Stage     string
	Response  evidencequerymcp.GroundedEvidenceBriefResponse
	Baseline  *evidencequerymcp.GroundedEvidenceBriefResponse `json:",omitempty"`
	ToolError json.RawMessage                                 `json:",omitempty"`
	Passed    bool
}

type hanQueryLabTableState struct {
	Rows   int
	SHA256 string
}

type hanQueryLabReport struct {
	Contract, StartedAt, FinishedAt, Database, SystemIdentifier, Schema string
	Stage                                                               string
	ServerVersion                                                       int
	SocketOnly, Passed, QueryEOF, IntakeEOF, RuntimeWriteDenials        bool
	SchemaCleanupVerified                                               bool
	PermanentTablesBefore, PermanentTablesAfterCleanup                  int
	Limitations                                                         []string
	FilterCoverage                                                      []string
	LiveQueryTools                                                      json.RawMessage
	Sources                                                             []hanQueryLabSource
	Repositories                                                        []hanQueryLabRepository
	Cases                                                               []hanQueryLabObservation
	CodeSHA256                                                          map[string]string
	SourcePlanSHA256, ResolvedCasePlanSHA256                            string
	BeforeQueries, AfterQueries                                         map[string]hanQueryLabTableState
}

// Neither this config nor the test consumes a general-purpose database DSN.
// All inherited PG settings are rejected before pgx can consult service files,
// passwords, host overrides, or target-session settings.
func hanQueryLabConfig(socket, port string) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN") {
			return nil, fmt.Errorf("Han lab refuses inherited database environment settings")
		}
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || socket == "/" || strings.ContainsAny(socket, "\x00\r\n") || err != nil || portNumber == 0 {
		return nil, fmt.Errorf("Han lab requires an explicit absolute Unix socket directory and port")
	}
	config, err := pgx.ParseConfig("host=/nonexistent port=1 user=ahe_han_lab_operator dbname=ahe_han_mcp_lab password='' passfile=/dev/null sslmode=disable target_session_attrs=any connect_timeout=5")
	if err != nil {
		return nil, fmt.Errorf("cannot parse fixed Han lab configuration")
	}
	config.Host, config.Port = socket, uint16(portNumber)
	config.Password, config.Fallbacks = "", nil
	config.RuntimeParams = map[string]string{"application_name": "ahe_han_query_mcp_lab", "statement_timeout": "10000"}
	return config, nil
}

func TestHanQueryLabConfig(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN" {
			t.Setenv(name, "")
		}
	}
	config, err := hanQueryLabConfig("/private/tmp/han-query-config-test", "55441")
	if err != nil || config.Host != "/private/tmp/han-query-config-test" || config.Port != 55441 || config.Database != "ahe_han_mcp_lab" || config.User != "ahe_han_lab_operator" || config.Password != "" || config.TLSConfig != nil || len(config.Fallbacks) != 0 || len(config.RuntimeParams) != 2 {
		t.Fatal("fixed Han lab config did not preserve the socket-only boundary")
	}
	for _, name := range []string{"PGHOST", "PGSERVICE", "PGSERVICEFILE", "PGPASSWORD", "PGPASSFILE", "DATABASE_DSN", "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "synthetic-setting-must-not-be-consumed")
			if got, err := hanQueryLabConfig("/private/tmp/han-query-config-test", "55441"); err == nil || got != nil {
				t.Fatal("inherited database setting reached config parsing")
			}
		})
	}
	for _, input := range [][2]string{{"localhost", "55441"}, {"/", "55441"}, {"/private/tmp/../tmp", "55441"}, {"/private/tmp/lab", ""}, {"/private/tmp/lab", "0"}, {"/private/tmp/lab", "65536"}} {
		if got, err := hanQueryLabConfig(input[0], input[1]); err == nil || got != nil {
			t.Fatal("invalid socket/port configuration was accepted")
		}
	}
}

func TestIntegrationHanQueryMCPIsolatedLab(t *testing.T) {
	socket, port := os.Getenv("AHE_HAN_MCP_LAB_SOCKET"), os.Getenv("AHE_HAN_MCP_LAB_PORT")
	if socket == "" && port == "" {
		t.Skip("set AHE_HAN_MCP_LAB_SOCKET and AHE_HAN_MCP_LAB_PORT for the disposable lab")
	}
	config, err := hanQueryLabConfig(socket, port)
	if err != nil {
		t.Fatal(err)
	}
	expectedIdentity := os.Getenv("AHE_HAN_MCP_LAB_SYSTEM_IDENTIFIER")
	if _, err := strconv.ParseUint(expectedIdentity, 10, 64); err != nil || expectedIdentity == "0" {
		t.Fatal("AHE_HAN_MCP_LAB_SYSTEM_IDENTIFIER must explicitly pin the isolated PostgreSQL cluster")
	}
	report := hanQueryLabReport{
		Contract: "han-query-mcp-isolated-lab-v1", StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Stage: "preflight",
		Limitations: []string{
			"Synthetic pending-only fixtures; no model, human admission, reject, or audit_only decision was executed.",
			"Admitted/rejected/audit_only filters are exclusion witnesses only, not positive disposition coverage.",
			"Repository generations are prepared through typed APIs in a temporary fixture repository; this is not canonical admission.",
			"Hits are lexical candidates, not source support, temporal correctness, completeness, or a model quality score.",
			"Source context is post-retrieval only; the source-only keyword controls must remain misses.",
			"The schema and runtime roles are disposable; this report retains observations, not a persistent Desktop/DB preview.",
			"Repeated ordering and one same-batch tie are tested; this small lab does not establish production-scale performance.",
		},
		FilterCoverage: []string{"source_snapshot_id", "repository_snapshot_id", "source_generation_id", "source_id", "source_version", "admission_outcome", "lifecycle_scope", "limit (bound, not an identity filter)", "source_kind is not a request filter and is not invented here"},
	}
	hanQueryLabSaveReport(t, &report)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open isolated Han lab database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var listen string
	var publicAuthority, extensions int
	if err := admin.QueryRow(ctx, `SELECT current_database(), current_setting('listen_addresses'), current_setting('server_version_num')::integer, (SELECT system_identifier::text FROM pg_control_system()), (SELECT count(*) FROM pg_extension WHERE extname <> 'plpgsql'), (SELECT count(*) FROM pg_database d, LATERAL aclexplode(COALESCE(d.datacl, acldefault('d',d.datdba))) acl WHERE d.datname=current_database() AND acl.grantee=0 AND acl.privilege_type IN ('CREATE','TEMPORARY'))`).Scan(&report.Database, &listen, &report.ServerVersion, &report.SystemIdentifier, &extensions, &publicAuthority); err != nil {
		t.Fatal("cannot verify isolated Han lab identity and authority boundary")
	}
	report.SocketOnly = listen == ""
	if report.Database != "ahe_han_mcp_lab" || !report.SocketOnly || report.ServerVersion/10000 != 18 || report.SystemIdentifier != expectedIdentity || extensions != 0 || publicAuthority != 0 {
		t.Fatal("Han lab requires the pinned socket-only PostgreSQL 18 database with PUBLIC CREATE/TEMPORARY already revoked and no additional extensions")
	}
	report.PermanentTablesBefore = hanQueryLabPermanentTables(t, ctx, admin)
	if report.PermanentTablesBefore != 0 {
		t.Fatal("Han lab must start without permanent user tables")
	}
	// This cleanup is registered before the shared fixture, so it checks the
	// schema after all processes, pools, generated roles, and schema are removed.
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if report.Schema == "" {
			// A setup failure may occur before the fixture returns its schema.
			// Zero remaining tables is still observable, but does not verify
			// cleanup of an unidentified schema or its generated roles.
			report.PermanentTablesAfterCleanup = hanQueryLabPermanentTables(t, cleanupCtx, admin)
			return
		}
		var exists bool
		if err := admin.QueryRow(cleanupCtx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, report.Schema).Scan(&exists); err != nil {
			t.Error("cannot verify disposable Han schema cleanup")
			return
		}
		report.PermanentTablesAfterCleanup = hanQueryLabPermanentTables(t, cleanupCtx, admin)
		report.SchemaCleanupVerified = !exists && report.PermanentTablesAfterCleanup == report.PermanentTablesBefore
		if !report.SchemaCleanupVerified {
			t.Error("disposable Han schema cleanup was not verified")
		}
	})
	// Encode only the fixed, validated socket destination. This value is passed
	// directly to the fixture and is never exported through a general DSN env.
	destination := url.URL{Scheme: "postgres", User: url.User(config.User), Path: "/" + config.Database}
	destination.RawQuery = url.Values{"host": {config.Host}, "port": {strconv.Itoa(int(config.Port))}, "sslmode": {"disable"}, "passfile": {"/dev/null"}, "connect_timeout": {"5"}}.Encode()
	report.Stage = "fixture_setup"
	fixture := newAuthorityProcessFixture(t, ctx, destination.String())
	report.Schema = fixture.schema
	report.CodeSHA256 = hanQueryLabCodeHashes(t)
	report.Stage = "repository_fixture_setup"
	report.Repositories = hanQueryLabRepositoryFixtures(t, ctx, fixture.pool)
	report.Stage = "compiled_process_setup"
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", fixture.intake, fixture.schema, "intake")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", fixture.query, fixture.schema, "")
	report.LiveQueryTools = hanQueryLabTools(t, query)
	intake.assertTools(t, []string{"submit_manual_evidence", "submit_text_source", "submit_external_source", "submit_extractor_output", "get_extractor_input"})
	report.Sources = hanQueryLabSourcePlan()
	report.SourcePlanSHA256 = hanQueryLabJSONHash(t, report.Sources)
	report.Stage = "source_fixture_setup"
	for i := range report.Sources {
		hanQueryLabSubmitSource(t, intake, query, &report.Sources[i])
	}
	for i := range report.Repositories {
		for _, id := range report.Repositories[i].Extraction.ProposalOccurrenceIDs {
			record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]string{"proposal_occurrence_id": id})
			report.Repositories[i].Records = append(report.Repositories[i].Records, record)
		}
	}
	cases := hanQueryLabCases(t, ctx, fixture.pool, report.Sources, report.Repositories)
	report.ResolvedCasePlanSHA256 = hanQueryLabJSONHash(t, cases)
	report.BeforeQueries = hanQueryLabTableDigests(t, ctx, fixture.pool, fixture.schema)
	report.Stage = "query_cases"
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			observation := hanQueryLabObservation{Case: tc, Stage: "request"}
			defer func() {
				observation.Passed = observation.Stage == "complete" && !t.Failed()
				report.Cases = append(report.Cases, observation)
			}()
			if tc.ExpectedToolError != "" {
				result := query.request(t, "tools/call", map[string]any{"name": "get_grounded_evidence_brief", "arguments": tc.Request})
				var envelope struct {
					IsError           bool            `json:"isError"`
					StructuredContent json.RawMessage `json:"structuredContent"`
				}
				var detail struct {
					Code string `json:"code"`
				}
				observation.Stage = "expected_error_validation"
				if result.Error != nil || json.Unmarshal(result.Result, &envelope) != nil || !envelope.IsError || json.Unmarshal(envelope.StructuredContent, &detail) != nil || detail.Code != tc.ExpectedToolError {
					t.Error("incompatible filter combination was not rejected at the request boundary")
				}
				observation.ToolError = envelope.StructuredContent
				observation.Stage = "complete"
				return
			}
			response := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, query, "get_grounded_evidence_brief", tc.Request)
			observation.Response = response
			observation.Stage = "response_validation"
			hanQueryLabAssertResponse(t, tc, response)
			if strings.HasPrefix(tc.Name, "english-baseline-") {
				request := tc.Request
				request.QueryMode = ""
				observation.Stage = "baseline_request"
				old := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, query, "get_grounded_evidence_brief", request)
				observation.Baseline = &old
				observation.Stage = "baseline_validation"
				if !reflect.DeepEqual(response.Matches, old.Matches) || response.Counts != old.Counts || !reflect.DeepEqual(response.QueryExecution.Attempts, old.QueryExecution.Attempts) || response.QueryExecution.CompletionReason != old.QueryExecution.CompletionReason || response.QueryExecution.FallbackStatus != "baseline_hit_preserved" {
					t.Error("experimental opt-in changed an existing English result, rank, order, or attempt trace")
				}
			}
			observation.Stage = "replay_request"
			replayed := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, query, "get_grounded_evidence_brief", tc.Request)
			observation.Stage = "replay_validation"
			if !reflect.DeepEqual(response, replayed) {
				t.Error("unchanged query did not return the same ordered response")
			}
			observation.Stage = "complete"
		})
	}
	report.Stage = "authority_denials_and_readonly_verification"
	for _, tool := range []string{"submit_text_source", "submit_extractor_output", "admit_pending_proposal", "record_pending_proposal_disposition"} {
		query.assertDenied(t, tool, map[string]any{})
	}
	for _, tool := range []string{"admit_pending_proposal", "record_pending_proposal_disposition"} {
		intake.assertDenied(t, tool, map[string]any{})
	}
	assertAuthorityDatabaseWritesDenied(t, ctx, fixture.schema, fixture.query)
	assertAuthorityDatabaseWritesDenied(t, ctx, fixture.schema, fixture.intake)
	report.RuntimeWriteDenials = true
	report.AfterQueries = hanQueryLabTableDigests(t, ctx, fixture.pool, fixture.schema)
	if !reflect.DeepEqual(report.BeforeQueries, report.AfterQueries) {
		t.Error("schema row contents changed during Query and authority denial checks")
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_contradiction_proposals", "canonical_supersession_admission_events"} {
		if state, ok := report.AfterQueries[table]; !ok || state.Rows != 0 {
			t.Errorf("pending-only lab unexpectedly populated %s", table)
		}
	}
	if !reflect.DeepEqual(report.CodeSHA256, hanQueryLabCodeHashes(t)) {
		t.Error("source files changed during the compiled-command witness")
	}
	report.Stage = "normal_process_shutdown"
	query.finish(t)
	report.QueryEOF = true
	intake.finish(t)
	report.IntakeEOF = true
	report.Stage = "complete"
}

func hanQueryLabSourcePlan() []hanQueryLabSource {
	return []hanQueryLabSource{
		{Label: "observatory", SourceID: "han-lab:observatory", Version: "v1", RawText: "The synthetic observatory reported a preliminary magnitude 4.2 earthquake near Sample Island.\nThe location and depth remain under review. This bulletin does not report damage or casualties.", Statements: []string{"合成天文台報告稱，在樣本島附近發生了一次初步測得芮氏規模4.2的地震，其具體位置和深度仍在審查中，報告未提及任何損壞或傷亡。"}},
		{Label: "maintenance-v1", SourceID: "han-lab:maintenance", Version: "v1", RawText: "The synthetic Orion bulletin announces a planned Search index rebuild.\nThe rebuild has not started. This bulletin reports a plan, not an observed service incident.", Statements: []string{"合成的 Orion 公告宣布計劃進行搜尋索引重建，但該重建尚未開始。", "Orion 公告中的搜尋索引重建仍屬計畫，尚未開始執行。"}},
		{Label: "maintenance-v2", SourceID: "han-lab:maintenance", Version: "v2", RawText: "The synthetic Orion bulletin still describes a planned Search index rebuild.\nThe rebuild has not started.", Statements: []string{"Orion 公告仍將搜尋索引重建列為計畫，尚未開始。"}},
		{Label: "ascii-prefix", SourceID: "han-lab:controls", Version: "c1", RawText: "OrionBeta 搜尋索引重建尚未開始。", Statements: []string{"OrionBeta 搜尋索引重建尚未開始。"}},
		{Label: "missing-han", SourceID: "han-lab:controls", Version: "c2", RawText: "Orion Search 維護仍在進行。", Statements: []string{"Orion Search 維護仍在進行。"}},
		{Label: "missing-ascii", SourceID: "han-lab:controls", Version: "c3", RawText: "搜尋索引重建仍在規劃中。", Statements: []string{"搜尋索引重建仍在規劃中。"}},
		{Label: "source-only-maintenance", SourceID: "han-lab:controls", Version: "c4", RawText: "Orion 搜尋索引重建", Statements: []string{"A synthetic service maintenance bulletin exists."}},
		{Label: "source-only-earthquake", SourceID: "han-lab:controls", Version: "c5", RawText: "地震 芮氏", Statements: []string{"The synthetic earthquake report is under review."}},
		{Label: "missing-second-han", SourceID: "han-lab:controls", Version: "c6", RawText: "地震區域仍在審查中。", Statements: []string{"地震區域仍在審查中。"}},
		{Label: "missing-first-han", SourceID: "han-lab:controls", Version: "c7", RawText: "芮氏規模4.2仍在審查中。", Statements: []string{"芮氏規模4.2仍在審查中。"}},
		{Label: "english", SourceID: "han-lab:english", Version: "v1", RawText: "A proposal lifecycle remains pending.\nProposals lifecycle records remain pending.", Statements: []string{"A proposal lifecycle remains pending.", "Proposals lifecycle records remain pending."}},
	}
}

func hanQueryLabSubmitSource(t *testing.T, intake, query *authorityProcess, fixture *hanQueryLabSource) {
	t.Helper()
	fixture.Source = authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", evidenceingestionmcp.SubmitTextSourceRequest{
		RequestID: "han-source-" + fixture.Label, SourceID: fixture.SourceID, SourceVersion: fixture.Version,
		RawText: fixture.RawText, OriginMetadata: map[string]string{"fixture": "han-query-mcp-lab-synthetic"},
	})
	fixture.Input = authorityProcessTool[evidenceingestionmcp.GetExtractorInputResponse](t, intake, "get_extractor_input", evidenceingestionmcp.GetExtractorInputRequest{ExtractionViewID: fixture.Source.ExtractionViewID})
	if fixture.Input.RenderedText != fixture.RawText || fixture.Source.RawContentHash != stdioContentHash([]byte(fixture.RawText)) || len(fixture.Input.Spans) == 0 {
		t.Fatal("synthetic source bytes or spans were not preserved")
	}
	refs := make([]string, 0, len(fixture.Input.Spans))
	for _, span := range fixture.Input.Spans {
		refs = append(refs, span.SpanID)
	}
	proposals := make([]evidenceingestion.ExtractorProposalOutput, 0, len(fixture.Statements))
	for i, statement := range fixture.Statements {
		proposals = append(proposals, evidenceingestion.ExtractorProposalOutput{ProposalLocalID: fmt.Sprintf("claim-%d", i+1), StatementText: statement, EvidenceRefs: refs})
	}
	fixture.Output = authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", evidenceingestionmcp.SubmitExtractorOutputRequest{
		RequestID: "han-output-" + fixture.Label, SourceSnapshotID: fixture.Source.SourceSnapshotID, ExtractionViewID: fixture.Source.ExtractionViewID,
		ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "han-query-synthetic-fixture", Version: "1", Config: map[string]string{}},
		ExtractorOutput:     evidenceingestion.FrozenExtractorOutput{Proposals: proposals},
	})
	if fixture.Output.Status != "pending" || fixture.Output.ProposalCount != len(proposals) {
		t.Fatal("synthetic output was not pending-only")
	}
	listed := authorityProcessTool[evidencequerymcp.ListEvidenceRecordsResponse](t, query, "list_evidence_records", evidencequerymcp.ListEvidenceRecordsRequest{SourceSnapshotID: fixture.Source.SourceSnapshotID, AdmissionOutcome: "pending", Limit: 100})
	if listed.Count != len(proposals) {
		t.Fatal("synthetic pending identities were not all queryable")
	}
	fixture.Records = listed.Records
	for _, record := range fixture.Records {
		if !slices.Contains(fixture.Statements, record.StatementText) || record.AdmissionOutcome != "pending" || record.CanonicalRef != nil || record.Source.RawContentHash != fixture.Source.RawContentHash {
			t.Fatal("pending statement, source identity, or authority was changed")
		}
	}
}

func hanQueryLabRepositoryFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []hanQueryLabRepository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("temporary typed repository fixture requires git")
	}
	root := t.TempDir()
	const goMod = "module example.com/han-query-fixture\n\ngo 1.22\n"
	stdioWriteFile(t, filepath.Join(root, "go.mod"), []byte(goMod))
	stdioRunGit(t, root, "init", "--quiet")
	stdioRunGit(t, root, "config", "user.name", "AHE Synthetic Lab")
	stdioRunGit(t, root, "config", "user.email", "synthetic@example.invalid")
	var fixtures []hanQueryLabRepository
	for i, name := range []string{"索引重建甲", "索引重建乙"} {
		label := fmt.Sprintf("repository-generation-%d", i+1)
		goSource := "package sample\n\nfunc " + name + "() {}\n"
		stdioWriteFile(t, filepath.Join(root, "main.go"), []byte(goSource))
		stdioRunGit(t, root, "add", "go.mod", "main.go")
		stdioRunGit(t, root, "commit", "--quiet", "-m", label)
		cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
		sha, err := cmd.Output()
		if err != nil {
			t.Fatal("cannot identify temporary repository fixture revision")
		}
		snapshot, err := evidenceingestion.CaptureGitRepositorySnapshot(ctx, pool, evidenceingestion.GitRepositorySnapshotConfig{WorkspaceRoot: root, RepoID: "han-query-synthetic-repository", CommitSHA: strings.TrimSpace(string(sha)), RequestID: label + "-snapshot"})
		if err != nil {
			t.Fatalf("typed synthetic repository capture: %v", err)
		}
		extraction, err := evidenceingestion.RunRepositoryGoParserExtractor(ctx, pool, evidenceingestion.RepositoryGoParserRequest{RequestID: label + "-extract", RepositorySnapshotID: snapshot.RepositorySnapshot.ID})
		if err != nil {
			t.Fatalf("typed synthetic repository extraction: %v", err)
		}
		if _, err := evidenceingestion.ActivateRepositorySourceGeneration(ctx, pool, evidenceingestion.RepositorySourceGenerationActivationInput{RequestID: label + "-activate", SourceGenerationID: extraction.SourceGeneration.ID}); err != nil {
			t.Fatalf("typed synthetic repository activation: %v", err)
		}
		fixtures = append(fixtures, hanQueryLabRepository{Label: label, GoMod: goMod, GoSource: goSource, Snapshot: snapshot, Extraction: extraction})
	}
	return fixtures
}

func hanQueryLabCases(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sources []hanQueryLabSource, repositories []hanQueryLabRepository) []hanQueryLabCase {
	t.Helper()
	byLabel := make(map[string]hanQueryLabSource)
	for _, source := range sources {
		byLabel[source.Label] = source
	}
	ids := func(label string) []string {
		var result []string
		for _, record := range byLabel[label].Records {
			result = append(result, record.RecordRef.ID)
		}
		return hanQueryLabOrderedIDs(t, ctx, pool, result)
	}
	repoIDs := func(index int) []string {
		var result []string
		for _, record := range repositories[index].Records {
			if strings.Contains(record.StatementText, "索引重建") {
				result = append(result, record.RecordRef.ID)
			}
		}
		if len(result) != 1 {
			t.Fatal("typed repository fixture did not yield exactly one Han function candidate per generation")
		}
		return result
	}
	observatory := byLabel["observatory"]
	maintenance := byLabel["maintenance-v1"]
	maintenanceIDs := ids("maintenance-v1")
	allMaintenanceIDs := hanQueryLabOrderedIDs(t, ctx, pool, append(slices.Clone(maintenanceIDs), ids("maintenance-v2")...))
	oldRepoIDs, activeRepoIDs := repoIDs(0), repoIDs(1)
	allRepoIDs := hanQueryLabOrderedIDs(t, ctx, pool, append(slices.Clone(oldRepoIDs), activeRepoIDs...))
	// Both v1 proposals were created in one typed intake transaction. Verify
	// the timestamp tie independently, then require ID-ascending tie-breaking.
	var distinctCreatedAt int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT created_at) FROM proposal_occurrences WHERE proposal_occurrence_id=ANY($1::text[])`, maintenanceIDs).Scan(&distinctCreatedAt); err != nil || distinctCreatedAt != 1 || !slices.IsSorted(maintenanceIDs) {
		t.Fatal("same-batch ordering control did not establish an ID-ascending timestamp tie")
	}
	base := func(query string) evidencequerymcp.GetGroundedEvidenceBriefRequest {
		return evidencequerymcp.GetGroundedEvidenceBriefRequest{Query: query, QueryMode: hanQueryLabMode, ResponseSchema: evidencequerymcp.GroundedEvidenceBriefSchemaV3, Limit: 100}
	}
	var cases []hanQueryLabCase
	add := func(name string, request evidencequerymcp.GetGroundedEvidenceBriefRequest, expected []string, fallback bool, note string) {
		cases = append(cases, hanQueryLabCase{Name: name, Request: request, ExpectedIDs: expected, WantFallback: fallback, Note: note})
	}
	earthquake := base("地震 芮氏")
	earthquake.SourceSnapshotID = observatory.Source.SourceSnapshotID
	for _, mode := range []string{"", evidenceingestion.EvidenceQueryModeExactLexical, evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery} {
		request := earthquake
		request.QueryMode = mode
		add("old-mode-earthquake-"+mode, request, nil, false, "Existing modes must remain unchanged.")
	}
	add("han-earthquake-positive", earthquake, ids("observatory"), true, "Finding the persisted word 芮氏 does not make it supported by the original source.")
	for _, query := range []string{"  芮氏\t地震\n", "地震，芮氏"} {
		request := earthquake
		request.Query = query
		add("han-delimiters-"+query, request, ids("observatory"), true, "Explicit delimiters only; no semantic query rewriting.")
	}
	maintenanceRequest := base("Orion 索引重建")
	maintenanceRequest.SourceID = maintenance.SourceID
	add("source-id-positive", maintenanceRequest, allMaintenanceIDs, true, "Both source revisions remain distinct pending records.")
	for _, mode := range []string{"", evidenceingestion.EvidenceQueryModeExactLexical} {
		request := maintenanceRequest
		request.QueryMode = mode
		add("old-mode-maintenance-"+mode, request, nil, false, "The mixed-query default must still miss.")
	}
	for _, query := range []string{"orion，索引重建", "OrionBeta 索引重建", "Ori 索引重建", "Orion索引重建", "地 芮"} {
		request := maintenanceRequest
		request.Query = query
		var expected []string
		if strings.HasPrefix(query, "orion，") {
			expected = allMaintenanceIDs
		}
		add("ascii-and-token-boundary-"+query, request, expected, len(expected) > 0, "ASCII remains lexical, and unsupported mixed/single-Han tokens do not gain a substring rescue.")
	}
	for _, label := range []string{"source-only-maintenance", "ascii-prefix", "missing-han", "missing-ascii"} {
		request := base("Orion 索引重建")
		request.SourceSnapshotID = byLabel[label].Source.SourceSnapshotID
		add("same-statement-control-"+label, request, nil, false, "Source body and other records cannot supply missing statement terms.")
	}
	for _, label := range []string{"source-only-earthquake", "missing-second-han", "missing-first-han"} {
		request := base("地震 芮氏")
		request.SourceSnapshotID = byLabel[label].Source.SourceSnapshotID
		add("same-statement-control-"+label, request, nil, false, "All Han terms must occur in the same persisted statement.")
	}
	for _, outcome := range []string{"pending", "admitted", "rejected", "audit_only"} {
		request := earthquake
		request.AdmissionOutcome = outcome
		var expected []string
		if outcome == "pending" {
			expected = ids("observatory")
		}
		add("admission-outcome-"+outcome, request, expected, outcome == "pending", "Only pending has positive fixtures; no disposition is executed.")
	}
	for _, sourceID := range []string{observatory.SourceID, "han-lab:controls"} {
		request := earthquake
		request.SourceID = sourceID
		var expected []string
		if sourceID == observatory.SourceID {
			expected = ids("observatory")
		}
		add("combined-source-id-"+sourceID, request, expected, len(expected) > 0, "The supplied source ID must intersect the snapshot filter.")
	}
	for _, snapshotID := range []string{maintenance.Source.SourceSnapshotID, observatory.Source.SourceSnapshotID} {
		request := maintenanceRequest
		request.SourceSnapshotID = snapshotID
		var expected []string
		if snapshotID == maintenance.Source.SourceSnapshotID {
			expected = maintenanceIDs
		}
		add("source-snapshot-"+snapshotID, request, expected, len(expected) > 0, "Snapshot and source ID filters must both apply.")
	}
	for _, version := range []string{"v1", "v2", "absent-version"} {
		request := maintenanceRequest
		request.SourceVersion = version
		var expected []string
		if version == "v1" {
			expected = maintenanceIDs
		} else if version == "v2" {
			expected = ids("maintenance-v2")
		}
		add("source-version-"+version, request, expected, len(expected) > 0, "Source version is an exact lexical scope, not external freshness.")
	}
	limited := maintenanceRequest
	limited.SourceSnapshotID, limited.Limit = maintenance.Source.SourceSnapshotID, 1
	cases = append(cases, hanQueryLabCase{Name: "same-batch-tie-limit-one", Request: limited, ExpectedIDs: maintenanceIDs[:1], WantFallback: true, WantTruncated: true, Note: "Two fallback matches with the same creation timestamp; ID order resolves the tie. A limited response cannot claim complete coverage."})
	for _, scope := range []string{"", "active", "historical", "all"} {
		request := earthquake
		request.LifecycleScope = scope
		var expected []string
		if scope != "historical" {
			expected = ids("observatory")
		}
		add("manual-lifecycle-"+scope, request, expected, len(expected) > 0, "Manual source snapshots are not repository history.")
	}
	for _, scope := range []string{"", "active", "historical", "all"} {
		request := base("索引重建")
		request.ResponseSchema, request.LifecycleScope = evidencequerymcp.GroundedEvidenceBriefSchemaV5, scope
		request.RepositorySnapshotID = repositories[0].Snapshot.RepositorySnapshot.ID
		var expected []string
		if scope == "historical" || scope == "all" {
			expected = oldRepoIDs
		}
		add("old-repository-snapshot-"+scope, request, expected, len(expected) > 0, "Old snapshot is hidden by the default active lifecycle, not deleted.")
	}
	for _, scope := range []string{"", "active", "historical", "all"} {
		request := base("索引重建")
		request.ResponseSchema, request.LifecycleScope = evidencequerymcp.GroundedEvidenceBriefSchemaV5, scope
		request.RepositorySnapshotID = repositories[1].Snapshot.RepositorySnapshot.ID
		var expected []string
		if scope != "historical" {
			expected = activeRepoIDs
		}
		add("active-repository-snapshot-"+scope, request, expected, len(expected) > 0, "Active generation is not canonical admission.")
	}
	for i, repository := range repositories {
		for _, scope := range []string{"", "active", "historical", "all"} {
			request := base("索引重建")
			request.ResponseSchema, request.LifecycleScope = evidencequerymcp.GroundedEvidenceBriefSchemaV5, scope
			request.SourceGenerationID = repository.Extraction.SourceGeneration.ID
			var expected []string
			if scope == "" || scope == "all" || i == 0 && scope == "historical" || i == 1 && scope == "active" {
				expected = repoIDs(i)
			}
			add(fmt.Sprintf("repository-generation-%d-%s", i, scope), request, expected, len(expected) > 0, "Explicit generation defaults to all only when lifecycle_scope is omitted.")
		}
	}
	for _, filter := range []string{"wrong-snapshot", "wrong-generation", "manual-source-id-on-repository", "manual-version-on-repository", "repository-snapshot-on-manual", "repository-generation-on-manual"} {
		request := base("索引重建")
		request.ResponseSchema, request.LifecycleScope = evidencequerymcp.GroundedEvidenceBriefSchemaV5, "all"
		switch filter {
		case "wrong-snapshot":
			request.RepositorySnapshotID = "repo-snapshot:absent-han-lab"
		case "wrong-generation":
			request.SourceGenerationID = "generation:absent-han-lab"
		case "manual-source-id-on-repository":
			request.RepositorySnapshotID, request.SourceID = repositories[1].Snapshot.RepositorySnapshot.ID, maintenance.SourceID
		case "manual-version-on-repository":
			request.RepositorySnapshotID, request.SourceVersion = repositories[1].Snapshot.RepositorySnapshot.ID, "v1"
		case "repository-snapshot-on-manual":
			request = earthquake
			request.RepositorySnapshotID = repositories[1].Snapshot.RepositorySnapshot.ID
		case "repository-generation-on-manual":
			request = earthquake
			request.SourceGenerationID = repositories[1].Extraction.SourceGeneration.ID
		}
		add(filter, request, nil, false, "Inapplicable binding filters must exclude, not be ignored or join unrelated sources.")
		if filter == "repository-snapshot-on-manual" || filter == "repository-generation-on-manual" {
			cases[len(cases)-1].ExpectedToolError = "invalid_request"
			cases[len(cases)-1].Note = "The request contract rejects mutually exclusive source/repository filters before SQL; this is not a retrieval miss."
		}
	}
	// A mixed ASCII/Han query selects both repository generations without
	// matching the synthetic manual maintenance statements.
	repoAll := base("Go 索引重建")
	repoAll.ResponseSchema, repoAll.LifecycleScope = evidencequerymcp.GroundedEvidenceBriefSchemaV5, "all"
	add("repository-all-two-generations", repoAll, allRepoIDs, true, "Both generation IDs must survive all-scope retrieval.")
	for _, query := range []string{"proposal lifecycle", "proposals lifecycle", "proposal lifecycle timing"} {
		request := base(query)
		request.SourceID = byLabel["english"].SourceID
		// English rank ordering is separately observed and compared to the
		// unchanged baseline, not assigned Han recency ordering by this test.
		add("english-baseline-"+query, request, ids("english"), false, "Expected ID set and full baseline response equivalence are checked; timing is absent and does not become supported.")
	}
	return cases
}

func hanQueryLabAssertResponse(t *testing.T, tc hanQueryLabCase, response evidencequerymcp.GroundedEvidenceBriefResponse) {
	t.Helper()
	var gotIDs []string
	for _, match := range response.Matches {
		gotIDs = append(gotIDs, match.RecordRef.ID)
		if match.AdmissionOutcome != "pending" || match.CanonicalRef != nil {
			t.Error("query manufactured a non-pending disposition or canonical reference")
		}
		if match.SourceContext != nil && match.SourceContext.SearchParticipation {
			t.Error("source hydration participated in statement retrieval")
		}
	}
	if strings.HasPrefix(tc.Name, "english-baseline-") {
		got, want := slices.Clone(gotIDs), slices.Clone(tc.ExpectedIDs)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("English IDs = %v, want set %v", gotIDs, tc.ExpectedIDs)
		}
	} else if !slices.Equal(gotIDs, tc.ExpectedIDs) {
		t.Errorf("ordered IDs = %v, want %v", gotIDs, tc.ExpectedIDs)
	}
	execution := response.QueryExecution
	if execution.QueryCount != len(execution.Attempts) || execution.CandidateCount != len(response.Matches) || len(response.Matches) > tc.Request.Limit || response.Counts.ReturnedMatches != len(response.Matches) {
		t.Error("query counts, attempts, or returned limit are inconsistent")
	}
	if execution.GlobalAbsenceInferenceAllowed || execution.Truncated != tc.WantTruncated || response.Boundary.Truncated != tc.WantTruncated || execution.SearchCompleteWithinSurface == tc.WantTruncated {
		t.Error("query lost its bounded coverage or no-global-absence boundary")
	}
	if tc.Request.QueryMode == hanQueryLabMode {
		if execution.QueryMode != hanQueryLabMode || execution.PlanVersion != evidenceingestion.EvidenceQueryPlanExperimentalHanRecoveryV1 || execution.NormalizerVersion != evidenceingestion.EvidenceQueryNormalizerExperimentalHanV1 {
			t.Error("explicit experimental mode did not expose its versioned execution")
		}
	} else {
		mode, plan := evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, evidenceingestion.EvidenceQueryPlanRecoveryV2
		if tc.Request.QueryMode == evidenceingestion.EvidenceQueryModeExactLexical {
			mode, plan = evidenceingestion.EvidenceQueryModeExactLexical, evidenceingestion.EvidenceQueryPlanExactV1
		}
		if execution.QueryMode != mode || execution.PlanVersion != plan || execution.FallbackStatus != "" || execution.RankingPolicy != "" {
			t.Error("old/default modes changed plan or exposed experimental-only metadata")
		}
	}
	if tc.WantFallback {
		if len(execution.Attempts) == 0 || execution.Attempts[len(execution.Attempts)-1].Strategy != evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms || execution.CompletionReason != evidenceingestion.EvidenceQueryCompletionHanCandidates || execution.RankingPolicy != "created_at_desc,id_asc_unscored" || execution.FallbackStatus != "eligible" {
			t.Error("expected Han candidate result did not execute the Han fallback")
		}
		for _, match := range response.Matches {
			if match.Rank != 0 {
				t.Error("unscored Han fallback exposed a nonzero rank")
			}
		}
		if tc.WantTruncated && (len(execution.Attempts) == 0 || execution.Attempts[len(execution.Attempts)-1].CandidateCount < tc.Request.Limit+1 || !execution.Attempts[len(execution.Attempts)-1].Truncated) {
			t.Error("truncated fallback did not retain its extra-row witness")
		}
	}
	filters := execution.Filters
	scope := tc.Request.LifecycleScope
	if scope == "" {
		scope = "active"
		if tc.Request.SourceGenerationID != "" {
			scope = "all"
		}
	}
	if filters.SourceSnapshotID != tc.Request.SourceSnapshotID || filters.RepositorySnapshotID != tc.Request.RepositorySnapshotID || filters.SourceGenerationID != tc.Request.SourceGenerationID || filters.SourceID != tc.Request.SourceID || filters.SourceVersion != tc.Request.SourceVersion || filters.AdmissionOutcome != tc.Request.AdmissionOutcome || filters.LifecycleScope != scope || execution.Limit != tc.Request.Limit {
		t.Error("execution did not retain every exact filter and limit")
	}
}

func hanQueryLabOrderedIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ids []string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT proposal_occurrence_id, created_at FROM proposal_occurrences WHERE proposal_occurrence_id=ANY($1::text[])`, ids)
	if err != nil {
		t.Fatal("cannot read synthetic fixture ordering metadata")
	}
	defer rows.Close()
	type item struct {
		id      string
		created time.Time
	}
	var items []item
	for rows.Next() {
		var row item
		if err := rows.Scan(&row.id, &row.created); err != nil {
			t.Fatal("cannot decode synthetic fixture ordering metadata")
		}
		items = append(items, row)
	}
	if rows.Err() != nil || len(items) != len(ids) {
		t.Fatal("synthetic ordering fixture identities are incomplete")
	}
	slices.SortFunc(items, func(a, b item) int {
		if comparison := b.created.Compare(a.created); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.id, b.id)
	})
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.id)
	}
	return result
}

func hanQueryLabTools(t *testing.T, query *authorityProcess) json.RawMessage {
	t.Helper()
	response := query.request(t, "tools/list", map[string]any{})
	var listed struct {
		Tools []mcpstdio.Tool `json:"tools"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &listed) != nil || len(listed.Tools) != 13 {
		t.Fatal("compiled Query did not expose the expected tool catalog")
	}
	var names []string
	found := false
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Error("live Query tool did not advertise its read-only boundary")
		}
		if tool.Name != "get_grounded_evidence_brief" {
			continue
		}
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		mode, _ := properties["query_mode"].(map[string]any)
		values, _ := mode["enum"].([]any)
		for _, value := range values {
			found = found || value == hanQueryLabMode
		}
	}
	wantNames := []string{"get_evidence_record", "list_evidence_records", "search_evidence_records", "get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance", "get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path", "get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal", "get_canonical_supersession_head", "get_canonical_supersession_currentness"}
	slices.Sort(names)
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Error("live Query catalog changed the exact 13 read tools")
	}
	if !found {
		t.Fatal("live Query tool schema did not advertise the explicit experimental mode")
	}
	return slices.Clone(response.Result)
}

func hanQueryLabTableDigests(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) map[string]hanQueryLabTableState {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=$1 ORDER BY tablename`, schema)
	if err != nil {
		t.Fatal("cannot enumerate isolated domain tables")
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatal("cannot decode isolated domain table name")
		}
		tables = append(tables, table)
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal("cannot finish isolated domain table enumeration")
	}
	states := make(map[string]hanQueryLabTableState, len(tables))
	for _, table := range tables {
		rows, err := pool.Query(ctx, `SELECT to_jsonb(r)::text FROM `+pgx.Identifier{schema, table}.Sanitize()+` r ORDER BY to_jsonb(r)::text COLLATE "C"`)
		if err != nil {
			t.Fatal("cannot read isolated domain table digest input")
		}
		hash := sha256.New()
		count := 0
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal("cannot decode isolated domain row digest input")
			}
			_, _ = fmt.Fprintf(hash, "%d:%s\n", len(row), row)
			count++
		}
		rows.Close()
		if rows.Err() != nil {
			t.Fatal("cannot finish isolated domain row digest")
		}
		states[table] = hanQueryLabTableState{Rows: count, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	return states
}

func hanQueryLabPermanentTables(t *testing.T, ctx context.Context, conn *pgx.Conn) int {
	t.Helper()
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p') AND c.relpersistence <> 't' AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'`).Scan(&count); err != nil {
		t.Fatal("cannot count isolated lab permanent tables")
	}
	return count
}

func hanQueryLabCodeHashes(t *testing.T) map[string]string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot identify Han lab source root")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	paths := []string{"internal/mcpintegration/han_query_integration_test.go", "internal/mcpintegration/runtime_authority_integration_test.go", "internal/evidencequerymcp/server.go", "internal/evidenceingestion/query_execution.go", "internal/evidenceingestion/query_recovery.go", "internal/evidenceingestion/query_records.go", "internal/evidenceingestion/query_grounded_brief.go", "internal/mcpquery/backend.go", "internal/mcpquery/authorization.go", "internal/dbrole/runtime.go", "internal/dbrole/policy.go", "internal/dbrole/manifest.go", "cmd/ahe-query-mcp/main.go", "cmd/ahe-ingest-mcp/main.go", "go.mod", "go.sum"}
	// Include new Han implementation files without assuming their final name.
	for _, pattern := range []string{"internal/evidenceingestion/*han*.go", "migrations/*.up.sql"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal("cannot enumerate Han lab source hashes")
		}
		for _, match := range matches {
			relative, err := filepath.Rel(root, match)
			if err != nil {
				t.Fatal("cannot identify Han lab source path")
			}
			paths = append(paths, relative)
		}
	}
	hashes := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal("cannot hash Han lab implementation or migration source")
		}
		digest := sha256.Sum256(data)
		hashes[path] = hex.EncodeToString(digest[:])
	}
	return hashes
}

func hanQueryLabJSONHash(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot hash Han lab plan")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func hanQueryLabSaveReport(t *testing.T, report *hanQueryLabReport) {
	t.Helper()
	path := os.Getenv("AHE_HAN_MCP_LAB_REPORT")
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		t.Fatal("Han lab report path must be absolute")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively create private Han lab report")
	}
	// Registered first: emit the report after fixture cleanup and its check.
	t.Cleanup(func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.Passed = !t.Failed()
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			t.Error("cannot write private Han lab report")
		}
		if err := file.Close(); err != nil {
			t.Error("cannot close private Han lab report")
		}
	})
}
