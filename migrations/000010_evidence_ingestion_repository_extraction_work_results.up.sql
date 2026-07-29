ALTER TABLE repository_extraction_work_items
    DROP CONSTRAINT repository_extraction_work_items_status_ck,
    DROP CONSTRAINT repository_extraction_work_items_state_ck,
    ADD COLUMN source_generation_id TEXT,
    ADD COLUMN finished_at TIMESTAMPTZ,
    ADD COLUMN failure_class TEXT,
    ADD COLUMN failure_message TEXT;

ALTER TABLE repository_extraction_work_items
    ADD CONSTRAINT repository_extraction_work_items_generation_fk
        FOREIGN KEY (source_generation_id, repo_id, extractor_name)
        REFERENCES repository_source_generations(source_generation_id, repo_id, extractor_name),
    ADD CONSTRAINT repository_extraction_work_items_claim_outcome_uq
        UNIQUE (work_item_id, claim_id, claimed_by, status),
    ADD CONSTRAINT repository_extraction_work_items_failure_class_ck
        CHECK (failure_class IS NULL OR octet_length(failure_class) BETWEEN 1 AND 100),
    ADD CONSTRAINT repository_extraction_work_items_failure_message_ck
        CHECK (failure_message IS NULL OR octet_length(failure_message) BETWEEN 1 AND 2000),
    ADD CONSTRAINT repository_extraction_work_items_status_ck
        CHECK (status IN ('pending', 'running', 'superseded', 'succeeded', 'failed')),
    ADD CONSTRAINT repository_extraction_work_items_state_ck CHECK (
        (status = 'pending'
            AND superseded_by_work_item_id IS NULL
            AND claim_id IS NULL
            AND claimed_by IS NULL
            AND claimed_at IS NULL
            AND source_generation_id IS NULL
            AND finished_at IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'running'
            AND superseded_by_work_item_id IS NULL
            AND claim_id IS NOT NULL
            AND claimed_by IS NOT NULL
            AND claimed_at IS NOT NULL
            AND source_generation_id IS NULL
            AND finished_at IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'superseded'
            AND superseded_by_work_item_id IS NOT NULL
            AND claim_id IS NULL
            AND claimed_by IS NULL
            AND claimed_at IS NULL
            AND source_generation_id IS NULL
            AND finished_at IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'succeeded'
            AND superseded_by_work_item_id IS NULL
            AND claim_id IS NOT NULL
            AND claimed_by IS NOT NULL
            AND claimed_at IS NOT NULL
            AND source_generation_id IS NOT NULL
            AND finished_at IS NOT NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'failed'
            AND superseded_by_work_item_id IS NULL
            AND claim_id IS NOT NULL
            AND claimed_by IS NOT NULL
            AND claimed_at IS NOT NULL
            AND source_generation_id IS NULL
            AND finished_at IS NOT NULL
            AND failure_class IS NOT NULL
            AND failure_message IS NOT NULL)
    );

CREATE TABLE repository_extraction_work_finish_requests (
    request_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    worker_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed')),
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT repository_extraction_work_finish_requests_work_outcome_fk
        FOREIGN KEY (work_item_id, claim_id, worker_id, outcome)
        REFERENCES repository_extraction_work_items(work_item_id, claim_id, claimed_by, status)
);
