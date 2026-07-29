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

func TestIntegrationRepositoryExtractionWorkFailurePolicyRecordsRetryWithoutMutatingWork(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 22, 0, 0, 0, time.UTC)
	work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, "failure-policy-retry", string(ErrorRunnerInvocationFailed), base)
	input := RepositoryExtractionWorkFailurePolicyInput{
		RequestID:       "failure-policy-retry",
		WorkItemID:      work.WorkItemID,
		ClaimID:         claim.ClaimID,
		DecisionActorID: "worker-controller-1",
	}

	_, err := classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, withFailurePolicyRequestID(input, "failure-policy-too-early"), finish.FinishedAt.Add(-time.Nanosecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", 0)

	updatedAtBefore := repositoryExtractionWorkUpdatedAtForPolicyTest(t, ctx, pool, work.WorkItemID)
	decidedAt := finish.FinishedAt.Add(time.Millisecond)
	result, err := classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, decidedAt)
	if err != nil {
		t.Fatalf("classify failed work: %v", err)
	}
	wantRetryNotBefore := decidedAt.Add(5 * time.Second)
	if result.Replayed ||
		result.RequestID != input.RequestID ||
		result.Work != work ||
		result.ClaimID != claim.ClaimID ||
		result.AttemptNumber != 1 ||
		result.WorkerID != claim.WorkerID ||
		!result.FinishedAt.Equal(finish.FinishedAt) ||
		result.FailureClass != string(ErrorRunnerInvocationFailed) ||
		result.FailureMessage != finish.FailureMessage ||
		result.PolicyVersion != RepositoryExtractionWorkFailurePolicyV1 ||
		result.Decision != RepositoryExtractionWorkFailureDecisionRetry ||
		result.DecisionReason != RepositoryExtractionWorkFailureReasonRetryable ||
		result.MaxAttempts != RepositoryExtractionWorkFailurePolicyMaxAttempts ||
		result.BackoffMilliseconds != 5000 ||
		result.RetryNotBefore == nil ||
		!result.RetryNotBefore.Equal(wantRetryNotBefore) ||
		!result.DecidedAt.Equal(decidedAt) ||
		result.DecisionActorID != input.DecisionActorID {
		t.Fatalf("failure policy result = %+v", result)
	}
	updatedAtAfter := repositoryExtractionWorkUpdatedAtForPolicyTest(t, ctx, pool, work.WorkItemID)
	if !updatedAtAfter.Equal(updatedAtBefore) {
		t.Fatalf("work updated_at changed from %s to %s", updatedAtBefore, updatedAtAfter)
	}
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, work.WorkItemID, RepositoryExtractionWorkOutcomeFailed, "", finish.FailureClass, finish.FailureMessage)
	assertFailedRepositoryExtractionWorkClaimAttempt(t, ctx, pool, claim.ClaimID, finish)

	replay, err := classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, decidedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay failure policy: %v", err)
	}
	if !replay.Replayed || !sameRepositoryExtractionWorkFailurePolicyResult(replay, result) {
		t.Fatalf("failure policy replay = %+v, first = %+v", replay, result)
	}

	reused := input
	reused.DecisionActorID = "worker-controller-2"
	_, err = ClassifyFailedRepositoryExtractionWork(ctx, pool, reused)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	secondRequest := input
	secondRequest.RequestID = "failure-policy-retry-second-request"
	_, err = classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, secondRequest, decidedAt.Add(2*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_retry_requests", 0)
}

func TestIntegrationRepositoryExtractionWorkFailurePolicyPersistsTerminalDecisions(t *testing.T) {
	tests := []struct {
		name         string
		failureClass string
		decision     string
		reason       string
	}{
		{
			name:         "deterministic failure stops",
			failureClass: string(ErrorInvalidExtractorOutput),
			decision:     RepositoryExtractionWorkFailureDecisionStop,
			reason:       RepositoryExtractionWorkFailureReasonDeterministic,
		},
		{
			name:         "unknown failure requires review",
			failureClass: "future_failure_class",
			decision:     RepositoryExtractionWorkFailureDecisionManualReview,
			reason:       RepositoryExtractionWorkFailureReasonUnclassified,
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, pool := integrationPool(t)
			suffix := fmt.Sprintf("failure-policy-terminal-%d", index)
			base := time.Date(2026, 7, 21, 23, index, 0, 0, time.UTC)
			work, claim, finish := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, suffix, tt.failureClass, base)
			result, err := classifyFailedRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFailurePolicyInput{
				RequestID:       suffix,
				WorkItemID:      work.WorkItemID,
				ClaimID:         claim.ClaimID,
				DecisionActorID: "worker-controller-terminal",
			}, finish.FinishedAt.Add(time.Millisecond))
			if err != nil {
				t.Fatalf("classify terminal decision: %v", err)
			}
			if result.Decision != tt.decision || result.DecisionReason != tt.reason || result.BackoffMilliseconds != 0 || result.RetryNotBefore != nil {
				t.Fatalf("terminal failure policy result = %+v", result)
			}
			assertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", 1)
		})
	}
}

func TestIntegrationRepositoryExtractionWorkFailurePolicyConcurrentRequestsHaveOneWinner(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Now().UTC().Add(-time.Second)
	work, claim, _ := persistFailedRepositoryExtractionWorkForPolicy(t, ctx, pool, "failure-policy-concurrent", string(ErrorRunnerInvocationTimeout), base)

	errs := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		go func(index int) {
			_, err := ClassifyFailedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkFailurePolicyInput{
				RequestID:       fmt.Sprintf("failure-policy-concurrent-%d", index),
				WorkItemID:      work.WorkItemID,
				ClaimID:         claim.ClaimID,
				DecisionActorID: fmt.Sprintf("worker-controller-%d", index),
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
			t.Fatalf("concurrent failure policy error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent failure policy succeeded/conflicted = %d/%d, want 1/1", succeeded, conflicted)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_failure_policy_decisions", 1)
}

func persistFailedRepositoryExtractionWorkForPolicy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix, failureClass string, base time.Time) (RepositoryExtractionWork, RepositoryExtractionWorkClaimResult, RepositoryExtractionWorkFinishResult) {
	t.Helper()
	_, stable := persistStableWorkObservation(t, ctx, pool, suffix, strings.Repeat("a", 40), suffix, base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-"+suffix, stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-" + suffix,
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-" + suffix,
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim failure policy work: %v", err)
	}
	finish, err := finishRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-" + suffix,
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        claim.ClaimID,
		WorkerID:       claim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   failureClass,
		FailureMessage: "terminal failure for " + suffix,
	}, claimedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("finish failure policy work: %v", err)
	}
	return scheduled.Work, claim, finish
}

func repositoryExtractionWorkUpdatedAtForPolicyTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItemID string) time.Time {
	t.Helper()
	var updatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT updated_at
		FROM repository_extraction_work_items
		WHERE work_item_id = $1
	`, workItemID).Scan(&updatedAt); err != nil {
		t.Fatalf("read repository extraction work updated_at: %v", err)
	}
	return updatedAt.UTC()
}

func sameRepositoryExtractionWorkFailurePolicyResult(left, right RepositoryExtractionWorkFailurePolicyResult) bool {
	left.Replayed = false
	right.Replayed = false
	return left.RequestID == right.RequestID &&
		left.Work == right.Work &&
		left.ClaimID == right.ClaimID &&
		left.AttemptNumber == right.AttemptNumber &&
		left.WorkerID == right.WorkerID &&
		left.FinishedAt.Equal(right.FinishedAt) &&
		left.FailureClass == right.FailureClass &&
		left.FailureMessage == right.FailureMessage &&
		left.PolicyVersion == right.PolicyVersion &&
		left.Decision == right.Decision &&
		left.DecisionReason == right.DecisionReason &&
		left.MaxAttempts == right.MaxAttempts &&
		left.BackoffMilliseconds == right.BackoffMilliseconds &&
		sameOptionalTime(left.RetryNotBefore, right.RetryNotBefore) &&
		left.DecidedAt.Equal(right.DecidedAt) &&
		left.DecisionActorID == right.DecisionActorID
}
