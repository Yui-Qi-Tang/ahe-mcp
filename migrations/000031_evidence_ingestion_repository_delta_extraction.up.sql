ALTER TABLE repository_snapshots
    ADD CONSTRAINT repository_snapshots_id_repo_uq
    UNIQUE (repository_snapshot_id, repo_id);

ALTER TABLE extraction_runs
    ADD CONSTRAINT extraction_runs_repository_delta_uq
    UNIQUE (extraction_run_id, repository_snapshot_id, extractor_definition_id);

ALTER TABLE extraction_attempts
    ADD CONSTRAINT extraction_attempts_run_uq
    UNIQUE (extraction_attempt_id, extraction_run_id);

ALTER TABLE proposal_batches
    ADD CONSTRAINT proposal_batches_attempt_uq
    UNIQUE (proposal_batch_id, extraction_attempt_id);

ALTER TABLE repository_source_generations
    ADD CONSTRAINT repository_source_generations_delta_base_uq
    UNIQUE (
        source_generation_id,
        repo_id,
        extractor_name,
        repository_snapshot_id
    );

CREATE TABLE repository_delta_extractions (
    delta_extraction_id TEXT PRIMARY KEY
        CHECK (delta_extraction_id ~ '^repo-delta:[0-9a-f]{64}$'),
    extraction_run_id TEXT NOT NULL,
    extraction_attempt_id TEXT NOT NULL UNIQUE,
    proposal_batch_id TEXT NOT NULL UNIQUE,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL
        CHECK (extractor_name = 'repository-go-parser-code-fact'),
    extractor_definition_id TEXT NOT NULL,
    base_source_generation_id TEXT,
    base_repository_snapshot_id TEXT,
    repository_snapshot_id TEXT NOT NULL,
    adapter_name TEXT NOT NULL
        CHECK (adapter_name = 'git-go-parser-delta'),
    adapter_version TEXT NOT NULL
        CHECK (adapter_version = 'v1'),
    revision_authority_contract TEXT NOT NULL
        CHECK (revision_authority_contract = 'git-immutable-revision-authority-v1'),
    file_set_authority_contract TEXT NOT NULL
        CHECK (file_set_authority_contract = 'git-tracked-go-file-set-delta-v1'),
    endpoint_authority_contract TEXT NOT NULL
        CHECK (endpoint_authority_contract = 'go-parser-declaration-endpoints-v1'),
    decision TEXT NOT NULL
        CHECK (decision IN ('delta_verified', 'full_fallback')),
    fallback_reason TEXT
        CHECK (fallback_reason IS NULL OR fallback_reason IN (
            'no_base_generation',
            'base_authority_invalid',
            'candidate_invalid',
            'candidate_mismatch'
        )),
    added_file_count INTEGER NOT NULL CHECK (added_file_count BETWEEN 0 AND 10000),
    modified_file_count INTEGER NOT NULL CHECK (modified_file_count BETWEEN 0 AND 10000),
    deleted_file_count INTEGER NOT NULL CHECK (deleted_file_count BETWEEN 0 AND 10000),
    unchanged_file_count INTEGER NOT NULL CHECK (unchanged_file_count BETWEEN 0 AND 10000),
    delta_parsed_file_count INTEGER NOT NULL CHECK (delta_parsed_file_count BETWEEN 0 AND 10000),
    full_verified_file_count INTEGER NOT NULL CHECK (full_verified_file_count BETWEEN 0 AND 10000),
    revision_proof_hash TEXT NOT NULL
        CHECK (revision_proof_hash ~ '^sha256:[0-9a-f]{64}$'),
    file_set_proof_hash TEXT NOT NULL
        CHECK (file_set_proof_hash ~ '^sha256:[0-9a-f]{64}$'),
    endpoint_proof_hash TEXT NOT NULL
        CHECK (endpoint_proof_hash ~ '^sha256:[0-9a-f]{64}$'),
    candidate_output_hash TEXT
        CHECK (candidate_output_hash IS NULL OR candidate_output_hash ~ '^sha256:[0-9a-f]{64}$'),
    full_output_hash TEXT NOT NULL
        CHECK (full_output_hash ~ '^sha256:[0-9a-f]{64}$'),
    selected_output_hash TEXT NOT NULL
        CHECK (selected_output_hash ~ '^sha256:[0-9a-f]{64}$'),
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (repository_snapshot_id, repo_id)
        REFERENCES repository_snapshots(repository_snapshot_id, repo_id),
    FOREIGN KEY (extractor_definition_id, extractor_name)
        REFERENCES extractor_definitions(extractor_definition_id, extractor_name),
    FOREIGN KEY (
        extraction_run_id,
        repository_snapshot_id,
        extractor_definition_id
    ) REFERENCES extraction_runs (
        extraction_run_id,
        repository_snapshot_id,
        extractor_definition_id
    ),
    FOREIGN KEY (extraction_attempt_id, extraction_run_id)
        REFERENCES extraction_attempts(extraction_attempt_id, extraction_run_id),
    FOREIGN KEY (proposal_batch_id, extraction_attempt_id)
        REFERENCES proposal_batches(proposal_batch_id, extraction_attempt_id),
    FOREIGN KEY (
        base_source_generation_id,
        repo_id,
        extractor_name,
        base_repository_snapshot_id
    ) REFERENCES repository_source_generations (
        source_generation_id,
        repo_id,
        extractor_name,
        repository_snapshot_id
    ),
    CHECK (
        (base_source_generation_id IS NULL AND base_repository_snapshot_id IS NULL)
        OR
        (base_source_generation_id IS NOT NULL AND base_repository_snapshot_id IS NOT NULL)
    ),
    CHECK (
        base_repository_snapshot_id IS NULL
        OR base_repository_snapshot_id <> repository_snapshot_id
    ),
    CHECK (
        added_file_count + modified_file_count + unchanged_file_count
            = full_verified_file_count
    ),
    CHECK (delta_parsed_file_count <= full_verified_file_count),
    CHECK (selected_output_hash = full_output_hash),
    CHECK (
        (decision = 'delta_verified'
            AND fallback_reason IS NULL
            AND base_source_generation_id IS NOT NULL
            AND candidate_output_hash IS NOT NULL
            AND candidate_output_hash = full_output_hash
            AND delta_parsed_file_count = added_file_count + modified_file_count)
        OR
        (decision = 'full_fallback'
            AND fallback_reason IS NOT NULL)
    ),
    CHECK (
        decision <> 'full_fallback'
        OR
        (fallback_reason = 'no_base_generation'
            AND base_source_generation_id IS NULL
            AND candidate_output_hash IS NULL)
        OR
        (fallback_reason IN ('base_authority_invalid', 'candidate_invalid')
            AND base_source_generation_id IS NOT NULL
            AND candidate_output_hash IS NULL)
        OR
        (fallback_reason = 'candidate_mismatch'
            AND base_source_generation_id IS NOT NULL
            AND candidate_output_hash IS NOT NULL
            AND candidate_output_hash <> full_output_hash)
    )
);

CREATE INDEX repository_delta_extractions_snapshot_idx
    ON repository_delta_extractions (repository_snapshot_id);

CREATE INDEX repository_delta_extractions_base_generation_idx
    ON repository_delta_extractions (base_source_generation_id)
    WHERE base_source_generation_id IS NOT NULL;
