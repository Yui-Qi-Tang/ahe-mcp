package mcpsupersession

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSupersessionProfileClosed(t *testing.T) {
	b, err := NewBackend(&pgxpool.Pool{}, runtimeauth.Principal{ID: "synthetic:reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	tools := b.Tools()
	if len(tools) != 2 || tools[0].Name != ToolReview || tools[1].Name != ToolAdmit {
		t.Fatal("profile inventory differs")
	}
	for _, name := range []string{"admit_pending_supersession", "admit_pending_proposal", "record_reviewed_endpoint_disposition", "activate_repository_source_generation"} {
		if _, err := b.CallTool(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatal("unauthorized writer exposed")
		}
	}
	frozen, _ := json.Marshal(tools)
	tools[0].InputSchema["properties"] = nil
	again, _ := json.Marshal(b.Tools())
	if string(frozen) != string(again) {
		t.Fatal("caller mutated discovery")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.CallTool(ctx, ToolReview, json.RawMessage(`{}`)); err != context.Canceled {
		t.Fatal("cancel ignored")
	}
	manifest, err := dbrole.BuildManifest(dbrole.ProfileSupersessionReviewer)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"canonical_graph_nodes": true, "canonical_graph_edges": true, "admission_decisions": true, "canonical_supersession_lineages": true, "canonical_supersession_admission_events": true, "canonical_supersession_members": true, "canonical_supersession_replacement_targets": true, "supersession_review_bindings": true}
	for _, rule := range manifest.Tables {
		for _, p := range rule.Privileges {
			if p == dbrole.PrivilegeSelect {
				continue
			}
			if p == dbrole.PrivilegeInsert && allowed[rule.Table] {
				continue
			}
			if p == dbrole.PrivilegeUpdate && (rule.Table == "proposal_occurrences" || rule.Table == "canonical_supersession_admission_head") {
				continue
			}
			t.Fatalf("unexpected supersession privilege %s %s", rule.Table, p)
		}
	}
	if len(manifest.FunctionExecute) != 2 || manifest.FunctionExecute[0] != "canonical_supersession_lock_targets_v1(text[])" {
		t.Fatal("internal helpers exposed")
	}
}
func TestSupersessionRequestAuthorityRejected(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}{}`, `{"reviewer_id":"caller"}`, `{"decision":"reject","DECISION":"approved"}`, `{"review":{"basis":{"slot_id":"a","slot_id":"b"}}}`, `{"review":{"winner":"canon-node:a"}}`, `{"receipt":{}}`} {
		var input evidenceingestion.ReviewedSupersessionInput
		if err := decodeRequest([]byte(raw), &input); err == nil {
			t.Fatalf("ambiguous authority accepted: %s", raw)
		}
	}
}
