DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM canonical_contradiction_review_bindings) THEN
  RAISE EXCEPTION 'cannot discard persisted contradiction review authority';
 END IF;
END $$;
ALTER TABLE canonical_contradiction_admission_decisions DROP COLUMN review_binding_receipt_id;
DROP TABLE canonical_contradiction_review_bindings;
DROP FUNCTION canonical_contradiction_review_apply_v1();
