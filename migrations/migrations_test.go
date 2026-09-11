package migrations

import (
	"strings"
	"testing"
)

func TestUpSQLOrdersEmbeddedMigrations(t *testing.T) {
	sql, err := UpSQL()
	if err != nil {
		t.Fatalf("UpSQL() error = %v", err)
	}
	first := strings.Index(sql, "CREATE TABLE source_blobs")
	second := strings.Index(sql, "CREATE TABLE canonical_graph_nodes")
	third := strings.Index(sql, "CREATE TABLE repository_snapshots")
	fourth := strings.Index(sql, "CREATE TABLE repository_source_generations")
	fifth := strings.Index(sql, "CREATE TABLE repository_generation_reconciliations")
	sixth := strings.Index(sql, "CREATE TABLE repository_change_observations")
	seventh := strings.Index(sql, "CREATE TABLE repository_extraction_work_items")
	eighth := strings.Index(sql, "CREATE TABLE repository_extraction_work_finish_requests")
	ninth := strings.Index(sql, "CREATE TABLE repository_extraction_work_claim_attempts")
	tenth := strings.Index(sql, "CREATE TABLE repository_extraction_work_recovery_requests")
	eleventh := strings.Index(sql, "CREATE TABLE repository_extraction_work_retry_requests")
	twelfth := strings.Index(sql, "CREATE TABLE repository_extraction_work_lease_renewal_requests")
	thirteenth := strings.Index(sql, "CREATE TABLE repository_extraction_work_execution_requests")
	fourteenth := strings.Index(sql, "ADD COLUMN lease_duration_milliseconds BIGINT")
	fifteenth := strings.Index(sql, "CREATE TABLE repository_extraction_worker_tick_requests")
	sixteenth := strings.Index(sql, "CREATE TABLE repository_extraction_work_failure_policy_decisions")
	seventeenth := strings.Index(sql, "CREATE TABLE repository_extraction_work_retry_decision_consumptions")
	eighteenth := strings.Index(sql, "CREATE TABLE repository_extraction_work_execution_repairs")
	nineteenth := strings.Index(sql, "CREATE INDEX repository_extraction_work_expired_execution_scan_idx")
	twentieth := strings.Index(sql, "CREATE TABLE repository_extraction_work_retry_controller_tick_requests")
	twentyFirst := strings.Index(sql, "CREATE TABLE repository_extraction_work_expired_maintenance_tick_requests")
	twentySecond := strings.Index(sql, "CREATE TABLE evidence_ingestion_request_serializations")
	twentyThird := strings.Index(sql, "CREATE TABLE detective_workspaces")
	twentyFourth := strings.Index(sql, "CREATE TABLE detective_orchestration_runs")
	twentyFifth := strings.Index(sql, "CREATE TABLE detective_planner_recommendation_consumptions")
	twentySixth := strings.Index(sql, "CREATE TABLE detective_periodic_source_cursors")
	twentySeventh := strings.Index(sql, "CREATE TABLE detective_connector_inbox_deliveries")
	twentyEighth := strings.Index(sql, "CREATE TABLE detective_connector_inbox_processing_work")
	twentyNinth := strings.Index(sql, "CREATE TABLE repository_delta_extractions")
	thirtieth := strings.Index(sql, "CREATE INDEX proposal_occurrences_statement_search_idx")
	thirtyFirst := strings.Index(sql, "CREATE INDEX proposal_occurrences_statement_english_search_idx")
	thirtySecond := strings.Index(sql, "CREATE TABLE canonical_derivations")
	thirtyThird := strings.Index(sql, "CREATE TABLE canonical_contradiction_proposals")
	thirtyFourth := strings.Index(sql, "CREATE TABLE canonical_supersession_proposals")
	thirtyFifth := strings.Index(sql, "CREATE TABLE canonical_supersession_lineages")
	if first < 0 || second < 0 || third < 0 || fourth < 0 || fifth < 0 || sixth < 0 || seventh < 0 || eighth < 0 || ninth < 0 || tenth < 0 || eleventh < 0 || twelfth < 0 || thirteenth < 0 || fourteenth < 0 || fifteenth < 0 || sixteenth < 0 || seventeenth < 0 || eighteenth < 0 || nineteenth < 0 || twentieth < 0 || twentyFirst < 0 || twentySecond < 0 || twentyThird < 0 || twentyFourth < 0 || twentyFifth < 0 || twentySixth < 0 || twentySeventh < 0 || twentyEighth < 0 || twentyNinth < 0 || thirtieth < 0 || thirtyFirst < 0 || thirtySecond < 0 || thirtyThird < 0 || thirtyFourth < 0 || thirtyFifth < 0 {
		t.Fatalf("migration SQL missing expected tables")
	}
	if first > second || second > third || third > fourth || fourth > fifth || fifth > sixth || sixth > seventh || seventh > eighth || eighth > ninth || ninth > tenth || tenth > eleventh || eleventh > twelfth || twelfth > thirteenth || thirteenth > fourteenth || fourteenth > fifteenth || fifteenth > sixteenth || sixteenth > seventeenth || seventeenth > eighteenth || eighteenth > nineteenth || nineteenth > twentieth || twentieth > twentyFirst || twentyFirst > twentySecond || twentySecond > twentyThird || twentyThird > twentyFourth || twentyFourth > twentyFifth || twentyFifth > twentySixth || twentySixth > twentySeventh || twentySeventh > twentyEighth || twentyEighth > twentyNinth || twentyNinth > thirtieth || thirtieth > thirtyFirst || thirtyFirst > thirtySecond || thirtySecond > thirtyThird || thirtyThird > thirtyFourth || thirtyFourth > thirtyFifth {
		t.Fatalf("migrations not ordered: source_blobs=%d canonical_graph_nodes=%d repository_snapshots=%d repository_source_generations=%d repository_generation_reconciliations=%d repository_change_observations=%d repository_extraction_work_items=%d repository_extraction_work_finish_requests=%d repository_extraction_work_claim_attempts=%d repository_extraction_work_recovery_requests=%d repository_extraction_work_retry_requests=%d repository_extraction_work_lease_renewal_requests=%d repository_extraction_work_execution_requests=%d repository_execution_heartbeat=%d repository_extraction_worker_tick_requests=%d repository_extraction_work_failure_policy_decisions=%d repository_extraction_work_retry_decision_consumptions=%d repository_extraction_work_execution_repairs=%d repository_extraction_work_expired_execution_scan_idx=%d repository_extraction_work_retry_controller_tick_requests=%d repository_extraction_work_expired_maintenance_tick_requests=%d evidence_ingestion_request_serializations=%d detective_workspaces=%d detective_orchestration_runs=%d detective_planner_recommendation_consumptions=%d detective_periodic_source_cursors=%d detective_connector_inbox_deliveries=%d detective_connector_inbox_processing_work=%d repository_delta_extractions=%d evidence_query_indexes=%d evidence_query_recovery_index=%d canonical_derivations=%d canonical_contradictions=%d canonical_supersession_v1=%d canonical_supersession_v2=%d", first, second, third, fourth, fifth, sixth, seventh, eighth, ninth, tenth, eleventh, twelfth, thirteenth, fourteenth, fifteenth, sixteenth, seventeenth, eighteenth, nineteenth, twentieth, twentyFirst, twentySecond, twentyThird, twentyFourth, twentyFifth, twentySixth, twentySeventh, twentyEighth, twentyNinth, thirtieth, thirtyFirst, thirtySecond, thirtyThird, thirtyFourth, thirtyFifth)
	}
	sourceRunRequestIdentity := strings.Index(sql, "CREATE UNIQUE INDEX extraction_runs_source_request_id_uq")
	if sourceRunRequestIdentity < thirtyFifth {
		t.Fatalf("source run request identity migration position = %d, want after canonical supersession v2 at %d", sourceRunRequestIdentity, thirtyFifth)
	}
	ordinaryAdmission := strings.Index(sql, "CREATE TABLE canonical_ordinary_admission_manifests")
	reviewBinding := strings.Index(sql, "CREATE TABLE canonical_source_claim_review_bindings")
	if ordinaryAdmission < sourceRunRequestIdentity {
		t.Fatalf("ordinary admission migration position = %d, want after source request identity at %d", ordinaryAdmission, sourceRunRequestIdentity)
	}
	if reviewBinding < ordinaryAdmission {
		t.Fatalf("review binding migration position = %d, want after ordinary admission at %d", reviewBinding, ordinaryAdmission)
	}
}

func TestSourceRunRequestIdentityMigrationHasPartialUniqueContract(t *testing.T) {
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var sql string
	for _, migration := range migrationSet {
		if migration.name == "000043_evidence_ingestion_source_run_request_identity.up.sql" {
			sql = migration.sql
			break
		}
	}
	if sql == "" {
		t.Fatal("source run request identity migration is missing")
	}
	for _, fragment := range []string{
		"CREATE UNIQUE INDEX extraction_runs_source_request_id_uq",
		"ON extraction_runs (request_id)",
		"WHERE repository_snapshot_id IS NULL",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 43 is missing %q", fragment)
		}
	}
}

func TestOrdinaryAdmissionIntegrityMigrationHasNativeAuthorityContract(t *testing.T) {
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var sql string
	for _, migration := range migrationSet {
		if migration.name == "000044_evidence_ingestion_ordinary_admission_integrity.up.sql" {
			sql = migration.sql
			break
		}
	}
	if sql == "" {
		t.Fatal("ordinary admission integrity migration is missing")
	}
	for _, fragment := range []string{
		"migration 000044 requires authority-fresh native state",
		"CREATE TABLE canonical_ordinary_admission_manifests",
		"CREATE TABLE canonical_ordinary_admission_node_bindings",
		"CREATE TABLE canonical_ordinary_admission_edge_bindings",
		"CREATE UNIQUE INDEX canonical_ordinary_admission_node_materializer_uq",
		"CREATE UNIQUE INDEX canonical_ordinary_admission_edge_materializer_uq",
		"canonical_contradiction_admission_decisions AS decision",
		"canonical_supersession_admission_events AS event",
		"CREATE FUNCTION canonical_supersession_edge_authority_dispatch_v1()",
		"DROP TRIGGER canonical_supersession_edges_authority_trigger",
		"EXECUTE FUNCTION canonical_supersession_edge_authority_dispatch_v1()",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"REVOKE EXECUTE ON FUNCTION canonical_admission_guard_proposal_v1() FROM PUBLIC",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 44 is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"canonical_edge_ref",
		"canonical_contradiction_admission_manifests",
		"canonical_implements_admissions",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration 44 contains Core-only surface %q", forbidden)
		}
	}
}

func TestSourceClaimReviewBindingMigrationHasNativeExactReviewContract(t *testing.T) {
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var sql string
	for _, migration := range migrationSet {
		if migration.name == "000045_evidence_ingestion_source_claim_review_binding.up.sql" {
			sql = migration.sql
			break
		}
	}
	if sql == "" {
		t.Fatal("source claim review binding migration is missing")
	}
	for _, fragment := range []string{
		"migration 000045 requires no intermediate ordinary admission history",
		"ADD COLUMN review_binding_contract_version TEXT",
		"CREATE TABLE canonical_source_claim_review_bindings",
		"reviewed-source-claim-admission/v1",
		"application/vnd.ahe.review-package.v1+json",
		"attempt.status = 'succeeded'",
		"attempt.fixture_output IS NOT NULL",
		"CREATE CONSTRAINT TRIGGER canonical_source_claim_review_bindings_authority",
		"CREATE CONSTRAINT TRIGGER admission_decisions_source_claim_review_authority",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"REVOKE EXECUTE ON FUNCTION canonical_source_claim_review_binding_assert_v1(TEXT) FROM PUBLIC",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 45 is missing %q", fragment)
		}
	}
	if strings.Contains(sql, "canonical_edge_ref") {
		t.Error("migration 45 contains Core-only canonical_edge_ref")
	}
}

func TestCanonicalSupersessionV2MigrationRetiresPairV1AndInstallsAuthority(t *testing.T) {
	migrationSet, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	var sql string
	for _, migration := range migrationSet {
		if migration.name == "000042_evidence_ingestion_canonical_supersession_v2.up.sql" {
			sql = migration.sql
			break
		}
	}
	if sql == "" {
		t.Fatal("canonical supersession v2 migration is missing")
	}
	for _, fragment := range []string{
		"canonical_graph_edges,\n    canonical_supersession_proposals,\n    canonical_supersession_admission_decisions\nIN SHARE ROW EXCLUSIVE MODE",
		"legacy_edge_count",
		"legacy_proposal_count",
		"legacy_decision_count",
		"DROP COLUMN origin_canonical_supersession_proposal_id CASCADE",
		"DROP TABLE canonical_supersession_admission_decisions",
		"DROP TABLE canonical_supersession_proposals",
		"CREATE TABLE canonical_supersession_lineages",
		"CREATE TABLE canonical_supersession_admission_events",
		"CREATE TABLE canonical_supersession_admission_head",
		"CREATE TABLE canonical_supersession_members",
		"CREATE TABLE canonical_supersession_replacement_targets",
		"CREATE FUNCTION canonical_supersession_stable_id(",
		"CREATE FUNCTION canonical_supersession_assert_edge(checked_edge_id TEXT)",
		"CREATE FUNCTION canonical_supersession_assert_target(",
		"CREATE FUNCTION canonical_supersession_assert_event(checked_event_id TEXT)",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_lineages_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_events_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_members_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_head_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_nodes_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_decisions_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_proposals_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_edges_authority_trigger",
		"CREATE CONSTRAINT TRIGGER canonical_supersession_targets_authority_trigger",
		"event_record.event_payload\n                - 'target_node_ids'",
		"canonical supersession head must advance exactly one event",
		"canonical supersession member node is immutable",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration 42 is missing %q", fragment)
		}
	}
}

func TestValidateAppliedMigrationsRejectsChecksumDrift(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	err = validateAppliedMigrations(migrations, map[string]string{
		migrations[0].name: "sha256:changed",
	})
	if err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("validateAppliedMigrations() error = %v, want checksum changed", err)
	}
}

func TestApplyUpRequiresPool(t *testing.T) {
	_, err := ApplyUp(t.Context(), nil)
	if err == nil {
		t.Fatal("ApplyUp(nil) error = nil, want error")
	}
}

func TestVerifyCurrentRequiresPool(t *testing.T) {
	_, err := VerifyCurrent(t.Context(), nil)
	if err == nil {
		t.Fatal("VerifyCurrent(nil) error = nil, want error")
	}
}

func TestValidateTargetSchema(t *testing.T) {
	for _, schema := range []string{"ahe_mcp_v1", "_ahe_v1", "AHE_MCP_V1"} {
		if err := ValidateTargetSchema(schema); err != nil {
			t.Errorf("ValidateTargetSchema(%q) error = %v", schema, err)
		}
	}

	for _, schema := range []string{
		"",
		"public",
		"PUBLIC",
		"information_schema",
		"pg_catalog",
		"pg_temp_7",
		"ahe, public",
		"ahe;DROP_SCHEMA",
		"quoted\"schema",
		"-ahe",
		strings.Repeat("a", 64),
	} {
		if err := ValidateTargetSchema(schema); err == nil {
			t.Errorf("ValidateTargetSchema(%q) error = nil, want rejection", schema)
		}
	}
}

func TestExactMigrationSearchPath(t *testing.T) {
	for _, searchPath := range []string{
		"ahe_mcp_v1",
		`"ahe_mcp_v1"`,
		"ahe_mcp_v1, pg_catalog",
		`"ahe_mcp_v1", "pg_catalog"`,
	} {
		if !isExactMigrationSearchPath(searchPath, "ahe_mcp_v1") {
			t.Errorf("isExactMigrationSearchPath(%q) = false, want true", searchPath)
		}
	}

	for _, searchPath := range []string{
		"public, ahe_mcp_v1",
		"ahe_mcp_v1, public",
		"ahe_mcp_v1, missing_schema",
		"ahe_mcp_v1, pg_catalog, public",
		"other_schema",
		"",
	} {
		if isExactMigrationSearchPath(searchPath, "ahe_mcp_v1") {
			t.Errorf("isExactMigrationSearchPath(%q) = true, want false", searchPath)
		}
	}
}

func TestSchemaBoundMigrationAPIsRequirePool(t *testing.T) {
	if _, err := ApplyUpInSchema(t.Context(), nil, "ahe_mcp_v1"); err == nil || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("ApplyUpInSchema(nil) error = %v, want pool error", err)
	}
	if _, err := VerifyCurrentInSchema(t.Context(), nil, "ahe_mcp_v1"); err == nil || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("VerifyCurrentInSchema(nil) error = %v, want pool error", err)
	}
}
