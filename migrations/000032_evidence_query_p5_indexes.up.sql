CREATE INDEX proposal_occurrences_statement_search_idx
    ON proposal_occurrences
    USING GIN (to_tsvector('simple', statement_text));

CREATE INDEX proposal_occurrences_relation_caller_symbol_idx
    ON proposal_occurrences ((proposed_payload->'code_relation'->'caller'->>'symbol_ref'))
    WHERE proposed_payload ? 'code_relation';

CREATE INDEX proposal_occurrences_relation_target_symbol_idx
    ON proposal_occurrences ((proposed_payload->'code_relation'->'target'->>'symbol_ref'))
    WHERE proposed_payload ? 'code_relation';

CREATE INDEX canonical_graph_edges_from_relation_idx
    ON canonical_graph_edges (from_node_id, relation, canonical_edge_id);

CREATE INDEX canonical_graph_edges_to_relation_idx
    ON canonical_graph_edges (to_node_id, relation, canonical_edge_id);
