ALTER TABLE extraction_runs
    ADD COLUMN repository_snapshot_id TEXT REFERENCES repository_snapshots(repository_snapshot_id),
    ALTER COLUMN source_snapshot_id DROP NOT NULL,
    ALTER COLUMN extraction_view_id DROP NOT NULL;

ALTER TABLE extraction_runs
    ADD CONSTRAINT extraction_runs_binding_ck CHECK (
        (repository_snapshot_id IS NULL AND source_snapshot_id IS NOT NULL AND extraction_view_id IS NOT NULL)
        OR
        (repository_snapshot_id IS NOT NULL AND source_snapshot_id IS NULL AND extraction_view_id IS NULL)
    );

CREATE INDEX extraction_runs_repository_snapshot_idx ON extraction_runs(repository_snapshot_id);
