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

// GitRepositoryChangeMaxStabilityWindow bounds caller-owned scheduling policy.
const GitRepositoryChangeMaxStabilityWindow = time.Hour

// GitRepositoryChangeObservationConfig identifies one persisted Git observation request.
type GitRepositoryChangeObservationConfig struct {
	GitBinaryPath   string
	WorkspaceRoot   string
	RepoID          string
	RequestID       string
	StabilityWindow time.Duration
	Timeout         time.Duration
}

// GitRepositoryChangeObservationResult reports the state produced by one idempotent observation request.
type GitRepositoryChangeObservationResult struct {
	RequestID             string                        `json:"request_id"`
	ObservationID         string                        `json:"change_observation_id"`
	ObservationNumber     int                           `json:"observation_number"`
	PreviousObservationID string                        `json:"previous_change_observation_id,omitempty"`
	Inspection            GitRepositoryChangeInspection `json:"inspection"`
	FirstObservedAt       time.Time                     `json:"first_observed_at"`
	LastObservedAt        time.Time                     `json:"last_observed_at"`
	ObservationCount      int                           `json:"observation_count"`
	StabilityWindowMillis int64                         `json:"stability_window_milliseconds"`
	StableAfter           time.Time                     `json:"stable_after"`
	Changed               bool                          `json:"changed"`
	Coalesced             bool                          `json:"coalesced"`
	Stable                bool                          `json:"stable"`
	Replayed              bool                          `json:"replayed"`
}

type gitRepositoryChangeObservationInput struct {
	RequestID       string
	StabilityWindow time.Duration
	Inspection      GitRepositoryChangeInspection
}

type gitRepositoryChangeWindow struct {
	id                    string
	repoID                string
	number                int
	previousObservationID string
	inspection            GitRepositoryChangeInspection
	firstObservedAt       time.Time
	lastObservedAt        time.Time
	observationCount      int
}

type persistedGitRepositoryChangeObservationRequest struct {
	result             GitRepositoryChangeObservationResult
	requestPayloadHash string
}

// ObserveGitRepositoryChange inspects trusted Git state and persists one coalesced stability-window observation.
func ObserveGitRepositoryChange(ctx context.Context, pool *pgxpool.Pool, config GitRepositoryChangeObservationConfig) (GitRepositoryChangeObservationResult, error) {
	if pool == nil {
		return GitRepositoryChangeObservationResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	requestID, stabilityWindow, err := validateGitRepositoryChangeObservationRequest(config.RequestID, config.StabilityWindow)
	if err != nil {
		return GitRepositoryChangeObservationResult{}, err
	}
	inspection, err := InspectGitRepositoryChange(ctx, GitRepositoryChangeConfig{
		GitBinaryPath: config.GitBinaryPath,
		WorkspaceRoot: config.WorkspaceRoot,
		RepoID:        config.RepoID,
		Timeout:       config.Timeout,
	})
	if err != nil {
		return GitRepositoryChangeObservationResult{}, err
	}
	return recordGitRepositoryChangeObservation(ctx, pgxDB{pool: pool}, gitRepositoryChangeObservationInput{
		RequestID:       requestID,
		StabilityWindow: stabilityWindow,
		Inspection:      inspection,
	}, time.Now().UTC())
}

func recordGitRepositoryChangeObservation(ctx context.Context, db sqlDB, input gitRepositoryChangeObservationInput, observedAt time.Time) (GitRepositoryChangeObservationResult, error) {
	requestID, stabilityWindow, err := validateGitRepositoryChangeObservationRequest(input.RequestID, input.StabilityWindow)
	if err != nil {
		return GitRepositoryChangeObservationResult{}, err
	}
	inspection, err := validateGitRepositoryChangeInspection(input.Inspection)
	if err != nil {
		return GitRepositoryChangeObservationResult{}, err
	}
	if observedAt.IsZero() {
		return GitRepositoryChangeObservationResult{}, newDomainError(ErrorInvalidInput, "observed_at is required")
	}
	observedAt = observedAt.UTC()
	payloadHash, err := gitRepositoryChangeObservationPayloadHash(inspection, stabilityWindow)
	if err != nil {
		return GitRepositoryChangeObservationResult{}, err
	}

	var result GitRepositoryChangeObservationResult
	err = withTx(ctx, db, func(tx sqlTx) error {
		if err := ensureAndLockGitRepositorySourceStream(ctx, tx, inspection.RepoID); err != nil {
			return err
		}
		if err := lockEvidenceIngestionRequest(ctx, tx, "git-repository-change-observation", requestID); err != nil {
			return err
		}

		persisted, ok, err := readGitRepositoryChangeObservationRequest(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if ok {
			if persisted.requestPayloadHash != payloadHash || persisted.result.Inspection.RepoID != inspection.RepoID {
				return newDomainError(ErrorIdempotencyKeyReused, "request_id %s already exists with different Git change observation payload", requestID)
			}
			result = persisted.result
			result.Replayed = true
			return nil
		}

		current, exists, err := readLatestGitRepositoryChangeWindow(ctx, tx, inspection.RepoID)
		if err != nil {
			return err
		}
		if exists && observedAt.Before(current.lastObservedAt) {
			return newDomainError(ErrorInvalidInput, "observed_at precedes the latest observation for repo_id %s", inspection.RepoID)
		}

		changed := !exists || current.inspection.ChangeToken != inspection.ChangeToken
		coalesced := !changed
		if coalesced {
			if !sameGitRepositoryChangeInspection(current.inspection, inspection) {
				return newDomainError(ErrorChangeObservationConflict, "change token %s maps to different Git observation material", inspection.ChangeToken)
			}
			current.lastObservedAt = observedAt
			current.observationCount++
			if _, err := tx.exec(ctx, `
				UPDATE repository_change_observations
				SET last_observed_at = $2, observation_count = $3, updated_at = now()
				WHERE change_observation_id = $1
			`, current.id, current.lastObservedAt, current.observationCount); err != nil {
				return fmt.Errorf("coalescing repository change observation: %w", err)
			}
		} else {
			current, err = insertGitRepositoryChangeWindow(ctx, tx, inspection, current, exists, observedAt)
			if err != nil {
				return err
			}
		}

		stableAfter := current.firstObservedAt.Add(stabilityWindow)
		stable := coalesced && current.observationCount >= 2 && !observedAt.Before(stableAfter)
		result = GitRepositoryChangeObservationResult{
			RequestID:             requestID,
			ObservationID:         current.id,
			ObservationNumber:     current.number,
			PreviousObservationID: current.previousObservationID,
			Inspection:            current.inspection,
			FirstObservedAt:       current.firstObservedAt,
			LastObservedAt:        observedAt,
			ObservationCount:      current.observationCount,
			StabilityWindowMillis: stabilityWindow.Milliseconds(),
			StableAfter:           stableAfter,
			Changed:               changed,
			Coalesced:             coalesced,
			Stable:                stable,
		}
		if _, err := tx.exec(ctx, `
			INSERT INTO repository_change_observation_requests (
				request_id,
				change_observation_id,
				repo_id,
				request_payload_hash,
				observed_at,
				stability_window_milliseconds,
				observation_count,
				changed,
				coalesced,
				stable_after,
				stable
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, result.RequestID, result.ObservationID, result.Inspection.RepoID, payloadHash, result.LastObservedAt, result.StabilityWindowMillis, result.ObservationCount, result.Changed, result.Coalesced, result.StableAfter, result.Stable); err != nil {
			return fmt.Errorf("inserting repository change observation request: %w", err)
		}
		return nil
	})
	if err != nil {
		return GitRepositoryChangeObservationResult{}, err
	}
	return result, nil
}

func validateGitRepositoryChangeObservationRequest(requestID string, stabilityWindow time.Duration) (string, time.Duration, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", 0, newDomainError(ErrorInvalidInput, "Git change observation request_id is required")
	}
	if stabilityWindow < time.Millisecond || stabilityWindow > GitRepositoryChangeMaxStabilityWindow || stabilityWindow%time.Millisecond != 0 {
		return "", 0, newDomainError(ErrorInvalidInput, "Git change stability window must be a whole millisecond between 1 and %d", GitRepositoryChangeMaxStabilityWindow.Milliseconds())
	}
	return requestID, stabilityWindow, nil
}

func validateGitRepositoryChangeInspection(inspection GitRepositoryChangeInspection) (GitRepositoryChangeInspection, error) {
	inspection.RepoID = strings.TrimSpace(inspection.RepoID)
	if inspection.RepoID == "" {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "Git change observation repo_id is required")
	}
	if inspection.TokenContract != GitRepositoryChangeTokenV1 {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "unsupported Git change token contract %q", inspection.TokenContract)
	}
	if inspection.DirtyFingerprintContract != GitRepositoryDirtyFingerprintV1 {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "unsupported Git dirty fingerprint contract %q", inspection.DirtyFingerprintContract)
	}
	if !isCanonicalGitObjectID(inspection.HeadCommitSHA) {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "Git change observation HEAD must be a lowercase full SHA")
	}
	if inspection.TrackedChangeCount < 0 || inspection.UntrackedFileCount < 0 {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "Git change observation counts must be non-negative")
	}
	switch {
	case inspection.Dirty:
		if !isSHA256ContentHash(inspection.DirtyFingerprint) || inspection.TrackedChangeCount+inspection.UntrackedFileCount == 0 {
			return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "dirty Git change observation requires a SHA-256 fingerprint and positive change count")
		}
	case inspection.DirtyFingerprint != "" || inspection.TrackedChangeCount != 0 || inspection.UntrackedFileCount != 0:
		return GitRepositoryChangeInspection{}, newDomainError(ErrorInvalidInput, "clean Git change observation cannot contain dirty material")
	}
	wantToken, err := gitRepositoryChangeToken(inspection.RepoID, inspection.HeadCommitSHA, inspection.DirtyFingerprint)
	if err != nil {
		return GitRepositoryChangeInspection{}, err
	}
	if inspection.ChangeToken != wantToken {
		return GitRepositoryChangeInspection{}, newDomainError(ErrorChangeObservationConflict, "Git change token does not match its observation material")
	}
	return inspection, nil
}

func isSHA256ContentHash(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func gitRepositoryChangeObservationPayloadHash(inspection GitRepositoryChangeInspection, stabilityWindow time.Duration) (string, error) {
	payload, err := deterministicJSON(struct {
		Inspection                  GitRepositoryChangeInspection `json:"inspection"`
		StabilityWindowMilliseconds int64                         `json:"stability_window_milliseconds"`
	}{
		Inspection:                  inspection,
		StabilityWindowMilliseconds: stabilityWindow.Milliseconds(),
	})
	if err != nil {
		return "", err
	}
	return contentHash(payload), nil
}

func readLatestGitRepositoryChangeWindow(ctx context.Context, tx sqlTx, repoID string) (gitRepositoryChangeWindow, bool, error) {
	window, err := scanGitRepositoryChangeWindow(tx.queryRow(ctx, `
		SELECT
			change_observation_id,
			repo_id,
			observation_number,
			previous_change_observation_id,
			token_contract,
			dirty_fingerprint_contract,
			head_commit_sha,
			dirty,
			dirty_fingerprint,
			tracked_change_count,
			untracked_file_count,
			change_token,
			first_observed_at,
			last_observed_at,
			observation_count
		FROM repository_change_observations
		WHERE repo_id = $1
		ORDER BY observation_number DESC
		LIMIT 1
		FOR UPDATE
	`, repoID))
	if errors.Is(err, pgx.ErrNoRows) {
		return gitRepositoryChangeWindow{}, false, nil
	}
	if err != nil {
		return gitRepositoryChangeWindow{}, false, fmt.Errorf("reading latest repository change observation: %w", err)
	}
	return window, true, nil
}

func scanGitRepositoryChangeWindow(row sqlRow) (gitRepositoryChangeWindow, error) {
	var window gitRepositoryChangeWindow
	var previousObservationID, dirtyFingerprint *string
	err := row.Scan(
		&window.id,
		&window.repoID,
		&window.number,
		&previousObservationID,
		&window.inspection.TokenContract,
		&window.inspection.DirtyFingerprintContract,
		&window.inspection.HeadCommitSHA,
		&window.inspection.Dirty,
		&dirtyFingerprint,
		&window.inspection.TrackedChangeCount,
		&window.inspection.UntrackedFileCount,
		&window.inspection.ChangeToken,
		&window.firstObservedAt,
		&window.lastObservedAt,
		&window.observationCount,
	)
	if previousObservationID != nil {
		window.previousObservationID = *previousObservationID
	}
	if dirtyFingerprint != nil {
		window.inspection.DirtyFingerprint = *dirtyFingerprint
	}
	window.inspection.RepoID = window.repoID
	window.firstObservedAt = window.firstObservedAt.UTC()
	window.lastObservedAt = window.lastObservedAt.UTC()
	return window, err
}

func insertGitRepositoryChangeWindow(ctx context.Context, tx sqlTx, inspection GitRepositoryChangeInspection, previous gitRepositoryChangeWindow, hasPrevious bool, observedAt time.Time) (gitRepositoryChangeWindow, error) {
	number := 1
	var previousID any
	if hasPrevious {
		number = previous.number + 1
		previousID = previous.id
	}
	observationID, err := stableID("change-observation:", "repository_change_observation", struct {
		RepoID            string `json:"repo_id"`
		ObservationNumber int    `json:"observation_number"`
		ChangeToken       string `json:"change_token"`
	}{
		RepoID:            inspection.RepoID,
		ObservationNumber: number,
		ChangeToken:       inspection.ChangeToken,
	})
	if err != nil {
		return gitRepositoryChangeWindow{}, err
	}
	var dirtyFingerprint any
	if inspection.DirtyFingerprint != "" {
		dirtyFingerprint = inspection.DirtyFingerprint
	}
	if _, err := tx.exec(ctx, `
		INSERT INTO repository_change_observations (
			change_observation_id,
			repo_id,
			observation_number,
			previous_change_observation_id,
			token_contract,
			dirty_fingerprint_contract,
			head_commit_sha,
			dirty,
			dirty_fingerprint,
			tracked_change_count,
			untracked_file_count,
			change_token,
			first_observed_at,
			last_observed_at,
			observation_count
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,1)
	`, observationID, inspection.RepoID, number, previousID, inspection.TokenContract, inspection.DirtyFingerprintContract, inspection.HeadCommitSHA, inspection.Dirty, dirtyFingerprint, inspection.TrackedChangeCount, inspection.UntrackedFileCount, inspection.ChangeToken, observedAt); err != nil {
		return gitRepositoryChangeWindow{}, fmt.Errorf("inserting repository change observation: %w", err)
	}
	previousObservationID := ""
	if hasPrevious {
		previousObservationID = previous.id
	}
	return gitRepositoryChangeWindow{
		id:                    observationID,
		repoID:                inspection.RepoID,
		number:                number,
		previousObservationID: previousObservationID,
		inspection:            inspection,
		firstObservedAt:       observedAt,
		lastObservedAt:        observedAt,
		observationCount:      1,
	}, nil
}

func readGitRepositoryChangeObservationRequest(ctx context.Context, tx sqlTx, requestID string) (persistedGitRepositoryChangeObservationRequest, bool, error) {
	var request persistedGitRepositoryChangeObservationRequest
	var previousObservationID, dirtyFingerprint *string
	err := tx.queryRow(ctx, `
		SELECT
			r.request_id,
			o.change_observation_id,
			o.observation_number,
			o.previous_change_observation_id,
			o.token_contract,
			o.dirty_fingerprint_contract,
			o.repo_id,
			o.head_commit_sha,
			o.dirty,
			o.dirty_fingerprint,
			o.tracked_change_count,
			o.untracked_file_count,
			o.change_token,
			o.first_observed_at,
			r.observed_at,
			r.observation_count,
			r.stability_window_milliseconds,
			r.stable_after,
			r.changed,
			r.coalesced,
			r.stable,
			r.request_payload_hash
		FROM repository_change_observation_requests r
		JOIN repository_change_observations o
		  ON o.change_observation_id = r.change_observation_id
		WHERE r.request_id = $1
	`, requestID).Scan(
		&request.result.RequestID,
		&request.result.ObservationID,
		&request.result.ObservationNumber,
		&previousObservationID,
		&request.result.Inspection.TokenContract,
		&request.result.Inspection.DirtyFingerprintContract,
		&request.result.Inspection.RepoID,
		&request.result.Inspection.HeadCommitSHA,
		&request.result.Inspection.Dirty,
		&dirtyFingerprint,
		&request.result.Inspection.TrackedChangeCount,
		&request.result.Inspection.UntrackedFileCount,
		&request.result.Inspection.ChangeToken,
		&request.result.FirstObservedAt,
		&request.result.LastObservedAt,
		&request.result.ObservationCount,
		&request.result.StabilityWindowMillis,
		&request.result.StableAfter,
		&request.result.Changed,
		&request.result.Coalesced,
		&request.result.Stable,
		&request.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedGitRepositoryChangeObservationRequest{}, false, nil
	}
	if err != nil {
		return persistedGitRepositoryChangeObservationRequest{}, false, fmt.Errorf("reading repository change observation request: %w", err)
	}
	if previousObservationID != nil {
		request.result.PreviousObservationID = *previousObservationID
	}
	if dirtyFingerprint != nil {
		request.result.Inspection.DirtyFingerprint = *dirtyFingerprint
	}
	request.result.FirstObservedAt = request.result.FirstObservedAt.UTC()
	request.result.LastObservedAt = request.result.LastObservedAt.UTC()
	request.result.StableAfter = request.result.StableAfter.UTC()
	return request, true, nil
}

func sameGitRepositoryChangeInspection(left, right GitRepositoryChangeInspection) bool {
	return left == right
}
