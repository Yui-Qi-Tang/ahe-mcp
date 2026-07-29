ALTER TABLE repository_extraction_work_execution_requests
    ADD CONSTRAINT repo_work_execution_repair_authority_key
    UNIQUE (request_id, work_item_id, claim_id, worker_id);

CREATE TABLE repository_extraction_work_execution_repairs (
    request_id TEXT PRIMARY KEY,
    execution_request_id TEXT NOT NULL UNIQUE,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    worker_id TEXT NOT NULL
        CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    repair_actor_id TEXT NOT NULL
        CHECK (octet_length(repair_actor_id) BETWEEN 1 AND 200),
    repair_reason TEXT NOT NULL
        CHECK (repair_reason = 'lease_expired'),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    disposition TEXT NOT NULL
        CHECK (disposition IN ('requeued', 'superseded')),
    superseded_by_work_item_id TEXT
        REFERENCES repository_extraction_work_items(work_item_id),
    repaired_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT repo_work_execution_repair_authority_fk
        FOREIGN KEY (execution_request_id, work_item_id, claim_id, worker_id)
        REFERENCES repository_extraction_work_execution_requests (
            request_id,
            work_item_id,
            claim_id,
            worker_id
        ),
    CHECK (repaired_at >= lease_expires_at),
    CHECK (
        (disposition = 'requeued' AND superseded_by_work_item_id IS NULL)
        OR
        (disposition = 'superseded' AND superseded_by_work_item_id IS NOT NULL)
    )
);
