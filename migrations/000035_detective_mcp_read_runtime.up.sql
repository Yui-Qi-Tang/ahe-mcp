DO $$
DECLARE
    capability_constraint TEXT;
    relative_path_constraint TEXT;
BEGIN
    SELECT conname
    INTO capability_constraint
    FROM pg_constraint
    WHERE conrelid = 'detective_workspace_sources'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%git-go-repository%'
      AND pg_get_constraintdef(oid) LIKE '%local-prd-text%';

    IF capability_constraint IS NULL THEN
        RAISE EXCEPTION 'detective workspace capability constraint was not found';
    END IF;
    EXECUTE format(
        'ALTER TABLE detective_workspace_sources DROP CONSTRAINT %I',
        capability_constraint
    );

    SELECT c.conname
    INTO relative_path_constraint
    FROM pg_constraint c
    WHERE c.conrelid = 'detective_workspace_sources'::regclass
      AND c.contype = 'u'
      AND pg_get_indexdef(c.conindid) LIKE
          '%(workspace_id, capability_name, capability_version, relative_path)%';

    IF relative_path_constraint IS NULL THEN
        RAISE EXCEPTION 'detective workspace relative-path constraint was not found';
    END IF;
    EXECUTE format(
        'ALTER TABLE detective_workspace_sources DROP CONSTRAINT %I',
        relative_path_constraint
    );
END
$$;

ALTER TABLE detective_workspace_sources
    DROP CONSTRAINT detective_workspace_sources_path_kind_check;

ALTER TABLE detective_workspace_sources
    ADD CONSTRAINT detective_workspace_sources_path_kind_check
    CHECK (path_kind IN ('directory', 'file', 'remote'));

ALTER TABLE detective_workspace_sources
    ADD CONSTRAINT detective_workspace_sources_capability_check
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
            AND path_kind IN ('directory', 'file')
        )
        OR
        (
            capability_name = 'mcp-read-document'
            AND capability_version = 'v1'
            AND source_system = 'mcp_read_document'
            AND relative_path = '.'
            AND path_kind = 'remote'
        )
    );

CREATE UNIQUE INDEX detective_workspace_sources_local_path_unique
    ON detective_workspace_sources (
        workspace_id,
        capability_name,
        capability_version,
        relative_path
    )
    WHERE path_kind <> 'remote';

CREATE TABLE detective_mcp_read_source_bindings (
    source_binding_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    capability_name TEXT NOT NULL DEFAULT 'mcp-read-document'
        CHECK (capability_name = 'mcp-read-document'),
    capability_version TEXT NOT NULL DEFAULT 'v1'
        CHECK (capability_version = 'v1'),
    source_system TEXT NOT NULL DEFAULT 'mcp_read_document'
        CHECK (source_system = 'mcp_read_document'),
    source_id TEXT NOT NULL
        CHECK (octet_length(source_id) BETWEEN 1 AND 500),
    connector_id TEXT NOT NULL
        CHECK (octet_length(connector_id) BETWEEN 1 AND 200),
    provider TEXT NOT NULL
        CHECK (octet_length(provider) BETWEEN 1 AND 100),
    logical_capability TEXT NOT NULL
        CHECK (octet_length(logical_capability) BETWEEN 1 AND 100),
    adapter_name TEXT NOT NULL
        CHECK (octet_length(adapter_name) BETWEEN 1 AND 100),
    adapter_version TEXT NOT NULL
        CHECK (octet_length(adapter_version) BETWEEN 1 AND 100),
    provider_tool_name TEXT NOT NULL
        CHECK (octet_length(provider_tool_name) BETWEEN 1 AND 200),
    provider_tool_input_schema_hash TEXT NOT NULL
        CHECK (provider_tool_input_schema_hash ~ '^sha256:[0-9a-f]{64}$'),
    arguments JSONB NOT NULL
        CHECK (
            jsonb_typeof(arguments) = 'object'
            AND octet_length(arguments::TEXT) <= 65536
        ),
    command_hash TEXT NOT NULL
        CHECK (command_hash ~ '^sha256:[0-9a-f]{64}$'),
    binding_hash TEXT NOT NULL
        CHECK (binding_hash ~ '^sha256:[0-9a-f]{64}$'),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, source_binding_id),
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
    )
);

CREATE INDEX detective_mcp_read_source_bindings_workspace_idx
    ON detective_mcp_read_source_bindings (workspace_id, source_binding_id);

CREATE TABLE detective_mcp_read_collection_cycles (
    cycle_id TEXT PRIMARY KEY
        CHECK (cycle_id ~ '^detective-mcp-read-cycle:[0-9a-f]{64}$'),
    workspace_id TEXT NOT NULL,
    source_binding_id TEXT NOT NULL,
    cycle_number BIGINT NOT NULL
        CHECK (cycle_number BETWEEN 1 AND 9223372036854775807),
    binding_hash TEXT NOT NULL
        CHECK (binding_hash ~ '^sha256:[0-9a-f]{64}$'),
    collection_request_id TEXT NOT NULL UNIQUE
        CHECK (octet_length(collection_request_id) BETWEEN 1 AND 300),
    status TEXT NOT NULL
        CHECK (status IN ('running', 'completed', 'failed')),
    connector_delivery_id TEXT
        REFERENCES detective_connector_inbox_deliveries(connector_delivery_id),
    source_snapshot_id TEXT
        REFERENCES source_snapshots(source_snapshot_id),
    provider_object_id TEXT,
    provider_revision TEXT,
    document_id TEXT,
    coverage_complete BOOLEAN,
    coverage_truncated BOOLEAN,
    completion_reason TEXT,
    failure_kind TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (workspace_id, source_binding_id, cycle_number),
    FOREIGN KEY (workspace_id, source_binding_id)
        REFERENCES detective_mcp_read_source_bindings(workspace_id, source_binding_id),
    CHECK (
        (provider_object_id IS NULL OR octet_length(provider_object_id) BETWEEN 1 AND 500)
        AND (provider_revision IS NULL OR octet_length(provider_revision) BETWEEN 1 AND 500)
        AND (document_id IS NULL OR octet_length(document_id) BETWEEN 1 AND 500)
        AND (completion_reason IS NULL OR octet_length(completion_reason) BETWEEN 1 AND 100)
        AND (failure_kind IS NULL OR octet_length(failure_kind) BETWEEN 1 AND 100)
    ),
    CHECK (
        (
            status = 'running'
            AND connector_delivery_id IS NULL
            AND source_snapshot_id IS NULL
            AND provider_object_id IS NULL
            AND provider_revision IS NULL
            AND document_id IS NULL
            AND coverage_complete IS NULL
            AND coverage_truncated IS NULL
            AND completion_reason IS NULL
            AND failure_kind IS NULL
            AND finished_at IS NULL
        )
        OR
        (
            status = 'completed'
            AND connector_delivery_id IS NOT NULL
            AND source_snapshot_id IS NOT NULL
            AND provider_object_id IS NOT NULL
            AND provider_revision IS NOT NULL
            AND document_id IS NOT NULL
            AND coverage_complete IS NOT NULL
            AND coverage_truncated IS NOT NULL
            AND completion_reason IS NOT NULL
            AND failure_kind IS NULL
            AND finished_at IS NOT NULL
            AND finished_at >= started_at
        )
        OR
        (
            status = 'failed'
            AND source_snapshot_id IS NULL
            AND failure_kind IS NOT NULL
            AND finished_at IS NOT NULL
            AND finished_at >= started_at
        )
    )
);

CREATE UNIQUE INDEX detective_mcp_read_collection_cycles_one_running_idx
    ON detective_mcp_read_collection_cycles (workspace_id, source_binding_id)
    WHERE status = 'running';

CREATE INDEX detective_mcp_read_collection_cycles_source_order_idx
    ON detective_mcp_read_collection_cycles (
        workspace_id,
        source_binding_id,
        cycle_number DESC
    );
