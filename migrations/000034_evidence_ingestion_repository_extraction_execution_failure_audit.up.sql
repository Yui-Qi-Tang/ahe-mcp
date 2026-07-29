ALTER TABLE repository_extraction_work_execution_requests
    ADD COLUMN failure_class TEXT,
    ADD COLUMN failure_message TEXT,
    ADD COLUMN failed_at TIMESTAMPTZ,
    ADD CONSTRAINT repository_extraction_work_execution_failure_audit_ck
        CHECK (
            (
                failure_class IS NULL
                AND failure_message IS NULL
                AND failed_at IS NULL
            )
            OR (
                failure_class IS NOT NULL
                AND failure_message IS NOT NULL
                AND failed_at IS NOT NULL
                AND octet_length(failure_class) BETWEEN 1 AND 100
                AND octet_length(failure_message) BETWEEN 1 AND 2000
            )
        );
