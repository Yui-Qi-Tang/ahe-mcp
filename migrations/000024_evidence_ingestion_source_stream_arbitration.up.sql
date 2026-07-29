CREATE TABLE evidence_ingestion_request_serializations (
    operation_name TEXT NOT NULL
        CHECK (octet_length(operation_name) BETWEEN 1 AND 100),
    request_id TEXT NOT NULL
        CHECK (octet_length(request_id) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (operation_name, request_id)
);

CREATE TABLE git_repository_source_streams (
    repo_id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO git_repository_source_streams (repo_id)
SELECT DISTINCT repo_id
FROM repository_change_observations;

ALTER TABLE repository_change_observations
    ADD CONSTRAINT repository_change_observations_source_stream_fk
    FOREIGN KEY (repo_id)
    REFERENCES git_repository_source_streams(repo_id);

CREATE TABLE repository_source_streams (
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, extractor_name)
);

INSERT INTO repository_source_streams (repo_id, extractor_name)
SELECT repo_id, extractor_name
FROM repository_source_generations
UNION
SELECT repo_id, extractor_name
FROM repository_extraction_work_items;

ALTER TABLE repository_source_generations
    ADD CONSTRAINT repository_source_generations_stream_fk
    FOREIGN KEY (repo_id, extractor_name)
    REFERENCES repository_source_streams(repo_id, extractor_name);

ALTER TABLE repository_source_heads
    ADD CONSTRAINT repository_source_heads_stream_fk
    FOREIGN KEY (repo_id, extractor_name)
    REFERENCES repository_source_streams(repo_id, extractor_name);

ALTER TABLE repository_extraction_work_items
    ADD CONSTRAINT repository_extraction_work_items_source_stream_fk
    FOREIGN KEY (repo_id, extractor_name)
    REFERENCES repository_source_streams(repo_id, extractor_name);
