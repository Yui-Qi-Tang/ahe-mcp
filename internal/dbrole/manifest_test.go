package dbrole

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

func TestQueryManifestMatchesShippingMigrationInventory(t *testing.T) {
	// Read the shipping migration inventory, not Core's inventory. This test
	// intentionally fails when a migration adds a table without policy review.
	file, err := parser.ParseFile(token.NewFileSet(), "../../migrations/migrations.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"schema_migrations"}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "requiredTables" {
			return true
		}
		found = true
		literal, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatal("requiredTables must remain an explicit inventory")
		}
		for _, element := range literal.Elts {
			value, ok := element.(*ast.BasicLit)
			if !ok {
				t.Fatal("requiredTables contains a nonliteral table")
			}
			name, err := strconv.Unquote(value.Value)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, name)
		}
		return false
	})
	if !found {
		t.Fatal("shipping migration inventory not found")
	}
	slices.Sort(want)
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rule := range manifest.Tables {
		got = append(got, rule.Table)
		if !slices.Equal(rule.Privileges, []Privilege{PrivilegeSelect}) {
			t.Fatalf("query privileges on %s = %v", rule.Table, rule.Privileges)
		}
	}
	if len(got) != 78 || !slices.Equal(got, want) {
		t.Fatalf("shipping migration manifest = %v, want %v", got, want)
	}
	if len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Fatal("query manifest has duplicate tables")
	}
}

func TestQueryManifestIsIndependentAndWriterProfilesFailClosed(t *testing.T) {
	first, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := first.Hash()
	if err != nil {
		t.Fatal(err)
	}
	first.Tables[0].Table = "caller_mutation"
	first.Tables[1].Privileges[0] = PrivilegeUpdate
	second, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	gotHash, err := second.Hash()
	if err != nil || gotHash != wantHash {
		t.Fatalf("fresh manifest digest = %q, error %v, want %q", gotHash, err, wantHash)
	}
	for _, profile := range []Profile{"", " query", "Query", "ingest-collector", "ingest-admission-reviewer", "ingest-trusted-local"} {
		if _, err := BuildManifest(profile); err == nil {
			t.Fatalf("unimplemented profile %q accepted", profile)
		}
	}
}

func TestIntakeManifestHasOnlySourceAndPendingWrites(t *testing.T) {
	manifest, err := BuildManifest(ProfileIntake)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Privilege{
		"source_blobs":                    {PrivilegeInsert},
		"source_snapshots":                {PrivilegeInsert},
		"extraction_views":                {PrivilegeInsert},
		"span_catalog_entries":            {PrivilegeInsert},
		"source_intake_requests":          {PrivilegeInsert},
		"external_source_intake_receipts": {PrivilegeInsert},
		"extractor_definitions":           {PrivilegeInsert},
		"extraction_runs":                 {PrivilegeInsert},
		"extraction_attempts":             {PrivilegeInsert, PrivilegeUpdate},
		"proposal_batches":                {PrivilegeInsert, PrivilegeUpdate},
		"proposal_occurrences":            {PrivilegeInsert},
	}
	if !manifest.TrustedRawDML || len(manifest.Tables) != 78 {
		t.Fatalf("unexpected intake manifest: %+v", manifest)
	}
	for _, rule := range manifest.Tables {
		var writes []Privilege
		for _, privilege := range rule.Privileges {
			if privilege != PrivilegeSelect {
				writes = append(writes, privilege)
			}
		}
		if !slices.Equal(writes, want[rule.Table]) {
			t.Fatalf("intake writes on %s = %v, want %v", rule.Table, writes, want[rule.Table])
		}
		delete(want, rule.Table)
	}
	if len(want) != 0 {
		t.Fatalf("intake manifest omits %v", want)
	}
}

func TestSourceClaimReviewerManifestHasOnlyExactReviewedWrites(t *testing.T) {
	manifest, err := BuildManifest(ProfileSourceClaimReviewer)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Privilege{
		"canonical_graph_nodes":                      {PrivilegeInsert},
		"canonical_graph_edges":                      {PrivilegeInsert},
		"admission_decisions":                        {PrivilegeInsert},
		"canonical_ordinary_admission_manifests":     {PrivilegeInsert},
		"canonical_ordinary_admission_node_bindings": {PrivilegeInsert},
		"canonical_ordinary_admission_edge_bindings": {PrivilegeInsert},
		"canonical_source_claim_review_bindings":     {PrivilegeInsert},
		"source_claim_disposition_review_bindings":   {PrivilegeInsert},
		"proposal_occurrences":                       {PrivilegeUpdate},
	}
	// The profile narrows tables, not human authentication or all possible raw DML.
	if !manifest.TrustedRawDML || len(manifest.Tables) != 78 {
		t.Fatal("reviewer trust boundary or table inventory changed")
	}
	for _, rule := range manifest.Tables {
		var writes []Privilege
		for _, privilege := range rule.Privileges {
			if privilege != PrivilegeSelect {
				writes = append(writes, privilege)
			}
		}
		if !slices.Equal(writes, want[rule.Table]) {
			t.Fatalf("reviewer writes on %s = %v, want %v", rule.Table, writes, want[rule.Table])
		}
		delete(want, rule.Table)
	}
	if len(want) != 0 {
		t.Fatalf("reviewer manifest omits %v", want)
	}
}
