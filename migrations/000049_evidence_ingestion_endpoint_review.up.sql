-- Exact endpoint review is independent of source-claim and relation approval.
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_review_binding_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_review_binding_contract_ck
 CHECK (review_binding_contract_version IS NULL OR review_binding_contract_version IN
 ('reviewed-source-claim-admission/v1','reviewed-endpoint-admission/v1'));

CREATE TABLE canonical_endpoint_review_bindings (
 admission_decision_id TEXT PRIMARY KEY REFERENCES canonical_ordinary_admission_manifests(admission_decision_id) DEFERRABLE INITIALLY DEFERRED,
 proposal_occurrence_id TEXT NOT NULL UNIQUE REFERENCES proposal_occurrences(proposal_occurrence_id),
 request_id TEXT NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200 AND btrim(request_id)=request_id),
 review_subject TEXT NOT NULL CHECK (review_subject ~ '^endpoint-review:sha256:[0-9a-f]{64}$'),
 display_payload TEXT NOT NULL CHECK (octet_length(display_payload) BETWEEN 1 AND 1048576),
 receipt_payload JSONB NOT NULL CHECK (jsonb_typeof(receipt_payload)='object' AND octet_length(receipt_payload::text)<=32768),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE FUNCTION canonical_endpoint_review_assert_v1(checked_decision_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
 authority_count BIGINT;
 decision_contract TEXT;
 binding_count BIGINT;
BEGIN
 SELECT review_binding_contract_version INTO decision_contract FROM admission_decisions WHERE admission_decision_id=checked_decision_id;
 SELECT count(*) INTO binding_count FROM canonical_endpoint_review_bindings WHERE admission_decision_id=checked_decision_id;
 IF decision_contract IS DISTINCT FROM 'reviewed-endpoint-admission/v1' AND binding_count=0 THEN RETURN; END IF;
 SELECT count(*) INTO authority_count FROM canonical_endpoint_review_bindings b
 JOIN admission_decisions d USING(admission_decision_id)
 JOIN proposal_occurrences p ON p.proposal_occurrence_id=b.proposal_occurrence_id AND d.proposal_occurrence_id=p.proposal_occurrence_id
 JOIN canonical_ordinary_admission_manifests m USING(admission_decision_id)
 WHERE b.admission_decision_id=checked_decision_id
 AND d.review_binding_contract_version='reviewed-endpoint-admission/v1'
 AND d.outcome='admitted' AND p.admission_outcome='admitted' AND p.canonical_ref=d.canonical_ref
 AND m.canonical_ref=d.canonical_ref AND m.proposal_occurrence_id=p.proposal_occurrence_id
 AND b.review_subject='endpoint-review:sha256:'||encode(sha256(convert_to(b.display_payload,'UTF8')),'hex')
 AND b.display_payload::jsonb->>'contract_version'='endpoint-review/v1'
 AND b.display_payload::jsonb#>>'{request,proposal_occurrence_id}'=p.proposal_occurrence_id
 AND b.display_payload::jsonb#>>'{proposal,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND b.display_payload::jsonb#>>'{proposal,StatementText}'=p.statement_text
 AND b.display_payload::jsonb#>>'{proposal,ProposalFingerprint}'=p.proposal_fingerprint
 AND b.display_payload::jsonb#>>'{proposal,ExtractionAttemptID}'=p.extraction_attempt_id
 AND b.display_payload::jsonb#>'{proposal,SourceRefs}'=p.source_refs
 AND ((b.display_payload::jsonb#>>'{request,kind}'='derived_spec' AND m.mutation_kind='derived_claim')
   OR (b.display_payload::jsonb#>>'{request,kind}'='repository_code' AND m.mutation_kind='source_backed_claim'
    AND p.proposal_fingerprint_version='code-fact-v1'))
 AND b.receipt_payload->>'contract_version'='reviewed-endpoint-admission/v1'
 AND b.receipt_payload->>'request_id'=b.request_id AND b.receipt_payload->>'review_subject'=b.review_subject
 AND b.receipt_payload->>'reviewer_id'=d.decision_by AND b.receipt_payload->>'decision_reason'=d.decision_reason
 AND b.receipt_payload#>>'{admission,AdmissionDecisionID}'=d.admission_decision_id
 AND b.receipt_payload#>>'{admission,ProposalOccurrenceID}'=p.proposal_occurrence_id
 AND b.receipt_payload#>>'{admission,CanonicalRef}'=d.canonical_ref
 AND b.receipt_payload#>>'{admission,AdmissionOutcome}'='admitted';
 IF authority_count<>1 THEN
 RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='endpoint reviewed decision/binding authority differs';
 END IF;
END $$;
CREATE FUNCTION canonical_endpoint_review_trigger_v1()
RETURNS TRIGGER LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 IF TG_TABLE_NAME<>'canonical_endpoint_review_bindings' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP<>'INSERT' THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid endpoint review trigger context';
 END IF;
 PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
 PERFORM canonical_endpoint_review_assert_v1(NEW.admission_decision_id);
 RETURN NULL;
END $$;
CREATE TRIGGER canonical_endpoint_review_append_only BEFORE UPDATE OR DELETE ON canonical_endpoint_review_bindings
 FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_endpoint_review_forbid_truncate BEFORE TRUNCATE ON canonical_endpoint_review_bindings
 FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE CONSTRAINT TRIGGER canonical_endpoint_review_authority AFTER INSERT ON canonical_endpoint_review_bindings
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION canonical_endpoint_review_trigger_v1();
REVOKE EXECUTE ON FUNCTION canonical_endpoint_review_assert_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_endpoint_review_trigger_v1() FROM PUBLIC;

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


-- Same FOR KEY SHARE lock as the native writer, without granting node UPDATE.
-- This function cannot choose a schema or mutate evidence.
CREATE FUNCTION canonical_endpoint_lock_nodes_v1(node_ids TEXT[])
RETURNS SETOF TEXT LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
 IF node_ids IS NULL OR cardinality(node_ids) NOT BETWEEN 1 AND 9 OR EXISTS (
  SELECT 1 FROM unnest(node_ids) AS id WHERE id IS NULL OR octet_length(id)>200 OR id !~ '^canon-node:[a-z0-9:-]+$'
 ) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='bounded canonical node IDs required'; END IF;
 RETURN QUERY SELECT n.canonical_node_id FROM canonical_graph_nodes n
 WHERE n.canonical_node_id=ANY(node_ids) ORDER BY n.canonical_node_id FOR KEY SHARE;
END $$;
DO $$
BEGIN
 EXECUTE format('ALTER FUNCTION %I.canonical_endpoint_lock_nodes_v1(text[]) SET search_path TO pg_catalog, %I, pg_temp',
  current_schema(),current_schema());
END $$;
REVOKE EXECUTE ON FUNCTION canonical_endpoint_lock_nodes_v1(TEXT[]) FROM PUBLIC;
