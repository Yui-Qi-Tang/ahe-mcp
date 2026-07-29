CREATE TABLE repository_extraction_run_requests (
    request_id TEXT PRIMARY KEY,
    extraction_run_id TEXT NOT NULL REFERENCES extraction_runs(extraction_run_id),
    request_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
