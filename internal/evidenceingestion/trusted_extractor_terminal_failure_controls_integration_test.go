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

// The frozen before test covers runner failure after committed success. These
// controls cover materialization failure and the opposite terminal ordering,
// without opening another writer surface or changing trusted-runner retries.
func TestIntegrationTrustedExtractorTerminalFailureControls(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstFails    bool
		lateBehavior  string
		lateErrorKind ErrorKind
	}{
		{"invalid_output_after_success", false, "invalid-output", ErrorUnknownSpan},
		{"runner_failure_after_failure", true, "runner-failure", ErrorRunnerInvocationFailed},
		{"valid_output_after_failure", true, "valid-output", ErrorPersistedAttemptFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentCtx, pool := integrationPool(t)
			ctx, cancel := context.WithTimeout(parentCtx, 20*time.Second)
			defer cancel()
			input, fixture := integrationInputFixture(t, "terminal-control-source")
			source, err := CaptureManualSource(ctx, pool, input)
			if err != nil {
				t.Fatalf("capture mock source: %v", err)
			}
			validOutput, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			var invalidFixture FrozenExtractorOutput
			if err := json.Unmarshal(validOutput, &invalidFixture); err != nil {
				t.Fatal(err)
			}
			invalidFixture.Proposals[0].EvidenceRefs = []string{"span:missing"}
			invalidOutput, err := json.Marshal(invalidFixture)
			if err != nil {
				t.Fatal(err)
			}
			request := TrustedExtractorRequest{
				RequestID: "terminal-control-request", ExtractionViewID: source.ExtractionViewID,
				ExtractorDefinition: testExternalExtractorDefinition(),
			}
			type callResult struct {
				result IngestResult
				err    error
			}
			var calls sync.WaitGroup
			start := func(output []byte, runnerError error) (<-chan struct{}, chan<- struct{}, <-chan callResult) {
				entered, release := make(chan struct{}), make(chan struct{})
				result := make(chan callResult, 1)
				calls.Go(func() {
					got, err := RunTrustedExtractor(ctx, pool, request, func(runnerCtx context.Context, _ ExtractorInput) ([]byte, error) {
						close(entered)
						select {
						case <-release:
							return output, runnerError
						case <-runnerCtx.Done():
							return nil, runnerCtx.Err()
						}
					})
					result <- callResult{result: got, err: err}
				})
				return entered, release, result
			}
			firstOutput := validOutput
			var firstError error
			if tc.firstFails {
				firstOutput = []byte("first failed invocation output")
				firstError = errors.New("synthetic first terminal failure")
			}
			lateOutput := validOutput
			var lateError error
			switch tc.lateBehavior {
			case "invalid-output":
				lateOutput = invalidOutput
			case "runner-failure":
				lateOutput = nil
				lateError = errors.New("synthetic different late failure")
			}
			firstEntered, releaseFirst, firstResult := start(firstOutput, firstError)
			lateEntered, releaseLate, lateResult := start(lateOutput, lateError)
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
					t.Error("terminal-control runners did not join before database cleanup")
				}
			})
			for _, entered := range []<-chan struct{}{firstEntered, lateEntered} {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("both control runners must enter from the same started attempt")
				}
			}
			receive := func(result <-chan callResult) callResult {
				t.Helper()
				select {
				case got := <-result:
					return got
				case <-ctx.Done():
					t.Fatal("terminal-control call did not complete")
					return callResult{}
				}
			}
			close(releaseFirst)
			first := receive(firstResult)
			if tc.firstFails {
				assertKind(t, first.err, ErrorRunnerInvocationFailed)
			} else if first.err != nil || first.result.Replayed || first.result.ProposalCount != 1 {
				t.Fatalf("first valid invocation did not commit one pending proposal: %+v, %v", first.result, first.err)
			}
			type terminalState struct {
				status          string
				outputHash      string
				fixture         []byte
				failureClass    string
				failureMetadata []byte
				completedAt     time.Time
			}
			readState := func() terminalState {
				t.Helper()
				var state terminalState
				if err := pool.QueryRow(ctx, `
					SELECT status, COALESCE(output_hash, ''), COALESCE(fixture_output, 'null'::jsonb),
					       COALESCE(failure_class, ''), COALESCE(failure_metadata, 'null'::jsonb), completed_at
					FROM extraction_attempts
				`).Scan(&state.status, &state.outputHash, &state.fixture, &state.failureClass, &state.failureMetadata, &state.completedAt); err != nil {
					t.Fatalf("read terminal control witness: %v", err)
				}
				return state
			}
			before := readState()
			expectedStatus, expectedProposals := attemptStatusSucceeded, 1
			if tc.firstFails {
				expectedStatus, expectedProposals = attemptStatusFailed, 0
			}
			if before.status != expectedStatus || before.outputHash == "" || before.completedAt.IsZero() {
				t.Fatal("first terminal state was not committed before releasing the late invocation")
			}
			close(releaseLate)
			late := receive(lateResult)
			assertKind(t, late.err, tc.lateErrorKind)
			after := readState()
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("late invocation changed first terminal state: status %q -> %q or terminal payload changed", before.status, after.status)
			}
			replay, err := RunTrustedExtractor(ctx, pool, request, func(context.Context, ExtractorInput) ([]byte, error) {
				t.Error("exact retry invoked a runner after terminal completion")
				return nil, errors.New("unexpected terminal-control replay invocation")
			})
			if tc.firstFails {
				assertKind(t, err, ErrorPersistedAttemptFailed)
			} else if err != nil || !replay.Replayed || replay.ProposalOccurrenceID != first.result.ProposalOccurrenceID {
				t.Fatalf("successful terminal replay changed: %+v, %v", replay, err)
			}
			for _, table := range []string{"extraction_runs", "extraction_attempts"} {
				assertTableCount(t, ctx, pool, table, 1)
			}
			for _, table := range []string{"proposal_batches", "proposal_occurrences"} {
				assertTableCount(t, ctx, pool, table, expectedProposals)
			}
			for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "admission_decisions"} {
				assertTableCount(t, ctx, pool, table, 0)
			}
		})
	}
}
