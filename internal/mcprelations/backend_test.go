package mcprelations

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

func TestRelationReviewerClosedCapabilitySet(t *testing.T) {
	b := &Backend{principal: runtimeauth.Principal{ID: "test:reviewer"}}
	if _, err := NewBackend(nil, b.principal); err == nil {
		t.Fatal("nil pool accepted")
	}
	tools := b.Tools()
	if len(tools) != 4 {
		t.Fatal("relation reviewer must expose four tools")
	}
	expected := []string{ToolGetImplementsReview, ToolAdmitReviewedImplements, ToolGetReferencesReview, ToolAdmitReviewedReferences}
	for i, tool := range tools {
		if tool.Name != expected[i] || tool.InputSchema["additionalProperties"] != false {
			t.Fatal("open or altered schema")
		}
	}
	snapshot, _ := json.Marshal(b.Tools())
	tools[0].InputSchema["properties"] = nil
	tools[0].Name = "arbitrary_writer"
	unchanged, _ := json.Marshal(b.Tools())
	if string(snapshot) != string(unchanged) {
		t.Fatal("caller mutated capability schema")
	}
	for _, name := range []string{"admit_reviewed_source_claim", "submit_external_source", "admit_pending_proposal", "capture_git_repository_snapshot", "unknown"} {
		if _, err := b.CallTool(context.Background(), name, json.RawMessage("{}")); err == nil {
			t.Fatal("non-relation operation dispatched")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.CallTool(ctx, ToolGetReferencesReview, json.RawMessage("{}")); err != context.Canceled {
		t.Fatal("cancelled review was not stopped")
	}
}

func TestRelationApprovalIsExplicitAndIdentityNotCallerControlled(t *testing.T) {
	for _, decision := range []string{"", "admit", "reject", "audit_only", "Approved", "approved "} {
		if err := validateApproval("request", decision, "reason"); err == nil {
			t.Fatalf("accepted %q", decision)
		}
	}
	for _, pair := range [][2]string{{"", "reason"}, {" request", "reason"}, {"request", ""}, {"request", " reason"}, {"request", strings.Repeat("r", 2001)}, {"request", "reason\x00"}} {
		if err := validateApproval(pair[0], "approved", pair[1]); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	if err := validateApproval("request", "approved", "Synthetic review."); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		`{}` + `{}`, `null`, `[]`, `{"decision":"reject","decision":"approved"}`,
		`{"decision":"reject","DECISION":"approved"}`, `{"reviewer_id":"caller"}`,
		`{"receipt":{}}`, `{"review":{"reference":{"token":"a","token":"b"}}}`,
		`{"review":{"unknown":true}}`,
		"{" + strings.Repeat(" ", 1<<20) + "}", string([]byte{'{', 0xff, '}'}),
	} {
		var req ReferencesAdmissionRequest
		if err := decodeRequest([]byte(data), &req); err == nil {
			t.Fatalf("ambiguous request accepted: %.100s", data)
		}
	}
}

func TestRelationManifestOnlyAppendsEdgesAndReceipts(t *testing.T) {
	m, err := dbrole.BuildManifest(dbrole.ProfileRelationReviewer)
	if err != nil || !m.TrustedRawDML {
		t.Fatal("missing explicit trusted native-writer boundary")
	}
	want := map[string]bool{"canonical_graph_edges": true, "canonical_implements_admissions": true, "canonical_references_admissions": true}
	found := map[string]bool{}
	for _, rule := range m.Tables {
		expected := []dbrole.Privilege{dbrole.PrivilegeSelect}
		if want[rule.Table] {
			expected = []dbrole.Privilege{dbrole.PrivilegeInsert, dbrole.PrivilegeSelect}
			found[rule.Table] = true
		}
		if !reflect.DeepEqual(rule.Privileges, expected) {
			t.Fatalf("unexpected relation authority on %s: %v", rule.Table, rule.Privileges)
		}
	}
	if !reflect.DeepEqual(want, found) {
		t.Fatal("missing relation table manifest")
	}
}
