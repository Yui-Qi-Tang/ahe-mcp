ALTER TABLE extraction_runs
ADD COLUMN producer_session_ref TEXT NOT NULL DEFAULT ''
CHECK (octet_length(producer_session_ref) <= 500);
