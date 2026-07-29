//go:build integration

package evidenceingestion

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationRepositoryExtractionWorkRecoveryRequeuesAndAllocatesNextAttempt(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 17, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "recover-requeue", strings.Repeat("a", 40), "recover-requeue", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-recover-requeue", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claimInput := RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-recover-requeue-1",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-recover-1",
		LeaseDurationMilliseconds: RepositoryExtractionWorkMinLeaseDuration.Milliseconds(),
	}
	firstClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, claimInput, claimedAt)
	if err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}
	recoveryInput := RepositoryExtractionWorkRecoveryInput{
		RequestID:       "recover-requeue-1",
		WorkItemID:      scheduled.Work.WorkItemID,
		ClaimID:         firstClaim.ClaimID,
		RecoveryActorID: "operator-recover",
	}
	_, err = recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, recoveryInput, firstClaim.LeaseExpiresAt.Add(-time.Nanosecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 0)

	recoveredAt := *firstClaim.LeaseExpiresAt
	recovered, err := recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, recoveryInput, recoveredAt)
	if err != nil {
		t.Fatalf("recover expired first attempt: %v", err)
	}
	if recovered.Replayed || recovered.Work != scheduled.Work || recovered.ClaimID != firstClaim.ClaimID || recovered.AttemptNumber != 1 || recovered.WorkerID != firstClaim.WorkerID || !recovered.LeaseExpiresAt.Equal(*firstClaim.LeaseExpiresAt) || !recovered.RecoveredAt.Equal(recoveredAt) || recovered.RecoveryActorID != recoveryInput.RecoveryActorID || recovered.Disposition != RepositoryExtractionWorkRecoveryDispositionRequeued || recovered.SupersededByWorkItemID != "" {
		t.Fatalf("recovered result = %+v", recovered)
	}

	secondClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-recover-requeue-2",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-recover-2",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, recoveredAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("claim second attempt: %v", err)
	}
	if !secondClaim.Claimed || secondClaim.Work == nil || *secondClaim.Work != scheduled.Work || secondClaim.AttemptNumber != 2 || secondClaim.ClaimID == firstClaim.ClaimID {
		t.Fatalf("second claim = %+v, first = %+v", secondClaim, firstClaim)
	}

	firstClaimReplay, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, claimInput, recoveredAt.Add(2*time.Millisecond))
	if err != nil {
		t.Fatalf("replay first claim after requeue: %v", err)
	}
	if !firstClaimReplay.Replayed || !sameRepositoryExtractionClaimResult(firstClaimReplay, firstClaim) {
		t.Fatalf("first claim replay = %+v, first = %+v", firstClaimReplay, firstClaim)
	}
	recoveryReplay, err := recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, recoveryInput, recoveredAt.Add(3*time.Millisecond))
	if err != nil {
		t.Fatalf("replay recovery after second claim: %v", err)
	}
	if !recoveryReplay.Replayed || !sameRepositoryExtractionWorkRecoveryResult(recoveryReplay, recovered) {
		t.Fatalf("recovery replay = %+v, first = %+v", recoveryReplay, recovered)
	}
	reusedRecovery := recoveryInput
	reusedRecovery.RecoveryActorID = "different-operator"
	_, err = RecoverExpiredRepositoryExtractionWork(ctx, pool, reusedRecovery)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-expired-first-attempt",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        firstClaim.ClaimID,
		WorkerID:       firstClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_timeout",
		FailureMessage: "stale worker returned after recovery",
	}, recoveredAt.Add(4*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	finishedAt := recoveredAt.Add(5 * time.Millisecond)
	finishInput := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-second-attempt",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        secondClaim.ClaimID,
		WorkerID:       secondClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "second worker reported a terminal failure",
	}
	finished, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, finishInput, finishedAt)
	if err != nil {
		t.Fatalf("finish second attempt: %v", err)
	}
	if finished.AttemptNumber != 2 || !finished.FinishedAt.Equal(finishedAt) {
		t.Fatalf("second finish = %+v", finished)
	}
	finishReplay, err := FinishRepositoryExtractionWork(ctx, pool, finishInput)
	if err != nil {
		t.Fatalf("replay second finish: %v", err)
	}
	if !finishReplay.Replayed || !sameRepositoryExtractionFinishResult(finishReplay, finished) {
		t.Fatalf("second finish replay = %+v, first = %+v", finishReplay, finished)
	}

	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, firstClaim.ClaimID, recoveredAt)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, scheduled.Work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", finishInput.FailureClass, finishInput.FailureMessage)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 1)
}

func TestIntegrationRepositoryExtractionWorkRecoverySupersedesBehindNewerPendingWork(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	repoID := "recover-supersede"
	extractor := ExtractorRepositoryGoParserCodeFact
	_, stableFirst := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("b", 40), "recover-supersede-first", base, false)
	first := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-recover-supersede-first", stableFirst.RequestID, extractor)
	claimedAt := base.Add(10 * time.Millisecond)
	firstClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-recover-supersede-first",
		RepoID:                    repoID,
		ExtractorName:             extractor,
		WorkerID:                  "worker-superseded",
		LeaseDurationMilliseconds: RepositoryExtractionWorkMinLeaseDuration.Milliseconds(),
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim first work: %v", err)
	}

	_, stableSecond := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("c", 40), "recover-supersede-second", base.Add(20*time.Millisecond), false)
	second := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-recover-supersede-second", stableSecond.RequestID, extractor)
	if second.Work.ObservationNumber <= first.Work.ObservationNumber {
		t.Fatalf("second observation number = %d, first = %d", second.Work.ObservationNumber, first.Work.ObservationNumber)
	}

	recoveredAt := base.Add(30 * time.Millisecond)
	recovered, err := recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkRecoveryInput{
		RequestID:       "recover-supersede-first",
		WorkItemID:      first.Work.WorkItemID,
		ClaimID:         firstClaim.ClaimID,
		RecoveryActorID: "operator-supersede",
	}, recoveredAt)
	if err != nil {
		t.Fatalf("recover first work: %v", err)
	}
	if recovered.Disposition != RepositoryExtractionWorkRecoveryDispositionSuperseded || recovered.SupersededByWorkItemID != second.Work.WorkItemID {
		t.Fatalf("superseding recovery = %+v, newer work = %+v", recovered, second)
	}
	assertRepositoryExtractionWorkSupersededBy(t, ctx, pool, first.Work.WorkItemID, second.Work.WorkItemID)
	assertExpiredRepositoryExtractionWorkClaimAttempt(t, ctx, pool, firstClaim.ClaimID, recoveredAt)

	secondClaim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-recover-supersede-second", repoID, extractor, "worker-newer")
	if secondClaim.Work == nil || *secondClaim.Work != second.Work || secondClaim.AttemptNumber != 1 {
		t.Fatalf("newer claim = %+v, newer work = %+v", secondClaim, second)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_items", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 1)
}

func TestIntegrationRepositoryExtractionWorkConcurrentRecoveryHasOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	_, stable := persistStableWorkObservation(t, ctx, pool, "recover-concurrent", strings.Repeat("d", 40), "recover-concurrent", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-recover-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-recover-concurrent",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-recover-concurrent",
		LeaseDurationMilliseconds: RepositoryExtractionWorkMinLeaseDuration.Milliseconds(),
	}, base.Add(10*time.Millisecond))
	if err != nil {
		t.Fatalf("claim concurrent recovery work: %v", err)
	}

	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(index int) {
			_, err := RecoverExpiredRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkRecoveryInput{
				RequestID:       fmt.Sprintf("recover-concurrent-%d", index),
				WorkItemID:      scheduled.Work.WorkItemID,
				ClaimID:         claim.ClaimID,
				RecoveryActorID: fmt.Sprintf("operator-concurrent-%d", index),
			})
			errs <- err
		}(i)
	}
	succeeded := 0
	conflicted := 0
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case isKind(err, ErrorRepositoryWorkConflict):
			conflicted++
		default:
			t.Fatalf("concurrent recovery error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent recovery succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
}

func sameRepositoryExtractionWorkRecoveryResult(left, right RepositoryExtractionWorkRecoveryResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left == right
}

func assertExpiredRepositoryExtractionWorkClaimAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claimID string, wantExpiredAt time.Time) {
	t.Helper()
	var status string
	var expiredAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, expired_at
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, claimID).Scan(&status, &expiredAt); err != nil {
		t.Fatalf("reading expired claim attempt %s: %v", claimID, err)
	}
	if status != repositoryExtractionWorkExpired || !expiredAt.Equal(wantExpiredAt) {
		t.Fatalf("expired claim attempt %s = %s/%s, want %s/%s", claimID, status, expiredAt, repositoryExtractionWorkExpired, wantExpiredAt)
	}
}
