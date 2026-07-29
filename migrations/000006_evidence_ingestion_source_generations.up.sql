ALTER TABLE extractor_definitions
    ADD CONSTRAINT extractor_definitions_id_name_uq
    UNIQUE (extractor_definition_id, extractor_name);

CREATE TABLE repository_source_generations (
    source_generation_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    extractor_definition_id TEXT NOT NULL,
    generation_number INTEGER NOT NULL CHECK (generation_number > 0),
    repository_snapshot_id TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    proposal_batch_id TEXT NOT NULL UNIQUE REFERENCES proposal_batches(proposal_batch_id),
    extractor_output_hash TEXT NOT NULL,
    proposal_count INTEGER NOT NULL CHECK (proposal_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (repository_snapshot_id, repo_id, commit_sha)
        REFERENCES repository_snapshots(repository_snapshot_id, repo_id, commit_sha),
    FOREIGN KEY (extractor_definition_id, extractor_name)
        REFERENCES extractor_definitions(extractor_definition_id, extractor_name),
    UNIQUE (repo_id, extractor_name, generation_number),
    UNIQUE (repo_id, extractor_name, repository_snapshot_id, extractor_definition_id),
    UNIQUE (source_generation_id, repo_id, extractor_name)
);

CREATE TABLE repository_source_heads (
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    active_generation_id TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, extractor_name),
    FOREIGN KEY (active_generation_id, repo_id, extractor_name)
        REFERENCES repository_source_generations(source_generation_id, repo_id, extractor_name)
);

CREATE TABLE repository_generation_activation_requests (
    request_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    previous_generation_id TEXT,
    activated_generation_id TEXT NOT NULL,
    changed BOOLEAN NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (activated_generation_id, repo_id, extractor_name)
        REFERENCES repository_source_generations(source_generation_id, repo_id, extractor_name),
    FOREIGN KEY (previous_generation_id, repo_id, extractor_name)
        REFERENCES repository_source_generations(source_generation_id, repo_id, extractor_name)
);

CREATE INDEX repository_source_generations_snapshot_idx
    ON repository_source_generations (repository_snapshot_id);
