package migrations

const externalCheckMigration = "000051_evidence_ingestion_external_checks.up.sql"

var externalCheckFunctions = []requiredFixedCoreFunction{
	{Migration: externalCheckMigration, Name: "external_record_immutable", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true},
	{Migration: externalCheckMigration, Name: "external_check_record_guard", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true},
}
var externalCheckTriggers = []requiredFixedCoreTrigger{
	{Migration: externalCheckMigration, Name: "external_check_insert_guard", Table: "external_check_records", Function: "external_check_record_guard", TriggerType: 1 | 2 | 4},
	{Migration: externalCheckMigration, Name: "external_check_immutable_rows", Table: "external_check_records", Function: "external_record_immutable", TriggerType: 1 | 2 | 8 | 16},
	{Migration: externalCheckMigration, Name: "external_check_immutable_table", Table: "external_check_records", Function: "external_record_immutable", TriggerType: 2 | 32},
}
var externalCheckColumns = []requiredFixedCoreColumn{
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "check_id", DataType: "text", NotNull: true},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "request_id", DataType: "text", NotNull: true},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "proposal_occurrence_id", DataType: "text", NotNull: true},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "body", DataType: "text", NotNull: true},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "revises_id", DataType: "text"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "recorded_at", DataType: "timestamp with time zone", NotNull: true, Default: "clock_timestamp()"},
}
var externalCheckConstraints = []requiredFixedCoreConstraint{
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_records_pkey", Type: "p", Definition: "PRIMARY KEY (check_id)"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_records_request_id_key", Type: "u", Definition: "UNIQUE (request_id)"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_records_proposal_occurrence_id_fkey", Type: "f", Definition: "FOREIGN KEY (proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id)"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_records_revises_id_fkey", Type: "f", Definition: "FOREIGN KEY (revises_id) REFERENCES external_check_records(check_id)"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_records_body_check", Type: "c", Definition: "CHECK (((octet_length(body) >= 1) AND (octet_length(body) <= 1048576)))"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_records_request_id_check", Type: "c", Definition: "CHECK (((octet_length(request_id) >= 1) AND (octet_length(request_id) <= 512)))"},
	{Migration: externalCheckMigration, Table: "external_check_records", Name: "external_check_digest", Type: "c", Definition: "CHECK ((check_id = ('check:sha256:'::text || encode(sha256(convert_to(body, 'UTF8'::name)), 'hex'::text))))"},
}
