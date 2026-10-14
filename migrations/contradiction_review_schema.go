package migrations

const contradictionReviewMigration = "000059_reviewed_contradictions.up.sql"

func init() {
	requiredFixedCoreColumns = append(requiredFixedCoreColumns, requiredFixedCoreColumn{Migration: contradictionReviewMigration, Table: "canonical_contradiction_admission_decisions", Name: "review_binding_receipt_id", DataType: "text"})
	requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, requiredFixedCoreConstraint{Migration: contradictionReviewMigration, Table: "canonical_contradiction_admission_decisions", Name: "canonical_contradiction_decision_review_fk", Type: "f", Deferrable: true, InitiallyDeferred: true, Definition: "FOREIGN KEY (canonical_contradiction_proposal_id, review_binding_receipt_id) REFERENCES canonical_contradiction_review_bindings(proposal_id, receipt_id) DEFERRABLE INITIALLY DEFERRED"})
	requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, requiredFixedCoreFunction{Migration: contradictionReviewMigration, Name: "canonical_contradiction_review_apply_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true})
	for _, t := range []requiredFixedCoreTrigger{
		{Name: "canonical_contradiction_review_bindings_apply", Function: "canonical_contradiction_review_apply_v1", TriggerType: 1 | 2 | 4},
		{Name: "canonical_contradiction_review_bindings_immutable_rows", Function: "external_record_immutable", TriggerType: 1 | 2 | 8 | 16},
		{Name: "canonical_contradiction_review_bindings_immutable_table", Function: "external_record_immutable", TriggerType: 2 | 32},
	} {
		t.Migration = contradictionReviewMigration
		t.Table = "canonical_contradiction_review_bindings"
		requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, t)
	}
	for _, c := range []requiredFixedCoreColumn{
		{Name: "receipt_id", DataType: "text", NotNull: true}, {Name: "request_id", DataType: "text", NotNull: true}, {Name: "proposal_id", DataType: "text", NotNull: true}, {Name: "body", DataType: "text", NotNull: true}, {Name: "recorded_at", DataType: "timestamp with time zone", NotNull: true, Default: "clock_timestamp()"},
	} {
		c.Migration = contradictionReviewMigration
		c.Table = "canonical_contradiction_review_bindings"
		requiredFixedCoreColumns = append(requiredFixedCoreColumns, c)
	}
	for _, c := range []requiredFixedCoreConstraint{
		{Name: "contradiction_review_proposal_receipt_key", Type: "u", Definition: "UNIQUE (proposal_id, receipt_id)"},
		{Name: "canonical_contradiction_review_bindings_pkey", Type: "p", Definition: "PRIMARY KEY (receipt_id)"},
		{Name: "canonical_contradiction_review_bindings_request_id_key", Type: "u", Definition: "UNIQUE (request_id)"},
		{Name: "canonical_contradiction_review_bindings_proposal_id_key", Type: "u", Definition: "UNIQUE (proposal_id)"},
		{Name: "canonical_contradiction_review_bindings_proposal_id_fkey", Type: "f", Definition: "FOREIGN KEY (proposal_id) REFERENCES canonical_contradiction_proposals(canonical_contradiction_proposal_id)"},
		{Name: "canonical_contradiction_review_bindings_request_id_check", Type: "c", Definition: "CHECK (((octet_length(request_id) >= 1) AND (octet_length(request_id) <= 200)))"},
		{Name: "canonical_contradiction_review_bindings_body_check", Type: "c", Definition: "CHECK (((octet_length(body) >= 1) AND (octet_length(body) <= 1048576)))"},
		{Name: "canonical_contradiction_review_bindings_digest", Type: "c", Definition: "CHECK ((receipt_id = ('contradiction-review-receipt:sha256:'::text || encode(sha256(convert_to(body, 'UTF8'::name)), 'hex'::text))))"},
	} {
		c.Migration = contradictionReviewMigration
		c.Table = "canonical_contradiction_review_bindings"
		requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, c)
	}
}
