LOCK TABLE canonical_initial_lineage_registrations,canonical_supersession_members,canonical_supersession_lineages IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN IF EXISTS(SELECT 1 FROM canonical_initial_lineage_registrations) THEN RAISE EXCEPTION 'cannot discard initial registration authority'; END IF; END $$;
DROP TRIGGER canonical_initial_registration_apply ON canonical_initial_lineage_registrations;
DROP FUNCTION canonical_initial_lineage_registration_apply_v1();
DROP FUNCTION initial_registration_payload_json_v1(jsonb), initial_registration_binding_json_v1(jsonb), initial_registration_basis_json_v1(jsonb,boolean), initial_registration_json_string_v1(text);
ALTER TABLE canonical_supersession_members DROP CONSTRAINT canonical_supersession_members_origin_choice;
ALTER TABLE canonical_supersession_members DROP CONSTRAINT canonical_supersession_members_initial_registration_fk;
ALTER TABLE canonical_supersession_members DROP COLUMN initial_registration_event_id;
ALTER TABLE canonical_supersession_members ALTER COLUMN first_admission_event_id SET NOT NULL;
DROP TABLE canonical_initial_lineage_registrations;
DROP FUNCTION canonical_supersession_assert_member(text);
CREATE OR REPLACE FUNCTION canonical_supersession_assert_target(
    checked_event_id TEXT,
    checked_target_node_id TEXT,
    checked_edge_id TEXT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF checked_event_id IS NULL
        OR checked_target_node_id IS NULL
        OR checked_edge_id IS NULL THEN
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM canonical_supersession_replacement_targets AS target
        WHERE target.event_id = checked_event_id
          AND target.target_node_id = checked_target_node_id
    ) THEN
        PERFORM canonical_supersession_assert_edge(checked_edge_id);
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM canonical_supersession_replacement_targets AS target
        LEFT JOIN canonical_supersession_admission_events AS event
            ON event.event_id = target.event_id
        LEFT JOIN canonical_graph_edges AS edge
            ON edge.canonical_edge_id = target.canonical_edge_id
        LEFT JOIN canonical_graph_nodes AS replacement
            ON replacement.canonical_node_id = target.replacement_node_id
        LEFT JOIN canonical_graph_nodes AS replaced
            ON replaced.canonical_node_id = target.target_node_id
        LEFT JOIN canonical_supersession_members AS target_member
            ON target_member.canonical_node_id = target.target_node_id
           AND target_member.lineage_key = target.lineage_key
        LEFT JOIN canonical_supersession_admission_events AS first_event
            ON first_event.event_id = target_member.first_admission_event_id
        LEFT JOIN proposal_occurrences AS replacement_proposal
            ON replacement_proposal.proposal_occurrence_id =
                replacement.origin_proposal_occurrence_id
        LEFT JOIN proposal_occurrences AS replaced_proposal
            ON replaced_proposal.proposal_occurrence_id =
                replaced.origin_proposal_occurrence_id
        LEFT JOIN admission_decisions AS decision
            ON decision.admission_decision_id = event.admission_decision_id
        WHERE target.event_id = checked_event_id
          AND target.target_node_id = checked_target_node_id
          AND (
              target.canonical_edge_id <> checked_edge_id
              OR event.event_id IS NULL
              OR event.lineage_key <> target.lineage_key
              OR event.replacement_node_id <> target.replacement_node_id
              OR event.admission_outcome <> 'admitted'
              OR jsonb_typeof(event.event_payload -> 'target_node_ids')
                    IS DISTINCT FROM 'array'
              OR NOT (
                    event.event_payload -> 'target_node_ids'
                    @> jsonb_build_array(target.target_node_id)
              )
              OR edge.canonical_edge_id IS NULL
              OR edge.relation <> 'supersedes'
              OR edge.from_node_id <> target.replacement_node_id
              OR edge.to_node_id <> target.target_node_id
              OR edge.origin_proposal_occurrence_id <>
                    event.proposal_occurrence_id
              OR edge.provenance ->> 'trace_ref'
                    IS DISTINCT FROM event.event_id
              OR edge.provenance ->> 'review_ref'
                    IS DISTINCT FROM event.admission_decision_id
              OR edge.provenance ->> 'method'
                    IS DISTINCT FROM 'supersession_admission_edge'
              OR edge.provenance ->> 'method_version'
                    IS DISTINCT FROM 'v1'
              OR replacement.canonical_node_id IS NULL
              OR replacement.node_kind <> 'source_claim'
              OR replacement.origin_proposal_occurrence_id <>
                    event.proposal_occurrence_id
              OR replacement_proposal.proposal_occurrence_id IS NULL
              OR replacement_proposal.admission_outcome <> 'admitted'
              OR replacement_proposal.canonical_ref <>
                    replacement.canonical_node_id
              OR replaced.canonical_node_id IS NULL
              OR replaced.node_kind <> 'source_claim'
              OR target_member.canonical_node_id IS NULL
              OR NOT (
                    (
                        target_member.first_admission_event_id = event.event_id
                        AND target_member.was_bootstrapped
                        AND (
                            event.event_payload
                                -> 'bootstrapped_target_node_ids'
                        ) @> jsonb_build_array(target.target_node_id)
                    )
                    OR (
                        first_event.event_id IS NOT NULL
                        AND first_event.lineage_key = event.lineage_key
                        AND first_event.revision < event.revision
                    )
              )
              OR replaced_proposal.proposal_occurrence_id IS NULL
              OR replaced_proposal.admission_outcome <> 'admitted'
              OR replaced_proposal.canonical_ref <> replaced.canonical_node_id
              OR decision.admission_decision_id IS NULL
              OR decision.proposal_occurrence_id <>
                    event.proposal_occurrence_id
              OR decision.outcome <> 'admitted'
              OR decision.canonical_ref <> event.replacement_node_id
              OR jsonb_typeof(decision.canonical_edge_ids)
                    IS DISTINCT FROM 'array'
              OR NOT decision.canonical_edge_ids
                    @> jsonb_build_array(target.canonical_edge_id)
          )
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession replacement target authority is inconsistent';
    END IF;

    PERFORM canonical_supersession_assert_edge(checked_edge_id);
END $$;
CREATE OR REPLACE FUNCTION canonical_supersession_lineage_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession lineage is append-only';
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION canonical_supersession_member_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME<>'canonical_supersession_members' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid supersession authority trigger context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.first_admission_event_id);
    RETURN NULL;
END $$;
