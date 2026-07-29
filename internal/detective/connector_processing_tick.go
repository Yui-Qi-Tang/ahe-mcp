package detective

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ConnectorTextProcessingTickActionProcess starts a controller-owned attempt.
	ConnectorTextProcessingTickActionProcess = "process"
	// ConnectorTextProcessingTickActionResume reuses one active exact request.
	ConnectorTextProcessingTickActionResume = "resume"
	// ConnectorTextProcessingTickActionRecoverAndProcess expires one elapsed claim before retrying.
	ConnectorTextProcessingTickActionRecoverAndProcess = "recover_and_process"

	maxConnectorTextProcessingTickDeliveries = 100
	connectorTextProcessingTickContract      = "detective-connector-text-processing-tick-v1"
	connectorTextProcessingTickLeaseDuration = time.Minute
)

// ConnectorTextProcessingTickInput selects one connector's durable text work.
type ConnectorTextProcessingTickInput struct {
	ConnectorID   string `json:"connector_id"`
	MaxDeliveries int    `json:"max_deliveries"`
}

// ConnectorTextProcessingTickItem reports one selected delivery transition.
type ConnectorTextProcessingTickItem struct {
	ConnectorDeliveryID string                             `json:"connector_delivery_id"`
	Action              string                             `json:"action"`
	Recovery            *ConnectorProcessingRecoveryResult `json:"recovery,omitempty"`
	Processing          *ConnectorTextProcessingResult     `json:"processing,omitempty"`
	FailureClass        string                             `json:"failure_class,omitempty"`
	FailureMessage      string                             `json:"failure_message,omitempty"`
}

// ConnectorTextProcessingTickResult is a bounded, non-authoritative observation.
// Exact process and recovery request rows remain the durable transition authority.
type ConnectorTextProcessingTickResult struct {
	ConnectorID    string                            `json:"connector_id"`
	SelectedCount  int                               `json:"selected_count"`
	SucceededCount int                               `json:"succeeded_count"`
	FailedCount    int                               `json:"failed_count"`
	RecoveredCount int                               `json:"recovered_count"`
	Items          []ConnectorTextProcessingTickItem `json:"items"`
}

type preparedConnectorTextProcessingTick struct {
	connectorID   string
	maxDeliveries int
}

type connectorTextProcessingCandidate struct {
	connectorDeliveryID  string
	connectorID          string
	externalDeliveryID   string
	adapterName          string
	adapterVersion       string
	workStatus           string
	attemptCount         int
	workClaimID          string
	attemptClaimID       string
	attemptNumber        int
	attemptWorkerID      string
	attemptStatus        string
	leaseDurationMillis  int64
	claimedAt            *time.Time
	leaseExpiresAt       *time.Time
	attemptRequestCount  int64
	attemptRequestID     string
	attemptRequestStatus string
	requestWorkerID      string
	requestLeaseMillis   int64
}

// RunConnectorTextProcessingTick processes a bounded snapshot for one connector.
// Failed attempts remain pending for an explicit retry; only new or expired work is selected.
func RunConnectorTextProcessingTick(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ConnectorTextProcessingTickInput,
) (ConnectorTextProcessingTickResult, error) {
	if pool == nil {
		return ConnectorTextProcessingTickResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := prepareConnectorTextProcessingTick(input)
	if err != nil {
		return ConnectorTextProcessingTickResult{}, err
	}
	observedAt := time.Now().UTC()
	candidates, err := discoverConnectorTextProcessingCandidates(ctx, pool, request)
	if err != nil {
		return ConnectorTextProcessingTickResult{}, err
	}
	result := ConnectorTextProcessingTickResult{
		ConnectorID:   request.connectorID,
		SelectedCount: len(candidates),
		Items:         make([]ConnectorTextProcessingTickItem, 0, len(candidates)),
	}
	var tickErrors []error
	for _, candidate := range candidates {
		item := ConnectorTextProcessingTickItem{ConnectorDeliveryID: candidate.connectorDeliveryID}
		processingInput := connectorTextTickNewProcessingInput(candidate)

		if candidate.workStatus == ConnectorProcessingStatusRunning {
			if observedAt.Before(*candidate.leaseExpiresAt) {
				item.Action = ConnectorTextProcessingTickActionResume
				processingInput = ConnectorTextProcessingInput{
					RequestID:                 candidate.attemptRequestID,
					ConnectorDeliveryID:       candidate.connectorDeliveryID,
					WorkerID:                  candidate.requestWorkerID,
					LeaseDurationMilliseconds: candidate.requestLeaseMillis,
				}
			} else {
				item.Action = ConnectorTextProcessingTickActionRecoverAndProcess
				recovery, recoveryErr := RecoverConnectorTextProcessing(ctx, pool, ConnectorProcessingRecoveryInput{
					RequestID:           connectorTextTickRecoveryRequestID(candidate.attemptClaimID),
					ConnectorDeliveryID: candidate.connectorDeliveryID,
					ClaimID:             candidate.attemptClaimID,
					WorkerID:            candidate.attemptWorkerID,
				})
				if recoveryErr != nil {
					setConnectorTextTickFailure(&item, recoveryErr)
					result.FailedCount++
					result.Items = append(result.Items, item)
					tickErrors = append(tickErrors, fmt.Errorf("recovering connector delivery %s: %w", candidate.connectorDeliveryID, recoveryErr))
					continue
				}
				item.Recovery = &recovery
				result.RecoveredCount++
			}
		} else {
			item.Action = ConnectorTextProcessingTickActionProcess
		}
		if item.Action != ConnectorTextProcessingTickActionResume && candidate.attemptCount >= maxConnectorProcessingAttempts {
			exhaustedErr := newDomainError(
				ErrorConnectorProcessingConflict,
				"connector delivery %s exhausted bounded processing attempts",
				candidate.connectorDeliveryID,
			)
			setConnectorTextTickFailure(&item, exhaustedErr)
			result.FailedCount++
			result.Items = append(result.Items, item)
			tickErrors = append(tickErrors, exhaustedErr)
			continue
		}

		processed, processErr := ProcessConnectorTextDelivery(ctx, pool, processingInput)
		if processErr != nil {
			setConnectorTextTickFailure(&item, processErr)
			result.FailedCount++
			result.Items = append(result.Items, item)
			tickErrors = append(tickErrors, fmt.Errorf("processing connector delivery %s: %w", candidate.connectorDeliveryID, processErr))
			continue
		}
		item.Processing = &processed
		result.SucceededCount++
		result.Items = append(result.Items, item)
	}
	return result, errors.Join(tickErrors...)
}

func prepareConnectorTextProcessingTick(input ConnectorTextProcessingTickInput) (preparedConnectorTextProcessingTick, error) {
	connectorID, err := normalizeBoundedText(input.ConnectorID, "connector_id", 200)
	if err != nil {
		return preparedConnectorTextProcessingTick{}, err
	}
	if input.MaxDeliveries < 1 || input.MaxDeliveries > maxConnectorTextProcessingTickDeliveries {
		return preparedConnectorTextProcessingTick{}, newDomainError(
			ErrorInvalidInput,
			"max_deliveries must be between 1 and %d",
			maxConnectorTextProcessingTickDeliveries,
		)
	}
	return preparedConnectorTextProcessingTick{
		connectorID: connectorID, maxDeliveries: input.MaxDeliveries,
	}, nil
}

func discoverConnectorTextProcessingCandidates(
	ctx context.Context,
	pool *pgxpool.Pool,
	request preparedConnectorTextProcessingTick,
) ([]connectorTextProcessingCandidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			work.connector_delivery_id, work.connector_id, work.external_delivery_id,
			work.adapter_name, work.adapter_version, work.status, work.attempt_count,
			COALESCE(work.claim_id, ''), COALESCE(attempt.claim_id, ''),
			COALESCE(attempt.attempt_number, 0), COALESCE(attempt.worker_id, ''),
			COALESCE(attempt.status, ''),
			COALESCE(attempt.lease_duration_milliseconds, 0),
			attempt.claimed_at, attempt.lease_expires_at,
			COALESCE(attempt_request.request_count, 0),
			COALESCE(attempt_request.request_id, ''),
			COALESCE(attempt_request.status, ''),
			COALESCE(attempt_request.worker_id, ''),
			COALESCE(attempt_request.lease_duration_milliseconds, 0)
		FROM detective_connector_inbox_processing_work AS work
		LEFT JOIN detective_connector_inbox_processing_attempts AS attempt
		  ON attempt.connector_delivery_id = work.connector_delivery_id
		 AND attempt.attempt_number = work.attempt_count
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) AS request_count,
				MIN(request_id) AS request_id,
				MIN(status) AS status,
				MIN(worker_id) AS worker_id,
				MIN(lease_duration_milliseconds) AS lease_duration_milliseconds
			FROM detective_connector_inbox_processing_requests
			WHERE claim_id = attempt.claim_id
		) AS attempt_request ON TRUE
		WHERE work.connector_id = $1
		  AND work.adapter_name = $2
		  AND work.adapter_version = $3
		  AND (
			work.status = $4
			OR (
				work.status = $5
				AND (work.attempt_count = 0 OR attempt.status = $6)
			)
		  )
		ORDER BY work.updated_at, work.connector_delivery_id
		LIMIT $7
	`, request.connectorID, ConnectorTextAdapterName, ConnectorTextAdapterVersion,
		ConnectorProcessingStatusRunning, connectorProcessingStatusPending,
		ConnectorProcessingStatusExpired, request.maxDeliveries,
	)
	if err != nil {
		return nil, fmt.Errorf("discovering connector text processing work: %w", err)
	}
	defer rows.Close()

	candidates := make([]connectorTextProcessingCandidate, 0, request.maxDeliveries)
	for rows.Next() {
		var candidate connectorTextProcessingCandidate
		if err := rows.Scan(
			&candidate.connectorDeliveryID,
			&candidate.connectorID,
			&candidate.externalDeliveryID,
			&candidate.adapterName,
			&candidate.adapterVersion,
			&candidate.workStatus,
			&candidate.attemptCount,
			&candidate.workClaimID,
			&candidate.attemptClaimID,
			&candidate.attemptNumber,
			&candidate.attemptWorkerID,
			&candidate.attemptStatus,
			&candidate.leaseDurationMillis,
			&candidate.claimedAt,
			&candidate.leaseExpiresAt,
			&candidate.attemptRequestCount,
			&candidate.attemptRequestID,
			&candidate.attemptRequestStatus,
			&candidate.requestWorkerID,
			&candidate.requestLeaseMillis,
		); err != nil {
			return nil, fmt.Errorf("scanning connector text processing work: %w", err)
		}
		if err := validateConnectorTextProcessingCandidate(candidate, request.connectorID); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating connector text processing work: %w", err)
	}
	return candidates, nil
}

func validateConnectorTextProcessingCandidate(candidate connectorTextProcessingCandidate, connectorID string) error {
	if normalized, err := normalizeBoundedText(candidate.connectorID, "connector_id", 200); err != nil || normalized != candidate.connectorID {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing candidate has invalid connector identity")
	}
	if normalized, err := normalizeBoundedText(candidate.externalDeliveryID, "external_delivery_id", 500); err != nil || normalized != candidate.externalDeliveryID {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing candidate has invalid external delivery identity")
	}
	if candidate.connectorID != connectorID ||
		candidate.adapterName != ConnectorTextAdapterName ||
		candidate.adapterVersion != ConnectorTextAdapterVersion ||
		candidate.connectorDeliveryID != connectorDeliveryID(candidate.connectorID, candidate.externalDeliveryID) {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector processing candidate %s has inconsistent delivery authority",
			candidate.connectorDeliveryID,
		)
	}
	if _, err := normalizeConnectorDeliveryID(candidate.connectorDeliveryID); err != nil {
		return newDomainError(ErrorConnectorProcessingConflict, "connector processing candidate has invalid delivery identity")
	}
	if candidate.attemptCount < 0 || candidate.attemptCount > maxConnectorProcessingAttempts {
		return newDomainError(ErrorConnectorProcessingConflict, "connector delivery %s has invalid attempt count", candidate.connectorDeliveryID)
	}

	switch candidate.workStatus {
	case connectorProcessingStatusPending:
		if candidate.workClaimID != "" {
			return newDomainError(ErrorConnectorProcessingConflict, "pending connector delivery %s retains active claim authority", candidate.connectorDeliveryID)
		}
		if candidate.attemptCount == 0 {
			if candidate.attemptClaimID != "" || candidate.attemptNumber != 0 || candidate.attemptStatus != "" ||
				candidate.attemptRequestCount != 0 {
				return newDomainError(ErrorConnectorProcessingConflict, "new connector delivery %s has unexpected attempt history", candidate.connectorDeliveryID)
			}
			return nil
		}
		if candidate.attemptClaimID == "" || candidate.attemptNumber != candidate.attemptCount ||
			candidate.attemptStatus != ConnectorProcessingStatusExpired ||
			candidate.attemptRequestCount != 1 || candidate.attemptRequestStatus != ConnectorProcessingStatusExpired {
			return newDomainError(ErrorConnectorProcessingConflict, "pending connector delivery %s is not recoverable expired work", candidate.connectorDeliveryID)
		}
		return validateConnectorTextProcessingCandidateAuthority(candidate)
	case ConnectorProcessingStatusRunning:
		if candidate.attemptCount < 1 || candidate.workClaimID == "" ||
			candidate.workClaimID != candidate.attemptClaimID ||
			candidate.attemptNumber != candidate.attemptCount ||
			candidate.attemptStatus != ConnectorProcessingStatusRunning ||
			candidate.attemptRequestCount != 1 || candidate.attemptRequestStatus != ConnectorProcessingStatusRunning ||
			candidate.attemptRequestID == "" ||
			candidate.attemptWorkerID == "" || candidate.requestWorkerID != candidate.attemptWorkerID ||
			candidate.requestLeaseMillis != candidate.leaseDurationMillis ||
			candidate.claimedAt == nil || candidate.leaseExpiresAt == nil ||
			!candidate.leaseExpiresAt.After(*candidate.claimedAt) {
			return newDomainError(ErrorConnectorProcessingConflict, "running connector delivery %s has inconsistent claim authority", candidate.connectorDeliveryID)
		}
		return validateConnectorTextProcessingCandidateAuthority(candidate)
	default:
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector delivery %s has unsupported tick status %s",
			candidate.connectorDeliveryID,
			candidate.workStatus,
		)
	}
}

func validateConnectorTextProcessingCandidateAuthority(candidate connectorTextProcessingCandidate) error {
	if _, err := normalizeConnectorClaimID(candidate.attemptClaimID); err != nil {
		return newDomainError(ErrorConnectorProcessingConflict, "connector delivery %s has invalid claim identity", candidate.connectorDeliveryID)
	}
	if normalized, err := normalizeBoundedText(candidate.attemptWorkerID, "worker_id", 200); err != nil || normalized != candidate.attemptWorkerID {
		return newDomainError(ErrorConnectorProcessingConflict, "connector delivery %s has invalid worker authority", candidate.connectorDeliveryID)
	}
	if candidate.leaseDurationMillis < ConnectorProcessingMinLeaseDuration.Milliseconds() ||
		candidate.leaseDurationMillis > ConnectorProcessingMaxLeaseDuration.Milliseconds() ||
		candidate.claimedAt == nil || candidate.leaseExpiresAt == nil ||
		!candidate.leaseExpiresAt.After(*candidate.claimedAt) {
		return newDomainError(ErrorConnectorProcessingConflict, "connector delivery %s has invalid lease authority", candidate.connectorDeliveryID)
	}
	if normalized, err := normalizeBoundedText(candidate.attemptRequestID, "request_id", 300); err != nil || normalized != candidate.attemptRequestID ||
		candidate.requestWorkerID != candidate.attemptWorkerID ||
		candidate.requestLeaseMillis != candidate.leaseDurationMillis {
		return newDomainError(ErrorConnectorProcessingConflict, "connector delivery %s has invalid request authority", candidate.connectorDeliveryID)
	}
	return nil
}

func connectorTextTickNewProcessingInput(candidate connectorTextProcessingCandidate) ConnectorTextProcessingInput {
	return ConnectorTextProcessingInput{
		RequestID:                 connectorTextTickProcessingRequestID(candidate.connectorDeliveryID, candidate.attemptCount+1),
		ConnectorDeliveryID:       candidate.connectorDeliveryID,
		WorkerID:                  connectorTextTickWorkerID(candidate.connectorID),
		LeaseDurationMilliseconds: connectorTextProcessingTickLeaseDuration.Milliseconds(),
	}
}

func connectorTextTickWorkerID(connectorID string) string {
	return "detective-connector-text-worker:" + hashHex([]byte(
		connectorTextProcessingTickContract+"\x00worker\x00"+connectorID,
	))
}

func connectorTextTickProcessingRequestID(connectorDeliveryID string, attemptNumber int) string {
	return "detective-connector-text-process:" + hashHex(fmt.Appendf(nil,
		"%s\x00process\x00%s\x00%d",
		connectorTextProcessingTickContract,
		connectorDeliveryID,
		attemptNumber,
	))
}

func connectorTextTickRecoveryRequestID(claimID string) string {
	return "detective-connector-text-recover:" + hashHex([]byte(
		connectorTextProcessingTickContract+"\x00recover\x00"+claimID,
	))
}

func setConnectorTextTickFailure(item *ConnectorTextProcessingTickItem, err error) {
	item.FailureClass, item.FailureMessage = connectorProcessingFailure(err)
}
