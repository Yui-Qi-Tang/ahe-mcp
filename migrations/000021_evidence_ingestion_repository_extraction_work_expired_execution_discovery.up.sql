CREATE INDEX repository_extraction_work_expired_execution_scan_idx
    ON repository_extraction_work_claim_attempts (
        lease_expires_at,
        work_item_id,
        claim_id
    )
    WHERE status = 'running';
