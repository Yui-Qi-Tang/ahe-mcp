package mcpendpoints

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEndpointProfilesClosed(t *testing.T) {
	principal := runtimeauth.Principal{ID: "synthetic:reviewer"}
	for _, profile := range []string{ProfileRepositoryIntake, ProfileEndpointReviewer} {
		root, id := "", ""
		if profile == ProfileRepositoryIntake {
			root, id = "/synthetic/repository", "synthetic:repo"
		}
		b, err := NewBackend(&pgxpool.Pool{}, principal, profile, root, id)
		if err != nil {
			t.Fatal(err)
		}
		tools := b.Tools()
		if len(tools) != 2 {
			t.Fatal("profile must have exactly two tools")
		}
		for _, tool := range tools {
			if tool.InputSchema["additionalProperties"] != false {
				t.Fatal("open schema")
			}
		}
		frozen, _ := json.Marshal(b.Tools())
		tools[0].InputSchema["properties"] = nil
		again, _ := json.Marshal(b.Tools())
		if string(frozen) != string(again) {
			t.Fatal("discovery mutated server authority")
		}
		for _, name := range []string{"admit_pending_proposal", "submit_external_source", "activate_repository_generation", "admit_reviewed_implements"} {
			if _, err := b.CallTool(t.Context(), name, json.RawMessage("{}")); err == nil {
				t.Fatal("wrong profile dispatch")
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := b.CallTool(ctx, tools[0].Name, json.RawMessage("{}")); err != context.Canceled {
			t.Fatal("cancel ignored")
		}
	}
	for _, bad := range []struct{ profile, root, id string }{
		{"", "", ""}, {ProfileRepositoryIntake, "/", "repo"}, {ProfileRepositoryIntake, "relative", "repo"},
		{ProfileRepositoryIntake, "/repo", ""}, {ProfileEndpointReviewer, "/repo", "repo"},
	} {
		if _, err := NewBackend(&pgxpool.Pool{}, principal, bad.profile, bad.root, bad.id); err == nil {
			t.Fatal("invalid profile binding")
		}
	}
}
func TestEndpointRequestsRejectAuthorityAndAmbiguity(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}{}`, `{"reviewer_id":"caller"}`, `{"receipt":{}}`,
		`{"decision":"no","decision":"approved"}`, `{"decision":"no","DECISION":"approved"}`,
		`{"review":{"kind":"derived_spec","derivation":{"method":"a","method":"b"}}}`,
		`{"review":{"graph":[]}}`, `{"review":{"workspace_root":"/repo"}}`,
	} {
		var input evidenceingestion.ReviewedEndpointAdmissionInput
		if err := decodeRequest([]byte(raw), &input); err == nil {
			t.Fatalf("ambiguous authority accepted: %s", raw)
		}
	}
}
func TestEndpointProfileTableAndFunctionAuthority(t *testing.T) {
	endpointWrites := map[string][]dbrole.Privilege{
		"canonical_graph_nodes": {dbrole.PrivilegeInsert}, "canonical_graph_edges": {dbrole.PrivilegeInsert},
		"canonical_derivations": {dbrole.PrivilegeInsert}, "canonical_derivation_parents": {dbrole.PrivilegeInsert},
		"admission_decisions": {dbrole.PrivilegeInsert}, "canonical_ordinary_admission_manifests": {dbrole.PrivilegeInsert},
		"canonical_ordinary_admission_node_bindings": {dbrole.PrivilegeInsert}, "canonical_ordinary_admission_edge_bindings": {dbrole.PrivilegeInsert},
		"canonical_endpoint_review_bindings": {dbrole.PrivilegeInsert}, "proposal_occurrences": {dbrole.PrivilegeUpdate},
	}
	repositoryWrites := map[string][]dbrole.Privilege{}
	for _, name := range []string{"source_blobs", "repository_snapshots", "source_file_snapshots", "repository_snapshot_intake_requests",
		"extractor_definitions", "extraction_runs", "repository_extraction_run_requests", "proposal_occurrences", "repository_source_streams", "repository_source_generations"} {
		repositoryWrites[name] = []dbrole.Privilege{dbrole.PrivilegeInsert}
	}
	for _, name := range []string{"extraction_attempts", "proposal_batches", "repository_source_streams"} {
		repositoryWrites[name] = []dbrole.Privilege{dbrole.PrivilegeInsert, dbrole.PrivilegeUpdate}
	}
	for _, tc := range []struct {
		profile dbrole.Profile
		writes  map[string][]dbrole.Privilege
	}{{dbrole.ProfileEndpointReviewer, endpointWrites}, {dbrole.ProfileRepositoryIntake, repositoryWrites}} {
		m, err := dbrole.BuildManifest(tc.profile)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range m.Tables {
			var actual []dbrole.Privilege
			for _, p := range rule.Privileges {
				if p != dbrole.PrivilegeSelect {
					actual = append(actual, p)
				}
			}
			if !reflect.DeepEqual(actual, tc.writes[rule.Table]) {
				t.Fatalf("unexpected %s authority: %s %v", tc.profile, rule.Table, actual)
			}
		}
		if tc.profile == dbrole.ProfileEndpointReviewer {
			if !reflect.DeepEqual(m.FunctionExecute, []string{"canonical_endpoint_lock_nodes_v1(text[])"}) {
				t.Fatal("lock-only function boundary differs")
			}
		} else if len(m.FunctionExecute) != 0 {
			t.Fatal("repository intake has function authority")
		}
	}
}
