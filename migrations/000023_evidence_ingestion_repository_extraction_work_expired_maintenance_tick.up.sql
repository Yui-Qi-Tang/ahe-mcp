CREATE TABLE repository_extraction_work_expired_maintenance_tick_requests (
    request_id TEXT PRIMARY KEY,
    maintenance_actor_id TEXT NOT NULL
        CHECK (octet_length(maintenance_actor_id) BETWEEN 1 AND 200),
    result_limit INTEGER NOT NULL
        CHECK (result_limit BETWEEN 1 AND 100),
    transition_count INTEGER NOT NULL
        CHECK (transition_count BETWEEN 0 AND result_limit),
    discovered_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE repository_extraction_work_expired_maintenance_tick_items (
    tick_request_id TEXT NOT NULL
        REFERENCES repository_extraction_work_expired_maintenance_tick_requests(request_id),
    item_ordinal INTEGER NOT NULL
        CHECK (item_ordinal > 0),
    transition_kind TEXT NOT NULL
        CHECK (transition_kind IN ('recovery', 'execution_repair')),
    recovery_request_id TEXT UNIQUE
        REFERENCES repository_extraction_work_recovery_requests(request_id),
    execution_repair_request_id TEXT UNIQUE
        REFERENCES repository_extraction_work_execution_repairs(request_id),
    PRIMARY KEY (tick_request_id, item_ordinal),
    CHECK (
        (transition_kind = 'recovery'
            AND recovery_request_id IS NOT NULL
            AND execution_repair_request_id IS NULL)
        OR
        (transition_kind = 'execution_repair'
            AND recovery_request_id IS NULL
            AND execution_repair_request_id IS NOT NULL)
    )
);
