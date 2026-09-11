//go:build integration

package evidenceingestion

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Every decision below is synthetic test input, not authenticated human review.
func TestIntegrationReviewedDispositionBindsExactDisplayAndReplays(t *testing.T) {
	for _, outcome := range []string{ProposalDispositionRejected, ProposalDispositionAuditOnly} {
		t.Run(outcome, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "disposition-"+outcome, "revision-1")
			input := reviewedDispositionIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID, outcome)
			display, _, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, input.ExtractionAttemptID, fixture.ProposalOccurrenceID)
			if err != nil {
				t.Fatal(err)
			}
			first, err := RecordReviewedSourceClaimDisposition(ctx, pool, input)
			if err != nil || first.Replayed || first.AdmissionOutcome != outcome || first.AdmissionDecisionID == "" ||
				first.DecisionBy != input.DecisionBy || first.DecisionReason != input.DecisionReason {
				t.Fatalf("first disposition = %+v, error = %v", first, err)
			}
			assertReviewedDispositionCounts(t, ctx, pool, 1)
			var payload, artifactID, marker, reviewer, reason string
			if err := pool.QueryRow(ctx, `SELECT b.review_display_payload_utf8, b.review_display_artifact_id,
				d.disposition_review_binding_contract_version, d.decision_by, d.decision_reason
				FROM source_claim_disposition_review_bindings b JOIN admission_decisions d USING (admission_decision_id)
				WHERE d.admission_decision_id=$1`, first.AdmissionDecisionID).Scan(&payload, &artifactID, &marker, &reviewer, &reason); err != nil {
				t.Fatal(err)
			}
			if payload != display.PayloadUTF8 || artifactID != display.ID || marker != ReviewedSourceClaimDispositionV1 || reviewer != input.DecisionBy || reason != input.DecisionReason {
				t.Fatal("persisted disposition lost exact display, reviewer or reason")
			}
			if _, err := pool.Exec(ctx, `UPDATE source_snapshots SET origin_metadata=origin_metadata || '{"title":"source changed after terminal decision"}'::jsonb WHERE source_snapshot_id=$1`, fixture.SourceSnapshotID); err != nil {
				t.Fatal(err)
			}
			replay, err := RecordReviewedSourceClaimDisposition(ctx, pool, input)
			if err != nil || !replay.Replayed {
				t.Fatalf("exact historical replay: %+v %v", replay, err)
			}
			replay.Replayed = false
			if replay != first {
				t.Fatal("exact replay changed decision identities")
			}
			for _, tc := range []struct {
				name   string
				change func(*ReviewedSourceClaimDispositionInput)
			}{
				{"reviewer", func(i *ReviewedSourceClaimDispositionInput) { i.DecisionBy += "-changed" }},
				{"reason", func(i *ReviewedSourceClaimDispositionInput) { i.DecisionReason += " changed" }},
				{"display", func(i *ReviewedSourceClaimDispositionInput) {
					i.ExpectedSubject.ReviewDisplayArtifactID = differentReviewStableID(i.ExpectedSubject.ReviewDisplayArtifactID)
				}},
				{"basis", func(i *ReviewedSourceClaimDispositionInput) {
					i.ExpectedSubject.ReviewSubject.ProposalBasisID = differentReviewStableID(i.ExpectedSubject.ReviewSubject.ProposalBasisID)
				}},
				{"attempt", func(i *ReviewedSourceClaimDispositionInput) {
					i.ExtractionAttemptID = differentReviewStableID(i.ExtractionAttemptID)
				}},
				{"outcome", func(i *ReviewedSourceClaimDispositionInput) {
					if i.Outcome == ProposalDispositionRejected {
						i.Outcome = ProposalDispositionAuditOnly
					} else {
						i.Outcome = ProposalDispositionRejected
					}
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					changed := input
					tc.change(&changed)
					got, err := RecordReviewedSourceClaimDisposition(ctx, pool, changed)
					if err == nil || got != (ProposalDispositionResult{}) {
						t.Fatal("changed replay succeeded")
					}
				})
			}
			_, err = RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{ProposalOccurrenceID: fixture.ProposalOccurrenceID,
				Outcome: outcome, DecisionBy: input.DecisionBy, DecisionReason: input.DecisionReason})
			assertKind(t, err, ErrorAdmissionStateConflict)
			assertReviewedDispositionCounts(t, ctx, pool, 1)
			proposal, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
			if err != nil || proposal.AdmissionOutcome != outcome || proposal.CanonicalRef != "" {
				t.Fatal("terminal disposition proposal readback differs")
			}
			for _, sql := range []string{
				`UPDATE source_claim_disposition_review_bindings SET review_display_payload_utf8=review_display_payload_utf8`,
				`DELETE FROM source_claim_disposition_review_bindings`, `TRUNCATE source_claim_disposition_review_bindings`,
			} {
				if _, err := pool.Exec(ctx, sql); err == nil {
					t.Fatal("disposition review history was mutable")
				}
			}
			assertReviewedDispositionCounts(t, ctx, pool, 1)
		})
	}
}

func TestIntegrationReviewedDispositionRejectsStaleDisplayWithoutWrites(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "disposition-stale", "revision-1")
	valid := reviewedDispositionIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID, ProposalDispositionRejected)
	for _, change := range []func(*ReviewedSourceClaimDispositionInput){
		func(i *ReviewedSourceClaimDispositionInput) {
			i.ExpectedSubject.ReviewDisplayArtifactID = differentReviewStableID(i.ExpectedSubject.ReviewDisplayArtifactID)
		},
		func(i *ReviewedSourceClaimDispositionInput) {
			i.ExpectedSubject.ReviewSubject.SubmissionReceiptID = differentReviewStableID(i.ExpectedSubject.ReviewSubject.SubmissionReceiptID)
		},
		func(i *ReviewedSourceClaimDispositionInput) {
			i.ExpectedSubject.ReviewSubject.ProposalManifestID = differentReviewStableID(i.ExpectedSubject.ReviewSubject.ProposalManifestID)
		},
		func(i *ReviewedSourceClaimDispositionInput) {
			i.ExpectedSubject.ReviewSubject.ReviewPackageID = differentReviewStableID(i.ExpectedSubject.ReviewSubject.ReviewPackageID)
		},
		func(i *ReviewedSourceClaimDispositionInput) {
			i.ExpectedSubject.ReviewSubject.ProposalBasisID = differentReviewStableID(i.ExpectedSubject.ReviewSubject.ProposalBasisID)
		},
		func(i *ReviewedSourceClaimDispositionInput) {
			i.ExtractionAttemptID = differentReviewStableID(i.ExtractionAttemptID)
		},
	} {
		input := valid
		change(&input)
		got, err := RecordReviewedSourceClaimDisposition(ctx, pool, input)
		assertKind(t, err, ErrorReviewContractConflict)
		if got != (ProposalDispositionResult{}) {
			t.Fatal("stale display returned disposition")
		}
		assertReviewedDispositionCounts(t, ctx, pool, 0)
	}
	if _, err := pool.Exec(ctx, `UPDATE source_snapshots SET origin_metadata=origin_metadata || '{"title":"changed after display"}'::jsonb WHERE source_snapshot_id=$1`, fixture.SourceSnapshotID); err != nil {
		t.Fatal(err)
	}
	_, err := RecordReviewedSourceClaimDisposition(ctx, pool, valid)
	assertKind(t, err, ErrorReviewContractConflict)
	assertReviewedDispositionCounts(t, ctx, pool, 0)
}

func TestIntegrationReviewedDispositionCannotClaimLegacyTerminalDecision(t *testing.T) {
	for _, outcome := range []string{ProposalDispositionRejected, ProposalDispositionAuditOnly, admissionOutcomeAdmitted} {
		t.Run(outcome, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "disposition-legacy-"+outcome, "revision-1")
			input := reviewedDispositionIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID, ProposalDispositionRejected)
			var err error
			if outcome == admissionOutcomeAdmitted {
				_, err = AdmitPendingProposal(ctx, pool, AdmissionInput{ProposalOccurrenceID: fixture.ProposalOccurrenceID, DecisionBy: input.DecisionBy, DecisionReason: input.DecisionReason})
			} else {
				input.Outcome = outcome
				_, err = RecordPendingProposalDisposition(ctx, pool, ProposalDispositionInput{ProposalOccurrenceID: fixture.ProposalOccurrenceID,
					Outcome: outcome, DecisionBy: input.DecisionBy, DecisionReason: input.DecisionReason})
			}
			if err != nil {
				t.Fatal(err)
			}
			before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
			got, err := RecordReviewedSourceClaimDisposition(ctx, pool, input)
			if err == nil || got != (ProposalDispositionResult{}) {
				t.Fatal("reviewed writer adopted a legacy terminal decision")
			}
			if after := reviewedSourceClaimAuthorityCounts(t, ctx, pool); after != before {
				t.Fatal("legacy replay conflict changed authority")
			}
			assertTableCount(t, ctx, pool, "source_claim_disposition_review_bindings", 0)
		})
	}
}

func TestIntegrationReviewedDispositionRollsBackIncompleteBinding(t *testing.T) {
	for _, body := range []string{"RETURN NULL;", "NEW.review_display_payload_utf8 := NEW.review_display_payload_utf8 || ' '; RETURN NEW;"} {
		t.Run(body, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "disposition-binding-fault", "revision-1")
			input := reviewedDispositionIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID, ProposalDispositionAuditOnly)
			if _, err := pool.Exec(ctx, `CREATE FUNCTION disposition_binding_fault_test() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$;
				CREATE TRIGGER disposition_binding_fault_test BEFORE INSERT ON source_claim_disposition_review_bindings
				FOR EACH ROW EXECUTE FUNCTION disposition_binding_fault_test()`); err != nil {
				t.Fatal(err)
			}
			got, err := RecordReviewedSourceClaimDisposition(ctx, pool, input)
			if err == nil || got != (ProposalDispositionResult{}) {
				t.Fatal("incomplete binding transaction succeeded")
			}
			assertReviewedDispositionCounts(t, ctx, pool, 0)
			proposal, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
			if err != nil || proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
				t.Fatal("binding failure was not atomic")
			}
		})
	}
}

func TestIntegrationReviewedDispositionConcurrentDecisions(t *testing.T) {
	for _, opposite := range []bool{false, true} {
		name := "identical"
		if opposite {
			name = "opposite"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "disposition-race-"+name, "revision-1")
			inputs := [2]ReviewedSourceClaimDispositionInput{}
			inputs[0] = reviewedDispositionIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID, ProposalDispositionRejected)
			inputs[1] = inputs[0]
			if opposite {
				inputs[1].Outcome = ProposalDispositionAuditOnly
			}
			var results [2]ProposalDispositionResult
			var errs [2]error
			start := make(chan struct{})
			var wait sync.WaitGroup
			for i := range 2 {
				wait.Go(func() { <-start; results[i], errs[i] = RecordReviewedSourceClaimDisposition(ctx, pool, inputs[i]) })
			}
			close(start)
			wait.Wait()
			if opposite {
				if (errs[0] == nil) == (errs[1] == nil) {
					t.Fatalf("opposite decisions need one winner: %v", errs)
				}
				for i, err := range errs {
					if err != nil && (!errorHasKind(err, ErrorAdmissionStateConflict) || results[i] != (ProposalDispositionResult{})) {
						t.Fatalf("opposite conflict: %v", err)
					}
				}
			} else {
				if errs[0] != nil || errs[1] != nil || results[0].Replayed == results[1].Replayed {
					t.Fatalf("identical decisions did not converge: %+v %v", results, errs)
				}
				results[0].Replayed, results[1].Replayed = false, false
				if !reflect.DeepEqual(results[0], results[1]) {
					t.Fatal("identical decisions changed identities")
				}
			}
			assertReviewedDispositionCounts(t, ctx, pool, 1)
		})
	}
}

func reviewedDispositionIntegrationInput(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptID, occurrenceID, outcome string) ReviewedSourceClaimDispositionInput {
	t.Helper()
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, attemptID, occurrenceID)
	return ReviewedSourceClaimDispositionInput{ExtractionAttemptID: input.ExtractionAttemptID, ExpectedSubject: input.ExpectedSubject,
		Outcome: outcome, DecisionBy: input.DecisionBy, DecisionReason: "SYNTHETIC disposition test, not authenticated human review"}
}

func assertReviewedDispositionCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, decisions int) {
	t.Helper()
	for _, table := range []string{"admission_decisions", "source_claim_disposition_review_bindings"} {
		assertTableCount(t, ctx, pool, table, decisions)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "canonical_ordinary_admission_manifests",
		"canonical_ordinary_admission_node_bindings", "canonical_ordinary_admission_edge_bindings", "canonical_source_claim_review_bindings"} {
		assertTableCount(t, ctx, pool, table, 0)
	}
}
