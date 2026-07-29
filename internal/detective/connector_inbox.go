package detective

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ConnectorDeliveryContentTypeJSON = "application/json"
	ConnectorDeliveryContentTypeText = "text/plain"

	maxConnectorDeliveryBytes = 1 << 20
	connectorInboxContract    = "detective-connector-inbox-v1"
)

// ConnectorDeliveryInput is one authenticated transport delivery presented by a trusted adapter.
// The inbox stores bytes before acknowledgement but does not interpret them as evidence.
type ConnectorDeliveryInput struct {
	RequestID          string `json:"request_id"`
	ConnectorID        string `json:"connector_id"`
	ExternalDeliveryID string `json:"external_delivery_id"`
	ContentType        string `json:"content_type"`
	Payload            []byte `json:"payload"`
}

// ConnectorDeliveryReceipt proves one delivery is durable and may now be acknowledged externally.
type ConnectorDeliveryReceipt struct {
	RequestID           string    `json:"request_id"`
	ConnectorDeliveryID string    `json:"connector_delivery_id"`
	ConnectorID         string    `json:"connector_id"`
	ExternalDeliveryID  string    `json:"external_delivery_id"`
	ContentType         string    `json:"content_type"`
	PayloadHash         string    `json:"payload_hash"`
	ByteLength          int       `json:"byte_length"`
	ReceivedAt          time.Time `json:"received_at"`
	DeliveryCreated     bool      `json:"delivery_created"`
	Replayed            bool      `json:"replayed"`
}

type connectorDeliveryRequest struct {
	input               ConnectorDeliveryInput
	connectorDeliveryID string
	payloadHash         string
	requestPayloadHash  string
}

type persistedConnectorDelivery struct {
	receipt ConnectorDeliveryReceipt
	payload []byte
}

type persistedConnectorReceiveRequest struct {
	requestID           string
	connectorDeliveryID string
	connectorID         string
	externalDeliveryID  string
	deliveryCreated     bool
	requestPayloadHash  string
}

// ReceiveConnectorDelivery durably records exact raw bytes before returning an acknowledgement-safe receipt.
func ReceiveConnectorDelivery(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ConnectorDeliveryInput,
) (ConnectorDeliveryReceipt, error) {
	if pool == nil {
		return ConnectorDeliveryReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	request, err := prepareConnectorDelivery(input)
	if err != nil {
		return ConnectorDeliveryReceipt{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return ConnectorDeliveryReceipt{}, fmt.Errorf("beginning connector delivery receipt: %w", err)
	}
	defer tx.Rollback(context.Background())

	if err := lockConnectorReceiveRequest(ctx, tx, request.input.RequestID); err != nil {
		return ConnectorDeliveryReceipt{}, err
	}
	persistedRequest, found, err := readConnectorReceiveRequest(ctx, tx, request.input.RequestID)
	if err != nil {
		return ConnectorDeliveryReceipt{}, err
	}
	if found {
		if persistedRequest.requestPayloadHash != request.requestPayloadHash ||
			persistedRequest.connectorDeliveryID != request.connectorDeliveryID ||
			persistedRequest.connectorID != request.input.ConnectorID ||
			persistedRequest.externalDeliveryID != request.input.ExternalDeliveryID {
			return ConnectorDeliveryReceipt{}, newDomainError(
				ErrorIdempotencyKeyReused,
				"request_id %s already exists with different connector delivery payload",
				request.input.RequestID,
			)
		}
		delivery, found, err := readConnectorDelivery(ctx, tx, request.connectorDeliveryID, false)
		if err != nil {
			return ConnectorDeliveryReceipt{}, err
		}
		if !found {
			return ConnectorDeliveryReceipt{}, newDomainError(
				ErrorConnectorDeliveryConflict,
				"connector delivery request %s references missing delivery %s",
				request.input.RequestID,
				request.connectorDeliveryID,
			)
		}
		if err := ensureConnectorDeliveryMatches(request, delivery); err != nil {
			return ConnectorDeliveryReceipt{}, err
		}
		if err := ensureConnectorTextProcessingWork(ctx, tx, delivery); err != nil {
			return ConnectorDeliveryReceipt{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ConnectorDeliveryReceipt{}, fmt.Errorf("committing connector delivery replay: %w", err)
		}
		receipt := delivery.receipt
		receipt.RequestID = request.input.RequestID
		receipt.DeliveryCreated = persistedRequest.deliveryCreated
		receipt.Replayed = true
		return receipt, nil
	}

	delivery, created, err := insertOrReadConnectorDelivery(ctx, tx, request)
	if err != nil {
		return ConnectorDeliveryReceipt{}, err
	}
	if err := ensureConnectorDeliveryMatches(request, delivery); err != nil {
		return ConnectorDeliveryReceipt{}, err
	}
	if err := ensureConnectorTextProcessingWork(ctx, tx, delivery); err != nil {
		return ConnectorDeliveryReceipt{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO detective_connector_inbox_receive_requests (
			request_id, connector_delivery_id, connector_id,
			external_delivery_id, delivery_created, request_payload_hash
		)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, request.input.RequestID, request.connectorDeliveryID, request.input.ConnectorID,
		request.input.ExternalDeliveryID, created, request.requestPayloadHash,
	); err != nil {
		return ConnectorDeliveryReceipt{}, fmt.Errorf("inserting connector delivery receive request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectorDeliveryReceipt{}, fmt.Errorf("committing connector delivery receipt: %w", err)
	}
	receipt := delivery.receipt
	receipt.RequestID = request.input.RequestID
	receipt.DeliveryCreated = created
	return receipt, nil
}

// ReceiveConnectorDeliveryAndAcknowledge invokes acknowledge only after the receipt transaction commits.
func ReceiveConnectorDeliveryAndAcknowledge(
	ctx context.Context,
	pool *pgxpool.Pool,
	input ConnectorDeliveryInput,
	acknowledge func(context.Context, ConnectorDeliveryReceipt) error,
) (ConnectorDeliveryReceipt, error) {
	if acknowledge == nil {
		return ConnectorDeliveryReceipt{}, newDomainError(ErrorInvalidInput, "connector acknowledge function is required")
	}
	receipt, err := ReceiveConnectorDelivery(ctx, pool, input)
	if err != nil {
		return ConnectorDeliveryReceipt{}, err
	}
	if err := acknowledge(ctx, receipt); err != nil {
		return receipt, fmt.Errorf("acknowledging connector delivery %s: %w", receipt.ConnectorDeliveryID, err)
	}
	return receipt, nil
}

func prepareConnectorDelivery(input ConnectorDeliveryInput) (connectorDeliveryRequest, error) {
	requestID, err := normalizeBoundedText(input.RequestID, "request_id", 300)
	if err != nil {
		return connectorDeliveryRequest{}, err
	}
	connectorID, err := normalizeBoundedText(input.ConnectorID, "connector_id", 200)
	if err != nil {
		return connectorDeliveryRequest{}, err
	}
	externalDeliveryID, err := normalizeBoundedText(input.ExternalDeliveryID, "external_delivery_id", 500)
	if err != nil {
		return connectorDeliveryRequest{}, err
	}
	contentType := strings.TrimSpace(input.ContentType)
	if contentType != ConnectorDeliveryContentTypeJSON && contentType != ConnectorDeliveryContentTypeText {
		return connectorDeliveryRequest{}, newDomainError(ErrorInvalidInput, "unsupported connector delivery content_type %q", contentType)
	}
	if len(input.Payload) > maxConnectorDeliveryBytes {
		return connectorDeliveryRequest{}, newDomainError(
			ErrorInvalidInput,
			"connector delivery payload exceeds %d bytes",
			maxConnectorDeliveryBytes,
		)
	}
	payload := append([]byte(nil), input.Payload...)
	payloadHash := contentHash(payload)
	connectorDeliveryID := connectorDeliveryID(connectorID, externalDeliveryID)
	requestPayloadHash, err := orchestrationPayloadHash(struct {
		ConnectorID        string `json:"connector_id"`
		ExternalDeliveryID string `json:"external_delivery_id"`
		ContentType        string `json:"content_type"`
		PayloadHash        string `json:"payload_hash"`
		ByteLength         int    `json:"byte_length"`
	}{
		ConnectorID: connectorID, ExternalDeliveryID: externalDeliveryID,
		ContentType: contentType, PayloadHash: payloadHash, ByteLength: len(payload),
	})
	if err != nil {
		return connectorDeliveryRequest{}, err
	}
	return connectorDeliveryRequest{
		input: ConnectorDeliveryInput{
			RequestID: requestID, ConnectorID: connectorID,
			ExternalDeliveryID: externalDeliveryID, ContentType: contentType, Payload: payload,
		},
		connectorDeliveryID: connectorDeliveryID,
		payloadHash:         payloadHash, requestPayloadHash: requestPayloadHash,
	}, nil
}

func insertOrReadConnectorDelivery(
	ctx context.Context,
	tx pgx.Tx,
	request connectorDeliveryRequest,
) (persistedConnectorDelivery, bool, error) {
	var receivedAt time.Time
	err := tx.QueryRow(ctx, `
		INSERT INTO detective_connector_inbox_deliveries (
			connector_delivery_id, connector_id, external_delivery_id,
			content_type, payload_hash, payload_bytes, byte_length
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (connector_id, external_delivery_id) DO NOTHING
		RETURNING received_at
	`, request.connectorDeliveryID, request.input.ConnectorID, request.input.ExternalDeliveryID,
		request.input.ContentType, request.payloadHash, request.input.Payload, len(request.input.Payload),
	).Scan(&receivedAt)
	if err == nil {
		return persistedConnectorDelivery{
			receipt: ConnectorDeliveryReceipt{
				ConnectorDeliveryID: request.connectorDeliveryID,
				ConnectorID:         request.input.ConnectorID,
				ExternalDeliveryID:  request.input.ExternalDeliveryID,
				ContentType:         request.input.ContentType,
				PayloadHash:         request.payloadHash,
				ByteLength:          len(request.input.Payload),
				ReceivedAt:          receivedAt.UTC(),
			},
			payload: append([]byte(nil), request.input.Payload...),
		}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return persistedConnectorDelivery{}, false, fmt.Errorf("inserting connector inbox delivery: %w", err)
	}
	delivery, found, err := readConnectorDelivery(ctx, tx, request.connectorDeliveryID, true)
	if err != nil {
		return persistedConnectorDelivery{}, false, err
	}
	if !found {
		return persistedConnectorDelivery{}, false, newDomainError(
			ErrorConnectorDeliveryConflict,
			"connector delivery %s could not be created or resolved",
			request.connectorDeliveryID,
		)
	}
	return delivery, false, nil
}

func ensureConnectorDeliveryMatches(request connectorDeliveryRequest, delivery persistedConnectorDelivery) error {
	if err := ensureConnectorDeliveryIntegrity(delivery); err != nil {
		return err
	}
	receipt := delivery.receipt
	if receipt.ConnectorDeliveryID != request.connectorDeliveryID ||
		receipt.ConnectorID != request.input.ConnectorID ||
		receipt.ExternalDeliveryID != request.input.ExternalDeliveryID ||
		receipt.ContentType != request.input.ContentType ||
		receipt.PayloadHash != request.payloadHash ||
		receipt.ByteLength != len(request.input.Payload) ||
		!bytes.Equal(delivery.payload, request.input.Payload) {
		return newDomainError(
			ErrorConnectorDeliveryConflict,
			"connector %s delivery %s was reused with different immutable payload",
			request.input.ConnectorID,
			request.input.ExternalDeliveryID,
		)
	}
	return nil
}

func ensureConnectorDeliveryIntegrity(delivery persistedConnectorDelivery) error {
	receipt := delivery.receipt
	if receipt.ConnectorDeliveryID != connectorDeliveryID(receipt.ConnectorID, receipt.ExternalDeliveryID) ||
		receipt.PayloadHash != contentHash(delivery.payload) ||
		receipt.ByteLength != len(delivery.payload) {
		return newDomainError(
			ErrorConnectorDeliveryConflict,
			"connector delivery %s failed persisted identity or content validation",
			receipt.ConnectorDeliveryID,
		)
	}
	return nil
}

func lockConnectorReceiveRequest(ctx context.Context, tx pgx.Tx, requestID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_ingestion_request_serializations (operation_name, request_id)
		VALUES ('detective-connector-inbox-receive', $1)
		ON CONFLICT (operation_name, request_id) DO NOTHING
	`, requestID); err != nil {
		return fmt.Errorf("creating connector receive request serialization: %w", err)
	}
	var locked int
	if err := tx.QueryRow(ctx, `
		SELECT 1
		FROM evidence_ingestion_request_serializations
		WHERE operation_name = 'detective-connector-inbox-receive' AND request_id = $1
		FOR UPDATE
	`, requestID).Scan(&locked); err != nil {
		return fmt.Errorf("locking connector receive request: %w", err)
	}
	return nil
}

func readConnectorReceiveRequest(
	ctx context.Context,
	db workspaceQuerier,
	requestID string,
) (persistedConnectorReceiveRequest, bool, error) {
	var request persistedConnectorReceiveRequest
	err := db.QueryRow(ctx, `
		SELECT
			request_id, connector_delivery_id, connector_id,
			external_delivery_id, delivery_created, request_payload_hash
		FROM detective_connector_inbox_receive_requests
		WHERE request_id = $1
	`, requestID).Scan(
		&request.requestID,
		&request.connectorDeliveryID,
		&request.connectorID,
		&request.externalDeliveryID,
		&request.deliveryCreated,
		&request.requestPayloadHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedConnectorReceiveRequest{}, false, nil
	}
	if err != nil {
		return persistedConnectorReceiveRequest{}, false, fmt.Errorf("reading connector receive request: %w", err)
	}
	return request, true, nil
}

func readConnectorDelivery(
	ctx context.Context,
	db workspaceQuerier,
	connectorDeliveryID string,
	lock bool,
) (persistedConnectorDelivery, bool, error) {
	query := `
		SELECT
			connector_delivery_id, connector_id, external_delivery_id,
			content_type, payload_hash, payload_bytes, byte_length, received_at
		FROM detective_connector_inbox_deliveries
		WHERE connector_delivery_id = $1
	`
	if lock {
		query += " FOR UPDATE"
	}
	var delivery persistedConnectorDelivery
	err := db.QueryRow(ctx, query, connectorDeliveryID).Scan(
		&delivery.receipt.ConnectorDeliveryID,
		&delivery.receipt.ConnectorID,
		&delivery.receipt.ExternalDeliveryID,
		&delivery.receipt.ContentType,
		&delivery.receipt.PayloadHash,
		&delivery.payload,
		&delivery.receipt.ByteLength,
		&delivery.receipt.ReceivedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistedConnectorDelivery{}, false, nil
	}
	if err != nil {
		return persistedConnectorDelivery{}, false, fmt.Errorf("reading connector inbox delivery: %w", err)
	}
	delivery.receipt.ReceivedAt = delivery.receipt.ReceivedAt.UTC()
	delivery.payload = append([]byte(nil), delivery.payload...)
	if err := ensureConnectorDeliveryIntegrity(delivery); err != nil {
		return persistedConnectorDelivery{}, false, err
	}
	return delivery, true, nil
}

func connectorDeliveryID(connectorID, externalDeliveryID string) string {
	return "detective-connector-delivery:" + hashHex([]byte(
		connectorInboxContract+"\x00delivery\x00"+connectorID+"\x00"+externalDeliveryID,
	))
}
