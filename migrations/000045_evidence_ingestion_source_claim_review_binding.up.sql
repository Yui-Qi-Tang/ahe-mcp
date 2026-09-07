-- The final reviewed-binding companion does not adopt ordinary rows written
-- against the intermediate migration-44 contract. Serving processes require
-- the latest schema, but this explicit fence also keeps manual partial
-- migrations from silently creating legacy review semantics.
LOCK TABLE proposal_occurrences IN EXCLUSIVE MODE;
LOCK TABLE
    admission_decisions,
    canonical_ordinary_admission_manifests,
    canonical_ordinary_admission_node_bindings,
    canonical_ordinary_admission_edge_bindings
IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    ordinary_manifest_count BIGINT;
BEGIN
    SELECT pg_catalog.count(*)
    INTO ordinary_manifest_count
    FROM canonical_ordinary_admission_manifests;

    IF ordinary_manifest_count <> 0 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = pg_catalog.format(
                'migration 000045 requires no intermediate ordinary admission history; ordinary_manifests=%s',
                ordinary_manifest_count
            );
    END IF;
END $$;

ALTER TABLE admission_decisions
    ADD COLUMN review_binding_contract_version TEXT,
    ADD CONSTRAINT admission_decisions_review_binding_contract_ck
        CHECK (
            review_binding_contract_version IS NULL
            OR review_binding_contract_version = 'reviewed-source-claim-admission/v1'
        );

CREATE TABLE canonical_source_claim_review_bindings (
    admission_decision_id TEXT
        CONSTRAINT canonical_source_claim_review_bindings_pkey PRIMARY KEY,
    proposal_occurrence_id TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_proposal_uq UNIQUE,
    contract_version TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_contract_ck
        CHECK (contract_version = 'reviewed-source-claim-admission/v1'),
    review_contract_version TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_review_contract_ck
        CHECK (review_contract_version = 'reviewable-ingestion/v1'),
    proposed_effect TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_effect_ck
        CHECK (proposed_effect = 'admit-source-backed-statement/v1'),
    extraction_attempt_id TEXT NOT NULL,
    submission_receipt_id TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_receipt_id_ck
        CHECK (
            submission_receipt_id ~
                '^submission-receipt:v1:sha256:[0-9a-f]{64}$'
        ),
    proposal_manifest_id TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_manifest_id_ck
        CHECK (
            proposal_manifest_id ~
                '^proposal-manifest:v1:sha256:[0-9a-f]{64}$'
        ),
    proposal_basis_id TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_basis_id_ck
        CHECK (
            proposal_basis_id ~
                '^proposal-basis:v1:sha256:[0-9a-f]{64}$'
        ),
    review_package_id TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_package_id_ck
        CHECK (
            review_package_id ~
                '^review-package:v1:sha256:[0-9a-f]{64}$'
        ),
    review_display_artifact_id TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_display_uq UNIQUE
        CONSTRAINT canonical_source_claim_review_bindings_display_id_ck
        CHECK (
            review_display_artifact_id ~
                '^review-display:v1:sha256:[0-9a-f]{64}$'
        ),
    review_display_media_type TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_media_type_ck
        CHECK (
            review_display_media_type =
                'application/vnd.ahe.review-package.v1+json'
        ),
    review_display_payload_utf8 TEXT NOT NULL
        CONSTRAINT canonical_source_claim_review_bindings_payload_size_ck
        CHECK (
            octet_length(review_display_payload_utf8) > 0
            AND octet_length(review_display_payload_utf8) <= 131072
        ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_source_claim_review_bindings_decision_fk
        FOREIGN KEY (admission_decision_id)
        REFERENCES admission_decisions(admission_decision_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT canonical_source_claim_review_bindings_proposal_fk
        FOREIGN KEY (proposal_occurrence_id)
        REFERENCES proposal_occurrences(proposal_occurrence_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT canonical_source_claim_review_bindings_attempt_fk
        FOREIGN KEY (extraction_attempt_id)
        REFERENCES extraction_attempts(extraction_attempt_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT canonical_source_claim_review_bindings_ordinary_manifest_fk
        FOREIGN KEY (admission_decision_id)
        REFERENCES canonical_ordinary_admission_manifests(admission_decision_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT canonical_source_claim_review_bindings_attempt_id_ck
        CHECK (btrim(extraction_attempt_id) <> '')
);

CREATE FUNCTION canonical_source_claim_review_binding_assert_v1(
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

CREATE FUNCTION canonical_source_claim_review_binding_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME NOT IN (
        'canonical_source_claim_review_bindings',
        'admission_decisions'
    ) OR TG_LEVEL <> 'ROW' OR TG_WHEN <> 'AFTER'
        OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical source-claim review binding trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    PERFORM canonical_source_claim_review_binding_assert_v1(
        CASE WHEN TG_OP = 'DELETE'
            THEN OLD.admission_decision_id
            ELSE NEW.admission_decision_id
        END
    );
    RETURN NULL;
END $$;

CREATE TRIGGER canonical_source_claim_review_bindings_append_only
BEFORE UPDATE OR DELETE ON canonical_source_claim_review_bindings
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_source_claim_review_bindings_forbid_truncate
BEFORE TRUNCATE ON canonical_source_claim_review_bindings
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE CONSTRAINT TRIGGER canonical_source_claim_review_bindings_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_source_claim_review_bindings
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_source_claim_review_binding_trigger_v1();
CREATE CONSTRAINT TRIGGER admission_decisions_source_claim_review_authority
AFTER INSERT OR UPDATE OR DELETE ON admission_decisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_source_claim_review_binding_trigger_v1();

REVOKE EXECUTE ON FUNCTION canonical_source_claim_review_binding_assert_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_source_claim_review_binding_trigger_v1() FROM PUBLIC;

