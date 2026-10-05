-- Preserve existing journal bytes. Refuse incompatible history; never rewrite it.
-- json (not jsonb) retains the integer token consumed by Go encoding/json.
LOCK TABLE consistency_watches, consistency_watch_versions, consistency_runs,
    consistency_run_artifacts, consistency_events IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM consistency_watch_versions
        WHERE json_typeof(body::json->'expected_revision') IS DISTINCT FROM 'number'
           OR body::json->>'expected_revision' IS DISTINCT FROM (revision-1)::text)
       OR EXISTS (SELECT 1 FROM consistency_runs
        WHERE json_typeof(body::json->'revision') IS DISTINCT FROM 'number'
           OR body::json->>'revision' IS DISTINCT FROM revision::text)
       OR EXISTS (SELECT 1 FROM consistency_runs r,
            json_array_elements(r.body::json->'artifacts') item
        WHERE json_typeof(item->'bytes') IS DISTINCT FROM 'number'
           OR (item->>'bytes') !~ '^(0|[1-9][0-9]*)$')
       OR EXISTS (SELECT 1 FROM consistency_events e JOIN consistency_watch_versions v
            ON v.watch_id=e.watch_id AND v.revision=e.revision
            WHERE e.kind='configured' AND e.target_id IS DISTINCT FROM v.body_hash) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='consistency history contains incompatible JSON integers or configured hash; preserve and review before upgrade';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION consistency_version_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE previous bigint; payload json;
BEGIN
    PERFORM set_config('search_path', format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    PERFORM 1 FROM consistency_watches WHERE watch_id=NEW.watch_id FOR UPDATE;
    SELECT coalesce(max(revision), 0) INTO previous FROM consistency_watch_versions WHERE watch_id=NEW.watch_id;
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='invalid consistency configuration JSON';
    END IF;
    payload := NEW.body::json;
    IF payload->>'contract' IS DISTINCT FROM 'consistency-watch/v1'
       OR payload->>'watch_id' IS DISTINCT FROM NEW.watch_id
       OR payload->>'request_id' IS DISTINCT FROM NEW.request_id
       OR json_typeof(payload->'expected_revision') IS DISTINCT FROM 'number'
       OR payload->>'expected_revision' IS DISTINCT FROM previous::text
       OR NEW.revision <> previous+1
       OR json_typeof(payload->'request') IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='consistency configuration identity or revision mismatch';
    END IF;
    NEW.recorded_at := clock_timestamp();
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION consistency_run_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE payload json;
BEGIN
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='invalid consistency run JSON';
    END IF;
    payload := NEW.body::json;
    IF payload->>'contract' IS DISTINCT FROM 'consistency-run/v1'
       OR payload->>'watch_id' IS DISTINCT FROM NEW.watch_id
       OR json_typeof(payload->'revision') IS DISTINCT FROM 'number'
       OR payload->>'revision' IS DISTINCT FROM NEW.revision::text
       OR payload->>'target_id' IS DISTINCT FROM NEW.target_id
       OR json_typeof(payload->'diagnosis') IS DISTINCT FROM 'object'
       OR json_typeof(payload->'artifacts') IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='consistency run identity mismatch';
    END IF;
    NEW.recorded_at := clock_timestamp();
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION consistency_artifact_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE manifest json; expected bigint; actual bigint; total_bytes bigint;
BEGIN
    PERFORM set_config('search_path', format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    SELECT body::json->'artifacts' INTO manifest FROM consistency_runs WHERE run_id=NEW.run_id;
    expected := json_array_length(manifest);
    SELECT count(*), coalesce(sum(octet_length(content)),0) INTO actual,total_bytes
        FROM consistency_run_artifacts WHERE run_id=NEW.run_id;
    IF expected IS NULL OR expected <> actual OR total_bytes > 134217728
       OR expected <> (SELECT count(DISTINCT item->>'id') FROM json_array_elements(manifest) item)
       OR EXISTS (
           SELECT 1 FROM json_array_elements(manifest) item LEFT JOIN consistency_run_artifacts a
             ON a.run_id=NEW.run_id AND a.artifact_id=item->>'id'
           WHERE a.artifact_id IS NULL OR a.digest IS DISTINCT FROM item->>'sha256'
             OR json_typeof(item->'bytes') IS DISTINCT FROM 'number'
             OR item->>'bytes' IS DISTINCT FROM octet_length(a.content)::text
       ) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='consistency artifact manifest incomplete';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION consistency_event_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE previous bigint; configuration_hash text;
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    -- A per-watch row lock orders COMMIT visibility; a sequence alone can lose
    -- late-committing events behind a consumer's cursor.
    PERFORM 1 FROM consistency_watches WHERE watch_id=NEW.watch_id FOR UPDATE;
    SELECT coalesce(max(cursor),0) INTO previous FROM consistency_events WHERE watch_id=NEW.watch_id;
    IF NEW.cursor<>previous+1 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency event cursor mismatch'; END IF;
    IF NEW.kind='configured' THEN
        SELECT body_hash INTO configuration_hash FROM consistency_watch_versions
            WHERE watch_id=NEW.watch_id AND revision=NEW.revision;
        IF NEW.target_id IS DISTINCT FROM configuration_hash THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='consistency configured event hash mismatch';
        END IF;
    END IF;
    IF NEW.run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM consistency_runs WHERE run_id=NEW.run_id AND watch_id=NEW.watch_id AND revision=NEW.revision AND target_id=NEW.target_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency event result mismatch';
    END IF;
    NEW.recorded_at:=clock_timestamp(); RETURN NEW;
END $$;
