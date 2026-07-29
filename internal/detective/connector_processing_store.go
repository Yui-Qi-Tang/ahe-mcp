package detective

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type connectorProcessingWorkRecord struct {
	connectorDeliveryID   string
	connectorID           string
	externalDeliveryID    string
	status                string
	attemptCount          int
	claimID               string
	sourceIntakeRequestID string
	sourceSnapshotID      string
	extractionViewID      string
	rawContentHash        string
	completedAt           *time.Time
}

type connectorProcessingAttemptRecord struct {
	claimID               string
	connectorDeliveryID   string
	connectorID           string
	externalDeliveryID    string
	attemptNumber         int
	workerID              string
	status                string
	claimedAt             time.Time
	leaseDurationMillis   int64
	leaseExpiresAt        time.Time
	finishedAt            *time.Time
	sourceIntakeRequestID string
	sourceSnapshotID      string
	extractionViewID      string
	rawContentHash        string
	failureClass          string
	failureMessage        string
}

type persistedConnectorProcessingRequest struct {
	result               ConnectorTextProcessingResult
	requestedWorkerID    string
	requestedLeaseMillis int64
	payloadHash          string
	failureClass         string
	failureMessage       string
}

type persistedConnectorRecoveryRequest struct {
	result      ConnectorProcessingRecoveryResult
	connectorID string
	externalID  string
	payloadHash string
}

func claimConnectorTextProcessing(
	ctx context.Context,
	pool *pgxpool.Pool,
	request preparedConnectorProcessingRequest,
	claimedAt time.Time,
) (connectorProcessingClaim, error) {
	if claimedAt.IsZero() {
		return connectorProcessingClaim{}, newDomainError(ErrorInvalidInput, "claimed_at is required")
	}
	claimedAt = claimedAt.UTC()
	leaseExpiresAt := claimedAt.Add(request.lease)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return connectorProcessingClaim{}, fmt.Errorf("beginning connector text processing claim: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockConnectorProcessingRequest(ctx, tx, request.input.RequestID); err != nil {
		return connectorProcessingClaim{}, err
	}
	peek, requestFound, err := readConnectorProcessingRequest(ctx, tx, request.input.RequestID, false)
	if err != nil {
		return connectorProcessingClaim{}, err
	}
	if requestFound && (peek.payloadHash != request.payloadHash ||
		peek.result.ConnectorDeliveryID != request.input.ConnectorDeliveryID ||
		peek.requestedWorkerID != request.input.WorkerID ||
		peek.requestedLeaseMillis != request.input.LeaseDurationMilliseconds) {
		return connectorProcessingClaim{}, newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists with different connector processing payload",
			request.input.RequestID,
		)
	}

	delivery, found, err := readConnectorDelivery(ctx, tx, request.input.ConnectorDeliveryID, true)
	if err != nil {
		return connectorProcessingClaim{}, err
	}
	if !found {
		return connectorProcessingClaim{}, newDomainError(
			ErrorConnectorDeliveryNotFound,
			"connector delivery %s was not found",
			request.input.ConnectorDeliveryID,
		)
	}
	if delivery.receipt.ContentType != ConnectorDeliveryContentTypeText {
		return connectorProcessingClaim{}, newDomainError(
			ErrorUnsupportedCapability,
			"connector text adapter does not support content_type %q",
			delivery.receipt.ContentType,
		)
	}
	if err := ensureConnectorTextProcessingWork(ctx, tx, delivery); err != nil {
		return connectorProcessingClaim{}, err
	}
	work, err := readConnectorProcessingWork(ctx, tx, request.input.ConnectorDeliveryID, true)
	if err != nil {
		return connectorProcessingClaim{}, err
	}

	if requestFound {
		attempt, err := readConnectorProcessingAttempt(ctx, tx, peek.result.ClaimID, true)
		if err != nil {
			return connectorProcessingClaim{}, err
		}
		persisted, found, err := readConnectorProcessingRequest(ctx, tx, request.input.RequestID, true)
		if err != nil {
			return connectorProcessingClaim{}, err
		}
		if !found {
			return connectorProcessingClaim{}, newDomainError(
				ErrorConnectorProcessingConflict,
				"connector processing request %s disappeared during replay",
				request.input.RequestID,
			)
		}
		if err := validateConnectorProcessingRequest(request, persisted, work, attempt); err != nil {
			return connectorProcessingClaim{}, err
		}
		persisted.result.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return connectorProcessingClaim{}, fmt.Errorf("committing connector processing replay: %w", err)
		}
		switch persisted.result.Status {
		case ConnectorProcessingStatusRunning:
			return connectorProcessingClaim{request: request, result: persisted.result, delivery: delivery, execute: true}, nil
		case ConnectorProcessingStatusSucceeded:
			return connectorProcessingClaim{request: request, result: persisted.result, delivery: delivery}, nil
		case ConnectorProcessingStatusFailed, ConnectorProcessingStatusExpired:
			return connectorProcessingClaim{}, connectorProcessingRequestTerminalError(
				persisted.result.Status,
				persisted.failureClass,
				persisted.failureMessage,
			)
		default:
			return connectorProcessingClaim{}, newDomainError(
				ErrorConnectorProcessingConflict,
				"connector processing request %s has unsupported status %s",
				request.input.RequestID,
				persisted.result.Status,
			)
		}
	}

	switch work.status {
	case ConnectorProcessingStatusSucceeded:
		attempt, err := readConnectorProcessingAttempt(ctx, tx, work.claimID, true)
		if err != nil {
			return connectorProcessingClaim{}, err
		}
		result, err := connectorProcessingResult(request.input.RequestID, work, attempt)
		if err != nil {
			return connectorProcessingClaim{}, err
		}
		result.Reused = true
		if err := insertConnectorProcessingRequest(ctx, tx, request, result, false, "", ""); err != nil {
			return connectorProcessingClaim{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return connectorProcessingClaim{}, fmt.Errorf("committing reused connector processing result: %w", err)
		}
		return connectorProcessingClaim{request: request, result: result, delivery: delivery}, nil
	case ConnectorProcessingStatusRunning:
		return connectorProcessingClaim{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector delivery %s is already claimed by %s",
			work.connectorDeliveryID,
			work.claimID,
		)
	case connectorProcessingStatusPending:
		// Continue below.
	default:
		return connectorProcessingClaim{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector delivery %s has unsupported processing status %s",
			work.connectorDeliveryID,
			work.status,
		)
	}
	if work.attemptCount >= maxConnectorProcessingAttempts {
		return connectorProcessingClaim{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector delivery %s exhausted bounded processing attempts",
			work.connectorDeliveryID,
		)
	}

	attempt := connectorProcessingAttemptRecord{
		claimID:             connectorProcessingClaimID(request),
		connectorDeliveryID: work.connectorDeliveryID,
		connectorID:         work.connectorID,
		externalDeliveryID:  work.externalDeliveryID,
		attemptNumber:       work.attemptCount + 1,
		workerID:            request.input.WorkerID,
		status:              ConnectorProcessingStatusRunning,
		claimedAt:           claimedAt,
		leaseDurationMillis: request.input.LeaseDurationMilliseconds,
		leaseExpiresAt:      leaseExpiresAt,
	}
	if err := insertConnectorProcessingAttempt(ctx, tx, attempt); err != nil {
		return connectorProcessingClaim{}, err
	}
	if err := markConnectorProcessingWorkRunning(ctx, tx, work, attempt); err != nil {
		return connectorProcessingClaim{}, err
	}
	work.status = ConnectorProcessingStatusRunning
	work.attemptCount = attempt.attemptNumber
	work.claimID = attempt.claimID
	result, err := connectorProcessingResult(request.input.RequestID, work, attempt)
	if err != nil {
		return connectorProcessingClaim{}, err
	}
	result.ClaimCreated = true
	if err := insertConnectorProcessingRequest(ctx, tx, request, result, true, "", ""); err != nil {
		return connectorProcessingClaim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return connectorProcessingClaim{}, fmt.Errorf("committing connector text processing claim: %w", err)
	}
	return connectorProcessingClaim{request: request, result: result, delivery: delivery, execute: true}, nil
}

func finishConnectorTextProcessing(
	ctx context.Context,
	pool *pgxpool.Pool,
	claim connectorProcessingClaim,
	intake evidenceingestion.SourceIntakeResult,
	finishedAt time.Time,
) (ConnectorTextProcessingResult, error) {
	if finishedAt.IsZero() {
		return ConnectorTextProcessingResult{}, newDomainError(ErrorInvalidInput, "finished_at is required")
	}
	finishedAt = finishedAt.UTC()
	if err := validateConnectorTextIntake(claim.delivery, intake); err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ConnectorTextProcessingResult{}, fmt.Errorf("beginning connector text processing finish: %w", err)
	}
	defer tx.Rollback(context.Background())
	if err := lockConnectorProcessingRequest(ctx, tx, claim.request.input.RequestID); err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	work, err := readConnectorProcessingWork(ctx, tx, claim.request.input.ConnectorDeliveryID, true)
	if err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	attempt, err := readConnectorProcessingAttempt(ctx, tx, claim.result.ClaimID, true)
	if err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	persisted, found, err := readConnectorProcessingRequest(ctx, tx, claim.request.input.RequestID, true)
	if err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	if !found {
		return ConnectorTextProcessingResult{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing request %s was not found during finish",
			claim.request.input.RequestID,
		)
	}
	if err := validateConnectorProcessingRequest(claim.request, persisted, work, attempt); err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	if persisted.result.Status == ConnectorProcessingStatusSucceeded {
		persisted.result.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return ConnectorTextProcessingResult{}, fmt.Errorf("committing connector processing finish replay: %w", err)
		}
		return persisted.result, nil
	}
	if persisted.result.Status == ConnectorProcessingStatusFailed || persisted.result.Status == ConnectorProcessingStatusExpired {
		return ConnectorTextProcessingResult{}, connectorProcessingRequestTerminalError(
			persisted.result.Status,
			persisted.failureClass,
			persisted.failureMessage,
		)
	}
	if finishedAt.After(attempt.leaseExpiresAt) {
		return ConnectorTextProcessingResult{}, newDomainError(
			ErrorConnectorProcessingLeaseExpired,
			"connector processing claim %s lease expired at %s",
			attempt.claimID,
			attempt.leaseExpiresAt.Format(time.RFC3339Nano),
		)
	}
	result := persisted.result
	result.Status = ConnectorProcessingStatusSucceeded
	result.FinishedAt = &finishedAt
	result.SourceIntakeRequestID = connectorTextSourceIntakeRequestID(claim.delivery.receipt.ConnectorDeliveryID)
	result.SourceSnapshotID = intake.SourceSnapshotID
	result.ExtractionViewID = intake.ExtractionViewID
	result.RawContentHash = intake.RawContentHash
	if err := markConnectorProcessingAttemptSucceeded(ctx, tx, attempt, result); err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	if err := markConnectorProcessingWorkSucceeded(ctx, tx, work, result); err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	if err := markConnectorProcessingRequestSucceeded(ctx, tx, claim.request.input.RequestID, result); err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectorTextProcessingResult{}, fmt.Errorf("committing connector text processing finish: %w", err)
	}
	return result, nil
}

func failConnectorTextProcessing(
	ctx context.Context,
	pool *pgxpool.Pool,
	claim connectorProcessingClaim,
	failureClass string,
	failureMessage string,
	finishedAt time.Time,
) error {
	if finishedAt.IsZero() {
		return newDomainError(ErrorInvalidInput, "finished_at is required")
	}
	finishedAt = finishedAt.UTC()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning connector text processing failure: %w", err)
	}
	defer tx.Rollback(context.Background())
	if err := lockConnectorProcessingRequest(ctx, tx, claim.request.input.RequestID); err != nil {
		return err
	}
	work, err := readConnectorProcessingWork(ctx, tx, claim.request.input.ConnectorDeliveryID, true)
	if err != nil {
		return err
	}
	attempt, err := readConnectorProcessingAttempt(ctx, tx, claim.result.ClaimID, true)
	if err != nil {
		return err
	}
	persisted, found, err := readConnectorProcessingRequest(ctx, tx, claim.request.input.RequestID, true)
	if err != nil {
		return err
	}
	if !found {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing request %s was not found during failure",
			claim.request.input.RequestID,
		)
	}
	if persisted.result.Status != ConnectorProcessingStatusRunning {
		return tx.Commit(ctx)
	}
	if err := validateConnectorProcessingRequest(claim.request, persisted, work, attempt); err != nil {
		return err
	}
	if finishedAt.After(attempt.leaseExpiresAt) {
		return newDomainError(
			ErrorConnectorProcessingLeaseExpired,
			"connector processing claim %s lease expired at %s",
			attempt.claimID,
			attempt.leaseExpiresAt.Format(time.RFC3339Nano),
		)
	}
	if err := markConnectorProcessingAttemptFailed(ctx, tx, attempt, failureClass, failureMessage, finishedAt); err != nil {
		return err
	}
	if err := requeueConnectorProcessingWork(ctx, tx, work, attempt.claimID); err != nil {
		return err
	}
	if err := markConnectorProcessingRequestFailed(ctx, tx, claim.request.input.RequestID, failureClass, failureMessage); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing connector text processing failure: %w", err)
	}
	return nil
}

func recoverConnectorTextProcessing(
	ctx context.Context,
	pool *pgxpool.Pool,
	request preparedConnectorRecoveryRequest,
	recoveredAt time.Time,
) (ConnectorProcessingRecoveryResult, error) {
	if recoveredAt.IsZero() {
		return ConnectorProcessingRecoveryResult{}, newDomainError(ErrorInvalidInput, "recovered_at is required")
	}
	recoveredAt = recoveredAt.UTC()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ConnectorProcessingRecoveryResult{}, fmt.Errorf("beginning connector processing recovery: %w", err)
	}
	defer tx.Rollback(context.Background())
	if err := lockConnectorRecoveryRequest(ctx, tx, request.input.RequestID); err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	persisted, found, err := readConnectorRecoveryRequest(ctx, tx, request.input.RequestID)
	if err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	if found {
		if persisted.payloadHash != request.payloadHash ||
			persisted.result.ConnectorDeliveryID != request.input.ConnectorDeliveryID ||
			persisted.result.ClaimID != request.input.ClaimID ||
			persisted.result.WorkerID != request.input.WorkerID {
			return ConnectorProcessingRecoveryResult{}, newDomainError(
				ErrorIdempotencyKeyReused,
				"request_id %s already exists with different connector recovery payload",
				request.input.RequestID,
			)
		}
		persisted.result.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return ConnectorProcessingRecoveryResult{}, fmt.Errorf("committing connector recovery replay: %w", err)
		}
		return persisted.result, nil
	}

	work, err := readConnectorProcessingWork(ctx, tx, request.input.ConnectorDeliveryID, true)
	if err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	attempt, err := readConnectorProcessingAttempt(ctx, tx, request.input.ClaimID, true)
	if err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	if work.status != ConnectorProcessingStatusRunning || work.claimID != attempt.claimID ||
		attempt.status != ConnectorProcessingStatusRunning || attempt.workerID != request.input.WorkerID ||
		attempt.connectorDeliveryID != request.input.ConnectorDeliveryID {
		return ConnectorProcessingRecoveryResult{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing recovery does not match the active claim",
		)
	}
	if recoveredAt.Before(attempt.leaseExpiresAt) {
		return ConnectorProcessingRecoveryResult{}, newDomainError(
			ErrorConnectorProcessingLeaseActive,
			"connector processing claim %s remains leased until %s",
			attempt.claimID,
			attempt.leaseExpiresAt.Format(time.RFC3339Nano),
		)
	}
	failureClass := "lease_expired"
	failureMessage := "connector processing claim lease elapsed before terminal finish"
	if err := markConnectorProcessingAttemptExpired(ctx, tx, attempt, failureClass, failureMessage, recoveredAt); err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	if err := requeueConnectorProcessingWork(ctx, tx, work, attempt.claimID); err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	if err := markConnectorProcessingClaimRequestsExpired(ctx, tx, attempt.claimID, failureClass, failureMessage); err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	result := ConnectorProcessingRecoveryResult{
		RequestID:           request.input.RequestID,
		ConnectorDeliveryID: request.input.ConnectorDeliveryID,
		ClaimID:             request.input.ClaimID,
		WorkerID:            request.input.WorkerID,
		AttemptNumber:       attempt.attemptNumber,
		RecoveredAt:         recoveredAt,
	}
	if err := insertConnectorRecoveryRequest(ctx, tx, request, attempt, result); err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectorProcessingRecoveryResult{}, fmt.Errorf("committing connector processing recovery: %w", err)
	}
	return result, nil
}

func ensureConnectorTextProcessingWork(
	ctx context.Context,
	tx pgx.Tx,
	delivery persistedConnectorDelivery,
) error {
	if delivery.receipt.ContentType != ConnectorDeliveryContentTypeText {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_work (
			connector_delivery_id, connector_id, external_delivery_id,
			adapter_name, adapter_version, status
		)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (connector_delivery_id) DO NOTHING
	`, delivery.receipt.ConnectorDeliveryID, delivery.receipt.ConnectorID,
		delivery.receipt.ExternalDeliveryID, ConnectorTextAdapterName,
		ConnectorTextAdapterVersion, connectorProcessingStatusPending,
	); err != nil {
		return fmt.Errorf("ensuring connector text processing work: %w", err)
	}
	return nil
}

func readConnectorProcessingWork(
	ctx context.Context,
	tx pgx.Tx,
	connectorDeliveryID string,
	lock bool,
) (connectorProcessingWorkRecord, error) {
	query := `
		SELECT
			connector_delivery_id, connector_id, external_delivery_id,
			status, attempt_count, COALESCE(claim_id, ''),
			COALESCE(source_intake_request_id, ''),
			COALESCE(source_snapshot_id, ''),
			COALESCE(extraction_view_id, ''),
			COALESCE(raw_content_hash, ''), completed_at
		FROM detective_connector_inbox_processing_work
		WHERE connector_delivery_id = $1
	`
	if lock {
		query += " FOR UPDATE"
	}
	var work connectorProcessingWorkRecord
	if err := tx.QueryRow(ctx, query, connectorDeliveryID).Scan(
		&work.connectorDeliveryID,
		&work.connectorID,
		&work.externalDeliveryID,
		&work.status,
		&work.attemptCount,
		&work.claimID,
		&work.sourceIntakeRequestID,
		&work.sourceSnapshotID,
		&work.extractionViewID,
		&work.rawContentHash,
		&work.completedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connectorProcessingWorkRecord{}, newDomainError(
				ErrorConnectorProcessingConflict,
				"connector delivery %s has no text processing work",
				connectorDeliveryID,
			)
		}
		return connectorProcessingWorkRecord{}, fmt.Errorf("reading connector processing work: %w", err)
	}
	if work.completedAt != nil {
		completedAt := work.completedAt.UTC()
		work.completedAt = &completedAt
	}
	return work, nil
}

func readConnectorProcessingAttempt(
	ctx context.Context,
	tx pgx.Tx,
	claimID string,
	lock bool,
) (connectorProcessingAttemptRecord, error) {
	query := `
		SELECT
			claim_id, connector_delivery_id, connector_id, external_delivery_id,
			attempt_number, worker_id, status, claimed_at,
			lease_duration_milliseconds, lease_expires_at, finished_at,
			COALESCE(source_intake_request_id, ''),
			COALESCE(source_snapshot_id, ''),
			COALESCE(extraction_view_id, ''),
			COALESCE(raw_content_hash, ''),
			COALESCE(failure_class, ''), COALESCE(failure_message, '')
		FROM detective_connector_inbox_processing_attempts
		WHERE claim_id = $1
	`
	if lock {
		query += " FOR UPDATE"
	}
	var attempt connectorProcessingAttemptRecord
	if err := tx.QueryRow(ctx, query, claimID).Scan(
		&attempt.claimID,
		&attempt.connectorDeliveryID,
		&attempt.connectorID,
		&attempt.externalDeliveryID,
		&attempt.attemptNumber,
		&attempt.workerID,
		&attempt.status,
		&attempt.claimedAt,
		&attempt.leaseDurationMillis,
		&attempt.leaseExpiresAt,
		&attempt.finishedAt,
		&attempt.sourceIntakeRequestID,
		&attempt.sourceSnapshotID,
		&attempt.extractionViewID,
		&attempt.rawContentHash,
		&attempt.failureClass,
		&attempt.failureMessage,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connectorProcessingAttemptRecord{}, newDomainError(
				ErrorConnectorProcessingConflict,
				"connector processing claim %s was not found",
				claimID,
			)
		}
		return connectorProcessingAttemptRecord{}, fmt.Errorf("reading connector processing attempt: %w", err)
	}
	attempt.claimedAt = attempt.claimedAt.UTC()
	attempt.leaseExpiresAt = attempt.leaseExpiresAt.UTC()
	if attempt.finishedAt != nil {
		finishedAt := attempt.finishedAt.UTC()
		attempt.finishedAt = &finishedAt
	}
	return attempt, nil
}

func readConnectorProcessingRequest(
	ctx context.Context,
	tx pgx.Tx,
	requestID string,
	lock bool,
) (persistedConnectorProcessingRequest, bool, error) {
	query := `
		SELECT
			request.request_id, request.connector_delivery_id,
			request.connector_id, request.external_delivery_id,
			request.worker_id, request.lease_duration_milliseconds,
			request.claim_id, attempt.attempt_number, attempt.worker_id,
			request.status, attempt.claimed_at, attempt.lease_expires_at,
			attempt.finished_at,
			COALESCE(request.source_intake_request_id, ''),
			COALESCE(request.source_snapshot_id, ''),
			COALESCE(request.extraction_view_id, ''),
			COALESCE(request.raw_content_hash, ''),
			request.claim_created,
			COALESCE(request.failure_class, ''),
			COALESCE(request.failure_message, ''),
			request.request_payload_hash
		FROM detective_connector_inbox_processing_requests AS request
		JOIN detective_connector_inbox_processing_attempts AS attempt
		  ON attempt.claim_id = request.claim_id
		WHERE request.request_id = $1
	`
	if lock {
		query += " FOR UPDATE OF request"
	}
	var persisted persistedConnectorProcessingRequest
	var connectorID, externalID string
	err := tx.QueryRow(ctx, query, requestID).Scan(
		&persisted.result.RequestID,
		&persisted.result.ConnectorDeliveryID,
		&connectorID,
		&externalID,
		&persisted.requestedWorkerID,
		&persisted.requestedLeaseMillis,
		&persisted.result.ClaimID,
		&persisted.result.AttemptNumber,
		&persisted.result.WorkerID,
		&persisted.result.Status,
		&persisted.result.ClaimedAt,
		&persisted.result.LeaseExpiresAt,
		&persisted.result.FinishedAt,
		&persisted.result.SourceIntakeRequestID,
		&persisted.result.SourceSnapshotID,
		&persisted.result.ExtractionViewID,
		&persisted.result.RawContentHash,
		&persisted.result.ClaimCreated,
		&persisted.failureClass,
		&persisted.failureMessage,
		&persisted.payloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedConnectorProcessingRequest{}, false, nil
	}
	if err != nil {
		return persistedConnectorProcessingRequest{}, false, fmt.Errorf("reading connector processing request: %w", err)
	}
	persisted.result.ConnectorID = connectorID
	persisted.result.ExternalDeliveryID = externalID
	persisted.result.Reused = !persisted.result.ClaimCreated && persisted.result.Status == ConnectorProcessingStatusSucceeded
	persisted.result.ClaimedAt = persisted.result.ClaimedAt.UTC()
	persisted.result.LeaseExpiresAt = persisted.result.LeaseExpiresAt.UTC()
	if persisted.result.FinishedAt != nil {
		finishedAt := persisted.result.FinishedAt.UTC()
		persisted.result.FinishedAt = &finishedAt
	}
	return persisted, true, nil
}

func connectorProcessingResult(
	requestID string,
	work connectorProcessingWorkRecord,
	attempt connectorProcessingAttemptRecord,
) (ConnectorTextProcessingResult, error) {
	if work.connectorDeliveryID != attempt.connectorDeliveryID ||
		work.connectorID != attempt.connectorID ||
		work.externalDeliveryID != attempt.externalDeliveryID ||
		work.claimID != attempt.claimID ||
		work.attemptCount != attempt.attemptNumber ||
		work.status != attempt.status {
		return ConnectorTextProcessingResult{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing work and claim %s disagree",
			attempt.claimID,
		)
	}
	if attempt.status == ConnectorProcessingStatusSucceeded &&
		(work.sourceIntakeRequestID != attempt.sourceIntakeRequestID ||
			work.sourceSnapshotID != attempt.sourceSnapshotID ||
			work.extractionViewID != attempt.extractionViewID ||
			work.rawContentHash != attempt.rawContentHash ||
			work.completedAt == nil || attempt.finishedAt == nil ||
			!work.completedAt.Equal(*attempt.finishedAt)) {
		return ConnectorTextProcessingResult{}, newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing success %s has inconsistent source authority",
			attempt.claimID,
		)
	}
	return ConnectorTextProcessingResult{
		RequestID:             requestID,
		ConnectorDeliveryID:   work.connectorDeliveryID,
		ConnectorID:           work.connectorID,
		ExternalDeliveryID:    work.externalDeliveryID,
		ClaimID:               attempt.claimID,
		AttemptNumber:         attempt.attemptNumber,
		WorkerID:              attempt.workerID,
		Status:                attempt.status,
		ClaimedAt:             attempt.claimedAt,
		LeaseExpiresAt:        attempt.leaseExpiresAt,
		FinishedAt:            attempt.finishedAt,
		SourceIntakeRequestID: attempt.sourceIntakeRequestID,
		SourceSnapshotID:      attempt.sourceSnapshotID,
		ExtractionViewID:      attempt.extractionViewID,
		RawContentHash:        attempt.rawContentHash,
	}, nil
}

func validateConnectorProcessingRequest(
	request preparedConnectorProcessingRequest,
	persisted persistedConnectorProcessingRequest,
	work connectorProcessingWorkRecord,
	attempt connectorProcessingAttemptRecord,
) error {
	if persisted.payloadHash != request.payloadHash ||
		persisted.result.RequestID != request.input.RequestID ||
		persisted.result.ConnectorDeliveryID != request.input.ConnectorDeliveryID ||
		persisted.requestedWorkerID != request.input.WorkerID ||
		persisted.requestedLeaseMillis != request.input.LeaseDurationMilliseconds {
		return newDomainError(
			ErrorIdempotencyKeyReused,
			"request_id %s already exists with different connector processing payload",
			request.input.RequestID,
		)
	}
	if persisted.result.ConnectorDeliveryID != work.connectorDeliveryID ||
		persisted.result.ConnectorID != work.connectorID ||
		persisted.result.ExternalDeliveryID != work.externalDeliveryID ||
		persisted.result.ClaimID != attempt.claimID ||
		persisted.result.AttemptNumber != attempt.attemptNumber ||
		persisted.result.WorkerID != attempt.workerID ||
		persisted.result.Status != attempt.status ||
		!persisted.result.ClaimedAt.Equal(attempt.claimedAt) ||
		!persisted.result.LeaseExpiresAt.Equal(attempt.leaseExpiresAt) {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing request %s disagrees with its exact claim",
			request.input.RequestID,
		)
	}
	if attempt.status == ConnectorProcessingStatusSucceeded &&
		(persisted.result.SourceIntakeRequestID != attempt.sourceIntakeRequestID ||
			persisted.result.SourceSnapshotID != attempt.sourceSnapshotID ||
			persisted.result.ExtractionViewID != attempt.extractionViewID ||
			persisted.result.RawContentHash != attempt.rawContentHash ||
			persisted.result.FinishedAt == nil || attempt.finishedAt == nil ||
			!persisted.result.FinishedAt.Equal(*attempt.finishedAt)) {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing request %s has inconsistent source authority",
			request.input.RequestID,
		)
	}
	if (attempt.status == ConnectorProcessingStatusFailed || attempt.status == ConnectorProcessingStatusExpired) &&
		(persisted.failureClass != attempt.failureClass || persisted.failureMessage != attempt.failureMessage) {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing request %s has inconsistent failure diagnostics",
			request.input.RequestID,
		)
	}
	if attempt.status == ConnectorProcessingStatusRunning {
		if work.status != ConnectorProcessingStatusRunning || work.claimID != attempt.claimID {
			return newDomainError(
				ErrorConnectorProcessingConflict,
				"connector processing claim %s no longer owns its work",
				attempt.claimID,
			)
		}
	} else if attempt.status == ConnectorProcessingStatusSucceeded {
		if work.status != ConnectorProcessingStatusSucceeded || work.claimID != attempt.claimID ||
			work.sourceIntakeRequestID != attempt.sourceIntakeRequestID ||
			work.sourceSnapshotID != attempt.sourceSnapshotID ||
			work.extractionViewID != attempt.extractionViewID ||
			work.rawContentHash != attempt.rawContentHash {
			return newDomainError(
				ErrorConnectorProcessingConflict,
				"connector processing success %s disagrees with durable work",
				attempt.claimID,
			)
		}
	}
	return nil
}

func lockConnectorProcessingRequest(ctx context.Context, tx pgx.Tx, requestID string) error {
	return lockConnectorOperationRequest(ctx, tx, "detective-connector-text-process", requestID)
}

func lockConnectorRecoveryRequest(ctx context.Context, tx pgx.Tx, requestID string) error {
	return lockConnectorOperationRequest(ctx, tx, "detective-connector-text-recover", requestID)
}

func lockConnectorOperationRequest(ctx context.Context, tx pgx.Tx, operation, requestID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_ingestion_request_serializations (operation_name, request_id)
		VALUES ($1, $2)
		ON CONFLICT (operation_name, request_id) DO NOTHING
	`, operation, requestID); err != nil {
		return fmt.Errorf("creating %s request serialization: %w", operation, err)
	}
	var locked int
	if err := tx.QueryRow(ctx, `
		SELECT 1
		FROM evidence_ingestion_request_serializations
		WHERE operation_name = $1 AND request_id = $2
		FOR UPDATE
	`, operation, requestID).Scan(&locked); err != nil {
		return fmt.Errorf("locking %s request: %w", operation, err)
	}
	return nil
}

func insertConnectorProcessingAttempt(ctx context.Context, tx pgx.Tx, attempt connectorProcessingAttemptRecord) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_attempts (
			claim_id, connector_delivery_id, connector_id, external_delivery_id,
			attempt_number, worker_id, status, claimed_at,
			lease_duration_milliseconds, lease_expires_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, attempt.claimID, attempt.connectorDeliveryID, attempt.connectorID,
		attempt.externalDeliveryID, attempt.attemptNumber, attempt.workerID,
		attempt.status, attempt.claimedAt, attempt.leaseDurationMillis,
		attempt.leaseExpiresAt,
	); err != nil {
		return fmt.Errorf("inserting connector processing attempt: %w", err)
	}
	return nil
}

func markConnectorProcessingWorkRunning(
	ctx context.Context,
	tx pgx.Tx,
	work connectorProcessingWorkRecord,
	attempt connectorProcessingAttemptRecord,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_work
		SET status = $2, attempt_count = $3, claim_id = $4, updated_at = $5
		WHERE connector_delivery_id = $1 AND status = $6
	`, work.connectorDeliveryID, ConnectorProcessingStatusRunning,
		attempt.attemptNumber, attempt.claimID, attempt.claimedAt,
		connectorProcessingStatusPending,
	)
	if err != nil {
		return fmt.Errorf("claiming connector processing work: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"pending connector processing work %s changed during claim",
			work.connectorDeliveryID,
		)
	}
	return nil
}

func insertConnectorProcessingRequest(
	ctx context.Context,
	tx pgx.Tx,
	request preparedConnectorProcessingRequest,
	result ConnectorTextProcessingResult,
	claimCreated bool,
	failureClass string,
	failureMessage string,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_requests (
			request_id, connector_delivery_id, connector_id, external_delivery_id,
			worker_id, lease_duration_milliseconds, claim_id, status,
			source_intake_request_id, source_snapshot_id, extraction_view_id,
			raw_content_hash, claim_created, failure_class, failure_message,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	`, request.input.RequestID, result.ConnectorDeliveryID, result.ConnectorID,
		result.ExternalDeliveryID, request.input.WorkerID,
		request.input.LeaseDurationMilliseconds, result.ClaimID, result.Status,
		nullableConnectorString(result.SourceIntakeRequestID),
		nullableConnectorString(result.SourceSnapshotID),
		nullableConnectorString(result.ExtractionViewID),
		nullableConnectorString(result.RawContentHash),
		claimCreated, nullableConnectorString(failureClass),
		nullableConnectorString(failureMessage), request.payloadHash,
	); err != nil {
		return fmt.Errorf("inserting connector processing request: %w", err)
	}
	return nil
}

func markConnectorProcessingAttemptSucceeded(
	ctx context.Context,
	tx pgx.Tx,
	attempt connectorProcessingAttemptRecord,
	result ConnectorTextProcessingResult,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_attempts
		SET status = $2, finished_at = $3,
			source_intake_request_id = $4, source_snapshot_id = $5,
			extraction_view_id = $6, raw_content_hash = $7
		WHERE claim_id = $1 AND status = $8
	`, attempt.claimID, ConnectorProcessingStatusSucceeded, result.FinishedAt,
		result.SourceIntakeRequestID, result.SourceSnapshotID,
		result.ExtractionViewID, result.RawContentHash,
		ConnectorProcessingStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("finishing connector processing attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing claim %s changed during finish", attempt.claimID)
	}
	return nil
}

func markConnectorProcessingWorkSucceeded(
	ctx context.Context,
	tx pgx.Tx,
	work connectorProcessingWorkRecord,
	result ConnectorTextProcessingResult,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_work
		SET status = $2,
			source_intake_request_id = $3, source_snapshot_id = $4,
			extraction_view_id = $5, raw_content_hash = $6,
			completed_at = $7, updated_at = $7
		WHERE connector_delivery_id = $1 AND status = $8 AND claim_id = $9
	`, work.connectorDeliveryID, ConnectorProcessingStatusSucceeded,
		result.SourceIntakeRequestID, result.SourceSnapshotID,
		result.ExtractionViewID, result.RawContentHash, result.FinishedAt,
		ConnectorProcessingStatusRunning, result.ClaimID,
	)
	if err != nil {
		return fmt.Errorf("finishing connector processing work: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing work %s changed during finish", work.connectorDeliveryID)
	}
	return nil
}

func markConnectorProcessingRequestSucceeded(
	ctx context.Context,
	tx pgx.Tx,
	requestID string,
	result ConnectorTextProcessingResult,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_requests
		SET status = $2,
			source_intake_request_id = $3, source_snapshot_id = $4,
			extraction_view_id = $5, raw_content_hash = $6,
			updated_at = $7
		WHERE request_id = $1 AND status = $8 AND claim_id = $9
	`, requestID, ConnectorProcessingStatusSucceeded,
		result.SourceIntakeRequestID, result.SourceSnapshotID,
		result.ExtractionViewID, result.RawContentHash, result.FinishedAt,
		ConnectorProcessingStatusRunning, result.ClaimID,
	)
	if err != nil {
		return fmt.Errorf("finishing connector processing request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing request %s changed during finish", requestID)
	}
	return nil
}

func markConnectorProcessingAttemptFailed(
	ctx context.Context,
	tx pgx.Tx,
	attempt connectorProcessingAttemptRecord,
	failureClass string,
	failureMessage string,
	finishedAt time.Time,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_attempts
		SET status = $2, finished_at = $3,
			failure_class = $4, failure_message = $5
		WHERE claim_id = $1 AND status = $6
	`, attempt.claimID, ConnectorProcessingStatusFailed, finishedAt,
		failureClass, failureMessage, ConnectorProcessingStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("failing connector processing attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing claim %s changed during failure", attempt.claimID)
	}
	return nil
}

func markConnectorProcessingAttemptExpired(
	ctx context.Context,
	tx pgx.Tx,
	attempt connectorProcessingAttemptRecord,
	failureClass string,
	failureMessage string,
	recoveredAt time.Time,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_attempts
		SET status = $2, finished_at = $3,
			failure_class = $4, failure_message = $5
		WHERE claim_id = $1 AND status = $6
	`, attempt.claimID, ConnectorProcessingStatusExpired, recoveredAt,
		failureClass, failureMessage, ConnectorProcessingStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("expiring connector processing attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing claim %s changed during recovery", attempt.claimID)
	}
	return nil
}

func requeueConnectorProcessingWork(
	ctx context.Context,
	tx pgx.Tx,
	work connectorProcessingWorkRecord,
	claimID string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_work
		SET status = $2, claim_id = NULL, updated_at = now()
		WHERE connector_delivery_id = $1 AND status = $3 AND claim_id = $4
	`, work.connectorDeliveryID, connectorProcessingStatusPending,
		ConnectorProcessingStatusRunning, claimID,
	)
	if err != nil {
		return fmt.Errorf("requeueing connector processing work: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing work %s changed during requeue", work.connectorDeliveryID)
	}
	return nil
}

func markConnectorProcessingRequestFailed(
	ctx context.Context,
	tx pgx.Tx,
	requestID string,
	failureClass string,
	failureMessage string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_requests
		SET status = $2, failure_class = $3, failure_message = $4, updated_at = now()
		WHERE request_id = $1 AND status = $5
	`, requestID, ConnectorProcessingStatusFailed, failureClass,
		failureMessage, ConnectorProcessingStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("failing connector processing request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing request %s changed during failure", requestID)
	}
	return nil
}

func markConnectorProcessingClaimRequestsExpired(
	ctx context.Context,
	tx pgx.Tx,
	claimID string,
	failureClass string,
	failureMessage string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_requests
		SET status = $2, failure_class = $3, failure_message = $4, updated_at = now()
		WHERE claim_id = $1 AND status = $5
	`, claimID, ConnectorProcessingStatusExpired, failureClass,
		failureMessage, ConnectorProcessingStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("expiring connector processing requests: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing claim %s has no running request", claimID)
	}
	return nil
}

func insertConnectorRecoveryRequest(
	ctx context.Context,
	tx pgx.Tx,
	request preparedConnectorRecoveryRequest,
	attempt connectorProcessingAttemptRecord,
	result ConnectorProcessingRecoveryResult,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_recovery_requests (
			request_id, connector_delivery_id, connector_id,
			external_delivery_id, claim_id, worker_id,
			recovered_at, request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, request.input.RequestID, attempt.connectorDeliveryID, attempt.connectorID,
		attempt.externalDeliveryID, attempt.claimID, attempt.workerID,
		result.RecoveredAt, request.payloadHash,
	); err != nil {
		return fmt.Errorf("inserting connector processing recovery request: %w", err)
	}
	return nil
}

func readConnectorRecoveryRequest(
	ctx context.Context,
	tx pgx.Tx,
	requestID string,
) (persistedConnectorRecoveryRequest, bool, error) {
	var persisted persistedConnectorRecoveryRequest
	err := tx.QueryRow(ctx, `
		SELECT
			recovery.request_id, recovery.connector_delivery_id,
			recovery.connector_id, recovery.external_delivery_id,
			recovery.claim_id, recovery.worker_id,
			attempt.attempt_number, recovery.recovered_at,
			recovery.request_payload_hash
		FROM detective_connector_inbox_processing_recovery_requests AS recovery
		JOIN detective_connector_inbox_processing_attempts AS attempt
		  ON attempt.claim_id = recovery.claim_id
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.result.RequestID,
		&persisted.result.ConnectorDeliveryID,
		&persisted.connectorID,
		&persisted.externalID,
		&persisted.result.ClaimID,
		&persisted.result.WorkerID,
		&persisted.result.AttemptNumber,
		&persisted.result.RecoveredAt,
		&persisted.payloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedConnectorRecoveryRequest{}, false, nil
	}
	if err != nil {
		return persistedConnectorRecoveryRequest{}, false, fmt.Errorf("reading connector processing recovery request: %w", err)
	}
	persisted.result.RecoveredAt = persisted.result.RecoveredAt.UTC()
	return persisted, true, nil
}

func nullableConnectorString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
