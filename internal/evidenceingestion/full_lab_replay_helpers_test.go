//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fullLabReplayPool preserves the original fixture isolation using product migrations.
func fullLabReplayPool(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	ctx, pool := integrationPool(t)
	return ctx, pool, ""
}

// fullLabConvert only adapts valid persisted output shapes; raw inputs never pass through JSON.
func fullLabConvert[T any](value any) (T, error) {
	var out T
	b, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func fullLabContradictionProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, left, right, rationale string) CanonicalContradictionProposalResult {
	t.Helper()
	result, err := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{
		RequestID: id, NodeAID: left, NodeBID: right, Rationale: rationale,
		ProducerName: "frozen-lab-fixture", ProducerVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// fullLabFault raises after a native insert in this test's disposable schema.
// Existing product guards stay enabled; no production writer is replaced.
func fullLabFault(ctx context.Context, pool *pgxpool.Pool, point string) (func() error, error) {
	if point == "" {
		return func() error { return nil }, nil
	}
	tables := map[string]string{"definition": "canonical_propositions", "membership": "canonical_proposition_bindings", "event": "canonical_proposition_binding_events"}
	table, ok := tables[point]
	if !ok {
		return nil, fmt.Errorf("unknown replay fault point %q", point)
	}
	_, err := pool.Exec(ctx, `CREATE FUNCTION full_lab_replay_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'FULL LAB INJECTED'; END $$; CREATE TRIGGER full_lab_replay_fault AFTER INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION full_lab_replay_fault()`)
	if err != nil {
		return nil, err
	}
	return func() error {
		_, err := pool.Exec(context.Background(), `DROP TRIGGER full_lab_replay_fault ON `+table+`; DROP FUNCTION full_lab_replay_fault()`)
		return err
	}, nil
}
