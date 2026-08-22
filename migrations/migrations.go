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
	"000038_evidence_ingestion_producer_session_ref.up.sql": {},
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
