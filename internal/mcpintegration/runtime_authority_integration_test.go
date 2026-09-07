//go:build integration

package mcpintegration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
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

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestionmcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This is a real compiled-command/stdio witness, not an in-process backend
// test. The dedicated acceptance database is the only permitted database; no
// DATABASE_DNS fallback or production launcher is used. It never admits data.
func TestIntegrationRuntimeAuthoritySubprocessPendingRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("AHE_DBROLE_ACCEPTANCE_DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	fixture := newAuthorityProcessFixture(t, ctx, databaseURL)
	intake := startAuthorityProcess(t, ctx, "ahe-ingest-mcp", fixture.intake, fixture.schema, "intake")
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", fixture.query, fixture.schema, "")
	intake.assertTools(t, []string{
		"submit_manual_evidence", "submit_text_source", "submit_external_source",
		"submit_extractor_output", "get_extractor_input",
	})
	query.assertTools(t, []string{
		"get_evidence_record", "list_evidence_records", "search_evidence_records",
		"get_grounded_evidence_brief", "list_evidence_neighbors", "get_relation_provenance",
		"get_mcp_read_source_states", "open_canonical_read_view", "find_canonical_path",
		"get_canonical_topology_diagnostics", "get_canonical_contradiction_proposal",
		"get_canonical_supersession_head", "get_canonical_supersession_currentness",
	})

	const statement = "Mock refunds must be completed within 7 days."
	sourceArgs := map[string]any{
		"request_id": "authority-source-1", "source_id": "mock:authority-refunds", "source_version": "1",
		"raw_text": statement, "origin_metadata": map[string]string{"fixture": "mock-only"},
	}
	source := authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", sourceArgs)
	if source.Replayed || source.SourceSnapshotID == "" || source.ExtractionViewID == "" || len(source.Spans) != 1 {
		t.Fatalf("source capture did not create one grounded view: %+v", source)
	}
	if source.RawContentHash != stdioContentHash([]byte(statement)) || source.RenderedContentHash != source.RawContentHash {
		t.Fatal("source hashes do not bind the exact mock bytes")
	}
	replaySource := authorityProcessTool[evidenceingestionmcp.SubmitTextSourceResponse](t, intake, "submit_text_source", sourceArgs)
	if !replaySource.Replayed {
		t.Fatal("exact source retry was not marked replayed")
	}
	replaySource.Replayed = false
	if !reflect.DeepEqual(source, replaySource) {
		t.Fatal("exact source retry changed immutable authority")
	}
	input := authorityProcessTool[evidenceingestionmcp.GetExtractorInputResponse](t, intake, "get_extractor_input", map[string]any{
		"extraction_view_id": source.ExtractionViewID,
	})
	if input.RenderedText != statement || input.SourceSnapshotID != source.SourceSnapshotID ||
		input.RawContentHash != source.RawContentHash || input.RenderedContentHash != source.RenderedContentHash ||
		len(input.Spans) != 1 || input.Spans[0].Text != statement || input.Spans[0].StartByte != 0 ||
		input.Spans[0].EndByte != len(statement) || input.Spans[0].QuotedTextHash != source.RawContentHash {
		t.Fatalf("extractor readback lost exact bytes, source identity, or span grounding: %+v", input)
	}
	outputArgs := map[string]any{
		"request_id": "authority-output-1", "source_snapshot_id": source.SourceSnapshotID,
		"extraction_view_id":   source.ExtractionViewID,
		"extractor_definition": map[string]any{"name": "mock-authority-extractor", "version": "1", "config": map[string]string{}},
		"extractor_output": map[string]any{"proposals": []any{map[string]any{
			"proposal_local_id": "claim-1", "statement_text": statement, "evidence_refs": []string{input.Spans[0].SpanID},
		}}},
	}
	proposal := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", outputArgs)
	if proposal.Status != "pending" || proposal.Replayed || proposal.ProposalCount != 1 ||
		proposal.ProposalOccurrenceID == "" || proposal.ExtractionAttemptID == "" || proposal.ProposalFingerprint == "" {
		t.Fatalf("source-to-proposal handoff was not pending-only: %+v", proposal)
	}
	replayProposal := authorityProcessTool[evidenceingestionmcp.SubmitExtractorOutputResponse](t, intake, "submit_extractor_output", outputArgs)
	if !replayProposal.Replayed {
		t.Fatal("exact output retry was not marked replayed")
	}
	replayProposal.Replayed = false
	if proposal != replayProposal {
		t.Fatal("exact output retry changed pending proposal identity")
	}
	record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]any{
		"proposal_occurrence_id": proposal.ProposalOccurrenceID,
	})
	if record.RecordRef.ID != proposal.ProposalOccurrenceID || record.RecordRef.Kind != "proposal" ||
		record.AdmissionOutcome != "pending" || record.CanonicalRef != nil || record.Canonical != nil ||
		record.StatementText != statement || record.Source.SourceSnapshotID != source.SourceSnapshotID ||
		record.Source.RawContentHash != source.RawContentHash || len(record.SourceRefs) != 1 ||
		record.SourceRefs[0].QuotedText != statement || record.SourceRefs[0].QuotedTextHash != source.RawContentHash {
		t.Fatalf("read-only Query lost pending lifecycle or exact provenance: %+v", record)
	}
	listed := authorityProcessTool[evidencequerymcp.ListEvidenceRecordsResponse](t, query, "list_evidence_records", map[string]any{
		"source_snapshot_id": source.SourceSnapshotID, "admission_outcome": "pending", "limit": 5,
	})
	if listed.Count != 1 || len(listed.Records) != 1 || listed.Records[0].RecordRef.ID != proposal.ProposalOccurrenceID {
		t.Fatal("bounded Query list did not return the one pending proposal")
	}

	// These arguments are deliberately plausible. Hidden-tool rejection must
	// occur before an admission, disposition, or repository writer is invoked.
	for _, tool := range []string{
		"admit_pending_proposal", "record_pending_proposal_disposition", "activate_repository_source_generation",
		"admit_pending_supersession", "submit_canonical_contradiction_proposal", "capture_git_repository_snapshot",
	} {
		intake.assertDenied(t, tool, map[string]any{
			"proposal_occurrence_id": proposal.ProposalOccurrenceID, "decision_by": "forged-reviewer",
			"decision_reason": "not a human approval", "outcome": "rejected", "request_id": "forbidden-writer",
		})
	}
	for _, tool := range []string{"submit_text_source", "submit_extractor_output", "admit_pending_proposal", "record_pending_proposal_disposition"} {
		query.assertDenied(t, tool, sourceArgs)
	}
	for _, login := range []authorityProcessLogin{fixture.query, fixture.intake} {
		assertAuthorityDatabaseWritesDenied(t, ctx, fixture.schema, login)
	}
	for _, table := range []string{
		"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions",
		"canonical_contradiction_proposals", "canonical_supersession_admission_events",
		"repository_generation_activation_requests", "repository_source_heads",
	} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 0)
	}
	for _, table := range []string{"source_snapshots", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
		stdioAssertTableCount(t, ctx, fixture.pool, table, 1)
	}
	var pending int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM proposal_occurrences WHERE admission_outcome = 'pending' AND canonical_ref IS NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Fatal("denied writer calls changed the one pending proposal")
	}
	// A final successful RPC is not a process-lifecycle witness. Require normal
	// EOF shutdown so late race reports and shutdown errors cannot be hidden by
	// the failure-only kill/join cleanup.
	query.finish(t)
	intake.finish(t)
}

type authorityProcessLogin struct {
	group   string
	profile dbrole.Profile
	dsn     string // Ephemeral test credential: never print or retain this value.
}

type authorityProcessFixture struct {
	schema   string
	pool     *pgxpool.Pool
	query    authorityProcessLogin
	intake   authorityProcessLogin
	reviewer authorityProcessLogin
}

func newAuthorityProcessFixture(t *testing.T, ctx context.Context, databaseURL string, extraProfiles ...dbrole.Profile) authorityProcessFixture {
	t.Helper()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("cannot open dedicated authority acceptance database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal("cannot identify acceptance database")
	}
	suffix := stdioRandomHex(t, 8)
	fixture := authorityProcessFixture{schema: "ahe_stdio_authority_" + suffix}
	schemaID := pgx.Identifier{fixture.schema}.Sanitize()
	var createdRoles []string
	schemaCreated := false
	// Processes and runtime pools register their cleanup later, so they are
	// killed/joined/closed before this schema and its exact generated roles.
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if schemaCreated {
			if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schemaID+" CASCADE"); err != nil {
				t.Error("cannot drop isolated authority test schema")
			}
		}
		for i := len(createdRoles) - 1; i >= 0; i-- {
			roleID := pgx.Identifier{createdRoles[i]}.Sanitize()
			if _, err := admin.Exec(cleanupCtx, "DROP OWNED BY "+roleID); err != nil {
				t.Error("cannot remove isolated authority role grants")
			}
			if _, err := admin.Exec(cleanupCtx, "DROP ROLE "+roleID); err != nil {
				t.Error("cannot drop isolated authority test role")
			}
		}
	})
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaID); err != nil {
		t.Fatal("cannot create isolated authority test schema")
	}
	schemaCreated = true
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid dedicated authority acceptance configuration")
	}
	config.ConnConfig.RuntimeParams["search_path"] = fixture.schema
	fixture.pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open isolated migration pool")
	}
	t.Cleanup(fixture.pool.Close)
	if _, err := migrations.ApplyUp(ctx, fixture.pool); err != nil {
		t.Fatalf("apply isolated shipping migrations: %v", err)
	}
	for _, profile := range append([]dbrole.Profile{dbrole.ProfileQuery, dbrole.ProfileIntake}, extraProfiles...) {
		label := strings.ReplaceAll(string(profile), "-", "_")
		group := "ahe_stdio_" + label + "_group_" + suffix
		login := "ahe_stdio_" + label + "_login_" + suffix
		password := stdioRandomHex(t, 24)
		for _, role := range []struct{ name, capability string }{{group, "NOLOGIN"}, {login, "LOGIN PASSWORD '" + password + "'"}} {
			if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role.name}.Sanitize()+" "+role.capability+
				" NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"); err != nil {
				t.Fatal("cannot create isolated bounded authority role")
			}
			createdRoles = append(createdRoles, role.name)
		}
		for _, statement := range []string{
			"GRANT " + pgx.Identifier{group}.Sanitize() + " TO " + pgx.Identifier{login}.Sanitize() + " WITH ADMIN FALSE, INHERIT FALSE, SET TRUE",
			"GRANT CONNECT ON DATABASE " + pgx.Identifier{database}.Sanitize() + " TO " + pgx.Identifier{login}.Sanitize(),
		} {
			if _, err := admin.Exec(ctx, statement); err != nil {
				t.Fatal("cannot install bounded authority membership or CONNECT")
			}
		}
		if _, err := dbrole.InstallPolicy(ctx, admin, dbrole.InstallInput{Role: group, Schema: fixture.schema, Profile: profile}); err != nil {
			t.Fatalf("install exact %s policy: %v", profile, err)
		}
		// Construct a fresh DSN rather than retaining the admin identity or any
		// admin runtime parameters. Passwords exist only in this process fixture.
		connectionURL := url.URL{Scheme: "postgres", User: url.UserPassword(login, password), Path: "/" + database}
		query := url.Values{"host": {config.ConnConfig.Host}, "port": {strconv.Itoa(int(config.ConnConfig.Port))},
			"sslmode": {"disable"}, "connect_timeout": {"3"}}
		connectionURL.RawQuery = query.Encode()
		binding := authorityProcessLogin{group: group, profile: profile, dsn: connectionURL.String()}
		if profile == dbrole.ProfileQuery {
			fixture.query = binding
		} else if profile == dbrole.ProfileIntake {
			fixture.intake = binding
		} else if profile == dbrole.ProfileSourceClaimReviewer {
			fixture.reviewer = binding
		}
	}
	return fixture
}

func assertAuthorityDatabaseWritesDenied(t *testing.T, ctx context.Context, schema string, login authorityProcessLogin) {
	t.Helper()
	config, err := pgxpool.ParseConfig(login.dsn)
	if err != nil {
		t.Fatal("cannot parse isolated runtime login")
	}
	pool, _, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: login.group, Schema: schema, Profile: login.profile})
	if err != nil {
		t.Fatalf("open %s ACL witness pool: %v", login.profile, err)
	}
	defer pool.Close()
	tables := []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "repository_generation_activation_requests"}
	if login.profile == dbrole.ProfileQuery {
		tables = append(tables, "source_snapshots", "proposal_occurrences")
	}
	for _, table := range tables {
		// A zero-row INSERT still checks ACLs, without writing canonical data
		// even if a regression accidentally broadens the grant.
		_, err := pool.Exec(ctx, "INSERT INTO "+pgx.Identifier{table}.Sanitize()+" SELECT * FROM "+pgx.Identifier{table}.Sanitize()+" WHERE false")
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "42501" {
			t.Fatalf("%s must deny INSERT on %s with 42501", login.profile, table)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE proposal_occurrences SET admission_outcome = 'rejected' WHERE false`)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("%s must deny proposal disposition UPDATE with 42501", login.profile)
	}
}

type authorityProcessResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}

type authorityProcess struct {
	ctx         context.Context
	input       *json.Encoder
	stdin       io.WriteCloser
	responses   <-chan []byte
	processDone chan struct{}
	scannerDone chan struct{}
	waitErr     error // Read only after processDone closes.
	scanErr     error // Read only after scannerDone closes.
	raceOutput  *authorityRaceOutput
	nextID      int
}

// authorityRaceOutput retains only a bounded pattern-matcher state, never
// stderr bytes or credentials. cmd.Wait joins the writer before it is read.
type authorityRaceOutput struct {
	matched  int
	detected bool
}

func (w *authorityRaceOutput) Write(data []byte) (int, error) {
	const warning = "WARNING: DATA RACE"
	for _, character := range data {
		if character == warning[w.matched] {
			w.matched++
			if w.matched == len(warning) {
				w.detected = true
				w.matched = 0
			}
		} else if character == warning[0] {
			w.matched = 1
		} else {
			w.matched = 0
		}
	}
	return len(data), nil
}

func startAuthorityProcess(t *testing.T, ctx context.Context, command string, login authorityProcessLogin, schema, profile string) *authorityProcess {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate command build root")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	binary := filepath.Join(t.TempDir(), command)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/"+command)
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build authority command %s: %v\n%s", command, err, output)
	}
	processCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	cmd := exec.CommandContext(processCtx, binary)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_DNS=" + login.dsn,
		"AHE_RUNTIME_PRINCIPAL_ID=mock:" + command, "AHE_DATABASE_ROLE=" + login.group, "AHE_DATABASE_SCHEMA=" + schema,
		"GORACE=halt_on_error=1"}
	if profile != "" {
		cmd.Env = append(cmd.Env, "AHE_RUNTIME_PROFILE="+profile)
	}
	// Never emit child stderr: startup errors can contain connection material.
	// Retain just a race-warning boolean, including reports during shutdown.
	raceOutput := &authorityRaceOutput{}
	cmd.Stderr = raceOutput
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal("cannot open child stdin")
	}
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = reader.Close()
		_ = writer.Close()
		t.Fatalf("cannot start authority command %s", command)
	}
	responses := make(chan []byte, 1)
	process := &authorityProcess{ctx: processCtx, input: json.NewEncoder(stdin), stdin: stdin,
		responses: responses, processDone: make(chan struct{}), scannerDone: make(chan struct{}), raceOutput: raceOutput}
	go func() {
		process.waitErr = cmd.Wait()
		_ = writer.Close()
		close(process.processDone)
	}()
	go func() {
		defer func() {
			close(responses)
			close(process.scannerDone)
		}()
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
				t.Error("authority child did not join before database cleanup")
			}
		}
		select {
		case <-process.scannerDone:
		case <-time.After(5 * time.Second):
			t.Error("authority stdout reader did not join before database cleanup")
		}
	})
	initialized := process.request(t, "initialize", map[string]any{"protocolVersion": mcpstdio.ProtocolVersion,
		"capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mock-authority-test", "version": "1"}})
	var initialization struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if initialized.Error != nil || json.Unmarshal(initialized.Result, &initialization) != nil ||
		initialization.ProtocolVersion != mcpstdio.ProtocolVersion || initialization.ServerInfo.Name != command {
		t.Fatal("compiled authority command did not initialize its expected protocol")
	}
	return process
}

func (p *authorityProcess) finish(t *testing.T) {
	t.Helper()
	if err := p.stdin.Close(); err != nil {
		t.Fatal("cannot close authority subprocess stdin for normal shutdown")
	}
	select {
	case <-p.processDone:
	case <-time.After(5 * time.Second):
		t.Fatal("authority subprocess did not finish after stdin EOF")
	}
	select {
	case <-p.scannerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("authority subprocess stdout reader did not finish")
	}
	if p.waitErr != nil || p.scanErr != nil {
		t.Fatal("authority subprocess did not finish normally with exit 0 and complete stdout")
	}
	if p.raceOutput.detected {
		t.Fatal("authority subprocess reported a data race")
	}
}

func (p *authorityProcess) request(t *testing.T, method string, params any) authorityProcessResponse {
	t.Helper()
	p.nextID++
	if err := p.input.Encode(map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": method, "params": params}); err != nil {
		t.Fatal("cannot write authority subprocess request")
	}
	select {
	case data, ok := <-p.responses:
		var response authorityProcessResponse
		if !ok || json.Unmarshal(data, &response) != nil || response.JSONRPC != "2.0" || response.ID != p.nextID {
			t.Fatal("authority subprocess exited or returned an invalid response (stderr intentionally suppressed)")
		}
		return response
	case <-p.ctx.Done():
		t.Fatal("authority subprocess response exceeded its deadline")
		return authorityProcessResponse{}
	}
}

func (p *authorityProcess) assertTools(t *testing.T, expected []string) {
	t.Helper()
	response := p.request(t, "tools/list", map[string]any{})
	var listed struct {
		Tools []mcpstdio.Tool `json:"tools"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &listed) != nil {
		t.Fatal("authority subprocess cannot list tools")
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	expected = slices.Clone(expected)
	slices.Sort(expected)
	if !slices.Equal(names, expected) {
		t.Fatalf("authority tools = %v, want exact %v", names, expected)
	}
}

func (p *authorityProcess) assertDenied(t *testing.T, tool string, arguments any) {
	t.Helper()
	response := p.request(t, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	if response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("hidden tool %s was not rejected before dispatch", tool)
	}
}

func authorityProcessTool[T any](t *testing.T, process *authorityProcess, tool string, arguments any) T {
	t.Helper()
	response := process.request(t, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	var envelope struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if response.Error != nil || json.Unmarshal(response.Result, &envelope) != nil || envelope.IsError {
		t.Fatalf("authority tool %s failed (payload intentionally suppressed)", tool)
	}
	var result T
	if err := json.Unmarshal(envelope.StructuredContent, &result); err != nil {
		t.Fatalf("authority tool %s returned an invalid typed result", tool)
	}
	return result
}
