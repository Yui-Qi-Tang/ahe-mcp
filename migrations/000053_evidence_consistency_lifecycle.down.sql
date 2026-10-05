DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM consistency_watches) THEN
        RAISE EXCEPTION 'cannot roll back consistency history with registered watches';
    END IF;
END $$;
DROP TABLE consistency_events,consistency_run_artifacts,consistency_runs,consistency_watch_versions,consistency_watches;
DROP FUNCTION consistency_version_guard(),consistency_run_guard(),consistency_artifact_guard(),consistency_event_guard();
