package mcpcontradictions

import (
	"encoding/json"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestContradictionProfileAuthority(t *testing.T) {
	b, err := NewBackend(&pgxpool.Pool{}, runtimeauth.Principal{ID: "synthetic-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	tools := b.Tools()
	if len(tools) != 2 || tools[0].Name != ToolReview || tools[1].Name != ToolApply {
		t.Fatal("unexpected capabilities")
	}
	for _, name := range []string{"admit_pending_proposal", "admit_pending_canonical_contradiction", "submit_canonical_contradiction_proposal", "admit_reviewed_implements", "admit_reviewed_supersession", "activate_repository_source_generation"} {
		if _, err = b.CallTool(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatal("other writer exposed")
		}
	}
	frozen, _ := json.Marshal(tools)
	tools[0].InputSchema["properties"] = nil
	again, _ := json.Marshal(b.Tools())
	if string(frozen) != string(again) {
		t.Fatal("mutable discovery")
	}
	manifest, err := dbrole.BuildManifest(dbrole.ProfileContradictionReviewer)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range manifest.Tables {
		for _, p := range rule.Privileges {
			if p != dbrole.PrivilegeSelect && !(rule.Table == "canonical_contradiction_review_bindings" && p == dbrole.PrivilegeInsert) {
				t.Fatal("unexpected write authority", rule.Table, p)
			}
		}
	}
	if len(manifest.FunctionExecute) != 1 || manifest.FunctionExecute[0] != "evidence_governance_lock_v1(regclass)" {
		t.Fatal("unexpected helper authority")
	}
}
func TestContradictionRequestRejectsCallerAuthority(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}{}`, `{"reviewer_id":"caller"}`, `{"decision":"reject","DECISION":"approved"}`, `{"review":{"conditions":[],"conditions":["changed"]}}`, `{"review":{"relation":"contradicts","admit":true}}`, `{"expected_ſubject":"alias"}`, `{"decision_by":"caller"}`} {
		var req evidenceingestion.ReviewedContradictionInput
		if err := decodeRequest([]byte(raw), &req); err == nil {
			t.Fatalf("ambiguous/caller authority accepted: %s", raw)
		}
	}
	req := evidenceingestion.ReviewedContradictionInput{RequestID: "decision", ExpectedSubject: "subject", Decision: "approved", DecisionReason: "synthetic", Review: evidenceingestion.ContradictionReviewRequest{ProposalRequestID: "pair", NodeAID: "canon-node:a", NodeBID: "canon-node:b", Rationale: "conflict", Scope: "test", Conditions: []string{}, ProducerName: "test", ProducerVersion: "v1", Relation: "contradicts"}}
	raw, _ := json.Marshal(req)
	var got evidenceingestion.ReviewedContradictionInput
	if err := decodeRequest(raw, &got); err != nil {
		t.Fatal("native request failed round-trip", err)
	}
}
