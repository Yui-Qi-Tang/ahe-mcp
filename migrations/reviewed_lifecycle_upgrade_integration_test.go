//go:build integration

package migrations

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestIntegrationReviewedLifecycleUpgradeAndGuards(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 1)
	applyMigrationsThrough(t, ctx, pool, "000055_evidence_ingestion_candidate_relation_source.up.sql")
	proposal := createOrdinaryFencePendingProposal(t, ctx, pool)
	input := evidenceingestion.AdmissionInput{ProposalOccurrenceID: proposal, DecisionBy: "synthetic:upgrade", DecisionReason: "TEST APPROVAL STUB: native historical claim"}
	result, err := evidenceingestion.AdmitPendingProposal(ctx, pool, input)
	if err != nil {
		t.Fatal(err)
	}
	before := ordinaryFenceRows(t, ctx, pool)
	if _, err = ApplyUpInSchema(ctx, pool, schema); err != nil {
		t.Fatal(err)
	}
	after := ordinaryFenceRows(t, ctx, pool)
	for table, rows := range before {
		if table != "schema_migrations" && !reflect.DeepEqual(upgradeHistoricalRows(t, table, rows), upgradeHistoricalRows(t, table, after[table])) {
			t.Fatalf("upgrade rewrote historical %s", table)
		}
	}
	replay, err := evidenceingestion.AdmitPendingProposal(ctx, pool, input)
	if err != nil || !replay.Replayed || replay.CanonicalRef != result.CanonicalRef {
		t.Fatal("upgrade changed native historical replay")
	}
	for _, q := range []string{
		"ALTER TABLE endpoint_disposition_review_bindings DROP CONSTRAINT endpoint_disposition_review_bindings_pkey",
		"ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_supersession_review_contract_ck",
		"ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_disposition_review_contract_ck",
		"DROP TRIGGER endpoint_review_requests_reviewed_lifecycle ON endpoint_review_requests",
		"ALTER FUNCTION canonical_supersession_lock_targets_v1(text[]) SECURITY INVOKER",
		"ALTER FUNCTION canonical_supersession_event_authority_trigger() SECURITY INVOKER",
	} {

		t.Run(q, func(t *testing.T) {
			caseCtx, casePool, caseSchema := migrationTestPoolWithMaxConns(t, 1)
			if _, err := ApplyUpInSchema(caseCtx, casePool, caseSchema); err != nil {
				t.Fatal(err)
			}
			if _, err := casePool.Exec(caseCtx, q); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrentInSchema(caseCtx, casePool, caseSchema); !errors.Is(err, ErrSchemaNotCurrent) {
				t.Fatalf("altered lifecycle guard accepted: %s", q)
			}
		})

	}
	down, err := os.ReadFile("000056_reviewed_lifecycle_writers.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	parentDown, err := os.ReadFile("000057_reviewed_derivation_parent_limit.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	// No new lifecycle receipt exists: restoring the old schema must preserve history.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(parentDown)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyCurrentInSchema(ctx, pool, schema); err != nil {
		t.Fatal(err)
	}
}

// Compare all original fields and refuse any invented review classification.
func upgradeHistoricalRows(t *testing.T, table, raw string) string {
	t.Helper()
	if table != "admission_decisions" {
		return raw
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if value, exists := row["supersession_review_binding_contract_version"]; exists {
			if string(value) != "null" {
				t.Fatal("upgrade relabeled a historical decision as reviewed supersession")
			}
			delete(row, "supersession_review_binding_contract_version")
		}
	}
	result, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(result)
}
