//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const hanPlanObserverSchema = "han_plan_observer"

type hanPlanBlock struct {
	Name, CacheMode string
	Rows            int
	FreshPool       bool
}
type hanPlanFrozen struct {
	Contract     string
	Blocks       []hanPlanBlock
	Cases        []hanScaleCase
	PairOrder    []string
	Corpus       []hanScaleRow
	CorpusSHA256 string
	CandidateSQL []hanScaleSQL
	CodeSHA256   map[string]string
	Limitations  []string
}
type hanPlanPrepared struct {
	Name, SQLSHA256           string
	PrepareTime               time.Time
	ParameterOIDs             []uint32
	FromSQL                   bool
	GenericPlans, CustomPlans int64
}
type hanPlanGIN struct {
	Index, Definition, DefinitionSHA256 string
	Bytes, PendingPages, PendingTuples  int64
	Options                             []string
}
type hanPlanMaintenance struct {
	LastVacuum, LastAutoVacuum, LastAnalyze, LastAutoAnalyze     *time.Time
	VacuumCount, AutoVacuumCount, AnalyzeCount, AutoAnalyzeCount int64
	LiveTuples, DeadTuples, ModifiedSinceAnalyze                 int64
}
type hanPlanSnapshot struct {
	At, FinishedAt string
	GIN            []hanPlanGIN
	Maintenance    hanPlanMaintenance
}
type hanPlanObserverFailure struct {
	Phase, Case string
	Repeat      int
	Snapshot    hanPlanSnapshot
}
type hanPlanAcquire struct {
	Case, Phase                                   string
	Repeat                                        int
	StartedAt, FinishedAt, AcquiredAt, ReleasedAt string
	ElapsedMicros, ReleaseMicros                  int64
	PID                                           int32
	Prepared                                      []hanPlanPrepared
	Before, After                                 hanPlanSnapshot
	Passed                                        bool
}
type hanPlanExplain struct {
	Case, Mode, Strategy, StatementName, SQLSHA256 string
	Arguments                                      []any
	Before, After                                  []hanPlanPrepared
	ElapsedMicros                                  int64
	Plan                                           json.RawMessage
	Passed                                         bool
}
type hanPlanDomain struct {
	hanScaleObservation
	StartedAt, FinishedAt string
}
type hanPlanBlockResult struct {
	Block                                           hanPlanBlock
	Phase                                           string
	PID                                             int32
	Binding                                         dbrole.RuntimeBinding
	Settings                                        map[string]string
	PlannedDomain, AttemptedDomain, NotRunDomain    int
	PlannedAcquire, AttemptedAcquire, NotRunAcquire int
	Before, After                                   hanPlanSnapshot
	BeforeTables, AfterTables                       map[string]hanQueryLabTableState
	Acquires                                        []hanPlanAcquire
	Domain                                          []hanPlanDomain
	PreparedBeforeExplain, PreparedAfterExplain     []hanPlanPrepared
	Explains                                        []hanPlanExplain
	Passed                                          bool
}
type hanPlanReport struct {
	Contract, StartedAt, FinishedAt, Phase, ActiveBlock, PlanFile, PlanSHA256    string
	Database, SystemIdentifier, Schema, ObserverExtensionVersion                 string
	ServerVersion                                                                int
	RuntimeRoles                                                                 []string
	PermanentBefore, PermanentAfter                                              int
	SchemaCleanupVerified, ObserverCleanupVerified, RolesCleanupVerified, Passed bool
	PlannedDomain, AttemptedDomain, NotRunDomain                                 int
	PlannedAcquire, AttemptedAcquire, NotRunAcquire                              int
	InitialReadback                                                              *evidenceingestion.ProposalQueryResult
	Batches                                                                      []hanScaleBatch
	Identities                                                                   []hanScaleIdentity
	Blocks                                                                       []hanPlanBlockResult
	LiveTools                                                                    json.RawMessage
	StdioSmoke                                                                   []evidencequerymcp.GroundedEvidenceBriefResponse
	StdioEOF                                                                     bool
	Failure                                                                      *hanScaleFailure
	ObserverFailure                                                              *hanPlanObserverFailure
	GINBaseline                                                                  []hanPlanGIN
}

func hanPlanBlocks() []hanPlanBlock {
	return []hanPlanBlock{
		{Name: "auto-1k", CacheMode: "auto", Rows: 1000},
		{Name: "auto-10k", CacheMode: "auto", Rows: 10000},
		{Name: "auto-50k", CacheMode: "auto", Rows: 50000},
		{Name: "force-custom-50k", CacheMode: "force_custom_plan", Rows: 50000, FreshPool: true},
		{Name: "force-generic-50k", CacheMode: "force_generic_plan", Rows: 50000, FreshPool: true},
		{Name: "auto-fresh-50k", CacheMode: "auto", Rows: 50000, FreshPool: true},
	}
}
func hanPlanCases() []hanScaleCase {
	all := hanScaleCases()
	return []hanScaleCase{all[0], all[2], all[5], all[8]}
}
func hanPlanDigest(data []byte) string { return strings.TrimPrefix(stdioContentHash(data), "sha256:") }

func hanPlanSameGINDefinitions(baseline, current []hanPlanGIN) bool {
	if len(baseline) != 2 || len(current) != 2 {
		return false
	}
	for i := range baseline {
		if baseline[i].Index != current[i].Index || baseline[i].DefinitionSHA256 == "" || baseline[i].DefinitionSHA256 != current[i].DefinitionSHA256 || baseline[i].Definition != current[i].Definition || !slices.Equal(baseline[i].Options, current[i].Options) {
			return false
		}
	}
	return true
}

// The four frozen cases require the complete baseline pair. Empty A results
// must not accidentally pass without performing the planned candidate lookups.
func hanPlanValidateExecution(input evidenceingestion.GroundedEvidenceBriefInput, tc hanScaleCase, execution evidenceingestion.EvidenceQueryExecution) error {
	wantMode, wantPlan, wantNormalizer := evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, evidenceingestion.EvidenceQueryPlanRecoveryV2, evidenceingestion.EvidenceQueryNormalizerRecoveryV1
	wantFallback, wantRanking := "", ""
	wantStrategies := []string{evidenceingestion.EvidenceQueryStrategyExactSimple, evidenceingestion.EvidenceQueryStrategyEnglishMorphology}
	wantCompletion := evidenceingestion.EvidenceQueryCompletionBoundedNoMatch
	if tc.Class == "english" {
		wantCompletion = evidenceingestion.EvidenceQueryCompletionMorphologyCandidates
	}
	if input.QueryMode == evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1 {
		wantMode, wantPlan, wantNormalizer = input.QueryMode, evidenceingestion.EvidenceQueryPlanExperimentalHanRecoveryV1, evidenceingestion.EvidenceQueryNormalizerExperimentalHanV1
		wantFallback = "baseline_hit_preserved"
		if tc.Class != "english" {
			wantStrategies = append(wantStrategies, evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms)
			wantFallback, wantRanking = "eligible", "created_at_desc,id_asc_unscored"
			wantCompletion = evidenceingestion.EvidenceQueryCompletionHanCandidates
		}
	}
	var strategies []string
	for _, attempt := range execution.Attempts {
		strategies = append(strategies, attempt.Strategy)
	}
	filters := evidenceingestion.EvidenceQueryFilters{SourceSnapshotID: input.SourceSnapshotID, RepositorySnapshotID: input.RepositorySnapshotID, SourceGenerationID: input.SourceGenerationID, SourceID: input.SourceID, SourceVersion: input.SourceVersion, AdmissionOutcome: input.AdmissionOutcome, LifecycleScope: input.LifecycleScope}
	if !slices.Equal(strategies, wantStrategies) || execution.QueryCount != len(wantStrategies) || execution.OriginalQuery != input.Query || execution.QueryMode != wantMode || execution.PlanVersion != wantPlan || execution.NormalizerVersion != wantNormalizer || execution.CompletionReason != wantCompletion || execution.FallbackStatus != wantFallback || execution.RankingPolicy != wantRanking || execution.SearchSurface != evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement || execution.Limit != input.Limit || execution.Filters != filters || !slices.Equal(execution.SearchedFields, []string{evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement}) || !slices.Equal(execution.SearchedRecordKinds, []string{"proposal_occurrence"}) || !slices.Equal(execution.EligibleSourceBindingKinds, []string{evidenceingestion.ProposalSourceBindingSourceSnapshot, evidenceingestion.ProposalSourceBindingRepositorySnapshot}) {
		return errors.New("fixed query execution plan or metadata changed")
	}
	return nil
}

// These are constant expressions, not arbitrary SQL. E strings make the
// backslash behavior independent of standard_conforming_strings.
func hanPlanLiteral(value any) (string, error) {
	switch value := value.(type) {
	case string:
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return "", errors.New("invalid diagnostic text literal")
		}
		quoted := strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", "''")
		return "E'" + quoted + "'::text", nil
	case int:
		if value < 1 || value > 101 {
			return "", errors.New("diagnostic limit is outside the fixed bound")
		}
		return strconv.Itoa(value) + "::bigint", nil
	case []string:
		if value == nil {
			return "", errors.New("nil diagnostic array is not an empty array")
		}
		var elements []string
		for _, element := range value {
			literal, err := hanPlanLiteral(element)
			if err != nil {
				return "", err
			}
			elements = append(elements, literal)
		}
		return "ARRAY[" + strings.Join(elements, ",") + "]::text[]", nil
	default:
		return "", errors.New("unsupported diagnostic literal type")
	}
}

func hanPlanExecute(spec hanScaleSQL, statement hanPlanPrepared, args []any) (string, error) {
	if statement.Name == "" || strings.IndexByte(statement.Name, 0) >= 0 || !utf8.ValidString(statement.Name) || statement.FromSQL || statement.SQLSHA256 != spec.SHA256 {
		return "", errors.New("live prepared statement does not match frozen source")
	}
	want := []uint32{25, 25, 25, 25, 25, 25, 25, 25, 20}
	if spec.Strategy == evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms {
		want = append(want, 1009)
	}
	if len(args) != len(want) || !slices.Equal(statement.ParameterOIDs, want) {
		return "", errors.New("live candidate parameter types changed")
	}
	var literals []string
	for i, arg := range args {
		if i < 8 {
			if _, ok := arg.(string); !ok {
				return "", errors.New("candidate text argument type changed")
			}
		}
		if i == 8 {
			if _, ok := arg.(int); !ok {
				return "", errors.New("candidate limit argument type changed")
			}
		}
		if i == 9 {
			if _, ok := arg.([]string); !ok {
				return "", errors.New("candidate Han array argument type changed")
			}
		}
		literal, err := hanPlanLiteral(arg)
		if err != nil {
			return "", err
		}
		literals = append(literals, literal)
	}
	return "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE " + pgx.Identifier{statement.Name}.Sanitize() + "(" + strings.Join(literals, ",") + ")", nil
}

func hanPlanConfig(socket, port string) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN") {
			return nil, errors.New("plan lab refuses inherited database settings")
		}
	}
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || socket == "/" || strings.ContainsAny(socket, "\x00\r\n") || port != "55443" {
		return nil, errors.New("plan lab requires an absolute Unix socket and fixed port 55443")
	}
	config, err := pgx.ParseConfig("host=/nonexistent port=1 user=ahe_han_plan_operator dbname=ahe_han_plan_lab password='' passfile=/dev/null sslmode=disable target_session_attrs=any connect_timeout=5")
	if err != nil {
		return nil, errors.New("parsing fixed plan lab configuration")
	}
	config.Host, config.Port, config.Fallbacks = socket, 55443, nil
	config.RuntimeParams = map[string]string{"application_name": "ahe_han_plan_lab", "statement_timeout": hanScaleSQLTimeout}
	return config, nil
}

func hanPlanCodeHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := hanQueryLabCodeHashes(t)
	files, err := os.ReadDir(filepath.Join(hanScaleRoot(t), "internal/dbrole"))
	if err != nil {
		t.Fatal("cannot enumerate role-policy code")
	}
	paths := []string{"internal/evidenceingestion/postgres.go", "internal/mcpintegration/han_scale_integration_test.go", "internal/mcpintegration/han_plan_integration_test.go"}
	for _, entry := range files {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			paths = append(paths, "internal/dbrole/"+entry.Name())
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(hanScaleRoot(t), path))
		if err != nil {
			t.Fatal("cannot freeze diagnostic code")
		}
		hashes[path] = hanPlanDigest(data)
	}
	return hashes
}

func TestHanPlanConfig(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN" {
			t.Setenv(name, "")
		}
	}
	config, err := hanPlanConfig("/private/tmp/han-plan-config-test", "55443")
	if err != nil || config.Database != "ahe_han_plan_lab" || config.User != "ahe_han_plan_operator" || config.Password != "" || config.TLSConfig != nil || config.Tracer != nil || len(config.Fallbacks) != 0 || config.RuntimeParams["statement_timeout"] != "2000" {
		t.Fatal("fixed plan configuration boundary changed")
	}
	for _, name := range []string{"PGHOST", "PGSERVICE", "PGPASSWORD", "DATABASE_DSN", "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "not-consumed")
			if config, err := hanPlanConfig("/private/tmp/han-plan-config-test", "55443"); err == nil || config != nil {
				t.Fatal("inherited setting accepted")
			}
		})
	}
	for _, bad := range [][2]string{{"localhost", "55443"}, {"/", "55443"}, {"/private/tmp/lab", "55442"}, {"/private/tmp/../tmp/lab", "55443"}} {
		if config, err := hanPlanConfig(bad[0], bad[1]); err == nil || config != nil {
			t.Fatal("unapproved destination accepted")
		}
	}
}

func TestHanPlanMatrixAndLiterals(t *testing.T) {
	blocks, cases := hanPlanBlocks(), hanPlanCases()
	if len(blocks) != 6 || len(cases) != 4 || len(blocks)*len(cases)*6*2 != 288 || len(blocks)*len(cases)*6 != 144 {
		t.Fatal("fixed block matrix changed")
	}
	if blocks[0].FreshPool || blocks[1].FreshPool || blocks[2].FreshPool || !blocks[3].FreshPool || !blocks[4].FreshPool || !blocks[5].FreshPool {
		t.Fatal("fixed pool history changed")
	}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{"O'Reilly", `E'O''Reilly'::text`}, {`a\b`, `E'a\\b'::text`}, {"'; SELECT 1; --", `E'''; SELECT 1; --'::text`}, {"巡檢", `E'巡檢'::text`}, {"", `E''::text`}, {11, `11::bigint`}, {[]string{}, `ARRAY[]::text[]`}, {[]string{"索引", "a'b"}, `ARRAY[E'索引'::text,E'a''b'::text]::text[]`},
	} {
		actual, err := hanPlanLiteral(tc.value)
		if err != nil || actual != tc.want {
			t.Fatal("safe literal construction mismatch")
		}
	}
	for _, bad := range []any{"\x00", string([]byte{0xff}), nil, []string(nil), []string{"\x00"}, float64(1), int64(1), 0, 102, map[string]string{}} {
		if _, err := hanPlanLiteral(bad); err == nil {
			t.Fatal("invalid diagnostic literal accepted")
		}
	}
	specs := hanScaleSQLPlan(t)
	input := hanScaleInput(cases[0], nil, nil)
	args, err := hanScaleArguments(specs[0], input, evidenceingestion.EvidenceQueryAttempt{})
	if err != nil {
		t.Fatal(err)
	}
	statement := hanPlanPrepared{Name: `live"statement`, SQLSHA256: specs[0].SHA256, ParameterOIDs: []uint32{25, 25, 25, 25, 25, 25, 25, 25, 20}}
	execute, err := hanPlanExecute(specs[0], statement, args)
	if err != nil || !strings.Contains(execute, `EXECUTE "live""statement"(`) {
		t.Fatal("live statement identifier was not quoted")
	}
	for _, change := range []func(*hanPlanPrepared){func(p *hanPlanPrepared) { p.Name = "" }, func(p *hanPlanPrepared) { p.SQLSHA256 = "different" }, func(p *hanPlanPrepared) { p.FromSQL = true }, func(p *hanPlanPrepared) { p.ParameterOIDs = nil }} {
		bad := statement
		change(&bad)
		if _, err := hanPlanExecute(specs[0], bad, args); err == nil {
			t.Fatal("unmatched live prepared statement accepted")
		}
	}
	if hashes := hanPlanCodeHashes(t); hashes["internal/dbrole/verify.go"] == "" || hashes["internal/dbrole/scope.go"] == "" || hashes["internal/evidenceingestion/postgres.go"] == "" {
		t.Fatal("guard/domain source hashes are incomplete")
	}
	baseline := []hanPlanGIN{{Index: "simple", Definition: "fixed one", DefinitionSHA256: "one"}, {Index: "english", Definition: "fixed two", DefinitionSHA256: "two"}}
	current := slices.Clone(baseline)
	current[0].PendingPages = 3
	current[1].Bytes = 9000
	if !hanPlanSameGINDefinitions(baseline, current) {
		t.Fatal("GIN pending/size changes must not be definition drift")
	}
	current[0].Options = []string{"fastupdate=off"}
	if hanPlanSameGINDefinitions(baseline, current) || hanPlanSameGINDefinitions(nil, current) {
		t.Fatal("GIN options or missing baseline did not fail closed")
	}
	fixed := evidenceingestion.EvidenceQueryExecution{OriginalQuery: input.Query, QueryMode: evidenceingestion.EvidenceQueryModeDeterministicLexicalRecovery, PlanVersion: evidenceingestion.EvidenceQueryPlanRecoveryV2, NormalizerVersion: evidenceingestion.EvidenceQueryNormalizerRecoveryV1, CompletionReason: evidenceingestion.EvidenceQueryCompletionBoundedNoMatch, SearchSurface: evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement, SearchedFields: []string{evidenceingestion.EvidenceQuerySearchSurfaceProposalStatement}, SearchedRecordKinds: []string{"proposal_occurrence"}, EligibleSourceBindingKinds: []string{evidenceingestion.ProposalSourceBindingSourceSnapshot, evidenceingestion.ProposalSourceBindingRepositorySnapshot}, Filters: evidenceingestion.EvidenceQueryFilters{AdmissionOutcome: "pending", LifecycleScope: "active"}, Limit: input.Limit, QueryCount: 2, Attempts: []evidenceingestion.EvidenceQueryAttempt{{Strategy: evidenceingestion.EvidenceQueryStrategyExactSimple}, {Strategy: evidenceingestion.EvidenceQueryStrategyEnglishMorphology}}}
	if err := hanPlanValidateExecution(input, cases[0], fixed); err != nil {
		t.Fatal("fixed baseline execution metadata rejected")
	}
	for _, change := range []func(*evidenceingestion.EvidenceQueryExecution){func(e *evidenceingestion.EvidenceQueryExecution) { e.Attempts = nil; e.QueryCount = 0 }, func(e *evidenceingestion.EvidenceQueryExecution) { e.PlanVersion = "unknown" }, func(e *evidenceingestion.EvidenceQueryExecution) { e.Filters.AdmissionOutcome = "admitted" }, func(e *evidenceingestion.EvidenceQueryExecution) { e.SearchSurface = "source_text" }} {
		bad := fixed
		change(&bad)
		if hanPlanValidateExecution(input, cases[0], bad) == nil {
			t.Fatal("missing or altered execution metadata accepted")
		}
	}
}

func TestIntegrationHanPlanIsolatedLab(t *testing.T) {
	socket, port := os.Getenv("AHE_HAN_PLAN_LAB_SOCKET"), os.Getenv("AHE_HAN_PLAN_LAB_PORT")
	if socket == "" && port == "" {
		t.Skip("set explicit AHE_HAN_PLAN_LAB_SOCKET/PORT for the disposable plan lab")
	}
	config, err := hanPlanConfig(socket, port)
	if err != nil {
		t.Fatal(err)
	}
	expectedIdentity := os.Getenv("AHE_HAN_PLAN_LAB_SYSTEM_IDENTIFIER")
	if _, err := strconv.ParseUint(expectedIdentity, 10, 64); err != nil || expectedIdentity == "0" {
		t.Fatal("plan lab requires a fixed PostgreSQL system identifier")
	}
	plan := hanPlanFrozen{Contract: "han-query-plan-v1", Blocks: hanPlanBlocks(), Cases: hanPlanCases(), PairOrder: []string{"AB", "BA", "AB", "BA", "AB", "BA"}, Corpus: hanScaleCorpus(50000), CandidateSQL: hanScaleSQLPlan(t), CodeSHA256: hanPlanCodeHashes(t), Limitations: []string{
		"Acquire duration ends at Acquire return and includes normal pool/session guards, queueing or ping; it is not pure ACL cost. Held-connection metadata and Release are recorded separately.",
		"Domain requests use the unchanged public API and restricted pool, including every normal acquire guard and hydration. Probe, maintenance reads, EXPLAIN and report serialization are excluded from domain walltime.",
		"This is not exact replay of the previous scale experiment: extra probes and maintenance reads affect time/cache history. first_in_case is not cold cache; five warm samples do not establish p95/p99.",
		"EXPLAIN EXECUTE uses a live prepared statement but obtains a plan again and may replan. Its own generic/custom counter increments are diagnostic, not domain activity; it is not a historical execution trace.",
		"Candidate-only EXPLAIN does not contain guards, normalizer or hydration. Domain minus Acquire or EXPLAIN is not an exact decomposition of runtime costs.",
		"pgstattuple is owner-only in a separate observer schema. Query authority is unchanged. No GIN cleanup, index alteration, autovacuum alteration, statistics reset or runtime change occurs.",
		"Blocks are sequential, not randomized replicas; forced cache modes are new-session diagnostic controls, not a change to default runtime behavior or proof of a sole cause.",
		"Only synthetic pending data is written through typed APIs. No admission, external source/model, original DB or Git operations. Timeouts and mismatches stop remaining work without retries.",
		"Observer snapshots bound multiple read-only catalog calls by timestamps, not a claim of an atomic GIN/statistics snapshot. Automatic maintenance can occur between reads.",
	}}
	plan.CorpusSHA256 = hanQueryLabJSONHash(t, plan.Corpus)
	report := hanPlanReport{Contract: plan.Contract, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: "preflight", PlannedDomain: 288, PlannedAcquire: 144}
	hanPlanReserve(t, plan, &report)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		hanPlanStop(t, &report, "connect", 0, "", "", 0, 0, err)
		return
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var listen string
	var extensions, extraSchemas, publicDatabase, publicSchema int
	err = admin.QueryRow(ctx, `SELECT current_database(),current_setting('listen_addresses'),current_setting('server_version_num')::integer,(SELECT system_identifier::text FROM pg_control_system()),(SELECT count(*) FROM pg_extension WHERE extname<>'plpgsql'),(SELECT count(*) FROM pg_namespace WHERE nspname NOT IN ('public','information_schema') AND nspname NOT LIKE 'pg_%'),(SELECT count(*) FROM pg_database d,LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname=current_database() AND a.grantee=0 AND a.privilege_type IN ('CREATE','TEMPORARY')),(SELECT count(*) FROM pg_namespace n,LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a WHERE n.nspname='public' AND a.grantee=0)`).Scan(&report.Database, &listen, &report.ServerVersion, &report.SystemIdentifier, &extensions, &extraSchemas, &publicDatabase, &publicSchema)
	if err != nil {
		hanPlanStop(t, &report, "identity", 0, "", "", 0, 0, err)
		return
	}
	if report.Database != "ahe_han_plan_lab" || report.SystemIdentifier != expectedIdentity || report.ServerVersion/10000 != 18 || listen != "" || extensions != 0 || extraSchemas != 0 || publicDatabase != 0 || publicSchema != 0 {
		t.Fatal("plan lab identity or empty authority preflight failed")
	}
	report.PermanentBefore = hanQueryLabPermanentTables(t, ctx, admin)
	if report.PermanentBefore != 0 {
		t.Fatal("plan lab must start empty")
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		report.PermanentAfter = hanQueryLabPermanentTables(t, cleanupCtx, admin)
		var schemaExists, observerExists, extensionExists bool
		var roles int
		if err := admin.QueryRow(cleanupCtx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1),EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$2),EXISTS(SELECT 1 FROM pg_extension WHERE extname='pgstattuple'),(SELECT count(*) FROM pg_roles WHERE rolname=ANY($3::text[]))`, report.Schema, hanPlanObserverSchema, report.RuntimeRoles).Scan(&schemaExists, &observerExists, &extensionExists, &roles); err != nil {
			t.Error("cannot verify plan fixture cleanup")
			return
		}
		report.SchemaCleanupVerified = report.Schema != "" && !schemaExists && report.PermanentAfter == report.PermanentBefore
		report.ObserverCleanupVerified = !observerExists && !extensionExists
		report.RolesCleanupVerified = len(report.RuntimeRoles) == 4 && roles == 0
		if report.Schema != "" && (!report.SchemaCleanupVerified || !report.ObserverCleanupVerified || !report.RolesCleanupVerified) {
			t.Error("plan fixture cleanup incomplete")
		}
	})
	report.Phase = "observer_setup"
	if err := hanPlanInstallObserver(ctx, admin); err != nil {
		hanPlanStop(t, &report, "observer_setup", 0, "", "", 0, 0, err)
		return
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanupCtx, "DROP EXTENSION pgstattuple"); err != nil {
			t.Error("cannot remove isolated observer extension")
			return
		}
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{hanPlanObserverSchema}.Sanitize()); err != nil {
			t.Error("cannot remove isolated observer schema")
		}
	})
	if err := admin.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname='pgstattuple'`).Scan(&report.ObserverExtensionVersion); err != nil {
		t.Fatal("cannot read observer extension version")
	}
	destination := url.URL{Scheme: "postgres", User: url.User(config.User), Path: "/" + config.Database}
	destination.RawQuery = url.Values{"host": {config.Host}, "port": {"55443"}, "sslmode": {"disable"}, "passfile": {"/dev/null"}, "connect_timeout": {"5"}}.Encode()
	report.Phase = "fixture_setup"
	fixture := newAuthorityProcessFixture(t, ctx, destination.String())
	report.Schema = fixture.schema
	for _, login := range []authorityProcessLogin{fixture.query, fixture.intake} {
		parsed, err := url.Parse(login.dsn)
		if err != nil || parsed.User == nil {
			t.Fatal("cannot identify private fixture role")
		}
		report.RuntimeRoles = append(report.RuntimeRoles, login.group, parsed.User.Username())
	}
	intake, _ := hanScaleRuntimePool(t, ctx, fixture.intake, fixture.schema)
	auto, binding := hanPlanRuntimePool(t, ctx, fixture.query, fixture.schema, "auto")
	observerConfig, err := pgxpool.ParseConfig(destination.String())
	if err != nil {
		t.Fatal("cannot configure observer pool")
	}
	observerConfig.MaxConns = 1
	observerConfig.ConnConfig.RuntimeParams = map[string]string{"search_path": fixture.schema, "statement_timeout": hanScaleSQLTimeout}
	observer, err := pgxpool.NewWithConfig(ctx, observerConfig)
	if err != nil {
		t.Fatal("cannot open observer pool")
	}
	t.Cleanup(observer.Close)
	var autoPID int32
	for _, block := range plan.Blocks {
		report.ActiveBlock, report.Phase = block.Name, "seed"
		t.Logf("plan block %s: preparing %d pending rows", block.Name, block.Rows)
		grew := len(report.Identities) < block.Rows
		for len(report.Identities) < block.Rows {
			start := len(report.Identities)
			started := time.Now()
			batch, identities, err := hanScaleSeed(ctx, intake, observer, plan.Corpus[start:start+hanScaleBatchSize], start/hanScaleBatchSize)
			if err != nil {
				hanPlanStop(t, &report, "seed", block.Rows, "", "", 0, time.Since(started).Microseconds(), err)
				return
			}
			report.Batches = append(report.Batches, batch)
			report.Identities = append(report.Identities, identities...)
			if start == 0 {
				readCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
				record, err := evidenceingestion.GetProposalByOccurrenceID(readCtx, auto, identities[0].ID)
				stop()
				if err != nil {
					hanPlanStop(t, &report, "initial_readback", block.Rows, "", "", 0, 0, err)
					return
				}
				if record.StatementText != plan.Corpus[0].Statement || record.AdmissionOutcome != "pending" || record.CanonicalRef != "" || len(record.SourceRefs) != 1 || record.SourceRefs[0].QuotedText != plan.Corpus[0].Statement || record.RawContentHash != batch.RawSHA256 {
					t.Fatal("first batch exact pending citation readback failed")
				}
				report.InitialReadback = &record
			}
		}
		if grew {
			report.Phase = "analyze"
			if err := hanPlanAnalyze(ctx, observer, fixture.schema); err != nil {
				hanPlanStop(t, &report, "analyze", block.Rows, "", "", 0, 0, err)
				return
			}
		}
		pool, currentBinding := auto, binding
		if block.FreshPool {
			pool, currentBinding = hanPlanRuntimePool(t, ctx, fixture.query, fixture.schema, block.CacheMode)
		}
		report.Blocks = append(report.Blocks, hanPlanBlockResult{Block: block, Phase: "before", Binding: currentBinding, PlannedDomain: 48, PlannedAcquire: 24})
		current := &report.Blocks[len(report.Blocks)-1]
		if !block.FreshPool {
			current.PID = autoPID
		}
		report.Phase = "block"
		if err := hanPlanRunBlock(t, ctx, fixture.schema, observer, pool, plan, &report, current); err != nil {
			if report.Failure == nil {
				hanPlanStop(t, &report, current.Phase, block.Rows, "", "", 0, 0, err)
			}
			return
		}
		if block.FreshPool {
			pool.Close()
		} else {
			autoPID = current.PID
		}
		t.Logf("plan block %s: 48 domain, 24 acquire and %d live-plan diagnostics complete", block.Name, len(current.Explains))
	}
	report.Phase = "stdio_smoke"
	login := fixture.query
	parsed, err := url.Parse(login.dsn)
	if err != nil {
		t.Fatal("cannot configure bounded stdio login")
	}
	values := parsed.Query()
	values.Set("statement_timeout", hanScaleSQLTimeout)
	parsed.RawQuery = values.Encode()
	login.dsn = parsed.String()
	process := startAuthorityProcess(t, ctx, "ahe-query-mcp", login, fixture.schema, "")
	report.LiveTools = hanQueryLabTools(t, process)
	sparse := hanScaleCases()[4]
	for _, mode := range []string{"", evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1} {
		response := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, process, "get_grounded_evidence_brief", evidencequerymcp.GetGroundedEvidenceBriefRequest{Query: sparse.Query, QueryMode: mode, AdmissionOutcome: "pending", Limit: sparse.Limit})
		report.StdioSmoke = append(report.StdioSmoke, response)
		var ids []string
		for _, match := range response.Matches {
			ids = append(ids, match.RecordRef.ID)
		}
		expected := hanScaleExpected(sparse, hanScaleInput(sparse, report.Identities, plan.Corpus), report.Identities, plan.Corpus, mode != "")
		if !slices.Equal(ids, expected[:min(len(expected), sparse.Limit)]) {
			t.Fatal("final stdio candidate oracle mismatch")
		}
	}
	process.finish(t)
	report.StdioEOF = true
	if !reflect.DeepEqual(plan.CodeSHA256, hanPlanCodeHashes(t)) {
		t.Fatal("frozen plan experiment code changed")
	}
	report.Phase = "complete"
}

func hanPlanInstallObserver(ctx context.Context, admin *pgx.Conn) error {
	tx, err := admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, sql := range []string{"CREATE SCHEMA " + pgx.Identifier{hanPlanObserverSchema}.Sanitize(), "CREATE EXTENSION pgstattuple WITH SCHEMA " + pgx.Identifier{hanPlanObserverSchema}.Sanitize(), "REVOKE ALL ON SCHEMA " + pgx.Identifier{hanPlanObserverSchema}.Sanitize() + " FROM PUBLIC, pg_stat_scan_tables", "REVOKE ALL ON ALL FUNCTIONS IN SCHEMA " + pgx.Identifier{hanPlanObserverSchema}.Sanitize() + " FROM PUBLIC, pg_stat_scan_tables"} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func hanPlanRuntimePool(t *testing.T, ctx context.Context, login authorityProcessLogin, schema, mode string) (*pgxpool.Pool, dbrole.RuntimeBinding) {
	t.Helper()
	config, err := pgxpool.ParseConfig(login.dsn)
	if err != nil {
		t.Fatal("cannot configure bounded plan runtime pool")
	}
	if mode != "auto" && mode != "force_custom_plan" && mode != "force_generic_plan" {
		t.Fatal("unplanned runtime cache mode")
	}
	config.MaxConns, config.MinConns = 1, 1
	config.ConnConfig.RuntimeParams["statement_timeout"] = hanScaleSQLTimeout
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = mode
	pool, binding, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: login.group, Schema: schema, Profile: login.profile})
	if err != nil {
		t.Fatal("cannot open closed plan runtime pool")
	}
	t.Cleanup(pool.Close)
	return pool, binding
}

func hanPlanAnalyze(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=$1 ORDER BY tablename`, schema)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	if len(names) != 78 {
		return errors.New("unexpected full schema table count")
	}
	for _, name := range names {
		if _, err := pool.Exec(ctx, "ANALYZE "+pgx.Identifier{schema, name}.Sanitize()); err != nil {
			return err
		}
	}
	return nil
}

func hanPlanObserve(ctx context.Context, pool *pgxpool.Pool, schema string) (snapshot hanPlanSnapshot, resultErr error) {
	snapshot = hanPlanSnapshot{At: time.Now().UTC().Format(time.RFC3339Nano)}
	defer func() { snapshot.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano) }()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return snapshot, err
	}
	defer conn.Release()
	// This clears only this observer's cached statistics snapshot, never the
	// server counters. No maintenance operation is requested.
	if _, err := conn.Exec(ctx, `SELECT pg_stat_clear_snapshot()`, pgx.QueryExecModeExec); err != nil {
		return snapshot, err
	}
	for _, index := range []string{"proposal_occurrences_statement_search_idx", "proposal_occurrences_statement_english_search_idx"} {
		item := hanPlanGIN{Index: index}
		var definition string
		sql := `SELECT pg_relation_size(c.oid),COALESCE(c.reloptions,ARRAY[]::text[]),pg_get_indexdef(c.oid),g.pending_pages,g.pending_tuples FROM pg_class c CROSS JOIN LATERAL ` + pgx.Identifier{hanPlanObserverSchema, "pgstatginindex"}.Sanitize() + `(c.oid::regclass) g WHERE c.oid=$1::regclass`
		if err := conn.QueryRow(ctx, sql, pgx.QueryExecModeExec, pgx.Identifier{schema, index}.Sanitize()).Scan(&item.Bytes, &item.Options, &definition, &item.PendingPages, &item.PendingTuples); err != nil {
			return snapshot, err
		}
		item.DefinitionSHA256 = hanPlanDigest([]byte(definition))
		item.Definition = definition
		snapshot.GIN = append(snapshot.GIN, item)
	}
	m := &snapshot.Maintenance
	err = conn.QueryRow(ctx, `SELECT last_vacuum,last_autovacuum,last_analyze,last_autoanalyze,vacuum_count,autovacuum_count,analyze_count,autoanalyze_count,n_live_tup,n_dead_tup,n_mod_since_analyze FROM pg_stat_user_tables WHERE schemaname=$1 AND relname='proposal_occurrences'`, pgx.QueryExecModeExec, schema).Scan(&m.LastVacuum, &m.LastAutoVacuum, &m.LastAnalyze, &m.LastAutoAnalyze, &m.VacuumCount, &m.AutoVacuumCount, &m.AnalyzeCount, &m.AutoAnalyzeCount, &m.LiveTuples, &m.DeadTuples, &m.ModifiedSinceAnalyze)
	return snapshot, err
}

func hanPlanObserveBoundary(t *testing.T, ctx context.Context, observer *pgxpool.Pool, schema string, report *hanPlanReport, block *hanPlanBlockResult, phase, caseName string, repeat int) (hanPlanSnapshot, error) {
	snapshot, err := hanPlanObserve(ctx, observer, schema)
	if err == nil {
		if report.GINBaseline == nil {
			report.GINBaseline = slices.Clone(snapshot.GIN)
		}
		if !hanPlanSameGINDefinitions(report.GINBaseline, snapshot.GIN) {
			err = errors.New("GIN definition or options changed")
		}
	}
	if err != nil {
		report.ObserverFailure = &hanPlanObserverFailure{Phase: phase, Case: caseName, Repeat: repeat, Snapshot: snapshot}
		block.Phase = phase
		hanPlanStop(t, report, phase, block.Block.Rows, caseName, "", repeat, 0, err)
	}
	return snapshot, err
}

func hanPlanPreparedStatements(ctx context.Context, conn *pgxpool.Conn) ([]hanPlanPrepared, map[string]string, error) {
	rows, err := conn.Query(ctx, `SELECT name,statement,prepare_time,parameter_types::oid[],from_sql,generic_plans,custom_plans FROM pg_prepared_statements ORDER BY name`, pgx.QueryExecModeExec)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var result []hanPlanPrepared
	sqlByName := make(map[string]string)
	for rows.Next() {
		var item hanPlanPrepared
		var sql string
		if err := rows.Scan(&item.Name, &sql, &item.PrepareTime, &item.ParameterOIDs, &item.FromSQL, &item.GenericPlans, &item.CustomPlans); err != nil {
			return nil, nil, err
		}
		item.SQLSHA256 = hanPlanDigest([]byte(sql))
		result = append(result, item)
		sqlByName[item.Name] = sql
	}
	return result, sqlByName, rows.Err()
}

func hanPlanHeldMetadata(ctx context.Context, conn *pgxpool.Conn) (int32, map[string]string, error) {
	var pid int32
	if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`, pgx.QueryExecModeExec).Scan(&pid); err != nil {
		return 0, nil, err
	}
	rows, err := conn.Query(ctx, `SELECT name,setting,COALESCE(unit,'') FROM pg_settings WHERE name=ANY($1::text[]) ORDER BY name`, pgx.QueryExecModeExec, []string{"server_version", "server_encoding", "statement_timeout", "plan_cache_mode", "shared_buffers", "work_mem", "effective_cache_size", "jit", "max_parallel_workers_per_gather", "track_io_timing", "random_page_cost", "cpu_tuple_cost", "gin_pending_list_limit", "autovacuum"})
	if err != nil {
		return pid, nil, err
	}
	defer rows.Close()
	settings := make(map[string]string)
	for rows.Next() {
		var name, value, unit string
		if err := rows.Scan(&name, &value, &unit); err != nil {
			return pid, nil, err
		}
		settings[name] = value + " " + unit
	}
	return pid, settings, rows.Err()
}

func hanPlanRunBlock(t *testing.T, ctx context.Context, schema string, observer, query *pgxpool.Pool, plan hanPlanFrozen, report *hanPlanReport, block *hanPlanBlockResult) (resultErr error) {
	t.Helper()
	defer func() {
		originalPhase := block.Phase
		completed := block.Phase == "complete" && resultErr == nil
		block.Passed = false
		block.AttemptedDomain, block.AttemptedAcquire = len(block.Domain), len(block.Acquires)
		block.NotRunDomain = block.PlannedDomain - block.AttemptedDomain
		block.NotRunAcquire = block.PlannedAcquire - block.AttemptedAcquire
		if block.BeforeTables != nil {
			block.Phase = "after_digest"
			cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			block.AfterTables = hanQueryLabTableDigests(t, cleanupCtx, observer, schema)
			if !reflect.DeepEqual(block.BeforeTables, block.AfterTables) {
				resultErr = errors.New("domain table contents changed during diagnostic block")
				block.Passed = false
				hanPlanStop(t, report, "after_digest", block.Block.Rows, "", "", 0, 0, resultErr)
			} else {
				block.Phase = originalPhase
			}
		}
		if completed && resultErr == nil && !t.Failed() {
			block.Phase = "complete"
			block.Passed = true
		}
	}()
	var err error
	block.Before, err = hanPlanObserveBoundary(t, ctx, observer, schema, report, block, "observer_before_block", "", 0)
	if err != nil {
		return err
	}
	block.BeforeTables = hanQueryLabTableDigests(t, ctx, observer, schema)
	if len(block.BeforeTables) != 78 || block.BeforeTables["proposal_occurrences"].Rows != block.Block.Rows || block.BeforeTables["canonical_graph_nodes"].Rows != 0 || block.BeforeTables["canonical_graph_edges"].Rows != 0 || block.BeforeTables["admission_decisions"].Rows != 0 {
		return errors.New("diagnostic block violates pending-only table boundaries")
	}
	block.Phase = "timing"
	for _, tc := range plan.Cases {
		base := hanScaleInput(tc, report.Identities, plan.Corpus)
		for repeat, order := range plan.PairOrder {
			probe := hanPlanAcquire{Case: tc.Name, Phase: "warm", Repeat: repeat}
			if repeat == 0 {
				probe.Phase = "first_in_case"
			}
			probe.Before, err = hanPlanObserveBoundary(t, ctx, observer, schema, report, block, "observer_before", tc.Name, repeat)
			if err != nil {
				return err
			}
			acquireCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
			started := time.Now()
			conn, acquireErr := query.Acquire(acquireCtx)
			acquired := time.Now()
			probe.ElapsedMicros = acquired.Sub(started).Microseconds()
			probe.StartedAt = started.UTC().Format(time.RFC3339Nano)
			probe.AcquiredAt = acquired.UTC().Format(time.RFC3339Nano)
			probe.FinishedAt = probe.AcquiredAt
			if acquireErr == nil {
				var settings map[string]string
				probe.PID, settings, acquireErr = hanPlanHeldMetadata(acquireCtx, conn)
				if acquireErr == nil {
					probe.Prepared, _, acquireErr = hanPlanPreparedStatements(acquireCtx, conn)
				}
				if block.PID == 0 {
					block.PID = probe.PID
				}
				if block.Settings == nil {
					block.Settings = settings
				}
				if acquireErr == nil && (probe.PID != block.PID || settings["plan_cache_mode"] != block.Block.CacheMode+" " || settings["statement_timeout"] != "2000 ms") {
					acquireErr = errors.New("query session identity or settings changed")
				}
				released := time.Now()
				conn.Release()
				probe.ReleaseMicros = time.Since(released).Microseconds()
				probe.ReleasedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			stop()
			probe.Passed = acquireErr == nil
			block.Acquires = append(block.Acquires, probe)
			probeIndex := len(block.Acquires) - 1
			if acquireErr != nil {
				hanPlanStop(t, report, "acquire", block.Block.Rows, tc.Name, "", repeat, probe.ElapsedMicros, acquireErr)
				return acquireErr
			}
			for _, mode := range order {
				input := base
				if mode == 'B' {
					input.QueryMode = evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1
				}
				expected := hanScaleExpected(tc, input, report.Identities, plan.Corpus, mode == 'B')
				observation := hanPlanDomain{hanScaleObservation: hanScaleObservation{Case: tc.Name, Mode: string(mode), Phase: probe.Phase, Repeat: repeat, Input: input, FullCandidateCardinality: len(expected), ExpectedIDs: slices.Clone(expected[:min(len(expected), tc.Limit)])}}
				requestCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
				started := time.Now()
				result, callErr := evidenceingestion.GetGroundedEvidenceBrief(requestCtx, query, input)
				finished := time.Now()
				observation.ElapsedMicros = finished.Sub(started).Microseconds()
				observation.StartedAt, observation.FinishedAt = started.UTC().Format(time.RFC3339Nano), finished.UTC().Format(time.RFC3339Nano)
				stop()
				observation.Execution = result.Execution
				observation.Exploratory100msWarning = observation.ElapsedMicros > 100000
				for _, match := range result.Matches {
					observation.ReturnedIDs = append(observation.ReturnedIDs, match.Record.ProposalOccurrenceID)
				}
				if callErr == nil {
					callErr = hanPlanValidateExecution(input, tc, result.Execution)
					truncated := len(expected) > tc.Limit
					if !slices.Equal(observation.ExpectedIDs, observation.ReturnedIDs) || result.Execution.Truncated != truncated || result.Execution.GlobalAbsenceInferenceAllowed || result.Execution.CandidateCount != len(result.Matches) || result.Execution.QueryCount != len(result.Execution.Attempts) || result.Execution.SearchCompleteWithinSurface == truncated {
						callErr = errors.New("candidate oracle or bounded execution mismatch")
					}
					for _, match := range result.Matches {
						if match.Record.AdmissionOutcome != "pending" || match.Record.CanonicalRef != "" {
							callErr = errors.New("pending authority boundary changed")
						}
						if mode == 'B' && tc.Class != "english" && match.Rank != 0 {
							callErr = errors.New("Han candidates must remain unscored")
						}
					}
				}
				observation.Passed = callErr == nil
				block.Domain = append(block.Domain, observation)
				if callErr != nil {
					hanPlanStop(t, report, "domain", block.Block.Rows, tc.Name, string(mode), repeat, observation.ElapsedMicros, callErr)
					return callErr
				}
			}
			block.Acquires[probeIndex].After, err = hanPlanObserveBoundary(t, ctx, observer, schema, report, block, "observer_after", tc.Name, repeat)
			if err != nil {
				return err
			}
		}
	}
	t.Logf("plan block %s: 48 domain and 24 acquire measurements complete", block.Block.Name)
	block.Phase = "live_explain"
	conn, err := query.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	pid, _, err := hanPlanHeldMetadata(ctx, conn)
	if err != nil {
		return err
	}
	if pid != block.PID {
		return errors.New("query backend changed before live diagnostics")
	}
	prepared, sqlByName, err := hanPlanPreparedStatements(ctx, conn)
	if err != nil {
		return err
	}
	block.PreparedBeforeExplain = prepared
	seen := make(map[string]bool)
	for _, observation := range block.Domain {
		if observation.Repeat != 0 {
			continue
		}
		for _, attempt := range observation.Execution.Attempts {
			var spec *hanScaleSQL
			for i := range plan.CandidateSQL {
				if plan.CandidateSQL[i].Strategy == attempt.Strategy {
					spec = &plan.CandidateSQL[i]
					break
				}
			}
			if spec == nil {
				return errors.New("candidate strategy lacks frozen SQL")
			}
			args, err := hanScaleArguments(*spec, observation.Input, attempt)
			if err != nil {
				return err
			}
			key := hanQueryLabJSONHash(t, struct {
				Case, Strategy string
				Args           []any
			}{observation.Case, attempt.Strategy, args})
			if seen[key] {
				continue
			}
			seen[key] = true
			block.Explains = append(block.Explains, hanPlanExplain{Case: observation.Case, Mode: observation.Mode, Strategy: attempt.Strategy, SQLSHA256: spec.SHA256, Arguments: args})
			diagnostic := &block.Explains[len(block.Explains)-1]
			var live []hanPlanPrepared
			for _, statement := range prepared {
				if sqlByName[statement.Name] == spec.SQL {
					live = append(live, statement)
				}
			}
			if len(live) != 1 {
				return errors.New("exact live candidate prepared statement missing or ambiguous")
			}
			diagnostic.StatementName = live[0].Name
			sql, err := hanPlanExecute(*spec, live[0], args)
			if err != nil {
				return err
			}
			diagnostic.Before, _, err = hanPlanPreparedStatements(ctx, conn)
			if err != nil {
				return err
			}
			explainCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
			started := time.Now()
			explainErr := conn.QueryRow(explainCtx, sql, pgx.QueryExecModeExec).Scan(&diagnostic.Plan)
			diagnostic.ElapsedMicros = time.Since(started).Microseconds()
			stop()
			if explainErr == nil {
				diagnostic.After, _, explainErr = hanPlanPreparedStatements(ctx, conn)
			}
			diagnostic.Passed = explainErr == nil
			if explainErr != nil {
				hanPlanStop(t, report, "live_explain", block.Block.Rows, observation.Case, observation.Mode, 0, diagnostic.ElapsedMicros, explainErr)
				return explainErr
			}
		}
	}
	block.PreparedAfterExplain, _, err = hanPlanPreparedStatements(ctx, conn)
	if err != nil {
		return err
	}
	block.After, err = hanPlanObserveBoundary(t, ctx, observer, schema, report, block, "observer_after_block", "", 0)
	if err != nil {
		return err
	}
	block.Phase = "complete"
	block.Passed = true
	return nil
}

func hanPlanStop(t *testing.T, report *hanPlanReport, phase string, size int, caseName, mode string, repeat int, elapsed int64, err error) {
	t.Helper()
	if report.Failure != nil {
		return
	}
	failure := hanScaleFailure{Phase: phase, StageRows: size, Case: caseName, Mode: mode, Repeat: repeat, ElapsedMicros: elapsed, Class: "operation_error"}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		failure.SQLState = postgresError.Code
		failure.Class = "postgres_error"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		failure.Class = "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		failure.Class = "context_canceled"
	}
	failure.Timeout = failure.SQLState == "57014" || errors.Is(err, context.DeadlineExceeded)
	report.Failure = &failure
	report.Phase = "stopped"
	t.Errorf("plan experiment stopped in %s/%s; class=%s SQLSTATE=%s", report.ActiveBlock, phase, failure.Class, failure.SQLState)
}

func hanPlanReserve(t *testing.T, plan hanPlanFrozen, report *hanPlanReport) {
	t.Helper()
	path := os.Getenv("AHE_HAN_PLAN_LAB_REPORT")
	if !filepath.IsAbs(path) {
		t.Fatal("plan experiment requires an absolute private report path")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively reserve plan report")
	}
	t.Cleanup(func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		for _, block := range report.Blocks {
			report.AttemptedDomain += len(block.Domain)
			report.AttemptedAcquire += len(block.Acquires)
		}
		report.NotRunDomain = report.PlannedDomain - report.AttemptedDomain
		report.NotRunAcquire = report.PlannedAcquire - report.AttemptedAcquire
		report.Passed = !t.Failed() && report.Phase == "complete" && report.SchemaCleanupVerified && report.ObserverCleanupVerified && report.RolesCleanupVerified && report.StdioEOF
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			t.Error("cannot save private plan report")
		}
		if err := file.Close(); err != nil {
			t.Error("cannot close private plan report")
		}
	})
	report.PlanFile = path + ".plan.json"
	planFile, err := os.OpenFile(report.PlanFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively create frozen plan")
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		_ = planFile.Close()
		t.Fatal("cannot encode fixed diagnostic plan")
	}
	report.PlanSHA256 = hanPlanDigest(data)
	_, writeErr := planFile.Write(data)
	closeErr := planFile.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("cannot save diagnostic plan before DB operations")
	}
}
