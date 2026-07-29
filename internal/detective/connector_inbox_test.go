package detective

import (
	"bytes"
	"context"
	"testing"
)

func TestPrepareConnectorDeliveryCanonicalizesAndCopiesPayload(t *testing.T) {
	payload := []byte("{\"issue\":\"AHE-42\"}\n")
	request, err := prepareConnectorDelivery(ConnectorDeliveryInput{
		RequestID:          " receive-ahe-42 ",
		ConnectorID:        " jira-primary ",
		ExternalDeliveryID: " webhook-42 ",
		ContentType:        " application/json ",
		Payload:            payload,
	})
	if err != nil {
		t.Fatalf("prepareConnectorDelivery() error = %v", err)
	}
	if request.input.RequestID != "receive-ahe-42" ||
		request.input.ConnectorID != "jira-primary" ||
		request.input.ExternalDeliveryID != "webhook-42" ||
		request.input.ContentType != ConnectorDeliveryContentTypeJSON {
		t.Fatalf("normalized connector delivery = %+v", request.input)
	}
	if request.connectorDeliveryID != connectorDeliveryID("jira-primary", "webhook-42") {
		t.Fatalf("connector delivery ID = %q", request.connectorDeliveryID)
	}
	if request.payloadHash != contentHash(payload) || len(request.payloadHash) != 71 {
		t.Fatalf("payload hash = %q", request.payloadHash)
	}
	if request.requestPayloadHash == "" || len(request.requestPayloadHash) != 71 {
		t.Fatalf("request payload hash = %q", request.requestPayloadHash)
	}

	payload[0] = 'X'
	if bytes.Equal(request.input.Payload, payload) || string(request.input.Payload) != "{\"issue\":\"AHE-42\"}\n" {
		t.Fatalf("prepared payload changed with caller buffer: %q", request.input.Payload)
	}

	second, err := prepareConnectorDelivery(request.input)
	if err != nil {
		t.Fatalf("second prepareConnectorDelivery() error = %v", err)
	}
	if second.connectorDeliveryID != request.connectorDeliveryID ||
		second.payloadHash != request.payloadHash ||
		second.requestPayloadHash != request.requestPayloadHash {
		t.Fatalf("connector delivery identity is not deterministic: %+v != %+v", second, request)
	}
}

func TestPrepareConnectorDeliveryRejectsInvalidEnvelope(t *testing.T) {
	base := ConnectorDeliveryInput{
		RequestID:          "receive-valid",
		ConnectorID:        "connector-valid",
		ExternalDeliveryID: "delivery-valid",
		ContentType:        ConnectorDeliveryContentTypeText,
		Payload:            []byte("raw delivery"),
	}
	tests := []struct {
		name  string
		input ConnectorDeliveryInput
	}{
		{name: "missing request", input: withConnectorRequestID(base, " ")},
		{name: "missing connector", input: withConnectorID(base, " ")},
		{name: "missing external delivery", input: withConnectorExternalDeliveryID(base, " ")},
		{name: "unsupported content type", input: withConnectorContentType(base, "application/xml")},
		{name: "content type parameters", input: withConnectorContentType(base, "application/json; charset=utf-8")},
		{name: "oversized payload", input: withConnectorPayload(base, make([]byte, maxConnectorDeliveryBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareConnectorDelivery(test.input)
			assertDetectiveKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestPrepareConnectorDeliveryAcceptsClosedBoundaryValues(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
		payload     []byte
	}{
		{name: "empty JSON", contentType: ConnectorDeliveryContentTypeJSON},
		{name: "maximum text", contentType: ConnectorDeliveryContentTypeText, payload: make([]byte, maxConnectorDeliveryBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := prepareConnectorDelivery(ConnectorDeliveryInput{
				RequestID:          "receive-boundary",
				ConnectorID:        "connector-boundary",
				ExternalDeliveryID: "delivery-boundary",
				ContentType:        test.contentType,
				Payload:            test.payload,
			})
			if err != nil {
				t.Fatalf("prepareConnectorDelivery() error = %v", err)
			}
			if len(request.input.Payload) != len(test.payload) {
				t.Fatalf("payload length = %d, want %d", len(request.input.Payload), len(test.payload))
			}
		})
	}
}

func TestReceiveConnectorDeliveryRequiresPool(t *testing.T) {
	_, err := ReceiveConnectorDelivery(context.Background(), nil, ConnectorDeliveryInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func TestReceiveConnectorDeliveryAndAcknowledgeRequiresCallback(t *testing.T) {
	_, err := ReceiveConnectorDeliveryAndAcknowledge(context.Background(), nil, ConnectorDeliveryInput{}, nil)
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func withConnectorRequestID(input ConnectorDeliveryInput, requestID string) ConnectorDeliveryInput {
	input.RequestID = requestID
	return input
}

func withConnectorID(input ConnectorDeliveryInput, connectorID string) ConnectorDeliveryInput {
	input.ConnectorID = connectorID
	return input
}

func withConnectorExternalDeliveryID(input ConnectorDeliveryInput, externalDeliveryID string) ConnectorDeliveryInput {
	input.ExternalDeliveryID = externalDeliveryID
	return input
}

func withConnectorContentType(input ConnectorDeliveryInput, contentType string) ConnectorDeliveryInput {
	input.ContentType = contentType
	return input
}

func withConnectorPayload(input ConnectorDeliveryInput, payload []byte) ConnectorDeliveryInput {
	input.Payload = payload
	return input
}
