//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const hanOutcomeSnapshotSHA = "876e1acae436c209668590465679598c188fb6121f36e3da6b338dc46d83725c"

// MCP record references use this projection kind, not the SQL search kind.
const hanOutcomeProposalReferenceKind = "proposal"

// This runner only reads an independently restored native T1-T4 archive. It
// never provisions a schema, installs policy, submits material, or decides it.
type hanOutcomeExpected struct {
	IDs       []string `json:"ids"`
	Truncated bool     `json:"truncated"`
}

type hanOutcomeCase struct {
	ID                  string                                           `json:"id"`
	Kind                string                                           `json:"kind"`
	Request             evidencequerymcp.GetGroundedEvidenceBriefRequest `json:"request"`
	Baseline            *hanOutcomeExpected                              `json:"baseline"`
	Han                 *hanOutcomeExpected                              `json:"han"`
	Exact               *hanOutcomeExpected                              `json:"exact"`
	ExpectedRelevantIDs []string                                         `json:"expected_relevant_ids"`
	Note                string                                           `json:"note"`
}

type hanOutcomeFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type hanOutcomePlan struct {
	Contract            string            `json:"contract"`
	Snapshot            hanOutcomeFile    `json:"snapshot"`
	SourceContentHashes map[string]string `json:"source_content_hashes"`
	LabelForIDs         map[string]string `json:"label_for_ids"`
	Replays             int               `json:"replays"`
	Cases               []hanOutcomeCase  `json:"cases"`
}

type hanOutcomeObservation struct {
	CaseID, Kind, Mode, Stage, StartedAt, FinishedAt string
	Replay                                           int
	ElapsedNanoseconds                               int64
	Request                                          evidencequerymcp.GetGroundedEvidenceBriefRequest
	Response                                         evidencequerymcp.GroundedEvidenceBriefResponse
	ReturnedIDs, MissingRelevantIDs, ExtraIDs        []string
	EngineeringAssertion, ContractPassed             bool
}

type hanOutcomeReport struct {
	Contract, Phase, StartedAt, FinishedAt, Database, SystemIdentifier string
	PlanSHA256                                                         string
	Plan                                                               hanOutcomePlan
	ServerVersion                                                      int
	SocketOnly, Passed, QueryEOF, SnapshotVerified, Unchanged          bool
	Planned, Attempted, Completed, NotRun                              int
	UniqueCaseModes, RecordReadbackCalls, CatalogCalls                 int
	Failures, Limitations                                              []string
	Policy                                                             dbrole.PolicyStatus
	LiveQueryTools                                                     json.RawMessage
	Records                                                            []evidencequerymcp.GetEvidenceRecordResponse
	Observations                                                       []hanOutcomeObservation
	CodeBefore, CodeAfter                                              map[string]string
	Before, After                                                      map[string]hanQueryLabTableState
}

func hanOutcomeConfig(socket, port, user string) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN") {
			return nil, fmt.Errorf("outcome lab refuses inherited database environment")
		}
	}
	if port != "55444" || filepath.Dir(socket) != "/private/tmp" || !strings.HasPrefix(filepath.Base(socket), "ahe-han-outcome.") || filepath.Clean(socket) != socket || strings.ContainsAny(socket, "\x00\r\n") || (user != "ahe_brief_operator" && user != "brief_query_login") {
		return nil, fmt.Errorf("outcome lab requires its pinned private socket, port, and login")
	}
	values := url.Values{"host": {socket}, "port": {port}, "sslmode": {"disable"}, "passfile": {"/dev/null"}, "connect_timeout": {"5"}, "target_session_attrs": {"any"}, "statement_timeout": {"2000"}, "default_transaction_read_only": {"on"}, "timezone": {"Asia/Taipei"}, "search_path": {"ahe_brief,pg_catalog"}}
	dsn := url.URL{Scheme: "postgres", User: url.User(user), Path: "/ahe_brief_lab", RawQuery: values.Encode()}
	config, err := pgx.ParseConfig(dsn.String())
	if err != nil {
		return nil, fmt.Errorf("cannot parse fixed outcome lab settings")
	}
	config.Password, config.Fallbacks = "", nil
	return config, nil
}

func hanOutcomeDecodePlan(data []byte) (hanOutcomePlan, error) {
	var plan hanOutcomePlan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, fmt.Errorf("invalid outcome plan JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return plan, fmt.Errorf("outcome plan must contain one JSON object")
	}
	if plan.Contract != "han-outcome-plan-v1" || !filepath.IsAbs(plan.Snapshot.Path) || plan.Snapshot.SHA256 != hanOutcomeSnapshotSHA || plan.Replays != 2 || len(plan.LabelForIDs) != 4 || len(plan.SourceContentHashes) != 4 || len(plan.Cases) == 0 || len(plan.Cases) > 100 {
		return plan, fmt.Errorf("outcome plan does not pin the native snapshot and bounded matrix")
	}
	labels := map[string]bool{}
	for id, label := range plan.LabelForIDs {
		if !strings.HasPrefix(id, "occ:") || !slices.Contains([]string{"T1", "T2", "T3", "T4"}, label) || labels[label] {
			return plan, fmt.Errorf("outcome labels must identify four distinct native proposals")
		}
		labels[label] = true
	}
	for id, hash := range plan.SourceContentHashes {
		decoded, err := hex.DecodeString(strings.TrimPrefix(hash, "sha256:"))
		if !strings.HasPrefix(id, "srcsnap:") || !strings.HasPrefix(hash, "sha256:") || err != nil || len(decoded) != 32 {
			return plan, fmt.Errorf("outcome plan source hashes are invalid")
		}
	}
	seen := map[string]bool{}
	for _, tc := range plan.Cases {
		q := tc.Request
		if tc.ID == "" || seen[tc.ID] || q.QueryMode != "" || strings.TrimSpace(q.Query) == "" || !utf8.ValidString(q.Query) || utf8.RuneCountInString(q.Query) > 256 || len(strings.Fields(q.Query)) > 16 || q.Limit < 0 || q.Limit > 100 {
			return plan, fmt.Errorf("outcome case identity or request is invalid")
		}
		seen[tc.ID] = true
		for _, expected := range []*hanOutcomeExpected{tc.Baseline, tc.Han, tc.Exact} {
			if (tc.Kind == "engineering" && (expected == nil || expected.IDs == nil)) || (tc.Kind == "relevance" && expected != nil) || (tc.Kind != "engineering" && tc.Kind != "relevance") {
				return plan, fmt.Errorf("engineering or relevance oracle boundary is invalid")
			}
			if expected != nil {
				if err := hanOutcomeKnownIDs(expected.IDs, plan.LabelForIDs); err != nil {
					return plan, err
				}
			}
		}
		if err := hanOutcomeKnownIDs(tc.ExpectedRelevantIDs, plan.LabelForIDs); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func hanOutcomeKnownIDs(ids []string, labels map[string]string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if labels[id] == "" || seen[id] {
			return fmt.Errorf("outcome oracle contains an unknown or repeated proposal")
		}
		seen[id] = true
	}
	return nil
}

func hanOutcomeHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func hanOutcomeCodeHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := hanQueryLabCodeHashes(t)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate outcome runner")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("cannot hash outcome runner")
	}
	hashes["internal/mcpintegration/han_outcome_integration_test.go"] = hanOutcomeHash(data)
	return hashes
}

func TestIntegrationHanOutcomeReadOnlyLab(t *testing.T) {
	socket, port := os.Getenv("AHE_HAN_OUTCOME_LAB_SOCKET"), os.Getenv("AHE_HAN_OUTCOME_LAB_PORT")
	if socket == "" && port == "" && os.Getenv("AHE_HAN_OUTCOME_LAB_SYSTEM_IDENTIFIER") == "" && os.Getenv("AHE_HAN_OUTCOME_LAB_REPORT") == "" && os.Getenv("AHE_HAN_OUTCOME_LAB_PLAN") == "" {
		t.Skip("set the five dedicated AHE_HAN_OUTCOME_LAB settings for the restored read-only lab")
	}
	report := hanOutcomeReport{Contract: "han-outcome-read-only-v1", Phase: "configuration", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{
		"Native restored synthetic T1-T4 records already carry human decisions; this run makes no new admission or disposition.",
		"Engineering ID assertions and agent-labeled relevance observations are separate; a relevance miss or extra is not a test failure.",
		"Returned candidates, ranks, and historical admission are not new source-support judgments or general recall/semantic evidence.",
		"Sequential single-process replay is not a performance benchmark; each request has 5s, each SQL statement 2s, the child 60s total.",
		"No schema/role cleanup is attempted: the restored database is preserved; unchanged row-content digests and normal EOF are separate witnesses.",
	}}
	reportPath := os.Getenv("AHE_HAN_OUTCOME_LAB_REPORT")
	if !filepath.IsAbs(reportPath) {
		t.Fatal("outcome lab requires an absolute private report path")
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
	config, err := hanOutcomeConfig(socket, port, "ahe_brief_operator")
	if err != nil {
		t.Fatal(err)
	}
	identity := os.Getenv("AHE_HAN_OUTCOME_LAB_SYSTEM_IDENTIFIER")
	if n, err := strconv.ParseUint(identity, 10, 64); err != nil || n == 0 {
		t.Fatal("outcome lab requires a pinned PostgreSQL system identifier")
	}
	planPath := os.Getenv("AHE_HAN_OUTCOME_LAB_PLAN")
	if !filepath.IsAbs(planPath) {
		t.Fatal("outcome plan path must be absolute")
	}
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal("cannot read frozen outcome plan")
	}
	report.Plan, err = hanOutcomeDecodePlan(planData)
	if err != nil {
		t.Fatal(err)
	}
	report.PlanSHA256 = hanOutcomeHash(planData)
	report.Planned = len(report.Plan.Cases) * 3 * report.Plan.Replays
	report.UniqueCaseModes = len(report.Plan.Cases) * 3
	snapshotData, err := os.ReadFile(report.Plan.Snapshot.Path)
	if err != nil || hanOutcomeHash(snapshotData) != hanOutcomeSnapshotSHA {
		t.Fatal("native T4 saved snapshot hash mismatch")
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal(snapshotData, &snapshot) != nil {
		t.Fatal("cannot decode pinned native snapshot")
	}
	report.CodeBefore = hanOutcomeCodeHashes(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	report.Phase = "database_preflight"
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to the pinned restored outcome lab")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var listen, sockets, user, schema, readOnly string
	var extensions, publicAuthority int
	if err := admin.QueryRow(ctx, `SELECT current_database(), current_user, current_schema(), current_setting('listen_addresses'), current_setting('unix_socket_directories'), current_setting('server_version_num')::integer, current_setting('default_transaction_read_only'), (SELECT system_identifier::text FROM pg_control_system()), (SELECT count(*) FROM pg_extension WHERE extname <> 'plpgsql'), (SELECT count(*) FROM pg_database d, LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname=current_database() AND a.grantee=0 AND a.privilege_type IN ('CREATE','TEMPORARY'))`).Scan(&report.Database, &user, &schema, &listen, &sockets, &report.ServerVersion, &readOnly, &report.SystemIdentifier, &extensions, &publicAuthority); err != nil {
		t.Fatal("cannot inspect restored outcome database identity")
	}
	report.SocketOnly = listen == "" && sockets == socket
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
		report.CodeAfter = hanOutcomeCodeHashes(t)
		currentPlan, planErr := os.ReadFile(planPath)
		currentSnapshot, snapshotErr := os.ReadFile(report.Plan.Snapshot.Path)
		if !reflect.DeepEqual(report.Before, report.After) || !reflect.DeepEqual(report.CodeBefore, report.CodeAfter) || planErr != nil || snapshotErr != nil || hanOutcomeHash(currentPlan) != report.PlanSHA256 || hanOutcomeHash(currentSnapshot) != hanOutcomeSnapshotSHA {
			t.Fatal("outcome data, source, plan, or code changed during read-only queries")
		}
		report.Unchanged = true
		report.Phase = phase
	})
	report.Phase = "query_policy"
	queryConfig, err := hanOutcomeConfig(socket, port, "brief_query_login")
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
	report.LiveQueryTools = hanQueryLabTools(t, query)
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
	first := map[string]evidencequerymcp.GroundedEvidenceBriefResponse{}
	for replay := range report.Plan.Replays {
		for _, tc := range report.Plan.Cases {
			for i, mode := range []string{"", hanQueryLabMode, evidenceingestion.EvidenceQueryModeExactLexical} {
				name := []string{"baseline", "han", "exact"}[i]
				report.Phase = "query/" + tc.ID + "/" + name
				if !t.Run(fmt.Sprintf("%s/%s/replay-%d", tc.ID, name, replay), func(t *testing.T) {
					hanOutcomeObserve(t, query, tc, mode, name, replay, wants, report.Records, first, &report)
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

type hanOutcomeProposal struct {
	ID        string                                `json:"proposal_occurrence_id"`
	Statement string                                `json:"statement_text"`
	Outcome   string                                `json:"admission_outcome"`
	Canonical *string                               `json:"canonical_ref"`
	Refs      []evidenceingestion.ResolvedSourceRef `json:"source_refs"`
}

func hanOutcomeObserve(t *testing.T, query *authorityProcess, tc hanOutcomeCase, mode, name string, replay int, wants map[string]hanOutcomeProposal, records []evidencequerymcp.GetEvidenceRecordResponse, first map[string]evidencequerymcp.GroundedEvidenceBriefResponse, report *hanOutcomeReport) {
	t.Helper()
	observation := hanOutcomeObservation{CaseID: tc.ID, Kind: tc.Kind, Mode: name, Replay: replay, Stage: "request", Request: tc.Request, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), EngineeringAssertion: tc.Kind == "engineering"}
	observation.Request.QueryMode = mode
	report.Attempted++
	start := time.Now()
	defer func() {
		observation.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if observation.ElapsedNanoseconds == 0 {
			observation.ElapsedNanoseconds = time.Since(start).Nanoseconds()
		}
		report.Observations = append(report.Observations, observation)
	}()
	originalContext := query.ctx
	requestContext, cancel := context.WithTimeout(originalContext, 5*time.Second)
	query.ctx = requestContext
	defer func() { query.ctx = originalContext; cancel() }()
	response := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, query, "get_grounded_evidence_brief", observation.Request)
	observation.ElapsedNanoseconds = time.Since(start).Nanoseconds()
	observation.Response = response
	observation.Stage = "authority_validation"
	for _, match := range response.Matches {
		want, ok := wants[match.RecordRef.ID]
		var canonical *string
		if match.CanonicalRef != nil {
			canonical = &match.CanonicalRef.ID
			if match.CanonicalRef.Kind != "canonical_evidence" {
				t.Fatal("retrieved canonical reference kind changed")
			}
		}
		if !ok || match.RecordRef.Kind != hanOutcomeProposalReferenceKind || match.ProposalKind != "statement" || match.StatementText != want.Statement || match.AdmissionOutcome != want.Outcome || !reflect.DeepEqual(canonical, want.Canonical) || !reflect.DeepEqual(match.SourceRefs, want.Refs) {
			t.Fatal("retrieved proposal changed native statement, disposition, canonical reference, or spans")
		}
		if match.SourceContext != nil && match.SourceContext.SearchParticipation {
			t.Fatal("source context participated in statement retrieval")
		}
		for _, record := range records {
			if record.RecordRef.ID == match.RecordRef.ID {
				if match.SourceScopeRef.Kind != "source_snapshot" || match.SourceScopeRef.ID != record.Source.SourceSnapshotID {
					t.Fatal("retrieved proposal changed its immutable source scope")
				}
				found := false
				for _, scope := range response.SourceScopes {
					if scope.ScopeRef == match.SourceScopeRef && reflect.DeepEqual(scope.Source, record.Source) {
						found = true
					}
				}
				if !found {
					t.Fatal("retrieved source package differs from exact native readback")
				}
			}
		}
		observation.ReturnedIDs = append(observation.ReturnedIDs, match.RecordRef.ID)
	}
	if err := hanOutcomeKnownIDs(observation.ReturnedIDs, report.Plan.LabelForIDs); err != nil {
		t.Fatal(err)
	}
	execution := response.QueryExecution
	wantMode, wantPlan, wantNormalizer := mode, evidenceingestion.EvidenceQueryPlanRecoveryV2, evidenceingestion.EvidenceQueryNormalizerRecoveryV1
	if mode == "" {
		wantMode = evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery
	} else if mode == hanQueryLabMode {
		wantPlan, wantNormalizer = evidenceingestion.EvidenceQueryPlanExperimentalHanRecoveryV1, evidenceingestion.EvidenceQueryNormalizerExperimentalHanV1
	} else {
		wantPlan, wantNormalizer = evidenceingestion.EvidenceQueryPlanExactV1, evidenceingestion.EvidenceQueryNormalizerSimpleV1
	}
	limit, scope := tc.Request.Limit, strings.TrimSpace(tc.Request.LifecycleScope)
	if limit == 0 {
		limit = 20
	}
	if scope == "" {
		scope = "active"
		if tc.Request.SourceGenerationID != "" {
			scope = "all"
		}
	}
	filters := evidencequerymcp.GroundedEvidenceQueryFilters{SourceSnapshotID: strings.TrimSpace(tc.Request.SourceSnapshotID), RepositorySnapshotID: strings.TrimSpace(tc.Request.RepositorySnapshotID), SourceGenerationID: strings.TrimSpace(tc.Request.SourceGenerationID), SourceID: strings.TrimSpace(tc.Request.SourceID), SourceVersion: strings.TrimSpace(tc.Request.SourceVersion), AdmissionOutcome: strings.TrimSpace(tc.Request.AdmissionOutcome), LifecycleScope: scope}
	wantSchema := tc.Request.ResponseSchema
	if wantSchema == "" {
		wantSchema = evidencequerymcp.GroundedEvidenceBriefSchemaV2
	}
	if response.SchemaVersion != wantSchema || response.Query != strings.TrimSpace(tc.Request.Query) || execution.OriginalQuery != response.Query || execution.QueryMode != wantMode || execution.PlanVersion != wantPlan || execution.NormalizerVersion != wantNormalizer || execution.Filters != filters || execution.Limit != limit || execution.GlobalAbsenceInferenceAllowed || execution.SearchSurface != evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement || !slices.Equal(execution.SearchedFields, []string{evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement}) || !slices.Equal(execution.SearchedRecordKinds, []string{"proposal_occurrence"}) || execution.CandidateCount != len(response.Matches) || response.Counts.ReturnedMatches != len(response.Matches) || execution.QueryCount != len(execution.Attempts) || len(response.Matches) > limit || execution.SearchCompleteWithinSurface == execution.Truncated || response.Boundary != (evidencequerymcp.GroundedEvidenceBriefBoundary{SearchSurface: execution.SearchSurface, QueryMode: wantMode, LifecycleScope: scope, Limit: limit, Truncated: execution.Truncated}) {
		t.Fatal("query execution boundary differs from the frozen request")
	}
	if mode != hanQueryLabMode && (execution.FallbackStatus != "" || execution.RankingPolicy != "") {
		t.Fatal("legacy mode acquired experimental metadata")
	}
	if execution.RankingPolicy != "" {
		if execution.RankingPolicy != "created_at_desc,id_asc_unscored" {
			t.Fatal("unknown ranking policy")
		}
		for _, match := range response.Matches {
			if match.Rank != 0 {
				t.Fatal("Han fallback rank is not unscored")
			}
		}
	}
	observation.Stage = "oracle_and_replay"
	if tc.Kind == "engineering" {
		want := map[string]*hanOutcomeExpected{"baseline": tc.Baseline, "han": tc.Han, "exact": tc.Exact}[name]
		if !slices.Equal(observation.ReturnedIDs, want.IDs) || execution.Truncated != want.Truncated {
			t.Fatal("engineering IDs or truncation differ from the predeclared oracle")
		}
	}
	if tc.Kind == "relevance" {
		for _, id := range tc.ExpectedRelevantIDs {
			if !slices.Contains(observation.ReturnedIDs, id) {
				observation.MissingRelevantIDs = append(observation.MissingRelevantIDs, id)
			}
		}
		for _, id := range observation.ReturnedIDs {
			if !slices.Contains(tc.ExpectedRelevantIDs, id) {
				observation.ExtraIDs = append(observation.ExtraIDs, id)
			}
		}
	}
	key := tc.ID + "/" + name
	if replay == 0 {
		first[key] = response
	} else if !reflect.DeepEqual(first[key], response) {
		t.Fatal("identical read-only request changed on replay")
	}
	observation.Stage, observation.ContractPassed = "completed", true
	report.Completed++
}

func hanOutcomeSavedSources(t *testing.T, snapshot map[string]json.RawMessage) map[string]evidencequerymcp.SourceInfo {
	t.Helper()
	read := func(key string) []map[string]any {
		var rows []map[string]any
		if json.Unmarshal(snapshot[key], &rows) != nil {
			t.Fatal("cannot decode saved source lineage")
		}
		return rows
	}
	sources := map[string]evidencequerymcp.SourceInfo{}
	for _, row := range read("source_snapshots") {
		source := evidencequerymcp.SourceInfo{BindingKind: "source_snapshot", SourceSnapshotID: row["source_snapshot_id"].(string), SourceSystem: row["source_system"].(string), SourceID: row["source_id"].(string), SourceVersion: row["source_version"].(string), RawContentHash: row["raw_content_hash"].(string)}
		sources[source.SourceSnapshotID] = source
	}
	runs, attempts := map[string]string{}, map[string]string{}
	for _, row := range read("extraction_runs") {
		runs[row["extraction_run_id"].(string)] = row["source_snapshot_id"].(string)
	}
	for _, row := range read("extraction_attempts") {
		attempts[row["extraction_attempt_id"].(string)] = row["extraction_run_id"].(string)
	}
	result := map[string]evidencequerymcp.SourceInfo{}
	for _, row := range read("proposals") {
		source := sources[runs[attempts[row["extraction_attempt_id"].(string)]]]
		if source.SourceSnapshotID == "" {
			t.Fatal("saved proposal source lineage is incomplete")
		}
		result[row["proposal_occurrence_id"].(string)] = source
	}
	return result
}

func TestHanOutcomeFrozenPlan(t *testing.T) {
	// This variable selects only an offline decoder test, never a DB connection.
	path := os.Getenv("AHE_HAN_OUTCOME_PLAN_CHECK")
	if path == "" {
		t.Skip("set AHE_HAN_OUTCOME_PLAN_CHECK for an offline frozen-plan check")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read offline plan")
	}
	plan, err := hanOutcomeDecodePlan(data)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(plan.Snapshot.Path)
	if err != nil || hanOutcomeHash(snapshot) != hanOutcomeSnapshotSHA {
		t.Fatal("offline native snapshot hash mismatch")
	}
	var saved map[string]json.RawMessage
	if json.Unmarshal(snapshot, &saved) != nil {
		t.Fatal("offline snapshot cannot decode")
	}
	if len(hanOutcomeSavedSources(t, saved)) != 4 {
		t.Fatal("offline source lineage must contain four proposals")
	}
	t.Logf("offline plan SHA256=%s cases=%d unique-case-modes=%d calls=%d", hanOutcomeHash(data), len(plan.Cases), len(plan.Cases)*3, len(plan.Cases)*3*plan.Replays)
}

// Compare saved fields, not a freshly searched oracle. The fixed map names only
// native tables from the pinned snapshot; it cannot execute plan-supplied SQL.
func hanOutcomeCompareSnapshot(t *testing.T, ctx context.Context, observer *pgxpool.Pool, snapshot map[string]json.RawMessage, plan hanOutcomePlan) {
	t.Helper()
	for saved, table := range map[string]string{"proposals": "proposal_occurrences", "source_snapshots": "source_snapshots", "decisions": "admission_decisions", "nodes": "canonical_graph_nodes", "edges": "canonical_graph_edges", "extraction_runs": "extraction_runs", "extraction_attempts": "extraction_attempts"} {
		var expected []map[string]any
		if json.Unmarshal(snapshot[saved], &expected) != nil || len(expected) == 0 {
			t.Fatal("saved native table witness missing")
		}
		rows, err := observer.Query(ctx, "SELECT to_jsonb(r) FROM "+pgx.Identifier{"ahe_brief", table}.Sanitize()+" r")
		if err != nil {
			t.Fatal("cannot compare saved native table witness")
		}
		var actual []map[string]any
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatal("cannot read native table row")
			}
			var all map[string]any
			if json.Unmarshal(raw, &all) != nil {
				rows.Close()
				t.Fatal("cannot decode native table row")
			}
			projected := map[string]any{}
			for key := range expected[0] {
				projected[key] = all[key]
			}
			actual = append(actual, projected)
		}
		rows.Close()
		if rows.Err() != nil {
			t.Fatal("cannot finish native table comparison")
		}
		encodeSorted := func(values []map[string]any) []string {
			var result []string
			for _, value := range values {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal("cannot encode native table witness")
				}
				result = append(result, string(data))
			}
			slices.Sort(result)
			return result
		}
		if !slices.Equal(encodeSorted(expected), encodeSorted(actual)) {
			t.Fatalf("restored table %s differs from pinned snapshot", table)
		}
	}
	var sources []struct {
		ID   string `json:"source_snapshot_id"`
		Hash string `json:"raw_content_hash"`
	}
	if json.Unmarshal(snapshot["source_snapshots"], &sources) != nil {
		t.Fatal("cannot decode saved sources")
	}
	for _, source := range sources {
		var content []byte
		if plan.SourceContentHashes[source.ID] != source.Hash {
			t.Fatal("plan source hash disagrees with saved authority")
		}
		if err := observer.QueryRow(ctx, `SELECT raw_content FROM ahe_brief.source_blobs WHERE raw_content_hash=$1`, source.Hash).Scan(&content); err != nil || "sha256:"+hanOutcomeHash(content) != source.Hash {
			t.Fatal("restored source bytes differ from saved content hash")
		}
	}
	var proposals []hanOutcomeProposal
	if json.Unmarshal(snapshot["proposals"], &proposals) != nil {
		t.Fatal("cannot decode saved proposal identities")
	}
	for _, proposal := range proposals {
		if plan.LabelForIDs[proposal.ID] == "" {
			t.Fatal("plan did not identify a saved native proposal")
		}
	}
}

func TestHanOutcomeConfig(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN" {
			t.Setenv(name, "")
		}
	}
	config, err := hanOutcomeConfig("/private/tmp/ahe-han-outcome.config-test", "55444", "ahe_brief_operator")
	if err != nil || config.Database != "ahe_brief_lab" || config.User != "ahe_brief_operator" || config.Port != 55444 || config.Password != "" || config.TLSConfig != nil || len(config.Fallbacks) != 0 || config.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatal("fixed outcome configuration mismatch")
	}
	for _, name := range []string{"PGHOST", "PGSERVICE", "PGSERVICEFILE", "PGPASSWORD", "PGPASSFILE", "DATABASE_DSN", "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "must-not-be-read")
			if _, err := hanOutcomeConfig("/private/tmp/ahe-han-outcome.config-test", "55444", "ahe_brief_operator"); err == nil {
				t.Fatal("inherited database setting accepted")
			}
		})
	}
	for _, input := range [][3]string{{"localhost", "55444", "ahe_brief_operator"}, {"/private/tmp/ahe-han-outcome.x", "55443", "ahe_brief_operator"}, {"/private/tmp/other", "55444", "ahe_brief_operator"}, {"/private/tmp/ahe-han-outcome.x/child", "55444", "ahe_brief_operator"}, {"/private/tmp/ahe-han-outcome.x", "55444", "postgres"}} {
		if _, err := hanOutcomeConfig(input[0], input[1], input[2]); err == nil {
			t.Fatal("unbounded outcome connection accepted")
		}
	}
}

func TestHanOutcomeProjectionKind(t *testing.T) {
	// The captured v3 failure exposed these two deliberately different kinds.
	// This offline witness contains no query oracle or relevance judgment.
	var response evidencequerymcp.GroundedEvidenceBriefResponse
	if err := json.Unmarshal([]byte(`{"query_execution":{"searched_record_kinds":["proposal_occurrence"]},"matches":[{"record_ref":{"kind":"proposal","id":"occ:projection-test"}}]}`), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Matches) != 1 || response.Matches[0].RecordRef.Kind != hanOutcomeProposalReferenceKind || !slices.Equal(response.QueryExecution.SearchedRecordKinds, []string{"proposal_occurrence"}) {
		t.Fatal("MCP proposal projection was confused with its persisted search record kind")
	}
	if hanOutcomeProposalReferenceKind == response.QueryExecution.SearchedRecordKinds[0] {
		t.Fatal("projection-kind assertion must not accept the SQL search kind")
	}
}

func TestHanOutcomePlanBoundary(t *testing.T) {
	plan := hanOutcomePlan{Contract: "han-outcome-plan-v1", Snapshot: hanOutcomeFile{Path: "/synthetic/snapshot.json", SHA256: hanOutcomeSnapshotSHA}, SourceContentHashes: map[string]string{}, LabelForIDs: map[string]string{}, Replays: 2}
	for i := range 4 {
		plan.LabelForIDs[fmt.Sprintf("occ:%d", i)] = fmt.Sprintf("T%d", i+1)
		plan.SourceContentHashes[fmt.Sprintf("srcsnap:%d", i)] = "sha256:" + strings.Repeat("a", 64)
	}
	plan.Cases = []hanOutcomeCase{{ID: "engineering", Kind: "engineering", Request: evidencequerymcp.GetGroundedEvidenceBriefRequest{Query: "地震 芮氏"}, Baseline: &hanOutcomeExpected{IDs: []string{}}, Han: &hanOutcomeExpected{IDs: []string{"occ:2"}}, Exact: &hanOutcomeExpected{IDs: []string{}}}, {ID: "relevance", Kind: "relevance", Request: evidencequerymcp.GetGroundedEvidenceBriefRequest{Query: "planned maintenance"}, ExpectedRelevantIDs: []string{"occ:3"}}}
	data, _ := json.Marshal(plan)
	if _, err := hanOutcomeDecodePlan(data); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*hanOutcomePlan){func(p *hanOutcomePlan) { p.Cases[1].Han = &hanOutcomeExpected{IDs: []string{}} }, func(p *hanOutcomePlan) { p.Cases[0].Baseline = nil }, func(p *hanOutcomePlan) { p.Cases[1].ID = p.Cases[0].ID }, func(p *hanOutcomePlan) { p.Cases[0].Request.QueryMode = hanQueryLabMode }, func(p *hanOutcomePlan) { p.Cases[1].ExpectedRelevantIDs = []string{"occ:unknown"} }, func(p *hanOutcomePlan) { p.Snapshot.SHA256 = strings.Repeat("0", 64) }} {
		var changed hanOutcomePlan
		if json.Unmarshal(data, &changed) != nil {
			t.Fatal("cannot clone pure plan")
		}
		mutate(&changed)
		encoded, _ := json.Marshal(changed)
		if _, err := hanOutcomeDecodePlan(encoded); err == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	for _, invalid := range [][]byte{append(slices.Clone(data), []byte("{}")...), bytes.Replace(data, []byte(`"contract":`), []byte(`"unknown":1,"contract":`), 1)} {
		if _, err := hanOutcomeDecodePlan(invalid); err == nil {
			t.Fatal("non-strict plan accepted")
		}
	}
}
