//go:build integration

package evidenceingestion

import (
	"context"
	"testing"
)

// Ordinary legacy compatibility must never turn missing/changed historical
// classification into ordinary authority. Corruption stays in rolled-back
// owner transactions of the disposable fixture.
func TestIntegrationHistoricalMetadataRejectsMissingOrChangedClass(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parent := historicalSources(t, ctx, pool, 1, false)[0]
	req := historicalProposal(t, ctx, pool, "class-reader", parent)
	h, err := ApplyHistoricalDerivationDecision(ctx, pool, historicalDecision(t, ctx, pool, req, "class-reader-admit"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"NULL", "'ordinary'", "'unrecognized'"} {
		t.Run(value, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, `ALTER TABLE canonical_derivations DISABLE TRIGGER USER; ALTER TABLE canonical_derivations ALTER COLUMN use_class DROP NOT NULL; ALTER TABLE canonical_derivations DROP CONSTRAINT canonical_derivation_use_class_ck`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE canonical_derivations SET use_class=`+value+` WHERE node_id=$1`, h.Admission.CanonicalRef); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadAdmissionDecisionResult(ctx, pgxTx{tx: tx}, req.ProposalOccurrenceID); err == nil {
				t.Fatal("historical metadata silently became ordinary")
			}
		})
	}
}
