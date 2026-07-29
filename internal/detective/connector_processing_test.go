package detective

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPrepareConnectorProcessingRequestCanonicalizesAndHashes(t *testing.T) {
	deliveryID := connectorDeliveryID("connector-processing", "delivery-processing")
	prepared, err := prepareConnectorProcessingRequest(ConnectorTextProcessingInput{
		RequestID:                 " process-connector ",
		ConnectorDeliveryID:       " " + deliveryID + " ",
		WorkerID:                  " worker-processing ",
		LeaseDurationMilliseconds: 60_000,
	})
	if err != nil {
		t.Fatalf("prepareConnectorProcessingRequest() error = %v", err)
	}
	if prepared.input.RequestID != "process-connector" ||
		prepared.input.ConnectorDeliveryID != deliveryID ||
		prepared.input.WorkerID != "worker-processing" ||
		prepared.lease != time.Minute || len(prepared.payloadHash) != 71 {
		t.Fatalf("prepared connector processing request = %+v", prepared)
	}
	second, err := prepareConnectorProcessingRequest(prepared.input)
	if err != nil {
		t.Fatalf("second prepareConnectorProcessingRequest() error = %v", err)
	}
	if second.payloadHash != prepared.payloadHash || connectorProcessingClaimID(second) != connectorProcessingClaimID(prepared) {
		t.Fatal("connector processing request identity is not deterministic")
	}
}

func TestPrepareConnectorProcessingRequestRejectsInvalidInput(t *testing.T) {
	deliveryID := connectorDeliveryID("connector-invalid", "delivery-invalid")
	base := ConnectorTextProcessingInput{
		RequestID:                 "process-valid",
		ConnectorDeliveryID:       deliveryID,
		WorkerID:                  "worker-valid",
		LeaseDurationMilliseconds: 1000,
	}
	tests := []struct {
		name  string
		input ConnectorTextProcessingInput
	}{
		{name: "missing request", input: withProcessingRequestID(base, " ")},
		{name: "invalid delivery", input: withProcessingDeliveryID(base, "delivery-invalid")},
		{name: "invalid delivery hash", input: withProcessingDeliveryID(base, "detective-connector-delivery:"+strings.Repeat("z", 64))},
		{name: "missing worker", input: withProcessingWorkerID(base, " ")},
		{name: "short lease", input: withProcessingLease(base, 0)},
		{name: "long lease", input: withProcessingLease(base, ConnectorProcessingMaxLeaseDuration.Milliseconds()+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := prepareConnectorProcessingRequest(test.input)
			assertDetectiveKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestConnectorTextLineBoundMatchesSpanSemantics(t *testing.T) {
	payload := []byte("first\n\n\r\nsecond\r\nthird")
	if got := connectorTextNonEmptyLineCount(payload); got != 3 {
		t.Fatalf("connectorTextNonEmptyLineCount() = %d, want 3", got)
	}
	tooMany := []byte(strings.Repeat("line\n", maxConnectorTextNonEmptyLines+1))
	if got := connectorTextNonEmptyLineCount(tooMany); got != maxConnectorTextNonEmptyLines+1 {
		t.Fatalf("connectorTextNonEmptyLineCount(tooMany) = %d", got)
	}
}

func TestConnectorProcessingFailureBoundsDiagnostics(t *testing.T) {
	class, message := connectorProcessingFailure(newDomainError(
		ErrorConnectorProcessingConflict,
		"failed\n%s",
		strings.Repeat("x", 1200),
	))
	if class != string(ErrorConnectorProcessingConflict) || len(message) > 1000 || strings.Contains(message, "\n") {
		t.Fatalf("bounded connector diagnostic = %q/%q", class, message)
	}
}

func TestConnectorProcessingRequiresPool(t *testing.T) {
	_, err := ProcessConnectorTextDelivery(context.Background(), nil, ConnectorTextProcessingInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
	_, err = RecoverConnectorTextProcessing(context.Background(), nil, ConnectorProcessingRecoveryInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func withProcessingRequestID(input ConnectorTextProcessingInput, value string) ConnectorTextProcessingInput {
	input.RequestID = value
	return input
}

func withProcessingDeliveryID(input ConnectorTextProcessingInput, value string) ConnectorTextProcessingInput {
	input.ConnectorDeliveryID = value
	return input
}

func withProcessingWorkerID(input ConnectorTextProcessingInput, value string) ConnectorTextProcessingInput {
	input.WorkerID = value
	return input
}

func withProcessingLease(input ConnectorTextProcessingInput, value int64) ConnectorTextProcessingInput {
	input.LeaseDurationMilliseconds = value
	return input
}
