//go:build integration

package evidenceingestion

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationConcurrentCanonicalContradictionAdmissionReplaysOneMutation(t *testing.T) {
	ctx, pool := integrationPool(t)
	nodeA := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-concurrent-replay-a", "contradiction-concurrent-replay-source-a")
	nodeB := ingestAndAdmitContradictionFixture(t, ctx, pool, "contradiction-concurrent-replay-b", "contradiction-concurrent-replay-source-b")
	proposal, err := SubmitCanonicalContradictionProposal(ctx, pool, CanonicalContradictionProposalInput{
		RequestID:       "contradiction-concurrent-replay",
		NodeAID:         nodeA,
		NodeBID:         nodeB,
		Rationale:       "The two grounded claims are mutually incompatible.",
		ProducerName:    "integration-agent",
		ProducerVersion: "workflow-v1",
	})
	if err != nil {
		t.Fatalf("SubmitCanonicalContradictionProposal() error = %v", err)
	}
	input := CanonicalContradictionAdmissionInput{
		ProposalID:     proposal.Proposal.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "concurrent exact contradiction admission replay",
	}
	calls := runBlockedConcurrentContradictionAdmissions(t, ctx, pool, input, input)

	newAdmissions := 0
	replays := 0
	decisionID := ""
	for _, call := range calls {
		if call.err != nil {
			t.Fatalf("concurrent exact contradiction admission error = %v", call.err)
		}
		if decisionID == "" {
			decisionID = call.result.Decision.ID
		} else if call.result.Decision.ID != decisionID {
			t.Fatalf("concurrent contradiction decisions differ: got %s and %s", decisionID, call.result.Decision.ID)
		}
		if call.result.Replayed {
			replays++
		} else {
			newAdmissions++
		}
	}
	if newAdmissions != 1 || replays != 1 {
		t.Fatalf("concurrent exact contradiction admissions = new %d replayed %d, want 1/1", newAdmissions, replays)
	}
	assertTableCount(t, ctx, pool, "canonical_contradiction_admission_decisions", 1)
	assertCanonicalContradictionEdgeCount(t, ctx, pool, 1)
}

type canonicalContradictionAdmissionCall struct {
	result CanonicalContradictionDecisionResult
	err    error
}

func runBlockedConcurrentContradictionAdmissions(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	inputs ...CanonicalContradictionAdmissionInput,
) []canonicalContradictionAdmissionCall {
	t.Helper()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin contradiction admission blocker: %v", err)
	}
	defer blocker.Rollback(context.Background())
	var proposalID string
	if err := blocker.QueryRow(ctx, `
		SELECT canonical_contradiction_proposal_id
		FROM canonical_contradiction_proposals
		WHERE canonical_contradiction_proposal_id = $1
		FOR UPDATE
	`, inputs[0].ProposalID).Scan(&proposalID); err != nil {
		t.Fatalf("hold contradiction proposal lock: %v", err)
	}

	start := make(chan struct{})
	calls := make(chan canonicalContradictionAdmissionCall, len(inputs))
	for _, input := range inputs {
		go func(input CanonicalContradictionAdmissionInput) {
			<-start
			result, err := AdmitPendingCanonicalContradiction(ctx, pool, input)
			calls <- canonicalContradictionAdmissionCall{result: result, err: err}
		}(input)
	}
	close(start)
	waitForCanonicalContradictionAdmissionWaiters(t, ctx, pool, len(inputs))
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release contradiction proposal lock: %v", err)
	}

	results := make([]canonicalContradictionAdmissionCall, 0, len(inputs))
	for range inputs {
		select {
		case call := <-calls:
			results = append(results, call)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent contradiction admission")
		}
	}
	return results
}

func waitForCanonicalContradictionAdmissionWaiters(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var got int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
				AND pid <> pg_backend_pid()
				AND query LIKE '%canonical_contradiction_proposals%'
				AND query LIKE '%FOR UPDATE%'
				AND wait_event_type = 'Lock'
		`).Scan(&got); err != nil {
			t.Fatalf("query contradiction admission waiters: %v", err)
		}
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("contradiction admission waiters = %d, want at least %d", got, want)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for contradiction admission waiters: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertCanonicalContradictionEdgeCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM canonical_graph_edges
		WHERE relation = 'contradicts'
	`).Scan(&got); err != nil {
		t.Fatalf("count contradiction edges: %v", err)
	}
	if got != want {
		t.Fatalf("contradiction edge count = %d, want %d", got, want)
	}
}
