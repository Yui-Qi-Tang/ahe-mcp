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

func TestIntegrationRepositoryExtractionWorkLeaseRenewalExtendsFinishAndRecoveryDeadline(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 21, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "lease-renewal", strings.Repeat("3", 40), "lease-renewal", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-lease-renewal", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-lease-renewal",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-lease-renewal",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim renewal work: %v", err)
	}
	originalLeaseExpiresAt := claimedAt.Add(10 * time.Millisecond)

	baseInput := RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "renew-lease-1",
		WorkItemID:                scheduled.Work.WorkItemID,
		ClaimID:                   claim.ClaimID,
		WorkerID:                  claim.WorkerID,
		LeaseDurationMilliseconds: 20,
	}
	wrongWorker := baseInput
	wrongWorker.RequestID = "renew-lease-wrong-worker"
	wrongWorker.WorkerID = "worker-other"
	_, err = renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, wrongWorker, claimedAt.Add(5*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	shortRenewal := baseInput
	shortRenewal.RequestID = "renew-lease-short"
	shortRenewal.LeaseDurationMilliseconds = 1
	_, err = renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, shortRenewal, claimedAt.Add(5*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	_, err = renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, baseInput, originalLeaseExpiresAt)
	assertKind(t, err, ErrorRepositoryWorkLeaseExpired)
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 0)

	firstRenewedAt := claimedAt.Add(5 * time.Millisecond)
	first, err := renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, baseInput, firstRenewedAt)
	if err != nil {
		t.Fatalf("renew first lease: %v", err)
	}
	firstLeaseExpiresAt := firstRenewedAt.Add(20 * time.Millisecond)
	if first.Replayed || first.Work != scheduled.Work || first.ClaimID != claim.ClaimID || first.AttemptNumber != 1 || first.WorkerID != claim.WorkerID || first.LeaseDurationMilliseconds != 20 || !first.PriorLeaseExpiresAt.Equal(originalLeaseExpiresAt) || !first.RenewedAt.Equal(firstRenewedAt) || !first.LeaseExpiresAt.Equal(firstLeaseExpiresAt) {
		t.Fatalf("first renewal = %+v, claim = %+v", first, claim)
	}
	assertRepositoryExtractionWorkClaimLease(t, ctx, pool, claim.ClaimID, firstLeaseExpiresAt)

	reusedRequest := baseInput
	reusedRequest.LeaseDurationMilliseconds = 21
	_, err = RenewRepositoryExtractionWorkLease(ctx, pool, reusedRequest)
	assertKind(t, err, ErrorIdempotencyKeyReused)

	secondInput := baseInput
	secondInput.RequestID = "renew-lease-2"
	secondRenewedAt := claimedAt.Add(15 * time.Millisecond)
	second, err := renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, secondInput, secondRenewedAt)
	if err != nil {
		t.Fatalf("renew second lease: %v", err)
	}
	secondLeaseExpiresAt := secondRenewedAt.Add(20 * time.Millisecond)
	if second.PriorLeaseExpiresAt != firstLeaseExpiresAt || second.LeaseExpiresAt != secondLeaseExpiresAt {
		t.Fatalf("second renewal = %+v, first = %+v", second, first)
	}
	assertRepositoryExtractionWorkClaimLease(t, ctx, pool, claim.ClaimID, secondLeaseExpiresAt)

	firstReplay, err := renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, baseInput, secondRenewedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("replay first renewal after second: %v", err)
	}
	if !firstReplay.Replayed || !sameRepositoryExtractionWorkLeaseRenewalResult(firstReplay, first) {
		t.Fatalf("first renewal replay = %+v, first = %+v", firstReplay, first)
	}

	_, err = recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkRecoveryInput{
		RequestID:       "recover-before-renewed-expiry",
		WorkItemID:      scheduled.Work.WorkItemID,
		ClaimID:         claim.ClaimID,
		RecoveryActorID: "controller-renewal",
	}, firstLeaseExpiresAt)
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", 0)

	finishedAt := firstLeaseExpiresAt.Add(time.Millisecond)
	finishInput := RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-after-lease-renewal",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        claim.ClaimID,
		WorkerID:       claim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "worker finished after its original lease but before the renewed lease",
	}
	finished, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, finishInput, finishedAt)
	if err != nil {
		t.Fatalf("finish after lease renewal: %v", err)
	}
	if !finished.LeaseExpiresAt.Equal(secondLeaseExpiresAt) || !finished.FinishedAt.Equal(finishedAt) {
		t.Fatalf("finish after renewal = %+v, second renewal = %+v", finished, second)
	}

	secondReplay, err := renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, secondInput, finishedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("replay second renewal after finish: %v", err)
	}
	if !secondReplay.Replayed || !sameRepositoryExtractionWorkLeaseRenewalResult(secondReplay, second) {
		t.Fatalf("second renewal replay = %+v, second = %+v", secondReplay, second)
	}
	thirdInput := secondInput
	thirdInput.RequestID = "renew-lease-after-finish"
	_, err = renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, thirdInput, finishedAt.Add(time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)

	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 2)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 1)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, scheduled.Work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", finishInput.FailureClass, finishInput.FailureMessage)
}

func TestIntegrationRepositoryExtractionWorkConcurrentLeaseRenewalHasOneExtension(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Minute)
	_, stable := persistStableWorkObservation(t, ctx, pool, "lease-renewal-concurrent", strings.Repeat("4", 40), "lease-renewal-concurrent", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-lease-renewal-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-lease-renewal-concurrent",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-lease-renewal-concurrent",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim concurrent renewal work: %v", err)
	}
	renewedAt := claimedAt.Add(5 * time.Millisecond)
	wantLeaseExpiresAt := renewedAt.Add(20 * time.Millisecond)

	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(index int) {
			_, err := renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkLeaseRenewalInput{
				RequestID:                 fmt.Sprintf("renew-lease-concurrent-%d", index),
				WorkItemID:                scheduled.Work.WorkItemID,
				ClaimID:                   claim.ClaimID,
				WorkerID:                  claim.WorkerID,
				LeaseDurationMilliseconds: 20,
			}, renewedAt)
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
			t.Fatalf("concurrent lease renewal error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent lease renewal succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertRepositoryExtractionWorkClaimLease(t, ctx, pool, claim.ClaimID, wantLeaseExpiresAt)
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 1)
}

func sameRepositoryExtractionWorkLeaseRenewalResult(left, right RepositoryExtractionWorkLeaseRenewalResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left == right
}

func assertRepositoryExtractionWorkClaimLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claimID string, want time.Time) {
	t.Helper()
	var got time.Time
	if err := pool.QueryRow(ctx, `
		SELECT lease_expires_at
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, claimID).Scan(&got); err != nil {
		t.Fatalf("reading claim %s lease: %v", claimID, err)
	}
	if !got.Equal(want) {
		t.Fatalf("claim %s lease = %s, want %s", claimID, got, want)
	}
}
