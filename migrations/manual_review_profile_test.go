package migrations

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestManualReviewProfileMigrationHasCompleteReadiness(t *testing.T) {
	if tables := requiredTablesByMigration[manualReviewProfileMigration]; !slices.Equal(tables, []string{"source_manual_review_profiles"}) {
		t.Fatalf("manual profile migration tables = %v, total = %d", tables, len(requiredTables))
	}
	if len(manualReviewProfileFunctions) != 6 || len(manualReviewProfileTriggers) != 8 || len(manualReviewProfileColumns) != 7 || len(manualReviewProfileConstraints) != 7 {
		t.Fatal("manual profile readiness must cover every helper, trigger, column and constraint")
	}
	for _, function := range manualReviewProfileFunctions {
		if _, err := embeddedMigrationFunctionSource(manualReviewProfileMigration, function.Name); err != nil {
			t.Fatal(err)
		}
		if function.Migration != manualReviewProfileMigration || !function.RequirePublicExecuteRevoked {
			t.Fatalf("helper authority readiness is incomplete: %+v", function)
		}
		if function.Result == "trigger" && (!function.SecurityDefiner || !slices.Equal(function.Config, []string{"search_path=pg_catalog"})) {
			t.Fatalf("trigger must use scoped definer execution: %+v", function)
		}
		if function.Result != "trigger" && function.SecurityDefiner {
			t.Fatalf("validation helper must not become a public definer: %+v", function)
		}
		if !slices.ContainsFunc(requiredFixedCoreFunctions, func(got requiredFixedCoreFunction) bool {
			return got.Name == function.Name && got.Migration == manualReviewProfileMigration
		}) {
			t.Fatalf("function %s is not registered", function.Name)
		}
	}
	for _, trigger := range manualReviewProfileTriggers {
		if !slices.ContainsFunc(requiredFixedCoreTriggers, func(got requiredFixedCoreTrigger) bool { return got.Name == trigger.Name && got.Table == trigger.Table }) {
			t.Fatalf("trigger %s is not registered", trigger.Name)
		}
	}
}

func TestManualReviewProfileMigrationPreservesLegacyAndBoundsNewAuthority(t *testing.T) {
	data, err := os.ReadFile(manualReviewProfileMigration)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, fragment := range []string{
		"ADD COLUMN manual_profile_capture_xid xid8;",
		"NEW.manual_profile_capture_xid := CASE WHEN NEW.source_system = 'manual_text'",
		"THEN pg_catalog.pg_current_xact_id() ELSE NULL END",
		"NEW.manual_profile_capture_xid IS DISTINCT FROM OLD.manual_profile_capture_xid",
		"capture_xid IS DISTINCT FROM pg_catalog.pg_current_xact_id()",
		"count(*), count(DISTINCT key)",
		"FROM json_each(profile.profile_payload_utf8::json)",
		"key_count <> 5 OR distinct_key_count <> 5",
		"BETWEEN 1 AND 24576",
		"encode(sha256(convert_to(profile_payload_utf8, 'UTF8')), 'hex')",
		"jsonb_typeof(payload -> 'limitations') IS DISTINCT FROM 'array'",
		"jsonb_array_length(payload -> 'limitations') > 16",
		"payload ->> 'coverage' = 'exact_excerpt'",
		"r.source_snapshot_id = s.source_snapshot_id",
		"v.source_snapshot_id = s.source_snapshot_id",
		"encode(sha256(b.raw_content), 'hex')",
		"encode(sha256(v.rendered_content), 'hex')",
		"basis -> 'manual_review_profile' IS DISTINCT FROM expected",
		"basis -> 'source_title' IS DISTINCT FROM payload -> 'title'",
		"basis -> 'source_limitations' IS DISTINCT FROM payload -> 'limitations'",
		"NEW.review_display_payload_utf8::jsonb -> 'proposal_basis'",
		"leaf #> '{review_snapshot,review_package,proposal_basis}'",
		"count(DISTINCT item ->> 'specification_node_id')",
		"item ->> 'specification_node_id' = p.parent_node_id",
		"FOR leaf IN SELECT * FROM jsonb_array_elements(leaves) LOOP",
		"IF resolved_count <> 1 THEN",
		"manual implements source leaves differ from exact native AND parents",
		"manual implements source display differs from its exact native profile review",
		"(card #>> '{display,payload_utf8}')::jsonb = leaf #> '{review_snapshot,review_package}'",
		"BEFORE UPDATE OR DELETE ON source_manual_review_profiles",
		"BEFORE TRUNCATE ON source_manual_review_profiles",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("manual profile migration omits %q", fragment)
		}
	}
	for _, forbidden := range []string{"CREATE OR REPLACE FUNCTION", "GRANT ", "UPDATE source_snapshots", "SET manual_profile_capture_xid", "profile_payload_utf8::jsonb::text"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("manual profile migration reinterprets old authority: %q", forbidden)
		}
	}
	down, err := os.ReadFile("000062_manual_review_profile.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	guard := strings.Index(string(down), "migration 000062 down requires zero manual review profiles")
	if guard < 0 || guard > strings.Index(string(down), "DROP TRIGGER") || strings.Contains(string(down), "DELETE FROM") {
		t.Fatal("down must refuse retained profiles before dropping any authority, without deleting evidence")
	}
}
