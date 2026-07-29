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

func TestIntegrationRepositoryExtractionWorkRetryDecisionConsumptionRequeuesWhenDue(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 23, 30, 0, 0, time.UTC)
	work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, "retry-decision-requeue", string(ErrorRunnerInvocationFailed), base)
	policy := persistRepositoryExtractionWorkRetryPolicyForConsumption(t, ctx, pool, "retry-decision-requeue", work, claim, finish)
	input := RepositoryExtractionWorkRetryDecisionConsumptionInput{
		RequestID:              "consume-retry-decision-requeue",
		FailurePolicyRequestID: policy.RequestID,
		WorkItemID:             work.WorkItemID,
		ClaimID:                claim.ClaimID,
		ConsumerActorID:        "retry-controller-1",
	}

	_, err := consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, input, policy.RetryNotBefore.Add(-time.Nanosecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 0)
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", finish.FailureClass, finish.FailureMessage)

	consumedAt := *policy.RetryNotBefore
	consumed, err := consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, input, consumedAt)
	if err != nil {
		t.Fatalf("consume due retry decision: %v", err)
	}
	if consumed.Replayed ||
		consumed.RequestID != input.RequestID ||
		consumed.FailurePolicyRequestID != policy.RequestID ||
		consumed.Work != work ||
		consumed.ClaimID != claim.ClaimID ||
		consumed.AttemptNumber != claim.AttemptNumber ||
		consumed.WorkerID != claim.WorkerID ||
		!consumed.FinishedAt.Equal(finish.FinishedAt) ||
		consumed.FailureClass != finish.FailureClass ||
		consumed.FailureMessage != finish.FailureMessage ||
		consumed.PolicyVersion != policy.PolicyVersion ||
		consumed.Decision != RepositoryExtractionWorkFailureDecisionRetry ||
		consumed.DecisionReason != policy.DecisionReason ||
		consumed.BackoffMilliseconds != policy.BackoffMilliseconds ||
		!consumed.RetryNotBefore.Equal(*policy.RetryNotBefore) ||
		!consumed.DecidedAt.Equal(policy.DecidedAt) ||
		!consumed.ConsumedAt.Equal(consumedAt) ||
		consumed.ConsumerActorID != input.ConsumerActorID ||
		consumed.Disposition != RepositoryExtractionWorkRetryDispositionRequeued ||
		consumed.SupersededByWorkItemID != "" {
		t.Fatalf("retry decision consumption = %+v, policy = %+v", consumed, policy)
	}
	assertRepositoryExtractionWorkPendingAfterRetry(t, ctx, pool, work.WorkItemID, consumedAt)
	assertFailedRepositoryExtractionWorkClaimAttempt(t, ctx, pool, claim.ClaimID, finish)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 0)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 1)

	secondClaim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-retry-decision-requeue-2",
		RepoID:                    work.RepoID,
		ExtractorName:             work.ExtractorName,
		WorkerID:                  "worker-retry-decision-requeue-2",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, consumedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("claim requeued retry decision work: %v", err)
	}
	if !secondClaim.Claimed || secondClaim.Work == nil || *secondClaim.Work != work || secondClaim.AttemptNumber != 2 || secondClaim.ClaimID == claim.ClaimID {
		t.Fatalf("second claim = %+v, first claim = %+v", secondClaim, claim)
	}

	replay, err := consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, input, consumedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay consumed retry decision: %v", err)
	}
	if !replay.Replayed || !sameRepositoryExtractionWorkRetryDecisionConsumptionResult(replay, consumed) {
		t.Fatalf("retry decision consumption replay = %+v, first = %+v", replay, consumed)
	}
	reused := input
	reused.ConsumerActorID = "retry-controller-2"
	_, err = ConsumeDueRepositoryExtractionWorkRetryDecision(ctx, pool, reused)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	secondRequest := input
	secondRequest.RequestID = "consume-retry-decision-requeue-second"
	_, err = consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, secondRequest, consumedAt.Add(time.Hour))
	assertKind(t, err, ErrorRepositoryWorkConflict)
}

func TestIntegrationRepositoryExtractionWorkRetryDecisionConsumptionRejectsStopAndSupersedesOldWork(t *testing.T) {
	t.Run("stop decision", func(t *testing.T) {
		ctx, pool := integrationPool(t)
		base := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
		work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, "retry-decision-stop", string(ErrorInvalidExtractorOutput), base)
		policy := persistRepositoryExtractionWorkRetryPolicyForConsumption(t, ctx, pool, "retry-decision-stop", work, claim, finish)
		_, err := consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkRetryDecisionConsumptionInput{
			RequestID:              "consume-retry-decision-stop",
			FailurePolicyRequestID: policy.RequestID,
			WorkItemID:             work.WorkItemID,
			ClaimID:                claim.ClaimID,
			ConsumerActorID:        "retry-controller-stop",
		}, policy.DecidedAt.Add(time.Hour))
		assertKind(t, err, ErrorRepositoryWorkConflict)
		assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 0)
		assertRepositoryExtractionWorkTerminalState(t, ctx, pool, work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", finish.FailureClass, finish.FailureMessage)
	})

	t.Run("newer pending work", func(t *testing.T) {
		ctx, pool := integrationPool(t)
		base := time.Date(2026, 7, 22, 0, 30, 0, 0, time.UTC)
		repoID := "retry-decision-supersede"
		work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, repoID, string(ErrorRunnerInvocationTimeout), base)
		policy := persistRepositoryExtractionWorkRetryPolicyForConsumption(t, ctx, pool, "retry-decision-supersede", work, claim, finish)

		_, stable := persistStableWorkObservation(t, ctx, pool, repoID, strings.Repeat("b", 40), "retry-decision-supersede-newer", base.Add(20*time.Millisecond), false)
		newer := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-retry-decision-supersede-newer", stable.RequestID, work.ExtractorName)
		consumed, err := consumeDueRepositoryExtractionWorkRetryDecision(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkRetryDecisionConsumptionInput{
			RequestID:              "consume-retry-decision-supersede",
			FailurePolicyRequestID: policy.RequestID,
			WorkItemID:             work.WorkItemID,
			ClaimID:                claim.ClaimID,
			ConsumerActorID:        "retry-controller-supersede",
		}, *policy.RetryNotBefore)
		if err != nil {
			t.Fatalf("consume retry decision behind newer work: %v", err)
		}
		if consumed.Disposition != RepositoryExtractionWorkRetryDispositionSuperseded || consumed.SupersededByWorkItemID != newer.Work.WorkItemID {
			t.Fatalf("superseding consumption = %+v, newer work = %+v", consumed, newer)
		}
		assertRepositoryExtractionWorkSupersededBy(t, ctx, pool, work.WorkItemID, newer.Work.WorkItemID)
		assertFailedRepositoryExtractionWorkClaimAttempt(t, ctx, pool, claim.ClaimID, finish)
		assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 1)
	})
}

func TestIntegrationRepositoryExtractionWorkRetryDecisionConsumptionConcurrentRequestsHaveOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-10 * time.Second)
	work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, "retry-decision-concurrent", string(ErrorRunnerInvocationCancelled), base)
	policy := persistRepositoryExtractionWorkRetryPolicyForConsumption(t, ctx, pool, "retry-decision-concurrent", work, claim, finish)
	if policy.RetryNotBefore == nil || time.Now().UTC().Before(*policy.RetryNotBefore) {
		t.Fatalf("concurrent retry policy is not due: %+v", policy)
	}

	errs := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		go func(index int) {
			_, err := ConsumeDueRepositoryExtractionWorkRetryDecision(ctx, pool, RepositoryExtractionWorkRetryDecisionConsumptionInput{
				RequestID:              fmt.Sprintf("consume-retry-decision-concurrent-%d", index),
				FailurePolicyRequestID: policy.RequestID,
				WorkItemID:             work.WorkItemID,
				ClaimID:                claim.ClaimID,
				ConsumerActorID:        fmt.Sprintf("retry-controller-concurrent-%d", index),
			})
			errs <- err
		}(index)
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
			t.Fatalf("concurrent retry decision consumption error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent retry decision consumption succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 1)
}

func persistRepositoryExtractionWorkRetryPolicyForConsumption(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string, work RepositoryExtractionWork, claim RepositoryExtractionWorkClaimResult, finish RepositoryExtractionWorkFinishResult) RepositoryExtractionWorkFailurePolicyResult {
	t.Helper()
	policy, err := classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFailurePolicyInput{
		RequestID:       "failure-policy-" + suffix,
		WorkItemID:      work.WorkItemID,
		ClaimID:         claim.ClaimID,
		DecisionActorID: "failure-policy-controller-" + suffix,
	}, finish.FinishedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("classify work for retry decision consumption: %v", err)
	}
	return policy
}

func sameRepositoryExtractionWorkRetryDecisionConsumptionResult(left, right RepositoryExtractionWorkRetryDecisionConsumptionResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left == right
}
