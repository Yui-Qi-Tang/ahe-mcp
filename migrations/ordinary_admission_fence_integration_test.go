//go:build integration

package migrations

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ordinaryFencePredecessor = "000043_evidence_ingestion_source_run_request_identity.up.sql"
	ordinaryFenceMigration   = "000044_evidence_ingestion_ordinary_admission_integrity.up.sql"
)

func TestIntegrationOrdinaryAdmissionFreshAuthorityFence(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 0)
	applyMigrationsThrough(t, ctx, pool, ordinaryFencePredecessor)
	createOrdinaryFencePendingProposal(t, ctx, pool)
	before := ordinaryFenceRows(t, ctx, pool)

	changed, err := ApplyUpInSchema(ctx, pool, schema)
	assertOrdinaryFenceFailure(t, changed, err, "migration 000044 requires authority-fresh native state; authority_rows=1 supersession_head_rows=1 exact_empty_heads=1")
	if after := ordinaryFenceRows(t, ctx, pool); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected migration 44 changed persisted rows or the migration ledger")
	}
	assertMigrationCount(t, ctx, pool, 43)
	for _, table := range []string{
		"canonical_ordinary_admission_manifests",
		"canonical_ordinary_admission_node_bindings",
		"canonical_ordinary_admission_edge_bindings",
		"canonical_source_claim_review_bindings",
	} {
		assertMigrationTableMissing(t, ctx, pool, table)
	}
	assertMigrationColumnMissing(t, ctx, pool, "admission_decisions", "review_binding_contract_version")
	var guardExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_trigger
		WHERE tgrelid = 'proposal_occurrences'::regclass
		AND tgname = 'proposal_occurrences_terminal_guard')
	`).Scan(&guardExists); err != nil {
		t.Fatalf("read migration 44 guard after rejected upgrade: %v", err)
	}
	if guardExists {
		t.Fatal("rejected migration 44 left its proposal guard installed")
	}
}

func TestIntegrationOrdinaryAdmissionFreshSourceCaptureIsAllowed(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 0)
	applyMigrationsThrough(t, ctx, pool, ordinaryFencePredecessor)
	input := ordinaryFenceSourceInput()
	source, err := evidenceingestion.CaptureManualSource(ctx, pool, input)
	if err != nil {
		t.Fatalf("capture synthetic source before ordinary migration: %v", err)
	}
	var emptyHeadCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM canonical_supersession_admission_head
		WHERE chain_key = 'canonical-supersession/v1' AND revision = 0 AND head_event_id IS NULL`).Scan(&emptyHeadCount); err != nil {
		t.Fatalf("read native seeded head: %v", err)
	}
	if emptyHeadCount != 1 {
		t.Fatalf("exact empty native head count = %d, want 1", emptyHeadCount)
	}
	before := ordinaryFenceRows(t, ctx, pool)
	changed, err := ApplyUpInSchema(ctx, pool, schema)
	if err != nil || !changed {
		t.Fatalf("source-only ApplyUpInSchema() = (%v, %v), want changed success", changed, err)
	}
	assertMigrationCount(t, ctx, pool, 45)
	if _, err := VerifyCurrentInSchema(ctx, pool, schema); err != nil {
		t.Fatalf("verify source-only upgrade: %v", err)
	}
	after := ordinaryFenceRows(t, ctx, pool)
	for table, rows := range before {
		if table != "schema_migrations" && after[table] != rows {
			t.Fatalf("source-only migration changed pre-existing table %s", table)
		}
	}
	replayed, err := evidenceingestion.CaptureManualSource(ctx, pool, input)
	wantReplay := source
	wantReplay.Replayed = true
	if err != nil || !reflect.DeepEqual(replayed, wantReplay) {
		t.Fatalf("source exact retry after upgrade did not retain the original source result: %v", err)
	}
}

func TestIntegrationReviewedBindingRejectsIntermediateOrdinaryHistory(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 0)
	applyMigrationsThrough(t, ctx, pool, ordinaryFenceMigration)
	proposalID := createOrdinaryFencePendingProposal(t, ctx, pool)
	createIntermediateOrdinaryFenceHistory(t, ctx, pool, proposalID)
	before := ordinaryFenceRows(t, ctx, pool)

	changed, err := ApplyUpInSchema(ctx, pool, schema)
	assertOrdinaryFenceFailure(t, changed, err, "migration 000045 requires no intermediate ordinary admission history; ordinary_manifests=1")
	if after := ordinaryFenceRows(t, ctx, pool); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected migration 45 changed persisted rows or the migration ledger")
	}
	assertMigrationCount(t, ctx, pool, 44)
	assertMigrationTableExists(t, ctx, pool, "canonical_ordinary_admission_manifests")
	assertMigrationTableMissing(t, ctx, pool, "canonical_source_claim_review_bindings")
	assertMigrationColumnMissing(t, ctx, pool, "admission_decisions", "review_binding_contract_version")
	var bindingHelper *string
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('canonical_source_claim_review_binding_assert_v1(text)')::text`).Scan(&bindingHelper); err != nil {
		t.Fatalf("read migration 45 helper after rejected upgrade: %v", err)
	}
	if bindingHelper != nil {
		t.Fatal("rejected migration 45 left its review binding helper installed")
	}
}

func ordinaryFenceSourceInput() evidenceingestion.ManualTextInput {
	return evidenceingestion.ManualTextInput{
		RequestID: "migration-fence-source-request", SourceID: "migration-fence-source",
		SourceVersion: "v1", Raw: []byte("Synthetic migration fence source.\n"), AttemptNumber: 1,
	}
}

func createOrdinaryFencePendingProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	result, err := evidenceingestion.IngestManualText(ctx, pool, ordinaryFenceSourceInput(), evidenceingestion.FrozenExtractorOutput{
		Proposals: []evidenceingestion.ExtractorProposalOutput{{
			ProposalLocalID: "stmt-1", StatementText: "Synthetic migration fence source.", EvidenceRefs: []string{"span:S1"},
		}},
	})
	if err != nil {
		t.Fatalf("create synthetic pending migration fixture: %v", err)
	}
	return result.ProposalOccurrenceID
}

// This is a schema-44-only owner fixture, not a runtime admission path or a
// human decision. Current typed writers require migration 45, so the fixture
// commits a complete ordinary graph under every intermediate DB constraint.
func createIntermediateOrdinaryFenceHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposalID string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin intermediate ordinary fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, query := range []string{
		`INSERT INTO canonical_graph_nodes (canonical_node_id, node_kind, payload, provenance, temporal, integrity, origin_proposal_occurrence_id)
		 VALUES ('fence-claim', 'source_claim', '{}', '{}', '{}', '{}', $1), ('fence-raw', 'raw_evidence', '{}', '{}', '{}', '{}', $1)`,
		`INSERT INTO canonical_graph_edges (canonical_edge_id, from_node_id, to_node_id, relation, provenance, origin_proposal_occurrence_id)
		 VALUES ('fence-edge', 'fence-raw', 'fence-claim', 'supports_claim', '{}', $1)`,
		`UPDATE proposal_occurrences SET admission_outcome = 'admitted', canonical_ref = 'fence-claim' WHERE proposal_occurrence_id = $1`,
		`INSERT INTO admission_decisions (admission_decision_id, proposal_occurrence_id, outcome, canonical_ref, raw_evidence_node_ids, canonical_edge_ids, decision_by, decision_reason)
		 VALUES ('fence-decision', $1, 'admitted', 'fence-claim', '["fence-raw"]', '["fence-edge"]', 'test-stub:migration-fence', 'APPROVAL STUB: synthetic intermediate schema fixture')`,
		`INSERT INTO canonical_ordinary_admission_manifests (admission_decision_id, proposal_occurrence_id, contract_version, mutation_kind, canonical_ref)
		 VALUES ('fence-decision', $1, 'ordinary-admission/v1', 'source_backed_claim', 'fence-claim')`,
	} {
		if _, err := tx.Exec(ctx, query, proposalID); err != nil {
			t.Fatalf("write intermediate ordinary fixture: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO canonical_ordinary_admission_node_bindings (admission_decision_id, binding_role, binding_position, canonical_node_id, materialization)
		VALUES ('fence-decision', 'canonical_ref', 0, 'fence-claim', 'materialized'), ('fence-decision', 'raw_evidence', 0, 'fence-raw', 'materialized');
		INSERT INTO canonical_ordinary_admission_edge_bindings (admission_decision_id, binding_role, binding_position, canonical_edge_id, materialization)
		VALUES ('fence-decision', 'supports_claim', 0, 'fence-edge', 'materialized')
	`); err != nil {
		t.Fatalf("write intermediate ordinary bindings: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit complete intermediate ordinary fixture: %v", err)
	}
}

func assertOrdinaryFenceFailure(t *testing.T, changed bool, err error, message string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if changed || !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.Message, message) {
		t.Fatalf("migration fence result = (%v, %v), want unchanged and expected 23514 fence rejection", changed, err)
	}
}

// Preserve every row, including exact ledger checksums, on a rejected upgrade.
// The helper reads only its new synthetic schema and never logs source data.
func ordinaryFenceRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = current_schema() ORDER BY tablename`)
	if err != nil {
		t.Fatalf("list migration fixture tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read migration fixture table names: %v", err)
	}
	result := make(map[string]string, len(tables))
	for _, table := range tables {
		var body string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text), '[]'::jsonb)::text FROM `+pgx.Identifier{table}.Sanitize()+` AS r`).Scan(&body); err != nil {
			t.Fatalf("snapshot fixture table %s: %v", table, err)
		}
		result[table] = body
	}
	return result
}
