CREATE TABLE repository_change_observations (
    change_observation_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    observation_number INTEGER NOT NULL CHECK (observation_number > 0),
    previous_change_observation_id TEXT,
    token_contract TEXT NOT NULL CHECK (token_contract = 'git-head-dirty-token-v1'),
    dirty_fingerprint_contract TEXT NOT NULL CHECK (dirty_fingerprint_contract = 'git-status-diff-untracked-v1'),
    head_commit_sha TEXT NOT NULL,
    dirty BOOLEAN NOT NULL,
    dirty_fingerprint TEXT,
    tracked_change_count INTEGER NOT NULL CHECK (tracked_change_count >= 0),
    untracked_file_count INTEGER NOT NULL CHECK (untracked_file_count >= 0),
    change_token TEXT NOT NULL,
    first_observed_at TIMESTAMPTZ NOT NULL,
    last_observed_at TIMESTAMPTZ NOT NULL,
    observation_count INTEGER NOT NULL CHECK (observation_count > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, observation_number),
    UNIQUE (change_observation_id, repo_id),
    UNIQUE (previous_change_observation_id),
    FOREIGN KEY (previous_change_observation_id, repo_id)
        REFERENCES repository_change_observations(change_observation_id, repo_id),
    CHECK (previous_change_observation_id IS NULL OR previous_change_observation_id <> change_observation_id),
    CHECK (first_observed_at <= last_observed_at),
    CHECK (
        (dirty AND dirty_fingerprint IS NOT NULL AND tracked_change_count + untracked_file_count > 0)
        OR
        (NOT dirty AND dirty_fingerprint IS NULL AND tracked_change_count = 0 AND untracked_file_count = 0)
    )
);

CREATE TABLE repository_change_observation_requests (
    request_id TEXT PRIMARY KEY,
    change_observation_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    request_payload_hash TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    stability_window_milliseconds BIGINT NOT NULL
        CHECK (stability_window_milliseconds BETWEEN 1 AND 3600000),
    observation_count INTEGER NOT NULL CHECK (observation_count > 0),
    changed BOOLEAN NOT NULL,
    coalesced BOOLEAN NOT NULL,
    stable_after TIMESTAMPTZ NOT NULL,
    stable BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (change_observation_id, repo_id)
        REFERENCES repository_change_observations(change_observation_id, repo_id),
    CHECK (changed <> coalesced),
    CHECK (NOT changed OR (observation_count = 1 AND NOT stable)),
    CHECK (NOT stable OR (coalesced AND observation_count >= 2 AND observed_at >= stable_after))
);

CREATE INDEX repository_change_observation_requests_observation_idx
    ON repository_change_observation_requests (change_observation_id, observed_at);
