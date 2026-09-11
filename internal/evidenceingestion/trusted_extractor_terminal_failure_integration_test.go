//go:build integration

package evidenceingestion

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Both public calls have started the same attempt before either runner is
// released. The successful call commits first; a late failure must not replace
// that terminal success or make its exact request stop replaying.
func TestIntegrationTrustedExtractorLateFailurePreservesCommittedSuccess(t *testing.T) {
	parentCtx, pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(parentCtx, 20*time.Second)
	input, fixture := integrationInputFixture(t, "late-runner-failure-source")
	source, err := CaptureManualSource(ctx, pool, input)
	if err != nil {
		cancel()
		t.Fatalf("capture mock source: %v", err)
	}
	fixtureData, err := json.Marshal(fixture)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request := TrustedExtractorRequest{
		RequestID:           "late-runner-failure-request",
		ExtractionViewID:    source.ExtractionViewID,
		ExtractorDefinition: testExternalExtractorDefinition(),
	}
	successEntered, failureEntered := make(chan struct{}), make(chan struct{})
	releaseSuccess, releaseFailure := make(chan struct{}), make(chan struct{})
	type callResult struct {
		result IngestResult
		err    error
	}
	successResult, failureResult := make(chan callResult, 1), make(chan callResult, 1)
	var calls sync.WaitGroup
	calls.Add(2)
	allDone := make(chan struct{})
	go func() {
		calls.Wait()
		close(allDone)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-allDone:
		case <-time.After(10 * time.Second):
			t.Error("trusted extractor calls did not join before database cleanup")
		}
	})
	go func() {
		defer calls.Done()
		result, err := RunTrustedExtractor(ctx, pool, request, func(runnerCtx context.Context, _ ExtractorInput) ([]byte, error) {
			close(successEntered)
			select {
			case <-releaseSuccess:
				return fixtureData, nil
			case <-runnerCtx.Done():
				return nil, runnerCtx.Err()
			}
		})
		successResult <- callResult{result: result, err: err}
	}()
	go func() {
		defer calls.Done()
		result, err := RunTrustedExtractor(ctx, pool, request, func(runnerCtx context.Context, _ ExtractorInput) ([]byte, error) {
			close(failureEntered)
			select {
			case <-releaseFailure:
				return nil, errors.New("synthetic late runner failure")
			case <-runnerCtx.Done():
				return nil, runnerCtx.Err()
			}
		})
		failureResult <- callResult{result: result, err: err}
	}()
	for _, entered := range []<-chan struct{}{successEntered, failureEntered} {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both runners must enter from the same started attempt")
		}
	}
	close(releaseSuccess)
	var succeeded callResult
	select {
	case succeeded = <-successResult:
	case <-ctx.Done():
		t.Fatal("successful runner did not complete")
	}
	if succeeded.err != nil || succeeded.result.Replayed || succeeded.result.ProposalCount != 1 {
		t.Fatalf("first runner did not commit one new pending proposal: %+v, %v", succeeded.result, succeeded.err)
	}
	type terminalState struct {
		status       string
		outputHash   string
		fixture      []byte
		failureClass string
		completedAt  time.Time
	}
	readState := func() terminalState {
		t.Helper()
		var state terminalState
		if err := pool.QueryRow(ctx, `
			SELECT status, COALESCE(output_hash, ''), COALESCE(fixture_output, 'null'::jsonb),
			       COALESCE(failure_class, ''), completed_at
			FROM extraction_attempts
			WHERE extraction_attempt_id = $1
		`, succeeded.result.ExtractionAttemptID).Scan(&state.status, &state.outputHash, &state.fixture, &state.failureClass, &state.completedAt); err != nil {
			t.Fatalf("read terminal attempt witness: %v", err)
		}
		return state
	}
	before := readState()
	if before.status != attemptStatusSucceeded || before.outputHash == "" || before.failureClass != "" || before.completedAt.IsZero() {
		t.Fatal("success was not persisted before releasing the late failure")
	}
	close(releaseFailure)
	select {
	case failed := <-failureResult:
		if failed.err == nil {
			t.Fatal("late failed invocation was reported as a new success")
		}
	case <-ctx.Done():
		t.Fatal("late failed runner did not complete")
	}
	after := readState()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("late failure overwrote committed success: status %q -> %q; terminal attempt fields changed", before.status, after.status)
	}
	replayed, err := RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
		t.Error("exact retry invoked a runner after committed success")
		return nil, errors.New("unexpected replay runner invocation")
	})
	if err != nil || !replayed.Replayed || replayed.ProposalOccurrenceID != succeeded.result.ProposalOccurrenceID ||
		replayed.ExtractionAttemptID != succeeded.result.ExtractionAttemptID {
		t.Fatalf("exact retry did not preserve the committed success: %+v, %v", replayed, err)
	}
	for _, table := range []string{"extraction_runs", "extraction_attempts", "proposal_batches", "proposal_occurrences"} {
		assertTableCount(t, ctx, pool, table, 1)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions"} {
		assertTableCount(t, ctx, pool, table, 0)
	}
}
