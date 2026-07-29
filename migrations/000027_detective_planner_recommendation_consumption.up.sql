ALTER TABLE detective_orchestration_transition_requests
    ADD CONSTRAINT detective_orchestration_transition_requests_exact_key
    UNIQUE (request_id, transition_kind, run_id, step_id);

CREATE TABLE detective_planner_recommendation_consumptions (
    request_id TEXT PRIMARY KEY
        CHECK (octet_length(request_id) BETWEEN 1 AND 300),
    transition_kind TEXT NOT NULL DEFAULT 'plan_step'
        CHECK (transition_kind = 'plan_step'),
    run_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    planning_context_hash TEXT NOT NULL
        CHECK (planning_context_hash ~ '^sha256:[0-9a-f]{64}$'),
    classification TEXT NOT NULL
        CHECK (classification = 'query_source'),
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    consumed_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT detective_planner_consumption_transition_fk
        FOREIGN KEY (request_id, transition_kind, run_id, step_id)
        REFERENCES detective_orchestration_transition_requests (
            request_id,
            transition_kind,
            run_id,
            step_id
        ),
    CONSTRAINT detective_planner_consumption_step_fk
        FOREIGN KEY (run_id, step_id)
        REFERENCES detective_orchestration_steps(run_id, step_id),
    UNIQUE (run_id, step_id),
    UNIQUE (run_id, planning_context_hash)
);

CREATE INDEX detective_planner_recommendation_consumptions_run_idx
    ON detective_planner_recommendation_consumptions (run_id, consumed_at, request_id);
