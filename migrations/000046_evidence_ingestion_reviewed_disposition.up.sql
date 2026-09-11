-- Noncanonical reviewed decisions have their own mode and binding authority.
-- Existing admission/review rows keep their original contract unchanged.
LOCK TABLE admission_decisions, proposal_occurrences IN EXCLUSIVE MODE;

ALTER TABLE admission_decisions
    ADD COLUMN disposition_review_binding_contract_version TEXT,
    ADD CONSTRAINT admission_decisions_disposition_review_contract_ck CHECK (
        disposition_review_binding_contract_version IS NULL
        OR (
            disposition_review_binding_contract_version = 'reviewed-source-claim-disposition/v1'
            AND review_binding_contract_version IS NULL
            AND outcome IN ('rejected', 'audit_only')
            AND canonical_ref IS NULL
            AND raw_evidence_node_ids = '[]'::jsonb
            AND canonical_edge_ids = '[]'::jsonb
        )
    );

-- Reuse only the exact display column checks, not canonical foreign keys or
-- authority. This separate table must never require an ordinary mutation.
CREATE TABLE source_claim_disposition_review_bindings (
    LIKE canonical_source_claim_review_bindings INCLUDING DEFAULTS INCLUDING CONSTRAINTS
);
ALTER TABLE source_claim_disposition_review_bindings
    DROP CONSTRAINT canonical_source_claim_review_bindings_contract_ck,
    ADD CONSTRAINT source_claim_disposition_review_contract_ck
        CHECK (contract_version = 'reviewed-source-claim-disposition/v1'),
    ADD PRIMARY KEY (admission_decision_id),
    ADD UNIQUE (proposal_occurrence_id),
    ADD UNIQUE (review_display_artifact_id),
    ADD FOREIGN KEY (admission_decision_id) REFERENCES admission_decisions(admission_decision_id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD FOREIGN KEY (proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD FOREIGN KEY (extraction_attempt_id) REFERENCES extraction_attempts(extraction_attempt_id)
        DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION source_claim_disposition_review_assert_v1(checked_decision_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    decision_contract TEXT;
    binding_count BIGINT;
    authority_count BIGINT;
BEGIN
    SELECT disposition_review_binding_contract_version INTO decision_contract
    FROM admission_decisions WHERE admission_decision_id = checked_decision_id;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    SELECT count(*) INTO binding_count FROM source_claim_disposition_review_bindings
    WHERE admission_decision_id = checked_decision_id;
    IF decision_contract IS NULL AND binding_count = 0 THEN
        RETURN;
    END IF;
    IF decision_contract IS NULL OR binding_count <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'reviewed disposition decision requires one exact review binding';
    END IF;
    SELECT count(*) INTO authority_count
    FROM source_claim_disposition_review_bindings AS binding
    JOIN admission_decisions AS decision
      ON decision.admission_decision_id = binding.admission_decision_id
     AND decision.proposal_occurrence_id = binding.proposal_occurrence_id
    JOIN proposal_occurrences AS proposal
      ON proposal.proposal_occurrence_id = binding.proposal_occurrence_id
     AND proposal.extraction_attempt_id = binding.extraction_attempt_id
    JOIN extraction_attempts AS attempt ON attempt.extraction_attempt_id = binding.extraction_attempt_id
    WHERE binding.admission_decision_id = checked_decision_id
      AND decision.disposition_review_binding_contract_version = binding.contract_version
      AND decision.review_binding_contract_version IS NULL
      AND decision.outcome IN ('rejected', 'audit_only')
      AND decision.canonical_ref IS NULL
      AND decision.raw_evidence_node_ids = '[]'::jsonb
      AND decision.canonical_edge_ids = '[]'::jsonb
      AND proposal.admission_outcome = decision.outcome
      AND proposal.canonical_ref IS NULL
      AND proposal.proposal_kind = 'statement'
      AND proposal.proposal_fingerprint_version = 'statement-v1'
      AND attempt.status = 'succeeded'
      AND attempt.output_hash IS NOT NULL
      AND attempt.fixture_output IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM canonical_ordinary_admission_manifests AS m WHERE m.admission_decision_id = checked_decision_id)
      AND NOT EXISTS (SELECT 1 FROM canonical_graph_nodes AS n WHERE n.origin_proposal_occurrence_id = binding.proposal_occurrence_id)
      AND NOT EXISTS (SELECT 1 FROM canonical_graph_edges AS e WHERE e.origin_proposal_occurrence_id = binding.proposal_occurrence_id)
      AND binding.review_display_artifact_id = 'review-display:v1:sha256:' || encode(
          sha256(convert_to(binding.review_display_media_type || chr(10) || binding.review_display_payload_utf8, 'UTF8')), 'hex')
      AND binding.review_display_payload_utf8::jsonb ->> 'contract_version' = binding.review_contract_version
      AND binding.review_display_payload_utf8::jsonb ->> 'review_package_id' = binding.review_package_id
      AND binding.review_display_payload_utf8::jsonb ->> 'submission_receipt_id' = binding.submission_receipt_id
      AND binding.review_display_payload_utf8::jsonb ->> 'proposed_effect' = binding.proposed_effect
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,proposal_basis_id}' = binding.proposal_basis_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,submission_receipt_id}' = binding.submission_receipt_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,proposal_manifest_id}' = binding.proposal_manifest_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,proposal_occurrence_id}' = binding.proposal_occurrence_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,extraction_attempt_id}' = binding.extraction_attempt_id;
    IF authority_count <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'reviewed disposition binding has inconsistent authority';
    END IF;
END $$;

CREATE FUNCTION source_claim_disposition_review_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    checked_decision_id TEXT;
BEGIN
    IF TG_TABLE_NAME NOT IN ('source_claim_disposition_review_bindings', 'admission_decisions', 'proposal_occurrences')
       OR TG_LEVEL <> 'ROW' OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'reviewed disposition trigger has an invalid context';
    END IF;
    PERFORM pg_catalog.set_config('search_path', pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    IF TG_TABLE_NAME = 'proposal_occurrences' THEN
        SELECT admission_decision_id INTO checked_decision_id FROM admission_decisions
        WHERE proposal_occurrence_id = CASE WHEN TG_OP = 'DELETE' THEN OLD.proposal_occurrence_id ELSE NEW.proposal_occurrence_id END;
    ELSE
        checked_decision_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.admission_decision_id ELSE NEW.admission_decision_id END;
    END IF;
    PERFORM source_claim_disposition_review_assert_v1(checked_decision_id);
    RETURN NULL;
END $$;

CREATE TRIGGER source_claim_disposition_review_append_only
BEFORE UPDATE OR DELETE ON source_claim_disposition_review_bindings
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER source_claim_disposition_review_forbid_truncate
BEFORE TRUNCATE ON source_claim_disposition_review_bindings
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE CONSTRAINT TRIGGER source_claim_disposition_review_authority
AFTER INSERT OR UPDATE OR DELETE ON source_claim_disposition_review_bindings
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION source_claim_disposition_review_trigger_v1();
CREATE CONSTRAINT TRIGGER admission_decisions_reviewed_disposition_authority
AFTER INSERT OR UPDATE OR DELETE ON admission_decisions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION source_claim_disposition_review_trigger_v1();
CREATE CONSTRAINT TRIGGER proposal_occurrences_reviewed_disposition_authority
AFTER INSERT OR UPDATE OR DELETE ON proposal_occurrences
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION source_claim_disposition_review_trigger_v1();

REVOKE EXECUTE ON FUNCTION source_claim_disposition_review_assert_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION source_claim_disposition_review_trigger_v1() FROM PUBLIC;
