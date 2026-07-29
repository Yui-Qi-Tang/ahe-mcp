ALTER TABLE repository_extraction_work_failure_policy_decisions
    ADD CONSTRAINT repo_work_failure_policy_retry_authority_key
    UNIQUE (request_id, work_item_id, claim_id, decision, retry_not_before);

CREATE INDEX repo_work_failure_policy_due_idx
    ON repository_extraction_work_failure_policy_decisions (retry_not_before, request_id)
    WHERE decision = 'retry';

CREATE TABLE repository_extraction_work_retry_decision_consumptions (
    request_id TEXT PRIMARY KEY,
    failure_policy_request_id TEXT NOT NULL UNIQUE,
    work_item_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    consumer_actor_id TEXT NOT NULL
        CHECK (octet_length(consumer_actor_id) BETWEEN 1 AND 200),
    policy_decision TEXT NOT NULL
        CHECK (policy_decision = 'retry'),
    retry_not_before TIMESTAMPTZ NOT NULL,
    disposition TEXT NOT NULL
        CHECK (disposition IN ('requeued', 'superseded')),
    superseded_by_work_item_id TEXT
        REFERENCES repository_extraction_work_items(work_item_id),
    consumed_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT repo_work_retry_consumption_policy_fk
        FOREIGN KEY (
            failure_policy_request_id,
            work_item_id,
            claim_id,
            policy_decision,
            retry_not_before
        ) REFERENCES repository_extraction_work_failure_policy_decisions (
            request_id,
            work_item_id,
            claim_id,
            decision,
            retry_not_before
        ),
    CHECK (consumed_at >= retry_not_before),
    CHECK (
        (disposition = 'requeued' AND superseded_by_work_item_id IS NULL)
        OR
        (disposition = 'superseded' AND superseded_by_work_item_id IS NOT NULL)
    )
);
