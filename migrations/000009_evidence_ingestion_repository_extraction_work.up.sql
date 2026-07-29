CREATE TABLE repository_extraction_work_items (
    work_item_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL CHECK (
        extractor_name IN ('repository-go-parser-code-fact', 'repository-gopls-code-fact')
    ),
    change_observation_id TEXT NOT NULL,
    status TEXT NOT NULL,
    superseded_by_work_item_id TEXT,
    claim_id TEXT UNIQUE,
    claimed_by TEXT CHECK (claimed_by IS NULL OR claimed_by <> ''),
    claimed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (change_observation_id, extractor_name),
    UNIQUE (work_item_id, repo_id, extractor_name),
    UNIQUE (work_item_id, claim_id),
    FOREIGN KEY (change_observation_id, repo_id)
        REFERENCES repository_change_observations(change_observation_id, repo_id),
    FOREIGN KEY (superseded_by_work_item_id)
        REFERENCES repository_extraction_work_items(work_item_id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (superseded_by_work_item_id IS NULL OR superseded_by_work_item_id <> work_item_id),
    CONSTRAINT repository_extraction_work_items_status_ck
        CHECK (status IN ('pending', 'running', 'superseded')),
    CONSTRAINT repository_extraction_work_items_state_ck CHECK (
        (status = 'pending'
            AND superseded_by_work_item_id IS NULL
            AND claim_id IS NULL
            AND claimed_by IS NULL
            AND claimed_at IS NULL)
        OR
        (status = 'running'
            AND superseded_by_work_item_id IS NULL
            AND claim_id IS NOT NULL
            AND claimed_by IS NOT NULL
            AND claimed_at IS NOT NULL)
        OR
        (status = 'superseded'
            AND superseded_by_work_item_id IS NOT NULL
            AND claim_id IS NULL
            AND claimed_by IS NULL
            AND claimed_at IS NULL)
    )
);

CREATE UNIQUE INDEX repository_extraction_work_pending_stream_uq
    ON repository_extraction_work_items (repo_id, extractor_name)
    WHERE status = 'pending';

CREATE UNIQUE INDEX repository_extraction_work_running_stream_uq
    ON repository_extraction_work_items (repo_id, extractor_name)
    WHERE status = 'running';

CREATE TABLE repository_extraction_work_schedule_requests (
    request_id TEXT PRIMARY KEY,
    observation_request_id TEXT NOT NULL
        REFERENCES repository_change_observation_requests(request_id),
    work_item_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    superseded_work_item_id TEXT REFERENCES repository_extraction_work_items(work_item_id),
    request_payload_hash TEXT NOT NULL,
    created BOOLEAN NOT NULL,
    coalesced BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, repo_id, extractor_name)
        REFERENCES repository_extraction_work_items(work_item_id, repo_id, extractor_name),
    CHECK (created <> coalesced),
    CHECK (NOT coalesced OR superseded_work_item_id IS NULL)
);

CREATE TABLE repository_extraction_work_claim_requests (
    request_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    worker_id TEXT NOT NULL CHECK (worker_id <> ''),
    work_item_id TEXT,
    claim_id TEXT,
    claimed_at TIMESTAMPTZ,
    claimed BOOLEAN NOT NULL,
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT repository_extraction_work_claim_requests_work_fk
        FOREIGN KEY (work_item_id, repo_id, extractor_name)
        REFERENCES repository_extraction_work_items(work_item_id, repo_id, extractor_name),
    CONSTRAINT repository_extraction_work_claim_requests_claim_fk
        FOREIGN KEY (work_item_id, claim_id)
        REFERENCES repository_extraction_work_items(work_item_id, claim_id),
    CHECK (
        (claimed AND work_item_id IS NOT NULL AND claim_id IS NOT NULL AND claimed_at IS NOT NULL)
        OR
        (NOT claimed AND work_item_id IS NULL AND claim_id IS NULL AND claimed_at IS NULL)
    )
);
