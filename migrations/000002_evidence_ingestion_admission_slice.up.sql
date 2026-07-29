CREATE TABLE canonical_graph_nodes (
    canonical_node_id TEXT PRIMARY KEY,
    node_kind TEXT NOT NULL CHECK (node_kind IN ('raw_evidence', 'source_claim', 'derived_claim', 'candidate')),
    payload JSONB NOT NULL,
    provenance JSONB NOT NULL,
    temporal JSONB NOT NULL,
    integrity JSONB NOT NULL,
    origin_proposal_occurrence_id TEXT NOT NULL REFERENCES proposal_occurrences(proposal_occurrence_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE canonical_graph_edges (
    canonical_edge_id TEXT PRIMARY KEY,
    from_node_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    to_node_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    relation TEXT NOT NULL CHECK (relation IN ('supports_claim', 'contradicts', 'references', 'supersedes', 'implements', 'derived_from')),
    provenance JSONB NOT NULL,
    origin_proposal_occurrence_id TEXT NOT NULL REFERENCES proposal_occurrences(proposal_occurrence_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (from_node_id, to_node_id, relation)
);

CREATE TABLE admission_decisions (
    admission_decision_id TEXT PRIMARY KEY,
    proposal_occurrence_id TEXT NOT NULL UNIQUE REFERENCES proposal_occurrences(proposal_occurrence_id),
    outcome TEXT NOT NULL CHECK (outcome IN ('rejected', 'audit_only', 'admitted')),
    canonical_ref TEXT REFERENCES canonical_graph_nodes(canonical_node_id),
    raw_evidence_node_ids JSONB NOT NULL,
    canonical_edge_ids JSONB NOT NULL,
    decision_by TEXT NOT NULL,
    decision_reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (outcome = 'admitted' AND canonical_ref IS NOT NULL)
        OR
        (outcome <> 'admitted' AND canonical_ref IS NULL)
    )
);

CREATE INDEX canonical_graph_nodes_origin_proposal_idx
    ON canonical_graph_nodes (origin_proposal_occurrence_id);

CREATE INDEX canonical_graph_edges_origin_proposal_idx
    ON canonical_graph_edges (origin_proposal_occurrence_id);
