-- Preserve the existing admission authority; candidate is a distinct target kind.
ALTER TABLE canonical_ordinary_admission_manifests
    DROP CONSTRAINT canonical_ordinary_admission_manifests_kind_ck,
    ADD CONSTRAINT canonical_ordinary_admission_manifests_kind_ck
        CHECK (mutation_kind IN ('source_backed_claim', 'derived_claim', 'candidate'));

ALTER TABLE canonical_contradiction_proposals
    ADD COLUMN source_proposal_occurrence_id TEXT
        CONSTRAINT canonical_contradiction_source_fk REFERENCES proposal_occurrences(proposal_occurrence_id);

CREATE OR REPLACE FUNCTION canonical_ordinary_admission_assert_decision_v1(
    checked_decision_id TEXT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    decision admission_decisions%ROWTYPE;
    proposal proposal_occurrences%ROWTYPE;
    manifest canonical_ordinary_admission_manifests%ROWTYPE;
    manifest_found BOOLEAN;
    supersession_count BIGINT;
    mismatch_count BIGINT;
    bound_row RECORD;
BEGIN
    SELECT * INTO decision
    FROM admission_decisions
    WHERE admission_decision_id = checked_decision_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'ordinary admission authority references a missing decision';
    END IF;

    SELECT * INTO proposal
    FROM proposal_occurrences
    WHERE proposal_occurrence_id = decision.proposal_occurrence_id;
    IF NOT FOUND
        OR proposal.admission_outcome IS DISTINCT FROM decision.outcome
        OR proposal.canonical_ref IS DISTINCT FROM decision.canonical_ref THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'admission decision and terminal proposal disagree';
    END IF;

    IF jsonb_typeof(decision.raw_evidence_node_ids) IS DISTINCT FROM 'array'
        OR jsonb_typeof(decision.canonical_edge_ids) IS DISTINCT FROM 'array'
        OR EXISTS (
            SELECT 1
            FROM jsonb_array_elements(decision.raw_evidence_node_ids) AS member
            WHERE jsonb_typeof(member) <> 'string'
        )
        OR EXISTS (
            SELECT 1
            FROM jsonb_array_elements(decision.canonical_edge_ids) AS member
            WHERE jsonb_typeof(member) <> 'string'
        ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'admission decision member arrays must contain only JSON strings';
    END IF;

    SELECT count(*) INTO supersession_count
    FROM canonical_supersession_admission_events
    WHERE admission_decision_id = checked_decision_id;

    SELECT * INTO manifest
    FROM canonical_ordinary_admission_manifests
    WHERE admission_decision_id = checked_decision_id;
    manifest_found := FOUND;

    IF decision.outcome <> 'admitted' THEN
        IF manifest_found OR supersession_count <> 0
            OR decision.canonical_ref IS NOT NULL
            OR decision.raw_evidence_node_ids <> '[]'::jsonb
            OR decision.canonical_edge_ids <> '[]'::jsonb THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'non-admitted decision has canonical admission authority';
        END IF;
        RETURN;
    END IF;

    IF supersession_count = 1 THEN
        IF manifest_found THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'supersession decision also has ordinary admission authority';
        END IF;
        FOR bound_row IN
            SELECT decision.canonical_ref AS canonical_node_id
            UNION ALL
            SELECT member.node_id
            FROM jsonb_array_elements_text(decision.raw_evidence_node_ids)
                AS member(node_id)
        LOOP
            PERFORM canonical_ordinary_admission_assert_node_v1(
                bound_row.canonical_node_id
            );
        END LOOP;
        FOR bound_row IN
            SELECT member.edge_id AS canonical_edge_id
            FROM jsonb_array_elements_text(decision.canonical_edge_ids)
                AS member(edge_id)
        LOOP
            PERFORM canonical_ordinary_admission_assert_edge_v1(
                bound_row.canonical_edge_id
            );
        END LOOP;
        RETURN;
    END IF;
    IF supersession_count <> 0 OR NOT manifest_found THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'admitted decision lacks exactly one authority manifest';
    END IF;

    IF manifest.contract_version <> 'ordinary-admission/v1'
        OR manifest.proposal_occurrence_id <> decision.proposal_occurrence_id
        OR manifest.canonical_ref <> decision.canonical_ref
        OR manifest.admission_outcome <> decision.outcome THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'ordinary admission manifest disagrees with its decision';
    END IF;

    SELECT count(*) INTO mismatch_count
    FROM canonical_ordinary_admission_node_bindings AS binding
    WHERE binding.admission_decision_id = checked_decision_id
      AND (
        (
            binding.binding_role = 'canonical_ref'
            AND (
                binding.binding_position <> 0
                OR binding.canonical_node_id <> decision.canonical_ref
            )
        )
        OR
        (
            binding.binding_role = 'raw_evidence'
            AND NOT EXISTS (
                SELECT 1
                FROM jsonb_array_elements_text(decision.raw_evidence_node_ids)
                    WITH ORDINALITY AS member(node_id, ordinality)
                WHERE member.ordinality - 1 = binding.binding_position
                  AND member.node_id = binding.canonical_node_id
            )
        )
      );
    IF mismatch_count <> 0
        OR (SELECT count(*)
            FROM canonical_ordinary_admission_node_bindings
            WHERE admission_decision_id = checked_decision_id) <>
            1 + jsonb_array_length(decision.raw_evidence_node_ids)
        OR NOT EXISTS (
            SELECT 1
            FROM canonical_ordinary_admission_node_bindings
            WHERE admission_decision_id = checked_decision_id
              AND binding_role = 'canonical_ref'
              AND binding_position = 0
              AND canonical_node_id = decision.canonical_ref
        ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'ordinary admission node bindings do not match decision order';
    END IF;

    SELECT count(*) INTO mismatch_count
    FROM canonical_ordinary_admission_edge_bindings AS binding
    WHERE binding.admission_decision_id = checked_decision_id
      AND (
        binding.binding_role <>
            CASE manifest.mutation_kind
                WHEN 'source_backed_claim' THEN 'supports_claim'
                ELSE 'derived_from'
            END
        OR NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements_text(decision.canonical_edge_ids)
                WITH ORDINALITY AS member(edge_id, ordinality)
            WHERE member.ordinality - 1 = binding.binding_position
              AND member.edge_id = binding.canonical_edge_id
        )
      );
    IF mismatch_count <> 0
        OR (SELECT count(*)
            FROM canonical_ordinary_admission_edge_bindings
            WHERE admission_decision_id = checked_decision_id) <>
            jsonb_array_length(decision.canonical_edge_ids) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'ordinary admission edge bindings do not match decision order';
    END IF;

    SELECT count(*) INTO mismatch_count
    FROM canonical_ordinary_admission_node_bindings AS binding
    JOIN canonical_graph_nodes AS node
      ON node.canonical_node_id = binding.canonical_node_id
    WHERE binding.admission_decision_id = checked_decision_id
      AND (
        (binding.materialization = 'materialized'
            AND node.origin_proposal_occurrence_id <>
                decision.proposal_occurrence_id)
        OR
        (binding.materialization = 'reused'
            AND node.origin_proposal_occurrence_id =
                decision.proposal_occurrence_id)
      );
    IF mismatch_count <> 0 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'ordinary admission node materialization ownership is inconsistent';
    END IF;

    SELECT count(*) INTO mismatch_count
    FROM canonical_ordinary_admission_edge_bindings AS binding
    JOIN canonical_graph_edges AS edge
      ON edge.canonical_edge_id = binding.canonical_edge_id
    WHERE binding.admission_decision_id = checked_decision_id
      AND (
        (binding.materialization = 'materialized'
            AND edge.origin_proposal_occurrence_id <>
                decision.proposal_occurrence_id)
        OR
        (binding.materialization = 'reused'
            AND edge.origin_proposal_occurrence_id =
                decision.proposal_occurrence_id)
      );
    IF mismatch_count <> 0 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'ordinary admission edge materialization ownership is inconsistent';
    END IF;

    FOR bound_row IN
        SELECT canonical_node_id
        FROM canonical_ordinary_admission_node_bindings
        WHERE admission_decision_id = checked_decision_id
    LOOP
        PERFORM canonical_ordinary_admission_assert_node_v1(
            bound_row.canonical_node_id
        );
    END LOOP;
    FOR bound_row IN
        SELECT canonical_edge_id
        FROM canonical_ordinary_admission_edge_bindings
        WHERE admission_decision_id = checked_decision_id
    LOOP
        PERFORM canonical_ordinary_admission_assert_edge_v1(
            bound_row.canonical_edge_id
        );
    END LOOP;

    IF manifest.mutation_kind = 'source_backed_claim' THEN
        IF jsonb_array_length(decision.raw_evidence_node_ids) = 0
            OR jsonb_array_length(decision.canonical_edge_ids) <>
                jsonb_array_length(decision.raw_evidence_node_ids)
            OR NOT EXISTS (
                SELECT 1 FROM canonical_graph_nodes
                WHERE canonical_node_id = decision.canonical_ref
                  AND node_kind = 'source_claim'
            )
            OR EXISTS (
                SELECT 1
                FROM canonical_ordinary_admission_node_bindings AS binding
                JOIN canonical_graph_nodes AS node
                  ON node.canonical_node_id = binding.canonical_node_id
                WHERE binding.admission_decision_id = checked_decision_id
                  AND binding.binding_role = 'raw_evidence'
                  AND node.node_kind <> 'raw_evidence'
            )
            OR EXISTS (
                SELECT 1
                FROM canonical_ordinary_admission_edge_bindings AS binding
                JOIN canonical_graph_edges AS edge
                  ON edge.canonical_edge_id = binding.canonical_edge_id
                WHERE binding.admission_decision_id = checked_decision_id
                  AND (
                    edge.relation <> 'supports_claim'
                    OR edge.to_node_id <> decision.canonical_ref
                    OR NOT decision.raw_evidence_node_ids
                        @> jsonb_build_array(edge.from_node_id)
                  )
            )
            OR EXISTS (
                SELECT 1
                FROM canonical_ordinary_admission_node_bindings AS raw_binding
                LEFT JOIN canonical_ordinary_admission_edge_bindings AS edge_binding
                  ON edge_binding.admission_decision_id =
                        raw_binding.admission_decision_id
                 AND edge_binding.binding_position = raw_binding.binding_position
                LEFT JOIN canonical_graph_edges AS edge
                  ON edge.canonical_edge_id = edge_binding.canonical_edge_id
                WHERE raw_binding.admission_decision_id = checked_decision_id
                  AND raw_binding.binding_role = 'raw_evidence'
                  AND (
                    edge_binding.canonical_edge_id IS NULL
                    OR edge.from_node_id IS DISTINCT FROM raw_binding.canonical_node_id
                    OR edge.to_node_id IS DISTINCT FROM decision.canonical_ref
                    OR edge.relation IS DISTINCT FROM 'supports_claim'
                  )
            )
            OR EXISTS (
                SELECT 1 FROM canonical_derivations
                WHERE node_id = decision.canonical_ref
            ) THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'source-backed ordinary admission shape is inconsistent';
        END IF;
    ELSE
        IF manifest.mutation_kind = 'derived_claim' AND EXISTS (
            SELECT 1 FROM canonical_derivations d
            JOIN canonical_derivation_parents p ON p.derivation_id = d.derivation_id
            JOIN canonical_graph_nodes n ON n.canonical_node_id = p.parent_node_id
            WHERE d.node_id = decision.canonical_ref AND n.node_kind = 'candidate'
        ) THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'derived claim cannot promote a candidate parent';
        END IF;
        IF decision.raw_evidence_node_ids <> '[]'::jsonb
            OR jsonb_array_length(decision.canonical_edge_ids) = 0
            OR NOT EXISTS (
                SELECT 1
                FROM canonical_graph_nodes AS node
                JOIN canonical_derivations AS derivation
                  ON derivation.node_id = node.canonical_node_id
                WHERE node.canonical_node_id = decision.canonical_ref
                  AND node.node_kind = manifest.mutation_kind
                  AND node.origin_proposal_occurrence_id =
                        decision.proposal_occurrence_id
                  AND derivation.origin_proposal_occurrence_id =
                        decision.proposal_occurrence_id
            )
            OR EXISTS (
                SELECT 1
                FROM canonical_ordinary_admission_edge_bindings AS binding
                JOIN canonical_graph_edges AS edge
                  ON edge.canonical_edge_id = binding.canonical_edge_id
                LEFT JOIN canonical_derivations AS derivation
                  ON derivation.node_id = decision.canonical_ref
                LEFT JOIN canonical_derivation_parents AS parent
                  ON parent.derivation_id = derivation.derivation_id
                 AND parent.canonical_edge_id = edge.canonical_edge_id
                WHERE binding.admission_decision_id = checked_decision_id
                  AND (
                    binding.materialization <> 'materialized'
                    OR edge.relation <> 'derived_from'
                    OR edge.to_node_id <> decision.canonical_ref
                    OR edge.origin_proposal_occurrence_id <>
                        decision.proposal_occurrence_id
                    OR parent.parent_node_id IS DISTINCT FROM edge.from_node_id
                    OR binding.binding_position <> (
                        SELECT count(*)
                        FROM canonical_derivation_parents AS preceding
                        WHERE preceding.derivation_id = derivation.derivation_id
                          AND preceding.parent_node_id < parent.parent_node_id
                    )
                  )
            )
            OR (SELECT count(*)
                FROM canonical_derivation_parents AS parent
                JOIN canonical_derivations AS derivation
                  ON derivation.derivation_id = parent.derivation_id
                WHERE derivation.node_id = decision.canonical_ref) <>
                jsonb_array_length(decision.canonical_edge_ids) THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'derived ordinary admission shape is inconsistent';
        END IF;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION canonical_ordinary_admission_derivation_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    checked_proposal_id TEXT;
    checked_decision_id TEXT;
    checked_node_id TEXT;
BEGIN
    IF TG_TABLE_NAME NOT IN (
        'canonical_derivations',
        'canonical_derivation_parents'
    ) OR TG_LEVEL <> 'ROW' OR TG_WHEN <> 'AFTER'
        OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical ordinary admission derivation trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    IF TG_TABLE_NAME = 'canonical_derivations' THEN
        checked_proposal_id := CASE WHEN TG_OP = 'DELETE'
            THEN OLD.origin_proposal_occurrence_id
            ELSE NEW.origin_proposal_occurrence_id
        END;
        checked_node_id := CASE WHEN TG_OP = 'DELETE'
            THEN OLD.node_id
            ELSE NEW.node_id
        END;
    ELSE
        SELECT
            derivation.origin_proposal_occurrence_id,
            derivation.node_id
        INTO checked_proposal_id, checked_node_id
        FROM canonical_derivations AS derivation
        WHERE derivation.derivation_id = CASE WHEN TG_OP = 'DELETE'
            THEN OLD.derivation_id
            ELSE NEW.derivation_id
        END;
    END IF;
    SELECT admission_decision_id INTO checked_decision_id
    FROM admission_decisions
    WHERE proposal_occurrence_id = checked_proposal_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical derivation lacks admission decision authority';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM canonical_ordinary_admission_manifests AS manifest
        WHERE manifest.admission_decision_id = checked_decision_id
          AND manifest.mutation_kind IN ('derived_claim', 'candidate')
          AND manifest.canonical_ref = checked_node_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical derivation lacks derived ordinary admission authority';
    END IF;
    PERFORM canonical_ordinary_admission_assert_decision_v1(checked_decision_id);
    RETURN NULL;
END $$;
CREATE FUNCTION canonical_contradiction_source_guard_v1()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
BEGIN
    PERFORM set_config('search_path', format('pg_catalog,%I,pg_temp', TG_TABLE_SCHEMA), true);
    IF TG_OP = 'UPDATE' AND (NEW.source_proposal_occurrence_id IS DISTINCT FROM OLD.source_proposal_occurrence_id
        OR (OLD.source_proposal_occurrence_id IS NOT NULL AND
            (to_jsonb(NEW) - ARRAY['admission_outcome','canonical_edge_id','decided_at']) IS DISTINCT FROM
            (to_jsonb(OLD) - ARRAY['admission_outcome','canonical_edge_id','decided_at']))) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'relation source binding is immutable';
    END IF;
    IF NEW.source_proposal_occurrence_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM proposal_occurrences p
        JOIN extraction_attempts a USING (extraction_attempt_id)
        JOIN extraction_runs r USING (extraction_run_id)
        WHERE p.proposal_occurrence_id = NEW.source_proposal_occurrence_id
          AND p.proposal_kind = 'statement' AND p.statement_text = NEW.rationale
          AND a.status = 'succeeded' AND jsonb_array_length(p.source_refs) > 0
          AND r.source_snapshot_id IS NOT NULL AND r.extraction_view_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'relation source must be a grounded statement matching the exact rationale';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION canonical_contradiction_source_guard_v1() FROM PUBLIC;
CREATE TRIGGER canonical_contradiction_source_guard
BEFORE INSERT OR UPDATE ON canonical_contradiction_proposals
FOR EACH ROW EXECUTE FUNCTION canonical_contradiction_source_guard_v1();
