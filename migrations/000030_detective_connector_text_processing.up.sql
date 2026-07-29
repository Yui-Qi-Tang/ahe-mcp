ALTER TABLE extraction_views
    ADD CONSTRAINT extraction_views_snapshot_view_uk
    UNIQUE (source_snapshot_id, extraction_view_id);

ALTER TABLE source_intake_requests
    ADD CONSTRAINT source_intake_requests_exact_result_uk
    UNIQUE (request_id, source_snapshot_id, extraction_view_id);

ALTER TABLE source_snapshots
    ADD CONSTRAINT source_snapshots_id_content_uk
    UNIQUE (source_snapshot_id, raw_content_hash);

CREATE TABLE detective_connector_inbox_processing_work (
    connector_delivery_id TEXT PRIMARY KEY,
    connector_id TEXT NOT NULL,
    external_delivery_id TEXT NOT NULL,
    adapter_name TEXT NOT NULL
        CHECK (adapter_name = 'connector-text-delivery'),
    adapter_version TEXT NOT NULL
        CHECK (adapter_version = 'v1'),
    status TEXT NOT NULL
        CHECK (status IN ('pending', 'running', 'succeeded')),
    attempt_count INTEGER NOT NULL DEFAULT 0
        CHECK (attempt_count BETWEEN 0 AND 1000000),
    claim_id TEXT,
    source_intake_request_id TEXT,
    source_snapshot_id TEXT,
    extraction_view_id TEXT,
    raw_content_hash TEXT,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ),
    FOREIGN KEY (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ) REFERENCES detective_connector_inbox_deliveries (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ),
    FOREIGN KEY (
        source_intake_request_id,
        source_snapshot_id,
        extraction_view_id
    ) REFERENCES source_intake_requests (
        request_id,
        source_snapshot_id,
        extraction_view_id
    ),
    FOREIGN KEY (
        source_snapshot_id,
        raw_content_hash
    ) REFERENCES source_snapshots (
        source_snapshot_id,
        raw_content_hash
    ),
    CHECK (
        (status = 'pending'
            AND claim_id IS NULL
            AND source_intake_request_id IS NULL
            AND source_snapshot_id IS NULL
            AND extraction_view_id IS NULL
            AND raw_content_hash IS NULL
            AND completed_at IS NULL)
        OR
        (status = 'running'
            AND claim_id IS NOT NULL
            AND source_intake_request_id IS NULL
            AND source_snapshot_id IS NULL
            AND extraction_view_id IS NULL
            AND raw_content_hash IS NULL
            AND completed_at IS NULL)
        OR
        (status = 'succeeded'
            AND claim_id IS NOT NULL
            AND source_intake_request_id IS NOT NULL
            AND source_snapshot_id IS NOT NULL
            AND extraction_view_id IS NOT NULL
            AND raw_content_hash IS NOT NULL
            AND completed_at IS NOT NULL)
    )
);

CREATE INDEX detective_connector_inbox_processing_work_pending_idx
    ON detective_connector_inbox_processing_work (
        updated_at,
        connector_delivery_id
    )
    WHERE status = 'pending';

CREATE TABLE detective_connector_inbox_processing_attempts (
    claim_id TEXT PRIMARY KEY
        CHECK (claim_id ~ '^detective-connector-claim:[0-9a-f]{64}$'),
    connector_delivery_id TEXT NOT NULL,
    connector_id TEXT NOT NULL,
    external_delivery_id TEXT NOT NULL,
    attempt_number INTEGER NOT NULL
        CHECK (attempt_number BETWEEN 1 AND 1000000),
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    status TEXT NOT NULL
        CHECK (status IN ('running', 'succeeded', 'failed', 'expired')),
    claimed_at TIMESTAMPTZ NOT NULL,
    lease_duration_milliseconds BIGINT NOT NULL
        CHECK (lease_duration_milliseconds BETWEEN 1 AND 3600000),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    source_intake_request_id TEXT,
    source_snapshot_id TEXT,
    extraction_view_id TEXT,
    raw_content_hash TEXT,
    failure_class TEXT,
    failure_message TEXT,
    UNIQUE (connector_delivery_id, attempt_number),
    UNIQUE (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ),
    FOREIGN KEY (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ) REFERENCES detective_connector_inbox_processing_work (
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ),
    FOREIGN KEY (
        source_intake_request_id,
        source_snapshot_id,
        extraction_view_id
    ) REFERENCES source_intake_requests (
        request_id,
        source_snapshot_id,
        extraction_view_id
    ),
    FOREIGN KEY (
        source_snapshot_id,
        raw_content_hash
    ) REFERENCES source_snapshots (
        source_snapshot_id,
        raw_content_hash
    ),
    CHECK (lease_expires_at > claimed_at),
    CHECK (failure_class IS NULL OR octet_length(failure_class) BETWEEN 1 AND 200),
    CHECK (failure_message IS NULL OR octet_length(failure_message) BETWEEN 1 AND 1000),
    CHECK (
        (status = 'running'
            AND finished_at IS NULL
            AND source_intake_request_id IS NULL
            AND source_snapshot_id IS NULL
            AND extraction_view_id IS NULL
            AND raw_content_hash IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'succeeded'
            AND finished_at IS NOT NULL
            AND source_intake_request_id IS NOT NULL
            AND source_snapshot_id IS NOT NULL
            AND extraction_view_id IS NOT NULL
            AND raw_content_hash IS NOT NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status IN ('failed', 'expired')
            AND finished_at IS NOT NULL
            AND source_intake_request_id IS NULL
            AND source_snapshot_id IS NULL
            AND extraction_view_id IS NULL
            AND raw_content_hash IS NULL
            AND failure_class IS NOT NULL
            AND failure_message IS NOT NULL)
    )
);

ALTER TABLE detective_connector_inbox_processing_work
    ADD CONSTRAINT detective_connector_inbox_processing_work_claim_fk
    FOREIGN KEY (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ) REFERENCES detective_connector_inbox_processing_attempts (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    );

CREATE TABLE detective_connector_inbox_processing_requests (
    request_id TEXT PRIMARY KEY
        CHECK (octet_length(request_id) BETWEEN 1 AND 300),
    connector_delivery_id TEXT NOT NULL,
    connector_id TEXT NOT NULL,
    external_delivery_id TEXT NOT NULL,
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    lease_duration_milliseconds BIGINT NOT NULL
        CHECK (lease_duration_milliseconds BETWEEN 1 AND 3600000),
    claim_id TEXT NOT NULL,
    claim_created BOOLEAN NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN ('running', 'succeeded', 'failed', 'expired')),
    source_intake_request_id TEXT,
    source_snapshot_id TEXT,
    extraction_view_id TEXT,
    raw_content_hash TEXT,
    failure_class TEXT,
    failure_message TEXT,
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ) REFERENCES detective_connector_inbox_processing_attempts (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ),
    FOREIGN KEY (
        source_intake_request_id,
        source_snapshot_id,
        extraction_view_id
    ) REFERENCES source_intake_requests (
        request_id,
        source_snapshot_id,
        extraction_view_id
    ),
    FOREIGN KEY (
        source_snapshot_id,
        raw_content_hash
    ) REFERENCES source_snapshots (
        source_snapshot_id,
        raw_content_hash
    ),
    CHECK (failure_class IS NULL OR octet_length(failure_class) BETWEEN 1 AND 200),
    CHECK (failure_message IS NULL OR octet_length(failure_message) BETWEEN 1 AND 1000),
    CHECK (
        (status = 'running'
            AND source_intake_request_id IS NULL
            AND source_snapshot_id IS NULL
            AND extraction_view_id IS NULL
            AND raw_content_hash IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'succeeded'
            AND source_intake_request_id IS NOT NULL
            AND source_snapshot_id IS NOT NULL
            AND extraction_view_id IS NOT NULL
            AND raw_content_hash IS NOT NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status IN ('failed', 'expired')
            AND source_intake_request_id IS NULL
            AND source_snapshot_id IS NULL
            AND extraction_view_id IS NULL
            AND raw_content_hash IS NULL
            AND failure_class IS NOT NULL
            AND failure_message IS NOT NULL)
    )
);

CREATE INDEX detective_connector_inbox_processing_requests_delivery_idx
    ON detective_connector_inbox_processing_requests (
        connector_delivery_id,
        recorded_at,
        request_id
    );

CREATE TABLE detective_connector_inbox_processing_recovery_requests (
    request_id TEXT PRIMARY KEY
        CHECK (octet_length(request_id) BETWEEN 1 AND 300),
    connector_delivery_id TEXT NOT NULL,
    connector_id TEXT NOT NULL,
    external_delivery_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    recovered_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    ) REFERENCES detective_connector_inbox_processing_attempts (
        claim_id,
        connector_delivery_id,
        connector_id,
        external_delivery_id
    )
);

CREATE INDEX detective_connector_inbox_processing_recovery_delivery_idx
    ON detective_connector_inbox_processing_recovery_requests (
        connector_delivery_id,
        recorded_at,
        request_id
    );

INSERT INTO detective_connector_inbox_processing_work (
    connector_delivery_id,
    connector_id,
    external_delivery_id,
    adapter_name,
    adapter_version,
    status
)
SELECT
    connector_delivery_id,
    connector_id,
    external_delivery_id,
    'connector-text-delivery',
    'v1',
    'pending'
FROM detective_connector_inbox_deliveries
WHERE content_type = 'text/plain';
