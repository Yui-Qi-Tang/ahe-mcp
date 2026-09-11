package evidenceingestion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// This lab mirrors lexical SQL only, not MCP, AHE joins, or admission.
// Public synthetic T1-T4 candidates are frozen from Detective's
// docs/BRIEF_DESKTOP_INTEGRATION_20260911.md:78,194/227,335,536;
// source bodies are from internal/sourcepilot/testdata/brief_*.json.
type cjkLabDocument struct {
	ID, SourceID, Outcome, Statement, SourceBody string
}

var cjkLabDocuments = []cjkLabDocument{
	{ID: "T1", SourceID: "synthetic-orion", Outcome: "admitted", Statement: "Orion Search 正在經歷性能下降，其他 Orion 服務未受影響，團隊正在調查中，尚未有恢復報告。", SourceBody: "Orion Search is experiencing degraded performance. Other Orion services are not affected.\nThe team is investigating; no recovery has been reported.\nView history. Notification preferences. Contact support."},
	{ID: "T2", SourceID: "synthetic-orion-reject-control", Outcome: "rejected", Statement: "Orion Search has recovered after the team rolled back a deployment.", SourceBody: "Orion Search is experiencing degraded performance. Other Orion services are not affected.\nThe team is investigating; no recovery has been reported.\nView history. Notification preferences. Contact support."},
	{ID: "T3", SourceID: "synthetic-observatory-audit-control", Outcome: "admitted", Statement: "合成天文台報告稱，在樣本島附近發生了一次初步測得芮氏規模4.2的地震，其具體位置和深度仍在審查中，報告未提及任何損壞或傷亡。", SourceBody: "The synthetic observatory reported a preliminary magnitude 4.2 earthquake near Sample Island.\nThe location and depth remain under review. This bulletin does not report damage or casualties."},
	{ID: "T4", SourceID: "synthetic-orion-maintenance-audit-only", Outcome: "audit_only", Statement: "合成的 Orion 公告宣布計劃進行搜尋索引重建，但該重建尚未開始。", SourceBody: "The synthetic Orion bulletin announces a planned Search index rebuild.\nThe rebuild has not started. This bulletin reports a plan, not an observed service incident."},
	{ID: "C1", SourceID: "controls", Outcome: "admitted", Statement: "OrionBeta 搜尋索引重建尚未開始。", SourceBody: "synthetic control"},
	{ID: "C2", SourceID: "controls", Outcome: "admitted", Statement: "Orion Search 維護仍在進行。", SourceBody: "synthetic control"},
	{ID: "C3", SourceID: "controls", Outcome: "admitted", Statement: "索引重建仍在規劃中。", SourceBody: "synthetic control"},
	{ID: "C4", SourceID: "controls", Outcome: "admitted", Statement: "A service maintenance bulletin exists.", SourceBody: "Orion 索引重建"},
	{ID: "C5", SourceID: "controls", Outcome: "admitted", Statement: "The earthquake report is under review.", SourceBody: "地震 芮氏"},
	{ID: "C6", SourceID: "controls", Outcome: "admitted", Statement: "地震區域仍在審查中。", SourceBody: "synthetic control"},
	{ID: "C7", SourceID: "controls", Outcome: "admitted", Statement: "芮氏規模4.2仍在審查中。", SourceBody: "synthetic control"},
	{ID: "E1", SourceID: "english", Outcome: "admitted", Statement: "A proposal lifecycle remains pending.", SourceBody: "synthetic control"},
	{ID: "E2", SourceID: "english", Outcome: "admitted", Statement: "Proposals lifecycle records remain pending.", SourceBody: "synthetic control"},
}

type cjkLabCase struct {
	Name, Query, SourceID, Outcome string
	WantA, WantB                   []string
	Note                           string
}

var cjkLabCases = []cjkLabCase{
	{Name: "T1 existing hit", Query: "Orion Search", SourceID: "synthetic-orion", Outcome: "admitted", WantA: []string{"T1"}, WantB: []string{"T1"}},
	{Name: "T2 rejected remains findable", Query: "recovered deployment", SourceID: "synthetic-orion-reject-control", Outcome: "rejected", WantA: []string{"T2"}, WantB: []string{"T2"}},
	{Name: "T3 two Han terms", Query: "地震 芮氏", SourceID: "synthetic-observatory-audit-control", Outcome: "admitted", WantB: []string{"T3"}},
	{Name: "T4 mixed query", Query: "Orion 索引重建", SourceID: "synthetic-orion-maintenance-audit-only", Outcome: "audit_only", WantB: []string{"T4"}},
	{Name: "T3 outcome only", Query: "地震 芮氏", Outcome: "admitted", WantB: []string{"T3"}},
	{Name: "T4 outcome only", Query: "Orion 索引重建", Outcome: "audit_only", WantB: []string{"T4"}},
	{Name: "T3 reversed whitespace", Query: "  芮氏\t地震\n", SourceID: "synthetic-observatory-audit-control", Outcome: "admitted", WantB: []string{"T3"}},
	{Name: "T3 Chinese punctuation", Query: "地震，芮氏", SourceID: "synthetic-observatory-audit-control", Outcome: "admitted", WantB: []string{"T3"}},
	{Name: "T4 lowercase punctuation", Query: "orion，索引重建", SourceID: "synthetic-orion-maintenance-audit-only", Outcome: "audit_only", WantB: []string{"T4"}},
	{Name: "wrong source", Query: "地震 芮氏", SourceID: "synthetic-orion", Outcome: "admitted"},
	{Name: "wrong outcome", Query: "地震 芮氏", SourceID: "synthetic-observatory-audit-control", Outcome: "rejected"},
	{Name: "audit excluded by admitted filter", Query: "Orion 索引重建", SourceID: "synthetic-orion-maintenance-audit-only", Outcome: "admitted"},
	{Name: "OrionBeta missing term and source-only controls", Query: "Orion 索引重建", SourceID: "controls", Outcome: "admitted"},
	{Name: "Han missing term and source-only controls", Query: "地震 芮氏", SourceID: "controls", Outcome: "admitted"},
	{Name: "ASCII prefix remains exact", Query: "Ori 索引重建", SourceID: "synthetic-orion-maintenance-audit-only", Outcome: "audit_only"},
	{Name: "English singular baseline", Query: "proposal lifecycle", SourceID: "english", Outcome: "admitted", WantA: []string{"E1", "E2"}, WantB: []string{"E1", "E2"}},
	{Name: "English plural baseline", Query: "proposals lifecycle", SourceID: "english", Outcome: "admitted", WantA: []string{"E1", "E2"}, WantB: []string{"E1", "E2"}},
	{Name: "English min2 baseline", Query: "proposal lifecycle timing", SourceID: "english", Outcome: "admitted", WantA: []string{"E1", "E2"}, WantB: []string{"E1", "E2"}, Note: "The timing term is absent. A returns partial lexical matches; B preserves them and does not establish support for the full query."},
	{Name: "ASCII miss has no fallback", Query: "Orion quasar", SourceID: "synthetic-orion", Outcome: "admitted"},
	{Name: "unseparated mixed token unsupported", Query: "Orion索引重建", SourceID: "synthetic-orion-maintenance-audit-only", Outcome: "audit_only"},
	{Name: "single Han character unsupported", Query: "地 芮", SourceID: "synthetic-observatory-audit-control", Outcome: "admitted"},
}

// Delimiters are explicit: no aliases, ASCII stemming, arbitrary segmentation,
// or normalization of stored source text. Single Han characters are too broad.
func cjkLabTerms(query string) (han, ascii []string, gate string) {
	input := ProposalSearchInput{Query: strings.TrimSpace(query), ProposalListInput: normalizeProposalListInput(ProposalListInput{})}
	if err := validateProposalSearchInput(input); err != nil {
		return nil, nil, "invalid_query"
	}
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，、；：！？。,;:!?", r)
	})
	if len(terms) > proposalSearchMaxTerms {
		return nil, nil, "too_many_terms"
	}
	for _, term := range terms {
		hasHan, hasASCII := false, false
		for _, r := range term {
			switch {
			case unicode.Is(unicode.Han, r):
				hasHan = true
			case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
				hasASCII = true
			default:
				return nil, nil, "unsupported_token"
			}
		}
		if hasHan && hasASCII {
			return nil, nil, "mixed_token_requires_separator"
		}
		if hasHan {
			if len([]rune(term)) < 2 {
				return nil, nil, "single_han_character"
			}
			han = append(han, term)
		} else {
			ascii = append(ascii, term)
		}
	}
	if len(han) == 0 {
		return nil, nil, "no_han_terms"
	}
	return han, ascii, "eligible"
}

func TestCJKLexicalLabTerms(t *testing.T) {
	for _, tc := range []struct {
		query, gate string
		han, ascii  []string
	}{
		{"地震，芮氏", "eligible", []string{"地震", "芮氏"}, nil},
		{" orion\t索引重建\n", "eligible", []string{"索引重建"}, []string{"orion"}},
		{"Orion索引重建", "mixed_token_requires_separator", nil, nil},
		{"Orion-Beta 索引重建", "unsupported_token", nil, nil},
		{"地 芮", "single_han_character", nil, nil},
		{"proposal lifecycle", "no_han_terms", nil, nil},
		{"地震 café", "unsupported_token", nil, nil},
		{"", "invalid_query", nil, nil},
		{strings.Repeat("地震，", 17), "too_many_terms", nil, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			han, ascii, gate := cjkLabTerms(tc.query)
			if gate != tc.gate || !slices.Equal(han, tc.han) || !slices.Equal(ascii, tc.ascii) {
				t.Fatalf("terms = %q/%q/%s, want %q/%q/%s", han, ascii, gate, tc.han, tc.ascii, tc.gate)
			}
		})
	}
}

const cjkLabLimit = 100

type cjkLabPage struct {
	IDs       []string
	Ranks     []*float64 // A rank in result order; nil for B fallback's unscored ordering.
	Truncated bool
}

type cjkLabAttempt struct {
	Strategy, Compiled string
	Terms              []string
	Page               cjkLabPage
	ElapsedMicros      int64
}

type cjkLabObservation struct {
	Case                         cjkLabCase
	Fields                       []string
	Attempts                     []cjkLabAttempt
	HanTerms, ASCIITerms         []string
	FallbackGate                 string
	A, B                         cjkLabPage
	BOrder                       string
	AElapsedMicros, BExtraMicros int64
}

func cjkLabConfig(socket string, port uint16) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") && value != "" {
			return nil, fmt.Errorf("lab refuses inherited PG environment settings")
		}
	}
	// An explicitly empty service still makes pgx read a service file. Omit the
	// key entirely after rejecting inherited PG settings, including PGSERVICE.
	config, err := pgx.ParseConfig("host=/nonexistent port=1 user=ahe_text_search_operator dbname=ahe_text_search_lab password='' passfile=/dev/null sslmode=disable target_session_attrs=any")
	if err != nil {
		return nil, fmt.Errorf("parsing fixed lab connection settings")
	}
	config.Host, config.Port = socket, port
	config.Password, config.Fallbacks, config.RuntimeParams = "", nil, map[string]string{"application_name": "ahe_cjk_lexical_lab", "statement_timeout": "5000"}
	return config, nil
}

func TestCJKLexicalLabConfig(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") {
			t.Setenv(name, "")
		}
	}
	config, err := cjkLabConfig("/private/tmp/cjk-lab-test", 55440)
	if err != nil || config.Host != "/private/tmp/cjk-lab-test" || config.Port != 55440 || config.Database != "ahe_text_search_lab" || config.User != "ahe_text_search_operator" || config.Password != "" || config.TLSConfig != nil || len(config.Fallbacks) != 0 || len(config.RuntimeParams) != 2 {
		t.Fatal("fixed isolated config did not parse with the required connection boundary")
	}
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSWORD"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "synthetic-test-value-must-not-be-consumed")
			if config, err := cjkLabConfig("/private/tmp/cjk-lab-test", 55440); err == nil || config != nil {
				t.Fatal("inherited PG setting reached config parsing")
			}
		})
	}
}

func TestCJKLexicalLabPostgres(t *testing.T) {
	socket, port := os.Getenv("AHE_LEXICAL_LAB_SOCKET"), os.Getenv("AHE_LEXICAL_LAB_PORT")
	if socket == "" && port == "" {
		t.Skip("set both AHE_LEXICAL_LAB_SOCKET and AHE_LEXICAL_LAB_PORT for the isolated lab")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if !filepath.IsAbs(socket) || err != nil || portNumber == 0 {
		t.Fatal("lab requires an absolute Unix socket directory and a valid explicit port")
	}
	config, err := cjkLabConfig(socket, uint16(portNumber))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("isolated lab connection failed")
	}
	defer conn.Close(context.Background())
	var database, listen string
	var serverVersion, extensions int
	if err := conn.QueryRow(ctx, `SELECT current_database(), current_setting('listen_addresses'), current_setting('server_version_num')::integer, (SELECT count(*) FROM pg_extension WHERE extname <> 'plpgsql')`).Scan(&database, &listen, &serverVersion, &extensions); err != nil {
		t.Fatal(err)
	}
	if database != "ahe_text_search_lab" || listen != "" || serverVersion/10000 != 18 || extensions != 0 {
		t.Fatal("lab requires ahe_text_search_lab on socket-only PostgreSQL 18 without additional extensions")
	}
	permanentBefore := cjkLabPermanentTables(t, ctx, conn)
	if permanentBefore != 0 {
		t.Fatal("isolated lab must start with zero permanent user tables")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE lab_documents (id text PRIMARY KEY, source_id text, outcome text, statement_text text, source_body text, created_at timestamptz DEFAULT '2026-09-11T00:00:00Z') ON COMMIT DROP`); err != nil {
		t.Fatal(err)
	}
	for _, doc := range cjkLabDocuments {
		if _, err := tx.Exec(ctx, "INSERT INTO pg_temp.lab_documents (id,source_id,outcome,statement_text,source_body) VALUES ($1,$2,$3,$4,$5)", doc.ID, doc.SourceID, doc.Outcome, doc.Statement, doc.SourceBody); err != nil {
			t.Fatal(err)
		}
	}
	observations := make([]cjkLabObservation, 0, len(cjkLabCases))
	for _, tc := range cjkLabCases {
		t.Run(tc.Name, func(t *testing.T) {
			started := time.Now()
			compiled, err := compileEvidenceQuery(ctx, pgxTx{tx: tx}, tc.Query)
			if err != nil {
				t.Fatal(err)
			}
			obs := cjkLabObservation{Case: tc, Fields: []string{EvidenceQuerySearchSurfaceProposalStatement}}
			for i, plan := range []struct {
				config, query, strategy string
				terms                   []string
			}{
				{"simple", compiled.simpleQuery, EvidenceQueryStrategyExactSimple, compiled.simpleTerms},
				{"english", compiled.englishQuery, EvidenceQueryStrategyEnglishMorphology, compiled.englishTerms},
				{"english", compiled.relaxedQuery, EvidenceQueryStrategyEnglishRelaxation, compiled.englishTerms},
			} {
				if i == 2 && (len(obs.A.IDs) > 0 || len(compiled.englishTerms) < 3) {
					break
				}
				attemptStart := time.Now()
				obs.A = cjkLabFTS(t, ctx, tx, tc, plan.config, plan.query, plan.terms, i == 2)
				obs.Attempts = append(obs.Attempts, cjkLabAttempt{Strategy: plan.strategy, Compiled: plan.query, Terms: plan.terms, Page: obs.A, ElapsedMicros: time.Since(attemptStart).Microseconds()})
			}
			obs.AElapsedMicros = time.Since(started).Microseconds()
			obs.B, obs.BOrder = obs.A, "baseline_order_unchanged"
			obs.HanTerms, obs.ASCIITerms, obs.FallbackGate = cjkLabTerms(tc.Query)
			if len(obs.A.IDs) > 0 {
				obs.FallbackGate = "baseline_hit_preserved"
			} else if obs.FallbackGate == "eligible" {
				fallbackStart := time.Now()
				obs.B = cjkLabFallback(t, ctx, tx, tc, obs.HanTerms, obs.ASCIITerms)
				obs.BOrder = "created_at_desc,id_asc; no relevance score"
				obs.BExtraMicros = time.Since(fallbackStart).Microseconds()
			}
			observations = append(observations, obs)
			if !slices.Equal(obs.A.IDs, tc.WantA) || !slices.Equal(obs.B.IDs, tc.WantB) {
				t.Errorf("query %q: A=%v B=%v; want A=%v B=%v; attempts=%+v", tc.Query, obs.A, obs.B, tc.WantA, tc.WantB, obs.Attempts)
			}
		})
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	permanentAfter := cjkLabPermanentTables(t, ctx, conn)
	if permanentAfter != 0 {
		t.Fatal("isolated lab must finish with zero permanent user tables")
	}
	corpus, err := json.Marshal(cjkLabDocuments)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := json.Marshal(cjkLabCases)
	if err != nil {
		t.Fatal(err)
	}
	report := struct {
		Contract, Baseline, Fallback, Caveat, CorpusSHA256         string
		CasePlanSHA256                                             string
		Limit, PermanentTablesBefore, PermanentTablesAfterRollback int
		ServerVersion                                              int
		Documents                                                  []cjkLabDocument
		Observations                                               []cjkLabObservation
	}{
		Contract: "cjk-lexical-lab-v1", Baseline: EvidenceQueryPlanRecoveryV2 + ": SQL-equivalent only; not full MCP",
		Fallback:     "Han literal substring AND ASCII simple lexemes; statement_text only; baseline-miss only",
		Caveat:       "T3 retrieval is not semantic approval. Synthetic small-corpus timings cannot establish performance gains. No AHE data or admission writes.",
		CorpusSHA256: fmt.Sprintf("%x", sha256.Sum256(corpus)), ServerVersion: serverVersion, Documents: cjkLabDocuments, Observations: observations,
		CasePlanSHA256: fmt.Sprintf("%x", sha256.Sum256(cases)), Limit: cjkLabLimit, PermanentTablesBefore: permanentBefore, PermanentTablesAfterRollback: permanentAfter,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("AHE_LEXICAL_LAB_REPORT"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("lab report path must be absolute")
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(append(data, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("writing lab report: %v; closing: %v", writeErr, closeErr)
		}
	} else {
		t.Log(string(data))
	}
}

func cjkLabFTS(t *testing.T, ctx context.Context, tx pgx.Tx, tc cjkLabCase, config, query string, terms []string, relaxed bool) cjkLabPage {
	t.Helper()
	rows, err := tx.Query(ctx, `
		WITH matches AS (
			SELECT id, created_at, ts_rank_cd(to_tsvector($1::regconfig, statement_text), $2::tsquery) AS rank,
				(SELECT count(*) FROM unnest($5::text[]) AS term WHERE term = ANY(tsvector_to_array(to_tsvector($1::regconfig, statement_text)))) AS matched_terms
			FROM pg_temp.lab_documents
			WHERE to_tsvector($1::regconfig, statement_text) @@ $2::tsquery
				AND ($3 = '' OR source_id = $3) AND ($4 = '' OR outcome = $4)
		)
		SELECT id, CASE WHEN $6 THEN matched_terms::double precision / cardinality($5::text[]) ELSE rank::double precision END
		FROM matches WHERE NOT $6::boolean OR matched_terms >= 2
		ORDER BY CASE WHEN $6 THEN matched_terms ELSE 0 END DESC, rank DESC, created_at DESC, id
		LIMIT $7`, config, query, tc.SourceID, tc.Outcome, terms, relaxed, cjkLabLimit+1)
	return cjkLabRows(t, rows, err)
}

func cjkLabFallback(t *testing.T, ctx context.Context, tx pgx.Tx, tc cjkLabCase, han, ascii []string) cjkLabPage {
	t.Helper()
	rows, err := tx.Query(ctx, `
		SELECT id, NULL::double precision FROM pg_temp.lab_documents
		WHERE ($1 = '' OR source_id = $1) AND ($2 = '' OR outcome = $2)
			AND NOT EXISTS (SELECT 1 FROM unnest($3::text[]) AS term WHERE strpos(statement_text, term) = 0)
			AND ($4 = '' OR to_tsvector('simple', statement_text) @@ plainto_tsquery('simple', $4))
		ORDER BY created_at DESC, id LIMIT $5`, tc.SourceID, tc.Outcome, han, strings.Join(ascii, " "), cjkLabLimit+1)
	return cjkLabRows(t, rows, err)
}

func cjkLabRows(t *testing.T, rows pgx.Rows, err error) cjkLabPage {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var page cjkLabPage
	for rows.Next() {
		var id string
		var rank *float64
		if err := rows.Scan(&id, &rank); err != nil {
			t.Fatal(err)
		}
		page.IDs = append(page.IDs, id)
		page.Ranks = append(page.Ranks, rank)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	page.Truncated = len(page.IDs) > cjkLabLimit
	if page.Truncated {
		page.IDs, page.Ranks = page.IDs[:cjkLabLimit], page.Ranks[:cjkLabLimit]
	}
	return page
}

func cjkLabPermanentTables(t *testing.T, ctx context.Context, conn *pgx.Conn) int {
	t.Helper()
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p') AND c.relpersistence <> 't' AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
