//go:build integration

package evidenceingestion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationExecuteClaimedRepositoryExtractionWorkSucceedsAndReplaysWithoutActivation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/work-execution", "package sample\n\nfunc ExecuteMe() {}\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-execution", commitSHA, "work-execution", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-execution", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-work-execution", scheduled.Work.RepoID, scheduled.Work.ExtractorName, "worker-execution")
	input := RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: root,
	}

	first, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, input)
	if err != nil {
		t.Fatalf("ExecuteClaimedRepositoryExtractionWork() error = %v", err)
	}
	if first.Replayed || first.RequestID != input.RequestID || first.Work != scheduled.Work || first.ClaimID != claim.ClaimID || first.WorkerID != claim.WorkerID {
		t.Fatalf("first work execution = %+v, claim = %+v", first, claim)
	}
	if first.Extraction.Replayed || first.Extraction.SourceGeneration.ID == "" || first.Extraction.SourceGeneration.RepoID != scheduled.Work.RepoID || first.Extraction.SourceGeneration.ExtractorName != scheduled.Work.ExtractorName || first.Extraction.SourceGeneration.CommitSHA != scheduled.Work.HeadCommitSHA {
		t.Fatalf("first execution extraction = %+v", first.Extraction)
	}
	if first.Extraction.DeltaExtraction == nil || first.Extraction.DeltaExtraction.Decision != RepositoryDeltaDecisionFullFallback || first.Extraction.DeltaExtraction.FallbackReason != RepositoryDeltaFallbackNoBaseGeneration {
		t.Fatalf("first execution delta audit = %+v", first.Extraction.DeltaExtraction)
	}
	if first.Finish.Replayed || first.Finish.Outcome != RepositoryExtractionWorkOutcomeSucceeded || first.Finish.Work != scheduled.Work || first.Finish.ClaimID != claim.ClaimID || first.Finish.AttemptNumber != 1 || first.Finish.SourceGenerationID != first.Extraction.SourceGeneration.ID {
		t.Fatalf("first execution finish = %+v", first.Finish)
	}
	assertRepositoryExtractionWorkTerminalState(t, ctx, pool, scheduled.Work.WorkItemID, RepositoryExtractionWorkOutcomeSucceeded, first.Extraction.SourceGeneration.ID, "", "")

	replay, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay ExecuteClaimedRepositoryExtractionWork() error = %v", err)
	}
	if !replay.Replayed || !replay.Extraction.Replayed || !replay.Extraction.SourceGenerationReplayed || replay.Extraction.DeltaExtraction == nil || !replay.Extraction.DeltaExtraction.Replayed || !replay.Finish.Replayed {
		t.Fatalf("replayed work execution = %+v", replay)
	}
	if replay.Work != first.Work || replay.ClaimID != first.ClaimID || replay.HeartbeatCount != first.HeartbeatCount || replay.Extraction.ExtractionAttemptID != first.Extraction.ExtractionAttemptID || replay.Extraction.ProposalBatchID != first.Extraction.ProposalBatchID || replay.Extraction.SourceGeneration.ID != first.Extraction.SourceGeneration.ID || !replay.Finish.FinishedAt.Equal(first.Finish.FinishedAt) {
		t.Fatalf("replayed work execution identity = %+v, first = %+v", replay, first)
	}

	reused := input
	reused.WorkspaceRoot = t.TempDir()
	_, err = ExecuteClaimedRepositoryExtractionWork(ctx, pool, reused)
	assertKind(t, err, ErrorIdempotencyKeyReused)
	secondRequest := input
	secondRequest.RequestID = "execute-work-again"
	_, err = ExecuteClaimedRepositoryExtractionWork(ctx, pool, secondRequest)
	assertKind(t, err, ErrorRepositoryWorkConflict)

	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertRepositoryExtractionWorkExecutionRequest(t, ctx, pool, input.RequestID, input.WorkItemID, input.ClaimID, input.WorkerID)
	assertTableCount(t, ctx, pool, "repository_snapshot_intake_requests", 1)
	assertTableCount(t, ctx, pool, "repository_source_generations", 1)
	assertTableCount(t, ctx, pool, "repository_delta_extractions", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 1)
	assertTableCount(t, ctx, pool, "repository_source_heads", 0)
	assertTableCount(t, ctx, pool, "repository_generation_activation_requests", 0)
}

func TestIntegrationExecuteClaimedRepositoryExtractionWorkHeartbeatsDuringLongSnapshot(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/work-execution-heartbeat", "package sample\n\nfunc HeartbeatMe() {}\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-execution-heartbeat", commitSHA, "work-execution-heartbeat", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-execution-heartbeat", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	const leaseMilliseconds int64 = 600
	claim, err := ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-work-execution-heartbeat",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-execution-heartbeat",
		LeaseDurationMilliseconds: leaseMilliseconds,
	})
	if err != nil {
		t.Fatalf("claim heartbeat execution work: %v", err)
	}

	originalPath := os.Getenv("PATH")
	binDir := t.TempDir()
	gitWrapper := filepath.Join(binDir, "git")
	wrapper := "#!/bin/sh\nsleep 0.4\nexec " + strconv.Quote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(gitWrapper, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write delayed git wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+originalPath)

	result, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-heartbeat",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("ExecuteClaimedRepositoryExtractionWork() with heartbeat error = %v", err)
	}
	if result.HeartbeatCount < 1 {
		t.Fatalf("execution heartbeat count = %d, want at least 1", result.HeartbeatCount)
	}
	if claim.LeaseExpiresAt == nil || !result.Finish.LeaseExpiresAt.After(*claim.LeaseExpiresAt) {
		t.Fatalf("finish lease expiry = %s, initial expiry = %v", result.Finish.LeaseExpiresAt, claim.LeaseExpiresAt)
	}
	var storedDuration int64
	var storedCount, renewalCount int
	if err := pool.QueryRow(ctx, `
		SELECT heartbeat_lease_duration_milliseconds, heartbeat_count
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, result.RequestID).Scan(&storedDuration, &storedCount); err != nil {
		t.Fatalf("read execution heartbeat state: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM repository_extraction_work_lease_renewal_requests
		WHERE claim_id = $1
	`, claim.ClaimID).Scan(&renewalCount); err != nil {
		t.Fatalf("count execution heartbeat renewals: %v", err)
	}
	if storedDuration != leaseMilliseconds || storedCount != result.HeartbeatCount || renewalCount != storedCount {
		t.Fatalf("heartbeat duration/count/renewals = %d/%d/%d, result = %+v", storedDuration, storedCount, renewalCount, result)
	}
	t.Setenv("PATH", originalPath)
	replay, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkExecutionInput{
		RequestID:     result.RequestID,
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("replay heartbeat execution: %v", err)
	}
	if !replay.Replayed || replay.HeartbeatCount != result.HeartbeatCount {
		t.Fatalf("terminal heartbeat replay = %+v, first = %+v", replay, result)
	}
	var replayRenewalCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM repository_extraction_work_lease_renewal_requests
		WHERE claim_id = $1
	`, claim.ClaimID).Scan(&replayRenewalCount); err != nil {
		t.Fatalf("count terminal replay renewals: %v", err)
	}
	if replayRenewalCount != renewalCount {
		t.Fatalf("terminal replay renewal count = %d, want unchanged %d", replayRenewalCount, renewalCount)
	}
}

func TestIntegrationRepositoryExtractionWorkHeartbeatCancelsExecutionAfterAuthorityLoss(t *testing.T) {
	ctx, pool := integrationPool(t)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-heartbeat-authority-loss", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "work-heartbeat-authority-loss", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-heartbeat-authority-loss", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim, err := ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-work-heartbeat-authority-loss",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-heartbeat-authority-loss",
		LeaseDurationMilliseconds: 300,
	})
	if err != nil {
		t.Fatalf("claim authority-loss work: %v", err)
	}
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-heartbeat-authority-loss",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare authority-loss execution: %v", err)
	}
	reserved, _, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, time.Now().UTC())
	if err != nil {
		t.Fatalf("reserve authority-loss execution: %v", err)
	}
	heartbeat, err := startRepositoryExtractionWorkHeartbeat(ctx, pool, reserved)
	if err != nil {
		t.Fatalf("start authority-loss heartbeat: %v", err)
	}
	defer heartbeat.stop()

	_, err = FinishRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkFinishInput{
		RequestID:      "finish-work-heartbeat-authority-loss",
		WorkItemID:     scheduled.Work.WorkItemID,
		ClaimID:        claim.ClaimID,
		WorkerID:       claim.WorkerID,
		Outcome:        RepositoryExtractionWorkOutcomeFailed,
		FailureClass:   "authority_lost_fixture",
		FailureMessage: "test fixture ends the exact claim while its execution heartbeat is active",
	})
	if err != nil {
		t.Fatalf("finish authority-loss fixture: %v", err)
	}
	select {
	case <-heartbeat.context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("execution context was not canceled after heartbeat lost claim authority")
	}
	assertKind(t, context.Cause(heartbeat.context()), ErrorRepositoryWorkConflict)
	heartbeatErr := heartbeat.stop()
	assertKind(t, heartbeatErr, ErrorRepositoryWorkConflict)

	var storedCount, renewalCount int
	if err := pool.QueryRow(ctx, `
		SELECT heartbeat_count
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, reserved.input.RequestID).Scan(&storedCount); err != nil {
		t.Fatalf("read authority-loss heartbeat count: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM repository_extraction_work_lease_renewal_requests
		WHERE claim_id = $1
	`, claim.ClaimID).Scan(&renewalCount); err != nil {
		t.Fatalf("count authority-loss renewals: %v", err)
	}
	if storedCount != heartbeat.count() || renewalCount != storedCount {
		t.Fatalf("authority-loss heartbeat stored/runtime/renewal counts = %d/%d/%d", storedCount, heartbeat.count(), renewalCount)
	}
}

func TestIntegrationRepositoryExtractionWorkHeartbeatHonorsConcurrentLongerManualRenewal(t *testing.T) {
	ctx, pool := integrationPool(t)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-heartbeat-manual-renewal", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "work-heartbeat-manual-renewal", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-heartbeat-manual-renewal", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim, err := ClaimRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-work-heartbeat-manual-renewal",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-heartbeat-manual-renewal",
		LeaseDurationMilliseconds: 300,
	})
	if err != nil {
		t.Fatalf("claim manual-renewal work: %v", err)
	}
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-heartbeat-manual-renewal",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare manual-renewal execution: %v", err)
	}
	reserved, _, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, time.Now().UTC())
	if err != nil {
		t.Fatalf("reserve manual-renewal execution: %v", err)
	}
	heartbeat, err := startRepositoryExtractionWorkHeartbeat(ctx, pool, reserved)
	if err != nil {
		t.Fatalf("start manual-renewal heartbeat: %v", err)
	}
	defer heartbeat.stop()
	manual, err := RenewRepositoryExtractionWorkLease(ctx, pool, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "manual-renewal-during-execution",
		WorkItemID:                scheduled.Work.WorkItemID,
		ClaimID:                   claim.ClaimID,
		WorkerID:                  claim.WorkerID,
		LeaseDurationMilliseconds: 1000,
	})
	if err != nil {
		t.Fatalf("manually renew active execution claim: %v", err)
	}
	if claim.LeaseExpiresAt == nil || !manual.LeaseExpiresAt.After(*claim.LeaseExpiresAt) {
		t.Fatalf("manual renewal expiry = %s, initial = %v", manual.LeaseExpiresAt, claim.LeaseExpiresAt)
	}

	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-heartbeat.context().Done():
		t.Fatalf("heartbeat canceled after valid longer manual renewal: %v", context.Cause(heartbeat.context()))
	case <-timer.C:
	}
	if err := heartbeat.stop(); err != nil {
		t.Fatalf("stop heartbeat after manual renewal: %v", err)
	}
	if heartbeat.count() != 0 {
		t.Fatalf("automatic heartbeat count after longer manual renewal = %d, want 0", heartbeat.count())
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 1)
	assertRepositoryExtractionWorkClaimLease(t, ctx, pool, claim.ClaimID, manual.LeaseExpiresAt)
}

func TestIntegrationRepositoryExtractionWorkHeartbeatStartAdoptsNewerLease(t *testing.T) {
	ctx, pool := integrationPool(t)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-heartbeat-start-newer-lease", "ffffffffffffffffffffffffffffffffffffffff", "work-heartbeat-start-newer-lease", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-heartbeat-start-newer-lease", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-work-heartbeat-start-newer-lease", scheduled.Work.RepoID, scheduled.Work.ExtractorName, "worker-heartbeat-start-newer-lease")
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-heartbeat-start-newer-lease",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare newer-lease execution: %v", err)
	}
	reserved, _, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, time.Now().UTC())
	if err != nil {
		t.Fatalf("reserve newer-lease execution: %v", err)
	}
	manual, err := RenewRepositoryExtractionWorkLease(ctx, pool, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 "manual-renewal-before-heartbeat-start",
		WorkItemID:                scheduled.Work.WorkItemID,
		ClaimID:                   claim.ClaimID,
		WorkerID:                  claim.WorkerID,
		LeaseDurationMilliseconds: RepositoryExtractionWorkMaxLeaseDuration.Milliseconds(),
	})
	if err != nil {
		t.Fatalf("manually renew before heartbeat start: %v", err)
	}
	reserved.leaseExpiresAt = time.Now().UTC()
	heartbeat, err := startRepositoryExtractionWorkHeartbeat(ctx, pool, reserved)
	if err != nil {
		t.Fatalf("start heartbeat with stale in-memory expiry: %v", err)
	}
	if err := heartbeat.stop(); err != nil {
		t.Fatalf("stop heartbeat with adopted newer lease: %v", err)
	}
	if heartbeat.count() != 0 {
		t.Fatalf("automatic heartbeat count after adopting newer lease = %d, want 0", heartbeat.count())
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 1)
	assertRepositoryExtractionWorkClaimLease(t, ctx, pool, claim.ClaimID, manual.LeaseExpiresAt)
}

func TestIntegrationRepositoryExtractionWorkHeartbeatRepairsRenewalCountCrashGap(t *testing.T) {
	ctx, pool := integrationPool(t)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-heartbeat-crash-gap", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "work-heartbeat-crash-gap", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-heartbeat-crash-gap", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-work-heartbeat-crash-gap", scheduled.Work.RepoID, scheduled.Work.ExtractorName, "worker-heartbeat-crash-gap")
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-heartbeat-crash-gap",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare crash-gap execution: %v", err)
	}
	reserved, _, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, time.Now().UTC())
	if err != nil {
		t.Fatalf("reserve crash-gap execution: %v", err)
	}
	heartbeatRequestID, err := repositoryExtractionWorkExecutionHeartbeatRequestID(reserved.input.RequestID, 1)
	if err != nil {
		t.Fatalf("derive crash-gap heartbeat request ID: %v", err)
	}
	first, err := RenewRepositoryExtractionWorkLease(ctx, pool, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 heartbeatRequestID,
		WorkItemID:                reserved.input.WorkItemID,
		ClaimID:                   reserved.input.ClaimID,
		WorkerID:                  reserved.input.WorkerID,
		LeaseDurationMilliseconds: reserved.heartbeatLeaseMilliseconds,
	})
	if err != nil {
		t.Fatalf("persist crash-gap renewal: %v", err)
	}
	var beforeCount int
	if err := pool.QueryRow(ctx, `
		SELECT heartbeat_count
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, reserved.input.RequestID).Scan(&beforeCount); err != nil {
		t.Fatalf("read crash-gap count before repair: %v", err)
	}
	if beforeCount != 0 {
		t.Fatalf("crash-gap count before repair = %d, want 0", beforeCount)
	}

	replayed, count, err := renewRepositoryExtractionWorkExecutionHeartbeat(ctx, pool, reserved)
	if err != nil {
		t.Fatalf("repair crash-gap heartbeat: %v", err)
	}
	if !replayed.Replayed || replayed.RequestID != first.RequestID || count != 1 {
		t.Fatalf("repaired crash-gap heartbeat = %+v, count = %d, first = %+v", replayed, count, first)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 1)
	var afterCount int
	if err := pool.QueryRow(ctx, `
		SELECT heartbeat_count
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, reserved.input.RequestID).Scan(&afterCount); err != nil {
		t.Fatalf("read crash-gap count after repair: %v", err)
	}
	if afterCount != 1 {
		t.Fatalf("crash-gap count after repair = %d, want 1", afterCount)
	}
}

func TestIntegrationRepositoryExtractionWorkHeartbeatConcurrentSequenceCoalesces(t *testing.T) {
	ctx, pool := integrationPool(t)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-heartbeat-concurrent", "dddddddddddddddddddddddddddddddddddddddd", "work-heartbeat-concurrent", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-heartbeat-concurrent", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-work-heartbeat-concurrent", scheduled.Work.RepoID, scheduled.Work.ExtractorName, "worker-heartbeat-concurrent")
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-heartbeat-concurrent",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare concurrent heartbeat execution: %v", err)
	}
	reserved, _, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, time.Now().UTC())
	if err != nil {
		t.Fatalf("reserve concurrent heartbeat execution: %v", err)
	}
	type heartbeatResult struct {
		renewal RepositoryExtractionWorkLeaseRenewalResult
		count   int
		err     error
	}
	results := make(chan heartbeatResult, 2)
	for range 2 {
		go func() {
			renewal, count, err := renewRepositoryExtractionWorkExecutionHeartbeat(ctx, pool, reserved)
			results <- heartbeatResult{renewal: renewal, count: count, err: err}
		}()
	}
	replayed := 0
	var requestID string
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent heartbeat error = %v", result.err)
		}
		if result.count != 1 {
			t.Fatalf("concurrent heartbeat count = %d, want 1", result.count)
		}
		if requestID == "" {
			requestID = result.renewal.RequestID
		} else if result.renewal.RequestID != requestID {
			t.Fatalf("concurrent heartbeat request IDs = %s/%s", requestID, result.renewal.RequestID)
		}
		if result.renewal.Replayed {
			replayed++
		}
	}
	if replayed != 1 {
		t.Fatalf("concurrent replayed heartbeat results = %d, want 1", replayed)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_lease_renewal_requests", 1)
	var storedCount int
	if err := pool.QueryRow(ctx, `
		SELECT heartbeat_count
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, reserved.input.RequestID).Scan(&storedCount); err != nil {
		t.Fatalf("read concurrent heartbeat count: %v", err)
	}
	if storedCount != 1 {
		t.Fatalf("concurrent stored heartbeat count = %d, want 1", storedCount)
	}
}

func TestIntegrationExecuteClaimedRepositoryExtractionWorkLeavesFailedExecutionRunning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	_, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/work-authority", "package authority\n\nfunc Authority() {}\n")
	wrongRoot, _ := repositoryWorkExecutionGitFixture(t, "example.com/wrong-workspace", "package wrong\n\nfunc Wrong() {}\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-execution-failure", commitSHA, "work-execution-failure", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-execution-failure", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-work-execution-failure", scheduled.Work.RepoID, scheduled.Work.ExtractorName, "worker-execution-failure")
	input := RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-failure",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: wrongRoot,
	}

	const concurrentExecutions = 16
	start := make(chan struct{})
	failures := make(chan error, concurrentExecutions)
	var wg sync.WaitGroup
	for range concurrentExecutions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, input)
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	var firstFailure string
	for err := range failures {
		assertKind(t, err, ErrorInvalidInput)
		if firstFailure == "" {
			firstFailure = err.Error()
			continue
		}
		if err.Error() != firstFailure {
			t.Fatalf("concurrent execution failure = %q, want %q", err, firstFailure)
		}
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertRepositoryExtractionWorkExecutionRequest(t, ctx, pool, input.RequestID, input.WorkItemID, input.ClaimID, input.WorkerID)
	assertTableCount(t, ctx, pool, "repository_snapshots", 0)
	assertTableCount(t, ctx, pool, "repository_extraction_run_requests", 0)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 0)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	var failureClass, failureMessage string
	var failedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT failure_class, failure_message, failed_at
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, input.RequestID).Scan(&failureClass, &failureMessage, &failedAt); err != nil {
		t.Fatalf("read execution failure audit: %v", err)
	}
	if failureClass != string(ErrorInvalidInput) ||
		failureMessage == "" ||
		failedAt.IsZero() {
		t.Fatalf(
			"execution failure audit = %q/%q/%s",
			failureClass,
			failureMessage,
			failedAt,
		)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE repository_extraction_work_execution_requests
		SET failure_message = NULL
		WHERE request_id = $1
	`, input.RequestID); err == nil {
		t.Fatal("execution request accepted a partial failure audit")
	}

	_, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, input)
	assertKind(t, err, ErrorInvalidInput)
	if err.Error() != firstFailure {
		t.Fatalf("terminal execution replay failure = %q, want %q", err, firstFailure)
	}
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 0)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)

	competing := input
	competing.RequestID = "execute-work-competing"
	_, err = ExecuteClaimedRepositoryExtractionWork(ctx, pool, competing)
	assertKind(t, err, ErrorRepositoryWorkConflict)
	wrongWorker := input
	wrongWorker.RequestID = "execute-work-wrong-worker"
	wrongWorker.WorkerID = "worker-other"
	_, err = ExecuteClaimedRepositoryExtractionWork(ctx, pool, wrongWorker)
	assertKind(t, err, ErrorRepositoryWorkConflict)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)
}

func TestIntegrationExecuteClaimedRepositoryExtractionWorkDoesNotClassifyExtractorFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx, pool := integrationPool(t)
	root, commitSHA := repositoryWorkExecutionGitFixture(t, "example.com/work-extractor-failure", "package sample\n\nfunc Broken(\n")
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-extractor-failure", commitSHA, "work-extractor-failure", time.Now().UTC().Add(-time.Second), false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-extractor-failure", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claim := claimRepositoryExtractionWorkForTest(t, ctx, pool, "claim-work-extractor-failure", scheduled.Work.RepoID, scheduled.Work.ExtractorName, "worker-extractor-failure")
	input := RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-extractor-failure",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: root,
	}

	_, err := ExecuteClaimedRepositoryExtractionWork(ctx, pool, input)
	assertKind(t, err, ErrorInvalidInput)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
	assertTableCount(t, ctx, pool, "repository_snapshots", 1)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "proposal_batches", 0)
	assertTableCount(t, ctx, pool, "repository_source_generations", 0)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 0)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)
	var attemptStatus, failureClass string
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_class
		FROM extraction_attempts
	`).Scan(&attemptStatus, &failureClass); err != nil {
		t.Fatalf("reading failed extractor attempt: %v", err)
	}
	if attemptStatus != attemptStatusFailed || failureClass != string(ErrorInvalidInput) {
		t.Fatalf("extractor attempt status/failure = %s/%s, want %s/%s", attemptStatus, failureClass, attemptStatusFailed, ErrorInvalidInput)
	}

	_, err = ExecuteClaimedRepositoryExtractionWork(ctx, pool, input)
	assertKind(t, err, ErrorPersistedAttemptFailed)
	assertTableCount(t, ctx, pool, "extraction_attempts", 1)
	assertTableCount(t, ctx, pool, "repository_extraction_work_finish_requests", 0)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)
}

func TestIntegrationReserveRepositoryExtractionWorkExecutionRejectsExactLeaseExpiry(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 22, 0, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-execution-expired", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "work-execution-expired", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-execution-expired", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-work-execution-expired",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-execution-expired",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim expiring execution work: %v", err)
	}
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-expired",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare expired execution: %v", err)
	}
	_, _, err = reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, claimedAt.Add(10*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkLeaseExpired)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 0)
	assertRepositoryExtractionWorkRunningClaimAttempt(t, ctx, pool, claim)
}

func TestIntegrationReserveRepositoryExtractionWorkExecutionReplayRejectsExpiredRunningClaim(t *testing.T) {
	ctx, pool := integrationPool(t)
	base := time.Date(2026, 7, 21, 22, 30, 0, 0, time.UTC)
	_, stable := persistStableWorkObservation(t, ctx, pool, "work-execution-replay-expired", "cccccccccccccccccccccccccccccccccccccccc", "work-execution-replay-expired", base, false)
	scheduled := scheduleRepositoryExtractionWorkForTest(t, ctx, pool, "schedule-work-execution-replay-expired", stable.RequestID, ExtractorRepositoryGoParserCodeFact)
	claimedAt := base.Add(10 * time.Millisecond)
	claim, err := claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-work-execution-replay-expired",
		RepoID:                    scheduled.Work.RepoID,
		ExtractorName:             scheduled.Work.ExtractorName,
		WorkerID:                  "worker-execution-replay-expired",
		LeaseDurationMilliseconds: 10,
	}, claimedAt)
	if err != nil {
		t.Fatalf("claim replay-expiring execution work: %v", err)
	}
	prepared, err := prepareRepositoryExtractionWorkExecution(RepositoryExtractionWorkExecutionInput{
		RequestID:     "execute-work-replay-expired",
		WorkItemID:    scheduled.Work.WorkItemID,
		ClaimID:       claim.ClaimID,
		WorkerID:      claim.WorkerID,
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("prepare replay-expiring execution: %v", err)
	}
	_, replayed, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, claimedAt.Add(5*time.Millisecond))
	if err != nil {
		t.Fatalf("reserve replay-expiring execution: %v", err)
	}
	if replayed {
		t.Fatal("first replay-expiring reservation reported replay")
	}
	_, _, err = reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, prepared, claimedAt.Add(10*time.Millisecond))
	assertKind(t, err, ErrorRepositoryWorkLeaseExpired)
	assertTableCount(t, ctx, pool, "repository_extraction_work_execution_requests", 1)
}

func repositoryWorkExecutionGitFixture(t *testing.T, module, source string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeGitSnapshotTestFile(t, filepath.Join(root, "go.mod"), []byte("module "+module+"\n\ngo 1.22\n"))
	writeGitSnapshotTestFile(t, filepath.Join(root, "main.go"), []byte(source))
	runGitSnapshotTestCommand(t, root, "init", "--quiet")
	runGitSnapshotTestCommand(t, root, "config", "user.name", "AHE Test")
	runGitSnapshotTestCommand(t, root, "config", "user.email", "ahe-test@example.com")
	runGitSnapshotTestCommand(t, root, "add", ".")
	runGitSnapshotTestCommand(t, root, "commit", "--quiet", "-m", "initial")
	return root, gitSnapshotTestOutput(t, root, "rev-parse", "HEAD")
}

func assertRepositoryExtractionWorkExecutionRequest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, requestID, workItemID, claimID, workerID string) {
	t.Helper()
	var gotWorkItemID, gotClaimID, gotWorkerID string
	if err := pool.QueryRow(ctx, `
		SELECT work_item_id, claim_id, worker_id
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, requestID).Scan(&gotWorkItemID, &gotClaimID, &gotWorkerID); err != nil {
		t.Fatalf("reading execution request %s: %v", requestID, err)
	}
	if gotWorkItemID != workItemID || gotClaimID != claimID || gotWorkerID != workerID {
		t.Fatalf("execution request %s authority = %s/%s/%s, want %s/%s/%s", requestID, gotWorkItemID, gotClaimID, gotWorkerID, workItemID, claimID, workerID)
	}
}
