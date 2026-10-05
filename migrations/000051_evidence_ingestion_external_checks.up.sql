-- External reports are historical annotations, not canonical evidence or admission authority.
CREATE TABLE external_check_records (
    check_id text COLLATE "C" PRIMARY KEY,
    request_id text COLLATE "C" NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 512),
    proposal_occurrence_id text NOT NULL REFERENCES proposal_occurrences(proposal_occurrence_id),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
    revises_id text REFERENCES external_check_records(check_id),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT external_check_digest CHECK (check_id = 'check:sha256:' || encode(sha256(convert_to(body, 'UTF8')), 'hex'))
);
CREATE INDEX external_check_proposal_idx ON external_check_records(proposal_occurrence_id, check_id);

CREATE FUNCTION external_record_immutable() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external records are append-only';
END $$;

CREATE FUNCTION external_check_record_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
DECLARE
    b jsonb := NEW.body::jsonb;
    subject jsonb;
    source_text text;
    candidate_text text;
    item jsonb;
    field text;
    ids text[] := ARRAY[]::text[];
    dimensions text[] := ARRAY[]::text[];
    used text[];
    input_id text;
BEGIN
    PERFORM set_config('search_path', format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='ambiguous external check envelope';
    END IF;
    SELECT jsonb_build_object('proposal_occurrence_id',p.proposal_occurrence_id,
        'source_snapshot_id',s.source_snapshot_id,'extraction_view_id',v.extraction_view_id,
        'source_version',s.source_version,'raw_content_hash',s.raw_content_hash,
        'rendered_content_hash',v.rendered_content_hash,'proposal_fingerprint',p.proposal_fingerprint),
        convert_from(v.rendered_content,'UTF8'),p.statement_text
    INTO subject,source_text,candidate_text
    FROM proposal_occurrences p JOIN extraction_attempts a USING(extraction_attempt_id)
      JOIN extraction_runs r USING(extraction_run_id)
      JOIN source_snapshots s USING(source_snapshot_id)
      JOIN extraction_views v ON v.extraction_view_id=r.extraction_view_id AND v.source_snapshot_id=s.source_snapshot_id
    WHERE p.proposal_occurrence_id=NEW.proposal_occurrence_id AND a.status='succeeded'
      AND p.proposal_kind='statement' AND NOT (p.proposed_payload ?| ARRAY['code_fact','code_relation','canonical_contradiction']);
    IF subject IS NULL OR (b->'subject') IS DISTINCT FROM subject
       OR (b->>'contract') IS DISTINCT FROM 'external-check/v1'
       OR (b->>'request_id') IS DISTINCT FROM NEW.request_id
       OR NULLIF(b->>'revises_id','') IS DISTINCT FROM NEW.revises_id THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check subject or envelope mismatch';
    END IF;
    FOREACH field IN ARRAY ARRAY['checker_name','checker_version','checker_configuration','run_ref','recorded_by'] LOOP
        IF jsonb_typeof(b->field) IS DISTINCT FROM 'string' OR length(btrim(b->>field))=0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check identity missing';
        END IF;
    END LOOP;
    IF jsonb_typeof(b->'materials') IS DISTINCT FROM 'array' OR jsonb_typeof(b->'findings') IS DISTINCT FROM 'array'
       OR jsonb_typeof(b->'limitations') IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check arrays required';
    END IF;
    IF jsonb_array_length(b->'materials') NOT BETWEEN 1 AND 32 OR jsonb_array_length(b->'findings') NOT BETWEEN 1 AND 128
       OR jsonb_array_length(b->'limitations') NOT BETWEEN 1 AND 32 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check array bounds';
    END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(b->'materials') LOOP
        FOREACH field IN ARRAY ARRAY['id','role','format','version','content','mapping_claim'] LOOP
            IF jsonb_typeof(item->field) IS DISTINCT FROM 'string' OR length(btrim(item->>field))=0 THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check material field missing';
            END IF;
        END LOOP;
        IF (item->>'id')=ANY(ids) OR (item->>'role') NOT IN ('source','candidate') THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check material identity';
        END IF;
        ids:=array_append(ids,item->>'id');
        IF item->>'format'='verbatim' AND (item->>'content') IS DISTINCT FROM
             (CASE WHEN item->>'role'='source' THEN source_text ELSE candidate_text END) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check verbatim input mismatch';
        END IF;
    END LOOP;
    FOR item IN SELECT value FROM jsonb_array_elements(b->'findings') LOOP
        FOREACH field IN ARRAY ARRAY['dimension','criterion','result','detail'] LOOP
            IF jsonb_typeof(item->field) IS DISTINCT FROM 'string' OR length(btrim(item->>field))=0 THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check finding field missing';
            END IF;
        END LOOP;
        IF (item->>'dimension')=ANY(dimensions) OR (item->>'result') NOT IN ('pass','fail','inconclusive','not_checked','unsupported') THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check finding result';
        END IF;
        dimensions:=array_append(dimensions,item->>'dimension');
        IF jsonb_typeof(item->'input_ids') NOT IN ('array','null') OR NOT (item ? 'input_ids') THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check input references required';
        END IF;
        used:=ARRAY[]::text[];
        IF jsonb_typeof(item->'input_ids')='array' THEN
            FOR input_id IN SELECT jsonb_array_elements_text(item->'input_ids') LOOP
                IF input_id IS NULL OR NOT input_id=ANY(ids) OR input_id=ANY(used) THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check unresolved input';
                END IF;
                used:=array_append(used,input_id);
            END LOOP;
        END IF;
        IF item->>'result' IN ('pass','fail','inconclusive') AND cardinality(used)=0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check actual inputs required';
        END IF;
    END LOOP;
    FOR item IN SELECT value FROM jsonb_array_elements(b->'limitations') LOOP
        IF jsonb_typeof(item) IS DISTINCT FROM 'string' OR length(btrim(item#>>'{}'))=0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check limitation required';
        END IF;
    END LOOP;
    IF NEW.revises_id IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM external_check_records WHERE check_id=NEW.revises_id AND body::jsonb->'subject'=subject)
           OR COALESCE(length(btrim(b->>'revision_reason')),0)=0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check revision subject mismatch';
        END IF;
    ELSIF COALESCE(b->>'revision_reason','')<>'' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external check revision target missing';
    END IF;
    NEW.recorded_at:=clock_timestamp();
    RETURN NEW;
END $$;
CREATE TRIGGER external_check_insert_guard BEFORE INSERT ON external_check_records
    FOR EACH ROW EXECUTE FUNCTION external_check_record_guard();
CREATE TRIGGER external_check_immutable_rows BEFORE UPDATE OR DELETE ON external_check_records
    FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER external_check_immutable_table BEFORE TRUNCATE ON external_check_records
    FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();
REVOKE ALL ON FUNCTION external_record_immutable(), external_check_record_guard() FROM PUBLIC;
