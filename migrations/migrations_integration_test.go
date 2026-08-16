//go:build integration

package migrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationVerifyCurrentRejectsMissingMigrationLedger(t *testing.T) {
	ctx, pool := migrationTestPool(t)

	_, err := VerifyCurrent(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifyCurrent() error = %v, want ErrSchemaNotCurrent", err)
	}
	if !strings.Contains(err.Error(), "schema_migrations table is missing") {
		t.Fatalf("VerifyCurrent() error = %v, want missing ledger detail", err)
	}
}

func TestIntegrationVerifyCurrentAcceptsAppliedSchema(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}

	status, err := VerifyCurrent(ctx, pool)
	if err != nil {
		t.Fatalf("VerifyCurrent() error = %v", err)
	}
	if status.AppliedMigrations != 36 {
		t.Fatalf("AppliedMigrations = %d, want 36", status.AppliedMigrations)
	}
	if status.LatestMigration != "000036_evidence_ingestion_derivations.up.sql" {
		t.Fatalf("LatestMigration = %q", status.LatestMigration)
	}
}

func TestIntegrationVerifyCurrentRejectsIncompleteMigrationLedger(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM schema_migrations
		WHERE migration_name = '000036_evidence_ingestion_derivations.up.sql'
	`); err != nil {
		t.Fatalf("delete latest migration row: %v", err)
	}

	_, err := VerifyCurrent(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifyCurrent() error = %v, want ErrSchemaNotCurrent", err)
	}
	if !strings.Contains(err.Error(), "35/36 embedded migrations are applied") {
		t.Fatalf("VerifyCurrent() error = %v, want migration count detail", err)
	}
}

func TestIntegrationVerifyCurrentRejectsChecksumDrift(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE schema_migrations
		SET migration_checksum = 'sha256:drift'
		WHERE migration_name = '000001_evidence_ingestion_slice1.up.sql'
	`); err != nil {
		t.Fatalf("change migration checksum: %v", err)
	}

	_, err := VerifyCurrent(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifyCurrent() error = %v, want ErrSchemaNotCurrent", err)
	}
	if !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("VerifyCurrent() error = %v, want checksum detail", err)
	}
}

func TestIntegrationVerifyCurrentRejectsMissingRequiredTable(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE repository_snapshots CASCADE`); err != nil {
		t.Fatalf("drop required table: %v", err)
	}

	_, err := VerifyCurrent(ctx, pool)
	if !errors.Is(err, ErrSchemaNotCurrent) {
		t.Fatalf("VerifyCurrent() error = %v, want ErrSchemaNotCurrent", err)
	}
	if !strings.Contains(err.Error(), "required tables exist") {
		t.Fatalf("VerifyCurrent() error = %v, want required-table detail", err)
	}
}

func TestIntegrationApplyUpBootstrapsLegacyBaseline(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	for _, name := range []string{
		"000001_evidence_ingestion_slice1.up.sql",
		"000002_evidence_ingestion_admission_slice.up.sql",
	} {
		data, err := upFiles.ReadFile(name)
		if err != nil {
			t.Fatalf("upFiles.ReadFile(%q) error = %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("applying legacy migration %s: %v", name, err)
		}
	}

	changed, err := ApplyUp(ctx, pool)
	if err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if !changed {
		t.Fatal("ApplyUp() changed = false, want true")
	}
	assertMigrationCount(t, ctx, pool, 36)
	assertMigrationTableExists(t, ctx, pool, "repository_snapshots")
	assertMigrationTableExists(t, ctx, pool, "source_file_snapshots")
	assertMigrationTableExists(t, ctx, pool, "repository_snapshot_intake_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_source_generations")
	assertMigrationTableExists(t, ctx, pool, "repository_source_heads")
	assertMigrationTableExists(t, ctx, pool, "repository_generation_activation_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_generation_reconciliations")
	assertMigrationTableExists(t, ctx, pool, "repository_generation_proposal_reconciliations")
	assertMigrationTableExists(t, ctx, pool, "repository_change_observations")
	assertMigrationTableExists(t, ctx, pool, "repository_change_observation_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_items")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_schedule_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_claim_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_finish_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_claim_attempts")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_recovery_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_retry_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_lease_renewal_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_execution_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_worker_tick_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_failure_policy_decisions")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_retry_decision_consumptions")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_execution_repairs")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_retry_controller_tick_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_retry_controller_tick_items")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_extraction_work_expired_maintenance_tick_items")
	assertMigrationTableExists(t, ctx, pool, "evidence_ingestion_request_serializations")
	assertMigrationTableExists(t, ctx, pool, "git_repository_source_streams")
	assertMigrationTableExists(t, ctx, pool, "repository_source_streams")
	assertMigrationTableExists(t, ctx, pool, "detective_workspaces")
	assertMigrationTableExists(t, ctx, pool, "detective_workspace_sources")
	assertMigrationTableExists(t, ctx, pool, "detective_workspace_registration_requests")
	assertMigrationTableExists(t, ctx, pool, "detective_orchestration_runs")
	assertMigrationTableExists(t, ctx, pool, "detective_orchestration_steps")
	assertMigrationTableExists(t, ctx, pool, "detective_orchestration_transition_requests")
	assertMigrationTableExists(t, ctx, pool, "detective_planner_recommendation_consumptions")
	assertMigrationTableExists(t, ctx, pool, "detective_periodic_source_cursors")
	assertMigrationTableExists(t, ctx, pool, "detective_periodic_source_cycles")
	assertMigrationTableExists(t, ctx, pool, "detective_connector_inbox_deliveries")
	assertMigrationTableExists(t, ctx, pool, "detective_connector_inbox_receive_requests")
	assertMigrationTableExists(t, ctx, pool, "detective_connector_inbox_processing_work")
	assertMigrationTableExists(t, ctx, pool, "detective_connector_inbox_processing_attempts")
	assertMigrationTableExists(t, ctx, pool, "detective_connector_inbox_processing_requests")
	assertMigrationTableExists(t, ctx, pool, "detective_connector_inbox_processing_recovery_requests")
	assertMigrationTableExists(t, ctx, pool, "repository_delta_extractions")
	assertMigrationTableExists(t, ctx, pool, "detective_mcp_read_source_bindings")
	assertMigrationTableExists(t, ctx, pool, "detective_mcp_read_collection_cycles")
	assertMigrationTableExists(t, ctx, pool, "canonical_derivations")
	assertMigrationTableExists(t, ctx, pool, "canonical_derivation_parents")
	assertMigrationIndexExists(t, ctx, pool, "repo_work_failure_policy_due_idx")
	assertMigrationIndexExists(t, ctx, pool, "repository_extraction_work_expired_execution_scan_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_planner_recommendation_consumptions_run_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_periodic_source_cycles_one_running_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_connector_inbox_deliveries_received_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_connector_inbox_receive_requests_delivery_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_connector_inbox_processing_work_pending_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_connector_inbox_processing_requests_delivery_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_connector_inbox_processing_recovery_delivery_idx")
	assertMigrationIndexExists(t, ctx, pool, "repository_delta_extractions_snapshot_idx")
	assertMigrationIndexExists(t, ctx, pool, "repository_delta_extractions_base_generation_idx")
	assertMigrationIndexExists(t, ctx, pool, "proposal_occurrences_statement_search_idx")
	assertMigrationIndexExists(t, ctx, pool, "detective_mcp_read_collection_cycles_one_running_idx")
	assertMigrationIndexExists(t, ctx, pool, "proposal_occurrences_statement_english_search_idx")
	assertMigrationIndexExists(t, ctx, pool, "proposal_occurrences_relation_caller_symbol_idx")
	assertMigrationIndexExists(t, ctx, pool, "proposal_occurrences_relation_target_symbol_idx")
	assertMigrationIndexExists(t, ctx, pool, "canonical_graph_edges_from_relation_idx")
	assertMigrationIndexExists(t, ctx, pool, "canonical_graph_edges_to_relation_idx")
	assertMigrationColumnExists(t, ctx, pool, "repository_extraction_work_claim_attempts", "lease_duration_milliseconds")
	assertMigrationColumnExists(t, ctx, pool, "repository_extraction_work_execution_requests", "heartbeat_lease_duration_milliseconds")
	assertMigrationColumnExists(t, ctx, pool, "repository_extraction_work_execution_requests", "heartbeat_count")
	assertMigrationColumnExists(t, ctx, pool, "repository_extraction_work_execution_requests", "failure_class")
	assertMigrationColumnExists(t, ctx, pool, "repository_extraction_work_execution_requests", "failure_message")
	assertMigrationColumnExists(t, ctx, pool, "repository_extraction_work_execution_requests", "failed_at")

	changed, err = ApplyUp(ctx, pool)
	if err != nil {
		t.Fatalf("second ApplyUp() error = %v", err)
	}
	if changed {
		t.Fatal("second ApplyUp() changed = true, want false")
	}
}

func TestIntegrationExecutionHeartbeatMigrationBackfillsInitialLeasePolicy(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var heartbeatMigration embeddedMigration
	for _, migration := range migrationSet {
		if migration.name == "000016_evidence_ingestion_repository_extraction_work_execution_heartbeat.up.sql" {
			heartbeatMigration = migration
			break
		}
		if _, err := pool.Exec(ctx, migration.sql); err != nil {
			t.Fatalf("applying predecessor migration %s: %v", migration.name, err)
		}
	}
	if heartbeatMigration.name == "" {
		t.Fatal("heartbeat migration was not loaded")
	}

	claimedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin heartbeat migration fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_change_observations (
			change_observation_id, repo_id, observation_number,
			token_contract, dirty_fingerprint_contract, head_commit_sha,
			dirty, tracked_change_count, untracked_file_count, change_token,
			first_observed_at, last_observed_at, observation_count
		)
		VALUES (
			'observation:heartbeat-migration', 'heartbeat-migration-repo', 1,
			'git-head-dirty-token-v1', 'git-status-diff-untracked-v1',
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', false, 0, 0,
			'heartbeat-migration-token', $1, $1, 2
		)
	`, claimedAt.Add(-time.Second)); err != nil {
		t.Fatalf("insert heartbeat migration observation: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_extraction_work_items (
			work_item_id, repo_id, extractor_name, change_observation_id,
			status, claim_id, claimed_by, claimed_at
		)
		VALUES (
			'repo-work:heartbeat-migration', 'heartbeat-migration-repo',
			'repository-go-parser-code-fact', 'observation:heartbeat-migration',
			'running', 'work-claim:heartbeat-migration', 'worker-heartbeat-migration', $1
		)
	`, claimedAt); err != nil {
		t.Fatalf("insert heartbeat migration work: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_extraction_work_claim_attempts (
			claim_id, work_item_id, attempt_number, worker_id, status,
			claimed_at, lease_expires_at
		)
		VALUES (
			'work-claim:heartbeat-migration', 'repo-work:heartbeat-migration', 1,
			'worker-heartbeat-migration', 'running', $1, $2
		)
	`, claimedAt, claimedAt.Add(1200*time.Millisecond)); err != nil {
		t.Fatalf("insert heartbeat migration claim attempt: %v", err)
	}
	for _, renewal := range []struct {
		requestID string
		prior     time.Time
		renewed   time.Time
		expires   time.Time
	}{
		{
			requestID: "renew-heartbeat-migration-1",
			prior:     claimedAt.Add(500 * time.Millisecond),
			renewed:   claimedAt.Add(250 * time.Millisecond),
			expires:   claimedAt.Add(850 * time.Millisecond),
		},
		{
			requestID: "renew-heartbeat-migration-2",
			prior:     claimedAt.Add(850 * time.Millisecond),
			renewed:   claimedAt.Add(600 * time.Millisecond),
			expires:   claimedAt.Add(1200 * time.Millisecond),
		},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO repository_extraction_work_lease_renewal_requests (
				request_id, work_item_id, claim_id, worker_id,
				lease_duration_milliseconds, prior_lease_expires_at,
				renewed_at, lease_expires_at, request_payload_hash
			)
			VALUES (
				$1, 'repo-work:heartbeat-migration', 'work-claim:heartbeat-migration',
				'worker-heartbeat-migration', 600, $2, $3, $4, 'sha256:heartbeat-migration'
			)
		`, renewal.requestID, renewal.prior, renewal.renewed, renewal.expires); err != nil {
			t.Fatalf("insert heartbeat migration renewal %s: %v", renewal.requestID, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_extraction_work_execution_requests (
			request_id, work_item_id, claim_id, worker_id,
			repository_snapshot_request_id, extractor_request_id,
			finish_request_id, request_payload_hash
		)
		VALUES (
			'execute-heartbeat-migration', 'repo-work:heartbeat-migration',
			'work-claim:heartbeat-migration', 'worker-heartbeat-migration',
			'snapshot-heartbeat-migration', 'extractor-heartbeat-migration',
			'finish-heartbeat-migration', 'sha256:heartbeat-migration'
		)
	`); err != nil {
		t.Fatalf("insert heartbeat migration execution: %v", err)
	}
	legacyFinishedAt := claimedAt.Add(2 * time.Hour)
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_change_observations (
			change_observation_id, repo_id, observation_number,
			token_contract, dirty_fingerprint_contract, head_commit_sha,
			dirty, tracked_change_count, untracked_file_count, change_token,
			first_observed_at, last_observed_at, observation_count
		)
		VALUES (
			'observation:heartbeat-migration-legacy', 'heartbeat-migration-legacy-repo', 1,
			'git-head-dirty-token-v1', 'git-status-diff-untracked-v1',
			'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', false, 0, 0,
			'heartbeat-migration-legacy-token', $1, $1, 2
		)
	`, claimedAt.Add(-time.Second)); err != nil {
		t.Fatalf("insert legacy heartbeat migration observation: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_extraction_work_items (
			work_item_id, repo_id, extractor_name, change_observation_id,
			status, claim_id, claimed_by, claimed_at, finished_at,
			failure_class, failure_message
		)
		VALUES (
			'repo-work:heartbeat-migration-legacy', 'heartbeat-migration-legacy-repo',
			'repository-go-parser-code-fact', 'observation:heartbeat-migration-legacy',
			'failed', 'work-claim:heartbeat-migration-legacy',
			'worker-heartbeat-migration-legacy', $1, $2,
			'legacy_fixture', 'legacy terminal attempt predates lease policy persistence'
		)
	`, claimedAt, legacyFinishedAt); err != nil {
		t.Fatalf("insert legacy heartbeat migration work: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO repository_extraction_work_claim_attempts (
			claim_id, work_item_id, attempt_number, worker_id, status,
			claimed_at, lease_expires_at, finished_at, failure_class, failure_message
		)
		VALUES (
			'work-claim:heartbeat-migration-legacy',
			'repo-work:heartbeat-migration-legacy', 1,
			'worker-heartbeat-migration-legacy', 'failed', $1, $2, $3,
			'legacy_fixture', 'legacy terminal attempt predates lease policy persistence'
		)
	`, claimedAt, legacyFinishedAt.Add(time.Microsecond), legacyFinishedAt); err != nil {
		t.Fatalf("insert legacy heartbeat migration claim attempt: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit heartbeat migration fixture: %v", err)
	}

	if _, err := pool.Exec(ctx, heartbeatMigration.sql); err != nil {
		t.Fatalf("apply heartbeat migration: %v", err)
	}
	var attemptDuration, executionDuration int64
	var heartbeatCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			attempt.lease_duration_milliseconds,
			execution.heartbeat_lease_duration_milliseconds,
			execution.heartbeat_count
		FROM repository_extraction_work_claim_attempts AS attempt
		JOIN repository_extraction_work_execution_requests AS execution
		  ON execution.claim_id = attempt.claim_id
		WHERE attempt.claim_id = 'work-claim:heartbeat-migration'
	`).Scan(&attemptDuration, &executionDuration, &heartbeatCount); err != nil {
		t.Fatalf("read heartbeat migration backfill: %v", err)
	}
	if attemptDuration != 500 || executionDuration != 500 || heartbeatCount != 0 {
		t.Fatalf("heartbeat migration backfill = %d/%d/%d, want 500/500/0", attemptDuration, executionDuration, heartbeatCount)
	}
	var legacyDuration int64
	if err := pool.QueryRow(ctx, `
		SELECT lease_duration_milliseconds
		FROM repository_extraction_work_claim_attempts
		WHERE claim_id = 'work-claim:heartbeat-migration-legacy'
	`).Scan(&legacyDuration); err != nil {
		t.Fatalf("read legacy heartbeat migration backfill: %v", err)
	}
	if legacyDuration != 3600000 {
		t.Fatalf("legacy heartbeat migration duration = %d, want bounded 3600000", legacyDuration)
	}
}

func TestIntegrationSourceStreamArbitrationMigrationBackfillsExistingStreams(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var arbitrationMigration embeddedMigration
	for _, migration := range migrationSet {
		if migration.name == "000024_evidence_ingestion_source_stream_arbitration.up.sql" {
			arbitrationMigration = migration
			break
		}
		if _, err := pool.Exec(ctx, migration.sql); err != nil {
			t.Fatalf("applying predecessor migration %s: %v", migration.name, err)
		}
	}
	if arbitrationMigration.name == "" {
		t.Fatal("source stream arbitration migration was not loaded")
	}

	observedAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO repository_change_observations (
			change_observation_id, repo_id, observation_number,
			token_contract, dirty_fingerprint_contract, head_commit_sha,
			dirty, tracked_change_count, untracked_file_count, change_token,
			first_observed_at, last_observed_at, observation_count
		)
		VALUES (
			'observation:source-stream-migration', 'source-stream-migration-repo', 1,
			'git-head-dirty-token-v1', 'git-status-diff-untracked-v1',
			'cccccccccccccccccccccccccccccccccccccccc', false, 0, 0,
			'source-stream-migration-token', $1, $1, 2
		)
	`, observedAt); err != nil {
		t.Fatalf("insert source stream migration observation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO repository_extraction_work_items (
			work_item_id, repo_id, extractor_name, change_observation_id, status
		)
		VALUES (
			'repo-work:source-stream-migration', 'source-stream-migration-repo',
			'repository-go-parser-code-fact', 'observation:source-stream-migration', 'pending'
		)
	`); err != nil {
		t.Fatalf("insert source stream migration work: %v", err)
	}

	if _, err := pool.Exec(ctx, arbitrationMigration.sql); err != nil {
		t.Fatalf("apply source stream arbitration migration: %v", err)
	}
	var gitStreams, repositoryStreams int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM git_repository_source_streams WHERE repo_id = 'source-stream-migration-repo'`).Scan(&gitStreams); err != nil {
		t.Fatalf("count Git source stream backfill: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM repository_source_streams WHERE repo_id = 'source-stream-migration-repo' AND extractor_name = 'repository-go-parser-code-fact'`).Scan(&repositoryStreams); err != nil {
		t.Fatalf("count repository source stream backfill: %v", err)
	}
	if gitStreams != 1 || repositoryStreams != 1 {
		t.Fatalf("source stream backfill counts = %d/%d, want 1/1", gitStreams, repositoryStreams)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO repository_change_observations (
			change_observation_id, repo_id, observation_number,
			token_contract, dirty_fingerprint_contract, head_commit_sha,
			dirty, tracked_change_count, untracked_file_count, change_token,
			first_observed_at, last_observed_at, observation_count
		)
		VALUES (
			'observation:missing-source-stream', 'missing-source-stream-repo', 1,
			'git-head-dirty-token-v1', 'git-status-diff-untracked-v1',
			'dddddddddddddddddddddddddddddddddddddddd', false, 0, 0,
			'missing-source-stream-token', $1, $1, 2
		)
	`, observedAt); err == nil {
		t.Fatal("observation without Git source stream unexpectedly succeeded")
	}
}

func TestIntegrationDetectiveWorkspaceRegistryMigrationConstrainsCapabilities(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var registryMigration embeddedMigration
	for _, migration := range migrationSet {
		if migration.name == "000025_detective_workspace_registry.up.sql" {
			registryMigration = migration
			break
		}
		if _, err := pool.Exec(ctx, migration.sql); err != nil {
			t.Fatalf("applying predecessor migration %s: %v", migration.name, err)
		}
	}
	if registryMigration.name == "" {
		t.Fatal("detective workspace registry migration was not loaded")
	}
	if _, err := pool.Exec(ctx, registryMigration.sql); err != nil {
		t.Fatalf("apply detective workspace registry migration: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspaces (workspace_id, canonical_root)
		VALUES ('workspace:migration', '/tmp/detective-migration')
	`); err != nil {
		t.Fatalf("insert detective migration workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_sources (
			source_binding_id, workspace_id, capability_name,
			capability_version, source_system, source_id,
			relative_path, path_kind
		)
		VALUES (
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'workspace:migration', 'jira',
			'v1', 'manual_text', 'JIRA-1', '.', 'directory'
		)
	`); err == nil {
		t.Fatal("unsupported detective capability unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_sources (
			source_binding_id, workspace_id, capability_name,
			capability_version, source_system, source_id,
			relative_path, path_kind
		)
		VALUES (
			'workspace-source:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:migration', 'local-prd-text',
			'v1', 'manual_text', 'prd', 'requirements.txt', 'file'
		)
	`); err != nil {
		t.Fatalf("insert allowlisted detective source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_registration_requests (
			request_id, workspace_id, request_payload_hash
		)
		VALUES ('register-migration', 'workspace:migration', 'not-a-hash')
	`); err == nil {
		t.Fatal("invalid detective registration hash unexpectedly succeeded")
	}
}

func TestIntegrationDetectiveOrchestrationAuditMigrationConstrainsState(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspaces (workspace_id, canonical_root)
		VALUES ('workspace:audit-migration', '/tmp/detective-audit-migration')
	`); err != nil {
		t.Fatalf("insert audit migration workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_sources (
			source_binding_id, workspace_id, capability_name,
			capability_version, source_system, source_id,
			relative_path, path_kind
		)
		VALUES (
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'workspace:audit-migration', 'local-prd-text',
			'v1', 'manual_text', 'prd', 'requirements.txt', 'file'
		)
	`); err != nil {
		t.Fatalf("insert audit migration source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_runs (
			run_id, workspace_id, status, max_steps
		)
		VALUES (
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:audit-migration', 'running', 2
		)
	`); err != nil {
		t.Fatalf("insert audit migration run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_orchestration_runs
		SET status = 'completed', stop_reason = 'source_failed'
		WHERE run_id = 'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
	`); err == nil {
		t.Fatal("invalid terminal run state unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_orchestration_runs
		SET status = 'completed', stop_reason = NULL, finished_at = now()
		WHERE run_id = 'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
	`); err == nil {
		t.Fatal("terminal run with NULL stop reason unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_steps (
			step_id, run_id, workspace_id, step_number, source_binding_id,
			capability_name, capability_version, source_system, source_id,
			reason_code, input_authority_kind, input_authority_ref, status
		)
		VALUES (
			'detective-step:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:audit-migration', 1,
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'local-prd-text', 'v1', 'manual_text', 'prd',
			'initial_source', 'none', 'model-supplied-ref', 'planned'
		)
	`); err == nil {
		t.Fatal("none authority with a reference unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_steps (
			step_id, run_id, workspace_id, step_number, source_binding_id,
			capability_name, capability_version, source_system, source_id,
			reason_code, input_authority_kind, status
		)
		VALUES (
			'detective-step:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccd',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:audit-migration', 1,
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'local-prd-text', 'v1', 'manual_text', 'prd',
			'initial_source', 'source_token', 'planned'
		)
	`); err == nil {
		t.Fatal("source-token authority with NULL reference unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_steps (
			step_id, run_id, workspace_id, step_number, source_binding_id,
			capability_name, capability_version, source_system, source_id,
			reason_code, input_authority_kind, status
		)
		VALUES (
			'detective-step:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:audit-migration', 1,
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'jira', 'v1', 'manual_text', 'prd',
			'initial_source', 'none', 'planned'
		)
	`); err == nil {
		t.Fatal("unsupported audit capability snapshot unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_steps (
			step_id, run_id, workspace_id, step_number, source_binding_id,
			capability_name, capability_version, source_system, source_id,
			reason_code, input_authority_kind, status
		)
		VALUES (
			'detective-step:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:audit-migration', 1,
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'local-prd-text', 'v1', 'manual_text', 'prd',
			'initial_source', 'none', 'planned'
		)
	`); err != nil {
		t.Fatalf("insert valid planned audit step: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_orchestration_steps
		SET status = 'completed',
			outcome = 'completed',
			stop_reason = 'coverage_satisfied',
			coverage_schema_version = NULL,
			coverage_unit_kind = 'text_files',
			attempted_unit_count = 1,
			covered_unit_count = 1,
			unsupported_unit_count = 0,
			coverage_complete = TRUE,
			finished_at = now()
		WHERE step_id = 'detective-step:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
	`); err == nil {
		t.Fatal("terminal step with NULL coverage schema unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_transition_requests (
			request_id, transition_kind, run_id, request_payload_hash
		)
		VALUES (
			'plan-without-step', 'plan_step',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'
		)
	`); err == nil {
		t.Fatal("plan transition without step identity unexpectedly succeeded")
	}
}

func TestIntegrationDetectivePlannerConsumptionMigrationRequiresExactPlanTransition(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspaces (workspace_id, canonical_root)
		VALUES ('workspace:planner-consumption-migration', '/tmp/detective-planner-consumption-migration')
	`); err != nil {
		t.Fatalf("insert planner consumption migration workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_sources (
			source_binding_id, workspace_id, capability_name,
			capability_version, source_system, source_id,
			relative_path, path_kind
		)
		VALUES (
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'workspace:planner-consumption-migration', 'local-prd-text',
			'v1', 'manual_text', 'prd', 'requirements.txt', 'file'
		)
	`); err != nil {
		t.Fatalf("insert planner consumption migration source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_runs (
			run_id, workspace_id, status, max_steps
		)
		VALUES (
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:planner-consumption-migration', 'running', 2
		)
	`); err != nil {
		t.Fatalf("insert planner consumption migration run: %v", err)
	}
	var plannedAt time.Time
	if err := pool.QueryRow(ctx, `
		INSERT INTO detective_orchestration_steps (
			step_id, run_id, workspace_id, step_number, source_binding_id,
			capability_name, capability_version, source_system, source_id,
			reason_code, input_authority_kind, input_authority_ref, status
		)
		VALUES (
			'detective-step:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:planner-consumption-migration', 1,
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'local-prd-text', 'v1', 'manual_text', 'prd',
			'initial_source', 'source_token', 'source-token:migration', 'planned'
		)
		RETURNING planned_at
	`).Scan(&plannedAt); err != nil {
		t.Fatalf("insert planner consumption migration step: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_orchestration_runs
		SET step_count = 1
		WHERE run_id = 'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
	`); err != nil {
		t.Fatalf("advance planner consumption migration run: %v", err)
	}
	for _, request := range []struct {
		id   string
		kind string
	}{
		{id: "consume-planner-migration", kind: "plan_step"},
		{id: "consume-planner-migration-abstain", kind: "plan_step"},
		{id: "consume-planner-migration-complete", kind: "complete_step"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO detective_orchestration_transition_requests (
				request_id, transition_kind, run_id, step_id, request_payload_hash
			)
			VALUES (
				$1, $2,
				'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
				'detective-step:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
				'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
			)
		`, request.id, request.kind); err != nil {
			t.Fatalf("insert planner consumption migration transition %s: %v", request.id, err)
		}
	}
	insertConsumption := func(requestID, classification string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO detective_planner_recommendation_consumptions (
				request_id, transition_kind, run_id, step_id,
				planning_context_hash, classification, request_payload_hash, consumed_at
			)
			VALUES (
				$1, 'plan_step',
				'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
				'detective-step:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
				'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
				$2,
				'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
				$3
			)
		`, requestID, classification, plannedAt)
		return err
	}
	if err := insertConsumption("consume-planner-migration-missing", "query_source"); err == nil {
		t.Fatal("planner consumption without transition unexpectedly succeeded")
	}
	if err := insertConsumption("consume-planner-migration-abstain", "abstain"); err == nil {
		t.Fatal("abstain planner consumption unexpectedly succeeded")
	}
	if err := insertConsumption("consume-planner-migration-complete", "query_source"); err == nil {
		t.Fatal("planner consumption bound to complete transition unexpectedly succeeded")
	}
	if err := insertConsumption("consume-planner-migration", "query_source"); err != nil {
		t.Fatalf("insert valid planner consumption migration row: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM detective_planner_recommendation_consumptions`).Scan(&count); err != nil {
		t.Fatalf("count planner consumption migration rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("planner consumption migration rows = %d, want 1", count)
	}
}

func TestIntegrationDetectivePeriodicRuntimeMigrationConstrainsCursorCycles(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspaces (workspace_id, canonical_root)
		VALUES ('workspace:periodic-migration', '/tmp/detective-periodic-migration')
	`); err != nil {
		t.Fatalf("insert periodic migration workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_workspace_sources (
			source_binding_id, workspace_id, capability_name,
			capability_version, source_system, source_id,
			relative_path, path_kind
		)
		VALUES (
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'workspace:periodic-migration', 'local-prd-text',
			'v1', 'manual_text', 'prd', 'requirements.txt', 'file'
		)
	`); err != nil {
		t.Fatalf("insert periodic migration source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_runs (
			run_id, workspace_id, status, max_steps
		)
		VALUES (
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'workspace:periodic-migration', 'running', 2
		)
	`); err != nil {
		t.Fatalf("insert periodic migration run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_transition_requests (
			request_id, transition_kind, run_id, request_payload_hash
		)
		VALUES (
			'periodic-migration-one-shot', 'start_run',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
		)
	`); err != nil {
		t.Fatalf("insert periodic migration start transition: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_periodic_source_cursors (
			workspace_id, source_binding_id, cycle_count
		)
		VALUES (
			'workspace:periodic-migration',
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			1
		)
	`); err != nil {
		t.Fatalf("insert periodic migration cursor: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_periodic_source_cycles (
			cycle_id, workspace_id, source_binding_id, cycle_number,
			source_token, one_shot_request_id, run_id, max_steps, status
		)
		VALUES (
			'detective-periodic-cycle:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
			'workspace:periodic-migration',
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			1, 'periodic-token', 'periodic-migration-one-shot',
			'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			2, 'running'
		)
	`); err != nil {
		t.Fatalf("insert periodic migration cycle: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_periodic_source_cursors
		SET active_cycle_id = 'detective-periodic-cycle:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
		WHERE workspace_id = 'workspace:periodic-migration'
	`); err != nil {
		t.Fatalf("activate periodic migration cycle: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_periodic_source_cursors
		SET
			last_cycle_id = active_cycle_id,
			cursor_token = 'periodic-token',
			cursor_advanced_at = now()
		WHERE workspace_id = 'workspace:periodic-migration'
	`); err == nil {
		t.Fatal("periodic cursor advanced while its cycle was active")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_runs (
			run_id, workspace_id, status, max_steps
		)
		VALUES (
			'detective-run:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
			'workspace:periodic-migration', 'running', 2
		)
	`); err != nil {
		t.Fatalf("insert second periodic migration run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_orchestration_transition_requests (
			request_id, transition_kind, run_id, request_payload_hash
		)
		VALUES (
			'periodic-migration-one-shot-second', 'start_run',
			'detective-run:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
			'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
		)
	`); err != nil {
		t.Fatalf("insert second periodic migration start transition: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_periodic_source_cycles (
			cycle_id, workspace_id, source_binding_id, cycle_number,
			source_token, one_shot_request_id, run_id, max_steps, status
		)
		VALUES (
			'detective-periodic-cycle:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
			'workspace:periodic-migration',
			'workspace-source:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			2, 'periodic-token-2', 'periodic-migration-one-shot-second',
			'detective-run:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
			2, 'running'
		)
	`); err == nil {
		t.Fatal("second running periodic cycle unexpectedly succeeded")
	}
	var finishedAt time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE detective_orchestration_runs
		SET status = 'completed', stop_reason = 'coverage_satisfied', finished_at = now()
		WHERE run_id = 'detective-run:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
		RETURNING finished_at
	`).Scan(&finishedAt); err != nil {
		t.Fatalf("complete periodic migration run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_periodic_source_cycles
		SET status = 'completed', finalized_at = $1
		WHERE cycle_id = 'detective-periodic-cycle:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
	`, finishedAt); err != nil {
		t.Fatalf("complete periodic migration cycle: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_periodic_source_cursors
		SET
			active_cycle_id = NULL,
			last_cycle_id = 'detective-periodic-cycle:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
			cursor_token = 'periodic-token',
			cursor_advanced_at = $1
		WHERE workspace_id = 'workspace:periodic-migration'
	`, finishedAt); err != nil {
		t.Fatalf("advance periodic migration cursor: %v", err)
	}
	var cycleCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM detective_periodic_source_cycles
	`).Scan(&cycleCount); err != nil {
		t.Fatalf("count periodic migration cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("periodic migration cycle count = %d, want 1", cycleCount)
	}
}

func TestIntegrationDetectiveConnectorInboxMigrationConstrainsExactReceipt(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := ApplyUp(ctx, pool); err != nil {
		t.Fatalf("ApplyUp() error = %v", err)
	}
	deliveryID := "detective-connector-delivery:" + strings.Repeat("a", 64)
	payloadHash := "sha256:" + strings.Repeat("b", 64)
	requestHash := "sha256:" + strings.Repeat("c", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_deliveries (
			connector_delivery_id, connector_id, external_delivery_id,
			content_type, payload_hash, payload_bytes, byte_length
		)
		VALUES ($1, 'connector-migration', 'delivery-migration',
			'application/json', $2, $3, 3)
	`, deliveryID, payloadHash, []byte{0x00, 0xff, 0x01}); err != nil {
		t.Fatalf("insert valid connector inbox delivery: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_receive_requests (
			request_id, connector_delivery_id, connector_id,
			external_delivery_id, delivery_created, request_payload_hash
		)
		VALUES ('receive-migration', $1, 'connector-migration',
			'delivery-migration', true, $2)
	`, deliveryID, requestHash); err != nil {
		t.Fatalf("insert valid connector inbox receive request: %v", err)
	}

	invalidLengthID := "detective-connector-delivery:" + strings.Repeat("d", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_deliveries (
			connector_delivery_id, connector_id, external_delivery_id,
			content_type, payload_hash, payload_bytes, byte_length
		)
		VALUES ($1, 'connector-migration', 'delivery-invalid-length',
			'text/plain', $2, $3, 2)
	`, invalidLengthID, payloadHash, []byte("one")); err == nil {
		t.Fatal("connector inbox delivery with mismatched byte length unexpectedly succeeded")
	}

	invalidTypeID := "detective-connector-delivery:" + strings.Repeat("e", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_deliveries (
			connector_delivery_id, connector_id, external_delivery_id,
			content_type, payload_hash, payload_bytes, byte_length
		)
		VALUES ($1, 'connector-migration', 'delivery-invalid-type',
			'application/xml', $2, $3, 3)
	`, invalidTypeID, payloadHash, []byte("xml")); err == nil {
		t.Fatal("connector inbox delivery with unsupported content type unexpectedly succeeded")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_receive_requests (
			request_id, connector_delivery_id, connector_id,
			external_delivery_id, delivery_created, request_payload_hash
		)
		VALUES ('receive-invalid-owner', $1, 'connector-other',
			'delivery-migration', false, $2)
	`, deliveryID, requestHash); err == nil {
		t.Fatal("connector inbox request with mismatched delivery owner unexpectedly succeeded")
	}

	var deliveries, requests int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM detective_connector_inbox_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count connector inbox deliveries: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM detective_connector_inbox_receive_requests`).Scan(&requests); err != nil {
		t.Fatalf("count connector inbox receive requests: %v", err)
	}
	if deliveries != 1 || requests != 1 {
		t.Fatalf("connector inbox migration row counts = %d/%d, want 1/1", deliveries, requests)
	}
}

func TestIntegrationDetectiveConnectorTextProcessingMigrationBackfillsAndFencesClaims(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var processingMigration embeddedMigration
	for _, migration := range migrationSet {
		if migration.name == "000030_detective_connector_text_processing.up.sql" {
			processingMigration = migration
			break
		}
		if _, err := pool.Exec(ctx, migration.sql); err != nil {
			t.Fatalf("applying predecessor migration %s: %v", migration.name, err)
		}
	}
	if processingMigration.name == "" {
		t.Fatal("connector text processing migration was not loaded")
	}
	payloadHash := "sha256:" + strings.Repeat("a", 64)
	textDeliveryID := "detective-connector-delivery:" + strings.Repeat("b", 64)
	jsonDeliveryID := "detective-connector-delivery:" + strings.Repeat("c", 64)
	for _, delivery := range []struct {
		id          string
		externalID  string
		contentType string
		payload     []byte
	}{
		{id: textDeliveryID, externalID: "delivery-processing-text", contentType: "text/plain", payload: []byte("text")},
		{id: jsonDeliveryID, externalID: "delivery-processing-json", contentType: "application/json", payload: []byte("json")},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO detective_connector_inbox_deliveries (
				connector_delivery_id, connector_id, external_delivery_id,
				content_type, payload_hash, payload_bytes, byte_length
			)
			VALUES ($1, 'connector-processing-migration', $2, $3, $4, $5, $6)
		`, delivery.id, delivery.externalID, delivery.contentType, payloadHash, delivery.payload, len(delivery.payload)); err != nil {
			t.Fatalf("insert connector processing predecessor delivery %s: %v", delivery.externalID, err)
		}
	}
	if _, err := pool.Exec(ctx, processingMigration.sql); err != nil {
		t.Fatalf("apply connector text processing migration: %v", err)
	}

	var workCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM detective_connector_inbox_processing_work
		WHERE connector_delivery_id = $1
		  AND connector_id = 'connector-processing-migration'
		  AND external_delivery_id = 'delivery-processing-text'
		  AND adapter_name = 'connector-text-delivery'
		  AND adapter_version = 'v1'
		  AND status = 'pending'
	`, textDeliveryID).Scan(&workCount); err != nil {
		t.Fatalf("count backfilled connector text processing work: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("backfilled connector text work = %d, want 1", workCount)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM detective_connector_inbox_processing_work
		WHERE connector_delivery_id = $1
	`, jsonDeliveryID).Scan(&workCount); err != nil {
		t.Fatalf("count JSON connector processing work: %v", err)
	}
	if workCount != 0 {
		t.Fatalf("JSON connector processing work = %d, want 0", workCount)
	}

	missingClaimID := "detective-connector-claim:" + strings.Repeat("d", 64)
	if _, err := pool.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_work
		SET status = 'running', attempt_count = 1, claim_id = $2
		WHERE connector_delivery_id = $1
	`, textDeliveryID, missingClaimID); err == nil {
		t.Fatal("connector processing work accepted a missing claim")
	}

	claimID := "detective-connector-claim:" + strings.Repeat("e", 64)
	claimedAt := time.Date(2026, 7, 22, 17, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_attempts (
			claim_id, connector_delivery_id, connector_id, external_delivery_id,
			attempt_number, worker_id, status, claimed_at,
			lease_duration_milliseconds, lease_expires_at
		)
		VALUES ($1,$2,'connector-processing-migration','delivery-processing-text',
			1,'worker-processing-migration','running',$3,60000,$4)
	`, claimID, textDeliveryID, claimedAt, claimedAt.Add(time.Minute)); err != nil {
		t.Fatalf("insert valid connector processing attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_work
		SET status = 'running', attempt_count = 1, claim_id = $2
		WHERE connector_delivery_id = $1
	`, textDeliveryID, claimID); err != nil {
		t.Fatalf("activate valid connector processing claim: %v", err)
	}
	requestHash := "sha256:" + strings.Repeat("f", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_requests (
			request_id, connector_delivery_id, connector_id, external_delivery_id,
			worker_id, lease_duration_milliseconds, claim_id, claim_created,
			status, request_payload_hash
		)
		VALUES ('process-migration',$1,'connector-processing-migration',
			'delivery-processing-text','worker-processing-migration',60000,$2,true,
			'running',$3)
	`, textDeliveryID, claimID, requestHash); err != nil {
		t.Fatalf("insert valid connector processing request: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO detective_connector_inbox_processing_requests (
			request_id, connector_delivery_id, connector_id, external_delivery_id,
			worker_id, lease_duration_milliseconds, claim_id, claim_created,
			status, request_payload_hash
		)
		VALUES ('process-migration-wrong-owner',$1,'connector-other',
			'delivery-processing-text','worker-processing-migration',60000,$2,true,
			'running',$3)
	`, textDeliveryID, claimID, requestHash); err == nil {
		t.Fatal("connector processing request accepted a mismatched delivery owner")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE detective_connector_inbox_processing_work
		SET status = 'succeeded', completed_at = now()
		WHERE connector_delivery_id = $1
	`, textDeliveryID); err == nil {
		t.Fatal("connector processing work succeeded without exact source authority")
	}
}

func TestIntegrationApplyUpRejectsPartialLegacyMigration(t *testing.T) {
	ctx, pool := migrationTestPool(t)
	if _, err := pool.Exec(ctx, `
		CREATE TABLE source_blobs (
			raw_content_hash TEXT PRIMARY KEY,
			raw_content BYTEA NOT NULL,
			byte_length INTEGER NOT NULL
		)
	`); err != nil {
		t.Fatalf("creating partial legacy table: %v", err)
	}

	_, err := ApplyUp(ctx, pool)
	if err == nil || !strings.Contains(err.Error(), "partially present") {
		t.Fatalf("ApplyUp() error = %v, want partially present", err)
	}
	var migrationTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations')::text`).Scan(&migrationTable); err != nil {
		t.Fatalf("checking rolled-back schema_migrations: %v", err)
	}
	if migrationTable != nil {
		t.Fatalf("schema_migrations survived rollback: %q", *migrationTable)
	}
}

func migrationTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_DNS")
	if databaseURL == "" {
		t.Skip("DATABASE_DNS is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Close(context.Background())
	})

	schema := "ahe_migrations_test_" + migrationRandomHex(t, 8)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create temp schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func assertMigrationCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("counting schema_migrations: %v", err)
	}
	if got != want {
		t.Fatalf("schema_migrations count = %d, want %d", got, want)
	}
}

func assertMigrationTableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) {
	t.Helper()
	var got *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&got); err != nil {
		t.Fatalf("checking table %s: %v", table, err)
	}
	if got == nil || *got == "" {
		t.Fatalf("table %s does not exist", table)
	}
}

func assertMigrationIndexExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, index string) {
	t.Helper()
	var got *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, index).Scan(&got); err != nil {
		t.Fatalf("checking migration index %s: %v", index, err)
	}
	if got == nil || *got != index {
		t.Fatalf("migration index %s = %v, want present", index, got)
	}
}

func assertMigrationColumnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1
			  AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		t.Fatalf("checking column %s.%s: %v", table, column, err)
	}
	if !exists {
		t.Fatalf("column %s.%s does not exist", table, column)
	}
}

func migrationRandomHex(t *testing.T, size int) string {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return hex.EncodeToString(data)
}
