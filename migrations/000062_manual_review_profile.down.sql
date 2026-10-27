LOCK TABLE source_snapshots, source_intake_requests, source_manual_review_profiles,
    canonical_source_claim_review_bindings, canonical_implements_admissions IN ACCESS EXCLUSIVE MODE;
DO $$
DECLARE
    profile_count BIGINT;
BEGIN
    SELECT count(*) INTO profile_count FROM source_manual_review_profiles;
    IF profile_count <> 0 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = format(
            'migration 000062 down requires zero manual review profiles: profiles=%s', profile_count);
    END IF;
END $$;

DROP TRIGGER canonical_implements_manual_profile_authority ON canonical_implements_admissions;
DROP TRIGGER canonical_source_claim_manual_profile_authority ON canonical_source_claim_review_bindings;
DROP TRIGGER source_manual_review_profiles_authority ON source_manual_review_profiles;
DROP TRIGGER source_manual_review_profiles_forbid_truncate ON source_manual_review_profiles;
DROP TRIGGER source_manual_review_profiles_append_only ON source_manual_review_profiles;
DROP TRIGGER source_intake_requests_manual_profile_immutable ON source_intake_requests;
DROP TRIGGER source_snapshots_manual_profile_immutable ON source_snapshots;
DROP TRIGGER source_snapshots_manual_profile_capture ON source_snapshots;
DROP FUNCTION source_manual_review_profile_review_trigger_v1();
DROP FUNCTION source_manual_review_profile_insert_trigger_v1();
DROP FUNCTION source_manual_review_profile_assert_basis_v1(TEXT, JSONB);
DROP FUNCTION source_manual_review_profile_assert_v1(TEXT);
DROP FUNCTION source_manual_review_profile_source_guard_v1();
DROP FUNCTION source_manual_review_profile_capture_trigger_v1();
DROP TABLE source_manual_review_profiles;
ALTER TABLE source_snapshots DROP COLUMN manual_profile_capture_xid;
