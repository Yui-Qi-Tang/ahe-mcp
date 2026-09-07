-- Adopt ordinary canonical admission only on an authority-fresh native MCP
-- schema. Source capture and extraction rows may already exist, but no proposal,
-- canonical graph, decision, derivation, contradiction, or supersession history
-- is converted by this migration. Migration 42's exact empty head is the sole
-- seeded authority row.
LOCK TABLE proposal_occurrences IN EXCLUSIVE MODE;
LOCK TABLE
    admission_decisions,
    canonical_graph_nodes,
    canonical_graph_edges,
    canonical_derivations,
    canonical_derivation_parents,
    canonical_contradiction_proposals,
    canonical_contradiction_admission_decisions,
    canonical_supersession_lineages,
    canonical_supersession_admission_events,
    canonical_supersession_admission_head,
    canonical_supersession_members,
    canonical_supersession_replacement_targets
IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    authority_row_count BIGINT;
    head_row_count BIGINT;
    empty_head_count BIGINT;
BEGIN
    SELECT
        (SELECT pg_catalog.count(*) FROM proposal_occurrences)
        + (SELECT pg_catalog.count(*) FROM admission_decisions)
        + (SELECT pg_catalog.count(*) FROM canonical_graph_nodes)
        + (SELECT pg_catalog.count(*) FROM canonical_graph_edges)
        + (SELECT pg_catalog.count(*) FROM canonical_derivations)
        + (SELECT pg_catalog.count(*) FROM canonical_derivation_parents)
        + (SELECT pg_catalog.count(*) FROM canonical_contradiction_proposals)
        + (SELECT pg_catalog.count(*) FROM canonical_contradiction_admission_decisions)
        + (SELECT pg_catalog.count(*) FROM canonical_supersession_lineages)
        + (SELECT pg_catalog.count(*) FROM canonical_supersession_admission_events)
        + (SELECT pg_catalog.count(*) FROM canonical_supersession_members)
        + (SELECT pg_catalog.count(*) FROM canonical_supersession_replacement_targets)
    INTO authority_row_count;

    SELECT
        pg_catalog.count(*),
        pg_catalog.count(*) FILTER (
            WHERE chain_key = 'canonical-supersession/v1'
              AND revision = 0
              AND head_event_id IS NULL
        )
    INTO head_row_count, empty_head_count
    FROM canonical_supersession_admission_head;

    IF authority_row_count <> 0
        OR head_row_count <> 1
        OR empty_head_count <> 1 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = pg_catalog.format(
                'migration 000044 requires authority-fresh native state; authority_rows=%s supersession_head_rows=%s exact_empty_heads=%s',
                authority_row_count,
                head_row_count,
                empty_head_count
            );
    END IF;
END $$;

CREATE TABLE canonical_ordinary_admission_manifests (
    admission_decision_id TEXT
        CONSTRAINT canonical_ordinary_admission_manifests_pkey PRIMARY KEY,
    proposal_occurrence_id TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_manifests_proposal_uq UNIQUE,
    contract_version TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_manifests_contract_ck
        CHECK (contract_version = 'ordinary-admission/v1'),
    mutation_kind TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_manifests_kind_ck
        CHECK (mutation_kind IN ('source_backed_claim', 'derived_claim')),
    canonical_ref TEXT NOT NULL,
    admission_outcome TEXT NOT NULL DEFAULT 'admitted'
        CONSTRAINT canonical_ordinary_admission_manifests_outcome_ck
        CHECK (admission_outcome = 'admitted'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_ordinary_admission_manifests_decision_binding_fk
        FOREIGN KEY (
            admission_decision_id,
            proposal_occurrence_id,
            canonical_ref,
            admission_outcome
        )
        REFERENCES admission_decisions (
            admission_decision_id,
            proposal_occurrence_id,
            canonical_ref,
            outcome
        )
);

CREATE TABLE canonical_ordinary_admission_node_bindings (
    admission_decision_id TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_node_bindings_manifest_fk
        REFERENCES canonical_ordinary_admission_manifests(admission_decision_id),
    binding_role TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_node_bindings_role_ck
        CHECK (binding_role IN ('canonical_ref', 'raw_evidence')),
    binding_position INTEGER NOT NULL
        CONSTRAINT canonical_ordinary_admission_node_bindings_position_ck
        CHECK (binding_position >= 0),
    canonical_node_id TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_node_bindings_node_fk
        REFERENCES canonical_graph_nodes(canonical_node_id),
    materialization TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_node_bindings_materialization_ck
        CHECK (materialization IN ('materialized', 'reused')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_ordinary_admission_node_bindings_pkey
        PRIMARY KEY (admission_decision_id, binding_role, binding_position),
    CONSTRAINT canonical_ordinary_admission_node_bindings_decision_node_uq
        UNIQUE (admission_decision_id, canonical_node_id)
);

CREATE TABLE canonical_ordinary_admission_edge_bindings (
    admission_decision_id TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_edge_bindings_manifest_fk
        REFERENCES canonical_ordinary_admission_manifests(admission_decision_id),
    binding_role TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_edge_bindings_role_ck
        CHECK (binding_role IN ('supports_claim', 'derived_from')),
    binding_position INTEGER NOT NULL
        CONSTRAINT canonical_ordinary_admission_edge_bindings_position_ck
        CHECK (binding_position >= 0),
    canonical_edge_id TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_edge_bindings_edge_fk
        REFERENCES canonical_graph_edges(canonical_edge_id),
    materialization TEXT NOT NULL
        CONSTRAINT canonical_ordinary_admission_edge_bindings_materialization_ck
        CHECK (materialization IN ('materialized', 'reused')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_ordinary_admission_edge_bindings_pkey
        PRIMARY KEY (admission_decision_id, binding_position),
    CONSTRAINT canonical_ordinary_admission_edge_bindings_decision_edge_uq
        UNIQUE (admission_decision_id, canonical_edge_id)
);

CREATE UNIQUE INDEX canonical_ordinary_admission_node_materializer_uq
    ON canonical_ordinary_admission_node_bindings (canonical_node_id)
    WHERE materialization = 'materialized';

CREATE UNIQUE INDEX canonical_ordinary_admission_edge_materializer_uq
    ON canonical_ordinary_admission_edge_bindings (canonical_edge_id)
    WHERE materialization = 'materialized';
CREATE FUNCTION canonical_admission_forbid_mutation_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME NOT IN (
        'canonical_graph_nodes',
        'canonical_graph_edges',
        'admission_decisions',
        'canonical_derivations',
        'canonical_derivation_parents',
        'canonical_ordinary_admission_manifests',
        'canonical_ordinary_admission_node_bindings',
        'canonical_ordinary_admission_edge_bindings',
        'canonical_source_claim_review_bindings'
    )
        OR TG_WHEN <> 'BEFORE'
        OR (
            TG_LEVEL = 'ROW'
            AND TG_OP NOT IN ('UPDATE', 'DELETE')
        )
        OR (
            TG_LEVEL = 'STATEMENT'
            AND TG_OP <> 'TRUNCATE'
        )
        OR TG_LEVEL NOT IN ('ROW', 'STATEMENT') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical admission mutation guard has an invalid table or event context';
    END IF;
    RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = pg_catalog.format('%s is append-only', TG_TABLE_NAME);
END $$;

CREATE FUNCTION canonical_admission_guard_proposal_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME <> 'proposal_occurrences'
        OR TG_WHEN <> 'BEFORE'
        OR (
            TG_LEVEL = 'ROW'
            AND TG_OP NOT IN ('UPDATE', 'DELETE')
        )
        OR (
            TG_LEVEL = 'STATEMENT'
            AND TG_OP <> 'TRUNCATE'
        )
        OR TG_LEVEL NOT IN ('ROW', 'STATEMENT') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical admission proposal guard has an invalid table or event context';
    END IF;
    IF TG_OP = 'TRUNCATE' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'proposal_occurrences cannot be truncated after admission integrity is installed';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.admission_outcome <> 'pending' THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'terminal proposal occurrence is immutable';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.admission_outcome <> 'pending' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'terminal proposal occurrence is immutable';
    END IF;
    IF NEW.admission_outcome = 'pending' THEN
        IF pg_catalog.to_jsonb(NEW) IS DISTINCT FROM pg_catalog.to_jsonb(OLD) THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'pending proposal occurrence is immutable before terminal disposition';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.admission_outcome NOT IN ('admitted', 'rejected', 'audit_only')
        OR (
            pg_catalog.to_jsonb(NEW) - 'admission_outcome' - 'canonical_ref'
        ) IS DISTINCT FROM (
            pg_catalog.to_jsonb(OLD) - 'admission_outcome' - 'canonical_ref'
        )
        OR (NEW.admission_outcome = 'admitted' AND NEW.canonical_ref IS NULL)
        OR (NEW.admission_outcome <> 'admitted' AND NEW.canonical_ref IS NOT NULL) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'proposal terminal transition may change only admission_outcome and canonical_ref';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION canonical_ordinary_admission_assert_decision_v1(
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
        IF decision.raw_evidence_node_ids <> '[]'::jsonb
            OR jsonb_array_length(decision.canonical_edge_ids) = 0
            OR NOT EXISTS (
                SELECT 1
                FROM canonical_graph_nodes AS node
                JOIN canonical_derivations AS derivation
                  ON derivation.node_id = node.canonical_node_id
                WHERE node.canonical_node_id = decision.canonical_ref
                  AND node.node_kind = 'derived_claim'
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

CREATE FUNCTION canonical_ordinary_admission_assert_node_v1(
    checked_node_id TEXT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    owner_proposal TEXT;
    authority_count BIGINT;
BEGIN
    SELECT origin_proposal_occurrence_id INTO owner_proposal
    FROM canonical_graph_nodes
    WHERE canonical_node_id = checked_node_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical admission authority references a missing node';
    END IF;
    SELECT
        (SELECT count(*)
         FROM canonical_ordinary_admission_node_bindings AS binding
         JOIN canonical_ordinary_admission_manifests AS manifest
           ON manifest.admission_decision_id = binding.admission_decision_id
         JOIN admission_decisions AS decision
           ON decision.admission_decision_id = manifest.admission_decision_id
         JOIN proposal_occurrences AS proposal
           ON proposal.proposal_occurrence_id = manifest.proposal_occurrence_id
         WHERE binding.canonical_node_id = checked_node_id
           AND binding.materialization = 'materialized'
           AND manifest.proposal_occurrence_id = owner_proposal
           AND decision.proposal_occurrence_id = manifest.proposal_occurrence_id
           AND decision.outcome = 'admitted'
           AND proposal.admission_outcome = decision.outcome
           AND proposal.canonical_ref = decision.canonical_ref)
        +
        (SELECT count(*)
         FROM canonical_supersession_admission_events AS event
         JOIN admission_decisions AS decision
           ON decision.admission_decision_id = event.admission_decision_id
         JOIN proposal_occurrences AS proposal
           ON proposal.proposal_occurrence_id = event.proposal_occurrence_id
         WHERE event.proposal_occurrence_id = owner_proposal
           AND decision.proposal_occurrence_id = event.proposal_occurrence_id
           AND decision.outcome = 'admitted'
           AND proposal.admission_outcome = decision.outcome
           AND proposal.canonical_ref = decision.canonical_ref
           AND (
             decision.canonical_ref = checked_node_id
             OR decision.raw_evidence_node_ids @> jsonb_build_array(checked_node_id)
           ))
    INTO authority_count;
    IF authority_count = 1 THEN
        RETURN;
    END IF;
    RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = format(
            'canonical node first-materializer admission authority count is %s, expected 1',
            authority_count
        );
END $$;

CREATE FUNCTION canonical_ordinary_admission_assert_edge_v1(
    checked_edge_id TEXT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    owner_proposal TEXT;
    owner_contradiction_proposal TEXT;
    edge_relation TEXT;
    authority_count BIGINT;
BEGIN
    SELECT
        origin_proposal_occurrence_id,
        origin_canonical_contradiction_proposal_id,
        relation
    INTO owner_proposal, owner_contradiction_proposal, edge_relation
    FROM canonical_graph_edges
    WHERE canonical_edge_id = checked_edge_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical admission authority references a missing edge';
    END IF;
    IF edge_relation = 'supersedes' THEN
        PERFORM canonical_supersession_assert_edge(checked_edge_id);
        RETURN;
    END IF;
    SELECT
        (SELECT pg_catalog.count(*)
         FROM canonical_ordinary_admission_edge_bindings AS binding
         JOIN canonical_ordinary_admission_manifests AS manifest
           ON manifest.admission_decision_id = binding.admission_decision_id
         JOIN admission_decisions AS decision
           ON decision.admission_decision_id = manifest.admission_decision_id
         JOIN proposal_occurrences AS proposal
           ON proposal.proposal_occurrence_id = manifest.proposal_occurrence_id
         WHERE binding.canonical_edge_id = checked_edge_id
           AND binding.materialization = 'materialized'
           AND manifest.proposal_occurrence_id = owner_proposal
           AND decision.proposal_occurrence_id = manifest.proposal_occurrence_id
           AND decision.outcome = 'admitted'
           AND proposal.admission_outcome = decision.outcome
           AND proposal.canonical_ref = decision.canonical_ref)
        +
        (SELECT pg_catalog.count(*)
         FROM canonical_supersession_admission_events AS event
         JOIN admission_decisions AS decision
           ON decision.admission_decision_id = event.admission_decision_id
         JOIN proposal_occurrences AS proposal
           ON proposal.proposal_occurrence_id = event.proposal_occurrence_id
         WHERE event.proposal_occurrence_id = owner_proposal
           AND decision.proposal_occurrence_id = event.proposal_occurrence_id
           AND decision.outcome = 'admitted'
           AND proposal.admission_outcome = decision.outcome
           AND proposal.canonical_ref = decision.canonical_ref
           AND decision.canonical_edge_ids @>
                pg_catalog.jsonb_build_array(checked_edge_id))
        +
        (SELECT pg_catalog.count(*)
         FROM canonical_contradiction_admission_decisions AS decision
         JOIN canonical_contradiction_proposals AS proposal
           ON proposal.canonical_contradiction_proposal_id =
                decision.canonical_contradiction_proposal_id
         JOIN canonical_graph_edges AS edge
           ON edge.canonical_edge_id = decision.canonical_edge_id
          AND edge.origin_canonical_contradiction_proposal_id =
                decision.canonical_contradiction_proposal_id
         WHERE decision.canonical_contradiction_proposal_id =
                owner_contradiction_proposal
           AND decision.outcome = 'admitted'
           AND decision.canonical_edge_id = checked_edge_id
           AND proposal.admission_outcome = decision.outcome
           AND proposal.canonical_edge_id = decision.canonical_edge_id
           AND edge.relation = 'contradicts'
           AND edge.from_node_id = proposal.node_a_id
           AND edge.to_node_id = proposal.node_b_id)
    INTO authority_count;
    IF authority_count = 1 THEN
        RETURN;
    END IF;
    RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = pg_catalog.format(
            'canonical edge first-materializer admission authority count is %s, expected 1',
            authority_count
        );
END $$;

CREATE FUNCTION canonical_ordinary_admission_decision_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME <> 'admission_decisions' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical ordinary admission decision trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    PERFORM canonical_ordinary_admission_assert_decision_v1(
        CASE WHEN TG_OP = 'DELETE'
            THEN OLD.admission_decision_id
            ELSE NEW.admission_decision_id
        END
    );
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_ordinary_admission_manifest_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME NOT IN (
        'canonical_ordinary_admission_manifests',
        'canonical_ordinary_admission_node_bindings',
        'canonical_ordinary_admission_edge_bindings'
    ) OR TG_LEVEL <> 'ROW' OR TG_WHEN <> 'AFTER'
        OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical ordinary admission manifest trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    PERFORM canonical_ordinary_admission_assert_decision_v1(
        CASE WHEN TG_OP = 'DELETE'
            THEN OLD.admission_decision_id
            ELSE NEW.admission_decision_id
        END
    );
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_ordinary_admission_node_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME <> 'canonical_graph_nodes' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical ordinary admission node trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    PERFORM canonical_ordinary_admission_assert_node_v1(
        CASE WHEN TG_OP = 'DELETE'
            THEN OLD.canonical_node_id
            ELSE NEW.canonical_node_id
        END
    );
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_ordinary_admission_edge_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME <> 'canonical_graph_edges' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical ordinary admission edge trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    PERFORM canonical_ordinary_admission_assert_edge_v1(
        CASE WHEN TG_OP = 'DELETE'
            THEN OLD.canonical_edge_id
            ELSE NEW.canonical_edge_id
        END
    );
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_ordinary_admission_proposal_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    checked_proposal_id TEXT;
    checked_outcome TEXT;
    checked_decision_id TEXT;
BEGIN
    IF TG_TABLE_NAME <> 'proposal_occurrences' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical ordinary admission proposal trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );
    checked_proposal_id := CASE WHEN TG_OP = 'DELETE'
        THEN OLD.proposal_occurrence_id
        ELSE NEW.proposal_occurrence_id
    END;
    SELECT admission_outcome INTO checked_outcome
    FROM proposal_occurrences
    WHERE proposal_occurrence_id = checked_proposal_id;
    IF NOT FOUND OR checked_outcome = 'pending' THEN
        RETURN NULL;
    END IF;
    SELECT admission_decision_id INTO checked_decision_id
    FROM admission_decisions
    WHERE proposal_occurrence_id = checked_proposal_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'terminal proposal lacks an admission decision';
    END IF;
    PERFORM canonical_ordinary_admission_assert_decision_v1(checked_decision_id);
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_ordinary_admission_derivation_trigger_v1()
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
          AND manifest.mutation_kind = 'derived_claim'
          AND manifest.canonical_ref = checked_node_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical derivation lacks derived ordinary admission authority';
    END IF;
    PERFORM canonical_ordinary_admission_assert_decision_v1(checked_decision_id);
    RETURN NULL;
END $$;

-- Migration 42's edge trigger is SECURITY INVOKER and calls internal
-- supersession assertion helpers for every edge INSERT. Restricted ordinary
-- writers intentionally have no function EXECUTE, so rebind only that shared
-- trigger to a native-compatible definer wrapper. Keep every migration-42
-- helper and its body unchanged.
CREATE FUNCTION canonical_supersession_edge_authority_dispatch_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    protected_event_id TEXT;
BEGIN
    IF TG_TABLE_NAME <> 'canonical_graph_edges' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession edge trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );

    IF TG_OP <> 'INSERT' AND EXISTS (
        SELECT 1
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = OLD.origin_proposal_occurrence_id
    ) THEN
        SELECT event_id
        INTO protected_event_id
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = OLD.origin_proposal_occurrence_id;
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession admission edge is immutable';
    END IF;

    IF TG_OP = 'UPDATE' THEN
        SELECT event_id
        INTO protected_event_id
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = NEW.origin_proposal_occurrence_id;
        IF protected_event_id IS NOT NULL THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'canonical edge cannot be attached to a supersession event';
        END IF;
    END IF;

    IF (TG_OP = 'DELETE' AND OLD.relation = 'supersedes')
        OR (
            TG_OP = 'UPDATE'
            AND (
                OLD.relation = 'supersedes'
                OR NEW.relation = 'supersedes'
            )
        ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession edge is append-only';
    END IF;

    IF TG_OP = 'INSERT' THEN
        PERFORM canonical_supersession_assert_edge(NEW.canonical_edge_id);
        SELECT event_id
        INTO protected_event_id
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = NEW.origin_proposal_occurrence_id;
        PERFORM canonical_supersession_assert_event(protected_event_id);
    ELSIF TG_OP = 'UPDATE' THEN
        PERFORM canonical_supersession_assert_edge(OLD.canonical_edge_id);
        IF NEW.canonical_edge_id IS DISTINCT FROM OLD.canonical_edge_id
            OR NEW.from_node_id IS DISTINCT FROM OLD.from_node_id
            OR NEW.to_node_id IS DISTINCT FROM OLD.to_node_id
            OR NEW.relation IS DISTINCT FROM OLD.relation
            OR NEW.provenance IS DISTINCT FROM OLD.provenance
            OR NEW.origin_proposal_occurrence_id
                IS DISTINCT FROM OLD.origin_proposal_occurrence_id THEN
            PERFORM canonical_supersession_assert_edge(NEW.canonical_edge_id);
        END IF;
    ELSE
        PERFORM canonical_supersession_assert_edge(OLD.canonical_edge_id);
    END IF;
    RETURN NULL;
END $$;

DROP TRIGGER canonical_supersession_edges_authority_trigger
    ON canonical_graph_edges;
CREATE CONSTRAINT TRIGGER canonical_supersession_edges_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_graph_edges
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION canonical_supersession_edge_authority_dispatch_v1();

CREATE TRIGGER canonical_graph_nodes_append_only
BEFORE UPDATE OR DELETE ON canonical_graph_nodes
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_graph_nodes_forbid_truncate
BEFORE TRUNCATE ON canonical_graph_nodes
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_graph_edges_append_only
BEFORE UPDATE OR DELETE ON canonical_graph_edges
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_graph_edges_forbid_truncate
BEFORE TRUNCATE ON canonical_graph_edges
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER admission_decisions_append_only
BEFORE UPDATE OR DELETE ON admission_decisions
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER admission_decisions_forbid_truncate
BEFORE TRUNCATE ON admission_decisions
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_derivations_append_only
BEFORE UPDATE OR DELETE ON canonical_derivations
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_derivations_forbid_truncate
BEFORE TRUNCATE ON canonical_derivations
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_derivation_parents_append_only
BEFORE UPDATE OR DELETE ON canonical_derivation_parents
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_derivation_parents_forbid_truncate
BEFORE TRUNCATE ON canonical_derivation_parents
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_ordinary_admission_manifests_append_only
BEFORE UPDATE OR DELETE ON canonical_ordinary_admission_manifests
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_ordinary_admission_manifests_forbid_truncate
BEFORE TRUNCATE ON canonical_ordinary_admission_manifests
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_ordinary_admission_node_bindings_append_only
BEFORE UPDATE OR DELETE ON canonical_ordinary_admission_node_bindings
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_ordinary_admission_node_bindings_forbid_truncate
BEFORE TRUNCATE ON canonical_ordinary_admission_node_bindings
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_ordinary_admission_edge_bindings_append_only
BEFORE UPDATE OR DELETE ON canonical_ordinary_admission_edge_bindings
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_ordinary_admission_edge_bindings_forbid_truncate
BEFORE TRUNCATE ON canonical_ordinary_admission_edge_bindings
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER proposal_occurrences_terminal_guard
BEFORE UPDATE OR DELETE ON proposal_occurrences
FOR EACH ROW EXECUTE FUNCTION canonical_admission_guard_proposal_v1();
CREATE TRIGGER proposal_occurrences_forbid_truncate
BEFORE TRUNCATE ON proposal_occurrences
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_guard_proposal_v1();

CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_decisions_authority
AFTER INSERT OR UPDATE OR DELETE ON admission_decisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_decision_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_manifests_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_ordinary_admission_manifests
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_manifest_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_node_bindings_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_ordinary_admission_node_bindings
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_manifest_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_edge_bindings_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_ordinary_admission_edge_bindings
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_manifest_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_nodes_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_graph_nodes
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_node_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_edges_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_graph_edges
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_edge_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_proposals_authority
AFTER INSERT OR UPDATE OR DELETE ON proposal_occurrences
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_proposal_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_derivations_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_derivations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_derivation_trigger_v1();
CREATE CONSTRAINT TRIGGER canonical_ordinary_admission_derivation_parents_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_derivation_parents
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_ordinary_admission_derivation_trigger_v1();

REVOKE EXECUTE ON FUNCTION canonical_admission_forbid_mutation_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_admission_guard_proposal_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_assert_decision_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_assert_node_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_assert_edge_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_decision_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_manifest_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_node_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_edge_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_proposal_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_ordinary_admission_derivation_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_supersession_edge_authority_dispatch_v1() FROM PUBLIC;
