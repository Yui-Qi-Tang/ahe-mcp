ALTER TABLE repository_extraction_work_claim_attempts
    ADD COLUMN lease_duration_milliseconds BIGINT;

UPDATE repository_extraction_work_claim_attempts AS attempt
SET lease_duration_milliseconds = LEAST(
    3600000::NUMERIC,
    GREATEST(
        1::NUMERIC,
        CEIL(
            EXTRACT(
                EPOCH FROM (
                    COALESCE(
                        (
                            SELECT MIN(renewal.prior_lease_expires_at)
                            FROM repository_extraction_work_lease_renewal_requests AS renewal
                            WHERE renewal.claim_id = attempt.claim_id
                        ),
                        attempt.lease_expires_at
                    )
                    - attempt.claimed_at
                )
            ) * 1000
        )
    )
)::BIGINT;

ALTER TABLE repository_extraction_work_claim_attempts
    ALTER COLUMN lease_duration_milliseconds SET NOT NULL,
    ADD CONSTRAINT repository_extraction_work_claim_attempts_lease_duration_ck
        CHECK (lease_duration_milliseconds BETWEEN 1 AND 3600000);

ALTER TABLE repository_extraction_work_execution_requests
    ADD COLUMN heartbeat_lease_duration_milliseconds BIGINT,
    ADD COLUMN heartbeat_count INTEGER NOT NULL DEFAULT 0;

UPDATE repository_extraction_work_execution_requests AS execution
SET heartbeat_lease_duration_milliseconds = attempt.lease_duration_milliseconds
FROM repository_extraction_work_claim_attempts AS attempt
WHERE attempt.claim_id = execution.claim_id
  AND attempt.work_item_id = execution.work_item_id;

ALTER TABLE repository_extraction_work_execution_requests
    ALTER COLUMN heartbeat_lease_duration_milliseconds SET NOT NULL,
    ADD CONSTRAINT repository_extraction_work_execution_heartbeat_duration_ck
        CHECK (heartbeat_lease_duration_milliseconds BETWEEN 1 AND 3600000),
    ADD CONSTRAINT repository_extraction_work_execution_heartbeat_count_ck
        CHECK (heartbeat_count >= 0);
