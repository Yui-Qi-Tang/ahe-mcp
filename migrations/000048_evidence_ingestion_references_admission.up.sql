-- References are independently reviewed navigation. Existing source admission
-- mutation lists remain unchanged; representation is not writer authority.
LOCK TABLE canonical_graph_edges, canonical_graph_nodes, admission_decisions,
    proposal_occurrences, canonical_supersession_admission_events IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    references_count BIGINT;
BEGIN
    SELECT count(*) INTO references_count FROM canonical_graph_edges WHERE relation = 'references';
    IF references_count <> 0 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = format(
            'migration 000048 references preflight failed: references_edges=%s', references_count);
    END IF;
END $$;

CREATE TABLE canonical_references_admissions (
    request_id TEXT CONSTRAINT canonical_references_admissions_pkey PRIMARY KEY,
    contract_version TEXT NOT NULL,
    receipt_id TEXT NOT NULL CONSTRAINT canonical_references_admissions_receipt_uq UNIQUE,
    canonical_edge_id TEXT NOT NULL CONSTRAINT canonical_references_admissions_edge_uq UNIQUE,
    from_node_id TEXT NOT NULL CONSTRAINT canonical_references_admissions_from_fk REFERENCES canonical_graph_nodes(canonical_node_id),
    to_node_id TEXT NOT NULL CONSTRAINT canonical_references_admissions_to_fk REFERENCES canonical_graph_nodes(canonical_node_id),
    origin_proposal_occurrence_id TEXT NOT NULL CONSTRAINT canonical_references_admissions_origin_fk REFERENCES proposal_occurrences(proposal_occurrence_id),
    reviewer_id TEXT NOT NULL,
    decision_reason TEXT NOT NULL,
    producer_session_ref TEXT NOT NULL DEFAULT '',
    review_subject_id TEXT NOT NULL,
    cut_id TEXT NOT NULL,
    request_payload_utf8 TEXT NOT NULL,
    request_payload_hash TEXT NOT NULL,
    review_payload_utf8 TEXT NOT NULL,
    review_payload_hash TEXT NOT NULL,
    receipt_payload_utf8 TEXT NOT NULL,
    receipt_payload_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT canonical_references_admissions_pair_uq UNIQUE(from_node_id, to_node_id),
    CONSTRAINT canonical_references_admissions_edge_fk FOREIGN KEY(canonical_edge_id)
        REFERENCES canonical_graph_edges(canonical_edge_id) DEFERRABLE INITIALLY DEFERRED
);

-- Length-prefixed UTF-8 fields match the Go identity framing without depending
-- on PostgreSQL JSON formatting, key order, escaping, or a caller's hash label.
CREATE FUNCTION canonical_references_framed_hash_v1(parts TEXT[])
RETURNS TEXT
LANGUAGE plpgsql
AS $$
DECLARE
    framed BYTEA := ''::bytea;
    part TEXT;
BEGIN
    IF parts IS NULL OR cardinality(parts) NOT BETWEEN 1 AND 80 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'references identity requires bounded exact fields';
    END IF;
    FOREACH part IN ARRAY parts LOOP
        IF part IS NULL OR octet_length(part) > 524288 THEN
            RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'references identity field is missing or oversized';
        END IF;
        framed := framed || convert_to(octet_length(convert_to(part, 'UTF8'))::text || ':' || part, 'UTF8');
    END LOOP;
    RETURN encode(sha256(framed), 'hex');
END $$;


-- Encode only the frozen typed DTOs, with Go encoding/json field order and
-- escaping. This is not a general JSON canonicalization or caller-defined schema.
CREATE FUNCTION canonical_references_json_v1(value JSONB, shape TEXT)
RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    keys TEXT[];
    key TEXT;
    item JSONB;
    child JSONB;
    child_shape TEXT;
    encoded TEXT := '{';
    part TEXT;
    parts TEXT[];
    count_keys INT := 0;
BEGIN
    IF value IS NULL OR jsonb_typeof(value) IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON requires an object';
    END IF;
    keys := CASE shape
        WHEN 'request' THEN ARRAY['resolver_profile','from_node_id','to_node_id','reference','target']
        WHEN 'reference' THEN ARRAY['source_snapshot_id','extraction_view_id','span_id','start_byte','end_byte','token','token_hash']
        WHEN 'target' THEN ARRAY['source_snapshot_id','extraction_view_id','span_id','anchor_id','span_hash']
        WHEN 'cut' THEN ARRAY['contract_version','source_snapshot_id','extraction_view_id','span_id','start_byte','end_byte','span_hash','candidate_ids']
        WHEN 'resolution' THEN ARRAY['contract_version','resolver_profile','from_node_id','to_node_id','reference','target','target_cut']
        WHEN 'endpoint' THEN ARRAY['node_id','origin_proposal_occurrence_id','claim','source_snapshot_id','extraction_view_id','source_system','source_id','source_version','raw_content_hash','rendered_content_hash','title','location','coverage','limitations','source_refs']
        WHEN 'source_ref' THEN ARRAY['target_kind','extraction_view_id','repository_snapshot_id','file_snapshot_id','repo_id','commit_sha','path','span_id','start_byte','end_byte','quoted_text_hash','quoted_text']
        WHEN 'review' THEN ARRAY['contract_version','request','resolution','from','to','relation','effect','limitations']
        WHEN 'receipt' THEN ARRAY['contract_version','receipt_id','request_id','review_subject_id','cut_id','canonical_edge_id','from_node_id','to_node_id','origin_proposal_occurrence_id','reviewer_id','decision_reason','request_payload_hash','review_payload_hash']
        ELSE NULL END;
    IF keys IS NULL THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON shape is unsupported';
    END IF;
    FOREACH key IN ARRAY keys LOOP
        IF shape='source_ref' AND key=ANY(ARRAY['target_kind','extraction_view_id','repository_snapshot_id','file_snapshot_id','repo_id','commit_sha','path']) AND NOT value ? key THEN
            CONTINUE;
        END IF;
        item := value->key;
        IF item IS NULL THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON field is missing';
        END IF;
        child_shape := CASE
            WHEN key='request' THEN 'request'
            WHEN key='resolution' THEN 'resolution'
            WHEN key='reference' THEN 'reference'
            WHEN key='target' THEN 'target'
            WHEN key='target_cut' THEN 'cut'
            WHEN shape='review' AND key IN ('from','to') THEN 'endpoint'
            ELSE NULL END;
        IF child_shape IS NOT NULL THEN
            part := canonical_references_json_v1(item, child_shape);
        ELSIF key IN ('limitations','candidate_ids','source_refs') THEN
            IF jsonb_typeof(item) IS DISTINCT FROM 'array' OR jsonb_array_length(item)>64 THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON array is invalid or oversized';
            END IF;
            parts := ARRAY[]::text[];
            FOR child IN SELECT * FROM jsonb_array_elements(item) LOOP
                IF key='source_refs' THEN
                    parts := array_append(parts,canonical_references_json_v1(child,'source_ref'));
                ELSE
                    IF jsonb_typeof(child) IS DISTINCT FROM 'string' THEN
                        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON array requires strings';
                    END IF;
                    parts := array_append(parts,replace(replace(replace(replace(replace(to_json(child#>>'{}')::text,
                        '&','\u0026'),'<','\u003c'),'>','\u003e'),U&'\2028','\u2028'),U&'\2029','\u2029'));
                END IF;
            END LOOP;
            part := '['||array_to_string(parts,',')||']';
        ELSIF key IN ('start_byte','end_byte') THEN
            IF jsonb_typeof(item) IS DISTINCT FROM 'number' OR item::text !~ '^(0|[1-9][0-9]{0,9})$' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON offset requires a bounded integer';
            END IF;
            part := item::text;
        ELSE
            IF jsonb_typeof(item) IS DISTINCT FROM 'string' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON field requires a string';
            END IF;
            IF shape='source_ref' AND key=ANY(ARRAY['target_kind','extraction_view_id','repository_snapshot_id','file_snapshot_id','repo_id','commit_sha','path']) AND item#>>'{}'='' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON optional field must be omitted when empty';
            END IF;
            part := replace(replace(replace(replace(replace(to_json(item#>>'{}')::text,
                '&','\u0026'),'<','\u003c'),'>','\u003e'),U&'\2028','\u2028'),U&'\2029','\u2029');
        END IF;
        IF count_keys>0 THEN encoded:=encoded||','; END IF;
        encoded:=encoded||to_json(key)::text||':'||part;
        count_keys:=count_keys+1;
        IF octet_length(encoded)>524288 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON exceeds payload bound';
        END IF;
    END LOOP;
    IF (SELECT count(*) FROM jsonb_object_keys(value))<>count_keys THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references typed JSON contains unexpected fields';
    END IF;
    RETURN encoded||'}';
END $$;

-- Stable retained authority: no endpoint replay and no current target universe.
-- This permits historical exact retry and independent A->B->A navigation cycles.
CREATE FUNCTION canonical_references_assert_edge_v1(checked_edge_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    a canonical_references_admissions%ROWTYPE;
    edge canonical_graph_edges%ROWTYPE;
    request JSONB;
    review JSONB;
    receipt JSONB;
    cut JSONB;
    expected_receipt JSONB;
    expected_resolution JSONB;
    expected_provenance JSONB;
    expected_receipt_id TEXT;
    expected_cut_id TEXT;
    field RECORD;
    trim_chars TEXT := E' \t\n\r\f\013' || U&'\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000';
BEGIN
    SELECT * INTO a FROM canonical_references_admissions WHERE canonical_edge_id=checked_edge_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references edge lacks its independent reviewed receipt';
    END IF;
    SELECT * INTO edge FROM canonical_graph_edges WHERE canonical_edge_id=checked_edge_id;
    IF NOT FOUND OR a.contract_version<>'canonical-references-admission/v1'
        OR edge.relation<>'references' OR a.from_node_id=a.to_node_id
        OR edge.from_node_id<>a.from_node_id OR edge.to_node_id<>a.to_node_id
        OR edge.origin_proposal_occurrence_id<>a.origin_proposal_occurrence_id
        OR a.canonical_edge_id<>'canon-edge:'||canonical_references_framed_hash_v1(
            ARRAY['canonical-references-edge/v1',a.from_node_id,a.to_node_id]) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references receipt differs from its exact directed edge';
    END IF;
    FOR field IN SELECT * FROM (VALUES
        (a.request_id,200,false),(a.reviewer_id,200,false),(a.decision_reason,2000,false),
        (a.producer_session_ref,2000,true),(a.from_node_id,512,false),(a.to_node_id,512,false)
    ) fields(value,max_bytes,optional) LOOP
        IF octet_length(field.value)>field.max_bytes OR btrim(field.value,trim_chars)<>field.value
            OR (NOT field.optional AND field.value='') THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references receipt identity or audit field is invalid';
        END IF;
    END LOOP;
    FOR field IN SELECT * FROM (VALUES
        (a.request_payload_utf8,a.request_payload_hash,16384,'request'),
        (a.review_payload_utf8,a.review_payload_hash,524288,'review'),
        (a.receipt_payload_utf8,a.receipt_payload_hash,524288,'receipt')
    ) fields(payload,hash,max_bytes,shape) LOOP
        IF octet_length(field.payload) NOT BETWEEN 1 AND field.max_bytes
            OR field.hash IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to(field.payload,'UTF8')),'hex')
            OR field.payload IS DISTINCT FROM canonical_references_json_v1(field.payload::jsonb,field.shape) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references payload lacks exact typed bytes and hash';
        END IF;
    END LOOP;
    request:=a.request_payload_utf8::jsonb;
    review:=a.review_payload_utf8::jsonb;
    receipt:=a.receipt_payload_utf8::jsonb;
    cut:=review#>'{resolution,target_cut}';
    IF request->>'from_node_id' IS DISTINCT FROM a.from_node_id
        OR request->>'to_node_id' IS DISTINCT FROM a.to_node_id
        OR review->'request' IS DISTINCT FROM request
        OR review->>'contract_version' IS DISTINCT FROM 'canonical-references-review/v1'
        OR review->>'relation' IS DISTINCT FROM 'references'
        OR review->>'effect' IS DISTINCT FROM 'direct_structural_navigation_only'
        OR review->'limitations' IS DISTINCT FROM jsonb_build_array(
            'observed_target_uniqueness_not_permanent_or_commit_latest',
            'no_truth_support_dependency_status_supersession_or_currentness_effect',
            'reviewer_and_display_are_asserted_not_authenticated')
        OR cut->>'contract_version' IS DISTINCT FROM 'canonical-references-target-cut/v1'
        OR cut->'candidate_ids' IS DISTINCT FROM jsonb_build_array(a.to_node_id)
        OR cut->>'source_snapshot_id' IS DISTINCT FROM request#>>'{target,source_snapshot_id}'
        OR cut->>'extraction_view_id' IS DISTINCT FROM request#>>'{target,extraction_view_id}'
        OR cut->>'span_id' IS DISTINCT FROM request#>>'{target,span_id}'
        OR cut->>'span_hash' IS DISTINCT FROM request#>>'{target,span_hash}'
        OR review#>>'{from,node_id}' IS DISTINCT FROM a.from_node_id
        OR review#>>'{from,origin_proposal_occurrence_id}' IS DISTINCT FROM a.origin_proposal_occurrence_id
        OR review#>>'{to,node_id}' IS DISTINCT FROM a.to_node_id
        OR review#>>'{from,source_snapshot_id}' IS DISTINCT FROM request#>>'{reference,source_snapshot_id}'
        OR review#>>'{from,extraction_view_id}' IS DISTINCT FROM request#>>'{reference,extraction_view_id}'
        OR review#>>'{to,source_snapshot_id}' IS DISTINCT FROM request#>>'{target,source_snapshot_id}'
        OR review#>>'{to,extraction_view_id}' IS DISTINCT FROM request#>>'{target,extraction_view_id}'
        OR jsonb_array_length(review#>'{from,source_refs}') NOT BETWEEN 1 AND 64
        OR jsonb_array_length(review#>'{to,source_refs}')<>1
        OR review#>>'{to,source_refs,0,extraction_view_id}' IS DISTINCT FROM cut->>'extraction_view_id'
        OR review#>>'{to,source_refs,0,span_id}' IS DISTINCT FROM cut->>'span_id'
        OR review#>>'{to,source_refs,0,quoted_text_hash}' IS DISTINCT FROM cut->>'span_hash'
        OR review#>'{to,source_refs,0,start_byte}' IS DISTINCT FROM cut->'start_byte'
        OR review#>'{to,source_refs,0,end_byte}' IS DISTINCT FROM cut->'end_byte'
        OR (cut->>'end_byte')::bigint <= (cut->>'start_byte')::bigint THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references retained review coordinates or effect differ';
    END IF;
    expected_resolution:=jsonb_build_object('contract_version','canonical-references-resolution/v1',
        'resolver_profile',request->'resolver_profile','from_node_id',a.from_node_id,'to_node_id',a.to_node_id,
        'reference',request->'reference','target',request->'target','target_cut',cut);
    expected_cut_id:='references-cut:v1:sha256:'||canonical_references_framed_hash_v1(ARRAY[
        cut->>'contract_version',cut->>'source_snapshot_id',cut->>'extraction_view_id',cut->>'span_id',
        cut->>'start_byte',cut->>'end_byte',cut->>'span_hash',a.to_node_id]);
    IF review->'resolution' IS DISTINCT FROM expected_resolution OR a.cut_id<>expected_cut_id
        OR a.review_subject_id IS DISTINCT FROM 'references-review:v1:sha256:'||encode(sha256(convert_to(
            'application/vnd.ahe.references-review.v1+json'||E'\n'||a.review_payload_utf8,'UTF8')),'hex') THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references retained resolution, cut or subject hash differs';
    END IF;
    expected_receipt_id:='references-receipt:v1:sha256:'||canonical_references_framed_hash_v1(ARRAY[
        a.contract_version,a.request_id,a.review_subject_id,a.cut_id,a.canonical_edge_id,a.from_node_id,a.to_node_id,
        a.origin_proposal_occurrence_id,a.reviewer_id,a.decision_reason,a.request_payload_hash,a.review_payload_hash]);
    expected_receipt:=jsonb_build_object('contract_version',a.contract_version,'receipt_id',expected_receipt_id,
        'request_id',a.request_id,'review_subject_id',a.review_subject_id,'cut_id',a.cut_id,
        'canonical_edge_id',a.canonical_edge_id,'from_node_id',a.from_node_id,'to_node_id',a.to_node_id,
        'origin_proposal_occurrence_id',a.origin_proposal_occurrence_id,'reviewer_id',a.reviewer_id,
        'decision_reason',a.decision_reason,'request_payload_hash',a.request_payload_hash,'review_payload_hash',a.review_payload_hash);
    expected_provenance:=jsonb_build_object('id','provenance:'||a.canonical_edge_id,
        'origin_refs',jsonb_build_array(a.from_node_id,a.to_node_id,a.origin_proposal_occurrence_id),
        'origin_group_id',a.canonical_edge_id,'producer','ahe-wrap','method','reviewed_references',
        'method_version','v1','trace_ref',a.request_id,'review_ref',a.receipt_id);
    IF a.receipt_id<>expected_receipt_id OR receipt IS DISTINCT FROM expected_receipt
        OR edge.provenance IS DISTINCT FROM expected_provenance THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references receipt or complete native provenance differs';
    END IF;
END $$;

-- This predicate grants no source admission. It recognizes only the separately
-- retained relation receipt when an old source event replays its original list.
CREATE FUNCTION canonical_references_origin_edge_v1(checked_edge_id TEXT, checked_proposal_id TEXT, checked_node_id TEXT)
RETURNS BOOLEAN LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM canonical_graph_edges e JOIN canonical_references_admissions a
        ON a.canonical_edge_id=e.canonical_edge_id
        WHERE e.canonical_edge_id=checked_edge_id AND e.relation='references'
            AND e.origin_proposal_occurrence_id=checked_proposal_id AND e.from_node_id=checked_node_id
            AND a.origin_proposal_occurrence_id=checked_proposal_id AND a.from_node_id=checked_node_id) THEN
        RETURN false;
    END IF;
    PERFORM canonical_references_assert_edge_v1(checked_edge_id);
    RETURN true;
END $$;


-- Decode complete immutable identity-rendered lines, not caller-provided spans.
-- True enables the closed anchor grammar over every target line, including
-- malformed or duplicate anchors that occur after the requested target.
CREATE FUNCTION canonical_references_catalog_v1(checked_view_id TEXT, scan_anchors BOOLEAN)
RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE
    view extraction_views%ROWTYPE;
    source_kind TEXT;
    catalog TEXT;
    raw BYTEA;
    span span_catalog_entries%ROWTYPE;
    start_pos INT:=0;
    end_pos INT;
    content_end INT;
    next_newline INT;
    line_number INT:=1;
    span_number INT:=0;
    span_count BIGINT;
    line_text TEXT;
    line_hash TEXT;
    marker_pos INT;
    close_pos INT;
    suffix TEXT;
    anchor_id TEXT;
    anchors JSONB:='{}'::jsonb;
    trim_chars TEXT:=E' \t\n\r\f\013'||U&'\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000';
BEGIN
    SELECT count(*) INTO span_count FROM span_catalog_entries WHERE extraction_view_id=checked_view_id;
    IF span_count>4096 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references complete catalog exceeds span bound';
    END IF;
    SELECT * INTO view FROM extraction_views WHERE extraction_view_id=checked_view_id
        AND octet_length(rendered_content)<=1048576;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references exact bounded view is unavailable';
    END IF;
    SELECT s.source_system,b.raw_content INTO source_kind,raw
        FROM source_snapshots s JOIN source_blobs b USING(raw_content_hash)
        WHERE s.source_snapshot_id=view.source_snapshot_id
            AND octet_length(b.raw_content)<=1048576
            AND b.byte_length=octet_length(b.raw_content)
            AND s.raw_content_hash='sha256:'||encode(sha256(b.raw_content),'hex');
    catalog:=CASE source_kind WHEN 'manual_text' THEN 'manual-line-v1'
        WHEN 'external_document' THEN 'external-document-line-v1' ELSE NULL END;
    IF raw IS NULL OR catalog IS NULL OR raw<>view.rendered_content
        OR view.rendered_content_hash<>'sha256:'||encode(sha256(raw),'hex')
        OR view.renderer_version<>'v1'
        OR view.renderer_name<>(CASE source_kind WHEN 'manual_text' THEN 'manual-text-identity' ELSE 'external-document-identity' END) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references source is not its exact identity-rendered view';
    END IF;
    -- convert_from also rejects invalid UTF-8 and NUL, before byte offsets are used.
    PERFORM convert_from(raw,'UTF8');
    WHILE start_pos<=octet_length(raw) LOOP
        next_newline:=position(decode('0a','hex') IN substring(raw FROM start_pos+1));
        end_pos:=CASE WHEN next_newline=0 THEN octet_length(raw) ELSE start_pos+next_newline-1 END;
        content_end:=end_pos;
        IF content_end>start_pos AND get_byte(raw,content_end-1)=13 THEN content_end:=content_end-1; END IF;
        IF content_end>start_pos THEN
            span_number:=span_number+1;
            IF span_number>4096 OR content_end-start_pos>8192 THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references exact line exceeds complete catalog bounds';
            END IF;
            line_text:=convert_from(substring(raw FROM start_pos+1 FOR content_end-start_pos),'UTF8');
            line_hash:='sha256:'||encode(sha256(convert_to(line_text,'UTF8')),'hex');
            SELECT * INTO span FROM span_catalog_entries WHERE extraction_view_id=checked_view_id
                AND span_id='span:S'||span_number::text;
            IF NOT FOUND OR span.span_catalog_version<>catalog OR span.start_byte<>start_pos
                OR span.end_byte<>content_end OR span.display_line<>line_number
                OR span.quoted_text_hash<>line_hash OR span.quoted_text<>convert_to(line_text,'UTF8') THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references complete line catalog differs from native bytes';
            END IF;
            marker_pos:=position('[[ahe-anchor:' IN line_text);
            IF scan_anchors AND marker_pos>0 THEN
                IF marker_pos<>1 OR (length(line_text)-length(replace(line_text,'[[ahe-anchor:','')))/length('[[ahe-anchor:')<>1 THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references anchor marker must occur once at line start';
                END IF;
                suffix:=substring(line_text FROM length('[[ahe-anchor:')+1);
                close_pos:=position(']]' IN suffix);
                anchor_id:=substring(suffix FROM 1 FOR greatest(close_pos-1,0));
                IF close_pos=0 OR anchor_id COLLATE "C" !~ '^[A-Za-z][A-Za-z0-9._-]{0,63}$'
                    OR substring(suffix FROM close_pos+2 FOR 1)<>' '
                    OR btrim(substring(suffix FROM close_pos+3),trim_chars)='' THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references anchor marker has malformed ID or missing content';
                END IF;
                IF anchors ? anchor_id THEN
                    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references anchor ID is duplicated in complete target view';
                END IF;
                anchors:=anchors||jsonb_build_object(anchor_id,jsonb_build_object('span_id',span.span_id,
                    'start_byte',start_pos,'end_byte',content_end,'span_hash',line_hash));
            END IF;
        END IF;
        EXIT WHEN end_pos=octet_length(raw);
        start_pos:=end_pos+1;
        line_number:=line_number+1;
    END LOOP;
    IF span_number<>span_count THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references complete catalog contains missing or extra lines';
    END IF;
    RETURN anchors;
END $$;

-- Reconstruct one native first-materialized source card. No relation traversal,
-- no current head selection and no caller-origin fallback are permitted.
CREATE FUNCTION canonical_references_endpoint_v1(checked_node_id TEXT)
RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE
    node canonical_graph_nodes%ROWTYPE;
    proposal proposal_occurrences%ROWTYPE;
    attempt extraction_attempts%ROWTYPE;
    run extraction_runs%ROWTYPE;
    source source_snapshots%ROWTYPE;
    view extraction_views%ROWTYPE;
    profile JSONB;
    title TEXT;
    location TEXT;
    coverage TEXT;
    limits JSONB;
    ref JSONB;
    exact_ref JSONB;
    refs JSONB;
    item JSONB;
    event_id TEXT;
    event_count BIGINT;
    decision_id TEXT;
    trim_chars TEXT:=E' \t\n\r\f\013'||U&'\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000';
BEGIN
    SELECT * INTO node FROM canonical_graph_nodes n WHERE n.canonical_node_id=checked_node_id
        AND n.node_kind='source_claim'
        AND octet_length(n.payload::text)+octet_length(n.provenance::text)+octet_length(n.temporal::text)+octet_length(n.integrity::text)<=131072;
    IF NOT FOUND OR checked_node_id NOT LIKE 'canon-node:%' OR octet_length(checked_node_id)>512 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references endpoint requires bounded native source_claim';
    END IF;
    SELECT * INTO proposal FROM proposal_occurrences p WHERE p.proposal_occurrence_id=node.origin_proposal_occurrence_id
        AND p.admission_outcome='admitted' AND p.canonical_ref=checked_node_id
        AND jsonb_typeof(p.source_refs)='array' AND jsonb_array_length(p.source_refs) BETWEEN 1 AND 64
        AND octet_length(p.source_refs::text)+octet_length(p.proposed_payload::text)
            +octet_length(node.payload::text)+octet_length(node.provenance::text)+octet_length(node.temporal::text)+octet_length(node.integrity::text)<=131072;
    IF NOT FOUND OR node.payload->>'claim' IS DISTINCT FROM proposal.statement_text
        OR EXISTS(SELECT 1 FROM canonical_derivations WHERE node_id=checked_node_id)
        OR EXISTS(SELECT 1 FROM canonical_graph_edges WHERE to_node_id=checked_node_id AND relation='derived_from') THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references endpoint differs from its exact admitted source origin';
    END IF;
    PERFORM canonical_ordinary_admission_assert_node_v1(checked_node_id);
    SELECT count(*),min(e.event_id) INTO event_count,event_id
        FROM canonical_supersession_admission_events e WHERE e.proposal_occurrence_id=proposal.proposal_occurrence_id;
    IF event_count>0 THEN
        IF event_count<>1 THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references source origin has ambiguous supersession events';
        END IF;
        PERFORM canonical_references_source_event_assert_v1(event_id);
    ELSE
        SELECT b.admission_decision_id INTO decision_id
            FROM canonical_ordinary_admission_node_bindings b
            JOIN canonical_ordinary_admission_manifests m USING(admission_decision_id)
            WHERE b.canonical_node_id=checked_node_id AND b.materialization='materialized'
                AND m.proposal_occurrence_id=proposal.proposal_occurrence_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references source lacks ordinary first-materializer authority';
        END IF;
        PERFORM canonical_ordinary_admission_assert_decision_v1(decision_id);
    END IF;
    SELECT * INTO attempt FROM extraction_attempts a WHERE a.extraction_attempt_id=proposal.extraction_attempt_id
        AND a.status='succeeded' AND a.fixture_output IS NOT NULL AND octet_length(a.fixture_output::text)<=1048576;
    IF NOT FOUND OR (SELECT count(*) FROM proposal_occurrences WHERE extraction_attempt_id=proposal.extraction_attempt_id)>205 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references original extraction attempt exceeds authority bounds';
    END IF;
    SELECT * INTO run FROM extraction_runs r WHERE r.extraction_run_id=attempt.extraction_run_id
        AND r.repository_snapshot_id IS NULL AND r.source_snapshot_id IS NOT NULL AND r.extraction_view_id IS NOT NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references endpoint source binding is unsupported';
    END IF;
    SELECT * INTO source FROM source_snapshots s WHERE s.source_snapshot_id=run.source_snapshot_id
        AND s.source_system IN ('manual_text','external_document') AND s.source_snapshot_id~'^srcsnap:[0-9a-f]{64}$';
    SELECT * INTO view FROM extraction_views v WHERE v.extraction_view_id=run.extraction_view_id
        AND v.source_snapshot_id=source.source_snapshot_id AND v.extraction_view_id LIKE 'view:%'
        AND octet_length(v.extraction_view_id)<=200 AND octet_length(v.rendered_content)<=1048576;
    IF source.source_snapshot_id IS NULL OR NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references endpoint exact native source or view is missing';
    END IF;
    IF source.source_system='manual_text' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references requires qualified external source intake; manual profiles are not enabled';
    ELSE
        IF source.origin_metadata->>'external_source_schema' IS DISTINCT FROM 'external-source-envelope-v1'
            OR source.origin_metadata->>'external_content_fidelity' IS DISTINCT FROM 'verbatim'
            OR source.origin_metadata->>'global_absence_inference_allowed' IS DISTINCT FROM 'false'
            OR NOT EXISTS(SELECT 1 FROM external_source_intake_receipts e
                WHERE e.source_snapshot_id=source.source_snapshot_id AND e.extraction_view_id=view.extraction_view_id) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references external endpoint lacks qualified intake authority';
        END IF;
        title:=source.origin_metadata->>'external_title'; location:=source.origin_metadata->>'external_source_location';
        coverage:=source.origin_metadata->>'external_coverage'; limits:=(source.origin_metadata->>'external_limitations_json')::jsonb;
        IF coverage NOT IN ('full_document','exact_excerpt','truncated_document') OR jsonb_typeof(limits) IS DISTINCT FROM 'array'
            OR jsonb_array_length(limits)>32 OR (coverage='full_document' AND jsonb_array_length(limits)<>0)
            OR (coverage<>'full_document' AND jsonb_array_length(limits)=0) THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references external source coverage or limitations differ';
        END IF;
        FOR item IN SELECT * FROM jsonb_array_elements(limits) LOOP
            IF jsonb_typeof(item)<>'string' OR octet_length(item#>>'{}') NOT BETWEEN 1 AND 500
                OR btrim(item#>>'{}',trim_chars)<>item#>>'{}' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references external limitation is invalid';
            END IF;
        END LOOP;
    END IF;
    IF title IS NULL OR location IS NULL OR octet_length(title) NOT BETWEEN 1 AND 2000
        OR octet_length(location) NOT BETWEEN 1 AND 2000 OR btrim(title,trim_chars)<>title OR btrim(location,trim_chars)<>location THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references endpoint lacks exact display title or location';
    END IF;
    FOR ref IN SELECT * FROM jsonb_array_elements(proposal.source_refs) LOOP
        SELECT jsonb_build_object('extraction_view_id',s.extraction_view_id,'span_id',s.span_id,
            'start_byte',s.start_byte,'end_byte',s.end_byte,'quoted_text_hash',s.quoted_text_hash,
            'quoted_text',convert_from(s.quoted_text,'UTF8')) INTO exact_ref
            FROM span_catalog_entries s WHERE s.extraction_view_id=view.extraction_view_id
                AND s.span_id=ref->>'span_id' AND octet_length(s.quoted_text)<=8192;
        IF NOT FOUND OR ref IS DISTINCT FROM exact_ref THEN
            RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references endpoint source refs differ from exact native spans';
        END IF;
    END LOOP;
    SELECT jsonb_agg(item.value ORDER BY (item.value->>'start_byte')::bigint,(item.value->>'end_byte')::bigint,item.value->>'quoted_text_hash' COLLATE "C")
        INTO refs FROM jsonb_array_elements(proposal.source_refs) item(value);
    IF refs IS DISTINCT FROM proposal.source_refs OR
        (SELECT count(DISTINCT item.value->>'span_id') FROM jsonb_array_elements(refs) item(value))<>jsonb_array_length(refs) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references source refs must be complete, ordered and distinct';
    END IF;
    RETURN jsonb_build_object('node_id',checked_node_id,'origin_proposal_occurrence_id',proposal.proposal_occurrence_id,
        'claim',proposal.statement_text,'source_snapshot_id',source.source_snapshot_id,'extraction_view_id',view.extraction_view_id,
        'source_system',source.source_system,'source_id',source.source_id,'source_version',source.source_version,
        'raw_content_hash',source.raw_content_hash,'rendered_content_hash',view.rendered_content_hash,
        'title',title,'location',location,'coverage',coverage,'limitations',limits,'source_refs',refs);
END $$;

-- Only first INSERT observes current candidates. A future overlapping admission
-- cannot retroactively change the retained cut, edge or receipt.
CREATE FUNCTION canonical_references_insert_assert_v1(checked_request_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    a canonical_references_admissions%ROWTYPE;
    request JSONB;
    review JSONB;
    from_card JSONB;
    to_card JSONB;
    anchors JSONB;
    target JSONB;
    expected_cut JSONB;
    candidate_count BIGINT;
    candidate_ids TEXT[];
    ref JSONB;
    token TEXT;
    start_pos INT;
    end_pos INT;
    raw BYTEA;
BEGIN
    IF current_setting('transaction_isolation') NOT IN ('repeatable read','serializable')
        OR current_setting('row_security')<>'off' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references insertion requires complete repeatable-read native visibility';
    END IF;
    SELECT * INTO a FROM canonical_references_admissions WHERE request_id=checked_request_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references insert lacks its retained request';
    END IF;
    PERFORM canonical_references_assert_edge_v1(a.canonical_edge_id);
    request:=a.request_payload_utf8::jsonb; review:=a.review_payload_utf8::jsonb;
    from_card:=canonical_references_endpoint_v1(a.from_node_id);
    to_card:=canonical_references_endpoint_v1(a.to_node_id);
    IF review->'from' IS DISTINCT FROM from_card OR review->'to' IS DISTINCT FROM to_card THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references review endpoint card differs from native admitted evidence';
    END IF;
    PERFORM canonical_references_catalog_v1(from_card->>'extraction_view_id',false);
    anchors:=canonical_references_catalog_v1(to_card->>'extraction_view_id',true);
    target:=anchors->(request#>>'{target,anchor_id}');
    IF target IS NULL OR request#>>'{reference,source_snapshot_id}' IS DISTINCT FROM from_card->>'source_snapshot_id'
        OR request#>>'{reference,extraction_view_id}' IS DISTINCT FROM from_card->>'extraction_view_id'
        OR request#>>'{target,source_snapshot_id}' IS DISTINCT FROM to_card->>'source_snapshot_id'
        OR request#>>'{target,extraction_view_id}' IS DISTINCT FROM to_card->>'extraction_view_id'
        OR request#>>'{target,span_id}' IS DISTINCT FROM target->>'span_id'
        OR request#>>'{target,span_hash}' IS DISTINCT FROM target->>'span_hash'
        OR jsonb_array_length(to_card->'source_refs')<>1
        OR to_card#>>'{source_refs,0,span_id}' IS DISTINCT FROM target->>'span_id' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references target is not the exact unique complete anchored claim';
    END IF;
    CASE request->>'resolver_profile'
        WHEN 'same-snapshot-anchor/v1' THEN
            IF from_card->>'source_snapshot_id'<>to_card->>'source_snapshot_id'
                OR from_card->>'extraction_view_id'<>to_card->>'extraction_view_id' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references local profile requires the same immutable view';
            END IF;
            token:='[[ahe-ref:#'||(request#>>'{target,anchor_id}')||']]';
        WHEN 'native-snapshot-anchor/v1' THEN
            IF from_card->>'source_snapshot_id'=to_card->>'source_snapshot_id' THEN
                RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references cross-document profile requires another exact snapshot';
            END IF;
            token:='[[ahe-ref:'||(to_card->>'source_snapshot_id')||'#'||(request#>>'{target,anchor_id}')||']]';
        ELSE RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references resolver profile is unsupported';
    END CASE;
    ref:=request->'reference'; start_pos:=(ref->>'start_byte')::int; end_pos:=(ref->>'end_byte')::int;
    SELECT rendered_content INTO raw FROM extraction_views WHERE extraction_view_id=from_card->>'extraction_view_id';
    IF ref->>'token' IS DISTINCT FROM token OR octet_length(token)>256
        OR ref->>'token_hash' IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to(token,'UTF8')),'hex')
        OR start_pos<0 OR end_pos<=start_pos OR end_pos>octet_length(raw)
        OR substring(raw FROM start_pos+1 FOR end_pos-start_pos)<>convert_to(token,'UTF8')
        OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(from_card->'source_refs') source_ref
            WHERE source_ref->>'span_id'=ref->>'span_id' AND (source_ref->>'start_byte')::int<=start_pos
                AND (source_ref->>'end_byte')::int>=end_pos) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references token lacks exact native source witness bytes';
    END IF;
    -- Count before collecting IDs; no manifest, reviewer, origin or profile filter.
    SELECT count(DISTINCT n.canonical_node_id) INTO candidate_count
        FROM canonical_graph_nodes n JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
        JOIN extraction_attempts x ON x.extraction_attempt_id=p.extraction_attempt_id
        JOIN extraction_runs r ON r.extraction_run_id=x.extraction_run_id
        CROSS JOIN LATERAL jsonb_array_elements(p.source_refs) original_ref
        WHERE n.node_kind='source_claim' AND p.admission_outcome='admitted' AND p.canonical_ref=n.canonical_node_id
            AND r.source_snapshot_id=to_card->>'source_snapshot_id' AND r.extraction_view_id=to_card->>'extraction_view_id'
            AND original_ref->>'extraction_view_id'=r.extraction_view_id
            AND (original_ref->>'start_byte')::bigint<(target->>'end_byte')::bigint
            AND (original_ref->>'end_byte')::bigint>(target->>'start_byte')::bigint;
    IF candidate_count NOT BETWEEN 1 AND 64 THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references complete target candidate universe exceeds bound or is empty';
    END IF;
    SELECT array_agg(candidate_id ORDER BY candidate_id COLLATE "C") INTO candidate_ids FROM (
        SELECT DISTINCT n.canonical_node_id AS candidate_id
        FROM canonical_graph_nodes n JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
        JOIN extraction_attempts x ON x.extraction_attempt_id=p.extraction_attempt_id
        JOIN extraction_runs r ON r.extraction_run_id=x.extraction_run_id
        CROSS JOIN LATERAL jsonb_array_elements(p.source_refs) original_ref
        WHERE n.node_kind='source_claim' AND p.admission_outcome='admitted' AND p.canonical_ref=n.canonical_node_id
            AND r.source_snapshot_id=to_card->>'source_snapshot_id' AND r.extraction_view_id=to_card->>'extraction_view_id'
            AND original_ref->>'extraction_view_id'=r.extraction_view_id
            AND (original_ref->>'start_byte')::bigint<(target->>'end_byte')::bigint
            AND (original_ref->>'end_byte')::bigint>(target->>'start_byte')::bigint
    ) candidates;
    expected_cut:=jsonb_build_object('contract_version','canonical-references-target-cut/v1',
        'source_snapshot_id',to_card->>'source_snapshot_id','extraction_view_id',to_card->>'extraction_view_id',
        'span_id',target->>'span_id','start_byte',target->'start_byte','end_byte',target->'end_byte',
        'span_hash',target->>'span_hash','candidate_ids',to_jsonb(candidate_ids));
    IF cardinality(candidate_ids)<>candidate_count OR candidate_ids IS DISTINCT FROM ARRAY[a.to_node_id]
        OR review#>'{resolution,target_cut}' IS DISTINCT FROM expected_cut THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references observed target cut is changed, ambiguous or incomplete';
    END IF;
END $$;

CREATE FUNCTION canonical_references_admission_trigger_v1()
RETURNS TRIGGER LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog SET row_security=off AS $$
BEGIN
    IF TG_TABLE_NAME<>'canonical_references_admissions' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP<>'INSERT' THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references admission trigger has invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    PERFORM canonical_references_insert_assert_v1(NEW.request_id);
    RETURN NULL;
END $$;

CREATE TRIGGER canonical_references_admissions_append_only
BEFORE UPDATE OR DELETE ON canonical_references_admissions
FOR EACH ROW EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE TRIGGER canonical_references_admissions_forbid_truncate
BEFORE TRUNCATE ON canonical_references_admissions
FOR EACH STATEMENT EXECUTE FUNCTION canonical_admission_forbid_mutation_v1();
CREATE CONSTRAINT TRIGGER canonical_references_admissions_authority
AFTER INSERT ON canonical_references_admissions DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION canonical_references_admission_trigger_v1();

-- Preserve original event validation while recognizing separately receipted
-- navigation edges. The historical source proof omits only today's head test.
CREATE FUNCTION canonical_references_source_event_assert_v1(checked_event_id TEXT)
RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    IF checked_event_id IS NULL OR NOT EXISTS (SELECT 1 FROM canonical_supersession_admission_events WHERE event_id=checked_event_id) THEN
        RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='references source origin event is missing';
    END IF;
    PERFORM canonical_supersession_assert_event(checked_event_id);
END $$;

CREATE OR REPLACE FUNCTION canonical_supersession_assert_event(checked_event_id TEXT)
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
    WHERE origin_proposal_occurrence_id = event_record.proposal_occurrence_id
      AND NOT canonical_references_origin_edge_v1(canonical_edge_id,
          event_record.proposal_occurrence_id, event_record.replacement_node_id);

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
    IF edge_relation = 'references' THEN
        PERFORM canonical_references_assert_edge_v1(checked_edge_id);
        RETURN;
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
CREATE OR REPLACE FUNCTION canonical_supersession_edge_authority_dispatch_v1()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    protected_event_id TEXT;
BEGIN
    IF TG_TABLE_NAME <> 'canonical_graph_edges' OR TG_LEVEL <> 'ROW'
        OR TG_WHEN <> 'AFTER' OR TG_OP NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession edge trigger has an invalid table or event context';
    END IF;
    PERFORM pg_catalog.set_config(
        'search_path',
        pg_catalog.format('pg_catalog, %I, pg_temp', TG_TABLE_SCHEMA),
        true
    );

    IF TG_OP <> 'INSERT' AND EXISTS (
        SELECT 1
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = OLD.origin_proposal_occurrence_id
    ) THEN
        SELECT event_id
        INTO protected_event_id
        FROM canonical_supersession_admission_events
        WHERE proposal_occurrence_id = OLD.origin_proposal_occurrence_id;
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession admission edge is immutable';
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

    IF TG_OP = 'INSERT' AND NEW.relation = 'references' THEN
        PERFORM canonical_references_assert_edge_v1(NEW.canonical_edge_id);
        RETURN NULL;
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
REVOKE EXECUTE ON FUNCTION canonical_references_framed_hash_v1(TEXT[]) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_json_v1(JSONB,TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_assert_edge_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_origin_edge_v1(TEXT,TEXT,TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_catalog_v1(TEXT,BOOLEAN) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_endpoint_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_insert_assert_v1(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_admission_trigger_v1() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION canonical_references_source_event_assert_v1(TEXT) FROM PUBLIC;
