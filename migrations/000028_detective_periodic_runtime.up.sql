ALTER TABLE detective_workspace_sources
    ADD CONSTRAINT detective_workspace_sources_periodic_identity_unique
    UNIQUE (workspace_id, source_binding_id);

ALTER TABLE detective_orchestration_transition_requests
    ADD CONSTRAINT detective_orchestration_transition_requests_periodic_start_key
    UNIQUE (request_id, transition_kind, run_id);

CREATE TABLE detective_periodic_source_cursors (
    workspace_id TEXT NOT NULL,
    source_binding_id TEXT NOT NULL,
    cycle_count BIGINT NOT NULL DEFAULT 0
        CHECK (cycle_count BETWEEN 0 AND 9223372036854775807),
    active_cycle_id TEXT,
    last_cycle_id TEXT,
    cursor_token TEXT,
    cursor_advanced_at TIMESTAMPTZ,
    PRIMARY KEY (workspace_id, source_binding_id),
    FOREIGN KEY (workspace_id, source_binding_id)
        REFERENCES detective_workspace_sources(workspace_id, source_binding_id),
    CHECK (
        (
            last_cycle_id IS NULL
            AND cursor_token IS NULL
            AND cursor_advanced_at IS NULL
        )
        OR
        (
            last_cycle_id IS NOT NULL
            AND cursor_token IS NOT NULL
            AND octet_length(cursor_token) BETWEEN 1 AND 500
            AND cursor_advanced_at IS NOT NULL
            AND cycle_count > 0
        )
    ),
    CHECK (active_cycle_id IS NULL OR cycle_count > 0),
    CHECK (active_cycle_id IS NULL OR active_cycle_id <> last_cycle_id)
);

CREATE TABLE detective_periodic_source_cycles (
    cycle_id TEXT PRIMARY KEY
        CHECK (cycle_id ~ '^detective-periodic-cycle:[0-9a-f]{64}$'),
    workspace_id TEXT NOT NULL,
    source_binding_id TEXT NOT NULL,
    cycle_number BIGINT NOT NULL
        CHECK (cycle_number BETWEEN 1 AND 9223372036854775807),
    source_token TEXT NOT NULL
        CHECK (octet_length(source_token) BETWEEN 1 AND 500),
    one_shot_request_id TEXT NOT NULL UNIQUE
        CHECK (octet_length(one_shot_request_id) BETWEEN 1 AND 300),
    transition_kind TEXT NOT NULL DEFAULT 'start_run'
        CHECK (transition_kind = 'start_run'),
    run_id TEXT NOT NULL UNIQUE
        CHECK (run_id ~ '^detective-run:[0-9a-f]{64}$'),
    max_steps INTEGER NOT NULL
        CHECK (max_steps BETWEEN 1 AND 32),
    status TEXT NOT NULL
        CHECK (status IN ('running', 'completed', 'failed', 'stopped')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finalized_at TIMESTAMPTZ,
    UNIQUE (workspace_id, source_binding_id, cycle_number),
    UNIQUE (cycle_id, workspace_id, source_binding_id),
    FOREIGN KEY (workspace_id, source_binding_id)
        REFERENCES detective_periodic_source_cursors(workspace_id, source_binding_id),
    FOREIGN KEY (run_id, workspace_id)
        REFERENCES detective_orchestration_runs(run_id, workspace_id),
    FOREIGN KEY (one_shot_request_id, transition_kind, run_id)
        REFERENCES detective_orchestration_transition_requests (
            request_id,
            transition_kind,
            run_id
        ),
    CHECK (
        (status = 'running' AND finalized_at IS NULL)
        OR
        (status <> 'running' AND finalized_at IS NOT NULL AND finalized_at >= created_at)
    )
);

CREATE UNIQUE INDEX detective_periodic_source_cycles_one_running_idx
    ON detective_periodic_source_cycles (workspace_id, source_binding_id)
    WHERE status = 'running';

CREATE INDEX detective_periodic_source_cycles_source_order_idx
    ON detective_periodic_source_cycles (
        workspace_id,
        source_binding_id,
        cycle_number DESC
    );

ALTER TABLE detective_periodic_source_cursors
    ADD CONSTRAINT detective_periodic_source_cursors_active_cycle_fk
    FOREIGN KEY (active_cycle_id, workspace_id, source_binding_id)
    REFERENCES detective_periodic_source_cycles (
        cycle_id,
        workspace_id,
        source_binding_id
    );

ALTER TABLE detective_periodic_source_cursors
    ADD CONSTRAINT detective_periodic_source_cursors_last_cycle_fk
    FOREIGN KEY (last_cycle_id, workspace_id, source_binding_id)
    REFERENCES detective_periodic_source_cycles (
        cycle_id,
        workspace_id,
        source_binding_id
    );
