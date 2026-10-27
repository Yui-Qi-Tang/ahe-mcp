-- Historical purpose is independent of time, truth, and current-use clearance.
ALTER TABLE canonical_derivations ADD COLUMN use_class text NOT NULL DEFAULT 'ordinary'
 CONSTRAINT canonical_derivation_use_class_ck CHECK (use_class IN ('ordinary','historical_only'));
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_review_binding_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_review_binding_contract_ck CHECK
 (review_binding_contract_version IS NULL OR review_binding_contract_version IN ('reviewed-source-claim-admission/v1','reviewed-endpoint-admission/v1','historical-derivation-admission/v1'));
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_disposition_review_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_disposition_review_contract_ck CHECK
 (disposition_review_binding_contract_version IS NULL OR (disposition_review_binding_contract_version IN ('reviewed-source-claim-disposition/v1','reviewed-endpoint-disposition/v1','historical-derivation-admission/v1') AND review_binding_contract_version IS NULL AND outcome IN ('rejected','audit_only') AND canonical_ref IS NULL AND raw_evidence_node_ids='[]'::jsonb AND canonical_edge_ids='[]'::jsonb));
CREATE TABLE canonical_historical_derivation_reviews (
 admission_decision_id text PRIMARY KEY REFERENCES admission_decisions DEFERRABLE INITIALLY DEFERRED,
 proposal_occurrence_id text NOT NULL UNIQUE REFERENCES proposal_occurrences,
 request_id text NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200 AND btrim(request_id)=request_id),
 review_subject text NOT NULL CHECK (review_subject ~ '^historical-derivation-review:sha256:[0-9a-f]{64}$'),
 display_payload text NOT NULL CHECK (octet_length(display_payload) BETWEEN 1 AND 1048576 AND display_payload IS JSON OBJECT WITH UNIQUE KEYS),
 receipt_payload text NOT NULL CHECK (octet_length(receipt_payload) BETWEEN 1 AND 32768 AND receipt_payload IS JSON OBJECT WITH UNIQUE KEYS),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION historical_derivation_assert_v1(checked_decision_id text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE d admission_decisions%ROWTYPE; p proposal_occurrences%ROWTYPE;
 h canonical_historical_derivation_reviews%ROWTYPE; dr canonical_derivations%ROWTYPE;
 b jsonb; receipt jsonb; req jsonb; parents jsonb;
BEGIN
 SELECT * INTO d FROM admission_decisions WHERE admission_decision_id=checked_decision_id;
 IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='historical authority has no decision'; END IF;
 SELECT * INTO h FROM canonical_historical_derivation_reviews WHERE admission_decision_id=checked_decision_id;
 SELECT * INTO dr FROM canonical_derivations WHERE origin_proposal_occurrence_id=d.proposal_occurrence_id;
 IF coalesce(dr.use_class,'ordinary')='ordinary' AND h.admission_decision_id IS NULL
 AND d.review_binding_contract_version IS DISTINCT FROM 'historical-derivation-admission/v1'
 AND d.disposition_review_binding_contract_version IS DISTINCT FROM 'historical-derivation-admission/v1' THEN RETURN; END IF;
 SELECT * INTO STRICT p FROM proposal_occurrences WHERE proposal_occurrence_id=d.proposal_occurrence_id;
 b:=h.display_payload::jsonb; receipt:=h.receipt_payload::jsonb; req:=b->'request';
 IF NOT coalesce((h.proposal_occurrence_id=p.proposal_occurrence_id
 AND h.review_subject='historical-derivation-review:sha256:'||encode(sha256(convert_to(h.display_payload,'UTF8')),'hex')
 AND b->>'contract_version'='historical-derivation-review/v1' AND b->>'use_class'='historical_only'
 AND b#>>'{basis,use_review,store_id}'=(SELECT store_id FROM evidence_governance_head WHERE singleton)
 AND b#>>'{basis,proposal,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND b#>>'{basis,proposal,StatementText}'=p.statement_text
 AND b#>>'{basis,proposal,ProposalFingerprint}'=p.proposal_fingerprint
 AND b#>>'{basis,proposal,ExtractionAttemptID}'=p.extraction_attempt_id
 AND b#>'{basis,proposal,SourceRefs}'=p.source_refs
 AND req->>'proposal_occurrence_id'=p.proposal_occurrence_id
 AND NOT (req->'derivation' ? 'use_policy')
 AND receipt->>'contract_version'='historical-derivation-admission/v1'
 AND receipt->>'request_id'=h.request_id AND receipt->>'review_subject'=h.review_subject
 AND receipt->>'reviewer_id'=d.decision_by AND receipt->>'decision_reason'=d.decision_reason
 AND receipt->>'outcome'=d.outcome AND p.admission_outcome=d.outcome
 AND receipt->'request'=req AND NOT (receipt ? 'replayed')
 AND NOT EXISTS (SELECT 1 FROM canonical_endpoint_review_bindings WHERE admission_decision_id=checked_decision_id)
 AND NOT EXISTS (SELECT 1 FROM canonical_source_claim_review_bindings WHERE admission_decision_id=checked_decision_id)
 AND NOT EXISTS (SELECT 1 FROM source_claim_disposition_review_bindings WHERE admission_decision_id=checked_decision_id)
 AND NOT EXISTS (SELECT 1 FROM endpoint_disposition_review_bindings WHERE admission_decision_id=checked_decision_id)),false)
 THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='historical exact review authority differs'; END IF;
 IF d.outcome='admitted' THEN
 SELECT jsonb_agg(parent_node_id ORDER BY parent_node_id) INTO parents FROM canonical_derivation_parents WHERE derivation_id=dr.derivation_id;
 IF NOT coalesce((dr.use_class='historical_only' AND dr.node_id=d.canonical_ref AND p.canonical_ref=d.canonical_ref
 AND d.review_binding_contract_version='historical-derivation-admission/v1'
 AND d.disposition_review_binding_contract_version IS NULL
 AND jsonb_array_length(parents) BETWEEN 1 AND 32 AND parents=req#>'{derivation,parent_node_ids}'
 AND dr.method=req#>>'{derivation,method}' AND dr.producer=req#>>'{derivation,producer}' AND dr.trace_ref=req#>>'{derivation,trace_ref}'
 AND receipt#>>'{admission,AdmissionDecisionID}'=d.admission_decision_id
 AND receipt#>>'{admission,CanonicalRef}'=dr.node_id
 AND receipt#>>'{admission,DerivationID}'=dr.derivation_id
 AND receipt#>'{admission,ParentNodeIDs}'=parents
 AND receipt#>'{admission,CanonicalEdgeIDs}'=d.canonical_edge_ids
 AND receipt#>>'{admission,Replayed}'='false'
 AND receipt#>>'{admission,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND receipt#>>'{admission,AdmissionOutcome}'='admitted' AND NOT (receipt ? 'disposition')
 AND EXISTS (SELECT 1 FROM canonical_graph_nodes n WHERE n.canonical_node_id=dr.node_id AND n.node_kind='derived_claim')) ,false)
 THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='historical derivation classification or manifest differs'; END IF;
 ELSE
 IF NOT coalesce((d.outcome IN ('rejected','audit_only') AND dr.derivation_id IS NULL
 AND d.disposition_review_binding_contract_version='historical-derivation-admission/v1' AND d.review_binding_contract_version IS NULL
 AND receipt#>>'{disposition,AdmissionDecisionID}'=d.admission_decision_id
 AND receipt#>>'{disposition,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND receipt#>>'{disposition,AdmissionOutcome}'=d.outcome
 AND receipt#>>'{disposition,DecisionBy}'=d.decision_by
 AND receipt#>>'{disposition,DecisionReason}'=d.decision_reason
 AND receipt#>>'{disposition,Replayed}'='false'
 AND NOT (receipt ? 'admission')),false)
 THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='historical disposition authority differs'; END IF;
 END IF;
END $$;
CREATE FUNCTION historical_derivation_authority_trigger_v1() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE decision_id text;
BEGIN
 IF TG_OP<>'INSERT' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_TABLE_NAME NOT IN ('canonical_derivations','admission_decisions','canonical_historical_derivation_reviews') THEN
 RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='invalid historical authority trigger'; END IF;
 PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
 IF TG_TABLE_NAME='canonical_derivations' THEN SELECT admission_decision_id INTO decision_id FROM admission_decisions WHERE proposal_occurrence_id=NEW.origin_proposal_occurrence_id;
 ELSE decision_id:=NEW.admission_decision_id; END IF;
 PERFORM historical_derivation_assert_v1(decision_id);
 RETURN NULL;
END $$;
CREATE TRIGGER historical_derivation_immutable_rows BEFORE UPDATE OR DELETE ON canonical_historical_derivation_reviews FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER historical_derivation_immutable_table BEFORE TRUNCATE ON canonical_historical_derivation_reviews FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();
CREATE CONSTRAINT TRIGGER historical_derivation_binding_authority AFTER INSERT ON canonical_historical_derivation_reviews DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION historical_derivation_authority_trigger_v1();
CREATE CONSTRAINT TRIGGER historical_derivation_class_authority AFTER INSERT ON canonical_derivations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION historical_derivation_authority_trigger_v1();
CREATE CONSTRAINT TRIGGER historical_derivation_decision_authority AFTER INSERT ON admission_decisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION historical_derivation_authority_trigger_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT ON canonical_historical_derivation_reviews FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
REVOKE EXECUTE ON FUNCTION historical_derivation_assert_v1(text),historical_derivation_authority_trigger_v1() FROM PUBLIC;


CREATE OR REPLACE FUNCTION canonical_source_claim_review_binding_assert_v1(
    checked_decision_id TEXT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    decision_contract TEXT;
    binding_count BIGINT;
    authority_count BIGINT;
BEGIN
    IF EXISTS (SELECT 1 FROM admission_decisions WHERE admission_decision_id=checked_decision_id AND review_binding_contract_version='historical-derivation-admission/v1') THEN
        PERFORM historical_derivation_assert_v1(checked_decision_id);
        RETURN;
    END IF;
    SELECT review_binding_contract_version
    INTO decision_contract
    FROM admission_decisions
    WHERE admission_decision_id = checked_decision_id;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    SELECT count(*)
    INTO binding_count
    FROM canonical_source_claim_review_bindings
    WHERE admission_decision_id = checked_decision_id;

    IF decision_contract = 'reviewed-endpoint-admission/v1' THEN
        IF binding_count <> 0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='endpoint approval cannot reuse source review binding';
        END IF;
        PERFORM canonical_endpoint_review_assert_v1(checked_decision_id);
        RETURN;
    END IF;

    IF decision_contract IS NULL AND binding_count = 0 THEN
        RETURN;
    END IF;

    IF decision_contract IS NULL OR binding_count <> 1 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = format(
                'source-claim reviewed decision/binding count mismatch: contract=%s bindings=%s',
                coalesce(decision_contract, '<none>'),
                binding_count
            );
    END IF;

    SELECT count(*)
    INTO authority_count
    FROM canonical_source_claim_review_bindings AS binding
    JOIN admission_decisions AS decision
      ON decision.admission_decision_id = binding.admission_decision_id
     AND decision.proposal_occurrence_id = binding.proposal_occurrence_id
    JOIN proposal_occurrences AS proposal
      ON proposal.proposal_occurrence_id = binding.proposal_occurrence_id
     AND proposal.extraction_attempt_id = binding.extraction_attempt_id
    JOIN extraction_attempts AS attempt
      ON attempt.extraction_attempt_id = binding.extraction_attempt_id
    JOIN canonical_ordinary_admission_manifests AS manifest
      ON manifest.admission_decision_id = binding.admission_decision_id
     AND manifest.proposal_occurrence_id = binding.proposal_occurrence_id
    WHERE binding.admission_decision_id = checked_decision_id
      AND decision.outcome = 'admitted'
      AND decision.review_binding_contract_version = binding.contract_version
      AND decision.canonical_ref IS NOT NULL
      AND proposal.admission_outcome = decision.outcome
      AND proposal.canonical_ref = decision.canonical_ref
      AND proposal.proposal_kind = 'statement'
      AND proposal.proposal_fingerprint_version = 'statement-v1'
      AND attempt.status = 'succeeded'
      AND attempt.output_hash IS NOT NULL
      AND attempt.fixture_output IS NOT NULL
      AND manifest.contract_version = 'ordinary-admission/v1'
      AND manifest.mutation_kind = 'source_backed_claim'
      AND manifest.canonical_ref = decision.canonical_ref
      AND manifest.admission_outcome = decision.outcome
      AND binding.review_display_media_type =
            'application/vnd.ahe.review-package.v1+json'
      AND octet_length(binding.review_display_payload_utf8) <= 131072
      AND binding.review_display_artifact_id =
            'review-display:v1:sha256:' || encode(
                sha256(
                    convert_to(
                        binding.review_display_media_type || chr(10) ||
                            binding.review_display_payload_utf8,
                        'UTF8'
                    )
                ),
                'hex'
            )
      AND binding.review_display_payload_utf8::jsonb ->> 'contract_version' =
            binding.review_contract_version
      AND binding.review_display_payload_utf8::jsonb ->> 'review_package_id' =
            binding.review_package_id
      AND binding.review_display_payload_utf8::jsonb ->> 'submission_receipt_id' =
            binding.submission_receipt_id
      AND binding.review_display_payload_utf8::jsonb ->> 'proposed_effect' =
            binding.proposed_effect
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,proposal_basis_id}' =
            binding.proposal_basis_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,submission_receipt_id}' =
            binding.submission_receipt_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,proposal_manifest_id}' =
            binding.proposal_manifest_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,proposal_occurrence_id}' =
            binding.proposal_occurrence_id
      AND binding.review_display_payload_utf8::jsonb #>> '{proposal_basis,extraction_attempt_id}' =
            binding.extraction_attempt_id;

    IF authority_count = 1 THEN
        RETURN;
    END IF;
    RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = format(
            'source-claim review binding authority count is %s, expected 1',
            authority_count
        );
END $$;

CREATE OR REPLACE FUNCTION source_claim_disposition_review_assert_v1(checked_decision_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    decision_contract TEXT;
    binding_count BIGINT;
    authority_count BIGINT;
BEGIN
    IF EXISTS (SELECT 1 FROM admission_decisions WHERE admission_decision_id=checked_decision_id AND disposition_review_binding_contract_version='historical-derivation-admission/v1') THEN
        PERFORM historical_derivation_assert_v1(checked_decision_id);
        RETURN;
    END IF;
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
