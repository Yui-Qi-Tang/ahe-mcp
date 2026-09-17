-- A reviewed implements receipt authorizes one directed navigation edge. The
-- existing derived proposal is only its immutable source carrier: its previous
-- node admission is not retroactively treated as permission for this edge.
LOCK TABLE proposal_occurrences IN ACCESS EXCLUSIVE MODE;
LOCK TABLE canonical_graph_nodes, canonical_graph_edges, canonical_derivations,
    canonical_derivation_parents IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    implements_edge_count BIGINT;
BEGIN
    SELECT count(*) INTO implements_edge_count
    FROM canonical_graph_edges WHERE relation = 'implements';
    IF implements_edge_count <> 0 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = format(
            'migration 000047 implements preflight failed: implements_edges=%s',
            implements_edge_count);
    END IF;
END $$;

CREATE TABLE canonical_implements_admissions (
    request_id TEXT CONSTRAINT canonical_implements_admissions_pkey PRIMARY KEY,
    contract_version TEXT NOT NULL,
    receipt_id TEXT NOT NULL CONSTRAINT canonical_implements_admissions_receipt_uq UNIQUE,
    canonical_edge_id TEXT NOT NULL CONSTRAINT canonical_implements_admissions_edge_uq UNIQUE,
    specification_node_id TEXT NOT NULL,
    implementation_node_id TEXT NOT NULL,
    origin_proposal_occurrence_id TEXT NOT NULL,
    reviewer_id TEXT NOT NULL,
    decision_reason TEXT NOT NULL,
    producer_session_ref TEXT NOT NULL DEFAULT '',
    basis_id TEXT NOT NULL,
    report_id TEXT NOT NULL,
    candidate_id TEXT NOT NULL,
    display_id TEXT NOT NULL,
    basis_payload_utf8 TEXT NOT NULL,
    receipt_payload_utf8 TEXT NOT NULL,
    display_media_type TEXT NOT NULL,
    display_payload_utf8 TEXT NOT NULL,
    basis_payload_hash TEXT NOT NULL,
    receipt_payload_hash TEXT NOT NULL,
    display_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_implements_admissions_pair_uq
        UNIQUE (specification_node_id, implementation_node_id),
    CONSTRAINT canonical_implements_admissions_specification_fk
        FOREIGN KEY (specification_node_id) REFERENCES canonical_graph_nodes(canonical_node_id),
    CONSTRAINT canonical_implements_admissions_implementation_fk
        FOREIGN KEY (implementation_node_id) REFERENCES canonical_graph_nodes(canonical_node_id),
    CONSTRAINT canonical_implements_admissions_origin_fk
        FOREIGN KEY (origin_proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id),
    CONSTRAINT canonical_implements_admissions_edge_fk
        FOREIGN KEY (canonical_edge_id) REFERENCES canonical_graph_edges(canonical_edge_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE FUNCTION canonical_implements_admission_assert_v1(checked_request_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    admission canonical_implements_admissions%ROWTYPE;
    edge canonical_graph_edges%ROWTYPE;
    specification canonical_graph_nodes%ROWTYPE;
    implementation canonical_graph_nodes%ROWTYPE;
    derivation canonical_derivations%ROWTYPE;
    packet JSONB;
    receipt JSONB;
    display JSONB;
    expected_edge_id TEXT;
    parent_count BIGINT;
BEGIN
    SELECT * INTO admission FROM canonical_implements_admissions
    WHERE request_id = checked_request_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements edge lacks its reviewed admission receipt';
    END IF;
    IF admission.contract_version <> 'canonical-derived-implements-admission/v1'
        OR octet_length(admission.request_id) NOT BETWEEN 1 AND 200
        OR btrim(admission.request_id) <> admission.request_id
        OR octet_length(admission.reviewer_id) NOT BETWEEN 1 AND 200
        OR btrim(admission.reviewer_id) = ''
        OR octet_length(admission.decision_reason) NOT BETWEEN 1 AND 2000
        OR btrim(admission.decision_reason) = ''
        OR octet_length(admission.producer_session_ref) > 2000
        OR octet_length(admission.basis_payload_utf8) NOT BETWEEN 1 AND 8388608
        OR octet_length(admission.receipt_payload_utf8) NOT BETWEEN 1 AND 262144
        OR octet_length(admission.display_payload_utf8) NOT BETWEEN 1 AND 131072
        OR admission.display_media_type <> 'application/vnd.ahe.lab-implements-derived-review.v1+json'
        OR admission.specification_node_id !~ '^canon-node:[A-Za-z0-9_.:-]+$'
        OR admission.implementation_node_id !~ '^canon-node:[A-Za-z0-9_.:-]+$'
        OR admission.receipt_id !~ '^canonical-implements-derived-receipt:v1:sha256:[0-9a-f]{64}$'
        OR admission.basis_id !~ '^canonical-implements-derived-basis:v1:sha256:[0-9a-f]{64}$'
        OR admission.report_id !~ '^canonical-implements-candidate-report:v1:sha256:[0-9a-f]{64}$'
        OR admission.candidate_id !~ '^canonical-implements-candidate:v1:sha256:[0-9a-f]{64}$'
        OR admission.display_id !~ '^canonical-implements-derived-display:v1:sha256:[0-9a-f]{64}$'
        OR admission.specification_node_id = admission.implementation_node_id THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements admission exceeds its fixed contract or byte bounds';
    END IF;
    IF admission.basis_payload_hash <> 'sha256:' || encode(sha256(convert_to(admission.basis_payload_utf8, 'UTF8')), 'hex')
        OR admission.receipt_payload_hash <> 'sha256:' || encode(sha256(convert_to(admission.receipt_payload_utf8, 'UTF8')), 'hex')
        OR admission.display_payload_hash <> 'sha256:' || encode(sha256(convert_to(admission.display_payload_utf8, 'UTF8')), 'hex') THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements admission exact payload hash differs';
    END IF;
    packet := admission.basis_payload_utf8::jsonb;
    receipt := admission.receipt_payload_utf8::jsonb;
    display := admission.display_payload_utf8::jsonb;
    IF receipt ->> 'contract_version' IS DISTINCT FROM 'lab-canonical-implements-derived-review-receipt/v1'
        OR receipt ->> 'id' IS DISTINCT FROM admission.receipt_id
        OR receipt ->> 'authority' IS DISTINCT FROM 'review_asserted_lab_receipt'
        OR receipt ->> 'direction' IS DISTINCT FROM 'specification_claim_to_implementation_claim'
        OR receipt ->> 'semantic_effect' IS DISTINCT FROM 'direct_structural_navigation_only'
        OR receipt ->> 'policy_effect' IS DISTINCT FROM 'no_truth_support_status_supersession_or_currentness_effect'
        OR receipt ->> 'reviewer_id' IS DISTINCT FROM admission.reviewer_id
        OR receipt ->> 'decision_reason' IS DISTINCT FROM admission.decision_reason
        OR receipt #>> '{subject,basis_id}' IS DISTINCT FROM admission.basis_id
        OR receipt #>> '{subject,report_id}' IS DISTINCT FROM admission.report_id
        OR receipt #>> '{subject,candidate_id}' IS DISTINCT FROM admission.candidate_id
        OR receipt #>> '{subject,display_id}' IS DISTINCT FROM admission.display_id
        OR receipt #>> '{display,id}' IS DISTINCT FROM admission.display_id
        OR receipt #>> '{display,contract_version}' IS DISTINCT FROM 'lab-canonical-implements-derived-review-display/v1'
        OR receipt #>> '{display,media_type}' IS DISTINCT FROM admission.display_media_type
        OR receipt #>> '{display,payload_utf8}' IS DISTINCT FROM admission.display_payload_utf8
        OR receipt #> '{projected_edge}' IS DISTINCT FROM jsonb_build_object(
            'id', admission.canonical_edge_id,
            'from', admission.specification_node_id,
            'to', admission.implementation_node_id,
            'relation', 'implements',
            'provenance_receipt_id', admission.receipt_id)
        OR packet #>> '{basis,id}' IS DISTINCT FROM admission.basis_id
        OR packet #>> '{basis,contract_version}' IS DISTINCT FROM 'lab-canonical-implements-derived-review-basis/v1'
        OR packet #>> '{basis,root_node_id}' IS DISTINCT FROM admission.specification_node_id
        OR packet #>> '{basis,admitted_cut_id}' IS DISTINCT FROM receipt #>> '{subject,admitted_cut_id}'
        OR packet #>> '{cut,id}' IS DISTINCT FROM receipt #>> '{subject,admitted_cut_id}'
        OR packet #>> '{report,id}' IS DISTINCT FROM admission.report_id
        OR COALESCE(receipt #>> '{subject,admitted_cut_id}', '') !~ '^canonical-implements-cut:v1:sha256:[0-9a-f]{64}$'
        OR COALESCE(receipt #>> '{subject,review_package_id}', '') !~ '^canonical-implements-derived-package:v1:sha256:[0-9a-f]{64}$'
        OR display ->> 'contract_version' IS DISTINCT FROM 'lab-canonical-implements-derived-review-display/v1'
        OR display ->> 'id' IS DISTINCT FROM receipt #>> '{subject,review_package_id}'
        OR display ->> 'basis_id' IS DISTINCT FROM admission.basis_id
        OR display ->> 'report_id' IS DISTINCT FROM admission.report_id
        OR display #>> '{candidate,id}' IS DISTINCT FROM admission.candidate_id
        OR display #>> '{candidate,specification,node_id}' IS DISTINCT FROM admission.specification_node_id
        OR display #>> '{candidate,implementation,node_id}' IS DISTINCT FROM admission.implementation_node_id
        OR display -> 'mapping' IS DISTINCT FROM receipt -> 'mapping'
        OR display ->> 'rule_statement' IS DISTINCT FROM packet #>> '{basis,rule_statement}'
        OR display -> 'ancestors' IS DISTINCT FROM packet #> '{basis,ancestors}' THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements receipt, display, and independent basis disagree';
    END IF;

    SELECT * INTO specification FROM canonical_graph_nodes
    WHERE canonical_node_id = admission.specification_node_id;
    SELECT * INTO implementation FROM canonical_graph_nodes
    WHERE canonical_node_id = admission.implementation_node_id;
    SELECT * INTO derivation FROM canonical_derivations
    WHERE node_id = admission.specification_node_id;
    IF specification.node_kind IS DISTINCT FROM 'derived_claim'
        OR implementation.node_kind IS DISTINCT FROM 'source_claim'
        OR specification.origin_proposal_occurrence_id IS DISTINCT FROM admission.origin_proposal_occurrence_id
        OR derivation.origin_proposal_occurrence_id IS DISTINCT FROM admission.origin_proposal_occurrence_id
        OR NOT EXISTS (
            SELECT 1 FROM proposal_occurrences p JOIN admission_decisions d USING (proposal_occurrence_id)
            WHERE p.proposal_occurrence_id = admission.origin_proposal_occurrence_id
                AND p.admission_outcome = 'admitted' AND d.outcome = 'admitted'
                AND p.canonical_ref = admission.specification_node_id
                AND d.canonical_ref = admission.specification_node_id
        ) OR NOT EXISTS (
            SELECT 1 FROM proposal_occurrences p JOIN admission_decisions d USING (proposal_occurrence_id)
            WHERE p.proposal_occurrence_id = implementation.origin_proposal_occurrence_id
                AND p.admission_outcome = 'admitted' AND d.outcome = 'admitted'
                AND p.canonical_ref = admission.implementation_node_id
                AND d.canonical_ref = admission.implementation_node_id
                AND p.proposed_payload ? 'code_fact'
                AND implementation.payload ->> 'source_type' = 'code_repository'
        ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements endpoints lack admitted derived and code-fact authority';
    END IF;
    SELECT count(*) INTO parent_count FROM canonical_derivation_parents
    WHERE derivation_id = derivation.derivation_id;
    IF parent_count NOT BETWEEN 1 AND 8 OR EXISTS (
        SELECT 1 FROM canonical_derivation_parents p
        JOIN canonical_graph_nodes n ON n.canonical_node_id = p.parent_node_id
        JOIN canonical_graph_edges e ON e.canonical_edge_id = p.canonical_edge_id
        WHERE p.derivation_id = derivation.derivation_id
            AND (n.node_kind <> 'source_claim' OR e.relation <> 'derived_from'
                OR e.from_node_id <> p.parent_node_id OR e.to_node_id <> admission.specification_node_id)
    ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements admission requires complete depth-one source parents';
    END IF;
    IF jsonb_array_length(packet #> '{basis,ancestors,nodes}') IS DISTINCT FROM parent_count + 1
        OR jsonb_array_length(packet #> '{basis,ancestors,edges}') IS DISTINCT FROM parent_count
        OR jsonb_array_length(packet #> '{basis,source_leaves}') IS DISTINCT FROM parent_count
        OR packet #> '{basis,ancestors,derivations}' IS DISTINCT FROM jsonb_build_array(jsonb_build_object(
            'id', derivation.derivation_id, 'node_id', derivation.node_id,
            'parents', (SELECT jsonb_agg(parent_node_id ORDER BY parent_node_id COLLATE "C")
                FROM canonical_derivation_parents WHERE derivation_id = derivation.derivation_id),
            'method', derivation.method, 'producer', derivation.producer,
            'trace_ref', derivation.trace_ref, 'provenance_ref', derivation.provenance_ref))
        OR EXISTS (
            SELECT 1 FROM canonical_derivation_parents p
            WHERE p.derivation_id = derivation.derivation_id AND NOT EXISTS (
                SELECT 1 FROM jsonb_array_elements(packet #> '{basis,ancestors,edges}') e
                WHERE e ->> 'id' = p.canonical_edge_id AND e ->> 'from' = p.parent_node_id
                    AND e ->> 'to' = admission.specification_node_id AND e ->> 'relation' = 'derived_from'
            )
        ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements reviewed basis differs from complete persisted AND parents';
    END IF;
    expected_edge_id := 'canon-edge:' || substr(encode(sha256(
        convert_to(admission.specification_node_id, 'UTF8') || '\x00'::BYTEA ||
        convert_to(admission.implementation_node_id, 'UTF8') || '\x00'::BYTEA ||
        convert_to('implements', 'UTF8') || '\x00'::BYTEA), 'hex'), 1, 16);
    SELECT * INTO edge FROM canonical_graph_edges WHERE canonical_edge_id = admission.canonical_edge_id;
    IF NOT FOUND OR admission.canonical_edge_id <> expected_edge_id
        OR edge.from_node_id <> admission.specification_node_id
        OR edge.to_node_id <> admission.implementation_node_id OR edge.relation <> 'implements'
        OR edge.origin_proposal_occurrence_id <> admission.origin_proposal_occurrence_id
        OR edge.provenance <> jsonb_build_object(
            'id', 'provenance:' || admission.canonical_edge_id,
            'origin_refs', jsonb_build_array(admission.specification_node_id, admission.implementation_node_id, admission.origin_proposal_occurrence_id),
            'origin_group_id', admission.canonical_edge_id, 'producer', 'ahe-wrap',
            'method', 'reviewed_derived_implements', 'method_version', 'v1',
            'trace_ref', admission.request_id, 'review_ref', admission.receipt_id)
        OR EXISTS (SELECT 1 FROM canonical_ordinary_admission_edge_bindings WHERE canonical_edge_id = admission.canonical_edge_id)
        OR EXISTS (SELECT 1 FROM canonical_contradiction_admission_decisions WHERE canonical_edge_id = admission.canonical_edge_id) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements edge differs from its sole reviewed receipt authority';
    END IF;
END $$;

CREATE FUNCTION canonical_implements_admission_assert_edge_v1(checked_edge_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    checked_request_id TEXT;
    authority_count BIGINT;
BEGIN
    SELECT count(*), min(request_id) INTO authority_count, checked_request_id
    FROM canonical_implements_admissions WHERE canonical_edge_id = checked_edge_id;
    IF authority_count <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = format(
            'canonical implements edge admission authority count is %s, expected 1', authority_count);
    END IF;
    PERFORM canonical_implements_admission_assert_v1(checked_request_id);
END $$;

CREATE FUNCTION canonical_implements_admission_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM canonical_implements_admission_assert_v1(
        CASE WHEN TG_OP = 'DELETE' THEN OLD.request_id ELSE NEW.request_id END);
    RETURN NULL;
END $$;

CREATE TRIGGER canonical_implements_admissions_append_only
BEFORE UPDATE OR DELETE ON canonical_implements_admissions
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_implements_admissions_forbid_truncate
BEFORE TRUNCATE ON canonical_implements_admissions
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE CONSTRAINT TRIGGER canonical_implements_admissions_authority
AFTER INSERT OR UPDATE OR DELETE ON canonical_implements_admissions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_implements_admission_trigger_v1();

CREATE OR REPLACE FUNCTION canonical_implements_recursive_basis_assert_v2(root_id TEXT, basis JSONB)
RETURNS VOID
LANGUAGE plpgsql
AS $$
#variable_conflict use_variable
DECLARE
    node_ids TEXT[] := ARRAY[root_id];
    edge_ids TEXT[] := ARRAY[]::TEXT[];
    derived_ids TEXT[] := ARRAY[]::TEXT[];
    source_ids TEXT[] := ARRAY[]::TEXT[];
    processed_ids TEXT[] := ARRAY[]::TEXT[];
    layer_by_node JSONB := '{}'::jsonb;
    cursor_index INT := 1;
    node_id TEXT;
    parent_id TEXT;
    node canonical_graph_nodes%ROWTYPE;
    derivation canonical_derivations%ROWTYPE;
    parent RECORD;
    parent_ids TEXT[];
    expected_node JSONB;
    expected_derivation JSONB;
    expected_edge JSONB;
    rules JSONB := basis -> 'rules';
    ancestors JSONB := basis -> 'ancestors';
    layer INT;
    progressed BOOLEAN;
BEGIN
    IF jsonb_typeof(ancestors) IS DISTINCT FROM 'object'
        OR jsonb_typeof(rules) IS DISTINCT FROM 'array'
        OR jsonb_array_length(rules) NOT BETWEEN 1 AND 63
        OR jsonb_typeof(ancestors -> 'nodes') IS DISTINCT FROM 'array'
        OR jsonb_array_length(ancestors -> 'nodes') NOT BETWEEN 2 AND 64
        OR jsonb_typeof(ancestors -> 'edges') IS DISTINCT FROM 'array'
        OR jsonb_array_length(ancestors -> 'edges') NOT BETWEEN 1 AND 128
        OR jsonb_typeof(ancestors -> 'derivations') IS DISTINCT FROM 'array'
        OR jsonb_typeof(basis -> 'source_leaves') IS DISTINCT FROM 'array'
        OR jsonb_array_length(basis -> 'source_leaves') NOT BETWEEN 1 AND 63 THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements recursive basis exceeds complete bounded AND contract';
    END IF;
    WHILE cursor_index <= cardinality(node_ids) LOOP
        node_id := node_ids[cursor_index];
        cursor_index := cursor_index + 1;
        SELECT * INTO node FROM canonical_graph_nodes n WHERE n.canonical_node_id = node_id;
        IF NOT FOUND OR node.node_kind NOT IN ('source_claim', 'derived_claim')
            OR NOT (
                EXISTS (
                    SELECT 1 FROM proposal_occurrences p JOIN admission_decisions a USING(proposal_occurrence_id)
                    JOIN canonical_ordinary_admission_node_bindings b ON b.admission_decision_id = a.admission_decision_id
                    WHERE p.proposal_occurrence_id = node.origin_proposal_occurrence_id
                        AND p.admission_outcome = 'admitted' AND a.outcome = 'admitted'
                        AND p.canonical_ref = node_id AND a.canonical_ref = node_id
                        AND b.canonical_node_id = node_id AND b.materialization = 'materialized'
                )
                OR (
                    node.node_kind = 'source_claim'
                    AND EXISTS (
                        SELECT 1 FROM canonical_supersession_admission_events e
                        JOIN admission_decisions a ON a.admission_decision_id = e.admission_decision_id
                            AND a.proposal_occurrence_id = e.proposal_occurrence_id
                        JOIN proposal_occurrences p ON p.proposal_occurrence_id = e.proposal_occurrence_id
                        JOIN canonical_supersession_members m ON m.canonical_node_id = e.replacement_node_id
                            AND m.lineage_key = e.lineage_key AND m.first_admission_event_id = e.event_id
                        WHERE e.proposal_occurrence_id = node.origin_proposal_occurrence_id
                            AND e.replacement_node_id = node_id AND NOT m.was_bootstrapped
                            AND e.contract_version = 'supersession-admission-event/v2'
                            AND e.admission_outcome = 'admitted'
                            AND p.admission_outcome = 'admitted' AND a.outcome = 'admitted'
                            AND p.canonical_ref = node_id AND a.canonical_ref = node_id
                    )
                )
            ) THEN
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements recursive ancestor lacks its original admitted authority';
        END IF;
        -- Creation authority is unique; later supersession membership or a
        -- newer global head does not change this historical review subject.
        -- Derived ancestors still require the ordinary materialization arm.
        PERFORM canonical_ordinary_admission_assert_node_v1(node_id);
        expected_node := jsonb_build_object('id', node_id, 'kind', node.node_kind,
            'payload_ref', node.payload ->> 'id', 'provenance_ref', node.provenance ->> 'id',
            'temporal_ref', node.temporal ->> 'id', 'integrity_ref', node.integrity ->> 'id');
        IF (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'nodes') item WHERE item = expected_node) <> 1
            OR (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'payloads') item WHERE item = node.payload) <> 1
            OR (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'provenance') item WHERE item = node.provenance) <> 1
            OR (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'temporal') item WHERE item = node.temporal) <> 1
            OR (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'integrity') item WHERE item = node.integrity) <> 1 THEN
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements recursive ancestor content differs from native authority';
        END IF;
        IF node.node_kind = 'source_claim' THEN
            IF EXISTS (SELECT 1 FROM canonical_derivations d WHERE d.node_id = node_id)
                OR EXISTS (SELECT 1 FROM canonical_graph_edges e WHERE e.to_node_id = node_id AND e.relation = 'derived_from')
                OR (SELECT count(*) FROM jsonb_array_elements(basis -> 'source_leaves') leaf
                    WHERE leaf ->> 'specification_node_id' = node_id) <> 1 THEN
                RAISE EXCEPTION USING ERRCODE = '23514',
                    MESSAGE = 'canonical implements recursive source leaf is not an exact terminal';
            END IF;
            source_ids := array_append(source_ids, node_id);
            processed_ids := array_append(processed_ids, node_id);
            layer_by_node := layer_by_node || jsonb_build_object(node_id, 0);
            CONTINUE;
        END IF;
        derived_ids := array_append(derived_ids, node_id);
        SELECT * INTO derivation FROM canonical_derivations d WHERE d.node_id = node_id;
        IF NOT FOUND OR derivation.origin_proposal_occurrence_id <> node.origin_proposal_occurrence_id THEN
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements recursive layer lacks its registered derivation';
        END IF;
        SELECT array_agg(p.parent_node_id ORDER BY p.parent_node_id COLLATE "C")
        INTO parent_ids FROM canonical_derivation_parents p WHERE p.derivation_id = derivation.derivation_id;
        IF coalesce(cardinality(parent_ids), 0) NOT BETWEEN 1 AND 8
            OR (SELECT count(*) FROM canonical_graph_edges e
                WHERE e.to_node_id = node_id AND e.relation = 'derived_from') <> cardinality(parent_ids) THEN
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements recursive layer differs from complete persisted AND parents';
        END IF;
        expected_derivation := jsonb_build_object('id', derivation.derivation_id, 'node_id', node_id,
            'parents', to_jsonb(parent_ids), 'method', derivation.method, 'producer', derivation.producer,
            'trace_ref', derivation.trace_ref, 'provenance_ref', derivation.provenance_ref);
        IF (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'derivations') item WHERE item = expected_derivation) <> 1
            OR (SELECT count(*) FROM jsonb_array_elements(rules) rule
                WHERE rule ->> 'node_id' = node_id
                    AND rule ->> 'derivation_id' = derivation.derivation_id
                    AND jsonb_typeof(rule -> 'rule_statement') = 'string'
                    AND octet_length(rule ->> 'rule_statement') BETWEEN 1 AND 4000
                    AND btrim(rule ->> 'rule_statement') <> ''
                    AND btrim(rule ->> 'rule_statement') = rule ->> 'rule_statement'
                    AND rule = jsonb_build_object('node_id', node_id, 'derivation_id', derivation.derivation_id,
                        'rule_statement', rule ->> 'rule_statement')) <> 1 THEN
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements recursive layer rule or native derivation differs';
        END IF;
        FOR parent IN
            SELECT p.parent_node_id, p.canonical_edge_id, e.from_node_id, e.to_node_id,
                e.relation, e.provenance, e.origin_proposal_occurrence_id
            FROM canonical_derivation_parents p
            LEFT JOIN canonical_graph_edges e ON e.canonical_edge_id = p.canonical_edge_id
            WHERE p.derivation_id = derivation.derivation_id ORDER BY p.parent_node_id COLLATE "C"
        LOOP
            IF parent.from_node_id IS DISTINCT FROM parent.parent_node_id
                OR parent.to_node_id IS DISTINCT FROM node_id OR parent.relation IS DISTINCT FROM 'derived_from'
                OR parent.origin_proposal_occurrence_id IS DISTINCT FROM node.origin_proposal_occurrence_id THEN
                RAISE EXCEPTION USING ERRCODE = '23514',
                    MESSAGE = 'canonical implements recursive registered edge differs from native AND parent';
            END IF;
            expected_edge := jsonb_build_object('id', parent.canonical_edge_id,
                'from', parent.parent_node_id, 'to', node_id, 'relation', 'derived_from',
                'provenance_ref', parent.provenance ->> 'id');
            IF (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'edges') item WHERE item = expected_edge) <> 1
                OR (SELECT count(*) FROM jsonb_array_elements(ancestors -> 'provenance') item WHERE item = parent.provenance) <> 1 THEN
                RAISE EXCEPTION USING ERRCODE = '23514',
                    MESSAGE = 'canonical implements recursive reviewed edge or provenance differs';
            END IF;
            edge_ids := array_append(edge_ids, parent.canonical_edge_id);
            IF NOT parent.parent_node_id = ANY(node_ids) THEN
                node_ids := array_append(node_ids, parent.parent_node_id);
            END IF;
            IF cardinality(node_ids) > 64 OR cardinality(edge_ids) > 128 THEN
                RAISE EXCEPTION USING ERRCODE = '23514',
                    MESSAGE = 'canonical implements recursive closure exceeds node or edge bound';
            END IF;
        END LOOP;
    END LOOP;
    IF jsonb_array_length(ancestors -> 'nodes') <> cardinality(node_ids)
        OR jsonb_array_length(ancestors -> 'edges') <> cardinality(edge_ids)
        OR jsonb_array_length(ancestors -> 'derivations') <> cardinality(derived_ids)
        OR jsonb_array_length(rules) <> cardinality(derived_ids)
        OR jsonb_array_length(basis -> 'source_leaves') <> cardinality(source_ids)
        OR jsonb_array_length(ancestors -> 'payloads') IS DISTINCT FROM
            (SELECT count(DISTINCT n.payload ->> 'id') FROM canonical_graph_nodes n WHERE n.canonical_node_id = ANY(node_ids))
        OR jsonb_array_length(ancestors -> 'temporal') IS DISTINCT FROM
            (SELECT count(DISTINCT n.temporal ->> 'id') FROM canonical_graph_nodes n WHERE n.canonical_node_id = ANY(node_ids))
        OR jsonb_array_length(ancestors -> 'integrity') IS DISTINCT FROM
            (SELECT count(DISTINCT n.integrity ->> 'id') FROM canonical_graph_nodes n WHERE n.canonical_node_id = ANY(node_ids))
        OR jsonb_array_length(ancestors -> 'provenance') IS DISTINCT FROM
            (SELECT count(DISTINCT item ->> 'id') FROM (
                SELECT n.provenance AS item FROM canonical_graph_nodes n WHERE n.canonical_node_id = ANY(node_ids)
                UNION ALL SELECT e.provenance FROM canonical_graph_edges e WHERE e.canonical_edge_id = ANY(edge_ids)
            ) native_provenance) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements recursive review is not the exact complete ancestor closure';
    END IF;
    WHILE cardinality(processed_ids) < cardinality(node_ids) LOOP
        progressed := false;
        FOREACH node_id IN ARRAY derived_ids LOOP
            IF node_id = ANY(processed_ids) THEN CONTINUE; END IF;
            SELECT array_agg(p.parent_node_id) INTO parent_ids FROM canonical_derivation_parents p
                JOIN canonical_derivations d USING(derivation_id) WHERE d.node_id = node_id;
            IF NOT parent_ids <@ processed_ids THEN CONTINUE; END IF;
            layer := 0;
            FOREACH parent_id IN ARRAY parent_ids LOOP
                layer := greatest(layer, (layer_by_node ->> parent_id)::INT);
            END LOOP;
            layer := layer + 1;
            IF layer > 8 THEN
                RAISE EXCEPTION USING ERRCODE = '23514',
                    MESSAGE = 'canonical implements recursive AND depth exceeds eight derived layers';
            END IF;
            layer_by_node := layer_by_node || jsonb_build_object(node_id, layer);
            processed_ids := array_append(processed_ids, node_id);
            progressed := true;
        END LOOP;
        IF NOT progressed THEN
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements recursive AND closure contains a cycle';
        END IF;
    END LOOP;
END $$;
CREATE FUNCTION canonical_implements_admission_assert_v2(checked_request_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    admission canonical_implements_admissions%ROWTYPE;
    edge canonical_graph_edges%ROWTYPE;
    specification canonical_graph_nodes%ROWTYPE;
    implementation canonical_graph_nodes%ROWTYPE;
    derivation canonical_derivations%ROWTYPE;
    packet JSONB;
    receipt JSONB;
    display JSONB;
    expected_edge_id TEXT;
BEGIN
    SELECT * INTO admission FROM canonical_implements_admissions
    WHERE request_id = checked_request_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements edge lacks its reviewed admission receipt';
    END IF;
    IF admission.contract_version <> 'canonical-derived-implements-admission/v2'
        OR octet_length(admission.request_id) NOT BETWEEN 1 AND 200
        OR btrim(admission.request_id) <> admission.request_id
        OR octet_length(admission.reviewer_id) NOT BETWEEN 1 AND 200
        OR btrim(admission.reviewer_id) = ''
        OR octet_length(admission.decision_reason) NOT BETWEEN 1 AND 2000
        OR btrim(admission.decision_reason) = ''
        OR octet_length(admission.producer_session_ref) > 2000
        OR octet_length(admission.basis_payload_utf8) NOT BETWEEN 1 AND 8388608
        OR octet_length(admission.receipt_payload_utf8) NOT BETWEEN 1 AND 262144
        OR octet_length(admission.display_payload_utf8) NOT BETWEEN 1 AND 131072
        OR admission.display_media_type <> 'application/vnd.ahe.implements-recursive-review.v2+json'
        OR admission.specification_node_id !~ '^canon-node:[A-Za-z0-9_.:-]+$'
        OR admission.implementation_node_id !~ '^canon-node:[A-Za-z0-9_.:-]+$'
        OR admission.receipt_id !~ '^canonical-implements-recursive-receipt:v2:sha256:[0-9a-f]{64}$'
        OR admission.basis_id !~ '^canonical-implements-recursive-basis:v2:sha256:[0-9a-f]{64}$'
        OR admission.report_id !~ '^canonical-implements-candidate-report:v1:sha256:[0-9a-f]{64}$'
        OR admission.candidate_id !~ '^canonical-implements-candidate:v1:sha256:[0-9a-f]{64}$'
        OR admission.display_id !~ '^canonical-implements-recursive-display:v2:sha256:[0-9a-f]{64}$'
        OR admission.specification_node_id = admission.implementation_node_id THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements admission exceeds its fixed contract or byte bounds';
    END IF;
    IF admission.basis_payload_hash <> 'sha256:' || encode(sha256(convert_to(admission.basis_payload_utf8, 'UTF8')), 'hex')
        OR admission.receipt_payload_hash <> 'sha256:' || encode(sha256(convert_to(admission.receipt_payload_utf8, 'UTF8')), 'hex')
        OR admission.display_payload_hash <> 'sha256:' || encode(sha256(convert_to(admission.display_payload_utf8, 'UTF8')), 'hex') THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements admission exact payload hash differs';
    END IF;
    packet := admission.basis_payload_utf8::jsonb;
    receipt := admission.receipt_payload_utf8::jsonb;
    display := admission.display_payload_utf8::jsonb;
    -- Equality alone would accept two missing/null mappings. Require the
    -- reviewed mapping's bounded structural witness envelope independently.
    IF jsonb_typeof(receipt -> 'mapping') IS DISTINCT FROM 'object'
        OR jsonb_typeof(receipt #> '{mapping,proposal_sentence}') IS DISTINCT FROM 'string'
        OR octet_length(receipt #>> '{mapping,proposal_sentence}') NOT BETWEEN 1 AND 4000
        OR btrim(receipt #>> '{mapping,proposal_sentence}') = ''
        OR jsonb_typeof(receipt #> '{mapping,coverage}') IS DISTINCT FROM 'string'
        OR octet_length(receipt #>> '{mapping,coverage}') NOT BETWEEN 1 AND 4000
        OR btrim(receipt #>> '{mapping,coverage}') = ''
        OR jsonb_typeof(receipt #> '{mapping,witnesses}') IS DISTINCT FROM 'array'
        OR jsonb_array_length(receipt #> '{mapping,witnesses}') NOT BETWEEN 1 AND 8
        OR jsonb_typeof(receipt #> '{mapping,limitations}') IS DISTINCT FROM 'array'
        OR jsonb_array_length(receipt #> '{mapping,limitations}') > 32 THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements recursive reviewed mapping is missing or exceeds its bounded shape';
    END IF;
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(receipt #> '{mapping,witnesses}') witness
        WHERE jsonb_typeof(witness) IS DISTINCT FROM 'object'
            OR coalesce(witness ->> 'kind', '') NOT IN ('source_explicit_implementation_mapping', 'deterministic_language_contract', 'reviewed_behavior_mapping')
            OR witness -> 'endpoint_node_ids' IS DISTINCT FROM
                (SELECT jsonb_agg(id ORDER BY id COLLATE "C") FROM (VALUES (admission.specification_node_id), (admission.implementation_node_id)) endpoints(id))
            OR jsonb_typeof(witness -> 'source_title') IS DISTINCT FROM 'string'
            OR octet_length(witness ->> 'source_title') NOT BETWEEN 1 AND 1000 OR btrim(witness ->> 'source_title') = ''
            OR jsonb_typeof(witness -> 'source_location') IS DISTINCT FROM 'string'
            OR octet_length(witness ->> 'source_location') NOT BETWEEN 1 AND 2000 OR btrim(witness ->> 'source_location') = ''
            OR jsonb_typeof(witness -> 'exact_excerpt') IS DISTINCT FROM 'string'
            OR octet_length(witness ->> 'exact_excerpt') NOT BETWEEN 1 AND 16384
            OR witness ->> 'excerpt_hash' IS DISTINCT FROM 'sha256:' || encode(sha256(convert_to(witness ->> 'exact_excerpt', 'UTF8')), 'hex'))
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(receipt #> '{mapping,limitations}') limitation
            WHERE jsonb_typeof(limitation) <> 'string' OR octet_length(limitation #>> '{}') NOT BETWEEN 1 AND 2000 OR btrim(limitation #>> '{}') = '') THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements recursive reviewed mapping witness or limitation is invalid';
    END IF;
    IF receipt ->> 'contract_version' IS DISTINCT FROM 'canonical-implements-recursive-review-receipt/v2'
        OR receipt ->> 'id' IS DISTINCT FROM admission.receipt_id
        OR receipt ->> 'authority' IS DISTINCT FROM 'review_asserted_lab_receipt'
        OR receipt ->> 'direction' IS DISTINCT FROM 'specification_claim_to_implementation_claim'
        OR receipt ->> 'semantic_effect' IS DISTINCT FROM 'direct_structural_navigation_only'
        OR receipt ->> 'policy_effect' IS DISTINCT FROM 'no_truth_support_status_supersession_or_currentness_effect'
        OR receipt ->> 'reviewer_id' IS DISTINCT FROM admission.reviewer_id
        OR receipt ->> 'decision_reason' IS DISTINCT FROM admission.decision_reason
        OR receipt #>> '{subject,basis_id}' IS DISTINCT FROM admission.basis_id
        OR receipt #>> '{subject,report_id}' IS DISTINCT FROM admission.report_id
        OR receipt #>> '{subject,candidate_id}' IS DISTINCT FROM admission.candidate_id
        OR receipt #>> '{subject,display_id}' IS DISTINCT FROM admission.display_id
        OR receipt #>> '{display,id}' IS DISTINCT FROM admission.display_id
        OR receipt #>> '{display,contract_version}' IS DISTINCT FROM 'canonical-implements-recursive-review-display/v2'
        OR receipt #>> '{display,media_type}' IS DISTINCT FROM admission.display_media_type
        OR receipt #>> '{display,payload_utf8}' IS DISTINCT FROM admission.display_payload_utf8
        OR receipt #> '{projected_edge}' IS DISTINCT FROM jsonb_build_object(
            'id', admission.canonical_edge_id,
            'from', admission.specification_node_id,
            'to', admission.implementation_node_id,
            'relation', 'implements',
            'provenance_receipt_id', admission.receipt_id)
        OR packet #>> '{basis,id}' IS DISTINCT FROM admission.basis_id
        OR packet #>> '{basis,contract_version}' IS DISTINCT FROM 'canonical-implements-recursive-review-basis/v2'
        OR packet #>> '{basis,root_node_id}' IS DISTINCT FROM admission.specification_node_id
        OR packet #>> '{basis,admitted_cut_id}' IS DISTINCT FROM receipt #>> '{subject,admitted_cut_id}'
        OR packet #>> '{cut,id}' IS DISTINCT FROM receipt #>> '{subject,admitted_cut_id}'
        OR packet #>> '{report,id}' IS DISTINCT FROM admission.report_id
        OR COALESCE(receipt #>> '{subject,admitted_cut_id}', '') !~ '^canonical-implements-cut:v1:sha256:[0-9a-f]{64}$'
        OR COALESCE(receipt #>> '{subject,review_package_id}', '') !~ '^canonical-implements-recursive-package:v2:sha256:[0-9a-f]{64}$'
        OR display ->> 'contract_version' IS DISTINCT FROM 'canonical-implements-recursive-review-display/v2'
        OR display ->> 'id' IS DISTINCT FROM receipt #>> '{subject,review_package_id}'
        OR display ->> 'basis_id' IS DISTINCT FROM admission.basis_id
        OR display ->> 'report_id' IS DISTINCT FROM admission.report_id
        OR display #>> '{candidate,id}' IS DISTINCT FROM admission.candidate_id
        OR display #>> '{candidate,specification,node_id}' IS DISTINCT FROM admission.specification_node_id
        OR display #>> '{candidate,implementation,node_id}' IS DISTINCT FROM admission.implementation_node_id
        OR display -> 'mapping' IS DISTINCT FROM receipt -> 'mapping'
        OR display -> 'rules' IS DISTINCT FROM packet #> '{basis,rules}'
        OR display -> 'ancestors' IS DISTINCT FROM packet #> '{basis,ancestors}' THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements receipt, display, and independent basis disagree';
    END IF;

    SELECT * INTO specification FROM canonical_graph_nodes
    WHERE canonical_node_id = admission.specification_node_id;
    SELECT * INTO implementation FROM canonical_graph_nodes
    WHERE canonical_node_id = admission.implementation_node_id;
    SELECT * INTO derivation FROM canonical_derivations
    WHERE node_id = admission.specification_node_id;
    IF specification.node_kind IS DISTINCT FROM 'derived_claim'
        OR implementation.node_kind IS DISTINCT FROM 'source_claim'
        OR specification.origin_proposal_occurrence_id IS DISTINCT FROM admission.origin_proposal_occurrence_id
        OR derivation.origin_proposal_occurrence_id IS DISTINCT FROM admission.origin_proposal_occurrence_id
        OR NOT EXISTS (
            SELECT 1 FROM proposal_occurrences p JOIN admission_decisions d USING (proposal_occurrence_id)
            WHERE p.proposal_occurrence_id = admission.origin_proposal_occurrence_id
                AND p.admission_outcome = 'admitted' AND d.outcome = 'admitted'
                AND p.canonical_ref = admission.specification_node_id
                AND d.canonical_ref = admission.specification_node_id
        ) OR NOT EXISTS (
            SELECT 1 FROM proposal_occurrences p JOIN admission_decisions d USING (proposal_occurrence_id)
            WHERE p.proposal_occurrence_id = implementation.origin_proposal_occurrence_id
                AND p.admission_outcome = 'admitted' AND d.outcome = 'admitted'
                AND p.canonical_ref = admission.implementation_node_id
                AND d.canonical_ref = admission.implementation_node_id
                AND p.proposed_payload ? 'code_fact'
                AND implementation.payload ->> 'source_type' = 'code_repository'
        ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements endpoints lack admitted derived and code-fact authority';
    END IF;
    PERFORM canonical_implements_recursive_basis_assert_v2(admission.specification_node_id, packet -> 'basis');
    IF jsonb_array_length(display -> 'source_displays') IS DISTINCT FROM jsonb_array_length(packet #> '{basis,source_leaves}')
        OR EXISTS (
            SELECT 1 FROM jsonb_array_elements(packet #> '{basis,source_leaves}') leaf
            WHERE (SELECT count(*) FROM jsonb_array_elements(display -> 'source_displays') source_display
                WHERE source_display ->> 'node_id' = leaf ->> 'specification_node_id'
                    AND source_display #> '{subject,review_subject}' = leaf #> '{review_snapshot,exact_review_subject}'
                    AND source_display #>> '{subject,review_display_artifact_id}' = source_display #>> '{display,review_display_artifact_id}'
                    AND source_display #>> '{display,contract_version}' = 'review-package-display/v1'
                    AND source_display #>> '{display,media_type}' = 'application/vnd.ahe.review-package.v1+json'
                    AND (source_display #>> '{display,payload_utf8}')::jsonb = leaf #> '{review_snapshot,review_package}') <> 1
        ) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements recursive source display differs from its exact leaf review';
    END IF;
    expected_edge_id := 'canon-edge:' || substr(encode(sha256(
        convert_to(admission.specification_node_id, 'UTF8') || '\x00'::BYTEA ||
        convert_to(admission.implementation_node_id, 'UTF8') || '\x00'::BYTEA ||
        convert_to('implements', 'UTF8') || '\x00'::BYTEA), 'hex'), 1, 16);
    SELECT * INTO edge FROM canonical_graph_edges WHERE canonical_edge_id = admission.canonical_edge_id;
    IF NOT FOUND OR admission.canonical_edge_id <> expected_edge_id
        OR edge.from_node_id <> admission.specification_node_id
        OR edge.to_node_id <> admission.implementation_node_id OR edge.relation <> 'implements'
        OR edge.origin_proposal_occurrence_id <> admission.origin_proposal_occurrence_id
        OR edge.provenance <> jsonb_build_object(
            'id', 'provenance:' || admission.canonical_edge_id,
            'origin_refs', jsonb_build_array(admission.specification_node_id, admission.implementation_node_id, admission.origin_proposal_occurrence_id),
            'origin_group_id', admission.canonical_edge_id, 'producer', 'ahe-wrap',
            'method', 'reviewed_derived_implements', 'method_version', 'v2',
            'trace_ref', admission.request_id, 'review_ref', admission.receipt_id)
        OR EXISTS (SELECT 1 FROM canonical_ordinary_admission_edge_bindings WHERE canonical_edge_id = admission.canonical_edge_id)
        OR EXISTS (SELECT 1 FROM canonical_contradiction_admission_decisions WHERE canonical_edge_id = admission.canonical_edge_id) THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements edge differs from its sole reviewed receipt authority';
    END IF;
END $$;

-- Preserve the v1 assertion body byte-for-byte; dispatch only by the native
-- envelope version. Unknown versions fail closed, including on direct SQL.
CREATE FUNCTION canonical_implements_admission_assert_current(checked_request_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    version TEXT;
BEGIN
    SELECT contract_version INTO version FROM canonical_implements_admissions
    WHERE request_id = checked_request_id;
    CASE version
        WHEN 'canonical-derived-implements-admission/v1' THEN
            PERFORM canonical_implements_admission_assert_v1(checked_request_id);
        WHEN 'canonical-derived-implements-admission/v2' THEN
            PERFORM canonical_implements_admission_assert_v2(checked_request_id);
        ELSE
            RAISE EXCEPTION USING ERRCODE = '23514',
                MESSAGE = 'canonical implements admission has a missing or unsupported contract version';
    END CASE;
END $$;

CREATE OR REPLACE FUNCTION canonical_implements_admission_assert_edge_v1(checked_edge_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    checked_request_id TEXT;
    authority_count BIGINT;
BEGIN
    SELECT count(*), min(request_id) INTO authority_count, checked_request_id
    FROM canonical_implements_admissions WHERE canonical_edge_id = checked_edge_id;
    IF authority_count <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = format(
            'canonical implements edge admission authority count is %s, expected 1', authority_count);
    END IF;
    PERFORM canonical_implements_admission_assert_current(checked_request_id);
END $$;


CREATE OR REPLACE FUNCTION canonical_implements_admission_trigger_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME <> 'canonical_implements_admissions' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING ERRCODE = '23514',
            MESSAGE = 'canonical implements admission trigger has an invalid table or event context';
    END IF;
    -- TG_TABLE_SCHEMA comes from the installed trigger, never caller arguments.
    -- pg_temp is explicit and last; the function SET restores caller search_path
    -- on return, including when a nested assertion raises an exception.
    PERFORM pg_catalog.set_config('search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA), true);
    PERFORM canonical_implements_admission_assert_current(
        CASE WHEN TG_OP = 'DELETE' THEN OLD.request_id ELSE NEW.request_id END);
    RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION canonical_ordinary_admission_assert_edge_v1(
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
    IF edge_relation = 'implements' THEN
        PERFORM canonical_implements_admission_assert_edge_v1(checked_edge_id);
        RETURN;
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
REVOKE EXECUTE ON FUNCTION canonical_implements_admission_assert_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_implements_admission_assert_v2(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_implements_admission_assert_current(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_implements_admission_assert_edge_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_implements_recursive_basis_assert_v2(TEXT, JSONB) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_implements_admission_trigger_v1() FROM PUBLIC;
