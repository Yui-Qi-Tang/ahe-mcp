CREATE UNIQUE INDEX extraction_runs_source_request_id_uq
    ON extraction_runs (request_id)
    WHERE repository_snapshot_id IS NULL;
