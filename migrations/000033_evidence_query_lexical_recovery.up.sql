CREATE INDEX proposal_occurrences_statement_english_search_idx
    ON proposal_occurrences
    USING GIN (to_tsvector('english', statement_text));
