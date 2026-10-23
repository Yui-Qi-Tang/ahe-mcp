package migrations

const initialRegistrationMigration = "000060_initial_lineage_registration.up.sql"

func init() {
	for _, f := range []requiredFixedCoreFunction{
		{Name: "canonical_supersession_assert_member", Arguments: "text", Result: "void", RequirePublicExecuteRevoked: true},
	} {
		f.Migration = initialRegistrationMigration
		requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, f)
	}

	for i := range requiredCanonicalSupersessionFunctions {
		switch requiredCanonicalSupersessionFunctions[i].Name {
		case "canonical_supersession_assert_target":
			requiredCanonicalSupersessionFunctions[i].SourceSHA256 = "sha256:9c9ea047357d7ecdd4ed25f9e2a6d7dc42e231ed8d5f653e03a20f2d2001e869"
		case "canonical_supersession_lineage_authority_trigger":
			requiredCanonicalSupersessionFunctions[i].SourceSHA256 = "sha256:a0a6d4dd91237952d3c72e249f22d0aeab5a07f73f4a7c95e5d1a2902aa93850"
		case "canonical_supersession_member_authority_trigger":
			requiredCanonicalSupersessionFunctions[i].SourceSHA256 = "sha256:0e8effc497e2b4d6e579d3dccb9df80a9cc39bd26a1d4787b94bce965707ec8e"
		}
	}
	for i := range requiredAdmissionFunctions {
		if requiredAdmissionFunctions[i].Name == "canonical_supersession_member_authority_trigger" {
			requiredAdmissionFunctions[i].SourceSHA256 = "sha256:0e8effc497e2b4d6e579d3dccb9df80a9cc39bd26a1d4787b94bce965707ec8e"
		}
	}
	table := "canonical_initial_lineage_registrations"
	for _, f := range []requiredFixedCoreFunction{
		{Name: "initial_registration_json_string_v1", Arguments: "text", Result: "text"},
		{Name: "initial_registration_basis_json_v1", Arguments: "jsonb, boolean", Result: "text"},
		{Name: "initial_registration_binding_json_v1", Arguments: "jsonb", Result: "text"},
		{Name: "initial_registration_payload_json_v1", Arguments: "jsonb", Result: "text"},
	} {
		f.Migration = initialRegistrationMigration
		f.RequirePublicExecuteRevoked = true
		requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, f)
	}
	requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, requiredFixedCoreFunction{Migration: initialRegistrationMigration, Name: "canonical_initial_lineage_registration_apply_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true})
	for _, t := range []requiredFixedCoreTrigger{
		{Name: "canonical_initial_registration_immutable_rows", Function: "external_record_immutable", TriggerType: 27},
		{Name: "canonical_initial_registration_immutable_table", Function: "external_record_immutable", TriggerType: 34},
		{Name: "canonical_initial_registration_apply", Function: "canonical_initial_lineage_registration_apply_v1", TriggerType: 7},
		{Name: "evidence_governance_touch", Function: "evidence_governance_touch_v1", TriggerType: 4},
	} {
		t.Migration = initialRegistrationMigration
		t.Table = table
		requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, t)
	}
	for _, name := range []string{"event_id", "request_id", "claim_id", "lineage_key", "body", "binding_body", "grounding_body"} {
		requiredFixedCoreColumns = append(requiredFixedCoreColumns, requiredFixedCoreColumn{Migration: initialRegistrationMigration, Table: table, Name: name, DataType: "text", NotNull: true})
	}
	requiredFixedCoreColumns = append(requiredFixedCoreColumns,
		requiredFixedCoreColumn{Migration: initialRegistrationMigration, Table: table, Name: "recorded_at", DataType: "timestamp with time zone", NotNull: true, Default: "clock_timestamp()"},
		requiredFixedCoreColumn{Migration: initialRegistrationMigration, Table: "canonical_supersession_members", Name: "initial_registration_event_id", DataType: "text"},
		requiredFixedCoreColumn{Migration: initialRegistrationMigration, Table: "canonical_supersession_members", Name: "first_admission_event_id", DataType: "text"})
	for _, c := range []requiredFixedCoreConstraint{
		{Name: table + "_pkey", Type: "p", Definition: "PRIMARY KEY (event_id)"},
		{Name: table + "_request_id_key", Type: "u", Definition: "UNIQUE (request_id)"},
		{Name: table + "_claim_id_key", Type: "u", Definition: "UNIQUE (claim_id)"},
		{Name: table + "_lineage_key_key", Type: "u", Definition: "UNIQUE (lineage_key)"},
		{Name: "canonical_initial_registration_origin_key", Type: "u", Definition: "UNIQUE (event_id, claim_id, lineage_key)"},
		{Name: table + "_claim_id_fkey", Type: "f", Definition: "FOREIGN KEY (claim_id) REFERENCES canonical_graph_nodes(canonical_node_id)"},
		{Name: table + "_lineage_key_fkey", Type: "f", Definition: "FOREIGN KEY (lineage_key) REFERENCES canonical_supersession_lineages(lineage_key) DEFERRABLE INITIALLY DEFERRED", Deferrable: true, InitiallyDeferred: true},
		{Name: "canonical_initial_registration_digest", Type: "c", Definition: "CHECK ((event_id = ('initial-lineage-registration:v1:sha256:'::text || encode(sha256(convert_to(body, 'UTF8'::name)), 'hex'::text))))"},
	} {
		c.Migration = initialRegistrationMigration
		c.Table = table
		requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, c)
	}
	requiredFixedCoreConstraints = append(requiredFixedCoreConstraints,
		requiredFixedCoreConstraint{Migration: initialRegistrationMigration, Table: "canonical_supersession_members", Name: "canonical_supersession_members_initial_registration_fk", Type: "f", Definition: "FOREIGN KEY (initial_registration_event_id, canonical_node_id, lineage_key) REFERENCES canonical_initial_lineage_registrations(event_id, claim_id, lineage_key) DEFERRABLE INITIALLY DEFERRED", Deferrable: true, InitiallyDeferred: true},
		requiredFixedCoreConstraint{Migration: initialRegistrationMigration, Table: "canonical_supersession_members", Name: "canonical_supersession_members_origin_choice", Type: "c", Definition: "CHECK ((((initial_registration_event_id IS NULL) AND (first_admission_event_id IS NOT NULL)) OR ((initial_registration_event_id IS NOT NULL) AND (first_admission_event_id IS NULL) AND (NOT was_bootstrapped))))"})
}
