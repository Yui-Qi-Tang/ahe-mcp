package migrations

import (
	"context"
	"fmt"
)

// Receipt bodies remain bounded by their SQL guards. Readiness also requires
// their original non-null storage types: a weakened column must not silently
// turn a three-valued SQL check into a nullable authority field.
func verifyRelationAdmissionColumns(ctx context.Context, db tableQueryer) error {
	for _, table := range []struct {
		name   string
		fields []string
	}{
		{"canonical_endpoint_review_bindings", []string{"admission_decision_id", "proposal_occurrence_id", "request_id", "review_subject", "display_payload", "receipt_payload", "created_at"}},
		{"canonical_implements_admissions", []string{"request_id", "contract_version", "receipt_id", "canonical_edge_id", "specification_node_id", "implementation_node_id", "origin_proposal_occurrence_id", "reviewer_id", "decision_reason", "producer_session_ref", "basis_id", "report_id", "candidate_id", "display_id", "basis_payload_utf8", "receipt_payload_utf8", "display_media_type", "display_payload_utf8", "basis_payload_hash", "receipt_payload_hash", "display_payload_hash", "created_at"}},
		{"canonical_references_admissions", []string{"request_id", "contract_version", "receipt_id", "canonical_edge_id", "from_node_id", "to_node_id", "origin_proposal_occurrence_id", "reviewer_id", "decision_reason", "producer_session_ref", "review_subject_id", "cut_id", "request_payload_utf8", "request_payload_hash", "review_payload_utf8", "review_payload_hash", "receipt_payload_utf8", "receipt_payload_hash", "created_at"}},
	} {
		var valid int
		err := db.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_attribute a
   JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
   JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
   WHERE n.nspname=current_schema() AND c.relname=$1 AND a.attname=ANY($2)
    AND a.attnum>0 AND NOT a.attisdropped AND a.attnotnull
    AND a.atttypid=CASE WHEN a.attname='created_at' THEN 'timestamptz'::regtype WHEN c.relname='canonical_endpoint_review_bindings' AND a.attname='receipt_payload' THEN 'jsonb'::regtype ELSE 'text'::regtype END
    AND a.attgenerated='' AND a.attidentity=''
    AND CASE WHEN a.attname='created_at' THEN pg_catalog.pg_get_expr(d.adbin,d.adrelid)='now()'
        WHEN a.attname='producer_session_ref' THEN pg_catalog.pg_get_expr(d.adbin,d.adrelid)=$3
        ELSE d.oid IS NULL END`, table.name, table.fields, "''::text").Scan(&valid)
		if err != nil {
			return fmt.Errorf("checking relation admission columns: %w", err)
		}
		if valid != len(table.fields) {
			return fmt.Errorf("relation admission columns in %s differ from the exact storage contract", table.name)
		}
	}
	return nil
}
