-- Governed fresh-v2 replacement and independent endpoint non-admission.
-- Existing events and endpoint admission displays keep their original contracts.
ALTER TABLE admission_decisions ADD COLUMN supersession_review_binding_contract_version TEXT
 CONSTRAINT admission_decisions_supersession_review_contract_ck CHECK (supersession_review_binding_contract_version IS NULL OR
 (supersession_review_binding_contract_version='reviewed-supersession-admission/v1' AND outcome='admitted' AND review_binding_contract_version IS NULL AND disposition_review_binding_contract_version IS NULL));
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_disposition_review_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_disposition_review_contract_ck CHECK (
 disposition_review_binding_contract_version IS NULL OR
 (disposition_review_binding_contract_version IN ('reviewed-source-claim-disposition/v1','reviewed-endpoint-disposition/v1')
 AND review_binding_contract_version IS NULL AND outcome IN ('rejected','audit_only') AND canonical_ref IS NULL
 AND raw_evidence_node_ids='[]'::jsonb AND canonical_edge_ids='[]'::jsonb));

CREATE TABLE endpoint_review_requests (
 request_id TEXT PRIMARY KEY CHECK (octet_length(request_id) BETWEEN 1 AND 200 AND btrim(request_id)=request_id),
 proposal_occurrence_id TEXT NOT NULL UNIQUE REFERENCES proposal_occurrences,
 admission_decision_id TEXT NOT NULL UNIQUE REFERENCES admission_decisions DEFERRABLE INITIALLY DEFERRED,
 UNIQUE (request_id,proposal_occurrence_id,admission_decision_id)
);
INSERT INTO endpoint_review_requests SELECT request_id,proposal_occurrence_id,admission_decision_id FROM canonical_endpoint_review_bindings;
ALTER TABLE canonical_endpoint_review_bindings ADD CONSTRAINT endpoint_admission_request_fk
 FOREIGN KEY (request_id,proposal_occurrence_id,admission_decision_id) REFERENCES endpoint_review_requests(request_id,proposal_occurrence_id,admission_decision_id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE endpoint_disposition_review_bindings (
 admission_decision_id TEXT PRIMARY KEY REFERENCES admission_decisions DEFERRABLE INITIALLY DEFERRED,
 proposal_occurrence_id TEXT NOT NULL UNIQUE REFERENCES proposal_occurrences,
 request_id TEXT NOT NULL UNIQUE,
 review_subject TEXT NOT NULL CHECK (review_subject ~ '^endpoint-review:sha256:[0-9a-f]{64}$'),
 display_payload TEXT NOT NULL CHECK (octet_length(display_payload) BETWEEN 1 AND 1048576),
 receipt_payload JSONB NOT NULL CHECK (jsonb_typeof(receipt_payload)='object' AND octet_length(receipt_payload::text)<=32768),
 FOREIGN KEY (request_id,proposal_occurrence_id,admission_decision_id) REFERENCES endpoint_review_requests(request_id,proposal_occurrence_id,admission_decision_id) DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE supersession_review_bindings (
 admission_decision_id TEXT PRIMARY KEY REFERENCES admission_decisions DEFERRABLE INITIALLY DEFERRED,
 proposal_occurrence_id TEXT NOT NULL UNIQUE REFERENCES proposal_occurrences,
 request_id TEXT NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200 AND btrim(request_id)=request_id),
 event_id TEXT NOT NULL UNIQUE REFERENCES canonical_supersession_admission_events DEFERRABLE INITIALLY DEFERRED,
 review_subject TEXT NOT NULL CHECK (review_subject ~ '^supersession-review:sha256:[0-9a-f]{64}$'),
 display_payload TEXT NOT NULL CHECK (octet_length(display_payload) BETWEEN 1 AND 1048576),
 receipt_payload JSONB NOT NULL CHECK (jsonb_typeof(receipt_payload)='object' AND octet_length(receipt_payload::text)<=32768)
);

CREATE FUNCTION endpoint_disposition_review_assert_v1(checked_decision_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE authority_count BIGINT; decision_contract TEXT; binding_count BIGINT;
BEGIN
 SELECT disposition_review_binding_contract_version INTO decision_contract FROM admission_decisions WHERE admission_decision_id=checked_decision_id;
 SELECT count(*) INTO binding_count FROM endpoint_disposition_review_bindings WHERE admission_decision_id=checked_decision_id;
 IF decision_contract IS DISTINCT FROM 'reviewed-endpoint-disposition/v1' AND binding_count=0 THEN RETURN; END IF;
 SELECT count(*) INTO authority_count FROM endpoint_disposition_review_bindings b
 JOIN admission_decisions d USING(admission_decision_id)
 JOIN proposal_occurrences p ON p.proposal_occurrence_id=b.proposal_occurrence_id AND d.proposal_occurrence_id=p.proposal_occurrence_id
 JOIN extraction_attempts a USING(extraction_attempt_id)
 WHERE b.admission_decision_id=checked_decision_id AND d.disposition_review_binding_contract_version='reviewed-endpoint-disposition/v1'
 AND d.review_binding_contract_version IS NULL AND d.outcome IN ('rejected','audit_only') AND p.admission_outcome=d.outcome
 AND d.canonical_ref IS NULL AND p.canonical_ref IS NULL AND d.raw_evidence_node_ids='[]'::jsonb AND d.canonical_edge_ids='[]'::jsonb
 AND a.status='succeeded' AND p.proposal_kind='statement'
 AND b.review_subject='endpoint-review:sha256:'||encode(sha256(convert_to(b.display_payload,'UTF8')),'hex')
 AND b.display_payload::jsonb->>'contract_version'='endpoint-review/v1'
 AND b.display_payload::jsonb#>>'{request,proposal_occurrence_id}'=p.proposal_occurrence_id
 AND b.display_payload::jsonb#>>'{proposal,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND b.display_payload::jsonb#>>'{proposal,StatementText}'=p.statement_text
 AND b.display_payload::jsonb#>>'{proposal,ProposalFingerprint}'=p.proposal_fingerprint
 AND b.display_payload::jsonb#>>'{proposal,ExtractionAttemptID}'=p.extraction_attempt_id
 AND b.display_payload::jsonb#>'{proposal,SourceRefs}'=p.source_refs
 AND b.display_payload::jsonb#>>'{proposal,AdmissionOutcome}'='pending'
 AND ((b.display_payload::jsonb#>>'{request,kind}'='repository_code' AND p.proposal_fingerprint_version='code-fact-v1')
 OR (b.display_payload::jsonb#>>'{request,kind}'='derived_spec' AND p.proposal_fingerprint_version='statement-v1'))
 AND b.receipt_payload->>'contract_version'='reviewed-endpoint-disposition/v1'
 AND b.receipt_payload->>'request_id'=b.request_id AND b.receipt_payload->>'review_subject'=b.review_subject
 AND b.receipt_payload->>'reviewer_id'=d.decision_by AND b.receipt_payload->>'decision_reason'=d.decision_reason
 AND b.receipt_payload->>'proposal_occurrence_id'=p.proposal_occurrence_id
 AND b.receipt_payload->>'admission_decision_id'=d.admission_decision_id AND b.receipt_payload->>'admission_outcome'=d.outcome
 AND b.receipt_payload->>'replayed'='false'
 AND NOT EXISTS (SELECT 1 FROM canonical_ordinary_admission_manifests WHERE admission_decision_id=checked_decision_id)
 AND NOT EXISTS (SELECT 1 FROM canonical_graph_nodes WHERE origin_proposal_occurrence_id=p.proposal_occurrence_id)
 AND NOT EXISTS (SELECT 1 FROM canonical_graph_edges WHERE origin_proposal_occurrence_id=p.proposal_occurrence_id)
 AND NOT EXISTS (SELECT 1 FROM source_claim_disposition_review_bindings WHERE admission_decision_id=checked_decision_id);
 IF authority_count<>1 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='endpoint disposition authority differs'; END IF;
END $$;

CREATE FUNCTION supersession_review_assert_v1(checked_decision_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE authority_count BIGINT; decision_contract TEXT; binding_count BIGINT;
BEGIN
 SELECT supersession_review_binding_contract_version INTO decision_contract FROM admission_decisions WHERE admission_decision_id=checked_decision_id;
 SELECT count(*) INTO binding_count FROM supersession_review_bindings WHERE admission_decision_id=checked_decision_id;
 IF decision_contract IS NULL AND binding_count=0 THEN RETURN; END IF;
 SELECT count(*) INTO authority_count FROM supersession_review_bindings b
 JOIN admission_decisions d USING(admission_decision_id)
 JOIN proposal_occurrences p ON p.proposal_occurrence_id=b.proposal_occurrence_id AND d.proposal_occurrence_id=p.proposal_occurrence_id
 JOIN canonical_supersession_admission_events e ON e.event_id=b.event_id AND e.admission_decision_id=d.admission_decision_id
 WHERE b.admission_decision_id=checked_decision_id AND d.supersession_review_binding_contract_version='reviewed-supersession-admission/v1'
 AND d.outcome='admitted' AND p.admission_outcome='admitted' AND p.canonical_ref=d.canonical_ref
 AND b.review_subject='supersession-review:sha256:'||encode(sha256(convert_to(b.display_payload,'UTF8')),'hex')
 AND b.display_payload::jsonb->>'contract_version'='supersession-review/v1'
 AND b.display_payload::jsonb#>>'{request,proposal_occurrence_id}'=p.proposal_occurrence_id
 AND b.display_payload::jsonb#>>'{proposal,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND b.display_payload::jsonb#>>'{proposal,StatementText}'=p.statement_text
 AND b.display_payload::jsonb#>'{proposal,SourceRefs}'=p.source_refs
 AND b.display_payload::jsonb#>'{request,basis}'=e.event_payload->'lineage_basis'
 AND b.display_payload::jsonb#>'{request,target_node_ids}'=e.event_payload->'target_node_ids'
 AND (b.display_payload::jsonb#>>'{request,expected_revision}')::bigint=e.previous_revision
 AND coalesce(b.display_payload::jsonb#>>'{request,expected_head_event_id}','')=coalesce(e.previous_event_id,'')
 AND b.receipt_payload->>'contract_version'='reviewed-supersession-admission/v1'
 AND b.receipt_payload->>'request_id'=b.request_id AND b.receipt_payload->>'review_subject'=b.review_subject
 AND b.receipt_payload->>'reviewer_id'=d.decision_by AND b.receipt_payload->>'decision_reason'=d.decision_reason
 AND b.receipt_payload#>>'{admission,admission_event_id}'=e.event_id
 AND b.receipt_payload#>>'{admission,CanonicalRef}'=p.canonical_ref
 AND b.receipt_payload#>>'{admission,AdmissionDecisionID}'=d.admission_decision_id;
 IF authority_count<>1 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='supersession review authority differs'; END IF;
END $$;

CREATE FUNCTION reviewed_lifecycle_authority_trigger_v1()
RETURNS TRIGGER LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE checked_decision TEXT; n BIGINT;
BEGIN
 IF TG_TABLE_NAME NOT IN ('admission_decisions','proposal_occurrences','endpoint_review_requests','canonical_endpoint_review_bindings','endpoint_disposition_review_bindings','supersession_review_bindings')
 OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid reviewed lifecycle trigger context'; END IF;
 PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
 IF TG_TABLE_NAME='proposal_occurrences' THEN
 SELECT admission_decision_id INTO checked_decision FROM admission_decisions WHERE proposal_occurrence_id=CASE WHEN TG_OP='DELETE' THEN OLD.proposal_occurrence_id ELSE NEW.proposal_occurrence_id END;
 ELSE checked_decision:=CASE WHEN TG_OP='DELETE' THEN OLD.admission_decision_id ELSE NEW.admission_decision_id END; END IF;
 PERFORM endpoint_disposition_review_assert_v1(checked_decision);
 PERFORM supersession_review_assert_v1(checked_decision);
 IF TG_TABLE_NAME='endpoint_review_requests' OR EXISTS (SELECT 1 FROM admission_decisions WHERE admission_decision_id=checked_decision AND
 (review_binding_contract_version='reviewed-endpoint-admission/v1' OR disposition_review_binding_contract_version='reviewed-endpoint-disposition/v1')) THEN
 SELECT count(*) INTO n FROM endpoint_review_requests r WHERE r.admission_decision_id=checked_decision
 AND ((EXISTS (SELECT 1 FROM canonical_endpoint_review_bindings b WHERE b.admission_decision_id=r.admission_decision_id AND b.request_id=r.request_id AND b.proposal_occurrence_id=r.proposal_occurrence_id))
 <> (EXISTS (SELECT 1 FROM endpoint_disposition_review_bindings b WHERE b.admission_decision_id=r.admission_decision_id AND b.request_id=r.request_id AND b.proposal_occurrence_id=r.proposal_occurrence_id)));
 IF n<>1 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='endpoint request lacks one exact terminal receipt'; END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION canonical_supersession_lock_targets_v1(node_ids TEXT[])
RETURNS SETOF TEXT LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 IF node_ids IS NULL OR cardinality(node_ids) NOT BETWEEN 1 AND 64 OR EXISTS
 (SELECT 1 FROM unnest(node_ids) id WHERE id IS NULL OR octet_length(id)>200 OR id !~ '^canon-node:[a-z0-9:-]+$')
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='bounded supersession targets required'; END IF;
 RETURN QUERY SELECT canonical_node_id FROM canonical_graph_nodes WHERE canonical_node_id=ANY(node_ids) ORDER BY canonical_node_id FOR UPDATE;
END $$;
DO $$
DECLARE t TEXT;
BEGIN
 EXECUTE format('ALTER FUNCTION %I.canonical_supersession_lock_targets_v1(text[]) SET search_path TO pg_catalog, %I, pg_temp',current_schema(),current_schema());
 FOREACH t IN ARRAY ARRAY['endpoint_review_requests','endpoint_disposition_review_bindings','supersession_review_bindings'] LOOP
 EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1()',t||'_append_only',t);
 EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1()',t||'_forbid_truncate',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['admission_decisions','proposal_occurrences','endpoint_review_requests','canonical_endpoint_review_bindings','endpoint_disposition_review_bindings','supersession_review_bindings'] LOOP
 EXECUTE format('CREATE CONSTRAINT TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %I DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reviewed_lifecycle_authority_trigger_v1()',t||'_reviewed_lifecycle',t);
 END LOOP;
END $$;
REVOKE EXECUTE ON FUNCTION endpoint_disposition_review_assert_v1(TEXT),supersession_review_assert_v1(TEXT),reviewed_lifecycle_authority_trigger_v1(),canonical_supersession_lock_targets_v1(TEXT[]) FROM PUBLIC;

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
    IF decision_contract = 'reviewed-endpoint-disposition/v1' THEN
        IF EXISTS (SELECT 1 FROM source_claim_disposition_review_bindings WHERE admission_decision_id=checked_decision_id) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='endpoint disposition cannot reuse source review binding';
        END IF;
        PERFORM endpoint_disposition_review_assert_v1(checked_decision_id);
        RETURN;
    END IF;
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


CREATE OR REPLACE FUNCTION canonical_supersession_event_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME<>'canonical_supersession_admission_events' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid supersession authority trigger context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession admission event is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.event_id);
    RETURN NULL;
END $$;
REVOKE EXECUTE ON FUNCTION canonical_supersession_event_authority_trigger() FROM PUBLIC;

CREATE OR REPLACE FUNCTION canonical_supersession_member_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME<>'canonical_supersession_members' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid supersession authority trigger context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.first_admission_event_id);
    RETURN NULL;
END $$;
REVOKE EXECUTE ON FUNCTION canonical_supersession_member_authority_trigger() FROM PUBLIC;

CREATE OR REPLACE FUNCTION canonical_supersession_target_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME<>'canonical_supersession_replacement_targets' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid supersession authority trigger context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
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
REVOKE EXECUTE ON FUNCTION canonical_supersession_target_authority_trigger() FROM PUBLIC;

CREATE OR REPLACE FUNCTION canonical_supersession_proposal_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
DECLARE
    protected_event_id TEXT;
BEGIN
    IF TG_TABLE_NAME<>'proposal_occurrences' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid supersession authority trigger context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
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
REVOKE EXECUTE ON FUNCTION canonical_supersession_proposal_authority_trigger() FROM PUBLIC;
