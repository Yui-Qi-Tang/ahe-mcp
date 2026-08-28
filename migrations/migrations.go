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
		Function: "canonical_supersession_edge_authority_trigger",
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
	if err := verifyCanonicalSupersessionSchemaObjects(ctx, tx); err != nil {
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
	if err := verifyCanonicalSupersessionSchemaObjects(ctx, tx); err != nil {
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

func normalizeSchemaDefinition(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
