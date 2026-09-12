//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const multisurfaceLabMode = "experimental_multisurface_lexical_v1"
const multisurfaceLabSchema = "grounded-evidence-brief-v6"
const multisurfaceOriginalPlanSHA = "96593e33c47df10bab6645c0f89129354f162f962618418430a1947ded1313a6"

type multisurfaceLabModePlan struct {
	Name           string `json:"name"`
	QueryMode      string `json:"query_mode"`
	ResponseSchema string `json:"response_schema"`
}

// The relevance cases are copied unchanged from the previously frozen plan.
// Their labels are observations, never assertions of semantic correctness.
type multisurfaceLabPlan struct {
	hanOutcomePlan
	FrozenSourcePlan           hanOutcomeFile            `json:"frozen_source_plan"`
	Modes                      []multisurfaceLabModePlan `json:"modes"`
	PriorBaselineExpectedPairs int                       `json:"prior_baseline_expected_pairs"`
	TotalExpectedPairs         int                       `json:"total_expected_pairs"`
	PlannedBriefCalls          int                       `json:"planned_brief_calls"`
	LabelsUnchanged            bool                      `json:"query_text_and_relevance_labels_unchanged"`
	LabelAuthority             string                    `json:"label_authority"`
	Limitations                []string                  `json:"limitations"`
}

type multisurfaceLabReport struct {
	hanOutcomeReport
	FrozenPlan    multisurfaceLabPlan
	PracticalPlan *practicalLabPlan          `json:",omitempty"`
	AnchorPlan    *practicalAnchorLabPlan    `json:",omitempty"`
	RawResponses  map[string]json.RawMessage `json:",omitempty"`
}

func multisurfaceLabConfig(socket, port, user string) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(name, "PG") || name == "DATABASE_DNS" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DNS") {
			return nil, fmt.Errorf("multisurface lab refuses inherited database environment")
		}
	}
	if port != "55445" || filepath.Dir(socket) != "/private/tmp" || !strings.HasPrefix(filepath.Base(socket), "ahe-multisurface.") || filepath.Clean(socket) != socket || strings.ContainsAny(socket, "\x00\r\n") || (user != "ahe_brief_operator" && user != "brief_query_login") {
		return nil, fmt.Errorf("multisurface lab requires its dedicated private socket, port, and login")
	}
	values := url.Values{"host": {socket}, "port": {port}, "sslmode": {"disable"}, "passfile": {"/dev/null"}, "connect_timeout": {"5"}, "target_session_attrs": {"any"}, "statement_timeout": {"2000"}, "default_transaction_read_only": {"on"}, "timezone": {"Asia/Taipei"}, "search_path": {"ahe_brief,pg_catalog"}}
	u := url.URL{Scheme: "postgres", User: url.User(user), Path: "/ahe_brief_lab", RawQuery: values.Encode()}
	config, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot parse dedicated multisurface settings")
	}
	config.Password, config.Fallbacks = "", nil
	return config, nil
}

func multisurfaceDecodePlan(data, original []byte) (multisurfaceLabPlan, error) {
	var plan multisurfaceLabPlan
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&plan); err != nil {
		return plan, fmt.Errorf("invalid multisurface plan JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return plan, fmt.Errorf("plan must contain one object")
	}
	if plan.FrozenSourcePlan.SHA256 != multisurfaceOriginalPlanSHA || hanOutcomeHash(original) != multisurfaceOriginalPlanSHA || !filepath.IsAbs(plan.FrozenSourcePlan.Path) {
		return plan, fmt.Errorf("original plan hash is not the frozen baseline")
	}
	prior, err := hanOutcomeDecodePlan(original)
	if err != nil {
		return plan, err
	}
	prior.Cases = slices.DeleteFunc(prior.Cases, func(c hanOutcomeCase) bool { return c.Kind != "relevance" })
	prior.Contract = "multisurface-read-only-plan-v1"
	wantModes := []multisurfaceLabModePlan{{"baseline", "", evidencequerymcp.GroundedEvidenceBriefSchemaV3}, {"han", hanQueryLabMode, evidencequerymcp.GroundedEvidenceBriefSchemaV3}, {"multisurface", multisurfaceLabMode, multisurfaceLabSchema}}
	if !reflect.DeepEqual(plan.hanOutcomePlan, prior) || !reflect.DeepEqual(plan.Modes, wantModes) || len(plan.Cases) != 12 || plan.Replays != 2 || plan.PriorBaselineExpectedPairs != 8 || plan.TotalExpectedPairs != 18 || plan.PlannedBriefCalls != 72 || !plan.LabelsUnchanged || plan.LabelAuthority == "" || len(plan.Limitations) == 0 {
		return plan, fmt.Errorf("frozen questions, labels, or bounded mode matrix changed")
	}
	return plan, nil
}

func multisurfaceCodeHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := hanOutcomeCodeHashes(t)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate multisurface runner")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	for _, pattern := range []string{"internal/mcpintegration/multisurface*_test.go", "internal/mcpintegration/practical*_test.go", "internal/evidenceingestion/query_*.go", "internal/evidenceingestion/source_view*.go", "internal/evidencequerymcp/*.go", "internal/mcpquery/*.go", "internal/mcpstdio/*.go"} {
		paths, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal("cannot enumerate experiment implementation")
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("cannot hash experiment implementation")
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatal("cannot relativize implementation path")
			}
			hashes[relative] = hanOutcomeHash(data)
		}
	}
	return hashes
}

func multisurfaceLabTools(t *testing.T, query *authorityProcess) json.RawMessage {
	t.Helper()
	raw := hanQueryLabTools(t, query)
	var catalog struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if json.Unmarshal(raw, &catalog) != nil {
		t.Fatal("cannot inspect live multisurface tool schema")
	}
	for _, tool := range catalog.Tools {
		if tool.Name == "get_grounded_evidence_brief" {
			if !slices.Contains(tool.InputSchema.Properties["query_mode"].Enum, multisurfaceLabMode) || !slices.Contains(tool.InputSchema.Properties["response_schema"].Enum, multisurfaceLabSchema) {
				t.Fatal("live schema lacks explicit multisurface mode/v6")
			}
			return raw
		}
	}
	t.Fatal("grounded brief missing from live schema")
	return nil
}

type multisurfaceSavedView struct {
	ID       string                                         `json:"extraction_view_id"`
	SourceID string                                         `json:"source_snapshot_id"`
	Hash     string                                         `json:"rendered_content_hash"`
	Hex      string                                         `json:"rendered_content_hex"`
	Text     string                                         `json:"rendered_content_utf8"`
	Spans    map[string]evidenceingestion.ResolvedSourceRef `json:"-"`
}

func multisurfaceSavedViews(t *testing.T, snapshot map[string]json.RawMessage) map[string]multisurfaceSavedView {
	t.Helper()
	var views []multisurfaceSavedView
	if json.Unmarshal(snapshot["extraction_views"], &views) != nil || len(views) != 4 {
		t.Fatal("four saved identity views required")
	}
	result := map[string]multisurfaceSavedView{}
	for _, view := range views {
		data, err := hex.DecodeString(view.Hex)
		if err != nil || string(data) != view.Text || "sha256:"+hanOutcomeHash(data) != view.Hash {
			t.Fatal("saved identity-view bytes/hash mismatch")
		}
		view.Spans = map[string]evidenceingestion.ResolvedSourceRef{}
		result[view.ID] = view
	}
	if len(result) != 4 {
		t.Fatal("source views were incorrectly collapsed")
	}
	var spans []struct {
		evidenceingestion.ResolvedSourceRef
		Text string `json:"quoted_text_utf8"`
		Hex  string `json:"quoted_text_hex"`
	}
	if json.Unmarshal(snapshot["span_catalog_entries"], &spans) != nil || len(spans) == 0 {
		t.Fatal("saved span catalog missing")
	}
	for _, row := range spans {
		view, ok := result[row.ExtractionViewID]
		data, err := hex.DecodeString(row.Hex)
		if !ok || err != nil || string(data) != row.Text || row.StartByte < 0 || row.EndByte <= row.StartByte || row.EndByte > len(view.Text) || view.Text[row.StartByte:row.EndByte] != row.Text || "sha256:"+hanOutcomeHash(data) != row.QuotedTextHash {
			t.Fatal("saved span catalog differs from exact source view")
		}
		row.ResolvedSourceRef.QuotedText = row.Text
		if _, duplicate := view.Spans[row.SpanID]; duplicate {
			t.Fatal("saved span catalog repeats an ID within its view")
		}
		view.Spans[row.SpanID] = row.ResolvedSourceRef
	}
	return result
}

func TestIntegrationMultisurfaceReadOnlyLab(t *testing.T) {
	runMultisurfaceReadOnlyLab(t, false, false)
}

func runMultisurfaceReadOnlyLab(t *testing.T, practical, anchors bool) {
	socket, port := os.Getenv("AHE_MULTISURFACE_LAB_SOCKET"), os.Getenv("AHE_MULTISURFACE_LAB_PORT")
	if !practical && socket == "" && port == "" && os.Getenv("AHE_MULTISURFACE_LAB_SYSTEM_IDENTIFIER") == "" && os.Getenv("AHE_MULTISURFACE_LAB_REPORT") == "" && os.Getenv("AHE_MULTISURFACE_LAB_PLAN") == "" {
		t.Skip("set the five dedicated AHE_MULTISURFACE_LAB settings for the restored read-only lab")
	}
	report := multisurfaceLabReport{hanOutcomeReport: hanOutcomeReport{Contract: "multisurface-read-only-v1", Phase: "configuration", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{
		"Native restored synthetic T1-T4 records already carry human decisions; this run makes no new admission or disposition.",
		"Engineering ID assertions and agent-labeled relevance observations are separate; a relevance miss or extra is not a test failure.",
		"Returned candidates, ranks, and historical admission are not new source-support judgments or general recall/semantic evidence.",
		"Sequential single-process replay is not a performance benchmark; each request has 5s, each SQL statement 2s, the child 60s total.",
		"No schema/role cleanup is attempted: the restored database is preserved; unchanged row-content digests and normal EOF are separate witnesses.",
	}}}
	reportPath := os.Getenv("AHE_MULTISURFACE_LAB_REPORT")
	rootPrefix := "multisurface-lab."
	if anchors {
		rootPrefix = "practical-han-lab."
	}
	if !multisurfaceReportCoordinate(reportPath, multisurfaceLabBin(t), rootPrefix) {
		t.Fatal("outcome lab requires an absolute private report path")
	}
	for _, directory := range []string{socket, filepath.Dir(reportPath)} {
		info, err := os.Lstat(directory)
		canonical, canonicalErr := filepath.EvalSymlinks(directory)
		if err != nil || canonicalErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || canonical != directory {
			t.Fatal("multisurface lab requires canonical private directories")
		}
	}
	file, err := os.OpenFile(reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("cannot reserve private outcome report")
	}
	t.Cleanup(func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.NotRun = report.Planned - report.Attempted
		report.Passed = !t.Failed() && report.Completed == report.Planned && report.Planned > 0 && report.QueryEOF && report.Unchanged
		if t.Failed() {
			report.Failures = append(report.Failures, "stopped without retry in phase: "+report.Phase)
		}
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			t.Error("cannot encode outcome report")
		}
		if err := file.Close(); err != nil {
			t.Error("cannot close outcome report")
		}
	})
	config, err := multisurfaceLabConfig(socket, port, "ahe_brief_operator")
	if err != nil {
		t.Fatal(err)
	}
	identity := os.Getenv("AHE_MULTISURFACE_LAB_SYSTEM_IDENTIFIER")
	if n, err := strconv.ParseUint(identity, 10, 64); err != nil || n == 0 || slices.Contains([]string{"7683988729274717512", "7684171091801649800"}, identity) {
		t.Fatal("outcome lab requires a pinned PostgreSQL system identifier")
	}
	planPath := os.Getenv("AHE_MULTISURFACE_LAB_PLAN")
	if !filepath.IsAbs(planPath) {
		t.Fatal("outcome plan path must be absolute")
	}
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal("cannot read frozen outcome plan")
	}
	originalData, originalErr := os.ReadFile(multisurfaceOriginalPlanPath(t))
	if originalErr != nil {
		t.Fatal("cannot read frozen original question plan")
	}
	report.FrozenPlan, err = multisurfaceDecodePlan(planData, originalData)
	report.Plan = report.FrozenPlan.hanOutcomePlan
	if err != nil {
		t.Fatal(err)
	}
	report.PlanSHA256 = hanOutcomeHash(planData)
	modes := slices.Clone(report.FrozenPlan.Modes)
	var frozenResponses map[string]evidencequerymcp.GroundedEvidenceBriefResponse
	var practicalFileHashes map[string]string
	practicalPlanVersion := "practical-multisurface-lexical-v1"
	if practical {
		if anchors {
			report.AnchorPlan, frozenResponses, practicalFileHashes = practicalAnchorReadPlan(t, planData)
			modes = append(modes, report.AnchorPlan.Mode)
			report.Contract, practicalPlanVersion = "practical-han-anchor-read-only-v1", "practical-multisurface-lexical-v2"
		} else {
			report.PracticalPlan, frozenResponses, practicalFileHashes = practicalReadPlan(t, planData)
			modes = append(modes, report.PracticalPlan.Mode)
			report.Contract = "practical-multisurface-read-only-v1"
		}
		report.RawResponses = map[string]json.RawMessage{}
	}
	report.Planned = len(report.Plan.Cases) * len(modes) * report.Plan.Replays
	report.UniqueCaseModes = len(report.Plan.Cases) * len(modes)
	snapshotData, err := os.ReadFile(report.Plan.Snapshot.Path)
	if err != nil || hanOutcomeHash(snapshotData) != hanOutcomeSnapshotSHA {
		t.Fatal("native T4 saved snapshot hash mismatch")
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal(snapshotData, &snapshot) != nil {
		t.Fatal("cannot decode pinned native snapshot")
	}
	report.CodeBefore = multisurfaceCodeHashes(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	report.Phase = "database_preflight"
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to the pinned restored outcome lab")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var listen, sockets, user, schema, readOnly, dataDirectory string
	var extensions, publicAuthority int
	if err := admin.QueryRow(ctx, `SELECT current_setting('data_directory'), current_database(), current_user, current_schema(), current_setting('listen_addresses'), current_setting('unix_socket_directories'), current_setting('server_version_num')::integer, current_setting('default_transaction_read_only'), (SELECT system_identifier::text FROM pg_control_system()), (SELECT count(*) FROM pg_extension WHERE extname <> 'plpgsql'), (SELECT count(*) FROM pg_database d, LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname=current_database() AND a.grantee=0 AND a.privilege_type IN ('CREATE','TEMPORARY'))`).Scan(&dataDirectory, &report.Database, &user, &schema, &listen, &sockets, &report.ServerVersion, &readOnly, &report.SystemIdentifier, &extensions, &publicAuthority); err != nil {
		t.Fatal("cannot inspect restored outcome database identity")
	}
	report.SocketOnly = listen == "" && sockets == socket
	wantDataDirectory := filepath.Join(filepath.Dir(reportPath), "pgdata")
	if anchors {
		wantDataDirectory = practicalAnchorPGData(t)
	}
	if dataDirectory != wantDataDirectory {
		t.Fatal("multisurface lab PGDATA is outside its isolated root")
	}
	if report.Database != "ahe_brief_lab" || user != "ahe_brief_operator" || schema != "ahe_brief" || !report.SocketOnly || report.ServerVersion/10000 != 18 || report.SystemIdentifier != identity || readOnly != "on" || extensions != 0 || publicAuthority != 0 {
		t.Fatal("restored outcome identity or authority preflight mismatch")
	}
	poolConfig, err := pgxpool.ParseConfig(config.ConnString())
	if err != nil {
		t.Fatal("cannot configure read-only observer")
	}
	poolConfig.MaxConns = 1
	observer, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal("cannot open read-only observer")
	}
	t.Cleanup(observer.Close)
	report.Phase = "snapshot_readback"
	hanOutcomeCompareSnapshot(t, ctx, observer, snapshot, report.Plan)
	report.SnapshotVerified = true
	report.Before = hanQueryLabTableDigests(t, ctx, observer, "ahe_brief")
	if len(report.Before) != 78 {
		t.Fatal("restored outcome schema does not have the frozen 78-table inventory")
	}
	for table, count := range map[string]int{"source_snapshots": 4, "proposal_occurrences": 4, "admission_decisions": 4, "canonical_graph_nodes": 6, "canonical_graph_edges": 4} {
		if report.Before[table].Rows != count {
			t.Fatal("restored native lifecycle counts mismatch")
		}
	}
	t.Cleanup(func() {
		phase := report.Phase
		report.Phase = "after_readback"
		auditCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		report.After = hanQueryLabTableDigests(t, auditCtx, observer, "ahe_brief")
		report.CodeAfter = multisurfaceCodeHashes(t)
		currentPlan, planErr := os.ReadFile(planPath)
		currentSnapshot, snapshotErr := os.ReadFile(report.Plan.Snapshot.Path)
		currentOriginal, originalErr := os.ReadFile(report.FrozenPlan.FrozenSourcePlan.Path)
		if !reflect.DeepEqual(report.Before, report.After) || !reflect.DeepEqual(report.CodeBefore, report.CodeAfter) || planErr != nil || snapshotErr != nil || originalErr != nil || hanOutcomeHash(currentOriginal) != multisurfaceOriginalPlanSHA || hanOutcomeHash(currentPlan) != report.PlanSHA256 || hanOutcomeHash(currentSnapshot) != hanOutcomeSnapshotSHA {
			t.Fatal("outcome data, source, plan, or code changed during read-only queries")
		}
		practicalCheckFiles(t, practicalFileHashes)
		report.Unchanged = true
		report.Phase = phase
	})
	report.Phase = "query_policy"
	queryConfig, err := multisurfaceLabConfig(socket, port, "brief_query_login")
	if err != nil {
		t.Fatal(err)
	}
	policyConfig := queryConfig.Copy()
	policyConfig.RuntimeParams["role"] = "brief_query_group"
	policyConn, err := pgx.ConnectConfig(ctx, policyConfig)
	if err != nil {
		t.Fatal("cannot open bounded query policy verification connection")
	}
	report.Policy, err = dbrole.VerifyRuntimeConnection(ctx, policyConn, dbrole.RuntimeVerificationInput{Role: "brief_query_group", SessionUser: "brief_query_login", Schema: "ahe_brief", Profile: dbrole.ProfileQuery})
	closeErr := policyConn.Close(ctx)
	if err != nil || closeErr != nil {
		t.Fatal("restored query policy failed read-only verification")
	}
	report.Phase = "query_start"
	query := startAuthorityProcess(t, ctx, "ahe-query-mcp", authorityProcessLogin{group: "brief_query_group", profile: dbrole.ProfileQuery, dsn: queryConfig.ConnString()}, "ahe_brief", "")
	report.LiveQueryTools = multisurfaceLabTools(t, query)
	if practical {
		practicalCheckCatalog(t, report.LiveQueryTools)
	}
	report.CatalogCalls++
	if t.Failed() {
		t.Fatal("query catalog gate failed")
	}
	var proposals []hanOutcomeProposal
	if json.Unmarshal(snapshot["proposals"], &proposals) != nil {
		t.Fatal("cannot decode saved proposal authority")
	}
	wants := map[string]hanOutcomeProposal{}
	savedSources := hanOutcomeSavedSources(t, snapshot)
	report.Phase = "record_readback"
	for _, proposal := range proposals {
		wants[proposal.ID] = proposal
		report.RecordReadbackCalls++
		record := authorityProcessTool[evidencequerymcp.GetEvidenceRecordResponse](t, query, "get_evidence_record", map[string]string{"proposal_occurrence_id": proposal.ID})
		report.Records = append(report.Records, record)
		if record.RecordRef.ID != proposal.ID || record.StatementText != proposal.Statement || record.AdmissionOutcome != proposal.Outcome || !reflect.DeepEqual(record.CanonicalRef, proposal.Canonical) || !reflect.DeepEqual(record.SourceRefs, proposal.Refs) || !reflect.DeepEqual(record.Source, savedSources[proposal.ID]) {
			t.Fatal("native proposal MCP readback differs from saved authority")
		}
	}
	views := multisurfaceSavedViews(t, snapshot)
	var savedCreated []struct {
		ID      string    `json:"proposal_occurrence_id"`
		Created time.Time `json:"created_at"`
	}
	if json.Unmarshal(snapshot["proposals"], &savedCreated) != nil {
		t.Fatal("cannot decode frozen proposal creation order")
	}
	created := map[string]time.Time{}
	for _, row := range savedCreated {
		created[row.ID] = row.Created
	}
	first := map[string]evidencequerymcp.GroundedEvidenceBriefResponse{}
	for replay := range report.Plan.Replays {
		for _, tc := range report.Plan.Cases {
			for _, modePlan := range modes {
				mode, name := modePlan.QueryMode, modePlan.Name
				report.Phase = "query/" + tc.ID + "/" + name
				if !t.Run(fmt.Sprintf("%s/%s/replay-%d", tc.ID, name, replay), func(t *testing.T) {
					if mode == multisurfaceLabMode || mode == practicalLabMode {
						multisurfaceObserve(t, query, tc, replay, modePlan, practicalPlanVersion, wants, views, created, first, &report.hanOutcomeReport)
					} else {
						hanOutcomeObserve(t, query, tc, mode, name, replay, wants, report.Records, first, &report.hanOutcomeReport)
					}
					if practical {
						observation := report.Observations[len(report.Observations)-1]
						if anchors {
							practicalAnchorCheckObservation(t, observation, tc, frozenResponses)
						} else {
							practicalCheckObservation(t, observation, tc, frozenResponses)
						}
						practicalCheckRawResponse(t, observation, query.lastToolContent, frozenResponses, report.RawResponses)
					}
				}) {
					t.Fatal("outcome query failed; remaining requests not run")
				}
			}
		}
	}
	report.Phase = "normal_eof"
	query.finish(t)
	report.QueryEOF = true
	report.Phase = "completed"
}

func multisurfaceObserve(t *testing.T, query *authorityProcess, tc hanOutcomeCase, replay int, mode multisurfaceLabModePlan, practicalPlanVersion string, wants map[string]hanOutcomeProposal, views map[string]multisurfaceSavedView, created map[string]time.Time, first map[string]evidencequerymcp.GroundedEvidenceBriefResponse, report *hanOutcomeReport) {
	t.Helper()
	o := hanOutcomeObservation{CaseID: tc.ID, Kind: tc.Kind, Mode: mode.Name, Replay: replay, Stage: "request", Request: tc.Request, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	o.Request.QueryMode, o.Request.ResponseSchema = mode.QueryMode, mode.ResponseSchema
	report.Attempted++
	start := time.Now()
	defer func() {
		o.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if o.ElapsedNanoseconds == 0 {
			o.ElapsedNanoseconds = time.Since(start).Nanoseconds()
		}
		report.Observations = append(report.Observations, o)
	}()
	originalContext := query.ctx
	requestContext, cancel := context.WithTimeout(originalContext, 5*time.Second)
	query.ctx = requestContext
	defer func() { query.ctx = originalContext; cancel() }()
	r := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, query, "get_grounded_evidence_brief", o.Request)
	o.Response, o.ElapsedNanoseconds, o.Stage = r, time.Since(start).Nanoseconds(), "authority_and_contract"
	e := r.QueryExecution
	baseline, exists := first[tc.ID+"/baseline"]
	if !exists || e.Multisurface == nil || !reflect.DeepEqual(e.Multisurface.Baseline, baseline.QueryExecution) {
		t.Fatal("expanded baseline trace differs from actual default request")
	}
	meta := e.Multisurface
	planVersion, extraQueries := evidenceingestion.EvidenceQueryPlanExperimentalMultisurfaceV1, 1
	if mode.QueryMode == practicalLabMode {
		planVersion = practicalPlanVersion
		extraQueries += practicalFallbackCount(t, r)
	}
	fields := []string{evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement, "extraction_views.rendered_content"}
	if r.SchemaVersion != mode.ResponseSchema || r.Query != tc.Request.Query || e.OriginalQuery != r.Query || e.QueryMode != mode.QueryMode || e.PlanVersion != planVersion || e.NormalizerVersion != evidenceingestion.EvidenceQueryNormalizerExperimentalMultisurfaceV1 || e.SearchSurface != strings.Join(fields, "+") || !slices.Equal(e.SearchedFields, fields) || !slices.Equal(e.SearchedRecordKinds, []string{"proposal_occurrence"}) || e.Filters != baseline.QueryExecution.Filters || e.Limit != tc.Request.Limit || e.GlobalAbsenceInferenceAllowed || e.RankingPolicy != "matched_term_count_desc,created_at_desc,id_asc_not_confidence" || e.FallbackStatus != "" || e.CandidateCount != len(r.Matches) || r.Counts.ReturnedMatches != len(r.Matches) || e.QueryCount != len(e.Attempts) || e.QueryCount != baseline.QueryExecution.QueryCount+extraQueries || len(r.Matches) > e.Limit || e.SearchCompleteWithinSurface != (!e.Truncated && !meta.Baseline.Truncated && meta.ExcludedSourceViews == 0) || r.Boundary != (evidencequerymcp.GroundedEvidenceBriefBoundary{SearchSurface: e.SearchSurface, QueryMode: e.QueryMode, LifecycleScope: e.Filters.LifecycleScope, Limit: e.Limit, Truncated: e.Truncated}) {
		t.Fatal("multisurface execution does not preserve the frozen request/boundary")
	}
	// These four pre-existing manual identity views are all small and eligible.
	// Two contain identical bytes but remain distinct views, not corroboration.
	if meta.SearchedSourceViews != len(views) || meta.ExcludedSourceViews != 0 || !slices.IsSorted(meta.NormalizedQueryTerms) {
		t.Fatal("distinct source-view inventory or normalized term ordering mismatch")
	}
	seenTerms := map[string]bool{}
	for _, term := range meta.NormalizedQueryTerms {
		if term == "" || seenTerms[term] {
			t.Fatal("empty or duplicated normalized term")
		}
		seenTerms[term] = true
	}
	baselineIDs := []string{}
	for _, m := range baseline.Matches {
		baselineIDs = append(baselineIDs, m.RecordRef.ID)
	}
	counts := evidencequerymcp.GroundedEvidenceBriefAdmissionCounts{}
	for index, match := range r.Matches {
		want, ok := wants[match.RecordRef.ID]
		var canonical *string
		if match.CanonicalRef != nil {
			canonical = &match.CanonicalRef.ID
			if match.CanonicalRef.Kind != "canonical_evidence" {
				t.Fatal("canonical reference kind changed")
			}
		}
		if !ok || match.RecordRef.Kind != hanOutcomeProposalReferenceKind || match.ProposalKind != "statement" || match.StatementText != want.Statement || match.AdmissionOutcome != want.Outcome || !reflect.DeepEqual(canonical, want.Canonical) || !reflect.DeepEqual(match.SourceRefs, want.Refs) {
			t.Fatal("retrieval changed saved statement, disposition, canonical reference, or original citations")
		}
		if match.SourceContext != nil && match.SourceContext.SearchParticipation {
			t.Fatal("post-retrieval compiler context was incorrectly marked as the lexical search surface")
		}
		var record *evidencequerymcp.GetEvidenceRecordResponse
		for i := range report.Records {
			if report.Records[i].RecordRef.ID == match.RecordRef.ID {
				record = &report.Records[i]
				break
			}
		}
		if record == nil || match.SourceScopeRef.Kind != "source_snapshot" || match.SourceScopeRef.ID != record.Source.SourceSnapshotID {
			t.Fatal("source scope changed")
		}
		foundScope := false
		for _, scope := range r.SourceScopes {
			if scope.ScopeRef == match.SourceScopeRef && reflect.DeepEqual(scope.Source, record.Source) {
				foundScope = true
			}
		}
		if !foundScope {
			t.Fatal("retrieved source package differs from exact record")
		}
		basis := match.RetrievalBasis
		if basis == nil || !slices.Equal(basis.NormalizedQueryTerms, meta.NormalizedQueryTerms) || basis.BaselineMatched != slices.Contains(baselineIDs, match.RecordRef.ID) || len(basis.Surfaces) != 2 {
			t.Fatal("missing or inconsistent retrieval basis")
		}
		union := map[string]bool{}
		for surfaceIndex, surface := range basis.Surfaces {
			if surface.Surface != fields[surfaceIndex] || surface.Status != "searched" {
				t.Fatal("saved identity surfaces must both be searched")
			}
			if err := multisurfaceTermPartition(meta.NormalizedQueryTerms, surface.MatchedTerms, surface.MissingTerms); err != nil {
				t.Fatal(err)
			}
			for _, term := range surface.MatchedTerms {
				union[term] = true
			}
			if surfaceIndex == 0 {
				if surface.SourceSnapshotID != "" || surface.ExtractionViewID != "" || surface.RawContentHash != "" || surface.RenderedContentHash != "" || len(surface.MatchedSpans) != 0 || surface.SpansTruncated {
					t.Fatal("statement basis includes source-only fields")
				}
				continue
			}
			view, exists := views[surface.ExtractionViewID]
			if !exists || surface.SourceSnapshotID != record.Source.SourceSnapshotID || view.SourceID != surface.SourceSnapshotID || view.Hash != surface.RenderedContentHash || surface.RawContentHash != record.Source.RawContentHash || surface.RawContentHash != view.Hash || surface.ExtractionViewID != record.ExtractionViewID || len(surface.MatchedSpans) > 8 {
				t.Fatal("source retrieval identity/hash differs from frozen view")
			}
			coveredTerms := map[string]bool{}
			for _, span := range surface.MatchedSpans {
				ref := span.SourceRef
				if saved, ok := view.Spans[ref.SpanID]; !ok || !reflect.DeepEqual(ref, saved) {
					t.Fatal("search excerpt differs from frozen span catalog identity")
				}
				if ref.ExtractionViewID != view.ID || ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte > len(view.Text) || view.Text[ref.StartByte:ref.EndByte] != ref.QuotedText || "sha256:"+hanOutcomeHash([]byte(ref.QuotedText)) != ref.QuotedTextHash {
					t.Fatal("search excerpt is not exact saved source bytes")
				}
				within := false
				for _, old := range want.Refs {
					within = within || (old.ExtractionViewID == ref.ExtractionViewID && old.StartByte <= ref.StartByte && old.EndByte >= ref.EndByte)
				}
				if span.WithinProposalSourceRefs != within {
					t.Fatal("search excerpt original-citation membership is incorrect")
				}
				seen := map[string]bool{}
				for _, term := range span.MatchedTerms {
					if !slices.Contains(surface.MatchedTerms, term) || seen[term] {
						t.Fatal("excerpt reports an unknown or repeated term")
					}
					seen[term] = true
					coveredTerms[term] = true
				}
			}
			if !surface.SpansTruncated && len(coveredTerms) != len(surface.MatchedTerms) {
				t.Fatal("untruncated source excerpts omit reported matched terms")
			}
		}
		if match.Rank != float64(len(union)) {
			t.Fatal("rank is not unique matched-term count")
		}
		if index > 0 {
			previous := r.Matches[index-1]
			a, b := created[previous.RecordRef.ID], created[match.RecordRef.ID]
			if previous.Rank < match.Rank || (previous.Rank == match.Rank && (a.Before(b) || (a.Equal(b) && previous.RecordRef.ID > match.RecordRef.ID))) {
				t.Fatal("results violate term-count/time/ID total order")
			}
		}
		switch match.AdmissionOutcome {
		case "admitted":
			counts.Admitted++
		case "rejected":
			counts.Rejected++
		case "audit_only":
			counts.AuditOnly++
		case "pending":
			counts.Pending++
		default:
			t.Fatal("unknown saved outcome")
		}
		o.ReturnedIDs = append(o.ReturnedIDs, match.RecordRef.ID)
	}
	if err := hanOutcomeKnownIDs(o.ReturnedIDs, report.Plan.LabelForIDs); err != nil {
		t.Fatal(err)
	}
	if r.Counts.Admission != counts || r.Counts.Lifecycle.SourceSnapshot != len(r.Matches) || r.Counts.Lifecycle.RepositoryActive != 0 || r.Counts.Lifecycle.RepositoryHistorical != 0 {
		t.Fatal("returned-match aggregates do not match exact records")
	}
	for _, id := range baselineIDs {
		if !slices.Contains(o.ReturnedIDs, id) && !e.Truncated {
			t.Fatal("untruncated expanded union lost a baseline candidate")
		}
	}
	o.Stage = "relevance_observation_and_replay"
	for _, id := range tc.ExpectedRelevantIDs {
		if !slices.Contains(o.ReturnedIDs, id) {
			o.MissingRelevantIDs = append(o.MissingRelevantIDs, id)
		}
	}
	for _, id := range o.ReturnedIDs {
		if !slices.Contains(tc.ExpectedRelevantIDs, id) {
			o.ExtraIDs = append(o.ExtraIDs, id)
		}
	}
	key := tc.ID + "/" + mode.Name
	if replay == 0 {
		first[key] = r
	} else if !reflect.DeepEqual(first[key], r) {
		t.Fatal("full multisurface response changed on identical replay")
	}
	o.Stage, o.ContractPassed = "completed", true
	report.Completed++
}

func multisurfaceTermPartition(query, matched, missing []string) error {
	seen := map[string]bool{}
	for _, part := range [][]string{matched, missing} {
		for _, term := range part {
			if term == "" || seen[term] || !slices.Contains(query, term) {
				return fmt.Errorf("retrieval basis terms do not partition the normalized query")
			}
			seen[term] = true
		}
	}
	if len(seen) != len(query) {
		return fmt.Errorf("retrieval basis omitted query terms")
	}
	return nil
}

func TestMultisurfaceLabConfigBoundaries(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || name == "DATABASE_DNS" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DNS" {
			t.Setenv(name, "")
		}
	}
	for _, user := range []string{"ahe_brief_operator", "brief_query_login"} {
		c, err := multisurfaceLabConfig("/private/tmp/ahe-multisurface.fixture", "55445", user)
		if err != nil || c.Password != "" || c.Fallbacks != nil || c.Database != "ahe_brief_lab" || c.RuntimeParams["default_transaction_read_only"] != "on" {
			t.Fatal("fixed read-only configuration failed")
		}
	}
	for _, tc := range []struct{ socket, port, user string }{{"/private/tmp/unapproved-synthetic-socket", "55444", "ahe_brief_operator"}, {"localhost", "55445", "ahe_brief_operator"}, {"/private/tmp/ahe-multisurface.fixture", "55439", "ahe_brief_operator"}, {"/private/tmp/ahe-multisurface.fixture", "55445", "brief_intake_login"}, {"/private/tmp/ahe-multisurface.fixture/../other", "55445", "ahe_brief_operator"}} {
		if _, err := multisurfaceLabConfig(tc.socket, tc.port, tc.user); err == nil {
			t.Fatal("unapproved database target accepted")
		}
	}
	t.Setenv("PGHOST", "unexpected")
	if _, err := multisurfaceLabConfig("/private/tmp/ahe-multisurface.fixture", "55445", "brief_query_login"); err == nil {
		t.Fatal("inherited PG environment accepted")
	}
}

func TestMultisurfaceTermPartition(t *testing.T) {
	if multisurfaceTermPartition([]string{"地震", "orion"}, []string{"地震"}, []string{"orion"}) != nil {
		t.Fatal("valid partition rejected")
	}
	for _, tc := range []struct{ matched, missing []string }{{[]string{"地震"}, []string{"地震"}}, {[]string{"other"}, []string{"orion"}}, {[]string{}, []string{}}, {[]string{""}, []string{"orion"}}} {
		if multisurfaceTermPartition([]string{"地震", "orion"}, tc.matched, tc.missing) == nil {
			t.Fatal("invalid partition accepted")
		}
	}
}

func TestMultisurfaceFrozenPlan(t *testing.T) {
	path := os.Getenv("AHE_MULTISURFACE_PLAN_CHECK")
	if path == "" {
		t.Skip("optional offline plan check; never opens DB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read offline plan")
	}
	original, err := os.ReadFile(multisurfaceOriginalPlanPath(t))
	if err != nil {
		t.Fatal("cannot read frozen baseline plan")
	}
	plan, err := multisurfaceDecodePlan(data, original)
	if err != nil {
		t.Fatal(err)
	}
	snapshotData, err := os.ReadFile(plan.Snapshot.Path)
	if err != nil || hanOutcomeHash(snapshotData) != hanOutcomeSnapshotSHA {
		t.Fatal("offline source snapshot hash mismatch")
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal(snapshotData, &snapshot) != nil {
		t.Fatal("offline source snapshot invalid")
	}
	if len(multisurfaceSavedViews(t, snapshot)) != 4 {
		t.Fatal("offline source view inventory mismatch")
	}
	for _, change := range []string{"query", "label", "schema", "replays"} {
		candidate := plan
		candidate.Cases = slices.Clone(plan.Cases)
		candidate.Modes = slices.Clone(plan.Modes)
		switch change {
		case "query":
			candidate.Cases[0].Request.Query += " changed"
		case "label":
			candidate.Cases[0].ExpectedRelevantIDs = []string{}
		case "schema":
			candidate.Modes[0].ResponseSchema = multisurfaceLabSchema
		case "replays":
			candidate.Replays = 1
		}
		changed, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := multisurfaceDecodePlan(changed, original); err == nil {
			t.Fatal("changed frozen case or mode was accepted")
		}
	}
	t.Logf("frozen 12 cases, 18 expected pairs, 72 brief calls; plan SHA256=%s", hanOutcomeHash(data))
}
