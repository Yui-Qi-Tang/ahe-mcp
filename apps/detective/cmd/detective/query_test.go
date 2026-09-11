package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

func TestQueryCLIPracticalDefaultsAndPlainOutput(t *testing.T) {
	for _, count := range []int{0, 1} {
		var stdout, stderr bytes.Buffer
		calls := 0
		search := func(ctx context.Context, command string, input ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
			calls++
			if ctx.Err() != nil || command != "/operator/query" || input.Question != "Richter earthquake" || input.Limit != 20 || input.LifecycleScope != "active" || input.AdmissionOutcome != "" {
				t.Fatal("query arguments changed")
			}
			if count == 0 {
				return ahemcp.PracticalQueryResult{Response: json.RawMessage(`{"matches":[]}`), ReturnedMatches: 0, FallbackAttempted: true}, nil
			}
			return ahemcp.PracticalQueryResult{Response: json.RawMessage(`{"matches":[{"statement_text":"候選","admission_outcome":"audit_only","source_refs":[{"quoted_text":"<source>","span_id":"span:S1"}],"retrieval_basis":{"surfaces":[{"surface":"extraction_views.rendered_content","status":"searched","matched_terms":["earthquake"],"missing_terms":["richter"],"matched_spans":[{"source_ref":{"quoted_text":"Additional context","span_id":"span:S2"}}]}]}}]}`), ReturnedMatches: count, FallbackAttempted: true}, nil
		}
		if err := runQueryContext(t.Context(), []string{"-query-command", "/operator/query", "-question", "Richter earthquake"}, &stdout, &stderr, search); err != nil || calls != 1 || stderr.Len() != 0 {
			t.Fatal(err)
		}
		expectedText := []string{"補查材料不是答案", "未新增待審", "不需 reject"}
		if count == 1 {
			expectedText = append(expectedText, "原引用", "\\u003csource\\u003e", "audit_only", "未命中字詞", "richter", "不是新增原引用", "Additional context")
		}
		for _, expected := range expectedText {
			if !strings.Contains(stdout.String(), expected) {
				t.Fatal("missing plain boundary", expected)
			}
		}
		if count == 0 && !strings.Contains(stdout.String(), "不代表資料不存在") {
			t.Fatal("empty result claimed absence")
		}
	}
}

func TestQueryCLIJSONPreservesFieldsAndExactFilters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	raw := json.RawMessage(`{"record_state":{"authority_status":"audit_only"},"source_refs":[{"quoted_text":"Source\u202e"}],"retrieval_basis":{"surface":"source"},"additive":9007199254740993}`)
	search := func(_ context.Context, _ string, input ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
		if input.SourceID != "exact-source" || input.AdmissionOutcome != "audit_only" || input.Limit != 3 || input.LifecycleScope != "active" {
			t.Fatal("filters changed")
		}
		return ahemcp.PracticalQueryResult{Response: raw}, nil
	}
	err := runQueryContext(t.Context(), []string{"-query-command", "/operator/query", "-question", "index", "-source-id", "exact-source", "-outcome", "audit_only", "-limit", "3", "-lifecycle", "active", "-format", "json"}, &stdout, &stderr, search)
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, stdout.Bytes()) != nil || !bytes.Equal(compact.Bytes(), raw) || stderr.Len() != 0 {
		t.Fatal("JSON changed", err, stdout.String())
	}
}

func TestQueryCLIInvalidFlagsNeverSearch(t *testing.T) {
	t.Setenv("DETECTIVE_AHE_QUERY_COMMAND", "/must-not-default")
	for _, args := range [][]string{nil, {"-question", "earthquake"}, {"-query-command", "/operator/query"}, {"-query-command", "/operator/query", "-question", "earthquake", "-question", "different"}, {"-query-command", "/operator/query", "-question", "earthquake", "-mode", "research"}, {"-query-command", "/operator/query", "-question", "earthquake", "-format", "html"}, {"-query-command", "/operator/query", "-question", "earthquake", "-timeout", "3m"}} {
		var stdout, stderr bytes.Buffer
		calls := 0
		search := func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
			calls++
			return ahemcp.PracticalQueryResult{}, errors.New("unexpected")
		}
		if err := runQueryContext(t.Context(), args, &stdout, &stderr, search); err == nil || calls != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatal("invalid request ran search", args, err)
		}
	}
}

func TestQueryCLIHelpDispatchIsOffline(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"query", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "grounded-evidence-brief-v7") || stderr.Len() != 0 {
		t.Fatal("help failed", err)
	}
}

func TestQueryCLIHanPolicyTextAndJSON(t *testing.T) {
	for _, preserved := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			var stdout, stderr bytes.Buffer
			anchors := []string{"太陽", "衛星"}
			question := "太陽閃焰影響了哪些衛星？"
			if preserved {
				anchors, question = []string{"影響"}, "影響"
			}
			policy := ahemcp.PracticalHanTermPolicy{Version: "han-auxiliary-anchor-v1", AuxiliaryTerms: []string{"影響"}, EligibleAnchorTerms: anchors, AuxiliaryOnlyQueryPreserved: preserved}
			raw, _ := json.Marshal(map[string]any{"matches": []any{}, "query_execution": map[string]any{"original_query": question, "practical_recovery": map[string]any{"han_term_policy": policy}}, "unknown_field": json.Number("9007199254740993")})
			search := func(context.Context, string, ahemcp.PracticalQueryInput) (ahemcp.PracticalQueryResult, error) {
				return ahemcp.PracticalQueryResult{Response: raw, HanTermPolicy: &policy}, nil
			}
			if err := runQueryContext(t.Context(), []string{"-query-command", "/operator/query", "-question", question, "-format", format}, &stdout, &stderr, search); err != nil || stderr.Len() != 0 {
				t.Fatal(err)
			}
			if format == "json" {
				var compact bytes.Buffer
				if json.Compact(&compact, stdout.Bytes()) != nil || !bytes.Equal(compact.Bytes(), raw) {
					t.Fatal("native JSON policy changed")
				}
				continue
			}
			for _, phrase := range []string{"輔助詞不可單獨作為中文補充命中", `本次輔助詞 ["影響"]`, "不攔截原本搜尋或英文命中", "不代表理解主題或語意支持", "不代表資料不存在", "未新增待審"} {
				if !strings.Contains(stdout.String(), phrase) {
					t.Fatalf("missing %q in %s", phrase, stdout.String())
				}
			}
			if strings.Contains(stdout.String(), "已保留廣查") != preserved {
				t.Fatal("broad-only query exception misrepresented")
			}
		}
	}
}
