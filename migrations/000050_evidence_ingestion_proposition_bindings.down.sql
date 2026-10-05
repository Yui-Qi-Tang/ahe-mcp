-- Rollback must never erase recorded identity decisions.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM canonical_propositions)
    OR EXISTS (SELECT 1 FROM canonical_proposition_bindings)
    OR EXISTS (SELECT 1 FROM canonical_proposition_binding_events) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'proposition binding rollback requires empty history';
 END IF;
END $$;
DROP TABLE canonical_proposition_binding_events;
DROP TABLE canonical_proposition_bindings;
DROP TABLE canonical_propositions;
DROP FUNCTION proposition_binding_transition_guard();
DROP FUNCTION proposition_check_complete();
DROP FUNCTION proposition_check_member();
DROP FUNCTION proposition_immutable();
DROP FUNCTION proposition_digest(text[]);
