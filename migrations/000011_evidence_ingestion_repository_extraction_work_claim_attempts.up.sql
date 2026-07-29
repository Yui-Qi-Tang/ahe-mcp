CREATE TABLE repository_extraction_work_claim_attempts (
    claim_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL
        REFERENCES repository_extraction_work_items(work_item_id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    worker_id TEXT NOT NULL CHECK (octet_length(worker_id) BETWEEN 1 AND 200),
    status TEXT NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    source_generation_id TEXT REFERENCES repository_source_generations(source_generation_id),
    failure_class TEXT,
    failure_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (work_item_id, attempt_number),
    CONSTRAINT repository_extraction_work_claim_attempts_work_claim_uq
        UNIQUE (work_item_id, claim_id),
    CHECK (lease_expires_at > claimed_at),
    CHECK (finished_at IS NULL OR finished_at < lease_expires_at),
    CHECK (failure_class IS NULL OR octet_length(failure_class) BETWEEN 1 AND 100),
    CHECK (failure_message IS NULL OR octet_length(failure_message) BETWEEN 1 AND 2000),
    CONSTRAINT repository_extraction_work_claim_attempts_status_ck
        CHECK (status IN ('running', 'succeeded', 'failed')),
    CONSTRAINT repository_extraction_work_claim_attempts_state_ck CHECK (
        (status = 'running'
            AND finished_at IS NULL
            AND source_generation_id IS NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'succeeded'
            AND finished_at IS NOT NULL
            AND source_generation_id IS NOT NULL
            AND failure_class IS NULL
            AND failure_message IS NULL)
        OR
        (status = 'failed'
            AND finished_at IS NOT NULL
            AND source_generation_id IS NULL
            AND failure_class IS NOT NULL
            AND failure_message IS NOT NULL)
    )
);

CREATE UNIQUE INDEX repository_extraction_work_running_attempt_uq
    ON repository_extraction_work_claim_attempts (work_item_id)
    WHERE status = 'running';

INSERT INTO repository_extraction_work_claim_attempts (
    claim_id,
    work_item_id,
    attempt_number,
    worker_id,
    status,
    claimed_at,
    lease_expires_at,
    finished_at,
    source_generation_id,
    failure_class,
    failure_message
)
SELECT
    claim_id,
    work_item_id,
    1,
    claimed_by,
    status,
    claimed_at,
    COALESCE(finished_at, claimed_at) + INTERVAL '1 microsecond',
    finished_at,
    source_generation_id,
    failure_class,
    failure_message
FROM repository_extraction_work_items
WHERE claim_id IS NOT NULL;

ALTER TABLE repository_extraction_work_items
    ADD CONSTRAINT repository_extraction_work_items_claim_attempt_fk
        FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id)
        DEFERRABLE INITIALLY DEFERRED;
