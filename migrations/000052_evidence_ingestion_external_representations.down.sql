DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM external_representation_records) OR EXISTS(SELECT 1 FROM external_check_representation_links) THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external representation rollback requires empty history';
 END IF;
END $$;
DROP TABLE external_check_representation_links;
DROP TABLE external_representation_records;
DROP FUNCTION external_representation_link_guard();
DROP FUNCTION external_representation_guard();
