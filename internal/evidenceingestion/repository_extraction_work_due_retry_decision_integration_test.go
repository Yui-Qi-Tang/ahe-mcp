//go:build integration

package evidenceingestion

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationListDueRepositoryExtractionWorkRetryDecisionsUsesCurrentFailedAuthorityWithoutMutation(t *testing.T) {
	ctx, pool := integrationPool(t)
	db := pgxDB{pool: pool}
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	dueA := createDueRetryDecisionFixture(t, ctx, pool, "a-due-retry-decision", string(ErrorRunnerInvocationFailed), base)
	dueZ := createDueRetryDecisionFixture(t, ctx, pool, "z-due-retry-decision", string(ErrorRunnerInvocationTimeout), base)
	createDueRetryDecisionFixture(t, ctx, pool, "future-retry-decision", string(ErrorRunnerInvocationCancelled), base.Add(time.Minute))
	createDueRetryDecisionFixture(t, ctx, pool, "stop-retry-decision", string(ErrorInvalidExtractorOutput), base)
	consumed := createDueRetryDecisionFixture(t, ctx, pool, "consumed-retry-decision", string(ErrorRunnerInvocationFailed), base.Add(-time.Minute))
	explicit := createDueRetryDecisionFixture(t, ctx, pool, "explicit-retry-decision", string(ErrorRunnerInvocationTimeout), base.Add(-time.Minute))

	_, err := consumeDueRepositoryExtractionWorkRetryDecision(ctx, db, RepositoryExtractionWorkRetryDecisionConsumptionInput{
		RequestID:              "consume-before-discovery",
		FailurePolicyRequestID: consumed.policy.RequestID,
		WorkItemID:             consumed.work.WorkItemID,
		ClaimID:                consumed.claim.ClaimID,
		ConsumerActorID:        "controller-consumed-before-discovery",
	}, *consumed.policy.RetryNotBefore)
	if err != nil {
		t.Fatalf("consume retry decision fixture: %v", err)
	}
	_, err = retryFailedRepositoryExtractionWork(ctx, db, RepositoryExtractionWorkRetryInput{
		RequestID:    "explicit-retry-before-discovery",
		WorkItemID:   explicit.work.WorkItemID,
		ClaimID:      explicit.claim.ClaimID,
		RetryActorID: "operator-explicit-before-discovery",
	}, *explicit.policy.RetryNotBefore)
	if err != nil {
		t.Fatalf("explicitly retry decision fixture: %v", err)
	}

	dueAt := *dueA.policy.RetryNotBefore
	input := RepositoryExtractionWorkDueRetryDecisionListInput{Limit: 10}
	decisions, err := listDueRepositoryExtractionWorkRetryDecisions(ctx, db, input, dueAt.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("list before retry due boundary: %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions before due boundary = %+v, want empty", decisions)
	}

	decisions, err = listDueRepositoryExtractionWorkRetryDecisions(ctx, db, RepositoryExtractionWorkDueRetryDecisionListInput{Limit: 1}, dueAt)
	if err != nil {
		t.Fatalf("list bounded due retry decisions: %v", err)
	}
	assertDueRepositoryExtractionWorkRetryDecision(t, decisions, dueA)

	decisions, err = listDueRepositoryExtractionWorkRetryDecisions(ctx, db, input, dueAt)
	if err != nil {
		t.Fatalf("list at retry due boundary: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("due retry decisions = %+v, want two", decisions)
	}
	if decisions[0].FailurePolicyRequestID != dueA.policy.RequestID || decisions[1].FailurePolicyRequestID != dueZ.policy.RequestID {
		t.Fatalf("due retry decision order = %+v, want %q then %q", decisions, dueA.policy.RequestID, dueZ.policy.RequestID)
	}
	assertDueRetryDecisionDiscoveryTableCounts(t, ctx, pool, 6, 6, 6, 1, 1)

	_, err = consumeDueRepositoryExtractionWorkRetryDecision(ctx, db, RepositoryExtractionWorkRetryDecisionConsumptionInput{
		RequestID:              "consume-discovered-due-decision",
		FailurePolicyRequestID: dueA.policy.RequestID,
		WorkItemID:             dueA.work.WorkItemID,
		ClaimID:                dueA.claim.ClaimID,
		ConsumerActorID:        "controller-discovered-due-decision",
	}, dueAt)
	if err != nil {
		t.Fatalf("consume discovered due retry decision: %v", err)
	}
	decisions, err = listDueRepositoryExtractionWorkRetryDecisions(ctx, db, input, dueAt)
	if err != nil {
		t.Fatalf("list after retry decision consumption: %v", err)
	}
	assertDueRepositoryExtractionWorkRetryDecision(t, decisions, dueZ)
	assertDueRetryDecisionDiscoveryTableCounts(t, ctx, pool, 6, 6, 6, 2, 1)
}

type dueRetryDecisionFixture struct {
	work   RepositoryExtractionWork
	claim  RepositoryExtractionWorkClaimResult
	finish RepositoryExtractionWorkFinishResult
	policy RepositoryExtractionWorkFailurePolicyResult
}

func createDueRetryDecisionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix, failureClass string, base time.Time) dueRetryDecisionFixture {
	t.Helper()
	work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, suffix, failureClass, base)
	policy := persistRepositoryExtractionWorkRetryPolicyForConsumption(t, ctx, pool, suffix, work, claim, finish)
	return dueRetryDecisionFixture{work: work, claim: claim, finish: finish, policy: policy}
}

func assertDueRepositoryExtractionWorkRetryDecision(t *testing.T, decisions []RepositoryExtractionWorkDueRetryDecision, want dueRetryDecisionFixture) {
	t.Helper()
	if len(decisions) != 1 {
		t.Fatalf("due retry decisions = %+v, want one", decisions)
	}
	got := decisions[0]
	if want.policy.RetryNotBefore == nil ||
		got.FailurePolicyRequestID != want.policy.RequestID ||
		got.Work != want.work ||
		got.ClaimID != want.claim.ClaimID ||
		got.AttemptNumber != want.claim.AttemptNumber ||
		got.WorkerID != want.claim.WorkerID ||
		!got.FinishedAt.Equal(want.finish.FinishedAt) ||
		got.FailureClass != want.finish.FailureClass ||
		got.FailureMessage != want.finish.FailureMessage ||
		got.PolicyVersion != want.policy.PolicyVersion ||
		got.Decision != want.policy.Decision ||
		got.DecisionReason != want.policy.DecisionReason ||
		got.MaxAttempts != want.policy.MaxAttempts ||
		got.BackoffMilliseconds != want.policy.BackoffMilliseconds ||
		!got.RetryNotBefore.Equal(*want.policy.RetryNotBefore) ||
		!got.DecidedAt.Equal(want.policy.DecidedAt) ||
		got.DecisionActorID != want.policy.DecisionActorID {
		t.Fatalf("due retry decision = %+v, want fixture %+v", got, want)
	}
}

func assertDueRetryDecisionDiscoveryTableCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItems, attempts, policies, consumptions, retries int) {
	t.Helper()
	assertTableCount(t, ctx, pool, "repository_extraction_work_items", workItems)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", attempts)
	assertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", policies)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions", consumptions)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", retries)
}
