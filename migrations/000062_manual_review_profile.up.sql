-- A qualified manual profile belongs to the first immutable source capture.
-- Existing sources keep NULL capture authority; no metadata is reinterpreted.
LOCK TABLE source_snapshots, source_intake_requests,
    canonical_source_claim_review_bindings, canonical_implements_admissions
IN ACCESS EXCLUSIVE MODE;

ALTER TABLE source_snapshots ADD COLUMN manual_profile_capture_xid xid8;

CREATE TABLE source_manual_review_profiles (
    source_snapshot_id TEXT CONSTRAINT source_manual_review_profiles_pkey PRIMARY KEY
        CONSTRAINT source_manual_review_profiles_source_fk REFERENCES source_snapshots(source_snapshot_id),
    intake_request_id TEXT NOT NULL
        CONSTRAINT source_manual_review_profiles_request_uq UNIQUE
        CONSTRAINT source_manual_review_profiles_request_fk REFERENCES source_intake_requests(request_id),
    contract_version TEXT NOT NULL
        CONSTRAINT source_manual_review_profiles_contract_ck CHECK (contract_version = 'manual-source-review-profile/v1'),
    profile_payload_utf8 TEXT NOT NULL
        CONSTRAINT source_manual_review_profiles_size_ck CHECK (octet_length(profile_payload_utf8) BETWEEN 1 AND 24576),
    profile_hash TEXT NOT NULL
        CONSTRAINT source_manual_review_profiles_hash_ck CHECK (profile_hash =
            'sha256:' || encode(sha256(convert_to(profile_payload_utf8, 'UTF8')), 'hex')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION source_manual_review_profile_capture_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME <> 'source_snapshots' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'BEFORE' OR TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile capture trigger has invalid table or event context';
    END IF;
    -- Never use caller-supplied timestamps, xmin, or a caller-provided xid.
    NEW.manual_profile_capture_xid := CASE WHEN NEW.source_system = 'manual_text'
        THEN pg_catalog.pg_current_xact_id() ELSE NULL END;
    RETURN NEW;
END $$;

CREATE FUNCTION source_manual_review_profile_source_guard_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME NOT IN ('source_snapshots', 'source_intake_requests') OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'BEFORE' OR TG_OP <> 'UPDATE' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile source guard has invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config('search_path', pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    IF TG_TABLE_NAME = 'source_snapshots' THEN
        IF NEW.manual_profile_capture_xid IS DISTINCT FROM OLD.manual_profile_capture_xid
            OR (NEW IS DISTINCT FROM OLD AND EXISTS (
                SELECT 1 FROM source_manual_review_profiles WHERE source_snapshot_id = OLD.source_snapshot_id)) THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile source capture authority is immutable';
        END IF;
    ELSIF NEW IS DISTINCT FROM OLD AND EXISTS (
        SELECT 1 FROM source_manual_review_profiles WHERE intake_request_id = OLD.request_id) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile intake request binding is immutable';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION source_manual_review_profile_assert_v1(checked_source_snapshot_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    profile source_manual_review_profiles%ROWTYPE;
    payload JSONB;
    key_count INT;
    distinct_key_count INT;
    item JSONB;
    value TEXT;
    trim_chars TEXT := E' \t\n\r\f' || chr(11) || U&'\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000';
BEGIN
    SELECT * INTO profile FROM source_manual_review_profiles WHERE source_snapshot_id = checked_source_snapshot_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile binding is missing';
    END IF;
    payload := profile.profile_payload_utf8::jsonb;
    IF jsonb_typeof(payload) IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile requires an exact JSON object';
    END IF;
    SELECT count(*), count(DISTINCT key) INTO key_count, distinct_key_count
    FROM json_each(profile.profile_payload_utf8::json);
    IF key_count <> 5 OR distinct_key_count <> 5
        OR payload - ARRAY['contract_version', 'title', 'location', 'coverage', 'limitations'] <> '{}'::jsonb
        OR payload ->> 'contract_version' IS DISTINCT FROM profile.contract_version
        OR jsonb_typeof(payload -> 'title') IS DISTINCT FROM 'string'
        OR octet_length(payload ->> 'title') NOT BETWEEN 1 AND 1000
        OR btrim(payload ->> 'title', trim_chars) IS DISTINCT FROM payload ->> 'title'
        OR jsonb_typeof(payload -> 'location') IS DISTINCT FROM 'string'
        OR octet_length(payload ->> 'location') NOT BETWEEN 1 AND 2000
        OR btrim(payload ->> 'location', trim_chars) IS DISTINCT FROM payload ->> 'location'
        OR jsonb_typeof(payload -> 'coverage') IS DISTINCT FROM 'string'
        OR payload ->> 'coverage' NOT IN ('submitted_text', 'exact_excerpt')
        OR jsonb_typeof(payload -> 'limitations') IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile fields violate the typed bounded contract';
    END IF;
    IF jsonb_array_length(payload -> 'limitations') > 16
        OR (payload ->> 'coverage' = 'exact_excerpt' AND jsonb_array_length(payload -> 'limitations') = 0) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile limitations violate the coverage contract';
    END IF;
    FOR item IN SELECT * FROM jsonb_array_elements(payload -> 'limitations') LOOP
        value := item #>> '{}';
        IF jsonb_typeof(item) <> 'string' OR octet_length(value) NOT BETWEEN 1 AND 1000
            OR btrim(value, trim_chars) IS DISTINCT FROM value THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile limitation is invalid';
        END IF;
    END LOOP;
    IF NOT EXISTS (
        SELECT 1 FROM source_snapshots s
        JOIN source_blobs b ON b.raw_content_hash = s.raw_content_hash
        JOIN source_intake_requests r ON r.request_id = profile.intake_request_id
            AND r.source_snapshot_id = s.source_snapshot_id
        JOIN extraction_views v ON v.extraction_view_id = r.extraction_view_id
            AND v.source_snapshot_id = s.source_snapshot_id
        WHERE s.source_snapshot_id = profile.source_snapshot_id
            AND s.source_system = 'manual_text' AND s.manual_profile_capture_xid IS NOT NULL
            AND s.raw_content_hash = 'sha256:' || encode(sha256(b.raw_content), 'hex')
            AND b.byte_length = octet_length(b.raw_content)
            AND v.rendered_content_hash = 'sha256:' || encode(sha256(v.rendered_content), 'hex')
            AND r.request_payload_hash ~ '^sha256:[0-9a-f]{64}$'
    ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile lacks exact source, content and request binding';
    END IF;
END $$;

CREATE FUNCTION source_manual_review_profile_insert_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    capture_xid xid8;
BEGIN
    IF TG_TABLE_NAME <> 'source_manual_review_profiles' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile insert trigger has invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config('search_path', pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    SELECT manual_profile_capture_xid INTO capture_xid FROM source_snapshots
    WHERE source_snapshot_id = NEW.source_snapshot_id;
    IF capture_xid IS DISTINCT FROM pg_catalog.pg_current_xact_id() THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual review profile must be bound in its first capture transaction';
    END IF;
    PERFORM source_manual_review_profile_assert_v1(NEW.source_snapshot_id);
    RETURN NULL;
END $$;

CREATE FUNCTION source_manual_review_profile_assert_basis_v1(checked_source_snapshot_id TEXT, basis JSONB)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    profile source_manual_review_profiles%ROWTYPE;
    expected JSONB;
    payload JSONB;
BEGIN
    SELECT * INTO profile FROM source_manual_review_profiles WHERE source_snapshot_id = checked_source_snapshot_id;
    IF NOT FOUND THEN
        IF basis ? 'manual_review_profile' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'review claims a manual profile without native authority';
        END IF;
        RETURN;
    END IF;
    PERFORM source_manual_review_profile_assert_v1(checked_source_snapshot_id);
    payload := profile.profile_payload_utf8::jsonb;
    expected := jsonb_build_object(
        'source_snapshot_id', profile.source_snapshot_id,
        'intake_request_id', profile.intake_request_id,
        'contract_version', profile.contract_version,
        'profile', profile.profile_payload_utf8::jsonb,
        'profile_hash', profile.profile_hash);
    IF basis ->> 'source_snapshot_id' IS DISTINCT FROM checked_source_snapshot_id
        OR basis -> 'manual_review_profile' IS DISTINCT FROM expected
        OR basis -> 'source_title' IS DISTINCT FROM payload -> 'title'
        OR basis -> 'source_location' IS DISTINCT FROM payload -> 'location'
        OR basis -> 'source_coverage' IS DISTINCT FROM payload -> 'coverage'
        OR basis -> 'source_limitations' IS DISTINCT FROM payload -> 'limitations'
        OR NOT EXISTS (SELECT 1 FROM source_snapshots s
            WHERE s.source_snapshot_id = checked_source_snapshot_id
                AND basis ->> 'source_system' = s.source_system
                AND basis ->> 'source_id' = s.source_id
                AND basis ->> 'source_version' = s.source_version
                AND basis ->> 'raw_content_hash' = s.raw_content_hash) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'review manual profile differs from exact native source authority';
    END IF;
END $$;

CREATE FUNCTION source_manual_review_profile_review_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    snapshot_id TEXT;
    basis JSONB;
    leaves JSONB;
    leaf JSONB;
    display JSONB;
    leaf_count INT;
    resolved_count BIGINT;
BEGIN
    IF TG_TABLE_NAME NOT IN ('canonical_source_claim_review_bindings', 'canonical_implements_admissions')
        OR TG_LEVEL <> 'ROW' OR TG_WHEN <> 'AFTER' OR TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile review trigger has invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config('search_path', pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    IF TG_TABLE_NAME = 'canonical_source_claim_review_bindings' THEN
        SELECT r.source_snapshot_id INTO snapshot_id
        FROM proposal_occurrences p JOIN extraction_attempts a USING(extraction_attempt_id)
        JOIN extraction_runs r USING(extraction_run_id)
        WHERE p.proposal_occurrence_id = NEW.proposal_occurrence_id;
        IF snapshot_id IS NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual profile review lacks its native source origin';
        END IF;
        PERFORM source_manual_review_profile_assert_basis_v1(snapshot_id,
            NEW.review_display_payload_utf8::jsonb -> 'proposal_basis');
    ELSE
        leaves := NEW.basis_payload_utf8::jsonb #> '{basis,source_leaves}';
        display := NEW.display_payload_utf8::jsonb;
        IF jsonb_typeof(leaves) IS DISTINCT FROM 'array' THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual implements source leaves require an explicit bounded array';
        END IF;
        leaf_count := jsonb_array_length(leaves);
        IF leaf_count NOT BETWEEN 1 AND 63 OR (
            SELECT count(DISTINCT item ->> 'specification_node_id')
            FROM jsonb_array_elements(leaves) item) <> leaf_count THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual implements source leaves must each name one unique native source';
        END IF;
        -- The retained v1 validator verifies the depth-one parent count and
        -- ancestor edges, but not the leaf ID set. Do not let a caller replace
        -- a qualified manual parent with an unrelated (or duplicate) leaf.
        -- The retained v2 validator already checks its full recursive AND set.
        IF NEW.contract_version = 'canonical-derived-implements-admission/v1' AND (
            (SELECT count(*) FROM canonical_derivations d
                JOIN canonical_derivation_parents p USING(derivation_id)
                WHERE d.node_id = NEW.specification_node_id) <> leaf_count
            OR EXISTS (SELECT 1 FROM canonical_derivations d
                JOIN canonical_derivation_parents p USING(derivation_id)
                WHERE d.node_id = NEW.specification_node_id AND NOT EXISTS (
                    SELECT 1 FROM jsonb_array_elements(leaves) item
                    WHERE item ->> 'specification_node_id' = p.parent_node_id))) THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual implements source leaves differ from exact native AND parents';
        END IF;
        -- Enumerate the submitted leaves first. An INNER JOIN enumeration
        -- would silently omit unresolved IDs before the profile check ran.
        FOR leaf IN SELECT * FROM jsonb_array_elements(leaves) LOOP
            SELECT count(*), min(r.source_snapshot_id) INTO resolved_count, snapshot_id
            FROM canonical_graph_nodes n
            JOIN proposal_occurrences p ON p.proposal_occurrence_id = n.origin_proposal_occurrence_id
            JOIN extraction_attempts a USING(extraction_attempt_id)
            JOIN extraction_runs r USING(extraction_run_id)
            WHERE n.canonical_node_id = leaf ->> 'specification_node_id' AND n.node_kind = 'source_claim';
            IF resolved_count <> 1 THEN
                RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual implements source leaves lack exact native source origins';
            END IF;
            basis := leaf #> '{review_snapshot,review_package,proposal_basis}';
            PERFORM source_manual_review_profile_assert_basis_v1(snapshot_id, basis);
            IF EXISTS (SELECT 1 FROM source_manual_review_profiles WHERE source_snapshot_id = snapshot_id) THEN
                IF jsonb_typeof(display -> 'source_displays') IS DISTINCT FROM 'array' THEN
                    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual implements source display is missing its exact review card';
                END IF;
                IF (SELECT count(*) FROM jsonb_array_elements(display -> 'source_displays') card
                    WHERE card ->> 'node_id' = leaf ->> 'specification_node_id') <> 1
                    OR (SELECT count(*) FROM jsonb_array_elements(display -> 'source_displays') card
                    WHERE card ->> 'node_id' = leaf ->> 'specification_node_id'
                        AND card #> '{subject,review_subject}' = leaf #> '{review_snapshot,exact_review_subject}'
                        AND card #>> '{subject,review_display_artifact_id}' = card #>> '{display,review_display_artifact_id}'
                        AND card #>> '{display,contract_version}' = 'review-package-display/v1'
                        AND card #>> '{display,media_type}' = 'application/vnd.ahe.review-package.v1+json'
                        AND card #>> '{display,review_display_artifact_id}' = 'review-display:v1:sha256:' || encode(sha256(convert_to(
                            'application/vnd.ahe.review-package.v1+json' || chr(10) || (card #>> '{display,payload_utf8}'), 'UTF8')), 'hex')
                        AND (card #>> '{display,payload_utf8}')::jsonb = leaf #> '{review_snapshot,review_package}') <> 1 THEN
                    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'manual implements source display differs from its exact native profile review';
                END IF;
            END IF;
        END LOOP;
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER source_snapshots_manual_profile_capture
BEFORE INSERT ON source_snapshots
FOR EACH ROW EXECUTE FUNCTION source_manual_review_profile_capture_trigger_v1();
CREATE TRIGGER source_snapshots_manual_profile_immutable
BEFORE UPDATE ON source_snapshots
FOR EACH ROW EXECUTE FUNCTION source_manual_review_profile_source_guard_v1();
CREATE TRIGGER source_intake_requests_manual_profile_immutable
BEFORE UPDATE ON source_intake_requests
FOR EACH ROW EXECUTE FUNCTION source_manual_review_profile_source_guard_v1();
CREATE TRIGGER source_manual_review_profiles_append_only
BEFORE UPDATE OR DELETE ON source_manual_review_profiles
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER source_manual_review_profiles_forbid_truncate
BEFORE TRUNCATE ON source_manual_review_profiles
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER source_manual_review_profiles_authority
AFTER INSERT ON source_manual_review_profiles
FOR EACH ROW EXECUTE FUNCTION source_manual_review_profile_insert_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_source_claim_manual_profile_authority
AFTER INSERT ON canonical_source_claim_review_bindings DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION source_manual_review_profile_review_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_implements_manual_profile_authority
AFTER INSERT ON canonical_implements_admissions DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION source_manual_review_profile_review_trigger_v1();

REVOKE EXECUTE ON FUNCTION source_manual_review_profile_capture_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION source_manual_review_profile_source_guard_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION source_manual_review_profile_insert_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION source_manual_review_profile_review_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION source_manual_review_profile_assert_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION source_manual_review_profile_assert_basis_v1(TEXT, JSONB) FROM PUBLIC;

CREATE TRIGGER evidence_governance_touch BEFORE INSERT ON source_manual_review_profiles FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
