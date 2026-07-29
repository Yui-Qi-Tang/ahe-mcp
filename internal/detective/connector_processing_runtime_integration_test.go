//go:build integration

package detective

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationConnectorTextProcessingTickBoundsConnectorAndLeavesFailedRetryExplicit(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	connectorID := "connector-runtime-main"
	validA := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "valid-a", []byte("valid A\n"))
	invalid := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "invalid", []byte{0xff})
	validB := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "valid-b", []byte("valid B\n"))
	other := receiveConnectorRuntimeFixture(t, ctx, pool, "connector-runtime-other", "valid", []byte("other\n"))

	result, err := RunConnectorTextProcessingTick(ctx, pool, ConnectorTextProcessingTickInput{
		ConnectorID: connectorID, MaxDeliveries: 10,
	})
	if err == nil {
		t.Fatal("RunConnectorTextProcessingTick() error = nil, want invalid UTF-8 item error")
	}
	if result.SelectedCount != 3 || result.SucceededCount != 2 || result.FailedCount != 1 ||
		result.RecoveredCount != 0 || len(result.Items) != 3 {
		t.Fatalf("connector processing tick result = %+v", result)
	}
	seen := map[string]ConnectorTextProcessingTickItem{}
	for _, item := range result.Items {
		seen[item.ConnectorDeliveryID] = item
	}
	for _, receipt := range []ConnectorDeliveryReceipt{validA, validB} {
		item := seen[receipt.ConnectorDeliveryID]
		if item.Action != ConnectorTextProcessingTickActionProcess || item.Processing == nil ||
			item.Processing.Status != ConnectorProcessingStatusSucceeded || item.FailureClass != "" {
			t.Fatalf("successful connector tick item = %+v", item)
		}
	}
	if item := seen[invalid.ConnectorDeliveryID]; item.FailureClass != "evidence_ingestion_invalid_utf8" ||
		item.Processing != nil {
		t.Fatalf("failed connector tick item = %+v", item)
	}
	if _, found := seen[other.ConnectorDeliveryID]; found {
		t.Fatal("connector tick selected a different connector's delivery")
	}

	retryProbe, err := RunConnectorTextProcessingTick(ctx, pool, ConnectorTextProcessingTickInput{
		ConnectorID: connectorID, MaxDeliveries: 10,
	})
	if err != nil || retryProbe.SelectedCount != 0 {
		t.Fatalf("failed-work automatic retry probe = %+v, %v", retryProbe, err)
	}
	otherResult, err := RunConnectorTextProcessingTick(ctx, pool, ConnectorTextProcessingTickInput{
		ConnectorID: other.ConnectorID, MaxDeliveries: 1,
	})
	if err != nil || otherResult.SucceededCount != 1 || otherResult.Items[0].ConnectorDeliveryID != other.ConnectorDeliveryID {
		t.Fatalf("other connector tick = %+v, %v", otherResult, err)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 4)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 3)
}

func TestIntegrationConnectorTextProcessingTickResumesAndRecoversDurableClaims(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	connectorID := "connector-runtime-recovery"
	activeReceipt := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "active", []byte("active\n"))
	expiredReceipt := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "expired", []byte("expired\n"))
	recoveredReceipt := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "already-recovered", []byte("already recovered\n"))

	activeInput := ConnectorTextProcessingInput{
		RequestID: "connector-runtime-active-request", ConnectorDeliveryID: activeReceipt.ConnectorDeliveryID,
		WorkerID: "connector-runtime-active-worker", LeaseDurationMilliseconds: 60_000,
	}
	activeClaim := claimConnectorRuntimeFixture(t, ctx, pool, activeInput, time.Now().UTC())
	expiredInput := ConnectorTextProcessingInput{
		RequestID: "connector-runtime-expired-request", ConnectorDeliveryID: expiredReceipt.ConnectorDeliveryID,
		WorkerID: "connector-runtime-expired-worker", LeaseDurationMilliseconds: 1,
	}
	expiredClaim := claimConnectorRuntimeFixture(t, ctx, pool, expiredInput, time.Now().UTC().Add(-time.Second))
	recoveredInput := ConnectorTextProcessingInput{
		RequestID: "connector-runtime-recovered-request", ConnectorDeliveryID: recoveredReceipt.ConnectorDeliveryID,
		WorkerID: "connector-runtime-recovered-worker", LeaseDurationMilliseconds: 1,
	}
	recoveredClaim := claimConnectorRuntimeFixture(t, ctx, pool, recoveredInput, time.Now().UTC().Add(-time.Second))
	if _, err := RecoverConnectorTextProcessing(ctx, pool, ConnectorProcessingRecoveryInput{
		RequestID: "connector-runtime-pre-recovery", ConnectorDeliveryID: recoveredReceipt.ConnectorDeliveryID,
		ClaimID: recoveredClaim.result.ClaimID, WorkerID: recoveredInput.WorkerID,
	}); err != nil {
		t.Fatalf("pre-recover connector processing: %v", err)
	}

	result, err := RunConnectorTextProcessingTick(ctx, pool, ConnectorTextProcessingTickInput{
		ConnectorID: connectorID, MaxDeliveries: 10,
	})
	if err != nil {
		t.Fatalf("RunConnectorTextProcessingTick() error = %v", err)
	}
	if result.SelectedCount != 3 || result.SucceededCount != 3 || result.FailedCount != 0 ||
		result.RecoveredCount != 1 {
		t.Fatalf("recovery connector processing tick = %+v", result)
	}
	items := make(map[string]ConnectorTextProcessingTickItem, len(result.Items))
	for _, item := range result.Items {
		items[item.ConnectorDeliveryID] = item
	}
	activeItem := items[activeReceipt.ConnectorDeliveryID]
	if activeItem.Action != ConnectorTextProcessingTickActionResume || activeItem.Processing == nil ||
		activeItem.Processing.RequestID != activeInput.RequestID || activeItem.Processing.ClaimID != activeClaim.result.ClaimID {
		t.Fatalf("active connector processing resume = %+v", activeItem)
	}
	expiredItem := items[expiredReceipt.ConnectorDeliveryID]
	if expiredItem.Action != ConnectorTextProcessingTickActionRecoverAndProcess || expiredItem.Recovery == nil ||
		expiredItem.Recovery.ClaimID != expiredClaim.result.ClaimID || expiredItem.Processing == nil ||
		expiredItem.Processing.AttemptNumber != 2 {
		t.Fatalf("expired connector processing recovery = %+v", expiredItem)
	}
	recoveredItem := items[recoveredReceipt.ConnectorDeliveryID]
	if recoveredItem.Action != ConnectorTextProcessingTickActionProcess || recoveredItem.Recovery != nil ||
		recoveredItem.Processing == nil || recoveredItem.Processing.AttemptNumber != 2 {
		t.Fatalf("pre-recovered connector processing restart = %+v", recoveredItem)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 5)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_recovery_requests", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 3)
}

func TestIntegrationConnectorTextProcessingTickConcurrentInvocationsConverge(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	receipt := receiveConnectorRuntimeFixture(t, ctx, pool, "connector-runtime-concurrent", "one", []byte("concurrent\n"))
	input := ConnectorTextProcessingTickInput{ConnectorID: receipt.ConnectorID, MaxDeliveries: 1}
	type outcome struct {
		result ConnectorTextProcessingTickResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := RunConnectorTextProcessingTick(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	selected := 0
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent connector processing tick error = %v", got.err)
		}
		selected += got.result.SelectedCount
	}
	if selected < 1 || selected > 2 {
		t.Fatalf("concurrent connector processing selected count = %d", selected)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_requests", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 1)

	var requestID, workerID string
	if err := pool.QueryRow(ctx, `
		SELECT request_id, worker_id
		FROM detective_connector_inbox_processing_requests
		WHERE connector_delivery_id = $1
	`, receipt.ConnectorDeliveryID).Scan(&requestID, &workerID); err != nil {
		t.Fatalf("read concurrent connector processing request: %v", err)
	}
	if requestID != connectorTextTickProcessingRequestID(receipt.ConnectorDeliveryID, 1) ||
		workerID != connectorTextTickWorkerID(receipt.ConnectorID) {
		t.Fatalf("concurrent connector processing authority = %s/%s", requestID, workerID)
	}
}

func TestIntegrationConnectorTextProcessingRuntimeInitialWakeAndRestartDiscovery(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	connectorID := "connector-runtime-restart"
	firstReceipt := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "first", []byte("first\n"))
	config := ConnectorTextProcessingRuntimeConfig{
		ConnectorIDs: []string{connectorID}, MaxDeliveries: 10, Interval: time.Hour,
	}
	first, err := NewConnectorTextProcessingRuntime(ctx, pool, config)
	if err != nil {
		t.Fatalf("NewConnectorTextProcessingRuntime() error = %v", err)
	}
	firstOutcome := waitForConnectorTextProcessingRuntimeOutcome(t, first.Outcomes())
	if firstOutcome.Err != nil || firstOutcome.Result.SucceededCount != 1 ||
		firstOutcome.Result.Items[0].ConnectorDeliveryID != firstReceipt.ConnectorDeliveryID {
		t.Fatalf("initial connector processing runtime outcome = %+v", firstOutcome)
	}
	first.Close()

	secondReceipt := receiveConnectorRuntimeFixture(t, ctx, pool, connectorID, "second", []byte("second\n"))
	restarted, err := NewConnectorTextProcessingRuntime(ctx, pool, config)
	if err != nil {
		t.Fatalf("restarted NewConnectorTextProcessingRuntime() error = %v", err)
	}
	t.Cleanup(restarted.Close)
	secondOutcome := waitForConnectorTextProcessingRuntimeOutcome(t, restarted.Outcomes())
	if secondOutcome.Err != nil || secondOutcome.Result.SelectedCount != 1 ||
		secondOutcome.Result.Items[0].ConnectorDeliveryID != secondReceipt.ConnectorDeliveryID {
		t.Fatalf("restarted connector processing runtime outcome = %+v", secondOutcome)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 2)
}

func receiveConnectorRuntimeFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	connectorID string,
	suffix string,
	payload []byte,
) ConnectorDeliveryReceipt {
	t.Helper()
	receipt, err := ReceiveConnectorDelivery(ctx, pool, ConnectorDeliveryInput{
		RequestID:   "receive-connector-runtime-" + connectorID + "-" + suffix,
		ConnectorID: connectorID, ExternalDeliveryID: "delivery-" + suffix,
		ContentType: ConnectorDeliveryContentTypeText, Payload: payload,
	})
	if err != nil {
		t.Fatalf("ReceiveConnectorDelivery(%s/%s) error = %v", connectorID, suffix, err)
	}
	return receipt
}

func claimConnectorRuntimeFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	input ConnectorTextProcessingInput,
	claimedAt time.Time,
) connectorProcessingClaim {
	t.Helper()
	prepared, err := prepareConnectorProcessingRequest(input)
	if err != nil {
		t.Fatalf("prepare connector runtime claim: %v", err)
	}
	claim, err := claimConnectorTextProcessing(ctx, pool, prepared, claimedAt)
	if err != nil {
		t.Fatalf("claim connector runtime work: %v", err)
	}
	return claim
}

func waitForConnectorTextProcessingRuntimeOutcome(
	t *testing.T,
	outcomes <-chan ConnectorTextProcessingRuntimeOutcome,
) ConnectorTextProcessingRuntimeOutcome {
	t.Helper()
	select {
	case outcome, ok := <-outcomes:
		if !ok {
			t.Fatal("connector processing runtime outcomes closed early")
		}
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("connector processing runtime outcome timed out")
		return ConnectorTextProcessingRuntimeOutcome{Err: errors.New("unreachable timeout")}
	}
}
