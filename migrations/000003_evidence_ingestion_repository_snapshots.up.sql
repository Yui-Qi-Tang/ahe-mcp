CREATE TABLE repository_snapshots (
    repository_snapshot_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    manifest_entry_count INTEGER NOT NULL CHECK (manifest_entry_count >= 0),
    revision_verification_method TEXT NOT NULL,
    manifest_contract TEXT NOT NULL,
    file_selection_contract TEXT NOT NULL,
    selected_file_count INTEGER NOT NULL CHECK (selected_file_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, commit_sha),
    UNIQUE (repository_snapshot_id, repo_id, commit_sha),
    CHECK (selected_file_count <= manifest_entry_count)
);

CREATE TABLE source_file_snapshots (
    file_snapshot_id TEXT PRIMARY KEY,
    repository_snapshot_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    path TEXT NOT NULL CHECK (path <> ''),
    blob_hash TEXT NOT NULL REFERENCES source_blobs(raw_content_hash),
    git_blob_oid TEXT NOT NULL,
    byte_length INTEGER NOT NULL CHECK (byte_length >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (repository_snapshot_id, repo_id, commit_sha)
        REFERENCES repository_snapshots(repository_snapshot_id, repo_id, commit_sha),
    UNIQUE (repository_snapshot_id, path),
    UNIQUE (repo_id, commit_sha, path)
);

CREATE TABLE repository_snapshot_intake_requests (
    request_id TEXT PRIMARY KEY,
    repository_snapshot_id TEXT NOT NULL REFERENCES repository_snapshots(repository_snapshot_id),
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX source_file_snapshots_blob_hash_idx
    ON source_file_snapshots (blob_hash);
