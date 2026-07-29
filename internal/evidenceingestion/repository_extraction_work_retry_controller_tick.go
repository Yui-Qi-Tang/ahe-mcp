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
	// RepositoryExtractionWorkRetryControllerTickMaxLimit bounds one due-retry controller transaction.
	RepositoryExtractionWorkRetryControllerTickMaxLimit = RepositoryExtractionWorkDueRetryDecisionMaxLimit
)

// RepositoryExtractionWorkRetryControllerTickInput identifies one bounded due-retry controller run.
type RepositoryExtractionWorkRetryControllerTickInput struct {
	RequestID       string `json:"request_id"`
	Limit           int    `json:"limit"`
	ConsumerActorID string `json:"consumer_actor_id"`
}

// RepositoryExtractionWorkRetryControllerTickResult reports one durable bounded batch.
type RepositoryExtractionWorkRetryControllerTickResult struct {
	RequestID       string                                                   `json:"request_id"`
	Limit           int                                                      `json:"limit"`
	ConsumerActorID string                                                   `json:"consumer_actor_id"`
	DiscoveredAt    time.Time                                                `json:"discovered_at"`
	Consumptions    []RepositoryExtractionWorkRetryDecisionConsumptionResult `json:"consumptions"`
	Replayed        bool                                                     `json:"replayed"`
}

type repositoryExtractionWorkRetryControllerTickRequest struct {
	input              RepositoryExtractionWorkRetryControllerTickInput
	requestPayloadHash string
}

type persistedRepositoryExtractionWorkRetryControllerTickRequest struct {
	input              RepositoryExtractionWorkRetryControllerTickInput
	consumptionCount   int
	discoveredAt       time.Time
	requestPayloadHash string
}

// RunRepositoryExtractionWorkRetryControllerTick consumes one bounded batch of due retry decisions.
func RunRepositoryExtractionWorkRetryControllerTick(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkRetryControllerTickInput) (RepositoryExtractionWorkRetryControllerTickResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkRetryControllerTickResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return runRepositoryExtractionWorkRetryControllerTick(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func runRepositoryExtractionWorkRetryControllerTick(ctx context.Context, db sqlDB, input RepositoryExtractionWorkRetryControllerTickInput, discoveredAt time.Time) (RepositoryExtractionWorkRetryControllerTickResult, error) {
	request, err := prepareRepositoryExtractionWorkRetryControllerTick(input)
	if err != nil {
		return RepositoryExtractionWorkRetryControllerTickResult{}, err
	}
	if discoveredAt.IsZero() {
		return RepositoryExtractionWorkRetryControllerTickResult{}, newDomainError(ErrorInvalidInput, "repository extraction work retry controller discovery time is required")
	}
	discoveredAt = discoveredAt.UTC()

	var result RepositoryExtractionWorkRetryControllerTickResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-retry-controller-tick", request.input.RequestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkRetryControllerTickRequest(ctx, tx, request.input.RequestID)
		if err != nil {
			return err
		}
		if ok {
			persistedPayloadHash, err := repositoryExtractionWorkRetryControllerTickPayloadHash(persisted.input)
			if err != nil {
				return err
			}
			if persisted.requestPayloadHash != persistedPayloadHash {
				return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry controller tick request %s has inconsistent durable identity", request.input.RequestID)
			}
			if persisted.requestPayloadHash != request.requestPayloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work retry controller tick payload", request.input.RequestID)
			}
			result, err = replayRepositoryExtractionWorkRetryControllerTick(ctx, tx, persisted)
			return err
		}

		decisions, err := lockDueRepositoryExtractionWorkRetryDecisions(ctx, tx, RepositoryExtractionWorkDueRetryDecisionListInput{Limit: request.input.Limit}, discoveredAt)
		if err != nil {
			return err
		}
		result = RepositoryExtractionWorkRetryControllerTickResult{
			RequestID:       request.input.RequestID,
			Limit:           request.input.Limit,
			ConsumerActorID: request.input.ConsumerActorID,
			DiscoveredAt:    discoveredAt,
			Consumptions:    make([]RepositoryExtractionWorkRetryDecisionConsumptionResult, 0, len(decisions)),
		}
		for _, decision := range decisions {
			consumptionRequestID, err := repositoryExtractionWorkRetryControllerTickChildRequestID(request.input.RequestID, decision.FailurePolicyRequestID)
			if err != nil {
				return err
			}
			consumptionRequest, err := prepareRepositoryExtractionWorkRetryDecisionConsumption(RepositoryExtractionWorkRetryDecisionConsumptionInput{
				RequestID:              consumptionRequestID,
				FailurePolicyRequestID: decision.FailurePolicyRequestID,
				WorkItemID:             decision.Work.WorkItemID,
				ClaimID:                decision.ClaimID,
				ConsumerActorID:        request.input.ConsumerActorID,
			}, discoveredAt)
			if err != nil {
				return err
			}
			consumption, err := consumeDueRepositoryExtractionWorkRetryDecisionTx(ctx, tx, consumptionRequest)
			if err != nil {
				return err
			}
			result.Consumptions = append(result.Consumptions, consumption)
		}
		return persistRepositoryExtractionWorkRetryControllerTick(ctx, tx, request, result)
	})
	if err != nil {
		return RepositoryExtractionWorkRetryControllerTickResult{}, err
	}
	return result, nil
}

func prepareRepositoryExtractionWorkRetryControllerTick(input RepositoryExtractionWorkRetryControllerTickInput) (repositoryExtractionWorkRetryControllerTickRequest, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.ConsumerActorID = strings.TrimSpace(input.ConsumerActorID)
	if input.RequestID == "" {
		return repositoryExtractionWorkRetryControllerTickRequest{}, newDomainError(ErrorInvalidInput, "repository extraction work retry controller tick request_id is required")
	}
	if input.ConsumerActorID == "" || len(input.ConsumerActorID) > 200 {
		return repositoryExtractionWorkRetryControllerTickRequest{}, newDomainError(ErrorInvalidInput, "repository extraction work retry controller consumer_actor_id must contain 1 to 200 bytes")
	}
	if err := validateRepositoryExtractionWorkDueRetryDecisionListInput(RepositoryExtractionWorkDueRetryDecisionListInput{Limit: input.Limit}); err != nil {
		return repositoryExtractionWorkRetryControllerTickRequest{}, err
	}
	payloadHash, err := repositoryExtractionWorkRetryControllerTickPayloadHash(input)
	if err != nil {
		return repositoryExtractionWorkRetryControllerTickRequest{}, err
	}
	return repositoryExtractionWorkRetryControllerTickRequest{input: input, requestPayloadHash: payloadHash}, nil
}

func repositoryExtractionWorkRetryControllerTickPayloadHash(input RepositoryExtractionWorkRetryControllerTickInput) (string, error) {
	payload, err := deterministicJSON(struct {
		Limit           int    `json:"limit"`
		ConsumerActorID string `json:"consumer_actor_id"`
	}{
		Limit:           input.Limit,
		ConsumerActorID: input.ConsumerActorID,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func repositoryExtractionWorkRetryControllerTickChildRequestID(tickRequestID, failurePolicyRequestID string) (string, error) {
	return stableID("retry-controller-consume:", "repository_extraction_work_retry_controller_tick_consumption", struct {
		TickRequestID          string `json:"tick_request_id"`
		FailurePolicyRequestID string `json:"failure_policy_request_id"`
	}{
		TickRequestID:          tickRequestID,
		FailurePolicyRequestID: failurePolicyRequestID,
	})
}

func persistRepositoryExtractionWorkRetryControllerTick(ctx context.Context, tx sqlTx, request repositoryExtractionWorkRetryControllerTickRequest, result RepositoryExtractionWorkRetryControllerTickResult) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_retry_controller_tick_requests (
			request_id,
			consumer_actor_id,
			result_limit,
			consumption_count,
			discovered_at,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, request.input.RequestID, request.input.ConsumerActorID, request.input.Limit, len(result.Consumptions), result.DiscoveredAt, request.requestPayloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work retry controller tick request: %w", err)
	}
	for index, consumption := range result.Consumptions {
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_extraction_work_retry_controller_tick_items (
				tick_request_id,
				item_ordinal,
				consumption_request_id
			)
			VALUES ($1,$2,$3)
		`, request.input.RequestID, index+1, consumption.RequestID); err != nil {
			return fmt.Errorf("inserting repository extraction work retry controller tick item: %w", err)
		}
	}
	return nil
}

func readRepositoryExtractionWorkRetryControllerTickRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkRetryControllerTickRequest, bool, error) {
	var persisted persistedRepositoryExtractionWorkRetryControllerTickRequest
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			consumer_actor_id,
			result_limit,
			consumption_count,
			discovered_at,
			request_payload_hash
		FROM repository_extraction_work_retry_controller_tick_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&persisted.input.RequestID,
		&persisted.input.ConsumerActorID,
		&persisted.input.Limit,
		&persisted.consumptionCount,
		&persisted.discoveredAt,
		&persisted.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkRetryControllerTickRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkRetryControllerTickRequest{}, false, fmt.Errorf("reading repository extraction work retry controller tick request: %w", err)
	}
	persisted.discoveredAt = persisted.discoveredAt.UTC()
	return persisted, true, nil
}

func replayRepositoryExtractionWorkRetryControllerTick(ctx context.Context, tx sqlTx, persisted persistedRepositoryExtractionWorkRetryControllerTickRequest) (RepositoryExtractionWorkRetryControllerTickResult, error) {
	rows, err := tx.query(ctx, `
		SELECT item_ordinal, consumption_request_id
		FROM repository_extraction_work_retry_controller_tick_items
		WHERE tick_request_id = $1
		ORDER BY item_ordinal
	`, persisted.input.RequestID)
	if err != nil {
		return RepositoryExtractionWorkRetryControllerTickResult{}, fmt.Errorf("listing repository extraction work retry controller tick items: %w", err)
	}
	type durableItem struct {
		ordinal              int
		consumptionRequestID string
	}
	items := make([]durableItem, 0, persisted.consumptionCount)
	for rows.Next() {
		var item durableItem
		if err := rows.Scan(&item.ordinal, &item.consumptionRequestID); err != nil {
			rows.Close()
			return RepositoryExtractionWorkRetryControllerTickResult{}, fmt.Errorf("scanning repository extraction work retry controller tick item: %w", err)
		}
		if item.ordinal != len(items)+1 {
			rows.Close()
			return RepositoryExtractionWorkRetryControllerTickResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry controller tick request %s has non-contiguous durable items", persisted.input.RequestID)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RepositoryExtractionWorkRetryControllerTickResult{}, fmt.Errorf("iterating repository extraction work retry controller tick items: %w", err)
	}
	rows.Close()

	result := RepositoryExtractionWorkRetryControllerTickResult{
		RequestID:       persisted.input.RequestID,
		Limit:           persisted.input.Limit,
		ConsumerActorID: persisted.input.ConsumerActorID,
		DiscoveredAt:    persisted.discoveredAt,
		Consumptions:    make([]RepositoryExtractionWorkRetryDecisionConsumptionResult, 0, persisted.consumptionCount),
		Replayed:        true,
	}
	for _, item := range items {
		consumption, ok, err := readRepositoryExtractionWorkRetryDecisionConsumption(ctx, tx, item.consumptionRequestID)
		if err != nil {
			return RepositoryExtractionWorkRetryControllerTickResult{}, err
		}
		if !ok {
			return RepositoryExtractionWorkRetryControllerTickResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry controller tick request %s is missing consumption %s", persisted.input.RequestID, item.consumptionRequestID)
		}
		expectedRequestID, err := repositoryExtractionWorkRetryControllerTickChildRequestID(persisted.input.RequestID, consumption.result.FailurePolicyRequestID)
		if err != nil {
			return RepositoryExtractionWorkRetryControllerTickResult{}, err
		}
		if consumption.result.RequestID != expectedRequestID ||
			consumption.result.ConsumerActorID != persisted.input.ConsumerActorID ||
			!consumption.result.ConsumedAt.Equal(persisted.discoveredAt) {
			return RepositoryExtractionWorkRetryControllerTickResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry controller tick request %s has inconsistent durable consumption %s", persisted.input.RequestID, item.consumptionRequestID)
		}
		consumption.result.Replayed = true
		result.Consumptions = append(result.Consumptions, consumption.result)
	}
	if len(result.Consumptions) != persisted.consumptionCount || persisted.consumptionCount > persisted.input.Limit {
		return RepositoryExtractionWorkRetryControllerTickResult{}, newDomainError(ErrorRepositoryWorkConflict, "repository extraction work retry controller tick request %s has inconsistent durable consumption count", persisted.input.RequestID)
	}
	return result, nil
}
