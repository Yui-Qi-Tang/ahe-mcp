-- External JSON is stored exactly. Dependencies and checker links are annotations,
-- never synthesized evidence, admission decisions, or executable Core logic.
CREATE TABLE external_representation_records (
    representation_id text COLLATE "C" PRIMARY KEY,
    request_id text COLLATE "C" NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 512),
    proposal_occurrence_id text NOT NULL REFERENCES proposal_occurrences(proposal_occurrence_id),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
    previous_id text REFERENCES external_representation_records(representation_id),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT external_representation_digest CHECK (representation_id = 'representation:sha256:' || encode(sha256(convert_to(body,'UTF8')),'hex'))
);
CREATE INDEX external_representation_proposal_idx ON external_representation_records(proposal_occurrence_id, representation_id);

CREATE TABLE external_check_representation_links (
    check_id text NOT NULL REFERENCES external_check_records(check_id),
    input_id text COLLATE "C" NOT NULL CHECK (octet_length(input_id) BETWEEN 1 AND 128),
    representation_id text NOT NULL REFERENCES external_representation_records(representation_id),
    PRIMARY KEY(check_id,input_id)
);
CREATE INDEX external_representation_links_idx ON external_check_representation_links(representation_id,check_id);

CREATE FUNCTION external_representation_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE
    b jsonb:=NEW.body::jsonb;
    subject jsonb;
    content jsonb;
    dep jsonb;
    key jsonb;
    field text;
    pointer text;
    component text;
    cursor_value jsonb;
    dependency_id text;
    seen_keys text[]:=ARRAY[]::text[];
    seen_pointers text[]:=ARRAY[]::text[];
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='ambiguous representation envelope';
    END IF;
    SELECT jsonb_build_object('proposal_occurrence_id',p.proposal_occurrence_id,
        'source_snapshot_id',s.source_snapshot_id,'extraction_view_id',v.extraction_view_id,
        'source_version',s.source_version,'raw_content_hash',s.raw_content_hash,
        'rendered_content_hash',v.rendered_content_hash,'proposal_fingerprint',p.proposal_fingerprint)
    INTO subject
    FROM proposal_occurrences p JOIN extraction_attempts a USING(extraction_attempt_id)
      JOIN extraction_runs r USING(extraction_run_id)
      JOIN source_snapshots s USING(source_snapshot_id)
      JOIN extraction_views v ON v.extraction_view_id=r.extraction_view_id AND v.source_snapshot_id=s.source_snapshot_id
    WHERE p.proposal_occurrence_id=NEW.proposal_occurrence_id AND a.status='succeeded'
      AND p.proposal_kind='statement' AND NOT (p.proposed_payload ?| ARRAY['code_fact','code_relation','canonical_contradiction']);
    IF subject IS NULL OR b->'subject' IS DISTINCT FROM subject
       OR b->>'contract' IS DISTINCT FROM 'external-representation/v1'
       OR b->>'request_id' IS DISTINCT FROM NEW.request_id
       OR NULLIF(b->>'previous_id','') IS DISTINCT FROM NEW.previous_id THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external representation subject or envelope mismatch';
    END IF;
    FOREACH field IN ARRAY ARRAY['name','version','role','format','format_version','recorded_by','producer','mapping_claim','content'] LOOP
        IF jsonb_typeof(b->field) IS DISTINCT FROM 'string' OR length(btrim(b->>field))=0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external representation field missing';
        END IF;
    END LOOP;
    IF b->>'role' NOT IN ('source','candidate') OR NOT ((b->>'content') IS JSON WITH UNIQUE KEYS) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation requires unambiguous JSON and role';
    END IF;
    content:=(b->>'content')::jsonb;
    IF NOT (b ? 'dependencies') OR jsonb_typeof(b->'dependencies') NOT IN ('array','null') THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation dependency list required';
    END IF;
    IF jsonb_typeof(b->'dependencies')='array' THEN
        IF jsonb_array_length(b->'dependencies')>64 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation dependency bounds';
        END IF;
        FOR dep IN SELECT value FROM jsonb_array_elements(b->'dependencies') LOOP
            key:=dep->'key';
            FOREACH field IN ARRAY ARRAY['namespace','local_id','scope_ref','revision'] LOOP
                IF jsonb_typeof(key->field) IS DISTINCT FROM 'string' OR octet_length(key->>field) NOT BETWEEN 1 AND 512 OR length(btrim(key->>field))=0 THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency identity missing';
                END IF;
            END LOOP;
            IF key IS DISTINCT FROM jsonb_build_object('namespace',key->>'namespace','local_id',key->>'local_id','scope_ref',key->>'scope_ref','revision',key->>'revision') THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency identity must contain exactly the four defined fields';
            END IF;
            dependency_id:='dependency:sha256:'||proposition_digest('external-dependency/v1',key->>'namespace',key->>'local_id',key->>'scope_ref',key->>'revision');
            IF dependency_id=ANY(seen_keys) OR COALESCE(length(btrim(dep->>'reason')),0)=0 THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='duplicate dependency or missing reason';
            END IF;
            seen_keys:=array_append(seen_keys,dependency_id);
            IF dep->>'status'='missing' THEN
                IF COALESCE(dep->>'source_snapshot_id','')<>'' OR COALESCE(dep->>'extraction_view_id','')<>'' OR COALESCE(dep->>'rendered_content_hash','')<>'' THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='missing dependency cannot claim supplied evidence';
                END IF;
            ELSIF dep->>'status'='supplied' THEN
                IF NOT EXISTS(SELECT 1 FROM extraction_views WHERE extraction_view_id=dep->>'extraction_view_id'
                   AND source_snapshot_id=dep->>'source_snapshot_id' AND rendered_content_hash=dep->>'rendered_content_hash') THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='supplied dependency source mismatch';
                END IF;
            ELSE
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency status required';
            END IF;
            IF jsonb_typeof(dep->'pointers') IS DISTINCT FROM 'array' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency pointers required';
            END IF;
            IF jsonb_array_length(dep->'pointers') NOT BETWEEN 1 AND 32 THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency pointer bounds';
            END IF;
            FOR pointer IN SELECT jsonb_array_elements_text(dep->'pointers') LOOP
                IF pointer IS NULL OR left(pointer,1)<>'/' OR pointer ~ '~([^01]|$)' OR pointer=ANY(seen_pointers) THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='invalid or duplicate dependency pointer';
                END IF;
                seen_pointers:=array_append(seen_pointers,pointer);
                cursor_value:=content;
                FOREACH component IN ARRAY (CASE WHEN pointer='/' THEN ARRAY['']::text[] ELSE string_to_array(substring(pointer FROM 2),'/') END) LOOP
                    component:=replace(replace(component,'~1','/'),'~0','~');
                    IF jsonb_typeof(cursor_value)='array' THEN
                        IF component !~ '^(0|[1-9][0-9]*)$' OR length(component)>9 THEN
                            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='noncanonical dependency array index';
                        END IF;
                        cursor_value:=cursor_value->component::integer;
                    ELSIF jsonb_typeof(cursor_value)='object' THEN
                        cursor_value:=cursor_value->component;
                    ELSE
                        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency pointer crosses scalar or missing path';
                    END IF;
                END LOOP;
                IF cursor_value IS DISTINCT FROM to_jsonb(dependency_id) THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='dependency pointer identity mismatch';
                END IF;
            END LOOP;
        END LOOP;
    END IF;
    IF NEW.previous_id IS NOT NULL THEN
        IF NOT EXISTS(SELECT 1 FROM external_representation_records WHERE representation_id=NEW.previous_id AND body::jsonb->'subject'=subject)
           OR COALESCE(length(btrim(b->>'revision_reason')),0)=0 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation revision subject mismatch';
        END IF;
    ELSIF COALESCE(b->>'revision_reason','')<>'' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation previous reference missing';
    END IF;
    IF COALESCE(b->>'canonical_node_id','')<>'' THEN
        IF NOT EXISTS(SELECT 1 FROM canonical_graph_nodes n JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
            WHERE n.canonical_node_id=b->>'canonical_node_id' AND n.origin_proposal_occurrence_id=NEW.proposal_occurrence_id
              AND n.node_kind IN ('source_claim','derived_claim') AND p.admission_outcome='admitted' AND p.canonical_ref=n.canonical_node_id) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation canonical subject mismatch';
        END IF;
    END IF;
    IF COALESCE(b->>'derivation_id','')<>'' AND NOT EXISTS(SELECT 1 FROM canonical_derivations
        WHERE derivation_id=b->>'derivation_id' AND node_id=b->>'canonical_node_id') THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='representation derivation mismatch';
    END IF;
    NEW.recorded_at:=clock_timestamp();
    RETURN NEW;
END $$;

CREATE FUNCTION external_representation_link_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE
    report jsonb;
    representation_body text;
    declaration jsonb;
    material jsonb;
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    SELECT body::jsonb INTO report FROM external_check_records WHERE check_id=NEW.check_id;
    SELECT body INTO representation_body FROM external_representation_records WHERE representation_id=NEW.representation_id;
    declaration:=representation_body::jsonb;
    SELECT value INTO material FROM jsonb_array_elements(report->'materials') WHERE value->>'id'=NEW.input_id;
    IF report IS NULL OR declaration IS NULL OR material IS NULL OR report->'subject' IS DISTINCT FROM declaration->'subject'
       OR material->>'content' IS DISTINCT FROM representation_body
       OR material->>'role' IS DISTINCT FROM declaration->>'role'
       OR material->>'format' IS DISTINCT FROM 'external-representation/v1'
       OR material->>'version' IS DISTINCT FROM '1'
       OR material->>'mapping_claim' IS DISTINCT FROM declaration->>'mapping_claim' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='check input is not the exact representation envelope';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER external_representation_insert_guard BEFORE INSERT ON external_representation_records
 FOR EACH ROW EXECUTE FUNCTION external_representation_guard();
CREATE TRIGGER external_representation_immutable_rows BEFORE UPDATE OR DELETE ON external_representation_records
 FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER external_representation_immutable_table BEFORE TRUNCATE ON external_representation_records
 FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER external_representation_link_insert_guard BEFORE INSERT ON external_check_representation_links
 FOR EACH ROW EXECUTE FUNCTION external_representation_link_guard();
CREATE TRIGGER external_representation_link_immutable_rows BEFORE UPDATE OR DELETE ON external_check_representation_links
 FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER external_representation_link_immutable_table BEFORE TRUNCATE ON external_check_representation_links
 FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();
REVOKE ALL ON FUNCTION external_representation_guard(), external_representation_link_guard() FROM PUBLIC;
