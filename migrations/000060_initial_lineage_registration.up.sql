-- Independent first-membership authority. Existing v2 payloads/IDs/head remain unchanged.
LOCK TABLE canonical_supersession_lineages, canonical_supersession_members IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE canonical_initial_lineage_registrations (
 event_id text PRIMARY KEY CHECK (event_id ~ '^initial-lineage-registration:v1:sha256:[0-9a-f]{64}$'),
 request_id text NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200),
 claim_id text NOT NULL UNIQUE REFERENCES canonical_graph_nodes(canonical_node_id),
 lineage_key text NOT NULL UNIQUE REFERENCES canonical_supersession_lineages(lineage_key) DEFERRABLE INITIALLY DEFERRED,
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 16384),
 binding_body text NOT NULL CHECK (octet_length(binding_body) BETWEEN 1 AND 16384),
 grounding_body text NOT NULL CHECK (octet_length(grounding_body) BETWEEN 1 AND 1048576),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT canonical_initial_registration_origin_key UNIQUE(event_id,claim_id,lineage_key),
 CONSTRAINT canonical_initial_registration_digest CHECK (event_id='initial-lineage-registration:v1:sha256:' || encode(sha256(convert_to(body,'UTF8')),'hex'))
);
ALTER TABLE canonical_supersession_members ALTER COLUMN first_admission_event_id DROP NOT NULL;
ALTER TABLE canonical_supersession_members ADD COLUMN initial_registration_event_id text;
ALTER TABLE canonical_supersession_members ADD CONSTRAINT canonical_supersession_members_initial_registration_fk
 FOREIGN KEY(initial_registration_event_id,canonical_node_id,lineage_key) REFERENCES canonical_initial_lineage_registrations(event_id,claim_id,lineage_key) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE canonical_supersession_members ADD CONSTRAINT canonical_supersession_members_origin_choice
 CHECK ((initial_registration_event_id IS NULL AND first_admission_event_id IS NOT NULL)
 OR (initial_registration_event_id IS NOT NULL AND first_admission_event_id IS NULL AND NOT was_bootstrapped));
CREATE TRIGGER canonical_initial_registration_immutable_rows BEFORE UPDATE OR DELETE ON canonical_initial_lineage_registrations
 FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER canonical_initial_registration_immutable_table BEFORE TRUNCATE ON canonical_initial_lineage_registrations
 FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();
CREATE OR REPLACE FUNCTION canonical_supersession_assert_member(checked_node_id TEXT)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF checked_node_id IS NULL THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1 FROM canonical_supersession_members m
        JOIN canonical_initial_lineage_registrations r
          ON r.event_id=m.initial_registration_event_id
         AND r.claim_id=m.canonical_node_id AND r.lineage_key=m.lineage_key
        JOIN canonical_graph_nodes n ON n.canonical_node_id=m.canonical_node_id AND n.node_kind='source_claim'
        WHERE m.canonical_node_id=checked_node_id AND m.first_admission_event_id IS NULL AND NOT m.was_bootstrapped
    ) THEN RETURN; END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM canonical_supersession_members AS member
        JOIN canonical_supersession_admission_events AS event
          ON event.event_id = member.first_admission_event_id
         AND event.lineage_key = member.lineage_key
        JOIN canonical_graph_nodes AS node
          ON node.canonical_node_id = member.canonical_node_id
         AND node.node_kind = 'source_claim'
        WHERE member.canonical_node_id = checked_node_id
          AND (
              (
                  NOT member.was_bootstrapped
                  AND member.canonical_node_id = event.replacement_node_id
              )
              OR (
                  member.was_bootstrapped
                  AND event.event_payload -> 'bootstrapped_target_node_ids'
                        @> jsonb_build_array(member.canonical_node_id)
                  AND EXISTS (
                      SELECT 1
                      FROM canonical_supersession_replacement_targets AS target
                      WHERE target.event_id = event.event_id
                        AND target.target_node_id = member.canonical_node_id
                  )
              )
          )
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member lacks event authority';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION canonical_supersession_lineage_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession lineage is append-only';
    END IF;
    IF EXISTS (SELECT 1 FROM canonical_initial_lineage_registrations r WHERE r.lineage_key=NEW.lineage_key) THEN RETURN NULL; END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM canonical_supersession_admission_events AS event
        WHERE event.lineage_key = NEW.lineage_key
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession lineage lacks admission-event authority';
    END IF;
    RETURN NULL;
END $$;


-- Canonical bytes match Go encoding/json for the bounded string-only event
-- fields. No alternate key order, whitespace, unknown keys or scalar types may
-- acquire an authority ID that the native deterministic decoder cannot replay.
CREATE FUNCTION initial_registration_json_string_v1(v text) RETURNS text LANGUAGE plpgsql AS $$
DECLARE result text;
BEGIN
 IF v IS NULL THEN RETURN NULL; END IF;
 IF v<>btrim(v,chr(9)||chr(10)||chr(11)||chr(12)||chr(13)||chr(32)||chr(133)||chr(160)||chr(5760)||chr(8192)||chr(8193)||chr(8194)||chr(8195)||chr(8196)||chr(8197)||chr(8198)||chr(8199)||chr(8200)||chr(8201)||chr(8202)||chr(8232)||chr(8233)||chr(8239)||chr(8287)||chr(12288)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='noncanonical registration text';
 END IF;
 result:=to_json(v)::text;
 RETURN replace(replace(replace(replace(replace(result,'&','\u0026'),'<','\u003c'),'>','\u003e'),chr(8232),'\u2028'),chr(8233),'\u2029');
END $$;
REVOKE ALL ON FUNCTION initial_registration_json_string_v1(text) FROM PUBLIC;
CREATE FUNCTION initial_registration_basis_json_v1(b jsonb, slots boolean) RETURNS text LANGUAGE plpgsql AS $$
DECLARE result text;
BEGIN
 result:='{"source_system":'||initial_registration_json_string_v1(b->>'source_system')||',"source_namespace":'||initial_registration_json_string_v1(b->>'source_namespace')||',"object_type":'||initial_registration_json_string_v1(b->>'object_type')||',"object_id":'||initial_registration_json_string_v1(b->>'object_id');
 IF slots THEN result:=result||',"slot_kind":'||initial_registration_json_string_v1(b->>'slot_kind')||',"slot_id":'||initial_registration_json_string_v1(b->>'slot_id'); END IF;
 RETURN result||'}';
END $$;
REVOKE ALL ON FUNCTION initial_registration_basis_json_v1(jsonb,boolean) FROM PUBLIC;
CREATE FUNCTION initial_registration_binding_json_v1(b jsonb) RETURNS text LANGUAGE plpgsql AS $$
DECLARE a jsonb:=b->'supersession_anchor'; head text;
BEGIN
 head:=CASE WHEN a->'head_event_id'='null'::jsonb THEN 'null' ELSE initial_registration_json_string_v1(a->>'head_event_id') END;
 RETURN '{"store_id":'||initial_registration_json_string_v1(b->>'store_id')||',"claim_id":'||initial_registration_json_string_v1(b->>'claim_id')||',"original_admission_id":'||initial_registration_json_string_v1(b->>'original_admission_id')||',"original_occurrence_id":'||initial_registration_json_string_v1(b->>'original_occurrence_id')||',"snapshot_id":'||initial_registration_json_string_v1(b->>'snapshot_id')||',"view_id":'||initial_registration_json_string_v1(b->>'view_id')||',"grounding_hash":'||initial_registration_json_string_v1(b->>'grounding_hash')||',"observation_receipt_id":'||initial_registration_json_string_v1(b->>'observation_receipt_id')||',"observation_hash":'||initial_registration_json_string_v1(b->>'observation_hash')||',"lineage_basis":'||initial_registration_basis_json_v1(b->'lineage_basis',true)||',"lineage_key":'||initial_registration_json_string_v1(b->>'lineage_key')||',"supersession_anchor":{"chain_key":'||initial_registration_json_string_v1(a->>'chain_key')||',"revision":'||((a->>'revision')::bigint)::text||',"head_event_id":'||head||'},"creation_object_manifest_hash":'||initial_registration_json_string_v1(b->>'creation_object_manifest_hash')||',"governance_digest":'||initial_registration_json_string_v1(b->>'governance_digest')||'}';
END $$;
REVOKE ALL ON FUNCTION initial_registration_binding_json_v1(jsonb) FROM PUBLIC;
CREATE FUNCTION initial_registration_payload_json_v1(p jsonb) RETURNS text LANGUAGE plpgsql AS $$
BEGIN
 RETURN '{"contract_version":'||initial_registration_json_string_v1(p->>'contract_version')||',"binding":'||initial_registration_binding_json_v1(p->'binding')||',"review_subject_id":'||initial_registration_json_string_v1(p->>'review_subject_id')||',"request_id":'||initial_registration_json_string_v1(p->>'request_id')||',"reviewer":'||initial_registration_json_string_v1(p->>'reviewer')||',"reason":'||initial_registration_json_string_v1(p->>'reason')||'}';
END $$;
REVOKE ALL ON FUNCTION initial_registration_payload_json_v1(jsonb) FROM PUBLIC;

CREATE FUNCTION canonical_initial_lineage_registration_apply_v1() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE p jsonb; b jsonb; basis jsonb; a jsonb; g jsonb; n record; snapshot record; receipt record;
 store text; governance_revision bigint; head_revision bigint; head_id text; occupied bigint; catalog_bytes bigint; manifest_count bigint;
BEGIN
 PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
 IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) OR NOT (NEW.binding_body IS JSON WITH UNIQUE KEYS) OR NOT (NEW.grounding_body IS JSON WITH UNIQUE KEYS) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ambiguous initial registration authority';
 END IF;
 p:=NEW.body::jsonb; b:=p->'binding'; basis:=b->'lineage_basis';a:=b->'supersession_anchor';g:=NEW.grounding_body::jsonb;
 IF NEW.body IS DISTINCT FROM initial_registration_payload_json_v1(p)
 OR NEW.binding_body IS DISTINCT FROM '{"contract_version":"core-initial-lineage-registration/v1","binding":'||initial_registration_binding_json_v1(b)||'}'
 OR b->>'lineage_key' IS DISTINCT FROM 'lineage:v1:sha256:'||encode(sha256(convert_to('{"contract_version":"supersession-lineage/v1","basis":'||initial_registration_basis_json_v1(basis,true)||'}','UTF8')),'hex')
 OR b->>'creation_object_manifest_hash' IS DISTINCT FROM 'object-claim-manifest:v1:sha256:'||encode(sha256(convert_to('{"contract_version":"supersession-closure-cut/v1","coverage_policy":"classified_admitted_source_object_claims/v1","source_object":'||initial_registration_basis_json_v1(basis,false)||',"claims":[{"node_id":'||initial_registration_json_string_v1(b->>'claim_id')||'}]}','UTF8')),'hex')
 OR coalesce(b->>'grounding_hash','') !~ '^sha256:[0-9a-f]{64}$'
 OR coalesce(b->>'observation_hash','') !~ '^sha256:[0-9a-f]{64}$'
 OR coalesce(b->>'governance_digest','') !~ '^sha256:[0-9a-f]{64}$'
 OR coalesce(b->>'original_admission_id','') !~ '^adm:.+'
 OR coalesce(b->>'original_occurrence_id','') !~ '^occ:.+'
 OR coalesce(b->>'claim_id','') !~ '^canon-node:.+'
 OR EXISTS(SELECT 1 FROM jsonb_object_keys(g) key WHERE key NOT IN ('governance_revision','node','source','related_governance')) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='noncanonical initial registration authority';
 END IF;
 PERFORM evidence_governance_lock_v1('evidence_governance_head'::regclass);
 SELECT store_id,revision INTO STRICT store,governance_revision FROM evidence_governance_head WHERE singleton;
 SELECT revision,head_event_id INTO STRICT head_revision,head_id FROM canonical_supersession_admission_head WHERE chain_key='canonical-supersession/v1';
 IF p->>'contract_version' IS DISTINCT FROM 'core-initial-lineage-registration/v1'
 OR b->>'store_id' IS DISTINCT FROM store OR b->>'claim_id' IS DISTINCT FROM NEW.claim_id OR b->>'lineage_key' IS DISTINCT FROM NEW.lineage_key
 OR p->>'request_id' IS DISTINCT FROM NEW.request_id
 OR NEW.binding_body::jsonb IS DISTINCT FROM jsonb_build_object('contract_version',p->'contract_version','binding',b)
 OR p->>'review_subject_id' IS DISTINCT FROM 'initial-lineage-review:v1:sha256:' || encode(sha256(convert_to(NEW.binding_body,'UTF8')),'hex')
 OR b->>'grounding_hash' IS DISTINCT FROM 'sha256:' || encode(sha256(convert_to(NEW.grounding_body,'UTF8')),'hex')
 OR coalesce(octet_length(btrim(p->>'reviewer')),0) NOT BETWEEN 1 AND 200
 OR coalesce(octet_length(btrim(p->>'reason')),0) NOT BETWEEN 1 AND 2000
 OR a->>'chain_key' IS DISTINCT FROM 'canonical-supersession/v1'
 OR (a->>'revision')::bigint IS DISTINCT FROM head_revision OR a->>'head_event_id' IS DISTINCT FROM head_id
 OR (g->>'governance_revision')::bigint IS DISTINCT FROM governance_revision THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='initial registration exact review or anchor mismatch';
 END IF;
 SELECT * INTO STRICT n FROM canonical_graph_nodes WHERE canonical_node_id=NEW.claim_id;
 IF n.node_kind<>'source_claim' OR n.origin_proposal_occurrence_id IS DISTINCT FROM b->>'original_occurrence_id'
 OR NOT EXISTS(SELECT 1 FROM admission_decisions d WHERE d.admission_decision_id=b->>'original_admission_id'
  AND d.proposal_occurrence_id=n.origin_proposal_occurrence_id AND d.canonical_ref=NEW.claim_id AND d.outcome='admitted')
 OR jsonb_typeof(g->'source') IS DISTINCT FROM 'object'
 OR g #>> '{source,review_package,proposal_basis,proposal_occurrence_id}' IS DISTINCT FROM b->>'original_occurrence_id'
 OR g #>> '{node,CanonicalID}' IS DISTINCT FROM NEW.claim_id OR g #> '{node,Payload}' IS DISTINCT FROM n.payload
 OR g #> '{node,OriginProposal,SourceRefs}' IS DISTINCT FROM (SELECT source_refs FROM proposal_occurrences WHERE proposal_occurrence_id=n.origin_proposal_occurrence_id)
 OR g #> '{node,Temporal}' IS DISTINCT FROM n.temporal OR g #> '{node,Provenance}' IS DISTINCT FROM n.provenance
 OR EXISTS(SELECT 1 FROM canonical_supersession_members WHERE canonical_node_id=NEW.claim_id)
 OR EXISTS(SELECT 1 FROM canonical_supersession_lineages WHERE lineage_key=NEW.lineage_key)
 OR EXISTS(SELECT 1 FROM canonical_graph_edges WHERE relation='supersedes' AND to_node_id=NEW.claim_id) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='initial registration requires original unclassified source claim and fresh lineage';
 END IF;
 IF (SELECT ((w.body::jsonb->>'display')::jsonb #>> '{request,operation}') FROM canonical_withdrawal_events w WHERE w.target_id=NEW.claim_id ORDER BY revision DESC LIMIT 1)='withdraw' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='withdrawn claim cannot register';
 END IF;
 SELECT ss.* INTO STRICT snapshot FROM proposal_occurrences po JOIN extraction_attempts ea USING(extraction_attempt_id)
 JOIN extraction_runs er USING(extraction_run_id) JOIN source_snapshots ss USING(source_snapshot_id) WHERE po.proposal_occurrence_id=n.origin_proposal_occurrence_id AND po.admission_outcome='admitted' AND po.canonical_ref=NEW.claim_id;
 SELECT * INTO STRICT receipt FROM external_source_intake_receipts WHERE request_id=b->>'observation_receipt_id';
 IF snapshot.source_system<>'external_document' OR snapshot.source_snapshot_id IS DISTINCT FROM b->>'snapshot_id'
 OR snapshot.origin_metadata->>'external_source_schema' IS DISTINCT FROM 'external-source-envelope-v1'
 OR snapshot.origin_metadata->>'external_content_fidelity' IS DISTINCT FROM 'verbatim'
 OR snapshot.origin_metadata->>'external_source_system' IS DISTINCT FROM basis->>'source_system'
 OR snapshot.origin_metadata->>'external_source_namespace' IS DISTINCT FROM basis->>'source_namespace'
 OR snapshot.origin_metadata->>'external_object_type' IS DISTINCT FROM basis->>'object_type'
 OR snapshot.origin_metadata->>'external_object_id' IS DISTINCT FROM basis->>'object_id'
 OR receipt.source_snapshot_id IS DISTINCT FROM snapshot.source_snapshot_id OR receipt.extraction_view_id IS DISTINCT FROM b->>'view_id'
 OR receipt.receipt_payload_hash IS DISTINCT FROM b->>'observation_hash'
 OR NOT EXISTS(SELECT 1 FROM extraction_views v WHERE v.extraction_view_id=b->>'view_id' AND v.source_snapshot_id=snapshot.source_snapshot_id)
 OR EXISTS(SELECT 1 FROM proposal_occurrences po CROSS JOIN LATERAL jsonb_array_elements(po.source_refs) ref WHERE po.proposal_occurrence_id=n.origin_proposal_occurrence_id AND ref->>'extraction_view_id' IS DISTINCT FROM b->>'view_id') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='initial registration source/observation binding mismatch';
 END IF;
 SELECT count(*) INTO manifest_count FROM canonical_graph_nodes cn JOIN proposal_occurrences po ON po.proposal_occurrence_id=cn.origin_proposal_occurrence_id
 JOIN extraction_attempts ea USING(extraction_attempt_id) JOIN extraction_runs er USING(extraction_run_id) JOIN source_snapshots ss USING(source_snapshot_id)
 WHERE cn.node_kind='source_claim' AND ss.source_system='external_document'
 AND ss.origin_metadata->>'external_source_system'=basis->>'source_system' AND ss.origin_metadata->>'external_source_namespace'=basis->>'source_namespace'
 AND ss.origin_metadata->>'external_object_type'=basis->>'object_type' AND ss.origin_metadata->>'external_object_id'=basis->>'object_id';
 IF manifest_count<>1 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='initial registration object manifest is not singleton'; END IF;
 -- Canonical record array bytes: brackets + records + separating commas.
 SELECT count(*),coalesce(sum(octet_length('{"event_id":"'||event_id||'","payload":'||body||'}')),0) INTO occupied,catalog_bytes FROM canonical_initial_lineage_registrations;
 catalog_bytes:=2+catalog_bytes+occupied+octet_length('{"event_id":"'||NEW.event_id||'","payload":'||NEW.body||'}');
 IF occupied+1>10000 OR catalog_bytes>16777216 THEN RAISE EXCEPTION USING ERRCODE='54000',MESSAGE='initial registration catalog capacity exceeded'; END IF;
 INSERT INTO canonical_supersession_lineages(lineage_key,contract_version,source_system,source_namespace,object_type,object_id,slot_kind,slot_id)
 VALUES(NEW.lineage_key,'supersession-lineage/v1',basis->>'source_system',basis->>'source_namespace',basis->>'object_type',basis->>'object_id',basis->>'slot_kind',basis->>'slot_id');
 INSERT INTO canonical_supersession_members(canonical_node_id,lineage_key,first_admission_event_id,was_bootstrapped,initial_registration_event_id)
 VALUES(NEW.claim_id,NEW.lineage_key,NULL,false,NEW.event_id);
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION canonical_initial_lineage_registration_apply_v1() FROM PUBLIC;
CREATE TRIGGER canonical_initial_registration_apply BEFORE INSERT ON canonical_initial_lineage_registrations
 FOR EACH ROW EXECUTE FUNCTION canonical_initial_lineage_registration_apply_v1();
CREATE TRIGGER evidence_governance_touch AFTER INSERT ON canonical_initial_lineage_registrations
 FOR EACH STATEMENT EXECUTE FUNCTION evidence_governance_touch_v1();

CREATE OR REPLACE FUNCTION canonical_supersession_assert_target(
    checked_event_id TEXT,
    checked_target_node_id TEXT,
    checked_edge_id TEXT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF checked_event_id IS NULL
        OR checked_target_node_id IS NULL
        OR checked_edge_id IS NULL THEN
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM canonical_supersession_replacement_targets AS target
        WHERE target.event_id = checked_event_id
          AND target.target_node_id = checked_target_node_id
    ) THEN
        PERFORM canonical_supersession_assert_edge(checked_edge_id);
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM canonical_supersession_replacement_targets AS target
        LEFT JOIN canonical_supersession_admission_events AS event
            ON event.event_id = target.event_id
        LEFT JOIN canonical_graph_edges AS edge
            ON edge.canonical_edge_id = target.canonical_edge_id
        LEFT JOIN canonical_graph_nodes AS replacement
            ON replacement.canonical_node_id = target.replacement_node_id
        LEFT JOIN canonical_graph_nodes AS replaced
            ON replaced.canonical_node_id = target.target_node_id
        LEFT JOIN canonical_supersession_members AS target_member
            ON target_member.canonical_node_id = target.target_node_id
           AND target_member.lineage_key = target.lineage_key
        LEFT JOIN canonical_supersession_admission_events AS first_event
            ON first_event.event_id = target_member.first_admission_event_id
        LEFT JOIN canonical_initial_lineage_registrations AS registration
            ON registration.event_id = target_member.initial_registration_event_id
        LEFT JOIN proposal_occurrences AS replacement_proposal
            ON replacement_proposal.proposal_occurrence_id =
                replacement.origin_proposal_occurrence_id
        LEFT JOIN proposal_occurrences AS replaced_proposal
            ON replaced_proposal.proposal_occurrence_id =
                replaced.origin_proposal_occurrence_id
        LEFT JOIN admission_decisions AS decision
            ON decision.admission_decision_id = event.admission_decision_id
        WHERE target.event_id = checked_event_id
          AND target.target_node_id = checked_target_node_id
          AND (
              target.canonical_edge_id <> checked_edge_id
              OR event.event_id IS NULL
              OR event.lineage_key <> target.lineage_key
              OR event.replacement_node_id <> target.replacement_node_id
              OR event.admission_outcome <> 'admitted'
              OR jsonb_typeof(event.event_payload -> 'target_node_ids')
                    IS DISTINCT FROM 'array'
              OR NOT (
                    event.event_payload -> 'target_node_ids'
                    @> jsonb_build_array(target.target_node_id)
              )
              OR edge.canonical_edge_id IS NULL
              OR edge.relation <> 'supersedes'
              OR edge.from_node_id <> target.replacement_node_id
              OR edge.to_node_id <> target.target_node_id
              OR edge.origin_proposal_occurrence_id <>
                    event.proposal_occurrence_id
              OR edge.provenance ->> 'trace_ref'
                    IS DISTINCT FROM event.event_id
              OR edge.provenance ->> 'review_ref'
                    IS DISTINCT FROM event.admission_decision_id
              OR edge.provenance ->> 'method'
                    IS DISTINCT FROM 'supersession_admission_edge'
              OR edge.provenance ->> 'method_version'
                    IS DISTINCT FROM 'v1'
              OR replacement.canonical_node_id IS NULL
              OR replacement.node_kind <> 'source_claim'
              OR replacement.origin_proposal_occurrence_id <>
                    event.proposal_occurrence_id
              OR replacement_proposal.proposal_occurrence_id IS NULL
              OR replacement_proposal.admission_outcome <> 'admitted'
              OR replacement_proposal.canonical_ref <>
                    replacement.canonical_node_id
              OR replaced.canonical_node_id IS NULL
              OR replaced.node_kind <> 'source_claim'
              OR target_member.canonical_node_id IS NULL
              OR NOT (
                    (
                        target_member.first_admission_event_id = event.event_id
                        AND target_member.was_bootstrapped
                        AND (
                            event.event_payload
                                -> 'bootstrapped_target_node_ids'
                        ) @> jsonb_build_array(target.target_node_id)
                    )
                    OR (
                        first_event.event_id IS NOT NULL
                        AND first_event.lineage_key = event.lineage_key
                        AND first_event.revision < event.revision
                    )
                    OR (
                        registration.event_id IS NOT NULL
                        AND registration.claim_id = target.target_node_id
                        AND registration.lineage_key = event.lineage_key
                        AND target_member.first_admission_event_id IS NULL
                        AND NOT target_member.was_bootstrapped
                        AND (registration.body::jsonb #>> '{binding,supersession_anchor,revision}')::bigint < event.revision
                    )
              )
              OR replaced_proposal.proposal_occurrence_id IS NULL
              OR replaced_proposal.admission_outcome <> 'admitted'
              OR replaced_proposal.canonical_ref <> replaced.canonical_node_id
              OR decision.admission_decision_id IS NULL
              OR decision.proposal_occurrence_id <>
                    event.proposal_occurrence_id
              OR decision.outcome <> 'admitted'
              OR decision.canonical_ref <> event.replacement_node_id
              OR jsonb_typeof(decision.canonical_edge_ids)
                    IS DISTINCT FROM 'array'
              OR NOT decision.canonical_edge_ids
                    @> jsonb_build_array(target.canonical_edge_id)
          )
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession replacement target authority is inconsistent';
    END IF;

    PERFORM canonical_supersession_assert_edge(checked_edge_id);
END $$;
CREATE OR REPLACE FUNCTION canonical_supersession_member_authority_trigger()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
BEGIN
    IF TG_TABLE_NAME<>'canonical_supersession_members' OR TG_LEVEL<>'ROW' OR TG_WHEN<>'AFTER' OR TG_OP NOT IN ('INSERT','UPDATE','DELETE') THEN
        RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid supersession authority trigger context';
    END IF;
    PERFORM pg_catalog.set_config('search_path',pg_catalog.format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            MESSAGE = 'canonical supersession member is append-only';
    END IF;
    PERFORM canonical_supersession_assert_member(NEW.canonical_node_id);
    PERFORM canonical_supersession_assert_event(NEW.first_admission_event_id);
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION canonical_supersession_assert_member(text) FROM PUBLIC;
