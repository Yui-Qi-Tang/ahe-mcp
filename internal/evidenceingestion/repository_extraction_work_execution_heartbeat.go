package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repositoryExtractionWorkHeartbeat struct {
	executionContext context.Context
	cancelExecution  context.CancelCauseFunc
	cancelHeartbeat  context.CancelFunc
	done             chan struct{}
	stopOnce         sync.Once
	stateMu          sync.Mutex
	heartbeatCount   int
	heartbeatErr     error
}

func startRepositoryExtractionWorkHeartbeat(ctx context.Context, pool *pgxpool.Pool, request repositoryExtractionWorkExecutionRequest) (*repositoryExtractionWorkHeartbeat, error) {
	if pool == nil {
		return nil, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	leaseDuration := time.Duration(request.heartbeatLeaseMilliseconds) * time.Millisecond
	if leaseDuration < RepositoryExtractionWorkMinLeaseDuration || leaseDuration > RepositoryExtractionWorkMaxLeaseDuration {
		return nil, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s has invalid heartbeat lease duration", request.input.RequestID)
	}
	if request.leaseExpiresAt.IsZero() {
		return nil, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s has no current lease expiry", request.input.RequestID)
	}

	if repositoryExtractionWorkHeartbeatDelay(time.Now().UTC(), request.leaseExpiresAt, leaseDuration) == 0 {
		leaseExpiresAt, count, err := advanceRepositoryExtractionWorkExecutionHeartbeat(ctx, pool, request, request.leaseExpiresAt)
		if err != nil {
			return nil, err
		}
		request.leaseExpiresAt = leaseExpiresAt
		request.heartbeatCount = count
	}

	executionContext, cancelExecution := context.WithCancelCause(ctx)
	heartbeatContext, cancelHeartbeat := context.WithCancel(ctx)
	heartbeat := &repositoryExtractionWorkHeartbeat{
		executionContext: executionContext,
		cancelExecution:  cancelExecution,
		cancelHeartbeat:  cancelHeartbeat,
		done:             make(chan struct{}),
		heartbeatCount:   request.heartbeatCount,
	}
	go heartbeat.run(heartbeatContext, ctx, pool, request, leaseDuration)
	return heartbeat, nil
}

func (heartbeat *repositoryExtractionWorkHeartbeat) context() context.Context {
	return heartbeat.executionContext
}

func (heartbeat *repositoryExtractionWorkHeartbeat) stop() error {
	heartbeat.stopOnce.Do(heartbeat.cancelHeartbeat)
	<-heartbeat.done
	heartbeat.cancelExecution(context.Canceled)
	heartbeat.stateMu.Lock()
	defer heartbeat.stateMu.Unlock()
	return heartbeat.heartbeatErr
}

func (heartbeat *repositoryExtractionWorkHeartbeat) count() int {
	heartbeat.stateMu.Lock()
	defer heartbeat.stateMu.Unlock()
	return heartbeat.heartbeatCount
}

func (heartbeat *repositoryExtractionWorkHeartbeat) run(stopContext, renewalContext context.Context, pool *pgxpool.Pool, request repositoryExtractionWorkExecutionRequest, leaseDuration time.Duration) {
	defer close(heartbeat.done)
	leaseExpiresAt := request.leaseExpiresAt
	for {
		delay := repositoryExtractionWorkHeartbeatDelay(time.Now().UTC(), leaseExpiresAt, leaseDuration)
		timer := time.NewTimer(delay)
		select {
		case <-stopContext.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}

		advancedExpiry, count, err := advanceRepositoryExtractionWorkExecutionHeartbeat(renewalContext, pool, request, leaseExpiresAt)
		if err != nil {
			heartbeat.stateMu.Lock()
			heartbeat.heartbeatErr = err
			heartbeat.stateMu.Unlock()
			heartbeat.cancelExecution(err)
			return
		}
		request.heartbeatCount = count
		leaseExpiresAt = advancedExpiry
		heartbeat.stateMu.Lock()
		heartbeat.heartbeatCount = count
		heartbeat.stateMu.Unlock()
	}
}

func advanceRepositoryExtractionWorkExecutionHeartbeat(ctx context.Context, pool *pgxpool.Pool, request repositoryExtractionWorkExecutionRequest, knownLeaseExpiresAt time.Time) (time.Time, int, error) {
	renewal, count, err := renewRepositoryExtractionWorkExecutionHeartbeat(ctx, pool, request)
	if err == nil {
		return renewal.LeaseExpiresAt, count, nil
	}
	if kind, ok := KindOf(err); !ok || kind != ErrorRepositoryWorkConflict {
		return time.Time{}, 0, err
	}
	observedExpiry, observedCount, observeErr := inspectRepositoryExtractionWorkExecutionHeartbeatLease(ctx, pgxDB{pool: pool}, request, time.Now().UTC())
	if observeErr != nil || !observedExpiry.After(knownLeaseExpiresAt) {
		return time.Time{}, 0, err
	}
	return observedExpiry, observedCount, nil
}

func inspectRepositoryExtractionWorkExecutionHeartbeatLease(ctx context.Context, db sqlDB, request repositoryExtractionWorkExecutionRequest, observedAt time.Time) (time.Time, int, error) {
	if observedAt.IsZero() {
		return time.Time{}, 0, newDomainError(ErrorInvalidInput, "repository extraction work execution heartbeat observed_at is required")
	}
	observedAt = observedAt.UTC()
	var leaseExpiresAt time.Time
	var heartbeatCount int
	err := withTx(ctx, db, func(tx sqlTx) error {
		if err := lockRepositorySourceStream(ctx, tx, request.work.RepoID, request.work.ExtractorName); err != nil {
			return err
		}
		work, err := readRepositoryExtractionWorkByIDForUpdate(ctx, tx, request.input.WorkItemID)
		if err != nil {
			return err
		}
		if work.status != repositoryExtractionWorkRunning ||
			work.claimID != request.input.ClaimID ||
			work.claimedBy != request.input.WorkerID ||
			work.claimedAt == nil ||
			work.finishedAt != nil ||
			work.sourceGenerationID != "" ||
			work.failureClass != "" ||
			work.failureMessage != "" {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s no longer owns running claim %s", request.input.RequestID, request.input.ClaimID)
		}
		attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, request.input.ClaimID)
		if err != nil {
			return err
		}
		if attempt.workItemID != request.input.WorkItemID ||
			attempt.workerID != request.input.WorkerID ||
			attempt.status != repositoryExtractionWorkRunning ||
			attempt.finishedAt != nil ||
			attempt.expiredAt != nil ||
			attempt.sourceGenerationID != "" ||
			attempt.failureClass != "" ||
			attempt.failureMessage != "" ||
			attempt.leaseDurationMilliseconds != request.heartbeatLeaseMilliseconds ||
			!attempt.claimedAt.Equal(work.claimedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s no longer matches running claim attempt %s", request.input.RequestID, request.input.ClaimID)
		}
		var workItemID, claimID, workerID string
		var heartbeatLeaseMilliseconds int64
		if err := tx.queryRow(ctx, `
			SELECT
				work_item_id,
				claim_id,
				worker_id,
				heartbeat_lease_duration_milliseconds,
				heartbeat_count
			FROM repository_extraction_work_execution_requests
			WHERE request_id = $1
			FOR UPDATE
		`, request.input.RequestID).Scan(&workItemID, &claimID, &workerID, &heartbeatLeaseMilliseconds, &heartbeatCount); errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work execution request %s was not found", request.input.RequestID)
		} else if err != nil {
			return fmt.Errorf("locking repository extraction work execution request: %w", err)
		}
		if workItemID != request.input.WorkItemID ||
			claimID != request.input.ClaimID ||
			workerID != request.input.WorkerID ||
			heartbeatLeaseMilliseconds != request.heartbeatLeaseMilliseconds {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s has inconsistent heartbeat authority", request.input.RequestID)
		}
		if !observedAt.Before(attempt.leaseExpiresAt) {
			return newDomainError(ErrorRepositoryWorkLeaseExpired, "repository extraction work claim %s expired at %s", request.input.ClaimID, attempt.leaseExpiresAt.UTC().Format(time.RFC3339Nano))
		}
		leaseExpiresAt = attempt.leaseExpiresAt.UTC()
		return nil
	})
	if err != nil {
		return time.Time{}, 0, err
	}
	return leaseExpiresAt, heartbeatCount, nil
}

func repositoryExtractionWorkHeartbeatDelay(now, leaseExpiresAt time.Time, leaseDuration time.Duration) time.Duration {
	delay := leaseExpiresAt.Sub(now) - leaseDuration/2
	if delay < 0 {
		return 0
	}
	return delay
}

func renewRepositoryExtractionWorkExecutionHeartbeat(ctx context.Context, pool *pgxpool.Pool, request repositoryExtractionWorkExecutionRequest) (RepositoryExtractionWorkLeaseRenewalResult, int, error) {
	sequence := request.heartbeatCount + 1
	requestID, err := repositoryExtractionWorkExecutionHeartbeatRequestID(request.input.RequestID, sequence)
	if err != nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, 0, err
	}
	renewal, err := RenewRepositoryExtractionWorkLease(ctx, pool, RepositoryExtractionWorkLeaseRenewalInput{
		RequestID:                 requestID,
		WorkItemID:                request.input.WorkItemID,
		ClaimID:                   request.input.ClaimID,
		WorkerID:                  request.input.WorkerID,
		LeaseDurationMilliseconds: request.heartbeatLeaseMilliseconds,
	})
	if err != nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, 0, err
	}
	count, err := recordRepositoryExtractionWorkExecutionHeartbeat(ctx, pgxDB{pool: pool}, request.input.RequestID, sequence)
	if err != nil {
		return RepositoryExtractionWorkLeaseRenewalResult{}, 0, err
	}
	return renewal, count, nil
}

func repositoryExtractionWorkExecutionHeartbeatRequestID(executionRequestID string, sequence int) (string, error) {
	if sequence < 1 {
		return "", newDomainError(ErrorInvalidInput, "repository extraction work execution heartbeat sequence must be positive")
	}
	return stableID("work-exec-heartbeat:", "repository_extraction_work_execution_heartbeat", struct {
		ExecutionRequestID string `json:"execution_request_id"`
		Sequence           int    `json:"sequence"`
	}{
		ExecutionRequestID: executionRequestID,
		Sequence:           sequence,
	})
}

func recordRepositoryExtractionWorkExecutionHeartbeat(ctx context.Context, db sqlDB, executionRequestID string, sequence int) (int, error) {
	if sequence < 1 {
		return 0, newDomainError(ErrorInvalidInput, "repository extraction work execution heartbeat sequence must be positive")
	}
	var count int
	err := withTx(ctx, db, func(tx sqlTx) error {
		if err := tx.queryRow(ctx, `
			SELECT heartbeat_count
			FROM repository_extraction_work_execution_requests
			WHERE request_id = $1
			FOR UPDATE
		`, executionRequestID).Scan(&count); errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work execution request %s was not found", executionRequestID)
		} else if err != nil {
			return fmt.Errorf("locking repository extraction work execution heartbeat: %w", err)
		}
		if count >= sequence {
			return nil
		}
		if count != sequence-1 {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s heartbeat count is %d, cannot record sequence %d", executionRequestID, count, sequence)
		}
		if _, err := tx.exec(ctx, `
			UPDATE repository_extraction_work_execution_requests
			SET heartbeat_count = $2
			WHERE request_id = $1
		`, executionRequestID, sequence); err != nil {
			return fmt.Errorf("recording repository extraction work execution heartbeat: %w", err)
		}
		count = sequence
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
