//go:build integration

package evidenceingestion

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationAdmitReviewedSourceClaimBindsExactDisplayAndReplays(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "reviewed-admit", "revision-1")
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)

	first, err := AdmitReviewedSourceClaim(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitReviewedSourceClaim(first) error = %v", err)
	}
	if first.Replayed || first.AdmissionOutcome != admissionOutcomeAdmitted || first.CanonicalRef == "" {
		t.Fatalf("first reviewed admission = %+v, want new admitted source claim", first)
	}
	assertPersistedSourceClaimReviewBinding(t, ctx, pool, first, input)
	beforeReplay := reviewedSourceClaimAuthorityCounts(t, ctx, pool)

	replay, err := AdmitReviewedSourceClaim(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitReviewedSourceClaim(replay) error = %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("reviewed admission replay = %+v, want Replayed", replay)
	}
	replay.Replayed = false
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("reviewed admission replay = %+v, want %+v", replay, first)
	}
	afterReplay := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	if afterReplay != beforeReplay {
		t.Fatalf("authority counts after replay = %+v, want unchanged %+v", afterReplay, beforeReplay)
	}

	changedReviewer := input
	changedReviewer.DecisionBy = "another-reviewer"
	_, err = AdmitReviewedSourceClaim(ctx, pool, changedReviewer)
	assertKind(t, err, ErrorAdmissionReplayConflict)
	changedReason := input
	changedReason.DecisionReason = "another reason"
	_, err = AdmitReviewedSourceClaim(ctx, pool, changedReason)
	assertKind(t, err, ErrorAdmissionReplayConflict)
	changedSubject := input
	changedSubject.ExpectedSubject.ReviewDisplayArtifactID = differentReviewStableID(changedSubject.ExpectedSubject.ReviewDisplayArtifactID)
	_, err = AdmitReviewedSourceClaim(ctx, pool, changedSubject)
	assertKind(t, err, ErrorAdmissionReplayConflict)
	if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != beforeReplay {
		t.Fatalf("authority counts after conflicting replays = %+v, want %+v", got, beforeReplay)
	}
	_, err = AdmitPendingProposal(ctx, pool, AdmissionInput{
		ProposalOccurrenceID: input.ExpectedSubject.ReviewSubject.ProposalOccurrenceID,
		DecisionBy:           input.DecisionBy,
		DecisionReason:       input.DecisionReason,
	})
	assertKind(t, err, ErrorAdmissionStateConflict)
}

func TestIntegrationAdmitReviewedSourceClaimPlainManualIsUnqualified(t *testing.T) {
	ctx, pool := integrationPool(t)
	input, fixture := integrationInputFixture(t, "reviewed-plain-manual")
	input.OriginMetadata = map[string]string{"title": "not a qualified manual profile", "coverage": "full_document"}
	pending, err := IngestManualText(ctx, pool, input, fixture)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadReviewableSourceClaimReviewSnapshot(ctx, pool, pending.ExtractionAttemptID, pending.ProposalOccurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	basis := snapshot.ReviewPackage.ProposalBasis
	if basis.SourceSystem != SourceSystemManualText || basis.SourceTitle != "" || basis.SourceLocation != "" || basis.SourceCoverage != "" || basis.SourceLimitations == nil || len(basis.SourceLimitations) != 0 {
		t.Fatal("plain manual review invented qualified source metadata")
	}
	reviewed := reviewedSourceClaimIntegrationInput(t, ctx, pool, pending.ExtractionAttemptID, pending.ProposalOccurrenceID)
	reviewed.DecisionReason = "APPROVAL STUB: synthetic plain manual fixture; not human approval"
	admitted, err := AdmitReviewedSourceClaim(ctx, pool, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	assertPersistedSourceClaimReviewBinding(t, ctx, pool, admitted, reviewed)
	canonical, err := GetCanonicalEvidenceByID(ctx, pool, admitted.CanonicalRef)
	if err != nil || canonical.OriginProposalOccurrenceID != pending.ProposalOccurrenceID || canonical.Payload.Claim != basis.StatementText {
		t.Fatalf("plain manual canonical readback differs: %v", err)
	}
}

func TestIntegrationAdmitReviewedSourceClaimRejectsStaleSubjectWithZeroWrites(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "stale-subject", "revision-1")
	valid := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	occurrenceIDs := reviewableIntegrationOccurrenceIDs(t, ctx, pool, fixture.ExtractionAttemptID)
	var siblingOccurrenceID string
	for _, occurrenceID := range occurrenceIDs {
		if occurrenceID != fixture.ProposalOccurrenceID {
			siblingOccurrenceID = occurrenceID
		}
	}
	if siblingOccurrenceID == "" {
		t.Fatal("review fixture has no sibling occurrence")
	}
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)

	tests := []struct {
		name   string
		mutate func(*ReviewedSourceClaimAdmissionInput)
	}{
		{name: "submission receipt", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.SubmissionReceiptID = differentReviewStableID(input.ExpectedSubject.ReviewSubject.SubmissionReceiptID)
		}},
		{name: "proposal manifest", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalManifestID = differentReviewStableID(input.ExpectedSubject.ReviewSubject.ProposalManifestID)
		}},
		{name: "proposal occurrence", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalOccurrenceID = siblingOccurrenceID
		}},
		{name: "proposal basis", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ProposalBasisID = differentReviewStableID(input.ExpectedSubject.ReviewSubject.ProposalBasisID)
		}},
		{name: "review package", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewSubject.ReviewPackageID = differentReviewStableID(input.ExpectedSubject.ReviewSubject.ReviewPackageID)
		}},
		{name: "display artifact", mutate: func(input *ReviewedSourceClaimAdmissionInput) {
			input.ExpectedSubject.ReviewDisplayArtifactID = differentReviewStableID(input.ExpectedSubject.ReviewDisplayArtifactID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := AdmitReviewedSourceClaim(ctx, pool, input)
			assertKind(t, err, ErrorReviewContractConflict)
			if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != before {
				t.Fatalf("authority counts after stale subject = %+v, want unchanged %+v", got, before)
			}
		})
	}
	proposal, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID() error = %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("proposal after stale subject = %+v, want pending without canonical authority", proposal)
	}
}

func TestIntegrationAdmitReviewedSourceClaimRejectsAuthorityDriftAfterDisplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "authority-drift", "revision-1")
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)

	commandTag, err := pool.Exec(ctx, `
		UPDATE source_snapshots
		SET origin_metadata = origin_metadata || '{"title":"changed after display"}'::jsonb
		WHERE source_snapshot_id = $1
	`, fixture.SourceSnapshotID)
	if err != nil {
		t.Fatalf("mutate source authority after display: %v", err)
	}
	if commandTag.RowsAffected() != 1 {
		t.Fatalf("source authority rows affected = %d, want 1", commandTag.RowsAffected())
	}

	_, err = AdmitReviewedSourceClaim(ctx, pool, input)
	assertKind(t, err, ErrorReviewContractConflict)
	if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != before {
		t.Fatalf("authority counts after source drift = %+v, want unchanged %+v", got, before)
	}
	proposal, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID() error = %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("proposal after source drift = %+v, want pending without canonical authority", proposal)
	}
}

func TestIntegrationReviewedSourceClaimReplayRetainsHistoricalDisplayAfterSourceDrift(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "historical-display", "revision-1")
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	first, err := AdmitReviewedSourceClaim(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitReviewedSourceClaim(first) error = %v", err)
	}
	var payloadBefore string
	if err := pool.QueryRow(ctx, `
		SELECT review_display_payload_utf8
		FROM canonical_source_claim_review_bindings
		WHERE admission_decision_id = $1
	`, first.AdmissionDecisionID).Scan(&payloadBefore); err != nil {
		t.Fatalf("load historical review display: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE source_snapshots
		SET origin_metadata = origin_metadata || '{"title":"changed after admission"}'::jsonb
		WHERE source_snapshot_id = $1
	`, fixture.SourceSnapshotID); err != nil {
		t.Fatalf("mutate source after admission: %v", err)
	}
	replay, err := AdmitReviewedSourceClaim(ctx, pool, input)
	if err != nil {
		t.Fatalf("AdmitReviewedSourceClaim(replay after source drift) error = %v", err)
	}
	if !replay.Replayed || replay.AdmissionDecisionID != first.AdmissionDecisionID {
		t.Fatalf("replay after source drift = %+v, want original decision replay", replay)
	}
	var payloadAfter string
	if err := pool.QueryRow(ctx, `
		SELECT review_display_payload_utf8
		FROM canonical_source_claim_review_bindings
		WHERE admission_decision_id = $1
	`, first.AdmissionDecisionID).Scan(&payloadAfter); err != nil {
		t.Fatalf("reload historical review display: %v", err)
	}
	if payloadAfter != payloadBefore {
		t.Fatal("historical review display changed after upstream source drift")
	}
}

func TestIntegrationAdmitReviewedSourceClaimsSequentiallyFromOneBatch(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "same-batch", "revision-1")
	occurrenceIDs := reviewableIntegrationOccurrenceIDs(t, ctx, pool, fixture.ExtractionAttemptID)
	if len(occurrenceIDs) != 2 {
		t.Fatalf("occurrence IDs = %v, want two", occurrenceIDs)
	}

	for _, occurrenceID := range occurrenceIDs {
		input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, occurrenceID)
		result, err := AdmitReviewedSourceClaim(ctx, pool, input)
		if err != nil {
			t.Fatalf("AdmitReviewedSourceClaim(%s) error = %v", occurrenceID, err)
		}
		if result.Replayed || result.CanonicalRef == "" {
			t.Fatalf("reviewed admission %s = %+v, want first admission", occurrenceID, result)
		}
	}
	counts := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	if counts.Decisions != 2 || counts.OrdinaryManifests != 2 || counts.ReviewBindings != 2 {
		t.Fatalf("same-batch authority counts = %+v, want two decisions/manifests/review bindings", counts)
	}
}

func TestIntegrationAdmitReviewedSourceClaimsConcurrentlyFromOneBatch(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "same-batch-concurrent", "revision-1")
	occurrenceIDs := reviewableIntegrationOccurrenceIDs(t, ctx, pool, fixture.ExtractionAttemptID)
	if len(occurrenceIDs) != 2 {
		t.Fatalf("occurrence IDs = %v, want two", occurrenceIDs)
	}
	inputs := make([]ReviewedSourceClaimAdmissionInput, 0, len(occurrenceIDs))
	for _, occurrenceID := range occurrenceIDs {
		inputs = append(inputs, reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, occurrenceID))
	}

	type outcome struct {
		result AdmissionResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(inputs))
	var workers sync.WaitGroup
	for _, input := range inputs {
		workers.Add(1)
		go func(input ReviewedSourceClaimAdmissionInput) {
			defer workers.Done()
			<-start
			result, err := AdmitReviewedSourceClaim(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}(input)
	}
	close(start)
	workers.Wait()
	close(outcomes)

	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("same-batch concurrent AdmitReviewedSourceClaim() error = %v", outcome.err)
		}
		if outcome.result.Replayed {
			t.Fatalf("same-batch concurrent result = %+v, want distinct first admission", outcome.result)
		}
	}
	counts := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	if counts.Decisions != 2 || counts.OrdinaryManifests != 2 || counts.ReviewBindings != 2 {
		t.Fatalf("same-batch concurrent authority counts = %+v, want two decisions/manifests/review bindings", counts)
	}
}

func TestIntegrationAdmitReviewedSourceClaimConcurrentExactReplay(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "concurrent-exact", "revision-1")
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)

	type outcome struct {
		result AdmissionResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := AdmitReviewedSourceClaim(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(outcomes)

	var results []AdmissionResult
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent AdmitReviewedSourceClaim() error = %v", outcome.err)
		}
		results = append(results, outcome.result)
	}
	if len(results) != 2 || results[0].AdmissionDecisionID != results[1].AdmissionDecisionID {
		t.Fatalf("concurrent results = %+v, want same decision", results)
	}
	var newCount, replayCount int
	for _, result := range results {
		if result.Replayed {
			replayCount++
		} else {
			newCount++
		}
	}
	if newCount != 1 || replayCount != 1 {
		t.Fatalf("concurrent results = %+v, want one new admission and one replay", results)
	}
	counts := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	if counts.Decisions != 1 || counts.OrdinaryManifests != 1 || counts.ReviewBindings != 1 {
		t.Fatalf("concurrent authority counts = %+v, want one decision/manifest/review binding", counts)
	}
}

func TestIntegrationAdmitReviewedSourceClaimConcurrentReviewerConflict(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "concurrent-reviewer", "revision-1")
	firstInput := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	secondInput := firstInput
	secondInput.DecisionBy = "competing-reviewer"
	secondInput.DecisionReason = "competing exact-package decision"

	type outcome struct {
		result AdmissionResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var workers sync.WaitGroup
	for _, input := range []ReviewedSourceClaimAdmissionInput{firstInput, secondInput} {
		workers.Add(1)
		go func(input ReviewedSourceClaimAdmissionInput) {
			defer workers.Done()
			<-start
			result, err := AdmitReviewedSourceClaim(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}(input)
	}
	close(start)
	workers.Wait()
	close(outcomes)

	var successes, conflicts int
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			successes++
			if outcome.result.Replayed {
				t.Fatalf("conflicting reviewer returned replay success: %+v", outcome.result)
			}
		case errorHasKind(outcome.err, ErrorAdmissionReplayConflict):
			conflicts++
		default:
			t.Fatalf("concurrent reviewer result error = %v, want nil or admission replay conflict", outcome.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent reviewer outcomes success/conflict = %d/%d, want 1/1", successes, conflicts)
	}
	counts := reviewedSourceClaimAuthorityCounts(t, ctx, pool)
	if counts.Decisions != 1 || counts.OrdinaryManifests != 1 || counts.ReviewBindings != 1 {
		t.Fatalf("concurrent reviewer authority counts = %+v, want one decision/manifest/review binding", counts)
	}
}

func TestIntegrationAdmitReviewedSourceClaimRollsBackWhenBindingIsSuppressed(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "binding-failure", "revision-1")
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION suppress_review_binding_insert_for_test()
		RETURNS TRIGGER
		LANGUAGE plpgsql
		AS $$
		BEGIN
			RETURN NULL;
		END $$;
		CREATE TRIGGER suppress_review_binding_insert_for_test
		BEFORE INSERT ON canonical_source_claim_review_bindings
		FOR EACH ROW EXECUTE FUNCTION suppress_review_binding_insert_for_test();
	`); err != nil {
		t.Fatalf("install review-binding suppression trigger: %v", err)
	}
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)

	if _, err := AdmitReviewedSourceClaim(ctx, pool, input); err == nil {
		t.Fatal("AdmitReviewedSourceClaim() error = nil, want deferred missing-binding failure")
	}
	if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != before {
		t.Fatalf("authority counts after binding failure = %+v, want rolled back %+v", got, before)
	}
	proposal, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID() error = %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("proposal after binding failure = %+v, want pending without canonical authority", proposal)
	}
}

func TestIntegrationAdmitReviewedSourceClaimRollsBackWhenDisplayIDDoesNotHashPayload(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "display-hash-failure", "revision-1")
	input := reviewedSourceClaimIntegrationInput(t, ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION change_review_display_id_for_test()
		RETURNS TRIGGER
		LANGUAGE plpgsql
		AS $$
		BEGIN
			NEW.review_display_artifact_id :=
				'review-display:v1:sha256:' || repeat('0', 64);
			RETURN NEW;
		END $$;
		CREATE TRIGGER change_review_display_id_for_test
		BEFORE INSERT ON canonical_source_claim_review_bindings
		FOR EACH ROW EXECUTE FUNCTION change_review_display_id_for_test();
	`); err != nil {
		t.Fatalf("install review-display ID mutation trigger: %v", err)
	}
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)

	if _, err := AdmitReviewedSourceClaim(ctx, pool, input); err == nil {
		t.Fatal("AdmitReviewedSourceClaim() error = nil, want display hash authority failure")
	}
	if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != before {
		t.Fatalf("authority counts after display hash failure = %+v, want rolled back %+v", got, before)
	}
	proposal, err := GetProposalByOccurrenceID(ctx, pool, fixture.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("GetProposalByOccurrenceID() error = %v", err)
	}
	if proposal.AdmissionOutcome != admissionOutcomePending || proposal.CanonicalRef != "" {
		t.Fatalf("proposal after display hash failure = %+v, want pending without canonical authority", proposal)
	}
}

func TestIntegrationReviewedGateCannotClaimLegacyAdmission(t *testing.T) {
	ctx, pool := integrationPool(t)
	fixture := persistReviewableSourceClaimIntegrationFixture(t, ctx, pool, "legacy-admit", "revision-1")
	artifact, subject, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, fixture.ExtractionAttemptID, fixture.ProposalOccurrenceID)
	if err != nil {
		t.Fatalf("LoadSourceClaimReviewDisplayArtifact() error = %v", err)
	}
	reviewed := ReviewedSourceClaimAdmissionInput{
		ExtractionAttemptID: fixture.ExtractionAttemptID,
		ExpectedSubject:     subject,
		DecisionBy:          "integration-reviewer",
		DecisionReason:      "exact review package approved for integration proof",
	}
	legacyInput := AdmissionInput{
		ProposalOccurrenceID: fixture.ProposalOccurrenceID,
		DecisionBy:           reviewed.DecisionBy,
		DecisionReason:       reviewed.DecisionReason,
	}
	legacy, err := AdmitPendingProposal(ctx, pool, legacyInput)
	if err != nil {
		t.Fatalf("AdmitPendingProposal() error = %v", err)
	}
	before := reviewedSourceClaimAuthorityCounts(t, ctx, pool)

	_, err = AdmitReviewedSourceClaim(ctx, pool, reviewed)
	assertKind(t, err, ErrorAdmissionReplayConflict)
	if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != before || got.ReviewBindings != 0 {
		t.Fatalf("authority counts after reviewed claim of legacy admission = %+v, want unchanged %+v with no binding", got, before)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin post-hoc review-binding fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO canonical_source_claim_review_bindings (
			admission_decision_id, proposal_occurrence_id, contract_version,
			review_contract_version, proposed_effect, extraction_attempt_id,
			submission_receipt_id, proposal_manifest_id, proposal_basis_id,
			review_package_id, review_display_artifact_id,
			review_display_media_type, review_display_payload_utf8
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		legacy.AdmissionDecisionID,
		legacy.ProposalOccurrenceID,
		ReviewedSourceClaimAdmissionV1,
		ReviewableIngestionContractV1,
		SourceClaimProposedEffectV1,
		fixture.ExtractionAttemptID,
		subject.ReviewSubject.SubmissionReceiptID,
		subject.ReviewSubject.ProposalManifestID,
		subject.ReviewSubject.ProposalBasisID,
		subject.ReviewSubject.ReviewPackageID,
		subject.ReviewDisplayArtifactID,
		artifact.MediaType,
		artifact.PayloadUTF8,
	); err != nil {
		t.Fatalf("insert post-hoc review binding before deferred check: %v", err)
	}
	if err := tx.Commit(ctx); err == nil || !strings.Contains(err.Error(), "reviewed decision/binding count mismatch") {
		t.Fatalf("post-hoc review-binding commit error = %v, want decision/binding mismatch", err)
	}
	if got := reviewedSourceClaimAuthorityCounts(t, ctx, pool); got != before || got.ReviewBindings != 0 {
		t.Fatalf("authority counts after post-hoc review-binding attempt = %+v, want unchanged %+v", got, before)
	}
}

func errorHasKind(err error, kind ErrorKind) bool {
	var domainErr *DomainError
	return errors.As(err, &domainErr) && domainErr.Kind == kind
}

func differentReviewStableID(id string) string {
	if id[len(id)-1] == '0' {
		return id[:len(id)-1] + "1"
	}
	return id[:len(id)-1] + "0"
}

func reviewedSourceClaimIntegrationInput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extractionAttemptID string,
	proposalOccurrenceID string,
) ReviewedSourceClaimAdmissionInput {
	t.Helper()
	_, subject, err := LoadSourceClaimReviewDisplayArtifact(ctx, pool, extractionAttemptID, proposalOccurrenceID)
	if err != nil {
		t.Fatalf("LoadSourceClaimReviewDisplayArtifact() error = %v", err)
	}
	return ReviewedSourceClaimAdmissionInput{
		ExtractionAttemptID: extractionAttemptID,
		ExpectedSubject:     subject,
		DecisionBy:          "integration-reviewer",
		DecisionReason:      "exact review package approved for integration proof",
	}
}

func reviewableIntegrationOccurrenceIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extractionAttemptID string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT proposal_occurrence_id
		FROM proposal_occurrences
		WHERE extraction_attempt_id = $1
		ORDER BY proposal_local_id
	`, extractionAttemptID)
	if err != nil {
		t.Fatalf("query reviewable occurrence IDs: %v", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan reviewable occurrence ID: %v", err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reviewable occurrence IDs: %v", err)
	}
	return result
}

type reviewedSourceClaimAuthorityCount struct {
	Nodes             int
	Edges             int
	Decisions         int
	OrdinaryManifests int
	ReviewBindings    int
}

func reviewedSourceClaimAuthorityCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) reviewedSourceClaimAuthorityCount {
	t.Helper()
	var result reviewedSourceClaimAuthorityCount
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM canonical_graph_nodes),
			(SELECT count(*) FROM canonical_graph_edges),
			(SELECT count(*) FROM admission_decisions),
			(SELECT count(*) FROM canonical_ordinary_admission_manifests),
			(SELECT count(*) FROM canonical_source_claim_review_bindings)
	`).Scan(
		&result.Nodes,
		&result.Edges,
		&result.Decisions,
		&result.OrdinaryManifests,
		&result.ReviewBindings,
	); err != nil {
		t.Fatalf("query reviewed source-claim authority counts: %v", err)
	}
	return result
}

func assertPersistedSourceClaimReviewBinding(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	result AdmissionResult,
	input ReviewedSourceClaimAdmissionInput,
) {
	t.Helper()
	var got sourceClaimReviewBinding
	if err := pool.QueryRow(ctx, `
		SELECT admission_decision_id, proposal_occurrence_id, contract_version,
			review_contract_version, proposed_effect, extraction_attempt_id,
			submission_receipt_id, proposal_manifest_id, proposal_basis_id,
			review_package_id, review_display_artifact_id,
			review_display_media_type, review_display_payload_utf8
		FROM canonical_source_claim_review_bindings
		WHERE admission_decision_id = $1
	`, result.AdmissionDecisionID).Scan(
		&got.AdmissionDecisionID,
		&got.ProposalOccurrenceID,
		&got.ContractVersion,
		&got.ReviewContractVersion,
		&got.ProposedEffect,
		&got.ExtractionAttemptID,
		&got.SubmissionReceiptID,
		&got.ProposalManifestID,
		&got.ProposalBasisID,
		&got.ReviewPackageID,
		&got.ReviewDisplayArtifactID,
		&got.ReviewDisplayMediaType,
		&got.ReviewDisplayPayload,
	); err != nil {
		t.Fatalf("load persisted source-claim review binding: %v", err)
	}
	if err := validatePersistedSourceClaimReviewBinding(got, result, input); err != nil {
		t.Fatalf("validate persisted source-claim review binding: %v", err)
	}
	var decisionContract *string
	if err := pool.QueryRow(ctx, `
		SELECT review_binding_contract_version
		FROM admission_decisions
		WHERE admission_decision_id = $1
	`, result.AdmissionDecisionID).Scan(&decisionContract); err != nil {
		t.Fatalf("load admission decision review contract: %v", err)
	}
	if decisionContract == nil || *decisionContract != ReviewedSourceClaimAdmissionV1 {
		t.Fatalf("admission decision review contract = %v", decisionContract)
	}
}
