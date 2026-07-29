package evidenceingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const failurePersistenceTimeout = 5 * time.Second

// RunTrustedExtractor invokes a caller-supplied local extractor outside any PostgreSQL transaction.
func RunTrustedExtractor(ctx context.Context, pool *pgxpool.Pool, request TrustedExtractorRequest, runner ExtractorRunner) (IngestResult, error) {
	if pool == nil {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return runTrustedExtractor(ctx, pgxDB{pool: pool}, request, runner)
}

func runTrustedExtractor(ctx context.Context, db sqlDB, request TrustedExtractorRequest, runner ExtractorRunner) (IngestResult, error) {
	if runner == nil {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "extractor runner is required")
	}
	if request.RequestID == "" {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "request_id is required")
	}
	if request.ExtractionViewID == "" {
		return IngestResult{}, newDomainError(ErrorInvalidInput, "extraction_view_id is required")
	}
	if !strings.HasPrefix(request.ExtractionViewID, "view:") {
		return IngestResult{}, newDomainError(ErrorInvalidRecordID, "extraction_view_id %q must start with view:", request.ExtractionViewID)
	}

	sourceCtx, err := loadManualSourceContextByViewID(ctx, db, request.ExtractionViewID)
	if err != nil {
		return IngestResult{}, err
	}
	attemptCtx, status, err := startTrustedExtractorAttempt(ctx, db, sourceCtx, request)
	if err != nil {
		return IngestResult{}, err
	}
	switch status {
	case attemptStatusSucceeded:
		result, err := replaySucceededAttempt(ctx, db, attemptCtx, "")
		if err != nil {
			return IngestResult{}, err
		}
		result.Replayed = true
		return result, nil
	case attemptStatusFailed:
		return IngestResult{}, newDomainError(ErrorPersistedAttemptFailed, "attempt %s already failed", attemptCtx.ExtractionAttempt.ID)
	}

	outputData, err := runner(ctx, sourceCtx.extractorInput())
	if err != nil {
		failure := classifyRunnerError(ctx, err)
		failureCtx, cancel := detachedFailureContext(ctx)
		defer cancel()
		if persistErr := persistAttemptFailureBytes(failureCtx, db, attemptCtx, outputData, failure); persistErr != nil {
			return IngestResult{}, fmt.Errorf("persisting attempt failure after %w: %v", failure, persistErr)
		}
		return IngestResult{}, failure
	}

	output, err := decodeTrustedExtractorOutput(outputData)
	if err != nil {
		failureCtx, cancel := detachedFailureContext(ctx)
		defer cancel()
		if persistErr := persistAttemptFailureBytes(failureCtx, db, attemptCtx, outputData, err); persistErr != nil {
			return IngestResult{}, fmt.Errorf("persisting attempt failure after %w: %v", err, persistErr)
		}
		return IngestResult{}, err
	}
	return completeAttemptWithRawOutput(ctx, db, attemptCtx, output, outputData)
}

func startTrustedExtractorAttempt(ctx context.Context, db sqlDB, sourceCtx manualSourceContext, request TrustedExtractorRequest) (attemptContext, string, error) {
	attemptCtx, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, request.RequestID, 0, request.ExtractorDefinition)
	if err != nil {
		return attemptContext{}, "", err
	}
	status, err := persistAttemptStartForExistingSource(ctx, db, attemptCtx)
	if err != nil {
		return attemptContext{}, "", err
	}
	if status != attemptStatusFailed {
		return attemptCtx, status, nil
	}

	succeededNumber, ok, err := succeededAttemptNumber(ctx, db, attemptCtx.ExtractionRun.ID)
	if err != nil {
		return attemptContext{}, "", err
	}
	if ok {
		attemptCtx, err = buildAttemptContextFromSourceWithDefinition(sourceCtx, request.RequestID, succeededNumber, request.ExtractorDefinition)
		if err != nil {
			return attemptContext{}, "", err
		}
		return attemptCtx, attemptStatusSucceeded, nil
	}
	if !request.RetryFailedAttempt {
		return attemptCtx, status, nil
	}

	nextNumber, err := nextAttemptNumber(ctx, db, attemptCtx.ExtractionRun.ID)
	if err != nil {
		return attemptContext{}, "", err
	}
	attemptCtx, err = buildAttemptContextFromSourceWithDefinition(sourceCtx, request.RequestID, nextNumber, request.ExtractorDefinition)
	if err != nil {
		return attemptContext{}, "", err
	}
	status, err = persistAttemptStartForExistingSource(ctx, db, attemptCtx)
	if err != nil {
		return attemptContext{}, "", err
	}
	return attemptCtx, status, nil
}

func succeededAttemptNumber(ctx context.Context, db sqlDB, extractionRunID string) (int, bool, error) {
	var attemptNumber int
	err := db.queryRow(ctx, `
		SELECT attempt_number
		FROM extraction_attempts
		WHERE extraction_run_id = $1
			AND status = 'succeeded'
		ORDER BY attempt_number
		LIMIT 1
	`, extractionRunID).Scan(&attemptNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("reading succeeded attempt: %w", err)
	}
	return attemptNumber, true, nil
}

func nextAttemptNumber(ctx context.Context, db sqlDB, extractionRunID string) (int, error) {
	var maxAttempt int
	err := db.queryRow(ctx, `
		SELECT COALESCE(MAX(attempt_number), 0)
		FROM extraction_attempts
		WHERE extraction_run_id = $1
	`, extractionRunID).Scan(&maxAttempt)
	if err != nil {
		return 0, fmt.Errorf("reading next attempt number: %w", err)
	}
	return maxAttempt + 1, nil
}

func decodeTrustedExtractorOutput(data []byte) (FrozenExtractorOutput, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var output FrozenExtractorOutput
	if err := decoder.Decode(&output); err != nil {
		return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "decoding extractor output: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return FrozenExtractorOutput{}, newDomainError(ErrorInvalidExtractorOutput, "extractor output must contain exactly one JSON document")
	}
	return output, nil
}

func classifyRunnerError(ctx context.Context, err error) error {
	if _, ok := KindOf(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return newDomainError(ErrorRunnerInvocationTimeout, "extractor runner timed out: %v", err)
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return newDomainError(ErrorRunnerInvocationCancelled, "extractor runner was cancelled: %v", err)
	default:
		return newDomainError(ErrorRunnerInvocationFailed, "extractor runner failed: %v", err)
	}
}

func detachedFailureContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), failurePersistenceTimeout)
}
