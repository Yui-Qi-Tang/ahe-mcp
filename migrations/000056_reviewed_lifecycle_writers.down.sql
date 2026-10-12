DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM endpoint_disposition_review_bindings) OR EXISTS(SELECT 1 FROM supersession_review_bindings) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='rollback refuses to discard reviewed lifecycle history'; END IF;
END $$;
DROP TRIGGER admission_decisions_reviewed_lifecycle ON admission_decisions;
DROP TRIGGER proposal_occurrences_reviewed_lifecycle ON proposal_occurrences;
DROP TRIGGER endpoint_review_requests_reviewed_lifecycle ON endpoint_review_requests;
DROP TRIGGER canonical_endpoint_review_bindings_reviewed_lifecycle ON canonical_endpoint_review_bindings;
DROP TRIGGER endpoint_disposition_review_bindings_reviewed_lifecycle ON endpoint_disposition_review_bindings;
DROP TRIGGER supersession_review_bindings_reviewed_lifecycle ON supersession_review_bindings;
ALTER TABLE canonical_endpoint_review_bindings DROP CONSTRAINT endpoint_admission_request_fk;
DROP TABLE supersession_review_bindings,endpoint_disposition_review_bindings,endpoint_review_requests;
DROP FUNCTION reviewed_lifecycle_authority_trigger_v1(),canonical_supersession_lock_targets_v1(TEXT[]),supersession_review_assert_v1(TEXT);
ALTER TABLE admission_decisions DROP COLUMN supersession_review_binding_contract_version;
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_disposition_review_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_disposition_review_contract_ck CHECK
 (disposition_review_binding_contract_version IS NULL OR (disposition_review_binding_contract_version='reviewed-source-claim-disposition/v1' AND review_binding_contract_version IS NULL AND outcome IN ('rejected','audit_only') AND canonical_ref IS NULL AND raw_evidence_node_ids='[]'::jsonb AND canonical_edge_ids='[]'::jsonb));
CREATE OR REPLACE FUNCTION source_claim_disposition_review_assert_v1(checked_decision_id TEXT)
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


DROP FUNCTION endpoint_disposition_review_assert_v1(TEXT);
CREATE OR REPLACE FUNCTION canonical_supersession_event_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession admission event is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.event_id);
    RETURN NULL;
END $$;
ALTER FUNCTION canonical_supersession_event_authority_trigger() SECURITY INVOKER;
ALTER FUNCTION canonical_supersession_event_authority_trigger() RESET ALL;
CREATE OR REPLACE FUNCTION canonical_supersession_member_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.first_admission_event_id);
    RETURN NULL;
END $$;
ALTER FUNCTION canonical_supersession_member_authority_trigger() SECURITY INVOKER;
ALTER FUNCTION canonical_supersession_member_authority_trigger() RESET ALL;
CREATE OR REPLACE FUNCTION canonical_supersession_target_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession replacement target is append-only';
    END IF;

    PERFORM canonical_supersession_assert_target(
        NEW.event_id,
        NEW.target_node_id,
        NEW.canonical_edge_id
    );
    PERFORM canonical_supersession_assert_event(NEW.event_id);
    RETURN NULL;
END $$;
ALTER FUNCTION canonical_supersession_target_authority_trigger() SECURITY INVOKER;
ALTER FUNCTION canonical_supersession_target_authority_trigger() RESET ALL;
CREATE OR REPLACE FUNCTION canonical_supersession_proposal_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    protected_event_id TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        RETURN NULL;
    END IF;

    SELECT event.event_id
    INTO protected_event_id
    FROM canonical_supersession_admission_events AS event
    WHERE event.proposal_occurrence_id = OLD.proposal_occurrence_id
    UNION
    SELECT member.first_admission_event_id
    FROM canonical_supersession_members AS member
    JOIN canonical_graph_nodes AS node
        ON node.canonical_node_id = member.canonical_node_id
    WHERE node.origin_proposal_occurrence_id = OLD.proposal_occurrence_id
    LIMIT 1;

    IF protected_event_id IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'UPDATE'
        AND OLD.admission_outcome = 'pending'
        AND OLD.canonical_ref IS NULL
        AND NEW.admission_outcome = 'admitted'
        AND NEW.canonical_ref IS NOT NULL
        AND (
            to_jsonb(NEW) - 'admission_outcome' - 'canonical_ref'
        ) = (
            to_jsonb(OLD) - 'admission_outcome' - 'canonical_ref'
        )
        AND EXISTS (
            SELECT 1
            FROM canonical_supersession_admission_events
            WHERE proposal_occurrence_id = NEW.proposal_occurrence_id
              AND replacement_node_id = NEW.canonical_ref
        ) THEN
        PERFORM canonical_supersession_assert_event(protected_event_id);
        RETURN NULL;
    END IF;

    RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = 'canonical supersession proposal binding is immutable';
END $$;
ALTER FUNCTION canonical_supersession_proposal_authority_trigger() SECURITY INVOKER;
ALTER FUNCTION canonical_supersession_proposal_authority_trigger() RESET ALL;
