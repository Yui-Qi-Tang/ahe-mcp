ALTER TABLE repository_extraction_work_claim_attempts
    DROP CONSTRAINT repository_extraction_work_claim_attempts_status_ck,
    DROP CONSTRAINT repository_extraction_work_claim_attempts_state_ck,
    ADD COLUMN expired_at TIMESTAMPTZ,
    ADD CONSTRAINT repository_extraction_work_claim_attempts_finish_authority_uq
        UNIQUE (work_item_id, claim_id, worker_id, status),
    ADD CONSTRAINT repository_extraction_work_claim_attempts_status_ck
        CHECK (status IN ('running', 'succeeded', 'failed', 'expired')),
    ADD CONSTRAINT repository_extraction_work_claim_attempts_expired_after_lease_ck
        CHECK (expired_at IS NULL OR expired_at >= lease_expires_at),
    ADD CONSTRAINT repository_extraction_work_claim_attempts_state_ck CHECK (
        (status = 'running'
            AND finished_at IS NULL
            AND expired_at IS NULL
            AND source_generation_id IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'succeeded'
            AND finished_at IS NOT NULL
            AND expired_at IS NULL
            AND source_generation_id IS NOT NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'failed'
            AND finished_at IS NOT NULL
            AND expired_at IS NULL
            AND source_generation_id IS NULL
            AND failure_class IS NOT NULL
            AND failure_message IS NOT NULL)
        OR
        (status = 'expired'
            AND finished_at IS NULL
            AND expired_at IS NOT NULL
            AND source_generation_id IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
    );

ALTER TABLE repository_extraction_work_claim_requests
    DROP CONSTRAINT repository_extraction_work_claim_requests_claim_fk,
    ADD CONSTRAINT repository_extraction_work_claim_requests_claim_fk
        FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id);

ALTER TABLE repository_extraction_work_finish_requests
    DROP CONSTRAINT repository_extraction_work_finish_requests_work_outcome_fk,
    ADD CONSTRAINT repository_extraction_work_finish_requests_claim_outcome_fk
        FOREIGN KEY (work_item_id, claim_id, worker_id, outcome)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id, worker_id, status);

CREATE TABLE repository_extraction_work_recovery_requests (
    request_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    recovery_actor_id TEXT NOT NULL
        CHECK (octet_length(recovery_actor_id) BETWEEN 1 AND 200),
    disposition TEXT NOT NULL
        CHECK (disposition IN ('requeued', 'superseded')),
    superseded_by_work_item_id TEXT
        REFERENCES repository_extraction_work_items(work_item_id),
    recovered_at TIMESTAMPTZ NOT NULL,
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
