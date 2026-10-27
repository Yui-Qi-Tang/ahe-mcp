package migrations

const historicalDerivationMigration = "000061_historical_derivation.up.sql"

func init() {
	table := "canonical_historical_derivation_reviews"
	for _, f := range []requiredFixedCoreFunction{
		{Name: "historical_derivation_assert_v1", Arguments: "text", Result: "void"},
		{Name: "historical_derivation_authority_trigger_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}},
	} {
		f.Migration = historicalDerivationMigration
		f.RequirePublicExecuteRevoked = true
		requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, f)
	}
	for _, t := range []requiredFixedCoreTrigger{
		{Name: "historical_derivation_immutable_rows", Table: table, Function: "external_record_immutable", TriggerType: 27},
		{Name: "historical_derivation_immutable_table", Table: table, Function: "external_record_immutable", TriggerType: 34},
		{Name: "historical_derivation_binding_authority", Table: table, Function: "historical_derivation_authority_trigger_v1", TriggerType: 5, Constraint: true, Deferrable: true, InitiallyDeferred: true},
		{Name: "historical_derivation_class_authority", Table: "canonical_derivations", Function: "historical_derivation_authority_trigger_v1", TriggerType: 5, Constraint: true, Deferrable: true, InitiallyDeferred: true},
		{Name: "historical_derivation_decision_authority", Table: "admission_decisions", Function: "historical_derivation_authority_trigger_v1", TriggerType: 5, Constraint: true, Deferrable: true, InitiallyDeferred: true},
		{Name: "evidence_governance_touch", Table: table, Function: "evidence_governance_touch_v1", TriggerType: 6},
	} {
		t.Migration = historicalDerivationMigration
		requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, t)
	}
	for _, name := range []string{"admission_decision_id", "proposal_occurrence_id", "request_id", "review_subject", "display_payload", "receipt_payload"} {
		requiredFixedCoreColumns = append(requiredFixedCoreColumns, requiredFixedCoreColumn{Migration: historicalDerivationMigration, Table: table, Name: name, DataType: "text", NotNull: true})
	}
	requiredFixedCoreColumns = append(requiredFixedCoreColumns,
		requiredFixedCoreColumn{Migration: historicalDerivationMigration, Table: table, Name: "recorded_at", DataType: "timestamp with time zone", NotNull: true, Default: "clock_timestamp()"},
		requiredFixedCoreColumn{Migration: historicalDerivationMigration, Table: "canonical_derivations", Name: "use_class", DataType: "text", NotNull: true, Default: "'ordinary'::text"})
	for _, c := range []requiredFixedCoreConstraint{
		{Table: "canonical_derivations", Name: "canonical_derivation_use_class_ck", Type: "c", Definition: "CHECK ((use_class = ANY (ARRAY['ordinary'::text, 'historical_only'::text])))", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_rev_proposal_occurrence_id_fkey", Type: "f", Definition: "FOREIGN KEY (proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id)", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_revi_admission_decision_id_fkey", Type: "f", Definition: "FOREIGN KEY (admission_decision_id) REFERENCES admission_decisions(admission_decision_id) DEFERRABLE INITIALLY DEFERRED", Deferrable: true, InitiallyDeferred: true},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_revi_proposal_occurrence_id_key", Type: "u", Definition: "UNIQUE (proposal_occurrence_id)", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_reviews_display_payload_check", Type: "c", Definition: "CHECK ((((octet_length(display_payload) >= 1) AND (octet_length(display_payload) <= 1048576)) AND (display_payload IS JSON OBJECT WITH UNIQUE KEYS)))", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_reviews_pkey", Type: "p", Definition: "PRIMARY KEY (admission_decision_id)", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_reviews_receipt_payload_check", Type: "c", Definition: "CHECK ((((octet_length(receipt_payload) >= 1) AND (octet_length(receipt_payload) <= 32768)) AND (receipt_payload IS JSON OBJECT WITH UNIQUE KEYS)))", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_reviews_request_id_check", Type: "c", Definition: "CHECK ((((octet_length(request_id) >= 1) AND (octet_length(request_id) <= 200)) AND (btrim(request_id) = request_id)))", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_reviews_request_id_key", Type: "u", Definition: "UNIQUE (request_id)", Deferrable: false, InitiallyDeferred: false},
		{Table: "canonical_historical_derivation_reviews", Name: "canonical_historical_derivation_reviews_review_subject_check", Type: "c", Definition: "CHECK ((review_subject ~ '^historical-derivation-review:sha256:[0-9a-f]{64}$'::text))", Deferrable: false, InitiallyDeferred: false},
	} {
		c.Migration = historicalDerivationMigration
		requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, c)
	}
}
