package migrations

const consistencyMigration = "000053_evidence_consistency_lifecycle.up.sql"

const consistencyContractMigration = "000054_evidence_consistency_contract.up.sql"

func init() {
	for _, name := range []string{"consistency_version_guard", "consistency_run_guard", "consistency_artifact_guard", "consistency_event_guard"} {
		requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, requiredFixedCoreFunction{Migration: consistencyContractMigration, Name: name, Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true})
	}
	for _, t := range []requiredFixedCoreTrigger{
		{Name: "consistency_version_insert", Table: "consistency_watch_versions", Function: "consistency_version_guard", TriggerType: 7},
		{Name: "consistency_run_insert", Table: "consistency_runs", Function: "consistency_run_guard", TriggerType: 7},
		{Name: "consistency_event_insert", Table: "consistency_events", Function: "consistency_event_guard", TriggerType: 7},
		{Name: "consistency_run_artifacts_complete", Table: "consistency_runs", Function: "consistency_artifact_guard", TriggerType: 5, Constraint: true, Deferrable: true, InitiallyDeferred: true},
		{Name: "consistency_artifact_manifest", Table: "consistency_run_artifacts", Function: "consistency_artifact_guard", TriggerType: 5, Constraint: true, Deferrable: true, InitiallyDeferred: true},
		{Name: "consistency_watches_immutable", Table: "consistency_watches", Function: "external_record_immutable", TriggerType: 58},
		{Name: "consistency_versions_immutable", Table: "consistency_watch_versions", Function: "external_record_immutable", TriggerType: 58},
		{Name: "consistency_runs_immutable", Table: "consistency_runs", Function: "external_record_immutable", TriggerType: 58},
		{Name: "consistency_artifacts_immutable", Table: "consistency_run_artifacts", Function: "external_record_immutable", TriggerType: 58},
		{Name: "consistency_events_immutable", Table: "consistency_events", Function: "external_record_immutable", TriggerType: 58},
	} {
		t.Migration = consistencyMigration
		requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, t)
	}
}
