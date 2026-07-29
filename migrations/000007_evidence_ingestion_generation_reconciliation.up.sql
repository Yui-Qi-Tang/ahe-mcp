CREATE TABLE repository_generation_reconciliations (
    source_generation_id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    extractor_name TEXT NOT NULL,
    previous_generation_id TEXT UNIQUE,
    identity_contract TEXT NOT NULL CHECK (identity_contract = 'proposal-fingerprint+source-refs-v1'),
    unchanged_count INTEGER NOT NULL CHECK (unchanged_count >= 0),
    new_count INTEGER NOT NULL CHECK (new_count >= 0),
    stale_count INTEGER NOT NULL CHECK (stale_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (source_generation_id, repo_id, extractor_name)
        REFERENCES repository_source_generations(source_generation_id, repo_id, extractor_name),
    FOREIGN KEY (previous_generation_id, repo_id, extractor_name)
        REFERENCES repository_source_generations(source_generation_id, repo_id, extractor_name),
    CHECK (previous_generation_id IS NULL OR previous_generation_id <> source_generation_id)
);

CREATE TABLE repository_generation_proposal_reconciliations (
    reconciliation_item_id TEXT PRIMARY KEY,
    source_generation_id TEXT NOT NULL REFERENCES repository_generation_reconciliations(source_generation_id),
    lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN ('new', 'unchanged', 'stale')),
    proposal_identity TEXT NOT NULL,
    current_proposal_occurrence_id TEXT REFERENCES proposal_occurrences(proposal_occurrence_id),
    previous_proposal_occurrence_id TEXT REFERENCES proposal_occurrences(proposal_occurrence_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_generation_id, current_proposal_occurrence_id),
    UNIQUE (source_generation_id, previous_proposal_occurrence_id),
    CHECK (
        (lifecycle_state = 'new' AND current_proposal_occurrence_id IS NOT NULL AND previous_proposal_occurrence_id IS NULL)
        OR
        (lifecycle_state = 'unchanged' AND current_proposal_occurrence_id IS NOT NULL AND previous_proposal_occurrence_id IS NOT NULL)
        OR
        (lifecycle_state = 'stale' AND current_proposal_occurrence_id IS NULL AND previous_proposal_occurrence_id IS NOT NULL)
    )
);

CREATE INDEX repository_generation_proposal_reconciliations_identity_idx
    ON repository_generation_proposal_reconciliations (source_generation_id, proposal_identity);
