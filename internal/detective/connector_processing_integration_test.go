//go:build integration

package detective

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationProcessConnectorTextDeliveryCapturesExactSourceAndReplays(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	payload := []byte("first connector requirement\n\nsecond connector requirement\r\n")
	receipt := receiveConnectorProcessingFixture(t, ctx, pool, "round-trip", ConnectorDeliveryContentTypeText, payload)
	input := ConnectorTextProcessingInput{
		RequestID:                 "process-connector-round-trip",
		ConnectorDeliveryID:       receipt.ConnectorDeliveryID,
		WorkerID:                  "worker-connector-round-trip",
		LeaseDurationMilliseconds: 60_000,
	}

	first, err := ProcessConnectorTextDelivery(ctx, pool, input)
	if err != nil {
		t.Fatalf("ProcessConnectorTextDelivery() error = %v", err)
	}
	if first.Status != ConnectorProcessingStatusSucceeded || !first.ClaimCreated || first.Reused || first.Replayed ||
		first.AttemptNumber != 1 || first.FinishedAt == nil || first.SourceSnapshotID == "" ||
		first.ExtractionViewID == "" || first.RawContentHash != contentHash(payload) ||
		first.SourceIntakeRequestID != connectorTextSourceIntakeRequestID(receipt.ConnectorDeliveryID) {
		t.Fatalf("first connector text processing result = %+v", first)
	}

	var sourceSystem, sourceID, sourceVersion, rawHash string
	var raw, rendered []byte
	var rendererName, rendererVersion, spanCatalogVersion, intakeRequestID string
	var originData []byte
	if err := pool.QueryRow(ctx, `
		SELECT
			snapshot.source_system, snapshot.source_id, snapshot.source_version,
			snapshot.raw_content_hash, blob.raw_content, snapshot.origin_metadata,
			view.rendered_content, view.renderer_name, view.renderer_version,
			span.span_catalog_version, intake.request_id
		FROM source_snapshots AS snapshot
		JOIN source_blobs AS blob
		  ON blob.raw_content_hash = snapshot.raw_content_hash
		JOIN extraction_views AS view
		  ON view.source_snapshot_id = snapshot.source_snapshot_id
		JOIN span_catalog_entries AS span
		  ON span.extraction_view_id = view.extraction_view_id
		JOIN source_intake_requests AS intake
		  ON intake.source_snapshot_id = snapshot.source_snapshot_id
		 AND intake.extraction_view_id = view.extraction_view_id
		WHERE snapshot.source_snapshot_id = $1
		ORDER BY span.display_line
		LIMIT 1
	`, first.SourceSnapshotID).Scan(
		&sourceSystem, &sourceID, &sourceVersion, &rawHash, &raw, &originData,
		&rendered, &rendererName, &rendererVersion, &spanCatalogVersion,
		&intakeRequestID,
	); err != nil {
		t.Fatalf("read connector text source authority: %v", err)
	}
	if sourceSystem != evidenceingestion.SourceSystemConnectorText ||
		sourceID != connectorTextSourceID(receipt.ConnectorID, receipt.ExternalDeliveryID) ||
		sourceVersion != receipt.PayloadHash || rawHash != receipt.PayloadHash ||
		!bytes.Equal(raw, payload) || !bytes.Equal(rendered, payload) ||
		rendererName != evidenceingestion.RendererConnectorTextIdentity ||
		rendererVersion != evidenceingestion.RendererConnectorTextIdentityVersion ||
		spanCatalogVersion != evidenceingestion.SpanCatalogConnectorTextLineV1 ||
		intakeRequestID != first.SourceIntakeRequestID {
		t.Fatalf("connector text source authority = %s/%s/%s/%s/%q/%q/%s/%s/%s/%s", sourceSystem, sourceID, sourceVersion, rawHash, raw, rendered, rendererName, rendererVersion, spanCatalogVersion, intakeRequestID)
	}
	var origin map[string]string
	if err := json.Unmarshal(originData, &origin); err != nil {
		t.Fatalf("decode connector text origin: %v", err)
	}
	wantOrigin := map[string]string{
		"connector_adapter_name":    ConnectorTextAdapterName,
		"connector_adapter_version": ConnectorTextAdapterVersion,
		"connector_delivery_id":     receipt.ConnectorDeliveryID,
		"connector_id":              receipt.ConnectorID,
		"connector_payload_hash":    receipt.PayloadHash,
		"connector_content_type":    receipt.ContentType,
		"external_delivery_id":      receipt.ExternalDeliveryID,
	}
	if !reflect.DeepEqual(origin, wantOrigin) {
		t.Fatalf("connector text origin = %+v, want %+v", origin, wantOrigin)
	}

	replay, err := ProcessConnectorTextDelivery(ctx, pool, input)
	if err != nil {
		t.Fatalf("replay ProcessConnectorTextDelivery() error = %v", err)
	}
	first.Replayed = true
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("connector processing replay = %+v, want %+v", replay, first)
	}
	changedRequest := input
	changedRequest.WorkerID = "worker-connector-round-trip-changed"
	_, err = ProcessConnectorTextDelivery(ctx, pool, changedRequest)
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)

	secondInput := input
	secondInput.RequestID = "process-connector-round-trip-again"
	secondInput.WorkerID = "worker-connector-round-trip-again"
	reused, err := ProcessConnectorTextDelivery(ctx, pool, secondInput)
	if err != nil {
		t.Fatalf("reused ProcessConnectorTextDelivery() error = %v", err)
	}
	if !reused.Reused || reused.ClaimCreated || reused.Replayed ||
		reused.ClaimID != first.ClaimID || reused.WorkerID != first.WorkerID ||
		reused.SourceSnapshotID != first.SourceSnapshotID {
		t.Fatalf("reused connector processing result = %+v", reused)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_work", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_requests", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertDetectiveTableCount(t, ctx, pool, "extraction_attempts", 0)
	assertDetectiveTableCount(t, ctx, pool, "proposal_occurrences", 0)
}

func TestIntegrationProcessConnectorTextDeliveryRejectsJSONWithoutCreatingWork(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	receipt := receiveConnectorProcessingFixture(t, ctx, pool, "json", ConnectorDeliveryContentTypeJSON, []byte(`{"event":"updated"}`))
	_, err := ProcessConnectorTextDelivery(ctx, pool, ConnectorTextProcessingInput{
		RequestID:                 "process-connector-json",
		ConnectorDeliveryID:       receipt.ConnectorDeliveryID,
		WorkerID:                  "worker-connector-json",
		LeaseDurationMilliseconds: 60_000,
	})
	assertDetectiveKind(t, err, ErrorUnsupportedCapability)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_work", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 0)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_requests", 0)
	assertDetectiveTableCount(t, ctx, pool, "source_snapshots", 0)
}

func TestIntegrationProcessConnectorTextDeliveryFailureIsRetryableNotDeadLettered(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	receipt := receiveConnectorProcessingFixture(t, ctx, pool, "invalid-utf8", ConnectorDeliveryContentTypeText, []byte{0xff})
	input := ConnectorTextProcessingInput{
		RequestID:                 "process-connector-invalid-utf8",
		ConnectorDeliveryID:       receipt.ConnectorDeliveryID,
		WorkerID:                  "worker-connector-invalid-utf8",
		LeaseDurationMilliseconds: 60_000,
	}
	if _, err := ProcessConnectorTextDelivery(ctx, pool, input); err == nil {
		t.Fatal("ProcessConnectorTextDelivery(invalid UTF-8) error = nil")
	}

	var workStatus, attemptStatus, requestStatus, failureClass string
	var attemptCount int
	var claimID *string
	if err := pool.QueryRow(ctx, `
		SELECT status, attempt_count, claim_id
		FROM detective_connector_inbox_processing_work
		WHERE connector_delivery_id = $1
	`, receipt.ConnectorDeliveryID).Scan(&workStatus, &attemptCount, &claimID); err != nil {
		t.Fatalf("read failed connector processing work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT status, failure_class
		FROM detective_connector_inbox_processing_attempts
		WHERE connector_delivery_id = $1 AND attempt_number = 1
	`, receipt.ConnectorDeliveryID).Scan(&attemptStatus, &failureClass); err != nil {
		t.Fatalf("read failed connector processing attempt: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT status
		FROM detective_connector_inbox_processing_requests
		WHERE request_id = $1
	`, input.RequestID).Scan(&requestStatus); err != nil {
		t.Fatalf("read failed connector processing request: %v", err)
	}
	if workStatus != connectorProcessingStatusPending || attemptCount != 1 || claimID != nil ||
		attemptStatus != ConnectorProcessingStatusFailed || requestStatus != ConnectorProcessingStatusFailed ||
		failureClass != "evidence_ingestion_invalid_utf8" {
		t.Fatalf("failed connector processing state = %s/%d/%v/%s/%s/%s", workStatus, attemptCount, claimID, attemptStatus, requestStatus, failureClass)
	}

	_, err := ProcessConnectorTextDelivery(ctx, pool, input)
	assertDetectiveKind(t, err, ErrorConnectorProcessingFailed)
	retry := input
	retry.RequestID = "process-connector-invalid-utf8-again"
	if _, err := ProcessConnectorTextDelivery(ctx, pool, retry); err == nil {
		t.Fatal("retry ProcessConnectorTextDelivery(invalid UTF-8) error = nil")
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_requests", 2)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 0)
}

func TestIntegrationClaimConnectorTextProcessingHasOneConcurrentWinner(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	receipt := receiveConnectorProcessingFixture(t, ctx, pool, "concurrent-claim", ConnectorDeliveryContentTypeText, []byte("concurrent claim\n"))
	inputs := []ConnectorTextProcessingInput{
		{
			RequestID: "claim-connector-concurrent-a", ConnectorDeliveryID: receipt.ConnectorDeliveryID,
			WorkerID: "worker-connector-concurrent-a", LeaseDurationMilliseconds: 60_000,
		},
		{
			RequestID: "claim-connector-concurrent-b", ConnectorDeliveryID: receipt.ConnectorDeliveryID,
			WorkerID: "worker-connector-concurrent-b", LeaseDurationMilliseconds: 60_000,
		},
	}
	type outcome struct {
		claim connectorProcessingClaim
		err   error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(inputs))
	claimedAt := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	for _, input := range inputs {
		prepared, err := prepareConnectorProcessingRequest(input)
		if err != nil {
			t.Fatalf("prepare concurrent claim: %v", err)
		}
		go func() {
			<-start
			claim, err := claimConnectorTextProcessing(ctx, pool, prepared, claimedAt)
			outcomes <- outcome{claim: claim, err: err}
		}()
	}
	close(start)

	winners, conflicts := 0, 0
	for range inputs {
		got := <-outcomes
		if got.err == nil {
			winners++
			if !got.claim.execute || !got.claim.result.ClaimCreated {
				t.Fatalf("concurrent claim winner = %+v", got.claim.result)
			}
			continue
		}
		if kind, ok := KindOf(got.err); !ok || kind != ErrorConnectorProcessingConflict {
			t.Fatalf("concurrent connector claim error = %v", got.err)
		}
		conflicts++
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent connector claims = %d winners/%d conflicts", winners, conflicts)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_requests", 1)
}

func TestIntegrationProcessConnectorTextDeliveryConcurrentSameRequestConverges(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	receipt := receiveConnectorProcessingFixture(t, ctx, pool, "same-request", ConnectorDeliveryContentTypeText, []byte("same request\n"))
	input := ConnectorTextProcessingInput{
		RequestID:                 "process-connector-same-request",
		ConnectorDeliveryID:       receipt.ConnectorDeliveryID,
		WorkerID:                  "worker-connector-same-request",
		LeaseDurationMilliseconds: 60_000,
	}
	type outcome struct {
		result ConnectorTextProcessingResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := ProcessConnectorTextDelivery(ctx, pool, input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)

	replayed := 0
	var first ConnectorTextProcessingResult
	for range 2 {
		got := <-outcomes
		if got.err != nil {
			t.Fatalf("concurrent same-request processing error = %v", got.err)
		}
		if got.result.Status != ConnectorProcessingStatusSucceeded {
			t.Fatalf("concurrent same-request result = %+v", got.result)
		}
		if got.result.Replayed {
			replayed++
		}
		if first.ClaimID == "" {
			first = got.result
		} else if first.ClaimID != got.result.ClaimID || first.SourceSnapshotID != got.result.SourceSnapshotID {
			t.Fatalf("concurrent same-request results disagree: %+v / %+v", first, got.result)
		}
	}
	if replayed != 1 {
		t.Fatalf("concurrent same-request replay count = %d, want 1", replayed)
	}
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_requests", 1)
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 1)
}

func TestIntegrationRecoverConnectorTextProcessingResumesCaptureFinishGap(t *testing.T) {
	ctx, pool := detectiveIntegrationPool(t)
	receipt := receiveConnectorProcessingFixture(t, ctx, pool, "recover", ConnectorDeliveryContentTypeText, []byte("recover exact intake\n"))
	input := ConnectorTextProcessingInput{
		RequestID:                 "process-connector-recover-first",
		ConnectorDeliveryID:       receipt.ConnectorDeliveryID,
		WorkerID:                  "worker-connector-recover-first",
		LeaseDurationMilliseconds: 1000,
	}
	prepared, err := prepareConnectorProcessingRequest(input)
	if err != nil {
		t.Fatalf("prepare first connector processing: %v", err)
	}
	claimedAt := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	claim, err := claimConnectorTextProcessing(ctx, pool, prepared, claimedAt)
	if err != nil {
		t.Fatalf("claimConnectorTextProcessing() error = %v", err)
	}
	intake, err := captureConnectorTextSource(ctx, pool, claim.delivery)
	if err != nil {
		t.Fatalf("captureConnectorTextSource() error = %v", err)
	}

	recoveryInput := ConnectorProcessingRecoveryInput{
		RequestID:           "recover-connector-processing",
		ConnectorDeliveryID: receipt.ConnectorDeliveryID,
		ClaimID:             claim.result.ClaimID,
		WorkerID:            input.WorkerID,
	}
	preparedRecovery, err := prepareConnectorRecoveryRequest(recoveryInput)
	if err != nil {
		t.Fatalf("prepare connector recovery: %v", err)
	}
	_, err = recoverConnectorTextProcessing(ctx, pool, preparedRecovery, claimedAt.Add(999*time.Millisecond))
	assertDetectiveKind(t, err, ErrorConnectorProcessingLeaseActive)
	recovered, err := recoverConnectorTextProcessing(ctx, pool, preparedRecovery, claimedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("recoverConnectorTextProcessing() error = %v", err)
	}
	if recovered.AttemptNumber != 1 || recovered.Replayed {
		t.Fatalf("connector processing recovery = %+v", recovered)
	}
	recoveryReplay, err := recoverConnectorTextProcessing(ctx, pool, preparedRecovery, claimedAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("replay recoverConnectorTextProcessing() error = %v", err)
	}
	if !recoveryReplay.Replayed || recoveryReplay.RecoveredAt != recovered.RecoveredAt {
		t.Fatalf("connector processing recovery replay = %+v", recoveryReplay)
	}
	changedRecoveryInput := recoveryInput
	changedRecoveryInput.WorkerID = "worker-connector-recover-changed"
	changedRecovery, err := prepareConnectorRecoveryRequest(changedRecoveryInput)
	if err != nil {
		t.Fatalf("prepare changed connector recovery: %v", err)
	}
	_, err = recoverConnectorTextProcessing(ctx, pool, changedRecovery, claimedAt.Add(2*time.Second))
	assertDetectiveKind(t, err, ErrorIdempotencyKeyReused)
	_, err = claimConnectorTextProcessing(ctx, pool, prepared, claimedAt.Add(2*time.Second))
	assertDetectiveKind(t, err, ErrorConnectorProcessingLeaseExpired)

	secondInput := input
	secondInput.RequestID = "process-connector-recover-second"
	secondInput.WorkerID = "worker-connector-recover-second"
	secondInput.LeaseDurationMilliseconds = 60_000
	result, err := ProcessConnectorTextDelivery(ctx, pool, secondInput)
	if err != nil {
		t.Fatalf("second ProcessConnectorTextDelivery() error = %v", err)
	}
	if result.AttemptNumber != 2 || result.Status != ConnectorProcessingStatusSucceeded || result.SourceSnapshotID != intake.SourceSnapshotID {
		t.Fatalf("recovered connector processing result = %+v, intake %+v", result, intake)
	}
	assertDetectiveTableCount(t, ctx, pool, "source_intake_requests", 1)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_attempts", 2)
	assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_processing_recovery_requests", 1)
	var expired, succeeded int
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'expired'),
			COUNT(*) FILTER (WHERE status = 'succeeded')
		FROM detective_connector_inbox_processing_attempts
	`).Scan(&expired, &succeeded); err != nil {
		t.Fatalf("count recovered connector attempts: %v", err)
	}
	if expired != 1 || succeeded != 1 {
		t.Fatalf("recovered connector attempt states = %d expired/%d succeeded", expired, succeeded)
	}
}

func receiveConnectorProcessingFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
	contentType string,
	payload []byte,
) ConnectorDeliveryReceipt {
	t.Helper()
	receipt, err := ReceiveConnectorDelivery(ctx, pool, ConnectorDeliveryInput{
		RequestID:          "receive-connector-processing-" + suffix,
		ConnectorID:        "connector-processing-" + suffix,
		ExternalDeliveryID: "delivery-processing-" + suffix,
		ContentType:        contentType,
		Payload:            payload,
	})
	if err != nil {
		t.Fatalf("ReceiveConnectorDelivery(%s) error = %v", suffix, err)
	}
	return receipt
}
