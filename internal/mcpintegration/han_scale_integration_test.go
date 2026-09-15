//go:build integration

package mcpintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	hanScaleBatchSize      = 100
	hanScaleSQLTimeout     = "2000"
	hanScaleRequestTimeout = 5 * time.Second
)

type hanScaleRow struct {
	Index     int
	Class     string
	Long      bool
	Statement string
}

type hanScaleCase struct {
	Name, Query, Class, Scope string
	Limit                     int
}

type hanScaleSQL struct {
	Strategy, File, Function, SQL, SHA256 string
	ArgumentExpressions                   []string
}

type hanScalePlan struct {
	Contract                   string
	StageRows                  []int
	BatchSize, WarmRepetitions int
	PairOrder                  []string
	Corpus                     []hanScaleRow
	CorpusSHA256               string
	Cases                      []hanScaleCase
	CandidateSQL               []hanScaleSQL
	CodeSHA256                 map[string]string
	Limitations                []string
}

type hanScaleIdentity struct {
	Index                          int
	ID, SourceID, SourceSnapshotID string
	CreatedAt                      time.Time
}

type hanScaleBatch struct {
	FirstIndex, LastIndex                                                                 int
	SourceID, Version, SourceSnapshotID, ExtractionViewID, ExtractionAttemptID, RawSHA256 string
}

type hanScaleObservation struct {
	Case, Mode, Phase        string
	Repeat                   int
	Input                    evidenceingestion.GroundedEvidenceBriefInput
	ElapsedMicros            int64
	Exploratory100msWarning  bool
	FullCandidateCardinality int
	ExpectedIDs, ReturnedIDs []string
	Execution                evidenceingestion.EvidenceQueryExecution
	Passed                   bool
}

type hanScaleDiagnostic struct {
	Case, Mode, Strategy, SQL, SQLSHA256 string
	Arguments                            []any
	ElapsedMicros                        int64
	Plan                                 json.RawMessage
}

type hanScalePrepared struct {
	StatementSHA256           string
	GenericPlans, CustomPlans int64
}

type hanScaleStage struct {
	Rows                                            int
	Phase                                           string
	PlannedSamples, AttemptedSamples, NotRunSamples int
	Settings                                        map[string]string
	RelationBytes                                   map[string]int64
	Before, After                                   map[string]hanQueryLabTableState
	Observations                                    []hanScaleObservation
	Diagnostics                                     []hanScaleDiagnostic
	PreparedBefore, PreparedAfter                   []hanScalePrepared
	LiveTools                                       json.RawMessage
	StdioSmoke                                      []evidencequerymcp.GroundedEvidenceBriefResponse
	StdioEOF, Passed                                bool
}

type hanScaleFailure struct {
	Phase, Case, Mode, SQLState, Class string
	StageRows, Repeat                  int
	ElapsedMicros                      int64
	Timeout                            bool
}

type hanScaleReport struct {
	Contract, StartedAt, FinishedAt, Phase, PlanFile, PlanSHA256 string
	Database, SystemIdentifier, Schema                           string
	ServerVersion                                                int
	PermanentTablesBefore, PermanentTablesAfterCleanup           int
	SchemaCleanupVerified, Passed                                bool
	PlannedSamples, AttemptedSamples, NotRunSamples              int
	QueryBinding, IntakeBinding                                  dbrole.RuntimeBinding
	InitialReadback                                              *evidenceingestion.ProposalQueryResult
	Batches                                                      []hanScaleBatch
	Identities                                                   []hanScaleIdentity
	Stages                                                       []hanScaleStage
	Failure                                                      *hanScaleFailure
}

func hanScaleCases() []hanScaleCase {
	return []hanScaleCase{
		{Name: "dense-two-limit-1", Query: "巡檢", Class: "dense2", Limit: 1},
		{Name: "dense-two-limit-10", Query: "巡檢", Class: "dense2", Limit: 10},
		{Name: "dense-two-limit-100", Query: "巡檢", Class: "dense2", Limit: 100},
		{Name: "dense-four", Query: "索引重建", Class: "dense4", Limit: 100},
		{Name: "sparse-han", Query: "極光異常", Class: "sparse", Limit: 10},
		{Name: "rare-ascii-han", Query: "ZephyrQuartz 稀有觀測", Class: "mixed", Limit: 10},
		{Name: "dense-two-source", Query: "巡檢", Class: "dense2", Scope: "lane03", Limit: 100},
		{Name: "dense-four-snapshot", Query: "索引重建", Class: "dense4", Scope: "first_dense4_snapshot", Limit: 10},
		{Name: "english-control", Query: "Nebula relay", Class: "english", Limit: 100},
		{Name: "no-match", Query: "海底熔岩", Class: "absent", Limit: 10},
		{Name: "long-only", Query: "長文", Class: "long", Limit: 100},
	}
}

func hanScaleCorpus(size int) []hanScaleRow {
	const suffix = " The operator preserves this synthetic local observation for later review and makes no claim about canonical evidence."
	const appendix = " 長文附錄保留完整觀測脈絡供後續閱讀。 The observer describes the local test area and keeps the original wording available beside the extracted statement. The maintenance desk has not approved any change to the evidence database, and this entry remains a pending source-backed candidate. The recording team separates collection from interpretation so that another reader can inspect the context without assuming that a search result establishes truth. A reviewer may compare this entry with other records, but the synthetic exercise supplies no external measurements and does not identify a real service incident. The operator records the relevant boundaries in complete sentences and retains each source line as an exact quotation. This appendix adds readable context to the same observation; it does not introduce an additional independent event or a decision about admission. The retained text is intentionally longer than the ordinary fixture entries, and its presence also enlarges stored source and extractor payloads."
	rows := make([]hanScaleRow, 0, size)
	for index := 1; index <= size; index++ {
		position := (index - 1) % 1000
		row := hanScaleRow{Index: index, Long: index%10 == 0}
		var lead string
		switch {
		case position == 0:
			row.Class, lead = "sparse", "Local observer %06d recorded 北岸極光異常事件尚待查核。"
		case position == 1:
			row.Class, lead = "mixed", "ZephyrQuartz observer %06d recorded 北岸稀有觀測事件尚待查核。"
		case position%100 < 50:
			row.Class, lead = "dense2", "Local observer %06d recorded 設備巡檢工作持續進行。"
		case position%100 < 70:
			row.Class, lead = "dense4", "Local observer %06d recorded 搜尋索引重建工作尚未開始。"
		case position%100 < 80:
			row.Class, lead = "english", "Nebula relay check %06d reports stable power."
		default:
			row.Class, lead = "filler", "Local observer %06d reports a routine desk review."
		}
		row.Statement = fmt.Sprintf(lead, index) + suffix
		if row.Long {
			row.Statement += appendix
		}
		rows = append(rows, row)
	}
	return rows
}

func hanScaleBatchSource(rows []hanScaleRow) []byte {
	var raw strings.Builder
	for _, row := range rows {
		raw.WriteString(row.Statement)
		raw.WriteByte('\n')
	}
	return []byte(raw.String())
}

func hanScaleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot identify scale test source root")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// Extract the source literal and verify every argument expression. This is
// deliberately not an execution tracer or a reconstruction of a cached plan.
func hanScaleExtractSQL(root, file, function, strategy string, want []string) (hanScaleSQL, error) {
	set := token.NewFileSet()
	source, err := parser.ParseFile(set, filepath.Join(root, file), nil, 0)
	if err != nil {
		return hanScaleSQL{}, fmt.Errorf("parsing frozen candidate source")
	}
	var calls []*ast.CallExpr
	for _, declaration := range source.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "query" {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if ok && receiver.Name == "db" {
				calls = append(calls, call)
			}
			return true
		})
	}
	if len(calls) != 1 || len(calls[0].Args) != len(want)+2 {
		return hanScaleSQL{}, fmt.Errorf("candidate function must have one exact db.query signature")
	}
	call := calls[0]
	literal, ok := call.Args[1].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return hanScaleSQL{}, fmt.Errorf("candidate SQL must remain a source string literal")
	}
	sql, err := strconv.Unquote(literal.Value)
	if err != nil {
		return hanScaleSQL{}, fmt.Errorf("decoding candidate SQL literal")
	}
	var contextExpression bytes.Buffer
	if err := format.Node(&contextExpression, set, call.Args[0]); err != nil || contextExpression.String() != "ctx" {
		return hanScaleSQL{}, fmt.Errorf("candidate query context expression changed")
	}
	var expressions []string
	for _, expression := range call.Args[2:] {
		var text bytes.Buffer
		if err := format.Node(&text, set, expression); err != nil {
			return hanScaleSQL{}, fmt.Errorf("formatting candidate argument expression")
		}
		expressions = append(expressions, text.String())
	}
	if !slices.Equal(expressions, want) {
		return hanScaleSQL{}, fmt.Errorf("candidate argument mapping changed")
	}
	return hanScaleSQL{Strategy: strategy, File: file, Function: function, SQL: sql, SHA256: strings.TrimPrefix(stdioContentHash([]byte(sql)), "sha256:"), ArgumentExpressions: expressions}, nil
}

func hanScaleSQLPlan(t *testing.T) []hanScaleSQL {
	t.Helper()
	ordinary := []string{"input.Query", "input.SourceSnapshotID", "input.RepositorySnapshotID", "input.SourceID", "input.SourceVersion", "input.AdmissionOutcome", "input.SourceGenerationID", "input.LifecycleScope", "limit"}
	han := slices.Clone(ordinary)
	han[0] = "asciiQuery"
	han = append(han, "han")
	var result []hanScaleSQL
	for _, target := range []struct {
		file, function, strategy string
		args                     []string
	}{
		{"internal/evidenceingestion/query_records.go", "queryProposalSearchResults", evidenceingestion.EvidenceQueryStrategyExactSimple, ordinary},
		{"internal/evidenceingestion/query_recovery.go", "queryEnglishMorphologyProposalSearchResults", evidenceingestion.EvidenceQueryStrategyEnglishMorphology, ordinary},
		{"internal/evidenceingestion/query_han_recovery.go", "queryHanLiteralProposalSearchResults", evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms, han},
	} {
		specification, err := hanScaleExtractSQL(hanScaleRoot(t), target.file, target.function, target.strategy, target.args)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, specification)
	}
	return result
}

func hanScaleArguments(specification hanScaleSQL, input evidenceingestion.GroundedEvidenceBriefInput, attempt evidenceingestion.EvidenceQueryAttempt) ([]any, error) {
	first := input.Query
	var han []string
	switch specification.Strategy {
	case evidenceingestion.EvidenceQueryStrategyExactSimple, evidenceingestion.EvidenceQueryStrategyEnglishMorphology:
	case evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms:
		var compiled struct {
			HanLiteralTerms    []string `json:"han_literal_terms"`
			ASCIISimpleTSQuery string   `json:"ascii_simple_tsquery"`
		}
		if attempt.Strategy != specification.Strategy || json.Unmarshal([]byte(attempt.CompiledQuery), &compiled) != nil || len(compiled.HanLiteralTerms) == 0 {
			return nil, fmt.Errorf("missing authoritative Han compiled query")
		}
		first, han = compiled.ASCIISimpleTSQuery, compiled.HanLiteralTerms
	default:
		return nil, fmt.Errorf("unplanned candidate strategy")
	}
	args := []any{first, input.SourceSnapshotID, input.RepositorySnapshotID, input.SourceID, input.SourceVersion, input.AdmissionOutcome, input.SourceGenerationID, input.LifecycleScope, input.Limit + 1}
	if han != nil {
		args = append(args, han)
	}
	return args, nil
}

func hanScaleConfig(socket, port string) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN") {
			return nil, fmt.Errorf("scale lab refuses inherited database environment settings")
		}
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || socket == "/" || strings.ContainsAny(socket, "\x00\r\n") || err != nil || number == 0 {
		return nil, fmt.Errorf("scale lab requires an absolute Unix socket directory and explicit port")
	}
	config, err := pgx.ParseConfig("host=/nonexistent port=1 user=ahe_han_scale_operator dbname=ahe_han_scale_lab password='' passfile=/dev/null sslmode=disable target_session_attrs=any connect_timeout=5")
	if err != nil {
		return nil, fmt.Errorf("parsing fixed scale lab configuration")
	}
	config.Host, config.Port, config.Fallbacks = socket, uint16(number), nil
	config.RuntimeParams = map[string]string{"application_name": "ahe_han_scale_lab", "statement_timeout": hanScaleSQLTimeout}
	return config, nil
}

func TestHanScalePlan(t *testing.T) {
	corpus := hanScaleCorpus(50000)
	want := map[string]int{"sparse": 50, "mixed": 50, "dense2": 24900, "dense4": 10000, "english": 5000, "filler": 10000}
	counts := make(map[string]int)
	long := 0
	for _, row := range corpus {
		counts[row.Class]++
		if !utf8.ValidString(row.Statement) || strings.ContainsAny(row.Statement, "\r\n") || len(row.Statement) > 1300 {
			t.Fatal("source row violates the fixed UTF-8 single-span bound")
		}
		if row.Long {
			long++
			if len(row.Statement) < 950 || !strings.Contains(row.Statement, "長文") {
				t.Fatal("long source fixture is not readable-sized or searchable")
			}
		} else if len(row.Statement) < 100 || len(row.Statement) > 200 || strings.Contains(row.Statement, "長文") {
			t.Fatal("short source fixture violates the fixed 100-200 byte range")
		}
	}
	if !reflect.DeepEqual(counts, want) || long != 5000 {
		t.Fatalf("unexpected corpus class counts: %v, long %d", counts, long)
	}
	for start := 0; start < len(corpus); start += hanScaleBatchSize {
		raw := hanScaleBatchSource(corpus[start : start+hanScaleBatchSize])
		if len(raw) > 1<<20 || bytes.Count(raw, []byte{'\n'}) != 100 {
			t.Fatal("source batch exceeded source-view byte or line bounds")
		}
	}
	if len(hanScaleCases()) != 11 || !reflect.DeepEqual(corpus[:1000], hanScaleCorpus(1000)) {
		t.Fatal("case count or cumulative source prefix changed")
	}
	var identities []hanScaleIdentity
	for _, row := range corpus[:1000] {
		identities = append(identities, hanScaleIdentity{Index: row.Index, ID: fmt.Sprintf("p:%06d", row.Index), SourceID: fmt.Sprintf("han-scale:lane%02d", (row.Index-1)/100%10), SourceSnapshotID: fmt.Sprintf("srcsnap:%d", (row.Index-1)/100), CreatedAt: time.Unix(int64((row.Index-1)/100), 0)})
	}
	for _, tc := range hanScaleCases() {
		if len(strings.Fields(tc.Query)) > 2 {
			t.Fatal("fixed scale cases must not require a three-term recovery strategy")
		}
		input := hanScaleInput(tc, identities, corpus)
		selected := hanScaleExpected(tc, input, identities, corpus, true)
		if tc.Class == "absent" && len(selected) != 0 || tc.Class != "absent" && len(selected) == 0 {
			t.Fatalf("expected selection is empty or unexpected for %s", tc.Name)
		}
		if tc.Scope == "first_dense4_snapshot" && len(selected) != 20 {
			t.Fatal("selected first dense-four snapshot lacks its 20 matching rows")
		}
		if tc.Scope == "lane03" && len(selected) != 50 {
			t.Fatal("source scope selected the wrong lane")
		}
	}
	specifications := hanScaleSQLPlan(t)
	for _, spec := range specifications {
		wrong := slices.Clone(spec.ArgumentExpressions)
		wrong[0] = "incorrect.mapping"
		if _, err := hanScaleExtractSQL(hanScaleRoot(t), spec.File, spec.Function, spec.Strategy, wrong); err == nil {
			t.Fatal("changed SQL argument mapping did not fail closed")
		}
	}
	input := evidenceingestion.GroundedEvidenceBriefInput{Query: "Orion 索引重建", ProposalListInput: evidenceingestion.ProposalListInput{SourceSnapshotID: "srcsnap:one", RepositorySnapshotID: "repo-snapshot:two", SourceID: "source", SourceVersion: "version", AdmissionOutcome: "pending", SourceGenerationID: "generation:three", LifecycleScope: "all", Limit: 10}}
	attempt := evidenceingestion.EvidenceQueryAttempt{Strategy: evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms, CompiledQuery: `{"han_literal_terms":["索引重建"],"ascii_simple_tsquery":"'orion'"}`}
	args, err := hanScaleArguments(specifications[2], input, attempt)
	wantArgs := []any{"'orion'", "srcsnap:one", "repo-snapshot:two", "source", "version", "pending", "generation:three", "all", 11, []string{"索引重建"}}
	if err != nil || !reflect.DeepEqual(args, wantArgs) {
		t.Fatal("Han SQL argument reconstruction changed order or content")
	}
	if _, err := hanScaleArguments(specifications[2], input, evidenceingestion.EvidenceQueryAttempt{}); err == nil {
		t.Fatal("Han SQL mapping accepted a missing runtime compiled query")
	}
}

func TestHanScaleConfig(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || name == "DATABASE_DSN" || name == "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN" {
			t.Setenv(name, "")
		}
	}
	config, err := hanScaleConfig("/private/tmp/han-scale-config-test", "55442")
	if err != nil || config.Database != "ahe_han_scale_lab" || config.User != "ahe_han_scale_operator" || config.Password != "" || config.TLSConfig != nil || config.Tracer != nil || len(config.Fallbacks) != 0 || config.RuntimeParams["statement_timeout"] != "2000" {
		t.Fatal("fixed scale configuration boundary changed")
	}
	for _, name := range []string{"PGHOST", "PGSERVICE", "PGPASSWORD", "DATABASE_DSN", "AHE_DBROLE_ACCEPTANCE_DATABASE_DSN"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "synthetic-setting-not-consumed")
			if config, err := hanScaleConfig("/private/tmp/han-scale-config-test", "55442"); err == nil || config != nil {
				t.Fatal("inherited DB setting reached scale config")
			}
		})
	}
	for _, item := range [][2]string{{"localhost", "55442"}, {"/", "55442"}, {"/private/tmp/lab", "0"}, {"/private/tmp/lab", "65536"}} {
		if config, err := hanScaleConfig(item[0], item[1]); err == nil || config != nil {
			t.Fatal("invalid socket destination accepted")
		}
	}
}

func TestIntegrationHanScaleIsolatedLab(t *testing.T) {
	socket, port := os.Getenv("AHE_HAN_SCALE_LAB_SOCKET"), os.Getenv("AHE_HAN_SCALE_LAB_PORT")
	if socket == "" && port == "" {
		t.Skip("set explicit AHE_HAN_SCALE_LAB_SOCKET/PORT for the disposable scale lab")
	}
	config, err := hanScaleConfig(socket, port)
	if err != nil {
		t.Fatal(err)
	}
	expectedIdentity := os.Getenv("AHE_HAN_SCALE_LAB_SYSTEM_IDENTIFIER")
	if _, err := strconv.ParseUint(expectedIdentity, 10, 64); err != nil || expectedIdentity == "0" {
		t.Fatal("scale lab requires an explicit PostgreSQL system identifier")
	}
	plan := hanScalePlan{Contract: "han-query-scale-v1", StageRows: []int{1000, 10000, 50000}, BatchSize: 100, WarmRepetitions: 5, PairOrder: []string{"AB", "BA", "AB", "BA", "AB", "BA"}, Corpus: hanScaleCorpus(50000), Cases: hanScaleCases(), CandidateSQL: hanScaleSQLPlan(t), CodeSHA256: hanQueryLabCodeHashes(t), Limitations: []string{
		"Domain walltime uses the unchanged restricted OpenRuntimePool path, including proposal/coverage hydration; it is not full MCP or Desktop time.",
		"first_in_case is not a cold-cache measurement: setup, readback, statistics, and prior queries may warm caches. Five warm repetitions are not a p95/p99 estimate.",
		"EXPLAIN is a source-extracted SQL diagnostic rerun, not runtime tracing or a guarantee about the cached generic/custom plan used by an earlier request.",
		"No individual runtime SQL durations are captured. Domain minus EXPLAIN time is not exact hydration cost. Batch size 100 and long fixture_output duplication are experimental conditions.",
		"100ms is an exploratory warning, not an SLO. A timeout or data mismatch stops all later requests and stages without retry or relaxed deadlines.",
		"Only synthetic pending manual sources are generated; no admission/disposition, external source, model, repository lifecycle, or project Git change occurs.",
		"The long-only query has different selectivity and is not a single-variable causal comparison of sentence length. Results do not establish relevance or production/company hardware performance.",
		"Source lanes correlate with corpus classes; scoped cardinalities are reported, not interpreted as an isolated causal source-filter speedup. The three extracted SQL statements exclude normalizer, hydration, coverage, and ACL queries.",
		"The independent stdio smoke uses a 2-second SQL timeout and the shared helper's 60-second whole-process deadline, not the timed domain requests' 5-second deadline.",
	}}
	data, err := os.ReadFile(filepath.Join(hanScaleRoot(t), "internal/mcpintegration/han_scale_integration_test.go"))
	if err != nil {
		t.Fatal("cannot freeze scale harness code hash")
	}
	plan.CodeSHA256["internal/mcpintegration/han_scale_integration_test.go"] = strings.TrimPrefix(stdioContentHash(data), "sha256:")
	plan.CorpusSHA256 = hanQueryLabJSONHash(t, plan.Corpus)
	report := hanScaleReport{Contract: plan.Contract, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: "preflight", PlannedSamples: len(plan.StageRows) * len(plan.Cases) * len(plan.PairOrder) * 2}
	hanScaleReserveArtifacts(t, plan, &report)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		hanScaleStop(t, &report, "preflight_connect", 0, "", "", 0, 0, err)
		return
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var listen string
	var extensions, publicAuthority, publicSchemaAuthority int
	if err := admin.QueryRow(ctx, `SELECT current_database(),current_setting('listen_addresses'),current_setting('server_version_num')::integer,(SELECT system_identifier::text FROM pg_control_system()),(SELECT count(*) FROM pg_extension WHERE extname<>'plpgsql'),(SELECT count(*) FROM pg_database d,LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname=current_database() AND a.grantee=0 AND a.privilege_type IN ('CREATE','TEMPORARY')),(SELECT count(*) FROM pg_namespace n,LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a WHERE n.nspname='public' AND a.grantee=0)`).Scan(&report.Database, &listen, &report.ServerVersion, &report.SystemIdentifier, &extensions, &publicAuthority, &publicSchemaAuthority); err != nil {
		hanScaleStop(t, &report, "identity", 0, "", "", 0, 0, err)
		return
	}
	if report.Database != "ahe_han_scale_lab" || listen != "" || report.ServerVersion/10000 != 18 || report.SystemIdentifier != expectedIdentity || extensions != 0 || publicAuthority != 0 || publicSchemaAuthority != 0 {
		t.Fatal("scale lab database identity or closed public authority preflight failed")
	}
	var extraSchemas int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname NOT IN ('public','information_schema') AND nspname NOT LIKE 'pg_%'`).Scan(&extraSchemas); err != nil {
		hanScaleStop(t, &report, "schema_preflight", 0, "", "", 0, 0, err)
		return
	}
	if extraSchemas != 0 {
		t.Fatal("scale lab must start without extra user schemas")
	}
	report.PermanentTablesBefore = hanQueryLabPermanentTables(t, ctx, admin)
	if report.PermanentTablesBefore != 0 {
		t.Fatal("scale lab must start with zero permanent user tables")
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		report.PermanentTablesAfterCleanup = hanQueryLabPermanentTables(t, cleanupCtx, admin)
		if report.Schema == "" {
			return
		}
		var exists bool
		if err := admin.QueryRow(cleanupCtx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, report.Schema).Scan(&exists); err != nil {
			t.Error("cannot verify scale schema cleanup")
			return
		}
		report.SchemaCleanupVerified = !exists && report.PermanentTablesAfterCleanup == report.PermanentTablesBefore
		if !report.SchemaCleanupVerified {
			t.Error("disposable scale schema cleanup was not verified")
		}
	})
	destination := url.URL{Scheme: "postgres", User: url.User(config.User), Path: "/" + config.Database}
	destination.RawQuery = url.Values{"host": {config.Host}, "port": {strconv.Itoa(int(config.Port))}, "sslmode": {"disable"}, "passfile": {"/dev/null"}, "connect_timeout": {"5"}}.Encode()
	report.Phase = "fixture_setup"
	fixture := newAuthorityProcessFixture(t, ctx, destination.String())
	report.Schema = fixture.schema
	query, binding := hanScaleRuntimePool(t, ctx, fixture.query, fixture.schema)
	report.QueryBinding = binding
	intake, binding := hanScaleRuntimePool(t, ctx, fixture.intake, fixture.schema)
	report.IntakeBinding = binding
	observerConfig, err := pgxpool.ParseConfig(destination.String())
	if err != nil {
		t.Fatal("cannot configure isolated scale observation pool")
	}
	observerConfig.MaxConns = 1
	observerConfig.ConnConfig.RuntimeParams = map[string]string{"search_path": fixture.schema, "statement_timeout": hanScaleSQLTimeout}
	observer, err := pgxpool.NewWithConfig(ctx, observerConfig)
	if err != nil {
		t.Fatal("cannot open isolated scale observation pool")
	}
	t.Cleanup(observer.Close)
	for _, size := range plan.StageRows {
		t.Logf("scale stage %d: seeding cumulative corpus", size)
		report.Phase = "seed"
		for len(report.Identities) < size {
			start := len(report.Identities)
			started := time.Now()
			batch, identities, err := hanScaleSeed(ctx, intake, observer, plan.Corpus[start:start+100], start/100)
			if err != nil {
				hanScaleStop(t, &report, "seed", size, "", "", 0, time.Since(started).Microseconds(), err)
				return
			}
			report.Batches = append(report.Batches, batch)
			report.Identities = append(report.Identities, identities...)
			if start == 0 {
				readCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
				record, err := evidenceingestion.GetProposalByOccurrenceID(readCtx, query, identities[0].ID)
				stop()
				if err != nil {
					hanScaleStop(t, &report, "initial_readback", size, "", "", 0, 0, err)
					return
				}
				if record.StatementText != plan.Corpus[0].Statement || record.AdmissionOutcome != "pending" || record.CanonicalRef != "" || len(record.SourceRefs) != 1 || record.SourceRefs[0].QuotedText != plan.Corpus[0].Statement || record.RawContentHash != batch.RawSHA256 {
					t.Fatal("first batch exact-source pending readback gate failed")
				}
				report.InitialReadback = &record
			}
		}
		t.Logf("scale stage %d: seed and exact readback complete", size)
		report.Phase = "stage"
		stage := hanScaleStage{Rows: size, Phase: "analyze", PlannedSamples: len(plan.Cases) * len(plan.PairOrder) * 2}
		report.Stages = append(report.Stages, stage)
		current := &report.Stages[len(report.Stages)-1]
		if err := hanScaleRunStage(t, ctx, fixture, observer, query, plan, &report, current); err != nil {
			if report.Failure == nil {
				hanScaleStop(t, &report, current.Phase, size, "", "", 0, 0, err)
			}
			return
		}
	}
	currentHashes := hanQueryLabCodeHashes(t)
	data, err = os.ReadFile(filepath.Join(hanScaleRoot(t), "internal/mcpintegration/han_scale_integration_test.go"))
	if err != nil {
		t.Fatal("cannot recheck frozen scale harness")
	}
	currentHashes["internal/mcpintegration/han_scale_integration_test.go"] = strings.TrimPrefix(stdioContentHash(data), "sha256:")
	if !reflect.DeepEqual(plan.CodeSHA256, currentHashes) {
		t.Fatal("frozen implementation changed during scale measurement")
	}
	report.Phase = "complete"
}

func hanScaleRuntimePool(t *testing.T, ctx context.Context, login authorityProcessLogin, schema string) (*pgxpool.Pool, dbrole.RuntimeBinding) {
	t.Helper()
	config, err := pgxpool.ParseConfig(login.dsn)
	if err != nil {
		t.Fatal("cannot parse bounded scale runtime login")
	}
	config.MaxConns, config.MinConns = 1, 1
	config.ConnConfig.RuntimeParams["statement_timeout"] = hanScaleSQLTimeout
	pool, binding, err := dbrole.OpenRuntimePool(ctx, config, dbrole.RuntimePoolInput{Role: login.group, Schema: schema, Profile: login.profile})
	if err != nil {
		t.Fatal("cannot open closed scale runtime pool")
	}
	t.Cleanup(pool.Close)
	return pool, binding
}

func hanScaleSeed(ctx context.Context, intake, observer *pgxpool.Pool, corpus []hanScaleRow, batchIndex int) (hanScaleBatch, []hanScaleIdentity, error) {
	batch := hanScaleBatch{FirstIndex: corpus[0].Index, LastIndex: corpus[len(corpus)-1].Index, SourceID: fmt.Sprintf("han-scale:lane%02d", batchIndex%10), Version: fmt.Sprintf("batch-%06d", batchIndex)}
	raw := hanScaleBatchSource(corpus)
	batch.RawSHA256 = stdioContentHash(raw)
	captureCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
	source, err := evidenceingestion.CaptureManualSource(captureCtx, intake, evidenceingestion.ManualTextInput{SourceID: batch.SourceID, SourceVersion: batch.Version, Raw: raw, RequestID: "han-scale-source-" + batch.Version, OriginMetadata: map[string]string{"fixture": "han-scale-v1-synthetic"}})
	stop()
	if err != nil {
		return batch, nil, err
	}
	batch.SourceSnapshotID, batch.ExtractionViewID = source.SourceSnapshotID, source.ExtractionViewID
	if len(source.Spans) != len(corpus) || source.RawContentHash != batch.RawSHA256 {
		return batch, nil, fmt.Errorf("source span/hash mismatch")
	}
	var proposals []evidenceingestion.ExtractorProposalOutput
	for i, row := range corpus {
		if source.Spans[i].QuotedText != row.Statement {
			return batch, nil, fmt.Errorf("source line is not exact statement")
		}
		proposals = append(proposals, evidenceingestion.ExtractorProposalOutput{ProposalLocalID: fmt.Sprintf("row-%06d", row.Index), StatementText: row.Statement, EvidenceRefs: []string{source.Spans[i].SpanID}})
	}
	outputCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
	result, err := evidenceingestion.SubmitExtractorOutput(outputCtx, intake, evidenceingestion.ExtractorOutputInput{RequestID: "han-scale-output-" + batch.Version, SourceSnapshotID: source.SourceSnapshotID, ExtractionViewID: source.ExtractionViewID, ExtractorDefinition: evidenceingestion.ExtractorDefinitionInput{Name: "han-scale-fixed-extractor", Version: "1", Config: map[string]string{}}, Output: evidenceingestion.FrozenExtractorOutput{Proposals: proposals}})
	stop()
	if err != nil {
		return batch, nil, err
	}
	batch.ExtractionAttemptID = result.ExtractionAttemptID
	if result.ProposalCount != len(corpus) {
		return batch, nil, fmt.Errorf("typed seed proposal count mismatch")
	}
	rows, err := observer.Query(ctx, `SELECT proposal_local_id,proposal_occurrence_id,created_at,statement_text,admission_outcome,canonical_ref IS NULL FROM proposal_occurrences WHERE extraction_attempt_id=$1`, result.ExtractionAttemptID)
	if err != nil {
		return batch, nil, err
	}
	defer rows.Close()
	var identities []hanScaleIdentity
	for rows.Next() {
		var local, statement, outcome string
		var noCanonical bool
		item := hanScaleIdentity{SourceID: batch.SourceID, SourceSnapshotID: batch.SourceSnapshotID}
		if err := rows.Scan(&local, &item.ID, &item.CreatedAt, &statement, &outcome, &noCanonical); err != nil {
			return batch, nil, err
		}
		if !strings.HasPrefix(local, "row-") {
			return batch, nil, fmt.Errorf("seed local identity mismatch")
		}
		item.Index, err = strconv.Atoi(strings.TrimPrefix(local, "row-"))
		if err != nil || item.Index < batch.FirstIndex || item.Index > batch.LastIndex || statement != corpus[item.Index-batch.FirstIndex].Statement || outcome != "pending" || !noCanonical {
			return batch, nil, fmt.Errorf("seed readback identity, text, or pending boundary mismatch")
		}
		identities = append(identities, item)
	}
	if err := rows.Err(); err != nil {
		return batch, nil, err
	}
	if len(identities) != len(corpus) {
		return batch, nil, fmt.Errorf("seed readback missing identities")
	}
	slices.SortFunc(identities, func(a, b hanScaleIdentity) int { return a.Index - b.Index })
	return batch, identities, nil
}

func hanScaleInput(tc hanScaleCase, identities []hanScaleIdentity, corpus []hanScaleRow) evidenceingestion.GroundedEvidenceBriefInput {
	input := evidenceingestion.GroundedEvidenceBriefInput{Query: tc.Query, ProposalListInput: evidenceingestion.ProposalListInput{AdmissionOutcome: "pending", LifecycleScope: "active", Limit: tc.Limit}}
	if tc.Scope == "lane03" {
		input.SourceID = "han-scale:lane03"
	}
	if tc.Scope == "first_dense4_snapshot" {
		for _, id := range identities {
			if corpus[id.Index-1].Class == "dense4" {
				input.SourceSnapshotID = id.SourceSnapshotID
				break
			}
		}
	}
	return input
}

func hanScaleExpected(tc hanScaleCase, input evidenceingestion.GroundedEvidenceBriefInput, identities []hanScaleIdentity, corpus []hanScaleRow, newMode bool) []string {
	if !newMode && tc.Class != "english" {
		return nil
	}
	var matches []hanScaleIdentity
	for _, id := range identities {
		row := corpus[id.Index-1]
		if input.SourceID != "" && id.SourceID != input.SourceID || input.SourceSnapshotID != "" && id.SourceSnapshotID != input.SourceSnapshotID {
			continue
		}
		if tc.Class == row.Class || tc.Class == "long" && row.Long {
			matches = append(matches, id)
		}
	}
	slices.SortFunc(matches, func(a, b hanScaleIdentity) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	var ids []string
	for _, match := range matches {
		ids = append(ids, match.ID)
	}
	return ids
}

func hanScaleRunStage(t *testing.T, ctx context.Context, fixture authorityProcessFixture, observer, query *pgxpool.Pool, plan hanScalePlan, report *hanScaleReport, stage *hanScaleStage) (resultErr error) {
	t.Helper()
	// Keep the reached stage even if a shared helper calls t.Fatal. Query
	// requests themselves return errors, so their partial receipt survives.
	defer func() {
		stage.AttemptedSamples = len(stage.Observations)
		stage.NotRunSamples = stage.PlannedSamples - stage.AttemptedSamples
		if stage.Before != nil {
			stage.After = hanQueryLabTableDigests(t, context.Background(), observer, fixture.schema)
			if !reflect.DeepEqual(stage.Before, stage.After) {
				resultErr = fmt.Errorf("scale measurement changed domain row contents")
				stage.Passed = false
				hanScaleStop(t, report, "after_digest", stage.Rows, "", "", 0, 0, resultErr)
			}
		}
	}()
	tables, err := observer.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=$1 ORDER BY tablename`, fixture.schema)
	if err != nil {
		return err
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			tables.Close()
			return err
		}
		names = append(names, name)
	}
	tables.Close()
	if tables.Err() != nil {
		return tables.Err()
	}
	stage.RelationBytes = make(map[string]int64)
	for _, name := range names {
		qualified := pgx.Identifier{fixture.schema, name}.Sanitize()
		if _, err := observer.Exec(ctx, "ANALYZE "+qualified); err != nil {
			return err
		}
		var size int64
		if err := observer.QueryRow(ctx, `SELECT pg_total_relation_size($1::regclass)`, qualified).Scan(&size); err != nil {
			return err
		}
		stage.RelationBytes[name] = size
	}
	stage.Settings = make(map[string]string)
	settings, err := query.Query(ctx, `SELECT name,setting,COALESCE(unit,'') FROM pg_settings WHERE name=ANY($1::text[]) ORDER BY name`, []string{"server_version", "server_encoding", "statement_timeout", "plan_cache_mode", "shared_buffers", "work_mem", "effective_cache_size", "jit", "max_parallel_workers_per_gather", "track_io_timing", "random_page_cost", "cpu_tuple_cost"})
	if err != nil {
		return err
	}
	for settings.Next() {
		var name, value, unit string
		if err := settings.Scan(&name, &value, &unit); err != nil {
			settings.Close()
			return err
		}
		stage.Settings[name] = value + " " + unit
	}
	settings.Close()
	if settings.Err() != nil {
		return settings.Err()
	}
	if stage.Settings["plan_cache_mode"] != "auto " || stage.Settings["statement_timeout"] != "2000 ms" {
		return fmt.Errorf("measurement session settings changed")
	}
	stage.Before = hanQueryLabTableDigests(t, ctx, observer, fixture.schema)
	if stage.Before["proposal_occurrences"].Rows != stage.Rows || stage.Before["canonical_graph_nodes"].Rows != 0 || stage.Before["admission_decisions"].Rows != 0 {
		return fmt.Errorf("stage count or pending-only precondition failed")
	}
	stage.PreparedBefore, err = hanScalePreparedStatements(ctx, query, plan.CandidateSQL)
	if err != nil {
		return err
	}
	stage.Phase = "timing"
	for _, tc := range plan.Cases {
		base := hanScaleInput(tc, report.Identities, plan.Corpus)
		for repeat, order := range plan.PairOrder {
			for _, mode := range order {
				input := base
				if mode == 'B' {
					input.QueryMode = evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1
				}
				expected := hanScaleExpected(tc, input, report.Identities, plan.Corpus, mode == 'B')
				observation := hanScaleObservation{Case: tc.Name, Mode: string(mode), Repeat: repeat, Phase: "warm", Input: input, FullCandidateCardinality: len(expected)}
				if repeat == 0 {
					observation.Phase = "first_in_case"
				}
				observation.ExpectedIDs = slices.Clone(expected[:min(len(expected), tc.Limit)])
				requestCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
				started := time.Now()
				result, callErr := evidenceingestion.GetGroundedEvidenceBrief(requestCtx, query, input)
				observation.ElapsedMicros = time.Since(started).Microseconds()
				stop()
				observation.Exploratory100msWarning = observation.ElapsedMicros > 100000
				observation.Execution = result.Execution
				for _, match := range result.Matches {
					observation.ReturnedIDs = append(observation.ReturnedIDs, match.Record.ProposalOccurrenceID)
				}
				if callErr == nil {
					for _, attempt := range result.Execution.Attempts {
						if attempt.Strategy != evidenceingestion.EvidenceQueryStrategyExactSimple && attempt.Strategy != evidenceingestion.EvidenceQueryStrategyEnglishMorphology && attempt.Strategy != evidenceingestion.EvidenceQueryStrategyHanLiteralAllTerms {
							callErr = fmt.Errorf("unplanned candidate strategy in timing observation")
						}
					}
					wantTruncated := len(expected) > tc.Limit
					if !slices.Equal(observation.ExpectedIDs, observation.ReturnedIDs) || result.Execution.Truncated != wantTruncated || result.Execution.GlobalAbsenceInferenceAllowed || result.Execution.CandidateCount != len(result.Matches) || result.Execution.SearchCompleteWithinSurface == wantTruncated {
						callErr = fmt.Errorf("bounded candidate oracle mismatch")
					}
					for _, match := range result.Matches {
						if match.Record.AdmissionOutcome != "pending" || match.Record.CanonicalRef != "" {
							callErr = fmt.Errorf("pending boundary changed")
						}
					}
				}
				observation.Passed = callErr == nil
				stage.Observations = append(stage.Observations, observation)
				if callErr != nil {
					hanScaleStop(t, report, "timing", stage.Rows, tc.Name, string(mode), repeat, observation.ElapsedMicros, callErr)
					return callErr
				}
			}
		}
	}
	t.Logf("scale stage %d: %d domain timing observations complete", stage.Rows, len(stage.Observations))
	stage.PreparedAfter, err = hanScalePreparedStatements(ctx, query, plan.CandidateSQL)
	if err != nil {
		return err
	}
	stage.Phase = "explain"
	seen := make(map[string]bool)
	for _, observation := range stage.Observations {
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
				return fmt.Errorf("unexpected candidate strategy outside frozen AST plan")
			}
			args, err := hanScaleArguments(*spec, observation.Input, attempt)
			if err != nil {
				return err
			}
			key := hanQueryLabJSONHash(t, struct {
				SQL  string
				Args []any
			}{spec.SQL, args})
			if seen[key] {
				continue
			}
			seen[key] = true
			diagnostic := hanScaleDiagnostic{Case: observation.Case, Mode: observation.Mode, Strategy: spec.Strategy, SQL: spec.SQL, SQLSHA256: spec.SHA256, Arguments: args}
			explainCtx, stop := context.WithTimeout(ctx, hanScaleRequestTimeout)
			started := time.Now()
			err = query.QueryRow(explainCtx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+spec.SQL, args...).Scan(&diagnostic.Plan)
			diagnostic.ElapsedMicros = time.Since(started).Microseconds()
			stop()
			stage.Diagnostics = append(stage.Diagnostics, diagnostic)
			if err != nil {
				hanScaleStop(t, report, "explain", stage.Rows, observation.Case, observation.Mode, 0, diagnostic.ElapsedMicros, err)
				return err
			}
		}
	}
	t.Logf("scale stage %d: %d source-extracted EXPLAIN diagnostics complete", stage.Rows, len(stage.Diagnostics))
	stage.Phase = "stdio_smoke"
	stdioLogin := fixture.query
	stdioDestination, err := url.Parse(stdioLogin.dsn)
	if err != nil {
		return fmt.Errorf("cannot configure bounded stdio smoke")
	}
	stdioSettings := stdioDestination.Query()
	stdioSettings.Set("statement_timeout", hanScaleSQLTimeout)
	stdioDestination.RawQuery = stdioSettings.Encode()
	stdioLogin.dsn = stdioDestination.String()
	process := startAuthorityProcess(t, ctx, "ahe-query-mcp", stdioLogin, fixture.schema, "")
	stage.LiveTools = hanQueryLabTools(t, process)
	tc := plan.Cases[4]
	for _, mode := range []string{"", evidenceingestion.EvidenceQueryModeExperimentalHanRecoveryV1} {
		request := evidencequerymcp.GetGroundedEvidenceBriefRequest{Query: tc.Query, QueryMode: mode, AdmissionOutcome: "pending", Limit: tc.Limit}
		response := authorityProcessTool[evidencequerymcp.GroundedEvidenceBriefResponse](t, process, "get_grounded_evidence_brief", request)
		stage.StdioSmoke = append(stage.StdioSmoke, response)
		var ids []string
		for _, match := range response.Matches {
			ids = append(ids, match.RecordRef.ID)
		}
		expected := hanScaleExpected(tc, hanScaleInput(tc, report.Identities, plan.Corpus), report.Identities, plan.Corpus, mode != "")
		if !slices.Equal(ids, expected[:min(len(expected), tc.Limit)]) {
			return fmt.Errorf("stdio smoke did not match the fixed candidate oracle")
		}
	}
	process.finish(t)
	stage.StdioEOF = true
	stage.Phase = "complete"
	stage.Passed = true
	return nil
}

func hanScalePreparedStatements(ctx context.Context, pool *pgxpool.Pool, sql []hanScaleSQL) ([]hanScalePrepared, error) {
	var allowed []string
	for _, spec := range sql {
		allowed = append(allowed, spec.SQL)
	}
	rows, err := pool.Query(ctx, `SELECT statement,generic_plans,custom_plans FROM pg_prepared_statements WHERE statement=ANY($1::text[]) ORDER BY statement`, allowed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []hanScalePrepared
	for rows.Next() {
		var statement string
		var item hanScalePrepared
		if err := rows.Scan(&statement, &item.GenericPlans, &item.CustomPlans); err != nil {
			return nil, err
		}
		item.StatementSHA256 = strings.TrimPrefix(stdioContentHash([]byte(statement)), "sha256:")
		result = append(result, item)
	}
	return result, rows.Err()
}

func hanScaleStop(t *testing.T, report *hanScaleReport, phase string, size int, caseName, mode string, repeat int, elapsed int64, err error) {
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
	t.Errorf("scale experiment stopped in %s at %d rows; class=%s SQLSTATE=%s", phase, size, failure.Class, failure.SQLState)
}

func hanScaleReserveArtifacts(t *testing.T, plan hanScalePlan, report *hanScaleReport) {
	t.Helper()
	path := os.Getenv("AHE_HAN_SCALE_LAB_REPORT")
	if !filepath.IsAbs(path) {
		t.Fatal("scale lab requires an absolute private report path")
	}
	reportFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively reserve scale report")
	}
	t.Cleanup(func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		for _, stage := range report.Stages {
			report.AttemptedSamples += len(stage.Observations)
		}
		report.NotRunSamples = report.PlannedSamples - report.AttemptedSamples
		report.Passed = !t.Failed() && report.Phase == "complete" && report.SchemaCleanupVerified
		encoder := json.NewEncoder(reportFile)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			t.Error("cannot save private scale report")
		}
		if err := reportFile.Close(); err != nil {
			t.Error("cannot close private scale report")
		}
	})
	report.PlanFile = path + ".plan.json"
	planFile, err := os.OpenFile(report.PlanFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("cannot exclusively create frozen scale plan")
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		_ = planFile.Close()
		t.Fatal("cannot serialize frozen scale plan")
	}
	report.PlanSHA256 = strings.TrimPrefix(stdioContentHash(encoded), "sha256:")
	_, writeErr := planFile.Write(encoded)
	closeErr := planFile.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("cannot persist frozen scale plan before database work")
	}
}
