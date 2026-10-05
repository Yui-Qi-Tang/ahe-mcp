package mcpcorerecords

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecorderBoundaryBeforeDatabase(t *testing.T) {
	b, err := NewBackend(new(pgxpool.Pool), runtimeauth.Principal{ID: "recorder"})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"recorded_by":"somebody"}`, `{"DECISION_BY":null}`, `{"request_id":"a","REQUEST_ID":"b"}`, `{"key":{"namespace":"a","namespace":"b"}}`, `{"unknown":"x"}`, `null`, `{} {}`} {
		if _, err := b.CallTool(context.Background(), "bind_canonical_proposition", json.RawMessage(args)); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	for _, name := range []string{"admit_pending_proposal", "submit_external_source", "get_proposition_members", "run_evidence_selector"} {
		if _, err := b.CallTool(context.Background(), name, []byte(`{}`)); err == nil {
			t.Fatalf("accepted tool %s", name)
		}
	}
	if _, err := NewBackend(new(pgxpool.Pool), runtimeauth.Principal{}); err == nil {
		t.Fatal("missing identity accepted")
	}
	for _, tool := range b.Tools() {
		fields := tool.InputSchema["properties"].(map[string]any)
		if _, ok := fields["recorded_by"]; ok {
			t.Fatal("identity exposed")
		}
		if _, ok := fields["decision_by"]; ok {
			t.Fatal("identity exposed")
		}
		if IsReadTool(tool.Name) || *tool.Annotations.ReadOnlyHint {
			t.Fatal("write classified as read")
		}
	}
	for _, tool := range ReadTools() {
		if !IsReadTool(tool.Name) || !*tool.Annotations.ReadOnlyHint {
			t.Fatal("read registry mismatch")
		}
	}
}
func TestCoreRecordsRoleCannotAdmitOrCollect(t *testing.T) {
	m, err := dbrole.BuildManifest(dbrole.ProfileCoreRecords)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]int{"canonical_propositions": 1, "canonical_proposition_bindings": 2, "canonical_proposition_binding_events": 1, "external_check_records": 1, "external_representation_records": 1, "external_check_representation_links": 1}
	for _, table := range m.Tables {
		n := 0
		for _, p := range table.Privileges {
			if p != dbrole.PrivilegeSelect {
				n++
				if p != dbrole.PrivilegeInsert && !(table.Table == "canonical_proposition_bindings" && p == dbrole.PrivilegeUpdate) {
					t.Fatalf("unexpected write %s %s", table.Table, p)
				}
			}
		}
		if n != expected[table.Table] {
			t.Fatalf("unexpected authority on %s", table.Table)
		}
		delete(expected, table.Table)
	}
	if len(expected) != 0 || len(m.FunctionExecute) != 0 {
		t.Fatal("incomplete or widened record role")
	}
}
