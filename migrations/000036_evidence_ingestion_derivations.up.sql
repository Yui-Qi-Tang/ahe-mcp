CREATE TABLE canonical_derivations (
    derivation_id TEXT PRIMARY KEY CHECK (derivation_id LIKE 'derivation:%'),
    node_id TEXT NOT NULL UNIQUE REFERENCES canonical_graph_nodes(canonical_node_id),
    method TEXT NOT NULL CHECK (btrim(method) <> ''),
    producer TEXT NOT NULL CHECK (btrim(producer) <> ''),
    trace_ref TEXT NOT NULL CHECK (btrim(trace_ref) <> ''),
    provenance_ref TEXT NOT NULL CHECK (btrim(provenance_ref) <> ''),
    origin_proposal_occurrence_id TEXT NOT NULL UNIQUE REFERENCES proposal_occurrences(proposal_occurrence_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE canonical_derivation_parents (
    derivation_id TEXT NOT NULL REFERENCES canonical_derivations(derivation_id),
    parent_node_id TEXT NOT NULL REFERENCES canonical_graph_nodes(canonical_node_id),
    canonical_edge_id TEXT NOT NULL UNIQUE REFERENCES canonical_graph_edges(canonical_edge_id),
    PRIMARY KEY (derivation_id, parent_node_id)
);

CREATE INDEX canonical_derivation_parents_parent_idx
    ON canonical_derivation_parents (parent_node_id, derivation_id);
