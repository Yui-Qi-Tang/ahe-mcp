//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCanonicalSupersessionAuthorityRejectsPostAdmissionMutation(t *testing.T) {
	tests := []struct {
		name       string
		wantDetail string
		mutate     func(context.Context, *pgxpool.Pool, SupersessionAdmissionResult) error
	}{
		{
			name:       "lineage",
			wantDetail: "canonical supersession lineage is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_supersession_lineages
					SET slot_id = slot_id || '-drift'
					WHERE lineage_key = $1
				`, result.LineageKey)
				return err
			},
		},
		{
			name:       "event",
			wantDetail: "canonical supersession admission event is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_supersession_admission_events
					SET event_payload = jsonb_set(event_payload, '{kind}', '"forged"'::jsonb)
					WHERE event_id = $1
				`, result.AdmissionEventID)
				return err
			},
		},
		{
			name:       "head rollback",
			wantDetail: "canonical supersession head must advance exactly one event",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, _ SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_supersession_admission_head
					SET revision = 0, head_event_id = NULL
					WHERE chain_key = 'canonical-supersession/v1'
				`)
				return err
			},
		},
		{
			name:       "member",
			wantDetail: "canonical supersession member is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_supersession_members
					SET was_bootstrapped = NOT was_bootstrapped
					WHERE canonical_node_id = $1
				`, result.CanonicalRef)
				return err
			},
		},
		{
			name:       "member node",
			wantDetail: "canonical_graph_nodes is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_graph_nodes
					SET payload = jsonb_set(payload, '{authority_drift}', 'true'::jsonb, true)
					WHERE canonical_node_id = $1
				`, result.CanonicalRef)
				return err
			},
		},
		{
			name:       "decision",
			wantDetail: "admission_decisions is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE admission_decisions
					SET decision_reason = decision_reason || ' drift'
					WHERE admission_decision_id = $1
				`, result.AdmissionDecisionID)
				return err
			},
		},
		{
			name:       "proposal",
			wantDetail: "terminal proposal occurrence is immutable",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE proposal_occurrences
					SET statement_text = statement_text || ' drift'
					WHERE proposal_occurrence_id = $1
				`, result.ProposalOccurrenceID)
				return err
			},
		},
		{
			name:       "supersedes edge",
			wantDetail: "canonical_graph_edges is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_graph_edges
					SET provenance = jsonb_set(provenance, '{method}', '"forged"'::jsonb, true)
					WHERE canonical_edge_id = $1
				`, result.SupersedesEdgeIDs[0])
				return err
			},
		},
		{
			name:       "supports claim edge",
			wantDetail: "canonical_graph_edges is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_graph_edges
					SET provenance = jsonb_set(provenance, '{method}', '"forged"'::jsonb, true)
					WHERE canonical_edge_id = $1
				`, validationAuditSupportEdgeID(t, result))
				return err
			},
		},
		{
			name:       "generic edge promoted to supersedes",
			wantDetail: "canonical_graph_edges is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_graph_edges
					SET relation = 'supersedes'
					WHERE canonical_edge_id = (
						SELECT canonical_edge_id
						FROM canonical_graph_edges
						WHERE relation = 'supports_claim'
						  AND origin_proposal_occurrence_id <> $1
						ORDER BY canonical_edge_id
						LIMIT 1
					)
				`, result.ProposalOccurrenceID)
				return err
			},
		},
		{
			name:       "replacement target",
			wantDetail: "canonical supersession replacement target is append-only",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, result SupersessionAdmissionResult) error {
				_, err := pool.Exec(ctx, `
					UPDATE canonical_supersession_replacement_targets
					SET created_at = created_at + interval '1 second'
					WHERE event_id = $1
				`, result.AdmissionEventID)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			lineageKey, result := prepareIntegrationClosurePair(t, ctx, pool, "authority-"+strings.ReplaceAll(test.name, " ", "-"))
			wantCounts := supersessionAuthorityCounts(t, ctx, pool)
			wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
			if err != nil {
				t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
			}
			wantCurrentness := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)

			err = test.mutate(ctx, pool, result)
			validationAdmissionAssertAuthorityRejected(t, err, test.wantDetail)
			validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
			gotCurrentness := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
			if !reflect.DeepEqual(gotCurrentness, wantCurrentness) {
				t.Fatalf("currentness changed after rejected mutation: before=%+v after=%+v", wantCurrentness, gotCurrentness)
			}
		})
	}
}

func TestIntegrationCanonicalSupersessionAuthorityRejectsCoordinatedEdgeTargetDelete(t *testing.T) {
	ctx, pool := integrationPool(t)
	lineageKey, result := prepareIntegrationClosurePair(t, ctx, pool, "authority-coordinated-delete")
	wantCounts := supersessionAuthorityCounts(t, ctx, pool)
	wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}
	wantCurrentness := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin coordinated mutation: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		DELETE FROM canonical_supersession_replacement_targets
		WHERE event_id = $1
	`, result.AdmissionEventID); err != nil {
		t.Fatalf("delete target inside coordinated mutation: %v", err)
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM canonical_graph_edges
		WHERE canonical_edge_id = $1
	`, result.SupersedesEdgeIDs[0])
	// The shared immutable-graph guard now rejects the second mutation before
	// deferred supersession validation at COMMIT. The first DELETE must roll back.
	validationAdmissionAssertAuthorityRejected(t, err, "canonical_graph_edges is append-only")
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("roll back rejected coordinated mutation: %v", err)
	}
	validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
	gotCurrentness := integrationClosureCurrentnessReadOnly(t, ctx, pool, lineageKey)
	if !reflect.DeepEqual(gotCurrentness, wantCurrentness) {
		t.Fatalf("currentness changed after rejected coordinated mutation: before=%+v after=%+v", wantCurrentness, gotCurrentness)
	}
}

func validationAdmissionAssertAuthorityRejected(t *testing.T, err error, wantDetail string) {
	t.Helper()
	if err == nil {
		t.Fatal("authority mutation error = nil, want fail-closed rejection")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("authority mutation error = %v, want PostgreSQL 23514", err)
	}
	if !strings.Contains(pgErr.Message, wantDetail) {
		t.Fatalf("authority mutation error = %v, want detail %q", err, wantDetail)
	}
}

func TestIntegrationSupersessionAdmissionValidationRejectsAlteredReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()

	oldA := createSupersessionExternalProposal(t, ctx, pool, "validation-replay-old-a", "r1", "Policy value is v1.")
	oldAAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: oldA.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial source-backed claim A",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(old A) error = %v", err)
	}
	oldB := createSupersessionExternalProposal(t, ctx, pool, "validation-replay-old-b", "r1b", "Alternate policy value is v1.")
	oldBAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: oldB.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial source-backed claim B",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(old B) error = %v", err)
	}

	candidate := createSupersessionExternalProposal(t, ctx, pool, "validation-replay-new", "r2", "Policy value is v2.")
	input := SupersessionAdmissionInput{
		ProposalOccurrenceID: candidate.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "reviewed exact replacement",
		Basis:                basis,
		TargetNodeIDs:        []string{oldAAdmission.CanonicalRef},
	}
	admitted, err := AdmitPendingSupersession(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitPendingSupersession() error = %v", err)
	}
	wantCounts := supersessionAuthorityCounts(t, ctx, pool)
	wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SupersessionAdmissionInput)
	}{
		{
			name: "reviewer",
			mutate: func(value *SupersessionAdmissionInput) {
				value.DecisionBy = "different-reviewer"
			},
		},
		{
			name: "target set",
			mutate: func(value *SupersessionAdmissionInput) {
				value.TargetNodeIDs = []string{oldBAdmission.CanonicalRef}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := input
			changed.TargetNodeIDs = append([]string(nil), input.TargetNodeIDs...)
			test.mutate(&changed)
			_, err := AdmitPendingSupersession(ctx, pool, changed)
			validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
			assertKind(t, err, ErrorSupersessionReplayConflict)
		})
	}

	replayed, err := AdmitPendingSupersession(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(exact replay) error = %v", err)
	}
	if !replayed.Replayed || replayed.AdmissionEventID != admitted.AdmissionEventID || replayed.CanonicalRef != admitted.CanonicalRef {
		t.Fatalf("exact replay = %+v, want admitted event/ref from %+v", replayed, admitted)
	}
}

func TestIntegrationSupersessionAdmissionValidationExactParallelReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	old := createSupersessionExternalProposal(t, ctx, pool, "validation-parallel-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: old.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial source-backed claim",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(old) error = %v", err)
	}
	candidate := createSupersessionExternalProposal(t, ctx, pool, "validation-parallel-new", "r2", "Policy value is v2.")
	input := SupersessionAdmissionInput{
		ProposalOccurrenceID: candidate.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "parallel exact replay",
		Basis:                supersessionIntegrationBasis(),
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	}

	type outcome struct {
		result SupersessionAdmissionResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := AdmitPendingSupersession(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)

	results := make([]SupersessionAdmissionResult, 0, 2)
	writes := 0
	replays := 0
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("AdmitPendingSupersession(parallel exact request) error = %v", outcome.err)
		}
		results = append(results, outcome.result)
		if outcome.result.Replayed {
			replays++
		} else {
			writes++
		}
	}
	if writes != 1 || replays != 1 || len(results) != 2 {
		t.Fatalf("parallel exact outcomes writes=%d replays=%d results=%d, want 1/1/2", writes, replays, len(results))
	}
	if results[0].CanonicalRef != results[1].CanonicalRef ||
		results[0].AdmissionDecisionID != results[1].AdmissionDecisionID ||
		results[0].AdmissionEventID != results[1].AdmissionEventID ||
		!slices.Equal(results[0].CanonicalEdgeIDs, results[1].CanonicalEdgeIDs) ||
		!slices.Equal(results[0].SupersedesEdgeIDs, results[1].SupersedesEdgeIDs) {
		t.Fatalf("parallel exact results differ: first=%+v second=%+v", results[0], results[1])
	}
	assertSupersessionIntegrationCounts(t, ctx, pool, 1, 2, 1)
	head, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}
	if head.Revision != 1 || head.HeadEventID != results[0].AdmissionEventID {
		t.Fatalf("head = %+v, want revision 1 event %s", head, results[0].AdmissionEventID)
	}
}

func TestIntegrationSupersessionAdmissionValidationRejectsCrossSlotRebinding(t *testing.T) {
	ctx, pool := integrationPool(t)
	basis := supersessionIntegrationBasis()
	old := createSupersessionExternalProposal(t, ctx, pool, "validation-slot-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: old.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial source-backed claim",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(old) error = %v", err)
	}
	v2 := createSupersessionExternalProposal(t, ctx, pool, "validation-slot-v2", "r2", "Policy value is v2.")
	first, err := AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v2.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "bind description lineage",
		Basis:                basis,
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	})
	if err != nil {
		t.Fatalf("AdmitPendingSupersession(v2) error = %v", err)
	}
	v3 := createSupersessionExternalProposal(t, ctx, pool, "validation-slot-v3", "r3", "Policy value is v3.")
	wantCounts := supersessionAuthorityCounts(t, ctx, pool)
	wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}

	wrongSlot := basis
	wrongSlot.SlotID = "summary"
	_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: v3.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "must not rebind a description member as summary",
		Basis:                wrongSlot,
		TargetNodeIDs:        []string{first.CanonicalRef},
		ExpectedRevision:     first.EventRevision,
		ExpectedHeadEventID:  first.AdmissionEventID,
	})
	assertKind(t, err, ErrorSupersessionInvariant)
	validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
	validationAdmissionAssertProposalPending(t, ctx, pool, v3.ProposalOccurrenceID)
}

func TestIntegrationSupersessionAdmissionValidationRequiresExternalVerbatimEndpoints(t *testing.T) {
	t.Run("candidate", func(t *testing.T) {
		ctx, pool := integrationPool(t)
		old := createSupersessionExternalProposal(t, ctx, pool, "validation-endpoint-old", "r1", "Policy value is v1.")
		oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
			ProposalOccurrenceID: old.ProposalOccurrenceID,
			DecisionBy:           "yuki",
			DecisionReason:       "initial source-backed claim",
		})
		if err != nil {
			t.Fatalf("AdmitPendingProposal(old) error = %v", err)
		}
		manualInput, fixture := integrationInputFixture(t, "validation-admission-manual-candidate")
		manualCandidate, err := IngestManualText(ctx, pool, manualInput, fixture)
		if err != nil {
			t.Fatalf("IngestManualText(candidate) error = %v", err)
		}
		wantCounts := supersessionAuthorityCounts(t, ctx, pool)
		wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
		if err != nil {
			t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
		}

		_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
			ProposalOccurrenceID: manualCandidate.ProposalOccurrenceID,
			DecisionBy:           "yuki",
			DecisionReason:       "manual text is not a verbatim external candidate",
			Basis:                supersessionIntegrationBasis(),
			TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
		})
		assertKind(t, err, ErrorUnsupportedAdmission)
		validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
		validationAdmissionAssertProposalPending(t, ctx, pool, manualCandidate.ProposalOccurrenceID)
	})

	t.Run("target", func(t *testing.T) {
		ctx, pool := integrationPool(t)
		manualInput, fixture := integrationInputFixture(t, "validation-admission-manual-target")
		manualTarget, err := IngestManualText(ctx, pool, manualInput, fixture)
		if err != nil {
			t.Fatalf("IngestManualText(target) error = %v", err)
		}
		manualAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
			ProposalOccurrenceID: manualTarget.ProposalOccurrenceID,
			DecisionBy:           "yuki",
			DecisionReason:       "admit manual target fixture",
		})
		if err != nil {
			t.Fatalf("AdmitPendingProposal(manual target) error = %v", err)
		}
		candidate := createSupersessionExternalProposal(t, ctx, pool, "validation-endpoint-new", "r2", "Policy value is v2.")
		wantCounts := supersessionAuthorityCounts(t, ctx, pool)
		wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
		if err != nil {
			t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
		}

		_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
			ProposalOccurrenceID: candidate.ProposalOccurrenceID,
			DecisionBy:           "yuki",
			DecisionReason:       "manual text is not a verbatim external target",
			Basis:                supersessionIntegrationBasis(),
			TargetNodeIDs:        []string{manualAdmission.CanonicalRef},
		})
		assertKind(t, err, ErrorSupersessionInvariant)
		validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
		validationAdmissionAssertProposalPending(t, ctx, pool, candidate.ProposalOccurrenceID)
	})
}

func TestIntegrationSupersessionAdmissionValidationCommitFailureRollsBackAllRows(t *testing.T) {
	ctx, pool := integrationPool(t)
	old := createSupersessionExternalProposal(t, ctx, pool, "validation-commit-old", "r1", "Policy value is v1.")
	oldAdmission, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: old.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "initial source-backed claim",
	})
	if err != nil {
		t.Fatalf("AdmitPendingProposal(old) error = %v", err)
	}
	candidate := createSupersessionExternalProposal(t, ctx, pool, "validation-commit-new", "r2", "Policy value is v2.")
	wantCounts := supersessionAuthorityCounts(t, ctx, pool)
	wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION validation_admission_fail_supersession_commit()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			RAISE EXCEPTION 'validation admission forced commit failure';
		END;
		$$;

		CREATE CONSTRAINT TRIGGER validation_admission_fail_supersession_commit
		AFTER INSERT ON canonical_supersession_replacement_targets
		DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW EXECUTE FUNCTION validation_admission_fail_supersession_commit();
	`); err != nil {
		t.Fatalf("install deferred validation trigger: %v", err)
	}

	_, err = AdmitPendingSupersession(ctx, pool, SupersessionAdmissionInput{
		ProposalOccurrenceID: candidate.ProposalOccurrenceID,
		DecisionBy:           "yuki",
		DecisionReason:       "forced commit rollback",
		Basis:                supersessionIntegrationBasis(),
		TargetNodeIDs:        []string{oldAdmission.CanonicalRef},
	})
	if err == nil {
		t.Fatal("AdmitPendingSupersession() error = nil, want deferred commit failure")
	}
	if !strings.Contains(err.Error(), "commit transaction") ||
		!strings.Contains(err.Error(), "validation admission forced commit failure") {
		t.Fatalf("AdmitPendingSupersession() error = %v, want deferred commit failure detail", err)
	}
	validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
	validationAdmissionAssertProposalPending(t, ctx, pool, candidate.ProposalOccurrenceID)
}

func TestIntegrationSupersessionAdmissionValidationChecksReusedRawEvidenceMaterialization(t *testing.T) {
	tests := []struct {
		name            string
		secondExtractor string
		wantConflict    bool
	}{
		{name: "compatible shared raw evidence", secondExtractor: "validation-shared-raw-extractor"},
		{name: "producer drift", secondExtractor: "validation-different-extractor", wantConflict: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			firstProposal, secondProposal := validationAdmissionSharedRawProposals(
				t,
				ctx,
				pool,
				test.name,
				test.secondExtractor,
			)
			first, err := AdmitPendingProposal(ctx, pool, AdmissionInput{
				ProposalOccurrenceID: firstProposal.ProposalOccurrenceID,
				DecisionBy:           "yuki",
				DecisionReason:       "admit the first shared-span interpretation",
			})
			if err != nil {
				t.Fatalf("AdmitPendingProposal(first) error = %v", err)
			}
			wantCounts := supersessionAuthorityCounts(t, ctx, pool)
			wantHead, err := GetCanonicalSupersessionHead(ctx, pool)
			if err != nil {
				t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
			}

			input := SupersessionAdmissionInput{
				ProposalOccurrenceID: secondProposal.ProposalOccurrenceID,
				DecisionBy:           "yuki",
				DecisionReason:       "review the second shared-span interpretation",
				Basis:                supersessionIntegrationBasis(),
				TargetNodeIDs:        []string{first.CanonicalRef},
			}
			result, err := AdmitPendingSupersession(ctx, pool, input)
			if test.wantConflict {
				assertKind(t, err, ErrorSupersessionInvariant)
				validationAdmissionAssertAuthorityUnchanged(t, ctx, pool, wantCounts, wantHead)
				validationAdmissionAssertProposalPending(t, ctx, pool, secondProposal.ProposalOccurrenceID)
				return
			}
			if err != nil {
				t.Fatalf("AdmitPendingSupersession(shared raw) error = %v", err)
			}
			if !slices.Equal(result.RawEvidenceNodeIDs, first.RawEvidenceNodeIDs) {
				t.Fatalf("shared raw node IDs = %v, want %v", result.RawEvidenceNodeIDs, first.RawEvidenceNodeIDs)
			}
			replay, err := AdmitPendingSupersession(ctx, pool, input)
			if err != nil {
				t.Fatalf("AdmitPendingSupersession(shared raw replay) error = %v", err)
			}
			if !replay.Replayed || replay.AdmissionEventID != result.AdmissionEventID {
				t.Fatalf("shared raw replay = %+v, want event %s", replay, result.AdmissionEventID)
			}
		})
	}
}

func validationAdmissionSharedRawProposals(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	secondExtractor string,
) (IngestResult, IngestResult) {
	t.Helper()
	normalizedSuffix := strings.ReplaceAll(suffix, " ", "-")
	envelope := validExternalSourceEnvelope("validation-shared-raw-" + normalizedSuffix)
	envelope.Revision = "shared-raw-" + normalizedSuffix
	envelope.Content = "The reviewed shared raw span supports more than one interpretation.\n"
	envelope.ObservedAt = "2026-08-28T02:03:04Z"
	envelope.SourceUpdatedAt = ""
	source, err := CaptureExternalSource(ctx, pool, envelope)
	if err != nil {
		t.Fatalf("CaptureExternalSource(shared raw) error = %v", err)
	}

	submit := func(requestID, extractorName, statement string) IngestResult {
		t.Helper()
		result, err := SubmitExtractorOutput(ctx, pool, ExtractorOutputInput{
			RequestID:        requestID,
			SourceSnapshotID: source.SourceSnapshotID,
			ExtractionViewID: source.ExtractionViewID,
			ExtractorDefinition: ExtractorDefinitionInput{
				Name:    extractorName,
				Version: "v1",
			},
			Output: FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
				ProposalLocalID: "claim",
				StatementText:   statement,
				EvidenceRefs:    []string{"span:S1"},
			}}},
		})
		if err != nil {
			t.Fatalf("SubmitExtractorOutput(%s) error = %v", requestID, err)
		}
		return result
	}

	first := submit(
		"validation-shared-raw-first-"+normalizedSuffix,
		"validation-shared-raw-extractor",
		"The first reviewed interpretation is v1.",
	)
	second := submit(
		"validation-shared-raw-second-"+normalizedSuffix,
		secondExtractor,
		"The second reviewed interpretation is v2.",
	)
	return first, second
}

func validationAdmissionAssertAuthorityUnchanged(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantCounts supersessionIntegrationAuthorityCounts,
	wantHead CanonicalSupersessionHead,
) {
	t.Helper()
	if got := supersessionAuthorityCounts(t, ctx, pool); got != wantCounts {
		t.Fatalf("supersession authority counts changed from %+v to %+v", wantCounts, got)
	}
	gotHead, err := GetCanonicalSupersessionHead(ctx, pool)
	if err != nil {
		t.Fatalf("GetCanonicalSupersessionHead() error = %v", err)
	}
	if gotHead != wantHead {
		t.Fatalf("supersession head changed from %+v to %+v", wantHead, gotHead)
	}
}

func validationAdmissionAssertProposalPending(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	proposalOccurrenceID string,
) {
	t.Helper()
	proposal, err := GetProposalByOccurrenceID(ctx, pool, proposalOccurrenceID)
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID(%s) error = %v", proposalOccurrenceID, err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("proposal %s mutated = %+v", proposalOccurrenceID, proposal)
	}
}
