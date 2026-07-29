package detective

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ConnectorTextAdapterName    = "connector-text-delivery"
	ConnectorTextAdapterVersion = "v1"

	ConnectorProcessingStatusRunning   = "running"
	ConnectorProcessingStatusSucceeded = "succeeded"
	ConnectorProcessingStatusFailed    = "failed"
	ConnectorProcessingStatusExpired   = "expired"

	ConnectorProcessingMinLeaseDuration = time.Millisecond
	ConnectorProcessingMaxLeaseDuration = time.Hour

	connectorProcessingStatusPending = "pending"
	connectorTextProcessingContract  = "detective-connector-text-processing-v1"
	maxConnectorProcessingAttempts   = 1_000_000
	maxConnectorTextNonEmptyLines    = 4_096
)

// ConnectorTextProcessingInput claims and converts one exact text delivery into source authority.
type ConnectorTextProcessingInput struct {
	RequestID                 string `json:"request_id"`
	ConnectorDeliveryID       string `json:"connector_delivery_id"`
	WorkerID                  string `json:"worker_id"`
	LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
}

// ConnectorTextProcessingResult identifies the exact claim and source intake result.
type ConnectorTextProcessingResult struct {
	RequestID             string     `json:"request_id"`
	ConnectorDeliveryID   string     `json:"connector_delivery_id"`
	ConnectorID           string     `json:"connector_id"`
	ExternalDeliveryID    string     `json:"external_delivery_id"`
	ClaimID               string     `json:"claim_id"`
	AttemptNumber         int        `json:"attempt_number"`
	WorkerID              string     `json:"worker_id"`
	Status                string     `json:"status"`
	ClaimedAt             time.Time  `json:"claimed_at"`
	LeaseExpiresAt        time.Time  `json:"lease_expires_at"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	SourceIntakeRequestID string     `json:"source_intake_request_id,omitempty"`
	SourceSnapshotID      string     `json:"source_snapshot_id,omitempty"`
	ExtractionViewID      string     `json:"extraction_view_id,omitempty"`
	RawContentHash        string     `json:"raw_content_hash,omitempty"`
	ClaimCreated          bool       `json:"claim_created"`
	Reused                bool       `json:"reused"`
	Replayed              bool       `json:"replayed"`
}

// ConnectorProcessingRecoveryInput requeues one exact running claim after its lease elapsed.
type ConnectorProcessingRecoveryInput struct {
	RequestID           string `json:"request_id"`
	ConnectorDeliveryID string `json:"connector_delivery_id"`
	ClaimID             string `json:"claim_id"`
	WorkerID            string `json:"worker_id"`
}

// ConnectorProcessingRecoveryResult records one exact expired-claim recovery.
type ConnectorProcessingRecoveryResult struct {
	RequestID           string    `json:"request_id"`
	ConnectorDeliveryID string    `json:"connector_delivery_id"`
	ClaimID             string    `json:"claim_id"`
	WorkerID            string    `json:"worker_id"`
	AttemptNumber       int       `json:"attempt_number"`
	RecoveredAt         time.Time `json:"recovered_at"`
	Replayed            bool      `json:"replayed"`
}

type preparedConnectorProcessingRequest struct {
	input       ConnectorTextProcessingInput
	lease       time.Duration
	payloadHash string
}

type connectorProcessingClaim struct {
	request  preparedConnectorProcessingRequest
	result   ConnectorTextProcessingResult
	delivery persistedConnectorDelivery
	execute  bool
}

type preparedConnectorRecoveryRequest struct {
	input       ConnectorProcessingRecoveryInput
	payloadHash string
}

// ProcessConnectorTextDelivery claims one text delivery and captures its exact bytes as connector_text source authority.
func ProcessConnectorTextDelivery(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ConnectorTextProcessingInput,
) (ConnectorTextProcessingResult, error) {
	if pool == nil {
		return ConnectorTextProcessingResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareConnectorProcessingRequest(input)
	if err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	claim, err := claimConnectorTextProcessing(ctx, pool, prepared, time.Now().UTC())
	if err != nil {
		return ConnectorTextProcessingResult{}, err
	}
	if !claim.execute {
		return claim.result, nil
	}

	intake, err := captureConnectorTextSource(ctx, pool, claim.delivery)
	if err != nil {
		failureClass, failureMessage := connectorProcessingFailure(err)
		if finishErr := failConnectorTextProcessing(
			ctx,
			pool,
			claim,
			failureClass,
			failureMessage,
			time.Now().UTC(),
		); finishErr != nil {
			return ConnectorTextProcessingResult{}, fmt.Errorf(
				"processing connector delivery %s: %w (recording retryable failure: %v)",
				prepared.input.ConnectorDeliveryID,
				err,
				finishErr,
			)
		}
		return ConnectorTextProcessingResult{}, fmt.Errorf(
			"processing connector delivery %s: %w",
			prepared.input.ConnectorDeliveryID,
			err,
		)
	}
	return finishConnectorTextProcessing(ctx, pool, claim, intake, time.Now().UTC())
}

// RecoverConnectorTextProcessing requeues one exact claim only after its persisted lease elapsed.
func RecoverConnectorTextProcessing(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ConnectorProcessingRecoveryInput,
) (ConnectorProcessingRecoveryResult, error) {
	if pool == nil {
		return ConnectorProcessingRecoveryResult{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	prepared, err := prepareConnectorRecoveryRequest(input)
	if err != nil {
		return ConnectorProcessingRecoveryResult{}, err
	}
	return recoverConnectorTextProcessing(ctx, pool, prepared, time.Now().UTC())
}

func prepareConnectorProcessingRequest(input ConnectorTextProcessingInput) (preparedConnectorProcessingRequest, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return preparedConnectorProcessingRequest{}, err
	}
	deliveryID, err := normalizeConnectorDeliveryID(input.ConnectorDeliveryID)
	if err != nil {
		return preparedConnectorProcessingRequest{}, err
	}
	workerID, err := normalizeBoundedText(input.WorkerID, "worker_id", 200)
	if err != nil {
		return preparedConnectorProcessingRequest{}, err
	}
	if input.LeaseDurationMilliseconds < ConnectorProcessingMinLeaseDuration.Milliseconds() ||
		input.LeaseDurationMilliseconds > ConnectorProcessingMaxLeaseDuration.Milliseconds() {
		return preparedConnectorProcessingRequest{}, newDomainError(
			ErrorInvalidInput,
			"connector processing lease duration must be between %d and %d milliseconds",
			ConnectorProcessingMinLeaseDuration.Milliseconds(),
			ConnectorProcessingMaxLeaseDuration.Milliseconds(),
		)
	}
	payloadHash, err := orchestrationPayloadHash(struct {
		ConnectorDeliveryID       string `json:"connector_delivery_id"`
		WorkerID                  string `json:"worker_id"`
		LeaseDurationMilliseconds int64  `json:"lease_duration_milliseconds"`
		AdapterName               string `json:"adapter_name"`
		AdapterVersion            string `json:"adapter_version"`
	}{
		ConnectorDeliveryID:       deliveryID,
		WorkerID:                  workerID,
		LeaseDurationMilliseconds: input.LeaseDurationMilliseconds,
		AdapterName:               ConnectorTextAdapterName,
		AdapterVersion:            ConnectorTextAdapterVersion,
	})
	if err != nil {
		return preparedConnectorProcessingRequest{}, err
	}
	return preparedConnectorProcessingRequest{
		input: ConnectorTextProcessingInput{
			RequestID:                 requestID,
			ConnectorDeliveryID:       deliveryID,
			WorkerID:                  workerID,
			LeaseDurationMilliseconds: input.LeaseDurationMilliseconds,
		},
		lease:       time.Duration(input.LeaseDurationMilliseconds) * time.Millisecond,
		payloadHash: payloadHash,
	}, nil
}

func prepareConnectorRecoveryRequest(input ConnectorProcessingRecoveryInput) (preparedConnectorRecoveryRequest, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return preparedConnectorRecoveryRequest{}, err
	}
	deliveryID, err := normalizeConnectorDeliveryID(input.ConnectorDeliveryID)
	if err != nil {
		return preparedConnectorRecoveryRequest{}, err
	}
	claimID, err := normalizeConnectorClaimID(input.ClaimID)
	if err != nil {
		return preparedConnectorRecoveryRequest{}, err
	}
	workerID, err := normalizeBoundedText(input.WorkerID, "worker_id", 200)
	if err != nil {
		return preparedConnectorRecoveryRequest{}, err
	}
	payloadHash, err := orchestrationPayloadHash(struct {
		ConnectorDeliveryID string `json:"connector_delivery_id"`
		ClaimID             string `json:"claim_id"`
		WorkerID            string `json:"worker_id"`
	}{
		ConnectorDeliveryID: deliveryID,
		ClaimID:             claimID,
		WorkerID:            workerID,
	})
	if err != nil {
		return preparedConnectorRecoveryRequest{}, err
	}
	return preparedConnectorRecoveryRequest{
		input: ConnectorProcessingRecoveryInput{
			RequestID:           requestID,
			ConnectorDeliveryID: deliveryID,
			ClaimID:             claimID,
			WorkerID:            workerID,
		},
		payloadHash: payloadHash,
	}, nil
}

func captureConnectorTextSource(
	ctx context.Context,
	pool *pgxpool.Pool,
	delivery persistedConnectorDelivery,
) (evidenceingestion.SourceIntakeResult, error) {
	if delivery.receipt.ContentType != ConnectorDeliveryContentTypeText {
		return evidenceingestion.SourceIntakeResult{}, newDomainError(
			ErrorUnsupportedCapability,
			"connector text adapter does not support content_type %q",
			delivery.receipt.ContentType,
		)
	}
	if lineCount := connectorTextNonEmptyLineCount(delivery.payload); lineCount > maxConnectorTextNonEmptyLines {
		return evidenceingestion.SourceIntakeResult{}, newDomainError(
			ErrorInvalidInput,
			"connector text delivery contains %d non-empty lines, limit %d",
			lineCount,
			maxConnectorTextNonEmptyLines,
		)
	}
	return evidenceingestion.CaptureManualSource(ctx, pool, evidenceingestion.ManualTextInput{
		SourceSystem:  evidenceingestion.SourceSystemConnectorText,
		SourceID:      connectorTextSourceID(delivery.receipt.ConnectorID, delivery.receipt.ExternalDeliveryID),
		SourceVersion: delivery.receipt.PayloadHash,
		Raw:           append([]byte(nil), delivery.payload...),
		OriginMetadata: map[string]string{
			"connector_adapter_name":    ConnectorTextAdapterName,
			"connector_adapter_version": ConnectorTextAdapterVersion,
			"connector_delivery_id":     delivery.receipt.ConnectorDeliveryID,
			"connector_id":              delivery.receipt.ConnectorID,
			"connector_payload_hash":    delivery.receipt.PayloadHash,
			"connector_content_type":    delivery.receipt.ContentType,
			"external_delivery_id":      delivery.receipt.ExternalDeliveryID,
		},
		RequestID: connectorTextSourceIntakeRequestID(delivery.receipt.ConnectorDeliveryID),
	})
}

func connectorTextNonEmptyLineCount(payload []byte) int {
	count := 0
	lineStart := 0
	for index, value := range payload {
		if value != '\n' {
			continue
		}
		lineEnd := index
		if lineEnd > lineStart && payload[lineEnd-1] == '\r' {
			lineEnd--
		}
		if lineEnd > lineStart {
			count++
		}
		lineStart = index + 1
	}
	lineEnd := len(payload)
	if lineEnd > lineStart && payload[lineEnd-1] == '\r' {
		lineEnd--
	}
	if lineEnd > lineStart {
		count++
	}
	return count
}

func validateConnectorTextIntake(
	delivery persistedConnectorDelivery,
	intake evidenceingestion.SourceIntakeResult,
) error {
	if intake.SourceSystem != evidenceingestion.SourceSystemConnectorText ||
		intake.SourceID != connectorTextSourceID(delivery.receipt.ConnectorID, delivery.receipt.ExternalDeliveryID) ||
		intake.SourceVersion != delivery.receipt.PayloadHash ||
		intake.RawContentHash != delivery.receipt.PayloadHash ||
		intake.RenderedContentHash != delivery.receipt.PayloadHash ||
		intake.RendererName != evidenceingestion.RendererConnectorTextIdentity ||
		intake.RendererVersion != evidenceingestion.RendererConnectorTextIdentityVersion ||
		intake.SpanCatalogVersion != evidenceingestion.SpanCatalogConnectorTextLineV1 {
		return newDomainError(
			ErrorConnectorProcessingConflict,
			"connector text intake result does not match delivery %s authority",
			delivery.receipt.ConnectorDeliveryID,
		)
	}
	return nil
}

func normalizeConnectorDeliveryID(value string) (string, error) {
	return normalizeConnectorHashedID(value, "connector_delivery_id", "detective-connector-delivery:")
}

func normalizeConnectorClaimID(value string) (string, error) {
	return normalizeConnectorHashedID(value, "claim_id", "detective-connector-claim:")
}

func normalizeConnectorHashedID(value, field, prefix string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return "", newDomainError(ErrorInvalidInput, "%s has invalid stable identity", field)
	}
	if _, err := hex.DecodeString(value[len(prefix):]); err != nil {
		return "", newDomainError(ErrorInvalidInput, "%s has invalid stable identity", field)
	}
	return value, nil
}

func connectorProcessingClaimID(request preparedConnectorProcessingRequest) string {
	return "detective-connector-claim:" + hashHex([]byte(
		connectorTextProcessingContract+"\x00claim\x00"+
			request.input.RequestID+"\x00"+request.input.ConnectorDeliveryID+"\x00"+request.input.WorkerID,
	))
}

func connectorTextSourceID(connectorID, externalDeliveryID string) string {
	return "connector-text-source:" + hashHex([]byte(
		connectorTextProcessingContract+"\x00source\x00"+connectorID+"\x00"+externalDeliveryID,
	))
}

func connectorTextSourceIntakeRequestID(connectorDeliveryID string) string {
	return "detective-connector-intake:" + hashHex([]byte(
		connectorTextProcessingContract+"\x00intake\x00"+connectorDeliveryID,
	))
}

func connectorProcessingFailure(err error) (string, string) {
	failureClass := "connector_text_processing"
	if kind, ok := evidenceingestion.KindOf(err); ok {
		failureClass = "evidence_ingestion_" + string(kind)
	} else if kind, ok := KindOf(err); ok {
		failureClass = string(kind)
	}
	return boundedConnectorDiagnostic(failureClass, 200), boundedConnectorDiagnostic(err.Error(), 1000)
}

func boundedConnectorDiagnostic(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "?")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		value = "unspecified"
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func connectorProcessingRequestTerminalError(status, failureClass, failureMessage string) error {
	switch status {
	case ConnectorProcessingStatusFailed:
		return newDomainError(
			ErrorConnectorProcessingFailed,
			"connector processing failed with %s: %s",
			failureClass,
			failureMessage,
		)
	case ConnectorProcessingStatusExpired:
		return newDomainError(
			ErrorConnectorProcessingLeaseExpired,
			"connector processing claim expired: %s",
			failureMessage,
		)
	default:
		return errors.New("connector processing request is not terminal")
	}
}
