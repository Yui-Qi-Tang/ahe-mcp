-- Migration 41's directed-pair workflow cannot establish the lineage-wide,
-- append-only authority needed by certified currentness. Do not infer a v2
-- history from pair-v1 rows: hold a write fence, count all three legacy state
-- surfaces, and fail closed unless the retirement is lossless.
LOCK TABLE
    canonical_graph_edges,
    canonical_supersession_proposals,
    canonical_supersession_admission_decisions
IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    legacy_edge_count BIGINT;
    legacy_proposal_count BIGINT;
    legacy_decision_count BIGINT;
BEGIN
    SELECT count(*)
    INTO legacy_edge_count
    FROM canonical_graph_edges
    WHERE relation = 'supersedes';

    SELECT count(*)
    INTO legacy_proposal_count
    FROM canonical_supersession_proposals;

    SELECT count(*)
    INTO legacy_decision_count
    FROM canonical_supersession_admission_decisions;

    IF legacy_edge_count <> 0
        OR legacy_proposal_count <> 0
        OR legacy_decision_count <> 0 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = format(
                'migration 000042 requires empty pair-v1 supersession state; supersedes_edges=%s proposals=%s decisions=%s',
                legacy_edge_count,
                legacy_proposal_count,
                legacy_decision_count
            );
    END IF;
END $$;

-- Removing this column with CASCADE retires every migration-41 constraint and
-- index that depends on the pair-v1 origin. The contradiction origin remains.
ALTER TABLE canonical_graph_edges
    DROP COLUMN origin_canonical_supersession_proposal_id CASCADE;

DROP TABLE canonical_supersession_admission_decisions;
DROP TABLE canonical_supersession_proposals;

ALTER TABLE canonical_graph_edges
    ADD CONSTRAINT canonical_graph_edges_exact_origin_ck CHECK (
        num_nonnulls(
            origin_proposal_occurrence_id,
            origin_canonical_contradiction_proposal_id
        ) = 1
    );

ALTER TABLE admission_decisions
    ADD CONSTRAINT admission_decisions_supersession_binding_uq
    UNIQUE (
        admission_decision_id,
        proposal_occurrence_id,
        canonical_ref,
        outcome
    );

CREATE TABLE canonical_supersession_lineages (
    lineage_key TEXT PRIMARY KEY
        CHECK (lineage_key ~ '^lineage:v1:sha256:[0-9a-f]{64}$'),
    contract_version TEXT NOT NULL
        CHECK (contract_version = 'supersession-lineage/v1'),
    source_system TEXT NOT NULL
        CHECK (
            source_system = btrim(source_system)
            AND source_system ~ '^[a-z0-9._-]+$'
            AND octet_length(source_system) BETWEEN 1 AND 64
        ),
    source_namespace TEXT NOT NULL
        CHECK (
            source_namespace = btrim(source_namespace)
            AND octet_length(source_namespace) BETWEEN 1 AND 200
        ),
    object_type TEXT NOT NULL
        CHECK (
            object_type = btrim(object_type)
            AND octet_length(object_type) BETWEEN 1 AND 100
        ),
    object_id TEXT NOT NULL
        CHECK (
            object_id = btrim(object_id)
            AND octet_length(object_id) BETWEEN 1 AND 500
        ),
    slot_kind TEXT NOT NULL
        CHECK (
            slot_kind = btrim(slot_kind)
            AND octet_length(slot_kind) BETWEEN 1 AND 100
        ),
    slot_id TEXT NOT NULL
        CHECK (
            slot_id = btrim(slot_id)
            AND octet_length(slot_id) BETWEEN 1 AND 500
        ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_supersession_lineages_basis_uq UNIQUE (
        source_system,
        source_namespace,
        object_type,
        object_id,
        slot_kind,
        slot_id
    )
);

CREATE TABLE canonical_supersession_admission_events (
    event_id TEXT PRIMARY KEY
        CHECK (event_id ~ '^admission-event:v2:sha256:[0-9a-f]{64}$'),
    contract_version TEXT NOT NULL
        CHECK (contract_version = 'supersession-admission-event/v2'),
    revision BIGINT NOT NULL CHECK (revision > 0),
    previous_revision BIGINT NOT NULL CHECK (previous_revision = revision - 1),
    previous_event_id TEXT,
    event_kind TEXT NOT NULL CHECK (event_kind = 'atomic_replacement'),
    lineage_key TEXT NOT NULL REFERENCES canonical_supersession_lineages(lineage_key),
    replacement_node_id TEXT NOT NULL
        REFERENCES canonical_graph_nodes(canonical_node_id),
    proposal_occurrence_id TEXT NOT NULL
        REFERENCES proposal_occurrences(proposal_occurrence_id),
    admission_decision_id TEXT NOT NULL,
    admission_outcome TEXT NOT NULL DEFAULT 'admitted'
        CHECK (admission_outcome = 'admitted'),
    request_payload_hash TEXT NOT NULL
        CHECK (request_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    decision_payload_hash TEXT NOT NULL
        CHECK (decision_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    event_payload JSONB NOT NULL
        CHECK (jsonb_typeof(event_payload) = 'object'),
    event_payload_hash TEXT NOT NULL
        CHECK (event_payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_supersession_events_revision_uq
        UNIQUE (revision),
    CONSTRAINT canonical_supersession_events_previous_event_uq
        UNIQUE (previous_event_id),
    CONSTRAINT canonical_supersession_events_replacement_node_uq
        UNIQUE (replacement_node_id),
    CONSTRAINT canonical_supersession_events_proposal_occurrence_uq
        UNIQUE (proposal_occurrence_id),
    CONSTRAINT canonical_supersession_events_admission_decision_uq
        UNIQUE (admission_decision_id),
    CONSTRAINT canonical_supersession_events_event_revision_uq
        UNIQUE (event_id, revision),
    CONSTRAINT canonical_supersession_events_lineage_replacement_uq
        UNIQUE (event_id, lineage_key, replacement_node_id),
    CONSTRAINT canonical_supersession_events_event_id_hash_ck CHECK (
        event_id = 'admission-event:v2:' || event_payload_hash
    ),
    CONSTRAINT canonical_supersession_events_previous_ck CHECK (
        (revision = 1 AND previous_revision = 0 AND previous_event_id IS NULL)
        OR
        (revision > 1 AND previous_event_id IS NOT NULL)
    ),
    CONSTRAINT canonical_supersession_events_previous_fk
    FOREIGN KEY (previous_event_id, previous_revision)
        REFERENCES canonical_supersession_admission_events(event_id, revision),
    CONSTRAINT canonical_supersession_events_admission_fk
    FOREIGN KEY (
        admission_decision_id,
        proposal_occurrence_id,
        replacement_node_id,
        admission_outcome
    )
        REFERENCES admission_decisions (
            admission_decision_id,
            proposal_occurrence_id,
            canonical_ref,
            outcome
        )
);

CREATE TABLE canonical_supersession_admission_head (
    chain_key TEXT PRIMARY KEY
        CHECK (chain_key = 'canonical-supersession/v1'),
    revision BIGINT NOT NULL CHECK (revision >= 0),
    head_event_id TEXT
        CONSTRAINT canonical_supersession_head_event_uq UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_supersession_head_coordinate_ck CHECK (
        (revision = 0 AND head_event_id IS NULL)
        OR
        (revision > 0 AND head_event_id IS NOT NULL)
    ),
    CONSTRAINT canonical_supersession_head_event_fk
    FOREIGN KEY (head_event_id, revision)
        REFERENCES canonical_supersession_admission_events(event_id, revision)
        DEFERRABLE INITIALLY DEFERRED
);

INSERT INTO canonical_supersession_admission_head (
    chain_key,
    revision,
    head_event_id
)
VALUES ('canonical-supersession/v1', 0, NULL);

CREATE TABLE canonical_supersession_members (
    canonical_node_id TEXT PRIMARY KEY
        CONSTRAINT canonical_supersession_members_node_fk
        REFERENCES canonical_graph_nodes(canonical_node_id),
    lineage_key TEXT NOT NULL
        CONSTRAINT canonical_supersession_members_lineage_fk
        REFERENCES canonical_supersession_lineages(lineage_key),
    first_admission_event_id TEXT NOT NULL
        CONSTRAINT canonical_supersession_members_event_fk
        REFERENCES canonical_supersession_admission_events(event_id),
    was_bootstrapped BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_supersession_members_lineage_node_uq
        UNIQUE (lineage_key, canonical_node_id)
);

ALTER TABLE canonical_supersession_admission_events
    ADD CONSTRAINT canonical_supersession_event_replacement_member_fk
    FOREIGN KEY (lineage_key, replacement_node_id)
    REFERENCES canonical_supersession_members(lineage_key, canonical_node_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE canonical_supersession_replacement_targets (
    event_id TEXT NOT NULL,
    lineage_key TEXT NOT NULL,
    replacement_node_id TEXT NOT NULL,
    target_node_id TEXT NOT NULL,
    canonical_edge_id TEXT NOT NULL
        CONSTRAINT canonical_supersession_targets_edge_uq UNIQUE
        CONSTRAINT canonical_supersession_targets_edge_fk
        REFERENCES canonical_graph_edges(canonical_edge_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, target_node_id),
    CONSTRAINT canonical_supersession_targets_distinct_nodes_ck
        CHECK (replacement_node_id <> target_node_id),
    CONSTRAINT canonical_supersession_targets_event_fk
    FOREIGN KEY (event_id, lineage_key, replacement_node_id)
        REFERENCES canonical_supersession_admission_events(
            event_id,
            lineage_key,
            replacement_node_id
        ),
    CONSTRAINT canonical_supersession_targets_member_fk
    FOREIGN KEY (lineage_key, target_node_id)
        REFERENCES canonical_supersession_members(lineage_key, canonical_node_id)
);

CREATE INDEX canonical_supersession_events_lineage_revision_idx
    ON canonical_supersession_admission_events (lineage_key, revision DESC);

CREATE INDEX canonical_supersession_targets_target_idx
    ON canonical_supersession_replacement_targets (target_node_id, event_id);

-- Match evidencegraph.StableCanonicalID without requiring pgcrypto. PostgreSQL
-- supplies sha256(bytea); each trimmed part is separated by the same NUL byte
-- used by the Go implementation.
CREATE FUNCTION canonical_supersession_stable_id(
    id_prefix TEXT,
    id_parts TEXT[]
)
RETURNS TEXT
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    part TEXT;
    preimage BYTEA := ''::bytea;
BEGIN
    FOREACH part IN ARRAY id_parts LOOP
        preimage := preimage
            || convert_to(btrim(part), 'UTF8')
            || decode('00', 'hex');
    END LOOP;
    RETURN btrim(id_prefix)
        || ':'
        || left(encode(sha256(preimage), 'hex'), 16);
END $$;

-- Supersedes is not a generic graph-write capability. The non-deferrable edge
-- foreign key requires the writer to insert the edge before its target row;
-- these deferred triggers require the final committed state to agree in both
-- directions and inspect only the changed edge or target key.
CREATE FUNCTION canonical_supersession_assert_edge(checked_edge_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF checked_edge_id IS NULL THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM canonical_graph_edges AS edge
        LEFT JOIN canonical_supersession_replacement_targets AS target
            ON target.canonical_edge_id = edge.canonical_edge_id
        LEFT JOIN canonical_supersession_admission_events AS event
            ON event.event_id = target.event_id
        LEFT JOIN canonical_graph_nodes AS replacement
            ON replacement.canonical_node_id = target.replacement_node_id
        WHERE edge.canonical_edge_id = checked_edge_id
          AND (
              (
                  edge.relation = 'supersedes'
                  AND (
                      target.canonical_edge_id IS NULL
                      OR target.replacement_node_id <> edge.from_node_id
                      OR target.target_node_id <> edge.to_node_id
                      OR event.event_id IS NULL
                      OR replacement.canonical_node_id IS NULL
                      OR edge.canonical_edge_id <>
                            canonical_supersession_stable_id(
                                'canon-edge',
                                ARRAY[
                                    target.replacement_node_id,
                                    target.target_node_id,
                                    'supersedes'
                                ]
                            )
                      OR event.proposal_occurrence_id <>
                            edge.origin_proposal_occurrence_id
                      OR edge.provenance ->> 'id'
                            IS DISTINCT FROM
                            canonical_supersession_stable_id(
                                'provenance',
                                ARRAY[edge.canonical_edge_id]
                            )
                      OR edge.provenance -> 'origin_refs'
                            IS DISTINCT FROM
                            (
                                replacement.provenance -> 'origin_refs'
                                || jsonb_build_array(target.target_node_id)
                            )
                      OR edge.provenance ->> 'origin_group_id'
                            IS DISTINCT FROM
                            replacement.provenance ->> 'origin_group_id'
                      OR edge.provenance ->> 'producer'
                            IS DISTINCT FROM
                            replacement.provenance ->> 'producer'
                      OR edge.provenance ->> 'trace_ref'
                            IS DISTINCT FROM event.event_id
                      OR edge.provenance ->> 'review_ref'
                            IS DISTINCT FROM event.admission_decision_id
                      OR edge.provenance ->> 'method'
                            IS DISTINCT FROM 'supersession_admission_edge'
                      OR edge.provenance ->> 'method_version'
                            IS DISTINCT FROM 'v1'
                  )
              )
              OR (
                  edge.relation <> 'supersedes'
                  AND target.canonical_edge_id IS NOT NULL
              )
          )
    ) OR (
        NOT EXISTS (
            SELECT 1
            FROM canonical_graph_edges AS edge
            WHERE edge.canonical_edge_id = checked_edge_id
        )
        AND EXISTS (
            SELECT 1
            FROM canonical_supersession_replacement_targets AS target
            WHERE target.canonical_edge_id = checked_edge_id
        )
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical edge lacks exact supersession replacement authority';
    END IF;
END $$;

CREATE FUNCTION canonical_supersession_assert_target(
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

CREATE FUNCTION canonical_supersession_assert_event(checked_event_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    event_record RECORD;
    persisted_targets JSONB;
    persisted_proposal_edges JSONB;
    decision_edges JSONB;
    persisted_bootstrapped JSONB;
    persisted_members JSONB;
    expected_members JSONB;
    target_record RECORD;
BEGIN
    IF checked_event_id IS NULL THEN
        RETURN;
    END IF;

    SELECT
        event.*,
        decision.canonical_edge_ids AS decision_canonical_edge_ids,
        lineage.source_system AS lineage_source_system,
        lineage.source_namespace AS lineage_source_namespace,
        lineage.object_type AS lineage_object_type,
        lineage.object_id AS lineage_object_id,
        lineage.slot_kind AS lineage_slot_kind,
        lineage.slot_id AS lineage_slot_id
    INTO event_record
    FROM canonical_supersession_admission_events AS event
    LEFT JOIN canonical_supersession_lineages AS lineage
        ON lineage.lineage_key = event.lineage_key
    LEFT JOIN admission_decisions AS decision
        ON decision.admission_decision_id = event.admission_decision_id
    WHERE event.event_id = checked_event_id;

    IF NOT FOUND THEN
        IF EXISTS (
            SELECT 1
            FROM canonical_supersession_replacement_targets
            WHERE event_id = checked_event_id
        ) OR EXISTS (
            SELECT 1
            FROM canonical_supersession_members
            WHERE first_admission_event_id = checked_event_id
        ) THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'canonical supersession event authority is missing';
        END IF;
        RETURN;
    END IF;

    IF jsonb_typeof(event_record.event_payload -> 'target_node_ids')
            IS DISTINCT FROM 'array'
        OR jsonb_typeof(
            event_record.event_payload -> 'bootstrapped_target_node_ids'
        ) IS DISTINCT FROM 'array'
        OR jsonb_typeof(event_record.event_payload -> 'lineage_basis')
            IS DISTINCT FROM 'object'
        OR jsonb_typeof(event_record.decision_canonical_edge_ids)
            IS DISTINCT FROM 'array'
        OR jsonb_array_length(
            event_record.event_payload -> 'target_node_ids'
        ) NOT BETWEEN 1 AND 64
        OR NOT (
            (event_record.event_payload -> 'target_node_ids')
            @> (
                event_record.event_payload
                    -> 'bootstrapped_target_node_ids'
            )
        ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession event payload shape is invalid';
    END IF;

    SELECT COALESCE(
        jsonb_agg(target_node_id ORDER BY target_node_id),
        '[]'::jsonb
    )
    INTO persisted_targets
    FROM canonical_supersession_replacement_targets
    WHERE event_id = checked_event_id;

    SELECT COALESCE(
        jsonb_agg(canonical_edge_id ORDER BY canonical_edge_id),
        '[]'::jsonb
    )
    INTO persisted_proposal_edges
    FROM canonical_graph_edges
    WHERE origin_proposal_occurrence_id = event_record.proposal_occurrence_id;

    SELECT COALESCE(jsonb_agg(edge_id ORDER BY edge_id), '[]'::jsonb)
    INTO decision_edges
    FROM jsonb_array_elements_text(
        event_record.decision_canonical_edge_ids
    ) AS decision_edge(edge_id);

    SELECT COALESCE(
        jsonb_agg(canonical_node_id ORDER BY canonical_node_id)
            FILTER (WHERE was_bootstrapped),
        '[]'::jsonb
    ), COALESCE(
        jsonb_agg(canonical_node_id ORDER BY canonical_node_id),
        '[]'::jsonb
    )
    INTO persisted_bootstrapped, persisted_members
    FROM canonical_supersession_members
    WHERE first_admission_event_id = checked_event_id;

    SELECT COALESCE(jsonb_agg(node_id ORDER BY node_id), '[]'::jsonb)
    INTO expected_members
    FROM (
        SELECT event_record.replacement_node_id AS node_id
        UNION ALL
        SELECT jsonb_array_elements_text(
            event_record.event_payload -> 'bootstrapped_target_node_ids'
        ) AS node_id
    ) AS expected;

    IF event_record.event_id <>
            'admission-event:v2:' || event_record.event_payload_hash
        OR (
            event_record.event_payload
                - 'target_node_ids'
                - 'bootstrapped_target_node_ids'
                - 'lineage_basis'
        ) IS DISTINCT FROM jsonb_strip_nulls(jsonb_build_object(
            'contract_version', event_record.contract_version,
            'kind', event_record.event_kind,
            'proposal_occurrence_id', event_record.proposal_occurrence_id,
            'replacement_node_id', event_record.replacement_node_id,
            'lineage_key', event_record.lineage_key,
            'previous_revision', event_record.previous_revision,
            'previous_event_id', event_record.previous_event_id,
            'revision', event_record.revision,
            'admission_decision_id', event_record.admission_decision_id,
            'request_payload_hash', event_record.request_payload_hash,
            'decision_payload_hash', event_record.decision_payload_hash
        ))
        OR event_record.event_payload ->> 'contract_version'
            IS DISTINCT FROM event_record.contract_version
        OR event_record.event_payload ->> 'kind'
            IS DISTINCT FROM event_record.event_kind
        OR event_record.event_payload ->> 'proposal_occurrence_id'
            IS DISTINCT FROM event_record.proposal_occurrence_id
        OR event_record.event_payload ->> 'replacement_node_id'
            IS DISTINCT FROM event_record.replacement_node_id
        OR event_record.event_payload ->> 'lineage_key'
            IS DISTINCT FROM event_record.lineage_key
        OR (event_record.event_payload ->> 'previous_revision')::BIGINT
            IS DISTINCT FROM event_record.previous_revision
        OR event_record.event_payload ->> 'previous_event_id'
            IS DISTINCT FROM event_record.previous_event_id
        OR (event_record.event_payload ->> 'revision')::BIGINT
            IS DISTINCT FROM event_record.revision
        OR event_record.event_payload ->> 'admission_decision_id'
            IS DISTINCT FROM event_record.admission_decision_id
        OR event_record.event_payload ->> 'request_payload_hash'
            IS DISTINCT FROM event_record.request_payload_hash
        OR event_record.event_payload ->> 'decision_payload_hash'
            IS DISTINCT FROM event_record.decision_payload_hash
        OR (event_record.event_payload -> 'lineage_basis')
            IS DISTINCT FROM jsonb_build_object(
                'source_system', event_record.lineage_source_system,
                'source_namespace', event_record.lineage_source_namespace,
                'object_type', event_record.lineage_object_type,
                'object_id', event_record.lineage_object_id,
                'slot_kind', event_record.lineage_slot_kind,
                'slot_id', event_record.lineage_slot_id
            )
        OR persisted_targets
            IS DISTINCT FROM (
                event_record.event_payload -> 'target_node_ids'
            )
        OR persisted_proposal_edges IS DISTINCT FROM decision_edges
        OR persisted_bootstrapped IS DISTINCT FROM
            (
                event_record.event_payload
                    -> 'bootstrapped_target_node_ids'
            )
        OR persisted_members IS DISTINCT FROM expected_members
        OR NOT EXISTS (
            SELECT 1
            FROM canonical_supersession_members
            WHERE canonical_node_id = event_record.replacement_node_id
              AND lineage_key = event_record.lineage_key
              AND first_admission_event_id = checked_event_id
              AND NOT was_bootstrapped
        )
        OR EXISTS (
            SELECT 1
            FROM canonical_supersession_members
            WHERE first_admission_event_id = checked_event_id
              AND (
                  (
                      canonical_node_id = event_record.replacement_node_id
                      AND was_bootstrapped
                  )
                  OR (
                      canonical_node_id <> event_record.replacement_node_id
                      AND NOT was_bootstrapped
                  )
              )
        )
        OR NOT EXISTS (
            SELECT 1
            FROM canonical_supersession_admission_head
            WHERE chain_key = 'canonical-supersession/v1'
              AND revision >= event_record.revision
        ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession event authority is inconsistent';
    END IF;

    FOR target_record IN
        SELECT target_node_id, canonical_edge_id
        FROM canonical_supersession_replacement_targets
        WHERE event_id = checked_event_id
    LOOP
        PERFORM canonical_supersession_assert_target(
            checked_event_id,
            target_record.target_node_id,
            target_record.canonical_edge_id
        );
    END LOOP;

    IF EXISTS (
        WITH RECURSIVE reachable(node_id) AS (
            SELECT target_node_id
            FROM canonical_supersession_replacement_targets
            WHERE event_id = checked_event_id
            UNION
            SELECT edge.to_node_id
            FROM reachable
            JOIN canonical_graph_edges AS edge
                ON edge.from_node_id = reachable.node_id
               AND edge.relation = 'supersedes'
        )
        SELECT 1
        FROM reachable
        WHERE node_id = event_record.replacement_node_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession event would create a cycle';
    END IF;
END $$;

CREATE FUNCTION canonical_supersession_lineage_authority_trigger()
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

CREATE FUNCTION canonical_supersession_event_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession admission event is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.event_id);
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_supersession_member_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member is append-only';
    END IF;
    PERFORM canonical_supersession_assert_event(NEW.first_admission_event_id);
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_supersession_head_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'UPDATE'
        OR NEW.chain_key IS DISTINCT FROM OLD.chain_key
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
        OR NEW.revision <> OLD.revision + 1
        OR NEW.head_event_id IS NULL
        OR NOT EXISTS (
            SELECT 1
            FROM canonical_supersession_admission_events AS event
            WHERE event.event_id = NEW.head_event_id
              AND event.revision = NEW.revision
              AND event.previous_revision = OLD.revision
              AND event.previous_event_id IS NOT DISTINCT FROM OLD.head_event_id
        ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession head must advance exactly one event';
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_supersession_node_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND EXISTS (
        SELECT 1
        FROM canonical_supersession_members
        WHERE canonical_node_id = OLD.canonical_node_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member node is immutable';
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_supersession_decision_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND EXISTS (
        SELECT 1
        FROM canonical_supersession_admission_events
        WHERE admission_decision_id = OLD.admission_decision_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession admission decision is immutable';
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION canonical_supersession_proposal_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    protected_event_id TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
        RETURN NULL;
    END IF;

    SELECT event.event_id
    INTO protected_event_id
    FROM canonical_supersession_admission_events AS event
    WHERE event.proposal_occurrence_id = OLD.proposal_occurrence_id
    UNION
    SELECT member.first_admission_event_id
    FROM canonical_supersession_members AS member
    JOIN canonical_graph_nodes AS node
        ON node.canonical_node_id = member.canonical_node_id
    WHERE node.origin_proposal_occurrence_id = OLD.proposal_occurrence_id
    LIMIT 1;

    IF protected_event_id IS NULL THEN
        RETURN NULL;
    END IF;

    IF TG_OP = 'UPDATE'
        AND OLD.admission_outcome = 'pending'
        AND OLD.canonical_ref IS NULL
        AND NEW.admission_outcome = 'admitted'
        AND NEW.canonical_ref IS NOT NULL
        AND (
            to_jsonb(NEW) - 'admission_outcome' - 'canonical_ref'
        ) = (
            to_jsonb(OLD) - 'admission_outcome' - 'canonical_ref'
        )
        AND EXISTS (
            SELECT 1
            FROM canonical_supersession_admission_events
            WHERE proposal_occurrence_id = NEW.proposal_occurrence_id
              AND replacement_node_id = NEW.canonical_ref
        ) THEN
        PERFORM canonical_supersession_assert_event(protected_event_id);
        RETURN NULL;
    END IF;

    RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = 'canonical supersession proposal binding is immutable';
END $$;

CREATE FUNCTION canonical_supersession_edge_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    protected_event_id TEXT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        SELECT event_id
        INTO protected_event_id
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = OLD.origin_proposal_occurrence_id;
        IF protected_event_id IS NOT NULL THEN
            RAISE EXCEPTION USING
                ERRCODE = '23514',
                MESSAGE = 'canonical supersession admission edge is immutable';
        END IF;
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

CREATE FUNCTION canonical_supersession_target_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession replacement target is append-only';
    END IF;

    PERFORM canonical_supersession_assert_target(
        NEW.event_id,
        NEW.target_node_id,
        NEW.canonical_edge_id
    );
    PERFORM canonical_supersession_assert_event(NEW.event_id);
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER canonical_supersession_lineages_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_supersession_lineages
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_lineage_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_events_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_supersession_admission_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_event_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_members_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_supersession_members
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_member_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_head_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_supersession_admission_head
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_head_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_nodes_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_graph_nodes
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_node_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_decisions_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON admission_decisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_decision_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_proposals_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON proposal_occurrences
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_proposal_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_edges_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_graph_edges
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_edge_authority_trigger();

CREATE CONSTRAINT TRIGGER canonical_supersession_targets_authority_trigger
AFTER INSERT OR UPDATE OR DELETE ON canonical_supersession_replacement_targets
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_supersession_target_authority_trigger();
