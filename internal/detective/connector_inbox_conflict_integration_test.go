//go:build integration

package detective

import (
	"bytes"
	"context"
	"testing"
)

func TestIntegrationReceiveConnectorDeliveryRejectsInconsistentUniqueCoordinates(t *testing.T) {
	for _, mode := range []string{"primary_key_identity", "natural_key_missing_expected_id", "payload_hash"} {
		t.Run(mode, func(t *testing.T) {
			ctx, pool := detectiveIntegrationPool(t)
			input := ConnectorDeliveryInput{
				RequestID: "receive-inconsistent", ConnectorID: "connector-inconsistent",
				ExternalDeliveryID: "delivery-inconsistent", ContentType: ConnectorDeliveryContentTypeText,
				Payload: []byte("exact fixture bytes"),
			}
			request, err := prepareConnectorDelivery(input)
			if err != nil {
				t.Fatal(err)
			}
			storedID, storedConnector, storedHash := request.connectorDeliveryID, input.ConnectorID, request.payloadHash
			switch mode {
			case "primary_key_identity":
				storedConnector = "different-connector"
			case "natural_key_missing_expected_id":
				storedID = connectorDeliveryID(input.ConnectorID, "different-delivery")
			case "payload_hash":
				storedHash = contentHash([]byte("different bytes"))
			}
			// These deliberately inconsistent rows satisfy SQL constraints but cannot
			// be produced by the public receipt API. A skipped INSERT is not success
			// until both persisted identity and exact bytes have been checked.
			if _, err := pool.Exec(ctx, `INSERT INTO detective_connector_inbox_deliveries (
				connector_delivery_id, connector_id, external_delivery_id,
				content_type, payload_hash, payload_bytes, byte_length
			) VALUES ($1,$2,$3,$4,$5,$6,$7)`, storedID, storedConnector, input.ExternalDeliveryID,
				input.ContentType, storedHash, input.Payload, len(input.Payload)); err != nil {
				t.Fatal(err)
			}
			ackCalls := 0
			_, err = ReceiveConnectorDeliveryAndAcknowledge(ctx, pool, input, func(context.Context, ConnectorDeliveryReceipt) error {
				ackCalls++
				return nil
			})
			assertDetectiveKind(t, err, ErrorConnectorDeliveryConflict)
			if ackCalls != 0 {
				t.Fatalf("inconsistent delivery acknowledged %d times", ackCalls)
			}
			assertDetectiveTableCount(t, ctx, pool, "detective_connector_inbox_deliveries", 1)
			for _, table := range []string{
				"detective_connector_inbox_receive_requests", "detective_connector_inbox_processing_work",
				"evidence_ingestion_request_serializations", "canonical_graph_nodes", "canonical_graph_edges",
			} {
				assertDetectiveTableCount(t, ctx, pool, table, 0)
			}
			var gotID, gotConnector, gotHash string
			var gotPayload []byte
			if err := pool.QueryRow(ctx, `SELECT connector_delivery_id, connector_id, payload_hash, payload_bytes
				FROM detective_connector_inbox_deliveries`).Scan(&gotID, &gotConnector, &gotHash, &gotPayload); err != nil {
				t.Fatal(err)
			}
			if gotID != storedID || gotConnector != storedConnector || gotHash != storedHash || !bytes.Equal(gotPayload, input.Payload) {
				t.Fatal("failed receipt changed the immutable fixture row")
			}
		})
	}
}
