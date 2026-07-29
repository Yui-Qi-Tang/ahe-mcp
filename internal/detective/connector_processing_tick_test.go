package detective

import (
	"context"
	"testing"
	"time"
)

func TestPrepareConnectorTextProcessingTickCanonicalizesAndBounds(t *testing.T) {
	prepared, err := prepareConnectorTextProcessingTick(ConnectorTextProcessingTickInput{
		ConnectorID: " connector-primary ", MaxDeliveries: 7,
	})
	if err != nil {
		t.Fatalf("prepareConnectorTextProcessingTick() error = %v", err)
	}
	if prepared.connectorID != "connector-primary" || prepared.maxDeliveries != 7 {
		t.Fatalf("prepared connector text processing tick = %+v", prepared)
	}

	for _, input := range []ConnectorTextProcessingTickInput{
		{ConnectorID: "", MaxDeliveries: 1},
		{ConnectorID: "connector-primary", MaxDeliveries: 0},
		{ConnectorID: "connector-primary", MaxDeliveries: maxConnectorTextProcessingTickDeliveries + 1},
	} {
		if _, err := prepareConnectorTextProcessingTick(input); err == nil {
			t.Fatalf("prepareConnectorTextProcessingTick(%+v) error = nil", input)
		}
	}
}

func TestValidateConnectorTextProcessingCandidateAcceptsOnlyNewExpiredOrRunningWork(t *testing.T) {
	connectorID := "connector-primary"
	externalID := "delivery-1"
	deliveryID := connectorDeliveryID(connectorID, externalID)
	newCandidate := connectorTextProcessingCandidate{
		connectorDeliveryID: deliveryID,
		connectorID:         connectorID,
		externalDeliveryID:  externalID,
		adapterName:         ConnectorTextAdapterName,
		adapterVersion:      ConnectorTextAdapterVersion,
		workStatus:          connectorProcessingStatusPending,
	}
	if err := validateConnectorTextProcessingCandidate(newCandidate, connectorID); err != nil {
		t.Fatalf("validate new candidate: %v", err)
	}

	claimedAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	leaseExpiresAt := claimedAt.Add(time.Minute)
	running := newCandidate
	running.workStatus = ConnectorProcessingStatusRunning
	running.attemptCount = 1
	running.workClaimID = connectorProcessingTestClaimID("running")
	running.attemptClaimID = running.workClaimID
	running.attemptNumber = 1
	running.attemptWorkerID = "worker-running"
	running.attemptStatus = ConnectorProcessingStatusRunning
	running.leaseDurationMillis = 60_000
	running.claimedAt = &claimedAt
	running.leaseExpiresAt = &leaseExpiresAt
	running.attemptRequestCount = 1
	running.attemptRequestID = "request-running"
	running.attemptRequestStatus = ConnectorProcessingStatusRunning
	running.requestWorkerID = running.attemptWorkerID
	running.requestLeaseMillis = running.leaseDurationMillis
	if err := validateConnectorTextProcessingCandidate(running, connectorID); err != nil {
		t.Fatalf("validate running candidate: %v", err)
	}

	expired := running
	expired.workStatus = connectorProcessingStatusPending
	expired.workClaimID = ""
	expired.attemptStatus = ConnectorProcessingStatusExpired
	expired.attemptRequestStatus = ConnectorProcessingStatusExpired
	if err := validateConnectorTextProcessingCandidate(expired, connectorID); err != nil {
		t.Fatalf("validate expired candidate: %v", err)
	}

	failed := expired
	failed.attemptStatus = ConnectorProcessingStatusFailed
	if err := validateConnectorTextProcessingCandidate(failed, connectorID); err == nil {
		t.Fatal("validate failed pending candidate error = nil")
	}
}

func TestConnectorTextProcessingTickIdentitiesAreDeterministicAndAttemptScoped(t *testing.T) {
	connectorID := "connector-primary"
	deliveryID := connectorDeliveryID(connectorID, "delivery-1")
	workerID := connectorTextTickWorkerID(connectorID)
	requestID := connectorTextTickProcessingRequestID(deliveryID, 1)
	if workerID != connectorTextTickWorkerID(connectorID) ||
		requestID != connectorTextTickProcessingRequestID(deliveryID, 1) {
		t.Fatal("connector text tick identities are not deterministic")
	}
	if requestID == connectorTextTickProcessingRequestID(deliveryID, 2) {
		t.Fatal("connector text tick processing request is not attempt scoped")
	}
	claimID := connectorProcessingTestClaimID("recover")
	recoveryRequestID := connectorTextTickRecoveryRequestID(claimID)
	if recoveryRequestID != connectorTextTickRecoveryRequestID(claimID) {
		t.Fatal("connector text tick recovery identity is not deterministic")
	}
	if recoveryRequestID == connectorTextTickRecoveryRequestID(connectorProcessingTestClaimID("other")) {
		t.Fatal("connector text tick recovery identity is not claim scoped")
	}
}

func TestRunConnectorTextProcessingTickRequiresPool(t *testing.T) {
	_, err := RunConnectorTextProcessingTick(context.Background(), nil, ConnectorTextProcessingTickInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}

func connectorProcessingTestClaimID(suffix string) string {
	return "detective-connector-claim:" + hashHex([]byte("connector-processing-test-claim\x00"+suffix))
}
