package dbrole

import (
	"reflect"
	"slices"
	"testing"
)

func TestHistoricalReviewAuthorityDoesNotCrossProfiles(t *testing.T) {
	writes := map[string]Privilege{
		"admission_decisions": PrivilegeInsert, "canonical_graph_nodes": PrivilegeInsert,
		"canonical_graph_edges": PrivilegeInsert, "canonical_derivations": PrivilegeInsert,
		"canonical_derivation_parents": PrivilegeInsert, "canonical_ordinary_admission_manifests": PrivilegeInsert,
		"canonical_ordinary_admission_node_bindings": PrivilegeInsert, "canonical_ordinary_admission_edge_bindings": PrivilegeInsert,
		"canonical_historical_derivation_reviews": PrivilegeInsert, "proposal_occurrences": PrivilegeUpdate,
	}
	for _, name := range ProfileNames() {
		profile := Profile(name)
		m, err := BuildManifest(profile)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range m.Tables {
			if rule.Table == "canonical_historical_derivation_reviews" && slices.Contains(rule.Privileges, PrivilegeInsert) != (profile == ProfileHistoricalDerivationReviewer) {
				t.Fatal("historical writer authority leaked", profile)
			}
			if profile == ProfileHistoricalDerivationReviewer {
				want := []Privilege{PrivilegeSelect}
				if p, ok := writes[rule.Table]; ok {
					want = append(want, p)
				}
				slices.Sort(want)
				if !slices.Equal(rule.Privileges, want) {
					t.Fatalf("unexpected historical authority on %s: %v", rule.Table, rule.Privileges)
				}
			}
		}
		if profile == ProfileHistoricalDerivationReviewer && !reflect.DeepEqual(m.FunctionExecute, []string{"canonical_endpoint_lock_nodes_v1(text[])", "evidence_governance_lock_v1(regclass)"}) {
			t.Fatal("historical lock-only function authority differs")
		}
	}
}
