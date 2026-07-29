CREATE TABLE repository_extraction_worker_tick_requests (
    request_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL
        CHECK (extractor_name IN (
            'repository-go-parser-code-fact',
            'repository-gopls-code-fact'
        )),
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    lease_duration_milliseconds BIGINT NOT NULL
        CHECK (lease_duration_milliseconds BETWEEN 1 AND 3600000),
    claim_request_id TEXT NOT NULL UNIQUE
        REFERENCES repository_extraction_work_claim_requests(request_id),
    execution_request_id TEXT NOT NULL UNIQUE,
    claimed BOOLEAN NOT NULL,
    work_item_id TEXT,
    claim_id TEXT,
    attempt_number INTEGER,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id),
    CHECK (
        (claimed AND work_item_id IS NOT NULL AND claim_id IS NOT NULL AND attempt_number > 0)
        OR
        (NOT claimed AND work_item_id IS NULL AND claim_id IS NULL AND attempt_number IS NULL)
    )
);
