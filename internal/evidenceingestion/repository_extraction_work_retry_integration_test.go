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

func TestIntegrationRepositoryExtractionWorkRetryRequeuesAndAllocatesNextAttempt(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 19, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "retry-requeue", strings.Repeat("e", 40), "retry-requeue", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-retry-requeue", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	firstClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-retry-requeue-1",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-retry-1",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}

	retryInput := RepositoryExtractionWorkRetryInput{
		RequestID:    "retry-requeue-1",
		WorkItemID:   scheduled.Work.WorkItemID,
		ClaimID:      firstClaim.ClaimID,
		RetryActorID: "operator-retry",
	}
	_, err = retryFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, retryInput, claimedAt.Add(time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 0)

	finishedAt := claimedAt.Add(2 * time.Millisecond)
	finishInput := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-retry-requeue-1",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        firstClaim.ClaimID,
		WorkerID:       firstClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "first worker reported a terminal failure",
	}
	firstFinish, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, finishInput, finishedAt)
	if err != nil {
		t.Fatalf("finish first attempt: %v", err)
	}
	_, err = retryFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, retryInput, finishedAt.Add(-time.Nanosecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	retriedAt := finishedAt.Add(time.Millisecond)
	retried, err := retryFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, retryInput, retriedAt)
	if err != nil {
		t.Fatalf("retry failed first attempt: %v", err)
	}
	if retried.Replayed || retried.Work != scheduled.Work || retried.ClaimID != firstClaim.ClaimID || retried.AttemptNumber != 1 || retried.WorkerID != firstClaim.WorkerID || !retried.FinishedAt.Equal(finishedAt) || retried.FailureClass != finishInput.FailureClass || retried.FailureMessage != finishInput.FailureMessage || !retried.RetriedAt.Equal(retriedAt) || retried.RetryActorID != retryInput.RetryActorID || retried.Disposition != RepositoryExtractionWorkRetryDispositionRequeued || retried.SupersededByWorkItemID != "" {
		t.Fatalf("retried result = %+v", retried)
	}
	assertRepositoryExtractionWorkPendingAfterRetry(t, ctx, pool, scheduled.Work.WorkItemID, retriedAt)
	assertFailedRepositoryExtractionWorkClaimAttempt(t, ctx, pool, firstClaim.ClaimID, firstFinish)

	secondClaimAt := retriedAt.Add(time.Millisecond)
	secondClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-retry-requeue-2",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-retry-2",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, secondClaimAt)
	if err != nil {
		t.Fatalf("claim second attempt: %v", err)
	}
	if !secondClaim.Claimed || secondClaim.Work == nil || *secondClaim.Work != scheduled.Work || secondClaim.AttemptNumber != 2 || secondClaim.ClaimID == firstClaim.ClaimID {
		t.Fatalf("second claim = %+v, first = %+v", secondClaim, firstClaim)
	}

	finishReplay, err := FinishRepositoryExtractionWork(ctx, pool, finishInput)
	if err != nil {
		t.Fatalf("replay first finish after second claim: %v", err)
	}
	if !finishReplay.Replayed || !sameRepositoryExtractionFinishResult(finishReplay, firstFinish) {
		t.Fatalf("first finish replay = %+v, first = %+v", finishReplay, firstFinish)
	}
	retryReplay, err := retryFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, retryInput, retriedAt.Add(2*time.Millisecond))
	if err != nil {
		t.Fatalf("replay retry after second claim: %v", err)
	}
	if !retryReplay.Replayed || !sameRepositoryExtractionWorkRetryResult(retryReplay, retried) {
		t.Fatalf("retry replay = %+v, first = %+v", retryReplay, retried)
	}
	reusedRetry := retryInput
	reusedRetry.RetryActorID = "different-operator"
	_, err = RetryFailedRepositoryExtractionWork(ctx, pool, reusedRetry)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	staleFinish := finishInput
	staleFinish.RequestID = "finish-stale-retried-attempt"
	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, staleFinish, secondClaimAt.Add(time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	secondFinishInput := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-retry-requeue-2",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        secondClaim.ClaimID,
		WorkerID:       secondClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_output_invalid",
		FailureMessage: "second worker also reported a terminal failure",
	}
	secondFinishedAt := secondClaimAt.Add(2 * time.Millisecond)
	secondFinish, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, secondFinishInput, secondFinishedAt)
	if err != nil {
		t.Fatalf("finish second attempt: %v", err)
	}
	if secondFinish.AttemptNumber != 2 || !secondFinish.FinishedAt.Equal(secondFinishedAt) {
		t.Fatalf("second finish = %+v", secondFinish)
	}

	assertFailedRepositoryExtractionWorkClaimAttempt(t, ctx, pool, firstClaim.ClaimID, firstFinish)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, scheduled.Work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", secondFinishInput.FailureClass, secondFinishInput.FailureMessage)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 2)
}

func TestIntegrationRepositoryExtractionWorkRetrySupersedesBehindNewerPendingWork(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	repoID := "retry-supersede"
	extractor := ExtractorRepositoryGoParserCodeFact
	_, stableFirst := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("f", 40), "retry-supersede-first", base, false)
	first := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-retry-supersede-first", stableFirst.RequestID, extractor)
	firstClaimAt := base.Add(10 * time.Millisecond)
	firstClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-retry-supersede-first",
		RepoID:                    repoID,
		ExtractorName:             extractor,
		WorkerID:                  "worker-retry-superseded",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, firstClaimAt)
	if err != nil {
		t.Fatalf("claim first work: %v", err)
	}
	finishInput := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-retry-supersede-first",
		WorkItemID:     first.Work.WorkItemID,
		ClaimID:        firstClaim.ClaimID,
		WorkerID:       firstClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "old observation failed",
	}
	finishedAt := firstClaimAt.Add(time.Millisecond)
	firstFinish, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, finishInput, finishedAt)
	if err != nil {
		t.Fatalf("finish first work: %v", err)
	}

	_, stableSecond := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("1", 40), "retry-supersede-second", base.Add(20*time.Millisecond), false)
	second := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-retry-supersede-second", stableSecond.RequestID, extractor)
	if second.Work.ObservationNumber <= first.Work.ObservationNumber {
		t.Fatalf("second observation number = %d, first = %d", second.Work.ObservationNumber, first.Work.ObservationNumber)
	}

	retriedAt := base.Add(30 * time.Millisecond)
	retried, err := retryFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkRetryInput{
		RequestID:    "retry-supersede-first",
		WorkItemID:   first.Work.WorkItemID,
		ClaimID:      firstClaim.ClaimID,
		RetryActorID: "operator-supersede",
	}, retriedAt)
	if err != nil {
		t.Fatalf("retry first work: %v", err)
	}
	if retried.Disposition != RepositoryExtractionWorkRetryDispositionSuperseded || retried.SupersededByWorkItemID != second.Work.WorkItemID {
		t.Fatalf("superseding retry = %+v, newer work = %+v", retried, second)
	}
	assertRepositoryExtractionWorkSupersededBy(t, ctx, pool, first.Work.WorkItemID, second.Work.WorkItemID)
	assertFailedRepositoryExtractionWorkClaimAttempt(t, ctx, pool, firstClaim.ClaimID, firstFinish)

	secondClaim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-retry-supersede-second", repoID, extractor, "worker-newer")
	if secondClaim.Work == nil || *secondClaim.Work != second.Work || secondClaim.AttemptNumber != 1 {
		t.Fatalf("newer claim = %+v, newer work = %+v", secondClaim, second)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_items", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 1)
}

func TestIntegrationRepositoryExtractionWorkConcurrentRetryHasOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	_, stable := persistStableWorkObservation(t, ctx, pool, "retry-concurrent", strings.Repeat("2", 40), "retry-concurrent", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-retry-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-retry-concurrent",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-retry-concurrent",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimAt)
	if err != nil {
		t.Fatalf("claim concurrent retry work: %v", err)
	}
	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-retry-concurrent",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        claim.ClaimID,
		WorkerID:       claim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "concurrent retry fixture",
	}, claimAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("finish concurrent retry work: %v", err)
	}

	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(index int) {
			_, err := RetryFailedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkRetryInput{
				RequestID:    fmt.Sprintf("retry-concurrent-%d", index),
				WorkItemID:   scheduled.Work.WorkItemID,
				ClaimID:      claim.ClaimID,
				RetryActorID: fmt.Sprintf("operator-concurrent-%d", index),
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
			t.Fatalf("concurrent retry error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent retry succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
}

func sameRepositoryExtractionWorkRetryResult(left, right RepositoryExtractionWorkRetryResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left == right
}

func assertRepositoryExtractionWorkPendingAfterRetry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItemID string, retriedAt time.Time) {
	t.Helper()
	var status string
	var supersededBy, claimID, claimedBy, claimedAt, sourceGenerationID, finishedAt, failureClass, failureMessage any
	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status,
			superseded_by_work_item_id,
			claim_id,
			claimed_by,
			claimed_at,
			source_generation_id,
			finished_at,
			failure_class,
			failure_message,
			updated_at
		FROM repository_extraction_work_items
		WHERE work_item_id = $1
	`, workItemID).Scan(&status, &supersededBy, &claimID, &claimedBy, &claimedAt, &sourceGenerationID, &finishedAt, &failureClass, &failureMessage, &updatedAt); err != nil {
		t.Fatalf("reading retried work %s: %v", workItemID, err)
	}
	if status != repositoryExtractionWorkPending || supersededBy != nil || claimID != nil || claimedBy != nil || claimedAt != nil || sourceGenerationID != nil || finishedAt != nil || failureClass != nil || failureMessage != nil || !updatedAt.Equal(retriedAt) {
		t.Fatalf("retried work %s = %s/%v/%v/%v/%v/%v/%v/%v/%v/%s", workItemID, status, supersededBy, claimID, claimedBy, claimedAt, sourceGenerationID, finishedAt, failureClass, failureMessage, updatedAt)
	}
}

func assertFailedRepositoryExtractionWorkClaimAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claimID string, want RepositoryExtractionWorkFinishResult) {
	t.Helper()
	var workItemID, workerID, status, failureClass, failureMessage string
	var attemptNumber int
	var finishedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT work_item_id, attempt_number, worker_id, status, finished_at, failure_class, failure_message
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, claimID).Scan(&workItemID, &attemptNumber, &workerID, &status, &finishedAt, &failureClass, &failureMessage); err != nil {
		t.Fatalf("reading failed claim attempt %s: %v", claimID, err)
	}
	if workItemID != want.Work.WorkItemID || attemptNumber != want.AttemptNumber || workerID != want.WorkerID || status != RepositoryExtractionWorkOutcomeFailed || !finishedAt.Equal(want.FinishedAt) || failureClass != want.FailureClass || failureMessage != want.FailureMessage {
		t.Fatalf("failed claim attempt %s = %s/%d/%s/%s/%s/%s/%s, want %+v", claimID, workItemID, attemptNumber, workerID, status, finishedAt, failureClass, failureMessage, want)
	}
}
