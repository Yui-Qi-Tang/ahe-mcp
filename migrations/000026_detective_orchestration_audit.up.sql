ALTER TABLE detective_workspace_sources
    ADD CONSTRAINT detective_workspace_sources_audit_identity_unique
    UNIQUE (
        workspace_id,
        source_binding_id,
        capability_name,
        capability_version,
        source_system,
        source_id
    );

CREATE TABLE detective_orchestration_runs (
    run_id TEXT PRIMARY KEY
        CHECK (run_id ~ '^detective-run:[0-9a-f]{64}$'),
    workspace_id TEXT NOT NULL
        REFERENCES detective_workspaces(workspace_id),
    status TEXT NOT NULL
        CHECK (status IN ('running', 'completed', 'failed', 'stopped')),
    max_steps INTEGER NOT NULL
        CHECK (max_steps BETWEEN 1 AND 32),
    step_count INTEGER NOT NULL DEFAULT 0
        CHECK (step_count BETWEEN 0 AND max_steps),
    stop_reason TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (run_id, workspace_id),
    CHECK (finished_at IS NULL OR finished_at >= started_at),
    CHECK (
        (status = 'running' AND stop_reason IS NULL AND finished_at IS NULL)
        OR
        (
            status = 'completed'
            AND stop_reason IS NOT NULL
            AND stop_reason = 'coverage_satisfied'
            AND finished_at IS NOT NULL
        )
        OR
        (
            status = 'failed'
            AND stop_reason IS NOT NULL
            AND stop_reason IN ('source_failed', 'workspace_unavailable')
            AND finished_at IS NOT NULL
        )
        OR
        (
            status = 'stopped'
            AND stop_reason IS NOT NULL
            AND stop_reason IN (
                'unsupported_capability',
                'contradictory_output',
                'ungrounded_output',
                'budget_exhausted',
                'no_allowlisted_source'
            )
            AND finished_at IS NOT NULL
        )
    )
);

CREATE INDEX detective_orchestration_runs_workspace_idx
    ON detective_orchestration_runs (workspace_id, started_at, run_id);

CREATE TABLE detective_orchestration_steps (
    step_id TEXT PRIMARY KEY
        CHECK (step_id ~ '^detective-step:[0-9a-f]{64}$'),
    run_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    step_number INTEGER NOT NULL
        CHECK (step_number BETWEEN 1 AND 32),
    source_binding_id TEXT NOT NULL,
    capability_name TEXT NOT NULL,
    capability_version TEXT NOT NULL,
    source_system TEXT NOT NULL,
    source_id TEXT NOT NULL
        CHECK (octet_length(source_id) BETWEEN 1 AND 500),
    reason_code TEXT NOT NULL
        CHECK (reason_code IN ('initial_source', 'coverage_gap', 'conflict_check', 'refresh')),
    input_authority_kind TEXT NOT NULL
        CHECK (input_authority_kind IN ('none', 'source_token', 'source_generation')),
    input_authority_ref TEXT,
    status TEXT NOT NULL
        CHECK (status IN ('planned', 'completed', 'failed', 'stopped')),
    outcome TEXT
        CHECK (outcome IN ('continue', 'completed', 'failed', 'stopped')),
    stop_reason TEXT,
    coverage_schema_version TEXT,
    coverage_unit_kind TEXT,
    attempted_unit_count INTEGER,
    covered_unit_count INTEGER,
    unsupported_unit_count INTEGER,
    coverage_complete BOOLEAN,
    planned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (run_id, step_number),
    UNIQUE (run_id, step_id),
    FOREIGN KEY (run_id, workspace_id)
        REFERENCES detective_orchestration_runs(run_id, workspace_id),
    FOREIGN KEY (
        workspace_id,
        source_binding_id,
        capability_name,
        capability_version,
        source_system,
        source_id
    ) REFERENCES detective_workspace_sources(
        workspace_id,
        source_binding_id,
        capability_name,
        capability_version,
        source_system,
        source_id
    ),
    CHECK (
        (
            capability_name = 'git-go-repository'
            AND capability_version = 'v1'
            AND source_system = 'code_repository'
        )
        OR
        (
            capability_name = 'local-prd-text'
            AND capability_version = 'v1'
            AND source_system = 'manual_text'
        )
    ),
    CHECK (
        (input_authority_kind = 'none' AND input_authority_ref IS NULL)
        OR
        (
            input_authority_kind = 'source_token'
            AND input_authority_ref IS NOT NULL
            AND octet_length(input_authority_ref) BETWEEN 1 AND 500
        )
        OR
        (
            input_authority_kind = 'source_generation'
            AND input_authority_ref IS NOT NULL
            AND input_authority_ref LIKE 'generation:%'
            AND octet_length(input_authority_ref) BETWEEN 12 AND 300
        )
    ),
    CHECK (finished_at IS NULL OR finished_at >= planned_at),
    CHECK (
        (
            status = 'planned'
            AND outcome IS NULL
            AND stop_reason IS NULL
            AND coverage_schema_version IS NULL
            AND coverage_unit_kind IS NULL
            AND attempted_unit_count IS NULL
            AND covered_unit_count IS NULL
            AND unsupported_unit_count IS NULL
            AND coverage_complete IS NULL
            AND finished_at IS NULL
        )
        OR
        (
            status <> 'planned'
            AND outcome IS NOT NULL
            AND coverage_schema_version IS NOT NULL
            AND coverage_schema_version = 'detective-coverage-v1'
            AND coverage_unit_kind IS NOT NULL
            AND (
                (
                    capability_name = 'git-go-repository'
                    AND coverage_unit_kind = 'repository_files'
                )
                OR
                (
                    capability_name = 'local-prd-text'
                    AND coverage_unit_kind = 'text_files'
                )
            )
            AND attempted_unit_count IS NOT NULL
            AND attempted_unit_count BETWEEN 0 AND 1000000000
            AND covered_unit_count IS NOT NULL
            AND covered_unit_count BETWEEN 0 AND attempted_unit_count
            AND unsupported_unit_count IS NOT NULL
            AND unsupported_unit_count BETWEEN 0 AND attempted_unit_count
            AND covered_unit_count + unsupported_unit_count <= attempted_unit_count
            AND coverage_complete IS NOT NULL
            AND (
                coverage_complete = FALSE
                OR covered_unit_count + unsupported_unit_count = attempted_unit_count
            )
            AND finished_at IS NOT NULL
            AND (
                (
                    status = 'completed'
                    AND outcome = 'continue'
                    AND stop_reason IS NULL
                    AND coverage_complete = FALSE
                )
                OR
                (
                    status = 'completed'
                    AND outcome = 'completed'
                    AND stop_reason IS NOT NULL
                    AND stop_reason = 'coverage_satisfied'
                    AND coverage_complete = TRUE
                    AND covered_unit_count = attempted_unit_count
                    AND unsupported_unit_count = 0
                )
                OR
                (
                    status = 'failed'
                    AND outcome = 'failed'
                    AND stop_reason IS NOT NULL
                    AND stop_reason IN ('source_failed', 'workspace_unavailable')
                )
                OR
                (
                    status = 'stopped'
                    AND outcome = 'stopped'
                    AND stop_reason IS NOT NULL
                    AND stop_reason IN (
                        'unsupported_capability',
                        'contradictory_output',
                        'ungrounded_output',
                        'budget_exhausted',
                        'no_allowlisted_source'
                    )
                )
            )
        )
    )
);

CREATE UNIQUE INDEX detective_orchestration_steps_one_planned_idx
    ON detective_orchestration_steps (run_id)
    WHERE status = 'planned';

CREATE INDEX detective_orchestration_steps_run_order_idx
    ON detective_orchestration_steps (run_id, step_number);

CREATE TABLE detective_orchestration_transition_requests (
    request_id TEXT PRIMARY KEY
        CHECK (octet_length(request_id) BETWEEN 1 AND 300),
    transition_kind TEXT NOT NULL
        CHECK (transition_kind IN ('start_run', 'plan_step', 'complete_step')),
    run_id TEXT NOT NULL
        REFERENCES detective_orchestration_runs(run_id),
    step_id TEXT,
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (run_id, step_id)
        REFERENCES detective_orchestration_steps(run_id, step_id),
    CHECK (
        (transition_kind = 'start_run' AND step_id IS NULL)
        OR
        (transition_kind IN ('plan_step', 'complete_step') AND step_id IS NOT NULL)
    )
);

CREATE INDEX detective_orchestration_transition_requests_run_idx
    ON detective_orchestration_transition_requests (run_id, recorded_at, request_id);
