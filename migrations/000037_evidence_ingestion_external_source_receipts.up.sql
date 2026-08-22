CREATE TABLE external_source_intake_receipts (
    request_id TEXT PRIMARY KEY REFERENCES source_intake_requests(request_id),
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(source_snapshot_id),
    extraction_view_id TEXT NOT NULL REFERENCES extraction_views(extraction_view_id),
    envelope_schema_version TEXT NOT NULL CHECK (btrim(envelope_schema_version) <> ''),
    collector_id TEXT NOT NULL CHECK (btrim(collector_id) <> ''),
    connector_id TEXT NOT NULL CHECK (btrim(connector_id) <> ''),
    observed_at TIMESTAMPTZ NOT NULL,
    receipt_payload_hash TEXT NOT NULL CHECK (receipt_payload_hash LIKE 'sha256:%'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX external_source_intake_receipts_source_observed_idx
    ON external_source_intake_receipts (source_snapshot_id, observed_at DESC, request_id);
