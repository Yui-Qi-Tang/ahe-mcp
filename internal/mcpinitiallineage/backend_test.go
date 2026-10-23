package mcpinitiallineage

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInitialLineageProfileClosed(t *testing.T) {
	b, err := NewBackend(&pgxpool.Pool{}, runtimeauth.Principal{ID: "TEST APPROVAL STUB"})
	if err != nil {
		t.Fatal(err)
	}
	tools := b.Tools()
	if len(tools) != 2 || tools[0].Name != ToolReview || tools[1].Name != ToolAdmit {
		t.Fatal("unexpected tool inventory")
	}
	before, _ := json.Marshal(tools)
	tools[0].InputSchema["properties"] = nil
	after, _ := json.Marshal(b.Tools())
	if string(before) != string(after) {
		t.Fatal("discovery mutated backend schema")
	}
	for _, name := range []string{"admit_pending_proposal", "admit_reviewed_supersession", "apply_reviewed_withdrawal", "activate_repository_source_generation"} {
		if _, err := b.CallTool(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("exposed %s", name)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.CallTool(ctx, ToolReview, json.RawMessage(`{}`)); err != context.Canceled {
		t.Fatal("cancellation lost", err)
	}
	m, err := dbrole.BuildManifest(dbrole.ProfileInitialLineageReviewer)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range m.Tables {
		for _, privilege := range rule.Privileges {
			if privilege == dbrole.PrivilegeSelect {
				continue
			}
			if rule.Table == "canonical_initial_lineage_registrations" && privilege == dbrole.PrivilegeInsert {
				continue
			}
			t.Fatalf("unexpected write capability: %s %s", rule.Table, privilege)
		}
	}
	if !reflect.DeepEqual(m.FunctionExecute, []string{"evidence_governance_lock_v1(regclass)"}) {
		t.Fatal("unexpected helper capability", m.FunctionExecute)
	}
}

func TestInitialLineageRejectsCallerAuthority(t *testing.T) {
	b, err := NewBackend(&pgxpool.Pool{}, runtimeauth.Principal{ID: "TEST APPROVAL STUB"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `[]`, `{}{}`, `{"reviewer_id":"caller"}`, `{"decision":"approved","Decision":"reject"}`, `{"review":{"basis":{"slot_id":"a","slot_id":"b"}}}`, `{"review":{"anchor":{}}}`, `{"receipt":{}}`, `{"Request_ID":"alias"}`} {
		if _, err := b.CallTool(t.Context(), ToolAdmit, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted caller authority: %s", raw)
		}
	}
}
