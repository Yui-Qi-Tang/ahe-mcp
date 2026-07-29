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
	// RepositoryExtractionWorkOutcomeSucceeded marks work bound to a verified source generation.
	RepositoryExtractionWorkOutcomeSucceeded = "succeeded"
	// RepositoryExtractionWorkOutcomeFailed marks work with bounded failure diagnostics.
	RepositoryExtractionWorkOutcomeFailed = "failed"
	// RepositoryExtractionWorkMinLeaseDuration is the shortest accepted worker claim lease.
	RepositoryExtractionWorkMinLeaseDuration = time.Millisecond
	// RepositoryExtractionWorkMaxLeaseDuration bounds how long one claim can block recovery.
	RepositoryExtractionWorkMaxLeaseDuration = time.Hour

	repositoryExtractionWorkPending    = "pending"
	repositoryExtractionWorkRunning    = "running"
	repositoryExtractionWorkSuperseded = "superseded"
	repositoryExtractionWorkExpired    = "expired"
)

// RepositoryExtractionWork identifies one clean, stable observation for one extractor stream.
type RepositoryExtractionWork struct {
	WorkItemID        string `json:"work_item_id"`
	RepoID            string `json:"repo_id"`
	ExtractorName     string `json:"extractor_name"`
	ObservationID     string `json:"change_observation_id"`
	ObservationNumber int    `json:"observation_number"`
	ChangeToken       string `json:"change_token"`
	HeadCommitSHA     string `json:"head_commit_sha"`
}

// RepositoryExtractionWorkScheduleInput selects one stable observation request and extractor stream.
type RepositoryExtractionWorkScheduleInput struct {
	RequestID            string `json:"request_id"`
	ObservationRequestID string `json:"observation_request_id"`
	ExtractorName        string `json:"extractor_name"`
}

// RepositoryExtractionWorkScheduleResult records one idempotent pending-work decision.
type RepositoryExtractionWorkScheduleResult struct {
	RequestID            string                   `json:"request_id"`
	ObservationRequestID string                   `json:"observation_request_id"`
	Work                 RepositoryExtractionWork `json:"work"`
	SupersededWorkItemID string                   `json:"superseded_work_item_id,omitempty"`
	Created              bool                     `json:"created"`
	Coalesced            bool                     `json:"coalesced"`
	Replayed             bool                     `json:"replayed"`
}

// RepositoryExtractionWorkClaimInput selects one extractor stream for an idempotent worker claim.
type RepositoryExtractionWorkClaimInput struct {
	RequestID                 string `json:"request_id"`
	RepoID                    string `json:"repo_id"`
	ExtractorName             string `json:"extractor_name"`
	WorkerID                  string `json:"worker_id"`
	LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
}

// RepositoryExtractionWorkClaimResult reports an atomic claim or an idempotent no-work decision.
type RepositoryExtractionWorkClaimResult struct {
	RequestID      string                    `json:"request_id"`
	RepoID         string                    `json:"repo_id"`
	ExtractorName  string                    `json:"extractor_name"`
	WorkerID       string                    `json:"worker_id"`
	Claimed        bool                      `json:"claimed"`
	Work           *RepositoryExtractionWork `json:"work,omitempty"`
	ClaimID        string                    `json:"claim_id,omitempty"`
	AttemptNumber  int                       `json:"attempt_number,omitempty"`
	ClaimedAt      *time.Time                `json:"claimed_at,omitempty"`
	LeaseExpiresAt *time.Time                `json:"lease_expires_at,omitempty"`
	Replayed       bool                      `json:"replayed"`
}

type repositoryExtractionWorkRecord struct {
	work               RepositoryExtractionWork
	status             string
	claimID            string
	claimedBy          string
	claimedAt          *time.Time
	sourceGenerationID string
	finishedAt         *time.Time
	failureClass       string
	failureMessage     string
}

type persistedRepositoryExtractionWorkScheduleRequest struct {
	result             RepositoryExtractionWorkScheduleResult
	requestPayloadHash string
}

type persistedRepositoryExtractionWorkClaimRequest struct {
	result             RepositoryExtractionWorkClaimResult
	requestPayloadHash string
}

// ScheduleRepositoryExtractionWork creates or coalesces pending work for one stable clean observation.
func ScheduleRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkScheduleInput) (RepositoryExtractionWorkScheduleResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkScheduleResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return scheduleRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input)
}

func scheduleRepositoryExtractionWork(ctx context.Context, db sqlDB, input RepositoryExtractionWorkScheduleInput) (RepositoryExtractionWorkScheduleResult, error) {
	requestID, observationRequestID, extractorName, err := validateRepositoryExtractionWorkScheduleInput(input)
	if err != nil {
		return RepositoryExtractionWorkScheduleResult{}, err
	}
	payloadHash, err := repositoryExtractionWorkSchedulePayloadHash(observationRequestID, extractorName)
	if err != nil {
		return RepositoryExtractionWorkScheduleResult{}, err
	}

	var result RepositoryExtractionWorkScheduleResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-schedule", requestID); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkScheduleRequest(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != payloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work payload", requestID)
			}
			result = persisted.result
			result.Replayed = true
			return nil
		}

		observationRequest, ok, err := readGitRepositoryChangeObservationRequest(ctx, tx, observationRequestID)
		if err != nil {
			return err
		}
		if !ok {
			return newDomainError(ErrorMissingSourceViewAttempt, "Git change observation request %s was not found", observationRequestID)
		}
		observation := observationRequest.result
		if err := lockGitRepositorySourceStream(ctx, tx, observation.Inspection.RepoID); err != nil {
			return err
		}
		if err := ensureAndLockRepositorySourceStream(ctx, tx, observation.Inspection.RepoID, extractorName); err != nil {
			return err
		}
		latest, exists, err := readLatestGitRepositoryChangeWindow(ctx, tx, observation.Inspection.RepoID)
		if err != nil {
			return err
		}
		if !exists || latest.id != observation.ObservationID {
			return newDomainError(ErrorRepositoryWorkConflict, "Git change observation request %s is not the latest repository observation", observationRequestID)
		}
		if !observation.Stable {
			return newDomainError(ErrorRepositoryWorkConflict, "Git change observation request %s is not stable", observationRequestID)
		}
		if observation.Inspection.Dirty {
			return newDomainError(ErrorRepositoryWorkConflict, "dirty Git change observation %s has no immutable repository snapshot authority", observation.ObservationID)
		}

		existing, ok, err := readRepositoryExtractionWorkByObservation(ctx, tx, observation.ObservationID, extractorName)
		if err != nil {
			return err
		}
		if ok {
			result = RepositoryExtractionWorkScheduleResult{
				RequestID:            requestID,
				ObservationRequestID: observationRequestID,
				Work:                 existing.work,
				Coalesced:            true,
			}
			return persistRepositoryExtractionWorkScheduleRequest(ctx, tx, result, payloadHash)
		}

		pending, hasPending, err := readPendingRepositoryExtractionWork(ctx, tx, observation.Inspection.RepoID, extractorName)
		if err != nil {
			return err
		}
		work, err := newRepositoryExtractionWork(observation, extractorName)
		if err != nil {
			return err
		}
		var supersededWorkItemID string
		if hasPending {
			tag, err := tx.exec(ctx, `
				UPDATE repository_extraction_work_items
				SET status = $2, superseded_by_work_item_id = $3, updated_at = now()
				WHERE work_item_id = $1 AND status = $4
			`, pending.work.WorkItemID, repositoryExtractionWorkSuperseded, work.WorkItemID, repositoryExtractionWorkPending)
			if err != nil {
				return fmt.Errorf("superseding pending repository extraction work: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return newDomainError(ErrorRepositoryWorkConflict, "pending repository extraction work %s changed during scheduling", pending.work.WorkItemID)
			}
			supersededWorkItemID = pending.work.WorkItemID
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_extraction_work_items (
				work_item_id,
				repo_id,
				extractor_name,
				change_observation_id,
				status
			)
			VALUES ($1,$2,$3,$4,$5)
		`, work.WorkItemID, work.RepoID, work.ExtractorName, work.ObservationID, repositoryExtractionWorkPending); err != nil {
			return fmt.Errorf("inserting repository extraction work: %w", err)
		}
		result = RepositoryExtractionWorkScheduleResult{
			RequestID:            requestID,
			ObservationRequestID: observationRequestID,
			Work:                 work,
			SupersededWorkItemID: supersededWorkItemID,
			Created:              true,
		}
		return persistRepositoryExtractionWorkScheduleRequest(ctx, tx, result, payloadHash)
	})
	if err != nil {
		return RepositoryExtractionWorkScheduleResult{}, err
	}
	return result, nil
}

// ClaimRepositoryExtractionWork atomically claims pending work for one repository and extractor stream.
func ClaimRepositoryExtractionWork(ctx context.Context, pool *pgxpool.Pool, input RepositoryExtractionWorkClaimInput) (RepositoryExtractionWorkClaimResult, error) {
	if pool == nil {
		return RepositoryExtractionWorkClaimResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return claimRepositoryExtractionWork(ctx, pgxDB{pool: pool}, input, time.Now().UTC())
}

func claimRepositoryExtractionWork(ctx context.Context, db sqlDB, input RepositoryExtractionWorkClaimInput, claimedAt time.Time) (RepositoryExtractionWorkClaimResult, error) {
	requestID, repoID, extractorName, workerID, leaseDuration, err := validateRepositoryExtractionWorkClaimInput(input)
	if err != nil {
		return RepositoryExtractionWorkClaimResult{}, err
	}
	if claimedAt.IsZero() {
		return RepositoryExtractionWorkClaimResult{}, newDomainError(ErrorInvalidInput, "claimed_at is required")
	}
	claimedAt = claimedAt.UTC()
	leaseExpiresAt := claimedAt.Add(leaseDuration)
	payloadHash, err := repositoryExtractionWorkClaimPayloadHash(repoID, extractorName, workerID, input.LeaseDurationMilliseconds)
	if err != nil {
		return RepositoryExtractionWorkClaimResult{}, err
	}

	var result RepositoryExtractionWorkClaimResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := lockEvidenceIngestionRequest(ctx, tx, "repository-extraction-work-claim", requestID); err != nil {
			return err
		}
		if err := ensureAndLockRepositorySourceStream(ctx, tx, repoID, extractorName); err != nil {
			return err
		}
		persisted, ok, err := readRepositoryExtractionWorkClaimRequest(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != payloadHash {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different repository extraction work claim payload", requestID)
			}
			result = persisted.result
			result.Replayed = true
			return nil
		}

		result = RepositoryExtractionWorkClaimResult{
			RequestID:     requestID,
			RepoID:        repoID,
			ExtractorName: extractorName,
			WorkerID:      workerID,
		}
		running, err := hasRunningRepositoryExtractionWork(ctx, tx, repoID, extractorName)
		if err != nil {
			return err
		}
		if running {
			return persistRepositoryExtractionWorkClaimRequest(ctx, tx, result, payloadHash)
		}
		pending, ok, err := readPendingRepositoryExtractionWork(ctx, tx, repoID, extractorName)
		if err != nil {
			return err
		}
		if !ok {
			return persistRepositoryExtractionWorkClaimRequest(ctx, tx, result, payloadHash)
		}
		claimID, err := stableID("work-claim:", "repository_extraction_work_claim", struct {
			RequestID  string `json:"request_id"`
			WorkItemID string `json:"work_item_id"`
			WorkerID   string `json:"worker_id"`
		}{
			RequestID:  requestID,
			WorkItemID: pending.work.WorkItemID,
			WorkerID:   workerID,
		})
		if err != nil {
			return err
		}
		attemptNumber, err := nextRepositoryExtractionWorkClaimAttemptNumber(ctx, tx, pending.work.WorkItemID)
		if err != nil {
			return err
		}
		tag, err := tx.exec(ctx, `
			UPDATE repository_extraction_work_items
			SET status = $2, claim_id = $3, claimed_by = $4, claimed_at = $5, updated_at = now()
			WHERE work_item_id = $1 AND status = $6
		`, pending.work.WorkItemID, repositoryExtractionWorkRunning, claimID, workerID, claimedAt, repositoryExtractionWorkPending)
		if err != nil {
			return fmt.Errorf("claiming repository extraction work: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return newDomainError(ErrorRepositoryWorkConflict, "pending repository extraction work %s changed during claim", pending.work.WorkItemID)
		}
		attempt := repositoryExtractionWorkClaimAttempt{
			claimID:                   claimID,
			workItemID:                pending.work.WorkItemID,
			attemptNumber:             attemptNumber,
			workerID:                  workerID,
			status:                    repositoryExtractionWorkRunning,
			claimedAt:                 claimedAt,
			leaseDurationMilliseconds: input.LeaseDurationMilliseconds,
			leaseExpiresAt:            leaseExpiresAt,
		}
		if err := persistRepositoryExtractionWorkClaimAttempt(ctx, tx, attempt); err != nil {
			return err
		}
		result.Claimed = true
		result.Work = &pending.work
		result.ClaimID = claimID
		result.AttemptNumber = attemptNumber
		result.ClaimedAt = &claimedAt
		result.LeaseExpiresAt = &leaseExpiresAt
		return persistRepositoryExtractionWorkClaimRequest(ctx, tx, result, payloadHash)
	})
	if err != nil {
		return RepositoryExtractionWorkClaimResult{}, err
	}
	return result, nil
}

func validateRepositoryExtractionWorkScheduleInput(input RepositoryExtractionWorkScheduleInput) (string, string, string, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return "", "", "", newDomainError(ErrorInvalidInput, "repository extraction work request_id is required")
	}
	observationRequestID := strings.TrimSpace(input.ObservationRequestID)
	if observationRequestID == "" {
		return "", "", "", newDomainError(ErrorInvalidInput, "observation_request_id is required")
	}
	extractorName, err := validateRepositoryExtractionWorkExtractor(input.ExtractorName)
	if err != nil {
		return "", "", "", err
	}
	return requestID, observationRequestID, extractorName, nil
}

func validateRepositoryExtractionWorkClaimInput(input RepositoryExtractionWorkClaimInput) (string, string, string, string, time.Duration, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return "", "", "", "", 0, newDomainError(ErrorInvalidInput, "repository extraction work claim request_id is required")
	}
	repoID := strings.TrimSpace(input.RepoID)
	if repoID == "" {
		return "", "", "", "", 0, newDomainError(ErrorInvalidInput, "repository extraction work claim repo_id is required")
	}
	extractorName, err := validateRepositoryExtractionWorkExtractor(input.ExtractorName)
	if err != nil {
		return "", "", "", "", 0, err
	}
	workerID := strings.TrimSpace(input.WorkerID)
	if workerID == "" || len(workerID) > 200 {
		return "", "", "", "", 0, newDomainError(ErrorInvalidInput, "repository extraction work worker_id must contain 1 to 200 bytes")
	}
	minLeaseMilliseconds := RepositoryExtractionWorkMinLeaseDuration.Milliseconds()
	maxLeaseMilliseconds := RepositoryExtractionWorkMaxLeaseDuration.Milliseconds()
	if input.LeaseDurationMilliseconds < minLeaseMilliseconds || input.LeaseDurationMilliseconds > maxLeaseMilliseconds {
		return "", "", "", "", 0, newDomainError(
			ErrorInvalidInput,
			"repository extraction work lease duration must be between %d and %d milliseconds",
			minLeaseMilliseconds,
			maxLeaseMilliseconds,
		)
	}
	leaseDuration := time.Duration(input.LeaseDurationMilliseconds) * time.Millisecond
	return requestID, repoID, extractorName, workerID, leaseDuration, nil
}

func validateRepositoryExtractionWorkExtractor(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case ExtractorRepositoryGoParserCodeFact, ExtractorRepositoryGoplsCodeFact:
		return value, nil
	default:
		return "", newDomainError(ErrorInvalidInput, "unsupported repository extraction work extractor %q", value)
	}
}

func repositoryExtractionWorkSchedulePayloadHash(observationRequestID, extractorName string) (string, error) {
	payload, err := deterministicJSON(struct {
		ObservationRequestID string `json:"observation_request_id"`
		ExtractorName        string `json:"extractor_name"`
	}{
		ObservationRequestID: observationRequestID,
		ExtractorName:        extractorName,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func repositoryExtractionWorkClaimPayloadHash(repoID, extractorName, workerID string, leaseDurationMilliseconds int64) (string, error) {
	payload, err := deterministicJSON(struct {
		RepoID                    string `json:"repo_id"`
		ExtractorName             string `json:"extractor_name"`
		WorkerID                  string `json:"worker_id"`
		LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
	}{
		RepoID:                    repoID,
		ExtractorName:             extractorName,
		WorkerID:                  workerID,
		LeaseDurationMilliseconds: leaseDurationMilliseconds,
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func newRepositoryExtractionWork(observation GitRepositoryChangeObservationResult, extractorName string) (RepositoryExtractionWork, error) {
	workItemID, err := stableID("repo-work:", "repository_extraction_work", struct {
		ObservationID string `json:"change_observation_id"`
		ExtractorName string `json:"extractor_name"`
	}{
		ObservationID: observation.ObservationID,
		ExtractorName: extractorName,
	})
	if err != nil {
		return RepositoryExtractionWork{}, err
	}
	return RepositoryExtractionWork{
		WorkItemID:        workItemID,
		RepoID:            observation.Inspection.RepoID,
		ExtractorName:     extractorName,
		ObservationID:     observation.ObservationID,
		ObservationNumber: observation.ObservationNumber,
		ChangeToken:       observation.Inspection.ChangeToken,
		HeadCommitSHA:     observation.Inspection.HeadCommitSHA,
	}, nil
}

func readRepositoryExtractionWorkByObservation(ctx context.Context, tx sqlTx, observationID, extractorName string) (repositoryExtractionWorkRecord, bool, error) {
	record, err := scanRepositoryExtractionWork(tx.queryRow(ctx, repositoryExtractionWorkSelect+`
		WHERE w.change_observation_id = $1 AND w.extractor_name = $2
	`, observationID, extractorName))
	if errors.Is(err, pgx.ErrNoRows) {
		return repositoryExtractionWorkRecord{}, false, nil
	}
	if err != nil {
		return repositoryExtractionWorkRecord{}, false, fmt.Errorf("reading repository extraction work by observation: %w", err)
	}
	return record, true, nil
}

func readPendingRepositoryExtractionWork(ctx context.Context, tx sqlTx, repoID, extractorName string) (repositoryExtractionWorkRecord, bool, error) {
	record, err := scanRepositoryExtractionWork(tx.queryRow(ctx, repositoryExtractionWorkSelect+`
		WHERE w.repo_id = $1 AND w.extractor_name = $2 AND w.status = $3
		ORDER BY w.created_at, w.work_item_id
		LIMIT 1
		FOR UPDATE OF w SKIP LOCKED
	`, repoID, extractorName, repositoryExtractionWorkPending))
	if errors.Is(err, pgx.ErrNoRows) {
		return repositoryExtractionWorkRecord{}, false, nil
	}
	if err != nil {
		return repositoryExtractionWorkRecord{}, false, fmt.Errorf("reading pending repository extraction work: %w", err)
	}
	return record, true, nil
}

const repositoryExtractionWorkSelect = `
	SELECT
		w.work_item_id,
		w.repo_id,
		w.extractor_name,
		w.change_observation_id,
		o.observation_number,
		o.change_token,
		o.head_commit_sha,
		w.status,
		COALESCE(w.claim_id, ''),
		COALESCE(w.claimed_by, ''),
		w.claimed_at,
		COALESCE(w.source_generation_id, ''),
		w.finished_at,
		COALESCE(w.failure_class, ''),
		COALESCE(w.failure_message, '')
	FROM repository_extraction_work_items w
	JOIN repository_change_observations o
	  ON o.change_observation_id = w.change_observation_id
`

func scanRepositoryExtractionWork(row sqlRow) (repositoryExtractionWorkRecord, error) {
	var record repositoryExtractionWorkRecord
	err := row.Scan(
		&record.work.WorkItemID,
		&record.work.RepoID,
		&record.work.ExtractorName,
		&record.work.ObservationID,
		&record.work.ObservationNumber,
		&record.work.ChangeToken,
		&record.work.HeadCommitSHA,
		&record.status,
		&record.claimID,
		&record.claimedBy,
		&record.claimedAt,
		&record.sourceGenerationID,
		&record.finishedAt,
		&record.failureClass,
		&record.failureMessage,
	)
	return record, err
}

func hasRunningRepositoryExtractionWork(ctx context.Context, tx sqlTx, repoID, extractorName string) (bool, error) {
	var found int
	err := tx.queryRow(ctx, `
		SELECT 1
		FROM repository_extraction_work_items
		WHERE repo_id = $1 AND extractor_name = $2 AND status = $3
		LIMIT 1
	`, repoID, extractorName, repositoryExtractionWorkRunning).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking running repository extraction work: %w", err)
	}
	return true, nil
}

func persistRepositoryExtractionWorkScheduleRequest(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkScheduleResult, payloadHash string) error {
	var superseded any
	if result.SupersededWorkItemID != "" {
		superseded = result.SupersededWorkItemID
	}
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_schedule_requests (
			request_id,
			observation_request_id,
			work_item_id,
			repo_id,
			extractor_name,
			superseded_work_item_id,
			request_payload_hash,
			created,
			coalesced
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, result.RequestID, result.ObservationRequestID, result.Work.WorkItemID, result.Work.RepoID, result.Work.ExtractorName, superseded, payloadHash, result.Created, result.Coalesced)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work schedule request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkScheduleRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkScheduleRequest, bool, error) {
	var request persistedRepositoryExtractionWorkScheduleRequest
	var workItemID string
	var superseded *string
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			observation_request_id,
			work_item_id,
			superseded_work_item_id,
			created,
			coalesced,
			request_payload_hash
		FROM repository_extraction_work_schedule_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&request.result.RequestID,
		&request.result.ObservationRequestID,
		&workItemID,
		&superseded,
		&request.result.Created,
		&request.result.Coalesced,
		&request.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkScheduleRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkScheduleRequest{}, false, fmt.Errorf("reading repository extraction work schedule request: %w", err)
	}
	work, err := readRepositoryExtractionWorkByID(ctx, tx, workItemID)
	if err != nil {
		return persistedRepositoryExtractionWorkScheduleRequest{}, false, err
	}
	request.result.Work = work.work
	if superseded != nil {
		request.result.SupersededWorkItemID = *superseded
	}
	return request, true, nil
}

func readRepositoryExtractionWorkByID(ctx context.Context, tx sqlTx, workItemID string) (repositoryExtractionWorkRecord, error) {
	record, err := scanRepositoryExtractionWork(tx.queryRow(ctx, repositoryExtractionWorkSelect+`
		WHERE w.work_item_id = $1
	`, workItemID))
	if err != nil {
		return repositoryExtractionWorkRecord{}, fmt.Errorf("reading repository extraction work %s: %w", workItemID, err)
	}
	return record, nil
}

func persistRepositoryExtractionWorkClaimRequest(ctx context.Context, tx sqlTx, result RepositoryExtractionWorkClaimResult, payloadHash string) error {
	var workItemID, claimID, claimedAt any
	if result.Claimed {
		workItemID = result.Work.WorkItemID
		claimID = result.ClaimID
		claimedAt = *result.ClaimedAt
	}
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_claim_requests (
			request_id,
			repo_id,
			extractor_name,
			worker_id,
			work_item_id,
			claim_id,
			claimed_at,
			claimed,
			request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, result.RequestID, result.RepoID, result.ExtractorName, result.WorkerID, workItemID, claimID, claimedAt, result.Claimed, payloadHash)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work claim request: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkClaimRequest(ctx context.Context, tx sqlTx, requestID string) (persistedRepositoryExtractionWorkClaimRequest, bool, error) {
	var request persistedRepositoryExtractionWorkClaimRequest
	var workItemID, claimID *string
	var claimedAt *time.Time
	err := tx.queryRow(ctx, `
		SELECT
			request_id,
			repo_id,
			extractor_name,
			worker_id,
			work_item_id,
			claim_id,
			claimed_at,
			claimed,
			request_payload_hash
		FROM repository_extraction_work_claim_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&request.result.RequestID,
		&request.result.RepoID,
		&request.result.ExtractorName,
		&request.result.WorkerID,
		&workItemID,
		&claimID,
		&claimedAt,
		&request.result.Claimed,
		&request.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedRepositoryExtractionWorkClaimRequest{}, false, nil
	}
	if err != nil {
		return persistedRepositoryExtractionWorkClaimRequest{}, false, fmt.Errorf("reading repository extraction work claim request: %w", err)
	}
	if request.result.Claimed {
		if workItemID == nil || claimID == nil || claimedAt == nil {
			return persistedRepositoryExtractionWorkClaimRequest{}, false, newDomainError(ErrorRepositoryWorkConflict, "claimed repository extraction work request %s is incomplete", requestID)
		}
		work, err := readRepositoryExtractionWorkByID(ctx, tx, *workItemID)
		if err != nil {
			return persistedRepositoryExtractionWorkClaimRequest{}, false, err
		}
		attempt, err := readRepositoryExtractionWorkClaimAttempt(ctx, tx, *claimID)
		if err != nil {
			return persistedRepositoryExtractionWorkClaimRequest{}, false, err
		}
		if attempt.workItemID != *workItemID || attempt.workerID != request.result.WorkerID || !attempt.claimedAt.Equal(claimedAt.UTC()) {
			return persistedRepositoryExtractionWorkClaimRequest{}, false, newDomainError(ErrorRepositoryWorkConflict, "claimed repository extraction work request %s does not match claim attempt", requestID)
		}
		request.result.Work = &work.work
		request.result.ClaimID = *claimID
		request.result.AttemptNumber = attempt.attemptNumber
		claimedUTC := attempt.claimedAt.UTC()
		leaseUTC := attempt.leaseExpiresAt.UTC()
		request.result.ClaimedAt = &claimedUTC
		request.result.LeaseExpiresAt = &leaseUTC
	}
	return request, true, nil
}
