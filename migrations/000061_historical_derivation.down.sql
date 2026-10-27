DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM canonical_historical_derivation_reviews) OR EXISTS (SELECT 1 FROM canonical_derivations WHERE use_class<>'ordinary') THEN
 RAISE EXCEPTION 'cannot discard historical derivation authority'; END IF;
END $$;
DROP TRIGGER historical_derivation_class_authority ON canonical_derivations;
DROP TRIGGER historical_derivation_decision_authority ON admission_decisions;
DROP TABLE canonical_historical_derivation_reviews;
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
DROP FUNCTION historical_derivation_authority_trigger_v1();
DROP FUNCTION historical_derivation_assert_v1(text);
ALTER TABLE canonical_derivations DROP COLUMN use_class;
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_review_binding_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_review_binding_contract_ck CHECK (review_binding_contract_version IS NULL OR review_binding_contract_version IN ('reviewed-source-claim-admission/v1','reviewed-endpoint-admission/v1'));
ALTER TABLE admission_decisions DROP CONSTRAINT admission_decisions_disposition_review_contract_ck;
ALTER TABLE admission_decisions ADD CONSTRAINT admission_decisions_disposition_review_contract_ck CHECK (disposition_review_binding_contract_version IS NULL OR (disposition_review_binding_contract_version IN ('reviewed-source-claim-disposition/v1','reviewed-endpoint-disposition/v1') AND review_binding_contract_version IS NULL AND outcome IN ('rejected','audit_only') AND canonical_ref IS NULL AND raw_evidence_node_ids='[]'::jsonb AND canonical_edge_ids='[]'::jsonb));
