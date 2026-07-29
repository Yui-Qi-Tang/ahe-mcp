//go:build integration

package evidenceingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationListExpiredRepositoryExtractionWorkClaimsUsesCurrentLeaseWithoutMutation(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 22, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "expired-claim-list", strings.Repeat("5", 40), "expired-claim-list", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-expired-claim-list", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-expired-claim-list-1",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-expired-claim-list-1",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim first discovery attempt: %v", err)
	}

	input := RepositoryExtractionWorkExpiredClaimListInput{
		RepoID:        scheduled.Work.RepoID,
		ExtractorName: scheduled.Work.ExtractorName,
		Limit:         10,
	}
	claims, err := listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, input, claim.LeaseExpiresAt.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("list before original expiry: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("claims before original expiry = %+v, want empty", claims)
	}

	renewedAt := claimedAt.Add(5 * time.Millisecond)
	renewed, err := renewRepositoryExtractionWorkLease(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "renew-expired-claim-list",
		WorkItemID:                scheduled.Work.WorkItemID,
		ClaimID:                   claim.ClaimID,
		WorkerID:                  claim.WorkerID,
		LeaseDurationMilliseconds: 30,
	}, renewedAt)
	if err != nil {
		t.Fatalf("renew discovery attempt: %v", err)
	}

	claims, err = listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, input, *claim.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("list at superseded original expiry: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("claims at superseded original expiry = %+v, want empty", claims)
	}

	claims, err = listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, input, renewed.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("list at renewed expiry: %v", err)
	}
	assertRepositoryExtractionWorkExpiredClaim(t, claims, scheduled.Work, claim, renewed.LeaseExpiresAt)

	for _, filtered := range []RepositoryExtractionWorkExpiredClaimListInput{
		{RepoID: "other-repo", ExtractorName: input.ExtractorName, Limit: input.Limit},
		{RepoID: input.RepoID, ExtractorName: ExtractorRepositoryGoplsCodeFact, Limit: input.Limit},
	} {
		claims, err := listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, filtered, renewed.LeaseExpiresAt)
		if err != nil {
			t.Fatalf("list filtered claims %+v: %v", filtered, err)
		}
		if len(claims) != 0 {
			t.Fatalf("filtered claims %+v = %+v, want empty", filtered, claims)
		}
	}
	assertExpiredClaimDiscoveryTableCounts(t, ctx, pool, 1, 1, 1, 0, 0)

	recoveredAt := renewed.LeaseExpiresAt
	_, err = recoverExpiredRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkRecoveryInput{
		RequestID:       "recover-expired-claim-list",
		WorkItemID:      scheduled.Work.WorkItemID,
		ClaimID:         claim.ClaimID,
		RecoveryActorID: "controller-expired-claim-list",
	}, recoveredAt)
	if err != nil {
		t.Fatalf("recover discovered claim: %v", err)
	}
	claims, err = listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, input, recoveredAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("list after recovery: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("claims after recovery = %+v, want empty", claims)
	}

	secondClaimedAt := recoveredAt.Add(time.Millisecond)
	secondClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-expired-claim-list-2",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-expired-claim-list-2",
		LeaseDurationMilliseconds: 10,
	}, secondClaimedAt)
	if err != nil {
		t.Fatalf("claim second discovery attempt: %v", err)
	}
	_, err = finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-expired-claim-list-2",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        secondClaim.ClaimID,
		WorkerID:       secondClaim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "runner_invocation_failed",
		FailureMessage: "fixture terminal claim must not remain discoverable",
	}, secondClaimedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("finish second discovery attempt: %v", err)
	}
	claims, err = listExpiredRepositoryExtractionWorkClaims(ctx, pgxDB{pool: pool}, input, secondClaim.LeaseExpiresAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("list after terminal finish: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("claims after terminal finish = %+v, want empty", claims)
	}
	assertExpiredClaimDiscoveryTableCounts(t, ctx, pool, 2, 2, 1, 1, 1)
}

func assertRepositoryExtractionWorkExpiredClaim(t *testing.T, claims []RepositoryExtractionWorkExpiredClaim, work RepositoryExtractionWork, claim RepositoryExtractionWorkClaimResult, leaseExpiresAt time.Time) {
	t.Helper()
	if len(claims) != 1 {
		t.Fatalf("expired claims = %+v, want one", claims)
	}
	got := claims[0]
	if got.Work != work || got.ClaimID != claim.ClaimID || got.AttemptNumber != claim.AttemptNumber || got.WorkerID != claim.WorkerID || claim.ClaimedAt == nil || !got.ClaimedAt.Equal(*claim.ClaimedAt) || !got.LeaseExpiresAt.Equal(leaseExpiresAt) {
		t.Fatalf("expired claim = %+v, work/claim/lease = %+v/%+v/%s", got, work, claim, leaseExpiresAt)
	}
}

func assertExpiredClaimDiscoveryTableCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claimRequests, attempts, renewals, recoveries, finishes int) {
	t.Helper()
	assertTableCount(t, ctx, pool, "repository_extraction_work_items", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", claimRequests)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", attempts)
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", renewals)
	assertTableCount(t, ctx, pool, "repository_extraction_work_recovery_requests", recoveries)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", finishes)
}
