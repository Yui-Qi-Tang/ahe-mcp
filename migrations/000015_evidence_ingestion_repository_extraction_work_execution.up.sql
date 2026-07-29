CREATE TABLE repository_extraction_work_execution_requests (
    request_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL UNIQUE,
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    repository_snapshot_request_id TEXT NOT NULL UNIQUE,
    extractor_request_id TEXT NOT NULL UNIQUE,
    finish_request_id TEXT NOT NULL UNIQUE,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id)
);
