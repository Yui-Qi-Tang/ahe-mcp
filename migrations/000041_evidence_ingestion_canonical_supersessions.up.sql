DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM canonical_graph_edges
        WHERE relation = 'supersedes'
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = 'check_violation',
            MESSAGE = 'migration 41 requires zero existing supersedes edges; classify and migrate legacy rows before retrying';
    END IF;
END
$$;

CREATE TABLE canonical_supersession_proposals (
    canonical_supersession_proposal_id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    request_payload_hash TEXT NOT NULL,
    proposal_fingerprint TEXT NOT NULL,
    from_node_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    to_node_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    relation TEXT NOT NULL CHECK (relation = 'supersedes'),
    proposal_sentence TEXT NOT NULL,
    rationale TEXT NOT NULL,
    version_difference TEXT NOT NULL,
    limitations JSONB NOT NULL DEFAULT '[]'::jsonb,
    producer_name TEXT NOT NULL,
    producer_version TEXT NOT NULL,
    producer_session_ref TEXT,
    admission_outcome TEXT NOT NULL CHECK (
        admission_outcome IN ('pending', 'rejected', 'audit_only', 'admitted')
    ),
    canonical_edge_id TEXT REFERENCES canonical_graph_edges(canonical_edge_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    CHECK (canonical_supersession_proposal_id LIKE 'supersession-proposal:%'),
    CHECK (octet_length(request_id) BETWEEN 1 AND 200),
    CHECK (from_node_id <> to_node_id),
    CHECK (octet_length(proposal_sentence) BETWEEN 1 AND 4000),
    CHECK (octet_length(rationale) BETWEEN 1 AND 4000),
    CHECK (octet_length(version_difference) BETWEEN 1 AND 4000),
    CHECK (
        jsonb_typeof(limitations) = 'array'
        AND jsonb_array_length(limitations) <= 32
        AND octet_length(limitations::text) <= 65536
        AND NOT limitations @? '$[*] ? (@.type() != "string")'
    ),
    CHECK (octet_length(producer_name) BETWEEN 1 AND 200),
    CHECK (octet_length(producer_version) BETWEEN 1 AND 200),
    CHECK (
        producer_session_ref IS NULL
        OR octet_length(producer_session_ref) BETWEEN 1 AND 500
    ),
    CHECK (
        (admission_outcome = 'pending' AND canonical_edge_id IS NULL AND decided_at IS NULL)
        OR
        (admission_outcome = 'admitted' AND canonical_edge_id IS NOT NULL AND decided_at IS NOT NULL)
        OR
        (admission_outcome IN ('rejected', 'audit_only') AND canonical_edge_id IS NULL AND decided_at IS NOT NULL)
    ),
    UNIQUE (from_node_id, to_node_id, relation),
    CONSTRAINT canonical_supersession_proposals_edge_shape_key UNIQUE (
        canonical_supersession_proposal_id,
        from_node_id,
        to_node_id,
        relation
    ),
    CONSTRAINT canonical_supersession_proposals_edge_link_key UNIQUE (
        canonical_supersession_proposal_id,
        canonical_edge_id
    )
);

ALTER TABLE canonical_graph_edges
    DROP CONSTRAINT canonical_graph_edges_exact_origin_ck,
    ADD COLUMN origin_canonical_supersession_proposal_id TEXT
        REFERENCES canonical_supersession_proposals(canonical_supersession_proposal_id),
    ADD CONSTRAINT canonical_graph_edges_exact_origin_ck CHECK (
        num_nonnulls(
            origin_proposal_occurrence_id,
            origin_canonical_contradiction_proposal_id,
            origin_canonical_supersession_proposal_id
        ) = 1
    ),
    ADD CONSTRAINT canonical_graph_edges_supersession_origin_relation_ck CHECK (
        (relation = 'supersedes') =
        (origin_canonical_supersession_proposal_id IS NOT NULL)
    ),
    ADD CONSTRAINT canonical_graph_edges_supersession_shape_fk FOREIGN KEY (
        origin_canonical_supersession_proposal_id,
        from_node_id,
        to_node_id,
        relation
    ) REFERENCES canonical_supersession_proposals (
        canonical_supersession_proposal_id,
        from_node_id,
        to_node_id,
        relation
    ),
    ADD CONSTRAINT canonical_graph_edges_supersession_link_key UNIQUE (
        origin_canonical_supersession_proposal_id,
        canonical_edge_id
    );

CREATE TABLE canonical_supersession_admission_decisions (
    canonical_supersession_admission_decision_id TEXT PRIMARY KEY,
    canonical_supersession_proposal_id TEXT NOT NULL UNIQUE
        REFERENCES canonical_supersession_proposals(canonical_supersession_proposal_id),
    outcome TEXT NOT NULL CHECK (outcome IN ('rejected', 'audit_only', 'admitted')),
    canonical_edge_id TEXT REFERENCES canonical_graph_edges(canonical_edge_id),
    decision_by TEXT NOT NULL,
    decision_reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (octet_length(decision_by) BETWEEN 1 AND 200),
    CHECK (octet_length(decision_reason) BETWEEN 1 AND 2000),
    CHECK (
        (outcome = 'admitted' AND canonical_edge_id IS NOT NULL)
        OR
        (outcome <> 'admitted' AND canonical_edge_id IS NULL)
    ),
    CONSTRAINT canonical_supersession_decisions_edge_link_key UNIQUE (
        canonical_supersession_proposal_id,
        canonical_edge_id
    )
);

ALTER TABLE canonical_supersession_proposals
    ADD CONSTRAINT canonical_supersession_proposals_edge_link_fk FOREIGN KEY (
        canonical_supersession_proposal_id,
        canonical_edge_id
    ) REFERENCES canonical_graph_edges (
        origin_canonical_supersession_proposal_id,
        canonical_edge_id
    ) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE canonical_graph_edges
    ADD CONSTRAINT canonical_graph_edges_supersession_decision_fk FOREIGN KEY (
        origin_canonical_supersession_proposal_id,
        canonical_edge_id
    ) REFERENCES canonical_supersession_admission_decisions (
        canonical_supersession_proposal_id,
        canonical_edge_id
    ) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE canonical_supersession_admission_decisions
    ADD CONSTRAINT canonical_supersession_decisions_proposal_link_fk FOREIGN KEY (
        canonical_supersession_proposal_id,
        canonical_edge_id
    ) REFERENCES canonical_supersession_proposals (
        canonical_supersession_proposal_id,
        canonical_edge_id
    ) DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX canonical_supersession_proposals_outcome_idx
    ON canonical_supersession_proposals (admission_outcome, created_at);

CREATE INDEX canonical_graph_edges_supersession_origin_idx
    ON canonical_graph_edges (origin_canonical_supersession_proposal_id)
    WHERE origin_canonical_supersession_proposal_id IS NOT NULL;
