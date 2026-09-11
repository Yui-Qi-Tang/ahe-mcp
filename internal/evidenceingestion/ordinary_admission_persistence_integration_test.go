//go:build integration

package evidenceingestion

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationOrdinaryAdmissionExactReuseAndProducerConflict(t *testing.T) {
	for _, differentProducer := range []bool{false, true} {
		name := "exact_reuse"
		if differentProducer {
			name = "producer_body_conflict"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, fixture := integrationInputFixture(t, "ordinary-reuse-source")
			source, err := CaptureManualSource(ctx, pool, input)
			if err != nil {
				t.Fatal(err)
			}
			extraction := ExtractorOutputInput{
				RequestID: "ordinary-first-extraction", SourceSnapshotID: source.SourceSnapshotID,
				ExtractionViewID: source.ExtractionViewID, ExtractorDefinition: testExternalExtractorDefinition(), Output: fixture,
			}
			firstProposal, err := SubmitExtractorOutput(ctx, pool, extraction)
			if err != nil {
				t.Fatal(err)
			}
			extraction.RequestID = "ordinary-second-extraction"
			if differentProducer {
				extraction.ExtractorDefinition.Name += "-another-producer"
			}
			secondProposal, err := SubmitExtractorOutput(ctx, pool, extraction)
			if err != nil || secondProposal.ProposalOccurrenceID == firstProposal.ProposalOccurrenceID {
				t.Fatalf("distinct legal second proposal: result=%+v error=%v", secondProposal, err)
			}
			firstRequest := AdmissionInput{ProposalOccurrenceID: firstProposal.ProposalOccurrenceID}
			first, err := AdmitPendingProposal(ctx, pool, firstRequest)
			if err != nil {
				t.Fatal(err)
			}
			secondRequest := AdmissionInput{ProposalOccurrenceID: secondProposal.ProposalOccurrenceID}
			second, err := AdmitPendingProposal(ctx, pool, secondRequest)
			if differentProducer {
				assertKind(t, err, ErrorCanonicalAdmissionInvariant)
				proposal, err := TraceProposalProvenance(ctx, pool, secondProposal.ProposalOccurrenceID)
				if err != nil || proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
					t.Fatalf("body conflict changed pending proposal: result=%+v error=%v", proposal, err)
				}
				assertTableCount(t, ctx, pool, "admission_decisions", 1)
				assertTableCount(t, ctx, pool, "canonical_ordinary_admission_manifests", 1)
			} else {
				if err != nil || second.CanonicalRef != first.CanonicalRef || second.Replayed {
					t.Fatalf("independent exact duplicate admission: result=%+v error=%v", second, err)
				}
				for _, result := range []AdmissionResult{first, second} {
					var materializedNodes, reusedNodes, materializedEdges, reusedEdges int
					if err := pool.QueryRow(ctx, `SELECT
						(SELECT count(*) FROM canonical_ordinary_admission_node_bindings WHERE admission_decision_id=$1 AND materialization='materialized'),
						(SELECT count(*) FROM canonical_ordinary_admission_node_bindings WHERE admission_decision_id=$1 AND materialization='reused'),
						(SELECT count(*) FROM canonical_ordinary_admission_edge_bindings WHERE admission_decision_id=$1 AND materialization='materialized'),
						(SELECT count(*) FROM canonical_ordinary_admission_edge_bindings WHERE admission_decision_id=$1 AND materialization='reused')`,
						result.AdmissionDecisionID).Scan(&materializedNodes, &reusedNodes, &materializedEdges, &reusedEdges); err != nil {
						t.Fatal(err)
					}
					want := [4]int{2, 0, 1, 0}
					if result.AdmissionDecisionID == second.AdmissionDecisionID {
						want = [4]int{0, 2, 0, 1}
					}
					if got := [4]int{materializedNodes, reusedNodes, materializedEdges, reusedEdges}; got != want {
						t.Fatalf("materialization counts=%v, want %v", got, want)
					}
				}
				replay, err := AdmitPendingProposal(ctx, pool, secondRequest)
				if err != nil || !replay.Replayed || replay.AdmissionDecisionID != second.AdmissionDecisionID {
					t.Fatalf("reused graph exact replay: result=%+v error=%v", replay, err)
				}
				assertTableCount(t, ctx, pool, "admission_decisions", 2)
				assertTableCount(t, ctx, pool, "canonical_ordinary_admission_manifests", 2)
			}
			assertTableCount(t, ctx, pool, "canonical_graph_nodes", 2)
			assertTableCount(t, ctx, pool, "canonical_graph_edges", 1)
			var firstOrigin string
			if err := pool.QueryRow(ctx, `SELECT origin_proposal_occurrence_id FROM canonical_graph_nodes WHERE canonical_node_id=$1`, first.CanonicalRef).Scan(&firstOrigin); err != nil || firstOrigin != firstProposal.ProposalOccurrenceID {
				t.Fatalf("first materializer origin=%q error=%v", firstOrigin, err)
			}
		})
	}
}

func TestIntegrationOrdinaryAdmissionParallelExactAuthority(t *testing.T) {
	for _, mode := range []string{"exact", "decision_by", "decision_reason", "duplicate_proposal"} {
		t.Run(mode, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, fixture := integrationInputFixture(t, "ordinary-parallel-source")
			proposal, err := IngestManualText(ctx, pool, input, fixture)
			if err != nil {
				t.Fatal(err)
			}
			firstInput := AdmissionInput{ProposalOccurrenceID: proposal.ProposalOccurrenceID, DecisionBy: "reviewer:first", DecisionReason: "approved fixture"}
			secondInput := firstInput
			insertFragment := "SET admission_outcome = 'admitted'"
			waitFragment := "%FROM proposal_occurrences%"
			switch mode {
			case "decision_by":
				secondInput.DecisionBy = "reviewer:second"
			case "decision_reason":
				secondInput.DecisionReason = "different reason"
			case "duplicate_proposal":
				input.RequestID += "-another-extraction"
				secondProposal, err := IngestManualText(ctx, pool, input, fixture)
				if err != nil {
					t.Fatal(err)
				}
				secondInput.ProposalOccurrenceID = secondProposal.ProposalOccurrenceID
				insertFragment = "INSERT INTO canonical_graph_nodes"
				waitFragment = "%INSERT INTO canonical_graph_nodes%"
			}
			barrier := &ordinaryAdmissionBarrier{fragment: insertFragment, reached: make(chan struct{}), release: make(chan struct{}, 1)}
			firstPool := ordinaryAdmissionTestPool(t, ctx, pool, barrier)
			secondPool := ordinaryAdmissionTestPool(t, ctx, pool, nil)
			var secondPID uint32
			if err := secondPool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondPID); err != nil {
				t.Fatal(err)
			}
			callCtx, cancel := context.WithCancel(ctx)
			var workers sync.WaitGroup
			defer func() {
				cancel()
				close(barrier.release)
				workers.Wait()
			}()
			type callResult struct {
				result AdmissionResult
				err    error
			}
			firstDone, secondDone := make(chan callResult, 1), make(chan callResult, 1)
			workers.Go(func() {
				result, err := AdmitPendingProposal(callCtx, firstPool, firstInput)
				firstDone <- callResult{result, err}
			})
			select {
			case <-barrier.reached:
			case got := <-firstDone:
				t.Fatalf("first writer exited before transaction barrier: %+v", got)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			workers.Go(func() {
				result, err := AdmitPendingProposal(callCtx, secondPool, secondInput)
				secondDone <- callResult{result, err}
			})
			waitCtx, stopWait := context.WithTimeout(ctx, 5*time.Second)
			defer stopWait()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				if err := pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
					WHERE pid=$1 AND wait_event_type='Lock' AND query LIKE $2
					AND cardinality(pg_blocking_pids(pid))>0)`, secondPID, waitFragment).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case got := <-secondDone:
					t.Fatalf("second writer did not wait for the first transaction: %+v", got)
				case <-waitCtx.Done():
					t.Fatal("second writer never reached the expected PostgreSQL lock wait")
				case <-ticker.C:
				}
			}
			barrier.release <- struct{}{}
			var first, second callResult
			select {
			case first = <-firstDone:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case second = <-secondDone:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if first.err != nil || first.result.Replayed {
				t.Fatalf("first admission: %+v", first)
			}
			if mode == "exact" || mode == "duplicate_proposal" {
				if second.err != nil || second.result.CanonicalRef != first.result.CanonicalRef || second.result.Replayed != (mode == "exact") {
					t.Fatalf("second admission: %+v", second)
				}
			} else {
				assertKind(t, second.err, ErrorAdmissionReplayConflict)
			}
			replay, err := AdmitPendingProposal(ctx, pool, firstInput)
			if err != nil || !replay.Replayed || replay.AdmissionDecisionID != first.result.AdmissionDecisionID {
				t.Fatalf("first exact replay after parallel calls: result=%+v error=%v", replay, err)
			}
			decisions := 1
			if mode == "duplicate_proposal" {
				decisions = 2
			}
			assertTableCount(t, ctx, pool, "admission_decisions", decisions)
			assertTableCount(t, ctx, pool, "canonical_ordinary_admission_manifests", decisions)
			assertTableCount(t, ctx, pool, "canonical_graph_nodes", 2)
			assertTableCount(t, ctx, pool, "canonical_graph_edges", 1)
		})
	}
}

func TestIntegrationOrdinaryAdmissionReusesSupersessionRawMaterializer(t *testing.T) {
	ctx, pool := integrationPool(t)
	oldProposal := createSupersessionExternalProposal(t, ctx, pool, "ordinary-old", "r1", "Policy value is v1.")
	old, err := AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: oldProposal.ProposalOccurrenceID})
	if err != nil {
		t.Fatal(err)
	}
	replacement := createSupersessionExternalProposal(t, ctx, pool, "ordinary-replacement", "r2", "Policy value is v2.")
	supersessionInput := SupersessionAdmissionInput{
		ProposalOccurrenceID: replacement.ProposalOccurrenceID,
		DecisionBy:           "reviewer:fixture", DecisionReason: "approved fixture replacement",
		Basis: supersessionIntegrationBasis(), TargetNodeIDs: []string{old.CanonicalRef},
	}
	supersession, err := AdmitPendingSupersession(ctx, pool, supersessionInput)
	if err != nil {
		t.Fatal(err)
	}
	// The supersession claim and its supports edge carry their own ReviewRef.
	// Only its unchanged raw evidence body can be shared by this ordinary claim.
	another, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
		RequestID: "ordinary-same-supersession-source", SourceSnapshotID: replacement.SourceSnapshotID,
		ExtractionViewID:    replacement.ExtractionViewID,
		ExtractorDefinition: ExtractorDefinitionInput{Name: "supersession-integration-extractor", Version: "v1"},
		Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
			ProposalLocalID: "same-raw-another-claim", StatementText: "Policy value is v2", EvidenceRefs: []string{"span:S1"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := AdmissionInput{ProposalOccurrenceID: another.ProposalOccurrenceID}
	admission, err := AdmitPendingProposal(ctx, pool, input)
	if err != nil || admission.CanonicalRef == supersession.CanonicalRef || admission.Replayed ||
		len(admission.RawEvidenceNodeIDs) != 1 || admission.RawEvidenceNodeIDs[0] != supersession.RawEvidenceNodeIDs[0] {
		t.Fatalf("ordinary reuse of independent supersession raw materializer: result=%+v error=%v", admission, err)
	}
	replay, err := AdmitPendingProposal(ctx, pool, input)
	if err != nil || !replay.Replayed || replay.AdmissionDecisionID != admission.AdmissionDecisionID {
		t.Fatalf("ordinary exact replay after supersession-origin reuse: result=%+v error=%v", replay, err)
	}
	supersessionReplay, err := AdmitPendingSupersession(ctx, pool, supersessionInput)
	if err != nil || !supersessionReplay.Replayed || supersessionReplay.AdmissionEventID != supersession.AdmissionEventID {
		t.Fatalf("independent supersession exact replay changed: result=%+v error=%v", supersessionReplay, err)
	}
	assertTableCount(t, ctx, pool, "canonical_ordinary_admission_manifests", 2)
	assertTableCount(t, ctx, pool, "canonical_supersession_admission_events", 1)
	assertTableCount(t, ctx, pool, "admission_decisions", 3)
	assertTableCount(t, ctx, pool, "canonical_graph_nodes", 5)
	assertTableCount(t, ctx, pool, "canonical_graph_edges", 4)
}

func ordinaryAdmissionTestPool(t *testing.T, ctx context.Context, source *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	config := source.Config()
	config.MaxConns = 1
	config.ConnConfig.Tracer = tracer
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type ordinaryAdmissionBarrier struct {
	fragment string
	once     sync.Once
	reached  chan struct{}
	release  chan struct{}
}

type ordinaryAdmissionBarrierKey struct{}

func (b *ordinaryAdmissionBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, b.fragment) {
		return context.WithValue(ctx, ordinaryAdmissionBarrierKey{}, true)
	}
	return ctx
}

func (b *ordinaryAdmissionBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if marked, _ := ctx.Value(ordinaryAdmissionBarrierKey{}).(bool); !marked || data.Err != nil {
		return
	}
	b.once.Do(func() {
		close(b.reached)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
}
