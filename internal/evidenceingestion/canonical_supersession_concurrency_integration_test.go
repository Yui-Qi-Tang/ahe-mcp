//go:build integration

package evidenceingestion

import (
	"context"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceprojection"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationConcurrentCanonicalSupersessionAdmissionReplaysOneMutation(t *testing.T) {
	ctx, pool := integrationPool(t)
	currentNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-replay-current", "supersession-concurrent-replay-current-source")
	replacedNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-replay-old", "supersession-concurrent-replay-old-source")
	proposal, err := SubmitCanonicalSupersessionProposal(
		ctx,
		pool,
		canonicalSupersessionIntegrationInput("supersession-concurrent-replay", currentNodeID, replacedNodeID),
	)
	if err != nil {
		t.Fatalf("SubmitCanonicalSupersessionProposal() error = %v", err)
	}
	input := CanonicalSupersessionAdmissionInput{
		ProposalID:     proposal.Proposal.ID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "concurrent exact admission replay",
	}
	calls := runBlockedConcurrentSupersessionAdmissions(t, ctx, pool, 1, input, input)

	newAdmissions := 0
	replays := 0
	decisionID := ""
	for _, call := range calls {
		if call.err != nil {
			t.Fatalf("concurrent exact admission error = %v", call.err)
		}
		if decisionID == "" {
			decisionID = call.result.Decision.ID
		} else if call.result.Decision.ID != decisionID {
			t.Fatalf("concurrent decisions differ: got %s and %s", decisionID, call.result.Decision.ID)
		}
		if call.result.Replayed {
			replays++
		} else {
			newAdmissions++
		}
	}
	if newAdmissions != 1 || replays != 1 {
		t.Fatalf("concurrent exact admissions = new %d replayed %d, want 1/1", newAdmissions, replays)
	}
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 1)
	assertSupersessionEdgeCount(t, ctx, pool, 1)
	assertNoCanonicalSupersessionCycle(t, ctx, pool, []string{currentNodeID, replacedNodeID})
}

func TestIntegrationConcurrentCanonicalSupersessionAdmissionAndDispositionHaveOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	currentNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-terminal-current", "supersession-concurrent-terminal-current-source")
	replacedNodeID := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-terminal-old", "supersession-concurrent-terminal-old-source")
	proposal, err := SubmitCanonicalSupersessionProposal(
		ctx,
		pool,
		canonicalSupersessionIntegrationInput("supersession-concurrent-terminal", currentNodeID, replacedNodeID),
	)
	if err != nil {
		t.Fatalf("SubmitCanonicalSupersessionProposal() error = %v", err)
	}

	start := make(chan struct{})
	calls := make(chan canonicalSupersessionTerminalCall, 2)
	go func() {
		<-start
		_, err := AdmitPendingCanonicalSupersession(ctx, pool, CanonicalSupersessionAdmissionInput{
			ProposalID:     proposal.Proposal.ID,
			DecisionBy:     "integration-reviewer",
			DecisionReason: "concurrent admission decision",
		})
		calls <- canonicalSupersessionTerminalCall{operation: admissionOutcomeAdmitted, err: err}
	}()
	go func() {
		<-start
		_, err := RecordPendingCanonicalSupersessionDisposition(ctx, pool, CanonicalSupersessionDispositionInput{
			ProposalID:     proposal.Proposal.ID,
			Outcome:        ProposalDispositionRejected,
			DecisionBy:     "integration-reviewer",
			DecisionReason: "concurrent rejection decision",
		})
		calls <- canonicalSupersessionTerminalCall{operation: ProposalDispositionRejected, err: err}
	}()
	close(start)

	winner := ""
	conflicts := 0
	for range 2 {
		call := receiveCanonicalSupersessionTerminalCall(t, calls)
		if call.err == nil {
			if winner != "" {
				t.Fatalf("multiple successful terminal decisions: %s and %s", winner, call.operation)
			}
			winner = call.operation
			continue
		}
		if kind, ok := KindOf(call.err); !ok || kind != ErrorAdmissionStateConflict {
			t.Fatalf("concurrent terminal error = %v, want admission state conflict", call.err)
		}
		conflicts++
	}
	if winner == "" || conflicts != 1 {
		t.Fatalf("concurrent terminal result = winner %q conflicts %d, want one/one", winner, conflicts)
	}

	stored, err := GetCanonicalSupersessionProposal(ctx, pool, proposal.Proposal.ID)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionProposal() error = %v", err)
	}
	if stored.Decision == nil || stored.Proposal.AdmissionOutcome != winner || stored.Decision.Outcome != winner {
		t.Fatalf("stored terminal winner = %+v, want %s", stored, winner)
	}
	wantEdges := 0
	if winner == admissionOutcomeAdmitted {
		wantEdges = 1
		if stored.Decision.CanonicalEdgeID == "" {
			t.Fatalf("admitted winner lacks canonical edge: %+v", stored.Decision)
		}
	} else if stored.Decision.CanonicalEdgeID != "" {
		t.Fatalf("non-admitted winner has canonical edge: %+v", stored.Decision)
	}
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 1)
	assertSupersessionEdgeCount(t, ctx, pool, wantEdges)
	assertNoCanonicalSupersessionCycle(t, ctx, pool, []string{currentNodeID, replacedNodeID})
}

func TestIntegrationConcurrentOpposingCanonicalSupersessionsAdmitOneDirection(t *testing.T) {
	ctx, pool := integrationPool(t)
	nodeA := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-opposing-a", "supersession-concurrent-opposing-source-a")
	nodeB := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-opposing-b", "supersession-concurrent-opposing-source-b")
	proposalAB := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-a-b", nodeA, nodeB)
	proposalBA := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-b-a", nodeB, nodeA)

	calls := runBlockedConcurrentSupersessionAdmissions(
		t,
		ctx,
		pool,
		2,
		canonicalSupersessionConcurrentAdmissionInput(proposalAB.ID),
		canonicalSupersessionConcurrentAdmissionInput(proposalBA.ID),
	)
	assertConcurrentCanonicalSupersessionCycleWinner(t, calls)
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 1)
	assertSupersessionEdgeCount(t, ctx, pool, 1)
	assertNoCanonicalSupersessionCycle(t, ctx, pool, []string{nodeA, nodeB})
	assertCanonicalSupersessionProposalOutcomes(t, ctx, pool, []string{proposalAB.ID, proposalBA.ID}, 1, 1)
}

func TestIntegrationConcurrentCanonicalSupersessionMultiHopClosureAdmitsOneEdge(t *testing.T) {
	ctx, pool := integrationPool(t)
	v1 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-multihop-v1", "supersession-concurrent-multihop-source-v1")
	v2 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-multihop-v2", "supersession-concurrent-multihop-source-v2")
	v3 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-multihop-v3", "supersession-concurrent-multihop-source-v3")

	base := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-multihop-v2-v1", v2, v1)
	if _, err := AdmitPendingCanonicalSupersession(ctx, pool, canonicalSupersessionConcurrentAdmissionInput(base.ID)); err != nil {
		t.Fatalf("admit base supersession: %v", err)
	}
	v3ToV2 := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-multihop-v3-v2", v3, v2)
	v1ToV3 := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-multihop-v1-v3", v1, v3)

	calls := runBlockedConcurrentSupersessionAdmissions(
		t,
		ctx,
		pool,
		2,
		canonicalSupersessionConcurrentAdmissionInput(v3ToV2.ID),
		canonicalSupersessionConcurrentAdmissionInput(v1ToV3.ID),
	)
	assertConcurrentCanonicalSupersessionCycleWinner(t, calls)
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 2)
	assertSupersessionEdgeCount(t, ctx, pool, 2)
	assertNoCanonicalSupersessionCycle(t, ctx, pool, []string{v1, v2, v3})
	assertCanonicalSupersessionProposalOutcomes(t, ctx, pool, []string{v3ToV2.ID, v1ToV3.ID}, 1, 1)
}

func TestIntegrationConcurrentAcyclicCanonicalSupersessionsBothAdmit(t *testing.T) {
	ctx, pool := integrationPool(t)
	v1 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-acyclic-v1", "supersession-concurrent-acyclic-source-v1")
	v2 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-acyclic-v2", "supersession-concurrent-acyclic-source-v2")
	v3 := ingestAndAdmitSupersessionFixture(t, ctx, pool, "supersession-concurrent-acyclic-v3", "supersession-concurrent-acyclic-source-v3")
	v2ToV1 := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-acyclic-v2-v1", v2, v1)
	v3ToV2 := submitCanonicalSupersessionIntegrationProposal(t, ctx, pool, "supersession-concurrent-acyclic-v3-v2", v3, v2)

	calls := runBlockedConcurrentSupersessionAdmissions(
		t,
		ctx,
		pool,
		2,
		canonicalSupersessionConcurrentAdmissionInput(v2ToV1.ID),
		canonicalSupersessionConcurrentAdmissionInput(v3ToV2.ID),
	)
	for _, call := range calls {
		if call.err != nil {
			t.Fatalf("concurrent acyclic admission error = %v", call.err)
		}
		if call.result.Replayed || call.result.Decision.CanonicalEdgeID == "" {
			t.Fatalf("concurrent acyclic admission = %+v", call.result)
		}
	}
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_decisions", 2)
	assertSupersessionEdgeCount(t, ctx, pool, 2)
	assertNoCanonicalSupersessionCycle(t, ctx, pool, []string{v1, v2, v3})
	assertCanonicalSupersessionProposalOutcomes(t, ctx, pool, []string{v2ToV1.ID, v3ToV2.ID}, 2, 0)
}

type canonicalSupersessionAdmissionCall struct {
	result CanonicalSupersessionDecisionResult
	err    error
}

type canonicalSupersessionTerminalCall struct {
	operation string
	err       error
}

func runBlockedConcurrentSupersessionAdmissions(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantWaiters int,
	inputs ...CanonicalSupersessionAdmissionInput,
) []canonicalSupersessionAdmissionCall {
	t.Helper()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin supersession admission blocker: %v", err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, supersessionAdmissionLockKey); err != nil {
		t.Fatalf("hold supersession admission lock: %v", err)
	}

	start := make(chan struct{})
	calls := make(chan canonicalSupersessionAdmissionCall, len(inputs))
	for _, input := range inputs {
		go func(input CanonicalSupersessionAdmissionInput) {
			<-start
			result, err := AdmitPendingCanonicalSupersession(ctx, pool, input)
			calls <- canonicalSupersessionAdmissionCall{result: result, err: err}
		}(input)
	}
	close(start)
	waitForCanonicalSupersessionAdmissionWaiters(t, ctx, pool, wantWaiters)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release supersession admission lock: %v", err)
	}

	results := make([]canonicalSupersessionAdmissionCall, 0, len(inputs))
	for range inputs {
		select {
		case call := <-calls:
			results = append(results, call)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent supersession admission")
		}
	}
	return results
}

func waitForCanonicalSupersessionAdmissionWaiters(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	lockKey := uint64(supersessionAdmissionLockKey)
	classID := int64(uint32(lockKey >> 32))
	objectID := int64(uint32(lockKey))
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var got int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_locks
			WHERE locktype = 'advisory'
				AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
				AND classid::bigint = $1
				AND objid::bigint = $2
				AND objsubid = 1
				AND NOT granted
		`, classID, objectID).Scan(&got); err != nil {
			t.Fatalf("query supersession admission waiters: %v", err)
		}
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("supersession admission waiters = %d, want at least %d", got, want)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for supersession admission waiters: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func receiveCanonicalSupersessionTerminalCall(
	t *testing.T,
	calls <-chan canonicalSupersessionTerminalCall,
) canonicalSupersessionTerminalCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent supersession terminal decision")
		return canonicalSupersessionTerminalCall{}
	}
}

func assertConcurrentCanonicalSupersessionCycleWinner(
	t *testing.T,
	calls []canonicalSupersessionAdmissionCall,
) {
	t.Helper()
	successes := 0
	cycleFailures := 0
	for _, call := range calls {
		if call.err == nil {
			successes++
			if call.result.Replayed || call.result.Decision.CanonicalEdgeID == "" {
				t.Fatalf("successful concurrent cycle decision = %+v", call.result)
			}
			continue
		}
		if kind, ok := KindOf(call.err); !ok || kind != ErrorSupersessionInvariant {
			t.Fatalf("concurrent cycle error = %v, want supersession invariant", call.err)
		}
		cycleFailures++
	}
	if successes != 1 || cycleFailures != 1 {
		t.Fatalf("concurrent cycle results = successes %d cycle failures %d, want 1/1", successes, cycleFailures)
	}
}

func submitCanonicalSupersessionIntegrationProposal(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	requestID, fromNodeID, toNodeID string,
) CanonicalSupersessionProposal {
	t.Helper()
	result, err := SubmitCanonicalSupersessionProposal(
		ctx,
		pool,
		canonicalSupersessionIntegrationInput(requestID, fromNodeID, toNodeID),
	)
	if err != nil {
		t.Fatalf("submit canonical supersession %s: %v", requestID, err)
	}
	return result.Proposal
}

func canonicalSupersessionConcurrentAdmissionInput(proposalID string) CanonicalSupersessionAdmissionInput {
	return CanonicalSupersessionAdmissionInput{
		ProposalID:     proposalID,
		DecisionBy:     "integration-reviewer",
		DecisionReason: "concurrent canonical supersession admission",
	}
}

func assertCanonicalSupersessionProposalOutcomes(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proposalIDs []string,
	wantAdmitted, wantPending int,
) {
	t.Helper()
	admitted := 0
	pending := 0
	for _, proposalID := range proposalIDs {
		result, err := GetCanonicalSupersessionProposal(ctx, pool, proposalID)
		if err != nil {
			t.Fatalf("GetCanonicalSupersessionProposal(%s) error = %v", proposalID, err)
		}
		switch result.Proposal.AdmissionOutcome {
		case admissionOutcomeAdmitted:
			admitted++
			if result.Decision == nil || result.Decision.CanonicalEdgeID == "" {
				t.Fatalf("admitted proposal %s is incomplete: %+v", proposalID, result)
			}
		case admissionOutcomePending:
			pending++
			if result.Decision != nil || result.Proposal.CanonicalEdgeID != "" {
				t.Fatalf("pending proposal %s has terminal material: %+v", proposalID, result)
			}
		default:
			t.Fatalf("proposal %s outcome = %s", proposalID, result.Proposal.AdmissionOutcome)
		}
	}
	if admitted != wantAdmitted || pending != wantPending {
		t.Fatalf("proposal outcomes = admitted %d pending %d, want %d/%d", admitted, pending, wantAdmitted, wantPending)
	}
}

func assertSupersessionEdgeCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM canonical_graph_edges
		WHERE relation = 'supersedes'
	`).Scan(&got); err != nil {
		t.Fatalf("count supersession edges: %v", err)
	}
	if got != want {
		t.Fatalf("supersession edge count = %d, want %d", got, want)
	}
}

func assertNoCanonicalSupersessionCycle(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	nodeIDs []string,
) {
	t.Helper()
	view, err := ReadCanonicalGraphView(ctx, pool, CanonicalReadInput{
		RootNodeIDs: nodeIDs,
		Relations:   []evidencegraph.CanonicalEdgeRelation{evidencegraph.CanonicalSupersedes},
		MaxDepth:    len(nodeIDs),
		MaxNodes:    len(nodeIDs),
		MaxEdges:    len(nodeIDs) * len(nodeIDs),
	})
	if err != nil {
		t.Fatalf("ReadCanonicalGraphView() error = %v", err)
	}
	topology, err := evidenceprojection.PrepareTopology(view.Artifact)
	if err != nil {
		t.Fatalf("PrepareTopology() error = %v", err)
	}
	if cycle := topology.Diagnostics().SupersedesCycle; cycle != nil {
		t.Fatalf("canonical supersession cycle = %+v", cycle)
	}
}
