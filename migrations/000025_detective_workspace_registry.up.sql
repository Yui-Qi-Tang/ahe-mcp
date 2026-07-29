CREATE TABLE detective_workspaces (
    workspace_id TEXT PRIMARY KEY
        CHECK (workspace_id LIKE 'workspace:%' AND octet_length(workspace_id) BETWEEN 11 AND 300),
    canonical_root TEXT NOT NULL UNIQUE
        CHECK (octet_length(canonical_root) BETWEEN 1 AND 4096),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE detective_workspace_sources (
    source_binding_id TEXT PRIMARY KEY
        CHECK (source_binding_id ~ '^workspace-source:[0-9a-f]{64}$'),
    workspace_id TEXT NOT NULL
        REFERENCES detective_workspaces(workspace_id),
    capability_name TEXT NOT NULL,
    capability_version TEXT NOT NULL,
    source_system TEXT NOT NULL,
    source_id TEXT NOT NULL
        CHECK (octet_length(source_id) BETWEEN 1 AND 500),
    relative_path TEXT NOT NULL
        CHECK (
            octet_length(relative_path) BETWEEN 1 AND 4096
            AND relative_path NOT LIKE '/%'
            AND relative_path <> '..'
            AND relative_path NOT LIKE '../%'
            AND relative_path NOT LIKE '%/../%'
        ),
    path_kind TEXT NOT NULL
        CHECK (path_kind IN ('directory', 'file')),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, capability_name, capability_version, source_id),
    UNIQUE (workspace_id, capability_name, capability_version, relative_path),
    CHECK (
        (
            capability_name = 'git-go-repository'
            AND capability_version = 'v1'
            AND source_system = 'code_repository'
            AND relative_path = '.'
            AND path_kind = 'directory'
        )
        OR
        (
            capability_name = 'local-prd-text'
            AND capability_version = 'v1'
            AND source_system = 'manual_text'
        )
    )
);

CREATE INDEX detective_workspace_sources_workspace_idx
    ON detective_workspace_sources (workspace_id, capability_name, capability_version);

CREATE TABLE detective_workspace_registration_requests (
    request_id TEXT PRIMARY KEY
        CHECK (octet_length(request_id) BETWEEN 1 AND 300),
    workspace_id TEXT NOT NULL
        REFERENCES detective_workspaces(workspace_id),
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
