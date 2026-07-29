CREATE TABLE repository_extraction_work_retry_controller_tick_requests (
    request_id TEXT PRIMARY KEY,
    consumer_actor_id TEXT NOT NULL
        CHECK (octet_length(consumer_actor_id) BETWEEN 1 AND 200),
    result_limit INTEGER NOT NULL
        CHECK (result_limit BETWEEN 1 AND 100),
    consumption_count INTEGER NOT NULL
        CHECK (consumption_count BETWEEN 0 AND result_limit),
    discovered_at TIMESTAMPTZ NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE repository_extraction_work_retry_controller_tick_items (
    tick_request_id TEXT NOT NULL
        REFERENCES repository_extraction_work_retry_controller_tick_requests(request_id),
    item_ordinal INTEGER NOT NULL
        CHECK (item_ordinal > 0),
    consumption_request_id TEXT NOT NULL UNIQUE
        REFERENCES repository_extraction_work_retry_decision_consumptions(request_id),
    PRIMARY KEY (tick_request_id, item_ordinal)
);
