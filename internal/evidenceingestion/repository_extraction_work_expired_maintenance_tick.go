package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit bounds one maintenance transaction.
	RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit = 100

	// RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery identifies an ownerless claim recovery.
	RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery = "recovery"
	// RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair identifies an execution-owned repair.
	RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair = "execution_repair"
)

// RepositoryExtractionWorkExpiredMaintenanceTickInput identifies one bounded expired-work maintenance run.
type RepositoryExtractionWorkExpiredMaintenanceTickInput struct {
	RequestID          string `json:"request_id"`
	Limit              int    `json:"limit"`
	MaintenanceActorID string `json:"maintenance_actor_id"`
}

// RepositoryExtractionWorkExpiredMaintenanceTransition reports one exact recovery or execution repair.
type RepositoryExtractionWorkExpiredMaintenanceTransition struct {
	TransitionKind  string                                         `json:"transition_kind"`
	Recovery        *RepositoryExtractionWorkRecoveryResult        `json:"recovery,omitempty"`
	ExecutionRepair *RepositoryExtractionWorkExecutionRepairResult `json:"execution_repair,omitempty"`
}

// RepositoryExtractionWorkExpiredMaintenanceTickResult reports one durable bounded maintenance batch.
type RepositoryExtractionWorkExpiredMaintenanceTickResult struct {
	RequestID          string                                                 `json:"request_id"`
	Limit              int                                                    `json:"limit"`
	MaintenanceActorID string                                                 `json:"maintenance_actor_id"`
	DiscoveredAt       time.Time                                              `json:"discovered_at"`
	Transitions        []RepositoryExtractionWorkExpiredMaintenanceTransition `json:"transitions"`
	Replayed           bool                                                   `json:"replayed"`
}

type repositoryExtractionWorkExpiredMaintenanceTickRequest struct {
	input              RepositoryExtractionWorkExpiredMaintenanceTickInput
	requestPayloadHash string
}

type persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest struct {
	input              RepositoryExtractionWorkExpiredMaintenanceTickInput
	transitionCount    int
	discoveredAt       time.Time
	requestPayloadHash string
}

type repositoryExtractionWorkExpiredMaintenanceCandidate struct {
	executionRequestID string
	work               RepositoryExtractionWork
	claimID            string
	workerID           string
}

// RunExpiredRepositoryExtractionWorkMaintenanceTick recovers one bounded cross-stream batch of expired work.
func RunExpiredRepositoryExtractionWorkMaintenanceTick(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkExpiredMaintenanceTickInput) (RepositoryExtractionWorkExpiredMaintenanceTickResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return runExpiredRepositoryExtractionWorkMaintenanceTick(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func runExpiredRepositoryExtractionWorkMaintenanceTick(ctx context.Context, db sqlDB, input RepositoryExtractionWorkExpiredMaintenanceTickInput, discoveredAt time.Time) (RepositoryExtractionWorkExpiredMaintenanceTickResult, error) {
	request, err := prepareRepositoryExtractionWorkExpiredMaintenanceTick(input)
	if err != nil {
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, err
	}
	if discoveredAt.IsZero() {
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, newDomainError(ErrorInvalidInput, "repository extraction work expired maintenance discovery time is required")
	}
	discoveredAt = discoveredAt.UTC()

	var result RepositoryExtractionWorkExpiredMaintenanceTickResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-expired-maintenance-tick", request.input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkExpiredMaintenanceTickRequest(ctx, tx, request.input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			persistedPayloadHash, err := repositoryExtractionWorkExpiredMaintenanceTickPayloadHash(persisted.input)
			if err != nil {
				return err
			}
			if persisted.requestPayloadHash != persistedPayloadHash {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has inconsistent durable identity", request.input.RequestID)
			}
			if persisted.requestPayloadHash != request.requestPayloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work expired maintenance tick payload", request.input.RequestID)
			}
			result, err = replayRepositoryExtractionWorkExpiredMaintenanceTick(ctx, tx, persisted)
			return err
		}

		candidates, err := lockExpiredRepositoryExtractionWorkMaintenanceCandidates(ctx, tx, request.input.Limit, discoveredAt)
		if err != nil {
			return err
		}
		result = RepositoryExtractionWorkExpiredMaintenanceTickResult{
			RequestID:          request.input.RequestID,
			Limit:              request.input.Limit,
			MaintenanceActorID: request.input.MaintenanceActorID,
			DiscoveredAt:       discoveredAt,
			Transitions:        make([]RepositoryExtractionWorkExpiredMaintenanceTransition, 0, len(candidates)),
		}
		for _, candidate := range candidates {
			transition, err := applyRepositoryExtractionWorkExpiredMaintenanceCandidate(ctx, tx, request.input, candidate, discoveredAt)
			if err != nil {
				return err
			}
			result.Transitions = append(result.Transitions, transition)
		}
		return persistRepositoryExtractionWorkExpiredMaintenanceTick(ctx, tx, request, result)
	})
	if err != nil {
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, err
	}
	return result, nil
}

func prepareRepositoryExtractionWorkExpiredMaintenanceTick(input RepositoryExtractionWorkExpiredMaintenanceTickInput) (repositoryExtractionWorkExpiredMaintenanceTickRequest, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.MaintenanceActorID = strings.TrimSpace(input.MaintenanceActorID)
	if input.RequestID == "" {
		return repositoryExtractionWorkExpiredMaintenanceTickRequest{}, newDomainError(ErrorInvalidInput, "repository extraction work expired maintenance tick request_id is required")
	}
	if input.Limit < 1 || input.Limit > RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit {
		return repositoryExtractionWorkExpiredMaintenanceTickRequest{}, newDomainError(
			ErrorInvalidInput,
			"repository extraction work expired maintenance tick limit must be between 1 and %d",
			RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit,
		)
	}
	if input.MaintenanceActorID == "" || len(input.MaintenanceActorID) > 200 {
		return repositoryExtractionWorkExpiredMaintenanceTickRequest{}, newDomainError(ErrorInvalidInput, "repository extraction work expired maintenance_actor_id must contain 1 to 200 bytes")
	}
	payloadHash, err := repositoryExtractionWorkExpiredMaintenanceTickPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkExpiredMaintenanceTickRequest{}, err
	}
	return repositoryExtractionWorkExpiredMaintenanceTickRequest{input: input, requestPayloadHash: payloadHash}, nil
}

func repositoryExtractionWorkExpiredMaintenanceTickPayloadHash(input RepositoryExtractionWorkExpiredMaintenanceTickInput) (string, error) {
	payload, err := deterministicJSON(struct {
		Limit              int    `json:"limit"`
		MaintenanceActorID string `json:"maintenance_actor_id"`
	}{
		Limit:              input.Limit,
		MaintenanceActorID: input.MaintenanceActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func lockExpiredRepositoryExtractionWorkMaintenanceCandidates(ctx context.Context, tx sqlTx, limit int, discoveredAt time.Time) ([]repositoryExtractionWorkExpiredMaintenanceCandidate, error) {
	rows, err := tx.query(ctx, `
		SELECT
			e.request_id,
			w.work_item_id,
			w.repo_id,
			w.extractor_name,
			w.change_observation_id,
			o.observation_number,
			o.change_token,
			o.head_commit_sha,
			a.claim_id,
			a.worker_id
		FROM repository_extraction_work_claim_attempts a
		JOIN repository_extraction_work_items w
		  ON w.work_item_id = a.work_item_id
		 AND w.claim_id = a.claim_id
		 AND w.claimed_by = a.worker_id
		 AND w.claimed_at = a.claimed_at
		JOIN repository_source_streams s
		  ON s.repo_id = w.repo_id
		 AND s.extractor_name = w.extractor_name
		JOIN repository_change_observations o
		  ON o.change_observation_id = w.change_observation_id
		LEFT JOIN repository_extraction_work_execution_requests e
		  ON e.work_item_id = a.work_item_id
		 AND e.claim_id = a.claim_id
		 AND e.worker_id = a.worker_id
		WHERE a.status = 'running'
		  AND a.finished_at IS NULL
		  AND a.expired_at IS NULL
		  AND a.lease_expires_at <= $1
		  AND w.status = 'running'
		  AND w.finished_at IS NULL
		  AND w.source_generation_id IS NULL
		  AND w.failure_class IS NULL
		  AND w.failure_message IS NULL
		ORDER BY
			a.lease_expires_at,
			w.repo_id,
			w.extractor_name,
			w.work_item_id,
			a.claim_id
		LIMIT $2
		FOR UPDATE OF s SKIP LOCKED
	`, discoveredAt, limit)
	if err != nil {
		return nil, fmt.Errorf("locking expired repository extraction work maintenance candidates: %w", err)
	}

	candidates := make([]repositoryExtractionWorkExpiredMaintenanceCandidate, 0, limit)
	for rows.Next() {
		var candidate repositoryExtractionWorkExpiredMaintenanceCandidate
		var executionRequestID *string
		if err := rows.Scan(
			&executionRequestID,
			&candidate.work.WorkItemID,
			&candidate.work.RepoID,
			&candidate.work.ExtractorName,
			&candidate.work.ObservationID,
			&candidate.work.ObservationNumber,
			&candidate.work.ChangeToken,
			&candidate.work.HeadCommitSHA,
			&candidate.claimID,
			&candidate.workerID,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning expired repository extraction work maintenance candidate: %w", err)
		}
		if executionRequestID != nil {
			candidate.executionRequestID = *executionRequestID
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterating expired repository extraction work maintenance candidates: %w", err)
	}
	rows.Close()
	return candidates, nil
}

func applyRepositoryExtractionWorkExpiredMaintenanceCandidate(ctx context.Context, tx sqlTx, tick RepositoryExtractionWorkExpiredMaintenanceTickInput, candidate repositoryExtractionWorkExpiredMaintenanceCandidate, discoveredAt time.Time) (RepositoryExtractionWorkExpiredMaintenanceTransition, error) {
	if candidate.executionRequestID == "" {
		requestID, err := repositoryExtractionWorkExpiredMaintenanceRecoveryRequestID(tick.RequestID, candidate.work.WorkItemID, candidate.claimID)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		request, err := prepareRepositoryExtractionWorkRecovery(RepositoryExtractionWorkRecoveryInput{
			RequestID:       requestID,
			WorkItemID:      candidate.work.WorkItemID,
			ClaimID:         candidate.claimID,
			RecoveryActorID: tick.MaintenanceActorID,
		}, discoveredAt)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		recovery, err := recoverExpiredRepositoryExtractionWorkTx(ctx, tx, request)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		return RepositoryExtractionWorkExpiredMaintenanceTransition{
			TransitionKind: RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery,
			Recovery:       &recovery,
		}, nil
	}

	requestID, err := repositoryExtractionWorkExpiredMaintenanceExecutionRepairRequestID(tick.RequestID, candidate.executionRequestID)
	if err != nil {
		return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
	}
	request, err := prepareRepositoryExtractionWorkExecutionRepair(RepositoryExtractionWorkExecutionRepairInput{
		RequestID:          requestID,
		ExecutionRequestID: candidate.executionRequestID,
		WorkItemID:         candidate.work.WorkItemID,
		ClaimID:            candidate.claimID,
		WorkerID:           candidate.workerID,
		RepairActorID:      tick.MaintenanceActorID,
	}, discoveredAt)
	if err != nil {
		return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
	}
	repair, err := repairExpiredRepositoryExtractionWorkExecutionTx(ctx, tx, request)
	if err != nil {
		return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
	}
	return RepositoryExtractionWorkExpiredMaintenanceTransition{
		TransitionKind:  RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair,
		ExecutionRepair: &repair,
	}, nil
}

func repositoryExtractionWorkExpiredMaintenanceRecoveryRequestID(tickRequestID, workItemID, claimID string) (string, error) {
	return stableID("expired-maintenance-recover:", "repository_extraction_work_expired_maintenance_recovery", struct {
		TickRequestID string `json:"tick_request_id"`
		WorkItemID    string `json:"work_item_id"`
		ClaimID       string `json:"claim_id"`
	}{
		TickRequestID: tickRequestID,
		WorkItemID:    workItemID,
		ClaimID:       claimID,
	})
}

func repositoryExtractionWorkExpiredMaintenanceExecutionRepairRequestID(tickRequestID, executionRequestID string) (string, error) {
	return stableID("expired-maintenance-repair:", "repository_extraction_work_expired_maintenance_execution_repair", struct {
		TickRequestID      string `json:"tick_request_id"`
		ExecutionRequestID string `json:"execution_request_id"`
	}{
		TickRequestID:      tickRequestID,
		ExecutionRequestID: executionRequestID,
	})
}

func persistRepositoryExtractionWorkExpiredMaintenanceTick(ctx context.Context, tx sqlTx, request repositoryExtractionWorkExpiredMaintenanceTickRequest, result RepositoryExtractionWorkExpiredMaintenanceTickResult) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_expired_maintenance_tick_requests (
			request_id,
			maintenance_actor_id,
			result_limit,
			transition_count,
			discovered_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, request.input.RequestID, request.input.MaintenanceActorID, request.input.Limit, len(result.Transitions), result.DiscoveredAt, request.requestPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work expired maintenance tick request: %w", err)
	}
	for index, transition := range result.Transitions {
		var recoveryRequestID, executionRepairRequestID any
		switch transition.TransitionKind {
		case RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery:
			if transition.Recovery == nil || transition.ExecutionRepair != nil {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance recovery transition has inconsistent result")
			}
			recoveryRequestID = transition.Recovery.RequestID
		case RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair:
			if transition.ExecutionRepair == nil || transition.Recovery != nil {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance execution repair transition has inconsistent result")
			}
			executionRepairRequestID = transition.ExecutionRepair.RequestID
		default:
			return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance transition kind %q is invalid", transition.TransitionKind)
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_extraction_work_expired_maintenance_tick_items (
				tick_request_id,
				item_ordinal,
				transition_kind,
				recovery_request_id,
				execution_repair_request_id
			)
			VALUES ($1,$2,$3,$4,$5)
		`, request.input.RequestID, index+1, transition.TransitionKind, recoveryRequestID, executionRepairRequestID); err != nil {
			return fmt.Errorf("inserting repository extraction work expired maintenance tick item: %w", err)
		}
	}
	return nil
}

func readRepositoryExtractionWorkExpiredMaintenanceTickRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest, bool, error) {
	var persisted persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			maintenance_actor_id,
			result_limit,
			transition_count,
			discovered_at,
			request_payload_hash
		FROM repository_extraction_work_expired_maintenance_tick_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.input.RequestID,
		&persisted.input.MaintenanceActorID,
		&persisted.input.Limit,
		&persisted.transitionCount,
		&persisted.discoveredAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest{}, false, fmt.Errorf("reading repository extraction work expired maintenance tick request: %w", err)
	}
	persisted.discoveredAt = persisted.discoveredAt.UTC()
	return persisted, true, nil
}

func replayRepositoryExtractionWorkExpiredMaintenanceTick(ctx context.Context, tx sqlTx, persisted persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest) (RepositoryExtractionWorkExpiredMaintenanceTickResult, error) {
	rows, err := tx.query(ctx, `
		SELECT item_ordinal, transition_kind, recovery_request_id, execution_repair_request_id
		FROM repository_extraction_work_expired_maintenance_tick_items
		WHERE tick_request_id = $1
		ORDER BY item_ordinal
	`, persisted.input.RequestID)
	if err != nil {
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, fmt.Errorf("listing repository extraction work expired maintenance tick items: %w", err)
	}
	type durableItem struct {
		ordinal                  int
		transitionKind           string
		recoveryRequestID        *string
		executionRepairRequestID *string
	}
	items := make([]durableItem, 0, persisted.transitionCount)
	for rows.Next() {
		var item durableItem
		if err := rows.Scan(&item.ordinal, &item.transitionKind, &item.recoveryRequestID, &item.executionRepairRequestID); err != nil {
			rows.Close()
			return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, fmt.Errorf("scanning repository extraction work expired maintenance tick item: %w", err)
		}
		if item.ordinal != len(items)+1 {
			rows.Close()
			return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has non-contiguous durable items", persisted.input.RequestID)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, fmt.Errorf("iterating repository extraction work expired maintenance tick items: %w", err)
	}
	rows.Close()

	result := RepositoryExtractionWorkExpiredMaintenanceTickResult{
		RequestID:          persisted.input.RequestID,
		Limit:              persisted.input.Limit,
		MaintenanceActorID: persisted.input.MaintenanceActorID,
		DiscoveredAt:       persisted.discoveredAt,
		Transitions:        make([]RepositoryExtractionWorkExpiredMaintenanceTransition, 0, persisted.transitionCount),
		Replayed:           true,
	}
	for _, item := range items {
		transition, err := replayRepositoryExtractionWorkExpiredMaintenanceTransition(ctx, tx, persisted, item.transitionKind, item.recoveryRequestID, item.executionRepairRequestID)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, err
		}
		result.Transitions = append(result.Transitions, transition)
	}
	if len(result.Transitions) != persisted.transitionCount || persisted.transitionCount > persisted.input.Limit {
		return RepositoryExtractionWorkExpiredMaintenanceTickResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has inconsistent durable transition count", persisted.input.RequestID)
	}
	return result, nil
}

func replayRepositoryExtractionWorkExpiredMaintenanceTransition(ctx context.Context, tx sqlTx, persisted persistedRepositoryExtractionWorkExpiredMaintenanceTickRequest, transitionKind string, recoveryRequestID, executionRepairRequestID *string) (RepositoryExtractionWorkExpiredMaintenanceTransition, error) {
	switch transitionKind {
	case RepositoryExtractionWorkExpiredMaintenanceTransitionRecovery:
		if recoveryRequestID == nil || executionRepairRequestID != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has inconsistent recovery item", persisted.input.RequestID)
		}
		recovery, ok, err := readRepositoryExtractionWorkRecoveryRequest(ctx, tx, *recoveryRequestID)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		if !ok {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s is missing recovery %s", persisted.input.RequestID, *recoveryRequestID)
		}
		expectedRequestID, err := repositoryExtractionWorkExpiredMaintenanceRecoveryRequestID(persisted.input.RequestID, recovery.result.Work.WorkItemID, recovery.result.ClaimID)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		if recovery.result.RequestID != expectedRequestID || recovery.result.RecoveryActorID != persisted.input.MaintenanceActorID || !recovery.result.RecoveredAt.Equal(persisted.discoveredAt) {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has inconsistent durable recovery %s", persisted.input.RequestID, *recoveryRequestID)
		}
		recovery.result.Replayed = true
		return RepositoryExtractionWorkExpiredMaintenanceTransition{
			TransitionKind: transitionKind,
			Recovery:       &recovery.result,
		}, nil

	case RepositoryExtractionWorkExpiredMaintenanceTransitionExecutionRepair:
		if executionRepairRequestID == nil || recoveryRequestID != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has inconsistent execution repair item", persisted.input.RequestID)
		}
		repair, ok, err := readRepositoryExtractionWorkExecutionRepair(ctx, tx, *executionRepairRequestID)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		if !ok {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s is missing execution repair %s", persisted.input.RequestID, *executionRepairRequestID)
		}
		expectedRequestID, err := repositoryExtractionWorkExpiredMaintenanceExecutionRepairRequestID(persisted.input.RequestID, repair.result.ExecutionRequestID)
		if err != nil {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, err
		}
		if repair.result.RequestID != expectedRequestID || repair.result.RepairActorID != persisted.input.MaintenanceActorID || !repair.result.RepairedAt.Equal(persisted.discoveredAt) {
			return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has inconsistent durable execution repair %s", persisted.input.RequestID, *executionRepairRequestID)
		}
		repair.result.Replayed = true
		return RepositoryExtractionWorkExpiredMaintenanceTransition{
			TransitionKind:  transitionKind,
			ExecutionRepair: &repair.result,
		}, nil

	default:
		return RepositoryExtractionWorkExpiredMaintenanceTransition{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work expired maintenance tick request %s has invalid transition kind %q", persisted.input.RequestID, transitionKind)
	}
}
