ALTER TABLE external_source_intake_receipts
    ADD CONSTRAINT external_source_intake_receipts_exact_result_fk
    FOREIGN KEY (request_id, source_snapshot_id, extraction_view_id)
    REFERENCES source_intake_requests (
        request_id,
        source_snapshot_id,
        extraction_view_id
    );

ALTER TABLE extractor_definitions
    ADD CONSTRAINT extractor_definitions_name_bytes_ck
        CHECK (octet_length(extractor_name) BETWEEN 1 AND 200),
    ADD CONSTRAINT extractor_definitions_version_bytes_ck
        CHECK (octet_length(extractor_version) BETWEEN 1 AND 200),
    ADD CONSTRAINT extractor_definitions_config_object_ck
        CHECK (jsonb_typeof(extractor_config) = 'object'),
    ADD CONSTRAINT extractor_definitions_config_bytes_ck
        CHECK (octet_length(extractor_config::text) <= 65536);
