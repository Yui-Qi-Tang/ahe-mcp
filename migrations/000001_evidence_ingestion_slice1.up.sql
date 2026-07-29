CREATE TABLE source_blobs (
    raw_content_hash TEXT PRIMARY KEY,
    raw_content BYTEA NOT NULL,
    byte_length INTEGER NOT NULL CHECK (byte_length >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE source_snapshots (
    source_snapshot_id TEXT PRIMARY KEY,
    source_system TEXT NOT NULL,
    source_id TEXT NOT NULL,
    source_version TEXT NOT NULL,
    raw_content_hash TEXT NOT NULL REFERENCES source_blobs(raw_content_hash),
    origin_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_system, source_id, source_version, raw_content_hash)
);

CREATE TABLE extraction_views (
    extraction_view_id TEXT PRIMARY KEY,
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(source_snapshot_id),
    renderer_name TEXT NOT NULL,
    renderer_version TEXT NOT NULL,
    rendered_content BYTEA NOT NULL,
    rendered_content_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_snapshot_id, renderer_name, renderer_version, rendered_content_hash)
);

CREATE TABLE span_catalog_entries (
    extraction_view_id TEXT NOT NULL REFERENCES extraction_views(extraction_view_id),
    span_id TEXT NOT NULL,
    span_catalog_version TEXT NOT NULL,
    start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
    end_byte INTEGER NOT NULL CHECK (end_byte >= start_byte),
    display_line INTEGER NOT NULL CHECK (display_line > 0),
    quoted_text_hash TEXT NOT NULL,
    quoted_text BYTEA NOT NULL,
    PRIMARY KEY (extraction_view_id, span_id)
);

CREATE TABLE source_intake_requests (
    request_id TEXT PRIMARY KEY,
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(source_snapshot_id),
    extraction_view_id TEXT NOT NULL REFERENCES extraction_views(extraction_view_id),
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE extractor_definitions (
    extractor_definition_id TEXT PRIMARY KEY,
    extractor_name TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    extractor_config_hash TEXT NOT NULL,
    extractor_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (extractor_name, extractor_version, extractor_config_hash)
);

CREATE TABLE extraction_runs (
    extraction_run_id TEXT PRIMARY KEY,
    extractor_definition_id TEXT NOT NULL REFERENCES extractor_definitions(extractor_definition_id),
    source_snapshot_id TEXT NOT NULL REFERENCES source_snapshots(source_snapshot_id),
    extraction_view_id TEXT NOT NULL REFERENCES extraction_views(extraction_view_id),
    request_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE extraction_attempts (
    extraction_attempt_id TEXT PRIMARY KEY,
    extraction_run_id TEXT NOT NULL REFERENCES extraction_runs(extraction_run_id),
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    status TEXT NOT NULL CHECK (status IN ('started', 'succeeded', 'failed')),
    output_hash TEXT,
    failure_class TEXT,
    failure_metadata JSONB,
    fixture_output JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (extraction_run_id, attempt_number)
);

CREATE TABLE proposal_batches (
    proposal_batch_id TEXT PRIMARY KEY,
    extraction_attempt_id TEXT NOT NULL REFERENCES extraction_attempts(extraction_attempt_id),
    status TEXT NOT NULL CHECK (status IN ('started', 'completed', 'failed')),
    proposal_count INTEGER NOT NULL CHECK (proposal_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE proposal_occurrences (
    proposal_occurrence_id TEXT PRIMARY KEY,
    proposal_batch_id TEXT NOT NULL REFERENCES proposal_batches(proposal_batch_id),
    extraction_attempt_id TEXT NOT NULL REFERENCES extraction_attempts(extraction_attempt_id),
    proposal_local_id TEXT NOT NULL,
    proposal_kind TEXT NOT NULL,
    statement_text TEXT NOT NULL,
    proposal_fingerprint TEXT NOT NULL,
    proposal_fingerprint_version TEXT NOT NULL,
    admission_outcome TEXT NOT NULL CHECK (admission_outcome IN ('pending', 'rejected', 'audit_only', 'admitted')),
    canonical_ref TEXT,
    source_refs JSONB NOT NULL,
    proposed_payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (extraction_attempt_id, proposal_batch_id, proposal_local_id),
    CHECK (
        (admission_outcome = 'admitted' AND canonical_ref IS NOT NULL)
        OR
        (admission_outcome <> 'admitted' AND canonical_ref IS NULL)
    )
);

CREATE INDEX proposal_occurrences_fingerprint_idx
    ON proposal_occurrences (proposal_fingerprint);
