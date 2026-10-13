package migrations

const governanceMigration = "000058_evidence_governance.up.sql"

func init() {
	for _, f := range []requiredFixedCoreFunction{
		{Name: "evidence_governance_lock_v1", Arguments: "regclass", Result: "bigint"},
		{Name: "evidence_governance_touch_v1", Result: "trigger"},
		{Name: "evidence_governance_event_guard_v1", Result: "trigger"},
	} {
		f.Migration = governanceMigration
		f.SecurityDefiner = true
		f.Config = []string{"search_path=pg_catalog", "row_security=off"}
		f.RequirePublicExecuteRevoked = true
		requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, f)
	}
	for _, table := range []string{"canonical_graph_nodes", "canonical_graph_edges", "canonical_derivations", "canonical_derivation_parents", "canonical_ordinary_admission_manifests", "canonical_ordinary_admission_node_bindings", "canonical_ordinary_admission_edge_bindings", "admission_decisions", "canonical_supersession_lineages", "canonical_supersession_members", "canonical_supersession_admission_events", "canonical_supersession_admission_head", "canonical_supersession_replacement_targets", "source_snapshots", "source_blobs", "source_file_snapshots", "extraction_runs", "extraction_attempts", "extraction_views", "proposal_occurrences", "canonical_withdrawal_events", "source_assessment_events"} {
		requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, requiredFixedCoreTrigger{Migration: governanceMigration, Name: "evidence_governance_touch", Table: table, Function: "evidence_governance_touch_v1", TriggerType: 2 | 4 | 8 | 16 | 32})
	}
	for _, table := range []string{"canonical_withdrawal_events", "source_assessment_events"} {
		for _, t := range []requiredFixedCoreTrigger{
			{Name: table + "_immutable_rows", Function: "external_record_immutable", TriggerType: 1 | 2 | 8 | 16},
			{Name: table + "_immutable_table", Function: "external_record_immutable", TriggerType: 2 | 32},
			{Name: table + "_review_guard", Function: "evidence_governance_event_guard_v1", TriggerType: 1 | 2 | 4},
		} {
			t.Migration = governanceMigration
			t.Table = table
			requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, t)
		}
		for _, c := range []requiredFixedCoreColumn{
			{Name: "event_id", DataType: "text", NotNull: true}, {Name: "request_id", DataType: "text", NotNull: true}, {Name: "target_id", DataType: "text", NotNull: true},
			{Name: "revision", DataType: "bigint", NotNull: true}, {Name: "previous_event_id", DataType: "text"}, {Name: "body", DataType: "text", NotNull: true},
			{Name: "recorded_at", DataType: "timestamp with time zone", NotNull: true, Default: "clock_timestamp()"},
		} {
			c.Migration = governanceMigration
			c.Table = table
			requiredFixedCoreColumns = append(requiredFixedCoreColumns, c)
		}
		for _, c := range []requiredFixedCoreConstraint{
			{Name: table + "_pkey", Type: "p", Definition: "PRIMARY KEY (event_id)"},
			{Name: table + "_request_id_key", Type: "u", Definition: "UNIQUE (request_id)"},
			{Name: table + "_target_id_revision_key", Type: "u", Definition: "UNIQUE (target_id, revision)"},
			{Name: table + "_previous_event_id_fkey", Type: "f", Definition: "FOREIGN KEY (previous_event_id) REFERENCES " + table + "(event_id)"},
			{Name: table + "_revision_check", Type: "c", Definition: "CHECK ((revision > 0))"},
			{Name: table + "_body_check", Type: "c", Definition: "CHECK (((octet_length(body) >= 1) AND (octet_length(body) <= 1048576)))"},
			{Name: table + "_request_id_check", Type: "c", Definition: "CHECK (((octet_length(request_id) >= 1) AND (octet_length(request_id) <= 200)))"},
			{Name: table + "_digest", Type: "c", Definition: "CHECK ((event_id = ('governance:sha256:'::text || encode(sha256(convert_to(body, 'UTF8'::name)), 'hex'::text))))"},
		} {
			c.Migration = governanceMigration
			c.Table = table
			requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, c)
		}
		target, column := "canonical_graph_nodes", "canonical_node_id"
		if table == "source_assessment_events" {
			target, column = "source_snapshots", "source_snapshot_id"
		}
		requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, requiredFixedCoreConstraint{Migration: governanceMigration, Table: table, Name: table + "_target_id_fkey", Type: "f", Definition: "FOREIGN KEY (target_id) REFERENCES " + target + "(" + column + ")"})
	}
	for _, c := range []requiredFixedCoreColumn{
		{Name: "singleton", DataType: "boolean", NotNull: true},
		{Name: "store_id", DataType: "text", NotNull: true, Default: "('store:'::text || (gen_random_uuid())::text)"},
		{Name: "revision", DataType: "bigint", NotNull: true, Default: "0"},
	} {
		c.Migration = governanceMigration
		c.Table = "evidence_governance_head"
		requiredFixedCoreColumns = append(requiredFixedCoreColumns, c)
	}
	for _, c := range []requiredFixedCoreConstraint{
		{Name: "evidence_governance_head_pkey", Type: "p", Definition: "PRIMARY KEY (singleton)"},
		{Name: "evidence_governance_head_store_id_key", Type: "u", Definition: "UNIQUE (store_id)"},
		{Name: "evidence_governance_head_singleton_check", Type: "c", Definition: "CHECK (singleton)"},
		{Name: "evidence_governance_head_revision_check", Type: "c", Definition: "CHECK ((revision >= 0))"},
	} {
		c.Migration = governanceMigration
		c.Table = "evidence_governance_head"
		requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, c)
	}
}
