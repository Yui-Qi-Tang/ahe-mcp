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

func TestIntegrationRepositoryExtractionWorkSchedulingAndClaiming(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 14, 0, 0, 0, time.UTC)
	parser := ExtractorRepositoryGoParserCodeFact
	gopls := ExtractorRepositoryGoplsCodeFact

	firstA, stableA := persistStableWorkObservation(t, ctx, pool, "work-repo", strings.Repeat("a", 40), "a", base, false)
	_, err := ScheduleRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkScheduleInput{
		RequestID:            "schedule-a-unstable",
		ObservationRequestID: firstA.RequestID,
		ExtractorName:        parser,
	})
	assertKind(t, err, ErrorRepositoryWorkConflict)

	scheduleA := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-a", stableA.RequestID, parser)
	if !scheduleA.Created || scheduleA.Coalesced || scheduleA.Replayed || scheduleA.SupersededWorkItemID != "" || scheduleA.Work.ObservationID != stableA.ObservationID {
		t.Fatalf("schedule A = %+v", scheduleA)
	}
	scheduleAReplay := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-a", stableA.RequestID, parser)
	wantScheduleAReplay := scheduleA
	wantScheduleAReplay.Replayed = true
	if scheduleAReplay != wantScheduleAReplay {
		t.Fatalf("schedule A replay = %+v, want %+v", scheduleAReplay, wantScheduleAReplay)
	}
	scheduleACoalesced := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-a-coalesced", stableA.RequestID, parser)
	if scheduleACoalesced.Created || !scheduleACoalesced.Coalesced || scheduleACoalesced.Work != scheduleA.Work {
		t.Fatalf("coalesced schedule A = %+v, first = %+v", scheduleACoalesced, scheduleA)
	}
	_, err = ScheduleRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkScheduleInput{
		RequestID:            "schedule-a",
		ObservationRequestID: stableA.RequestID,
		ExtractorName:        gopls,
	})
	assertKind(t, err, ErrorIdempotencyKeyReused)

	claimATime := base.Add(10 * time.Millisecond)
	claimA, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-a",
		RepoID:                    "work-repo",
		ExtractorName:             parser,
		WorkerID:                  "worker-a",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimATime)
	if err != nil {
		t.Fatalf("claimRepositoryExtractionWork(A) error = %v", err)
	}
	if !claimA.Claimed || claimA.Replayed || claimA.Work == nil || *claimA.Work != scheduleA.Work || claimA.ClaimID == "" || claimA.AttemptNumber != 1 || claimA.ClaimedAt == nil || !claimA.ClaimedAt.Equal(claimATime) || claimA.LeaseExpiresAt == nil || !claimA.LeaseExpiresAt.Equal(claimATime.Add(time.Minute)) {
		t.Fatalf("claim A = %+v, schedule = %+v", claimA, scheduleA)
	}
	claimAReplay, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-a",
		RepoID:                    "work-repo",
		ExtractorName:             parser,
		WorkerID:                  "worker-a",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}, claimATime.Add(time.Second))
	if err != nil {
		t.Fatalf("claimRepositoryExtractionWork(A replay) error = %v", err)
	}
	if !claimAReplay.Replayed || !sameRepositoryExtractionClaimResult(claimAReplay, claimA) {
		t.Fatalf("claim A replay = %+v, first = %+v", claimAReplay, claimA)
	}
	differentLease := RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-a",
		RepoID:                    "work-repo",
		ExtractorName:             parser,
		WorkerID:                  "worker-a",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds + 1,
	}
	_, err = ClaimRepositoryExtractionWork(ctx, pool, differentLease)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	noWorkA := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-a-second", "work-repo", parser, "worker-b")
	if noWorkA.Claimed || noWorkA.Work != nil || noWorkA.ClaimID != "" || noWorkA.ClaimedAt != nil {
		t.Fatalf("second claim while A running = %+v", noWorkA)
	}
	_, err = ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-a-second",
		RepoID:                    "work-repo",
		ExtractorName:             parser,
		WorkerID:                  "different-worker",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	})
	assertKind(t, err, ErrorIdempotencyKeyReused)

	_, stableB := persistStableWorkObservation(t, ctx, pool, "work-repo", strings.Repeat("b", 40), "b", base.Add(20*time.Millisecond), false)
	scheduleBParser := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-b-parser", stableB.RequestID, parser)
	if !scheduleBParser.Created || scheduleBParser.SupersededWorkItemID != "" {
		t.Fatalf("schedule B parser behind running A = %+v", scheduleBParser)
	}
	scheduleBGopls := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-b-gopls", stableB.RequestID, gopls)
	if !scheduleBGopls.Created || scheduleBGopls.SupersededWorkItemID != "" {
		t.Fatalf("schedule B gopls = %+v", scheduleBGopls)
	}

	_, stableC := persistStableWorkObservation(t, ctx, pool, "work-repo", strings.Repeat("c", 40), "c", base.Add(40*time.Millisecond), false)
	scheduleCParser := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-c-parser", stableC.RequestID, parser)
	if !scheduleCParser.Created || scheduleCParser.SupersededWorkItemID != scheduleBParser.Work.WorkItemID {
		t.Fatalf("schedule C parser = %+v, B = %+v", scheduleCParser, scheduleBParser)
	}
	scheduleCGopls := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-c-gopls", stableC.RequestID, gopls)
	if !scheduleCGopls.Created || scheduleCGopls.SupersededWorkItemID != scheduleBGopls.Work.WorkItemID {
		t.Fatalf("schedule C gopls = %+v, B = %+v", scheduleCGopls, scheduleBGopls)
	}
	_, err = ScheduleRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkScheduleInput{
		RequestID:            "schedule-stale-b",
		ObservationRequestID: stableB.RequestID,
		ExtractorName:        parser,
	})
	assertKind(t, err, ErrorRepositoryWorkConflict)

	blockedParser := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-c-parser-blocked", "work-repo", parser, "worker-c")
	if blockedParser.Claimed {
		t.Fatalf("parser claim while A running = %+v", blockedParser)
	}
	claimCGopls := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-c-gopls", "work-repo", gopls, "worker-gopls")
	if !claimCGopls.Claimed || claimCGopls.Work == nil || *claimCGopls.Work != scheduleCGopls.Work {
		t.Fatalf("gopls claim C = %+v, schedule = %+v", claimCGopls, scheduleCGopls)
	}

	_, stableDirty := persistStableWorkObservation(t, ctx, pool, "dirty-work-repo", strings.Repeat("d", 40), "dirty", base, true)
	_, err = ScheduleRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkScheduleInput{
		RequestID:            "schedule-dirty",
		ObservationRequestID: stableDirty.RequestID,
		ExtractorName:        parser,
	})
	assertKind(t, err, ErrorRepositoryWorkConflict)

	_, stableConcurrent := persistStableWorkObservation(t, ctx, pool, "concurrent-work-repo", strings.Repeat("e", 40), "concurrent", base, false)
	concurrentSchedule := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-concurrent", stableConcurrent.RequestID, parser)
	concurrentClaims := make(chan RepositoryExtractionWorkClaimResult, 2)
	concurrentErrors := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(index int) {
			result, err := ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
				RequestID:                 fmt.Sprintf("claim-concurrent-%d", index),
				RepoID:                    "concurrent-work-repo",
				ExtractorName:             parser,
				WorkerID:                  fmt.Sprintf("worker-concurrent-%d", index),
				LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
			})
			concurrentClaims <- result
			concurrentErrors <- err
		}(i)
	}
	claimed := 0
	for range 2 {
		if err := <-concurrentErrors; err != nil {
			t.Fatalf("concurrent claim error = %v", err)
		}
		result := <-concurrentClaims
		if result.Claimed {
			claimed++
			if result.Work == nil || *result.Work != concurrentSchedule.Work {
				t.Fatalf("concurrent claimed work = %+v, schedule = %+v", result, concurrentSchedule)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent successful claims = %d, want 1", claimed)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO repository_extraction_work_claim_requests (
			request_id, repo_id, extractor_name, worker_id,
			work_item_id, claim_id, claimed_at, claimed, request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8)
	`, "claim-mismatched-work", "work-repo", parser, "worker-mismatch", scheduleA.Work.WorkItemID, claimCGopls.ClaimID, base, "sha256:mismatch"); err == nil {
		t.Fatal("mismatched work_item_id and claim_id insert succeeded")
	}

	assertTableCount(t, ctx, pool, "repository_extraction_work_items", 6)
	assertTableCount(t, ctx, pool, "repository_extraction_work_schedule_requests", 7)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_requests", 6)
	assertTableCount(t, ctx, pool, "repository_extraction_work_claim_attempts", 3)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claimA)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claimCGopls)
	assertRepositoryExtractionWorkStatusCounts(t, ctx, pool, 1, 3, 2)
	assertRepositoryExtractionWorkSupersededBy(t, ctx, pool, scheduleBParser.Work.WorkItemID, scheduleCParser.Work.WorkItemID)
	assertRepositoryExtractionWorkSupersededBy(t, ctx, pool, scheduleBGopls.Work.WorkItemID, scheduleCGopls.Work.WorkItemID)
	for _, table := range []string{"source_snapshots", "repository_snapshots", "proposal_occurrences", "repository_source_generations"} {
		assertTableCount(t, ctx, pool, table, 0)
	}
}

func persistStableWorkObservation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoID, headSHA, requestPrefix string, firstAt time.Time, dirty bool) (GitRepositoryChangeObservationResult, GitRepositoryChangeObservationResult) {
	t.Helper()
	inspection := GitRepositoryChangeInspection{
		TokenContract:            GitRepositoryChangeTokenV1,
		DirtyFingerprintContract: GitRepositoryDirtyFingerprintV1,
		RepoID:                   repoID,
		HeadCommitSHA:            headSHA,
	}
	if dirty {
		inspection.Dirty = true
		inspection.DirtyFingerprint = contentHash([]byte(requestPrefix + "-dirty"))
		inspection.TrackedChangeCount = 1
	}
	changeToken, err := gitRepositoryChangeToken(repoID, headSHA, inspection.DirtyFingerprint)
	if err != nil {
		t.Fatalf("gitRepositoryChangeToken(%s) error = %v", requestPrefix, err)
	}
	inspection.ChangeToken = changeToken
	window := time.Millisecond
	first := recordGitRepositoryChangeObservationForTest(t, ctx, pool, requestPrefix+"-first", inspection, window, firstAt)
	stable := recordGitRepositoryChangeObservationForTest(t, ctx, pool, requestPrefix+"-stable", inspection, window, firstAt.Add(window))
	if first.Stable || !stable.Stable {
		t.Fatalf("observation stability %s = first %+v stable %+v", requestPrefix, first, stable)
	}
	return first, stable
}

func scheduleRepositoryExtractionWorkForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID, observationRequestID, extractorName string) RepositoryExtractionWorkScheduleResult {
	t.Helper()
	result, err := ScheduleRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkScheduleInput{
		RequestID:            requestID,
		ObservationRequestID: observationRequestID,
		ExtractorName:        extractorName,
	})
	if err != nil {
		t.Fatalf("ScheduleRepositoryExtractionWork(%s) error = %v", requestID, err)
	}
	return result
}

func claimRepositoryExtractionWorkForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID, repoID, extractorName, workerID string) RepositoryExtractionWorkClaimResult {
	t.Helper()
	result, err := ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
		RequestID:                 requestID,
		RepoID:                    repoID,
		ExtractorName:             extractorName,
		WorkerID:                  workerID,
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	})
	if err != nil {
		t.Fatalf("ClaimRepositoryExtractionWork(%s) error = %v", requestID, err)
	}
	return result
}

func sameRepositoryExtractionClaimResult(left, right RepositoryExtractionWorkClaimResult) bool {
	left.Replayed = false
	right.Replayed = false
	if left.RequestID != right.RequestID || left.RepoID != right.RepoID || left.ExtractorName != right.ExtractorName || left.WorkerID != right.WorkerID || left.Claimed != right.Claimed || left.ClaimID != right.ClaimID || left.AttemptNumber != right.AttemptNumber {
		return false
	}
	if (left.Work == nil) != (right.Work == nil) || (left.ClaimedAt == nil) != (right.ClaimedAt == nil) || (left.LeaseExpiresAt == nil) != (right.LeaseExpiresAt == nil) {
		return false
	}
	if left.Work != nil && *left.Work != *right.Work {
		return false
	}
	if left.ClaimedAt != nil && !left.ClaimedAt.Equal(*right.ClaimedAt) {
		return false
	}
	return left.LeaseExpiresAt == nil || left.LeaseExpiresAt.Equal(*right.LeaseExpiresAt)
}

func assertRepositoryExtractionWorkRunningClaimAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim RepositoryExtractionWorkClaimResult) {
	t.Helper()
	if !claim.Claimed || claim.Work == nil || claim.ClaimedAt == nil || claim.LeaseExpiresAt == nil {
		t.Fatalf("claim has no attempt authority: %+v", claim)
	}
	var workItemID, workerID, status string
	var attemptNumber int
	var claimedAt, leaseExpiresAt time.Time
	var finishedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT work_item_id, attempt_number, worker_id, status, claimed_at, lease_expires_at, finished_at
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = $1
	`, claim.ClaimID).Scan(&workItemID, &attemptNumber, &workerID, &status, &claimedAt, &leaseExpiresAt, &finishedAt); err != nil {
		t.Fatalf("reading claim attempt %s: %v", claim.ClaimID, err)
	}
	if workItemID != claim.Work.WorkItemID || attemptNumber != claim.AttemptNumber || workerID != claim.WorkerID || status != repositoryExtractionWorkRunning || !claimedAt.Equal(*claim.ClaimedAt) || !leaseExpiresAt.Equal(*claim.LeaseExpiresAt) || finishedAt != nil {
		t.Fatalf("claim attempt %s = %s/%d/%s/%s/%s/%s/%v, claim = %+v", claim.ClaimID, workItemID, attemptNumber, workerID, status, claimedAt, leaseExpiresAt, finishedAt, claim)
	}
}

func assertRepositoryExtractionWorkStatusCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pending, running, superseded int) {
	t.Helper()
	var gotPending, gotRunning, gotSuperseded int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'superseded')
		FROM repository_extraction_work_items
	`).Scan(&gotPending, &gotRunning, &gotSuperseded); err != nil {
		t.Fatalf("reading repository extraction work status counts: %v", err)
	}
	if gotPending != pending || gotRunning != running || gotSuperseded != superseded {
		t.Fatalf("work status counts = %d/%d/%d, want %d/%d/%d", gotPending, gotRunning, gotSuperseded, pending, running, superseded)
	}
}

func assertRepositoryExtractionWorkSupersededBy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workItemID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `
		SELECT superseded_by_work_item_id
		FROM repository_extraction_work_items
		WHERE work_item_id = $1
	`, workItemID).Scan(&got); err != nil {
		t.Fatalf("reading superseded work %s: %v", workItemID, err)
	}
	if got != want {
		t.Fatalf("work %s superseded_by = %s, want %s", workItemID, got, want)
	}
}
