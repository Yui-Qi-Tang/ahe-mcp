package migrations

const externalRepresentationMigration = "000052_evidence_ingestion_external_representations.up.sql"

var externalRepresentationFunctions = []requiredFixedCoreFunction{
	{Migration: externalRepresentationMigration, Name: "external_representation_guard", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true},
	{Migration: externalRepresentationMigration, Name: "external_representation_link_guard", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true},
}
var externalRepresentationTriggers = []requiredFixedCoreTrigger{
	{Migration: externalRepresentationMigration, Name: "external_representation_insert_guard", Table: "external_representation_records", Function: "external_representation_guard", TriggerType: 1 | 2 | 4},
	{Migration: externalRepresentationMigration, Name: "external_representation_immutable_rows", Table: "external_representation_records", Function: "external_record_immutable", TriggerType: 1 | 2 | 8 | 16},
	{Migration: externalRepresentationMigration, Name: "external_representation_immutable_table", Table: "external_representation_records", Function: "external_record_immutable", TriggerType: 2 | 32},
	{Migration: externalRepresentationMigration, Name: "external_representation_link_insert_guard", Table: "external_check_representation_links", Function: "external_representation_link_guard", TriggerType: 1 | 2 | 4},
	{Migration: externalRepresentationMigration, Name: "external_representation_link_immutable_rows", Table: "external_check_representation_links", Function: "external_record_immutable", TriggerType: 1 | 2 | 8 | 16},
	{Migration: externalRepresentationMigration, Name: "external_representation_link_immutable_table", Table: "external_check_representation_links", Function: "external_record_immutable", TriggerType: 2 | 32},
}
var externalRepresentationColumns = []requiredFixedCoreColumn{
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "representation_id", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "request_id", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "proposal_occurrence_id", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "body", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "previous_id", DataType: "text", NotNull: false},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "check_id", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "input_id", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "representation_id", DataType: "text", NotNull: true},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "recorded_at", DataType: "timestamp with time zone", NotNull: true, Default: "clock_timestamp()"},
}
var externalRepresentationConstraints = []requiredFixedCoreConstraint{
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_records_pkey", Type: "p", Definition: "PRIMARY KEY (representation_id)"},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_records_request_id_key", Type: "u", Definition: "UNIQUE (request_id)"},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_records_proposal_occurrence_id_fkey", Type: "f", Definition: "FOREIGN KEY (proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id)"},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_records_previous_id_fkey", Type: "f", Definition: "FOREIGN KEY (previous_id) REFERENCES external_representation_records(representation_id)"},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_records_body_check", Type: "c", Definition: "CHECK (((octet_length(body) >= 1) AND (octet_length(body) <= 1048576)))"},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_records_request_id_check", Type: "c", Definition: "CHECK (((octet_length(request_id) >= 1) AND (octet_length(request_id) <= 512)))"},
	{Migration: externalRepresentationMigration, Table: "external_representation_records", Name: "external_representation_digest", Type: "c", Definition: "CHECK ((representation_id = ('representation:sha256:'::text || encode(sha256(convert_to(body, 'UTF8'::name)), 'hex'::text))))"},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "external_check_representation_links_pkey", Type: "p", Definition: "PRIMARY KEY (check_id, input_id)"},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "external_check_representation_links_check_id_fkey", Type: "f", Definition: "FOREIGN KEY (check_id) REFERENCES external_check_records(check_id)"},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "external_check_representation_links_representation_id_fkey", Type: "f", Definition: "FOREIGN KEY (representation_id) REFERENCES external_representation_records(representation_id)"},
	{Migration: externalRepresentationMigration, Table: "external_check_representation_links", Name: "external_check_representation_links_input_id_check", Type: "c", Definition: "CHECK (((octet_length(input_id) >= 1) AND (octet_length(input_id) <= 128)))"},
}
