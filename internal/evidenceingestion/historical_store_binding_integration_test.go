//go:build integration

package evidenceingestion

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Product nests the authoritative store ID in basis.use_review, unlike Core.
// The owner-only trigger deliberately corrupts a synthetic fresh write after
// native validation, with matching payload digests, to exercise the DB guard.
func TestIntegrationHistoricalProductStoreBinding(t *testing.T) {
	ctx, pool := integrationPoolWithMigrations(t)
	parents := historicalSources(t, ctx, pool, 1, false)
	for _, mutation := range []struct{ name, expression string }{
		{"missing", "b #- '{basis,use_review,store_id}'"},
		{"empty", "jsonb_set(b,'{basis,use_review,store_id}','\"\"'::jsonb)"},
		{"other_store", "jsonb_set(b,'{basis,use_review,store_id}','\"synthetic-other-store\"'::jsonb)"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			req := historicalProposal(t, ctx, pool, "store-"+mutation.name, parents...)
			in := historicalDecision(t, ctx, pool, req, "store-decision-"+mutation.name)
			before := historicalCounts(t, ctx, pool)
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION corrupt_history_store_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b jsonb; BEGIN
b:=NEW.display_payload::jsonb; NEW.display_payload:=(%s)::text;
NEW.review_subject:='historical-derivation-review:sha256:'||encode(sha256(convert_to(NEW.display_payload,'UTF8')),'hex');
NEW.receipt_payload:=jsonb_set(NEW.receipt_payload::jsonb,'{review_subject}',to_jsonb(NEW.review_subject))::text;
RETURN NEW; END $$;
CREATE TRIGGER corrupt_history_store_fixture BEFORE INSERT ON canonical_historical_derivation_reviews FOR EACH ROW EXECUTE FUNCTION corrupt_history_store_fixture()`, mutation.expression))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); err == nil || !strings.Contains(err.Error(), "historical exact review authority differs") {
				t.Fatalf("store corruption did not reach the native SQL authority guard: %v", err)
			}
			if !reflect.DeepEqual(before, historicalCounts(t, ctx, pool)) {
				t.Fatal("store mismatch partially committed")
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER corrupt_history_store_fixture ON canonical_historical_derivation_reviews; DROP FUNCTION corrupt_history_store_fixture()`); err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyHistoricalDerivationDecision(ctx, pool, in); err != nil {
				t.Fatal("valid store control", err)
			}
		})
	}
}
