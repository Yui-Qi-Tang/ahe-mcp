-- One store-scoped serialization coordinate covers negative lookups and phantom edges.
CREATE TABLE evidence_governance_head (
    singleton boolean PRIMARY KEY CHECK (singleton),
    store_id text NOT NULL UNIQUE DEFAULT ('store:' || gen_random_uuid()::text),
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0)
);
INSERT INTO evidence_governance_head(singleton) VALUES (true);

CREATE FUNCTION evidence_governance_lock_v1(target regclass) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE r bigint;
BEGIN
    -- Only a real, migration-owner-owned head is accepted. Runtime roles cannot
    -- create one or redirect this helper to arbitrary privileged tables.
    IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles p ON p.oid=c.relowner
        WHERE c.oid=target AND c.relname='evidence_governance_head' AND c.relkind='r'
          AND p.rolname=current_user AND has_schema_privilege(CASE WHEN current_setting('role')='none' THEN session_user::text ELSE current_setting('role') END,c.relnamespace,'USAGE')) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='invalid governance head';
    END IF;
    EXECUTE format('UPDATE %s SET revision=revision WHERE singleton RETURNING revision',target) INTO STRICT r;
    RETURN r;
END $$;
REVOKE ALL ON FUNCTION evidence_governance_lock_v1(regclass) FROM PUBLIC;

CREATE FUNCTION evidence_governance_touch_v1() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE affected bigint;
BEGIN
    EXECUTE format('UPDATE %I.evidence_governance_head SET revision=revision+1 WHERE singleton',TG_TABLE_SCHEMA);
    GET DIAGNOSTICS affected=ROW_COUNT;
    IF affected<>1 THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='governance head missing'; END IF;
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION evidence_governance_touch_v1() FROM PUBLIC;

CREATE TABLE canonical_withdrawal_events (
    event_id text PRIMARY KEY,
    request_id text NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200),
    target_id text NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    revision bigint NOT NULL CHECK (revision > 0),
    previous_event_id text REFERENCES canonical_withdrawal_events(event_id),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, revision),
    CONSTRAINT canonical_withdrawal_events_digest CHECK (event_id='governance:sha256:' || encode(sha256(convert_to(body,'UTF8')),'hex'))
);
CREATE TRIGGER canonical_withdrawal_events_immutable_rows BEFORE UPDATE OR DELETE ON canonical_withdrawal_events
FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER canonical_withdrawal_events_immutable_table BEFORE TRUNCATE ON canonical_withdrawal_events
FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();
CREATE TABLE source_assessment_events (
    event_id text PRIMARY KEY,
    request_id text NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200),
    target_id text NOT NULL REFERENCES source_snapshots(source_snapshot_id),
    revision bigint NOT NULL CHECK (revision > 0),
    previous_event_id text REFERENCES source_assessment_events(event_id),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, revision),
    CONSTRAINT source_assessment_events_digest CHECK (event_id='governance:sha256:' || encode(sha256(convert_to(body,'UTF8')),'hex'))
);
CREATE TRIGGER source_assessment_events_immutable_rows BEFORE UPDATE OR DELETE ON source_assessment_events
FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER source_assessment_events_immutable_table BEFORE TRUNCATE ON source_assessment_events
FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();

CREATE FUNCTION evidence_governance_event_guard_v1() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE
    b jsonb := NEW.body::jsonb; d jsonb; req jsonb;
    prev_id text; prev_rev bigint; store text; expected_hash text; item jsonb;
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ambiguous governance envelope';
    END IF;
    d := (b->>'display')::jsonb;
    req := d->'request';
    SELECT store_id INTO STRICT store FROM evidence_governance_head WHERE singleton;
    EXECUTE format('SELECT event_id,revision FROM %I.%I WHERE target_id=$1 ORDER BY revision DESC LIMIT 1',TG_TABLE_SCHEMA,TG_TABLE_NAME)
      INTO prev_id,prev_rev USING NEW.target_id;
    IF NEW.revision IS DISTINCT FROM coalesce(prev_rev,0)+1 OR NEW.previous_event_id IS DISTINCT FROM prev_id
       OR (req->>'expected_revision')::bigint IS DISTINCT FROM coalesce(prev_rev,0)
       OR nullif(req->>'previous_event_id','') IS DISTINCT FROM prev_id THEN
        RAISE EXCEPTION USING ERRCODE='40001',MESSAGE='governance revision conflict';
    END IF;
    IF b->>'contract' IS DISTINCT FROM 'evidence-governance-event/v1'
       OR b->>'request_id' IS DISTINCT FROM NEW.request_id OR req->>'target_id' IS DISTINCT FROM NEW.target_id
       OR d->>'store_id' IS DISTINCT FROM store OR d->>'contract' IS DISTINCT FROM 'evidence-governance-review/v1'
       OR b->>'decision' IS DISTINCT FROM 'approved'
       OR coalesce(length(btrim(b->>'reviewer_id')),0) NOT BETWEEN 1 AND 200
       OR coalesce(length(btrim(b->>'reason')),0) NOT BETWEEN 1 AND 2000
       OR b->>'subject' IS DISTINCT FROM 'governance-review:sha256:' || encode(sha256(convert_to(b->>'display','UTF8')),'hex') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='governance review binding mismatch';
    END IF;
    IF TG_TABLE_NAME='canonical_withdrawal_events' THEN
        IF req->>'kind' IS DISTINCT FROM 'withdrawal' OR coalesce(req->>'operation','') NOT IN ('withdraw','restore')
           OR NOT EXISTS(SELECT 1 FROM canonical_graph_nodes WHERE canonical_node_id=NEW.target_id AND node_kind IN ('source_claim','derived_claim')) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid withdrawal target or operation';
        END IF;
        IF (req->>'operation'='restore' AND (prev_id IS NULL OR
            NOT EXISTS(SELECT 1 FROM canonical_withdrawal_events e WHERE e.event_id=prev_id AND ((e.body::jsonb->>'display')::jsonb #>> '{request,operation}')='withdraw'))) THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='restore requires an active withdrawal';
        END IF;
        FOR item IN SELECT value FROM jsonb_array_elements(coalesce(nullif(req->'correction_ids','null'::jsonb),'[]'::jsonb)) LOOP
            IF item #>> '{}' = NEW.target_id OR NOT EXISTS (SELECT 1 FROM canonical_graph_nodes
                WHERE canonical_node_id=item #>> '{}' AND node_kind IN ('source_claim','derived_claim')) THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='correction must reference independent admitted evidence';
            END IF;
        END LOOP;
    ELSE
        SELECT raw_content_hash INTO STRICT expected_hash FROM source_snapshots WHERE source_snapshot_id=NEW.target_id;
        IF req->>'kind' IS DISTINCT FROM 'source_assessment' OR req->>'operation' IS DISTINCT FROM 'assess'
           OR d #>> '{source,snapshot,ID}' IS DISTINCT FROM NEW.target_id
           OR d #>> '{source,snapshot,RawContentHash}' IS DISTINCT FROM expected_hash THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='source assessment coordinates mismatch';
        END IF;
        IF jsonb_typeof(req->'checks') IS DISTINCT FROM 'array' OR jsonb_array_length(req->'checks') NOT BETWEEN 1 AND 32 THEN
            RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='source findings required';
        END IF;
        FOR item IN SELECT value FROM jsonb_array_elements(coalesce(nullif(req->'resolves_event_ids','null'::jsonb),'[]'::jsonb)) LOOP
            IF NOT EXISTS(SELECT 1 FROM source_assessment_events WHERE event_id=item #>> '{}' AND target_id=NEW.target_id) THEN
                RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='resolution must name earlier findings on this exact snapshot';
            END IF;
        END LOOP;
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION evidence_governance_event_guard_v1() FROM PUBLIC;
CREATE TRIGGER canonical_withdrawal_events_review_guard BEFORE INSERT ON canonical_withdrawal_events FOR EACH ROW EXECUTE FUNCTION evidence_governance_event_guard_v1();
CREATE TRIGGER source_assessment_events_review_guard BEFORE INSERT ON source_assessment_events FOR EACH ROW EXECUTE FUNCTION evidence_governance_event_guard_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_graph_nodes FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_graph_edges FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_derivations FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_derivation_parents FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_ordinary_admission_manifests FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_ordinary_admission_node_bindings FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_ordinary_admission_edge_bindings FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON admission_decisions FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_supersession_lineages FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_supersession_members FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_supersession_admission_events FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_supersession_admission_head FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_supersession_replacement_targets FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON source_snapshots FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON source_blobs FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON source_file_snapshots FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON extraction_runs FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON extraction_attempts FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON extraction_views FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON proposal_occurrences FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON canonical_withdrawal_events FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
CREATE TRIGGER evidence_governance_touch BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON source_assessment_events FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();
