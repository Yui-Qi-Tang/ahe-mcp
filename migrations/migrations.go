// Package migrations embeds and applies AHE PostgreSQL migrations.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql
var upFiles embed.FS

var requiredTablesByMigration = map[string][]string{
	"000001_evidence_ingestion_slice1.up.sql": {
		"source_blobs",
		"source_snapshots",
		"extraction_views",
		"span_catalog_entries",
		"source_intake_requests",
		"extractor_definitions",
		"extraction_runs",
		"extraction_attempts",
		"proposal_batches",
		"proposal_occurrences",
	},
	"000002_evidence_ingestion_admission_slice.up.sql": {
		"canonical_graph_nodes",
		"canonical_graph_edges",
		"admission_decisions",
	},
	"000003_evidence_ingestion_repository_snapshots.up.sql": {
		"repository_snapshots",
		"source_file_snapshots",
		"repository_snapshot_intake_requests",
	},
	"000004_evidence_ingestion_repository_extraction_runs.up.sql": {},
	"000005_evidence_ingestion_repository_run_requests.up.sql": {
		"repository_extraction_run_requests",
	},
	"000006_evidence_ingestion_source_generations.up.sql": {
		"repository_source_generations",
		"repository_source_heads",
		"repository_generation_activation_requests",
	},
	"000007_evidence_ingestion_generation_reconciliation.up.sql": {
		"repository_generation_reconciliations",
		"repository_generation_proposal_reconciliations",
	},
	"000008_evidence_ingestion_repository_change_observations.up.sql": {
		"repository_change_observations",
		"repository_change_observation_requests",
	},
	"000009_evidence_ingestion_repository_extraction_work.up.sql": {
		"repository_extraction_work_items",
		"repository_extraction_work_schedule_requests",
		"repository_extraction_work_claim_requests",
	},
	"000010_evidence_ingestion_repository_extraction_work_results.up.sql": {
		"repository_extraction_work_finish_requests",
	},
	"000011_evidence_ingestion_repository_extraction_work_claim_attempts.up.sql": {
		"repository_extraction_work_claim_attempts",
	},
	"000012_evidence_ingestion_repository_extraction_work_recovery.up.sql": {
		"repository_extraction_work_recovery_requests",
	},
	"000013_evidence_ingestion_repository_extraction_work_retry.up.sql": {
		"repository_extraction_work_retry_requests",
	},
	"000014_evidence_ingestion_repository_extraction_work_lease_renewal.up.sql": {
		"repository_extraction_work_lease_renewal_requests",
	},
	"000015_evidence_ingestion_repository_extraction_work_execution.up.sql": {
		"repository_extraction_work_execution_requests",
	},
	"000016_evidence_ingestion_repository_extraction_work_execution_heartbeat.up.sql": {},
	"000017_evidence_ingestion_repository_extraction_worker_tick.up.sql": {
		"repository_extraction_worker_tick_requests",
	},
	"000018_evidence_ingestion_repository_extraction_work_failure_policy.up.sql": {
		"repository_extraction_work_failure_policy_decisions",
	},
	"000019_evidence_ingestion_repository_extraction_work_retry_decision.up.sql": {
		"repository_extraction_work_retry_decision_consumptions",
	},
	"000020_evidence_ingestion_repository_extraction_work_execution_repair.up.sql": {
		"repository_extraction_work_execution_repairs",
	},
	"000021_evidence_ingestion_repository_extraction_work_expired_execution_discovery.up.sql": {},
	"000022_evidence_ingestion_repository_extraction_work_retry_controller_tick.up.sql": {
		"repository_extraction_work_retry_controller_tick_requests",
		"repository_extraction_work_retry_controller_tick_items",
	},
	"000023_evidence_ingestion_repository_extraction_work_expired_maintenance_tick.up.sql": {
		"repository_extraction_work_expired_maintenance_tick_requests",
		"repository_extraction_work_expired_maintenance_tick_items",
	},
	"000024_evidence_ingestion_source_stream_arbitration.up.sql": {
		"evidence_ingestion_request_serializations",
		"git_repository_source_streams",
		"repository_source_streams",
	},
	"000025_detective_workspace_registry.up.sql": {
		"detective_workspaces",
		"detective_workspace_sources",
		"detective_workspace_registration_requests",
	},
	"000026_detective_orchestration_audit.up.sql": {
		"detective_orchestration_runs",
		"detective_orchestration_steps",
		"detective_orchestration_transition_requests",
	},
	"000027_detective_planner_recommendation_consumption.up.sql": {
		"detective_planner_recommendation_consumptions",
	},
	"000028_detective_periodic_runtime.up.sql": {
		"detective_periodic_source_cursors",
		"detective_periodic_source_cycles",
	},
	"000029_detective_connector_inbox_receipt.up.sql": {
		"detective_connector_inbox_deliveries",
		"detective_connector_inbox_receive_requests",
	},
	"000030_detective_connector_text_processing.up.sql": {
		"detective_connector_inbox_processing_work",
		"detective_connector_inbox_processing_attempts",
		"detective_connector_inbox_processing_requests",
		"detective_connector_inbox_processing_recovery_requests",
	},
	"000031_evidence_ingestion_repository_delta_extraction.up.sql": {
		"repository_delta_extractions",
	},
	"000032_evidence_query_p5_indexes.up.sql":                                        {},
	"000033_evidence_query_lexical_recovery.up.sql":                                  {},
	"000034_evidence_ingestion_repository_extraction_execution_failure_audit.up.sql": {},
	"000035_detective_mcp_read_runtime.up.sql": {
		"detective_mcp_read_source_bindings",
		"detective_mcp_read_collection_cycles",
	},
	"000036_evidence_ingestion_derivations.up.sql": {
		"canonical_derivations",
		"canonical_derivation_parents",
	},
	"000037_evidence_ingestion_external_source_receipts.up.sql": {
		"external_source_intake_receipts",
	},
	"000038_evidence_ingestion_producer_session_ref.up.sql":         {},
	"000039_evidence_ingestion_external_agent_core_contract.up.sql": {},
	"000040_evidence_ingestion_canonical_contradictions.up.sql": {
		"canonical_contradiction_proposals",
		"canonical_contradiction_admission_decisions",
	},
	"000041_evidence_ingestion_canonical_supersessions.up.sql": {
		"canonical_supersession_proposals",
		"canonical_supersession_admission_decisions",
	},
	"000042_evidence_ingestion_canonical_supersession_v2.up.sql": {
		"canonical_supersession_lineages",
		"canonical_supersession_admission_events",
		"canonical_supersession_admission_head",
		"canonical_supersession_members",
		"canonical_supersession_replacement_targets",
	},
	"000043_evidence_ingestion_source_run_request_identity.up.sql": {},
	"000044_evidence_ingestion_ordinary_admission_integrity.up.sql": {
		"canonical_ordinary_admission_manifests",
		"canonical_ordinary_admission_node_bindings",
		"canonical_ordinary_admission_edge_bindings",
	},
	"000045_evidence_ingestion_source_claim_review_binding.up.sql": {
		"canonical_source_claim_review_bindings",
	},
}

var requiredTables = []string{
	"source_blobs",
	"source_snapshots",
	"extraction_views",
	"span_catalog_entries",
	"source_intake_requests",
	"extractor_definitions",
	"extraction_runs",
	"extraction_attempts",
	"proposal_batches",
	"proposal_occurrences",
	"canonical_graph_nodes",
	"canonical_graph_edges",
	"admission_decisions",
	"canonical_derivations",
	"canonical_derivation_parents",
	"repository_snapshots",
	"source_file_snapshots",
	"repository_snapshot_intake_requests",
	"repository_extraction_run_requests",
	"repository_source_generations",
	"repository_source_heads",
	"repository_generation_activation_requests",
	"repository_generation_reconciliations",
	"repository_generation_proposal_reconciliations",
	"repository_change_observations",
	"repository_change_observation_requests",
	"repository_extraction_work_items",
	"repository_extraction_work_schedule_requests",
	"repository_extraction_work_claim_requests",
	"repository_extraction_work_finish_requests",
	"repository_extraction_work_claim_attempts",
	"repository_extraction_work_recovery_requests",
	"repository_extraction_work_retry_requests",
	"repository_extraction_work_lease_renewal_requests",
	"repository_extraction_work_execution_requests",
	"repository_extraction_worker_tick_requests",
	"repository_extraction_work_failure_policy_decisions",
	"repository_extraction_work_retry_decision_consumptions",
	"repository_extraction_work_execution_repairs",
	"repository_extraction_work_retry_controller_tick_requests",
	"repository_extraction_work_retry_controller_tick_items",
	"repository_extraction_work_expired_maintenance_tick_requests",
	"repository_extraction_work_expired_maintenance_tick_items",
	"evidence_ingestion_request_serializations",
	"git_repository_source_streams",
	"repository_source_streams",
	"detective_workspaces",
	"detective_workspace_sources",
	"detective_workspace_registration_requests",
	"detective_orchestration_runs",
	"detective_orchestration_steps",
	"detective_orchestration_transition_requests",
	"detective_planner_recommendation_consumptions",
	"detective_periodic_source_cursors",
	"detective_periodic_source_cycles",
	"detective_connector_inbox_deliveries",
	"detective_connector_inbox_receive_requests",
	"detective_connector_inbox_processing_work",
	"detective_connector_inbox_processing_attempts",
	"detective_connector_inbox_processing_requests",
	"detective_connector_inbox_processing_recovery_requests",
	"repository_delta_extractions",
	"detective_mcp_read_source_bindings",
	"detective_mcp_read_collection_cycles",
	"external_source_intake_receipts",
	"canonical_contradiction_proposals",
	"canonical_contradiction_admission_decisions",
	"canonical_supersession_lineages",
	"canonical_supersession_admission_events",
	"canonical_supersession_admission_head",
	"canonical_supersession_members",
	"canonical_supersession_replacement_targets",
	"canonical_ordinary_admission_manifests",
	"canonical_ordinary_admission_node_bindings",
	"canonical_ordinary_admission_edge_bindings",
	"canonical_source_claim_review_bindings",
}

type requiredAdmissionFunction struct {
	Name            string
	Arguments       string
	Result          string
	SourceSHA256    string
	SecurityDefiner bool
	SearchPath      string
	OwnerTable      string
}

var requiredAdmissionFunctions = []requiredAdmissionFunction{
	{
		Name:            "canonical_admission_forbid_mutation_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:6f8dc655ad49d21b54d0588049ec6241aace2cb9bdcc18afaa898bf35ec201dc",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_admission_guard_proposal_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:2aac324a0600e1c3007adec93b4621ba9b5673652f5af7645e271bf7be8c3075",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:         "canonical_ordinary_admission_assert_decision_v1",
		Arguments:    "text",
		Result:       "void",
		SourceSHA256: "sha256:d288459a62116f9d2ae18daa152cf5b05179fa3a1a00cab5f3483b398b7fb60e",
		OwnerTable:   "canonical_ordinary_admission_manifests",
	},
	{
		Name:         "canonical_ordinary_admission_assert_node_v1",
		Arguments:    "text",
		Result:       "void",
		SourceSHA256: "sha256:32889104f911e14aff9aa3ddeefa1c5262d9bf963c0d009eee24af93df286883",
		OwnerTable:   "canonical_ordinary_admission_manifests",
	},
	{
		Name:         "canonical_ordinary_admission_assert_edge_v1",
		Arguments:    "text",
		Result:       "void",
		SourceSHA256: "sha256:8997cf90ddeb950488b67f19e6d3a48c101628d994dcab3b8a59736151bd8826",
		OwnerTable:   "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_ordinary_admission_decision_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:3727452cfd41faa605b09a583b866fd532ccd1dbdb39ad932e691539b66b3fd5",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_ordinary_admission_manifest_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:98792e01e40b9571ebaeed06c538e8e62e6f408ba33698145fb23f761f11b4d5",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_ordinary_admission_node_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:71c3f03e178de1372b32a14f7da1db2edf05ccdf753b3ae16691f2f6775a179a",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_ordinary_admission_edge_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:6dc0a30f77345d7b8a4c8637cd9ffc211d4033ab2e90bf2d727dc44ba8fad8a2",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_ordinary_admission_proposal_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:930f2e384056fad9d54ccff8b7cb0fc72090d95dc09d4a82b137c6ee921e852e",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_ordinary_admission_derivation_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:1376d6495b53d16aa1020a77141644b1e513ee9773725766d7b7d8edcc7ec30d",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:            "canonical_supersession_edge_authority_dispatch_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:fc46b2b754def60cd2d6dca37453d33ceb7a93915ad964062cc392302b3682d7",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_ordinary_admission_manifests",
	},
	{
		Name:         "canonical_source_claim_review_binding_assert_v1",
		Arguments:    "text",
		Result:       "void",
		SourceSHA256: "sha256:ab1061047a89ef005505209098907a5d271309d2a3059feabe006217ee2a4365",
		OwnerTable:   "canonical_source_claim_review_bindings",
	},
	{
		Name:            "canonical_source_claim_review_binding_trigger_v1",
		Result:          "trigger",
		SourceSHA256:    "sha256:4093d8587b4c24d13739f794eba4e0773b2d374f8e8b9e927c0e098fb88befad",
		SecurityDefiner: true,
		SearchPath:      "search_path=pg_catalog",
		OwnerTable:      "canonical_source_claim_review_bindings",
	},
}

type requiredAdmissionTrigger struct {
	Name              string
	Table             string
	Function          string
	TriggerType       int
	Constraint        bool
	Deferrable        bool
	InitiallyDeferred bool
}

const (
	beforeRowUpdateDelete      = 1 | 2 | 8 | 16
	beforeStatementTruncate    = 2 | 32
	afterRowInsertUpdateDelete = 1 | 4 | 8 | 16
)

var requiredAdmissionTriggers = []requiredAdmissionTrigger{
	{Name: "canonical_graph_nodes_append_only", Table: "canonical_graph_nodes", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_graph_nodes_forbid_truncate", Table: "canonical_graph_nodes", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_graph_edges_append_only", Table: "canonical_graph_edges", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_graph_edges_forbid_truncate", Table: "canonical_graph_edges", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "admission_decisions_append_only", Table: "admission_decisions", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "admission_decisions_forbid_truncate", Table: "admission_decisions", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_derivations_append_only", Table: "canonical_derivations", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_derivations_forbid_truncate", Table: "canonical_derivations", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_derivation_parents_append_only", Table: "canonical_derivation_parents", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_derivation_parents_forbid_truncate", Table: "canonical_derivation_parents", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_ordinary_admission_manifests_append_only", Table: "canonical_ordinary_admission_manifests", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_ordinary_admission_manifests_forbid_truncate", Table: "canonical_ordinary_admission_manifests", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_ordinary_admission_node_bindings_append_only", Table: "canonical_ordinary_admission_node_bindings", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_ordinary_admission_node_bindings_forbid_truncate", Table: "canonical_ordinary_admission_node_bindings", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_ordinary_admission_edge_bindings_append_only", Table: "canonical_ordinary_admission_edge_bindings", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_ordinary_admission_edge_bindings_forbid_truncate", Table: "canonical_ordinary_admission_edge_bindings", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "proposal_occurrences_terminal_guard", Table: "proposal_occurrences", Function: "canonical_admission_guard_proposal_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "proposal_occurrences_forbid_truncate", Table: "proposal_occurrences", Function: "canonical_admission_guard_proposal_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_ordinary_admission_decisions_authority", Table: "admission_decisions", Function: "canonical_ordinary_admission_decision_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_manifests_authority", Table: "canonical_ordinary_admission_manifests", Function: "canonical_ordinary_admission_manifest_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_node_bindings_authority", Table: "canonical_ordinary_admission_node_bindings", Function: "canonical_ordinary_admission_manifest_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_edge_bindings_authority", Table: "canonical_ordinary_admission_edge_bindings", Function: "canonical_ordinary_admission_manifest_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_nodes_authority", Table: "canonical_graph_nodes", Function: "canonical_ordinary_admission_node_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_edges_authority", Table: "canonical_graph_edges", Function: "canonical_ordinary_admission_edge_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_proposals_authority", Table: "proposal_occurrences", Function: "canonical_ordinary_admission_proposal_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_derivations_authority", Table: "canonical_derivations", Function: "canonical_ordinary_admission_derivation_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_ordinary_admission_derivation_parents_authority", Table: "canonical_derivation_parents", Function: "canonical_ordinary_admission_derivation_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "canonical_source_claim_review_bindings_append_only", Table: "canonical_source_claim_review_bindings", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeRowUpdateDelete},
	{Name: "canonical_source_claim_review_bindings_forbid_truncate", Table: "canonical_source_claim_review_bindings", Function: "canonical_admission_forbid_mutation_v1", TriggerType: beforeStatementTruncate},
	{Name: "canonical_source_claim_review_bindings_authority", Table: "canonical_source_claim_review_bindings", Function: "canonical_source_claim_review_binding_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Name: "admission_decisions_source_claim_review_authority", Table: "admission_decisions", Function: "canonical_source_claim_review_binding_trigger_v1", TriggerType: afterRowInsertUpdateDelete, Constraint: true, Deferrable: true, InitiallyDeferred: true},
}

type requiredAdmissionIndex struct {
	Name      string
	Table     string
	KeyColumn string
	Predicate string
}

var requiredAdmissionIndexes = []requiredAdmissionIndex{
	{
		Name:      "canonical_ordinary_admission_node_materializer_uq",
		Table:     "canonical_ordinary_admission_node_bindings",
		KeyColumn: "canonical_node_id",
		Predicate: "materialization = 'materialized'::text",
	},
	{
		Name:      "canonical_ordinary_admission_edge_materializer_uq",
		Table:     "canonical_ordinary_admission_edge_bindings",
		KeyColumn: "canonical_edge_id",
		Predicate: "materialization = 'materialized'::text",
	},
}

type requiredCanonicalSupersessionTrigger struct {
	Name     string
	Table    string
	Function string
}

var requiredCanonicalSupersessionTriggers = []requiredCanonicalSupersessionTrigger{
	{
		Name:     "canonical_supersession_lineages_authority_trigger",
		Table:    "canonical_supersession_lineages",
		Function: "canonical_supersession_lineage_authority_trigger",
	},
	{
		Name:     "canonical_supersession_events_authority_trigger",
		Table:    "canonical_supersession_admission_events",
		Function: "canonical_supersession_event_authority_trigger",
	},
	{
		Name:     "canonical_supersession_members_authority_trigger",
		Table:    "canonical_supersession_members",
		Function: "canonical_supersession_member_authority_trigger",
	},
	{
		Name:     "canonical_supersession_head_authority_trigger",
		Table:    "canonical_supersession_admission_head",
		Function: "canonical_supersession_head_authority_trigger",
	},
	{
		Name:     "canonical_supersession_nodes_authority_trigger",
		Table:    "canonical_graph_nodes",
		Function: "canonical_supersession_node_authority_trigger",
	},
	{
		Name:     "canonical_supersession_decisions_authority_trigger",
		Table:    "admission_decisions",
		Function: "canonical_supersession_decision_authority_trigger",
	},
	{
		Name:     "canonical_supersession_proposals_authority_trigger",
		Table:    "proposal_occurrences",
		Function: "canonical_supersession_proposal_authority_trigger",
	},
	{
		Name:     "canonical_supersession_edges_authority_trigger",
		Table:    "canonical_graph_edges",
		Function: "canonical_supersession_edge_authority_dispatch_v1",
	},
	{
		Name:     "canonical_supersession_targets_authority_trigger",
		Table:    "canonical_supersession_replacement_targets",
		Function: "canonical_supersession_target_authority_trigger",
	},
}

type requiredCanonicalSupersessionFunction struct {
	Name         string
	Arguments    string
	Result       string
	SourceSHA256 string
	Volatility   string
	Strict       bool
}

var requiredCanonicalSupersessionFunctions = []requiredCanonicalSupersessionFunction{
	{
		Name:         "canonical_supersession_stable_id",
		Arguments:    "text, text[]",
		Result:       "text",
		SourceSHA256: "sha256:41dae87ad6770731024e5d77db72bb2ae28075f69e022161468aa0e7933c1174",
		Volatility:   "i",
		Strict:       true,
	},
	{
		Name:         "canonical_supersession_assert_edge",
		Arguments:    "text",
		Result:       "void",
		SourceSHA256: "sha256:6668d32c3d55ab9c349f61ded365deff9bc729be7070b6ebc01491aa900ff147",
	},
	{
		Name:         "canonical_supersession_assert_target",
		Arguments:    "text, text, text",
		Result:       "void",
		SourceSHA256: "sha256:b1afcb69ee285a3e88496382b9baef3263d8af0dbf6809e4256a820e881e086b",
	},
	{
		Name:         "canonical_supersession_assert_event",
		Arguments:    "text",
		Result:       "void",
		SourceSHA256: "sha256:6ca36ecdbd3e9617bb0f05041326e8ba6037bd7af5624c64d7dcc9b3b0995713",
	},
	{
		Name:         "canonical_supersession_lineage_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:e9ab006b6f1017494e063d8b8c7ee21c5ab21158cdbe1c230d3b9aeb7c60d772",
	},
	{
		Name:         "canonical_supersession_event_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:41cd6f4752bc891c45b08fd2a08d0ff533904c8d79462b5927e6c9ef14fb5d0e",
	},
	{
		Name:         "canonical_supersession_member_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:f36bec9612b5216112c34f95a90b74054456d2f1737d843cfc74c326f13604c6",
	},
	{
		Name:         "canonical_supersession_head_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:3f041d59ae1fd1fa3680333f072bb73a1daa186d7cb7877441fc96f0f6eab705",
	},
	{
		Name:         "canonical_supersession_node_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:137136ee32f037fdddcd40bee6296cfd6cdb7578b7a49b585e6a333cbf4de3c6",
	},
	{
		Name:         "canonical_supersession_decision_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:fba72d4168898d10e1c8140a9db3ca31125c160e086306d827a534de135d76f6",
	},
	{
		Name:         "canonical_supersession_proposal_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:0e4bb17c57aa03549744e39815f97c6fab4359a9682d1d2afaa4730c38a51679",
	},
	{
		Name:         "canonical_supersession_edge_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:872f58dc2bed5445290fdafd9656cfa75d91697041ba14a44bda8c02147f719e",
	},
	{
		Name:         "canonical_supersession_target_authority_trigger",
		Result:       "trigger",
		SourceSHA256: "sha256:23e8601a652a80f465415ba78e903f45ff6cc36cb5ab6772c2c86a9f278eaebc",
	},
}

type requiredCanonicalSupersessionConstraint struct {
	Name              string
	Table             string
	Type              string
	Deferrable        bool
	InitiallyDeferred bool
	Definition        string
}

var requiredCanonicalSupersessionConstraints = []requiredCanonicalSupersessionConstraint{
	{
		Name:       "canonical_graph_edges_exact_origin_ck",
		Table:      "canonical_graph_edges",
		Type:       "c",
		Definition: "CHECK ((num_nonnulls(origin_proposal_occurrence_id, origin_canonical_contradiction_proposal_id) = 1))",
	},
	{
		Name:       "admission_decisions_supersession_binding_uq",
		Table:      "admission_decisions",
		Type:       "u",
		Definition: "UNIQUE (admission_decision_id, proposal_occurrence_id, canonical_ref, outcome)",
	},
	{
		Name:       "canonical_supersession_lineages_pkey",
		Table:      "canonical_supersession_lineages",
		Type:       "p",
		Definition: "PRIMARY KEY (lineage_key)",
	},
	{
		Name:       "canonical_supersession_lineages_basis_uq",
		Table:      "canonical_supersession_lineages",
		Type:       "u",
		Definition: "UNIQUE (source_system, source_namespace, object_type, object_id, slot_kind, slot_id)",
	},
	{
		Name:       "canonical_supersession_admission_events_pkey",
		Table:      "canonical_supersession_admission_events",
		Type:       "p",
		Definition: "PRIMARY KEY (event_id)",
	},
	{
		Name:       "canonical_supersession_events_revision_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (revision)",
	},
	{
		Name:       "canonical_supersession_events_previous_event_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (previous_event_id)",
	},
	{
		Name:       "canonical_supersession_events_replacement_node_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (replacement_node_id)",
	},
	{
		Name:       "canonical_supersession_events_proposal_occurrence_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (proposal_occurrence_id)",
	},
	{
		Name:       "canonical_supersession_events_admission_decision_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (admission_decision_id)",
	},
	{
		Name:       "canonical_supersession_events_event_revision_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (event_id, revision)",
	},
	{
		Name:       "canonical_supersession_events_lineage_replacement_uq",
		Table:      "canonical_supersession_admission_events",
		Type:       "u",
		Definition: "UNIQUE (event_id, lineage_key, replacement_node_id)",
	},
	{
		Name:       "canonical_supersession_events_event_id_hash_ck",
		Table:      "canonical_supersession_admission_events",
		Type:       "c",
		Definition: "CHECK ((event_id = ('admission-event:v2:'::text || event_payload_hash)))",
	},
	{
		Name:       "canonical_supersession_events_previous_ck",
		Table:      "canonical_supersession_admission_events",
		Type:       "c",
		Definition: "CHECK ((((revision = 1) AND (previous_revision = 0) AND (previous_event_id IS NULL)) OR ((revision > 1) AND (previous_event_id IS NOT NULL))))",
	},
	{
		Name:       "canonical_supersession_events_previous_fk",
		Table:      "canonical_supersession_admission_events",
		Type:       "f",
		Definition: "FOREIGN KEY (previous_event_id, previous_revision) REFERENCES canonical_supersession_admission_events(event_id, revision)",
	},
	{
		Name:  "canonical_supersession_events_admission_fk",
		Table: "canonical_supersession_admission_events",
		Type:  "f",
		Definition: "FOREIGN KEY (admission_decision_id, proposal_occurrence_id, replacement_node_id, admission_outcome) " +
			"REFERENCES admission_decisions(admission_decision_id, proposal_occurrence_id, canonical_ref, outcome)",
	},
	{
		Name:       "canonical_supersession_admission_head_pkey",
		Table:      "canonical_supersession_admission_head",
		Type:       "p",
		Definition: "PRIMARY KEY (chain_key)",
	},
	{
		Name:       "canonical_supersession_head_event_uq",
		Table:      "canonical_supersession_admission_head",
		Type:       "u",
		Definition: "UNIQUE (head_event_id)",
	},
	{
		Name:       "canonical_supersession_head_coordinate_ck",
		Table:      "canonical_supersession_admission_head",
		Type:       "c",
		Definition: "CHECK ((((revision = 0) AND (head_event_id IS NULL)) OR ((revision > 0) AND (head_event_id IS NOT NULL))))",
	},
	{
		Name:              "canonical_supersession_head_event_fk",
		Table:             "canonical_supersession_admission_head",
		Type:              "f",
		Deferrable:        true,
		InitiallyDeferred: true,
		Definition: "FOREIGN KEY (head_event_id, revision) " +
			"REFERENCES canonical_supersession_admission_events(event_id, revision) " +
			"DEFERRABLE INITIALLY DEFERRED",
	},
	{
		Name:       "canonical_supersession_members_pkey",
		Table:      "canonical_supersession_members",
		Type:       "p",
		Definition: "PRIMARY KEY (canonical_node_id)",
	},
	{
		Name:       "canonical_supersession_members_lineage_node_uq",
		Table:      "canonical_supersession_members",
		Type:       "u",
		Definition: "UNIQUE (lineage_key, canonical_node_id)",
	},
	{
		Name:       "canonical_supersession_members_node_fk",
		Table:      "canonical_supersession_members",
		Type:       "f",
		Definition: "FOREIGN KEY (canonical_node_id) REFERENCES canonical_graph_nodes(canonical_node_id)",
	},
	{
		Name:       "canonical_supersession_members_lineage_fk",
		Table:      "canonical_supersession_members",
		Type:       "f",
		Definition: "FOREIGN KEY (lineage_key) REFERENCES canonical_supersession_lineages(lineage_key)",
	},
	{
		Name:       "canonical_supersession_members_event_fk",
		Table:      "canonical_supersession_members",
		Type:       "f",
		Definition: "FOREIGN KEY (first_admission_event_id) REFERENCES canonical_supersession_admission_events(event_id)",
	},
	{
		Name:              "canonical_supersession_event_replacement_member_fk",
		Table:             "canonical_supersession_admission_events",
		Type:              "f",
		Deferrable:        true,
		InitiallyDeferred: true,
		Definition: "FOREIGN KEY (lineage_key, replacement_node_id) " +
			"REFERENCES canonical_supersession_members(lineage_key, canonical_node_id) " +
			"DEFERRABLE INITIALLY DEFERRED",
	},
	{
		Name:       "canonical_supersession_replacement_targets_pkey",
		Table:      "canonical_supersession_replacement_targets",
		Type:       "p",
		Definition: "PRIMARY KEY (event_id, target_node_id)",
	},
	{
		Name:       "canonical_supersession_targets_distinct_nodes_ck",
		Table:      "canonical_supersession_replacement_targets",
		Type:       "c",
		Definition: "CHECK ((replacement_node_id <> target_node_id))",
	},
	{
		Name:       "canonical_supersession_targets_edge_uq",
		Table:      "canonical_supersession_replacement_targets",
		Type:       "u",
		Definition: "UNIQUE (canonical_edge_id)",
	},
	{
		Name:       "canonical_supersession_targets_edge_fk",
		Table:      "canonical_supersession_replacement_targets",
		Type:       "f",
		Definition: "FOREIGN KEY (canonical_edge_id) REFERENCES canonical_graph_edges(canonical_edge_id)",
	},
	{
		Name:  "canonical_supersession_targets_event_fk",
		Table: "canonical_supersession_replacement_targets",
		Type:  "f",
		Definition: "FOREIGN KEY (event_id, lineage_key, replacement_node_id) " +
			"REFERENCES canonical_supersession_admission_events(event_id, lineage_key, replacement_node_id)",
	},
	{
		Name:  "canonical_supersession_targets_member_fk",
		Table: "canonical_supersession_replacement_targets",
		Type:  "f",
		Definition: "FOREIGN KEY (lineage_key, target_node_id) " +
			"REFERENCES canonical_supersession_members(lineage_key, canonical_node_id)",
	},
}

var requiredAdmissionConstraints = []requiredCanonicalSupersessionConstraint{
	{
		Name:       "canonical_ordinary_admission_manifests_pkey",
		Table:      "canonical_ordinary_admission_manifests",
		Type:       "p",
		Definition: "PRIMARY KEY (admission_decision_id)",
	},
	{
		Name:       "canonical_ordinary_admission_manifests_proposal_uq",
		Table:      "canonical_ordinary_admission_manifests",
		Type:       "u",
		Definition: "UNIQUE (proposal_occurrence_id)",
	},
	{
		Name:       "canonical_ordinary_admission_manifests_contract_ck",
		Table:      "canonical_ordinary_admission_manifests",
		Type:       "c",
		Definition: "CHECK ((contract_version = 'ordinary-admission/v1'::text))",
	},
	{
		Name:       "canonical_ordinary_admission_manifests_kind_ck",
		Table:      "canonical_ordinary_admission_manifests",
		Type:       "c",
		Definition: "CHECK ((mutation_kind = ANY (ARRAY['source_backed_claim'::text, 'derived_claim'::text])))",
	},
	{
		Name:       "canonical_ordinary_admission_manifests_outcome_ck",
		Table:      "canonical_ordinary_admission_manifests",
		Type:       "c",
		Definition: "CHECK ((admission_outcome = 'admitted'::text))",
	},
	{
		Name:  "canonical_ordinary_admission_manifests_decision_binding_fk",
		Table: "canonical_ordinary_admission_manifests",
		Type:  "f",
		Definition: "FOREIGN KEY (admission_decision_id, proposal_occurrence_id, canonical_ref, admission_outcome) " +
			"REFERENCES admission_decisions(admission_decision_id, proposal_occurrence_id, canonical_ref, outcome)",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_pkey",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "p",
		Definition: "PRIMARY KEY (admission_decision_id, binding_role, binding_position)",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_decision_node_uq",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "u",
		Definition: "UNIQUE (admission_decision_id, canonical_node_id)",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_role_ck",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "c",
		Definition: "CHECK ((binding_role = ANY (ARRAY['canonical_ref'::text, 'raw_evidence'::text])))",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_position_ck",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "c",
		Definition: "CHECK ((binding_position >= 0))",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_materialization_ck",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "c",
		Definition: "CHECK ((materialization = ANY (ARRAY['materialized'::text, 'reused'::text])))",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_manifest_fk",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "f",
		Definition: "FOREIGN KEY (admission_decision_id) REFERENCES canonical_ordinary_admission_manifests(admission_decision_id)",
	},
	{
		Name:       "canonical_ordinary_admission_node_bindings_node_fk",
		Table:      "canonical_ordinary_admission_node_bindings",
		Type:       "f",
		Definition: "FOREIGN KEY (canonical_node_id) REFERENCES canonical_graph_nodes(canonical_node_id)",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_pkey",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "p",
		Definition: "PRIMARY KEY (admission_decision_id, binding_position)",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_decision_edge_uq",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "u",
		Definition: "UNIQUE (admission_decision_id, canonical_edge_id)",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_role_ck",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "c",
		Definition: "CHECK ((binding_role = ANY (ARRAY['supports_claim'::text, 'derived_from'::text])))",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_position_ck",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "c",
		Definition: "CHECK ((binding_position >= 0))",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_materialization_ck",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "c",
		Definition: "CHECK ((materialization = ANY (ARRAY['materialized'::text, 'reused'::text])))",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_manifest_fk",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "f",
		Definition: "FOREIGN KEY (admission_decision_id) REFERENCES canonical_ordinary_admission_manifests(admission_decision_id)",
	},
	{
		Name:       "canonical_ordinary_admission_edge_bindings_edge_fk",
		Table:      "canonical_ordinary_admission_edge_bindings",
		Type:       "f",
		Definition: "FOREIGN KEY (canonical_edge_id) REFERENCES canonical_graph_edges(canonical_edge_id)",
	},
	{
		Name:       "admission_decisions_review_binding_contract_ck",
		Table:      "admission_decisions",
		Type:       "c",
		Definition: "CHECK (((review_binding_contract_version IS NULL) OR (review_binding_contract_version = 'reviewed-source-claim-admission/v1'::text)))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_pkey",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "p",
		Definition: "PRIMARY KEY (admission_decision_id)",
	},
	{
		Name:       "canonical_source_claim_review_bindings_proposal_uq",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "u",
		Definition: "UNIQUE (proposal_occurrence_id)",
	},
	{
		Name:       "canonical_source_claim_review_bindings_contract_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((contract_version = 'reviewed-source-claim-admission/v1'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_review_contract_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((review_contract_version = 'reviewable-ingestion/v1'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_effect_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((proposed_effect = 'admit-source-backed-statement/v1'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_receipt_id_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((submission_receipt_id ~ '^submission-receipt:v1:sha256:[0-9a-f]{64}$'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_manifest_id_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((proposal_manifest_id ~ '^proposal-manifest:v1:sha256:[0-9a-f]{64}$'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_basis_id_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((proposal_basis_id ~ '^proposal-basis:v1:sha256:[0-9a-f]{64}$'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_package_id_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((review_package_id ~ '^review-package:v1:sha256:[0-9a-f]{64}$'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_display_uq",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "u",
		Definition: "UNIQUE (review_display_artifact_id)",
	},
	{
		Name:       "canonical_source_claim_review_bindings_display_id_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((review_display_artifact_id ~ '^review-display:v1:sha256:[0-9a-f]{64}$'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_media_type_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((review_display_media_type = 'application/vnd.ahe.review-package.v1+json'::text))",
	},
	{
		Name:       "canonical_source_claim_review_bindings_payload_size_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK (((octet_length(review_display_payload_utf8) > 0) AND (octet_length(review_display_payload_utf8) <= 131072)))",
	},
	{
		Name:              "canonical_source_claim_review_bindings_decision_fk",
		Table:             "canonical_source_claim_review_bindings",
		Type:              "f",
		Deferrable:        true,
		InitiallyDeferred: true,
		Definition: "FOREIGN KEY (admission_decision_id) REFERENCES admission_decisions(admission_decision_id) " +
			"DEFERRABLE INITIALLY DEFERRED",
	},
	{
		Name:              "canonical_source_claim_review_bindings_proposal_fk",
		Table:             "canonical_source_claim_review_bindings",
		Type:              "f",
		Deferrable:        true,
		InitiallyDeferred: true,
		Definition: "FOREIGN KEY (proposal_occurrence_id) REFERENCES proposal_occurrences(proposal_occurrence_id) " +
			"DEFERRABLE INITIALLY DEFERRED",
	},
	{
		Name:              "canonical_source_claim_review_bindings_attempt_fk",
		Table:             "canonical_source_claim_review_bindings",
		Type:              "f",
		Deferrable:        true,
		InitiallyDeferred: true,
		Definition: "FOREIGN KEY (extraction_attempt_id) REFERENCES extraction_attempts(extraction_attempt_id) " +
			"DEFERRABLE INITIALLY DEFERRED",
	},
	{
		Name:              "canonical_source_claim_review_bindings_ordinary_manifest_fk",
		Table:             "canonical_source_claim_review_bindings",
		Type:              "f",
		Deferrable:        true,
		InitiallyDeferred: true,
		Definition: "FOREIGN KEY (admission_decision_id) REFERENCES canonical_ordinary_admission_manifests(admission_decision_id) " +
			"DEFERRABLE INITIALLY DEFERRED",
	},
	{
		Name:       "canonical_source_claim_review_bindings_attempt_id_ck",
		Table:      "canonical_source_claim_review_bindings",
		Type:       "c",
		Definition: "CHECK ((btrim(extraction_attempt_id) <> ''::text))",
	},
}

// ErrSchemaNotCurrent indicates that the database cannot satisfy the embedded
// migration contract.
var ErrSchemaNotCurrent = errors.New("database schema is not current; run ahe-migrate")

// SchemaStatus describes a database verified against the embedded migrations.
type SchemaStatus struct {
	AppliedMigrations int
	LatestMigration   string
}

type embeddedMigration struct {
	name     string
	sql      string
	checksum string
}

// UpSQL returns all embedded up migrations in filename order.
func UpSQL() (string, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return "", err
	}
	var combined strings.Builder
	for _, migration := range migrations {
		combined.WriteString(migration.sql)
		combined.WriteString("\n")
	}
	return combined.String(), nil
}

// ApplyUp applies missing migrations and records immutable checksums.
func ApplyUp(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	return applyUp(ctx, pool, "")
}

// ApplyUpInSchema applies missing migrations only after binding the migration
// transaction to one explicit pre-existing private schema. The pool must
// already expose exactly that schema, optionally followed by pg_catalog; a
// fallback user schema is rejected before any migration write occurs.
func ApplyUpInSchema(ctx context.Context, pool *pgxpool.Pool, schema string) (bool, error) {
	if err := ValidateTargetSchema(schema); err != nil {
		return false, err
	}
	return applyUp(ctx, pool, schema)
}

func applyUp(ctx context.Context, pool *pgxpool.Pool, schema string) (bool, error) {
	if pool == nil {
		return false, errors.New("postgres pool is required")
	}
	migrations, err := loadMigrations()
	if err != nil {
		return false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning migration transaction: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback(context.Background())
	}
	if schema != "" {
		if err := bindMigrationSchema(ctx, tx, schema, true); err != nil {
			rollback()
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('ahe-wrap:evidence-migrations'))`); err != nil {
		rollback()
		return false, fmt.Errorf("locking evidence migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			migration_name TEXT PRIMARY KEY,
			migration_checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		rollback()
		return false, fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := loadAppliedMigrations(ctx, tx)
	if err != nil {
		rollback()
		return false, err
	}
	changed := false
	if len(applied) == 0 {
		bootstrapped, err := bootstrapExistingMigrations(ctx, tx, migrations)
		if err != nil {
			rollback()
			return false, err
		}
		changed = bootstrapped
		applied, err = loadAppliedMigrations(ctx, tx)
		if err != nil {
			rollback()
			return false, err
		}
	}
	if err := validateAppliedMigrations(migrations, applied); err != nil {
		rollback()
		return false, err
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.name]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, migration.sql); err != nil {
			rollback()
			return false, fmt.Errorf("applying embedded migration %s: %w", migration.name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO schema_migrations (migration_name, migration_checksum)
			VALUES ($1, $2)
		`, migration.name, migration.checksum); err != nil {
			rollback()
			return false, fmt.Errorf("recording embedded migration %s: %w", migration.name, err)
		}
		changed = true
	}

	existing, err := existingTableCount(ctx, tx, requiredTables)
	if err != nil {
		rollback()
		return false, err
	}
	if existing != len(requiredTables) {
		rollback()
		return false, fmt.Errorf("evidence ingestion schema is incomplete after migrations: %d/%d required tables exist", existing, len(requiredTables))
	}
	if err := verifySourceRunRequestIdentityIndex(ctx, tx); err != nil {
		rollback()
		return false, fmt.Errorf("evidence ingestion schema is incomplete after migrations: %w", err)
	}
	if err := verifyCanonicalSupersessionSchemaObjects(ctx, tx); err != nil {
		rollback()
		return false, fmt.Errorf("evidence ingestion schema is incomplete after migrations: %w", err)
	}
	if err := verifyAdmissionSchemaObjects(ctx, tx); err != nil {
		rollback()
		return false, fmt.Errorf("evidence ingestion schema is incomplete after migrations: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing migration transaction: %w", err)
	}
	return changed, nil
}

// VerifyCurrent checks the migration ledger and required tables without
// changing the database.
func VerifyCurrent(ctx context.Context, pool *pgxpool.Pool) (SchemaStatus, error) {
	return verifyCurrent(ctx, pool, "")
}

// VerifyCurrentInSchema verifies only one explicit pre-existing private
// schema. It rejects fallback search paths before reading the migration ledger
// or required relations.
func VerifyCurrentInSchema(ctx context.Context, pool *pgxpool.Pool, schema string) (SchemaStatus, error) {
	if err := ValidateTargetSchema(schema); err != nil {
		return SchemaStatus{}, err
	}
	return verifyCurrent(ctx, pool, schema)
}

func verifyCurrent(ctx context.Context, pool *pgxpool.Pool, schema string) (SchemaStatus, error) {
	if pool == nil {
		return SchemaStatus{}, errors.New("postgres pool is required")
	}
	migrationSet, err := loadMigrations()
	if err != nil {
		return SchemaStatus{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return SchemaStatus{}, fmt.Errorf("beginning schema verification transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if schema != "" {
		if err := bindMigrationSchema(ctx, tx, schema, false); err != nil {
			return SchemaStatus{}, err
		}
	}

	migrationTableCount, err := existingTableCount(ctx, tx, []string{"schema_migrations"})
	if err != nil {
		return SchemaStatus{}, err
	}
	if migrationTableCount != 1 {
		return SchemaStatus{}, fmt.Errorf("%w: schema_migrations table is missing", ErrSchemaNotCurrent)
	}

	applied, err := loadAppliedMigrations(ctx, tx)
	if err != nil {
		return SchemaStatus{}, err
	}
	if err := validateAppliedMigrations(migrationSet, applied); err != nil {
		return SchemaStatus{}, fmt.Errorf("%w: %v", ErrSchemaNotCurrent, err)
	}
	if len(applied) != len(migrationSet) {
		return SchemaStatus{}, fmt.Errorf(
			"%w: %d/%d embedded migrations are applied",
			ErrSchemaNotCurrent,
			len(applied),
			len(migrationSet),
		)
	}

	existing, err := existingTableCount(ctx, tx, requiredTables)
	if err != nil {
		return SchemaStatus{}, err
	}
	if existing != len(requiredTables) {
		return SchemaStatus{}, fmt.Errorf(
			"%w: %d/%d required tables exist",
			ErrSchemaNotCurrent,
			existing,
			len(requiredTables),
		)
	}
	if err := verifySourceRunRequestIdentityIndex(ctx, tx); err != nil {
		return SchemaStatus{}, fmt.Errorf("%w: %v", ErrSchemaNotCurrent, err)
	}
	if err := verifyCanonicalSupersessionSchemaObjects(ctx, tx); err != nil {
		return SchemaStatus{}, fmt.Errorf("%w: %v", ErrSchemaNotCurrent, err)
	}
	if err := verifyAdmissionSchemaObjects(ctx, tx); err != nil {
		return SchemaStatus{}, fmt.Errorf("%w: %v", ErrSchemaNotCurrent, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SchemaStatus{}, fmt.Errorf("committing schema verification transaction: %w", err)
	}
	return SchemaStatus{
		AppliedMigrations: len(applied),
		LatestMigration:   migrationSet[len(migrationSet)-1].name,
	}, nil
}

// ValidateTargetSchema accepts only exact ordinary PostgreSQL identifiers and
// excludes shared and system namespaces. Keeping this deployment coordinate
// deliberately narrow also prevents a schema value from being interpreted as
// a search_path expression.
func ValidateTargetSchema(schema string) error {
	if schema == "" {
		return errors.New("AHE_DATABASE_SCHEMA is required")
	}
	if len(schema) > 63 {
		return errors.New("AHE_DATABASE_SCHEMA must be at most 63 bytes")
	}
	for index := 0; index < len(schema); index++ {
		character := schema[index]
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		if index == 0 {
			if !letter && character != '_' {
				return errors.New("AHE_DATABASE_SCHEMA must be one exact PostgreSQL identifier")
			}
			continue
		}
		if !letter && (character < '0' || character > '9') && character != '_' {
			return errors.New("AHE_DATABASE_SCHEMA must be one exact PostgreSQL identifier")
		}
	}
	lower := strings.ToLower(schema)
	if lower == "public" || lower == "information_schema" || strings.HasPrefix(lower, "pg_") {
		return errors.New("AHE_DATABASE_SCHEMA must identify a private non-system schema")
	}
	return nil
}

func bindMigrationSchema(ctx context.Context, tx pgx.Tx, schema string, requireCreate bool) error {
	var hasTemporarySchema bool
	if err := tx.QueryRow(ctx, `
		SELECT pg_catalog.pg_my_temp_schema() <> 0
	`).Scan(&hasTemporarySchema); err != nil {
		return fmt.Errorf("checking migration temporary schema: %w", err)
	}
	if hasTemporarySchema {
		return errors.New("migration session has an active temporary schema")
	}

	var namespaceOID uint32
	if err := tx.QueryRow(ctx, `
		SELECT oid
		FROM pg_catalog.pg_namespace
		WHERE nspname = $1
	`, schema).Scan(&namespaceOID); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("migration target schema %q does not exist", schema)
	} else if err != nil {
		return fmt.Errorf("loading migration target schema: %w", err)
	}

	var hasUsage, hasCreate, hasPublicPrivilege bool
	if err := tx.QueryRow(ctx, `
		SELECT
			pg_catalog.has_schema_privilege(current_user, $1::oid, 'USAGE'),
			pg_catalog.has_schema_privilege(current_user, $1::oid, 'CREATE'),
			EXISTS (
				SELECT 1
				FROM pg_catalog.pg_namespace AS namespace
				CROSS JOIN LATERAL pg_catalog.aclexplode(
					COALESCE(
						namespace.nspacl,
						pg_catalog.acldefault('n', namespace.nspowner)
					)
				) AS acl
				WHERE namespace.oid = $1::oid
				  AND acl.grantee = 0
				  AND acl.privilege_type IN ('USAGE', 'CREATE')
			)
	`, namespaceOID).Scan(&hasUsage, &hasCreate, &hasPublicPrivilege); err != nil {
		return fmt.Errorf("checking migration target schema privileges: %w", err)
	}
	if !hasUsage {
		return fmt.Errorf("current PostgreSQL role lacks USAGE on migration target schema %q", schema)
	}
	if requireCreate && !hasCreate {
		return fmt.Errorf("current PostgreSQL role lacks CREATE on migration target schema %q", schema)
	}
	if hasPublicPrivilege {
		return fmt.Errorf("migration target schema %q grants USAGE or CREATE to PUBLIC", schema)
	}

	var currentSchema, currentSearchPath string
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(pg_catalog.current_schema(), ''),
			pg_catalog.current_setting('search_path')
	`).Scan(&currentSchema, &currentSearchPath); err != nil {
		return fmt.Errorf("loading migration search path: %w", err)
	}
	if currentSchema != schema || !isExactMigrationSearchPath(currentSearchPath, schema) {
		return fmt.Errorf(
			"migration search_path must contain only target schema %q and optional pg_catalog",
			schema,
		)
	}

	exactSearchPath := pgx.Identifier{schema}.Sanitize() + ", pg_catalog"
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('search_path', $1, true)`, exactSearchPath); err != nil {
		return fmt.Errorf("binding migration search path: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.current_schema(), '')`).Scan(&currentSchema); err != nil {
		return fmt.Errorf("verifying bound migration schema: %w", err)
	}
	if currentSchema != schema {
		return fmt.Errorf("bound migration schema = %q, want %q", currentSchema, schema)
	}
	return nil
}

func isExactMigrationSearchPath(searchPath, schema string) bool {
	parts := strings.Split(searchPath, ",")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	target := strings.TrimSpace(parts[0])
	if target != schema && target != (pgx.Identifier{schema}).Sanitize() {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	catalog := strings.TrimSpace(parts[1])
	return catalog == "pg_catalog" || catalog == (pgx.Identifier{"pg_catalog"}).Sanitize()
}

func loadMigrations() ([]embeddedMigration, error) {
	paths, err := fs.Glob(upFiles, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("listing embedded migrations: %w", err)
	}
	if len(paths) == 0 {
		return nil, errors.New("no embedded up migrations found")
	}
	sort.Strings(paths)
	migrations := make([]embeddedMigration, 0, len(paths))
	for _, path := range paths {
		data, err := upFiles.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading embedded migration %s: %w", path, err)
		}
		if _, ok := requiredTablesByMigration[path]; !ok {
			return nil, fmt.Errorf("migration %s has no required-table mapping", path)
		}
		sum := sha256.Sum256(data)
		migrations = append(migrations, embeddedMigration{
			name:     path,
			sql:      string(data),
			checksum: "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	return migrations, nil
}

func loadAppliedMigrations(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT migration_name, migration_checksum FROM schema_migrations ORDER BY migration_name`)
	if err != nil {
		return nil, fmt.Errorf("reading applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]string)
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			return nil, fmt.Errorf("scanning applied migration: %w", err)
		}
		applied[name] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating applied migrations: %w", err)
	}
	return applied, nil
}

func bootstrapExistingMigrations(ctx context.Context, tx pgx.Tx, migrations []embeddedMigration) (bool, error) {
	changed := false
	missingSeen := false
	for _, migration := range migrations {
		tables := requiredTablesByMigration[migration.name]
		existing, err := existingTableCount(ctx, tx, tables)
		if err != nil {
			return false, err
		}
		switch {
		case existing == 0:
			missingSeen = true
		case existing != len(tables):
			return false, fmt.Errorf("migration %s is partially present: %d/%d required tables exist", migration.name, existing, len(tables))
		case missingSeen:
			return false, fmt.Errorf("migration %s tables exist after an earlier migration is missing", migration.name)
		default:
			if _, err := tx.Exec(ctx, `
				INSERT INTO schema_migrations (migration_name, migration_checksum)
				VALUES ($1, $2)
			`, migration.name, migration.checksum); err != nil {
				return false, fmt.Errorf("recording existing migration %s: %w", migration.name, err)
			}
			changed = true
		}
	}
	return changed, nil
}

func validateAppliedMigrations(migrations []embeddedMigration, applied map[string]string) error {
	known := make(map[string]embeddedMigration, len(migrations))
	for _, migration := range migrations {
		known[migration.name] = migration
	}
	for name, checksum := range applied {
		migration, ok := known[name]
		if !ok {
			return fmt.Errorf("database contains unknown migration %s", name)
		}
		if checksum != migration.checksum {
			return fmt.Errorf("migration %s checksum changed: database=%s embedded=%s", name, checksum, migration.checksum)
		}
	}
	missingSeen := false
	for _, migration := range migrations {
		_, ok := applied[migration.name]
		if !ok {
			missingSeen = true
			continue
		}
		if missingSeen {
			return fmt.Errorf("migration %s is applied after an earlier migration is missing", migration.name)
		}
	}
	return nil
}

type tableQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func existingTableCount(ctx context.Context, db tableQueryer, tables []string) (int, error) {
	if len(tables) == 0 {
		return 0, nil
	}
	values := make([]string, 0, len(tables))
	for _, table := range tables {
		values = append(values, fmt.Sprintf("('%s')", table))
	}
	query := fmt.Sprintf(`
		SELECT count(*)
		FROM (VALUES %s) AS required(table_name)
		WHERE to_regclass(required.table_name) IS NOT NULL
	`, strings.Join(values, ","))
	var count int
	if err := db.QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("checking evidence ingestion schema: %w", err)
	}
	return count, nil
}

func verifySourceRunRequestIdentityIndex(ctx context.Context, db tableQueryer) error {
	const indexName = "extraction_runs_source_request_id_uq"
	var (
		unique           bool
		valid            bool
		ready            bool
		immediate        bool
		keyCount         int
		attributeCount   int
		tableName        string
		accessMethod     string
		keyColumn        string
		keyIsExpression  bool
		defaultOpclass   bool
		defaultCollation bool
		defaultOptions   bool
		predicate        string
	)
	err := db.QueryRow(ctx, `
		SELECT
			index_row.indisunique,
			index_row.indisvalid,
			index_row.indisready,
			index_row.indimmediate,
			index_row.indnkeyatts,
			index_row.indnatts,
			table_relation.relname,
			access_method.amname,
			COALESCE(key_attribute.attname, ''),
			index_row.indexprs IS NOT NULL,
			operator_class.opcdefault,
			COALESCE(index_row.indcollation[0] = key_attribute.attcollation, false),
			index_row.indoption[0] = 0,
			COALESCE(pg_catalog.pg_get_expr(index_row.indpred, index_row.indrelid), '')
		FROM pg_catalog.pg_class AS index_relation
		JOIN pg_catalog.pg_namespace AS index_namespace
			ON index_namespace.oid = index_relation.relnamespace
		JOIN pg_catalog.pg_index AS index_row
			ON index_row.indexrelid = index_relation.oid
		JOIN pg_catalog.pg_class AS table_relation
			ON table_relation.oid = index_row.indrelid
		JOIN pg_catalog.pg_namespace AS table_namespace
			ON table_namespace.oid = table_relation.relnamespace
		JOIN pg_catalog.pg_am AS access_method
			ON access_method.oid = index_relation.relam
		JOIN pg_catalog.pg_opclass AS operator_class
			ON operator_class.oid = index_row.indclass[0]
		LEFT JOIN pg_catalog.pg_attribute AS key_attribute
			ON key_attribute.attrelid = table_relation.oid
			AND key_attribute.attnum = index_row.indkey[0]
		WHERE index_namespace.nspname = pg_catalog.current_schema()
		  AND table_namespace.nspname = pg_catalog.current_schema()
		  AND index_relation.relname = $1
	`, indexName).Scan(
		&unique,
		&valid,
		&ready,
		&immediate,
		&keyCount,
		&attributeCount,
		&tableName,
		&accessMethod,
		&keyColumn,
		&keyIsExpression,
		&defaultOpclass,
		&defaultCollation,
		&defaultOptions,
		&predicate,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("required source run request identity index %s is missing", indexName)
	}
	if err != nil {
		return fmt.Errorf("checking source run request identity index %s: %w", indexName, err)
	}
	predicate = strings.TrimSpace(predicate)
	predicate = strings.TrimPrefix(predicate, "(")
	predicate = strings.TrimSuffix(predicate, ")")
	if !unique || !valid || !ready || !immediate || keyCount != 1 || attributeCount != 1 ||
		tableName != "extraction_runs" || accessMethod != "btree" || keyColumn != "request_id" ||
		keyIsExpression || !defaultOpclass || !defaultCollation || !defaultOptions ||
		normalizeSchemaDefinition(predicate) != "repository_snapshot_id is null" {
		return fmt.Errorf("required source run request identity index %s does not match its unique key and predicate contract", indexName)
	}
	return nil
}

func verifyCanonicalSupersessionSchemaObjects(ctx context.Context, db tableQueryer) error {
	for _, required := range requiredCanonicalSupersessionTriggers {
		var (
			tableName         string
			functionName      string
			triggerType       int
			enabled           string
			constraintTrigger bool
			deferrable        bool
			initiallyDeferred bool
		)
		err := db.QueryRow(ctx, `
			SELECT
				table_rel.relname,
				function_proc.proname,
				trigger_row.tgtype::INTEGER,
				trigger_row.tgenabled::TEXT,
				trigger_row.tgconstraint <> 0,
				trigger_row.tgdeferrable,
				trigger_row.tginitdeferred
			FROM pg_trigger AS trigger_row
			JOIN pg_class AS table_rel ON table_rel.oid = trigger_row.tgrelid
			JOIN pg_namespace AS table_ns ON table_ns.oid = table_rel.relnamespace
			JOIN pg_proc AS function_proc ON function_proc.oid = trigger_row.tgfoid
			JOIN pg_namespace AS function_ns ON function_ns.oid = function_proc.pronamespace
			WHERE table_ns.nspname = current_schema()
			  AND function_ns.nspname = current_schema()
			  AND trigger_row.tgname = $1
			  AND NOT trigger_row.tgisinternal
		`, required.Name).Scan(
			&tableName,
			&functionName,
			&triggerType,
			&enabled,
			&constraintTrigger,
			&deferrable,
			&initiallyDeferred,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required canonical supersession trigger %s is missing", required.Name)
		}
		if err != nil {
			return fmt.Errorf("checking canonical supersession trigger %s: %w", required.Name, err)
		}
		const afterRowInsertUpdateDelete = 1 | 4 | 8 | 16
		if tableName != required.Table || functionName != required.Function ||
			triggerType != afterRowInsertUpdateDelete || enabled != "O" ||
			!constraintTrigger || !deferrable || !initiallyDeferred {
			return fmt.Errorf("required canonical supersession trigger %s does not match its deferred authority contract", required.Name)
		}
	}

	for _, required := range requiredCanonicalSupersessionFunctions {
		var (
			language   string
			arguments  string
			result     string
			kind       string
			security   bool
			volatility string
			strict     bool
			source     string
		)
		err := db.QueryRow(ctx, `
			SELECT
				language_row.lanname,
				oidvectortypes(function_row.proargtypes),
				pg_get_function_result(function_row.oid),
				function_row.prokind::TEXT,
				function_row.prosecdef,
				function_row.provolatile::TEXT,
				function_row.proisstrict,
				function_row.prosrc
			FROM pg_proc AS function_row
			JOIN pg_namespace AS function_ns ON function_ns.oid = function_row.pronamespace
			JOIN pg_language AS language_row ON language_row.oid = function_row.prolang
			WHERE function_ns.nspname = current_schema()
			  AND function_row.proname = $1
			  AND oidvectortypes(function_row.proargtypes) = $2
		`, required.Name, required.Arguments).Scan(
			&language,
			&arguments,
			&result,
			&kind,
			&security,
			&volatility,
			&strict,
			&source,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required canonical supersession function %s(%s) is missing", required.Name, required.Arguments)
		}
		if err != nil {
			return fmt.Errorf("checking canonical supersession function %s: %w", required.Name, err)
		}
		if language != "plpgsql" || arguments != required.Arguments ||
			result != required.Result || kind != "f" || security {
			return fmt.Errorf("required canonical supersession function %s does not match its signature contract", required.Name)
		}
		if required.Volatility != "" &&
			(volatility != required.Volatility || strict != required.Strict) {
			return fmt.Errorf("required canonical supersession function %s does not match its execution contract", required.Name)
		}
		sourceSum := sha256.Sum256([]byte(source))
		if "sha256:"+hex.EncodeToString(sourceSum[:]) != required.SourceSHA256 {
			return fmt.Errorf("required canonical supersession function %s does not match its definition contract", required.Name)
		}
	}

	for _, required := range requiredCanonicalSupersessionConstraints {
		var (
			tableName         string
			constraintType    string
			deferrable        bool
			initiallyDeferred bool
			validated         bool
			definition        string
		)
		err := db.QueryRow(ctx, `
			SELECT
				table_rel.relname,
				constraint_row.contype::TEXT,
				constraint_row.condeferrable,
				constraint_row.condeferred,
				constraint_row.convalidated,
				pg_get_constraintdef(constraint_row.oid)
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS table_rel ON table_rel.oid = constraint_row.conrelid
			JOIN pg_namespace AS table_ns ON table_ns.oid = table_rel.relnamespace
			WHERE table_ns.nspname = current_schema()
			  AND constraint_row.conname = $1
		`, required.Name).Scan(
			&tableName,
			&constraintType,
			&deferrable,
			&initiallyDeferred,
			&validated,
			&definition,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required canonical supersession constraint %s is missing", required.Name)
		}
		if err != nil {
			return fmt.Errorf("checking canonical supersession constraint %s: %w", required.Name, err)
		}
		if tableName != required.Table || constraintType != required.Type ||
			deferrable != required.Deferrable || initiallyDeferred != required.InitiallyDeferred ||
			!validated || normalizeSchemaDefinition(definition) != normalizeSchemaDefinition(required.Definition) {
			return fmt.Errorf("required canonical supersession constraint %s does not match its definition contract", required.Name)
		}
	}
	return nil
}

func verifyAdmissionSchemaObjects(ctx context.Context, db tableQueryer) error {
	for _, required := range requiredAdmissionFunctions {
		var (
			language        string
			arguments       string
			result          string
			kind            string
			securityDefiner bool
			volatility      string
			strict          bool
			source          string
			searchPath      string
			ownerMatches    bool
			publicExecute   bool
		)
		err := db.QueryRow(ctx, `
			SELECT
				language_row.lanname,
				pg_catalog.oidvectortypes(function_row.proargtypes),
				pg_catalog.pg_get_function_result(function_row.oid),
				function_row.prokind::TEXT,
				function_row.prosecdef,
				function_row.provolatile::TEXT,
				function_row.proisstrict,
				function_row.prosrc,
				COALESCE(
					pg_catalog.array_to_string(function_row.proconfig, E'\n'),
					''
				),
				function_row.proowner = owner_table.relowner,
				EXISTS (
					SELECT 1
					FROM pg_catalog.aclexplode(
						COALESCE(
							function_row.proacl,
							pg_catalog.acldefault('f', function_row.proowner)
						)
					) AS acl
					WHERE acl.grantee = 0
					  AND acl.privilege_type = 'EXECUTE'
				)
			FROM pg_catalog.pg_proc AS function_row
			JOIN pg_catalog.pg_namespace AS function_ns
				ON function_ns.oid = function_row.pronamespace
			JOIN pg_catalog.pg_language AS language_row
				ON language_row.oid = function_row.prolang
			JOIN pg_catalog.pg_class AS owner_table
				ON owner_table.relname = $3
			JOIN pg_catalog.pg_namespace AS owner_ns
				ON owner_ns.oid = owner_table.relnamespace
			   AND owner_ns.oid = function_ns.oid
			WHERE function_ns.nspname = pg_catalog.current_schema()
			  AND function_row.proname = $1
			  AND pg_catalog.oidvectortypes(function_row.proargtypes) = $2
		`, required.Name, required.Arguments, required.OwnerTable).Scan(
			&language,
			&arguments,
			&result,
			&kind,
			&securityDefiner,
			&volatility,
			&strict,
			&source,
			&searchPath,
			&ownerMatches,
			&publicExecute,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf(
				"required canonical admission function %s(%s) is missing",
				required.Name,
				required.Arguments,
			)
		}
		if err != nil {
			return fmt.Errorf(
				"checking canonical admission function %s: %w",
				required.Name,
				err,
			)
		}
		if language != "plpgsql" || arguments != required.Arguments ||
			result != required.Result || kind != "f" || volatility != "v" ||
			strict || securityDefiner != required.SecurityDefiner {
			return fmt.Errorf(
				"required canonical admission function %s does not match its signature and execution contract",
				required.Name,
			)
		}
		if searchPath != required.SearchPath || !ownerMatches || publicExecute {
			return fmt.Errorf(
				"required canonical admission function %s does not match its owner, search_path, and privilege contract",
				required.Name,
			)
		}
		sourceSum := sha256.Sum256([]byte(source))
		if "sha256:"+hex.EncodeToString(sourceSum[:]) != required.SourceSHA256 {
			return fmt.Errorf(
				"required canonical admission function %s does not match its definition contract",
				required.Name,
			)
		}
	}

	for _, required := range requiredAdmissionTriggers {
		var (
			tableName         string
			functionName      string
			triggerType       int
			enabled           string
			constraintTrigger bool
			deferrable        bool
			initiallyDeferred bool
			argumentBytes     int
			hasWhen           bool
		)
		err := db.QueryRow(ctx, `
			SELECT
				table_relation.relname,
				function_row.proname,
				trigger_row.tgtype::INTEGER,
				trigger_row.tgenabled::TEXT,
				trigger_row.tgconstraint <> 0,
				trigger_row.tgdeferrable,
				trigger_row.tginitdeferred,
				pg_catalog.octet_length(trigger_row.tgargs),
				trigger_row.tgqual IS NOT NULL
			FROM pg_catalog.pg_trigger AS trigger_row
			JOIN pg_catalog.pg_class AS table_relation
				ON table_relation.oid = trigger_row.tgrelid
			JOIN pg_catalog.pg_namespace AS table_ns
				ON table_ns.oid = table_relation.relnamespace
			JOIN pg_catalog.pg_proc AS function_row
				ON function_row.oid = trigger_row.tgfoid
			JOIN pg_catalog.pg_namespace AS function_ns
				ON function_ns.oid = function_row.pronamespace
			WHERE table_ns.nspname = pg_catalog.current_schema()
			  AND function_ns.oid = table_ns.oid
			  AND trigger_row.tgname = $1
			  AND NOT trigger_row.tgisinternal
		`, required.Name).Scan(
			&tableName,
			&functionName,
			&triggerType,
			&enabled,
			&constraintTrigger,
			&deferrable,
			&initiallyDeferred,
			&argumentBytes,
			&hasWhen,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf(
				"required canonical admission trigger %s is missing",
				required.Name,
			)
		}
		if err != nil {
			return fmt.Errorf(
				"checking canonical admission trigger %s: %w",
				required.Name,
				err,
			)
		}
		if tableName != required.Table || functionName != required.Function ||
			triggerType != required.TriggerType || enabled != "O" ||
			constraintTrigger != required.Constraint ||
			deferrable != required.Deferrable ||
			initiallyDeferred != required.InitiallyDeferred ||
			argumentBytes != 0 || hasWhen {
			return fmt.Errorf(
				"required canonical admission trigger %s does not match its execution contract",
				required.Name,
			)
		}
	}

	for _, required := range requiredAdmissionIndexes {
		var (
			unique           bool
			valid            bool
			ready            bool
			immediate        bool
			keyCount         int
			attributeCount   int
			tableName        string
			accessMethod     string
			keyColumn        string
			keyIsExpression  bool
			defaultOpclass   bool
			defaultCollation bool
			defaultOptions   bool
			predicate        string
		)
		err := db.QueryRow(ctx, `
			SELECT
				index_row.indisunique,
				index_row.indisvalid,
				index_row.indisready,
				index_row.indimmediate,
				index_row.indnkeyatts,
				index_row.indnatts,
				table_relation.relname,
				access_method.amname,
				COALESCE(key_attribute.attname, ''),
				index_row.indexprs IS NOT NULL,
				operator_class.opcdefault,
				COALESCE(
					index_row.indcollation[0] = key_attribute.attcollation,
					false
				),
				index_row.indoption[0] = 0,
				COALESCE(
					pg_catalog.pg_get_expr(
						index_row.indpred,
						index_row.indrelid
					),
					''
				)
			FROM pg_catalog.pg_class AS index_relation
			JOIN pg_catalog.pg_namespace AS index_ns
				ON index_ns.oid = index_relation.relnamespace
			JOIN pg_catalog.pg_index AS index_row
				ON index_row.indexrelid = index_relation.oid
			JOIN pg_catalog.pg_class AS table_relation
				ON table_relation.oid = index_row.indrelid
			JOIN pg_catalog.pg_namespace AS table_ns
				ON table_ns.oid = table_relation.relnamespace
			JOIN pg_catalog.pg_am AS access_method
				ON access_method.oid = index_relation.relam
			JOIN pg_catalog.pg_opclass AS operator_class
				ON operator_class.oid = index_row.indclass[0]
			LEFT JOIN pg_catalog.pg_attribute AS key_attribute
				ON key_attribute.attrelid = table_relation.oid
			   AND key_attribute.attnum = index_row.indkey[0]
			WHERE index_ns.nspname = pg_catalog.current_schema()
			  AND table_ns.oid = index_ns.oid
			  AND index_relation.relname = $1
		`, required.Name).Scan(
			&unique,
			&valid,
			&ready,
			&immediate,
			&keyCount,
			&attributeCount,
			&tableName,
			&accessMethod,
			&keyColumn,
			&keyIsExpression,
			&defaultOpclass,
			&defaultCollation,
			&defaultOptions,
			&predicate,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf(
				"required canonical admission index %s is missing",
				required.Name,
			)
		}
		if err != nil {
			return fmt.Errorf(
				"checking canonical admission index %s: %w",
				required.Name,
				err,
			)
		}
		predicate = strings.TrimSpace(predicate)
		predicate = strings.TrimPrefix(predicate, "(")
		predicate = strings.TrimSuffix(predicate, ")")
		if !unique || !valid || !ready || !immediate ||
			keyCount != 1 || attributeCount != 1 ||
			tableName != required.Table || accessMethod != "btree" ||
			keyColumn != required.KeyColumn || keyIsExpression ||
			!defaultOpclass || !defaultCollation || !defaultOptions ||
			normalizeSchemaDefinition(predicate) !=
				normalizeSchemaDefinition(required.Predicate) {
			return fmt.Errorf(
				"required canonical admission index %s does not match its unique materializer contract",
				required.Name,
			)
		}
	}

	for _, required := range requiredAdmissionConstraints {
		var (
			tableName         string
			constraintType    string
			deferrable        bool
			initiallyDeferred bool
			validated         bool
			definition        string
		)
		err := db.QueryRow(ctx, `
			SELECT
				table_relation.relname,
				constraint_row.contype::TEXT,
				constraint_row.condeferrable,
				constraint_row.condeferred,
				constraint_row.convalidated,
				pg_catalog.pg_get_constraintdef(constraint_row.oid)
			FROM pg_catalog.pg_constraint AS constraint_row
			JOIN pg_catalog.pg_class AS table_relation
				ON table_relation.oid = constraint_row.conrelid
			JOIN pg_catalog.pg_namespace AS table_ns
				ON table_ns.oid = table_relation.relnamespace
			WHERE table_ns.nspname = pg_catalog.current_schema()
			  AND constraint_row.conname = $1
		`, required.Name).Scan(
			&tableName,
			&constraintType,
			&deferrable,
			&initiallyDeferred,
			&validated,
			&definition,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf(
				"required canonical admission constraint %s is missing",
				required.Name,
			)
		}
		if err != nil {
			return fmt.Errorf(
				"checking canonical admission constraint %s: %w",
				required.Name,
				err,
			)
		}
		if tableName != required.Table ||
			constraintType != required.Type ||
			deferrable != required.Deferrable ||
			initiallyDeferred != required.InitiallyDeferred ||
			!validated ||
			normalizeSchemaDefinition(definition) !=
				normalizeSchemaDefinition(required.Definition) {
			return fmt.Errorf(
				"required canonical admission constraint %s does not match its definition contract",
				required.Name,
			)
		}
	}

	var (
		columnType string
		notNull    bool
		hasDefault bool
	)
	err := db.QueryRow(ctx, `
		SELECT
			pg_catalog.format_type(attribute.atttypid, attribute.atttypmod),
			attribute.attnotnull,
			attribute.atthasdef
		FROM pg_catalog.pg_attribute AS attribute
		JOIN pg_catalog.pg_class AS relation
			ON relation.oid = attribute.attrelid
		JOIN pg_catalog.pg_namespace AS namespace
			ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = pg_catalog.current_schema()
		  AND relation.relname = 'admission_decisions'
		  AND attribute.attname = 'review_binding_contract_version'
		  AND attribute.attnum > 0
		  AND NOT attribute.attisdropped
	`).Scan(&columnType, &notNull, &hasDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New(
			"required admission_decisions.review_binding_contract_version column is missing",
		)
	}
	if err != nil {
		return fmt.Errorf(
			"checking admission_decisions.review_binding_contract_version: %w",
			err,
		)
	}
	if columnType != "text" || notNull || hasDefault {
		return errors.New(
			"required admission_decisions.review_binding_contract_version column does not match its nullable text contract",
		)
	}
	return nil
}

func normalizeSchemaDefinition(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
