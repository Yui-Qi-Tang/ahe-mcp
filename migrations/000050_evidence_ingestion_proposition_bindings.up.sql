-- Externally declared proposition identity; no semantic normalization or truth arbitration.
-- Initial request IDs and change request IDs have separate idempotency namespaces.
CREATE FUNCTION proposition_digest(VARIADIC parts text[]) RETURNS text
LANGUAGE plpgsql AS $$
BEGIN
    RETURN (SELECT encode(sha256(convert_to(
        string_agg(octet_length(v)::text || ':' || v, '' ORDER BY n), 'UTF8')), 'hex')
    FROM unnest(parts) WITH ORDINALITY AS fields(v, n));
END $$;

CREATE TABLE canonical_propositions (
    proposition_id text COLLATE "C" PRIMARY KEY,
    namespace_id text COLLATE "C" NOT NULL CHECK (octet_length(namespace_id) BETWEEN 1 AND 512),
    local_id text COLLATE "C" NOT NULL CHECK (octet_length(local_id) BETWEEN 1 AND 512),
    scope_ref text COLLATE "C" NOT NULL CHECK (octet_length(scope_ref) BETWEEN 1 AND 512),
    revision text COLLATE "C" NOT NULL CHECK (octet_length(revision) BETWEEN 1 AND 512),
    definition text COLLATE "C" NOT NULL CHECK (octet_length(definition) BETWEEN 1 AND 8192),
    UNIQUE (namespace_id, local_id, scope_ref, revision),
    CHECK (proposition_id = 'prop:sha256:' || encode(sha256(convert_to('23:proposition-identity/v1' || octet_length(namespace_id)::text || ':' || namespace_id || octet_length(local_id)::text || ':' || local_id || octet_length(scope_ref)::text || ':' || scope_ref || octet_length(revision)::text || ':' || revision, 'UTF8')), 'hex'))
);

CREATE TABLE canonical_proposition_bindings (
    request_id text COLLATE "C" PRIMARY KEY CHECK (octet_length(request_id) BETWEEN 1 AND 512),
    proposition_id text NOT NULL REFERENCES canonical_propositions(proposition_id),
    canonical_node_id text NOT NULL UNIQUE REFERENCES canonical_graph_nodes(canonical_node_id),
    decision_by text NOT NULL CHECK (octet_length(decision_by) BETWEEN 1 AND 512),
    decision_reason text NOT NULL CHECK (octet_length(decision_reason) BETWEEN 1 AND 2048),
    request_hash text NOT NULL
);

CREATE FUNCTION proposition_check_member() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
DECLARE
    p record;
    node_kind text;
    admitted boolean;
BEGIN
    PERFORM set_config('search_path', format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    SELECT * INTO STRICT p FROM canonical_propositions WHERE proposition_id = NEW.proposition_id;
    SELECT n.node_kind, o.admission_outcome = 'admitted' AND o.canonical_ref = n.canonical_node_id
      INTO node_kind, admitted
      FROM canonical_graph_nodes n JOIN proposal_occurrences o
        ON o.proposal_occurrence_id = n.origin_proposal_occurrence_id
      WHERE n.canonical_node_id = NEW.canonical_node_id;
    IF node_kind IS NULL OR node_kind NOT IN ('source_claim', 'derived_claim') OR admitted IS NOT TRUE THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'membership requires an admitted source_claim or derived_claim';
    END IF;
    IF NEW.request_hash <> proposition_digest(
        'proposition-binding/v1', NEW.request_id,
        p.namespace_id, p.local_id, p.scope_ref, p.revision, p.definition,
        NEW.canonical_node_id, NEW.decision_by, NEW.decision_reason) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'membership request hash mismatch';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER proposition_member_guard BEFORE INSERT ON canonical_proposition_bindings
    FOR EACH ROW EXECUTE FUNCTION proposition_check_member();

CREATE FUNCTION proposition_check_complete() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
BEGIN
    PERFORM set_config('search_path', format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    IF NOT EXISTS (SELECT 1 FROM canonical_proposition_bindings WHERE proposition_id=NEW.proposition_id)
       AND NOT EXISTS (SELECT 1 FROM canonical_proposition_binding_events WHERE target_id=NEW.proposition_id) THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'proposition requires a recorded binding at commit';
    END IF;
    RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER proposition_complete AFTER INSERT ON canonical_propositions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION proposition_check_complete();

CREATE FUNCTION proposition_immutable() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'proposition records are append-only';
END $$;
CREATE TRIGGER proposition_immutable_rows BEFORE UPDATE OR DELETE ON canonical_propositions
    FOR EACH ROW EXECUTE FUNCTION proposition_immutable();
CREATE TRIGGER proposition_immutable_table BEFORE TRUNCATE ON canonical_propositions
    FOR EACH STATEMENT EXECUTE FUNCTION proposition_immutable();
CREATE TRIGGER proposition_binding_immutable_rows BEFORE UPDATE OR DELETE ON canonical_proposition_bindings
    FOR EACH ROW EXECUTE FUNCTION proposition_immutable();
CREATE TRIGGER proposition_binding_immutable_table BEFORE TRUNCATE ON canonical_proposition_bindings
    FOR EACH STATEMENT EXECUTE FUNCTION proposition_immutable();
CREATE TABLE canonical_proposition_binding_events (
    request_id text COLLATE "C" PRIMARY KEY CHECK (octet_length(request_id) BETWEEN 1 AND 512),
    canonical_node_id text NOT NULL REFERENCES canonical_proposition_bindings(canonical_node_id),
    revision bigint NOT NULL CHECK (revision > 0),
    previous_ref text NOT NULL CHECK (octet_length(previous_ref) BETWEEN 1 AND 1024),
    from_id text REFERENCES canonical_propositions(proposition_id),
    target_id text REFERENCES canonical_propositions(proposition_id),
    operation text NOT NULL CHECK (operation IN ('correct', 'withdraw', 'restore')),
    decision_by text NOT NULL CHECK (octet_length(decision_by) BETWEEN 1 AND 512),
    decision_reason text NOT NULL CHECK (octet_length(decision_reason) BETWEEN 1 AND 2048),
    evidence_ref text NOT NULL CHECK (octet_length(evidence_ref) BETWEEN 1 AND 2048),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT proposition_binding_event_revision_uq UNIQUE (canonical_node_id, revision)
);

CREATE FUNCTION proposition_binding_transition_guard() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog SET row_security = off AS $$
DECLARE
    initial record;
    head record;
    head_revision bigint := 0;
    head_ref text;
    active_id text;
BEGIN
    PERFORM set_config('search_path', format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    SELECT * INTO STRICT initial FROM canonical_proposition_bindings
        WHERE canonical_node_id=NEW.canonical_node_id FOR UPDATE;
    active_id := initial.proposition_id;
    head_ref := 'membership:' || initial.request_id;
    SELECT * INTO head FROM canonical_proposition_binding_events WHERE canonical_node_id=NEW.canonical_node_id
        ORDER BY revision DESC LIMIT 1;
    IF FOUND THEN
        head_revision := head.revision;
        head_ref := 'event:' || head.request_id;
        active_id := head.target_id;
    END IF;
    IF NEW.revision <> head_revision + 1 OR NEW.previous_ref <> head_ref
       OR NEW.from_id IS DISTINCT FROM active_id THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'binding head conflict';
    END IF;
    IF NEW.operation = 'correct' THEN
        IF active_id IS NULL OR NEW.target_id IS NULL OR NEW.target_id = active_id THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'correction requires active distinct target';
        END IF;
    ELSIF NEW.operation = 'withdraw' THEN
        IF active_id IS NULL OR NEW.target_id IS NOT NULL THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'withdrawal requires active to null';
        END IF;
    ELSIF NEW.operation = 'restore' THEN
        IF active_id IS NOT NULL OR head.operation IS DISTINCT FROM 'withdraw'
           OR NEW.target_id IS NULL OR NEW.target_id IS DISTINCT FROM head.from_id THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'restoration requires last withdrawn target';
        END IF;
    ELSE
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'unknown binding operation';
    END IF;
    NEW.recorded_at := clock_timestamp();
    RETURN NEW;
END $$;
CREATE TRIGGER proposition_binding_transition BEFORE INSERT ON canonical_proposition_binding_events
    FOR EACH ROW EXECUTE FUNCTION proposition_binding_transition_guard();
CREATE TRIGGER proposition_binding_immutable_rows BEFORE UPDATE OR DELETE ON canonical_proposition_binding_events
    FOR EACH ROW EXECUTE FUNCTION proposition_immutable();
CREATE TRIGGER proposition_binding_immutable_table BEFORE TRUNCATE ON canonical_proposition_binding_events
    FOR EACH STATEMENT EXECUTE FUNCTION proposition_immutable();




REVOKE ALL ON FUNCTION proposition_digest(text[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION proposition_check_member() FROM PUBLIC;
REVOKE ALL ON FUNCTION proposition_check_complete() FROM PUBLIC;
REVOKE ALL ON FUNCTION proposition_immutable() FROM PUBLIC;
REVOKE ALL ON FUNCTION proposition_binding_transition_guard() FROM PUBLIC;
