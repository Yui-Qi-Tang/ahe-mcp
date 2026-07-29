package evidenceingestion

import (
	"context"
	"fmt"
	"time"
)

type repositoryExtractionWorkClaimAttempt struct {
	claimID                   string
	workItemID                string
	attemptNumber             int
	workerID                  string
	status                    string
	claimedAt                 time.Time
	leaseDurationMilliseconds int64
	leaseExpiresAt            time.Time
	finishedAt                *time.Time
	expiredAt                 *time.Time
	sourceGenerationID        string
	failureClass              string
	failureMessage            string
}

const repositoryExtractionWorkClaimAttemptSelect = `
	SELECT
		claim_id,
		work_item_id,
		attempt_number,
		worker_id,
		status,
		claimed_at,
		lease_duration_milliseconds,
		lease_expires_at,
		finished_at,
		expired_at,
		COALESCE(source_generation_id, ''),
		COALESCE(failure_class, ''),
		COALESCE(failure_message, '')
	FROM repository_extraction_work_claim_attempts
`

func nextRepositoryExtractionWorkClaimAttemptNumber(ctx context.Context, tx sqlTx, workItemID string) (int, error) {
	var number int
	if err := tx.queryRow(ctx, `
		SELECT COALESCE(MAX(attempt_number), 0) + 1
		FROM repository_extraction_work_claim_attempts
		WHERE work_item_id = $1
	`, workItemID).Scan(&number); err != nil {
		return 0, fmt.Errorf("allocating repository extraction work claim attempt number: %w", err)
	}
	return number, nil
}

func persistRepositoryExtractionWorkClaimAttempt(ctx context.Context, tx sqlTx, attempt repositoryExtractionWorkClaimAttempt) error {
	_, err := tx.exec(ctx, `
		INSERT INTO repository_extraction_work_claim_attempts (
			claim_id,
			work_item_id,
			attempt_number,
			worker_id,
			status,
			claimed_at,
			lease_duration_milliseconds,
			lease_expires_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, attempt.claimID, attempt.workItemID, attempt.attemptNumber, attempt.workerID, attempt.status, attempt.claimedAt, attempt.leaseDurationMilliseconds, attempt.leaseExpiresAt)
	if err != nil {
		return fmt.Errorf("inserting repository extraction work claim attempt: %w", err)
	}
	return nil
}

func readRepositoryExtractionWorkClaimAttempt(ctx context.Context, tx sqlTx, claimID string) (repositoryExtractionWorkClaimAttempt, error) {
	attempt, err := scanRepositoryExtractionWorkClaimAttempt(tx.queryRow(ctx, repositoryExtractionWorkClaimAttemptSelect+`
		WHERE claim_id = $1
	`, claimID))
	if err != nil {
		return repositoryExtractionWorkClaimAttempt{}, fmt.Errorf("reading repository extraction work claim attempt %s: %w", claimID, err)
	}
	return attempt, nil
}

func readRepositoryExtractionWorkClaimAttemptForUpdate(ctx context.Context, tx sqlTx, claimID string) (repositoryExtractionWorkClaimAttempt, error) {
	attempt, err := scanRepositoryExtractionWorkClaimAttempt(tx.queryRow(ctx, repositoryExtractionWorkClaimAttemptSelect+`
		WHERE claim_id = $1
		FOR UPDATE
	`, claimID))
	if err != nil {
		return repositoryExtractionWorkClaimAttempt{}, fmt.Errorf("locking repository extraction work claim attempt %s: %w", claimID, err)
	}
	return attempt, nil
}

func scanRepositoryExtractionWorkClaimAttempt(row sqlRow) (repositoryExtractionWorkClaimAttempt, error) {
	var attempt repositoryExtractionWorkClaimAttempt
	err := row.Scan(
		&attempt.claimID,
		&attempt.workItemID,
		&attempt.attemptNumber,
		&attempt.workerID,
		&attempt.status,
		&attempt.claimedAt,
		&attempt.leaseDurationMilliseconds,
		&attempt.leaseExpiresAt,
		&attempt.finishedAt,
		&attempt.expiredAt,
		&attempt.sourceGenerationID,
		&attempt.failureClass,
		&attempt.failureMessage,
	)
	return attempt, err
}

func expireRepositoryExtractionWorkClaimAttempt(ctx context.Context, tx sqlTx, attempt repositoryExtractionWorkClaimAttempt, expiredAt time.Time) error {
	tag, err := tx.exec(ctx, `
		UPDATE repository_extraction_work_claim_attempts
		SET status = $4, expired_at = $5
		WHERE claim_id = $1 AND work_item_id = $2 AND worker_id = $3 AND status = $6
	`, attempt.claimID, attempt.workItemID, attempt.workerID, repositoryExtractionWorkExpired, expiredAt, repositoryExtractionWorkRunning)
	if err != nil {
		return fmt.Errorf("expiring repository extraction work claim attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s changed during recovery", attempt.claimID)
	}
	return nil
}

func finishRepositoryExtractionWorkClaimAttempt(ctx context.Context, tx sqlTx, attempt repositoryExtractionWorkClaimAttempt, input RepositoryExtractionWorkFinishInput, finishedAt time.Time) error {
	tag, err := tx.exec(ctx, `
		UPDATE repository_extraction_work_claim_attempts
		SET status = $4,
			finished_at = $5,
			source_generation_id = $6,
			failure_class = $7,
			failure_message = $8
		WHERE claim_id = $1 AND work_item_id = $2 AND worker_id = $3 AND status = $9
	`, attempt.claimID, attempt.workItemID, attempt.workerID, input.Outcome, finishedAt, nullableString(input.SourceGenerationID), nullableString(input.FailureClass), nullableString(input.FailureMessage), repositoryExtractionWorkRunning)
	if err != nil {
		return fmt.Errorf("finishing repository extraction work claim attempt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return newDomainError(ErrorRepositoryWorkConflict, "repository extraction work claim attempt %s changed during finish", attempt.claimID)
	}
	return nil
}
