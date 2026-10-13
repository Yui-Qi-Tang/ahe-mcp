DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM canonical_withdrawal_events) OR EXISTS(SELECT 1 FROM source_assessment_events) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='cannot roll back populated governance history';
 END IF;
END $$;
-- Explicit operator rollback removes governance state; never run as a runtime role.
DROP TRIGGER evidence_governance_touch ON canonical_graph_nodes;
DROP TRIGGER evidence_governance_touch ON canonical_graph_edges;
DROP TRIGGER evidence_governance_touch ON canonical_derivations;
DROP TRIGGER evidence_governance_touch ON canonical_derivation_parents;
DROP TRIGGER evidence_governance_touch ON canonical_ordinary_admission_manifests;
DROP TRIGGER evidence_governance_touch ON canonical_ordinary_admission_node_bindings;
DROP TRIGGER evidence_governance_touch ON canonical_ordinary_admission_edge_bindings;
DROP TRIGGER evidence_governance_touch ON admission_decisions;
DROP TRIGGER evidence_governance_touch ON canonical_supersession_lineages;
DROP TRIGGER evidence_governance_touch ON canonical_supersession_members;
DROP TRIGGER evidence_governance_touch ON canonical_supersession_admission_events;
DROP TRIGGER evidence_governance_touch ON canonical_supersession_admission_head;
DROP TRIGGER evidence_governance_touch ON canonical_supersession_replacement_targets;
DROP TRIGGER evidence_governance_touch ON source_snapshots;
DROP TRIGGER evidence_governance_touch ON source_blobs;
DROP TRIGGER evidence_governance_touch ON source_file_snapshots;
DROP TRIGGER evidence_governance_touch ON extraction_runs;
DROP TRIGGER evidence_governance_touch ON extraction_attempts;
DROP TRIGGER evidence_governance_touch ON extraction_views;
DROP TRIGGER evidence_governance_touch ON proposal_occurrences;
DROP TRIGGER evidence_governance_touch ON canonical_withdrawal_events;
DROP TRIGGER evidence_governance_touch ON source_assessment_events;
DROP TABLE source_assessment_events, canonical_withdrawal_events;
DROP FUNCTION evidence_governance_event_guard_v1(), evidence_governance_touch_v1(), evidence_governance_lock_v1(regclass);
DROP TABLE evidence_governance_head;
