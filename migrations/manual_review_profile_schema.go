package migrations

const manualReviewProfileMigration = "000062_manual_review_profile.up.sql"

// PostgreSQL pg_trigger.tgtype flags used by the exact readiness contract.
const (
	postgresTriggerTypeRow      = 1
	postgresTriggerTypeBefore   = 2
	postgresTriggerTypeInsert   = 4
	postgresTriggerTypeDelete   = 8
	postgresTriggerTypeUpdate   = 16
	postgresTriggerTypeTruncate = 32
)

// Manual profiles qualify one immutable capture without reinterpreting legacy
// source or relation receipts. Readiness checks every new authority boundary.
var manualReviewProfileTriggers = []requiredFixedCoreTrigger{
	{Migration: manualReviewProfileMigration, Name: "source_snapshots_manual_profile_capture", Table: "source_snapshots", Function: "source_manual_review_profile_capture_trigger_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeBefore | postgresTriggerTypeInsert},
	{Migration: manualReviewProfileMigration, Name: "source_snapshots_manual_profile_immutable", Table: "source_snapshots", Function: "source_manual_review_profile_source_guard_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeBefore | postgresTriggerTypeUpdate},
	{Migration: manualReviewProfileMigration, Name: "source_intake_requests_manual_profile_immutable", Table: "source_intake_requests", Function: "source_manual_review_profile_source_guard_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeBefore | postgresTriggerTypeUpdate},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profiles_append_only", Table: "source_manual_review_profiles", Function: "canonical_admission_forbid_mutation_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeBefore | postgresTriggerTypeUpdate | postgresTriggerTypeDelete},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profiles_forbid_truncate", Table: "source_manual_review_profiles", Function: "canonical_admission_forbid_mutation_v1", TriggerType: postgresTriggerTypeBefore | postgresTriggerTypeTruncate},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profiles_authority", Table: "source_manual_review_profiles", Function: "source_manual_review_profile_insert_trigger_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeInsert},
	{Migration: manualReviewProfileMigration, Name: "canonical_source_claim_manual_profile_authority", Table: "canonical_source_claim_review_bindings", Function: "source_manual_review_profile_review_trigger_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeInsert, Constraint: true, Deferrable: true, InitiallyDeferred: true},
	{Migration: manualReviewProfileMigration, Name: "canonical_implements_manual_profile_authority", Table: "canonical_implements_admissions", Function: "source_manual_review_profile_review_trigger_v1", TriggerType: postgresTriggerTypeRow | postgresTriggerTypeInsert, Constraint: true, Deferrable: true, InitiallyDeferred: true},
}

var manualReviewProfileFunctions = []requiredFixedCoreFunction{
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profile_capture_trigger_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog"}, RequirePublicExecuteRevoked: true},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profile_source_guard_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog"}, RequirePublicExecuteRevoked: true},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profile_insert_trigger_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog"}, RequirePublicExecuteRevoked: true},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profile_review_trigger_v1", Result: "trigger", SecurityDefiner: true, Config: []string{"search_path=pg_catalog"}, RequirePublicExecuteRevoked: true},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profile_assert_v1", Arguments: "text", Result: "void", RequirePublicExecuteRevoked: true},
	{Migration: manualReviewProfileMigration, Name: "source_manual_review_profile_assert_basis_v1", Arguments: "text, jsonb", Result: "void", RequirePublicExecuteRevoked: true},
}

var manualReviewProfileColumns = []requiredFixedCoreColumn{
	{Migration: manualReviewProfileMigration, Table: "source_snapshots", Name: "manual_profile_capture_xid", DataType: "xid8"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_snapshot_id", DataType: "text", NotNull: true},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "intake_request_id", DataType: "text", NotNull: true},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "contract_version", DataType: "text", NotNull: true},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "profile_payload_utf8", DataType: "text", NotNull: true},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "profile_hash", DataType: "text", NotNull: true},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "created_at", DataType: "timestamp with time zone", NotNull: true, Default: "now()"},
}

var manualReviewProfileConstraints = []requiredFixedCoreConstraint{
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_pkey", Type: "p", Definition: "PRIMARY KEY (source_snapshot_id)"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_source_fk", Type: "f", Definition: "FOREIGN KEY (source_snapshot_id) REFERENCES source_snapshots(source_snapshot_id)"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_request_uq", Type: "u", Definition: "UNIQUE (intake_request_id)"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_request_fk", Type: "f", Definition: "FOREIGN KEY (intake_request_id) REFERENCES source_intake_requests(request_id)"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_contract_ck", Type: "c", Definition: "CHECK ((contract_version = 'manual-source-review-profile/v1'::text))"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_size_ck", Type: "c", Definition: "CHECK (((octet_length(profile_payload_utf8) >= 1) AND (octet_length(profile_payload_utf8) <= 24576)))"},
	{Migration: manualReviewProfileMigration, Table: "source_manual_review_profiles", Name: "source_manual_review_profiles_hash_ck", Type: "c", Definition: "CHECK ((profile_hash = ('sha256:'::text || encode(sha256(convert_to(profile_payload_utf8, 'UTF8'::name)), 'hex'::text))))"},
}

func init() {
	requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, manualReviewProfileTriggers...)
	requiredFixedCoreTriggers = append(requiredFixedCoreTriggers, requiredFixedCoreTrigger{Migration: manualReviewProfileMigration, Name: "evidence_governance_touch", Table: "source_manual_review_profiles", Function: "evidence_governance_touch_v1", TriggerType: 6})
	requiredFixedCoreFunctions = append(requiredFixedCoreFunctions, manualReviewProfileFunctions...)
	requiredFixedCoreColumns = append(requiredFixedCoreColumns, manualReviewProfileColumns...)
	requiredFixedCoreConstraints = append(requiredFixedCoreConstraints, manualReviewProfileConstraints...)
}
