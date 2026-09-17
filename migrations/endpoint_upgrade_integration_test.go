//go:build integration

package migrations

import (
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestIntegrationEndpointUpgradeRetainsNativeHistory(t *testing.T) {
	ctx, pool, schema := migrationTestPoolWithMaxConns(t, 0)
	applyMigrationsThrough(t, ctx, pool, "000048_evidence_ingestion_references_admission.up.sql")
	proposal := createOrdinaryFencePendingProposal(t, ctx, pool)
	approval := evidenceingestion.AdmissionInput{ProposalOccurrenceID: proposal, DecisionBy: "synthetic:migration-review", DecisionReason: "APPROVE STUB: preserve historical native admission"}
	admitted, err := evidenceingestion.AdmitPendingProposal(ctx, pool, approval)
	if err != nil {
		t.Fatal(err)
	}
	before := ordinaryFenceRows(t, ctx, pool)
	changed, err := ApplyUpInSchema(ctx, pool, schema)
	if err != nil || !changed {
		t.Fatalf("endpoint upgrade: changed=%v err=%v", changed, err)
	}
	after := ordinaryFenceRows(t, ctx, pool)
	for name, rows := range before {
		if name != "schema_migrations" && !reflect.DeepEqual(rows, after[name]) {
			t.Fatalf("upgrade changed %s", name)
		}
	}
	replay, err := evidenceingestion.AdmitPendingProposal(ctx, pool, approval)
	if err != nil || !replay.Replayed || replay.CanonicalRef != admitted.CanonicalRef {
		t.Fatal("native admission replay changed")
	}
	canonical, err := evidenceingestion.GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil || canonical.EndpointAdmission != nil {
		t.Fatal("historical native admission was relabeled as exact endpoint review")
	}
	status, err := VerifyCurrentInSchema(ctx, pool, schema)
	if err != nil || status.AppliedMigrations != 49 {
		t.Fatal("endpoint schema not current")
	}
	again, err := ApplyUpInSchema(ctx, pool, schema)
	if err != nil || again {
		t.Fatal("endpoint migration is not idempotent")
	}
}
