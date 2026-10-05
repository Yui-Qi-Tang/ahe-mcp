DO $$
BEGIN
    IF EXISTS(SELECT 1 FROM external_check_records) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='external checks rollback requires empty history';
    END IF;
END $$;
DROP TABLE external_check_records;
DROP FUNCTION external_check_record_guard();
DROP FUNCTION external_record_immutable();
