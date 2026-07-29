CREATE TABLE repository_extraction_work_retry_requests (
    request_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    retry_actor_id TEXT NOT NULL
        CHECK (octet_length(retry_actor_id) BETWEEN 1 AND 200),
    disposition TEXT NOT NULL
        CHECK (disposition IN ('requeued', 'superseded')),
    superseded_by_work_item_id TEXT
        REFERENCES repository_extraction_work_items(work_item_id),
    retried_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id),
    CHECK (
        (disposition = 'requeued' AND superseded_by_work_item_id IS NULL)
        OR
        (disposition = 'superseded' AND superseded_by_work_item_id IS NOT NULL)
    )
);
