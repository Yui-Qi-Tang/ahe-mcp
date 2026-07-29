CREATE TABLE repository_extraction_work_failure_policy_decisions (
    request_id TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL UNIQUE,
    decision_actor_id TEXT NOT NULL
        CHECK (octet_length(decision_actor_id) BETWEEN 1 AND 200),
    policy_version TEXT NOT NULL
        CHECK (policy_version = 'repository-retry-policy-v1'),
    failure_class TEXT NOT NULL
        CHECK (octet_length(failure_class) BETWEEN 1 AND 100),
    attempt_number INTEGER NOT NULL
        CHECK (attempt_number > 0),
    decision TEXT NOT NULL
        CHECK (decision IN ('retry', 'stop', 'manual_review', 'exhausted')),
    decision_reason TEXT NOT NULL
        CHECK (octet_length(decision_reason) BETWEEN 1 AND 100),
    max_attempts INTEGER NOT NULL
        CHECK (max_attempts > 0),
    backoff_milliseconds BIGINT NOT NULL
        CHECK (backoff_milliseconds BETWEEN 0 AND 3600000),
    retry_not_before TIMESTAMPTZ,
    decided_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_claim_attempts(work_item_id, claim_id),
    CHECK (
        (decision = 'retry'
            AND attempt_number < max_attempts
            AND backoff_milliseconds > 0
            AND retry_not_before = decided_at + backoff_milliseconds * INTERVAL '1 millisecond')
        OR
        (decision <> 'retry'
            AND backoff_milliseconds = 0
            AND retry_not_before IS NULL)
    )
);
