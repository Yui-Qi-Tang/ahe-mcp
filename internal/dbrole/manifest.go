// Package dbrole enforces the closed PostgreSQL capabilities of AHE-MCP runtimes.
package dbrole

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// PolicyVersion identifies the native ordinary/review authority policy, not Core's policy.
const PolicyVersion = "ahe-mcp-database-role-policy/v5"

// Profile identifies an implemented database capability set.
type Profile string

// ProfileQuery grants read access to the shipping schema and no writer authority.
const ProfileQuery Profile = "query"

// ProfileIntake permits the five source/extractor intake tools to persist source
// authority and pending proposals. It cannot update proposal disposition, write
// canonical evidence, activate a generation, or invoke Supersession helpers.
const ProfileIntake Profile = "intake"

// ProfileSourceClaimReviewer permits the exact-reviewed source-claim runtime.
// It cannot collect sources, derive claims, write independent relations or
// activate repositories. The Go entry point enforces the review-only routing;
// table privileges alone are not an authenticated-human or row-level policy.
const ProfileSourceClaimReviewer Profile = "source-claim-reviewer"

// ProfileRelationReviewer only appends independently reviewed relation receipts and edges.
const ProfileRelationReviewer Profile = "relation-reviewer"
const ProfileEndpointReviewer Profile = "endpoint-reviewer"
const ProfileRepositoryIntake Profile = "repository-intake"

// Privilege is an effective PostgreSQL table privilege.
type Privilege string

const (
	PrivilegeSelect     Privilege = "SELECT"
	PrivilegeInsert     Privilege = "INSERT"
	PrivilegeUpdate     Privilege = "UPDATE"
	PrivilegeDelete     Privilege = "DELETE"
	PrivilegeTruncate   Privilege = "TRUNCATE"
	PrivilegeReferences Privilege = "REFERENCES"
	PrivilegeTrigger    Privilege = "TRIGGER"
	PrivilegeMaintain   Privilege = "MAINTAIN"
)

var tablePrivileges = []Privilege{
	PrivilegeSelect, PrivilegeInsert, PrivilegeUpdate, PrivilegeDelete,
	PrivilegeTruncate, PrivilegeReferences, PrivilegeTrigger, PrivilegeMaintain,
}

// TableRule fixes the effective privileges for one shipping table.
type TableRule struct {
	Table      string      `json:"table"`
	Privileges []Privilege `json:"privileges"`
}

// Manifest is the closed table capability set. Ownership, role membership,
// schema, function, sequence, and configuration authority are verified separately.
type Manifest struct {
	SchemaVersion string      `json:"schema_version"`
	Profile       Profile     `json:"profile"`
	Tables        []TableRule `json:"tables"`
	// TrustedRawDML is true when Go writers, not the table ACL, enforce the
	// admitted/pending values and exact-review routing of individual fields.
	TrustedRawDML   bool     `json:"trusted_raw_dml"`
	FunctionExecute []string `json:"function_execute,omitempty"`
}

// queryTables is intentionally target-native. A test compares this frozen list
// with migrations.requiredTables so a migration cannot silently expand authority.
var queryTables = []string{
	"admission_decisions",
	"canonical_contradiction_admission_decisions",
	"canonical_contradiction_proposals",
	"canonical_derivation_parents",
	"canonical_derivations",
	"canonical_endpoint_review_bindings",
	"canonical_graph_edges",
	"canonical_graph_nodes",
	"canonical_implements_admissions",
	"canonical_ordinary_admission_edge_bindings",
	"canonical_ordinary_admission_manifests",
	"canonical_ordinary_admission_node_bindings",
	"canonical_references_admissions",
	"canonical_source_claim_review_bindings",
	"canonical_supersession_admission_events",
	"canonical_supersession_admission_head",
	"canonical_supersession_lineages",
	"canonical_supersession_members",
	"canonical_supersession_replacement_targets",
	"detective_connector_inbox_deliveries",
	"detective_connector_inbox_processing_attempts",
	"detective_connector_inbox_processing_recovery_requests",
	"detective_connector_inbox_processing_requests",
	"detective_connector_inbox_processing_work",
	"detective_connector_inbox_receive_requests",
	"detective_mcp_read_collection_cycles",
	"detective_mcp_read_source_bindings",
	"detective_orchestration_runs",
	"detective_orchestration_steps",
	"detective_orchestration_transition_requests",
	"detective_periodic_source_cursors",
	"detective_periodic_source_cycles",
	"detective_planner_recommendation_consumptions",
	"detective_workspace_registration_requests",
	"detective_workspace_sources",
	"detective_workspaces",
	"evidence_ingestion_request_serializations",
	"external_source_intake_receipts",
	"extraction_attempts",
	"extraction_runs",
	"extraction_views",
	"extractor_definitions",
	"git_repository_source_streams",
	"proposal_batches",
	"proposal_occurrences",
	"repository_change_observation_requests",
	"repository_change_observations",
	"repository_delta_extractions",
	"repository_extraction_run_requests",
	"repository_extraction_work_claim_attempts",
	"repository_extraction_work_claim_requests",
	"repository_extraction_work_execution_repairs",
	"repository_extraction_work_execution_requests",
	"repository_extraction_work_expired_maintenance_tick_items",
	"repository_extraction_work_expired_maintenance_tick_requests",
	"repository_extraction_work_failure_policy_decisions",
	"repository_extraction_work_finish_requests",
	"repository_extraction_work_items",
	"repository_extraction_work_lease_renewal_requests",
	"repository_extraction_work_recovery_requests",
	"repository_extraction_work_retry_controller_tick_items",
	"repository_extraction_work_retry_controller_tick_requests",
	"repository_extraction_work_retry_decision_consumptions",
	"repository_extraction_work_retry_requests",
	"repository_extraction_work_schedule_requests",
	"repository_extraction_worker_tick_requests",
	"repository_generation_activation_requests",
	"repository_generation_proposal_reconciliations",
	"repository_generation_reconciliations",
	"repository_snapshot_intake_requests",
	"repository_snapshots",
	"repository_source_generations",
	"repository_source_heads",
	"repository_source_streams",
	"schema_migrations",
	"source_blobs",
	"source_claim_disposition_review_bindings",
	"source_file_snapshots",
	"source_intake_requests",
	"source_snapshots",
	"span_catalog_entries",
}

// ProfileNames returns implemented profile names only.
func ProfileNames() []string {
	return []string{string(ProfileQuery), string(ProfileIntake), string(ProfileSourceClaimReviewer), string(ProfileRelationReviewer), string(ProfileEndpointReviewer), string(ProfileRepositoryIntake)}
}

// ParseProfile accepts an exact implemented profile without normalization.
func ParseProfile(value string) (Profile, error) {
	if !slices.Contains(ProfileNames(), value) {
		return "", fmt.Errorf("database role profile must be one of %v", ProfileNames())
	}
	return Profile(value), nil
}

// BuildManifest returns a fresh copy of one shipping capability manifest.
func BuildManifest(profile Profile) (Manifest, error) {
	if _, err := ParseProfile(string(profile)); err != nil {
		return Manifest{}, err
	}
	rules := make([]TableRule, 0, len(queryTables))
	for _, table := range queryTables {
		privileges := []Privilege{PrivilegeSelect}
		if profile == ProfileEndpointReviewer {
			switch table {
			case "canonical_graph_nodes", "canonical_graph_edges", "admission_decisions", "canonical_ordinary_admission_manifests",
				"canonical_ordinary_admission_node_bindings", "canonical_ordinary_admission_edge_bindings",
				"canonical_derivations", "canonical_derivation_parents", "canonical_endpoint_review_bindings":
				privileges = append(privileges, PrivilegeInsert)
			case "proposal_occurrences":
				privileges = append(privileges, PrivilegeUpdate)
			}
		} else if profile == ProfileRepositoryIntake {
			switch table {
			case "source_blobs", "repository_snapshots", "source_file_snapshots", "repository_snapshot_intake_requests",
				"extractor_definitions", "extraction_runs", "repository_extraction_run_requests", "proposal_occurrences",
				"repository_source_generations":
				privileges = append(privileges, PrivilegeInsert)
			case "extraction_attempts", "proposal_batches", "repository_source_streams":
				privileges = append(privileges, PrivilegeInsert, PrivilegeUpdate)
			}
		} else if profile == ProfileIntake {
			privileges = append(privileges, intakeWrites(table)...)
		} else if profile == ProfileRelationReviewer {
			if table == "canonical_graph_edges" || table == "canonical_implements_admissions" || table == "canonical_references_admissions" {
				privileges = append(privileges, PrivilegeInsert)
			}
		} else if profile == ProfileSourceClaimReviewer {
			privileges = append(privileges, sourceClaimReviewerWrites(table)...)
		}
		slices.Sort(privileges)
		rules = append(rules, TableRule{Table: table, Privileges: privileges})
	}
	manifest := Manifest{SchemaVersion: PolicyVersion, Profile: profile, Tables: rules, TrustedRawDML: profile != ProfileQuery}
	if profile == ProfileEndpointReviewer {
		manifest.FunctionExecute = []string{"canonical_endpoint_lock_nodes_v1(text[])"}
	}
	return manifest, nil
}

func sourceClaimReviewerWrites(table string) []Privilege {
	switch table {
	case "canonical_graph_nodes", "canonical_graph_edges", "admission_decisions",
		"canonical_ordinary_admission_manifests", "canonical_ordinary_admission_node_bindings",
		"canonical_ordinary_admission_edge_bindings", "canonical_source_claim_review_bindings", "source_claim_disposition_review_bindings":
		return []Privilege{PrivilegeInsert}
	case "proposal_occurrences":
		return []Privilege{PrivilegeUpdate}
	default:
		return nil
	}
}

// This is the DML used by CaptureManualSource, CaptureExternalSource,
// IngestManualText, SubmitExtractorOutput, and their success/failure/replay paths
// in evidenceingestion/postgres.go. It is not the broader repository collector.
func intakeWrites(table string) []Privilege {
	switch table {
	case "extraction_attempts", "proposal_batches":
		return []Privilege{PrivilegeInsert, PrivilegeUpdate}
	case "source_blobs", "source_snapshots", "extraction_views", "span_catalog_entries",
		"source_intake_requests", "external_source_intake_receipts", "extractor_definitions",
		"extraction_runs", "proposal_occurrences":
		return []Privilege{PrivilegeInsert}
	default:
		return nil
	}
}

// Hash returns a stable credential-free digest.
func (m Manifest) Hash() (string, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encoding database role manifest: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func allTablePrivileges() []Privilege { return append([]Privilege(nil), tablePrivileges...) }
