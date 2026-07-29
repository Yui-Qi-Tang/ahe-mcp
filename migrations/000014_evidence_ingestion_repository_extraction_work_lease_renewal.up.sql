CREATE TABLE repository_extraction_work_lease_renewal_requests (
    request_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    lease_duration_milliseconds BIGINT NOT NULL
        CHECK (lease_duration_milliseconds BETWEEN 1 AND 3600000),
    prior_lease_expires_at TIMESTAMPTZ NOT NULL,
    renewed_at TIMESTAMPTZ NOT NULL,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id),
    CHECK (renewed_at < prior_lease_expires_at),
    CHECK (lease_expires_at > prior_lease_expires_at),
    CHECK (
        lease_expires_at = renewed_at
            + lease_duration_milliseconds * INTERVAL '1 millisecond'
    )
);
