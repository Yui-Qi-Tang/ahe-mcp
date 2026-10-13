//go:build integration

package migrations

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationGovernanceReadinessRejectsTamperedAuthority(t *testing.T) {
	for name, sql := range map[string]string{
		"missing common guard":  `DROP TRIGGER evidence_governance_touch ON canonical_graph_edges`,
		"disabled source guard": `ALTER TABLE source_snapshots DISABLE TRIGGER evidence_governance_touch`,
		"mutable event":         `DROP TRIGGER canonical_withdrawal_events_immutable_rows ON canonical_withdrawal_events`,
		"public helper":         `GRANT EXECUTE ON FUNCTION evidence_governance_lock_v1(regclass) TO PUBLIC`,
		"wrong helper security": `ALTER FUNCTION evidence_governance_lock_v1(regclass) SECURITY INVOKER`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, pool := migrationTestPool(t)
			if _, err := ApplyUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyCurrent(ctx, pool); !errors.Is(err, ErrSchemaNotCurrent) {
				t.Fatalf("tampered authority accepted: %v", err)
			}
		})
	}
}

func TestIntegrationGovernanceUpgradePreservesHistoryAndRollbackRefusesEvents(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// TEST APPROVAL STUB: disposable synthetic history, never operational review.
	p, err := evidenceingestion.IngestManualText(ctx, pool, evidenceingestion.ManualTextInput{SourceID: "governance-upgrade", SourceVersion: "v1", Raw: []byte("Synthetic historical statement."), RequestID: "governance-upgrade"}, evidenceingestion.FrozenExtractorOutput{Proposals: []evidenceingestion.ExtractorProposalOutput{{ProposalLocalID: "claim", StatementText: "Synthetic historical statement.", EvidenceRefs: []string{"span:S1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := evidenceingestion.AdmitPendingProposal(ctx, pool, evidenceingestion.AdmissionInput{ProposalOccurrenceID: p.ProposalOccurrenceID, DecisionBy: "TEST APPROVAL STUB", DecisionReason: "synthetic historical admission"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := evidenceingestion.GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("000058_evidence_governance.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE migration_name='000058_evidence_governance.up.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if changed, err := ApplyUp(ctx, pool); err != nil || !changed {
		t.Fatal("native upgrade failed", changed, err)
	}
	if _, err := VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
	after, err := evidenceingestion.GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade rewrote evidence history", err)
	}
	review, err := evidenceingestion.LoadGovernanceReview(ctx, pool, evidenceingestion.GovernanceRequest{Kind: "withdrawal", TargetID: admitted.CanonicalRef, Operation: "withdraw"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := evidenceingestion.ApplyGovernanceDecision(ctx, pool, evidenceingestion.GovernanceDecision{RequestID: "upgrade-withdraw", Review: review.Display.Request, ExpectedSubject: review.Subject, Decision: "approved", Reason: "TEST APPROVAL STUB", ReviewerID: "TEST APPROVAL STUB"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, string(down))
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatal("populated rollback accepted", err)
	}
	history, err := evidenceingestion.ReadGovernanceHistory(ctx, pool, "withdrawal", admitted.CanonicalRef)
	if err != nil || len(history) != 1 || history[0].EventID != receipt.EventID {
		t.Fatal("failed rollback changed events", err)
	}
	if _, err := VerifyCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
