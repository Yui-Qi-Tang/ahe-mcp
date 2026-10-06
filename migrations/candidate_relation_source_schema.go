package migrations

const candidateRelationSourceMigration = "000055_evidence_ingestion_candidate_relation_source.up.sql"

func init() {
	requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, requiredFixedCoreFunction{Migration: candidateRelationSourceMigration, Name: "canonical_contradiction_source_guard_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog", "row_security=off"}, RequirePublicExecuteRevoked: true})
	requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, requiredFixedCoreTrigger{Migration: candidateRelationSourceMigration, Name: "canonical_contradiction_source_guard", Table: "canonical_contradiction_proposals", Function: "canonical_contradiction_source_guard_v1", TriggerType: 1 | 2 | 4 | 16})
	requiredFixedCoreColumns = append(requiredFixedCoreColumns, requiredFixedCoreColumn{Migration: candidateRelationSourceMigration, Table: "canonical_contradiction_proposals", Name: "source_proposal_occurrence_id", DataType: "text"})
	requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, requiredFixedCoreConstraint{Migration: candidateRelationSourceMigration, Table: "canonical_contradiction_proposals", Name: "canonical_contradiction_source_fk", Type: "f", Definition: "FOREIGN KEY (source_proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id)"})
}
