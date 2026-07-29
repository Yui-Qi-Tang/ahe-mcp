//go:build integration

package detective

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestIntegrationReceiveConnectorDeliveryStoresExactBytesAndReplays(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	payload := []byte{0x00, 0xff, '{', '}', '\n'}
	input := ConnectorDeliveryInput{
		RequestID:          "receive-connector-round-trip",
		ConnectorID:        "connector-primary",
		ExternalDeliveryID: "delivery-42",
		ContentType:        ConnectorDeliveryContentTypeJSON,
		Payload:            payload,
	}

	first, err := ReceiveConnectorDelivery(ctx, pool, input)
	if err != nil {
		t.Fatalf("ReceiveConnectorDelivery() error = %v", err)
	}
	if first.Replayed || !first.DeliveryCreated || first.RequestID != input.RequestID ||
		first.ConnectorDeliveryID != connectorDeliveryID(input.ConnectorID, input.ExternalDeliveryID) ||
		first.PayloadHash != contentHash(payload) || first.ByteLength != len(payload) || first.ReceivedAt.IsZero() {
		t.Fatalf("first connector delivery receipt = %+v", first)
	}

	var storedPayload []byte
	var storedHash string
	var storedLength int
	if err := pool.QueryRow(ctx, `
		SELECT payload_bytes, payload_hash, byte_length
		FROM detective_connector_inbox_deliveries
		WHERE connector_delivery_id = $1
	`, first.ConnectorDeliveryID).Scan(&storedPayload, &storedHash, &storedLength); err != nil {
		t.Fatalf("read stored connector delivery: %v", err)
	}
	if !bytes.Equal(storedPayload, payload) || storedHash != contentHash(payload) || storedLength != len(payload) {
		t.Fatalf("stored connector delivery = %v/%q/%d", storedPayload, storedHash, storedLength)
	}

	replay, err := ReceiveConnectorDelivery(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay ReceiveConnectorDelivery() error = %v", err)
	}
	if !replay.Replayed || !replay.DeliveryCreated {
		t.Fatalf("same-request replay = %+v", replay)
	}
	first.Replayed = true
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("same-request replay = %+v, want %+v", replay, first)
	}

	secondInput := input
	secondInput.RequestID = "receive-connector-round-trip-again"
	second, err := ReceiveConnectorDelivery(ctx, pool, secondInput)
	if err != nil {
		t.Fatalf("second-request ReceiveConnectorDelivery() error = %v", err)
	}
	if second.Replayed || second.DeliveryCreated || second.ConnectorDeliveryID != first.ConnectorDeliveryID || !second.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("second-request connector delivery receipt = %+v", second)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", 2)
}

func TestIntegrationReceiveConnectorDeliveryRejectsRequestAndDeliveryConflicts(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	input := ConnectorDeliveryInput{
		RequestID:          "receive-connector-conflict",
		ConnectorID:        "connector-conflict",
		ExternalDeliveryID: "delivery-conflict",
		ContentType:        ConnectorDeliveryContentTypeText,
		Payload:            []byte("original bytes"),
	}
	if _, err := ReceiveConnectorDelivery(ctx, pool, input); err != nil {
		t.Fatalf("initial ReceiveConnectorDelivery() error = %v", err)
	}

	reusedRequest := input
	reusedRequest.Payload = []byte("changed request bytes")
	_, err := ReceiveConnectorDelivery(ctx, pool, reusedRequest)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)

	reusedDelivery := reusedRequest
	reusedDelivery.RequestID = "receive-connector-conflict-new-request"
	_, err = ReceiveConnectorDelivery(ctx, pool, reusedDelivery)
	assertDetectiveKind(t, err, ErrorConnectorDeliveryConflict)

	changedType := input
	changedType.RequestID = "receive-connector-conflict-new-content-type"
	changedType.ContentType = ConnectorDeliveryContentTypeJSON
	_, err = ReceiveConnectorDelivery(ctx, pool, changedType)
	assertDetectiveKind(t, err, ErrorConnectorDeliveryConflict)

	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", 1)
}

func TestIntegrationReceiveConnectorDeliveryConcurrentRequestsConverge(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	base := ConnectorDeliveryInput{
		ConnectorID:        "connector-concurrent",
		ExternalDeliveryID: "delivery-concurrent",
		ContentType:        ConnectorDeliveryContentTypeJSON,
		Payload:            []byte("{\"sequence\":1}"),
	}
	type outcome struct {
		receipt ConnectorDeliveryReceipt
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for index := range 2 {
		input := base
		input.RequestID = "receive-connector-concurrent-" + string(rune('a'+index))
		go func() {
			<-start
			receipt, err := ReceiveConnectorDelivery(ctx, pool, input)
			outcomes <- outcome{receipt: receipt, err: err}
		}()
	}
	close(start)

	created := 0
	var deliveryID string
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent ReceiveConnectorDelivery() error = %v", got.err)
		}
		if got.receipt.Replayed {
			t.Fatalf("different concurrent request replayed: %+v", got.receipt)
		}
		if got.receipt.DeliveryCreated {
			created++
		}
		if deliveryID == "" {
			deliveryID = got.receipt.ConnectorDeliveryID
		} else if deliveryID != got.receipt.ConnectorDeliveryID {
			t.Fatalf("concurrent connector delivery IDs = %q/%q", deliveryID, got.receipt.ConnectorDeliveryID)
		}
	}
	if created != 1 {
		t.Fatalf("concurrent delivery-created count = %d, want 1", created)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", 2)
}

func TestIntegrationReceiveConnectorDeliveryConcurrentConflictHasOneWinner(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	inputs := []ConnectorDeliveryInput{
		{
			RequestID:          "receive-connector-race-a",
			ConnectorID:        "connector-race",
			ExternalDeliveryID: "delivery-race",
			ContentType:        ConnectorDeliveryContentTypeText,
			Payload:            []byte("payload-a"),
		},
		{
			RequestID:          "receive-connector-race-b",
			ConnectorID:        "connector-race",
			ExternalDeliveryID: "delivery-race",
			ContentType:        ConnectorDeliveryContentTypeText,
			Payload:            []byte("payload-b"),
		},
	}
	type outcome struct {
		receipt ConnectorDeliveryReceipt
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(inputs))
	for _, input := range inputs {
		go func() {
			<-start
			receipt, err := ReceiveConnectorDelivery(ctx, pool, input)
			outcomes <- outcome{receipt: receipt, err: err}
		}()
	}
	close(start)

	winners, conflicts := 0, 0
	var winner ConnectorDeliveryReceipt
	for range inputs {
		got := <-outcomes
		if got.err == nil {
			winners++
			winner = got.receipt
			continue
		}
		if kind, ok := KindOf(got.err); !ok || kind != ErrorConnectorDeliveryConflict {
			t.Fatalf("concurrent conflicting receive error = %v", got.err)
		}
		conflicts++
	}
	if winners != 1 || conflicts != 1 || !winner.DeliveryCreated {
		t.Fatalf("concurrent conflicting results = %d winners/%d conflicts, receipt %+v", winners, conflicts, winner)
	}
	var storedPayload []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload_bytes
		FROM detective_connector_inbox_deliveries
		WHERE connector_delivery_id = $1
	`, winner.ConnectorDeliveryID).Scan(&storedPayload); err != nil {
		t.Fatalf("read concurrent winning payload: %v", err)
	}
	if !bytes.Equal(storedPayload, []byte("payload-a")) && !bytes.Equal(storedPayload, []byte("payload-b")) {
		t.Fatalf("concurrent winning payload = %q", storedPayload)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", 1)
}

func TestIntegrationReceiveConnectorDeliveryAcknowledgesOnlyAfterCommit(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	input := ConnectorDeliveryInput{
		RequestID:          "receive-connector-ack",
		ConnectorID:        "connector-ack",
		ExternalDeliveryID: "delivery-ack",
		ContentType:        ConnectorDeliveryContentTypeText,
		Payload:            []byte("ack only after durable receipt"),
	}
	ackCalls := 0
	receipt, err := ReceiveConnectorDeliveryAndAcknowledge(ctx, pool, input, func(ctx context.Context, got ConnectorDeliveryReceipt) error {
		ackCalls++
		var payload []byte
		if err := pool.QueryRow(ctx, `
			SELECT payload_bytes
			FROM detective_connector_inbox_deliveries
			WHERE connector_delivery_id = $1
		`, got.ConnectorDeliveryID).Scan(&payload); err != nil {
			return err
		}
		if !bytes.Equal(payload, input.Payload) {
			return errors.New("ack observed changed connector payload")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReceiveConnectorDeliveryAndAcknowledge() error = %v", err)
	}
	if ackCalls != 1 || !receipt.DeliveryCreated || receipt.Replayed {
		t.Fatalf("acknowledged connector delivery = %+v, calls %d", receipt, ackCalls)
	}

	ackFailure := errors.New("upstream ack unavailable")
	failedInput := input
	failedInput.RequestID = "receive-connector-ack-failure"
	failedInput.ExternalDeliveryID = "delivery-ack-failure"
	failedReceipt, err := ReceiveConnectorDeliveryAndAcknowledge(ctx, pool, failedInput, func(context.Context, ConnectorDeliveryReceipt) error {
		return ackFailure
	})
	if !errors.Is(err, ackFailure) || !failedReceipt.DeliveryCreated || failedReceipt.Replayed {
		t.Fatalf("failed acknowledgement = %+v / %v", failedReceipt, err)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 2)

	retryCalls := 0
	retried, err := ReceiveConnectorDeliveryAndAcknowledge(ctx, pool, failedInput, func(context.Context, ConnectorDeliveryReceipt) error {
		retryCalls++
		return nil
	})
	if err != nil {
		t.Fatalf("retry acknowledgement error = %v", err)
	}
	if retryCalls != 1 || !retried.Replayed || !retried.DeliveryCreated || retried.ConnectorDeliveryID != failedReceipt.ConnectorDeliveryID {
		t.Fatalf("retried acknowledgement = %+v, calls %d", retried, retryCalls)
	}
}

func TestIntegrationReceiveConnectorDeliveryDoesNotAcknowledgeFailedReceipt(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	input := ConnectorDeliveryInput{
		RequestID:          "receive-connector-no-ack",
		ConnectorID:        "connector-no-ack",
		ExternalDeliveryID: "delivery-no-ack",
		ContentType:        ConnectorDeliveryContentTypeText,
		Payload:            []byte("original"),
	}
	if _, err := ReceiveConnectorDelivery(ctx, pool, input); err != nil {
		t.Fatalf("initial ReceiveConnectorDelivery() error = %v", err)
	}
	input.RequestID = "receive-connector-no-ack-conflict"
	input.Payload = []byte("conflict")
	ackCalls := 0
	_, err := ReceiveConnectorDeliveryAndAcknowledge(ctx, pool, input, func(context.Context, ConnectorDeliveryReceipt) error {
		ackCalls++
		return nil
	})
	assertDetectiveKind(t, err, ErrorConnectorDeliveryConflict)
	if ackCalls != 0 {
		t.Fatalf("failed receipt acknowledgement calls = %d, want 0", ackCalls)
	}
}

func TestIntegrationReceiveConnectorDeliveryConcurrentSameRequestReplays(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	input := ConnectorDeliveryInput{
		RequestID:          "receive-connector-same-request",
		ConnectorID:        "connector-same-request",
		ExternalDeliveryID: "delivery-same-request",
		ContentType:        ConnectorDeliveryContentTypeText,
		Payload:            []byte("same request"),
	}
	type outcome struct {
		receipt ConnectorDeliveryReceipt
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			receipt, err := ReceiveConnectorDelivery(ctx, pool, input)
			outcomes <- outcome{receipt: receipt, err: err}
		}()
	}
	close(start)

	replayed := 0
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent same-request receive error = %v", got.err)
		}
		if got.receipt.Replayed {
			replayed++
		}
	}
	if replayed != 1 {
		t.Fatalf("concurrent same-request replay count = %d, want 1", replayed)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_receive_requests", 1)

	var recordedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT recorded_at
		FROM detective_connector_inbox_receive_requests
		WHERE request_id = $1
	`, input.RequestID).Scan(&recordedAt); err != nil {
		t.Fatalf("read concurrent receive request: %v", err)
	}
	if recordedAt.IsZero() {
		t.Fatal("concurrent receive request has zero recorded_at")
	}
}
