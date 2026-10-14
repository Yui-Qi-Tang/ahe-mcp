-- The standard reviewer may INSERT only this immutable command/receipt. Its
-- fixed trigger can materialize one contradiction pair, never nodes or other
-- relations. Legacy pair-v1 identity and terminal records remain unchanged.
CREATE TABLE canonical_contradiction_review_bindings (
 receipt_id text PRIMARY KEY,
 request_id text NOT NULL UNIQUE CHECK (octet_length(request_id) BETWEEN 1 AND 200),
 proposal_id text NOT NULL UNIQUE REFERENCES canonical_contradiction_proposals(canonical_contradiction_proposal_id),
 CONSTRAINT contradiction_review_proposal_receipt_key UNIQUE (proposal_id, receipt_id),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT canonical_contradiction_review_bindings_digest CHECK (receipt_id = 'contradiction-review-receipt:sha256:' || encode(sha256(convert_to(body,'UTF8')),'hex'))
);
ALTER TABLE canonical_contradiction_admission_decisions ADD COLUMN review_binding_receipt_id text;
ALTER TABLE canonical_contradiction_admission_decisions ADD CONSTRAINT canonical_contradiction_decision_review_fk FOREIGN KEY (canonical_contradiction_proposal_id,review_binding_receipt_id) REFERENCES canonical_contradiction_review_bindings(proposal_id,receipt_id) DEFERRABLE INITIALLY DEFERRED;
CREATE TRIGGER canonical_contradiction_review_bindings_immutable_rows BEFORE UPDATE OR DELETE ON canonical_contradiction_review_bindings FOR EACH ROW EXECUTE FUNCTION external_record_immutable();
CREATE TRIGGER canonical_contradiction_review_bindings_immutable_table BEFORE TRUNCATE ON canonical_contradiction_review_bindings FOR EACH STATEMENT EXECUTE FUNCTION external_record_immutable();

CREATE FUNCTION canonical_contradiction_review_apply_v1() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog SET row_security=off AS $$
DECLARE
 b jsonb; d jsonb; req jsonb; p jsonb; decision jsonb; edge jsonb;
 store text; revision bigint; old record; expected_edge text;
BEGIN
 PERFORM set_config('search_path',format('pg_catalog, %I, pg_temp',TG_TABLE_SCHEMA),true);
 IF NOT (NEW.body IS JSON WITH UNIQUE KEYS) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ambiguous contradiction receipt';
 END IF;
 b:=NEW.body::jsonb;
 IF NOT ((b->>'display') IS JSON WITH UNIQUE KEYS) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='ambiguous contradiction display';
 END IF;
 d:=(b->>'display')::jsonb; req:=d->'request';p:=d->'proposal';decision:=b->'decision';edge:=b->'edge';
 -- This also serializes callers using raw INSERT with standard profile authority.
 PERFORM evidence_governance_lock_v1('evidence_governance_head'::regclass);
 SELECT store_id,h.revision INTO STRICT store,revision FROM evidence_governance_head h WHERE singleton;
 IF d->>'store_id' IS DISTINCT FROM store OR (d->>'governance_revision')::bigint IS DISTINCT FROM revision THEN
  RAISE EXCEPTION USING ERRCODE='40001',MESSAGE='contradiction review snapshot changed';
 END IF;
 IF b->>'contract' IS DISTINCT FROM 'reviewed-contradiction/v1'
  OR d->>'contract' IS DISTINCT FROM 'contradiction-review/v1'
  OR b->>'request_id' IS DISTINCT FROM NEW.request_id
  OR b->>'subject' IS DISTINCT FROM 'contradiction-review:sha256:' || encode(sha256(convert_to(b->>'display','UTF8')),'hex')
  OR p->>'ID' IS DISTINCT FROM NEW.proposal_id OR decision->>'ProposalID' IS DISTINCT FROM NEW.proposal_id
  OR p->>'AdmissionOutcome' IS DISTINCT FROM 'pending' OR coalesce(p->>'CanonicalEdgeID','')<>''
  OR p->>'Relation' IS DISTINCT FROM 'contradicts' OR req->>'relation' IS DISTINCT FROM 'contradicts'
  OR req->>'node_a_id' IS DISTINCT FROM p->>'NodeAID' OR req->>'node_b_id' IS DISTINCT FROM p->>'NodeBID'
  OR NOT (p->>'NodeAID' < p->>'NodeBID')
  OR req->>'rationale' IS DISTINCT FROM p->>'Rationale'
  OR req->>'proposal_request_id' IS DISTINCT FROM p->>'RequestID'
  OR req->>'producer_name' IS DISTINCT FROM p->>'ProducerName'
  OR req->>'producer_version' IS DISTINCT FROM p->>'ProducerVersion'
  OR coalesce(req->>'producer_session_ref','') IS DISTINCT FROM coalesce(p->>'ProducerSessionRef','')
  OR coalesce(req->>'source_proposal_occurrence_id','') IS DISTINCT FROM coalesce(p->>'SourceProposalOccurrenceID','')
  OR coalesce(octet_length(btrim(req->>'scope')),0) NOT BETWEEN 1 AND 4000
  OR jsonb_typeof(req->'conditions') IS DISTINCT FROM 'array'
  OR jsonb_array_length(req->'conditions') >32
  OR coalesce(decision->>'Outcome','') NOT IN ('admitted','rejected','audit_only')
  OR coalesce(octet_length(btrim(decision->>'DecisionBy')),0) NOT BETWEEN 1 AND 200
  OR coalesce(octet_length(btrim(decision->>'DecisionReason')),0) NOT BETWEEN 1 AND 2000
  OR d #>> '{node_a,CanonicalID}' IS DISTINCT FROM p->>'NodeAID'
  OR d #>> '{node_b,CanonicalID}' IS DISTINCT FROM p->>'NodeBID' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='contradiction exact review binding mismatch';
 END IF;
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(req->'conditions') c WHERE jsonb_typeof(c)<>'string' OR octet_length(btrim(c #>> '{}')) NOT BETWEEN 1 AND 2000) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invalid contradiction conditions';
 END IF;
 PERFORM canonical_node_id FROM canonical_graph_nodes WHERE canonical_node_id IN (p->>'NodeAID',p->>'NodeBID') ORDER BY canonical_node_id FOR KEY SHARE;
 IF (SELECT count(*) FROM canonical_graph_nodes WHERE canonical_node_id IN (p->>'NodeAID',p->>'NodeBID'))<>2 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='contradiction requires two admitted endpoints';
 END IF;
 INSERT INTO canonical_contradiction_proposals(canonical_contradiction_proposal_id,request_id,request_payload_hash,proposal_fingerprint,node_a_id,node_b_id,relation,rationale,producer_name,producer_version,producer_session_ref,admission_outcome,source_proposal_occurrence_id)
 VALUES(NEW.proposal_id,p->>'RequestID',p->>'RequestPayloadHash',p->>'ProposalFingerprint',p->>'NodeAID',p->>'NodeBID','contradicts',p->>'Rationale',p->>'ProducerName',p->>'ProducerVersion',nullif(p->>'ProducerSessionRef',''),'pending',nullif(p->>'SourceProposalOccurrenceID','')) ON CONFLICT DO NOTHING;
 SELECT * INTO STRICT old FROM canonical_contradiction_proposals WHERE canonical_contradiction_proposal_id=NEW.proposal_id FOR UPDATE;
 IF old.admission_outcome<>'pending' OR old.request_payload_hash IS DISTINCT FROM p->>'RequestPayloadHash'
  OR old.request_id IS DISTINCT FROM p->>'RequestID' OR old.node_a_id IS DISTINCT FROM p->>'NodeAID' OR old.node_b_id IS DISTINCT FROM p->>'NodeBID'
  OR old.rationale IS DISTINCT FROM p->>'Rationale' OR old.producer_name IS DISTINCT FROM p->>'ProducerName'
  OR old.producer_version IS DISTINCT FROM p->>'ProducerVersion'
  OR coalesce(old.producer_session_ref,'') IS DISTINCT FROM coalesce(p->>'ProducerSessionRef','')
  OR coalesce(old.source_proposal_occurrence_id,'') IS DISTINCT FROM coalesce(p->>'SourceProposalOccurrenceID','') THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='pair-v1 conflict or terminal pair';
 END IF;
 IF decision->>'Outcome'='admitted' THEN
  -- Exactly the existing StableCanonicalID framing, including trailing NUL.
  expected_edge:='canon-edge:' || substr(encode(sha256(convert_to(p->>'NodeAID','UTF8') || decode('00','hex') || convert_to(p->>'NodeBID','UTF8') || decode('00','hex') || convert_to('contradicts','UTF8') || decode('00','hex')),'hex'),1,16);
  IF edge->>'ID' IS DISTINCT FROM expected_edge OR decision->>'CanonicalEdgeID' IS DISTINCT FROM expected_edge
   OR edge->>'From' IS DISTINCT FROM p->>'NodeAID' OR edge->>'To' IS DISTINCT FROM p->>'NodeBID'
   OR edge->>'Relation' IS DISTINCT FROM 'contradicts' OR edge->>'OriginContradictionProposalID' IS DISTINCT FROM NEW.proposal_id
   OR edge #>> '{Provenance,trace_ref}' IS DISTINCT FROM NEW.proposal_id
   OR edge #>> '{Provenance,review_ref}' IS DISTINCT FROM decision->>'ID'
   OR edge #>> '{Provenance,producer}' IS DISTINCT FROM p->>'ProducerName'
   OR edge #>> '{Provenance,method}' IS DISTINCT FROM 'human_admitted_canonical_contradiction'
   OR edge #>> '{Provenance,method_version}' IS DISTINCT FROM 'v1' THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='contradiction edge provenance mismatch';
  END IF;
  INSERT INTO canonical_graph_edges(canonical_edge_id,from_node_id,to_node_id,relation,provenance,origin_canonical_contradiction_proposal_id)
   VALUES(expected_edge,p->>'NodeAID',p->>'NodeBID','contradicts',edge->'Provenance',NEW.proposal_id);
 ELSIF coalesce(decision->>'CanonicalEdgeID','')<>'' OR coalesce(edge->>'ID','')<>'' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='non-admission cannot create an edge';
 END IF;
 INSERT INTO canonical_contradiction_admission_decisions(canonical_contradiction_admission_decision_id,canonical_contradiction_proposal_id,outcome,canonical_edge_id,decision_by,decision_reason,review_binding_receipt_id)
  VALUES(decision->>'ID',NEW.proposal_id,decision->>'Outcome',nullif(decision->>'CanonicalEdgeID',''),decision->>'DecisionBy',decision->>'DecisionReason',NEW.receipt_id);
 UPDATE canonical_contradiction_proposals SET admission_outcome=decision->>'Outcome',canonical_edge_id=nullif(decision->>'CanonicalEdgeID',''),decided_at=now() WHERE canonical_contradiction_proposal_id=NEW.proposal_id;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION canonical_contradiction_review_apply_v1() FROM PUBLIC;
CREATE TRIGGER canonical_contradiction_review_bindings_apply BEFORE INSERT ON canonical_contradiction_review_bindings FOR EACH ROW EXECUTE FUNCTION canonical_contradiction_review_apply_v1();
