CREATE TABLE canonical_contradiction_proposals (
    canonical_contradiction_proposal_id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    request_payload_hash TEXT NOT NULL,
    proposal_fingerprint TEXT NOT NULL,
    node_a_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    node_b_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    relation TEXT NOT NULL CHECK (relation = 'contradicts'),
    rationale TEXT NOT NULL,
    producer_name TEXT NOT NULL,
    producer_version TEXT NOT NULL,
    producer_session_ref TEXT,
    admission_outcome TEXT NOT NULL CHECK (
        admission_outcome IN ('pending', 'rejected', 'audit_only', 'admitted')
    ),
    canonical_edge_id TEXT REFERENCES canonical_graph_edges(canonical_edge_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    CHECK (canonical_contradiction_proposal_id LIKE 'contradiction-proposal:%'),
    CHECK (octet_length(request_id) BETWEEN 1 AND 200),
    CHECK (octet_length(rationale) BETWEEN 1 AND 4000),
    CHECK (octet_length(producer_name) BETWEEN 1 AND 200),
    CHECK (octet_length(producer_version) BETWEEN 1 AND 200),
    CHECK (
        producer_session_ref IS NULL
        OR octet_length(producer_session_ref) BETWEEN 1 AND 500
    ),
    CHECK (node_a_id < node_b_id),
    CHECK (
        (admission_outcome = 'pending' AND canonical_edge_id IS NULL AND decided_at IS NULL)
        OR
        (admission_outcome = 'admitted' AND canonical_edge_id IS NOT NULL AND decided_at IS NOT NULL)
        OR
        (admission_outcome IN ('rejected', 'audit_only') AND canonical_edge_id IS NULL AND decided_at IS NOT NULL)
    ),
    UNIQUE (node_a_id, node_b_id, relation),
    CONSTRAINT canonical_contradiction_proposals_edge_shape_key UNIQUE (
        canonical_contradiction_proposal_id,
        node_a_id,
        node_b_id,
        relation
    ),
    CONSTRAINT canonical_contradiction_proposals_edge_link_key UNIQUE (
        canonical_contradiction_proposal_id,
        canonical_edge_id
    )
);

ALTER TABLE canonical_graph_edges
    ADD COLUMN origin_canonical_contradiction_proposal_id TEXT
        REFERENCES canonical_contradiction_proposals(canonical_contradiction_proposal_id),
    ALTER COLUMN origin_proposal_occurrence_id DROP NOT NULL,
    ADD CONSTRAINT canonical_graph_edges_exact_origin_ck CHECK (
        num_nonnulls(
            origin_proposal_occurrence_id,
            origin_canonical_contradiction_proposal_id
        ) = 1
    ),
    ADD CONSTRAINT canonical_graph_edges_contradiction_origin_relation_ck CHECK (
        (relation = 'contradicts') =
        (origin_canonical_contradiction_proposal_id IS NOT NULL)
    ),
    ADD CONSTRAINT canonical_graph_edges_contradiction_shape_fk FOREIGN KEY (
        origin_canonical_contradiction_proposal_id,
        from_node_id,
        to_node_id,
        relation
    ) REFERENCES canonical_contradiction_proposals (
        canonical_contradiction_proposal_id,
        node_a_id,
        node_b_id,
        relation
    ),
    ADD CONSTRAINT canonical_graph_edges_contradiction_link_key UNIQUE (
        origin_canonical_contradiction_proposal_id,
        canonical_edge_id
    );

CREATE TABLE canonical_contradiction_admission_decisions (
    canonical_contradiction_admission_decision_id TEXT PRIMARY KEY,
    canonical_contradiction_proposal_id TEXT NOT NULL UNIQUE
        REFERENCES canonical_contradiction_proposals(canonical_contradiction_proposal_id),
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
    CONSTRAINT canonical_contradiction_decisions_edge_link_key UNIQUE (
        canonical_contradiction_proposal_id,
        canonical_edge_id
    )
);

ALTER TABLE canonical_contradiction_proposals
    ADD CONSTRAINT canonical_contradiction_proposals_edge_link_fk FOREIGN KEY (
        canonical_contradiction_proposal_id,
        canonical_edge_id
    ) REFERENCES canonical_graph_edges (
        origin_canonical_contradiction_proposal_id,
        canonical_edge_id
    ) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE canonical_graph_edges
    ADD CONSTRAINT canonical_graph_edges_contradiction_decision_fk FOREIGN KEY (
        origin_canonical_contradiction_proposal_id,
        canonical_edge_id
    ) REFERENCES canonical_contradiction_admission_decisions (
        canonical_contradiction_proposal_id,
        canonical_edge_id
    ) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE canonical_contradiction_admission_decisions
    ADD CONSTRAINT canonical_contradiction_decisions_proposal_link_fk FOREIGN KEY (
        canonical_contradiction_proposal_id,
        canonical_edge_id
    ) REFERENCES canonical_contradiction_proposals (
        canonical_contradiction_proposal_id,
        canonical_edge_id
    ) DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX canonical_contradiction_proposals_outcome_idx
    ON canonical_contradiction_proposals (admission_outcome, created_at);

CREATE INDEX canonical_graph_edges_contradiction_origin_idx
    ON canonical_graph_edges (origin_canonical_contradiction_proposal_id)
    WHERE origin_canonical_contradiction_proposal_id IS NOT NULL;
