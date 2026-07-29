CREATE TABLE detective_connector_inbox_deliveries (
    connector_delivery_id TEXT PRIMARY KEY
        CHECK (connector_delivery_id ~ '^detective-connector-delivery:[0-9a-f]{64}$'),
    connector_id TEXT NOT NULL
        CHECK (octet_length(connector_id) BETWEEN 1 AND 200),
    external_delivery_id TEXT NOT NULL
        CHECK (octet_length(external_delivery_id) BETWEEN 1 AND 500),
    content_type TEXT NOT NULL
        CHECK (content_type IN ('application/json', 'text/plain')),
    payload_hash TEXT NOT NULL
        CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    payload_bytes BYTEA NOT NULL,
    byte_length INTEGER NOT NULL
        CHECK (byte_length BETWEEN 0 AND 1048576),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, external_delivery_id),
    UNIQUE (connector_delivery_id, connector_id, external_delivery_id),
    CHECK (octet_length(payload_bytes) = byte_length)
);

CREATE INDEX detective_connector_inbox_deliveries_received_idx
    ON detective_connector_inbox_deliveries (received_at, connector_delivery_id);

CREATE TABLE detective_connector_inbox_receive_requests (
    request_id TEXT PRIMARY KEY
        CHECK (octet_length(request_id) BETWEEN 1 AND 300),
    connector_delivery_id TEXT NOT NULL,
    connector_id TEXT NOT NULL,
    external_delivery_id TEXT NOT NULL,
    delivery_created BOOLEAN NOT NULL,
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ) REFERENCES detective_connector_inbox_deliveries (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    )
);

CREATE INDEX detective_connector_inbox_receive_requests_delivery_idx
    ON detective_connector_inbox_receive_requests (
        connector_delivery_id,
        recorded_at,
        request_id
    );
