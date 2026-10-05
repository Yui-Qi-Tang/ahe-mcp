-- Restore prior guards without removing journal records. Stop consumers first.
CREATE OR REPLACE FUNCTION consistency_version_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE b jsonb; previous bigint;
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    PERFORM 1 FROM consistency_watches WHERE watch_id=NEW.watch_id FOR UPDATE;
    SELECT coalesce(max(revision),0) INTO previous FROM consistency_watch_versions WHERE watch_id=NEW.watch_id;
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid consistency configuration'; END IF;
    b:=NEW.body::jsonb;
    IF NEW.revision<>previous+1 OR (b->>'watch_id') IS DISTINCT FROM NEW.watch_id
       OR (b->>'request_id') IS DISTINCT FROM NEW.request_id OR (b->>'expected_revision')::bigint IS DISTINCT FROM previous
       OR (b->>'contract') IS DISTINCT FROM 'consistency-watch/v1'
       OR jsonb_typeof(b->'request') IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency configuration version mismatch';
    END IF;
    NEW.recorded_at:=clock_timestamp(); RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION consistency_run_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE b jsonb;
BEGIN
    IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid consistency result'; END IF;
    b:=NEW.body::jsonb;
    IF (b->>'contract') IS DISTINCT FROM 'consistency-run/v1' OR (b->>'watch_id') IS DISTINCT FROM NEW.watch_id
       OR (b->>'revision')::bigint IS DISTINCT FROM NEW.revision OR (b->>'target_id') IS DISTINCT FROM NEW.target_id
       OR jsonb_typeof(b->'diagnosis') IS DISTINCT FROM 'object' OR jsonb_typeof(b->'artifacts') IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency result identity mismatch';
    END IF;
    NEW.recorded_at:=clock_timestamp(); RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION consistency_artifact_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE r text; manifest jsonb; expected integer; actual integer; total_bytes bigint;
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    r:=NEW.run_id;
    SELECT body::jsonb->'artifacts' INTO manifest FROM consistency_runs WHERE run_id=r;
    expected:=jsonb_array_length(manifest);
    SELECT count(*),coalesce(sum(octet_length(content)),0) INTO actual,total_bytes FROM consistency_run_artifacts WHERE run_id=r;
    IF expected<>actual OR total_bytes>134217728 OR expected<>(SELECT count(DISTINCT m->>'id') FROM jsonb_array_elements(manifest) m) OR EXISTS (
      SELECT 1 FROM jsonb_array_elements(manifest) m LEFT JOIN consistency_run_artifacts a
      ON a.run_id=r AND a.artifact_id=m->>'id'
      WHERE a.artifact_id IS NULL OR a.digest IS DISTINCT FROM m->>'sha256' OR octet_length(a.content) IS DISTINCT FROM (m->>'bytes')::bigint
    ) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency artifacts incomplete'; END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION consistency_event_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
DECLARE previous bigint;
BEGIN
    PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    -- A per-watch row lock orders COMMIT visibility; a sequence alone can lose
    -- late-committing events behind a consumer's cursor.
    PERFORM 1 FROM consistency_watches WHERE watch_id=NEW.watch_id FOR UPDATE;
    SELECT coalesce(max(cursor),0) INTO previous FROM consistency_events WHERE watch_id=NEW.watch_id;
    IF NEW.cursor<>previous+1 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency event cursor mismatch'; END IF;
    IF NEW.run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM consistency_runs WHERE run_id=NEW.run_id AND watch_id=NEW.watch_id AND revision=NEW.revision AND target_id=NEW.target_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='consistency event result mismatch';
    END IF;
    NEW.recorded_at:=clock_timestamp(); RETURN NEW;
END $$;
