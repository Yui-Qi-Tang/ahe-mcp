package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RepositoryExtractionWorkExecutionInput identifies one exact claimed work item to execute.
type RepositoryExtractionWorkExecutionInput struct {
	RequestID     string `json:"request_id"`
	WorkItemID    string `json:"work_item_id"`
	ClaimID       string `json:"claim_id"`
	WorkerID      string `json:"worker_id"`
	WorkspaceRoot string `json:"workspace_root"`
}

// RepositoryExtractionWorkExecutionResult reports deterministic extraction and successful finish.
type RepositoryExtractionWorkExecutionResult struct {
	RequestID      string                               `json:"request_id"`
	Work           RepositoryExtractionWork             `json:"work"`
	ClaimID        string                               `json:"claim_id"`
	WorkerID       string                               `json:"worker_id"`
	HeartbeatCount int                                  `json:"heartbeat_count"`
	Extraction     RepositoryIngestResult               `json:"extraction"`
	Finish         RepositoryExtractionWorkFinishResult `json:"finish"`
	Replayed       bool                                 `json:"replayed"`
}

type repositoryExtractionWorkExecutionRequest struct {
	input                       RepositoryExtractionWorkExecutionInput
	repositorySnapshotRequestID string
	extractorRequestID          string
	finishRequestID             string
	requestPayloadHash          string
	work                        RepositoryExtractionWork
	workRecord                  repositoryExtractionWorkRecord
	heartbeatLeaseMilliseconds  int64
	heartbeatCount              int
	leaseExpiresAt              time.Time
	requiresHeartbeat           bool
	failureClass                string
	failureMessage              string
	failedAt                    *time.Time
}

const (
	repositoryExtractionExecutionReplayInitialWait = 10 * time.Millisecond
	repositoryExtractionExecutionReplayMaxWait     = 250 * time.Millisecond
)

// ExecuteClaimedRepositoryExtractionWork runs one allowlisted extractor and finishes only success.
func ExecuteClaimedRepositoryExtractionWork(
	ctx context.Context,
	pool *pgxpool.Pool,
	input RepositoryExtractionWorkExecutionInput,
) (_ RepositoryExtractionWorkExecutionResult, returnErr error) {
	if pool == nil {
		return RepositoryExtractionWorkExecutionResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := prepareRepositoryExtractionWorkExecution(input)
	if err != nil {
		return RepositoryExtractionWorkExecutionResult{}, err
	}
	request, replayed, err := reserveRepositoryExtractionWorkExecution(ctx, pgxDB{pool: pool}, request, time.Now().UTC())
	if err != nil {
		return RepositoryExtractionWorkExecutionResult{}, err
	}
	if !replayed {
		defer func() {
			if returnErr == nil {
				return
			}
			failureContext, cancel := detachedFailureContext(ctx)
			defer cancel()
			if err := persistRepositoryExtractionWorkExecutionFailure(
				failureContext,
				pgxDB{pool: pool},
				request,
				returnErr,
				time.Now().UTC(),
			); err != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("persisting repository extraction work execution failure audit: %w", err),
				)
			}
		}()
	}
	if replayed && request.requiresHeartbeat {
		request, err = waitForRepositoryExtractionWorkExecutionReplay(ctx, pgxDB{pool: pool}, request)
		if err != nil {
			return RepositoryExtractionWorkExecutionResult{}, err
		}
	}
	if replayed && request.failureClass != "" {
		replayDirectly, err := repositoryExtractionWorkExecutionFailureReplaysDirectly(
			ctx,
			pgxDB{pool: pool},
			request,
		)
		if err != nil {
			return RepositoryExtractionWorkExecutionResult{}, err
		}
		if replayDirectly {
			return RepositoryExtractionWorkExecutionResult{}, replayRepositoryExtractionWorkExecutionFailure(request)
		}
	}
	executionContext := ctx
	var heartbeat *repositoryExtractionWorkHeartbeat
	if request.requiresHeartbeat {
		heartbeat, err = startRepositoryExtractionWorkHeartbeat(ctx, pool, request)
		if err != nil {
			return RepositoryExtractionWorkExecutionResult{}, err
		}
		executionContext = heartbeat.context()
		defer heartbeat.stop()
	}
	stopHeartbeat := func(operationErr error) error {
		if heartbeat == nil {
			return operationErr
		}
		heartbeatErr := heartbeat.stop()
		request.heartbeatCount = heartbeat.count()
		if heartbeatErr != nil {
			return heartbeatErr
		}
		return operationErr
	}

	snapshot, err := CaptureGitRepositorySnapshot(executionContext, pool, GitRepositorySnapshotConfig{
		WorkspaceRoot: request.input.WorkspaceRoot,
		RepoID:        request.work.RepoID,
		CommitSHA:     request.work.HeadCommitSHA,
		RequestID:     request.repositorySnapshotRequestID,
	})
	if err != nil {
		return RepositoryExtractionWorkExecutionResult{}, stopHeartbeat(err)
	}

	var extraction RepositoryIngestResult
	switch request.work.ExtractorName {
	case ExtractorRepositoryGoParserCodeFact:
		extraction, err = RunRepositoryGoParserDeltaExtractor(executionContext, pool, RepositoryGoParserRequest{
			RequestID:            request.extractorRequestID,
			RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
		})
	case ExtractorRepositoryGoplsCodeFact:
		extraction, err = RunRepositoryGoplsExtractor(executionContext, pool, RepositoryGoplsRequest{
			RequestID:            request.extractorRequestID,
			RepositorySnapshotID: snapshot.RepositorySnapshot.ID,
			WorkspaceRoot:        request.input.WorkspaceRoot,
		})
	default:
		return RepositoryExtractionWorkExecutionResult{}, stopHeartbeat(newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s has unsupported extractor %q", request.work.WorkItemID, request.work.ExtractorName))
	}
	if err != nil {
		return RepositoryExtractionWorkExecutionResult{}, stopHeartbeat(err)
	}
	if err := stopHeartbeat(nil); err != nil {
		return RepositoryExtractionWorkExecutionResult{}, err
	}

	finish, err := FinishRepositoryExtractionWork(ctx, pool, RepositoryExtractionWorkFinishInput{
		RequestID:          request.finishRequestID,
		WorkItemID:         request.input.WorkItemID,
		ClaimID:            request.input.ClaimID,
		WorkerID:           request.input.WorkerID,
		Outcome:            RepositoryExtractionWorkOutcomeSucceeded,
		SourceGenerationID: extraction.SourceGeneration.ID,
	})
	if err != nil {
		return RepositoryExtractionWorkExecutionResult{}, err
	}
	return RepositoryExtractionWorkExecutionResult{
		RequestID:      request.input.RequestID,
		Work:           request.work,
		ClaimID:        request.input.ClaimID,
		WorkerID:       request.input.WorkerID,
		HeartbeatCount: request.heartbeatCount,
		Extraction:     extraction,
		Finish:         finish,
		Replayed:       replayed,
	}, nil
}

func waitForRepositoryExtractionWorkExecutionReplay(
	ctx context.Context,
	db sqlDB,
	request repositoryExtractionWorkExecutionRequest,
) (repositoryExtractionWorkExecutionRequest, error) {
	wait := repositoryExtractionExecutionReplayInitialWait
	for request.requiresHeartbeat {
		attemptStatus, attemptFound, err := repositoryExtractionWorkExecutionAttemptStatus(
			ctx,
			db,
			request.extractorRequestID,
		)
		if err != nil {
			return repositoryExtractionWorkExecutionRequest{}, err
		}
		if attemptFound && attemptStatus != attemptStatusStarted {
			return request, nil
		}
		if request.failureClass != "" {
			return request, nil
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return repositoryExtractionWorkExecutionRequest{}, ctx.Err()
		case <-timer.C:
		}

		refreshed, replayed, err := reserveRepositoryExtractionWorkExecution(
			ctx,
			db,
			request,
			time.Now().UTC(),
		)
		if err != nil {
			return repositoryExtractionWorkExecutionRequest{}, err
		}
		if !replayed {
			return repositoryExtractionWorkExecutionRequest{}, newDomainError(
				ErrorRepositoryWorkConflict,
				"repository extraction work execution request %s lost its replay identity",
				request.input.RequestID,
			)
		}
		request = refreshed
		wait = min(wait*2, repositoryExtractionExecutionReplayMaxWait)
	}
	return request, nil
}

func repositoryExtractionWorkExecutionAttemptStatus(
	ctx context.Context,
	db sqlQueryer,
	extractorRequestID string,
) (string, bool, error) {
	var status string
	err := db.queryRow(ctx, `
		SELECT attempt.status
		FROM repository_extraction_run_requests AS request
		JOIN extraction_attempts AS attempt
		  ON attempt.extraction_run_id = request.extraction_run_id
		WHERE request.request_id = $1
		ORDER BY attempt.attempt_number DESC
		LIMIT 1
	`, extractorRequestID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading replayed repository extraction attempt: %w", err)
	}
	switch status {
	case attemptStatusStarted, attemptStatusSucceeded, attemptStatusFailed:
		return status, true, nil
	default:
		return "", false, newDomainError(
			ErrorRepositoryWorkConflict,
			"repository extraction request %s has unsupported attempt status %s",
			extractorRequestID,
			status,
		)
	}
}

func repositoryExtractionWorkExecutionFailureReplaysDirectly(
	ctx context.Context,
	db sqlQueryer,
	request repositoryExtractionWorkExecutionRequest,
) (bool, error) {
	status, found, err := repositoryExtractionWorkExecutionAttemptStatus(
		ctx,
		db,
		request.extractorRequestID,
	)
	if err != nil {
		return false, err
	}
	return !found || status != attemptStatusFailed, nil
}

func replayRepositoryExtractionWorkExecutionFailure(request repositoryExtractionWorkExecutionRequest) error {
	return &DomainError{
		Kind:    ErrorKind(request.failureClass),
		Message: request.failureMessage,
	}
}

func prepareRepositoryExtractionWorkExecution(input RepositoryExtractionWorkExecutionInput) (repositoryExtractionWorkExecutionRequest, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.WorkItemID = strings.TrimSpace(input.WorkItemID)
	input.ClaimID = strings.TrimSpace(input.ClaimID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.RequestID == "" {
		return repositoryExtractionWorkExecutionRequest{}, newDomainError(ErrorInvalidInput, "repository extraction work execution request_id is required")
	}
	if !strings.HasPrefix(input.WorkItemID, "repo-work:") {
		return repositoryExtractionWorkExecutionRequest{}, newDomainError(ErrorInvalidRecordID, "work_item_id %q must start with repo-work:", input.WorkItemID)
	}
	if !strings.HasPrefix(input.ClaimID, "work-claim:") {
		return repositoryExtractionWorkExecutionRequest{}, newDomainError(ErrorInvalidRecordID, "claim_id %q must start with work-claim:", input.ClaimID)
	}
	if input.WorkerID == "" || len(input.WorkerID) > 200 {
		return repositoryExtractionWorkExecutionRequest{}, newDomainError(ErrorInvalidInput, "repository extraction work worker_id must contain 1 to 200 bytes")
	}
	workspaceRoot, err := canonicalDirectory(input.WorkspaceRoot)
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, err
	}
	input.WorkspaceRoot = workspaceRoot

	repositorySnapshotRequestID, err := repositoryExtractionWorkExecutionChildRequestID("work-exec-snapshot:", "repository_extraction_work_execution_snapshot", input.RequestID)
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, err
	}
	extractorRequestID, err := repositoryExtractionWorkExecutionChildRequestID("work-exec-extractor:", "repository_extraction_work_execution_extractor", input.RequestID)
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, err
	}
	finishRequestID, err := repositoryExtractionWorkExecutionChildRequestID("work-exec-finish:", "repository_extraction_work_execution_finish", input.RequestID)
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, err
	}
	payloadHash, err := repositoryExtractionWorkExecutionPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, err
	}
	return repositoryExtractionWorkExecutionRequest{
		input:                       input,
		repositorySnapshotRequestID: repositorySnapshotRequestID,
		extractorRequestID:          extractorRequestID,
		finishRequestID:             finishRequestID,
		requestPayloadHash:          payloadHash,
	}, nil
}

func repositoryExtractionWorkExecutionChildRequestID(prefix, kind, requestID string) (string, error) {
	return stableID(prefix, kind, struct {
		ExecutionRequestID string `json:"execution_request_id"`
	}{ExecutionRequestID: requestID})
}

func repositoryExtractionWorkExecutionPayloadHash(input RepositoryExtractionWorkExecutionInput) (string, error) {
	payload, err := deterministicJSON(struct {
		WorkItemID    string `json:"work_item_id"`
		ClaimID       string `json:"claim_id"`
		WorkerID      string `json:"worker_id"`
		WorkspaceRoot string `json:"workspace_root"`
	}{
		WorkItemID:    input.WorkItemID,
		ClaimID:       input.ClaimID,
		WorkerID:      input.WorkerID,
		WorkspaceRoot: input.WorkspaceRoot,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func reserveRepositoryExtractionWorkExecution(ctx context.Context, db sqlDB, request repositoryExtractionWorkExecutionRequest, startedAt time.Time) (repositoryExtractionWorkExecutionRequest, bool, error) {
	if startedAt.IsZero() {
		return repositoryExtractionWorkExecutionRequest{}, false, newDomainError(ErrorInvalidInput, "repository extraction work execution started_at is required")
	}
	startedAt = startedAt.UTC()
	var result repositoryExtractionWorkExecutionRequest
	var replayed bool
	err := withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-execute", request.input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkExecutionRequest(ctx, tx, request.input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != request.requestPayloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work execution payload", request.input.RequestID)
			}
			if persisted.input.WorkItemID != request.input.WorkItemID ||
				persisted.input.ClaimID != request.input.ClaimID ||
				persisted.input.WorkerID != request.input.WorkerID ||
				persisted.repositorySnapshotRequestID != request.repositorySnapshotRequestID ||
				persisted.extractorRequestID != request.extractorRequestID ||
				persisted.finishRequestID != request.finishRequestID {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s has inconsistent durable identity", request.input.RequestID)
			}
			persisted.input.WorkspaceRoot = request.input.WorkspaceRoot
			if err := lockRepositorySourceStream(ctx, tx, persisted.work.RepoID, persisted.work.ExtractorName); err != nil {
				return err
			}
			work, err := readRepositoryExtractionWorkByIDForUpdate(ctx, tx, persisted.input.WorkItemID)
			if err != nil {
				return err
			}
			persisted.work = work.work
			persisted.workRecord = work
			if err := bindReplayedRepositoryExtractionWorkExecution(ctx, tx, &persisted, startedAt); err != nil {
				return err
			}
			result = persisted
			replayed = true
			return nil
		}

		work, err := readRepositoryExtractionWorkByID(ctx, tx, request.input.WorkItemID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work %s was not found", request.input.WorkItemID)
		}
		if err != nil {
			return err
		}
		if err := lockRepositorySourceStream(ctx, tx, work.work.RepoID, work.work.ExtractorName); err != nil {
			return err
		}
		work, err = readRepositoryExtractionWorkByIDForUpdate(ctx, tx, request.input.WorkItemID)
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
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s is not running claim %s for worker %s", request.input.WorkItemID, request.input.ClaimID, request.input.WorkerID)
		}
		if startedAt.Before(work.claimedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work %s execution time precedes its claim", request.input.WorkItemID)
		}

		attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, request.input.ClaimID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", request.input.ClaimID)
		}
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
			!attempt.claimedAt.Equal(work.claimedAt.UTC()) {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s does not match running work", request.input.ClaimID)
		}
		if !startedAt.Before(attempt.leaseExpiresAt) {
			return newDomainError(ErrorRepositoryWorkLeaseExpired, "repository extraction work claim %s expired at %s", request.input.ClaimID, attempt.leaseExpiresAt.UTC().Format(time.RFC3339Nano))
		}
		ownerRequestID, owned, err := readRepositoryExtractionWorkExecutionOwner(ctx, tx, request.input.ClaimID)
		if err != nil {
			return err
		}
		if owned {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim %s is already owned by execution request %s", request.input.ClaimID, ownerRequestID)
		}

		request.work = work.work
		request.workRecord = work
		request.heartbeatLeaseMilliseconds = attempt.leaseDurationMilliseconds
		request.leaseExpiresAt = attempt.leaseExpiresAt.UTC()
		request.requiresHeartbeat = true
		if err := persistRepositoryExtractionWorkExecutionRequest(ctx, tx, request); err != nil {
			return err
		}
		result = request
		return nil
	})
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, false, err
	}
	return result, replayed, nil
}

func bindReplayedRepositoryExtractionWorkExecution(ctx context.Context, tx sqlTx, request *repositoryExtractionWorkExecutionRequest, startedAt time.Time) error {
	work := request.workRecord
	if work.claimID != request.input.ClaimID || work.claimedBy != request.input.WorkerID || work.claimedAt == nil {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s no longer owns claim %s", request.input.RequestID, request.input.ClaimID)
	}
	attempt, err := readRepositoryExtractionWorkClaimAttemptForUpdate(ctx, tx, request.input.ClaimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return newDomainError(ErrorMissingSourceViewAttempt, "repository extraction work claim attempt %s was not found", request.input.ClaimID)
	}
	if err != nil {
		return err
	}
	if attempt.workItemID != request.input.WorkItemID ||
		attempt.workerID != request.input.WorkerID ||
		attempt.leaseDurationMilliseconds != request.heartbeatLeaseMilliseconds ||
		!attempt.claimedAt.Equal(work.claimedAt.UTC()) {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s no longer matches claim attempt %s", request.input.RequestID, request.input.ClaimID)
	}

	switch work.status {
	case repositoryExtractionWorkRunning:
		if attempt.status != repositoryExtractionWorkRunning ||
			attempt.finishedAt != nil ||
			attempt.expiredAt != nil ||
			attempt.sourceGenerationID != "" ||
			attempt.failureClass != "" ||
			attempt.failureMessage != "" ||
			work.finishedAt != nil ||
			work.sourceGenerationID != "" ||
			work.failureClass != "" ||
			work.failureMessage != "" {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s has inconsistent running claim %s", request.input.RequestID, request.input.ClaimID)
		}
		if !startedAt.Before(attempt.leaseExpiresAt) {
			return newDomainError(ErrorRepositoryWorkLeaseExpired, "repository extraction work claim %s expired at %s", request.input.ClaimID, attempt.leaseExpiresAt.UTC().Format(time.RFC3339Nano))
		}
		request.leaseExpiresAt = attempt.leaseExpiresAt.UTC()
		request.requiresHeartbeat = true
		return nil
	case RepositoryExtractionWorkOutcomeSucceeded:
		if attempt.status != RepositoryExtractionWorkOutcomeSucceeded ||
			attempt.finishedAt == nil ||
			attempt.expiredAt != nil ||
			attempt.sourceGenerationID == "" ||
			attempt.sourceGenerationID != work.sourceGenerationID ||
			work.finishedAt == nil ||
			work.sourceGenerationID == "" ||
			attempt.failureClass != "" ||
			attempt.failureMessage != "" ||
			work.failureClass != "" ||
			work.failureMessage != "" {
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s has inconsistent successful claim %s", request.input.RequestID, request.input.ClaimID)
		}
		request.requiresHeartbeat = false
		return nil
	default:
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work execution request %s is not bound to a running claim or successful terminal replay", request.input.RequestID)
	}
}

func persistRepositoryExtractionWorkExecutionRequest(ctx context.Context, tx sqlTx, request repositoryExtractionWorkExecutionRequest) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_execution_requests (
			request_id,
			work_item_id,
			claim_id,
			worker_id,
			repository_snapshot_request_id,
			extractor_request_id,
			finish_request_id,
			heartbeat_lease_duration_milliseconds,
			heartbeat_count,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, request.input.RequestID, request.input.WorkItemID, request.input.ClaimID, request.input.WorkerID, request.repositorySnapshotRequestID, request.extractorRequestID, request.finishRequestID, request.heartbeatLeaseMilliseconds, request.heartbeatCount, request.requestPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work execution request: %w", err)
	}
	return nil
}

func persistRepositoryExtractionWorkExecutionFailure(
	ctx context.Context,
	db sqlDB,
	request repositoryExtractionWorkExecutionRequest,
	cause error,
	failedAt time.Time,
) error {
	if cause == nil {
		return newDomainError(ErrorInvalidInput, "repository extraction work execution failure cause is required")
	}
	if failedAt.IsZero() {
		return newDomainError(ErrorInvalidInput, "repository extraction work execution failed_at is required")
	}
	failureClass, failureMessage := repositoryExtractionWorkExecutionFailureMaterial(cause)
	return withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-execute", request.input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkExecutionRequest(ctx, tx, request.input.RequestID)
		if err != nil {
			return err
		}
		if !ok {
			return newDomainError(
				ErrorMissingSourceViewAttempt,
				"repository extraction work execution request %s was not found",
				request.input.RequestID,
			)
		}
		if persisted.requestPayloadHash != request.requestPayloadHash ||
			persisted.input.WorkItemID != request.input.WorkItemID ||
			persisted.input.ClaimID != request.input.ClaimID ||
			persisted.input.WorkerID != request.input.WorkerID {
			return newDomainError(
				ErrorRepositoryWorkConflict,
				"repository extraction work execution request %s has inconsistent failure audit identity",
				request.input.RequestID,
			)
		}
		if persisted.failureClass != "" {
			if persisted.failureClass == failureClass && persisted.failureMessage == failureMessage {
				return nil
			}
			return newDomainError(
				ErrorRepositoryWorkConflict,
				"repository extraction work execution request %s already has a different failure audit",
				request.input.RequestID,
			)
		}
		if persisted.workRecord.status != repositoryExtractionWorkRunning ||
			persisted.workRecord.claimID != request.input.ClaimID ||
			persisted.workRecord.claimedBy != request.input.WorkerID {
			return nil
		}
		tag, err := tx.exec(ctx, `
			UPDATE repository_extraction_work_execution_requests AS execution
			SET failure_class = $2,
				failure_message = $3,
				failed_at = $4
			WHERE execution.request_id = $1
			  AND execution.failure_class IS NULL
			  AND EXISTS (
				SELECT 1
				FROM repository_extraction_work_items AS work
				WHERE work.work_item_id = execution.work_item_id
				  AND work.status = $5
				  AND work.claim_id = execution.claim_id
				  AND work.claimed_by = execution.worker_id
			  )
		`, request.input.RequestID, failureClass, failureMessage, failedAt.UTC(), repositoryExtractionWorkRunning)
		if err != nil {
			return fmt.Errorf("updating repository extraction work execution failure audit: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		refreshed, ok, err := readRepositoryExtractionWorkExecutionRequest(ctx, tx, request.input.RequestID)
		if err != nil {
			return err
		}
		if ok && refreshed.failureClass == failureClass && refreshed.failureMessage == failureMessage {
			return nil
		}
		if ok && refreshed.workRecord.status != repositoryExtractionWorkRunning {
			return nil
		}
		return newDomainError(
			ErrorRepositoryWorkConflict,
			"repository extraction work execution request %s changed during failure audit",
			request.input.RequestID,
		)
	})
}

func repositoryExtractionWorkExecutionFailureMaterial(cause error) (string, string) {
	failureClass := string(ErrorRepositoryWorkExecutionFailed)
	failureMessage := cause.Error()
	var domainErr *DomainError
	if errors.As(cause, &domainErr) {
		failureClass = string(domainErr.Kind)
		failureMessage = domainErr.Message
	}
	failureClass = boundedRepositoryExtractionWorkExecutionFailureText(
		failureClass,
		repositoryExtractionWorkMaxFailureClassBytes,
		string(ErrorRepositoryWorkExecutionFailed),
	)
	failureMessage = boundedRepositoryExtractionWorkExecutionFailureText(
		failureMessage,
		repositoryExtractionWorkMaxFailureMessageBytes,
		failureClass,
	)
	return failureClass, failureMessage
}

func boundedRepositoryExtractionWorkExecutionFailureText(value string, limit int, fallback string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "\uFFFD"))
	if value == "" {
		value = fallback
	}
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

func readRepositoryExtractionWorkExecutionRequest(ctx context.Context, tx sqlTx, requestID string) (repositoryExtractionWorkExecutionRequest, bool, error) {
	var request repositoryExtractionWorkExecutionRequest
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			work_item_id,
			claim_id,
			worker_id,
			repository_snapshot_request_id,
			extractor_request_id,
			finish_request_id,
			heartbeat_lease_duration_milliseconds,
			heartbeat_count,
			request_payload_hash,
			COALESCE(failure_class, ''),
			COALESCE(failure_message, ''),
			failed_at
		FROM repository_extraction_work_execution_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&request.input.RequestID,
		&request.input.WorkItemID,
		&request.input.ClaimID,
		&request.input.WorkerID,
		&request.repositorySnapshotRequestID,
		&request.extractorRequestID,
		&request.finishRequestID,
		&request.heartbeatLeaseMilliseconds,
		&request.heartbeatCount,
		&request.requestPayloadHash,
		&request.failureClass,
		&request.failureMessage,
		&request.failedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return repositoryExtractionWorkExecutionRequest{}, false, nil
	}
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, false, fmt.Errorf("reading repository extraction work execution request: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, request.input.WorkItemID)
	if err != nil {
		return repositoryExtractionWorkExecutionRequest{}, false, err
	}
	request.work = work.work
	request.workRecord = work
	if request.failedAt != nil {
		failedAt := request.failedAt.UTC()
		request.failedAt = &failedAt
	}
	return request, true, nil
}

func readRepositoryExtractionWorkExecutionOwner(ctx context.Context, tx sqlTx, claimID string) (string, bool, error) {
	var requestID string
	err := tx.queryRow(ctx, `
		SELECT request_id
		FROM repository_extraction_work_execution_requests
		WHERE claim_id = $1
	`, claimID).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading repository extraction work execution owner: %w", err)
	}
	return requestID, true, nil
}
