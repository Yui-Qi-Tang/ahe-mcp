//go:build integration

package mcpintegration

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// These statement-local relations shadow every stored relation used by the
// original SQL. They test SQL predicates, not ingestion, support or admission.
const multisurfaceSQLShadow = `WITH fixture AS (
 SELECT * FROM jsonb_to_recordset($18::jsonb) AS f(id text, statement text, body text,
 snapshot text, source text, version text, outcome text, repo text, generation text,
 active boolean, span_count integer, span_bytes integer, raw_hash text, rendered_hash text)
), proposal_occurrences AS (
 SELECT id proposal_occurrence_id,statement statement_text,'2026-01-01T00:00:00Z'::timestamptz created_at,
 id extraction_attempt_id,id proposal_batch_id,outcome admission_outcome FROM fixture
), extraction_attempts AS (SELECT id extraction_attempt_id,id extraction_run_id FROM fixture),
extraction_runs AS (
 SELECT id extraction_run_id,CASE WHEN repo='' THEN snapshot END source_snapshot_id,
 NULLIF(repo,'') repository_snapshot_id,id extraction_view_id FROM fixture
), source_snapshots AS (
 SELECT snapshot source_snapshot_id,'manual_text'::text source_system,source source_id,
 version source_version,raw_hash raw_content_hash FROM fixture WHERE repo=''
), repository_snapshots AS (SELECT DISTINCT repo repository_snapshot_id FROM fixture WHERE repo<>''),
repository_source_generations AS (
 SELECT id proposal_batch_id,generation source_generation_id,repo repo_id,'fixture'::text extractor_name
 FROM fixture WHERE repo<>'' AND generation<>''
), repository_source_heads AS (
 SELECT DISTINCT repo repo_id,'fixture'::text extractor_name,generation active_generation_id
 FROM fixture WHERE repo<>'' AND active
), extraction_views AS (
 SELECT id extraction_view_id,snapshot source_snapshot_id,convert_to(body,'UTF8') rendered_content,
 rendered_hash rendered_content_hash,'manual-text-identity'::text renderer_name,'v1'::text renderer_version FROM fixture
), span_catalog_entries AS (
 SELECT id extraction_view_id,0 start_byte,span_bytes end_byte,
 substring(convert_to(body,'UTF8') FROM 1 FOR span_bytes) quoted_text
 FROM fixture CROSS JOIN LATERAL generate_series(1,span_count) n
), `

type multisurfaceSQLControl struct {
	name                               string
	rows                               []map[string]any
	filters                            [7]string
	baseline, terms, ids               []string
	scores                             []int
	limit, searched, excluded, invalid int
	minimumEnglish                     int
	han, anchors                       []string
}

func multisurfaceSQLControls() []multisurfaceSQLControl {
	row := func(id, statement, body string) map[string]any {
		return map[string]any{"id": id, "statement": statement, "body": body, "snapshot": "snapshot-" + id,
			"source": "source", "version": "v1", "outcome": "admitted", "repo": "", "generation": "",
			"active": false, "span_count": 1, "span_bytes": 1}
	}
	control := func(name string, rows []map[string]any, ids ...string) multisurfaceSQLControl {
		return multisurfaceSQLControl{name: name, rows: rows, ids: ids, terms: []string{"alpha", "beta"},
			filters: [7]string{"", "", "", "", "", "", "all"}, limit: 21, searched: len(rows), minimumEnglish: 2}
	}
	union := control("baseline-union-source-dedup-tie-limit-plus-one", []map[string]any{
		row("A", "alpha", "other"), row("B", "other", "alpha beta"),
		row("C", "alpha beta", "alpha beta"), row("D", "other", "other")}, "B", "C", "A")
	union.baseline, union.limit, union.scores = []string{"A", "D"}, 3, []int{2, 2, 1}
	controls := []multisurfaceSQLControl{union}
	for _, field := range []struct {
		name  string
		index int
	}{{"snapshot", 0}, {"source", 2}, {"version", 3}, {"outcome", 4}} {
		keep, bait := row("keep", "alpha beta", "other"), row("bait", "alpha beta", "other")
		bait[field.name] = "different"
		if field.name == "outcome" {
			bait[field.name] = "rejected"
		}
		c := control("filter-"+field.name, []map[string]any{keep, bait}, "keep")
		c.filters[field.index] = keep[field.name].(string)
		c.searched = 1
		controls = append(controls, c)
	}
	repository := func(id, repo, generation string, active bool) map[string]any {
		r := row(id, "alpha beta", "other")
		r["repo"], r["generation"], r["active"] = repo, generation, active
		return r
	}
	for _, filter := range []struct {
		name, value string
		index       int
	}{{"repository", "repo-keep", 1}, {"generation", "generation-keep", 5}} {
		c := control("filter-"+filter.name, []map[string]any{
			repository("keep", "repo-keep", "generation-keep", true), repository("bait", "repo-bait", "generation-bait", true)}, "keep")
		c.filters[filter.index] = filter.value
		c.searched = 0
		controls = append(controls, c)
	}
	for _, scope := range []string{"active", "historical", "all"} {
		c := control("lifecycle-"+scope, []map[string]any{row("manual", "alpha beta", "other"),
			repository("repo-active", "repo", "new", true), repository("repo-history", "repo", "old", false)})
		c.filters[6] = scope
		c.searched = 1
		c.ids = []string{"manual", "repo-active"}
		if scope == "historical" {
			c.ids = []string{"repo-history"}
			c.searched = 0
		}
		if scope == "all" {
			c.ids = append(c.ids, "repo-history")
		}
		controls = append(controls, c)
	}
	exact := control("three-source-caps-exact", nil, "body", "span_bytes", "span_count")
	for _, dimension := range []string{"body", "span_count", "span_bytes"} {
		for _, over := range []bool{false, true} {
			r := row(dimension, "other", "alpha beta")
			bound := map[string]int{"body": 1 << 20, "span_count": 4096, "span_bytes": 8192}[dimension]
			if over {
				bound++
			}
			if dimension == "body" || dimension == "span_bytes" {
				r["body"] = "alpha beta " + strings.Repeat("x", bound-11)
			}
			if dimension != "body" {
				r[dimension] = bound
			}
			if !over {
				exact.rows = append(exact.rows, r)
				exact.searched++
				continue
			}
			c := control(dimension+"-over", []map[string]any{r})
			c.searched = 0
			c.excluded = 1
			controls = append(controls, c)
		}
	}
	controls = append(controls, exact)
	bad := control("invalid-source-hash", []map[string]any{row("bad", "other", "alpha beta")})
	bad.rows[0]["raw_hash"] = "sha256:invalid"
	bad.invalid = 1
	controls = append(controls, bad)
	empty := control("empty-terms-do-not-match-all", []map[string]any{row("empty", "alpha beta", "alpha beta")})
	empty.terms = nil
	controls = append(controls, empty)
	return controls
}

func multisurfaceSQLFixture(t *testing.T, c multisurfaceSQLControl) []byte {
	t.Helper()
	for _, row := range c.rows {
		hash := hanOutcomeHash([]byte(row["body"].(string)))
		row["rendered_hash"] = "sha256:" + hash
		if _, ok := row["raw_hash"]; !ok {
			row["raw_hash"] = "sha256:" + hash
		}
	}
	data, err := json.Marshal(c.rows)
	if err != nil || len(data) > 2<<20 {
		t.Fatal("SQL control fixture exceeds its fixed input budget")
	}
	return data
}

func multisurfaceSQLConstant(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join("..", "evidenceingestion", "query_multisurface.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read multisurface SQL source")
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if name.Name == "multisurfaceCandidatesSQL" {
				literal, ok := spec.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatal("SQL must remain a literal")
				}
				sql, err = strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		return true
	})
	body, ok := strings.CutPrefix(strings.TrimSpace(sql), "WITH ")
	if !ok || strings.Contains(body, ";") {
		t.Fatal("SQL must remain one WITH SELECT statement")
	}
	return multisurfaceSQLShadow + body, hanOutcomeHash(data)
}

func TestMultisurfaceSQLControlPlan(t *testing.T) {
	sql, _ := multisurfaceSQLConstant(t)
	controls := multisurfaceSQLControls()
	if len(controls) != 16 || !strings.Contains(sql, "jsonb_to_recordset($18::jsonb)") {
		t.Fatal("fixed SQL control plan changed")
	}
	seen := map[string]bool{}
	for _, c := range controls {
		if seen[c.name] {
			t.Fatal("duplicate SQL control")
		}
		seen[c.name] = true
		multisurfaceSQLFixture(t, c)
	}
}

func TestIntegrationMultisurfaceSQLControls(t *testing.T) {
	runMultisurfaceSQLControls(t, false)
}

func runMultisurfaceSQLControls(t *testing.T, practical bool) {
	gate := "AHE_MULTISURFACE_SQL_CONTROLS"
	controls := multisurfaceSQLControls()
	if practical {
		gate, controls = "AHE_PRACTICAL_SQL_CONTROLS", practicalSQLControls()
	}
	runSelectedMultisurfaceSQLControls(t, gate, controls)
}

func runSelectedMultisurfaceSQLControls(t *testing.T, gate string, controls []multisurfaceSQLControl) {
	if os.Getenv(gate) == "" {
		t.Skip("set the dedicated SQL controls gate to run only after the runtime freeze")
	}
	if os.Getenv(gate) != "run" {
		t.Fatal("unknown SQL control phase")
	}
	sql, hash := multisurfaceSQLConstant(t)
	if hash != os.Getenv("AHE_MULTISURFACE_SQL_RUNTIME_SHA256") {
		t.Fatal("multisurface runtime differs from the pinned freeze")
	}
	socket, port := os.Getenv("AHE_MULTISURFACE_LAB_SOCKET"), os.Getenv("AHE_MULTISURFACE_LAB_PORT")
	config, err := multisurfaceLabConfig(socket, port, "ahe_brief_operator")
	if err != nil {
		t.Fatal(err)
	}
	identity := os.Getenv("AHE_MULTISURFACE_LAB_SYSTEM_IDENTIFIER")
	if n, e := strconv.ParseUint(identity, 10, 64); e != nil || n == 0 || slices.Contains([]string{"7683988729274717512", "7684171091801649800"}, identity) {
		t.Fatal("SQL controls need the new pinned clone identity")
	}
	root := filepath.Dir(os.Getenv("AHE_MULTISURFACE_LAB_REPORT"))
	rootPrefix := "multisurface-lab."
	wantDataDirectory := filepath.Join(root, "pgdata")
	if gate == "AHE_PRACTICAL_ANCHOR_SQL_CONTROLS" {
		rootPrefix, wantDataDirectory = "practical-han-lab.", practicalAnchorPGData
	}
	if filepath.Dir(root) != "/redacted/ahe-mcp/bin" || !strings.HasPrefix(filepath.Base(root), rootPrefix) {
		t.Fatal("SQL controls require the private clone root")
	}
	for _, dir := range []string{socket, root} {
		info, e := os.Lstat(dir)
		canonical, ce := filepath.EvalSymlinks(dir)
		if e != nil || ce != nil || !info.IsDir() || info.Mode().Perm() != 0700 || canonical != dir {
			t.Fatal("SQL controls require canonical private directories")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to pinned SQL control clone")
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal("cannot open read-only SQL control transaction")
	}
	defer tx.Rollback(context.Background())
	var system, data, db, user, schema, listen, sockets, readonly string
	if err := tx.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_control_system()),current_setting('data_directory'),current_database(),current_user,current_schema(),current_setting('listen_addresses'),current_setting('unix_socket_directories'),current_setting('transaction_read_only')`).Scan(&system, &data, &db, &user, &schema, &listen, &sockets, &readonly); err != nil {
		t.Fatal("cannot inspect SQL control clone identity")
	}
	if system != identity || data != wantDataDirectory || db != "ahe_brief_lab" || user != "ahe_brief_operator" || schema != "ahe_brief" || listen != "" || sockets != socket || readonly != "on" {
		t.Fatal("SQL control clone identity mismatch")
	}
	for _, c := range controls {
		args := []any{c.han, c.filters[0], c.filters[1], c.filters[2], c.filters[3], c.filters[4], c.filters[5], c.filters[6], c.limit, c.terms, c.baseline, int64(1 << 20), int64(4096), int64(8192), "[一-鿿]", c.minimumEnglish, c.anchors, multisurfaceSQLFixture(t, c)}
		var payload []byte
		var searched, excluded, invalid int
		if err := tx.QueryRow(ctx, sql, args...).Scan(&payload, &searched, &excluded, &invalid); err != nil {
			t.Fatalf("SQL control %s failed; no retry", c.name)
		}
		var results []struct {
			ID             string
			Score          int
			Baseline       bool
			StatementTerms []string `json:"statement_terms"`
			SourceTerms    []string `json:"source_terms"`
		}
		if json.Unmarshal(payload, &results) != nil {
			t.Fatal("invalid SQL control result")
		}
		ids, scores := []string{}, []int{}
		for _, r := range results {
			ids = append(ids, r.ID)
			scores = append(scores, r.Score)
		}
		if !slices.Equal(ids, c.ids) || searched != c.searched || excluded != c.excluded || invalid != c.invalid || (c.scores != nil && !slices.Equal(scores, c.scores)) {
			t.Fatalf("SQL control %s: ids=%v scores=%v searched/excluded/invalid=%d/%d/%d", c.name, ids, scores, searched, excluded, invalid)
		}
		if c.name == "baseline-union-source-dedup-tie-limit-plus-one" && (len(results[0].StatementTerms) != 0 || len(results[0].SourceTerms) != 2 || !results[2].Baseline) {
			t.Fatal("SQL union lost source-only or baseline provenance")
		}
		if c.name == "anchor-retains-auxiliary-score-and-full-terms" &&
			(!slices.Equal(results[0].StatementTerms, []string{"影響", "火山"}) || !slices.Equal(results[0].SourceTerms, []string{"影響"})) {
			t.Fatal("anchor eligibility discarded original auxiliary retrieval terms")
		}
		t.Logf("SQL control %s PASS ids=%v searched/excluded/invalid=%d/%d/%d", c.name, ids, searched, excluded, invalid)
	}
	t.Logf("%d fixed SQL controls completed; runtime_sha256=%s; no stored relations modified", len(controls), hash)
}
