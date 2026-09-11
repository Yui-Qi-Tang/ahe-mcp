//go:build integration

package evidenceingestion

import (
	"bytes"
	"testing"
)

func TestIntegrationOrdinaryAdmissionReplayRejectsChangedDecisionMetadata(t *testing.T) {
	for _, field := range []string{"decision_by", "decision_reason"} {
		t.Run(field, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			input, fixture := integrationInputFixture(t, "ordinary-decision-replay")
			proposal, err := IngestManualText(ctx, pool, input, fixture)
			if err != nil {
				t.Fatal(err)
			}
			request := AdmissionInput{
				ProposalOccurrenceID: proposal.ProposalOccurrenceID,
				DecisionBy:           "reviewer:first", DecisionReason: "approved exact fixture statement",
			}
			first, err := AdmitPendingProposal(ctx, pool, request)
			if err != nil || first.Replayed {
				t.Fatalf("first ordinary admission: result=%+v error=%v", first, err)
			}
			readDecision := func() []byte {
				t.Helper()
				var row []byte
				if err := pool.QueryRow(ctx, `SELECT to_jsonb(decision) FROM admission_decisions AS decision
					WHERE admission_decision_id=$1`, first.AdmissionDecisionID).Scan(&row); err != nil {
					t.Fatal(err)
				}
				return row
			}
			before := readDecision()
			changed := request
			if field == "decision_by" {
				changed.DecisionBy = "reviewer:other"
			} else {
				changed.DecisionReason = "a different decision reason"
			}
			result, err := AdmitPendingProposal(ctx, pool, changed)
			// The explicit contract is exact decision replay, not a second
			// review silently answered with the first review's receipt.
			if kind, ok := KindOf(err); !ok || kind != ErrorKind("admission_replay_conflict") {
				t.Errorf("changed %s: result=%+v error=%v, want admission_replay_conflict", field, result, err)
			}
			replay, err := AdmitPendingProposal(ctx, pool, request)
			if err != nil || !replay.Replayed || replay.AdmissionDecisionID != first.AdmissionDecisionID || replay.CanonicalRef != first.CanonicalRef {
				t.Fatalf("original exact replay: result=%+v error=%v", replay, err)
			}
			if !bytes.Equal(before, readDecision()) {
				t.Fatal("replay changed the original persisted decision")
			}
			assertTableCount(t, ctx, pool, "admission_decisions", 1)
			assertTableCount(t, ctx, pool, "canonical_graph_nodes", 2)
			assertTableCount(t, ctx, pool, "canonical_graph_edges", 1)
		})
	}
}
